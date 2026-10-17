package runtime

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// Scancodes used by the chord tests. Literal values keep this file independent
// of the fork's key-code constants.
const (
	chordTestLCtrl  uint16 = 29
	chordTestLShift uint16 = 42
	chordTestRShift uint16 = 54
	chordTestLAlt   uint16 = 56
	chordTestRCtrl  uint16 = 97
	chordTestRAlt   uint16 = 100
	chordTestKeyA   uint16 = 30
	chordTestF12    uint16 = 88
)

var chordTestNames = map[string]uint16{
	"L":  chordTestLShift,
	"R":  chordTestRShift,
	"Lc": chordTestLCtrl,
	"Rc": chordTestRCtrl,
	"La": chordTestLAlt,
	"Ra": chordTestRAlt,
	"A":  chordTestKeyA,
	"F":  chordTestF12,
}

type chordTestEvent struct {
	code  uint16
	value int32
}

func (e chordTestEvent) String() string {
	suffix := map[int32]string{K_UP: "-", K_DOWN: "+", K_REPEAT: "*"}[e.value]
	return fmt.Sprintf("%d%s", e.code, suffix)
}

type chordTestFire struct {
	index  int
	action ChordAction
}

// chordTestSeq parses "L+ R+ R- L-": a key name from chordTestNames followed
// by + (K_DOWN), - (K_UP) or * (K_REPEAT).
func chordTestSeq(t *testing.T, seq string) []chordTestEvent {
	t.Helper()
	var events []chordTestEvent
	for _, token := range strings.Fields(seq) {
		name, op := token[:len(token)-1], token[len(token)-1]
		code, ok := chordTestNames[name]
		if !ok {
			t.Fatalf("unknown key %q in %q", name, seq)
		}
		var value int32
		switch op {
		case '+':
			value = K_DOWN
		case '-':
			value = K_UP
		case '*':
			value = K_REPEAT
		default:
			t.Fatalf("bad token %q in %q", token, seq)
		}
		events = append(events, chordTestEvent{code: code, value: value})
	}
	return events
}

// chordTestDefaults returns a detector with the default bindings:
// toggle LShift+RShift, selection LShift+RCtrl+RAlt.
func chordTestDefaults() *ChordDetector {
	d := &ChordDetector{}
	d.Set(map[ChordAction][]uint16{
		ChordToggleAuto:       {chordTestLShift, chordTestRShift},
		ChordConvertSelection: {chordTestLShift, chordTestRCtrl, chordTestRAlt},
	})
	return d
}

// chordTestFeed feeds events and returns every fire with its event index. It
// fails the test if Feed reports an action without firing.
func chordTestFeed(t *testing.T, d *ChordDetector, events []chordTestEvent) []chordTestFire {
	t.Helper()
	var fires []chordTestFire
	for i, e := range events {
		action, fired := d.Feed(e.code, e.value)
		if !fired {
			if action != ChordNone {
				t.Fatalf("event %d (%v): action %v without fire", i, e, action)
			}
			continue
		}
		fires = append(fires, chordTestFire{index: i, action: action})
	}
	return fires
}

func chordTestAssertIdle(t *testing.T, d *ChordDetector, context string) {
	t.Helper()
	d.mu.Lock()
	state := d.state
	d.mu.Unlock()
	if state != chordIdle {
		t.Fatalf("%s: state = %d, want idle", context, state)
	}
	if n := d.HeldCount(); n != 0 {
		t.Fatalf("%s: HeldCount = %d, want 0", context, n)
	}
}

// chordTestAssertToggleWorks checks that a plain LShift+RShift cycle fires
// afterwards, i.e. the previous sequence left no residue.
func chordTestAssertToggleWorks(t *testing.T, d *ChordDetector, context string) {
	t.Helper()
	fires := chordTestFeed(t, d, chordTestSeq(t, "L+ R+ R- L-"))
	if len(fires) != 1 || fires[0] != (chordTestFire{index: 3, action: ChordToggleAuto}) {
		t.Fatalf("%s: follow-up toggle chord fires = %+v, want one toggle on the last event", context, fires)
	}
	chordTestAssertIdle(t, d, context+" (after follow-up)")
}

