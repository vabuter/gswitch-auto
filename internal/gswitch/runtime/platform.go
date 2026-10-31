package runtime

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/arumata/gswitch/internal/gswitch/detect"
	"github.com/arumata/gswitch/internal/gswitch/focus"
	"github.com/arumata/gswitch/internal/gswitch/layoutstate"
	"github.com/arumata/gswitch/internal/gswitch/userconfig"
)

// gswitch-auto: platform source selection (SPEC-X11 §2, §3.4). The daemon
// decides once at start, from the session type, the desktop, the GNOME Shell
// version and auto.conf, which layout and focus sources it runs: the IBus
// engine on GNOME Shell 49.3+ (Wayland), the XKB group of the X display in
// X11 sessions, the Shell extension and, on X11, the EWMH active window.
// Nothing is selected by build tags.

// PlatformSelection is the result of selectPlatform (one per daemon start;
// IPC ConfigWarnings repeat Warnings). The type is exported only for
// PlatformForDiagnostics; it is constructed by selectPlatform alone and
// carries NO strict flag: the effective auto-strict-purpose lives in the
// focus policy only (hot-reloadable, §2.3).
type PlatformSelection struct {
	X11Session bool // positively X11: XDG_SESSION_TYPE=x11 (or a Display) and nothing Wayland
	Wayland    bool // positively Wayland: WAYLAND_DISPLAY set or XDG_SESSION_TYPE=wayland (input of StrictPurposeFor)
	GNOME      bool // detect.IsGNOMESession
	// Shell is the GNOME Shell version probe; the zero value when no probe
	// ran (layout-source=none, root, non-GNOME session).
	Shell layoutstate.ShellInfo

	LayoutIBus        bool   // add the IBus source
	LayoutIBusGuarded bool   // wrap it in GuardedIBusSource (probe failed; keeps probing)
	LayoutX11         bool   // add the X11 XKB source
	LayoutReason      string // "" | "layout-source-off" | "layout-source-unsupported" | "root-mode"
	// LayoutNote is one log line explaining a disabled or deferred layout
	// source ("" when there is nothing to say).
	LayoutNote string

	FocusExt bool // start the extension client
	FocusX11 bool // start the EWMH source

	Warnings []string // configuration warnings (logged once, repeated in ConfigWarnings)
}

// platformIBusRefusedX11 is the warning of a refused layout-source=ibus in
// an X11 session whose GNOME Shell is below 49.3 (SPEC-X11 §2.1); it replaces
// IBusSourceAllowed's forced-source warning there.
const platformIBusRefusedX11 = "layout-source=ibus is refused in an X11 session: GNOME Shell %s reports the fixed engine xkb:us::eng below 49.3; use layout-source=auto or x11"

// selectPlatform is pure given its inputs. probe is the forkProbeShellVersion
// hook in production and a fake in tests; it runs at most once, and only
// when the IBus source is a candidate (layout-source ibus, or auto in a
// GNOME session), bounded by forkShellProbeTimeout.
func selectPlatform(st userconfig.Settings, env *SessionEnv, euid int,
	probe func(context.Context) layoutstate.ShellInfo,
) PlatformSelection {
	sel := PlatformSelection{
		X11Session: isX11Session(env),
		Wayland:    isWaylandSession(env),
		GNOME:      detect.IsGNOMESession(env),
	}
	if layoutstate.IBusSourceEnabled(st.LayoutSource, env, euid) && probe != nil {
		ctx, cancel := context.WithTimeout(context.Background(), forkShellProbeTimeout)
		sel.Shell = probe(ctx)
		cancel()
	}
	sel.selectLayout(st.LayoutSource, env, euid)
	sel.selectFocus(st.FocusSource, env, euid)
	return sel
}

