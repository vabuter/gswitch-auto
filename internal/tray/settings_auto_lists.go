package tray

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gotk3/gotk3/glib"
	"github.com/gotk3/gotk3/gtk"

	"github.com/arumata/gswitch/internal/gswitch/focus"
	"github.com/arumata/gswitch/internal/gswitch/userconfig"
)

const runningAppsTimeout = 2 * time.Second

// normalizeListEntries normalizes editor entries, drops duplicates and fails
// on the first invalid entry.
func normalizeListEntries(entries []string, normalize func(string) (string, bool)) ([]string, error) {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		normalized, ok := normalize(entry)
		if !ok {
			return nil, fmt.Errorf(tr(strGAInvalidEntry), entry)
		}
		if !slices.Contains(out, normalized) {
			out = append(out, normalized)
		}
	}
	return out, nil
}

// listEditorDiff returns the entries to append (in editor order) and to
// remove, comparing the editor with the snapshot it was loaded from.
func listEditorDiff(loaded, current []string) (add, remove []string) {
	for _, entry := range current {
		if !slices.Contains(loaded, entry) && !slices.Contains(add, entry) {
			add = append(add, entry)
		}
	}
	for _, entry := range loaded {
		if !slices.Contains(current, entry) && !slices.Contains(remove, entry) {
			remove = append(remove, entry)
		}
	}
	return add, remove
}

// appPickerEntry returns the exclusion entry for a running application: the
// normalized Shell app id, or the WM_CLASS when the id is empty (windows
// without a .desktop file, "window:N" ids).
func appPickerEntry(app focus.App) (string, bool) {
	if app.ID != "" && !strings.HasPrefix(app.ID, focusExtWindowAppID) {
		return userconfig.NormalizeApp(app.ID)
	}
	return userconfig.NormalizeApp(app.WmClass)
}

// desktopAppDirs lists the applications directories of the XDG data dirs,
// the user's first.
func desktopAppDirs() []string {
	var dirs []string
	if dataHome := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(dataHome) {
		dirs = append(dirs, filepath.Join(dataHome, "applications"))
	} else if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs, filepath.Join(home, ".local", "share", "applications"))
	}
	dataDirs := os.Getenv("XDG_DATA_DIRS")
	if dataDirs == "" {
		dataDirs = "/usr/local/share:/usr/share"
	}
	for _, dir := range strings.Split(dataDirs, ":") {
		if filepath.IsAbs(dir) {
			dirs = append(dirs, filepath.Join(dir, "applications"))
		}
	}
	return dirs
}

// scanDesktopApps lists visible applications from .desktop files (the
// fallback of "Add running…" when the focus extension is unavailable). The
// first directory that provides an id wins; the result is sorted by name.
func scanDesktopApps(dirs []string) []focus.App {
	seen := map[string]bool{}
	var apps []focus.App
	for _, dir := range dirs {
		matches, err := filepath.Glob(filepath.Join(dir, "*.desktop"))
		if err != nil {
			continue
		}
		slices.Sort(matches)
		for _, path := range matches {
			id := strings.TrimSuffix(filepath.Base(path), ".desktop")
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			name, wmClass, visible := readDesktopEntry(path)
			if !visible {
				continue
			}
			if name == "" {
				name = id
			}
			apps = append(apps, focus.App{ID: id + ".desktop", Name: name, WmClass: wmClass})
		}
	}
	slices.SortStableFunc(apps, func(a, b focus.App) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return apps
}

// readDesktopEntry reads Name, StartupWMClass and visibility from the
// [Desktop Entry] group of a .desktop file.
func readDesktopEntry(path string) (name, wmClass string, visible bool) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", "", false
	}
	defer f.Close()
	visible = true
	isApp := false
	inEntry := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") {
			inEntry = line == "[Desktop Entry]"
			continue
		}
		if !inEntry {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "Name":
			name = value
		case "StartupWMClass":
			wmClass = value
		case "Type":
			isApp = value == "Application"
		case "NoDisplay", "Hidden":
			if strings.EqualFold(value, "true") {
				visible = false
			}
		}
	}
	return name, wmClass, visible && isApp
}