func chordTestPermutations(keys []uint16) [][]uint16 {
	if len(keys) <= 1 {
		return [][]uint16{append([]uint16(nil), keys...)}
	}
	var out [][]uint16
	for i := range keys {
		rest := make([]uint16, 0, len(keys)-1)
		rest = append(rest, keys[:i]...)
		rest = append(rest, keys[i+1:]...)
		for _, tail := range chordTestPermutations(rest) {
			out = append(out, append([]uint16{keys[i]}, tail...))
		}
	}
	return out
}

func TestChordActionString(t *testing.T) {
	tests := map[ChordAction]string{
		ChordNone:             "none",
		ChordToggleAuto:       "toggle-auto",
		ChordConvertSelection: "convert-selection",
		ChordAction(9):        "chord-action(9)",
	}
	for action, want := range tests {
		if got := action.String(); got != want {
			t.Errorf("ChordAction(%d).String() = %q, want %q", action, got, want)
		}
	}
}

func TestChordDetectorFiresOnFullRelease(t *testing.T) {
	for _, seq := range []string{
		"L+ R+ R- L-",
		"L+ R+ L- R-",
		"R+ L+ R- L-",
		"R+ L+ L- R-",
	} {
		t.Run(seq, func(t *testing.T) {
			d := chordTestDefaults()
			events := chordTestSeq(t, seq)
			fires := chordTestFeed(t, d, events)
			want := chordTestFire{index: len(events) - 1, action: ChordToggleAuto}
			if len(fires) != 1 || fires[0] != want {
				t.Fatalf("fires = %+v, want exactly %+v", fires, want)
			}
			chordTestAssertIdle(t, d, seq)
			chordTestAssertToggleWorks(t, d, seq)
		})
	}
}

func TestChordDetectorHeldShiftDoubleTapNeverFires(t *testing.T) {
	for _, seq := range []string{
		"L+ R+ R- R+ R- L-",
		"R+ L+ L- L+ L- R-",
		"L+ R+ R- R+ L- R-",
		"Lc+ L+ L- L+ L- Lc-",
	} {
		t.Run(seq, func(t *testing.T) {
			d := chordTestDefaults()
			if fires := chordTestFeed(t, d, chordTestSeq(t, seq)); len(fires) != 0 {
				t.Fatalf("fires = %+v, want none", fires)
			}
			chordTestAssertIdle(t, d, seq)
			chordTestAssertToggleWorks(t, d, seq)
		})
	}
}

func TestChordDetectorUpstreamDoubleShiftNeverFires(t *testing.T) {
	for _, seq := range []string{
		"L+ L- L+ L-",
		"R+ R- R+ R-",
		"A+ A- L+ L- L+ L-",
		"Lc+ L+ L- L+ L- Lc-",
		"L+ A+ A- L-",
	} {
		t.Run(seq, func(t *testing.T) {
			d := chordTestDefaults()
			if fires := chordTestFeed(t, d, chordTestSeq(t, seq)); len(fires) != 0 {
				t.Fatalf("fires = %+v, want none", fires)
			}
			chordTestAssertIdle(t, d, seq)
		})
	}
}

