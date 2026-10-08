package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	// JSON Patch appends array items and removes explicit members on the receiving assembler.
	r, _ := lime.NewRegistry(lime.Limits{})
	assembly, _ := lime.NewAssembler(r, lime.Limits{})
	for _, e := range []lime.Envelope{{ID: "j", To: b.node, Type: "json", Stream: "start"}, {ID: "j", To: b.node, Stream: "data", Content: []byte(`[{"op":"add","path":"/options","value":[1,2]},{"op":"add","path":"/delete","value":true}]`)}, {ID: "j", To: b.node, Stream: "data", Content: []byte(`[{"op":"add","path":"/options/-","value":3},{"op":"remove","path":"/delete"}]`)}, {ID: "j", To: b.node, Stream: "end"}} {
		send(t, a, e)
		received := receive(t, b)
		result, err := assembly.Apply(received)
		if err != nil {
			t.Fatal(err)
		}
		if result.Complete && string(result.Message.Content) != `{"options":[1,2,3]}` {
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
	for _, e := range []lime.Envelope{{ID: "j", To: b.node, Type: "json", Stream: "start"}, {ID: "j", To: b.node, Stream: "data", Content: []byte(`[{"op":"add","path":"/text","value":"hello"},{"op":"add","path":"/options","value":[1]}]`)}, {ID: "j", To: b.node, Stream: "end"}} {
		send(t, a, e)
		_ = receive(t, b)
	}
	send(t, a, lime.Envelope{ID: "j", To: b.node, Type: "json", Content: []byte(`{"text":"hello","options":[1]}`)})
	if got := receive(t, b); got.ID != "j" || got.Stream != "" {
		t.Fatal("valid JSON retry rejected", got)
	}
}

func TestScopedNotificationRelay(t *testing.T) {
	h, s := demo(t)
	a, b, c := client(t, s), client(t, s), client(t, s)
	for _, item := range []struct {
		sender     testClient
		id, thread string
		rev        uint64
	}{
		{a, "a1", "one", 2}, {c, "c1", "one", 3},
		{a, "a2", "two", 4}, {c, "c2", "two", 5},
	} {
		send(t, item.sender, lime.Envelope{ID: item.id, Rev: item.rev, To: b.node, Thread: item.thread, Type: "text", Content: []byte(`"complete"`)})
		if got := receive(t, b); got.ID != item.id || got.Rev != item.rev {
			t.Fatal(got)
		}
	}
	// Each original sender receives its latest covered marker. The receiver's
	// session spans both origins, while their outbound buffers stay independent.
	for _, item := range []struct {
		sender testClient
		id     string
		rev    uint64
	}{
		{a, "a2", 4}, {c, "c2", 5},
	} {
		send(t, b, lime.Envelope{ID: item.id, Rev: item.rev, To: item.sender.node, Event: "received", Scope: "session"})
		got := receive(t, item.sender)
		if got.ID != item.id || got.Rev != item.rev || got.Scope != "session" || got.From != b.node || got.To != item.sender.node || got.Thread != "" {
			t.Fatal(got)
		}
	}
	if entries := h.find(b.node).tracker.Pending(); len(entries) != 0 {
		t.Fatal("prefix not released", entries)
	}
	for _, item := range []struct {
		sender testClient
		id     string
		rev    uint64
	}{
		{a, "a1", 2}, {c, "c1", 3},
	} {
		send(t, b, lime.Envelope{ID: item.id, Rev: item.rev, To: item.sender.node, Event: "consumed", Scope: "thread", Thread: "one"})
		got := receive(t, item.sender)
		if got.ID != item.id || got.Rev != item.rev || got.Scope != "thread" || got.Thread != "one" || got.From != b.node {
			t.Fatal(got)
		}
	}
	// Scoped relay does not prevent later direct messaging on this session.
	send(t, a, lime.Envelope{ID: "after", To: b.node, Type: "text", Content: []byte(`"after"`)})
	if got := receive(t, b); got.ID != "after" {
		t.Fatal(got)
	}
}

func TestScopedNotificationGaps(t *testing.T) {
	for _, tc := range []struct {
		name, event, scope, gapThread string
		accept                        bool
	}{
		{"session across threads", "received", "session", "other", false},
		{"thread skips other threads", "consumed", "thread", "other", true},
		{"thread blocks its own gap", "consumed", "thread", "chosen", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, s := demo(t)
			a, b := client(t, s), client(t, s)
			send(t, a, lime.Envelope{ID: "gap", To: b.node, Thread: tc.gapThread, Type: "text", Stream: "start"})
			if got := receive(t, b); got.Stream != "start" {
				t.Fatal(got)
			}
			send(t, a, lime.Envelope{ID: "marker", Rev: 2, To: b.node, Thread: "chosen", Type: "text", Content: []byte(`"done"`)})
			if got := receive(t, b); got.ID != "marker" {
				t.Fatal(got)
			}
			n := lime.Envelope{ID: "marker", Rev: 2, To: a.node, Event: tc.event, Scope: tc.scope}
			if tc.scope == "thread" {
				n.Thread = "chosen"
			}
			send(t, b, n)
			if tc.accept {
				if got := receive(t, a); got.ID != "marker" || got.Scope != "thread" || got.Thread != "chosen" {
					t.Fatal(got)
				}
			} else {
				if got := receive(t, b); got.State != "failed" || got.Reason == nil || !strings.Contains(got.Reason.Description, "incomplete") {
					t.Fatal(got)
				}
			}
		})
	}
}

