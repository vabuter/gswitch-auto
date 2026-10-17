package runtime

import (
	"strings"
	"testing"
)

// taintTestKeys maps named tokens to scancodes; single characters of
// wordTestRowEN are keys too.
var taintTestKeys = map[string]uint16{
	"SP": KEY_SPACE, "EN": KEY_ENTER, "KPE": KEY_KPENTER, "BS": KEY_BACKSPACE,
	"DEL": KEY_DELETE, "LSGT": KEY_102ND, "CAPS": KEY_CAPSLOCK, "NUM": KEY_NUMLOCK,
	"LM": KEY_LEFTMETA, "RM": KEY_RIGHTMETA, "LS": KEY_LEFTSHIFT, "RS": KEY_RIGHTSHIFT,
	"LC": KEY_LEFTCTRL, "RC": KEY_RIGHTCTRL, "LA": KEY_LEFTALT, "RA": KEY_RIGHTALT,
	"TAB": KEY_TAB, "ESC": KEY_ESC, "F1": 59, "UP": 103, "HOME": 102, "KP1": 79,
}

// taintTestFeeder drives a WordTaint with scripted events and tracks Ctrl the
// way Switcher.trackSelectionModifiers does (state includes the event).
type taintTestFeeder struct {
	t     *testing.T
	taint WordTaint
	ctrl  map[uint16]bool
}

func taintTestNew(t *testing.T) *taintTestFeeder {
	return &taintTestFeeder{t: t, ctrl: make(map[uint16]bool)}
}

// feed runs a script. Tokens: KEY (press and release), KEY+ (press), KEY-
// (release), KEY* (autorepeat).
func (f *taintTestFeeder) feed(script string) *taintTestFeeder {
	f.t.Helper()
	for _, tok := range strings.Fields(script) {
		var values []int32
		switch {
		case strings.HasSuffix(tok, "+"):
			tok, values = tok[:len(tok)-1], []int32{K_DOWN}
		case strings.HasSuffix(tok, "-") && len(tok) > 1:
			tok, values = tok[:len(tok)-1], []int32{K_UP}
		case strings.HasSuffix(tok, "*") && len(tok) > 1:
			tok, values = tok[:len(tok)-1], []int32{K_REPEAT}
		default:
			values = []int32{K_DOWN, K_UP}
		}
		code, ok := taintTestKeys[tok]
		if !ok {
			code, ok = wordTestCharCode(tok)
		}
		if !ok {
			f.t.Fatalf("taintTestFeeder: unknown token %q", tok)
		}
		for _, v := range values {
			if code == KEY_LEFTCTRL || code == KEY_RIGHTCTRL {
				switch v {
				case K_DOWN:
					f.ctrl[code] = true
				case K_UP:
					delete(f.ctrl, code)
				}
			}
			f.taint.Observe(code, v, len(f.ctrl) > 0)
		}
	}
	return f
}

func (f *taintTestFeeder) want(tainted bool, reason string) *taintTestFeeder {
	f.t.Helper()
	gotTainted, gotReason := f.taint.Tainted()
	if gotTainted != tainted || gotReason != reason {
		f.t.Fatalf("Tainted() = %v, %q; want %v, %q", gotTainted, gotReason, tainted, reason)
	}
	return f
}

func (f *taintTestFeeder) clean() *taintTestFeeder {
	f.t.Helper()
	return f.want(false, "")
}

func TestWordTaintZeroValue(t *testing.T) {
	var w WordTaint
	if tainted, reason := w.Tainted(); tainted || reason != "" {
		t.Fatalf("zero WordTaint = %v, %q", tainted, reason)
	}
	taintTestNew(t).feed("LS+ g LS- h b d t n SP").clean()
}

