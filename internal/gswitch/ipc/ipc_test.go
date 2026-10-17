package ipc

import (
	"encoding/xml"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
)

// ipcTestSpecXML is the interface of SPEC §3.9.1 (comments dropped).
const ipcTestSpecXML = `<node>
  <interface name="io.github.vabuter.GswitchAuto">
    <property name="AutoEnabled" type="b" access="readwrite"/>
    <property name="AutoEnabledUnsaved" type="b" access="read"/>
    <property name="PendingLearnedWords" type="as" access="read"/>
    <property name="AutoAvailable" type="b" access="read"/>
    <property name="AutoUnavailableReason" type="s" access="read"/>
    <property name="ActiveLayout" type="s" access="read"/>
    <property name="LayoutKnown" type="b" access="read"/>
    <property name="LayoutSource" type="s" access="read"/>
    <property name="FocusSource" type="s" access="read"/>
    <property name="FocusedApp" type="s" access="read"/>
    <property name="FocusedWmClass" type="s" access="read"/>
    <property name="InputPurpose" type="s" access="read"/>
    <property name="FocusSuppressed" type="b" access="read"/>
    <property name="FocusSuppressedReason" type="s" access="read"/>
    <property name="LastCorrection" type="(ssst)" access="read"/>
    <property name="CorrectionCount" type="u" access="read"/>
    <property name="Stats" type="a{st}" access="read"/>
    <property name="ToggleChord" type="s" access="read"/>
    <property name="SelectionChord" type="s" access="read"/>
    <property name="HotkeysSuspended" type="b" access="read"/>
    <property name="ConfigDir" type="s" access="read"/>
    <property name="ConfigWarnings" type="as" access="read"/>
    <property name="Version" type="s" access="read"/>
    <method name="SetAutoEnabled"><arg name="enabled" type="b" direction="in"/></method>
    <method name="Reload"/>
    <method name="SuspendHotkeys"><arg name="suspend" type="b" direction="in"/></method>
    <signal name="AutoEnabledChanged"><arg name="enabled" type="b"/><arg name="source" type="s"/></signal>
    <signal name="Corrected"><arg name="from" type="s"/><arg name="to" type="s"/><arg name="direction" type="s"/><arg name="time" type="t"/></signal>
    <signal name="CorrectionUndone"><arg name="word" type="s"/><arg name="learned" type="b"/></signal>
  </interface>
</node>`

func ipcTestFindInterface(t *testing.T, n *introspect.Node, name string) introspect.Interface {
	t.Helper()
	for _, iface := range n.Interfaces {
		if iface.Name == name {
			return iface
		}
	}
	t.Fatalf("interface %s not found", name)
	return introspect.Interface{}
}

// ipcTestCompareInterface compares names, types, access and args in order.
func ipcTestCompareInterface(t *testing.T, got, want introspect.Interface) {
	t.Helper()
	type propKey struct{ Name, Type, Access string }
	var gotProps, wantProps []propKey
	for _, p := range got.Properties {
		gotProps = append(gotProps, propKey{p.Name, p.Type, p.Access})
	}
	for _, p := range want.Properties {
		wantProps = append(wantProps, propKey{p.Name, p.Type, p.Access})
	}
	if !reflect.DeepEqual(gotProps, wantProps) {
		t.Errorf("properties:\n got %v\nwant %v", gotProps, wantProps)
	}
	type argKey struct{ Name, Type, Direction string }
	args := func(in []introspect.Arg) []argKey {
		out := []argKey{}
		for _, a := range in {
			out = append(out, argKey{a.Name, a.Type, a.Direction})
		}
		return out
	}
	gotMethods, wantMethods := map[string][]argKey{}, map[string][]argKey{}
	var gotOrder, wantOrder []string
	for _, m := range got.Methods {
		gotMethods[m.Name] = args(m.Args)
		gotOrder = append(gotOrder, m.Name)
	}
	for _, m := range want.Methods {
		wantMethods[m.Name] = args(m.Args)
		wantOrder = append(wantOrder, m.Name)
	}
	if !reflect.DeepEqual(gotMethods, wantMethods) || !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Errorf("methods:\n got %v %v\nwant %v %v", gotOrder, gotMethods, wantOrder, wantMethods)
	}
	gotSignals, wantSignals := map[string][]argKey{}, map[string][]argKey{}
	gotOrder, wantOrder = nil, nil
	for _, s := range got.Signals {
		gotSignals[s.Name] = args(s.Args)
		gotOrder = append(gotOrder, s.Name)
	}
	for _, s := range want.Signals {
		wantSignals[s.Name] = args(s.Args)
		wantOrder = append(wantOrder, s.Name)
	}
	if !reflect.DeepEqual(gotSignals, wantSignals) || !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Errorf("signals:\n got %v %v\nwant %v %v", gotOrder, gotSignals, wantOrder, wantSignals)
	}
}

func TestIntrospectionMatchesSpec(t *testing.T) {
	var want introspect.Node
	if err := xml.Unmarshal([]byte(ipcTestSpecXML), &want); err != nil {
		t.Fatalf("parse spec XML: %v", err)
	}
	generated := string(introspect.NewIntrospectable(serviceNode()))
	var got introspect.Node
	if err := xml.Unmarshal([]byte(generated), &got); err != nil {
		t.Fatalf("parse generated XML: %v\n%s", err, generated)
	}
	ipcTestCompareInterface(t, ipcTestFindInterface(t, &got, Interface), ipcTestFindInterface(t, &want, Interface))
	ipcTestFindInterface(t, &got, "org.freedesktop.DBus.Properties")
	ipcTestFindInterface(t, &got, "org.freedesktop.DBus.Introspectable")
}

