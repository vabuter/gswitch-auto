package runtime

import (
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/arumata/gswitch/internal/gswitch/fold"
	"github.com/arumata/gswitch/internal/gswitch/lang"
)

const (
	// autoSpaceHeldRetry is how long a due fix waits again while Space is
	// still held.
	autoSpaceHeldRetry = 30 * time.Millisecond
	// autoSpaceHeldMax bounds the wait for the Space release, counted from
	// arming; after it the fix is cancelled ("space-held").
	autoSpaceHeldMax = time.Second
	// autoUndoCountsCap bounds the per-word undo counters.
	autoUndoCountsCap = 64
	// autoDefaultMaxLetters applies when the settings carry no word limit
	// (the auto-max-letters default).
	autoDefaultMaxLetters = 24
	// autoEdgeMaxCoreCost bounds the mean trigram cost (bits per letter) of
	// a shown core that may veto a trailing-edge fix (autoEdgeShownKnown).
	// The English Bloom filter also knows abbreviations such as "dc", "lf"
	// or "bvt", whose cores are typed for the common Russian words "всю",
	// "даю", "имею"; their costs are above 6.0, while words typed with
	// sentence punctuation, such as "it" (5.40) and "bye" (4.78), are below.
	autoEdgeMaxCoreCost = 6.0
)

// Reason codes of the auto engine. lastWordRange, WordTaint, the focus
// policy, lexicalVeto and the detector contribute their own codes, prefixed
// where noted.
const (
	autoReasonDisabled      = "disabled"
	autoReasonModifier      = "modifier"
	autoReasonNoWordStart   = "no-word-start" // the word began before the engine saw a boundary
	autoReasonFocusChanged  = "focus-changed"
	autoReasonLayoutUnknown = "layout-unknown"
	autoReasonLayoutChanged = "layout-changed"
	autoReasonUnrenderable  = "unrenderable"
	autoReasonShiftShape    = "shift-shape"
	// autoReasonEdgeKnown: a trailing key is punctuation on screen but a
	// letter in the other layout, and the word without it is a dictionary
	// word of the shown language that reads like one ("it." would become
	// "шею").
	autoReasonEdgeKnown     = "edge-shown-known"
	autoReasonRateLimit     = "rate-limit"
	autoReasonInputPending  = "input-pending"
	autoReasonBufferChanged = "buffer-changed"
	autoReasonSpaceHeld     = "space-held"
	autoReasonInput         = "input"
	autoReasonLayout        = "layout"
	autoReasonDiscarded     = "discarded"

	autoPrefixTainted  = "tainted:"
	autoPrefixFocus    = "focus:"
	autoPrefixLexical  = "lexical:"
	autoPrefixDetector = "detector:"
	autoPrefixEmit     = "emit:"
	autoPrefixUndoEmit = "undo-emit:"
)

var (
	errAutoMissingDep = errors.New("auto engine: missing dependency")
	errAutoLangs      = errors.New("auto engine: unsupported language pair")
)

// autoModifierKeys are the keys that turn Space into a shortcut: a word is
// never armed while one of them is physically held.
var autoModifierKeys = [...]uint16{
	KEY_LEFTCTRL, KEY_RIGHTCTRL, KEY_LEFTALT, KEY_RIGHTALT, KEY_LEFTMETA, KEY_RIGHTMETA,
}

// autoPending is an armed fix waiting for its deadline.
type autoPending struct {
	start, end int        // wordRange.Start, wordRange.End
	events     []KeyEvent // copy of buffer[start:end)
	shown, alt string
	reason     string // detector Fix reason ("scores", "short-list", "compound")
	keys       int
	index      int    // active layout index at decision time
	generation uint64 // layout generation at decision time
	seq        uint64 // focus Seq at word start (verified at decision time)
	armedAt    time.Time
	deadline   time.Time
}

// autoFix is the last fired fix, kept for the undo gesture.
type autoFix struct {
	start, end  int
	events      []KeyEvent // copy of buffer[start:end)
	gen         uint64     // Converter.UndoGeneration right after the fix
	seq         uint64     // focus Seq the fix was typed into
	shownFolded string     // fold.Word of the word as it was shown before the fix
	keys        int
	from        int // layout index the word was typed in (restored by the undo)
}

