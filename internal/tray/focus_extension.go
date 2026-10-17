package tray

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/arumata/gswitch/internal/gswitch/focus"
)

// FocusExtensionState is the state of the gswitch-focus Shell extension as
// the tray reports it in the settings window.
type FocusExtensionState int

const (
	// FocusExtNoShell: org.gnome.Shell is not on the session bus (not GNOME).
	FocusExtNoShell FocusExtensionState = iota
	// FocusExtNotInstalled: unknown to the Shell and no extension files found.
	FocusExtNotInstalled
	// FocusExtNeedsRelogin: the files exist but the running Shell has not
	// loaded them; GNOME scans extension directories only at login (C9).
	FocusExtNeedsRelogin
	// FocusExtDisabled: loaded by the Shell but not enabled.
	FocusExtDisabled
	// FocusExtActive: enabled and running.
	FocusExtActive
	// FocusExtError: the Shell failed to load or enable it (see Errors).
	FocusExtError
	// FocusExtOutOfDate: the metadata does not list the running Shell version.
	FocusExtOutOfDate
)

// String returns a stable identifier for logs.
func (s FocusExtensionState) String() string {
	switch s {
	case FocusExtNoShell:
		return "no-shell"
	case FocusExtNotInstalled:
		return "not-installed"
	case FocusExtNeedsRelogin:
		return "needs-relogin"
	case FocusExtDisabled:
		return "disabled"
	case FocusExtActive:
		return "active"
	case FocusExtError:
		return "error"
	case FocusExtOutOfDate:
		return "out-of-date"
	default:
		return fmt.Sprintf("FocusExtensionState(%d)", int(s))
	}
}

// FocusExtensionInfo describes the extension: its state, the directory it is
// loaded or installed from, and the Shell's error messages for FocusExtError.
type FocusExtensionInfo struct {
	State  FocusExtensionState
	Path   string
	Errors []string
}

const (
	focusExtShellName   = "org.gnome.Shell"
	focusExtShellPath   = dbus.ObjectPath("/org/gnome/Shell")
	focusExtShellIface  = "org.gnome.Shell.Extensions"
	focusExtStateSignal = focusExtShellIface + ".ExtensionStateChanged"
	focusExtCallTimeout = 2 * time.Second
	focusExtSchema      = "org.gnome.shell"
	focusExtEnabledKey  = "enabled-extensions"
	focusExtDisabledKey = "disabled-extensions"
	focusExtWindowAppID = "window:"
)

// ExtensionState values of GNOME Shell (js/misc/extensionUtils.js); the
// Shell sends them as doubles.
const (
	focusExtShellActive       = 1
	focusExtShellInactive     = 2
	focusExtShellError        = 3
	focusExtShellOutOfDate    = 4
	focusExtShellDownloading  = 5
	focusExtShellInitialized  = 6
	focusExtShellDeactivating = 7
	focusExtShellActivating   = 8
	focusExtShellUninstalled  = 99
)

// focusExtRunGsettings runs gsettings and returns its trimmed output. Tests
// replace it so that they never touch the user's settings.
var focusExtRunGsettings = func(ctx context.Context, args ...string) (string, error) {
	// #nosec G204 -- fixed binary; the arguments are the schema, key and a
	// formatted string list built by this file.
	out, err := exec.CommandContext(ctx, "gsettings", args...).Output()
	if err != nil {
		return "", fmt.Errorf("gsettings %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// focusExtDataDirs lists the data directories searched for an installed
// extension, the user's first because a user copy shadows the system copy
// (extensionSystem.js:717-721).
var focusExtDataDirs = func() []string {
	var dirs []string
	if dataHome := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(dataHome) {
		dirs = append(dirs, dataHome)
	} else if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs, filepath.Join(home, ".local", "share"))
	}
	return append(dirs, "/usr/share")
}

// focusExtInstalledPath returns the directory of an installed copy of the
// extension, or "" when there is none.
func focusExtInstalledPath() string {
	for _, dataDir := range focusExtDataDirs() {
		dir := filepath.Join(dataDir, "gnome-shell", "extensions", focus.ExtensionUUID)
		if st, err := os.Stat(filepath.Join(dir, "metadata.json")); err == nil && st.Mode().IsRegular() {
			return dir
		}
	}
	return ""
}

func focusExtCallContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, focusExtCallTimeout)
}

func focusExtShell(conn *dbus.Conn) dbus.BusObject {
	return conn.Object(focusExtShellName, focusExtShellPath)
}

