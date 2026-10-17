package runtime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/arumata/gswitch/internal/gswitch/focus"
	"github.com/arumata/gswitch/internal/gswitch/layoutstate"
	"github.com/arumata/gswitch/internal/gswitch/userconfig"
)

// gswitch-auto WP11 tests of the switcher hooks (processKeyEvent,
// performConversion, the Run-loop helpers) with the fork wired in.

// forkTestGestureRun feeds script to a switcher and returns the selection
// calls, the canonical emitted events and the final buffer.
type forkTestGestureRun struct {
	selections []selectionTransform
	emitted    []KeyEvent
	buffer     []KeyEvent
}

func forkTestRunGesture(t *testing.T, cfg Config, withFork bool, script string) (forkTestGestureRun, *Switcher, *forkTestIPC) {
	t.Helper()
	c := cfg
	s, out := forkTestSwitcher(t, &c)
	var run forkTestGestureRun
	s.selectionHandler = func(tr selectionTransform) { run.selections = append(run.selections, tr) }
	var tap *forkTestIPC
	if withFork {
		forkTestDefaultChords(s)
		tap = forkTestAttachIPC(s)
	}
	forkTestFeed(t, s, script)
	run.emitted = forkTestCanonical(forkTestCaptured(t, out))
	run.buffer = s.converter.GetBuffer()
	return run, s, tap
}

func TestProcessKeyEventChordsKeepUpstreamGestures(t *testing.T) {
	tests := []struct {
		name    string
		convKey uint16
		script  string
	}{
		{name: "double shift word", script: "a b c L L"},
		{name: "held shift phrase", script: "a SP b c L+ R R L-"},
		{name: "custom key layout conversion", convKey: testKeyPause, script: "LCTRL+ PAUSE LCTRL-"},
		{name: "custom key swap case", convKey: testKeyPause, script: "LCTRL+ L+ PAUSE L- LCTRL-"},
		{name: "ctrl double shift layout conversion", script: "LCTRL+ L L LCTRL-"},
		{name: "ctrl held shift double shift swap case", script: "LCTRL+ L+ R R L- LCTRL-"},
		{name: "swap case with ctrl released first", script: "LCTRL+ L+ R R LCTRL- L-"},
	}
	for _, swap := range []bool{false, true} {
		for _, tt := range tests {
			t.Run(fmt.Sprintf("%s/swap=%t", tt.name, swap), func(t *testing.T) {
				cfg := Config{
					ConvertKey:              tt.convKey,
					SwapConversionModifiers: swap && tt.convKey == 0,
					LayoutSwitchKey:         []uint16{125},
				}
				want, _, _ := forkTestRunGesture(t, cfg, false, tt.script)
				got, s, tap := forkTestRunGesture(t, cfg, true, tt.script)
				if !slices.Equal(got.selections, want.selections) {
					t.Fatalf("selection calls = %v, upstream %v", got.selections, want.selections)
				}
				if !slices.Equal(got.emitted, want.emitted) {
					t.Fatalf("emitted = %v, upstream %v", got.emitted, want.emitted)
				}
				if !slices.Equal(got.buffer, want.buffer) {
					t.Fatalf("buffer = %v, upstream %v", got.buffer, want.buffer)
				}
				if _, n := tap.lastToggle(); n != 0 || s.AutoEnabled() {
					t.Fatalf("toggle chord fired %d times during an upstream gesture", n)
				}
			})
		}
	}
}

