package ipc

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

type ipcTestSetCall struct {
	enabled bool
	source  string
}

type ipcTestSuspendCall struct {
	suspend bool
	owner   string
}

// ipcTestController is a fake Controller recording every call.
type ipcTestController struct {
	mu         sync.Mutex
	enabled    bool
	setErr     error
	reloadErr  error
	suspendErr error
	sets       []ipcTestSetCall
	suspends   []ipcTestSuspendCall
	reloads    int
	snapshot   Status
	setCh      chan ipcTestSetCall
}

func (c *ipcTestController) AutoEnabled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enabled
}

func (c *ipcTestController) SetAutoEnabled(enabled bool, source string) error {
	c.mu.Lock()
	c.sets = append(c.sets, ipcTestSetCall{enabled, source})
	err := c.setErr
	if err == nil {
		c.enabled = enabled
	}
	ch := c.setCh
	c.mu.Unlock()
	if ch != nil {
		ch <- ipcTestSetCall{enabled, source}
	}
	return err
}

func (c *ipcTestController) Reload() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reloads++
	return c.reloadErr
}

func (c *ipcTestController) SuspendHotkeys(suspend bool, owner string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.suspends = append(c.suspends, ipcTestSuspendCall{suspend, owner})
	if suspend {
		return c.suspendErr
	}
	return nil
}

func (c *ipcTestController) Snapshot() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := cloneStatus(c.snapshot)
	st.AutoEnabled = c.enabled
	return st
}

func (c *ipcTestController) suspendCalls() []ipcTestSuspendCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.suspends)
}

// ipcTestPropStore mimics prop.Properties storage: values are held behind
// pointers and written with dbus.Store, exactly like godbus v5.2.2.
type ipcTestPropStore struct {
	mu      sync.Mutex
	values  map[string]any
	sets    []string
	block   chan struct{}
	entered chan struct{}
}

func ipcTestNewPropStore(st Status) *ipcTestPropStore {
	ps := &ipcTestPropStore{values: map[string]any{}}
	for name, v := range statusValues(st) {
		ptr := reflect.New(reflect.TypeOf(v))
		ptr.Elem().Set(reflect.ValueOf(v))
		ps.values[name] = ptr.Interface()
	}
	return ps
}

func (p *ipcTestPropStore) GetMust(iface, property string) any {
	if iface != Interface {
		panic("unexpected interface " + iface)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return reflect.ValueOf(p.values[property]).Elem().Interface()
}

func (p *ipcTestPropStore) SetMust(iface, property string, v any) {
	if p.entered != nil {
		select {
		case p.entered <- struct{}{}:
		default:
		}
	}
	if p.block != nil {
		<-p.block
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := dbus.Store([]any{v}, p.values[property]); err != nil {
		panic(err)
	}
	p.sets = append(p.sets, property)
}

// storeRaw emulates godbus storing a property Set without counting it.
func (p *ipcTestPropStore) storeRaw(property string, v any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := dbus.Store([]any{v}, p.values[property]); err != nil {
		panic(err)
	}
}

func (p *ipcTestPropStore) takeSets() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.sets
	p.sets = nil
	return out
}

type ipcTestEmitted struct {
	member      string
	args        []any
	autoEnabled bool // exported AutoEnabled at emission time
}

type ipcTestNames struct {
	mu        sync.Mutex
	absent    map[string]bool
	watched   []string
	unwatched []string
}

func (n *ipcTestNames) Watch(name string) (bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.watched = append(n.watched, name)
	return !n.absent[name], nil
}

func (n *ipcTestNames) Unwatch(name string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.unwatched = append(n.unwatched, name)
}

type ipcTestTimer struct {
	d       time.Duration
	f       func()
	stopped bool
}

type ipcTestClock struct {
	mu     sync.Mutex
	timers []*ipcTestTimer
}

func (c *ipcTestClock) afterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	tm := &ipcTestTimer{d: d, f: f}
	c.timers = append(c.timers, tm)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		was := !tm.stopped
		tm.stopped = true
		return was
	}
}

func (c *ipcTestClock) last() *ipcTestTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.timers) == 0 {
		return nil
	}
	return c.timers[len(c.timers)-1]
}

type ipcTestHarness struct {
	s       *Service
	ctl     *ipcTestController
	store   *ipcTestPropStore
	names   *ipcTestNames
	clock   *ipcTestClock
	mu      sync.Mutex
	emitted []ipcTestEmitted
	logs    []string
}

