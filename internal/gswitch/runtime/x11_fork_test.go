package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arumata/gswitch/internal/gswitch/focus"
	"github.com/arumata/gswitch/internal/gswitch/layoutstate"
	"github.com/arumata/gswitch/internal/gswitch/userconfig"
)

// gswitch-auto WPX7 tests of the X11 integration (SPEC-X11 §3.4, §5 WPX7).
// Nothing here connects to an X display: the display of every session
// environment is a number no server listens on, the sources are built but
// never started, and a Resync against them fails at once (which is how the
// tests see which sources a tracker holds).

const platformTestDisplay = ":4242"

// platformTestInitFork builds a switcher with the fixture pair, the given
// session environment (its Home points at a temporary directory holding
// auto.conf with conf) and runs initFork. The probe seam answers probe.
func platformTestInitFork(t *testing.T, env *SessionEnv, conf string, probe layoutstate.ShellInfo) *Switcher {
	t.Helper()
	orig := forkGeteuid
	forkGeteuid = func() int { return 1000 }
	t.Cleanup(func() { forkGeteuid = orig })
	gvTestSetShellInfo(t, probe)
	t.Setenv("IBUS_ADDRESS", "")
	t.Setenv("IBUS_ADDRESS_FILE", "")

	home := t.TempDir()
	dir := filepath.Join(home, userconfig.DirName)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, userconfig.AutoConfName), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	s, _ := forkTestSwitcher(t, &Config{})
	env.User, env.Home, env.XDGConfigHome = "u", home, home
	s.sessionEnv = env
	forkTestWithLayouts(s)
	s.initFork()
	t.Cleanup(s.stopFork)
	return s
}

// platformTestResyncErr runs a tracker resync whose every leg fails (no X
// server on the display, no IBus address file, gsettings needs root with a
// session env) and returns the joined error naming the legs.
func platformTestResyncErr(t *testing.T, s *Switcher) string {
	t.Helper()
	if s.tracker == nil {
		t.Fatal("tracker is nil")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := s.tracker.Resync(ctx)
	if err == nil {
		t.Fatal("Resync() succeeded, want every leg to fail")
	}
	return err.Error()
}

func TestInitForkPlatformBeforePolicy(t *testing.T) {
	tests := []struct {
		name       string
		env        *SessionEnv
		x11Session bool
		strict     bool
	}{
		{name: "x11 gnome", env: platformTestX11GNOME(), x11Session: true, strict: true},
		{name: "wayland gnome", env: platformTestWaylandGNOME(), strict: false},
		{name: "unknown session gnome", env: platformTestUnknownGNOME(), strict: true}, // fail-closed
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := platformTestInitFork(t, tt.env, "", platformTestShell50)
			if s.platform.X11Session != tt.x11Session {
				t.Fatalf("platform.X11Session = %v, want %v", s.platform.X11Session, tt.x11Session)
			}
			p := s.focusPolicy.Load()
			if p == nil || p.RequirePurpose != tt.strict {
				t.Fatalf("policy = %+v, want RequirePurpose %v", p, tt.strict)
			}
			ok, reason := forkFocusDecision(focus.State{}, p)
			if tt.strict && (ok || reason != focus.ReasonFocusUnavailable) {
				t.Fatalf("strict decision without focus = %v %q, want denied focus-unavailable", ok, reason)
			}
			if !tt.strict && !ok {
				t.Fatalf("non-strict decision without focus = %v %q, want allowed", ok, reason)
			}
		})
	}
}

