package focus

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

const focusTestWait = 3 * time.Second

const focusTestBusConfig = `<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:dir=%s</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
`

// focusTestBus starts a private dbus-daemon without service activation and
// returns its address and a function that kills it. The test is skipped when
// dbus-daemon is not installed.
func focusTestBus(t *testing.T) (string, func()) {
	t.Helper()
	daemon, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon not available")
	}
	dir, err := os.MkdirTemp("", "gsfocus")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	config := filepath.Join(dir, "bus.conf")
	if err := os.WriteFile(config, fmt.Appendf(nil, focusTestBusConfig, dir), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(daemon, "--config-file="+config, "--nofork", "--print-address=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("start dbus-daemon: %v", err)
	}
	var once sync.Once
	kill := func() {
		once.Do(func() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})
	}
	t.Cleanup(kill)

	lines := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		lines <- strings.TrimSpace(line)
	}()
	select {
	case addr := <-lines:
		if addr == "" {
			t.Fatal("dbus-daemon printed no address")
		}
		return addr, kill
	case <-time.After(focusTestWait):
		t.Fatal("dbus-daemon did not print its address")
	}
	return "", kill
}

func focusTestConnect(t *testing.T, addr string) *dbus.Conn {
	t.Helper()
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatalf("connect %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func focusTestOwn(t *testing.T, conn *dbus.Conn, name string) {
	t.Helper()
	reply, err := conn.RequestName(name, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("RequestName(%s) = %v, %v", name, reply, err)
	}
}

func focusTestRelease(t *testing.T, conn *dbus.Conn, name string) {
	t.Helper()
	if _, err := conn.ReleaseName(name); err != nil {
		t.Fatalf("ReleaseName(%s): %v", name, err)
	}
}

// focusTestExtension is a fake of the Shell extension's D-Bus object.
type focusTestExtension struct {
	conn *dbus.Conn

	mu                   sync.Mutex
	appID, wmClass, purp string
	apps                 []App
	block                chan struct{} // GetFocus waits for it when non-nil
	getFocusCalls        int
}

func (f *focusTestExtension) GetFocus() (string, string, string, *dbus.Error) {
	f.mu.Lock()
	f.getFocusCalls++
	block := f.block
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.appID, f.wmClass, f.purp, nil
}

func (f *focusTestExtension) ListRunningApps() ([]App, *dbus.Error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.apps, nil
}

// focusTestNewExtension exports a fake extension; configure runs before the
// export so that the fake's call goroutines observe its fields.
func focusTestNewExtension(t *testing.T, addr string, appID, wmClass, purpose string,
	configure ...func(*focusTestExtension),
) *focusTestExtension {
	t.Helper()
	f := &focusTestExtension{conn: focusTestConnect(t, addr), appID: appID, wmClass: wmClass, purp: purpose}
	for _, fn := range configure {
		fn(f)
	}
	if err := f.conn.Export(f, ObjectPath, Interface); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *focusTestExtension) emit(t *testing.T, member string, args ...any) {
	t.Helper()
	if err := f.conn.Emit(ObjectPath, Interface+"."+member, args...); err != nil {
		t.Fatalf("emit %s: %v", member, err)
	}
}

// focusTestScreenSaver is a fake of org.gnome.ScreenSaver.
type focusTestScreenSaver struct {
	conn   *dbus.Conn
	mu     sync.Mutex
	active bool
}

func (s *focusTestScreenSaver) GetActive() (bool, *dbus.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active, nil
}

func focusTestNewScreenSaver(t *testing.T, addr string, active bool) *focusTestScreenSaver {
	t.Helper()
	s := &focusTestScreenSaver{conn: focusTestConnect(t, addr), active: active}
	if err := s.conn.Export(s, ScreenSaverPath, ScreenSaverName); err != nil {
		t.Fatal(err)
	}
	focusTestOwn(t, s.conn, ScreenSaverName)
	return s
}

func (s *focusTestScreenSaver) setActive(t *testing.T, active bool) {
	t.Helper()
	s.mu.Lock()
	s.active = active
	s.mu.Unlock()
	if err := s.conn.Emit(ScreenSaverPath, ScreenSaverName+".ActiveChanged", active); err != nil {
		t.Fatal(err)
	}
}

// focusTestRecorder collects OnChange notifications.
type focusTestRecorder struct {
	mu     sync.Mutex
	states []State
}

func (r *focusTestRecorder) onChange(st State) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states = append(r.states, st)
}

func (r *focusTestRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.states)
}