// autoWordInfo carries what the debug log may say about a decision.
type autoWordInfo struct {
	keys       int
	rendered   bool
	caps       bool
	shown, alt string
	scores     *lang.Scores
}

func (p *autoPending) info() autoWordInfo {
	return autoWordInfo{keys: p.keys, rendered: true, shown: p.shown, alt: p.alt}
}

// AutoEngine corrects the last word typed in the wrong layout. It decides on
// Space K_DOWN (OnSpace), fires from the Run loop (Tick) once Space is
// released, no key is held and the input is quiet, and undoes its last fix
// on the convert-word gesture (TryUndo). It never clears the converter
// buffer. Every method except Stats must be called on the Run goroutine.
// All exported methods are no-ops on a nil *AutoEngine (auto unavailable).
type AutoEngine struct {
	d AutoDeps

	held  map[uint16]bool // physically held keys
	taint WordTaint

	// Word-start snapshot (§3.2.4 boundaries): taken at the first text
	// K_DOWN after a boundary, invalidated by BufKillers and Reset.
	pendingBoundary bool
	snapValid       bool
	wordGen         uint64
	wordSeq         uint64

	spaceUp bool // Space K_UP observed since the fix was armed

	pending    *autoPending
	lastFix    *autoFix
	lastFireAt time.Time
	undoCounts map[string]int
	undoOrder  []string // insertion order of undoCounts, oldest first

	armed, fired, cancelled, skipped, undone, learned, garbleRisk atomic.Uint64
	lastReason                                                    atomic.Pointer[string]
}

// NewAutoEngine validates the dependencies and returns an idle engine. It
// fails when a layout is nil, the languages are not one supported pair
// ({En, Ru} in either order), or Judge, Layout, Emit or Settings is nil.
func NewAutoEngine(d AutoDeps) (*AutoEngine, error) {
	for i, l := range d.Layouts {
		if l == nil {
			return nil, fmt.Errorf("%w: layout %d", errAutoMissingDep, i+1)
		}
	}
	if !autoLangSupported(d.Langs[0]) || !autoLangSupported(d.Langs[1]) || d.Langs[0] == d.Langs[1] {
		return nil, fmt.Errorf("%w: %s/%s", errAutoLangs, d.Langs[0], d.Langs[1])
	}
	switch {
	case d.Judge == nil:
		return nil, fmt.Errorf("%w: judge", errAutoMissingDep)
	case d.Layout == nil:
		return nil, fmt.Errorf("%w: layout state", errAutoMissingDep)
	case d.Emit == nil:
		return nil, fmt.Errorf("%w: emit", errAutoMissingDep)
	case d.Settings == nil:
		return nil, fmt.Errorf("%w: settings", errAutoMissingDep)
	}
	return &AutoEngine{
		d:          d,
		held:       make(map[uint16]bool),
		undoCounts: make(map[string]int),
	}, nil
}

func autoLangSupported(l lang.Lang) bool {
	return l == lang.En || l == lang.Ru
}

// Observe must see every EV_KEY event before Converter.Push. ctrlHeld is the
// physical Ctrl state including this event. Any K_DOWN or K_REPEAT cancels an
// armed fix; K_UP never does.
func (e *AutoEngine) Observe(code uint16, value int32, ctrlHeld bool) {
	if e == nil {
		return
	}
	if e.pending != nil && (value == K_DOWN || value == K_REPEAT) {
		e.Cancel(autoReasonInput)
	}
	switch value {
	case K_DOWN:
		e.held[code] = true
	case K_UP:
		delete(e.held, code)
		if code == KEY_SPACE {
			e.spaceUp = true
		}
	}
	e.taint.Observe(code, value, ctrlHeld)
	switch {
	case value == K_DOWN:
		e.trackWordStart(code, ctrlHeld)
	case value == K_UP && taintClearsBuffer(code):
		// Converter.Push clears the buffer on this release too; WordTaint
		// taints a word that was in progress.
		e.snapValid = false
		e.pendingBoundary = false
	}
}

