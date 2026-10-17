package tray

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gotk3/gotk3/gtk"

	"github.com/arumata/gswitch/internal/gswitch/focus"
	"github.com/arumata/gswitch/internal/gswitch/ipc"
	"github.com/arumata/gswitch/internal/gswitch/userconfig"
	"github.com/arumata/gswitch/internal/i18n"
)

func gaTestWriteList(t *testing.T, dir, name, text string) {
	t.Helper()
	if err := userconfig.WriteListText(dir, name, text); err != nil {
		t.Fatal(err)
	}
}

// TestGAMessageIDsHaveTranslations checks every fork message ID of
// strings_ga.go: fork prefix, English and Russian text.
func TestGAMessageIDsHaveTranslations(t *testing.T) {
	source, err := parser.ParseFile(token.NewFileSet(), "strings_ga.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	en := i18n.Builtin.Select("en", func(string) string { return "" })
	ru := i18n.Builtin.Select("ru", func(string) string { return "" })
	count := 0
	ast.Inspect(source, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range spec.Names {
			literal, ok := spec.Values[i].(*ast.BasicLit)
			if !ok {
				t.Fatalf("message %s has no literal ID", name.Name)
			}
			key, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatal(err)
			}
			count++
			if !i18n.IsForkKey(key) || !strings.HasPrefix(name.Name, i18n.ForkKeyPrefix) {
				t.Errorf("fork message %s = %q lacks the %s prefix", name.Name, key, i18n.ForkKeyPrefix)
			}
			if en.Text(key) == key {
				t.Errorf("message %s has no English text", key)
			}
			if ru.Text(key) == key || ru.Text(key) == en.Text(key) {
				t.Errorf("message %s has no Russian translation", key)
			}
		}
		return true
	})
	if count < 70 {
		t.Fatalf("only %d fork messages found", count)
	}
}

