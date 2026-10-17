package layoutstate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/arumata/gswitch/internal/gswitch/detect"
)

const (
	ibusService   = "org.freedesktop.IBus"
	ibusInterface = "org.freedesktop.IBus"
	ibusPath      = dbus.ObjectPath("/org/freedesktop/IBus")

	ibusGlobalEngineChanged = ibusInterface + ".GlobalEngineChanged"
	ibusGlobalEngineProp    = "GlobalEngine"
	ibusEngineDescType      = "IBusEngineDesc"
	ibusNoGlobalEngine      = "No global engine"

	dbusService          = "org.freedesktop.DBus"
	dbusInterface        = "org.freedesktop.DBus"
	dbusPath             = dbus.ObjectPath("/org/freedesktop/DBus")
	dbusNameOwnerChanged = dbusInterface + ".NameOwnerChanged"

	errUnknownProperty = "org.freedesktop.DBus.Error.UnknownProperty"
	errInvalidArgs     = "org.freedesktop.DBus.Error.InvalidArgs"

	ibusConnectTimeout = 2 * time.Second
	ibusReadTimeout    = 2 * time.Second
	ibusBackoffInitial = 200 * time.Millisecond
	ibusBackoffMax     = 5 * time.Second
	maxVariantDepth    = 3
)

// errNoGlobalEngine is returned by Query when IBus has no global engine.
var errNoGlobalEngine = errors.New("IBus: no global engine")

// errIBusDisconnected ends a connected session.
var errIBusDisconnected = errors.New("IBus connection closed")

// IBusSource follows org.freedesktop.IBus.GlobalEngineChanged on the private
// IBus bus. It implements Source.
type IBusSource struct {
	env  AddressEnv
	logf func(string, ...any)

	// Test hooks; production values are set by NewIBusSource.
	resolve func(AddressEnv) (string, int, error)
	dial    func(ctx context.Context, addr string) (*dbus.Conn, error)
	sleep   func(ctx context.Context, d time.Duration, wake <-chan struct{}) (ok, woken bool)
	// connectSession opens a private session-bus connection for the optional
	// NameOwnerChanged accelerator; nil disables it.
	connectSession func(opts ...dbus.ConnOption) (*dbus.Conn, error)

	wake chan struct{}

	mu   sync.Mutex
	conn *dbus.Conn // live connection while Run is connected
}

var _ Source = (*IBusSource)(nil)

// NewIBusSource creates a source that re-runs ResolveAddress(env) on every
// (re)connect, because the address file and daemon pid change on ibus restart.
func NewIBusSource(env AddressEnv, logf func(string, ...any)) *IBusSource {
	return &IBusSource{
		env:            env,
		logf:           logf,
		resolve:        ResolveAddress,
		dial:           dialIBus,
		sleep:          sleepOrWake,
		connectSession: connectSessionBus,
		wake:           make(chan struct{}, 1),
	}
}

// IBusSourceEnabled reports whether the IBus source should be used for the
// auto.conf layout-source value: "auto" in a GNOME session or "ibus", and never
// for root (IBus accepts same-uid peers only).
func IBusSourceEnabled(layoutSource string, env *detect.SessionEnv, euid int) bool {
	if euid == 0 {
		return false
	}
	switch layoutSource {
	case "ibus":
		return true
	case "auto":
		return detect.IsGNOMESession(env)
	default:
		return false
	}
}

// Name implements Source.
func (s *IBusSource) Name() string { return SourceIBus }

// Run implements Source: connect, subscribe to GlobalEngineChanged, read the
// current engine, forward signals, and reconnect with a 200 ms…5 s backoff
// after any failure or disconnect. It returns when ctx is done.
func (s *IBusSource) Run(ctx context.Context, sink Sink) {
	var wg sync.WaitGroup
	defer wg.Wait()
	if s.connectSession != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.watchSessionBus(ctx)
		}()
	}

	backoff := ibusBackoffInitial
	lastErr := ""
	for ctx.Err() == nil {
		connected, err := s.session(ctx, sink)
		if ctx.Err() != nil {
			return
		}
		sink.Available(s.Name(), false)
		if connected {
			backoff = ibusBackoffInitial
			lastErr = ""
		}
		if msg := err.Error(); msg != lastErr {
			lastErr = msg
			s.log("IBus layout source: %v (retrying)", err)
		}
		ok, woken := s.sleep(ctx, backoff, s.wake)
		if !ok {
			return
		}
		if woken {
			backoff = ibusBackoffInitial
			continue
		}
		backoff = min(backoff*2, ibusBackoffMax)
	}
}

