package runtime

import (
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// gswitch-auto WP0 tests: golden capture of the manual conversion output
// (performConversion) taken before the emitConversionEvents extraction, plus
// the shared WP0 helpers (prefix emitTest*). Emitted evdev records go to a
// regular file; no host input device is ever created.

var emitTestKeyNames = map[string]uint16{
	"1": 2, "2": 3, "3": 4, "4": 5, "5": 6, "6": 7, "7": 8, "8": 9, "9": 10, "0": 11,
	"q": 16, "w": 17, "e": 18, "r": 19, "t": 20, "y": 21, "u": 22, "i": 23, "o": 24, "p": 25,
	"a": 30, "s": 31, "d": 32, "f": 33, "g": 34, "h": 35, "j": 36, "k": 37, "l": 38,
	"z": 44, "x": 45, "c": 46, "v": 47, "b": 48, "n": 49, "m": 50,
	",": 51, ".": 52, "/": 53,
	"sp": KEY_SPACE, "ent": KEY_ENTER, "kpent": KEY_KPENTER, "bs": KEY_BACKSPACE,
	"tab": 15, "ls": KEY_LEFTSHIFT, "rs": KEY_RIGHTSHIFT, "ctrl": KEY_LEFTCTRL,
	"pause": testKeyPause,
}

// emitTestSeq parses a space-separated key script. A token is a key name from
// emitTestKeyNames or "#<code>", optionally followed by "+" (down, the
// default), "-" (up), "~" (repeat) or "*" (tap: down then up).
func emitTestSeq(t testing.TB, script string) []KeyEvent {
	t.Helper()
	var events []KeyEvent
	for token := range strings.FieldsSeq(script) {
		name, suffix := token, byte('+')
		if last := token[len(token)-1]; len(token) > 1 && strings.IndexByte("+-~*", last) >= 0 {
			name, suffix = token[:len(token)-1], last
		}
		var code uint16
		if rest, ok := strings.CutPrefix(name, "#"); ok {
			n, err := strconv.ParseUint(rest, 10, 16)
			if err != nil {
				t.Fatalf("bad key code in %q: %v", token, err)
			}
			code = uint16(n)
		} else {
			known, ok := emitTestKeyNames[name]
			if !ok {
				t.Fatalf("unknown key name in %q", token)
			}
			code = known
		}
		switch suffix {
		case '+':
			events = append(events, KeyEvent{Code: code, Value: K_DOWN})
		case '-':
			events = append(events, KeyEvent{Code: code, Value: K_UP})
		case '~':
			events = append(events, KeyEvent{Code: code, Value: K_REPEAT})
		case '*':
			events = append(events, KeyEvent{Code: code, Value: K_DOWN}, KeyEvent{Code: code, Value: K_UP})
		}
	}
	return events
}

// emitTestFormat renders events as "code+" (down), "code-" (up) or "code~"
// (repeat) tokens. nil renders as "<nil>".
func emitTestFormat(events []KeyEvent) string {
	if events == nil {
		return "<nil>"
	}
	parts := make([]string, len(events))
	for i, ev := range events {
		switch ev.Value {
		case K_DOWN:
			parts[i] = fmt.Sprintf("%d+", ev.Code)
		case K_UP:
			parts[i] = fmt.Sprintf("%d-", ev.Code)
		case K_REPEAT:
			parts[i] = fmt.Sprintf("%d~", ev.Code)
		default:
			parts[i] = fmt.Sprintf("%d?%d", ev.Code, ev.Value)
		}
	}
	return strings.Join(parts, " ")
}

func emitTestIsShiftUp(ev KeyEvent) bool {
	return Shifts[ev.Code] && ev.Value == K_UP
}

func emitTestSortByCode(events []KeyEvent) {
	slices.SortStableFunc(events, func(a, b KeyEvent) int { return cmp.Compare(a.Code, b.Code) })
}

// emitTestCanonicalConvert sorts the trailing run of shift releases: upstream
// appends the "extra" releases of shifts still held at the end of the range
// in map iteration order (converter.go, Convert).
func emitTestCanonicalConvert(events []KeyEvent) []KeyEvent {
	if events == nil {
		return nil
	}
	out := slices.Clone(events)
	i := len(out)
	for i > 0 && emitTestIsShiftUp(out[i-1]) {
		i--
	}
	emitTestSortByCode(out[i:])
	return out
}

// emitTestCanonicalEmitted additionally sorts the leading shift releases that
// performConversion emits by ranging over the Shifts map.
func emitTestCanonicalEmitted(events []KeyEvent) []KeyEvent {
	out := emitTestCanonicalConvert(events)
	n := 0
	for n < len(out) && n < len(Shifts) && emitTestIsShiftUp(out[n]) {
		n++
	}
	emitTestSortByCode(out[:n])
	return out
}

// emitTestKeyboard returns a VirtualKeyboard writing to a temporary regular file.
func emitTestKeyboard(t *testing.T) (*VirtualKeyboard, *os.File) {
	t.Helper()
	output, err := os.CreateTemp(t.TempDir(), "events")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { output.Close() })
	return &VirtualKeyboard{fd: output}, output
}

