package layoutstate

import (
	"context"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jezek/xgb"

	"github.com/arumata/gswitch/internal/gswitch/x11conn/x11conntest"
)

func init() {
	xgb.Logger.SetOutput(io.Discard) // "Could not get authority info" for the fake servers
}

// A display whose screen number the server does not have (DISPLAY=:0.1 on a
// one-screen server) is a connect error: dialX11 used to index
// xproto.SetupInfo.Roots unchecked and panic the whole daemon.
func TestDialX11NonexistentScreenIsAnError(t *testing.T) {
	srv := x11conntest.New(t, ".1", x11conntest.OneScreen)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := dialX11(ctx, srv.Display)
	if err == nil {
		conn.Close()
		t.Fatal("dialX11(screen 1 of a one-screen server) error = nil")
	}
	if !strings.Contains(err.Error(), "default screen 1") {
		t.Fatalf("dialX11() error = %v, want the screen-range error", err)
	}

	src := NewX11Source(X11Options{Display: srv.Display, Fallback: usRu, ReplyTimeout: 200 * time.Millisecond})
	if _, err := src.Query(ctx); err == nil || !strings.Contains(err.Error(), "default screen 1") {
		t.Fatalf("X11Source.Query() error = %v, want the screen-range error", err)
	}

	ok := x11conntest.New(t, "", x11conntest.OneScreen)
	conn, err = dialX11(ctx, ok.Display)
	if err != nil {
		t.Fatalf("dialX11(screen 0) error = %v", err)
	}
	defer conn.Close()
	if got := conn.(*xgbConn).root; got != x11conntest.RootWindow {
		t.Fatalf("root = 0x%x, want 0x%x", got, x11conntest.RootWindow)
	}
}

// Against a server that accepts connections but never answers the setup,
// every connect attempt of Run times out and leaves nothing behind: no
// goroutine blocked in the handshake, no open socket (it used to leak one of
// each per attempt).
func TestX11SourceRunSilentServerLeaksNothing(t *testing.T) {
	srv := x11conntest.New(t, "", x11conntest.Silent)
	before := runtime.NumGoroutine()
	for range 3 {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		_, err := dialX11(ctx, srv.Display)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("dialX11(silent server) error = %v, want context.DeadlineExceeded", err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		conns, closed := srv.Counts()
		if conns == 3 && closed == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server: %d connections, %d closed by the client; want 3 and 3", conns, closed)
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+1 {
		t.Fatalf("goroutines: %d before, %d after three timed-out connects", before, after)
	}
}