var errFocusExtNoConn = errors.New("no session bus connection")

// QueryFocusExtension reports the extension's state through the Shell's
// org.gnome.Shell.Extensions interface. conn must be a PRIVATE session
// connection (dbus.ConnectSessionBus(), owned by the tray's App.forkConn,
// closed in Quit); never dbus.SessionBus(): fyne systray shares that one
// (systray_unix.go:192) and its stayRegistered loop breaks on
// ExtensionStateChanged (2 args), C27. On error the returned info has
// State FocusExtError and the error message in Errors.
func QueryFocusExtension(ctx context.Context, conn *dbus.Conn) (FocusExtensionInfo, error) {
	if conn == nil {
		return focusExtErrorInfo(errFocusExtNoConn), errFocusExtNoConn
	}
	ctx, cancel := focusExtCallContext(ctx)
	defer cancel()

	var owned bool
	err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, focusExtShellName).Store(&owned)
	if err != nil {
		err = fmt.Errorf("check %s: %w", focusExtShellName, err)
		return focusExtErrorInfo(err), err
	}
	if !owned {
		return FocusExtensionInfo{State: FocusExtNoShell}, nil
	}

	var info map[string]dbus.Variant
	err = focusExtShell(conn).CallWithContext(ctx, focusExtShellIface+".GetExtensionInfo",
		dbus.FlagNoAutoStart, focus.ExtensionUUID).Store(&info)
	if err != nil {
		err = fmt.Errorf("get extension info: %w", err)
		return focusExtErrorInfo(err), err
	}
	installed := focusExtInstalledPath()
	result := stateFromInfo(info, installed != "")
	if result.Path == "" {
		result.Path = installed
	}
	if result.State == FocusExtError {
		var messages []string
		err := focusExtShell(conn).CallWithContext(ctx, focusExtShellIface+".GetExtensionErrors",
			dbus.FlagNoAutoStart, focus.ExtensionUUID).Store(&messages)
		if err == nil && len(messages) > 0 {
			result.Errors = messages
		}
	}
	return result, nil
}

func focusExtErrorInfo(err error) FocusExtensionInfo {
	return FocusExtensionInfo{State: FocusExtError, Errors: []string{err.Error()}}
}

// stateFromInfo maps the Shell's GetExtensionInfo result. An empty map means
// the running Shell does not know the UUID: installed files then mean that a
// relogin is needed. Transitional Shell states (activating, deactivating)
// count as disabled until the next ExtensionStateChanged.
func stateFromInfo(info map[string]dbus.Variant, dirExists bool) FocusExtensionInfo {
	missing := FocusExtNotInstalled
	if dirExists {
		missing = FocusExtNeedsRelogin
	}
	if len(info) == 0 {
		return FocusExtensionInfo{State: missing}
	}

	result := FocusExtensionInfo{Path: focusExtVariantString(info["path"])}
	state, ok := focusExtVariantNumber(info["state"])
	if !ok {
		result.State = FocusExtError
		result.Errors = []string{"extension info has no state"}
		return result
	}
	switch state {
	case focusExtShellActive:
		result.State = FocusExtActive
	case focusExtShellInactive, focusExtShellInitialized, focusExtShellDeactivating, focusExtShellActivating:
		result.State = FocusExtDisabled
	case focusExtShellError:
		result.State = FocusExtError
		if message := focusExtVariantString(info["error"]); message != "" {
			result.Errors = []string{message}
		}
	case focusExtShellOutOfDate:
		result.State = FocusExtOutOfDate
	case focusExtShellDownloading, focusExtShellUninstalled:
		result.State = missing
	default:
		result.State = FocusExtError
		result.Errors = []string{fmt.Sprintf("unknown extension state %v", state)}
	}
	return result
}

func focusExtVariantString(v dbus.Variant) string {
	s, _ := v.Value().(string)
	return s
}

func focusExtVariantNumber(v dbus.Variant) (float64, bool) {
	switch n := v.Value().(type) {
	case float64:
		return n, true
	case int32:
		return float64(n), true
	case uint32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint64:
		return float64(n), true
	default:
		return 0, false
	}
}

