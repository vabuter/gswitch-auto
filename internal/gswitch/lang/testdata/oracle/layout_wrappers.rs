
// Oracle-only wrappers (not part of punto-rs): expose private helpers unchanged.
// Appended to a copy of punto-rs cli/src/layout.rs by the steps in README.md.
pub fn oracle_known(lang: Lang, word: &[usize]) -> bool {
    known(lang, word)
}
pub fn oracle_cost(lang: Lang, word: &[usize]) -> f64 {
    cost(lang, word)
}
pub fn oracle_shown_known(keys: &[(u16, bool)], shown: Lang) -> bool {
    shown_known(keys, shown)
}
pub fn oracle_compound(keys: &[(u16, bool)], pair: Pair) -> bool {
    wrong_compound(keys, pair)
}
