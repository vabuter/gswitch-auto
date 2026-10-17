package focus

import (
	"path"
	"reflect"
	"testing"

	"github.com/arumata/gswitch/internal/gswitch/fold"
)

// Compile-time: every Purpose* constant is assignable to Purpose. The reflect
// check in TestPurposeConstantsAreTyped is the one that catches an untyped
// constant, which would also pass these assignments.
var (
	_ Purpose = PurposeUnknown
	_ Purpose = PurposeNone
	_ Purpose = PurposeNormal
	_ Purpose = PurposePassword
	_ Purpose = PurposePIN
	_ Purpose = PurposeTerminal
)

func TestPurposeConstantsAreTyped(t *testing.T) {
	want := reflect.TypeFor[Purpose]()
	for _, value := range []any{
		PurposeUnknown, PurposeNone, PurposeNormal, PurposePassword, PurposePIN, PurposeTerminal,
	} {
		if got := reflect.TypeOf(value); got != want {
			t.Errorf("constant %q has type %v, want %v", value, got, want)
		}
	}
}

// focusTestMatcher records its arguments and matches exact keys or path.Match
// globs, normalising its arguments with fold.AppKey as the contract requires.
type focusTestMatcher struct {
	patterns []string
	calls    [][2]string
}

func (m *focusTestMatcher) MatchApp(appID, wmClass string) bool {
	if m == nil {
		return false
	}
	m.calls = append(m.calls, [2]string{appID, wmClass})
	for _, key := range []string{fold.AppKey(appID), fold.AppKey(wmClass)} {
		if key == "" {
			continue
		}
		for _, pattern := range m.patterns {
			if ok, err := path.Match(pattern, key); err == nil && ok {
				return true
			}
		}
	}
	return false
}

func focusTestDefaultPolicy(excluded AppMatcher) Policy {
	return Policy{
		Excluded:       excluded,
		SkipPassword:   true,
		SkipTerminal:   true,
		SkipWhenLocked: true,
	}
}

func focusTestState(appID, wmClass string, purpose Purpose) State {
	return State{
		Available:     true,
		EverAvailable: true,
		AppID:         appID,
		WmClass:       wmClass,
		AppKey:        fold.AppKey(appID),
		WmClassKey:    fold.AppKey(wmClass),
		Purpose:       purpose,
	}
}

func TestPolicyAllow(t *testing.T) {
	editor := focusTestState("org.gnome.TextEditor.desktop", "org.gnome.TextEditor", PurposeNormal)
	locked := editor
	locked.Locked, locked.LockReason = true, LockReasonExtensionLocked
	vanished := State{EverAvailable: true, Purpose: PurposeUnknown, Locked: true, LockReason: LockReasonExtensionVanished}
	screenSaver := editor
	screenSaver.Locked, screenSaver.LockReason = true, LockReasonScreenSaver
	absent := State{Purpose: PurposeUnknown}
	disabled := State{EverAvailable: true, Purpose: PurposeUnknown}
	lockedAbsent := State{Locked: true, LockReason: LockReasonScreenSaver}
	password := focusTestState("org.gnome.Settings.desktop", "gnome-control-center", PurposePassword)
	pin := focusTestState("org.gnome.Settings.desktop", "gnome-control-center", PurposePIN)
	terminal := focusTestState("org.gnome.Ptyxis.desktop", "org.gnome.Ptyxis", PurposeTerminal)
	emptyApp := focusTestState("", "", PurposeNormal)
	windowBacked := focusTestState("window:12", "Foo-Bar", PurposeNormal)
	files := focusTestState("org.gnome.Nautilus.desktop", "org.gnome.Nautilus", PurposeNormal)
	unknownFocus := State{Available: true, EverAvailable: true, Purpose: PurposeUnknown, FocusUnknown: true}
	lockedUnknown := unknownFocus
	lockedUnknown.Locked, lockedUnknown.LockReason = true, LockReasonScreenSaver

	defaults := focusTestDefaultPolicy(nil)
	permissive := Policy{}
	requireExt := defaults
	requireExt.RequireExtension = true
	excluded := focusTestDefaultPolicy(&focusTestMatcher{patterns: []string{"org.gnome.texteditor", "foo-*"}})

	tests := []struct {
		name   string
		policy Policy
		state  State
		ok     bool
		reason string
	}{
		{"normal field allowed", defaults, editor, true, ""},
		{"extension lock denies", defaults, locked, false, ReasonLocked},
		{"vanish without Disabling denies", defaults, vanished, false, ReasonLocked},
		{"screensaver lock denies", defaults, screenSaver, false, ReasonLocked},
		{"lock ignored without SkipWhenLocked", permissive, locked, true, ""},
		{"lock checked before availability", requireExt, lockedAbsent, false, ReasonLocked},
		{"extension absent fails open", defaults, absent, true, ""},
		{"extension disabled fails open", defaults, disabled, true, ""},
		{"extension absent with RequireExtension", requireExt, absent, false, ReasonFocusUnavailable},
		{"extension disabled with RequireExtension", requireExt, disabled, false, ReasonFocusUnavailable},
		{"available with RequireExtension", requireExt, editor, true, ""},
		{"password field denies", defaults, password, false, ReasonPasswordField},
		{"pin field denies", defaults, pin, false, ReasonPasswordField},
		{"password allowed without SkipPassword", permissive, password, true, ""},
		{"terminal denies", defaults, terminal, false, ReasonTerminal},
		{"terminal allowed without SkipTerminal", permissive, terminal, true, ""},
		{"empty app id allowed", excluded, emptyApp, true, ""},
		{"excluded app by exact key", excluded, editor, false, ReasonExcludedApp},
		{"excluded app by WM_CLASS glob", excluded, windowBacked, false, ReasonExcludedApp},
		{"not excluded", excluded, files, true, ""},
		{"terminal checked before exclusions", excluded, terminal, false, ReasonTerminal},
		{"nil Excluded allows", defaults, windowBacked, true, ""},
		// GetFocus failed after the extension appeared: fail closed until the
		// focus is known (the app could be excluded, the field a password).
		{"unknown focus denies", excluded, unknownFocus, false, ReasonFocusUnknown},
		{"unknown focus denies with a permissive policy", permissive, unknownFocus, false, ReasonFocusUnknown},
		{"lock checked before unknown focus", defaults, lockedUnknown, false, ReasonLocked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, reason := tt.policy.Allow(tt.state)
			if ok != tt.ok || reason != tt.reason {
				t.Fatalf("Allow() = (%v, %q), want (%v, %q)", ok, reason, tt.ok, tt.reason)
			}
		})
	}
}

