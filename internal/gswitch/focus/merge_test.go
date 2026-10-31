package focus

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestMergeStates(t *testing.T) {
	now := time.Now()
	later := now.Add(time.Second)
	available := State{
		Available: true, EverAvailable: true,
		AppID: "org.gnome.Ptyxis.desktop", WmClass: "org.gnome.Ptyxis",
		AppKey: "org.gnome.ptyxis", WmClassKey: "org.gnome.ptyxis",
		Purpose: PurposeTerminal, Seq: 3, UpdatedAt: now,
	}
	absent := State{Purpose: PurposeUnknown}
	vanished := State{EverAvailable: true, Purpose: PurposeUnknown, Locked: true, LockReason: LockReasonExtensionVanished, Seq: 4, UpdatedAt: later}
	unknownFocus := State{Available: true, EverAvailable: true, Purpose: PurposeUnknown, FocusUnknown: true, Seq: 5, UpdatedAt: now}
	x11 := X11State{Connected: true, Window: 0x400001, AppID: "org.gnome.Terminal", WmClass: "Gnome-terminal", Seq: 2, UpdatedAt: later}
	x11Empty := X11State{Connected: true, Seq: 7, UpdatedAt: now}
	x11Off := X11State{}

	withX11 := func(st State, seq uint64, at time.Time) State {
		st.AppID, st.WmClass = "org.gnome.Terminal", "Gnome-terminal"
		st.AppKey, st.WmClassKey = "org.gnome.terminal", "gnome-terminal"
		st.Seq, st.UpdatedAt = seq, at
		return st
	}
	seq := func(st State, seq uint64, at time.Time) State {
		st.Seq, st.UpdatedAt = seq, at
		return st
	}

	tests := []struct {
		name string
		ext  State
		x    X11State
		want State
		src  string
	}{
		{"extension available wins", available, x11, seq(available, 5, later), AppSourceExtension},
		{"extension available, x11 off", available, x11Off, available, AppSourceExtension},
		{"extension absent + x11", absent, x11, withX11(absent, 2, later), AppSourceX11},
		{"extension absent + x11 without window", absent, x11Empty, seq(absent, 7, now), AppSourceX11},
		{"extension absent + x11 disconnected", absent, x11Off, absent, ""},
		{"lock preserved with x11 identity", vanished, x11, withX11(vanished, 6, later), AppSourceX11},
		{"lock preserved without x11", vanished, x11Off, vanished, ""},
		{"unknown focus keeps the extension's empty identity", unknownFocus, x11, seq(unknownFocus, 7, later), AppSourceExtension},
		{"both zero", State{}, x11Off, State{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, src := mergeStates(tt.ext, tt.x)
			if got != tt.want || src != tt.src {
				t.Fatalf("mergeStates() = (%+v, %q), want (%+v, %q)", got, src, tt.want, tt.src)
			}
			if got.Purpose != tt.ext.Purpose || got.Available != tt.ext.Available || got.Locked != tt.ext.Locked {
				t.Fatalf("extension fields not preserved: %+v", got)
			}
		})
	}
}