func focusTestStartClient(t *testing.T, addr string, rec *focusTestRecorder, timeout time.Duration) *Client {
	t.Helper()
	opts := Options{Address: addr, CallTimeout: timeout, Logf: t.Logf}
	if rec != nil {
		opts.OnChange = rec.onChange
	}
	c := New(opts)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		c.Close()
		cancel()
	})
	c.Start(ctx)
	return c
}

func focusTestWaitFor(t *testing.T, c *Client, what string, cond func(State) bool) State {
	t.Helper()
	deadline := time.Now().Add(focusTestWait)
	for {
		st := c.Snapshot()
		if cond(st) {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; state %+v", what, st)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// focusTestSettle waits until no OnChange arrives for a short while.
func focusTestSettle(rec *focusTestRecorder) {
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

func TestClientExtensionPresentAtStart(t *testing.T) {
	addr, _ := focusTestBus(t)
	ext := focusTestNewExtension(t, addr, "org.gnome.TextEditor.desktop", "org.gnome.TextEditor", "normal")
	focusTestOwn(t, ext.conn, BusName)
	focusTestNewScreenSaver(t, addr, false)

	c := focusTestStartClient(t, addr, nil, 0)
	st := focusTestWaitFor(t, c, "available", func(st State) bool { return st.Available })
	want := State{
		Available: true, EverAvailable: true,
		AppID: "org.gnome.TextEditor.desktop", WmClass: "org.gnome.TextEditor",
		AppKey: "org.gnome.texteditor", WmClassKey: "org.gnome.texteditor",
		Purpose: PurposeNormal,
	}
	if !sameState(st, want) {
		t.Fatalf("state = %+v, want %+v", st, want)
	}
	if st.Seq == 0 || st.UpdatedAt.IsZero() {
		t.Fatalf("Seq/UpdatedAt not set: %+v", st)
	}
}

func TestClientExtensionAppearsLater(t *testing.T) {
	addr, _ := focusTestBus(t)
	rec := &focusTestRecorder{}
	c := focusTestStartClient(t, addr, rec, 0)

	// Give the client time to connect and find the name unowned.
	time.Sleep(150 * time.Millisecond)
	if st := c.Snapshot(); st.Available || st.EverAvailable || st.Locked {
		t.Fatalf("state before the extension appeared = %+v", st)
	}

	ext := focusTestNewExtension(t, addr, "org.gnome.Ptyxis.desktop", "org.gnome.Ptyxis", "terminal")
	focusTestOwn(t, ext.conn, BusName)
	st := focusTestWaitFor(t, c, "extension to appear", func(st State) bool { return st.Available && st.AppID != "" })
	if !st.EverAvailable || st.Purpose != PurposeTerminal || st.AppKey != "org.gnome.ptyxis" {
		t.Fatalf("state after appearance = %+v", st)
	}
}

// TestClientAddsMatchesBeforeInitialQueries watches the bus as a monitor and
// checks that the client installs all match rules before it asks whether
// the extension or the screen saver exist.
func TestClientAddsMatchesBeforeInitialQueries(t *testing.T) {
	addr, _ := focusTestBus(t)
	monitor := focusTestConnect(t, addr)
	err := monitor.BusObject().Call("org.freedesktop.DBus.Monitoring.BecomeMonitor", 0,
		[]string{"type='method_call'"}, uint32(0)).Store()
	if err != nil {
		t.Skipf("BecomeMonitor: %v", err)
	}
	msgs := make(chan *dbus.Message, 256)
	monitor.Eavesdrop(msgs)

	ext := focusTestNewExtension(t, addr, "a.desktop", "A", "normal")
	focusTestOwn(t, ext.conn, BusName)
	focusTestNewScreenSaver(t, addr, false)
	c := focusTestStartClient(t, addr, nil, 0)
	focusTestWaitFor(t, c, "available", func(st State) bool { return st.Available })

	var order []string
	timeout := time.After(focusTestWait)
	for len(order) < 6 {
		select {
		case msg := <-msgs:
			member, _ := msg.Headers[dbus.FieldMember].Value().(string)
			switch member {
			case "AddMatch", "NameHasOwner", "GetFocus", "GetActive":
				order = append(order, member)
			}
		case <-timeout:
			t.Fatalf("observed calls %q, want 3 AddMatch, NameHasOwner, GetFocus, GetActive", order)
		}
	}
	want := []string{"AddMatch", "AddMatch", "AddMatch", "NameHasOwner", "GetFocus", "GetActive"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("call order = %q, want %q", order, want)
	}
}

func TestClientFocusChangedBumpsSeqOnce(t *testing.T) {
	addr, _ := focusTestBus(t)
	ext := focusTestNewExtension(t, addr, "org.gnome.TextEditor.desktop", "org.gnome.TextEditor", "normal")
	focusTestOwn(t, ext.conn, BusName)
	rec := &focusTestRecorder{}
	c := focusTestStartClient(t, addr, rec, 0)
	focusTestWaitFor(t, c, "available", func(st State) bool { return st.Available })
	focusTestSettle(rec)
	before, calls := c.Snapshot(), rec.count()

	ext.emit(t, "FocusChanged", "org.gnome.Ptyxis.desktop", "org.gnome.Ptyxis")
	st := focusTestWaitFor(t, c, "focus change", func(st State) bool { return st.AppID == "org.gnome.Ptyxis.desktop" })
	focusTestSettle(rec)
	if st.Seq != before.Seq+1 || rec.count() != calls+1 {
		t.Fatalf("Seq %d -> %d, OnChange calls %d -> %d; want one bump and one call",
			before.Seq, st.Seq, calls, rec.count())
	}
	if st.AppKey != "org.gnome.ptyxis" || st.WmClassKey != "org.gnome.ptyxis" || st.WmClass != "org.gnome.Ptyxis" {
		t.Fatalf("keys not normalised: %+v", st)
	}
	if st.Purpose != PurposeNormal {
		t.Fatalf("FocusChanged altered the purpose: %+v", st)
	}

	// Another window of the same application: identical values, new Seq.
	ext.emit(t, "FocusChanged", "org.gnome.Ptyxis.desktop", "org.gnome.Ptyxis")
	focusTestWaitFor(t, c, "same-app focus change", func(s State) bool { return s.Seq == st.Seq+1 })
}

func TestClientInputPurposeChanged(t *testing.T) {
	addr, _ := focusTestBus(t)
	ext := focusTestNewExtension(t, addr, "org.gnome.Settings.desktop", "gnome-control-center", "normal")
	focusTestOwn(t, ext.conn, BusName)
	rec := &focusTestRecorder{}
	c := focusTestStartClient(t, addr, rec, 0)
	before := focusTestWaitFor(t, c, "available", func(st State) bool { return st.Available })

	ext.emit(t, "InputPurposeChanged", "password")
	st := focusTestWaitFor(t, c, "password purpose", func(st State) bool { return st.Purpose == PurposePassword })
	if st.Seq != before.Seq+1 {
		t.Fatalf("Seq %d -> %d, want one bump", before.Seq, st.Seq)
	}

	// A repeated purpose is not a change; an unknown string maps to unknown.
	ext.emit(t, "InputPurposeChanged", "password")
	ext.emit(t, "InputPurposeChanged", "bogus")
	st = focusTestWaitFor(t, c, "unknown purpose", func(st State) bool { return st.Purpose == PurposeUnknown })
	if st.Seq != before.Seq+2 {
		t.Fatalf("Seq %d -> %d, want two bumps", before.Seq, st.Seq)
	}
}

func TestClientLockedThenVanishThenReappear(t *testing.T) {
	addr, _ := focusTestBus(t)
	ext := focusTestNewExtension(t, addr, "a.desktop", "A", "normal")
	focusTestOwn(t, ext.conn, BusName)
	c := focusTestStartClient(t, addr, nil, 0)
	focusTestWaitFor(t, c, "available", func(st State) bool { return st.Available })

	ext.emit(t, "Disabling", "locked")
	st := focusTestWaitFor(t, c, "extension lock", func(st State) bool { return st.Locked })
	if st.LockReason != LockReasonExtensionLocked {
		t.Fatalf("lock reason = %q", st.LockReason)
	}
	focusTestRelease(t, ext.conn, BusName)
	st = focusTestWaitFor(t, c, "vanish", func(st State) bool { return !st.Available })
	if !st.Locked || st.LockReason != LockReasonExtensionLocked || st.AppID != "" || st.Purpose != PurposeUnknown {
		t.Fatalf("state after locked vanish = %+v", st)
	}

	focusTestOwn(t, ext.conn, BusName)
	st = focusTestWaitFor(t, c, "reappear", func(st State) bool { return st.Available })
	if st.Locked || st.LockReason != "" || st.AppID != "a.desktop" {
		t.Fatalf("state after reappearance = %+v", st)
	}
}

func TestClientVanishWithoutDisablingFailsClosed(t *testing.T) {
	addr, _ := focusTestBus(t)
	ext := focusTestNewExtension(t, addr, "a.desktop", "A", "normal")
	focusTestOwn(t, ext.conn, BusName)
	c := focusTestStartClient(t, addr, nil, 0)
	focusTestWaitFor(t, c, "available", func(st State) bool { return st.Available })

	focusTestRelease(t, ext.conn, BusName)
	st := focusTestWaitFor(t, c, "vanish", func(st State) bool { return !st.Available })
	if !st.Locked || st.LockReason != LockReasonExtensionVanished || !st.EverAvailable {
		t.Fatalf("state after vanish = %+v", st)
	}

	focusTestOwn(t, ext.conn, BusName)
	st = focusTestWaitFor(t, c, "reappear", func(st State) bool { return st.Available })
	if st.Locked || st.LockReason != "" {
		t.Fatalf("state after reappearance = %+v", st)
	}
}

func TestClientDisablingDisabledIsNotLocked(t *testing.T) {
	addr, _ := focusTestBus(t)
	ext := focusTestNewExtension(t, addr, "a.desktop", "A", "normal")
	focusTestOwn(t, ext.conn, BusName)
	c := focusTestStartClient(t, addr, nil, 0)
	focusTestWaitFor(t, c, "available", func(st State) bool { return st.Available })

	ext.emit(t, "Disabling", "disabled")
	st := focusTestWaitFor(t, c, "disabled", func(st State) bool { return !st.Available })
	if st.Locked || st.AppID != "" || st.Purpose != PurposeUnknown || !st.EverAvailable {
		t.Fatalf("state after Disabling(disabled) = %+v", st)
	}
	focusTestRelease(t, ext.conn, BusName)
	time.Sleep(100 * time.Millisecond)
	if st := c.Snapshot(); st.Locked || st.Available {
		t.Fatalf("state after the vanish that followed Disabling(disabled) = %+v", st)
	}
}

func TestClientScreenSaverLock(t *testing.T) {
	addr, _ := focusTestBus(t)
	ss := focusTestNewScreenSaver(t, addr, true)
	ext := focusTestNewExtension(t, addr, "a.desktop", "A", "normal")
	focusTestOwn(t, ext.conn, BusName)
	c := focusTestStartClient(t, addr, nil, 0)

	st := focusTestWaitFor(t, c, "screensaver lock at start", func(st State) bool { return st.Available && st.Locked })
	if st.LockReason != LockReasonScreenSaver {
		t.Fatalf("lock reason = %q", st.LockReason)
	}
	ss.setActive(t, false)
	focusTestWaitFor(t, c, "unlock", func(st State) bool { return !st.Locked && st.LockReason == "" })

	// The extension lock wins while it lasts; the screensaver clears only
	// its own lock, and a reappearance under an active screensaver keeps the
	// session locked.
	ext.emit(t, "Disabling", "locked")
	focusTestWaitFor(t, c, "extension lock", func(st State) bool { return st.LockReason == LockReasonExtensionLocked })
	ss.setActive(t, true)
	focusTestRelease(t, ext.conn, BusName)
	focusTestWaitFor(t, c, "vanish", func(st State) bool { return !st.Available })
	if st := c.Snapshot(); st.LockReason != LockReasonExtensionLocked {
		t.Fatalf("screensaver replaced the extension lock: %+v", st)
	}
	focusTestOwn(t, ext.conn, BusName)
	st = focusTestWaitFor(t, c, "reappear", func(st State) bool { return st.Available })
	if !st.Locked || st.LockReason != LockReasonScreenSaver {
		t.Fatalf("state after reappearance under an active screensaver = %+v", st)
	}
	ss.setActive(t, false)
	focusTestWaitFor(t, c, "final unlock", func(st State) bool { return !st.Locked })
}

func TestClientGetFocusTimeout(t *testing.T) {
	addr, _ := focusTestBus(t)
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	ext := focusTestNewExtension(t, addr, "a.desktop", "A", "normal",
		func(f *focusTestExtension) { f.block = block })
	focusTestOwn(t, ext.conn, BusName)

	c := New(Options{Address: addr, CallTimeout: 100 * time.Millisecond, Logf: t.Logf})
	start := time.Now()
	c.Start(context.Background())
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("Start blocked for %v", elapsed)
	}
	st := focusTestWaitFor(t, c, "available after GetFocus timeout", func(st State) bool { return st.Available })
	if st.AppID != "" || st.Purpose != PurposeUnknown {
		t.Fatalf("state after GetFocus timeout = %+v", st)
	}

	// The client still follows signals after the failed call.
	ext.emit(t, "FocusChanged", "b.desktop", "B")
	focusTestWaitFor(t, c, "focus after timeout", func(st State) bool { return st.AppID == "b.desktop" })

	start = time.Now()
	c.Close()
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("Close took %v", elapsed)
	}
}

func TestClientCloseDuringBlockedCall(t *testing.T) {
	addr, _ := focusTestBus(t)
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	ext := focusTestNewExtension(t, addr, "a.desktop", "A", "normal",
		func(f *focusTestExtension) { f.block = block })
	focusTestOwn(t, ext.conn, BusName)

	c := New(Options{Address: addr, CallTimeout: 10 * time.Second, Logf: t.Logf})
	c.Start(context.Background())
	deadline := time.Now().Add(focusTestWait)
	for {
		ext.mu.Lock()
		calls := ext.getFocusCalls
		ext.mu.Unlock()
		if calls > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("client never called GetFocus")
		}
		time.Sleep(5 * time.Millisecond)
	}
	start := time.Now()
	c.Close()
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("Close took %v with a blocked call", elapsed)
	}
}

