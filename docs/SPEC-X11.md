# gswitch-auto — SPEC-X11: Debian 12 / GNOME Shell 43.9 / X11 addendum

Status: implementation contract (addendum to `docs/SPEC.md`), date 2026-10-22. Base: fork HEAD
`7c0de1a` (`chore: ignore agent worktree directory`), i.e. upstream v0.8.0 + 65 fork commits.
Every `file:line` reference below is to that tree unless marked otherwise.

This addendum makes the same program (same repository, same Go module, same binaries) work on
the user's second machine. It changes nothing in SPEC.md's fixed decisions D1–D8 and corrections
C1–C29; where this document and SPEC.md differ for the **X11 target**, this document wins; for
the Wayland target SPEC.md stays normative except for the GNOME Shell version guard (§2.1), which
applies to both. Names (packages, types, config keys, D-Bus values, file names) are normative.

Conventions: as SPEC.md (English code/comments/commit messages, UI strings en+ru). "Run
goroutine" as in SPEC.md. "X11 session" = `XDG_SESSION_TYPE=x11` and `WAYLAND_DISPLAY` empty,
evaluated exactly like `shouldVerifyGNOMEX11LayoutSwitch` (`runtime/switcher.go:1005-1017`:
`SessionEnv` when non-nil, else the process environment).

---

## 0. Fixed decisions for this addendum (not re-litigated)

