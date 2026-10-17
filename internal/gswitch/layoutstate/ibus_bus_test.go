package layoutstate

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// These tests run a private dbus-daemon and a fake IBus service on it, so the
// real godbus code paths (Connect, AddMatch, Properties.Get, signal delivery,
// disconnect) are exercised without touching the user's IBus or session bus.

type testBus struct {
	addr string
	cmd  *exec.Cmd
	once sync.Once
}

func startTestBus(t *testing.T) *testBus {
	t.Helper()
	bin, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon not available")
	}
	dir, err := os.MkdirTemp("", "gsw-bus") // short path: unix socket paths are limited to 108 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	// A minimal configuration without <servicedir>: nothing can be
	// D-Bus-activated, so a stray call to org.freedesktop.IBus can never start
	// a real ibus-daemon (its session service file runs `ibus-daemon --replace`).
	config := filepath.Join(dir, "bus.conf")
	if err := os.WriteFile(config, []byte(testBusConfig(filepath.Join(dir, "bus"))), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "--config-file="+config, "--nofork", "--print-address=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start dbus-daemon: %v", err)
	}
	bus := &testBus{cmd: cmd}
	t.Cleanup(bus.stop)

	lines := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		lines <- strings.TrimSpace(line)
	}()
	select {
	case bus.addr = <-lines:
	case <-time.After(5 * time.Second):
	}
	if bus.addr == "" {
		bus.stop()
		t.Skipf("dbus-daemon printed no address: %s", stderr.String())
	}
	return bus
}

func testBusConfig(socket string) string {
	return `<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:path=` + socket + `</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
`
}

func (b *testBus) stop() {
	b.once.Do(func() {
		_ = b.cmd.Process.Kill()
		_ = b.cmd.Wait()
	})
}

// fakeIBus serves org.freedesktop.IBus on a test bus like ibus-daemon does on
// its private bus.
type fakeIBus struct {
	conn *dbus.Conn

	mu      sync.Mutex
	engine  string      // "" ⇒ "No global engine."
	propErr *dbus.Error // forced Properties.Get error
	calls   []string
}

type fakeIBusProps struct{ f *fakeIBus }

type fakeIBusMethods struct{ f *fakeIBus }

func startFakeIBus(t *testing.T, bus *testBus, engine string) *fakeIBus {
	t.Helper()
	conn, err := dbus.Connect(bus.addr)
	if err != nil {
		t.Fatalf("fake IBus connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	f := &fakeIBus{conn: conn, engine: engine}
	if err := conn.Export(fakeIBusProps{f}, ibusPath, "org.freedesktop.DBus.Properties"); err != nil {
		t.Fatal(err)
	}
	if err := conn.Export(fakeIBusMethods{f}, ibusPath, ibusInterface); err != nil {
		t.Fatal(err)
	}
	reply, err := conn.RequestName(ibusService, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("fake IBus RequestName = %v, %v", reply, err)
	}
	return f
}

func (p fakeIBusProps) Get(iface, prop string) (dbus.Variant, *dbus.Error) {
	f := p.f
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "Properties.Get "+iface+"."+prop)
	if f.propErr != nil {
		return dbus.Variant{}, f.propErr
	}
	if iface != ibusInterface || prop != ibusGlobalEngineProp {
		return dbus.Variant{}, &dbus.Error{Name: errUnknownProperty, Body: []any{"unknown property " + prop}}
	}
	if f.engine == "" {
		return dbus.Variant{}, &dbus.Error{Name: "org.freedesktop.DBus.Error.Failed", Body: []any{"No global engine."}}
	}
	// The property has type v, so the reply value is double-wrapped.
	return dbus.MakeVariant(dbus.MakeVariant(ibusDesc(f.engine))), nil
}

func (p fakeIBusProps) GetAll(string) (map[string]dbus.Variant, *dbus.Error) {
	return map[string]dbus.Variant{}, nil
}

func (p fakeIBusProps) Set(string, string, dbus.Variant) *dbus.Error {
	return &dbus.Error{Name: "org.freedesktop.DBus.Error.PropertyReadOnly", Body: []any{"read-only"}}
}

func (m fakeIBusMethods) GetGlobalEngine() (dbus.Variant, *dbus.Error) {
	f := m.f
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "GetGlobalEngine")
	if f.engine == "" {
		return dbus.Variant{}, &dbus.Error{Name: "org.freedesktop.DBus.Error.Failed", Body: []any{"No global engine."}}
	}
	return dbus.MakeVariant(ibusDesc(f.engine)), nil
}

