package tray

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/gotk3/gotk3/gdk"
	"github.com/gotk3/gotk3/gtk"

	gsconfig "github.com/arumata/gswitch/internal/gswitch/config"
	"github.com/arumata/gswitch/internal/gswitch/detect"
	"github.com/arumata/gswitch/internal/gswitch/userconfig"
)

const xkbKeycodeOffset uint16 = 8

// gdkHardwareKeycodeToEvdev converts the XKB keycode reported by GDK on
// X11 and Wayland to the Linux evdev scancode used by gswitch.
func gdkHardwareKeycodeToEvdev(hardwareKeycode uint16) (uint16, bool) {
	if hardwareKeycode <= xkbKeycodeOffset {
		return 0, false
	}
	return hardwareKeycode - xkbKeycodeOffset, true
}

// keyPickerCapture keeps the last chord after its keys are released.
type keyPickerCapture struct {
	pressed map[uint16]string
	saved   map[uint16]string
}

func (c *keyPickerCapture) press(hardware uint16, source string) bool {
	code, ok := gdkHardwareKeycodeToEvdev(hardware)
	if !ok || isXTestKeyboard(source) {
		return false
	}
	if c.pressed == nil {
		c.pressed = make(map[uint16]string)
	}
	c.pressed[code] = detect.ScancodesToKeyNames([]uint16{code})
	c.saved = maps.Clone(c.pressed)
	return true
}

func (c *keyPickerCapture) release(hardware uint16, source string) {
	if code, ok := gdkHardwareKeycodeToEvdev(hardware); ok && !isXTestKeyboard(source) {
		delete(c.pressed, code)
	}
}

// clearPressed forgets the keys believed to be held (focus-out or a broken
// grab loses their releases) and keeps the saved chord.
func (c *keyPickerCapture) clearPressed() {
	clear(c.pressed)
}

// XTest injectors such as xcape emit a second chord after a physical modifier
// is released. It must not overwrite the physical key selected by the user.
func isXTestKeyboard(source string) bool {
	return strings.HasSuffix(source, " XTEST keyboard")
}

// isModifier checks if scancode is a modifier key.
// Modifiers: Shift (42, 54), Ctrl (29, 97), Alt (56, 100), Super (125, 126)
func isModifier(code uint16) bool {
	return gsconfig.IsModifierKey(code)
}

// sortScancodes sorts keycodes: modifiers first, main key last.
// Example: [57, 125] (Space, Super) -> [125, 57] (Super, Space)
func sortScancodes(codes []uint16) []uint16 {
	if len(codes) <= 1 {
		return codes
	}

	modifiers := make([]uint16, 0, len(codes))
	others := make([]uint16, 0, len(codes))

	for _, code := range codes {
		if isModifier(code) {
			modifiers = append(modifiers, code)
		} else {
			others = append(others, code)
		}
	}

	// Sort modifiers and others for stable output
	slices.Sort(modifiers)
	slices.Sort(others)

	return append(modifiers, others...)
}

// reorderKeyNames reorders key names to match sorted scancodes.
func reorderKeyNames(sortedCodes []uint16, codeToName map[uint16]string) []string {
	names := make([]string, len(sortedCodes))
	for i, code := range sortedCodes {
		names[i] = codeToName[code]
	}
	return names
}

// KeyPickerResult represents the result of the key picker dialog.
type KeyPickerResult struct {
	KeyCodes []uint16 // List of hardware keycodes (scancodes)
	KeyNames []string // List of key names for display
	Value    string   // Config value (e.g., "29+42" for Ctrl+Shift)
}

// KeyPickerContext indicates what key is being selected.
type KeyPickerContext int

const (
	// KeyPickerForLayoutSwitch - selecting the layout switch key.
	KeyPickerForLayoutSwitch KeyPickerContext = iota
	// KeyPickerForConvertKey - selecting the convert key.
	KeyPickerForConvertKey
	// KeyPickerForChordToggleAuto - selecting the auto mode toggle chord.
	KeyPickerForChordToggleAuto
	// KeyPickerForChordConvertSelection - selecting the selection conversion chord.
	KeyPickerForChordConvertSelection
)

// Keys that confirm (Enter, KP_Enter) or cancel (Escape) a chord picker when
// pressed alone instead of being captured.
const (
	keyPickerEnter   uint16 = 28
	keyPickerKPEnter uint16 = 96
	keyPickerEscape  uint16 = 1
)

func isChordPickerContext(context KeyPickerContext) bool {
	return context == KeyPickerForChordToggleAuto || context == KeyPickerForChordConvertSelection
}

