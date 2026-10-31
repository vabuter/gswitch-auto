package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arumata/gswitch/internal/gswitch/layoutstate"
	"github.com/arumata/gswitch/internal/gswitch/userconfig"
)

// gswitch-auto WPX7 tests of the platform source selection (SPEC-X11 §2,
// §3.4). Helpers carry the platformTest prefix. No test touches the session
// bus or an X display: the probe is a fake and nothing is started.

// Session environments of the matrix.
func platformTestWaylandGNOME() *SessionEnv {
	return &SessionEnv{SessionType: "wayland", WaylandDisplay: "wayland-0",
		XDGCurrentDesktop: "GNOME", XDGSessionDesktop: "gnome"}
}

func platformTestX11GNOME() *SessionEnv {
	return &SessionEnv{SessionType: "x11", Display: ":4242",
		XDGCurrentDesktop: "GNOME", XDGSessionDesktop: "gnome"}
}

func platformTestX11KDE() *SessionEnv {
	return &SessionEnv{SessionType: "x11", Display: ":4242",
		XDGCurrentDesktop: "KDE", XDGSessionDesktop: "plasma"}
}

func platformTestWaylandKDE() *SessionEnv {
	return &SessionEnv{SessionType: "wayland", WaylandDisplay: "wayland-0",
		XDGCurrentDesktop: "KDE", XDGSessionDesktop: "plasma"}
}

// platformTestUnknownGNOME has neither a session type nor a display: a user
// service whose systemd environment lacks XDG_SESSION_TYPE (K-X3).
func platformTestUnknownGNOME() *SessionEnv {
	return &SessionEnv{XDGCurrentDesktop: "GNOME", XDGSessionDesktop: "gnome"}
}

var (
	platformTestShell50  = layoutstate.ShellInfo{Version: "50.6"}
	platformTestShell43  = layoutstate.ShellInfo{Version: "43.9"}
	platformTestShellErr = layoutstate.ShellInfo{Err: layoutstate.ErrShellUnavailable}
)

// platformTestProbe returns info and counts its calls.
func platformTestProbe(info layoutstate.ShellInfo, calls *int) func(context.Context) layoutstate.ShellInfo {
	return func(context.Context) layoutstate.ShellInfo {
		*calls++
		return info
	}
}

func platformTestSettings(layoutSource, focusSource string) userconfig.Settings {
	st := userconfig.DefaultSettings()
	st.LayoutSource = layoutSource
	st.FocusSource = focusSource
	return st
}

// platformTestSetProcessEnv makes the process environment look like the
// given session for selections with a nil SessionEnv.
func platformTestSetProcessEnv(t *testing.T, sessionType, waylandDisplay, display, desktop string) {
	t.Helper()
	t.Setenv("XDG_SESSION_TYPE", sessionType)
	t.Setenv("WAYLAND_DISPLAY", waylandDisplay)
	t.Setenv("DISPLAY", display)
	t.Setenv("XDG_CURRENT_DESKTOP", desktop)
	t.Setenv("XDG_SESSION_DESKTOP", strings.ToLower(desktop))
	t.Setenv("DESKTOP_SESSION", strings.ToLower(desktop))
}

type platformTestWant struct {
	x11Session, wayland, gnome bool
	ibus, guarded, x11         bool
	reason                     string
	focusExt, focusX11         bool
	warning                    string // substring of exactly one warning; "" ⇒ no warnings
	note                       string // substring of LayoutNote; "" ⇒ empty note
	probed                     bool
}

