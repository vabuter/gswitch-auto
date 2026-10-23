package layoutstate

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/godbus/dbus/v5"
)

// gswitch-auto: GNOME Shell version guard for the IBus layout source
// (SPEC-X11 §3.1.1). GNOME Shell below 49.3 sets the IBus global engine to the
// fixed "xkb:us::eng" for every XKB input source, so GlobalEngineChanged never
// reflects the active layout there; 49.3+ derives the engine name from the
// source ("xkb:ru::rus"). The daemon therefore reads org.gnome.Shell's
// ShellVersion property before it trusts the IBus source.

const (
	shellService     = "org.gnome.Shell"
	shellInterface   = "org.gnome.Shell"
	shellPath        = dbus.ObjectPath("/org/gnome/Shell")
	shellVersionProp = "ShellVersion"

	errServiceUnknown = "org.freedesktop.DBus.Error.ServiceUnknown"
	errNameHasNoOwner = "org.freedesktop.DBus.Error.NameHasNoOwner"

	// shellTrackingMajor.shellTrackingMinor is the first GNOME Shell version
	// whose IBus engine follows the XKB source.
	shellTrackingMajor = 49
	shellTrackingMinor = 3

	// guardProbeTimeout bounds one probe of GuardedIBusSource.Run.
	guardProbeTimeout = 2 * time.Second
	// guardBackoffInitial..guardBackoffMax paces the probe retries.
	guardBackoffInitial = time.Second
	guardBackoffMax     = 30 * time.Second
)

// Reasons returned by IBusSourceAllowed.
const (
	// GuardReasonShellVersionUnknown: the probe failed (no session bus,
	// org.gnome.Shell not owned yet, or an unexpected reply).
	GuardReasonShellVersionUnknown = "shell-version-unknown"
	// GuardReasonIBusEngineStatic: GNOME Shell is below 49.3 and reports the
	// fixed engine for every XKB source.
	GuardReasonIBusEngineStatic = "ibus-engine-static"
)

// ErrShellUnavailable is the probe error when org.gnome.Shell has no owner or
// no session bus can be reached.
var ErrShellUnavailable = errors.New("org.gnome.Shell is not available on the session bus")

// ErrShellVersionFormat is the probe error for a ShellVersion value that
// ParseShellVersion does not accept.
var ErrShellVersionFormat = errors.New("org.gnome.Shell ShellVersion has an unexpected format")

// ShellInfo is the result of one org.gnome.Shell ShellVersion probe.
type ShellInfo struct {
	Version string // e.g. "43.9", "50.6", "49.rc"; "" when Err != nil
	Err     error  // nil, ErrShellUnavailable (name not owned / no session bus), or a D-Bus error
}

// ProbeShellVersion reads org.freedesktop.DBus.Properties.Get("org.gnome.Shell",
// "ShellVersion") on /org/gnome/Shell over a private session connection
// (connectSessionBus, never the shared dbus.SessionBus(); FlagNoAutoStart)
// with ctx bounding the call (callers use 2 s). It never returns a Version
// with a nil Err that failed to parse: an unparsable string is
// Err = ErrShellVersionFormat.
func ProbeShellVersion(ctx context.Context) ShellInfo {
	return probeShellVersion(ctx, connectSessionBus)
}

// probeShellVersion is ProbeShellVersion with the bus connector injected.
func probeShellVersion(ctx context.Context, connect func(opts ...dbus.ConnOption) (*dbus.Conn, error)) ShellInfo {
	conn, err := connect(dbus.WithContext(ctx))
	if err != nil {
		return ShellInfo{Err: fmt.Errorf("%w: %w", ErrShellUnavailable, err)}
	}
	defer conn.Close()

	var value dbus.Variant
	err = conn.Object(shellService, shellPath).CallWithContext(ctx,
		"org.freedesktop.DBus.Properties.Get", dbus.FlagNoAutoStart,
		shellInterface, shellVersionProp).Store(&value)
	if err != nil {
		if isShellUnavailable(err) {
			return ShellInfo{Err: fmt.Errorf("%w: %w", ErrShellUnavailable, err)}
		}
		return ShellInfo{Err: fmt.Errorf("read %s %s: %w", shellInterface, shellVersionProp, err)}
	}
	version, ok := shellVersionFromValue(value)
	if !ok {
		return ShellInfo{Err: fmt.Errorf("%w: %T", ErrShellVersionFormat, value.Value())}
	}
	if _, _, ok := ParseShellVersion(version); !ok {
		return ShellInfo{Err: fmt.Errorf("%w: %q", ErrShellVersionFormat, version)}
	}
	return ShellInfo{Version: version}
}