// emitTestCaptured decodes the EV_KEY records written to output so far.
func emitTestCaptured(t *testing.T, output *os.File) []KeyEvent {
	t.Helper()
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	events := []KeyEvent{}
	for {
		var event InputEvent
		err := binary.Read(output, binary.LittleEndian, &event)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if event.Type == EV_KEY {
			events = append(events, KeyEvent{Code: event.Code, Value: event.Value})
		}
	}
	if _, err := output.Seek(0, io.SeekEnd); err != nil {
		t.Fatal(err)
	}
	return events
}

// emitTestStubGsettings replaces the GNOME Wayland mru-sources reader:
// the first read returns "us", later reads return "ru" (switch observed) or,
// with failLater, an error (confirmation fails before any text mutation).
func emitTestStubGsettings(t *testing.T, failLater bool) *int {
	t.Helper()
	original := runGsettingsForGNOMEWayland
	t.Cleanup(func() { runGsettingsForGNOMEWayland = original })
	calls := 0
	runGsettingsForGNOMEWayland = func(*SessionEnv, ...string) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte("[('xkb', 'us'), ('xkb', 'ru')]\n"), nil
		}
		if failLater {
			return nil, errors.New("gsettings unavailable")
		}
		return []byte("[('xkb', 'ru'), ('xkb', 'us')]\n"), nil
	}
	return &calls
}

type emitTestPath string

const (
	emitTestPathBlind          emitTestPath = "blind"
	emitTestPathWaylandOK      emitTestPath = "wayland-confirmed"
	emitTestPathWaylandFailed  emitTestPath = "wayland-read-error"
	emitTestPathX11Unavailable emitTestPath = "x11-unavailable"
)

// emitTestSwitcher builds a Switcher whose replay goes to a temporary file
// and selects one of the upstream switch paths.
func emitTestSwitcher(t *testing.T, path emitTestPath, cfg *Config) (*Switcher, *os.File) {
	t.Helper()
	keyboard, output := emitTestKeyboard(t)
	s := &Switcher{
		config:      cfg,
		converter:   NewConverter(),
		vKeyboard:   keyboard,
		inputReader: &InputReader{},
	}
	s.converter.ConvKey = cfg.ConvertKey
	s.converter.LSKeys = cfg.LayoutSwitchKey
	switch path {
	case emitTestPathBlind:
	case emitTestPathWaylandOK:
		s.verifyGNOMEWaylandLayoutSwitch = true
		emitTestStubGsettings(t, false)
	case emitTestPathWaylandFailed:
		s.verifyGNOMEWaylandLayoutSwitch = true
		emitTestStubGsettings(t, true)
	case emitTestPathX11Unavailable:
		s.verifyGNOMEX11LayoutSwitch = true
		t.Setenv("DISPLAY", "")
	default:
		t.Fatalf("unknown path %q", path)
	}
	return s, output
}

type emitTestPerformFixture struct {
	name    string
	lsKeys  []uint16
	convKey uint16
	script  string
	process bool
}

