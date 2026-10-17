package tray

import (
	"bytes"
	"testing"
)

type recordingTrayBackend struct {
	icon    []byte
	titles  []string
	tooltip string
	items   []*recordingTrayMenuItem
}

func (*recordingTrayBackend) Run(onReady, onExit func()) error {
	onReady()
	onExit()
	return nil
}

func (*recordingTrayBackend) Quit() {}

func (b *recordingTrayBackend) SetIcon(icon []byte) {
	b.icon = append(b.icon[:0], icon...)
}

func (b *recordingTrayBackend) SetTitle(title string) {
	b.titles = append(b.titles, title)
}

func (b *recordingTrayBackend) SetTooltip(tooltip string) {
	b.tooltip = tooltip
}

//nolint:ireturn // The fake implements the production trayBackend contract.
func (b *recordingTrayBackend) AddMenuItem(title, tooltip string) trayMenuItem {
	item := &recordingTrayMenuItem{clicks: make(chan struct{}), title: title, tooltip: tooltip, enabled: true}
	b.items = append(b.items, item)
	return item
}

func (*recordingTrayBackend) AddSeparator() {}

type recordingTrayMenuItem struct {
	clicks     chan struct{}
	title      string
	tooltip    string
	enabled    bool
	checked    bool
	checkCalls int
}

func (m *recordingTrayMenuItem) Clicks() <-chan struct{} {
	return m.clicks
}

func (m *recordingTrayMenuItem) SetTitle(title string)     { m.title = title }
func (m *recordingTrayMenuItem) SetTooltip(tooltip string) { m.tooltip = tooltip }

func (m *recordingTrayMenuItem) Enable() { m.enabled = true }

func (m *recordingTrayMenuItem) Disable() { m.enabled = false }

// SetChecked records check-mark updates (checkableTrayMenuItem).
func (m *recordingTrayMenuItem) SetChecked(checked bool) {
	m.checked = checked
	m.checkCalls++
}

func TestTrayInitialAppearanceUsesStableTitle(t *testing.T) {
	t.Parallel()

	backend := &recordingTrayBackend{}
	tray := &Tray{backend: backend}

	tray.setInitialAppearance()

	if !bytes.Equal(backend.icon, GetNormalIcon(TrayIconModeApp, "")) {
		t.Fatal("initial icon is not the application icon")
	}
	if len(backend.titles) != 1 || backend.titles[0] != trayApplicationID {
		t.Fatalf("initial titles = %v, want [%q]", backend.titles, trayApplicationID)
	}
	if backend.tooltip == "" {
		t.Fatal("initial tooltip is empty")
	}
}
