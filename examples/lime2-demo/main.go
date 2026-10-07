// The demo is an ephemeral, local protocol laboratory. It does not claim durable
// completion. Production integrations must persist before releasing stream end.
package main

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"sync"
	"syscall"
	"time"

	lime "github.com/andrebires/lime-go/v2"
)

//go:embed index.html
var page []byte

//go:embed client.js
var clientScript []byte

const maxPeers = 16
const maxCapabilities = 64

type capability struct {
	node, token string
	expires     time.Time
}
type peer struct {
	sendMu    sync.Mutex
	node      string
	conn      *lime.Conn
	assembler *lime.Assembler
	registry  *lime.Registry
	tracker   *lime.Tracker
	routes    map[string][]*peer
}
type hub struct {
	closed       bool
	mu           sync.Mutex
	peers        map[string]*peer
	capabilities map[string]capability
	now          func() time.Time
}

func newHub() *hub {
	return &hub{peers: make(map[string]*peer), capabilities: make(map[string]capability), now: time.Now}
}
func (h *hub) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", h.index)
	mux.HandleFunc("/client.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(clientScript)
	})
	mux.HandleFunc("/identity", h.identity)
	mux.HandleFunc("/ws", h.websocket)
	return mux
}
func (h *hub) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; object-src 'none'; frame-ancestors 'none'")
	_, _ = w.Write(page)
}
func (h *hub) identity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host && origin != "https://"+r.Host {
		http.Error(w, "same origin required", http.StatusForbidden)
		return
	}
	node, err := lime.NewID()
	if err != nil {
		http.Error(w, "identity unavailable", http.StatusServiceUnavailable)
		return
	}
	token, err := lime.NewID()
	if err != nil {
		http.Error(w, "identity unavailable", http.StatusServiceUnavailable)
		return
	}
	node = "client-" + node[:8] + "@localhost"
	h.mu.Lock()
	defer h.mu.Unlock()
	for name, cap := range h.capabilities {
		if !h.now().Before(cap.expires) {
			delete(h.capabilities, name)
		}
	}
	if len(h.capabilities) >= maxCapabilities {
		http.Error(w, "capability limit", http.StatusTooManyRequests)
		return
	}
	h.capabilities[node] = capability{node: node, token: token, expires: h.now().Add(time.Minute)}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(struct {
		Node  string `json:"node"`
		Token string `json:"token"`
	}{node, token})
}
func (h *hub) authenticate(_ context.Context, node string, password []byte) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cap, ok := h.capabilities[node]
	if !ok || !h.now().Before(cap.expires) || subtle.ConstantTimeCompare([]byte(cap.token), password) != 1 {
		return "", errors.New("invalid capability")
	}
	delete(h.capabilities, node)
	return cap.node, nil
}
func (h *hub) websocket(w http.ResponseWriter, r *http.Request) {
	// Reserve capacity before upgrading; includes unauthenticated handshakes.
	h.mu.Lock()
	if h.closed || len(h.peers) >= maxPeers {
		h.mu.Unlock()
		http.Error(w, "peer limit", http.StatusServiceUnavailable)
		return
	}
	slot, err := lime.NewID()
	if err != nil {
		h.mu.Unlock()
		http.Error(w, "session unavailable", http.StatusServiceUnavailable)
		return
	}
	h.peers[slot] = nil
	h.mu.Unlock()
	defer func() { h.mu.Lock(); delete(h.peers, slot); h.mu.Unlock() }()
	c, err := lime.Upgrade(w, r, lime.Limits{})
	if err != nil {
		return
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	info, err := lime.AcceptSession(ctx, c, lime.ServerOptions{Authenticate: h.authenticate})
	cancel()
	if err != nil {
		return
	}
	registry, _ := lime.NewRegistry(lime.Limits{})
	assembly, _ := lime.NewAssembler(registry, lime.Limits{})
	tracker, _ := lime.NewTracker(info.Remote, lime.Limits{})
	p := &peer{node: info.Remote, conn: c, registry: registry, assembler: assembly, tracker: tracker, routes: make(map[string][]*peer)}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.peers[slot] = p
	h.mu.Unlock()
	for {
		e, err := c.Receive(r.Context())
		if err != nil {
			return
		}
		if (e.From != "" && e.From != p.node) || e.PP != "" {
			_ = c.Send(r.Context(), lime.Envelope{ID: info.ID, State: "failed", Reason: &lime.Reason{Code: lime.AuthenticationFailed, Description: "sender does not match authenticated identity"}})
			return
		}
		e.From = p.node
		kind, _ := e.Kind()
		switch kind {
		case lime.Session:
			if e.State != "finishing" || e.ID != info.ID {
				return
			}
			_ = c.Send(r.Context(), lime.Envelope{ID: info.ID, State: "finished"})
			return
		case lime.Command:
			if e.Status != "" {
				continue
			}
			resp := h.command(p, e)
			if err = c.Send(r.Context(), resp); err != nil {
				return
			}
		case lime.Notification:
			if err = p.tracker.Apply(e); err != nil {
				_ = c.Send(r.Context(), lime.Envelope{ID: info.ID, State: "failed", Reason: &lime.Reason{Code: lime.InvalidInput, Description: err.Error()}})
				return
			}
			if target := h.find(e.To); target != nil {
				if err = target.conn.Send(r.Context(), e); err != nil {
					_ = target.conn.Close()
				}
			}
		case lime.Message:
			if err = h.message(r.Context(), p, e); err != nil {
				p.assembler.Discard(e)
				delete(p.routes, messageKey(e))
				if e.ID == "" {
					_ = c.Send(r.Context(), lime.Envelope{ID: info.ID, State: "failed", Reason: &lime.Reason{Code: lime.InvalidInput, Description: err.Error()}})
					return
				}
				if err = c.Send(r.Context(), lime.Envelope{ID: e.ID, Rev: e.Rev, To: p.node, Event: "failed", Reason: &lime.Reason{Code: lime.InvalidInput, Description: err.Error()}}); err != nil {
					return
				}
			}
		}
	}
}
func (h *hub) find(node string) *peer {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, p := range h.peers {
		if p != nil && p.node == node {
			return p
		}
	}
	return nil
}
func (h *hub) destinations(p *peer, to string) []*peer {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*peer, 0, len(h.peers))
	for _, other := range h.peers {
		if other != nil && other != p && (to == "" || to == other.node) {
			out = append(out, other)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].node < out[j].node })
	return out
}
func messageKey(e lime.Envelope) string { return fmt.Sprintf("%s/%d", e.ID, e.Revision()) }
func (h *hub) message(ctx context.Context, p *peer, e lime.Envelope) error {
	result, err := p.assembler.Apply(e)
	if err != nil {
		return err
	}
	// Resolve sender aliases before routing; registries belong to different sessions.
	if e.Stream == "" || e.Stream == "start" {
		e.Type = result.Message.Type
	}
	key := messageKey(e)
	var recipients []*peer
	if e.Stream == "" || e.Stream == "start" {
		recipients = h.destinations(p, e.To)
		if len(recipients) == 0 {
			return errors.New("destination unavailable")
		}
		if e.Stream == "start" {
			p.routes[key] = recipients
		}
	} else {
		recipients = p.routes[key]
		if len(recipients) == 0 {
			return errors.New("stream destination unavailable")
		}
	}
	if e.Stream == "end" {
		delete(p.routes, key)
	}
	for _, target := range recipients {
		target.sendMu.Lock()
		outgoing := e
		outgoing.To = target.node
		if e.Stream == "start" || result.Complete {
			tracked := outgoing
			if result.Complete {
				tracked = result.Message
				tracked.To = target.node
			}
			if err = target.tracker.Track(tracked); err != nil {
				target.sendMu.Unlock()
				return err
			}
		}
		if result.Duplicate {
			// Retry the completed revision atomically; receiver suppresses redisplay and
			// emits its receipt again. Never replay old chunks.
			outgoing = result.Message
			outgoing.To = target.node
		}
		err = target.conn.Send(ctx, outgoing)
		target.sendMu.Unlock()
		if err != nil {
			_ = target.conn.Close()
			return err
		}
	}
	return nil
}
func (h *hub) command(p *peer, e lime.Envelope) lime.Envelope {
	if e.URI == lime.AliasURI {
		return p.registry.AliasCommand(e)
	}
	resp := lime.Envelope{ID: e.ID, Method: e.Method, To: p.node, Status: "success"}
	if e.URI != "/peers" || e.Method != "get" {
		resp.Status = "failure"
		resp.Reason = &lime.Reason{Code: lime.UnsupportedOperation, Description: "use get /peers or /protocol/aliases"}
		return resp
	}
	peers := h.destinations(p, "")
	names := make([]string, len(peers))
	for i, other := range peers {
		names[i] = other.node
	}
	resp.Type = "json"
	resp.Resource, _ = json.Marshal(names)
	return resp
}
func (h *hub) close() {
	h.mu.Lock()
	h.closed = true
	defer h.mu.Unlock()
	for _, p := range h.peers {
		if p != nil {
			_ = p.conn.Close()
		}
	}
}
func run(ctx context.Context, addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("demo address must be a loopback IP with port")
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	h := newHub()
	server := &http.Server{Handler: h.handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		h.close()
		shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = server.Shutdown(shutdown)
	}()
	slog.Info("LIME 2.0 demo ready; ephemeral messages", "url", "http://"+listener.Addr().String())
	err = server.Serve(listener)
	cancel()
	<-done
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "local listen address")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *addr); err != nil {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}
