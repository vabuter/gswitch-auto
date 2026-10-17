package focus

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/arumata/gswitch/internal/gswitch/fold"
)

const (
	clientRetryMin     = time.Second
	clientRetryMax     = 30 * time.Second
	clientCloseTimeout = time.Second
	// clientFocusRetryMin and clientFocusRetryMax bound the backoff of the
	// GetFocus retry after a failed read (for example a Shell main loop busy
	// with the unlock animation when the extension reappears).
	clientFocusRetryMin = 250 * time.Millisecond
	clientFocusRetryMax = 5 * time.Second
	// clientSignalBuffer keeps godbus from falling back to its unordered
	// deferred delivery (one goroutine per signal) during short bursts: the
	// order of Disabling and NameOwnerChanged matters for the lock state.
	clientSignalBuffer = 64

	dbusName               = "org.freedesktop.DBus"
	dbusPath               = dbus.ObjectPath("/org/freedesktop/DBus")
	dbusNameOwnerChanged   = dbusName + ".NameOwnerChanged"
	dbusErrServiceUnknown  = "org.freedesktop.DBus.Error.ServiceUnknown"
	dbusErrNameHasNoOwner  = "org.freedesktop.DBus.Error.NameHasNoOwner"
	signalFocusChanged     = Interface + ".FocusChanged"
	signalPurposeChanged   = Interface + ".InputPurposeChanged"
	signalDisabling        = Interface + ".Disabling"
	signalSSActiveChanged  = ScreenSaverName + ".ActiveChanged"
	methodGetFocus         = Interface + ".GetFocus"
	methodListRunningApps  = Interface + ".ListRunningApps"
	methodSSGetActive      = ScreenSaverName + ".GetActive"
	disablingReasonLocked  = "locked"
	disablingReasonGeneric = "disabled"
)

var errConnectionClosed = errors.New("session bus connection closed")

// Options configures a Client.
type Options struct {
	Address     string        // "" ⇒ dbus.ConnectSessionBus()
	CallTimeout time.Duration // per D-Bus call; <= 0 ⇒ DefaultCallTimeout
	OnChange    func(State)   // client goroutine; must not block
	Logf        func(string, ...any)
}

// Client follows the focus extension on the session bus. It uses its own
// private connection (never the shared dbus.SessionBus(), C27), reconnects
// with a 1 s..30 s backoff and publishes lock-free snapshots.
type Client struct {
	opts  Options
	state atomic.Pointer[State]

	mu      sync.Mutex
	started bool
	closed  bool
	cancel  context.CancelFunc
	done    chan struct{}

	// Owned by the client goroutine.
	disabling         string // reason of a Disabling signal not yet followed by the vanish
	screenSaverActive bool
	focusRetry        *time.Timer // pending GetFocus retry (State.FocusUnknown); nil: none
	focusRetryDelay   time.Duration
}

// New returns a client that does nothing until Start.
func New(opts Options) *Client {
	if opts.CallTimeout <= 0 {
		opts.CallTimeout = DefaultCallTimeout
	}
	c := &Client{opts: opts, done: make(chan struct{})}
	c.state.Store(&State{Purpose: PurposeUnknown})
	return c
}

// Start connects in the background and returns immediately. It is a no-op
// when the client was started or closed already. The client stops when ctx is
// done or Close is called.
func (c *Client) Start(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started || c.closed {
		return
	}
	c.started = true
	runCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	go c.run(runCtx)
}

// Snapshot returns the latest state. It is lock-free and safe for concurrent
// use.
func (c *Client) Snapshot() State {
	return *c.state.Load()
}

// Close stops the client and waits up to one second for its goroutine. It is
// idempotent.
func (c *Client) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	started := c.started
	if c.cancel != nil {
		c.cancel()
	}
	c.mu.Unlock()
	if !started {
		return
	}
	select {
	case <-c.done:
	case <-time.After(clientCloseTimeout):
		c.logf("focus: client did not stop within %v", clientCloseTimeout)
	}
}

func (c *Client) logf(format string, args ...any) {
	if c.opts.Logf != nil {
		c.opts.Logf(format, args...)
	}
}

