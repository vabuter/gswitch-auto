package runtime

// renderXKBKeyNames maps the evdev scancodes of the alphanumeric block to XKB key
// names (X keycode = evdev + 8, see /usr/share/X11/xkb/keycodes/evdev). The
// table is static because the evdev keycodes file may be absent on minimal
// systems. Unlisted indices are "".
var renderXKBKeyNames = [...]string{
	2: "AE01", 3: "AE02", 4: "AE03", 5: "AE04", 6: "AE05", 7: "AE06",
	8: "AE07", 9: "AE08", 10: "AE09", 11: "AE10", 12: "AE11", 13: "AE12",
	16: "AD01", 17: "AD02", 18: "AD03", 19: "AD04", 20: "AD05", 21: "AD06",
	22: "AD07", 23: "AD08", 24: "AD09", 25: "AD10", 26: "AD11", 27: "AD12",
	30: "AC01", 31: "AC02", 32: "AC03", 33: "AC04", 34: "AC05", 35: "AC06",
	36: "AC07", 37: "AC08", 38: "AC09", 39: "AC10", 40: "AC11", 41: "TLDE",
	43: "BKSL", 44: "AB01", 45: "AB02", 46: "AB03", 47: "AB04", 48: "AB05",
	49: "AB06", 50: "AB07", 51: "AB08", 52: "AB09", 53: "AB10",
	86: "LSGT",
}

// XKBKeyName maps an evdev scancode to the XKB key name used as LayoutInfo.KeyMap key
// (no angle brackets). X keycode = evdev + 8 (/usr/share/X11/xkb/keycodes/evdev, minimum = 8).
//
//	41→TLDE, 2..13→AE01..AE12, 16..27→AD01..AD12, 30..40→AC01..AC11, 43→BKSL,
//	44..53→AB01..AB10, 86→LSGT (48 entries); everything else ok=false.
func XKBKeyName(code uint16) (name string, ok bool) {
	if int(code) >= len(renderXKBKeyNames) {
		return "", false
	}
	name = renderXKBKeyNames[code]
	return name, name != ""
}
