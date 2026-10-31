package focus

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/arumata/gswitch/internal/gswitch/detect"
)

// Fake X connection for the EWMH source (WPX3 helpers carry the
// x11FocusTest prefix).

const (
	x11FocusTestRoot         uint32 = 0x1a4
	x11FocusTestAtomActive   uint32 = 300
	x11FocusTestAtomGtkAppID uint32 = 301
	x11FocusTestAtomUTF8     uint32 = 302
	x11FocusTestAtomOther    uint32 = 303 // e.g. _NET_CLIENT_LIST_STACKING

	x11FocusTestTerminal uint32 = 0x400001
	x11FocusTestEditor   uint32 = 0x400002
	x11FocusTestGone     uint32 = 0x400003 // never exists: BadWindow
)

type x11FocusTestWindow struct {
	wmClass, appID []byte
}

type x11FocusTestEvent struct {
	ev  x11PropertyNotify
	err error
}

// x11FocusTestConn is a fake x11Conn: a root window, a window table and an
// event queue the test feeds. Close (by the source or the test, to simulate
// a server loss) ends NextPropertyNotify with io.EOF.
type x11FocusTestConn struct {
	mu        sync.Mutex
	active    uint32
	hasActive bool
	windows   map[uint32]x11FocusTestWindow
	selected  []uint32
	readErr   error // injected GetProperty failure (not BadWindow)
	// blockReads makes GetProperty wait until the connection is closed and
	// then fail with io.EOF, like an xgb reply that never arrives; each
	// blocked read is announced on readBlocked.
	blockReads  bool
	readBlocked chan struct{}
	events      chan x11FocusTestEvent
	closed      chan struct{}
	closeOnce   sync.Once
}

func x11FocusTestNewConn() *x11FocusTestConn {
	return &x11FocusTestConn{
		windows:     map[uint32]x11FocusTestWindow{},
		readBlocked: make(chan struct{}, 1),
		events:      make(chan x11FocusTestEvent, 16),
		closed:      make(chan struct{}),
	}
}

// x11FocusTestTerminalConn returns a fake whose active window is a
// gnome-terminal window with a _GTK_APPLICATION_ID.
func x11FocusTestTerminalConn() *x11FocusTestConn {
	conn := x11FocusTestNewConn()
	conn.windows[x11FocusTestTerminal] = x11FocusTestWindow{
		wmClass: []byte("gnome-terminal-server\x00Gnome-terminal\x00"),
		appID:   []byte("org.gnome.Terminal"),
	}
	conn.windows[x11FocusTestEditor] = x11FocusTestWindow{wmClass: []byte("gedit\x00Gedit\x00")}
	conn.active, conn.hasActive = x11FocusTestTerminal, true
	return conn
}

func (c *x11FocusTestConn) Root() uint32 { return x11FocusTestRoot }

func (c *x11FocusTestConn) InternAtom(name string) (uint32, error) {
	switch name {
	case x11AtomNetActiveWindow:
		return x11FocusTestAtomActive, nil
	case x11AtomGtkApplicationID:
		return x11FocusTestAtomGtkAppID, nil
	case x11AtomUTF8String:
		return x11FocusTestAtomUTF8, nil
	default:
		return 0, fmt.Errorf("unexpected atom %q", name)
	}
}

func (c *x11FocusTestConn) SelectPropertyChanges(window uint32) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.selected = append(c.selected, window)
	return nil
}

func (c *x11FocusTestConn) GetProperty(window, property, typ, length uint32) ([]byte, error) {
	c.mu.Lock()
	if c.blockReads {
		c.mu.Unlock()
		c.readBlocked <- struct{}{}
		<-c.closed
		return nil, io.EOF
	}
	defer c.mu.Unlock()
	// SPEC-X11 §3.2.1 request sizes: one unit for the WINDOW value of
	// _NET_ACTIVE_WINDOW, 256 units for the window properties.
	wantLen := uint32(x11PropertyMaxLen)
	if window == x11FocusTestRoot {
		wantLen = 1
	}
	if length != wantLen {
		return nil, fmt.Errorf("GetProperty(0x%x, %d) asked for %d units, want %d", window, property, length, wantLen)
	}
	if c.readErr != nil {
		return nil, c.readErr
	}
	if window == x11FocusTestRoot {
		if property != x11FocusTestAtomActive || typ != uint32(xproto.AtomWindow) || !c.hasActive {
			return nil, nil
		}
		buf := make([]byte, 4)
		xgb.Put32(buf, c.active)
		return buf, nil
	}
	w, ok := c.windows[window]
	if !ok {
		return nil, fmt.Errorf("%w 0x%x", errX11BadWindow, window)
	}
	switch {
	case property == uint32(xproto.AtomWmClass) && typ == uint32(xproto.AtomString):
		return w.wmClass, nil
	case property == x11FocusTestAtomGtkAppID && typ == x11FocusTestAtomUTF8:
		return w.appID, nil
	default:
		return nil, nil
	}
}

