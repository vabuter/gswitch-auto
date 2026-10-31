package focus

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jezek/xgb"

	"github.com/arumata/gswitch/internal/gswitch/x11conn"
	"github.com/arumata/gswitch/internal/gswitch/x11conn/x11conntest"
)

func init() {
	xgb.Logger.SetOutput(io.Discard) // "Could not get authority info" for the fake servers
}

// Against a server that accepts the socket but never answers the setup, the
// connect is bounded by its context.
func TestX11DialSilentServerIsBounded(t *testing.T) {
	srv := x11conntest.New(t, "", x11conntest.Silent)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := dialX11(ctx, srv.Display); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dialX11(silent server) error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("dialX11 took %v, want about 50 ms", elapsed)
	}
}

// A display whose screen number the server does not have is an error.
func TestX11DialNonexistentScreen(t *testing.T) {
	srv := x11conntest.New(t, ".3", x11conntest.OneScreen)
	if _, err := dialX11(context.Background(), srv.Display); err == nil || !strings.Contains(err.Error(), "default screen 3") {
		t.Fatalf("dialX11(screen 3 of a one-screen server) error = %v", err)
	}
}

// Close stops a source whose session is stuck connecting to a server that
// never answers (it used to block forever in xgb.NewConnDisplay, past
// Close's one-second wait, leaving the goroutine and socket behind).
func TestX11SourceCloseWhileConnectingToSilentServer(t *testing.T) {
	srv := x11conntest.New(t, "", x11conntest.Silent)
	var mu sync.Mutex
	var logs []string
	s := NewX11Source(X11Options{Display: srv.Display, Logf: func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, format)
	}})
	s.Start(context.Background())
	waitConn := time.Now().Add(2 * time.Second)
	for {
		if conns, _ := srv.Counts(); conns > 0 {
			break
		}
		if time.Now().After(waitConn) {
			t.Fatal("the source never connected to the fake server")
		}
		time.Sleep(5 * time.Millisecond)
	}
	start := time.Now()
	s.Close()
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("Close() took %v while the session was connecting", elapsed)
	}
	select {
	case <-s.done:
	default:
		t.Fatal("the source goroutine is still running after Close()")
	}
	mu.Lock()
	defer mu.Unlock()
	for _, l := range logs {
		if strings.Contains(l, "did not stop") {
			t.Fatalf("Close() logged %q", l)
		}
	}
}

// A request whose reply never comes fails with errX11ReplyTimeout after the
// reply timeout, and Close is bounded on that hung connection.
func TestX11XGBConnReplyTimeout(t *testing.T) {
	srv := x11conntest.New(t, "", x11conntest.OneScreen) // answers the setup only
	conn, err := dialX11(context.Background(), srv.Display)
	if err != nil {
		t.Fatalf("dialX11() error = %v", err)
	}
	xc := conn.(*x11XGBConn)
	if xc.Root() != x11conntest.RootWindow {
		t.Fatalf("Root() = 0x%x, want 0x%x", xc.Root(), x11conntest.RootWindow)
	}
	xc.replyTimeout = 50 * time.Millisecond
	for name, call := range map[string]func() error{
		"InternAtom": func() error { _, err := xc.InternAtom("_NET_ACTIVE_WINDOW"); return err },
		"GetProperty": func() error {
			_, err := xc.GetProperty(xc.Root(), 1, 2, 1)
			return err
		},
		"SelectPropertyChanges": func() error { return xc.SelectPropertyChanges(xc.Root()) },
	} {
		start := time.Now()
		if err := call(); !errors.Is(err, errX11ReplyTimeout) {
			t.Errorf("%s error = %v, want errX11ReplyTimeout", name, err)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Errorf("%s took %v, want about 50 ms", name, elapsed)
		}
	}
	start := time.Now()
	conn.Close()
	if elapsed := time.Since(start); elapsed > x11conn.CloseTimeout+500*time.Millisecond {
		t.Fatalf("Close() on a hung server took %v", elapsed)
	}
}

// A reply timeout during a read ends the session: the source publishes
// Connected=false instead of keeping the old identity, and reconnects.
func TestX11SourceReplyTimeoutEndsSession(t *testing.T) {
	conn := x11FocusTestTerminalConn()
	rec := &x11FocusTestRecorder{}
	s := x11FocusTestStart(t, x11FocusTestDialer(conn), rec)
	x11FocusTestWaitFor(t, s, "connected", func(st X11State) bool { return st.Connected && st.WmClass != "" })
	conn.setReadErr(fmt.Errorf("%w within 1s", errX11ReplyTimeout))
	conn.notify(x11FocusTestRoot, x11FocusTestAtomActive)
	x11FocusTestWaitFor(t, s, "disconnected", func(st X11State) bool { return !st.Connected })
}

// probeX11 (gswitch --focus-state) gives up when its context ends instead of
// hanging on a server that never answers (and ignoring Ctrl+C).
func TestProbeX11SilentServerHonoursContext(t *testing.T) {
	srv := x11conntest.New(t, "", x11conntest.Silent)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := probeX11(ctx, srv.Display); err == nil {
		t.Fatal("probeX11(silent server) error = nil")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("probeX11 took %v, want about 50 ms", elapsed)
	}
}
