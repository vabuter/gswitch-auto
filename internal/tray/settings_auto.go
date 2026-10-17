package tray

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gotk3/gotk3/glib"
	"github.com/gotk3/gotk3/gtk"

	"github.com/arumata/gswitch/internal/gswitch/ipc"
	"github.com/arumata/gswitch/internal/gswitch/userconfig"
)

// autoSettings holds the widgets and the editor state of the "Auto
// correction" page of the settings window.
type autoSettings struct {
	// Frame "Automatic correction".
	autoEnabledCheck *gtk.CheckButton
	enabledUpdating  bool
	statusLabel      *gtk.Label
	lastLabel        *gtk.Label
	noticeLabel      *gtk.Label
	// minLettersAdj is kept with its spin button on purpose: see the GC note
	// on the delay adjustments in settings_window.go.
	minLettersAdj     *gtk.Adjustment
	minLettersSpin    *gtk.SpinButton
	shortWordsCheck   *gtk.CheckButton
	learnOnUndoCheck  *gtk.CheckButton
	skipTerminalCheck *gtk.CheckButton
	skipPasswordCheck *gtk.CheckButton
	requireFocusCheck *gtk.CheckButton

	// Frame "Hotkeys" (settings_hotkeys.go).
	keycapStyle *gtk.CssProvider
	hotkeys     [2]*hotkeyRow
	accels      []string

	// Frames "Exception words" and "Excluded applications"
	// (settings_auto_lists.go).
	words         *autoListEditor
	apps          *autoListEditor
	addLastButton *gtk.Button
	extLabel      *gtk.Label
	extButton     *gtk.Button
	extHint       *gtk.Label

	// Editor snapshot: what was loaded from (or last written to) the user
	// directory. Apply writes only the difference against it.
	dir            string
	loaded         bool
	loadedSettings userconfig.Settings
	fileEnabled    bool
	loadWarnings   []string

	// Live state.
	daemon        ipc.ClientStatus
	serviceStatus ServiceStatus
	extInfo       FocusExtensionInfo
	extKnown      bool
	persistError  string
}

func newAutoFrame(title string) (*gtk.Frame, *gtk.Grid, error) {
	frame, err := gtk.FrameNew(title)
	if err != nil {
		return nil, nil, err
	}
	grid, err := gtk.GridNew()
	if err != nil {
		return nil, nil, err
	}
	grid.SetColumnSpacing(10)
	grid.SetRowSpacing(8)
	grid.SetMarginTop(10)
	grid.SetMarginBottom(10)
	grid.SetMarginStart(10)
	grid.SetMarginEnd(10)
	frame.Add(grid)
	return frame, grid, nil
}

func newHintLabel(text string) (*gtk.Label, error) {
	label, err := gtk.LabelNew("")
	if err != nil {
		return nil, err
	}
	label.SetMarkup("<small><span foreground='gray'>" + glib.MarkupEscapeText(text) + "</span></small>")
	label.SetLineWrap(true)
	label.SetMaxWidthChars(60)
	label.SetXAlign(0)
	label.SetHAlign(gtk.ALIGN_START)
	return label, nil
}

func newWrappedLabel() (*gtk.Label, error) {
	label, err := gtk.LabelNew("")
	if err != nil {
		return nil, err
	}
	label.SetLineWrap(true)
	label.SetMaxWidthChars(60)
	label.SetXAlign(0)
	label.SetHAlign(gtk.ALIGN_START)
	return label, nil
}

