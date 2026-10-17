package lang

import (
	"math"
	"math/bits"
	"strings"
	"testing"
)

// Golden values in this file come from the real punto-rs code (c836c87)
// through the testdata/oracle harness; the spec's rounded values are checked
// alongside.

func modelTestLetters(t *testing.T, l Lang, w string) []uint8 {
	t.Helper()
	idx, ok := letters(l, []rune(w), nil)
	if !ok {
		t.Fatalf("%q is not spelled in the %s alphabet", w, l)
	}
	return idx
}

func TestEmbeddedModelSizes(t *testing.T) {
	for _, tc := range []struct {
		name string
		blob string
		want int
	}{
		{"en.bin", enTable, 19683},
		{"ru.bin", ruTable, 35937},
		{"en.bloom", enBloom, 444422},
		{"ru.bloom", ruBloom, 1708013},
	} {
		if len(tc.blob) != tc.want {
			t.Errorf("%s: %d bytes, want %d", tc.name, len(tc.blob), tc.want)
		}
	}
	if enModel.symbols*enModel.symbols*enModel.symbols != 27*27*27 ||
		ruModel.symbols*ruModel.symbols*ruModel.symbols != 33*33*33 {
		t.Error("alphabet sizes must be 26+1 and 32+1")
	}
	for _, m := range []*model{enModel, ruModel} {
		if err := m.validate(); err != nil {
			t.Error(err)
		}
	}
}

func TestModelValidateRejectsWrongSizes(t *testing.T) {
	bad := newModel(En, enAlphabetLen, enTable[:100], enBloom)
	if err := bad.validate(); err == nil {
		t.Error("short trigram table accepted")
	}
	empty := newModel(Ru, ruAlphabetLen, ruTable, "")
	if err := empty.validate(); err == nil {
		t.Error("empty Bloom filter accepted")
	}
	if empty.known([]uint8{1, 2, 3}) {
		t.Error("an empty Bloom filter knows no word")
	}
}

// TestLetterIndexMatchesPuntoAlphabets checks the arithmetic letter index
// against the alphabets of keymap.rs:7-15.
func TestLetterIndexMatchesPuntoAlphabets(t *testing.T) {
	alphabets := map[Lang]string{
		En: "abcdefghijklmnopqrstuvwxyz",
		Ru: "абвгдежзийклмнопрстуфхцчшщъыьэюя",
	}
	for l, alphabet := range alphabets {
		in := map[rune]bool{}
		for i, r := range []rune(alphabet) {
			in[r] = true
			if got := letterIndex(l, r); int(got) != i+1 {
				t.Errorf("%s %q: index %d, want %d", l, r, got, i+1)
			}
		}
		for r := rune(0); r < 0x500; r++ {
			if !in[r] && r != 'ё' && letterIndex(l, r) != 0 {
				t.Errorf("%s %q (U+%04X) must not be a letter", l, r, r)
			}
		}
	}
	if letterIndex(Ru, 'ё') != letterIndex(Ru, 'е') {
		t.Error("ё must fold into е")
	}
	if letterIndex(En, 'ё') != 0 || letterIndex(Unknown, 'a') != 0 {
		t.Error("ё is not English; Unknown has no alphabet")
	}
}

func TestBloomBitsGolden(t *testing.T) {
	ru := bloomBits(modelTestLetters(t, Ru, "привет"), 13_664_104)
	wantRu := [bloomHashes]uint64{4112785, 6389110, 3647715, 906320, 11829029, 441250, 11363959}
	if ru != wantRu {
		t.Errorf("bloomBits(привет) = %v, want %v", ru, wantRu)
	}
	en := bloomBits(modelTestLetters(t, En, "hello"), 3_555_376)
	wantEn := [bloomHashes]uint64{1695371, 1975120, 573877, 853626, 1133375, 3287508, 11881}
	if en != wantEn {
		t.Errorf("bloomBits(hello) = %v, want %v", en, wantEn)
	}
	if ruModel.bits != 13_664_104 || enModel.bits != 3_555_376 {
		t.Errorf("filter sizes %d/%d bits", ruModel.bits, enModel.bits)
	}
}

