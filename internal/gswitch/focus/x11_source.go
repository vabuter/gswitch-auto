package focus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/arumata/gswitch/internal/gswitch/detect"
	"github.com/arumata/gswitch/internal/gswitch/x11conn"
)

// gswitch-auto: X11 focus source (SPEC-X11 §3.2.1). In an X11 session the
// active window is public: every EWMH window manager keeps _NET_ACTIVE_WINDOW
// on the root window, and the window's WM_CLASS and _GTK_APPLICATION_ID name
// the application. Only that identity is derived here; X11 has no notion of
// the focused text field, so the purpose stays unknown and strict purpose
// mode still needs the Shell extension (§2.2). The source never reads
// _NET_WM_NAME, WM_NAME or _NET_WM_PID.

const (
	x11RetryMin     = clientRetryMin
	x11RetryMax     = clientRetryMax
	x11CloseTimeout = clientCloseTimeout
	// x11PropertyMaxLen is the GetProperty length in 32-bit units for the
	// window properties WM_CLASS and _GTK_APPLICATION_ID (1 KiB);
	// x11ActiveWindowLen is the one unit of the WINDOW value of
	// _NET_ACTIVE_WINDOW.
	x11PropertyMaxLen  = 256
	x11ActiveWindowLen = 1
	// x11ConnectTimeout bounds the socket connect and the X connection setup;
	// x11ReplyTimeout bounds every reply (InternAtom, the event-mask Check,
	// GetProperty). A reply that does not arrive in time ends the session
	// like a lost connection.
	x11ConnectTimeout = 2 * time.Second
	x11ReplyTimeout   = time.Second

	x11AtomNetActiveWindow  = "_NET_ACTIVE_WINDOW"
	x11AtomGtkApplicationID = "_GTK_APPLICATION_ID"
	x11AtomUTF8String       = "UTF8_STRING"
)

// AppSource values returned by Merged.AppSource and printed by the
// diagnostics: where the application identity of a focus State comes from.
const (
	AppSourceExtension = "extension"
	AppSourceX11       = "x11"
)

var (
	// errX11BadWindow is returned by x11Conn.GetProperty when the window is
	// gone (X BadWindow): the active window closed between the notification
	// and the read.
	errX11BadWindow = errors.New("bad window")
	// errX11Disconnected ends a session whose event stream closed while the
	// source was still running.
	errX11Disconnected = errors.New("X11 connection closed")
	// errX11ReplyTimeout is wrapped by a request whose reply did not arrive
	// within x11ReplyTimeout (a hung or stalled server); it ends the session.
	errX11ReplyTimeout = errors.New("no reply from the X server")
)

// X11State is the raw X11 view (no purpose information exists on X11).
type X11State struct {
	Connected bool
	Window    uint32 // _NET_ACTIVE_WINDOW; 0 = none
	AppID     string // _GTK_APPLICATION_ID (UTF8_STRING), "" when absent
	WmClass   string // WM_CLASS class part (second string), "" when absent
	Seq       uint64 // bumps on every change of Window/AppID/WmClass/Connected
	UpdatedAt time.Time
}

// X11Options configures NewX11Source.
type X11Options struct {
	Display  string         // "" ⇒ $DISPLAY
	OnChange func(X11State) // source goroutine; must not block
	Logf     func(string, ...any)
}

// X11Source follows _NET_ACTIVE_WINDOW on the root window over its own X
// connection: it selects PropertyNotify on the root, reads the active window
// once, and re-reads it on every PropertyNotify for _NET_ACTIVE_WINDOW (other
// root properties such as _NET_CLIENT_LIST_STACKING are ignored). For the
// active window it reads WM_CLASS (class part) and _GTK_APPLICATION_ID; a
// window that is already gone (BadWindow) yields Window 0 and empty fields.
// On connection loss the source publishes Connected=false once and
// reconnects with the client's 1 s..30 s backoff. The connect and setup are
// bounded by ctx and x11ConnectTimeout, every reply by x11ReplyTimeout (a
// reply that does not come ends the session like a lost connection), and the
// connection is closed as soon as ctx ends, so Close never waits on a hung
// server. Start, Close and Snapshot behave like Client's (lock-free atomic
// snapshots).
type X11Source struct {
	opts  X11Options
	state atomic.Pointer[X11State]

	// Test hooks; production values are set by NewX11Source.
	dial     func(ctx context.Context, display string) (x11Conn, error)
	retryMin time.Duration
	retryMax time.Duration

	mu      sync.Mutex
	started bool
	closed  bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// NewX11Source returns a source that does nothing until Start.
func NewX11Source(opts X11Options) *X11Source {
	s := &X11Source{
		opts:     opts,
		dial:     x11Dial,
		retryMin: x11RetryMin,
		retryMax: x11RetryMax,
		done:     make(chan struct{}),
	}
	s.state.Store(&X11State{})
	return s
}

// Start connects in the background and returns immediately. It is a no-op
// when the source was started or closed already. The source stops when ctx
// is done or Close is called.
func (s *X11Source) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.closed {
		return
	}
	s.started = true
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	go s.run(runCtx)
}

