package focus

import (
	"encoding/json"
	"os"
	"path/filepath"
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
