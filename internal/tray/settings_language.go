package tray

import (
	"errors"

	"github.com/gotk3/gotk3/gtk"

	"github.com/arumata/gswitch/internal/i18n"
)

func (w *SettingsWindow) addLanguageSelector(grid *gtk.Grid) error {
	label, err := gtk.LabelNew(tr(strLabelLanguage))
	if err != nil {
		return err
	}
	label.SetHAlign(gtk.ALIGN_START)
	grid.SetRowSpacing(10)
	grid.Attach(label, 0, 1, 1, 1)
	w.languageCombo, err = gtk.ComboBoxTextNew()
	if err != nil {
		return err
	}
	w.languageCombo.Append("auto", tr(strLanguageAuto))
	for _, language := range i18n.Builtin.Languages() {
		w.languageCombo.Append(language.Code, language.Name)
	}
	w.languageCombo.SetActiveID("auto")
	w.languageCombo.SetHExpand(true)
	grid.Attach(w.languageCombo, 1, 1, 1, 1)
	return nil
}

func (w *SettingsWindow) setLanguageSelection(language string) {
	if w.languageCombo != nil && !w.languageCombo.SetActiveID(language) {
		w.languageCombo.SetActiveID(currentLanguage().Code())
	}
}

func (w *SettingsWindow) selectedLanguage() (string, error) {
	if w.languageCombo == nil {
		return "", errors.New(tr(strInvalidLanguage))
	}
	choice := w.languageCombo.GetActiveID()
	if choice == "auto" {
		return choice, nil
	}
	for _, language := range i18n.Builtin.Languages() {
		if language.Code == choice {
			return choice, nil
		}
	}
	return "", errors.New(tr(strInvalidLanguage))
}

func (w *SettingsWindow) saveInterfacePreferences(prefs UIPreferences) error {
	if err := SaveUIPreferences(prefs); err != nil {
		return err
	}
	selectInterfaceLanguage(prefs.Language)
	if w.app != nil {
		w.app.UpdateTrayIconMode(prefs.IconMode)
		w.app.mu.Lock()
		tray := w.app.tray
		w.app.mu.Unlock()
		if tray != nil {
			tray.UpdateLanguage()
		}
	}
	return nil
}

// UpdateLanguage serializes menu translation with status and layout updates.
func (t *Tray) UpdateLanguage() { t.enqueueUIReliable(t.applyLanguage) }

func (t *Tray) applyLanguage() {
	for _, entry := range []struct {
		item        trayMenuItem
		title, hint string
	}{
		{t.mSettings, strMenuSettings, strMenuSettingsHint},
		{t.mQuit, strMenuQuit, strMenuQuitHint},
		{t.mRefresh, strTrayRefresh, strMenuRefreshHint},
	} {
		if entry.item != nil {
			entry.item.SetTitle(tr(entry.title))
			entry.item.SetTooltip(tr(entry.hint))
		}
	}
	if t.mServiceStatus != nil {
		t.mServiceStatus.SetTooltip(tr(strMenuServiceHint))
	}
	if t.mServiceAction != nil {
		t.mServiceAction.SetTooltip(tr(strMenuActionHint))
	}
	t.renderAutoMenuItem()
	status := t.currentServiceStatus
	t.currentServiceStatus = ServiceStatus(-1)
	t.applyServiceStatus(status)
	t.applyDetectionStatus(t.detectionInfo)
	if t.latestLayout != nil {
		t.currentCode = ""
		t.applyLayout(*t.latestLayout)
	}
}