func TestInitTrackerX11Session(t *testing.T) {
	t.Run("x11 gnome auto", func(t *testing.T) {
		s := platformTestInitFork(t, platformTestX11GNOME(), "", platformTestShell43)
		if !s.platform.LayoutX11 || s.platform.LayoutIBus {
			t.Fatalf("platform = %+v, want the X11 source only", s.platform)
		}
		msg := platformTestResyncErr(t, s)
		if !strings.Contains(msg, "x11 query:") || strings.Contains(msg, "ibus query:") {
			t.Fatalf("Resync() = %q, want exactly the x11 source leg", msg)
		}
		if !strings.Contains(msg, "resync:") {
			t.Fatalf("Resync() = %q, want the gsettings resync leg on GNOME", msg)
		}
		if s.layoutReaderFn == nil || s.onOwnSwitch == nil {
			t.Fatal("tracker hooks not installed")
		}
		if s.focusMerged == nil || s.focusClient == nil || s.focusX11 == nil {
			t.Fatalf("focus sources: merged=%v client=%v x11=%v, want all", s.focusMerged != nil, s.focusClient != nil, s.focusX11 != nil)
		}
		if s.auto == nil || s.autoReason != "" {
			t.Fatalf("auto engine nil (%q) in an X11 GNOME session", s.autoReason)
		}
	})
	t.Run("x11 kde auto", func(t *testing.T) {
		s := platformTestInitFork(t, platformTestX11KDE(), "", platformTestShell50)
		if s.tracker != nil || s.layoutReaderFn != nil || s.onOwnSwitch != nil {
			t.Fatal("tracker built in a non-GNOME X11 session with layout-source=auto")
		}
		if s.autoReason != autoUnavailableLayoutSourceUnsupported {
			t.Fatalf("autoReason = %q, want layout-source-unsupported", s.autoReason)
		}
		if s.focusClient != nil || s.focusX11 == nil {
			t.Fatalf("focus sources: client=%v x11=%v, want the X11 source only", s.focusClient != nil, s.focusX11 != nil)
		}
		st := s.statusSnapshot()
		if st.AutoAvailable || st.UnavailableReason != autoUnavailableLayoutSourceUnsupported {
			t.Fatalf("status = %v %q", st.AutoAvailable, st.UnavailableReason)
		}
	})
	t.Run("x11 kde explicit x11", func(t *testing.T) {
		s := platformTestInitFork(t, platformTestX11KDE(), "layout-source=x11\n", platformTestShell50)
		msg := platformTestResyncErr(t, s)
		if !strings.Contains(msg, "x11 query:") || strings.Contains(msg, "ibus query:") {
			t.Fatalf("Resync() = %q, want exactly the x11 source leg", msg)
		}
		if strings.Contains(msg, "resync:") {
			t.Fatalf("Resync() = %q: a gsettings resyncer outside GNOME", msg)
		}
		if s.auto == nil {
			t.Fatalf("auto engine nil (%q) with layout-source=x11", s.autoReason)
		}
	})
}

func TestEnableAutoReasonsX11(t *testing.T) {
	tests := []struct {
		name    string
		env     *SessionEnv
		conf    string
		probe   layoutstate.ShellInfo
		reason  string
		warning string
	}{
		{name: "wayland gnome below 49.3", env: platformTestWaylandGNOME(), probe: platformTestShell43,
			reason: autoUnavailableLayoutSourceUnsupported},
		{name: "x11 source on wayland", env: platformTestWaylandGNOME(), conf: "layout-source=x11\n", probe: platformTestShell50,
			reason: autoUnavailableLayoutSourceUnsupported, warning: "layout-source=x11 needs an X11 session"},
		{name: "wayland kde", env: platformTestWaylandKDE(), probe: platformTestShell50,
			reason: autoUnavailableLayoutSourceUnsupported},
		{name: "layout-source none", env: platformTestX11GNOME(), conf: "layout-source=none\n", probe: platformTestShell43,
			reason: autoUnavailableLayoutSourceOf},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := platformTestInitFork(t, tt.env, tt.conf, tt.probe)
			if s.tracker != nil || s.auto != nil {
				t.Fatalf("tracker=%v auto=%v, want neither", s.tracker != nil, s.auto != nil)
			}
			if s.autoReason != tt.reason {
				t.Fatalf("autoReason = %q, want %q", s.autoReason, tt.reason)
			}
			st := s.statusSnapshot()
			if st.AutoAvailable || st.UnavailableReason != tt.reason {
				t.Fatalf("status = %v %q, want false %q", st.AutoAvailable, st.UnavailableReason, tt.reason)
			}
			switch {
			case tt.warning == "" && len(s.platform.Warnings) != 0:
				t.Fatalf("unexpected warnings %q", s.platform.Warnings)
			case tt.warning != "":
				found := false
				for _, w := range st.ConfigWarnings {
					if strings.Contains(w, tt.warning) {
						found = true
					}
				}
				if !found {
					t.Fatalf("ConfigWarnings = %q, want %q", st.ConfigWarnings, tt.warning)
				}
			}
		})
	}
}

