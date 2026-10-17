# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this folder is

A fork of **gswitch** (Go, MIT, github.com/arumata/gswitch) that adds automatic
wrong-layout correction (Punto Switcher-like) for the user's machine: Fedora 44, GNOME
Shell 50, Wayland, IBus, layouts `us`+`ru`, switch hotkey `<Alt>Shift_L`. Upstream history
is preserved: remote `upstream` is arumata/gswitch, remote `origin` is
github.com/vabuter/gswitch-auto. The Go module path stays `github.com/arumata/gswitch` to
keep rebases onto upstream releases small. Upstream gswitch 0.8.0 (manual double-Shift
correction only) is installed on the host from the RPM kept at the repo root (gitignored).

Read in this order; do not redo the research:

1. `docs/NOTES.md` — environment, what is installed and how, key findings, staged plan.
2. `docs/gswitch-auto-mode-assessment.md` — A-vs-B assessment with effort estimates.
3. `docs/SPEC.md` (once it exists) — the implementation spec for the auto mode.
4. `docs/research/` — raw agent output (~60k words). `01-code-readings.md` has file:line
   references into upstream gswitch and punto-rs; `02-designs.md` has three auto-mode
   designs; `03-judgements-and-verification.md` lists claims that were **refuted**
   (e.g. "performConversion can be reused verbatim", "undo is free"). Grep these rather
   than reading them whole.

The user communicates in Russian; notes and reports here are in Russian. Code, comments
and commit messages in a gswitch fork should stay in English to match upstream.

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

## Building a gswitch fork (from upstream's README and repo layout)

Prerequisites not yet on this host: Go ≥ 1.25.5 (`go.mod`), `gtk3-devel` and
`libX11-devel` for the tray; clipboard support needs CGO.

```bash
go build -o builds/gswitch ./cmd/gswitch                  # daemon
go build -o builds/gswitch-tray ./cmd/gswitch-tray        # tray (GTK3, CGO)
go test ./...                                             # ~320 tests, roughly 1:1 test-to-code in runtime/detect
go test ./internal/gswitch/runtime -run 'TestName'        # single test
golangci-lint run                                         # config in .golangci.yml
```

Upstream layout: `internal/gswitch/runtime` (evdev reader, key buffer, conversion/replay —
`switcher.go`, `converter.go`), `internal/gswitch/detect` (layout and hotkey detection
providers), `internal/gswitch/config`, `internal/tray` (GTK settings window, layout
monitor), `configs/` (systemd user unit, udev uaccess rule, polkit policy, default.conf).
Packaging is goreleaser/nfpm (`.goreleaser.yml`) producing the RPM.

## Housekeeping

If this folder becomes a git repo, do not commit `gswitch_0.8.0_linux_amd64.rpm` (6 MB
binary); it is kept only as the exact installed artifact (sha256 verified against the
release's checksums.txt).
