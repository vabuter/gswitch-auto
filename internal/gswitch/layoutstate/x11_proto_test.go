package layoutstate

import (
	"errors"
	"reflect"
	"testing"

	"github.com/jezek/xgb/xproto"
)

// TestX11LayoutRequestEncoding keeps the hand-built XKB requests byte-identical
// to upstream's (runtime/x11_layout_switch_test.go TestXKBStateRequestEncoding).
func TestX11LayoutRequestEncoding(t *testing.T) {
	const opcode = 135
	if got, want := xkbUseExtensionRequest(opcode), []byte{135, 0, 2, 0, 1, 0, 0, 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("xkbUseExtensionRequest() = %v, want %v", got, want)
	}
	if got, want := xkbGetStateRequest(opcode), []byte{135, 4, 2, 0, 0, 1, 0, 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("xkbGetStateRequest() = %v, want %v", got, want)
	}
}

func TestXKBGroupFromStateReply(t *testing.T) {
	reply := make([]byte, 32)
	reply[0] = 1 // X_Reply
	reply[12] = 1
	reply[13] = 0 // lockedGroup is ignored
	group, err := xkbGroupFromStateReply(reply)
	if err != nil || group != 1 {
		t.Fatalf("xkbGroupFromStateReply() = %d, %v; want 1, nil", group, err)
	}
	if _, err := xkbGroupFromStateReply(reply[:12]); err == nil {
		t.Fatal("xkbGroupFromStateReply(short) error = nil, want short reply error")
	}
}

func TestXKBVersionFromUseExtensionReply(t *testing.T) {
	reply := make([]byte, 32)
	reply[0], reply[1] = 1, 1
	reply[8], reply[9] = 1, 0 // serverMajor 1
	reply[10], reply[11] = 0, 0
	major, minor, err := xkbVersionFromUseExtensionReply(reply)
	if err != nil || major != 1 || minor != 0 {
		t.Fatalf("xkbVersionFromUseExtensionReply() = %d.%d, %v; want 1.0, nil", major, minor, err)
	}
	reply[1] = 0
	if _, _, err := xkbVersionFromUseExtensionReply(reply); !errors.Is(err, errXKBUnsupported) {
		t.Fatalf("unsupported reply error = %v, want %v", err, errXKBUnsupported)
	}
	if _, _, err := xkbVersionFromUseExtensionReply(reply[:8]); err == nil {
		t.Fatal("short reply error = nil, want short reply error")
	}
}

