package runtime

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/arumata/gswitch/internal/gswitch/layoutstate"
)

// The confirm timing of the automatic path, per platform (SPEC-X11 §2.1
// rev. 4): GNOME's popup for a binding without a modifier applies the switch
// only gnomeNoModsPopupTimeout after the last key release, so the Wayland
// timings (2 x 20 x 50 ms) would fail every fix and re-press the key into the
// open popup.
func TestAutoSwitchPlanPerPlatform(t *testing.T) {
	tests := []struct {
		name         string
		lsKeys       []uint16
		gnome        bool
		verifyX11    bool
		shiftLaunch7 bool
		wantKeys     []uint16
		wantAttempts int
		wantNoMods   bool
	}{
		{name: "Fedora GNOME Wayland <Alt>Shift_L", lsKeys: []uint16{KEY_LEFTALT, KEY_LEFTSHIFT}, gnome: true,
			wantKeys: []uint16{KEY_LEFTALT, KEY_LEFTSHIFT}, wantAttempts: gnomeWaylandLayoutSwitchAttempts},
		{name: "Debian GNOME X11 XF86Launch7 with <Shift>XF86Launch7", lsKeys: []uint16{KEY_F16}, gnome: true, verifyX11: true, shiftLaunch7: true,
			wantKeys: []uint16{KEY_LEFTSHIFT, KEY_F16}, wantAttempts: gnomeWaylandLayoutSwitchAttempts},
		{name: "Debian GNOME X11 XF86Launch7 without the Shift binding", lsKeys: []uint16{KEY_F16}, gnome: true, verifyX11: true,
			wantKeys: []uint16{KEY_F16}, wantAttempts: 1, wantNoMods: true},
		{name: "GNOME single-key Caps_Lock binding", lsKeys: []uint16{KEY_CAPSLOCK}, gnome: true,
			wantKeys: []uint16{KEY_CAPSLOCK}, wantAttempts: 1, wantNoMods: true},
		{name: "GNOME binding of a lone modifier key", lsKeys: []uint16{KEY_RIGHTALT}, gnome: true,
			wantKeys: []uint16{KEY_RIGHTALT}, wantAttempts: 1, wantNoMods: true},
		{name: "non-GNOME X11 (blind upstream keys)", lsKeys: []uint16{KEY_CAPSLOCK},
			wantKeys: []uint16{KEY_CAPSLOCK}, wantAttempts: gnomeWaylandLayoutSwitchAttempts},
		{name: "no layout switch keys (degraded)", gnome: true,
			wantKeys: nil, wantAttempts: gnomeWaylandLayoutSwitchAttempts},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Switcher{converter: NewConverter()}
			s.converter.LSKeys = tt.lsKeys
			s.platform.GNOME = tt.gnome
			s.verifyGNOMEX11LayoutSwitch = tt.verifyX11
			s.autoShiftLaunch7 = tt.shiftLaunch7
			plan := s.autoSwitchPlan()
			if !slices.Equal(plan.keys, tt.wantKeys) || plan.attempts != tt.wantAttempts || plan.noMods != tt.wantNoMods {
				t.Fatalf("plan = %+v, want keys %v attempts %d noMods %v", plan, tt.wantKeys, tt.wantAttempts, tt.wantNoMods)
			}
			if plan.noMods {
				// One window that outlasts NO_MODS_TIMEOUT with a margin.
				if w := plan.window(); w < gnomeNoModsPopupTimeout+500*time.Millisecond {
					t.Fatalf("no-modifier window = %v, want > %v + 500 ms", w, gnomeNoModsPopupTimeout)
				}
			} else if w := plan.window(); w != time.Second {
				t.Fatalf("window = %v, want the 20 x 50 ms Wayland window", w)
			}
		})
	}
}

// autoSwitchTestSwitcher is a GNOME X11 switcher with LSKeys [KEY_F16] whose
// replay goes to a file, with "a sp b c" in the buffer.
func autoSwitchTestSwitcher(t *testing.T) (*Switcher, func() []KeyEvent, []KeyEvent) {
	t.Helper()
	s, output := emitTestSwitcher(t, emitTestPathBlind, &Config{LayoutSwitchKey: []uint16{KEY_F16}})
	s.platform.GNOME = true
	s.verifyGNOMEX11LayoutSwitch = true
	s.eventChan = make(chan *InputEvent, 8)
	for _, ev := range emitTestSeq(t, "a sp b c") {
		s.converter.Push(ev.Code, ev.Value)
	}
	events := s.converter.Convert(ActionConvertWord)
	if got, want := emitTestFormat(events), "186+ 186- 14+ 14- 14+ 14- 48+ 48- 46+ 46-"; got != want {
		t.Fatalf("fixture events = %s, want %s", got, want)
	}
	return s, func() []KeyEvent { return emitTestCanonicalEmitted(emitTestCaptured(t, output)) }, events
}

