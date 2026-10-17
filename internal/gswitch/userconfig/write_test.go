package userconfig

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

func ucTestMode(t *testing.T, path string) fs.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func ucTestInode(t *testing.T, path string) uint64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Sys().(*syscall.Stat_t).Ino
}

// ucTestNoTemps fails when a temporary file is left in dir.
func ucTestNoTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temporary file %s left behind", e.Name())
		}
	}
}

func TestSaveCreatesFromTemplate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "xdg", DirName) // parents missing too
	s := DefaultSettings()
	s.AutoEnabled = true
	s.MinLetters = 4
	if err := Save(dir, s); err != nil {
		t.Fatal(err)
	}
	if m := ucTestMode(t, dir); m != 0o700 {
		t.Fatalf("dir mode = %o, want 700", m)
	}
	path := filepath.Join(dir, AutoConfName)
	if m := ucTestMode(t, path); m != 0o600 {
		t.Fatalf("file mode = %o, want 600", m)
	}
	text := ucTestRead(t, dir, AutoConfName)
	if !strings.HasPrefix(text, "# gswitch-auto user settings") {
		t.Fatalf("file was not created from the template:\n%s", text)
	}
	if !strings.Contains(text, "# auto-enabled=false\nauto-enabled=true\n") {
		t.Fatalf("auto-enabled not placed after its template line:\n%s", text)
	}
	c := ucTestLoad(t, dir)
	if !c.Settings.Equal(s) || len(c.Warnings) != 0 {
		t.Fatalf("loaded %v (warnings %q), want %v", c.Settings.Values(), c.Warnings, s.Values())
	}
	ucTestNoTemps(t, dir)
}

func TestSaveChmodsExistingDir(t *testing.T) {
	dir := ucTestDir(t)
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	ucTestWrite(t, dir, AutoConfName, "auto-enabled=true\n")
	if err := os.Chmod(filepath.Join(dir, AutoConfName), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, DefaultSettings()); err != nil {
		t.Fatal(err)
	}
	if m := ucTestMode(t, dir); m != 0o700 {
		t.Fatalf("dir mode = %o, want 700", m)
	}
	if m := ucTestMode(t, filepath.Join(dir, AutoConfName)); m != 0o600 {
		t.Fatalf("rewritten file mode = %o, want 600", m)
	}
	if m := ucTestMode(t, filepath.Join(dir, lockName)); m != 0o600 {
		t.Fatalf("lock file mode = %o, want 600", m)
	}
}

func TestUpdatePreservesLines(t *testing.T) {
	dir := ucTestDir(t)
	ucTestWrite(t, dir, AutoConfName, "# my notes\r\n[auto]\r\nfoo=bar\r\nauto-min-letters=4\r\n"+
		"layout-switch=56+42\r\n\r\nauto-min-letters = 5\r\n# end\r\n")
	err := Update(dir, map[string]string{KeyMinLetters: "6", KeyMargin: "2", KeyToggleChord: "54+42"})
	if err != nil {
		t.Fatal(err)
	}
	want := "# my notes\n[auto]\nfoo=bar\nauto-min-letters=6\nlayout-switch=56+42\n\nauto-min-letters=6\n# end\n" +
		"auto-margin=2.0\nhotkey-toggle-auto=42+54\n"
	if got := ucTestRead(t, dir, AutoConfName); got != want {
		t.Fatalf("Update result:\n%q\nwant:\n%q", got, want)
	}
}

