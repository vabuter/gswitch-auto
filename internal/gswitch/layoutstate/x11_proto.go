package layoutstate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// XKB wire protocol pieces used by X11Source (X Keyboard Extension Protocol
// Specification, /usr/include/X11/extensions/XKBproto.h). jezek/xgb has no XKB
// binding, so the two requests are built by hand; the builders are copies of
// runtime/x11_layout_switch.go (this package must not import runtime).
const (
	xkbExtensionName        = "XKEYBOARD"
	xkbUseExtensionMinorOp  = 0 // X_kbUseExtension
	xkbGetStateMinorOp      = 4 // X_kbGetState
	xkbUseCoreKeyboard      = 0x0100
	xkbProtocolMajor        = 1
	xkbProtocolMinor        = 0
	xkbRulesNamesProperty   = "_XKB_RULES_NAMES"
	xkbRulesNamesMaxLongs   = 1024 // GetProperty long_length: 4 KiB is far above any real value
	xkbRulesNamesFieldCount = 5    // rules, model, layouts, variants, options

	// xkbGetStateReply offsets: byte 12 is the effective group, byte 13 the
	// locked group (the effective one is used, as upstream does).
	xkbStateReplyGroupOffset = 12
	// xkbUseExtensionReply: byte 1 is `supported`, bytes 8-9 serverMajor,
	// bytes 10-11 serverMinor.
	xkbUseExtensionReplySupported = 1
	xkbUseExtensionReplyMajor     = 8
	xkbUseExtensionReplyMinor     = 10
	xkbUseExtensionReplyMinLen    = 12
)

// errXKBUnavailable is returned when the X server has no XKEYBOARD extension.
var errXKBUnavailable = errors.New("XKEYBOARD extension is unavailable")

// errXKBUnsupported is returned when XkbUseExtension refuses protocol 1.0.
var errXKBUnsupported = errors.New("XKEYBOARD 1.0 is unsupported by the X server")

// xkbUseExtensionRequest encodes XkbUseExtension(wantedMajor=1, wantedMinor=0):
// [opcode, 0, 2,0 (length), 1,0 (major), 0,0 (minor)].
func xkbUseExtensionRequest(opcode byte) []byte {
	request := make([]byte, 8)
	request[0] = opcode
	request[1] = xkbUseExtensionMinorOp
	xgb.Put16(request[2:], 2)
	xgb.Put16(request[4:], xkbProtocolMajor)
	xgb.Put16(request[6:], xkbProtocolMinor)
	return request
}

// xkbGetStateRequest encodes XkbGetState(deviceSpec=UseCoreKbd):
// [opcode, 4, 2,0 (length), 0x00,0x01 (0x0100 little-endian), 0,0 (pad)].
func xkbGetStateRequest(opcode byte) []byte {
	request := make([]byte, 8)
	request[0] = opcode
	request[1] = xkbGetStateMinorOp
	xgb.Put16(request[2:], 2)
	xgb.Put16(request[4:], xkbUseCoreKeyboard)
	return request
}

// xkbGroupFromStateReply extracts the effective group from an XkbGetState
// reply.
func xkbGroupFromStateReply(reply []byte) (uint8, error) {
	if len(reply) <= xkbStateReplyGroupOffset {
		return 0, fmt.Errorf("get XKB keyboard state: short reply (%d bytes)", len(reply))
	}
	return reply[xkbStateReplyGroupOffset], nil
}

// xkbVersionFromUseExtensionReply checks the `supported` flag of an
// XkbUseExtension reply and returns the server's protocol version.
func xkbVersionFromUseExtensionReply(reply []byte) (major, minor uint16, err error) {
	if len(reply) < xkbUseExtensionReplyMinLen {
		return 0, 0, fmt.Errorf("initialize %s extension: short reply (%d bytes)", xkbExtensionName, len(reply))
	}
	if reply[xkbUseExtensionReplySupported] != 1 {
		return 0, 0, errXKBUnsupported
	}
	return xgb.Get16(reply[xkbUseExtensionReplyMajor:]), xgb.Get16(reply[xkbUseExtensionReplyMinor:]), nil
}

