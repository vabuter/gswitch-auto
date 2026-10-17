package tray

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gotk3/gotk3/glib"
	"github.com/gotk3/gotk3/gtk"

	"github.com/arumata/gswitch/internal/gswitch/detect"
	"github.com/arumata/gswitch/internal/gswitch/userconfig"
)

// hotkeyKind identifies one chord row of the "Hotkeys" frame.
type hotkeyKind int

const (
	hotkeyToggleAuto hotkeyKind = iota
	hotkeyConvertSelection
)

// Combo entries of a chord row. hotkeyEntryCustomValue exists only while a
// non-default custom chord is selected.
const (
	hotkeyEntryDefault = iota
	hotkeyEntryDisabled
	hotkeyEntryCustom
	hotkeyEntryCustomValue
)

func (k hotkeyKind) defaultChord() userconfig.Chord {
	defaults := userconfig.DefaultSettings()
	if k == hotkeyToggleAuto {
		return defaults.ToggleChord
	}
	return defaults.SelectionChord
}

func (k hotkeyKind) pickerContext() KeyPickerContext {
	if k == hotkeyToggleAuto {
		return KeyPickerForChordToggleAuto
	}
	return KeyPickerForChordConvertSelection
}

func (k hotkeyKind) label() string {
	if k == hotkeyToggleAuto {
		return tr(strGALabelToggleChord)
	}
	return tr(strGALabelSelectionChord)
}

// chordKeyName names one chord key, including the function keys and the
// other non-modifier keys that userconfig.ChordExtraKeys allows.
func chordKeyName(code uint16) string {
	switch {
	case code >= 59 && code <= 68:
		return "F" + strconv.Itoa(int(code)-58)
	case code == 87 || code == 88:
		return "F" + strconv.Itoa(int(code)-76)
	case code >= 183 && code <= 194:
		return "F" + strconv.Itoa(int(code)-170)
	case code == 119:
		return "Pause"
	case code == 70:
		return "ScrollLock"
	case code == 127:
		return "Menu"
	default:
		return detect.ScancodesToKeyNames([]uint16{code})
	}
}

// formatChordLabel renders a chord with modifiers first, e.g.
// "LShift+RCtrl+RAlt"; a disabled (empty) chord is "".
func formatChordLabel(c userconfig.Chord) string {
	if len(c) == 0 {
		return ""
	}
	codes := sortScancodes(slices.Clone([]uint16(c)))
	names := make([]string, len(codes))
	for i, code := range codes {
		names[i] = chordKeyName(code)
	}
	return strings.Join(names, "+")
}

// chordDisplay is formatChordLabel with "Disabled" for an empty chord.
func chordDisplay(c userconfig.Chord) string {
	if len(c) == 0 {
		return tr(strGAPresetDisabled)
	}
	return formatChordLabel(c)
}

// chordFromCodes builds a sorted chord from picker scancodes.
func chordFromCodes(codes []uint16) userconfig.Chord {
	c := userconfig.Chord(slices.Clone(codes))
	slices.Sort(c)
	return slices.Compact(c)
}

// hotkeyComboLabels returns the combo entries for a row: the default preset,
// "Disabled", "Custom…" and, when current is a non-default chord, a fourth
// entry that shows it.
func hotkeyComboLabels(kind hotkeyKind, current userconfig.Chord) []string {
	labels := []string{
		fmt.Sprintf(tr(strGAPresetDefault), formatChordLabel(kind.defaultChord())),
		tr(strGAPresetDisabled),
		tr(strGAPresetCustom),
	}
	if hotkeyPresetIndex(kind, current) == hotkeyEntryCustomValue {
		labels = append(labels, formatChordLabel(current))
	}
	return labels
}

// hotkeyPresetIndex returns the combo entry that shows current.
func hotkeyPresetIndex(kind hotkeyKind, current userconfig.Chord) int {
	switch {
	case len(current) == 0:
		return hotkeyEntryDisabled
	case current.Equal(kind.defaultChord()):
		return hotkeyEntryDefault
	default:
		return hotkeyEntryCustomValue
	}
}

