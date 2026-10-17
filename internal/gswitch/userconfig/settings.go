package userconfig

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// auto.conf keys.
const (
	KeyAutoEnabled         = "auto-enabled"
	KeyMinLetters          = "auto-min-letters"
	KeyMaxLetters          = "auto-max-letters"
	KeyShortWords          = "auto-short-words"
	KeyMargin              = "auto-margin"
	KeyMaxAltCost          = "auto-max-alt-cost"
	KeyHoldoffMs           = "auto-holdoff-ms"
	KeyMinIntervalMs       = "auto-min-interval-ms"
	KeyDelayMs             = "auto-delay-ms"
	KeySwitchDelayMs       = "auto-switch-delay-ms"
	KeyLearnOnUndo         = "auto-learn-on-undo"
	KeyLearnUndoCount      = "auto-learn-undo-count"
	KeyRequireFocus        = "auto-require-focus"
	KeySkipPassword        = "auto-skip-password"
	KeySkipTerminal        = "auto-skip-terminal"
	KeySkipLocked          = "auto-skip-locked"
	KeyToggleChord         = "hotkey-toggle-auto"
	KeySelectionChord      = "hotkey-convert-selection"
	KeyExposeWords         = "ipc-expose-words"
	KeyFocusSource         = "focus-source"
	KeyLayoutSource        = "layout-source"
	KeyLayoutHintTimeoutMs = "layout-hint-timeout-ms"
)

// Values of focus-source and layout-source.
const (
	SourceAuto = "auto"
	SourceOff  = "off"
	SourceIBus = "ibus"
	SourceNone = "none"
)

// Settings holds the values of auto.conf.
type Settings struct {
	AutoEnabled, ShortWords, LearnOnUndo                 bool
	RequireFocus, SkipPassword, SkipTerminal, SkipLocked bool
	ExposeWords                                          bool
	MinLetters, MaxLetters                               int
	Margin, MaxAltCost                                   float64
	HoldoffMs, MinIntervalMs, DelayMs, SwitchDelayMs     int
	LearnUndoCount, LayoutHintTimeoutMs                  int
	FocusSource, LayoutSource                            string
	ToggleChord, SelectionChord                          Chord // nil = disabled ("none")
}

// DefaultSettings returns the documented defaults (§3.6.2 of the spec).
func DefaultSettings() Settings {
	return Settings{
		AutoEnabled:         false,
		MinLetters:          3,
		MaxLetters:          24,
		ShortWords:          true,
		Margin:              1.0,
		MaxAltCost:          7.0,
		HoldoffMs:           120,
		MinIntervalMs:       1000,
		DelayMs:             3,
		SwitchDelayMs:       60,
		LearnOnUndo:         true,
		LearnUndoCount:      1,
		RequireFocus:        false,
		SkipPassword:        true,
		SkipTerminal:        true,
		SkipLocked:          true,
		ToggleChord:         Chord{42, 54},
		SelectionChord:      Chord{42, 97, 100},
		ExposeWords:         true,
		FocusSource:         SourceAuto,
		LayoutSource:        SourceAuto,
		LayoutHintTimeoutMs: 400,
	}
}

// settingField describes one auto.conf key. set parses and range-checks a
// value; get formats the current value canonically.
type settingField struct {
	key  string
	kind string // human description of the accepted values
	get  func(*Settings) string
	set  func(*Settings, string) error
}

func boolField(key string, p func(*Settings) *bool) settingField {
	return settingField{
		key:  key,
		kind: "true or false",
		get:  func(s *Settings) string { return strconv.FormatBool(*p(s)) },
		set: func(s *Settings, v string) error {
			b, err := parseBool(v)
			if err != nil {
				return err
			}
			*p(s) = b
			return nil
		},
	}
}

func intField(key string, minV, maxV int, p func(*Settings) *int) settingField {
	return settingField{
		key:  key,
		kind: fmt.Sprintf("integer %d..%d", minV, maxV),
		get:  func(s *Settings) string { return strconv.Itoa(*p(s)) },
		set: func(s *Settings, v string) error {
			n, err := strconv.Atoi(v)
			if err != nil {
				return errors.New("not an integer")
			}
			if n < minV || n > maxV {
				return errors.New("out of range")
			}
			*p(s) = n
			return nil
		},
	}
}

func floatField(key string, minV, maxV float64, p func(*Settings) *float64) settingField {
	return settingField{
		key:  key,
		kind: fmt.Sprintf("number %s..%s", formatFloat(minV), formatFloat(maxV)),
		get:  func(s *Settings) string { return formatFloat(*p(s)) },
		set: func(s *Settings, v string) error {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
				return errors.New("not a number")
			}
			if f < minV || f > maxV {
				return errors.New("out of range")
			}
			*p(s) = f
			return nil
		},
	}
}

func enumField(key string, allowed []string, p func(*Settings) *string) settingField {
	return settingField{
		key:  key,
		kind: strings.Join(allowed, " or "),
		get:  func(s *Settings) string { return *p(s) },
		set: func(s *Settings, v string) error {
			v = strings.ToLower(v)
			if !slices.Contains(allowed, v) {
				return errors.New("unknown value")
			}
			*p(s) = v
			return nil
		},
	}
}

