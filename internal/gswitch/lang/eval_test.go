package lang

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"testing"
)

// evalTestRow is one en/ru line of testdata/layout_cases.tsv (punto-rs
// cli/tests/data/layout_cases.tsv): split, class, shown layout, typed text,
// expected label ("fix" or "keep"). Ukrainian rows are skipped.
type evalTestRow struct {
	split, class, text string
	pair               Pair
	fix                bool
}

func evalTestRows(t *testing.T) []evalTestRow {
	t.Helper()
	f, err := os.Open("testdata/layout_cases.tsv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var rows []evalTestRow
	sc := bufio.NewScanner(f)
	total := 0
	for sc.Scan() {
		fields := strings.Split(sc.Text(), "\t")
		if len(fields) != 5 {
			t.Fatalf("malformed line %q", sc.Text())
		}
		total++
		var pair Pair
		switch fields[2] {
		case "en":
			pair = Pair{Shown: En, Alt: Ru}
		case "ru":
			pair = Pair{Shown: Ru, Alt: En}
		default: // "uk", "en-uk": the Ukrainian model is not shipped
			continue
		}
		rows = append(rows, evalTestRow{
			split: fields[0], class: fields[1], text: fields[3], pair: pair, fix: fields[4] == "fix",
		})
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if total != 2403 {
		t.Fatalf("layout_cases.tsv has %d rows, want 2403", total)
	}
	return rows
}

// evalTestResult counts one split.
type evalTestResult struct {
	rows, keeps         int
	falseFixes          []string
	ruWrong, ruWrongAll int
	enWrong, enWrongAll int
	short2, short2All   int
}

func (r evalTestResult) String() string {
	return fmt.Sprintf("rows=%d keep-rows=%d false-fixes=%d ru_wrong=%d/%d en_wrong=%d/%d short_wrong(2 keys)=%d/%d",
		r.rows, r.keeps, len(r.falseFixes), r.ruWrong, r.ruWrongAll, r.enWrong, r.enWrongAll, r.short2, r.short2All)
}

func evalTestRun(t *testing.T, d *Detector, rows []evalTestRow, split string, o *Options) evalTestResult {
	t.Helper()
	var res evalTestResult
	for _, row := range rows {
		if row.split != split {
			continue
		}
		res.rows++
		shown, alt := rowsTestWord(t, row.pair, row.text)
		fixed := d.decideWith(shown, alt, row.pair, o).Verdict == Fix
		if !row.fix {
			res.keeps++
			if fixed {
				res.falseFixes = append(res.falseFixes, row.class+":"+row.text)
			}
			continue
		}
		switch {
		case row.class == "ru_wrong":
			res.ruWrongAll++
			res.ruWrong += evalTestCount(fixed)
		case row.class == "en_wrong":
			res.enWrongAll++
			res.enWrong += evalTestCount(fixed)
		case row.class == "short_wrong" && len([]rune(row.text)) == 2:
			res.short2All++
			res.short2 += evalTestCount(fixed)
		}
	}
	return res
}

func evalTestCount(b bool) int {
	if b {
		return 1
	}
	return 0
}

// TestEvalGates runs Decide with DefaultOptions and the built-in exceptions
// over the labelled punto-rs set. False fixes are a hard zero on both splits;
// recall must not drop below the values measured for the punto-rs port.
func TestEvalGates(t *testing.T) {
	d, err := NewDetector(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	rows := evalTestRows(t)
	opts := DefaultOptions()
	gates := []struct {
		split                 string
		ruWrong, enWrong, sh2 int
		ruAll, enAll, sh2All  int
	}{
		{"test", 100, 93, 13, 105, 100, 26},
		{"dev", 99, 92, 12, 105, 101, 24},
	}
	for _, g := range gates {
		res := evalTestRun(t, d, rows, g.split, &opts)
		t.Logf("%s: %s", g.split, res)
		if res.rows != 606 {
			t.Errorf("%s: %d en/ru rows, want 606", g.split, res.rows)
		}
		if len(res.falseFixes) != 0 {
			t.Errorf("%s: false fixes %v, want none", g.split, res.falseFixes)
		}
		if res.ruWrongAll != g.ruAll || res.enWrongAll != g.enAll || res.short2All != g.sh2All {
			t.Errorf("%s: class sizes ru=%d en=%d short2=%d, want %d/%d/%d",
				g.split, res.ruWrongAll, res.enWrongAll, res.short2All, g.ruAll, g.enAll, g.sh2All)
		}
		if res.ruWrong < g.ruWrong {
			t.Errorf("%s: ru_wrong recall %d < %d", g.split, res.ruWrong, g.ruWrong)
		}
		if res.enWrong < g.enWrong {
			t.Errorf("%s: en_wrong recall %d < %d", g.split, res.enWrong, g.enWrong)
		}
		if res.short2 < g.sh2 {
			t.Errorf("%s: 2-key short_wrong recall %d < %d", g.split, res.short2, g.sh2)
		}
	}
}

// TestEvalGrid prints false fixes and recall over a threshold grid when
// GSWITCH_EVAL_GRID=1. It never fails.
func TestEvalGrid(t *testing.T) {
	if os.Getenv("GSWITCH_EVAL_GRID") != "1" {
		t.Skip("set GSWITCH_EVAL_GRID=1 to print the margin x max-alt-cost grid")
	}
	d, err := NewDetector(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	rows := evalTestRows(t)
	for _, margin := range []float64{0, 0.5, 1, 1.5, 2, 3} {
		for _, maxAlt := range []float64{4, 5, 6, 7, 99} {
			o := DefaultOptions()
			o.MinMargin, o.MaxAltCost = margin, maxAlt
			for _, split := range []string{"dev", "test"} {
				res := evalTestRun(t, d, rows, split, &o)
				t.Logf("margin=%-3v max-alt=%-2v %-4s false-fixes=%-3d recall ru=%d/%d en=%d/%d short2=%d/%d",
					margin, maxAlt, split, len(res.falseFixes), res.ruWrong, res.ruWrongAll,
					res.enWrong, res.enWrongAll, res.short2, res.short2All)
			}
		}
	}
}