// focusExtStatusText describes the extension state in the "Excluded
// applications" frame.
func focusExtStatusText(info FocusExtensionInfo) string {
	switch info.State {
	case FocusExtActive:
		return tr(strGAExtActive)
	case FocusExtDisabled:
		return tr(strGAExtDisabled)
	case FocusExtNeedsRelogin:
		return tr(strGAExtNeedsRelogin)
	case FocusExtNotInstalled:
		return tr(strGAExtNotInstalled)
	case FocusExtOutOfDate:
		return tr(strGAExtOutOfDate)
	case FocusExtNoShell:
		return tr(strGAExtNotGnome)
	default:
		text := tr(strGAExtError)
		if len(info.Errors) > 0 {
			text += ": " + strings.Join(info.Errors, "; ")
		}
		return text
	}
}

// focusExtActionLabel returns the label of the enable button, or "" when the
// state offers no action (active, not installed, out of date, not GNOME).
func focusExtActionLabel(state FocusExtensionState) string {
	switch state {
	case FocusExtDisabled, FocusExtError:
		return tr(strGAButtonEnableExtension)
	case FocusExtNeedsRelogin:
		return tr(strGAButtonEnableNextLogin)
	default:
		return ""
	}
}

// autoListEditor edits one exception list: a TreeView over a
// ListStore(string) with editable cells, an entry with Add, and Remove.
type autoListEditor struct {
	window    *SettingsWindow
	normalize func(string) (string, bool)
	store     *gtk.ListStore
	view      *gtk.TreeView
	renderer  *gtk.CellRendererText
	column    *gtk.TreeViewColumn
	entry     *gtk.Entry
	message   *gtk.Label
	loaded    []string // snapshot entries (normalized)
}

func (w *SettingsWindow) newAutoListEditor(normalize func(string) (string, bool)) (*autoListEditor, *gtk.Box, error) {
	e := &autoListEditor{window: w, normalize: normalize}
	box, err := gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 6)
	if err != nil {
		return nil, nil, err
	}
	if e.store, err = gtk.ListStoreNew(glib.TYPE_STRING); err != nil {
		return nil, nil, err
	}
	if e.view, err = gtk.TreeViewNewWithModel(e.store); err != nil {
		return nil, nil, err
	}
	e.view.SetHeadersVisible(false)
	if e.renderer, err = gtk.CellRendererTextNew(); err != nil {
		return nil, nil, err
	}
	if err := e.renderer.SetProperty("editable", true); err != nil {
		return nil, nil, err
	}
	e.renderer.Connect("edited", func(_ any, path, text string) {
		e.onEdited(path, text)
	})
	if e.column, err = gtk.TreeViewColumnNewWithAttribute("", e.renderer, "text", 0); err != nil {
		return nil, nil, err
	}
	e.view.AppendColumn(e.column)
	scrolled, err := gtk.ScrolledWindowNew(nil, nil)
	if err != nil {
		return nil, nil, err
	}
	scrolled.SetPolicy(gtk.POLICY_NEVER, gtk.POLICY_AUTOMATIC)
	scrolled.SetShadowType(gtk.SHADOW_IN)
	scrolled.SetSizeRequest(-1, 120)
	scrolled.Add(e.view)
	box.PackStart(scrolled, true, true, 0)

	row, err := gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 6)
	if err != nil {
		return nil, nil, err
	}
	if e.entry, err = gtk.EntryNew(); err != nil {
		return nil, nil, err
	}
	e.entry.SetHExpand(true)
	e.entry.Connect("activate", e.onAddClicked)
	row.PackStart(e.entry, true, true, 0)
	addButton, err := gtk.ButtonNewWithLabel(tr(strGAButtonAdd))
	if err != nil {
		return nil, nil, err
	}
	addButton.Connect("clicked", e.onAddClicked)
	row.PackStart(addButton, false, false, 0)
	removeButton, err := gtk.ButtonNewWithLabel(tr(strGAButtonRemove))
	if err != nil {
		return nil, nil, err
	}
	removeButton.Connect("clicked", e.removeSelected)
	row.PackStart(removeButton, false, false, 0)
	box.PackStart(row, false, false, 0)

	if e.message, err = newWrappedLabel(); err != nil {
		return nil, nil, err
	}
	e.message.SetNoShowAll(true)
	if style, _ := e.message.GetStyleContext(); style != nil {
		style.AddClass("error")
	}
	box.PackStart(e.message, false, false, 0)
	return e, box, nil
}

// entries returns the editor rows in order.
func (e *autoListEditor) entries() []string {
	if e == nil || e.store == nil {
		return nil
	}
	var out []string
	iter, ok := e.store.GetIterFirst()
	for ok {
		if value, err := e.store.GetValue(iter, 0); err == nil {
			if text, err := value.GetString(); err == nil {
				out = append(out, text)
			}
		}
		ok = e.store.IterNext(iter)
	}
	return out
}