// createAutoPage builds the "Auto correction" notebook page.
func (w *SettingsWindow) createAutoPage() (*gtk.ScrolledWindow, error) {
	w.auto = &autoSettings{serviceStatus: StatusUnknown}
	scrolled, err := gtk.ScrolledWindowNew(nil, nil)
	if err != nil {
		return nil, err
	}
	scrolled.SetPolicy(gtk.POLICY_NEVER, gtk.POLICY_AUTOMATIC)
	box, err := gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 10)
	if err != nil {
		return nil, err
	}
	box.SetMarginTop(10)
	box.SetMarginBottom(10)
	box.SetMarginStart(10)
	box.SetMarginEnd(10)

	for _, create := range []func() (*gtk.Frame, error){
		w.createAutoFrame,
		w.createHotkeysFrame,
		w.createWordsFrame,
		w.createAppsFrame,
	} {
		frame, err := create()
		if err != nil {
			return nil, err
		}
		box.PackStart(frame, false, false, 0)
	}
	scrolled.Add(box)
	w.applyLoadedAutoConfig(nil, nil)
	return scrolled, nil
}

// createAutoFrame builds the "Automatic correction" frame.
func (w *SettingsWindow) createAutoFrame() (*gtk.Frame, error) {
	frame, grid, err := newAutoFrame(tr(strGASectionAuto))
	if err != nil {
		return nil, err
	}
	a := w.auto
	row := 0

	if a.autoEnabledCheck, err = gtk.CheckButtonNewWithLabel(tr(strGACheckEnabled)); err != nil {
		return nil, err
	}
	a.autoEnabledCheck.Connect("toggled", w.onAutoEnabledToggled)
	grid.Attach(a.autoEnabledCheck, 0, row, 2, 1)
	row++

	statusTitle, err := gtk.LabelNew(tr(strLabelStatus))
	if err != nil {
		return nil, err
	}
	statusTitle.SetHAlign(gtk.ALIGN_START)
	grid.Attach(statusTitle, 0, row, 1, 1)
	if a.statusLabel, err = newWrappedLabel(); err != nil {
		return nil, err
	}
	a.statusLabel.SetHExpand(true)
	grid.Attach(a.statusLabel, 1, row, 1, 1)
	row++

	if a.lastLabel, err = newWrappedLabel(); err != nil {
		return nil, err
	}
	if style, _ := a.lastLabel.GetStyleContext(); style != nil {
		style.AddClass("dim-label")
	}
	grid.Attach(a.lastLabel, 0, row, 2, 1)
	row++

	if a.noticeLabel, err = newWrappedLabel(); err != nil {
		return nil, err
	}
	a.noticeLabel.SetNoShowAll(true)
	if style, _ := a.noticeLabel.GetStyleContext(); style != nil {
		style.AddClass("warning")
	}
	grid.Attach(a.noticeLabel, 0, row, 2, 1)
	row++

	minLabel, err := gtk.LabelNew(tr(strGALabelMinLetters))
	if err != nil {
		return nil, err
	}
	minLabel.SetHAlign(gtk.ALIGN_START)
	grid.Attach(minLabel, 0, row, 1, 1)
	defaults := userconfig.DefaultSettings()
	if a.minLettersAdj, err = gtk.AdjustmentNew(float64(defaults.MinLetters), 2, 8, 1, 1, 0); err != nil {
		return nil, err
	}
	if a.minLettersSpin, err = newSpinButton(a.minLettersAdj); err != nil {
		return nil, err
	}
	a.minLettersSpin.SetNumeric(true)
	a.minLettersSpin.SetHAlign(gtk.ALIGN_START)
	grid.Attach(a.minLettersSpin, 1, row, 1, 1)
	row++

	for _, check := range []struct {
		target **gtk.CheckButton
		label  string
	}{
		{&a.shortWordsCheck, strGACheckShortWords},
		{&a.learnOnUndoCheck, strGACheckLearnOnUndo},
		{&a.skipTerminalCheck, strGACheckSkipTerminal},
		{&a.skipPasswordCheck, strGACheckSkipPassword},
		{&a.requireFocusCheck, strGACheckRequireFocus},
	} {
		button, err := gtk.CheckButtonNewWithLabel(tr(check.label))
		if err != nil {
			return nil, err
		}
		*check.target = button
		grid.Attach(button, 0, row, 2, 1)
		row++
	}

	hint, err := newHintLabel(tr(strGAHintTrigger))
	if err != nil {
		return nil, err
	}
	grid.Attach(hint, 0, row, 2, 1)
	return frame, nil
}

