package lang

import (
	_ "embed" // model blobs
	"fmt"
	"unicode"
)

// Model blobs, copied unchanged from punto-rs cli/src/layout/ (commit c836c87).
// They are embedded as strings so that they stay in read-only data.
var (
	//go:embed data/en.bin
	enTable string
	//go:embed data/ru.bin
	ruTable string
	//go:embed data/en.bloom
	enBloom string
	//go:embed data/ru.bloom
	ruBloom string
)

// Model constants (layout.rs:67-79).
const (
	// costScale: a table entry is -log2(p) * costScale rounded to a byte.
	costScale = 10.0
	// bloomHashes is the number of Bloom probes per word.
	bloomHashes = 7

	fnvPrime     = 0x0100_0000_01b3
	fnvSeedFirst = 0xcbf2_9ce4_8422_2325
	fnvSeedStep  = 0x8422_2325_cbf2_9ce4

	enAlphabetLen = 26
	ruAlphabetLen = 32 // 'ё' is folded into 'е' before scoring
)

// model is one language's letter-trigram cost table and Bloom dictionary.
type model struct {
	lang    Lang
	symbols int    // alphabet size + 1 (index 0 is the word boundary)
	table   string // symbols³ costs, index (a·N+b)·N+c
	bloom   string // Bloom filter over letter indices
	bits    uint64 // len(bloom) * 8
}

var (
	enModel = newModel(En, enAlphabetLen, enTable, enBloom)
	ruModel = newModel(Ru, ruAlphabetLen, ruTable, ruBloom)
)

func newModel(l Lang, alphabet int, table, bloom string) *model {
	return &model{
		lang:    l,
		symbols: alphabet + 1,
		table:   table,
		bloom:   bloom,
		bits:    uint64(len(bloom)) * 8,
	}
}

// modelFor returns the model of a supported language, or nil.
func modelFor(l Lang) *model {
	switch l {
	case En:
		return enModel
	case Ru:
		return ruModel
	default:
		return nil
	}
}

// validate checks the blob sizes that punto-rs asserts at compile time
// (layout.rs:72-74) plus a non-empty Bloom filter.
func (m *model) validate() error {
	if want := m.symbols * m.symbols * m.symbols; len(m.table) != want {
		return fmt.Errorf("lang: %s trigram table has %d bytes, want %d", m.lang, len(m.table), want)
	}
	if m.bits == 0 || m.bits != uint64(len(m.bloom))*8 {
		return fmt.Errorf("lang: %s Bloom filter is empty", m.lang)
	}
	return nil
}

// letterIndex returns the 1-based index of a lowercase letter in the
// language's alphabet, or 0 when the rune is not a letter of that alphabet.
// 'ё' maps to the index of 'е' (layout.rs:107-114). The Russian alphabet of
// keymap.rs:12-15 is exactly U+0430..U+044F in code point order.
func letterIndex(l Lang, r rune) uint8 {
	switch l {
	case En:
		if r >= 'a' && r <= 'z' {
			return uint8(r-'a') + 1
		}
	case Ru:
		if r == 'ё' {
			r = 'е'
		}
		if r >= 'а' && r <= 'я' {
			return uint8(r-'а') + 1
		}
	}
	return 0
}

// lower mirrors Rust's ch.to_lowercase().next(): for every rune whose full
// lowercase mapping is a single rune (all runes but U+0130, whose first rune
// is also 'i') it equals unicode.ToLower.
func lower(r rune) rune {
	return unicode.ToLower(r)
}

// cost is the mean trigram cost of the word "^^word$" in bits per letter
// (layout.rs:116-122, 150-157). Lower is more plausible.
func (m *model) cost(word []uint8) float64 {
	n := m.symbols
	var sum, count uint32
	a, b := 0, 0
	for _, c := range word {
		sum += uint32(m.table[(a*n+b)*n+int(c)])
		count++
		a, b = b, int(c)
	}
	sum += uint32(m.table[(a*n+b)*n])
	count++
	return float64(sum) / float64(count) / costScale
}

// fnv folds each letter index as a whole u64 word into an FNV-1a hash
// (layout.rs:126-130).
func fnv(seed uint64, word []uint8) uint64 {
	h := seed
	for _, c := range word {
		h = (h ^ uint64(c)) * fnvPrime
	}
	return h
}

// bloomBits returns the bit numbers of a word in a filter of the given size:
// double hashing with an odd step (layout.rs:124-135).
func bloomBits(word []uint8, bits uint64) [bloomHashes]uint64 {
	var out [bloomHashes]uint64
	first := fnv(fnvSeedFirst, word)
	step := fnv(fnvSeedStep, word) | 1
	for i := uint64(0); i < bloomHashes; i++ {
		out[i] = (first + i*step) % bits
	}
	return out
}

// known reports whether the word is in the dictionary, with Bloom-filter
// precision (layout.rs:137-148).
func (m *model) known(word []uint8) bool {
	if m.bits == 0 {
		return false
	}
	for _, bit := range bloomBits(word, m.bits) {
		if m.bloom[bit/8]&(1<<(bit%8)) == 0 {
			return false
		}
	}
	return true
}

// isCoreLetter is the letter test of punto-rs core (layout.rs:162-164).
func isCoreLetter(l Lang, r rune) bool {
	return r != '\'' && letterIndex(l, lower(r)) != 0
}

// core returns the letter indices of text without leading and trailing
// non-letters (layout.rs:159-171), appended to dst. It fails with
// ReasonUnreadableAlt when text has no letter of the alphabet and with
// ReasonNonLetter when a non-letter (digit, '_', '.', ...) sits inside.
func core(l Lang, text []rune, dst []uint8) ([]uint8, string) {
	start, end := -1, -1
	for i, r := range text {
		if isCoreLetter(l, r) {
			if start < 0 {
				start = i
			}
			end = i + 1
		}
	}
	if start < 0 {
		return dst, ReasonUnreadableAlt
	}
	for _, r := range text[start:end] {
		idx := letterIndex(l, lower(r))
		if idx == 0 {
			return dst, ReasonNonLetter
		}
		dst = append(dst, idx)
	}
	return dst, ""
}

// letters converts a whole word to letter indices after lowercasing and
// folding 'ё', as punto-rs shown_known does (layout.rs:285-294): every rune,
// edges included, must be a letter of the alphabet.
func letters(l Lang, text []rune, dst []uint8) ([]uint8, bool) {
	for _, r := range text {
		idx := letterIndex(l, foldRune(r))
		if idx == 0 {
			return dst, false
		}
		dst = append(dst, idx)
	}
	return dst, true
}

// foldRune lowercases a rune and folds 'ё' into 'е' (layout.rs:275-283).
func foldRune(r rune) rune {
	r = lower(r)
	if r == 'ё' {
		r = 'е'
	}
	return r
}
