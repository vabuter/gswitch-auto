package layoutstate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arumata/gswitch/internal/gswitch/detect"
)

// x11LayoutTestConn is a scripted x11Conn: GetState returns the current group
// (or blocks until ctx is done after blockAfter calls, like a reply the server
// never sends) and RulesNames returns the scripted property. pending is the
// number of queued events DrainEvents discards; with stallOnPending GetState
// blocks while any are queued, like xgb's reader stuck behind a full queue.
// onState, when set, runs after every GetState that answered.
type x11LayoutTestConn struct {
	mu             sync.Mutex
	group          uint8
	stateErr       error
	blockAfter     int // GetState blocks once stateCalls exceeds this; 0 = never
	rules          []byte
	rulesFound     bool
	rulesErr       error
	queryErr       error
	useErr         error
	pending        int
	stallOnPending bool
	onState        func()
	stateCalls     int
	rulesCalls     int
	drainCalls     int
	first          string // firstDrained
	closed         bool
}

var _ x11DrainReporter = (*x11LayoutTestConn)(nil)

// newX11LayoutTestConn scripts a connection whose _XKB_RULES_NAMES has the
// given layouts and variants fields.
func newX11LayoutTestConn(group uint8, layouts, variants string) *x11LayoutTestConn {
	return &x11LayoutTestConn{
		group:      group,
		rules:      []byte("evdev\x00pc105\x00" + layouts + "\x00" + variants + "\x00\x00"),
		rulesFound: true,
	}
}

func (c *x11LayoutTestConn) QueryExtension(context.Context) (byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.queryErr != nil {
		return 0, c.queryErr
	}
	return 135, nil
}

func (c *x11LayoutTestConn) UseExtension(context.Context) (uint16, uint16, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.useErr != nil {
		return 0, 0, c.useErr
	}
	return 1, 0, nil
}

func (c *x11LayoutTestConn) GetState(ctx context.Context) (uint8, error) {
	c.mu.Lock()
	c.stateCalls++
	block := (c.blockAfter > 0 && c.stateCalls > c.blockAfter) || (c.stallOnPending && c.pending > 0)
	group, err, closed, onState := c.group, c.stateErr, c.closed, c.onState
	c.mu.Unlock()
	if closed {
		return 0, io.EOF
	}
	if block {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	if onState != nil {
		onState()
	}
	return group, err
}

func (c *x11LayoutTestConn) RulesNames(context.Context) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rulesCalls++
	if c.closed {
		return nil, false, io.EOF
	}
	return c.rules, c.rulesFound, c.rulesErr
}

func (c *x11LayoutTestConn) DrainEvents() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.drainCalls++
	n := c.pending
	c.pending = 0
	if n > 0 && c.first == "" {
		c.first = fmt.Sprintf("event MappingNotify #%d", c.drainCalls)
	}
	return n
}

func (c *x11LayoutTestConn) firstDrained() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.first
}

func (c *x11LayoutTestConn) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
}

// queue adds n events to the scripted event queue.
func (c *x11LayoutTestConn) queue(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending += n
}

// drains returns the DrainEvents and GetState call counts read together.
func (c *x11LayoutTestConn) drains() (drain, state int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.drainCalls, c.stateCalls
}

func (c *x11LayoutTestConn) setGroup(group uint8) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.group = group
}

func (c *x11LayoutTestConn) setRules(layouts, variants string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rules = []byte("evdev\x00pc105\x00" + layouts + "\x00" + variants + "\x00\x00")
	c.rulesFound = true
}

func (c *x11LayoutTestConn) calls() (state, rules int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stateCalls, c.rulesCalls
}

func (c *x11LayoutTestConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// x11LayoutTestLog collects Logf output.
type x11LayoutTestLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *x11LayoutTestLog) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, strings.TrimSpace(fmt.Sprintf(format, args...)))
}

func (l *x11LayoutTestLog) count(substr string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, line := range l.lines {
		if strings.Contains(line, substr) {
			n++
		}
	}
	return n
}

func (l *x11LayoutTestLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.lines)
}

