package tray

import (
	"slices"
	"strings"
	"testing"

	"github.com/arumata/gswitch/internal/gswitch/userconfig"
)

// gaTestHostAccels are the GNOME bindings of the target host (§1.3).
var gaTestHostAccels = []string{"<Alt>Shift_L", "<Shift>Alt_L"}

func TestFormatChordLabel(t *testing.T) {
	for _, tt := range []struct {
		chord userconfig.Chord
		want  string
	}{
		{userconfig.Chord{42, 54}, "LShift+RShift"},
		{userconfig.Chord{100, 97, 42}, "LShift+RCtrl+RAlt"},
		{userconfig.Chord{59, 29}, "LCtrl+F1"},
		{userconfig.Chord{125, 88}, "LSuper+F12"},
		{userconfig.Chord{56, 119}, "LAlt+Pause"},
		{userconfig.Chord{42, 183}, "LShift+F13"},
		{nil, ""},
	} {
		if got := formatChordLabel(tt.chord); got != tt.want {
			t.Errorf("formatChordLabel(%v) = %q, want %q", tt.chord, got, tt.want)
		}
	}
	gaTestUseLanguage(t, "en")
	if got := chordDisplay(nil); got != "Disabled" {
		t.Fatalf("chordDisplay(nil) = %q", got)
	}
}

func TestHotkeyPresetMapping(t *testing.T) {
	gaTestUseLanguage(t, "en")
	toggleDefault := userconfig.Chord{42, 54}
	selectionDefault := userconfig.Chord{42, 97, 100}
	if !hotkeyToggleAuto.defaultChord().Equal(toggleDefault) || !hotkeyConvertSelection.defaultChord().Equal(selectionDefault) {
		t.Fatal("hotkey defaults differ from the user configuration defaults")
	}
	labels := hotkeyComboLabels(hotkeyConvertSelection, nil)
	if want := []string{"Default (LShift+RCtrl+RAlt)", "Disabled", "Custom..."}; !slices.Equal(labels, want) {
		t.Fatalf("labels = %q, want %q", labels, want)
	}
	if labels := hotkeyComboLabels(hotkeyToggleAuto, toggleDefault); len(labels) != 3 || labels[0] != "Default (LShift+RShift)" {
		t.Fatalf("default chord labels = %q", labels)
	}
	custom := userconfig.Chord{29, 97}
	if labels := hotkeyComboLabels(hotkeyToggleAuto, custom); len(labels) != 4 || labels[3] != "LCtrl+RCtrl" {
		t.Fatalf("custom chord labels = %q", labels)
	}
	for _, tt := range []struct {
		chord userconfig.Chord
		want  int
	}{
		{toggleDefault, hotkeyEntryDefault},
		{userconfig.Chord{54, 42}, hotkeyEntryDefault},
		{nil, hotkeyEntryDisabled},
		{custom, hotkeyEntryCustomValue},
	} {
		if got := hotkeyPresetIndex(hotkeyToggleAuto, tt.chord); got != tt.want {
			t.Errorf("hotkeyPresetIndex(%v) = %d, want %d", tt.chord, got, tt.want)
		}
	}
	if chord, ok := hotkeyChordAt(hotkeyToggleAuto, hotkeyEntryDefault, nil); !ok || !chord.Equal(toggleDefault) {
		t.Fatalf("default entry = %v, %v", chord, ok)
	}
	if chord, ok := hotkeyChordAt(hotkeyToggleAuto, hotkeyEntryDisabled, custom); !ok || chord != nil {
		t.Fatalf("disabled entry = %v, %v", chord, ok)
	}
	if _, ok := hotkeyChordAt(hotkeyToggleAuto, hotkeyEntryCustom, custom); ok {
		t.Fatal("Custom... must open the key picker")
	}
	if chord, ok := hotkeyChordAt(hotkeyToggleAuto, hotkeyEntryCustomValue, custom); !ok || !chord.Equal(custom) {
		t.Fatalf("custom entry = %v, %v", chord, ok)
	}
	if got := chordFromCodes([]uint16{100, 42, 97, 42}); !slices.Equal(got, userconfig.Chord{42, 97, 100}) {
		t.Fatalf("chordFromCodes = %v", got)
	}
}