// EnableFocusExtension enables the extension. When the running Shell knows
// it, EnableExtension does the work and takes effect at once. Otherwise (it
// was installed after login, C9, or no Shell answered) the UUID is appended
// to org.gnome.shell enabled-extensions and removed from
// disabled-extensions, which takes precedence (extensionSystem.js
// _getEnabledExtensions), and needsRelogin is true.
func EnableFocusExtension(ctx context.Context, conn *dbus.Conn) (needsRelogin bool, err error) {
	ctx, cancel := focusExtCallContext(ctx)
	defer cancel()

	callErr := errFocusExtNoConn
	var enabled bool
	if conn != nil {
		callErr = focusExtShell(conn).CallWithContext(ctx, focusExtShellIface+".EnableExtension",
			dbus.FlagNoAutoStart, focus.ExtensionUUID).Store(&enabled)
	}
	if callErr == nil && enabled {
		return false, nil
	}
	if err := focusExtUpdateGsettingsList(ctx, focusExtEnabledKey, appendToGsettingsList); err != nil {
		return false, errors.Join(callErr, err)
	}
	if err := focusExtUpdateGsettingsList(ctx, focusExtDisabledKey, focusExtRemoveFromGsettingsList); err != nil {
		return false, errors.Join(callErr, err)
	}
	return true, nil
}

// DisableFocusExtension disables the extension through the Shell; when the
// Shell does not know it, the UUID is removed from enabled-extensions so that
// a pending pre-enable does not take effect at the next login.
func DisableFocusExtension(ctx context.Context, conn *dbus.Conn) error {
	ctx, cancel := focusExtCallContext(ctx)
	defer cancel()

	callErr := errFocusExtNoConn
	var disabled bool
	if conn != nil {
		callErr = focusExtShell(conn).CallWithContext(ctx, focusExtShellIface+".DisableExtension",
			dbus.FlagNoAutoStart, focus.ExtensionUUID).Store(&disabled)
	}
	if callErr == nil && disabled {
		return nil
	}
	if err := focusExtUpdateGsettingsList(ctx, focusExtEnabledKey, focusExtRemoveFromGsettingsList); err != nil {
		return errors.Join(callErr, err)
	}
	return nil
}

// focusExtUpdateGsettingsList applies edit to one org.gnome.shell string list
// key and writes the result when it changed.
func focusExtUpdateGsettingsList(ctx context.Context, key string, edit func(cur, uuid string) (string, bool)) error {
	cur, err := focusExtRunGsettings(ctx, "get", focusExtSchema, key)
	if err != nil {
		return fmt.Errorf("read %s %s: %w", focusExtSchema, key, err)
	}
	if _, err := focusExtParseStrv(cur); err != nil {
		return fmt.Errorf("read %s %s: %w", focusExtSchema, key, err)
	}
	next, changed := edit(cur, focus.ExtensionUUID)
	if !changed {
		return nil
	}
	if _, err := focusExtRunGsettings(ctx, "set", focusExtSchema, key, next); err != nil {
		return fmt.Errorf("write %s %s: %w", focusExtSchema, key, err)
	}
	return nil
}

// WatchFocusExtension calls cb with a fresh FocusExtensionInfo whenever the
// Shell emits ExtensionStateChanged for the extension (match arg0=uuid). The
// match rule is installed before WatchFocusExtension returns, so a caller
// that watches first and then queries misses no transition. cb runs on an
// internal goroutine. When ctx is done the match rule and the signal channel
// are removed (RemoveMatchSignal + RemoveSignal). conn must be a private
// session connection (see QueryFocusExtension).
func WatchFocusExtension(ctx context.Context, conn *dbus.Conn, cb func(FocusExtensionInfo)) {
	if conn == nil || cb == nil {
		return
	}
	rule := []dbus.MatchOption{
		dbus.WithMatchSender(focusExtShellName),
		dbus.WithMatchObjectPath(focusExtShellPath),
		dbus.WithMatchInterface(focusExtShellIface),
		dbus.WithMatchMember("ExtensionStateChanged"),
		dbus.WithMatchArg(0, focus.ExtensionUUID),
	}
	signals := make(chan *dbus.Signal, 16)
	conn.Signal(signals)
	addCtx, cancel := focusExtCallContext(ctx)
	err := conn.AddMatchSignalContext(addCtx, rule...)
	cancel()
	if err != nil {
		conn.RemoveSignal(signals)
		fmt.Printf("Warning: failed to watch the focus extension: %v\n", err)
		return
	}

	go func() {
		defer func() {
			conn.RemoveSignal(signals)
			removeCtx, cancel := context.WithTimeout(context.Background(), focusExtCallTimeout)
			_ = conn.RemoveMatchSignalContext(removeCtx, rule...)
			cancel()
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case sig, ok := <-signals:
				if !ok {
					return
				}
				body, ok := focusExtStateChange(sig)
				if !ok {
					continue
				}
				info, err := QueryFocusExtension(ctx, conn)
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					info = stateFromInfo(body, focusExtInstalledPath() != "")
				}
				cb(info)
			}
		}
	}()
}

