package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	cfg "github.com/arumata/gswitch/internal/gswitch/config"
	"github.com/arumata/gswitch/internal/gswitch/lang"
	"github.com/arumata/gswitch/internal/gswitch/userconfig"
)

// Exit codes of RunAutoCheck.
const (
	autoCheckExitOK           = 0
	autoCheckExitError        = 1
	autoCheckExitUnrenderable = 2
)

// autoCheckVerdictSkip is the verdict printed when the detector cannot run:
// lastWordRange finds no word or the word cannot be rendered in both layouts.
const autoCheckVerdictSkip = "skip"

// autoCheckEngineArm is the engine field when OnSpace would arm the fix.
const autoCheckEngineArm = "arm"

const autoCheckUsage = "usage: gswitch --auto-check <text> [--shown <layout>]"

var errAutoCheckUnmapped = errors.New("character is not on the layout")

// autoCheckDeps is the environment RunAutoCheck reads; tests replace it.
type autoCheckDeps struct {
	layouts func() ([]LayoutSpec, error)
	load    func(name, variant string) (*LayoutInfo, error)
	userDir func() (string, error)
}

var autoCheckSystem = autoCheckDeps{
	layouts: autoCheckResolveLayouts,
	load:    LoadLayout,
	userDir: userconfig.Dir,
}

// autoCheckResult is the JSON printed by --auto-check. Verdict and Reason are
// the detector's (SPEC 3.8, manual test M4), also when an engine veto
// (lexical, shift-shape, trailing edge) would stop the word; Engine is what
// the engine's OnSpace would do: "arm", or its skip reason code as logged
// (e.g. "lexical:digit", "detector:shown-known", "too-short"). Costs and
// dictionary flags are null when the word is not a score candidate.
type autoCheckResult struct {
	Shown      string   `json:"shown"`
	Alt        string   `json:"alt"`
	Active     string   `json:"active"`
	Verdict    string   `json:"verdict"`
	Reason     string   `json:"reason"`
	Engine     string   `json:"engine"`
	ShownCost  *float64 `json:"shown_cost"`
	AltCost    *float64 `json:"alt_cost"`
	ShownKnown *bool    `json:"shown_known"`
	AltKnown   *bool    `json:"alt_known"`
}

// RunAutoCheck implements `gswitch --auto-check <text> [--shown us|ru]`: it
// types text on the shown layout (inferred from the text when --shown is
// absent), appends Space and runs the auto engine's word pipeline
// (lastWordRange, renderWord, vetoes, detector) with the user configuration's
// detector options and exception words. It prints one JSON object and
// returns 0, 2 when the word cannot be rendered in both layouts, 1 on other
// errors. It needs neither root nor input devices.
func RunAutoCheck(args []string, stdout, stderr io.Writer) int {
	return autoCheckRun(args, stdout, stderr, autoCheckSystem)
}

func autoCheckRun(args []string, stdout, stderr io.Writer, deps autoCheckDeps) int {
	text, shownName, err := autoCheckParseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n%s\n", err, autoCheckUsage)
		return autoCheckExitError
	}
	specs, layouts, langs, err := autoCheckLoadLayouts(deps)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return autoCheckExitError
	}
	idx, err := autoCheckShownIndex(text, shownName, specs, layouts)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return autoCheckExitError
	}
	settings, words := autoCheckUserConfig(deps, stderr)
	d, err := autoCheckDetector(settings, words, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return autoCheckExitError
	}

	var res autoCheckResult
	var code int
	if events, err := autoCheckKeyEvents(text, layouts[idx]); err == nil {
		res, code = autoCheckEvaluate(events, layouts, idx, langs, d, settings.MaxLetters)
	} else {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		res = autoCheckResult{Shown: text, Verdict: autoCheckVerdictSkip, Reason: autoReasonUnrenderable,
			Engine: autoReasonUnrenderable}
		code = autoCheckExitUnrenderable
	}
	res.Active = FormatLayout(specs[idx])
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(res); err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return autoCheckExitError
	}
	return code
}

// autoCheckParseArgs returns the text (arguments joined by spaces, trailing
// spaces dropped) and the --shown value. "--" ends the options.
func autoCheckParseArgs(args []string) (text, shown string, err error) {
	var words []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			words = append(words, args[i+1:]...)
			i = len(args)
		case arg == "--shown":
			if i+1 >= len(args) {
				return "", "", errors.New("--shown needs a layout name")
			}
			i++
			shown = args[i]
		case strings.HasPrefix(arg, "--shown="):
			shown = strings.TrimPrefix(arg, "--shown=")
		case strings.HasPrefix(arg, "--"):
			return "", "", fmt.Errorf("unknown option %s", arg)
		default:
			words = append(words, arg)
		}
	}
	text = strings.TrimRight(strings.Join(words, " "), " ")
	if strings.TrimSpace(text) == "" {
		return "", "", errors.New("no text given")
	}
	return text, shown, nil
}