func TestChordConflictText(t *testing.T) {
	gaTestUseLanguage(t, "en")
	// R4 default: RAlt held, then LShift fires <Alt>Shift_L (order dependent).
	errText, warnText := chordConflictText(userconfig.Chord{42, 97, 100}, gaTestHostAccels)
	if errText != "" || !strings.HasPrefix(warnText, "Press Shift first") {
		t.Fatalf("selection default: err=%q warn=%q", errText, warnText)
	}
	if errText, warnText := chordConflictText(userconfig.Chord{42, 54}, gaTestHostAccels); errText != "" || warnText != "" {
		t.Fatalf("toggle default: err=%q warn=%q", errText, warnText)
	}
	// LAlt+LShift fires a switch accelerator in every order.
	if errText, _ := chordConflictText(userconfig.Chord{42, 56}, gaTestHostAccels); errText != tr(strGAChordConflictSwitch) {
		t.Fatalf("always-conflicting chord: err=%q", errText)
	}
	if errText, warnText := chordConflictText(userconfig.Chord{42, 56}, nil); errText != "" || warnText != "" {
		t.Fatal("no accelerators (not GNOME) must not report conflicts")
	}
}

func TestChordErrorText(t *testing.T) {
	gaTestUseLanguage(t, "en")
	overlap := userconfig.ValidateChords(userconfig.Chord{42, 54}, userconfig.Chord{42, 54, 29}, nil, 0)
	if got := chordErrorText(overlap); got != tr(strGAChordOverlap) {
		t.Fatalf("overlap text = %q", got)
	}
	layout := userconfig.ValidateChords(userconfig.Chord{42, 56}, nil, []uint16{56, 42}, 0)
	if got, want := chordErrorText(layout), tr(strGALabelToggleChord)+" "+tr(strGAChordConflictSwitch); got != want {
		t.Fatalf("layout switch text = %q, want %q", got, want)
	}
	convert := userconfig.ValidateChords(nil, userconfig.Chord{42, 119}, nil, 119)
	if got, want := chordErrorText(convert), tr(strGALabelSelectionChord)+" "+tr(strGAChordConvertKey); got != want {
		t.Fatalf("convert key text = %q, want %q", got, want)
	}
	text := userconfig.ValidateChords(userconfig.Chord{42, 30}, nil, nil, 0)
	if got := chordErrorText(text); !strings.Contains(got, tr(strGAKeyPickerTextKey)) {
		t.Fatalf("text key message = %q", got)
	}
	if chordErrorText(nil) != "" {
		t.Fatal("nil error produced a message")
	}
}

func TestValidateHotkeys(t *testing.T) {
	gaTestUseLanguage(t, "en")
	defaults := userconfig.DefaultSettings()
	// Defaults on the host: the selection chord is only a warning.
	if got := validateHotkeys(defaults.ToggleChord, defaults.SelectionChord, []uint16{56, 42}, 0, gaTestHostAccels); got != "" {
		t.Fatalf("defaults rejected: %q", got)
	}
	got := validateHotkeys(userconfig.Chord{42, 56}, defaults.SelectionChord, nil, 0, gaTestHostAccels)
	if !strings.Contains(got, tr(strGAChordConflictSwitch)) {
		t.Fatalf("ConflictAlways not rejected: %q", got)
	}
	if got := validateHotkeys(nil, nil, []uint16{56, 42}, 0, gaTestHostAccels); got != "" {
		t.Fatalf("disabled chords rejected: %q", got)
	}
}

// TestHotkeyApplyMustValidate: a clash in chords the user did not change
// (the default LShift+RShift toggle with grp:shifts_toggle, layout switch
// [42,54]) must not block saving unrelated settings; a changed chord or a
// changed layout switch / convert key is validated.
func TestHotkeyApplyMustValidate(t *testing.T) {
	gaTestUseLanguage(t, "en")
	loaded := userconfig.DefaultSettings()
	prev := &TrayConfig{LayoutSwitch: "42+54", ConvertKey: "0", Layout1: "us", Layout2: "ru"}
	languageOnly := cloneTrayConfig(prev)
	languageOnly.Layout2 = "ua"

	// The scenario: the default toggle chord clashes with the layout switch.
	if text := validateHotkeys(loaded.ToggleChord, loaded.SelectionChord, chordLayoutSwitchKeys(prev.LayoutSwitch, nil),
		0, nil); !strings.Contains(text, tr(strGAChordConflictSwitch)) {
		t.Fatalf("expected a layout switch clash, got %q", text)
	}
	if hotkeyApplyMustValidate(loaded, loaded.ToggleChord, loaded.SelectionChord, prev, languageOnly) {
		t.Fatal("unchanged chords and layout switch block Apply")
	}
	if hotkeyApplyMustValidate(loaded, loaded.ToggleChord, loaded.SelectionChord, prev, nil) {
		t.Fatal("unchanged chords block Apply without an upstream config")
	}

	changedToggle := userconfig.Chord{29, 97}
	if !hotkeyApplyMustValidate(loaded, changedToggle, loaded.SelectionChord, prev, languageOnly) {
		t.Fatal("changed toggle chord not validated")
	}
	if !hotkeyApplyMustValidate(loaded, loaded.ToggleChord, nil, prev, languageOnly) {
		t.Fatal("disabled selection chord not validated")
	}
	newSwitch := cloneTrayConfig(prev)
	newSwitch.LayoutSwitch = "56+42"
	if !hotkeyApplyMustValidate(loaded, loaded.ToggleChord, loaded.SelectionChord, prev, newSwitch) {
		t.Fatal("changed layout switch not validated")
	}
	newConvert := cloneTrayConfig(prev)
	newConvert.ConvertKey = "54"
	if !hotkeyApplyMustValidate(loaded, loaded.ToggleChord, loaded.SelectionChord, prev, newConvert) {
		t.Fatal("changed convert key not validated")
	}
	if !hotkeyApplyMustValidate(loaded, loaded.ToggleChord, loaded.SelectionChord, nil, languageOnly) {
		t.Fatal("unknown loaded upstream config not validated")
	}
}