// shellVersionFromValue unwraps the Properties.Get reply (a variant holding
// s; extra variant levels are tolerated like engineNameFromDesc does).
func shellVersionFromValue(v any) (string, bool) {
	for range maxVariantDepth {
		variant, ok := v.(dbus.Variant)
		if !ok {
			break
		}
		v = variant.Value()
	}
	s, ok := v.(string)
	return strings.TrimSpace(s), ok
}

// isShellUnavailable matches the bus daemon's replies for a name without an
// owner (FlagNoAutoStart turns an activatable name into ServiceUnknown too).
func isShellUnavailable(err error) bool {
	if dbusErr, ok := asDBusError(err); ok {
		switch dbusErr.Name {
		case errServiceUnknown, errNameHasNoOwner:
			return true
		}
	}
	return false
}

// ParseShellVersion accepts "<major>[.<minor>[.<micro>]]" where a non-numeric
// minor ("alpha", "beta", "rc", "rc.1") counts as 0: "43.9"→43,9; "49.3"→49,3;
// "50.6"→50,6; "49.rc"→49,0; "48.alpha"→48,0; ""/"x" ⇒ ok=false.
func ParseShellVersion(s string) (major, minor int, ok bool) {
	parts := strings.Split(strings.TrimSpace(s), ".")
	major, ok = parseVersionNumber(parts[0])
	if !ok {
		return 0, 0, false
	}
	if len(parts) > 1 {
		if n, isNumber := parseVersionNumber(parts[1]); isNumber {
			minor = n
		}
	}
	return major, minor, true
}

