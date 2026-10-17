# Detector model data

These files are copied unchanged from punto-rs
(https://github.com/rashevskyv/punto-rs, commit c836c87, `cli/src/layout/`) and
embedded into the daemon with `go:embed`:

| File | Size (bytes) | Content |
|---|---|---|
| `en.bin` | 19,683 | English letter-trigram costs, 27³ entries |
| `ru.bin` | 35,937 | Russian letter-trigram costs, 33³ entries |
| `en.bloom` | 444,422 | English dictionary as a Bloom filter (7 probes) |
| `ru.bloom` | 1,708,013 | Russian dictionary as a Bloom filter (7 probes) |
| `exceptions.txt` | 123 | Built-in exception words (20) |

They are derived data: do not edit them by hand and do not regenerate them
here. The Ukrainian model (`uk.*`) is intentionally not included.

Licences and provenance (punto-rs MIT licence; `ru.*` is a modified form of
hunspell-ru 0.99g5, BSD-3-Clause-Modification; `en.*` derives from the public
domain Fedora `words` list) are documented in
[`third_party/punto-rs/NOTICE.md`](../../../../third_party/punto-rs/NOTICE.md).
