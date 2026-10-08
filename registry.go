package lime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"strings"
)

// Limits bound retained state and input. Zero selects defaults; negative values
// are invalid. New stateful objects copy the configuration once at startup.
type Limits struct {
	FrameBytes   int
	RetryBytes   int
	ContentBytes int
	Streams      int
	Entries      int
	Aliases      int
}

func (l Limits) normalized() (Limits, error) {
	values := []*int{&l.FrameBytes, &l.RetryBytes, &l.ContentBytes, &l.Streams, &l.Entries, &l.Aliases}
	defaults := []int{64 << 10, 8 << 20, 1 << 20, 32, 256, 32}
	for i, v := range values {
		if *v < 0 {
			return l, errors.New("negative limit")
		}
		if *v == 0 {
			*v = defaults[i]
		}
	}
	return l, nil
}

// Validator receives a complete JSON value, never a partial provider fragment.
// Vendor aliases are recognized, but require explicit application validators
// for their versioned schemas before content can be accepted or streamed.
type Validator func(json.RawMessage) error

type Registry struct {
	aliases    map[string]string
	validators map[string]Validator
	limit      int
}

func NewRegistry(l Limits) (*Registry, error) {
	l, err := l.normalized()
	if err != nil {
		return nil, err
	}
	return &Registry{aliases: make(map[string]string), validators: make(map[string]Validator), limit: l.Aliases}, nil
}
func builtin(name string) string {
	switch name {
	case "text", "text/plain":
		return "text/plain"
	case "json", "application/json":
		return "application/json"
	case "chatstate", "collection", "document-select", "location", "media-link", "select", "web-link":
		return "application/vnd.lime." + name + "+json"
	case "application/vnd.lime.chatstate+json", "application/vnd.lime.collection+json", "application/vnd.lime.document-select+json", "application/vnd.lime.location+json", "application/vnd.lime.media-link+json", "application/vnd.lime.select+json", "application/vnd.lime.web-link+json":
		return name
	}
	return ""
}
func canonical(name string) bool {
	t, p, err := mime.ParseMediaType(name)
	return err == nil && t == name && len(p) == 0 && strings.Contains(t, "/") && (t == "text/plain" || strings.HasSuffix(t, "/json") || strings.HasSuffix(t, "+json"))
}

// Support registers a canonical custom JSON type and validator before session use.
// Registry is owned by one session goroutine; it is not safe for concurrent mutation.
func (r *Registry) Support(t string, v Validator) error {
	if !canonical(t) || v == nil {
		return errors.New("canonical text/JSON type and validator required")
	}
	if len(r.validators) >= r.limit {
		if _, ok := r.validators[t]; !ok {
			return errors.New("type limit exceeded")
		}
	}
	r.validators[t] = v
	return nil
}
func (r *Registry) Resolve(name string) (string, error) {
	if t := builtin(name); t != "" {
		return t, nil
	}
	if t, ok := r.aliases[name]; ok {
		return t, nil
	}
	if _, ok := r.validators[name]; ok {
		return name, nil
	}
	return "", fmt.Errorf("unsupported type %q", name)
}
func aliasName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for i, c := range name {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9' || i == 0) && c != '-' {
			return false
		}
	}
	return name[0] != '-'
}

// Register validates the whole batch before changing anything. No builtin alias
// can be redefined; identical custom registrations are idempotent.
func (r *Registry) Register(batch map[string]string) error {
	extra := 0
	for name, t := range batch {
		if !aliasName(name) || builtin(name) != "" {
			return errors.New("invalid or reserved alias")
		}
		if !canonical(t) {
			return errors.New("alias requires canonical MIME type")
		}
		if _, err := r.Resolve(t); err != nil {
			return err
		}
		if t != "text/plain" && t != "application/json" && r.validators[t] == nil {
			return errors.New("alias target requires an application schema validator")
		}
		if old, ok := r.aliases[name]; ok {
			if old != t {
				return errors.New("alias conflict")
			}
		} else {
			extra++
		}
	}
	if len(r.aliases)+extra > r.limit {
		return errors.New("alias limit exceeded")
	}
	for name, t := range batch {
		r.aliases[name] = t
	}
	return nil
}
func (r *Registry) Aliases() map[string]string {
	out := make(map[string]string, len(r.aliases))
	for k, v := range r.aliases {
		out[k] = v
	}
	return out
}
func (r *Registry) Validate(t string, value json.RawMessage) error {
	resolved, err := r.Resolve(t)
	if err != nil {
		return err
	}
	if err = strictJSON(value); err != nil {
		return err
	}
	if resolved == "text/plain" {
		if bytes.TrimSpace(value)[0] != '"' {
			return errors.New("text must be a JSON string")
		}
		return nil
	}

	if v := r.validators[resolved]; v != nil {
		return v(value)
	}
	if resolved != "application/json" {
		return errors.New("application schema validator required for vendor type")
	}
	return nil
}

const AliasURI = "/protocol/aliases"

// AliasCommand handles the profile's session-local alias resource. Only requests
// addressed to the current endpoint belong here; routing authorization is external.
func (r *Registry) AliasCommand(e Envelope) Envelope {
	resp := Envelope{ID: e.ID, Method: e.Method, To: e.From, Status: "success"}
	var err error
	switch {
	case e.URI != AliasURI:
		err = errors.New("unknown alias resource")
	case e.Method == "get":
		resp.Type = "json"
		resp.Resource, _ = json.Marshal(r.Aliases())
	case e.Method == "set" && (e.Type == "json" || e.Type == "application/json") && e.Resource != nil:
		var batch map[string]string
		if err = json.Unmarshal(e.Resource, &batch); err == nil {
			if len(batch) == 0 {
				err = errors.New("alias batch must not be empty")
			} else {
				err = r.Register(batch)
			}
		}
	default:
		err = errors.New("use get or set with type json")
	}
	if err != nil {
		resp.Status = "failure"
		resp.Type = ""
		resp.Resource = nil
		resp.Reason = &Reason{Code: UnsupportedOperation, Description: err.Error()}
	}
	return resp
}

// Streamable separates alias recognition from supported content schemas.
func (r *Registry) Streamable(t string) error {
	if t == "text/plain" || t == "application/json" || r.validators[t] != nil {
		return nil
	}
	return errors.New("application schema validator required for streaming type")
}