func TestChordDetectorExactSetOnly(t *testing.T) {
	for _, seq := range []string{
		"Lc+ L+ R+ R- L- Lc-",
		"L+ R+ A+ A- R- L-",
		"L+ R+ Lc+ Lc- R- L-",
		"L+ R+ R- A+ A- L-",
		"L+ R+ R- Lc+ L- Lc-",
		"Lc+ L+ R+ Lc- R- L-",
		"L+ Rc+ Lc+ Ra+ Ra- Rc- Lc- L-",
		"L+ Rc+ Ra+ R+ R- Ra- Rc- L-",
		"La+ L+ R+ R- L- La-",
	} {
		t.Run(seq, func(t *testing.T) {
			d := chordTestDefaults()
			if fires := chordTestFeed(t, d, chordTestSeq(t, seq)); len(fires) != 0 {
				t.Fatalf("fires = %+v, want none", fires)
			}
			chordTestAssertIdle(t, d, seq)
			chordTestAssertToggleWorks(t, d, seq)
		})
	}
}

// Arming looks only at the set held at the moment of the press: a modifier
// pressed and released before the chord keys complete the set does not
// prevent the chord (§2.3.2: "K_DOWN making held == binding").
func TestChordDetectorArmsOnExactHeldSet(t *testing.T) {
	d := chordTestDefaults()
	events := chordTestSeq(t, "Lc+ L+ Lc- R+ R- L-")
	fires := chordTestFeed(t, d, events)
	want := chordTestFire{index: len(events) - 1, action: ChordToggleAuto}
	if len(fires) != 1 || fires[0] != want {
		t.Fatalf("fires = %+v, want exactly %+v", fires, want)
	}
	chordTestAssertIdle(t, d, "after chord")
}

func TestChordDetectorThreeKeyChordAllOrders(t *testing.T) {
	keys := []uint16{chordTestLShift, chordTestRCtrl, chordTestRAlt}
	presses := chordTestPermutations(keys)
	releases := chordTestPermutations(keys)
	if len(presses) != 6 || len(releases) != 6 {
		t.Fatalf("permutations = %d/%d, want 6/6", len(presses), len(releases))
	}
	d := chordTestDefaults()
	for _, press := range presses {
		for _, release := range releases {
			var events []chordTestEvent
			for _, code := range press {
				events = append(events, chordTestEvent{code: code, value: K_DOWN})
			}
			for _, code := range release {
				events = append(events, chordTestEvent{code: code, value: K_UP})
			}
			fires := chordTestFeed(t, d, events)
			want := chordTestFire{index: len(events) - 1, action: ChordConvertSelection}
			if len(fires) != 1 || fires[0] != want {
				t.Fatalf("press %v release %v: fires = %+v, want exactly %+v", press, release, fires, want)
			}
			chordTestAssertIdle(t, d, fmt.Sprintf("press %v release %v", press, release))
		}
	}
}

func TestChordDetectorIgnoresRepeat(t *testing.T) {
	d := chordTestDefaults()
	events := chordTestSeq(t, "L+ L* L* R+ R* L* R* R- L* L-")
	fires := chordTestFeed(t, d, events)
	want := chordTestFire{index: len(events) - 1, action: ChordToggleAuto}
	if len(fires) != 1 || fires[0] != want {
		t.Fatalf("fires = %+v, want exactly %+v", fires, want)
	}
	chordTestAssertIdle(t, d, "after repeats")

	// A repeat is neither a press nor a release: it can neither arm nor break.
	if fires := chordTestFeed(t, d, chordTestSeq(t, "L+ R* L-")); len(fires) != 0 {
		t.Fatalf("repeat armed a chord: fires = %+v", fires)
	}
	chordTestAssertIdle(t, d, "after repeat-only second key")
	if fires := chordTestFeed(t, d, chordTestSeq(t, "L+ R+ A* R- L-")); len(fires) != 1 {
		t.Fatalf("repeat of an unrelated key broke the chord: fires = %+v", fires)
	}
	if n := d.HeldCount(); n != 0 {
		t.Fatalf("HeldCount = %d after repeats, want 0", n)
	}
}