// Snapshot returns the latest state. It is lock-free and safe for concurrent
// use.
func (s *X11Source) Snapshot() X11State {
	return *s.state.Load()
}

// Close stops the source and waits up to one second for its goroutine. It is
// idempotent.
func (s *X11Source) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	started := s.started
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	if !started {
		return
	}
	select {
	case <-s.done:
	case <-time.After(x11CloseTimeout):
		s.logf("focus: x11 source did not stop within %v", x11CloseTimeout)
	}
}

func (s *X11Source) logf(format string, args ...any) {
	if s.opts.Logf != nil {
		s.opts.Logf(format, args...)
	}
}

func (s *X11Source) run(ctx context.Context) {
	defer close(s.done)
	backoff := s.retryMin
	for {
		connected, established, err := s.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if connected {
			s.onDisconnected()
		}
		if established {
			backoff = s.retryMin
		}
		s.logf("focus: x11: %v; retrying in %v", err, backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff = min(backoff*2, s.retryMax)
	}
}

// session serves one X connection until it closes or ctx is done. connected
// reports that the connection was opened (its view must be reset),
// established that the subscription was installed and a state published.
func (s *X11Source) session(ctx context.Context) (connected, established bool, err error) {
	conn, err := s.dial(ctx, s.opts.Display)
	if err != nil {
		return false, false, fmt.Errorf("connect to X11: %w", err)
	}
	defer conn.Close()

	// WaitForEvent has no context; closing the connection is the only way to
	// unblock the loop (and any pending reply) on shutdown, so the watcher
	// starts before the first request. The wrapper's Close is idempotent.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-stop:
		}
	}()

	sess, err := newX11Session(conn)
	if err != nil {
		return true, false, err
	}
	if err := sess.conn.SelectPropertyChanges(sess.root); err != nil {
		return true, false, fmt.Errorf("select PropertyNotify on the root window: %w", err)
	}
	if err := s.publish(ctx, sess); err != nil {
		return true, false, err
	}

	for {
		ev, err := sess.conn.NextPropertyNotify()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return true, true, errX11Disconnected
			}
			s.logf("focus: x11: %v", err)
			continue
		}
		if ev.Window != sess.root || ev.Atom != sess.atomActive {
			continue
		}
		if err := s.publish(ctx, sess); err != nil {
			return true, true, err
		}
	}
}

// publish reads the active window and commits the result. Any read error
// other than a closed connection or a reply timeout is logged and the window
// identity is empty for this round. It returns an error, publishing nothing,
// when the connection closed during the read (server gone, or Close/ctx
// shutting the session down) or the server did not answer within
// x11ReplyTimeout: the caller ends the session, so a lost or hung connection
// yields exactly one Connected=false change and a shutdown none.
func (s *X11Source) publish(ctx context.Context, sess *x11Session) error {
	st, err := sess.read()
	if err != nil {
		switch {
		case errors.Is(err, io.EOF) || ctx.Err() != nil:
			return errX11Disconnected
		case errors.Is(err, errX11ReplyTimeout):
			return err
		}
		s.logf("focus: x11: %v", err)
	}
	s.commit(st)
	return nil
}

// onDisconnected resets the view after the connection was lost; the next
// session re-reads it.
func (s *X11Source) onDisconnected() {
	s.commit(X11State{})
}

// commit publishes st when it differs from the current state, bumping Seq,
// and notifies OnChange. It runs on the source goroutine only.
func (s *X11Source) commit(st X11State) {
	cur := s.state.Load()
	if sameX11State(*cur, st) {
		return
	}
	st.Seq = cur.Seq + 1
	st.UpdatedAt = time.Now()
	s.state.Store(&st)
	if s.opts.OnChange != nil {
		s.opts.OnChange(st)
	}
}

func sameX11State(a, b X11State) bool {
	a.Seq, b.Seq = 0, 0
	a.UpdatedAt, b.UpdatedAt = time.Time{}, time.Time{}
	return a == b
}

// X11FocusEnabled reports whether the EWMH source should run for the
// auto.conf focus-source value: "auto" in an X11 session, never for root.
// "X11 session" is the SPEC-X11 §0 rule shared with the layout source
// (layoutstate.X11SourceEnabled): with a SessionEnv, SessionType x11 or a
// Display and nothing Wayland; without one, XDG_SESSION_TYPE=x11 with
// WAYLAND_DISPLAY empty (DISPLAY alone never counts, K-X3). Unlike the
// layout source it does not require GNOME: _NET_ACTIVE_WINDOW is set by
// every EWMH window manager.
func X11FocusEnabled(focusSource string, env *detect.SessionEnv, euid int) bool {
	if euid == 0 || focusSource != "auto" {
		return false
	}
	return isX11Session(env)
}