func TestMergedAdapters(t *testing.T) {
	rec := &focusTestRecorder{}
	m := NewMerged(MergedOptions{OnChange: rec.onChange, Logf: t.Logf})
	if st := m.Snapshot(); st.Available || st.Purpose != PurposeUnknown || st.Seq != 0 || st.AppKey != "" {
		t.Fatalf("initial state = %+v", st)
	}
	if src := m.AppSource(); src != "" {
		t.Fatalf("initial AppSource = %q", src)
	}

	x11Terminal := X11State{Connected: true, Window: 1, AppID: "org.gnome.Terminal", WmClass: "Gnome-terminal", Seq: 1}
	m.X11Changed(x11Terminal)
	st := m.Snapshot()
	if rec.count() != 1 || st.Seq != 1 || st.AppKey != "org.gnome.terminal" || st.WmClassKey != "gnome-terminal" ||
		st.AppID != "org.gnome.Terminal" || st.WmClass != "Gnome-terminal" || st.Purpose != PurposeUnknown || st.Available {
		t.Fatalf("after X11Changed: %d changes, state %+v", rec.count(), st)
	}
	if src := m.AppSource(); src != AppSourceX11 {
		t.Fatalf("AppSource = %q, want x11", src)
	}

	// The same report again (nothing moved): no notification.
	m.X11Changed(x11Terminal)
	if rec.count() != 1 {
		t.Fatalf("identical X11 state notified: %d changes", rec.count())
	}

	// Another window of the same application: Seq moves, identity does not.
	x11Terminal2 := x11Terminal
	x11Terminal2.Window, x11Terminal2.Seq = 2, 2
	m.X11Changed(x11Terminal2)
	if st := m.Snapshot(); rec.count() != 2 || st.Seq != 2 || st.WmClassKey != "gnome-terminal" {
		t.Fatalf("after a window change: %d changes, state %+v", rec.count(), st)
	}

	// The extension appears: its identity and purpose win.
	editor := State{Available: true, EverAvailable: true, AppID: "gedit.desktop", WmClass: "Gedit", AppKey: "gedit", WmClassKey: "gedit", Purpose: PurposeNormal, Seq: 1}
	m.ExtensionChanged(editor)
	st = m.Snapshot()
	if rec.count() != 3 || st.Seq != 3 || st.AppKey != "gedit" || st.Purpose != PurposeNormal || !st.Available || m.AppSource() != AppSourceExtension {
		t.Fatalf("after ExtensionChanged: %d changes, state %+v, source %q", rec.count(), st, m.AppSource())
	}

	// X11 changes while the extension is available: Seq moves (a word typed
	// across the switch is skipped), identity stays the extension's.
	x11Other := X11State{Connected: true, Window: 3, WmClass: "XTerm", Seq: 3}
	m.X11Changed(x11Other)
	if st := m.Snapshot(); rec.count() != 4 || st.Seq != 4 || st.WmClassKey != "gedit" || m.AppSource() != AppSourceExtension {
		t.Fatalf("x11 change under the extension: %d changes, state %+v", rec.count(), st)
	}

	// The extension vanishes (locked): lock preserved, identity from X11.
	m.ExtensionChanged(State{EverAvailable: true, Purpose: PurposeUnknown, Locked: true, LockReason: LockReasonExtensionVanished, Seq: 2})
	st = m.Snapshot()
	if rec.count() != 5 || st.Seq != 5 || !st.Locked || st.LockReason != LockReasonExtensionVanished ||
		st.WmClassKey != "xterm" || st.AppKey != "" || st.Available || m.AppSource() != AppSourceX11 {
		t.Fatalf("after the extension vanished: %d changes, state %+v, source %q", rec.count(), st, m.AppSource())
	}

	// X11 disconnects: empty identity, lock still preserved.
	m.X11Changed(X11State{Seq: 4})
	st = m.Snapshot()
	if rec.count() != 6 || st.Seq != 6 || st.WmClassKey != "" || st.WmClass != "" || !st.Locked || m.AppSource() != "" {
		t.Fatalf("after the X11 disconnect: %d changes, state %+v, source %q", rec.count(), st, m.AppSource())
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	for i := 1; i < len(rec.states); i++ {
		if rec.states[i].Seq <= rec.states[i-1].Seq {
			t.Fatalf("Seq not monotonic: %d after %d", rec.states[i].Seq, rec.states[i-1].Seq)
		}
	}
}

func TestMergedNilSafe(t *testing.T) {
	ctx := context.Background()
	m := NewMerged(MergedOptions{})
	m.SetExtension(nil)
	m.SetX11(nil)
	if m.ext != nil || m.x11 != nil {
		t.Fatal("nil sides were stored")
	}
	m.Start(ctx)
	m.Start(ctx)
	m.ExtensionChanged(State{Available: true, EverAvailable: true, Purpose: PurposeNormal, Seq: 1})
	if st := m.Snapshot(); !st.Available || st.Seq != 1 {
		t.Fatalf("state = %+v", st)
	}
	m.Close()
	m.Close()
	m.Start(ctx) // after Close: no-op

	// Only the extension side, without a bus: nothing becomes available and
	// Close returns promptly.
	m = NewMerged(MergedOptions{Logf: t.Logf})
	m.SetExtension(New(Options{Address: "unix:path=/nonexistent/gswitch-focus-test/bus", OnChange: m.ExtensionChanged}))
	m.Start(ctx)
	time.Sleep(30 * time.Millisecond)
	if st := m.Snapshot(); st.Available || st.Locked {
		t.Fatalf("state without a bus = %+v", st)
	}
	start := time.Now()
	m.Close()
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("Close took %v", elapsed)
	}

	// Only the X11 side, without a display.
	m = NewMerged(MergedOptions{Logf: t.Logf})
	src := NewX11Source(X11Options{OnChange: m.X11Changed})
	src.dial = func(context.Context, string) (x11Conn, error) { return nil, errors.New("no display") }
	src.retryMin, src.retryMax = 5*time.Millisecond, 10*time.Millisecond
	m.SetX11(src)
	m.Start(ctx)
	time.Sleep(30 * time.Millisecond)
	if st := m.Snapshot(); st.Available || st.AppKey != "" || m.AppSource() != "" {
		t.Fatalf("state without a display = %+v", st)
	}
	start = time.Now()
	m.Close()
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("Close took %v", elapsed)
	}
}