func (f *fakeIBus) setEngine(t *testing.T, engine string) {
	t.Helper()
	f.mu.Lock()
	f.engine = engine
	f.mu.Unlock()
	if err := f.conn.Emit(ibusPath, ibusGlobalEngineChanged, engine); err != nil {
		t.Fatalf("emit GlobalEngineChanged: %v", err)
	}
}

func (f *fakeIBus) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// busTestSource returns an IBusSource that resolves to addr and retries fast.
func busTestSource(addr string) *IBusSource {
	src := NewIBusSource(AddressEnv{IBusAddress: addr}, nil)
	src.connectSession = nil
	src.sleep = fastSleep
	return src
}

func fastSleep(ctx context.Context, _ time.Duration, wake <-chan struct{}) (bool, bool) {
	return sleepOrWake(ctx, 5*time.Millisecond, wake)
}

func runSource(t *testing.T, src *IBusSource) (*recordingSink, context.CancelFunc) {
	t.Helper()
	sink := newRecordingSink()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		src.Run(ctx, sink)
	}()
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("IBusSource.Run did not return after cancel")
		}
	}
	t.Cleanup(stop)
	return sink, stop
}

func TestIBusSourceBusReadsPropertyAndFollowsSignals(t *testing.T) {
	bus := startTestBus(t)
	fake := startFakeIBus(t, bus, "xkb:us::eng")
	sink, _ := runSource(t, busTestSource(bus.addr))

	if ev := sink.next(t); ev != (sinkEvent{kind: "available", ok: true}) {
		t.Fatalf("first event = %+v, want Available(true)", ev)
	}
	if ev := sink.next(t); ev != (sinkEvent{kind: "engine", engine: "xkb:us::eng"}) {
		t.Fatalf("second event = %+v, want initial engine", ev)
	}
	if log := fake.callLog(); len(log) != 1 || log[0] != "Properties.Get org.freedesktop.IBus.GlobalEngine" {
		t.Fatalf("calls = %v, want only Properties.Get (no deprecated GetGlobalEngine)", log)
	}

	// Other signals on the same object and interface are ignored.
	if err := fake.conn.Emit(ibusPath, ibusInterface+".RegistryChanged"); err != nil {
		t.Fatal(err)
	}
	if err := fake.conn.Emit("/org/freedesktop/IBus/Other", ibusGlobalEngineChanged, "xkb:de::ger"); err != nil {
		t.Fatal(err)
	}
	fake.setEngine(t, "xkb:ru::rus")
	if ev := sink.next(t); ev != (sinkEvent{kind: "engine", engine: "xkb:ru::rus"}) {
		t.Fatalf("event after signal = %+v, want xkb:ru::rus", ev)
	}
	fake.setEngine(t, "xkb:us::eng")
	if ev := sink.next(t); ev != (sinkEvent{kind: "engine", engine: "xkb:us::eng"}) {
		t.Fatalf("event after second signal = %+v", ev)
	}
}

func TestIBusSourceBusNoGlobalEngine(t *testing.T) {
	bus := startTestBus(t)
	fake := startFakeIBus(t, bus, "")
	src := busTestSource(bus.addr)
	sink, _ := runSource(t, src)

	sink.waitFor(t, sinkEvent{kind: "available", ok: true})
	if ev := sink.next(t); ev != (sinkEvent{kind: "engine", engine: ""}) {
		t.Fatalf("event = %+v, want Engine(\"\")", ev)
	}
	fake.setEngine(t, "xkb:ru::rus")
	sink.waitFor(t, sinkEvent{kind: "engine", engine: "xkb:ru::rus"})
}

func TestIBusSourceBusUnknownPropertyFallsBack(t *testing.T) {
	bus := startTestBus(t)
	fake := startFakeIBus(t, bus, "xkb:ru::rus")
	fake.mu.Lock()
	fake.propErr = &dbus.Error{Name: errUnknownProperty, Body: []any{"No such property 'GlobalEngine'"}}
	fake.mu.Unlock()
	sink, _ := runSource(t, busTestSource(bus.addr))

	sink.waitFor(t, sinkEvent{kind: "engine", engine: "xkb:ru::rus"})
	log := fake.callLog()
	if len(log) != 2 || log[1] != "GetGlobalEngine" {
		t.Fatalf("calls = %v, want Properties.Get then GetGlobalEngine", log)
	}
}

