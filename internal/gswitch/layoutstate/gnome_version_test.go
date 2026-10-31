package layoutstate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

func TestParseShellVersion(t *testing.T) {
	tests := []struct {
		in           string
		major, minor int
		ok           bool
	}{
		{"43.9", 43, 9, true},
		{"49.3", 49, 3, true},
		{"50.6", 50, 6, true},
		{"49.rc", 49, 0, true},
		{"49.rc.1", 49, 0, true},
		{"48.alpha", 48, 0, true},
		{"48.beta.2", 48, 0, true},
		{"47.0.1", 47, 0, true},
		{"46", 46, 0, true},
		{" 43.9\n", 43, 9, true},
		{"", 0, 0, false},
		{"x", 0, 0, false},
		{".9", 0, 0, false},
		{"-1.2", 0, 0, false},
		{"4x.3", 0, 0, false},
	}
	for _, tt := range tests {
		major, minor, ok := ParseShellVersion(tt.in)
		if major != tt.major || minor != tt.minor || ok != tt.ok {
			t.Errorf("ParseShellVersion(%q) = %d, %d, %v; want %d, %d, %v",
				tt.in, major, minor, ok, tt.major, tt.minor, tt.ok)
		}
	}
}

func TestIBusEngineTracksLayout(t *testing.T) {
	for version, want := range map[string]bool{
		"43.9":   false,
		"49.2":   false,
		"49.2.1": false,
		"49.rc":  false,
		"49.3":   true,
		"49.10":  true,
		"50.6":   true,
		"50.rc":  true,
		"":       false,
		"x":      false,
	} {
		if got := IBusEngineTracksLayout(version); got != want {
			t.Errorf("IBusEngineTracksLayout(%q) = %v, want %v", version, got, want)
		}
	}
}

func TestIBusSourceAllowed(t *testing.T) {
	probeErr := fmt.Errorf("%w: boom", ErrShellUnavailable)
	tests := []struct {
		name         string
		layoutSource string
		info         ShellInfo
		ok, deferred bool
		reason       string
		warning      string // the exact warning; "" ⇒ none
	}{
		{name: "auto tracking", layoutSource: "auto", info: ShellInfo{Version: "50.6"}, ok: true},
		{name: "auto boundary", layoutSource: "auto", info: ShellInfo{Version: "49.3"}, ok: true},
		{name: "auto static", layoutSource: "auto", info: ShellInfo{Version: "43.9"}, reason: GuardReasonIBusEngineStatic},
		{name: "auto below boundary", layoutSource: "auto", info: ShellInfo{Version: "49.2"}, reason: GuardReasonIBusEngineStatic},
		{name: "auto probe error", layoutSource: "auto", info: ShellInfo{Err: probeErr}, deferred: true, reason: GuardReasonShellVersionUnknown},
		{name: "ibus tracking", layoutSource: "ibus", info: ShellInfo{Version: "50.6"}, ok: true},
		{name: "ibus static", layoutSource: "ibus", info: ShellInfo{Version: "43.9"}, ok: true,
			reason: GuardReasonIBusEngineStatic,
			warning: "layout-source=ibus forces the IBus source although GNOME Shell 43.9 reports a fixed engine below 49.3: " +
				"every layout reads as the first one, so automatic correction misjudges words typed in the second layout"},
		{name: "ibus below boundary", layoutSource: "ibus", info: ShellInfo{Version: "49.2"}, ok: true,
			reason: GuardReasonIBusEngineStatic,
			warning: "layout-source=ibus forces the IBus source although GNOME Shell 49.2 reports a fixed engine below 49.3: " +
				"every layout reads as the first one, so automatic correction misjudges words typed in the second layout"},
		{name: "ibus boundary", layoutSource: "ibus", info: ShellInfo{Version: "49.3"}, ok: true},
		{name: "ibus probe error", layoutSource: "ibus", info: ShellInfo{Err: probeErr}, ok: true,
			reason:  GuardReasonShellVersionUnknown,
			warning: "layout-source=ibus forces the IBus source; GNOME Shell version unknown (org.gnome.Shell is not available on the session bus: boom)"},
		{name: "none", layoutSource: "none", info: ShellInfo{Version: "50.6"}},
		{name: "none static", layoutSource: "none", info: ShellInfo{Version: "43.9"}, reason: GuardReasonIBusEngineStatic},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, deferred, reason, warning := IBusSourceAllowed(tt.layoutSource, tt.info)
			if ok != tt.ok || deferred != tt.deferred || reason != tt.reason {
				t.Fatalf("IBusSourceAllowed(%q, %+v) = %v, %v, %q; want %v, %v, %q",
					tt.layoutSource, tt.info, ok, deferred, reason, tt.ok, tt.deferred, tt.reason)
			}
			if warning != tt.warning {
				t.Fatalf("warning = %q, want %q", warning, tt.warning)
			}
		})
	}
}