// styleKeycap gives a label the keycap look of the conversion shortcuts
// popover (settings_shortcuts.go).
func (w *SettingsWindow) styleKeycap(label *gtk.Label) error {
	a := w.auto
	if a.keycapStyle == nil {
		provider, err := gtk.CssProviderNew()
		if err != nil {
			return err
		}
		if err := provider.LoadFromData(shortcutsCSS); err != nil {
			return err
		}
		a.keycapStyle = provider
	}
	style, err := label.GetStyleContext()
	if err != nil {
		return err
	}
	style.AddProvider(a.keycapStyle, gtk.STYLE_PROVIDER_PRIORITY_APPLICATION)
	style.AddClass("shortcut-keycap")
	return nil
}

// autoUserDir returns the user configuration directory of this window.
func (w *SettingsWindow) autoUserDir() string {
	if dir := w.app.UserConfigDir(); dir != "" {
		return dir
	}
	dir, err := userconfig.Dir()
	if err != nil {
		return ""
	}
	return dir
}

// loadAutoConfig loads the user configuration and the extension status into
// the page; it runs from loadConfig when the window is shown.
func (w *SettingsWindow) loadAutoConfig() {
	if w.auto == nil {
		return
	}
	w.auto.dir = w.autoUserDir()
	cfg, err := userconfig.Load(w.auto.dir)
	w.applyLoadedAutoConfig(cfg, err)
	w.loadSwitchAccelsAsync()
	w.auto.daemon = w.app.daemonStatus()
	if w.app != nil {
		w.app.mu.Lock()
		info, known, persistError := w.app.focusExtInfo, w.app.focusExtKnown, w.app.persistError
		w.app.mu.Unlock()
		w.auto.persistError = persistError
		if known {
			w.setFocusExtensionInfo(info)
		} else if conn := w.app.ForkConn(); conn != nil {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), runningAppsTimeout)
				info, err := QueryFocusExtension(ctx, conn)
				cancel()
				if err == nil {
					w.app.onFocusExtensionChanged(info)
				}
			}()
		}
	}
	w.refreshAutoStatus()
}

// applyLoadedAutoConfig replaces the editor content and its snapshot. A nil
// cfg shows the defaults.
func (w *SettingsWindow) applyLoadedAutoConfig(cfg *userconfig.Config, loadErr error) {
	a := w.auto
	settings := userconfig.DefaultSettings()
	var words, apps []string
	a.loadWarnings = nil
	if cfg != nil {
		settings = cfg.Settings
		words = cfg.Words.Entries()
		apps = cfg.Apps.Entries()
		a.loadWarnings = slices.Clone(cfg.Warnings)
		a.loaded = true
	}
	if loadErr != nil && !slices.Contains(a.loadWarnings, loadErr.Error()) {
		a.loadWarnings = append(a.loadWarnings, loadErr.Error())
	}
	a.loadedSettings = settings
	a.fileEnabled = settings.AutoEnabled
	w.setAutoSettingsWidgets(settings)
	a.words.load(words)
	a.apps.load(apps)
	w.refreshAutoStatus()
}

// setAutoSettingsWidgets shows the GUI-editable settings.
func (w *SettingsWindow) setAutoSettingsWidgets(s userconfig.Settings) {
	a := w.auto
	a.minLettersSpin.SetValue(float64(s.MinLetters))
	a.shortWordsCheck.SetActive(s.ShortWords)
	a.learnOnUndoCheck.SetActive(s.LearnOnUndo)
	a.skipTerminalCheck.SetActive(s.SkipTerminal)
	a.skipPasswordCheck.SetActive(s.SkipPassword)
	a.requireFocusCheck.SetActive(s.RequireFocus)
	for i, chord := range []userconfig.Chord{s.ToggleChord, s.SelectionChord} {
		if hr := a.hotkeys[i]; hr != nil {
			w.setHotkeyChord(hr, chord)
		}
	}
}

