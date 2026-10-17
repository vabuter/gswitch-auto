package runtime

import (
	"errors"
	"fmt"
	"math/bits"
	"slices"
	"syscall"
	"unsafe"
)

// gswitch-auto: kernel key and LED state of the open input devices. The
// queries are read-only ioctls on the descriptors InputReader already holds
// (opened O_RDONLY|O_NONBLOCK in AddDevice); they never consume events.

const (
	keystateKeyBytes = 96 // EVIOCGKEY96 bitmap: codes 0..767 (KEY_MAX)
	keystateLEDBytes = 8  // EVIOCGLED8 / EVIOCGBITLED8 bitmaps
)

// KeysDown returns the union of keys currently down on all open devices (EVIOCGKEY).
// Errors on individual devices are ignored; err only when no device could be queried.
// An InputReader without devices reports an empty set and no error.
func (ir *InputReader) KeysDown() (map[uint16]bool, error) {
	ir.mu.RLock()
	defer ir.mu.RUnlock()

	down := make(map[uint16]bool)
	var state [keystateKeyBytes]byte
	queried := 0
	var firstErr error
	for _, device := range ir.devices {
		if device == nil {
			continue
		}
		if err := keystateIoctl(device.FD, EVIOCGKEY96, state[:]); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("EVIOCGKEY %s: %w", device.Path, err)
			}
			continue
		}
		queried++
		keystateCollect(state[:], down)
	}
	if queried == 0 && firstErr != nil {
		return nil, fmt.Errorf("query key state: no device could be queried: %w", firstErr)
	}
	return down, nil
}

// AnyKeyDown reports whether any key is down on any device; ioctl errors count as "not down".
func (ir *InputReader) AnyKeyDown() bool {
	ir.mu.RLock()
	defer ir.mu.RUnlock()

	var state [keystateKeyBytes]byte
	for _, device := range ir.devices {
		if device == nil || keystateIoctl(device.FD, EVIOCGKEY96, state[:]) != nil {
			continue
		}
		for _, b := range state {
			if b != 0 {
				return true
			}
		}
	}
	return false
}

// CapsLockState queries EVIOCGLED on the first open device advertising LED_CAPSL
// (EVIOCGBIT(EV_LED)). known=false when no device has the LED. Never uses sysfs paths.
// Devices are tried in ascending descriptor order so that the answer is stable;
// a device whose EVIOCGLED fails is skipped.
func (ir *InputReader) CapsLockState() (on, known bool) {
	ir.mu.RLock()
	defer ir.mu.RUnlock()

	fds := make([]int, 0, len(ir.devices))
	for _, device := range ir.devices {
		if device != nil {
			fds = append(fds, device.FD)
		}
	}
	slices.Sort(fds)

	var supported, state [keystateLEDBytes]byte
	for _, fd := range fds {
		if keystateIoctl(fd, EVIOCGBITLED8, supported[:]) != nil || !keystateBit(supported[:], LED_CAPSL) {
			continue
		}
		if keystateIoctl(fd, EVIOCGLED8, state[:]) != nil {
			continue
		}
		return keystateBit(state[:], LED_CAPSL), true
	}
	return false, false
}

var errKeystateBadFD = errors.New("invalid file descriptor")

// keystateIoctl fills buf (cleared first) through a read-only evdev ioctl.
func keystateIoctl(fd int, request uintptr, buf []byte) error {
	clear(buf)
	if fd < 0 || len(buf) == 0 {
		return errKeystateBadFD
	}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), request, uintptr(unsafe.Pointer(&buf[0])))
	if errno != 0 {
		return errno
	}
	return nil
}

// keystateCollect adds the code of every set bit in an evdev bitmap to set.
func keystateCollect(bitmap []byte, set map[uint16]bool) {
	for i, b := range bitmap {
		for b != 0 {
			set[uint16(i*8+bits.TrailingZeros8(b))] = true //nolint:gosec // i < keystateKeyBytes
			b &= b - 1
		}
	}
}

// keystateBit reports whether bit n is set in an evdev bitmap.
func keystateBit(bitmap []byte, n int) bool {
	return n >= 0 && n/8 < len(bitmap) && bitmap[n/8]&(1<<(n%8)) != 0
}
