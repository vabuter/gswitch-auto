package runtime

import (
	"github.com/arumata/gswitch/internal/gswitch/focus"
	"github.com/arumata/gswitch/internal/gswitch/fold"
	"github.com/arumata/gswitch/internal/gswitch/userconfig"
)

// gswitch-auto: glue between the Switcher and the focus sources (the Shell
// extension client and, on X11, the EWMH source, merged by focus.Merged). A
// focus change never clears the converter buffer (C24).

// IPC FocusSource values.
const (
	forkFocusSourceActive = "active"
	forkFocusSourceX11    = "x11"
	forkFocusSourceAbsent = "absent"
	forkFocusSourceOff    = "off"
)

// initFocusClient builds the merged focus source from the platform
// selection (SPEC-X11 §2.2): the extension client when FocusExt, the EWMH
// source when FocusX11, both started by startFork. Without either (focus-
// source=off, root) no focus source exists.
func (s *Switcher) initFocusClient() {
	sel := s.platform
	if !sel.FocusExt && !sel.FocusX11 {
		return
	}
	m := focus.NewMerged(focus.MergedOptions{
		OnChange: func(fs focus.State) {
			s.enqueueControl(func() { s.onFocusState(fs) })
			s.pushStatus()
		},
		Logf: s.logDebug,
	})
	if sel.FocusExt {
		s.focusClient = focus.New(focus.Options{OnChange: m.ExtensionChanged, Logf: s.logDebug})
		m.SetExtension(s.focusClient)
	}
	if sel.FocusX11 {
		s.focusX11 = focus.NewX11Source(focus.X11Options{
			Display:  x11DisplayFor(s.sessionEnv),
			OnChange: m.X11Changed,
			Logf:     s.logDebug,
		})
		m.SetX11(s.focusX11)
	}
	s.focusMerged = m
}

// forkFocusPolicy builds the focus policy of a loaded configuration. Excluded
// is the (never nil) application list; ListSet is nil-receiver safe anyway.
// wayland is PlatformSelection.Wayland: RequirePurpose is the effective
// auto-strict-purpose, c.Settings.StrictPurposeFor(wayland) (SPEC-X11 §3.3).
func forkFocusPolicy(c *userconfig.Config, wayland bool) *focus.Policy {
	st := c.Settings
	return &focus.Policy{
		Excluded:         c.Apps,
		SkipPassword:     st.SkipPassword,
		SkipTerminal:     st.SkipTerminal,
		SkipWhenLocked:   st.SkipLocked,
		RequireExtension: st.RequireFocus,
		RequirePurpose:   st.StrictPurposeFor(wayland),
	}
}

// forkNormalizeFocus fills the normalised application keys the client
// normally sets, so that a raw state (e.g. "org.gnome.Ptyxis.desktop") is
// matched against normalised exclusion entries.
func forkNormalizeFocus(st focus.State) focus.State {
	if st.AppKey == "" && st.AppID != "" {
		st.AppKey = fold.AppKey(st.AppID)
	}
	if st.WmClassKey == "" && st.WmClass != "" {
		st.WmClassKey = fold.AppKey(st.WmClass)
	}
	return st
}

// forkFocusDecision applies the policy to a focus state. Without a policy
// everything is allowed.
func forkFocusDecision(st focus.State, p *focus.Policy) (allowed bool, reason string) {
	if p == nil {
		return true, ""
	}
	return p.Allow(forkNormalizeFocus(st))
}

// focusState is the current merged focus state; the zero State (extension
// unavailable, no application) when there is no focus source.
func (s *Switcher) focusState() focus.State {
	if s.focusMerged == nil {
		return focus.State{}
	}
	return forkNormalizeFocus(s.focusMerged.Snapshot())
}

// autoFocusSnapshot is the engine's Focus dependency.
func (s *Switcher) autoFocusSnapshot() FocusSnapshot {
	st := s.focusState()
	ok, reason := forkFocusDecision(st, s.focusPolicy.Load())
	return FocusSnapshot{Seq: st.Seq, Allowed: ok, Reason: reason}
}

// onFocusState runs on the Run goroutine for every focus change: it cancels a
// pending fix and disarms the undo (the engine also checks the focus Seq).
func (s *Switcher) onFocusState(focus.State) {
	if s.auto != nil {
		s.auto.Cancel(forkReasonFocus)
	}
}

// focusSourceName is the IPC FocusSource value: off (no focus source),
// active (extension owned), x11 (application identity from the EWMH source
// while the extension is absent), else absent.
func (s *Switcher) focusSourceName(st focus.State) string {
	switch {
	case s.focusMerged == nil:
		return forkFocusSourceOff
	case st.Available:
		return forkFocusSourceActive
	case s.focusMerged.AppSource() == focus.AppSourceX11:
		return forkFocusSourceX11
	default:
		return forkFocusSourceAbsent
	}
}