// EngineForGroup returns the synthesized IBus-style engine name
// "xkb:<name>:<variant>:" of a 0-based XKB group, or ok=false when the group is
// outside the table (the tracker then sees Engine("") and reports Unknown
// until the next poll re-reads the table). ParseXKBEngine accepts the
// trailing-colon form (empty language part).
func EngineForGroup(table []Layout, group int) (engine string, ok bool) {
	if group < 0 || group >= len(table) {
		return "", false
	}
	return "xkb:" + table[group].Name + ":" + table[group].Variant + ":", true
}

// xkbRulesNamesFields splits a _XKB_RULES_NAMES value into its five
// NUL-terminated strings (rules, model, layouts, variants, options). A trailing
// NUL may be absent; missing fields are empty.
func xkbRulesNamesFields(data []byte) []string {
	fields := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	for len(fields) < xkbRulesNamesFieldCount {
		fields = append(fields, "")
	}
	return fields[:xkbRulesNamesFieldCount]
}

// ParseXKBRulesNames turns the root window's _XKB_RULES_NAMES value into the
// XKB group table: layouts "us,ru" with variants "," give [{us ""},{ru ""}],
// "us,ru" with "dvorak," give [{us dvorak},{ru ""}]. A missing variants field
// is all-empty; an empty layouts field yields ok=false.
func ParseXKBRulesNames(data []byte) (table []Layout, ok bool) {
	fields := xkbRulesNamesFields(data)
	if fields[2] == "" {
		return nil, false
	}
	names := strings.Split(fields[2], ",")
	var variants []string
	if fields[3] != "" {
		variants = strings.Split(fields[3], ",")
	}
	table = make([]Layout, len(names))
	for i, name := range names {
		table[i].Name = name
		if i < len(variants) {
			table[i].Variant = variants[i]
		}
	}
	return table, true
}

// x11Conn is the connection seam of X11Source: one X connection with the
// requests the source needs. Every method returns once ctx is done, whatever
// the X server does; Close is bounded as well. Tests substitute a fake.
type x11Conn interface {
	// QueryExtension finds the XKEYBOARD extension and remembers its major
	// opcode, which it returns.
	QueryExtension(ctx context.Context) (opcode byte, err error)
	// UseExtension sends XkbUseExtension(1.0) and returns the server version.
	UseExtension(ctx context.Context) (major, minor uint16, err error)
	// GetState returns the effective XKB group of the core keyboard.
	GetState(ctx context.Context) (group uint8, err error)
	// RulesNames reads _XKB_RULES_NAMES from the root window; found is false
	// when the property (or its atom) does not exist.
	RulesNames(ctx context.Context) (data []byte, found bool, err error)
	Close()
}

// xgbConn is the production x11Conn on a dedicated jezek/xgb connection.
//
// Every reply is awaited on its own goroutine and abandoned once ctx is done:
// xgb drops X errors it has no constructor for (an XKB error such as
// BadKeyboard has none) with a log line, so a cookie could otherwise wait
// forever. Close is bounded too, because xgb's graceful Close does a round
// trip (GetInputFocus) that a hung server never answers; in that case the
// socket and xgb's two goroutines linger until the server closes it.
type xgbConn struct {
	conn      *xgb.Conn
	root      xproto.Window
	opcode    byte
	rulesAtom xproto.Atom // interned once per connection, 0 until found
}

var _ x11Conn = (*xgbConn)(nil)

// x11CloseTimeout bounds xgbConn.Close.
const x11CloseTimeout = time.Second

// dialX11 opens an xgb connection to display ("" ⇒ $DISPLAY). The handshake
// is bounded by ctx; a connection that completes after ctx is done is closed.
func dialX11(ctx context.Context, display string) (x11Conn, error) {
	type result struct {
		conn *xgb.Conn
		err  error
	}
	done := make(chan result, 1)
	go func() {
		conn, err := xgb.NewConnDisplay(display)
		done <- result{conn, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			return nil, r.err
		}
		return &xgbConn{conn: r.conn, root: xproto.Setup(r.conn).DefaultScreen(r.conn).Root}, nil
	case <-ctx.Done():
		go func() {
			if r := <-done; r.err == nil {
				r.conn.Close()
			}
		}()
		return nil, ctx.Err()
	}
}