func TestProcessKeyEventToggleChord(t *testing.T) {
	for _, chord := range []string{"L+ R+ R- L-", "L+ R+ L- R-"} {
		t.Run(chord, func(t *testing.T) {
			s, out := forkTestSwitcher(t, &Config{LayoutSwitchKey: []uint16{125}})
			forkTestDefaultChords(s)
			tap := forkTestAttachIPC(s)

			forkTestFeed(t, s, chord)
			if !s.AutoEnabled() {
				t.Fatal("toggle chord did not enable auto mode")
			}
			if last, n := tap.lastToggle(); n != 1 || last != (forkTestToggle{true, "chord"}) {
				t.Fatalf("toggle notifications = %d, last %+v; want one, on by chord", n, last)
			}
			if buf := s.converter.GetBuffer(); len(buf) != 0 {
				t.Fatalf("buffer after the chord = %v, want no shift events", buf)
			}

			forkTestFeed(t, s, "a b c L+ L- L+")
			if !s.converter.Push(KEY_LEFTSHIFT, K_UP) {
				t.Fatal("final shift release not buffered")
			}
			if got := s.converter.Process(); got != ActionConvertWord {
				t.Fatalf("double-Shift after the chord: Process() = %v, want ActionConvertWord", got)
			}
			if got := forkTestCaptured(t, out); len(got) != 0 {
				t.Fatalf("chord emitted key events: %v", got)
			}

			// After text the chord keeps the word in the buffer.
			s.converter.ClearBuffer()
			forkTestFeed(t, s, "a b c "+chord)
			if s.AutoEnabled() {
				t.Fatal("second chord did not disable auto mode")
			}
			buf := s.converter.GetBuffer()
			if len(buf) != 3 || Shifts[buf[len(buf)-1].Code] {
				t.Fatalf("buffer after text and chord = %v, want the three letters only", buf)
			}
		})
	}
}

func TestProcessKeyEventSelectionChord(t *testing.T) {
	releases := []string{"RCTRL- RALT- L-", "L- RALT- RCTRL-", "RALT- L- RCTRL-"}
	for _, convKey := range []uint16{0, testKeyPause} {
		for _, rel := range releases {
			t.Run(fmt.Sprintf("convkey=%d/%s", convKey, rel), func(t *testing.T) {
				var selections []selectionTransform
				s, out := forkTestSwitcher(t, &Config{ConvertKey: convKey, LayoutSwitchKey: []uint16{125}})
				s.selectionHandler = func(tr selectionTransform) { selections = append(selections, tr) }
				forkTestDefaultChords(s)
				tap := forkTestAttachIPC(s)

				forkTestFeed(t, s, "a b c L+ RALT+ RCTRL+ "+rel)
				if !slices.Equal(selections, []selectionTransform{selectionConvertLayout}) {
					t.Fatalf("selection calls = %v, want one layout conversion", selections)
				}
				if buf := s.converter.GetBuffer(); len(buf) != 0 {
					t.Fatalf("buffer after the selection chord = %v, want empty", buf)
				}
				if _, n := tap.lastToggle(); n != 0 {
					t.Fatalf("selection chord toggled auto mode %d times", n)
				}
				if got := forkTestCaptured(t, out); len(got) != 0 {
					t.Fatalf("selection chord emitted key events: %v", got)
				}
			})
		}
	}
}

// forkTestRestoreSwitcher builds a switcher with the default chords and a
// tracker Confirmed on "us"; the stubbed pause of the switch confirmation
// lets GNOME "react" to the restore by switching back to us.
func forkTestRestoreSwitcher(t *testing.T) (*Switcher, *layoutstate.Tracker, *[]selectionTransform, func() []KeyEvent) {
	t.Helper()
	s, out := forkTestSwitcher(t, &Config{LayoutSwitchKey: []uint16{KEY_LEFTALT, KEY_LEFTSHIFT}})
	selections := &[]selectionTransform{}
	s.selectionHandler = func(tr selectionTransform) { *selections = append(*selections, tr) }
	forkTestDefaultChords(s)
	tr := forkTestTracker(t)
	tr.Engine(layoutstate.SourceIBus, "xkb:us::eng")
	s.tracker = tr
	orig := emitSleep
	emitSleep = func(d time.Duration) {
		if d == gnomeWaylandLayoutGroupPollDelay {
			tr.Engine(layoutstate.SourceIBus, "xkb:us::eng")
		}
	}
	t.Cleanup(func() { emitSleep = orig })
	return s, tr, selections, func() []KeyEvent { return forkTestCaptured(t, out) }
}

