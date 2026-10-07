package lime

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func registry(t *testing.T, l Limits) *Registry {
	t.Helper()
	r, err := NewRegistry(l)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func assembler(t *testing.T, l Limits) *Assembler {
	t.Helper()
	a, err := NewAssembler(registry(t, l), l)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func apply(t *testing.T, a *Assembler, s string) Result {
	t.Helper()
	r, err := a.Apply(mustDecode(t, s))
	if err != nil {
		t.Fatal(s, err)
	}
	return r
}
func TestStreamLifecycle(t *testing.T) {
	a := assembler(t, Limits{})
	commits := 0
	a.Commit = func(e Envelope) error { commits++; return nil }
	start := `{"id":"m","from":"alice","to":"bob","thread":"t","type":"text","stream":"start","rev":2}`
	if r := apply(t, a, start); r.Complete || a.Active() != 1 {
		t.Fatal("start completed")
	}
	for _, s := range []string{`{"id":"m","from":"alice","rev":2,"stream":"data","content":"Olá "}`, `{"id":"m","from":"alice","rev":2,"stream":"data","content":"😀"}`} {
		if r := apply(t, a, s); r.Complete {
			t.Fatal("data completed")
		}
	}
	if commits != 0 {
		t.Fatal("premature commit")
	}
	end := apply(t, a, `{"id":"m","from":"alice","rev":2,"stream":"end"}`)
	if !end.Complete || end.Duplicate || string(end.Message.Content) != `"Olá 😀"` || end.Message.Type != "text/plain" || end.Message.Thread != "t" || commits != 1 || a.Active() != 0 {
		t.Fatalf("bad completion %+v", end)
	}
	dup, err := a.Apply(end.Message)
	if err != nil || !dup.Duplicate || commits != 1 {
		t.Fatal("duplicate re-committed", err)
	}
	changed := clone(end.Message)
	changed.Content = []byte(`"changed"`)
	if _, err = a.Apply(changed); err == nil {
		t.Fatal("conflicting revision")
	}
	end.Message.ID = ""
	if _, err = a.Apply(end.Message); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Apply(end.Message); err != nil || commits != 3 {
		t.Fatal("IDless was deduplicated")
	}
	if _, err = a.Apply(mustDecode(t, start)); err == nil {
		t.Fatal("stream retry accepted")
	}
	a.Reset()
	if a.Active() != 0 {
		t.Fatal("reset")
	}
	// Revisions and sender namespaces never borrow an active stream's type.
	apply(t, a, start)
	for _, s := range []string{`{"id":"m","from":"alice","stream":"end"}`, `{"id":"m","from":"mallory","rev":2,"stream":"end"}`} {
		if _, err := a.Apply(mustDecode(t, s)); err == nil {
			t.Fatal("ambiguous stream", s)
		}
	}
	if a.Active() != 1 {
		t.Fatal("unrelated marker removed stream")
	}
	a.Discard(mustDecode(t, start))
	if a.Active() != 0 {
		t.Fatal("discard")
	}
}
func TestJSONStreamingFreshRevision(t *testing.T) {
	a := assembler(t, Limits{})
	for _, s := range []string{`{"id":"j","type":"json","stream":"start"}`, `{"id":"j","stream":"data","content":{"a":{"b":1,"c":2},"options":[1,2]}}`, `{"id":"j","stream":"data","content":{"a":{"b":null},"options":[3]}}`} {
		apply(t, a, s)
	}
	r := apply(t, a, `{"id":"j","stream":"end"}`)
	if string(r.Message.Content) != `{"a":{"c":2},"options":[3]}` {
		t.Fatal(string(r.Message.Content))
	}
	apply(t, a, `{"id":"j","rev":2,"type":"json","stream":"start"}`)
	r = apply(t, a, `{"id":"j","rev":2,"stream":"end"}`)
	if string(r.Message.Content) != `{}` {
		t.Fatal("revision inherited previous content")
	}
	apply(t, a, `{"id":"scalar","type":"json","stream":"start"}`)
	apply(t, a, `{"id":"scalar","stream":"data","content":null}`)
	r = apply(t, a, `{"id":"scalar","stream":"end"}`)
	if string(r.Message.Content) != `null` {
		t.Fatal("null changed")
	}
}
func TestAssemblerFailures(t *testing.T) {
	for _, s := range []string{`{"id":"m","stream":"data","content":null}`, `{"id":"m","stream":"data","content":1}`, `{"id":"m","stream":"data","to":"other","content":"x"}`, `{"id":"m","stream":"data","thread":"other","content":"x"}`, `{"id":"m","stream":"data","pp":"other","content":"x"}`, `{"id":"m","type":"text","content":"x"}`, `{"id":"m","type":"text","stream":"start"}`} {
		a := assembler(t, Limits{})
		apply(t, a, `{"id":"m","type":"text","stream":"start"}`)
		if _, err := a.Apply(mustDecode(t, s)); err == nil || a.Active() != 0 {
			t.Fatal("did not discard rejected stream", s, err)
		}
	}
	for _, l := range []Limits{{ContentBytes: 3}, {Entries: 1}} {
		a := assembler(t, l)
		apply(t, a, `{"id":"m","type":"text","stream":"start"}`)
		apply(t, a, `{"id":"m","stream":"data","content":"a"}`)
		if _, err := a.Apply(mustDecode(t, `{"id":"m","stream":"data","content":"long"}`)); err == nil || a.Active() != 0 {
			t.Fatal("limit did not discard")
		}
	}
	a := assembler(t, Limits{Streams: 1})
	apply(t, a, `{"id":"m","type":"text","stream":"start"}`)
	if _, err := a.Apply(mustDecode(t, `{"id":"other","type":"text","stream":"start"}`)); err == nil {
		t.Fatal("stream limit")
	}
	if _, err := a.Apply(mustDecode(t, `{"id":"bad","type":"unknown","stream":"start"}`)); err == nil {
		t.Fatal("unsupported type")
	}
	a = assembler(t, Limits{})
	if _, err := a.Apply(mustDecode(t, `{"id":"bad","type":"unknown","stream":"start"}`)); err == nil {
		t.Fatal("unknown")
	}
	if _, err := a.Apply(Envelope{}); err == nil {
		t.Fatal("invalid")
	}
	if _, err := a.Apply(mustDecode(t, `{"state":"new"}`)); err == nil {
		t.Fatal("not a message")
	}
	for _, s := range []string{`{"type":"bad","content":{}}`, `{"type":"text","content":{}}`} {
		if _, err := a.Apply(mustDecode(t, s)); err == nil {
			t.Fatal("bad content accepted")
		}
	}
	a = assembler(t, Limits{ContentBytes: 2})
	if _, err := a.Apply(mustDecode(t, `{"type":"text","content":"long"}`)); err == nil {
		t.Fatal("complete limit")
	}
	apply(t, a, `{"id":"j","type":"json","stream":"start"}`)
	if _, err := a.Apply(mustDecode(t, `{"id":"j","stream":"data","content":{"long":1}}`)); err == nil {
		t.Fatal("patch limit")
	}
	a = assembler(t, Limits{})
	a.Commit = func(Envelope) error { return errors.New("storage unavailable") }
	apply(t, a, `{"id":"m","type":"text","stream":"start"}`)
	if _, err := a.Apply(mustDecode(t, `{"id":"m","stream":"end"}`)); err == nil || a.Active() != 0 {
		t.Fatal("false completion")
	}
	a = assembler(t, Limits{Entries: 1})
	apply(t, a, `{"id":"a","type":"text","content":"a"}`)
	apply(t, a, `{"id":"b","type":"text","content":"b"}`)
	r := apply(t, a, `{"id":"a","type":"text","content":"a"}`)
	if r.Duplicate {
		t.Fatal("eviction window did not advance")
	}
	if _, err := NewAssembler(nil, Limits{}); err == nil {
		t.Fatal("nil registry")
	}
	if _, err := NewAssembler(registry(t, Limits{}), Limits{Streams: -1}); err == nil {
		t.Fatal("negative")
	}
}
func TestRFC7396Vectors(t *testing.T) {
	vectors := [][3]string{
		{`{"a":"b"}`, `{"a":"c"}`, `{"a":"c"}`}, {`{"a":"b"}`, `{"b":"c"}`, `{"a":"b","b":"c"}`},
		{`{"a":"b"}`, `{"a":null}`, `{}`}, {`{"a":"b","b":"c"}`, `{"a":null}`, `{"b":"c"}`},
		{`{"a":["b"]}`, `{"a":"c"}`, `{"a":"c"}`}, {`{"a":"c"}`, `{"a":["b"]}`, `{"a":["b"]}`},
		{`{"a":{"b":"c"}}`, `{"a":{"b":"d","c":null}}`, `{"a":{"b":"d"}}`},
		{`{"a":[{"b":"c"}]}`, `{"a":[1]}`, `{"a":[1]}`},
		{`["a","b"]`, `["c","d"]`, `["c","d"]`}, {`{"a":"b"}`, `["c"]`, `["c"]`},
		{`{"a":"foo"}`, `null`, `null`}, {`{"a":"foo"}`, `"bar"`, `"bar"`},
		{`{"e":null}`, `{"a":1}`, `{"a":1,"e":null}`}, {`[1,2]`, `{"a":"b","c":null}`, `{"a":"b"}`},
		{`{}`, `{"a":{"bb":{"ccc":null}}}`, `{"a":{"bb":{}}}`},
		{`{}`, `{"n":9007199254740993123456789}`, `{"n":9007199254740993123456789}`},
	}
	for _, v := range vectors {
		out, err := MergePatch([]byte(v[0]), []byte(v[1]))
		if err != nil || !bytes.Equal(out, []byte(v[2])) {
			t.Fatal(v, string(out), err)
		}
	}
	for _, v := range [][2]string{{`bad`, `{}`}, {`{}`, `bad`}, {`{}`, `{"a":1,"a":2}`}} {
		if _, err := MergePatch([]byte(v[0]), []byte(v[1])); err == nil {
			t.Fatal("invalid merge")
		}
	}
	target := json.RawMessage(`{"a":1}`)
	out, _ := MergePatch(target, []byte(`[1]`))
	out[0] = 'x'
	if string(target) != `{"a":1}` {
		t.Fatal("alias")
	}
}

func TestProgressReturnsContribution(t *testing.T) {
	a := assembler(t, Limits{})
	apply(t, a, `{"id":"m","type":"text","stream":"start"}`)
	apply(t, a, `{"id":"m","stream":"data","content":"first"}`)
	r := apply(t, a, `{"id":"m","stream":"data","content":"second"}`)
	if r.Message.Stream != "data" || string(r.Message.Content) != `"second"` {
		t.Fatal("copied accumulated text into every result")
	}
	if r := apply(t, a, `{"id":"m","stream":"end"}`); string(r.Message.Content) != `"firstsecond"` {
		t.Fatal("assembly lost content")
	}
}
func BenchmarkTextStreaming(b *testing.B) {
	r, _ := NewRegistry(Limits{})
	a, _ := NewAssembler(r, Limits{})
	start := Envelope{ID: "m", Type: "text", Stream: "start"}
	data := Envelope{ID: "m", Stream: "data", Content: []byte(`"Hello"`)}
	end := Envelope{ID: "m", Stream: "end"}
	b.ReportAllocs()
	for b.Loop() {
		a.Reset()
		_, _ = a.Apply(start)
		for range 100 {
			_, _ = a.Apply(data)
		}
		if _, err := a.Apply(end); err != nil {
			b.Fatal(err)
		}
	}
}
func TestTextEscapesAndChunkOwnership(t *testing.T) {
	a := assembler(t, Limits{})
	apply(t, a, `{"id":"m","type":"text","stream":"start"}`)
	e := mustDecode(t, `{"id":"m","stream":"data","content":"Olá"}`)
	if _, err := a.Apply(e); err != nil {
		t.Fatal(err)
	}
	clear(e.Content)
	apply(t, a, `{"id":"m","stream":"data","content":"\n\uD83D\uDE00\t\\\""}`)
	result := apply(t, a, `{"id":"m","stream":"end"}`)
	var text string
	_ = json.Unmarshal(result.Message.Content, &text)
	if text != "Olá\n😀\t\\\"" {
		t.Fatal(text)
	}
}
func TestRetryJSONValueEquality(t *testing.T) {
	a := assembler(t, Limits{})
	apply(t, a, `{"id":"j","type":"json","stream":"start"}`)
	apply(t, a, `{"id":"j","stream":"data","content":{"text":"Choose <topic>","options":["Payments"],"n":9007199254740993123}}`)
	apply(t, a, `{"id":"j","stream":"end"}`)
	retry := apply(t, a, `{"id":"j","type":"json","content":{"text":"Choose <topic>", "n":9007199254740993123,"options":["Payments"]}}`)
	if !retry.Duplicate {
		t.Fatal("property order changed revision identity")
	}
	apply(t, a, `{"id":"text","type":"text","content":"Olá <a>"}`)
	retry = apply(t, a, `{"id":"text","type":"text","content":"Ol\u00e1 \u003ca\u003e"}`)
	if !retry.Duplicate {
		t.Fatal("string escaping changed revision identity")
	}
	apply(t, a, `{"id":"null","type":"json","content":null}`)
	if _, err := a.Apply(mustDecode(t, `{"id":"null","type":"json","content":"null"}`)); err == nil {
		t.Fatal("string and JSON null collided")
	}
}