// keyPickerPassthrough reports whether a key press in a chord context is a
// bare Enter/KP_Enter/Escape (nothing else held) that confirms or cancels the
// dialog instead of being captured.
func keyPickerPassthrough(context KeyPickerContext, hardware uint16, pressed map[uint16]string) bool {
	if !isChordPickerContext(context) || len(pressed) != 0 {
		return false
	}
	code, ok := gdkHardwareKeycodeToEvdev(hardware)
	if !ok {
		return false
	}
	return code == keyPickerEnter || code == keyPickerKPEnter || code == keyPickerEscape
}

// keyPickerSuspend holds the hook that suspends (true) or resumes (false)
// the daemon's gestures and chords while a picker captures keys. The tray's
// daemon client installs it; it must not block.
var keyPickerSuspend atomic.Pointer[func(bool)]

func setKeyPickerSuspend(fn func(bool)) {
	if fn == nil {
		keyPickerSuspend.Store(nil)
		return
	}
	keyPickerSuspend.Store(&fn)
}

func suspendHotkeysForPicker(suspend bool) {
	if fn := keyPickerSuspend.Load(); fn != nil {
		(*fn)(suspend)
	}
}

// keyPickerTitle returns the dialog title based on context.
func keyPickerTitle(context KeyPickerContext) string {
	switch context {
	case KeyPickerForLayoutSwitch:
		return tr(strKeyPickerTitleLayoutSwitch)
	case KeyPickerForConvertKey:
		return tr(strKeyPickerTitleConvertKey)
	case KeyPickerForChordToggleAuto:
		return tr(strGAKeyPickerTitleToggle)
	case KeyPickerForChordConvertSelection:
		return tr(strGAKeyPickerTitleSelection)
	default:
		return tr(strKeyPickerTitleConvertKey)
	}
}

// keyPickerWarningLabel returns the warning label text based on context.
func keyPickerWarningLabel(context KeyPickerContext) string {
	switch context {
	case KeyPickerForLayoutSwitch:
		return tr(strKeyPickerCurrentConvertKey)
	case KeyPickerForConvertKey:
		return tr(strKeyPickerCurrentLayoutSwitch)
	default:
		return tr(strKeyPickerCurrentLayoutSwitch)
	}
}

func keyPickerHint(context KeyPickerContext) string {
	if context == KeyPickerForConvertKey {
		return tr(strKeyPickerHintConvert)
	}
	if isChordPickerContext(context) {
		return tr(strGAKeyPickerHintChord)
	}
	return tr(strKeyPickerHint)
}

func keySelectionValid(context KeyPickerContext, codes []uint16) bool {
	if len(codes) == 0 {
		return false
	}
	if isChordPickerContext(context) {
		return userconfig.ValidateChord(chordFromCodes(codes)) == nil
	}
	if context != KeyPickerForConvertKey {
		return true
	}
	return len(codes) == 1 && gsconfig.ValidateConvertKey(codes[0]) == nil
}

// keySelectionMessage explains a rejected choice; an empty initial selection
// needs only the instruction, not an error.
func keySelectionMessage(context KeyPickerContext, codes []uint16) string {
	if isChordPickerContext(context) && len(codes) > 0 {
		if err := userconfig.ValidateChord(chordFromCodes(codes)); err != nil {
			return chordErrorMessage(err)
		}
		return ""
	}
	if context != KeyPickerForConvertKey || len(codes) == 0 {
		return ""
	}
	if len(codes) > 1 {
		return tr(strKeyPickerCombinationRejected)
	}
	if gsconfig.ValidateConvertKey(codes[0]) != nil {
		return fmt.Sprintf(tr(strKeyPickerModifierRejected), detect.ScancodesToKeyNames(codes))
	}
	return ""
}

func updateKeySelectionFeedback(label *gtk.Label, okButton *gtk.Button, context KeyPickerContext, codes []uint16) {
	message := keySelectionMessage(context, codes)
	label.SetText(message)
	label.SetVisible(message != "")
	if okButton != nil {
		okButton.SetSensitive(keySelectionValid(context, codes))
	}
}

