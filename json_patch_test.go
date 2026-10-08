package lime

import (
	"bytes"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestJSONPatchSharedVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/json-patch-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name                    string
		Target, Patch, Expected json.RawMessage
		Error                   bool
	}
	if err = json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			original := bytes.Clone(v.Target)
			patch := bytes.Clone(v.Patch)
			out, err := JSONPatch(v.Target, v.Patch)
			if v.Error {
				if err == nil {
					t.Fatal("invalid patch accepted", string(out))
				}
			} else if err != nil || jsonDigest(out) != jsonDigest(v.Expected) {
				t.Fatal(string(out), string(v.Expected), err)
			}
			if !bytes.Equal(v.Target, original) || !bytes.Equal(v.Patch, patch) {
				t.Fatal("mutated input")
			}
			a := assembler(t, Limits{})
			apply(t, a, `{"id":"m","type":"json","stream":"start"}`)
			setup := append([]byte(`[{"op":"add","path":"","value":`), v.Target...)
			setup = append(setup, '}', ']')
			if _, err = a.Apply(Envelope{ID: "m", Stream: "data", Content: setup}); err != nil {
				t.Fatal(err)
			}
			_, err = a.Apply(Envelope{ID: "m", Stream: "data", Content: v.Patch})
			if err == nil {
				_, err = a.Apply(Envelope{ID: "m", Stream: "end"})
			}
			if v.Error && err == nil {
				t.Fatal("stream accepted invalid patch")
			}
			if !v.Error && err != nil {
				t.Fatal(err)
			}
			if v.Error && a.Active() != 0 {
				t.Fatal("rejected stream remains active")
			}
		})
	}
}
func TestJSONPatchNumbersAndOwnership(t *testing.T) {
	for _, v := range [][2]string{{`1`, `1.0`}, {`1e1000000000`, `10e999999999`}, {`-0`, `0.00`}, {`-12.30`, `-123e-1`}, {`1000`, `1e3`}, {`9007199254740993123456789`, `9007199254740993123456789`}} {
		patch := []byte(`[{"op":"test","path":"","value":` + v[1] + `}]`)
		out, err := JSONPatch([]byte(v[0]), patch)
		if err != nil || string(out) != v[0] {
			t.Fatal(v, string(out), err)
		}
	}
	if _, err := JSONPatch([]byte(`9007199254740993123456789`), []byte(`[{"op":"test","path":"","value":9007199254740993123456790}]`)); err == nil {
		t.Fatal("rounded numbers")
	}
	for _, v := range [][2]string{{`bad`, `[]`}, {`{}`, `bad`}, {`{}`, `[{"op":"add","op":"remove","path":"/a","value":1}]`}, {`{}`, `[1]`}, {`{}`, `[{"op":1,"path":""}]`}} {
		if _, err := JSONPatch([]byte(v[0]), []byte(v[1])); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
	target := []byte(`{"a":1}`)
	out, err := JSONPatch(target, []byte(`[]`))
	if err != nil {
		t.Fatal(err)
	}
	out[0] = 'x'
	if string(target) != `{"a":1}` {
		t.Fatal("aliased output")
	}
}
func TestJSONPatchLimits(t *testing.T) {
	if _, err := newPatchDocument([]byte(`{"x":1}`), 2); err == nil {
		t.Fatal("initial size")
	}
	d, _ := newPatchDocument([]byte(`{}`), 100)
	if err := d.apply([]byte(`[{"op":"add","path":"/a","value":1},{"op":"add","path":"/b","value":2}]`), 1); err == nil {
		t.Fatal("operation limit")
	}
	if err := d.apply([]byte(`[{"op":"add","path":"/a","value":"`+strings.Repeat("x", 101)+`"}]`), 256); err == nil {
		t.Fatal("contribution limit")
	}
	d, _ = newPatchDocument([]byte(`{"a":"`+strings.Repeat("x", 80)+`"}`), 200)
	if err := d.apply([]byte(`[{"op":"copy","from":"/a","path":"/b"},{"op":"copy","from":"/a","path":"/c"}]`), 256); err == nil {
		t.Fatal("assembled size")
	}
	d, _ = newPatchDocument([]byte(`{"a":"`+strings.Repeat("x", 150)+`"}`), 600)
	raw := `[` + strings.TrimSuffix(strings.Repeat(`{"op":"copy","from":"/a","path":"/b"},{"op":"remove","path":"/b"},`, 6), ",") + `]`
	if err := d.apply([]byte(raw), 256); err == nil || !strings.Contains(err.Error(), "copy work") {
		t.Fatal("copy budget", err)
	}
	d, _ = newPatchDocument([]byte(`{"a":["`+strings.Repeat("x", 100)+`"]}`), 200)
	if err := d.apply([]byte(`[{"op":"copy","from":"/a/0","path":"/a/-"}]`), 256); err == nil || !strings.Contains(err.Error(), "assembled JSON") {
		t.Fatal("array size", err)
	}

	// Build parents in separate small batches so resulting depth, not payload depth, is checked.
	d, _ = newPatchDocument([]byte(`{}`), 1<<20)
	path := ""
	for range MaxDepth {
		path += "/a"
		if err := d.apply([]byte(`[{"op":"add","path":"`+path+`","value":{}}]`), 256); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.apply([]byte(`[{"op":"add","path":"`+path+`/a","value":1}]`), 256); err == nil {
		t.Fatal("result depth")
	}
	d, _ = newPatchDocument([]byte(`{"source":{"x":1}}`), 1<<20)
	path = ""
	for range MaxDepth - 1 {
		path += "/a"
		if err := d.apply([]byte(`[{"op":"add","path":"`+path+`","value":{}}]`), 256); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.apply([]byte(`[{"op":"copy","from":"/source","path":"`+path+`/copy"}]`), 256); err == nil {
		t.Fatal("copied result depth")
	}
}
func BenchmarkJSONArrayAppend(b *testing.B) {
	for _, count := range []int{100, 1000, 10000} {
		b.Run(strconv.Itoa(count), func(b *testing.B) {
			raw := []byte(`[{"op":"add","path":"/items/-","value":{"id":1}}]`)
			b.ReportAllocs()
			for b.Loop() {
				d, _ := newPatchDocument([]byte(`{"items":[]}`), 1<<20)
				for range count {
					if err := d.apply(raw, 256); err != nil {
						b.Fatal(err)
					}
				}
				out, err := d.finish()
				if err != nil || len(out) == 0 {
					b.Fatal(err)
				}
			}
		})
	}
}

func FuzzJSONPatch(f *testing.F) {
	for _, v := range [][2]string{{`{}`, `[]`}, {`{"items":[]}`, `[{"op":"add","path":"/items/-","value":null}]`}, {`{"a":{}}`, `[{"op":"move","from":"/a","path":"/a/b"}]`}, {`1e999999999`, `[{"op":"test","path":"","value":10e999999998}]`}} {
		f.Add(v[0], v[1])
	}
	f.Fuzz(func(t *testing.T, target, patch string) {
		if len(target) > 4096 || len(patch) > 4096 {
			return
		}
		original := []byte(target)
		delta := []byte(patch)
		out, err := JSONPatch(original, delta)
		if string(original) != target || string(delta) != patch {
			t.Fatal("inputs mutated")
		}
		if err == nil {
			if !json.Valid(out) || len(out) > 1<<20 {
				t.Fatal("invalid or oversized result")
			}
		}
	})
}
