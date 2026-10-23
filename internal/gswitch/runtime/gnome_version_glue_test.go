package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/arumata/gswitch/internal/gswitch/layoutstate"
	"github.com/arumata/gswitch/internal/gswitch/userconfig"
)

// gvTestShellInfo is what forkProbeShellVersion returns in this package's
// tests. The default is the Fedora host's answer, so every existing test that
// builds a tracker in a GNOME session keeps the unguarded IBus source and no
// test ever queries the live session bus (the real probe would wait up to 2 s
// and ask the running gnome-shell).
var gvTestShellInfo = layoutstate.ShellInfo{Version: "50.6"}

func gvTestProbe(context.Context) layoutstate.ShellInfo { return gvTestShellInfo }

func init() { forkProbeShellVersion = gvTestProbe }

// gvTestSetShellInfo installs a probe answer for the test's duration.
func gvTestSetShellInfo(t *testing.T, info layoutstate.ShellInfo) {
	t.Helper()
	orig := gvTestShellInfo
	gvTestShellInfo = info
	t.Cleanup(func() { gvTestShellInfo = orig })
}

// gvTestSwitcher builds a debug Switcher (log to stdout) in a GNOME Wayland
// session with the us/ru pair and a hermetic IBus address environment, so the
// IBus source can never reach a real ibus-daemon. Tests build the tracker
// with gvTestInitTracker (selection + initTracker, as initFork does).
func gvTestSwitcher(t *testing.T) *Switcher {
	t.Helper()
	origEuid := forkGeteuid
	forkGeteuid = func() int { return 1000 }
	t.Cleanup(func() { forkGeteuid = origEuid })
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("IBUS_ADDRESS", "")
	t.Setenv("IBUS_ADDRESS_FILE", "")

	s, _ := forkTestSwitcher(t, &Config{})
	s.debug = true
	s.sessionEnv = &SessionEnv{
		User: "u", Home: home, XDGConfigHome: home,
		SessionType: "wayland", WaylandDisplay: "wayland-0",
		XDGCurrentDesktop: "GNOME", XDGSessionDesktop: "gnome",
	}
	forkTestWithLayouts(s)
	return s
}

func gvTestSettings(layoutSource string) userconfig.Settings {
	st := userconfig.DefaultSettings()
	st.LayoutSource = layoutSource
	return st
}

// gvTestInitTracker runs the two initFork steps that build the tracker: the
// platform selection (which probes and logs) and initTracker.
func gvTestInitTracker(s *Switcher, st userconfig.Settings) {
	s.initPlatform(st)
	s.initTracker(st)
}