// selectLayout fills the layout half of the selection (§2.1).
func (sel *PlatformSelection) selectLayout(layoutSource string, env *SessionEnv, euid int) {
	switch {
	case euid == 0:
		sel.LayoutReason = autoUnavailableRootMode
		return
	case layoutSource == userconfig.SourceNone:
		sel.LayoutReason = autoUnavailableLayoutSourceOf
		return
	case layoutSource == userconfig.SourceX11:
		sel.selectForcedX11(env, euid)
		return
	case layoutstate.X11SourceEnabled(layoutSource, env, euid):
		// auto in an X11 GNOME session: the XKB group is authoritative
		// whatever the IBus engine says; never two push sources.
		sel.LayoutX11 = true
		return
	case !layoutstate.IBusSourceEnabled(layoutSource, env, euid):
		sel.LayoutReason = autoUnavailableLayoutSourceUnsupported
		sel.LayoutNote = "no layout source for this session (automatic correction needs GNOME, or layout-source=x11 on X11)"
		return
	}
	ok, deferred, reason, warning := layoutstate.IBusSourceAllowed(layoutSource, sel.Shell)
	if sel.X11Session && layoutSource == userconfig.SourceIBus && reason == layoutstate.GuardReasonIBusEngineStatic {
		// Refused, not merely warned about (SPEC-X11 §2.1, rev. 3): below
		// 49.3 the engine reads xkb:us::eng in every layout, so a forced
		// IBus source would confirm the first layout forever and misjudge
		// every word typed in the second one, while the XKB source that
		// tracks the real group exists in this session.
		sel.LayoutReason = autoUnavailableLayoutSourceUnsupported
		sel.Warnings = append(sel.Warnings, fmt.Sprintf(platformIBusRefusedX11, sel.Shell.Version))
		return
	}
	if warning != "" {
		sel.Warnings = append(sel.Warnings, warning)
	}
	switch {
	case ok:
		sel.LayoutIBus = true
	case deferred:
		sel.LayoutIBus, sel.LayoutIBusGuarded = true, true
		sel.LayoutNote = fmt.Sprintf("IBus layout source deferred: GNOME Shell version unknown (%v), probing in the background", sel.Shell.Err)
	default:
		sel.LayoutReason = autoUnavailableLayoutSourceUnsupported
		if reason == layoutstate.GuardReasonIBusEngineStatic {
			sel.LayoutNote = fmt.Sprintf("IBus layout source disabled: GNOME Shell %s reports a fixed engine for XKB sources (needs 49.3+)", sel.Shell.Version)
		}
	}
}

// selectForcedX11 handles layout-source=x11: accepted by the parser on every
// platform, usable only with an X display outside a Wayland session.
func (sel *PlatformSelection) selectForcedX11(env *SessionEnv, euid int) {
	switch {
	case sel.Wayland:
		sel.Warnings = append(sel.Warnings, "layout-source=x11 needs an X11 session")
	case !layoutstate.X11SourceEnabled(userconfig.SourceX11, env, euid):
		sel.Warnings = append(sel.Warnings, "layout-source=x11 needs an X display (DISPLAY is unset)")
	default:
		sel.LayoutX11 = true
		return
	}
	sel.LayoutReason = autoUnavailableLayoutSourceUnsupported
}

// selectFocus fills the focus half of the selection (§2.2): the extension
// client on GNOME (and in sessions that are not positively X11, where the
// extension may still appear), the EWMH source in X11 sessions.
func (sel *PlatformSelection) selectFocus(focusSource string, env *SessionEnv, euid int) {
	if euid == 0 || focusSource != userconfig.SourceAuto {
		return
	}
	sel.FocusExt = sel.GNOME || !sel.X11Session
	sel.FocusX11 = focus.X11FocusEnabled(focusSource, env, euid)
}

// Summary is the one-line description logged at start (strict is the start
// value of auto-strict-purpose; the live value is the policy's).
func (sel PlatformSelection) Summary(strict bool) string {
	var layout []string
	if sel.LayoutX11 {
		layout = append(layout, layoutstate.SourceX11)
	}
	if sel.LayoutIBus {
		name := layoutstate.SourceIBus
		if sel.LayoutIBusGuarded {
			name += " (guarded)"
		}
		layout = append(layout, name)
	}
	layoutText := strings.Join(layout, ", ")
	if layoutText == "" {
		layoutText = "none (" + sel.LayoutReason + ")"
	}
	if sel.Shell.Version != "" {
		layoutText += " (GNOME Shell " + sel.Shell.Version + ")"
	}
	var fsrc []string
	if sel.FocusExt {
		fsrc = append(fsrc, "extension")
	}
	if sel.FocusX11 {
		fsrc = append(fsrc, layoutstate.SourceX11)
	}
	focusText := strings.Join(fsrc, ", ")
	if focusText == "" {
		focusText = "none"
	}
	return fmt.Sprintf("layout sources: %s; focus sources: %s; strict purpose: %v", layoutText, focusText, strict)
}

