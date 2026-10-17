package tray

import (
	"bytes"
	"image/color"
	"image/png"
	"testing"
)

func TestGetNormalIconAutoBadge(t *testing.T) {
	base := GetNormalIcon(TrayIconModeApp, "")
	if got := GetNormalIconAuto(TrayIconModeApp, "", false); !bytes.Equal(got, base) {
		t.Fatal("icon without badge differs from GetNormalIcon")
	}
	badged := GetNormalIconAuto(TrayIconModeApp, "", true)
	if bytes.Equal(badged, base) {
		t.Fatal("badged icon equals the base icon")
	}
	img, err := png.Decode(bytes.NewReader(badged))
	if err != nil {
		t.Fatalf("decode badged icon: %v", err)
	}
	if size := img.Bounds().Size(); size.X != iconSize || size.Y != iconSize {
		t.Fatalf("badged icon size = %v, want 48x48", size)
	}
	baseImg, err := png.Decode(bytes.NewReader(base))
	if err != nil {
		t.Fatal(err)
	}
	center := color.RGBAModel.Convert(img.At(autoBadgeSize/2, autoBadgeSize/2)).(color.RGBA)
	if center != autoBadgeFillColor {
		t.Fatalf("badge centre = %v, want %v", center, autoBadgeFillColor)
	}
	edge := color.RGBAModel.Convert(img.At(0, autoBadgeSize/2)).(color.RGBA)
	if edge != autoBadgeOutlineColor {
		t.Fatalf("badge outline = %v, want %v", edge, autoBadgeOutlineColor)
	}
	if !sameColor(img.At(iconSize-1, iconSize-1), baseImg.At(iconSize-1, iconSize-1)) ||
		!sameColor(img.At(0, 0), baseImg.At(0, 0)) {
		t.Fatal("badge changed pixels outside the dot")
	}
	again := GetNormalIconAuto(TrayIconModeApp, "", true)
	if len(again) == 0 || &again[0] != &badged[0] {
		t.Fatal("badged icon is not cached")
	}
}

func TestGetNormalIconAutoPerModeAndLayout(t *testing.T) {
	flag := GetNormalIconAuto(TrayIconModeFlag, "US", true)
	if bytes.Equal(flag, GetNormalIcon(TrayIconModeFlag, "US")) {
		t.Fatal("flag icon has no badge")
	}
	img, err := png.Decode(bytes.NewReader(flag))
	if err != nil {
		t.Fatal(err)
	}
	if size := img.Bounds().Size(); size.X != iconSize || size.Y != 33 {
		t.Fatalf("badged flag size = %v, want 48x33", size)
	}
	overlay := GetNormalIconAuto(TrayIconModeAppWithFlag, "RU", true)
	if bytes.Equal(overlay, flag) || bytes.Equal(overlay, GetNormalIconAuto(TrayIconModeAppWithFlag, "US", true)) {
		t.Fatal("badged icons are not cached per mode and layout")
	}
	if !bytes.Equal(GetNormalIconAuto(TrayIconModeAppWithFlag, "ru", true), overlay) {
		t.Fatal("layout code case changes the cached icon")
	}
}
