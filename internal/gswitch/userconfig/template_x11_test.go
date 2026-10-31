package userconfig

import (
	"testing"

	"github.com/arumata/gswitch/internal/gswitch/focus"
	"github.com/arumata/gswitch/internal/gswitch/fold"
)

// Strict purpose mode cannot protect X11 clients that never report a content
// type (XIM: xterm-like terminals, Emacs, Java, Wine) or never report the move
// to a password field (Qt): GNOME's panel sees IBus's default content type
// (FREE_FORM, no hints) and the extension reports "normal" (SPEC-X11 K-X4,
// K-X5). Their only protection is the exclusion list, so the default template
// lists them, matched by WM_CLASS class.
func TestAppsTemplateCoversUntrustedX11Clients(t *testing.T) {
	apps, warnings := parseList(listSpec{name: AppsName, normalize: NormalizeApp, globs: true}, appsTemplate)
	if len(warnings) > 0 {
		t.Fatalf("template warnings: %q", warnings)
	}
	strict := focus.Policy{Excluded: apps, SkipPassword: true, SkipTerminal: true, RequirePurpose: true}
	for _, wmClass := range []string{
		"XTerm", "URxvt", "Rxvt", "Emacs", "mlterm", "st-256color",
		"sun-awt-X11-XFramePeer", "notepad.exe", "explorer.exe",
		"pinentry-qt", "KeePassXC", "ksshaskpass",
	} {
		// What the extension reports for such a client: an IBus focus-in
		// followed only by the pushed (FREE_FORM, 0) content type.
		st := focus.State{
			Available:  true,
			WmClass:    wmClass,
			WmClassKey: fold.AppKey(wmClass),
			Purpose:    focus.PurposeNormal,
		}
		if ok, reason := strict.Allow(st); ok || reason != focus.ReasonExcludedApp {
			t.Errorf("WM_CLASS %q with purpose normal in strict mode: Allow = %v, %q; want denied %q",
				wmClass, ok, reason, focus.ReasonExcludedApp)
		}
	}
	// A GTK application that reports its fields keeps working.
	gedit := focus.State{Available: true, WmClass: "Gedit", WmClassKey: fold.AppKey("Gedit"), Purpose: focus.PurposeNormal}
	if ok, reason := strict.Allow(gedit); !ok {
		t.Errorf("gedit denied: %q", reason)
	}
}