// session runs one connection. connected reports whether the subscription and
// the initial read succeeded.
func (s *IBusSource) session(ctx context.Context, sink Sink) (connected bool, err error) {
	addr, pid, err := s.resolve(s.env)
	if err != nil {
		return false, err
	}
	conn, err := s.dial(ctx, addr)
	if err != nil {
		return false, fmt.Errorf("connect %s: %w", addr, err)
	}
	defer conn.Close()

	signals := make(chan *dbus.Signal, 16)
	conn.Signal(signals)
	if err := conn.AddMatchSignalContext(ctx,
		dbus.WithMatchInterface(ibusInterface),
		dbus.WithMatchMember("GlobalEngineChanged"),
		dbus.WithMatchObjectPath(ibusPath),
	); err != nil {
		return false, fmt.Errorf("subscribe GlobalEngineChanged: %w", err)
	}
	readCtx, cancel := context.WithTimeout(ctx, ibusReadTimeout)
	engine, err := readGlobalEngine(readCtx, conn)
	cancel()
	if err != nil && !errors.Is(err, errNoGlobalEngine) {
		return false, err
	}

	s.setConn(conn)
	defer s.setConn(nil)
	s.log("IBus layout source: connected (pid %d), global engine %q", pid, engine)
	sink.Available(s.Name(), true)
	sink.Engine(s.Name(), engine)

	for {
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case <-conn.Context().Done():
			return true, errIBusDisconnected
		case sig, ok := <-signals:
			if !ok {
				return true, errIBusDisconnected
			}
			if name, ok := globalEngineFromSignal(sig); ok {
				sink.Engine(s.Name(), name)
			}
		}
	}
}

// Query implements Source. It reuses the live connection when Run is connected
// and otherwise opens a temporary one.
func (s *IBusSource) Query(ctx context.Context) (string, error) {
	if conn := s.getConn(); conn != nil {
		return readGlobalEngine(ctx, conn)
	}
	addr, _, err := s.resolve(s.env)
	if err != nil {
		return "", err
	}
	conn, err := s.dial(ctx, addr)
	if err != nil {
		return "", fmt.Errorf("connect %s: %w", addr, err)
	}
	defer conn.Close()
	return readGlobalEngine(ctx, conn)
}

func (s *IBusSource) setConn(conn *dbus.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conn = conn
}

func (s *IBusSource) getConn() *dbus.Conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn
}

func (s *IBusSource) log(format string, args ...any) {
	if s.logf != nil {
		s.logf(format, args...)
	}
}

// watchSessionBus wakes the reconnect loop when org.freedesktop.IBus gets a new
// owner on the session bus (ibus restart). It only talks to the bus daemon
// (Hello, AddMatch); it never sends anything to org.freedesktop.IBus on the
// session bus, where that name is activatable (`ibus-daemon --replace`).
func (s *IBusSource) watchSessionBus(ctx context.Context) {
	conn, err := s.connectSession(dbus.WithContext(ctx))
	if err != nil {
		s.log("IBus layout source: session bus accelerator unavailable: %v", err)
		return
	}
	defer conn.Close()

	signals := make(chan *dbus.Signal, 8)
	conn.Signal(signals)
	if err := conn.AddMatchSignalContext(ctx,
		dbus.WithMatchSender(dbusService),
		dbus.WithMatchInterface(dbusInterface),
		dbus.WithMatchMember("NameOwnerChanged"),
		dbus.WithMatchObjectPath(dbusPath),
		dbus.WithMatchArg(0, ibusService),
	); err != nil {
		s.log("IBus layout source: session bus accelerator unavailable: %v", err)
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-conn.Context().Done():
			return
		case sig, ok := <-signals:
			if !ok {
				return
			}
			if ibusOwnerAppeared(sig) {
				select {
				case s.wake <- struct{}{}:
				default:
				}
			}
		}
	}
}

// ibusOwnerAppeared reports a NameOwnerChanged signal from the bus daemon that
// gives org.freedesktop.IBus a new owner.
func ibusOwnerAppeared(sig *dbus.Signal) bool {
	if sig == nil || sig.Name != dbusNameOwnerChanged || sig.Sender != dbusService || len(sig.Body) != 3 {
		return false
	}
	name, _ := sig.Body[0].(string)
	newOwner, _ := sig.Body[2].(string)
	return name == ibusService && newOwner != ""
}

// connectSessionBus opens a private, authenticated session-bus connection like
// dbus.ConnectSessionBus, but never autolaunches a bus (dbus-launch) when no
// session bus address can be found. It never uses the shared dbus.SessionBus().
func connectSessionBus(opts ...dbus.ConnOption) (*dbus.Conn, error) {
	conn, err := dbus.SessionBusPrivateNoAutoStartup(opts...)
	if err != nil {
		return nil, err
	}
	if err := conn.Auth(nil); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := conn.Hello(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// dialIBus connects with plain dbus.Connect (EXTERNAL auth, Hello) and a 2 s
// connect deadline. The connection lives until ctx is done or it is closed.
func dialIBus(ctx context.Context, addr string) (*dbus.Conn, error) {
	connCtx, cancel := context.WithCancel(ctx)
	deadline := time.AfterFunc(ibusConnectTimeout, cancel)
	conn, err := dbus.Connect(addr, dbus.WithContext(connCtx))
	if !deadline.Stop() {
		if err == nil {
			_ = conn.Close()
		}
		cancel()
		return nil, fmt.Errorf("connect timeout after %v: %w", ibusConnectTimeout, context.DeadlineExceeded)
	}
	if err != nil {
		cancel()
		return nil, err
	}
	go func() {
		<-conn.Context().Done()
		cancel()
	}()
	return conn, nil
}

func sleepOrWake(ctx context.Context, d time.Duration, wake <-chan struct{}) (ok, woken bool) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false, false
	case <-timer.C:
		return true, false
	case <-wake:
		return true, true
	}
}

