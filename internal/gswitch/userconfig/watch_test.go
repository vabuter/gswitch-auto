package userconfig

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ucTestWatcher runs Watch in the background and collects callbacks.
type ucTestWatcher struct {
	changes chan *Config
	cancel  context.CancelFunc
	done    chan error
}

// ucTestStartWatch starts Watch and returns once its baseline is loaded.
func ucTestStartWatch(t *testing.T, dir string, opts Options) *ucTestWatcher {
	t.Helper()
	ready := make(chan struct{})
	testHookWatchReady = func() { close(ready) }
	ctx, cancel := context.WithCancel(context.Background())
	w := &ucTestWatcher{changes: make(chan *Config, 16), cancel: cancel, done: make(chan error, 1)}
	go func() {
		w.done <- Watch(ctx, dir, func(c *Config) { w.changes <- c }, opts)
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not start")
	}
	testHookWatchReady = nil
	t.Cleanup(w.stop)
	return w
}

func (w *ucTestWatcher) stop() {
	w.cancel()
	<-w.done
}

// next waits for a callback.
func (w *ucTestWatcher) next(t *testing.T, timeout time.Duration) *Config {
	t.Helper()
	select {
	case c := <-w.changes:
		return c
	case <-time.After(timeout):
		t.Fatalf("no callback within %v", timeout)
		return nil
	}
}

// none asserts that no callback arrives within d.
func (w *ucTestWatcher) none(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case c := <-w.changes:
		t.Fatalf("unexpected callback: %v words %v warnings %q", c.Settings.Values(), c.Words.Entries(), c.Warnings)
	case <-time.After(d):
	}
}

var ucTestFastWatch = Options{Debounce: 50 * time.Millisecond, PollInterval: 50 * time.Millisecond}

func TestWatchRenameTriggersCallback(t *testing.T) {
	dir := ucTestDir(t)
	if err := EnsureFiles(dir); err != nil {
		t.Fatal(err)
	}
	w := ucTestStartWatch(t, dir, Options{}) // default 200 ms debounce
	start := time.Now()
	if err := SetAutoEnabled(dir, true); err != nil { // temp file + rename
		t.Fatal(err)
	}
	c := w.next(t, time.Second)
	if !c.Settings.AutoEnabled {
		t.Fatal("callback config does not reflect the change")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("callback after %v", d)
	}
	w.none(t, 400*time.Millisecond) // one callback per change
}

func TestWatchHandEditInPlace(t *testing.T) {
	dir := ucTestDir(t)
	ucTestWrite(t, dir, WordsName, "ghbdtn\n")
	w := ucTestStartWatch(t, dir, ucTestFastWatch)
	ucTestWrite(t, dir, WordsName, "ghbdtn\nrfr\n") // truncate + write, IN_CLOSE_WRITE
	if c := w.next(t, time.Second); !c.Words.Contains("rfr") {
		t.Fatalf("words = %q", c.Words.Entries())
	}
}

func TestWatchNoInitialCallbackAndIdenticalRewrite(t *testing.T) {
	dir := ucTestDir(t)
	if err := SetAutoEnabled(dir, true); err != nil {
		t.Fatal(err)
	}
	if err := WriteListText(dir, WordsName, "ghbdtn\n"); err != nil {
		t.Fatal(err)
	}
	w := ucTestStartWatch(t, dir, ucTestFastWatch)
	w.none(t, 300*time.Millisecond)

	// Identical content written atomically: events fire, the config is
	// equal, so no callback.
	if err := WriteListText(dir, WordsName, "ghbdtn\n"); err != nil {
		t.Fatal(err)
	}
	ucTestWrite(t, dir, AutoConfName, ucTestRead(t, dir, AutoConfName))
	// Unrelated files in the directory are ignored.
	ucTestWrite(t, dir, "ui.conf", "x=1\n")
	w.none(t, 400*time.Millisecond)
}