// trackWordStart maintains the word-start snapshot with the word boundaries
// of WordTaint: SPACE/ENTER/KPENTER set a pending boundary, a Backspace that
// erases it resumes the previous word (and keeps its snapshot), the next text
// key starts a new word, and a BufKiller other than Ctrl (which empties the
// converter buffer on its press and on its release, see Observe) forces a new
// snapshot. Ctrl+key is neither a boundary nor
// a word start: the switcher never buffers it.
func (e *AutoEngine) trackWordStart(code uint16, ctrlHeld bool) {
	switch {
	case Letters[code]:
		if ctrlHeld {
			return
		}
		switch code {
		case KEY_SPACE, KEY_ENTER, KEY_KPENTER:
			e.pendingBoundary = true
		case KEY_BACKSPACE:
			e.pendingBoundary = false
		default:
			if e.pendingBoundary || !e.snapValid {
				e.wordGen = e.d.Layout().Generation
				e.wordSeq = e.focus().Seq
				e.snapValid = true
			}
			e.pendingBoundary = false
		}
	case taintClearsBuffer(code):
		e.snapValid = false
		e.pendingBoundary = false
	}
}

// OnSpace decides about the word that the Space K_DOWN just pushed into c
// ends. It is called after Push/Process returned ActionNone. A Fix verdict
// arms the engine; every other outcome is counted as a skip with its reason.
func (e *AutoEngine) OnSpace(c *Converter) {
	if e == nil {
		return
	}
	if e.pending != nil {
		// Observe already cancelled it on the Space press; stay safe if the
		// caller skipped Observe.
		e.Cancel(autoReasonInput)
	}
	st := e.d.Settings()
	p, info, reason := e.evaluate(c, st)
	if reason != "" {
		e.skip(reason, info)
		return
	}
	now := e.now()
	p.armedAt = now
	p.deadline = now.Add(st.Holdoff)
	e.pending = p
	e.spaceUp = false
	e.armed.Add(1)
	e.logWord("arm", p.reason, info, st.Holdoff)
}

// evaluate runs the OnSpace pipeline. It returns the pending fix and "" on a
// Fix verdict, else the reason of the first failing step.
func (e *AutoEngine) evaluate(c *Converter, st AutoSettings) (*autoPending, autoWordInfo, string) {
	var info autoWordInfo
	if !st.Enabled {
		return nil, info, autoReasonDisabled
	}
	if e.modifierHeld() {
		return nil, info, autoReasonModifier
	}
	r, reason, ok := lastWordRange(c.buffer, autoMaxLetters(st))
	info.keys = r.TextKeys
	if !ok {
		return nil, info, reason
	}
	if tainted, why := e.taint.Tainted(); tainted {
		return nil, info, autoPrefixTainted + why
	}
	if !e.snapValid {
		return nil, info, autoReasonNoWordStart
	}
	fs := e.focus()
	if fs.Seq != e.wordSeq {
		return nil, info, autoReasonFocusChanged
	}
	if !fs.Allowed {
		return nil, info, autoPrefixFocus + fs.Reason
	}
	ls := e.d.Layout()
	if !ls.Confirmed || ls.Index < 0 || ls.Index > 1 {
		return nil, info, autoReasonLayoutUnknown
	}
	if ls.Generation != e.wordGen {
		return nil, info, autoReasonLayoutChanged
	}
	on, known := e.caps()
	info.caps = on && known
	w, dec, reason := autoDecideWord(c.buffer, r, e.d.Layouts[ls.Index], e.d.Layouts[1-ls.Index],
		lang.Pair{Shown: e.d.Langs[ls.Index], Alt: e.d.Langs[1-ls.Index]}, e.d.Judge, info.caps)
	if reason != autoReasonUnrenderable {
		info.rendered = true
		info.shown, info.alt = w.Shown, w.Alt
		info.scores = dec.Scores
	}
	if reason != "" {
		return nil, info, reason
	}
	return &autoPending{
		start:      r.Start,
		end:        r.End,
		events:     slices.Clone(c.buffer[r.Start:r.End]),
		shown:      w.Shown,
		alt:        w.Alt,
		reason:     dec.Reason,
		keys:       r.TextKeys,
		index:      ls.Index,
		generation: ls.Generation,
		seq:        fs.Seq,
	}, info, ""
}

