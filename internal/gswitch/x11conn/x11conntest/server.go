// Package x11conntest provides fake X servers on unix sockets for the tests of
// the X11 sources (gswitch-auto). It is imported by tests only.
package x11conntest

import (
	"encoding/binary"
	"io"
	"net"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// RootWindow is the root window of the fake server's only screen.
const RootWindow = 0x1a2

// Server is a fake X server listening on "<tmp>/x:0".
type Server struct {
	// Display is the display name to dial: "<tmp>/x:0" plus the suffix given
	// to New (xgb's "/path/sock:N" form dials the socket "/path/sock:N").
	Display string

	ln    net.Listener
	serve func(net.Conn)

	mu     sync.Mutex
	conns  int
	closed int
}

// New starts a server that runs serve for each accepted connection and then
// waits until the client closes it. It sets XAUTHORITY to a missing file for
// the test, so clients connect without authorisation.
func New(t testing.TB, suffix string, serve func(net.Conn)) *Server {
	t.Helper()
	dir := t.TempDir()
	ln, err := net.Listen("unix", filepath.Join(dir, "x:0"))
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Display: filepath.Join(dir, "x") + ":0" + suffix, ln: ln, serve: serve}
	t.Setenv("XAUTHORITY", filepath.Join(dir, "no-xauthority"))
	go s.accept()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *Server) accept() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns++
		s.mu.Unlock()
		go func() {
			defer c.Close()
			s.serve(c)
			_, _ = io.Copy(io.Discard, c)
			s.mu.Lock()
			s.closed++
			s.mu.Unlock()
		}()
	}
}

// Counts returns how many connections were accepted and how many of them the
// client has closed.
func (s *Server) Counts() (conns, closed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns, s.closed
}

// Silent never answers the connection setup.
func Silent(net.Conn) {}

// OneScreen answers the connection setup as a server with one screen whose
// root window is RootWindow, and then answers nothing.
func OneScreen(c net.Conn) {
	head := make([]byte, 12)
	if _, err := io.ReadFull(c, head); err != nil {
		return
	}
	nameLen := int(binary.LittleEndian.Uint16(head[6:]))
	dataLen := int(binary.LittleEndian.Uint16(head[8:]))
	if _, err := io.ReadFull(c, make([]byte, xgb.Pad(nameLen)+xgb.Pad(dataLen))); err != nil {
		return
	}
	setup := xproto.SetupInfo{
		Status:               1,
		ProtocolMajorVersion: 11,
		ResourceIdBase:       0x200000,
		ResourceIdMask:       0x1fffff,
		MaximumRequestLength: 0xffff,
		MinKeycode:           8,
		MaxKeycode:           255,
		RootsLen:             1,
		Roots:                []xproto.ScreenInfo{{Root: RootWindow, WidthInPixels: 640, HeightInPixels: 480}},
	}
	buf := setup.Bytes()
	binary.LittleEndian.PutUint16(buf[6:], uint16((len(buf)-8)/4))
	_, _ = c.Write(buf)
}