func (c *Client) run(ctx context.Context) {
	defer close(c.done)
	backoff := clientRetryMin
	for {
		conn, err := c.connect(ctx)
		if err == nil {
			established, sessErr := c.session(ctx, conn)
			_ = conn.Close()
			if ctx.Err() != nil {
				return
			}
			c.onDisconnected()
			if established {
				backoff = clientRetryMin
			}
			err = sessErr
		}
		if ctx.Err() != nil {
			return
		}
		c.logf("focus: session bus: %v; retrying in %v", err, backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff = min(backoff*2, clientRetryMax)
	}
}

func (c *Client) connect(ctx context.Context) (*dbus.Conn, error) {
	if c.opts.Address == "" {
		conn, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
		if err != nil {
			return nil, fmt.Errorf("connect session bus: %w", err)
		}
		return conn, nil
	}
	conn, err := dbus.Connect(c.opts.Address, dbus.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", c.opts.Address, err)
	}
	return conn, nil
}

func (c *Client) callContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, c.opts.CallTimeout)
}

// session serves one connection until it closes or ctx is done. established
// reports whether the match rules were installed.
func (c *Client) session(ctx context.Context, conn *dbus.Conn) (established bool, err error) {
	ch := make(chan *dbus.Signal, clientSignalBuffer)
	conn.Signal(ch)
	defer conn.RemoveSignal(ch)
	defer c.stopFocusRetry()

	// Matches go in before the initial NameHasOwner/GetFocus/GetActive calls
	// so that no transition between the query and the subscription is lost.
	if err := c.addMatches(ctx, conn); err != nil {
		return false, err
	}
	c.refreshOwner(ctx, conn)
	c.refreshScreenSaver(ctx, conn)

	for {
		var retry <-chan time.Time
		if c.focusRetry != nil {
			retry = c.focusRetry.C
		}
		select {
		case <-ctx.Done():
			return true, nil
		case sig, ok := <-ch:
			if !ok {
				return true, errConnectionClosed
			}
			c.handleSignal(ctx, conn, sig)
		case <-retry:
			c.focusRetry = nil
			c.retryFocus(ctx, conn)
		}
	}
}

func (c *Client) addMatches(ctx context.Context, conn *dbus.Conn) error {
	rules := [][]dbus.MatchOption{
		{
			dbus.WithMatchSender(dbusName),
			dbus.WithMatchObjectPath(dbusPath),
			dbus.WithMatchInterface(dbusName),
			dbus.WithMatchMember("NameOwnerChanged"),
			dbus.WithMatchArg(0, BusName),
		},
		{
			dbus.WithMatchSender(BusName),
			dbus.WithMatchObjectPath(ObjectPath),
			dbus.WithMatchInterface(Interface),
		},
		{
			dbus.WithMatchSender(ScreenSaverName),
			dbus.WithMatchObjectPath(ScreenSaverPath),
			dbus.WithMatchInterface(ScreenSaverName),
			dbus.WithMatchMember("ActiveChanged"),
		},
	}
	for _, rule := range rules {
		callCtx, cancel := c.callContext(ctx)
		err := conn.AddMatchSignalContext(callCtx, rule...)
		cancel()
		if err != nil {
			return fmt.Errorf("add match rule: %w", err)
		}
	}
	return nil
}

func (c *Client) refreshOwner(ctx context.Context, conn *dbus.Conn) {
	callCtx, cancel := c.callContext(ctx)
	defer cancel()
	var owned bool
	err := conn.BusObject().CallWithContext(callCtx, dbusName+".NameHasOwner", 0, BusName).Store(&owned)
	if err != nil {
		c.logf("focus: NameHasOwner(%s): %v", BusName, err)
		return
	}
	if owned {
		c.onAppeared(ctx, conn)
	}
}

func (c *Client) refreshScreenSaver(ctx context.Context, conn *dbus.Conn) {
	callCtx, cancel := c.callContext(ctx)
	defer cancel()
	var active bool
	err := conn.Object(ScreenSaverName, ScreenSaverPath).
		CallWithContext(callCtx, methodSSGetActive, dbus.FlagNoAutoStart).Store(&active)
	if err != nil {
		c.logf("focus: %s.GetActive: %v", ScreenSaverName, err)
		return
	}
	if active {
		c.onScreenSaver(true)
	}
}

