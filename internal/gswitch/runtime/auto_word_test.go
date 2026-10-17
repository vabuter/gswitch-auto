package runtime

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// Key rows in scancode order 2..13, 16..27, 30..41, 43..53 (47 keys), as in
// punto-rs cli/src/layout/keymap.rs key_char (ru = XKB ru(winkeys)).
const (
	wordTestRowEN      = "1234567890-=qwertyuiop[]asdfghjkl;'`\\zxcvbnm,./"
	wordTestRowENShift = "!@#$%^&*()_+QWERTYUIOP{}ASDFGHJKL:\"~|ZXCVBNM<>?"
	wordTestRowRU      = "1234567890-=йцукенгшщзхъфывапролджэё\\ячсмитьбю."
	wordTestRowRUShift = "!\"№;%:?*()_+ЙЦУКЕНГШЩЗХЪФЫВАПРОЛДЖЭЁ/ЯЧСМИТЬБЮ,"
)

// wordTestRowCodes returns the 47 scancodes of the punto-rs key rows.
func wordTestRowCodes() []uint16 {
	codes := make([]uint16, 0, 47)
	for _, r := range [][2]uint16{{2, 13}, {16, 27}, {30, 41}, {43, 53}} {
		for c := r[0]; c <= r[1]; c++ {
			codes = append(codes, c)
		}
	}
	return codes
}

// wordTestRowLayout builds a fixture LayoutInfo from a pair of key rows.
func wordTestRowLayout(name, normal, shifted string) *LayoutInfo {
	l := &LayoutInfo{Name: name, KeyMap: make(map[string][]rune), Unmapped: make(map[string]bool)}
	n, s := []rune(normal), []rune(shifted)
	for i, code := range wordTestRowCodes() {
		key, _ := XKBKeyName(code)
		l.KeyMap[key] = []rune{n[i], s[i]}
	}
	return l
}

// wordTestLayouts returns fixture us and ru layouts (47 keys, no LSGT).
func wordTestLayouts() (us, ru *LayoutInfo) {
	return wordTestRowLayout("us", wordTestRowEN, wordTestRowENShift),
		wordTestRowLayout("ru", wordTestRowRU, wordTestRowRUShift)
}

// wordTestTokenCodes maps named tokens to scancodes.
var wordTestTokenCodes = map[string]uint16{
	"SP": KEY_SPACE, "EN": KEY_ENTER, "KPE": KEY_KPENTER,
	"KP1": 79, "KPAST": 55, "KPSLASH": 98, "KPDOT": 83, "KPMINUS": 74,
	"LSGT": KEY_102ND,
}

// wordTestSeq builds a converter buffer from a script. Tokens: a character of
// wordTestRowEN types that key (K_DOWN, as the converter buffers text keys);
// "^c" types it with LShift (L+ c L-); "L+"/"L-"/"R+"/"R-" press/release
// LShift/RShift; named tokens from wordTestTokenCodes; "c@up" adds a K_UP of
// key c (only a convert key could produce one).
func wordTestSeq(t testing.TB, script string) []KeyEvent {
	t.Helper()
	var events []KeyEvent
	for _, tok := range strings.Fields(script) {
		switch tok {
		case "L+":
			events = append(events, KeyEvent{Code: KEY_LEFTSHIFT, Value: K_DOWN})
			continue
		case "L-":
			events = append(events, KeyEvent{Code: KEY_LEFTSHIFT, Value: K_UP})
			continue
		case "R+":
			events = append(events, KeyEvent{Code: KEY_RIGHTSHIFT, Value: K_DOWN})
			continue
		case "R-":
			events = append(events, KeyEvent{Code: KEY_RIGHTSHIFT, Value: K_UP})
			continue
		}
		if code, ok := wordTestTokenCodes[tok]; ok {
			events = append(events, KeyEvent{Code: code, Value: K_DOWN})
			continue
		}
		value := int32(K_DOWN)
		if base, ok := strings.CutSuffix(tok, "@up"); ok {
			tok, value = base, K_UP
		}
		shift := false
		if rest, ok := strings.CutPrefix(tok, "^"); ok && rest != "" {
			tok, shift = rest, true
		}
		code, ok := wordTestCharCode(tok)
		if !ok {
			t.Fatalf("wordTestSeq: unknown token %q", tok)
		}
		if shift {
			events = append(events,
				KeyEvent{Code: KEY_LEFTSHIFT, Value: K_DOWN},
				KeyEvent{Code: code, Value: value},
				KeyEvent{Code: KEY_LEFTSHIFT, Value: K_UP})
		} else {
			events = append(events, KeyEvent{Code: code, Value: value})
		}
	}
	return events
}