func TestSelectPlatformMatrix(t *testing.T) {
	tests := []struct {
		name         string
		env          *SessionEnv
		layoutSource string
		focusSource  string
		euid         int
		probe        layoutstate.ShellInfo
		want         platformTestWant
	}{
		{name: "wayland gnome auto 50.6", env: platformTestWaylandGNOME(), layoutSource: "auto", focusSource: "auto", euid: 1000, probe: platformTestShell50,
			want: platformTestWant{wayland: true, gnome: true, ibus: true, focusExt: true, probed: true}},
		{name: "wayland gnome auto 43.9", env: platformTestWaylandGNOME(), layoutSource: "auto", focusSource: "auto", euid: 1000, probe: platformTestShell43,
			want: platformTestWant{wayland: true, gnome: true, reason: autoUnavailableLayoutSourceUnsupported, focusExt: true, probed: true,
				note: "IBus layout source disabled: GNOME Shell 43.9 reports a fixed engine for XKB sources (needs 49.3+)"}},
		{name: "wayland gnome auto probe failed", env: platformTestWaylandGNOME(), layoutSource: "auto", focusSource: "auto", euid: 1000, probe: platformTestShellErr,
			want: platformTestWant{wayland: true, gnome: true, ibus: true, guarded: true, focusExt: true, probed: true,
				note: "IBus layout source deferred: GNOME Shell version unknown"}},
		{name: "wayland gnome ibus 43.9", env: platformTestWaylandGNOME(), layoutSource: "ibus", focusSource: "auto", euid: 1000, probe: platformTestShell43,
			want: platformTestWant{wayland: true, gnome: true, ibus: true, focusExt: true, probed: true,
				warning: "layout-source=ibus forces the IBus source although GNOME Shell 43.9 reports a fixed engine below 49.3"}},
		{name: "x11 gnome auto", env: platformTestX11GNOME(), layoutSource: "auto", focusSource: "auto", euid: 1000, probe: platformTestShell43,
			want: platformTestWant{x11Session: true, gnome: true, x11: true, focusExt: true, focusX11: true, probed: true}},
		{name: "x11 gnome x11", env: platformTestX11GNOME(), layoutSource: "x11", focusSource: "auto", euid: 1000, probe: platformTestShell43,
			want: platformTestWant{x11Session: true, gnome: true, x11: true, focusExt: true, focusX11: true}},
		{name: "x11 gnome ibus 50.6", env: platformTestX11GNOME(), layoutSource: "ibus", focusSource: "auto", euid: 1000, probe: platformTestShell50,
			want: platformTestWant{x11Session: true, gnome: true, ibus: true, focusExt: true, focusX11: true, probed: true}},
		// Rev. 3: refused below 49.3 in an X11 session (the Debian machine);
		// the refusal replaces IBusSourceAllowed's forced-source warning.
		{name: "x11 gnome ibus 43.9 refused", env: platformTestX11GNOME(), layoutSource: "ibus", focusSource: "auto", euid: 1000, probe: platformTestShell43,
			want: platformTestWant{x11Session: true, gnome: true, reason: autoUnavailableLayoutSourceUnsupported, focusExt: true, focusX11: true, probed: true,
				warning: "layout-source=ibus is refused in an X11 session: GNOME Shell 43.9 reports the fixed engine xkb:us::eng below 49.3; use layout-source=auto or x11"}},
		{name: "x11 gnome ibus probe failed", env: platformTestX11GNOME(), layoutSource: "ibus", focusSource: "auto", euid: 1000, probe: platformTestShellErr,
			want: platformTestWant{x11Session: true, gnome: true, ibus: true, focusExt: true, focusX11: true, probed: true,
				warning: "layout-source=ibus forces the IBus source; GNOME Shell version unknown"}},
		{name: "x11 kde ibus 43.9 refused", env: platformTestX11KDE(), layoutSource: "ibus", focusSource: "auto", euid: 1000, probe: platformTestShell43,
			want: platformTestWant{x11Session: true, reason: autoUnavailableLayoutSourceUnsupported, focusX11: true, probed: true,
				warning: "layout-source=ibus is refused in an X11 session"}},
		{name: "wayland gnome ibus 50.6", env: platformTestWaylandGNOME(), layoutSource: "ibus", focusSource: "auto", euid: 1000, probe: platformTestShell50,
			want: platformTestWant{wayland: true, gnome: true, ibus: true, focusExt: true, probed: true}},
		{name: "wayland gnome ibus probe failed", env: platformTestWaylandGNOME(), layoutSource: "ibus", focusSource: "auto", euid: 1000, probe: platformTestShellErr,
			want: platformTestWant{wayland: true, gnome: true, ibus: true, focusExt: true, probed: true,
				warning: "layout-source=ibus forces the IBus source; GNOME Shell version unknown"}},
		{name: "unknown session gnome ibus 43.9", env: platformTestUnknownGNOME(), layoutSource: "ibus", focusSource: "auto", euid: 1000, probe: platformTestShell43,
			want: platformTestWant{gnome: true, ibus: true, focusExt: true, probed: true,
				warning: "layout-source=ibus forces the IBus source although GNOME Shell 43.9 reports a fixed engine below 49.3"}},
		{name: "x11 kde auto", env: platformTestX11KDE(), layoutSource: "auto", focusSource: "auto", euid: 1000, probe: platformTestShell50,
			want: platformTestWant{x11Session: true, reason: autoUnavailableLayoutSourceUnsupported, focusX11: true,
				note: "no layout source for this session"}},
		{name: "x11 kde x11 opt-in", env: platformTestX11KDE(), layoutSource: "x11", focusSource: "auto", euid: 1000, probe: platformTestShell50,
			want: platformTestWant{x11Session: true, x11: true, focusX11: true}},
		{name: "wayland kde auto", env: platformTestWaylandKDE(), layoutSource: "auto", focusSource: "auto", euid: 1000, probe: platformTestShell50,
			want: platformTestWant{wayland: true, reason: autoUnavailableLayoutSourceUnsupported, focusExt: true,
				note: "no layout source for this session"}},
		{name: "wayland gnome x11 refused", env: platformTestWaylandGNOME(), layoutSource: "x11", focusSource: "auto", euid: 1000, probe: platformTestShell50,
			want: platformTestWant{wayland: true, gnome: true, reason: autoUnavailableLayoutSourceUnsupported, focusExt: true,
				warning: "layout-source=x11 needs an X11 session"}},
		{name: "x11 gnome x11 without display", env: &SessionEnv{SessionType: "x11", XDGCurrentDesktop: "GNOME"}, layoutSource: "x11", focusSource: "auto", euid: 1000, probe: platformTestShell50,
			want: platformTestWant{x11Session: true, gnome: true, reason: autoUnavailableLayoutSourceUnsupported, focusExt: true, focusX11: true,
				warning: "layout-source=x11 needs an X display"}},
		{name: "layout-source none", env: platformTestX11GNOME(), layoutSource: "none", focusSource: "auto", euid: 1000, probe: platformTestShell50,
			want: platformTestWant{x11Session: true, gnome: true, reason: autoUnavailableLayoutSourceOf, focusExt: true, focusX11: true}},
		{name: "focus-source off", env: platformTestX11GNOME(), layoutSource: "auto", focusSource: "off", euid: 1000, probe: platformTestShell43,
			want: platformTestWant{x11Session: true, gnome: true, x11: true, probed: true}},
		{name: "root mode", env: platformTestX11GNOME(), layoutSource: "auto", focusSource: "auto", euid: 0, probe: platformTestShell43,
			want: platformTestWant{x11Session: true, gnome: true, reason: autoUnavailableRootMode}},
		{name: "unknown session gnome auto 50.6", env: platformTestUnknownGNOME(), layoutSource: "auto", focusSource: "auto", euid: 1000, probe: platformTestShell50,
			want: platformTestWant{gnome: true, ibus: true, focusExt: true, probed: true}},
		{name: "unknown session gnome x11 refused", env: platformTestUnknownGNOME(), layoutSource: "x11", focusSource: "auto", euid: 1000, probe: platformTestShell50,
			want: platformTestWant{gnome: true, reason: autoUnavailableLayoutSourceUnsupported, focusExt: true,
				warning: "layout-source=x11 needs an X display"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			sel := selectPlatform(platformTestSettings(tt.layoutSource, tt.focusSource), tt.env, tt.euid,
				platformTestProbe(tt.probe, &calls))
			platformTestCheck(t, sel, tt.want, calls)
		})
	}
}

