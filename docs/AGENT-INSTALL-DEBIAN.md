# Installing gswitch-auto on the Debian 12 machine — runbook for an AI agent

You are an AI coding agent running in a terminal on the user's **Debian 12 machine (GNOME Shell
43.9 on Xorg, IBus 1.5.27, layouts `us`+`ru`, layout switch `<Super>space`)**, in a fresh clone of
this repository. Your job: build the `.deb` from this checkout **on this machine**, have the user
install it, bring up the daemon, the focus extension and the tray, verify everything that can be
verified without typing, walk the user through the typing checks, and report. Follow the steps in
order; each step says what to expect and when to stop.

The human-facing version of this procedure (Russian) is [INSTALL-DEBIAN.md](INSTALL-DEBIAN.md);
the design is in [SPEC-X11.md](SPEC-X11.md) (manual script MX1–MX13 in §6.3) and the
install/rollback notes in [FORK.md](FORK.md) («Debian 12»).

## 0. Ground rules

- **Talk to the user in Russian.** Commands, file contents and the code stay in English.
- **`CLAUDE.md` describes the Fedora development host.** Its host facts do not apply here: there is
  no RPM, no installed upstream RPM, no Xwayland `:0` (this display *is* a real X server), no
  podman images, no `~/go/bin/nfpm`, and `make install`/`make rpm` are Fedora-only. Its
  conventions do apply (English code and commits, Russian reports, never modify
  `configs/gswitch.service` or `scripts/{pre,post}{install,remove}.sh`).
- **Root.** You never run `sudo` yourself and never ask for a password. When a step needs root,
  show the exact command in its own `bash` block, ask the user to run it in their own terminal, wait
  until they say it is done (e.g. «готово»), then check the result yourself.
- **Never do these on this machine:**
  - run tests with `GSWITCH_X11_TEST_SWITCH=1` — they lock the XKB group of the live display
    (SPEC-X11 §6.2 allows them only inside a container with Xvfb);
  - start a second daemon (`gswitch`, `gswitch --debug`, `gswitch -d`) while `gswitch.service` is
    active — both would correct the same text; debugging needs the user's consent and
    `systemctl --user stop gswitch.service` first, `start` afterwards;
  - synthesise input (`xdotool`, `ydotool`, `xte`, writing to `/dev/uinput`), lock the XKB group
    (`xkb-switch`, `setxkbmap`, `xdotool key`), restart or kill `gnome-shell`, log the user out,
    or touch `org.gnome.desktop.input-sources` — typing and Alt+F2 are the user's;
  - edit `/etc/gswitch/default.conf`, run `sudo gswitch --configure`, or edit
    `~/.config/gswitch/*` by hand unless a step of the manual script says so and the user agrees
    (the tray writes these files);
  - change tracked files to make a failing build pass, run `--skip-tests` without the user's
    consent, commit, or push. A build or check failure is a finding: collect it and report it
    (the fork is developed on the Fedora host). If the user explicitly asks you to fix something
    here, work on a local branch `debian/<topic>` and do not push without asking.
- **Downloads** are limited to: the Go toolchain from `https://go.dev/dl/` (sha256-verified), Go
  modules through the `go` command (`proxy.golang.org`, verified against `go.sum`), and Debian
  packages through `apt` (run by the user). Nothing is piped into a shell.
- **Keep a log.** Append what you ran and saw to `~/.cache/gswitch-auto-build/install-log.md`
  (create the directory). If the session is interrupted, read it first and resume from the last
  completed step.
- **Stop conditions.** When a check below says *stop*, do not improvise a workaround that changes
  the system: tell the user what failed, show the evidence, and propose the next step.

Throughout, `B` is the build directory and `R` is the repository root:

```bash
B=~/.cache/gswitch-auto-build; R=$(git rev-parse --show-toplevel); mkdir -p "$B"
```

## 1. Preflight (read-only)

Run these and record the outputs in the log; the baseline values are needed for the report and
for rollback.

