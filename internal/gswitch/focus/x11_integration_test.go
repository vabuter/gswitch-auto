package focus

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Gated integration test against a live X display (GSWITCH_X11_TEST=1).
// Read-only: it selects PropertyNotify on its own connection and reads
// _NET_ACTIVE_WINDOW, WM_CLASS and _GTK_APPLICATION_ID; nothing on the
// server changes. It passes on the Fedora host's Xwayland (:0) as well as
// under Xvfb in the Debian container (SPEC-X11 §6.2).

func TestX11SourceAgainstDisplay(t *testing.T) {
	if os.Getenv("GSWITCH_X11_TEST") != "1" {
		t.Skip("set GSWITCH_X11_TEST=1 to run against $DISPLAY")
	}
	if os.Getenv("DISPLAY") == "" {
		t.Skip("DISPLAY is not set")
	}

	probe, err := probeX11("")
	if err != nil {
		t.Fatalf("probeX11: %v", err)
	}
	if !probe.Connected {
		t.Fatalf("probe not connected: %+v", probe)
	}
	t.Logf("active window 0x%x, WM_CLASS %q, _GTK_APPLICATION_ID %q", probe.Window, probe.WmClass, probe.AppID)

	rec := &x11FocusTestRecorder{}
	s := NewX11Source(X11Options{OnChange: rec.onChange, Logf: t.Logf})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	st := x11FocusTestWaitFor(t, s, "connection", func(st X11State) bool { return st.Connected })
	if st.Seq != 1 || rec.count() < 1 {
		t.Fatalf("first state = %+v after %d notifications", st, rec.count())
	}
	if probe.Window != 0 && st.Window == probe.Window && (st.WmClass != probe.WmClass || st.AppID != probe.AppID) {
		t.Fatalf("source read %+v, probe read %+v for the same window", st, probe)
	}
	time.Sleep(200 * time.Millisecond)
	if st := s.Snapshot(); !st.Connected {
		t.Fatalf("source disconnected: %+v", st)
	}
	start := time.Now()
	s.Close()
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("Close took %v", elapsed)
	}

	var buf bytes.Buffer
	if err := RunDiagnostics(ctx, &buf, DiagOptions{X11Enabled: true, Policy: Policy{RequireExtension: true}}); err != nil {
		t.Fatalf("RunDiagnostics: %v\n%s", err, buf.String())
	}
	t.Logf("diagnostics:\n%s", buf.String())
	for _, want := range []string{"Extension: off\n", "X11: connected", "Merged: app source: x11", "Policy: denied (focus-unavailable)\n"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("diagnostics lack %q", want)
		}
	}
}
