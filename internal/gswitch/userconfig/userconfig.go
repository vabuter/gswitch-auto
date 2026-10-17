// Package userconfig reads and writes the per-user gswitch-auto configuration
// in ~/.config/gswitch: auto.conf (automatic correction settings and chords),
// exceptions-words.txt and exceptions-apps.txt.
//
// The package is shared by the daemon and the tray. The daemon only calls
// Load and Watch: under the upstream systemd unit (ProtectHome=read-only) any
// write under $HOME fails. Every write helper is meant for the tray (and
// tests). The package never touches /etc/gswitch/default.conf and adds no
// field to config.Config.
package userconfig

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	// DirName is the name of the per-user configuration directory inside
	// the XDG configuration home.
	DirName = "gswitch"
	// AutoConfName is the settings file.
	AutoConfName = "auto.conf"
	// WordsName is the exception word list.
	WordsName = "exceptions-words.txt"
	// AppsName is the excluded application list.
	AppsName = "exceptions-apps.txt"

	// MaxAutoConfBytes caps auto.conf; a larger file is ignored.
	MaxAutoConfBytes = 64 << 10
	// MaxListBytes caps each exception list file.
	MaxListBytes = 1 << 20
	// MaxListEntries caps the number of entries of each exception list.
	MaxListEntries = 10000
)

const (
	lockName = ".lock"
	dirMode  = 0o700
	fileMode = 0o600
)

var (
	//go:embed templates/auto.conf
	autoConfTemplate string
	//go:embed templates/exceptions-words.txt
	wordsTemplate string
	//go:embed templates/exceptions-apps.txt
	appsTemplate string
)

// templateFor returns the seed content of a configuration file.
func templateFor(name string) string {
	switch name {
	case AutoConfName:
		return autoConfTemplate
	case WordsName:
		return wordsTemplate
	case AppsName:
		return appsTemplate
	default:
		return ""
	}
}

// Dir returns the configuration directory of the current process:
// $XDG_CONFIG_HOME/gswitch when XDG_CONFIG_HOME is absolute, otherwise
// $HOME/.config/gswitch (what os.UserConfigDir returns without
// XDG_CONFIG_HOME). A relative XDG_CONFIG_HOME is ignored.
func Dir() (string, error) {
	home := os.Getenv("HOME")
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = h
		}
	}
	return DirFor(home, os.Getenv("XDG_CONFIG_HOME"))
}

// DirFor resolves the configuration directory for an explicit home directory
// and XDG_CONFIG_HOME value (root compatibility mode passes both from the
// session environment). A relative xdgConfigHome is ignored; home must be
// absolute when it is needed.
func DirFor(home, xdgConfigHome string) (string, error) {
	if xdgConfigHome != "" && filepath.IsAbs(xdgConfigHome) {
		return filepath.Join(xdgConfigHome, DirName), nil
	}
	if home == "" {
		return "", errors.New("userconfig: neither an absolute XDG_CONFIG_HOME nor HOME is set")
	}
	if !filepath.IsAbs(home) {
		return "", fmt.Errorf("userconfig: home directory %q is not absolute", home)
	}
	return filepath.Join(home, ".config", DirName), nil
}

// checkDir validates a directory argument of the public API.
func checkDir(dir string) error {
	if dir == "" {
		return errors.New("userconfig: empty configuration directory")
	}
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("userconfig: configuration directory %q is not absolute", dir)
	}
	return nil
}