func TestChordDetectorStaleKeyDownDropsOtherHeld(t *testing.T) {
	t.Run("idle", func(t *testing.T) {
		d := chordTestDefaults()
		// The releases of LCtrl and LShift were lost.
		chordTestFeed(t, d, chordTestSeq(t, "Lc+ L+"))
		if n := d.HeldCount(); n != 2 {
			t.Fatalf("HeldCount = %d, want 2", n)
		}
		// LShift goes down again: only LShift is held now, so the stale LCtrl
		// no longer blocks the exact set.
		chordTestFeed(t, d, chordTestSeq(t, "L+"))
		if n := d.HeldCount(); n != 1 {
			t.Fatalf("HeldCount after re-press = %d, want 1", n)
		}
		events := chordTestSeq(t, "R+ R- L-")
		fires := chordTestFeed(t, d, events)
		if len(fires) != 1 || fires[0] != (chordTestFire{index: 2, action: ChordToggleAuto}) {
			t.Fatalf("fires = %+v, want one toggle on the last event", fires)
		}
		chordTestAssertIdle(t, d, "idle stale press")
	})

	t.Run("armed", func(t *testing.T) {
		d := chordTestDefaults()
		// Armed, then LShift goes down again (its release was lost): broken,
		// and only LShift is still held.
		if fires := chordTestFeed(t, d, chordTestSeq(t, "L+ R+ L+")); len(fires) != 0 {
			t.Fatalf("fires = %+v, want none", fires)
		}
		if n := d.HeldCount(); n != 1 {
			t.Fatalf("HeldCount = %d, want 1", n)
		}
		if fires := chordTestFeed(t, d, chordTestSeq(t, "L- R-")); len(fires) != 0 {
			t.Fatalf("broken chord fired: %+v", fires)
		}
		chordTestAssertIdle(t, d, "armed stale press")
		chordTestAssertToggleWorks(t, d, "armed stale press")
	})

	t.Run("broken", func(t *testing.T) {
		d := chordTestDefaults()
		// Broken with A held; A's release is lost, then A is pressed again.
		chordTestFeed(t, d, chordTestSeq(t, "L+ R+ A+ R- L- A+"))
		if n := d.HeldCount(); n != 1 {
			t.Fatalf("HeldCount = %d, want 1", n)
		}
		if fires := chordTestFeed(t, d, chordTestSeq(t, "A-")); len(fires) != 0 {
			t.Fatalf("fires = %+v, want none", fires)
		}
		chordTestAssertIdle(t, d, "broken stale press")
		chordTestAssertToggleWorks(t, d, "broken stale press")
	})
}

func TestChordDetectorNoRearmWhileBroken(t *testing.T) {
	d := chordTestDefaults()
	// Break with A, release A (held is exactly the binding again), re-press
	// RShift (a press that makes held equal the binding): still broken.
	seq := "L+ R+ A+ A- R- R+ R- L-"
	if fires := chordTestFeed(t, d, chordTestSeq(t, seq)); len(fires) != 0 {
		t.Fatalf("fires = %+v, want none", fires)
	}
	chordTestAssertIdle(t, d, seq)

	// Broken by a second chord key press, then the selection set is reached
	// while still broken: nothing fires.
	seq = "L+ R+ Rc+ R- Ra+ Ra- Rc- L-"
	if fires := chordTestFeed(t, d, chordTestSeq(t, seq)); len(fires) != 0 {
		t.Fatalf("fires = %+v, want none", fires)
	}
	chordTestAssertIdle(t, d, seq)
	chordTestAssertToggleWorks(t, d, seq)
}

func TestChordDetectorIgnoresUnknownRelease(t *testing.T) {
	d := chordTestDefaults()
	// Releases of keys pressed before tracking started change nothing.
	if fires := chordTestFeed(t, d, chordTestSeq(t, "A- L- R-")); len(fires) != 0 {
		t.Fatalf("fires = %+v, want none", fires)
	}
	chordTestAssertIdle(t, d, "unknown releases")
	// An unknown release while releasing does not complete the chord.
	events := chordTestSeq(t, "L+ R+ R- A- L-")
	fires := chordTestFeed(t, d, events)
	if len(fires) != 1 || fires[0] != (chordTestFire{index: 4, action: ChordToggleAuto}) {
		t.Fatalf("fires = %+v, want one toggle on the last event", fires)
	}
}