func TestMergedWithX11Source(t *testing.T) {
	conn := x11FocusTestTerminalConn()
	rec := &focusTestRecorder{}
	m := NewMerged(MergedOptions{OnChange: rec.onChange, Logf: t.Logf})
	src := NewX11Source(X11Options{OnChange: m.X11Changed, Logf: t.Logf})
	src.dial = x11FocusTestDialer(conn)
	src.retryMin, src.retryMax = 10*time.Millisecond, 50*time.Millisecond
	m.SetX11(src)
	m.SetExtension(nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	t.Cleanup(m.Close)

	deadline := time.Now().Add(focusTestWait)
	for m.Snapshot().WmClassKey != "gnome-terminal" {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the X11 identity; state %+v", m.Snapshot())
		}
		time.Sleep(5 * time.Millisecond)
	}
	st := m.Snapshot()
	if st.Available || st.AppKey != "org.gnome.terminal" || st.Purpose != PurposeUnknown || st.Seq != 1 || m.AppSource() != AppSourceX11 {
		t.Fatalf("merged state = %+v, source %q", st, m.AppSource())
	}
	if rec.count() != 1 {
		t.Fatalf("OnChange called %d times", rec.count())
	}

	conn.setActive(x11FocusTestEditor)
	for m.Snapshot().Seq < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the window change; state %+v", m.Snapshot())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if st := m.Snapshot(); st.WmClassKey != "gedit" || st.AppKey != "" {
		t.Fatalf("merged state after the window change = %+v", st)
	}

	start := time.Now()
	m.Close()
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("Close took %v", elapsed)
	}
	if !conn.isClosed() {
		t.Fatal("Close did not close the X connection")
	}
}

func TestRunDiagnosticsSourcesOff(t *testing.T) {
	var buf bytes.Buffer
	err := RunDiagnostics(context.Background(), &buf, DiagOptions{Policy: Policy{RequireExtension: true}})
	if err != nil {
		t.Fatalf("RunDiagnostics: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"Extension: off\n",
		"X11: off\n",
		`Merged: app source: none, app key: "", wm_class key: "", purpose: unknown` + "\n",
		"Policy: denied (focus-unavailable)\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Watching") {
		t.Errorf("Watch 0 streamed changes:\n%s", out)
	}
}

func TestRunDiagnosticsX11(t *testing.T) {
	saved := x11Dial
	t.Cleanup(func() { x11Dial = saved })
	x11Dial = func(context.Context, string) (x11Conn, error) { return x11FocusTestTerminalConn(), nil }

	var buf bytes.Buffer
	err := RunDiagnostics(context.Background(), &buf, DiagOptions{Display: ":99", X11Enabled: true, Policy: Policy{}})
	if err != nil {
		t.Fatalf("RunDiagnostics: %v\n%s", err, buf.String())
	}
	out := buf.String()
	for _, want := range []string{
		"Extension: off\n",
		`X11: connected, active window 0x400001, WM_CLASS "Gnome-terminal", _GTK_APPLICATION_ID "org.gnome.Terminal"` + "\n",
		`Merged: app source: x11, app key: "org.gnome.terminal", wm_class key: "gnome-terminal", purpose: unknown` + "\n",
		"Policy: allowed\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	// A policy that needs the extension denies; the X11 identity is still
	// shown (strict purpose mode, WPX5, takes the same path).
	buf.Reset()
	strict := Policy{RequireExtension: true}
	err = RunDiagnostics(context.Background(), &buf, DiagOptions{X11Enabled: true, Policy: strict, Watch: 80 * time.Millisecond})
	if err != nil {
		t.Fatalf("RunDiagnostics(watch): %v\n%s", err, buf.String())
	}
	out = buf.String()
	_, reason := strict.Allow(State{Purpose: PurposeUnknown})
	for _, want := range []string{
		"Policy: denied (" + reason + ")\n",
		"Watching focus changes for 80ms...\n",
		`Merged: app source: x11, app key: "org.gnome.terminal", wm_class key: "gnome-terminal", purpose: unknown; Policy: denied (` + reason + ")\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestRunDiagnosticsX11Error(t *testing.T) {
	saved := x11Dial
	t.Cleanup(func() { x11Dial = saved })
	boom := errors.New("boom")
	x11Dial = func(context.Context, string) (x11Conn, error) { return nil, boom }

	var buf bytes.Buffer
	err := RunDiagnostics(context.Background(), &buf, DiagOptions{X11Enabled: true, Watch: time.Second})
	if !errors.Is(err, boom) {
		t.Fatalf("RunDiagnostics error = %v, want boom", err)
	}
	out := buf.String()
	for _, want := range []string{"X11: error: connect to X11: boom\n", "Merged: app source: none", "Policy: allowed\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Watching") {
		t.Errorf("watch started after a connection error:\n%s", out)
	}
}
