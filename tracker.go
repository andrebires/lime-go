package lime

import (
	"errors"
	"sync"
)

type retiredMarker struct {
	key         key
	fingerprint fingerprint
}

type delivery struct {
	fingerprint                                fingerprint
	bytes                                      int
	message                                    Envelope
	complete, received, read, failed, resolved bool
}

// Tracker is a bounded directional delivery ledger. It retains whole messages,
// never chunks. It is safe for concurrent send and receive loops. The peer is
// explicit: notifications from another recipient cannot release this buffer.
// Retired markers remain only within a bounded window; unknown markers fail.
type Tracker struct {
	mu       sync.Mutex
	peer     string
	limits   Limits
	order    []key
	entries  map[key]*delivery
	retired  []retiredMarker
	retained int
}

func NewTracker(peer string, l Limits) (*Tracker, error) {
	l, err := l.normalized()
	if err != nil {
		return nil, err
	}
	if peer == "" {
		return nil, errors.New("peer required")
	}
	return &Tracker{peer: peer, limits: l, entries: make(map[key]*delivery)}, nil
}

// Track reserves the original ordered entry on start or complete message. End
// must be represented by the assembled complete message, not an end-only frame.
func (t *Tracker) Track(e Envelope) error {
	if err := e.Validate(); err != nil {
		return err
	}
	kind, _ := e.Kind()
	if kind != Message || (e.Stream != "" && e.Stream != "start") {
		return errors.New("track complete message or start only")
	}
	if e.To != "" && e.To != t.peer {
		return errors.New("wrong delivery peer")
	}
	if e.ID == "" {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	id := identity(e)
	if old, ok := t.entries[id]; ok {
		if old.complete {
			if e.Stream == "start" || old.fingerprint != messageFingerprint(e) {
				return errors.New("conflicting retry")
			}
			return nil
		}
		if e.Stream == "start" {
			return errors.New("duplicate tracked start")
		}
		if old.message.Type != e.Type || old.message.Thread != e.Thread || old.message.To != e.To || old.message.PP != e.PP {
			return errors.New("completion changed routing/type")
		}
		if len(e.Content) > t.limits.ContentBytes {
			return errors.New("tracked content limit exceeded")
		}
		size := retainedSize(e)
		if t.retained-old.bytes+size > t.limits.RetryBytes {
			return errors.New("retry byte limit exceeded")
		}
		t.retained += size - old.bytes
		old.bytes = size
		old.message = clone(e)
		old.fingerprint = messageFingerprint(e)
		old.complete = true
		return nil
	}
	for _, old := range t.retired {
		if old.key == id {
			if e.Stream != "" || old.fingerprint != messageFingerprint(e) {
				return errors.New("conflicting retired retry")
			}
			return nil
		}
	}

	t.compact()
	if len(t.order) >= t.limits.Entries {
		return errors.New("pending delivery limit exceeded")
	}
	if len(e.Content) > t.limits.ContentBytes {
		return errors.New("tracked content limit exceeded")
	}
	size := retainedSize(e)
	if t.retained+size > t.limits.RetryBytes {
		return errors.New("retry byte limit exceeded")
	}
	t.retained += size
	t.entries[id] = &delivery{message: clone(e), fingerprint: messageFingerprint(e), bytes: size, complete: e.Stream == ""}
	t.order = append(t.order, id)
	return nil
}
func (t *Tracker) Pending() []Envelope {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Envelope, 0, len(t.order))
	for _, id := range t.order {
		e := t.entries[id]
		if !e.received && !e.resolved && !e.failed && e.complete {
			out = append(out, clone(e.message))
		}
	}
	return out
}
func (t *Tracker) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	count := 0
	for _, id := range t.order {
		e := t.entries[id]
		if !e.received && !e.resolved {
			count++
		}
	}
	return count
}
func (t *Tracker) Resolve(e Envelope) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, ok := t.entries[identity(e)]
	if !ok {
		return errors.New("unknown resolution marker")
	}
	if !entry.failed {
		return errors.New("only a failed entry can be explicitly resolved")
	}
	entry.resolved = true
	t.release(entry)
	return nil
}
func (t *Tracker) compact() {
	// Preserve receipt history as well as live read positions until space is needed.
	// Retiring a received entry never drops an unreceived message.
	for len(t.order) >= t.limits.Entries && len(t.order) > 0 {
		id := t.order[0]
		entry := t.entries[id]
		if !entry.received && !entry.resolved {
			return
		}
		delete(t.entries, id)
		t.order = t.order[1:]
		t.retired = append(t.retired, retiredMarker{id, entry.fingerprint})
		if len(t.retired) > t.limits.Entries {
			copy(t.retired, t.retired[1:])
			t.retired = t.retired[:len(t.retired)-1]
		}
	}
}

