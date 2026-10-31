package detect

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// gswitch-auto (fork file).

// GNOMEX11ShiftLaunch7Accelerator is the fork-owned layout-switch accelerator
// of the automatic path on GNOME X11 (SPEC-X11 §2.1, rev. 4). Upstream's
// XF86Launch7 has no modifier, so GNOME Shell opens a no-modifier
// InputSourcePopup for it, which grabs the keyboard and applies the switch
// only 1.5 s (switcherPopup.js NO_MODS_TIMEOUT) after the last key release;
// every key typed meanwhile is swallowed and restarts the timer. A binding
// with a modifier finishes the popup as soon as the modifier is released,
// within milliseconds, like Super+Space.
const GNOMEX11ShiftLaunch7Accelerator = "<Shift>XF86Launch7"

var runGsettingsForShiftLaunch7 = RunGsettings

// EnsureGNOMEX11ShiftLaunch7Binding appends <Shift>XF86Launch7 to GNOME's
// switch-input-source accelerators in an X11 GNOME session, like
// EnsureGNOMEX11Launch7Binding does for XF86Launch7: the existing accelerators
// keep their order, the setting is persistent, and a write that cannot be
// verified is rolled back. It reports whether it changed the setting; outside
// an X11 GNOME session it does nothing and returns an error, because the
// accelerator would not be usable.
func EnsureGNOMEX11ShiftLaunch7Binding(env *SessionEnv) (bool, error) {
	if !isGNOMEX11Session(env) {
		return false, errors.New("not an X11 GNOME session")
	}
	accel := GNOMEX11ShiftLaunch7Accelerator
	originalOutput, err := runGsettingsForShiftLaunch7(
		env, "get", gnomeWMKeybindingsSchema, gnomeSwitchInputSourceKey,
	)
	if err != nil {
		return false, fmt.Errorf("read GNOME layout-switch bindings: %w", err)
	}
	original := parseGsettingsArray(strings.TrimSpace(string(originalOutput)))
	if slices.Contains(original, accel) {
		return false, nil
	}
	updated := append(slices.Clone(original), accel)
	if _, err := runGsettingsForShiftLaunch7(
		env, "set", gnomeWMKeybindingsSchema, gnomeSwitchInputSourceKey,
		formatGsettingsStringArray(updated),
	); err != nil {
		return false, fmt.Errorf("add GNOME %s layout-switch binding: %w", accel, err)
	}
	verified, verifyErr := runGsettingsForShiftLaunch7(
		env, "get", gnomeWMKeybindingsSchema, gnomeSwitchInputSourceKey,
	)
	if verifyErr == nil && slices.Contains(parseGsettingsArray(strings.TrimSpace(string(verified))), accel) {
		return true, nil
	}
	if verifyErr == nil {
		verifyErr = fmt.Errorf("gsettings value does not contain %s", accel)
	}
	if _, rollbackErr := runGsettingsForShiftLaunch7(
		env, "set", gnomeWMKeybindingsSchema, gnomeSwitchInputSourceKey,
		formatGsettingsStringArray(original),
	); rollbackErr != nil {
		return false, errors.Join(
			fmt.Errorf("verify %s binding: %w", accel, verifyErr),
			fmt.Errorf("restore original GNOME bindings: %w", rollbackErr),
		)
	}
	return false, fmt.Errorf("verify %s binding: %w", accel, verifyErr)
}