// x11LayoutTestSource builds a fast source (20 ms polls, 40 ms reply timeout)
// whose dial hands out conns in order (the last one repeatedly) and whose
// backoff sleep returns immediately.
func x11LayoutTestSource(log *x11LayoutTestLog, conns ...x11Conn) (*X11Source, *atomic.Int32) {
	src := NewX11Source(X11Options{
		Display:      ":9",
		PollInterval: 20 * time.Millisecond,
		ReplyTimeout: 40 * time.Millisecond,
		Fallback:     usRu,
		Logf:         log.logf,
	})
	dials := &atomic.Int32{}
	src.dial = func(context.Context, string) (x11Conn, error) {
		n := int(dials.Add(1)) - 1
		if n >= len(conns) {
			n = len(conns) - 1
		}
		return conns[n], nil
	}
	src.sleep = func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil }
	return src, dials
}

// x11LayoutTestWaitCalls waits until conn has seen at least n GetState calls.
func x11LayoutTestWaitCalls(t *testing.T, conn *x11LayoutTestConn, n int) {
	t.Helper()
	waitUntil(t, func() bool { s, _ := conn.calls(); return s >= n })
}

// x11LayoutTestNoEngine fails when an Engine event is pending on the sink.
func x11LayoutTestNoEngine(t *testing.T, sink *recordingSink) {
	t.Helper()
	for {
		select {
		case ev := <-sink.ch:
			if ev.kind == "engine" {
				t.Fatalf("unexpected sink event %+v", ev)
			}
		default:
			return
		}
	}
}

func TestX11SourceReportsInitialGroupAndChanges(t *testing.T) {
	conn := newX11LayoutTestConn(0, "us,ru", ",")
	log := &x11LayoutTestLog{}
	src, _ := x11LayoutTestSource(log, conn)
	sink := newRecordingSink()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); src.Run(ctx, sink) }()

	if ev := sink.next(t); ev != (sinkEvent{kind: "available", ok: true}) {
		t.Fatalf("first event = %+v, want Available(true)", ev)
	}
	if ev := sink.next(t); ev != (sinkEvent{kind: "engine", engine: "xkb:us::"}) {
		t.Fatalf("second event = %+v, want Engine(xkb:us::)", ev)
	}
	conn.setGroup(1)
	sink.waitFor(t, sinkEvent{kind: "engine", engine: "xkb:ru::"})
	conn.setGroup(0)
	sink.waitFor(t, sinkEvent{kind: "engine", engine: "xkb:us::"})

	cancel()
	<-done
	if !conn.isClosed() {
		t.Error("connection not closed after Run returned")
	}
	if got := log.count("connected to display :9"); got != 1 {
		t.Errorf("connected log lines = %d, want 1: %q", got, log.all())
	}
	if src.Name() != SourceX11 {
		t.Errorf("Name() = %q, want %q", src.Name(), SourceX11)
	}
}

func TestX11SourceIdenticalPollsAreSilent(t *testing.T) {
	conn := newX11LayoutTestConn(1, "us,ru", ",")
	src, _ := x11LayoutTestSource(&x11LayoutTestLog{}, conn)
	sink := newRecordingSink()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go src.Run(ctx, sink)

	sink.waitFor(t, sinkEvent{kind: "engine", engine: "xkb:ru::"})
	x11LayoutTestWaitCalls(t, conn, 8)
	x11LayoutTestNoEngine(t, sink)
}

// TestX11SourceKeepsPendingAcrossIdenticalPolls checks, through a real
// Tracker, that a switch hint is not cancelled by polls that report the same
// group, and that a real group change confirms it.
func TestX11SourceKeepsPendingAcrossIdenticalPolls(t *testing.T) {
	conn := newX11LayoutTestConn(0, "us,ru", ",")
	src, _ := x11LayoutTestSource(&x11LayoutTestLog{}, conn)
	tr, _ := newTestTracker(usRu)
	tr.AddSource(src)
	tr.Start(context.Background())
	defer tr.Stop()

	waitUntil(t, func() bool { st := tr.State(); return st.Confidence == Confirmed && st.Index == 0 })
	base := tr.State().Generation
	tr.NoteSwitchHint()
	st := tr.State()
	if st.Confidence != Pending || st.Generation != base+1 {
		t.Fatalf("after hint: %s, want pending generation %d", st, base+1)
	}
	calls, _ := conn.calls()
	x11LayoutTestWaitCalls(t, conn, calls+6)
	if st := tr.State(); st.Confidence != Pending || st.Generation != base+1 || st.Index != 0 {
		t.Fatalf("after identical polls: %s, want pending index 0 generation %d", st, base+1)
	}
	conn.setGroup(1)
	waitUntil(t, func() bool { st := tr.State(); return st.Confidence == Confirmed && st.Index == 1 })
	if st := tr.State(); st.Generation != base+2 || st.Source != SourceX11 || st.Engine != "xkb:ru::" {
		t.Fatalf("after switch: %s, want confirmed index 1 generation %d source x11", st, base+2)
	}
}