// collectAutoSettings returns the snapshot settings with the GUI-editable
// values replaced by the widgets' values. Keys without widgets (margin,
// holdoff, …) keep their loaded values.
func (w *SettingsWindow) collectAutoSettings() userconfig.Settings {
	a := w.auto
	s := a.loadedSettings
	s.MinLetters = a.minLettersSpin.GetValueAsInt()
	s.ShortWords = a.shortWordsCheck.GetActive()
	s.LearnOnUndo = a.learnOnUndoCheck.GetActive()
	s.SkipTerminal = a.skipTerminalCheck.GetActive()
	s.SkipPassword = a.skipPasswordCheck.GetActive()
	s.RequireFocus = a.requireFocusCheck.GetActive()
	s.ToggleChord, s.SelectionChord = w.hotkeyChords()
	return s
}

// autoSettingsDirty reports unsaved edits of the GUI-editable settings.
func (w *SettingsWindow) autoSettingsDirty() bool {
	current := w.collectAutoSettings()
	current.AutoEnabled = w.auto.loadedSettings.AutoEnabled
	return !current.Equal(w.auto.loadedSettings)
}

// onUserConfigChanged follows hand edits and the tray's own writes: parts
// without unsaved edits reload; edited parts keep the user's edits (Apply
// merges them as a diff).
func (w *SettingsWindow) onUserConfigChanged(cfg *userconfig.Config) {
	if w.auto == nil || cfg == nil || w.window == nil {
		return
	}
	a := w.auto
	a.fileEnabled = cfg.Settings.AutoEnabled
	a.loadWarnings = slices.Clone(cfg.Warnings)
	if !w.autoSettingsDirty() {
		a.loadedSettings = cfg.Settings
		w.setAutoSettingsWidgets(cfg.Settings)
	} else {
		a.loadedSettings.AutoEnabled = cfg.Settings.AutoEnabled
	}
	if !a.words.dirty() {
		a.words.load(cfg.Words.Entries())
	}
	if !a.apps.dirty() {
		a.apps.load(cfg.Apps.Entries())
	}
	w.refreshAutoStatus()
}

// setDaemonStatus shows a new daemon status (GTK thread).
func (w *SettingsWindow) setDaemonStatus(st ipc.ClientStatus) {
	if w.auto == nil || w.window == nil {
		return
	}
	w.auto.daemon = st
	w.refreshAutoStatus()
}

// setAutoPersistError shows or clears a persistence failure (GTK thread).
func (w *SettingsWindow) setAutoPersistError(message string) {
	if w.auto == nil || w.window == nil {
		return
	}
	w.auto.persistError = message
	w.refreshAutoStatus()
}

// setAutoServiceStatus records the service state used by the status text.
func (w *SettingsWindow) setAutoServiceStatus(status ServiceStatus) {
	if w.auto == nil {
		return
	}
	w.auto.serviceStatus = status
	w.refreshAutoStatus()
}

// refreshAutoStatus redraws the live parts of the frame.
func (w *SettingsWindow) refreshAutoStatus() {
	a := w.auto
	if a == nil || a.statusLabel == nil {
		return
	}
	st := a.daemon
	enabled := a.fileEnabled
	if st.Connected {
		enabled = st.AutoEnabled
	}
	a.enabledUpdating = true
	a.autoEnabledCheck.SetActive(enabled)
	a.enabledUpdating = false

	a.statusLabel.SetText(autoStatusText(st, a.serviceStatus))
	a.lastLabel.SetText(lastCorrectionText(st))
	notices := autoNotices(st, a.dir, a.persistError, a.loadWarnings)
	a.noticeLabel.SetText(strings.Join(notices, "\n"))
	a.noticeLabel.SetVisible(len(notices) > 0)
	if a.addLastButton != nil {
		a.addLastButton.SetSensitive(st.Connected && st.Last.From != "")
	}
}

