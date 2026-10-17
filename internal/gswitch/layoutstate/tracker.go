// Package layoutstate tracks the active keyboard layout of the graphical
// session. The authoritative source is the IBus global engine
// (GlobalEngineChanged on the private IBus bus); GNOME's mru-sources setting is
// only a lower-confidence resync that never reports Confirmed.
package layoutstate

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// Confidence describes how much the tracked layout can be trusted.
type Confidence uint8

const (
	// Unknown means there is no usable source, IBus is down, the engine is not
	// an XKB engine of the configured pair, or the pair is ambiguous.
	Unknown Confidence = iota
	// Pending means a physical layout-switch chord was seen and the switch has
	// not been confirmed yet; Index is the last known value and may be stale.
	Pending
	// Settings means the layout was read from gsettings only. It never enables
	// automatic correction.
	Settings
	// Confirmed means the layout comes from an IBus signal or property read.
	Confirmed
)

// String returns the lower-case name of the confidence level.
func (c Confidence) String() string {
	switch c {
	case Unknown:
		return "unknown"
	case Pending:
		return "pending"
	case Settings:
		return "settings"
	case Confirmed:
		return "confirmed"
	default:
		return fmt.Sprintf("confidence(%d)", uint8(c))
	}
}

// Layout is an XKB layout name with an optional variant.
type Layout struct{ Name, Variant string }

// String formats the layout as "name" or "name(variant)".
func (l Layout) String() string {
	if l.Variant == "" {
		return l.Name
	}
	return l.Name + "(" + l.Variant + ")"
}

// State is a snapshot of the tracked layout.
type State struct {
	Index      int    // 0 = Layout1, 1 = Layout2, -1 = not one of the pair
	Engine     string // e.g. "xkb:ru::rus"; "" when none
	Confidence Confidence
	Generation uint64 // increments on every state change
	At         time.Time
	Source     string // "ibus" | "gsettings" | ""
}

// String formats the state for logs and diagnostics.
func (s State) String() string {
	return fmt.Sprintf("index=%d engine=%q confidence=%s generation=%d source=%q",
		s.Index, s.Engine, s.Confidence, s.Generation, s.Source)
}

// Source names reported in State.Source.
const (
	SourceIBus      = "ibus"
	SourceGSettings = "gsettings"
)

// DefaultHintTimeout is the Pending window used when Options.HintTimeout is not
// positive (auto.conf layout-hint-timeout-ms default).
const DefaultHintTimeout = 400 * time.Millisecond

// queryTimeout bounds every Source.Query issued by Resync.
const queryTimeout = 500 * time.Millisecond

// Options configures a Tracker.
type Options struct {
	Layouts     [2]Layout
	HintTimeout time.Duration // layout-hint-timeout-ms, default 400ms
	Logger      func(format string, args ...any)
	DebugLogger func(format string, args ...any)
	Now         func() time.Time
}

// Source is a push source of the active engine (IBus).
type Source interface {
	Name() string
	// Run blocks until ctx is done and reconnects internally. It reports the
	// current engine through sink.Engine and connection state through
	// sink.Available.
	Run(ctx context.Context, sink Sink)
	// Query reads the current engine once.
	Query(ctx context.Context) (engine string, err error)
}

// Sink receives engine names and availability from a Source. *Tracker
// implements Sink.
type Sink interface {
	Engine(source, name string)
	Available(source string, ok bool)
}

// Resyncer is a lower-confidence pull source consulted only when every
// Source.Query fails.
type Resyncer interface {
	Current(ctx context.Context) (Layout, error)
}

// Tracker maintains the layout State. All methods are safe for concurrent use.
type Tracker struct {
	opts Options
	// afterFunc schedules the hint timeout; replaced by tests.
	afterFunc func(d time.Duration, f func()) (stop func() bool)

	mu        sync.Mutex
	state     State
	sources   []Source
	resyncers []Resyncer
	callbacks []func(State)
	hintStop  func() bool
	hintSeq   uint64
	ctx       context.Context
	cancel    context.CancelFunc
	started   bool
	stopped   bool
	wg        sync.WaitGroup
	notify    chan struct{}
}

var _ Sink = (*Tracker)(nil)

