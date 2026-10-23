package layoutstate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/arumata/gswitch/internal/gswitch/detect"
)

// SourceX11 is the State.Source value of X11Source.
const SourceX11 = "x11"

const (
	x11DefaultPollInterval = 50 * time.Millisecond
	x11MinPollInterval     = 20 * time.Millisecond
	x11MaxPollInterval     = 500 * time.Millisecond
	x11DefaultReplyTimeout = time.Second
	x11ConnectTimeout      = 2 * time.Second
	// x11TableRefreshPolls is how often _XKB_RULES_NAMES is re-read while
	// polling (every 40th poll ≈ 2 s at the default interval).
	x11TableRefreshPolls = 40
)

// X11Options configures NewX11Source.
type X11Options struct {
	Display      string        // "" ⇒ $DISPLAY (xgb.NewConnDisplay(""))
	PollInterval time.Duration // 50 ms default; clamped to 20 ms..500 ms
	Fallback     [2]Layout     // group i ⇒ Fallback[i] when _XKB_RULES_NAMES is unusable (the daemon's pair)
	ReplyTimeout time.Duration // 1 s default; bound on every reply
	Logf         func(string, ...any)
}

// X11Source tracks the XKB group of the core keyboard on an X11 display and
// reports it as a synthesized engine name "xkb:<layout>:<variant>:" so that the
// Tracker's pair mapping is unchanged. It implements Source.
//
// It polls XkbGetState every PollInterval on a dedicated xgb connection
// (SPEC-X11 X3: no XkbSelectEvents, because registering XKB events with xgb
// needs its process-global, unlocked event map). The group index is the order
// of _XKB_RULES_NAMES, which GNOME keeps equal to the order of
// org.gnome.desktop.input-sources sources. Engine is reported on connect and
// whenever the derived engine name changes; identical polls are silent, so a
// Pending state (NoteSwitchHint) is not flipped back to Confirmed by a poll.
// Any X error, EOF or reply timeout makes the source unavailable and
// reconnects with a 200 ms…5 s backoff.
type X11Source struct {
	opts         X11Options
	pollInterval time.Duration
	replyTimeout time.Duration

	// Test hooks; production values are set by NewX11Source.
	dial  func(ctx context.Context, display string) (x11Conn, error)
	sleep func(ctx context.Context, d time.Duration) bool

	mu   sync.Mutex
	live *x11Session // Run's connection while connected; requests on it are serialised by mu
}

var _ Source = (*X11Source)(nil)

// x11Session is one connection with its group table.
type x11Session struct {
	conn        x11Conn
	opcode      byte
	major       uint16
	minor       uint16
	table       []Layout
	rules       []byte // raw _XKB_RULES_NAMES value, nil when the property is missing
	fromRules   bool   // table comes from rules, not Fallback
	warnedTable bool   // the Fallback warning was logged on this connection
}

// x11Dial opens connections for new sources; tests replace it.
var x11Dial = dialX11

// NewX11Source creates a source for opts; nothing is connected until Run or
// Query.
func NewX11Source(opts X11Options) *X11Source {
	s := &X11Source{
		opts:         opts,
		pollInterval: opts.PollInterval,
		replyTimeout: opts.ReplyTimeout,
		dial:         x11Dial,
		sleep:        sleepCtx,
	}
	if s.pollInterval <= 0 {
		s.pollInterval = x11DefaultPollInterval
	}
	s.pollInterval = max(x11MinPollInterval, min(s.pollInterval, x11MaxPollInterval))
	if s.replyTimeout <= 0 {
		s.replyTimeout = x11DefaultReplyTimeout
	}
	return s
}

// X11SourceEnabled reports whether the X11 source should be used for the
// auto.conf layout-source value: "x11" when an X display is known (env.Display,
// or $DISPLAY when env is nil) and the session is not Wayland; "auto" only in
// an X11 GNOME session (the shouldVerifyGNOMEX11LayoutSwitch rule — SessionType
// x11 or Display set, never Wayland — and detect.IsGNOMESession, mirroring
// IBusSourceEnabled); never for root; "ibus"/"none" ⇒ false.
func X11SourceEnabled(layoutSource string, env *detect.SessionEnv, euid int) bool {
	if euid == 0 || x11WaylandSession(env) {
		return false
	}
	switch layoutSource {
	case "x11":
		return x11DisplayKnown(env)
	case "auto":
		return inX11Session(env) && detect.IsGNOMESession(env)
	default:
		return false
	}
}

// x11WaylandSession reports a positively Wayland session.
func x11WaylandSession(env *detect.SessionEnv) bool {
	if env != nil {
		return strings.EqualFold(env.SessionType, "wayland") || env.WaylandDisplay != ""
	}
	return strings.EqualFold(os.Getenv("XDG_SESSION_TYPE"), "wayland") || os.Getenv("WAYLAND_DISPLAY") != ""
}

