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
  podman images, no `~/go/bin/nfpm`, and its build and test commands (`make test`, `make build`,
  `go test ./...`, `make rpm`, `make install`, …) are not for this machine: step 5 is the only
  build and test run. Its conventions do apply (English code and commits, Russian reports, never
  modify `configs/gswitch.service` or `scripts/{pre,post}{install,remove}.sh`).
- **Root.** You never run `sudo` or `su` yourself and never ask for a password. When a step needs
  root, show the exact command in its own `bash` block and ask the user to run it in a **separate
  terminal window or tab** (not with `!` inside this agent session: sudo cannot ask for the
  password there). Wait until they say it is done (e.g. «готово»), then check the result yourself.
  If the preflight shows the user is not in group `sudo` (a root password was set during the
  Debian installation), ask how they become root; if with the root password, give every root
  command as `su -c '<command>'`. Never suggest changing sudoers or group membership.
- **Variables do not survive between your shell calls** (Claude Code keeps only the working
  directory). Every command below starts by defining what it uses: `B=~/.cache/gswitch-auto-build`
  (build directory, everything this procedure downloads or builds lives there) and
  `R=~/p/gswitch-auto` (the clone). Never drop these prefixes. If the clone is somewhere else
  (`git rev-parse --show-toplevel` in your working directory prints another path), replace
  `~/p/gswitch-auto` with that path in every command. In an `rm`, never use a variable: write the
  literal path.