// Apply validates exact revision, scope, peer and contiguous completion before
// changing any state. Failed/consumed never release a received delivery.
func (t *Tracker) Apply(n Envelope) error {
	if err := n.Validate(); err != nil {
		return err
	}
	kind, _ := n.Kind()
	if kind != Notification {
		return errors.New("notification required")
	}
	if n.From != t.peer || n.PP != "" {
		return errors.New("receipt from wrong peer")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	id := key{ID: n.ID, Rev: n.Revision()}
	// From on tracked messages identifies our sender, not the receiving peer.
	index := -1
	for i, k := range t.order {
		if k.ID == id.ID && k.Rev == id.Rev {
			if index != -1 {
				return errors.New("ambiguous marker")
			}
			index = i
			id = k
		}
	}
	if index < 0 {
		for _, k := range t.retired {
			if k.key.ID == n.ID && k.key.Rev == n.Revision() && n.Event == "received" {
				if (n.To != "" && n.To != k.key.From) || (n.Thread != "" && n.Thread != k.fingerprint.Thread) {
					return errors.New("retired receipt routing mismatch")
				}
				return nil
			}
		}
		return errors.New("unknown receipt marker")
	}
	entry := t.entries[id]
	if n.To != "" && n.To != entry.message.From {
		return errors.New("receipt addressed to wrong sender")
	}
	if n.Thread != "" && n.Thread != entry.message.Thread {
		return errors.New("receipt thread mismatch")
	}
	if n.Event == "failed" {
		if entry.received {
			return errors.New("failure after received")
		}
		entry.failed = true
		return nil
	}
	scope := n.NotificationScope()
	for i, k := range t.order {
		if i > index {
			break
		}
		e := t.entries[k]
		covered := scope == "session" || (scope == "thread" && e.message.Thread == n.Thread) || i == index
		if !covered {
			continue
		}
		if !e.resolved && (!e.complete || e.failed) {
			return errors.New("receipt crosses incomplete or failed delivery")
		}
	}
	for i, k := range t.order {
		if i > index {
			break
		}
		e := t.entries[k]
		if scope == "session" || (scope == "thread" && e.message.Thread == n.Thread) || i == index {
			if n.Event == "received" {
				e.received = true
				t.release(e)
			} else {
				e.read = true
			}
		}
	}
	return nil
}

// MarkRead records UI visibility locally. ReadNotification refuses cumulative
// progress across an unread or incomplete earlier message in the same thread.
func (t *Tracker) MarkRead(e Envelope) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry := t.entries[identity(e)]
	if entry == nil || !entry.complete || entry.failed {
		return errors.New("unknown or incomplete read marker")
	}
	entry.read = true
	return nil
}
func (t *Tracker) ReadNotification(e Envelope, scope string) (Envelope, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry := t.entries[identity(e)]
	if entry == nil {
		return Envelope{}, errors.New("unknown read marker")
	}
	n := Envelope{ID: e.ID, Rev: e.Rev, From: t.peer, To: entry.message.From, Event: "consumed", Scope: scope, Thread: entry.message.Thread}
	if err := n.Validate(); err != nil {
		return Envelope{}, err
	}
	for _, id := range t.order {
		d := t.entries[id]
		if (scope == "thread" && d.message.Thread == entry.message.Thread) || id == identity(e) {
			if !d.resolved && (!d.read || !d.complete || d.failed) {
				return Envelope{}, errors.New("read watermark has a gap")
			}
		}
		if id == identity(e) {
			break
		}
	}
	return n, nil
}

// Reset explicitly abandons session-local state without reporting receipt.
func (t *Tracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	clear(t.entries)
	t.order = nil
	t.retired = nil
	t.retained = 0
}

func retainedSize(e Envelope) int { return len(e.Content) + len(e.Metadata) }
func (t *Tracker) release(d *delivery) {
	t.retained -= d.bytes
	d.bytes = 0
	d.message.Content = nil
	d.message.Metadata = nil
}

// RetainedBytes reports whole-message payload bytes still awaiting receipt.
func (t *Tracker) RetainedBytes() int { t.mu.Lock(); defer t.mu.Unlock(); return t.retained }
