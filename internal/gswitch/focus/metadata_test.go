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

// legacyExtTestTree is one of the two extension trees (SPEC-X11 §3.6): the
// ESM variant shipped by the RPM and the legacy variant shipped by the .deb.
type legacyExtTestTree struct {
	name          string   // subtest name
	dir           string   // tree directory
	shellVersions []string // shell-version entries metadata.json must list
}

func legacyExtTestTrees() []legacyExtTestTree {
	return []legacyExtTestTree{
		{"esm", filepath.Join(focusTestExtensionRoot, ExtensionUUID), []string{"50"}},
		{"legacy", filepath.Join(focusTestExtensionRoot, "legacy", ExtensionUUID), []string{"43", "44"}},
	}
}

func legacyExtTestRead(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// legacyExtTestDirs returns the sorted names of the directories in root.
func legacyExtTestDirs(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read %s: %v", root, err)
	}
	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, entry.Name())
		}
	}
	slices.Sort(dirs)
	return dirs
}

func TestExtensionDirectoryMatchesUUID(t *testing.T) {
	// shell-extension/ holds the ESM tree and legacy/, which holds the legacy
	// tree; both trees carry the daemon's UUID.
	if got, want := legacyExtTestDirs(t, focusTestExtensionRoot), []string{ExtensionUUID, "legacy"}; !slices.Equal(got, want) {
		t.Fatalf("directories in %s = %q, want %q", focusTestExtensionRoot, got, want)
	}
	legacyRoot := filepath.Join(focusTestExtensionRoot, "legacy")
	if got, want := legacyExtTestDirs(t, legacyRoot), []string{ExtensionUUID}; !slices.Equal(got, want) {
		t.Fatalf("directories in %s = %q, want %q", legacyRoot, got, want)
	}
}

func TestExtensionMetadata(t *testing.T) {
	shared := map[string]string{}
	for _, tree := range legacyExtTestTrees() {
		t.Run(tree.name, func(t *testing.T) {
			raw := legacyExtTestRead(t, filepath.Join(tree.dir, "metadata.json"))
			var meta map[string]any
			if err := json.Unmarshal([]byte(raw), &meta); err != nil {
				t.Fatalf("parse metadata.json: %v", err)
			}
			if uuid, _ := meta["uuid"].(string); uuid != ExtensionUUID {
				t.Fatalf("metadata uuid = %q, want %q", uuid, ExtensionUUID)
			}
			versions, _ := meta["shell-version"].([]any)
			for _, want := range tree.shellVersions {
				if !slices.Contains(versions, any(want)) {
					t.Errorf("metadata shell-version = %v, want it to contain %q", versions, want)
				}
			}
			// Default session mode only: the Shell must disable the extension
			// on lock so that the daemon sees Disabling("locked").
			for _, key := range []string{"session-modes", "settings-schema"} {
				if _, ok := meta[key]; ok {
					t.Errorf("metadata has %q, which the extension must not declare", key)
				}
			}
			var fields []string
			for _, key := range []string{"name", "description", "url"} {
				s, _ := meta[key].(string)
				if s == "" {
					t.Errorf("metadata %q is empty", key)
				}
				fields = append(fields, s)
			}
			version, ok := meta["version"].(float64)
			if !ok || version < 1 {
				t.Errorf("metadata version = %v, want a positive integer", meta["version"])
			}
			shared[tree.name] = strings.Join(fields, "|") + "|" + jsonNumber(version)
		})
	}
	// Same extension, two formats: everything but shell-version is shared.
	if shared["esm"] != shared["legacy"] {
		t.Errorf("metadata name/description/url/version differ:\n esm    %q\n legacy %q", shared["esm"], shared["legacy"])
	}
}