// autoCheckResolveLayouts returns the layout pair as the daemon resolves it:
// layout1/layout2 from the system config, else the desktop's layouts.
func autoCheckResolveLayouts() ([]LayoutSpec, error) {
	if c, err := cfg.LoadConfigFrom(cfg.DefaultConfigPath); err == nil && len(c.Layouts) == 2 {
		return c.Layouts, nil
	}
	layouts, err := GetCurrentLayouts(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get layouts: %w", err)
	}
	if len(layouts) != 2 {
		return nil, fmt.Errorf("need exactly 2 layouts, detected %d: %v (set layout1/layout2 in %s)",
			len(layouts), layouts, ConfigFile)
	}
	return layouts, nil
}

func autoCheckLoadLayouts(deps autoCheckDeps) ([]LayoutSpec, [2]*LayoutInfo, [2]lang.Lang, error) {
	var layouts [2]*LayoutInfo
	var langs [2]lang.Lang
	specs, err := deps.layouts()
	if err != nil {
		return nil, layouts, langs, err
	}
	if len(specs) != 2 {
		return nil, layouts, langs, fmt.Errorf("need exactly 2 layouts, got %d", len(specs))
	}
	for i, spec := range specs {
		l, ok := lang.LangForLayout(spec.Name)
		if !ok {
			return nil, layouts, langs, fmt.Errorf("layout %s has no language model (supported: us, ru)", FormatLayout(spec))
		}
		langs[i] = l
		if layouts[i], err = deps.load(spec.Name, spec.Variant); err != nil {
			return nil, layouts, langs, fmt.Errorf("failed to load layout %s: %w", FormatLayout(spec), err)
		}
	}
	if langs[0] == langs[1] {
		return nil, layouts, langs, fmt.Errorf("layouts %s and %s have the same language",
			FormatLayout(specs[0]), FormatLayout(specs[1]))
	}
	return specs, layouts, langs, nil
}

// autoCheckShownIndex picks the layout the text is shown in: the --shown
// layout name, else the only layout that can type the text, else layout 1.
func autoCheckShownIndex(text, shown string, specs []LayoutSpec, layouts [2]*LayoutInfo) (int, error) {
	if shown != "" {
		for i, spec := range specs {
			if strings.EqualFold(spec.Name, shown) || strings.EqualFold(FormatLayout(spec), shown) {
				return i, nil
			}
		}
		return 0, fmt.Errorf("--shown %s is not one of %s, %s", shown, FormatLayout(specs[0]), FormatLayout(specs[1]))
	}
	_, err0 := autoCheckKeyEvents(text, layouts[0])
	_, err1 := autoCheckKeyEvents(text, layouts[1])
	if err0 != nil && err1 == nil {
		return 1, nil
	}
	return 0, nil
}

// autoCheckUserConfig loads the user's settings and exception words; any
// problem is reported on stderr and the defaults are used.
func autoCheckUserConfig(deps autoCheckDeps, stderr io.Writer) (userconfig.Settings, []string) {
	dir, err := deps.userDir()
	if err != nil {
		fmt.Fprintf(stderr, "Warning: user configuration: %v\n", err)
		return userconfig.DefaultSettings(), nil
	}
	c, err := userconfig.Load(dir)
	if err != nil {
		fmt.Fprintf(stderr, "Warning: user configuration: %v\n", err)
	}
	for _, w := range c.Warnings {
		fmt.Fprintf(stderr, "Warning: %s\n", w)
	}
	return c.Settings, c.Words.Entries()
}

// autoCheckDetector builds the detector from the user's options, falling
// back to the defaults when they are out of range.
func autoCheckDetector(s userconfig.Settings, words []string, stderr io.Writer) (*lang.Detector, error) {
	opts := lang.Options{
		MinLetters: s.MinLetters,
		MinMargin:  s.Margin,
		MaxAltCost: s.MaxAltCost,
		ShortWords: s.ShortWords,
	}
	d, err := lang.NewDetector(opts)
	if err != nil {
		fmt.Fprintf(stderr, "Warning: %v; using the default detector options\n", err)
		if d, err = lang.NewDetector(lang.DefaultOptions()); err != nil {
			return nil, err
		}
	}
	d.SetUserExceptions(words)
	return d, nil
}