func TestRestoreOnFlipWaitsForLateSignal(t *testing.T) {
	restore := []KeyEvent{
		{Code: KEY_LEFTALT, Value: K_DOWN}, {Code: KEY_LEFTSHIFT, Value: K_DOWN},
		{Code: KEY_LEFTSHIFT, Value: K_UP}, {Code: KEY_LEFTALT, Value: K_UP},
	}
	flipLater := func(tr *layoutstate.Tracker, d time.Duration) chan struct{} {
		done := make(chan struct{})
		time.AfterFunc(d, func() {
			tr.Engine(layoutstate.SourceIBus, "xkb:ru::rus")
			close(done)
		})
		return done
	}

	t.Run("late signal after an alt-first chord is restored", func(t *testing.T) {
		s, tr, selections, emitted := forkTestRestoreSwitcher(t)
		forkTestFeed(t, s, "RALT+ L+ RCTRL+ RCTRL- RALT-")
		if !s.chordSawAltShift {
			t.Fatal("Shift pressed while RAlt was held was not recorded")
		}
		done := flipLater(tr, 60*time.Millisecond)
		start := time.Now()
		forkTestFeed(t, s, "L-")
		<-done
		if got := emitted(); !slices.Equal(got, restore) {
			t.Fatalf("emitted %v, want the layout restore %v", got, restore)
		}
		if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
			t.Fatalf("restore did not wait for the late signal (%v)", elapsed)
		}
		if st := tr.State(); st.Index != 0 || st.Generation > s.expectOwnGen {
			t.Fatalf("after restore: state %v, expectOwnGen %d", st, s.expectOwnGen)
		}
		if len(*selections) != 1 {
			t.Fatalf("selection calls = %v, want 1", *selections)
		}
	})

	t.Run("no signal means no restore", func(t *testing.T) {
		s, _, selections, emitted := forkTestRestoreSwitcher(t)
		start := time.Now()
		forkTestFeed(t, s, "RALT+ L+ RCTRL+ RCTRL- RALT- L-")
		elapsed := time.Since(start)
		if got := emitted(); len(got) != 0 {
			t.Fatalf("emitted %v without a layout flip", got)
		}
		if elapsed < forkRestoreWait-20*time.Millisecond || elapsed > time.Second {
			t.Fatalf("waited %v, want about %v", elapsed, forkRestoreWait)
		}
		if len(*selections) != 1 {
			t.Fatalf("selection calls = %v, want 1", *selections)
		}
	})

	t.Run("shift-first chord does not wait", func(t *testing.T) {
		s, tr, selections, emitted := forkTestRestoreSwitcher(t)
		forkTestFeed(t, s, "L+ RALT+ RCTRL+ RCTRL- RALT-")
		if s.chordSawAltShift {
			t.Fatal("RAlt after Shift recorded as a risky order")
		}
		done := flipLater(tr, 120*time.Millisecond)
		start := time.Now()
		forkTestFeed(t, s, "L-")
		elapsed := time.Since(start)
		<-done
		if elapsed > 100*time.Millisecond {
			t.Fatalf("chord without Alt/Shift overlap waited %v", elapsed)
		}
		if got := emitted(); len(got) != 0 {
			t.Fatalf("emitted %v for a flip after the chord", got)
		}
		if len(*selections) != 1 {
			t.Fatalf("selection calls = %v, want 1", *selections)
		}
	})

	t.Run("flip already seen is restored at once", func(t *testing.T) {
		s, tr, _, emitted := forkTestRestoreSwitcher(t)
		forkTestFeed(t, s, "L+ RALT+ RCTRL+ RCTRL- RALT-")
		tr.Engine(layoutstate.SourceIBus, "xkb:ru::rus")
		forkTestFeed(t, s, "L-")
		if got := emitted(); !slices.Equal(got, restore) {
			t.Fatalf("emitted %v, want the layout restore %v", got, restore)
		}
	})

	t.Run("own switch is not restored", func(t *testing.T) {
		s, tr, _, emitted := forkTestRestoreSwitcher(t)
		forkTestFeed(t, s, "L+ RALT+ RCTRL+ RCTRL- RALT-")
		tr.Engine(layoutstate.SourceIBus, "xkb:ru::rus")
		s.noteOwnSwitch()
		forkTestFeed(t, s, "L-")
		if got := emitted(); len(got) != 0 {
			t.Fatalf("emitted %v for our own switch", got)
		}
	})
}