func TestChordDetectorTwoBindings(t *testing.T) {
	d := chordTestDefaults()
	fires := chordTestFeed(t, d, chordTestSeq(t, "L+ Ra+ Rc+ L- Ra- Rc- L+ R+ R- L-"))
	want := []chordTestFire{{index: 5, action: ChordConvertSelection}, {index: 9, action: ChordToggleAuto}}
	if fmt.Sprint(fires) != fmt.Sprint(want) {
		t.Fatalf("fires = %+v, want %+v", fires, want)
	}
	chordTestAssertIdle(t, d, "two bindings")
}

func TestChordDetectorZeroValuePassThrough(t *testing.T) {
	sequences := []string{
		"L+ R+ R- L-",
		"L+ Ra+ Rc+ Rc- Ra- L-",
		"L+ L+ L- R+ R* R-",
	}
	check := func(t *testing.T, d *ChordDetector) {
		t.Helper()
		for _, seq := range sequences {
			if fires := chordTestFeed(t, d, chordTestSeq(t, seq)); len(fires) != 0 {
				t.Fatalf("%s: fires = %+v, want none", seq, fires)
			}
			if n := d.HeldCount(); n != 0 {
				t.Fatalf("%s: HeldCount = %d, want 0 (no tracking)", seq, n)
			}
		}
		d.Resync(map[uint16]bool{chordTestLShift: true, chordTestRShift: true})
		if n := d.HeldCount(); n != 0 {
			t.Fatalf("HeldCount after Resync = %d, want 0", n)
		}
		if action, fired := d.Feed(chordTestRShift, K_UP); fired || action != ChordNone {
			t.Fatalf("Feed after Resync = %v, %v", action, fired)
		}
		d.Reset()
	}

	t.Run("zero value", func(t *testing.T) {
		var d ChordDetector
		check(t, &d)
	})
	t.Run("embedded in a struct literal", func(t *testing.T) {
		host := &struct {
			name   string
			chords ChordDetector
		}{name: "switcher"}
		check(t, &host.chords)
	})
	for name, bindings := range map[string]map[ChordAction][]uint16{
		"Set(nil)":      nil,
		"Set(empty)":    {},
		"nil chords":    {ChordToggleAuto: nil, ChordConvertSelection: {}},
		"one-key chord": {ChordToggleAuto: {chordTestLShift}, ChordConvertSelection: {chordTestRCtrl, chordTestRCtrl}},
		"ChordNone":     {ChordNone: {chordTestLShift, chordTestRShift}},
	} {
		t.Run(name, func(t *testing.T) {
			d := chordTestDefaults()
			chordTestFeed(t, d, chordTestSeq(t, "L+ R+"))
			d.Set(bindings)
			if n := d.HeldCount(); n != 0 {
				t.Fatalf("HeldCount after Set = %d, want 0", n)
			}
			check(t, d)
		})
	}
}