// TestWatchBaselineCatchesEarlierChange: a change made between the caller's
// Load and the start of the watch (the tray seeding exceptions-apps.txt while
// the daemon starts) is reported once the watch is installed.
func TestWatchBaselineCatchesEarlierChange(t *testing.T) {
	dir := ucTestDir(t)
	ucTestWrite(t, dir, AutoConfName, "auto-enabled=false\n")
	baseline, _ := Load(dir)
	if baseline.Apps.Len() != 0 {
		t.Fatalf("baseline apps = %q", baseline.Apps.Entries())
	}
	if err := EnsureFiles(dir); err != nil { // seeds exceptions-apps.txt
		t.Fatal(err)
	}
	opts := ucTestFastWatch
	opts.Baseline = baseline
	w := ucTestStartWatch(t, dir, opts)
	if c := w.next(t, time.Second); !c.Apps.MatchApp("org.keepassxc.keepassxc", "") {
		t.Fatalf("callback apps = %q, want the seeded list", c.Apps.Entries())
	}
	w.none(t, 300*time.Millisecond)

	// A baseline equal to the directory fires nothing.
	current, _ := Load(dir)
	opts.Baseline = current
	ucTestStartWatch(t, dir, opts).none(t, 300*time.Millisecond)
}

func TestWatchDirMissingAtStart(t *testing.T) {
	dir := ucTestDir(t)
	w := ucTestStartWatch(t, dir, ucTestFastWatch)
	w.none(t, 150*time.Millisecond)
	if err := SetAutoEnabled(dir, true); err != nil { // creates the directory
		t.Fatal(err)
	}
	if c := w.next(t, 2*time.Second); !c.Settings.AutoEnabled {
		t.Fatal("callback config does not reflect the new directory")
	}
	// Now watched with inotify: a further change is seen.
	if _, err := AppendListEntry(dir, WordsName, "ghbdtn"); err != nil {
		t.Fatal(err)
	}
	if c := w.next(t, time.Second); !c.Words.Contains("ghbdtn") {
		t.Fatal("change after the directory appeared was missed")
	}
}

func TestWatchDirRemovedAndRecreated(t *testing.T) {
	dir := ucTestDir(t)
	if err := SetAutoEnabled(dir, true); err != nil {
		t.Fatal(err)
	}
	w := ucTestStartWatch(t, dir, ucTestFastWatch)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if c := w.next(t, 2*time.Second); c.Settings.AutoEnabled || len(c.Warnings) != 0 {
		t.Fatalf("after removal: AutoEnabled=%v warnings %q", c.Settings.AutoEnabled, c.Warnings)
	}
	if _, err := AppendListEntry(dir, WordsName, "rfr"); err != nil { // recreates the directory
		t.Fatal(err)
	}
	if c := w.next(t, 2*time.Second); !c.Words.Contains("rfr") {
		t.Fatal("recreated directory not picked up")
	}
	if err := SetAutoEnabled(dir, true); err != nil {
		t.Fatal(err)
	}
	if c := w.next(t, time.Second); !c.Settings.AutoEnabled {
		t.Fatal("recreated directory is not watched")
	}
}

func TestWatchDirReplacedThroughParentRename(t *testing.T) {
	old := stampRecheckInterval
	stampRecheckInterval = 100 * time.Millisecond
	t.Cleanup(func() { stampRecheckInterval = old })

	parent := filepath.Join(t.TempDir(), "config")
	dir := filepath.Join(parent, DirName)
	if err := SetAutoEnabled(dir, false); err != nil {
		t.Fatal(err)
	}
	w := ucTestStartWatch(t, dir, ucTestFastWatch)
	// Renaming the parent moves the watched inode without an event on it.
	if err := os.Rename(parent, parent+".old"); err != nil {
		t.Fatal(err)
	}
	if err := SetAutoEnabled(dir, true); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case c := <-w.changes:
			if c.Settings.AutoEnabled {
				return
			}
		case <-deadline:
			t.Fatal("replaced directory not picked up")
		}
	}
}