func TestUpdateSkipsUnchangedFile(t *testing.T) {
	dir := ucTestDir(t)
	if err := SetAutoEnabled(dir, true); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, AutoConfName)
	ino := ucTestInode(t, path)
	if err := Update(dir, map[string]string{KeyAutoEnabled: "TRUE"}); err != nil {
		t.Fatal(err)
	}
	if ucTestInode(t, path) != ino {
		t.Fatal("unchanged update rewrote the file")
	}
	if err := Update(dir, nil); err != nil {
		t.Fatal(err)
	}
	if err := Update(ucTestDir(t), map[string]string{}); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateRejectsInvalidInput(t *testing.T) {
	dir := ucTestDir(t)
	for _, values := range []map[string]string{
		{"bogus": "1"},
		{"layout-switch": "56+42"},
		{KeyMinLetters: "1"},
		{KeyMinLetters: "4", KeyMargin: "x"},
		{KeyToggleChord: "42+30"},
		{KeyFocusSource: "on\nauto-enabled=true"},
	} {
		if err := Update(dir, values); err == nil {
			t.Errorf("Update(%v) succeeded", values)
		}
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("rejected updates touched the directory: %v", err)
	}
	if err := Update("relative", map[string]string{KeyMinLetters: "4"}); err == nil {
		t.Fatal("Update accepted a relative directory")
	}
}

func TestSetAutoEnabled(t *testing.T) {
	dir := ucTestDir(t)
	for _, enabled := range []bool{true, false, true} {
		if err := SetAutoEnabled(dir, enabled); err != nil {
			t.Fatal(err)
		}
		if c := ucTestLoad(t, dir); c.Settings.AutoEnabled != enabled {
			t.Fatalf("AutoEnabled = %v, want %v", c.Settings.AutoEnabled, enabled)
		}
	}
	text := ucTestRead(t, dir, AutoConfName)
	if strings.Count(text, "\nauto-enabled=") != 1 {
		t.Fatalf("auto-enabled assigned more than once:\n%s", text)
	}
}

