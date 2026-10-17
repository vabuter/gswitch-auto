// Package lang decides whether a word was typed in the wrong keyboard layout.
//
// It is a bit-exact Go port of the EN/RU part of the punto-rs layout detector
// (cli/src/layout.rs and cli/src/layout/keymap.rs at commit c836c87, MIT,
// Copyright (c) 2026 netherguy4, https://github.com/rashevskyv/punto-rs): a
// letter-trigram cost model plus a Bloom-filter dictionary per language, with
// the punto-rs thresholds, closed short-word lists, hyphen-compound rule and
// built-in exception list. The model blobs are embedded unchanged; see
// data/README.md and third_party/punto-rs/NOTICE.md for their provenance.
//
// The detector works on rendered text, not on key codes: the caller renders
// the typed keys in the active ("shown") layout and in the other ("alt")
// layout, one rune per key, and passes both strings to Detector.Decide. The
// package performs no file I/O and does not depend on the runtime package.
package lang

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// Lang is a language with an embedded detector model.
type Lang uint8

// Supported languages. Unknown is the zero value and is never supported.
const (
	Unknown Lang = iota
	En
	Ru
)

// String returns the short language code ("en", "ru") or "unknown".
func (l Lang) String() string {
	switch l {
	case En:
		return "en"
	case Ru:
		return "ru"
	default:
		return "unknown"
	}
}

// LangForLayout maps an XKB layout name to its detector language: "us" is
// English and "ru" is Russian. A variant suffix ("ru(phonetic)", "us+intl")
// is ignored because the caller renders keys through the real XKB keymap;
// only the language of the resulting letters matters. Every other layout
// yields (Unknown, false).
func LangForLayout(xkbLayoutName string) (Lang, bool) {
	name := strings.TrimSpace(xkbLayoutName)
	if i := strings.IndexAny(name, "(+:"); i >= 0 {
		name = name[:i]
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "us":
		return En, true
	case "ru":
		return Ru, true
	default:
		return Unknown, false
	}
}

// Pair names the language of the active layout (Shown) and of the other
// layout (Alt). Decide supports only {En, Ru} and {Ru, En}.
type Pair struct{ Shown, Alt Lang }

func (p Pair) supported() bool {
	return (p.Shown == En && p.Alt == Ru) || (p.Shown == Ru && p.Alt == En)
}

// Option bounds and defaults. The defaults are the punto-rs constants
// (layout.rs:83-92).
const (
	// puntoMinLetters is punto-rs MIN_LETTERS (layout.rs:83).
	puntoMinLetters   = 3
	defaultMinLetters = puntoMinLetters
	minMinLetters     = 2
	maxMinLetters     = 8

	defaultMinMargin = 1.0
	minMinMargin     = 0.0
	maxMinMargin     = 5.0

	defaultMaxAltCost = 7.0
	minMaxAltCost     = 3.0
	maxMaxAltCost     = unreadableCost
)

// unreadableCost is the cost of a shown word whose core contains a rune
// outside the shown alphabet (layout.rs:92).
const unreadableCost = 25.5

// Options are the detector thresholds. They can be swapped at run time with
// Detector.SetOptions.
type Options struct {
	// MinLetters is the minimal number of letters in the alt-layout word core
	// for the score path (punto-rs MIN_LETTERS, layout.rs:83). It gates
	// neither the short-list path nor hyphen compounds, whose parts are always
	// scored with the punto-rs value 3.
	MinLetters int
	// MinMargin is the minimal advantage of the alt word in bits per letter
	// (punto-rs MIN_MARGIN, layout.rs:85).
	MinMargin float64
	// MaxAltCost caps the cost of the alt word (punto-rs MAX_ALT_COST,
	// layout.rs:87).
	MaxAltCost float64
	// ShortWords enables the closed lists for 2-key words (keymap.rs:80-88).
	ShortWords bool
}

// DefaultOptions returns the punto-rs thresholds.
func DefaultOptions() Options {
	return Options{
		MinLetters: defaultMinLetters,
		MinMargin:  defaultMinMargin,
		MaxAltCost: defaultMaxAltCost,
		ShortWords: true,
	}
}

// ErrInvalidOptions is wrapped by every error returned from Options.Validate.
var ErrInvalidOptions = errors.New("invalid detector options")

// Validate reports whether every threshold lies in its documented range.
func (o Options) Validate() error {
	if o.MinLetters < minMinLetters || o.MinLetters > maxMinLetters {
		return fmt.Errorf("%w: min letters %d outside %d..%d",
			ErrInvalidOptions, o.MinLetters, minMinLetters, maxMinLetters)
	}
	if !inRange(o.MinMargin, minMinMargin, maxMinMargin) {
		return fmt.Errorf("%w: margin %v outside %v..%v",
			ErrInvalidOptions, o.MinMargin, minMinMargin, maxMinMargin)
	}
	if !inRange(o.MaxAltCost, minMaxAltCost, maxMaxAltCost) {
		return fmt.Errorf("%w: max alt cost %v outside %v..%v",
			ErrInvalidOptions, o.MaxAltCost, minMaxAltCost, maxMaxAltCost)
	}
	return nil
}

func inRange(v, lo, hi float64) bool {
	return !math.IsNaN(v) && v >= lo && v <= hi
}

// Scores are the costs of a word in both layouts, in bits per letter (lower
// is more plausible), and whether each spelling is in its language's
// dictionary. They mirror punto-rs Scores (layout.rs:173-182).
type Scores struct {
	ShownCost, AltCost   float64
	ShownKnown, AltKnown bool
}

// Verdict is the outcome of Detector.Decide.
type Verdict uint8

// Verdicts.
const (
	// Keep: the word is a candidate but stays as typed.
	Keep Verdict = iota
	// Fix: the word was typed in the wrong layout.
	Fix
	// NotCandidate: the word is not judged at all (digits, too short, ...).
	NotCandidate
)

// String returns "keep", "fix" or "not-candidate".
func (v Verdict) String() string {
	switch v {
	case Keep:
		return "keep"
	case Fix:
		return "fix"
	case NotCandidate:
		return "not-candidate"
	default:
		return fmt.Sprintf("verdict(%d)", uint8(v))
	}
}

// Decision reason codes.
const (
	ReasonScores    = "scores"
	ReasonShortList = "short-list"
	ReasonCompound  = "compound"

	ReasonException  = "exception"
	ReasonShownKnown = "shown-known"
	ReasonAltUnknown = "alt-unknown"
	ReasonAltCost    = "alt-cost"
	ReasonMargin     = "margin"

	ReasonDigits          = "digits"
	ReasonNonLetter       = "non-letter"
	ReasonTooShort        = "too-short"
	ReasonUnreadableAlt   = "unreadable-alt"
	ReasonUnsupportedPair = "unsupported-pair"
)

// Decision is the result of Detector.Decide.
type Decision struct {
	Verdict Verdict
	// Reason is one of the Reason* codes:
	// Fix: "scores" | "short-list" | "compound";
	// Keep: "exception" | "shown-known" | "alt-unknown" | "alt-cost" | "margin";
	// NotCandidate: "digits" | "non-letter" | "too-short" | "unreadable-alt" |
	// "unsupported-pair".
	Reason string
	// Scores are the score-path values; nil when the word was not scored.
	Scores *Scores
}