var emitTestPerformFixtures = []emitTestPerformFixture{
	{name: "abc", lsKeys: []uint16{125}, script: "a b c"},
	{name: "hello-world", lsKeys: []uint16{125}, script: "h e l l o sp w o r l d"},
	{name: "alt-shift-toggle", lsKeys: []uint16{56, 42}, script: "h e l l o sp w o r l d"},
	{name: "shift-held-at-end", lsKeys: []uint16{125}, script: "a sp ls+ b"},
	{name: "both-shifts-held", lsKeys: []uint16{125}, script: "a sp ls+ rs+ b"},
	{name: "shift-only-tail", lsKeys: []uint16{125}, script: "a ls+ sp ls- ls+ ls- ls+ ls-", process: true},
	{name: "custom-key-shift-held", lsKeys: []uint16{125}, convKey: testKeyPause, script: "ls+ a pause+ pause-", process: true},
}

func emitTestPerformRecord(t *testing.T, fx emitTestPerformFixture, path emitTestPath, reverse bool, action Action) string {
	t.Helper()
	cfg := &Config{LayoutSwitchKey: fx.lsKeys, ConvertKey: fx.convKey, ReverseMode: reverse}
	s, output := emitTestSwitcher(t, path, cfg)
	for _, ev := range emitTestSeq(t, fx.script) {
		s.converter.Push(ev.Code, ev.Value)
	}
	if fx.process {
		s.converter.Process()
	}
	s.performConversion(action)
	effective := action
	if reverse {
		effective = map[Action]Action{ActionConvertWord: ActionConvertAll, ActionConvertAll: ActionConvertWord}[action]
	}
	emitted := emitTestCanonicalEmitted(emitTestCaptured(t, output))
	return fmt.Sprintf("%s | undo=%t buf=%d", emitTestFormat(emitted), s.converter.CanUndo(effective), s.converter.BufferLen())
}

func emitTestActionName(action Action) string {
	switch action {
	case ActionNone:
		return "none"
	case ActionConvertWord:
		return "word"
	case ActionConvertAll:
		return "all"
	case ActionDoubleShiftNoText:
		return "double-shift-no-text"
	case ActionDoubleShiftHeldNoText:
		return "double-shift-held-no-text"
	}
	return fmt.Sprintf("action(%d)", int(action))
}

// emitTestPerformRecords runs every performConversion fixture through every
// switch path. ReverseMode is exercised on the blind path only.
func emitTestPerformRecords(t *testing.T) map[string]string {
	t.Helper()
	got := make(map[string]string)
	paths := []emitTestPath{emitTestPathBlind, emitTestPathWaylandOK, emitTestPathWaylandFailed, emitTestPathX11Unavailable}
	for _, fx := range emitTestPerformFixtures {
		for _, path := range paths {
			for _, reverse := range []bool{false, true} {
				if reverse && path != emitTestPathBlind {
					continue
				}
				for _, action := range []Action{ActionConvertWord, ActionConvertAll} {
					key := fmt.Sprintf("%s/%s/reverse=%t/%s", fx.name, path, reverse, emitTestActionName(action))
					got[key] = emitTestPerformRecord(t, fx, path, reverse, action)
				}
			}
		}
	}
	return got
}

// emitTestKeyScripts drive processKeyEvent end to end (blind path).
var emitTestKeyScripts = []struct {
	name    string
	lsKeys  []uint16
	convKey uint16
	reverse bool
	script  string
}{
	{name: "double-shift-word", lsKeys: []uint16{125}, script: "g* h* b* d* t* n* ls* ls*"},
	{name: "double-shift-word-twice-undo", lsKeys: []uint16{125}, script: "g* h* b* ls* ls* ls* ls*"},
	{name: "double-shift-word-after-space", lsKeys: []uint16{125}, script: "a* b* sp* c* d* sp* ls* ls*"},
	{name: "held-shift-phrase", lsKeys: []uint16{125}, script: "a* b* sp* c* d* ls+ rs* rs* ls-"},
	{name: "shift-only-tail", lsKeys: []uint16{125}, script: "a* ls+ sp* ls- ls* ls*"},
	{name: "shifted-capital", lsKeys: []uint16{56, 42}, script: "ls+ g* ls- h* b* d* t* n* rs* rs*"},
	{name: "reverse-double-shift", lsKeys: []uint16{125}, reverse: true, script: "a* b* sp* c* d* ls* ls*"},
	{name: "custom-key-word", lsKeys: []uint16{125}, convKey: testKeyPause, script: "a* sp* b* c* pause*"},
	{name: "custom-key-phrase", lsKeys: []uint16{125}, convKey: testKeyPause, script: "a* sp* b* c* ls+ pause* ls-"},
	{name: "backspace-edit", lsKeys: []uint16{125}, script: "a* b* bs* c* ls* ls*"},
}