func platformTestCheck(t *testing.T, sel PlatformSelection, want platformTestWant, probeCalls int) {
	t.Helper()
	got := platformTestWant{
		x11Session: sel.X11Session, wayland: sel.Wayland, gnome: sel.GNOME,
		ibus: sel.LayoutIBus, guarded: sel.LayoutIBusGuarded, x11: sel.LayoutX11,
		reason: sel.LayoutReason, focusExt: sel.FocusExt, focusX11: sel.FocusX11,
		probed: probeCalls > 0,
	}
	cmp := want
	cmp.warning, cmp.note = "", ""
	if got != cmp {
		t.Fatalf("selection = %+v, want %+v (sel %+v)", got, cmp, sel)
	}
	if probeCalls > 1 {
		t.Fatalf("probe called %d times, want at most once", probeCalls)
	}
	switch {
	case want.warning == "" && len(sel.Warnings) != 0:
		t.Fatalf("unexpected warnings %q", sel.Warnings)
	case want.warning != "" && (len(sel.Warnings) != 1 || !strings.Contains(sel.Warnings[0], want.warning)):
		t.Fatalf("warnings = %q, want one containing %q", sel.Warnings, want.warning)
	}
	switch {
	case want.note == "" && sel.LayoutNote != "":
		t.Fatalf("unexpected layout note %q", sel.LayoutNote)
	case want.note != "" && !strings.Contains(sel.LayoutNote, want.note):
		t.Fatalf("layout note = %q, want it to contain %q", sel.LayoutNote, want.note)
	}
	if sel.LayoutIBus && sel.LayoutX11 {
		t.Fatal("two push sources selected")
	}
	if (sel.LayoutIBus || sel.LayoutX11) != (sel.LayoutReason == "") {
		t.Fatalf("sources %v/%v with reason %q", sel.LayoutIBus, sel.LayoutX11, sel.LayoutReason)
	}
}