// parseVersionNumber parses a non-negative decimal made of digits only.
func parseVersionNumber(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

// IBusEngineTracksLayout reports whether GNOME Shell of that version sets the
// IBus global engine per XKB source (49.3+) rather than the fixed
// "xkb:us::eng". An unparsable version never tracks.
func IBusEngineTracksLayout(version string) bool {
	major, minor, ok := ParseShellVersion(version)
	if !ok {
		return false
	}
	return major > shellTrackingMajor || (major == shellTrackingMajor && minor >= shellTrackingMinor)
}

// IBusSourceAllowed combines the auto.conf layout-source value with the probe
// result (used only after IBusSourceEnabled returned true). "ibus" ⇒ ok, with
// a warning when the version is unknown or below 49.3; "auto" ⇒ ok only when
// the probe succeeded with a tracking version. deferred is true when the
// probe failed (the caller wraps the IBus source in GuardedIBusSource, which
// keeps probing). reason is "", GuardReasonShellVersionUnknown or
// GuardReasonIBusEngineStatic and describes the probe result whatever the
// decision.
func IBusSourceAllowed(layoutSource string, info ShellInfo) (ok, deferred bool, reason, warning string) {
	switch {
	case info.Err != nil:
		reason = GuardReasonShellVersionUnknown
	case !IBusEngineTracksLayout(info.Version):
		reason = GuardReasonIBusEngineStatic
	}
	switch layoutSource {
	case "ibus":
		switch reason {
		case GuardReasonShellVersionUnknown:
			warning = fmt.Sprintf("layout-source=ibus forces the IBus source; GNOME Shell version unknown (%v)", info.Err)
		case GuardReasonIBusEngineStatic:
			warning = fmt.Sprintf("layout-source=ibus forces the IBus source; GNOME Shell %s reports a fixed engine below 49.3", info.Version)
		}
		return true, false, reason, warning
	case "auto":
		switch reason {
		case "":
			return true, false, "", ""
		case GuardReasonShellVersionUnknown:
			return false, true, reason, ""
		default:
			return false, false, reason, ""
		}
	default:
		return false, false, reason, ""
	}
}

// GuardState is the state of a GuardedIBusSource.
type GuardState uint8

const (
	// GuardPending: the GNOME Shell version has not been read yet; the inner
	// source is not running.
	GuardPending GuardState = iota
	// GuardPassed: GNOME Shell is 49.3+ and the inner source runs.
	GuardPassed
	// GuardRefused: GNOME Shell is below 49.3; the inner source never runs.
	GuardRefused
)

// String returns the lower-case name of the state.
func (g GuardState) String() string {
	switch g {
	case GuardPending:
		return "pending"
	case GuardPassed:
		return "passed"
	case GuardRefused:
		return "refused"
	default:
		return fmt.Sprintf("guard(%d)", uint8(g))
	}
}

// GuardedIBusSource runs inner only after ProbeShellVersion succeeds with a
// tracking version. It exists because the user unit starts after
// graphical-session.target but gnome-shell may own its name a little later: a
// one-shot "unavailable ⇒ no IBus source" would silently disable automatic
// correction until the next daemon restart. It implements Source.
type GuardedIBusSource struct {
	inner Source
	logf  func(string, ...any)

	// Test hooks; production values are set by NewGuardedIBusSource.
	probe func(ctx context.Context) ShellInfo
	sleep func(ctx context.Context, d time.Duration) bool

	state atomic.Uint32 // GuardState
}

var _ Source = (*GuardedIBusSource)(nil)

// NewGuardedIBusSource wraps inner (normally an *IBusSource). logf receives
// the one-line decision.
func NewGuardedIBusSource(inner Source, logf func(string, ...any)) *GuardedIBusSource {
	return &GuardedIBusSource{
		inner: inner,
		logf:  logf,
		probe: ProbeShellVersion,
		sleep: sleepCtx,
	}
}

// Name implements Source: the inner source's name ("ibus").
func (g *GuardedIBusSource) Name() string { return g.inner.Name() }

// State reports pending, passed or refused (gswitch --layout-state).
func (g *GuardedIBusSource) State() GuardState { return GuardState(g.state.Load()) }

// Run implements Source: probe (2 s per attempt) with a 1 s…30 s backoff until
// ctx is done. A version below 49.3 is logged once and ends Run without ever
// calling sink (the tracker stays Unknown); a tracking version hands ctx and
// sink to inner.Run.
func (g *GuardedIBusSource) Run(ctx context.Context, sink Sink) {
	backoff := guardBackoffInitial
	lastErr := ""
	for ctx.Err() == nil {
		probeCtx, cancel := context.WithTimeout(ctx, guardProbeTimeout)
		info := g.probe(probeCtx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if info.Err == nil {
			if !IBusEngineTracksLayout(info.Version) {
				g.state.Store(uint32(GuardRefused))
				g.log("IBus layout source disabled: GNOME Shell %s reports a fixed engine for XKB sources (needs 49.3+)", info.Version)
				return
			}
			g.state.Store(uint32(GuardPassed))
			g.log("IBus layout source enabled: GNOME Shell %s tracks the layout in the IBus engine", info.Version)
			g.inner.Run(ctx, sink)
			return
		}
		if msg := info.Err.Error(); msg != lastErr {
			lastErr = msg
			g.log("IBus layout source waiting for GNOME Shell: %v (retrying)", info.Err)
		}
		if !g.sleep(ctx, backoff) {
			return
		}
		backoff = min(backoff*2, guardBackoffMax)
	}
}

// Query implements Source. It delegates to inner only once the guard passed;
// otherwise it returns ErrShellUnavailable wrapped, so that Tracker.Resync
// falls through to the resyncers.
func (g *GuardedIBusSource) Query(ctx context.Context) (string, error) {
	switch g.State() {
	case GuardPassed:
		return g.inner.Query(ctx)
	case GuardRefused:
		return "", fmt.Errorf("IBus layout source disabled below GNOME Shell 49.3: %w", ErrShellUnavailable)
	default:
		return "", fmt.Errorf("IBus layout source waiting for the GNOME Shell version: %w", ErrShellUnavailable)
	}
}

func (g *GuardedIBusSource) log(format string, args ...any) {
	if g.logf != nil {
		g.logf(format, args...)
	}
}

// sleepCtx waits d or until ctx is done; it reports whether the wait completed.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
