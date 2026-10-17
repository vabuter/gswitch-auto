package runtime

import (
	"cmp"
	"slices"
	"strconv"
	"sync"
)

// ChordAction is the switcher action bound to a key chord.
type ChordAction uint8

const (
	ChordNone ChordAction = iota
	ChordToggleAuto
	ChordConvertSelection
)

// String returns the stable name used in logs and diagnostics.
func (a ChordAction) String() string {
	switch a {
	case ChordNone:
		return "none"
	case ChordToggleAuto:
		return "toggle-auto"
	case ChordConvertSelection:
		return "convert-selection"
	default:
		return "chord-action(" + strconv.Itoa(int(a)) + ")"
	}
}

// chordMinKeys is the smallest chord the detector accepts. A one-key chord
// would fire on every bare release of that key (for Shift that would swallow
// the upstream double-Shift gesture), so shorter bindings are ignored even if
// the configuration layer let one through.
const chordMinKeys = 2

type chordState uint8

const (
	// chordIdle: no binding armed; a key press that makes the held set equal
	// to a binding arms it.
	chordIdle chordState = iota
	// chordArmed: the held set equals the armed binding and nothing has been
	// released yet.
	chordArmed
	// chordReleasing: at least one key of the armed binding was released and
	// no key has been pressed since.
	chordReleasing
	// chordBroken: a key was pressed while armed or releasing; nothing fires
	// or re-arms until every key is released.
	chordBroken
)

// chordBinding is one configured chord: its action and its key set (scancodes,
// sorted ascending, distinct).
type chordBinding struct {
	action ChordAction
	keys   []uint16
}

// ChordDetector is an exact-set chord matcher over raw evdev key events.
//
// A chord arms on the key press that makes the set of held keys exactly equal
// to a binding (nothing else held), and fires once, on the key release that
// empties the held set, provided no key was pressed after arming. Any key
// press while armed or releasing (including a second press of a chord key)
// breaks the cycle; a broken cycle never re-arms until all keys are released.
// Autorepeat is ignored and there are no timers.
//
// The zero value has no bindings and is a pass-through: Feed returns false
// without tracking keys, so structs that embed a ChordDetector by value (such
// as test literals of Switcher) need no initialisation. Keys are scancodes
// only; events carry no device id, so the same code on two keyboards is one
// key (the same limitation as upstream Shift tracking).
//
// All methods are guarded by a mutex: Feed, Resync, Reset and HeldCount run on
// the Run goroutine, Set may be called from the configuration goroutine.
type ChordDetector struct {
	mu       sync.Mutex
	bindings []chordBinding
	held     map[uint16]struct{}
	state    chordState
	armed    int // index into bindings while chordArmed or chordReleasing
}

// Set replaces the bindings (hot reload) and resets the detector state. A nil
// or empty key list disables that action; a list with fewer than two distinct
// codes and the ChordNone action are ignored. Key lists are copied. If two
// actions are bound to the same key set, the one with the lower action value
// wins. Set(nil) turns the detector back into a pass-through.
func (d *ChordDetector) Set(bindings map[ChordAction][]uint16) {
	list := make([]chordBinding, 0, len(bindings))
	for action, keys := range bindings {
		if action == ChordNone {
			continue
		}
		normalized := chordNormalizeKeys(keys)
		if len(normalized) < chordMinKeys {
			continue
		}
		list = append(list, chordBinding{action: action, keys: normalized})
	}
	slices.SortFunc(list, func(a, b chordBinding) int {
		return cmp.Compare(a.action, b.action)
	})
	if len(list) == 0 {
		list = nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	d.bindings = list
	d.resetLocked()
}

// Feed processes one EV_KEY event and reports the action of a chord that
// fired on it. It fires only on the K_UP that empties the held set; every
// other event returns (ChordNone, false). K_REPEAT is ignored. Without
// bindings Feed does nothing and never fires.
func (d *ChordDetector) Feed(code uint16, value int32) (ChordAction, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.bindings) == 0 {
		return ChordNone, false
	}
	switch value {
	case K_DOWN:
		d.keyDownLocked(code)
		return ChordNone, false
	case K_UP:
		return d.keyUpLocked(code)
	default:
		return ChordNone, false
	}
}

// HeldCount returns the number of keys the detector believes are held. It is
// always 0 without bindings.
func (d *ChordDetector) HeldCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.held)
}

// Resync replaces the held set with the kernel's view (codes mapped to true)
// after input was discarded. An armed or releasing chord drops back to idle
// when a key it still counted as held is no longer down (its release was
// discarded, so the chord does not fire), and breaks when an unseen key went
// down; a broken cycle ends once nothing is held. Resync never arms a chord.
// Without bindings it does nothing.
func (d *ChordDetector) Resync(down map[uint16]bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.bindings) == 0 {
		return
	}

	next := make(map[uint16]struct{}, len(down))
	for code, isDown := range down {
		if isDown {
			next[code] = struct{}{}
		}
	}

	switch d.state {
	case chordArmed, chordReleasing:
		missing := false
		for code := range d.held {
			if _, ok := next[code]; !ok {
				missing = true
				break
			}
		}
		switch {
		case missing:
			d.state = chordIdle
		case len(next) != len(d.held):
			// d.held is a subset of next, so next has keys pressed unseen.
			d.state = chordBroken
		}
	case chordBroken:
		if len(next) == 0 {
			d.state = chordIdle
		}
	}
	d.held = next
	if d.state == chordIdle {
		d.armed = 0
	}
}

// Reset forgets all held keys and returns to idle. Releases of keys that were
// held before the reset are ignored afterwards.
func (d *ChordDetector) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.resetLocked()
}

func (d *ChordDetector) resetLocked() {
	d.held = nil
	d.state = chordIdle
	d.armed = 0
}

func (d *ChordDetector) keyDownLocked(code uint16) {
	if d.held == nil {
		d.held = make(map[uint16]struct{}, chordMinKeys*2)
	}
	if _, ok := d.held[code]; ok {
		// A press of a key that is already held means its release was lost,
		// and other releases may have been lost with it: keep only this key.
		clear(d.held)
	}
	d.held[code] = struct{}{}

	switch d.state {
	case chordIdle:
		if i := d.matchHeldLocked(); i >= 0 {
			d.state = chordArmed
			d.armed = i
		}
	case chordArmed, chordReleasing:
		d.state = chordBroken
	}
}

func (d *ChordDetector) keyUpLocked(code uint16) (ChordAction, bool) {
	if _, ok := d.held[code]; !ok {
		// Release of a key pressed before tracking started or before a reset.
		return ChordNone, false
	}
	delete(d.held, code)
	if d.state == chordArmed {
		d.state = chordReleasing
	}
	if len(d.held) != 0 {
		return ChordNone, false
	}

	fired := d.state == chordReleasing
	action := ChordNone
	if fired {
		action = d.bindings[d.armed].action
	}
	d.state = chordIdle
	d.armed = 0
	return action, fired
}

// matchHeldLocked returns the index of the binding whose key set equals the
// held set, or -1.
func (d *ChordDetector) matchHeldLocked() int {
	for i, binding := range d.bindings {
		if len(binding.keys) != len(d.held) {
			continue
		}
		match := true
		for _, code := range binding.keys {
			if _, ok := d.held[code]; !ok {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// chordNormalizeKeys returns a sorted copy of keys without duplicates.
func chordNormalizeKeys(keys []uint16) []uint16 {
	if len(keys) == 0 {
		return nil
	}
	normalized := slices.Clone(keys)
	slices.Sort(normalized)
	return slices.Compact(normalized)
}
