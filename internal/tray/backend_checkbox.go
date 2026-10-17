package tray

// Optional tray backend capabilities used by the automatic correction menu
// item. backend.go stays unchanged: a backend that does not implement
// checkboxTrayBackend (XEmbed) gets a plain item whose title carries the
// state (strGAMenuOn/strGAMenuOff).

// checkableTrayMenuItem is a menu item with a check mark.
type checkableTrayMenuItem interface {
	trayMenuItem
	SetChecked(bool)
}

// checkboxTrayBackend creates checkable menu items.
type checkboxTrayBackend interface {
	AddMenuItemCheckbox(title, tooltip string, checked bool) trayMenuItem
}
