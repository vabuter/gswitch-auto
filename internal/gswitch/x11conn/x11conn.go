// Package x11conn opens jezek/xgb connections whose socket connect and X
// connection setup (handshake) are bounded by a context, and closes them in
// bounded time even when the server stopped answering (gswitch-auto,
// SPEC-X11 K-X16). It is shared by the X11 layout source
// (internal/gswitch/layoutstate) and the X11 focus source
// (internal/gswitch/focus).
//
// xgb.NewConnDisplay has no deadline: against a server that accepts the
// connection but never completes the setup (a stopped or hung Xorg/Xwayland,
// a stale socket, a stalled TCP or forwarded display) it blocks in
// io.ReadFull forever, and a caller that gives up on it leaks the goroutine
// and the socket. Dial instead parses the display name exactly like xgb,
// dials the socket itself with net.Dialer.DialContext, looks the
// MIT-MAGIC-COOKIE-1 up in the Xauthority file exactly like xgb, and hands
// the socket to xgb.NewConnNetWithCookieHex (xgb.NewConnNet when there is no
// entry: xgb then connects without authorisation, as NewConnDisplay does).
// When ctx is done during the setup, the socket's deadline is moved into the
// past, so the setup read fails at once; nothing is left running.
package x11conn

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// CloseTimeout bounds Conn.Close's wait for xgb's graceful shutdown round
// trip; the socket is closed after it in any case.
const CloseTimeout = 500 * time.Millisecond

// Conn is an xgb connection together with its socket.
type Conn struct {
	x    *xgb.Conn
	raw  net.Conn
	once sync.Once
}

// X returns the xgb connection for requests.
func (c *Conn) X() *xgb.Conn { return c.x }

// Close closes the connection. xgb's Close only queues a graceful shutdown: a
// GetInputFocus round trip after which xgb closes the socket and its reader
// and writer goroutines exit. Close waits at most CloseTimeout for that (the
// reader's end is seen as the closed event queue) and then closes the socket
// itself, so a server that never answers the round trip cannot keep the
// socket, the goroutines or any pending reply alive (pending replies return
// io.EOF). It is idempotent and safe for concurrent use; events still queued
// are discarded.
func (c *Conn) Close() {
	c.once.Do(func() {
		readerDone := make(chan struct{})
		go func() {
			defer close(readerDone)
			for {
				if ev, err := c.x.WaitForEvent(); ev == nil && err == nil {
					return // the event queue is closed: the reader has exited
				}
			}
		}()
		go c.x.Close() // returns once the writer took the request or exited
		timer := time.NewTimer(CloseTimeout)
		defer timer.Stop()
		select {
		case <-readerDone:
		case <-timer.C:
		}
		_ = c.raw.Close()
	})
}

// DefaultRoot returns the root window of the display's default screen (the
// screen number of the display name, ":0.1" ⇒ 1). A screen number the server
// does not have is an error: xproto.SetupInfo.DefaultScreen would index its
// Roots slice out of range and panic.
func (c *Conn) DefaultRoot() (xproto.Window, error) {
	return ScreenRoot(xproto.Setup(c.x), c.x.DefaultScreen)
}

// ScreenRoot returns the root window of screen in setup, or an error when the
// server has no such screen.
func ScreenRoot(setup *xproto.SetupInfo, screen int) (xproto.Window, error) {
	if setup == nil || screen < 0 || screen >= len(setup.Roots) {
		n := 0
		if setup != nil {
			n = len(setup.Roots)
		}
		return 0, fmt.Errorf("default screen %d is not one of the display's %d screen(s)", screen, n)
	}
	return setup.Roots[screen].Root, nil
}

// pastDeadline is the deadline that aborts a connection setup once ctx is
// done.
var pastDeadline = time.Unix(1, 0)

// Dial opens a connection to display ("" ⇒ $DISPLAY). The socket connect
// and the connection setup are bounded by ctx; on expiry the socket is closed
// and ctx.Err() returned.
func Dial(ctx context.Context, display string) (*Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target, err := ParseDisplay(display)
	if err != nil {
		return nil, err
	}
	var dialer net.Dialer
	raw, err := dialer.DialContext(ctx, target.Network, target.Address)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("cannot connect to %s: %w", target.Name, err)
	}

	authName, authData, authErr := readAuthority(target.Host, target.Display)
	stop := context.AfterFunc(ctx, func() { _ = raw.SetDeadline(pastDeadline) })
	var x *xgb.Conn
	switch {
	case authErr != nil:
		// No usable Xauthority entry: xgb logs it and connects without
		// authorisation, exactly what NewConnDisplay does.
		x, err = xgb.NewConnNet(raw)
	case authName != "MIT-MAGIC-COOKIE-1" || len(authData) != 16:
		err = errors.New("unsupported auth protocol " + authName)
	default:
		x, err = xgb.NewConnNetWithCookieHex(raw, hex.EncodeToString(authData))
	}
	if !stop() {
		// ctx ended during the setup (the deadline may already have failed
		// it); xgb's goroutines of a completed setup exit with the socket.
		_ = raw.Close()
		return nil, ctx.Err()
	}
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	x.DisplayNumber = target.DisplayNumber
	x.DefaultScreen = target.Screen
	return &Conn{x: x, raw: raw}, nil
}

