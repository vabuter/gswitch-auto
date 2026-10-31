# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this folder is

The git repository of **gswitch-auto**, a fork of **gswitch** (Go, MIT,
github.com/arumata/gswitch) that adds automatic wrong-layout correction (Punto
Switcher-like) for the user's two machines: this host (Fedora 44, GNOME Shell 50, Wayland,
IBus, layouts `us`+`ru`, switch hotkey `<Alt>Shift_L`) and a second machine (Debian 12,
GNOME Shell 43.9, X11 session, IBus 1.5.27, `GTK_IM_MODULE=ibus`, same layouts, switch
`<Super>space`; there `ibus engine` prints `xkb:us::eng` in both layouts, so the layout is
read from the XKB group of the X display instead — `docs/SPEC-X11.md`). Same repository,
same binaries: the daemon selects its layout and focus sources at start
(`runtime/platform.go`), never by build tags. Upstream history is preserved: remote `upstream`
is arumata/gswitch, remote `origin` is github.com/vabuter/gswitch-auto. The tree is
upstream v0.8.0 plus fork commits; the Go module path stays `github.com/arumata/gswitch` to
keep rebases onto upstream releases small. The fork ships as the RPM `gswitch-auto`, which
obsoletes and replaces upstream's `gswitch` package (same binaries and paths). Upstream
gswitch 0.8.0 (manual double-Shift correction only) is installed on the host from the RPM
kept at the repo root (gitignored). `make rpm` builds the replacement
`dist/gswitch-auto-<V>-1.x86_64.rpm`; installing it (`make install`, needs sudo) and the
manual test script (SPEC §6.4) are done by the user.

Read in this order; do not redo the research:

1. `docs/SPEC.md` — the implementation contract: decisions D1–D8 and the corrections
   ledger (§1), architecture (§2), per-package design (§3), integration points in upstream
   files (§4), work packages with file ownership (§5), tests and the manual test script
   (§6). Follow it; do not re-litigate fixed decisions. `docs/SPEC-X11.md` is the addendum
   for the Debian 12 / GNOME 43 / X11 target (decisions X1–X10, source selection matrix
   §2, packaging §3.8, manual script MX1–MX13 §6.3); it wins over SPEC.md for X11.
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

Fork additions (the first three also work from `builds/gswitch` without installing; all
are read-only):

```bash
gswitch --auto-check ghbdtn --shown us         # detector + engine verdict as JSON (fix -> привет, engine arm)
gswitch --layout-state --watch 0               # session, GNOME Shell version, selected layout source (ibus here, x11 on Debian), its state
gswitch --layout-state --watch 0 --source x11  # force a source (here: Xwayland :0, read-only; the daemon would not use it)
gswitch --focus-state --watch 0                # extension view, X11 active window, merged state, policy decision
busctl --user introspect io.github.vabuter.GswitchAuto /io/github/vabuter/GswitchAuto
gnome-extensions info gswitch-focus@vabuter.github.io   # focus extension state (after relogin)
```

To see what the daemon would select on the Debian machine from this host, force the session
type for the diagnostic only: `env XDG_SESSION_TYPE=x11 WAYLAND_DISPLAY= builds/gswitch
--layout-state --watch 0` (connects to Xwayland `:0` read-only; the group changes cannot be
observed here, F16). Never run the daemon itself that way.

Per-user fork settings live in `~/.config/gswitch/{auto.conf,exceptions-words.txt,
exceptions-apps.txt}` (written by the tray only; the daemon reads them). Auto mode is off by
default; LShift+RShift toggles it, LShift+RAlt+RCtrl converts the selection (README has
the full key reference). `auto-strict-purpose=auto` is strict on X11 and when the session
type is unknown, off on Wayland (fail-closed); `layout-source` accepts `auto|ibus|x11|none`
(`ibus` is refused in X11 sessions below GNOME Shell 49.3 and only warned about on Wayland:
the fixed `xkb:us::eng` engine makes it misjudge every word typed in the second layout).

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
- GNOME Shell ≤ 49.2 sets the IBus engine to the fixed `xkb:us::eng` for every XKB source
  (verified in 43.9 `js/ui/status/keyboard.js`); 49.3+ builds `xkb:<layout>:<variant>:<lang>`.
  The daemon probes `org.gnome.Shell ShellVersion` and uses the IBus source only at 49.3+;
  on X11 it polls the XKB group (`XkbGetState`, 50 ms) instead and confirms manual switches
  with upstream's X11 path (`XF86Launch7`, scancode 186). GNOME 43 needs the legacy
  (non-ESM) extension tree and loads extensions only at login (Alt+F2 `r` on X11).
- Focused window is not obtainable on GNOME Wayland (`org.gnome.Shell.Introspect` is
  allowlisted); terminal/password exclusions require a GNOME Shell extension.
- `GrabAll` exists in the input reader but is never called; without grab, typing during
  the ~0.5 s replay interleaves with backspaces. Trigger only on Space, never on Enter.