func TestX11SourceRereadsTableOnOutOfRangeGroup(t *testing.T) {
	conn := newX11LayoutTestConn(0, "us,ru", ",")
	src, _ := x11LayoutTestSource(&x11LayoutTestLog{}, conn)
	sink := newRecordingSink()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go src.Run(ctx, sink)

	sink.waitFor(t, sinkEvent{kind: "engine", engine: "xkb:us::"})
	_, rulesBefore := conn.calls()
	conn.setGroup(2)
	sink.waitFor(t, sinkEvent{kind: "engine", engine: ""})
	waitUntil(t, func() bool { _, r := conn.calls(); return r > rulesBefore })
	conn.setRules("us,ru,de", ",,nodeadkeys")
	sink.waitFor(t, sinkEvent{kind: "engine", engine: "xkb:de:nodeadkeys:"})
}

func TestX11SourceFallbackWhenRulesMissing(t *testing.T) {
	conn := newX11LayoutTestConn(0, "", "")
	conn.rulesFound = false
	log := &x11LayoutTestLog{}
	src, _ := x11LayoutTestSource(log, conn)
	sink := newRecordingSink()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go src.Run(ctx, sink)

	sink.waitFor(t, sinkEvent{kind: "engine", engine: "xkb:us::"})
	conn.setGroup(1)
	sink.waitFor(t, sinkEvent{kind: "engine", engine: "xkb:ru::"})
	// An out-of-range group forces a re-read; the warning is not repeated.
	conn.setGroup(3)
	sink.waitFor(t, sinkEvent{kind: "engine", engine: ""})
	x11LayoutTestWaitCalls(t, conn, 12)
	if got := log.count("Warning: X11 layout source: _XKB_RULES_NAMES on display :9 is missing"); got != 1 {
		t.Fatalf("missing-property warnings = %d, want 1: %q", got, log.all())
	}
}

func TestX11SourceFallbackWhenRulesHaveOneLayout(t *testing.T) {
	conn := newX11LayoutTestConn(0, "us", "")
	log := &x11LayoutTestLog{}
	src, _ := x11LayoutTestSource(log, conn)
	sink := newRecordingSink()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go src.Run(ctx, sink)

	sink.waitFor(t, sinkEvent{kind: "engine", engine: "xkb:us::"})
	conn.setGroup(1)
	sink.waitFor(t, sinkEvent{kind: "engine", engine: "xkb:ru::"})
	if got := log.count(`has fewer than two layouts ("us")`); got != 1 {
		t.Fatalf("one-layout warnings = %d, want 1: %q", got, log.all())
	}
}

// TestX11SourceWarnsAboutReversedPair compares the first two table entries
// with the configured pair (us,ru): GNOME appends the locale's layout as a
// third entry, so the check must not depend on the table length.
func TestX11SourceWarnsAboutReversedPair(t *testing.T) {
	tests := []struct {
		name, layouts, variants string
		engine                  string // reported for group 0
		warning                 string // "" = no warning
	}{
		{"reversed two layouts", "ru,us", ",", "xkb:ru::", "Warning: X11 layout table ru,us differs from the configured pair us,ru"},
		{"reversed with gnome locale layout", "ru,us,us", ",,", "xkb:ru::", "Warning: X11 layout table ru,us,us differs from the configured pair us,ru"},
		{"same order", "us,ru", ",", "xkb:us::", ""},
		{"same order with gnome locale layout", "us,ru,us", ",,", "xkb:us::", ""},
		{"same order with ru locale layout", "us,ru,ru", ",,", "xkb:us::", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			conn := newX11LayoutTestConn(0, tt.layouts, tt.variants)
			log := &x11LayoutTestLog{}
			src, _ := x11LayoutTestSource(log, conn)
			sink := newRecordingSink()
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); src.Run(ctx, sink) }()

			sink.waitFor(t, sinkEvent{kind: "engine", engine: tt.engine})
			// The check runs at connect only: later table re-reads add nothing.
			x11LayoutTestWaitCalls(t, conn, x11TableRefreshPolls+2)
			cancel()
			<-done
			want := 0
			if tt.warning != "" {
				want = 1
			}
			if got := log.count("differs from the configured pair"); got != want {
				t.Fatalf("reversed-pair warnings = %d, want %d: %q", got, want, log.all())
			}
			if tt.warning != "" && log.count(tt.warning) != 1 {
				t.Fatalf("log lacks %q: %q", tt.warning, log.all())
			}
		})
	}
}

