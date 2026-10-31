package layoutstate

import (
	"context"
	"testing"
	"time"
)

// GNOME reorders the input sources by uploading a new keymap and locking the
// group of the active source under the new order (us active, ru moved first:
// "ru,us,us" with group 1). The poll that sees the new group must name it
// through the new table, never through the cached one ([us, ru, us] would make
// group 1 "ru" for up to 2 s and auto mode would judge English words as typed
// in ru).
func TestX11SourceRereadsTableWhenGroupChanges(t *testing.T) {
	conn := newX11LayoutTestConn(0, "us,ru,us", ",,")
	src, _ := x11LayoutTestSource(&x11LayoutTestLog{}, conn)
	tr, _ := newTestTracker(usRu)
	tr.AddSource(src)
	tr.Start(context.Background())
	defer tr.Stop()
	rec := newChangeRecorder()
	tr.OnChange(rec.record)

	waitUntil(t, func() bool { st := tr.State(); return st.Confidence == Confirmed && st.Index == 0 })
	// mutter writes _XKB_RULES_NAMES, then locks the group.
	conn.setRules("ru,us,us", ",,")
	conn.setGroup(1)
	calls, _ := conn.calls()
	x11LayoutTestWaitCalls(t, conn, calls+5)
	rec.mu.Lock()
	seen := append([]State(nil), rec.states...)
	rec.mu.Unlock()
	for _, st := range seen {
		if st.Index == 1 || st.Engine == "xkb:ru::" {
			t.Fatalf("tracker reported %s after the reorder; the active layout is us", st)
		}
	}
	if st := tr.State(); st.Confidence != Confirmed || st.Index != 0 || st.Engine != "xkb:us::" {
		t.Fatalf("state after reorder = %s, want Confirmed index 0 (us)", st)
	}
}

// A drained event (a keymap upload sends MappingNotify) also re-reads the
// table, for a reorder that keeps the active group index.
func TestX11SourceRereadsTableAfterDrainedEvents(t *testing.T) {
	conn := newX11LayoutTestConn(1, "us,ru,us", ",,")
	src, _ := x11LayoutTestSource(&x11LayoutTestLog{}, conn)
	sink := newRecordingSink()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go src.Run(ctx, sink)

	sink.waitFor(t, sinkEvent{kind: "engine", engine: "xkb:ru::"})
	// The group stays 1 while the table changes under it.
	conn.setRules("de,us,us", ",,")
	before, _ := conn.calls()
	conn.queue(1)
	sink.waitFor(t, sinkEvent{kind: "engine", engine: "xkb:us::"})
	// Within a few polls, not at the periodic refresh (every 40th poll).
	if after, _ := conn.calls(); after-before > 5 {
		t.Fatalf("table re-read after %d polls, want it at the poll that drained the event", after-before)
	}
}

// After a Resync whose Query failed replaced the state with a fallback, the
// next identical poll restores Confirmed (it used to stay Settings/Unknown,
// auto mode unavailable, until the next real layout switch).
func TestX11SourceReportsAgainAfterResyncFallback(t *testing.T) {
	conn := newX11LayoutTestConn(1, "us,ru", ",")
	src, _ := x11LayoutTestSource(&x11LayoutTestLog{}, conn)
	tr, _ := newTestTracker(usRu)
	tr.AddSource(src)
	res := &fakeResyncer{layout: Layout{Name: "ru"}}
	tr.AddResync(res)
	tr.Start(context.Background())
	defer tr.Stop()
	waitUntil(t, func() bool { st := tr.State(); return st.Confidence == Confirmed && st.Index == 1 })

	// Hold the request mutex like a long poll, so Query's wait expires.
	src.lock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	if err := tr.Resync(ctx); err != nil {
		t.Fatalf("Resync() error = %v, want the gsettings fallback", err)
	}
	if elapsed := time.Since(start); elapsed > 900*time.Millisecond {
		t.Fatalf("Resync() took %v: Query's wait for the poll mutex is not bounded by its context", elapsed)
	}
	if st := tr.State(); st.Confidence != Settings || st.Index != 1 {
		src.unlock()
		t.Fatalf("state after Resync = %s, want the Settings fallback", st)
	}
	src.unlock()
	waitUntil(t, func() bool { st := tr.State(); return st.Confidence == Confirmed && st.Index == 1 })
	if st := tr.State(); st.Source != SourceX11 {
		t.Fatalf("state = %s, want source x11", st)
	}
}

// Query gives up waiting for a poll that holds the request mutex when its
// context ends.
func TestX11SourceQueryLiveWaitIsBounded(t *testing.T) {
	conn := newX11LayoutTestConn(0, "us,ru", ",")
	src, _ := x11LayoutTestSource(&x11LayoutTestLog{}, conn)
	sink := newRecordingSink()
	runCtx, stop := context.WithCancel(context.Background())
	defer stop()
	go src.Run(runCtx, sink)
	sink.waitFor(t, sinkEvent{kind: "engine", engine: "xkb:us::"})

	src.lock()
	defer src.unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := src.Query(ctx); err == nil {
		t.Fatal("Query() with the poll mutex held: error = nil")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("Query() took %v, want about 50 ms", elapsed)
	}
}
