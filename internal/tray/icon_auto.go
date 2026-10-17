package tray

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strconv"
	"strings"
)

// Badge geometry on the 48 px source raster: a 12 px green dot with a 1 px
// dark outline in the top-left corner, clear of the bottom-right flag of
// TrayIconModeAppWithFlag.
const (
	autoBadgeSize         = 12
	autoBadgeOutlineWidth = 1
)

var (
	autoBadgeFillColor    = color.RGBA{46, 204, 64, 255} // #2ECC40
	autoBadgeOutlineColor = color.RGBA{20, 20, 20, 255}
	autoIconCache         = make(map[string][]byte) // guarded by iconCacheMu
)

// GetNormalIconAuto returns the normal tray icon for mode and layout code,
// with the automatic correction badge when autoOn is true. Without the badge
// it is exactly GetNormalIcon(mode, code). Badged icons are cached per
// mode|code|on.
func GetNormalIconAuto(mode TrayIconMode, code string, autoOn bool) []byte {
	base := GetNormalIcon(mode, code)
	if !autoOn {
		return base
	}
	key := autoIconCacheKey(mode, code, autoOn)
	iconCacheMu.Lock()
	if icon, ok := autoIconCache[key]; ok {
		iconCacheMu.Unlock()
		return icon
	}
	iconCacheMu.Unlock()

	icon := addAutoBadge(base)
	if icon == nil {
		return base
	}
	iconCacheMu.Lock()
	if cached, ok := autoIconCache[key]; ok {
		icon = cached
	} else {
		autoIconCache[key] = icon
	}
	iconCacheMu.Unlock()
	return icon
}

func autoIconCacheKey(mode TrayIconMode, code string, autoOn bool) string {
	return string(normalizeTrayIconMode(mode)) + "|" + strings.ToLower(code) + "|" + strconv.FormatBool(autoOn)
}

// addAutoBadge draws the badge onto a copy of the encoded icon. It returns
// nil when the icon cannot be decoded or encoded.
func addAutoBadge(encoded []byte) []byte {
	decoded, err := png.Decode(bytes.NewReader(encoded))
	if err != nil {
		return nil
	}
	bounds := decoded.Bounds()
	canvas := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(canvas, canvas.Bounds(), decoded, bounds.Min, draw.Src)

	// A filled circle of diameter autoBadgeSize; pixels in the outer ring of
	// width autoBadgeOutlineWidth use the outline color.
	outer := float64(autoBadgeSize) / 2
	inner := outer - autoBadgeOutlineWidth
	for y := range autoBadgeSize {
		for x := range autoBadgeSize {
			dx := float64(x) + 0.5 - outer
			dy := float64(y) + 0.5 - outer
			distance := dx*dx + dy*dy
			switch {
			case distance <= inner*inner:
				canvas.SetRGBA(x, y, autoBadgeFillColor)
			case distance <= outer*outer:
				canvas.SetRGBA(x, y, autoBadgeOutlineColor)
			}
		}
	}

	var out bytes.Buffer
	if err := png.Encode(&out, canvas); err != nil {
		return nil
	}
	return out.Bytes()
}
