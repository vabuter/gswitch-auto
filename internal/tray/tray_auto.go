package tray

import (
	"fmt"

	"github.com/arumata/gswitch/internal/gswitch/ipc"
	"github.com/arumata/gswitch/internal/gswitch/userconfig"
)

// addAutoMenuItem creates the automatic correction item. Backends with
// checkable items (StatusNotifier) get a checkbox; the others (XEmbed) a
// plain item whose title carries the state.
func (t *Tray) addAutoMenuItem() {
	title := tr(strGAMenuUnavailable)
	hint := t.autoMenuTooltip()
	var item trayMenuItem
	if backend, ok := t.backend.(checkboxTrayBackend); ok {
		item = backend.AddMenuItemCheckbox(title, hint, false)
		if checkable, ok := item.(checkableTrayMenuItem); ok {
			t.autoCheckable = checkable
		}
	}
	if item == nil {
		item = t.backend.AddMenuItem(title, hint)
	}
	t.mAutoToggle = item
	t.renderAutoMenuItem()
}

// autoClicks returns the click channel of the automatic correction item; a
// nil channel (never ready) when the item does not exist.
func (t *Tray) autoClicks() <-chan struct{} {
	if t.mAutoToggle == nil {
		return nil
	}
	return t.mAutoToggle.Clicks()
}

// handleAutoToggleClick runs on the tray event loop; the toggle itself may
// block on D-Bus or file I/O, so it runs on its own goroutine.
func (t *Tray) handleAutoToggleClick() {
	if t.app != nil {
		go t.app.ToggleAuto()
	}
}

// UpdateAutoState queues an automatic correction state update. While no
// daemon is connected the status carries Connected=false and the auto-enabled
// value of auto.conf.
func (t *Tray) UpdateAutoState(st ipc.ClientStatus) {
	t.enqueueUIReliable(func() {
		t.applyAutoState(st)
	})
}

// UpdateAutoPersistError queues the tooltip text of a failed persistence
// write ("" clears it).
func (t *Tray) UpdateAutoPersistError(message string) {
	t.enqueueUIReliable(func() {
		t.autoPersistError = message
		t.renderAutoMenuItem()
	})
}

// applyAutoState records st and redraws the menu item, the badge and the
// tooltip suffix. It runs on the tray event loop.
func (t *Tray) applyAutoState(st ipc.ClientStatus) {
	t.autoStatus = st
	t.renderAutoMenuItem()
	if t.detectionInfo.Status != TrayStatusOK {
		return // warning and error icons keep priority
	}
	if t.latestLayout != nil {
		t.currentCode = ""
		t.applyLayout(*t.latestLayout)
		return
	}
	t.applyDetectionStatus(t.detectionInfo)
}

// autoAvailable reports whether the daemon is connected and can correct.
func (t *Tray) autoAvailable() bool {
	return t.autoStatus.Connected && t.autoStatus.AutoAvailable
}

// autoBadgeOn reports whether the icon shows the automatic correction badge.
func (t *Tray) autoBadgeOn() bool {
	return t.autoAvailable() && t.autoStatus.AutoEnabled
}

// autoTooltipSuffix is appended to the layout tooltip while a daemon is
// connected.
func (t *Tray) autoTooltipSuffix() string {
	if !t.autoStatus.Connected {
		return ""
	}
	if t.autoBadgeOn() {
		return "\n" + tr(strGATooltipOn)
	}
	return "\n" + tr(strGATooltipOff)
}

// autoMenuTitle returns the item title: with a checkbox the check mark
// carries the state, otherwise the title does.
func (t *Tray) autoMenuTitle() string {
	if !t.autoAvailable() {
		return tr(strGAMenuUnavailable)
	}
	if t.autoCheckable != nil {
		return tr(strGAMenuAuto)
	}
	if t.autoStatus.AutoEnabled {
		return tr(strGAMenuOn)
	}
	return tr(strGAMenuOff)
}

// autoMenuTooltip names the toggle chord, or shows a persistence error.
func (t *Tray) autoMenuTooltip() string {
	if t.autoPersistError != "" {
		return t.autoPersistError
	}
	return fmt.Sprintf(tr(strGAMenuHint), toggleChordHint(t.autoStatus.ToggleChord))
}

// toggleChordHint formats a chord value as published by the daemon or read
// from auto.conf ("42+54", "none").
func toggleChordHint(value string) string {
	chord, err := userconfig.ParseChord(value)
	if err != nil || len(chord) == 0 {
		return tr(strGAMenuNoHotkey)
	}
	return formatChordLabel(chord)
}

func (t *Tray) renderAutoMenuItem() {
	if t.mAutoToggle == nil {
		return
	}
	if t.autoCheckable != nil {
		t.autoCheckable.SetChecked(t.autoStatus.AutoEnabled)
	}
	t.mAutoToggle.SetTitle(t.autoMenuTitle())
	t.mAutoToggle.SetTooltip(t.autoMenuTooltip())
}
