package runtime

// Fork-only key codes and ioctls. Kept out of types.go to minimise rebase conflicts.

// Key codes (linux/input-event-codes.h).
const (
	KEY_ESC       = 1
	KEY_TAB       = 15
	KEY_LEFTALT   = 56
	KEY_CAPSLOCK  = 58
	KEY_NUMLOCK   = 69
	KEY_102ND     = 86
	KEY_RIGHTALT  = 100
	KEY_DELETE    = 111
	KEY_RIGHTMETA = 126
)

// LED events and evdev state ioctls (linux/input.h).
const (
	EV_LED        = 0x11
	LED_CAPSL     = 0x01
	EVIOCGKEY96   = 0x80604518 // EVIOCGKEY(96): 768-bit key state bitmap
	EVIOCGLED8    = 0x80084519 // EVIOCGLED(8)
	EVIOCGBITLED8 = 0x80084531 // EVIOCGBIT(EV_LED, 8)
)
