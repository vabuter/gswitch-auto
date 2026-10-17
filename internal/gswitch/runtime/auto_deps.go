package runtime

import (
	"time"

	"github.com/arumata/gswitch/internal/gswitch/lang"
)

// wordJudge decides whether a rendered word was typed in the wrong layout.
// *lang.Detector implements it.
type wordJudge interface {
	Decide(shown, alt string, pair lang.Pair) lang.Decision
}

// autoWordKnower is an optional capability of a wordJudge: dictionary
// membership, used by the trailing-edge check of autoDecideWord.
// *lang.Detector implements it; a judge without it skips the check.
type autoWordKnower interface {
	Known(l lang.Lang, word string) bool
}

// LayoutSnapshot is the active layout as the auto engine sees it.
type LayoutSnapshot struct {
	Index      int    // 0 = Layout1, 1 = Layout2; only meaningful when Confirmed
	Generation uint64 // changes on every layout state change
	Confirmed  bool   // the layout is known for sure (IBus) and is one of the pair
}

// FocusSnapshot is the focused application as the auto engine sees it.
type FocusSnapshot struct {
	Seq     uint64 // changes on every focus change
	Allowed bool   // the focus policy allows automatic correction
	Reason  string // why it is not allowed ("password-field", "excluded-app", ...)
}

// AutoSettings are the engine settings, read once per decision and per tick.
type AutoSettings struct {
	Enabled        bool
	MaxLetters     int
	Holdoff        time.Duration
	MinInterval    time.Duration
	LearnOnUndo    bool
	LearnUndoCount int
}

// FixEvent describes one automatic correction. Shown and Alt are the word as
// typed and as corrected; From and To are layout indexes.
type FixEvent struct {
	Shown, Alt string
	From, To   int
	At         time.Time
}

// AutoStats are the engine counters. LastReason is the reason code of the
// latest skip or cancellation ("" before the first one).
type AutoStats struct {
	Armed, Fired, Cancelled, Skipped, Undone, Learned, GarbleRisk uint64
	LastReason                                                    string
}

// AutoDeps is every environment access of the auto engine. Layouts, Langs,
// Judge, Layout, Emit and Settings are required; the other hooks may be nil.
type AutoDeps struct {
	Layouts   [2]*LayoutInfo        // s.layoutConverter.Layout1/Layout2
	Langs     [2]lang.Lang          // lang.LangForLayout(Name) for each; both must be supported
	Judge     wordJudge             // *lang.Detector
	Layout    func() LayoutSnapshot // from Tracker.State(): Confirmed && Index>=0
	Focus     func() FocusSnapshot  // nil => {Seq: 0, Allowed: true}
	Caps      func() (on, known bool)
	InputIdle func() bool                   // eventChan and overflow empty && !InputReader.AnyKeyDown(); nil => idle
	Emit      func(events []KeyEvent) error // emitConversionEvents with auto options
	Settings  func() AutoSettings           // read once per decision / per tick
	OnFix     func(FixEvent)                // IPC + stats hook; may be nil
	// OnUndo receives the undone word folded with fold.Word; learn is true
	// when the undo count of that word reached LearnUndoCount and LearnOnUndo
	// is set.
	OnUndo   func(foldedShown string, learn bool)
	Now      func() time.Time // nil => time.Now
	Log      DebugLogger      // nil => no logging
	LogWords bool             // GSWITCH_AUTO_LOG_WORDS=1 under --debug only
}
