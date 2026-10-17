# punto-rs oracle

`../punto_oracle.tsv` holds the results of the real punto-rs detector
(commit c836c87) for every en/ru word of `../layout_cases.tsv` in both layout
directions, the regression words of punto-rs `tests.rs`, extra edge cases and
1,500 pseudo-random key sequences. `TestDetectorMatchesPuntoOracle` requires
the Go port to agree on every row (`wrong_layout`, `short_wrong`,
`shown_known`, `wrong_compound`, and bit-identical `scores`).

The harness is not built by `go test`. To regenerate the file (only needed if
the embedded models or the port's reference commit ever change):

```sh
PUNTO=/path/to/punto-rs            # checkout at c836c87
WORK=$(mktemp -d)
mkdir -p "$WORK/src"
cat "$PUNTO/cli/src/layout.rs" layout_wrappers.rs > "$WORK/src/layout.rs"
ln -s "$PUNTO/cli/src/layout" "$WORK/src/layout"
cp main.rs "$WORK/src/main.rs"
rustc --edition 2021 -O -o "$WORK/oracle" "$WORK/src/main.rs"
python3 -I mkcases.py ../layout_cases.tsv > "$WORK/cases.txt"
{
  sed -n '1,4p' ../punto_oracle.tsv            # keep the header comment
  "$WORK/oracle" cases < "$WORK/cases.txt"
  "$WORK/oracle" random 1500
} > "$WORK/punto_oracle.tsv" && mv "$WORK/punto_oracle.tsv" ../punto_oracle.tsv
```

`"$WORK/oracle" golden` prints the Bloom bit lists, costs, trigram indices and
Bloom fill ratios used by `model_test.go`.