// SessionName names the session for diagnostics: "x11", "wayland" or
// "unknown".
func (sel PlatformSelection) SessionName() string {
	switch {
	case sel.Wayland:
		return "wayland"
	case sel.X11Session:
		return "x11"
	default:
		return "unknown"
	}
}

// isX11Session implements the SPEC-X11 §0 rule (the logic of
// shouldVerifyGNOMEX11LayoutSwitch, which stays untouched): with a
// SessionEnv, SessionType x11 or a Display and nothing Wayland; without one,
// XDG_SESSION_TYPE=x11 with WAYLAND_DISPLAY empty (DISPLAY alone never
// counts, K-X3).
func isX11Session(env *SessionEnv) bool {
	if env != nil {
		if strings.EqualFold(env.SessionType, "wayland") || env.WaylandDisplay != "" {
			return false
		}
		return strings.EqualFold(env.SessionType, "x11") || env.Display != ""
	}
	return strings.EqualFold(os.Getenv("XDG_SESSION_TYPE"), "x11") &&
		os.Getenv("WAYLAND_DISPLAY") == ""
}

// isWaylandSession is "positively Wayland" (§3.3): WAYLAND_DISPLAY set or
// XDG_SESSION_TYPE=wayland, from the SessionEnv when non-nil, else from the
// process environment.
func isWaylandSession(env *SessionEnv) bool {
	if env != nil {
		return strings.EqualFold(env.SessionType, "wayland") || env.WaylandDisplay != ""
	}
	return strings.EqualFold(os.Getenv("XDG_SESSION_TYPE"), "wayland") ||
		os.Getenv("WAYLAND_DISPLAY") != ""
}

// x11DisplayFor is the Display option of the X11 sources: the session's
// display in root compatibility mode, else "" ($DISPLAY).
func x11DisplayFor(env *SessionEnv) string {
	if env != nil {
		return env.Display
	}
	return ""
}

// PlatformForDiagnostics runs the daemon's source selection for the calling
// user: user config from userconfig.Dir() (its warnings are appended to
// sel.Warnings), env nil (process environment), forkGeteuid(), and the
// forkProbeShellVersion hook bounded by ctx. Nothing is started. err reports
// a configuration directory or load problem; sel is usable either way.
func PlatformForDiagnostics(ctx context.Context) (sel PlatformSelection, err error) {
	cfg, err := loadUserConfigForDiagnostics()
	probe := func(probeCtx context.Context) layoutstate.ShellInfo {
		bounded, cancel := context.WithCancel(probeCtx)
		defer cancel()
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
		return forkProbeShellVersion(bounded)
	}
	sel = selectPlatform(cfg.Settings, nil, forkGeteuid(), probe)
	sel.Warnings = append(sel.Warnings, cfg.Warnings...)
	return sel, err
}

// FocusPolicyForDiagnostics loads ~/.config/gswitch and returns
// forkFocusPolicy(cfg, wayland) by value; callers pass sel.Wayland. A load
// problem is returned with the policy of the usable part.
func FocusPolicyForDiagnostics(wayland bool) (focus.Policy, error) {
	cfg, err := loadUserConfigForDiagnostics()
	return *forkFocusPolicy(cfg, wayland), err
}

// loadUserConfigForDiagnostics loads the calling user's configuration
// read-only; the result is never nil.
func loadUserConfigForDiagnostics() (*userconfig.Config, error) {
	dir, err := userconfig.Dir()
	if err != nil {
		cfg, _ := userconfig.Load("")
		return cfg, fmt.Errorf("user config directory: %w", err)
	}
	return userconfig.Load(dir)
}