// hotkeyChordAt maps a combo entry back to a chord. custom is the chord
// shown by hotkeyEntryCustomValue. ok is false for "Custom…" (the key picker
// decides) and for out-of-range entries.
func hotkeyChordAt(kind hotkeyKind, index int, custom userconfig.Chord) (userconfig.Chord, bool) {
	switch index {
	case hotkeyEntryDefault:
		return slices.Clone(kind.defaultChord()), true
	case hotkeyEntryDisabled:
		return nil, true
	case hotkeyEntryCustomValue:
		if len(custom) == 0 {
			return nil, false
		}
		return slices.Clone(custom), true
	default:
		return nil, false
	}
}

// chordConflictText classifies a chord against the GNOME layout switch
// accelerators: ConflictAlways gives an error text (Apply refuses it),
// ConflictOrderDependent a warning ("press Shift first"), anything else
// nothing.
func chordConflictText(c userconfig.Chord, accels []string) (errText, warnText string) {
	if len(c) == 0 || len(accels) == 0 {
		return "", ""
	}
	level, desc := userconfig.SwitchConflict(c, accels)
	switch level {
	case userconfig.ConflictAlways:
		return tr(strGAChordConflictSwitch), ""
	case userconfig.ConflictOrderDependent:
		return "", fmt.Sprintf(tr(strGAChordOrderWarning), desc)
	default:
		return "", ""
	}
}

// chordErrorMessage maps one userconfig chord error to a UI message.
func chordErrorMessage(err error) string {
	switch {
	case errors.Is(err, userconfig.ErrChordOverlap):
		return tr(strGAChordOverlap)
	case errors.Is(err, userconfig.ErrChordContainsLayoutSwitch):
		return tr(strGAChordConflictSwitch)
	case errors.Is(err, userconfig.ErrChordContainsConvertKey):
		return tr(strGAChordConvertKey)
	case errors.Is(err, userconfig.ErrChordTooShort):
		return tr(strGAKeyPickerTooShort)
	case errors.Is(err, userconfig.ErrChordTooLong):
		return tr(strGAKeyPickerTooLong)
	case errors.Is(err, userconfig.ErrChordNeedsModifier):
		return tr(strGAKeyPickerNeedsModifier)
	case errors.Is(err, userconfig.ErrChordTextKey):
		return tr(strGAKeyPickerTextKey)
	default:
		return err.Error()
	}
}

// chordErrorText turns the (joined) error of userconfig.ValidateChords into
// one line per problem, prefixed with the row label where it names one.
func chordErrorText(err error) string {
	if err == nil {
		return ""
	}
	leaves := []error{err}
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		leaves = joined.Unwrap()
	}
	lines := make([]string, 0, len(leaves))
	for _, leaf := range leaves {
		message := chordErrorMessage(leaf)
		text := leaf.Error()
		switch {
		case errors.Is(leaf, userconfig.ErrChordOverlap):
		case strings.HasPrefix(text, userconfig.KeyToggleChord+":"):
			message = hotkeyToggleAuto.label() + " " + message
		case strings.HasPrefix(text, userconfig.KeySelectionChord+":"):
			message = hotkeyConvertSelection.label() + " " + message
		}
		if !slices.Contains(lines, message) {
			lines = append(lines, message)
		}
	}
	return strings.Join(lines, "\n")
}

// chordLayoutSwitchKeys returns the layout switch keys that chords must not
// contain: the explicit layout-switch value, or the detected keys for "auto".
func chordLayoutSwitchKeys(layoutSwitch string, detection *DetectionInfo) []uint16 {
	if layoutSwitch == "auto" || layoutSwitch == "" {
		if detection == nil {
			return nil
		}
		return slices.Clone(detection.Scancodes)
	}
	return layoutSwitchScancodes(layoutSwitch)
}