// platformTestStrictSwitcher builds a switcher with a confirmed tracker, a
// fake engine and a strict policy; the merged focus source has no sides, so
// its state is "extension absent" until the test feeds it.
func platformTestStrictSwitcher(t *testing.T, strict bool) (*Switcher, *focus.Merged) {
	t.Helper()
	s, _ := forkTestSwitcher(t, &Config{})
	forkTestWithLayouts(s)
	tr := forkTestTracker(t)
	tr.Engine(layoutstate.SourceX11, "xkb:us::")
	s.tracker = tr
	forkTestFakeEngine(t, s)
	st := userconfig.DefaultSettings()
	if strict {
		st.StrictPurpose = userconfig.StrictOn
	} else {
		st.StrictPurpose = userconfig.StrictOff
	}
	c := forkTestConfig(st, nil, nil)
	s.userCfg.Store(c)
	s.focusPolicy.Store(forkFocusPolicy(c, false))
	m := focus.NewMerged(focus.MergedOptions{})
	s.focusMerged = m
	return s, m
}

func TestStatusAvailabilityStrict(t *testing.T) {
	s, m := platformTestStrictSwitcher(t, true)
	check := func(label string, wantAvailable bool, wantReason, wantSuppressed string) {
		t.Helper()
		st := s.statusSnapshot()
		if st.AutoAvailable != wantAvailable || st.UnavailableReason != wantReason || st.FocusSuppressedReason != wantSuppressed {
			t.Fatalf("%s: AutoAvailable=%v reason=%q suppressed=%q, want %v %q %q",
				label, st.AutoAvailable, st.UnavailableReason, st.FocusSuppressedReason, wantAvailable, wantReason, wantSuppressed)
		}
	}
	check("extension absent", false, autoUnavailableFocusExtRequired, focus.ReasonFocusUnavailable)

	m.ExtensionChanged(focus.State{Available: true, EverAvailable: true, Purpose: focus.PurposeNormal, Seq: 1})
	check("extension owned, text field", true, "", "")
	m.ExtensionChanged(focus.State{Available: true, EverAvailable: true, Purpose: focus.PurposeNone, Seq: 2})
	check("extension owned, no field", true, "", focus.ReasonPurposeUnknown)
	m.ExtensionChanged(focus.State{EverAvailable: true, Locked: true, LockReason: focus.LockReasonExtensionLocked, Purpose: focus.PurposeUnknown, Seq: 3})
	check("locked", true, "", focus.ReasonLocked)
	m.ExtensionChanged(focus.State{EverAvailable: true, Purpose: focus.PurposeUnknown, Seq: 4})
	check("extension vanished", false, autoUnavailableFocusExtRequired, focus.ReasonFocusUnavailable)

	// A reload that turns strict mode off is seen through the live policy,
	// not the start value.
	st := userconfig.DefaultSettings()
	st.StrictPurpose = userconfig.StrictOff
	s.applyUserConfig(forkTestConfig(st, nil, nil))
	check("strict off after reload", true, "", "")

	// focus-source=off: no focus source at all.
	s, _ = platformTestStrictSwitcher(t, true)
	s.focusMerged = nil
	check("focus-source off", false, autoUnavailableFocusSourceOff, focus.ReasonFocusUnavailable)
	if got := s.statusSnapshot().FocusSource; got != forkFocusSourceOff {
		t.Fatalf("FocusSource = %q, want off", got)
	}
	// Layout precedence: an unknown layout wins over the focus reasons.
	s.tracker = forkTestTracker(t)
	check("layout unknown first", false, autoUnavailableLayoutUnknown, focus.ReasonFocusUnavailable)
}

