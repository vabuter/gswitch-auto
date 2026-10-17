package runtime

import (
	"errors"
	"os"
	"testing"
)

// renderTestRealLayouts loads us and ru from the installed XKB data, skipping
// the test when it is not available.
func renderTestRealLayouts(t *testing.T) (us, ru *LayoutInfo) {
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

// TestRenderXKBRowsMatchPunto checks that the installed us and ru(winkeys)
// keymaps give the punto-rs keymap.rs rows for all 47 keys at both levels,
// with no unmapped keysym.
func TestRenderXKBRowsMatchPunto(t *testing.T) {
	us, ru := renderTestRealLayouts(t)
	for _, tc := range []struct {
		layout          *LayoutInfo
		normal, shifted []rune
	}{
		{us, []rune(wordTestRowEN), []rune(wordTestRowENShift)},
		{ru, []rune(wordTestRowRU), []rune(wordTestRowRUShift)},
	} {
		for i, code := range wordTestRowCodes() {
			name, ok := XKBKeyName(code)
			if !ok {
				t.Fatalf("XKBKeyName(%d) not ok", code)
			}
			if tc.layout.Unmapped[name] {
				t.Errorf("%s %s: unexpectedly unmapped", tc.layout.Name, name)
			}
			runes := tc.layout.KeyMap[name]
			if len(runes) < 2 {
				t.Errorf("%s %s: KeyMap = %q, want two levels", tc.layout.Name, name, string(runes))
				continue
			}
			if runes[0] != tc.normal[i] || runes[1] != tc.shifted[i] {
				t.Errorf("%s %s: levels %q %q, want %q %q", tc.layout.Name, name,
					runes[0], runes[1], tc.normal[i], tc.shifted[i])
			}
		}
	}
}

func TestRenderXKBWords(t *testing.T) {
	us, ru := renderTestRealLayouts(t)
	tests := []struct {
		name       string
		script     string
		shownRU    bool
		caps       bool
		shown, alt string
	}{
		{name: "ghbdtn", script: "g h b d t n", shown: "ghbdtn", alt: "привет"},
		{name: "interior punctuation", script: "k . , j d m", shown: "k.,jdm", alt: "любовь"},
		{name: "reverse with ru active", script: "h e l l o", shownRU: true, shown: "руддщ", alt: "hello"},
		{name: "capitalised", script: "^g h b d t n", shown: "Ghbdtn", alt: "Привет"},
		{name: "caps lock", script: "g h b d t n", caps: true, shown: "GHBDTN", alt: "ПРИВЕТ"},
		{name: "caps lock two-level comma", script: "k ,", caps: true, shown: "K,", alt: "ЛБ"},
		{name: "backquote is yo", script: "` g", shown: "`g", alt: "ёп"},
		{name: "winkeys punctuation", script: `^4 ^6 ^7 \ ^\`, shownRU: true, shown: `;:?\/`, alt: `$^&\|`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
		})
	}
}

// TestRenderXKBPipeline runs lastWordRange and renderWord on a buffer ending
// in Space, as the auto engine does.
func TestRenderXKBPipeline(t *testing.T) {
	us, ru := renderTestRealLayouts(t)
	buf := wordTestType(t, "ok ghbdtn? ", wordTestRowEN, wordTestRowENShift)
	r, reason, ok := lastWordRange(buf, 24)
	if !ok {
		t.Fatalf("lastWordRange() failed: %s", reason)
	}
	w, err := renderWord(buf, r.Start, r.TextEnd, us, ru, false)
	if err != nil {
		t.Fatalf("renderWord() error = %v", err)
	}
	if w.Shown != "ghbdtn?" || w.Alt != "привет," || r.TextKeys != 7 {
		t.Fatalf("got %q/%q keys %d, want ghbdtn?/привет, keys 7", w.Shown, w.Alt, r.TextKeys)
	}
	if reason, vetoed := lexicalVeto(w.Shown); vetoed {
		t.Fatalf("lexicalVeto(%q) = %s", w.Shown, reason)
	}
	if shiftShapeVeto(w) {
		t.Fatalf("shiftShapeVeto(%+v) = true", w)
	}
}

func TestShiftShapeVetoRealXKB(t *testing.T) {
	us, ru := renderTestRealLayouts(t)
	wordTestRunShiftShape(t, us, ru)
}

// TestRenderXKBLSGTUnrenderable: us(basic) defines no <LSGT>, so a word with
// the 102nd key cannot be rendered in the us+ru pair.
func TestRenderXKBLSGTUnrenderable(t *testing.T) {
	us, ru := renderTestRealLayouts(t)
	if _, ok := us.KeyMap["LSGT"]; ok {
		t.Skip("installed us layout defines LSGT")
	}
	buf := wordTestSeq(t, "g LSGT")
	if _, err := renderWord(buf, 0, len(buf), us, ru, false); !errors.Is(err, ErrUnrenderable) {
		t.Fatalf("renderWord() with LSGT error = %v, want ErrUnrenderable", err)
	}
}
