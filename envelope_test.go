package lime

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func mustDecode(t *testing.T, s string) Envelope {
	t.Helper()
	e, err := Decode([]byte(s))
	if err != nil {
		t.Fatal(s, err)
	}
	return e
}
func TestWireFixtures(t *testing.T) {
	valid := []string{
		`{"state":"new","version":2}`,
		`{"state":"new"}`,
		`{"id":"s1","state":"established","to":"alice"}`,
		`{"id":"s1","state":"authenticating","schemeOptions":["plain"]}`,
		`{"id":"s1","from":"alice","state":"authenticating","scheme":"plain","authentication":{"password":"eA=="}}`,
		`{"id":"s1","state":"finishing"}`, `{"id":"s1","state":"finished"}`,
		`{"state":"failed","reason":{"code":101,"description":"version required"}}`,
		`{"type":"text","content":""}`, `{"type":"json","content":null}`,
		`{"id":"m1","rev":9007199254740991,"thread":"t","type":"text","stream":"start"}`,
		`{"id":"m1","rev":2,"stream":"data","content":null}`,
		`{"id":"m1","stream":"end"}`,
		`{"id":"m1","event":"received"}`, `{"id":"m1","rev":2,"from":"bob","to":"alice","event":"received","scope":"session"}`,
		`{"id":"m1","event":"consumed","scope":"thread","thread":"t"}`,
		`{"id":"m1","event":"failed","reason":{"code":0}}`,
		`{"id":"c","method":"get","uri":"/x"}`, `{"method":"observe","uri":"/x"}`,
		`{"id":"c","method":"set","uri":"/x","type":"json","resource":{"x":[1,true,null]}}`,
		`{"id":"c","method":"get","status":"success","type":"json","resource":[]}`,
		`{"id":"c","method":"delete","status":"failure","reason":{"code":100}}`,
		`{"id":"a","from":"alice","to":"bob","pp":"delegate","metadata":{"key":"value"},"thread":"t","type":"json","content":{"literal":"{\"x\":1}","nested":[{},[1,2]]}}`,
		`{"type":"text","content":"Olá 😀\n\r\t\u0001\"\\<script>"}`,
		`{"ty\u0070e":"json","content":{"\u0061":1}}`,
	}
	for _, s := range valid {
		t.Run(s, func(t *testing.T) {
			e := mustDecode(t, s)
			b, err := Append(make([]byte, 0, 1024), e)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = Decode(b); err != nil {
				t.Fatal(string(b), err)
			}
			var a, c any
			_ = json.Unmarshal([]byte(s), &a)
			_ = json.Unmarshal(b, &c)
			aa, _ := json.Marshal(a)
			cc, _ := json.Marshal(c)
			if !bytes.Equal(aa, cc) {
				t.Fatalf("roundtrip %s != %s", aa, cc)
			}
		})
	}
}
func TestStrictWireRejections(t *testing.T) {
	bad := []string{
		``, `null`, `[]`, `1`, `{"type":"text","content":"x"} {}`,
		`{"type":"text","type":"json","content":"x"}`, `{"type":"text","ty\u0070e":"text","content":"x"}`,
		`{"type":"json","content":{"a":1,"\u0061":2}}`, `{"type":"json","content":[{"a":1,"a":2}]}`,
		`{"type":"text","content":"x","extra":true}`, `{"type":"text"}`, `{}`,
		`{"state":"new","content":"x","type":"text"}`, `{"event":"received","method":"get","uri":"/x","id":"a"}`,
		`{"state":null}`, `{"type":"text","content":"x","id":""}`, `{"type":"text","content":"x","rev":null}`,
		`{"type":"text","content":"x","rev":0}`, `{"type":"text","content":"x","rev":-1}`, `{"type":"text","content":"x","rev":1.1}`, `{"type":"text","content":"x","rev":"2"}`, `{"type":"text","content":"x","rev":9007199254740992}`,
		`{"id":"a","stream":"start"}`, `{"id":"a","type":"text","stream":"start","content":""}`, `{"stream":"start","type":"text"}`,
		`{"id":"a","stream":"data"}`, `{"id":"a","stream":"data","type":"text","content":"x"}`, `{"stream":"data","content":"x"}`,
		`{"stream":"end"}`, `{"id":"a","stream":"end","type":"text"}`, `{"id":"a","stream":"end","content":null}`, `{"id":"a","stream":"oops"}`,
		`{"id":"a","event":"accepted"}`, `{"id":"a","event":"received","scope":"thread","thread":"t"}`,
		`{"event":"received"}`, `{"id":"a","event":"consumed","scope":"session"}`, `{"id":"a","event":"consumed","scope":"thread"}`, `{"id":"a","event":"received","scope":"bad"}`,
		`{"id":"a","event":"failed"}`, `{"id":"a","event":"failed","scope":"session","reason":{"code":1}}`,
		`{"id":"a","event":"received","reason":{"code":1}}`,
		`{"id":"a","event":"failed","reason":{}}`, `{"id":"a","event":"failed","reason":{"code":null}}`, `{"id":"a","event":"failed","reason":{"code":1.5}}`, `{"id":"a","event":"failed","reason":{"code":1,"extra":2}}`,
		`{"id":"a","method":"bad","uri":"/x"}`, `{"method":"get","uri":"/x"}`, `{"id":"a","method":"get"}`, `{"id":"a","method":"get","uri":"/x","status":"success"}`,
		`{"id":"a","method":"get","status":"bad"}`, `{"id":"a","method":"get","status":"failure"}`,
		`{"id":"a","method":"get","uri":"/x","reason":{"code":1}}`,
		`{"id":"a","method":"set","uri":"/x","resource":{}}`, `{"id":"a","method":"get","uri":"/x","type":"json"}`,
		`{"state":"negotiating"}`, `{"state":"established","version":2}`, `{"state":"failed"}`, `{"state":"new","reason":{"code":1}}`,
		`{"state":"authenticating","authentication":{}}`, `{"state":"new","scheme":"plain"}`, `{"state":"new","schemeOptions":["plain"]}`,
		`{"state":"new","rev":2}`, `{"type":"text","content":"x","status":"success"}`, `{"type":"text","content":"x","metadata":[]}`,
	}
	bad = append(bad, `{"type":"json","content":`+strings.Repeat("[", MaxDepth+1)+`0`+strings.Repeat("]", MaxDepth+1)+`}`)
	bad = append(bad, string([]byte{'{', '"', 't', 'y', 'p', 'e', '"', ':', '"', 0xff, '"', '}'}))
	for _, s := range bad {
		t.Run(s, func(t *testing.T) {
			if e, err := Decode([]byte(s)); err == nil {
				t.Fatalf("accepted invalid %s: %+v", s, e)
			}
		})
	}
}
func TestAppendOwnershipAndStrings(t *testing.T) {
	e := Envelope{ID: "quoted\"\\\n\r\t\x00\a\b\f", Type: "text", Content: []byte(`"héllo"`)}
	b, err := Append([]byte("prefix"), e)
	if err != nil {
		t.Fatal(err)
	}
	got := mustDecode(t, string(b[6:]))
	if got.ID != e.ID {
		t.Fatal("string changed")
	}
	original := []byte(`{"type":"text","content":"hello"}`)
	got, err = Decode(original)
	if err != nil {
		t.Fatal(err)
	}
	clear(original)
	if string(got.Content) != `"hello"` {
		t.Fatal("raw bytes alias input")
	}
	for _, e := range []Envelope{
		{Type: "json", Content: []byte(`bad`)}, {Type: "json", Content: []byte(`{}`), Metadata: []byte(`bad`)},
		{Type: "text", Content: []byte(`"x"`), ID: string([]byte{255})},
		{State: "failed", Reason: &Reason{Code: 1, Description: string([]byte{255})}},
		{State: "authenticating", SchemeOptions: []string{string([]byte{255})}},
	} {
		base := []byte("prefix")
		if _, err := Append(base, e); err == nil {
			t.Fatal("invalid value accepted")
		}
		if string(base) != "prefix" {
			t.Fatal("changed caller bytes")
		}
	}
	e = Envelope{Type: "json", Content: []byte(`{"number":9007199254740993123456789}`)}
	if e.Revision() != 1 || e.NotificationScope() != "message" {
		t.Fatal("defaults")
	}
	if _, err := Append(nil, e); err != nil {
		t.Fatal(err)
	}
}
func FuzzCodec(f *testing.F) {
	for _, s := range []string{`{"type":"text","content":""}`, `{"id":"m","stream":"end"}`, `{"type":"json","content":null}`, `{"state":"new","version":2}`, `{"type":"json","content":{"a":{"b":1}}}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 65536 {
			return
		}
		e, err := Decode(b)
		if err != nil {
			return
		}
		out, err := Append(nil, e)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = Decode(out); err != nil {
			t.Fatal(err)
		}
	})
}
func BenchmarkEncodeText(b *testing.B) {
	e := Envelope{ID: "m1", Type: "text/plain", Content: []byte(`"Hello from Lime!"`)}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Append(nil, e); err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkAppendText(b *testing.B) {
	e := Envelope{ID: "m1", Type: "text/plain", Content: []byte(`"Hello from Lime!"`)}
	buf := make([]byte, 0, 256)
	b.ReportAllocs()
	for b.Loop() {
		var err error
		buf, err = Append(buf[:0], e)
		if err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkDecodeText(b *testing.B) {
	data := []byte(`{"id":"m1","type":"text/plain","content":"Hello from Lime!"}`)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Decode(data); err != nil {
			b.Fatal(err)
		}
	}
}
