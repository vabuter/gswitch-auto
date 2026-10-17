package ipc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
)

const (
	requestQueueSize = 8
	signalQueueSize  = 64
	suspendTimeout   = 120 * time.Second
	callTimeout      = 2 * time.Second
	closeWaitTimeout = 500 * time.Millisecond

	dbusName      = "org.freedesktop.DBus"
	dbusPath      = dbus.ObjectPath("/org/freedesktop/DBus")
	propertiesIfc = "org.freedesktop.DBus.Properties"
)

var (
	errBusy          = errors.New("AutoEnabled request queue is full")
	errServiceClosed = errors.New("IPC service is closed")
)

// serviceGeteuid is replaced in tests.
var serviceGeteuid = os.Geteuid

// propStore is the part of *prop.Properties used by propLoop.
type propStore interface {
	GetMust(iface, property string) any
	SetMust(iface, property string, v any)
}

// nameWatcher reports when the unique name of a hotkey-suspension owner leaves
// the bus.
type nameWatcher interface {
	// Watch subscribes to NameOwnerChanged for name and reports whether the
	// name currently has an owner.
	Watch(name string) (bool, error)
	Unwatch(name string)
}

type signalMsg struct {
	member string
	args   []any
}

// Service exports the daemon interface on the session bus.
//
// Locking contract (godbus v5.2.2 prop.Properties.Set holds the property mutex
// while it runs the property callback, and SetMust takes the same
// non-reentrant mutex): the AutoEnabled callback only enqueues the request;
// requestLoop calls the Controller with no lock held; propLoop is the only
// goroutine that calls SetMust and emits interface signals. UpdateStatus and
// Notify* never block.
type Service struct {
	ctl  Controller
	logf func(string, ...any)

	conn      *dbus.Conn // nil in unit tests
	props     propStore
	propMu    sync.RWMutex // serialises SetMust with Get/GetAll reply construction
	emit      func(member string, args ...any) error
	names     nameWatcher
	afterFunc func(time.Duration, func()) func() bool

	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closed    atomic.Bool
	closeOnce sync.Once

	statusMu   sync.Mutex
	pending    *Status
	resyncAuto bool
	wake       chan struct{}

	signals  chan signalMsg
	requests chan bool

	suspendMu sync.Mutex
	susp      suspension
	suspGen   uint64
}

func newService(ctl Controller, logf func(string, ...any)) *Service {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Service{
		ctl:  ctl,
		logf: logf,
		afterFunc: func(d time.Duration, f func()) func() bool {
			return time.AfterFunc(d, f).Stop
		},
		wake:     make(chan struct{}, 1),
		signals:  make(chan signalMsg, signalQueueSize),
		requests: make(chan bool, requestQueueSize),
	}
}

// Start connects a private session-bus connection, exports the daemon
// interface and requests BusName. It returns ErrNameTaken when another process
// owns the name and ErrRootMode when running as root; the caller logs the error
// and continues without IPC. The service is closed when ctx is done.
func Start(ctx context.Context, ctl Controller, logf func(string, ...any)) (*Service, error) {
	if ctl == nil {
		return nil, errors.New("ipc: nil controller")
	}
	if serviceGeteuid() == 0 {
		return nil, ErrRootMode
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("connect to session bus: %w", err)
	}
	s := newService(ctl, logf)
	s.conn = conn
	nameSignals := make(chan *dbus.Signal, 16)
	conn.Signal(nameSignals)
	if err := s.export(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	loopCtx := s.startLoops()
	s.wg.Add(1)
	go s.nameLoop(loopCtx, nameSignals)
	go func() {
		// Not on wg: it may itself be the caller of Close.
		select {
		case <-ctx.Done():
			s.Close()
		case <-loopCtx.Done():
		}
	}()
	return s, nil
}

func (s *Service) export(ctx context.Context) error {
	props, err := prop.Export(s.conn, ObjectPath, prop.Map{Interface: s.propSpec(s.ctl.Snapshot())})
	if err != nil {
		return fmt.Errorf("export properties: %w", err)
	}
	s.props = props
	// Replace prop's own Get/GetAll export by the guarded one (see guardedProps).
	if err := s.conn.Export(&guardedProps{mu: &s.propMu, p: props}, ObjectPath, propertiesIfc); err != nil {
		return fmt.Errorf("export properties: %w", err)
	}
	if err := s.conn.Export(&methods{s: s}, ObjectPath, Interface); err != nil {
		return fmt.Errorf("export methods: %w", err)
	}
	node := introspect.NewIntrospectable(serviceNode())
	if err := s.conn.Export(node, ObjectPath, "org.freedesktop.DBus.Introspectable"); err != nil {
		return fmt.Errorf("export introspection: %w", err)
	}
	s.emit = func(member string, args ...any) error {
		return s.conn.Emit(ObjectPath, Interface+"."+member, args...)
	}
	s.names = busNameWatcher{conn: s.conn}

	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	var reply uint32
	err = s.conn.BusObject().CallWithContext(callCtx, dbusName+".RequestName", 0,
		BusName, uint32(dbus.NameFlagDoNotQueue)).Store(&reply)
	if err != nil {
		return fmt.Errorf("request bus name %s: %w", BusName, err)
	}
	if dbus.RequestNameReply(reply) != dbus.RequestNameReplyPrimaryOwner {
		return ErrNameTaken
	}
	return nil
}

func (s *Service) propSpec(st Status) map[string]*prop.Prop {
	values := statusValues(st)
	spec := make(map[string]*prop.Prop, len(values))
	for _, p := range interfaceData.Properties {
		pp := &prop.Prop{Value: values[p.Name], Emit: prop.EmitTrue}
		if p.Name == propAutoEnabled {
			pp.Writable = true
			pp.Callback = s.onAutoEnabledSet
		}
		spec[p.Name] = pp
	}
	return spec
}

// startLoops starts propLoop and requestLoop and returns their context.
func (s *Service) startLoops() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.wg.Add(2)
	go s.propLoop(ctx)
	go s.requestLoop(ctx)
	return ctx
}