func TestSpaceArmsAndTickFires(t *testing.T) {
	s, out := forkTestSwitcher(t, &Config{LayoutSwitchKey: []uint16{KEY_LEFTALT, KEY_LEFTSHIFT}})
	env := forkTestFakeEngine(t, s)

	if got := s.loopWait(); got != EventLoopTimeoutMs*time.Millisecond {
		t.Fatalf("idle loopWait = %v, want %v", got, EventLoopTimeoutMs*time.Millisecond)
	}
	forkTestFeed(t, s, "g h b d t n SP+")
	if st := s.auto.Stats(); st.Armed != 1 {
		t.Fatalf("Space did not arm: %+v", st)
	}
	if got := s.loopWait(); got <= 0 || got > env.settings.Holdoff {
		t.Fatalf("armed loopWait = %v, want (0, %v]", got, env.settings.Holdoff)
	}
	r, reason, ok := lastWordRange(s.converter.GetBuffer(), 24)
	if !ok {
		t.Fatalf("lastWordRange: %s", reason)
	}
	want := s.converter.ConvertRange(r.Start, r.End)

	env.clock.Advance(env.settings.Holdoff + time.Millisecond)
	s.autoTick()
	if len(env.emits) != 0 {
		t.Fatal("fired while Space was still held")
	}
	forkTestFeed(t, s, "SP-")
	env.clock.Advance(50 * time.Millisecond)
	s.autoTick()
	if len(env.emits) != 1 {
		t.Fatalf("emits after Space release = %d, want 1 (stats %+v)", len(env.emits), s.auto.Stats())
	}
	if !slices.Equal(env.emits[0], want) {
		t.Fatalf("emitted %v, want ConvertRange %v", env.emits[0], want)
	}
	if st := s.auto.Stats(); st.Fired != 1 {
		t.Fatalf("stats after fire: %+v", st)
	}
	if got := s.loopWait(); got != EventLoopTimeoutMs*time.Millisecond {
		t.Fatalf("loopWait after fire = %v", got)
	}
	if got := forkTestCaptured(t, out); len(got) != 0 {
		t.Fatalf("upstream path emitted %v", got)
	}
}

func TestAutoEmitConfirmsWithoutVerifyFlags(t *testing.T) {
	clock := forkTestSetClock(t)
	orig := emitSleep
	emitSleep = func(time.Duration) {}
	t.Cleanup(func() { emitSleep = orig })

	s, out := forkTestSwitcher(t, &Config{LayoutSwitchKey: []uint16{KEY_LEFTALT, KEY_LEFTSHIFT}})
	forkTestWithLayouts(s)
	tr := forkTestTracker(t) // never changes after the toggle
	tr.Engine(layoutstate.SourceIBus, "xkb:us::eng")
	s.tracker = tr
	c := forkTestConfig(userconfig.DefaultSettings(), nil, nil)
	s.userCfg.Store(c)
	s.initDetector(c)
	s.EnableAuto()
	if s.auto == nil {
		t.Fatalf("auto engine unavailable: %s", s.autoReason)
	}
	if s.verifyGNOMEWaylandLayoutSwitch || s.verifyGNOMEX11LayoutSwitch {
		t.Fatal("test needs both verify flags false")
	}
	s.autoEnabled.Store(true)
	tap := forkTestAttachIPC(s)

	forkTestFeed(t, s, "g h b d t n SP")
	if st := s.auto.Stats(); st.Armed != 1 {
		t.Fatalf("ghbdtn did not arm with the real detector: %+v", st)
	}
	clock.Advance(time.Second)
	s.autoTick()

	emitted := forkTestCaptured(t, out)
	if n := forkTestCountCode(emitted, KEY_BACKSPACE); n != 0 {
		t.Fatalf("unconfirmed switch emitted %d backspaces: %v", n, emitted)
	}
	if n := forkTestCountCode(emitted, KEY_LEFTALT); n == 0 {
		t.Fatalf("the auto path did not try the confirmed switch: %v", emitted)
	}
	st := s.auto.Stats()
	if !strings.HasPrefix(st.LastReason, "emit:") || st.Fired != 0 || st.Skipped == 0 {
		t.Fatalf("stats = %+v, want a skip with reason emit:…", st)
	}
	if s.auto.lastFix != nil {
		t.Fatal("lastFix recorded for a failed emission")
	}
	if len(tap.corrected) != 0 {
		t.Fatalf("Corrected announced for a failed emission: %+v", tap.corrected)
	}
}