func TestSelectPlatformProcessEnvironment(t *testing.T) {
	orig := forkGeteuid
	forkGeteuid = func() int { return 1000 }
	t.Cleanup(func() { forkGeteuid = orig })

	t.Run("x11 gnome", func(t *testing.T) {
		platformTestSetProcessEnv(t, "x11", "", ":4242", "GNOME")
		calls := 0
		sel := selectPlatform(platformTestSettings("auto", "auto"), nil, forkGeteuid(), platformTestProbe(platformTestShell43, &calls))
		platformTestCheck(t, sel, platformTestWant{x11Session: true, gnome: true, x11: true, focusExt: true, focusX11: true, probed: true}, calls)
	})
	t.Run("wayland gnome with xwayland display", func(t *testing.T) {
		// The Fedora host: WAYLAND_DISPLAY wins over DISPLAY (Xwayland never
		// sees GNOME's layout changes, F16).
		platformTestSetProcessEnv(t, "wayland", "wayland-0", ":0", "GNOME")
		calls := 0
		sel := selectPlatform(platformTestSettings("auto", "auto"), nil, forkGeteuid(), platformTestProbe(platformTestShell50, &calls))
		platformTestCheck(t, sel, platformTestWant{wayland: true, gnome: true, ibus: true, focusExt: true, probed: true}, calls)
	})
	t.Run("display alone is not an x11 session", func(t *testing.T) {
		// K-X3: a missing XDG_SESSION_TYPE never selects the X11 sources.
		platformTestSetProcessEnv(t, "", "", ":4242", "GNOME")
		calls := 0
		sel := selectPlatform(platformTestSettings("auto", "auto"), nil, forkGeteuid(), platformTestProbe(platformTestShell43, &calls))
		platformTestCheck(t, sel, platformTestWant{gnome: true, reason: autoUnavailableLayoutSourceUnsupported, focusExt: true, probed: true,
			note: "IBus layout source disabled"}, calls)
	})
}

// TestInitTrackerNeverTwoPushSources sweeps every environment, layout-source
// and probe answer: the IBus and X11 sources are never selected together
// (Tracker.setLocked treats a different Source as a change).
func TestInitTrackerNeverTwoPushSources(t *testing.T) {
	envs := map[string]*SessionEnv{
		"wayland gnome": platformTestWaylandGNOME(), "x11 gnome": platformTestX11GNOME(),
		"x11 kde": platformTestX11KDE(), "wayland kde": platformTestWaylandKDE(),
		"unknown gnome": platformTestUnknownGNOME(),
	}
	probes := map[string]layoutstate.ShellInfo{"50.6": platformTestShell50, "43.9": platformTestShell43, "err": platformTestShellErr}
	for envName, env := range envs {
		for _, source := range []string{"auto", "ibus", "x11", "none"} {
			for probeName, info := range probes {
				for _, euid := range []int{0, 1000} {
					calls := 0
					sel := selectPlatform(platformTestSettings(source, "auto"), env, euid, platformTestProbe(info, &calls))
					if sel.LayoutIBus && sel.LayoutX11 {
						t.Errorf("%s layout-source=%s probe %s euid %d: both sources selected", envName, source, probeName, euid)
					}
					if euid == 0 && (sel.LayoutIBus || sel.LayoutX11 || sel.FocusExt || sel.FocusX11 || calls != 0) {
						t.Errorf("%s layout-source=%s probe %s: root selected a source or probed (%+v)", envName, source, probeName, sel)
					}
				}
			}
		}
	}
}

