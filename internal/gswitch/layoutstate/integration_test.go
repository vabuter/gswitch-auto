package layoutstate

import (
	"bytes"
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// TestIBusIntegration talks to the real ibus-daemon of the current session. It
// only reads (address file, GlobalEngine property, GlobalEngineChanged
// subscription) and never changes the layout. Run with GSWITCH_IBUS_TEST=1.
func TestIBusIntegration(t *testing.T) {
	if os.Getenv("GSWITCH_IBUS_TEST") != "1" {
		t.Skip("set GSWITCH_IBUS_TEST=1 to run against the session's ibus-daemon")
	}
	env := AddressEnvFromProcess()
	path, err := SocketPath(env)
	if err != nil {
		t.Fatalf("SocketPath() error = %v", err)
	}
	addr, pid, err := ResolveAddress(env)
	if err != nil {
		t.Fatalf("ResolveAddress() error = %v (address file %s)", err, path)
	}
	t.Logf("address file %s, address %s, pid %d", path, addr, pid)

	src := NewIBusSource(env, t.Logf)
	var outMu sync.Mutex
	var sessionDests []string
	src.connectSession = func(opts ...dbus.ConnOption) (*dbus.Conn, error) {
		opts = append(opts, dbus.WithOutgoingInterceptor(func(msg *dbus.Message) {
			var dest string
			if v, ok := msg.Headers[dbus.FieldDestination]; ok {
				_ = v.Store(&dest)
			}
			outMu.Lock()
			sessionDests = append(sessionDests, dest)
			outMu.Unlock()
		}))
		return connectSessionBus(opts...)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	engine, err := src.Query(ctx)
	cancel()
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	layout, variant, ok := ParseXKBEngine(engine)
	t.Logf("global engine %q (xkb=%v layout=%q variant=%q)", engine, ok, layout, variant)

	tracker := New(Options{Layouts: usRu, DebugLogger: t.Logf})
	tracker.AddSource(src)
	tracker.Start(context.Background())
	waitUntil(t, func() bool { return tracker.State().Source == SourceIBus })
	time.Sleep(200 * time.Millisecond) // let the session-bus accelerator subscribe
	st := tracker.State()
	tracker.Stop()
	t.Logf("tracker state: %s", st)
	if st.Engine != engine {
		t.Fatalf("tracker engine = %q, want %q", st.Engine, engine)
	}
	if want := pairIndex(usRu, Layout{Name: layout, Variant: variant}); ok && want >= 0 {
		if st.Confidence != Confirmed || st.Index != want {
			t.Fatalf("tracker state = %s, want Confirmed index %d", st, want)
		}
	}

	outMu.Lock()
	for _, dest := range sessionDests {
		if dest != dbusService {
			t.Errorf("session-bus message to %q, want only %s", dest, dbusService)
		}
	}
	t.Logf("session-bus messages: %d, all to %s", len(sessionDests), dbusService)
	outMu.Unlock()

	var out bytes.Buffer
	if err := RunDiagnostics(context.Background(), &out, env, usRu, 0); err != nil {
		t.Fatalf("RunDiagnostics() error = %v\n%s", err, out.String())
	}
	t.Logf("diagnostics:\n%s", out.String())
}