// autoDecideWord is the pure tail of the OnSpace pipeline, shared with
// --auto-check: renderWord over [r.Start, r.TextEnd) (the word without its
// Space), lexicalVeto, shiftShapeVeto, the detector, then the trailing-edge
// check (autoEdgeShownKnown). reason is "" only for an accepted Fix verdict;
// the decision is the zero value unless the detector ran.
func autoDecideWord(buf []KeyEvent, r wordRange, shown, alt *LayoutInfo, pair lang.Pair,
	judge wordJudge, caps bool,
) (renderedWord, lang.Decision, string) {
	w, err := renderWord(buf, r.Start, r.TextEnd, shown, alt, caps)
	if err != nil {
		return renderedWord{}, lang.Decision{}, autoReasonUnrenderable
	}
	if why, vetoed := lexicalVeto(w.Shown); vetoed {
		return w, lang.Decision{}, autoPrefixLexical + why
	}
	if shiftShapeVeto(w) {
		return w, lang.Decision{}, autoReasonShiftShape
	}
	dec := judge.Decide(w.Shown, w.Alt, pair)
	if dec.Verdict != lang.Fix {
		return w, dec, autoPrefixDetector + dec.Reason
	}
	if autoEdgeShownKnown(w, pair.Shown, judge) {
		return w, dec, autoReasonEdgeKnown
	}
	return w, dec, ""
}

// autoEdgeShownKnown reports a word that is plausible as typed: a key after
// the core gives terminal or closing punctuation (Unicode Po, Pe, Pf) in the
// shown layout but a letter in the alt layout (the us keys . , ; : and the
// apostrophe, quote and closing brackets are ru letters), and the shown core,
// at least two letters long, is a dictionary word whose mean trigram cost is
// at most autoEdgeMaxCoreCost. The detector scores the alt core with that
// letter and the shown core without it, and punto-rs only lets a shown
// dictionary word veto when both cores have the same length
// (layout.rs:215-216), so "it." would become "шею". The cost bound keeps
// Bloom-known abbreviations ("dc." = "всю", "lf." = "даю", "bvt." = "имею")
// from vetoing; this is an addition to the SPEC 3.8 pipeline (SPEC 1.6 C29).
// Not covered, and left to the detector: leading edges (Russian words
// starting with э/х/ж, "'nj" = "это", are common), keys that are not
// word-final punctuation in English (backquote, tilde, opening brackets,
// angle brackets: "dc`" = "всё") and one-letter cores (every letter is a
// dictionary word: "j," = "об").
func autoEdgeShownKnown(w renderedWord, shown lang.Lang, judge wordJudge) bool {
	k, ok := judge.(autoWordKnower)
	if !ok || w.CoreKeys < 2 {
		return false
	}
	shownRunes, altRunes := []rune(w.Shown), []rune(w.Alt)
	edge := false
	for i := w.CoreEnd; i < len(shownRunes) && i < len(altRunes); i++ {
		if unicode.IsLetter(altRunes[i]) && unicode.In(shownRunes[i], unicode.Po, unicode.Pe, unicode.Pf) {
			edge = true
			break
		}
	}
	if !edge {
		return false
	}
	core := string(shownRunes[w.CoreStart:w.CoreEnd])
	if !k.Known(shown, core) {
		return false
	}
	cost, ok := k.Cost(shown, core)
	return ok && cost <= autoEdgeMaxCoreCost
}

// NextDeadline returns how long the Run loop may wait before the next Tick;
// false when no fix is armed.
func (e *AutoEngine) NextDeadline(now time.Time) (time.Duration, bool) {
	if e == nil {
		return 0, false
	}
	if e.pending == nil {
		return 0, false
	}
	return max(e.pending.deadline.Sub(now), 0), true
}

