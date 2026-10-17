// Package ipc implements the session-bus interface between the gswitch daemon
// and the tray (io.github.vabuter.GswitchAuto).
//
// The package is pure Go: it depends on godbus only, never on GTK or on the
// daemon's runtime package. Both sides open their own private session-bus
// connection and close only that connection; the shared process-wide session
// connection is owned by the tray's systray library and must not be touched.
package ipc

import (
	"errors"
	"time"

	"github.com/godbus/dbus/v5"
)

// D-Bus names of the daemon interface.
const (
	BusName                    = "io.github.vabuter.GswitchAuto"
	ObjectPath dbus.ObjectPath = "/io/github/vabuter/GswitchAuto"
	Interface                  = BusName

	// ErrConfig is the D-Bus error name returned by Reload when the user
	// configuration cannot be applied.
	ErrConfig = BusName + ".Error.Config"
	// ErrNotAvailable is the D-Bus error name returned when the daemon refuses
	// a SetAutoEnabled or SuspendHotkeys request.
	ErrNotAvailable = BusName + ".Error.NotAvailable"
)

// Sources reported by the AutoEnabledChanged signal.
const (
	SourceChord   = "chord"
	SourceDBus    = "dbus"
	SourceConfig  = "config"
	SourceStartup = "startup"
)

var (
	// ErrNameTaken is returned by Start when another process already owns
	// BusName (for example `gswitch --debug` running beside the service).
	ErrNameTaken = errors.New("IPC disabled: bus name owned by another instance")
	// ErrRootMode is returned by Start when the daemon runs as root.
	ErrRootMode = errors.New("IPC disabled: root mode")
	// ErrNotConnected is returned by Client calls while no daemon owns BusName.
	ErrNotConnected = errors.New("gswitch daemon is not connected")
)

// Correction describes one automatic correction.
type Correction struct {
	From, To, Direction string
	Time                time.Time
}

// Status is the daemon state published as D-Bus properties.
type Status struct {
	AutoEnabled, AutoAvailable, LayoutKnown, FocusSuppressed, HotkeysSuspended bool
	AutoEnabledUnsaved                                                         bool
	PendingLearnedWords                                                        []string
	UnavailableReason, ActiveLayout, LayoutSource, FocusSource                 string
	FocusedApp, FocusedWmClass, InputPurpose, FocusSuppressedReason            string
	ToggleChord, SelectionChord, ConfigDir, Version                            string
	ConfigWarnings                                                             []string
	Last                                                                       Correction
	Count                                                                      uint32
	Stats                                                                      map[string]uint64
}

// Controller is implemented by the daemon.
//
// Controller implementations may call any Service method synchronously from
// any goroutine, including from inside SetAutoEnabled: no Service method blocks
// or takes the property lock in the caller's goroutine.
type Controller interface {
	AutoEnabled() bool
	SetAutoEnabled(enabled bool, source string) error
	Reload() error
	SuspendHotkeys(suspend bool, owner string) error
	Snapshot() Status
}