// ShowKeyPickerDialog shows a dialog for capturing a key or key combination.
// context specifies what key is being selected (layout switch or convert key).
// otherKeyValue is the other key's current value to display as warning (to avoid conflicts).
// Returns the result and true if a key was selected, or empty result and false if canceled.
func ShowKeyPickerDialog(parent *gtk.Window, context KeyPickerContext, otherKeyValue string) (KeyPickerResult, bool) {
	dialog, err := gtk.DialogNew()
	if err != nil {
		return KeyPickerResult{}, false
	}
	defer dialog.Destroy()

	dialog.SetTitle(keyPickerTitle(context))
	dialog.SetTransientFor(parent)
	dialog.SetModal(true)
	dialog.SetDefaultSize(350, 200)
	dialog.SetPosition(gtk.WIN_POS_CENTER_ON_PARENT)
	dialog.SetBorderWidth(10)

	contentArea, err := dialog.GetContentArea()
	if err != nil {
		return KeyPickerResult{}, false
	}

	box, err := gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 10)
	if err != nil {
		return KeyPickerResult{}, false
	}
	box.SetMarginTop(10)
	box.SetMarginBottom(10)
	box.SetMarginStart(10)
	box.SetMarginEnd(10)

	instructionLabel, err := gtk.LabelNew(tr(strKeyPickerInstruction))
	if err != nil {
		return KeyPickerResult{}, false
	}
	box.PackStart(instructionLabel, false, false, 0)

	keyLabel, err := gtk.LabelNew("")
	if err != nil {
		return KeyPickerResult{}, false
	}
	keyLabel.SetMarkup("<span size='x-large' weight='bold'>-</span>")
	box.PackStart(keyLabel, true, true, 10)

	scancodeLabel, err := gtk.LabelNew("")
	if err != nil {
		return KeyPickerResult{}, false
	}
	scancodeLabel.SetMarkup("<span color='gray'>" + tr(strKeyPickerScancode) + " -</span>")
	box.PackStart(scancodeLabel, false, false, 0)

	validationLabel, err := gtk.LabelNew("")
	if err != nil {
		return KeyPickerResult{}, false
	}
	validationLabel.SetName("key-picker-validation")
	validationLabel.SetLineWrap(true)
	validationLabel.SetMaxWidthChars(44)
	validationLabel.SetNoShowAll(true)
	if style, _ := validationLabel.GetStyleContext(); style != nil {
		style.AddClass("error")
	}
	box.PackStart(validationLabel, false, false, 0)

	hintLabel, err := gtk.LabelNew(keyPickerHint(context))
	if err != nil {
		return KeyPickerResult{}, false
	}
	hintLabel.SetLineWrap(true)
	if ctx, _ := hintLabel.GetStyleContext(); ctx != nil {
		ctx.AddClass("dim-label")
	}
	box.PackStart(hintLabel, false, false, 10)

	// Show other key warning if set (to help avoid conflicts)
	if otherKeyValue != "" && otherKeyValue != "custom" {
		if currentKeyLabel, err := gtk.LabelNew(""); err == nil {
			currentKeyLabel.SetMarkup(fmt.Sprintf("<span color='#cc6600'>%s <b>%s</b></span>",
				keyPickerWarningLabel(context), formatKeyValue(otherKeyValue)))
			currentKeyLabel.SetLineWrap(true)
			box.PackStart(currentKeyLabel, false, false, 5)
		}
	}

	contentArea.Add(box)

	// Add buttons
	if _, err := dialog.AddButton(tr(strButtonCancel), gtk.RESPONSE_CANCEL); err != nil {
		return KeyPickerResult{}, false
	}
	okButton, err := dialog.AddButton(tr(strButtonOK), gtk.RESPONSE_OK)
	if err != nil {
		return KeyPickerResult{}, false
	}
	if okButton != nil {
		okButton.SetSensitive(false) // Disable until a key is pressed
	}

	// State for capturing keys
	var result KeyPickerResult
	capture := &keyPickerCapture{}

	dialog.Connect("key-press-event", func(_ *gtk.Dialog, event *gdk.Event) bool {
		keyEvent := gdk.EventKeyNewFromEvent(event)
		if keyPickerPassthrough(context, keyEvent.HardwareKeyCode(), capture.pressed) {
			// Confirm or cancel instead of capturing; the focused button
			// must not decide what Enter does.
			if code, _ := gdkHardwareKeycodeToEvdev(keyEvent.HardwareKeyCode()); code == keyPickerEscape {
				dialog.Response(gtk.RESPONSE_CANCEL)
			} else if keySelectionValid(context, slices.Collect(maps.Keys(capture.saved))) {
				dialog.Response(gtk.RESPONSE_OK)
			}
			return true
		}
		if !capture.press(keyEvent.HardwareKeyCode(), keyEventSourceName(event)) {
			return true
		}
		updateKeyDisplay(keyLabel, scancodeLabel, capture.saved)
		selectedCodes := slices.Collect(maps.Keys(capture.saved))
		updateKeySelectionFeedback(validationLabel, okButton, context, selectedCodes)
		return true
	})

	dialog.Connect("key-release-event", func(_ *gtk.Dialog, event *gdk.Event) bool {
		keyEvent := gdk.EventKeyNewFromEvent(event)
		capture.release(keyEvent.HardwareKeyCode(), keyEventSourceName(event))
		return true
	})

	// Releases are lost while the dialog has no keyboard focus.
	dialog.Connect("focus-out-event", func() bool {
		capture.clearPressed()
		return false
	})
	dialog.Connect("grab-broken-event", func() bool {
		capture.clearPressed()
		return false
	})

	dialog.ShowAll()
	// The daemon must not act on the keys typed into the picker.
	suspendHotkeysForPicker(true)
	response := dialog.Run()
	suspendHotkeysForPicker(false)

	if response == gtk.RESPONSE_OK && len(capture.saved) > 0 {
		// Collect keycodes from saved combination
		codes := make([]uint16, 0, len(capture.saved))
		for code := range capture.saved {
			codes = append(codes, code)
		}

		// Sort: modifiers first, main key last (fixes bug with Super+Space showing as "57+125")
		result.KeyCodes = sortScancodes(codes)
		result.KeyNames = reorderKeyNames(result.KeyCodes, capture.saved)

		// Build config value string
		codeStrings := make([]string, len(result.KeyCodes))
		for i, code := range result.KeyCodes {
			codeStrings[i] = strconv.FormatUint(uint64(code), 10)
		}
		result.Value = strings.Join(codeStrings, "+")

		return result, true
	}

	return KeyPickerResult{}, false
}