| Check | Command | Expect | Otherwise |
|---|---|---|---|
| OS | `. /etc/os-release; echo "$ID $VERSION_ID"` | `debian 12` | stop |
| Architecture | `dpkg --print-architecture` | `amd64` | stop: the package is amd64 only |
| Graphical session | `echo "$XDG_SESSION_TYPE $DISPLAY"; echo "$DBUS_SESSION_BUS_ADDRESS"` | `x11 :0` (or `:1`); a `unix:` bus address | stop if empty (SSH, `su`, tmux started outside the session): ask the user to start the agent from a terminal inside the GNOME session |
| GNOME Shell | `gnome-shell --version` | `GNOME Shell 43.9` (any 43.x/44.x works) | 45+ needs the ESM extension (RPM tree): stop |
| Layouts | `gsettings get org.gnome.desktop.input-sources sources` | `[('xkb', 'us'), ('xkb', 'ru')]` | another order or variants is fine, record it |
| Switch keys (baseline) | `gsettings get org.gnome.desktop.wm.keybindings switch-input-source` | `['<Super>space', 'XF86Keyboard']` | record whatever it is (rollback restores it) |
| Extensions (baseline) | `gsettings get org.gnome.shell enabled-extensions; gsettings get org.gnome.shell disable-user-extensions` | record | if `disable-user-extensions` is `true`, tell the user extensions are globally off |
| Existing gswitch | `dpkg-query -W -f='${Package} ${Status} ${Version}\n' 'gswitch*' 2>/dev/null; systemctl --user is-enabled gswitch.service; pgrep -a gswitch` | nothing, or upstream `gswitch` | record: decides step 7 |
| User config | `ls -la ~/.config/gswitch/ 2>&1` | absent, or upstream's `ui.conf` | record |
| IM | `echo "GTK_IM_MODULE=$GTK_IM_MODULE"; pgrep -a ibus-daemon` | `ibus`, a running `ibus-daemon` | record (strict mode relies on IBus, SPEC-X11 §2.2) |
| Checkout | `git -C "$R" status --porcelain; git -C "$R" rev-parse --is-shallow-repository` | empty; `false` | dirty: stop and ask; shallow: `git -C "$R" fetch --unshallow --tags origin` |
| Tags | `git -C "$R" describe --tags --abbrev=0 --match 'v[0-9]*'` | `v0.8.0` (or newer upstream tag) | `git -C "$R" fetch --tags origin`, retry; still failing: stop (the build needs the upstream tag) |
| Version | `"$R/scripts/version.sh"` | `0.8.0+auto.<N>` or `0.8.0+auto.0.dev<n>.g<sha>` | stop |
| Disk | `df -h ~` | ≥ 3 GB free | stop |
| Network | `curl -fsSI https://go.dev/dl/ -o /dev/null && echo ok` | `ok` | stop (needs network for Go and modules) |
| Build deps present | `dpkg-query -W -f='${Package} ${Status}\n' gcc libc6-dev pkg-config libgtk-3-dev libx11-dev libxkbcommon-dev dpkg-dev fakeroot lintian python3 git ca-certificates curl 2>&1` | record which are **not** `install ok installed` | these are the ones step 2 adds (and rollback may remove) |

## 2. Build dependencies (user runs, root)

Ask the user to run, one after the other:

```bash
sudo apt update
```

```bash
sudo apt install --no-install-recommends gcc libc6-dev pkg-config libgtk-3-dev libx11-dev libxkbcommon-dev dpkg-dev fakeroot lintian python3 git ca-certificates curl
```

Then re-run the "Build deps present" check: every package must be `install ok installed`.
This is the package list of `packaging/debian12.Containerfile`, the image the `.deb` is normally
built in; the build below is the same build without the container (`--no-container`).

## 3. Go toolchain (no root)

Debian 12 ships Go 1.19; `go.mod` needs `go 1.25.5`. Install the newest stable **go1.26.x**
(the version of the `golang:1.26-bookworm` build image; if go.dev lists no go1.26 release any
more, take the newest stable release) into `$B/go`, verifying its sha256 against go.dev's own
listing:

```bash
B=~/.cache/gswitch-auto-build; mkdir -p "$B/dl" && python3 -I - "$B/dl" >"$B/dl/selected" <<'EOF'
import hashlib, json, os, sys, urllib.request
dl = sys.argv[1]
rels = json.load(urllib.request.urlopen("https://go.dev/dl/?mode=json"))
stable = [r for r in rels if r.get("stable")]
pick = next((r for r in stable if r["version"].startswith("go1.26")), stable[0])
f = next(f for f in pick["files"] if f["os"] == "linux" and f["arch"] == "amd64" and f["kind"] == "archive")
dst = os.path.join(dl, f["filename"])
if not os.path.exists(dst):
    urllib.request.urlretrieve("https://go.dev/dl/" + f["filename"], dst)
h = hashlib.sha256(open(dst, "rb").read()).hexdigest()
if h != f["sha256"]:
    os.remove(dst)
    sys.exit("sha256 mismatch for %s: %s != %s" % (f["filename"], h, f["sha256"]))
print(dst)
EOF
```

This prints nothing on success and leaves the verified archive's path in `$B/dl/selected`; a
non-zero exit (network error, sha256 mismatch) is a *stop*. Then unpack it (the archive's top
directory is `go/`; if `$B/go` exists from an earlier attempt, remove it first):

```bash
B=~/.cache/gswitch-auto-build; test ! -e "$B/go" && tar -C "$B" -xzf "$(cat "$B/dl/selected")"
```

Check: `"$B/go/bin/go" version` prints `go version go1.26.<x> linux/amd64`.

## 4. nfpm (no root)

```bash
B=~/.cache/gswitch-auto-build; env GOTOOLCHAIN=local GOPATH="$B/gopath" GOMODCACHE="$B/gopath/pkg/mod" GOCACHE="$B/gocache" GOBIN="$B/bin" "$B/go/bin/go" install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.48.0
```

Check: `"$B/bin/nfpm" --version` reports `v2.48.0`.

## 5. Build the `.deb` (no root, several minutes)

Run the build **with a clean environment**: the test suite must not see the live session
(`DISPLAY`, the session bus), exactly as in the build container where this build is verified.
Nothing in the build needs the session.

```bash
B=~/.cache/gswitch-auto-build; cd "$(git rev-parse --show-toplevel)" && env -i HOME="$HOME" USER="$USER" LOGNAME="$LOGNAME" LANG=C.UTF-8 TERM=dumb PATH="$B/go/bin:$B/bin:/usr/local/bin:/usr/bin:/bin" GOTOOLCHAIN=local GOFLAGS=-buildvcs=false GOPATH="$B/gopath" GOMODCACHE="$B/gopath/pkg/mod" GOCACHE="$B/gocache" NFPM="$B/bin/nfpm" scripts/build-deb.sh --no-container >"$B/build-deb.log" 2>&1; echo "exit=$?"
```

Expect `exit=0`. Then:

- `tail -n 40 "$B/build-deb.log"`: no `FAIL` lines; lintian reports **0 errors**. Lintian
  warnings are expected and not fatal: `hardening-no-pie` (both binaries),
  `maintainer-script-calls-systemctl`, `maintainer-script-ignores-errors` (upstream scriptlets),
  `no-manual-page`.
- `ls -l "$R"/dist/` shows `gswitch-auto_<V>-1_amd64.deb` and `checksums-deb.txt`;
  `(cd "$R/dist" && sha256sum -c checksums-deb.txt)` prints `OK`.
- `dpkg-deb -f "$R"/dist/gswitch-auto_*_amd64.deb Package Version Depends Recommends Provides Replaces Conflicts`
  shows `Package: gswitch-auto`, `Version: <V>-1`, `Provides: gswitch (= <V>-1)`, `Replaces: gswitch`,
  `Conflicts: gswitch`.

On failure: stop; report the failing step (`grep -n -E 'FAIL|error|die' "$B/build-deb.log" | head`)
and the last 80 lines of the log. Do not retry with `--skip-tests` unless the user agrees.

Alternatives, only if the user prefers them: (a) the user copies a `.deb` built on the Fedora host
(`make deb`) together with its `checksums-deb.txt` — verify with `sha256sum -c` and continue at
step 6; (b) `sudo apt install podman` and `make deb` (container build; not tested with Debian 12's
podman 4.3).

## 6. Install (user runs, root)

Show the absolute path of the built file and ask the user to run:

```bash
sudo apt install /ABSOLUTE/PATH/TO/dist/gswitch-auto_<V>-1_amd64.deb
```

