package layoutstate

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

var usRu = [2]Layout{{Name: "us"}, {Name: "ru"}}

// fakeSource is a Source whose Run hands out the sink and whose Query result
// is configurable.
type fakeSource struct {
	mu          sync.Mutex
	queryEngine string
	queryErr    error
	queries     int
	queryGate   chan struct{} // when non-nil, Query waits for it
	sinks       chan Sink
	runExited   chan struct{}
}

func newFakeSource() *fakeSource {
	return &fakeSource{sinks: make(chan Sink, 1), runExited: make(chan struct{})}
}

func (f *fakeSource) Name() string { return SourceIBus }

func (f *fakeSource) Run(ctx context.Context, sink Sink) {
	defer close(f.runExited)
	f.sinks <- sink
	<-ctx.Done()
}

func (f *fakeSource) Query(ctx context.Context) (string, error) {
	f.mu.Lock()
	f.queries++
	gate := f.queryGate
	engine, err := f.queryEngine, f.queryErr
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return engine, err
}

func (f *fakeSource) queryCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.queries
}

type fakeResyncer struct {
	mu     sync.Mutex
	layout Layout
	err    error
	calls  int
}

func (f *fakeResyncer) Current(context.Context) (Layout, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.layout, f.err
}

func (f *fakeResyncer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeTimers replaces Tracker.afterFunc; timers fire only when the test says so.
type fakeTimers struct {
	mu     sync.Mutex
	timers []*fakeTimer
}

type fakeTimer struct {
	d       time.Duration
	f       func()
	stopped bool
}

func (ft *fakeTimers) afterFunc(d time.Duration, f func()) func() bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	tm := &fakeTimer{d: d, f: f}
	ft.timers = append(ft.timers, tm)
	return func() bool {
		ft.mu.Lock()
		defer ft.mu.Unlock()
		was := !tm.stopped
		tm.stopped = true
		return was
	}
}

func (ft *fakeTimers) count() int {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return len(ft.timers)
}

func (ft *fakeTimers) get(i int) *fakeTimer {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return ft.timers[i]
}

func (ft *fakeTimers) isStopped(i int) bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return ft.timers[i].stopped
}

// fire runs timer i synchronously even if it was stopped, emulating a
// callback that was already running when Stop was called.
func (ft *fakeTimers) fire(i int) {
	ft.get(i).f()
}

func newTestTracker(layouts [2]Layout) (*Tracker, *fakeTimers) {
	timers := &fakeTimers{}
	tr := New(Options{Layouts: layouts, HintTimeout: 250 * time.Millisecond})
	tr.afterFunc = timers.afterFunc
	return tr, timers
}

// changeRecorder collects OnChange callbacks.
type changeRecorder struct {
	mu     sync.Mutex
	states []State
	ch     chan State
}

func newChangeRecorder() *changeRecorder {
	return &changeRecorder{ch: make(chan State, 64)}
}

func (r *changeRecorder) record(st State) {
	r.mu.Lock()
	r.states = append(r.states, st)
	r.mu.Unlock()
	r.ch <- st
}

func (r *changeRecorder) waitFor(t *testing.T, pred func(State) bool) State {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case st := <-r.ch:
			if pred(st) {
				return st
			}
		case <-timeout:
			t.Fatal("timed out waiting for OnChange")
			return State{}
		}
	}
}

func (r *changeRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.states)
}

func TestTrackerInitialUnknown(t *testing.T) {
	tr, _ := newTestTracker(usRu)
	st := tr.State()
	if st.Confidence != Unknown || st.Index != -1 || st.Generation != 0 || st.Engine != "" {
		t.Fatalf("initial state = %+v, want Unknown/-1/gen 0", st)
	}
	if idx, known := tr.Current(); known || idx != -1 {
		t.Fatalf("Current() = %d, %v; want -1, false", idx, known)
	}
}