// updateKeyDisplay updates the key and scancode labels.
func updateKeyDisplay(keyLabel, scancodeLabel *gtk.Label, pressedKeys map[uint16]string) {
	if len(pressedKeys) == 0 {
		keyLabel.SetMarkup("<span size='x-large' weight='bold'>-</span>")
		scancodeLabel.SetMarkup("<span color='gray'>" + tr(strKeyPickerScancode) + " -</span>")
		return
	}

	// Collect and sort keys: modifiers first, main key last
	codes := make([]uint16, 0, len(pressedKeys))
	for code := range pressedKeys {
		codes = append(codes, code)
	}
	codes = sortScancodes(codes)

	// Build display strings
	names := make([]string, len(codes))
	codeStrings := make([]string, len(codes))
	for i, code := range codes {
		names[i] = pressedKeys[code]
		codeStrings[i] = strconv.FormatUint(uint64(code), 10)
	}

	keyLabel.SetMarkup(fmt.Sprintf("<span size='x-large' weight='bold'>%s</span>",
		strings.Join(names, " + ")))
	scancodeLabel.SetMarkup(fmt.Sprintf("<span color='gray'>%s %s</span>",
		tr(strKeyPickerScancode), strings.Join(codeStrings, "+")))
}

// formatKeyValue converts a key value to human-readable format.
func formatKeyValue(value string) string {
	// Known presets
	knownValues := map[string]string{
		"0":      tr(strDoubleShift),
		"58":     "Caps Lock",
		"29+42":  "LCtrl+LShift",
		"56+42":  "LAlt+LShift",
		"125+57": "LSuper+Space",
		"119":    "Pause/Break",
		"70":     "Scroll Lock",
	}

	if name, ok := knownValues[value]; ok {
		return name
	}

	// Return as-is (scancode)
	return value
}

// formatCustomKeyLabel rebuilds the label for a persisted custom evdev key
// value so reopening the settings window shows the same human-readable key.
func formatCustomKeyLabel(value string) string {
	parts := strings.Split(value, "+")
	codes := make([]uint16, 0, len(parts))
	for _, part := range parts {
		code, err := strconv.ParseUint(part, 10, 16)
		if err != nil {
			return fmt.Sprintf(tr(strCustomKey), value)
		}
		codes = append(codes, uint16(code))
	}

	return fmt.Sprintf("%s (%s)", detect.ScancodesToKeyNames(codes), value)
}

// GetKeyNameFromCode returns a human-readable name for a keycode.
func GetKeyNameFromCode(code uint16) string {
	knownKeys := map[uint16]string{
		29:  "LCtrl",
		42:  "LShift",
		54:  "RShift",
		56:  "LAlt",
		57:  "Space",
		58:  "Caps_Lock",
		70:  "Scroll_Lock",
		97:  "RCtrl",
		100: "RAlt",
		119: "Pause",
		125: "LSuper",
		126: "RSuper",
	}

	if name, ok := knownKeys[code]; ok {
		return name
	}
	return "Key_" + strconv.FormatUint(uint64(code), 10)
}
