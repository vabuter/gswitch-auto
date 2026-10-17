package runtime

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/arumata/gswitch/internal/gswitch/lang"
)

func TestAutoCheckKeyEvents(t *testing.T) {
	us, ru := autoTestLayouts()
	got, err := autoCheckKeyEvents("ok Ghbdtn?", us)
	if err != nil {
		t.Fatal(err)
	}
	want := autoTestScriptBuffer(t, "o k SP ^g h b d t n ^/ SP")
	if !slices.Equal(got, want) {
		t.Fatalf("autoCheckKeyEvents() =\n%v\nwant\n%v", got, want)
	}
	got, err = autoCheckKeyEvents("руддщ,", ru)
	if err != nil {
		t.Fatal(err)
	}
	if want := autoTestScriptBuffer(t, "h e l l o ^/ SP"); !slices.Equal(got, want) {
		t.Fatalf("autoCheckKeyEvents(ru) =\n%v\nwant\n%v", got, want)
	}
	// The unshifted level wins: ru '.' is AB10, not Shift+BKSL... ('/' is).
	got, err = autoCheckKeyEvents(".", ru)
	if err != nil || got[0] != (KeyEvent{Code: 53, Value: K_DOWN}) {
		t.Fatalf("autoCheckKeyEvents(.) = %v, %v", got, err)
	}
	if _, err := autoCheckKeyEvents("ghё", us); !errors.Is(err, errAutoCheckUnmapped) {
		t.Fatalf("unmapped rune error = %v", err)
	}
}

// autoTestCheckDeps returns RunAutoCheck dependencies with fixture layouts
// and the given user directory.
func autoTestCheckDeps(dir string, specs ...LayoutSpec) autoCheckDeps {
	if len(specs) == 0 {
		specs = []LayoutSpec{{Name: "us"}, {Name: "ru"}}
	}
	return autoCheckDeps{
		layouts: func() ([]LayoutSpec, error) { return specs, nil },
		load: func(name, _ string) (*LayoutInfo, error) {
			us, ru := autoTestLayouts()
			switch name {
			case "us":
				return us, nil
			case "ru":
				return ru, nil
			default:
				return nil, fmt.Errorf("no fixture for %s", name)
			}
		},
		userDir: func() (string, error) { return dir, nil },
	}
}

func autoTestRunCheck(t *testing.T, deps autoCheckDeps, args ...string) (autoCheckResult, int, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := autoCheckRun(args, &stdout, &stderr, deps)
	var res autoCheckResult
	if stdout.Len() > 0 {
		if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
			t.Fatalf("output %q is not JSON: %v", stdout.String(), err)
		}
		if strings.Count(stdout.String(), "\n") != 1 {
			t.Fatalf("output %q is not one line", stdout.String())
		}
	}
	return res, code, stderr.String()
}

