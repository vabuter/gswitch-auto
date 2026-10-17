package layoutstate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const testMachineID = "9d97fcbd32ad45fda0488900d3e03fdd"

// addressTestEnv returns an AddressEnv rooted in a temp dir with a machine-id
// fixture and the given set of live pids.
func addressTestEnv(t *testing.T, live ...int) AddressEnv {
	t.Helper()
	dir := t.TempDir()
	midPath := filepath.Join(dir, "machine-id")
	if err := os.WriteFile(midPath, []byte(testMachineID+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	alive := map[int]bool{}
	for _, pid := range live {
		alive[pid] = true
	}
	return AddressEnv{
		WaylandDisplay: "wayland-0",
		ConfigDir:      filepath.Join(dir, "config"),
		MachineIDPaths: []string{midPath},
		Alive:          func(pid int) bool { return alive[pid] },
	}
}

func writeAddressFile(t *testing.T, path, addr string, pid int, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	content := "# This file is created by ibus-daemon, please do not modify it.\n" +
		"# IBUS_ADDRESS=unix:path=/commented/out\n" +
		"IBUS_ADDRESS=" + addr + "\n"
	if pid != 0 {
		content += fmt.Sprintf("IBUS_DAEMON_PID=%d\n", pid)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSocketPathDisplays(t *testing.T) {
	tests := []struct {
		name           string
		wayland, x11   string
		wantFileSuffix string
	}{
		{name: "wayland", wayland: "wayland-0", wantFileSuffix: "-unix-wayland-0"},
		{name: "wayland wins over DISPLAY", wayland: "wayland-1", x11: ":0", wantFileSuffix: "-unix-wayland-1"},
		{name: "local display", x11: ":0", wantFileSuffix: "-unix-0"},
		{name: "local display with screen", x11: ":0.0", wantFileSuffix: "-unix-0"},
		{name: "remote display", x11: "host:1.0", wantFileSuffix: "-host-1"},
		{name: "display without colon", x11: "foo", wantFileSuffix: "-foo-0"},
		{name: "no display", wantFileSuffix: "-unix-0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := addressTestEnv(t)
			env.WaylandDisplay = tt.wayland
			env.Display = tt.x11
			got, err := SocketPath(env)
			if err != nil {
				t.Fatalf("SocketPath() error = %v", err)
			}
			want := filepath.Join(env.ConfigDir, "ibus", "bus", testMachineID+tt.wantFileSuffix)
			if got != want {
				t.Fatalf("SocketPath() = %q, want %q", got, want)
			}
		})
	}
}

func TestSocketPathOverridesAndErrors(t *testing.T) {
	env := addressTestEnv(t)
	env.IBusAddressFile = "/run/custom/ibus-address"
	if got, err := SocketPath(env); err != nil || got != "/run/custom/ibus-address" {
		t.Fatalf("IBUS_ADDRESS_FILE: SocketPath() = %q, %v", got, err)
	}

	env = addressTestEnv(t)
	env.ConfigDir = ""
	if _, err := SocketPath(env); err == nil {
		t.Fatal("SocketPath() without config dir: error = nil")
	}
}

func TestAddressEnvFromLookup(t *testing.T) {
	vars := map[string]string{
		"HOME":              "/home/u",
		"XDG_CONFIG_HOME":   "/srv/cfg",
		"WAYLAND_DISPLAY":   "wayland-0",
		"DISPLAY":           ":0",
		"IBUS_ADDRESS":      "unix:path=/x",
		"IBUS_ADDRESS_FILE": "/y",
	}
	env := addressEnvFromLookup(func(k string) string { return vars[k] })
	if env.ConfigDir != "/srv/cfg" {
		t.Fatalf("XDG_CONFIG_HOME override: ConfigDir = %q", env.ConfigDir)
	}
	if env.WaylandDisplay != "wayland-0" || env.Display != ":0" || env.IBusAddress != "unix:path=/x" || env.IBusAddressFile != "/y" {
		t.Fatalf("env = %+v", env)
	}
	if len(env.MachineIDPaths) != 2 || env.MachineIDPaths[0] != "/var/lib/dbus/machine-id" || env.Alive == nil {
		t.Fatalf("defaults missing: %+v", env)
	}

	vars["XDG_CONFIG_HOME"] = "relative/cfg"
	if env := addressEnvFromLookup(func(k string) string { return vars[k] }); env.ConfigDir != "/home/u/.config" {
		t.Fatalf("relative XDG_CONFIG_HOME: ConfigDir = %q, want /home/u/.config", env.ConfigDir)
	}
	delete(vars, "XDG_CONFIG_HOME")
	delete(vars, "HOME")
	if env := addressEnvFromLookup(func(k string) string { return vars[k] }); env.ConfigDir != "" {
		t.Fatalf("no HOME: ConfigDir = %q, want empty", env.ConfigDir)
	}
}

func TestAddressEnvFromProcess(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("WAYLAND_DISPLAY", "wayland-7")
	t.Setenv("IBUS_ADDRESS", "")
	env := AddressEnvFromProcess()
	if env.ConfigDir != "/home/test/.config" || env.WaylandDisplay != "wayland-7" || env.IBusAddress != "" {
		t.Fatalf("AddressEnvFromProcess() = %+v", env)
	}
}

func TestMachineIDPrecedence(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "var-lib-dbus-machine-id")
	second := filepath.Join(dir, "etc-machine-id")
	if err := os.WriteFile(second, []byte("  second-id \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := machineID([]string{first, second}); got != "second-id" {
		t.Fatalf("missing first: machineID() = %q, want second-id", got)
	}
	if err := os.WriteFile(first, []byte("first-id\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := machineID([]string{first, second}); got != "first-id" {
		t.Fatalf("both present: machineID() = %q, want first-id", got)
	}
	if got := machineID([]string{filepath.Join(dir, "none")}); got != "machine-id" {
		t.Fatalf("none readable: machineID() = %q, want ibus fallback", got)
	}
}

func TestResolveAddressEnvWins(t *testing.T) {
	env := addressTestEnv(t)
	env.IBusAddress = "unix:path=/from/env"
	addr, pid, err := ResolveAddress(env)
	if err != nil || addr != "unix:path=/from/env" || pid != 0 {
		t.Fatalf("ResolveAddress() = %q, %d, %v", addr, pid, err)
	}
}

func TestResolveAddressParsesFileSkippingComments(t *testing.T) {
	env := addressTestEnv(t, 1363)
	path, err := SocketPath(env)
	if err != nil {
		t.Fatal(err)
	}
	writeAddressFile(t, path, "unix:path=/home/u/.cache/ibus/dbus-F5Nop0Xd,guid=1727ef", 1363, time.Time{})
	addr, pid, err := ResolveAddress(env)
	if err != nil {
		t.Fatalf("ResolveAddress() error = %v", err)
	}
	if addr != "unix:path=/home/u/.cache/ibus/dbus-F5Nop0Xd,guid=1727ef" || pid != 1363 {
		t.Fatalf("ResolveAddress() = %q, %d", addr, pid)
	}
}

func TestResolveAddressDeadPid(t *testing.T) {
	env := addressTestEnv(t) // nothing alive
	path, _ := SocketPath(env)
	writeAddressFile(t, path, "unix:path=/dead", 4242, time.Time{})
	if _, _, err := ResolveAddress(env); !errors.Is(err, ErrIBusNotRunning) {
		t.Fatalf("dead pid: err = %v, want ErrIBusNotRunning", err)
	}

	writeAddressFile(t, path, "unix:path=/nopid", 0, time.Time{})
	if _, _, err := ResolveAddress(env); !errors.Is(err, ErrIBusNotRunning) {
		t.Fatalf("missing pid: err = %v, want ErrIBusNotRunning", err)
	}
}

func TestResolveAddressMissingFile(t *testing.T) {
	env := addressTestEnv(t)
	_, _, err := ResolveAddress(env)
	if !errors.Is(err, ErrIBusNotRunning) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file: err = %v, want ErrIBusNotRunning wrapping ErrNotExist", err)
	}
}

func TestResolveAddressGlobFallbackNewestLive(t *testing.T) {
	env := addressTestEnv(t, 200, 300)
	dir := filepath.Join(env.ConfigDir, "ibus", "bus")
	base := time.Now().Add(-time.Hour)
	writeAddressFile(t, filepath.Join(dir, testMachineID+"-unix-0"), "unix:path=/oldest-live", 300, base)
	writeAddressFile(t, filepath.Join(dir, testMachineID+"-unix-1"), "unix:path=/middle-live", 200, base.Add(time.Minute))
	writeAddressFile(t, filepath.Join(dir, testMachineID+"-unix-2"), "unix:path=/newest-dead", 100, base.Add(2*time.Minute))

	addr, pid, err := ResolveAddress(env) // primary <mid>-unix-wayland-0 is missing
	if err != nil {
		t.Fatalf("ResolveAddress() error = %v", err)
	}
	if addr != "unix:path=/middle-live" || pid != 200 {
		t.Fatalf("ResolveAddress() = %q, %d; want newest live file", addr, pid)
	}

	// A dead primary file also falls back.
	primary, _ := SocketPath(env)
	writeAddressFile(t, primary, "unix:path=/primary-dead", 999, base.Add(3*time.Minute))
	if addr, _, err := ResolveAddress(env); err != nil || addr != "unix:path=/middle-live" {
		t.Fatalf("dead primary: ResolveAddress() = %q, %v", addr, err)
	}
}

func TestResolveAddressExplicitFileNoFallback(t *testing.T) {
	env := addressTestEnv(t, 200)
	dir := filepath.Join(env.ConfigDir, "ibus", "bus")
	writeAddressFile(t, filepath.Join(dir, testMachineID+"-unix-0"), "unix:path=/live", 200, time.Time{})
	env.IBusAddressFile = filepath.Join(dir, "explicit")
	writeAddressFile(t, env.IBusAddressFile, "unix:path=/explicit-dead", 100, time.Time{})
	if _, _, err := ResolveAddress(env); !errors.Is(err, ErrIBusNotRunning) {
		t.Fatalf("IBUS_ADDRESS_FILE dead: err = %v, want ErrIBusNotRunning without fallback", err)
	}
}

func TestProcessAlive(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Fatal("processAlive(self) = false")
	}
	if processAlive(0) || processAlive(-1) {
		t.Fatal("processAlive(<=0) = true")
	}
}