(`apt` needs `./` or an absolute path, otherwise it searches the archive.) Expected: apt installs
the Depends and Recommends (`gnome-shell-extension-appindicator`, `wl-clipboard`, …); if upstream
`gswitch` was installed, apt removes it and the output contains
`gswitch-auto: enabled gswitch.service for <user> (the removal of the gswitch package had disabled it)`.
A notice `Download is performed unsandboxed as root` is harmless.

## 7. Daemon checks (agent)

| Check | Command | Expect |
|---|---|---|
| Package | `dpkg-query -W -f='${Status} ${Version}\n' gswitch-auto; dpkg-query -W -f='${Status}\n' gswitch 2>&1` | `install ok installed <V>-1`; `gswitch` not installed or `deinstall ok config-files` |
| Binary | `command -v gswitch; gswitch --version` | `/usr/bin/gswitch`; `gswitch version <V>` |
| Service | `systemctl --user is-enabled gswitch.service; systemctl --user is-active gswitch.service` | `enabled`, `active` |
| Start log | `journalctl --user -u gswitch.service -b --no-pager \| grep -E 'layout sources:\|XF86Launch7\|Warning\|rror'` | `layout sources: x11 (GNOME Shell 43.9); focus sources: extension, x11; strict purpose: true` and `Added <Shift>XF86Launch7 to GNOME layout-switch bindings for automatic correction` |
| Switch keys | `gsettings get org.gnome.desktop.wm.keybindings switch-input-source` | the baseline plus `'XF86Launch7'` and `'<Shift>XF86Launch7'` (SPEC-X11 F9, F21) |
| Layout source | `gswitch --layout-state --watch 0` | `Session: x11`, `GNOME Shell: 43.9`, `Daemon selection: layout sources: x11 …`, a table that starts with `us,ru`, the current group mapped to a layout, no pair-order warning |
| Focus | `gswitch --focus-state --watch 0` | `X11:` connected with the terminal's `WM_CLASS`; `Extension: absent` until step 8 |

If the service is `disabled` and upstream gswitch was **not** installed before (fresh install —
the package enables the unit only when it replaces upstream), enable it (user-level, no root):
`systemctl --user enable --now gswitch.service`, then re-check. If it is `failed`: stop, report
`systemctl --user status gswitch.service --no-pager` and the last 50 journal lines.

If the journal shows `Warning: cannot add <Shift>XF86Launch7 …`, automatic corrections still work
but wait ~2.5 s for GNOME's popup and drop the keys typed meanwhile: report it.

D-Bus status (read a property with
`busctl --user get-property io.github.vabuter.GswitchAuto /io/github/vabuter/GswitchAuto io.github.vabuter.GswitchAuto <Name>`):
`LayoutSource` ⇒ `"x11"`, `AutoEnabled` ⇒ `false` (off by default), `AutoUnavailableReason` ⇒
`"focus-extension-required"` until the extension is active (strict mode; step 8).

## 8. Focus extension and tray (user action, then agent)

1. Ask the user: press **Alt+F2**, type **`r`**, press **Enter**. On Xorg this restarts GNOME Shell
   in place; windows, this terminal and the session survive. GNOME 43 scans extension directories
   only at Shell start, so the extension is invisible before this.
2. `gnome-extensions info gswitch-focus@vabuter.github.io`. If the state is not `ACTIVE`, run
   `gnome-extensions enable gswitch-focus@vabuter.github.io` and check again ⇒ `ACTIVE`.
   "does not exist" ⇒ the Shell was not restarted yet. `ERROR` ⇒ stop and report
   `journalctl -b --no-pager /usr/bin/gnome-shell | grep -i -A5 gswitch`.
   Confirm the legacy variant: `grep -c 'function init' /usr/share/gnome-shell/extensions/gswitch-focus@vabuter.github.io/extension.js` ⇒ `1`.
3. Tray icon: `gnome-extensions info appindicatorsupport@rgcjonas.gmail.com`; if it is installed
   but not enabled, tell the user and enable it (`gnome-extensions enable …`) — the tray icon needs
   it.
4. Tray: if `pgrep -x gswitch-tray` finds a running (old) tray, `pkill -x gswitch-tray`; then
   `gio launch /usr/share/applications/gswitch-tray.desktop`. After a few seconds
   `pgrep -x gswitch-tray` finds it, and `ls ~/.config/gswitch/` lists `auto.conf`,
   `exceptions-words.txt`, `exceptions-apps.txt` (the tray seeds them; the apps list includes
   terminals, `xterm`, `keepassxc`, `pinentry-qt`, …). From the next login on, it autostarts.