func TestTrackerEngineMapping(t *testing.T) {
	tests := []struct {
		name    string
		layouts [2]Layout
		engine  string
		index   int
		conf    Confidence
	}{
		{"us", usRu, "xkb:us::eng", 0, Confirmed},
		{"ru", usRu, "xkb:ru::rus", 1, Confirmed},
		{"variant engine, plain member", usRu, "xkb:ru:phonetic:rus", 1, Confirmed},
		{"not in pair", usRu, "xkb:de::ger", -1, Unknown},
		{"non-xkb", usRu, "libpinyin", -1, Unknown},
		{"empty engine", usRu, "", -1, Unknown},
		{"same name pair plain", [2]Layout{{Name: "us"}, {Name: "us", Variant: "dvorak"}}, "xkb:us::eng", 0, Confirmed},
		{"same name pair variant", [2]Layout{{Name: "us"}, {Name: "us", Variant: "dvorak"}}, "xkb:us:dvorak:eng", 1, Confirmed},
		{"same name pair other variant", [2]Layout{{Name: "us"}, {Name: "us", Variant: "dvorak"}}, "xkb:us:intl:eng", -1, Unknown},
		{"variant member, plain engine", [2]Layout{{Name: "us", Variant: "dvorak"}, {Name: "ru"}}, "xkb:us::eng", 0, Confirmed},
		{"two variants, plain engine", [2]Layout{{Name: "us", Variant: "intl"}, {Name: "us", Variant: "dvorak"}}, "xkb:us::eng", -1, Unknown},
		{"identical pair", [2]Layout{{Name: "us"}, {Name: "us"}}, "xkb:us::eng", -1, Unknown},
		{"empty pair", [2]Layout{}, "xkb:us::eng", -1, Unknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, _ := newTestTracker(tt.layouts)
			tr.Engine(SourceIBus, tt.engine)
			st := tr.State()
			if st.Index != tt.index || st.Confidence != tt.conf {
				t.Fatalf("Engine(%q) state = %+v, want index %d confidence %s", tt.engine, st, tt.index, tt.conf)
			}
			if st.Engine != tt.engine || st.Source != SourceIBus {
				t.Fatalf("state engine/source = %q/%q, want %q/%q", st.Engine, st.Source, tt.engine, SourceIBus)
			}
			idx, known := tr.Current()
			if idx != tt.index || known != (tt.conf == Confirmed) {
				t.Fatalf("Current() = %d, %v", idx, known)
			}
		})
	}
}

func TestTrackerDeduplicatesIdenticalEngines(t *testing.T) {
	tr, _ := newTestTracker(usRu)
	rec := newChangeRecorder()
	tr.OnChange(rec.record)
	tr.Start(context.Background())
	defer tr.Stop()

	tr.Engine(SourceIBus, "xkb:us::eng")
	rec.waitFor(t, func(st State) bool { return st.Generation == 1 })
	tr.Engine(SourceIBus, "xkb:us::eng")
	tr.Engine(SourceIBus, "xkb:us::eng")
	if gen := tr.State().Generation; gen != 1 {
		t.Fatalf("generation after duplicates = %d, want 1", gen)
	}
	tr.Engine(SourceIBus, "xkb:ru::rus")
	rec.waitFor(t, func(st State) bool { return st.Generation == 2 })
	if n := rec.count(); n != 2 {
		t.Fatalf("callbacks = %d, want 2 (duplicates must not notify)", n)
	}
}

func TestTrackerHintPendingThenSignalConfirmed(t *testing.T) {
	tr, timers := newTestTracker(usRu)
	src := newFakeSource()
	src.queryEngine = "xkb:us::eng"
	tr.AddSource(src)

	tr.Engine(SourceIBus, "xkb:us::eng")
	tr.NoteSwitchHint()
	st := tr.State()
	if st.Confidence != Pending || st.Index != 0 || st.Generation != 2 {
		t.Fatalf("after hint state = %+v, want Pending index 0 gen 2", st)
	}
	if _, known := tr.Current(); known {
		t.Fatal("Current() known while Pending")
	}
	if timers.count() != 1 || timers.get(0).d != 250*time.Millisecond {
		t.Fatalf("hint timer not armed with HintTimeout")
	}

	tr.Engine(SourceIBus, "xkb:ru::rus")
	st = tr.State()
	if st.Confidence != Confirmed || st.Index != 1 || st.Generation != 3 {
		t.Fatalf("after signal state = %+v, want Confirmed index 1 gen 3", st)
	}
	if !timers.isStopped(0) {
		t.Fatal("hint timer not cancelled by the signal")
	}
	timers.fire(0) // a callback racing with the cancel must be a no-op
	if src.queryCount() != 0 {
		t.Fatalf("Query called %d times after a cancelled hint", src.queryCount())
	}
	if gen := tr.State().Generation; gen != 3 {
		t.Fatalf("generation = %d after stale timer, want 3", gen)
	}
}