func TestPropertyValuesMatchIntrospectionTypes(t *testing.T) {
	s := newService(&ipcTestController{}, nil)
	spec := s.propSpec(Status{})
	if len(spec) != len(interfaceData.Properties) {
		t.Fatalf("prop spec has %d entries, interface %d", len(spec), len(interfaceData.Properties))
	}
	for _, p := range interfaceData.Properties {
		pp, ok := spec[p.Name]
		if !ok {
			t.Errorf("no value for property %s", p.Name)
			continue
		}
		if sig := dbus.SignatureOf(pp.Value).String(); sig != p.Type {
			t.Errorf("%s: value signature %s, introspection %s", p.Name, sig, p.Type)
		}
		if pp.Writable != (p.Access == accessReadWrite) {
			t.Errorf("%s: writable=%t, access %s", p.Name, pp.Writable, p.Access)
		}
		if pp.Emit != prop.EmitTrue {
			t.Errorf("%s: emit %v, want EmitTrue", p.Name, pp.Emit)
		}
		if pp.Writable && pp.Callback == nil {
			t.Errorf("%s: writable without callback", p.Name)
		}
	}
	if len(statusValues(Status{})) != len(propertyNames) {
		t.Fatalf("statusValues covers %d properties, want %d", len(statusValues(Status{})), len(propertyNames))
	}
}

func ipcTestFullStatus() Status {
	return Status{
		AutoEnabled:           true,
		AutoAvailable:         true,
		LayoutKnown:           true,
		FocusSuppressed:       true,
		HotkeysSuspended:      true,
		AutoEnabledUnsaved:    true,
		PendingLearnedWords:   []string{"ghbdtn", "rfr"},
		UnavailableReason:     "layout-unknown",
		ActiveLayout:          "ru",
		LayoutSource:          "ibus",
		FocusSource:           "active",
		FocusedApp:            "org.gnome.TextEditor.desktop",
		FocusedWmClass:        "gnome-text-editor",
		InputPurpose:          "normal",
		FocusSuppressedReason: "excluded",
		ToggleChord:           "42+54",
		SelectionChord:        "42+100+97",
		ConfigDir:             "/home/u/.config/gswitch",
		Version:               "0.9.0",
		ConfigWarnings:        []string{"w1"},
		Last:                  Correction{From: "ghbdtn", To: "привет", Direction: "us>ru", Time: time.Unix(1760000000, 0)},
		Count:                 7,
		Stats:                 map[string]uint64{"armed": 3, "fired": 2, "custom": 9},
	}
}

func TestStatusPropertyRoundTrip(t *testing.T) {
	st := ipcTestFullStatus()
	values := statusValues(st)
	wire := make(map[string]dbus.Variant, len(values))
	for name, v := range values {
		wire[name] = dbus.MakeVariant(v)
	}
	got := statusFromProps(wire)
	want := cloneStatus(st)
	want.Stats = normalizeStats(st.Stats)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, want)
	}
}

func TestStatusPropertyRoundTripDecodedStruct(t *testing.T) {
	// The godbus decoder delivers structs as []any; applyProperty must accept that.
	var st Status
	v := dbus.MakeVariant([]any{"a", "b", "ru>us", uint64(42)})
	if err := applyProperty(&st, propLastCorrection, v); err != nil {
		t.Fatalf("applyProperty: %v", err)
	}
	want := Correction{From: "a", To: "b", Direction: "ru>us", Time: time.Unix(42, 0)}
	if st.Last != want {
		t.Fatalf("Last = %+v, want %+v", st.Last, want)
	}
	if err := applyProperty(&st, propCorrectionCount, dbus.MakeVariant("x")); err == nil {
		t.Fatal("type mismatch not reported")
	}
	if err := applyProperty(&st, "FutureProperty", dbus.MakeVariant("x")); err != nil {
		t.Fatalf("unknown property: %v", err)
	}
}

func TestZeroCorrectionTime(t *testing.T) {
	if got := unixSeconds(time.Time{}); got != 0 {
		t.Fatalf("unixSeconds(zero) = %d", got)
	}
	if got := timeFromUnix(0); !got.IsZero() {
		t.Fatalf("timeFromUnix(0) = %v", got)
	}
	if got := timeFromUnix(unixSeconds(time.Unix(1700000000, 0))); !got.Equal(time.Unix(1700000000, 0)) {
		t.Fatalf("round trip = %v", got)
	}
}

func TestStatsAlwaysCarryKnownKeys(t *testing.T) {
	got := normalizeStats(nil)
	for _, k := range statsKeys {
		if v, ok := got[k]; !ok || v != 0 {
			t.Fatalf("stats key %s = %d,%t", k, v, ok)
		}
	}
}

// TestPackageSourceRules enforces SPEC §5 WP6: no shared session bus and no
// runtime/GTK imports in non-test files.
func TestPackageSourceRules(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{
		"github.com/arumata/gswitch/internal/gswitch/runtime",
		"github.com/gotk3/",
		"fyne.io/systray",
	}
	checked := 0
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "dbus."+"SessionBus(") {
			t.Errorf("%s uses the shared session bus connection", name)
		}
		f, err := parser.ParseFile(fset, name, data, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			for _, bad := range forbidden {
				if strings.HasPrefix(path, bad) {
					t.Errorf("%s imports %s", name, path)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no source files checked")
	}
}
