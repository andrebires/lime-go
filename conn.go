package lime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Conn has one serialized reader and one serialized writer. No operation launches
// a goroutine per envelope. Cancellation closes the socket and is terminal.
// Always Close when the owning session loop ends. No automatic retry or resume.
type Conn struct {
	ws        *websocket.Conn
	limits    Limits
	readGate  chan struct{}
	writeGate chan struct{}
	closeOnce sync.Once
	closeErr  error
	buf       []byte
}

func NewConn(ws *websocket.Conn, l Limits) (*Conn, error) {
	l, err := l.normalized()
	if err != nil {
		return nil, err
	}
	if ws == nil {
		return nil, errors.New("WebSocket required")
	}
	ws.SetReadLimit(int64(l.FrameBytes))
	return &Conn{ws: ws, limits: l, buf: make([]byte, 0, 1024), readGate: make(chan struct{}, 1), writeGate: make(chan struct{}, 1)}, nil
}
func Dial(ctx context.Context, url string, header http.Header, l Limits) (*Conn, error) {
	if _, err := l.normalized(); err != nil {
		return nil, err
	}
	d := websocket.Dialer{Subprotocols: []string{"lime"}, HandshakeTimeout: 10 * time.Second}
	ws, resp, err := d.DialContext(ctx, url, header)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, fmt.Errorf("dial LIME: %w", err)
	}
	if ws.Subprotocol() != "lime" {
		_ = ws.Close()
		return nil, errors.New("server did not select lime subprotocol")
	}
	return NewConn(ws, l)
}

// Upgrade retains gorilla's same-origin default. HTTP authentication and origin
// policy are owned by the caller; this helper does not treat headers as identity.
func Upgrade(w http.ResponseWriter, r *http.Request, l Limits) (*Conn, error) {
	if _, err := l.normalized(); err != nil {
		return nil, err
	}
	offered := false
	for _, s := range websocket.Subprotocols(r) {
		if s == "lime" {
			offered = true
		}
	}
	if !offered {
		http.Error(w, "lime subprotocol required", http.StatusBadRequest)
		return nil, errors.New("lime subprotocol required")
	}
	u := websocket.Upgrader{Subprotocols: []string{"lime"}, ReadBufferSize: 4096, WriteBufferSize: 4096}
	ws, err := u.Upgrade(w, r, nil)
	if err != nil {
		return nil, err
	}
	return NewConn(ws, l)
}
func (c *Conn) Close() error { c.closeOnce.Do(func() { c.closeErr = c.ws.Close() }); return c.closeErr }
func (c *Conn) Send(ctx context.Context, e Envelope) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case c.writeGate <- struct{}{}:
		defer func() { <-c.writeGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(e.Content) > c.limits.FrameBytes || len(e.Resource) > c.limits.FrameBytes || len(e.Metadata) > c.limits.FrameBytes || len(e.Authentication) > c.limits.FrameBytes {
		return errors.New("frame limit exceeded")
	}
	data, err := Append(c.buf[:0], e)
	if err != nil {
		return err
	}
	if len(data) > c.limits.FrameBytes {
		return errors.New("frame limit exceeded")
	}
	c.buf = data
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err = c.ws.SetWriteDeadline(deadline); err == nil {
		err = c.ws.WriteMessage(websocket.TextMessage, data)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("send envelope: %w", err)
	}
	return nil
}
func (c *Conn) Receive(ctx context.Context) (Envelope, error) {
	if err := ctx.Err(); err != nil {
		return Envelope{}, err
	}
	select {
	case c.readGate <- struct{}{}:
		defer func() { <-c.readGate }()
	case <-ctx.Done():
		return Envelope{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return Envelope{}, err
	}
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	deadline := time.Time{}
	if d, ok := ctx.Deadline(); ok {
		deadline = d
	}
	if err := c.ws.SetReadDeadline(deadline); err != nil {
		return Envelope{}, err
	}
	kind, data, err := c.ws.ReadMessage()
	if ctx.Err() != nil {
		return Envelope{}, ctx.Err()
	}
	if err != nil {
		return Envelope{}, fmt.Errorf("receive envelope: %w", err)
	}
	if kind != websocket.TextMessage {
		_ = c.Close()
		return Envelope{}, errors.New("LIME requires JSON text frames")
	}
	e, err := Decode(data)
	if err != nil {
		_ = c.Close()
		return Envelope{}, fmt.Errorf("decode frame: %w", err)
	}
	return e, nil
}
func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	var out [36]byte
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], b[10:16])
	return string(out[:]), nil
}

type PlainAuthentication struct {
	Password string `json:"password"`
}

// Authenticator verifies existing LIME plain credentials and returns a bound node.
// It must also validate authorized scope and transport security for its deployment.
type Authenticator func(context.Context, string, []byte) (string, error)
type ServerOptions struct {
	LocalNode         string
	TransportIdentity string
	Authenticate      Authenticator
}
type SessionInfo struct{ ID, Local, Remote string }