func TestX11LayoutReversedPair(t *testing.T) {
	l := func(names ...string) []Layout {
		table := make([]Layout, len(names))
		for i, name := range names {
			table[i].Name = name
		}
		return table
	}
	tests := []struct {
		table []Layout
		want  bool
	}{
		{l("ru", "us"), true},
		{l("ru", "us", "us"), true},
		{l("ru", "us", "ru"), true},
		{l("us", "ru"), false},
		{l("us", "ru", "us"), false},
		{l("us", "ru", "ru"), false},
		{l("us", "us", "ru"), false},
		{l("ru"), false},
		{nil, false},
		{[]Layout{{Name: "ru", Variant: "phonetic"}, {Name: "us", Variant: "dvorak"}}, true},
	}
	for _, tt := range tests {
		if got := reversedPair(tt.table, usRu); got != tt.want {
			t.Errorf("reversedPair(%s, us,ru) = %v, want %v", formatLayouts(tt.table), got, tt.want)
		}
	}
	if reversedPair(l("us", "us"), [2]Layout{{Name: "us"}, {Name: "us"}}) {
		t.Error("reversedPair(us,us, us,us) = true, want false")
	}
}

// TestX11SourceDrainsEventsEveryPoll covers the event-queue drain: every poll
// calls DrainEvents before its request, so events queued while the source
// sleeps between polls (a full queue stalls xgb's reader and with it the
// reply) are discarded first and replies keep flowing on the same connection;
// the first non-empty drain is logged once per connection.
func TestX11SourceDrainsEventsEveryPoll(t *testing.T) {
	conn := newX11LayoutTestConn(0, "us,ru", ",")
	conn.stallOnPending = true
	answered := make(chan struct{}, 1)
	conn.onState = func() {
		select {
		case answered <- struct{}{}:
		default:
		}
	}
	next := newX11LayoutTestConn(1, "us,ru", ",")
	log := &x11LayoutTestLog{}
	src, dials := x11LayoutTestSource(log, conn, next)
	src.pollInterval = 100 * time.Millisecond // a wide gap between polls to queue events in
	sink := newRecordingSink()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go src.Run(ctx, sink)

	sink.waitFor(t, sinkEvent{kind: "engine", engine: "xkb:us::"})
	x11LayoutTestWaitCalls(t, conn, 4)
	drain, state := conn.drains()
	// The connect-time read is not a poll; a poll may sit between its drain and
	// its GetState while the counters are read.
	if drain != state-1 && drain != state {
		t.Fatalf("DrainEvents calls = %d for %d GetState calls, want one per poll", drain, state)
	}
	if got := log.count("discarded"); got != 0 {
		t.Fatalf("empty drains logged %q", log.all())
	}

	// A full queue arrives between two polls: the next poll drains it before
	// its request, so that request is answered on the same connection.
	x11LayoutTestQueueBetweenPolls(t, conn, answered, 5000)
	x11LayoutTestAwaitAnswer(t, answered)
	conn.setGroup(1)
	sink.waitFor(t, sinkEvent{kind: "engine", engine: "xkb:ru::"})
	x11LayoutTestQueueBetweenPolls(t, conn, answered, 3)
	x11LayoutTestAwaitAnswer(t, answered)
	conn.setGroup(0)
	sink.waitFor(t, sinkEvent{kind: "engine", engine: "xkb:us::"})
	if dials.Load() != 1 || conn.isClosed() {
		t.Fatalf("dials = %d, closed = %v; want the first connection kept", dials.Load(), conn.isClosed())
	}
	const wantLine = "X11 layout source: discarded 5000 queued X event(s)/error(s) on display :9, the first: event MappingNotify #"
	if got := log.count(wantLine); got != 1 {
		t.Fatalf("drain log lines with %q = %d, want 1: %q", wantLine, got, log.all())
	}
	if got := log.count("discarded"); got != 1 {
		t.Fatalf("drain log lines = %d, want 1 per connection: %q", got, log.all())
	}

	// A new connection logs its first non-empty drain again.
	next.queue(2)
	conn.mu.Lock()
	conn.stateErr = io.EOF
	conn.mu.Unlock()
	sink.waitFor(t, sinkEvent{kind: "available", ok: false})
	sink.waitFor(t, sinkEvent{kind: "engine", engine: "xkb:ru::"})
	waitUntil(t, func() bool { return log.count("discarded 2 queued") == 1 })
	if got := log.count("discarded"); got != 2 {
		t.Fatalf("drain log lines = %d, want 2 (one per connection): %q", got, log.all())
	}
}