func deliveryRequest(t *testing.T, c testClient, method, id string, rev uint64) lime.Envelope {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"id": id, "rev": rev})
	send(t, c, lime.Envelope{ID: "delivery-status", Method: method, URI: deliveryURI, Type: "json", Resource: raw})
	response := receive(t, c)
	if response.ID != "delivery-status" {
		t.Fatal("unexpected delivery response", response)
	}
	return response
}
func pendingNodes(t *testing.T, e lime.Envelope) []string {
	t.Helper()
	if e.Status != "success" {
		t.Fatal(e)
	}
	var body struct {
		Pending []string `json:"pending"`
	}
	if err := json.Unmarshal(e.Resource, &body); err != nil {
		t.Fatal(err)
	}
	return body.Pending
}
func TestBroadcastRetryUsesEveryOriginalRecipient(t *testing.T) {
	_, s := demo(t)
	a, b, c := client(t, s), client(t, s), client(t, s)
	send(t, a, lime.Envelope{ID: "broadcast-retry", Rev: 2, Thread: "t", Type: "json", Stream: "start"})
	for _, recipient := range []testClient{b, c} {
		if got := receive(t, recipient); got.Stream != "start" {
			t.Fatal(got)
		}
	}
	if response := deliveryRequest(t, a, "set", "broadcast-retry", 2); response.Status != "failure" || !strings.Contains(response.Reason.Description, "complete") {
		t.Fatal(response)
	}
	for _, e := range []lime.Envelope{
		{ID: "broadcast-retry", Rev: 2, Stream: "data", Content: []byte(`[{"op":"add","path":"/x","value":1},{"op":"add","path":"/delete","value":true}]`)},
		{ID: "broadcast-retry", Rev: 2, Stream: "data", Content: []byte(`[{"op":"remove","path":"/delete"},{"op":"add","path":"/y","value":2}]`)},
		{ID: "broadcast-retry", Rev: 2, Stream: "end"},
	} {
		send(t, a, e)
		for _, recipient := range []testClient{b, c} {
			if got := receive(t, recipient); got.Stream != e.Stream {
				t.Fatal(got)
			}
		}
	}
	send(t, b, lime.Envelope{ID: "broadcast-retry", Rev: 2, To: a.node, Event: "received"})
	if got := receive(t, a); got.From != b.node {
		t.Fatal(got)
	}
	if nodes := pendingNodes(t, deliveryRequest(t, a, "get", "broadcast-retry", 2)); len(nodes) != 1 || nodes[0] != c.node {
		t.Fatal(nodes)
	}
	joined := client(t, s)
	if nodes := pendingNodes(t, deliveryRequest(t, a, "set", "broadcast-retry", 2)); len(nodes) != 1 || nodes[0] != c.node {
		t.Fatal(nodes)
	}
	retry := receive(t, c)
	if retry.ID != "broadcast-retry" || retry.Rev != 2 || retry.Stream != "" || retry.To != c.node || string(retry.Content) != `{"x":1,"y":2}` {
		t.Fatal(retry)
	}
	// Acknowledged and newly joined peers receive no replay.
	for _, other := range []testClient{b, joined} {
		send(t, other, lime.Envelope{ID: "barrier", Method: "get", URI: "/peers"})
		if got := receive(t, other); got.ID != "barrier" {
			t.Fatal("retry reached wrong peer", got)
		}
	}
	send(t, c, lime.Envelope{ID: "broadcast-retry", Rev: 2, To: a.node, Event: "received", Scope: "session"})
	if got := receive(t, a); got.Scope != "session" || got.From != c.node {
		t.Fatal(got)
	}
	if nodes := pendingNodes(t, deliveryRequest(t, a, "get", "broadcast-retry", 2)); len(nodes) != 0 {
		t.Fatal(nodes)
	}
	if nodes := pendingNodes(t, deliveryRequest(t, a, "set", "broadcast-retry", 2)); len(nodes) != 0 {
		t.Fatal(nodes)
	}
}
func TestDisconnectedBroadcastRecipientIsNotAcknowledged(t *testing.T) {
	_, s := demo(t)
	a, b, c := client(t, s), client(t, s), client(t, s)
	send(t, a, lime.Envelope{ID: "disconnect-broadcast", Type: "text", Content: []byte(`"original"`)})
	_ = receive(t, b)
	_ = receive(t, c)
	send(t, b, lime.Envelope{ID: "disconnect-broadcast", To: a.node, Event: "received"})
	_ = receive(t, a)
	send(t, c, lime.Envelope{ID: c.session, State: "finishing"})
	_ = receive(t, c)
	// A new node never replaces the unacknowledged original session.
	joined := client(t, s)
	if nodes := pendingNodes(t, deliveryRequest(t, a, "get", "disconnect-broadcast", 1)); len(nodes) != 1 || nodes[0] != c.node {
		t.Fatal(nodes)
	}
	if got := deliveryRequest(t, a, "set", "disconnect-broadcast", 1); got.Status != "failure" {
		t.Fatal("disconnected recipient treated as success", got)
	}
	send(t, joined, lime.Envelope{ID: "barrier", Method: "get", URI: "/peers"})
	if got := receive(t, joined); got.ID != "barrier" {
		t.Fatal(got)
	}
	// Whole-message retries also retain the frozen original recipient list.
	send(t, a, lime.Envelope{ID: "disconnect-broadcast", Type: "text", Content: []byte(`"original"`)})
	if got := receive(t, a); got.Event != "failed" {
		t.Fatal(got)
	}
}
func TestPartialBroadcastStartFailureClosesOnlyStartedRecipients(t *testing.T) {
	h, s := demo(t)
	a, b, c := client(t, s), client(t, s), client(t, s)
	if b.node > c.node {
		b, c = c, b
	}
	congested := h.find(c.node)
	for i := 0; i < 256; i++ {
		if err := congested.tracker.Track(lime.Envelope{ID: fmt.Sprintf("busy-%d", i), From: a.node, To: c.node, Type: "text/plain", Content: []byte(`"busy"`)}); err != nil {
			t.Fatal(err)
		}
	}
	send(t, a, lime.Envelope{ID: "partial", Type: "text", Stream: "start"})
	if got := receive(t, b); got.Stream != "start" {
		t.Fatal(got)
	}
	if got := receive(t, a); got.Event != "failed" || !strings.Contains(got.Reason.Description, "limit") {
		t.Fatal(got)
	}
	if _, err := b.conn.Receive(timeout(t)); err == nil {
		t.Fatal("orphan stream session stayed alive")
	}
	send(t, c, lime.Envelope{ID: "alive", Method: "get", URI: "/peers"})
	if got := receive(t, c); got.ID != "alive" {
		t.Fatal("unstarted recipient closed", got)
	}
}
func TestPartialBroadcastTransportFailureClosesActiveStreams(t *testing.T) {
	_, s := demo(t)
	a, b, c := client(t, s), client(t, s), client(t, s)
	if b.node > c.node {
		b, c = c, b
	}
	send(t, a, lime.Envelope{ID: "transport", Type: "text", Stream: "start"})
	_ = receive(t, b)
	_ = receive(t, c)
	_ = c.conn.Close()
	send(t, a, lime.Envelope{ID: "transport", Stream: "data", Content: []byte(`"partial"`)})
	if got := receive(t, b); got.Stream != "data" {
		t.Fatal(got)
	}
	if got := receive(t, a); got.Event != "failed" {
		t.Fatal(got)
	}
	if _, err := b.conn.Receive(timeout(t)); err == nil {
		t.Fatal("active stream survived partial transport failure")
	}
}
func TestPartialBroadcastEndFailurePreservesCompletedRecipients(t *testing.T) {
	h, s := demo(t)
	a, b, c := client(t, s), client(t, s), client(t, s)
	if b.node > c.node {
		b, c = c, b
	}
	congested := h.find(c.node)
	for i := 0; i < 8; i++ {
		size := (1 << 20) - 2
		if i == 7 {
			size--
		}
		content := append([]byte{'"'}, bytes.Repeat([]byte{'x'}, size)...)
		content = append(content, '"')
		if err := congested.tracker.Track(lime.Envelope{ID: fmt.Sprintf("bytes-%d", i), From: a.node, To: c.node, Type: "text/plain", Content: content}); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range []lime.Envelope{{ID: "end-failure", Type: "text", Stream: "start"}, {ID: "end-failure", Stream: "data", Content: []byte(`"complete for the healthy peer"`)}} {
		send(t, a, e)
		_ = receive(t, b)
		_ = receive(t, c)
	}
	send(t, a, lime.Envelope{ID: "end-failure", Stream: "end"})
	if got := receive(t, b); got.Stream != "end" {
		t.Fatal(got)
	}
	if got := receive(t, a); got.Event != "failed" || !strings.Contains(got.Reason.Description, "byte limit") {
		t.Fatal(got)
	}
	if _, err := c.conn.Receive(timeout(t)); err == nil {
		t.Fatal("unfinished recipient stayed connected")
	}
	send(t, b, lime.Envelope{ID: "completed-alive", Method: "get", URI: "/peers"})
	if got := receive(t, b); got.ID != "completed-alive" {
		t.Fatal("completed recipient closed", got)
	}
}
func TestSenderDisconnectAbandonsRoutedStreams(t *testing.T) {
	_, s := demo(t)
	a, b := client(t, s), client(t, s)
	send(t, a, lime.Envelope{ID: "abandoned", To: b.node, Type: "text", Stream: "start"})
	_ = receive(t, b)
	_ = a.conn.Close()
	if _, err := b.conn.Receive(timeout(t)); err == nil {
		t.Fatal("sender disconnect orphaned a live stream")
	}
}
func TestDeliveryCommandsValidateAndIsolateMarkers(t *testing.T) {
	_, s := demo(t)
	a, b := client(t, s), client(t, s)
	send(t, a, lime.Envelope{ID: "owned", To: b.node, Type: "text", Content: []byte(`"owned"`)})
	_ = receive(t, b)
	for _, e := range []lime.Envelope{
		{Method: "delete", Type: "json", Resource: []byte(`{"id":"owned"}`)},
		{Method: "get"},
		{Method: "get", Type: "text", Resource: []byte(`"owned"`)},
		{Method: "get", Type: "json", Resource: []byte(`{"id":"owned","extra":true}`)},
		{Method: "get", Type: "json", Resource: []byte(`{"id":"owned","rev":0}`)},
		{Method: "get", Type: "json", Resource: []byte(`{"id":"owned","rev":9007199254740992}`)},
		{Method: "get", Type: "json", Resource: []byte(`{"id":""}`)},
		{Method: "get", Type: "json", Resource: []byte(`{"id":"unknown"}`)},
	} {
		e.ID = "invalid"
		e.URI = deliveryURI
		send(t, a, e)
		if got := receive(t, a); got.Status != "failure" || got.Reason == nil {
			t.Fatal(got)
		}
	}
	send(t, a, lime.Envelope{ID: "default-rev", Method: "get", URI: deliveryURI, Type: "json", Resource: []byte(`{"id":"owned"}`)})
	if nodes := pendingNodes(t, receive(t, a)); len(nodes) != 1 || nodes[0] != b.node {
		t.Fatal(nodes)
	}
	if got := deliveryRequest(t, b, "get", "owned", 1); got.Status != "failure" {
		t.Fatal("cross-sender access", got)
	}
	send(t, b, lime.Envelope{ID: "owned", To: a.node, Event: "failed", Reason: &lime.Reason{Code: lime.InvalidInput}})
	_ = receive(t, a)
	if got := deliveryRequest(t, a, "set", "owned", 1); got.Status != "failure" {
		t.Fatal("failed delivery retried", got)
	}
}
func TestSenderDeliveryWindowRetiresOnlyAcknowledgedMarkers(t *testing.T) {
	_, s := demo(t)
	a, b, c := client(t, s), client(t, s), client(t, s)
	for i := 0; i < 257; i++ {
		id := fmt.Sprintf("window-%d", i)
		target := c
		if i == 0 {
			target = b
		}
		send(t, a, lime.Envelope{ID: id, To: target.node, Type: "text", Content: []byte(`"window"`)})
		_ = receive(t, target)
		if i != 0 {
			send(t, target, lime.Envelope{ID: id, To: a.node, Event: "received"})
			_ = receive(t, a)
		}
	}
	if nodes := pendingNodes(t, deliveryRequest(t, a, "get", "window-0", 1)); len(nodes) != 1 {
		t.Fatal("unreceived marker evicted", nodes)
	}
	if got := deliveryRequest(t, a, "get", "window-1", 1); got.Status != "failure" {
		t.Fatal("old received marker did not retire", got)
	}
}

func TestSenderDeliveryLimitPreservesUnreceivedMessages(t *testing.T) {
	_, s := demo(t)
	a, b := client(t, s), client(t, s)
	for i := 0; i < 256; i++ {
		send(t, a, lime.Envelope{ID: fmt.Sprintf("unreceived-%d", i), To: b.node, Type: "text", Content: []byte(`"unreceived"`)})
		_ = receive(t, b)
	}
	send(t, a, lime.Envelope{ID: "overflow", To: b.node, Type: "text", Content: []byte(`"overflow"`)})
	if got := receive(t, a); got.Event != "failed" || !strings.Contains(got.Reason.Description, "sender delivery limit") {
		t.Fatal(got)
	}
	if nodes := pendingNodes(t, deliveryRequest(t, a, "get", "unreceived-0", 1)); len(nodes) != 1 {
		t.Fatal("capacity dropped pending delivery", nodes)
	}
	send(t, b, lime.Envelope{ID: "still-alive", Method: "get", URI: "/peers"})
	if got := receive(t, b); got.ID != "still-alive" {
		t.Fatal("over-limit message was forwarded", got)
	}
}
func TestCanceledRetryRetainsDeliveryAndClosesFailedTransport(t *testing.T) {
	h, s := demo(t)
	a, b := client(t, s), client(t, s)
	send(t, a, lime.Envelope{ID: "canceled", To: b.node, Type: "text", Content: []byte(`"canceled"`)})
	_ = receive(t, b)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response := h.command(ctx, h.find(a.node), lime.Envelope{ID: "cancel-retry", Method: "set", URI: deliveryURI, Type: "json", Resource: []byte(`{"id":"canceled"}`)})
	if response.Status != "failure" || !strings.Contains(response.Reason.Description, "context canceled") {
		t.Fatal(response)
	}
	if _, err := b.conn.Receive(timeout(t)); err == nil {
		t.Fatal("canceled retry left failed transport open")
	}
	if nodes := pendingNodes(t, deliveryRequest(t, a, "get", "canceled", 1)); len(nodes) != 1 || nodes[0] != b.node {
		t.Fatal("cancellation acknowledged delivery", nodes)
	}
}

func TestStreamedAliasCommandDoesNotExecuteBeforeEnd(t *testing.T) {
	_, s := demo(t)
	a := client(t, s)
	send(t, a, lime.Envelope{ID: "register", Method: "set", URI: lime.AliasURI, Type: "json", Stream: "start"})
	send(t, a, lime.Envelope{ID: "register", Method: "set", Stream: "data", Resource: []byte(`[{"op":"add","path":"/plain","value":"text/plain"}]`)})
	send(t, a, lime.Envelope{ID: "before", Method: "get", URI: lime.AliasURI})
	before := receive(t, a)
	if before.ID != "before" || strings.Contains(string(before.Resource), "plain") {
		t.Fatal("partial request executed", before)
	}
	send(t, a, lime.Envelope{ID: "register", Method: "set", Stream: "end"})
	if r := receive(t, a); r.ID != "register" || r.Status != "success" {
		t.Fatal(r)
	}
	send(t, a, lime.Envelope{ID: "after", Method: "get", URI: lime.AliasURI})
	if r := receive(t, a); !strings.Contains(string(r.Resource), "plain") {
		t.Fatal("completed request missing", r)
	}
	send(t, a, lime.Envelope{ID: "bad", Method: "set", URI: lime.AliasURI, Type: "json", Stream: "start"})
	send(t, a, lime.Envelope{ID: "bad", Method: "set", Stream: "data", Resource: []byte(`[{"op":"remove","path":"/absent"}]`)})
	if r := receive(t, a); r.Status != "failure" || r.ID != "bad" {
		t.Fatal(r)
	}
	send(t, a, lime.Envelope{ID: "bad", Method: "set", Stream: "end"})
	if r := receive(t, a); r.Status != "failure" {
		t.Fatal("failed input invoked", r)
	}
}
