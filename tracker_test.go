package lime

import (
	"sync"
	"testing"
)

func tracker(t *testing.T, l Limits) *Tracker {
	t.Helper()
	tr, err := NewTracker("bob", l)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}
func deliveryMessage(id, thread string) Envelope {
	return Envelope{ID: id, From: "alice", To: "bob", Thread: thread, Type: "text", Content: []byte(`"hello"`)}
}
func receipt(id, event, scope string) Envelope {
	return Envelope{ID: id, From: "bob", To: "alice", Event: event, Scope: scope}
}
func TestDeliveryProgressAndRetry(t *testing.T) {
	tr := tracker(t, Limits{})
	a := deliveryMessage("a", "t")
	b := deliveryMessage("b", "other")
	start := clone(a)
	start.Content = nil
	start.Stream = "start"
	if err := tr.Track(start); err != nil {
		t.Fatal(err)
	}
	if err := tr.Track(b); err != nil {
		t.Fatal(err)
	}
	if len(tr.Pending()) != 1 || tr.Len() != 2 {
		t.Fatal("incomplete was retryable")
	}
	if err := tr.Apply(receipt("b", "received", "session")); err == nil {
		t.Fatal("crossed incomplete a")
	}
	if err := tr.Apply(receipt("b", "received", "")); err != nil {
		t.Fatal(err)
	}
	if tr.Len() != 1 {
		t.Fatal("individual receipt cleared prefix")
	}
	if err := tr.Track(a); err != nil {
		t.Fatal(err)
	}
	if err := tr.Track(a); err != nil {
		t.Fatal("same retry moved position", err)
	}
	if len(tr.Pending()) != 1 {
		t.Fatal("pending")
	}
	pending := tr.Pending()
	clear(pending[0].Content)
	if string(tr.Pending()[0].Content) != `"hello"` {
		t.Fatal("pending alias")
	}
	if err := tr.Apply(receipt("b", "received", "session")); err != nil {
		t.Fatal(err)
	}
	if tr.Len() != 0 {
		t.Fatal("cumulative did not clear")
	}
	for _, id := range []string{"a", "b"} {
		if err := tr.Apply(receipt(id, "received", "session")); err != nil {
			t.Fatal("older receipt", err)
		}
	}
	tr.Reset()
	if tr.Len() != 0 || len(tr.Pending()) != 0 {
		t.Fatal("reset")
	}
	if err := tr.Track(Envelope{Type: "text", Content: []byte(`"x"`)}); err != nil || tr.Len() != 0 {
		t.Fatal("IDless entered buffer")
	}
}
func TestReceiptFailureResolutionAndReadGaps(t *testing.T) {
	tr := tracker(t, Limits{})
	a, b, c := deliveryMessage("a", "t"), deliveryMessage("b", "other"), deliveryMessage("c", "t")
	for _, e := range []Envelope{a, b, c} {
		if err := tr.Track(e); err != nil {
			t.Fatal(err)
		}
	}
	n := receipt("a", "failed", "")
	n.Reason = &Reason{Code: 1}
	if err := tr.Apply(n); err != nil {
		t.Fatal(err)
	}
	if tr.Len() != 3 || len(tr.Pending()) != 2 {
		t.Fatal("failed implicitly received")
	}
	if err := tr.Apply(receipt("c", "received", "session")); err == nil {
		t.Fatal("crossed failed")
	}
	if err := tr.Resolve(a); err != nil {
		t.Fatal(err)
	}
	if err := tr.Apply(receipt("c", "received", "session")); err != nil || tr.Len() != 0 {
		t.Fatal("resolve", err)
	}
	tr.Reset()
	for _, e := range []Envelope{a, b, c} {
		_ = tr.Track(e)
	}
	if err := tr.MarkRead(c); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.ReadNotification(c, "thread"); err == nil {
		t.Fatal("read gap")
	}
	if _, err := tr.ReadNotification(c, "message"); err != nil {
		t.Fatal("individual read", err)
	}
	if err := tr.MarkRead(a); err != nil {
		t.Fatal(err)
	}
	n, err := tr.ReadNotification(c, "thread")
	if err != nil {
		t.Fatal(err)
	}
	if n.Thread != "t" || n.To != "alice" {
		t.Fatal(n)
	}
	if err = tr.Apply(n); err != nil || tr.Len() != 3 {
		t.Fatal("consumed cleared retry", err)
	}
	if _, err := tr.ReadNotification(b, "thread"); err == nil {
		t.Fatal("unread")
	}
	if err := tr.Apply(receipt("a", "received", "message")); err != nil {
		t.Fatal(err)
	}
	n = receipt("a", "failed", "")
	n.Reason = &Reason{Code: 1}
	if err := tr.Apply(n); err == nil {
		t.Fatal("failure after received")
	}
}
func TestTrackerLimitsAndValidation(t *testing.T) {
	tr := tracker(t, Limits{Entries: 1})
	a := deliveryMessage("a", "t")
	if err := tr.Track(a); err != nil {
		t.Fatal(err)
	}
	if err := tr.Track(deliveryMessage("b", "t")); err == nil {
		t.Fatal("buffer evicted pending")
	}
	if len(tr.Pending()) != 1 {
		t.Fatal("lost a")
	}
	if err := tr.Apply(receipt("a", "received", "")); err != nil {
		t.Fatal(err)
	}
	if err := tr.Track(deliveryMessage("b", "t")); err != nil {
		t.Fatal(err)
	}
	if err := tr.Apply(receipt("a", "received", "session")); err != nil {
		t.Fatal("retired duplicate", err)
	}
	if err := tr.Apply(receipt("b", "received", "")); err != nil {
		t.Fatal(err)
	}
	_ = tr.Track(deliveryMessage("c", "t"))
	_ = tr.Apply(receipt("c", "received", ""))
	if err := tr.Apply(receipt("a", "received", "")); err == nil {
		t.Fatal("unknown expired marker")
	}
	tr = tracker(t, Limits{})
	_ = tr.Track(a)
	bad := []Envelope{Envelope{}, {State: "new"}, receipt("unknown", "received", ""), receipt("a", "consumed", "session")}
	for _, field := range []string{"from", "to", "pp", "thread"} {
		n := receipt("a", "received", "")
		switch field {
		case "from":
			n.From = "mallory"
		case "to":
			n.To = "mallory"
		case "pp":
			n.PP = "mallory"
		case "thread":
			n.Thread = "other"
		}
		bad = append(bad, n)
	}
	for _, n := range bad {
		if err := tr.Apply(n); err == nil {
			t.Fatal("invalid receipt", n)
		}
	}
	if tr.Len() != 1 {
		t.Fatal("invalid receipt changed state")
	}
	if err := tr.Resolve(a); err == nil {
		t.Fatal("resolved nonfailed")
	}
	if err := tr.Resolve(deliveryMessage("unknown", "t")); err == nil {
		t.Fatal("unknown resolve")
	}
	if err := tr.MarkRead(deliveryMessage("unknown", "t")); err == nil {
		t.Fatal("unknown read")
	}
	if _, err := tr.ReadNotification(deliveryMessage("unknown", "t"), "thread"); err == nil {
		t.Fatal("unknown read notification")
	}
	if _, err := tr.ReadNotification(a, "session"); err == nil {
		t.Fatal("invalid scope")
	}
	for _, e := range []Envelope{Envelope{}, {State: "new"}, {ID: "x", Stream: "data", Content: []byte(`"x"`)}, {ID: "x", Type: "text", Content: []byte(`"x"`), To: "other"}} {
		if err := tr.Track(e); err == nil {
			t.Fatal("invalid track", e)
		}
	}
	changed := clone(a)
	changed.Content = []byte(`"different"`)
	if err := tr.Track(changed); err == nil {
		t.Fatal("conflicting retry")
	}
	start := clone(a)
	start.Content = nil
	start.Stream = "start"
	if err := tr.Track(start); err == nil {
		t.Fatal("stream retry")
	}
	tr.Reset()
	_ = tr.Track(start)
	if err := tr.Track(start); err == nil {
		t.Fatal("duplicate reservation")
	}
	changed = clone(a)
	changed.Thread = "other"
	if err := tr.Track(changed); err == nil {
		t.Fatal("routing changed")
	}
	if err := tr.MarkRead(start); err == nil {
		t.Fatal("incomplete read")
	}
	n := receipt("a", "failed", "")
	n.Reason = &Reason{Code: 1}
	_ = tr.Apply(n)
	if err := tr.MarkRead(start); err == nil {
		t.Fatal("failed read")
	}
	tr = tracker(t, Limits{ContentBytes: 2})
	if err := tr.Track(a); err == nil {
		t.Fatal("content cap")
	}
	_ = tr.Track(start)
	if err := tr.Track(a); err == nil {
		t.Fatal("assembled cap")
	}
	if _, err := NewTracker("", Limits{}); err == nil {
		t.Fatal("peer")
	}
	if _, err := NewTracker("bob", Limits{Entries: -1}); err == nil {
		t.Fatal("limits")
	}
	// Same local ID from two senders is ambiguous within one directional tracker.
	tr = tracker(t, Limits{})
	_ = tr.Track(a)
	changed = clone(a)
	changed.From = "other"
	_ = tr.Track(changed)
	if err := tr.Apply(receipt("a", "received", "")); err == nil {
		t.Fatal("ambiguous marker")
	}
}
func TestTrackerConcurrentReaders(t *testing.T) {
	tr := tracker(t, Limits{})
	_ = tr.Track(deliveryMessage("a", "t"))
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				_ = tr.Pending()
				_ = tr.Len()
			}
		})
	}
	wg.Wait()
}