func (c *x11FocusTestConn) NextPropertyNotify() (x11PropertyNotify, error) {
	select {
	case e := <-c.events:
		return e.ev, e.err
	case <-c.closed:
		return x11PropertyNotify{}, io.EOF
	}
}

func (c *x11FocusTestConn) Close() {
	c.closeOnce.Do(func() { close(c.closed) })
}

func (c *x11FocusTestConn) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

func (c *x11FocusTestConn) notify(window, atom uint32) {
	c.events <- x11FocusTestEvent{ev: x11PropertyNotify{Window: window, Atom: atom}}
}

// setActive changes _NET_ACTIVE_WINDOW and emits its PropertyNotify.
func (c *x11FocusTestConn) setActive(window uint32) {
	c.mu.Lock()
	c.active, c.hasActive = window, true
	c.mu.Unlock()
	c.notify(x11FocusTestRoot, x11FocusTestAtomActive)
}

func (c *x11FocusTestConn) setReadErr(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readErr = err
}

func (c *x11FocusTestConn) setBlockReads() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.blockReads = true
}

func x11FocusTestDialer(conns ...*x11FocusTestConn) func(string) (x11Conn, error) {
	var mu sync.Mutex
	calls := 0
	return func(string) (x11Conn, error) {
		mu.Lock()
		defer mu.Unlock()
		conn := conns[min(calls, len(conns)-1)]
		calls++
		return conn, nil
	}
}

type x11FocusTestRecorder struct {
	mu     sync.Mutex
	states []X11State
}

func (r *x11FocusTestRecorder) onChange(st X11State) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states = append(r.states, st)
}

func (r *x11FocusTestRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.states)
}

func (r *x11FocusTestRecorder) all() []X11State {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]X11State(nil), r.states...)
}

func x11FocusTestStart(t *testing.T, dial func(string) (x11Conn, error), rec *x11FocusTestRecorder) *X11Source {
	t.Helper()
	opts := X11Options{Display: ":99", Logf: t.Logf}
	if rec != nil {
		opts.OnChange = rec.onChange
	}
	s := NewX11Source(opts)
	s.dial = dial
	s.retryMin, s.retryMax = 10*time.Millisecond, 50*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		s.Close()
		cancel()
	})
	s.Start(ctx)
	return s
}