// Tick fires the armed fix once its deadline is due and every gate passes.
// It is called after each Run loop iteration (not during a conversion).
func (e *AutoEngine) Tick(c *Converter) {
	if e == nil {
		return
	}
	p := e.pending
	if p == nil {
		return
	}
	now := e.now()
	if now.Before(p.deadline) {
		return
	}
	if !e.spaceUp {
		// The kernel's Space autorepeat normally cancels first (Observe);
		// this only covers holds shorter than the repeat delay.
		limit := p.armedAt.Add(autoSpaceHeldMax)
		if !now.Before(limit) {
			e.Cancel(autoReasonSpaceHeld)
			return
		}
		p.deadline = now.Add(autoSpaceHeldRetry)
		if p.deadline.After(limit) {
			p.deadline = limit
		}
		return
	}
	st := e.d.Settings()
	if reason := e.fireGate(c, p, st, now); reason != "" {
		e.pending = nil
		e.skip(reason, p.info())
		return
	}
	e.fire(c, p)
}

// fireGate returns the reason of the first fire gate that fails, or "".
func (e *AutoEngine) fireGate(c *Converter, p *autoPending, st AutoSettings, now time.Time) string {
	if !st.Enabled {
		return autoReasonDisabled
	}
	if !e.lastFireAt.IsZero() && now.Sub(e.lastFireAt) < st.MinInterval {
		return autoReasonRateLimit
	}
	if len(e.held) > 0 || !e.inputIdle() {
		return autoReasonInputPending
	}
	if !autoBufferHolds(c.buffer, p.start, p.end, p.events, autoIsShiftUp) {
		return autoReasonBufferChanged
	}
	ls := e.d.Layout()
	if !ls.Confirmed || ls.Index != p.index || ls.Generation != p.generation {
		return autoReasonLayoutChanged
	}
	fs := e.focus()
	if fs.Seq != p.seq {
		return autoReasonFocusChanged
	}
	if !fs.Allowed {
		return autoPrefixFocus + fs.Reason
	}
	return ""
}

// fire emits the correction of p. The layout stays switched afterwards.
func (e *AutoEngine) fire(c *Converter, p *autoPending) {
	e.pending = nil
	info := p.info()
	events := c.ConvertRange(p.start, p.end)
	if events == nil {
		e.skip(autoReasonBufferChanged, info)
		return
	}
	if err := e.d.Emit(events); err != nil {
		// Nothing is recorded: the switch confirmation fails before any
		// text is touched, and the buffer still matches the screen.
		e.skip(autoPrefixEmit+err.Error(), info)
		return
	}
	c.invalidateUndo()
	e.lastFix = &autoFix{
		start:       p.start,
		end:         p.end,
		events:      p.events,
		gen:         c.UndoGeneration(),
		seq:         p.seq,
		shownFolded: fold.Word(p.shown),
		keys:        p.keys,
		from:        p.index,
	}
	at := e.now()
	e.lastFireAt = at
	e.fired.Add(1)
	if !e.inputIdle() {
		// Keys arrived while the correction was typed; they may have been
		// interleaved with it.
		e.garbleRisk.Add(1)
	}
	e.logWord("fire", p.reason, info, 0)
	if e.d.OnFix != nil {
		e.d.OnFix(FixEvent{Shown: p.shown, Alt: p.alt, From: p.index, To: 1 - p.index, At: at})
	}
}

