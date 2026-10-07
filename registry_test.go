package lime

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRegistry(t *testing.T) {
	r := registry(t, Limits{Aliases: 2})
	names := []string{"text", "json", "chatstate", "collection", "document-select", "location", "media-link", "select", "web-link"}
	for _, name := range names {
		canonical, err := r.Resolve(name)
		if err != nil || !strings.Contains(canonical, "/") {
			t.Fatal(name, err)
		}
		if back, err := r.Resolve(canonical); err != nil || back != canonical {
			t.Fatal("canonical")
		}
	}
	if err := r.Register(map[string]string{"note": "text/plain"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(map[string]string{"note": "text/plain"}); err != nil {
		t.Fatal("idempotent", err)
	}
	if err := r.Register(map[string]string{"note": "application/json"}); err == nil {
		t.Fatal("conflict")
	}
	for _, batch := range []map[string]string{{"text": "text/plain"}, {"": "text/plain"}, {"Bad": "text/plain"}, {"-bad": "text/plain"}, {"1bad": "text/plain"}, {strings.Repeat("a", 65): "text/plain"}, {"bad": "application/json; charset=utf-8"}, {"bad": "Text/Plain"}, {"bad": "application/x-unknown+json"}} {
		if err := r.Register(batch); err == nil {
			t.Fatal("invalid alias", batch)
		}
	}
	if err := r.Register(map[string]string{"new": "text/plain", "other": "text/plain"}); err == nil {
		t.Fatal("capacity")
	}
	if _, err := r.Resolve("new"); err == nil {
		t.Fatal("partial registration")
	}
	if err := r.Support("application/x-check+json", func(b json.RawMessage) error {
		if string(b) != `{}` {
			return errors.New("schema")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(map[string]string{"check": "application/x-check+json"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Validate("check", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := r.Validate("check", []byte(`[]`)); err == nil {
		t.Fatal("schema bypass")
	}
	for _, v := range []struct{ typ, data string }{{"unknown", "{}"}, {"json", "bad"}, {"text", "null"}, {"text", "{}"}} {
		if err := r.Validate(v.typ, []byte(v.data)); err == nil {
			t.Fatal("invalid content", v)
		}
	}
	if err := r.Validate("text", []byte(`"{\"x\":1}"`)); err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct {
		typ      string
		validate Validator
	}{{"text", func(json.RawMessage) error { return nil }}, {"application/json", nil}} {
		if err := r.Support(v.typ, v.validate); err == nil {
			t.Fatal("invalid support")
		}
	}
	if err := r.Support("application/x-second+json", func(json.RawMessage) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := r.Support("application/x-third+json", func(json.RawMessage) error { return nil }); err == nil {
		t.Fatal("type capacity")
	}
	if err := r.Support("application/x-check+json", func(json.RawMessage) error { return nil }); err != nil {
		t.Fatal("support replacement")
	}
	aliases := r.Aliases()
	aliases["note"] = "bad"
	if typ, _ := r.Resolve("note"); typ != "text/plain" {
		t.Fatal("registry leaked map")
	}
	for _, l := range []Limits{{FrameBytes: -1}, {ContentBytes: -1}, {Streams: -1}, {Entries: -1}, {Aliases: -1}} {
		if _, err := NewRegistry(l); err == nil {
			t.Fatal("negative limits")
		}
	}
}
func TestAliasCommands(t *testing.T) {
	r := registry(t, Limits{})
	for _, s := range []string{`{"id":"c","method":"set","uri":"/protocol/aliases","type":"json","resource":{"note":"text/plain"}}`, `{"id":"g","method":"get","uri":"/protocol/aliases"}`} {
		e := mustDecode(t, s)
		resp := r.AliasCommand(e)
		if resp.Status != "success" {
			t.Fatal(resp)
		}
		if _, err := Append(nil, resp); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []string{`{"id":"c","method":"get","uri":"/other"}`, `{"id":"c","method":"delete","uri":"/protocol/aliases"}`, `{"id":"c","method":"set","uri":"/protocol/aliases","type":"json","resource":[]}`, `{"id":"c","method":"set","uri":"/protocol/aliases","type":"json","resource":{}}`, `{"id":"c","method":"set","uri":"/protocol/aliases","type":"json","resource":{"text":"text/plain"}}`} {
		resp := r.AliasCommand(mustDecode(t, s))
		if resp.Status != "failure" || resp.Reason == nil {
			t.Fatal("failure missing", resp)
		}
	}
	fresh := registry(t, Limits{})
	if _, err := fresh.Resolve("note"); err == nil {
		t.Fatal("aliases inherited across session")
	}
}

func TestVendorTypesRequireSchemas(t *testing.T) {
	r := registry(t, Limits{})
	if err := r.Validate("select", []byte(`"serialized object"`)); err == nil {
		t.Fatal("alias installed schema support")
	}
	a, _ := NewAssembler(r, Limits{})
	if _, err := a.Apply(mustDecode(t, `{"id":"s","type":"select","stream":"start"}`)); err == nil {
		t.Fatal("unsupported stream was accepted")
	}
	if err := r.Support("application/vnd.lime.select+json", func(raw json.RawMessage) error {
		var value struct {
			Options []string `json:"options"`
		}
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		if len(value.Options) == 0 {
			return errors.New("options required")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	apply(t, a, `{"id":"s","type":"select","stream":"start"}`)
	apply(t, a, `{"id":"s","stream":"data","content":{"options":["A"]}}`)
	if result := apply(t, a, `{"id":"s","stream":"end"}`); !result.Complete {
		t.Fatal("schema-supported stream failed")
	}
}

func TestAliasRegistrationRequiresUsableVendorSchema(t *testing.T) {
	r := registry(t, Limits{})
	const typ = "application/vnd.lime.select+json"
	response := r.AliasCommand(mustDecode(t, `{"id":"alias","method":"set","uri":"/protocol/aliases","type":"json","resource":{"note":"application/vnd.lime.select+json","plain":"text/plain"}}`))
	if response.Status != "failure" || response.Reason == nil || len(r.Aliases()) != 0 {
		t.Fatal("unusable or partial alias batch accepted", response, r.Aliases())
	}
	if err := r.Support(typ, func(raw json.RawMessage) error {
		if string(raw) != `{"options":["A"]}` {
			return errors.New("invalid select schema")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	response = r.AliasCommand(mustDecode(t, `{"id":"alias","method":"set","uri":"/protocol/aliases","type":"json","resource":{"note":"application/vnd.lime.select+json"}}`))
	if response.Status != "success" {
		t.Fatal(response)
	}
	if err := r.Validate("note", []byte(`{"options":["A"]}`)); err != nil {
		t.Fatal(err)
	}
	if err := r.Validate("note", []byte(`{}`)); err == nil {
		t.Fatal("aliased schema bypassed")
	}
}
