// Package fold holds the single normaliser for exception words and for
// application keys used by gswitch-auto. No other word or application
// normaliser may exist in the fork: the detector, the auto engine, the user
// configuration, the focus policy and the settings GUI all call these
// functions so that stored and observed values always compare equal.
package fold

import (
	"strings"
	"unicode"
)

const desktopSuffix = ".desktop"

// Word folds a word exactly as punto-rs shown_word does (cli/src/layout.rs:
// lowercase :276-284 + word_core :310-320 = shown_word :323-328): every rune is
// lowercased with unicode.ToLower and 'ё' becomes 'е', then leading and
// trailing runes that are not letters (unicode.IsLetter) are stripped.
// Interior punctuation is kept ("k.,jdm" stays "k.,jdm"). Word returns ""
// when no letter remains.
func Word(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		r = unicode.ToLower(r)
		if r == 'ё' {
			r = 'е'
		}
		b.WriteRune(r)
	}
	return strings.TrimFunc(b.String(), notLetter)
}

func notLetter(r rune) bool {
	return !unicode.IsLetter(r)
}

// AppKey normalises a desktop application id or a WM_CLASS: surrounding
// white space is trimmed, the value is lowercased and a trailing ".desktop"
// is stripped ("org.gnome.Ptyxis.desktop" -> "org.gnome.ptyxis",
// "  Kitty " -> "kitty"). The strip is repeated, together with the trim,
// until the value is stable, which keeps AppKey idempotent even for
// pathological input such as "a.desktop.desktop" or "a .desktop". AppKey
// does not allocate when its input is already normalised.
func AppKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for {
		trimmed, ok := strings.CutSuffix(s, desktopSuffix)
		if !ok {
			return s
		}
		s = strings.TrimSpace(trimmed)
	}
}