func TestGuardStateString(t *testing.T) {
	for state, want := range map[GuardState]string{
		GuardPending:  "pending",
		GuardPassed:   "passed",
		GuardRefused:  "refused",
		GuardState(7): "guard(7)",
	} {
		if got := state.String(); got != want {
			t.Errorf("GuardState(%d).String() = %q, want %q", state, got, want)
		}
	}
}

func TestShellVersionFromValue(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
		ok   bool
	}{
		{name: "variant of string", in: dbus.MakeVariant("50.6"), want: "50.6", ok: true},
		{name: "double variant", in: dbus.MakeVariant(dbus.MakeVariant(" 43.9 ")), want: "43.9", ok: true},
		{name: "plain string", in: "49.rc", want: "49.rc", ok: true},
		{name: "integer", in: dbus.MakeVariant(uint32(50)), ok: false},
		{name: "nil", in: nil, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := shellVersionFromValue(tt.in)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("shellVersionFromValue(%v) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
			}
		})
	}
}

// gvTestInner is the guarded Source: it records Run calls and the sink it
// received and serves a fixed Query answer.
type gvTestInner struct {
	mu      sync.Mutex
	runs    int
	sink    Sink
	started chan struct{}
	engine  string
}

func newGVTestInner(engine string) *gvTestInner {
	return &gvTestInner{started: make(chan struct{}), engine: engine}
}

func (f *gvTestInner) Name() string { return SourceIBus }

func (f *gvTestInner) Run(ctx context.Context, sink Sink) {
	f.mu.Lock()
	f.runs++
	f.sink = sink
	first := f.runs == 1
	f.mu.Unlock()
	if first {
		close(f.started)
	}
	sink.Available(f.Name(), true)
	sink.Engine(f.Name(), f.engine)
	<-ctx.Done()
}

func (f *gvTestInner) Query(context.Context) (string, error) { return f.engine, nil }

func (f *gvTestInner) runCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs
}

func (f *gvTestInner) sinkRef() Sink {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sink
}

// gvTestGuard wires a guard to a scripted probe and an instant sleep that
// records the requested delays.
type gvTestGuard struct {
	guard  *GuardedIBusSource
	inner  *gvTestInner
	mu     sync.Mutex
	probes int
	sleeps []time.Duration
	logs   []string
}

func newGVTestGuard(t *testing.T, answers []ShellInfo) *gvTestGuard {
	t.Helper()
	g := &gvTestGuard{inner: newGVTestInner("xkb:ru::rus")}
	g.guard = NewGuardedIBusSource(g.inner, func(format string, args ...any) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.logs = append(g.logs, fmt.Sprintf(format, args...))
	})
	g.guard.probe = func(ctx context.Context) ShellInfo {
		if ctx.Err() != nil {
			t.Error("probe called with a done context")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Error("probe context has no deadline")
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		i := min(g.probes, len(answers)-1)
		g.probes++
		return answers[i]
	}
	g.guard.sleep = func(ctx context.Context, d time.Duration) bool {
		g.mu.Lock()
		g.sleeps = append(g.sleeps, d)
		g.mu.Unlock()
		return ctx.Err() == nil
	}
	return g
}

func (g *gvTestGuard) snapshot() (probes int, sleeps []time.Duration, logs []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.probes, append([]time.Duration(nil), g.sleeps...), append([]string(nil), g.logs...)
}

