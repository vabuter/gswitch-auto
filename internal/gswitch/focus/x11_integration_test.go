package focus

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
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

	probe, err := probeX11(context.Background(), "")
	if err != nil {
		t.Fatalf("probeX11: %v", err)
	}
	if !probe.Connected {
		t.Fatalf("probe not connected: %+v", probe)
	}
	t.Logf("active window 0x%x, WM_CLASS %q, _GTK_APPLICATION_ID %q", probe.Window, probe.WmClass, probe.AppID)
	x11FocusTestReadClients(t)

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

// x11FocusTestReadClients runs the source's identity reads (WM_CLASS as
// STRING, _GTK_APPLICATION_ID as UTF8_STRING, 256 units) on the managed
// windows of _NET_CLIENT_LIST: the active window may have no WM_CLASS (on
// the Fedora host's Xwayland it is a Mutter helper window), and under Xvfb
// without a window manager the list is empty. A window closing meanwhile
// (BadWindow) is skipped; any other error fails.
func x11FocusTestReadClients(t *testing.T) {
	t.Helper()
	sess, err := openX11Session(context.Background(), x11Dial, "")
	if err != nil {
		t.Fatalf("open X11 session: %v", err)
	}
	defer sess.close()
	atomClients, err := sess.conn.InternAtom("_NET_CLIENT_LIST")
	if err != nil {
		t.Fatalf("intern _NET_CLIENT_LIST: %v", err)
	}
	list, err := sess.conn.GetProperty(sess.root, atomClients, uint32(xproto.AtomWindow), x11PropertyMaxLen)
	if err != nil {
		t.Fatalf("read _NET_CLIENT_LIST: %v", err)
	}
	const maxClients = 16
	for i := 0; i+4 <= len(list) && i < 4*maxClients; i += 4 {
		window := xgb.Get32(list[i:])
		wmClass, err := sess.conn.GetProperty(window, uint32(xproto.AtomWmClass), uint32(xproto.AtomString), x11PropertyMaxLen)
		if errors.Is(err, errX11BadWindow) {
			continue
		}
		if err != nil {
			t.Fatalf("read WM_CLASS of client 0x%x: %v", window, err)
		}
		appID, err := sess.conn.GetProperty(window, sess.atomGtkAppID, sess.atomUTF8, x11PropertyMaxLen)
		if errors.Is(err, errX11BadWindow) {
			continue
		}
		if err != nil {
			t.Fatalf("read _GTK_APPLICATION_ID of client 0x%x: %v", window, err)
		}
		t.Logf("client 0x%x: WM_CLASS class %q, _GTK_APPLICATION_ID %q", window, ParseWMClass(wmClass), x11String(appID))
	}
}
