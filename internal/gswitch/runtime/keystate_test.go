package runtime

import (
	"os"
	"reflect"
	"testing"
)

func keystateTestAssertIdle(t *testing.T, ir *InputReader) {
	t.Helper()
	down, err := ir.KeysDown()
	if err != nil {
		t.Fatalf("KeysDown() error = %v, want nil", err)
	}
	if down == nil || len(down) != 0 {
		t.Fatalf("KeysDown() = %#v, want an empty set", down)
	}
	if ir.AnyKeyDown() {
		t.Fatal("AnyKeyDown() = true, want false")
	}
	if on, known := ir.CapsLockState(); on || known {
		t.Fatalf("CapsLockState() = (%t, %t), want (false, false)", on, known)
	}
}

func TestKeyStateWithoutDevices(t *testing.T) {
	t.Run("zero value", func(t *testing.T) {
		keystateTestAssertIdle(t, &InputReader{})
	})
	t.Run("new reader", func(t *testing.T) {
		ir := NewInputReader()
		t.Cleanup(ir.Close)
		keystateTestAssertIdle(t, ir)
	})
	t.Run("closed reader", func(t *testing.T) {
		ir := NewInputReader()
		ir.Close()
		keystateTestAssertIdle(t, ir)
	})
}

// keystateTestReader registers devices with the given descriptors without
// any capability checks (AddDevice would reject non-evdev files).
func keystateTestReader(fds ...int) *InputReader {
	ir := &InputReader{devices: make(map[int]*Device), pathToFD: make(map[string]int)}
	for i, fd := range fds {
		path := "/dev/input/keystate-test-" + string(rune('a'+i))
		ir.devices[fd] = &Device{FD: fd, Path: path}
		ir.pathToFD[path] = fd
	}
	return ir
}

func keystateTestClosedFD(t *testing.T) int {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "closed")
	if err != nil {
		t.Fatal(err)
	}
	fd := int(file.Fd())
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return fd
}

func TestKeyStateUnqueryableDevicesFailSafe(t *testing.T) {
	regular, err := os.CreateTemp(t.TempDir(), "not-evdev")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { regular.Close() })

	tests := []struct {
		name string
		fds  func(t *testing.T) []int
	}{
		{name: "closed fd", fds: func(t *testing.T) []int { return []int{keystateTestClosedFD(t)} }},
		{name: "negative fd", fds: func(*testing.T) []int { return []int{-1} }},
		{name: "regular file", fds: func(*testing.T) []int { return []int{int(regular.Fd())} }},
		{name: "several bad devices", fds: func(t *testing.T) []int {
			return []int{-1, keystateTestClosedFD(t), int(regular.Fd())}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ir := keystateTestReader(tt.fds(t)...)
			down, err := ir.KeysDown()
			if err == nil {
				t.Fatalf("KeysDown() = %#v, nil; want an error when no device answers", down)
			}
			if down != nil {
				t.Fatalf("KeysDown() set = %#v on error, want nil", down)
			}
			if ir.AnyKeyDown() {
				t.Fatal("AnyKeyDown() = true; ioctl errors must count as not down")
			}
			if on, known := ir.CapsLockState(); on || known {
				t.Fatalf("CapsLockState() = (%t, %t), want (false, false)", on, known)
			}
		})
	}
}

func TestKeyStateNilDeviceEntryIgnored(t *testing.T) {
	ir := &InputReader{devices: map[int]*Device{7: nil}}
	keystateTestAssertIdle(t, ir)
}

func TestKeyStateCollectDecodesBitmap(t *testing.T) {
	bitmap := make([]byte, keystateKeyBytes)
	for _, code := range []int{1, 30, 42, 54, 255, 256, 767} {
		bitmap[code/8] |= 1 << (code % 8)
	}
	got := make(map[uint16]bool)
	keystateCollect(bitmap, got)
	want := map[uint16]bool{1: true, 30: true, 42: true, 54: true, 255: true, 256: true, 767: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keystateCollect() = %v, want %v", got, want)
	}
}

func TestKeyStateBit(t *testing.T) {
	bitmap := []byte{0b0000_0010, 0b1000_0000}
	for n, want := range map[int]bool{-1: false, 0: false, LED_CAPSL: true, 2: false, 15: true, 16: false, 64: false} {
		if got := keystateBit(bitmap, n); got != want {
			t.Errorf("keystateBit(%d) = %t, want %t", n, got, want)
		}
	}
}

func TestKeyStateIoctlNumbers(t *testing.T) {
	// _IOC(_IOC_READ, 'E', nr, size) from linux/input.h.
	ioc := func(nr, size uintptr) uintptr { return 2<<30 | size<<16 | 'E'<<8 | nr }
	tests := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"EVIOCGKEY(96)", EVIOCGKEY96, ioc(0x18, keystateKeyBytes)},
		{"EVIOCGLED(8)", EVIOCGLED8, ioc(0x19, keystateLEDBytes)},
		{"EVIOCGBIT(EV_LED, 8)", EVIOCGBITLED8, ioc(0x20+EV_LED, keystateLEDBytes)},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %#x, want %#x", tt.name, tt.got, tt.want)
		}
	}
}

// TestKeyStateLiveDevice queries a real keyboard when GSWITCH_TEST_EVDEV names
// one (for example /dev/input/event3). It only issues read-only ioctls.
func TestKeyStateLiveDevice(t *testing.T) {
	path := os.Getenv("GSWITCH_TEST_EVDEV")
	if path == "" {
		t.Skip("set GSWITCH_TEST_EVDEV=/dev/input/eventN to query a real keyboard")
	}
	ir := NewInputReader()
	t.Cleanup(ir.Close)
	if _, err := ir.AddDevice(path); err != nil {
		t.Fatalf("AddDevice(%q): %v", path, err)
	}
	down, err := ir.KeysDown()
	if err != nil {
		t.Fatalf("KeysDown(): %v", err)
	}
	on, known := ir.CapsLockState()
	t.Logf("keys down: %v, any: %t, caps lock: on=%t known=%t", down, ir.AnyKeyDown(), on, known)
	if !known {
		t.Errorf("CapsLockState() known = false for %s", path)
	}
}