func TestListRunningApps(t *testing.T) {
	addr, _ := focusTestBus(t)
	conn := focusTestConnect(t, addr)

	if _, err := ListRunningApps(context.Background(), conn); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ListRunningApps without the extension: err = %v, want ErrUnavailable", err)
	}

	want := []App{
		{ID: "org.gnome.Ptyxis.desktop", Name: "Ptyxis", WmClass: "org.gnome.Ptyxis"},
		{ID: "window:3", Name: "Foo", WmClass: "Foo"},
	}
	ext := focusTestNewExtension(t, addr, "", "", "none",
		func(f *focusTestExtension) { f.apps = want })
	focusTestOwn(t, ext.conn, BusName)
	apps, err := ListRunningApps(context.Background(), conn)
	if err != nil {
		t.Fatalf("ListRunningApps: %v", err)
	}
	if !reflect.DeepEqual(apps, want) {
		t.Fatalf("apps = %+v, want %+v", apps, want)
	}

	if _, err := ListRunningApps(context.Background(), nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ListRunningApps(nil conn): err = %v, want ErrUnavailable", err)
	}
}

func TestClientWithoutBus(t *testing.T) {
	c := New(Options{Address: "unix:path=/nonexistent/gswitch-focus-test/bus"})
	c.Close() // before Start: no-op
	c.Start(context.Background())
	if st := c.Snapshot(); st.Available || st.Purpose != PurposeUnknown {
		t.Fatalf("closed client state = %+v", st)
	}

	c = New(Options{Address: "unix:path=/nonexistent/gswitch-focus-test/bus", Logf: t.Logf})
	c.Start(context.Background())
	c.Start(context.Background()) // second Start: no-op
	time.Sleep(50 * time.Millisecond)
	if st := c.Snapshot(); st.Available || st.Locked {
		t.Fatalf("state without a bus = %+v", st)
	}
	start := time.Now()
	c.Close()
	c.Close()
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("Close took %v", elapsed)
	}
}

