// Package focus reports the focused application and the purpose of the focused
// text field on GNOME Shell. The data comes from the gswitch-focus Shell
// extension (shell-extension/gswitch-focus@vabuter.github.io), which exports a
// small read-only interface on the session bus; GNOME Shell's own
// org.gnome.Shell.Introspect is allowlisted and not available to the daemon.
//
// The package is shared by the daemon (Client, Policy) and the tray
// (ListRunningApps). It never exposes window titles, text or process ids.
package focus

import (
	"errors"
	"time"

	"github.com/godbus/dbus/v5"
)

// D-Bus names of the extension and of the screen saver lock hint. The
// extension UUID equals the extension directory name under shell-extension/
// and the install path under /usr/share/gnome-shell/extensions
// (scripts/check-extension.sh greps this file).
const (
	ExtensionUUID      = "gswitch-focus@vabuter.github.io"
	BusName            = "org.gnome.Shell.Extensions.GswitchFocus"
	ObjectPath         = dbus.ObjectPath("/org/gnome/Shell/Extensions/GswitchFocus")
	Interface          = "org.gnome.Shell.Extensions.GswitchFocus"
	ScreenSaverName    = "org.gnome.ScreenSaver"
	ScreenSaverPath    = dbus.ObjectPath("/org/gnome/ScreenSaver")
	DefaultCallTimeout = 500 * time.Millisecond
)

// Purpose is the input purpose of the focused text field as reported by the
// extension: normal, alpha, digits, number, phone, url, email, name, password,
// pin, terminal, date, time, datetime, none (no text field focused) or unknown.
type Purpose string

// Purposes the daemon acts on. Every constant is typed explicitly (in a const
// block only the first would otherwise be Purpose).
const (
	PurposeUnknown  Purpose = "unknown"
	PurposeNone     Purpose = "none"
	PurposeNormal   Purpose = "normal"
	PurposePassword Purpose = "password"
	PurposePIN      Purpose = "pin"
	PurposeTerminal Purpose = "terminal"
)

// Lock reasons reported in State.LockReason.
const (
	LockReasonExtensionLocked   = "extension-locked"
	LockReasonExtensionVanished = "extension-vanished"
	LockReasonScreenSaver       = "screensaver"
)

// State is a snapshot of the focus information known to the daemon.
type State struct {
	Available     bool // extension name currently owned
	EverAvailable bool // seen at least once since daemon start
	// AppID and WmClass are raw, as reported by the extension (IPC/debug
	// display only). AppID is a Shell.App id such as
	// "org.gnome.Ptyxis.desktop", "window:N" for windows without a .desktop
	// file (not stable across sessions), or "".
	AppID, WmClass string
	// AppKey and WmClassKey are fold.AppKey(AppID) and fold.AppKey(WmClass);
	// the client sets them on every change.
	AppKey, WmClassKey string
	Purpose            Purpose
	// FocusUnknown: the extension is available but GetFocus failed, so the
	// focused application and field purpose are not known. Policy.Allow
	// denies until a GetFocus retry or a FocusChanged signal succeeds.
	FocusUnknown bool
	Locked       bool
	LockReason   string // "extension-locked" | "extension-vanished" | "screensaver" | ""
	Seq          uint64 // bumps on every change
	UpdatedAt    time.Time
}

// App is one running application as listed by the extension: the Shell.App
// id, its display name and the WM_CLASS of its first window.
type App struct{ ID, Name, WmClass string }

// Source provides focus snapshots. *Client implements it.
type Source interface{ Snapshot() State }

// ErrUnavailable is returned when the extension's bus name is not owned.
var ErrUnavailable = errors.New("focus extension unavailable")

// Static returns a Source that always reports st (tests, disabled focus).
//
//nolint:ireturn // Static exists to provide the Source contract.
func Static(st State) Source {
	return staticSource{st: st}
}

type staticSource struct{ st State }

func (s staticSource) Snapshot() State { return s.st }

// knownPurpose maps a purpose string received from the extension to a
// Purpose; anything outside the documented vocabulary becomes PurposeUnknown.
func knownPurpose(s string) Purpose {
	switch s {
	case "normal", "alpha", "digits", "number", "phone", "url", "email", "name",
		"password", "pin", "terminal", "date", "time", "datetime", "none", "unknown":
		return Purpose(s)
	default:
		return PurposeUnknown
	}
}
