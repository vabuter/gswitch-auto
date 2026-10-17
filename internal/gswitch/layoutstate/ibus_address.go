package layoutstate

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ErrIBusNotRunning reports that no live ibus-daemon was found: the address
// file is missing, has no usable address, or names a dead daemon pid.
var ErrIBusNotRunning = errors.New("ibus-daemon is not running")

// defaultMachineIDPaths are the machine-id locations in ibus_get_local_machine_id order.
var defaultMachineIDPaths = []string{"/var/lib/dbus/machine-id", "/etc/machine-id"}

// AddressEnv is the process environment that determines the IBus address
// (port of ibus_get_socket_path / ibus_get_address, ibusshare.c).
type AddressEnv struct {
	IBusAddress, IBusAddressFile, WaylandDisplay, Display, ConfigDir string
	MachineIDPaths                                                   []string // {"/var/lib/dbus/machine-id", "/etc/machine-id"}
	Alive                                                            func(pid int) bool
}

// AddressEnvFromProcess reads IBUS_ADDRESS, IBUS_ADDRESS_FILE, WAYLAND_DISPLAY,
// DISPLAY and the user config directory (XDG_CONFIG_HOME when absolute, else
// $HOME/.config) from the current process.
func AddressEnvFromProcess() AddressEnv {
	return addressEnvFromLookup(os.Getenv)
}

func addressEnvFromLookup(getenv func(string) string) AddressEnv {
	configDir := getenv("XDG_CONFIG_HOME")
	if configDir == "" || !filepath.IsAbs(configDir) {
		configDir = ""
		if home := getenv("HOME"); home != "" {
			configDir = filepath.Join(home, ".config")
		}
	}
	return AddressEnv{
		IBusAddress:     getenv("IBUS_ADDRESS"),
		IBusAddressFile: getenv("IBUS_ADDRESS_FILE"),
		WaylandDisplay:  getenv("WAYLAND_DISPLAY"),
		Display:         getenv("DISPLAY"),
		ConfigDir:       configDir,
		MachineIDPaths:  slices.Clone(defaultMachineIDPaths),
		Alive:           processAlive,
	}
}

// processAlive mirrors ibus_get_address: the daemon counts as running when
// kill(pid, 0) succeeds.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// SocketPath returns the IBus address file path:
// IBUS_ADDRESS_FILE when set, else <ConfigDir>/ibus/bus/<machine-id>-<host>-<display>.
// WAYLAND_DISPLAY wins over DISPLAY and is used verbatim with host "unix";
// DISPLAY "host:N.S" yields "host-N" (empty host ⇒ "unix"); no display ⇒ "unix-0".
func SocketPath(e AddressEnv) (string, error) {
	if e.IBusAddressFile != "" {
		return e.IBusAddressFile, nil
	}
	if e.ConfigDir == "" {
		return "", errors.New("IBus address file: no user config directory (HOME unset)")
	}
	host, number := "unix", "0"
	switch {
	case e.WaylandDisplay != "":
		number = e.WaylandDisplay
	case e.Display != "":
		name, rest, hasColon := strings.Cut(e.Display, ":")
		host = name
		if hasColon {
			number, _, _ = strings.Cut(rest, ".")
		}
		if host == "" {
			host = "unix"
		}
	}
	name := fmt.Sprintf("%s-%s-%s", machineID(e.MachineIDPaths), host, number)
	return filepath.Join(e.ConfigDir, "ibus", "bus", name), nil
}

// machineID returns the first non-empty machine-id file content, or the
// literal "machine-id" as ibus does when none can be read.
func machineID(paths []string) string {
	if paths == nil {
		paths = defaultMachineIDPaths
	}
	for _, path := range paths {
		data, err := os.ReadFile(path) //nolint:gosec // fixed system paths or test fixtures
		if err != nil {
			continue
		}
		if id := strings.TrimSpace(string(data)); id != "" {
			return id
		}
	}
	return "machine-id"
}

// ResolveAddress returns the IBus bus address and daemon pid. IBUS_ADDRESS wins
// (pid 0). Otherwise the address file (SocketPath) is parsed for IBUS_ADDRESS=
// and IBUS_DAEMON_PID=; a dead or missing pid makes the file unusable. When the
// computed file is unusable and IBUS_ADDRESS_FILE is not set, the newest file
// in <ConfigDir>/ibus/bus whose daemon is alive is used. No usable file ⇒
// ErrIBusNotRunning.
func ResolveAddress(e AddressEnv) (addr string, pid int, err error) {
	if e.IBusAddress != "" {
		return e.IBusAddress, 0, nil
	}
	path, err := SocketPath(e)
	if err != nil {
		return "", 0, err
	}
	alive := e.Alive
	if alive == nil {
		alive = processAlive
	}

	addr, pid, primaryErr := readLiveAddressFile(path, alive)
	if primaryErr == nil {
		return addr, pid, nil
	}
	if e.IBusAddressFile != "" {
		return "", 0, primaryErr
	}
	for _, candidate := range addressFilesNewestFirst(filepath.Dir(path)) {
		if candidate == path {
			continue
		}
		if addr, pid, err := readLiveAddressFile(candidate, alive); err == nil {
			return addr, pid, nil
		}
	}
	return "", 0, primaryErr
}

// readLiveAddressFile parses one address file and checks the daemon pid.
func readLiveAddressFile(path string, alive func(int) bool) (string, int, error) {
	addr, pid, err := parseAddressFile(path)
	if err != nil {
		return "", 0, fmt.Errorf("%w: %w", ErrIBusNotRunning, err)
	}
	if addr == "" {
		return "", 0, fmt.Errorf("%w: %s has no IBUS_ADDRESS", ErrIBusNotRunning, path)
	}
	if pid <= 0 || !alive(pid) {
		return "", 0, fmt.Errorf("%w: daemon pid %d from %s is not alive", ErrIBusNotRunning, pid, path)
	}
	return addr, pid, nil
}

// parseAddressFile reads IBUS_ADDRESS= and IBUS_DAEMON_PID= lines; lines
// starting with '#' are comments. A missing pid is reported as -1.
func parseAddressFile(path string) (addr string, pid int, err error) {
	file, err := os.Open(path) //nolint:gosec // path derived from the IBus address-file convention
	if err != nil {
		return "", 0, err
	}
	defer file.Close()

	pid = -1
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		if value, ok := strings.CutPrefix(line, "IBUS_ADDRESS="); ok {
			addr = strings.TrimSpace(value)
			continue
		}
		if value, ok := strings.CutPrefix(line, "IBUS_DAEMON_PID="); ok {
			if n, convErr := strconv.Atoi(strings.TrimSpace(value)); convErr == nil {
				pid = n
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", 0, err
	}
	return addr, pid, nil
}

// addressFilesNewestFirst lists regular files in dir by modification time,
// newest first.
func addressFilesNewestFirst(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type candidate struct {
		path  string
		mtime time.Time
	}
	candidates := make([]candidate, 0, len(entries))
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{filepath.Join(dir, entry.Name()), info.ModTime()})
	}
	slices.SortStableFunc(candidates, func(a, b candidate) int {
		return b.mtime.Compare(a.mtime)
	})
	paths := make([]string, len(candidates))
	for i, c := range candidates {
		paths[i] = c.path
	}
	return paths
}