// TestWriteThroughSymlink: auto.conf and the word list managed as symlinks
// into a dotfiles directory stay symlinks; the files they point to are
// updated.
func TestWriteThroughSymlink(t *testing.T) {
	dir := ucTestDir(t)
	dotfiles := filepath.Join(t.TempDir(), "dotfiles")
	ucTestWrite(t, dotfiles, AutoConfName, "auto-enabled=false\n")
	ucTestWrite(t, dotfiles, WordsName, "ghbdtn\n")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{AutoConfName, WordsName} {
		if err := os.Symlink(filepath.Join(dotfiles, name), filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}

	if err := SetAutoEnabled(dir, true); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendListEntry(dir, WordsName, "rfr"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{AutoConfName, WordsName} {
		fi, err := os.Lstat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s was replaced by a regular file (mode %v)", name, fi.Mode())
		}
	}
	if got := ucTestRead(t, dotfiles, AutoConfName); !strings.Contains(got, "auto-enabled=true") {
		t.Fatalf("linked auto.conf not updated:\n%s", got)
	}
	if got := ucTestRead(t, dotfiles, WordsName); !strings.Contains(got, "rfr") {
		t.Fatalf("linked word list not updated:\n%s", got)
	}
	if c := ucTestLoad(t, dir); !c.Settings.AutoEnabled || !c.Words.Contains("rfr") {
		t.Fatalf("Load through the links: auto-enabled=%v words=%q", c.Settings.AutoEnabled, c.Words.Entries())
	}
	ucTestNoTemps(t, dir)
	ucTestNoTemps(t, dotfiles)
}

func TestAtomicWriteLeavesNoTempOnFailure(t *testing.T) {
	dir := ucTestDir(t)
	ucTestWrite(t, dir, AutoConfName, "auto-enabled=true\n")
	ucTestWrite(t, dir, WordsName, "ghbdtn\n")
	failing := errors.New("simulated rename failure")
	renameFile = func(string, string) error { return failing }
	t.Cleanup(func() { renameFile = os.Rename })

	if err := SetAutoEnabled(dir, false); !errors.Is(err, failing) {
		t.Fatalf("SetAutoEnabled error = %v, want the rename failure", err)
	}
	if _, err := AppendListEntry(dir, WordsName, "rfr"); !errors.Is(err, failing) {
		t.Fatalf("AppendListEntry error = %v, want the rename failure", err)
	}
	if err := EnsureFiles(dir); !errors.Is(err, failing) {
		t.Fatalf("EnsureFiles error = %v, want the rename failure", err)
	}
	ucTestNoTemps(t, dir)
	if got := ucTestRead(t, dir, AutoConfName); got != "auto-enabled=true\n" {
		t.Fatalf("auto.conf changed to %q", got)
	}
	if got := ucTestRead(t, dir, WordsName); got != "ghbdtn\n" {
		t.Fatalf("word list changed to %q", got)
	}
}

func TestAppendListEntry(t *testing.T) {
	dir := ucTestDir(t)
	if err := WriteListText(dir, WordsName, "# words\nghbdtn"); err != nil { // no final newline
		t.Fatal(err)
	}
	added, err := AppendListEntry(dir, WordsName, "Руддщ!")
	if err != nil || !added {
		t.Fatalf("AppendListEntry = %v, %v", added, err)
	}
	if got := ucTestRead(t, dir, WordsName); got != "# words\nghbdtn\nруддщ\n" {
		t.Fatalf("list = %q", got)
	}
	for _, dup := range []string{"Ghbdtn,", "руддщ", "РУДДЩ"} {
		added, err := AppendListEntry(dir, WordsName, dup)
		if err != nil || added {
			t.Fatalf("AppendListEntry(%q) = %v, %v; want duplicate", dup, added, err)
		}
	}
	if _, err := AppendListEntry(dir, WordsName, "two words"); err == nil {
		t.Fatal("invalid entry appended")
	}
	if _, err := AppendListEntry(dir, AppsName, "window:7"); err == nil {
		t.Fatal("window: entry appended")
	}
}

func TestAppendListEntryToMissingFileUsesTemplate(t *testing.T) {
	dir := ucTestDir(t)
	// The entry is one of the template seeds: the file is created anyway.
	added, err := AppendListEntry(dir, AppsName, "Kitty.desktop")
	if err != nil || !added {
		t.Fatalf("AppendListEntry = %v, %v", added, err)
	}
	if got := ucTestRead(t, dir, AppsName); got != appsTemplate {
		t.Fatalf("apps list = %q, want the template", got)
	}
	added, err = AppendListEntry(dir, AppsName, "kitty")
	if err != nil || added {
		t.Fatalf("second AppendListEntry = %v, %v", added, err)
	}
	if added, err = AppendListEntry(dir, WordsName, "ghbdtn"); err != nil || !added {
		t.Fatalf("AppendListEntry(words) = %v, %v", added, err)
	}
	if got := ucTestRead(t, dir, WordsName); got != wordsTemplate+"ghbdtn\n" {
		t.Fatalf("word list = %q", got)
	}
	c := ucTestLoad(t, dir)
	if !c.Apps.MatchApp("org.gnome.Ptyxis.desktop", "") || !c.Words.Contains("ghbdtn") {
		t.Fatal("created lists not loaded")
	}
}

func TestRemoveListEntry(t *testing.T) {
	dir := ucTestDir(t)
	// A missing file stays missing, even when the template lists the entry.
	if err := RemoveListEntry(dir, WordsName, "ghbdtn"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveListEntry(dir, AppsName, "kitty"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{WordsName, AppsName} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("RemoveListEntry created %s: %v", name, err)
		}
	}
	ucTestWrite(t, dir, WordsName, "# learned\nghbdtn # from undo\nrfr\nGhbdtn,\n# end\n")
	if err := RemoveListEntry(dir, WordsName, "GHBDTN"); err != nil {
		t.Fatal(err)
	}
	if got := ucTestRead(t, dir, WordsName); got != "# learned\nrfr\n# end\n" {
		t.Fatalf("list = %q", got)
	}
	path := filepath.Join(dir, WordsName)
	ino := ucTestInode(t, path)
	if err := RemoveListEntry(dir, WordsName, "absent"); err != nil {
		t.Fatal(err)
	}
	if ucTestInode(t, path) != ino {
		t.Fatal("removing an absent entry rewrote the file")
	}
}

func TestApplyListDiffPreservesConcurrentAppend(t *testing.T) {
	dir := ucTestDir(t)
	ucTestWrite(t, dir, WordsName, "# words\nghbdtn\nrfr # keep me\n")
	snapshot := ucTestLoad(t, dir).Words.Entries() // the editor's view

	// The tray persists a word learned by the daemon meanwhile.
	if _, err := AppendListEntry(dir, WordsName, "lfyyst"); err != nil {
		t.Fatal(err)
	}

	// The user removed "ghbdtn" and added "Ntcn" in the editor.
	edited := []string{"rfr", "ntcn"}
	var add, remove []string
	for _, w := range edited {
		if !strings.Contains(strings.Join(snapshot, "\n"), w) {
			add = append(add, w)
		}
	}
	for _, w := range snapshot {
		if !strings.Contains(strings.Join(edited, "\n"), w) {
			remove = append(remove, w)
		}
	}
	add = append(add, "Ntcn") // duplicate after normalisation
	if err := ApplyListDiff(dir, WordsName, add, remove); err != nil {
		t.Fatal(err)
	}
	want := "# words\nrfr # keep me\nlfyyst\nntcn\n"
	if got := ucTestRead(t, dir, WordsName); got != want {
		t.Fatalf("list = %q, want %q", got, want)
	}
}

func TestApplyListDiffInvalidAddWritesNothing(t *testing.T) {
	dir := ucTestDir(t)
	ucTestWrite(t, dir, WordsName, "ghbdtn\n")
	if err := ApplyListDiff(dir, WordsName, []string{"ok", "not ok"}, []string{"ghbdtn"}); err == nil {
		t.Fatal("ApplyListDiff accepted an invalid entry")
	}
	if got := ucTestRead(t, dir, WordsName); got != "ghbdtn\n" {
		t.Fatalf("list changed to %q", got)
	}
	// Invalid remove entries simply match nothing.
	if err := ApplyListDiff(dir, WordsName, nil, []string{"two words", ""}); err != nil {
		t.Fatal(err)
	}
}

func TestListFunctionsRejectOtherFiles(t *testing.T) {
	dir := ucTestDir(t)
	for _, name := range []string{AutoConfName, "../evil.txt", "", "ui.conf"} {
		if _, err := AppendListEntry(dir, name, "x"); err == nil {
			t.Errorf("AppendListEntry(%q) succeeded", name)
		}
		if err := RemoveListEntry(dir, name, "x"); err == nil {
			t.Errorf("RemoveListEntry(%q) succeeded", name)
		}
		if err := ApplyListDiff(dir, name, []string{"x"}, nil); err == nil {
			t.Errorf("ApplyListDiff(%q) succeeded", name)
		}
		if err := WriteListText(dir, name, "x\n"); err == nil {
			t.Errorf("WriteListText(%q) succeeded", name)
		}
		if _, err := ReadListText(dir, name); err == nil {
			t.Errorf("ReadListText(%q) succeeded", name)
		}
	}
}

func TestListFull(t *testing.T) {
	dir := ucTestDir(t)
	var b strings.Builder
	for i := range MaxListEntries {
		fmt.Fprintf(&b, "app%d\n", i)
	}
	if err := WriteListText(dir, AppsName, b.String()); err != nil {
		t.Fatal(err)
	}
	if added, err := AppendListEntry(dir, AppsName, "one.more"); !errors.Is(err, ErrListFull) || added {
		t.Fatalf("AppendListEntry on a full list = %v, %v; want false, ErrListFull", added, err)
	}
	if err := ApplyListDiff(dir, AppsName, []string{"one.more"}, []string{"app1"}); err != nil {
		t.Fatalf("replacing an entry in a full list: %v", err)
	}
	if err := RemoveListEntry(dir, AppsName, "app2"); err != nil {
		t.Fatalf("removing from a full list: %v", err)
	}
	if c := ucTestLoad(t, dir); c.Apps.Len() != MaxListEntries-1 || !c.Apps.MatchApp("one.more", "") {
		t.Fatalf("Len = %d", c.Apps.Len())
	}
	if err := WriteListText(dir, WordsName, strings.Repeat("x", MaxListBytes+1)); !errors.Is(err, ErrListFull) {
		t.Fatalf("WriteListText oversized = %v, want ErrListFull", err)
	}
}

func TestReadWriteListText(t *testing.T) {
	dir := ucTestDir(t)
	if text, err := ReadListText(dir, WordsName); err != nil || text != "" {
		t.Fatalf("ReadListText(missing) = %q, %v", text, err)
	}
	if err := WriteListText(dir, WordsName, "# hand\nGhbdtn\n"); err != nil {
		t.Fatal(err)
	}
	if text, err := ReadListText(dir, WordsName); err != nil || text != "# hand\nGhbdtn\n" {
		t.Fatalf("ReadListText = %q, %v", text, err)
	}
	if m := ucTestMode(t, filepath.Join(dir, WordsName)); m != 0o600 {
		t.Fatalf("mode = %o, want 600", m)
	}
}

func TestEnsureFiles(t *testing.T) {
	dir := ucTestDir(t)
	ucTestWrite(t, dir, WordsName, "mine\n")
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := EnsureFiles(dir); err != nil {
		t.Fatal(err)
	}
	if m := ucTestMode(t, dir); m != 0o700 {
		t.Fatalf("dir mode = %o, want 700", m)
	}
	if got := ucTestRead(t, dir, WordsName); got != "mine\n" {
		t.Fatalf("existing list overwritten: %q", got)
	}
	if got := ucTestRead(t, dir, AutoConfName); got != autoConfTemplate {
		t.Fatal("auto.conf not seeded from the template")
	}
	if got := ucTestRead(t, dir, AppsName); got != appsTemplate {
		t.Fatal("apps list not seeded from the template")
	}
	for _, name := range []string{AutoConfName, AppsName} {
		if m := ucTestMode(t, filepath.Join(dir, name)); m != 0o600 {
			t.Fatalf("%s mode = %o, want 600", name, m)
		}
	}
	c := ucTestLoad(t, dir)
	if !c.Settings.Equal(DefaultSettings()) || len(c.Warnings) != 0 {
		t.Fatalf("seeded config: %v warnings %q", c.Settings.Values(), c.Warnings)
	}
	for _, app := range []string{
		"org.gnome.terminal", "org.gnome.console", "org.gnome.ptyxis", "com.mitchellh.ghostty",
		"kitty", "alacritty", "org.wezfurlong.wezterm", "org.keepassxc.keepassxc",
	} {
		if !c.Apps.MatchApp(app, "") {
			t.Errorf("seed %s missing", app)
		}
	}
	if c.Apps.Len() != 8 {
		t.Errorf("seeded %d apps, want 8", c.Apps.Len())
	}
	if err := EnsureFiles(dir); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentWritersUnderFlock(t *testing.T) {
	dir := ucTestDir(t)
	const perWriter = 50
	var wg sync.WaitGroup
	errs := make(chan error, 2*perWriter+2*perWriter)
	for w := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range perWriter {
				if _, err := AppendListEntry(dir, WordsName, fmt.Sprintf("w%dx%dz", w, i)); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		s := DefaultSettings()
		for i := range perWriter {
			s.MinLetters = 2 + i%7
			if err := Save(dir, s); err != nil {
				errs <- err
			}
			if err := SetAutoEnabled(dir, i%2 == 0); err != nil {
				errs <- err
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	c := ucTestLoad(t, dir)
	if c.Words.Len() != 2*perWriter {
		t.Fatalf("word list has %d entries, want %d", c.Words.Len(), 2*perWriter)
	}
	if len(c.Warnings) != 0 {
		t.Fatalf("warnings after concurrent writes: %q", c.Warnings)
	}
	if c.Settings.AutoEnabled || c.Settings.MinLetters != 2+(perWriter-1)%7 {
		t.Fatalf("final settings %v", c.Settings.Values())
	}
	ucTestNoTemps(t, dir)
}
