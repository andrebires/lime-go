package lime

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
)

type key struct {
	From, ID string
	Rev      uint64
}

func identity(e Envelope) key { return key{e.From, e.ID, e.Revision()} }

type assembly struct {
	start  Envelope
	text   []byte
	value  json.RawMessage
	chunks int
}

// Result separates contributions from validated complete messages. For data,
// Message contains the contribution with resolved type/routing. It never copies
// the whole accumulated text on every chunk; complete content is returned at end.
type Result struct {
	Message   Envelope
	Complete  bool
	Duplicate bool
}

// Assembler owns one receive direction. Apply is serial; callers may keep it in
// their receive loop. Commit executes before a revision becomes receipt eligible.
type Assembler struct {
	registry  *Registry
	limits    Limits
	active    map[key]*assembly
	completed map[key]fingerprint
	order     []key
	Commit    func(Envelope) error
}

func NewAssembler(r *Registry, l Limits) (*Assembler, error) {
	l, err := l.normalized()
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.New("registry required")
	}
	return &Assembler{registry: r, limits: l, active: make(map[key]*assembly), completed: make(map[key]fingerprint)}, nil
}
func (a *Assembler) Active() int        { return len(a.active) }
func (a *Assembler) Reset()             { clear(a.active); clear(a.completed); a.order = nil }
func (a *Assembler) Discard(e Envelope) { delete(a.active, identity(e)) }
func (a *Assembler) Apply(e Envelope) (Result, error) {
	if err := e.Validate(); err != nil {
		return Result{}, err
	}
	k, err := e.Kind()
	if err != nil || k != Message {
		return Result{}, errors.New("assembler requires message")
	}
	id := identity(e)
	fail := func(err error) (Result, error) { delete(a.active, id); return Result{}, err }
	switch e.Stream {
	case "":
		if _, ok := a.active[id]; ok {
			return fail(errors.New("complete message conflicts with active stream"))
		}
		if len(e.Content) > a.limits.ContentBytes {
			return fail(errors.New("content limit exceeded"))
		}
		t, err := a.registry.Resolve(e.Type)
		if err != nil {
			return fail(err)
		}
		e.Type = t
		return a.finish(e)
	case "start":
		if _, ok := a.completed[id]; ok {
			return Result{}, errors.New("retry must send whole complete message, not chunks")
		}
		if _, ok := a.active[id]; ok {
			return fail(errors.New("duplicate stream start"))
		}
		if len(a.active) >= a.limits.Streams {
			return Result{}, errors.New("active stream limit exceeded")
		}
		t, err := a.registry.Resolve(e.Type)
		if err != nil {
			return Result{}, err
		}
		e.Type = t
		if err := a.registry.Streamable(t); err != nil {
			return Result{}, err
		}
		a.active[id] = &assembly{start: clone(e), value: json.RawMessage(`{}`)}
		return Result{Message: clone(e)}, nil
	case "data", "end":
		current, ok := a.active[id]
		if !ok {
			return Result{}, errors.New("stream has no start for sender/id/revision")
		}
		if (e.Thread != "" && e.Thread != current.start.Thread) || (e.To != "" && e.To != current.start.To) || e.PP != current.start.PP {
			return fail(errors.New("stream routing changed"))
		}
		if e.Stream == "end" {
			complete := current.start
			complete.Stream = ""
			if complete.Type == "text/plain" {
				complete.Content = quote(nil, string(current.text))
			} else {
				complete.Content = current.value
			}
			delete(a.active, id)
			return a.finish(complete)
		}
		if current.chunks >= a.limits.Entries {
			return fail(errors.New("stream contribution limit exceeded"))
		}
		current.chunks++
		if current.start.Type == "text/plain" {
			// Validation already proved this is one JSON value. Most typing chunks have
			// no escapes, so append their UTF-8 bytes directly without a temporary string.
			raw := bytes.TrimSpace(e.Content)
			if raw[0] != '"' {
				return fail(errors.New("text contribution must be string"))
			}
			contribution := raw[1 : len(raw)-1]
			if bytes.IndexByte(contribution, '\\') >= 0 {
				var text string
				if err := json.Unmarshal(raw, &text); err != nil {
					return fail(err)
				}
				contribution = []byte(text)
			}
			if len(current.text)+len(contribution) > a.limits.ContentBytes {
				return fail(errors.New("assembled text limit exceeded"))
			}
			current.text = append(current.text, contribution...)
			e.Type = current.start.Type
			e.To = current.start.To
			e.Thread = current.start.Thread
			return Result{Message: e}, nil
		}

		value, err := MergePatch(current.value, e.Content)
		if err != nil {
			return fail(err)
		}
		if len(value) > a.limits.ContentBytes {
			return fail(errors.New("assembled JSON limit exceeded"))
		}
		current.value = value
		e.Type = current.start.Type
		e.To = current.start.To
		e.Thread = current.start.Thread
		return Result{Message: e}, nil
	}
	return Result{}, errors.New("invalid stream")
}
func (a *Assembler) finish(e Envelope) (Result, error) {
	if len(e.Content) > a.limits.ContentBytes {
		return Result{}, errors.New("complete content limit exceeded")
	}
	if err := a.registry.Validate(e.Type, e.Content); err != nil {
		return Result{}, err
	}
	if e.ID != "" {
		if old, ok := a.completed[identity(e)]; ok {
			if old != messageFingerprint(e) {
				return Result{}, errors.New("same revision has different content or routing")
			}
			return Result{Message: e, Complete: true, Duplicate: true}, nil
		}
	}
	if a.Commit != nil {
		if err := a.Commit(clone(e)); err != nil {
			return Result{}, fmt.Errorf("commit complete message: %w", err)
		}
	}
	if e.ID != "" {
		if len(a.order) == a.limits.Entries {
			delete(a.completed, a.order[0])
			copy(a.order, a.order[1:])
			a.order = a.order[:len(a.order)-1]
		}
		id := identity(e)
		a.completed[id] = messageFingerprint(e)
		a.order = append(a.order, id)
	}
	return Result{Message: e, Complete: true}, nil
}