func TestPlanAutoApplyDiffsAgainstSnapshot(t *testing.T) {
	loaded := userconfig.DefaultSettings()
	edited := loaded
	edited.AutoEnabled = true // never written by Apply
	edited.MinLetters = 4
	edited.SelectionChord = nil
	plan, err := planAutoApply("/tmp/x", loaded, edited,
		[]string{"Ghbdtn,", "rfr"}, []string{"org.gnome.Ptyxis.desktop"},
		[]string{"rfr", "old"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.values) != 2 || plan.values[userconfig.KeyMinLetters] != "4" || plan.values[userconfig.KeySelectionChord] != "none" {
		t.Fatalf("values = %v", plan.values)
	}
	if _, ok := plan.values[userconfig.KeyAutoEnabled]; ok {
		t.Fatal("Apply writes auto-enabled")
	}
	if !slices.Equal(plan.wordsAdd, []string{"ghbdtn"}) || !slices.Equal(plan.wordsRemove, []string{"old"}) {
		t.Fatalf("word diff = +%v -%v", plan.wordsAdd, plan.wordsRemove)
	}
	if !slices.Equal(plan.appsAdd, []string{"org.gnome.ptyxis"}) || len(plan.appsRemove) != 0 {
		t.Fatalf("app diff = +%v -%v", plan.appsAdd, plan.appsRemove)
	}
	if !plan.changed() {
		t.Fatal("plan not marked changed")
	}

	unchanged, err := planAutoApply("/tmp/x", loaded, loaded, []string{"a"}, nil, []string{"a"}, nil)
	if err != nil || unchanged.changed() {
		t.Fatalf("unchanged plan = %+v, %v", unchanged, err)
	}
}

func TestPlanAutoApplyRejectsInvalidInput(t *testing.T) {
	gaTestUseLanguage(t, "en")
	loaded := userconfig.DefaultSettings()
	if _, err := planAutoApply("/tmp/x", loaded, loaded, []string{"two words"}, nil, nil, nil); err == nil {
		t.Fatal("word with inner space accepted")
	}
	if _, err := planAutoApply("/tmp/x", loaded, loaded, nil, []string{"window:12"}, nil, nil); err == nil {
		t.Fatal("unstable window id accepted")
	}
	overlap := loaded
	overlap.SelectionChord = userconfig.Chord{29, 42, 54}
	_, err := planAutoApply("/tmp/x", loaded, overlap, nil, nil, nil, nil)
	if err == nil || err.Error() != tr(strGAChordOverlap) {
		t.Fatalf("overlap error = %v", err)
	}
}

// TestApplyKeepsWordsAppendedAfterLoad: Apply writes list diffs, so a word
// learned (appended by the persister) while the editor was open survives.
func TestApplyKeepsWordsAppendedAfterLoad(t *testing.T) {
	dir := t.TempDir()
	gaTestWriteList(t, dir, userconfig.WordsName, "# words\nalpha\nbeta\n")
	cfg := gaTestLoad(t, dir)
	loadedWords := cfg.Words.Entries()

	// The editor removes beta and adds gamma; meanwhile a word is learned.
	if _, err := userconfig.AppendListEntry(dir, userconfig.WordsName, "learned"); err != nil {
		t.Fatal(err)
	}
	plan, err := planAutoApply(dir, cfg.Settings, cfg.Settings,
		[]string{"alpha", "gamma"}, cfg.Apps.Entries(), loadedWords, cfg.Apps.Entries())
	if err != nil {
		t.Fatal(err)
	}
	reloads := 0
	if err := executeAutoApply(plan, func() error { reloads++; return nil }); err != nil {
		t.Fatalf("apply: %v", err)
	}
	after := gaTestLoad(t, dir)
	if got := after.Words.Entries(); !slices.Equal(got, []string{"alpha", "learned", "gamma"}) {
		t.Fatalf("words after Apply = %v", got)
	}
	text, err := userconfig.ReadListText(dir, userconfig.WordsName)
	if err != nil || !strings.HasPrefix(text, "# words\n") {
		t.Fatalf("comment lost: %q, %v", text, err)
	}
	if reloads != 1 {
		t.Fatalf("reloads = %d, want 1", reloads)
	}
}

func TestExecuteAutoApplyWritesUserLevelOnly(t *testing.T) {
	// Any pkexec or systemctl invocation would be recorded here.
	bin := t.TempDir()
	log := filepath.Join(bin, "called")
	for _, name := range []string{"systemctl", "pkexec"} {
		// #nosec G306 -- executable stub in a test-owned temporary directory.
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\necho called >> '"+log+"'\nexit 1\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)

	dir := filepath.Join(t.TempDir(), "gswitch")
	loaded := userconfig.DefaultSettings()
	edited := loaded
	edited.ToggleChord = userconfig.Chord{29, 97}
	edited.SkipTerminal = false
	plan, err := planAutoApply(dir, loaded, edited, []string{"ghbdtn"}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := executeAutoApply(plan, func() error { return ipc.ErrNotConnected }); err != nil {
		t.Fatalf("apply: %v", err)
	}
	cfg := gaTestLoad(t, dir)
	if !cfg.Settings.ToggleChord.Equal(userconfig.Chord{29, 97}) || cfg.Settings.SkipTerminal || !cfg.Words.Contains("ghbdtn") {
		t.Fatalf("settings not applied: %+v words=%v", cfg.Settings, cfg.Words.Entries())
	}
	for _, name := range []string{userconfig.AutoConfName, userconfig.WordsName, userconfig.AppsName} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s not seeded: %v", name, err)
		}
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("user dir: %v, %v", info, err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("user-level Apply invoked systemctl or pkexec")
	}

	// A daemon that refuses the configuration is reported separately.
	again, err := planAutoApply(dir, cfg.Settings, func() userconfig.Settings { s := cfg.Settings; s.MinLetters = 5; return s }(),
		cfg.Words.Entries(), cfg.Apps.Entries(), cfg.Words.Entries(), cfg.Apps.Entries())
	if err != nil {
		t.Fatal(err)
	}
	var refused *autoReloadError
	if err := executeAutoApply(again, func() error { return errors.New("bad config") }); !errors.As(err, &refused) {
		t.Fatalf("refused reload: %v", err)
	}
	// Nothing changed: no reload at all.
	same, err := planAutoApply(dir, gaTestLoad(t, dir).Settings, gaTestLoad(t, dir).Settings, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := executeAutoApply(same, func() error { return errors.New("must not reload") }); err != nil {
		t.Fatal("unchanged Apply reloaded the daemon")
	}
	if err := executeAutoApply(&autoApplyPlan{skip: true}, nil); err != nil {
		t.Fatalf("skip plan: %v", err)
	}
}

func TestAutoStatusTexts(t *testing.T) {
	gaTestUseLanguage(t, "en")
	for _, tt := range []struct {
		status  ipc.ClientStatus
		service ServiceStatus
		want    string
	}{
		{ipc.ClientStatus{}, StatusStopped, tr(strGAStatusDaemonDown)},
		{ipc.ClientStatus{}, StatusRunning, tr(strGAStatusNoIPC)},
		{gaTestStatus(true, true), StatusRunning, tr(strGAStatusActive)},
		{gaTestStatus(false, true), StatusRunning, tr(strGAStatusOff)},
		{ipc.ClientStatus{Connected: true, Status: ipc.Status{UnavailableReason: "unsupported-layouts"}}, StatusRunning,
			"Unavailable: " + tr(strGAReasonUnsupportedLayouts)},
	} {
		if got := autoStatusText(tt.status, tt.service); got != tt.want {
			t.Errorf("autoStatusText(%+v, %v) = %q, want %q", tt.status, tt.service, got, tt.want)
		}
	}
	for code, id := range map[string]string{
		"degraded": strGAReasonDegraded, "no-layout-converter": strGAReasonNoLayoutConverter,
		"unsupported-layouts": strGAReasonUnsupportedLayouts, "layout-source-off": strGAReasonLayoutSourceOff,
		"layout-unknown": strGAReasonLayoutUnknown, "root-mode": strGAReasonRootMode, "detector-error": strGAReasonDetectorError,
	} {
		if autoReasonText(code) != tr(id) {
			t.Errorf("reason %s untranslated", code)
		}
	}
	if autoReasonText("future-code") != "future-code" {
		t.Fatal("unknown reason codes must be shown verbatim")
	}

	if got := lastCorrectionText(gaTestStatus(true, true)); got != tr(strGALastCorrectionNone) {
		t.Fatalf("no correction = %q", got)
	}
	st := gaTestStatus(true, true)
	st.Last = ipc.Correction{From: "ghbdtn", To: "привет", Direction: "us>ru", Time: time.Unix(1700000000, 0)}
	if got := lastCorrectionText(st); !strings.HasPrefix(got, "Last correction: ghbdtn → привет (") {
		t.Fatalf("last correction = %q", got)
	}
	st.Last.From, st.Last.To = "", ""
	if got := lastCorrectionText(st); !strings.HasPrefix(got, "Last correction: … → …") {
		t.Fatalf("hidden words = %q", got)
	}
}

func TestAutoNotices(t *testing.T) {
	gaTestUseLanguage(t, "en")
	st := gaTestStatus(true, true)
	st.AutoEnabledUnsaved = true
	st.ConfigDir = "/home/u/.config/gswitch"
	st.ConfigWarnings = []string{"auto.conf: unknown key", "shared"}
	notices := autoNotices(st, "/other/gswitch", "persist failed", []string{"shared"})
	if len(notices) != 4 {
		t.Fatalf("notices = %q", notices)
	}
	if notices[0] != "persist failed" || notices[1] != tr(strGAHintNotPersisted) ||
		!strings.Contains(notices[2], "/home/u/.config/gswitch") || !strings.Contains(notices[2], "/other/gswitch") {
		t.Fatalf("notices = %q", notices)
	}
	if notices[3] != "Configuration warnings: shared; auto.conf: unknown key" {
		t.Fatalf("warnings = %q", notices[3])
	}
	if got := autoNotices(ipc.ClientStatus{}, "/x", "", nil); len(got) != 0 {
		t.Fatalf("offline notices = %q", got)
	}
	same := gaTestStatus(true, true)
	same.ConfigDir = "/x/gswitch/"
	if got := autoNotices(same, "/x/gswitch", "", nil); len(got) != 0 {
		t.Fatalf("equal dirs reported: %q", got)
	}
}

func TestAutoToggleWroteFile(t *testing.T) {
	connected := ipc.ClientStatus{Connected: true}
	for _, tt := range []struct {
		hasApp bool
		daemon ipc.ClientStatus
		want   bool
	}{
		{false, ipc.ClientStatus{}, true},
		{false, connected, true},
		{true, ipc.ClientStatus{}, true}, // service stopped: the tray wrote auto.conf
		{true, connected, false},         // the checkbox follows the daemon status
	} {
		if got := autoToggleWroteFile(tt.hasApp, tt.daemon); got != tt.want {
			t.Errorf("autoToggleWroteFile(%v, connected=%v) = %v, want %v", tt.hasApp, tt.daemon.Connected, got, tt.want)
		}
	}
}

// TestServiceRefreshDroppedDuringUserAction: a periodic is-enabled read that
// was requested before (or overlaps) an Autostart toggle must not overwrite
// the checkbox the user just changed.
func TestServiceRefreshDroppedDuringUserAction(t *testing.T) {
	w := &SettingsWindow{}
	requested := w.serviceActionGen
	if !serviceRefreshCurrent(requested, w.serviceActionGen, w.serviceActions) {
		t.Fatal("refresh without a user action dropped")
	}
	w.beginServiceAction() // the user ticks Autostart; systemctl enable runs
	if serviceRefreshCurrent(requested, w.serviceActionGen, w.serviceActions) {
		t.Fatal("refresh requested before the action applied while it runs")
	}
	w.endServiceAction()
	if serviceRefreshCurrent(requested, w.serviceActionGen, w.serviceActions) {
		t.Fatal("refresh requested before the action applied after it ended")
	}
	if !serviceRefreshCurrent(w.serviceActionGen, w.serviceActionGen, w.serviceActions) {
		t.Fatal("refresh requested after the action dropped")
	}
	w.endServiceAction() // unbalanced end stays at zero
	if w.serviceActions != 0 {
		t.Fatalf("serviceActions = %d", w.serviceActions)
	}
}

func TestServiceButtonSensitivity(t *testing.T) {
	for _, tt := range []struct {
		status               ServiceStatus
		start, stop, restart bool
	}{
		{StatusRunning, false, true, true},
		{StatusStopped, true, false, true},
		{StatusFailed, true, false, true},
		{StatusNotInstalled, false, false, false},
		{StatusUnknown, true, true, true},
	} {
		start, stop, restart := serviceButtonSensitivity(tt.status)
		if start != tt.start || stop != tt.stop || restart != tt.restart {
			t.Errorf("status %v: start=%v stop=%v restart=%v", tt.status, start, stop, restart)
		}
	}
}

func TestListEditorDiffAndNormalization(t *testing.T) {
	add, remove := listEditorDiff([]string{"a", "b", "c"}, []string{"c", "d", "a", "d"})
	if !slices.Equal(add, []string{"d"}) || !slices.Equal(remove, []string{"b"}) {
		t.Fatalf("diff = +%v -%v", add, remove)
	}
	got, err := normalizeListEntries([]string{"Ghbdtn,", "ghbdtn", "Ёлка"}, userconfig.NormalizeWord)
	if err != nil || !slices.Equal(got, []string{"ghbdtn", "елка"}) {
		t.Fatalf("normalized = %v, %v", got, err)
	}
	for _, tt := range []struct {
		app  focus.App
		want string
		ok   bool
	}{
		{focus.App{ID: "org.gnome.TextEditor.desktop", WmClass: "gnome-text-editor"}, "org.gnome.texteditor", true},
		{focus.App{ID: "", WmClass: "Xterm"}, "xterm", true},
		{focus.App{ID: "window:7", WmClass: "Foo"}, "foo", true},
		{focus.App{}, "", false},
	} {
		if got, ok := appPickerEntry(tt.app); got != tt.want || ok != tt.ok {
			t.Errorf("appPickerEntry(%+v) = %q, %v", tt.app, got, ok)
		}
	}
}

func TestScanDesktopApps(t *testing.T) {
	user := t.TempDir()
	system := t.TempDir()
	write := func(dir, name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(system, "org.gnome.TextEditor.desktop", "[Desktop Entry]\nType=Application\nName=Text Editor\nStartupWMClass=gnome-text-editor\n[Desktop Action new]\nName=New Window\n")
	write(system, "hidden.desktop", "[Desktop Entry]\nType=Application\nName=Hidden\nNoDisplay=true\n")
	write(system, "link.desktop", "[Desktop Entry]\nType=Link\nName=Link\n")
	write(system, "kitty.desktop", "[Desktop Entry]\nType=Application\nName=kitty\n")
	write(user, "kitty.desktop", "[Desktop Entry]\nType=Application\nName=Kitty (user)\n")
	apps := scanDesktopApps([]string{user, system, filepath.Join(system, "missing")})
	if len(apps) != 2 {
		t.Fatalf("apps = %+v", apps)
	}
	if apps[0].Name != "Kitty (user)" || apps[0].ID != "kitty.desktop" {
		t.Fatalf("user copy does not shadow the system one: %+v", apps[0])
	}
	if apps[1].Name != "Text Editor" || apps[1].WmClass != "gnome-text-editor" {
		t.Fatalf("desktop entry = %+v", apps[1])
	}
	if entry, ok := appPickerEntry(apps[1]); !ok || entry != "org.gnome.texteditor" {
		t.Fatalf("picker entry = %q", entry)
	}
}

func TestFocusExtensionStatusTexts(t *testing.T) {
	gaTestUseLanguage(t, "en")
	for state, action := range map[FocusExtensionState]string{
		FocusExtActive:       "",
		FocusExtDisabled:     tr(strGAButtonEnableExtension),
		FocusExtError:        tr(strGAButtonEnableExtension),
		FocusExtNeedsRelogin: tr(strGAButtonEnableNextLogin),
		FocusExtNotInstalled: "",
		FocusExtOutOfDate:    "",
		FocusExtNoShell:      "",
	} {
		if got := focusExtActionLabel(state); got != action {
			t.Errorf("action(%v) = %q, want %q", state, got, action)
		}
		if focusExtStatusText(FocusExtensionInfo{State: state}) == "" {
			t.Errorf("state %v has no text", state)
		}
	}
	text := focusExtStatusText(FocusExtensionInfo{State: FocusExtError, Errors: []string{"SyntaxError"}})
	if !strings.HasPrefix(text, tr(strGAExtError)) || !strings.Contains(text, "SyntaxError") {
		t.Fatalf("error text = %q", text)
	}
}

// TestAutoPageRoundTrip exercises the GTK page against a temporary user
// directory (GSWITCH_GTK_TEST=1 on an isolated display).
func TestAutoPageRoundTrip(t *testing.T) {
	if os.Getenv("GSWITCH_GTK_TEST") != "1" {
		t.Skip("set GSWITCH_GTK_TEST=1 inside Xvfb")
	}
	runtime.LockOSThread()
	gtk.Init(nil)
	gaTestUseLanguage(t, "en")
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	dir := filepath.Join(home, userconfig.DirName)
	gaTestWriteList(t, dir, userconfig.WordsName, "alpha\n")
	oldAccels := readSwitchAccels
	readSwitchAccels = func() []string { return gaTestHostAccels }
	t.Cleanup(func() { readSwitchAccels = oldAccels })

	w, err := NewSettingsWindow(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.window.Destroy()
	if pages := w.notebook.GetNPages(); pages != 2 {
		t.Fatalf("notebook pages = %d, want 2", pages)
	}
	w.loadAutoConfig()
	for deadline := time.Now().Add(2 * time.Second); w.auto.accels == nil && time.Now().Before(deadline); {
		drainGTK() // the accelerators arrive through glib.IdleAdd
		runtime.Gosched()
	}
	if !slices.Equal(w.auto.accels, gaTestHostAccels) {
		t.Fatalf("accelerators = %v", w.auto.accels)
	}
	if got := w.auto.words.entries(); !slices.Equal(got, []string{"alpha"}) {
		t.Fatalf("loaded words = %v", got)
	}
	w.window.ShowAll()
	drainGTK()

	// A word learned while the editor has no unsaved edits shows up; with
	// unsaved edits the editor keeps them.
	if _, err := userconfig.AppendListEntry(dir, userconfig.WordsName, "learned"); err != nil {
		t.Fatal(err)
	}
	w.onUserConfigChanged(gaTestLoad(t, dir))
	if got := w.auto.words.entries(); !slices.Equal(got, []string{"alpha", "learned"}) {
		t.Fatalf("words after external change = %v", got)
	}
	w.auto.words.add("edited")
	if _, err := userconfig.AppendListEntry(dir, userconfig.WordsName, "second"); err != nil {
		t.Fatal(err)
	}
	w.onUserConfigChanged(gaTestLoad(t, dir))
	if got := w.auto.words.entries(); !slices.Equal(got, []string{"alpha", "learned", "edited"}) {
		t.Fatalf("unsaved edits lost: %v", got)
	}
	w.auto.minLettersSpin.SetValue(4)
	w.auto.words.add("Ghbdtn!")
	w.auto.skipTerminalCheck.SetActive(false)
	w.setHotkeyChord(w.auto.hotkeys[hotkeyToggleAuto], userconfig.Chord{29, 97})
	cfg := DefaultTrayConfig()
	plan, err := w.prepareAutoApply(cfg)
	if err != nil || plan == nil || plan.skip {
		t.Fatalf("prepare: %+v, %v", plan, err)
	}
	if err := executeAutoApply(plan, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}
	saved := gaTestLoad(t, dir)
	if !saved.Words.Contains("second") || !saved.Words.Contains("edited") {
		t.Fatalf("diff Apply lost a word: %v", saved.Words.Entries())
	}
	if saved.Settings.MinLetters != 4 || saved.Settings.SkipTerminal || !saved.Words.Contains("ghbdtn") ||
		!saved.Settings.ToggleChord.Equal(userconfig.Chord{29, 97}) {
		t.Fatalf("saved = %+v words=%v", saved.Settings, saved.Words.Entries())
	}

	// Validation: the selection chord may not contain the toggle chord, and a
	// chord that always fires the layout switch is rejected.
	w.setHotkeyChord(w.auto.hotkeys[hotkeyConvertSelection], userconfig.Chord{29, 97, 100})
	if _, err := w.prepareAutoApply(cfg); err == nil {
		t.Fatal("overlapping chords accepted")
	}
	w.setHotkeyChord(w.auto.hotkeys[hotkeyConvertSelection], userconfig.Chord{42, 56})
	if _, err := w.prepareAutoApply(cfg); err == nil || !strings.Contains(err.Error(), tr(strGAChordConflictSwitch)) {
		t.Fatalf("ConflictAlways chord: %v", err)
	}
	w.setHotkeyChord(w.auto.hotkeys[hotkeyConvertSelection], userconfig.Chord{42, 97, 100})
	if text, _ := w.auto.hotkeys[hotkeyConvertSelection].warning.GetText(); !strings.HasPrefix(text, "Press Shift first") {
		t.Fatalf("order warning = %q", text)
	}

	for _, tt := range []struct {
		status      ServiceStatus
		start, stop bool
	}{
		{StatusRunning, false, true},
		{StatusStopped, true, false},
		{StatusNotInstalled, false, false},
	} {
		w.applyServiceStatusWidgets(tt.status, false, nil)
		if w.startButton.GetSensitive() != tt.start || w.stopButton.GetSensitive() != tt.stop {
			t.Errorf("status %v: start=%v stop=%v", tt.status, w.startButton.GetSensitive(), w.stopButton.GetSensitive())
		}
	}
}
