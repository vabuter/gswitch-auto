package lang

import (
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/arumata/gswitch/internal/gswitch/fold"
)

// stackRunes is the word length up to which Decide keeps its rune and letter
// buffers on the stack; longer words still work through append.
const stackRunes = 64

// Closed lists of frequent 1-2 letter words (keymap.rs:79-88). Statistics do
// not work for them, so a 2-key word is fixed by list membership.
var (
	enShortWords = newWordSet(
		"a", "i", "am", "an", "as", "at", "be", "by", "do", "go", "he", "hi", "if", "in", "is", "it",
		"me", "my", "no", "of", "oh", "ok", "on", "or", "so", "to", "up", "us", "we",
	)
	ruShortWords = newWordSet(
		"а", "в", "и", "к", "о", "с", "у", "я", "ж", "бы", "во", "вы", "да", "до", "ее", "ей", "же",
		"за", "из", "им", "их", "ко", "ли", "мы", "на", "не", "ни", "но", "ну", "об", "ой", "он", "от",
		"по", "со", "та", "те", "то", "ты", "уж", "ах",
	)
)

func newWordSet(words ...string) wordSet {
	set := make(wordSet, len(words))
	for _, w := range words {
		set[w] = struct{}{}
	}
	return set
}

func shortWords(l Lang) wordSet {
	switch l {
	case En:
		return enShortWords
	case Ru:
		return ruShortWords
	default:
		return nil
	}
}

var defaultOptions = DefaultOptions()

// Detector decides whether a word was typed in the wrong layout. It is safe
// for concurrent use: options and the user exception list are swapped
// atomically. The zero value uses DefaultOptions and the built-in exceptions;
// NewDetector additionally validates the embedded models.
type Detector struct {
	opts   atomic.Pointer[Options]
	user   atomic.Pointer[wordSet]
	userMu sync.Mutex // serialises user-list writers (copy-on-write)
}

// NewDetector validates opts and the embedded model blobs (27³ and 33³ byte
// trigram tables, non-empty Bloom filters) and returns a detector with the
// built-in exception list and an empty user list.
func NewDetector(opts Options) (*Detector, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	for _, m := range []*model{enModel, ruModel} {
		if err := m.validate(); err != nil {
			return nil, err
		}
	}
	d := &Detector{}
	d.opts.Store(&opts)
	return d, nil
}

// SetOptions validates opts and swaps them in atomically (hot reload).
func (d *Detector) SetOptions(opts Options) error {
	if err := opts.Validate(); err != nil {
		return err
	}
	d.opts.Store(&opts)
	return nil
}

func (d *Detector) options() *Options {
	if o := d.opts.Load(); o != nil {
		return o
	}
	return &defaultOptions
}

// typedWord is a typed word rendered in both layouts, one rune per key.
type typedWord struct {
	shown, alt []rune
	pair       Pair
}

func (w typedWord) slice(from, to int) typedWord {
	return typedWord{shown: w.shown[from:to], alt: w.alt[from:to], pair: w.pair}
}

// isHyphen: the key gives '-' in both layouts (layout.rs:260-263).
func (w typedWord) isHyphen(i int) bool {
	return w.shown[i] == '-' && w.alt[i] == '-'
}

// makeWord decodes both spellings into the given buffers. It fails when they
// do not have one rune per key each (different rune counts).
func makeWord(shown, alt string, pair Pair, sbuf, abuf []rune) (typedWord, bool) {
	for _, r := range shown {
		sbuf = append(sbuf, r)
	}
	for _, r := range alt {
		abuf = append(abuf, r)
	}
	return typedWord{shown: sbuf, alt: abuf, pair: pair}, len(sbuf) == len(abuf)
}

// judgement is the allocation-free form of a Decision.
type judgement struct {
	verdict Verdict
	reason  string
	scores  Scores
	scored  bool
}

// Decide judges a word rendered in the active layout (shown) and in the other
// layout (alt), one rune per key, edge punctuation included. It mirrors
// punto-rs wrong_layout (layout.rs:232-254): after the pair and exception
// checks, the score path, the 2-key short-list path and the hyphen-compound
// path are OR-ed independently, so the candidate filter of the score path
// (digits, non-letter, too-short, unreadable-alt) gates neither of the other
// two.
func (d *Detector) Decide(shown, alt string, pair Pair) Decision {
	return d.decideWith(shown, alt, pair, d.options())
}

