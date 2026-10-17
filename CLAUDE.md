# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this folder is

The git repository of **gswitch-auto**, a fork of **gswitch** (Go, MIT,
github.com/arumata/gswitch) that adds automatic wrong-layout correction (Punto
Switcher-like) for the user's machine: Fedora 44, GNOME Shell 50, Wayland, IBus, layouts
`us`+`ru`, switch hotkey `<Alt>Shift_L`. Upstream history is preserved: remote `upstream`
is arumata/gswitch, remote `origin` is github.com/vabuter/gswitch-auto. The tree is
upstream v0.8.0 plus fork commits; the Go module path stays `github.com/arumata/gswitch` to
keep rebases onto upstream releases small. The fork ships as the RPM `gswitch-auto`, which
obsoletes and replaces upstream's `gswitch` package (same binaries and paths). Upstream
gswitch 0.8.0 (manual double-Shift correction only) is installed on the host from the RPM
kept at the repo root (gitignored).

Read in this order; do not redo the research:

1. `docs/SPEC.md` — the implementation contract: decisions D1–D8 and the corrections
   ledger (§1), architecture (§2), per-package design (§3), integration points in upstream
   files (§4), work packages with file ownership (§5), tests and the manual test script
   (§6). Follow it; do not re-litigate fixed decisions.
2. `docs/FORK.md` (Russian) — versioning, release and rebase checklists, modified upstream
   files, install/rollback.
3. `docs/NOTES.md` — environment, what is installed and how, key findings.
4. `docs/gswitch-auto-mode-assessment.md` — A-vs-B assessment with effort estimates.
5. `docs/research/` — raw agent output (~60k words). `01-code-readings.md` has file:line
   references into upstream gswitch and punto-rs; `02-designs.md` has three auto-mode
   designs; `03-judgements-and-verification.md` lists claims that were **refuted**
   (e.g. "performConversion can be reused verbatim", "undo is free"). Grep these rather
   than reading them whole.

The user communicates in Russian; notes and reports here are in Russian. Code, comments
and commit messages in the fork stay in English to match upstream.

## Operating the installed gswitch

```bash
systemctl --user status gswitch.service        # daemon runs as a user service
journalctl --user -u gswitch.service -f        # daemon log
gswitch --detect-layout-switch                 # JSON: detected hotkey (expects scancodes [56,42], source gnome)
gswitch --debug                                # run daemon in foreground with diagnostics (stop the service first)
sudo gswitch --configure                       # interactive config -> /etc/gswitch/default.conf
```

`gswitch -d` is `--debug` (runs the daemon), not a device listing. The tray
(`gswitch-tray`) autostarts via `/etc/xdg/autostart/`; its panel icon needs the
AppIndicator GNOME extension, which is installed and listed in `enabled-extensions`.

## Facts that constrain any fork (verified 2026-10-09, see research/)

- Upstream rejected automatic correction (issues #12, #31 "not planned"); development
  happens in a private tree and releases land as single squash commits. Auto mode is a
  hard fork that must be rebased onto each upstream release; do not plan an upstream PR.
- The daemon does not know the active layout: it buffers raw evdev scancodes and blindly
  replays the toggle chord. On this machine the reliable source is IBus:
  `org.freedesktop.IBus.GlobalEngineChanged` on the private IBus bus (address in
  `~/.config/ibus/bus/*`); `GetGlobalEngine` returns `xkb:us::eng` / `xkb:ru::rus`.
  `gsettings … mru-sources` is only a resync fallback.
- No evdev-scancode → XKB keyname (`<AD01>`…) table exists in gswitch; ~48 entries are
  needed before the buffer can be rendered as text in both layouts.
- Focused window is not obtainable on GNOME Wayland (`org.gnome.Shell.Introspect` is
  allowlisted); terminal/password exclusions require a GNOME Shell extension.
- `GrabAll` exists in the input reader but is never called; without grab, typing during
  the ~0.5 s replay interleaves with backspaces. Trigger only on Space, never on Enter.
- Language detector of choice: punto-rs's Bloom + letter-trigram model (MIT, ~2 MB).
  No LLM in the hot path (latency and the daemon's no-network security model).

## Building the fork

Toolchain on this host: golang 1.26 (satisfies `go 1.25.5` in `go.mod`), gcc, make,
`gtk3-devel`, `libX11-devel`, `libxkbcommon-devel`, rpm/rpm2cpio/cpio, python3, node.
nfpm v2.48.0 is at `~/go/bin/nfpm` (**not on PATH**; the Makefile defaults `NFPM` to it).
Not installed: golangci-lint, goreleaser (not used by the fork), rpmbuild, shellcheck,
xvfb-run (GTK tests stay gated). Builds use CGO (clipboard, tray).

```bash
make build                                                # builds/gswitch, builds/gswitch-tray (-X main.version)
make test                                                 # go test ./... (upstream ~320 tests + fork tests)
go test ./internal/gswitch/runtime -run 'TestName'        # single test
make vet                                                  # go vet ./...
make ext-check                                            # scripts/check-extension.sh
make version                                              # e.g. 0.8.0+auto.0.dev16.g90407de (scripts/version.sh)
make rpm                                                  # dist/gswitch-auto-<V>-1.x86_64.rpm + checksums.txt
make install                                              # make rpm + sudo dnf install (user runs this)
golangci-lint run                                         # config in .golangci.yml (CI only here)
```

`make rpm` runs `scripts/build-rpm.sh`: preflight (every `src:` in `packaging/nfpm.yaml`
must exist), `scripts/version.sh --verify`, the extension check, `go test ./...`, CGO
build, nfpm, `checksums.txt`, then `rpm -qp` assertions (Provides `gswitch = V-R`,
Obsoletes `gswitch < 0.9`, no Conflicts, packaged user unit byte-identical to upstream).
Versions are `<upstream>+auto.<N>` from tags `v<upstream>+auto.<N>`; dev builds get
`.dev<n>.g<sha>[.dirty]`; `~` versions are forbidden (they sort below upstream).
Gated test suites: `GSWITCH_IBUS_TEST=1`, `GSWITCH_DBUS_TEST=1` (`dbus-run-session`),
`GSWITCH_GTK_TEST=1` (`xvfb-run`).

Upstream layout: `internal/gswitch/runtime` (evdev reader, key buffer, conversion/replay —
`switcher.go`, `converter.go`), `internal/gswitch/detect` (layout and hotkey detection
providers), `internal/gswitch/config`, `internal/tray` (GTK settings window, layout
monitor), `configs/` (systemd user unit, udev uaccess rule, polkit policy, default.conf).
Fork additions (SPEC §3): `internal/gswitch/{fold,lang,layoutstate,focus,userconfig,ipc}`,
new files in `runtime/` hooked into `switcher.go` by one-line calls, `internal/i18n`
`strGA*` strings (en+ru), `shell-extension/gswitch-focus@vabuter.github.io`,
`third_party/punto-rs` licences, `packaging/nfpm.yaml` (replaces the deleted
`.goreleaser.yml`), `scripts/{build-rpm,version,pretrans,posttrans,check-extension}.sh`,
`Makefile`. Upstream files that must stay unmodified include `configs/gswitch.service`
and `scripts/{pre,post}{install,remove}.sh` (SPEC §4).

## Housekeeping

Do not commit `gswitch_0.8.0_linux_amd64.rpm` (6 MB binary, `*.rpm` is gitignored); it is
kept only as the exact installed artifact (sha256 verified against the release's
checksums.txt) and is the rollback target in `docs/FORK.md`.