// x11LayoutTestQueueBetweenPolls queues n events on conn while its source
// sleeps between two polls: 20 ms after a fresh GetState answer, well before
// the next tick of a 100 ms poll interval.
func x11LayoutTestQueueBetweenPolls(t *testing.T, conn *x11LayoutTestConn, answered <-chan struct{}, n int) {
	t.Helper()
	select {
	case <-answered: // drop a stale signal
	default:
	}
	x11LayoutTestAwaitAnswer(t, answered)
	time.Sleep(20 * time.Millisecond)
	conn.queue(n)
}

// x11LayoutTestAwaitAnswer waits for the next answered GetState on the
// connection that signals answered.
func x11LayoutTestAwaitAnswer(t *testing.T, answered <-chan struct{}) {
	t.Helper()
	select {
	case <-answered:
	case <-time.After(2 * time.Second):
		t.Fatal("no GetState answer within 2s: the poll after the queued events stalled")
	}
}

func TestX11SourceDialFailureBackoff(t *testing.T) {
	log := &x11LayoutTestLog{}
	src := NewX11Source(X11Options{Display: ":9", Logf: log.logf})
	src.dial = func(context.Context, string) (x11Conn, error) {
		return nil, errors.New("no such display")
	}
	var sleeps []time.Duration
	src.sleep = func(_ context.Context, d time.Duration) bool {
		sleeps = append(sleeps, d)
		return len(sleeps) < 8
	}
	sink := newRecordingSink()
	src.Run(context.Background(), sink)

	ms := time.Millisecond
	want := []time.Duration{200 * ms, 400 * ms, 800 * ms, 1600 * ms, 3200 * ms, 5000 * ms, 5000 * ms, 5000 * ms}
	if len(sleeps) != len(want) {
		t.Fatalf("sleeps = %v, want %v", sleeps, want)
	}
	for i := range want {
		if sleeps[i] != want[i] {
			t.Fatalf("sleeps = %v, want %v", sleeps, want)
		}
	}
	for range want {
		if ev := <-sink.ch; ev != (sinkEvent{kind: "available", ok: false}) {
			t.Fatalf("sink event = %+v, want Available(false)", ev)
		}
	}
	if got := log.count("connect to X display :9: no such display (retrying)"); got != 1 {
		t.Fatalf("retry log lines = %d, want 1 (same error logged once): %q", got, log.all())
	}
}

func TestX11SourceExtensionErrorsReconnect(t *testing.T) {
	broken := newX11LayoutTestConn(0, "us,ru", ",")
	broken.queryErr = errXKBUnavailable
	good := newX11LayoutTestConn(1, "us,ru", ",")
	log := &x11LayoutTestLog{}
	src, dials := x11LayoutTestSource(log, broken, good)
	sink := newRecordingSink()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go src.Run(ctx, sink)

	sink.waitFor(t, sinkEvent{kind: "engine", engine: "xkb:ru::"})
	if !broken.isClosed() || dials.Load() != 2 {
		t.Fatalf("broken closed = %v, dials = %d; want closed after 2 dials", broken.isClosed(), dials.Load())
	}
	if got := log.count("XKEYBOARD extension is unavailable (retrying)"); got != 1 {
		t.Fatalf("extension error log lines = %d, want 1: %q", got, log.all())
	}
}

