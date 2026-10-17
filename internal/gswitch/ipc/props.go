package ipc

import (
	"maps"
	"math"
	"slices"
	"time"

	"github.com/godbus/dbus/v5"
)

// statsKeys are the counters always present in the Stats property, so that the
// exported key set never shrinks (godbus merges a stored map instead of
// replacing it) and clients can rely on every key being present.
var statsKeys = []string{"armed", "fired", "cancelled", "skipped", "undone", "learned", "garble_risk"}

// correctionTuple is the D-Bus representation (ssst) of a Correction.
type correctionTuple struct {
	From      string
	To        string
	Direction string
	Time      uint64
}

func (c correctionTuple) correction() Correction {
	return Correction{From: c.From, To: c.To, Direction: c.Direction, Time: timeFromUnix(c.Time)}
}

func tupleOf(c Correction) correctionTuple {
	return correctionTuple{From: c.From, To: c.To, Direction: c.Direction, Time: unixSeconds(c.Time)}
}

func unixSeconds(t time.Time) uint64 {
	if t.IsZero() || t.Unix() <= 0 {
		return 0
	}
	return uint64(t.Unix())
}

func timeFromUnix(sec uint64) time.Time {
	if sec == 0 || sec > math.MaxInt64 {
		return time.Time{}
	}
	return time.Unix(int64(sec), 0)
}

// cloneStrings returns a copy of s that is never nil.
func cloneStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return slices.Clone(s)
}

// normalizeStats returns a fresh map holding every statsKeys entry plus m.
func normalizeStats(m map[string]uint64) map[string]uint64 {
	out := make(map[string]uint64, len(statsKeys)+len(m))
	for _, k := range statsKeys {
		out[k] = 0
	}
	maps.Copy(out, m)
	return out
}

// cloneStatus deep-copies the slices and maps of st.
func cloneStatus(st Status) Status {
	st.PendingLearnedWords = cloneStrings(st.PendingLearnedWords)
	st.ConfigWarnings = cloneStrings(st.ConfigWarnings)
	if st.Stats != nil {
		st.Stats = maps.Clone(st.Stats)
	}
	return st
}

// statusValues maps st to the exported property values. Every slice and map in
// the result is freshly allocated and non-nil.
func statusValues(st Status) map[string]any {
	return map[string]any{
		propAutoEnabled:           st.AutoEnabled,
		propAutoEnabledUnsaved:    st.AutoEnabledUnsaved,
		propPendingLearnedWords:   cloneStrings(st.PendingLearnedWords),
		propAutoAvailable:         st.AutoAvailable,
		propAutoUnavailableReason: st.UnavailableReason,
		propActiveLayout:          st.ActiveLayout,
		propLayoutKnown:           st.LayoutKnown,
		propLayoutSource:          st.LayoutSource,
		propFocusSource:           st.FocusSource,
		propFocusedApp:            st.FocusedApp,
		propFocusedWmClass:        st.FocusedWmClass,
		propInputPurpose:          st.InputPurpose,
		propFocusSuppressed:       st.FocusSuppressed,
		propFocusSuppressedReason: st.FocusSuppressedReason,
		propLastCorrection:        tupleOf(st.Last),
		propCorrectionCount:       st.Count,
		propStats:                 normalizeStats(st.Stats),
		propToggleChord:           st.ToggleChord,
		propSelectionChord:        st.SelectionChord,
		propHotkeysSuspended:      st.HotkeysSuspended,
		propConfigDir:             st.ConfigDir,
		propConfigWarnings:        cloneStrings(st.ConfigWarnings),
		propVersion:               st.Version,
	}
}

// cloneValue copies the mutable property representations (slices and maps) so
// that a D-Bus reply never aliases storage that a later SetMust mutates in place.
func cloneValue(v any) any {
	switch x := v.(type) {
	case []string:
		return cloneStrings(x)
	case map[string]uint64:
		return maps.Clone(x)
	default:
		return v
	}
}

// applyProperty stores one property value received over D-Bus into st.
// Unknown property names are ignored (a newer daemon may export more).
func applyProperty(st *Status, name string, v dbus.Variant) error {
	switch name {
	case propAutoEnabled:
		return v.Store(&st.AutoEnabled)
	case propAutoEnabledUnsaved:
		return v.Store(&st.AutoEnabledUnsaved)
	case propPendingLearnedWords:
		return storeStrings(v, &st.PendingLearnedWords)
	case propAutoAvailable:
		return v.Store(&st.AutoAvailable)
	case propAutoUnavailableReason:
		return v.Store(&st.UnavailableReason)
	case propActiveLayout:
		return v.Store(&st.ActiveLayout)
	case propLayoutKnown:
		return v.Store(&st.LayoutKnown)
	case propLayoutSource:
		return v.Store(&st.LayoutSource)
	case propFocusSource:
		return v.Store(&st.FocusSource)
	case propFocusedApp:
		return v.Store(&st.FocusedApp)
	case propFocusedWmClass:
		return v.Store(&st.FocusedWmClass)
	case propInputPurpose:
		return v.Store(&st.InputPurpose)
	case propFocusSuppressed:
		return v.Store(&st.FocusSuppressed)
	case propFocusSuppressedReason:
		return v.Store(&st.FocusSuppressedReason)
	case propLastCorrection:
		var tup correctionTuple
		if err := v.Store(&tup); err != nil {
			return err
		}
		st.Last = tup.correction()
		return nil
	case propCorrectionCount:
		return v.Store(&st.Count)
	case propStats:
		m := map[string]uint64{}
		if err := v.Store(&m); err != nil {
			return err
		}
		st.Stats = m
		return nil
	case propToggleChord:
		return v.Store(&st.ToggleChord)
	case propSelectionChord:
		return v.Store(&st.SelectionChord)
	case propHotkeysSuspended:
		return v.Store(&st.HotkeysSuspended)
	case propConfigDir:
		return v.Store(&st.ConfigDir)
	case propConfigWarnings:
		return storeStrings(v, &st.ConfigWarnings)
	case propVersion:
		return v.Store(&st.Version)
	default:
		return nil
	}
}

// storeStrings decodes into a fresh slice: dbus.Store writes into an existing
// slice in place, which would mutate a value already handed to a caller.
func storeStrings(v dbus.Variant, dst *[]string) error {
	var s []string
	if err := v.Store(&s); err != nil {
		return err
	}
	*dst = cloneStrings(s)
	return nil
}

// statusFromProps builds a Status from a GetAll reply.
func statusFromProps(m map[string]dbus.Variant) Status {
	var st Status
	for name, v := range m {
		_ = applyProperty(&st, name, v)
	}
	if st.PendingLearnedWords == nil {
		st.PendingLearnedWords = []string{}
	}
	if st.ConfigWarnings == nil {
		st.ConfigWarnings = []string{}
	}
	return st
}
