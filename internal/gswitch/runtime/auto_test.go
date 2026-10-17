package runtime

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/arumata/gswitch/internal/gswitch/lang"
)

// Key rows in scancode order 2..13, 16..27, 30..41, 43..53 (47 keys), as in
// punto-rs cli/src/layout/keymap.rs key_char (ru = XKB ru(winkeys)).
const (
	autoTestRowEN      = "1234567890-=qwertyuiop[]asdfghjkl;'`\\zxcvbnm,./"
	autoTestRowENShift = "!@#$%^&*()_+QWERTYUIOP{}ASDFGHJKL:\"~|ZXCVBNM<>?"
	autoTestRowRU      = "1234567890-=йцукенгшщзхъфывапролджэё\\ячсмитьбю."
	autoTestRowRUShift = "!\"№;%:?*()_+ЙЦУКЕНГШЩЗХЪФЫВАПРОЛДЖЭЁ/ЯЧСМИТЬБЮ,"
)

// autoTestLSKeys is the layout-switch chord of the test converter (<Alt>Shift_L).
var autoTestLSKeys = []uint16{KEY_LEFTALT, KEY_LEFTSHIFT}

func autoTestRowCodes() []uint16 {
	codes := make([]uint16, 0, 47)
	for _, r := range [][2]uint16{{2, 13}, {16, 27}, {30, 41}, {43, 53}} {
		for c := r[0]; c <= r[1]; c++ {
			codes = append(codes, c)
		}
	}
	return codes
}

func autoTestRowLayout(name, normal, shifted string) *LayoutInfo {
	l := &LayoutInfo{Name: name, KeyMap: make(map[string][]rune), Unmapped: make(map[string]bool)}
	n, s := []rune(normal), []rune(shifted)
	for i, code := range autoTestRowCodes() {
		key, _ := XKBKeyName(code)
		l.KeyMap[key] = []rune{n[i], s[i]}
	}
	return l
}

// autoTestLayouts returns fixture us and ru layouts (47 keys).
func autoTestLayouts() (us, ru *LayoutInfo) {
	return autoTestRowLayout("us", autoTestRowEN, autoTestRowENShift),
		autoTestRowLayout("ru", autoTestRowRU, autoTestRowRUShift)
}

// autoTestRealLayouts loads us and ru from the installed XKB data, skipping
// the test when it is not available.
func autoTestRealLayouts(t *testing.T) (us, ru *LayoutInfo) {
	t.Helper()
	if _, err := os.Stat("/usr/share/X11/xkb"); err != nil {
		t.Skipf("XKB data not available: %v", err)
	}
	us, err := LoadLayout("us", "")
	if err != nil {
		t.Skipf("XKB us layout not available: %v", err)
	}
	ru, err = LoadLayout("ru", "")
	if err != nil {
		t.Skipf("XKB ru layout not available: %v", err)
	}
	return us, ru
}

var autoTestNamedKeys = map[string]uint16{
	"SP": KEY_SPACE, "EN": KEY_ENTER, "BS": KEY_BACKSPACE, "TAB": KEY_TAB,
	"L": KEY_LEFTSHIFT, "R": KEY_RIGHTSHIFT,
	"LCTRL": KEY_LEFTCTRL, "RCTRL": KEY_RIGHTCTRL, "LALT": KEY_LEFTALT, "RALT": KEY_RIGHTALT,
	"LMETA": KEY_LEFTMETA, "RMETA": KEY_RIGHTMETA,
	"KP1": 79, "CAPS": KEY_CAPSLOCK, "DEL": KEY_DELETE,
}

// autoTestScript turns a script into raw key events. Tokens: a character of
// autoTestRowEN or a name of autoTestNamedKeys is pressed and released;
// "^c" types c with LShift (L↓ c↓ c↑ L↑); a ":d", ":u" or ":r" suffix sends
// only the K_DOWN, K_UP or K_REPEAT of the key.
func autoTestScript(t testing.TB, script string) []KeyEvent {
	t.Helper()
	var events []KeyEvent
	for _, tok := range strings.Fields(script) {
		shift := false
		if rest, ok := strings.CutPrefix(tok, "^"); ok && rest != "" {
			tok, shift = rest, true
		}
		values := []int32{K_DOWN, K_UP}
		if i := strings.LastIndex(tok, ":"); i > 0 && i == len(tok)-2 {
			switch tok[i+1] {
			case 'd':
				values = []int32{K_DOWN}
			case 'u':
				values = []int32{K_UP}
			case 'r':
				values = []int32{K_REPEAT}
			default:
				t.Fatalf("autoTestScript: bad suffix in %q", tok)
			}
			tok = tok[:i]
		}
		code, ok := autoTestKeyCode(tok)
		if !ok {
			t.Fatalf("autoTestScript: unknown token %q", tok)
		}
		if shift {
			events = append(events, KeyEvent{Code: KEY_LEFTSHIFT, Value: K_DOWN})
		}
		for _, v := range values {
			events = append(events, KeyEvent{Code: code, Value: v})
		}
		if shift {
			events = append(events, KeyEvent{Code: KEY_LEFTSHIFT, Value: K_UP})
		}
	}
	return events
}

func autoTestKeyCode(tok string) (uint16, bool) {
	if code, ok := autoTestNamedKeys[tok]; ok {
		return code, true
	}
	runes := []rune(tok)
	if len(runes) != 1 {
		return 0, false
	}
	idx := strings.IndexRune(autoTestRowEN, runes[0])
	if idx < 0 {
		return 0, false
	}
	return autoTestRowCodes()[len([]rune(autoTestRowEN[:idx]))], true
}

// autoTestRaw turns converter-buffer events (shift events and text K_DOWNs)
// into the raw key stream: every text K_DOWN is followed by its K_UP.
func autoTestRaw(buf []KeyEvent) []KeyEvent {
	raw := make([]KeyEvent, 0, 2*len(buf))
	for _, ev := range buf {
		raw = append(raw, ev)
		if !Shifts[ev.Code] && ev.Value == K_DOWN {
			raw = append(raw, KeyEvent{Code: ev.Code, Value: K_UP})
		}
	}
	return raw
}

// autoTestJudge is a fake detector: it returns verdict/reason for every word,
// or Fix only for the shown words in fix when fix is non-nil.
type autoTestJudge struct {
	verdict lang.Verdict
	reason  string
	fix     map[string]bool
	calls   []string
}

func (j *autoTestJudge) Decide(shown, alt string, pair lang.Pair) lang.Decision {
	j.calls = append(j.calls, shown+"|"+alt+"|"+pair.Shown.String()+">"+pair.Alt.String())
	if j.fix != nil {
		if j.fix[shown] {
			return lang.Decision{Verdict: lang.Fix, Reason: lang.ReasonScores}
		}
		return lang.Decision{Verdict: lang.Keep, Reason: lang.ReasonShownKnown}
	}
	return lang.Decision{Verdict: j.verdict, Reason: j.reason}
}

type autoTestUndo struct {
	word  string
	learn bool
}