func TestWatchForcePoll(t *testing.T) {
	dir := ucTestDir(t)
	if err := EnsureFiles(dir); err != nil {
		t.Fatal(err)
	}
	w := ucTestStartWatch(t, dir, Options{ForcePoll: true, PollInterval: 50 * time.Millisecond})
	w.none(t, 200*time.Millisecond)
	if err := SetAutoEnabled(dir, true); err != nil {
		t.Fatal(err)
	}
	if c := w.next(t, time.Second); !c.Settings.AutoEnabled {
		t.Fatal("polling missed the change")
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if c := w.next(t, time.Second); c.Settings.AutoEnabled {
		t.Fatal("polling missed the removal")
	}
	if err := SetAutoEnabled(dir, true); err != nil {
		t.Fatal(err)
	}
	if c := w.next(t, time.Second); !c.Settings.AutoEnabled {
		t.Fatal("polling missed the recreated directory")
	}
}

func TestWatchUnreadableFileReported(t *testing.T) {
	ucTestSkipIfRoot(t)
	dir := ucTestDir(t)
	if err := SetAutoEnabled(dir, true); err != nil {
		t.Fatal(err)
	}
	w := ucTestStartWatch(t, dir, ucTestFastWatch)
	if err := os.Chmod(filepath.Join(dir, AutoConfName), 0); err != nil { // IN_ATTRIB
		t.Fatal(err)
	}
	c := w.next(t, time.Second)
	if c.Settings.AutoEnabled || !ucTestHasWarning(c.Warnings, "automatic correction disabled") {
		t.Fatalf("AutoEnabled=%v warnings %q", c.Settings.AutoEnabled, c.Warnings)
	}
}

func TestWatchCancelReturnsNil(t *testing.T) {
	for _, opts := range []Options{{}, {ForcePoll: true}} {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- Watch(ctx, ucTestDir(t), func(*Config) {}, opts) }()
		time.Sleep(50 * time.Millisecond)
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Watch returned %v after cancel", err)
			}
		case <-time.After(time.Second):
			t.Fatal("Watch did not return after cancel")
		}
	}
}

func TestWatchInvalidArguments(t *testing.T) {
	ctx := context.Background()
	cb := func(*Config) {}
	dir := ucTestDir(t)
	cases := map[string]func() error{
		"nil context":       func() error { return Watch(nil, dir, cb, Options{}) }, //nolint:staticcheck // tested on purpose
		"nil callback":      func() error { return Watch(ctx, dir, nil, Options{}) },
		"empty dir":         func() error { return Watch(ctx, "", cb, Options{}) },
		"relative dir":      func() error { return Watch(ctx, "rel/gswitch", cb, Options{}) },
		"negative debounce": func() error { return Watch(ctx, dir, cb, Options{Debounce: -1}) },
		"negative poll":     func() error { return Watch(ctx, dir, cb, Options{PollInterval: -1}) },
	}
	for name, call := range cases {
		done := make(chan error, 1)
		go func() { done <- call() }()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("%s: Watch returned nil", name)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s: Watch blocked instead of rejecting its arguments", name)
		}
	}
}

func TestParseInotifyEvents(t *testing.T) {
	event := func(mask uint32, name string) []byte {
		nameLen := 0
		if name != "" {
			nameLen = (len(name) + 1 + 15) / 16 * 16
		}
		buf := make([]byte, 16+nameLen)
		binary.NativeEndian.PutUint32(buf[4:], mask)
		binary.NativeEndian.PutUint32(buf[12:], uint32(nameLen))
		copy(buf[16:], name)
		return buf
	}
	tests := []struct {
		name           string
		buf            []byte
		relevant, gone bool
	}{
		{"auto.conf", event(0x8, AutoConfName), true, false},
		{"temp file", event(0x8, ".auto.conf.123.tmp"), false, false},
		{"lock", event(0x100, lockName), false, false},
		{"dir attrib", event(0x4, ""), true, false},
		{"delete self", event(0x400, ""), false, true},
		{"overflow", event(0x4000, ""), true, false},
		{"two events", append(event(0x100, "x"), event(0x80, AppsName)...), true, false},
	}
	for _, tt := range tests {
		relevant, gone := parseInotifyEvents(tt.buf)
		if relevant != tt.relevant || gone != tt.gone {
			t.Errorf("%s: relevant=%v gone=%v, want %v %v", tt.name, relevant, gone, tt.relevant, tt.gone)
		}
	}
}