// TestX11SourceReplyTimeoutReconnects covers a GetState reply that never
// arrives: within ReplyTimeout the source reports unavailable, closes the
// connection, reconnects and reports the engine again.
func TestX11SourceReplyTimeoutReconnects(t *testing.T) {
	hung := newX11LayoutTestConn(0, "us,ru", ",")
	hung.blockAfter = 1 // the connect-time read succeeds, the first poll hangs
	fresh := newX11LayoutTestConn(1, "us,ru", ",")
	log := &x11LayoutTestLog{}
	src, dials := x11LayoutTestSource(log, hung, fresh)
	sink := newRecordingSink()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go src.Run(ctx, sink)

	sink.waitFor(t, sinkEvent{kind: "engine", engine: "xkb:us::"})
	start := time.Now()
	sink.waitFor(t, sinkEvent{kind: "available", ok: false})
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("Available(false) after %v, want within the reply timeout", elapsed)
	}
	waitUntil(t, hung.isClosed)
	sink.waitFor(t, sinkEvent{kind: "engine", engine: "xkb:ru::"})
	if dials.Load() != 2 {
		t.Fatalf("dials = %d, want 2", dials.Load())
	}
	if got := log.count("no reply within 40ms"); got != 1 {
		t.Fatalf("timeout log lines = %d, want 1: %q", got, log.all())
	}
}

func TestX11SourceQueryTemporaryConnection(t *testing.T) {
	conn := newX11LayoutTestConn(1, "us,ru", ",")
	log := &x11LayoutTestLog{}
	src, dials := x11LayoutTestSource(log, conn)
	engine, err := src.Query(context.Background())
	if err != nil || engine != "xkb:ru::" {
		t.Fatalf("Query() = %q, %v; want xkb:ru::, nil", engine, err)
	}
	if dials.Load() != 1 || !conn.isClosed() {
		t.Fatalf("dials = %d, closed = %v; want 1 dial and a closed connection", dials.Load(), conn.isClosed())
	}
	if len(log.all()) != 0 {
		t.Fatalf("Query logged %q, want nothing", log.all())
	}
	// A dial failure is returned.
	src.dial = func(context.Context, string) (x11Conn, error) { return nil, errors.New("refused") }
	if _, err := src.Query(context.Background()); err == nil || !strings.Contains(err.Error(), "connect to X display :9: refused") {
		t.Fatalf("Query() error = %v, want connect error", err)
	}
}

func TestX11SourceQueryLiveConnection(t *testing.T) {
	conn := newX11LayoutTestConn(0, "us,ru", ",")
	src, dials := x11LayoutTestSource(&x11LayoutTestLog{}, conn)
	sink := newRecordingSink()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); src.Run(ctx, sink) }()
	sink.waitFor(t, sinkEvent{kind: "engine", engine: "xkb:us::"})

	conn.setGroup(1)
	engine, err := src.Query(context.Background())
	if err != nil || engine != "xkb:ru::" {
		t.Fatalf("Query() = %q, %v; want xkb:ru::, nil", engine, err)
	}
	if dials.Load() != 1 {
		t.Fatalf("dials = %d, want 1 (live connection reused)", dials.Load())
	}
	cancel()
	<-done
	// After Run stopped, Query opens a temporary connection again.
	conn2 := newX11LayoutTestConn(0, "us,ru", ",")
	src.dial = func(context.Context, string) (x11Conn, error) { return conn2, nil }
	engine, err = src.Query(context.Background())
	if err != nil || engine != "xkb:us::" || !conn2.isClosed() {
		t.Fatalf("Query() after stop = %q, %v, closed %v; want xkb:us::, nil, true", engine, err, conn2.isClosed())
	}
}