- Language detector of choice: punto-rs's Bloom + letter-trigram model (MIT, ~2 MB).
  No LLM in the hot path (latency and the daemon's no-network security model).

## Building the fork

Toolchain on this host: golang 1.26 (satisfies `go 1.25.5` in `go.mod`), gcc, make,
`gtk3-devel`, `libX11-devel`, `libxkbcommon-devel`, rpm/rpm2cpio/cpio, python3, node,
podman 5.8.9 (images `golang:1.26-bookworm` and `debian:12` present locally).
nfpm v2.48.0 is at `~/go/bin/nfpm` (**not on PATH**; the Makefile defaults `NFPM` to it).
Not installed: golangci-lint, goreleaser (not used by the fork), rpmbuild, shellcheck,
dpkg, xvfb-run (GTK tests stay gated). Builds use CGO (clipboard, tray).

```bash
make build                                                # builds/gswitch, builds/gswitch-tray (-X main.version)
make test                                                 # go test ./... (upstream ~320 tests + fork tests)
go test ./internal/gswitch/runtime -run 'TestName'        # single test
make vet                                                  # go vet ./...
make ext-check                                            # scripts/check-extension.sh
make version                                              # e.g. 0.8.0+auto.0.dev64.g1a2b3c4 (scripts/version.sh)
make rpm                                                  # dist/gswitch-auto-<V>-1.x86_64.rpm + checksums.txt
scripts/version.sh --verify                               # rpm ordering rules + Obsoletes bound
scripts/version.sh --verify-deb                           # dpkg ordering rules (python fallback on the host)
make install                                              # make rpm + sudo dnf install (user runs this)
make deb                                                  # dist/gswitch-auto_<V>-1_amd64.deb + checksums-deb.txt, built in podman (scripts/build-deb.sh)
make deb-image                                            # only build localhost/gswitch-auto-deb:bookworm (packaging/debian12.Containerfile)
golangci-lint run                                         # config in .golangci.yml (CI only here)
```

`make deb` builds inside `docker.io/library/golang:1.26-bookworm` (Debian 12 ships Go 1.19):
`--userns=keep-id`, `--security-opt label=disable` (never `:Z`), `HOME=/tmp`, module and build
caches on the named volumes `gswitch-auto-gomod`/`gswitch-auto-gocache`, output in
`builds/deb/` and `dist/`; it needs network access for the module download. Never run a podman
build with the host's `builds/` as its target. The `.deb` ships the legacy extension tree and
`scripts/deb/{preinst,postinst}.sh`, which embed upstream's `scripts/{pre,post}install.sh`
between markers (`build-deb.sh` asserts identity). Installing the `.deb` is done by the user on
the Debian machine (`docs/FORK.md`, SPEC-X11 §6.3).

`make rpm` runs `scripts/build-rpm.sh`: preflight (every `src:` in `packaging/nfpm.yaml`
must exist), `scripts/version.sh --verify`, the extension check, `go test ./...`, CGO
build, nfpm, `checksums.txt`, then `rpm -qp` assertions (Provides `gswitch = V-R`,
Obsoletes `gswitch < 0.9`, no Conflicts, packaged user unit byte-identical to upstream)
and, because upstream `gswitch-0.8.0-1` is installed here, an upgrade dry run
(`rpm -U --test`: reads the rpmdb, installs nothing, needs no root). Inspect a build with
`rpm -qpi`, `rpm -qpR`, `rpm -qp --provides/--obsoletes/--scripts`, `rpm -qplv`.
Versions are `<upstream>+auto.<N>` from tags `v<upstream>+auto.<N>`; dev builds get
`.dev<n>.g<sha>[.dirty]`; `~` versions are forbidden (they sort below upstream).
Gated test suites: `GSWITCH_IBUS_TEST=1`, `GSWITCH_DBUS_TEST=1` (`dbus-run-session`),
`GSWITCH_GTK_TEST=1` (`xvfb-run`), `GSWITCH_X11_TEST=1` (read-only against `$DISPLAY`;
passes on the host's Xwayland `:0`), `GSWITCH_X11_TEST=1 GSWITCH_X11_TEST_SWITCH=1`
(locks XKB group 1 and back — **only** inside the podman image with Xvfb, SPEC-X11 §6.2,
never on the host's live Xwayland).

Upstream layout: `internal/gswitch/runtime` (evdev reader, key buffer, conversion/replay —
`switcher.go`, `converter.go`), `internal/gswitch/detect` (layout and hotkey detection
providers), `internal/gswitch/config`, `internal/tray` (GTK settings window, layout
monitor), `configs/` (systemd user unit, udev uaccess rule, polkit policy, default.conf).
Fork additions (SPEC §3, SPEC-X11 §3): `internal/gswitch/{fold,lang,layoutstate,focus,userconfig,ipc}`
(`layoutstate` has the IBus source, the GNOME Shell version guard and the X11 XKB source;
`focus` the extension client, the X11 EWMH source and their merge), new files in `runtime/`
hooked into `switcher.go` by one-line calls (`platform.go` selects the sources), `internal/i18n`
`strGA*` strings (en+ru), `shell-extension/gswitch-focus@vabuter.github.io` (ESM, GNOME 50,
RPM) and `shell-extension/legacy/<uuid>` (GNOME 43/44, `.deb`), `third_party/punto-rs`
licences, `packaging/nfpm.yaml` (replaces the deleted `.goreleaser.yml`),
`packaging/{debian12.Containerfile,debian/copyright}`,
`scripts/{build-rpm,build-deb,version,pretrans,posttrans,check-extension}.sh`, `scripts/deb/`,
`Makefile`. Upstream files that must stay unmodified include `configs/gswitch.service`
and `scripts/{pre,post}{install,remove}.sh` (SPEC §4).

## Housekeeping

Do not commit `gswitch_0.8.0_linux_amd64.rpm` (6 MB binary, `*.rpm` is gitignored); it is
kept only as the exact installed artifact (sha256 verified against the release's
checksums.txt) and is the rollback target in `docs/FORK.md`.
