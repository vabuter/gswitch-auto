package runtime

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStringsToSpecs_NoVariantSplit(t *testing.T) {
	input := []string{"us", "ru", "gb-extd"}
	want := []LayoutSpec{
		{Name: "us"},
		{Name: "ru"},
		{Name: "gb-extd"},
	}

	got := stringsToSpecs(input)

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stringsToSpecs() = %+v, want %+v", got, want)
	}
}

func TestGetCurrentLayoutsPrefersActiveDesktop(t *testing.T) {
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	configDir := filepath.Join(tmp, ".config")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(configDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "kxkbrc"),
		[]byte("[Layout]\nLayoutList=us,ru\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gsettings := `#!/bin/sh
case "$2" in
  org.gnome.desktop.input-sources) echo "[('xkb', 'us'), ('xkb', 'ua')]" ;;
  org.freedesktop.ibus.general) echo "@as []" ;;
  *) exit 1 ;;
esac
`
	gsettingsPath := filepath.Join(binDir, "gsettings")
	if err := os.WriteFile(gsettingsPath, []byte(gsettings), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(gsettingsPath, 0o750); err != nil { //nolint:gosec // Test fixture must be executable.
		t.Fatal(err)
	}
	loginctlPath := filepath.Join(binDir, "loginctl")
	if err := os.WriteFile(loginctlPath, []byte("#!/bin/sh\nexit 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loginctlPath, 0o750); err != nil { //nolint:gosec // Test fixture must be executable.
		t.Fatal(err)
	}
	setxkbmapPath := filepath.Join(binDir, "setxkbmap")
	if err := os.WriteFile(setxkbmapPath, []byte("#!/bin/sh\nprintf 'layout: us,de\\n'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(setxkbmapPath, 0o750); err != nil { //nolint:gosec // Test fixture must be executable.
		t.Fatal(err)
	}
	t.Setenv("HOME", tmp)
	t.Setenv("PATH", binDir)
	t.Setenv("XDG_CONFIG_HOME", "")
	// Hosted runners may inherit desktop variables (Ubuntu runners commonly
	// advertise GNOME). Keep the fixture hermetic so each subtest's
	// XDG_CURRENT_DESKTOP is the only active desktop signal.
	t.Setenv("XDG_SESSION_DESKTOP", "")
	t.Setenv("DESKTOP_SESSION", "")

	tests := []struct {
		desktop string
		want    []LayoutSpec
	}{
		{desktop: "GNOME", want: []LayoutSpec{{Name: "us"}, {Name: "ua"}}},
		{desktop: "KDE", want: []LayoutSpec{{Name: "us"}, {Name: "ru"}}},
		{desktop: "awesome", want: []LayoutSpec{{Name: "us"}, {Name: "de"}}},
	}
	for _, tt := range tests {
		t.Run(tt.desktop, func(t *testing.T) {
			t.Setenv("XDG_CURRENT_DESKTOP", tt.desktop)
			got, err := GetCurrentLayouts(nil)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("GetCurrentLayouts() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestGetLayoutsFromXkb_AlignVariants(t *testing.T) {
	// Simulate parsed output of setxkbmap -query:
	// layout:  us,ru
	// variant: ,phonetic
	layouts := stringsToSpecs([]string{"us", "ru"})
	variants := []string{"", "phonetic"}

	for i := range layouts {
		if i < len(variants) {
			layouts[i].Variant = variants[i]
		}
	}

	if layouts[0].Variant != "" {
		t.Fatalf("expected empty variant for first layout, got %q", layouts[0].Variant)
	}
	if layouts[1].Variant != "phonetic" {
		t.Fatalf("expected variant 'phonetic' for second layout, got %q", layouts[1].Variant)
	}
}

func TestParseIbusEngines_EmptyVariant(t *testing.T) {
	out := "['xkb:us::eng', 'xkb:ru:phonetic:rus']"
	got := parseIbusEngines(out)

	want := []LayoutSpec{
		{Name: "us", Variant: ""},
		{Name: "ru", Variant: "phonetic"},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseIbusEngines() = %+v, want %+v", got, want)
	}
}

func TestParseIbusEngines_DedupAndVariant(t *testing.T) {
	out := "['xkb:us::eng', 'xkb:us::eng', 'xkb:gb:extd:eng', 'xkb:ru::rus']"
	got := parseIbusEngines(out)

	want := []LayoutSpec{
		{Name: "us", Variant: ""},
		{Name: "gb", Variant: "extd"},
		{Name: "ru", Variant: ""},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseIbusEngines() dedup/variant = %+v, want %+v", got, want)
	}
}

func TestParseIbusEngines_IgnoreNonXkb(t *testing.T) {
	out := "['mozc-jp', 'xkb:us::eng']"
	got := parseIbusEngines(out)
	want := []LayoutSpec{{Name: "us", Variant: ""}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseIbusEngines() ignore non-xkb = %+v, want %+v", got, want)
	}
}

func TestParseIbusEngines_EmptyList(t *testing.T) {
	out := "[]"
	got := parseIbusEngines(out)
	if len(got) != 0 {
		t.Fatalf("expected empty slice for empty ibus list, got %+v", got)
	}
}

func TestSplitLayoutVariant(t *testing.T) {
	layout, variant := splitLayoutVariant("us")
	if layout != "us" || variant != "" {
		t.Fatalf("splitLayoutVariant('us') = %s, %s; want us, ''", layout, variant)
	}

	layout, variant = splitLayoutVariant("ru-phonetic")
	if layout != "ru" || variant != "phonetic" {
		t.Fatalf("splitLayoutVariant('ru-phonetic') = %s, %s; want ru, phonetic", layout, variant)
	}
}

func TestSplitLayoutVariant_MultiHyphen(t *testing.T) {
	layout, variant := splitLayoutVariant("gb-mac-intl")
	if layout != "gb" || variant != "mac-intl" {
		t.Fatalf("splitLayoutVariant('gb-mac-intl') = %s, %s; want gb, mac-intl", layout, variant)
	}
}

func TestSplitLayoutVariant_Parentheses(t *testing.T) {
	layout, variant := splitLayoutVariant("ua(unicode)")
	if layout != "ua" || variant != "unicode" {
		t.Fatalf("splitLayoutVariant('ua(unicode)') = %s, %s; want ua, unicode", layout, variant)
	}

	layout, variant = splitLayoutVariant("ru(phonetic)")
	if layout != "ru" || variant != "phonetic" {
		t.Fatalf("splitLayoutVariant('ru(phonetic)') = %s, %s; want ru, phonetic", layout, variant)
	}

	layout, variant = splitLayoutVariant("us")
	if layout != "us" || variant != "" {
		t.Fatalf("splitLayoutVariant('us') = %s, %s; want us, ''", layout, variant)
	}
}

func TestSplitLayoutVariant_WithSpaces(t *testing.T) {
	// Parentheses with spaces
	layout, variant := splitLayoutVariant("ua( unicode )")
	if layout != "ua" || variant != "unicode" {
		t.Fatalf("splitLayoutVariant('ua( unicode )') = %q, %q; want ua, unicode", layout, variant)
	}

	layout, variant = splitLayoutVariant(" ru (phonetic) ")
	if layout != "ru" || variant != "phonetic" {
		t.Fatalf("splitLayoutVariant(' ru (phonetic) ') = %q, %q; want ru, phonetic", layout, variant)
	}

	// Dash with spaces
	layout, variant = splitLayoutVariant("ru - phonetic")
	if layout != "ru" || variant != "phonetic" {
		t.Fatalf("splitLayoutVariant('ru - phonetic') = %q, %q; want ru, phonetic", layout, variant)
	}
}

func TestStringsToSpecs_SkipEmpty(t *testing.T) {
	input := []string{"us", "", "  ", "ru"}
	got := stringsToSpecs(input)
	want := []LayoutSpec{{Name: "us"}, {Name: "ru"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stringsToSpecs skip empty = %+v, want %+v", got, want)
	}
}

func TestGetLayoutsFromFcitx5_ParsesVariantLine(t *testing.T) {
	// Lines as in fcitx5 profile: Name=keyboard-ru-phonetic
	specs := []LayoutSpec{}
	lines := []string{
		"Name=keyboard-us",
		"Name=keyboard-ru-phonetic",
	}

	for _, line := range lines {
		if after, ok := strings.CutPrefix(line, "Name=keyboard-"); ok {
			layout, variant := splitLayoutVariant(after)
			spec := LayoutSpec{Name: layout, Variant: variant}
			if !containsLayoutSpec(specs, spec) {
				specs = append(specs, spec)
			}
		}
	}

	want := []LayoutSpec{
		{Name: "us", Variant: ""},
		{Name: "ru", Variant: "phonetic"},
	}

	if !reflect.DeepEqual(specs, want) {
		t.Fatalf("fcitx parse = %+v, want %+v", specs, want)
	}
}

func TestParseKxkbrcLayouts(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []LayoutSpec
	}{
		{
			name:    "plain two layouts",
			content: "[Layout]\nDisplayNames=,\nLayoutList=us,ru\nOptions=grp:caps_toggle\n",
			want:    []LayoutSpec{{Name: "us"}, {Name: "ru"}},
		},
		{
			name:    "with variants",
			content: "[Layout]\nLayoutList=us,ru\nVariantList=,phonetic\n",
			want:    []LayoutSpec{{Name: "us"}, {Name: "ru", Variant: "phonetic"}},
		},
		{
			name:    "layout keys outside section ignored",
			content: "[Other]\nLayoutList=de,fr\n[Layout]\nLayoutList=us,ru\n",
			want:    []LayoutSpec{{Name: "us"}, {Name: "ru"}},
		},
		{
			name:    "no layout section",
			content: "[Other]\nLayoutList=de,fr\n",
			want:    []LayoutSpec{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseKxkbrcLayouts(tt.content)
			if len(got) != len(tt.want) {
				t.Fatalf("parseKxkbrcLayouts() = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("layout[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestParseGnomeInputSources(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []LayoutSpec
	}{
		{
			name:    "plain two layouts",
			content: "[('xkb', 'us'), ('xkb', 'ru')]",
			want:    []LayoutSpec{{Name: "us"}, {Name: "ru"}},
		},
		{
			name:    "variant encoded with plus",
			content: "[('xkb', 'us'), ('xkb', 'ua+unicode')]",
			want:    []LayoutSpec{{Name: "us"}, {Name: "ua", Variant: "unicode"}},
		},
		{
			name:    "ibus engines skipped",
			content: "[('ibus', 'mozc-jp'), ('xkb', 'us'), ('xkb', 'de')]",
			want:    []LayoutSpec{{Name: "us"}, {Name: "de"}},
		},
		{
			name:    "duplicates removed",
			content: "[('xkb', 'us'), ('xkb', 'us')]",
			want:    []LayoutSpec{{Name: "us"}},
		},
		{
			name:    "empty",
			content: "@a(ss) []",
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseGnomeInputSources(tt.content)
			if len(got) != len(tt.want) {
				t.Fatalf("parseGnomeInputSources() = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("layout[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// renderTestSymbols is a fixture XKB symbols file for the LayoutInfo.Unmapped tests.
const renderTestSymbols = `// Fixture layout with dead keys.

