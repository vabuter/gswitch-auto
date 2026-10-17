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
}

// Denial reasons returned by Policy.Allow.
const (
	ReasonLocked           = "locked"
	ReasonFocusUnavailable = "focus-unavailable"
	ReasonFocusUnknown     = "focus-unknown"
	ReasonPasswordField    = "password-field"
	ReasonTerminal         = "terminal"
	ReasonExcludedApp      = "excluded-app"
)

// Allow reports whether automatic correction may act for st and, when it may
// not, the reason. The rules apply in order: lock, extension availability
// (fail-open unless RequireExtension), unknown focus while the extension is
// available (fail closed: the application and field could be excluded),
// password/PIN field, terminal field, excluded application. The matcher receives the pre-normalised AppKey and
// WmClassKey, so Allow itself does not allocate.
func (p Policy) Allow(st State) (ok bool, reason string) {
	if st.Locked && p.SkipWhenLocked {
		return false, ReasonLocked
	}
	if !st.Available {
		if p.RequireExtension {
			return false, ReasonFocusUnavailable
		}
		return true, ""
	}
	if st.FocusUnknown {
		return false, ReasonFocusUnknown
	}
	if (st.Purpose == PurposePassword || st.Purpose == PurposePIN) && p.SkipPassword {
		return false, ReasonPasswordField
	}
	if st.Purpose == PurposeTerminal && p.SkipTerminal {
		return false, ReasonTerminal
	}
	if p.Excluded != nil && p.Excluded.MatchApp(st.AppKey, st.WmClassKey) {
		return false, ReasonExcludedApp
	}
	return true, ""
}
