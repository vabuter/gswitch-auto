package userconfig

import (
	"strconv"
	"strings"
	"testing"
)

// ucTestHostAccels are the GNOME switch-input-source(-backward) bindings of
// the target host.
var ucTestHostAccels = []string{"<Alt>Shift_L", "<Shift>Alt_L"}

func TestSwitchConflictSelectionDefaultIsOrderDependent(t *testing.T) {
	level, desc := SwitchConflict(Chord{42, 97, 100}, ucTestHostAccels)
	if level != ConflictOrderDependent {
		t.Fatalf("level = %v, want %v (desc %q)", level, ConflictOrderDependent, desc)
	}
	// RAlt held, then LShift pressed before RCtrl fires <Alt>Shift_L.
	if !strings.Contains(desc, "RAlt, then LShift") || !strings.Contains(desc, "<Alt>Shift_L") {
		t.Fatalf("desc = %q, want the RAlt-first order and <Alt>Shift_L", desc)
	}
	if strings.Contains(desc, "ignored") {
		t.Fatalf("desc = %q carries an unexpected warning", desc)
	}
}

func TestSwitchConflictLevels(t *testing.T) {
	tests := []struct {
		name   string
		chord  Chord
		accels []string
		want   ConflictLevel
	}{
		{name: "toggle default", chord: Chord{42, 54}, accels: ucTestHostAccels, want: ConflictNone},
		{name: "alt+shift always", chord: Chord{56, 42}, accels: ucTestHostAccels, want: ConflictAlways},
		{name: "ralt+lshift order dependent", chord: Chord{100, 42}, accels: ucTestHostAccels, want: ConflictOrderDependent},
		{name: "no accelerators", chord: Chord{56, 42}, accels: nil, want: ConflictNone},
		{name: "disabled and empty entries", chord: Chord{56, 42}, accels: []string{"", "disabled"}, want: ConflictNone},
		{name: "nil chord", chord: nil, accels: ucTestHostAccels, want: ConflictNone},
		{name: "unproducible key", chord: Chord{125, 42}, accels: []string{"<Super>space"}, want: ConflictNone},
		{name: "primary is control", chord: Chord{29, 88}, accels: []string{"<Primary>F12"}, want: ConflictOrderDependent},
		{name: "control and ctrl spellings", chord: Chord{97, 88}, accels: []string{"<Ctrl>F12", "<Control>F12"}, want: ConflictOrderDependent},
		{name: "mod1 is alt", chord: Chord{100, 42}, accels: []string{"<Mod1>Shift_L"}, want: ConflictOrderDependent},
		{name: "exact modifier set required", chord: Chord{29, 56, 42}, accels: []string{"<Alt>Shift_L"}, want: ConflictOrderDependent},
		{name: "two modifiers", chord: Chord{29, 56, 42}, accels: []string{"<Control><Alt>Shift_L"}, want: ConflictOrderDependent},
		{name: "bare modifier key", chord: Chord{42, 54}, accels: []string{"Shift_L", "Shift_R"}, want: ConflictAlways},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			level, desc := SwitchConflict(tt.chord, tt.accels)
			if level != tt.want {
				t.Fatalf("SwitchConflict(%v, %q) = %v (%q), want %v", tt.chord, tt.accels, level, desc, tt.want)
			}
			if tt.want == ConflictNone && desc != "" {
				t.Fatalf("desc = %q, want empty for ConflictNone", desc)
			}
			if tt.want != ConflictNone && !strings.HasPrefix(desc, "pressing ") {
				t.Fatalf("desc = %q, want a press order description", desc)
			}
		})
	}
}

func TestSwitchConflictUnknownSyntax(t *testing.T) {
	level, desc := SwitchConflict(Chord{56, 42}, []string{"<Hyper>Shift_L", "<Alt", "<Alt>"})
	if level != ConflictNone {
		t.Fatalf("level = %v, want none", level)
	}
	if !strings.Contains(desc, "ignored unrecognised accelerator") || !strings.Contains(desc, "<Hyper>Shift_L") {
		t.Fatalf("desc = %q, want a warning naming the accelerator", desc)
	}

	// Known accelerators are still evaluated next to unknown ones.
	level, desc = SwitchConflict(Chord{56, 42}, []string{"<Hyper>x", "<Alt>Shift_L"})
	if level != ConflictOrderDependent {
		t.Fatalf("level = %v, want order-dependent", level)
	}
	if !strings.Contains(desc, "<Alt>Shift_L") || !strings.Contains(desc, "<Hyper>x") {
		t.Fatalf("desc = %q, want both the bad order and the warning", desc)
	}
}

func TestConflictLevelString(t *testing.T) {
	for level, want := range map[ConflictLevel]string{
		ConflictNone: "none", ConflictOrderDependent: "order-dependent", ConflictAlways: "always", 9: "unknown",
	} {
		if got := level.String(); got != want {
			t.Errorf("ConflictLevel(%d).String() = %q, want %q", level, got, want)
		}
	}
}

func TestPermuteVisitsEveryOrderOnce(t *testing.T) {
	seen := map[string]bool{}
	keys := []uint16{1, 2, 3, 4}
	permute(keys, 0, func(p []uint16) {
		seen[ucTestOrderString(p)] = true
	})
	if len(seen) != 24 {
		t.Fatalf("visited %d distinct orders, want 24", len(seen))
	}
	if ucTestOrderString(keys) != "1,2,3,4" {
		t.Fatalf("permute did not restore the input: %v", keys)
	}
}

// ucTestOrderString formats keys in their stored order.
func ucTestOrderString(keys []uint16) string {
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = strconv.Itoa(int(k))
	}
	return strings.Join(parts, ",")
}

func TestKeyLabel(t *testing.T) {
	for code, want := range map[uint16]string{
		42: "LShift", 100: "RAlt", 59: "F1", 68: "F10", 87: "F11", 88: "F12", 183: "F13", 194: "F24",
		119: "Pause", 30: "key 30",
	} {
		if got := keyLabel(code); got != want {
			t.Errorf("keyLabel(%d) = %q, want %q", code, got, want)
		}
	}
	_, desc := SwitchConflict(Chord{29, 88}, []string{"<Primary>F12"})
	if desc != "pressing LCtrl, then F12 triggers <Primary>F12" {
		t.Fatalf("desc = %q", desc)
	}
}