func TestRunAutoCheck(t *testing.T) {
	dir := t.TempDir()
	deps := autoTestCheckDeps(dir)
	tests := []struct {
		args                 []string
		shown, alt, active   string
		verdict, reason      string
		code                 int
		scored               bool
		shownCost, altCost   float64
		shownKnown, altKnown bool
	}{
		{args: []string{"ghbdtn"}, shown: "ghbdtn", alt: "привет", active: "us", verdict: "fix", reason: "scores",
			scored: true, shownCost: 7.6142857, altCost: 2.8428571, altKnown: true},
		{args: []string{"руддщ"}, shown: "руддщ", alt: "hello", active: "ru", verdict: "fix", reason: "scores", scored: true},
		{args: []string{"--shown=us", "hello"}, shown: "hello", alt: "руддщ", active: "us", verdict: "keep", reason: "shown-known", scored: true},
		{args: []string{"ok", "ghbdtn?"}, shown: "ghbdtn?", alt: "привет,", active: "us", verdict: "fix", reason: "scores", scored: true},
		{args: []string{"jy"}, shown: "jy", alt: "он", active: "us", verdict: "fix", reason: "short-list"},
		{args: []string{"GTK"}, shown: "GTK", alt: "ПЕЛ", active: "us", verdict: "skip", reason: "shift-shape", scored: true},
		{args: []string{"--", "-elm"}, shown: "-elm", alt: "-удь", active: "us", verdict: "skip", reason: "lexical:leading-dash", scored: true},
		{args: []string{"g1"}, shown: "g1", alt: "п1", active: "us", verdict: "skip", reason: "lexical:digit"},
		{args: []string{"a"}, shown: "a", alt: "ф", active: "us", verdict: "skip", reason: "too-short"},
		{args: []string{"it."}, shown: "it.", alt: "шею", active: "us", verdict: "skip", reason: "edge-shown-known", scored: true},
		{args: []string{"vj."}, shown: "vj.", alt: "мою", active: "us", verdict: "fix", reason: "scores", scored: true},
		{args: []string{"dc."}, shown: "dc.", alt: "всю", active: "us", verdict: "fix", reason: "scores", scored: true},
		{args: []string{"--shown", "ru", "ghbdtn"}, shown: "ghbdtn", active: "ru", verdict: "skip", reason: "unrenderable", code: 2},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			res, code, stderr := autoTestRunCheck(t, deps, tt.args...)
			if code != tt.code {
				t.Fatalf("exit %d, want %d (stderr %q)", code, tt.code, stderr)
			}
			if res.Shown != tt.shown || res.Alt != tt.alt || res.Active != tt.active || res.Verdict != tt.verdict || res.Reason != tt.reason {
				t.Fatalf("result %+v, want %s/%s/%s %s:%s", res, tt.shown, tt.alt, tt.active, tt.verdict, tt.reason)
			}
			if (res.ShownCost != nil) != tt.scored || (res.AltKnown != nil) != tt.scored {
				t.Fatalf("scored = %v, want %v", res.ShownCost != nil, tt.scored)
			}
			if tt.shownCost != 0 && (math.Abs(*res.ShownCost-tt.shownCost) > 1e-6 || math.Abs(*res.AltCost-tt.altCost) > 1e-6 ||
				*res.ShownKnown != tt.shownKnown || *res.AltKnown != tt.altKnown) {
				t.Fatalf("scores %v/%v %v/%v", *res.ShownCost, *res.AltCost, *res.ShownKnown, *res.AltKnown)
			}
		})
	}
	t.Run("raw json", func(t *testing.T) {
		var stdout bytes.Buffer
		if code := autoCheckRun([]string{"ghbdtn&"}, &stdout, &bytes.Buffer{}, deps); code != 0 {
			t.Fatalf("exit %d", code)
		}
		out := stdout.String()
		for _, want := range []string{`"shown":"ghbdtn&"`, `"alt":"привет?"`, `"verdict":"fix"`, `"shown_known":false`} {
			if !strings.Contains(out, want) {
				t.Fatalf("output %s lacks %s", out, want)
			}
		}
	})
}

func TestRunAutoCheckUserConfig(t *testing.T) {
	dir := t.TempDir()
	deps := autoTestCheckDeps(dir)
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("auto.conf", "auto-max-letters=4\n")
	if res, code, _ := autoTestRunCheck(t, deps, "ghbdtn"); code != 0 || res.Verdict != "skip" || res.Reason != "too-long" {
		t.Fatalf("auto-max-letters not applied: %+v exit %d", res, code)
	}
	// ghbdtn: shown 7.61 - alt 2.84 < 5.
	write("auto.conf", "auto-margin=5\n")
	if res, _, _ := autoTestRunCheck(t, deps, "ghbdtn"); res.Verdict != "keep" || res.Reason != "margin" {
		t.Fatalf("auto-margin not applied: %+v", res)
	}
	write("auto.conf", "auto-margin=99\n") // out of range: default with a warning
	if res, _, stderr := autoTestRunCheck(t, deps, "ghbdtn"); res.Verdict != "fix" || !strings.Contains(stderr, "Warning: ") {
		t.Fatalf("bad auto-margin: %+v stderr %q", res, stderr)
	}
	write("auto.conf", "")
	write("exceptions-words.txt", "# mine\nGhbdtn,\n")
	if res, _, _ := autoTestRunCheck(t, deps, "Ghbdtn"); res.Verdict != "keep" || res.Reason != "exception" {
		t.Fatalf("exception not applied: %+v", res)
	}
	deps.userDir = func() (string, error) { return "", errors.New("no home") }
	res, code, stderr := autoTestRunCheck(t, deps, "ghbdtn")
	if code != 0 || res.Verdict != "fix" || !strings.Contains(stderr, "no home") {
		t.Fatalf("defaults without a user dir: %+v exit %d stderr %q", res, code, stderr)
	}
}

