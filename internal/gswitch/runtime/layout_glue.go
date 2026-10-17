package runtime

import (
	"slices"
	"time"

	"github.com/arumata/gswitch/internal/gswitch/layoutstate"
	"github.com/arumata/gswitch/internal/gswitch/userconfig"
)

// gswitch-auto: glue between the Switcher and the IBus layout tracker
// (layoutstate). Nothing here ever clears the converter buffer (C5).

// initTracker builds the layout tracker when the layout pair is known and
// layout-source permits it (auto in a GNOME session, or ibus; never as root).
// The tracker is started by startFork.
func (s *Switcher) initTracker(st userconfig.Settings) {
	if len(s.layoutSpecs) != 2 {
		s.logDebug("layout tracker: no layout pair")
		return
	}
	if !layoutstate.IBusSourceEnabled(st.LayoutSource, s.sessionEnv, forkGeteuid()) {
		s.logDebug("layout tracker: disabled (layout-source=%s)", st.LayoutSource)
		return
	}
	t := layoutstate.New(layoutstate.Options{
		Layouts: [2]layoutstate.Layout{
			{Name: s.layoutSpecs[0].Name, Variant: s.layoutSpecs[0].Variant},
			{Name: s.layoutSpecs[1].Name, Variant: s.layoutSpecs[1].Variant},
		},
		HintTimeout: time.Duration(st.LayoutHintTimeoutMs) * time.Millisecond,
		Logger:      s.log,
		DebugLogger: s.logDebug,
	})
	t.AddSource(layoutstate.NewIBusSource(layoutstate.AddressEnvFromProcess(), s.log))
	t.AddResync(layoutstate.NewGSettingsResync(s.sessionEnv))
	t.OnChange(func(state layoutstate.State) {
		s.enqueueControl(func() { s.onLayoutState(state) })
		s.pushStatus()
	})
	s.tracker = t
}

// forkLayoutSnapshot maps a tracker state to the engine's view.
func forkLayoutSnapshot(st layoutstate.State) LayoutSnapshot {
	return LayoutSnapshot{
		Index:      st.Index,
		Generation: st.Generation,
		Confirmed:  st.Confidence == layoutstate.Confirmed && st.Index >= 0,
	}
}

// autoLayoutSnapshot is the engine's Layout dependency.
func (s *Switcher) autoLayoutSnapshot() LayoutSnapshot {
	return forkLayoutSnapshot(s.tracker.State())
}

// noteOwnSwitch marks every tracker generation up to the current one as our
// own switch. emitConversionEvents calls it after each successful emission
// (after a confirmed switch the tracker already holds the new generation;
// after a blind manual switch the IBus signal normally arrives during the
// replay), restoreLayoutIfChordFlipped after its restore.
func (s *Switcher) noteOwnSwitch() {
	if s.tracker == nil {
		return
	}
	s.expectOwnGen = s.tracker.State().Generation
}

// onLayoutState runs on the Run goroutine for every tracker change. Our own
// switches keep the engine state; an external change cancels a pending fix
// and disarms the undo. A late signal of a blind manual switch may be
// classified as external: harmless, the manual path already cancelled.
func (s *Switcher) onLayoutState(st layoutstate.State) {
	if s.auto == nil {
		return
	}
	s.auto.OnLayoutChange(forkLayoutSnapshot(st), st.Generation <= s.expectOwnGen)
}

// observeLayoutSwitchChord gives the tracker a Pending hint when the physical
// layout-switch chord completes (all LSKeys down), once per press cycle.
func (s *Switcher) observeLayoutSwitchChord(event *InputEvent) {
	if s.tracker == nil || s.converter == nil {
		return
	}
	keys := s.converter.LSKeys
	if len(keys) == 0 || !slices.Contains(keys, event.Code) {
		return
	}
	switch event.Value {
	case K_DOWN:
		if s.lsKeysDown == nil {
			s.lsKeysDown = make(map[uint16]bool, len(keys))
		}
		wasAll := s.layoutSwitchKeysDown(keys)
		s.lsKeysDown[event.Code] = true
		if !wasAll && s.layoutSwitchKeysDown(keys) {
			s.tracker.NoteSwitchHint()
		}
	case K_UP:
		delete(s.lsKeysDown, event.Code)
	}
}

func (s *Switcher) layoutSwitchKeysDown(keys []uint16) bool {
	for _, k := range keys {
		if !s.lsKeysDown[k] {
			return false
		}
	}
	return true
}

// restoreLayoutIfChordFlipped switches the layout back when the GNOME layout
// binding fired during the selection chord (C14). When the press order could
// fire the binding it first waits up to forkRestoreWait for the late IBus
// signal, because the chord fires on its last key release, which can precede
// the signal. A confirmation failure is logged; the selection conversion runs
// either way.
func (s *Switcher) restoreLayoutIfChordFlipped() {
	if s.tracker == nil {
		return
	}
	start := s.chordStartLayout
	if start.Confidence != layoutstate.Confirmed || start.Index < 0 {
		return
	}
	st := s.tracker.State()
	if s.chordSawAltShift {
		deadline := forkNow().Add(forkRestoreWait)
		for st.Generation <= start.Generation && forkNow().Before(deadline) {
			forkSleep(forkRestorePoll)
			st = s.tracker.State()
		}
	}
	if st.Confidence != layoutstate.Confirmed || st.Index < 0 || st.Index == start.Index ||
		st.Generation <= s.expectOwnGen {
		return
	}
	if s.vKeyboard == nil || s.converter == nil || len(s.converter.LSKeys) == 0 {
		s.logError("selection chord: layout flipped by GNOME binding, no layout switch key to restore it")
		return
	}
	delay := time.Duration(0)
	if s.config != nil {
		delay = time.Duration(s.config.Delay) * time.Millisecond
	}
	err := triggerAndConfirmGNOMEWaylandLayoutSwitch(
		trackerLayoutStateReader{tracker: s.tracker},
		s.converter.LSKeys,
		func(event KeyEvent) error {
			return s.vKeyboard.EmitKey(event.Code, event.Value)
		},
		gnomeWaylandLayoutSwitchAttempts,
		gnomeWaylandLayoutGroupPollAttempts,
		delay,
		emitSleep,
	)
	if err != nil {
		s.logError("selection chord: layout restore failed: %v", err)
		return
	}
	s.noteOwnSwitch()
	s.log("selection chord: restored layout flipped by GNOME binding")
}