func failSession(ctx context.Context, c *Conn, id string, code int, description string) error {
	err := c.Send(ctx, Envelope{ID: id, State: "failed", Reason: &Reason{Code: code, Description: description}})
	_ = c.Close()
	return errors.Join(errors.New(description), err)
}

// AcceptSession handles only establishment. Callers route/authorize operations
// against the returned bound identity and reject mismatching from/pp claims.
func AcceptSession(ctx context.Context, c *Conn, options ServerOptions) (SessionInfo, error) {
	e, err := c.Receive(ctx)
	if err != nil {
		return SessionInfo{}, err
	}
	kind, _ := e.Kind()
	if kind != Session || e.State != "new" {
		return SessionInfo{}, failSession(ctx, c, "", InvalidInput, "new session required")
	}
	if e.Version != 2 {
		return SessionInfo{}, failSession(ctx, c, "", UnsupportedVersion, "version 2 required")
	}
	id, err := NewID()
	if err != nil {
		return SessionInfo{}, err
	}
	node := options.TransportIdentity
	if node == "" {
		if options.Authenticate == nil {
			return SessionInfo{}, failSession(ctx, c, id, AuthenticationFailed, "authentication required")
		}
		if err = c.Send(ctx, Envelope{ID: id, State: "authenticating", SchemeOptions: []string{"plain"}}); err != nil {
			return SessionInfo{}, err
		}
		e, err = c.Receive(ctx)
		if err != nil {
			return SessionInfo{}, err
		}
		if e.State != "authenticating" || e.ID != id || e.Scheme != "plain" || e.From == "" || e.PP != "" {
			return SessionInfo{}, failSession(ctx, c, id, AuthenticationFailed, "invalid authentication exchange")
		}
		var auth PlainAuthentication
		dec := json.NewDecoder(bytes.NewReader(e.Authentication))
		dec.DisallowUnknownFields()
		if err = dec.Decode(&auth); err != nil {
			return SessionInfo{}, failSession(ctx, c, id, AuthenticationFailed, "invalid plain credentials")
		}
		password, decodeErr := base64.StdEncoding.DecodeString(auth.Password)
		if decodeErr != nil {
			return SessionInfo{}, failSession(ctx, c, id, AuthenticationFailed, "invalid plain password")
		}
		node, err = options.Authenticate(ctx, e.From, password)
		clear(password)
		if err != nil || node == "" {
			return SessionInfo{}, failSession(ctx, c, id, AuthenticationFailed, "authentication rejected")
		}
	}
	if err = c.Send(ctx, Envelope{ID: id, From: options.LocalNode, To: node, State: "established"}); err != nil {
		return SessionInfo{}, err
	}
	return SessionInfo{ID: id, Local: options.LocalNode, Remote: node}, nil
}

// EstablishSession sends version 2 without alias/capability setup round trips.
// auth is only used when the server requests the historical plain scheme.
func EstablishSession(ctx context.Context, c *Conn, node string, password []byte) (SessionInfo, error) {
	if err := c.Send(ctx, Envelope{State: "new", Version: 2}); err != nil {
		return SessionInfo{}, err
	}
	e, err := c.Receive(ctx)
	if err != nil {
		return SessionInfo{}, err
	}
	if e.State == "authenticating" {
		if node == "" || len(password) == 0 {
			return SessionInfo{}, errors.New("plain credentials required")
		}
		supported := false
		for _, s := range e.SchemeOptions {
			if s == "plain" {
				supported = true
			}
		}
		if !supported {
			return SessionInfo{}, errors.New("server does not support plain")
		}
		auth, _ := json.Marshal(PlainAuthentication{Password: base64.StdEncoding.EncodeToString(password)})
		if err = c.Send(ctx, Envelope{ID: e.ID, From: node, State: "authenticating", Scheme: "plain", Authentication: auth}); err != nil {
			return SessionInfo{}, err
		}
		e, err = c.Receive(ctx)
		if err != nil {
			return SessionInfo{}, err
		}
	}
	if e.State != "established" || e.ID == "" || e.To == "" {
		_ = c.Close()
		return SessionInfo{}, errors.New("session not established")
	}
	return SessionInfo{ID: e.ID, Local: e.To, Remote: e.From}, nil
}

// FinishSession performs graceful lifecycle termination. It must own the receive
// loop while awaiting finished; a generic application can instead handle that
// envelope in its existing dispatcher.
func FinishSession(ctx context.Context, c *Conn, id string) error {
	if err := c.Send(ctx, Envelope{ID: id, State: "finishing"}); err != nil {
		return err
	}
	e, err := c.Receive(ctx)
	if err == nil && (e.State != "finished" || e.ID != id) {
		err = errors.New("finished session confirmation required")
	}
	closeErr := c.Close()
	return errors.Join(err, closeErr)
}
