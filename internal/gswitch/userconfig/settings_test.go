package userconfig

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ucTestDir returns a configuration directory path inside a fresh temp dir;
// the directory itself does not exist yet.
func ucTestDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), DirName)
}

// ucTestWrite creates dir (0700) and writes name with content.
func ucTestWrite(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// ucTestRead returns the content of dir/name.
func ucTestRead(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// ucTestLoad loads dir and fails on an error.
func ucTestLoad(t *testing.T, dir string) *Config {
	t.Helper()
	c, err := Load(dir)
	if err != nil {
		t.Fatalf("Load(%s): %v", dir, err)
	}
	return c
}

// ucTestHasWarning reports whether a warning contains every fragment.
func ucTestHasWarning(warnings []string, fragments ...string) bool {
	for _, w := range warnings {
		ok := true
		for _, f := range fragments {
			if !strings.Contains(w, f) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func TestDefaultSettings(t *testing.T) {
	want := map[string]string{
		KeyAutoEnabled:         "false",
		KeyMinLetters:          "3",
		KeyMaxLetters:          "24",
		KeyShortWords:          "true",
		KeyMargin:              "1.0",
		KeyMaxAltCost:          "7.0",
		KeyHoldoffMs:           "120",
		KeyMinIntervalMs:       "1000",
		KeyDelayMs:             "3",
		KeySwitchDelayMs:       "60",
		KeyLearnOnUndo:         "true",
		KeyLearnUndoCount:      "1",
		KeyRequireFocus:        "false",
		KeySkipPassword:        "true",
		KeySkipTerminal:        "true",
		KeySkipLocked:          "true",
		KeyToggleChord:         "42+54",
		KeySelectionChord:      "42+97+100",
		KeyExposeWords:         "true",
		KeyFocusSource:         "auto",
		KeyLayoutSource:        "auto",
		KeyLayoutHintTimeoutMs: "400",
	}
	s := DefaultSettings()
	if err := s.Validate(); err != nil {
		t.Fatalf("defaults do not validate: %v", err)
	}
	got := s.Values()
	if len(got) != len(want) || len(Keys()) != len(want) {
		t.Fatalf("Values() has %d keys, Keys() %d, want %d", len(got), len(Keys()), len(want))
	}
	for key, v := range want {
		if got[key] != v {
			t.Errorf("default %s = %q, want %q", key, got[key], v)
		}
	}
	// DefaultSettings returns independent chords.
	s.ToggleChord[0] = 29
	if DefaultSettings().ToggleChord[0] != 42 {
		t.Fatal("DefaultSettings shares chord slices between calls")
	}
}

func TestTemplateListsEveryKeyWithDefault(t *testing.T) {
	s, warnings := parseAutoConf(autoConfTemplate)
	if len(warnings) != 0 || !s.Equal(DefaultSettings()) {
		t.Fatalf("template has active assignments: warnings %q", warnings)
	}
	defaults := DefaultSettings().Values()
	seen := map[string]int{}
	var uncommented []string
	for _, line := range splitLines(autoConfTemplate) {
		key, ok := commentedKey(line)
		if !ok {
			continue
		}
		if _, known := defaults[key]; !known {
			continue
		}
		seen[key]++
		body := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#"))
		if body != key+"="+defaults[key] {
			t.Errorf("template line %q, want %q", body, key+"="+defaults[key])
		}
		uncommented = append(uncommented, body)
	}
	for _, key := range Keys() {
		if seen[key] != 1 {
			t.Errorf("template lists %s %d times, want once", key, seen[key])
		}
	}
	parsed, warnings := parseAutoConf(strings.Join(uncommented, "\n"))
	if len(warnings) != 0 || !parsed.Equal(DefaultSettings()) {
		t.Fatalf("uncommented template differs from defaults (warnings %q)", warnings)
	}
}

// ucTestNonDefault holds a valid non-default value for every key.
var ucTestNonDefault = map[string]string{
	KeyAutoEnabled:         "true",
	KeyMinLetters:          "5",
	KeyMaxLetters:          "40",
	KeyShortWords:          "false",
	KeyMargin:              "2.5",
	KeyMaxAltCost:          "25.5",
	KeyHoldoffMs:           "300",
	KeyMinIntervalMs:       "0",
	KeyDelayMs:             "0",
	KeySwitchDelayMs:       "2000",
	KeyLearnOnUndo:         "false",
	KeyLearnUndoCount:      "10",
	KeyRequireFocus:        "true",
	KeySkipPassword:        "false",
	KeySkipTerminal:        "false",
	KeySkipLocked:          "false",
	KeyToggleChord:         "29+88",
	KeySelectionChord:      "none",
	KeyExposeWords:         "false",
	KeyFocusSource:         "off",
	KeyLayoutSource:        "none",
	KeyLayoutHintTimeoutMs: "2000",
}

func TestEveryKeyRoundTrips(t *testing.T) {
	if len(ucTestNonDefault) != len(Keys()) {
		t.Fatalf("ucTestNonDefault covers %d keys, want %d", len(ucTestNonDefault), len(Keys()))
	}
	for _, key := range Keys() {
		t.Run(key, func(t *testing.T) {
			dir := ucTestDir(t)
			v := ucTestNonDefault[key]
			if v == DefaultSettings().Values()[key] {
				t.Fatalf("test value %q is the default", v)
			}
			if err := Update(dir, map[string]string{key: v}); err != nil {
				t.Fatal(err)
			}
			c := ucTestLoad(t, dir)
			if len(c.Warnings) != 0 {
				t.Fatalf("warnings: %q", c.Warnings)
			}
			got := c.Settings.Values()
			if got[key] != v {
				t.Fatalf("%s loaded as %q, want %q", key, got[key], v)
			}
			for other, dv := range DefaultSettings().Values() {
				if other != key && got[other] != dv {
					t.Fatalf("%s changed to %q", other, got[other])
				}
			}
		})
	}

	t.Run("all keys via Save", func(t *testing.T) {
		dir := ucTestDir(t)
		s := DefaultSettings()
		for key, v := range ucTestNonDefault {
			f, _ := fieldFor(key)
			if err := f.set(&s, v); err != nil {
				t.Fatal(err)
			}
		}
		if err := Save(dir, s); err != nil {
			t.Fatal(err)
		}
		c := ucTestLoad(t, dir)
		if !c.Settings.Equal(s) {
			t.Fatalf("loaded %v, want %v", c.Settings.Values(), s.Values())
		}
		if c.Settings.SelectionChord != nil {
			t.Fatalf("SelectionChord = %v, want nil for none", c.Settings.SelectionChord)
		}
	})
}

func TestInvalidValuesFallBackToDefault(t *testing.T) {
	invalid := map[string][]string{
		KeyAutoEnabled:         {"maybe", ""},
		KeyMinLetters:          {"1", "9", "three", "3.5"},
		KeyMaxLetters:          {"3", "65"},
		KeyShortWords:          {"2"},
		KeyMargin:              {"-0.1", "5.1", "NaN", "Inf", "x"},
		KeyMaxAltCost:          {"2.9", "25.6"},
		KeyHoldoffMs:           {"29", "1001"},
		KeyMinIntervalMs:       {"-1", "10001"},
		KeyDelayMs:             {"-1", "201"},
		KeySwitchDelayMs:       {"2001"},
		KeyLearnOnUndo:         {"yesno"},
		KeyLearnUndoCount:      {"0", "11"},
		KeyRequireFocus:        {"enabled"},
		KeySkipPassword:        {"x"},
		KeySkipTerminal:        {"x"},
		KeySkipLocked:          {"x"},
		KeyToggleChord:         {"42+30", "42", "42+abc"},
		KeySelectionChord:      {"42+57", "29+42+54+56+97"},
		KeyExposeWords:         {"x"},
		KeyFocusSource:         {"ibus", "on"},
		KeyLayoutSource:        {"off", "gsettings"},
		KeyLayoutHintTimeoutMs: {"99", "2001"},
	}
	if len(invalid) != len(Keys()) {
		t.Fatalf("invalid table covers %d keys, want %d", len(invalid), len(Keys()))
	}
	defaults := DefaultSettings().Values()
	for _, key := range Keys() {
		t.Run(key, func(t *testing.T) {
			for _, v := range invalid[key] {
				// A valid earlier assignment does not survive a bad
				// last one: last assignment wins, a bad value means
				// the default.
				text := key + "=" + ucTestNonDefault[key] + "\n" + key + "=" + v + "\n"
				s, warnings := parseAutoConf(text)
				if got := s.Values()[key]; got != defaults[key] {
					t.Errorf("%s=%q loaded as %q, want default %q", key, v, got, defaults[key])
				}
				if !ucTestHasWarning(warnings, key, ":2:", "using default") {
					t.Errorf("%s=%q: no warning in %q", key, v, warnings)
				}
				if err := Update(t.TempDir(), map[string]string{key: v}); err == nil {
					t.Errorf("Update accepted %s=%q", key, v)
				}
			}
		})
	}
}

func TestUpstreamAndUnknownKeysIgnored(t *testing.T) {
	text := "layout-switch=56+42\nconvert-key=119\ndelay=10\nreverse-mode=true\nauto-bogus=1\nnot an assignment\nauto-min-letters=4\n"
	s, warnings := parseAutoConf(text)
	want := DefaultSettings()
	want.MinLetters = 4
	if !s.Equal(want) {
		t.Fatalf("settings = %v", s.Values())
	}
	for _, key := range []string{"layout-switch", "convert-key", "delay", "reverse-mode"} {
		if !ucTestHasWarning(warnings, `"`+key+`"`, "default.conf") {
			t.Errorf("no upstream warning for %s in %q", key, warnings)
		}
	}
	if !ucTestHasWarning(warnings, `unknown key "auto-bogus"`) {
		t.Errorf("no unknown-key warning in %q", warnings)
	}
	if !ucTestHasWarning(warnings, ":6:", "not key=value") {
		t.Errorf("no warning for the malformed line in %q", warnings)
	}
	if len(warnings) != 6 {
		t.Errorf("got %d warnings, want 6: %q", len(warnings), warnings)
	}
}

func TestAutoConfGrammar(t *testing.T) {
	text := "\uFEFF# comment\r\n[gswitch-auto]\r\n  auto-enabled = \"true\"  \r\n" +
		"auto-min-letters=4\r\nauto-min-letters=6\r\n\r\n#auto-max-letters=5\r\nhotkey-toggle-auto=\"54 + 42\"\r\n" +
		"focus-source=OFF\r\nauto-margin=2\r\n"
	s, warnings := parseAutoConf(text)
	if len(warnings) != 0 {
		t.Fatalf("warnings: %q", warnings)
	}
	want := DefaultSettings()
	want.AutoEnabled = true
	want.MinLetters = 6 // last assignment wins
	want.FocusSource = SourceOff
	want.Margin = 2
	if !s.Equal(want) {
		t.Fatalf("settings = %v, want %v", s.Values(), want.Values())
	}
	if !s.ToggleChord.Equal(Chord{42, 54}) {
		t.Fatalf("toggle = %v", s.ToggleChord)
	}
}

func TestBoolSpellings(t *testing.T) {
	for v, want := range map[string]bool{
		"true": true, "TRUE": true, "yes": true, "on": true, "1": true,
		"false": false, "False": false, "no": false, "off": false, "0": false,
	} {
		s, warnings := parseAutoConf("auto-short-words=" + v)
		if len(warnings) != 0 || s.ShortWords != want {
			t.Errorf("auto-short-words=%s => %v (warnings %q), want %v", v, s.ShortWords, warnings, want)
		}
	}
}

func TestAutoConfSizeCap(t *testing.T) {
	dir := ucTestDir(t)
	text := "auto-enabled=true\n" + strings.Repeat("# padding\n", MaxAutoConfBytes/10+1)
	ucTestWrite(t, dir, AutoConfName, text)
	c := ucTestLoad(t, dir)
	if !c.Settings.Equal(DefaultSettings()) {
		t.Fatalf("oversized file was parsed: %v", c.Settings.Values())
	}
	if !ucTestHasWarning(c.Warnings, AutoConfName, "larger than") {
		t.Fatalf("no size warning in %q", c.Warnings)
	}
	if err := SetAutoEnabled(dir, false); err == nil {
		t.Fatal("Update rewrote an oversized auto.conf")
	}

	// Exactly at the cap is accepted.
	exact := "auto-enabled=true\n"
	exact += strings.Repeat("#", MaxAutoConfBytes-len(exact)-1) + "\n"
	ucTestWrite(t, dir, AutoConfName, exact)
	if c := ucTestLoad(t, dir); !c.Settings.AutoEnabled || len(c.Warnings) != 0 {
		t.Fatalf("file at the cap: AutoEnabled=%v warnings %q", c.Settings.AutoEnabled, c.Warnings)
	}
}

func TestOverlappingChordsInFileUseDefaults(t *testing.T) {
	s, warnings := parseAutoConf("hotkey-toggle-auto=42+97\nhotkey-convert-selection=42+97+100\n")
	if !s.ToggleChord.Equal(Chord{42, 54}) || !s.SelectionChord.Equal(Chord{42, 97, 100}) {
		t.Fatalf("chords = %v / %v, want defaults", s.ToggleChord, s.SelectionChord)
	}
	if !ucTestHasWarning(warnings, "overlaps") {
		t.Fatalf("no overlap warning in %q", warnings)
	}
}

func TestSettingsValidate(t *testing.T) {
	mutations := map[string]func(*Settings){
		"min letters":     func(s *Settings) { s.MinLetters = 1 },
		"max letters":     func(s *Settings) { s.MaxLetters = 100 },
		"margin NaN":      func(s *Settings) { s.Margin = math.NaN() },
		"max alt cost":    func(s *Settings) { s.MaxAltCost = 30 },
		"holdoff":         func(s *Settings) { s.HoldoffMs = 0 },
		"undo count":      func(s *Settings) { s.LearnUndoCount = 0 },
		"focus source":    func(s *Settings) { s.FocusSource = "x" },
		"layout source":   func(s *Settings) { s.LayoutSource = "" },
		"hint timeout":    func(s *Settings) { s.LayoutHintTimeoutMs = 50 },
		"text key chord":  func(s *Settings) { s.ToggleChord = Chord{42, 30} },
		"one key chord":   func(s *Settings) { s.SelectionChord = Chord{42} },
		"chords overlap":  func(s *Settings) { s.ToggleChord = Chord{42, 97} },
		"duplicate chord": func(s *Settings) { s.ToggleChord = Chord{42, 42} },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			s := DefaultSettings()
			mutate(&s)
			if err := s.Validate(); err == nil {
				t.Fatal("Validate accepted invalid settings")
			}
			if err := Save(ucTestDir(t), s); err == nil {
				t.Fatal("Save accepted invalid settings")
			}
		})
	}
	s := DefaultSettings()
	s.ToggleChord, s.SelectionChord = nil, nil
	if err := s.Validate(); err != nil {
		t.Fatalf("disabled chords rejected: %v", err)
	}
}

func TestSettingsEqualAndDiff(t *testing.T) {
	a := DefaultSettings()
	b := DefaultSettings()
	b.ToggleChord = Chord{54, 42}
	if !a.Equal(b) {
		t.Fatal("chords in another order must be equal")
	}
	if d := DiffSettings(a, b); len(d) != 0 {
		t.Fatalf("DiffSettings of equal settings = %v", d)
	}
	b.MinLetters = 4
	b.SelectionChord = nil
	b.AutoEnabled = true
	if a.Equal(b) {
		t.Fatal("different settings reported equal")
	}
	d := DiffSettings(a, b)
	want := map[string]string{KeyMinLetters: "4", KeySelectionChord: "none", KeyAutoEnabled: "true"}
	if len(d) != len(want) {
		t.Fatalf("DiffSettings = %v, want %v", d, want)
	}
	for k, v := range want {
		if d[k] != v {
			t.Fatalf("DiffSettings[%s] = %q, want %q", k, d[k], v)
		}
	}
}

func TestFormatFloat(t *testing.T) {
	for f, want := range map[float64]string{1: "1.0", 7: "7.0", 25.5: "25.5", 0: "0.0", 0.25: "0.25"} {
		if got := formatFloat(f); got != want {
			t.Errorf("formatFloat(%v) = %q, want %q", f, got, want)
		}
	}
}