func (c *Client) handleSignal(ctx context.Context, conn *dbus.Conn, sig *dbus.Signal) {
	switch {
	case sig.Name == dbusNameOwnerChanged && sig.Path == dbusPath:
		var name, oldOwner, newOwner string
		if dbus.Store(sig.Body, &name, &oldOwner, &newOwner) != nil || name != BusName {
			return
		}
		if newOwner != "" {
			c.onAppeared(ctx, conn)
		} else {
			c.onVanished()
		}
	case sig.Path == ObjectPath && sig.Name == signalFocusChanged:
		var appID, wmClass string
		if dbus.Store(sig.Body, &appID, &wmClass) != nil {
			return
		}
		c.onFocus(appID, wmClass)
	case sig.Path == ObjectPath && sig.Name == signalPurposeChanged:
		var purpose string
		if dbus.Store(sig.Body, &purpose) != nil {
			return
		}
		c.onPurpose(knownPurpose(purpose))
	case sig.Path == ObjectPath && sig.Name == signalDisabling:
		var reason string
		if dbus.Store(sig.Body, &reason) != nil {
			return
		}
		c.onDisabling(reason)
	case sig.Path == ScreenSaverPath && sig.Name == signalSSActiveChanged:
		var active bool
		if dbus.Store(sig.Body, &active) != nil {
			return
		}
		c.onScreenSaver(active)
	}
}

// onAppeared handles the extension name being owned (at start or later):
// the extension's own lock is lifted unless the screen saver still reports
// the session as locked, and the focus is re-read. When GetFocus fails the
// state is published with FocusUnknown (the policy denies) and the read is
// retried with a backoff, because the extension emits no FocusChanged when
// the focused window did not change (e.g. across a screen lock).
func (c *Client) onAppeared(ctx context.Context, conn *dbus.Conn) {
	st := c.Snapshot()
	st.Available = true
	st.EverAvailable = true
	c.disabling = ""
	if st.LockReason == LockReasonExtensionLocked || st.LockReason == LockReasonExtensionVanished {
		st.Locked, st.LockReason = false, ""
		if c.screenSaverActive {
			st.Locked, st.LockReason = true, LockReasonScreenSaver
		}
	}

	c.stopFocusRetry()
	if !c.readFocus(ctx, conn, &st) {
		c.scheduleFocusRetry(clientFocusRetryMin)
	}
	c.commit(st, false)
}

// readFocus calls GetFocus and stores the result in st. On failure st keeps
// its application and purpose and is marked FocusUnknown.
func (c *Client) readFocus(ctx context.Context, conn *dbus.Conn, st *State) bool {
	callCtx, cancel := c.callContext(ctx)
	defer cancel()
	var appID, wmClass, purpose string
	err := conn.Object(BusName, ObjectPath).
		CallWithContext(callCtx, methodGetFocus, dbus.FlagNoAutoStart).
		Store(&appID, &wmClass, &purpose)
	if err != nil {
		c.logf("focus: GetFocus: %v", err)
		st.FocusUnknown = true
		return false
	}
	setApp(st, appID, wmClass)
	st.Purpose = knownPurpose(purpose)
	st.FocusUnknown = false
	return true
}

// retryFocus re-reads the focus after a failed GetFocus while the extension
// is still available and nothing else has resolved the focus meanwhile.
func (c *Client) retryFocus(ctx context.Context, conn *dbus.Conn) {
	st := c.Snapshot()
	if !st.Available || !st.FocusUnknown {
		return
	}
	if !c.readFocus(ctx, conn, &st) {
		c.scheduleFocusRetry(min(c.focusRetryDelay*2, clientFocusRetryMax))
		return
	}
	c.commit(st, false)
}

func (c *Client) scheduleFocusRetry(delay time.Duration) {
	c.stopFocusRetry()
	c.focusRetryDelay = delay
	c.focusRetry = time.NewTimer(delay)
}

func (c *Client) stopFocusRetry() {
	if c.focusRetry != nil {
		c.focusRetry.Stop()
		c.focusRetry = nil
	}
}

// onVanished handles the extension name losing its owner. Without a
// preceding Disabling the reason is unknown and the state fails closed
// (C7): the Shell disables the extension when the screen locks.
func (c *Client) onVanished() {
	c.stopFocusRetry()
	st := c.Snapshot()
	st.Available = false
	setApp(&st, "", "")
	st.Purpose = PurposeUnknown
	st.FocusUnknown = false
	switch c.disabling {
	case disablingReasonLocked:
		st.Locked, st.LockReason = true, LockReasonExtensionLocked
	case "":
		if st.EverAvailable {
			st.Locked, st.LockReason = true, LockReasonExtensionVanished
		}
	}
	c.disabling = ""
	c.commit(st, false)
}