// chordConvertKey parses the upstream convert-key value (0 = double Shift).
func chordConvertKey(value string) uint16 {
	code, err := strconv.ParseUint(strings.TrimSpace(value), 10, 16)
	if err != nil {
		return 0
	}
	return uint16(code)
}

// validateHotkeys checks both chords together (userconfig.ValidateChords)
// and against the GNOME layout switch accelerators (ConflictAlways is an
// error). It returns the UI text of every problem, or "".
func validateHotkeys(toggle, selection userconfig.Chord, layoutSwitch []uint16, convertKey uint16, accels []string) string {
	text := chordErrorText(userconfig.ValidateChords(toggle, selection, layoutSwitch, convertKey))
	lines := []string{}
	if text != "" {
		lines = append(lines, text)
	}
	for _, row := range []struct {
		kind  hotkeyKind
		chord userconfig.Chord
	}{{hotkeyToggleAuto, toggle}, {hotkeyConvertSelection, selection}} {
		if errText, _ := chordConflictText(row.chord, accels); errText != "" {
			line := row.kind.label() + " " + errText
			if !slices.Contains(lines, line) {
				lines = append(lines, line)
			}
		}
	}
	return strings.Join(lines, "\n")
}

// GNOME layout switch accelerators (C14). Tests replace the reader.
const (
	switchAccelSchema      = "org.gnome.desktop.wm.keybindings"
	switchAccelForward     = "switch-input-source"
	switchAccelBackward    = "switch-input-source-backward"
	switchAccelReadTimeout = 2 * time.Second
)

var readSwitchAccels = func() []string {
	ctx, cancel := context.WithTimeout(context.Background(), switchAccelReadTimeout)
	defer cancel()
	var accels []string
	for _, key := range []string{switchAccelForward, switchAccelBackward} {
		out, err := focusExtRunGsettings(ctx, "get", switchAccelSchema, key)
		if err != nil {
			continue
		}
		items, err := focusExtParseStrv(out)
		if err != nil {
			continue
		}
		accels = append(accels, items...)
	}
	return accels
}

// hotkeyRow is one chord row: preset combo, keycap and warning label.
type hotkeyRow struct {
	kind      hotkeyKind
	combo     *gtk.ComboBoxText
	keycap    *gtk.Label
	warning   *gtk.Label
	chord     userconfig.Chord // current selection (nil = disabled)
	custom    userconfig.Chord // non-default chord shown by the fourth entry
	prevIndex int
	updating  bool
}

// createHotkeysFrame builds the "Hotkeys" frame.
func (w *SettingsWindow) createHotkeysFrame() (*gtk.Frame, error) {
	frame, grid, err := newAutoFrame(tr(strGASectionHotkeys))
	if err != nil {
		return nil, err
	}
	a := w.auto
	row := 0
	for i, kind := range []hotkeyKind{hotkeyToggleAuto, hotkeyConvertSelection} {
		hr := &hotkeyRow{kind: kind, chord: slices.Clone(kind.defaultChord())}
		a.hotkeys[i] = hr

		label, err := gtk.LabelNew(kind.label())
		if err != nil {
			return nil, err
		}
		label.SetHAlign(gtk.ALIGN_START)
		grid.Attach(label, 0, row, 1, 1)

		if hr.combo, err = gtk.ComboBoxTextNew(); err != nil {
			return nil, err
		}
		hr.combo.SetHExpand(true)
		hr.combo.Connect("changed", func() { w.onHotkeyComboChanged(hr) })
		grid.Attach(hr.combo, 1, row, 1, 1)
		row++

		// The keycap sits under the combo to keep the page narrow.
		if hr.keycap, err = gtk.LabelNew(""); err != nil {
			return nil, err
		}
		if err := w.styleKeycap(hr.keycap); err != nil {
			return nil, err
		}
		hr.keycap.SetHAlign(gtk.ALIGN_START)
		grid.Attach(hr.keycap, 1, row, 1, 1)
		row++

		if hr.warning, err = gtk.LabelNew(""); err != nil {
			return nil, err
		}
		hr.warning.SetLineWrap(true)
		hr.warning.SetMaxWidthChars(60)
		hr.warning.SetHAlign(gtk.ALIGN_START)
		hr.warning.SetNoShowAll(true)
		if style, _ := hr.warning.GetStyleContext(); style != nil {
			style.AddClass("warning")
		}
		grid.Attach(hr.warning, 0, row, 2, 1)
		row++

		w.setHotkeyChord(hr, hr.chord)
	}

	hint, err := newHintLabel(tr(strGAHintChords))
	if err != nil {
		return nil, err
	}
	grid.Attach(hint, 0, row, 2, 1)
	return frame, nil
}

