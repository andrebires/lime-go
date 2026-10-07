package main

import (
	"bytes"
	"context"
	"encoding/json"
	lime "github.com/andrebires/lime-go/v2"
	"github.com/gorilla/websocket"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func timeout(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return ctx
}

type testClient struct {
	conn          *lime.Conn
	node, session string
}

func client(t *testing.T, s *httptest.Server) testClient {
	t.Helper()
	resp, err := http.Post(s.URL+"/identity", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var auth struct{ Node, Token string }
	if err = json.NewDecoder(resp.Body).Decode(&auth); err != nil {
		t.Fatal(err)
	}
	c, err := lime.Dial(timeout(t), "ws"+strings.TrimPrefix(s.URL, "http")+"/ws", nil, lime.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	info, err := lime.EstablishSession(timeout(t), c, auth.Node, []byte(auth.Token))
	if err != nil {
		t.Fatal(err)
	}
	// Roundtrip proves registration has finished before another client sends.
	if err = c.Send(timeout(t), lime.Envelope{ID: "ready", Method: "get", URI: "/peers"}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Receive(timeout(t)); err != nil {
		t.Fatal(err)
	}
	return testClient{c, info.Local, info.ID}
}
func send(t *testing.T, c testClient, e lime.Envelope) {
	t.Helper()
	if err := c.conn.Send(timeout(t), e); err != nil {
		t.Fatal(err)
	}
}
func receive(t *testing.T, c testClient) lime.Envelope {
	t.Helper()
	e, err := c.conn.Receive(timeout(t))
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func demo(t *testing.T) (*hub, *httptest.Server) {
	t.Helper()
	h := newHub()
	s := httptest.NewServer(h.handler())
	t.Cleanup(func() { h.close(); s.Close() })
	return h, s
}
func TestMultiClientStreamingAndReceipts(t *testing.T) {
	_, s := demo(t)
	a, b, c := client(t, s), client(t, s), client(t, s)
	send(t, a, lime.Envelope{ID: "peers", Method: "get", URI: "/peers"})
	resp := receive(t, a)
	var peers []string
	_ = json.Unmarshal(resp.Resource, &peers)
	if len(peers) != 2 {
		t.Fatal("peers", resp)
	}
	send(t, a, lime.Envelope{ID: "m", To: b.node, Thread: "t", Type: "text", Stream: "start", Rev: 2})
	start := receive(t, b)
	if start.From != a.node || start.Type != "text/plain" || start.Rev != 2 {
		t.Fatal(start)
	}
	send(t, a, lime.Envelope{ID: "m", To: b.node, Stream: "data", Content: []byte(`"typing"`), Rev: 2})
	data := receive(t, b)
	if data.Stream != "data" || string(data.Content) != `"typing"` {
		t.Fatal(data)
	}
	send(t, a, lime.Envelope{ID: "m", To: b.node, Stream: "end", Rev: 2})
	end := receive(t, b)
	if end.Stream != "end" {
		t.Fatal(end)
	}
	for _, event := range []string{"received", "consumed"} {
		send(t, b, lime.Envelope{ID: "m", To: a.node, Event: event, Rev: 2})
		not := receive(t, a)
		if not.Event != event || not.From != b.node || not.Rev != 2 {
			t.Fatal(not)
		}
	}
	// JSON patches replace arrays and delete null members on the receiving assembler.
	r, _ := lime.NewRegistry(lime.Limits{})
	assembly, _ := lime.NewAssembler(r, lime.Limits{})
	for _, e := range []lime.Envelope{{ID: "j", To: b.node, Type: "json", Stream: "start"}, {ID: "j", To: b.node, Stream: "data", Content: []byte(`{"options":[1,2],"delete":true}`)}, {ID: "j", To: b.node, Stream: "data", Content: []byte(`{"options":[3],"delete":null}`)}, {ID: "j", To: b.node, Stream: "end"}} {
		send(t, a, e)
		received := receive(t, b)
		result, err := assembly.Apply(received)
		if err != nil {
			t.Fatal(err)
		}
		if result.Complete && string(result.Message.Content) != `{"options":[3]}` {
			t.Fatal(string(result.Message.Content))
		}
	}
	send(t, a, lime.Envelope{ID: "all", Type: "text", Content: []byte(`"broadcast"`)})
	for _, other := range []testClient{b, c} {
		if got := receive(t, other); got.ID != "all" || got.From != a.node {
			t.Fatal(got)
		}
	}
	// Retry same identity as a complete message; no stream chunks are replayed.
	send(t, a, lime.Envelope{ID: "m", To: b.node, Thread: "t", Type: "text", Content: []byte(`"typing"`), Rev: 2})
	retry := receive(t, b)
	if retry.Stream != "" || retry.Rev != 2 {
		t.Fatal(retry)
	}
	send(t, a, lime.Envelope{Type: "text", To: b.node, Content: []byte(`"fire and forget"`)})
	if got := receive(t, b); got.ID != "" {
		t.Fatal(got)
	}
	send(t, a, lime.Envelope{ID: a.session, State: "finishing"})
	if got := receive(t, a); got.State != "finished" {
		t.Fatal(got)
	}
	fresh := client(t, s)
	if fresh.session == a.session || fresh.node == a.node {
		t.Fatal("resumed old session")
	}
}
func TestDemoAliasesAndFailures(t *testing.T) {
	_, s := demo(t)
	a, b := client(t, s), client(t, s)
	send(t, a, lime.Envelope{ID: "alias", Method: "set", URI: lime.AliasURI, Type: "json", Resource: []byte(`{"note":"text/plain"}`)})
	if resp := receive(t, a); resp.Status != "success" {
		t.Fatal(resp)
	}
	send(t, a, lime.Envelope{ID: "note", To: b.node, Type: "note", Content: []byte(`"alias works"`)})
	if e := receive(t, b); e.Type != "text/plain" {
		t.Fatal("alias leaked between registries", e)
	}
	for _, e := range []lime.Envelope{{ID: "bad", Method: "get", URI: "/unsupported"}, {ID: "bad", Method: "delete", URI: "/peers"}, {ID: "bad", Method: "set", URI: lime.AliasURI, Type: "json", Resource: []byte(`{"text":"text/plain"}`)}} {
		send(t, a, e)
		if resp := receive(t, a); resp.Status != "failure" || resp.Reason == nil {
			t.Fatal(resp)
		}
	}
	for _, e := range []lime.Envelope{{ID: "missing", To: "nobody", Type: "text", Content: []byte(`"x"`)}, {ID: "invalid", To: b.node, Type: "text", Content: []byte(`{}`)}, {ID: "unknown", To: b.node, Type: "bad", Stream: "start"}, {ID: "orphan", To: b.node, Stream: "end"}} {
		send(t, a, e)
		if resp := receive(t, a); resp.Event != "failed" || resp.ID != e.ID || resp.Reason == nil {
			t.Fatal(resp)
		}
	}
	send(t, a, lime.Envelope{ID: "response", Method: "get", Status: "success"})
	send(t, a, lime.Envelope{ID: "after", Method: "get", URI: "/peers"})
	if resp := receive(t, a); resp.ID != "after" {
		t.Fatal("unsolicited response routed", resp)
	}
	// A receipt is authorized by the recipient's ordered tracker, not browser claims.
	send(t, b, lime.Envelope{ID: "forged", To: a.node, Event: "received"})
	if resp := receive(t, b); resp.State != "failed" {
		t.Fatal("forged receipt accepted", resp)
	}
}
func TestDemoSecurityAndBoundaries(t *testing.T) {
	h, s := demo(t)
	for _, path := range []string{"/", "/client.js", "/missing"} {
		resp, err := http.Get(s.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if path == "/missing" {
			if resp.StatusCode != 404 {
				t.Fatal(resp.StatusCode)
			}
		} else if len(data) == 0 {
			t.Fatal("empty static asset")
		}
	}
	resp, _ := http.Get(s.URL + "/identity")
	resp.Body.Close()
	if resp.StatusCode != 405 {
		t.Fatal("GET minted identity")
	}
	req, _ := http.NewRequest("POST", s.URL+"/identity", nil)
	req.Header.Set("Origin", "https://evil.test")
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("cross origin")
	}
	req, _ = http.NewRequest("POST", s.URL+"/identity", nil)
	req.Header.Set("Origin", s.URL)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("same origin")
	}
	if _, err := h.authenticate(context.Background(), "unknown", []byte("token")); err == nil {
		t.Fatal("unbound identity")
	}
	now := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	h.mu.Lock()
	h.capabilities["expired"] = capability{node: "expired", token: "x", expires: now}
	h.capabilities["valid"] = capability{node: "valid", token: "x", expires: now.Add(time.Minute)}
	h.mu.Unlock()
	if _, err := h.authenticate(context.Background(), "expired", []byte("x")); err == nil {
		t.Fatal("expired accepted")
	}
	if _, err := h.authenticate(context.Background(), "valid", []byte("wrong")); err == nil {
		t.Fatal("wrong password")
	}
	if got, err := h.authenticate(context.Background(), "valid", []byte("x")); err != nil || got != "valid" {
		t.Fatal(err)
	}
	if _, err := h.authenticate(context.Background(), "valid", []byte("x")); err == nil {
		t.Fatal("replayed capability")
	}
	for range maxCapabilities + 1 {
		resp, _ := http.Post(s.URL+"/identity", "application/json", nil)
		resp.Body.Close()
	}
	resp, _ = http.Post(s.URL+"/identity", "application/json", nil)
	resp.Body.Close()
	if resp.StatusCode != 429 {
		t.Fatal("capability capacity")
	}
	// Fresh hub to test session spoofing without capability exhaustion.
	_, s2 := demo(t)
	a := client(t, s2)
	send(t, a, lime.Envelope{ID: "spoof", From: "mallory", Type: "text", Content: []byte(`"x"`)})
	if e := receive(t, a); e.State != "failed" {
		t.Fatal("sender spoof accepted")
	}
	a = client(t, s2)
	send(t, a, lime.Envelope{ID: "spoof", PP: "mallory", Type: "text", Content: []byte(`"x"`)})
	if e := receive(t, a); e.State != "failed" {
		t.Fatal("delegation spoof accepted")
	}
	a = client(t, s2)
	send(t, a, lime.Envelope{Type: "bad", Content: []byte(`{}`)})
	if e := receive(t, a); e.State != "failed" {
		t.Fatal("IDless failure not honest")
	}
	a = client(t, s2)
	send(t, a, lime.Envelope{State: "new", Version: 2})
	if _, err := a.conn.Receive(timeout(t)); err == nil {
		t.Fatal("second new allowed")
	}
	// Reserved pending handshakes count toward the finite peer limit.
	h.mu.Lock()
	for i := 0; i < maxPeers; i++ {
		h.peers[string(rune('a'+i))] = nil
	}
	h.mu.Unlock()
	if _, err := lime.Dial(timeout(t), "ws"+strings.TrimPrefix(s.URL, "http")+"/ws", nil, lime.Limits{}); err == nil {
		t.Fatal("peer limit")
	}
}
func TestDemoDisconnectInterruptsStream(t *testing.T) {
	h, s := demo(t)
	a, b := client(t, s), client(t, s)
	send(t, a, lime.Envelope{ID: "m", To: b.node, Type: "text", Stream: "start"})
	_ = receive(t, b)
	send(t, a, lime.Envelope{ID: "m", To: b.node, Stream: "data", Content: []byte(`"partial"`)})
	if e := receive(t, b); e.Stream != "data" {
		t.Fatal(e)
	}
	_ = a.conn.Close()
	h.close()
	if _, err := b.conn.Receive(timeout(t)); err == nil {
		t.Fatal("shutdown leaked connection")
	}
}
func TestHTTPUpgradeOrigin(t *testing.T) {
	_, s := demo(t)
	header := http.Header{"Origin": []string{"http://evil.test"}}
	if _, err := lime.Dial(timeout(t), "ws"+strings.TrimPrefix(s.URL, "http")+"/ws", header, lime.Limits{}); err == nil {
		t.Fatal("origin allowed")
	}
	resp, _ := http.Get(s.URL + "/ws")
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatal("missing subprotocol")
	}
}
func TestDemoWireIsPlainJSON(t *testing.T) {
	_, s := demo(t)
	d := websocket.Dialer{Subprotocols: []string{"lime"}}
	c, _, err := d.Dial("ws"+strings.TrimPrefix(s.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err = c.WriteMessage(websocket.TextMessage, []byte(`{"state":"new"}`)); err != nil {
		t.Fatal(err)
	}
	kind, b, err := c.ReadMessage()
	if err != nil || kind != websocket.TextMessage || !json.Valid(b) || !bytes.Contains(b, []byte(`"failed"`)) {
		t.Fatal(string(b), err)
	}
}
func TestDemoStartupAndShutdown(t *testing.T) {
	for _, addr := range []string{"invalid", "0.0.0.0:8080", "example.com:8080"} {
		if err := run(context.Background(), addr); err == nil {
			t.Fatal("invalid startup configuration")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, "127.0.0.1:0"); err != nil {
		t.Fatal("shutdown", err)
	}
	s := httptest.NewServer(http.NotFoundHandler())
	defer s.Close()
	if err := run(context.Background(), s.Listener.Addr().String()); err == nil {
		t.Fatal("occupied port")
	}
	h := newHub()
	h.close()
	s2 := httptest.NewServer(h.handler())
	defer s2.Close()
	if _, err := lime.Dial(timeout(t), "ws"+strings.TrimPrefix(s2.URL, "http")+"/ws", nil, lime.Limits{}); err == nil {
		t.Fatal("closed hub accepted connection")
	}
}
func TestJSONStreamRetryWithClientPropertyOrder(t *testing.T) {
	_, s := demo(t)
	a, b := client(t, s), client(t, s)
	for _, e := range []lime.Envelope{{ID: "j", To: b.node, Type: "json", Stream: "start"}, {ID: "j", To: b.node, Stream: "data", Content: []byte(`{"text":"hello","options":[1]}`)}, {ID: "j", To: b.node, Stream: "end"}} {
		send(t, a, e)
		_ = receive(t, b)
	}
	send(t, a, lime.Envelope{ID: "j", To: b.node, Type: "json", Content: []byte(`{"text":"hello","options":[1]}`)})
	if got := receive(t, b); got.ID != "j" || got.Stream != "" {
		t.Fatal("valid JSON retry rejected", got)
	}
}
