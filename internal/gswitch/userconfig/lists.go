package userconfig

import (
	"fmt"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/arumata/gswitch/internal/gswitch/fold"
)

// MaxWordRunes is the longest exception word accepted (after folding).
const MaxWordRunes = 64

// windowAppPrefix marks Shell ids of windows without a .desktop file; they are
// not stable across sessions and are rejected in the application list.
const windowAppPrefix = "window:"

// ListSet is a parsed exception list. All methods are nil-receiver safe: a nil
// *ListSet behaves as an empty set.
type ListSet struct {
	entries map[string]struct{}
	globs   []string
	order   []string
}

// NewWordSet builds a word list from raw entries, normalised with
// NormalizeWord; invalid entries are dropped.
func NewWordSet(entries []string) *ListSet {
	return newListSet(entries, NormalizeWord, false)
}

// NewAppSet builds an application list from raw entries, normalised with
// NormalizeApp; invalid entries are dropped. Entries containing '*' or '?' are
// glob patterns (path.Match).
func NewAppSet(entries []string) *ListSet {
	return newListSet(entries, NormalizeApp, true)
}

func newListSet(entries []string, normalize func(string) (string, bool), globs bool) *ListSet {
	l := emptyListSet()
	for _, e := range entries {
		if n, ok := normalize(e); ok {
			l.add(n, globs)
		}
	}
	return l
}

func emptyListSet() *ListSet {
	return &ListSet{entries: make(map[string]struct{})}
}

// add inserts a normalised entry; it reports false for a duplicate.
func (l *ListSet) add(entry string, globs bool) bool {
	if _, dup := l.entries[entry]; dup {
		return false
	}
	l.entries[entry] = struct{}{}
	l.order = append(l.order, entry)
	if globs && isGlob(entry) {
		l.globs = append(l.globs, entry)
	}
	return true
}

// Contains reports whether fold.Word(word) is in the list (exception words are
// matched against the word as shown on screen before correction).
func (l *ListSet) Contains(word string) bool {
	if l == nil || len(l.entries) == 0 {
		return false
	}
	w := fold.Word(word)
	if w == "" {
		return false
	}
	_, ok := l.entries[w]
	return ok
}

// MatchApp reports whether the application id or the WM_CLASS matches an
// entry exactly or a glob entry. Both arguments are normalised with
// fold.AppKey (idempotent), so raw extension ids such as
// "org.gnome.Ptyxis.desktop" match the entry "org.gnome.ptyxis". It implements
// focus.AppMatcher and does not allocate for already-normalised arguments.
func (l *ListSet) MatchApp(appID, wmClass string) bool {
	if l == nil || len(l.entries) == 0 {
		return false
	}
	return l.matchAppKey(fold.AppKey(appID)) || l.matchAppKey(fold.AppKey(wmClass))
}

func (l *ListSet) matchAppKey(key string) bool {
	if key == "" {
		return false
	}
	if _, ok := l.entries[key]; ok {
		return true
	}
	for _, g := range l.globs {
		if ok, err := path.Match(g, key); err == nil && ok {
			return true
		}
	}
	return false
}

// Entries returns a copy of the entries in file order.
func (l *ListSet) Entries() []string {
	if l == nil || len(l.order) == 0 {
		return []string{}
	}
	return append([]string(nil), l.order...)
}

// Len returns the number of entries.
func (l *ListSet) Len() int {
	if l == nil {
		return 0
	}
	return len(l.order)
}

// Equal reports whether both lists hold the same entries (order ignored).
func (l *ListSet) Equal(o *ListSet) bool {
	if l.Len() != o.Len() {
		return false
	}
	if l.Len() == 0 {
		return true
	}
	for e := range l.entries {
		if _, ok := o.entries[e]; !ok {
			return false
		}
	}
	return true
}

// NormalizeWord folds an exception word with fold.Word (lower case, ё→е,
// leading and trailing non-letters stripped) and validates it: it must be
// non-empty, contain no white space, control character or '#' (the comment
// marker), and have at most MaxWordRunes runes.
func NormalizeWord(s string) (string, bool) {
	w := fold.Word(s)
	if w == "" || utf8.RuneCountInString(w) > MaxWordRunes {
		return "", false
	}
	if strings.ContainsFunc(w, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || r == '#' }) {
		return "", false
	}
	return w, true
}

// NormalizeApp normalises an application entry with fold.AppKey (trimmed,
// lower case, ".desktop" stripped) and validates it: it must be non-empty, not
// start with "window:" (unstable Shell window ids), contain no control
// character or '#', and be a well-formed pattern when it contains '*' or '?'.
func NormalizeApp(s string) (string, bool) {
	k := fold.AppKey(s)
	if k == "" || strings.HasPrefix(k, windowAppPrefix) {
		return "", false
	}
	if strings.ContainsFunc(k, func(r rune) bool { return unicode.IsControl(r) || r == '#' }) {
		return "", false
	}
	if isGlob(k) {
		if _, err := path.Match(k, ""); err != nil {
			return "", false
		}
	}
	return k, true
}

// isGlob reports whether an application entry is a path.Match pattern.
func isGlob(entry string) bool {
	return strings.ContainsAny(entry, "*?")
}

// listSpec describes one exception list file.
type listSpec struct {
	name      string
	normalize func(string) (string, bool)
	globs     bool
}

func listSpecFor(name string) (listSpec, error) {
	switch name {
	case WordsName:
		return listSpec{name: WordsName, normalize: NormalizeWord}, nil
	case AppsName:
		return listSpec{name: AppsName, normalize: NormalizeApp, globs: true}, nil
	default:
		return listSpec{}, fmt.Errorf("userconfig: %q is not an exception list", name)
	}
}

// listLineEntry returns the raw entry of a list line: text before '#',
// trimmed. ok is false for blank and comment-only lines.
func listLineEntry(line string) (string, bool) {
	if i := strings.IndexByte(line, '#'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	return line, line != ""
}

// parseList parses list text: one entry per line, '#' starts a comment
// (whole line or trailing), blank lines ignored, duplicates dropped, at most
// MaxListEntries entries. Rejected entries produce warnings.
func parseList(spec listSpec, text string) (*ListSet, []string) {
	l := emptyListSet()
	var warnings []string
	for i, line := range splitLines(text) {
		raw, ok := listLineEntry(line)
		if !ok {
			continue
		}
		entry, valid := spec.normalize(raw)
		if !valid {
			warnings = append(warnings, fmt.Sprintf("%s:%d: entry %q rejected", spec.name, i+1, raw))
			continue
		}
		if _, dup := l.entries[entry]; dup {
			continue
		}
		if l.Len() >= MaxListEntries {
			warnings = append(warnings, fmt.Sprintf("%s:%d: more than %d entries; the rest is ignored",
				spec.name, i+1, MaxListEntries))
			break
		}
		l.add(entry, spec.globs)
	}
	return l, warnings
}
