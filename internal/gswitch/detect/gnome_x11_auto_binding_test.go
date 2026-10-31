package detect

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func stubShiftLaunch7Gsettings(t *testing.T, responses ...struct {
	output string
	err    error
},
) *[][]string {
	t.Helper()
	saved := runGsettingsForShiftLaunch7
	t.Cleanup(func() { runGsettingsForShiftLaunch7 = saved })
	calls := &[][]string{}
	runGsettingsForShiftLaunch7 = func(_ *SessionEnv, args ...string) ([]byte, error) {
		*calls = append(*calls, append([]string(nil), args...))
		if len(*calls) > len(responses) {
			t.Fatalf("unexpected gsettings call %q", args)
		}
		r := responses[len(*calls)-1]
		return []byte(r.output), r.err
	}
	return calls
}

type gsResp = struct {
	output string
	err    error
}

func TestEnsureGNOMEX11ShiftLaunch7BindingAppends(t *testing.T) {
	calls := stubShiftLaunch7Gsettings(t,
		gsResp{output: "['<Super>space', 'XF86Keyboard', 'XF86Launch7']\n"},
		gsResp{},
		gsResp{output: "['<Super>space', 'XF86Keyboard', 'XF86Launch7', '<Shift>XF86Launch7']\n"},
	)
	changed, err := EnsureGNOMEX11ShiftLaunch7Binding(gnomeX11TestEnv())
	if err != nil || !changed {
		t.Fatalf("EnsureGNOMEX11ShiftLaunch7Binding() = %v, %v; want true, nil", changed, err)
	}
	want := [][]string{
		{"get", gnomeWMKeybindingsSchema, gnomeSwitchInputSourceKey},
		{"set", gnomeWMKeybindingsSchema, gnomeSwitchInputSourceKey, "['<Super>space', 'XF86Keyboard', 'XF86Launch7', '<Shift>XF86Launch7']"},
		{"get", gnomeWMKeybindingsSchema, gnomeSwitchInputSourceKey},
	}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("gsettings calls = %#v, want %#v", *calls, want)
	}
}

func TestEnsureGNOMEX11ShiftLaunch7BindingIsIdempotent(t *testing.T) {
	calls := stubShiftLaunch7Gsettings(t, gsResp{output: "['XF86Launch7', '<Shift>XF86Launch7']\n"})
	changed, err := EnsureGNOMEX11ShiftLaunch7Binding(gnomeX11TestEnv())
	if err != nil || changed || len(*calls) != 1 {
		t.Fatalf("EnsureGNOMEX11ShiftLaunch7Binding() = %v, %v after %d calls; want false, nil after 1", changed, err, len(*calls))
	}
}

func TestEnsureGNOMEX11ShiftLaunch7BindingRollsBack(t *testing.T) {
	calls := stubShiftLaunch7Gsettings(t,
		gsResp{output: "['<Super>space']\n"},
		gsResp{},
		gsResp{output: "['<Super>space']\n"},
		gsResp{},
	)
	changed, err := EnsureGNOMEX11ShiftLaunch7Binding(gnomeX11TestEnv())
	if err == nil || changed || !strings.Contains(err.Error(), "verify <Shift>XF86Launch7 binding") {
		t.Fatalf("EnsureGNOMEX11ShiftLaunch7Binding() = %v, %v; want the verify error", changed, err)
	}
	if got := (*calls)[3]; !reflect.DeepEqual(got, []string{"set", gnomeWMKeybindingsSchema, gnomeSwitchInputSourceKey, "['<Super>space']"}) {
		t.Fatalf("rollback call = %q", got)
	}
}

func TestEnsureGNOMEX11ShiftLaunch7BindingReadError(t *testing.T) {
	stubShiftLaunch7Gsettings(t, gsResp{err: errors.New("no dconf")})
	if changed, err := EnsureGNOMEX11ShiftLaunch7Binding(gnomeX11TestEnv()); err == nil || changed {
		t.Fatalf("EnsureGNOMEX11ShiftLaunch7Binding() = %v, %v; want an error", changed, err)
	}
}

func TestEnsureGNOMEX11ShiftLaunch7BindingNeedsX11GNOME(t *testing.T) {
	calls := stubShiftLaunch7Gsettings(t)
	env := gnomeX11TestEnv()
	env.SessionType = "wayland"
	env.WaylandDisplay = "wayland-0"
	if changed, err := EnsureGNOMEX11ShiftLaunch7Binding(env); err == nil || changed || len(*calls) != 0 {
		t.Fatalf("EnsureGNOMEX11ShiftLaunch7Binding(wayland) = %v, %v after %d calls; want an error, no call", changed, err, len(*calls))
	}
}