func TestChordClashText(t *testing.T) {
	gaTestUseLanguage(t, "en")
	if got := chordClashText(userconfig.Chord{42, 54}, []uint16{42, 54}, 0); got != tr(strGAChordConflictSwitch) {
		t.Fatalf("layout switch clash = %q", got)
	}
	if got := chordClashText(userconfig.Chord{42, 97, 100}, []uint16{56, 42}, 97); got != tr(strGAChordConvertKey) {
		t.Fatalf("convert key clash = %q", got)
	}
	if got := chordClashText(userconfig.Chord{42, 54}, []uint16{56, 42}, 0); got != "" {
		t.Fatalf("no clash = %q", got)
	}
	if got := chordClashText(nil, []uint16{42, 54}, 42); got != "" {
		t.Fatalf("disabled chord = %q", got)
	}
}

func TestChordLayoutSwitchKeysUsesDetectionScancodes(t *testing.T) {
	detected := &DetectionInfo{Status: TrayStatusOK, Scancodes: []uint16{56, 42}}
	if got := chordLayoutSwitchKeys("auto", detected); !slices.Equal(got, []uint16{56, 42}) {
		t.Fatalf("auto keys = %v", got)
	}
	if got := chordLayoutSwitchKeys("auto", nil); got != nil {
		t.Fatalf("auto without detection = %v", got)
	}
	if got := chordLayoutSwitchKeys("29+42", detected); !slices.Equal(got, []uint16{29, 42}) {
		t.Fatalf("explicit keys = %v", got)
	}
	if got := chordConvertKey("119"); got != 119 {
		t.Fatalf("convert key = %d", got)
	}
	if got := chordConvertKey("x"); got != 0 {
		t.Fatalf("invalid convert key = %d", got)
	}
	// The detected set feeds ValidateChords.
	if userconfig.ValidateChords(userconfig.Chord{42, 56, 29}, nil, chordLayoutSwitchKeys("auto", detected), 0) == nil {
		t.Fatal("chord containing the detected layout switch keys accepted")
	}
}

func TestDetectionInfoScancodes(t *testing.T) {
	output := []byte(`{"schema":1,"status":"found","result":{"scancodes":[56,42],"source":"gnome","raw":"<Alt>Shift_L","keys":"LAlt+LShift"},"attempts":[]}`)
	info := detectionInfoFromOutput(output, nil)
	if info.Status != TrayStatusOK || !slices.Equal(info.Scancodes, []uint16{56, 42}) || info.KeyNames != "LAlt+LShift" {
		t.Fatalf("detection info = %+v", info)
	}
	if info := detectionInfoFromOutput([]byte(`{"schema":1,"status":"not_found","attempts":[]}`), nil); info.Status != TrayStatusNeedsConfig || info.Scancodes != nil {
		t.Fatalf("not found = %+v", info)
	}
	for value, want := range map[string][]uint16{
		"56+42": {56, 42}, "58": {58}, "auto": nil, "custom": nil, "a+b": nil, "": nil, "0": nil,
	} {
		if got := layoutSwitchScancodes(value); !slices.Equal(got, want) {
			t.Errorf("layoutSwitchScancodes(%q) = %v, want %v", value, got, want)
		}
	}
}
