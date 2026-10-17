package ipc

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	reconnectMinDelay = time.Second
	reconnectMaxDelay = 30 * time.Second
	clientStopTimeout = 3 * time.Second
)

// ClientStatus is the daemon status as seen by the tray. Status is the zero
// value while Connected is false.
type ClientStatus struct {
	Connected bool
	Status
}

// ClientCallbacks are invoked from the client's goroutine, never concurrently
// and never after Stop returns. They must not block and must not call Stop.
type ClientCallbacks struct {
	OnStatus  func(ClientStatus)
	OnToggle  func(enabled bool, source string)
	OnCorrect func(Correction)
	OnUndone  func(word string, learned bool)
}

// Client follows the daemon over its own private session-bus connection. It
// subscribes to NameOwnerChanged, PropertiesChanged and the interface signals,
// reads all properties with GetAll whenever the daemon (re)appears, and opens a
// new private connection if its own connection is lost.
type Client struct {
	cb      ClientCallbacks
	connect func() (*dbus.Conn, error)

	life sync.Mutex // serialises Start and Stop

	mu      sync.Mutex
	conn    *dbus.Conn
	owner   string // unique name of the daemon, "" while it is absent
	status  ClientStatus
	running bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// NewClient returns a stopped client.
func NewClient(cb ClientCallbacks) *Client {
	return &Client{
		cb: cb,
		connect: func() (*dbus.Conn, error) {
			return dbus.ConnectSessionBus()
		},
	}
}

// Start opens the private connection, adds the signal matches and reads the
// current daemon state. A missing daemon is not an error: the client reports
// Connected=false and connects when the daemon appears.
func (c *Client) Start() error {
	c.life.Lock()
	defer c.life.Unlock()
	c.mu.Lock()
	running := c.running
	c.mu.Unlock()
	if running {
		return errors.New("ipc: client already started")
	}

	conn, sigs, err := c.dial()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	c.mu.Lock()
	c.conn, c.running, c.cancel, c.done = conn, true, cancel, done
	c.mu.Unlock()
	c.resync(conn)
	go c.run(ctx, conn, sigs, done)
	return nil
}

// Stop removes the client's matches and closes only its own connection.
func (c *Client) Stop() {
	c.life.Lock()
	defer c.life.Unlock()
	c.mu.Lock()
	if !c.running {
		c.mu.Unlock()
		return
	}
	cancel, done := c.cancel, c.done
	c.running = false
	c.mu.Unlock()

	cancel()
	select {
	case <-done:
	case <-time.After(clientStopTimeout):
	}
	c.mu.Lock()
	c.conn, c.owner, c.status = nil, "", ClientStatus{}
	c.mu.Unlock()
}

// Status returns a copy of the last known daemon status.
func (c *Client) Status() ClientStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return ClientStatus{Connected: c.status.Connected, Status: cloneStatus(c.status.Status)}
}

// SetAutoEnabled calls the daemon's SetAutoEnabled method.
func (c *Client) SetAutoEnabled(ctx context.Context, enabled bool) error {
	return c.call(ctx, methodSetAutoEnabled, enabled)
}

// Reload asks the daemon to reload the user configuration. A configuration
// error is reported as a dbus.Error named ErrConfig.
func (c *Client) Reload(ctx context.Context) error {
	return c.call(ctx, methodReload)
}

// SuspendHotkeys suspends or resumes the daemon's gestures and chords. The
// suspension belongs to this client's connection and ends when it closes.
func (c *Client) SuspendHotkeys(ctx context.Context, suspend bool) error {
	return c.call(ctx, methodSuspendHotkeys, suspend)
}

func (c *Client) call(ctx context.Context, method string, args ...any) error {
	c.mu.Lock()
	conn, owner := c.conn, c.owner
	c.mu.Unlock()
	if conn == nil || owner == "" {
		return ErrNotConnected
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	return conn.Object(BusName, ObjectPath).CallWithContext(ctx, Interface+"."+method, dbus.FlagNoAutoStart, args...).Err
}

func clientMatches() [][]dbus.MatchOption {
	return [][]dbus.MatchOption{
		nameOwnerChangedMatch(BusName),
		{
			dbus.WithMatchSender(BusName),
			dbus.WithMatchObjectPath(ObjectPath),
			dbus.WithMatchInterface(propertiesIfc),
			dbus.WithMatchMember("PropertiesChanged"),
			dbus.WithMatchArg(0, Interface),
		},
		{
			dbus.WithMatchSender(BusName),
			dbus.WithMatchObjectPath(ObjectPath),
			dbus.WithMatchInterface(Interface),
		},
	}
}

func (c *Client) dial() (*dbus.Conn, chan *dbus.Signal, error) {
	conn, err := c.connect()
	if err != nil {
		return nil, nil, fmt.Errorf("connect to session bus: %w", err)
	}
	sigs := make(chan *dbus.Signal, 32)
	conn.Signal(sigs)
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	for _, m := range clientMatches() {
		if err := conn.AddMatchSignalContext(ctx, m...); err != nil {
			_ = conn.Close()
			return nil, nil, fmt.Errorf("subscribe to %s signals: %w", BusName, err)
		}
	}
	return conn, sigs, nil
}

func (c *Client) teardown(conn *dbus.Conn, sigs chan *dbus.Signal) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	for _, m := range clientMatches() {
		_ = conn.RemoveMatchSignalContext(ctx, m...)
	}
	conn.RemoveSignal(sigs)
	_ = conn.Close()
}