func chordField(key string, p func(*Settings) *Chord) settingField {
	return settingField{
		key:  key,
		kind: "chord such as 42+54, or none",
		get:  func(s *Settings) string { return p(s).String() },
		set: func(s *Settings, v string) error {
			c, err := ParseChord(v)
			if err != nil {
				return err
			}
			*p(s) = c
			return nil
		},
	}
}

// settingFields lists every auto.conf key in documentation order.
var settingFields = []settingField{
	boolField(KeyAutoEnabled, func(s *Settings) *bool { return &s.AutoEnabled }),
	intField(KeyMinLetters, 2, 8, func(s *Settings) *int { return &s.MinLetters }),
	intField(KeyMaxLetters, 4, 64, func(s *Settings) *int { return &s.MaxLetters }),
	boolField(KeyShortWords, func(s *Settings) *bool { return &s.ShortWords }),
	floatField(KeyMargin, 0, 5, func(s *Settings) *float64 { return &s.Margin }),
	floatField(KeyMaxAltCost, 3, 25.5, func(s *Settings) *float64 { return &s.MaxAltCost }),
	intField(KeyHoldoffMs, 30, 1000, func(s *Settings) *int { return &s.HoldoffMs }),
	intField(KeyMinIntervalMs, 0, 10000, func(s *Settings) *int { return &s.MinIntervalMs }),
	intField(KeyDelayMs, 0, 200, func(s *Settings) *int { return &s.DelayMs }),
	intField(KeySwitchDelayMs, 0, 2000, func(s *Settings) *int { return &s.SwitchDelayMs }),
	boolField(KeyLearnOnUndo, func(s *Settings) *bool { return &s.LearnOnUndo }),
	intField(KeyLearnUndoCount, 1, 10, func(s *Settings) *int { return &s.LearnUndoCount }),
	boolField(KeyRequireFocus, func(s *Settings) *bool { return &s.RequireFocus }),
	boolField(KeySkipPassword, func(s *Settings) *bool { return &s.SkipPassword }),
	boolField(KeySkipTerminal, func(s *Settings) *bool { return &s.SkipTerminal }),
	boolField(KeySkipLocked, func(s *Settings) *bool { return &s.SkipLocked }),
	chordField(KeyToggleChord, func(s *Settings) *Chord { return &s.ToggleChord }),
	chordField(KeySelectionChord, func(s *Settings) *Chord { return &s.SelectionChord }),
	boolField(KeyExposeWords, func(s *Settings) *bool { return &s.ExposeWords }),
	enumField(KeyFocusSource, []string{SourceAuto, SourceOff}, func(s *Settings) *string { return &s.FocusSource }),
	enumField(KeyLayoutSource, []string{SourceAuto, SourceIBus, SourceNone}, func(s *Settings) *string { return &s.LayoutSource }),
	intField(KeyLayoutHintTimeoutMs, 100, 2000, func(s *Settings) *int { return &s.LayoutHintTimeoutMs }),
}

func fieldFor(key string) (settingField, bool) {
	for _, f := range settingFields {
		if f.key == key {
			return f, true
		}
	}
	return settingField{}, false
}

// upstreamKeys are keys of /etc/gswitch/default.conf; they have no meaning in
// auto.conf.
var upstreamKeys = map[string]bool{
	"layout-switch": true, "layout-switch-key": true, "convert-key": true,
	"replace-key": true, "reverse-mode": true, "swap-conversion-modifiers": true,
	"delay": true, "layout-switch-delay": true, "blacklist": true,
	"layout1": true, "layout2": true,
}

// Keys returns every auto.conf key in documentation order.
func Keys() []string {
	keys := make([]string, len(settingFields))
	for i, f := range settingFields {
		keys[i] = f.key
	}
	return keys
}

func parseBool(v string) (bool, error) {
	switch strings.ToLower(v) {
	case "true", "yes", "on", "1":
		return true, nil
	case "false", "no", "off", "0":
		return false, nil
	default:
		return false, errors.New("not a boolean")
	}
}

// formatFloat formats a float so that it parses back exactly and always
// carries a decimal point ("1.0", "25.5").
func formatFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}

// Validate checks every value against its documented range (chords with
// ValidateChord) and that the two chords do not overlap. Conflicts with the
// layout switch keys and the convert key are checked by ValidateChords, which
// needs the upstream configuration. It returns all problems joined.
func (s Settings) Validate() error {
	var errs []error
	scratch := DefaultSettings()
	for _, f := range settingFields {
		if v := f.get(&s); f.set(&scratch, v) != nil {
			errs = append(errs, fmt.Errorf("%s: invalid value %q (want %s)", f.key, v, f.kind))
		}
	}
	if s.ToggleChord.Overlaps(s.SelectionChord) {
		errs = append(errs, fmt.Errorf("%w: %s %s and %s %s",
			ErrChordOverlap, KeyToggleChord, s.ToggleChord, KeySelectionChord, s.SelectionChord))
	}
	return errors.Join(errs...)
}

