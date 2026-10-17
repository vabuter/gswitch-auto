# Contributing to gswitch-auto

Thanks for your interest in improving gswitch-auto!

## How this repository works

gswitch-auto is a fork of [gswitch](https://github.com/arumata/gswitch) that
adds automatic layout correction. Upstream develops in a private tree, lands
each release as a single squashed commit and declined automatic correction, so
this fork is rebased onto every upstream release instead of sending changes
back (see [docs/FORK.md](docs/FORK.md), in Russian).

- **Issues and pull requests** for the fork go to
  [vabuter/gswitch-auto](https://github.com/vabuter/gswitch-auto). For
  anything non-trivial, please open an issue first so we can agree on the
  approach.
- **Manual-mode bugs that reproduce with upstream gswitch** (double Shift,
  selection conversion, layout detection) belong
  [upstream](https://github.com/arumata/gswitch/issues); please check there
  first.
- **Keep the upstream diff small.** Prefer new files over edits to upstream
  files; when an upstream file must change, keep the edit to a one-line hook
  that calls into a fork file. `docs/FORK.md` lists the upstream files the
  fork modifies; update it when that list changes.

## Building and testing

Requirements: Go (version from `go.mod`), a C compiler (CGO), and for the tray
application the GTK3 headers. On Fedora:

```bash
sudo dnf install golang gcc make pkgconf-pkg-config gtk3-devel libxkbcommon-devel libX11-devel \
    rpm cpio python3 nodejs
```

On Debian/Ubuntu: `libgtk-3-dev`, `libxkbcommon-dev`, `libx11-dev` (plus
`rpm`, `rpm2cpio` and `cpio` to build the RPM).

Packaging uses [nFPM](https://nfpm.goreleaser.com/) v2.48.0 (GoReleaser is not
used by the fork):

```bash
go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.48.0   # installs ~/go/bin/nfpm
```

Make targets:

```bash
make build       # builds/gswitch and builds/gswitch-tray (CGO, -X main.version)
make test        # go test ./...
make vet         # go vet ./...
make lint        # golangci-lint run ./... (v2, config in .golangci.yml)
make ext-check   # GNOME Shell extension consistency checks
make version     # package version, e.g. 0.8.0+auto.1 (scripts/version.sh)
make rpm         # dist/gswitch-auto-<version>-1.x86_64.rpm + dist/checksums.txt
make install     # make rpm, then sudo dnf install it
make clean       # remove builds/ and dist/
```

Single test: `go test ./internal/gswitch/runtime -run 'TestName'`. Suites that
need a live desktop are gated by environment variables: `GSWITCH_IBUS_TEST=1`
(live IBus), `GSWITCH_DBUS_TEST=1` (run under `dbus-run-session`) and
`GSWITCH_GTK_TEST=1` (run under `xvfb-run`).

To try your build: `./builds/gswitch --debug` with the user service stopped
(`systemctl --user stop gswitch.service`). The active graphical user needs the
udev/logind ACLs installed by the package for `/dev/input/event*` and
`/dev/uinput`; do not run development builds as root to bypass missing ACLs.

## Guidelines

- Code, comments and commit messages are in English; keep upstream's code
  style.
- Keep code `gofmt`/`goimports`-clean and passing `.golangci.yml`.
- **Interface strings:** new strings added by the fork use i18n IDs with the
  `strGA` prefix (not `strAuto`, which collides with upstream's
  `strAutoDetect*`) and must exist in both `internal/i18n/locales/en.json`
  and `ru.json`. Other locales may omit them and fall back to English.
- Prefer table-driven tests with deterministic inputs; tests must not require
  root, `/dev/input`, or a running desktop session (use the gated suites
  above for anything that does).
- Don't edit generated files (`*_generated.go`) by hand.
- Fork D-Bus clients use private connections (`dbus.ConnectSessionBus()`),
  never the shared `dbus.SessionBus()` used by the tray's systray library.
- Treat changes as security-sensitive: gswitch runs as the session user but
  has privileged raw-input and input-injection access. No network code, no
  persisting of keystrokes, and the daemon writes nothing under `$HOME` — see
  [the security policy](.github/SECURITY.md).

## Conduct

Be respectful. See [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).