func TestWordTaintRules(t *testing.T) {
	tests := []struct {
		name   string
		script string
		reason string // "" = clean
	}{
		{"letter repeat", "g h+ h* h-", "repeat"},
		{"backspace repeat", "g h BS+ BS* BS-", "repeat"},
		{"space repeat", "g h SP+ SP*", "repeat"},
		{"keypad repeat", "g KP1+ KP1*", "repeat"},
		{"shift repeat ignored", "g LS+ LS* LS* LS- h", ""},
		{"non-letter repeat ignored", "DEL+ DEL* DEL- g h", ""},
		{"delete inside word", "g DEL h", "delete"},
		{"102nd inside word", "g LSGT h", "102nd"},
		{"capslock inside word", "g CAPS h", "capslock"},
		{"numlock inside word", "g NUM h", "numlock"},
		{"delete before first word", "DEL g h", ""},
		{"102nd before first word", "LSGT g h", ""},
		{"capslock before first word", "CAPS g h", ""},
		{"numlock before first word", "NUM g h", ""},
		{"capslock after BufKiller", "g h TAB CAPS g h", ""},
		{"capslock between words", "g h SP CAPS g h", ""},
		{"meta press", "g LM h", "meta"},
		{"right meta press", "g RM", "meta"},
		{"letter while meta held carries into next word", "g LM+ SP h", "meta"},
		{"letter after meta released", "g LM SP h", ""},
		{"meta held after BufKiller", "LM+ TAB g", "meta"},
		{"space while meta held", "TAB LM+ TAB SP", "meta"},
		{"backspace while meta held", "LM+ TAB BS", "meta"},
		{"ctrl letter", "g LC+ v LC- h", "ctrl"},
		{"right ctrl letter", "g RC+ a RC-", "ctrl"},
		{"ctrl backspace", "g h LC+ BS LC-", "ctrl"},
		{"ctrl enter", "g h LC+ EN LC-", "ctrl"},
		{"ctrl alone", "g LC h", ""},
		{"escape", "g ESC h", "unknown-key"},
		{"function key", "g h F1", "unknown-key"},
		{"unknown key before first word", "ESC g h", "unknown-key"},
		{"unknown key between words", "g h SP ESC g h", ""},
		{"key releases ignored", "g- h- DEL- CAPS- ESC-", ""},
		{"first reason wins", "g h* DEL ESC", "repeat"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := taintTestNew(t).feed(tt.script)
			if tt.reason == "" {
				f.clean()
			} else {
				f.want(true, tt.reason)
			}
		})
	}
}

func TestWordTaintBoundaries(t *testing.T) {
	for _, boundary := range []string{"SP", "EN", "KPE"} {
		t.Run(boundary, func(t *testing.T) {
			// The finished word's taint is readable right after the boundary
			// and cleared by the next text key.
			taintTestNew(t).feed("g h* h- "+boundary).want(true, "repeat").feed("b").clean()
		})
	}

	// Shift and a second Space do not end the pending boundary.
	taintTestNew(t).feed("g h* SP SP LS+ LS-").want(true, "repeat").feed("LS+ b LS-").clean()

	// Backspace erasing the boundary resumes the word with its taint.
	taintTestNew(t).feed("g h* SP BS b d t n").want(true, "repeat").feed("SP").want(true, "repeat").
		feed("h").clean()

	// Ctrl+Space is not buffered, so it is no boundary.
	taintTestNew(t).feed("g h LC+ SP LC- b").want(true, "ctrl").feed("d SP").want(true, "ctrl").
		feed("h").clean()

	// A non-Ctrl BufKiller press resets at once.
	for _, killer := range []string{"TAB", "LA+", "RA+", "UP+", "HOME+"} {
		t.Run(killer, func(t *testing.T) {
			taintTestNew(t).feed("g h* " + killer).clean()
		})
	}
	for _, ctrl := range []string{"LC", "RC"} {
		t.Run(ctrl, func(t *testing.T) {
			taintTestNew(t).feed("g h* "+ctrl).want(true, "repeat")
		})
	}

	// BufKiller repeats do not reset; a release inside a word keeps the
	// earlier reason (first reason wins).
	taintTestNew(t).feed("TAB+ g h* TAB* TAB-").want(true, "repeat")

	// The converter clears the buffer on a non-Ctrl BufKiller release too:
	// letters typed since the press are on screen but no longer buffered,
	// so the word is tainted (Tab or arrow rollover).
	for _, killer := range []string{"TAB", "LA", "RA", "UP", "HOME"} {
		t.Run(killer+" released inside a word", func(t *testing.T) {
			taintTestNew(t).feed(killer+"+ g "+killer+"- h b d t n").want(true, "killer-release").
				feed("SP").want(true, "killer-release").feed("h").clean()
		})
		t.Run(killer+" released outside a word", func(t *testing.T) {
			// A release with no text since the reset just resets again.
			taintTestNew(t).feed(killer + "+ LS+ LS- " + killer + "- g h b d t n SP").clean()
			taintTestNew(t).feed("g h SP " + killer + "+ " + killer + "- b d t n").clean()
		})
	}
	// Ctrl presses and releases never clear the buffer.
	taintTestNew(t).feed("g LC+ LC- h RC+ RC- b").clean()

	// After a reset the next word starts outside a word: CapsLock is fine.
	taintTestNew(t).feed("g h* TAB CAPS g h b").clean()

	// Meta held across a boundary taints the next word too.
	taintTestNew(t).feed("g LM+ SP").want(true, "meta").feed("h").want(true, "meta").
		feed("LM- SP g").clean()
}