func TestUndoIntercept(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%t/double shift undoes", reverse), func(t *testing.T) {
			s, out := forkTestSwitcher(t, &Config{LayoutSwitchKey: []uint16{125}, ReverseMode: reverse})
			env := forkTestFakeEngine(t, s)
			forkTestFire(t, s, env)

			forkTestFeed(t, s, "L L")
			if len(env.emits) != 2 || !slices.Equal(env.emits[1], env.emits[0]) {
				t.Fatalf("undo emits = %d, want the fixed range replayed again", len(env.emits))
			}
			if got := forkTestCaptured(t, out); len(got) != 0 {
				t.Fatalf("the undo also ran the upstream conversion: %v", got)
			}
			if st := s.auto.Stats(); st.Undone != 1 {
				t.Fatalf("stats after undo: %+v", st)
			}
			if len(env.undos) != 1 || env.undos[0] != (forkTestUndo{"ghbdtn", true}) {
				t.Fatalf("OnUndo calls = %+v", env.undos)
			}

			// A second gesture falls through to upstream.
			forkTestFeed(t, s, "L L")
			if len(env.emits) != 2 {
				t.Fatal("second double-Shift undid again")
			}
			if got := forkTestCaptured(t, out); forkTestCountCode(got, KEY_BACKSPACE) == 0 {
				t.Fatalf("second double-Shift did not run the upstream conversion: %v", got)
			}
		})
	}

	t.Run("reverse=true/held shift falls through to upstream", func(t *testing.T) {
		s, out := forkTestSwitcher(t, &Config{LayoutSwitchKey: []uint16{125}, ReverseMode: true})
		env := forkTestFakeEngine(t, s)
		forkTestFire(t, s, env)

		forkTestFeed(t, s, "L+ R R L-")
		if len(env.emits) != 1 {
			t.Fatal("held-Shift gesture was taken as an undo")
		}
		got := forkTestCaptured(t, out)
		if n := forkTestCountCode(got, KEY_BACKSPACE); n != 7 {
			t.Fatalf("upstream word conversion emitted %d backspaces, want 7 (ghbdtn + Space): %v", n, got)
		}
		if s.auto.lastFix != nil {
			t.Fatal("manual conversion did not disarm the undo")
		}
	})

	t.Run("reverse=false/manual convert-all disarms the undo", func(t *testing.T) {
		s, out := forkTestSwitcher(t, &Config{LayoutSwitchKey: []uint16{125}})
		env := forkTestFakeEngine(t, s)
		forkTestFire(t, s, env)

		forkTestFeed(t, s, "L+ R R L-")
		if s.auto.lastFix != nil {
			t.Fatal("ConvertAll did not call Cancel(manual)")
		}
		afterAll := len(forkTestCaptured(t, out))
		if afterAll == 0 {
			t.Fatal("ConvertAll did not run upstream")
		}
		forkTestFeed(t, s, "L L")
		if len(env.emits) != 1 {
			t.Fatal("double-Shift after a manual conversion ran TryUndo")
		}
		if got := forkTestCaptured(t, out); len(got) <= afterAll {
			t.Fatal("double-Shift after a manual conversion did not run upstream")
		}
	})
}

