package x11conn

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/arumata/gswitch/internal/gswitch/x11conn/x11conntest"
)

func init() {
	// xgb logs "Could not get authority info" for the fake servers.
	xgb.Logger.SetOutput(io.Discard)
}

func TestParseDisplay(t *testing.T) {
	tests := []struct {
		in                     string
		network, address, host string
		display                string
		number, screen         int
	}{
		{":0", "unix", "/tmp/.X11-unix/X0", "", "0", 0, 0},
		{":1.2", "unix", "/tmp/.X11-unix/X1", "", "1", 1, 2},
		{"unix:3", "unix", "/tmp/.X11-unix/X3", "", "3", 3, 0},
		{"/tmp/launch-1/org.x:0", "unix", "/tmp/launch-1/org.x:0", "", "0", 0, 0},
		{"host:2.1", "tcp", "host:6002", "host", "2", 2, 1},
		{"tcp/host:1.0", "tcp", "host:6001", "host", "1", 1, 0},
		{":0.-1", "unix", "/tmp/.X11-unix/X0", "", "0", 0, -1},
	}
	for _, tt := range tests {
		got, err := ParseDisplay(tt.in)
		if err != nil {
			t.Fatalf("ParseDisplay(%q) error = %v", tt.in, err)
		}
		if got.Network != tt.network || got.Address != tt.address || got.Host != tt.host ||
			got.Display != tt.display || got.DisplayNumber != tt.number || got.Screen != tt.screen {
			t.Errorf("ParseDisplay(%q) = %+v", tt.in, got)
		}
	}
	for _, bad := range []string{"nocolon", "host:", ":x", ":-1", ":0.x"} {
		if _, err := ParseDisplay(bad); err == nil {
			t.Errorf("ParseDisplay(%q) error = nil, want an error", bad)
		}
	}
	t.Setenv("DISPLAY", "")
	if _, err := ParseDisplay(""); err == nil {
		t.Error(`ParseDisplay("") with DISPLAY unset: error = nil`)
	}
}

func TestScreenRootBounds(t *testing.T) {
	setup := &xproto.SetupInfo{Roots: []xproto.ScreenInfo{{Root: 7}}}
	if root, err := ScreenRoot(setup, 0); err != nil || root != 7 {
		t.Fatalf("ScreenRoot(screen 0) = %d, %v; want 7, nil", root, err)
	}
	for _, screen := range []int{1, -1, 5} {
		if _, err := ScreenRoot(setup, screen); err == nil {
			t.Errorf("ScreenRoot(screen %d) of a one-screen server: error = nil", screen)
		}
	}
	if _, err := ScreenRoot(nil, 0); err == nil {
		t.Error("ScreenRoot(nil setup): error = nil")
	}
}

// A display whose screen number does not exist is an error, not a panic
// (xproto.SetupInfo.DefaultScreen indexes Roots unchecked).
func TestDialNonexistentScreen(t *testing.T) {
	srv := x11conntest.New(t, ".1", x11conntest.OneScreen)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := Dial(ctx, srv.Display)
	if err != nil {
		t.Fatalf("Dial(%s) error = %v", srv.Display, err)
	}
	defer conn.Close()
	if _, err := conn.DefaultRoot(); err == nil || !strings.Contains(err.Error(), "default screen 1") {
		t.Fatalf("DefaultRoot() error = %v, want the screen-range error", err)
	}

	srv0 := x11conntest.New(t, "", x11conntest.OneScreen)
	conn0, err := Dial(ctx, srv0.Display)
	if err != nil {
		t.Fatalf("Dial(%s) error = %v", srv0.Display, err)
	}
	defer conn0.Close()
	if root, err := conn0.DefaultRoot(); err != nil || root != x11conntest.RootWindow {
		t.Fatalf("DefaultRoot() = 0x%x, %v; want 0x1a2", root, err)
	}
}