// setHotkeyChord shows chord in the row without opening the key picker.
func (w *SettingsWindow) setHotkeyChord(hr *hotkeyRow, chord userconfig.Chord) {
	hr.chord = slices.Clone(chord)
	index := hotkeyPresetIndex(hr.kind, chord)
	if index == hotkeyEntryCustomValue {
		hr.custom = slices.Clone(chord)
	}
	hr.updating = true
	hr.combo.RemoveAll()
	for _, label := range hotkeyComboLabels(hr.kind, hr.custom) {
		hr.combo.AppendText(label)
	}
	// A previous custom chord keeps its entry for quick re-selection.
	hr.combo.SetActive(index)
	hr.updating = false
	hr.prevIndex = index
	w.refreshHotkeyRow(hr)
}

// refreshHotkeyRow updates the keycap and the order warning of a row.
func (w *SettingsWindow) refreshHotkeyRow(hr *hotkeyRow) {
	if hr.keycap != nil {
		hr.keycap.SetText(chordDisplay(hr.chord))
	}
	if hr.warning == nil {
		return
	}
	errText, warnText := chordConflictText(hr.chord, w.auto.accels)
	message := warnText
	if errText != "" {
		message = errText
	}
	hr.warning.SetText(message)
	hr.warning.SetVisible(message != "")
}

func (w *SettingsWindow) refreshHotkeyRows() {
	for _, hr := range w.auto.hotkeys {
		if hr != nil {
			w.refreshHotkeyRow(hr)
		}
	}
}

// onHotkeyComboChanged applies a preset or opens the key picker for
// "Custom…" (reverting on cancel).
func (w *SettingsWindow) onHotkeyComboChanged(hr *hotkeyRow) {
	if hr.updating {
		return
	}
	index := hr.combo.GetActive()
	if chord, ok := hotkeyChordAt(hr.kind, index, hr.custom); ok {
		hr.chord = chord
		hr.prevIndex = index
		w.refreshHotkeyRow(hr)
		return
	}
	if index != hotkeyEntryCustom {
		return
	}
	result, ok := ShowKeyPickerDialog(w.window, hr.kind.pickerContext(), w.getLayoutSwitchValueForWarning())
	if !ok || len(result.KeyCodes) == 0 {
		hr.updating = true
		hr.combo.SetActive(hr.prevIndex)
		hr.updating = false
		return
	}
	w.setHotkeyChord(hr, chordFromCodes(result.KeyCodes))
}

// hotkeyChords returns the chords selected in the GUI.
func (w *SettingsWindow) hotkeyChords() (toggle, selection userconfig.Chord) {
	if hr := w.auto.hotkeys[hotkeyToggleAuto]; hr != nil {
		toggle = slices.Clone(hr.chord)
	}
	if hr := w.auto.hotkeys[hotkeyConvertSelection]; hr != nil {
		selection = slices.Clone(hr.chord)
	}
	return toggle, selection
}

// loadSwitchAccelsAsync reads the GNOME accelerators off the GTK thread and
// refreshes the order warnings.
func (w *SettingsWindow) loadSwitchAccelsAsync() {
	go func() {
		accels := readSwitchAccels()
		glib.IdleAdd(func() {
			if w.window == nil {
				return
			}
			w.auto.accels = accels
			w.refreshHotkeyRows()
		})
	}()
}