func x11FocusTestWaitFor(t *testing.T, s *X11Source, what string, cond func(X11State) bool) X11State {
	t.Helper()
	deadline := time.Now().Add(focusTestWait)
	for {
		st := s.Snapshot()
		if cond(st) {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; state %+v", what, st)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// x11FocusTestSettle waits until no OnChange arrives for a short while.
func x11FocusTestSettle(rec *x11FocusTestRecorder) {
	last := rec.count()
	for {
		time.Sleep(60 * time.Millisecond)
		n := rec.count()
		if n == last {
			return
		}
		last = n
	}
}

func x11FocusTestEqual(a, b X11State) bool {
	a.UpdatedAt, b.UpdatedAt = time.Time{}, time.Time{}
	return a == b
}

func x11FocusTestExpect(t *testing.T, s *X11Source, what string, want X11State) X11State {
	t.Helper()
	st := x11FocusTestWaitFor(t, s, what, func(st X11State) bool { return st.Seq >= want.Seq })
	if !x11FocusTestEqual(st, want) {
		t.Fatalf("%s: state = %+v, want %+v", what, st, want)
	}
	if st.UpdatedAt.IsZero() {
		t.Fatalf("%s: UpdatedAt not set", what)
	}
	return st
}

func TestX11SourceFollowsActiveWindow(t *testing.T) {
	conn := x11FocusTestTerminalConn()
	rec := &x11FocusTestRecorder{}
	s := x11FocusTestStart(t, x11FocusTestDialer(conn), rec)

	x11FocusTestExpect(t, s, "initial window", X11State{
		Connected: true, Window: x11FocusTestTerminal, AppID: "org.gnome.Terminal", WmClass: "Gnome-terminal", Seq: 1,
	})
	conn.mu.Lock()
	selected := append([]uint32(nil), conn.selected...)
	conn.mu.Unlock()
	if len(selected) != 1 || selected[0] != x11FocusTestRoot {
		t.Fatalf("PropertyNotify selected on %v, want [root]", selected)
	}

	// Another root property changes: nothing is re-read, nothing changes.
	conn.notify(x11FocusTestRoot, x11FocusTestAtomOther)
	conn.notify(x11FocusTestTerminal, x11FocusTestAtomActive)
	x11FocusTestSettle(rec)
	if n := rec.count(); n != 1 || s.Snapshot().Seq != 1 {
		t.Fatalf("after unrelated notifications: %d changes, Seq %d", n, s.Snapshot().Seq)
	}

	conn.setActive(x11FocusTestEditor)
	x11FocusTestExpect(t, s, "new window", X11State{Connected: true, Window: x11FocusTestEditor, WmClass: "Gedit", Seq: 2})

	// The active window closed before the read: BadWindow ⇒ no window.
	conn.setActive(x11FocusTestGone)
	x11FocusTestExpect(t, s, "bad window", X11State{Connected: true, Seq: 3})

	conn.setActive(x11FocusTestTerminal)
	x11FocusTestExpect(t, s, "back to the terminal", X11State{
		Connected: true, Window: x11FocusTestTerminal, AppID: "org.gnome.Terminal", WmClass: "Gnome-terminal", Seq: 4,
	})

	conn.setActive(0)
	x11FocusTestExpect(t, s, "window 0", X11State{Connected: true, Seq: 5})

	// An identical re-read is not a change.
	conn.setActive(0)
	x11FocusTestSettle(rec)
	if n := rec.count(); n != 5 {
		t.Fatalf("OnChange called %d times, want 5", n)
	}
	for i, st := range rec.all() {
		if st.Seq != uint64(i+1) {
			t.Fatalf("OnChange %d carried Seq %d", i, st.Seq)
		}
	}
}

// x11FocusTestRecorded checks the recorded OnChange states (UpdatedAt
// ignored) once the source has settled.
func x11FocusTestRecorded(t *testing.T, rec *x11FocusTestRecorder, want ...X11State) {
	t.Helper()
	x11FocusTestSettle(rec)
	got := rec.all()
	ok := len(got) == len(want)
	for i := 0; ok && i < len(got); i++ {
		ok = x11FocusTestEqual(got[i], want[i])
	}
	if !ok {
		t.Fatalf("OnChange states:\n got %+v\nwant %+v", got, want)
	}
}

// The reconnect after a lost connection is quick in the tests (10 ms), so
// the intermediate disconnect is checked in the recorded states rather than
// by polling the snapshot.
func TestX11SourceReconnectsOnEOF(t *testing.T) {
	first := x11FocusTestTerminalConn()
	second := x11FocusTestTerminalConn()
	second.active = x11FocusTestEditor
	rec := &x11FocusTestRecorder{}
	s := x11FocusTestStart(t, x11FocusTestDialer(first, second), rec)
	x11FocusTestWaitFor(t, s, "first connection", func(st X11State) bool { return st.Window == x11FocusTestTerminal })

	first.Close() // the server went away
	x11FocusTestExpect(t, s, "reconnected", X11State{Connected: true, Window: x11FocusTestEditor, WmClass: "Gedit", Seq: 3})
	x11FocusTestRecorded(t, rec,
		X11State{Connected: true, Window: x11FocusTestTerminal, AppID: "org.gnome.Terminal", WmClass: "Gnome-terminal", Seq: 1},
		X11State{Seq: 2},
		X11State{Connected: true, Window: x11FocusTestEditor, WmClass: "Gedit", Seq: 3},
	)
}

func TestX11SourceConnectionLostDuringRead(t *testing.T) {
	first := x11FocusTestTerminalConn()
	second := x11FocusTestTerminalConn()
	second.active = x11FocusTestEditor
	rec := &x11FocusTestRecorder{}
	s := x11FocusTestStart(t, x11FocusTestDialer(first, second), rec)
	x11FocusTestWaitFor(t, s, "first connection", func(st X11State) bool { return st.Window == x11FocusTestTerminal })

	// The server goes away while the source reads the new active window:
	// the reply fails with io.EOF. That read publishes nothing (no
	// "connected, no window" state); the session ends with one disconnect
	// and the source reconnects.
	first.setReadErr(fmt.Errorf("reply: %w", io.EOF))
	first.setActive(x11FocusTestEditor)
	x11FocusTestExpect(t, s, "reconnected", X11State{Connected: true, Window: x11FocusTestEditor, WmClass: "Gedit", Seq: 3})
	x11FocusTestRecorded(t, rec,
		X11State{Connected: true, Window: x11FocusTestTerminal, AppID: "org.gnome.Terminal", WmClass: "Gnome-terminal", Seq: 1},
		X11State{Seq: 2},
		X11State{Connected: true, Window: x11FocusTestEditor, WmClass: "Gedit", Seq: 3},
	)
	if !first.isClosed() {
		t.Fatal("the dead connection was not closed")
	}
}

func TestX11SourceShutdownDuringRead(t *testing.T) {
	conn := x11FocusTestTerminalConn()
	rec := &x11FocusTestRecorder{}
	s := NewX11Source(X11Options{Display: ":99", OnChange: rec.onChange, Logf: t.Logf})
	s.dial = x11FocusTestDialer(conn)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	x11FocusTestWaitFor(t, s, "connected", func(st X11State) bool { return st.Connected })

	// Close while a reply is outstanding: closing the connection fails the
	// read with io.EOF, which must not be published as a state.
	conn.setBlockReads()
	conn.setActive(x11FocusTestEditor)
	select {
	case <-conn.readBlocked:
	case <-time.After(focusTestWait):
		t.Fatal("the source did not read the new active window")
	}
	start := time.Now()
	s.Close()
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("Close took %v", elapsed)
	}
	select {
	case <-s.done:
	default:
		t.Fatal("source goroutine still running after Close")
	}
	if n := rec.count(); n != 1 {
		t.Fatalf("shutdown published %d extra changes: %+v", n-1, rec.all())
	}
	if st := s.Snapshot(); !st.Connected || st.Window != x11FocusTestTerminal || st.Seq != 1 {
		t.Fatalf("state after the shutdown = %+v", st)
	}
}

func TestX11SourceReadErrorsAndXErrors(t *testing.T) {
	conn := x11FocusTestTerminalConn()
	rec := &x11FocusTestRecorder{}
	s := x11FocusTestStart(t, x11FocusTestDialer(conn), rec)
	x11FocusTestWaitFor(t, s, "connected", func(st X11State) bool { return st.Connected })

	// A failing read keeps the connection but empties the identity.
	conn.setReadErr(errors.New("boom"))
	conn.setActive(x11FocusTestEditor)
	x11FocusTestExpect(t, s, "read error", X11State{Connected: true, Seq: 2})

	// An X error in the event stream is logged and the loop continues.
	conn.setReadErr(nil)
	conn.events <- x11FocusTestEvent{err: errors.New("BadAtom")}
	conn.setActive(x11FocusTestEditor)
	x11FocusTestExpect(t, s, "after the X error", X11State{Connected: true, Window: x11FocusTestEditor, WmClass: "Gedit", Seq: 3})
	if conn.isClosed() {
		t.Fatal("connection was closed by a read or X error")
	}
}

func TestX11SourceStartCloseLifecycle(t *testing.T) {
	s := NewX11Source(X11Options{Display: ":99"})
	s.dial = x11FocusTestDialer(x11FocusTestTerminalConn())
	s.Close() // before Start: no-op
	s.Start(context.Background())
	time.Sleep(20 * time.Millisecond)
	if st := s.Snapshot(); st.Connected || st.Seq != 0 {
		t.Fatalf("closed source connected: %+v", st)
	}

	conn := x11FocusTestTerminalConn()
	s = NewX11Source(X11Options{Display: ":99", Logf: t.Logf})
	s.dial = x11FocusTestDialer(conn)
	s.Start(context.Background())
	s.Start(context.Background()) // second Start: no-op
	x11FocusTestWaitFor(t, s, "connected", func(st X11State) bool { return st.Connected })
	start := time.Now()
	s.Close()
	s.Close()
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("Close took %v", elapsed)
	}
	if !conn.isClosed() {
		t.Fatal("Close did not close the X connection")
	}
	select {
	case <-s.done:
	default:
		t.Fatal("source goroutine still running after Close")
	}
}

func TestX11SourceWithoutDisplay(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	s := NewX11Source(X11Options{Logf: t.Logf})
	s.dial = func(string) (x11Conn, error) {
		mu.Lock()
		defer mu.Unlock()
		attempts++
		return nil, errors.New("DISPLAY is not set")
	}
	s.retryMin, s.retryMax = 5*time.Millisecond, 10*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	if st := s.Snapshot(); st.Connected || st.Seq != 0 {
		t.Fatalf("state without a display = %+v", st)
	}
	mu.Lock()
	n := attempts
	mu.Unlock()
	if n < 2 {
		t.Fatalf("dial attempted %d times, want retries", n)
	}
	start := time.Now()
	s.Close()
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("Close took %v", elapsed)
	}
}