func TestClientBusLoss(t *testing.T) {
	addr, kill := focusTestBus(t)
	ext := focusTestNewExtension(t, addr, "a.desktop", "A", "normal")
	focusTestOwn(t, ext.conn, BusName)
	c := focusTestStartClient(t, addr, nil, 0)
	focusTestWaitFor(t, c, "available", func(st State) bool { return st.Available })

	kill()
	st := focusTestWaitFor(t, c, "disconnect", func(st State) bool { return !st.Available })
	if st.Locked || st.AppID != "" {
		t.Fatalf("state after bus loss = %+v", st)
	}
	start := time.Now()
	c.Close()
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("Close took %v", elapsed)
	}
}

func TestPackageNeverUsesSharedSessionBus(t *testing.T) {
	for _, name := range []string{"client.go", "state.go", "policy.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "dbus" && sel.Sel.Name == "SessionBus" {
				t.Errorf("%s uses dbus.SessionBus; fork clients use private connections (C27)", name)
			}
			return true
		})
	}
}

func TestClientStopsWithContext(t *testing.T) {
	addr, _ := focusTestBus(t)
	c := New(Options{Address: addr})
	ctx, cancel := context.WithCancel(context.Background())
	c.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-c.done:
	case <-time.After(time.Second):
		t.Fatal("client goroutine did not stop after the context was cancelled")
	}
	c.Close()
}