// autoReasonText translates an AutoUnavailableReason code.
func autoReasonText(code string) string {
	switch code {
	case "degraded":
		return tr(strGAReasonDegraded)
	case "no-layout-converter":
		return tr(strGAReasonNoLayoutConverter)
	case "unsupported-layouts":
		return tr(strGAReasonUnsupportedLayouts)
	case "layout-source-off":
		return tr(strGAReasonLayoutSourceOff)
	case "layout-unknown":
		return tr(strGAReasonLayoutUnknown)
	case "root-mode":
		return tr(strGAReasonRootMode)
	case "detector-error":
		return tr(strGAReasonDetectorError)
	default:
		return code
	}
}

// autoStatusText is the live status line: Active / Off / Service not
// running / No connection / Unavailable: reason.
func autoStatusText(st ipc.ClientStatus, service ServiceStatus) string {
	switch {
	case !st.Connected && service == StatusRunning:
		return tr(strGAStatusNoIPC)
	case !st.Connected:
		return tr(strGAStatusDaemonDown)
	case !st.AutoAvailable:
		return fmt.Sprintf(tr(strGAStatusUnavailable), autoReasonText(st.UnavailableReason))
	case st.AutoEnabled:
		return tr(strGAStatusActive)
	default:
		return tr(strGAStatusOff)
	}
}

// lastCorrectionText describes the last correction; the words are hidden
// ("…") when the daemon does not expose them (ipc-expose-words=false).
func lastCorrectionText(st ipc.ClientStatus) string {
	if !st.Connected || (st.Last.Time.IsZero() && st.Last.From == "" && st.Last.To == "") {
		return tr(strGALastCorrectionNone)
	}
	from, to := st.Last.From, st.Last.To
	if from == "" {
		from = "…"
	}
	if to == "" {
		to = "…"
	}
	text := fmt.Sprintf(tr(strGALastCorrection), from, to)
	if !st.Last.Time.IsZero() {
		text += " (" + st.Last.Time.Local().Format(time.TimeOnly) + ")"
	}
	return text
}

// autoNotices lists the warnings shown under the status: a persistence
// failure, unsaved runtime state, a configuration directory mismatch (K22)
// and configuration warnings of the tray and the daemon.
func autoNotices(st ipc.ClientStatus, dir, persistError string, loadWarnings []string) []string {
	var notices []string
	if persistError != "" {
		notices = append(notices, persistError)
	}
	if st.Connected && st.AutoEnabledUnsaved {
		notices = append(notices, tr(strGAHintNotPersisted))
	}
	if st.Connected && st.ConfigDir != "" && dir != "" && filepath.Clean(st.ConfigDir) != filepath.Clean(dir) {
		notices = append(notices, fmt.Sprintf(tr(strGAConfigDirMismatch), st.ConfigDir, dir))
	}
	var warnings []string
	for _, warning := range slices.Concat(loadWarnings, st.ConfigWarnings) {
		if warning != "" && !slices.Contains(warnings, warning) {
			warnings = append(warnings, warning)
		}
	}
	if len(warnings) > 0 {
		notices = append(notices, fmt.Sprintf(tr(strGAConfigWarnings), strings.Join(warnings, "; ")))
	}
	return notices
}

// onAutoEnabledToggled acts immediately (not on Apply), like the tray menu.
func (w *SettingsWindow) onAutoEnabledToggled() {
	a := w.auto
	if a == nil || a.enabledUpdating {
		return
	}
	enabled := a.autoEnabledCheck.GetActive()
	dir := a.dir
	app := w.app
	go func() {
		var err error
		if app != nil {
			err = app.SetAutoEnabled(enabled)
		} else {
			err = userconfig.SetAutoEnabled(dir, enabled)
		}
		glib.IdleAdd(func() {
			if w.window == nil {
				return
			}
			if err != nil {
				w.showErrorDialog(tr(strGAErrorToggle), err.Error())
			} else if autoToggleWroteFile(app != nil, w.auto.daemon) {
				w.auto.fileEnabled = enabled
			}
			w.refreshAutoStatus()
		})
	}()
}