func TestParseXKBRulesNames(t *testing.T) {
	tests := []struct {
		name string
		data string
		want []Layout
		ok   bool
	}{
		{"gnome us,ru", "evdev\x00pc105\x00us,ru\x00,\x00grp:alt_shift_toggle\x00", []Layout{{Name: "us"}, {Name: "ru"}}, true},
		{"variant on first", "evdev\x00pc105\x00us,ru\x00dvorak,\x00\x00", []Layout{{Name: "us", Variant: "dvorak"}, {Name: "ru"}}, true},
		{"no trailing NUL", "evdev\x00pc105\x00us,ru\x00,phonetic\x00", []Layout{{Name: "us"}, {Name: "ru", Variant: "phonetic"}}, true},
		{"three layouts", "evdev\x00pc105\x00us,ru,de\x00,,nodeadkeys\x00", []Layout{{Name: "us"}, {Name: "ru"}, {Name: "de", Variant: "nodeadkeys"}}, true},
		{"gnome locale layout appended", "evdev\x00pc105\x00us,ru,us\x00,,\x00\x00", []Layout{{Name: "us"}, {Name: "ru"}, {Name: "us"}}, true},
		{"single layout (Xwayland)", "evdev\x00pc105\x00us\x00\x00\x00", []Layout{{Name: "us"}}, true},
		{"missing variants field", "evdev\x00pc105\x00us,ru", []Layout{{Name: "us"}, {Name: "ru"}}, true},
		{"empty layouts", "evdev\x00pc105\x00\x00\x00\x00", nil, false},
		{"empty", "", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseXKBRulesNames([]byte(tt.data))
			if ok != tt.ok || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseXKBRulesNames(%q) = %v, %v; want %v, %v", tt.data, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestXKBRulesNamesFields(t *testing.T) {
	got := xkbRulesNamesFields([]byte("evdev\x00pc105\x00us,ru\x00,\x00grp:alt_shift_toggle\x00"))
	want := []string{"evdev", "pc105", "us,ru", ",", "grp:alt_shift_toggle"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("xkbRulesNamesFields() = %q, want %q", got, want)
	}
	if got := xkbRulesNamesFields([]byte("evdev")); !reflect.DeepEqual(got, []string{"evdev", "", "", "", ""}) {
		t.Fatalf("xkbRulesNamesFields(short) = %q", got)
	}
}

func TestEngineForGroup(t *testing.T) {
	table := []Layout{{Name: "us"}, {Name: "ru", Variant: "phonetic"}}
	tests := []struct {
		group int
		want  string
		ok    bool
	}{
		{0, "xkb:us::", true},
		{1, "xkb:ru:phonetic:", true},
		{2, "", false},
		{-1, "", false},
	}
	for _, tt := range tests {
		got, ok := EngineForGroup(table, tt.group)
		if got != tt.want || ok != tt.ok {
			t.Errorf("EngineForGroup(%d) = %q, %v; want %q, %v", tt.group, got, ok, tt.want, tt.ok)
		}
	}
	// The synthesized names round-trip through the tracker's parser and pair mapping.
	for i, want := range []int{0, 1} {
		engine, _ := EngineForGroup(table, i)
		layout, variant, ok := ParseXKBEngine(engine)
		if !ok {
			t.Fatalf("ParseXKBEngine(%q) ok = false", engine)
		}
		if got := pairIndex(usRu, Layout{Name: layout, Variant: variant}); got != want {
			t.Errorf("pairIndex(%q) = %d, want %d", engine, got, want)
		}
	}
	if _, ok := EngineForGroup(nil, 0); ok {
		t.Error("EngineForGroup(nil, 0) ok = true")
	}
	// GNOME's appended locale layout (third entry) maps by name like the others,
	// so the never-locked third group still yields a pair index.
	gnome, _ := ParseXKBRulesNames([]byte("evdev\x00pc105\x00us,ru,us\x00,,\x00\x00"))
	for group, want := range []int{0, 1, 0} {
		engine, ok := EngineForGroup(gnome, group)
		if !ok {
			t.Fatalf("EngineForGroup(us,ru,us, %d) ok = false", group)
		}
		layout, variant, _ := ParseXKBEngine(engine)
		if got := pairIndex(usRu, Layout{Name: layout, Variant: variant}); got != want {
			t.Errorf("group %d (%q) maps to index %d, want %d", group, engine, got, want)
		}
	}
}

func TestX11DescribeQueued(t *testing.T) {
	ev := xproto.MappingNotifyEvent{Sequence: 7, Request: xproto.MappingKeyboard, FirstKeycode: 8, Count: 248}
	if got, want := x11DescribeQueued(ev, nil), "event MappingNotify {Sequence: 7, Request: 1, FirstKeycode: 8, Count: 248}"; got != want {
		t.Errorf("x11DescribeQueued(event) = %q, want %q", got, want)
	}
	xerr := xproto.ValueError{NiceName: "Value", Sequence: 9, BadValue: 3, MajorOpcode: 135}
	if got, want := x11DescribeQueued(nil, xerr), "error BadValue {NiceName: Value, Sequence: 9, BadValue: 3, MinorOpcode: 0, MajorOpcode: 135}"; got != want {
		t.Errorf("x11DescribeQueued(error) = %q, want %q", got, want)
	}
}
