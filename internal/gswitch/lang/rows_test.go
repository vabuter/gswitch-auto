package lang

import (
	"strings"
	"testing"
)

// Test-only port of punto-rs keymap.rs key_char (cli/src/layout/keymap.rs:22-49):
// the characters of the 47 printable keys per layout and Shift level. It is
// the oracle that turns the labelled words of testdata/layout_cases.tsv back
// into key presses, exactly as punto-rs tests.rs keys_for does. Production
// rendering goes through the XKB keymaps in package runtime instead.
const (
	rowsEN      = "1234567890-=qwertyuiop[]asdfghjkl;'`\\zxcvbnm,./"
	rowsENShift = "!@#$%^&*()_+QWERTYUIOP{}ASDFGHJKL:\"~|ZXCVBNM<>?"
	rowsRU      = "1234567890-=йцукенгшщзхъфывапролджэё\\ячсмитьбю."
	rowsRUShift = "!\"№;%:?*()_+ЙЦУКЕНГШЩЗХЪФЫВАПРОЛДЖЭЁ/ЯЧСМИТЬБЮ,"
)

// rowsTestKey is one key press: evdev code and Shift.
type rowsTestKey struct {
	code  uint16
	shift bool
}

// rowsTestKeyChar mirrors key_char: codes 2..=13, 16..=27, 30..=41, 43..=53.
func rowsTestKeyChar(l Lang, code uint16, shift bool) (rune, bool) {
	var pos int
	switch {
	case code >= 2 && code <= 13:
		pos = int(code) - 2
	case code >= 16 && code <= 27:
		pos = int(code) - 4
	case code >= 30 && code <= 41:
		pos = int(code) - 6
	case code >= 43 && code <= 53:
		pos = int(code) - 7
	default:
		return 0, false
	}
	var row string
	switch {
	case l == En && !shift:
		row = rowsEN
	case l == En && shift:
		row = rowsENShift
	case l == Ru && !shift:
		row = rowsRU
	case l == Ru && shift:
		row = rowsRUShift
	default:
		return 0, false
	}
	r := []rune(row)
	if pos >= len(r) {
		return 0, false
	}
	return r[pos], true
}

// rowsTestKeysFor mirrors tests.rs keys_for: the first (code, shift) giving
// each character; characters without a key are skipped.
func rowsTestKeysFor(l Lang, text string) []rowsTestKey {
	var keys []rowsTestKey
	for _, ch := range text {
	search:
		for code := uint16(2); code <= 53; code++ {
			for _, shift := range []bool{false, true} {
				if r, ok := rowsTestKeyChar(l, code, shift); ok && r == ch {
					keys = append(keys, rowsTestKey{code, shift})
					break search
				}
			}
		}
	}
	return keys
}

// rowsTestRender renders keys in a layout, one rune per key.
func rowsTestRender(t testing.TB, l Lang, keys []rowsTestKey) string {
	t.Helper()
	var b strings.Builder
	for _, k := range keys {
		r, ok := rowsTestKeyChar(l, k.code, k.shift)
		if !ok {
			t.Fatalf("key %d shift=%v has no %s character", k.code, k.shift, l)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// rowsTestWord returns text typed in layout shown and the same keys rendered
// in the other layout of the pair.
func rowsTestWord(t testing.TB, pair Pair, text string) (string, string) {
	t.Helper()
	keys := rowsTestKeysFor(pair.Shown, text)
	if len(keys) != len([]rune(text)) {
		t.Fatalf("%q: only %d of %d characters have a %s key", text, len(keys), len([]rune(text)), pair.Shown)
	}
	return rowsTestRender(t, pair.Shown, keys), rowsTestRender(t, pair.Alt, keys)
}

func TestRowsTestTablesHaveOneRunePerKey(t *testing.T) {
	for _, row := range []string{rowsEN, rowsENShift, rowsRU, rowsRUShift} {
		if n := len([]rune(row)); n != 47 {
			t.Errorf("row %q has %d runes, want 47", row, n)
		}
	}
	if _, ok := rowsTestKeyChar(Ru, 1, false); ok {
		t.Error("key 1 (Escape) must have no character")
	}
}
