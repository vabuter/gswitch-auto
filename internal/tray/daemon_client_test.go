package tray

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/arumata/gswitch/internal/gswitch/ipc"
	"github.com/arumata/gswitch/internal/gswitch/userconfig"
)

// gaTestNoGTK replaces scheduleGTK for the test; the returned channel
// receives one value per scheduled callback (the callbacks are dropped).
func gaTestNoGTK(t *testing.T) <-chan struct{} {
	t.Helper()
	scheduled := make(chan struct{}, 64)
	old := scheduleGTK
	scheduleGTK = func(func()) {
		select {
		case scheduled <- struct{}{}:
		default:
		}
	}
	t.Cleanup(func() { scheduleGTK = old })
	return scheduled
}

// gaTestFakeIPC implements autoDaemonIPC.
type gaTestFakeIPC struct {
	mu        sync.Mutex
	status    ipc.ClientStatus
	setErr    error
	setCalls  []bool
	reloads   int
	suspends  []bool
	started   bool
	stopped   bool
	callbacks ipc.ClientCallbacks
}

func (f *gaTestFakeIPC) Start() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = true
	return nil
}

func (f *gaTestFakeIPC) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
}

func (f *gaTestFakeIPC) Status() ipc.ClientStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func (f *gaTestFakeIPC) SetAutoEnabled(_ context.Context, enabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setCalls = append(f.setCalls, enabled)
	return f.setErr
}

func (f *gaTestFakeIPC) Reload(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reloads++
	return nil
}

func (f *gaTestFakeIPC) SuspendHotkeys(_ context.Context, suspend bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.suspends = append(f.suspends, suspend)
	return nil
}

// gaTestPersister builds a persister over dir whose writes are counted and
// whose passes are signalled on the returned channel.
type gaTestPersister struct {
	*daemonPersister
	mu         sync.Mutex
	autoWrites []bool
	wordWrites []string
	reports    []string
	passes     chan ipc.ClientStatus
}

func gaTestNewPersister(dir string) *gaTestPersister {
	tp := &gaTestPersister{passes: make(chan ipc.ClientStatus, 64)}
	p := newDaemonPersister(dir, func(message string) {
		tp.mu.Lock()
		tp.reports = append(tp.reports, message)
		tp.mu.Unlock()
	})
	p.setAutoEnabled = func(dir string, enabled bool) error {
		tp.mu.Lock()
		tp.autoWrites = append(tp.autoWrites, enabled)
		tp.mu.Unlock()
		return userconfig.SetAutoEnabled(dir, enabled)
	}
	p.appendWord = func(dir, name, entry string) (bool, error) {
		tp.mu.Lock()
		tp.wordWrites = append(tp.wordWrites, entry)
		tp.mu.Unlock()
		return userconfig.AppendListEntry(dir, name, entry)
	}
	p.afterPass = func(st ipc.ClientStatus) {
		select {
		case tp.passes <- st:
		default: // retries beyond the buffer are not awaited
		}
	}
	tp.daemonPersister = p
	return tp
}

func (tp *gaTestPersister) waitPass(t *testing.T) ipc.ClientStatus {
	t.Helper()
	select {
	case st := <-tp.passes:
		return st
	case <-time.After(5 * time.Second):
		t.Fatal("persister did not process a status")
		return ipc.ClientStatus{}
	}
}

func (tp *gaTestPersister) counts() (auto []bool, words []string, reports []string) {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return slices.Clone(tp.autoWrites), slices.Clone(tp.wordWrites), slices.Clone(tp.reports)
}

func gaTestUnsaved(enabled bool) ipc.ClientStatus {
	return ipc.ClientStatus{Connected: true, Status: ipc.Status{AutoEnabled: enabled, AutoEnabledUnsaved: true}}
}