// onDisabling handles the extension's last signal before it unexports. Any
// reason other than "locked" is treated as "disabled".
func (c *Client) onDisabling(reason string) {
	st := c.Snapshot()
	if reason == disablingReasonLocked {
		c.disabling = disablingReasonLocked
		st.Locked, st.LockReason = true, LockReasonExtensionLocked
	} else {
		c.disabling = disablingReasonGeneric
		c.stopFocusRetry()
		st.Available = false
		setApp(&st, "", "")
		st.Purpose = PurposeUnknown
		st.FocusUnknown = false
	}
	c.commit(st, false)
}

// onFocus handles FocusChanged. Every signal is a focus-window change (also
// within one application), so the sequence number always moves. It resolves
// an unknown focus: the extension reports the purpose of the new window's
// field separately (InputPurposeChanged).
func (c *Client) onFocus(appID, wmClass string) {
	c.stopFocusRetry()
	st := c.Snapshot()
	setApp(&st, appID, wmClass)
	st.FocusUnknown = false
	c.commit(st, true)
}

func (c *Client) onPurpose(purpose Purpose) {
	st := c.Snapshot()
	st.Purpose = purpose
	c.commit(st, false)
}

// onScreenSaver applies the late, best-effort screen saver hint: it can add a
// lock but clears only its own.
func (c *Client) onScreenSaver(active bool) {
	c.screenSaverActive = active
	st := c.Snapshot()
	switch {
	case active && !st.Locked:
		st.Locked, st.LockReason = true, LockReasonScreenSaver
	case !active && st.LockReason == LockReasonScreenSaver:
		st.Locked, st.LockReason = false, ""
	}
	c.commit(st, false)
}

// onDisconnected resets the extension view after the bus connection was
// lost; the next session re-reads it.
func (c *Client) onDisconnected() {
	c.disabling = ""
	c.stopFocusRetry()
	st := c.Snapshot()
	st.Available = false
	setApp(&st, "", "")
	st.Purpose = PurposeUnknown
	st.FocusUnknown = false
	c.commit(st, false)
}

func setApp(st *State, appID, wmClass string) {
	st.AppID = appID
	st.WmClass = wmClass
	st.AppKey = fold.AppKey(appID)
	st.WmClassKey = fold.AppKey(wmClass)
}

// commit publishes st when it differs from the current state (or when force
// is set), bumping Seq, and notifies OnChange.
func (c *Client) commit(st State, force bool) {
	cur := c.state.Load()
	if !force && sameState(*cur, st) {
		return
	}
	st.Seq = cur.Seq + 1
	st.UpdatedAt = time.Now()
	c.state.Store(&st)
	if c.opts.OnChange != nil {
		c.opts.OnChange(st)
	}
}

func sameState(a, b State) bool {
	a.Seq, b.Seq = 0, 0
	a.UpdatedAt, b.UpdatedAt = time.Time{}, time.Time{}
	return a == b
}

// ListRunningApps asks the extension for the running applications. It
// returns an error wrapping ErrUnavailable when the extension's name is not
// owned. conn should be a private session connection (the tray's forkConn).
// Without a deadline on ctx, DefaultCallTimeout applies to each call.
func ListRunningApps(ctx context.Context, conn *dbus.Conn) ([]App, error) {
	if conn == nil {
		return nil, fmt.Errorf("%w: no session bus connection", ErrUnavailable)
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultCallTimeout)
		defer cancel()
	}
	var owned bool
	if err := conn.BusObject().CallWithContext(ctx, dbusName+".NameHasOwner", 0, BusName).Store(&owned); err != nil {
		return nil, fmt.Errorf("check focus extension owner: %w", err)
	}
	if !owned {
		return nil, ErrUnavailable
	}
	var apps []App
	err := conn.Object(BusName, ObjectPath).
		CallWithContext(ctx, methodListRunningApps, dbus.FlagNoAutoStart).Store(&apps)
	if err != nil {
		if name := dbusErrorName(err); name == dbusErrServiceUnknown || name == dbusErrNameHasNoOwner {
			return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		return nil, fmt.Errorf("list running apps: %w", err)
	}
	return apps, nil
}

func dbusErrorName(err error) string {
	var value dbus.Error
	if errors.As(err, &value) {
		return value.Name
	}
	var ptr *dbus.Error
	if errors.As(err, &ptr) && ptr != nil {
		return ptr.Name
	}
	return ""
}