// Close stops the loops, ends an active hotkey suspension and closes the
// private connection. It is idempotent and safe on a nil Service.
func (s *Service) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		if s.cancel != nil {
			s.cancel()
		}
		done := make(chan struct{})
		go func() {
			s.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(closeWaitTimeout):
			s.logf("IPC: service loops did not stop within %s", closeWaitTimeout)
		}
		s.endSuspensionOnClose()
		if s.conn != nil {
			_ = s.conn.Close()
		}
	})
}

// UpdateStatus publishes st. It never blocks: the latest snapshot replaces any
// unapplied one and propLoop updates only the properties that differ from the
// exported values.
func (s *Service) UpdateStatus(st Status) {
	if s == nil || s.closed.Load() {
		return
	}
	c := cloneStatus(st)
	s.statusMu.Lock()
	s.pending = &c
	s.statusMu.Unlock()
	s.poke()
}

// NotifyAutoEnabled queues the AutoEnabledChanged signal. It never blocks.
func (s *Service) NotifyAutoEnabled(enabled bool, source string) {
	s.queueSignal(signalMsg{member: sigAutoEnabledChanged, args: []any{enabled, source}})
}

// NotifyCorrected queues the Corrected signal. It never blocks.
func (s *Service) NotifyCorrected(c Correction) {
	s.queueSignal(signalMsg{member: sigCorrected, args: []any{c.From, c.To, c.Direction, unixSeconds(c.Time)}})
}

// NotifyUndone queues the CorrectionUndone signal. It never blocks.
func (s *Service) NotifyUndone(word string, learned bool) {
	s.queueSignal(signalMsg{member: sigCorrectionUndone, args: []any{word, learned}})
}

func (s *Service) queueSignal(m signalMsg) {
	if s == nil || s.closed.Load() {
		return
	}
	select {
	case s.signals <- m:
		s.poke()
	default:
		s.logf("IPC: signal queue full, dropping %s", m.member)
	}
}

func (s *Service) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// onAutoEnabledSet runs inside prop.Properties.Set with the property mutex
// held: it must not call the Controller or any Service method.
func (s *Service) onAutoEnabledSet(c *prop.Change) *dbus.Error {
	v, ok := c.Value.(bool)
	if !ok {
		return prop.ErrInvalidArg
	}
	select {
	case s.requests <- v:
		return nil
	default:
		return dbus.MakeFailedError(errBusy)
	}
}

func (s *Service) requestLoop(ctx context.Context) {
	defer s.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case v := <-s.requests:
			if err := s.ctl.SetAutoEnabled(v, SourceDBus); err != nil {
				s.logf("IPC: AutoEnabled=%t request refused: %v", v, err)
			}
			// Revert a value stored by the property Set that the controller
			// refused (and confirm an accepted one) from the controller state.
			s.statusMu.Lock()
			s.resyncAuto = true
			s.statusMu.Unlock()
			s.poke()
		}
	}
}

func (s *Service) propLoop(ctx context.Context) {
	defer s.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
			s.flush()
		}
	}
}

