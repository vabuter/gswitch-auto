package runtime

import (
	"testing"
	"time"

	"github.com/arumata/gswitch/internal/gswitch/layoutstate"
)

// layoutReaderTestKeys is the host's <Alt>Shift_L chord (LAlt=56, LShift=42);
// KEY_LEFTALT itself belongs to WP0's types_fork.go.
var layoutReaderTestKeys = []uint16{56, KEY_LEFTSHIFT}

func layoutReaderTestTracker() *layoutstate.Tracker {
	return layoutstate.New(layoutstate.Options{
		Layouts: [2]layoutstate.Layout{{Name: "us"}, {Name: "ru"}},
	})
}

func TestTrackerLayoutStateReaderNilTracker(t *testing.T) {
	state, err := trackerLayoutStateReader{}.CurrentState()
	if err == nil {
		t.Fatalf("CurrentState() with nil tracker = %q, nil; want error", state)
	}
}

func TestTrackerLayoutStateReaderRequiresConfirmed(t *testing.T) {
	tracker := layoutReaderTestTracker()
	reader := trackerLayoutStateReader{tracker: tracker}
	if _, err := reader.CurrentState(); err == nil {
		t.Fatal("CurrentState() while Unknown: error = nil")
	}

	tracker.Engine(layoutstate.SourceIBus, "xkb:ru::rus")
	state, err := reader.CurrentState()
	if err != nil || state != "1@1" {
		t.Fatalf("CurrentState() = %q, %v; want 1@1", state, err)
	}

	tracker.NoteSwitchHint()
	if _, err := reader.CurrentState(); err == nil {
		t.Fatal("CurrentState() while Pending: error = nil")
	}
	tracker.Engine(layoutstate.SourceIBus, "libpinyin")
	if _, err := reader.CurrentState(); err == nil {
		t.Fatal("CurrentState() for a non-pair engine: error = nil")
	}
}

func TestNewCompositeLayoutReaderNilTracker(t *testing.T) {
	env := testSessionEnv()
	got := newCompositeLayoutReader(nil, env)()
	reader, ok := got.(gnomeWaylandLayoutStateReader)
	if !ok || reader.env != env {
		t.Fatalf("newCompositeLayoutReader(nil, env)() = %#v, want gnomeWaylandLayoutStateReader{env}", got)
	}
}

func TestNewCompositeLayoutReaderPicksTrackerWhenConfirmed(t *testing.T) {
	env := testSessionEnv()
	tracker := layoutReaderTestTracker()
	readerFn := newCompositeLayoutReader(tracker, env)

	if _, ok := readerFn().(gnomeWaylandLayoutStateReader); !ok {
		t.Fatal("Unknown tracker: composite reader did not fall back to gsettings")
	}

	tracker.Engine(layoutstate.SourceIBus, "xkb:us::eng")
	got := readerFn()
	if reader, ok := got.(trackerLayoutStateReader); !ok || reader.tracker != tracker {
		t.Fatalf("Confirmed tracker: composite reader = %#v, want trackerLayoutStateReader", got)
	}

	tracker.NoteSwitchHint()
	if reader, ok := readerFn().(gnomeWaylandLayoutStateReader); !ok || reader.env != env {
		t.Fatal("Pending tracker: composite reader did not fall back to gsettings")
	}
}

func TestTriggerAndConfirmWithTrackerReader(t *testing.T) {
	tracker := layoutReaderTestTracker()
	tracker.Engine(layoutstate.SourceIBus, "xkb:us::eng")
	keys := layoutReaderTestKeys
	var emitted []KeyEvent

	err := triggerAndConfirmGNOMEWaylandLayoutSwitch(
		trackerLayoutStateReader{tracker: tracker},
		keys,
		func(event KeyEvent) error {
			emitted = append(emitted, event)
			// IBus reports the switch once the chord is released.
			if event.Code == keys[0] && event.Value == K_UP {
				tracker.Engine(layoutstate.SourceIBus, "xkb:ru::rus")
			}
			return nil
		},
		gnomeWaylandLayoutSwitchAttempts,
		gnomeWaylandLayoutGroupPollAttempts,
		0,
		func(time.Duration) {},
	)
	if err != nil {
		t.Fatalf("triggerAndConfirmGNOMEWaylandLayoutSwitch() error = %v", err)
	}
	if len(emitted) != 4 {
		t.Fatalf("emitted %d events, want one chord (4 events)", len(emitted))
	}
	if idx, known := tracker.Current(); !known || idx != 1 {
		t.Fatalf("tracker Current() = %d, %v; want 1, true", idx, known)
	}
}

func TestTriggerAndConfirmWithTrackerReaderUnchanged(t *testing.T) {
	tracker := layoutReaderTestTracker()
	tracker.Engine(layoutstate.SourceIBus, "xkb:us::eng")
	emitted := 0

	err := triggerAndConfirmGNOMEWaylandLayoutSwitch(
		trackerLayoutStateReader{tracker: tracker},
		layoutReaderTestKeys,
		func(KeyEvent) error {
			emitted++
			return nil
		},
		gnomeWaylandLayoutSwitchAttempts,
		gnomeWaylandLayoutGroupPollAttempts,
		0,
		func(time.Duration) {},
	)
	if err == nil {
		t.Fatal("triggerAndConfirmGNOMEWaylandLayoutSwitch() error = nil, want unchanged-state error")
	}
	if emitted != 8 {
		t.Fatalf("emitted %d events, want two attempts (8 events)", emitted)
	}
}

func TestTriggerAndConfirmWithUnknownTrackerFailsClosed(t *testing.T) {
	emitted := 0
	err := triggerAndConfirmGNOMEWaylandLayoutSwitch(
		trackerLayoutStateReader{tracker: layoutReaderTestTracker()},
		layoutReaderTestKeys,
		func(KeyEvent) error {
			emitted++
			return nil
		},
		gnomeWaylandLayoutSwitchAttempts,
		gnomeWaylandLayoutGroupPollAttempts,
		0,
		func(time.Duration) {},
	)
	if err == nil || emitted != 0 {
		t.Fatalf("Unknown tracker: err = %v, emitted = %d; want error before any event", err, emitted)
	}
}
