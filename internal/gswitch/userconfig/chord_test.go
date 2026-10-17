package userconfig

import (
	"errors"
	"testing"
)

func TestParseChord(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want Chord
		err  error
	}{
		{name: "none", in: "none"},
		{name: "none upper case", in: "NONE"},
		{name: "off", in: "off"},
		{name: "empty", in: ""},
		{name: "blank", in: "   "},
		{name: "toggle default", in: "42+54", want: Chord{42, 54}},
		{name: "reversed", in: "54+42", want: Chord{42, 54}},
		{name: "spaces", in: " 42 + 54 ", want: Chord{42, 54}},
		{name: "selection default any order", in: "42+100+97", want: Chord{42, 97, 100}},
		{name: "four keys", in: "29+42+56+88", want: Chord{29, 42, 56, 88}},
		{name: "duplicate", in: "42+42", err: ErrChordDuplicateKey},
		{name: "duplicate among three", in: "42+54+42", err: ErrChordDuplicateKey},
		{name: "five keys", in: "29+42+54+56+97", err: ErrChordTooLong},
		{name: "zero", in: "0+42", err: ErrChordKeyRange},
		{name: "above KEY_MAX", in: "42+768", err: ErrChordKeyRange},
		{name: "hex 0x300", in: "42+0x300", err: ErrChordSyntax},
		{name: "non-numeric", in: "42+abc", err: ErrChordSyntax},
		{name: "negative", in: "42+-1", err: ErrChordSyntax},
		{name: "empty token", in: "42++54", err: ErrChordSyntax},
		{name: "trailing plus", in: "42+", err: ErrChordSyntax},
		{name: "lone shift", in: "42", err: ErrChordTooShort},
		{name: "two letters", in: "30+31", err: ErrChordTextKey},
		{name: "two function keys", in: "59+60", err: ErrChordNeedsModifier},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseChord(tt.in)
			if tt.err != nil {
				if !errors.Is(err, tt.err) {
					t.Fatalf("ParseChord(%q) error = %v, want %v", tt.in, err, tt.err)
				}
				if got != nil {
					t.Fatalf("ParseChord(%q) = %v on error, want nil", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseChord(%q) unexpected error: %v", tt.in, err)
			}
			if !got.Equal(tt.want) || (tt.want == nil) != (got == nil) {
				t.Fatalf("ParseChord(%q) = %v, want %v", tt.in, got, tt.want)
			}
			for i := 1; i < len(got); i++ {
				if got[i-1] >= got[i] {
					t.Fatalf("ParseChord(%q) = %v, not sorted ascending", tt.in, got)
				}
			}
		})
	}
}

func TestValidateChord(t *testing.T) {
	tests := []struct {
		name  string
		chord Chord
		err   error
	}{
		{name: "shift+A rejected", chord: Chord{42, 30}, err: ErrChordTextKey},
		{name: "shift+space rejected", chord: Chord{42, 57}, err: ErrChordTextKey},
		{name: "ctrl+enter rejected", chord: Chord{29, 28}, err: ErrChordTextKey},
		{name: "shift+F12 accepted", chord: Chord{42, 88}},
		{name: "rctrl+pause accepted", chord: Chord{97, 119}},
		{name: "unsorted accepted", chord: Chord{100, 42, 97}},
		{name: "modifiers only", chord: Chord{42, 54}},
		{name: "F13 and menu", chord: Chord{29, 183, 127}},
		{name: "scroll lock", chord: Chord{56, 70}},
		{name: "nil", chord: nil, err: ErrChordTooShort},
		{name: "single", chord: Chord{42}, err: ErrChordTooShort},
		{name: "five", chord: Chord{29, 42, 54, 56, 97}, err: ErrChordTooLong},
		{name: "duplicate", chord: Chord{42, 42}, err: ErrChordDuplicateKey},
		{name: "zero", chord: Chord{0, 42}, err: ErrChordKeyRange},
		{name: "out of range", chord: Chord{42, 0x300}, err: ErrChordKeyRange},
		{name: "function keys only", chord: Chord{59, 60}, err: ErrChordNeedsModifier},
		{name: "escape", chord: Chord{29, 1}, err: ErrChordTextKey},
		{name: "keypad", chord: Chord{29, 79}, err: ErrChordTextKey},
		{name: "arrow", chord: Chord{29, 103}, err: ErrChordTextKey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateChord(tt.chord)
			if tt.err == nil {
				if err != nil {
					t.Fatalf("ValidateChord(%v) = %v, want nil", tt.chord, err)
				}
				return
			}
			if !errors.Is(err, tt.err) {
				t.Fatalf("ValidateChord(%v) = %v, want %v", tt.chord, err, tt.err)
			}
		})
	}
}