| # | Decision | Where it lands |
|---|---|---|
| X1 | Same repository and module (`github.com/arumata/gswitch`); platform sources selected at daemon start, never by build tags. | §2, §3.4 |
| X2 | IBus layout source only when `org.gnome.Shell` `ShellVersion` ≥ 49.3; version unreadable ⇒ no IBus source *yet* (a guarded source keeps probing, §3.1.1); `layout-source=ibus` is an explicit override with a warning. | §3.1.1, §3.4 |
| X3 | New `layoutstate` source for X11: XKB group via a dedicated xgb connection; engine names synthesized as `xkb:<layout>:<variant>:` so `Tracker.pairIndex` is unchanged; group index = order of `org.gnome.desktop.input-sources sources` (= the daemon's pair order); enabled in X11 sessions. **Architect's pick: polling `XkbGetState` every 50 ms, not `XkbSelectEvents`** — justification in §3.1.2. | §3.1.2 |
| X4 | New `focus` source for X11 (EWMH): `PropertyNotify` on the root for `_NET_ACTIVE_WINDOW`, `WM_CLASS` class part and `_GTK_APPLICATION_ID`; merged with the extension client; `fold.AppKey` for matching. | §3.2 |
| X5 | Legacy (non-ESM) extension variant for GNOME 43/44 with the same UUID and D-Bus interface under `shell-extension/legacy/<uuid>/`; the RPM ships the ESM tree, the Debian 12 `.deb` ships the legacy tree; `check-extension.sh` validates both. | §3.6 |
| X6 | Strict purpose mode: key **`auto-strict-purpose`** (`auto` \| `true` \| `false`, default `auto` = true in X11 sessions, false elsewhere); auto-correction fires only when the extension reports a known non-password text purpose; extension absent in strict mode ⇒ `AutoAvailable=false`, reason `focus-extension-required`, tray hint. | §3.3, §3.2.4, §3.5 |
| X7 | `.deb` named `gswitch-auto` built in `docker.io/library/golang:1.26-bookworm` (`packaging/debian12.Containerfile`, `scripts/build-deb.sh`, `make deb`, podman); Depends/Recommends/Provides/Replaces/Conflicts as §3.8; copyright file; `version.sh` dpkg comparison; CI deb + install-test jobs; the dpkg "prerm remove in-favour" limitation documented and mitigated. | §3.8 |
| X8 | `gswitch --layout-state` shows the X11 XKB source; new `gswitch --focus-state` shows the merged focus state. | §3.7 |
| X9 | `exceptions-apps.txt` template gains Debian/X11 `WM_CLASS` variants. | §3.9 |
| X10 | Unit tests with fakes; X11 integration tests gated by `GSWITCH_X11_TEST=1` (read-only on this host's Xwayland; with a switch test inside a container with Xvfb). | §6 |

Not in scope: pointer tracking, keyboard grab, KDE/awesome support beyond "compiles and stays
safe" (SPEC §1.2), publishing to Debian repositories, i386/arm64 packages.

---

## 1. Target facts

Legend: **V** = verified in this round (source and location given); **R** = relayed from the
prior assessment (the user's or the assessment's observation; file not re-fetched); **H** =
observed on the Fedora host during this round.

| # | Fact | How known |
|---|---|---|
| F1 | Target: Debian 12 (bookworm), GNOME Shell 43.9, X11 session (`XDG_SESSION_TYPE=x11`), IBus 1.5.27, `GTK_IM_MODULE=ibus` (`im-config -m` ⇒ `ibus`), `input-sources sources = [('xkb','us'),('xkb','ru')]`, `switch-input-source = ['<Super>space','XF86Keyboard']`. | R (user's commands) |
| F2 | `ibus engine` prints `xkb:us::eng` in **both** layouts there. Cause: GNOME Shell ≤ 49.2 `js/ui/status/keyboard.js` `InputSourceManager.activateInputSource` sets `engine = 'xkb:us::eng'` for every XKB source ("All the "xkb:..." IBus engines simply "echo" back symbols […] so we always set one"), 43.9 lines ≈471-486. | V (gitlab.gnome.org/GNOME/gnome-shell, raw 43.9) |
| F3 | GNOME Shell 49.3 builds the engine from the source: `` engine = `xkb:${name}:${variant}:${lang}` `` with `[name, variant] = is.id.split('+')` and `lang` from `XkbInfo.get_languages_for_layout` (≈ lines 547-556). Hence the ≥ 49.3 threshold of X2; the Fedora host (50.6) reports `xkb:us::eng`/`xkb:ru::rus`. | V (raw 49.3), H |
| F4 | XKB groups on GNOME X11 follow the `sources` order: `_updateMruSources` calls `this._keyboardManager.setUserLayouts(sourcesList.map(x => x.xkbId))` where `sourcesList` is built from `this._inputSources` in `sources` order (43.9 ≈ line 498); `KeyboardManager.apply` locks the group index inside that list. | V (raw 43.9) |
| F5 | GNOME 43.9 extension system is legacy GJS: `extensionModule = extension.imports.extension`, `extensionModule.init(extension)` returns the state object, `stateObj.enable()/disable()`; extension directories are scanned once (`_loadExtensions` only when `!this._initialized`); unknown UUIDs are skipped by `_callExtensionEnable`; `ReloadExtension` returns `NOT_SUPPORTED` ("ReloadExtension is deprecated and does not work"); session-mode mismatch (lock screen) disables user-mode extensions via `_onEnabledExtensionsChanged`. ⇒ relogin (or Alt+F2 `r` on X11) after install, `Disabling("locked")` semantics unchanged. | V (raw 43.9 `extensionSystem.js`, `shellDBus.js`) |
| F6 | GNOME 43.9 `js/misc/ibusManager.js`: `function getIBusManager()` (legacy export), `IBusManager` emits `'focus-in'` (except for the `/InputContext_1` path), `'focus-out'`, `'set-content-type'(purpose, hints)` (IBus ≥ 1.5.10), `'ready'`; `js/ui/main.js` has module-level `inputMethod` (`new InputMethod.InputMethod()`) and `sessionMode`; `js/misc/signalTracker.js` exports standalone `connectObject/disconnectObject` (legacy). | V (raw 43.9) |
| F7 | `org.gnome.Shell` on `/org/gnome/Shell` has a read-only property `ShellVersion` returning `Config.PACKAGE_VERSION` (both the `GnomeShell` and the `GnomeShellExtensions` objects). On the host: `busctl --user get-property org.gnome.Shell /org/gnome/Shell org.gnome.Shell ShellVersion` ⇒ `s "50.6"`. | V (raw 43.9 `shellDBus.js`), H |
| F8 | ibus 1.5.27 `client/gtk2/ibusimcontext.c` `ibus_im_context_focus_in`: "don't set focus on password entry" — returns before `focus_in` when the client widget is a `GtkEntry` with `visibility == FALSE`; otherwise `_set_content_type` forwards GTK `input-purpose`/`input-hints` via `ibus_input_context_set_content_type`. ⇒ On X11 a GTK password entry never produces `focus-in`/`set-content-type`; the previous context's `focus-out` leaves purpose `none`. | V (raw ibus 1.5.27) |
| F9 | Upstream GNOME-X11 path: the gnome detection provider prefers the internal single-key accelerator `XF86Launch7` and `ensureGNOMEX11Launch7Binding` appends it to `switch-input-source` persistently (`detect/gnome_x11_binding.go:19-75`, called from `runtime/switcher.go:233-247`); scancode `KEY_F16` = 186; the manual path confirms the switch by polling `XkbGetState` (`runtime/x11_layout_switch.go:19-20,119`, raw XKB requests, 10 ms × 200). | V (tree), R (186) |
| F10 | gnome-session 43 exports `DISPLAY`/`XAUTHORITY` to `systemd --user`; `setupX11Environment()` (`runtime/x11.go:16-35`) fills them when missing. The user unit allows `/tmp/.X11-unix` (`configs/gswitch.service:26-29`: `ReadWritePaths=/tmp`, `PrivateTmp=false`; `RestrictAddressFamilies=AF_UNIX AF_NETLINK`). | R, V (tree) |
| F11 | jezek/xgb v1.3.1 has no XKB sub-package; unknown extension events are dropped with a log line unless a constructor is registered in the **process-global, unlocked** map `xgb.NewEventFuncs` (`xgb.go:197`, read in every connection's reader goroutine at `xgb.go:557`); the extension event base is only known after `QueryExtension`. | V (module cache) |
| F12 | `golang.design/x/clipboard` (vendored in `third_party/clipboard`) talks to X11 through `dlopen("libX11.so.6")` (`clipboard_linux.c:41-50`); no `xclip`/`xsel` needed. Built daemon links only `libc`/`libresolv`; the tray links GTK3, GDK, GLib/GIO/GObject, cairo, pango, gdk-pixbuf, atk, harfbuzz, fontconfig, libX11 (`readelf -d builds/*`). | V (tree, host) |
| F13 | nfpm v2.48.0 `Overridables` include `scripts` and `contents`; `contents[].packager: rpm|deb` filters entries; RPM-only content types (`license`, `doc`) are skipped for deb (`nfpm.go:359-375`, `files/files.go:52-63`). | V (module cache) |
| F14 | dpkg replacement order (Conflicts+Replaces+Provides): the upstream `gswitch` package's `prerm remove in-favour gswitch-auto <ver>` runs **before** our `preinst install`; upstream `preremove.sh` disables and stops the user unit on `remove` (`scripts/preremove.sh:17-40`). There is no `%pretrans` equivalent on deb. | R, V (tree) |
| F15 | Debian 12 ships Go 1.19 (backports 1.23); `go.mod` needs `go 1.25.5` ⇒ build in `docker.io/library/golang:1.26-bookworm` (image present locally; `podman` 5.8.9; `debian:12` image present). Binaries built on Fedora need `GLIBC_2.34` at most (ok on glibc 2.36) but official builds go through the container. | R, H (`podman images`) |
| F16 | Host (Fedora 44, GNOME 50.6, Wayland): Xwayland on `DISPLAY=:0` with `_XKB_RULES_NAMES = "evdev","pc105","us","",""` and a live `_NET_ACTIVE_WINDOW`; `setxkbmap -query` warns "Running setxkbmap against an Xwayland server". ⇒ read-only X11 smoke tests run here, but group **changes** cannot be observed here (Xwayland sees one layout). | H |
| F17 | Debian 12 packages: `libgtk-3-0`, `libx11-6`, `libglib2.0-bin` (gsettings), `policykit-1` (transitional: `polkitd` + `pkexec`), `x11-xkb-utils` (setxkbmap), `gnome-shell-extension-appindicator`, `wl-clipboard`, `desktop-file-utils`, `libgtk-3-bin` (gtk-update-icon-cache). | R (assessment) |

---

## 2. Source selection matrix

Selection runs once in `initFork` (`runtime/fork.go:116`), after `initLayoutConverter` resolved
the pair (`runtime/switcher.go:289`). Inputs: `s.sessionEnv` (nil for a user service ⇒ process
environment), `userconfig.Settings`, `forkGeteuid()`, and the GNOME Shell version probe
(`layoutstate.ProbeShellVersion`, 2 s). The result is logged once as one line
`layout sources: … ; focus sources: … ; strict purpose: …` and stored in `s.platform`
(§3.4).

### 2.1 Layout tracking

"GNOME" = `detect.IsGNOMESession(env)`. "≥49.3" = probe ok and `IBusEngineTracksLayout`.
"guarded" = `GuardedIBusSource` (§3.1.1): the IBus source starts only after the probe succeeds
with ≥ 49.3; while the probe keeps failing the tracker stays Unknown.

| Session | Shell version | `layout-source` | Sources added to the tracker | Resync | Auto confirm path | Manual path (unchanged upstream) | `AutoUnavailableReason` while idle |
|---|---|---|---|---|---|---|---|
| Wayland GNOME | ≥ 49.3 | `auto` | `ibus` | gsettings | tracker reader (`emitOptions.confirm`) | composite reader → GNOME Wayland verify | `""` once Confirmed, else `layout-unknown` |
| Wayland GNOME | < 49.3 (probe ok) | `auto` | none ⇒ `tracker == nil` | — | — | upstream gsettings verify | `layout-source-unsupported` |
| Wayland GNOME | probe failed | `auto` | `ibus` **guarded** | gsettings | tracker reader | composite reader | `layout-unknown` until the probe passes (then as row 1 or, if < 49.3, the guard logs and the tracker stays Unknown ⇒ `layout-unknown`) |
| Wayland GNOME | any | `ibus` | `ibus` unguarded + warning `layout-source=ibus forces the IBus source; GNOME Shell %s reports a fixed engine below 49.3` | gsettings | tracker reader | composite reader | as row 1 |
| X11 GNOME (Debian 12) | any | `auto` | `x11` (never `ibus`, see note) | gsettings | tracker reader | **upstream X11 verify** (`verifyGNOMEX11LayoutSwitch`, raw XKB reader) | `""` once Confirmed, else `layout-unknown` |
| X11 GNOME | any | `x11` | `x11` | gsettings | tracker reader | upstream X11 verify | as above |
| X11 GNOME | any | `ibus` | `ibus` unguarded + warning (useless below 49.3: always index 0) | gsettings | tracker reader | upstream X11 verify | `""`/`layout-unknown` |
| X11 non-GNOME (KDE/awesome/other) | n/a | `auto` or `x11` | `x11` | none | tracker reader | upstream blind replay | `""`/`layout-unknown`; best effort, untested |
| Wayland non-GNOME | n/a | `auto` | none | — | — | upstream | `layout-source-unsupported` |
| any | any | `none` | none | — | — | upstream | `layout-source-off` |
| root compatibility mode | any | any | none | — | — | upstream | `root-mode` |

Notes:
- Two push sources are never run together: `Tracker.setLocked` (`layoutstate/tracker.go:430-441`)
  treats a different `Source` as a change, so alternating `ibus`/`x11` reports would bump the
  generation and cancel words. On X11 the XKB group is authoritative whatever the IBus engine
  says.
- `layout-source=x11` on Wayland is accepted by the parser but refused at selection with warning
  `layout-source=x11 needs an X11 session` ⇒ `layout-source-unsupported` (Xwayland never sees
  GNOME's group changes, F16).
- The auto path keeps C26: `autoEmit` (`runtime/auto_control.go:141-159`) confirms through
  `trackerLayoutStateReader` on every platform. On X11 the tracker lags the group change by at
  most one poll (≤ 50 ms) and the confirm loop polls the tracker every 50 ms × 20
  (`gnome_wayland_layout_switch.go:10-14`), so a confirmed switch costs ≤ ~100 ms more than
  upstream's 10 ms X11 loop. Acceptable (K-X1).
- `noteOwnSwitch` after a tracker-confirmed auto switch sees the new generation (unchanged
  semantics). After a **manual** conversion on X11 (upstream reader, not the tracker) the
  tracker's poll may bump the generation after `noteOwnSwitch` ran ⇒ classified external ⇒
  `auto.OnLayoutChange(external)`: harmless, the manual path already called `Cancel("manual")`
  (same caveat as SPEC §3.10.3 for a late IBus signal).
- `observeLayoutSwitchChord` hints never fire on the Debian machine (LSKeys = `[186]`, a key no
  one presses), so `layout-hint-timeout-ms` is irrelevant there; the X11 source's `Query` still
  serves `Resync`.

### 2.2 Focus information

"ext" = the extension client (`focus.Client`), "x11" = `focus.X11Source` (EWMH), merged by
`focus.Merged` (§3.2.2). "strict" = effective `auto-strict-purpose` (§3.3).

| Session | `focus-source` | Extension owned | strict (effective) | Sources started | IPC `FocusSource` | Behaviour |
|---|---|---|---|---|---|---|
| Wayland GNOME | `auto` | yes | `false` (default) | ext | `active` | SPEC.md behaviour, unchanged |
| Wayland GNOME | `auto` | no | `false` | ext (absent) | `absent` | fail-open unless `auto-require-focus` (unchanged) |
| Wayland GNOME | `auto` | yes | `true` (explicit) | ext | `active` | fires only with a known text purpose (§3.2.4); `none`/`unknown` ⇒ `purpose-unknown` |
| Wayland GNOME | `auto` | no | `true` | ext | `absent` | `AutoAvailable=false`, `focus-extension-required`; engine denies `focus-unavailable` |
| X11 GNOME (Debian 12) | `auto` | yes | `true` (default) | ext + x11 | `active` | app identity and purpose from ext; fires only with a known text purpose; x11 identity is a fallback while ext is absent |
| X11 GNOME | `auto` | no (not enabled / relogin pending / disabled) | `true` | ext (absent) + x11 | `x11` | `AutoAvailable=false`, reason `focus-extension-required`; tray hint; exclusions still evaluated from x11 identity for diagnostics |
| X11 GNOME | `auto` | no | `false` (explicit) | ext (absent) + x11 | `x11` | fail-open; exclusions by `WM_CLASS`/`_GTK_APPLICATION_ID`; password/terminal purposes unknown ⇒ only the exclusion list protects (documented, §7 K-X5) |
| X11 non-GNOME | `auto` | n/a | `true` (default) | x11 | `x11` | `focus-extension-required` (no purpose source); the user must set `auto-strict-purpose=false` to use auto there |
| any | `off` | — | `true` | none | `off` | `AutoAvailable=false`, reason `focus-source-off` |
| any | `off` | — | `false` | none | `off` | unchanged: no focus information, everything allowed |
| root compatibility mode | any | — | — | none | `off` | unchanged (`root-mode`) |

### 2.3 `AutoUnavailableReason` codes

Existing: `""`, `degraded`, `no-layout-converter`, `unsupported-layouts`, `layout-source-off`,
`layout-unknown`, `root-mode`, `detector-error` (`runtime/auto_control.go:26-34`). New:

| Code | Set when | Producer |
|---|---|---|
| `layout-source-unsupported` | no layout source exists for this session (Wayland GNOME < 49.3 with `auto`; Wayland non-GNOME; `layout-source=x11` on Wayland) | `buildAutoEngine` (`tracker == nil` and `s.platform.LayoutReason == …`) |
| `focus-source-off` | strict purpose effective and `focus-source=off` | `autoAvailability` |
| `focus-extension-required` | strict purpose effective, `focus-source=auto`, and the extension is not owned (`!fs.Available`) while not `Locked` | `autoAvailability` |

Precedence in `autoAvailability(ls, fs)` (replaces `runtime/auto_control.go:417-427`): engine nil
⇒ `s.autoReason`; tracker `Unknown`/`Settings` ⇒ `layout-unknown`; strict && `focus-source=off`
⇒ `focus-source-off`; strict && `!fs.Available && !fs.Locked` ⇒ `focus-extension-required`;
otherwise available (`Pending` and `Locked` count as available, as today — a lock shows up as
`FocusSuppressedReason=locked`).

`Policy.Allow` denial reasons gain `purpose-unknown` (§3.2.4). `FocusSuppressedReason` keeps
reporting the policy reason independently of `AutoAvailable`.

### 2.4 IPC value vocabularies (no interface change)

`io.github.vabuter.GswitchAuto` keeps its members (SPEC §3.9.1). Value sets grow:
`LayoutSource`: `ibus` | `gsettings` | **`x11`** | `""`; `FocusSource`: `active` | **`x11`** |
`absent` | `off`; `AutoUnavailableReason`: §2.3; `FocusSuppressedReason`: + `purpose-unknown`.
`ConfigWarnings` carries the selection warnings of §2.1/§2.2. `introspect.go` comments are
updated by WPX7; the XML is unchanged.

---

## 3. Package designs

### 3.1 `internal/gswitch/layoutstate` additions

#### 3.1.1 GNOME Shell version guard — `gnome_version.go` (WPX1)

```go
// ShellInfo is the result of one org.gnome.Shell ShellVersion probe.
type ShellInfo struct {
	Version string // e.g. "43.9", "50.6", "49.rc"; "" when Err != nil
	Err     error  // nil, ErrShellUnavailable (name not owned / no session bus), or a D-Bus error
}
var ErrShellUnavailable = errors.New("org.gnome.Shell is not available on the session bus")

// ProbeShellVersion reads org.freedesktop.DBus.Properties.Get("org.gnome.Shell","ShellVersion")
// on /org/gnome/Shell over a PRIVATE session connection (connectSessionBus of ibus_source.go,
// C27; FlagNoAutoStart) with ctx bounding the call (callers use 2 s). It never returns a
// Version with a nil Err that failed to parse: an unparsable string is Err = ErrShellVersionFormat.
func ProbeShellVersion(ctx context.Context) ShellInfo

// ParseShellVersion accepts "<major>[.<minor>[.<micro>]]" where a non-numeric minor
// ("alpha", "beta", "rc", "rc.1") counts as 0: "43.9"→43,9; "49.3"→49,3; "50.6"→50,6;
// "49.rc"→49,0; "48.alpha"→48,0; ""/"x" ⇒ ok=false.
func ParseShellVersion(s string) (major, minor int, ok bool)

// IBusEngineTracksLayout reports whether GNOME Shell of that version sets the IBus global
// engine per XKB source (49.3+, F3) rather than the fixed "xkb:us::eng" (F2).
func IBusEngineTracksLayout(version string) bool

// IBusSourceAllowed combines the auto.conf value with the probe (used only after
// IBusSourceEnabled returned true): "ibus" ⇒ (true, warning when info is < 49.3 or unknown);
// "auto" ⇒ ok only when info.Err == nil && IBusEngineTracksLayout; defer is true when
// info.Err != nil (caller wraps the source in GuardedIBusSource). reason is one of
// "", "shell-version-unknown", "ibus-engine-static".
func IBusSourceAllowed(layoutSource string, info ShellInfo) (ok, defer_ bool, reason, warning string)

// GuardedIBusSource runs inner only after ProbeShellVersion succeeds with a tracking version.
// Run: probe (timeout 2 s) with backoff 1 s..30 s until ctx is done; refused (< 49.3) ⇒ log
// once "IBus layout source disabled: GNOME Shell %s reports a fixed engine for XKB sources
// (needs 49.3+)" and return without ever calling sink.Available(true); passed ⇒ inner.Run.
// Query delegates to inner only once passed, else returns ErrShellUnavailable wrapped.
// Name returns inner.Name() ("ibus"). State() reports pending|passed|refused for --layout-state.
type GuardedIBusSource struct{ /* inner Source; probe func(ctx) ShellInfo; sleep func(ctx, d) bool; state atomic */ }
func NewGuardedIBusSource(inner Source, logf func(string, ...any)) *GuardedIBusSource
func (g *GuardedIBusSource) State() GuardState // GuardPending | GuardPassed | GuardRefused
```

Probe details: `Properties.Get` with `dbus.FlagNoAutoStart`; `org.freedesktop.DBus.Error.ServiceUnknown`
/ `NameHasNoOwner` and a missing session bus ⇒ `ErrShellUnavailable`; the reply is a variant
holding `s`. The probe is one call per attempt; nothing is subscribed. Why a guard instead of a
one-shot decision: the user unit starts `After=graphical-session.target`
(`configs/gswitch.service:9`) but gnome-shell may own its name a little later; a one-shot
"unavailable ⇒ no IBus source" would silently disable auto mode on the Fedora host until the
next restart. The one-shot probe still runs first in `initTracker` so the common cases log a
definite decision (§2.1 rows 1–2).

#### 3.1.2 X11 XKB source — `x11_source.go`, `x11_proto.go`, `x11_diag.go` (WPX2)

Decision (X3): **poll `XkbGetState` on a dedicated xgb connection every 50 ms**; no
`XkbSelectEvents`. Justification:

1. jezek/xgb has no XKB binding. Delivering `XkbStateNotify` requires registering a constructor
   in `xgb.NewEventFuncs` (F11), a process-global map that every connection's reader goroutine
   reads without locking (`xgb.go:557`) and whose key (the extension's event base) is only known
   after `QueryExtension`, i.e. after `NewSwitcher` already opened the clipboard's X connection
   (`runtime/switcher.go:213`, `x11selection.go:26`). A late write to that map races with the
   clipboard connection's reads — in Go a concurrent map read/write is a fatal runtime error,
   not a benign race. Polling needs no global state.
2. Upstream itself confirms X11 switches by polling `XkbGetState` (`runtime/x11_layout_switch.go:19-20,119-150`,
   10 ms × 200); the request helpers (`:32-118`) can be copied verbatim (the `layoutstate`
   package must not import `runtime`, SPEC §3.4).
3. Safety never depends on latency: the engine fires only with `Confirmed` and an unchanged
   generation from word start to fire time (SPEC §3.8); a group change seen ≤ 50 ms late can
   only *skip* a word (`layout-changed`), never fix one in the wrong layout, because the tick
   re-checks the tracker before emitting.
4. Cost: 20 requests/s of 8 bytes with 32-byte replies on a Unix socket, one timer wake-up per
   50 ms — negligible against gnome-shell's own traffic; `PollInterval` is tunable.
   `XkbSelectEvents` stays a possible later optimisation (Q-X1) once xgb grows an XKB package
   or the registration can be made at process start.

```go
const SourceX11 = "x11" // State.Source value

// X11Options configures NewX11Source.
type X11Options struct {
	Display      string        // "" ⇒ $DISPLAY (xgb.NewConnDisplay(""))
	PollInterval time.Duration // 50 ms default; clamped to 20 ms..500 ms
	Fallback     [2]Layout     // group i ⇒ Fallback[i] when _XKB_RULES_NAMES is unusable (the daemon's pair)
	Logf         func(string, ...any)
	// Test hooks (production values set by NewX11Source): dial opens a conn, sleep paces retries.
}

// X11Source implements Source. Run: connect (xgb.NewConnDisplay), QueryExtension("XKEYBOARD"),
// XkbUseExtension(1.0), read _XKB_RULES_NAMES (root, STRING) ⇒ group table, XkbGetState ⇒
// sink.Available(true) + sink.Engine(EngineForGroup(table, group)); then every PollInterval:
// XkbGetState (+ re-read _XKB_RULES_NAMES every 40th poll ≈ 2 s and whenever group >= len(table)).
// Engine is called on every poll; the tracker deduplicates (tracker.go:430-441). Any X error or
// EOF ⇒ sink.Available(false), close, reconnect with backoff 200 ms..5 s (ibusBackoff* constants).
// Query: XkbGetState on the live connection (mutex) or on a temporary one; the engine name
// is derived from the same table. Name: SourceX11.
type X11Source struct{ /* ... */ }
func NewX11Source(opts X11Options) *X11Source
func (s *X11Source) Name() string
func (s *X11Source) Run(ctx context.Context, sink Sink)
func (s *X11Source) Query(ctx context.Context) (string, error)

// X11SourceEnabled: layoutSource "x11" ⇒ true when an X display is known (env.Display or
// $DISPLAY); "auto" ⇒ true only in an X11 session (the shouldVerifyGNOMEX11LayoutSwitch rule:
// SessionType x11 or Display set, and no Wayland); never for euid 0; "ibus"/"none" ⇒ false.
func X11SourceEnabled(layoutSource string, env *detect.SessionEnv, euid int) bool

// EngineForGroup returns "xkb:<name>:<variant>:" for group (0-based) or ok=false when the group
// is outside the table (the tracker then sees Engine("") ⇒ Unknown; the next poll re-reads the
// table). ParseXKBEngine (tracker.go:526) accepts the trailing-colon form (lang part empty).
func EngineForGroup(table []Layout, group int) (engine string, ok bool)

// ParseXKBRulesNames splits the _XKB_RULES_NAMES value (five NUL-terminated strings: rules,
// model, layouts, variants, options) into the group table: layouts "us,ru" + variants ","
// ⇒ [{us ""},{ru ""}]; "us,ru"+"dvorak," ⇒ [{us dvorak},{ru ""}]; a missing variants field is
// all-empty; fewer than one layout ⇒ ok=false. A trailing NUL may be absent.
func ParseXKBRulesNames(data []byte) (table []Layout, ok bool)
```

`x11_proto.go` (test-covered byte layouts, copied from `runtime/x11_layout_switch.go:32-118`
where applicable): `QueryExtension` via `xproto.QueryExtension(conn, 9, "XKEYBOARD")` ⇒
`MajorOpcode`; `xkbUseExtensionRequest(opcode)` = `[opcode, 0, 2,0 (len), 1,0 (major), 0,0]`;
`xkbGetStateRequest(opcode)` = `[opcode, 4, 2,0, 0x00,0x01 (deviceSpec UseCoreKbd 0x0100 LE), 0,0]`;
reply: byte 12 = effective `group`, byte 13 = `lockedGroup` (use byte 12, as upstream). Root
property read: `xproto.GetProperty(conn, false, root, atomXKBRulesNames, xproto.AtomString, 0, 1024)`
with the atom interned once per connection. Raw request/reply through `conn.NewCookie(true, true)`
+ `conn.NewRequest` exactly as upstream's `requestReply`.

Table order vs. the daemon's pair: on GNOME X11 both come from `sources` (F4;
`GetCurrentLayouts` ⇒ `getLayoutsFromGnome`, `runtime/layout.go:132-136,174-189`). With explicit
`layout1`/`layout2` in `default.conf` the user may have reversed them: at connect the source logs
`Warning: X11 layout table us,ru differs from the configured pair ru,us` when the names of
`Fallback` are a permutation of the table (pair mapping is still by name, so correction stays
right — only `--layout-state` output and the warning are informational). When
`_XKB_RULES_NAMES` is missing or has fewer than two layouts the table is `Fallback[:]` and a
warning is logged once per connection.

`x11_diag.go`: `func RunX11Diagnostics(ctx context.Context, w io.Writer, display string, layouts [2]Layout, watch time.Duration) error`
— prints `Display`, XKB extension opcode/version, `_XKB_RULES_NAMES` table, current group and
its engine/index (via `describeEngine`, `diag.go:74`), then streams tracker changes for `watch`
like `watchDiagnostics` (`diag.go:58`). The existing `RunDiagnostics` (IBus) is unchanged;
`--layout-state` composes both (§3.7). `RunX11Diagnostics` is also what the manual test uses
to watch Super+Space switches.

### 3.2 `internal/gswitch/focus` additions

#### 3.2.1 X11 EWMH source — `x11_source.go` (WPX3)

```go
// X11State is the raw X11 view (no purpose information exists on X11).
type X11State struct {
	Connected bool
	Window    uint32 // _NET_ACTIVE_WINDOW; 0 = none
	AppID     string // _GTK_APPLICATION_ID (UTF8_STRING), "" when absent
	WmClass   string // WM_CLASS class part (second string), "" when absent
	Seq       uint64 // bumps on every change of Window/AppID/WmClass/Connected
	UpdatedAt time.Time
}
type X11Options struct {
	Display  string               // "" ⇒ $DISPLAY
	OnChange func(X11State)       // source goroutine; must not block
	Logf     func(string, ...any)
}
// X11Source follows _NET_ACTIVE_WINDOW on the root window over its own xgb connection:
// ChangeWindowAttributes(root, CwEventMask, PropertyChange); initial read; on every
// PropertyNotify whose Atom == _NET_ACTIVE_WINDOW: GetProperty(root, _NET_ACTIVE_WINDOW,
// WINDOW, 0, 1) ⇒ window; GetProperty(window, WM_CLASS, STRING, 0, 256) ⇒ "instance\0class\0"
// ⇒ class; GetProperty(window, _GTK_APPLICATION_ID, UTF8_STRING, 0, 256) ⇒ app id. BadWindow
// (window gone) ⇒ Window=0, "" fields. Never reads _NET_WM_NAME, WM_NAME, _NET_WM_PID.
// Reconnect with backoff 1 s..30 s (clientRetry* of client.go) on connection loss; Connected
// false ⇒ one OnChange. Start/Close/Snapshot as Client (lock-free atomic snapshot).
type X11Source struct{ /* ... */ }
func NewX11Source(opts X11Options) *X11Source
func (s *X11Source) Start(ctx context.Context)
func (s *X11Source) Snapshot() X11State
func (s *X11Source) Close()
// X11FocusEnabled: focus-source "auto" in an X11 session (same rule as layoutstate.X11SourceEnabled), never for euid 0.
func X11FocusEnabled(focusSource string, env *detect.SessionEnv, euid int) bool
// ParseWMClass returns the class part of a WM_CLASS value; "" when there is no second string.
func ParseWMClass(value []byte) string
```

Events on the root also include other PropertyNotify atoms (`_NET_CLIENT_LIST_STACKING`,
`_NET_CURRENT_DESKTOP`); they are ignored. `WaitForEvent` returns `(nil, nil)` on `Close` ⇒ the
loop exits. `xgb.Error` values (BadWindow during the reads) are handled per request through
checked cookies; an error from `WaitForEvent` itself is logged and ignored.

#### 3.2.2 Merge — `merge.go` (WPX3)

```go
// Merged combines the extension client and the X11 source into one focus.Source for the
// daemon. It owns nothing: callers create the Client with Options.OnChange =
// merged.ExtensionChanged and the X11Source with X11Options.OnChange = merged.X11Changed,
// then SetExtension/SetX11 before Start. Either side may be nil.
type MergedOptions struct {
	OnChange func(State)         // called after every merged change (any goroutine; must not block)
	Logf     func(string, ...any)
}
type Merged struct{ /* ext Source (may be nil); x11 *X11Source (may be nil); state atomic.Pointer[State]; seq */ }
func NewMerged(opts MergedOptions) *Merged
func (m *Merged) SetExtension(c *Client)
func (m *Merged) SetX11(s *X11Source)
func (m *Merged) ExtensionChanged(State)   // adapter for Client.Options.OnChange
func (m *Merged) X11Changed(X11State)     // adapter for X11Options.OnChange
func (m *Merged) Start(ctx context.Context) // starts both (nil-safe)
func (m *Merged) Close()
func (m *Merged) Snapshot() State          // implements Source
// AppSource names where the app identity in Snapshot comes from: "extension" | "x11" | "".
func (m *Merged) AppSource() string
```

Merge rule (pure function `mergeStates(ext State, x X11State) (State, appSource string)`, unit
tested): start from `ext` (or the zero `State{Purpose: PurposeUnknown}` when no client).
`Available`, `EverAvailable`, `Purpose`, `FocusUnknown`, `Locked`, `LockReason` always come from
`ext` (so `Policy` keeps its meaning: `Available` = extension owned). When `!ext.Available &&
x.Connected`: `AppID = x.AppID`, `WmClass = x.WmClass`, keys via `fold.AppKey`, `appSource = "x11"`;
when `ext.Available`: `appSource = "extension"`; else `""` with empty app fields. `Seq =
ext.Seq + x.Seq` (both monotonic ⇒ the sum is monotonic and changes whenever either side
changed, including the extension's forced `FocusChanged` bumps, `client.go:490-501`);
`UpdatedAt = max`. `OnChange` fires when the merged state differs (`sameState` of `client.go`)
or `Seq` moved.

The engine's `Focus().Seq` therefore moves on X11 focus changes too, so a word typed across an
X11 window switch is skipped as `focus-changed` even without the extension (C24 unchanged: no
buffer clearing).

#### 3.2.3 Diagnostics — `diag.go` (WPX3)

`func RunDiagnostics(ctx context.Context, w io.Writer, opts DiagOptions) error` with
`DiagOptions{Display string; ExtensionEnabled, X11Enabled bool; Policy Policy; Watch time.Duration}`:
prints `Extension: owned|absent (GetFocus: app, wm_class, purpose)`, `X11: connected|off,
active window 0x…, WM_CLASS class, _GTK_APPLICATION_ID`, `Merged: app source, AppKey,
WmClassKey, purpose`, `Policy: allowed|denied (<reason>)`, then streams merged changes for
`Watch` with the same line format. Used by `gswitch --focus-state` (§3.7).

#### 3.2.4 Policy — `policy.go` (WPX5)

```go
type Policy struct {
	Excluded         AppMatcher
	SkipPassword     bool
	SkipTerminal     bool
	SkipWhenLocked   bool
	RequireExtension bool // auto-require-focus
	RequirePurpose   bool // auto-strict-purpose (effective value, §3.3)
}
const ReasonPurposeUnknown = "purpose-unknown"
// TextPurpose reports a purpose that names a text field a user types words into: normal,
// alpha, digits, number, phone, url, email, name, date, time, datetime, terminal. It is false
// for none, unknown, password and pin. (terminal is then denied by SkipTerminal.)
func TextPurpose(p Purpose) bool
```

`Allow` order (replaces `policy.go:37-60`; existing tests keep passing):

```
Locked && SkipWhenLocked                                   ⇒ "locked"
!Available:
    RequireExtension || RequirePurpose                     ⇒ "focus-unavailable"
    (fall through: app identity may come from X11)
Available:
    FocusUnknown                                           ⇒ "focus-unknown"
    (password|pin) && SkipPassword                         ⇒ "password-field"
    terminal && SkipTerminal                               ⇒ "terminal"
    RequirePurpose && !TextPurpose(Purpose)                ⇒ "purpose-unknown"
(AppKey != "" || WmClassKey != "") && Excluded != nil && Excluded.MatchApp(AppKey, WmClassKey)
                                                           ⇒ "excluded-app"
allow
```

The empty-key guard keeps today's "unavailable ⇒ allow" result for glob entries such as `*`
(`path.Match("*", "")` is true) and keeps `Allow` allocation-free.

Why `purpose-unknown` after the password rule: on X11 a GTK password entry reports `none`
(F8), which strict mode denies as `purpose-unknown`; a toolkit that does report `password`
still gets the clearer `password-field`.

### 3.3 `internal/gswitch/userconfig` (WPX5)

New key in `auto.conf` (§3.6.2 table of SPEC.md gains one row; `layout-source` gains a value):

| Key | Type / values | Default | Consumer | Hot reload |
|---|---|---|---|---|
| `auto-strict-purpose` | `auto` \| `true` \| `false` | `auto` (⇒ true in X11 sessions, false elsewhere) | `focus.Policy.RequirePurpose`, `autoAvailability` | yes |
| `layout-source` | `auto` \| `ibus` \| **`x11`** \| `none` | `auto` | source selection (§2.1) | restart |

```go
const KeyStrictPurpose = "auto-strict-purpose"
const ( StrictAuto = "auto"; StrictOn = "true"; StrictOff = "false" )
const SourceX11 = "x11" // layout-source value
// Settings gains: StrictPurpose string (canonical "auto"|"true"|"false").
// StrictPurposeFor resolves the setting for a session: "auto" ⇒ x11.
func (s Settings) StrictPurposeFor(x11 bool) bool
```

Parsing: a dedicated `strictField` accepts `auto` plus every `parseBool` spelling (`yes/on/1`
⇒ `true`, `no/off/0` ⇒ `false`), formats canonically; `Validate`, `Equal`, `Values`,
`DiffSettings`, `Keys` include it; `DefaultSettings().StrictPurpose == StrictAuto`. Template
`templates/auto.conf` gets, after the focus-rules block (`auto.conf:40-46`):

```
# Strict purpose mode: correct only in text fields whose type the focus
# extension reports (never in fields it cannot see). auto = on in X11
# sessions, off on Wayland. Without the extension, strict mode makes
# automatic correction unavailable.
# auto-strict-purpose=auto
```

and the sources comment (`auto.conf:58-61`) lists `layout-source auto|ibus|x11|none`.

### 3.4 Runtime glue (WPX7) — `runtime/{fork.go,layout_glue.go,focus_glue.go,auto_control.go,platform.go}`

New file `runtime/platform.go`:

```go
// platformSelection is the result of selectPlatform (one per daemon start; IPC ConfigWarnings
// repeat Warnings).
type platformSelection struct {
	X11Session   bool            // X11 session rule of §0
	GNOME        bool            // detect.IsGNOMESession
	Shell        layoutstate.ShellInfo
	LayoutIBus   bool            // add the IBus source
	LayoutIBusGuarded bool       // wrap it in GuardedIBusSource
	LayoutX11    bool            // add the X11 source
	LayoutReason string          // "" | "layout-source-off" | "layout-source-unsupported" | "root-mode"
	FocusExt     bool            // start the extension client
	FocusX11     bool            // start the EWMH source
	Strict       bool            // effective auto-strict-purpose
	Warnings     []string
}
// selectPlatform is pure given its inputs (unit-tested matrix, §6.1).
func selectPlatform(st userconfig.Settings, env *SessionEnv, euid int, probe func(context.Context) layoutstate.ShellInfo) platformSelection
// isX11Session implements the §0 rule (shared with shouldVerifyGNOMEX11LayoutSwitch's logic; the
// upstream function stays untouched).
func isX11Session(env *SessionEnv) bool
```

`forkState` (`fork.go:68-110`) gains `platform platformSelection`, `focusMerged *focus.Merged`,
`focusX11 *focus.X11Source`; `focusClient *focus.Client` (`fork.go:93`) stays (nil when the
extension client is not started). `initFork` (`fork.go:116`) calls `s.platform =
selectPlatform(...)` before `initTracker`.

`initTracker` (`layout_glue.go:17-42`): replaces the `IBusSourceEnabled` test (`:22`) with
`s.platform`: no source ⇒ log and return (tracker nil); otherwise build the tracker, then
`AddSource(NewX11Source(...))` when `LayoutX11` (Display = `sessionEnv.Display` or `""`,
`Fallback` = the pair), `AddSource(NewIBusSource(...))` or the guarded wrapper when `LayoutIBus`,
`AddResync(NewGSettingsResync)` only when `GNOME`. `IBusSourceEnabled` (`ibus_source.go:86-98`)
remains the first filter inside `selectPlatform` (root, `none`).

`initFocusClient` (`focus_glue.go:21-32`): builds `focus.NewMerged`, then the extension client
(when `FocusExt`) with `OnChange: merged.ExtensionChanged`, the X11 source (when `FocusX11`)
with `OnChange: merged.X11Changed`; `merged.OnChange` does today's `enqueueControl(onFocusState)` +
`pushStatus()`. `focusState()` (`:71-76`) reads `s.focusMerged.Snapshot()`; `focusSourceName`
(`:94-103`) returns `off` (no merged), `active` (`st.Available`), `x11` (`merged.AppSource()=="x11"`),
else `absent`. `startFork`/`stopFork` (`fork.go:150-152`, `:179-181`) start/close the merged
source instead of the bare client. `forkFocusPolicy` (`:36-45`) sets `RequirePurpose:
c.Settings.StrictPurposeFor(s.platform.X11Session)`; `applyUserConfig` (`userconfig_apply.go:164`, policy swap at `:177`) re-evaluates it on reload
(hot). `restartWarnings` (`auto_control.go:506-523`) is unchanged (`layout-source` and
`focus-source` stay restart-only; `auto-strict-purpose` is hot).

`buildAutoEngine` (`auto_control.go:70-115`): the `tracker == nil` branch (`:83-88`) returns
`s.platform.LayoutReason` when set (`layout-source-unsupported`, `layout-source-off`,
`root-mode`). `autoAvailability` takes the focus state too (§2.3); `statusSnapshot`
(`:431-487`) passes `fs` and adds the platform warnings to `ConfigWarnings`.

Nothing in the manual path changes: `emitConversionEvents` (`switcher.go:907-990`) still takes
the X11 branch (`:953-974`) on X11 GNOME, and `layoutReaderFn`/`onOwnSwitch` are installed
exactly as before (`fork.go:124-128`).

### 3.5 Tray and i18n (WPX5)

`internal/tray/settings_auto.go`: new `strictPurposeCheck *gtk.CheckButton` in the checkbox
table (`:213-222`) after `requireFocusCheck`, label `strGACheckStrictPurpose`, tooltip
`strGAHintStrictPurpose`. Loading (`setAutoSettingsWidgets`, `:333-340`): checked =
`s.StrictPurposeFor(trayX11Session())` where `trayX11Session()` applies the §0 rule to the
tray's own environment. Collecting (`collectAutoSettings`, `:351-359`): if the checkbox equals
`loaded.StrictPurposeFor(x11)` the loaded value (possibly `auto`) is kept, else `true`/`false`
— so an untouched `auto` is never rewritten. `autoReasonText` (`:449-467`) maps
`layout-source-unsupported` ⇒ `strGAReasonLayoutSourceUnsupported`, `focus-source-off` ⇒
`strGAReasonFocusSourceOff`, `focus-extension-required` ⇒ `strGAReasonFocusExtensionRequired`.
`autoNotices` (`:510-532`) appends `strGAHintStrictNoExtension` when
`st.UnavailableReason == "focus-extension-required"`. The extension status row already offers
Enable / "Enable at next login" (`focus_extension.go:267-291`); on GNOME 43 `EnableExtension`
and `GetExtensionInfo` behave the same (F5), so `FocusExtNeedsRelogin` text applies.

`internal/tray/strings_ga.go` + `internal/i18n/locales/{en,ru}.json` (en text; ru by WPX5):
`strGACheckStrictPurpose` "Correct only in fields that report their type (recommended on X11)",
`strGAHintStrictPurpose` "Needs the focus extension. Fields that do not report a type (for
example xterm, or password fields on X11) are never corrected.", `strGAReasonLayoutSourceUnsupported`
"no layout source for this session (GNOME Shell below 49.3 on Wayland, or no X11 display)",
`strGAReasonFocusSourceOff` "strict mode is on but focus-source=off",
`strGAReasonFocusExtensionRequired` "strict mode needs the focus extension (enable it below or
turn strict mode off)", `strGAHintStrictNoExtension` "Strict mode: enable the gswitch-focus
extension or clear 'Correct only in fields that report their type'." Printf parameters: none.

### 3.6 Shell extension: legacy variant (WPX4)

Layout:

```
shell-extension/gswitch-focus@vabuter.github.io/        ESM tree (GNOME 50; unchanged)
shell-extension/legacy/gswitch-focus@vabuter.github.io/ legacy tree (GNOME 43, 44)
    metadata.json   same uuid/name/description/url/version; "shell-version": ["43", "44"]
    extension.js    legacy format
shell-extension/README.md                               documents both trees and which package ships which
```

`legacy/…/extension.js` is the ESM file ported line by line:

- Header: `const {Clutter, Gio, GLib, IBus, Shell} = imports.gi; const Main = imports.ui.main;
  const IBusManager = imports.misc.ibusManager;` — no `import`/`export` statements anywhere.
- `getIBusManager()` ⇒ `IBusManager.getIBusManager()` (F6).
- Signal connections use `connect` with stored handler ids and `disconnect` in `destroy()`
  (`this._focusWindowId = global.display.connect('notify::focus-window', …)`, same for
  `Shell.WindowTracker` `notify::focus-app`, the three IBusManager signals and the two
  `Main.inputMethod` notifications), instead of `connectObject/disconnectObject`, so the file
  depends on nothing added after 43 (`signalTracker.js` exports only standalone functions there,
  F6).
- Entry points: `function init() { return new GswitchFocusExtension(); }` where the class has
  `enable()`/`disable()` with the same bodies as the ESM class (F5). No `Extension` base class.
- Everything else identical: `IFACE_XML`, the purpose tables, `hintMask`, `securePurpose`,
  `purposeTable`, `FocusReporter` (incl. the `Disabling` emission with
  `Main.sessionMode.isLocked`), `Gio.DBusExportedObject.wrapJSObject`, `own_name`.
  `console.warn`, optional chaining and `??` are available in GJS 1.74 (GNOME 43).
- SPDX header and a one-line comment "Legacy variant for GNOME Shell 43/44; keep in sync with
  ../../<uuid>/extension.js (scripts/check-extension.sh compares the shared helpers)".

`scripts/check-extension.sh` (rewritten by WPX4, same CLI): expects exactly the two trees
above; per tree: metadata parses, `uuid == dir name == focus.ExtensionUUID`, `shell-version`
contains `50` (ESM) / `43` and `44` (legacy), no `session-modes`/`settings-schema`; ESM file
contains `export default class` and no `imports.`; legacy file contains `function init(` and no
line starting with `import ` or `export `; `node --check` the ESM copy as `.mjs` and the legacy
copy as `.cjs`; the extracted top-level functions `hintMask`, `securePurpose`, `purposeTable`
and the `IFACE_XML` template literal are byte-identical between the trees; `packaging/nfpm.yaml`
lists the ESM files with `packager: rpm` and the legacy files with `packager: deb`, each
`0644`, the directory `0755` (`type: dir`, both packagers), never `type: tree`.
`internal/gswitch/focus/metadata_test.go` mirrors this for both trees (and runs the existing
node helper test on both files). `scripts/install-extension.sh` gains `--legacy` to install the
legacy tree (for the Debian machine).

### 3.7 CLI (WPX7, `internal/gswitch/cli.go`)

- `--layout-state [--watch SECONDS] [--source auto|ibus|x11]` (`cli.go:56-57,129-146`): prints
  `Session: x11|wayland (DISPLAY=…, WAYLAND_DISPLAY=…)`, `GNOME Shell: <version>|unavailable`,
  the selection row it would use (`selectPlatform` with the user config and `forkGeteuid`), then
  runs `layoutstate.RunX11Diagnostics` and/or `layoutstate.RunDiagnostics` accordingly
  (`--source` forces one). Exit 1 when the selected source cannot be reached.
- `--focus-state [--watch SECONDS]` (new): `focus.RunDiagnostics` with the extension client
  enabled in GNOME sessions, the X11 source in X11 sessions, and the policy built from
  `~/.config/gswitch` (`forkFocusPolicy` logic through a small exported helper in `runtime`:
  `func FocusPolicyForDiagnostics(x11 bool) (focus.Policy, error)`). Read-only, no root.
- `runHelp` (`:99-120`) lists both.

### 3.8 Packaging (WPX6)

#### 3.8.1 Package identity and metadata (`packaging/nfpm.yaml`, deb override `:83-97`)

Deb: `gswitch-auto_<V>-<R>_amd64.deb`, `Version: <V>-<R>` (`0.8.0+auto.1-1`; dpkg grammar allows
`+` and `.`; `~` stays forbidden). Override block:

```yaml
overrides:
  deb:
    depends: [systemd, libgtk-3-0, libx11-6, libglib2.0-bin, policykit-1, x11-xkb-utils]
    recommends: [libgtk-3-bin, desktop-file-utils, gnome-shell-extension-appindicator, ibus, wl-clipboard]
    provides:  ["gswitch (= ${GSWITCH_VERSION}-${GSWITCH_RELEASE})"]
    replaces:  [gswitch]
    conflicts: [gswitch]          # allowed with Provides of the same name (Debian Policy 7.4)
    scripts:
      preinstall:  scripts/deb/preinst.sh
      postinstall: scripts/deb/postinst.sh
      # preremove/postremove: upstream's, inherited from the top-level scripts block
```

No `xclip`/`xsel` (F12). `libxkbcommon0` is not linked by either binary (F12) and stays out.
`Section: utils`, `Priority: optional` as today.

Contents: the existing extension entries (`:209-220`) get `packager: rpm`; new entries with
`packager: deb` install `shell-extension/legacy/<uuid>/{metadata.json,extension.js}` to
`/usr/share/gnome-shell/extensions/<uuid>/` (0644; the `type: dir` entry serves both). Deb-only
documentation entries (plain files, since `license`/`doc` types are rpm-only, F13):
`packaging/debian/copyright` → `/usr/share/doc/gswitch-auto/copyright`, `LICENSE` →
`/usr/share/doc/gswitch-auto/LICENSE`, `third_party/punto-rs/LICENSE` →
`…/punto-rs-LICENSE`, `third_party/punto-rs/hunspell-ru-LICENSE` → `…/hunspell-ru-LICENSE`,
`third_party/punto-rs/NOTICE.md` → `…/THIRD-PARTY-NOTICES.md`, `third_party/clipboard/LICENSE` →
`…/clipboard-LICENSE`, `README.md` → `/usr/share/doc/gswitch-auto/README.md` (all 0644,
`packager: deb`). `packaging/debian/copyright` is machine-readable (DEP-5): `Files: *` MIT
(arumata, fork vabuter), `Files: internal/gswitch/lang/*` MIT (punto-rs) with the hunspell-ru
BSD-3-Clause-Modification paragraph for `ru.*`, `Files: internal/gswitch/lang/data/en.*`
public domain (Fedora `words`), `Files: third_party/clipboard/*` MIT (Changkun Ou), with the
licence texts inline as DEP-5 requires.

#### 3.8.2 Maintainer scripts (`scripts/deb/preinst.sh`, `scripts/deb/postinst.sh`)

F14: the upstream package's `prerm remove in-favour` has already disabled the user unit when
our `preinst` runs, and nothing on deb runs before it. Mitigation:

- `preinst.sh`: for `$1 = install` (first install of gswitch-auto) and when
  `dpkg-query -W -f='${db:Status-Status}' gswitch` prints `installed` or `half-installed`
  (i.e. the upstream package is being replaced in this run), write
  `/run/gswitch-auto/replacing.<uid>` for every logged-in user with a session bus (same loop as
  `scripts/pretrans.sh:27-40`, without the `is-enabled` test — it is already false). Then the
  upstream `preinstall.sh` body (stop/disable the legacy system unit) verbatim between the
  markers `# --- upstream scripts/preinstall.sh begin ---` / `end`.
- `postinst.sh`: for every `replacing.<uid>` marker: `systemctl --user daemon-reload`, and if
  `gswitch.service` is not enabled ⇒ `enable --now` + log line (as `posttrans.sh:43-51`), remove
  the markers; then the upstream `postinstall.sh` body verbatim between the same markers; then
  the extension hint (`posttrans.sh:67-75`, with "log out and back in (or Alt+F2, r on X11)").
- Limitation documented in `docs/FORK.md` and README: a user who had the upstream package
  installed but the unit deliberately disabled gets it enabled by the replacement; plain
  upgrades of gswitch-auto are not affected (`$1 = upgrade`).
- `scripts/build-deb.sh` asserts that the text between the markers equals
  `scripts/preinstall.sh` / `scripts/postinstall.sh` minus their trailing `exit 0`
  (`sed -n '/begin ---$/,/end ---$/p' | sed '1d;$d' | diff - <(sed '$d' scripts/postinstall.sh)`),
  so upstream changes cannot drift silently; `docs/FORK.md`'s rebase checklist gets the
  re-sync step. Both scripts exit 0 under `env -i PATH=/nonexistent /bin/sh`.

#### 3.8.3 Build (`packaging/debian12.Containerfile`, `scripts/build-deb.sh`, `Makefile`)

```Dockerfile
FROM docker.io/library/golang:1.26-bookworm
RUN apt-get update && apt-get install -y --no-install-recommends \
      gcc libc6-dev pkg-config libgtk-3-dev libx11-dev libxkbcommon-dev \
      dpkg-dev fakeroot lintian python3 git ca-certificates xvfb x11-xkb-utils \
    && rm -rf /var/lib/apt/lists/*
RUN go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.48.0
ENV GOFLAGS=-buildvcs=false
WORKDIR /src
```

`scripts/build-deb.sh [--version V] [--revision R] [--out DIR] [--skip-tests] [--no-container]
[--image IMG]`: without `--no-container` it builds the image
(`podman build -t localhost/gswitch-auto-deb:bookworm -f packaging/debian12.Containerfile packaging/`)
and re-executes itself inside (`podman run --rm --userns=keep-id -v "$repo:/src:Z" -w /src -e
GSWITCH_VERSION -e GSWITCH_RELEASE … scripts/build-deb.sh --no-container …`; `git config
--global --add safe.directory /src` first so `version.sh` works). Inside: preflight (tools,
every `src:` with `packager: deb` or no packager exists), `scripts/version.sh --verify-deb`,
`scripts/check-extension.sh`, the scriptlet-marker check (§3.8.2), `go test ./...` (unless
skipped), CGO build with `-X main.version=$V` into `builds/deb/` (never over the Fedora
binaries in `builds/`), `nfpm package --packager deb --target dist/gswitch-auto_<V>-<R>_amd64.deb`,
`sha256sum` into `dist/checksums-deb.txt`, then assertions with `dpkg-deb -I`/`-c`/`-e`:
Package/Version/Architecture, Depends/Recommends/Provides/Replaces/Conflicts lines, files
(binaries 0755, user unit, udev rule, `default.conf` listed in `conffiles`, legacy
`metadata.json`/`extension.js`, **no** ESM-only marker — the packaged `extension.js` contains
`function init(`), `/usr/share/doc/gswitch-auto/copyright`, licences, no
`/usr/share/licenses`, no `/usr/lib/systemd/system/gswitch.service`; packaged user unit
byte-identical to `configs/gswitch.service`; `lintian --fail-on error` (warnings allowed, listed).
`Makefile`: `deb` (runs `scripts/build-deb.sh`), `deb-image` (build the image only), variables
`PODMAN ?= podman`, `DEB_IMAGE ?= localhost/gswitch-auto-deb:bookworm`, `GSWITCH_DEB_REVISION ?= 1`;
`clean` also removes `builds/deb`. CLAUDE.md/FORK.md list `make deb`.

#### 3.8.4 Versions (`scripts/version.sh`)

New `--debcmp A B` (prints -1/0/1): `dpkg --compare-versions` when present, else a Python port
of dpkg's `verrevcmp` (epoch, upstream, revision; `~` lowest, letters before non-letters, digit
runs numeric) embedded like the rpm fallback (`version.sh:132-146`). New `--verify-deb`: the
same table as `verify` (`version.sh:181-196`) evaluated with `--debcmp`, plus
`0.8.0+auto.1-1 > 0.8.0-1`, `< 0.8.1-1`, `0.8.0+auto.1.dev3.gabc1234-1 > 0.8.0+auto.1-1 < 0.8.0+auto.2-1`,
`0.8.0~auto.1-1 < 0.8.0-1` (why `~` is forbidden on deb too), and that the computed version
contains no `-`/`~`. `build-deb.sh` runs `--verify-deb` inside the container (dpkg present);
`build-rpm.sh` is untouched.

#### 3.8.5 CI (`.github/workflows/ci.yml`, `release.yml`)

`ci.yml` gains job `deb` (`runs-on: ubuntu-24.04`, `container: docker.io/library/golang:1.26-bookworm`):
apt deps as the Containerfile, `go install nfpm@v2.48.0`, fetch upstream tags (as `:75-80`),
`scripts/build-deb.sh --no-container --skip-tests --out dist`, upload artifact `deb`
(`dist/*.deb`, `dist/checksums-deb.txt`). Job `install-test-deb` (`container: debian:12`,
`needs: deb`): `apt-get update`; download the upstream `gswitch_0.8.0_linux_amd64.deb` from the
v0.8.0 release with checksum verification (skip the step with a notice if the asset is absent),
`apt-get install -y ./gswitch_0.8.0_linux_amd64.deb`; `apt-get install -y ./dist/gswitch-auto_*.deb`;
assert: `dpkg-query -W -f='${db:Status-Status}' gswitch` is not `installed`, `dpkg -s
gswitch-auto`, `dpkg -L gswitch-auto` contains the legacy extension files and `copyright`,
`gswitch --version` equals the package version, `dpkg -V gswitch-auto` is empty, `test -f
/usr/lib/systemd/user/gswitch.service`, no `/run/gswitch-auto` left; `apt-get remove -y
gswitch-auto` removes `/usr/bin/gswitch`. The `preinst` marker logic runs with no logged-in
users there (loop is empty) — that is fine; the scriptlet tolerance test (`env -i`) covers
syntax. `release.yml` adds a `deb` job (same container) and uploads
`gswitch-auto_<V>-1_amd64.deb` + `checksums-deb.txt` to the same release (`gh release upload`
after the rpm job created it; `needs: release`).

### 3.9 Defaults: `templates/exceptions-apps.txt` (WPX7)

Matching is `fold.AppKey` on both the app id and the WM_CLASS (lower-case, `.desktop`
stripped; `userconfig/lists.go` `MatchApp`), so entries are written lower-case and match the
X11 `WM_CLASS` class part case-insensitively. Added lines (with a comment "X11 / Debian
WM_CLASS variants; the Wayland ids above still apply"):

```
# Terminals (X11 WM_CLASS class names and legacy ids)
gnome-terminal            # WM_CLASS class "Gnome-terminal" (gnome-terminal-server on X11)
gnome-terminal-server     # Wayland app id of the same program
org.gnome.console
kgx                       # GNOME Console binary / X11 class
xterm                     # "XTerm"
uxterm
konsole
terminator
tilix
xfce4-terminal
mate-terminal
lxterminal
urxvt
st-256color
# Password managers
keepassxc                 # WM_CLASS "keepassxc"/"KeePassXC"; app id org.keepassxc.KeePassXC
bitwarden
1password
```

(Existing entries stay.) The comment header is reworded: exclusions need the Shell extension on
Wayland; on X11 they also work from the window's `WM_CLASS` without it.

### 3.10 Documentation (WPX7)

- `README.md`: "About this fork" (`:8`) states both targets; "Install" (`:61`) adds the `.deb`
  (`sudo apt install ./gswitch-auto_<V>-1_amd64.deb`); "Configuration" (`:80`) documents
  `auto-strict-purpose` and `layout-source=x11`; "Command line additions" (`:122`) documents
  `--layout-state --source` and `--focus-state`; "GNOME Shell extension" (`:165`) documents
  the legacy tree and "Alt+F2, r"; a new "X11 notes" subsection (what works, strict mode, the
  F8 password behaviour, the dpkg replacement caveat).
- `docs/FORK.md` (Russian): build section (`:44`) adds `make deb` and the container; release
  checklist (`:93`) adds the deb asset; rebase checklist (`:106`) adds the scriptlet re-sync
  (§3.8.2) and the legacy extension sync; modified-files table (`:140`) is updated (no new
  upstream files are modified by this addendum: `switcher.go` hooks are unchanged); install /
  rollback (`:179`) gets the Debian section (`apt install ./gswitch_0.8.0_linux_amd64.deb` if
  the user keeps it, else `apt remove gswitch-auto`); limitations (`:217`) list strict mode,
  polling latency and the unit re-enable heuristic.
- `CLAUDE.md`: the second machine, `make deb`, `--focus-state`, gated `GSWITCH_X11_TEST`,
  "never run podman builds with the host's `builds/` as target".
- `docs/NOTES.md`: environment facts F1–F3 for the Debian machine.
- `shell-extension/README.md`: both trees, which package ships which, the GNOME 43 limits.

---

## 4. Integration points in existing files (HEAD `7c0de1a`)

Upstream files touched: **none** beyond the already-modified `internal/gswitch/cli.go`. Every
other edit is in fork-owned files. (`configs/gswitch.service`, `scripts/{pre,post}{install,remove}.sh`,
`runtime/switcher.go`, `x11_layout_switch.go`, `x11.go`, `detect/*` stay byte-identical.)

| File | Location (HEAD) | Change | WP |
|---|---|---|---|
| `internal/gswitch/layoutstate/gnome_version.go` (new) | — | §3.1.1 | WPX1 |
| `internal/gswitch/runtime/layout_glue.go` | `initTracker` `:17-42` (`IBusSourceEnabled` `:22`, `AddSource` `:35`, `AddResync` `:36`) | selection from `s.platform`; guarded/x11 sources; resync only on GNOME | WPX1 (guard + probe call), WPX7 (x11 source wiring, see §5 note) |
| `internal/gswitch/layoutstate/{x11_source.go,x11_proto.go,x11_diag.go}` (new) | — | §3.1.2 | WPX2 |
| `internal/gswitch/focus/{x11_source.go,merge.go,diag.go}` (new) | — | §3.2.1–§3.2.3 | WPX3 |
| `shell-extension/legacy/<uuid>/{metadata.json,extension.js}` (new), `shell-extension/README.md` | — | §3.6 | WPX4 |
| `scripts/check-extension.sh` | whole file (`:1-204`) | two trees, helper equality, packager filters | WPX4 |
| `scripts/install-extension.sh` | option parsing (`:30-`) | `--legacy` | WPX4 |
| `internal/gswitch/focus/metadata_test.go` | `TestExtensionDirectoryMatchesUUID` `:16-30`, `TestExtensionMetadata` `:32-60`, script tests `:62-152` | both trees | WPX4 |
| `internal/gswitch/focus/policy.go` | `Policy` `:13-19`; reasons `:22-29`; `Allow` `:37-60` | `RequirePurpose`, `TextPurpose`, `ReasonPurposeUnknown`, new order | WPX5 |
| `internal/gswitch/userconfig/settings.go` | keys `:13-36`; values `:38-44`; `Settings` `:46-57`; `DefaultSettings` `:60-85`; `settingFields` `:183-206` (`layout-source` `:204`); `Equal` `:277-303` | `auto-strict-purpose`, `layout-source=x11`, `StrictPurposeFor` | WPX5 |
| `internal/gswitch/userconfig/templates/auto.conf` | `:40-46`, `:58-61` | new key, value list | WPX5 |
| `internal/tray/settings_auto.go` | fields `:33-37`; table `:213-222`; `setAutoSettingsWidgets` `:333-340`; `collectAutoSettings` `:351-359`; `autoReasonText` `:449-467`; `autoNotices` `:510-532` | strict checkbox, reasons, hint | WPX5 |
| `internal/tray/strings_ga.go`, `internal/i18n/locales/{en,ru}.json` | after `strGACheckRequireFocus` (`strings_ga.go:30`, `en.json:175`, `ru.json:175`); after `strGAReasonDetectorError` (`:24`, `:169`) | six new ids | WPX5 |
| `packaging/nfpm.yaml` | override `:83-97`; extension entries `:209-220`; licences `:224-248`; README `:250-254` | §3.8.1 | WPX6 |
| `packaging/debian12.Containerfile`, `packaging/debian/copyright`, `scripts/build-deb.sh`, `scripts/deb/{preinst,postinst}.sh` (new) | — | §3.8.2–§3.8.3 | WPX6 |
| `scripts/version.sh` | `vercmp` `:115-150`; `verify` `:152-224`; `case` `:226-239` | `--debcmp`, `--verify-deb` | WPX6 |
| `scripts/build-rpm.sh` | preflight `:134-151` | skip `src:` entries whose `packager:` is `deb` | WPX6 |
| `Makefile` | `.PHONY` `:33`; after `install:` `:56-58`; `clean` `:66-67` | `deb`, `deb-image`, variables | WPX6 |
| `.github/workflows/ci.yml` | after `install-test` (`:103-162`) | `deb`, `install-test-deb` jobs | WPX6 |
| `.github/workflows/release.yml` | after `Publish GitHub release` (`:73`) | `deb` job + upload | WPX6 |
| `internal/gswitch/runtime/platform.go` (new) | — | §3.4 | WPX7 |
| `internal/gswitch/runtime/fork.go` | fields `:90-98`; `initFork` `:116-134` (`initTracker` `:123`, `initFocusClient` `:130`); `startFork` `:147-152`; `stopFork` `:178-181` | `platform`, merged focus, start/close | WPX7 |
| `internal/gswitch/runtime/focus_glue.go` | consts `:14-16`; `initFocusClient` `:21-32`; `forkFocusPolicy` `:36-45`; `focusState` `:71-76`; `focusSourceName` `:94-103` | merged source, `x11` value, `RequirePurpose` | WPX7 |
| `internal/gswitch/runtime/auto_control.go` | consts `:26-34`; `buildAutoEngine` `:83-88`; `autoAvailability` `:417-427`; `statusSnapshot` `:443-457` | new reasons, focus-aware availability, platform warnings | WPX7 |
| `internal/gswitch/runtime/userconfig_apply.go` | `applyUserConfig` (policy swap) | `RequirePurpose` from `StrictPurposeFor` on reload | WPX7 |
| `internal/gswitch/cli.go` | switch `:56-57`; help `:99-120`; `runLayoutState` `:129-146`; `parseLayoutStateArgs` `:149-172` | `--source`, `--focus-state` | WPX7 |
| `internal/gswitch/userconfig/templates/exceptions-apps.txt` | whole file | §3.9 | WPX7 |
| `README.md`, `docs/FORK.md`, `CLAUDE.md`, `docs/NOTES.md` | §3.10 | docs | WPX7 |

---

## 5. Work packages and ownership

Rules as SPEC §5: a WP modifies only the files it owns; cross-WP dependencies are on the
interfaces of §3; fakes for another WP's API are allowed, edits to its files are not. Unexported
test helpers carry the WP prefix (`gvTest*` WPX1, `x11LayoutTest*` WPX2, `x11FocusTest*` WPX3,
`legacyExtTest*` WPX4, `strictTest*` WPX5, `platformTest*` WPX7). Every WP ends with
`gofmt -l` empty, `go build ./... && go vet ./... && go test ./...` green and the commit format
of the task. Phase plan:

```
Phase A (parallel): WPX1  WPX2  WPX3  WPX4  WPX5  WPX6
Phase B (serial):   WPX7 (after merge of A)  →  WPX6 finalisation (CI/deb on the integrated tree)
```

### WPX1 — GNOME Shell version guard
- **Owns (new)**: `internal/gswitch/layoutstate/gnome_version.go`, `gnome_version_test.go`,
  `gnome_version_bus_test.go` (gated). **(modified)**: `internal/gswitch/runtime/layout_glue.go`
  — only `initTracker`, where it (a) runs `ProbeShellVersion` (2 s) when `IBusSourceEnabled`
  is true, (b) applies `IBusSourceAllowed`, (c) wraps the IBus source in `GuardedIBusSource`
  when deferred, (d) logs the decision; it leaves a clearly marked hook comment
  `// WPX7: X11 source selection goes here` for WPX7, which later replaces the probe call with
  `s.platform` (WPX7 owns `layout_glue.go` in Phase B).
- **Depends**: none.
- **Tests**: `ParseShellVersion` table; `IBusEngineTracksLayout` boundary (`43.9` f, `49.2` f,
  `49.3` t, `49.rc` f, `50.6` t); `IBusSourceAllowed` matrix (auto/ibus × ok ≥/ok </err);
  `GuardedIBusSource` with a fake probe and injected sleep: err×3 then ok ≥ ⇒ inner `Run`
  called once, `Query` delegates; ok < ⇒ inner never run, `Available(true)` never sent, one log
  line, `Query` errors; ctx cancel while pending ⇒ returns; gated `GSWITCH_DBUS_TEST=1`: a fake
  `org.gnome.Shell` object with a `ShellVersion` property on `dbus-run-session` ⇒ `ProbeShellVersion`
  returns it, and no name ⇒ `ErrShellUnavailable`; `initTracker` unit test with
  `layoutstate` fakes: GNOME env + probe `43.9` ⇒ `tracker == nil` and the log line; probe
  error ⇒ tracker with a guarded source.
- **Acceptance**: on the Fedora host `gswitch --layout-state` (after WPX7) still reports the
  IBus source; no message is ever sent to `org.freedesktop.IBus` on the session bus (C20).

### WPX2 — X11 XKB layout source
- **Owns (new)**: `internal/gswitch/layoutstate/x11_source.go`, `x11_proto.go`,
  `x11_diag.go`, `x11_source_test.go`, `x11_proto_test.go`, `x11_integration_test.go` (gated).
- **Depends**: none (`detect.SessionEnv` for `X11SourceEnabled`; `xgb`/`xproto` already in `go.mod`).
- **Tests**: unit with a fake connection interface (`x11Conn`: `QueryExtension`, `UseExtension`,
  `GetState`, `RulesNames`, `Close`): initial group ⇒ `Available(true)`+`Engine("xkb:us::")`;
  group 1 ⇒ `Engine("xkb:ru::")`; identical polls ⇒ repeated `Engine` calls that the real
  tracker dedups (test through a real `Tracker` too: generation unchanged); table re-read after
  out-of-range group; `_XKB_RULES_NAMES` missing ⇒ `Fallback` + one warning; dial failure ⇒
  `Available(false)` + backoff 200 ms→5 s with injected sleep; `Query` on live and temporary
  connections; `ParseXKBRulesNames` table (`evdev\0pc105\0us,ru\0,\0grp:alt_shift_toggle\0`,
  variants `dvorak,`, no trailing NUL, three layouts, empty); `EngineForGroup`; request bytes
  equal upstream's (`x11_layout_switch.go:638-653` golden copies); `X11SourceEnabled` matrix
  (env nil with process env, env x11, env wayland, euid 0, `"x11"` with/without display).
  Gated `GSWITCH_X11_TEST=1`: connect to `$DISPLAY`, `Query` returns the engine of the first
  `_XKB_RULES_NAMES` layout and `Run` reports `Available(true)`+`Engine` within 1 s (read-only;
  passes on the host's Xwayland, F16); additionally with `GSWITCH_X11_TEST_SWITCH=1` (container
  only): a second connection sends `XkbLatchLockState` (minor 5: `[opcode,5,4,0, deviceSpec,
  affectModLocks 0, modLocks 0, lockGroup 1, groupLock 1, affectModLatches 0, 0, latchGroup 0,
  groupLatch 0,0]`) to lock group 1 and expects index 1 within 200 ms, then restores group 0.
- **Acceptance**: no `runtime` import; `go vet` clean; host smoke test passes.

### WPX3 — X11 focus source and merge
- **Owns (new)**: `internal/gswitch/focus/x11_source.go`, `merge.go`, `diag.go`,
  `x11_source_test.go`, `merge_test.go`, `x11_integration_test.go` (gated).
- **Depends**: none (`fold.AppKey`, `detect.SessionEnv`).
- **Tests**: fake X client: initial active window with `WM_CLASS "gnome-terminal-server\0Gnome-terminal\0"`
  and `_GTK_APPLICATION_ID "org.gnome.Terminal"` ⇒ `X11State{WmClass:"Gnome-terminal", AppID:"org.gnome.Terminal"}`;
  PropertyNotify for another atom ⇒ no change; new window ⇒ Seq+1; BadWindow ⇒ empty fields,
  Seq+1; window 0 ⇒ empty; `ParseWMClass` (no second NUL, empty, single string); reconnect on
  EOF. `mergeStates`: ext available wins (x11 ignored); ext absent + x11 ⇒ app fields from x11
  with `AppKey == "org.gnome.terminal"`, `WmClassKey == "gnome-terminal"`, `Purpose ==
  PurposeUnknown`, `appSource == "x11"`; ext absent + x11 disconnected ⇒ empty, `""`; Locked
  and LockReason preserved; `Seq` monotonic and moves on either side; `OnChange` once per
  change; nil client / nil x11 / both nil safe; `Start`/`Close` idempotent. Gated
  `GSWITCH_X11_TEST=1`: connect to `$DISPLAY`, read `_NET_ACTIVE_WINDOW` and, when non-zero,
  its `WM_CLASS` (read-only, passes on the host).
- **Acceptance**: `focus` still has no GTK/`runtime` import; `TestPolicyAllowDoesNotAllocate`
  unaffected.

### WPX4 — Legacy extension, check script, metadata tests
- **Owns**: `shell-extension/legacy/**`, `shell-extension/README.md`,
  `scripts/check-extension.sh`, `scripts/install-extension.sh`,
  `internal/gswitch/focus/metadata_test.go`. (`packaging/nfpm.yaml` belongs to WPX6; WPX4
  tells WPX6 the exact entries in §3.6/§3.8.1 and tests against them once merged.)
- **Depends**: WPX6's nfpm entries for the packaging check (the script skips the packaging
  section with a notice while the legacy entries are absent, so WPX4 is green alone).
- **Tests**: `metadata_test.go` for both trees (uuid, shell-version 50 / 43+44, no session-modes,
  legacy has `function init(` and no `import `/`export ` lines, ESM has `export default class`);
  helper equality (`hintMask`, `securePurpose`, `purposeTable`, `IFACE_XML`); the node helper
  test runs on both files; `check-extension.sh` run from the test when `bash` exists (`--strict`
  in CI).
- **Acceptance**: `node --check` of both copies; `gnome-extensions pack` of the legacy tree
  succeeds (manual, on the Debian machine).

### WPX5 — Strict purpose mode
- **Owns**: `internal/gswitch/focus/policy.go`, `policy_test.go`;
  `internal/gswitch/userconfig/settings.go`, `settings_test.go`, `templates/auto.conf`;
  `internal/tray/settings_auto.go`, `settings_auto_test.go`, `strings_ga.go`;
  `internal/i18n/locales/{en,ru}.json`.
- **Depends**: none (the merged state only fills existing `State` fields).
- **Tests**: policy: `RequirePurpose` with purposes none/unknown ⇒ `purpose-unknown`,
  password/pin ⇒ `password-field`, terminal ⇒ `terminal`, normal/email/url ⇒ allowed;
  `!Available && RequirePurpose` ⇒ `focus-unavailable`; `!Available && !RequireExtension` with
  X11-filled keys matching the matcher ⇒ `excluded-app`; `!Available` with empty keys and a
  matcher that matches `""` ⇒ allowed (guard); `TextPurpose` table; allocation test still 0.
  userconfig: round trip of `auto|true|false|yes|off`, invalid ⇒ default + warning,
  `StrictPurposeFor(true/false)`, `layout-source=x11` accepted, `Equal`/`Diff` include the key,
  template contains the key. tray: `collectAutoSettings` keeps `auto` when unchanged and writes
  `true`/`false` when toggled; `autoReasonText` for the three codes; `autoNotices` hint;
  `TestForkKeysPresentInForkLocales` covers the new ids.
- **Acceptance**: existing policy and tray tests unchanged.

### WPX6 — Packaging
- **Owns**: `packaging/nfpm.yaml`, `packaging/debian12.Containerfile`,
  `packaging/debian/copyright`, `scripts/build-deb.sh`, `scripts/deb/{preinst,postinst}.sh`,
  `scripts/version.sh`, `scripts/build-rpm.sh` (preflight only, `:134-151`), `Makefile`,
  `.github/workflows/{ci.yml,release.yml}`.
- **Depends**: paths only. `shell-extension/legacy/<uuid>/*` comes from WPX4 in the same
  phase, so `build-rpm.sh`'s preflight (which today checks every `src:` in `nfpm.yaml`) must
  skip entries carrying `packager: deb`, and `build-deb.sh`'s preflight skips `packager: rpm`
  entries; RPM builds stay green while WPX4 is in flight, and the deb build is first exercised
  end to end after the Phase A merge.
- **Tests**: `scripts/version.sh --verify-deb` (python fallback on the host, dpkg in the
  container); `env -i PATH=/nonexistent /bin/sh scripts/deb/*.sh`; marker-section identity;
  `shellcheck` (CI); deb assertions of §3.8.3; the `install-test-deb` job; `build-rpm.sh` still
  passes on the host with the deb-only entries present.
- **Acceptance**: `make deb` on the host produces `dist/gswitch-auto_<V>-1_amd64.deb` and
  `dist/checksums-deb.txt` via podman; `make rpm` unchanged.

### WPX7 — Integration (after merge)
- **Owns (new)**: `internal/gswitch/runtime/platform.go`, `platform_test.go`,
  `x11_fork_test.go`. **(modified)**: `runtime/{fork.go,layout_glue.go,focus_glue.go,
  auto_control.go,userconfig_apply.go,fork_test.go,switcher_fork_test.go}`,
  `internal/gswitch/cli.go`, `cli_test.go` (new), `internal/gswitch/userconfig/templates/exceptions-apps.txt`,
  `internal/gswitch/ipc/introspect.go` (comments only), `README.md`, `docs/FORK.md`,
  `CLAUDE.md`, `docs/NOTES.md`.
- **Depends**: all of Phase A.
- **Tests**: `TestSelectPlatformMatrix` (every row of §2.1/§2.2 with fake env/probe/euid);
  `TestInitTrackerX11Session` (X11 env ⇒ exactly one source, `x11`; GNOME ⇒ resync present;
  non-GNOME ⇒ none); `TestInitTrackerNeverTwoPushSources`; `TestStatusAvailabilityStrict`
  (strict + ext absent ⇒ `focus-extension-required`, locked ⇒ available with
  `FocusSuppressedReason=locked`, `focus-source=off` ⇒ `focus-source-off`);
  `TestFocusSourceNameX11`; `TestEnableAutoReasonsX11` (Wayland GNOME < 49.3 ⇒
  `layout-source-unsupported`); `TestManualConversionWithoutTracker` extended with an X11
  non-GNOME env and `layout-source=none` (upstream path unchanged); `TestStrictPurposeHotReload`
  (reload flips `auto-strict-purpose` ⇒ policy swapped without restart);
  `TestExceptionsTemplateMatchesX11Classes` (`MatchApp("", "Gnome-terminal")`,
  `MatchApp("org.gnome.Terminal", "")`, `MatchApp("", "keepassxc")`, `MatchApp("", "XTerm")`
  all true with the seeded template; `MatchApp("", "gedit")` false); CLI argument parsing for
  `--source`/`--focus-state`.
- **Acceptance**: §6.3 manual script on the Debian machine; the Fedora host behaves as before
  (M3, M6, M8b of SPEC §6.4 re-run).

---

## 6. Test plan

### 6.1 Unit tests (always on)

Listed per WP in §5. Shared fakes: `layoutstate` keeps its `fakeSource` pattern
(`tracker_test.go`); the X11 sources get an interface seam for the X connection so the protocol
code and the state machine are tested without a server. `go test -race ./internal/gswitch/...`
must pass (the merged focus source and the polling source are the new concurrency).

### 6.2 Gated and integration tests

| Gate | What | Where it runs |
|---|---|---|
| `GSWITCH_DBUS_TEST=1` (`dbus-run-session`) | fake `org.gnome.Shell` ⇒ `ProbeShellVersion` | host, CI |
| `GSWITCH_X11_TEST=1` | X11 layout source `Query`/`Run` and EWMH focus read against `$DISPLAY` — **read-only**; `XkbSelectEvents` is not used, nothing is changed on the server | host (Xwayland `:0`, F16), CI (`xvfb-run`), container |
| `GSWITCH_X11_TEST=1 GSWITCH_X11_TEST_SWITCH=1` | additionally locks XKB group 1 and back (writes the server's keyboard state) — **never on the host's live Xwayland** | container / `xvfb-run` only |
| `GSWITCH_IBUS_TEST=1` | unchanged | host |

Container recipe (documented in `docs/FORK.md` and `CLAUDE.md`), run from the repo root:

```
podman run --rm --userns=keep-id -v "$PWD:/src:Z" -w /src localhost/gswitch-auto-deb:bookworm \
  sh -c 'Xvfb :99 -screen 0 1024x768x24 >/dev/null 2>&1 & sleep 1; export DISPLAY=:99; \
         setxkbmap -layout us,ru -option grp:alt_shift_toggle; \
         GSWITCH_X11_TEST=1 GSWITCH_X11_TEST_SWITCH=1 go test ./internal/gswitch/layoutstate/ ./internal/gswitch/focus/ -run X11 -v'
```

(`Xvfb` and `x11-xkb-utils` are in the image, §3.8.3.) CI's `build-test` job already installs
`xvfb` (`ci.yml:48`); it runs the same command with `xvfb-run -a`.

### 6.3 Manual test script — Debian 12, GNOME on Xorg (user)

Prerequisites: `gswitch-auto_<V>-1_amd64.deb` copied to the machine; no other gswitch process in
the foreground; gedit or gnome-text-editor as the test field; a terminal (gnome-terminal) and
`xterm` installed for MX7.

- **MX1 Install.** `sudo apt install ./gswitch-auto_<V>-1_amd64.deb` (over the upstream `.deb`
  if it is installed). Expect `dpkg -s gswitch` ⇒ not installed (or `config-files`),
  `gswitch --version` ⇒ `gswitch version <V>`, `systemctl --user is-enabled gswitch.service`
  ⇒ `enabled` and `is-active` ⇒ `active` (if the unit was never enabled before:
  `systemctl --user enable --now gswitch.service`), `systemctl --user cat gswitch.service`
  identical to upstream's. `journalctl --user -u gswitch.service -b` shows one line
  `layout sources: x11 … ; focus sources: extension, x11 ; strict purpose: true`.
- **MX2 Layout switch key.** `gswitch --detect-layout-switch` ⇒ `scancodes: [186]`, `source:
  gnome`, raw `XF86Launch7`; `gsettings get org.gnome.desktop.wm.keybindings switch-input-source`
  now also contains `'XF86Launch7'` (added by the daemon, F9). Super+Space still switches.
- **MX3 X11 layout tracker.** `xprop -root _XKB_RULES_NAMES` ⇒ `"evdev", "pc105", "us,ru", ",",
  "…"`. `gswitch --layout-state --watch 20` ⇒ `Session: x11`, `GNOME Shell: 43.9`, source `x11`,
  table `us,ru`, group 0 ⇒ `xkb:us::` index 0; press Super+Space twice during the watch ⇒ two
  lines, index 1 then 0, each within ~50 ms; `ibus engine` prints `xkb:us::eng` in both layouts
  (expected and unused, F2). `gswitch --layout-state --source ibus` ⇒ reports that the IBus
  source is disabled for GNOME Shell 43.9.
- **MX4 Extension.** Log out and in (or Alt+F2, `r`, Enter). `gnome-extensions info
  gswitch-focus@vabuter.github.io` ⇒ `State: ACTIVE` (else enable from the tray settings or
  `gnome-extensions enable …`); the installed file is the legacy variant (`grep -c 'function
  init' /usr/share/gnome-shell/extensions/gswitch-focus@vabuter.github.io/extension.js` ⇒ 1).
  `gdbus call --session --dest org.gnome.Shell.Extensions.GswitchFocus --object-path
  /org/gnome/Shell/Extensions/GswitchFocus --method org.gnome.Shell.Extensions.GswitchFocus.GetFocus`
  with gnome-terminal focused ⇒ `('org.gnome.Terminal.desktop', 'Gnome-terminal', 'terminal')`;
  with gedit ⇒ `(…, 'Gedit', 'normal')`; with a GTK password entry (Settings → Users → Password,
  or `zenity --password`) ⇒ purpose `none` (F8, expected on X11).
- **MX5 Focus diagnostics.** `gswitch --focus-state --watch 15` while switching between
  gedit, gnome-terminal and xterm ⇒ merged lines show `app source: extension`, purposes
  `normal` / `terminal` / `none`, policy `allowed` / `denied (terminal)` / `denied (purpose-unknown)`.
  `busctl --user get-property io.github.vabuter.GswitchAuto /io/github/vabuter/GswitchAuto
  io.github.vabuter.GswitchAuto FocusSource` ⇒ `"active"`.
- **MX6 Auto-correction.** Toggle auto on (LShift+RShift; tray shows on). In gedit, layout us:
  `ghbdtn ` ⇒ `привет ` and the layout indicator shows ru; `руддщ ` (ru) ⇒ `hello `; `hello `
  unchanged; `ghbdtn` + Enter unchanged; `GTK ` unchanged; `ghbdtn? ` ⇒ `привет, `. Double-Shift
  right after a fix undoes it. `journalctl --user -u gswitch.service -f` shows one `auto:` line
  per Space (with `gswitch --debug` after stopping the service, if preferred).
- **MX7 Strict mode.** In `xterm` type `ghbdtn ` ⇒ **no** correction; `busctl … FocusSuppressedReason`
  ⇒ `"purpose-unknown"`. Settings → Auto correction: clear "Correct only in fields that report
  their type", Apply ⇒ `grep auto-strict-purpose ~/.config/gswitch/auto.conf` ⇒ `false`;
  xterm now gets corrections (fail-open). Re-check the box, Apply ⇒ the file says `true`
  (not `auto`: the tray writes the explicit value once toggled); hand-edit back to
  `auto-strict-purpose=auto` ⇒ the daemon logs the reload and behaves as `true`.
- **MX8 Extension absent in strict mode.** `gnome-extensions disable gswitch-focus@vabuter.github.io`
  ⇒ within a second `AutoUnavailableReason` ⇒ `"focus-extension-required"`, `FocusSource` ⇒
  `"x11"`, the tray status says "Unavailable: strict mode needs the focus extension…" and the
  hint line appears; no corrections anywhere. `gswitch --focus-state` shows `app source: x11`
  with `WM_CLASS` of the focused window. Set strict off (MX7) ⇒ corrections return everywhere
  except excluded apps: in gnome-terminal (class `Gnome-terminal`) no correction
  (`excluded-app`, from X11 identity). Re-enable the extension; restore strict.
- **MX9 Password and password manager.** GTK password entry (MX4): no correction in any
  mode with the extension active (purpose `none`). KeePassXC (if installed): no correction with
  or without the extension (`keepassxc` in `exceptions-apps.txt` matches `WM_CLASS`).
- **MX10 Upstream gestures.** Auto off and on: `ghbdtn` + double-Shift ⇒ `привет`; again ⇒
  back; held-Shift phrase; select `Hello` + Ctrl + held Shift + double-tap other Shift ⇒ `hELLO`;
  the daemon log shows the X11 XKB confirmation path (no "GNOME Wayland" wording). Selection
  chord LShift+RAlt+RCtrl on a selected `ghbdtn` ⇒ `привет`, layout unchanged.
- **MX11 Lock.** Lock the screen (Super+L), unlock ⇒ during the lock `FocusSuppressedReason`
  was `locked` (`Disabling("locked")` from the legacy extension); auto works after unlock.
- **MX12 Robustness.** `systemctl --user restart gswitch.service` ⇒ state preserved (tray
  running). Change `sources` order in GNOME Settings (ru first), log out/in ⇒ `--layout-state`
  table `ru,us`, correction still right (mapping by name; a warning about the configured pair
  order is acceptable).
- **MX13 Removal / rollback.** `sudo apt remove gswitch-auto` ⇒ `/usr/bin/gswitch` gone, unit
  disabled; or `sudo apt install ./gswitch_0.8.0_linux_amd64.deb` (if kept) ⇒ upstream back,
  `systemctl --user enable --now gswitch.service`.

Fedora host regression after the merge: SPEC §6.4 M3, M5, M6, M8b and M12 unchanged;
`gswitch --layout-state` shows `GNOME Shell: 50.6`, source `ibus`.

---

## 7. Risks, mitigations, open questions

| # | Risk | Mitigation |
|---|---|---|
| K-X1 | Polling latency (≤ 50 ms) makes the auto confirm ~50–100 ms slower than upstream's X11 loop and may classify a Super+Space switch late | Fire gates re-read the tracker (safe direction: a late change only skips a word); `PollInterval` tunable; events deferred (Q-X1) |
| K-X2 | `_XKB_RULES_NAMES` order or content differs from the daemon's pair (explicit `layout1`/`layout2`, > 2 sources, non-GNOME WMs that do not set the property) | Mapping by name, not position; `Fallback` to the pair with a warning; `--layout-state` prints the table; MX12 |
| K-X3 | `DISPLAY`/`XAUTHORITY` missing in the user service environment (non-GDM, custom `.xsession`) | F10 + `setupX11Environment` fallback; documented `systemctl --user import-environment DISPLAY XAUTHORITY`; the sources log the connect error and retry |
| K-X4 | Strict mode false negatives: apps without an IBus input context (xterm, Java/SWT, Wine, some Electron builds) are never corrected | Documented; `auto-strict-purpose=false` switches to fail-open with X11 exclusions; tray tooltip explains |
| K-X5 | Strict mode off on X11: password fields of GTK apps look like "no field" (F8) and are then corrected like any text; Qt purposes are best effort | Default strict on X11; exclusion template lists password managers; README warns |
| K-X6 | Two extension trees drift | `check-extension.sh` helper/IFACE equality; FORK.md rebase checklist; shared tests on both files |
| K-X7 | GNOME 43 loads extensions only at login; a user with the ESM tree installed from the RPM on a 43 machine (or vice versa) gets OUT_OF_DATE | Packages ship the matching tree only; tray shows `FocusExtOutOfDate`; `install-extension.sh --legacy` for dev |
| K-X8 | dpkg replacement disables the user unit before our `preinst` (F14) | Marker in `preinst` + `enable --now` in `postinst` for logged-in users; documented heuristic |
| K-X9 | Upstream scriptlet text embedded in `scripts/deb/*.sh` goes stale after a rebase | Build-time identity check against `scripts/{pre,post}install.sh`; checklist item |
| K-X10 | Go 1.26 toolchain only via container; rootless podman bind mount uid/`.git` ownership | `--userns=keep-id`, `safe.directory`, `GOFLAGS=-buildvcs=false`; CI uses the same image |
| K-X11 | `xgb.NewEventFuncs` global map (F11) if someone later adds XKB events | Decision recorded in §3.1.2; any event-based source must register at process start |
| K-X12 | The IBus guard waits on gnome-shell at login; a non-GNOME Wayland session with `layout-source=auto` has no source | Guard with backoff; clear reason `layout-source-unsupported`; `layout-source=ibus` override |
| K-X13 | `_NET_ACTIVE_WINDOW` lags or points at a transient (menus, override-redirect never set it) | Only app identity is derived; a stale identity can only deny (excluded app) or allow a non-excluded app, same as the extension's `window:N` ids |
| K-X14 | Two sources would double-report (future X11 GNOME ≥ 49.3) | Selection never adds `ibus` on X11 with `auto` (§2.1) |
| K-X15 | `policykit-1` is transitional on bookworm; future Debian may drop it | Depends line reviewed per release; `pkexec` is what the tray needs |

Open questions (deferred, default chosen):

| # | Question | Default |
|---|---|---|
| Q-X1 | `XkbSelectEvents`/`StateNotify` instead of polling | Polling (§3.1.2); revisit if xgb gains an XKB package or if latency is noticeable |
| Q-X2 | Should strict mode also require the extension to have reported *this window's* purpose after the last `FocusChanged` (purpose staleness)? | No: the extension emits `focus-out` ⇒ `none` on every IBus focus change, which strict mode already denies |
| Q-X3 | Ship `.deb` for arm64 | No (amd64 only, as the RPM) |
| Q-X4 | `layout-source=x11` on Wayland via Xwayland for non-GNOME compositors | Refused with a warning (F16) |
| Q-X5 | Tray checkbox vs tri-state control for `auto-strict-purpose` | Checkbox that preserves `auto` until toggled (§3.5) |