5. D-Bus: `FocusSource` ⇒ `"active"`, `AutoUnavailableReason` ⇒ `""`, `AutoAvailable` ⇒ `true`.
   `gswitch --focus-state --watch 0` ⇒ `Extension: active`, merged `app source: extension`.

## 9. Typing checks (the user types, you observe)

You never type or inject keys. Ask the user to do each step, then read the D-Bus properties
(`AutoEnabled`, `LastCorrection`, `Stats`, `FocusSuppressedReason`, `AutoUnavailableReason`) and
compare. A property that depends on the focused window is read with `sleep 5; busctl …` started
here, while the user focuses the window under test within 5 s.

1. **Toggle.** LShift+RShift ⇒ `AutoEnabled` ⇒ `true`; the tray shows auto on.
2. **Corrections** in gedit or gnome-text-editor (layout us): `ghbdtn ` ⇒ `привет ` and the
   indicator shows ru; `руддщ ` (layout ru) ⇒ `hello `; `hello ` unchanged; `ghbdtn` + Enter
   unchanged (Space is the only trigger); Double-Shift right after a fix undoes it.
   `LastCorrection` and `Stats` change accordingly.
3. **Speed, no lost keys** (MX6 (c), SPEC-X11 F21): type `ghbdtn ` and, as soon as it turns into
   `привет `, continue `rfr ltkf ` at normal speed ⇒ `привет как дела ` with no letter missing;
   the input-source popup never appears; the fix appears with no noticeable (~1.5 s) delay.
4. **Terminal.** In gnome-terminal, `ghbdtn ` ⇒ unchanged; `FocusSuppressedReason` read inside it
   ⇒ `"terminal"` or `"excluded-app"`.
5. **Password field** (MX9 (a)): the user runs `zenity --password | od -c`, types `ghbdtn `, waits
   ~1 s, presses Enter ⇒ `od` prints `g   h   b   d   t   n` and a space (unchanged), the
   indicator stays en.
6. **Manual mode** (MX10): with auto off, `ghbdtn` + double LShift ⇒ `привет`; LShift+RAlt+RCtrl
   on a selected wrong-layout word converts it.

Then offer to walk through the rest of SPEC-X11 §6.3 (MX1–MX13); record each result.

## 10. Report (in Russian)

Write the report to `~/.cache/gswitch-auto-build/report.md` and show it to the user:

- version `<V>`, commit (`git -C "$R" rev-parse --short HEAD`), sha256 of the `.deb`;
- the baseline (step 1) and what changed: packages installed (build deps added in step 2,
  dependencies pulled by step 6), `switch-input-source` before/after, extensions enabled, whether
  upstream gswitch was replaced;
- results of steps 5, 7, 8 (with the log lines) and each typing check (pass / fail / skipped,
  with the observed text);
- warnings from the journal, anything that deviated from this runbook, and open questions.

For a problem that needs a code change, also save the evidence (journal excerpt, `--layout-state`
and `--focus-state` output, the busctl values) so the user can bring it to the Fedora host.

## 11. Rollback (on request)

1. User: `sudo apt remove gswitch-auto` (`purge` also drops `/etc/gswitch`). If upstream gswitch
   was installed before, the user reinstalls their upstream `.deb` instead
   (`sudo apt install ./gswitch_0.8.0_linux_amd64.deb`), then
   `systemctl --user enable --now gswitch.service`.
2. Restore the switch keys to the baseline from step 1 (the user's consent first):
   `gsettings set org.gnome.desktop.wm.keybindings switch-input-source "<baseline>"`.
3. `gnome-extensions disable gswitch-focus@vabuter.github.io` (only if the package is gone and the
   user wants it off).
4. Build leftovers: `rm -rf ~/.cache/gswitch-auto-build` (Go toolchain, caches, logs — keep the
   report if the user wants it). The build packages that step 2 **added** (and only those, per the
   preflight record) can be removed by the user with `sudo apt purge <those packages>` followed by
   `sudo apt autoremove`.
5. `~/.config/gswitch/` stays (user data); delete it only on request.