// TryUndo reverts the last fix when the raw convert-word gesture follows it
// directly: same converter undo generation (no text key or Backspace since),
// same focus, the fixed events still at their place and only shift (or
// convert-key) events after them. It retypes the same range with a layout
// toggle and reports the undo through OnUndo. It returns false (and the
// caller runs the upstream conversion) when there is nothing to undo. The
// fix is forgotten in every case, so a second gesture falls through.
func (e *AutoEngine) TryUndo(c *Converter) bool {
	if e == nil {
		return false
	}
	f := e.lastFix
	if f == nil {
		return false
	}
	e.lastFix = nil
	switch {
	case c.UndoGeneration() != f.gen:
		e.logf("auto: undo refused reason=generation")
		return false
	case e.focus().Seq != f.seq:
		e.logf("auto: undo refused reason=focus-changed")
		return false
	case !autoBufferHolds(c.buffer, f.start, f.end, f.events, func(ev KeyEvent) bool {
		return Shifts[ev.Code] || c.isConvKey(ev.Code)
	}):
		e.logf("auto: undo refused reason=buffer-changed")
		return false
	}
	events := c.ConvertRange(f.start, f.end)
	if events == nil {
		return false
	}
	if err := e.d.Emit(events); err != nil {
		// The gesture is consumed: falling through to the upstream
		// conversion could turn it into a whole-line conversion
		// (ReverseMode). A second gesture runs upstream.
		reason := autoPrefixUndoEmit + err.Error()
		e.setReason(reason)
		e.logf("auto: undo failed reason=%s", reason)
		return true
	}
	c.invalidateUndo()
	e.undone.Add(1)
	st := e.d.Settings()
	count := e.noteUndo(f.shownFolded)
	learn := st.LearnOnUndo && f.shownFolded != "" && count >= max(st.LearnUndoCount, 1)
	if learn {
		e.forgetUndo(f.shownFolded)
		e.learned.Add(1)
	}
	if e.d.LogWords {
		e.logf("auto: undo keys=%d layout=%d count=%d learn=%v word=%q", f.keys, f.from, count, learn, f.shownFolded)
	} else {
		e.logf("auto: undo keys=%d layout=%d count=%d learn=%v", f.keys, f.from, count, learn)
	}
	if e.d.OnUndo != nil {
		e.d.OnUndo(f.shownFolded, learn)
	}
	return true
}

// noteUndo increments and returns the undo count of a folded word. The
// counters are capped at autoUndoCountsCap words (the oldest is dropped).
func (e *AutoEngine) noteUndo(word string) int {
	if word == "" {
		return 0
	}
	if _, ok := e.undoCounts[word]; !ok {
		if len(e.undoOrder) >= autoUndoCountsCap {
			delete(e.undoCounts, e.undoOrder[0])
			e.undoOrder = slices.Delete(e.undoOrder, 0, 1)
		}
		e.undoOrder = append(e.undoOrder, word)
	}
	e.undoCounts[word]++
	return e.undoCounts[word]
}

func (e *AutoEngine) forgetUndo(word string) {
	delete(e.undoCounts, word)
	if i := slices.Index(e.undoOrder, word); i >= 0 {
		e.undoOrder = slices.Delete(e.undoOrder, i, i+1)
	}
}

// OnLayoutChange reports a layout state change. Our own switches are
// ignored; an external change cancels a pending fix and disarms the undo.
// The converter buffer is never touched (C5).
func (e *AutoEngine) OnLayoutChange(st LayoutSnapshot, own bool) {
	if e == nil {
		return
	}
	e.logf("auto: layout own=%v confirmed=%v index=%d gen=%d", own, st.Confirmed, st.Index, st.Generation)
	if own {
		return
	}
	e.Cancel(autoReasonLayout)
}

// Cancel cancels a pending fix and disarms the undo of the last fix.
func (e *AutoEngine) Cancel(reason string) {
	if e == nil {
		return
	}
	if e.lastFix != nil {
		e.lastFix = nil
		e.logf("auto: undo disarmed reason=%s", reason)
	}
	if e.pending == nil {
		return
	}
	e.pending = nil
	e.cancelled.Add(1)
	e.setReason(reason)
	e.logf("auto: cancel reason=%s", reason)
}