default partial alphanumeric_keys
xkb_symbols "basic" {

    include "renderfix(common)"
    name[Group1]= "Fixture";

    key <AC11>	{[ dead_acute,	dead_diaeresis	]};
    key <AE06>	{[	 6,	asciicircum	]};
    key <AD01>	{[	 q,	Q,	dead_grave	]};
    key <AD02>	{[ NoSymbol,	W		]};
    key <AB02>	{[	 x,	VoidSymbol	]};
    key <AB03>	{[ Multi_key,	C		]};
};

partial alphanumeric_keys
xkb_symbols "late" {

    key <AE06>	{[	 6,	dead_circumflex	]};
    key <AB01>	{[ dead_tilde,	Z		]};
    include "renderfix(common)"
};

hidden partial alphanumeric_keys
xkb_symbols "common" {

    key <AE06>	{[	 6,	dead_circumflex	]};
    key <AC11>	{[ apostrophe,	quotedbl	]};
    key <AB01>	{[	 z,	Z		]};
};
`

// renderTestLoadFixture loads a variant of renderTestSymbols through LoadLayout.
func renderTestLoadFixture(t *testing.T, variant string) *LayoutInfo {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "renderfix"), []byte(renderTestSymbols), 0o600); err != nil {
		t.Fatal(err)
	}
	old := xkbSymbolsPath
	xkbSymbolsPath = dir
	t.Cleanup(func() { xkbSymbolsPath = old })
	layout, err := LoadLayout("renderfix", variant)
	if err != nil {
		t.Fatalf("LoadLayout(renderfix, %q) error = %v", variant, err)
	}
	return layout
}

func TestLoadLayoutUnmapped(t *testing.T) {
	layout := renderTestLoadFixture(t, "")

	// KeyMap keeps upstream behaviour: runeless keysyms are dropped, and a key
	// with no rune at all keeps the included definition.
	wantKeyMap := map[string][]rune{
		"AC11": {'\'', '"'},
		"AE06": {'6', '^'},
		"AD01": {'q', 'Q'},
		"AD02": {'W'},
		"AB01": {'z', 'Z'},
		"AB02": {'x'},
		"AB03": {'C'},
	}
	if !reflect.DeepEqual(layout.KeyMap, wantKeyMap) {
		t.Fatalf("KeyMap = %q, want %q", layout.KeyMap, wantKeyMap)
	}
	wantUnmapped := map[string]bool{"AC11": true, "AD01": true, "AD02": true, "AB02": true, "AB03": true}
	if !reflect.DeepEqual(layout.Unmapped, wantUnmapped) {
		t.Fatalf("Unmapped = %v, want %v (AE06 redefined after the include must be mapped)",
			layout.Unmapped, wantUnmapped)
	}
}

func TestLoadLayoutUnmappedIncludeOverrides(t *testing.T) {
	layout := renderTestLoadFixture(t, "late")
	if !layout.Unmapped["AE06"] {
		t.Error("AE06: dead key in both the section and the include must stay unmapped")
	}
	if layout.Unmapped["AB01"] {
		t.Error("AB01: a mapped definition from a later include must clear the unmapped flag")
	}
	if got := string(layout.KeyMap["AB01"]); got != "zZ" {
		t.Errorf("AB01 KeyMap = %q, want zZ", got)
	}
	if layout.Unmapped["AC11"] {
		t.Error("AC11: mapped in the include, must not be unmapped")
	}
}

// TestLoadLayoutUnmappedBlocksRender checks that the compacted KeyMap of an
// unmapped key ([NoSymbol, W] → "W") is never rendered as level 1.
func TestLoadLayoutUnmappedBlocksRender(t *testing.T) {
	fixture := renderTestLoadFixture(t, "")
	other := &LayoutInfo{KeyMap: map[string][]rune{
		"AD02": {'w', 'W'}, "AE06": {'6', '^'}, "AB01": {'z', 'Z'},
	}}
	for _, tc := range []struct {
		code uint16
		ok   bool
	}{
		{17, false}, // AD02 [NoSymbol, W]
		{7, true},   // AE06
	} {
		buf := []KeyEvent{{Code: tc.code, Value: K_DOWN}}
		_, err := renderWord(buf, 0, 1, fixture, other, false)
		if (err == nil) != tc.ok {
			t.Errorf("renderWord(code %d) error = %v, want ok=%v", tc.code, err, tc.ok)
		}
	}
}

// TestLoadLayoutRealHasNoUnmappedLetters checks the installed us and ru
// layouts leave the alphanumeric block mapped (NewLayoutConverter input is
// unchanged by the Unmapped field).
func TestLoadLayoutRealHasNoUnmappedLetters(t *testing.T) {
	for _, name := range []string{"us", "ru"} {
		layout, err := LoadLayout(name, "")
		if err != nil {
			t.Skipf("XKB symbols not available: %v", err)
		}
		for key := range layout.Unmapped {
			if strings.HasPrefix(key, "A") || key == "TLDE" || key == "BKSL" {
				t.Errorf("%s: %s unexpectedly unmapped", name, key)
			}
		}
	}
}