func emitTestKeyScriptRecords(t *testing.T) map[string]string {
	t.Helper()
	got := make(map[string]string)
	for _, sc := range emitTestKeyScripts {
		cfg := &Config{LayoutSwitchKey: sc.lsKeys, ConvertKey: sc.convKey, ReverseMode: sc.reverse}
		s, output := emitTestSwitcher(t, emitTestPathBlind, cfg)
		// Canonicalise each conversion separately: every one starts with
		// shift releases in map order.
		var emitted []KeyEvent
		seen := 0
		for _, ev := range emitTestSeq(t, sc.script) {
			s.processKeyEvent(&InputEvent{Type: EV_KEY, Code: ev.Code, Value: ev.Value})
			if all := emitTestCaptured(t, output); len(all) > seen {
				emitted = append(emitted, emitTestCanonicalEmitted(all[seen:])...)
				seen = len(all)
			}
		}
		if emitted == nil {
			emitted = []KeyEvent{}
		}
		got["keys/"+sc.name] = fmt.Sprintf("%s | buf=%d", emitTestFormat(emitted), s.converter.BufferLen())
	}
	return got
}

func emitTestCompareGolden(t *testing.T, got, want map[string]string) {
	t.Helper()
	for key, w := range want {
		g, ok := got[key]
		if !ok {
			t.Errorf("%s: no record produced", key)
			continue
		}
		if g != w {
			t.Errorf("%s:\n got  %s\n want %s", key, g, w)
		}
	}
	for key := range got {
		if _, ok := want[key]; !ok {
			t.Errorf("%s: record has no golden value", key)
		}
	}
}

// TestPerformConversionGolden pins the exact events performConversion emits
// on each upstream switch path; the values were captured from the v0.8.0 code
// before performConversion was split (gswitch-auto WP0).
func TestPerformConversionGolden(t *testing.T) {
	emitTestCompareGolden(t, emitTestPerformRecords(t), emitTestPerformGolden)
}

// TestProcessKeyEventConversionGolden pins end-to-end manual gestures.
func TestProcessKeyEventConversionGolden(t *testing.T) {
	emitTestCompareGolden(t, emitTestKeyScriptRecords(t), emitTestKeyScriptGolden)
}