// autoToggleWroteFile reports whether a successful toggle from the window
// went to auto.conf (no daemon connected; always without an App): the
// checkbox must then show the written value at once instead of flipping back
// to the stale file value until the watcher reports the write (and for good
// when no watcher runs). While a daemon is connected the checkbox follows its
// status.
func autoToggleWroteFile(hasApp bool, daemon ipc.ClientStatus) bool {
	return !hasApp || !daemon.Connected
}

// autoApplyPlan is the user-level part of an Apply: auto.conf keys that
// changed against the snapshot (never auto-enabled) and list diffs. A skip
// plan (the page was never loaded) writes nothing.
type autoApplyPlan struct {
	skip                    bool
	dir                     string
	values                  map[string]string
	wordsAdd, wordsRemove   []string
	appsAdd, appsRemove     []string
	settings                userconfig.Settings
	words, apps             []string
	loadedWords, loadedApps []string
}

func (p *autoApplyPlan) changed() bool {
	return len(p.values) > 0 || len(p.wordsAdd) > 0 || len(p.wordsRemove) > 0 ||
		len(p.appsAdd) > 0 || len(p.appsRemove) > 0
}

// planAutoApply validates the edited settings and lists and computes what
// Apply writes. words and apps are the editor entries (normalized by the
// editors); loadedWords and loadedApps the snapshot entries.
func planAutoApply(dir string, loaded, edited userconfig.Settings, words, apps, loadedWords, loadedApps []string) (*autoApplyPlan, error) {
	if err := edited.Validate(); err != nil {
		if errors.Is(err, userconfig.ErrChordOverlap) {
			return nil, errors.New(tr(strGAChordOverlap))
		}
		return nil, err
	}
	normWords, err := normalizeListEntries(words, userconfig.NormalizeWord)
	if err != nil {
		return nil, err
	}
	normApps, err := normalizeListEntries(apps, userconfig.NormalizeApp)
	if err != nil {
		return nil, err
	}
	values := userconfig.DiffSettings(loaded, edited)
	delete(values, userconfig.KeyAutoEnabled) // written only through SetAutoEnabled
	plan := &autoApplyPlan{
		dir:         dir,
		values:      values,
		settings:    edited,
		words:       normWords,
		apps:        normApps,
		loadedWords: slices.Clone(loadedWords),
		loadedApps:  slices.Clone(loadedApps),
	}
	plan.wordsAdd, plan.wordsRemove = listEditorDiff(loadedWords, normWords)
	plan.appsAdd, plan.appsRemove = listEditorDiff(loadedApps, normApps)
	return plan, nil
}

// autoReloadError reports that Apply wrote the files but the daemon refused
// to reload them.
type autoReloadError struct{ err error }

func (e *autoReloadError) Error() string { return e.err.Error() }
func (e *autoReloadError) Unwrap() error { return e.err }

// executeAutoApply writes a plan into the user directory and asks the daemon
// to reload. It never calls pkexec and never restarts the service. An
// *autoReloadError means the files were written but the daemon refused them;
// any other error means nothing (or not everything) was written. A missing
// daemon is not an error (it reads the files at start or through its
// watcher).
func executeAutoApply(plan *autoApplyPlan, reload func() error) error {
	if plan == nil || plan.skip {
		return nil
	}
	if plan.dir == "" {
		return errors.New("no user configuration directory")
	}
	// Seed missing files (e.g. after `rm -r ~/.config/gswitch`).
	if err := userconfig.EnsureFiles(plan.dir); err != nil {
		return err
	}
	if len(plan.values) > 0 {
		if err := userconfig.Update(plan.dir, maps.Clone(plan.values)); err != nil {
			return err
		}
	}
	if len(plan.wordsAdd) > 0 || len(plan.wordsRemove) > 0 {
		if err := userconfig.ApplyListDiff(plan.dir, userconfig.WordsName, plan.wordsAdd, plan.wordsRemove); err != nil {
			return err
		}
	}
	if len(plan.appsAdd) > 0 || len(plan.appsRemove) > 0 {
		if err := userconfig.ApplyListDiff(plan.dir, userconfig.AppsName, plan.appsAdd, plan.appsRemove); err != nil {
			return err
		}
	}
	if plan.changed() && reload != nil {
		if err := reload(); err != nil && !errors.Is(err, ipc.ErrNotConnected) {
			return &autoReloadError{err: err}
		}
	}
	return nil
}

