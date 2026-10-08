package lime

import (
	"bytes"
	"encoding/json"
	"errors"
)

// CommandDirection describes traffic relative to one endpoint/session.
type CommandDirection uint8

const (
	IncomingCommand CommandDirection = iota + 1
	OutgoingCommand
)

type commandKey struct{ peer, id string }
type commandExchange struct {
	local             bool
	method            string
	submitted         bool
	request, response *assembly
}

// CommandResult contains provisional contributions or a complete invocation/result.
// Only Complete && !Response may be dispatched as an application request.
// Data borrows the contribution bytes; complete results own their resource.
// A failed response carries reason/status, never a provisional resource.
type CommandResult struct {
	Command            Envelope
	Complete, Response bool
}

// CommandAssembler owns both directions for one endpoint/session. Apply is serial.
// Apply outgoing requests before sending so response starts can be correlated.
// Apply outgoing responses to release the remote request's reserved ID at end.
// Call Discard on timeout, rejection or send failure; Reset on disconnect.
// Commands are never added to message receipt, deduplication or retry trackers.
type CommandAssembler struct {
	registry  *Registry
	limits    Limits
	exchanges map[commandKey]*commandExchange
}

func NewCommandAssembler(r *Registry, l Limits) (*CommandAssembler, error) {
	l, err := l.normalized()
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.New("registry required")
	}
	return &CommandAssembler{registry: r, limits: l, exchanges: make(map[commandKey]*commandExchange)}, nil
}
func commandIdentity(e Envelope, d CommandDirection) commandKey {
	peer := e.From
	if d == OutgoingCommand {
		peer = e.To
	}
	return commandKey{peer, e.ID}
}
func (a *CommandAssembler) Active() int { return len(a.exchanges) }
func (a *CommandAssembler) Reset()      { clear(a.exchanges) }
func (a *CommandAssembler) Discard(e Envelope, d CommandDirection) {
	delete(a.exchanges, commandIdentity(e, d))
}
func (a *CommandAssembler) Apply(e Envelope, d CommandDirection) (CommandResult, error) {
	if d != IncomingCommand && d != OutgoingCommand {
		return CommandResult{}, errors.New("invalid command direction")
	}
	id := commandIdentity(e, d)
	result, err := a.apply(e, d, id)
	if err != nil {
		delete(a.exchanges, id)
	}
	return result, err
}
func (a *CommandAssembler) apply(e Envelope, d CommandDirection, id commandKey) (CommandResult, error) {
	if err := e.Validate(); err != nil {
		return CommandResult{}, err
	}
	k, _ := e.Kind()
	if k != Command {
		return CommandResult{}, errors.New("command assembler requires command")
	}
	x := a.exchanges[id]
	initial := e.Stream == "" || e.Stream == "start"
	request := initial && e.URI != ""
	if request {
		if x != nil {
			return CommandResult{}, errors.New("command id already active in peer context")
		}
		if e.ID == "" {
			return a.complete(e, false)
		}
		if len(a.exchanges) >= a.limits.Entries {
			return CommandResult{}, errors.New("command exchange capacity exceeded")
		}
		x = &commandExchange{local: d == OutgoingCommand, method: e.Method}
		a.exchanges[id] = x
	}
	if x == nil {
		if e.Stream == "" && e.Status != "" {
			return a.complete(e, true)
		}
		return CommandResult{}, errors.New("command stream has no matching request")
	}
	if x.method != e.Method {
		return CommandResult{}, errors.New("command method mismatch")
	}
	response := x.local != (d == OutgoingCommand)
	if initial && !request && !response {
		return CommandResult{}, errors.New("command direction conflicts with active request")
	}
	if response && !x.submitted && e.Status != "failure" {
		return CommandResult{}, errors.New("command request has not ended")
	}
	part := &x.request
	if response {
		part = &x.response
	}
	if e.Stream == "" {
		if *part != nil {
			return CommandResult{}, errors.New("complete command conflicts with active stream")
		}
		result, err := a.complete(e, response)
		if err != nil {
			return CommandResult{}, err
		}
		if response {
			delete(a.exchanges, id)
		} else {
			x.submitted = true
		}
		return result, nil
	}
	if e.Stream == "start" {
		if *part != nil {
			return CommandResult{}, errors.New("command stream already started")
		}
		streams := 0
		for _, other := range a.exchanges {
			if other.request != nil {
				streams++
			}
			if other.response != nil {
				streams++
			}
		}
		if streams >= a.limits.Streams {
			return CommandResult{}, errors.New("command stream capacity exceeded")
		}
		t, err := a.registry.Resolve(e.Type)
		if err != nil {
			return CommandResult{}, err
		}
		if err = a.registry.Streamable(t); err != nil {
			return CommandResult{}, err
		}
		e.Type = t
		current := &assembly{start: clone(e)}
		if t != "text/plain" {
			current.value, err = newPatchDocument([]byte(`{}`), a.limits.ContentBytes)
			if err != nil {
				return CommandResult{}, err
			}
		}
		*part = current
		return CommandResult{Command: clone(e), Response: response}, nil
	}
	current := *part
	if current == nil {
		return CommandResult{}, errors.New("command stream has not started")
	}
	if (e.To != "" && e.To != current.start.To) || (e.From != "" && e.From != current.start.From) || e.PP != current.start.PP {
		return CommandResult{}, errors.New("command stream routing changed")
	}
	if e.Stream == "data" {
		if err := a.contribute(current, e.Resource); err != nil {
			return CommandResult{}, err
		}
		return CommandResult{Command: e, Response: response}, nil
	}
	if response != (e.Status != "") {
		return CommandResult{}, errors.New("only response end requires status")
	}
	complete := current.start
	complete.Stream = ""
	if response && e.Status == "failure" {
		complete.Type = ""
		complete.Status = e.Status
		complete.Reason = e.Reason
		delete(a.exchanges, id)
		return CommandResult{Command: clone(complete), Complete: true, Response: true}, nil
	}
	if current.start.Type == "text/plain" {
		complete.Resource = quote(nil, string(current.text))
	} else {
		raw, err := current.value.finish()
		if err != nil {
			return CommandResult{}, err
		}
		complete.Resource = raw
	}
	complete.Status = e.Status
	result, err := a.complete(complete, response)
	if err != nil {
		return CommandResult{}, err
	}
	if response {
		delete(a.exchanges, id)
	} else {
		x.submitted = true
		x.request = nil
	}
	return result, nil
}
func (a *CommandAssembler) complete(e Envelope, response bool) (CommandResult, error) {
	if len(e.Resource) > a.limits.ContentBytes {
		return CommandResult{}, errors.New("command resource limit exceeded")
	}
	if e.Resource != nil {
		if err := a.registry.Validate(e.Type, e.Resource); err != nil {
			return CommandResult{}, err
		}
	}
	return CommandResult{Command: clone(e), Complete: true, Response: response}, nil
}
func (a *CommandAssembler) contribute(current *assembly, raw json.RawMessage) error {
	if current.chunks >= a.limits.Entries {
		return errors.New("command contribution limit exceeded")
	}
	current.chunks++
	if len(raw) > a.limits.ContentBytes {
		return errors.New("command contribution byte limit exceeded")
	}
	if current.value != nil {
		return current.value.apply(raw, a.limits.Entries)
	}
	raw = bytes.TrimSpace(raw)
	if raw[0] != '"' {
		return errors.New("text resource contribution must be string")
	}
	text := raw[1 : len(raw)-1]
	if bytes.IndexByte(text, '\\') >= 0 {
		var decoded string
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return err
		}
		text = []byte(decoded)
	}
	if len(current.text)+len(text) > a.limits.ContentBytes {
		return errors.New("assembled command text limit exceeded")
	}
	current.text = append(current.text, text...)
	return nil
}
