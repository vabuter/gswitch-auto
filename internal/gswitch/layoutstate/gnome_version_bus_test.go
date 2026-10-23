package layoutstate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// These tests exercise ProbeShellVersion against a fake org.gnome.Shell. They
// are gated by GSWITCH_DBUS_TEST=1 (run under dbus-run-session, §6.2) and,
// whatever the gate, they only ever talk to a private dbus-daemon started
// here: DBUS_SESSION_BUS_ADDRESS is pointed at it for the test's duration, so
// the user's session bus (and the real gnome-shell) is never addressed.

func gvTestRequireGated(t *testing.T) {
	t.Helper()
	if os.Getenv("GSWITCH_DBUS_TEST") != "1" {
		t.Skip("set GSWITCH_DBUS_TEST=1 (run under dbus-run-session) to run the org.gnome.Shell probe tests")
	}
}

// gvTestShell serves org.gnome.Shell's ShellVersion property on a test bus.
type gvTestShell struct {
	mu      sync.Mutex
	version any // the Properties.Get reply value
	calls   int
}

type gvTestShellProps struct{ s *gvTestShell }

func (p gvTestShellProps) Get(iface, prop string) (dbus.Variant, *dbus.Error) {
	s := p.s
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if iface != shellInterface || prop != shellVersionProp {
		return dbus.Variant{}, &dbus.Error{Name: errUnknownProperty, Body: []any{"unknown property " + prop}}
	}
	return dbus.MakeVariant(s.version), nil
}

func (p gvTestShellProps) GetAll(string) (map[string]dbus.Variant, *dbus.Error) {
	return map[string]dbus.Variant{}, nil
}

func (p gvTestShellProps) Set(string, string, dbus.Variant) *dbus.Error {
	return &dbus.Error{Name: "org.freedesktop.DBus.Error.PropertyReadOnly", Body: []any{"read-only"}}
}

func gvTestStartShell(t *testing.T, bus *testBus, version any) *gvTestShell {
	t.Helper()
	conn, err := dbus.Connect(bus.addr)
	if err != nil {
		t.Fatalf("fake shell connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	s := &gvTestShell{version: version}
	if err := conn.Export(gvTestShellProps{s}, shellPath, "org.freedesktop.DBus.Properties"); err != nil {
		t.Fatal(err)
	}
	reply, err := conn.RequestName(shellService, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("fake shell RequestName = %v, %v", reply, err)
	}
	return s
}

func gvTestProbeCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestProbeShellVersionBusReadsProperty(t *testing.T) {
	gvTestRequireGated(t)
	bus := startTestBus(t)
	shell := gvTestStartShell(t, bus, "43.9")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", bus.addr)

	info := ProbeShellVersion(gvTestProbeCtx(t))
	if info.Err != nil || info.Version != "43.9" {
		t.Fatalf("ProbeShellVersion() = %+v, want Version 43.9", info)
	}
	shell.mu.Lock()
	calls := shell.calls
	shell.mu.Unlock()
	if calls != 1 {
		t.Fatalf("Properties.Get called %d times, want 1", calls)
	}
}

func TestProbeShellVersionBusNoAutoStartAndPrivateConnection(t *testing.T) {
	gvTestRequireGated(t)
	bus := startTestBus(t)
	gvTestStartShell(t, bus, "50.6")

	var outMu sync.Mutex
	var outgoing []*dbus.Message
	connect := func(opts ...dbus.ConnOption) (*dbus.Conn, error) {
		opts = append(opts, dbus.WithOutgoingInterceptor(func(msg *dbus.Message) {
			outMu.Lock()
			outgoing = append(outgoing, msg)
			outMu.Unlock()
		}))
		return dbus.Connect(bus.addr, opts...)
	}
	info := probeShellVersion(gvTestProbeCtx(t), connect)
	if info.Err != nil || info.Version != "50.6" {
		t.Fatalf("probeShellVersion() = %+v, want Version 50.6", info)
	}
	outMu.Lock()
	defer outMu.Unlock()
	sawGet := false
	for _, msg := range outgoing {
		var dest, member string
		if v, ok := msg.Headers[dbus.FieldDestination]; ok {
			_ = v.Store(&dest)
		}
		if v, ok := msg.Headers[dbus.FieldMember]; ok {
			_ = v.Store(&member)
		}
		if dest != shellService {
			if dest != dbusService {
				t.Errorf("probe sent %s to %q, want only %s and %s", member, dest, shellService, dbusService)
			}
			continue
		}
		if member != "Get" {
			t.Errorf("probe sent %s to %s, want only Properties.Get", member, shellService)
		}
		if msg.Flags&dbus.FlagNoAutoStart == 0 {
			t.Error("Properties.Get sent without FlagNoAutoStart")
		}
		sawGet = true
	}
	if !sawGet {
		t.Fatal("no Properties.Get sent to org.gnome.Shell")
	}
}

func TestProbeShellVersionBusNameNotOwned(t *testing.T) {
	gvTestRequireGated(t)
	bus := startTestBus(t)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", bus.addr)

	info := ProbeShellVersion(gvTestProbeCtx(t))
	if !errors.Is(info.Err, ErrShellUnavailable) || info.Version != "" {
		t.Fatalf("ProbeShellVersion() without an owner = %+v, want ErrShellUnavailable", info)
	}
}

func TestProbeShellVersionBusNoSessionBus(t *testing.T) {
	gvTestRequireGated(t)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(t.TempDir(), "no-bus"))

	info := ProbeShellVersion(gvTestProbeCtx(t))
	if !errors.Is(info.Err, ErrShellUnavailable) || info.Version != "" {
		t.Fatalf("ProbeShellVersion() without a bus = %+v, want ErrShellUnavailable", info)
	}
}

func TestProbeShellVersionBusUnexpectedFormat(t *testing.T) {
	gvTestRequireGated(t)
	for name, value := range map[string]any{
		"garbage": "unknown",
		"integer": uint32(50),
	} {
		t.Run(name, func(t *testing.T) {
			bus := startTestBus(t)
			gvTestStartShell(t, bus, value)
			t.Setenv("DBUS_SESSION_BUS_ADDRESS", bus.addr)

			info := ProbeShellVersion(gvTestProbeCtx(t))
			if !errors.Is(info.Err, ErrShellVersionFormat) || info.Version != "" {
				t.Fatalf("ProbeShellVersion() = %+v, want ErrShellVersionFormat", info)
			}
		})
	}
}

func TestProbeShellVersionBusContextDone(t *testing.T) {
	gvTestRequireGated(t)
	bus := startTestBus(t)
	// A context that is already done yields an error, never a version, even
	// though the fake shell would answer.
	gvTestStartShell(t, bus, "50.6")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", bus.addr)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	info := ProbeShellVersion(ctx)
	if info.Err == nil || info.Version != "" {
		t.Fatalf("ProbeShellVersion() with a done context = %+v, want an error", info)
	}
}
