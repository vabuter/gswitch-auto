package runtime

import (
	"errors"
	"strings"
	"unicode"
)

// wordRange locates the last word of the converter buffer for automatic
// correction.
type wordRange struct {
	Start    int // first event of the word (after previous SPACE/ENTER/KPENTER, or 0)
	TextEnd  int // index of the trailing SPACE K_DOWN; [Start,TextEnd) is the word
	End      int // TextEnd+1; [Start,End) is erased and replayed by ConvertRange
	TextKeys int // non-shift events in [Start,TextEnd)
}

// wordIsBoundaryKey reports whether code separates words in the converter
// buffer (the same set Convert uses for ActionConvertWord).
func wordIsBoundaryKey(code uint16) bool {
	return code == KEY_SPACE || code == KEY_ENTER || code == KEY_KPENTER
}

// lastWordRange applies, in order (reason codes in quotes):
//  1. skip trailing shift K_UP events; a trailing shift K_DOWN ⇒ "shift-held"
//  2. next event must be KEY_SPACE K_DOWN ⇒ else "no-trailing-space"
//  3. last non-shift event before it must exist and be a text key ⇒ "empty"/"double-space"/"enter-boundary"
//  4. Start = after previous SPACE/ENTER/KPENTER (or 0)
//  5. every non-shift event in [Start,TextEnd) has XKBKeyName ok ⇒ else "unrenderable-key"
//     (rejects keypad codes 55,71-83,98, Enter, convert-key events)
//  6. no shift held at Start (scan buf[:Start]) ⇒ else "shift-held-at-start"
//  7. 2 <= TextKeys <= maxLetters ⇒ else "too-short"/"too-long" (1-key words never fixed)
//
// A non-shift event that is not a K_DOWN (only a convert-key release can be one)
// also fails step 5. On success the reason is "". On failure the range is the
// zero value for steps 1-3 and fully populated for steps 5-7, for logging.
func lastWordRange(buf []KeyEvent, maxLetters int) (wordRange, string, bool) {
	// 1. Trailing shift releases may follow the Space; a held shift may not.
	i := len(buf) - 1
	for i >= 0 && Shifts[buf[i].Code] {
		if buf[i].Value != K_UP {
			return wordRange{}, "shift-held", false
		}
		i--
	}

	// 2. The word must be terminated by a Space press.
	if i < 0 || buf[i].Code != KEY_SPACE || buf[i].Value != K_DOWN {
		return wordRange{}, "no-trailing-space", false
	}
	textEnd := i

	// 3. The last non-shift event before the Space must be a text key.
	j := textEnd - 1
	for j >= 0 && Shifts[buf[j].Code] {
		j--
	}
	if j < 0 {
		return wordRange{}, "empty", false
	}
	switch buf[j].Code {
	case KEY_SPACE:
		return wordRange{}, "double-space", false
	case KEY_ENTER, KEY_KPENTER:
		return wordRange{}, "enter-boundary", false
	}

	// 4. The word starts after the previous boundary.
	start := j
	for start > 0 && !wordIsBoundaryKey(buf[start-1].Code) {
		start--
	}
	r := wordRange{Start: start, TextEnd: textEnd, End: textEnd + 1}

	// 5. Every text key must have an XKB name.
	renderable := true
	for _, ev := range buf[start:textEnd] {
		if Shifts[ev.Code] {
			continue
		}
		r.TextKeys++
		if _, ok := XKBKeyName(ev.Code); !ok || ev.Value != K_DOWN {
			renderable = false
		}
	}
	if !renderable {
		return r, "unrenderable-key", false
	}

	// 6. The replay starts with no shift pressed, so the word must too.
	if wordShiftHeldAfter(buf[:start]) {
		return r, "shift-held-at-start", false
	}

	// 7. Length limits.
	if r.TextKeys < 2 {
		return r, "too-short", false
	}
	if r.TextKeys > maxLetters {
		return r, "too-long", false
	}
	return r, "", true
}

// wordShiftHeldAfter reports whether any shift key is down after replaying
// the shift events of events.
func wordShiftHeldAfter(events []KeyEvent) bool {
	var left, right bool
	for _, ev := range events {
		switch ev.Code {
		case KEY_LEFTSHIFT:
			left = ev.Value == K_DOWN
		case KEY_RIGHTSHIFT:
			right = ev.Value == K_DOWN
		}
	}
	return left || right
}

// ErrUnrenderable reports that a word cannot be rendered as text in both layouts.
var ErrUnrenderable = errors.New("word contains a key that cannot be rendered")

// renderedWord is the text a word's key events produce in the shown (active)
// and the alternative layout.
type renderedWord struct {
	Shown, Alt string // one rune per text key, equal rune counts
	Keys       int
	// Core = key positions [CoreStart, CoreEnd) left after dropping leading and trailing keys
	// whose shown OR alt rune is not unicode.IsLetter (edge punctuation such as "?"/"," in
	// "ghbdtn?"→"привет,", "!" in "Ghbdtn!"/"руддщ!"). Interior keys are never dropped
	// ("k.,jdm"→"любовь" keeps all six). CoreKeys == 0 when no key qualifies.
	CoreStart, CoreEnd, CoreKeys int
	ShiftedKeys                  int  // CORE keys typed with Shift held
	InteriorShift                bool // a shifted CORE key at core position > 0
	CapsLock                     bool // Caps applied (false when unknown)
}