func isX11Session(env *detect.SessionEnv) bool {
	if env != nil {
		if strings.EqualFold(env.SessionType, "wayland") || env.WaylandDisplay != "" {
			return false
		}
		return strings.EqualFold(env.SessionType, "x11") || env.Display != ""
	}
	return strings.EqualFold(os.Getenv("XDG_SESSION_TYPE"), "x11") &&
		os.Getenv("WAYLAND_DISPLAY") == ""
}

// ParseWMClass returns the class part of a WM_CLASS value
// ("instance\0class\0"): the second NUL-terminated string, with or without
// its terminator. It returns "" when there is no second string.
func ParseWMClass(value []byte) string {
	first := bytes.IndexByte(value, 0)
	if first < 0 {
		return ""
	}
	rest := value[first+1:]
	if end := bytes.IndexByte(rest, 0); end >= 0 {
		rest = rest[:end]
	}
	return string(rest)
}

// x11String converts a STRING/UTF8_STRING property value, dropping a
// trailing NUL some clients add.
func x11String(value []byte) string {
	return strings.TrimRight(string(value), "\x00")
}

// x11PropertyNotify is a PropertyNotify event reduced to what the source
// needs.
type x11PropertyNotify struct {
	Window, Atom uint32
}

// x11Conn is the slice of an X connection the source uses; x11XGBConn wraps
// *xgb.Conn and tests supply a fake. Close may be called from another
// goroutine than the other methods and must be idempotent.
type x11Conn interface {
	// Root returns the root window of the default screen.
	Root() uint32
	// InternAtom returns the atom named name, creating it when needed.
	InternAtom(name string) (uint32, error)
	// SelectPropertyChanges subscribes this connection to PropertyNotify
	// events of window.
	SelectPropertyChanges(window uint32) error
	// GetProperty reads at most length 32-bit units of property of window
	// as typ. The value is empty when the property is absent or has another
	// type; a window that no longer exists yields an error wrapping
	// errX11BadWindow; a connection closed before the reply arrived yields
	// an error wrapping io.EOF.
	GetProperty(window, property, typ, length uint32) ([]byte, error)
	// NextPropertyNotify blocks until the next PropertyNotify event. Other
	// events are skipped; an X error delivered to the event queue is
	// returned as is (the caller logs it and calls again); io.EOF reports
	// that the connection is closed.
	NextPropertyNotify() (x11PropertyNotify, error)
	Close()
}

// x11Dial opens the production connection; tests replace it.
var x11Dial = dialX11

// dialX11 connects to display ("" ⇒ $DISPLAY) through x11conn.Dial: the
// socket connect and the connection setup are bounded by ctx and
// x11ConnectTimeout and leave nothing behind on expiry. A screen number the
// server does not have is an error.
func dialX11(ctx context.Context, display string) (x11Conn, error) {
	if display == "" && os.Getenv("DISPLAY") == "" {
		return nil, errors.New("DISPLAY is not set")
	}
	dialCtx, cancel := context.WithTimeout(ctx, x11ConnectTimeout)
	defer cancel()
	conn, err := x11conn.Dial(dialCtx, display)
	if err != nil {
		return nil, err
	}
	root, err := conn.DefaultRoot()
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &x11XGBConn{x: conn, conn: conn.X(), root: uint32(root), replyTimeout: x11ReplyTimeout}, nil
}

// x11XGBConn implements x11Conn on jezek/xgb. Every reply is bounded by
// replyTimeout (x11conn.Await); Close is bounded by x11conn.CloseTimeout and
// makes every pending reply return io.EOF.
type x11XGBConn struct {
	x            *x11conn.Conn
	conn         *xgb.Conn
	root         uint32
	replyTimeout time.Duration
}

