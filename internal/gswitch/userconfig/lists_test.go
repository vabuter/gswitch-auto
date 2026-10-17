package userconfig

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestNormalizeWord(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{in: "Ghbdtn,", want: "ghbdtn", ok: true},
		{in: "ghbdtn", want: "ghbdtn", ok: true},
		{in: "ЁЖИК!", want: "ежик", ok: true},
		{in: "Привет", want: "привет", ok: true},
		{in: "k.,jdm", want: "k.,jdm", ok: true},
		{in: `"Ghbdtn`, want: "ghbdtn", ok: true},
		{in: "  hello  ", want: "hello", ok: true},
		{in: strings.Repeat("ж", MaxWordRunes), want: strings.Repeat("ж", MaxWordRunes), ok: true},
		{in: strings.Repeat("ж", MaxWordRunes+1)},
		{in: "two words"},
		{in: "tab\tword"},
		{in: "a#b"},
		{in: "123"},
		{in: "!?"},
		{in: ""},
	}
	for _, tt := range tests {
		got, ok := NormalizeWord(tt.in)
		if ok != tt.ok || got != tt.want {
			t.Errorf("NormalizeWord(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestNormalizeApp(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{in: "org.gnome.Ptyxis.desktop", want: "org.gnome.ptyxis", ok: true},
		{in: "  Kitty ", want: "kitty", ok: true},
		{in: "org.mozilla.*", want: "org.mozilla.*", ok: true},
		{in: "term?", want: "term?", ok: true},
		{in: "window:3"},
		{in: "Window:12"},
		{in: ".desktop"},
		{in: ""},
		{in: "a*[b"},
		{in: "a#b"},
	}
	for _, tt := range tests {
		got, ok := NormalizeApp(tt.in)
		if ok != tt.ok || got != tt.want {
			t.Errorf("NormalizeApp(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestWordListParsing(t *testing.T) {
	dir := ucTestDir(t)
	text := "\uFEFF# learned words\r\nGhbdtn,\r\nghbdtn\r\n  ёжик  # trailing comment\r\n\r\n" +
		"two words\r\n" + strings.Repeat("ж", MaxWordRunes+1) + "\r\nРуддщ\r\n#ignored\r\nk.,jdm\r\n"
	ucTestWrite(t, dir, WordsName, text)
	c := ucTestLoad(t, dir)
	want := []string{"ghbdtn", "ежик", "руддщ", "k.,jdm"}
	if got := c.Words.Entries(); !slices.Equal(got, want) {
		t.Fatalf("entries = %q, want %q", got, want)
	}
	// Warnings name the line, never the rejected word (journal, D-Bus).
	if !ucTestHasWarning(c.Warnings, WordsName+":6:", "rejected") || ucTestHasWarning(c.Warnings, "two words") {
		t.Fatalf("no warning for inner whitespace, or one quoting the entry, in %q", c.Warnings)
	}
	if !ucTestHasWarning(c.Warnings, WordsName+":7:") {
		t.Fatalf("no warning for the 65-rune word in %q", c.Warnings)
	}
	if len(c.Warnings) != 2 {
		t.Fatalf("warnings = %q, want 2", c.Warnings)
	}
	for _, w := range []string{"ghbdtn,", "GHBDTN", "Ёжик", "ежик!", `"руддщ"`, "k.,jdm"} {
		if !c.Words.Contains(w) {
			t.Errorf("Contains(%q) = false", w)
		}
	}
	for _, w := range []string{"ghbdt", "", "123", "two"} {
		if c.Words.Contains(w) {
			t.Errorf("Contains(%q) = true", w)
		}
	}
}

func TestListEntryCap(t *testing.T) {
	dir := ucTestDir(t)
	var b strings.Builder
	for i := range MaxListEntries + 5 {
		fmt.Fprintf(&b, "app%d\n", i)
	}
	ucTestWrite(t, dir, AppsName, b.String())
	c := ucTestLoad(t, dir)
	if c.Apps.Len() != MaxListEntries {
		t.Fatalf("Len = %d, want %d", c.Apps.Len(), MaxListEntries)
	}
	if !c.Apps.MatchApp("app9999", "") || c.Apps.MatchApp("app10000", "") {
		t.Fatal("cap kept the wrong entries")
	}
	if !ucTestHasWarning(c.Warnings, "more than") {
		t.Fatalf("no cap warning in %q", c.Warnings)
	}
}

func TestListByteCap(t *testing.T) {
	dir := ucTestDir(t)
	line := strings.Repeat("я", 30) + "\n" // 61 bytes
	var b strings.Builder
	for b.Len() < MaxListBytes-len(line) {
		b.WriteString("#" + line)
	}
	b.WriteString("первое\n")
	b.WriteString(strings.Repeat("x", 100) + "\nпоследнее\n")
	ucTestWrite(t, dir, WordsName, b.String())
	c := ucTestLoad(t, dir)
	if !c.Words.Contains("первое") || c.Words.Contains("последнее") {
		t.Fatalf("byte cap kept the wrong entries: %q", c.Words.Entries())
	}
	if !ucTestHasWarning(c.Warnings, "larger than") {
		t.Fatalf("no size warning in %q", c.Warnings)
	}
}

func TestMatchAppWithRawArguments(t *testing.T) {
	dir := ucTestDir(t)
	ucTestWrite(t, dir, AppsName, "org.gnome.ptyxis\nkitty\norg.mozilla.*\nxterm-?\n")
	apps := ucTestLoad(t, dir).Apps
	tests := []struct {
		appID, wmClass string
		want           bool
	}{
		{"org.gnome.Ptyxis.desktop", "", true},
		{"org.gnome.ptyxis", "", true},
		{"", "Kitty", true},
		{"unknown.app", "kitty", true},
		{"org.mozilla.firefox.desktop", "", true},
		{"Org.Mozilla.Thunderbird", "", true},
		{"", "XTerm-1", true},
		{"org.gnome.TextEditor.desktop", "gnome-text-editor", false},
		{"org.mozilla", "", false},
		{"", "", false},
		{"kitty-like", "", false},
	}
	for _, tt := range tests {
		if got := apps.MatchApp(tt.appID, tt.wmClass); got != tt.want {
			t.Errorf("MatchApp(%q, %q) = %v, want %v", tt.appID, tt.wmClass, got, tt.want)
		}
	}
}

func TestListSetNilAndEmpty(t *testing.T) {
	var l *ListSet
	if l.MatchApp("kitty", "kitty") || l.Contains("ghbdtn") || l.Len() != 0 {
		t.Fatal("nil ListSet must behave as empty")
	}
	if e := l.Entries(); e == nil || len(e) != 0 {
		t.Fatalf("nil Entries() = %v, want empty non-nil", e)
	}
	if !l.Equal(nil) || !l.Equal(NewWordSet(nil)) || !NewWordSet(nil).Equal(l) {
		t.Fatal("nil and empty sets must be equal")
	}
	if l.Equal(NewWordSet([]string{"a"})) {
		t.Fatal("nil equals a non-empty set")
	}
}

func TestListSetConstructorsAndEqual(t *testing.T) {
	words := NewWordSet([]string{"Ghbdtn", "ghbdtn,", "two words", "руддщ"})
	if got := words.Entries(); !slices.Equal(got, []string{"ghbdtn", "руддщ"}) {
		t.Fatalf("NewWordSet entries = %q", got)
	}
	// Globs are literal in word lists.
	if NewWordSet([]string{"g*"}).Contains("gh") {
		t.Fatal("word list treated '*' as a glob")
	}
	apps := NewAppSet([]string{"org.gnome.Ptyxis.desktop", "window:3", "org.gnome.*"})
	if apps.Len() != 2 || !apps.MatchApp("org.gnome.Console", "") {
		t.Fatalf("NewAppSet entries = %q", apps.Entries())
	}
	if !NewAppSet([]string{"b", "a"}).Equal(NewAppSet([]string{"a", "b"})) {
		t.Fatal("Equal must ignore order")
	}
	if NewAppSet([]string{"a", "b"}).Equal(NewAppSet([]string{"a", "c"})) {
		t.Fatal("different sets equal")
	}
	// Entries returns a copy.
	e := words.Entries()
	e[0] = "changed"
	if words.Entries()[0] != "ghbdtn" {
		t.Fatal("Entries exposes internal storage")
	}
}

func TestMatchAppDoesNotAllocate(t *testing.T) {
	apps := NewAppSet([]string{"org.gnome.ptyxis", "org.mozilla.*"})
	allocs := testing.AllocsPerRun(100, func() {
		apps.MatchApp("org.gnome.ptyxis", "")
		apps.MatchApp("org.mozilla.firefox", "firefox")
		apps.MatchApp("gnome-text-editor", "gnome-text-editor")
	})
	if allocs != 0 {
		t.Fatalf("MatchApp allocated %.1f times per run on normalised input", allocs)
	}
}
