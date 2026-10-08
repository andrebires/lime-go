// Package lime implements the experimental LIME 2.0 JSON profile in docs/adr/0002-json-patch-streaming.md.
// It has no global registries. Connections, assemblers and trackers own their state.
package lime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"
)

const MaxRevision uint64 = 9007199254740991
const MaxDepth = 64

type Kind uint8

const (
	Message Kind = iota + 1
	Notification
	Command
	Session
)

type Reason struct {
	Code        int    `json:"code"`
	Description string `json:"description,omitempty"`
}

const (
	InvalidInput         = 100
	UnsupportedVersion   = 101
	AuthenticationFailed = 102
	LimitExceeded        = 103
	Conflict             = 104
	UnsupportedOperation = 105
)

// Envelope is a value, not an interface hierarchy. Nil Content/Resource means
// absent; []byte("null") means JSON null. Raw values own their bytes after Decode.
// Rev == 0 is the Go representation of an omitted revision (effective revision 1).
// Type is resolved by a session Registry, not by the framing codec.
type Envelope struct {
	ID             string          `json:"id,omitempty"`
	From           string          `json:"from,omitempty"`
	To             string          `json:"to,omitempty"`
	PP             string          `json:"pp,omitempty"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
	Rev            uint64          `json:"rev,omitempty"`
	Thread         string          `json:"thread,omitempty"`
	Type           string          `json:"type,omitempty"`
	Content        json.RawMessage `json:"content,omitempty"`
	Stream         string          `json:"stream,omitempty"`
	Event          string          `json:"event,omitempty"`
	Scope          string          `json:"scope,omitempty"`
	Reason         *Reason         `json:"reason,omitempty"`
	Method         string          `json:"method,omitempty"`
	URI            string          `json:"uri,omitempty"`
	Status         string          `json:"status,omitempty"`
	Resource       json.RawMessage `json:"resource,omitempty"`
	State          string          `json:"state,omitempty"`
	Version        int             `json:"version,omitempty"`
	Scheme         string          `json:"scheme,omitempty"`
	SchemeOptions  []string        `json:"schemeOptions,omitempty"`
	Authentication json.RawMessage `json:"authentication,omitempty"`
}

func (e Envelope) Revision() uint64 {
	if e.Rev == 0 {
		return 1
	}
	return e.Rev
}
func (e Envelope) NotificationScope() string {
	if e.Scope == "" {
		return "message"
	}
	return e.Scope
}
func (e Envelope) Kind() (Kind, error) {
	k := Kind(0)
	count := 0
	if e.Content != nil || (e.Stream != "" && e.Method == "" && e.Event == "" && e.State == "") {
		k = Message
		count++
	}
	if e.Event != "" {
		k = Notification
		count++
	}
	if e.Method != "" {
		k = Command
		count++
	}
	if e.State != "" {
		k = Session
		count++
	}
	if count != 1 {
		return 0, errors.New("exactly one envelope family required")
	}
	return k, nil
}

// Validate checks the wire grammar. Application schemas and lifecycle are checked
// by Registry and Assembler. Input errors never panic.
func (e Envelope) Validate() error {
	k, err := e.Kind()
	if err != nil {
		return err
	}
	if e.Rev > MaxRevision {
		return errors.New("revision exceeds exact JSON integer range")
	}
	for _, s := range []string{e.ID, e.From, e.To, e.PP, e.Thread, e.Type, e.Stream, e.Event, e.Scope, e.Method, e.URI, e.Status, e.State, e.Scheme} {
		if !utf8.ValidString(s) {
			return errors.New("invalid UTF-8 string")
		}
	}
	if e.Metadata != nil && (len(bytes.TrimSpace(e.Metadata)) == 0 || bytes.TrimSpace(e.Metadata)[0] != '{') {
		return errors.New("metadata must be an object")
	}
	for _, option := range e.SchemeOptions {
		if !utf8.ValidString(option) {
			return errors.New("invalid scheme option")
		}
	}
	for _, raw := range []json.RawMessage{e.Metadata, e.Content, e.Resource, e.Authentication} {
		if raw != nil {
			if err := strictJSON(raw); err != nil {
				return err
			}
		}
	}
	if e.Reason != nil && !utf8.ValidString(e.Reason.Description) {
		return errors.New("invalid reason description")
	}
	mask := e.fields()
	allowed := commonFields
	switch k {
	case Message:
		allowed |= fRev | fThread | fType | fContent | fStream
		switch e.Stream {
		case "":
			if e.Type == "" || e.Content == nil {
				return errors.New("complete message requires type and content")
			}
		case "start":
			if e.ID == "" || e.Type == "" || e.Content != nil {
				return errors.New("start requires id and type, forbids content")
			}
		case "data":
			if e.ID == "" || e.Content == nil || e.Type != "" {
				return errors.New("data requires id and content, forbids type")
			}
		case "end":
			if e.ID == "" || e.Content != nil || e.Type != "" {
				return errors.New("end requires id, forbids content and type")
			}
		default:
			return errors.New("invalid stream signal")
		}
	case Notification:
		allowed |= fRev | fThread | fEvent | fScope | fReason
		if e.ID == "" {
			return errors.New("notification id required")
		}
		scope := e.NotificationScope()
		switch e.Event {
		case "received":
			if scope != "message" && scope != "session" {
				return errors.New("invalid received scope")
			}
		case "consumed":
			if scope != "message" && scope != "thread" {
				return errors.New("invalid consumed scope")
			}
		case "failed":
			if scope != "message" || e.Reason == nil {
				return errors.New("failed requires message scope and reason")
			}
		default:
			return errors.New("invalid notification event")
		}
		if scope == "thread" && e.Thread == "" {
			return errors.New("thread scope requires thread")
		}
		if e.Event != "failed" && e.Reason != nil {
			return errors.New("reason only permitted for failed notification")
		}
	case Command:
		allowed |= fType | fMethod | fURI | fStatus | fResource | fReason | fStream
		if err := validateCommand(e); err != nil {
			return err
		}

	case Session:
		allowed |= fState | fVersion | fScheme | fSchemeOptions | fAuthentication | fReason
		switch e.State {
		case "new", "authenticating", "established", "finishing", "finished", "failed":
		default:
			return errors.New("unsupported session state")
		}
		// Version selection is enforced at establishment so unsupported new requests
		// receive a failed session rather than an opaque decoding error.
		if e.Version != 0 && e.State != "new" {
			return errors.New("version only permitted on new")
		}
		if e.State == "failed" && e.Reason == nil {
			return errors.New("failed session requires reason")
		}
		if e.State != "failed" && e.Reason != nil {
			return errors.New("reason only permitted on failed session")
		}
		if e.Authentication != nil && e.Scheme == "" {
			return errors.New("authentication requires scheme")
		}
		if e.State != "authenticating" && (e.Scheme != "" || len(e.SchemeOptions) != 0 || e.Authentication != nil) {
			return errors.New("authentication fields require authenticating")
		}
	}
	if mask & ^allowed != 0 {
		return errors.New("field not permitted for envelope family")
	}
	return nil
}

func validateCommand(e Envelope) error {
	switch e.Method {
	case "get", "set", "delete", "subscribe", "unsubscribe", "observe", "merge":
	default:
		return errors.New("invalid command method")
	}
	if e.ID == "" && (e.Method != "observe" || e.Stream != "") {
		return errors.New("command id required")
	}
	if e.Status != "" && e.Status != "success" && e.Status != "failure" {
		return errors.New("invalid command status")
	}
	if e.Status == "failure" && e.Reason == nil {
		return errors.New("command failure requires reason")
	}
	if e.Status != "failure" && e.Reason != nil {
		return errors.New("reason only permitted for failure response")
	}
	switch e.Stream {
	case "":
		if (e.URI == "") == (e.Status == "") {
			return errors.New("command requires uri or status exclusively")
		}
		if (e.Resource == nil) != (e.Type == "") {
			return errors.New("resource and type required together")
		}
	case "start":
		if e.Type == "" || e.Resource != nil || e.Status != "" {
			return errors.New("command start requires type and forbids resource/status")
		}
	case "data":
		if e.Resource == nil || e.Type != "" || e.URI != "" || e.Status != "" {
			return errors.New("command data requires resource and forbids type/uri/status")
		}
	case "end":
		if e.Resource != nil || e.Type != "" || e.URI != "" {
			return errors.New("command end forbids resource/type/uri")
		}
	default:
		return errors.New("invalid command stream signal")
	}
	return nil
}

const (
	fID uint64 = 1 << iota
	fFrom
	fTo
	fPP
	fMetadata
	fRev
	fThread
	fType
	fContent
	fStream
	fEvent
	fScope
	fReason
	fMethod
	fURI
	fStatus
	fResource
	fState
	fVersion
	fScheme
	fSchemeOptions
	fAuthentication
	commonFields = fID | fFrom | fTo | fPP | fMetadata
)

func fieldBit(s string) uint64 {
	switch s {
	case "id":
		return fID
	case "from":
		return fFrom
	case "to":
		return fTo
	case "pp":
		return fPP
	case "metadata":
		return fMetadata
	case "rev":
		return fRev
	case "thread":
		return fThread
	case "type":
		return fType
	case "content":
		return fContent
	case "stream":
		return fStream
	case "event":
		return fEvent
	case "scope":
		return fScope
	case "reason":
		return fReason
	case "method":
		return fMethod
	case "uri":
		return fURI
	case "status":
		return fStatus
	case "resource":
		return fResource
	case "state":
		return fState
	case "version":
		return fVersion
	case "scheme":
		return fScheme
	case "schemeOptions":
		return fSchemeOptions
	case "authentication":
		return fAuthentication
	}
	return 0
}
func (e Envelope) fields() uint64 {
	var m uint64
	for _, v := range []struct {
		s string
		b uint64
	}{{e.ID, fID}, {e.From, fFrom}, {e.To, fTo}, {e.PP, fPP}, {e.Thread, fThread}, {e.Type, fType}, {e.Stream, fStream}, {e.Event, fEvent}, {e.Scope, fScope}, {e.Method, fMethod}, {e.URI, fURI}, {e.Status, fStatus}, {e.State, fState}, {e.Scheme, fScheme}} {
		if v.s != "" {
			m |= v.b
		}
	}
	if e.Metadata != nil {
		m |= fMetadata
	}
	if e.Rev != 0 {
		m |= fRev
	}
	if e.Content != nil {
		m |= fContent
	}
	if e.Reason != nil {
		m |= fReason
	}
	if e.Resource != nil {
		m |= fResource
	}
	if e.Version != 0 {
		m |= fVersion
	}
	if len(e.SchemeOptions) > 0 {
		m |= fSchemeOptions
	}
	if e.Authentication != nil {
		m |= fAuthentication
	}
	return m
}

// Decode owns all returned fields. The scanner detects presence and duplicate
// keys without a per-field interface map or JSON Decoder token allocations.
func Decode(data []byte) (Envelope, error) {
	var e Envelope
	if !utf8.Valid(data) || !json.Valid(data) {
		return e, errors.New("invalid JSON or UTF-8")
	}
	s := scanner{data: data}
	s.space()
	if data[s.i] != '{' {
		return e, errors.New("envelope must be an object")
	}
	present, err := s.object(0, true)
	if err != nil {
		return e, err
	}
	if err = json.Unmarshal(data, &e); err != nil {
		return Envelope{}, fmt.Errorf("decode envelope: %w", err)
	}
	// Null/empty fields must not disappear into zero values and change the grammar.
	if present & ^e.fields() != 0 {
		return Envelope{}, errors.New("null, zero or empty envelope field")
	}
	if present&fReason != 0 {
		var r map[string]json.RawMessage
		if err = json.Unmarshal(reasonJSON(data), &r); err != nil {
			return Envelope{}, err
		}
		if len(r) > 2 || r["code"] == nil || bytes.Equal(r["code"], []byte("null")) {
			return Envelope{}, errors.New("reason requires integer code and optional description")
		}
		for key := range r {
			if key != "code" && key != "description" {
				return Envelope{}, errors.New("unknown reason field")
			}
		}
	}
	if err = e.Validate(); err != nil {
		return Envelope{}, err
	}
	return e, nil
}

// reasonJSON locates the already validated top-level reason without decoding content.
func reasonJSON(data []byte) []byte {
	s := scanner{data: data}
	s.space()
	s.i++
	for {
		s.space()
		if s.data[s.i] == '}' {
			return nil
		}
		key := s.key()
		s.space()
		s.i++
		s.space()
		start := s.i
		_ = s.value(1)
		if key == "reason" {
			return data[start:s.i]
		}
		s.space()
		if s.data[s.i] == ',' {
			s.i++
		}
	}
}

// Append encodes directly into a caller-owned buffer. It avoids the historical
// document marshal -> RawMessage -> envelope marshal -> outer marshal pipeline.
// On error, dst is unchanged. Do not modify e's raw fields during this call.
func Append(dst []byte, e Envelope) ([]byte, error) {
	if err := e.Validate(); err != nil {
		return dst, err
	}
	dst = append(dst, '{')
	first := true
	field := func(name, value string) {
		if value == "" {
			return
		}
		dst = prefix(dst, name, &first)
		dst = quote(dst, value)
	}
	raw := func(name string, value []byte) {
		if value == nil {
			return
		}
		dst = prefix(dst, name, &first)
		dst = append(dst, value...)
	}
	field("id", e.ID)
	field("from", e.From)
	field("to", e.To)
	field("pp", e.PP)
	raw("metadata", e.Metadata)
	if e.Rev != 0 {
		dst = prefix(dst, "rev", &first)
		dst = strconv.AppendUint(dst, e.Rev, 10)
	}
	field("thread", e.Thread)
	field("type", e.Type)
	raw("content", e.Content)
	field("stream", e.Stream)
	field("event", e.Event)
	field("scope", e.Scope)
	if e.Reason != nil {
		dst = prefix(dst, "reason", &first)
		dst = append(dst, `{"code":`...)
		dst = strconv.AppendInt(dst, int64(e.Reason.Code), 10)
		if e.Reason.Description != "" {
			dst = append(dst, `,"description":`...)
			dst = quote(dst, e.Reason.Description)
		}
		dst = append(dst, '}')
	}
	field("method", e.Method)
	field("uri", e.URI)
	field("status", e.Status)
	raw("resource", e.Resource)
	field("state", e.State)
	if e.Version != 0 {
		dst = prefix(dst, "version", &first)
		dst = strconv.AppendInt(dst, int64(e.Version), 10)
	}
	field("scheme", e.Scheme)
	if len(e.SchemeOptions) > 0 {
		dst = prefix(dst, "schemeOptions", &first)
		dst = append(dst, '[')
		for i, v := range e.SchemeOptions {

			if i != 0 {
				dst = append(dst, ',')
			}
			dst = quote(dst, v)
		}
		dst = append(dst, ']')
	}
	raw("authentication", e.Authentication)
	dst = append(dst, '}')
	return dst, nil
}
func prefix(dst []byte, name string, first *bool) []byte {
	if !*first {
		dst = append(dst, ',')
	}
	*first = false
	dst = append(dst, '"')
	dst = append(dst, name...)
	return append(dst, '"', ':')
}

// quote emits JSON escapes, not Go string escapes (which can contain invalid \x).
func quote(dst []byte, s string) []byte {
	dst = append(dst, '"')
	const hex = "0123456789abcdef"
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"', '\\':
			dst = append(dst, '\\', c)
		case '\n':
			dst = append(dst, '\\', 'n')
		case '\r':
			dst = append(dst, '\\', 'r')
		case '\t':
			dst = append(dst, '\\', 't')
		default:
			if c < 32 {
				dst = append(dst, '\\', 'u', '0', '0', hex[c>>4], hex[c&15])
			} else {
				dst = append(dst, c)
			}
		}
	}
	return append(dst, '"')
}

// scanner only runs on json.Valid input. It adds depth and duplicate-key checks.
// The framing object uses a bit set; arbitrary content objects use local key sets.
type scanner struct {
	data []byte
	i    int
}

func (s *scanner) space() {
	for s.i < len(s.data) && (s.data[s.i] == ' ' || s.data[s.i] == '\n' || s.data[s.i] == '\r' || s.data[s.i] == '\t') {
		s.i++
	}
}
func (s *scanner) skipString() bool {
	s.i++
	escaped := false
	for s.data[s.i] != '"' {
		if s.data[s.i] == '\\' {
			escaped = true
			s.i++
		}
		s.i++
	}
	s.i++
	return escaped
}
func (s *scanner) key() string {
	start := s.i
	escaped := s.skipString()
	if !escaped {
		return string(s.data[start+1 : s.i-1])
	}
	var v string
	_ = json.Unmarshal(s.data[start:s.i], &v)
	return v
}
func (s *scanner) object(depth int, envelope bool) (uint64, error) {
	s.i++
	var bits uint64
	var keys map[string]struct{}
	if !envelope {
		keys = make(map[string]struct{})
	}
	for {
		s.space()
		if s.data[s.i] == '}' {
			s.i++
			return bits, nil
		}
		key := s.key()
		if envelope {
			bit := fieldBit(key)
			if bit == 0 {
				return 0, fmt.Errorf("unknown envelope field %q", key)
			}
			if bits&bit != 0 {
				return 0, errors.New("duplicate envelope key")
			}
			bits |= bit
		} else {
			if _, ok := keys[key]; ok {
				return 0, errors.New("duplicate JSON key")
			}
			keys[key] = struct{}{}
		}
		s.space()
		s.i++
		s.space()
		if err := s.value(depth + 1); err != nil {
			return 0, err
		}
		s.space()
		if s.data[s.i] == ',' {
			s.i++
		}
	}
}
func (s *scanner) value(depth int) error {
	if depth > MaxDepth {
		return errors.New("JSON depth limit exceeded")
	}
	switch s.data[s.i] {
	case '{':
		_, err := s.object(depth, false)
		return err
	case '[':
		s.i++
		for {
			s.space()
			if s.data[s.i] == ']' {
				s.i++
				return nil
			}
			if err := s.value(depth + 1); err != nil {
				return err
			}
			s.space()
			if s.data[s.i] == ',' {
				s.i++
			}
		}
	case '"':
		s.skipString()
	default:
		for s.i < len(s.data) {
			c := s.data[s.i]
			if c == ',' || c == '}' || c == ']' || c == ' ' || c == '\n' || c == '\r' || c == '\t' {
				break
			}
			s.i++
		}
	}
	return nil
}
func strictJSON(data []byte) error {
	if !utf8.Valid(data) || !json.Valid(data) {
		return errors.New("invalid JSON or UTF-8")
	}
	s := scanner{data: data}
	s.space()
	return s.value(0)
}
