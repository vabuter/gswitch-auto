package gswitch

import (
	"strings"
	"testing"
	"time"

	"github.com/arumata/gswitch/internal/gswitch/layoutstate"
	gsruntime "github.com/arumata/gswitch/internal/gswitch/runtime"
)

// gswitch-auto: argument parsing and source selection of --layout-state and
// --focus-state (SPEC-X11 §3.7). Nothing here talks to a bus or a display.

func TestParseDiagArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		allowSource bool
		want        diagArgs
		wantErr     string
	}{
		{name: "defaults", allowSource: true, want: diagArgs{watch: defaultDiagWatch, source: diagSourceAuto}},
		{name: "watch value", args: []string{"--watch", "5"}, allowSource: true, want: diagArgs{watch: 5 * time.Second, source: diagSourceAuto}},
		{name: "watch equals zero", args: []string{"--watch=0"}, allowSource: true, want: diagArgs{watch: 0, source: diagSourceAuto}},
		{name: "watch fraction", args: []string{"--watch=0.5"}, want: diagArgs{watch: 500 * time.Millisecond, source: diagSourceAuto}},
		{name: "source x11", args: []string{"--source", "x11"}, allowSource: true, want: diagArgs{watch: defaultDiagWatch, source: diagSourceX11}},
		{name: "source equals ibus", args: []string{"--source=ibus", "--watch", "2"}, allowSource: true, want: diagArgs{watch: 2 * time.Second, source: diagSourceIBus}},
		{name: "source auto", args: []string{"--source=auto"}, allowSource: true, want: diagArgs{watch: defaultDiagWatch, source: diagSourceAuto}},
		{name: "source invalid", args: []string{"--source", "kde"}, allowSource: true, wantErr: `invalid --source value "kde"`},
		{name: "source missing value", args: []string{"--source"}, allowSource: true, wantErr: "--source needs a value"},
		{name: "source not allowed", args: []string{"--source", "x11"}, allowSource: false, wantErr: `unknown argument "--source"`},
		{name: "watch missing value", args: []string{"--watch"}, allowSource: true, wantErr: "--watch needs a value"},
		{name: "watch negative", args: []string{"--watch", "-1"}, allowSource: true, wantErr: `invalid --watch value "-1"`},
		{name: "watch not a number", args: []string{"--watch=soon"}, allowSource: true, wantErr: `invalid --watch value "soon"`},
		{name: "unknown argument", args: []string{"--verbose"}, allowSource: true, wantErr: `unknown argument "--verbose"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDiagArgs(tt.args, tt.allowSource)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parseDiagArgs(%q) error = %v, want %q", tt.args, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDiagArgs(%q) error = %v", tt.args, err)
			}
			if got != tt.want {
				t.Fatalf("parseDiagArgs(%q) = %+v, want %+v", tt.args, got, tt.want)
			}
		})
	}
}

func TestLayoutStateSources(t *testing.T) {
	x11 := gsruntime.PlatformSelection{X11Session: true, GNOME: true, LayoutX11: true,
		Shell: layoutstate.ShellInfo{Version: "43.9"}}
	ibus := gsruntime.PlatformSelection{Wayland: true, GNOME: true, LayoutIBus: true,
		Shell: layoutstate.ShellInfo{Version: "50.6"}}
	guarded := gsruntime.PlatformSelection{Wayland: true, GNOME: true, LayoutIBus: true, LayoutIBusGuarded: true,
		Shell: layoutstate.ShellInfo{Err: layoutstate.ErrShellUnavailable}}
	none := gsruntime.PlatformSelection{Wayland: true, LayoutReason: "layout-source-unsupported"}
	tests := []struct {
		name            string
		sel             gsruntime.PlatformSelection
		source          string
		runX11, runIBus bool
		note            string
	}{
		{name: "auto x11", sel: x11, source: diagSourceAuto, runX11: true},
		{name: "auto ibus", sel: ibus, source: diagSourceAuto, runIBus: true},
		{name: "auto guarded", sel: guarded, source: diagSourceAuto, runIBus: true, note: "IBus layout source guarded"},
		{name: "auto none", sel: none, source: diagSourceAuto},
		{name: "forced ibus below 49.3", sel: x11, source: diagSourceIBus, runIBus: true,
			note: "IBus layout source disabled for GNOME Shell 43.9"},
		{name: "forced ibus elsewhere", sel: none, source: diagSourceIBus, runIBus: true, note: "IBus layout source forced"},
		{name: "forced ibus selected", sel: ibus, source: diagSourceIBus, runIBus: true},
		{name: "forced x11 on wayland", sel: ibus, source: diagSourceX11, runX11: true, note: "Wayland session"},
		{name: "forced x11 selected", sel: x11, source: diagSourceX11, runX11: true},
		{name: "forced x11 elsewhere", sel: gsruntime.PlatformSelection{X11Session: true}, source: diagSourceX11, runX11: true,
			note: "X11 layout source forced"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runX11, runIBus, note := layoutStateSources(tt.sel, tt.source)
			if runX11 != tt.runX11 || runIBus != tt.runIBus {
				t.Fatalf("layoutStateSources = x11 %v, ibus %v; want %v, %v", runX11, runIBus, tt.runX11, tt.runIBus)
			}
			switch {
			case tt.note == "" && note != "":
				t.Fatalf("unexpected note %q", note)
			case tt.note != "" && !strings.Contains(note, tt.note):
				t.Fatalf("note = %q, want it to contain %q", note, tt.note)
			}
		})
	}
}