func TestPolicyAllowPassesNormalisedKeys(t *testing.T) {
	matcher := &focusTestMatcher{}
	policy := focusTestDefaultPolicy(matcher)
	st := focusTestState("org.gnome.Ptyxis.desktop", "org.gnome.Ptyxis", PurposeNormal)

	if ok, _ := policy.Allow(st); !ok {
		t.Fatal("Allow() denied a state the matcher does not match")
	}
	want := [][2]string{{"org.gnome.ptyxis", "org.gnome.ptyxis"}}
	if !reflect.DeepEqual(matcher.calls, want) {
		t.Fatalf("MatchApp calls = %q, want %q", matcher.calls, want)
	}
}

func TestPolicyAllowSkipsMatcherWhenEarlierRuleDecides(t *testing.T) {
	matcher := &focusTestMatcher{patterns: []string{"*"}}
	policy := focusTestDefaultPolicy(matcher)
	for _, st := range []State{
		{Purpose: PurposeUnknown},
		focusTestState("org.gnome.Ptyxis.desktop", "org.gnome.Ptyxis", PurposeTerminal),
	} {
		policy.Allow(st)
	}
	if len(matcher.calls) != 0 {
		t.Fatalf("MatchApp called %d times, want 0", len(matcher.calls))
	}
}

func TestPolicyAllowNilPointerMatcher(t *testing.T) {
	var matcher *focusTestMatcher
	policy := focusTestDefaultPolicy(matcher)
	st := focusTestState("org.gnome.Ptyxis.desktop", "org.gnome.Ptyxis", PurposeNormal)
	if ok, reason := policy.Allow(st); !ok {
		t.Fatalf("Allow() = (false, %q) with a nil-pointer matcher", reason)
	}
}

func TestPolicyAllowDoesNotAllocate(t *testing.T) {
	policy := focusTestDefaultPolicy(focusTestNoopMatcher{})
	st := focusTestState("org.gnome.Ptyxis.desktop", "org.gnome.Ptyxis", PurposeNormal)
	allocs := testing.AllocsPerRun(100, func() {
		policy.Allow(st)
	})
	if allocs != 0 {
		t.Fatalf("Allow() allocated %.1f times per run", allocs)
	}
}

type focusTestNoopMatcher struct{}

func (focusTestNoopMatcher) MatchApp(appID, wmClass string) bool {
	return fold.AppKey(appID) == "" && fold.AppKey(wmClass) == ""
}

func TestStaticSource(t *testing.T) {
	st := focusTestState("a.desktop", "A", PurposeNormal)
	st.Seq = 7
	if got := Static(st).Snapshot(); got != st {
		t.Fatalf("Static().Snapshot() = %+v, want %+v", got, st)
	}
}

func TestKnownPurpose(t *testing.T) {
	for _, s := range []string{
		"normal", "alpha", "digits", "number", "phone", "url", "email", "name",
		"password", "pin", "terminal", "date", "time", "datetime", "none", "unknown",
	} {
		if got := knownPurpose(s); got != Purpose(s) {
			t.Errorf("knownPurpose(%q) = %q", s, got)
		}
	}
	for _, s := range []string{"", "Password", "free-form", "secret"} {
		if got := knownPurpose(s); got != PurposeUnknown {
			t.Errorf("knownPurpose(%q) = %q, want %q", s, got, PurposeUnknown)
		}
	}
}