- **Long commands.** Steps 3, 4 and 5 take minutes each (download, `go install` and module
  download, build with tests; the build took 6–13 minutes in testing). Run each as a background
  command (Claude Code Bash: `run_in_background: true`) and wait for its `exit=` line, or give the
  call the maximum timeout (600000 ms). The network check of step 1 takes seconds, but up to
  5 minutes when it retries: give it the maximum timeout too. A tool timeout is not a result:
  before re-running a step, check that no earlier run is still going
  (`pgrep -a -f '[b]uild-deb.sh|[n]fpm@v2|[g]o mod download|[g]switch-auto-build/dl'` prints
  nothing; the last pattern is step 3's download and unpacking).
- **Network errors.** This network may drop TLS handshakes to Google hosts (`go.dev`,
  `dl.google.com`, `proxy.golang.org`, `sum.golang.org`); in testing about one in four timed out.
  The Python commands retry internally; for the `go` commands the step says when one more run is
  allowed.
- **Never do these on this machine:**
  - run the test suite yourself (`go test`, `go build`, `go vet`, `make test`, …) outside
    step 5's command, change or drop its `env -i`, or set any `GSWITCH_*_TEST` variable — the GTK
    tests would open windows on the live display, `GSWITCH_X11_TEST_SWITCH=1` locks the XKB group
    (SPEC-X11 §6.2 allows it only inside a container with Xvfb);
  - start a second daemon (`gswitch`, `gswitch --debug`, `gswitch -d`) while `gswitch.service` is
    active — both would correct the same text; debugging needs the user's consent and
    `systemctl --user stop gswitch.service` first, `start` afterwards;
  - synthesise input (`xdotool`, `ydotool`, `xte`, writing to `/dev/uinput`), lock the XKB group
    (`xkb-switch`, `setxkbmap`, `xdotool key`), restart or kill `gnome-shell` or `ibus`, log the
    user out, or touch `org.gnome.desktop.input-sources` — typing and Alt+F2 are the user's;
  - edit `/etc/gswitch/default.conf`, run `sudo gswitch --configure`, or edit
    `~/.config/gswitch/*` by hand unless a step of the manual script says so and the user agrees
    (the tray writes these files);
  - propose a bare `sudo apt autoremove`, or `apt purge` of anything the preflight did not record
    as added by this procedure;
  - change tracked files to make a failing build pass, run `--skip-tests` without the user's
    consent, commit, or push. A build or check failure is a finding: collect it and report it
    (the fork is developed on the Fedora host). If the user explicitly asks you to fix something
    here, work on a local branch `debian/<topic>` and do not push without asking.
- **Downloads** are limited to: the Go toolchain from `https://go.dev/dl/` (redirects to
  `dl.google.com`; sha256-verified against go.dev's listing), Go modules through the `go` command
  (`proxy.golang.org`; checked against `go.sum` for the build and against `sum.golang.org` for
  nfpm), upstream's tag `v0.8.0` from `https://github.com/arumata/gswitch.git` (only as the Tags
  fallback of step 1), and Debian packages through `apt` (run by the user). Nothing is piped into
  a shell.
- **Keep a log.** Append what you ran and saw to `~/.cache/gswitch-auto-build/install-log.md`
  (`mkdir -p ~/.cache/gswitch-auto-build` first), and after each step a line `STEP <n> DONE` or
  `STOPPED: <reason>`. If the log exists when you start, read it first and resume after the last
  `STEP <n> DONE`; restore anything a half-done manual-script step left changed (step 9).
- **Messages.** The user's locale may be Russian. The texts quoted under *Expect* are the
  English ones; the commands whose output is compared set `LC_ALL=C.UTF-8 LANGUAGE=` for that
  reason (a non-empty `LANGUAGE`, e.g. `ru_RU:ru`, overrides `LC_ALL=C.UTF-8` for translated
  messages; only an empty one does not). If you run anything else, set both the same way or
  compare values, not translated wording.
- **Stop conditions.** When a check below says *stop*, do not improvise a workaround that changes
  the system: tell the user what failed, show the evidence, and propose the next step.

## 1. Preflight (read-only)

Run this block and record its whole output in the log; the baseline values are needed for the
report and for rollback.

```bash
B=~/.cache/gswitch-auto-build; R=~/p/gswitch-auto
echo "== locale"; echo "LANG=$LANG LANGUAGE=$LANGUAGE LC_ALL=$LC_ALL"; export LC_ALL=C.UTF-8 LANGUAGE=
echo "== os"; . /etc/os-release; echo "$ID $VERSION_ID"; dpkg --print-architecture
echo "== user"; id -u; id -nG; command -v sudo
echo "== session"; echo "$XDG_SESSION_TYPE $DISPLAY"; echo "$DBUS_SESSION_BUS_ADDRESS"
echo "== shell"; gnome-shell --version; command -v gjs; gdbus call --session --dest org.gnome.Shell --object-path /org/gnome/Shell --method org.freedesktop.DBus.Properties.Get org.gnome.Shell ShellVersion 2>&1
echo "== layouts"; gsettings get org.gnome.desktop.input-sources sources; gsettings get org.gnome.desktop.input-sources xkb-options; gsettings get org.gnome.desktop.input-sources per-window
echo "== switch keys"; gsettings get org.gnome.desktop.wm.keybindings switch-input-source
echo "== extensions"; gsettings get org.gnome.shell enabled-extensions; gsettings get org.gnome.shell disable-user-extensions
echo "== gswitch"; dpkg-query -W -f='${Package} ${Status} ${Version}\n' 'gswitch*' 2>&1; systemctl --user is-enabled gswitch.service 2>&1; pgrep -a gswitch
echo "== shadowing"; ls -d /usr/local/bin/gswitch* ~/.local/bin/gswitch* ~/.config/systemd/user/gswitch.service* /etc/systemd/user/gswitch.service* 2>/dev/null
echo "== user config"; ls -la ~/.config/gswitch/ 2>&1
echo "== im"; echo "GTK_IM_MODULE=$GTK_IM_MODULE"; pgrep -a ibus-daemon; dpkg-query -W -f='${Package} ${Status}\n' ibus-gtk3 ibus-gtk4 2>&1
echo "== other tools"; pgrep -a -f -i '[x]neur|[p]unto|[k]eyd|[i]nput-remapper|[k]monad|[x]remap'
echo "== checkout"; git -C "$R" rev-parse --show-toplevel; git -C "$R" status --porcelain; git -C "$R" rev-parse --is-shallow-repository
echo "== tags"; git -C "$R" describe --tags --abbrev=0 --match 'v[0-9]*' --exclude '*+*' --exclude '*-*' 2>&1
echo "== version"; "$R/scripts/version.sh" 2>&1
echo "== disk"; df -BG --output=target,avail ~ / | tail -n +2
echo "== build deps"; dpkg-query -W -f='${Package} ${Status}\n' gcc libc6-dev pkg-config libgtk-3-dev libx11-dev libxkbcommon-dev dpkg-dev fakeroot lintian python3 git ca-certificates 2>&1
echo "== autoremove"; apt-get -s autoremove 2>/dev/null | grep '^Remv'
echo "== proxy"; env | grep -i -E '^(https?|no)_proxy='
echo "== previous run"; ls "$B" 2>&1
```

| Section | Expect | Otherwise |
|---|---|---|
| locale | record (e.g. `LANG=ru_RU.UTF-8 LANGUAGE= LC_ALL=`); the rest of the block runs with `LC_ALL=C.UTF-8 LANGUAGE=` | — |
| os | `debian 12`, `amd64` | stop (the package is amd64 only) |
| user | not `0`; groups include `sudo`; `/usr/bin/sudo` | `0`: stop, the agent must run as the desktop user. No `sudo` group or no sudo: ask how the user becomes root (§0 Root) |
| session | `x11` and a display such as `:0` or `:1`; a `unix:` bus address | `wayland`: stop — ask the user to log out, pick «GNOME на Xorg» with the gear icon on the login screen, log in and start the agent again (on GNOME 43 under Wayland the daemon has no layout source and Alt+F2 `r` does not exist). Empty (SSH, `su`, tmux started outside the session): stop, ask the user to start the agent from a terminal inside the GNOME session |
| shell | `GNOME Shell 43.9` (any 43.x/44.x works); `/usr/bin/gjs`; `(<'43.9'>,)` | 45+: stop (needs the ESM extension of the RPM tree). No `gjs`: every `gnome-extensions` command fails with exit 2 and no output (gnome-shell's `org.gnome.Shell.Extensions` D-Bus service, which it calls, runs under `/usr/bin/gjs`) — add `gjs` to step 2. `Error connecting: …` or `Error: GDBus.Error:…`: this shell cannot reach the session bus or the Shell — stop as for an empty session row |
| layouts | `[('xkb', 'us'), ('xkb', 'ru')]`; `@as []` or options without a Shift `grp:` entry; `false` | the first two sources must be `xkb` `us` and `ru` in any order (variants are fine); anything else makes auto mode unavailable (`unsupported-layouts`): tell the user before building. A `grp:` option with `shift` (e.g. `grp:shifts_toggle`) collides with LShift+RShift and double Shift: tell the user. `per-window` `true`: record |
| switch keys | `['<Super>space', 'XF86Keyboard']` | record whatever it is (rollback restores it) |
| extensions | record; `false` | `disable-user-extensions` `true`: tell the user extensions are globally off |
| gswitch | never installed: `dpkg-query: no packages found matching gswitch*` and `Failed to get unit file state for gswitch.service: No such file or directory`; or upstream `gswitch install ok installed 0.8.0…`, `gswitch deinstall ok config-files`, or an earlier `gswitch-auto` (record its version) and the unit's state | record: decides steps 6 and 7 |
| shadowing | nothing | stop and ask: a local binary or unit override would hide the package |
| user config | `No such file or directory`, or upstream's `ui.conf` only (earlier fork files: record them) | record |
| im | `GTK_IM_MODULE=ibus`; a running `ibus-daemon`; `ibus-gtk3 install ok installed`, `ibus-gtk4 install ok installed` | record; a missing `ibus-gtk3`/`ibus-gtk4` goes into step 2 (without it GTK apps report no field type and strict mode denies them as `purpose-unknown`; GTK apps must be restarted afterwards) |
| other tools | nothing | record and ask the user to stop that tool during the checks (it handles the same keys) |
| checkout | `/home/<user>/p/gswitch-auto`; nothing; `false` | dirty: stop and ask; shallow: `R=~/p/gswitch-auto; git -C "$R" fetch --unshallow --tags origin` and re-run the block |
| tags | `v0.8.0` (the upstream base; fork tags `v0.8.0+auto.<N>` are excluded on purpose) | `R=~/p/gswitch-auto; git -C "$R" fetch --tags origin` and retry. Still `No names found`: `R=~/p/gswitch-auto; git -C "$R" fetch --no-tags https://github.com/arumata/gswitch.git tag v0.8.0` (upstream's own tag; the fork keeps upstream history) and retry. Still failing: stop (the build needs the tag) |
| version | `0.8.0+auto.<N>` or `0.8.0+auto.<N>.dev<n>.g<sha>` (`N` is 0 without a fork tag), no `.dirty` | stop. Ignore its hint `git fetch upstream --tags`: this clone has no `upstream` remote — use the Tags row |
| disk | home ≥ `3G`, `/` ≥ `1G` | stop |
| build deps | record which are **not** `install ok installed` (`no packages found matching …` counts as not installed) | these are the ones step 2 adds (and rollback may remove) |
| autoremove | record (the baseline for rollback) | — |
| proxy | usually nothing | record: steps 4 and 5 pass `https_proxy`/`HTTPS_PROXY`/`no_proxy`/`NO_PROXY` through |
| previous run | `No such file or directory`, or only `install-log.md` | an earlier run: read `install-log.md` and resume (§0) |

Network (uses only `python3`, which a GNOME desktop has; `curl` is usually not installed; up to
three attempts per host, give the call the maximum timeout):

```bash
python3 -I - <<'EOF'
import time, urllib.error, urllib.request
for url in ("https://go.dev/dl/?mode=json", "https://dl.google.com/go/", "https://proxy.golang.org/", "https://sum.golang.org/"):
    for attempt in range(3):
        try:
            urllib.request.urlopen(url, timeout=20).close()
        except urllib.error.HTTPError:
            pass  # the host answered
        except OSError as e:  # URLError, timeouts, resets
            if attempt == 2:
                raise
            print("retry", url, e)
            time.sleep(5)
            continue
        break
    print("ok", url)
EOF
```

Expect four `ok` lines (`retry` lines before them are fine). A traceback (after three attempts)
gets one more run of the block a few minutes later; a second traceback is a *stop* (Go downloads
redirect to `dl.google.com`; modules come from `proxy.golang.org` and are checked against
`sum.golang.org`).

Before step 6, tell the user if upstream `gswitch` is installed or in `config-files` state while
`is-enabled` is not `enabled`: the package will enable the unit (heuristic «upstream is or was
installed», FORK.md «Debian 12»); `systemctl --user disable --now gswitch.service` afterwards
undoes it.

## 2. Build dependencies (user runs, root)

Ask the user to run, one after the other:

```bash
sudo apt update
```

```bash
sudo apt install --no-install-recommends gcc libc6-dev pkg-config libgtk-3-dev libx11-dev libxkbcommon-dev dpkg-dev fakeroot lintian python3 git ca-certificates
```

If the preflight found `ibus-gtk3`, `ibus-gtk4` or `gjs` missing, append them to that command
(they are runtime pieces, not build dependencies). Then re-run the "build deps" line of the
preflight: every package must be `install ok installed`. This is the build package list of
`packaging/debian12.Containerfile` minus `xvfb` and `x11-xkb-utils`, which only the gated X11 tests
use; the build below is the same build without the container (`--no-container`).

## 3. Go toolchain (no root, minutes)

Debian 12 ships Go 1.19; `go.mod` needs `go 1.25.5`. Install the newest stable **go1.26.x**
(the version of the `golang:1.26-bookworm` build image) into `$B/go`, verifying its sha256
against go.dev's own listing. Run it in the background (§0):

```bash
B=~/.cache/gswitch-auto-build; mkdir -p "$B/dl" "$B/home" && python3 -I - "$B/dl" >"$B/dl/selected" <<'EOF'
import hashlib, json, os, socket, sys, time, urllib.request
socket.setdefaulttimeout(60)
dl = sys.argv[1]
def retry(fn):
    for i in range(3):
        try:
            return fn()
        except OSError as e:  # URLError, timeouts, resets
            if i == 2:
                raise
            print("retry:", e, file=sys.stderr)
            time.sleep(5)
rels = retry(lambda: json.load(urllib.request.urlopen("https://go.dev/dl/?mode=json&include=all")))
cands = [r for r in rels if r.get("stable") and r["version"].startswith("go1.26.")]
if not cands:
    sys.exit("go.dev lists no stable go1.26.x release")
pick = max(cands, key=lambda r: int(r["version"].split(".")[2]))
f = next(f for f in pick["files"] if f["os"] == "linux" and f["arch"] == "amd64" and f["kind"] == "archive")
dst = os.path.join(dl, f["filename"])
src = dst if os.path.exists(dst) else dst + ".part"
if src != dst:
    retry(lambda: urllib.request.urlretrieve("https://go.dev/dl/" + f["filename"], src))
h = hashlib.sha256()
with open(src, "rb") as fh:
    for chunk in iter(lambda: fh.read(1 << 20), b""):
        h.update(chunk)
if h.hexdigest() != f["sha256"]:
    os.remove(src)
    sys.exit("sha256 mismatch for %s: %s != %s" % (f["filename"], h.hexdigest(), f["sha256"]))
os.replace(src, dst)
print(dst)
EOF
echo "exit=$?"
```

Expect `exit=0`, possibly after `retry:` lines (the script makes up to three attempts per
request); the verified archive's path is in `$B/dl/selected`. The download goes to a `.part` file
that is renamed only after the sha256 matched, so an interrupted or failed run is simply run
again: a network error after the script's own attempts (a traceback ending in `URLError`,
`TimeoutError` or `ConnectionResetError`) gets one more run a few minutes later; a second one or a
`sha256 mismatch` is a *stop*. Then unpack it (the archive's top directory is `go/`):

```bash
B=~/.cache/gswitch-auto-build; if [ -e "$B/go" ]; then echo "exists: $B/go"; else tar -C "$B" -xzf "$(cat "$B/dl/selected")" && echo unpacked; fi
```

`exists:` means an earlier attempt unpacked it: if the check below passes, keep it; otherwise
remove it with `rm -rf ~/.cache/gswitch-auto-build/go` and unpack again.

Check (`HOME` points into `$B` so that Go's settings and telemetry files stay there):

```bash
B=~/.cache/gswitch-auto-build; env -i HOME="$B/home" GOTOOLCHAIN=local "$B/go/bin/go" version
```

Expect `go version go1.26.<x> linux/amd64`.

## 4. nfpm and the Go modules (no root, minutes)

Same isolation as the build: a clean environment, `HOME` inside `$B`, so no Go setting of the
user's shell or `~/.config/go/env` (e.g. `GOSUMDB=off`, `GOFLAGS`, `GOPROXY`) can weaken the
checksum verification. This step does all the remaining downloads, so that step 5 needs no
network. Run each command in the background:

```bash
B=~/.cache/gswitch-auto-build; mkdir -p "$B/home" && env -i HOME="$B/home" PATH="$B/go/bin:/usr/bin:/bin" ${https_proxy:+"https_proxy=$https_proxy"} ${HTTPS_PROXY:+"HTTPS_PROXY=$HTTPS_PROXY"} ${no_proxy:+"no_proxy=$no_proxy"} ${NO_PROXY:+"NO_PROXY=$NO_PROXY"} GOTOOLCHAIN=local GOPATH="$B/gopath" GOMODCACHE="$B/gopath/pkg/mod" GOCACHE="$B/gocache" GOBIN="$B/bin" "$B/go/bin/go" install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.48.0 >"$B/nfpm-install.log" 2>&1; echo "exit=$?"
```

Expect `exit=0`. A failed module download (`dial tcp`, `TLS handshake timeout`, `i/o timeout` in
the log) gets one more run of the same command; any other failure, or a second one, is a *stop*:
report `tail -n 40` of `$B/nfpm-install.log`. A `SECURITY ERROR` / `checksum mismatch` is always a
*stop*. Check:

```bash
B=~/.cache/gswitch-auto-build; env -i HOME="$B/home" GOTOOLCHAIN=local "$B/go/bin/go" version -m "$B/bin/nfpm" | awk '$1 == "mod" {print $2, $3, $4}'
```

Expect exactly `github.com/goreleaser/nfpm/v2 v2.48.0 h1:Yvd07lX8YqDD+rtTwVqyeQiDX+7cnun/jFbZD501uB8=`.
(`nfpm --version` prints `GitVersion: dev` for a `go install` build: that is expected, not a
reason to fetch nfpm another way.)

Then the modules of the checkout (`go.mod`, verified against its `go.sum`), into the cache step 5
uses:

```bash
B=~/.cache/gswitch-auto-build; R=~/p/gswitch-auto; (cd "$R" && env -i HOME="$B/home" PATH="$B/go/bin:/usr/bin:/bin" ${https_proxy:+"https_proxy=$https_proxy"} ${HTTPS_PROXY:+"HTTPS_PROXY=$HTTPS_PROXY"} ${no_proxy:+"no_proxy=$no_proxy"} ${NO_PROXY:+"NO_PROXY=$NO_PROXY"} GOTOOLCHAIN=local GOPATH="$B/gopath" GOMODCACHE="$B/gopath/pkg/mod" GOCACHE="$B/gocache" "$B/go/bin/go" mod download) >"$B/gomod-download.log" 2>&1; echo "exit=$?"
```

Expect `exit=0` (the log is then empty). Failures follow the same rule as the nfpm command above:
a network error gets one more run, anything else or a second one is a *stop* (report `tail -n 40`
of `$B/gomod-download.log`).

## 5. Build the `.deb` (no root, 5–15 minutes)

Run the build **with a clean environment**: `HOME` is a throwaway directory in `$B` and the
session-bus address points nowhere, so the tests can reach neither your `~/.config` nor
`/run/user/$UID/bus` nor the display; Go's own settings file and telemetry stay in `$B` too.
Proxy settings are passed through; nothing else from the session is. After step 4 it needs no
network. This is the build that the build container verifies. Run it in the background (§0):

```bash
B=~/.cache/gswitch-auto-build; R=~/p/gswitch-auto; mkdir -p "$B/home" && (cd "$R" && env -i HOME="$B/home" USER="$USER" LOGNAME="$LOGNAME" LANG=C.UTF-8 TERM=dumb PATH="$B/go/bin:$B/bin:/usr/local/bin:/usr/bin:/bin" DBUS_SESSION_BUS_ADDRESS=unix:path=/nonexistent/gswitch-build-bus ${https_proxy:+"https_proxy=$https_proxy"} ${HTTPS_PROXY:+"HTTPS_PROXY=$HTTPS_PROXY"} ${no_proxy:+"no_proxy=$no_proxy"} ${NO_PROXY:+"NO_PROXY=$NO_PROXY"} GOTOOLCHAIN=local GOFLAGS=-buildvcs=false GOPATH="$B/gopath" GOMODCACHE="$B/gopath/pkg/mod" GOCACHE="$B/gocache" NFPM="$B/bin/nfpm" scripts/build-deb.sh --no-container >"$B/build-deb.log" 2>&1); echo "exit=$?"
```

Expect `exit=0`. Then:

```bash
B=~/.cache/gswitch-auto-build; R=~/p/gswitch-auto; export LC_ALL=C.UTF-8 LANGUAGE=; grep -c -e '--- FAIL' -e '^FAIL' "$B/build-deb.log"; grep -E '^(E|W): ' "$B/build-deb.log" | sed -E 's/ \[.*//' | sort | uniq -c; tail -n 5 "$B/build-deb.log"; (cd "$R/dist" && sha256sum -c checksums-deb.txt); deb="$R/dist/$(awk '{print $2}' "$R/dist/checksums-deb.txt")"; dpkg-deb -f "$deb" Package Version Depends Recommends Provides Replaces Conflicts
```

Expect:

- `0` (no failed test);
- no `E:` line; the lintian warnings are expected and not fatal: `hardening-no-pie` (both
  binaries), `maintainer-script-calls-systemctl`, `maintainer-script-ignores-errors` (upstream
  scriptlets), `no-manual-page` (2, 11, 4 and 2 in testing; the counts may differ);
- the log ends with `ok    lintian reports no errors (warnings and info tags above are advisory)`,
  `==> Done`, `wrote …/dist/gswitch-auto_<V>-1_amd64.deb and …/dist/checksums-deb.txt` and the
  sha256 line;
- `gswitch-auto_<V>-1_amd64.deb: OK` (the locale variables matter: under a Russian locale
  `sha256sum` prints `ЦЕЛ`);
- `Package: gswitch-auto`, `Version: <V>-1`, `Provides: gswitch (= <V>-1)`, `Replaces: gswitch`,
  `Conflicts: gswitch`.

`dist/` can hold `.deb` files of earlier builds; `checksums-deb.txt` names only the newest, which
is why the commands here and in step 6 take the file name from it.

On failure: if it failed inside `go test ./...` (`--- FAIL` lines), run the identical step-5
command once more (some tests are timing-sensitive under load); a second failure is a *stop*.
A failed module download (`go: downloading` followed by `dial tcp`, `TLS handshake timeout` or
`i/o timeout`; usually under `==> Go licence notices`) means step 4's module download was skipped
or incomplete: run step 4's `go mod download` command again, then the identical step-5 command
once more; a second network failure is a *stop*. Any other failure is a *stop* at once. Report
the failing step (`B=~/.cache/gswitch-auto-build; grep -n -E 'FAIL|error|die' "$B/build-deb.log" | head`)
and the last 80 lines of the log. Do not retry with `--skip-tests` unless the user agrees, and never
split the build into your own `go`/`make` calls (§0).

Alternatives, only if the user prefers them: (a) the user copies a `.deb` built on the Fedora host
(`make deb`) together with its `checksums-deb.txt` into `~/p/gswitch-auto/dist/`
(`mkdir -p` it first) — verify with `R=~/p/gswitch-auto; (cd "$R/dist" && LC_ALL=C.UTF-8 LANGUAGE= sha256sum -c checksums-deb.txt)` ⇒ `OK`
and continue at step 6; (b) the container build: the user runs `sudo apt install podman`, then
`scripts/build-deb.sh` without `--no-container` from the repository root (not tested with
Debian 12's podman 4.3).

## 6. Install (user runs, root)

First, read the version situation and what apt will do (a simulation, no root):

```bash
R=~/p/gswitch-auto; deb="$R/dist/$(awk '{print $2}' "$R/dist/checksums-deb.txt")"; new=$(dpkg-deb -f "$deb" Version); old=$(dpkg-query -W -f='${db:Status-Status} ${Version}\n' gswitch-auto 2>/dev/null | awk '$1 == "installed" {print $2}'); echo "deb=$deb"; echo "new=$new installed=${old:-none}"; if [ -n "$old" ]; then if dpkg --compare-versions "$new" gt "$old"; then echo upgrade; elif dpkg --compare-versions "$new" eq "$old"; then echo same; else echo DOWNGRADE; fi; fi; LC_ALL=C.UTF-8 LANGUAGE= apt-get -s install "$deb" 2>&1 | grep -E '^(Inst|Remv)'
```

Record the `Inst`/`Remv` lines: they are the packages step 6 adds or removes (for the report and
rollback). Then ask the user to run, with the absolute path printed as `deb=`:

```bash
sudo apt install /ABSOLUTE/PATH/TO/dist/gswitch-auto_<V>-1_amd64.deb
```

- `installed=none` or `upgrade`: the command above.
- `same`: the same version is already installed; a plain install does nothing, so the user runs
  `sudo apt install --reinstall /ABSOLUTE/PATH/…deb`.
- `DOWNGRADE`: an installed build is newer (for example a Fedora `.dirty` build). Tell the user
  and continue only with their consent; apt then asks to confirm the downgrade. (A native build of
  a later commit of `main` always sorts above an earlier dev build.)

(`apt` needs `./` or an absolute path, otherwise it searches the archive.) Expected: apt installs
the Depends and Recommends (`gnome-shell-extension-appindicator`, `wl-clipboard`, …); if upstream
`gswitch` was installed, apt removes it and the output contains
`gswitch-auto: enabled gswitch.service for <user> (the removal of the gswitch package had disabled it)`.
A notice `Download is performed unsandboxed as root` is harmless.

## 7. Daemon checks (agent)

```bash
export LC_ALL=C.UTF-8 LANGUAGE=
echo "== package"; dpkg-query -W -f='${Status} ${Version}\n' gswitch-auto; dpkg-query -W -f='${Status}\n' gswitch 2>&1
echo "== binary"; command -v gswitch; gswitch --version
echo "== service"; systemctl --user is-enabled gswitch.service; systemctl --user is-active gswitch.service; systemctl --user show -p FragmentPath -p DropInPaths gswitch.service
echo "== start log"; journalctl --user -u gswitch.service -b --no-pager | grep -E 'layout sources:|XF86Launch7|X11 layout table|Warning|rror'
echo "== switch keys"; gsettings get org.gnome.desktop.wm.keybindings switch-input-source
echo "== layout state"; gswitch --layout-state --watch 0
echo "== focus state"; gswitch --focus-state --watch 0
echo "== dbus"; for p in LayoutSource AutoEnabled AutoAvailable AutoUnavailableReason FocusSource; do printf '%s => ' "$p"; busctl --user get-property io.github.vabuter.GswitchAuto /io/github/vabuter/GswitchAuto io.github.vabuter.GswitchAuto "$p"; done
```

| Section | Expect |
|---|---|
| package | `install ok installed <V>-1`; then `unknown ok not-installed`, `deinstall ok config-files` or `dpkg-query: no packages found matching gswitch` |
| binary | `/usr/bin/gswitch`; `gswitch version <V>` |
| service | `enabled`, `active`; `FragmentPath=/usr/lib/systemd/user/gswitch.service`; `DropInPaths=` (empty) |
| start log | `layout sources: x11 (GNOME Shell 43.9); focus sources: extension, x11; strict purpose: true`. On the daemon's first start on this machine also `Added XF86Launch7 to GNOME layout-switch bindings for stable X11 input` (absent if upstream gswitch had added it already) and `Added <Shift>XF86Launch7 to GNOME layout-switch bindings for automatic correction`; after an upgrade or a restart the bindings exist already, the `Added` lines are absent and the switch keys row is the evidence. Nothing else: a `Warning: …`, `X11 layout table … differs from the configured pair …` or error line is a finding |
| switch keys | the baseline plus `'XF86Launch7'` and `'<Shift>XF86Launch7'` (SPEC-X11 F9, F21) |
| layout state | `Session: x11 (…)`, `GNOME Shell: 43.9`, `Daemon selection: layout sources: x11 (GNOME Shell 43.9); …`, `Layout table: us,ru,…` (GNOME 43 appends the locale's layout: `us,ru,us` or `us,ru,ru`), `Mapped: layout us = index 0 …` (or `ru = index 1` in the ru layout) |
| focus state | `Extension: absent (org.gnome.Shell.Extensions.GswitchFocus not owned)`; `X11: connected, active window 0x…, WM_CLASS "<this terminal's class>" …`; `Policy: denied (focus-unavailable)` |
| dbus | `LayoutSource => s "x11"`, `AutoEnabled => b false` (off by default; `b true` if an earlier fork install left auto mode on), `AutoAvailable => b false`, `AutoUnavailableReason => s "focus-extension-required"` (strict mode until the extension runs, step 8), `FocusSource => s "x11"` |

If the service is `disabled` and upstream gswitch was **not** installed before (fresh install —
the package enables the unit only when it replaces upstream), enable it (user-level, no root):
`systemctl --user enable --now gswitch.service`, then re-check. If the baseline (step 1) was
`disabled` and the unit is now `enabled`, the replacement heuristic enabled it: tell the user and
ask whether to keep it (if not: `systemctl --user disable --now gswitch.service`). If it is
`failed`: stop, report `systemctl --user status gswitch.service --no-pager -n 50`. If the start
log is empty, the same `status` command shows the latest log lines.

If the log shows `Warning: cannot add <Shift>XF86Launch7 …`, automatic corrections still work but
wait 1.5 s for GNOME's popup (up to 2.5 s until confirmed) and drop the keys typed meanwhile:
report it.

## 8. Focus extension and tray (user action, then agent)

1. Ask the user: press **Alt+F2**, type **`r`**, press **Enter**. On Xorg this restarts GNOME Shell
   in place; windows, this terminal and the session survive. GNOME 43 scans extension directories
   only at Shell start, so the extension (and the appindicator extension installed in step 6) is
   invisible before this. If the user has to log out instead, this agent session ends: first tell
   them to resume afterwards with `cd ~/p/gswitch-auto && claude --continue` (the log has the step
   markers).
2. `LC_ALL=C.UTF-8 LANGUAGE= gnome-extensions info gswitch-focus@vabuter.github.io` (the two
   variables keep the labels English under a Russian locale, §0). GNOME 43/44 print
   `State: ENABLED` for a running extension (GNOME 45+ print `ACTIVE`). If the state is
   `INITIALIZED` or `DISABLED`, run `gnome-extensions enable gswitch-focus@vabuter.github.io` and
   check again ⇒ `State: ENABLED`. `Extension “gswitch-focus@vabuter.github.io” doesn't exist`
   (exit code 2) ⇒ the Shell was not restarted yet. `ERROR` or `OUT OF DATE` ⇒ stop and report
   `journalctl -b --no-pager /usr/bin/gnome-shell | grep -i -A5 gswitch`. Exit code 2 with no
   output at all has two causes; re-run the `== shell` line of step 1: if its `gdbus` call prints
   an error, this shell cannot reach the session bus (stop as in step 1); if `command -v gjs`
   prints nothing, `gjs` is missing — ask the user to run `sudo apt install gjs`, then check
   again.
   Confirm the legacy variant: `grep -c 'function init' /usr/share/gnome-shell/extensions/gswitch-focus@vabuter.github.io/extension.js` ⇒ `1`.
3. Tray icon: on Debian 12 the `gnome-shell-extension-appindicator` package (a Recommends of
   step 6) has the UUID `ubuntu-appindicators@ubuntu.com`, not the extensions.gnome.org/Fedora
   UUID `appindicatorsupport@rgcjonas.gmail.com` (`gnome-extensions list | grep -i appindicator`
   finds either). Run
   `LC_ALL=C.UTF-8 LANGUAGE= gnome-extensions info ubuntu-appindicators@ubuntu.com`. If it is not
   `State: ENABLED`, ask the user — it shows the tray icons of all applications, not only
   gswitch's — and on yes run `gnome-extensions enable ubuntu-appindicators@ubuntu.com` and record
   it for rollback. Without it the tray runs without an icon (its log says
   `The name org.kde.StatusNotifierWatcher was not provided by any .service files`).
4. Tray: if `pgrep -x gswitch-tray` finds a running (old) tray, `pkill -x gswitch-tray`; then
   `gio launch /usr/share/applications/gswitch-tray.desktop`. After a few seconds
   `pgrep -x gswitch-tray` finds it, and `ls ~/.config/gswitch/` lists `auto.conf`,
   `exceptions-words.txt`, `exceptions-apps.txt` (the tray seeds them; the apps list includes
   terminals, `xterm`, `keepassxc`, `pinentry-qt`, …). From the next login on, it autostarts.
5. Re-run the `== focus state` and `== dbus` lines of step 7. Expect
   `FocusSource => s "active"`, `AutoUnavailableReason => s ""`, `AutoAvailable => b true`, and
   for gnome-terminal
   `Extension: owned (GetFocus: app "org.gnome.Terminal.desktop", wm_class "Gnome-terminal", purpose terminal)`,
   `Merged: app source: extension, …`, `Policy: denied (terminal)`. Purpose `unknown`
   (`Policy: denied (purpose-unknown)`) right after enabling the extension from this terminal is
   normal: ask the user to click another window and back, then re-run.

## 9. Typing checks (the user types, you observe)

You never type or inject keys. Ask the user to do each step, then read the D-Bus properties
(`AutoEnabled`, `LastCorrection`, `Stats`, `FocusSuppressedReason`, `AutoUnavailableReason`) and
compare; `busctl` prints them with a type prefix (`b true`, `s "terminal"`). A property that
depends on the focused window is read with `sleep 5; busctl …` started here, while the user
focuses the window under test within 5 s. Use gedit (validated; `sudo apt install gedit` if
missing) or Debian's default gnome-text-editor (GTK 4: fine if `--focus-state` shows its purpose
as `normal`).

Before the checks: `grep -c -x -F -e ghbdtn -e ntcn ~/.config/gswitch/exceptions-words.txt` ⇒ `0`.
Otherwise stop: an exception word is never corrected, so the checks below would pass without
testing anything.

1. **Toggle.** LShift+RShift ⇒ `AutoEnabled` ⇒ `b true`; the tray shows auto on.
2. **Corrections** in the editor (layout us): `ghbdtn ` ⇒ `привет ` and the indicator shows ru;
   `руддщ ` (layout ru) ⇒ `hello `; `hello ` unchanged; `ghbdtn` + Enter unchanged (Space is the
   only trigger). `LastCorrection` and `Stats` change accordingly. Do **not** undo here (check 7).
3. **Speed, no lost keys** (MX6 (c), SPEC-X11 F21): type `ghbdtn ` and, as soon as it turns into
   `привет `, continue `rfr ltkf ` at normal speed ⇒ `привет как дела ` with no letter missing;
   the input-source popup never appears; the fix appears with no noticeable (~1.5 s) delay.
4. **Terminal.** In gnome-terminal, `ghbdtn ` ⇒ unchanged, **and** `FocusSuppressedReason` read
   inside it ⇒ `s "terminal"` or `s "excluded-app"` (required: an unchanged text alone is no pass).
5. **Password field** (MX9 (a)): the user runs `zenity --password | od -c`, types `ghbdtn `, waits
   ~1 s, presses Enter ⇒ `od` prints `g   h   b   d   t   n` and a space (unchanged), the
   indicator stays en; **and** `sleep 5; busctl … FocusSuppressedReason` with the zenity entry
   focused ⇒ `s "purpose-unknown"` (or `s "password-field"`). `s ""` is a FAIL even when `od`
   shows the unchanged bytes.
6. **Manual mode** (MX10): auto off (LShift+RShift ⇒ `b false`), `ghbdtn` + double Shift ⇒
   `привет`; LShift+RAlt+RCtrl on a selected wrong-layout word converts it.
7. **Undo** (last: it changes the user's word list). Auto on again (LShift+RShift ⇒ `b true`),
   type `ntcn ` ⇒ `тест `, then double Shift right away ⇒ `ntcn ` and the layout back. With the
   defaults (learn on undo, count 1) this adds `ntcn` to the exception words: a few seconds later
   `grep -x -F ntcn ~/.config/gswitch/exceptions-words.txt` ⇒ `ntcn`. Record it and offer to
   remove it (tray → settings → «Автоисправление» → «Слова-исключения» → `ntcn` → «Удалить»,
   «Применить»).

Then offer the rest of SPEC-X11 §6.3 (MX1–MX13), one item at a time; the user performs every
action there, you read and record. Rules for that script:

- *(debug)* steps only with the user's consent: the user runs
  `systemctl --user stop gswitch.service` and `gswitch --debug …` in their own terminal (never in
  your tool: a timeout would kill it with the service stopped), then
  `systemctl --user start gswitch.service`; you check `is-active` ⇒ `active` afterwards.
- MX4's `ibus restart` and MX6 (a)'s `xdotool` (`sudo apt install xdotool`; record the package for
  rollback) are run by the user in their own terminal; skip them if the user declines.
- MX3 (`layout-source=ibus`), MX7, MX8 and MX9 (c) change `auto.conf`, strict mode or the
  extension; strict off leaves GTK3 password fields unprotected. Record the values before each and
  verify after each that they are back:
  `grep -E '^(auto-strict-purpose|layout-source)=' ~/.config/gswitch/auto.conf`,
  `LC_ALL=C.UTF-8 LANGUAGE= gnome-extensions info gswitch-focus@vabuter.github.io | grep State` ⇒
  `State: ENABLED`, `AutoUnavailableReason` ⇒ `s ""`. After an interruption, restore these first.
- MX6 (d) undoes a fix, which teaches its word: check `exceptions-words.txt` afterwards as in
  check 7.
- MX11 writes `~/mx11.txt`: `rm -f ~/mx11.txt` afterwards.
- MX12 reorders `sources` and needs a log out/in, which ends this agent session: tell the user how
  to resume first (`cd ~/p/gswitch-auto && claude --continue`) and to restore the original order
  afterwards. Its `/etc/gswitch/default.conf` part (`layout1`/`layout2`) only if the user
  explicitly wants it.
- MX13 removes the package: only on the user's request (step 11).

## 10. Report (in Russian)

Write the report to `~/.cache/gswitch-auto-build/report.md` and show it to the user:

- version `<V>`, commit (`R=~/p/gswitch-auto; git -C "$R" rev-parse --short HEAD`), sha256 of the
  `.deb`;
- the baseline (step 1) and what changed: build deps added in step 2, the `Inst`/`Remv` lines of
  step 6, `switch-input-source` before/after, extensions enabled (and whether the user agreed to
  appindicator), whether upstream gswitch was replaced, whether the unit's enabled state changed;
- the final `AutoEnabled` (the tray saves it to `auto.conf`; ask whether to leave auto mode on)
  and the words this session added to `~/.config/gswitch/exceptions-words.txt`;
- results of steps 5, 7, 8 (with the log lines) and each typing check (pass / fail / skipped,
  with the observed text);
- warnings from the journal, anything that deviated from this runbook, and open questions.
  `JS ERROR … PropertyNotFound` lines from `ubuntu-appindicators@ubuntu.com/util.js` in the Shell
  log are known, harmless noise of the tray's StatusNotifier implementation; only errors that
  mention `gswitch` matter.

For a problem that needs a code change, also save the evidence (journal excerpt, `--layout-state`
and `--focus-state` output, the busctl values) so the user can bring it to the Fedora host.

## 11. Rollback (on request)

0. If the user wants to keep the report: `cp ~/.cache/gswitch-auto-build/report.md ~/gswitch-auto-report.md`.
1. User: `sudo apt remove gswitch-auto` (`purge` also drops `/etc/gswitch` and the conffiles
   `/etc/xdg/autostart/gswitch-{tray,daemon}.desktop`), then `pkill -x gswitch-tray` (the running
   tray stays until logout). If upstream gswitch was installed before and its `.deb` is on this
   machine, the user then installs it again (`sudo apt install /PATH/TO/gswitch_0.8.0_linux_amd64.deb`;
   apt cannot download it, it comes from upstream's GitHub releases) and runs
   `systemctl --user enable --now gswitch.service`.
2. Restore the switch keys to the baseline from step 1 (the user's consent first):
   `gsettings set org.gnome.desktop.wm.keybindings switch-input-source "<baseline>"`.
3. Disable exactly the extensions this run enabled and the baseline did not have:
   `gnome-extensions disable gswitch-focus@vabuter.github.io` (only if the package is gone and the
   user wants it off), `gnome-extensions disable ubuntu-appindicators@ubuntu.com` (only if step 8
   enabled it).
4. Build leftovers: `chmod -R u+w ~/.cache/gswitch-auto-build && rm -rf ~/.cache/gswitch-auto-build`
   (Go makes its module cache read-only, so a plain `rm -rf` fails; this also deletes
   `install-log.md` and `report.md`, see item 0). Packages: the user first runs
   `apt-get -s purge <packages step 2 added>` and `apt-get -s autoremove` and compares the `Remv`
   lines with the preflight record (step 1 "autoremove") and the `Inst` lines of step 6; only the
   packages this procedure added are removed (`sudo apt purge <those packages>`), never with a bare
   `sudo apt autoremove`.
5. `~/.config/gswitch/` stays (user data); delete it only on request. `~/.config/go` is not
   touched by this procedure.