func (e *autoListEditor) loadedEntries() []string {
	if e == nil {
		return nil
	}
	return slices.Clone(e.loaded)
}

// load replaces the rows and the snapshot.
func (e *autoListEditor) load(entries []string) {
	if e == nil || e.store == nil {
		return
	}
	e.loaded = slices.Clone(entries)
	e.store.Clear()
	for _, entry := range entries {
		_ = e.store.SetValue(e.store.Append(), 0, entry)
	}
	e.showMessage("")
}

// dirty reports unsaved edits.
func (e *autoListEditor) dirty() bool {
	if e == nil {
		return false
	}
	add, remove := listEditorDiff(e.loaded, e.entries())
	return len(add) > 0 || len(remove) > 0
}

// add appends a normalized entry unless it is already listed.
func (e *autoListEditor) add(text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	normalized, ok := e.normalize(text)
	if !ok {
		e.showMessage(fmt.Sprintf(tr(strGAInvalidEntry), text))
		return false
	}
	e.showMessage("")
	if slices.Contains(e.entries(), normalized) {
		return true
	}
	_ = e.store.SetValue(e.store.Append(), 0, normalized)
	return true
}

func (e *autoListEditor) showMessage(text string) {
	if e.message == nil {
		return
	}
	e.message.SetText(text)
	e.message.SetVisible(text != "")
}

func (e *autoListEditor) onAddClicked() {
	text, err := e.entry.GetText()
	if err != nil {
		return
	}
	if e.add(text) {
		e.entry.SetText("")
	}
}

func (e *autoListEditor) removeSelected() {
	selection, err := e.view.GetSelection()
	if err != nil {
		return
	}
	_, iter, ok := selection.GetSelected()
	if !ok {
		return
	}
	e.store.Remove(iter)
}

// onEdited validates an in-place edit: an emptied row is removed, an invalid
// value is rejected, a duplicate collapses into the existing row.
func (e *autoListEditor) onEdited(path, text string) {
	iter, err := e.store.GetIterFromString(path)
	if err != nil {
		return
	}
	if strings.TrimSpace(text) == "" {
		e.store.Remove(iter)
		return
	}
	normalized, ok := e.normalize(text)
	if !ok {
		e.showMessage(fmt.Sprintf(tr(strGAInvalidEntry), text))
		return
	}
	e.showMessage("")
	for i, entry := range e.entries() {
		if entry == normalized && strconv.Itoa(i) != path {
			e.store.Remove(iter)
			return
		}
	}
	_ = e.store.SetValue(iter, 0, normalized)
}

// createWordsFrame builds the "Exception words" frame.
func (w *SettingsWindow) createWordsFrame() (*gtk.Frame, error) {
	frame, grid, err := newAutoFrame(tr(strGASectionWords))
	if err != nil {
		return nil, err
	}
	hint, err := newHintLabel(tr(strGAWordsHint))
	if err != nil {
		return nil, err
	}
	grid.Attach(hint, 0, 0, 1, 1)
	editor, box, err := w.newAutoListEditor(userconfig.NormalizeWord)
	if err != nil {
		return nil, err
	}
	box.SetHExpand(true)
	w.auto.words = editor
	grid.Attach(box, 0, 1, 1, 1)
	if w.auto.addLastButton, err = gtk.ButtonNewWithLabel(tr(strGAButtonAddLast)); err != nil {
		return nil, err
	}
	w.auto.addLastButton.SetHAlign(gtk.ALIGN_START)
	w.auto.addLastButton.SetSensitive(false)
	w.auto.addLastButton.Connect("clicked", w.onAddLastCorrectedClicked)
	grid.Attach(w.auto.addLastButton, 0, 2, 1, 1)
	return frame, nil
}

// onAddLastCorrectedClicked adds the last corrected word as it was shown
// before the correction (exceptions match the on-screen word).
func (w *SettingsWindow) onAddLastCorrectedClicked() {
	if word := w.auto.daemon.Last.From; word != "" {
		w.auto.words.add(word)
	}
}