func TestKnownGolden(t *testing.T) {
	for _, tc := range []struct {
		l    Lang
		w    string
		want bool
	}{
		{Ru, "привет", true},
		{En, "hello", true},
		{Ru, "ршщзх", false},
		{En, "ghbdtn", false},
		{En, "tls", false},
		{Ru, "еды", true},
		{En, "dnf", false},
		{Ru, "вта", false},
		{Ru, "ежик", true},
		{Ru, "ёжик", true},
	} {
		m := modelFor(tc.l)
		if got := m.known(modelTestLetters(t, tc.l, tc.w)); got != tc.want {
			t.Errorf("known(%s, %s) = %v, want %v", tc.l, tc.w, got, tc.want)
		}
	}
}

func TestCostGolden(t *testing.T) {
	for _, tc := range []struct {
		l        Lang
		w        string
		punto    float64 // exact punto-rs f64
		spec     float64 // rounded value in SPEC §3.3
		trigrams []int   // punto-rs trigrams() indices
	}{
		{En, "ghbdtn", 7.614285714285714, 7.6142857, []int{7, 197, 5321, 5890, 1586, 3470, 14958}},
		{Ru, "привет", 2.8428571428571425, 2.8428571, []int{16, 545, 17994, 18813, 9906, 3484, 7161}},
		{En, "hello", 3.7666666666666666, 3.7666667, []int{8, 221, 5979, 3981, 9087, 9153}},
		{Ru, "ршщзх", 9.1, 9.1, []int{17, 586, 19364, 28091, 28600, 9438}},
		{En, "tls", 6.375, 6.375, []int{20, 552, 14923, 9261}},
		{Ru, "еды", 5.175, 5.175, []int{6, 203, 6727, 6369}},
		{En, "dnf", 10.325, 10.325, []int{4, 122, 3300, 10368}},
		{Ru, "вта", 4.275, 4.275, []int{3, 118, 3895, 20724}},
	} {
		m := modelFor(tc.l)
		got := m.cost(modelTestLetters(t, tc.l, tc.w))
		if got != tc.punto {
			t.Errorf("cost(%s, %s) = %v, punto-rs %v", tc.l, tc.w, got, tc.punto)
		}
		if math.Abs(got-tc.spec) > 5e-8 {
			t.Errorf("cost(%s, %s) = %v, spec %v", tc.l, tc.w, got, tc.spec)
		}
		// The table index order (a·N+b)·N+c over "^^word$" is punto-rs's.
		var sum uint32
		for _, idx := range tc.trigrams {
			sum += uint32(m.table[idx])
		}
		if want := float64(sum) / float64(len(tc.trigrams)) / costScale; math.Abs(got-want) > 1e-9 {
			t.Errorf("cost(%s, %s) = %v, from punto-rs trigram indices %v", tc.l, tc.w, got, want)
		}
	}
}

func TestBloomFillRatio(t *testing.T) {
	for _, m := range []*model{enModel, ruModel} {
		ones := 0
		for i := range len(m.bloom) {
			ones += bits.OnesCount8(m.bloom[i])
		}
		ratio := float64(ones) / float64(m.bits)
		if ratio < 0.49 || ratio > 0.51 {
			t.Errorf("%s Bloom fill ratio %.4f outside 0.49..0.51", m.lang, ratio)
		}
	}
}

func TestCoreStripsEdgesAndRejectsInterior(t *testing.T) {
	for _, tc := range []struct {
		l    Lang
		text string
		want string // letters of the core, or the reason
	}{
		{Ru, "привет,", "привет"},
		{Ru, "\"Привет!", "привет"},
		{Ru, "ёлка", "елка"},
		{En, "'don't'", ReasonNonLetter},
		{En, "x.y", ReasonNonLetter},
		{En, ".,;", ReasonUnreadableAlt},
		{En, "", ReasonUnreadableAlt},
		{Ru, "abc", ReasonUnreadableAlt},
	} {
		idx, why := core(tc.l, []rune(tc.text), nil)
		got := why
		if why == "" {
			var b strings.Builder
			alphabet := map[Lang][]rune{En: []rune("abcdefghijklmnopqrstuvwxyz"), Ru: []rune("абвгдежзийклмнопрстуфхцчшщъыьэюя")}[tc.l]
			for _, i := range idx {
				b.WriteRune(alphabet[i-1])
			}
			got = b.String()
		}
		if got != tc.want {
			t.Errorf("core(%s, %q) = %q, want %q", tc.l, tc.text, got, tc.want)
		}
	}
}