// wordTestCharCode returns the scancode of a single character of wordTestRowEN.
func wordTestCharCode(tok string) (uint16, bool) {
	if utf8.RuneCountInString(tok) != 1 {
		return 0, false
	}
	idx := strings.IndexRune(wordTestRowEN, []rune(tok)[0])
	if idx < 0 {
		return 0, false
	}
	return wordTestRowCodes()[idx], true
}

// wordTestType types text on the given rows as a converter buffer: normal-row
// characters plainly, shifted-row characters with LShift around each key,
// ' ' as Space and '\n' as Enter.
func wordTestType(t testing.TB, text, normal, shifted string) []KeyEvent {
	t.Helper()
	codes := wordTestRowCodes()
	n, s := []rune(normal), []rune(shifted)
	var events []KeyEvent
	for _, r := range text {
		switch r {
		case ' ':
			events = append(events, KeyEvent{Code: KEY_SPACE, Value: K_DOWN})
			continue
		case '\n':
			events = append(events, KeyEvent{Code: KEY_ENTER, Value: K_DOWN})
			continue
		}
		found := false
		for i := range codes {
			if n[i] == r {
				events = append(events, KeyEvent{Code: codes[i], Value: K_DOWN})
				found = true
				break
			}
			if s[i] == r {
				events = append(events,
					KeyEvent{Code: KEY_LEFTSHIFT, Value: K_DOWN},
					KeyEvent{Code: codes[i], Value: K_DOWN},
					KeyEvent{Code: KEY_LEFTSHIFT, Value: K_UP})
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("wordTestType: %q is not on the rows", r)
		}
	}
	return events
}

func TestLastWordRange(t *testing.T) {
	tests := []struct {
		name       string
		script     string
		maxLetters int
		want       wordRange
		reason     string
	}{
		{name: "word at buffer start", script: "g h b d t n SP",
			want: wordRange{Start: 0, TextEnd: 6, End: 7, TextKeys: 6}},
		{name: "single trailing space after previous word", script: "f SP g h SP",
			want: wordRange{Start: 2, TextEnd: 4, End: 5, TextKeys: 2}},
		{name: "word after previous Enter", script: "a b EN g h b SP",
			want: wordRange{Start: 3, TextEnd: 6, End: 7, TextKeys: 3}},
		{name: "word after previous KPEnter", script: "a KPE g h SP",
			want: wordRange{Start: 2, TextEnd: 4, End: 5, TextKeys: 2}},
		{name: "Enter is a boundary, never inside the word", script: "g h EN b d SP",
			want: wordRange{Start: 3, TextEnd: 5, End: 6, TextKeys: 2}},
		{name: "trailing LShift K_UP after Space", script: "g h b d t n L+ SP L-",
			want: wordRange{Start: 0, TextEnd: 7, End: 8, TextKeys: 6}},
		{name: "two trailing shift releases", script: "g h L+ R+ SP L- R-",
			want: wordRange{Start: 0, TextEnd: 4, End: 5, TextKeys: 2}},
		{name: "shift events at word start belong to the word", script: "a SP L+ g L- h SP",
			want: wordRange{Start: 2, TextEnd: 6, End: 7, TextKeys: 2}},
		{name: "shift released before the word", script: "L+ a L- SP g h SP",
			want: wordRange{Start: 4, TextEnd: 6, End: 7, TextKeys: 2}},
		{name: "exactly max letters", script: "g h b d SP", maxLetters: 4,
			want: wordRange{Start: 0, TextEnd: 4, End: 5, TextKeys: 4}},
		{name: "two keys", script: "n f SP",
			want: wordRange{Start: 0, TextEnd: 2, End: 3, TextKeys: 2}},

		{name: "empty buffer", script: "", reason: "no-trailing-space"},
		{name: "no space", script: "g h", reason: "no-trailing-space"},
		{name: "ends with Enter", script: "g h EN", reason: "no-trailing-space"},
		{name: "trailing Shift K_DOWN", script: "g h SP L+", reason: "shift-held"},
		{name: "shift tapped after Space", script: "g h SP L+ L-", reason: "shift-held"},
		{name: "only Space", script: "SP", reason: "empty"},
		{name: "only shifts and Space", script: "L+ L- SP", reason: "empty"},
		{name: "double space", script: "a SP g h SP SP", reason: "double-space"},
		{name: "double space with shift between", script: "g h SP L+ L- SP", reason: "double-space"},
		{name: "word then Enter then Space", script: "g h EN SP", reason: "enter-boundary"},
		{name: "word then KPEnter then Space", script: "g h KPE SP", reason: "enter-boundary"},
		{name: "keypad digit inside", script: "g KP1 h SP", reason: "unrenderable-key",
			want: wordRange{Start: 0, TextEnd: 3, End: 4, TextKeys: 3}},
		{name: "KPASTERISK inside", script: "g KPAST h SP", reason: "unrenderable-key",
			want: wordRange{Start: 0, TextEnd: 3, End: 4, TextKeys: 3}},
		{name: "KPSLASH last", script: "g h KPSLASH SP", reason: "unrenderable-key",
			want: wordRange{Start: 0, TextEnd: 3, End: 4, TextKeys: 3}},
		{name: "KPDOT first", script: "KPDOT g h SP", reason: "unrenderable-key",
			want: wordRange{Start: 0, TextEnd: 3, End: 4, TextKeys: 3}},
		{name: "key release inside", script: "g g@up h SP", reason: "unrenderable-key",
			want: wordRange{Start: 0, TextEnd: 3, End: 4, TextKeys: 3}},
		{name: "shift held at start", script: "L+ a SP g h SP", reason: "shift-held-at-start",
			want: wordRange{Start: 3, TextEnd: 5, End: 6, TextKeys: 2}},
		{name: "right shift held at start", script: "R+ a SP g h SP R-", reason: "shift-held-at-start",
			want: wordRange{Start: 3, TextEnd: 5, End: 6, TextKeys: 2}},
		{name: "one key", script: "g SP", reason: "too-short",
			want: wordRange{Start: 0, TextEnd: 1, End: 2, TextKeys: 1}},
		{name: "one shifted key", script: "a SP ^g SP", reason: "too-short",
			want: wordRange{Start: 2, TextEnd: 5, End: 6, TextKeys: 1}},
		{name: "more than max", script: "g h b d t SP", maxLetters: 4, reason: "too-long",
			want: wordRange{Start: 0, TextEnd: 5, End: 6, TextKeys: 5}},
		{name: "default max exceeded", script: strings.Repeat("g ", 25) + "SP", reason: "too-long",
			want: wordRange{Start: 0, TextEnd: 25, End: 26, TextKeys: 25}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			maxLetters := tt.maxLetters
			if maxLetters == 0 {
				maxLetters = 24
			}
			got, reason, ok := lastWordRange(wordTestSeq(t, tt.script), maxLetters)
			if ok != (tt.reason == "") || reason != tt.reason {
				t.Fatalf("lastWordRange() reason=%q ok=%v, want reason=%q", reason, ok, tt.reason)
			}
			if got != tt.want {
				t.Fatalf("lastWordRange() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestLastWordRangeMatchesConvertRange checks that the range erases exactly
// the word and its Space: TextKeys+1 Backspace pairs.
func TestLastWordRangeMatchesConvertRange(t *testing.T) {
	c := NewConverter()
	c.LSKeys = []uint16{KEY_LEFTALT, KEY_LEFTSHIFT}
	for _, ev := range wordTestSeq(t, "f SP L+ g L- h b d t n SP") {
		c.Push(ev.Code, ev.Value)
	}
	r, reason, ok := lastWordRange(c.GetBuffer(), 24)
	if !ok {
		t.Fatalf("lastWordRange() failed: %s", reason)
	}
	backspaces := 0
	for _, ev := range c.ConvertRange(r.Start, r.End) {
		if ev.Code == KEY_BACKSPACE && ev.Value == K_DOWN {
			backspaces++
		}
	}
	if backspaces != r.TextKeys+1 {
		t.Fatalf("ConvertRange erases %d keys, want TextKeys+1 = %d", backspaces, r.TextKeys+1)
	}
}

func TestRenderWordFixture(t *testing.T) {
	tests := []struct {
		name        string
		script      string
		shownRU     bool // shown = ru, alt = us
		caps        bool
		shown, alt  string
		shiftedKeys int
		interior    bool
	}{
		{name: "plain", script: "g h b d t n", shown: "ghbdtn", alt: "привет"},
		{name: "reverse", script: "h e l l o", shownRU: true, shown: "руддщ", alt: "hello"},
		{name: "left shift pair", script: "L+ g L- h b d t n", shown: "Ghbdtn", alt: "Привет", shiftedKeys: 1},
		{name: "right shift pair", script: "R+ g R- h", shown: "Gh", alt: "Пр", shiftedKeys: 1},
		{name: "overlapping shifts", script: "L+ R+ g L- h R- b", shown: "GHb", alt: "ПРи", shiftedKeys: 2, interior: true},
		{name: "interior shift", script: "g L+ h L- b", shown: "gHb", alt: "пРи", shiftedKeys: 1, interior: true},
		{name: "caps alphabetic vs two-level", script: "g , h", caps: true, shown: "G,H", alt: "ПБР"},
		{name: "caps two-level digit", script: "1 g", caps: true, shown: "1G", alt: "1П"},
		{name: "caps and shift", script: "L+ g L- h", caps: true, shown: "gH", alt: "пР", shiftedKeys: 1},
		{name: "caps and shift on two-level", script: "g L+ 1 L-", caps: true, shown: "G!", alt: "П!"},
		{name: "caps on ru two-level key", script: "h /", caps: true, shownRU: true, shown: "Р.", alt: "H/"},
		{name: "digits and punctuation verbatim", script: "1 2 - = [", shown: "12-=[", alt: "12-=х"},
		{name: "shifted punctuation", script: "^1 ^2 ^3 ^4 ^/", shown: "!@#$?", alt: "!\"№;,"},
		{name: "backquote and backslash", script: "` \\", shown: "`\\", alt: "ё\\"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			us, ru := wordTestLayouts()
			shown, alt := us, ru
			if tt.shownRU {
				shown, alt = ru, us
			}
			buf := wordTestSeq(t, tt.script)
			w, err := renderWord(buf, 0, len(buf), shown, alt, tt.caps)
			if err != nil {
				t.Fatalf("renderWord() error = %v", err)
			}
			if w.Shown != tt.shown || w.Alt != tt.alt {
				t.Fatalf("renderWord() = %q/%q, want %q/%q", w.Shown, w.Alt, tt.shown, tt.alt)
			}
			if n := utf8.RuneCountInString(w.Shown); n != w.Keys || utf8.RuneCountInString(w.Alt) != w.Keys {
				t.Fatalf("rune counts %d/%d, Keys %d", n, utf8.RuneCountInString(w.Alt), w.Keys)
			}
			if w.ShiftedKeys != tt.shiftedKeys || w.InteriorShift != tt.interior {
				t.Fatalf("ShiftedKeys=%d InteriorShift=%v, want %d/%v", w.ShiftedKeys, w.InteriorShift, tt.shiftedKeys, tt.interior)
			}
			if w.CapsLock != tt.caps {
				t.Fatalf("CapsLock = %v, want %v", w.CapsLock, tt.caps)
			}
		})
	}
}

func TestRenderWordUnrenderable(t *testing.T) {
	tests := []struct {
		name   string
		script string
		mutate func(us, ru *LayoutInfo)
	}{
		{name: "unmapped in alt", script: "g h", mutate: func(_, ru *LayoutInfo) { ru.Unmapped["AC05"] = true }},
		{name: "unmapped in shown", script: "g h", mutate: func(us, _ *LayoutInfo) { us.Unmapped["AC06"] = true }},
		{name: "name missing in alt", script: "q w", mutate: func(_, ru *LayoutInfo) { delete(ru.KeyMap, "AD01") }},
		{name: "name missing in shown", script: "q w", mutate: func(us, _ *LayoutInfo) { delete(us.KeyMap, "AD02") }},
		{name: "empty rune list", script: "q w", mutate: func(us, _ *LayoutInfo) { us.KeyMap["AD02"] = nil }},
		{name: "level missing", script: "^w", mutate: func(us, _ *LayoutInfo) { us.KeyMap["AD02"] = []rune{'w'} }},
		{name: "LSGT missing in both fixtures", script: "g LSGT"},
		{name: "keypad key", script: "g KP1"},
		{name: "Space has no XKB name", script: "g h SP"},
		{name: "Enter has no XKB name", script: "g EN"},
		{name: "key release", script: "g g@up"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			us, ru := wordTestLayouts()
			if tt.mutate != nil {
				tt.mutate(us, ru)
			}
			buf := wordTestSeq(t, tt.script)
			if _, err := renderWord(buf, 0, len(buf), us, ru, false); !errors.Is(err, ErrUnrenderable) {
				t.Fatalf("renderWord() error = %v, want ErrUnrenderable", err)
			}
		})
	}
}

func TestRenderWordLevelPresentWithoutShift(t *testing.T) {
	us, ru := wordTestLayouts()
	us.KeyMap["AD02"] = []rune{'w'}
	buf := wordTestSeq(t, "w q")
	w, err := renderWord(buf, 0, len(buf), us, ru, false)
	if err != nil || w.Shown != "wq" || w.Alt != "цй" {
		t.Fatalf("renderWord() = %q/%q, %v; want wq/цй", w.Shown, w.Alt, err)
	}
}

func TestRenderWordBoundsAndNilLayouts(t *testing.T) {
	us, ru := wordTestLayouts()
	buf := wordTestSeq(t, "g h")
	for _, tc := range []struct {
		name       string
		start, end int
		shown, alt *LayoutInfo
	}{
		{"negative start", -1, 2, us, ru},
		{"end past buffer", 0, 3, us, ru},
		{"start after end", 2, 1, us, ru},
		{"nil shown", 0, 2, nil, ru},
		{"nil alt", 0, 2, us, nil},
	} {
		if _, err := renderWord(buf, tc.start, tc.end, tc.shown, tc.alt, false); !errors.Is(err, ErrUnrenderable) {
			t.Errorf("%s: error = %v, want ErrUnrenderable", tc.name, err)
		}
	}
	w, err := renderWord(buf, 1, 1, us, ru, false)
	if err != nil || w.Keys != 0 || w.CoreKeys != 0 || w.Shown != "" {
		t.Fatalf("empty range: %+v, %v", w, err)
	}
	// Layouts built outside LoadLayout may have a nil Unmapped map.
	us.Unmapped, ru.Unmapped = nil, nil
	if w, err := renderWord(buf, 0, 2, us, ru, false); err != nil || w.Shown != "gh" {
		t.Fatalf("nil Unmapped: %+v, %v", w, err)
	}
}

// TestRenderWordSpaceBound documents that callers render [Start, TextEnd):
// the trailing Space has no XKB name.
func TestRenderWordSpaceBound(t *testing.T) {
	us, ru := wordTestLayouts()
	buf := wordTestSeq(t, "f SP g h b d t n SP")
	r, reason, ok := lastWordRange(buf, 24)
	if !ok {
		t.Fatalf("lastWordRange() failed: %s", reason)
	}
	w, err := renderWord(buf, r.Start, r.TextEnd, us, ru, false)
	if err != nil || w.Shown != "ghbdtn" || w.Alt != "привет" {
		t.Fatalf("renderWord(Start, TextEnd) = %q/%q, %v", w.Shown, w.Alt, err)
	}
	if _, err := renderWord(buf, r.Start, r.End, us, ru, false); !errors.Is(err, ErrUnrenderable) {
		t.Fatalf("renderWord(Start, End) error = %v, want ErrUnrenderable", err)
	}
}

// TestRenderWordShiftTrackedInsideRange checks that the shift state starts
// released at start, exactly like the replay, even when the buffer before
// start left a shift down.
func TestRenderWordShiftTrackedInsideRange(t *testing.T) {
	us, ru := wordTestLayouts()
	buf := wordTestSeq(t, "L+ a SP g h")
	w, err := renderWord(buf, 3, len(buf), us, ru, false)
	if err != nil || w.Shown != "gh" || w.ShiftedKeys != 0 {
		t.Fatalf("renderWord() = %+v, %v; want unshifted gh", w, err)
	}
}

func TestRenderWordCore(t *testing.T) {
	tests := []struct {
		name                         string
		text                         string // typed on the us rows
		coreStart, coreEnd, coreKeys int
		shiftedKeys                  int
	}{
		{name: "trailing question mark", text: "ghbdtn?", coreStart: 0, coreEnd: 6, coreKeys: 6},
		{name: "leading quote dropped", text: "\"Ghbdtn", coreStart: 1, coreEnd: 7, coreKeys: 6, shiftedKeys: 1},
		{name: "interior punctuation kept", text: "k.,jdm", coreStart: 0, coreEnd: 6, coreKeys: 6},
		{name: "trailing exclamation", text: "Ghbdtn!", coreStart: 0, coreEnd: 6, coreKeys: 6, shiftedKeys: 1},
		{name: "trailing comma key letter in alt only", text: "ghbdtn,", coreStart: 0, coreEnd: 6, coreKeys: 6},
		{name: "both edges", text: "(ghbdtn)", coreStart: 1, coreEnd: 7, coreKeys: 6},
		{name: "no letters in both", text: "/1-", coreStart: 0, coreEnd: 0, coreKeys: 0},
		{name: "punctuation that is a letter on one side only", text: ",.;", coreStart: 0, coreEnd: 0, coreKeys: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			us, ru := wordTestLayouts()
			buf := wordTestType(t, tt.text, wordTestRowEN, wordTestRowENShift)
			w, err := renderWord(buf, 0, len(buf), us, ru, false)
			if err != nil {
				t.Fatalf("renderWord() error = %v", err)
			}
			if w.Shown != tt.text {
				t.Fatalf("Shown = %q, want %q", w.Shown, tt.text)
			}
			if w.CoreStart != tt.coreStart || w.CoreEnd != tt.coreEnd || w.CoreKeys != tt.coreKeys {
				t.Fatalf("core = [%d,%d) keys %d, want [%d,%d) keys %d",
					w.CoreStart, w.CoreEnd, w.CoreKeys, tt.coreStart, tt.coreEnd, tt.coreKeys)
			}
			if w.ShiftedKeys != tt.shiftedKeys {
				t.Fatalf("ShiftedKeys = %d, want %d", w.ShiftedKeys, tt.shiftedKeys)
			}
		})
	}
}

// wordTestShiftShapeCase is shared by the fixture and the real XKB tests.
type wordTestShiftShapeCase struct {
	name    string
	text    string
	onRU    bool // text is typed on the ru rows and ru is the active layout
	caps    bool
	vetoed  bool
	wantAlt string
}

var wordTestShiftShapeCases = []wordTestShiftShapeCase{
	{name: "acronym", text: "GTK", vetoed: true, wantAlt: "ПЕЛ"},
	{name: "acronym with comma", text: "GTK,", vetoed: true, wantAlt: "ПЕЛб"},
	{name: "camelCase", text: "getValue", vetoed: true, wantAlt: "пуеМфдгу"},
	{name: "iPhone", text: "iPhone", vetoed: true, wantAlt: "шЗрщту"},
	{name: "two shifted letters", text: "AB", vetoed: true, wantAlt: "ФИ"},
	{name: "capitalised ru", text: "Привет", onRU: true, wantAlt: "Ghbdtn"},
	{name: "capitalised us", text: "Ghbdtn", wantAlt: "Привет"},
	{name: "question mark is ru comma", text: "ghbdtn?", wantAlt: "привет,"},
	{name: "exclamation us", text: "Ghbdtn!", wantAlt: "Привет!"},
	{name: "exclamation ru", text: "руддщ!", onRU: true, wantAlt: "hello!"},
	{name: "hello? typed on ru", text: "руддщ,", onRU: true, wantAlt: "hello?"},
	{name: "Hello: typed on ru", text: "РуддщЖ", onRU: true, wantAlt: "Hello:"},
	{name: "single shifted letter", text: "A", wantAlt: "Ф"},
	{name: "caps lock does not veto", text: "GHBDTN", caps: true, wantAlt: "ПРИВЕТ"},
	{name: "lowercase", text: "ghbdtn", wantAlt: "привет"},
}

// wordTestRunShiftShape renders each case's keys with the given layouts and
// checks the alt text and the veto.
func wordTestRunShiftShape(t *testing.T, us, ru *LayoutInfo) {
	t.Helper()
	for _, tt := range wordTestShiftShapeCases {
		t.Run(tt.name, func(t *testing.T) {
			var buf []KeyEvent
			shown, alt := us, ru
			switch {
			case tt.onRU:
				buf = wordTestType(t, tt.text, wordTestRowRU, wordTestRowRUShift)
				shown, alt = ru, us
			case tt.caps:
				// CapsLock on: the letters are typed without Shift.
				buf = wordTestType(t, strings.ToLower(tt.text), wordTestRowEN, wordTestRowENShift)
			default:
				buf = wordTestType(t, tt.text, wordTestRowEN, wordTestRowENShift)
			}
			w, err := renderWord(buf, 0, len(buf), shown, alt, tt.caps)
			if err != nil {
				t.Fatalf("renderWord() error = %v", err)
			}
			if w.Shown != tt.text || w.Alt != tt.wantAlt {
				t.Fatalf("renderWord() = %q/%q, want %q/%q", w.Shown, w.Alt, tt.text, tt.wantAlt)
			}
			if got := shiftShapeVeto(w); got != tt.vetoed {
				t.Fatalf("shiftShapeVeto(%+v) = %v, want %v", w, got, tt.vetoed)
			}
		})
	}
}

func TestShiftShapeVetoFixture(t *testing.T) {
	us, ru := wordTestLayouts()
	wordTestRunShiftShape(t, us, ru)
}

func TestShiftShapeVetoNoCore(t *testing.T) {
	if shiftShapeVeto(renderedWord{ShiftedKeys: 0, InteriorShift: true}) {
		t.Fatal("a word without core keys must not be vetoed")
	}
	if !shiftShapeVeto(renderedWord{CoreKeys: 3, ShiftedKeys: 1, InteriorShift: true}) {
		t.Fatal("interior shift must veto")
	}
	if shiftShapeVeto(renderedWord{CoreKeys: 3, ShiftedKeys: 2}) {
		t.Fatal("two of three shifted keys without interior shift must not veto")
	}
}

func TestLexicalVeto(t *testing.T) {
	tests := []struct {
		shown  string
		reason string
	}{
		{"abc1", "digit"},
		{"1abc", "digit"},
		{"a1b", "digit"},
		{"/x", "leading-slash"},
		{"/usr", "leading-slash"},
		{"~x", "leading-tilde"},
		{"-x", "leading-dash"},
		{"--help", "leading-dash"},
		{"http://x", "url"},
		{"a@b", "at"},
		{"@user", "at"},
		{`a\b`, "backslash"},
		{`\`, "backslash"},

		{"k.,jdm", ""},
		{"ghbdtn/", ""},
		{"ghbdtn", ""},
		{"привет", ""},
		{"a-b", ""},
		{"a~b", ""},
		{"a/b", ""},
		{"ghbdtn?", ""},
		{"", ""},
	}
	for _, tt := range tests {
		reason, vetoed := lexicalVeto(tt.shown)
		if vetoed != (tt.reason != "") || reason != tt.reason {
			t.Errorf("lexicalVeto(%q) = %q, %v; want %q", tt.shown, reason, vetoed, tt.reason)
		}
	}
}