// autoCheckEvaluate runs the engine's word pipeline on a converter buffer
// that ends with Space: lastWordRange, then autoDecideWord (renderWord,
// lexicalVeto, shiftShapeVeto, detector, trailing-edge veto). The detector
// also runs when a veto stopped the pipeline before it, so that Verdict and
// Reason are always the detector's for a rendered word. CapsLock is off. The
// exit code is autoCheckExitUnrenderable when the word cannot be rendered.
func autoCheckEvaluate(events []KeyEvent, layouts [2]*LayoutInfo, idx int, langs [2]lang.Lang,
	d *lang.Detector, maxLetters int,
) (autoCheckResult, int) {
	var res autoCheckResult
	shown, alt := layouts[idx], layouts[1-idx]
	pair := lang.Pair{Shown: langs[idx], Alt: langs[1-idx]}
	r, reason, ok := lastWordRange(events, autoMaxLetters(AutoSettings{MaxLetters: maxLetters}))
	if !ok {
		res.Verdict, res.Reason, res.Engine = autoCheckVerdictSkip, reason, reason
		if r.End > r.Start {
			if w, err := renderWord(events, r.Start, r.TextEnd, shown, alt, false); err == nil {
				res.Shown, res.Alt = w.Shown, w.Alt
			}
		}
		if reason == "unrenderable-key" {
			return res, autoCheckExitUnrenderable
		}
		return res, autoCheckExitOK
	}
	w, dec, reason := autoDecideWord(events, r, shown, alt, pair, d, false)
	if reason == autoReasonUnrenderable {
		res.Verdict, res.Reason, res.Engine = autoCheckVerdictSkip, reason, reason
		return res, autoCheckExitUnrenderable
	}
	res.Shown, res.Alt = w.Shown, w.Alt
	if dec.Reason == "" {
		// A veto stopped the pipeline before the detector.
		dec = d.Decide(w.Shown, w.Alt, pair)
	}
	res.Verdict, res.Reason = dec.Verdict.String(), dec.Reason
	res.Engine = reason
	if reason == "" {
		res.Engine = autoCheckEngineArm
	}
	sc, scored := d.Scores(w.Shown, w.Alt, pair)
	if dec.Scores != nil {
		sc, scored = *dec.Scores, true
	}
	if scored {
		res.ShownCost, res.AltCost = &sc.ShownCost, &sc.AltCost
		res.ShownKnown, res.AltKnown = &sc.ShownKnown, &sc.AltKnown
	}
	return res, autoCheckExitOK
}

// autoCheckKeyEvents types text on layout l as a converter buffer: each rune
// becomes the K_DOWN of the first key (in scancode order) that produces it
// without Shift, else with LShift around it; ' ' is Space. A Space is
// appended. CapsLock is assumed off.
func autoCheckKeyEvents(text string, l *LayoutInfo) ([]KeyEvent, error) {
	events := make([]KeyEvent, 0, 3*len(text)+1)
	for _, r := range text {
		if r == ' ' {
			events = append(events, KeyEvent{Code: KEY_SPACE, Value: K_DOWN})
			continue
		}
		code, shift, ok := autoCheckKeyFor(l, r)
		if !ok {
			return nil, fmt.Errorf("%w: %q (%s)", errAutoCheckUnmapped, r, l.Name)
		}
		if shift {
			events = append(events,
				KeyEvent{Code: KEY_LEFTSHIFT, Value: K_DOWN},
				KeyEvent{Code: code, Value: K_DOWN},
				KeyEvent{Code: KEY_LEFTSHIFT, Value: K_UP})
		} else {
			events = append(events, KeyEvent{Code: code, Value: K_DOWN})
		}
	}
	return append(events, KeyEvent{Code: KEY_SPACE, Value: K_DOWN}), nil
}

// autoCheckKeyFor finds the key that types r on layout l, preferring the
// unshifted level.
func autoCheckKeyFor(l *LayoutInfo, r rune) (code uint16, shift, ok bool) {
	for level := range 2 {
		for _, c := range autoCheckKeyCodes {
			name, _ := XKBKeyName(c)
			runes := l.KeyMap[name]
			if !l.Unmapped[name] && level < len(runes) && runes[level] == r {
				return c, level == 1, true
			}
		}
	}
	return 0, false, false
}

// autoCheckKeyCodes lists the scancodes with an XKB key name, ascending.
var autoCheckKeyCodes = func() []uint16 {
	var codes []uint16
	for c := range uint16(256) {
		if _, ok := XKBKeyName(c); ok {
			codes = append(codes, c)
		}
	}
	return codes
}()
