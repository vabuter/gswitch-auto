package tray

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/arumata/gswitch/internal/gswitch/focus"
)

const focusExtTestWait = 3 * time.Second

const focusExtTestBusConfig = `<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:dir=%s</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
`

func TestFocusExtStateFromInfo(t *testing.T) {
	path := "/usr/share/gnome-shell/extensions/" + focus.ExtensionUUID
	info := func(state any, extra ...string) map[string]dbus.Variant {
		m := map[string]dbus.Variant{
			"uuid": dbus.MakeVariant(focus.ExtensionUUID),
			"path": dbus.MakeVariant(path),
		}
		if state != nil {
			m["state"] = dbus.MakeVariant(state)
		}
		if len(extra) > 0 {
			m["error"] = dbus.MakeVariant(extra[0])
		}
		return m
	}
	tests := []struct {
		name      string
		info      map[string]dbus.Variant
		dirExists bool
		want      FocusExtensionInfo
	}{
		{"unknown uuid with files", map[string]dbus.Variant{}, true, FocusExtensionInfo{State: FocusExtNeedsRelogin}},
		{"unknown uuid without files", map[string]dbus.Variant{}, false, FocusExtensionInfo{State: FocusExtNotInstalled}},
		{"nil info without files", nil, false, FocusExtensionInfo{State: FocusExtNotInstalled}},
		{"active", info(1.0), true, FocusExtensionInfo{State: FocusExtActive, Path: path}},
		{"inactive", info(2.0), true, FocusExtensionInfo{State: FocusExtDisabled, Path: path}},
		{"initialized", info(6.0), true, FocusExtensionInfo{State: FocusExtDisabled, Path: path}},
		{"deactivating", info(7.0), true, FocusExtensionInfo{State: FocusExtDisabled, Path: path}},
		{"activating", info(8.0), true, FocusExtensionInfo{State: FocusExtDisabled, Path: path}},
		{"error", info(3.0, "SyntaxError: boom"), true, FocusExtensionInfo{State: FocusExtError, Path: path, Errors: []string{"SyntaxError: boom"}}},
		{"error without message", info(3.0, ""), true, FocusExtensionInfo{State: FocusExtError, Path: path}},
		{"out of date", info(4.0), true, FocusExtensionInfo{State: FocusExtOutOfDate, Path: path}},
		{"uninstalled with files", info(99.0), true, FocusExtensionInfo{State: FocusExtNeedsRelogin, Path: path}},
		{"downloading without files", info(5.0), false, FocusExtensionInfo{State: FocusExtNotInstalled, Path: path}},
		{"integer state", info(int32(1)), true, FocusExtensionInfo{State: FocusExtActive, Path: path}},
		{"no state", info(nil), true, FocusExtensionInfo{State: FocusExtError, Path: path, Errors: []string{"extension info has no state"}}},
		{"unknown state", info(42.0), true, FocusExtensionInfo{State: FocusExtError, Path: path, Errors: []string{"unknown extension state 42"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stateFromInfo(tt.info, tt.dirExists); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("stateFromInfo() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestFocusExtAppendToGsettingsList(t *testing.T) {
	const uuid = focus.ExtensionUUID
	tests := []struct {
		cur, want string
		changed   bool
	}{
		{"['a', 'b']", "['a', 'b', '" + uuid + "']", true},
		{"@as []", "['" + uuid + "']", true},
		{"[]", "['" + uuid + "']", true},
		{"['a', '" + uuid + "', 'b']", "['a', '" + uuid + "', 'b']", false},
		{"[\"" + uuid + "\"]", "[\"" + uuid + "\"]", false},
		{"['it\\'s']", "['it\\'s', '" + uuid + "']", true},
		{"not a list", "not a list", false},
		{"['a', ]", "['a', ]", false},
	}
	for _, tt := range tests {
		got, changed := appendToGsettingsList(tt.cur, uuid)
		if got != tt.want || changed != tt.changed {
			t.Errorf("appendToGsettingsList(%q) = (%q, %v), want (%q, %v)", tt.cur, got, changed, tt.want, tt.changed)
		}
	}
}

func TestFocusExtRemoveFromGsettingsList(t *testing.T) {
	const uuid = focus.ExtensionUUID
	tests := []struct {
		cur, want string
		changed   bool
	}{
		{"['a', '" + uuid + "', 'b']", "['a', 'b']", true},
		{"['" + uuid + "', '" + uuid + "']", "@as []", true},
		{"['a', 'b']", "['a', 'b']", false},
		{"@as []", "@as []", false},
		{"garbage", "garbage", false},
	}
	for _, tt := range tests {
		got, changed := focusExtRemoveFromGsettingsList(tt.cur, uuid)
		if got != tt.want || changed != tt.changed {
			t.Errorf("focusExtRemoveFromGsettingsList(%q) = (%q, %v), want (%q, %v)", tt.cur, got, changed, tt.want, tt.changed)
		}
	}
}

func TestFocusExtParseStrv(t *testing.T) {
	good := map[string][]string{
		"@as []":                 {},
		"  []  ":                 {},
		"['a']":                  {"a"},
		"['a', \"b c\"]":         {"a", "b c"},
		"['x\\\\y', 'q\\'r']":    {`x\y`, "q'r"},
		"[ 'a' ,'b' ]":           {"a", "b"},
		"['сокет@example.org']":  {"сокет@example.org"},
		"['with, comma', 'end']": {"with, comma", "end"},
	}
	for in, want := range good {
		got, err := focusExtParseStrv(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("focusExtParseStrv(%q) = (%q, %v), want %q", in, got, err, want)
		}
		if err == nil {
			round, err := focusExtParseStrv(focusExtFormatStrv(got))
			if err != nil || !reflect.DeepEqual(round, want) {
				t.Errorf("round trip of %q = (%q, %v)", in, round, err)
			}
		}
	}
	for _, in := range []string{"", "a", "['a'", "['a' 'b']", "['a',]", "[a]", "['unterminated]", "@as"} {
		if got, err := focusExtParseStrv(in); err == nil {
			t.Errorf("focusExtParseStrv(%q) = %q, want an error", in, got)
		}
	}
}

func TestFocusExtPickerAppsMapsWindowIDs(t *testing.T) {
	in := []focus.App{
		{ID: "org.gnome.Ptyxis.desktop", Name: "Ptyxis", WmClass: "org.gnome.Ptyxis"},
		{ID: "window:7", Name: "Foo", WmClass: "Foo"},
		{ID: "window:8", Name: "Nameless", WmClass: ""},
		{ID: "", Name: "", WmClass: "bar"},
	}
	want := []focus.App{
		{ID: "org.gnome.Ptyxis.desktop", Name: "Ptyxis", WmClass: "org.gnome.Ptyxis"},
		{ID: "", Name: "Foo", WmClass: "Foo"},
		{ID: "", Name: "", WmClass: "bar"},
	}
	if got := focusExtPickerApps(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("focusExtPickerApps() = %+v, want %+v", got, want)
	}
}

func TestFocusExtStateString(t *testing.T) {
	for state, want := range map[FocusExtensionState]string{
		FocusExtNoShell:         "no-shell",
		FocusExtNotInstalled:    "not-installed",
		FocusExtNeedsRelogin:    "needs-relogin",
		FocusExtDisabled:        "disabled",
		FocusExtActive:          "active",
		FocusExtError:           "error",
		FocusExtOutOfDate:       "out-of-date",
		FocusExtensionState(42): "FocusExtensionState(42)",
	} {
		if got := state.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", int(state), got, want)
		}
	}
}

func TestFocusExtNoSharedSessionBus(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "focus_extension.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "dbus" &&
			(sel.Sel.Name == "SessionBus" || sel.Sel.Name == "ConnectSessionBus") {
			t.Errorf("focus_extension.go calls dbus.%s; it must use the caller's private connection (C27)", sel.Sel.Name)
		}
		return true
	})
}

// --- private bus fixtures ---

func focusExtTestBus(t *testing.T) string {
	t.Helper()
	daemon, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon not available")
	}
	dir, err := os.MkdirTemp("", "gsfocusext")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	config := filepath.Join(dir, "bus.conf")
	if err := os.WriteFile(config, fmt.Appendf(nil, focusExtTestBusConfig, dir), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(daemon, "--config-file="+config, "--nofork", "--print-address=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("start dbus-daemon: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	lines := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		lines <- strings.TrimSpace(line)
	}()
	select {
	case addr := <-lines:
		if addr == "" {
			t.Fatal("dbus-daemon printed no address")
		}
		return addr
	case <-time.After(focusExtTestWait):
		t.Fatal("dbus-daemon did not print its address")
	}
	return ""
}

func focusExtTestConnect(t *testing.T, addr string) *dbus.Conn {
	t.Helper()
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatalf("connect %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func focusExtTestOwn(t *testing.T, conn *dbus.Conn, name string) {
	t.Helper()
	reply, err := conn.RequestName(name, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("RequestName(%s) = %v, %v", name, reply, err)
	}
}

// focusExtTestShell fakes org.gnome.Shell's org.gnome.Shell.Extensions.
type focusExtTestShell struct {
	conn *dbus.Conn

	mu            sync.Mutex
	info          map[string]dbus.Variant
	errors        []string
	enableResult  bool
	disableResult bool
	calls         []string
}

func (s *focusExtTestShell) record(call string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
}

func (s *focusExtTestShell) GetExtensionInfo(uuid string) (map[string]dbus.Variant, *dbus.Error) {
	s.record("GetExtensionInfo " + uuid)
	s.mu.Lock()
	defer s.mu.Unlock()
	if uuid != focus.ExtensionUUID || s.info == nil {
		return map[string]dbus.Variant{}, nil
	}
	return s.info, nil
}

func (s *focusExtTestShell) GetExtensionErrors(uuid string) ([]string, *dbus.Error) {
	s.record("GetExtensionErrors " + uuid)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.errors, nil
}

func (s *focusExtTestShell) EnableExtension(uuid string) (bool, *dbus.Error) {
	s.record("EnableExtension " + uuid)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enableResult, nil
}

func (s *focusExtTestShell) DisableExtension(uuid string) (bool, *dbus.Error) {
	s.record("DisableExtension " + uuid)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.disableResult, nil
}

func (s *focusExtTestShell) set(change func(*focusExtTestShell)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change(s)
}

func (s *focusExtTestShell) setInfo(info map[string]dbus.Variant) {
	s.set(func(s *focusExtTestShell) { s.info = info })
}

func focusExtTestNewShell(t *testing.T, addr string) *focusExtTestShell {
	t.Helper()
	s := &focusExtTestShell{conn: focusExtTestConnect(t, addr)}
	if err := s.conn.Export(s, focusExtShellPath, focusExtShellIface); err != nil {
		t.Fatal(err)
	}
	focusExtTestOwn(t, s.conn, focusExtShellName)
	return s
}

func (s *focusExtTestShell) emitState(t *testing.T, uuid string, state float64) {
	t.Helper()
	body := map[string]dbus.Variant{"uuid": dbus.MakeVariant(uuid), "state": dbus.MakeVariant(state)}
	if err := s.conn.Emit(focusExtShellPath, focusExtStateSignal, uuid, body); err != nil {
		t.Fatal(err)
	}
}

// focusExtTestGsettings replaces gsettings for one test.
type focusExtTestGsettings struct {
	mu     sync.Mutex
	values map[string]string
	sets   []string
}

func focusExtTestFakeGsettings(t *testing.T, values map[string]string) *focusExtTestGsettings {
	t.Helper()
	g := &focusExtTestGsettings{values: values}
	orig := focusExtRunGsettings
	focusExtRunGsettings = g.run
	t.Cleanup(func() { focusExtRunGsettings = orig })
	return g
}

func (g *focusExtTestGsettings) run(_ context.Context, args ...string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(args) < 3 || args[1] != focusExtSchema {
		return "", fmt.Errorf("unexpected gsettings call %q", args)
	}
	switch {
	case args[0] == "get" && len(args) == 3:
		value, ok := g.values[args[2]]
		if !ok {
			return "", fmt.Errorf("no such key %s", args[2])
		}
		return value, nil
	case args[0] == "set" && len(args) == 4:
		g.values[args[2]] = args[3]
		g.sets = append(g.sets, args[2]+"="+args[3])
		return "", nil
	default:
		return "", fmt.Errorf("unexpected gsettings call %q", args)
	}
}

// focusExtTestDataDirs points the installed-copy lookup at temporary
// directories and returns the user data dir.
func focusExtTestDataDirs(t *testing.T) string {
	t.Helper()
	user, system := t.TempDir(), t.TempDir()
	orig := focusExtDataDirs
	focusExtDataDirs = func() []string { return []string{user, system} }
	t.Cleanup(func() { focusExtDataDirs = orig })
	return user
}

func focusExtTestInstall(t *testing.T, dataDir string) string {
	t.Helper()
	dir := filepath.Join(dataDir, "gnome-shell", "extensions", focus.ExtensionUUID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(`{"uuid":"`+focus.ExtensionUUID+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestFocusExtQuery(t *testing.T) {
	addr := focusExtTestBus(t)
	dataDir := focusExtTestDataDirs(t)
	conn := focusExtTestConnect(t, addr)
	ctx := context.Background()

	info, err := QueryFocusExtension(ctx, conn)
	if err != nil || info.State != FocusExtNoShell {
		t.Fatalf("without a Shell: (%+v, %v), want NoShell", info, err)
	}

	shell := focusExtTestNewShell(t, addr)
	info, err = QueryFocusExtension(ctx, conn)
	if err != nil || info.State != FocusExtNotInstalled {
		t.Fatalf("unknown and not installed: (%+v, %v), want NotInstalled", info, err)
	}

	dir := focusExtTestInstall(t, dataDir)
	info, err = QueryFocusExtension(ctx, conn)
	if err != nil || info.State != FocusExtNeedsRelogin || info.Path != dir {
		t.Fatalf("installed after login: (%+v, %v), want NeedsRelogin at %s", info, err, dir)
	}

	shell.setInfo(map[string]dbus.Variant{"state": dbus.MakeVariant(1.0), "path": dbus.MakeVariant("/loaded/from")})
	info, err = QueryFocusExtension(ctx, conn)
	if err != nil || info.State != FocusExtActive || info.Path != "/loaded/from" {
		t.Fatalf("active: (%+v, %v)", info, err)
	}

	shell.setInfo(map[string]dbus.Variant{"state": dbus.MakeVariant(3.0), "error": dbus.MakeVariant("short")})
	shell.set(func(s *focusExtTestShell) { s.errors = []string{"first error", "second error"} })
	info, err = QueryFocusExtension(ctx, conn)
	want := FocusExtensionInfo{State: FocusExtError, Path: dir, Errors: []string{"first error", "second error"}}
	if err != nil || !reflect.DeepEqual(info, want) {
		t.Fatalf("error state: (%+v, %v), want %+v", info, err, want)
	}

	if info, err := QueryFocusExtension(ctx, nil); err == nil || info.State != FocusExtError {
		t.Fatalf("nil conn: (%+v, %v), want an error", info, err)
	}
}

func TestFocusExtEnable(t *testing.T) {
	addr := focusExtTestBus(t)
	conn := focusExtTestConnect(t, addr)
	ctx := context.Background()
	uuid := focus.ExtensionUUID

	// No Shell on the bus: pre-enable through gsettings.
	g := focusExtTestFakeGsettings(t, map[string]string{
		focusExtEnabledKey:  "['a']",
		focusExtDisabledKey: "['" + uuid + "', 'b']",
	})
	relogin, err := EnableFocusExtension(ctx, conn)
	if err != nil || !relogin {
		t.Fatalf("without a Shell: (%v, %v), want (true, nil)", relogin, err)
	}
	wantSets := []string{
		focusExtEnabledKey + "=['a', '" + uuid + "']",
		focusExtDisabledKey + "=['b']",
	}
	if !reflect.DeepEqual(g.sets, wantSets) {
		t.Fatalf("gsettings writes = %q, want %q", g.sets, wantSets)
	}

	// The Shell does not know the UUID (installed after login): same
	// fallback, and nothing is written twice.
	shell := focusExtTestNewShell(t, addr)
	relogin, err = EnableFocusExtension(ctx, conn)
	if err != nil || !relogin || len(g.sets) != 2 {
		t.Fatalf("unknown to the Shell: (%v, %v), writes %q", relogin, err, g.sets)
	}

	// The Shell knows it: EnableExtension takes effect without relogin.
	shell.set(func(s *focusExtTestShell) { s.enableResult = true })
	relogin, err = EnableFocusExtension(ctx, conn)
	if err != nil || relogin || len(g.sets) != 2 {
		t.Fatalf("known to the Shell: (%v, %v), writes %q", relogin, err, g.sets)
	}

	// An unparsable gsettings value is an error, not a silent no-op.
	shell.set(func(s *focusExtTestShell) { s.enableResult = false })
	focusExtTestFakeGsettings(t, map[string]string{focusExtEnabledKey: "garbage", focusExtDisabledKey: "@as []"})
	if relogin, err := EnableFocusExtension(ctx, conn); err == nil || relogin {
		t.Fatalf("unparsable list: (%v, %v), want an error", relogin, err)
	}
}

func TestFocusExtDisable(t *testing.T) {
	addr := focusExtTestBus(t)
	conn := focusExtTestConnect(t, addr)
	ctx := context.Background()
	uuid := focus.ExtensionUUID

	shell := focusExtTestNewShell(t, addr)
	shell.set(func(s *focusExtTestShell) { s.disableResult = true })
	g := focusExtTestFakeGsettings(t, map[string]string{focusExtEnabledKey: "['a', '" + uuid + "']"})
	if err := DisableFocusExtension(ctx, conn); err != nil || len(g.sets) != 0 {
		t.Fatalf("known to the Shell: err %v, writes %q", err, g.sets)
	}

	shell.set(func(s *focusExtTestShell) { s.disableResult = false })
	if err := DisableFocusExtension(ctx, conn); err != nil {
		t.Fatalf("unknown to the Shell: %v", err)
	}
	if want := []string{focusExtEnabledKey + "=['a']"}; !reflect.DeepEqual(g.sets, want) {
		t.Fatalf("gsettings writes = %q, want %q", g.sets, want)
	}
}

func TestFocusExtWatch(t *testing.T) {
	addr := focusExtTestBus(t)
	focusExtTestDataDirs(t)
	conn := focusExtTestConnect(t, addr)
	shell := focusExtTestNewShell(t, addr)

	infos := make(chan FocusExtensionInfo, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	WatchFocusExtension(ctx, conn, func(info FocusExtensionInfo) { infos <- info })

	shell.emitState(t, "other@example.org", 1)
	shell.setInfo(map[string]dbus.Variant{"state": dbus.MakeVariant(1.0)})
	shell.emitState(t, focus.ExtensionUUID, 1)
	select {
	case info := <-infos:
		if info.State != FocusExtActive {
			t.Fatalf("watch delivered %+v, want Active", info)
		}
	case <-time.After(focusExtTestWait):
		t.Fatal("no callback for ExtensionStateChanged")
	}
	select {
	case info := <-infos:
		t.Fatalf("unexpected extra callback %+v (other UUID must be ignored)", info)
	case <-time.After(100 * time.Millisecond):
	}

	cancel()
	time.Sleep(100 * time.Millisecond)
	shell.emitState(t, focus.ExtensionUUID, 2)
	select {
	case info := <-infos:
		t.Fatalf("callback after the context was cancelled: %+v", info)
	case <-time.After(150 * time.Millisecond):
	}
}

// focusExtTestFocusExtension fakes the focus extension's ListRunningApps.
type focusExtTestFocusExtension struct{ apps []focus.App }

func (f *focusExtTestFocusExtension) ListRunningApps() ([]focus.App, *dbus.Error) {
	return f.apps, nil
}

func TestFocusExtListRunningAppsForPicker(t *testing.T) {
	addr := focusExtTestBus(t)
	conn := focusExtTestConnect(t, addr)
	ctx := context.Background()

	if _, err := ListRunningAppsForPicker(ctx, conn); !errors.Is(err, focus.ErrUnavailable) {
		t.Fatalf("without the extension: err = %v, want focus.ErrUnavailable", err)
	}

	ext := &focusExtTestFocusExtension{apps: []focus.App{
		{ID: "org.gnome.TextEditor.desktop", Name: "Text Editor", WmClass: "org.gnome.TextEditor"},
		{ID: "window:12", Name: "Foo", WmClass: "Foo"},
	}}
	extConn := focusExtTestConnect(t, addr)
	if err := extConn.Export(ext, focus.ObjectPath, focus.Interface); err != nil {
		t.Fatal(err)
	}
	focusExtTestOwn(t, extConn, focus.BusName)

	apps, err := ListRunningAppsForPicker(ctx, conn)
	if err != nil {
		t.Fatalf("ListRunningAppsForPicker: %v", err)
	}
	want := []focus.App{
		{ID: "org.gnome.TextEditor.desktop", Name: "Text Editor", WmClass: "org.gnome.TextEditor"},
		{ID: "", Name: "Foo", WmClass: "Foo"},
	}
	if !reflect.DeepEqual(apps, want) {
		t.Fatalf("apps = %+v, want %+v", apps, want)
	}
}