// autoTestEnv drives an AutoEngine like the Switcher does (Observe before
// Push, OnSpace after a Space push with ActionNone, TryUndo on the raw
// ConvertWord gesture, Tick after every 10 ms loop iteration) with fake
// dependencies and a fake clock.
type autoTestEnv struct {
	t        *testing.T
	now      time.Time
	layout   LayoutSnapshot
	focus    FocusSnapshot
	capsOn   bool
	capsKnow bool
	idle     bool
	// busyDuringEmit makes InputIdle report false once Emit ran (keys
	// arrived during the correction).
	busyDuringEmit bool
	settings       AutoSettings
	judge          autoTestJudge
	layouts        [2]*LayoutInfo
	emitErr        error
	emitted        [][]KeyEvent
	fixes          []FixEvent
	undos          []autoTestUndo
	undoResults    []bool
	logs           []string
	conv           *Converter
	eng            *AutoEngine
	ctrlDown       map[uint16]bool
}

func autoTestNewEnv(t *testing.T) *autoTestEnv {
	t.Helper()
	env := &autoTestEnv{
		t:      t,
		now:    time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC),
		layout: LayoutSnapshot{Index: 0, Generation: 7, Confirmed: true},
		focus:  FocusSnapshot{Seq: 3, Allowed: true},
		idle:   true,
		settings: AutoSettings{
			Enabled:        true,
			MaxLetters:     24,
			Holdoff:        120 * time.Millisecond,
			MinInterval:    time.Second,
			LearnOnUndo:    true,
			LearnUndoCount: 1,
		},
		judge:    autoTestJudge{verdict: lang.Fix, reason: lang.ReasonScores},
		conv:     NewConverter(),
		ctrlDown: make(map[uint16]bool),
	}
	env.conv.LSKeys = autoTestLSKeys
	us, ru := autoTestLayouts()
	env.layouts = [2]*LayoutInfo{us, ru}
	return env
}

// autoTestNew returns a started environment with fixture layouts and the fake judge.
func autoTestNew(t *testing.T) *autoTestEnv {
	t.Helper()
	env := autoTestNewEnv(t)
	env.start(env.deps(&env.judge))
	return env
}

