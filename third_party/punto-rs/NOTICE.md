# Third-party notices

gswitch-auto is a fork of gswitch (MIT License, Copyright (c) 2025 arumata; see the
repository's `LICENSE`).
It contains or embeds the following third-party material.

## punto-rs layout detector

The package `internal/gswitch/lang` is a Go port of the layout detector of
punto-rs (`cli/src/layout.rs` and `cli/src/layout/keymap.rs`, commit c836c87,
https://github.com/rashevskyv/punto-rs), and embeds its model files unchanged
(`internal/gswitch/lang/data/{en,ru}.{bin,bloom}` and `exceptions.txt`). The
test data `internal/gswitch/lang/testdata/layout_cases.tsv` is a copy of
punto-rs `cli/tests/data/layout_cases.tsv`.

punto-rs is distributed under the MIT License, Copyright (c) 2026 netherguy4.
The licence text is in `LICENSE` next to this file (installed as
`punto-rs-LICENSE`).

### Russian model (`ru.bin`, `ru.bloom`)

As stated in punto-rs `cli/LICENSES/README.md` (lines 10-15), the Russian model
embeds a **modified form** of the Russian hunspell dictionary (hunspell-ru
0.99g5, Copyright (c) 1997-2008, Alexander I. Lebedev): its word forms are
stored as a Bloom filter (`ru.bloom`) and a letter-trigram model (`ru.bin`),
built by punto-rs `cli/scripts/build_layout_model.sh`. These files are a
derivative of the dictionary, not the dictionary itself, and are marked here
as modified as its licence requires. The dictionary licence
(SPDX: BSD-3-Clause-Modification) is in `hunspell-ru-LICENSE` next to this
file.

### English model (`en.bin`, `en.bloom`)

The English model is built from the Fedora `words` package word list
(`/usr/share/dict/words`), which is in the public domain
(SPDX: LicenseRef-Fedora-Public-Domain).

### Ukrainian model (not included)

punto-rs also ships a Ukrainian model built from hunspell-uk (used there under
MPL-1.1) and from the hermitdave/FrequencyWords list (CC BY-SA 4.0). It is
intentionally **not** included in gswitch-auto, which supports only the
English/Russian layout pair.

## golang.design/x/clipboard

`third_party/clipboard` is a vendored copy of golang.design/x/clipboard
(MIT License, Copyright (c) 2021 Changkun Ou), carried over from upstream
gswitch with its `dlopen("libX11.so.6")` fallback. Its licence is in
`third_party/clipboard/LICENSE`.