func TestIBusSourceBusQuery(t *testing.T) {
	bus := startTestBus(t)
	fake := startFakeIBus(t, bus, "xkb:us::eng")
	src := busTestSource(bus.addr)

	engine, err := src.Query(context.Background())
	if err != nil || engine != "xkb:us::eng" {
		t.Fatalf("Query() without Run = %q, %v", engine, err)
	}

	sink, _ := runSource(t, src)
	sink.waitFor(t, sinkEvent{kind: "engine", engine: "xkb:us::eng"})
	fake.setEngine(t, "xkb:ru::rus")
	sink.waitFor(t, sinkEvent{kind: "engine", engine: "xkb:ru::rus"})
	if engine, err := src.Query(context.Background()); err != nil || engine != "xkb:ru::rus" {
		t.Fatalf("Query() on live connection = %q, %v", engine, err)
	}

	fake.mu.Lock()
	fake.engine = ""
	fake.mu.Unlock()
	if _, err := src.Query(context.Background()); !errors.Is(err, errNoGlobalEngine) {
		t.Fatalf("Query() without engine error = %v, want errNoGlobalEngine", err)
	}
}

func TestIBusSourceBusReresolvesAfterRestart(t *testing.T) {
	env := addressTestEnv(t)
	var aliveMu sync.Mutex
	alive := map[int]bool{}
	setAlive := func(pid int, ok bool) {
		aliveMu.Lock()
		defer aliveMu.Unlock()
		alive[pid] = ok
	}
	env.Alive = func(pid int) bool {
		aliveMu.Lock()
		defer aliveMu.Unlock()
		return alive[pid]
	}
	path, err := SocketPath(env)
	if err != nil {
		t.Fatal(err)
	}
	writeAtomically := func(addr string, pid int) {
		tmp := path + ".tmp"
		writeAddressFile(t, tmp, addr, pid, time.Time{})
		if err := os.Rename(tmp, path); err != nil {
			t.Fatal(err)
		}
	}

	bus1 := startTestBus(t)
	startFakeIBus(t, bus1, "xkb:us::eng")
	setAlive(111, true)
	writeAtomically(bus1.addr, 111)

	var resolveMu sync.Mutex
	var resolved []string
	src := NewIBusSource(env, nil)
	src.connectSession = nil
	src.sleep = fastSleep
	src.resolve = func(e AddressEnv) (string, int, error) {
		addr, pid, err := ResolveAddress(e)
		resolveMu.Lock()
		resolved = append(resolved, fmt.Sprintf("%s|%d|%v", addr, pid, err))
		resolveMu.Unlock()
		return addr, pid, err
	}
	sink, _ := runSource(t, src)
	sink.waitFor(t, sinkEvent{kind: "engine", engine: "xkb:us::eng"})

	// ibus restart: the daemon dies, a new one writes a new address and pid.
	setAlive(111, false)
	bus1.stop()
	sink.waitFor(t, sinkEvent{kind: "available", ok: false})
	bus2 := startTestBus(t)
	startFakeIBus(t, bus2, "xkb:ru::rus")
	writeAtomically(bus2.addr, 222)
	setAlive(222, true)

	sink.waitFor(t, sinkEvent{kind: "engine", engine: "xkb:ru::rus"})
	resolveMu.Lock()
	defer resolveMu.Unlock()
	last := resolved[len(resolved)-1]
	if !strings.HasPrefix(last, bus2.addr+"|222|") {
		t.Fatalf("last resolve = %q, want the new address and pid", last)
	}
}