// x11Await bounds one reply by timeout; on expiry the error wraps
// errX11ReplyTimeout.
func x11Await[T any](timeout time.Duration, call func() (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	value, err := x11conn.Await(ctx, call)
	if err != nil && errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil {
		return value, fmt.Errorf("%w within %v", errX11ReplyTimeout, timeout)
	}
	return value, err
}

func (c *x11XGBConn) Root() uint32 { return c.root }

func (c *x11XGBConn) InternAtom(name string) (uint32, error) {
	reply, err := x11Await(c.replyTimeout, func() (*xproto.InternAtomReply, error) {
		return xproto.InternAtom(c.conn, false, uint16(len(name)), name).Reply()
	})
	if err != nil {
		return 0, err
	}
	if reply == nil {
		return 0, errors.New("InternAtom: no reply")
	}
	return uint32(reply.Atom), nil
}

func (c *x11XGBConn) SelectPropertyChanges(window uint32) error {
	_, err := x11Await(c.replyTimeout, func() (struct{}, error) {
		return struct{}{}, xproto.ChangeWindowAttributesChecked(c.conn, xproto.Window(window),
			xproto.CwEventMask, []uint32{xproto.EventMaskPropertyChange}).Check()
	})
	return err
}

func (c *x11XGBConn) GetProperty(window, property, typ, length uint32) ([]byte, error) {
	// xgb's Reply returns io.EOF itself once the connection is closed.
	reply, err := x11Await(c.replyTimeout, func() (*xproto.GetPropertyReply, error) {
		return xproto.GetProperty(c.conn, false, xproto.Window(window),
			xproto.Atom(property), xproto.Atom(typ), 0, length).Reply()
	})
	if err != nil {
		var bad xproto.WindowError
		if errors.As(err, &bad) {
			return nil, fmt.Errorf("%w 0x%x", errX11BadWindow, bad.BadValue)
		}
		return nil, err
	}
	if reply == nil || reply.Type != xproto.Atom(typ) {
		return nil, nil
	}
	return reply.Value, nil
}

func (c *x11XGBConn) NextPropertyNotify() (x11PropertyNotify, error) {
	for {
		ev, xerr := c.conn.WaitForEvent()
		if ev == nil && xerr == nil {
			return x11PropertyNotify{}, io.EOF
		}
		if xerr != nil {
			return x11PropertyNotify{}, xerr
		}
		if pn, ok := ev.(xproto.PropertyNotifyEvent); ok {
			return x11PropertyNotify{Window: uint32(pn.Window), Atom: uint32(pn.Atom)}, nil
		}
	}
}

func (c *x11XGBConn) Close() {
	c.x.Close()
}

// x11Session is one open connection with the atoms it needs interned.
type x11Session struct {
	conn         x11Conn
	root         uint32
	atomActive   uint32
	atomGtkAppID uint32
	atomUTF8     uint32
}

// openX11Session dials display and interns the atoms; the caller closes the
// session. The dial is bounded by ctx.
func openX11Session(ctx context.Context, dial func(context.Context, string) (x11Conn, error), display string) (*x11Session, error) {
	conn, err := dial(ctx, display)
	if err != nil {
		return nil, fmt.Errorf("connect to X11: %w", err)
	}
	sess, err := newX11Session(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return sess, nil
}

// newX11Session interns the atoms on conn. It does not close conn.
func newX11Session(conn x11Conn) (*x11Session, error) {
	sess := &x11Session{conn: conn, root: conn.Root()}
	for _, atom := range []struct {
		name string
		dst  *uint32
	}{
		{x11AtomNetActiveWindow, &sess.atomActive},
		{x11AtomGtkApplicationID, &sess.atomGtkAppID},
		{x11AtomUTF8String, &sess.atomUTF8},
	} {
		value, err := conn.InternAtom(atom.name)
		if err != nil {
			return nil, fmt.Errorf("intern atom %s: %w", atom.name, err)
		}
		*atom.dst = value
	}
	return sess, nil
}

func (sess *x11Session) close() {
	sess.conn.Close()
}

// read returns the active window and its identity. A window that is gone
// (BadWindow) is reported as no window. Any other read error is returned
// together with the state known so far (Connected, empty identity); a closed
// connection is an error wrapping io.EOF.
func (sess *x11Session) read() (X11State, error) {
	st := X11State{Connected: true}
	value, err := sess.conn.GetProperty(sess.root, sess.atomActive, uint32(xproto.AtomWindow), x11ActiveWindowLen)
	if err != nil {
		return st, fmt.Errorf("read %s: %w", x11AtomNetActiveWindow, err)
	}
	if len(value) < 4 {
		return st, nil
	}
	window := xgb.Get32(value)
	if window == 0 {
		return st, nil
	}
	wmClass, err := sess.conn.GetProperty(window, uint32(xproto.AtomWmClass), uint32(xproto.AtomString), x11PropertyMaxLen)
	if err != nil {
		return st, x11WindowReadError("WM_CLASS", window, err)
	}
	appID, err := sess.conn.GetProperty(window, sess.atomGtkAppID, sess.atomUTF8, x11PropertyMaxLen)
	if err != nil {
		return st, x11WindowReadError(x11AtomGtkApplicationID, window, err)
	}
	st.Window = window
	st.WmClass = ParseWMClass(wmClass)
	st.AppID = x11String(appID)
	return st, nil
}

// x11WindowReadError turns a BadWindow into "no window" (nil error) and
// wraps anything else.
func x11WindowReadError(property string, window uint32, err error) error {
	if errors.Is(err, errX11BadWindow) {
		return nil
	}
	return fmt.Errorf("read %s of window 0x%x: %w", property, window, err)
}