// inX11Session is the X11 half of the shouldVerifyGNOMEX11LayoutSwitch rule:
// SessionType x11 or Display set from env, else XDG_SESSION_TYPE=x11 from the
// process environment (no DISPLAY fallback there, SPEC-X11 K-X3).
func inX11Session(env *detect.SessionEnv) bool {
	if env != nil {
		return strings.EqualFold(env.SessionType, "x11") || env.Display != ""
	}
	return strings.EqualFold(os.Getenv("XDG_SESSION_TYPE"), "x11")
}

// x11DisplayKnown reports whether an X display name is available.
func x11DisplayKnown(env *detect.SessionEnv) bool {
	if env != nil {
		return env.Display != ""
	}
	return os.Getenv("DISPLAY") != ""
}

// Name implements Source.
func (s *X11Source) Name() string { return SourceX11 }

// Run implements Source: connect, report the current group, poll it every
// PollInterval, and reconnect with a 200 ms…5 s backoff after any failure. It
// returns when ctx is done.
func (s *X11Source) Run(ctx context.Context, sink Sink) {
	backoff := ibusBackoffInitial
	lastErr := ""
	for ctx.Err() == nil {
		connected, err := s.session(ctx, sink)
		if ctx.Err() != nil {
			return
		}
		sink.Available(SourceX11, false)
		if connected {
			backoff = ibusBackoffInitial
			lastErr = ""
		}
		if msg := err.Error(); msg != lastErr {
			lastErr = msg
			s.log("X11 layout source: %v (retrying)", err)
		}
		if !s.sleep(ctx, backoff) {
			return
		}
		backoff = min(backoff*2, ibusBackoffMax)
	}
}

// session runs one connection until it fails. connected reports whether the
// initial group read succeeded.
func (s *X11Source) session(ctx context.Context, sink Sink) (connected bool, err error) {
	sess, err := s.connect(ctx, true)
	if err != nil {
		return false, err
	}
	defer sess.conn.Close()

	group, err := s.getState(ctx, sess)
	if err != nil {
		return false, s.describeErr(ctx, err)
	}
	engine, _ := EngineForGroup(sess.table, int(group))
	s.setLive(sess)
	defer s.setLive(nil)
	s.log("X11 layout source: connected to display %s (XKB %d.%d, opcode %d), layouts %s, group %d, engine %q",
		s.displayName(), sess.major, sess.minor, sess.opcode, formatLayouts(sess.table), group, engine)
	sink.Available(SourceX11, true)
	sink.Engine(SourceX11, engine)

	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()
	last := engine
	for polls := 1; ; polls++ {
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case <-ticker.C:
		}
		engine, err := s.poll(ctx, sess, polls%x11TableRefreshPolls == 0)
		if err != nil {
			return true, s.describeErr(ctx, err)
		}
		if engine != last {
			last = engine
			sink.Engine(SourceX11, engine)
		}
	}
}

// connect dials, initialises XKB and reads the group table. warn enables the
// table warnings (Run); Query's temporary connections stay quiet.
func (s *X11Source) connect(ctx context.Context, warn bool) (*x11Session, error) {
	dialCtx, cancel := context.WithTimeout(ctx, x11ConnectTimeout)
	conn, err := s.dial(dialCtx, s.opts.Display)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("connect to X display %s: %w", s.displayName(), err)
	}
	sess := &x11Session{conn: conn}
	if err := s.initSession(ctx, sess, warn); err != nil {
		conn.Close()
		return nil, s.describeErr(ctx, err)
	}
	return sess, nil
}

func (s *X11Source) initSession(ctx context.Context, sess *x11Session, warn bool) error {
	var err error
	rctx, cancel := context.WithTimeout(ctx, s.replyTimeout)
	sess.opcode, err = sess.conn.QueryExtension(rctx)
	cancel()
	if err != nil {
		return err
	}
	rctx, cancel = context.WithTimeout(ctx, s.replyTimeout)
	sess.major, sess.minor, err = sess.conn.UseExtension(rctx)
	cancel()
	if err != nil {
		return err
	}
	if err := s.readTable(ctx, sess, warn); err != nil {
		return err
	}
	if warn && len(sess.table) == 2 && reversedPair(sess.table, s.opts.Fallback) {
		s.log("Warning: X11 layout table %s differs from the configured pair %s",
			formatLayouts(sess.table), formatLayouts(s.opts.Fallback[:]))
	}
	return nil
}