func TestIBusSourceSessionAcceleratorNeverAddressesIBus(t *testing.T) {
	session := startTestBus(t)

	var outMu sync.Mutex
	var outgoing []*dbus.Message
	src := NewIBusSource(AddressEnv{}, nil)
	src.resolve = func(AddressEnv) (string, int, error) { return "", 0, ErrIBusNotRunning }
	src.connectSession = func(opts ...dbus.ConnOption) (*dbus.Conn, error) {
		opts = append(opts, dbus.WithOutgoingInterceptor(func(msg *dbus.Message) {
			outMu.Lock()
			outgoing = append(outgoing, msg)
			outMu.Unlock()
		}))
		return dbus.Connect(session.addr, opts...)
	}
	woken := make(chan struct{}, 16)
	src.sleep = func(ctx context.Context, _ time.Duration, wake <-chan struct{}) (bool, bool) {
		ok, w := sleepOrWake(ctx, time.Minute, wake)
		if w {
			woken <- struct{}{}
		}
		return ok, w
	}
	_, stop := runSource(t, src)

	// A new owner of org.freedesktop.IBus on the session bus wakes the loop.
	owner, err := dbus.Connect(session.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	deadline := time.Now().Add(5 * time.Second)
	wasWoken := false
	for !wasWoken && time.Now().Before(deadline) {
		if _, err := owner.RequestName(ibusService, dbus.NameFlagDoNotQueue); err != nil {
			t.Fatal(err)
		}
		select {
		case <-woken:
			wasWoken = true
		case <-time.After(100 * time.Millisecond):
		}
		if _, err := owner.ReleaseName(ibusService); err != nil {
			t.Fatal(err)
		}
	}
	if !wasWoken {
		t.Fatal("NameOwnerChanged for org.freedesktop.IBus did not wake the reconnect loop")
	}
	stop()

	outMu.Lock()
	defer outMu.Unlock()
	sawAddMatch := false
	for _, msg := range outgoing {
		var dest, member string
		if v, ok := msg.Headers[dbus.FieldDestination]; ok {
			_ = v.Store(&dest)
		}
		if v, ok := msg.Headers[dbus.FieldMember]; ok {
			_ = v.Store(&member)
		}
		if dest != dbusService {
			t.Errorf("session connection sent %s to %q, want only %s", member, dest, dbusService)
		}
		if member == "AddMatch" && len(msg.Body) == 1 {
			rule, _ := msg.Body[0].(string)
			if strings.Contains(rule, "sender='org.freedesktop.DBus'") && strings.Contains(rule, "arg0='org.freedesktop.IBus'") {
				sawAddMatch = true
			}
		}
	}
	if !sawAddMatch {
		t.Fatal("NameOwnerChanged match rule with sender and arg0 not sent")
	}
}

func TestRunDiagnosticsWithFakeIBus(t *testing.T) {
	bus := startTestBus(t)
	fake := startFakeIBus(t, bus, "xkb:ru::rus")
	env := addressTestEnv(t)
	env.IBusAddress = bus.addr

	var out syncBuffer
	done := make(chan error, 1)
	go func() {
		done <- RunDiagnostics(context.Background(), &out, env, usRu, time.Second)
	}()
	waitUntil(t, func() bool { return strings.Contains(out.String(), "confidence=confirmed") })
	fake.setEngine(t, "xkb:us::eng")
	if err := <-done; err != nil {
		t.Fatalf("RunDiagnostics() error = %v\n%s", err, out.String())
	}
	text := out.String()
	for _, want := range []string{
		"IBus socket path: " + filepath.Join(env.ConfigDir, "ibus", "bus", testMachineID+"-unix-wayland-0"),
		"IBus address: " + bus.addr,
		`Global engine: "xkb:ru::rus"`,
		"layout ru = index 1",
		`index=0 engine="xkb:us::eng" confidence=confirmed`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("diagnostics output lacks %q:\n%s", want, text)
		}
	}
}

func TestRunDiagnosticsIBusNotRunning(t *testing.T) {
	env := addressTestEnv(t)
	var out syncBuffer
	err := RunDiagnostics(context.Background(), &out, env, usRu, 0)
	if !errors.Is(err, ErrIBusNotRunning) {
		t.Fatalf("RunDiagnostics() error = %v, want ErrIBusNotRunning", err)
	}
	if !strings.Contains(out.String(), "IBus address: error:") {
		t.Fatalf("output lacks the address error:\n%s", out.String())
	}
}

func TestDescribeEngine(t *testing.T) {
	for engine, want := range map[string]string{
		"xkb:us::eng": "layout us = index 0",
		"xkb:de::ger": "layout de is not one of the pair",
		"libpinyin":   "not an XKB engine",
	} {
		if got := describeEngine(usRu, engine); !strings.HasPrefix(got, want) {
			t.Errorf("describeEngine(%q) = %q, want prefix %q", engine, got, want)
		}
	}
}

// syncBuffer is a bytes.Buffer safe for concurrent writes and reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