func TestX11SourceStopsWithContext(t *testing.T) {
	conn := x11FocusTestTerminalConn()
	s := NewX11Source(X11Options{Display: ":99"})
	s.dial = x11FocusTestDialer(conn)
	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	x11FocusTestWaitFor(t, s, "connected", func(st X11State) bool { return st.Connected })
	cancel()
	select {
	case <-s.done:
	case <-time.After(time.Second):
		t.Fatal("source goroutine did not stop after the context was cancelled")
	}
	if st := s.Snapshot(); !st.Connected {
		t.Fatalf("shutdown published a disconnect: %+v", st)
	}
	s.Close()
}

func TestParseWMClass(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"instance and class", "gnome-terminal-server\x00Gnome-terminal\x00", "Gnome-terminal"},
		{"no trailing NUL", "gedit\x00Gedit", "Gedit"},
		{"single string", "xterm", ""},
		{"single string terminated", "xterm\x00", ""},
		{"empty", "", ""},
		{"empty class", "\x00\x00", ""},
		{"extra strings ignored", "a\x00B\x00c\x00", "B"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseWMClass([]byte(tt.value)); got != tt.want {
				t.Fatalf("ParseWMClass(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
	if got := x11String([]byte("org.gnome.Terminal\x00")); got != "org.gnome.Terminal" {
		t.Fatalf("x11String = %q", got)
	}
}

func TestX11FocusEnabled(t *testing.T) {
	x11 := &detect.SessionEnv{SessionType: "x11", Display: ":0"}
	wayland := &detect.SessionEnv{SessionType: "wayland", Display: ":0", WaylandDisplay: "wayland-0"}
	displayOnly := &detect.SessionEnv{Display: ":1"}
	xwayland := &detect.SessionEnv{SessionType: "x11", Display: ":0", WaylandDisplay: "wayland-0"}
	tests := []struct {
		name   string
		source string
		env    *detect.SessionEnv
		euid   int
		want   bool
	}{
		{"auto on X11", "auto", x11, 1000, true},
		{"auto on Wayland", "auto", wayland, 1000, false},
		{"auto with a display only", "auto", displayOnly, 1000, true},
		{"auto with WAYLAND_DISPLAY set", "auto", xwayland, 1000, false},
		{"root", "auto", x11, 0, false},
		{"off", "off", x11, 1000, false},
		{"unknown value", "x11", x11, 1000, false},
		{"empty env", "auto", &detect.SessionEnv{}, 1000, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := X11FocusEnabled(tt.source, tt.env, tt.euid); got != tt.want {
				t.Fatalf("X11FocusEnabled(%q, %+v, %d) = %v, want %v", tt.source, tt.env, tt.euid, got, tt.want)
			}
		})
	}

	// nil env: the process environment, DISPLAY alone never counts (K-X3).
	processEnv := []struct {
		name                 string
		sessionType, wayland string
		display              string
		want                 bool
	}{
		{"x11 session", "x11", "", ":0", true},
		{"x11 session without DISPLAY", "X11", "", "", true},
		{"wayland session", "wayland", "wayland-0", ":0", false},
		{"x11 type with WAYLAND_DISPLAY", "x11", "wayland-0", ":0", false},
		{"unknown type with DISPLAY", "", "", ":0", false},
		{"tty", "tty", "", "", false},
	}
	for _, tt := range processEnv {
		t.Run("process env "+tt.name, func(t *testing.T) {
			t.Setenv("XDG_SESSION_TYPE", tt.sessionType)
			t.Setenv("WAYLAND_DISPLAY", tt.wayland)
			t.Setenv("DISPLAY", tt.display)
			if got := X11FocusEnabled("auto", nil, 1000); got != tt.want {
				t.Fatalf("X11FocusEnabled(auto, nil) = %v, want %v", got, tt.want)
			}
		})
	}
}