func TestX11SourceOptionsDefaults(t *testing.T) {
	src := NewX11Source(X11Options{})
	if src.pollInterval != 50*time.Millisecond || src.replyTimeout != time.Second {
		t.Fatalf("defaults = %v/%v, want 50ms/1s", src.pollInterval, src.replyTimeout)
	}
	src = NewX11Source(X11Options{PollInterval: time.Millisecond})
	if src.pollInterval != 20*time.Millisecond {
		t.Fatalf("poll interval clamp low = %v, want 20ms", src.pollInterval)
	}
	src = NewX11Source(X11Options{PollInterval: 5 * time.Second})
	if src.pollInterval != 500*time.Millisecond {
		t.Fatalf("poll interval clamp high = %v, want 500ms", src.pollInterval)
	}
}

func TestX11SourceEnabled(t *testing.T) {
	gnomeX11 := &detect.SessionEnv{SessionType: "x11", Display: ":0", XDGCurrentDesktop: "GNOME"}
	gnomeX11NoDisplay := &detect.SessionEnv{SessionType: "x11", XDGCurrentDesktop: "GNOME"}
	kdeX11 := &detect.SessionEnv{SessionType: "x11", Display: ":0", XDGCurrentDesktop: "KDE"}
	gnomeWayland := &detect.SessionEnv{SessionType: "wayland", Display: ":0", WaylandDisplay: "wayland-0", XDGCurrentDesktop: "GNOME"}
	xwayland := &detect.SessionEnv{Display: ":0", WaylandDisplay: "wayland-0", XDGCurrentDesktop: "GNOME"}
	unknown := &detect.SessionEnv{XDGCurrentDesktop: "GNOME"}
	tests := []struct {
		name   string
		source string
		env    *detect.SessionEnv
		euid   int
		want   bool
	}{
		{"auto gnome x11", "auto", gnomeX11, 1000, true},
		{"auto gnome x11 by session type only", "auto", gnomeX11NoDisplay, 1000, true},
		{"auto kde x11", "auto", kdeX11, 1000, false},
		{"auto gnome wayland", "auto", gnomeWayland, 1000, false},
		{"auto xwayland", "auto", xwayland, 1000, false},
		{"auto unknown session", "auto", unknown, 1000, false},
		{"auto root", "auto", gnomeX11, 0, false},
		{"x11 kde", "x11", kdeX11, 1000, true},
		{"x11 gnome", "x11", gnomeX11, 1000, true},
		{"x11 without display", "x11", gnomeX11NoDisplay, 1000, false},
		{"x11 wayland", "x11", gnomeWayland, 1000, false},
		{"x11 xwayland", "x11", xwayland, 1000, false},
		{"x11 root", "x11", kdeX11, 0, false},
		{"ibus", "ibus", gnomeX11, 1000, false},
		{"none", "none", gnomeX11, 1000, false},
		{"empty", "", gnomeX11, 1000, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := X11SourceEnabled(tt.source, tt.env, tt.euid); got != tt.want {
				t.Errorf("X11SourceEnabled(%q, %+v, %d) = %v, want %v", tt.source, tt.env, tt.euid, got, tt.want)
			}
		})
	}
}

func TestX11SourceEnabledProcessEnvironment(t *testing.T) {
	setEnv := func(t *testing.T, sessionType, display, wayland, desktop string) {
		t.Helper()
		t.Setenv("XDG_SESSION_TYPE", sessionType)
		t.Setenv("DISPLAY", display)
		t.Setenv("WAYLAND_DISPLAY", wayland)
		t.Setenv("XDG_CURRENT_DESKTOP", desktop)
		t.Setenv("XDG_SESSION_DESKTOP", "")
		t.Setenv("DESKTOP_SESSION", "")
	}
	tests := []struct {
		name                                   string
		sessionType, display, wayland, desktop string
		source                                 string
		want                                   bool
	}{
		{"auto gnome x11", "x11", ":0", "", "GNOME", "auto", true},
		{"auto gnome x11 without DISPLAY", "x11", "", "", "GNOME", "auto", true},
		{"auto gnome display but no session type", "", ":0", "", "GNOME", "auto", false},
		{"auto kde x11", "x11", ":0", "", "KDE", "auto", false},
		{"auto gnome wayland", "wayland", ":0", "wayland-0", "GNOME", "auto", false},
		{"x11 kde with display", "x11", ":1", "", "KDE", "x11", true},
		{"x11 no session type but display", "", ":1", "", "KDE", "x11", true},
		{"x11 without display", "x11", "", "", "KDE", "x11", false},
		{"x11 wayland", "", ":0", "wayland-0", "GNOME", "x11", false},
		{"x11 session type wayland", "wayland", ":0", "", "GNOME", "x11", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnv(t, tt.sessionType, tt.display, tt.wayland, tt.desktop)
			if got := X11SourceEnabled(tt.source, nil, 1000); got != tt.want {
				t.Errorf("X11SourceEnabled(%q, nil, 1000) = %v, want %v", tt.source, got, tt.want)
			}
		})
	}
}