func TestStatusSnapshotX11Identity(t *testing.T) {
	s, m := platformTestStrictSwitcher(t, false)
	st := s.statusSnapshot()
	if st.FocusSource != forkFocusSourceAbsent || st.FocusedApp != "" || st.FocusedWmClass != "" {
		t.Fatalf("before any X11 state: %+v", st)
	}
	m.X11Changed(focus.X11State{Connected: true, Window: 0x400001, AppID: "org.gnome.Terminal", WmClass: "Gnome-terminal", Seq: 1})
	st = s.statusSnapshot()
	if st.FocusSource != forkFocusSourceX11 || st.FocusedApp != "org.gnome.Terminal" || st.FocusedWmClass != "Gnome-terminal" {
		t.Fatalf("x11 identity: FocusSource=%q app=%q class=%q", st.FocusSource, st.FocusedApp, st.FocusedWmClass)
	}
	if st.InputPurpose != string(focus.PurposeUnknown) {
		t.Fatalf("InputPurpose = %q, want unknown on X11", st.InputPurpose)
	}
	// The X11 identity feeds the exclusion list (strict off: fail-open).
	c := forkTestConfig(s.userSettings(), nil, []string{"gnome-terminal"})
	s.userCfg.Store(c)
	s.focusPolicy.Store(forkFocusPolicy(c, false))
	st = s.statusSnapshot()
	if !st.FocusSuppressed || st.FocusSuppressedReason != focus.ReasonExcludedApp {
		t.Fatalf("excluded by WM_CLASS: suppressed=%v %q", st.FocusSuppressed, st.FocusSuppressedReason)
	}
	// Once the extension owns its name its identity wins.
	m.ExtensionChanged(focus.State{Available: true, EverAvailable: true, AppID: "org.gnome.gedit.desktop", WmClass: "Gedit", Purpose: focus.PurposeNormal, Seq: 1})
	st = s.statusSnapshot()
	if st.FocusSource != forkFocusSourceActive || st.FocusedWmClass != "Gedit" || st.FocusSuppressed {
		t.Fatalf("extension identity: FocusSource=%q class=%q suppressed=%v", st.FocusSource, st.FocusedWmClass, st.FocusSuppressed)
	}
}

func TestFocusSourceNameX11(t *testing.T) {
	s, _ := forkTestSwitcher(t, &Config{})
	if got := s.focusSourceName(s.focusState()); got != forkFocusSourceOff {
		t.Fatalf("no merged source: %q, want off", got)
	}
	m := focus.NewMerged(focus.MergedOptions{})
	s.focusMerged = m
	if got := s.focusSourceName(s.focusState()); got != forkFocusSourceAbsent {
		t.Fatalf("empty merged source: %q, want absent", got)
	}
	m.X11Changed(focus.X11State{Connected: true, Window: 1, WmClass: "XTerm", Seq: 1})
	if got := s.focusSourceName(s.focusState()); got != forkFocusSourceX11 {
		t.Fatalf("x11 identity: %q, want x11", got)
	}
	m.X11Changed(focus.X11State{Connected: false, Seq: 2})
	if got := s.focusSourceName(s.focusState()); got != forkFocusSourceAbsent {
		t.Fatalf("x11 disconnected: %q, want absent", got)
	}
	m.ExtensionChanged(focus.State{Available: true, EverAvailable: true, Purpose: focus.PurposeNormal, Seq: 1})
	if got := s.focusSourceName(s.focusState()); got != forkFocusSourceActive {
		t.Fatalf("extension owned: %q, want active", got)
	}
}

