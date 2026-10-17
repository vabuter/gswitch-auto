package layoutstate

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/arumata/gswitch/internal/gswitch/detect"
)

// ibusEngineDescWire mirrors the serialized IBusEngineDesc, wire signature
// (sa{sv}ssssssssussssssss), as returned by IBus 1.5.34 on the host:
//
//	busctl --address=$IBUS_ADDRESS call org.freedesktop.IBus /org/freedesktop/IBus \
//	    org.freedesktop.DBus.Properties Get ss org.freedesktop.IBus GlobalEngine
//	v v (sa{sv}ssssssssussssssss) "IBusEngineDesc" 0 "xkb:ru::rus" "Russian" "Russian" "ru" "GPL" ...
type ibusEngineDescWire struct {
	Kind                                                                 string
	Attachments                                                          map[string]dbus.Variant
	Name, LongName, Description, Language, License, Author, Icon, Layout string
	Rank                                                                 uint32
	Hotkeys, Symbol, Setup, LayoutVariant, LayoutOption, Version         string
	TextDomain, IconPropKey                                              string
}

func ibusDesc(name string) ibusEngineDescWire {
	return ibusEngineDescWire{
		Kind:        ibusEngineDescType,
		Attachments: map[string]dbus.Variant{},
		Name:        name,
		LongName:    "Russian",
		Description: "Russian",
		Language:    "ru",
		License:     "GPL",
		Author:      "Peng Huang <shawn.p.huang@gmail.com>",
		Icon:        "ibus-keyboard",
		Layout:      "ru",
		Rank:        50,
	}
}

// wireBody encodes a message carrying values to D-Bus wire format and decodes
// it again, returning the body exactly as godbus hands it to a client, and the
// wire signature.
func wireBody(t *testing.T, msgType dbus.Type, values ...any) ([]any, string) {
	t.Helper()
	headers := map[dbus.HeaderField]dbus.Variant{
		dbus.FieldSignature: dbus.MakeVariant(dbus.SignatureOf(values...)),
	}
	switch msgType {
	case dbus.TypeMethodReply:
		headers[dbus.FieldReplySerial] = dbus.MakeVariant(uint32(7))
	case dbus.TypeSignal:
		headers[dbus.FieldPath] = dbus.MakeVariant(ibusPath)
		headers[dbus.FieldInterface] = dbus.MakeVariant(ibusInterface)
		headers[dbus.FieldMember] = dbus.MakeVariant("GlobalEngineChanged")
	}
	msg := &dbus.Message{Type: msgType, Headers: headers, Body: values}
	var buf bytes.Buffer
	if err := msg.EncodeTo(&buf, binary.LittleEndian); err != nil {
		t.Fatalf("EncodeTo() error = %v", err)
	}
	decoded, err := dbus.DecodeMessage(&buf)
	if err != nil {
		t.Fatalf("DecodeMessage() error = %v", err)
	}
	var sig dbus.Signature
	if v, ok := decoded.Headers[dbus.FieldSignature]; ok {
		_ = v.Store(&sig)
	}
	return decoded.Body, sig.String()
}