// readGlobalEngine reads the GlobalEngine property (avoiding the deprecated
// GetGlobalEngine method and its g_warning) and falls back to GetGlobalEngine
// only when the property is unknown. "No global engine." ⇒ errNoGlobalEngine.
func readGlobalEngine(ctx context.Context, conn *dbus.Conn) (string, error) {
	obj := conn.Object(ibusService, ibusPath)
	var desc dbus.Variant
	err := obj.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0,
		ibusInterface, ibusGlobalEngineProp).Store(&desc)
	if err != nil && isUnknownProperty(err) {
		err = obj.CallWithContext(ctx, ibusInterface+".GetGlobalEngine", 0).Store(&desc)
	}
	if err != nil {
		if isNoGlobalEngine(err) {
			return "", errNoGlobalEngine
		}
		return "", fmt.Errorf("read IBus global engine: %w", err)
	}
	return engineNameFromDesc(desc)
}

// isUnknownProperty matches UnknownProperty and GDBus's older InvalidArgs
// "No such property" reply for the same condition.
func isUnknownProperty(err error) bool {
	if dbusErr, ok := asDBusError(err); ok {
		switch dbusErr.Name {
		case errUnknownProperty:
			return true
		case errInvalidArgs:
			return strings.Contains(dbusErrorMessage(dbusErr), "No such property")
		}
	}
	return false
}

func isNoGlobalEngine(err error) bool {
	if dbusErr, ok := asDBusError(err); ok {
		return strings.Contains(dbusErrorMessage(dbusErr), ibusNoGlobalEngine)
	}
	return strings.Contains(err.Error(), ibusNoGlobalEngine)
}

// asDBusError extracts a D-Bus error reply (godbus returns dbus.Error values;
// pointers are accepted as well).
func asDBusError(err error) (dbus.Error, bool) {
	var value dbus.Error
	if errors.As(err, &value) {
		return value, true
	}
	var ptr *dbus.Error
	if errors.As(err, &ptr) && ptr != nil {
		return *ptr, true
	}
	return dbus.Error{}, false
}

func dbusErrorMessage(err dbus.Error) string {
	if len(err.Body) > 0 {
		if msg, ok := err.Body[0].(string); ok {
			return msg
		}
	}
	return ""
}

// globalEngineFromSignal extracts the engine name from a GlobalEngineChanged
// signal; every other signal (NameAcquired, other members, other paths) is
// ignored.
func globalEngineFromSignal(sig *dbus.Signal) (string, bool) {
	if sig == nil || sig.Name != ibusGlobalEngineChanged || sig.Path != ibusPath || len(sig.Body) == 0 {
		return "", false
	}
	name, err := engineNameFromDesc(sig.Body[0])
	if err != nil {
		return "", false
	}
	return name, true
}

// engineNameFromDesc returns the engine name from an IBus value. It first
// unwraps every dbus.Variant level (at most 3): Properties.Get returns the
// serialized IBusEngineDesc double-wrapped (<<('IBusEngineDesc', {}, 'xkb:ru::rus', …)>>),
// GetGlobalEngine single-wrapped. A struct must be an IBusEngineDesc and the
// name is field [2]. A plain string (the GlobalEngineChanged argument, type s)
// is the name itself.
func engineNameFromDesc(v any) (string, error) {
	for range maxVariantDepth {
		variant, ok := v.(dbus.Variant)
		if !ok {
			break
		}
		v = variant.Value()
	}
	switch value := v.(type) {
	case string:
		return value, nil
	case []any:
		if len(value) < 3 {
			return "", fmt.Errorf("IBus engine desc: %d fields, want at least 3", len(value))
		}
		if kind, _ := value[0].(string); kind != ibusEngineDescType {
			return "", fmt.Errorf("IBus engine desc: type %v, want %s", value[0], ibusEngineDescType)
		}
		name, ok := value[2].(string)
		if !ok {
			return "", fmt.Errorf("IBus engine desc: name field is %T, want string", value[2])
		}
		return name, nil
	case dbus.Variant:
		return "", fmt.Errorf("IBus engine desc: more than %d variant levels", maxVariantDepth)
	default:
		return "", fmt.Errorf("IBus engine desc: unexpected value %T", v)
	}
}