// gvTestResyncErr runs a tracker resync whose every leg fails: the only push
// source is the IBus one (no address file) and the gsettings resyncer needs
// root for a non-nil session env. The joined error therefore shows which
// source the tracker holds.
func gvTestResyncErr(t *testing.T, s *Switcher) error {
	t.Helper()
	if s.tracker == nil {
		t.Fatal("tracker is nil")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := s.tracker.Resync(ctx)
	if err == nil {
		t.Fatal("Resync() succeeded, want every leg to fail")
	}
	if st := s.tracker.State(); st.Confidence != layoutstate.Unknown {
		t.Fatalf("tracker state after failed resync = %s, want unknown", st)
	}
	return err
}

func TestInitTrackerShellBelowThresholdDisablesIBusSource(t *testing.T) {
	for _, version := range []string{"43.9", "49.2", "49.rc"} {
		t.Run(version, func(t *testing.T) {
			gvTestSetShellInfo(t, layoutstate.ShellInfo{Version: version})
			s := gvTestSwitcher(t)
			out := captureStdout(t, func() { gvTestInitTracker(s, gvTestSettings("auto")) })
			if s.tracker != nil {
				t.Fatal("tracker built although GNOME Shell reports a fixed IBus engine")
			}
			want := "IBus layout source disabled: GNOME Shell " + version + " reports a fixed engine for XKB sources (needs 49.3+)"
			if !strings.Contains(out, want) {
				t.Fatalf("log lacks %q:\n%s", want, out)
			}
			if strings.Contains(out, "Warning:") {
				t.Fatalf("unexpected warning with layout-source=auto:\n%s", out)
			}
		})
	}
}

func TestInitTrackerProbeErrorGuardsIBusSource(t *testing.T) {
	gvTestSetShellInfo(t, layoutstate.ShellInfo{Err: layoutstate.ErrShellUnavailable})
	s := gvTestSwitcher(t)
	out := captureStdout(t, func() { gvTestInitTracker(s, gvTestSettings("auto")) })
	if s.tracker == nil {
		t.Fatal("tracker nil although the probe only failed (should be guarded)")
	}
	if !strings.Contains(out, "IBus layout source deferred: GNOME Shell version unknown") {
		t.Fatalf("log lacks the deferred line:\n%s", out)
	}
	if strings.Contains(out, "IBus layout source disabled") {
		t.Fatalf("probe error logged as a refusal:\n%s", out)
	}
	// Never started: the guard is pending, its Query refuses on behalf of the
	// IBus source and the tracker falls through to the resyncer.
	err := gvTestResyncErr(t, s)
	if !errors.Is(err, layoutstate.ErrShellUnavailable) || !strings.Contains(err.Error(), "waiting for the GNOME Shell version") {
		t.Fatalf("Resync() = %v, want the pending guard's error", err)
	}
	if errors.Is(err, layoutstate.ErrIBusNotRunning) {
		t.Fatalf("Resync() = %v: the IBus source ran unguarded", err)
	}
}

func TestInitTrackerShellTrackingKeepsIBusSource(t *testing.T) {
	// The seam's default (50.6) is in force.
	s := gvTestSwitcher(t)
	out := captureStdout(t, func() { gvTestInitTracker(s, gvTestSettings("auto")) })
	if s.tracker == nil {
		t.Fatal("tracker nil with GNOME Shell 50.6")
	}
	for _, unwanted := range []string{"IBus layout source disabled", "IBus layout source deferred", "Warning:"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("log contains %q with GNOME Shell 50.6:\n%s", unwanted, out)
		}
	}
	err := gvTestResyncErr(t, s)
	if errors.Is(err, layoutstate.ErrShellUnavailable) {
		t.Fatalf("Resync() = %v: the IBus source is guarded although the probe passed", err)
	}
	if !errors.Is(err, layoutstate.ErrIBusNotRunning) {
		t.Fatalf("Resync() = %v, want the unguarded IBus source's address error", err)
	}
}

func TestInitTrackerForcedIBusSourceWarns(t *testing.T) {
	tests := []struct {
		name string
		info layoutstate.ShellInfo
		want string
	}{
		{name: "static engine", info: layoutstate.ShellInfo{Version: "43.9"},
			want: "Warning: layout-source=ibus forces the IBus source; GNOME Shell 43.9 reports a fixed engine below 49.3"},
		{name: "probe error", info: layoutstate.ShellInfo{Err: layoutstate.ErrShellUnavailable},
			want: "Warning: layout-source=ibus forces the IBus source; GNOME Shell version unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gvTestSetShellInfo(t, tt.info)
			s := gvTestSwitcher(t)
			out := captureStdout(t, func() { gvTestInitTracker(s, gvTestSettings("ibus")) })
			if s.tracker == nil {
				t.Fatal("tracker nil although layout-source=ibus forces the source")
			}
			if !strings.Contains(out, tt.want) {
				t.Fatalf("log lacks %q:\n%s", tt.want, out)
			}
			// Forced means unguarded: the IBus source itself answers.
			err := gvTestResyncErr(t, s)
			if errors.Is(err, layoutstate.ErrShellUnavailable) || !errors.Is(err, layoutstate.ErrIBusNotRunning) {
				t.Fatalf("Resync() = %v, want the unguarded IBus source's address error", err)
			}
		})
	}
}

func TestInitTrackerProbeSkippedWhenSourceDisabled(t *testing.T) {
	// layout-source=none never probes (and the probe seam would notice).
	gvTestSetShellInfo(t, layoutstate.ShellInfo{Err: errors.New("probe must not run")})
	orig := forkProbeShellVersion
	calls := 0
	forkProbeShellVersion = func(ctx context.Context) layoutstate.ShellInfo {
		calls++
		return gvTestProbe(ctx)
	}
	t.Cleanup(func() { forkProbeShellVersion = orig })
	s := gvTestSwitcher(t)
	captureStdout(t, func() { gvTestInitTracker(s, gvTestSettings("none")) })
	if s.tracker != nil || calls != 0 {
		t.Fatalf("tracker=%v probes=%d with layout-source=none, want nil and 0", s.tracker != nil, calls)
	}
}