func TestTrackerHintTimeoutResyncsOnce(t *testing.T) {
	tr, timers := newTestTracker(usRu)
	src := newFakeSource()
	src.queryEngine = "xkb:ru::rus"
	res := &fakeResyncer{layout: Layout{Name: "us"}}
	tr.AddSource(src)
	tr.AddResync(res)

	tr.Engine(SourceIBus, "xkb:us::eng")
	tr.NoteSwitchHint()
	timers.fire(0)
	if n := src.queryCount(); n != 1 {
		t.Fatalf("Query calls = %d, want 1", n)
	}
	if res.callCount() != 0 {
		t.Fatal("resyncer consulted although Query succeeded")
	}
	st := tr.State()
	if st.Confidence != Confirmed || st.Index != 1 || st.Source != SourceIBus {
		t.Fatalf("after resync state = %+v, want Confirmed index 1 from ibus", st)
	}
	timers.fire(0)
	if n := src.queryCount(); n != 1 {
		t.Fatalf("Query calls after second fire = %d, want 1", n)
	}
}

func TestTrackerHintRaceSignalBeforeHint(t *testing.T) {
	tr, timers := newTestTracker(usRu)
	src := newFakeSource()
	src.queryEngine = "xkb:ru::rus"
	res := &fakeResyncer{layout: Layout{Name: "ru"}}
	tr.AddSource(src)
	tr.AddResync(res)

	tr.Engine(SourceIBus, "xkb:us::eng")
	// The tracker processes GlobalEngineChanged before the Run goroutine
	// delivers the physical hint.
	tr.Engine(SourceIBus, "xkb:ru::rus")
	tr.NoteSwitchHint()
	if st := tr.State(); st.Confidence != Pending || st.Index != 1 {
		t.Fatalf("after late hint state = %+v, want Pending index 1", st)
	}
	before := tr.State().Generation
	timers.fire(0)
	st := tr.State()
	if st.Confidence != Confirmed || st.Index != 1 || st.Generation != before+1 {
		t.Fatalf("after timeout state = %+v, want Confirmed index 1 gen %d (not Settings)", st, before+1)
	}
	if res.callCount() != 0 {
		t.Fatal("gsettings consulted although Query succeeded")
	}
}

func TestTrackerResyncQueryFailsUsesSettings(t *testing.T) {
	tr, timers := newTestTracker(usRu)
	src := newFakeSource()
	src.queryErr = errors.New("connection refused")
	res := &fakeResyncer{layout: Layout{Name: "ru"}}
	tr.AddSource(src)
	tr.AddResync(res)

	tr.Engine(SourceIBus, "xkb:us::eng")
	tr.NoteSwitchHint()
	timers.fire(0)
	st := tr.State()
	if st.Confidence != Settings || st.Index != 1 || st.Source != SourceGSettings {
		t.Fatalf("state = %+v, want Settings index 1 from gsettings", st)
	}
	if _, known := tr.Current(); known {
		t.Fatal("Settings state must not be known")
	}
	if src.queryCount() != 1 || res.callCount() != 1 {
		t.Fatalf("query/resync calls = %d/%d, want 1/1", src.queryCount(), res.callCount())
	}

	// A hint from Settings is honoured as well.
	tr.NoteSwitchHint()
	if st := tr.State(); st.Confidence != Pending {
		t.Fatalf("hint from Settings: %+v, want Pending", st)
	}
}

func TestTrackerResyncSettingsLayoutOutsidePair(t *testing.T) {
	tr, _ := newTestTracker(usRu)
	tr.AddResync(&fakeResyncer{layout: Layout{Name: "de"}})
	if err := tr.Resync(context.Background()); err != nil {
		t.Fatalf("Resync() error = %v", err)
	}
	if st := tr.State(); st.Confidence != Unknown || st.Index != -1 || st.Source != SourceGSettings {
		t.Fatalf("state = %+v, want Unknown/-1 from gsettings", st)
	}
}

