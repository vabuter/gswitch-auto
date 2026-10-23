package focus

// AppMatcher matches an application against the user's exclusion list.
// Implementations MUST normalise their own arguments with fold.AppKey (callers
// may pass raw or normalised strings) and MUST be nil-receiver safe (a nil
// *userconfig.ListSet stored in the interface is non-nil and must return
// false). userconfig.ListSet implements it.
type AppMatcher interface {
	MatchApp(appID, wmClass string) bool
}

// Policy decides whether automatic correction may act in the focused field.
type Policy struct {
	Excluded         AppMatcher // nil ⇒ none
	SkipPassword     bool       // auto-skip-password (true)
	SkipTerminal     bool       // auto-skip-terminal (true)
	SkipWhenLocked   bool       // auto-skip-locked (true)
	RequireExtension bool       // auto-require-focus (false)
	RequirePurpose   bool       // auto-strict-purpose, effective value (SPEC-X11 §3.3)
}

// Denial reasons returned by Policy.Allow.
const (
	ReasonLocked           = "locked"
	ReasonFocusUnavailable = "focus-unavailable"
	ReasonFocusUnknown     = "focus-unknown"
	ReasonPasswordField    = "password-field"
	ReasonTerminal         = "terminal"
	ReasonPurposeUnknown   = "purpose-unknown"
	ReasonExcludedApp      = "excluded-app"
)

// TextPurpose reports a purpose that names a text field a user types words
// into: normal, alpha, digits, number, phone, url, email, name, date, time,
// datetime and terminal. It is false for none, unknown, password and pin
// (terminal is then denied by SkipTerminal).
func TextPurpose(p Purpose) bool {
	switch p {
	case PurposeNormal, "alpha", "digits", "number", "phone", "url", "email", "name",
		"date", "time", "datetime", PurposeTerminal:
		return true
	default:
		return false
	}
}

// Allow reports whether automatic correction may act for st and, when it may
// not, the reason. The rules apply in order: lock; extension availability
// (fail-open unless RequireExtension or RequirePurpose; an absent extension
// then still goes through the exclusion list, because the application
// identity may come from X11); while the extension is available: unknown
// focus (fail closed: the application and field could be excluded),
// password/PIN field, terminal field, and in strict mode a purpose that is
// not a text purpose; finally the excluded application. The matcher receives
// the pre-normalised AppKey and WmClassKey and is skipped when both are
// empty (a glob such as "*" matches "" and must not exclude an unknown
// application), so Allow itself does not allocate.
func (p Policy) Allow(st State) (ok bool, reason string) {
	if st.Locked && p.SkipWhenLocked {
		return false, ReasonLocked
	}
	if !st.Available {
		if p.RequireExtension || p.RequirePurpose {
			return false, ReasonFocusUnavailable
		}
	} else {
		if st.FocusUnknown {
			return false, ReasonFocusUnknown
		}
		if (st.Purpose == PurposePassword || st.Purpose == PurposePIN) && p.SkipPassword {
			return false, ReasonPasswordField
		}
		if st.Purpose == PurposeTerminal && p.SkipTerminal {
			return false, ReasonTerminal
		}
		if p.RequirePurpose && !TextPurpose(st.Purpose) {
			return false, ReasonPurposeUnknown
		}
	}
	if (st.AppKey != "" || st.WmClassKey != "") && p.Excluded != nil && p.Excluded.MatchApp(st.AppKey, st.WmClassKey) {
		return false, ReasonExcludedApp
	}
	return true, ""
}