func TestWordTaintReset(t *testing.T) {
	f := taintTestNew(t).feed("g h* SP")
	f.taint.Reset()
	f.clean()
	// Reset leaves the word context empty: CapsLock does not taint.
	f.feed("CAPS g h").clean()
	// Reset keeps the physical Meta state.
	f.feed("LM+")
	f.taint.Reset()
	f.feed("g").want(true, "meta")
}

func TestWordTaintMarkDiscarded(t *testing.T) {
	t.Run("survives text keys until a boundary", func(t *testing.T) {
		f := taintTestNew(t)
		f.taint.MarkDiscarded()
		f.want(true, "discarded").feed("b d t n").want(true, "discarded")
		// The engine reads the finished word's taint at Space.
		f.feed("SP").want(true, "discarded")
		// The text key after that boundary clears it.
		f.feed("h").clean().feed("e l l o SP").clean()
	})

	t.Run("pending boundary before the call does not count", func(t *testing.T) {
		f := taintTestNew(t).feed("h e l l o SP")
		f.taint.MarkDiscarded()
		f.feed("b d t n").want(true, "discarded").feed("SP").want(true, "discarded").
			feed("h").clean()
	})

	t.Run("space erased by backspace does not count", func(t *testing.T) {
		f := taintTestNew(t)
		f.taint.MarkDiscarded()
		f.feed("b SP BS d").want(true, "discarded").feed("SP h").clean()
	})

	t.Run("cleared by a non-Ctrl BufKiller", func(t *testing.T) {
		f := taintTestNew(t)
		f.taint.MarkDiscarded()
		f.feed("b TAB").clean()
	})

	t.Run("not cleared by Ctrl", func(t *testing.T) {
		f := taintTestNew(t)
		f.taint.MarkDiscarded()
		f.feed("b LC").want(true, "discarded")
	})

	t.Run("overrides an earlier reason", func(t *testing.T) {
		f := taintTestNew(t).feed("g h*")
		f.taint.MarkDiscarded()
		f.want(true, "discarded")
	})

	t.Run("selection conversion drains the start of a word", func(t *testing.T) {
		// "gh" reached the application during a selection conversion and was
		// drained; "bdtn" is buffered. Space must not see a fixable word.
		f := taintTestNew(t).feed("o k SP")
		f.taint.MarkDiscarded()
		f.feed("b d t n SP").want(true, "discarded")
	})

	t.Run("forgets Meta whose release may have been dropped", func(t *testing.T) {
		f := taintTestNew(t).feed("LM+")
		f.taint.MarkDiscarded()
		f.feed("SP g h").clean()
	})

	t.Run("relearns a held Meta from autorepeat", func(t *testing.T) {
		f := taintTestNew(t).feed("LM+")
		f.taint.MarkDiscarded()
		f.feed("LM* SP g").want(true, "meta")
	})
}
