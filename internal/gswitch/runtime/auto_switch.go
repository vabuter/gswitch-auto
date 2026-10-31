package runtime

import (
	"slices"
	"time"

	"github.com/arumata/gswitch/internal/gswitch/detect"
)

// gswitch-auto: how the confirmed (automatic) path triggers the layout switch
// (SPEC-X11 §2.1, rev. 4).
//
// GNOME Shell switches input sources through an InputSourcePopup
// (keyboard.js _switchInputSource). For a binding with a modifier the popup
// finishes as soon as the modifier is released (switcherPopup.js show() and
// vfunc_key_release_event), so a replayed chord switches within
// milliseconds. For a binding without one (upstream's GNOME X11 accelerator
// XF86Launch7, a single-key Caps_Lock or XF86Keyboard binding) it grabs the
// keyboard and applies the switch only NO_MODS_TIMEOUT (1.5 s) after the last
// key release; every key typed meanwhile is swallowed and restarts the timer,
// Space or Return applies it early, Escape or Tab cancels it, and a second
// press of the binding inside the popup moves the selection back to the
// current source. The Wayland confirm timings (2 attempts of 20 polls of
// 50 ms) therefore fail every automatic fix there and re-press the key into
// the open popup.

// gnomeNoModsPopupTimeout is switcherPopup.js NO_MODS_TIMEOUT.
const gnomeNoModsPopupTimeout = 1500 * time.Millisecond

// autoNoModsConfirmWindow is the single confirm window of a modifier-less
// GNOME binding: NO_MODS_TIMEOUT plus a 1 s margin, never re-pressed.
const autoNoModsConfirmWindow = gnomeNoModsPopupTimeout + time.Second

// forkEnsureShiftLaunch7 adds the fork-owned <Shift>XF86Launch7 accelerator;
// the package tests replace it so that no test writes the live gsettings.
var forkEnsureShiftLaunch7 = detect.EnsureGNOMEX11ShiftLaunch7Binding

// switchPlan is how emitConversionEvents triggers and confirms the switch on
// the confirmed path. The zero value means the upstream GNOME Wayland
// confirmation (LSKeys, gnomeWaylandLayoutSwitchAttempts x
// gnomeWaylandLayoutGroupPollAttempts).
type switchPlan struct {
	keys     []uint16 // pressed in order, released in reverse
	attempts int
	polls    int // per attempt, gnomeWaylandLayoutGroupPollDelay apart
	// noMods: the binding opens GNOME's modifier-less popup, which swallows
	// the keys typed until it closes; they are discarded from the daemon's
	// input as well (discardSwallowedInput).
	noMods bool
}

// window is the longest time one attempt polls for the switch.
func (p switchPlan) window() time.Duration {
	return time.Duration(p.polls) * gnomeWaylandLayoutGroupPollDelay
}

// switchKeyIsModifier reports a key that GNOME counts as a modifier of an
// accelerator (Shift, Control, Alt, Super).
func switchKeyIsModifier(code uint16) bool {
	switch code {
	case KEY_LEFTSHIFT, KEY_RIGHTSHIFT, KEY_LEFTCTRL, KEY_RIGHTCTRL,
		KEY_LEFTALT, KEY_RIGHTALT, KEY_LEFTMETA, KEY_RIGHTMETA:
		return true
	}
	return false
}

// switchKeysHaveModifier reports a chord of at least two keys that holds a
// modifier while the last key is pressed. A single key never has one, even a
// modifier key bound on its own (GNOME then has mask 0).
func switchKeysHaveModifier(keys []uint16) bool {
	return len(keys) >= 2 && slices.ContainsFunc(keys[:len(keys)-1], switchKeyIsModifier)
}

// autoSwitchPlan returns the plan of the automatic path (autoEmit, the
// selection-chord restore):
//   - GNOME X11 with upstream's XF86Launch7 (LSKeys [KEY_F16]) and the
//     fork's <Shift>XF86Launch7 binding in place: Shift+F16, so the popup
//     finishes on the Shift release; the usual 2 x 20 x 50 ms confirmation;
//   - any other modifier-less GNOME binding (or the Shift binding could not
//     be added): one press, one window longer than NO_MODS_TIMEOUT, and the
//     input that arrived meanwhile is discarded;
//   - otherwise LSKeys with the usual confirmation (Fedora's <Alt>Shift_L).
func (s *Switcher) autoSwitchPlan() switchPlan {
	var keys []uint16
	if s.converter != nil {
		keys = s.converter.LSKeys
	}
	if s.autoShiftLaunch7 && slices.Equal(keys, []uint16{KEY_F16}) {
		return switchPlan{
			keys:     []uint16{KEY_LEFTSHIFT, KEY_F16},
			attempts: gnomeWaylandLayoutSwitchAttempts,
			polls:    gnomeWaylandLayoutGroupPollAttempts,
		}
	}
	if len(keys) > 0 && (s.verifyGNOMEX11LayoutSwitch || (s.platform.GNOME && !switchKeysHaveModifier(keys))) {
		return switchPlan{
			keys:     keys,
			attempts: 1,
			polls:    int(autoNoModsConfirmWindow / gnomeWaylandLayoutGroupPollDelay),
			noMods:   true,
		}
	}
	return switchPlan{
		keys:     keys,
		attempts: gnomeWaylandLayoutSwitchAttempts,
		polls:    gnomeWaylandLayoutGroupPollAttempts,
	}
}

// initAutoSwitchBinding adds the fork-owned <Shift>XF86Launch7 accelerator
// on GNOME X11 when the automatic path can run (a layout tracker) and the
// layout switch is upstream's XF86Launch7. The setting is persistent like
// upstream's XF86Launch7. On failure the automatic path keeps the slow
// modifier-less plan.
func (s *Switcher) initAutoSwitchBinding() {
	if s.tracker == nil || !s.verifyGNOMEX11LayoutSwitch || s.converter == nil ||
		!slices.Equal(s.converter.LSKeys, []uint16{KEY_F16}) {
		return
	}
	changed, err := forkEnsureShiftLaunch7(s.sessionEnv)
	if err != nil {
		s.log("Warning: cannot add %s to the GNOME layout-switch bindings (%v); automatic corrections wait %v for GNOME's popup and discard the keys typed meanwhile",
			detect.GNOMEX11ShiftLaunch7Accelerator, err, gnomeNoModsPopupTimeout)
		return
	}
	s.autoShiftLaunch7 = true
	if changed {
		s.log("Added %s to GNOME layout-switch bindings for automatic correction", detect.GNOMEX11ShiftLaunch7Accelerator)
	}
}

// discardSwallowedInput drops the input that arrived while a modifier-less
// GNOME popup held the keyboard grab: GNOME swallowed those keys, so they
// must not reach the converter buffer or the engine (the buffer would hold
// text that is not on the screen). The engine's held set is resynchronised
// from the kernel and the word in progress is tainted (afterInputDiscarded).
func (s *Switcher) discardSwallowedInput() {
	if s.eventChan != nil {
		s.drainEventChan()
	}
	s.afterInputDiscarded()
}