func ipcTestNewHarness(t *testing.T, initial Status) *ipcTestHarness {
	t.Helper()
	h := &ipcTestHarness{
		ctl:   &ipcTestController{snapshot: initial, enabled: initial.AutoEnabled},
		store: ipcTestNewPropStore(initial),
		names: &ipcTestNames{absent: map[string]bool{}},
		clock: &ipcTestClock{},
	}
	h.s = newService(h.ctl, func(format string, args ...any) {
		h.mu.Lock()
		h.logs = append(h.logs, fmt.Sprintf(format, args...))
		h.mu.Unlock()
	})
	h.s.props = h.store
	h.s.names = h.names
	h.s.afterFunc = h.clock.afterFunc
	h.s.emit = func(member string, args ...any) error {
		auto, _ := h.store.GetMust(Interface, propAutoEnabled).(bool)
		h.mu.Lock()
		h.emitted = append(h.emitted, ipcTestEmitted{member: member, args: args, autoEnabled: auto})
		h.mu.Unlock()
		return nil
	}
	t.Cleanup(h.s.Close)
	return h
}

func (h *ipcTestHarness) takeEmitted() []ipcTestEmitted {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.emitted
	h.emitted = nil
	return out
}

func ipcTestWait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestUpdateStatusDiffSetsOnlyChangedProperties(t *testing.T) {
	initial := ipcTestFullStatus()
	h := ipcTestNewHarness(t, initial)

	h.s.UpdateStatus(initial)
	h.s.flush()
	if sets := h.store.takeSets(); len(sets) != 0 {
		t.Fatalf("unchanged status set %v", sets)
	}

	next := ipcTestFullStatus()
	next.ActiveLayout = "us"
	next.Count = 8
	next.ConfigWarnings = []string{"w1", "w2"}
	h.s.UpdateStatus(next)
	h.s.flush()
	want := []string{propActiveLayout, propCorrectionCount, propConfigWarnings}
	if sets := h.store.takeSets(); !reflect.DeepEqual(sets, want) {
		t.Fatalf("sets = %v, want %v", sets, want)
	}

	// Shrinking a slice and dropping a map key converge to the new value.
	next.ConfigWarnings = nil
	next.Stats = map[string]uint64{"armed": 4}
	h.s.UpdateStatus(next)
	h.s.flush()
	if got := h.store.GetMust(Interface, propConfigWarnings); !reflect.DeepEqual(got, []string{}) {
		t.Fatalf("ConfigWarnings = %#v", got)
	}
	stats, _ := h.store.GetMust(Interface, propStats).(map[string]uint64)
	if stats["armed"] != 4 || stats["fired"] != 0 || stats["custom"] != 0 {
		t.Fatalf("Stats = %v", stats)
	}
	h.store.takeSets()
	h.s.UpdateStatus(next)
	h.s.flush()
	if sets := h.store.takeSets(); len(sets) != 0 {
		t.Fatalf("repeated status set %v", sets)
	}
}

func TestUpdateStatusLatestWinsAndCopiesInput(t *testing.T) {
	h := ipcTestNewHarness(t, Status{})
	words := []string{"one"}
	h.s.UpdateStatus(Status{ActiveLayout: "us", PendingLearnedWords: words})
	h.s.UpdateStatus(Status{ActiveLayout: "ru", PendingLearnedWords: words})
	words[0] = "mutated"
	h.s.flush()
	if got := h.store.GetMust(Interface, propActiveLayout); got != "ru" {
		t.Fatalf("ActiveLayout = %v", got)
	}
	if got := h.store.GetMust(Interface, propPendingLearnedWords); !reflect.DeepEqual(got, []string{"one"}) {
		t.Fatalf("PendingLearnedWords = %v", got)
	}
}

