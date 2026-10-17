package ipc

import (
	"context"

	"github.com/godbus/dbus/v5"
)

// suspension is an active SuspendHotkeys(true) request.
type suspension struct {
	owner string // unique bus name of the caller
	gen   uint64
	stop  func() bool // stops the watchdog timer
}

// suspendHotkeys implements the SuspendHotkeys method. A suspension is owned by
// the caller's unique name and ends on SuspendHotkeys(false) from that caller,
// when the caller leaves the bus, or after suspendTimeout. A suspension request
// from another caller takes the suspension over.
func (s *Service) suspendHotkeys(owner string, suspend bool) error {
	if !suspend {
		s.suspendMu.Lock()
		cur := s.susp
		s.suspendMu.Unlock()
		if cur.owner == "" || cur.owner != owner {
			return nil
		}
		return s.releaseSuspension(cur.gen, "released by owner")
	}

	if err := s.ctl.SuspendHotkeys(true, owner); err != nil {
		return err
	}
	s.suspendMu.Lock()
	prev := s.susp
	s.suspGen++
	gen := s.suspGen
	s.susp = suspension{
		owner: owner,
		gen:   gen,
		stop:  s.afterFunc(suspendTimeout, func() { _ = s.releaseSuspension(gen, "timeout") }),
	}
	s.suspendMu.Unlock()

	if prev.gen != 0 {
		prev.stop()
		s.unwatch(prev.owner)
	}
	if owner == "" || s.names == nil {
		return nil
	}
	present, err := s.names.Watch(owner)
	switch {
	case err != nil:
		s.logf("IPC: cannot watch hotkey suspension owner %s: %v", owner, err)
	case !present:
		_ = s.releaseSuspension(gen, "owner vanished")
	}
	return nil
}

// releaseSuspension ends suspension gen if it is still the active one.
func (s *Service) releaseSuspension(gen uint64, reason string) error {
	s.suspendMu.Lock()
	cur := s.susp
	if cur.gen == 0 || cur.gen != gen {
		s.suspendMu.Unlock()
		return nil
	}
	s.susp = suspension{}
	s.suspendMu.Unlock()

	cur.stop()
	s.unwatch(cur.owner)
	s.logf("IPC: hotkey suspension by %s ended (%s)", cur.owner, reason)
	return s.ctl.SuspendHotkeys(false, cur.owner)
}

// nameVanished ends the suspension owned by name, if any.
func (s *Service) nameVanished(name string) {
	s.suspendMu.Lock()
	cur := s.susp
	s.suspendMu.Unlock()
	if cur.gen != 0 && cur.owner == name {
		_ = s.releaseSuspension(cur.gen, "owner vanished")
	}
}

func (s *Service) endSuspensionOnClose() {
	s.suspendMu.Lock()
	gen := s.susp.gen
	s.suspendMu.Unlock()
	if gen != 0 {
		_ = s.releaseSuspension(gen, "service closed")
	}
}

func (s *Service) unwatch(owner string) {
	if owner != "" && s.names != nil {
		s.names.Unwatch(owner)
	}
}

func (s *Service) nameLoop(ctx context.Context, ch <-chan *dbus.Signal) {
	defer s.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case sig, ok := <-ch:
			if !ok {
				return
			}
			if name, vanished := parseNameOwnerChanged(sig); vanished {
				s.nameVanished(name)
			}
		}
	}
}

// parseNameOwnerChanged reports the name of a NameOwnerChanged signal sent by
// the bus and whether the name lost its owner.
func parseNameOwnerChanged(sig *dbus.Signal) (string, bool) {
	if sig == nil || sig.Sender != dbusName || sig.Name != dbusName+".NameOwnerChanged" || len(sig.Body) != 3 {
		return "", false
	}
	name, ok1 := sig.Body[0].(string)
	newOwner, ok2 := sig.Body[2].(string)
	if !ok1 || !ok2 || name == "" {
		return "", false
	}
	return name, newOwner == ""
}

// nameOwnerChangedMatch matches NameOwnerChanged for one name.
func nameOwnerChangedMatch(name string) []dbus.MatchOption {
	return []dbus.MatchOption{
		dbus.WithMatchSender(dbusName),
		dbus.WithMatchObjectPath(dbusPath),
		dbus.WithMatchInterface(dbusName),
		dbus.WithMatchMember("NameOwnerChanged"),
		dbus.WithMatchArg(0, name),
	}
}

// busNameWatcher implements nameWatcher on the Service's private connection.
type busNameWatcher struct{ conn *dbus.Conn }

func (w busNameWatcher) Watch(name string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	if err := w.conn.AddMatchSignalContext(ctx, nameOwnerChangedMatch(name)...); err != nil {
		return false, err
	}
	var has bool
	err := w.conn.BusObject().CallWithContext(ctx, dbusName+".NameHasOwner", 0, name).Store(&has)
	return has, err
}

func (w busNameWatcher) Unwatch(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	_ = w.conn.RemoveMatchSignalContext(ctx, nameOwnerChangedMatch(name)...)
}
