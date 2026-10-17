package lang

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
)

// oracleTestRow is one line of testdata/punto_oracle.tsv: the results of the
// real punto-rs functions for a word typed in the shown layout of a pair.
type oracleTestRow struct {
	line                                      int
	pair                                      Pair
	text                                      string
	wrong, short, shownKnown, compound, isSet bool
	scores                                    Scores
}

func oracleTestRows(t *testing.T) []oracleTestRow {
	t.Helper()
	f, err := os.Open("testdata/punto_oracle.tsv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var rows []oracleTestRow
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rows = append(rows, oracleTestParse(t, n, line))
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return rows
}

func oracleTestParse(t *testing.T, n int, line string) oracleTestRow {
	t.Helper()
	fields := strings.Split(line, "\t")
	if len(fields) != 7 {
		t.Fatalf("line %d: %d fields", n, len(fields))
	}
	row := oracleTestRow{line: n, text: fields[1]}
	switch fields[0] {
	case "en-ru":
		row.pair = Pair{Shown: En, Alt: Ru}
	case "ru-en":
		row.pair = Pair{Shown: Ru, Alt: En}
	default:
		t.Fatalf("line %d: pair %q", n, fields[0])
	}
	flag := func(s string) bool { return s == "1" }
	row.wrong, row.short, row.shownKnown, row.compound =
		flag(fields[2]), flag(fields[3]), flag(fields[4]), flag(fields[5])
	if fields[6] == "-" {
		return row
	}
	parts := strings.Split(fields[6], ",")
	if len(parts) != 4 {
		t.Fatalf("line %d: scores %q", n, fields[6])
	}
	shownCost, err1 := strconv.ParseFloat(parts[0], 64)
	altCost, err2 := strconv.ParseFloat(parts[1], 64)
	if err1 != nil || err2 != nil {
		t.Fatalf("line %d: costs %q", n, fields[6])
	}
	row.isSet = true
	row.scores = Scores{ShownCost: shownCost, AltCost: altCost, ShownKnown: flag(parts[2]), AltKnown: flag(parts[3])}
	return row
}

// TestDetectorMatchesPuntoOracle compares the port with the real punto-rs
// code (wrong_layout, scores, short_wrong, shown_known, wrong_compound) on
// every en/ru word of layout_cases.tsv in both directions, the regression
// words of tests.rs and 1,500 random key sequences. Costs must be equal as
// float64 values, not merely close.
func TestDetectorMatchesPuntoOracle(t *testing.T) {
	d, err := NewDetector(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	opts := DefaultOptions()
	rows := oracleTestRows(t)
	if len(rows) < 3000 {
		t.Fatalf("oracle has %d rows, want >= 3000", len(rows))
	}
	for _, row := range rows {
		shown, alt := rowsTestWord(t, row.pair, row.text)
		w := typedWord{shown: []rune(shown), alt: []rune(alt), pair: row.pair}
		ctx := "line " + strconv.Itoa(row.line) + " " + row.pair.Shown.String() + " " + row.text + " / " + alt

		if got := d.Decide(shown, alt, row.pair).Verdict == Fix; got != row.wrong {
			t.Errorf("%s: Decide fix=%v, punto-rs wrong_layout=%v", ctx, got, row.wrong)
		}
		sc, ok := d.Scores(shown, alt, row.pair)
		if ok != row.isSet || (ok && sc != row.scores) {
			t.Errorf("%s: Scores=%+v,%v, punto-rs %+v,%v", ctx, sc, ok, row.scores, row.isSet)
		}
		gotShort := !d.IsException(shown, row.pair.Shown) && shortWrong(w)
		if gotShort != row.short {
			t.Errorf("%s: short_wrong=%v, punto-rs %v", ctx, gotShort, row.short)
		}
		if got := shownKnown(w); got != row.shownKnown {
			t.Errorf("%s: shown_known=%v, punto-rs %v", ctx, got, row.shownKnown)
		}
		if got := d.compound(w, &opts); got != row.compound {
			t.Errorf("%s: wrong_compound=%v, punto-rs %v", ctx, got, row.compound)
		}
	}
}
