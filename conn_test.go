package lime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func wsServer(t *testing.T, fn func(*Conn)) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Upgrade(w, r, Limits{})
		if err != nil {
			return
		}
		defer c.Close()
		fn(c)
	}))
	t.Cleanup(s.Close)
	return s
}
func dialTest(t *testing.T, s *httptest.Server) *Conn {
	t.Helper()
	c, err := Dial(context.Background(), "ws"+strings.TrimPrefix(s.URL, "http"), nil, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}
func timeout(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}
func TestSessionPaths(t *testing.T) {
	for _, transport := range []bool{true, false} {
		t.Run(map[bool]string{true: "http auth", false: "fallback plain"}[transport], func(t *testing.T) {
			infoCh := make(chan SessionInfo, 1)
			errCh := make(chan error, 1)
			s := wsServer(t, func(c *Conn) {
				options := ServerOptions{}
				if transport {
					options.TransportIdentity = "alice"
				} else {
					options.Authenticate = func(ctx context.Context, node string, password []byte) (string, error) {
						if node != "alice" || string(password) != "secret" {
							return "", errors.New("credentials")
						}
						return "alice", nil
					}
				}
				info, err := AcceptSession(timeout(t), c, options)
				if err != nil {
					errCh <- err
					return
				}
				infoCh <- info
				e, err := c.Receive(timeout(t))
				if err != nil {
					errCh <- err
					return
				}
				if e.State != "finishing" || e.ID != info.ID {
					errCh <- errors.New("finishing")
					return
				}
				errCh <- c.Send(timeout(t), Envelope{ID: info.ID, State: "finished"})
			})
			c := dialTest(t, s)
			info, err := EstablishSession(timeout(t), c, "alice", []byte("secret"))
			if err != nil {
				t.Fatal(err)
			}
			serverInfo := <-infoCh
			if info.ID != serverInfo.ID || info.Local != "alice" || serverInfo.Remote != "alice" {
				t.Fatal(info, serverInfo)
			}
			if err = FinishSession(timeout(t), c, info.ID); err != nil {
				t.Fatal(err)
			}
			if err = <-errCh; err != nil {
				t.Fatal(err)
			}
			if err = c.Close(); err != nil {
				t.Fatal("idempotent close", err)
			}
		})
	}
}
func TestSessionRejection(t *testing.T) {
	cases := []struct {
		name    string
		first   Envelope
		auth    Envelope
		options ServerOptions
		code    int
	}{
		{name: "missing version", first: Envelope{State: "new"}, options: ServerOptions{TransportIdentity: "alice"}, code: UnsupportedVersion},
		{name: "unsupported version", first: Envelope{State: "new", Version: 3}, options: ServerOptions{TransportIdentity: "alice"}, code: UnsupportedVersion},
		{name: "wrong first family", first: Envelope{Type: "text", Content: []byte(`"hello"`)}, code: InvalidInput},
		{name: "no implicit guest", first: Envelope{State: "new", Version: 2}, code: AuthenticationFailed},
	}
	for _, auth := range []Envelope{
		{State: "authenticating", From: "alice", Scheme: "plain", Authentication: []byte(`{"password":"eA=="}`)},
		{State: "authenticating", From: "alice", Scheme: "other", Authentication: []byte(`{"password":"eA=="}`)},
		{State: "authenticating", From: "alice", Scheme: "plain", Authentication: []byte(`{"password":"*"}`)},
		{State: "authenticating", From: "alice", Scheme: "plain", Authentication: []byte(`{"password":"eA==","extra":1}`)},
		{State: "authenticating", From: "alice", Scheme: "plain", Authentication: []byte(`null`)},
		{State: "authenticating", From: "alice", PP: "mallory", Scheme: "plain", Authentication: []byte(`{"password":"eA=="}`)},
		{State: "finished", From: "alice"},
	} {
		cases = append(cases, struct {
			name    string
			first   Envelope
			auth    Envelope
			options ServerOptions
			code    int
		}{name: "bad auth " + string(auth.Authentication) + auth.Scheme + auth.PP + auth.State, first: Envelope{State: "new", Version: 2}, auth: auth, options: ServerOptions{Authenticate: func(context.Context, string, []byte) (string, error) { return "", errors.New("denied") }}, code: AuthenticationFailed})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			done := make(chan error, 1)
			s := wsServer(t, func(c *Conn) { _, err := AcceptSession(timeout(t), c, tc.options); done <- err })
			c := dialTest(t, s)
			if err := c.Send(timeout(t), tc.first); err != nil {
				t.Fatal(err)
			}
			e, err := c.Receive(timeout(t))
			if err != nil {
				t.Fatal(err)
			}
			if e.State == "authenticating" {
				auth := tc.auth
				auth.ID = e.ID
				if err = c.Send(timeout(t), auth); err != nil {
					t.Fatal(err)
				}
				e, err = c.Receive(timeout(t))
				if err != nil {
					t.Fatal(err)
				}
			}
			if e.State != "failed" || e.Reason.Code != tc.code {
				t.Fatal("bad failure", e)
			}
			if err := <-done; err == nil {
				t.Fatal("accepted")
			}
			if _, err := c.Receive(timeout(t)); err == nil {
				t.Fatal("failure did not close")
			}
		})
	}
}
func TestFramingAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		kind  int
		data  []byte
		limit int
	}{
		{"binary", websocket.BinaryMessage, []byte(`{"state":"new"}`), 0},
		{"malformed", websocket.TextMessage, []byte(`{"bad":1}`), 0},
		{"oversize", websocket.TextMessage, []byte(`{"type":"text","content":"too long"}`), 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				u := websocket.Upgrader{Subprotocols: []string{"lime"}}
				ws, err := u.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer ws.Close()
				_ = ws.WriteMessage(tc.kind, tc.data)
			}))
			defer s.Close()
			c, err := Dial(timeout(t), "ws"+strings.TrimPrefix(s.URL, "http"), nil, Limits{FrameBytes: tc.limit})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if _, err = c.Receive(timeout(t)); err == nil {
				t.Fatal("invalid frame accepted")
			}
		})
	}
	entered := make(chan struct{}, 4)
	s := wsServer(t, func(c *Conn) { entered <- struct{}{}; _, _ = c.Receive(context.Background()) })
	c := dialTest(t, s)
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Receive(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := c.Send(ctx, Envelope{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := c.Receive(ctx); result <- err }()
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	c = dialTest(t, s)
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.Receive(ctx); err == nil {
		t.Fatal("deadline did not interrupt")
	}
}
func TestTransportValidationAndErrors(t *testing.T) {
	if _, err := NewConn(nil, Limits{}); err == nil {
		t.Fatal("nil socket")
	}
	if _, err := NewConn(nil, Limits{FrameBytes: -1}); err == nil {
		t.Fatal("limit")
	}
	if _, err := Dial(context.Background(), "bad", nil, Limits{FrameBytes: -1}); err == nil {
		t.Fatal("dial limit")
	}
	if _, err := Dial(timeout(t), "http://invalid", nil, Limits{}); err == nil {
		t.Fatal("bad scheme")
	}
	request := httptest.NewRequest("GET", "http://localhost", nil)
	if _, err := Upgrade(httptest.NewRecorder(), request, Limits{FrameBytes: -1}); err == nil {
		t.Fatal("upgrade limits")
	}
	if _, err := Upgrade(httptest.NewRecorder(), request, Limits{}); err == nil {
		t.Fatal("missing subprotocol")
	}
	request.Header.Set("Sec-WebSocket-Protocol", "lime")
	if _, err := Upgrade(httptest.NewRecorder(), request, Limits{}); err == nil {
		t.Fatal("not an upgrade")
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "denied", 403) }))
	defer s.Close()
	if _, err := Dial(timeout(t), "ws"+strings.TrimPrefix(s.URL, "http"), nil, Limits{}); err == nil {
		t.Fatal("HTTP denied")
	}
	s2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := websocket.Upgrader{}
		ws, err := u.Upgrade(w, r, nil)
		if err == nil {
			defer ws.Close()
		}
	}))
	defer s2.Close()
	if _, err := Dial(timeout(t), "ws"+strings.TrimPrefix(s2.URL, "http"), nil, Limits{}); err == nil {
		t.Fatal("subprotocol mismatch")
	}
	wait := make(chan struct{})
	s3 := wsServer(t, func(c *Conn) { <-wait })
	c := dialTest(t, s3)
	if err := c.Send(timeout(t), Envelope{}); err == nil {
		t.Fatal("invalid outbound")
	}
	c.limits.FrameBytes = 8
	if err := c.Send(timeout(t), Envelope{State: "new", Version: 2}); err == nil {
		t.Fatal("size cap")
	}
	close(wait)
	_ = c.Close()
	c.limits.FrameBytes = 65536
	if err := c.Send(timeout(t), Envelope{State: "new", Version: 2}); err == nil {
		t.Fatal("closed write")
	}
	if _, err := c.Receive(timeout(t)); err == nil {
		t.Fatal("closed read")
	}
	for range 10 {
		id, err := NewID()
		if err != nil || len(id) != 36 || id[14] != '4' {
			t.Fatal("ID", id, err)
		}
	}
}
func TestClientRejectsUnexpectedHandshake(t *testing.T) {
	for _, reply := range []Envelope{
		{State: "failed", Reason: &Reason{Code: 1}}, {State: "established", ID: "s"},
		{State: "authenticating", ID: "s", SchemeOptions: []string{"other"}},
	} {
		s := wsServer(t, func(c *Conn) { _, _ = c.Receive(timeout(t)); _ = c.Send(timeout(t), reply) })
		c := dialTest(t, s)
		if _, err := EstablishSession(timeout(t), c, "alice", []byte("secret")); err == nil {
			t.Fatal("bad handshake", reply)
		}
	}
	s := wsServer(t, func(c *Conn) {
		_, _ = c.Receive(timeout(t))
		_ = c.Send(timeout(t), Envelope{State: "authenticating", ID: "s", SchemeOptions: []string{"plain"}})
	})
	c := dialTest(t, s)
	if _, err := EstablishSession(timeout(t), c, "", nil); err == nil {
		t.Fatal("credentials")
	}
	s = wsServer(t, func(c *Conn) {
		e, _ := c.Receive(timeout(t))
		_ = c.Send(timeout(t), Envelope{ID: e.ID, State: "established", To: "alice"})
	})
	c = dialTest(t, s)
	if err := FinishSession(timeout(t), c, "s"); err == nil {
		t.Fatal("finish mismatch")
	}
	s = wsServer(t, func(c *Conn) { _ = c.Close() })
	c = dialTest(t, s)
	if _, err := AcceptSession(timeout(t), c, ServerOptions{}); err == nil {
		t.Fatal("closed establish")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := EstablishSession(canceled, c, "alice", []byte("x")); err == nil {
		t.Fatal("canceled client")
	}
	if err := FinishSession(canceled, c, "s"); err == nil {
		t.Fatal("canceled finish")
	}
}
func TestPlainPasswordConvention(t *testing.T) {
	b, _ := json.Marshal(PlainAuthentication{Password: base64.StdEncoding.EncodeToString([]byte("secret"))})
	if string(b) != `{"password":"c2VjcmV0"}` {
		t.Fatal(string(b))
	}
}

func TestCanceledQueuedOperations(t *testing.T) {
	hold := make(chan struct{})
	s := wsServer(t, func(c *Conn) { <-hold })
	c := dialTest(t, s)
	c.writeGate <- struct{}{}
	c.readGate <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Send(ctx, Envelope{State: "new", Version: 2}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := c.Receive(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// A canceled operation waiting behind an owner returns without acquiring I/O.
	ctx, cancel = context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := c.Send(ctx, Envelope{State: "new", Version: 2}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel2()
	if _, err := c.Receive(ctx2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	<-c.writeGate
	<-c.readGate
	close(hold)
}
func TestFramePreflight(t *testing.T) {
	hold := make(chan struct{})
	s := wsServer(t, func(c *Conn) { <-hold })
	c := dialTest(t, s)
	defer close(hold)
	c.limits.FrameBytes = 8
	for _, e := range []Envelope{
		{Type: "json", Content: []byte(`"too long"`)},
		{Method: "set", ID: "x", URI: "/x", Type: "json", Resource: []byte(`"too long"`)},
		{State: "new", Metadata: []byte(`{"long":1}`)},
		{State: "authenticating", Scheme: "plain", Authentication: []byte(`{"password":"x"}`)},
	} {
		if err := c.Send(timeout(t), e); err == nil {
			t.Fatal("oversize preflight", e)
		}
	}
}
func TestOptionalServerNodeBinding(t *testing.T) {
	done := make(chan error, 1)
	s := wsServer(t, func(c *Conn) {
		info, err := AcceptSession(timeout(t), c, ServerOptions{LocalNode: "postmaster@localhost", TransportIdentity: "alice"})
		if err == nil && info.Local != "postmaster@localhost" {
			err = errors.New("local identity not returned")
		}
		done <- err
	})
	c := dialTest(t, s)
	info, err := EstablishSession(timeout(t), c, "", nil)
	if err != nil || info.Remote != "postmaster@localhost" || info.Local != "alice" {
		t.Fatal("peer routing binding", info, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