var emitTestPerformGolden = map[string]string{
	"abc/blind/reverse=false/all":                                 "42- 54- 125+ 125- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 48+ 48- 46+ 46- | undo=true buf=3",
	"abc/blind/reverse=false/word":                                "42- 54- 125+ 125- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 48+ 48- 46+ 46- | undo=true buf=3",
	"abc/blind/reverse=true/all":                                  "42- 54- 125+ 125- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 48+ 48- 46+ 46- | undo=true buf=3",
	"abc/blind/reverse=true/word":                                 "42- 54- 125+ 125- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 48+ 48- 46+ 46- | undo=true buf=3",
	"abc/wayland-confirmed/reverse=false/all":                     "42- 54- 125+ 125- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 48+ 48- 46+ 46- | undo=true buf=3",
	"abc/wayland-confirmed/reverse=false/word":                    "42- 54- 125+ 125- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 48+ 48- 46+ 46- | undo=true buf=3",
	"abc/wayland-read-error/reverse=false/all":                    "42- 54- 125+ 125- | undo=false buf=3",
	"abc/wayland-read-error/reverse=false/word":                   "42- 54- 125+ 125- | undo=false buf=3",
	"abc/x11-unavailable/reverse=false/all":                       "42- 54- | undo=false buf=3",
	"abc/x11-unavailable/reverse=false/word":                      "42- 54- | undo=false buf=3",
	"alt-shift-toggle/blind/reverse=false/all":                    "42- 54- 56+ 42+ 42- 56- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 35+ 35- 18+ 18- 38+ 38- 38+ 38- 24+ 24- 57+ 57- 17+ 17- 24+ 24- 19+ 19- 38+ 38- 32+ 32- | undo=true buf=11",
	"alt-shift-toggle/blind/reverse=false/word":                   "42- 54- 56+ 42+ 42- 56- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 17+ 17- 24+ 24- 19+ 19- 38+ 38- 32+ 32- | undo=true buf=11",
	"alt-shift-toggle/blind/reverse=true/all":                     "42- 54- 56+ 42+ 42- 56- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 17+ 17- 24+ 24- 19+ 19- 38+ 38- 32+ 32- | undo=true buf=11",
	"alt-shift-toggle/blind/reverse=true/word":                    "42- 54- 56+ 42+ 42- 56- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 35+ 35- 18+ 18- 38+ 38- 38+ 38- 24+ 24- 57+ 57- 17+ 17- 24+ 24- 19+ 19- 38+ 38- 32+ 32- | undo=true buf=11",
	"alt-shift-toggle/wayland-confirmed/reverse=false/all":        "42- 54- 56+ 42+ 42- 56- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 35+ 35- 18+ 18- 38+ 38- 38+ 38- 24+ 24- 57+ 57- 17+ 17- 24+ 24- 19+ 19- 38+ 38- 32+ 32- | undo=true buf=11",
	"alt-shift-toggle/wayland-confirmed/reverse=false/word":       "42- 54- 56+ 42+ 42- 56- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 17+ 17- 24+ 24- 19+ 19- 38+ 38- 32+ 32- | undo=true buf=11",
	"alt-shift-toggle/wayland-read-error/reverse=false/all":       "42- 54- 56+ 42+ 42- 56- | undo=false buf=11",
	"alt-shift-toggle/wayland-read-error/reverse=false/word":      "42- 54- 56+ 42+ 42- 56- | undo=false buf=11",
	"alt-shift-toggle/x11-unavailable/reverse=false/all":          "42- 54- | undo=false buf=11",
	"alt-shift-toggle/x11-unavailable/reverse=false/word":         "42- 54- | undo=false buf=11",
	"both-shifts-held/blind/reverse=false/all":                    "42- 54- 125+ 125- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 57+ 57- 42+ 54+ 48+ 48- 42- 54- | undo=true buf=5",
	"both-shifts-held/blind/reverse=false/word":                   "42- 54- 125+ 125- 14+ 14- 42+ 54+ 48+ 48- 42- 54- | undo=true buf=5",
	"both-shifts-held/blind/reverse=true/all":                     "42- 54- 125+ 125- 14+ 14- 42+ 54+ 48+ 48- 42- 54- | undo=true buf=5",
	"both-shifts-held/blind/reverse=true/word":                    "42- 54- 125+ 125- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 57+ 57- 42+ 54+ 48+ 48- 42- 54- | undo=true buf=5",
	"both-shifts-held/wayland-confirmed/reverse=false/all":        "42- 54- 125+ 125- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 57+ 57- 42+ 54+ 48+ 48- 42- 54- | undo=true buf=5",
	"both-shifts-held/wayland-confirmed/reverse=false/word":       "42- 54- 125+ 125- 14+ 14- 42+ 54+ 48+ 48- 42- 54- | undo=true buf=5",
	"both-shifts-held/wayland-read-error/reverse=false/all":       "42- 54- 125+ 125- | undo=false buf=5",
	"both-shifts-held/wayland-read-error/reverse=false/word":      "42- 54- 125+ 125- | undo=false buf=5",
	"both-shifts-held/x11-unavailable/reverse=false/all":          "42- 54- | undo=false buf=5",
	"both-shifts-held/x11-unavailable/reverse=false/word":         "42- 54- | undo=false buf=5",
	"custom-key-shift-held/blind/reverse=false/all":               "42- 54- 125+ 125- 14+ 14- 42+ 30+ 30- 42- | undo=true buf=2",
	"custom-key-shift-held/blind/reverse=false/word":              "42- 54- 125+ 125- 14+ 14- 42+ 30+ 30- 42- | undo=true buf=2",
	"custom-key-shift-held/blind/reverse=true/all":                "42- 54- 125+ 125- 14+ 14- 42+ 30+ 30- 42- | undo=true buf=2",
	"custom-key-shift-held/blind/reverse=true/word":               "42- 54- 125+ 125- 14+ 14- 42+ 30+ 30- 42- | undo=true buf=2",
	"custom-key-shift-held/wayland-confirmed/reverse=false/all":   "42- 54- 125+ 125- 14+ 14- 42+ 30+ 30- 42- | undo=true buf=2",
	"custom-key-shift-held/wayland-confirmed/reverse=false/word":  "42- 54- 125+ 125- 14+ 14- 42+ 30+ 30- 42- | undo=true buf=2",
	"custom-key-shift-held/wayland-read-error/reverse=false/all":  "42- 54- 125+ 125- | undo=false buf=2",
	"custom-key-shift-held/wayland-read-error/reverse=false/word": "42- 54- 125+ 125- | undo=false buf=2",
	"custom-key-shift-held/x11-unavailable/reverse=false/all":     "42- 54- | undo=false buf=2",
	"custom-key-shift-held/x11-unavailable/reverse=false/word":    "42- 54- | undo=false buf=2",
	"hello-world/blind/reverse=false/all":                         "42- 54- 125+ 125- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 35+ 35- 18+ 18- 38+ 38- 38+ 38- 24+ 24- 57+ 57- 17+ 17- 24+ 24- 19+ 19- 38+ 38- 32+ 32- | undo=true buf=11",
	"hello-world/blind/reverse=false/word":                        "42- 54- 125+ 125- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 17+ 17- 24+ 24- 19+ 19- 38+ 38- 32+ 32- | undo=true buf=11",
	"hello-world/blind/reverse=true/all":                          "42- 54- 125+ 125- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 17+ 17- 24+ 24- 19+ 19- 38+ 38- 32+ 32- | undo=true buf=11",
	"hello-world/blind/reverse=true/word":                         "42- 54- 125+ 125- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 35+ 35- 18+ 18- 38+ 38- 38+ 38- 24+ 24- 57+ 57- 17+ 17- 24+ 24- 19+ 19- 38+ 38- 32+ 32- | undo=true buf=11",
	"hello-world/wayland-confirmed/reverse=false/all":             "42- 54- 125+ 125- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 35+ 35- 18+ 18- 38+ 38- 38+ 38- 24+ 24- 57+ 57- 17+ 17- 24+ 24- 19+ 19- 38+ 38- 32+ 32- | undo=true buf=11",
	"hello-world/wayland-confirmed/reverse=false/word":            "42- 54- 125+ 125- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 17+ 17- 24+ 24- 19+ 19- 38+ 38- 32+ 32- | undo=true buf=11",
	"hello-world/wayland-read-error/reverse=false/all":            "42- 54- 125+ 125- | undo=false buf=11",
	"hello-world/wayland-read-error/reverse=false/word":           "42- 54- 125+ 125- | undo=false buf=11",
	"hello-world/x11-unavailable/reverse=false/all":               "42- 54- | undo=false buf=11",
	"hello-world/x11-unavailable/reverse=false/word":              "42- 54- | undo=false buf=11",
	"shift-held-at-end/blind/reverse=false/all":                   "42- 54- 125+ 125- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 57+ 57- 42+ 48+ 48- 42- | undo=true buf=4",
	"shift-held-at-end/blind/reverse=false/word":                  "42- 54- 125+ 125- 14+ 14- 42+ 48+ 48- 42- | undo=true buf=4",
	"shift-held-at-end/blind/reverse=true/all":                    "42- 54- 125+ 125- 14+ 14- 42+ 48+ 48- 42- | undo=true buf=4",
	"shift-held-at-end/blind/reverse=true/word":                   "42- 54- 125+ 125- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 57+ 57- 42+ 48+ 48- 42- | undo=true buf=4",
	"shift-held-at-end/wayland-confirmed/reverse=false/all":       "42- 54- 125+ 125- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 57+ 57- 42+ 48+ 48- 42- | undo=true buf=4",
	"shift-held-at-end/wayland-confirmed/reverse=false/word":      "42- 54- 125+ 125- 14+ 14- 42+ 48+ 48- 42- | undo=true buf=4",
	"shift-held-at-end/wayland-read-error/reverse=false/all":      "42- 54- 125+ 125- | undo=false buf=4",
	"shift-held-at-end/wayland-read-error/reverse=false/word":     "42- 54- 125+ 125- | undo=false buf=4",
	"shift-held-at-end/x11-unavailable/reverse=false/all":         "42- 54- | undo=false buf=4",
	"shift-held-at-end/x11-unavailable/reverse=false/word":        "42- 54- | undo=false buf=4",
	"shift-only-tail/blind/reverse=false/all":                     "42- 54- 125+ 125- 14+ 14- 14+ 14- 30+ 30- 42+ 57+ 57- 42- | undo=true buf=4",
	"shift-only-tail/blind/reverse=false/word":                    "42- 54- 125+ 125- 42- | undo=true buf=4",
	"shift-only-tail/blind/reverse=true/all":                      "42- 54- 125+ 125- 42- | undo=true buf=4",
	"shift-only-tail/blind/reverse=true/word":                     "42- 54- 125+ 125- 14+ 14- 14+ 14- 30+ 30- 42+ 57+ 57- 42- | undo=true buf=4",
	"shift-only-tail/wayland-confirmed/reverse=false/all":         "42- 54- 125+ 125- 14+ 14- 14+ 14- 30+ 30- 42+ 57+ 57- 42- | undo=true buf=4",
	"shift-only-tail/wayland-confirmed/reverse=false/word":        "42- 54- 125+ 125- 42- | undo=true buf=4",
	"shift-only-tail/wayland-read-error/reverse=false/all":        "42- 54- 125+ 125- | undo=false buf=4",
	"shift-only-tail/wayland-read-error/reverse=false/word":       "42- 54- 125+ 125- | undo=false buf=4",
	"shift-only-tail/x11-unavailable/reverse=false/all":           "42- 54- | undo=false buf=4",
	"shift-only-tail/x11-unavailable/reverse=false/word":          "42- 54- | undo=false buf=4",
}