// New creates a tracker in the Unknown state. It does not start any goroutine.
func New(opts Options) *Tracker {
	if opts.HintTimeout <= 0 {
		opts.HintTimeout = DefaultHintTimeout
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	t := &Tracker{
		opts: opts,
		afterFunc: func(d time.Duration, f func()) func() bool {
			return time.AfterFunc(d, f).Stop
		},
		notify: make(chan struct{}, 1),
	}
	t.state = State{Index: -1, Confidence: Unknown, At: opts.Now()}
	if opts.Layouts[0] == opts.Layouts[1] {
		t.logf("Warning: layout tracker: identical layout pair %s/%s, layout stays unknown",
			opts.Layouts[0], opts.Layouts[1])
	}
	return t
}

// AddSource registers a push source. Sources added after Start are not run.
func (t *Tracker) AddSource(src Source) {
	if t == nil || src == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sources = append(t.sources, src)
}

// AddResync registers a lower-confidence resync source.
func (t *Tracker) AddResync(r Resyncer) {
	if t == nil || r == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.resyncers = append(t.resyncers, r)
}

// OnChange registers a callback invoked on the tracker goroutine with the
// latest state after every change. Callbacks must not block; a slow callback
// delays later notifications but never a Source. Intermediate states may be
// coalesced: the callback always receives the newest state. Notifications are
// delivered only while the tracker is started.
func (t *Tracker) OnChange(fn func(State)) {
	if t == nil || fn == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.callbacks = append(t.callbacks, fn)
}

// Start runs every registered source and the notification goroutine. It does
// not block. Start after Stop, or a second Start, is a no-op.
func (t *Tracker) Start(ctx context.Context) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.started || t.stopped {
		return
	}
	t.started = true
	t.ctx, t.cancel = context.WithCancel(ctx)
	t.wg.Add(1)
	go t.dispatch(t.ctx)
	for _, src := range t.sources {
		t.wg.Add(1)
		go func(src Source) {
			defer t.wg.Done()
			src.Run(t.ctx, t)
		}(src)
	}
}

// Stop cancels all sources, the hint timer and pending notifications and waits
// for the tracker goroutines to exit. It is idempotent.
func (t *Tracker) Stop() {
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.stopped {
		t.mu.Unlock()
		return
	}
	t.stopped = true
	t.cancelHintLocked()
	cancel := t.cancel
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	t.wg.Wait()
}