// timedReader reports the layout as switched once the injected sleeps add up
// to at least at.
type timedReader struct {
	elapsed *time.Duration
	at      time.Duration
	calls   int
}

func (r *timedReader) CurrentState() (string, error) {
	r.calls++
	if r.at > 0 && *r.elapsed >= r.at {
		return "1@2", nil
	}
	return "0@1", nil
}

func autoSwitchTestClock(t *testing.T) *time.Duration {
	t.Helper()
	original := emitSleep
	t.Cleanup(func() { emitSleep = original })
	var total time.Duration
	emitSleep = func(d time.Duration) { total += d }
	return &total
}

func countPresses(events []KeyEvent, code uint16) int {
	n := 0
	for _, ev := range events {
		if ev.Code == code && ev.Value == K_DOWN {
			n++
		}
	}
	return n
}

// With the fork's <Shift>XF86Launch7 binding the automatic switch is
// Shift+F16: the popup finishes on the Shift release, the switch is seen
// within the first polls.
func TestAutoEmitGNOMEX11SwitchesWithShiftLaunch7(t *testing.T) {
	clock := autoSwitchTestClock(t)
	s, emitted, events := autoSwitchTestSwitcher(t)
	s.autoShiftLaunch7 = true
	reader := &timedReader{elapsed: clock, at: 100 * time.Millisecond}
	err := s.emitConversionEvents(events, emitOptions{reader: reader, confirm: true, plan: s.autoSwitchPlan()})
	if err != nil {
		t.Fatalf("emitConversionEvents() error = %v", err)
	}
	want := "42- 54- 42+ 186+ 186- 42- 14+ 14- 14+ 14- 48+ 48- 46+ 46-"
	if got := emitTestFormat(emitted()); got != want {
		t.Fatalf("emitted %s, want %s", got, want)
	}
}

// Without a modifier the switch is pressed once and awaited past GNOME's
// NO_MODS_TIMEOUT: a popup that applies the switch 1.5 s after the release
// confirms the fix (the Wayland timings gave up after 1 s and pressed F16
// again, which moved the popup's selection back to the current source).
func TestAutoEmitNoModifierBindingWaitsForThePopup(t *testing.T) {
	clock := autoSwitchTestClock(t)
	s, emitted, events := autoSwitchTestSwitcher(t)
	reader := &timedReader{elapsed: clock, at: gnomeNoModsPopupTimeout + 100*time.Millisecond}
	// Keys typed while the popup held the grab were swallowed by GNOME.
	for _, ev := range emitTestSeq(t, "d e") {
		s.eventChan <- &InputEvent{Type: EV_KEY, Code: ev.Code, Value: ev.Value}
	}
	err := s.emitConversionEvents(events, emitOptions{reader: reader, confirm: true, plan: s.autoSwitchPlan()})
	if err != nil {
		t.Fatalf("emitConversionEvents() error = %v", err)
	}
	got := emitted()
	if n := countPresses(got, KEY_F16); n != 1 {
		t.Fatalf("F16 pressed %d times, want once: %s", n, emitTestFormat(got))
	}
	if want := "42- 54- 186+ 186- 14+ 14- 14+ 14- 48+ 48- 46+ 46-"; emitTestFormat(got) != want {
		t.Fatalf("emitted %s, want %s", emitTestFormat(got), want)
	}
	if len(s.eventChan) != 0 {
		t.Fatalf("%d swallowed key events are still queued for the buffer", len(s.eventChan))
	}
}