func TestSuspendHotkeysConsumesEvents(t *testing.T) {
	var selections []selectionTransform
	s, out := forkTestSwitcher(t, &Config{LayoutSwitchKey: []uint16{125}})
	s.selectionHandler = func(tr selectionTransform) { selections = append(selections, tr) }
	forkTestDefaultChords(s)
	env := forkTestFakeEngine(t, s)
	tap := forkTestAttachIPC(s)

	forkTestFeed(t, s, "x y")
	s.SuspendHotkeys(true)
	forkTestDrainControl(s)
	if !s.statusSnapshot().HotkeysSuspended {
		t.Fatal("HotkeysSuspended = false")
	}
	if last := tap.statuses[len(tap.statuses)-1]; !last.HotkeysSuspended {
		t.Fatal("suspension not published")
	}
	forkTestFeed(t, s, "a b c L L L+ R+ R- L- L+ RALT+ RCTRL+ RCTRL- RALT- L- g h b d t n SP")
	if buf := s.converter.GetBuffer(); len(buf) != 0 {
		t.Fatalf("buffer while suspended = %v, want empty", buf)
	}
	if got := forkTestCaptured(t, out); len(got) != 0 {
		t.Fatalf("gesture ran while suspended: %v", got)
	}
	if s.AutoEnabled() || len(selections) != 0 {
		t.Fatalf("chord ran while suspended: enabled=%v selections=%v", s.AutoEnabled(), selections)
	}
	if st := s.auto.Stats(); st.Armed != 0 || env.judge.calls != 0 {
		t.Fatalf("auto engine decided while suspended: %+v", st)
	}

	s.SuspendHotkeys(false)
	forkTestDrainControl(s)
	forkTestFeed(t, s, "b d t n SP")
	if st := s.auto.Stats(); st.Armed != 0 || st.LastReason != "tainted:discarded" {
		t.Fatalf("first word after the suspension: %+v, want tainted:discarded", st)
	}
	forkTestFeed(t, s, "a b c")
	if buf := s.converter.GetBuffer(); len(buf) < 3 {
		t.Fatalf("buffer after resume = %v", buf)
	}
}

func TestOnLayoutStateNeverClearsBuffer(t *testing.T) {
	s, _ := forkTestSwitcher(t, &Config{LayoutSwitchKey: []uint16{125}})
	forkTestFakeEngine(t, s)
	forkTestFeed(t, s, "g h b d t n SP")
	if s.auto.pending == nil {
		t.Fatal("fix not armed")
	}
	before := s.converter.GetBuffer()
	gen := s.converter.UndoGeneration()

	s.onLayoutState(layoutstate.State{Index: 1, Confidence: layoutstate.Confirmed, Generation: 9, Source: "ibus"})
	if s.auto.pending != nil || s.auto.Stats().LastReason != "layout" {
		t.Fatalf("external layout change did not cancel the pending fix: %+v", s.auto.Stats())
	}
	s.onFocusState(focus.State{Seq: 5})
	if got := s.converter.GetBuffer(); !slices.Equal(got, before) || s.converter.UndoGeneration() != gen {
		t.Fatalf("buffer changed by a layout/focus notification: %v -> %v", before, got)
	}
}

func TestOwnSwitchRecognised(t *testing.T) {
	s, _ := forkTestSwitcher(t, &Config{LayoutSwitchKey: []uint16{125}})
	env := forkTestFakeEngine(t, s)
	tr := forkTestTracker(t)
	tr.Engine(layoutstate.SourceIBus, "xkb:us::eng")
	s.tracker = tr
	forkTestFire(t, s, env)

	tr.Engine(layoutstate.SourceIBus, "xkb:ru::rus") // our switch
	s.noteOwnSwitch()
	s.onLayoutState(tr.State())
	if s.auto.lastFix == nil {
		t.Fatal("own switch disarmed the undo")
	}

	tr.Engine(layoutstate.SourceIBus, "xkb:us::eng") // external
	s.onLayoutState(tr.State())
	if s.auto.lastFix != nil {
		t.Fatal("external switch kept the undo armed")
	}

	// noteOwnSwitch is a no-op without a tracker.
	s.tracker = nil
	s.expectOwnGen = 0
	s.noteOwnSwitch()
	if s.expectOwnGen != 0 {
		t.Fatal("noteOwnSwitch without a tracker changed expectOwnGen")
	}
}

// forkTestStubGsettings answers mru-sources reads alternately with us and ru,
// so every confirmation sees exactly one change, and counts the reads.
func forkTestStubGsettings(t *testing.T) *int {
	t.Helper()
	orig := runGsettingsForGNOMEWayland
	calls := 0
	runGsettingsForGNOMEWayland = func(*SessionEnv, ...string) ([]byte, error) {
		calls++
		if calls%2 == 1 {
			return []byte("[('xkb', 'us'), ('xkb', 'ru')]\n"), nil
		}
		return []byte("[('xkb', 'ru'), ('xkb', 'us')]\n"), nil
	}
	t.Cleanup(func() { runGsettingsForGNOMEWayland = orig })
	return &calls
}

