package userconfig

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// ConflictLevel classifies how a chord interacts with the GNOME layout switch
// accelerators.
type ConflictLevel uint8

const (
	// ConflictNone: no press order of the chord fires an accelerator.
	ConflictNone ConflictLevel = iota
	// ConflictOrderDependent: some press orders fire an accelerator
	// (a warning in the GUI; the daemon restores a flipped layout).
	ConflictOrderDependent
	// ConflictAlways: every press order fires an accelerator (a validation
	// error in the GUI).
	ConflictAlways
)

// String returns "none", "order-dependent" or "always".
func (l ConflictLevel) String() string {
	switch l {
	case ConflictNone:
		return "none"
	case ConflictOrderDependent:
		return "order-dependent"
	case ConflictAlways:
		return "always"
	default:
		return "unknown"
	}
}

// modClass is a bit set of accelerator modifier classes.
type modClass uint8

const (
	modShift modClass = 1 << iota
	modControl
	modAlt
	modSuper
)

// accelModifiers maps GTK accelerator modifier names (lower case) to classes.
var accelModifiers = map[string]modClass{
	"shift":   modShift,
	"control": modControl,
	"ctrl":    modControl,
	"primary": modControl,
	"alt":     modAlt,
	"mod1":    modAlt,
	"super":   modSuper,
	"mod4":    modSuper,
}

// keyModClass returns the modifier class of a modifier scancode. RAlt (100)
// is Alt_R, which is Mod1 on the us/ru (winkeys) layouts.
func keyModClass(code uint16) modClass {
	switch code {
	case 42, 54:
		return modShift
	case 29, 97:
		return modControl
	case 56, 100:
		return modAlt
	case 125, 126:
		return modSuper
	default:
		return 0
	}
}

// accelKeyCodes maps the keysyms a chord key can produce to scancodes. A
// valid chord only contains modifiers and ChordExtraKeys, so an accelerator
// whose key is not listed here can never be fired by a chord.
var accelKeyCodes = func() map[string]uint16 {
	m := map[string]uint16{
		"Shift_L": 42, "Shift_R": 54,
		"Alt_L": 56, "Alt_R": 100,
		"Control_L": 29, "Control_R": 97,
		"Super_L": 125, "Super_R": 126,
		"Scroll_Lock": 70, "Pause": 119, "Menu": 127,
		"F11": 87, "F12": 88,
	}
	for i := range 10 {
		m["F"+strconv.Itoa(i+1)] = uint16(59 + i)
	}
	for i := range 12 {
		m["F"+strconv.Itoa(i+13)] = uint16(183 + i)
	}
	return m
}()

// keyLabels names chord keys in conflict descriptions.
var keyLabels = map[uint16]string{
	29: "LCtrl", 42: "LShift", 54: "RShift", 56: "LAlt",
	97: "RCtrl", 100: "RAlt", 125: "LSuper", 126: "RSuper",
	70: "ScrollLock", 119: "Pause", 127: "Menu",
}

func keyLabel(code uint16) string {
	if l, ok := keyLabels[code]; ok {
		return l
	}
	switch {
	case code >= 59 && code <= 68:
		return "F" + strconv.Itoa(int(code)-58)
	case code == 87, code == 88:
		return "F" + strconv.Itoa(int(code)-76)
	case code >= 183 && code <= 194:
		return "F" + strconv.Itoa(int(code)-170)
	default:
		return "key " + strconv.Itoa(int(code))
	}
}

type accelerator struct {
	text string
	mods modClass
	code uint16 // 0: key the chord can never produce
}

// parseAccelerator parses "<Alt>Shift_L"-style GTK accelerators.
func parseAccelerator(s string) (accelerator, bool) {
	a := accelerator{text: s}
	rest := strings.TrimSpace(s)
	for strings.HasPrefix(rest, "<") {
		end := strings.IndexByte(rest, '>')
		if end < 0 {
			return a, false
		}
		mod, ok := accelModifiers[strings.ToLower(rest[1:end])]
		if !ok {
			return a, false
		}
		a.mods |= mod
		rest = rest[end+1:]
	}
	if rest == "" || strings.ContainsAny(rest, "<> \t") {
		return a, false
	}
	a.code = accelKeyCodes[rest]
	return a, true
}

// SwitchConflict evaluates every press order of c against the effective GNOME
// accelerators (the forward and backward lists as stored in gsettings, e.g.
// "<Alt>Shift_L", "<Shift>Alt_L"). The model: an accelerator <M1><M2>K fires
// when K is pressed while exactly the modifier set {M1,M2} is held; <Alt> is
// satisfied by LAlt(56) or RAlt(100), <Shift> by 42/54, <Control> by 29/97,
// <Super> by 125/126. The string describes the first bad press order; when
// some accelerators could not be parsed they are ignored (ConflictNone for
// them) and named in the string as a warning.
func SwitchConflict(c Chord, accels []string) (ConflictLevel, string) {
	parsed := make([]accelerator, 0, len(accels))
	var unknown []string
	for _, s := range accels {
		if t := strings.TrimSpace(s); t == "" || strings.EqualFold(t, "disabled") {
			continue
		}
		a, ok := parseAccelerator(s)
		if !ok {
			unknown = append(unknown, strconv.Quote(s))
			continue
		}
		if a.code != 0 {
			parsed = append(parsed, a)
		}
	}

	level, desc := ConflictNone, ""
	if len(c) > 0 && len(parsed) > 0 {
		level, desc = evaluateOrders(c.sorted(), parsed)
	}
	if len(unknown) > 0 {
		warning := "ignored unrecognised accelerator(s): " + strings.Join(unknown, ", ")
		if desc == "" {
			desc = warning
		} else {
			desc += "; " + warning
		}
	}
	return level, desc
}

// evaluateOrders tries every permutation of keys and counts the press orders
// that fire one of accels.
func evaluateOrders(keys Chord, accels []accelerator) (ConflictLevel, string) {
	total, bad := 0, 0
	desc := ""
	order := slices.Clone(keys)
	permute(order, 0, func(p []uint16) {
		total++
		if d, fired := firesAccelerator(p, accels); fired {
			bad++
			if desc == "" {
				desc = d
			}
		}
	})
	switch {
	case bad == 0:
		return ConflictNone, ""
	case bad == total:
		return ConflictAlways, desc
	default:
		return ConflictOrderDependent, desc
	}
}

// firesAccelerator replays one press order and reports the first accelerator
// it fires, described by the press prefix that fires it.
func firesAccelerator(order []uint16, accels []accelerator) (string, bool) {
	var held modClass
	for i, k := range order {
		for _, a := range accels {
			if a.code == k && a.mods == held {
				labels := make([]string, i+1)
				for j := range i + 1 {
					labels[j] = keyLabel(order[j])
				}
				return fmt.Sprintf("pressing %s triggers %s", strings.Join(labels, ", then "), a.text), true
			}
		}
		held |= keyModClass(k)
	}
	return "", false
}

// permute calls fn with every permutation of s[k:] (s[:k] fixed), in
// lexicographic order for sorted input.
func permute(s []uint16, k int, fn func([]uint16)) {
	if k >= len(s)-1 {
		fn(s)
		return
	}
	for i := k; i < len(s); i++ {
		// Rotate s[i] into position k to keep the remaining keys sorted.
		v := s[i]
		copy(s[k+1:i+1], s[k:i])
		s[k] = v
		permute(s, k+1, fn)
		copy(s[k:i], s[k+1:i+1])
		s[i] = v
	}
}