func TestRunAutoCheckErrors(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name string
		deps autoCheckDeps
		args []string
	}{
		{"no text", autoTestCheckDeps(dir), nil},
		{"blank text", autoTestCheckDeps(dir), []string{" "}},
		{"unknown option", autoTestCheckDeps(dir), []string{"--bogus", "x"}},
		{"shown without value", autoTestCheckDeps(dir), []string{"x", "--shown"}},
		{"shown not in pair", autoTestCheckDeps(dir), []string{"--shown", "de", "x"}},
		{"unsupported layout", autoTestCheckDeps(dir, LayoutSpec{Name: "us"}, LayoutSpec{Name: "de"}), []string{"x"}},
		{"same language", autoTestCheckDeps(dir, LayoutSpec{Name: "us"}, LayoutSpec{Name: "us", Variant: "intl"}), []string{"x"}},
		{"three layouts", autoTestCheckDeps(dir, LayoutSpec{Name: "us"}, LayoutSpec{Name: "ru"}, LayoutSpec{Name: "ua"}), []string{"x"}},
		{"layouts error", autoCheckDeps{
			layouts: func() ([]LayoutSpec, error) { return nil, errors.New("no session") },
		}, []string{"x"}},
		{"load error", autoCheckDeps{
			layouts: func() ([]LayoutSpec, error) { return []LayoutSpec{{Name: "us"}, {Name: "ru"}}, nil },
			load:    func(string, string) (*LayoutInfo, error) { return nil, errors.New("no xkb") },
		}, []string{"x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := autoCheckRun(tt.args, &stdout, &stderr, tt.deps); code != autoCheckExitError {
				t.Fatalf("exit %d, want %d", code, autoCheckExitError)
			}
			if stdout.Len() != 0 || !strings.HasPrefix(stderr.String(), "Error: ") {
				t.Fatalf("stdout %q stderr %q", stdout.String(), stderr.String())
			}
		})
	}
}

// autoTestEvalRow is one en/ru line of lang/testdata/layout_cases.tsv.
type autoTestEvalRow struct {
	split, class, text string
	shown              int // 0 = us, 1 = ru
	fix                bool
}

func autoTestEvalRows(t *testing.T) []autoTestEvalRow {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "lang", "testdata", "layout_cases.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var rows []autoTestEvalRow
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Split(sc.Text(), "\t")
		if len(fields) != 5 {
			t.Fatalf("malformed line %q", sc.Text())
		}
		row := autoTestEvalRow{split: fields[0], class: fields[1], text: fields[3], fix: fields[4] == "fix"}
		switch fields[2] {
		case "en":
			row.shown = 0
		case "ru":
			row.shown = 1
		default: // the Ukrainian model is not shipped
			continue
		}
		rows = append(rows, row)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1212 {
		t.Fatalf("%d en/ru rows, want 1212", len(rows))
	}
	return rows
}

// autoTestEvalCounts counts one split for one variant.
type autoTestEvalCounts struct {
	keeps, short2, short2All int
	ruWrong, ruAll           int
	enWrong, enAll           int
	falseFixes               []string
	edgeVetoed               []string // wrong-layout rows lost to the trailing-edge check
}

func (c *autoTestEvalCounts) add(row autoTestEvalRow, text string, fixed bool) {
	if !row.fix {
		c.keeps++
		if fixed {
			c.falseFixes = append(c.falseFixes, row.class+":"+text)
		}
		return
	}
	n := 0
	if fixed {
		n = 1
	}
	switch {
	case row.class == "ru_wrong":
		c.ruAll++
		c.ruWrong += n
	case row.class == "en_wrong":
		c.enAll++
		c.enWrong += n
	case row.class == "short_wrong" && len([]rune(row.text)) == 2:
		c.short2All++
		c.short2 += n
	}
}

func (c *autoTestEvalCounts) String() string {
	return fmt.Sprintf("keep-rows=%d false-fixes=%d ru_wrong=%d/%d en_wrong=%d/%d short_wrong(2 keys)=%d/%d",
		c.keeps, len(c.falseFixes), c.ruWrong, c.ruAll, c.enWrong, c.enAll, c.short2, c.short2All)
}

// autoTestEvalEngine runs one typed word through a fresh AutoEngine and
// reports whether it armed.
func autoTestEvalEngine(t *testing.T, layouts [2]*LayoutInfo, d *lang.Detector, shown int, buf []KeyEvent) (bool, string) {
	t.Helper()
	env := autoTestNewEnv(t)
	env.layouts = layouts
	env.layout.Index = shown
	env.start(env.deps(d))
	env.feedRaw(autoTestRaw(buf))
	return env.armed(), env.eng.Stats().LastReason
}