// State returns the current state. A nil tracker reports Unknown.
func (t *Tracker) State() State {
	if t == nil {
		return State{Index: -1}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

// Current returns the active pair index; known is true only when the state is
// Confirmed and the engine is one of the pair.
func (t *Tracker) Current() (idx int, known bool) {
	st := t.State()
	return st.Index, st.Confidence == Confirmed && st.Index >= 0
}

// Engine implements Sink: it applies an engine reported by a source. Unchanged
// consecutive states are deduplicated (no generation bump, no callback).
func (t *Tracker) Engine(source, name string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.cancelHintLocked()
	changed := t.setLocked(t.engineState(source, name))
	t.mu.Unlock()
	if changed {
		t.wake()
	}
}

// Available implements Sink. Losing a source makes the state Unknown;
// regaining it changes nothing until the source reports an engine.
func (t *Tracker) Available(source string, ok bool) {
	if t == nil || ok {
		return
	}
	t.mu.Lock()
	t.cancelHintLocked()
	changed := t.setLocked(State{Index: -1, Confidence: Unknown})
	t.mu.Unlock()
	if changed {
		t.debugf("layout tracker: source %s unavailable", source)
		t.wake()
	}
}

// NoteSwitchHint records that the physical layout-switch chord was pressed: a
// Confirmed or Settings state becomes Pending, and when no engine signal arrives
// within HintTimeout the tracker resyncs. A hint while Pending restarts the
// timeout; a hint while Unknown is ignored. A nil receiver is a no-op.
func (t *Tracker) NoteSwitchHint() {
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.stopped || t.state.Confidence == Unknown {
		t.mu.Unlock()
		return
	}
	next := t.state
	next.Confidence = Pending
	changed := t.setLocked(next)
	t.armHintLocked()
	t.mu.Unlock()
	if changed {
		t.wake()
	}
}

// Resync queries every Source in order (Source.Query, 500 ms each); the first
// success is applied like a signal. Only when every Query fails are the
// Resyncers consulted (Settings, or Unknown when the layout is not in the
// pair); when all of them fail the state becomes Unknown. A result is dropped
// when the state changed or a new hint arrived while Resync was running.
func (t *Tracker) Resync(ctx context.Context) error {
	if t == nil {
		return errors.New("layout tracker is nil")
	}
	t.mu.Lock()
	sources := slices.Clone(t.sources)
	resyncers := slices.Clone(t.resyncers)
	gen, seq := t.state.Generation, t.hintSeq
	t.mu.Unlock()

	var errs []error
	for _, src := range sources {
		qctx, cancel := context.WithTimeout(ctx, queryTimeout)
		engine, err := src.Query(qctx)
		cancel()
		if err == nil {
			t.applyResync(gen, seq, t.engineState(src.Name(), engine))
			return nil
		}
		errs = append(errs, fmt.Errorf("%s query: %w", src.Name(), err))
	}
	for _, r := range resyncers {
		layout, err := r.Current(ctx)
		if err == nil {
			next := State{Index: t.indexForLayout(layout), Source: SourceGSettings}
			if next.Index >= 0 {
				next.Confidence = Settings
			}
			t.applyResync(gen, seq, next)
			return nil
		}
		errs = append(errs, fmt.Errorf("resync: %w", err))
	}
	t.applyResync(gen, seq, State{Index: -1, Confidence: Unknown})
	if len(errs) == 0 {
		return errors.New("layout tracker has no source")
	}
	return errors.Join(errs...)
}

func (t *Tracker) applyResync(gen, seq uint64, next State) {
	t.mu.Lock()
	if t.state.Generation != gen || t.hintSeq != seq {
		t.mu.Unlock()
		t.debugf("layout tracker: resync result dropped, state changed meanwhile")
		return
	}
	t.cancelHintLocked()
	changed := t.setLocked(next)
	t.mu.Unlock()
	if changed {
		t.wake()
	}
}

// engineState maps an engine name to a state: Confirmed for an XKB engine of
// the pair, Unknown otherwise.
func (t *Tracker) engineState(source, name string) State {
	st := State{Index: -1, Engine: name, Confidence: Unknown, Source: source}
	if layout, variant, ok := ParseXKBEngine(name); ok {
		st.Index = t.indexForLayout(Layout{Name: layout, Variant: variant})
	}
	if st.Index >= 0 {
		st.Confidence = Confirmed
	}
	return st
}

// indexForLayout maps a layout to the pair index.
func (t *Tracker) indexForLayout(l Layout) int {
	return pairIndex(t.opts.Layouts, l)
}

// pairIndex maps a layout to the pair index. Names must be equal; when both
// variants are non-empty they must be equal; when exactly one is empty it
// matches only if the other pair member has a different name. An identical or
// ambiguous pair yields -1.
func pairIndex(pair [2]Layout, l Layout) int {
	matches := func(i int) bool {
		member := pair[i]
		if member.Name == "" || member.Name != l.Name {
			return false
		}
		if member.Variant != "" && l.Variant != "" {
			return member.Variant == l.Variant
		}
		if member.Variant == l.Variant {
			return true
		}
		return pair[1-i].Name != member.Name
	}
	m0, m1 := matches(0), matches(1)
	switch {
	case m0 && m1:
		return -1
	case m0:
		return 0
	case m1:
		return 1
	default:
		return -1
	}
}

// setLocked stores next (Generation and At are assigned here) unless it equals
// the current state. It reports whether the state changed.
func (t *Tracker) setLocked(next State) bool {
	cur := t.state
	if cur.Index == next.Index && cur.Engine == next.Engine &&
		cur.Confidence == next.Confidence && cur.Source == next.Source {
		return false
	}
	next.Generation = cur.Generation + 1
	next.At = t.opts.Now()
	t.state = next
	t.debugf("layout tracker: %s", next)
	return true
}

func (t *Tracker) armHintLocked() {
	t.cancelHintLocked()
	seq := t.hintSeq
	t.hintStop = t.afterFunc(t.opts.HintTimeout, func() { t.hintExpired(seq) })
}

// cancelHintLocked stops the hint timer and invalidates a timer callback that
// is already running.
func (t *Tracker) cancelHintLocked() {
	if t.hintStop != nil {
		t.hintStop()
		t.hintStop = nil
	}
	t.hintSeq++
}

func (t *Tracker) hintExpired(seq uint64) {
	t.mu.Lock()
	if t.stopped || seq != t.hintSeq || t.state.Confidence != Pending {
		t.mu.Unlock()
		return
	}
	t.hintStop = nil
	ctx := t.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	t.wg.Add(1)
	t.mu.Unlock()
	defer t.wg.Done()

	t.debugf("layout tracker: no engine signal within %v after switch hint, resyncing", t.opts.HintTimeout)
	if err := t.Resync(ctx); err != nil {
		t.debugf("layout tracker: resync failed: %v", err)
	}
}

func (t *Tracker) wake() {
	select {
	case t.notify <- struct{}{}:
	default:
	}
}

func (t *Tracker) dispatch(ctx context.Context) {
	defer t.wg.Done()
	var delivered uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.notify:
		}
		t.mu.Lock()
		st := t.state
		callbacks := slices.Clone(t.callbacks)
		t.mu.Unlock()
		if st.Generation == delivered {
			continue
		}
		delivered = st.Generation
		for _, fn := range callbacks {
			fn(st)
		}
	}
}

func (t *Tracker) logf(format string, args ...any) {
	if t.opts.Logger != nil {
		t.opts.Logger(format, args...)
	}
}

func (t *Tracker) debugf(format string, args ...any) {
	if t.opts.DebugLogger != nil {
		t.opts.DebugLogger(format, args...)
	}
}

// ParseXKBEngine splits an IBus XKB engine name "xkb:<layout>:<variant>:<lang>"
// into layout and variant. The variant and language parts may be empty or
// absent. Non-XKB engines (e.g. "libpinyin") and an empty layout yield
// ok=false.
func ParseXKBEngine(name string) (layout, variant string, ok bool) {
	rest, found := strings.CutPrefix(name, "xkb:")
	if !found {
		return "", "", false
	}
	parts := strings.SplitN(rest, ":", 3)
	layout = parts[0]
	if layout == "" {
		return "", "", false
	}
	if len(parts) > 1 {
		variant = parts[1]
	}
	return layout, variant, true
}