// prepareAutoApply validates the page on the GTK thread. cfg holds the
// upstream values being applied in the same click (layout switch and convert
// key, which the chords must not contain).
func (w *SettingsWindow) prepareAutoApply(cfg *TrayConfig) (*autoApplyPlan, error) {
	a := w.auto
	if a == nil || !a.loaded {
		return &autoApplyPlan{skip: true}, nil
	}
	toggle, selection := w.hotkeyChords()
	layoutSwitch, convertKey := w.hotkeyConstraints(cfg)
	// A problem in unchanged chords does not block (the row shows it as a
	// warning and the daemon disables such a chord at run time).
	if text := validateHotkeys(toggle, selection, layoutSwitch, convertKey, a.accels); text != "" &&
		hotkeyApplyMustValidate(a.loadedSettings, toggle, selection, w.loadedConfig, cfg) {
		return nil, errors.New(text)
	}
	return planAutoApply(a.dir, a.loadedSettings, w.collectAutoSettings(),
		a.words.entries(), a.apps.entries(), a.words.loadedEntries(), a.apps.loadedEntries())
}

// runAutoApply executes a plan off the GTK thread and reports the result on
// it. It returns false when saving failed (the window then stays open).
func (w *SettingsWindow) runAutoApply(plan *autoApplyPlan) bool {
	if plan == nil || plan.skip {
		return true
	}
	var reload func() error
	if w.app != nil {
		reload = w.app.ReloadDaemon
	}
	var saveErr, reloadErr error
	var refused *autoReloadError
	if err := executeAutoApply(plan, reload); errors.As(err, &refused) {
		reloadErr = refused.err
	} else {
		saveErr = err
	}
	var cfg *userconfig.Config
	var loadErr error
	if saveErr == nil {
		cfg, loadErr = userconfig.Load(plan.dir)
	}
	glib.IdleAdd(func() {
		if w.window == nil {
			return
		}
		if saveErr != nil {
			w.showErrorDialog(tr(strGAErrorSave), saveErr.Error())
			return
		}
		// The new snapshot is the merged file content (words learned while
		// the window was open included).
		w.applyLoadedAutoConfig(cfg, loadErr)
		if reloadErr != nil {
			w.showWarningDialog(fmt.Sprintf(tr(strGAErrorApply), reloadErr.Error()), "")
		}
	})
	return saveErr == nil
}

// Service section extras: Start/Stop beside Restart, sensitivity by status
// and a 2 s refresh while the window is visible.

const serviceRefreshIntervalMs = 2000

// serviceButtonSensitivity returns the sensitivity of Start, Stop and
// Restart for a service status.
func serviceButtonSensitivity(status ServiceStatus) (start, stop, restart bool) {
	switch status {
	case StatusRunning:
		return false, true, true
	case StatusStopped, StatusFailed:
		return true, false, true
	case StatusNotInstalled:
		return false, false, false
	default:
		return true, true, true
	}
}

