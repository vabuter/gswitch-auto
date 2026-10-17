package lang

import (
	_ "embed" // built-in exception list
	"maps"
	"strings"

	"github.com/arumata/gswitch/internal/gswitch/fold"
)

// exceptionsTxt is punto-rs cli/src/layout/exceptions.txt: live-speech words
// and terms the detector would otherwise "fix" (layout.rs:228-230).
//
//go:embed data/exceptions.txt
var exceptionsTxt string

// wordSet is a set of fold.Word outputs.
type wordSet map[string]struct{}

// builtinExceptions holds the built-in list, folded with fold.Word.
var builtinExceptions = parseWordList(strings.Split(exceptionsTxt, "\n"))

// parseWordList folds every entry with fold.Word and drops empty entries and
// lines that start with '#' (after surrounding white space is trimmed).
func parseWordList(lines []string) wordSet {
	set := make(wordSet, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if w := fold.Word(line); w != "" {
			set[w] = struct{}{}
		}
	}
	return set
}

// SetUserExceptions replaces the user exception list. Each entry is folded
// with fold.Word; empty entries and '#' comment lines are dropped. The swap is
// atomic: concurrent Decide calls see either the old or the new list.
func (d *Detector) SetUserExceptions(words []string) {
	set := parseWordList(words)
	d.userMu.Lock()
	defer d.userMu.Unlock()
	d.user.Store(&set)
}

// AddUserException adds one word (folded with fold.Word) to the user
// exception list, copy-on-write. It is used to learn a word after an undo.
func (d *Detector) AddUserException(word string) {
	w := fold.Word(word)
	if w == "" {
		return
	}
	d.userMu.Lock()
	defer d.userMu.Unlock()
	var next wordSet
	if cur := d.user.Load(); cur != nil {
		if _, ok := (*cur)[w]; ok {
			return
		}
		next = maps.Clone(*cur)
	}
	if next == nil {
		next = make(wordSet, 1)
	}
	next[w] = struct{}{}
	d.user.Store(&next)
}

// IsException reports whether fold.Word(shown) is in the built-in or the user
// exception list (punto-rs is_exception, layout.rs:330-333). Case and edge
// punctuation do not matter. As in punto-rs, the lists are shared by both
// languages; the language argument is accepted for symmetry with the other
// per-language queries.
func (d *Detector) IsException(shown string, _ Lang) bool {
	return d.isFoldedException(fold.Word(shown))
}

func (d *Detector) isFoldedException(w string) bool {
	if w == "" {
		return false
	}
	if _, ok := builtinExceptions[w]; ok {
		return true
	}
	if user := d.user.Load(); user != nil {
		_, ok := (*user)[w]
		return ok
	}
	return false
}
