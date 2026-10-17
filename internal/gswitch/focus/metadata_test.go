package focus

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var focusTestExtensionRoot = filepath.Join("..", "..", "..", "shell-extension")

func TestExtensionDirectoryMatchesUUID(t *testing.T) {
	entries, err := os.ReadDir(focusTestExtensionRoot)
	if err != nil {
		t.Fatalf("read %s: %v", focusTestExtensionRoot, err)
	}
	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, entry.Name())
		}
	}
	if len(dirs) != 1 || dirs[0] != ExtensionUUID {
		t.Fatalf("extension directories = %q, want exactly [%q]", dirs, ExtensionUUID)
	}
}

func TestExtensionMetadata(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(focusTestExtensionRoot, ExtensionUUID, "metadata.json"))
	if err != nil {
		t.Fatalf("read metadata.json: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("parse metadata.json: %v", err)
	}
	if uuid, _ := meta["uuid"].(string); uuid != ExtensionUUID {
		t.Fatalf("metadata uuid = %q, want %q", uuid, ExtensionUUID)
	}
	versions, _ := meta["shell-version"].([]any)
	if !slices.Contains(versions, any("50")) {
		t.Fatalf("metadata shell-version = %v, want it to contain \"50\"", versions)
	}
	// Default session mode only: the Shell must disable the extension on
	// lock so that the daemon sees Disabling("locked").
	for _, key := range []string{"session-modes", "settings-schema"} {
		if _, ok := meta[key]; ok {
			t.Errorf("metadata has %q, which the extension must not declare", key)
		}
	}
	for _, key := range []string{"name", "description", "url"} {
		if s, _ := meta[key].(string); s == "" {
			t.Errorf("metadata %q is empty", key)
		}
	}
}

func TestExtensionScriptUsesClientNames(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(focusTestExtensionRoot, ExtensionUUID, "extension.js"))
	if err != nil {
		t.Fatalf("read extension.js: %v", err)
	}
	script := string(raw)
	for _, want := range []string{
		"'" + BusName + "'",
		"'" + string(ObjectPath) + "'",
		`<interface name="` + Interface + `">`,
		`<method name="GetFocus">`,
		`<method name="ListRunningApps">`,
		`<arg type="a(sss)" name="apps" direction="out"/>`,
		`<signal name="FocusChanged">`,
		`<signal name="InputPurposeChanged">`,
		`<signal name="Disabling">`,
		`'locked'`,
		`'disabled'`,
		"export default class",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("extension.js does not contain %s", want)
		}
	}
}

// focusTestJSFunction extracts a top-level function declaration from script.
func focusTestJSFunction(t *testing.T, script, name string) string {
	t.Helper()
	m := regexp.MustCompile(`(?ms)^function ` + name + `\(.*?^}$`).FindString(script)
	if m == "" {
		t.Fatalf("extension.js has no top-level function %s", name)
	}
	return m
}

// TestExtensionReportsSecretHintsAsPassword: a field that marks itself
// secret only with content hints (Qt 6 password line edits on Wayland report
// purpose NORMAL with HIDDEN_TEXT|SENSITIVE_DATA, which gnome-shell maps to
// IBus HIDDEN_TEXT|PRIVATE) must be reported as "password", so that
// auto-skip-password covers it. The extension's pure helpers run under node
// with the IBus 1.5 and Clutter flag values.
func TestExtensionReportsSecretHintsAsPassword(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(focusTestExtensionRoot, ExtensionUUID, "extension.js"))
	if err != nil {
		t.Fatalf("read extension.js: %v", err)
	}
	script := string(raw)
	for _, want := range []string{
		`'set-content-type', (_manager, purpose, hints) => this._onIBusContentType(purpose, hints)`,
		`'notify::content-hints'`,
		`hintMask(IBus.InputHints, IBUS_SECRET_HINTS)`,
		`hintMask(Clutter.InputContentHintFlags, CLUTTER_SECRET_HINTS)`,
		`const IBUS_SECRET_HINTS = ['HIDDEN_TEXT', 'PRIVATE'];`,
		`const CLUTTER_SECRET_HINTS = ['HIDDEN_TEXT', 'SENSITIVE_DATA'];`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("extension.js does not contain %s", want)
		}
	}

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available")
	}
	program := focusTestJSFunction(t, script, "hintMask") + "\n" +
		focusTestJSFunction(t, script, "securePurpose") + `
const ibus = hintMask({HIDDEN_TEXT: 4096, PRIVATE: 2048, SPELLCHECK: 1}, ['HIDDEN_TEXT', 'PRIVATE', 'MISSING']);
const clutter = hintMask({HIDDEN_TEXT: 64, SENSITIVE_DATA: 128}, ['HIDDEN_TEXT', 'SENSITIVE_DATA']);
const cases = [
    [securePurpose('normal', 4096, ibus), 'password'],
    [securePurpose('normal', 2048 | 1, ibus), 'password'],
    [securePurpose('unknown', 4096, ibus), 'password'],
    [securePurpose('normal', 1, ibus), 'normal'],
    [securePurpose('normal', 0, ibus), 'normal'],
    [securePurpose('pin', 4096, ibus), 'pin'],
    [securePurpose('password', 0, ibus), 'password'],
    [securePurpose('normal', 128, clutter), 'password'],
    [securePurpose('email', 64, clutter), 'password'],
    [securePurpose('normal', 0, 0), 'normal'],
];
const bad = cases.filter(([got, want]) => got !== want);
if (ibus !== 6144 || clutter !== 192 || bad.length > 0)
    throw new Error('masks ' + ibus + ' ' + clutter + '; mismatches ' + JSON.stringify(bad));
console.log('ok');
`
	out, err := exec.Command(node, "-e", program).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("node: %v\n%s", err, out)
	}
}
