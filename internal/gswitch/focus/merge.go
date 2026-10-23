package focus

import (
	"cmp"
	"context"
	"sync"
	"sync/atomic"

	"github.com/arumata/gswitch/internal/gswitch/fold"
)

// gswitch-auto: merged focus source (SPEC-X11 §3.2.2). The daemon sees one
// Source: the extension client supplies availability, purpose and locks, the
// X11 source supplies the application identity while the extension is
// absent.

// MergedOptions configures NewMerged.
type MergedOptions struct {
	OnChange func(State) // called after every merged change (any goroutine; must not block)
	Logf     func(string, ...any)
}

// extensionSource is what Merged drives on the extension side (*Client).
type extensionSource interface {
	Source
	Start(context.Context)
	Close()
}

// mergedState is the published pair (state, app source).
type mergedState struct {
	st        State
	appSource string
}

// Merged combines the extension client and the X11 source into one Source
// for the daemon. It owns nothing: callers create the Client with
// Options.OnChange = merged.ExtensionChanged and the X11Source with
// X11Options.OnChange = merged.X11Changed, then SetExtension/SetX11 before
// Start. Either side may be nil.
type Merged struct {
	opts  MergedOptions
	ext   extensionSource // nil when the extension client is not used
	x11   *X11Source      // nil when the EWMH source is not used
	state atomic.Pointer[mergedState]

	mu      sync.Mutex // serialises merges and the lifecycle flags
	lastExt State
	lastX11 X11State
	started bool
	closed  bool
}

var _ Source = (*Merged)(nil)

// NewMerged returns a merged source with no sides; its snapshot is the
// client's initial state (extension absent, purpose unknown).
func NewMerged(opts MergedOptions) *Merged {
	m := &Merged{opts: opts, lastExt: State{Purpose: PurposeUnknown}}
	st, src := mergeStates(m.lastExt, m.lastX11)
	m.state.Store(&mergedState{st: st, appSource: src})
	return m
}

// SetExtension installs the extension client; a nil client leaves the side
// empty (a nil *Client in the interface field would be a non-nil interface
// holding a typed nil). Call before Start.
func (m *Merged) SetExtension(c *Client) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c != nil {
		m.ext = c
	}
}

// SetX11 installs the EWMH source; nil leaves the side empty. Call before
// Start.
func (m *Merged) SetX11(s *X11Source) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s != nil {
		m.x11 = s
	}
}

// ExtensionChanged is the adapter for Client.Options.OnChange.
func (m *Merged) ExtensionChanged(st State) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastExt = st
	m.mergeLocked()
}

// X11Changed is the adapter for X11Options.OnChange.
func (m *Merged) X11Changed(st X11State) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastX11 = st
	m.mergeLocked()
}

// Start starts both sides (nil-safe). It is a no-op when the source was
// started or closed already.
func (m *Merged) Start(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started || m.closed {
		return
	}
	m.started = true
	if m.ext != nil {
		m.ext.Start(ctx)
	}
	if m.x11 != nil {
		m.x11.Start(ctx)
	}
}

// Close closes both sides (nil-safe). It is idempotent. The sides are closed
// outside the lock: their goroutines may be inside an adapter call.
func (m *Merged) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	ext, x11 := m.ext, m.x11
	m.mu.Unlock()
	if ext != nil {
		ext.Close()
	}
	if x11 != nil {
		x11.Close()
	}
}

// Snapshot implements Source. It is lock-free and safe for concurrent use.
func (m *Merged) Snapshot() State {
	return m.state.Load().st
}

// AppSource names where the application identity in Snapshot comes from:
// AppSourceExtension, AppSourceX11 or "" (neither side knows the window).
func (m *Merged) AppSource() string {
	return m.state.Load().appSource
}

// mergeLocked recomputes the merged state and notifies OnChange when the
// state differs or Seq moved (both sides bump their Seq on every change, so
// a forced FocusChanged of the extension moves the merged Seq too).
func (m *Merged) mergeLocked() {
	cur := m.state.Load()
	st, src := mergeStates(m.lastExt, m.lastX11)
	if sameState(cur.st, st) && st.Seq == cur.st.Seq && src == cur.appSource {
		return
	}
	m.state.Store(&mergedState{st: st, appSource: src})
	if src != cur.appSource {
		m.logf("focus: application identity from %s", cmp.Or(src, "no source"))
	}
	if m.opts.OnChange != nil {
		m.opts.OnChange(st)
	}
}

func (m *Merged) logf(format string, args ...any) {
	if m.opts.Logf != nil {
		m.opts.Logf(format, args...)
	}
}

// mergeStates is the merge rule. Available, EverAvailable, Purpose,
// FocusUnknown, Locked and LockReason always come from ext, so Policy keeps
// its meaning (Available = extension owned). The application identity comes
// from ext while it is available, else from x while it is connected, else it
// is empty. Seq is the sum of both monotonic sequences; UpdatedAt the later
// of the two.
func mergeStates(ext State, x X11State) (State, string) {
	st := ext
	var appSource string
	switch {
	case ext.Available:
		appSource = AppSourceExtension
	case x.Connected:
		st.AppID, st.WmClass = x.AppID, x.WmClass
		st.AppKey, st.WmClassKey = fold.AppKey(x.AppID), fold.AppKey(x.WmClass)
		appSource = AppSourceX11
	default:
		st.AppID, st.WmClass, st.AppKey, st.WmClassKey = "", "", "", ""
	}
	st.Seq = ext.Seq + x.Seq
	if x.UpdatedAt.After(st.UpdatedAt) {
		st.UpdatedAt = x.UpdatedAt
	}
	return st, appSource
}