// Reset is called by afterInputDiscarded (after InputReader.Flush, drainEventChan
// and at the end of SuspendHotkeys): it cancels with reason "discarded", replaces
// the held set with the kernel key set keysDown (nil means none), marks the word
// in progress as tainted "discarded" until a boundary is processed after the
// discard (C25), and invalidates the word-start snapshot, which is re-taken at
// the next text K_DOWN.
func (e *AutoEngine) Reset(keysDown map[uint16]bool) {
	if e == nil {
		return
	}
	e.Cancel(autoReasonDiscarded)
	e.held = make(map[uint16]bool, len(keysDown))
	for code, down := range keysDown {
		if down {
			e.held[code] = true
		}
	}
	e.taint.MarkDiscarded()
	e.snapValid = false
	e.pendingBoundary = false
}

// Stats returns the counters. Safe on any goroutine.
func (e *AutoEngine) Stats() AutoStats {
	if e == nil {
		return AutoStats{}
	}
	st := AutoStats{
		Armed:      e.armed.Load(),
		Fired:      e.fired.Load(),
		Cancelled:  e.cancelled.Load(),
		Skipped:    e.skipped.Load(),
		Undone:     e.undone.Load(),
		Learned:    e.learned.Load(),
		GarbleRisk: e.garbleRisk.Load(),
	}
	if r := e.lastReason.Load(); r != nil {
		st.LastReason = *r
	}
	return st
}

func (e *AutoEngine) skip(reason string, info autoWordInfo) {
	e.skipped.Add(1)
	e.setReason(reason)
	e.logWord("skip", reason, info, 0)
}

func (e *AutoEngine) setReason(reason string) {
	e.lastReason.Store(&reason)
}

func (e *AutoEngine) modifierHeld() bool {
	for _, code := range autoModifierKeys {
		if e.held[code] {
			return true
		}
	}
	return false
}

func (e *AutoEngine) focus() FocusSnapshot {
	if e.d.Focus == nil {
		return FocusSnapshot{Allowed: true}
	}
	return e.d.Focus()
}

func (e *AutoEngine) caps() (on, known bool) {
	if e.d.Caps == nil {
		return false, false
	}
	return e.d.Caps()
}

func (e *AutoEngine) inputIdle() bool {
	return e.d.InputIdle == nil || e.d.InputIdle()
}

func (e *AutoEngine) now() time.Time {
	if e.d.Now == nil {
		return time.Now()
	}
	return e.d.Now()
}

func (e *AutoEngine) logf(format string, args ...any) {
	if e.d.Log != nil {
		e.d.Log(format, args...)
	}
}

// logWord logs one decision event. Words appear only with LogWords; the
// default log carries counts, costs and reasons only.
func (e *AutoEngine) logWord(event, reason string, info autoWordInfo, wait time.Duration) {
	if e.d.Log == nil {
		return
	}
	msg := fmt.Sprintf("auto: %s reason=%s keys=%d", event, reason, info.keys)
	if info.scores != nil {
		msg += fmt.Sprintf(" cost=%.2f/%.2f known=%v/%v",
			info.scores.ShownCost, info.scores.AltCost, info.scores.ShownKnown, info.scores.AltKnown)
	}
	if info.rendered {
		msg += fmt.Sprintf(" caps=%v", info.caps)
	}
	if wait > 0 {
		msg += " wait=" + wait.String()
	}
	if e.d.LogWords && info.rendered {
		msg += fmt.Sprintf(" shown=%q alt=%q", info.shown, info.alt)
	}
	e.d.Log("%s", msg)
}

func autoMaxLetters(st AutoSettings) int {
	if st.MaxLetters <= 0 {
		return autoDefaultMaxLetters
	}
	return st.MaxLetters
}

// autoBufferHolds reports whether buf still holds want at [start, end) and
// every event after end satisfies tailOK.
func autoBufferHolds(buf []KeyEvent, start, end int, want []KeyEvent, tailOK func(KeyEvent) bool) bool {
	if start < 0 || end > len(buf) || end-start != len(want) || !slices.Equal(buf[start:end], want) {
		return false
	}
	for _, ev := range buf[end:] {
		if !tailOK(ev) {
			return false
		}
	}
	return true
}

func autoIsShiftUp(ev KeyEvent) bool {
	return Shifts[ev.Code] && ev.Value == K_UP
}