func TestChordExtraKeys(t *testing.T) {
	want := []uint16{59, 60, 61, 62, 63, 64, 65, 66, 67, 68, 87, 88, 70, 119, 127}
	for code := uint16(183); code <= 194; code++ {
		want = append(want, code)
	}
	if len(ChordExtraKeys) != len(want) {
		t.Fatalf("ChordExtraKeys has %d keys, want %d", len(ChordExtraKeys), len(want))
	}
	for _, code := range want {
		if !ChordExtraKeys[code] {
			t.Errorf("ChordExtraKeys misses %d", code)
		}
	}
}

func TestChordStringRoundTrip(t *testing.T) {
	if got := Chord(nil).String(); got != "none" {
		t.Fatalf("nil chord String() = %q, want none", got)
	}
	if got := (Chord{100, 42, 97}).String(); got != "42+97+100" {
		t.Fatalf("String() = %q, want 42+97+100", got)
	}
	for _, c := range []Chord{nil, {42, 54}, {42, 97, 100}, {29, 42, 56, 88}} {
		parsed, err := ParseChord(c.String())
		if err != nil {
			t.Fatalf("ParseChord(%q): %v", c.String(), err)
		}
		if !parsed.Equal(c) {
			t.Fatalf("round trip of %v gave %v", c, parsed)
		}
	}
}

func TestChordSetOperations(t *testing.T) {
	toggle := Chord{42, 54}
	selection := Chord{42, 97, 100}

	if !(Chord{54, 42}).Equal(toggle) {
		t.Error("Equal must ignore order")
	}
	if !Chord(nil).Equal(Chord{}) {
		t.Error("nil and empty chords must be equal")
	}
	if toggle.Equal(selection) || toggle.Equal(Chord{42, 56}) {
		t.Error("different chords reported equal")
	}

	overlaps := []struct {
		a, b Chord
		want bool
	}{
		{toggle, toggle, true},
		{Chord{42, 97}, selection, true},
		{selection, Chord{97, 42}, true},
		{toggle, selection, false},
		{nil, selection, false},
		{selection, nil, false},
		{nil, nil, false},
	}
	for _, o := range overlaps {
		if got := o.a.Overlaps(o.b); got != o.want {
			t.Errorf("%v.Overlaps(%v) = %v, want %v", o.a, o.b, got, o.want)
		}
	}

	if !selection.ContainsAll([]uint16{100, 42}) {
		t.Error("ContainsAll subset = false")
	}
	if selection.ContainsAll([]uint16{56, 42}) {
		t.Error("ContainsAll non-subset = true")
	}
	if Chord(nil).ContainsAll([]uint16{42}) {
		t.Error("nil chord contains a key")
	}
}

func TestValidateChords(t *testing.T) {
	layoutSwitch := []uint16{56, 42}
	tests := []struct {
		name         string
		toggle, sel  Chord
		layoutSwitch []uint16
		convertKey   uint16
		err          error
	}{
		{name: "defaults", toggle: Chord{42, 54}, sel: Chord{42, 97, 100}, layoutSwitch: layoutSwitch},
		{name: "both disabled", layoutSwitch: layoutSwitch, convertKey: 119},
		{name: "overlap", toggle: Chord{42, 97}, sel: Chord{42, 97, 100}, err: ErrChordOverlap},
		{name: "equal chords overlap", toggle: Chord{42, 54}, sel: Chord{54, 42}, err: ErrChordOverlap},
		{
			name: "layout switch superset", toggle: Chord{42, 56, 97}, sel: nil,
			layoutSwitch: layoutSwitch, err: ErrChordContainsLayoutSwitch,
		},
		{
			name: "selection contains layout switch", toggle: nil, sel: Chord{42, 56},
			layoutSwitch: layoutSwitch, err: ErrChordContainsLayoutSwitch,
		},
		{name: "partial layout switch ok", toggle: Chord{42, 54}, sel: Chord{42, 97, 100}, layoutSwitch: layoutSwitch},
		{name: "no layout switch keys", toggle: Chord{42, 56}, sel: nil},
		{name: "convert key member", toggle: Chord{97, 119}, sel: nil, convertKey: 119, err: ErrChordContainsConvertKey},
		{name: "convert key zero ignored", toggle: Chord{42, 54}, sel: nil, convertKey: 0},
		{name: "invalid chord", toggle: Chord{42, 30}, sel: nil, err: ErrChordTextKey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateChords(tt.toggle, tt.sel, tt.layoutSwitch, tt.convertKey)
			if tt.err == nil {
				if err != nil {
					t.Fatalf("ValidateChords = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.err) {
				t.Fatalf("ValidateChords = %v, want %v", err, tt.err)
			}
		})
	}
}
