package runtime

import "errors"

// ErrTainted reports that the word in progress no longer matches what the
// application shows, so it must not be corrected automatically.
var ErrTainted = errors.New("word diverged from screen")

// Taint reasons reported by WordTaint.Tainted.
const (
	taintReasonRepeat     = "repeat"      // K_REPEAT of a Letters key (incl. Backspace)
	taintReasonCtrl       = "ctrl"        // Letters key while Ctrl is held
	taintReasonMeta       = "meta"        // Meta press, or a Letters key while Meta is held
	taintReasonDelete     = "delete"      // Delete inside a word
	taintReason102nd      = "102nd"       // KEY_102ND inside a word (not in Letters, never buffered)
	taintReasonCapsLock   = "capslock"    // CapsLock inside a word
	taintReasonNumLock    = "numlock"     // NumLock inside a word
	taintReasonUnknownKey = "unknown-key" // any other key outside Letters ∪ Shifts ∪ BufKillers
	taintReasonDiscarded  = "discarded"   // keystrokes already delivered were dropped (C25)
)

const (
	taintMetaLeft  = 1 << 0
	taintMetaRight = 1 << 1
)

// WordTaint watches the raw key stream for events that make the converter
// buffer diverge from the text on screen (autorepeat, editing keys, hotkeys,
// dropped input) and marks the word in progress as unfixable. It never counts
// characters. The zero value is ready to use. Not safe for concurrent use: it
// lives on the Run goroutine.
//
// Word boundaries (shared with the auto engine's word-start snapshot): a
// SPACE/ENTER/KPENTER press sets a pending boundary and the next text key
// press resets the state, so the taint of the finished word stays readable
// right after the boundary was observed; a Backspace press while a boundary is
// pending erases it (the word resumes); a BufKiller press other than Ctrl
// resets immediately, mirroring the converter's ClearBuffer.
type WordTaint struct {
	tainted         bool
	reason          string
	inWord          bool // a text key was buffered since the last reset
	pendingBoundary bool
	metaDown        int // taintMetaLeft | taintMetaRight
}

// Observe must see every EV_KEY event before Converter.Push. ctrlHeld is the
// physical Ctrl state including this event.
func (t *WordTaint) Observe(code uint16, value int32, ctrlHeld bool) {
	t.trackMeta(code, value)

	switch value {
	case K_REPEAT:
		// The converter ignores Letters repeats (and consumes Backspace
		// repeats at the kernel rate), while the compositor repeats at its
		// own rate: the buffer no longer matches the screen.
		if Letters[code] {
			t.taint(taintReasonRepeat)
		}
		return
	case K_DOWN:
	default:
		return
	}

	switch {
	case Letters[code]:
		t.observeLetter(code, ctrlHeld)
	case Shifts[code]:
		// Shift state is replayed from the buffer.
	case BufKillers[code]:
		if code != KEY_LEFTCTRL && code != KEY_RIGHTCTRL {
			t.Reset()
		}
	default:
		t.observeOtherKey(code)
	}
}

// observeLetter handles a K_DOWN of a Letters key.
func (t *WordTaint) observeLetter(code uint16, ctrlHeld bool) {
	if ctrlHeld {
		// Ctrl+key is a hotkey that the switcher never buffers: it is
		// neither a boundary nor a word start, but the application may have
		// edited the text (Ctrl+Backspace, Ctrl+V, Ctrl+Enter...).
		t.taint(taintReasonCtrl)
		return
	}
	switch code {
	case KEY_SPACE, KEY_ENTER, KEY_KPENTER:
		t.taintIfMeta()
		t.pendingBoundary = true
	case KEY_BACKSPACE:
		// Erasing a pending boundary resumes the previous word with its taint.
		t.pendingBoundary = false
		t.taintIfMeta()
	default:
		if t.pendingBoundary {
			t.Reset()
		}
		t.inWord = true
		t.taintIfMeta()
	}
}

// observeOtherKey handles a K_DOWN outside Letters ∪ Shifts ∪ BufKillers.
func (t *WordTaint) observeOtherKey(code uint16) {
	switch code {
	case KEY_DELETE:
		t.taintInWord(taintReasonDelete)
	case KEY_102ND:
		t.taintInWord(taintReason102nd)
	case KEY_CAPSLOCK:
		// CapsLock before a word starts is fine: the renderer reads the
		// LED state when the word ends.
		t.taintInWord(taintReasonCapsLock)
	case KEY_NUMLOCK:
		t.taintInWord(taintReasonNumLock)
	case KEY_LEFTMETA, KEY_RIGHTMETA:
		t.taint(taintReasonMeta)
	default:
		t.taint(taintReasonUnknownKey)
	}
}

// trackMeta keeps the set of held Meta keys. K_REPEAT also marks the key
// held, so a key-up lost to a discard cannot leave Meta stuck.
func (t *WordTaint) trackMeta(code uint16, value int32) {
	var bit int
	switch code {
	case KEY_LEFTMETA:
		bit = taintMetaLeft
	case KEY_RIGHTMETA:
		bit = taintMetaRight
	default:
		return
	}
	if value == K_UP {
		t.metaDown &^= bit
	} else {
		t.metaDown |= bit
	}
}

func (t *WordTaint) taint(reason string) {
	if t.tainted {
		return
	}
	t.tainted = true
	t.reason = reason
}

func (t *WordTaint) taintInWord(reason string) {
	if t.inWord {
		t.taint(reason)
	}
}

func (t *WordTaint) taintIfMeta() {
	if t.metaDown != 0 {
		t.taint(taintReasonMeta)
	}
}

// Tainted reports whether the current word is unfixable and why.
func (t *WordTaint) Tainted() (bool, string) {
	return t.tainted, t.reason
}

// Reset clears the taint (a word boundary that is known to match the screen).
// The held Meta keys are physical state and survive.
func (t *WordTaint) Reset() {
	t.tainted = false
	t.reason = ""
	t.inWord = false
	t.pendingBoundary = false
}

// MarkDiscarded taints the word in progress with reason "discarded" because keystrokes that
// the application already received were dropped (InputReader.Flush, drainEventChan,
// end of SuspendHotkeys). The taint survives until the next boundary processed AFTER the
// call (the next text K_DOWN after that boundary clears it; a non-Ctrl BufKiller clears it at
// once). A Space boundary that is then erased by Backspace does not count: the word resumes
// and stays tainted.
//
// A boundary that was pending before the call is dropped, and the held Meta keys are
// forgotten because their key-ups may have been among the dropped events (a still-held
// Meta key is re-learned from its autorepeat).
func (t *WordTaint) MarkDiscarded() {
	t.tainted = true
	t.reason = taintReasonDiscarded
	t.pendingBoundary = false
	t.metaDown = 0
}