func jsonNumber(v float64) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func TestExtensionScriptUsesClientNames(t *testing.T) {
	common := []string{
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
	}
	for _, tree := range legacyExtTestTrees() {
		t.Run(tree.name, func(t *testing.T) {
			script := legacyExtTestRead(t, filepath.Join(tree.dir, "extension.js"))
			for _, want := range common {
				if !strings.Contains(script, want) {
					t.Errorf("extension.js does not contain %s", want)
				}
			}
			switch tree.name {
			case "esm":
				if !strings.Contains(script, "export default class") {
					t.Errorf("extension.js has no 'export default class'")
				}
				if strings.Contains(script, "imports.") {
					t.Errorf("extension.js uses the legacy 'imports.' namespace")
				}
			case "legacy":
				// GNOME Shell 43/44 load extensions through the legacy GJS
				// importer: init() returns the state object, and
				// import/export statements are syntax errors there.
				if !strings.Contains(script, "function init(") {
					t.Errorf("extension.js has no 'function init('")
				}
				for n, line := range strings.Split(script, "\n") {
					if strings.HasPrefix(line, "import ") || strings.HasPrefix(line, "export ") {
						t.Errorf("extension.js line %d is an ESM statement: %q", n+1, line)
					}
				}
			}
		})
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

// legacyExtTestJSConst extracts a top-level const declaration from script
// (single-line, multi-line array or template literal).
func legacyExtTestJSConst(t *testing.T, script, name string) string {
	t.Helper()
	m := regexp.MustCompile(`(?ms)^const ` + name + ` = .*?;$`).FindString(script)
	if m == "" {
		t.Fatalf("extension.js has no top-level const %s", name)
	}
	return m
}

// TestExtensionTreesShareHelpers: the legacy tree is the ESM file ported line
// by line; the pure helpers, the D-Bus interface and the purpose/hint tables
// must stay byte-identical (K-X6, scripts/check-extension.sh mirrors this).
func TestExtensionTreesShareHelpers(t *testing.T) {
	trees := legacyExtTestTrees()
	esm := legacyExtTestRead(t, filepath.Join(trees[0].dir, "extension.js"))
	legacy := legacyExtTestRead(t, filepath.Join(trees[1].dir, "extension.js"))
	for _, name := range []string{"hintMask", "securePurpose", "purposeTable"} {
		if a, b := focusTestJSFunction(t, esm, name), focusTestJSFunction(t, legacy, name); a != b {
			t.Errorf("function %s differs between the ESM and legacy trees:\n--- esm\n%s\n--- legacy\n%s", name, a, b)
		}
	}
	for _, name := range []string{"IFACE_XML", "IBUS_PURPOSE_NAMES", "CLUTTER_PURPOSE_NAMES", "IBUS_SECRET_HINTS", "CLUTTER_SECRET_HINTS"} {
		if a, b := legacyExtTestJSConst(t, esm, name), legacyExtTestJSConst(t, legacy, name); a != b {
			t.Errorf("const %s differs between the ESM and legacy trees:\n--- esm\n%s\n--- legacy\n%s", name, a, b)
		}
	}
	if !strings.Contains(legacyExtTestJSConst(t, esm, "IFACE_XML"), `<interface name="`+Interface+`">`) {
		t.Errorf("IFACE_XML does not declare %s", Interface)
	}
}

// TestExtensionReportsSecretHintsAsPassword: a field that marks itself
// secret only with content hints (Qt 6 password line edits on Wayland report
// purpose NORMAL with HIDDEN_TEXT|SENSITIVE_DATA, which gnome-shell maps to
// IBus HIDDEN_TEXT|PRIVATE) must be reported as "password", so that
// auto-skip-password covers it. The extension's pure helpers run under node
// with the IBus 1.5 and Clutter flag values, for both trees.
func TestExtensionReportsSecretHintsAsPassword(t *testing.T) {
	node, nodeErr := exec.LookPath("node")
	for _, tree := range legacyExtTestTrees() {
		t.Run(tree.name, func(t *testing.T) {
			script := legacyExtTestRead(t, filepath.Join(tree.dir, "extension.js"))
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

			if nodeErr != nil {
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
		})
	}
}

// legacyExtTestJSMethod extracts a method of the FocusReporter class (four
// spaces of indentation) from script.
func legacyExtTestJSMethod(t *testing.T, script, name string) string {
	t.Helper()
	m := regexp.MustCompile(`(?ms)^    ` + name + `\([^)\n]*\) \{\n.*?^    \}$`).FindString(script)
	if m == "" {
		t.Fatalf("extension.js has no FocusReporter method %s", name)
	}
	return m
}

// legacyExtTestReadyWiring: per tree, the IBusManager 'ready' connection and
// how destroy() releases it (connectObject/disconnectObject in the ESM tree,
// a stored id in _ibusIds in the legacy tree).
var legacyExtTestReadyWiring = map[string][]*regexp.Regexp{
	"esm": {
		regexp.MustCompile(`(?s)getIBusManager\(\)\.connectObject\([^;]*\n            'ready', \(_manager, ready\) => this\._onIBusReady\(ready\),\n            this\);`),
		regexp.MustCompile(`(?m)^        getIBusManager\(\)\.disconnectObject\(this\);$`),
	},
	"legacy": {
		regexp.MustCompile(`(?s)this\._ibusIds = \[[^\]]*\n            ibusManager\.connect\('ready', \(_manager, ready\) => this\._onIBusReady\(ready\)\),\n        \];`),
		regexp.MustCompile(`(?m)^        for \(const id of this\._ibusIds\)\n            ibusManager\.disconnect\(id\);$`),
	},
}

// TestExtensionResetsPurposeOnIBusLoss (SPEC-X11 §3.6, rev. 3): a lost IBus
// connection is reported by gnome-shell's IBusManager only as 'ready'(false),
// never as 'focus-out' (F19), so both trees connect 'ready' and reset the
// purpose to "unknown" (denied in strict mode) instead of keeping the last
// field's purpose. The reporter's real purpose methods run under node against
// a fake D-Bus object, for both trees.
func TestExtensionResetsPurposeOnIBusLoss(t *testing.T) {
	node, nodeErr := exec.LookPath("node")
	methods := map[string]string{}
	for _, tree := range legacyExtTestTrees() {
		t.Run(tree.name, func(t *testing.T) {
			script := legacyExtTestRead(t, filepath.Join(tree.dir, "extension.js"))
			if !strings.Contains(script, "'ready'") {
				t.Fatalf("extension.js does not connect IBusManager 'ready'")
			}
			for _, re := range legacyExtTestReadyWiring[tree.name] {
				if !re.MatchString(script) {
					t.Errorf("extension.js does not match %s", re)
				}
			}
			ready := legacyExtTestJSMethod(t, script, "_onIBusReady")
			for _, want := range []string{"this._imFocused = false;", "this._setPurpose('unknown');"} {
				if !strings.Contains(ready, want) {
					t.Errorf("_onIBusReady does not contain %s:\n%s", want, ready)
				}
			}
			methods[tree.name] = ready

			if nodeErr != nil {
				t.Skip("node not available")
			}
			var proto []string
			for _, name := range []string{"_onIBusFocusIn", "_onIBusFocusOut", "_onIBusReady", "_onIBusContentType", "_setPurpose"} {
				proto = append(proto, legacyExtTestJSMethod(t, script, name))
			}
			program := focusTestJSFunction(t, script, "securePurpose") + "\n" +
				focusTestJSFunction(t, script, "purposeTable") + "\n" +
				legacyExtTestJSConst(t, script, "IBUS_PURPOSE_NAMES") + `
const GLib = {Variant: class { constructor(type, value) { this.value = value; } }};
const Main = {inputMethod: {currentFocus: null}};
const emitted = [];
const proto = {
` + strings.Join(proto, ",\n") + `
};
const r = Object.create(proto);
Object.assign(r, {
    _ibusPurposes: purposeTable({FREE_FORM: 0, PASSWORD: 8}, IBUS_PURPOSE_NAMES),
    _ibusSecretHints: 0,
    _imFocused: false,
    _ibusSeen: false,
    _purpose: 'unknown',
    _dbusImpl: {
        emit_property_changed(name, v) { emitted.push('prop ' + name + '=' + v.value); },
        emit_signal(name, v) { emitted.push('signal ' + name + JSON.stringify(v.value)); },
    },
});
const step = (label, fn) => { fn(); emitted.push(label + ': ' + r._purpose + ' focused=' + r._imFocused); };
step('gedit', () => { r._onIBusFocusIn(); r._onIBusContentType(0, 0); });
step('ibus lost', () => r._onIBusReady(false));
step('stray content type', () => r._onIBusContentType(0, 0));
step('ibus back', () => r._onIBusReady(true));
step('password', () => { r._onIBusFocusIn(); r._onIBusContentType(8, 0); });
step('ibus lost again', () => r._onIBusReady(false));
step('ibus still lost', () => r._onIBusReady(false));
console.log(emitted.join('\n'));
`
			want := strings.Join([]string{
				"prop InputPurpose=normal",
				`signal InputPurposeChanged["normal"]`,
				"gedit: normal focused=true",
				"prop InputPurpose=unknown",
				`signal InputPurposeChanged["unknown"]`,
				"ibus lost: unknown focused=false",
				"stray content type: unknown focused=false",
				"ibus back: unknown focused=false",
				"prop InputPurpose=password",
				`signal InputPurposeChanged["password"]`,
				"password: password focused=true",
				"prop InputPurpose=unknown",
				`signal InputPurposeChanged["unknown"]`,
				"ibus lost again: unknown focused=false",
				"ibus still lost: unknown focused=false",
			}, "\n")
			out, err := exec.Command(node, "-e", program).CombinedOutput()
			if err != nil {
				t.Fatalf("node: %v\n%s", err, out)
			}
			if got := strings.TrimSpace(string(out)); got != want {
				t.Fatalf("purpose sequence:\n%s\nwant:\n%s", got, want)
			}
		})
	}
	// The handler lives in the class, outside the helpers check-extension.sh
	// compares, so its equality is checked here.
	if methods["esm"] != "" && methods["esm"] != methods["legacy"] {
		t.Errorf("_onIBusReady differs between the ESM and legacy trees:\n--- esm\n%s\n--- legacy\n%s", methods["esm"], methods["legacy"])
	}
}

// TestCheckExtensionScript runs scripts/check-extension.sh, which repeats the
// checks above from the packaging side (nfpm.yaml entries, node --check of
// both module formats). --strict (node required) applies in CI.
func TestCheckExtensionScript(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	script := filepath.Join(repoRoot, "scripts", "check-extension.sh")
	// The script reads these files itself; read them here too so that the
	// test cache is keyed on their contents.
	for _, path := range []string{script, filepath.Join(repoRoot, "packaging", "nfpm.yaml"), "state.go"} {
		legacyExtTestRead(t, path)
	}
	for _, tree := range legacyExtTestTrees() {
		for _, name := range []string{"metadata.json", "extension.js"} {
			legacyExtTestRead(t, filepath.Join(tree.dir, name))
		}
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	args := []string{script}
	if os.Getenv("CI") != "" {
		args = append(args, "--strict")
	}
	out, err := exec.Command(bash, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", script, strings.Join(args[1:], " "), err, out)
	}
	if !strings.Contains(string(out), "all checks passed") {
		t.Fatalf("%s did not report success:\n%s", script, out)
	}
}
