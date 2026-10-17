package ipc

import (
	"bufio"
	"context"
	"encoding/xml"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
)

// ipcTestPrivateBus starts a private `dbus-daemon --session` and points
// DBUS_SESSION_BUS_ADDRESS at it for the duration of the test, so that every
// session-bus connection the test opens is hermetic. The test is skipped when
// no dbus-daemon binary exists.
func ipcTestPrivateBus(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon not installed")
	}
	dir, err := os.MkdirTemp("", "gsipc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "bus")

	cmd := exec.Command(bin, "--session", "--nofork", "--nopidfile",
		"--address=unix:path="+sock, "--print-address=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start dbus-daemon: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	lines := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		lines <- strings.TrimSpace(line)
	}()
	var addr string
	select {
	case addr = <-lines:
	case <-time.After(10 * time.Second):
		t.Fatalf("dbus-daemon did not print its address: %s", stderr.String())
	}
	if addr == "" {
		t.Fatalf("dbus-daemon failed: %s", stderr.String())
	}
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", addr)
	return addr
}

func ipcTestConnect(t *testing.T) *dbus.Conn {
	t.Helper()
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func ipcTestStartService(t *testing.T, ctl Controller) *Service {
	t.Helper()
	svc, err := Start(t.Context(), ctl, t.Logf)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc
}

// ipcTestSyncController calls back into the Service synchronously from inside
// SetAutoEnabled, while holding its own mutex, as the daemon is allowed to.
type ipcTestSyncController struct {
	mu      sync.Mutex
	svc     *Service
	enabled bool
	calls   int
	refused int
	stats   uint64
}

func (c *ipcTestSyncController) setService(svc *Service) {
	c.mu.Lock()
	c.svc = svc
	c.mu.Unlock()
}

func (c *ipcTestSyncController) AutoEnabled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enabled
}

func (c *ipcTestSyncController) SetAutoEnabled(enabled bool, source string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls%10 == 0 {
		c.refused++
		return errors.New("refused by test controller")
	}
	c.enabled = enabled
	c.svc.NotifyAutoEnabled(enabled, source)
	c.svc.UpdateStatus(c.snapshotLocked())
	return nil
}

func (c *ipcTestSyncController) Reload() error { return nil }

func (c *ipcTestSyncController) SuspendHotkeys(bool, string) error { return nil }

func (c *ipcTestSyncController) Snapshot() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshotLocked()
}

func (c *ipcTestSyncController) snapshotLocked() Status {
	warnings := make([]string, c.stats%4)
	for i := range warnings {
		warnings[i] = "warning"
	}
	return Status{
		AutoEnabled:    c.enabled,
		AutoAvailable:  true,
		Stats:          map[string]uint64{"armed": c.stats, "fired": c.stats / 2},
		Count:          uint32(c.stats % 1000),
		ConfigWarnings: warnings,
	}
}

// pushStatus publishes a fresh snapshot atomically with respect to
// SetAutoEnabled, like the daemon's status loop.
func (c *ipcTestSyncController) pushStatus() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stats++
	if c.svc != nil {
		c.svc.UpdateStatus(c.snapshotLocked())
	}
}

func ipcTestIsBusy(err error) bool {
	var de dbus.Error
	return errors.As(err, &de) && de.Name == "org.freedesktop.DBus.Error.Failed"
}