// A server that accepts the socket but never answers the setup: Dial returns
// ctx's error in time, closes the socket and leaves no goroutine behind, so
// retrying cannot accumulate goroutines or descriptors.
func TestDialSilentServerIsBounded(t *testing.T) {
	srv := x11conntest.New(t, "", x11conntest.Silent)
	before := runtime.NumGoroutine()
	const attempts = 5
	for range attempts {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		start := time.Now()
		conn, err := Dial(ctx, srv.Display)
		cancel()
		if err == nil {
			conn.Close()
			t.Fatal("Dial() against a silent server succeeded")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Dial() error = %v, want context.DeadlineExceeded", err)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("Dial() took %v, want about 50 ms", elapsed)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		conns, closed := srv.Counts()
		if conns == attempts && closed == attempts {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server saw %d connections, %d closed by the client; want %d and %d", conns, closed, attempts, attempts)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The server's own handler goroutines have ended too; only stray client
	// goroutines could remain.
	time.Sleep(50 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+1 {
		t.Fatalf("goroutines: %d before, %d after %d timed-out dials", before, after, attempts)
	}
}

func TestDialCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Dial(ctx, ":0"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Dial(cancelled ctx) error = %v, want context.Canceled", err)
	}
}

// Close returns within CloseTimeout even when the server stopped answering,
// and closes the socket.
func TestCloseHungServerIsBounded(t *testing.T) {
	srv := x11conntest.New(t, "", x11conntest.OneScreen) // answers the setup, then nothing
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := Dial(ctx, srv.Display)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	start := time.Now()
	conn.Close()
	if elapsed := time.Since(start); elapsed > CloseTimeout+500*time.Millisecond {
		t.Fatalf("Close() took %v, want at most about %v", elapsed, CloseTimeout)
	}
	conn.Close() // idempotent
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, closed := srv.Counts(); closed == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the socket was not closed after Close()")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestFindAuthority(t *testing.T) {
	var buf bytes.Buffer
	entry := func(family uint16, addr, disp, name string, data []byte) {
		_ = binary.Write(&buf, binary.BigEndian, family)
		for _, f := range [][]byte{[]byte(addr), []byte(disp), []byte(name), data} {
			_ = binary.Write(&buf, binary.BigEndian, uint16(len(f)))
			buf.Write(f)
		}
	}
	cookie := bytes.Repeat([]byte{0xab}, 16)
	entry(256, "otherhost", "0", "MIT-MAGIC-COOKIE-1", []byte("wrong-host-cookie"))
	entry(256, "myhost", "1", "MIT-MAGIC-COOKIE-1", []byte("wrong-display-ck"))
	entry(256, "myhost", "0", "MIT-MAGIC-COOKIE-1", cookie)
	name, data, err := findAuthority(bytes.NewReader(buf.Bytes()), "myhost", "0", 256, 65535)
	if err != nil || name != "MIT-MAGIC-COOKIE-1" || !bytes.Equal(data, cookie) {
		t.Fatalf("findAuthority() = %q, %x, %v", name, data, err)
	}
	if _, _, err := findAuthority(bytes.NewReader(buf.Bytes()), "myhost", "7", 256, 65535); err == nil {
		t.Fatal("findAuthority(no matching display) error = nil")
	}
	buf.Reset()
	entry(65535, "", "", "MIT-MAGIC-COOKIE-1", cookie)
	if name, _, err := findAuthority(bytes.NewReader(buf.Bytes()), "anyhost", "3", 256, 65535); err != nil || name == "" {
		t.Fatalf("findAuthority(FamilyWild) = %q, %v", name, err)
	}
}

func TestReadAuthorityFile(t *testing.T) {
	dir := t.TempDir()
	host, err := os.Hostname()
	if err != nil {
		t.Skip(err)
	}
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.BigEndian, uint16(256))
	cookie := bytes.Repeat([]byte{1}, 16)
	for _, f := range [][]byte{[]byte(host), []byte("5"), []byte("MIT-MAGIC-COOKIE-1"), cookie} {
		_ = binary.Write(&buf, binary.BigEndian, uint16(len(f)))
		buf.Write(f)
	}
	path := filepath.Join(dir, "Xauthority")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XAUTHORITY", path)
	name, data, err := readAuthority("", "5")
	if err != nil || name != "MIT-MAGIC-COOKIE-1" || !bytes.Equal(data, cookie) {
		t.Fatalf("readAuthority() = %q, %x, %v", name, data, err)
	}
}
