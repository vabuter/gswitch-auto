package userconfig

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/arumata/gswitch/internal/gswitch/config"
)

// Chord is a set of evdev scancodes that must be held together. Chords built
// by ParseChord are sorted ascending and distinct; the methods also accept
// unsorted values.
type Chord []uint16

const (
	// MinChordKeys is the smallest number of keys of a chord.
	MinChordKeys = 2
	// MaxChordKeys is the largest number of keys of a chord.
	MaxChordKeys = 4

	maxChordCode = 0x2ff // KEY_MAX
)

// Chord validation errors; the returned errors wrap them so callers can map
// them to UI messages with errors.Is.
var (
	ErrChordSyntax               = errors.New("invalid chord syntax")
	ErrChordTooShort             = errors.New("chord needs at least 2 keys")
	ErrChordTooLong              = errors.New("chord allows at most 4 keys")
	ErrChordKeyRange             = errors.New("chord key code out of range 1..767")
	ErrChordDuplicateKey         = errors.New("chord contains a key twice")
	ErrChordNeedsModifier        = errors.New("chord needs at least one modifier key")
	ErrChordTextKey              = errors.New("chord contains a text key")
	ErrChordOverlap              = errors.New("chords overlap")
	ErrChordContainsLayoutSwitch = errors.New("chord contains the layout switch keys")
	ErrChordContainsConvertKey   = errors.New("chord contains the convert key")
)

// ChordExtraKeys lists the non-modifier keys a chord may contain besides
// config.IsModifierKey keys: F1–F10 (59–68), F11/F12 (87, 88), F13–F24
// (183–194), ScrollLock (70), Pause (119) and Menu/Compose (127).
var ChordExtraKeys = func() map[uint16]bool {
	keys := map[uint16]bool{70: true, 87: true, 88: true, 119: true, 127: true}
	for code := uint16(59); code <= 68; code++ {
		keys[code] = true
	}
	for code := uint16(183); code <= 194; code++ {
		keys[code] = true
	}
	return keys
}()

// ParseChord parses "42+54" (order-insensitive, white space around codes
// allowed). "none", "off" and "" return nil (disabled) without error. The
// result is sorted ascending and has passed ValidateChord.
func ParseChord(s string) (Chord, error) {
	trimmed := strings.TrimSpace(s)
	switch strings.ToLower(trimmed) {
	case "", "none", "off":
		return nil, nil
	}
	parts := strings.Split(trimmed, "+")
	c := make(Chord, 0, len(parts))
	for _, part := range parts {
		token := strings.TrimSpace(part)
		v, err := strconv.ParseUint(token, 10, 16)
		if err != nil {
			return nil, fmt.Errorf("%w: %q is not a numeric key code", ErrChordSyntax, token)
		}
		c = append(c, uint16(v))
	}
	slices.Sort(c)
	if err := ValidateChord(c); err != nil {
		return nil, err
	}
	return c, nil
}

// String formats the chord as ascending codes joined by '+' ("42+97+100");
// an empty chord is "none".
func (c Chord) String() string {
	if len(c) == 0 {
		return "none"
	}
	sorted := c.sorted()
	parts := make([]string, len(sorted))
	for i, code := range sorted {
		parts[i] = strconv.Itoa(int(code))
	}
	return strings.Join(parts, "+")
}

// Equal reports set equality; nil and empty chords are equal.
func (c Chord) Equal(o Chord) bool {
	if len(c) != len(o) {
		return false
	}
	return slices.Equal(c.sorted(), o.sorted())
}

// Overlaps reports whether one chord is a subset of the other (including
// equality). An empty (disabled) chord overlaps nothing.
func (c Chord) Overlaps(o Chord) bool {
	if len(c) == 0 || len(o) == 0 {
		return false
	}
	return c.ContainsAll(o) || o.ContainsAll(c)
}

// ContainsAll reports whether every key of keys is part of the chord.
func (c Chord) ContainsAll(keys []uint16) bool {
	for _, k := range keys {
		if !slices.Contains(c, k) {
			return false
		}
	}
	return true
}

func (c Chord) sorted() Chord {
	if slices.IsSorted(c) {
		return c
	}
	s := slices.Clone(c)
	slices.Sort(s)
	return s
}

// ValidateChord checks a single chord: 2..4 distinct codes in 1..0x2ff, at
// least one config.IsModifierKey key, and every non-modifier key must be in
// ChordExtraKeys. Text keys (letters, digits, punctuation, Space, Enter,
// Backspace, Tab, Esc, arrows, keypad) are rejected: Shift+A (42+30) would
// toggle on every capital "A" and swallow the letter's key-up.
func ValidateChord(c Chord) error {
	if len(c) < MinChordKeys {
		return ErrChordTooShort
	}
	if len(c) > MaxChordKeys {
		return ErrChordTooLong
	}
	hasModifier := false
	for i, code := range c {
		if code == 0 || code > maxChordCode {
			return fmt.Errorf("%w: %d", ErrChordKeyRange, code)
		}
		if slices.Contains(c[:i], code) {
			return fmt.Errorf("%w: %d", ErrChordDuplicateKey, code)
		}
		if config.IsModifierKey(code) {
			hasModifier = true
			continue
		}
		if !ChordExtraKeys[code] {
			return fmt.Errorf("%w: %d", ErrChordTextKey, code)
		}
	}
	if !hasModifier {
		return ErrChordNeedsModifier
	}
	return nil
}

// ValidateChords checks the configured chords together: each non-nil chord
// must pass ValidateChord; toggle and selection must not overlap; neither may
// contain all layout switch keys (when any are configured) nor the convert key
// (when it is not 0). Nil chords (disabled) are skipped.
func ValidateChords(toggle, selection Chord, layoutSwitch []uint16, convertKey uint16) error {
	named := []struct {
		key   string
		chord Chord
	}{{KeyToggleChord, toggle}, {KeySelectionChord, selection}}
	var errs []error
	for _, n := range named {
		if n.chord == nil {
			continue
		}
		if err := ValidateChord(n.chord); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", n.key, err))
			continue
		}
		if len(layoutSwitch) > 0 && n.chord.ContainsAll(layoutSwitch) {
			errs = append(errs, fmt.Errorf("%s: %w (%s)", n.key, ErrChordContainsLayoutSwitch, Chord(layoutSwitch)))
		}
		if convertKey != 0 && slices.Contains(n.chord, convertKey) {
			errs = append(errs, fmt.Errorf("%s: %w (%d)", n.key, ErrChordContainsConvertKey, convertKey))
		}
	}
	if toggle.Overlaps(selection) {
		errs = append(errs, fmt.Errorf("%w: %s and %s", ErrChordOverlap, toggle, selection))
	}
	return errors.Join(errs...)
}