// TestAutoCheckEvalTSV is the integrated pipeline eval of §6.1 item 11: every
// en/ru row of the punto-rs labelled set, typed on the real XKB layouts, goes
// through lastWordRange, renderWord, the vetoes and Decide (the --auto-check
// path, cross-checked against the engine's OnSpace), bare and with the
// intended layout's ',' '.' '!' '?' key appended.
func TestAutoCheckEvalTSV(t *testing.T) {
	us, ru := autoTestRealLayouts(t)
	layouts := [2]*LayoutInfo{us, ru}
	langs := [2]lang.Lang{lang.En, lang.Ru}
	d, err := lang.NewDetector(lang.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	rows := autoTestEvalRows(t)
	variants := []string{"", ",", ".", "!", "?"}
	// The WP1 eval gates (lang/eval_test.go); the bare words typed here must
	// reach them for the comparison below to mean anything.
	wp1Gates := map[string][2]int{"test": {100, 93}, "dev": {99, 92}}
	for _, split := range []string{"dev", "test"} {
		var wp1 autoTestEvalCounts
		counts := make(map[string]*autoTestEvalCounts, len(variants))
		for _, v := range variants {
			counts[v] = &autoTestEvalCounts{}
		}
		for _, row := range rows {
			if row.split != split {
				continue
			}
			base, err := autoCheckKeyEvents(row.text, layouts[row.shown])
			if err != nil {
				t.Fatalf("%s: %v", row.text, err)
			}
			pair := lang.Pair{Shown: langs[row.shown], Alt: langs[1-row.shown]}
			w, err := renderWord(base, 0, len(base)-1, layouts[row.shown], layouts[1-row.shown], false)
			if err != nil {
				t.Fatalf("%s: %v", row.text, err)
			}
			wp1.add(row, row.text, d.Decide(w.Shown, w.Alt, pair).Verdict == lang.Fix)

			intended := row.shown
			if row.fix {
				intended = 1 - row.shown
			}
			for _, v := range variants {
				events := base
				if v != "" {
					code, shift, ok := autoCheckKeyFor(layouts[intended], []rune(v)[0])
					if !ok {
						t.Fatalf("%q is not on %s", v, layouts[intended].Name)
					}
					events = slices.Clone(base[:len(base)-1])
					if shift {
						events = append(events, KeyEvent{Code: KEY_LEFTSHIFT, Value: K_DOWN},
							KeyEvent{Code: code, Value: K_DOWN}, KeyEvent{Code: KEY_LEFTSHIFT, Value: K_UP})
					} else {
						events = append(events, KeyEvent{Code: code, Value: K_DOWN})
					}
					events = append(events, KeyEvent{Code: KEY_SPACE, Value: K_DOWN})
				}
				res, _ := autoCheckEvaluate(events, layouts, row.shown, langs, d, 24)
				fixed := res.Verdict == lang.Fix.String()
				if armed, why := autoTestEvalEngine(t, layouts, d, row.shown, events); armed != fixed {
					t.Errorf("%s %q: --auto-check says %s:%s, engine armed=%v (%s)",
						split, res.Shown, res.Verdict, res.Reason, armed, why)
				}
				counts[v].add(row, res.Shown, fixed)
				if row.fix && res.Reason == autoReasonEdgeKnown {
					counts[v].edgeVetoed = append(counts[v].edgeVetoed, row.class+":"+res.Shown)
				}
			}
		}
		t.Logf("%s WP1 bare Decide: %s", split, &wp1)
		if g := wp1Gates[split]; wp1.ruWrong < g[0] || wp1.enWrong < g[1] {
			t.Fatalf("%s: bare words typed on XKB reach ru=%d en=%d, below the WP1 gates %v", split, wp1.ruWrong, wp1.enWrong, g)
		}
		for _, v := range variants {
			c := counts[v]
			name := "bare"
			if v != "" {
				name = "+" + v
			}
			t.Logf("%s %-4s pipeline: %s; edge-shown-known vetoes %q", split, name, c, c.edgeVetoed)
			if len(c.falseFixes) != 0 {
				t.Errorf("%s %s: false fixes %q", split, name, c.falseFixes)
			}
			if c.ruWrong < wp1.ruWrong-2 {
				t.Errorf("%s %s: ru_wrong recall %d < WP1 bare %d - 2", split, name, c.ruWrong, wp1.ruWrong)
			}
			if c.enWrong < wp1.enWrong-2 {
				t.Errorf("%s %s: en_wrong recall %d < WP1 bare %d - 2", split, name, c.enWrong, wp1.enWrong)
			}
		}
	}
}