// createAppsFrame builds the "Excluded applications" frame with the focus
// extension status row.
func (w *SettingsWindow) createAppsFrame() (*gtk.Frame, error) {
	frame, grid, err := newAutoFrame(tr(strGASectionApps))
	if err != nil {
		return nil, err
	}
	a := w.auto
	statusRow, err := gtk.BoxNew(gtk.ORIENTATION_HORIZONTAL, 10)
	if err != nil {
		return nil, err
	}
	if a.extLabel, err = newWrappedLabel(); err != nil {
		return nil, err
	}
	a.extLabel.SetHExpand(true)
	a.extLabel.SetText(tr(strStatusUnknown))
	statusRow.PackStart(a.extLabel, true, true, 0)
	if a.extButton, err = gtk.ButtonNewWithLabel(tr(strGAButtonEnableExtension)); err != nil {
		return nil, err
	}
	a.extButton.SetNoShowAll(true)
	a.extButton.Connect("clicked", w.onEnableExtensionClicked)
	statusRow.PackStart(a.extButton, false, false, 0)
	grid.Attach(statusRow, 0, 0, 1, 1)

	if a.extHint, err = newHintLabel(tr(strGAAppsHint)); err != nil {
		return nil, err
	}
	a.extHint.SetNoShowAll(true)
	grid.Attach(a.extHint, 0, 1, 1, 1)

	editor, box, err := w.newAutoListEditor(userconfig.NormalizeApp)
	if err != nil {
		return nil, err
	}
	box.SetHExpand(true)
	a.apps = editor
	grid.Attach(box, 0, 2, 1, 1)
	addRunning, err := gtk.ButtonNewWithLabel(tr(strGAButtonAddRunning))
	if err != nil {
		return nil, err
	}
	addRunning.SetHAlign(gtk.ALIGN_START)
	addRunning.Connect("clicked", w.onAddRunningClicked)
	grid.Attach(addRunning, 0, 3, 1, 1)
	w.refreshExtensionRow()
	return frame, nil
}

// setFocusExtensionInfo shows the extension state (GTK thread).
func (w *SettingsWindow) setFocusExtensionInfo(info FocusExtensionInfo) {
	if w.auto == nil {
		return
	}
	w.auto.extInfo = info
	w.auto.extKnown = true
	w.refreshExtensionRow()
}

func (w *SettingsWindow) refreshExtensionRow() {
	a := w.auto
	if a.extLabel == nil {
		return
	}
	if !a.extKnown {
		a.extLabel.SetText(tr(strStatusUnknown))
		a.extButton.SetVisible(false)
		a.extHint.SetVisible(false)
		return
	}
	a.extLabel.SetText(focusExtStatusText(a.extInfo))
	label := focusExtActionLabel(a.extInfo.State)
	a.extButton.SetLabel(label)
	a.extButton.SetVisible(label != "")
	a.extHint.SetVisible(a.extInfo.State != FocusExtActive)
}

// onEnableExtensionClicked enables the extension through the Shell, or
// pre-enables it for the next login (C9).
func (w *SettingsWindow) onEnableExtensionClicked() {
	conn := w.app.ForkConn()
	w.auto.extButton.SetSensitive(false)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*runningAppsTimeout)
		needsRelogin, err := EnableFocusExtension(ctx, conn)
		var info FocusExtensionInfo
		var queryErr error
		if conn != nil {
			info, queryErr = QueryFocusExtension(ctx, conn)
		}
		cancel()
		glib.IdleAdd(func() {
			if w.window == nil {
				return
			}
			w.auto.extButton.SetSensitive(true)
			if conn != nil && queryErr == nil {
				w.setFocusExtensionInfo(info)
			}
			switch {
			case err != nil:
				w.showErrorDialog(tr(strGAErrorEnableExtension), err.Error())
			case needsRelogin:
				w.showInfoDialog(tr(strGAReloginDialog))
			}
		})
	}()
}

// showInfoDialog shows a modal information message.
func (w *SettingsWindow) showInfoDialog(message string) {
	dialog := gtk.MessageDialogNew(
		w.window,
		gtk.DIALOG_MODAL|gtk.DIALOG_DESTROY_WITH_PARENT,
		gtk.MESSAGE_INFO,
		gtk.BUTTONS_OK,
		"%s",
		message,
	)
	dialog.Run()
	dialog.Destroy()
}

// onAddRunningClicked lists running applications through the focus
// extension, or installed applications when it is unavailable.
func (w *SettingsWindow) onAddRunningClicked() {
	conn := w.app.ForkConn()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), runningAppsTimeout)
		apps, err := ListRunningAppsForPicker(ctx, conn)
		cancel()
		note := ""
		if err != nil {
			apps = scanDesktopApps(desktopAppDirs())
			note = fmt.Sprintf(tr(strGARunningAppsError), err.Error())
		}
		glib.IdleAdd(func() {
			if w.window == nil {
				return
			}
			w.showRunningAppsDialog(apps, note)
		})
	}()
}