// autoTestNewReal returns a started environment with the installed us/ru
// layouts and the real detector (skipped without XKB data).
func autoTestNewReal(t *testing.T) *autoTestEnv {
	t.Helper()
	env := autoTestNewEnv(t)
	us, ru := autoTestRealLayouts(t)
	env.layouts = [2]*LayoutInfo{us, ru}
	d, err := lang.NewDetector(lang.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	env.start(env.deps(d))
	return env
}

func (env *autoTestEnv) deps(judge wordJudge) AutoDeps {
	return AutoDeps{
		Layouts:   env.layouts,
		Langs:     [2]lang.Lang{lang.En, lang.Ru},
		Judge:     judge,
		Layout:    func() LayoutSnapshot { return env.layout },
		Focus:     func() FocusSnapshot { return env.focus },
		Caps:      func() (bool, bool) { return env.capsOn, env.capsKnow },
		InputIdle: func() bool { return env.idle },
		Emit: func(events []KeyEvent) error {
			if env.emitErr != nil {
				return env.emitErr
			}
			env.emitted = append(env.emitted, slices.Clone(events))
			if env.busyDuringEmit {
				env.idle = false
			}
			return nil
		},
		Settings: func() AutoSettings { return env.settings },
		OnFix:    func(f FixEvent) { env.fixes = append(env.fixes, f) },
		OnUndo: func(word string, learn bool) {
			env.undos = append(env.undos, autoTestUndo{word: word, learn: learn})
		},
		Now: func() time.Time { return env.now },
		Log: func(format string, args ...any) { env.logs = append(env.logs, fmt.Sprintf(format, args...)) },
	}
}

func (env *autoTestEnv) start(d AutoDeps) {
	env.t.Helper()
	eng, err := NewAutoEngine(d)
	if err != nil {
		env.t.Fatalf("NewAutoEngine() error = %v", err)
	}
	env.eng = eng
}

// feed sends a script through the switcher-like key path.
func (env *autoTestEnv) feed(script string) *autoTestEnv {
	env.t.Helper()
	for _, ev := range autoTestScript(env.t, script) {
		env.key(ev.Code, ev.Value)
	}
	return env
}

// feedRaw sends raw events through the switcher-like key path.
func (env *autoTestEnv) feedRaw(events []KeyEvent) *autoTestEnv {
	for _, ev := range events {
		env.key(ev.Code, ev.Value)
	}
	return env
}

// key mirrors Switcher.processKeyEvent with the WP11 hooks (§3.10.3, §4).
func (env *autoTestEnv) key(code uint16, value int32) {
	if code == KEY_LEFTCTRL || code == KEY_RIGHTCTRL {
		if value == K_UP {
			delete(env.ctrlDown, code)
		} else {
			env.ctrlDown[code] = true
		}
	}
	ctrl := len(env.ctrlDown) > 0
	env.eng.Observe(code, value, ctrl)
	if ctrl && Letters[code] {
		return // Ctrl+key is a hotkey, never buffered (switcher.go:671-674)
	}
	if !env.conv.Push(code, value) {
		return
	}
	action := env.conv.Process()
	switch action {
	case ActionNone:
		if code == KEY_SPACE && value == K_DOWN {
			env.eng.OnSpace(env.conv)
		}
	case ActionConvertWord, ActionConvertAll:
		if !env.conv.HasText() {
			return
		}
		// forkBeforeConversion: the raw ConvertWord gesture tries the undo;
		// every other manual conversion disarms it.
		undone := action == ActionConvertWord && env.eng.TryUndo(env.conv)
		if !undone {
			env.eng.Cancel("manual")
		}
		env.undoResults = append(env.undoResults, undone)
	}
}

// wait advances the fake clock in 10 ms loop iterations, ticking after each.
func (env *autoTestEnv) wait(d time.Duration) *autoTestEnv {
	for d > 0 {
		step := min(d, 10*time.Millisecond)
		env.now = env.now.Add(step)
		d -= step
		env.eng.Tick(env.conv)
	}
	return env
}

func (env *autoTestEnv) armed() bool { return env.eng.pending != nil }

func (env *autoTestEnv) wantReason(want string) {
	env.t.Helper()
	if got := env.eng.Stats().LastReason; got != want {
		env.t.Fatalf("LastReason = %q, want %q (logs: %q)", got, want, env.logs)
	}
}

func (env *autoTestEnv) wantEmits(n int) {
	env.t.Helper()
	if len(env.emitted) != n {
		env.t.Fatalf("emitted %d corrections, want %d", len(env.emitted), n)
	}
}

// autoTestConvertEvents builds the expected ConvertRange output for a range
// of buffer events that holds no shift: the toggle, one Backspace pair per
// key and every key replayed with its K_UP.
func autoTestConvertEvents(lsKeys []uint16, keys []uint16) []KeyEvent {
	var out []KeyEvent
	for _, k := range lsKeys {
		out = append(out, KeyEvent{Code: k, Value: K_DOWN})
	}
	for i := len(lsKeys) - 1; i >= 0; i-- {
		out = append(out, KeyEvent{Code: lsKeys[i], Value: K_UP})
	}
	for range keys {
		out = append(out, KeyEvent{Code: KEY_BACKSPACE, Value: K_DOWN}, KeyEvent{Code: KEY_BACKSPACE, Value: K_UP})
	}
	for _, k := range keys {
		out = append(out, KeyEvent{Code: k, Value: K_DOWN}, KeyEvent{Code: k, Value: K_UP})
	}
	return out
}

func autoTestCodes(t *testing.T, chars string) []uint16 {
	t.Helper()
	var codes []uint16
	for _, tok := range strings.Fields(chars) {
		code, ok := autoTestKeyCode(tok)
		if !ok {
			t.Fatalf("unknown key %q", tok)
		}
		codes = append(codes, code)
	}
	return codes
}

// autoTestReplayText renders the replay part of emitted correction events
// (after the toggle and the Backspaces) in layout l.
func autoTestReplayText(t *testing.T, events []KeyEvent, lsKeys []uint16, l *LayoutInfo) string {
	t.Helper()
	i := 2 * len(lsKeys)
	for i < len(events) && events[i].Code == KEY_BACKSPACE {
		i++
	}
	var b strings.Builder
	var left, right bool
	for _, ev := range events[i:] {
		switch ev.Code {
		case KEY_LEFTSHIFT:
			left = ev.Value == K_DOWN
			continue
		case KEY_RIGHTSHIFT:
			right = ev.Value == K_DOWN
			continue
		}
		if ev.Value != K_DOWN {
			continue
		}
		if ev.Code == KEY_SPACE {
			b.WriteRune(' ')
			continue
		}
		name, ok := XKBKeyName(ev.Code)
		if !ok {
			t.Fatalf("replayed key %d has no XKB name", ev.Code)
		}
		r, ok := renderKeyRune(l, name, left || right, false)
		if !ok {
			t.Fatalf("replayed key %s not in layout %s", name, l.Name)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// §6.1 item 1.
func TestAutoEngineFiresAfterHoldoffReleaseAndIdle(t *testing.T) {
	t.Run("holdoff", func(t *testing.T) {
		env := autoTestNew(t)
		env.feed("g h b d t n SP")
		if !env.armed() || env.eng.Stats().Armed != 1 {
			t.Fatalf("not armed after Space: %+v", env.eng.Stats())
		}
		env.wait(110 * time.Millisecond)
		env.wantEmits(0)
		env.wait(10 * time.Millisecond)
		env.wantEmits(1)
		if env.armed() {
			t.Fatal("still armed after firing")
		}
	})
	t.Run("space held re-arms", func(t *testing.T) {
		env := autoTestNew(t)
		env.feed("g h b d t n SP:d")
		env.wait(120 * time.Millisecond)
		env.wantEmits(0)
		if !env.armed() {
			t.Fatal("fix dropped while Space is held")
		}
		if wait, ok := env.eng.NextDeadline(env.now); !ok || wait != autoSpaceHeldRetry {
			t.Fatalf("NextDeadline() = %v, %v, want %v, true", wait, ok, autoSpaceHeldRetry)
		}
		env.wait(300 * time.Millisecond)
		env.wantEmits(0)
		env.feed("SP:u")
		env.wait(20 * time.Millisecond)
		env.wantEmits(0)
		env.wait(10 * time.Millisecond)
		env.wantEmits(1)
	})
	t.Run("space held too long", func(t *testing.T) {
		env := autoTestNew(t)
		env.feed("g h b d t n SP:d")
		env.wait(990 * time.Millisecond)
		if !env.armed() {
			t.Fatal("cancelled before 1 s")
		}
		env.wait(10 * time.Millisecond)
		if env.armed() {
			t.Fatal("still armed after 1 s of Space")
		}
		env.wantReason(autoReasonSpaceHeld)
		if st := env.eng.Stats(); st.Cancelled != 1 || st.Fired != 0 {
			t.Fatalf("stats = %+v, want one cancellation", st)
		}
		env.feed("SP:u").wait(time.Second)
		env.wantEmits(0)
	})
	t.Run("key still held", func(t *testing.T) {
		env := autoTestNew(t)
		env.feed("g h b d t n:d SP")
		env.wait(120 * time.Millisecond)
		env.wantEmits(0)
		env.wantReason(autoReasonInputPending)
	})
	t.Run("input not idle", func(t *testing.T) {
		env := autoTestNew(t)
		env.feed("g h b d t n SP")
		env.idle = false
		env.wait(120 * time.Millisecond)
		env.wantEmits(0)
		env.wantReason(autoReasonInputPending)
	})
}

// §6.1 item 2.
func TestAutoEngineCancelsOnInputDuringHoldoff(t *testing.T) {
	for _, tok := range []string{"a", "L:d", "LCTRL:d", "LMETA:d", "BS", "g:r", "SP:r", "L:r"} {
		t.Run(tok, func(t *testing.T) {
			env := autoTestNew(t)
			env.feed("g h b d t n SP")
			env.wait(50 * time.Millisecond)
			env.feed(tok)
			if env.armed() {
				t.Fatalf("%s did not cancel the armed fix", tok)
			}
			env.wantReason(autoReasonInput)
			env.wait(2 * time.Second)
			env.wantEmits(0)
			if st := env.eng.Stats(); st.Cancelled != 1 {
				t.Fatalf("Cancelled = %d, want 1", st.Cancelled)
			}
		})
	}
	t.Run("key-up of a key pressed before Space", func(t *testing.T) {
		env := autoTestNew(t)
		env.feed("g h b d t n:d SP:d")
		env.feed("n:u SP:u")
		if !env.armed() {
			t.Fatal("a K_UP cancelled the armed fix")
		}
		env.wait(120 * time.Millisecond)
		env.wantEmits(1)
	})
	t.Run("ctrl+space never reaches OnSpace", func(t *testing.T) {
		env := autoTestNew(t)
		env.feed("g h b d t n LCTRL:d SP LCTRL:u")
		if env.armed() || env.eng.Stats().Armed != 0 {
			t.Fatal("Ctrl+Space armed")
		}
	})
	for _, mod := range []string{"LCTRL", "RCTRL", "LALT", "RALT", "LMETA", "RMETA"} {
		t.Run("space with "+mod+" held", func(t *testing.T) {
			env := autoTestNew(t)
			env.feed("g h b d t n " + mod + ":d")
			// Call OnSpace directly: for Ctrl the switcher never pushes the
			// Space at all, for Alt/Meta it does.
			env.eng.Observe(KEY_SPACE, K_DOWN, mod == "LCTRL" || mod == "RCTRL")
			env.conv.Push(KEY_SPACE, K_DOWN)
			env.eng.OnSpace(env.conv)
			if env.armed() {
				t.Fatalf("Space with %s held armed", mod)
			}
			env.wantReason(autoReasonModifier)
		})
	}
}

// §6.1 item 3.
func TestAutoEngineSkipReasons(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(env *autoTestEnv)
		script string
		mid    func(env *autoTestEnv) // between script and tail
		tail   string
		direct bool // call OnSpace explicitly after the script
		want   string
	}{
		{name: "disabled", setup: func(env *autoTestEnv) { env.settings.Enabled = false }, script: "g h b d t n SP", want: "disabled"},
		{name: "modifier", script: "g h b d t n LMETA:d SP", want: "modifier"},
		{name: "shift-held", script: "g h b d t n SP:d L:d", direct: true, want: "shift-held"},
		{name: "no-trailing-space", script: "g h b", direct: true, want: "no-trailing-space"},
		{name: "empty", script: "SP", want: "empty"},
		{name: "double-space", script: "a b SP SP", want: "double-space"},
		{name: "enter-boundary", script: "a b EN SP", want: "enter-boundary"},
		{name: "unrenderable-key", script: "g KP1 h SP", want: "unrenderable-key"},
		{name: "shift-held-at-start", script: "L:d SP g h SP", want: "shift-held-at-start"},
		{name: "too-short", script: "a SP", want: "too-short"},
		{name: "too-long", setup: func(env *autoTestEnv) { env.settings.MaxLetters = 4 }, script: "a b c d e SP", want: "too-long"},
		{name: "tainted", script: "g g:r h b SP", want: "tainted:repeat"},
		{name: "focus-changed", script: "g h", mid: func(env *autoTestEnv) { env.focus.Seq++ }, tail: "b d SP", want: "focus-changed"},
		{name: "focus policy", setup: func(env *autoTestEnv) {
			env.focus.Allowed, env.focus.Reason = false, "password-field"
		}, script: "g h b d SP", want: "focus:password-field"},
		{name: "layout-unknown", setup: func(env *autoTestEnv) { env.layout.Confirmed = false }, script: "g h b d SP", want: "layout-unknown"},
		{name: "layout-changed", script: "g h", mid: func(env *autoTestEnv) { env.layout.Generation++ }, tail: "b d SP", want: "layout-changed"},
		{name: "unrenderable", setup: func(env *autoTestEnv) { delete(env.layouts[1].KeyMap, "AC05") }, script: "g h SP", want: "unrenderable"},
		{name: "unrenderable unmapped", setup: func(env *autoTestEnv) { env.layouts[0].Unmapped["AC06"] = true }, script: "g h SP", want: "unrenderable"},
		{name: "lexical digit", script: "g 1 SP", want: "lexical:digit"},
		{name: "lexical slash", script: "/ g h SP", want: "lexical:leading-slash"},
		{name: "shift-shape", script: "^g ^t ^k SP", want: "shift-shape"},
		{name: "detector", setup: func(env *autoTestEnv) {
			env.judge.verdict, env.judge.reason = lang.Keep, lang.ReasonShownKnown
		}, script: "g h b d t n SP", want: "detector:shown-known"},
		{name: "detector not-candidate", setup: func(env *autoTestEnv) {
			env.judge.verdict, env.judge.reason = lang.NotCandidate, lang.ReasonTooShort
		}, script: "g h SP", want: "detector:too-short"},
		// Order: the first failing step wins.
		{name: "disabled before modifier and taint", setup: func(env *autoTestEnv) { env.settings.Enabled = false },
			script: "g g:r h LMETA:d SP", want: "disabled"},
		{name: "modifier before word range", script: "LMETA:d SP", want: "modifier"},
		{name: "word range before taint", script: "g:r a SP", want: "too-short"},
		{name: "taint before focus", script: "g g:r h", mid: func(env *autoTestEnv) { env.focus.Seq++ }, tail: "b SP", want: "tainted:repeat"},
		{name: "focus before layout", setup: func(env *autoTestEnv) { env.layout.Confirmed = false },
			script: "g h", mid: func(env *autoTestEnv) { env.focus.Seq++ }, tail: "b SP", want: "focus-changed"},
		{name: "focus policy before layout", setup: func(env *autoTestEnv) {
			env.layout.Confirmed = false
			env.focus.Allowed, env.focus.Reason = false, "locked"
		}, script: "g h b SP", want: "focus:locked"},
		{name: "layout before render", setup: func(env *autoTestEnv) { delete(env.layouts[1].KeyMap, "AC05") },
			script: "g h", mid: func(env *autoTestEnv) { env.layout.Generation++ }, tail: "b SP", want: "layout-changed"},
		{name: "lexical before shift-shape", script: "^g ^h 1 SP", want: "lexical:digit"},
		{name: "shift-shape before detector", setup: func(env *autoTestEnv) {
			env.judge.verdict, env.judge.reason = lang.Keep, lang.ReasonShownKnown
		}, script: "^g ^t ^k SP", want: "shift-shape"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := autoTestNewEnv(t)
			if tt.setup != nil {
				tt.setup(env)
			}
			env.start(env.deps(&env.judge))
			env.feed(tt.script)
			if tt.mid != nil {
				tt.mid(env)
			}
			env.feed(tt.tail)
			if tt.direct {
				env.eng.OnSpace(env.conv)
			}
			if env.armed() {
				t.Fatal("armed")
			}
			env.wantReason(tt.want)
			if env.eng.Stats().Skipped == 0 {
				t.Fatal("Skipped not counted")
			}
			// Words skipped before the detector never reach it: every judge
			// call belongs to an earlier word of the script that armed.
			if !strings.HasPrefix(tt.want, autoPrefixDetector) && len(env.judge.calls) != int(env.eng.Stats().Armed) {
				t.Fatalf("judge calls %q for %d armed words", env.judge.calls, env.eng.Stats().Armed)
			}
			env.wait(2 * time.Second)
			env.wantEmits(0)
		})
	}
}

// TestAutoEngineNoWordStart: a word whose start the engine never observed
// (text already buffered when the engine was created) is not judged.
func TestAutoEngineNoWordStart(t *testing.T) {
	env := autoTestNew(t)
	env.conv.buffer = append(env.conv.buffer, autoTestScriptBuffer(t, "g h b d t n")...)
	env.eng.Observe(KEY_SPACE, K_DOWN, false)
	env.conv.Push(KEY_SPACE, K_DOWN)
	env.eng.OnSpace(env.conv)
	if env.armed() {
		t.Fatal("armed without a word start")
	}
	env.wantReason(autoReasonNoWordStart)
	// The next word is evaluated normally.
	env.feed("SP:u h e l l o SP")
	if !env.armed() {
		t.Fatalf("next word not armed: %q", env.eng.Stats().LastReason)
	}
}

// autoTestScriptBuffer returns the converter-buffer form of a script: shift
// events and text K_DOWNs only.
func autoTestScriptBuffer(t *testing.T, script string) []KeyEvent {
	t.Helper()
	var buf []KeyEvent
	for _, ev := range autoTestScript(t, script) {
		if Shifts[ev.Code] || ev.Value == K_DOWN {
			buf = append(buf, ev)
		}
	}
	return buf
}

// §6.1 item 4.
func TestAutoEngineFireGates(t *testing.T) {
	t.Run("rate limit", func(t *testing.T) {
		env := autoTestNew(t)
		env.feed("g h b d t n SP").wait(200 * time.Millisecond)
		env.wantEmits(1)
		env.feed("g h b d t n SP").wait(200 * time.Millisecond)
		env.wantEmits(1)
		env.wantReason(autoReasonRateLimit)
		env.wait(time.Second)
		env.feed("g h b d t n SP").wait(200 * time.Millisecond)
		env.wantEmits(2)
	})
	gates := []struct {
		name   string
		script string
		change func(env *autoTestEnv)
		want   string
	}{
		{"input pending", "g h b d t n SP", func(env *autoTestEnv) { env.idle = false }, autoReasonInputPending},
		{"held key", "g h b d t n:d SP", nil, autoReasonInputPending},
		{"buffer shorter", "g h b d t n SP", func(env *autoTestEnv) {
			env.conv.buffer = env.conv.buffer[:len(env.conv.buffer)-1]
		}, autoReasonBufferChanged},
		{"buffer content", "g h b d t n SP", func(env *autoTestEnv) {
			env.conv.buffer[0].Code = 30
		}, autoReasonBufferChanged},
		{"text after end", "g h b d t n SP", func(env *autoTestEnv) {
			env.conv.buffer = append(env.conv.buffer, KeyEvent{Code: 30, Value: K_DOWN})
		}, autoReasonBufferChanged},
		{"shift down after end", "g h b d t n SP", func(env *autoTestEnv) {
			env.conv.buffer = append(env.conv.buffer, KeyEvent{Code: KEY_LEFTSHIFT, Value: K_DOWN})
		}, autoReasonBufferChanged},
		{"cleared buffer", "g h b d t n SP", func(env *autoTestEnv) {
			env.conv.buffer = env.conv.buffer[:0]
		}, autoReasonBufferChanged},
		{"layout index", "g h b d t n SP", func(env *autoTestEnv) { env.layout.Index = 1 }, autoReasonLayoutChanged},
		{"layout generation", "g h b d t n SP", func(env *autoTestEnv) { env.layout.Generation++ }, autoReasonLayoutChanged},
		{"layout unconfirmed", "g h b d t n SP", func(env *autoTestEnv) { env.layout.Confirmed = false }, autoReasonLayoutChanged},
		{"focus seq", "g h b d t n SP", func(env *autoTestEnv) { env.focus.Seq++ }, autoReasonFocusChanged},
		{"focus policy", "g h b d t n SP", func(env *autoTestEnv) {
			env.focus.Allowed, env.focus.Reason = false, "locked"
		}, "focus:locked"},
		{"disabled", "g h b d t n SP", func(env *autoTestEnv) { env.settings.Enabled = false }, autoReasonDisabled},
	}
	for _, g := range gates {
		t.Run(g.name, func(t *testing.T) {
			env := autoTestNew(t)
			env.feed(g.script)
			if !env.armed() {
				t.Fatalf("not armed: %q", env.eng.Stats().LastReason)
			}
			if g.change != nil {
				g.change(env)
			}
			env.wait(200 * time.Millisecond)
			env.wantEmits(0)
			env.wantReason(g.want)
			if env.armed() {
				t.Fatal("still armed after a failed gate")
			}
			if st := env.eng.Stats(); st.Skipped != 1 || st.Fired != 0 {
				t.Fatalf("stats = %+v, want one skip", st)
			}
		})
	}
	t.Run("shift release after end is fine", func(t *testing.T) {
		env := autoTestNew(t)
		env.feed("g h b d t n L:d SP L:u").wait(200 * time.Millisecond)
		env.wantEmits(1)
	})
	t.Run("halved buffer", func(t *testing.T) {
		env := autoTestNew(t)
		word := autoTestScriptBuffer(t, "g h b d t n SP")
		filler := make([]KeyEvent, MaxKeyBufSize-len(word))
		for i := range filler {
			filler[i] = KeyEvent{Code: KEY_SPACE, Value: K_DOWN}
		}
		env.conv.buffer = append(env.conv.buffer, filler...)
		env.feed("g h b d t n SP:d")
		if !env.armed() || len(env.conv.buffer) != MaxKeyBufSize {
			t.Fatalf("armed=%v len=%d reason=%q", env.armed(), len(env.conv.buffer), env.eng.Stats().LastReason)
		}
		// Any event pushed into a full buffer (here the Space release)
		// makes Push drop the oldest half.
		env.feed("SP:u")
		if len(env.conv.buffer) != MaxKeyBufSize/2 {
			t.Fatalf("buffer not halved: %d", len(env.conv.buffer))
		}
		env.wait(200 * time.Millisecond)
		env.wantEmits(0)
		env.wantReason(autoReasonBufferChanged)
	})
	t.Run("emit error", func(t *testing.T) {
		env := autoTestNew(t)
		env.emitErr = errors.New("layout switch not confirmed")
		env.feed("g h b d t n SP")
		before, gen := env.conv.GetBuffer(), env.conv.UndoGeneration()
		env.wait(200 * time.Millisecond)
		env.wantReason("emit:layout switch not confirmed")
		if st := env.eng.Stats(); st.Skipped != 1 || st.Fired != 0 {
			t.Fatalf("stats = %+v", st)
		}
		if !slices.Equal(env.conv.GetBuffer(), before) || env.conv.UndoGeneration() != gen {
			t.Fatal("buffer or undo generation changed by a failed emission")
		}
		if len(env.fixes) != 0 {
			t.Fatal("OnFix called for a failed emission")
		}
		env.emitErr = nil
		env.feed("L L")
		if !slices.Equal(env.undoResults, []bool{false}) || len(env.emitted) != 0 {
			t.Fatalf("undo after a failed emission: results %v, emitted %d", env.undoResults, len(env.emitted))
		}
	})
}

// §6.1 item 5.
func TestAutoEngineEmittedEvents(t *testing.T) {
	env := autoTestNew(t)
	env.feed("o k SP g h b d t n SP").wait(200 * time.Millisecond)
	// "ok" armed first and was cancelled by "g".
	env.wantEmits(1)
	want := autoTestConvertEvents(autoTestLSKeys, autoTestCodes(t, "g h b d t n SP"))
	if !slices.Equal(env.emitted[0], want) {
		t.Fatalf("emitted\n%v\nwant\n%v", env.emitted[0], want)
	}
	if len(want) != 4+7*2+7*2 {
		t.Fatalf("expected %d events", len(want))
	}
	if len(env.fixes) != 1 {
		t.Fatalf("fixes = %v", env.fixes)
	}
	if f := env.fixes[0]; f.Shown != "ghbdtn" || f.Alt != "привет" || f.From != 0 || f.To != 1 || !f.At.Equal(env.now.Add(-80*time.Millisecond)) {
		t.Fatalf("fix event = %+v", f)
	}

	t.Run("trailing shift release is not replayed", func(t *testing.T) {
		env := autoTestNew(t)
		env.feed("g h b d t n L:d SP L:u").wait(200 * time.Millisecond)
		env.wantEmits(1)
		r, _, ok := lastWordRange(env.conv.buffer, 24)
		if !ok || r.End != len(env.conv.buffer)-1 {
			t.Fatalf("range %+v ok=%v over %d events", r, ok, len(env.conv.buffer))
		}
		want := env.conv.ConvertRange(r.Start, r.End)
		if !slices.Equal(env.emitted[0], want) {
			t.Fatalf("emitted\n%v\nwant ConvertRange\n%v", env.emitted[0], want)
		}
		// The shift pressed inside the range is replayed and released once.
		n := len(want)
		if want[n-1] != (KeyEvent{Code: KEY_LEFTSHIFT, Value: K_UP}) || want[n-2] != (KeyEvent{Code: KEY_SPACE, Value: K_UP}) {
			t.Fatalf("tail of the replay = %v", want[n-3:])
		}
	})
}

// autoTestFired returns an environment that has just auto-corrected "ghbdtn ".
func autoTestFired(t *testing.T) *autoTestEnv {
	t.Helper()
	env := autoTestNew(t)
	env.feed("g h b d t n SP").wait(200 * time.Millisecond)
	env.wantEmits(1)
	return env
}

// §6.1 item 6.
func TestAutoEngineUndo(t *testing.T) {
	t.Run("double shift right after the fix", func(t *testing.T) {
		env := autoTestFired(t)
		gen := env.conv.UndoGeneration()
		env.feed("L L")
		if !slices.Equal(env.undoResults, []bool{true}) {
			t.Fatalf("undo results = %v", env.undoResults)
		}
		env.wantEmits(2)
		if !slices.Equal(env.emitted[1], env.emitted[0]) {
			t.Fatalf("undo emitted\n%v\nwant the fix events\n%v", env.emitted[1], env.emitted[0])
		}
		if env.conv.UndoGeneration() == gen {
			t.Fatal("undo did not invalidate the converter undo")
		}
		if !slices.Equal(env.undos, []autoTestUndo{{word: "ghbdtn", learn: true}}) {
			t.Fatalf("OnUndo calls = %v", env.undos)
		}
		if st := env.eng.Stats(); st.Undone != 1 || st.Learned != 1 {
			t.Fatalf("stats = %+v", st)
		}
		// A second gesture falls through to upstream.
		env.feed("L L")
		if !slices.Equal(env.undoResults, []bool{true, false}) {
			t.Fatalf("second gesture: undo results = %v", env.undoResults)
		}
		env.wantEmits(2)
	})
	t.Run("convert-key events after the range", func(t *testing.T) {
		env := autoTestFired(t)
		env.conv.ConvKey = 119
		env.conv.buffer = append(env.conv.buffer, KeyEvent{Code: 119, Value: K_DOWN}, KeyEvent{Code: 119, Value: K_UP})
		if !env.eng.TryUndo(env.conv) {
			t.Fatal("TryUndo refused convert-key events after the range")
		}
	})
	disarm := []struct {
		name string
		do   func(env *autoTestEnv)
	}{
		{"text key", func(env *autoTestEnv) { env.feed("a") }},
		{"backspace", func(env *autoTestEnv) { env.feed("BS") }},
		{"external layout change", func(env *autoTestEnv) {
			env.layout.Generation++
			env.eng.OnLayoutChange(env.layout, false)
		}},
		{"focus change", func(env *autoTestEnv) { env.focus.Seq++ }},
		{"cancel", func(env *autoTestEnv) { env.eng.Cancel("chord") }},
		{"reset", func(env *autoTestEnv) { env.eng.Reset(nil) }},
		{"buffer changed", func(env *autoTestEnv) { env.conv.buffer[0].Code = 30 }},
	}
	for _, d := range disarm {
		t.Run("disarmed by "+d.name, func(t *testing.T) {
			env := autoTestFired(t)
			d.do(env)
			env.feed("L L")
			if !slices.Equal(env.undoResults, []bool{false}) {
				t.Fatalf("undo results = %v, want [false]", env.undoResults)
			}
			env.wantEmits(1)
			if len(env.undos) != 0 || env.eng.Stats().Undone != 0 {
				t.Fatalf("undo recorded: %v", env.undos)
			}
		})
	}
	t.Run("own layout change keeps the undo", func(t *testing.T) {
		env := autoTestFired(t)
		env.eng.OnLayoutChange(LayoutSnapshot{Index: 1, Generation: 8, Confirmed: true}, true)
		env.feed("L L")
		if !slices.Equal(env.undoResults, []bool{true}) {
			t.Fatalf("undo results = %v", env.undoResults)
		}
	})
	t.Run("learn after two undos of the same folded word", func(t *testing.T) {
		env := autoTestNew(t)
		env.settings.LearnUndoCount = 2
		env.settings.MinInterval = 0
		env.feed("^g h b d t n SP").wait(200 * time.Millisecond)
		env.feed("L L")
		env.feed("g h b d t n SP").wait(200 * time.Millisecond)
		env.feed("R R")
		want := []autoTestUndo{{word: "ghbdtn", learn: false}, {word: "ghbdtn", learn: true}}
		if !slices.Equal(env.undos, want) {
			t.Fatalf("OnUndo calls = %v, want %v", env.undos, want)
		}
		if st := env.eng.Stats(); st.Undone != 2 || st.Learned != 1 || st.Fired != 2 {
			t.Fatalf("stats = %+v", st)
		}
		// The counter restarts after learning.
		env.feed("g h b d t n SP").wait(200 * time.Millisecond)
		env.feed("L L")
		if got := env.undos[len(env.undos)-1]; len(env.undos) != 3 || got.learn {
			t.Fatalf("third undo: %v", env.undos)
		}
	})
	t.Run("learn-on-undo off", func(t *testing.T) {
		env := autoTestNew(t)
		env.settings.LearnOnUndo = false
		env.feed("g h b d t n SP").wait(200 * time.Millisecond)
		env.feed("L L")
		if !slices.Equal(env.undos, []autoTestUndo{{word: "ghbdtn", learn: false}}) || env.eng.Stats().Learned != 0 {
			t.Fatalf("OnUndo calls = %v", env.undos)
		}
	})
	t.Run("undo emission error consumes the gesture", func(t *testing.T) {
		env := autoTestFired(t)
		env.emitErr = errors.New("layout switch not confirmed")
		env.feed("L L")
		if !slices.Equal(env.undoResults, []bool{true}) {
			t.Fatalf("undo results = %v", env.undoResults)
		}
		env.wantReason("undo-emit:layout switch not confirmed")
		if len(env.undos) != 0 || env.eng.Stats().Undone != 0 {
			t.Fatal("failed undo was counted")
		}
		env.emitErr = nil
		env.feed("L L")
		if !slices.Equal(env.undoResults, []bool{true, false}) {
			t.Fatalf("second gesture: undo results = %v", env.undoResults)
		}
	})
}

func TestAutoEngineUndoCountsCapped(t *testing.T) {
	env := autoTestNew(t)
	for i := range autoUndoCountsCap + 1 {
		env.eng.noteUndo(fmt.Sprintf("w%02d", i))
	}
	if len(env.eng.undoCounts) != autoUndoCountsCap || len(env.eng.undoOrder) != autoUndoCountsCap {
		t.Fatalf("counters = %d/%d, want %d", len(env.eng.undoCounts), len(env.eng.undoOrder), autoUndoCountsCap)
	}
	if _, ok := env.eng.undoCounts["w00"]; ok {
		t.Fatal("oldest counter not dropped")
	}
	if got := env.eng.noteUndo("w01"); got != 2 {
		t.Fatalf("noteUndo(w01) = %d, want 2", got)
	}
}

// §6.1 item 7.
func TestAutoEngineLayoutChangeAndReset(t *testing.T) {
	t.Run("own layout change keeps the pending fix", func(t *testing.T) {
		env := autoTestNew(t)
		env.feed("g h b d t n SP")
		env.eng.OnLayoutChange(env.layout, true)
		if !env.armed() {
			t.Fatal("own change cancelled the pending fix")
		}
		env.wait(200 * time.Millisecond)
		env.wantEmits(1)
	})
	t.Run("external layout change cancels", func(t *testing.T) {
		env := autoTestNew(t)
		env.feed("g h b d t n SP")
		env.layout.Generation++
		env.eng.OnLayoutChange(env.layout, false)
		if env.armed() {
			t.Fatal("external change kept the pending fix")
		}
		env.wantReason(autoReasonLayout)
		env.wait(time.Second)
		env.wantEmits(0)
	})
	t.Run("reset replaces the held set and taints the word", func(t *testing.T) {
		env := autoTestNew(t)
		env.feed("x:d g h") // x's key-up is lost with the discarded events
		env.eng.Reset(nil)
		env.feed("b d t n SP")
		if env.armed() {
			t.Fatal("tainted word armed")
		}
		env.wantReason("tainted:discarded")
		env.feed("h e l l o SP")
		if !env.armed() {
			t.Fatalf("next word not armed: %q", env.eng.Stats().LastReason)
		}
		env.wait(200 * time.Millisecond)
		env.wantEmits(1) // held was emptied by Reset(nil)
	})
	t.Run("reset with kernel keys", func(t *testing.T) {
		env := autoTestNew(t)
		env.eng.Reset(map[uint16]bool{30: true, 31: false})
		if len(env.eng.held) != 1 || !env.eng.held[30] {
			t.Fatalf("held = %v, want {30}", env.eng.held)
		}
		env.feed("SP h e l l o SP")
		env.wait(200 * time.Millisecond)
		env.wantReason(autoReasonInputPending)
		env.feed("h e l l o SP a:u")
		env.wait(200 * time.Millisecond)
		env.wantEmits(1)
	})
	t.Run("reset cancels a pending fix", func(t *testing.T) {
		env := autoTestNew(t)
		env.feed("g h b d t n SP")
		env.eng.Reset(nil)
		if env.armed() {
			t.Fatal("pending fix survived Reset")
		}
		env.wantReason(autoReasonDiscarded)
	})
	t.Run("bufkiller after reset clears the taint", func(t *testing.T) {
		env := autoTestNew(t)
		env.feed("g h")
		env.eng.Reset(nil)
		env.feed("TAB g h b d t n SP")
		if !env.armed() {
			t.Fatalf("word after a BufKiller not armed: %q", env.eng.Stats().LastReason)
		}
	})
	t.Run("never clears the buffer", func(t *testing.T) {
		env := autoTestFired(t)
		env.feed("g h b d t n SP")
		before, gen := env.conv.GetBuffer(), env.conv.UndoGeneration()
		env.eng.OnLayoutChange(env.layout, true)
		env.eng.OnLayoutChange(env.layout, false)
		env.eng.Cancel("chord")
		env.eng.Reset(map[uint16]bool{30: true})
		env.eng.Tick(env.conv)
		env.eng.TryUndo(env.conv)
		if !slices.Equal(env.conv.GetBuffer(), before) || env.conv.UndoGeneration() != gen {
			t.Fatal("engine changed the converter buffer")
		}
		src, err := os.ReadFile("auto.go")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), "ClearBuffer(") {
			t.Fatal("auto.go calls ClearBuffer")
		}
	})
}