// TestPropertySetAutoEnabledNoDeadlock is mandatory (SPEC §5 WP6, C28): a
// writable property Set must never re-enter the godbus property lock, even
// when the controller calls back into the Service synchronously.
func TestPropertySetAutoEnabledNoDeadlock(t *testing.T) {
	ipcTestPrivateBus(t)
	ctl := &ipcTestSyncController{}
	svc, err := Start(t.Context(), ctl, t.Logf)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctl.setService(svc)
	closed := false
	t.Cleanup(func() {
		if !closed {
			svc.Close()
		}
	})

	peer := ipcTestConnect(t)
	obj := peer.Object(BusName, ObjectPath)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			ctl.pushStatus()
			time.Sleep(100 * time.Microsecond)
		}
	}()

	busy := 0
	for i := range 100 {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		start := time.Now()
		call := obj.CallWithContext(ctx, propertiesIfc+".Set", 0, Interface, propAutoEnabled, dbus.MakeVariant(i%2 == 0))
		elapsed := time.Since(start)
		cancel()
		if elapsed >= time.Second {
			close(stop)
			t.Fatalf("Set #%d took %s", i, elapsed)
		}
		if call.Err != nil {
			if !ipcTestIsBusy(call.Err) {
				close(stop)
				t.Fatalf("Set #%d: %v", i, call.Err)
			}
			busy++
		}
	}
	close(stop)
	wg.Wait()

	// After the queued requests are processed, Get reflects the controller.
	deadline := time.Now().Add(3 * time.Second)
	for {
		var got bool
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		err := obj.CallWithContext(ctx, propertiesIfc+".Get", 0, Interface, propAutoEnabled).Store(&got)
		cancel()
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if len(svc.requests) == 0 && got == ctl.AutoEnabled() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("AutoEnabled = %t, controller = %t", got, ctl.AutoEnabled())
		}
		time.Sleep(5 * time.Millisecond)
	}
	ctl.mu.Lock()
	calls, refused := ctl.calls, ctl.refused
	ctl.mu.Unlock()
	if calls == 0 || refused == 0 {
		t.Fatalf("controller calls=%d refused=%d", calls, refused)
	}
	t.Logf("controller calls=%d refused=%d busy=%d", calls, refused, busy)

	start := time.Now()
	svc.Close()
	closed = true
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("Close took %s", elapsed)
	}
}

// TestGetAllConcurrentWithUpdates exercises guardedProps: GetAll replies must
// not alias maps and slices that propLoop updates in place (run with -race).
func TestGetAllConcurrentWithUpdates(t *testing.T) {
	ipcTestPrivateBus(t)
	ctl := &ipcTestSyncController{}
	svc := ipcTestStartService(t, ctl)
	ctl.setService(svc)
	peer := ipcTestConnect(t)
	obj := peer.Object(BusName, ObjectPath)

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				ctl.pushStatus()
				time.Sleep(50 * time.Microsecond)
			}
		}
	}()
	for range 200 {
		var all map[string]dbus.Variant
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		err := obj.CallWithContext(ctx, propertiesIfc+".GetAll", 0, Interface).Store(&all)
		cancel()
		if err != nil {
			close(stop)
			t.Fatalf("GetAll: %v", err)
		}
		if len(all) != len(propertyNames) {
			close(stop)
			t.Fatalf("GetAll returned %d properties", len(all))
		}
	}
	close(stop)
	<-done
}

func ipcTestRequireGated(t *testing.T) {
	t.Helper()
	if os.Getenv("GSWITCH_DBUS_TEST") != "1" {
		t.Skip("set GSWITCH_DBUS_TEST=1 (run under dbus-run-session) to run D-Bus round-trip tests")
	}
}

// ipcTestEchoController reacts like the daemon: state changes are announced
// through the Service.
type ipcTestEchoController struct {
	ipcTestController
	svcMu sync.Mutex
	svc   *Service
}

func (c *ipcTestEchoController) setService(svc *Service) {
	c.svcMu.Lock()
	c.svc = svc
	c.svcMu.Unlock()
}

func (c *ipcTestEchoController) SetAutoEnabled(enabled bool, source string) error {
	if err := c.ipcTestController.SetAutoEnabled(enabled, source); err != nil {
		return err
	}
	c.svcMu.Lock()
	svc := c.svc
	c.svcMu.Unlock()
	svc.NotifyAutoEnabled(enabled, source)
	svc.UpdateStatus(c.Snapshot())
	return nil
}

type ipcTestClientEvents struct {
	status  chan ClientStatus
	toggle  chan ipcTestSetCall
	correct chan Correction
	undone  chan ipcTestSetCall
}

func ipcTestNewClient(t *testing.T) (*Client, *ipcTestClientEvents) {
	t.Helper()
	ev := &ipcTestClientEvents{
		status:  make(chan ClientStatus, 256),
		toggle:  make(chan ipcTestSetCall, 16),
		correct: make(chan Correction, 16),
		undone:  make(chan ipcTestSetCall, 16),
	}
	c := NewClient(ClientCallbacks{
		OnStatus:  func(st ClientStatus) { ev.status <- st },
		OnToggle:  func(enabled bool, source string) { ev.toggle <- ipcTestSetCall{enabled, source} },
		OnCorrect: func(c Correction) { ev.correct <- c },
		OnUndone:  func(word string, learned bool) { ev.undone <- ipcTestSetCall{learned, word} },
	})
	if err := c.Start(); err != nil {
		t.Fatalf("client Start: %v", err)
	}
	t.Cleanup(c.Stop)
	return c, ev
}