// Columns of the running applications picker.
const (
	runningAppColumnChecked = iota
	runningAppColumnName
	runningAppColumnEntry
)

// showRunningAppsDialog lets the user tick several applications; the ticked
// ones are added to the exclusion editor (Apply saves them).
func (w *SettingsWindow) showRunningAppsDialog(apps []focus.App, note string) {
	dialog, err := gtk.DialogNew()
	if err != nil {
		return
	}
	defer dialog.Destroy()
	dialog.SetTitle(tr(strGARunningAppsTitle))
	dialog.SetTransientFor(w.window)
	dialog.SetModal(true)
	dialog.SetDefaultSize(460, 420)
	content, err := dialog.GetContentArea()
	if err != nil {
		return
	}
	content.SetSpacing(8)
	content.SetMarginStart(10)
	content.SetMarginEnd(10)
	content.SetMarginTop(10)
	if note != "" {
		if label, err := newHintLabel(note); err == nil {
			content.PackStart(label, false, false, 0)
		}
	}

	store, err := gtk.ListStoreNew(glib.TYPE_BOOLEAN, glib.TYPE_STRING, glib.TYPE_STRING)
	if err != nil {
		return
	}
	for _, app := range apps {
		entry, ok := appPickerEntry(app)
		if !ok {
			continue
		}
		name := app.Name
		if name == "" {
			name = entry
		}
		iter := store.Append()
		if err := store.Set(iter,
			[]int{runningAppColumnChecked, runningAppColumnName, runningAppColumnEntry},
			[]any{false, name, entry}); err != nil {
			return
		}
	}
	view, err := gtk.TreeViewNewWithModel(store)
	if err != nil {
		return
	}
	toggle, err := gtk.CellRendererToggleNew()
	if err != nil {
		return
	}
	toggle.Connect("toggled", func(_ any, path string) {
		iter, err := store.GetIterFromString(path)
		if err != nil {
			return
		}
		value, err := store.GetValue(iter, runningAppColumnChecked)
		if err != nil {
			return
		}
		current, _ := value.GoValue()
		checked, _ := current.(bool)
		_ = store.SetValue(iter, runningAppColumnChecked, !checked)
	})
	for _, column := range []struct {
		title     string
		renderer  gtk.ICellRenderer
		attribute string
		index     int
	}{
		{"", toggle, "active", runningAppColumnChecked},
		{tr(strGAColumnApp), nil, "text", runningAppColumnName},
		{tr(strGAColumnID), nil, "text", runningAppColumnEntry},
	} {
		renderer := column.renderer
		if renderer == nil {
			text, err := gtk.CellRendererTextNew()
			if err != nil {
				return
			}
			renderer = text
		}
		viewColumn, err := gtk.TreeViewColumnNewWithAttribute(column.title, renderer, column.attribute, column.index)
		if err != nil {
			return
		}
		view.AppendColumn(viewColumn)
	}
	scrolled, err := gtk.ScrolledWindowNew(nil, nil)
	if err != nil {
		return
	}
	scrolled.SetPolicy(gtk.POLICY_AUTOMATIC, gtk.POLICY_AUTOMATIC)
	scrolled.SetShadowType(gtk.SHADOW_IN)
	scrolled.Add(view)
	content.PackStart(scrolled, true, true, 0)

	if _, err := dialog.AddButton(tr(strButtonCancel), gtk.RESPONSE_CANCEL); err != nil {
		return
	}
	if _, err := dialog.AddButton(tr(strGAButtonAdd), gtk.RESPONSE_OK); err != nil {
		return
	}
	dialog.ShowAll()
	if dialog.Run() != gtk.RESPONSE_OK {
		return
	}
	iter, ok := store.GetIterFirst()
	for ok {
		checkedValue, err1 := store.GetValue(iter, runningAppColumnChecked)
		entryValue, err2 := store.GetValue(iter, runningAppColumnEntry)
		if err1 == nil && err2 == nil {
			current, _ := checkedValue.GoValue()
			entry, _ := entryValue.GetString()
			if checked, _ := current.(bool); checked && entry != "" {
				w.auto.apps.add(entry)
			}
		}
		ok = store.IterNext(iter)
	}
}