func gaTestLoad(t *testing.T, dir string) *userconfig.Config {
	t.Helper()
	cfg, err := userconfig.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestDaemonPersisterWritesLatestAutoEnabled(t *testing.T) {
	dir := t.TempDir()
	tp := gaTestNewPersister(dir)
	// Two rapid statuses before the goroutine runs: only the last counts.
	tp.offer(gaTestUnsaved(true))
	tp.offer(gaTestUnsaved(false))
	tp.start()
	defer tp.stop()
	if st := tp.waitPass(t); st.AutoEnabled {
		t.Fatal("persister processed a stale status")
	}
	auto, _, _ := tp.counts()
	if !slices.Equal(auto, []bool{false}) {
		t.Fatalf("auto-enabled writes = %v, want [false]", auto)
	}
	if gaTestLoad(t, dir).Settings.AutoEnabled {
		t.Fatal("auto.conf does not hold the latest value")
	}
	data, err := os.ReadFile(filepath.Join(dir, userconfig.AutoConfName))
	if err != nil || !strings.Contains(string(data), "auto-enabled=false") {
		t.Fatalf("auto.conf = %q, %v", data, err)
	}

	// The same unsaved status again within the skip window: no rewrite.
	tp.offer(gaTestUnsaved(false))
	tp.waitPass(t)
	if auto, _, _ := tp.counts(); len(auto) != 1 {
		t.Fatalf("repeated status rewrote auto-enabled: %v", auto)
	}
	// A new value is written at once.
	tp.offer(gaTestUnsaved(true))
	tp.waitPass(t)
	if auto, _, _ := tp.counts(); !slices.Equal(auto, []bool{false, true}) {
		t.Fatalf("auto-enabled writes = %v, want [false true]", auto)
	}
	if !gaTestLoad(t, dir).Settings.AutoEnabled {
		t.Fatal("auto.conf did not follow the new value")
	}
}

func TestDaemonPersisterAppendsPendingWordsOnce(t *testing.T) {
	dir := t.TempDir()
	tp := gaTestNewPersister(dir)
	tp.start()
	defer tp.stop()
	st := ipc.ClientStatus{Connected: true, Status: ipc.Status{PendingLearnedWords: []string{"ghbdtn", "rfr"}}}
	tp.offer(st)
	tp.waitPass(t)
	tp.offer(st)
	tp.waitPass(t)
	_, words, _ := tp.counts()
	if !slices.Equal(words, []string{"ghbdtn", "rfr"}) {
		t.Fatalf("word writes = %v, want each once", words)
	}
	cfg := gaTestLoad(t, dir)
	if !cfg.Words.Contains("ghbdtn") || !cfg.Words.Contains("rfr") {
		t.Fatalf("words not persisted: %v", cfg.Words.Entries())
	}
	text, err := userconfig.ReadListText(dir, userconfig.WordsName)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count("\n"+text, "\nghbdtn\n") != 1 {
		t.Fatalf("word appended twice:\n%s", text)
	}
	// Retrying after the skip window is idempotent on disk.
	tp.skipWindow = 0
	tp.offer(st)
	tp.waitPass(t)
	if text2, _ := userconfig.ReadListText(dir, userconfig.WordsName); text2 != text {
		t.Fatalf("idempotent retry changed the file:\n%s", text2)
	}
}

func TestDaemonPersisterIgnoresSavedOrDisconnectedState(t *testing.T) {
	dir := t.TempDir()
	tp := gaTestNewPersister(dir)
	tp.start()
	defer tp.stop()
	for _, st := range []ipc.ClientStatus{
		{Connected: true, Status: ipc.Status{AutoEnabled: true}},
		{Status: ipc.Status{AutoEnabled: true, AutoEnabledUnsaved: true, PendingLearnedWords: []string{"x"}}},
	} {
		tp.offer(st)
		tp.waitPass(t)
	}
	auto, words, _ := tp.counts()
	if len(auto) != 0 || len(words) != 0 {
		t.Fatalf("unexpected writes: auto=%v words=%v", auto, words)
	}
	if _, err := os.Stat(filepath.Join(dir, userconfig.AutoConfName)); !os.IsNotExist(err) {
		t.Fatalf("auto.conf written: %v", err)
	}
}

func TestDaemonPersisterReportsFailureAndRetries(t *testing.T) {
	gaTestUseLanguage(t, "en")
	dir := t.TempDir()
	tp := gaTestNewPersister(dir)
	tp.retryDelay = 20 * time.Millisecond
	tp.skipWindow = 0
	failures := 1
	tp.setAutoEnabled = func(dir string, enabled bool) error {
		tp.mu.Lock()
		tp.autoWrites = append(tp.autoWrites, enabled)
		fail := failures > 0
		failures--
		tp.mu.Unlock()
		if fail {
			return errors.New("read-only file system")
		}
		return userconfig.SetAutoEnabled(dir, enabled)
	}
	tp.start()
	defer tp.stop()
	tp.offer(gaTestUnsaved(true))
	tp.waitPass(t) // fails
	tp.waitPass(t) // retry succeeds
	auto, _, reports := tp.counts()
	if len(auto) < 2 {
		t.Fatalf("no retry after failure: %v", auto)
	}
	if len(reports) < 2 {
		t.Fatalf("reports = %q, want failure then recovery", reports)
	}
	prefix, _, _ := strings.Cut(tr(strGAErrorPersist), "%s")
	if !strings.HasPrefix(reports[0], prefix) || !strings.Contains(reports[0], "read-only file system") {
		t.Fatalf("failure report = %q", reports[0])
	}
	if reports[1] != "" {
		t.Fatalf("recovery report = %q, want empty", reports[1])
	}
	if !gaTestLoad(t, dir).Settings.AutoEnabled {
		t.Fatal("retry did not persist the value")
	}
}

func TestToggleAutoOfflineWritesAutoEnabled(t *testing.T) {
	gaTestNoGTK(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	app := &App{userDir: dir}
	app.ToggleAuto()
	if !gaTestLoad(t, dir).Settings.AutoEnabled {
		t.Fatal("offline toggle did not enable auto correction in auto.conf")
	}
	if app.userConfig == nil || !app.userConfig.Settings.AutoEnabled {
		t.Fatal("offline toggle did not refresh the cached configuration")
	}
	app.ToggleAuto()
	if gaTestLoad(t, dir).Settings.AutoEnabled {
		t.Fatal("second offline toggle did not disable auto correction")
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode = %o, want 0700", info.Mode().Perm())
	}
}

func TestToggleAutoConnectedUsesDaemon(t *testing.T) {
	gaTestNoGTK(t)
	dir := t.TempDir()
	fake := &gaTestFakeIPC{status: ipc.ClientStatus{Connected: true, Status: ipc.Status{AutoEnabled: false}}}
	app := &App{userDir: dir, daemon: &daemonClient{client: fake}}
	app.ToggleAuto()
	if !slices.Equal(fake.setCalls, []bool{true}) {
		t.Fatalf("daemon calls = %v, want [true]", fake.setCalls)
	}
	if _, err := os.Stat(filepath.Join(dir, userconfig.AutoConfName)); !os.IsNotExist(err) {
		t.Fatal("connected toggle wrote auto.conf itself")
	}

	// The daemon vanished between Status and the call: fall back to the file.
	fake.setErr = ipc.ErrNotConnected
	if err := app.SetAutoEnabled(true); err != nil {
		t.Fatal(err)
	}
	if !gaTestLoad(t, dir).Settings.AutoEnabled {
		t.Fatal("fallback did not write auto.conf")
	}
	// Other daemon errors are reported, not hidden by a file write.
	fake.setErr = errors.New("refused")
	if err := app.SetAutoEnabled(false); err == nil {
		t.Fatal("daemon error swallowed")
	}
	if !gaTestLoad(t, dir).Settings.AutoEnabled {
		t.Fatal("refused toggle changed auto.conf")
	}
}

func TestEffectiveAutoState(t *testing.T) {
	cfg := &userconfig.Config{Settings: userconfig.DefaultSettings()}
	cfg.Settings.AutoEnabled = true
	cfg.Settings.ToggleChord = userconfig.Chord{29, 54}
	got := effectiveAutoState(ipc.ClientStatus{}, cfg)
	if got.Connected || !got.AutoEnabled || got.ToggleChord != "29+54" {
		t.Fatalf("offline state = %+v", got)
	}
	if got := effectiveAutoState(ipc.ClientStatus{}, nil); got.ToggleChord != "42+54" || got.AutoEnabled {
		t.Fatalf("offline state without config = %+v", got)
	}
	live := gaTestStatus(false, true)
	if got := effectiveAutoState(live, cfg); got.AutoEnabled || !got.Connected {
		t.Fatalf("connected state not passed through: %+v", got)
	}
}

func TestHotkeySuspenderAppliesLatestRequest(t *testing.T) {
	var mu sync.Mutex
	var calls []bool
	called := make(chan struct{}, 8)
	h := newHotkeySuspender(func(_ context.Context, suspend bool) error {
		mu.Lock()
		calls = append(calls, suspend)
		mu.Unlock()
		called <- struct{}{}
		return nil
	})
	// Requests that cancel out before the worker runs cause no call.
	h.request(true)
	h.request(false)
	h.start()
	defer h.stop()
	h.request(true)
	<-called
	h.request(false)
	<-called
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(calls, []bool{true, false}) {
		t.Fatalf("suspend calls = %v, want [true false]", calls)
	}
}

func TestStartForkPreparesUserDirAndStops(t *testing.T) {
	gaTestNoGTK(t)
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	dir := filepath.Join(home, userconfig.DirName)
	if err := os.MkdirAll(dir, 0o750); err != nil { // as upstream's tray creates it
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	oldConnect, oldIPC := connectForkSessionBus, newAutoDaemonIPC
	t.Cleanup(func() { connectForkSessionBus, newAutoDaemonIPC = oldConnect, oldIPC })
	connectForkSessionBus = func() (*dbus.Conn, error) { return nil, errors.New("no bus in tests") }
	fake := &gaTestFakeIPC{}
	newAutoDaemonIPC = func(cb ipc.ClientCallbacks) autoDaemonIPC {
		fake.callbacks = cb
		return fake
	}

	app := &App{}
	app.startFork()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("user dir mode = %o, want 0700", info.Mode().Perm())
	}
	for _, name := range []string{userconfig.AutoConfName, userconfig.WordsName, userconfig.AppsName} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s not seeded: %v", name, err)
		}
	}
	if app.UserConfigDir() != dir || !fake.started {
		t.Fatalf("fork not started: dir=%q started=%v", app.UserConfigDir(), fake.started)
	}
	if keyPickerSuspend.Load() == nil {
		t.Fatal("key picker suspension hook not installed")
	}
	// Status callbacks reach the app state.
	fake.callbacks.OnStatus(gaTestStatus(true, true))
	if st := app.daemonStatus(); !st.Connected || !st.AutoEnabled {
		t.Fatalf("daemon status not recorded: %+v", st)
	}

	app.stopFork()
	if !fake.stopped {
		t.Fatal("daemon client not stopped")
	}
	if keyPickerSuspend.Load() != nil {
		t.Fatal("key picker suspension hook left installed")
	}
}

// TestForkTrayFilesAvoidSharedSessionBus enforces C27: the fork's tray files
// never call dbus.SessionBus() (fyne systray shares that connection).
func TestForkTrayFilesAvoidSharedSessionBus(t *testing.T) {
	files := []string{
		"daemon_client.go", "focus_extension.go", "settings_auto.go",
		"settings_auto_lists.go", "settings_hotkeys.go", "tray_auto.go",
	}
	for _, name := range files {
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == "dbus" && selector.Sel.Name == "SessionBus" {
				t.Errorf("%s calls dbus.SessionBus()", name)
			}
			return true
		})
	}
}