func ipcTestWaitStatus(t *testing.T, ev *ipcTestClientEvents, what string, cond func(ClientStatus) bool) ClientStatus {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case st := <-ev.status:
			if cond(st) {
				return st
			}
		case <-timeout:
			t.Fatalf("timed out waiting for status: %s", what)
		}
	}
}

func TestServiceClientRoundTrip(t *testing.T) {
	ipcTestRequireGated(t)
	ipcTestPrivateBus(t)
	initial := ipcTestFullStatus()
	initial.AutoEnabled = false
	ctl := &ipcTestEchoController{ipcTestController: ipcTestController{snapshot: initial}}
	svc := ipcTestStartService(t, ctl)
	ctl.setService(svc)

	client, ev := ipcTestNewClient(t)
	st := ipcTestWaitStatus(t, ev, "connected", func(st ClientStatus) bool { return st.Connected })
	want := cloneStatus(initial)
	want.Stats = normalizeStats(initial.Stats)
	if st.ActiveLayout != "ru" || st.ToggleChord != "42+54" || st.Last != want.Last || st.Stats["custom"] != 9 {
		t.Fatalf("GetAll status = %+v", st.Status)
	}

	// Introspection over the bus matches the spec.
	peer := ipcTestConnect(t)
	var xmlData string
	if err := peer.Object(BusName, ObjectPath).CallWithContext(t.Context(),
		"org.freedesktop.DBus.Introspectable.Introspect", 0).Store(&xmlData); err != nil {
		t.Fatalf("Introspect: %v", err)
	}
	var got, spec introspect.Node
	if err := xml.Unmarshal([]byte(xmlData), &got); err != nil {
		t.Fatal(err)
	}
	if err := xml.Unmarshal([]byte(ipcTestSpecXML), &spec); err != nil {
		t.Fatal(err)
	}
	ipcTestCompareInterface(t, ipcTestFindInterface(t, &got, Interface), ipcTestFindInterface(t, &spec, Interface))

	// Method call.
	if err := client.SetAutoEnabled(t.Context(), true); err != nil {
		t.Fatalf("SetAutoEnabled: %v", err)
	}
	select {
	case tg := <-ev.toggle:
		if tg != (ipcTestSetCall{true, SourceDBus}) {
			t.Fatalf("toggle = %+v", tg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no AutoEnabledChanged")
	}
	ipcTestWaitStatus(t, ev, "AutoEnabled=true", func(st ClientStatus) bool { return st.AutoEnabled })

	// Property Set from another connection.
	if err := peer.Object(BusName, ObjectPath).CallWithContext(t.Context(), propertiesIfc+".Set", 0,
		Interface, propAutoEnabled, dbus.MakeVariant(false)).Err; err != nil {
		t.Fatalf("property Set: %v", err)
	}
	ipcTestWaitStatus(t, ev, "AutoEnabled=false", func(st ClientStatus) bool { return !st.AutoEnabled })
	ipcTestWait(t, "controller updated by property Set", func() bool { return !ctl.AutoEnabled() })

	// Signals.
	at := time.Unix(1760001234, 0)
	svc.NotifyCorrected(Correction{From: "ghbdtn", To: "привет", Direction: "us>ru", Time: at})
	select {
	case c := <-ev.correct:
		if c.From != "ghbdtn" || c.To != "привет" || c.Direction != "us>ru" || !c.Time.Equal(at) {
			t.Fatalf("correction = %+v", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no Corrected")
	}
	svc.NotifyUndone("ghbdtn", true)
	select {
	case u := <-ev.undone:
		if u != (ipcTestSetCall{true, "ghbdtn"}) {
			t.Fatalf("undone = %+v", u)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no CorrectionUndone")
	}

	// Status changes arrive through PropertiesChanged.
	next := ctl.Snapshot()
	next.ActiveLayout = "us"
	next.PendingLearnedWords = []string{"rfr"}
	svc.UpdateStatus(next)
	ipcTestWaitStatus(t, ev, "ActiveLayout=us", func(st ClientStatus) bool {
		return st.ActiveLayout == "us" && len(st.PendingLearnedWords) == 1 && st.PendingLearnedWords[0] == "rfr"
	})

	// Reload error is reported as Error.Config.
	ctl.mu.Lock()
	ctl.reloadErr = errors.New("bad auto.conf")
	ctl.mu.Unlock()
	var de dbus.Error
	if err := client.Reload(t.Context()); !errors.As(err, &de) || de.Name != ErrConfig {
		t.Fatalf("Reload error = %v", err)
	}

	// SuspendHotkeys records the client's unique name; stopping the client
	// (its connection vanishes) ends the suspension.
	if err := client.SuspendHotkeys(t.Context(), true); err != nil {
		t.Fatalf("SuspendHotkeys: %v", err)
	}
	calls := ctl.suspendCalls()
	if len(calls) != 1 || !calls[0].suspend || !strings.HasPrefix(calls[0].owner, ":") {
		t.Fatalf("suspend calls = %v", calls)
	}
	client.Stop()
	ipcTestWait(t, "suspension released on caller vanish", func() bool {
		c := ctl.suspendCalls()
		return len(c) == 2 && c[1] == ipcTestSuspendCall{false, calls[0].owner}
	})
	if client.Status().Connected {
		t.Fatal("stopped client reports connected")
	}
	if err := client.SetAutoEnabled(t.Context(), true); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("stopped client call error = %v", err)
	}
}

func TestClientReconnectsAfterOwnerLoss(t *testing.T) {
	ipcTestRequireGated(t)
	ipcTestPrivateBus(t)
	ctl := &ipcTestController{snapshot: Status{ActiveLayout: "us"}}
	svc := ipcTestStartService(t, ctl)

	_, ev := ipcTestNewClient(t)
	ipcTestWaitStatus(t, ev, "connected", func(st ClientStatus) bool { return st.Connected })

	svc.Close()
	ipcTestWaitStatus(t, ev, "disconnected", func(st ClientStatus) bool { return !st.Connected })

	ctl2 := &ipcTestController{snapshot: Status{ActiveLayout: "ru"}}
	ipcTestStartService(t, ctl2)
	ipcTestWaitStatus(t, ev, "reconnected", func(st ClientStatus) bool {
		return st.Connected && st.ActiveLayout == "ru"
	})
}

func TestClientStartsBeforeDaemon(t *testing.T) {
	ipcTestRequireGated(t)
	ipcTestPrivateBus(t)
	client, ev := ipcTestNewClient(t)
	ipcTestWaitStatus(t, ev, "initial disconnected", func(st ClientStatus) bool { return !st.Connected })
	if err := client.Reload(t.Context()); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("Reload without daemon = %v", err)
	}
	ipcTestStartService(t, &ipcTestController{snapshot: Status{Version: "x"}})
	ipcTestWaitStatus(t, ev, "connected", func(st ClientStatus) bool { return st.Connected && st.Version == "x" })
}

func TestSecondInstanceNotPrimaryOwner(t *testing.T) {
	ipcTestRequireGated(t)
	ipcTestPrivateBus(t)
	ipcTestStartService(t, &ipcTestController{})
	svc2, err := Start(t.Context(), &ipcTestController{}, t.Logf)
	if !errors.Is(err, ErrNameTaken) || svc2 != nil {
		t.Fatalf("second Start = %v, %v", svc2, err)
	}
	// The first instance still answers.
	peer := ipcTestConnect(t)
	var v dbus.Variant
	if err := peer.Object(BusName, ObjectPath).CallWithContext(t.Context(), propertiesIfc+".Get", 0,
		Interface, propVersion).Store(&v); err != nil {
		t.Fatalf("Get on first instance: %v", err)
	}
}

func TestClientStopKeepsSharedSessionBus(t *testing.T) {
	ipcTestRequireGated(t)
	ipcTestPrivateBus(t)
	shared, err := dbus.SessionBus()
	if err != nil {
		t.Fatalf("shared session bus: %v", err)
	}
	ipcTestStartService(t, &ipcTestController{})
	client, ev := ipcTestNewClient(t)
	ipcTestWaitStatus(t, ev, "connected", func(st ClientStatus) bool { return st.Connected })
	client.Stop()
	if !shared.Connected() {
		t.Fatal("Client.Stop closed the shared session bus connection")
	}
	var id string
	if err := shared.BusObject().CallWithContext(t.Context(), dbusName+".GetId", 0).Store(&id); err != nil {
		t.Fatalf("shared connection unusable after Client.Stop: %v", err)
	}
}
