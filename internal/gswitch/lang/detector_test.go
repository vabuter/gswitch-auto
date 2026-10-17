package lang

import (
	"errors"
	"math"
	"strconv"
	"sync"
	"testing"
)

var (
	detectorTestEnRu = Pair{Shown: En, Alt: Ru}
	detectorTestRuEn = Pair{Shown: Ru, Alt: En}
)

func detectorTestNew(t *testing.T, opts Options) *Detector {
	t.Helper()
	d, err := NewDetector(opts)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// detectorTestDecide types text in the shown layout of pair (rows oracle)
// and decides it.
func detectorTestDecide(t *testing.T, d *Detector, pair Pair, text string) Decision {
	t.Helper()
	shown, alt := rowsTestWord(t, pair, text)
	return d.Decide(shown, alt, pair)
}

// detectorTestWrong is punto-rs wrong_layout(keys_for(pair.shown, text), pair).
func detectorTestWrong(t *testing.T, d *Detector, pair Pair, text string) bool {
	t.Helper()
	return detectorTestDecide(t, d, pair, text).Verdict == Fix
}

// detectorTestShortWrong is punto-rs short_wrong (exception check included).
func detectorTestShortWrong(t *testing.T, d *Detector, pair Pair, text string) bool {
	t.Helper()
	shown, alt := rowsTestWord(t, pair, text)
	return !d.IsException(shown, pair.Shown) && shortWrong(typedWord{shown: []rune(shown), alt: []rune(alt), pair: pair})
}

// --- Ported punto-rs cli/src/layout/tests.rs assertions (EN/RU pair) ---

// tests.rs:78-85 (the keymap itself lives in the rows oracle).
func TestKeyCharGhbdtnInRuGivesPrivet(t *testing.T) {
	_, alt := rowsTestWord(t, detectorTestEnRu, "ghbdtn")
	if alt != "привет" {
		t.Errorf("ghbdtn in ru = %q", alt)
	}
	if _, ok := rowsTestKeyChar(Ru, 1, false); ok {
		t.Error("key 1 must have no character")
	}
}

// tests.rs:87-92.
func TestWrongLayoutGhbdtnOnEnSwitchedPrivetOnRuKept(t *testing.T) {
	d := detectorTestNew(t, DefaultOptions())
	if !detectorTestWrong(t, d, detectorTestEnRu, "ghbdtn") {
		t.Error("ghbdtn on en must be switched")
	}
	// The same keys with the ru layout active: привет is on screen.
	if detectorTestWrong(t, d, detectorTestRuEn, "привет") {
		t.Error("привет on ru must be kept")
	}
}

// tests.rs:94-105.
func TestWrongLayoutShortWordsByListSingleLetterNever(t *testing.T) {
	d := detectorTestNew(t, DefaultOptions())
	for _, tc := range []struct {
		pair Pair
		text string
		want bool
	}{
		{detectorTestEnRu, "jy", true},
		{detectorTestRuEn, "шы", true},
		{detectorTestEnRu, "on", false},
		{detectorTestRuEn, "он", false},
		{detectorTestEnRu, "F", false},
	} {
		if got := detectorTestWrong(t, d, tc.pair, tc.text); got != tc.want {
			t.Errorf("wrong_layout(%s) = %v, want %v", tc.text, got, tc.want)
		}
	}
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"F", true},
		{"a", false},
		{"t`", true},
	} {
		if got := detectorTestShortWrong(t, d, detectorTestEnRu, tc.text); got != tc.want {
			t.Errorf("short_wrong(%s) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

// tests.rs:107-111.
func TestScoresDigitsOrShortWordNotCandidate(t *testing.T) {
	d := detectorTestNew(t, DefaultOptions())
	for _, text := range []string{"ghb2", "yt"} {
		shown, alt := rowsTestWord(t, detectorTestEnRu, text)
		if sc, ok := d.Scores(shown, alt, detectorTestEnRu); ok {
			t.Errorf("Scores(%s) = %+v, want not a candidate", text, sc)
		}
	}
	if dec := detectorTestDecide(t, d, detectorTestEnRu, "ghb2"); dec.Verdict != NotCandidate ||
		dec.Reason != ReasonDigits || dec.Scores != nil {
		t.Errorf("ghb2: %+v, want NotCandidate digits", dec)
	}
}

// tests.rs:113-123.
func TestKnownDictionaryWordFoundGibberishNot(t *testing.T) {
	d := detectorTestNew(t, DefaultOptions())
	if !d.Known(Ru, "привет") || !d.Known(En, "hello") {
		t.Error("привет and hello are dictionary words")
	}
	if d.Known(Ru, "ршщзх") || d.Known(En, "ghbdtn") {
		t.Error("ршщзх and ghbdtn are not dictionary words")
	}
	if d.Known(En, "") || d.Known(En, "hello!") || d.Known(Unknown, "hello") || d.Known(En, "привет") {
		t.Error("empty, punctuated, unsupported-language and foreign words are unknown")
	}
	if !d.Known(En, "Hello") || !d.Known(Ru, "ЁЖИК") {
		t.Error("case and ё must not matter")
	}
	if c, ok := d.Cost(Ru, "ршщзх"); !ok || c != 9.1 {
		t.Errorf("Cost(ru, ршщзх) = %v, %v", c, ok)
	}
	if _, ok := d.Cost(En, "x-ray"); ok {
		t.Error("Cost of a word with a non-letter must fail")
	}
}

// tests.rs:125-137.
func TestWrongLayoutExceptionWordNotSwitched(t *testing.T) {
	d := detectorTestNew(t, DefaultOptions())
	for _, tc := range []struct {
		pair Pair
		text string
	}{
		{detectorTestRuEn, "афк"},
		{detectorTestRuEn, "ща"},
		{detectorTestRuEn, "Ща"},
		{detectorTestRuEn, "ща,"},
		{detectorTestRuEn, "ру"},
		{detectorTestEnRu, "tls"},
		{detectorTestEnRu, "TLS"},
	} {
		dec := detectorTestDecide(t, d, tc.pair, tc.text)
		if dec.Verdict != Keep || dec.Reason != ReasonException || dec.Scores != nil {
			t.Errorf("%s: %+v, want Keep exception", tc.text, dec)
		}
	}
	for _, text := range []string{"ру", "ин"} {
		if detectorTestShortWrong(t, d, detectorTestRuEn, text) {
			t.Errorf("short_wrong(%s) must be false for an exception", text)
		}
	}
}

// tests.rs:139-149.
func TestWrongLayoutTypoStillSwitchedDespiteExceptions(t *testing.T) {
	d := detectorTestNew(t, DefaultOptions())
	for _, tc := range []struct {
		pair Pair
		text string
	}{
		{detectorTestEnRu, "ghbdtn"},
		{detectorTestEnRu, "nfr"},
		{detectorTestEnRu, "xnj"},
		{detectorTestRuEn, "пше"},
		{detectorTestRuEn, "пщ"},
		{detectorTestEnRu, "jy"},
		{detectorTestEnRu, "negb"},
	} {
		if !detectorTestWrong(t, d, tc.pair, tc.text) {
			t.Errorf("%s must be switched", tc.text)
		}
	}
}

// tests.rs:206.
func TestShortWrongWtIsNotRussian(t *testing.T) {
	d := detectorTestNew(t, DefaultOptions())
	if detectorTestShortWrong(t, d, detectorTestEnRu, "wt") {
		t.Error("wt (цу) is not a Russian short word")
	}
}

// tests.rs:216-219: key-by-key rendering of a Russian-layout text in the US
// layout (convert_text itself is not ported; rendering is the caller's job).
func TestRenderRussianLayoutTextInUS(t *testing.T) {
	var got []string
	for _, w := range []string{"руддщ", "Цщкдвю"} {
		_, alt := rowsTestWord(t, detectorTestRuEn, w)
		got = append(got, alt)
	}
	if got[0]+" "+got[1] != "hello World." {
		t.Errorf("got %q", got)
	}
}

// tests.rs:227-245, re-targeted to the EN/RU pair.
func TestWrongLayoutHyphenatedWordFixedByParts(t *testing.T) {
	d := detectorTestNew(t, DefaultOptions())
	if dec := detectorTestDecide(t, d, detectorTestEnRu, "nfr-nfrb"); dec.Verdict != Fix || dec.Reason != ReasonCompound {
		t.Errorf("nfr-nfrb: %+v, want Fix compound", dec)
	}
	for _, text := range []string{"e-mail", "t-shirt", "self-made", "well-known", "x-ray", "-elm"} {
		if detectorTestWrong(t, d, detectorTestEnRu, text) {
			t.Errorf("%s must be kept", text)
		}
	}
}

// --- gswitch-auto specific behaviour (SPEC §3.3, §6.2) ---

func TestScoresGolden(t *testing.T) {
	d := detectorTestNew(t, DefaultOptions())
	for _, tc := range []struct {
		text string
		want Scores
	}{
		{"ghbdtn", Scores{7.614285714285714, 2.8428571428571425, false, true}},
		{"tls", Scores{6.375, 5.175, false, true}},
		{"dnf", Scores{10.325, 4.275, false, false}},
	} {
		shown, alt := rowsTestWord(t, detectorTestEnRu, tc.text)
		got, ok := d.Scores(shown, alt, detectorTestEnRu)
		if !ok || got != tc.want {
			t.Errorf("Scores(%s) = %+v, %v, want %+v", tc.text, got, ok, tc.want)
		}
	}
	// Spec values rounded to 7 decimals.
	shown, alt := rowsTestWord(t, detectorTestEnRu, "ghbdtn")
	got, _ := d.Scores(shown, alt, detectorTestEnRu)
	if math.Abs(got.ShownCost-7.6142857) > 5e-8 || math.Abs(got.AltCost-2.8428571) > 5e-8 {
		t.Errorf("ghbdtn scores %+v differ from the spec", got)
	}
}

func TestDecideReasons(t *testing.T) {
	d := detectorTestNew(t, DefaultOptions())
	for _, tc := range []struct {
		pair    Pair
		text    string
		verdict Verdict
		reason  string
		scored  bool
	}{
		{detectorTestEnRu, "ghbdtn", Fix, ReasonScores, true},
		{detectorTestEnRu, "jy", Fix, ReasonShortList, false},
		{detectorTestEnRu, "nfr-nfrb", Fix, ReasonCompound, false},
		{detectorTestEnRu, "tls", Keep, ReasonException, false},
		{detectorTestRuEn, "привет", Keep, ReasonShownKnown, true},
		{detectorTestEnRu, "dnf", Keep, ReasonAltUnknown, true},
		{detectorTestEnRu, "ghb2", NotCandidate, ReasonDigits, false},
		{detectorTestEnRu, "gh_bd", NotCandidate, ReasonNonLetter, false},
		{detectorTestEnRu, "on", NotCandidate, ReasonTooShort, false},
		{detectorTestRuEn, "..", NotCandidate, ReasonUnreadableAlt, false},
	} {
		dec := detectorTestDecide(t, d, tc.pair, tc.text)
		if dec.Verdict != tc.verdict || dec.Reason != tc.reason || (dec.Scores != nil) != tc.scored {
			t.Errorf("%s: %v %q scored=%v, want %v %q scored=%v",
				tc.text, dec.Verdict, dec.Reason, dec.Scores != nil, tc.verdict, tc.reason, tc.scored)
		}
	}
}

func TestDecideKeepBlockersAltCostAndMargin(t *testing.T) {
	d := detectorTestNew(t, DefaultOptions())
	shown, alt := rowsTestWord(t, detectorTestEnRu, "ghbdtn")
	// Alt cost 2.84 > 2.5 is outside the validated range; use decideWith.
	o := DefaultOptions()
	o.MaxAltCost = 2.5
	if dec := d.decideWith(shown, alt, detectorTestEnRu, &o); dec.Verdict != Keep || dec.Reason != ReasonAltCost {
		t.Errorf("max alt cost 2.5: %+v", dec)
	}
	o = DefaultOptions()
	o.MinMargin = 5
	if err := d.SetOptions(o); err != nil {
		t.Fatal(err)
	}
	if dec := d.Decide(shown, alt, detectorTestEnRu); dec.Verdict != Keep || dec.Reason != ReasonMargin {
		t.Errorf("margin 5: %+v", dec)
	}
}

func TestDecideMinLettersGatesOnlyTheScorePath(t *testing.T) {
	o := DefaultOptions()
	o.MinLetters = 4
	d := detectorTestNew(t, o)
	if dec := detectorTestDecide(t, d, detectorTestEnRu, "ghb"); dec.Verdict != NotCandidate || dec.Reason != ReasonTooShort {
		t.Errorf("MinLetters=4 ghb: %+v, want NotCandidate too-short", dec)
	}
	o.MinLetters = 8
	if err := d.SetOptions(o); err != nil {
		t.Fatal(err)
	}
	if dec := detectorTestDecide(t, d, detectorTestEnRu, "jy"); dec.Verdict != Fix || dec.Reason != ReasonShortList {
		t.Errorf("MinLetters=8 jy: %+v, want Fix short-list", dec)
	}
	shown, alt := rowsTestWord(t, detectorTestEnRu, "nfr-nfrb")
	if _, why, ok := scoreWord(typedWord{shown: []rune(shown), alt: []rune(alt), pair: detectorTestEnRu}, &o); ok || why != ReasonNonLetter {
		t.Errorf("score path alone for nfr-nfrb: ok=%v why=%q, want non-letter", ok, why)
	}
	if dec := d.Decide(shown, alt, detectorTestEnRu); dec.Verdict != Fix || dec.Reason != ReasonCompound {
		t.Errorf("MinLetters=8 nfr-nfrb: %+v, want Fix compound", dec)
	}
	if dec := detectorTestDecide(t, d, detectorTestEnRu, "ghbdtn"); dec.Verdict != NotCandidate || dec.Reason != ReasonTooShort {
		t.Errorf("MinLetters=8 ghbdtn: %+v, want NotCandidate too-short", dec)
	}
}

func TestDecideShortWordsOff(t *testing.T) {
	o := DefaultOptions()
	o.ShortWords = false
	d := detectorTestNew(t, o)
	if dec := detectorTestDecide(t, d, detectorTestEnRu, "jy"); dec.Verdict != NotCandidate || dec.Reason != ReasonTooShort {
		t.Errorf("ShortWords=false jy: %+v, want NotCandidate", dec)
	}
	if !detectorTestWrong(t, d, detectorTestEnRu, "ghbdtn") {
		t.Error("ShortWords=false must not affect the score path")
	}
}

func TestDecideEdgePunctuationStrippedByCore(t *testing.T) {
	d := detectorTestNew(t, DefaultOptions())
	bare := d.Decide("ghbdtn", "привет", detectorTestEnRu)
	punct := d.Decide("ghbdtn?", "привет,", detectorTestEnRu)
	if bare.Verdict != Fix || punct.Verdict != bare.Verdict || *punct.Scores != *bare.Scores {
		t.Errorf("ghbdtn? = %+v, ghbdtn = %+v", punct, bare)
	}
	// One rune per key: "ghbdtn?" is 7 keys, "привет," is 7 runes.
	if dec := d.Decide("ghbdtn?", "привет", detectorTestEnRu); dec.Verdict != NotCandidate || dec.Reason != ReasonUnreadableAlt {
		t.Errorf("rune count mismatch: %+v", dec)
	}
}

func TestUserExceptions(t *testing.T) {
	d := detectorTestNew(t, DefaultOptions())
	if d.IsException("Ghbdtn,", En) {
		t.Fatal("ghbdtn is not a built-in exception")
	}
	d.SetUserExceptions([]string{"# comment", "", "  ", "ghbdtn", "#ytn"})
	if !d.IsException("Ghbdtn,", En) {
		t.Error("IsException(Ghbdtn,) after SetUserExceptions([ghbdtn])")
	}
	if d.IsException("ytn", En) || d.IsException("comment", En) {
		t.Error("comment lines must be dropped")
	}
	if dec := d.Decide("ghbdtn", "привет", detectorTestEnRu); dec.Verdict != Keep || dec.Reason != ReasonException {
		t.Errorf("user exception: %+v", dec)
	}
	// Built-in exceptions stay active next to the user list.
	if !d.IsException("TLS", En) {
		t.Error("built-in tls lost")
	}
	d.SetUserExceptions(nil)
	if d.IsException("ghbdtn", En) {
		t.Error("SetUserExceptions(nil) must clear the user list")
	}
}

func TestUserExceptionsFoldYo(t *testing.T) {
	d := detectorTestNew(t, DefaultOptions())
	d.SetUserExceptions([]string{"Ёжик"})
	if !d.IsException("ежик", Ru) || !d.IsException("ЁЖИК!", Ru) {
		t.Error("ёжик must be stored and matched as ежик")
	}
}

func TestAddUserException(t *testing.T) {
	d := detectorTestNew(t, DefaultOptions())
	d.AddUserException("  ")
	d.AddUserException("...")
	if d.user.Load() != nil {
		t.Error("words without letters must not be added")
	}
	d.AddUserException("Ghbdtn!")
	before := d.user.Load()
	if !d.IsException("ghbdtn", En) {
		t.Fatal("added word not matched")
	}
	d.AddUserException("ghbdtn")
	if d.user.Load() != before {
		t.Error("re-adding an existing word must not copy the set")
	}
	d.AddUserException("ntcn")
	if !d.IsException("ntcn", En) || !d.IsException("ghbdtn", En) {
		t.Error("copy-on-write add lost a word")
	}
	if len(*before) != 1 {
		t.Error("the previous set must not be modified")
	}
}

func TestDetectorConcurrentUse(t *testing.T) {
	d := detectorTestNew(t, DefaultOptions())
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 200 {
				_ = d.Decide("ghbdtn", "привет", detectorTestEnRu)
				_ = d.Decide("nfr-nfrb", "так-таки", detectorTestEnRu)
			}
		}()
		go func() {
			defer wg.Done()
			for j := range 50 {
				d.AddUserException("word" + strconv.Itoa(i*100+j))
				d.SetUserExceptions([]string{"x" + strconv.Itoa(j)})
				o := DefaultOptions()
				o.MinMargin = float64(j%5) / 2
				_ = d.SetOptions(o)
			}
		}()
	}
	wg.Wait()
}

func TestUnsupportedPair(t *testing.T) {
	d := detectorTestNew(t, DefaultOptions())
	for _, p := range []Pair{{En, En}, {Ru, Ru}, {Unknown, Ru}, {En, Unknown}, {}} {
		if dec := d.Decide("ghbdtn", "привет", p); dec.Verdict != NotCandidate || dec.Reason != ReasonUnsupportedPair {
			t.Errorf("pair %+v: %+v", p, dec)
		}
		if _, ok := d.Scores("ghbdtn", "привет", p); ok {
			t.Errorf("pair %+v scored", p)
		}
	}
}

func TestZeroDetectorUsesDefaults(t *testing.T) {
	var d Detector
	if dec := d.Decide("ghbdtn", "привет", detectorTestEnRu); dec.Verdict != Fix {
		t.Errorf("zero detector: %+v", dec)
	}
	if dec := d.Decide("tls", "еды", detectorTestEnRu); dec.Reason != ReasonException {
		t.Errorf("zero detector built-in exception: %+v", dec)
	}
}

func TestBuiltinExceptions(t *testing.T) {
	if len(builtinExceptions) != 20 {
		t.Errorf("%d built-in exceptions, want 20", len(builtinExceptions))
	}
	for _, w := range []string{"afk", "tls", "wtype", "афк", "ща", "щаа", "еше"} {
		if _, ok := builtinExceptions[w]; !ok {
			t.Errorf("built-in exception %q missing", w)
		}
	}
}

func TestOptionsValidate(t *testing.T) {
	if err := DefaultOptions().Validate(); err != nil {
		t.Fatal(err)
	}
	def := DefaultOptions()
	if def.MinLetters != 3 || def.MinMargin != 1.0 || def.MaxAltCost != 7.0 || !def.ShortWords {
		t.Errorf("defaults %+v differ from punto-rs", def)
	}
	for _, mut := range []func(*Options){
		func(o *Options) { o.MinLetters = 1 },
		func(o *Options) { o.MinLetters = 9 },
		func(o *Options) { o.MinMargin = -0.1 },
		func(o *Options) { o.MinMargin = 5.1 },
		func(o *Options) { o.MinMargin = math.NaN() },
		func(o *Options) { o.MaxAltCost = 2.9 },
		func(o *Options) { o.MaxAltCost = 25.6 },
		func(o *Options) { o.MaxAltCost = math.Inf(1) },
	} {
		o := DefaultOptions()
		mut(&o)
		err := o.Validate()
		if !errors.Is(err, ErrInvalidOptions) {
			t.Errorf("%+v: err = %v, want ErrInvalidOptions", o, err)
		}
		if _, err := NewDetector(o); err == nil {
			t.Errorf("NewDetector(%+v) accepted invalid options", o)
		}
		d := detectorTestNew(t, DefaultOptions())
		if err := d.SetOptions(o); err == nil {
			t.Errorf("SetOptions(%+v) accepted invalid options", o)
		}
		if *d.options() != DefaultOptions() {
			t.Error("rejected options must not be stored")
		}
	}
	for _, o := range []Options{
		{MinLetters: 2, MinMargin: 0, MaxAltCost: 3},
		{MinLetters: 8, MinMargin: 5, MaxAltCost: 25.5, ShortWords: true},
	} {
		if err := o.Validate(); err != nil {
			t.Errorf("%+v: %v", o, err)
		}
	}
}

func TestLangForLayout(t *testing.T) {
	for _, tc := range []struct {
		name string
		want Lang
		ok   bool
	}{
		{"us", En, true},
		{"ru", Ru, true},
		{"ru(phonetic)", Ru, true},
		{"us+intl", En, true},
		{" US ", En, true},
		{"ua", Unknown, false},
		{"de", Unknown, false},
		{"", Unknown, false},
		{"russian", Unknown, false},
	} {
		got, ok := LangForLayout(tc.name)
		if got != tc.want || ok != tc.ok {
			t.Errorf("LangForLayout(%q) = %v, %v, want %v, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
	if En.String() != "en" || Ru.String() != "ru" || Unknown.String() != "unknown" {
		t.Error("Lang.String")
	}
	if Fix.String() != "fix" || Keep.String() != "keep" || NotCandidate.String() != "not-candidate" {
		t.Error("Verdict.String")
	}
}

func BenchmarkDecide(b *testing.B) {
	d, err := NewDetector(DefaultOptions())
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if d.Decide("ghbdtn", "привет", detectorTestEnRu).Verdict != Fix {
			b.Fatal("ghbdtn must be fixed")
		}
	}
}

func BenchmarkDecideCompound(b *testing.B) {
	d, err := NewDetector(DefaultOptions())
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = d.Decide("nfr-nfrb", "так-таки", detectorTestEnRu)
	}
}