// focusExtStateChange recognises ExtensionStateChanged for the extension (the
// connection may carry other signals) and returns its info dictionary.
func focusExtStateChange(sig *dbus.Signal) (map[string]dbus.Variant, bool) {
	if sig.Name != focusExtStateSignal || sig.Path != focusExtShellPath || len(sig.Body) < 2 {
		return nil, false
	}
	if uuid, _ := sig.Body[0].(string); uuid != focus.ExtensionUUID {
		return nil, false
	}
	body, _ := sig.Body[1].(map[string]dbus.Variant)
	return body, true
}

// ListRunningAppsForPicker lists running applications for the "Add
// running…" dialog. Window-backed Shell ids ("window:N", not stable across
// sessions) are returned with ID="" so that the picker stores the WM_CLASS;
// entries with neither an id nor a WM_CLASS are dropped. The error wraps
// focus.ErrUnavailable when the extension is not running.
func ListRunningAppsForPicker(ctx context.Context, conn *dbus.Conn) ([]focus.App, error) {
	apps, err := focus.ListRunningApps(ctx, conn)
	if err != nil {
		return nil, err
	}
	return focusExtPickerApps(apps), nil
}

func focusExtPickerApps(apps []focus.App) []focus.App {
	out := make([]focus.App, 0, len(apps))
	for _, app := range apps {
		if strings.HasPrefix(app.ID, focusExtWindowAppID) {
			app.ID = ""
		}
		if strings.TrimSpace(app.ID) == "" && strings.TrimSpace(app.WmClass) == "" {
			continue
		}
		out = append(out, app)
	}
	return out
}

// appendToGsettingsList appends uuid to a GVariant string list as printed by
// gsettings ("['a', 'b']" or "@as []"). It returns cur unchanged and false
// when uuid is already listed or cur cannot be parsed.
func appendToGsettingsList(cur, uuid string) (string, bool) {
	items, err := focusExtParseStrv(cur)
	if err != nil || slices.Contains(items, uuid) {
		return cur, false
	}
	return focusExtFormatStrv(append(items, uuid)), true
}

// focusExtRemoveFromGsettingsList removes every occurrence of uuid; it
// returns cur unchanged and false when uuid is absent or cur cannot be parsed.
func focusExtRemoveFromGsettingsList(cur, uuid string) (string, bool) {
	items, err := focusExtParseStrv(cur)
	if err != nil || !slices.Contains(items, uuid) {
		return cur, false
	}
	return focusExtFormatStrv(slices.DeleteFunc(items, func(item string) bool { return item == uuid })), true
}

// focusExtParseStrv parses the GVariant text form of an "as" value.
func focusExtParseStrv(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimSpace(strings.TrimPrefix(s, "@as"))
	if len(s) < 2 || s[0] != '[' || s[len(s)-1] != ']' {
		return nil, fmt.Errorf("not a string list: %q", s)
	}
	body := strings.TrimSpace(s[1 : len(s)-1])
	items := []string{}
	for body != "" {
		item, rest, err := focusExtParseQuoted(body)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
		rest = strings.TrimSpace(rest)
		if rest == "" {
			break
		}
		if rest[0] != ',' {
			return nil, fmt.Errorf("expected ',' in string list at %q", rest)
		}
		body = strings.TrimSpace(rest[1:])
		if body == "" {
			return nil, errors.New("trailing ',' in string list")
		}
	}
	return items, nil
}

func focusExtParseQuoted(s string) (item, rest string, err error) {
	quote := s[0]
	if quote != '\'' && quote != '"' {
		return "", "", fmt.Errorf("expected a quoted string at %q", s)
	}
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\' && i+1 < len(s):
			i++
			b.WriteByte(s[i])
		case c == quote:
			return b.String(), s[i+1:], nil
		default:
			b.WriteByte(c)
		}
	}
	return "", "", fmt.Errorf("unterminated string in %q", s)
}

// focusExtFormatStrv renders items in the GVariant text form gsettings set
// accepts.
func focusExtFormatStrv(items []string) string {
	if len(items) == 0 {
		return "@as []"
	}
	escape := strings.NewReplacer(`\`, `\\`, `'`, `\'`)
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = "'" + escape.Replace(item) + "'"
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