func TestChordDetectorSetReplacesBindings(t *testing.T) {
	d := chordTestDefaults()
	chordTestAssertToggleWorks(t, d, "initial")

	toggle := []uint16{chordTestF12, chordTestLAlt, chordTestF12}
	d.Set(map[ChordAction][]uint16{ChordToggleAuto: toggle})
	// The caller's slice is copied: later edits do not change the binding.
	toggle[0], toggle[1] = chordTestKeyA, chordTestKeyA

	for _, seq := range []string{"L+ R+ R- L-", "L+ Ra+ Rc+ Rc- Ra- L-"} {
		if fires := chordTestFeed(t, d, chordTestSeq(t, seq)); len(fires) != 0 {
			t.Fatalf("old binding %q fired after Set: %+v", seq, fires)
		}
	}
	events := chordTestSeq(t, "La+ F+ La- F-")
	fires := chordTestFeed(t, d, events)
	if len(fires) != 1 || fires[0] != (chordTestFire{index: 3, action: ChordToggleAuto}) {
		t.Fatalf("new binding fires = %+v, want one toggle on the last event", fires)
	}

	// Set in the middle of a chord resets the state: the releases that
	// follow are of keys the detector no longer counts as held.
	chordTestFeed(t, d, chordTestSeq(t, "La+ F+"))
	d.Set(map[ChordAction][]uint16{ChordToggleAuto: {chordTestLAlt, chordTestF12}})
	if n := d.HeldCount(); n != 0 {
		t.Fatalf("HeldCount after Set = %d, want 0", n)
	}
	if fires := chordTestFeed(t, d, chordTestSeq(t, "F- La-")); len(fires) != 0 {
		t.Fatalf("chord armed before Set fired after it: %+v", fires)
	}
	chordTestAssertIdle(t, d, "after mid-chord Set")

	// Two actions on the same set: the lower action value wins.
	d.Set(map[ChordAction][]uint16{
		ChordConvertSelection: {chordTestRShift, chordTestLShift},
		ChordToggleAuto:       {chordTestLShift, chordTestRShift},
	})
	chordTestAssertToggleWorks(t, d, "duplicate set")
}

func TestChordDetectorResync(t *testing.T) {
	both := map[uint16]bool{chordTestLShift: true, chordTestRShift: true}

	t.Run("armed and unchanged keeps the chord", func(t *testing.T) {
		d := chordTestDefaults()
		chordTestFeed(t, d, chordTestSeq(t, "L+ R+"))
		d.Resync(both)
		fires := chordTestFeed(t, d, chordTestSeq(t, "R- L-"))
		if len(fires) != 1 || fires[0].action != ChordToggleAuto {
			t.Fatalf("fires = %+v, want one toggle", fires)
		}
	})

	t.Run("releasing and unchanged keeps the chord", func(t *testing.T) {
		d := chordTestDefaults()
		chordTestFeed(t, d, chordTestSeq(t, "L+ R+ R-"))
		d.Resync(map[uint16]bool{chordTestLShift: true, chordTestRShift: false})
		if n := d.HeldCount(); n != 1 {
			t.Fatalf("HeldCount = %d, want 1 (false entries are not held)", n)
		}
		fires := chordTestFeed(t, d, chordTestSeq(t, "L-"))
		if len(fires) != 1 || fires[0].action != ChordToggleAuto {
			t.Fatalf("fires = %+v, want one toggle", fires)
		}
	})

	t.Run("armed key missing returns to idle", func(t *testing.T) {
		d := chordTestDefaults()
		chordTestFeed(t, d, chordTestSeq(t, "L+ R+"))
		d.Resync(map[uint16]bool{chordTestLShift: true})
		d.mu.Lock()
		state := d.state
		d.mu.Unlock()
		if state != chordIdle {
			t.Fatalf("state = %d, want idle", state)
		}
		if n := d.HeldCount(); n != 1 {
			t.Fatalf("HeldCount = %d, want 1", n)
		}
		if fires := chordTestFeed(t, d, chordTestSeq(t, "R- L-")); len(fires) != 0 {
			t.Fatalf("fires = %+v, want none", fires)
		}
		chordTestAssertIdle(t, d, "missing key")
		chordTestAssertToggleWorks(t, d, "missing key")
	})

	t.Run("releasing key missing returns to idle", func(t *testing.T) {
		d := chordTestDefaults()
		chordTestFeed(t, d, chordTestSeq(t, "L+ R+ R-"))
		d.Resync(map[uint16]bool{})
		chordTestAssertIdle(t, d, "released during discard")
		if fires := chordTestFeed(t, d, chordTestSeq(t, "L-")); len(fires) != 0 {
			t.Fatalf("fires = %+v, want none", fires)
		}
	})

	t.Run("unseen press breaks", func(t *testing.T) {
		d := chordTestDefaults()
		chordTestFeed(t, d, chordTestSeq(t, "L+ R+"))
		d.Resync(map[uint16]bool{chordTestLShift: true, chordTestRShift: true, chordTestKeyA: true})
		if n := d.HeldCount(); n != 3 {
			t.Fatalf("HeldCount = %d, want 3", n)
		}
		if fires := chordTestFeed(t, d, chordTestSeq(t, "A- R- L-")); len(fires) != 0 {
			t.Fatalf("fires = %+v, want none", fires)
		}
		chordTestAssertIdle(t, d, "unseen press")
		chordTestAssertToggleWorks(t, d, "unseen press")
	})

	t.Run("broken ends when nothing is held", func(t *testing.T) {
		d := chordTestDefaults()
		chordTestFeed(t, d, chordTestSeq(t, "L+ R+ A+"))
		d.Resync(map[uint16]bool{chordTestLShift: true})
		if fires := chordTestFeed(t, d, chordTestSeq(t, "R+ R- L-")); len(fires) != 0 {
			t.Fatalf("broken chord fired after partial resync: %+v", fires)
		}
		chordTestFeed(t, d, chordTestSeq(t, "L+ R+ A+"))
		d.Resync(nil)
		chordTestAssertIdle(t, d, "broken resync to empty")
		chordTestAssertToggleWorks(t, d, "broken resync to empty")
	})

	t.Run("idle drops stale keys and never arms", func(t *testing.T) {
		d := chordTestDefaults()
		// Releases of LCtrl and A were discarded: without a resync the stale
		// keys would block the exact set.
		chordTestFeed(t, d, chordTestSeq(t, "Lc+ A+"))
		d.Resync(map[uint16]bool{})
		chordTestAssertIdle(t, d, "stale keys")
		chordTestAssertToggleWorks(t, d, "stale keys")

		// The kernel reports the chord keys down, but the press order is
		// unknown: resync must not arm.
		d.Resync(both)
		if fires := chordTestFeed(t, d, chordTestSeq(t, "R- L-")); len(fires) != 0 {
			t.Fatalf("resync armed a chord: %+v", fires)
		}
		chordTestAssertIdle(t, d, "resync without arming")
	})
}

