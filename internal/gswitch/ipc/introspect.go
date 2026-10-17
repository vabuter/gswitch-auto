package ipc

import (
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
)

// Property names of the daemon interface.
const (
	propAutoEnabled           = "AutoEnabled"
	propAutoEnabledUnsaved    = "AutoEnabledUnsaved"
	propPendingLearnedWords   = "PendingLearnedWords"
	propAutoAvailable         = "AutoAvailable"
	propAutoUnavailableReason = "AutoUnavailableReason"
	propActiveLayout          = "ActiveLayout"
	propLayoutKnown           = "LayoutKnown"
	propLayoutSource          = "LayoutSource"
	propFocusSource           = "FocusSource"
	propFocusedApp            = "FocusedApp"
	propFocusedWmClass        = "FocusedWmClass"
	propInputPurpose          = "InputPurpose"
	propFocusSuppressed       = "FocusSuppressed"
	propFocusSuppressedReason = "FocusSuppressedReason"
	propLastCorrection        = "LastCorrection"
	propCorrectionCount       = "CorrectionCount"
	propStats                 = "Stats"
	propToggleChord           = "ToggleChord"
	propSelectionChord        = "SelectionChord"
	propHotkeysSuspended      = "HotkeysSuspended"
	propConfigDir             = "ConfigDir"
	propConfigWarnings        = "ConfigWarnings"
	propVersion               = "Version"
)

// Signal and method names of the daemon interface.
const (
	sigAutoEnabledChanged = "AutoEnabledChanged"
	sigCorrected          = "Corrected"
	sigCorrectionUndone   = "CorrectionUndone"

	methodSetAutoEnabled = "SetAutoEnabled"
	methodReload         = "Reload"
	methodSuspendHotkeys = "SuspendHotkeys"
)

const (
	accessRead      = "read"
	accessReadWrite = "readwrite"
)

func readProp(name, typ string) introspect.Property {
	return introspect.Property{Name: name, Type: typ, Access: accessRead}
}

func inArg(name, typ string) introspect.Arg {
	return introspect.Arg{Name: name, Type: typ, Direction: "in"}
}

func sigArg(name, typ string) introspect.Arg {
	return introspect.Arg{Name: name, Type: typ}
}

// interfaceData is the introspection data of io.github.vabuter.GswitchAuto.
// The property order is the order in which property updates are applied.
var interfaceData = introspect.Interface{
	Name: Interface,
	Methods: []introspect.Method{
		{Name: methodSetAutoEnabled, Args: []introspect.Arg{inArg("enabled", "b")}},
		{Name: methodReload},
		{Name: methodSuspendHotkeys, Args: []introspect.Arg{inArg("suspend", "b")}},
	},
	Signals: []introspect.Signal{
		{Name: sigAutoEnabledChanged, Args: []introspect.Arg{
			sigArg("enabled", "b"), sigArg("source", "s"),
		}},
		{Name: sigCorrected, Args: []introspect.Arg{
			sigArg("from", "s"), sigArg("to", "s"), sigArg("direction", "s"), sigArg("time", "t"),
		}},
		{Name: sigCorrectionUndone, Args: []introspect.Arg{
			sigArg("word", "s"), sigArg("learned", "b"),
		}},
	},
	Properties: []introspect.Property{
		{Name: propAutoEnabled, Type: "b", Access: accessReadWrite},
		readProp(propAutoEnabledUnsaved, "b"),
		readProp(propPendingLearnedWords, "as"),
		readProp(propAutoAvailable, "b"),
		readProp(propAutoUnavailableReason, "s"),
		readProp(propActiveLayout, "s"),
		readProp(propLayoutKnown, "b"),
		readProp(propLayoutSource, "s"),
		readProp(propFocusSource, "s"),
		readProp(propFocusedApp, "s"),
		readProp(propFocusedWmClass, "s"),
		readProp(propInputPurpose, "s"),
		readProp(propFocusSuppressed, "b"),
		readProp(propFocusSuppressedReason, "s"),
		readProp(propLastCorrection, "(ssst)"),
		readProp(propCorrectionCount, "u"),
		readProp(propStats, "a{st}"),
		readProp(propToggleChord, "s"),
		readProp(propSelectionChord, "s"),
		readProp(propHotkeysSuspended, "b"),
		readProp(propConfigDir, "s"),
		readProp(propConfigWarnings, "as"),
		readProp(propVersion, "s"),
	},
}

// propertyNames lists the interface properties in introspection order.
var propertyNames = func() []string {
	names := make([]string, 0, len(interfaceData.Properties))
	for _, p := range interfaceData.Properties {
		names = append(names, p.Name)
	}
	return names
}()

// serviceNode is the introspection node exported at ObjectPath.
func serviceNode() *introspect.Node {
	return &introspect.Node{
		Name: string(ObjectPath),
		Interfaces: []introspect.Interface{
			introspect.IntrospectData,
			prop.IntrospectData,
			interfaceData,
		},
	}
}
