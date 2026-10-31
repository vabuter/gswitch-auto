package runtime

import "errors"

// No test writes the live gsettings: the fork's <Shift>XF86Launch7 binding
// is never added unless a test installs its own stub.
func init() {
	forkEnsureShiftLaunch7 = func(*SessionEnv) (bool, error) {
		return false, errors.New("gsettings disabled in tests")
	}
}