func TestStrictPurposeHotReload(t *testing.T) {
	s := platformTestInitFork(t, platformTestX11GNOME(), "", platformTestShell43)
	if !s.focusPolicy.Load().RequirePurpose {
		t.Fatal("auto-strict-purpose=auto is not strict on X11")
	}
	st := s.userSettings()
	st.StrictPurpose = userconfig.StrictOff
	s.applyUserConfig(forkTestConfig(st, nil, nil))
	if s.focusPolicy.Load().RequirePurpose {
		t.Fatal("reload with auto-strict-purpose=false kept strict mode")
	}
	if w := s.statusSnapshot().ConfigWarnings; len(w) != 0 {
		t.Fatalf("ConfigWarnings = %q: auto-strict-purpose is hot-reloadable", w)
	}
	st.StrictPurpose = userconfig.StrictAuto
	s.applyUserConfig(forkTestConfig(st, nil, nil))
	if !s.focusPolicy.Load().RequirePurpose {
		t.Fatal("reload back to auto did not restore strict mode on X11")
	}
	if s.tracker == nil || !s.platform.LayoutX11 {
		t.Fatal("reload changed the platform selection")
	}
}

func TestExceptionsTemplateMatchesX11Classes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), userconfig.DirName)
	if err := userconfig.EnsureFiles(dir); err != nil {
		t.Fatal(err)
	}
	c, err := userconfig.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Warnings) != 0 {
		t.Fatalf("template warnings %q", c.Warnings)
	}
	tests := []struct {
		appID, wmClass string
		want           bool
	}{
		{"", "Gnome-terminal", true},
		{"org.gnome.Terminal", "", true},
		{"org.gnome.Terminal.desktop", "Gnome-terminal", true},
		{"", "keepassxc", true},
		{"", "KeePassXC", true},
		{"org.keepassxc.KeePassXC", "", true},
		{"", "XTerm", true},
		{"", "kgx", true},
		{"org.gnome.Ptyxis.desktop", "org.gnome.Ptyxis", true},
		{"", "gedit", false},
		{"", "Gedit", false},
		{"org.gnome.TextEditor.desktop", "gnome-text-editor", false},
		{"", "", false},
	}
	for _, tt := range tests {
		if got := c.Apps.MatchApp(tt.appID, tt.wmClass); got != tt.want {
			t.Errorf("MatchApp(%q, %q) = %v, want %v", tt.appID, tt.wmClass, got, tt.want)
		}
	}
	// The policy path the daemon takes with an X11 identity and strict off.
	raw := focus.State{AppID: "", WmClass: "XTerm", Purpose: focus.PurposeUnknown}
	p := forkFocusPolicy(c, true)
	if ok, reason := forkFocusDecision(raw, p); ok || reason != focus.ReasonExcludedApp {
		t.Fatalf("xterm by WM_CLASS: Allow = %v %q, want excluded-app", ok, reason)
	}
}

func TestStopForkClosesMergedFocus(t *testing.T) {
	s := platformTestInitFork(t, platformTestX11GNOME(), "", platformTestShell43)
	// Never started: Close on an unstarted merged source and tracker must
	// return at once, twice.
	done := make(chan struct{})
	go func() {
		s.stopFork()
		s.stopFork()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stopFork did not return")
	}
	if snap := s.focusMerged.Snapshot(); snap.Available {
		t.Fatal("merged source reports an available extension without a bus")
	}
}

// The resync error of a tracker whose only push source is the X11 source
// names the display, so a misconfigured DISPLAY is visible in the log.
func TestX11SourceErrorNamesDisplay(t *testing.T) {
	s := platformTestInitFork(t, platformTestX11GNOME(), "", platformTestShell43)
	msg := platformTestResyncErr(t, s)
	if !strings.Contains(msg, platformTestDisplay) {
		t.Fatalf("Resync() = %q, want the display %s", msg, platformTestDisplay)
	}
	if errors.Is(errors.New(msg), layoutstate.ErrIBusNotRunning) {
		t.Fatal("the IBus source ran in an X11 session")
	}
}
