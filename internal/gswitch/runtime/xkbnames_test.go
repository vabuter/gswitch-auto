package runtime

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"testing"
)

const renderTestEvdevKeycodes = "/usr/share/X11/xkb/keycodes/evdev"

// renderTestExpectedNames builds the expected scancode → name table from the
// row ranges of SPEC §3.2.1.
func renderTestExpectedNames() map[uint16]string {
	want := map[uint16]string{41: "TLDE", 43: "BKSL", 86: "LSGT"}
	rows := []struct {
		prefix   string
		from, to uint16
	}{
		{"AE", 2, 13},
		{"AD", 16, 27},
		{"AC", 30, 40},
		{"AB", 44, 53},
	}
	for _, r := range rows {
		for c := r.from; c <= r.to; c++ {
			want[c] = fmt.Sprintf("%s%02d", r.prefix, c-r.from+1)
		}
	}
	return want
}

func TestXKBKeyNameTable(t *testing.T) {
	want := renderTestExpectedNames()
	if len(want) != 48 {
		t.Fatalf("expected table has %d entries, want 48", len(want))
	}
	seen := make(map[string]uint16)
	count := 0
	for code := 0; code <= 0xffff; code++ {
		name, ok := XKBKeyName(uint16(code))
		wantName, wantOK := want[uint16(code)]
		if ok != wantOK || name != wantName {
			t.Fatalf("XKBKeyName(%d) = %q, %v; want %q, %v", code, name, ok, wantName, wantOK)
		}
		if !ok {
			if name != "" {
				t.Fatalf("XKBKeyName(%d) returned name %q with ok=false", code, name)
			}
			continue
		}
		count++
		if prev, dup := seen[name]; dup {
			t.Fatalf("XKBKeyName(%d) and XKBKeyName(%d) both return %q", prev, code, name)
		}
		seen[name] = uint16(code)
	}
	if count != 48 {
		t.Fatalf("XKBKeyName maps %d codes, want 48", count)
	}
}

func TestXKBKeyNameXKeycodes(t *testing.T) {
	// X keycode = evdev scancode + 8.
	for name, xcode := range map[string]int{
		"TLDE": 49, "AE01": 10, "AD01": 24, "AC01": 38, "BKSL": 51, "AB01": 52, "LSGT": 94,
	} {
		got, ok := XKBKeyName(uint16(xcode - 8))
		if !ok || got != name {
			t.Errorf("XKBKeyName(%d) = %q, %v; want %q (X keycode %d)", xcode-8, got, ok, name, xcode)
		}
	}
}

func TestXKBKeyNameRejectsNonRenderableKeys(t *testing.T) {
	codes := []uint16{0, KEY_ESC, KEY_BACKSPACE, KEY_TAB, KEY_ENTER, KEY_LEFTCTRL, KEY_LEFTSHIFT,
		KEY_RIGHTSHIFT, 55, KEY_LEFTALT, KEY_SPACE, KEY_CAPSLOCK, KEY_NUMLOCK, KEY_KPENTER, 98,
		KEY_RIGHTALT, KEY_DELETE, KEY_LEFTMETA, KEY_RIGHTMETA, 87, 0x2ff, 0xffff}
	for c := uint16(71); c <= 83; c++ {
		codes = append(codes, c)
	}
	for _, code := range codes {
		if name, ok := XKBKeyName(code); ok {
			t.Errorf("XKBKeyName(%d) = %q, want ok=false", code, name)
		}
	}
}

// TestXKBKeyNameMatchesEvdevKeycodes cross-checks the static table against the
// installed XKB evdev keycodes (minus 8).
func TestXKBKeyNameMatchesEvdevKeycodes(t *testing.T) {
	data, err := os.ReadFile(renderTestEvdevKeycodes)
	if err != nil {
		t.Skipf("XKB evdev keycodes not available: %v", err)
	}
	re := regexp.MustCompile(`(?m)^\s*<(\w+)>\s*=\s*(\d+)\s*;`)
	xcodes := make(map[string]int)
	for _, m := range re.FindAllStringSubmatch(string(data), -1) {
		n, err := strconv.Atoi(m[2])
		if err != nil {
			t.Fatalf("bad keycode line %q: %v", m[0], err)
		}
		xcodes[m[1]] = n
	}
	for code, name := range renderTestExpectedNames() {
		xcode, ok := xcodes[name]
		if !ok {
			t.Errorf("%s missing from %s", name, renderTestEvdevKeycodes)
			continue
		}
		if xcode-8 != int(code) {
			t.Errorf("%s: evdev keycodes give %d (scancode %d), table has scancode %d", name, xcode, xcode-8, code)
		}
	}
}