func TestManualConversionWithoutTracker(t *testing.T) {
	gnome := func(home string) *SessionEnv {
		return &SessionEnv{
			User: "u", Home: home, XDGConfigHome: home,
			SessionType: "wayland", WaylandDisplay: "wayland-0",
			XDGCurrentDesktop: "GNOME", XDGSessionDesktop: "gnome",
		}
	}
	kde := func(home string) *SessionEnv {
		return &SessionEnv{
			User: "u", Home: home, XDGConfigHome: home,
			SessionType: "wayland", WaylandDisplay: "wayland-0",
			XDGCurrentDesktop: "KDE", XDGSessionDesktop: "plasma",
		}
	}
	tests := []struct {
		name         string
		env          func(home string) *SessionEnv
		layoutSource string
		root         bool
		verify       bool
	}{
		{name: "layout-source=none", env: gnome, layoutSource: "none"},
		{name: "kde session", env: kde, layoutSource: "auto"},
		{name: "root mode", env: gnome, layoutSource: "auto", root: true},
		{name: "gnome wayland verify, layout-source=none", env: gnome, layoutSource: "none", verify: true},
		{name: "gnome wayland verify, root mode", env: gnome, layoutSource: "auto", root: true, verify: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origEuid := forkGeteuid
			forkGeteuid = func() int {
				if tt.root {
					return 0
				}
				return 1000
			}
			t.Cleanup(func() { forkGeteuid = origEuid })
			origSleep := emitSleep
			emitSleep = func(time.Duration) {}
			t.Cleanup(func() { emitSleep = origSleep })
			calls := forkTestStubGsettings(t)

			home := t.TempDir()
			dir := filepath.Join(home, userconfig.DirName)
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			conf := "auto-enabled=true\nlayout-source=" + tt.layoutSource + "\n"
			if err := os.WriteFile(filepath.Join(dir, userconfig.AutoConfName), []byte(conf), 0o600); err != nil {
				t.Fatal(err)
			}

			run := func(withFork bool) ([]KeyEvent, int) {
				*calls = 0
				s, out := forkTestSwitcher(t, &Config{LayoutSwitchKey: []uint16{KEY_LEFTALT, KEY_LEFTSHIFT}})
				s.sessionEnv = tt.env(home)
				s.verifyGNOMEWaylandLayoutSwitch = tt.verify
				forkTestWithLayouts(s)
				if withFork {
					s.initFork()
					if s.userDir != dir {
						t.Fatalf("userDir = %q, want %q", s.userDir, dir)
					}
					if !s.AutoEnabled() {
						t.Fatal("auto.conf was not read")
					}
					if s.tracker != nil || s.auto != nil || s.layoutReaderFn != nil || s.onOwnSwitch != nil {
						t.Fatalf("fork state without a tracker: tracker=%v auto=%v readerFn=%v onOwnSwitch=%v",
							s.tracker != nil, s.auto != nil, s.layoutReaderFn != nil, s.onOwnSwitch != nil)
					}
					if s.autoReason == "" {
						t.Fatal("autoReason empty without an engine")
					}
				}
				forkTestFeed(t, s, "a b c L L")
				return forkTestCanonical(forkTestCaptured(t, out)), *calls
			}
			wantEvents, wantCalls := run(false)
			gotEvents, gotCalls := run(true)
			if len(wantEvents) == 0 || forkTestCountCode(wantEvents, KEY_BACKSPACE) != 3 {
				t.Fatalf("upstream emitted %v, want a 3-letter conversion", wantEvents)
			}
			if !slices.Equal(gotEvents, wantEvents) {
				t.Fatalf("emitted %v, upstream %v", gotEvents, wantEvents)
			}
			if gotCalls != wantCalls {
				t.Fatalf("gsettings reads = %d, upstream %d", gotCalls, wantCalls)
			}
			if tt.verify && wantCalls == 0 {
				t.Fatal("verify path did not read gsettings")
			}
		})
	}
}

