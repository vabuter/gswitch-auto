package tray

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/arumata/gswitch/internal/gswitch/ipc"
	"github.com/arumata/gswitch/internal/gswitch/userconfig"
)

// gaTestCheckboxBackend adds checkable items to recordingTrayBackend, like
// the StatusNotifier backend.
type gaTestCheckboxBackend struct {
	*recordingTrayBackend
	checkboxItems []*recordingTrayMenuItem
}

//nolint:ireturn // The fake implements the production checkboxTrayBackend contract.
func (b *gaTestCheckboxBackend) AddMenuItemCheckbox(title, tooltip string, checked bool) trayMenuItem {
	item := &recordingTrayMenuItem{clicks: make(chan struct{}), title: title, tooltip: tooltip, enabled: true, checked: checked}
	b.checkboxItems = append(b.checkboxItems, item)
	return item
}

func gaTestUseLanguage(t *testing.T, language string) {
	t.Helper()
	old := interfaceLanguage.Load()
	t.Cleanup(func() { interfaceLanguage.Store(old) })
	selectInterfaceLanguage(language)
}

func gaTestStatus(enabled, available bool) ipc.ClientStatus {
	return ipc.ClientStatus{Connected: true, Status: ipc.Status{
		AutoEnabled: enabled, AutoAvailable: available, ToggleChord: "42+54",
	}}
}

func TestApplyAutoStateCheckbox(t *testing.T) {
	gaTestUseLanguage(t, "en")
	backend := &gaTestCheckboxBackend{recordingTrayBackend: &recordingTrayBackend{}}
	tray := &Tray{backend: backend, iconMode: TrayIconModeAppWithFlag,
		detectionInfo: DetectionInfo{Status: TrayStatusOK, KeyNames: "Alt+Shift", Source: "gnome"}}
	tray.addAutoMenuItem()
	if len(backend.checkboxItems) != 1 || len(backend.items) != 0 {
		t.Fatalf("items: checkbox=%d plain=%d, want one checkbox item", len(backend.checkboxItems), len(backend.items))
	}
	item := backend.checkboxItems[0]
	if tray.autoCheckable == nil {
		t.Fatal("checkbox item not used as checkable")
	}
	if item.title != tr(strGAMenuUnavailable) {
		t.Fatalf("initial title = %q", item.title)
	}

	layout := LayoutInfo{ShortCode: "US", LongName: "English (US)"}
	tray.applyLayout(layout)
	tray.applyAutoState(gaTestStatus(true, true))
	if !item.checked || item.title != tr(strGAMenuAuto) {
		t.Fatalf("enabled item: checked=%v title=%q", item.checked, item.title)
	}
	if !strings.Contains(item.tooltip, "LShift+RShift") {
		t.Fatalf("tooltip does not name the toggle chord: %q", item.tooltip)
	}
	if !bytes.Equal(backend.icon, GetNormalIconAuto(TrayIconModeAppWithFlag, "US", true)) {
		t.Fatal("enabled auto mode did not switch to the badged icon")
	}
	if bytes.Equal(backend.icon, GetNormalIcon(TrayIconModeAppWithFlag, "US")) {
		t.Fatal("badged icon equals the base icon")
	}
	if want := "US - English (US)\nAlt+Shift (gnome)\n" + tr(strGATooltipOn); backend.tooltip != want {
		t.Fatalf("tooltip = %q, want %q", backend.tooltip, want)
	}

	tray.applyAutoState(gaTestStatus(false, true))
	if item.checked || item.title != tr(strGAMenuAuto) {
		t.Fatalf("disabled item: checked=%v title=%q", item.checked, item.title)
	}
	if !bytes.Equal(backend.icon, GetNormalIcon(TrayIconModeAppWithFlag, "US")) {
		t.Fatal("disabled auto mode kept the badge")
	}
	if !strings.HasSuffix(backend.tooltip, "\n"+tr(strGATooltipOff)) {
		t.Fatalf("tooltip = %q, want off suffix", backend.tooltip)
	}

	// Not connected: no suffix, no badge, title "unavailable".
	tray.applyAutoState(ipc.ClientStatus{Status: ipc.Status{AutoEnabled: true}})
	if item.title != tr(strGAMenuUnavailable) || !item.checked {
		t.Fatalf("offline item: checked=%v title=%q", item.checked, item.title)
	}
	if backend.tooltip != "US - English (US)\nAlt+Shift (gnome)" {
		t.Fatalf("offline tooltip = %q", backend.tooltip)
	}
	if !bytes.Equal(backend.icon, GetNormalIcon(TrayIconModeAppWithFlag, "US")) {
		t.Fatal("offline state shows the badge")
	}
}