// awaitReply runs call on its own goroutine and returns its result, or
// ctx.Err() once ctx is done. A late result is dropped; closing the
// connection makes xgb return io.EOF to the abandoned goroutine.
func awaitReply[T any](ctx context.Context, call func() (T, error)) (T, error) {
	type result struct {
		value T
		err   error
	}
	done := make(chan result, 1)
	go func() {
		value, err := call()
		done <- result{value, err}
	}()
	select {
	case r := <-done:
		return r.value, r.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

// requestReply sends a hand-built request expecting a reply, exactly like
// upstream's x11LayoutGroupReader.requestReply, bounded by ctx.
func (c *xgbConn) requestReply(ctx context.Context, request []byte) ([]byte, error) {
	return awaitReply(ctx, func() ([]byte, error) {
		cookie := c.conn.NewCookie(true, true)
		c.conn.NewRequest(request, cookie)
		return cookie.Reply()
	})
}

func (c *xgbConn) QueryExtension(ctx context.Context) (byte, error) {
	reply, err := awaitReply(ctx, func() (*xproto.QueryExtensionReply, error) {
		return xproto.QueryExtension(c.conn, uint16(len(xkbExtensionName)), xkbExtensionName).Reply()
	})
	if err != nil {
		return 0, fmt.Errorf("query %s extension: %w", xkbExtensionName, err)
	}
	if reply == nil || !reply.Present {
		return 0, errXKBUnavailable
	}
	c.opcode = reply.MajorOpcode
	return c.opcode, nil
}

func (c *xgbConn) UseExtension(ctx context.Context) (uint16, uint16, error) {
	reply, err := c.requestReply(ctx, xkbUseExtensionRequest(c.opcode))
	if err != nil {
		return 0, 0, fmt.Errorf("initialize %s extension: %w", xkbExtensionName, err)
	}
	return xkbVersionFromUseExtensionReply(reply)
}

func (c *xgbConn) GetState(ctx context.Context) (uint8, error) {
	reply, err := c.requestReply(ctx, xkbGetStateRequest(c.opcode))
	if err != nil {
		return 0, fmt.Errorf("get XKB keyboard state: %w", err)
	}
	return xkbGroupFromStateReply(reply)
}

func (c *xgbConn) RulesNames(ctx context.Context) ([]byte, bool, error) {
	if c.rulesAtom == xproto.AtomNone {
		reply, err := awaitReply(ctx, func() (*xproto.InternAtomReply, error) {
			return xproto.InternAtom(c.conn, true, uint16(len(xkbRulesNamesProperty)), xkbRulesNamesProperty).Reply()
		})
		if err != nil {
			return nil, false, fmt.Errorf("intern %s: %w", xkbRulesNamesProperty, err)
		}
		if reply == nil || reply.Atom == xproto.AtomNone {
			return nil, false, nil // the atom does not exist, so no window has the property
		}
		c.rulesAtom = reply.Atom
	}
	reply, err := awaitReply(ctx, func() (*xproto.GetPropertyReply, error) {
		return xproto.GetProperty(c.conn, false, c.root, c.rulesAtom, xproto.AtomString, 0, xkbRulesNamesMaxLongs).Reply()
	})
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", xkbRulesNamesProperty, err)
	}
	if reply == nil || reply.Type == xproto.AtomNone || reply.Format != 8 || reply.ValueLen == 0 {
		return nil, false, nil
	}
	n := min(int(reply.ValueLen), len(reply.Value))
	return reply.Value[:n], true, nil
}

// Close closes the connection and waits at most x11CloseTimeout for xgb's
// graceful shutdown round trip.
func (c *xgbConn) Close() {
	done := make(chan struct{})
	go func() {
		c.conn.Close()
		close(done)
	}()
	timer := time.NewTimer(x11CloseTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}