func TestTrackerResyncAllFailUnknown(t *testing.T) {
	tr, _ := newTestTracker(usRu)
	src := newFakeSource()
	src.queryErr = errors.New("no ibus")
	tr.AddSource(src)
	tr.AddResync(&fakeResyncer{err: errors.New("no gsettings")})
	tr.Engine(SourceIBus, "xkb:us::eng")

	if err := tr.Resync(context.Background()); err == nil {
		t.Fatal("Resync() error = nil, want joined errors")
	}
	if st := tr.State(); st.Confidence != Unknown || st.Index != -1 {
		t.Fatalf("state = %+v, want Unknown", st)
	}
}

func TestTrackerResyncDropsResultAfterConcurrentSignal(t *testing.T) {
	tr, _ := newTestTracker(usRu)
	src := newFakeSource()
	src.queryEngine = "xkb:us::eng"
	src.queryGate = make(chan struct{})
	tr.AddSource(src)
	tr.Engine(SourceIBus, "xkb:us::eng")

	done := make(chan error, 1)
	go func() { done <- tr.Resync(context.Background()) }()
	waitUntil(t, func() bool { return src.queryCount() == 1 })
	tr.Engine(SourceIBus, "xkb:ru::rus") // fresher signal while Query is in flight
	close(src.queryGate)
	if err := <-done; err != nil {
		t.Fatalf("Resync() error = %v", err)
	}
	if st := tr.State(); st.Index != 1 || st.Confidence != Confirmed {
		t.Fatalf("stale resync overwrote a fresher signal: %+v", st)
	}
}

func TestTrackerAvailableFalseUnknownWithCallback(t *testing.T) {
	tr, timers := newTestTracker(usRu)
	rec := newChangeRecorder()
	tr.OnChange(rec.record)
	tr.Start(context.Background())
	defer tr.Stop()

	tr.Engine(SourceIBus, "xkb:ru::rus")
	rec.waitFor(t, func(st State) bool { return st.Confidence == Confirmed })
	tr.NoteSwitchHint()
	tr.Available(SourceIBus, false)
	st := rec.waitFor(t, func(st State) bool { return st.Confidence == Unknown })
	if st.Index != -1 || st.Engine != "" {
		t.Fatalf("unavailable state = %+v, want index -1 and no engine", st)
	}
	if !timers.isStopped(0) {
		t.Fatal("hint timer survives source loss")
	}
	gen := tr.State().Generation
	tr.Available(SourceIBus, true)
	tr.Available(SourceIBus, false)
	if tr.State().Generation != gen {
		t.Fatal("Available(true)/repeated Available(false) changed the state")
	}
}

func TestTrackerHintIgnoredWhileUnknownAndRestartsWhilePending(t *testing.T) {
	tr, timers := newTestTracker(usRu)
	tr.NoteSwitchHint()
	if st := tr.State(); st.Confidence != Unknown || st.Generation != 0 || timers.count() != 0 {
		t.Fatalf("hint while Unknown changed state %+v or armed %d timers", st, timers.count())
	}

	tr.Engine(SourceIBus, "xkb:us::eng")
	tr.NoteSwitchHint()
	gen := tr.State().Generation
	tr.NoteSwitchHint()
	if tr.State().Generation != gen {
		t.Fatal("second hint while Pending bumped the generation")
	}
	if timers.count() != 2 || !timers.isStopped(0) || timers.isStopped(1) {
		t.Fatal("second hint did not restart the timer")
	}
}

func TestTrackerNilReceiver(t *testing.T) {
	var tr *Tracker
	tr.NoteSwitchHint()
	tr.Engine(SourceIBus, "xkb:us::eng")
	tr.Available(SourceIBus, false)
	tr.OnChange(func(State) {})
	tr.Start(context.Background())
	tr.Stop()
	if st := tr.State(); st.Confidence != Unknown || st.Index != -1 {
		t.Fatalf("nil State() = %+v", st)
	}
	if _, known := tr.Current(); known {
		t.Fatal("nil Current() known")
	}
	if err := tr.Resync(context.Background()); err == nil {
		t.Fatal("nil Resync() error = nil")
	}
}

