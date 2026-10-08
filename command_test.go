package lime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func commandLab(t *testing.T, l Limits) *CommandAssembler {
	t.Helper()
	r, err := NewRegistry(l)
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewCommandAssembler(r, l)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func applyCommand(t *testing.T, a *CommandAssembler, e Envelope, d CommandDirection) CommandResult {
	t.Helper()
	r, err := a.Apply(e, d)
	if err != nil {
		t.Fatal(err, e)
	}
	return r
}
func TestCommandSharedVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/command-stream-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name  string
		Steps []struct {
			Direction string
			Envelope  json.RawMessage
			Expect    map[string]any
			Error     bool
		}
		Active int
	}
	if err = json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			a := commandLab(t, Limits{})
			for _, s := range v.Steps {
				var e Envelope
				if err := json.Unmarshal(s.Envelope, &e); err != nil {
					t.Fatal(err)
				}
				direction := IncomingCommand
				if s.Direction == "outgoing" {
					direction = OutgoingCommand
				}
				decoded, wireErr := Decode(s.Envelope)
				if wireErr == nil {
					if k, _ := decoded.Kind(); k != Command {
						t.Fatal("command classified as another family", k)
					}
				} else if !s.Error {
					t.Fatal(wireErr)
				}
				r, err := a.Apply(e, direction)
				if s.Error {
					if err == nil {
						t.Fatal("expected rejection", e)
					}
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				if r.Complete != (s.Expect != nil) {
					t.Fatal("completion", r)
				}
				if s.Expect != nil {
					out, _ := json.Marshal(r.Command)
					var fields map[string]any
					_ = json.Unmarshal(out, &fields)
					for k, want := range s.Expect {
						if k == "noResource" {
							if r.Command.Resource != nil {
								t.Fatal("partial failure resource")
							}
							continue
						}
						if !reflect.DeepEqual(fields[k], want) {
							t.Fatal(k, fields[k], want)
						}
					}
				}
			}
			if a.Active() != v.Active {
				t.Fatal("active exchanges", a.Active(), v.Active)
			}
			a.Reset()
			if a.Active() != 0 {
				t.Fatal("reset")
			}
		})
	}
}
func TestCommandBoundsAndCleanup(t *testing.T) {
	r, _ := NewRegistry(Limits{})
	if _, err := NewCommandAssembler(nil, Limits{}); err == nil {
		t.Fatal("nil registry")
	}
	if _, err := NewCommandAssembler(r, Limits{Entries: -1}); err == nil {
		t.Fatal("negative limit")
	}
	a := commandLab(t, Limits{Entries: 2, Streams: 1, ContentBytes: 100})
	req := Envelope{ID: "a", Method: "set", URI: "/x", Type: "text", Stream: "start"}
	applyCommand(t, a, req, IncomingCommand)
	if _, err := a.Apply(Envelope{ID: "b", Method: "set", URI: "/x", Type: "json", Stream: "start"}, IncomingCommand); err == nil {
		t.Fatal("stream capacity")
	}
	for i := 0; i < 2; i++ {
		applyCommand(t, a, Envelope{ID: "a", Method: "set", Stream: "data", Resource: []byte(`""`)}, IncomingCommand)
	}
	if _, err := a.Apply(Envelope{ID: "a", Method: "set", Stream: "data", Resource: []byte(`""`)}, IncomingCommand); err == nil {
		t.Fatal("contribution capacity")
	}
	if a.Active() != 0 {
		t.Fatal("failed stream retained")
	}
	for _, id := range []string{"a", "b"} {
		applyCommand(t, a, Envelope{ID: id, Method: "get", URI: "/x"}, OutgoingCommand)
	}
	if _, err := a.Apply(Envelope{ID: "c", Method: "get", URI: "/x"}, OutgoingCommand); err == nil {
		t.Fatal("exchange capacity")
	}
	a.Discard(Envelope{ID: "a"}, OutgoingCommand)
	if a.Active() != 1 {
		t.Fatal("discard")
	}
	a.Reset()
	for _, value := range []Envelope{
		{ID: "a", Method: "set", URI: "/x", Type: "json", Resource: []byte(`"` + strings.Repeat("x", 101) + `"`)},
		{ID: "a", Method: "set", URI: "/x", Type: "unknown", Resource: []byte(`{}`)},
		{ID: "a", Method: "set", URI: "/x", Type: "media-link", Stream: "start"},
		{ID: "a", Method: "set", URI: "/x", Type: "unknown", Stream: "start"},
	} {
		if _, err := a.Apply(value, IncomingCommand); err == nil {
			t.Fatal("unbounded/unsupported resource", value)
		}
		if a.Active() != 0 {
			t.Fatal("retained rejected request")
		}
	}
	if _, err := a.Apply(req, 0); err == nil {
		t.Fatal("invalid direction")
	}
	if _, err := a.Apply(Envelope{Type: "text", Content: []byte(`"x"`)}, IncomingCommand); err == nil {
		t.Fatal("requires command")
	}
	if got := applyCommand(t, a, Envelope{Method: "observe", URI: "/x"}, IncomingCommand); !got.Complete || got.Response || a.Active() != 0 {
		t.Fatal("ID-less complete observe", got)
	}
	if got := applyCommand(t, a, Envelope{ID: "unsolicited", Method: "get", Status: "success"}, IncomingCommand); !got.Response || !got.Complete {
		t.Fatal("ordinary response", got)
	}
}
func TestCommandResourceBoundsRoutingSchemaAndOwnership(t *testing.T) {
	for _, kind := range []string{"text", "json"} {
		t.Run(kind, func(t *testing.T) {
			a := commandLab(t, Limits{ContentBytes: 80})
			start := Envelope{ID: "c", Method: "set", From: "peer", To: "local", URI: "/x", Type: kind, Stream: "start"}
			applyCommand(t, a, start, IncomingCommand)
			data := Envelope{ID: "c", Method: "set", From: "peer", To: "local", Stream: "data", Resource: []byte(`"` + strings.Repeat("x", 90) + `"`)}
			if _, err := a.Apply(data, IncomingCommand); err == nil {
				t.Fatal("oversized contribution")
			}
			if a.Active() != 0 {
				t.Fatal("retained")
			}
			applyCommand(t, a, start, IncomingCommand)
			data.Resource = []byte(`"` + strings.Repeat("x", 50) + `"`)
			if kind == "json" {
				data.Resource = []byte(`[{"op":"remove","path":""}]`)
			}
			applyCommand(t, a, data, IncomingCommand)
			end := Envelope{ID: "c", Method: "set", From: "peer", Stream: "end"}
			if kind == "text" {
				if _, err := a.Apply(data, IncomingCommand); err == nil {
					t.Fatal("assembled text bound")
				}
			} else {
				if _, err := a.Apply(end, IncomingCommand); err == nil {
					t.Fatal("missing root")
				}
			}
			applyCommand(t, a, start, IncomingCommand)
			data.To = "wrong"
			if _, err := a.Apply(data, IncomingCommand); err == nil {
				t.Fatal("changed routing")
			}
			if a.Active() != 0 {
				t.Fatal("routing cleanup")
			}
		})
	}
	r, _ := NewRegistry(Limits{})
	_ = r.Support("application/vnd.test+json", func(raw json.RawMessage) error {
		if !strings.Contains(string(raw), `"ok":true`) {
			return errors.New("schema rejected")
		}
		return nil
	})
	a, _ := NewCommandAssembler(r, Limits{})
	start := Envelope{ID: "c", Method: "set", URI: "/x", Type: "application/vnd.test+json", Stream: "start"}
	applyCommand(t, a, start, IncomingCommand)
	applyCommand(t, a, Envelope{ID: "c", Method: "set", Stream: "data", Resource: []byte(`[{"op":"add","path":"/ok","value":false}]`)}, IncomingCommand)
	if _, err := a.Apply(Envelope{ID: "c", Method: "set", Stream: "end"}, IncomingCommand); err == nil {
		t.Fatal("schema before invocation")
	}
	if a.Active() != 0 {
		t.Fatal("schema cleanup")
	}
	applyCommand(t, a, start, IncomingCommand)
	raw := []byte(`[{"op":"add","path":"/ok","value":true}]`)
	applyCommand(t, a, Envelope{ID: "c", Method: "set", Stream: "data", Resource: raw}, IncomingCommand)
	clear(raw)
	result := applyCommand(t, a, Envelope{ID: "c", Method: "set", Stream: "end"}, IncomingCommand)
	if string(result.Command.Resource) != `{"ok":true}` {
		t.Fatal("input ownership", result)
	}
	// Equal IDs at different peers stay isolated; callback content cannot change state.
	a.Reset()
	for _, peer := range []string{"one", "two"} {
		applyCommand(t, a, Envelope{ID: "c", From: peer, Method: "get", URI: "/x"}, IncomingCommand)
	}
	applyCommand(t, a, Envelope{ID: "c", To: "one", Method: "get", Status: "success"}, OutgoingCommand)
	if a.Active() != 1 {
		t.Fatal("peer isolation")
	}
	a.Reset()
}
func TestCommandWebSocketBothDirections(t *testing.T) {
	calls := make(chan Envelope, 1)
	done := make(chan error, 1)
	s := wsServer(t, func(c *Conn) {
		a := commandLab(t, Limits{})
		defer a.Reset()
		send := func(e Envelope) error {
			if _, err := a.Apply(e, OutgoingCommand); err != nil {
				return err
			}
			return c.Send(timeout(t), e)
		}
		for {
			e, err := c.Receive(timeout(t))
			if err != nil {
				done <- err
				return
			}
			r, err := a.Apply(e, IncomingCommand)
			if err != nil {
				done <- err
				return
			}
			if !r.Complete {
				continue
			}
			if r.Command.URI == "/probe" {
				doneErr := send(Envelope{ID: e.ID, From: "server", To: "client", Method: e.Method, Status: "success"})
				if doneErr != nil {
					done <- doneErr
					return
				}
				continue
			}
			calls <- r.Command
			for _, out := range []Envelope{
				{ID: e.ID, From: "server", To: "client", Method: e.Method, Type: "json", Stream: "start"},
				{ID: e.ID, From: "server", To: "client", Method: e.Method, Stream: "data", Resource: []byte(`[{"op":"add","path":"/items","value":[null]},{"op":"add","path":"/items/-","value":2}]`)},
				{ID: e.ID, From: "server", To: "client", Method: e.Method, Stream: "end", Status: "success"},
			} {
				if err = send(out); err != nil {
					done <- err
					return
				}
			}
			if a.Active() != 0 {
				done <- errors.New("server retained exchange")
			} else {
				done <- nil
			}
			return
		}
	})
	c := dialTest(t, s)
	a := commandLab(t, Limits{})
	defer a.Reset()
	send := func(e Envelope) {
		t.Helper()
		applyCommand(t, a, e, OutgoingCommand)
		if err := c.Send(timeout(t), e); err != nil {
			t.Fatal(err)
		}
	}
	send(Envelope{ID: "c", From: "client", To: "server", Method: "set", URI: "/draft", Type: "text", Stream: "start"})
	send(Envelope{ID: "c", From: "client", To: "server", Method: "set", Stream: "data", Resource: []byte(`"Hello\n🌎"`)})
	send(Envelope{ID: "probe", From: "client", To: "server", Method: "get", URI: "/probe"})
	probe, err := c.Receive(timeout(t))
	if err != nil {
		t.Fatal(err)
	}
	if probe.ID != "probe" {
		t.Fatal("premature invocation", probe)
	}
	applyCommand(t, a, probe, IncomingCommand)
	select {
	case <-calls:
		t.Fatal("partial input invoked")
	default:
	}
	send(Envelope{ID: "c", From: "client", To: "server", Method: "set", Stream: "end"})
	request := <-calls
	if string(request.Resource) != `"Hello\n🌎"` {
		t.Fatal(request)
	}
	for i := 0; i < 3; i++ {
		e, err := c.Receive(timeout(t))
		if err != nil {
			t.Fatal(err)
		}
		result := applyCommand(t, a, e, IncomingCommand)
		if result.Complete != (i == 2) {
			t.Fatal("premature response completion", result)
		}
		if i == 2 && string(result.Command.Resource) != `{"items":[null,2]}` {
			t.Fatal(result)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if a.Active() != 0 {
		t.Fatal("caller retained exchange")
	}
	// Already canceled I/O returns an error; the receive-loop owner resets state.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Receive(ctx); err == nil {
		t.Fatal("canceled receive")
	}
}

func TestCommandInterruptedInputDoesNotInvoke(t *testing.T) {
	done := make(chan error, 1)
	s := wsServer(t, func(c *Conn) {
		a := commandLab(t, Limits{})
		defer a.Reset()
		for {
			e, err := c.Receive(timeout(t))
			if err != nil {
				if a.Active() != 1 {
					done <- errors.New("missing interrupted exchange")
					return
				}
				a.Reset()
				if a.Active() != 0 {
					done <- errors.New("disconnect state retained")
				} else {
					done <- nil
				}
				return
			}
			result, err := a.Apply(e, IncomingCommand)
			if err != nil {
				done <- err
				return
			}
			if !result.Complete {
				continue
			}
			if e.ID != "probe" {
				done <- errors.New("interrupted input invoked")
				return
			}
			reply := Envelope{ID: e.ID, Method: e.Method, From: "server", To: "client", Status: "success"}
			if _, err = a.Apply(reply, OutgoingCommand); err == nil {
				err = c.Send(timeout(t), reply)
			}
			if err != nil {
				done <- err
				return
			}
		}
	})
	c := dialTest(t, s)
	for _, e := range []Envelope{{ID: "partial", From: "client", To: "server", Method: "set", URI: "/x", Type: "json", Stream: "start"}, {ID: "partial", From: "client", To: "server", Method: "set", Stream: "data", Resource: []byte(`[{"op":"add","path":"/x","value":1}]`)}, {ID: "probe", From: "client", To: "server", Method: "get", URI: "/probe"}} {
		if err := c.Send(timeout(t), e); err != nil {
			t.Fatal(err)
		}
	}
	if e, err := c.Receive(timeout(t)); err != nil || e.ID != "probe" {
		t.Fatal(e, err)
	}
	_ = c.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func BenchmarkCommandTextStream(b *testing.B) {
	r, _ := NewRegistry(Limits{Entries: 200})
	a, _ := NewCommandAssembler(r, Limits{Entries: 200})
	start := Envelope{ID: "c", From: "peer", Method: "set", URI: "/x", Type: "text", Stream: "start"}
	data := Envelope{ID: "c", From: "peer", Method: "set", Stream: "data", Resource: []byte(`"hello"`)}
	end := Envelope{ID: "c", From: "peer", Method: "set", Stream: "end"}
	reply := Envelope{ID: "c", To: "peer", Method: "set", Status: "success"}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := a.Apply(start, IncomingCommand); err != nil {
			b.Fatal(err)
		}
		for i := 0; i < 100; i++ {
			if _, err := a.Apply(data, IncomingCommand); err != nil {
				b.Fatal(err)
			}
		}
		if result, err := a.Apply(end, IncomingCommand); err != nil || !result.Complete {
			b.Fatal(result, err)
		}
		if _, err := a.Apply(reply, OutgoingCommand); err != nil {
			b.Fatal(err)
		}
	}
}

func TestCompleteAndStreamedCommandsResolveCustomAliasesEqually(t *testing.T) {
	r, _ := NewRegistry(Limits{})
	if err := r.Register(map[string]string{"object": "application/json"}); err != nil {
		t.Fatal(err)
	}
	a, _ := NewCommandAssembler(r, Limits{})
	complete := applyCommand(t, a, Envelope{ID: "complete", Method: "set", URI: "/x", Type: "object", Resource: []byte(`{"x":null}`)}, IncomingCommand)
	applyCommand(t, a, Envelope{ID: "stream", Method: "set", URI: "/x", Type: "object", Stream: "start"}, IncomingCommand)
	applyCommand(t, a, Envelope{ID: "stream", Method: "set", Stream: "data", Resource: []byte(`[{"op":"add","path":"/x","value":null}]`)}, IncomingCommand)
	streamed := applyCommand(t, a, Envelope{ID: "stream", Method: "set", Stream: "end"}, IncomingCommand)
	if complete.Command.Type != "application/json" || streamed.Command.Type != complete.Command.Type || string(streamed.Command.Resource) != string(complete.Command.Resource) {
		t.Fatal(complete, streamed)
	}
	a.Reset()
}