// A modifier-less switch that never happens fails after one window, without
// a second press (which would act inside a still open popup), and the input
// queued meanwhile is discarded as well.
func TestAutoEmitNoModifierBindingNeverRepresses(t *testing.T) {
	clock := autoSwitchTestClock(t)
	s, emitted, events := autoSwitchTestSwitcher(t)
	reader := &timedReader{elapsed: clock}
	s.eventChan <- &InputEvent{Type: EV_KEY, Code: KEY_SPACE, Value: K_DOWN}
	err := s.emitConversionEvents(events, emitOptions{reader: reader, confirm: true, plan: s.autoSwitchPlan()})
	if err == nil || !strings.Contains(err.Error(), "did not change") {
		t.Fatalf("emitConversionEvents() error = %v, want an unconfirmed switch", err)
	}
	got := emitted()
	if want := "42- 54- 186+ 186-"; emitTestFormat(got) != want {
		t.Fatalf("emitted %s, want one press and no text: %s", emitTestFormat(got), want)
	}
	if *clock < gnomeNoModsPopupTimeout+500*time.Millisecond {
		t.Fatalf("gave up after %v, before the popup could have applied the switch", *clock)
	}
	if len(s.eventChan) != 0 {
		t.Fatal("input queued during the switch was not discarded")
	}
}

// The Fedora path is unchanged: <Alt>Shift_L, 2 attempts of 20 polls.
func TestAutoEmitWaylandModifierBindingKeepsTimings(t *testing.T) {
	autoSwitchTestClock(t)
	s, output := emitTestSwitcher(t, emitTestPathBlind, &Config{LayoutSwitchKey: []uint16{KEY_LEFTALT, KEY_LEFTSHIFT}})
	s.platform.GNOME = true
	for _, ev := range emitTestSeq(t, "g h") {
		s.converter.Push(ev.Code, ev.Value)
	}
	reader := &emitTestReader{states: []string{"0@1"}}
	err := s.emitConversionEvents(s.converter.Convert(ActionConvertWord),
		emitOptions{reader: reader, confirm: true, plan: s.autoSwitchPlan()})
	if err == nil {
		t.Fatal("emitConversionEvents() error = nil, want an unconfirmed switch")
	}
	if want := 1 + gnomeWaylandLayoutSwitchAttempts*gnomeWaylandLayoutGroupPollAttempts; reader.calls != want {
		t.Fatalf("reader calls = %d, want %d", reader.calls, want)
	}
	if n := countPresses(emitTestCaptured(t, output), KEY_LEFTALT); n != gnomeWaylandLayoutSwitchAttempts {
		t.Fatalf("toggle pressed %d times, want %d", n, gnomeWaylandLayoutSwitchAttempts)
	}
}

func TestInitAutoSwitchBinding(t *testing.T) {
	newSwitcher := func() *Switcher {
		s := &Switcher{converter: NewConverter(), verifyGNOMEX11LayoutSwitch: true}
		s.converter.LSKeys = []uint16{KEY_F16}
		s.tracker = layoutstate.New(layoutstate.Options{Layouts: [2]layoutstate.Layout{{Name: "us"}, {Name: "ru"}}})
		return s
	}
	saved := forkEnsureShiftLaunch7
	t.Cleanup(func() { forkEnsureShiftLaunch7 = saved })

	calls := 0
	forkEnsureShiftLaunch7 = func(*SessionEnv) (bool, error) { calls++; return true, nil }
	s := newSwitcher()
	s.initAutoSwitchBinding()
	if !s.autoShiftLaunch7 || calls != 1 {
		t.Fatalf("autoShiftLaunch7 = %v after %d calls, want true after 1", s.autoShiftLaunch7, calls)
	}

	forkEnsureShiftLaunch7 = func(*SessionEnv) (bool, error) { calls++; return false, errors.New("dconf read-only") }
	s = newSwitcher()
	s.initAutoSwitchBinding()
	if s.autoShiftLaunch7 {
		t.Fatal("autoShiftLaunch7 = true after a failed gsettings write")
	}

	for name, mutate := range map[string]func(*Switcher){
		"no tracker":      func(s *Switcher) { s.tracker = nil },
		"not GNOME X11":   func(s *Switcher) { s.verifyGNOMEX11LayoutSwitch = false },
		"configured keys": func(s *Switcher) { s.converter.LSKeys = []uint16{KEY_LEFTALT, KEY_LEFTSHIFT} },
	} {
		calls = 0
		forkEnsureShiftLaunch7 = func(*SessionEnv) (bool, error) {
			calls++
			return false, fmt.Errorf("must not be called (%s)", name)
		}
		s = newSwitcher()
		mutate(s)
		s.initAutoSwitchBinding()
		if calls != 0 || s.autoShiftLaunch7 {
			t.Fatalf("%s: ensure called %d times, autoShiftLaunch7 = %v", name, calls, s.autoShiftLaunch7)
		}
	}
}
