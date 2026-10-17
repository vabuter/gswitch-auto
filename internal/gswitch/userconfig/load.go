package userconfig

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
)

// Config is one loaded view of the configuration directory.
type Config struct {
	Dir      string
	Settings Settings
	Words    *ListSet
	Apps     *ListSet
	Warnings []string
	stamps   dirStamps
}

// fileStamp identifies the state of one path cheaply (stat only).
type fileStamp struct {
	state uint8 // stampMissing, stampPresent or stampError
	dev   uint64
	ino   uint64
	size  int64
	mode  uint32
	mtime unix.Timespec
	ctime unix.Timespec
}

const (
	stampMissing uint8 = iota
	stampPresent
	stampError
)

// dirStamps holds the stamps of the directory and its three files.
type dirStamps [4]fileStamp

var watchedNames = [...]string{AutoConfName, WordsName, AppsName}

func statStamp(path string) fileStamp {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) {
			return fileStamp{state: stampMissing}
		}
		return fileStamp{state: stampError}
	}
	return fileStamp{
		state: stampPresent,
		dev:   st.Dev,
		ino:   st.Ino,
		size:  st.Size,
		mode:  st.Mode,
		mtime: st.Mtim,
		ctime: st.Ctim,
	}
}

func readStamps(dir string) dirStamps {
	var s dirStamps
	// Only the identity and mode of the directory matter; its times change
	// with every lock or temp file and with unrelated files (ui.conf).
	d := statStamp(dir)
	s[0] = fileStamp{state: d.state, dev: d.dev, ino: d.ino, mode: d.mode}
	for i, name := range watchedNames {
		s[i+1] = statStamp(filepath.Join(dir, name))
	}
	return s
}

// Load reads auto.conf and both exception lists from dir. It never returns a
// nil *Config, and Words/Apps are always non-nil. A missing directory or file
// means defaults (or an empty list) and no error. Any other I/O error is
// returned (joined) together with a usable Config whose AutoEnabled is false
// and whose unreadable parts are defaults; the error text is also added to
// Warnings so that watchers see it. Parse problems are warnings only.
func Load(dir string) (*Config, error) {
	c := &Config{
		Dir:      dir,
		Settings: DefaultSettings(),
		Words:    emptyListSet(),
		Apps:     emptyListSet(),
	}
	if err := checkDir(dir); err != nil {
		c.Warnings = append(c.Warnings, err.Error())
		return c, err
	}
	c.stamps = readStamps(dir)

	var errs []error
	if text, found, err := readCapped(filepath.Join(dir, AutoConfName), MaxAutoConfBytes); err != nil {
		errs = append(errs, err)
	} else if found {
		if len(text) > MaxAutoConfBytes {
			c.Warnings = append(c.Warnings, fmt.Sprintf("%s: larger than %d bytes; ignored, using defaults",
				AutoConfName, MaxAutoConfBytes))
		} else {
			var warnings []string
			c.Settings, warnings = parseAutoConf(text)
			c.Warnings = append(c.Warnings, warnings...)
		}
	}

	for _, name := range []string{WordsName, AppsName} {
		spec, _ := listSpecFor(name)
		set, warnings, err := loadList(dir, spec)
		if err != nil {
			errs = append(errs, err)
		}
		c.Warnings = append(c.Warnings, warnings...)
		if name == WordsName {
			c.Words = set
		} else {
			c.Apps = set
		}
	}

	if err := errors.Join(errs...); err != nil {
		c.Settings.AutoEnabled = false
		for _, e := range errs {
			c.Warnings = append(c.Warnings, e.Error()+"; automatic correction disabled")
		}
		return c, err
	}
	return c, nil
}

// loadList reads one exception list. Content beyond MaxListBytes is ignored
// (up to the last complete line) with a warning.
func loadList(dir string, spec listSpec) (*ListSet, []string, error) {
	text, found, err := readCapped(filepath.Join(dir, spec.name), MaxListBytes)
	if err != nil {
		return emptyListSet(), nil, err
	}
	if !found {
		return emptyListSet(), nil, nil
	}
	var warnings []string
	if len(text) > MaxListBytes {
		text = text[:MaxListBytes]
		if i := strings.LastIndexByte(text, '\n'); i >= 0 {
			text = text[:i+1]
		} else {
			text = ""
		}
		warnings = append(warnings, fmt.Sprintf("%s: larger than %d bytes; the rest is ignored",
			spec.name, MaxListBytes))
	}
	set, parseWarnings := parseList(spec, text)
	return set, append(warnings, parseWarnings...), nil
}

// readCapped reads at most limit+1 bytes of a file, so callers can tell an
// oversized file (len(text) > limit). found is false when the file does not
// exist.
func readCapped(path string, limit int64) (text string, found bool, err error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("userconfig: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return "", true, fmt.Errorf("userconfig: read %s: %w", path, err)
	}
	return string(data), true, nil
}

// Equal reports whether both configs have the same directory, settings, list
// entries and warnings. File stamps are ignored, so an identical rewrite is
// equal.
func (c *Config) Equal(o *Config) bool {
	if c == nil || o == nil {
		return c == o
	}
	return c.Dir == o.Dir &&
		c.Settings.Equal(o.Settings) &&
		c.Words.Equal(o.Words) &&
		c.Apps.Equal(o.Apps) &&
		slices.Equal(c.Warnings, o.Warnings)
}