func (w *SettingsWindow) createServiceButtons() (*gtk.Box, error) {
	box, err := gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 6)
	if err != nil {
		return nil, err
	}
	if w.startButton, err = gtk.ButtonNewWithLabel(tr(strButtonStart)); err != nil {
		return nil, err
	}
	w.startButton.Connect("clicked", func() { w.runServiceAction(w.serviceManager.Start, strErrorStartFailed) })
	if w.stopButton, err = gtk.ButtonNewWithLabel(tr(strButtonStop)); err != nil {
		return nil, err
	}
	w.stopButton.Connect("clicked", func() { w.runServiceAction(w.serviceManager.Stop, strErrorStopFailed) })
	box.PackStart(w.startButton, false, false, 0)
	box.PackStart(w.stopButton, false, false, 0)
	if w.restartButton != nil {
		box.PackStart(w.restartButton, false, false, 0)
	}
	return box, nil
}

// updateServiceButtons applies serviceButtonSensitivity.
func (w *SettingsWindow) updateServiceButtons(status ServiceStatus, err error) {
	if err != nil {
		status = StatusUnknown
	}
	start, stop, restart := serviceButtonSensitivity(status)
	if w.startButton != nil {
		w.startButton.SetSensitive(start)
	}
	if w.stopButton != nil {
		w.stopButton.SetSensitive(stop)
	}
	if w.restartButton != nil {
		w.restartButton.SetSensitive(restart)
	}
}

// runServiceAction runs a systemctl action off the GTK thread, like
// onRestartClicked.
func (w *SettingsWindow) runServiceAction(action func() error, failure string) {
	w.beginServiceAction()
	go func() {
		err := action()
		glib.IdleAdd(func() {
			w.endServiceAction()
			if w.window == nil {
				return
			}
			if err != nil && !errors.Is(err, ErrUserCanceled) {
				w.showErrorDialog(tr(failure), err.Error())
			}
			w.updateServiceStatus()
			if w.app != nil {
				w.app.UpdateServiceStatus()
			}
		})
	}()
}

// startServiceRefresh polls the service state every 2 s while the window is
// visible; the timer ends itself when the window is hidden or destroyed.
func (w *SettingsWindow) startServiceRefresh() {
	if w.serviceRefreshActive {
		return
	}
	w.serviceRefreshActive = true
	glib.TimeoutAdd(serviceRefreshIntervalMs, func() bool {
		if w.window == nil || !w.window.IsVisible() {
			w.serviceRefreshActive = false
			return false
		}
		w.refreshServiceStatusAsync()
		return true
	})
}

// beginServiceAction and endServiceAction bracket a user action (autostart
// toggle, Start/Stop/Restart) on the GTK thread, so that the periodic refresh
// neither starts nor applies a state read while the action may still be
// running (a stale is-enabled would untick the Autostart box the user just
// ticked and invite a second click that undoes it).
func (w *SettingsWindow) beginServiceAction() {
	w.serviceActions++
	w.serviceActionGen++
}

func (w *SettingsWindow) endServiceAction() {
	if w.serviceActions > 0 {
		w.serviceActions--
	}
}

// serviceRefreshCurrent reports whether a refresh requested at action
// generation requested may be applied now: no user action is running and
// none started since the request (each action shows its own result when it
// ends, and the next tick reads the state again).
func serviceRefreshCurrent(requested, gen uint64, running int) bool {
	return running == 0 && requested == gen
}

// refreshServiceStatusAsync reads the service state off the GTK thread.
func (w *SettingsWindow) refreshServiceStatusAsync() {
	if w.serviceRefreshBusy || w.serviceActions > 0 {
		return
	}
	w.serviceRefreshBusy = true
	requested := w.serviceActionGen
	manager := w.serviceManager
	go func() {
		status, err := manager.GetStatus()
		enabled, _ := manager.IsEnabled()
		glib.IdleAdd(func() {
			w.serviceRefreshBusy = false
			if w.window == nil || !serviceRefreshCurrent(requested, w.serviceActionGen, w.serviceActions) {
				return
			}
			w.applyServiceStatusWidgets(status, enabled, err)
		})
	}()
}