// decideWith is Decide with explicit options (tests use it for threshold grids
// outside the validated ranges).
func (d *Detector) decideWith(shown, alt string, pair Pair, o *Options) Decision {
	if !pair.supported() {
		return Decision{Verdict: NotCandidate, Reason: ReasonUnsupportedPair}
	}
	if d.IsException(shown, pair.Shown) {
		return Decision{Verdict: Keep, Reason: ReasonException}
	}
	var sbuf, abuf [stackRunes]rune
	w, ok := makeWord(shown, alt, pair, sbuf[:0], abuf[:0])
	if !ok {
		return Decision{Verdict: NotCandidate, Reason: ReasonUnreadableAlt}
	}
	j := d.judge(w, o)
	dec := Decision{Verdict: j.verdict, Reason: j.reason}
	if j.scored {
		sc := j.scores
		dec.Scores = &sc
	}
	return dec
}

// judge runs steps 3-6 of Decide; the exception check is the caller's.
func (d *Detector) judge(w typedWord, o *Options) judgement {
	sc, why, ok := scoreWord(w, o)
	j := judgement{scores: sc, scored: ok}
	if ok {
		blocker := switchBlocker(sc, o)
		if blocker == "" {
			j.verdict, j.reason = Fix, ReasonScores
			return j
		}
		j.verdict, j.reason = Keep, blocker
	} else {
		j.verdict, j.reason = NotCandidate, why
	}
	// Closed lists decide 2-key words; a single key is never fixed (layout.rs:249-252).
	if o.ShortWords && len(w.shown) == 2 && shortWrong(w) && !shownKnown(w) {
		j.verdict, j.reason = Fix, ReasonShortList
		return j
	}
	if d.compound(w, o) {
		j.verdict, j.reason = Fix, ReasonCompound
	}
	return j
}

// Scores returns the score-path values of a word (punto-rs scores,
// layout.rs:197-226); false when the pair is unsupported or the word is not
// a score candidate.
func (d *Detector) Scores(shown, alt string, pair Pair) (Scores, bool) {
	if !pair.supported() {
		return Scores{}, false
	}
	var sbuf, abuf [stackRunes]rune
	w, ok := makeWord(shown, alt, pair, sbuf[:0], abuf[:0])
	if !ok {
		return Scores{}, false
	}
	sc, _, ok := scoreWord(w, d.options())
	return sc, ok
}

// scoreWord is punto-rs scores (layout.rs:197-226) with the reason why a word
// is not a candidate.
func scoreWord(w typedWord, o *Options) (Scores, string, bool) {
	for _, r := range w.shown {
		if r >= '0' && r <= '9' {
			return Scores{}, ReasonDigits, false
		}
	}
	var abuf, sbuf [stackRunes]uint8
	altCore, why := core(w.pair.Alt, w.alt, abuf[:0])
	if why != "" {
		return Scores{}, why, false
	}
	if len(altCore) < o.MinLetters {
		return Scores{}, ReasonTooShort, false
	}
	shownModel, altModel := modelFor(w.pair.Shown), modelFor(w.pair.Alt)
	sc := Scores{
		ShownCost: unreadableCost,
		AltCost:   altModel.cost(altCore),
		AltKnown:  altModel.known(altCore),
	}
	// An edge that is a letter only in the alt layout ("ls]") makes the shown
	// core shorter than the alt core; the shown dictionary does not excuse it.
	if shownCore, why := core(w.pair.Shown, w.shown, sbuf[:0]); why == "" {
		sc.ShownCost = shownModel.cost(shownCore)
		sc.ShownKnown = len(shownCore) == len(altCore) && shownModel.known(shownCore)
	}
	return sc, "", true
}