func TestPlatformSummary(t *testing.T) {
	calls := 0
	sel := selectPlatform(platformTestSettings("auto", "auto"), platformTestX11GNOME(), 1000, platformTestProbe(platformTestShell43, &calls))
	if got, want := sel.Summary(true), "layout sources: x11 (GNOME Shell 43.9); focus sources: extension, x11; strict purpose: true"; got != want {
		t.Fatalf("Summary() = %q, want %q", got, want)
	}
	if got := sel.SessionName(); got != "x11" {
		t.Fatalf("SessionName() = %q, want x11", got)
	}
	sel = selectPlatform(platformTestSettings("auto", "off"), platformTestWaylandGNOME(), 1000, platformTestProbe(platformTestShellErr, &calls))
	if got, want := sel.Summary(false), "layout sources: ibus (guarded); focus sources: none; strict purpose: false"; got != want {
		t.Fatalf("Summary() = %q, want %q", got, want)
	}
	if got := sel.SessionName(); got != "wayland" {
		t.Fatalf("SessionName() = %q, want wayland", got)
	}
	sel = selectPlatform(platformTestSettings("none", "off"), platformTestUnknownGNOME(), 1000, platformTestProbe(platformTestShell50, &calls))
	if got, want := sel.Summary(true), "layout sources: none (layout-source-off); focus sources: none; strict purpose: true"; got != want {
		t.Fatalf("Summary() = %q, want %q", got, want)
	}
	if got := sel.SessionName(); got != "unknown" {
		t.Fatalf("SessionName() = %q, want unknown", got)
	}
}

func TestPlatformForDiagnostics(t *testing.T) {
	orig := forkGeteuid
	forkGeteuid = func() int { return 1000 }
	t.Cleanup(func() { forkGeteuid = orig })
	platformTestSetProcessEnv(t, "x11", "", ":4242", "GNOME")
	base := t.TempDir()
	dir := filepath.Join(base, userconfig.DirName)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, userconfig.AutoConfName),
		[]byte("layout-source=x11\nauto-strict-purpose=false\nauto-min-letters=99\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", base)

	sel, err := PlatformForDiagnostics(context.Background())
	if err != nil {
		t.Fatalf("PlatformForDiagnostics() error = %v", err)
	}
	if !sel.LayoutX11 || sel.LayoutIBus || !sel.FocusX11 || !sel.FocusExt || !sel.X11Session {
		t.Fatalf("selection = %+v, want the X11 sources of an X11 GNOME session", sel)
	}
	// The loader's warning for the invalid value is carried along.
	found := false
	for _, w := range sel.Warnings {
		if strings.Contains(w, "auto-min-letters") {
			found = true
		}
	}
	if !found {
		t.Fatalf("Warnings = %q, want the auto-min-letters loader warning", sel.Warnings)
	}
	policy, err := FocusPolicyForDiagnostics(sel.Wayland)
	if err != nil {
		t.Fatalf("FocusPolicyForDiagnostics() error = %v", err)
	}
	if policy.RequirePurpose {
		t.Fatal("RequirePurpose = true with auto-strict-purpose=false")
	}
	if policy, _ := FocusPolicyForDiagnostics(false); policy.RequirePurpose {
		t.Fatal("explicit false must stay false whatever the session")
	}

	// Without a configuration directory the defaults are selected.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "missing"))
	sel, _ = PlatformForDiagnostics(context.Background())
	if !sel.LayoutX11 || sel.LayoutIBus || !sel.FocusExt || !sel.FocusX11 {
		t.Fatalf("selection with defaults = %+v, want the X11 sources", sel)
	}
	if policy, _ := FocusPolicyForDiagnostics(sel.Wayland); !policy.RequirePurpose {
		t.Fatal("default auto-strict-purpose=auto is not strict on X11")
	}
}

func TestPlatformForDiagnosticsProbeBoundedByContext(t *testing.T) {
	orig := forkGeteuid
	forkGeteuid = func() int { return 1000 }
	t.Cleanup(func() { forkGeteuid = orig })
	origProbe := forkProbeShellVersion
	forkProbeShellVersion = func(ctx context.Context) layoutstate.ShellInfo {
		<-ctx.Done()
		return layoutstate.ShellInfo{Err: ctx.Err()}
	}
	t.Cleanup(func() { forkProbeShellVersion = origProbe })
	platformTestSetProcessEnv(t, "wayland", "wayland-0", "", "GNOME")
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	sel, _ := PlatformForDiagnostics(ctx)
	if time.Since(start) > forkShellProbeTimeout/2 {
		t.Fatalf("PlatformForDiagnostics() took %v with a cancelled context", time.Since(start))
	}
	if !errors.Is(sel.Shell.Err, context.Canceled) || !sel.LayoutIBusGuarded {
		t.Fatalf("selection = %+v, want a guarded IBus source after the cancelled probe", sel)
	}
}