func gvTestRun(t *testing.T, src Source) (*recordingSink, context.CancelFunc, <-chan struct{}) {
	t.Helper()
	sink := newRecordingSink()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		src.Run(ctx, sink)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("GuardedIBusSource.Run did not return after cancel")
		}
	})
	return sink, cancel, done
}

func TestGuardedIBusSourceRunsInnerAfterProbeSucceeds(t *testing.T) {
	probeErr := fmt.Errorf("%w: not yet", ErrShellUnavailable)
	g := newGVTestGuard(t, []ShellInfo{{Err: probeErr}, {Err: probeErr}, {Err: probeErr}, {Version: "50.6"}})
	if g.guard.Name() != SourceIBus {
		t.Fatalf("Name() = %q, want %q", g.guard.Name(), SourceIBus)
	}
	if _, err := g.guard.Query(context.Background()); !errors.Is(err, ErrShellUnavailable) {
		t.Fatalf("Query() before Run = %v, want ErrShellUnavailable", err)
	}

	sink, _, _ := gvTestRun(t, g.guard)
	sink.waitFor(t, sinkEvent{kind: "available", ok: true})
	sink.waitFor(t, sinkEvent{kind: "engine", engine: "xkb:ru::rus"})

	if got := g.guard.State(); got != GuardPassed {
		t.Fatalf("State() = %s, want passed", got)
	}
	if n := g.inner.runCount(); n != 1 {
		t.Fatalf("inner Run called %d times, want 1", n)
	}
	probes, sleeps, logs := g.snapshot()
	if probes != 4 {
		t.Fatalf("probes = %d, want 4", probes)
	}
	wantSleeps := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	if len(sleeps) != len(wantSleeps) {
		t.Fatalf("sleeps = %v, want %v", sleeps, wantSleeps)
	}
	for i, d := range wantSleeps {
		if sleeps[i] != d {
			t.Fatalf("sleeps = %v, want %v", sleeps, wantSleeps)
		}
	}
	// The repeated probe error is logged once; the pass is logged once.
	var waiting, enabled int
	for _, line := range logs {
		switch {
		case strings.HasPrefix(line, "IBus layout source waiting for GNOME Shell"):
			waiting++
		case strings.Contains(line, "IBus layout source enabled: GNOME Shell 50.6"):
			enabled++
		default:
			t.Errorf("unexpected log line %q", line)
		}
	}
	if waiting != 1 || enabled != 1 {
		t.Fatalf("logs = %q, want one waiting and one enabled line", logs)
	}
	if engine, err := g.guard.Query(context.Background()); err != nil || engine != "xkb:ru::rus" {
		t.Fatalf("Query() after pass = %q, %v; want delegation to inner", engine, err)
	}
	if g.inner.sinkRef() != sink {
		t.Fatal("inner Run did not receive the guard's sink")
	}
}

func TestGuardedIBusSourceBackoffIsCapped(t *testing.T) {
	probeErr := errors.New("still down")
	answers := make([]ShellInfo, 0, 9)
	for range 8 {
		answers = append(answers, ShellInfo{Err: probeErr})
	}
	answers = append(answers, ShellInfo{Version: "49.3"})
	g := newGVTestGuard(t, answers)
	sink, _, _ := gvTestRun(t, g.guard)
	sink.waitFor(t, sinkEvent{kind: "engine", engine: "xkb:ru::rus"})
	_, sleeps, _ := g.snapshot()
	want := []time.Duration{1, 2, 4, 8, 16, 30, 30, 30}
	if len(sleeps) != len(want) {
		t.Fatalf("sleeps = %v, want %d entries", sleeps, len(want))
	}
	for i, s := range want {
		if sleeps[i] != s*time.Second {
			t.Fatalf("sleeps = %v, want %v seconds", sleeps, want)
		}
	}
}