// renderWord tracks Shift inside [start,end) exactly like the replay (ConvertRange);
// level = shift XOR (caps && alphabetic(key)), alphabetic := unicode.IsLetter(r0) &&
// unicode.ToUpper(r0) == r1 (TWO_LEVEL keys such as ',' '<' ignore Caps). Bail with
// ErrUnrenderable on: no XKB name, name missing in either KeyMap, level missing, Unmapped[name].
// Callers pass the word WITHOUT its trailing Space: OnSpace calls
// renderWord(c.buffer, r.Start, r.TextEnd, …). Passing r.End would include KEY_SPACE (57),
// which has no XKBKeyName, and every word would fail with ErrUnrenderable.
//
// A nil layout, invalid bounds or a non-shift event that is not a K_DOWN also
// yield ErrUnrenderable. The error carries no key content (words are never logged).
func renderWord(buf []KeyEvent, start, end int, shown, alt *LayoutInfo, capsLock bool) (renderedWord, error) {
	if shown == nil || alt == nil || start < 0 || end > len(buf) || start > end {
		return renderedWord{}, ErrUnrenderable
	}

	n := end - start
	shownRunes := make([]rune, 0, n)
	altRunes := make([]rune, 0, n)
	shifted := make([]bool, 0, n)
	var leftShift, rightShift bool

	for _, ev := range buf[start:end] {
		switch ev.Code {
		case KEY_LEFTSHIFT:
			leftShift = ev.Value == K_DOWN
			continue
		case KEY_RIGHTSHIFT:
			rightShift = ev.Value == K_DOWN
			continue
		}
		if ev.Value != K_DOWN {
			return renderedWord{}, ErrUnrenderable
		}
		name, ok := XKBKeyName(ev.Code)
		if !ok {
			return renderedWord{}, ErrUnrenderable
		}
		shift := leftShift || rightShift
		sr, ok := renderKeyRune(shown, name, shift, capsLock)
		if !ok {
			return renderedWord{}, ErrUnrenderable
		}
		ar, ok := renderKeyRune(alt, name, shift, capsLock)
		if !ok {
			return renderedWord{}, ErrUnrenderable
		}
		shownRunes = append(shownRunes, sr)
		altRunes = append(altRunes, ar)
		shifted = append(shifted, shift)
	}

	w := renderedWord{
		Shown:    string(shownRunes),
		Alt:      string(altRunes),
		Keys:     len(shownRunes),
		CapsLock: capsLock,
	}

	first, last := -1, -1
	for k := range shownRunes {
		if unicode.IsLetter(shownRunes[k]) && unicode.IsLetter(altRunes[k]) {
			if first < 0 {
				first = k
			}
			last = k
		}
	}
	if first < 0 {
		return w, nil
	}
	w.CoreStart, w.CoreEnd = first, last+1
	w.CoreKeys = w.CoreEnd - w.CoreStart
	for k := w.CoreStart; k < w.CoreEnd; k++ {
		if !shifted[k] {
			continue
		}
		w.ShiftedKeys++
		if k > w.CoreStart {
			w.InteriorShift = true
		}
	}
	return w, nil
}

// renderKeyRune returns the rune the named key produces in layout l.
func renderKeyRune(l *LayoutInfo, name string, shift, capsLock bool) (rune, bool) {
	if l.Unmapped[name] {
		return 0, false
	}
	runes := l.KeyMap[name]
	if len(runes) == 0 {
		return 0, false
	}
	alphabetic := len(runes) > 1 && unicode.IsLetter(runes[0]) && unicode.ToUpper(runes[0]) == runes[1]
	level := 0
	if shift != (capsLock && alphabetic) {
		level = 1
	}
	if level >= len(runes) {
		return 0, false
	}
	return runes[level], true
}

// lexicalVeto rejects shown words that look like code, paths, options, URLs or
// addresses: a digit anywhere, a leading '/', '~' or '-', or "://", "@" or `\`
// anywhere. Punctuation alone never vetoes ("k.,jdm"→"любовь" stays fixable).
// The reason is a short code without prefix: "digit", "leading-slash",
// "leading-tilde", "leading-dash", "url", "at", "backslash".
func lexicalVeto(shown string) (string, bool) {
	if strings.IndexFunc(shown, unicode.IsDigit) >= 0 {
		return "digit", true
	}
	switch {
	case strings.HasPrefix(shown, "/"):
		return "leading-slash", true
	case strings.HasPrefix(shown, "~"):
		return "leading-tilde", true
	case strings.HasPrefix(shown, "-"):
		return "leading-dash", true
	case strings.Contains(shown, "://"):
		return "url", true
	case strings.Contains(shown, "@"):
		return "at", true
	case strings.Contains(shown, `\`):
		return "backslash", true
	}
	return "", false
}

// shiftShapeVeto rejects words whose core was typed in a shape that suggests
// deliberate capitalisation (acronyms such as "GTK", camelCase such as
// "getValue"/"iPhone"). It is computed from Shift, never from runes, so
// CapsLock cannot veto everything, and only over the core, so Shift-typed edge
// punctuation never vetoes a word.
func shiftShapeVeto(w renderedWord) bool {
	return w.CoreKeys > 0 && ((w.ShiftedKeys >= 2 && w.ShiftedKeys == w.CoreKeys) || w.InteriorShift)
}