var emitTestKeyScriptGolden = map[string]string{
	"keys/backspace-edit":                "42- 54- 125+ 125- 14+ 14- 14+ 14- 30+ 30- 46+ 46- | buf=2",
	"keys/custom-key-phrase":             "42- 54- 125+ 125- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 57+ 57- 48+ 48- 46+ 46- | buf=4",
	"keys/custom-key-word":               "42- 54- 125+ 125- 14+ 14- 14+ 14- 48+ 48- 46+ 46- | buf=4",
	"keys/double-shift-word":             "42- 54- 125+ 125- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 34+ 34- 35+ 35- 48+ 48- 32+ 32- 20+ 20- 49+ 49- | buf=6",
	"keys/double-shift-word-after-space": "42- 54- 125+ 125- 14+ 14- 14+ 14- 14+ 14- 46+ 46- 32+ 32- 57+ 57- | buf=6",
	"keys/double-shift-word-twice-undo":  "42- 54- 125+ 125- 14+ 14- 14+ 14- 14+ 14- 34+ 34- 35+ 35- 48+ 48- 42- 54- 125+ 125- 14+ 14- 14+ 14- 14+ 14- 34+ 34- 35+ 35- 48+ 48- | buf=3",
	"keys/held-shift-phrase":             "42- 54- 125+ 125- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 48+ 48- 57+ 57- 46+ 46- 32+ 32- | buf=5",
	"keys/reverse-double-shift":          "42- 54- 125+ 125- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 48+ 48- 57+ 57- 46+ 46- 32+ 32- | buf=5",
	"keys/shift-only-tail":               "42- 54- 125+ 125- 42- | buf=4",
	"keys/shifted-capital":               "42- 54- 56+ 42+ 42- 56- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 42+ 34+ 34- 42- 35+ 35- 48+ 48- 32+ 32- 20+ 20- 49+ 49- | buf=8",
}