func TestTrackerOnChangeNeverBlocksEngine(t *testing.T) {
	tr, _ := newTestTracker(usRu)
	release := make(chan struct{})
	var mu sync.Mutex
	var last State
	tr.OnChange(func(st State) {
		<-release
		mu.Lock()
		last = st
		mu.Unlock()
	})
	tr.Start(context.Background())
	defer tr.Stop()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 100 {
			if i%2 == 0 {
				tr.Engine(SourceIBus, "xkb:us::eng")
			} else {
				tr.Engine(SourceIBus, "xkb:ru::rus")
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Engine() blocked behind a blocking OnChange callback")
	}
	want := tr.State().Generation
	close(release)
	waitUntil(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return last.Generation == want
	})
}

func TestTrackerStartRunsSourcesAndStopWaits(t *testing.T) {
	tr, _ := newTestTracker(usRu)
	src := newFakeSource()
	tr.AddSource(src)
	rec := newChangeRecorder()
	tr.OnChange(rec.record)
	tr.Start(context.Background())

	sink := <-src.sinks
	sink.Engine(SourceIBus, "xkb:ru::rus")
	st := rec.waitFor(t, func(st State) bool { return st.Generation == 1 })
	if st.Index != 1 || st.Confidence != Confirmed {
		t.Fatalf("callback state = %+v", st)
	}
	tr.Stop()
	select {
	case <-src.runExited:
	default:
		t.Fatal("Stop() returned before Source.Run exited")
	}
	tr.Stop() // idempotent
	tr.Start(context.Background())
}

func TestTrackerRealTimerResync(t *testing.T) {
	tr := New(Options{Layouts: usRu, HintTimeout: 20 * time.Millisecond})
	src := newFakeSource()
	src.queryEngine = "xkb:ru::rus"
	tr.AddSource(src)
	tr.Start(context.Background())
	defer tr.Stop()
	sink := <-src.sinks
	sink.Engine(SourceIBus, "xkb:us::eng")
	tr.NoteSwitchHint()
	waitUntil(t, func() bool {
		st := tr.State()
		return st.Confidence == Confirmed && st.Index == 1
	})
}

func TestTrackerAtUsesClock(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	tr := New(Options{Layouts: usRu, Now: func() time.Time { return now }})
	now = now.Add(time.Second)
	tr.Engine(SourceIBus, "xkb:us::eng")
	if got := tr.State().At; !got.Equal(now) {
		t.Fatalf("At = %v, want %v", got, now)
	}
}

func TestParseXKBEngine(t *testing.T) {
	tests := []struct {
		in              string
		layout, variant string
		ok              bool
	}{
		{"xkb:us::eng", "us", "", true},
		{"xkb:ru::rus", "ru", "", true},
		{"xkb:us:dvorak:eng", "us", "dvorak", true},
		{"xkb:de:nodeadkeys:ger", "de", "nodeadkeys", true},
		{"xkb:us:altgr-intl:eng", "us", "altgr-intl", true},
		{"xkb:us", "us", "", true},
		{"xkb:", "", "", false},
		{"xkb::dvorak:eng", "", "", false},
		{"libpinyin", "", "", false},
		{"m17n:ru:kbd", "", "", false},
		{"", "", "", false},
	}
	for _, tt := range tests {
		layout, variant, ok := ParseXKBEngine(tt.in)
		if layout != tt.layout || variant != tt.variant || ok != tt.ok {
			t.Errorf("ParseXKBEngine(%q) = %q, %q, %v; want %q, %q, %v",
				tt.in, layout, variant, ok, tt.layout, tt.variant, tt.ok)
		}
	}
}

func TestConfidenceString(t *testing.T) {
	for c, want := range map[Confidence]string{
		Unknown: "unknown", Pending: "pending", Settings: "settings", Confirmed: "confirmed", 9: "confidence(9)",
	} {
		if got := c.String(); got != want {
			t.Errorf("Confidence(%d).String() = %q, want %q", c, got, want)
		}
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached in time")
		}
		time.Sleep(2 * time.Millisecond)
	}
}