// flush applies the latest status snapshot, then emits queued signals in FIFO
// order. Runs on propLoop only.
func (s *Service) flush() {
	s.statusMu.Lock()
	st, resync := s.pending, s.resyncAuto
	s.pending, s.resyncAuto = nil, false
	s.statusMu.Unlock()

	if st != nil {
		s.applyValues(statusValues(*st))
	}
	if resync {
		s.applyValues(map[string]any{propAutoEnabled: s.ctl.AutoEnabled()})
	}
	for {
		select {
		case m := <-s.signals:
			s.emitSignal(m)
		default:
			return
		}
	}
}

// applyValues sets every property in values that differs from its exported
// value. The comparison reads the exported value (GetMust takes the property
// read lock), not a private cache, so it waits for an in-flight property Set
// and reverts a stored value that the controller refused.
func (s *Service) applyValues(values map[string]any) {
	s.propMu.Lock()
	defer s.propMu.Unlock()
	for _, name := range propertyNames {
		want, ok := values[name]
		if !ok {
			continue
		}
		cur := s.props.GetMust(Interface, name)
		if name == propStats {
			want = keepStatsKeys(cur, want)
		}
		if reflect.DeepEqual(cur, want) {
			continue
		}
		s.setMust(name, want)
	}
}

// keepStatsKeys zero-fills keys that the exported map has and want lacks:
// dbus.Store merges into the stored map and cannot delete keys.
func keepStatsKeys(cur, want any) any {
	c, ok1 := cur.(map[string]uint64)
	w, ok2 := want.(map[string]uint64)
	if !ok1 || !ok2 {
		return want
	}
	for k := range c {
		if _, ok := w[k]; !ok {
			w[k] = 0
		}
	}
	return w
}

func (s *Service) setMust(name string, v any) {
	defer func() {
		if r := recover(); r != nil {
			s.logf("IPC: update property %s: %v", name, r)
		}
	}()
	s.props.SetMust(Interface, name, v)
}

func (s *Service) emitSignal(m signalMsg) {
	if m.member == sigAutoEnabledChanged && len(m.args) > 0 {
		// AutoEnabledChanged follows the matching property update.
		s.applyValues(map[string]any{propAutoEnabled: m.args[0]})
	}
	if s.emit == nil {
		return
	}
	if err := s.emit(m.member, m.args...); err != nil {
		s.logf("IPC: emit %s: %v", m.member, err)
	}
}

// guardedProps is exported as org.freedesktop.DBus.Properties in place of
// prop's own export. godbus encodes a Get/GetAll reply after the property lock
// is released, and dbus.Store mutates a stored map or slice in place, so a
// reply holding the live value could race with propLoop's SetMust (a concurrent
// map read and write is fatal). Replies therefore carry copies made under
// propMu, which propLoop holds around SetMust. Set is delegated unchanged and
// does not take propMu.
type guardedProps struct {
	mu *sync.RWMutex
	p  *prop.Properties
}

func (g *guardedProps) Get(iface, property string) (dbus.Variant, *dbus.Error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	v, err := g.p.Get(iface, property)
	if err != nil {
		return v, err
	}
	return dbus.MakeVariant(cloneValue(v.Value())), nil
}

func (g *guardedProps) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	all, err := g.p.GetAll(iface)
	if err != nil {
		return nil, err
	}
	out := make(map[string]dbus.Variant, len(all))
	for name, v := range all {
		out[name] = dbus.MakeVariant(cloneValue(v.Value()))
	}
	return out, nil
}

func (g *guardedProps) Set(iface, property string, newv dbus.Variant) *dbus.Error {
	return g.p.Set(iface, property, newv)
}

// methods holds the exported D-Bus methods. Handlers run on godbus goroutines
// without the property lock and may call the Controller synchronously.
type methods struct{ s *Service }

func (m *methods) SetAutoEnabled(enabled bool) *dbus.Error {
	if m.s.closed.Load() {
		return dbus.NewError(ErrNotAvailable, []any{errServiceClosed.Error()})
	}
	if err := m.s.ctl.SetAutoEnabled(enabled, SourceDBus); err != nil {
		return dbus.NewError(ErrNotAvailable, []any{err.Error()})
	}
	return nil
}

func (m *methods) Reload() *dbus.Error {
	if m.s.closed.Load() {
		return dbus.NewError(ErrNotAvailable, []any{errServiceClosed.Error()})
	}
	if err := m.s.ctl.Reload(); err != nil {
		return dbus.NewError(ErrConfig, []any{err.Error()})
	}
	return nil
}

func (m *methods) SuspendHotkeys(sender dbus.Sender, suspend bool) *dbus.Error {
	if m.s.closed.Load() {
		return dbus.NewError(ErrNotAvailable, []any{errServiceClosed.Error()})
	}
	if err := m.s.suspendHotkeys(string(sender), suspend); err != nil {
		return dbus.NewError(ErrNotAvailable, []any{err.Error()})
	}
	return nil
}