// §6.1 item 8.
func TestAutoEngineStats(t *testing.T) {
	env := autoTestNew(t)
	env.settings.MinInterval = 0
	env.busyDuringEmit = true
	env.feed("g h b d t n SP").wait(200 * time.Millisecond) // armed, fired, garble risk
	env.idle = true
	env.busyDuringEmit = false
	env.feed("L L")              // undone, learned
	env.feed("g h b d t n SP a") // armed, then cancelled by the word "a"
	env.feed("SP")               // skipped: too-short
	env.feed("g h b d t n SP").wait(200 * time.Millisecond)
	want := AutoStats{Armed: 3, Fired: 2, Cancelled: 1, Skipped: 1, Undone: 1, Learned: 1, GarbleRisk: 1, LastReason: "too-short"}
	if got := env.eng.Stats(); got != want {
		t.Fatalf("Stats() = %+v, want %+v", got, want)
	}
}

// §6.1 item 9.
func TestAutoEngineWordStartSnapshot(t *testing.T) {
	t.Run("bufkiller re-takes the snapshot", func(t *testing.T) {
		for _, killer := range []string{"LALT", "TAB", "RALT"} {
			env := autoTestNew(t)
			env.feed("g h " + killer)
			env.focus.Seq++
			env.layout.Generation++
			env.feed("g h b d t n SP")
			if !env.armed() {
				t.Fatalf("%s: first word after the BufKiller skipped: %q", killer, env.eng.Stats().LastReason)
			}
		}
	})
	t.Run("ctrl does not re-take the snapshot", func(t *testing.T) {
		env := autoTestNew(t)
		env.feed("g h LCTRL")
		env.focus.Seq++
		env.feed("b d t n SP")
		env.wantReason(autoReasonFocusChanged)
	})
	t.Run("erased boundary keeps the snapshot", func(t *testing.T) {
		env := autoTestNew(t)
		env.feed("g h SP BS")
		env.focus.Seq++
		env.feed("b d t n SP")
		if env.armed() {
			t.Fatal("resumed word armed after a focus change")
		}
		env.wantReason(autoReasonFocusChanged)
	})
	t.Run("boundary re-takes the snapshot", func(t *testing.T) {
		env := autoTestNew(t)
		env.feed("g h SP")
		env.focus.Seq++
		env.layout.Generation++
		env.feed("b d t n SP")
		if !env.armed() {
			t.Fatalf("new word skipped: %q", env.eng.Stats().LastReason)
		}
	})
	t.Run("enter is a boundary", func(t *testing.T) {
		env := autoTestNew(t)
		env.feed("g h EN")
		env.layout.Generation++
		env.feed("b d t n SP")
		if !env.armed() {
			t.Fatalf("new word skipped: %q", env.eng.Stats().LastReason)
		}
	})
}