func TestGuardedIBusSourceRefusesStaticEngine(t *testing.T) {
	g := newGVTestGuard(t, []ShellInfo{{Version: "43.9"}})
	sink, _, done := gvTestRun(t, g.guard)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after a refused probe")
	}
	if got := g.guard.State(); got != GuardRefused {
		t.Fatalf("State() = %s, want refused", got)
	}
	if n := g.inner.runCount(); n != 0 {
		t.Fatalf("inner Run called %d times, want 0", n)
	}
	select {
	case ev := <-sink.ch:
		t.Fatalf("sink received %+v, want nothing", ev)
	default:
	}
	_, sleeps, logs := g.snapshot()
	if len(sleeps) != 0 {
		t.Fatalf("sleeps = %v, want none", sleeps)
	}
	if len(logs) != 1 || logs[0] != "IBus layout source disabled: GNOME Shell 43.9 reports a fixed engine for XKB sources (needs 49.3+)" {
		t.Fatalf("logs = %q, want exactly the disabled line", logs)
	}
	_, err := g.guard.Query(context.Background())
	if !errors.Is(err, ErrShellUnavailable) || !strings.Contains(err.Error(), "below GNOME Shell 49.3") {
		t.Fatalf("Query() after refusal = %v, want wrapped ErrShellUnavailable", err)
	}
}

func TestGuardedIBusSourceReturnsOnCancelWhilePending(t *testing.T) {
	g := newGVTestGuard(t, []ShellInfo{{Err: ErrShellUnavailable}})
	// Block in the backoff sleep until the cancel: Run must come back from it.
	g.guard.sleep = func(ctx context.Context, _ time.Duration) bool {
		<-ctx.Done()
		return false
	}
	_, cancel, done := gvTestRun(t, g.guard)
	waitUntil(t, func() bool { probes, _, _ := g.snapshot(); return probes >= 1 })
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel while pending")
	}
	if got := g.guard.State(); got != GuardPending {
		t.Fatalf("State() = %s, want pending", got)
	}
	if n := g.inner.runCount(); n != 0 {
		t.Fatalf("inner Run called %d times, want 0", n)
	}
	_, err := g.guard.Query(context.Background())
	if !errors.Is(err, ErrShellUnavailable) || !strings.Contains(err.Error(), "waiting for the GNOME Shell version") {
		t.Fatalf("Query() while pending = %v, want wrapped ErrShellUnavailable", err)
	}
}

func TestGuardedIBusSourceProbeTimeoutSeesCancel(t *testing.T) {
	// A probe that only returns when its context ends must not keep Run alive
	// after the parent is cancelled.
	g := newGVTestGuard(t, []ShellInfo{{Err: ErrShellUnavailable}})
	g.guard.probe = func(ctx context.Context) ShellInfo {
		<-ctx.Done()
		return ShellInfo{Err: ctx.Err()}
	}
	_, cancel, done := gvTestRun(t, g.guard)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return when the probe context was cancelled")
	}
	if n := g.inner.runCount(); n != 0 {
		t.Fatalf("inner Run called %d times, want 0", n)
	}
}

func TestGuardedIBusSourceWithTracker(t *testing.T) {
	// Refused guard inside a real tracker: the tracker stays Unknown and a
	// resync falls through to the resyncer.
	g := newGVTestGuard(t, []ShellInfo{{Version: "43.9"}})
	tracker := New(Options{Layouts: usRu})
	tracker.AddSource(g.guard)
	resync := &fakeResyncer{layout: Layout{Name: "ru"}}
	tracker.AddResync(resync)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tracker.Start(ctx)
	t.Cleanup(tracker.Stop)
	waitUntil(t, func() bool { return g.guard.State() == GuardRefused })
	if st := tracker.State(); st.Confidence != Unknown || st.Generation != 0 {
		t.Fatalf("tracker state after refusal = %s, want untouched Unknown", st)
	}
	if err := tracker.Resync(ctx); err != nil {
		t.Fatalf("Resync() = %v", err)
	}
	if st := tracker.State(); st.Confidence != Settings || st.Index != 1 || st.Source != SourceGSettings {
		t.Fatalf("state after resync = %s, want settings/index 1 from the resyncer", st)
	}
	if resync.callCount() != 1 {
		t.Fatalf("resyncer called %d times, want 1", resync.callCount())
	}
}
