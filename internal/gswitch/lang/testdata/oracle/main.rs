// Oracle harness: prints punto-rs detector results for gswitch-auto's Go port.
#![allow(dead_code)]
#[path = "layout.rs"]
mod layout;

use layout::*;
use std::io::{BufRead, Write};

const EN_RU: Pair = Pair::new(Lang::En, Lang::Ru);
const RU_EN: Pair = Pair::new(Lang::Ru, Lang::En);

fn keys_for(lang: Lang, text: &str) -> Vec<(u16, bool)> {
    text.chars()
        .filter_map(|ch| {
            (2..=53)
                .flat_map(|code| [(code, false), (code, true)])
                .find(|&(code, shift)| key_char(lang, code, shift) == Some(ch))
        })
        .collect()
}

fn pair_name(p: Pair) -> &'static str {
    if p == EN_RU { "en-ru" } else { "ru-en" }
}

fn emit(out: &mut impl Write, pair: Pair, text: &str) {
    let keys = keys_for(pair.shown, text);
    assert_eq!(keys.len(), text.chars().count(), "{text}");
    let s = match scores(&keys, pair) {
        None => "-".to_string(),
        Some(sc) => format!(
            "{:?},{:?},{},{}",
            sc.shown_cost, sc.alt_cost, sc.shown_known as u8, sc.alt_known as u8
        ),
    };
    writeln!(
        out,
        "{}\t{}\t{}\t{}\t{}\t{}\t{}",
        pair_name(pair),
        text,
        wrong_layout(&keys, pair) as u8,
        short_wrong(&keys, pair) as u8,
        oracle_shown_known(&keys, pair.shown) as u8,
        oracle_compound(&keys, pair) as u8,
        s
    )
    .unwrap();
}

fn render(lang: Lang, keys: &[(u16, bool)]) -> String {
    keys.iter().map(|&(c, s)| key_char(lang, c, s).unwrap()).collect()
}

const CODES: &[u16] = &[
    2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 30, 31,
    32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53,
];
// Keys that are letters in the RU layout (q..], a..', `, z..m) plus the US letter keys.
const LETTER_CODES: &[u16] = &[
    16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40, 41,
    44, 45, 46, 47, 48, 49, 50, 51, 52,
];

struct Lcg(u64);
impl Lcg {
    fn next(&mut self) -> u64 {
        self.0 = self
            .0
            .wrapping_mul(6364136223846793005)
            .wrapping_add(1442695040888963407);
        self.0 >> 33
    }
    fn below(&mut self, n: u64) -> u64 {
        self.next() % n
    }
}

fn main() {
    let args: Vec<String> = std::env::args().collect();
    let mut out = std::io::BufWriter::new(std::io::stdout());
    match args.get(1).map(String::as_str) {
        // stdin: "<shown-lang>\t<text>" lines; the shown language selects the pair.
        Some("cases") => {
            for line in std::io::stdin().lock().lines() {
                let line = line.unwrap();
                let (lang, text) = line.split_once('\t').unwrap();
                let pair = if lang == "en" { EN_RU } else { RU_EN };
                emit(&mut out, pair, text);
            }
        }
        Some("random") => {
            let n: usize = args[2].parse().unwrap();
            let mut rng = Lcg(0x5eed_6a5c_0ffe_e123);
            for i in 0..n {
                let len = 1 + rng.below(10) as usize;
                let keys: Vec<(u16, bool)> = (0..len)
                    .map(|_| {
                        let code = if rng.below(10) < 8 {
                            LETTER_CODES[rng.below(LETTER_CODES.len() as u64) as usize]
                        } else {
                            CODES[rng.below(CODES.len() as u64) as usize]
                        };
                        (code, rng.below(12) == 0)
                    })
                    .collect();
                let pair = if i % 2 == 0 { EN_RU } else { RU_EN };
                emit(&mut out, pair, &render(pair.shown, &keys));
            }
        }
        Some("golden") => {
            let idx = |lang: Lang, w: &str| -> Vec<usize> {
                w.chars().map(|c| letter_index(lang, c).unwrap()).collect()
            };
            let ru_bits = 1_708_013u64 * 8;
            let en_bits = 444_422u64 * 8;
            let b: Vec<u64> = bloom_bits(&idx(Lang::Ru, "привет"), ru_bits).collect();
            writeln!(out, "bloom ru привет {:?}", b).unwrap();
            let b: Vec<u64> = bloom_bits(&idx(Lang::En, "hello"), en_bits).collect();
            writeln!(out, "bloom en hello {:?}", b).unwrap();
            for (lang, name, w) in [
                (Lang::En, "en", "ghbdtn"),
                (Lang::Ru, "ru", "привет"),
                (Lang::En, "en", "hello"),
                (Lang::Ru, "ru", "ршщзх"),
                (Lang::En, "en", "tls"),
                (Lang::Ru, "ru", "еды"),
                (Lang::En, "en", "dnf"),
                (Lang::Ru, "ru", "вта"),
                (Lang::Ru, "ru", "ежик"),
                (Lang::Ru, "ru", "ёжик"),
            ] {
                let l = idx(lang, w);
                let tri: Vec<usize> = trigrams(lang, &l).collect();
                writeln!(
                    out,
                    "word {name} {w} known={} cost={:?} trigrams={:?}",
                    oracle_known(lang, &l),
                    oracle_cost(lang, &l),
                    tri
                )
                .unwrap();
            }
            let en: &[u8] = include_bytes!("layout/en.bloom");
            let ru: &[u8] = include_bytes!("layout/ru.bloom");
            for (name, f) in [("en", en), ("ru", ru)] {
                let ones: u64 = f.iter().map(|b| u64::from(b.count_ones())).sum();
                writeln!(out, "fill {name} {} / {}", ones, f.len() * 8).unwrap();
            }
        }
        _ => panic!("usage: oracle cases|random N|golden"),
    }
}
