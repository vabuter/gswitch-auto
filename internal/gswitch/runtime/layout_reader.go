package runtime

import (
	"errors"
	"fmt"

	"github.com/arumata/gswitch/internal/gswitch/layoutstate"
)

// trackerLayoutStateReader implements layoutStateReader
// (gnome_wayland_layout_switch.go) on top of the IBus layout tracker.
// CurrentState returns "<index>@<generation>" while the tracker is Confirmed
// and an error otherwise (also when tracker is nil), so a switch confirmed
// through it fails closed whenever the layout is not known. Each poll is a
// mutex read instead of a gsettings spawn. Used directly by the auto path
// (emitOptions.confirm).
type trackerLayoutStateReader struct{ tracker *layoutstate.Tracker }

func (reader trackerLayoutStateReader) CurrentState() (string, error) {
	if reader.tracker == nil {
		return "", errors.New("read tracked layout: no layout tracker")
	}
	st := reader.tracker.State()
	if st.Confidence != layoutstate.Confirmed {
		return "", fmt.Errorf("read tracked layout: state is %s", st.Confidence)
	}
	return fmt.Sprintf("%d@%d", st.Index, st.Generation), nil
}

// newCompositeLayoutReader returns a func that yields the tracker reader when t
// != nil and t is Confirmed at call time, else gnomeWaylandLayoutStateReader{env}.
// newCompositeLayoutReader(nil, env) is valid and always yields
// gnomeWaylandLayoutStateReader{env}. WP11 installs it as s.layoutReaderFn only
// when s.tracker != nil.
func newCompositeLayoutReader(t *layoutstate.Tracker, env *SessionEnv) func() layoutStateReader {
	return func() layoutStateReader {
		if t != nil && t.State().Confidence == layoutstate.Confirmed {
			return trackerLayoutStateReader{tracker: t}
		}
		return gnomeWaylandLayoutStateReader{env: env}
	}
}