// readTable reads _XKB_RULES_NAMES into sess.table. A missing or unusable
// property (fewer than two layouts) selects Fallback and logs once per
// connection when warn is set. An X error is returned.
func (s *X11Source) readTable(ctx context.Context, sess *x11Session, warn bool) error {
	rctx, cancel := context.WithTimeout(ctx, s.replyTimeout)
	data, found, err := sess.conn.RulesNames(rctx)
	cancel()
	if err != nil {
		return err
	}
	var table []Layout
	fromRules := false
	if !found {
		data = nil
	} else if parsed, ok := ParseXKBRulesNames(data); ok && len(parsed) >= 2 {
		table, fromRules = parsed, true
	}
	if !fromRules {
		table = []Layout{s.opts.Fallback[0], s.opts.Fallback[1]}
		if warn && !sess.warnedTable {
			sess.warnedTable = true
			reason := "is missing"
			if found {
				reason = fmt.Sprintf("has fewer than two layouts (%q)", xkbRulesNamesFields(data)[2])
			}
			s.log("Warning: X11 layout source: %s on display %s %s, using the configured pair %s",
				xkbRulesNamesProperty, s.displayName(), reason, formatLayouts(table))
		}
	}
	if warn && sess.table != nil && !slices.Equal(sess.table, table) {
		s.log("X11 layout source: layout table changed from %s to %s", formatLayouts(sess.table), formatLayouts(table))
	}
	sess.table, sess.rules, sess.fromRules = table, data, fromRules
	return nil
}

// getState reads the current group, bounded by ReplyTimeout.
func (s *X11Source) getState(ctx context.Context, sess *x11Session) (uint8, error) {
	rctx, cancel := context.WithTimeout(ctx, s.replyTimeout)
	defer cancel()
	return sess.conn.GetState(rctx)
}

// poll reads the group (and re-reads the table when refresh is set or the
// group is outside it) under the request mutex and returns the derived engine
// name ("" when the group is still outside the table).
func (s *X11Source) poll(ctx context.Context, sess *x11Session, refresh bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	group, err := s.getState(ctx, sess)
	if err != nil {
		return "", err
	}
	if refresh || int(group) >= len(sess.table) {
		if err := s.readTable(ctx, sess, true); err != nil {
			return "", err
		}
	}
	engine, _ := EngineForGroup(sess.table, int(group))
	return engine, nil
}

// Query implements Source: XkbGetState on Run's live connection (serialised
// with the poll by the request mutex) or on a temporary one, bounded by ctx and
// ReplyTimeout. The engine name is derived from that connection's table.
func (s *X11Source) Query(ctx context.Context) (string, error) {
	if engine, ok, err := s.queryLive(ctx); ok {
		return engine, err
	}
	sess, err := s.connect(ctx, false)
	if err != nil {
		return "", err
	}
	defer sess.conn.Close()
	group, err := s.getState(ctx, sess)
	if err != nil {
		return "", s.describeErr(ctx, err)
	}
	engine, _ := EngineForGroup(sess.table, int(group))
	return engine, nil
}

// queryLive queries the live connection; ok is false when Run is not connected.
func (s *X11Source) queryLive(ctx context.Context) (engine string, ok bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.live
	if sess == nil {
		return "", false, nil
	}
	group, err := s.getState(ctx, sess)
	if err != nil {
		return "", true, s.describeErr(ctx, err)
	}
	engine, _ = EngineForGroup(sess.table, int(group))
	return engine, true, nil
}

func (s *X11Source) setLive(sess *x11Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.live = sess
}

// describeErr labels a ReplyTimeout expiry (as opposed to the caller's ctx).
func (s *X11Source) describeErr(ctx context.Context, err error) error {
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return fmt.Errorf("%w (no reply within %v)", err, s.replyTimeout)
	}
	return err
}

func (s *X11Source) displayName() string {
	return x11DisplayName(s.opts.Display)
}

func (s *X11Source) log(format string, args ...any) {
	if s.opts.Logf != nil {
		s.opts.Logf(format, args...)
	}
}

// x11DisplayName resolves "" to $DISPLAY for messages.
func x11DisplayName(display string) string {
	if display != "" {
		return display
	}
	if env := os.Getenv("DISPLAY"); env != "" {
		return env
	}
	return "(DISPLAY unset)"
}

// reversedPair reports that table holds the names of pair in the opposite
// order (variants ignored).
func reversedPair(table []Layout, pair [2]Layout) bool {
	return len(table) == 2 && table[0].Name != table[1].Name &&
		table[0].Name == pair[1].Name && table[1].Name == pair[0].Name
}

// formatLayouts joins a layout table like "us,ru(phonetic)".
func formatLayouts(table []Layout) string {
	parts := make([]string, len(table))
	for i, l := range table {
		parts[i] = l.String()
	}
	return strings.Join(parts, ",")
}
