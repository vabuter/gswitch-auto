package userconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func ucTestSkipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permission checks do not apply to root")
	}
}

func ucTestAssertUsable(t *testing.T, c *Config) {
	t.Helper()
	if c == nil {
		t.Fatal("Load returned a nil Config")
	}
	if c.Words == nil || c.Apps == nil {
		t.Fatalf("Load returned nil lists: words %v apps %v", c.Words, c.Apps)
	}
}

func TestLoadMissingDirAndFiles(t *testing.T) {
	dir := ucTestDir(t)
	for _, stage := range []string{"missing dir", "empty dir"} {
		c, err := Load(dir)
		if err != nil {
			t.Fatalf("%s: Load error %v", stage, err)
		}
		ucTestAssertUsable(t, c)
		if !c.Settings.Equal(DefaultSettings()) || c.Words.Len() != 0 || c.Apps.Len() != 0 || len(c.Warnings) != 0 {
			t.Fatalf("%s: got %v words %d apps %d warnings %q", stage, c.Settings.Values(), c.Words.Len(), c.Apps.Len(), c.Warnings)
		}
		if c.Dir != dir {
			t.Fatalf("%s: Dir = %q, want %q", stage, c.Dir, dir)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadRelativeDir(t *testing.T) {
	c, err := Load("relative/gswitch")
	if err == nil {
		t.Fatal("Load accepted a relative directory")
	}
	ucTestAssertUsable(t, c)
	if c.Settings.AutoEnabled {
		t.Fatal("AutoEnabled on error")
	}
}

func TestLoadUnreadableAutoConf(t *testing.T) {
	ucTestSkipIfRoot(t)
	dir := ucTestDir(t)
	ucTestWrite(t, dir, AutoConfName, "auto-enabled=true\nauto-min-letters=5\n")
	ucTestWrite(t, dir, WordsName, "ghbdtn\n")
	if err := os.Chmod(filepath.Join(dir, AutoConfName), 0); err != nil {
		t.Fatal(err)
	}
	c, err := Load(dir)
	if err == nil {
		t.Fatal("Load of an unreadable auto.conf returned no error")
	}
	ucTestAssertUsable(t, c)
	if c.Settings.AutoEnabled || c.Settings.MinLetters != 3 {
		t.Fatalf("settings = %v, want defaults with auto-enabled=false", c.Settings.Values())
	}
	if !c.Words.Contains("ghbdtn") {
		t.Fatal("readable word list was not loaded")
	}
	if !ucTestHasWarning(c.Warnings, "automatic correction disabled") {
		t.Fatalf("error not in warnings: %q", c.Warnings)
	}
}

func TestLoadUnreadableListDisablesAuto(t *testing.T) {
	ucTestSkipIfRoot(t)
	dir := ucTestDir(t)
	ucTestWrite(t, dir, AutoConfName, "auto-enabled=true\n")
	ucTestWrite(t, dir, AppsName, "kitty\n")
	if err := os.Chmod(filepath.Join(dir, AppsName), 0); err != nil {
		t.Fatal(err)
	}
	c, err := Load(dir)
	if err == nil {
		t.Fatal("Load of an unreadable list returned no error")
	}
	ucTestAssertUsable(t, c)
	if c.Settings.AutoEnabled {
		t.Fatal("AutoEnabled stays true although a list is unreadable")
	}
	if c.Apps.Len() != 0 {
		t.Fatalf("apps = %v", c.Apps.Entries())
	}
}

func TestLoadUnreadableDir(t *testing.T) {
	ucTestSkipIfRoot(t)
	dir := ucTestDir(t)
	ucTestWrite(t, dir, AutoConfName, "auto-enabled=true\n")
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	c, err := Load(dir)
	if err == nil {
		t.Fatal("Load of an unreadable directory returned no error")
	}
	ucTestAssertUsable(t, c)
	if c.Settings.AutoEnabled {
		t.Fatal("AutoEnabled on error")
	}
}

func TestLoadParseErrorsKeepListsNonNil(t *testing.T) {
	dir := ucTestDir(t)
	ucTestWrite(t, dir, AutoConfName, "garbage\nauto-min-letters=x\n")
	ucTestWrite(t, dir, WordsName, "two words\n")
	ucTestWrite(t, dir, AppsName, "window:12\n")
	c := ucTestLoad(t, dir)
	ucTestAssertUsable(t, c)
	if c.Words.Len() != 0 || c.Apps.Len() != 0 {
		t.Fatalf("rejected entries loaded: %v %v", c.Words.Entries(), c.Apps.Entries())
	}
	if len(c.Warnings) != 4 {
		t.Fatalf("warnings = %q, want 4", c.Warnings)
	}
}

func TestDirFor(t *testing.T) {
	tests := []struct {
		home, xdg, want string
		wantErr         bool
	}{
		{home: "/home/u", xdg: "", want: "/home/u/.config/gswitch"},
		{home: "/home/u", xdg: "/srv/cfg", want: "/srv/cfg/gswitch"},
		{home: "/home/u", xdg: "relative/cfg", want: "/home/u/.config/gswitch"},
		{home: "", xdg: "/srv/cfg", want: "/srv/cfg/gswitch"},
		{home: "", xdg: "", wantErr: true},
		{home: "", xdg: "relative", wantErr: true},
		{home: "relative", xdg: "", wantErr: true},
	}
	for _, tt := range tests {
		got, err := DirFor(tt.home, tt.xdg)
		if tt.wantErr {
			if err == nil {
				t.Errorf("DirFor(%q, %q) = %q, want error", tt.home, tt.xdg, got)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("DirFor(%q, %q) = %q, %v; want %q", tt.home, tt.xdg, got, err, tt.want)
		}
	}
}

func TestDirPrecedence(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	t.Setenv("HOME", home)

	t.Setenv("XDG_CONFIG_HOME", xdg)
	if got, err := Dir(); err != nil || got != filepath.Join(xdg, DirName) {
		t.Fatalf("Dir() with absolute XDG = %q, %v", got, err)
	}
	t.Setenv("XDG_CONFIG_HOME", "relative/xdg")
	if got, err := Dir(); err != nil || got != filepath.Join(home, ".config", DirName) {
		t.Fatalf("Dir() with relative XDG = %q, %v", got, err)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	if got, err := Dir(); err != nil || got != filepath.Join(home, ".config", DirName) {
		t.Fatalf("Dir() without XDG = %q, %v", got, err)
	}
	// Without XDG_CONFIG_HOME, Dir agrees with os.UserConfigDir.
	ucd, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := Dir(); got != filepath.Join(ucd, DirName) {
		t.Fatalf("Dir() = %q, os.UserConfigDir()/gswitch = %q", got, filepath.Join(ucd, DirName))
	}
}

func TestConfigEqual(t *testing.T) {
	dir := ucTestDir(t)
	ucTestWrite(t, dir, AutoConfName, "auto-enabled=true\n")
	ucTestWrite(t, dir, WordsName, "ghbdtn\nrfr\n")
	a := ucTestLoad(t, dir)
	// An identical rewrite changes the stamps, not the config.
	ucTestWrite(t, dir, WordsName, "rfr\nghbdtn\n")
	b := ucTestLoad(t, dir)
	if !a.Equal(b) {
		t.Fatal("reordered identical list must be equal")
	}
	var nilCfg *Config
	if !nilCfg.Equal(nil) || nilCfg.Equal(a) || a.Equal(nil) {
		t.Fatal("nil handling of Config.Equal")
	}
	ucTestWrite(t, dir, AutoConfName, "auto-enabled=true\nbogus=1\n")
	if c := ucTestLoad(t, dir); a.Equal(c) {
		t.Fatal("a new warning must make configs differ")
	}
	ucTestWrite(t, dir, AutoConfName, "auto-enabled=true\n")
	ucTestWrite(t, dir, AppsName, "kitty\n")
	if c := ucTestLoad(t, dir); a.Equal(c) {
		t.Fatal("a new app entry must make configs differ")
	}
}