// §6.1 item 10 (real XKB layouts and detector).
func TestAutoEnginePunctuationRealXKB(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		index int    // active layout
		want  string // replay rendered in the other layout
		skip  string // expected skip reason instead
	}{
		{name: "trailing question mark", text: "ghbdtn?", want: "привет, "},
		{name: "capitalised with bang", text: "Ghbdtn!", want: "Привет! "},
		{name: "ru active", text: "руддщ!", index: 1, want: "hello! "},
		{name: "acronym", text: "GTK", skip: autoReasonShiftShape},
		{name: "acronym with comma", text: "GTK,", skip: autoReasonShiftShape},
		{name: "camel case", text: "getValue", skip: autoReasonShiftShape},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := autoTestNewReal(t)
			env.layout.Index = tt.index
			events, err := autoCheckKeyEvents(tt.text, env.layouts[tt.index])
			if err != nil {
				t.Fatal(err)
			}
			env.feedRaw(autoTestRaw(events))
			if tt.skip != "" {
				if env.armed() {
					t.Fatal("armed")
				}
				env.wantReason(tt.skip)
				return
			}
			if !env.armed() {
				t.Fatalf("not armed: %q", env.eng.Stats().LastReason)
			}
			env.wait(200 * time.Millisecond)
			env.wantEmits(1)
			if got := autoTestReplayText(t, env.emitted[0], autoTestLSKeys, env.layouts[1-tt.index]); got != tt.want {
				t.Fatalf("replay = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewAutoEngineValidation(t *testing.T) {
	env := autoTestNewEnv(t)
	valid := env.deps(&env.judge)
	tests := []struct {
		name   string
		mutate func(d *AutoDeps)
		want   error
	}{
		{"layout 1 nil", func(d *AutoDeps) { d.Layouts[0] = nil }, errAutoMissingDep},
		{"layout 2 nil", func(d *AutoDeps) { d.Layouts[1] = nil }, errAutoMissingDep},
		{"unknown language", func(d *AutoDeps) { d.Langs[1] = lang.Unknown }, errAutoLangs},
		{"same languages", func(d *AutoDeps) { d.Langs = [2]lang.Lang{lang.Ru, lang.Ru} }, errAutoLangs},
		{"judge nil", func(d *AutoDeps) { d.Judge = nil }, errAutoMissingDep},
		{"layout state nil", func(d *AutoDeps) { d.Layout = nil }, errAutoMissingDep},
		{"emit nil", func(d *AutoDeps) { d.Emit = nil }, errAutoMissingDep},
		{"settings nil", func(d *AutoDeps) { d.Settings = nil }, errAutoMissingDep},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := valid
			tt.mutate(&d)
			if eng, err := NewAutoEngine(d); !errors.Is(err, tt.want) || eng != nil {
				t.Fatalf("NewAutoEngine() = %v, %v, want %v", eng, err, tt.want)
			}
		})
	}
	t.Run("ru/en order", func(t *testing.T) {
		d := valid
		d.Langs = [2]lang.Lang{lang.Ru, lang.En}
		if _, err := NewAutoEngine(d); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("optional hooks nil", func(t *testing.T) {
		d := valid
		d.Focus, d.Caps, d.InputIdle, d.OnFix, d.OnUndo, d.Now, d.Log = nil, nil, nil, nil, nil, nil, nil
		d.Settings = func() AutoSettings {
			return AutoSettings{Enabled: true, LearnOnUndo: true} // zero holdoff and limits
		}
		eng, err := NewAutoEngine(d)
		if err != nil {
			t.Fatal(err)
		}
		env.eng = eng
		env.feed("g h b d t n SP")
		if !env.armed() {
			t.Fatalf("not armed: %q", eng.Stats().LastReason)
		}
		time.Sleep(time.Millisecond) // real clock
		eng.Tick(env.conv)
		env.wantEmits(1)
		env.feed("L L")
		if !slices.Equal(env.undoResults, []bool{true}) {
			t.Fatalf("undo results = %v", env.undoResults)
		}
	})
}

func TestAutoEngineNextDeadline(t *testing.T) {
	env := autoTestNew(t)
	if _, ok := env.eng.NextDeadline(env.now); ok {
		t.Fatal("deadline while idle")
	}
	env.feed("g h b d t n SP")
	if d, ok := env.eng.NextDeadline(env.now); !ok || d != 120*time.Millisecond {
		t.Fatalf("NextDeadline() = %v, %v", d, ok)
	}
	if d, ok := env.eng.NextDeadline(env.now.Add(time.Second)); !ok || d != 0 {
		t.Fatalf("NextDeadline() past the deadline = %v, %v", d, ok)
	}
	env.wait(200 * time.Millisecond)
	if _, ok := env.eng.NextDeadline(env.now); ok {
		t.Fatal("deadline after firing")
	}
}

func TestAutoEngineLogsNoWordsByDefault(t *testing.T) {
	for _, logWords := range []bool{false, true} {
		env := autoTestNewEnv(t)
		d := env.deps(&env.judge)
		d.LogWords = logWords
		env.start(d)
		env.feed("g h b d t n SP").wait(200 * time.Millisecond)
		env.feed("L L")
		env.feed("j SP")
		logs := strings.Join(env.logs, "\n")
		for _, want := range []string{
			"auto: arm reason=scores keys=6", "auto: fire reason=scores keys=6",
			"auto: undo keys=6", "auto: skip reason=too-short keys=1",
		} {
			if !strings.Contains(logs, want) {
				t.Fatalf("logs lack %q:\n%s", want, logs)
			}
		}
		hasWords := strings.Contains(logs, "ghbdtn") || strings.Contains(logs, "привет")
		if hasWords != logWords {
			t.Fatalf("LogWords=%v but words in logs = %v:\n%s", logWords, hasWords, logs)
		}
	}
}

func TestAutoEngineCapsLock(t *testing.T) {
	for _, tc := range []struct {
		on, known, want bool
	}{{true, true, true}, {true, false, false}, {false, true, false}} {
		env := autoTestNewEnv(t)
		env.capsOn, env.capsKnow = tc.on, tc.known
		env.start(env.deps(&env.judge))
		env.feed("g h b d t n SP")
		want := "ghbdtn|привет|en>ru"
		if tc.want {
			want = "GHBDTN|ПРИВЕТ|en>ru"
		}
		if !slices.Equal(env.judge.calls, []string{want}) {
			t.Fatalf("caps %v/%v: judge calls %q, want %q", tc.on, tc.known, env.judge.calls, want)
		}
	}
}

func TestAutoEngineRuActive(t *testing.T) {
	env := autoTestNew(t)
	env.layout.Index = 1
	env.feed("h e l l o SP").wait(200 * time.Millisecond)
	if !slices.Equal(env.judge.calls, []string{"руддщ|hello|ru>en"}) {
		t.Fatalf("judge calls %q", env.judge.calls)
	}
	if len(env.fixes) != 1 || env.fixes[0].From != 1 || env.fixes[0].To != 0 {
		t.Fatalf("fixes = %+v", env.fixes)
	}
}

// TestAutoEngineEdgeShownKnown: a trailing key that is word-final
// punctuation on screen but a letter in the other layout does not turn a
// dictionary word into a Russian one ("it." is not "шею"), while words that
// only look alike stay fixable.
func TestAutoEngineEdgeShownKnown(t *testing.T) {
	d, err := lang.NewDetector(lang.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		script string
		want   string // "" = armed
	}{
		{"i t . SP", autoReasonEdgeKnown},    // it. -> шею
		{"b y e . SP", autoReasonEdgeKnown},  // bye. -> иную
		{"^i t . SP", autoReasonEdgeKnown},   // It. -> Шею
		{"i t , SP", "detector:alt-unknown"}, // it, -> шеб
		{"v j . SP", ""},                     // vj. -> мою: "vj" is not a word
		{"d c ` SP", ""},                     // dc` -> всё: '`' is not word-final punctuation
		{"j , SP", ""},                       // j, -> об: one-letter core
		{"' n j SP", ""},                     // 'nj -> это: leading edge
		{"k . , k . SP", ""},                 // k.,k. -> люблю: the core has punctuation
		{"g h b d t n SP", ""},               // no edge
	}
	for _, tt := range tests {
		t.Run(tt.script, func(t *testing.T) {
			env := autoTestNewEnv(t)
			env.start(env.deps(d))
			env.feed(tt.script)
			if tt.want == "" {
				if !env.armed() {
					t.Fatalf("not armed: %q", env.eng.Stats().LastReason)
				}
				return
			}
			if env.armed() {
				t.Fatal("armed")
			}
			env.wantReason(tt.want)
		})
	}
	t.Run("judge without Known", func(t *testing.T) {
		env := autoTestNew(t) // the fake judge has no Known method
		env.feed("i t . SP")
		if !env.armed() {
			t.Fatalf("not armed: %q", env.eng.Stats().LastReason)
		}
	})
	t.Run("no core", func(t *testing.T) {
		us, ru := autoTestLayouts()
		buf := autoTestScriptBuffer(t, ". ,")
		w, err := renderWord(buf, 0, len(buf), us, ru, false)
		if err != nil || w.CoreKeys != 0 {
			t.Fatalf("renderWord() = %+v, %v", w, err)
		}
		if autoEdgeShownKnown(w, lang.En, d) {
			t.Fatal("edge check without a core")
		}
	})
}

// TestAutoEngineNilReceiver: with auto unavailable (s.auto == nil) every hook
// is a no-op and the converter is untouched.
func TestAutoEngineNilReceiver(t *testing.T) {
	var e *AutoEngine
	c := NewConverter()
	c.Push(30, K_DOWN)
	c.Push(KEY_SPACE, K_DOWN)
	before, gen := c.GetBuffer(), c.UndoGeneration()
	e.Observe(30, K_DOWN, false)
	e.OnSpace(c)
	e.Tick(c)
	e.OnLayoutChange(LayoutSnapshot{}, false)
	e.Cancel("manual")
	e.Reset(map[uint16]bool{30: true})
	if e.TryUndo(c) {
		t.Fatal("TryUndo on nil engine")
	}
	if _, ok := e.NextDeadline(time.Now()); ok {
		t.Fatal("deadline on nil engine")
	}
	if st := e.Stats(); st != (AutoStats{}) {
		t.Fatalf("Stats() = %+v", st)
	}
	if !slices.Equal(c.GetBuffer(), before) || c.UndoGeneration() != gen {
		t.Fatal("converter changed")
	}
}