// Completed deduplication retains digests and routing, not whole payloads.
type fingerprint struct {
	To, PP, Thread, Type string
	Content, Metadata    [32]byte
}

func messageFingerprint(e Envelope) fingerprint {
	return fingerprint{e.To, e.PP, e.Thread, e.Type, jsonDigest(e.Content), jsonDigest(e.Metadata)}
}

// Digests compare JSON values across property order, whitespace and string
// escaping. UseNumber preserves exact numeric literals; no float round-trip.
func jsonDigest(raw []byte) [32]byte {
	if raw == nil {
		return sha256.Sum256(nil)
	}
	raw = bytes.TrimSpace(raw)
	if raw[0] == '"' {
		if bytes.IndexByte(raw, '\\') < 0 {
			return sha256.Sum256(raw)
		}
		var text string
		_ = json.Unmarshal(raw, &text)
		return sha256.Sum256(quote(nil, text))
	}
	if raw[0] != '{' && raw[0] != '[' {
		return sha256.Sum256(raw)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	_ = dec.Decode(&value)
	canonical, _ := json.Marshal(value)
	return sha256.Sum256(canonical)
}
func clone(e Envelope) Envelope {
	e.Content = bytes.Clone(e.Content)
	e.Resource = bytes.Clone(e.Resource)
	e.Metadata = bytes.Clone(e.Metadata)
	e.Authentication = bytes.Clone(e.Authentication)
	e.SchemeOptions = append([]string(nil), e.SchemeOptions...)
	if e.Reason != nil {
		r := *e.Reason
		e.Reason = &r
	}
	return e
}

// MergePatch implements RFC 7396 and preserves exact numeric literals. Arrays and
// scalars replace; null object members delete. Inputs and output do not alias.
func MergePatch(target, patch []byte) (json.RawMessage, error) {
	if err := strictJSON(target); err != nil {
		return nil, err
	}
	if err := strictJSON(patch); err != nil {
		return nil, err
	}
	out, err := merge(target, patch, 0)
	if err != nil {
		return nil, err
	}
	if err = strictJSON(out); err != nil {
		return nil, err
	}
	return out, nil
}
func merge(target, patch []byte, depth int) (json.RawMessage, error) {
	if depth > MaxDepth {
		return nil, errors.New("merge depth exceeded")
	}
	patch = bytes.TrimSpace(patch)
	if patch[0] != '{' {
		return bytes.Clone(patch), nil
	}
	var p map[string]json.RawMessage
	_ = json.Unmarshal(patch, &p)
	t := make(map[string]json.RawMessage)
	target = bytes.TrimSpace(target)
	if target[0] == '{' {
		_ = json.Unmarshal(target, &t)
	}
	for name, value := range p {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			delete(t, name)
			continue
		}
		old := t[name]
		if old == nil {
			old = json.RawMessage(`null`)
		}
		merged, err := merge(old, value, depth+1)
		if err != nil {
			return nil, err
		}
		t[name] = merged
	}
	return json.Marshal(t)
}