func TestChordDetectorReset(t *testing.T) {
	d := chordTestDefaults()
	chordTestFeed(t, d, chordTestSeq(t, "L+ R+"))
	d.Reset()
	chordTestAssertIdle(t, d, "after Reset")
	if fires := chordTestFeed(t, d, chordTestSeq(t, "R- L-")); len(fires) != 0 {
		t.Fatalf("releases after Reset fired: %+v", fires)
	}
	chordTestAssertToggleWorks(t, d, "after Reset")

	// Reset keeps the bindings.
	d.Reset()
	fires := chordTestFeed(t, d, chordTestSeq(t, "Rc+ Ra+ L+ L- Rc- Ra-"))
	if len(fires) != 1 || fires[0].action != ChordConvertSelection {
		t.Fatalf("fires after Reset = %+v, want one selection", fires)
	}
}

// Set runs on the configuration goroutine while Feed runs on the Run
// goroutine; run with -race to check the locking.
func TestChordDetectorConcurrentSet(t *testing.T) {
	d := chordTestDefaults()
	events := chordTestSeq(t, "L+ R+ R- L- L+ Ra+ Rc+ Rc- Ra- L-")
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			if i%2 == 0 {
				d.Set(nil)
			} else {
				d.Set(map[ChordAction][]uint16{ChordToggleAuto: {chordTestLShift, chordTestRShift}})
			}
		}
	}()
	for i := 0; i < 200; i++ {
		for _, e := range events {
			d.Feed(e.code, e.value)
		}
		d.HeldCount()
		d.Resync(map[uint16]bool{chordTestLShift: i%3 == 0})
	}
	wg.Wait()
}