func (c *Client) run(ctx context.Context, conn *dbus.Conn, sigs chan *dbus.Signal, done chan struct{}) {
	defer close(done)
	c.notifyStatus()
	for {
		select {
		case <-ctx.Done():
			c.teardown(conn, sigs)
			return
		case sig, ok := <-sigs:
			if ok {
				c.handleSignal(conn, sig)
				continue
			}
			// Our own connection was lost: report and open a new one.
			c.mu.Lock()
			c.conn, c.owner, c.status = nil, "", ClientStatus{}
			c.mu.Unlock()
			c.notifyStatus()
			conn, sigs = c.redial(ctx)
			if conn == nil {
				return
			}
			c.mu.Lock()
			c.conn = conn
			c.mu.Unlock()
			c.resync(conn)
			c.notifyStatus()
		}
	}
}

func (c *Client) redial(ctx context.Context) (*dbus.Conn, chan *dbus.Signal) {
	delay := reconnectMinDelay
	for {
		select {
		case <-ctx.Done():
			return nil, nil
		case <-time.After(delay):
		}
		conn, sigs, err := c.dial()
		if err == nil {
			if ctx.Err() != nil {
				c.teardown(conn, sigs)
				return nil, nil
			}
			return conn, sigs
		}
		delay = min(2*delay, reconnectMaxDelay)
	}
}

// resync looks up the daemon's unique name and reads all its properties.
func (c *Client) resync(conn *dbus.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	var owner string
	if err := conn.BusObject().CallWithContext(ctx, dbusName+".GetNameOwner", 0, BusName).Store(&owner); err != nil {
		owner = ""
	}
	c.refresh(conn, owner)
}

// refresh records owner and replaces the status with a GetAll from it.
func (c *Client) refresh(conn *dbus.Conn, owner string) {
	st := ClientStatus{}
	if owner != "" {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		var all map[string]dbus.Variant
		err := conn.Object(owner, ObjectPath).CallWithContext(ctx, propertiesIfc+".GetAll",
			dbus.FlagNoAutoStart, Interface).Store(&all)
		cancel()
		if err == nil {
			st = ClientStatus{Connected: true, Status: statusFromProps(all)}
		}
	}
	c.mu.Lock()
	c.owner, c.status = owner, st
	c.mu.Unlock()
}

func (c *Client) handleSignal(conn *dbus.Conn, sig *dbus.Signal) {
	if name, vanished := parseNameOwnerChanged(sig); name != "" {
		if name != BusName {
			return
		}
		newOwner := ""
		if !vanished {
			newOwner, _ = sig.Body[2].(string)
		}
		c.refresh(conn, newOwner)
		c.notifyStatus()
		return
	}

	c.mu.Lock()
	fromDaemon := c.owner != "" && sig.Sender == c.owner && sig.Path == ObjectPath
	c.mu.Unlock()
	if !fromDaemon {
		return
	}
	switch sig.Name {
	case propertiesIfc + ".PropertiesChanged":
		c.handlePropertiesChanged(conn, sig.Body)
	case Interface + "." + sigAutoEnabledChanged:
		var enabled bool
		var source string
		if dbus.Store(sig.Body, &enabled, &source) == nil && c.cb.OnToggle != nil {
			c.cb.OnToggle(enabled, source)
		}
	case Interface + "." + sigCorrected:
		var tup correctionTuple
		if dbus.Store(sig.Body, &tup.From, &tup.To, &tup.Direction, &tup.Time) == nil && c.cb.OnCorrect != nil {
			c.cb.OnCorrect(tup.correction())
		}
	case Interface + "." + sigCorrectionUndone:
		var word string
		var learned bool
		if dbus.Store(sig.Body, &word, &learned) == nil && c.cb.OnUndone != nil {
			c.cb.OnUndone(word, learned)
		}
	}
}

func (c *Client) handlePropertiesChanged(conn *dbus.Conn, body []any) {
	var iface string
	var changed map[string]dbus.Variant
	var invalidated []string
	if dbus.Store(body, &iface, &changed, &invalidated) != nil || iface != Interface {
		return
	}
	c.mu.Lock()
	full := !c.status.Connected || len(invalidated) > 0
	if !full {
		for name, v := range changed {
			if applyProperty(&c.status.Status, name, v) != nil {
				full = true
			}
		}
	}
	owner := c.owner
	c.mu.Unlock()
	if full {
		c.refresh(conn, owner)
	}
	c.notifyStatus()
}

func (c *Client) notifyStatus() {
	if c.cb.OnStatus != nil {
		c.cb.OnStatus(c.Status())
	}
}