// Target is a parsed X display name.
type Target struct {
	Name          string // the display name as given (after the $DISPLAY default)
	Network       string // "unix", "tcp", ...
	Address       string // socket path or host:port
	Host          string // host for the Xauthority lookup ("" for local sockets)
	Display       string // display number as written, for the Xauthority lookup
	DisplayNumber int
	Screen        int
}

// ParseDisplay parses display ("" ⇒ $DISPLAY) like xgb's NewConnDisplay:
// "/path/sock:0" (unix socket "/path/sock:0"), "host:2.1" and
// "proto/host:1" (TCP port 6000+n), ":0" and "unix:0" (unix socket
// /tmp/.X11-unix/X0). The screen number is not range-checked here.
func ParseDisplay(display string) (Target, error) {
	if display == "" {
		display = os.Getenv("DISPLAY")
	}
	t := Target{Name: display}
	if display == "" {
		return t, errors.New("empty display string")
	}
	colon := strings.LastIndex(display, ":")
	if colon < 0 {
		return t, errors.New("bad display string: " + display)
	}
	var protocol, socket string
	if display[0] == '/' {
		socket = display[:colon]
	} else if slash := strings.LastIndex(display, "/"); slash >= 0 && slash < colon {
		protocol = display[:slash]
		t.Host = display[slash+1 : colon]
	} else {
		t.Host = display[:colon]
	}
	rest := display[colon+1:]
	if rest == "" {
		return t, errors.New("bad display string: " + display)
	}
	screen := ""
	if dot := strings.LastIndex(rest, "."); dot >= 0 {
		t.Display, screen = rest[:dot], rest[dot+1:]
	} else {
		t.Display = rest
	}
	n, err := strconv.Atoi(t.Display)
	if err != nil || n < 0 {
		return t, errors.New("bad display string: " + display)
	}
	t.DisplayNumber = n
	if screen != "" {
		if t.Screen, err = strconv.Atoi(screen); err != nil {
			return t, errors.New("bad display string: " + display)
		}
	}
	switch {
	case socket != "":
		t.Network, t.Address = "unix", socket+":"+t.Display
	case t.Host != "" && t.Host != "unix":
		if protocol == "" {
			protocol = "tcp"
		}
		t.Network, t.Address = protocol, net.JoinHostPort(t.Host, strconv.Itoa(6000+n))
	default:
		t.Host = ""
		t.Network, t.Address = "unix", "/tmp/.X11-unix/X"+t.Display
	}
	return t, nil
}

// readAuthority returns the first Xauthority entry for host ("" or
// "localhost" ⇒ os.Hostname()) and display, exactly like xgb's
// (unexported) readAuthority: $XAUTHORITY, else $HOME/.Xauthority; an entry
// matches with family FamilyWild, or FamilyLocal and the host name, and an
// empty or equal display number.
func readAuthority(host, display string) (name string, data []byte, err error) {
	const (
		familyLocal = 256
		familyWild  = 65535
	)
	if host == "" || host == "localhost" {
		if host, err = os.Hostname(); err != nil {
			return "", nil, err
		}
	}
	path := os.Getenv("XAUTHORITY")
	if path == "" {
		home := os.Getenv("HOME")
		if home == "" {
			return "", nil, errors.New("Xauthority not found: $XAUTHORITY, $HOME not set")
		}
		path = home + "/.Xauthority"
	}
	f, err := os.Open(path)
	if err != nil {
		return "", nil, err
	}
	defer f.Close()
	return findAuthority(f, host, display, familyLocal, familyWild)
}

func findAuthority(r io.Reader, host, display string, familyLocal, familyWild uint16) (string, []byte, error) {
	for {
		var family uint16
		if err := binary.Read(r, binary.BigEndian, &family); err != nil {
			return "", nil, err
		}
		var fields [4][]byte
		for i := range fields {
			var n uint16
			if err := binary.Read(r, binary.BigEndian, &n); err != nil {
				return "", nil, err
			}
			if n > 256 {
				return "", nil, errors.New("bytes too long for buffer")
			}
			fields[i] = make([]byte, n)
			if _, err := io.ReadFull(r, fields[i]); err != nil {
				return "", nil, err
			}
		}
		addr, disp := string(fields[0]), string(fields[1])
		addrMatch := family == familyWild || (family == familyLocal && addr == host)
		dispMatch := disp == "" || disp == display
		if addrMatch && dispMatch {
			return string(fields[2]), fields[3], nil
		}
	}
}

// Await runs call on its own goroutine and returns its result, or ctx.Err()
// once ctx is done. A late result is dropped; closing the connection makes
// xgb return io.EOF to the abandoned goroutine, so it ends with the
// connection. xgb drops X errors it has no constructor for (an XKB error
// such as BadKeyboard) with a log line, so a reply can otherwise be awaited
// forever.
func Await[T any](ctx context.Context, call func() (T, error)) (T, error) {
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
