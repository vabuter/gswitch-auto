package runtime

import (
	"github.com/arumata/gswitch/internal/gswitch/focus"
	"github.com/arumata/gswitch/internal/gswitch/fold"
	"github.com/arumata/gswitch/internal/gswitch/userconfig"
)

// gswitch-auto: glue between the Switcher and the focus extension client.
// A focus change never clears the converter buffer (C24).

// IPC FocusSource values.
const (
	forkFocusSourceActive = "active"
	forkFocusSourceAbsent = "absent"
	forkFocusSourceOff    = "off"
)

// initFocusClient builds the focus client (started by startFork) unless
// focus-source=off or the daemon runs as root.
func (s *Switcher) initFocusClient(st userconfig.Settings) {
	if st.FocusSource != userconfig.SourceAuto || forkGeteuid() == 0 {
		return
	}
	s.focusClient = focus.New(focus.Options{
		OnChange: func(fs focus.State) {
			s.enqueueControl(func() { s.onFocusState(fs) })
			s.pushStatus()
		},
		Logf: s.logDebug,
	})
}

// forkFocusPolicy builds the focus policy of a loaded configuration. Excluded
// is the (never nil) application list; ListSet is nil-receiver safe anyway.
func forkFocusPolicy(c *userconfig.Config) *focus.Policy {
	st := c.Settings
	return &focus.Policy{
		Excluded:         c.Apps,
		SkipPassword:     st.SkipPassword,
		SkipTerminal:     st.SkipTerminal,
		SkipWhenLocked:   st.SkipLocked,
		RequireExtension: st.RequireFocus,
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

// focusState is the current focus state; the zero State (extension
// unavailable) when there is no focus client.
func (s *Switcher) focusState() focus.State {
	if s.focusClient == nil {
		return focus.State{}
	}
	return forkNormalizeFocus(s.focusClient.Snapshot())
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

// focusSourceName is the IPC FocusSource value.
func (s *Switcher) focusSourceName(st focus.State) string {
	switch {
	case s.focusClient == nil:
		return forkFocusSourceOff
	case st.Available:
		return forkFocusSourceActive
	default:
		return forkFocusSourceAbsent
	}
}