func TestApplyAutoStateTitleFallback(t *testing.T) {
	gaTestUseLanguage(t, "en")
	backend := &recordingTrayBackend{}
	tray := &Tray{backend: backend, iconMode: TrayIconModeApp}
	tray.addAutoMenuItem()
	if len(backend.items) != 1 || tray.autoCheckable != nil {
		t.Fatalf("plain backend: items=%d checkable=%v", len(backend.items), tray.autoCheckable != nil)
	}
	item := backend.items[0]
	for _, tt := range []struct {
		status ipc.ClientStatus
		title  string
	}{
		{gaTestStatus(true, true), tr(strGAMenuOn)},
		{gaTestStatus(false, true), tr(strGAMenuOff)},
		{gaTestStatus(true, false), tr(strGAMenuUnavailable)},
		{ipc.ClientStatus{}, tr(strGAMenuUnavailable)},
	} {
		tray.applyAutoState(tt.status)
		if item.title != tt.title {
			t.Errorf("status %+v: title %q, want %q", tt.status, item.title, tt.title)
		}
	}
	if item.checkCalls != 0 {
		t.Fatal("plain item received check-mark updates")
	}
}

func TestApplyAutoStateUnavailableAndWarningIcon(t *testing.T) {
	gaTestUseLanguage(t, "en")
	backend := &recordingTrayBackend{}
	tray := &Tray{backend: backend, iconMode: TrayIconModeApp,
		detectionInfo: DetectionInfo{Status: TrayStatusOK}}
	tray.applyAutoState(gaTestStatus(true, false))
	if !bytes.Equal(backend.icon, GetNormalIcon(TrayIconModeApp, "")) {
		t.Fatal("unavailable auto mode shows the badge")
	}
	if !strings.HasSuffix(backend.tooltip, "\n"+tr(strGATooltipOff)) {
		t.Fatalf("unavailable tooltip = %q", backend.tooltip)
	}
	tray.applyAutoState(gaTestStatus(true, true))
	if !bytes.Equal(backend.icon, GetNormalIconAuto(TrayIconModeApp, "", true)) {
		t.Fatal("no badge without a layout")
	}

	tray.applyDetectionStatus(DetectionInfo{Status: TrayStatusNeedsConfig})
	tray.applyAutoState(gaTestStatus(true, true))
	if !bytes.Equal(backend.icon, GetWarningIcon()) {
		t.Fatal("auto state replaced the warning icon")
	}
}

func TestApplyLanguageRetitlesAutoItem(t *testing.T) {
	gaTestUseLanguage(t, "en")
	backend := &iconModeRecordingTrayBackend{}
	item := &recordingTrayMenuItem{clicks: make(chan struct{})}
	tray := &Tray{backend: backend, mAutoToggle: item, autoStatus: gaTestStatus(false, true)}
	selectInterfaceLanguage("ru")
	tray.applyLanguage()
	if item.title != "Автоисправление: выкл." {
		t.Fatalf("Russian title = %q", item.title)
	}
	if !strings.HasPrefix(item.tooltip, "Включить или выключить") {
		t.Fatalf("Russian tooltip = %q", item.tooltip)
	}
	selectInterfaceLanguage("en")
	tray.applyLanguage()
	if item.title != "Auto correction: off" {
		t.Fatalf("English title = %q", item.title)
	}
}

func TestAutoPersistErrorReplacesItemTooltip(t *testing.T) {
	gaTestUseLanguage(t, "en")
	item := &recordingTrayMenuItem{clicks: make(chan struct{})}
	tray := &Tray{backend: &recordingTrayBackend{}, mAutoToggle: item}
	tray.autoPersistError = "boom"
	tray.renderAutoMenuItem()
	if item.tooltip != "boom" {
		t.Fatalf("tooltip = %q", item.tooltip)
	}
	tray.autoPersistError = ""
	tray.renderAutoMenuItem()
	if !strings.Contains(item.tooltip, tr(strGAMenuNoHotkey)) {
		t.Fatalf("tooltip after recovery = %q", item.tooltip)
	}
}

func TestAutoMenuClickTogglesAuto(t *testing.T) {
	scheduled := gaTestNoGTK(t)
	dir := t.TempDir()
	app := &App{userDir: dir}
	backend := &recordingTrayBackend{}
	tray := NewTray(app, backend)
	app.tray = tray
	tray.mServiceAction = &recordingTrayMenuItem{clicks: make(chan struct{})}
	tray.mRefresh = &recordingTrayMenuItem{clicks: make(chan struct{})}
	tray.mSettings = &recordingTrayMenuItem{clicks: make(chan struct{})}
	tray.mQuit = &recordingTrayMenuItem{clicks: make(chan struct{})}
	auto := &recordingTrayMenuItem{clicks: make(chan struct{})}
	tray.mAutoToggle = auto
	done := make(chan struct{})
	go func() {
		tray.handleEvents()
		close(done)
	}()
	defer func() {
		tray.onExit()
		<-done
	}()

	auto.clicks <- struct{}{}
	deadline := time.Now().Add(5 * time.Second)
	for {
		cfg, err := userconfig.Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Settings.AutoEnabled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("menu click did not toggle auto-enabled in auto.conf")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The toggle publishes the new state (tray queue and settings window).
	select {
	case <-scheduled:
	case <-time.After(5 * time.Second):
		t.Fatal("toggle did not publish the new state")
	}
}