func TestSignalsFIFOAndAutoEnabledChangedAfterProperty(t *testing.T) {
	h := ipcTestNewHarness(t, Status{})
	at := time.Unix(1760000000, 0)
	h.s.NotifyAutoEnabled(true, SourceChord)
	h.s.NotifyCorrected(Correction{From: "ghbdtn", To: "привет", Direction: "us>ru", Time: at})
	h.s.NotifyUndone("ghbdtn", true)
	h.s.NotifyAutoEnabled(false, SourceDBus)
	h.s.flush()

	got := h.takeEmitted()
	want := []ipcTestEmitted{
		{member: sigAutoEnabledChanged, args: []any{true, SourceChord}, autoEnabled: true},
		{member: sigCorrected, args: []any{"ghbdtn", "привет", "us>ru", uint64(1760000000)}, autoEnabled: true},
		{member: sigCorrectionUndone, args: []any{"ghbdtn", true}, autoEnabled: true},
		{member: sigAutoEnabledChanged, args: []any{false, SourceDBus}, autoEnabled: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("emitted:\n got %+v\nwant %+v", got, want)
	}
}

func TestStatusAppliedBeforeQueuedSignals(t *testing.T) {
	h := ipcTestNewHarness(t, Status{})
	h.s.NotifyAutoEnabled(true, SourceChord)
	h.s.UpdateStatus(Status{AutoEnabled: true, ToggleChord: "42+54"})
	h.s.flush()
	if sets := h.store.takeSets(); !reflect.DeepEqual(sets, []string{propAutoEnabled, propToggleChord}) {
		t.Fatalf("sets = %v", sets)
	}
	if got := h.takeEmitted(); len(got) != 1 || !got[0].autoEnabled {
		t.Fatalf("emitted = %+v", got)
	}
}

func TestNotifyDropsWhenQueueFull(t *testing.T) {
	h := ipcTestNewHarness(t, Status{})
	for range signalQueueSize + 10 {
		h.s.NotifyUndone("w", false)
	}
	if n := len(h.s.signals); n != signalQueueSize {
		t.Fatalf("queued %d signals, want %d", n, signalQueueSize)
	}
	h.mu.Lock()
	logged := len(h.logs)
	h.mu.Unlock()
	if logged != 10 {
		t.Fatalf("logged %d drops, want 10", logged)
	}
}

func TestUpdateStatusAndNotifyDoNotBlockWhilePropLoopBlocked(t *testing.T) {
	h := ipcTestNewHarness(t, Status{})
	h.store.block = make(chan struct{})
	h.store.entered = make(chan struct{}, 1)
	h.s.startLoops()

	h.s.UpdateStatus(Status{ActiveLayout: "ru"})
	select {
	case <-h.store.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("propLoop did not reach SetMust")
	}

	start := time.Now()
	for i := range 1000 {
		h.s.UpdateStatus(Status{ActiveLayout: "us", Count: uint32(i)})
	}
	for range 3 * signalQueueSize {
		h.s.NotifyAutoEnabled(true, SourceChord)
		h.s.NotifyCorrected(Correction{From: "a", To: "b"})
		h.s.NotifyUndone("a", false)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("UpdateStatus/Notify blocked for %s", elapsed)
	}
	close(h.store.block)

	ipcTestWait(t, "latest status applied", func() bool {
		return h.store.GetMust(Interface, propCorrectionCount) == uint32(999)
	})
	start = time.Now()
	h.s.Close()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Close took %s", elapsed)
	}
}

func TestSetAutoEnabledMethod(t *testing.T) {
	h := ipcTestNewHarness(t, Status{})
	m := &methods{s: h.s}
	if err := m.SetAutoEnabled(true); err != nil {
		t.Fatalf("SetAutoEnabled: %v", err)
	}
	h.ctl.setErr = errors.New("auto unavailable")
	err := m.SetAutoEnabled(false)
	if err == nil || err.Name != ErrNotAvailable {
		t.Fatalf("refused SetAutoEnabled error = %v", err)
	}
	want := []ipcTestSetCall{{true, SourceDBus}, {false, SourceDBus}}
	if !reflect.DeepEqual(h.ctl.sets, want) {
		t.Fatalf("controller calls = %v, want %v", h.ctl.sets, want)
	}
}