// switchBlocker returns the first condition of punto-rs should_switch
// (layout.rs:189-194) that fails, or "" when the word should be switched.
func switchBlocker(sc Scores, o *Options) string {
	switch {
	case sc.ShownKnown:
		return ReasonShownKnown
	case !sc.AltKnown:
		return ReasonAltUnknown
	case sc.AltCost > o.MaxAltCost:
		return ReasonAltCost
	case sc.ShownCost-sc.AltCost < o.MinMargin: // costs are finite, so < is !(>=)
		return ReasonMargin
	default:
		return ""
	}
}

// shortWrong is punto-rs short_wrong (layout.rs:296-307) without its
// exception check: a word of at most two keys that is a listed short word in
// the alt layout and not in the shown one.
func shortWrong(w typedWord) bool {
	return len(w.shown) <= 2 && isShortWord(w.pair.Alt, w.alt) && !isShortWord(w.pair.Shown, w.shown)
}

func isShortWord(l Lang, text []rune) bool {
	var buf [4 * utf8.UTFMax]byte
	b := buf[:0]
	for _, r := range text {
		b = utf8.AppendRune(b, foldRune(r))
	}
	_, ok := shortWords(l)[string(b)]
	return ok
}

// shownKnown is punto-rs shown_known (layout.rs:285-294): the whole shown
// spelling, edges included, is a dictionary word.
func shownKnown(w typedWord) bool {
	var buf [stackRunes]uint8
	idx, ok := letters(w.pair.Shown, w.shown, buf[:0])
	return ok && modelFor(w.pair.Shown).known(idx)
}

// compound is punto-rs wrong_compound (layout.rs:256-273): a hyphenated word
// is fixed when every part is a dictionary word or a listed short word in the
// alt layout and at least one part is fixed on its own.
//
// Options.MinLetters is the candidate filter of the top-level score path
// only; the parts are always scored with punto-rs MIN_LETTERS, so raising
// MinLetters never disables compounds (as it never disables the short-list
// path). The other options apply to the parts unchanged.
func (d *Detector) compound(w typedWord, o *Options) bool {
	hyphens := false
	for i := range w.shown {
		if w.isHyphen(i) {
			hyphens = true
			break
		}
	}
	if !hyphens {
		return false
	}
	partOpts := *o
	partOpts.MinLetters = puntoMinLetters
	o = &partOpts
	fixed := false
	start := 0
	for i := 0; i <= len(w.shown); i++ {
		if i < len(w.shown) && !w.isHyphen(i) {
			continue
		}
		part := w.slice(start, i)
		start = i + 1
		if len(part.shown) == 0 {
			return false
		}
		exception := d.isFoldedException(fold.Word(string(part.shown)))
		if !compoundPartFits(part, exception, o) {
			return false
		}
		if !fixed && !exception && d.judge(part, o).verdict == Fix {
			fixed = true
		}
	}
	return fixed
}

// compoundPartFits: the part is a listed short word in the alt layout
// (short_wrong, exception check included) or an alt dictionary word that is
// not a shown dictionary word.
func compoundPartFits(part typedWord, exception bool, o *Options) bool {
	if !exception && shortWrong(part) {
		return true
	}
	sc, _, ok := scoreWord(part, o)
	return ok && sc.AltKnown && !sc.ShownKnown
}

// Known reports whether word is in the language's dictionary (Bloom-filter
// precision). Every rune, after lowercasing and folding 'ё' into 'е', must be
// a letter of the language's alphabet. For debugging and the CLI.
func (d *Detector) Known(l Lang, word string) bool {
	m, idx, ok := wordLetters(l, word)
	return ok && m.known(idx)
}

// Cost returns the mean trigram cost of word in bits per letter under the
// same letter rules as Known; false when the word is empty or not spelled in
// the language's alphabet. For debugging and the CLI.
func (d *Detector) Cost(l Lang, word string) (float64, bool) {
	m, idx, ok := wordLetters(l, word)
	if !ok {
		return 0, false
	}
	return m.cost(idx), true
}

func wordLetters(l Lang, word string) (*model, []uint8, bool) {
	m := modelFor(l)
	if m == nil || word == "" {
		return nil, nil, false
	}
	idx, ok := letters(l, []rune(word), nil)
	return m, idx, ok
}