func TestRunX11DiagnosticsFake(t *testing.T) {
	conn := newX11LayoutTestConn(1, "us,ru", ",phonetic")
	prev := x11Dial
	x11Dial = func(context.Context, string) (x11Conn, error) { return conn, nil }
	defer func() { x11Dial = prev }()

	var out bytes.Buffer
	if err := RunX11Diagnostics(context.Background(), &out, ":9", usRu, 0); err != nil {
		t.Fatalf("RunX11Diagnostics() error = %v\n%s", err, out.String())
	}
	for _, want := range []string{
		"Layout pair: us / ru\n",
		"Display: :9\n",
		"XKB extension: opcode 135, version 1.0\n",
		`_XKB_RULES_NAMES: rules "evdev", model "pc105", layouts "us,ru", variants ",phonetic", options ""` + "\n",
		"Layout table: us,ru(phonetic)\n",
		"XKB group: 1\n",
		`Engine: "xkb:ru:phonetic:"` + "\n",
		"Mapped: layout ru(phonetic) = index 1 (confidence confirmed)\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if !conn.isClosed() {
		t.Error("diagnostics connection not closed")
	}

	// Missing property: the configured pair is used and reported as such.
	missing := newX11LayoutTestConn(0, "", "")
	missing.rulesFound = false
	x11Dial = func(context.Context, string) (x11Conn, error) { return missing, nil }
	out.Reset()
	if err := RunX11Diagnostics(context.Background(), &out, "", usRu, 0); err != nil {
		t.Fatalf("RunX11Diagnostics(missing) error = %v\n%s", err, out.String())
	}
	for _, want := range []string{"_XKB_RULES_NAMES: missing\n", "Layout table: us,ru (configured pair; _XKB_RULES_NAMES is unusable)\n", "XKB group: 0\n"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}

	// Dial failure is printed and returned.
	x11Dial = func(context.Context, string) (x11Conn, error) { return nil, errors.New("refused") }
	out.Reset()
	if err := RunX11Diagnostics(context.Background(), &out, ":9", usRu, 0); err == nil {
		t.Fatalf("RunX11Diagnostics(refused) error = nil\n%s", out.String())
	}
	if !strings.Contains(out.String(), "X11: error: connect to X display :9: refused\n") {
		t.Errorf("output lacks the connect error:\n%s", out.String())
	}
}

func TestRunX11DiagnosticsWatch(t *testing.T) {
	// The one-shot read closes its connection; the watch dials a second one.
	conns := []*x11LayoutTestConn{newX11LayoutTestConn(0, "us,ru", ","), newX11LayoutTestConn(0, "us,ru", ",")}
	var dials atomic.Int32
	prev := x11Dial
	x11Dial = func(context.Context, string) (x11Conn, error) {
		return conns[min(int(dials.Add(1))-1, len(conns)-1)], nil
	}
	defer func() { x11Dial = prev }()

	var out bytes.Buffer
	go func() {
		watched := conns[1]
		for s, _ := watched.calls(); s < 3; s, _ = watched.calls() {
			time.Sleep(5 * time.Millisecond)
		}
		watched.setGroup(1)
	}()
	if err := RunX11Diagnostics(context.Background(), &out, ":9", usRu, 400*time.Millisecond); err != nil {
		t.Fatalf("RunX11Diagnostics() error = %v\n%s", err, out.String())
	}
	for _, want := range []string{
		"Watching layout changes for 400ms...\n",
		`index=0 engine="xkb:us::" confidence=confirmed generation=1 source="x11"`,
		`index=1 engine="xkb:ru::" confidence=confirmed generation=2 source="x11"`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}