func TestSetAutoEnabledPropertyCallback(t *testing.T) {
	h := ipcTestNewHarness(t, Status{})
	h.ctl.setCh = make(chan ipcTestSetCall, 4)
	h.s.startLoops()

	if err := h.s.onAutoEnabledSet(&prop.Change{Iface: Interface, Name: propAutoEnabled, Value: true}); err != nil {
		t.Fatalf("callback: %v", err)
	}
	h.store.storeRaw(propAutoEnabled, true) // godbus stores the value after the callback
	select {
	case call := <-h.ctl.setCh:
		if call != (ipcTestSetCall{true, SourceDBus}) {
			t.Fatalf("controller call = %+v", call)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("controller not called")
	}

	// A refused request is reverted to the controller's value.
	h.ctl.mu.Lock()
	h.ctl.setErr = errors.New("refused")
	h.ctl.mu.Unlock()
	if err := h.s.onAutoEnabledSet(&prop.Change{Value: false}); err != nil {
		t.Fatalf("callback: %v", err)
	}
	h.store.storeRaw(propAutoEnabled, false)
	<-h.ctl.setCh
	ipcTestWait(t, "AutoEnabled reverted", func() bool {
		return h.store.GetMust(Interface, propAutoEnabled) == true
	})
}

func TestAutoEnabledCallbackRejectsWrongTypeAndFullQueue(t *testing.T) {
	h := ipcTestNewHarness(t, Status{})
	if err := h.s.onAutoEnabledSet(&prop.Change{Value: "yes"}); err == nil || err.Name != prop.ErrInvalidArg.Name {
		t.Fatalf("wrong type error = %v", err)
	}
	for i := range requestQueueSize {
		if err := h.s.onAutoEnabledSet(&prop.Change{Value: true}); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	err := h.s.onAutoEnabledSet(&prop.Change{Value: true})
	if err == nil || err.Name != "org.freedesktop.DBus.Error.Failed" {
		t.Fatalf("full queue error = %v", err)
	}
	if len(h.ctl.sets) != 0 {
		t.Fatal("callback called the controller")
	}
}

func TestReloadErrorIsConfigError(t *testing.T) {
	h := ipcTestNewHarness(t, Status{})
	m := &methods{s: h.s}
	if err := m.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	h.ctl.reloadErr = errors.New("auto.conf: bad value")
	err := m.Reload()
	if err == nil || err.Name != ErrConfig {
		t.Fatalf("Reload error = %v", err)
	}
	if len(err.Body) != 1 || err.Body[0] != "auto.conf: bad value" {
		t.Fatalf("Reload error body = %v", err.Body)
	}
}

func TestSuspendHotkeysOwnerRecorded(t *testing.T) {
	h := ipcTestNewHarness(t, Status{})
	m := &methods{s: h.s}
	if err := m.SuspendHotkeys(":1.7", true); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if tm := h.clock.last(); tm == nil || tm.d != suspendTimeout {
		t.Fatalf("watchdog = %+v", tm)
	}
	// Another caller cannot end the suspension.
	if err := m.SuspendHotkeys(":1.8", false); err != nil {
		t.Fatalf("foreign resume: %v", err)
	}
	if err := m.SuspendHotkeys(":1.7", false); err != nil {
		t.Fatalf("resume: %v", err)
	}
	want := []ipcTestSuspendCall{{true, ":1.7"}, {false, ":1.7"}}
	if got := h.ctl.suspendCalls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("controller calls = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(h.names.watched, []string{":1.7"}) || !reflect.DeepEqual(h.names.unwatched, []string{":1.7"}) {
		t.Fatalf("watched %v unwatched %v", h.names.watched, h.names.unwatched)
	}
	if !h.clock.last().stopped {
		t.Fatal("watchdog not stopped")
	}

	h.ctl.suspendErr = errors.New("no")
	if err := m.SuspendHotkeys(":1.9", true); err == nil || err.Name != ErrNotAvailable {
		t.Fatalf("refused suspend error = %v", err)
	}
}

func TestSuspendHotkeysWatchdogExpiry(t *testing.T) {
	h := ipcTestNewHarness(t, Status{})
	if err := h.s.suspendHotkeys(":1.7", true); err != nil {
		t.Fatal(err)
	}
	first := h.clock.last()
	if err := h.s.suspendHotkeys(":1.7", true); err != nil { // re-arm
		t.Fatal(err)
	}
	second := h.clock.last()
	if !first.stopped || second == first {
		t.Fatal("re-suspend did not restart the watchdog")
	}
	first.f() // a stale timer must not end the newer suspension
	if calls := h.ctl.suspendCalls(); len(calls) != 2 {
		t.Fatalf("stale timer released: %v", calls)
	}
	second.f()
	want := []ipcTestSuspendCall{{true, ":1.7"}, {true, ":1.7"}, {false, ":1.7"}}
	if got := h.ctl.suspendCalls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("controller calls = %v, want %v", got, want)
	}
	second.f() // firing twice is harmless
	if got := h.ctl.suspendCalls(); len(got) != 3 {
		t.Fatalf("double expiry: %v", got)
	}
}

func TestSuspendHotkeysReleasedWhenCallerVanishes(t *testing.T) {
	h := ipcTestNewHarness(t, Status{})
	if err := h.s.suspendHotkeys(":1.7", true); err != nil {
		t.Fatal(err)
	}
	h.s.nameVanished(":1.99") // unrelated name
	if got := h.ctl.suspendCalls(); len(got) != 1 {
		t.Fatalf("unrelated vanish released: %v", got)
	}
	sig := &dbus.Signal{
		Sender: dbusName,
		Name:   dbusName + ".NameOwnerChanged",
		Body:   []any{":1.7", ":1.7", ""},
	}
	name, vanished := parseNameOwnerChanged(sig)
	if name != ":1.7" || !vanished {
		t.Fatalf("parse = %q, %t", name, vanished)
	}
	h.s.nameVanished(name)
	want := []ipcTestSuspendCall{{true, ":1.7"}, {false, ":1.7"}}
	if got := h.ctl.suspendCalls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("controller calls = %v, want %v", got, want)
	}

	// A caller that is already gone when the watch is installed is released at once.
	h.names.absent[":1.8"] = true
	if err := h.s.suspendHotkeys(":1.8", true); err != nil {
		t.Fatal(err)
	}
	if got := h.ctl.suspendCalls(); got[len(got)-1] != (ipcTestSuspendCall{false, ":1.8"}) {
		t.Fatalf("absent caller not released: %v", got)
	}
}

