package focus

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// gswitch-auto: focus diagnostics for `gswitch --focus-state` (SPEC-X11
// §3.2.3). Read-only: one private session-bus connection for the extension
// probe and one X connection for the EWMH read; nothing is changed anywhere.

// DiagOptions configures RunDiagnostics. ExtensionEnabled and X11Enabled are
// the daemon's selection (PlatformSelection.FocusExt/FocusX11); Policy is
// the daemon's focus policy; Watch > 0 streams merged changes for that long.
type DiagOptions struct {
	Display          string
	ExtensionEnabled bool
	X11Enabled       bool
	Policy           Policy
	Watch            time.Duration
}

// RunDiagnostics prints the extension view (owned/absent and GetFocus), the
// X11 view (active window, WM_CLASS, _GTK_APPLICATION_ID), the merged state
// with its app source and the policy decision, then, when Watch is positive,
// streams merged changes in the same format. It returns an error when an
// enabled source cannot be reached; the information gathered so far has been
// printed by then.
func RunDiagnostics(ctx context.Context, w io.Writer, opts DiagOptions) error {
	pw := &focusDiagWriter{w: w}
	var firstErr error

	ext := State{Purpose: PurposeUnknown}
	switch {
	case !opts.ExtensionEnabled:
		pw.printf("Extension: off\n")
	default:
		st, err := probeExtension(ctx)
		switch {
		case err != nil:
			pw.printf("Extension: error: %v\n", err)
			firstErr = err
		case !st.Available:
			pw.printf("Extension: absent (%s not owned)\n", BusName)
		case st.FocusUnknown:
			pw.printf("Extension: owned (GetFocus failed)\n")
		default:
			pw.printf("Extension: owned (GetFocus: app %q, wm_class %q, purpose %s)\n", st.AppID, st.WmClass, st.Purpose)
		}
		if err == nil {
			ext = st
		}
	}

	var x11 X11State
	switch {
	case !opts.X11Enabled:
		pw.printf("X11: off\n")
	default:
		st, err := probeX11(ctx, opts.Display)
		if err != nil {
			pw.printf("X11: error: %v\n", err)
			if firstErr == nil {
				firstErr = err
			}
		} else {
			x11 = st
			pw.printf("X11: %s\n", describeX11(st))
		}
	}

	merged, appSource := mergeStates(ext, x11)
	pw.printf("Merged: %s\n", describeMerged(merged, appSource))
	pw.printf("Policy: %s\n", describePolicy(opts.Policy, merged))
	if firstErr != nil {
		return firstErr
	}
	if opts.Watch <= 0 {
		return pw.error()
	}
	return watchFocus(ctx, pw, opts)
}

// probeExtension reads the extension state once over a private session-bus
// connection (never dbus.SessionBus, C27). A GetFocus failure while the name
// is owned yields FocusUnknown, as in the client.
func probeExtension(ctx context.Context) (State, error) {
	conn, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
	if err != nil {
		return State{}, fmt.Errorf("connect session bus: %w", err)
	}
	defer conn.Close()

	callCtx, cancel := context.WithTimeout(ctx, DefaultCallTimeout)
	defer cancel()
	var owned bool
	if err := conn.BusObject().CallWithContext(callCtx, dbusName+".NameHasOwner", 0, BusName).Store(&owned); err != nil {
		return State{}, fmt.Errorf("NameHasOwner(%s): %w", BusName, err)
	}
	st := State{Purpose: PurposeUnknown}
	if !owned {
		return st, nil
	}
	st.Available, st.EverAvailable = true, true
	var appID, wmClass, purpose string
	err = conn.Object(BusName, ObjectPath).
		CallWithContext(callCtx, methodGetFocus, dbus.FlagNoAutoStart).
		Store(&appID, &wmClass, &purpose)
	if err != nil {
		st.FocusUnknown = true
		return st, nil
	}
	setApp(&st, appID, wmClass)
	st.Purpose = knownPurpose(purpose)
	return st, nil
}

// probeX11 reads the active window once over a temporary connection. The
// connect is bounded by ctx (and x11ConnectTimeout), every reply by
// x11ReplyTimeout; a cancelled ctx (Ctrl+C) closes the connection.
func probeX11(ctx context.Context, display string) (X11State, error) {
	sess, err := openX11Session(ctx, x11Dial, display)
	if err != nil {
		return X11State{}, err
	}
	defer sess.close()
	stop := context.AfterFunc(ctx, sess.close)
	defer stop()
	return sess.read()
}

func describeX11(st X11State) string {
	switch {
	case !st.Connected:
		return "disconnected"
	case st.Window == 0:
		return "connected, no active window"
	default:
		return fmt.Sprintf("connected, active window 0x%x, WM_CLASS %q, %s %q",
			st.Window, st.WmClass, x11AtomGtkApplicationID, st.AppID)
	}
}

func describeMerged(st State, appSource string) string {
	if appSource == "" {
		appSource = "none"
	}
	s := fmt.Sprintf("app source: %s, app key: %q, wm_class key: %q, purpose: %s",
		appSource, st.AppKey, st.WmClassKey, st.Purpose)
	if st.FocusUnknown {
		s += ", focus unknown"
	}
	if st.Locked {
		s += ", locked: " + st.LockReason
	}
	return s
}

func describePolicy(p Policy, st State) string {
	ok, reason := p.Allow(st)
	if ok {
		return "allowed"
	}
	return "denied (" + reason + ")"
}

// watchFocus streams merged changes for opts.Watch through a Merged source
// built like the daemon's.
func watchFocus(ctx context.Context, pw *focusDiagWriter, opts DiagOptions) error {
	pw.printf("Watching focus changes for %v...\n", opts.Watch)
	m := NewMerged(MergedOptions{})
	m.opts.OnChange = func(st State) {
		pw.printf("%s Merged: %s; Policy: %s\n", time.Now().Format("15:04:05.000"),
			describeMerged(st, m.AppSource()), describePolicy(opts.Policy, st))
	}
	if opts.ExtensionEnabled {
		m.SetExtension(New(Options{OnChange: m.ExtensionChanged}))
	}
	if opts.X11Enabled {
		m.SetX11(NewX11Source(X11Options{Display: opts.Display, OnChange: m.X11Changed}))
	}
	watchCtx, cancel := context.WithTimeout(ctx, opts.Watch)
	defer cancel()
	m.Start(watchCtx)
	<-watchCtx.Done()
	m.Close()
	return pw.error()
}

// focusDiagWriter remembers the first write error and serialises writes
// from the source goroutines.
type focusDiagWriter struct {
	mu  sync.Mutex
	w   io.Writer
	err error
}

func (d *focusDiagWriter) printf(format string, args ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return
	}
	_, d.err = fmt.Fprintf(d.w, format, args...)
}

func (d *focusDiagWriter) error() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err
}