// Equal reports whether both settings hold the same values (chords compared
// as sets).
func (s Settings) Equal(o Settings) bool {
	return s.AutoEnabled == o.AutoEnabled &&
		s.ShortWords == o.ShortWords &&
		s.LearnOnUndo == o.LearnOnUndo &&
		s.RequireFocus == o.RequireFocus &&
		s.SkipPassword == o.SkipPassword &&
		s.SkipTerminal == o.SkipTerminal &&
		s.SkipLocked == o.SkipLocked &&
		s.ExposeWords == o.ExposeWords &&
		s.MinLetters == o.MinLetters &&
		s.MaxLetters == o.MaxLetters &&
		s.Margin == o.Margin &&
		s.MaxAltCost == o.MaxAltCost &&
		s.HoldoffMs == o.HoldoffMs &&
		s.MinIntervalMs == o.MinIntervalMs &&
		s.DelayMs == o.DelayMs &&
		s.SwitchDelayMs == o.SwitchDelayMs &&
		s.LearnUndoCount == o.LearnUndoCount &&
		s.LayoutHintTimeoutMs == o.LayoutHintTimeoutMs &&
		s.FocusSource == o.FocusSource &&
		s.LayoutSource == o.LayoutSource &&
		s.ToggleChord.Equal(o.ToggleChord) &&
		s.SelectionChord.Equal(o.SelectionChord)
}

// Values returns every key with its canonical auto.conf value.
func (s Settings) Values() map[string]string {
	values := make(map[string]string, len(settingFields))
	for _, f := range settingFields {
		values[f.key] = f.get(&s)
	}
	return values
}

// DiffSettings returns the keys whose canonical value differs between old and
// updated, with the updated values. It includes auto-enabled when that
// differs; the settings window removes it before calling Update (auto-enabled
// is written only through SetAutoEnabled).
func DiffSettings(old, updated Settings) map[string]string {
	oldValues := old.Values()
	diff := make(map[string]string)
	for key, v := range updated.Values() {
		if oldValues[key] != v {
			diff[key] = v
		}
	}
	return diff
}

// canonicalValues validates an Update argument: every key must be an auto.conf
// key and every value must parse. It returns the canonical values.
func canonicalValues(values map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(values))
	var errs []error
	for key, raw := range values {
		f, ok := fieldFor(key)
		if !ok {
			errs = append(errs, fmt.Errorf("unknown key %q", key))
			continue
		}
		s := DefaultSettings()
		if err := f.set(&s, strings.TrimSpace(raw)); err != nil {
			errs = append(errs, fmt.Errorf("%s: invalid value %q (want %s): %w", key, raw, f.kind, err))
			continue
		}
		out[key] = f.get(&s)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("userconfig: %w", err)
	}
	return out, nil
}

// parseAutoConf parses auto.conf text with the upstream line grammar:
// key=value lines, '#' comment lines and [section] lines ignored, double
// quotes around values stripped, last assignment wins. A UTF-8 BOM and CRLF
// line ends are tolerated. Problems are reported as warnings; a bad value
// keeps the default.
func parseAutoConf(text string) (Settings, []string) {
	type assignment struct {
		value string
		line  int
	}
	assigned := make(map[string]assignment)
	var warnings []string
	for i, line := range splitLines(text) {
		lineNo := i + 1
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			warnings = append(warnings, fmt.Sprintf("%s:%d: line is not key=value; ignored", AutoConfName, lineNo))
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), "\"")
		switch _, known := fieldFor(key); {
		case known:
			assigned[key] = assignment{value: value, line: lineNo}
		case upstreamKeys[key]:
			warnings = append(warnings, fmt.Sprintf(
				"%s:%d: %q belongs to /etc/gswitch/default.conf; ignored", AutoConfName, lineNo, key))
		default:
			warnings = append(warnings, fmt.Sprintf("%s:%d: unknown key %q; ignored", AutoConfName, lineNo, key))
		}
	}

	s := DefaultSettings()
	defaults := DefaultSettings()
	for _, f := range settingFields {
		a, ok := assigned[f.key]
		if !ok {
			continue
		}
		if err := f.set(&s, a.value); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s:%d: invalid %s value %q (want %s: %v); using default %s",
				AutoConfName, a.line, f.key, a.value, f.kind, err, f.get(&defaults)))
		}
	}
	if s.ToggleChord.Overlaps(s.SelectionChord) {
		warnings = append(warnings, fmt.Sprintf("%s: %s %s overlaps %s %s; using the default chords",
			AutoConfName, KeyToggleChord, s.ToggleChord, KeySelectionChord, s.SelectionChord))
		s.ToggleChord, s.SelectionChord = defaults.ToggleChord, defaults.SelectionChord
	}
	return s, warnings
}

// splitLines splits text into lines without terminators, stripping a leading
// UTF-8 BOM and a trailing CR of every line. A final line terminator does not
// produce an extra empty line.
func splitLines(text string) []string {
	text = strings.TrimPrefix(text, "\uFEFF")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
}