func TestSuspendHotkeysTakeover(t *testing.T) {
	h := ipcTestNewHarness(t, Status{})
	if err := h.s.suspendHotkeys(":1.7", true); err != nil {
		t.Fatal(err)
	}
	if err := h.s.suspendHotkeys(":1.8", true); err != nil {
		t.Fatal(err)
	}
	if err := h.s.suspendHotkeys(":1.7", false); err != nil {
		t.Fatal(err)
	}
	if got := h.ctl.suspendCalls(); len(got) != 2 {
		t.Fatalf("previous owner ended the suspension: %v", got)
	}
	h.s.nameVanished(":1.7")
	if got := h.ctl.suspendCalls(); len(got) != 2 {
		t.Fatalf("previous owner vanish ended the suspension: %v", got)
	}
	if !reflect.DeepEqual(h.names.unwatched, []string{":1.7"}) {
		t.Fatalf("unwatched %v", h.names.unwatched)
	}
	h.s.Close()
	want := []ipcTestSuspendCall{{true, ":1.7"}, {true, ":1.8"}, {false, ":1.8"}}
	if got := h.ctl.suspendCalls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("controller calls = %v, want %v", got, want)
	}
}

func TestParseNameOwnerChanged(t *testing.T) {
	cases := []struct {
		sig      *dbus.Signal
		name     string
		vanished bool
	}{
		{nil, "", false},
		{&dbus.Signal{Sender: dbusName, Name: dbusName + ".NameOwnerChanged", Body: []any{BusName, "", ":1.4"}}, BusName, false},
		{&dbus.Signal{Sender: dbusName, Name: dbusName + ".NameOwnerChanged", Body: []any{BusName, ":1.4", ""}}, BusName, true},
		{&dbus.Signal{Sender: ":1.5", Name: dbusName + ".NameOwnerChanged", Body: []any{BusName, ":1.4", ""}}, "", false},
		{&dbus.Signal{Sender: dbusName, Name: dbusName + ".NameAcquired", Body: []any{":1.4"}}, "", false},
		{&dbus.Signal{Sender: dbusName, Name: dbusName + ".NameOwnerChanged", Body: []any{BusName, ":1.4"}}, "", false},
	}
	for i, tc := range cases {
		name, vanished := parseNameOwnerChanged(tc.sig)
		if name != tc.name || vanished != tc.vanished {
			t.Errorf("case %d: got %q,%t want %q,%t", i, name, vanished, tc.name, tc.vanished)
		}
	}
}

func TestServiceNilAndClosedAreSafe(t *testing.T) {
	var nilSvc *Service
	nilSvc.UpdateStatus(Status{})
	nilSvc.NotifyAutoEnabled(true, SourceChord)
	nilSvc.NotifyCorrected(Correction{})
	nilSvc.NotifyUndone("", false)
	nilSvc.Close()

	h := ipcTestNewHarness(t, Status{})
	h.s.startLoops()
	h.s.Close()
	h.s.Close()
	h.s.UpdateStatus(Status{ActiveLayout: "ru"})
	h.s.NotifyUndone("w", false)
	if len(h.s.signals) != 0 || h.s.pending != nil {
		t.Fatal("closed service queued work")
	}
	if err := (&methods{s: h.s}).SetAutoEnabled(true); err == nil || err.Name != ErrNotAvailable {
		t.Fatalf("closed SetAutoEnabled error = %v", err)
	}
}

func TestStartRefusesRootMode(t *testing.T) {
	orig := serviceGeteuid
	serviceGeteuid = func() int { return 0 }
	defer func() { serviceGeteuid = orig }()
	if _, err := Start(t.Context(), &ipcTestController{}, nil); !errors.Is(err, ErrRootMode) {
		t.Fatalf("Start as root error = %v", err)
	}
}