func TestAfterInputDiscardedTaintsCurrentWord(t *testing.T) {
	stubKeys := func(t *testing.T, down map[uint16]bool, err error) {
		t.Helper()
		orig := forkKeysDown
		forkKeysDown = func(*InputReader) (map[uint16]bool, error) { return down, err }
		t.Cleanup(func() { forkKeysDown = orig })
	}

	t.Run("direct call", func(t *testing.T) {
		s, _ := forkTestSwitcher(t, &Config{LayoutSwitchKey: []uint16{125}})
		env := forkTestFakeEngine(t, s)
		stubKeys(t, map[uint16]bool{}, nil)

		forkTestFeed(t, s, "g h")
		s.afterInputDiscarded()
		forkTestFeed(t, s, "b d t n SP")
		if st := s.auto.Stats(); st.Armed != 0 || st.LastReason != "tainted:discarded" {
			t.Fatalf("word after discarded input: %+v, want tainted:discarded", st)
		}
		if env.judge.calls != 0 {
			t.Fatal("detector consulted for a tainted word")
		}
		forkTestFeed(t, s, "h e l l o SP")
		if st := s.auto.Stats(); st.Armed != 1 {
			t.Fatalf("next word not evaluated normally: %+v", st)
		}
	})

	t.Run("after the flush of a manual conversion", func(t *testing.T) {
		s, out := forkTestSwitcher(t, &Config{LayoutSwitchKey: []uint16{125}})
		forkTestFakeEngine(t, s)
		forkTestFeed(t, s, "x SP g h L L") // manual conversion of "gh", then Flush
		if len(forkTestCaptured(t, out)) == 0 {
			t.Fatal("manual conversion did not run")
		}
		forkTestFeed(t, s, "b d t n SP")
		if st := s.auto.Stats(); st.Armed != 0 || st.LastReason != "tainted:discarded" {
			t.Fatalf("word continued after a manual conversion: %+v, want tainted:discarded", st)
		}
	})

	t.Run("key state resync", func(t *testing.T) {
		s, _ := forkTestSwitcher(t, &Config{LayoutSwitchKey: []uint16{125}})
		forkTestDefaultChords(s)
		forkTestFakeEngine(t, s)

		forkTestFeed(t, s, "L+")
		stubKeys(t, map[uint16]bool{KEY_LEFTSHIFT: true, KEY_RIGHTALT: true}, nil)
		s.afterInputDiscarded()
		if got := s.chords.HeldCount(); got != 2 {
			t.Fatalf("chords held after resync = %d, want the 2 kernel keys", got)
		}
		if !s.auto.held[KEY_LEFTSHIFT] || !s.auto.held[KEY_RIGHTALT] || s.forkAltDown != 2 {
			t.Fatalf("engine held set %v, alt bits %b", s.auto.held, s.forkAltDown)
		}

		stubKeys(t, nil, errors.New("EVIOCGKEY failed"))
		s.afterInputDiscarded()
		if got := s.chords.HeldCount(); got != 0 {
			t.Fatalf("chords held after a failed key query = %d, want Reset", got)
		}
		if len(s.auto.held) != 0 || s.forkAltDown != 0 {
			t.Fatalf("engine held set after a failed key query = %v", s.auto.held)
		}
	})
}

func TestFirstWordAfterBufKillerNotSkipped(t *testing.T) {
	s, _ := forkTestSwitcher(t, &Config{LayoutSwitchKey: []uint16{125}})
	env := forkTestFakeEngine(t, s)

	forkTestFeed(t, s, "g h LALT+ TAB LALT-") // Alt+Tab: the word is abandoned
	env.focusSeq++
	forkTestFeed(t, s, "g h b d t n SP")
	if st := s.auto.Stats(); st.Armed != 1 {
		t.Fatalf("first word after a BufKiller and focus change: %+v, want armed", st)
	}

	// Without a boundary the focus change is detected.
	s2, _ := forkTestSwitcher(t, &Config{LayoutSwitchKey: []uint16{125}})
	env2 := forkTestFakeEngine(t, s2)
	forkTestFeed(t, s2, "g h")
	env2.focusSeq++
	forkTestFeed(t, s2, "b d t n SP")
	if st := s2.auto.Stats(); st.Armed != 0 || st.LastReason != "focus-changed" {
		t.Fatalf("focus change inside a word: %+v, want focus-changed", st)
	}
}
