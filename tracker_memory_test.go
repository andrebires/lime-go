package lime

import "testing"

func TestRetryByteBudgetAndRelease(t *testing.T) {
	tr := tracker(t, Limits{RetryBytes: 10})
	a := deliveryMessage("a", "t")
	a.Content = []byte(`"1234"`)
	if err := tr.Track(a); err != nil || tr.RetainedBytes() != 6 {
		t.Fatal("retained", err, tr.RetainedBytes())
	}
	b := deliveryMessage("b", "t")
	b.Content = []byte(`"5678"`)
	if err := tr.Track(b); err == nil || tr.Len() != 1 {
		t.Fatal("retry byte capacity lost pending entry")
	}
	if err := tr.Apply(receipt("a", "consumed", "")); err != nil || tr.RetainedBytes() != 6 {
		t.Fatal("read released retry payload")
	}
	if err := tr.Apply(receipt("a", "received", "")); err != nil || tr.RetainedBytes() != 0 {
		t.Fatal("receipt did not release payload")
	}
	if err := tr.Track(a); err != nil {
		t.Fatal("duplicate acknowledged retry", err)
	}
	if err := tr.Track(b); err != nil {
		t.Fatal("budget did not recover", err)
	}
	fail := receipt("b", "failed", "")
	fail.Reason = &Reason{Code: 1}
	if err := tr.Apply(fail); err != nil {
		t.Fatal(err)
	}
	if tr.RetainedBytes() != 6 {
		t.Fatal("failure silently dropped pending payload")
	}
	if err := tr.Resolve(b); err != nil || tr.RetainedBytes() != 0 {
		t.Fatal("explicit resolve did not release", err)
	}
	tr.Reset()
	start := a
	start.Content = nil
	start.Stream = "start"
	if err := tr.Track(start); err != nil {
		t.Fatal(err)
	}
	if err := tr.Track(b); err != nil {
		t.Fatal(err)
	}
	if err := tr.Track(a); err == nil {
		t.Fatal("completion exceeded byte budget")
	}
	if len(tr.Pending()) != 1 || tr.RetainedBytes() != 6 {
		t.Fatal("incomplete became retryable")
	}
	tr.Reset()
	if tr.RetainedBytes() != 0 {
		t.Fatal("reset")
	}
	if _, err := NewTracker("bob", Limits{RetryBytes: -1}); err == nil {
		t.Fatal("negative byte limit")
	}
}
func TestRetiredMarkerRetryIdentity(t *testing.T) {
	tr := tracker(t, Limits{Entries: 1})
	a := deliveryMessage("a", "t")
	_ = tr.Track(a)
	_ = tr.Apply(receipt("a", "received", ""))
	_ = tr.Track(deliveryMessage("b", "t"))
	if err := tr.Track(a); err != nil || tr.Len() != 1 {
		t.Fatal("retired retry got a second position", err)
	}
	changed := a
	changed.Content = []byte(`"different"`)
	if err := tr.Track(changed); err == nil {
		t.Fatal("same retired revision changed")
	}
	changed = a
	changed.Content = nil
	changed.Stream = "start"
	if err := tr.Track(changed); err == nil {
		t.Fatal("retired chunk replay")
	}
	for _, field := range []string{"to", "thread"} {
		n := receipt("a", "received", "session")
		if field == "to" {
			n.To = "mallory"
		} else {
			n.Thread = "other"
		}
		if err := tr.Apply(n); err == nil {
			t.Fatal("retired routing mismatch")
		}
	}
}
func TestResolvedFailureDoesNotBlockReadPrefix(t *testing.T) {
	tr := tracker(t, Limits{})
	a, b := deliveryMessage("a", "t"), deliveryMessage("b", "t")
	_ = tr.Track(a)
	_ = tr.Track(b)
	n := receipt("a", "failed", "")
	n.Reason = &Reason{Code: 1}
	_ = tr.Apply(n)
	_ = tr.Resolve(a)
	_ = tr.MarkRead(b)
	if _, err := tr.ReadNotification(b, "thread"); err != nil {
		t.Fatal("resolved read prefix", err)
	}
}
func TestDelayedRevisionDoesNotAcknowledgeNewEdit(t *testing.T) {
	tr := tracker(t, Limits{})
	a := deliveryMessage("a", "t")
	_ = tr.Track(a)
	edit := a
	edit.Rev = 2
	_ = tr.Track(edit)
	if err := tr.Apply(receipt("a", "received", "session")); err != nil {
		t.Fatal(err)
	}
	if len(tr.Pending()) != 1 || tr.Pending()[0].Rev != 2 {
		t.Fatal("old revision acknowledged edit")
	}
	if err := tr.Apply(receipt("a", "received", "session")); err != nil || tr.Len() != 1 {
		t.Fatal("duplicate progress acknowledged edit", err)
	}
	n := receipt("a", "received", "session")
	n.Rev = 2
	if err := tr.Apply(n); err != nil || tr.Len() != 0 {
		t.Fatal("edit receipt", err)
	}
}