func TestEngineNameFromDescGolden(t *testing.T) {
	desc := ibusDesc("xkb:ru::rus")
	propsGet, propsSig := wireBody(t, dbus.TypeMethodReply, dbus.MakeVariant(dbus.MakeVariant(desc)))
	getEngine, getSig := wireBody(t, dbus.TypeMethodReply, dbus.MakeVariant(desc))
	signal, signalSig := wireBody(t, dbus.TypeSignal, "xkb:us::eng")
	if propsSig != "v" || getSig != "v" || signalSig != "s" {
		t.Fatalf("wire signatures = %q/%q/%q, want v/v/s", propsSig, getSig, signalSig)
	}
	outer, ok := propsGet[0].(dbus.Variant)
	if !ok {
		t.Fatalf("Properties.Get body[0] = %T, want dbus.Variant", propsGet[0])
	}
	inner, ok := outer.Value().(dbus.Variant)
	if !ok || inner.Signature().String() != "(sa{sv}ssssssssussssssss)" {
		t.Fatalf("Properties.Get inner value = %#v, want a variant of IBusEngineDesc", outer.Value())
	}

	tests := []struct {
		name string
		in   any
		want string
	}{
		{"Properties.Get double-wrapped", propsGet[0], "xkb:ru::rus"},
		{"GetGlobalEngine single-wrapped", getEngine[0], "xkb:ru::rus"},
		{"GlobalEngineChanged argument", signal[0], "xkb:us::eng"},
		{"bare struct", inner.Value(), "xkb:ru::rus"},
		{"variant-wrapped string", dbus.MakeVariant("xkb:us::eng"), "xkb:us::eng"},
		{"three levels", dbus.MakeVariant(propsGet[0]), "xkb:ru::rus"},
		{"empty engine name", dbus.MakeVariant([]any{ibusEngineDescType, map[string]dbus.Variant{}, ""}), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := engineNameFromDesc(tt.in)
			if err != nil || got != tt.want {
				t.Fatalf("engineNameFromDesc() = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestEngineNameFromDescMalformed(t *testing.T) {
	tests := []struct {
		name string
		in   any
	}{
		{"four variant levels", dbus.MakeVariant(dbus.MakeVariant(dbus.MakeVariant(dbus.MakeVariant("xkb:us::eng"))))},
		{"short struct", []any{"IBusEngineDesc", map[string]dbus.Variant{}}},
		{"wrong type tag", []any{"IBusComponent", map[string]dbus.Variant{}, "xkb:us::eng"}},
		{"non-string name", []any{"IBusEngineDesc", map[string]dbus.Variant{}, uint32(1)}},
		{"integer", dbus.MakeVariant(int32(5))},
		{"nil", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := engineNameFromDesc(tt.in); err == nil {
				t.Fatalf("engineNameFromDesc() = %q, nil; want error", got)
			}
		})
	}
}

func TestGlobalEngineFromSignal(t *testing.T) {
	tests := []struct {
		name   string
		sig    *dbus.Signal
		want   string
		wantOK bool
	}{
		{"engine changed", &dbus.Signal{Sender: "org.freedesktop.IBus", Path: ibusPath, Name: ibusGlobalEngineChanged, Body: []any{"xkb:ru::rus"}}, "xkb:ru::rus", true},
		{"other member", &dbus.Signal{Path: ibusPath, Name: ibusInterface + ".RegistryChanged"}, "", false},
		{"other interface", &dbus.Signal{Path: ibusPath, Name: "org.freedesktop.IBus.Service.GlobalEngineChanged", Body: []any{"x"}}, "", false},
		{"name acquired", &dbus.Signal{Sender: dbusService, Path: dbusPath, Name: "org.freedesktop.DBus.NameAcquired", Body: []any{":1.5"}}, "", false},
		{"wrong path", &dbus.Signal{Path: "/org/freedesktop/IBus/InputContext_1", Name: ibusGlobalEngineChanged, Body: []any{"xkb:ru::rus"}}, "", false},
		{"empty body", &dbus.Signal{Path: ibusPath, Name: ibusGlobalEngineChanged}, "", false},
		{"bad body", &dbus.Signal{Path: ibusPath, Name: ibusGlobalEngineChanged, Body: []any{uint32(3)}}, "", false},
		{"nil", nil, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := globalEngineFromSignal(tt.sig)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("globalEngineFromSignal() = %q, %v; want %q, %v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestIBusOwnerAppeared(t *testing.T) {
	sig := func(sender, name, newOwner string) *dbus.Signal {
		return &dbus.Signal{Sender: sender, Path: dbusPath, Name: dbusNameOwnerChanged, Body: []any{name, "", newOwner}}
	}
	if !ibusOwnerAppeared(sig(dbusService, ibusService, ":1.42")) {
		t.Fatal("new owner not recognised")
	}
	if ibusOwnerAppeared(sig(dbusService, ibusService, "")) {
		t.Fatal("vanished owner treated as appeared")
	}
	if ibusOwnerAppeared(sig(dbusService, "org.example.Other", ":1.42")) {
		t.Fatal("other name treated as IBus")
	}
	if ibusOwnerAppeared(sig(":1.9", ibusService, ":1.42")) {
		t.Fatal("signal not from the bus daemon accepted")
	}
	if ibusOwnerAppeared(nil) {
		t.Fatal("nil signal accepted")
	}
}

func TestDBusErrorClassification(t *testing.T) {
	unknown := dbus.Error{Name: errUnknownProperty, Body: []any{"No such property 'GlobalEngine'"}}
	invalid := dbus.Error{Name: errInvalidArgs, Body: []any{"No such property “GlobalEngine”"}}
	otherInvalid := dbus.Error{Name: errInvalidArgs, Body: []any{"Type mismatch"}}
	noEngine := dbus.Error{Name: "org.freedesktop.DBus.Error.Failed", Body: []any{"No global engine."}}

	if !isUnknownProperty(unknown) || !isUnknownProperty(&unknown) || !isUnknownProperty(invalid) {
		t.Fatal("unknown property not recognised")
	}
	if isUnknownProperty(otherInvalid) || isUnknownProperty(noEngine) || isUnknownProperty(errors.New("x")) {
		t.Fatal("unrelated error treated as unknown property")
	}
	if !isNoGlobalEngine(noEngine) || isNoGlobalEngine(unknown) {
		t.Fatal("No global engine classification wrong")
	}
}

func TestIBusSourceEnabled(t *testing.T) {
	gnome := &detect.SessionEnv{XDGCurrentDesktop: "GNOME"}
	kde := &detect.SessionEnv{XDGCurrentDesktop: "KDE"}
	tests := []struct {
		source string
		env    *detect.SessionEnv
		euid   int
		want   bool
	}{
		{"auto", gnome, 1000, true},
		{"auto", kde, 1000, false},
		{"auto", gnome, 0, false},
		{"ibus", kde, 1000, true},
		{"ibus", gnome, 0, false},
		{"none", gnome, 1000, false},
		{"", gnome, 1000, false},
	}
	for _, tt := range tests {
		if got := IBusSourceEnabled(tt.source, tt.env, tt.euid); got != tt.want {
			t.Errorf("IBusSourceEnabled(%q, %s, %d) = %v, want %v", tt.source, tt.env.XDGCurrentDesktop, tt.euid, got, tt.want)
		}
	}
}

// sinkEvent is one call on recordingSink.
type sinkEvent struct {
	kind   string // "engine" | "available"
	engine string
	ok     bool
}

type recordingSink struct{ ch chan sinkEvent }

func newRecordingSink() *recordingSink { return &recordingSink{ch: make(chan sinkEvent, 1024)} }

func (r *recordingSink) Engine(_, name string) { r.ch <- sinkEvent{kind: "engine", engine: name} }

func (r *recordingSink) Available(_ string, ok bool) { r.ch <- sinkEvent{kind: "available", ok: ok} }

// waitFor skips events until want arrives.
func (r *recordingSink) waitFor(t *testing.T, want sinkEvent) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-r.ch:
			if ev == want {
				return
			}
		case <-timeout:
			t.Fatalf("timed out waiting for sink event %+v", want)
		}
	}
}

// next returns the next event that is not Available(false).
func (r *recordingSink) next(t *testing.T) sinkEvent {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-r.ch:
			if ev.kind == "available" && !ev.ok {
				continue
			}
			return ev
		case <-timeout:
			t.Fatal("timed out waiting for sink event")
			return sinkEvent{}
		}
	}
}

func TestIBusSourceBackoff(t *testing.T) {
	src := NewIBusSource(AddressEnv{}, nil)
	src.connectSession = nil
	resolves := 0
	src.resolve = func(AddressEnv) (string, int, error) {
		resolves++
		return "", 0, ErrIBusNotRunning
	}
	var sleeps []time.Duration
	src.sleep = func(_ context.Context, d time.Duration, _ <-chan struct{}) (bool, bool) {
		sleeps = append(sleeps, d)
		switch len(sleeps) {
		case 9:
			return true, true // woken by the session-bus accelerator
		case 11:
			return false, false
		}
		return true, false
	}
	sink := newRecordingSink()
	src.Run(context.Background(), sink)

	ms := time.Millisecond
	want := []time.Duration{200 * ms, 400 * ms, 800 * ms, 1600 * ms, 3200 * ms, 5000 * ms, 5000 * ms, 5000 * ms, 5000 * ms, 200 * ms, 400 * ms}
	if len(sleeps) != len(want) {
		t.Fatalf("sleeps = %v, want %v", sleeps, want)
	}
	for i := range want {
		if sleeps[i] != want[i] {
			t.Fatalf("sleeps = %v, want %v", sleeps, want)
		}
	}
	if resolves != len(want) {
		t.Fatalf("resolve calls = %d, want %d (address re-resolved on every attempt)", resolves, len(want))
	}
	if ev := <-sink.ch; ev.kind != "available" || ev.ok {
		t.Fatalf("first sink event = %+v, want Available(false)", ev)
	}
}

func TestIBusSourceRunStopsOnCancel(t *testing.T) {
	src := NewIBusSource(AddressEnv{}, nil)
	src.connectSession = nil
	src.resolve = func(AddressEnv) (string, int, error) { return "", 0, ErrIBusNotRunning }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		src.Run(ctx, newRecordingSink())
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestIBusSourceQueryResolveError(t *testing.T) {
	src := NewIBusSource(AddressEnv{}, nil)
	src.resolve = func(AddressEnv) (string, int, error) { return "", 0, ErrIBusNotRunning }
	if _, err := src.Query(context.Background()); !errors.Is(err, ErrIBusNotRunning) {
		t.Fatalf("Query() error = %v, want ErrIBusNotRunning", err)
	}
	if src.Name() != SourceIBus {
		t.Fatalf("Name() = %q", src.Name())
	}
}
