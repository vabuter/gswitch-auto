# gswitch-auto — SPEC-X11: Debian 12 / GNOME Shell 43.9 / X11 addendum

Status: implementation contract (addendum to `docs/SPEC.md`), date 2026-10-22. Base: fork HEAD
`7c0de1a` (`chore: ignore agent worktree directory`), i.e. upstream v0.8.0 + 65 fork commits.
Every `file:line` reference below is to that tree unless marked otherwise.
Revision 2 (2026-10-22): review fixes — init order of the platform selection and the
`forkFocusPolicy` signature (§3.4), fail-closed strict default (§3.3), live strict value in
`autoAvailability` (§2.3), X11 source reporting and request bounds (§3.1.2), `auto` ⇒ GNOME-only
on X11 (§2.1), packaging/container details (§3.8), manual script MX4/MX8/MX9 (§6.3).
Revision 3 (2026-10-31, reviewed against HEAD `1124225`, i.e. after the Phase B merge): the
strict test call sites pass `true` (§3.4); a namespace rule for unexported identifiers in
packages shared by parallel WPs (§5, after the `sleepCtx` clash fixed in `e9e75c0`); the
`X11FocusEnabled` rule without a GNOME check (§3.2.1); `Merged`'s `extensionSource` (§3.2.2);
the real start log line (§2, MX1); xgb close/dial bounds and the lost-connection
`WaitForEvent` case (§3.1.2, §3.2.1); the locale layout GNOME appends to `_XKB_RULES_NAMES`
(F18, §3.1.2, MX3/MX12); the apt replacement path (F14, §3.8.2, MX1); `layout-source=ibus`
refused on X11 below 49.3 (X2, §2.1); purpose reset when IBus disconnects (F19, §3.6, Q-X2);
manual checks that are observable on bookworm (F20, §6.3: password entries, debug-only decision
lines, `layout-changed`, the X11 confirmation path, `xterm` on the exclusion list); drifted
line references. Code follow-ups that this revision requires (the spec is ahead of the tree
there; each item goes to the owner of the file): WPX1 `layoutstate/gnome_version.go` new
forced-IBus warning text (§3.1.1); WPX2 `layoutstate/x11_source.go`/`x11_proto.go`
reversed-pair check on the first two entries and an event-queue drain per poll (§3.1.2); WPX4
the `ready(false)` handler in **both** extension trees (§3.6); WPX6 `scripts/deb/preinst.sh`
accepts `config-files` (§3.8.2); WPX7 `runtime/platform.go` refusal of `layout-source=ibus`
in X11 sessions below 49.3 plus its matrix row (§2.1), and the README/FORK.md wording (§3.10).

Revision 4 (2026-10-31, review fixes after the WPX merges): the automatic switch on GNOME X11
(F21, §2.1 note, K-X1, MX6): upstream's `XF86Launch7` has no modifier, so GNOME's input-source
popup applies it only 1.5 s after the last key release and swallows typing meanwhile; the
auto path now switches with a fork-owned `<Shift>XF86Launch7` binding and, for any other
modifier-less binding, presses once and waits past the popup timeout. Strict mode does not
protect XIM or Qt clients on X11 (F22, K-X4, K-X5, §2.2, MX5, MX7, the tooltip of §3.5, the
template of §3.9). The extension's Clutter fallback purpose counts on X11 only while a Shell
entry has the input focus (§3.6). X11 source: the group table is re-read when the group
changes, a Resync fallback is followed by a forced report, connects and replies of both X11
sources are bounded and leave nothing behind, a nonexistent screen number is an error
(§3.1.2, §3.2.1, K-X16). Packaging: `polkitd` + `pkexec` instead of `policykit-1` (F17), the
.deb leaves the build only after its checks, the release is published only with both packages
(§3.8.5); CI runs the gated X11 tests under Xvfb (§6.2).

Revision 5 (2026-10-31, packaging verification on a real `make deb` and a clean `debian:12`
install): the container build mounts the checkout read-only and writes only `builds/deb` and
the output directory (§3.8.3); the deb assertions also require `"43"` in the packaged
`metadata.json` `shell-version`, and `build-rpm.sh` asserts that the RPM ships the ESM tree
byte for byte (§3.8.3).

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
| X2 | IBus layout source only when `org.gnome.Shell` `ShellVersion` ≥ 49.3; version unreadable ⇒ no IBus source *yet* (a guarded source keeps probing, §3.1.1); `layout-source=ibus` is an explicit override with a warning — except in an X11 session whose probe reports < 49.3, where it is refused (`layout-source-unsupported`): the XKB source exists there, and a forced IBus source is harmful, not merely useless (§2.1, rev. 3). | §3.1.1, §3.4, §2.1 |
| X3 | New `layoutstate` source for X11: XKB group via a dedicated xgb connection; engine names synthesized as `xkb:<layout>:<variant>:` so `Tracker.pairIndex` is unchanged; group index = order of `org.gnome.desktop.input-sources sources` (= the daemon's pair order); enabled in X11 sessions. **Architect's pick: polling `XkbGetState` every 50 ms, not `XkbSelectEvents`** — justification in §3.1.2. | §3.1.2 |
| X4 | New `focus` source for X11 (EWMH): `PropertyNotify` on the root for `_NET_ACTIVE_WINDOW`, `WM_CLASS` class part and `_GTK_APPLICATION_ID`; merged with the extension client; `fold.AppKey` for matching. | §3.2 |
| X5 | Legacy (non-ESM) extension variant for GNOME 43/44 with the same UUID and D-Bus interface under `shell-extension/legacy/<uuid>/`; the RPM ships the ESM tree, the Debian 12 `.deb` ships the legacy tree; `check-extension.sh` validates both. | §3.6 |
| X6 | Strict purpose mode: key **`auto-strict-purpose`** (`auto` \| `true` \| `false`; `auto` = true unless the session is **positively Wayland** (`WAYLAND_DISPLAY` set or `XDG_SESSION_TYPE=wayland`), i.e. true on X11 *and* when the session type is unknown — fail-closed); the effective value is resolved before any policy or tracker is built (§3.4) and is hot-reloadable; auto-correction fires only when the extension reports a known non-password text purpose; extension absent in strict mode ⇒ `AutoAvailable=false`, reason `focus-extension-required`, tray hint. | §3.3, §3.2.4, §3.5 |
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
| F10 | gnome-session 43 exports `DISPLAY`/`XAUTHORITY`/`XDG_SESSION_TYPE` to `systemd --user` (`gsm_util_export_user_environment` exports everything except `NOTIFY_SOCKET`/`XDG_SEAT`/`XDG_SESSION_ID`/`XDG_VTNR`; Debian's `/etc/X11/Xsession.d/95dbus_update-activation-env` runs `dbus-update-activation-environment --systemd --all`); `setupX11Environment()` (`runtime/x11.go:16-35`) fills `DISPLAY`/`XAUTHORITY` when missing (there is no fallback for `XDG_SESSION_TYPE`, K-X3). The user unit allows `/tmp/.X11-unix` (`configs/gswitch.service:26-29`: `ReadWritePaths=/tmp`, `PrivateTmp=false`; `RestrictAddressFamilies=AF_UNIX AF_NETLINK`). | R, V (tree) |
| F11 | jezek/xgb v1.3.1 has no XKB sub-package; unknown extension events are dropped with a log line unless a constructor is registered in the **process-global, unlocked** map `xgb.NewEventFuncs` (`xgb.go:197`, read in every connection's reader goroutine at `xgb.go:557`); the extension event base is only known after `QueryExtension`. | V (module cache) |
| F12 | `golang.design/x/clipboard` (vendored in `third_party/clipboard`) talks to X11 through `dlopen("libX11.so.6")` (`clipboard_linux.c:41-50`); no `xclip`/`xsel` needed. Built daemon links only `libc`/`libresolv`; the tray links GTK3, GDK, GLib/GIO/GObject, cairo, pango, gdk-pixbuf, atk, harfbuzz, fontconfig, libX11 (`readelf -d builds/*`). | V (tree, host) |
| F13 | nfpm v2.48.0 `Overridables` include `scripts` and `contents`; `contents[].packager: rpm|deb` filters entries; RPM-only content types (`license`, `doc`) are skipped for deb (`nfpm.go:359-375`, `files/files.go:52-63`). | V (module cache) |
| F14 | Replacement order (Conflicts+Replaces+Provides) depends on the front end. `dpkg -i gswitch-auto_….deb` over upstream: the upstream package's `prerm remove in-favour gswitch-auto <ver>` runs **before** our `preinst install`, and `dpkg-query` then reports upstream as `half-configured`/`half-installed`. `apt install ./gswitch-auto_….deb` (the documented command, MX1): apt removes `gswitch` in a separate dpkg run first (`Removing gswitch` is logged before `Unpacking gswitch-auto`), upstream's prerm gets a plain `remove`, and our `preinst install` sees upstream as **`config-files`**. Either way upstream `preremove.sh` has disabled and stopped the user unit (`scripts/preremove.sh:17-40`). There is no `%pretrans` equivalent on deb. | R; apt path V (review, throwaway `debian:12` container, `--network=none`, dummy packages with the same relations and a conffile) |
| F15 | Debian 12 ships Go 1.19 (backports 1.23); `go.mod` needs `go 1.25.5` ⇒ build in `docker.io/library/golang:1.26-bookworm` (image present locally; `podman` 5.8.9; `debian:12` image present). Binaries built on Fedora need `GLIBC_2.34` at most (ok on glibc 2.36) but official builds go through the container. | R, H (`podman images`) |
| F16 | Host (Fedora 44, GNOME 50.6, Wayland): Xwayland on `DISPLAY=:0` with `_XKB_RULES_NAMES = "evdev","pc105","us","",""` and a live `_NET_ACTIVE_WINDOW`; `setxkbmap -query` warns "Running setxkbmap against an Xwayland server". ⇒ read-only X11 smoke tests run here, but group **changes** cannot be observed here (Xwayland sees one layout). | H |
| F17 | Debian 12 packages: `libgtk-3-0`, `libx11-6`, `libglib2.0-bin` (gsettings), `polkitd` + `pkexec` (not the transitional `policykit-1`: bookworm's lintian reports `depends-on-obsolete-package`, an error), `x11-xkb-utils` (setxkbmap), `gnome-shell-extension-appindicator`, `wl-clipboard`, `desktop-file-utils`, `libgtk-3-bin` (gtk-update-icon-cache). | R (assessment) |
| F18 | GNOME Shell 43.9 `js/misc/keyboardManager.js` `_buildGroupStrings(_group)` sets the keymap from `_group.concat(this._localeLayoutInfo)` (lines 144-148): the layout of the UI locale is appended to every group (for mnemonics such as Alt+Ф), and `apply` only ever locks indices inside the user's sources (`setUserLayouts`, ≤ 3 per group). ⇒ On the Debian machine `_XKB_RULES_NAMES` most likely reads layouts `us,ru,us` (en_US) or `us,ru,ru` (ru_RU) with variants `,,`; groups 0 and 1 follow `sources`, the third group is never locked. Exact value to be recorded in MX3. | V (raw 43.9) |
| F19 | GNOME Shell `js/misc/ibusManager.js` `_clear()` (43.9 lines 142-164, 50.6 lines 137-159), run when the IBus connection is lost or `ibus restart`/ibus-setup replaces the daemon, emits only `'ready'(false)` — no `'focus-out'`; `_updateReadiness` emits `'ready'(bool)` too. | V (raw 43.9, 50.6) |
| F20 | The auto engine's decision lines (`auto: arm|fire|skip reason=<reason> keys=<n>`, `auto.go:728-749`; a focus denial is `reason=focus:<policy reason>`, `auto.go:297-299`) go through `logDebug` (`auto_control.go:118`), which prints only with `--debug` (`switcher.go:381-386`); the service (`ExecStart=/usr/bin/gswitch --run`) writes only `s.log` lines (the start summary, warnings) to the journal. Upstream's `Auto-detected layout switch keys: <names> ([186]) from gnome` is debug-only as well (`switcher.go:183-186`), and the X11 confirmation branch logs nothing on success (`switcher.go:953-974`; only the error `GNOME X11 layout switch confirmation: …`). | V (tree) |
| F21 | GNOME Shell 43.9 `keyboard.js` `_switchInputSource` shows an `InputSourcePopup`; `switcherPopup.js` `show()` finishes at once when the binding's primary modifier is already released and otherwise on its release (`vfunc_key_release_event`), but for a binding **without** a modifier (mask 0, e.g. upstream's `XF86Launch7`) it calls `_resetNoModsTimeout()` (`NO_MODS_TIMEOUT` = 1500 ms) and every key release restarts it; while open, its modal grab swallows every key, Space/Return apply the selection at once, Escape/Tab cancel, and a second press of the binding moves the selection back to the current source. | V (raw 43.9 `switcherPopup.js`, `keyboard.js`) |
| F22 | ibus 1.5.27: `bus_panel_proxy_focus_in` pushes the context's stored content type to the panel on every `FocusIn` — `(FREE_FORM, 0)` when the client never set one. ibus-x11 (XIM; GNOME 43 starts `ibus-daemon --xim` on X11) never calls `set_content_type`; Qt 5.15/6.4's IBus plugin returns early from `setFocusObject` for a password `QLineEdit` (no `FocusOut`, no `SetContentType`). ⇒ XIM clients (xterm, urxvt, Emacs, Java, Wine) and Qt password fields are reported as `normal`. | V (raw ibus 1.5.27, qtbase 5.15/6.4) |

---

## 2. Source selection matrix

Selection runs once in `initFork` (`runtime/fork.go:116`), after `initLayoutConverter` resolved
the pair (`runtime/switcher.go:289`) and **immediately after `loadUserConfig`
(`fork.go:118-119`), before the focus policy is stored (`fork.go:120`) and before
`initTracker`** — the exact order is normative, §3.4. Inputs: `s.sessionEnv` (nil for a user
service ⇒ process environment), `userconfig.Settings`, `forkGeteuid()`, and the GNOME Shell
version probe through the package hook `forkProbeShellVersion` (= `layoutstate.ProbeShellVersion`,
2 s; §3.4). The result is stored in `s.platform` and logged by `initPlatform` (§3.4): first
each `Warning: <warning>`, then the `LayoutNote` when set, then one summary line in the format
of `PlatformSelection.Summary` (`runtime/platform.go:139-170`):
`layout sources: <list>[ (GNOME Shell <version>)]; focus sources: <list>; strict purpose: <bool>`
— no space before `;`; `<list>` is comma-separated (`x11`, `ibus`, `ibus (guarded)`), `none
(<LayoutReason>)` resp. `none` when empty; the version suffix appears whenever the probe ran
and succeeded (also with the `x11` source on X11 GNOME). Example on the Debian machine:
`layout sources: x11 (GNOME Shell 43.9); focus sources: extension, x11; strict purpose: true`.
The strict value printed there is the start value `cfg.Settings.StrictPurposeFor(sel.Wayland)`;
the live value is the policy's (§2.3).

### 2.1 Layout tracking

"GNOME" = `detect.IsGNOMESession(env)`. "≥49.3" = probe ok and `IBusEngineTracksLayout`.
"guarded" = `GuardedIBusSource` (§3.1.1): the IBus source starts only after the probe succeeds
with ≥ 49.3; while the probe keeps failing the tracker stays Unknown.

| Session | Shell version | `layout-source` | Sources added to the tracker | Resync | Auto confirm path | Manual path (unchanged upstream) | `AutoUnavailableReason` while idle |
|---|---|---|---|---|---|---|---|
| Wayland GNOME | ≥ 49.3 | `auto` | `ibus` | gsettings | tracker reader (`emitOptions.confirm`) | composite reader → GNOME Wayland verify | `""` once Confirmed, else `layout-unknown` |
| Wayland GNOME | < 49.3 (probe ok) | `auto` | none ⇒ `tracker == nil` | — | — | upstream gsettings verify | `layout-source-unsupported` |
| Wayland GNOME | probe failed | `auto` | `ibus` **guarded** | gsettings | tracker reader | composite reader | `layout-unknown` until the probe passes (then as row 1 or, if < 49.3, the guard logs and the tracker stays Unknown ⇒ `layout-unknown`) |
| Wayland GNOME | any | `ibus` | `ibus` unguarded; a warning only when the probe failed (`…GNOME Shell version unknown (%v)`) or reports < 49.3 (the fixed-engine warning of §3.1.1, which says the forced source is harmful); none at ≥ 49.3 — exactly `IBusSourceAllowed` | gsettings | tracker reader | composite reader | as row 1 |
| X11 GNOME (Debian 12) | any | `auto` | `x11` (never `ibus`, see note) | gsettings | tracker reader | **upstream X11 verify** (`verifyGNOMEX11LayoutSwitch`, raw XKB reader) | `""` once Confirmed, else `layout-unknown` |
| X11 GNOME | any | `x11` | `x11` | gsettings | tracker reader | upstream X11 verify | as above |
| X11 GNOME | ≥ 49.3 | `ibus` | `ibus` unguarded, no warning (the engine tracks the layout) | gsettings | tracker reader | upstream X11 verify | `""`/`layout-unknown` |
| X11 GNOME | < 49.3 (probe ok; the Debian machine) | `ibus` | **none ⇒ `tracker == nil`** (refused, rev. 3) + warning `layout-source=ibus is refused in an X11 session: GNOME Shell %s reports the fixed engine xkb:us::eng below 49.3; use layout-source=auto or x11` (instead of `IBusSourceAllowed`'s warning) | — | — | upstream X11 verify | `layout-source-unsupported` |
| X11 GNOME | probe failed | `ibus` | `ibus` unguarded + the version-unknown warning (§3.1.1) | gsettings | tracker reader | upstream X11 verify | `""`/`layout-unknown` |
| X11 non-GNOME (KDE/awesome/other) | n/a | `auto` | none ⇒ `tracker == nil` | — | — | upstream blind replay (SPEC D8, §3.10.2: exactly the upstream code) | `layout-source-unsupported` |
| X11 non-GNOME | n/a | `x11` (explicit opt-in) | `x11` | none | tracker reader | upstream blind replay | `""`/`layout-unknown`; best effort, untested |
| Wayland non-GNOME | n/a | `auto` | none | — | — | upstream | `layout-source-unsupported` |
| any | any | `none` | none | — | — | upstream | `layout-source-off` |
| root compatibility mode | any | any | none | — | — | upstream | `root-mode` |

Notes:
- `auto` adds the `x11` source only in **GNOME** X11 sessions, mirroring `IBusSourceEnabled`
  (`ibus_source.go:86-98`: `auto` ⇒ `detect.IsGNOMESession`). SPEC.md D8 and §3.10.2 ("KDE/X11
  without GNOME ⇒ `tracker == nil` ⇒ exactly the upstream conversion code") therefore keep
  their wording; the corrections ledger is not amended. Non-GNOME X11 needs the explicit
  `layout-source=x11`, which installs the tracker-dependent hooks (`layoutReaderFn`,
  `onOwnSwitch`, chord-flip restore) and is labelled best effort.
- Two push sources are never run together: `Tracker.setLocked` (`layoutstate/tracker.go:430-441`)
  treats a different `Source` as a change, so alternating `ibus`/`x11` reports would bump the
  generation and cancel words. On X11 the XKB group is authoritative whatever the IBus engine
  says.
- A forced IBus source below 49.3 is **harmful, not merely useless**: the engine is
  `xkb:us::eng` in every layout (F2), so the tracker reports index 0 as `Confirmed` forever and
  auto mode stays "available". A correct word typed in the second layout (keys `ghbdtn` while
  the real layout is ru, screen `привет`) is rendered as `ghbdtn` in the assumed layout and
  judged a wrong-layout word; the fix then toggles the layout and waits for a tracker change
  that never comes (`gnome_wayland_layout_switch.go`, 2 attempts × 20 polls × 50 ms): the
  replay is aborted, but the layout flips twice within ~2 s, and keys typed meanwhile land in
  the wrong layout. Hence the refusal on X11 (the `x11` source is the correct choice there and
  `auto` selects it) and the explicit warning text on Wayland, where the override is kept for a
  distribution that backports the 49.3 engine behaviour under an older version string.
  `selectPlatform` implements the refusal (WPX7, `runtime/platform.go` `selectLayout`): in an
  X11 session, `layout-source=ibus` with `IBusSourceAllowed` reason `ibus-engine-static`
  ⇒ `LayoutReason = layout-source-unsupported`, the refusal warning of the table, no IBus
  source; `IBusSourceAllowed` itself (WPX1) is unchanged apart from its warning text.
- `layout-source=x11` on Wayland is accepted by the parser but refused at selection with warning
  `layout-source=x11 needs an X11 session` ⇒ `layout-source-unsupported` (Xwayland never sees
  GNOME's group changes, F16).
- The auto path keeps C26: `autoEmit` (`runtime/auto_control.go`) confirms through
  `trackerLayoutStateReader` on every platform. How it triggers the switch is the binding's
  `switchPlan` (`runtime/auto_switch.go`, rev. 4, F21): upstream's GNOME X11 accelerator
  `XF86Launch7` has no modifier, so GNOME's popup would apply it only 1.5 s after the key
  release, swallow the user's typing meanwhile, and a second press (the Wayland confirm's
  attempt 2 after 1 s) would move the selection back — every automatic fix and undo failed.
  Therefore, when the layout switch is upstream's `XF86Launch7` (`verifyGNOMEX11LayoutSwitch`,
  LSKeys `[186]`) and a tracker exists, `initFork` adds the fork-owned accelerator
  `<Shift>XF86Launch7` to `switch-input-source` (`detect.EnsureGNOMEX11ShiftLaunch7Binding`,
  persistent like upstream's `XF86Launch7`, verified, rolled back on failure), and the auto
  path and the chord-flip restore emit Shift+F16: the popup finishes on the Shift release
  within milliseconds; confirmation 2 × 20 × 50 ms as on Wayland. Any other modifier-less
  GNOME binding (a single key; or the Shift binding could not be added) gets one press and one
  2.5 s window (`NO_MODS_TIMEOUT` + 1 s), never a second press, and the input queued until the
  confirmation ends is discarded (`discardSwallowedInput`: drain + `afterInputDiscarded`),
  because GNOME swallowed it. Bindings with a modifier (Fedora's `<Alt>Shift_L`) are
  unchanged. On X11 the tracker lags the group change by at most one poll (≤ 50 ms) (K-X1).
  The manual path is upstream's and unchanged (its 200 × 10 ms per attempt outlasts the
  popup).
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
`focus.Merged` (§3.2.2). "strict" = the **live** policy's `RequirePurpose`
(`s.focusPolicy.Load().RequirePurpose`, hot-reloaded; §3.3, §3.4) — never a value computed
once at start.

| Session | `focus-source` | Extension owned | strict (effective) | Sources started | IPC `FocusSource` | Behaviour |
|---|---|---|---|---|---|---|
| Wayland GNOME | `auto` | yes | `false` (default) | ext | `active` | SPEC.md behaviour, unchanged |
| Wayland GNOME | `auto` | no | `false` | ext (absent) | `absent` | fail-open unless `auto-require-focus` (unchanged) |
| Wayland GNOME | `auto` | yes | `true` (explicit) | ext | `active` | fires only with a known text purpose (§3.2.4); `none`/`unknown` ⇒ `purpose-unknown` |
| Wayland GNOME | `auto` | no | `true` | ext | `absent` | `AutoAvailable=false`, `focus-extension-required`; engine denies `focus-unavailable` |
| X11 GNOME (Debian 12) | `auto` | yes | `true` (default) | ext + x11 | `active` | app identity and purpose from ext; fires only with a known text purpose; x11 identity is a fallback while ext is absent. XIM clients and Qt password fields report `normal` (F22): only the exclusion list protects them (K-X4) |
| X11 GNOME | `auto` | no (not enabled / relogin pending / disabled) | `true` | ext (absent) + x11 | `x11` | `AutoAvailable=false`, reason `focus-extension-required`; tray hint; the policy reason is `focus-unavailable` (§3.2.4 returns it before the excluded-app check — exclusions are **not** evaluated); `--focus-state` and the IPC status still print the x11 identity (`FocusedApp`/`FocusedWmClass`) |
| X11 GNOME | `auto` | no | `false` (explicit) | ext (absent) + x11 | `x11` | fail-open; exclusions by `WM_CLASS`/`_GTK_APPLICATION_ID`; password/terminal purposes unknown ⇒ only the exclusion list protects (documented, §7 K-X5) |
| X11 non-GNOME | `auto` | n/a | `true` (default) | x11 | `x11` | with `layout-source=auto` there is no tracker, so `layout-source-unsupported` wins (§2.3 precedence); with `layout-source=x11` ⇒ `focus-extension-required` (no purpose source); auto there needs both `layout-source=x11` and `auto-strict-purpose=false` |
| any | `off` | — | `true` | none | `off` | `AutoAvailable=false`, reason `focus-source-off` |
| any | `off` | — | `false` | none | `off` | unchanged: no focus information, everything allowed |
| root compatibility mode | any | — | — | none | `off` | unchanged (`root-mode`) |

### 2.3 `AutoUnavailableReason` codes

Existing: `""`, `degraded`, `no-layout-converter`, `unsupported-layouts`, `layout-source-off`,
`layout-unknown`, `root-mode`, `detector-error` (`runtime/auto_control.go:26-34`). New:

| Code | Set when | Producer |
|---|---|---|
| `layout-source-unsupported` | no layout source exists for this session (Wayland GNOME < 49.3 with `auto`; Wayland non-GNOME; `layout-source=x11` on Wayland or without a display; `layout-source=ibus` in an X11 session with GNOME Shell < 49.3, §2.1) | `buildAutoEngine` (`tracker == nil` and `s.platform.LayoutReason == …`) |
| `focus-source-off` | strict purpose effective and `focus-source=off` | `autoAvailability` |
| `focus-extension-required` | strict purpose effective (live policy), `focus-source=auto`, and the extension is not owned (`!fs.Available`) while not `Locked` | `autoAvailability` |

Precedence in `autoAvailability(ls, fs)` (replaces `runtime/auto_control.go:417-427`), where
`strict := s.focusPolicy.Load().RequirePurpose` (the live policy — `auto-strict-purpose` is
hot-reloadable, so a value computed at start would make `AutoAvailable`/`UnavailableReason`
diverge from the gating actually applied after a tray toggle; `PlatformSelection` carries no
strict flag, §3.4): engine nil ⇒ `s.autoReason`; tracker `Unknown`/`Settings` ⇒
`layout-unknown`; strict && `focus-source=off` ⇒ `focus-source-off`; strict && `!fs.Available
&& !fs.Locked` ⇒ `focus-extension-required`; otherwise available (`Pending` and `Locked` count
as available, as today — a lock shows up as `FocusSuppressedReason=locked`).

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
var ErrShellVersionFormat = errors.New("org.gnome.Shell ShellVersion has an unexpected format")

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
// "auto" ⇒ ok only when info.Err == nil && IBusEngineTracksLayout; deferred is true when
// info.Err != nil (caller wraps the source in GuardedIBusSource). reason is one of
// "", "shell-version-unknown", "ibus-engine-static". (Not "defer_": golangci-lint's
// var-naming would flag the underscore.) Warnings for "ibus": version unknown ⇒
// "layout-source=ibus forces the IBus source; GNOME Shell version unknown (%v)"; < 49.3 ⇒
// "layout-source=ibus forces the IBus source although GNOME Shell %s reports a fixed engine
// below 49.3: every layout reads as the first one, so automatic correction misjudges words
// typed in the second layout" (rev. 3; it says harmful, not useless, §2.1 note). selectPlatform
// replaces the second one by its refusal warning in X11 sessions (§2.1).
func IBusSourceAllowed(layoutSource string, info ShellInfo) (ok, deferred bool, reason, warning string)

// GuardedIBusSource runs inner only after ProbeShellVersion succeeds with a tracking version.
// Run: probe (timeout 2 s) with backoff 1 s..30 s until ctx is done; refused (< 49.3) ⇒ log
// once "IBus layout source disabled: GNOME Shell %s reports a fixed engine for XKB sources
// (needs 49.3+)" and return without ever calling sink.Available(inner.Name(), true)
// (layoutstate.Sink is Engine(source, name string) / Available(source string, ok bool),
// tracker.go:112-115); passed ⇒ inner.Run(ctx, sink) with the same sink.
// Query delegates to inner only once passed, else returns ErrShellUnavailable wrapped.
// Name returns inner.Name() ("ibus"). State() reports pending|passed|refused for --layout-state.
// sleep's production value is the package helper sleepCtx (gnome_version.go): WPX1 owns it;
// the X11 source of §3.1.2 references it after the merge and never redeclares it (§5 rule).
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
5. Every request is bounded. xgb drops X errors whose constructor is not registered
   (`xgb.go:520-527` logs "BUG: Could not find error constructor" and continues), so an XKB
   error such as `BadKeyboard` (`errorBase+0`, no constructor for the XKB extension) would
   leave `cookie.Reply()` blocked forever and the poll goroutine hung while the tracker stays
   `Confirmed` on a stale index. It cannot happen with `deviceSpec = UseCoreKbd`, but the
   safety argument must not depend on the X server answering: `X11Source` waits for every
   `XkbGetState`/`GetProperty` reply in a goroutine and selects on it with `ReplyTimeout`
   (1 s); a timeout ⇒ `sink.Available(SourceX11, false)`, close the connection and reconnect
   with the usual backoff. Closing does **not** unblock anything while the server hangs:
   xgb v1.3.1 `Conn.Close` only queues a nil request, and the sender goroutine then runs
   `noop()` — a `GetInputFocus` round trip — before it closes the socket (`xgb.go:166-171,
   422-430, 461-470`). Therefore (as implemented in `layoutstate/x11_proto.go`): the waiter
   goroutine is abandoned (buffered result channel, `awaitReply`); `xgbConn.Close` waits at
   most `x11CloseTimeout` (1 s); and the dial — `xgb.NewConnDisplay`, i.e. connect plus the
   setup handshake, itself unbounded — runs in a goroutine bounded by ctx and
   `x11ConnectTimeout` (2 s) (`dialX11`), for `Run` **and** for the temporary connection of
   `Query` alike (`Query` dials through the same `connect`). Rev. 4 (Q-X7 resolved): the
   abandoned dial goroutine and socket leaked once per reconnect attempt against a server
   that accepts but never answers, so both X11 sources now dial through the fork package
   `internal/gswitch/x11conn`: it parses `DISPLAY` and looks the MIT-MAGIC-COOKIE-1 up
   exactly like xgb, dials the socket with `net.Dialer.DialContext`, hands it to
   `xgb.NewConnNetWithCookieHex` (`NewConnNet` without an entry) and moves the socket
   deadline into the past when ctx ends, so the setup runs on the caller's goroutine and
   leaves nothing behind. Its `Close` waits at most `x11conn.CloseTimeout` (500 ms) for xgb's
   graceful shutdown and then closes the socket itself, which ends xgb's goroutines and every
   abandoned reply. `DefaultRoot` rejects a screen number the server does not have
   (`DISPLAY=:0.1` used to panic in `xproto.SetupInfo.DefaultScreen`). Rev. 4 also re-reads
   `_XKB_RULES_NAMES` whenever the group differs from the previous poll or the poll drained
   queued events (a keymap upload sends `MappingNotify`; mutter writes the property before
   it locks the group), so a reordered source list is never named through the stale table;
   the periodic re-read stays as a backstop. `Query`'s wait for the poll's request mutex is
   bounded by its ctx, and after a `Resync` whose sources all failed (fallback `Settings` or
   `Unknown`) the tracker calls `RequestReport`, so the next identical poll reports its
   engine again instead of leaving the fallback in place until the next real switch.
6. The source selects no events and sends only checked requests, so its xgb event queue should
   stay empty; but xgb puts every event and every unchecked error into a 5000-slot channel and
   its reader goroutine blocks when that is full, which would stall replies until
   `ReplyTimeout` and a reconnect. Cheap hardening (rev. 3, WPX2): once per poll the source
   drains the queue with `PollForEvent` (non-blocking, `xgb.go:657-664`) until it returns
   `(nil, nil)`, logging the first drained item per connection (the `x11Conn` seam gains
   `DrainEvents() int`).

```go
const SourceX11 = "x11" // State.Source value

// X11Options configures NewX11Source.
type X11Options struct {
	Display      string        // "" ⇒ $DISPLAY (xgb.NewConnDisplay(""))
	PollInterval time.Duration // 50 ms default; clamped to 20 ms..500 ms
	Fallback     [2]Layout     // group i ⇒ Fallback[i] when _XKB_RULES_NAMES is unusable (the daemon's pair)
	ReplyTimeout time.Duration // 1 s default; bound on every reply (justification 5 above)
	Logf         func(string, ...any)
	// Test hooks (production values set by NewX11Source): dial opens a conn, sleep paces retries.
	// sleep's production value is WPX1's sleepCtx (gnome_version.go), referenced, never
	// redeclared; a WPX2 running in the same phase as WPX1 would declare x11SleepCtx (§5).
}

// X11Source implements Source. Run: connect (xgb.NewConnDisplay), QueryExtension("XKEYBOARD"),
// XkbUseExtension(1.0), read _XKB_RULES_NAMES (root, STRING) ⇒ group table, XkbGetState ⇒
// sink.Available(SourceX11, true) + sink.Engine(SourceX11, EngineForGroup(table, group));
// then every PollInterval: XkbGetState (+ re-read _XKB_RULES_NAMES every 40th poll ≈ 2 s,
// whenever group >= len(table), the group changed or queued events were drained; rev. 4). Engine is reported ONLY on connect and when the derived
// engine name changes (group or table changed); the source keeps the last reported name and
// identical polls are silent. Reason: Tracker.Engine (tracker.go:267-278) calls
// cancelHintLocked unconditionally before setLocked and engineState always yields Confirmed,
// so a repeated report would flip a Pending state (NoteSwitchHint) back to Confirmed with a
// generation bump — the hint window would be lost and onLayoutState would see a spurious
// external change. (Irrelevant on the Debian target, whose LSKeys [186] never hint, but wrong
// on any X11 setup with an observable chord.) Any X error, EOF or reply timeout ⇒
// sink.Available(SourceX11, false), close, reconnect with backoff 200 ms..5 s (ibusBackoff*
// constants); after a reconnect the first poll reports Engine again (the tracker is Unknown).
// Query: XkbGetState on the live connection (mutex) or on a temporary one, bounded by ctx and
// ReplyTimeout; the engine name is derived from the same table. Name: SourceX11.
type X11Source struct{ /* ... */ }
func NewX11Source(opts X11Options) *X11Source
func (s *X11Source) Name() string
func (s *X11Source) Run(ctx context.Context, sink Sink)
func (s *X11Source) Query(ctx context.Context) (string, error)

// X11SourceEnabled: layoutSource "x11" ⇒ true when an X display is known (env.Display or
// $DISPLAY) and the session is not Wayland; "auto" ⇒ true only in an X11 **GNOME** session
// (the shouldVerifyGNOMEX11LayoutSwitch rule — SessionType x11 or Display set, and no
// Wayland — AND detect.IsGNOMESession(env), mirroring IBusSourceEnabled; §2.1 note); never
// for euid 0; "ibus"/"none" ⇒ false.
func X11SourceEnabled(layoutSource string, env *detect.SessionEnv, euid int) bool

// EngineForGroup returns "xkb:<name>:<variant>:" for group (0-based) or ok=false when the group
// is outside the table (the tracker then sees Engine("") ⇒ Unknown; the next poll re-reads the
// table). ParseXKBEngine (tracker.go:526) accepts the trailing-colon form (lang part empty).
func EngineForGroup(table []Layout, group int) (engine string, ok bool)

// ParseXKBRulesNames splits the _XKB_RULES_NAMES value (five NUL-terminated strings: rules,
// model, layouts, variants, options) into the group table: layouts "us,ru" + variants ","
// ⇒ [{us ""},{ru ""}]; "us,ru"+"dvorak," ⇒ [{us dvorak},{ru ""}]; "us,ru,us"+",," (GNOME's
// appended locale layout, F18) ⇒ [{us ""},{ru ""},{us ""}]; a missing variants field is
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
`Warning: X11 layout table us,ru,us differs from the configured pair ru,us` when the **first
two** table entries hold the names of `Fallback` in the opposite order (`len(table) >= 2`; rev.
3 — GNOME appends the locale layout as a third, never-locked entry, F18, so a check on
`len(table) == 2` as in `x11_source.go:253`/`reversedPair` never fires on GNOME; WPX2
follow-up). Pair mapping is still by name, so correction stays right — only `--layout-state`
output and the warning are informational. When `_XKB_RULES_NAMES` is missing or has fewer than
two layouts the table is `Fallback[:]` and a warning is logged once per connection. A third
entry maps by name like the others (`us` ⇒ index 0); GNOME never locks it.

`x11_diag.go`: `func RunX11Diagnostics(ctx context.Context, w io.Writer, display string, layouts [2]Layout, watch time.Duration) error`
— prints `Display`, XKB extension opcode/version, `_XKB_RULES_NAMES` table, current group and
its engine/index (via `describeEngine`, `diag.go:74`), then streams tracker changes for `watch`
with its own `watchX11Diagnostics` (a `Tracker` on the X11 source); `watchDiagnostics`
(`diag.go:58`) takes `*IBusSource` and is not reused. The existing `RunDiagnostics` (IBus) is unchanged;
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
// X11FocusEnabled: focus-source "auto" in an X11 session by the §0 rule alone — env non-nil:
// SessionType x11 or Display set, and neither SessionType wayland nor WaylandDisplay; env nil:
// XDG_SESSION_TYPE=x11 and WAYLAND_DISPLAY empty — with NO GNOME check (unlike
// layoutstate.X11SourceEnabled("auto"), which also requires detect.IsGNOMESession), so the
// §2.2 row "X11 non-GNOME" starts it; never for euid 0. (focus/x11_source.go:280-296.)
func X11FocusEnabled(focusSource string, env *detect.SessionEnv, euid int) bool
// ParseWMClass returns the class part of a WM_CLASS value; "" when there is no second string.
func ParseWMClass(value []byte) string
```

Events on the root also include other PropertyNotify atoms (`_NET_CLIENT_LIST_STACKING`,
`_NET_CURRENT_DESKTOP`); they are ignored. `WaitForEvent` returns `(nil, nil)` whenever the
connection is closed — after our own `Close` **and** after a lost connection: xgb's
`processEventOrError` maps a socket read error and a closed event channel alike to `(nil, nil)`
(`xgb.go:625-655`). `(nil, nil)` therefore never means "stop" by itself: the connection
wrapper maps it to `io.EOF`, and the loop first checks the source's ctx (cancelled by
`Close`) — done ⇒ exit; not done ⇒ the connection was lost (X server restart, session end):
`Connected=false` (one `OnChange`) and reconnect with backoff, so a server restart never ends
the source silently (`focus/x11_source.go:165-189, 408-421`). `xgb.Error` values (BadWindow
during the reads) are handled per request through checked cookies; an X error delivered by
`WaitForEvent` itself is logged and ignored. The dial runs on the source goroutine; `Close`
waits at most `x11CloseTimeout` (1 s) for it.

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
// extensionSource is what Merged drives on the extension side (*Client): focus.Source has
// only Snapshot(), which cannot support Merged.Start/Close.
type extensionSource interface {
	Source
	Start(context.Context)
	Close()
}
type Merged struct{ /* ext extensionSource (may be nil); x11 *X11Source (may be nil); state atomic.Pointer[mergedState]; mu, closed */ }
func NewMerged(opts MergedOptions) *Merged
func (m *Merged) SetExtension(c *Client) // `if c != nil { m.ext = c }`: a nil *Client stored in the
                                         // `ext extensionSource` interface field would be a non-nil
                                         // interface holding a typed nil (the classic trap); same guard in SetX11
func (m *Merged) SetX11(s *X11Source)    // `if s != nil { m.x11 = s }`
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
| `auto-strict-purpose` | `auto` \| `true` \| `false` | `auto` (⇒ true unless the session is positively Wayland: X11 sessions and unknown session types are strict, fail-closed) | `focus.Policy.RequirePurpose` (and, through the live policy, `autoAvailability`) | yes |
| `layout-source` | `auto` \| `ibus` \| **`x11`** \| `none` | `auto` | source selection (§2.1) | restart |

```go
const KeyStrictPurpose = "auto-strict-purpose"
const ( StrictAuto = "auto"; StrictOn = "true"; StrictOff = "false" )
const SourceX11 = "x11" // layout-source value
// Settings gains: StrictPurpose string (canonical "auto"|"true"|"false").
// StrictPurposeFor resolves the setting for a session: "auto" ⇒ !wayland, where wayland is
// "positively Wayland" (WAYLAND_DISPLAY set or XDG_SESSION_TYPE=wayland; SessionEnv fields when
// non-nil, else the process environment — PlatformSelection.Wayland, §3.4). The parameter is
// deliberately "wayland", not "x11": with a nil SessionEnv (user service) the X11 rule needs
// XDG_SESSION_TYPE=x11 in the systemd --user environment, and a missing variable must select
// strict, never fail-open (K-X3). On the Fedora host WAYLAND_DISPLAY is exported, so "auto"
// stays false there.
func (s Settings) StrictPurposeFor(wayland bool) bool
```

Parsing: a dedicated `strictField` accepts `auto` plus every `parseBool` spelling (`yes/on/1`
⇒ `true`, `no/off/0` ⇒ `false`), formats canonically; `Validate`, `Equal`, `Values`,
`DiffSettings`, `Keys` include it; `DefaultSettings().StrictPurpose == StrictAuto`. Template
`templates/auto.conf` gets, after the focus-rules block (`auto.conf:40-46`):

```
# Strict purpose mode: correct only in text fields whose type the focus
# extension reports (never in fields it cannot see). auto = off on
# Wayland, on everywhere else (X11, or unknown session type). Without
# the extension, strict mode makes automatic correction unavailable.
# auto-strict-purpose=auto
```

and the sources comment (`auto.conf:58-61`) lists `layout-source auto|ibus|x11|none`.

### 3.4 Runtime glue (WPX7) — `runtime/{fork.go,layout_glue.go,focus_glue.go,auto_control.go,platform.go}`

New file `runtime/platform.go`:

```go
// PlatformSelection is the result of selectPlatform (one per daemon start; IPC ConfigWarnings
// repeat Warnings). The type is exported only for PlatformForDiagnostics (§3.7); it is
// constructed by selectPlatform alone and carries NO strict flag: the effective
// auto-strict-purpose lives in the focus policy only (hot-reloadable, §2.3).
type PlatformSelection struct {
	X11Session   bool            // X11 session rule of §0 (positively X11)
	Wayland      bool            // positively Wayland: WAYLAND_DISPLAY set or XDG_SESSION_TYPE=wayland (input of StrictPurposeFor)
	GNOME        bool            // detect.IsGNOMESession
	Shell        layoutstate.ShellInfo
	LayoutIBus   bool            // add the IBus source
	LayoutIBusGuarded bool       // wrap it in GuardedIBusSource
	LayoutX11    bool            // add the X11 source
	LayoutReason string          // "" | "layout-source-off" | "layout-source-unsupported" | "root-mode"
	LayoutNote   string          // one log line explaining a disabled or deferred layout source, "" otherwise
	FocusExt     bool            // start the extension client
	FocusX11     bool            // start the EWMH source
	Warnings     []string
}
// selectPlatform is pure given its inputs (unit-tested matrix, §6.1). probe is the
// forkProbeShellVersion hook (below) in production and a fake in tests.
func selectPlatform(st userconfig.Settings, env *SessionEnv, euid int, probe func(context.Context) layoutstate.ShellInfo) PlatformSelection
// isX11Session implements the §0 rule (shared with shouldVerifyGNOMEX11LayoutSwitch's logic; the
// upstream function stays untouched). isWaylandSession is "positively Wayland" (§3.3).
func isX11Session(env *SessionEnv) bool
func isWaylandSession(env *SessionEnv) bool

// In layout_glue.go (introduced by WPX1, reused by WPX7 as selectPlatform's probe):
var forkProbeShellVersion = layoutstate.ProbeShellVersion // test seam, like forkGeteuid
```

`forkState` (`fork.go:68-110`) gains `platform PlatformSelection`, `focusMerged *focus.Merged`,
`focusX11 *focus.X11Source`; `focusClient *focus.Client` (`fork.go:93`) stays (nil when the
extension client is not started).

**Init order (normative).** `initFork` (`fork.go:116-134`) becomes:

```go
s.controlChan = make(chan func(), forkControlQueue)
cfg := s.loadUserConfig()                       // fork.go:118
s.startSettings = cfg.Settings                  // fork.go:119
s.initPlatform(cfg.Settings) // NEW — here and nowhere else: s.platform = selectPlatform(cfg.Settings,
                             // s.sessionEnv, forkGeteuid(), forkProbeShellVersion), then the log lines of §2
s.focusPolicy.Store(forkFocusPolicy(cfg, s.platform.Wayland)) // fork.go:120, new signature
s.applyChords(nil, cfg.Settings)
s.initDetector(cfg)
s.initTracker(cfg.Settings)                     // reads s.platform
// … unchanged: layoutReaderFn/onOwnSwitch when tracker != nil, initFocusClient (reads
// s.platform), EnableAuto, autoEnabled …
```

`s.platform` is assigned **immediately after `loadUserConfig` (`fork.go:118-119`) and before
`fork.go:120`**, because the focus policy stored at `:120` is derived from it. "Before
`initTracker`" alone is not sufficient: a `selectPlatform` call inserted after `:120` would
build the start policy from the zero `PlatformSelection` and the daemon would run with it until
the first `auto.conf` reload. Under the fail-closed `StrictPurposeFor` (§3.3) the zero value
(`Wayland == false`) still yields strict, so the GTK3 password hole of K-X5 cannot open that
way — but the Fedora host would then wrongly start strict, and any future relaxation of the
rule would turn the mistake into exactly the fail-open hole (`RequirePurpose == false` ⇒ the
engine, `auto.go:293-299`/`:468-474`, allows a GTK3 password entry with purpose `none` and
allows everything while the extension is absent). WPX7's `TestInitForkPlatformBeforePolicy`
pins the order.
`s.platform` is written once in `initFork` and only read afterwards (control goroutine,
status snapshots, `applyUserConfig`), so it needs no lock.

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
source instead of the bare client.

`forkFocusPolicy` (`focus_glue.go:36-45`) is today a free function with no access to `s`; its
signature changes to

```go
// forkFocusPolicy builds the focus policy of a loaded configuration. wayland is
// PlatformSelection.Wayland; RequirePurpose = c.Settings.StrictPurposeFor(wayland) (§3.3).
func forkFocusPolicy(c *userconfig.Config, wayland bool) *focus.Policy
```

Callers (all updated by WPX7): `fork.go:120` ⇒ `forkFocusPolicy(cfg, s.platform.Wayland)`;
`applyUserConfig` (`userconfig_apply.go:164`, policy swap at `:177`) ⇒
`forkFocusPolicy(c, s.platform.Wayland)` (hot reload re-evaluates `auto`/`true`/`false`);
`FocusPolicyForDiagnostics(wayland bool)` (§3.7) ⇒ `forkFocusPolicy(cfg, wayland)`; the
existing test call sites `fork_test.go:831,836,843,849` pass **`true`** (Wayland host
semantics: `StrictPurposeFor(true)` resolves `auto` to non-strict, so the expectations stay
unchanged). Passing `false` there would mean an X11 session, i.e. strict mode, and
`TestExcludedAppMatchesRawExtensionID`'s fail-open check (no focus client ⇒ snapshot allowed,
`fork_test.go:842-846`) would become `focus-unavailable` and fail. `restartWarnings` (`auto_control.go:506-523`) is
unchanged (`layout-source` and `focus-source` stay restart-only; `auto-strict-purpose` is hot).

`buildAutoEngine` (`auto_control.go:70-115`): the `tracker == nil` branch (`:83-88`) returns
`s.platform.LayoutReason` when set (`layout-source-unsupported`, `layout-source-off`,
`root-mode`). `autoAvailability` takes the focus state too and reads strict from the live
policy (§2.3); `statusSnapshot` (`:431-487`) passes `fs`, adds the platform warnings to
`ConfigWarnings`, and publishes `FocusedApp`/`FocusedWmClass`/`InputPurpose` when
`s.focusMerged != nil` (today `:454-455` keys on `s.focusClient != nil`, which would hide the
x11 identity whenever the extension client is not started). `focusState()` returns the zero
`focus.State` when `s.focusMerged == nil`.

Nothing in the manual path changes: `emitConversionEvents` (`switcher.go:907-990`) still takes
the X11 branch (`:953-974`) on X11 GNOME, and `layoutReaderFn`/`onOwnSwitch` are installed
exactly as before (`fork.go:124-128`).

### 3.5 Tray and i18n (WPX5)

`internal/tray/settings_auto.go`: new `strictPurposeCheck *gtk.CheckButton` in the checkbox
table (`:213-222`) after `requireFocusCheck`, label `strGACheckStrictPurpose`, tooltip
`strGAHintStrictPurpose`. Loading (`setAutoSettingsWidgets`, `:333-340`): checked =
`s.StrictPurposeFor(trayWaylandSession())` where `trayWaylandSession()` applies the "positively
Wayland" rule (§3.3) to the tray's own environment. Collecting (`collectAutoSettings`,
`:351-359`): if the checkbox equals `loaded.StrictPurposeFor(wayland)` the loaded value
(possibly `auto`) is kept, else `true`/`false`
— so an untouched `auto` is never rewritten. `autoReasonText` (`:449-467`) maps
`layout-source-unsupported` ⇒ `strGAReasonLayoutSourceUnsupported`, `focus-source-off` ⇒
`strGAReasonFocusSourceOff`, `focus-extension-required` ⇒ `strGAReasonFocusExtensionRequired`.
`autoNotices` (`:510-532`) appends `strGAHintStrictNoExtension` when
`st.UnavailableReason == "focus-extension-required"`. The extension status row already offers
Enable / "Enable at next login" (`focus_extension.go:267-291`); on GNOME 43 `EnableExtension`
and `GetExtensionInfo` behave the same (F5), so `FocusExtNeedsRelogin` text applies.

`internal/tray/strings_ga.go` + `internal/i18n/locales/{en,ru}.json` (en text; ru by WPX5):
`strGACheckStrictPurpose` "Correct only in fields that report their type (recommended on X11)",
`strGAHintStrictPurpose` "Needs the focus extension. Corrects only fields that report a text
type; fields that report none are skipped. On X11, XIM clients (xterm, Emacs, Java, Wine) and Qt
applications report password fields as ordinary text: only the excluded applications list
protects them." (rev. 4, F22), `strGAReasonLayoutSourceUnsupported`
"no layout source for this session (GNOME Shell below 49.3 on Wayland, or no X11 display)",
`strGAReasonFocusSourceOff` "strict mode is on but focus-source=off",
`strGAReasonFocusExtensionRequired` "strict mode needs the focus extension (enable it below or
turn strict mode off)", `strGAHintStrictNoExtension` "Strict mode: enable the gswitch-focus
extension or clear 'Correct only in fields that report their type'." Printf parameters: none.

### 3.6 Shell extension: legacy variant (WPX4)

Layout:

```
shell-extension/gswitch-focus@vabuter.github.io/        ESM tree (GNOME 50; only the 'ready' handler below is added)
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
  `Shell.WindowTracker` `notify::focus-app`, the IBusManager signals (`focus-in`, `focus-out`,
  `set-content-type`, and `ready` from rev. 3, below) and the two
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

Purpose reset on IBus loss (rev. 3, **both trees**, WPX4): when the IBus connection is lost
(`ibus restart`, ibus-setup, a crashed ibus-daemon) `IBusManager._clear()` emits only
`'ready'(false)`, never `'focus-out'` (F19), and `_updateFocus` (`notify::focus-window`)
never touches the purpose. A `normal` left by gedit would then survive a move to a GTK3
password entry, which never sends `focus-in` (F8), and strict mode would allow a fix there.
Both trees therefore also connect `ibusManager.connect('ready', (_manager, ready) => …)` (id
stored with the other IBusManager ids and disconnected in `destroy()`): on `ready === false`
⇒ `this._imFocused = false; this._setPurpose('unknown');` (`true` is ignored: the next
`focus-in` + `set-content-type` reports the real purpose). `unknown` is denied in strict mode
(`purpose-unknown`) and changes nothing in non-strict mode (only password/pin/terminal are
denied there). The helper-equality check of `check-extension.sh` is unaffected (the handler
lives in the class); `metadata_test.go` asserts that both files contain `'ready'`.

Clutter fallback on X11 (rev. 4, **both trees**): until IBus reports anything (no
ibus-daemon), the purpose comes from `Main.inputMethod`. On X11 that input method serves only
GNOME Shell's own entries and is never told when the focus moves on to an X11 client, so a
`normal` left by the overview search would last into a GTK password entry. The shared helper
`clutterFallbackPurpose(wayland, shellEntryFocused, clutterPurpose)` returns `unknown` on X11
while `Main.inputMethod.currentFocus` is unset; the reporter re-derives the fallback on
`notify::focus-window` and on `global.stage` `notify::key-focus` (X11 only, while IBus was not
seen). Wayland is unchanged. `scripts/extension-sim.cjs` drives both trees through these
signals with stubbed Shell modules (run by `check-extension.sh` when node is present, and so
by `TestCheckExtensionScript`).

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

Phase A independence: while the legacy `packager: deb` entries are absent from `nfpm.yaml`
(WPX6 not merged yet) the packaging section of the rewritten script prints `skip  packaging:
legacy entries not in nfpm.yaml yet` and does **not** count a failure — also under `--strict`,
which (as today, `check-extension.sh:24-27,194-195`) governs only the node check. Otherwise
WPX4's `go test` (the metadata test runs the script with `--strict` in CI) would be red until
WPX6 merges. Once the entries exist the section is mandatory for both packagers.

### 3.7 CLI (WPX7, `internal/gswitch/cli.go`)

`cli.go` is package `gswitch` and cannot call the unexported `selectPlatform`; WPX7 adds two
exported, read-only helpers to `runtime` (next to `RunAutoCheck`):

```go
// PlatformForDiagnostics runs the daemon's source selection for the calling user: user config
// from userconfig.Dir() (warnings returned in sel.Warnings), env nil (process environment),
// forkGeteuid(), and the forkProbeShellVersion hook bounded by ctx. Nothing is started.
func PlatformForDiagnostics(ctx context.Context) (sel PlatformSelection, err error)
// FocusPolicyForDiagnostics loads ~/.config/gswitch and returns forkFocusPolicy(cfg, wayland)
// by value; callers pass sel.Wayland.
func FocusPolicyForDiagnostics(wayland bool) (focus.Policy, error)
```

- `--layout-state [--watch SECONDS] [--source auto|ibus|x11]` (`cli.go:56-57,129-146`): prints
  `Session: x11|wayland (DISPLAY=…, WAYLAND_DISPLAY=…)`, `GNOME Shell: <version>|unavailable`,
  the selection row it would use (`runtime.PlatformForDiagnostics`), then runs
  `layoutstate.RunX11Diagnostics` and/or `layoutstate.RunDiagnostics` accordingly (`--source`
  forces one). Exit 1 when the selected source cannot be reached.
- `--focus-state [--watch SECONDS]` (new): `focus.RunDiagnostics` with the extension client
  enabled when `sel.FocusExt`, the X11 source when `sel.FocusX11`, and the policy from
  `runtime.FocusPolicyForDiagnostics(sel.Wayland)`. Read-only, no root.
- `runHelp` (`:99-120`) lists both.

### 3.8 Packaging (WPX6)

#### 3.8.1 Package identity and metadata (`packaging/nfpm.yaml`, deb override `:83-97`)

Deb: `gswitch-auto_<V>-<R>_amd64.deb`, `Version: <V>-<R>` (`0.8.0+auto.1-1`; dpkg grammar allows
`+` and `.`; `~` stays forbidden). Override block:

```yaml
overrides:
  deb:
    depends: ["libc6 (>= 2.34)", systemd, libgtk-3-0, libx11-6, libglib2.0-bin, polkitd, pkexec, x11-xkb-utils]
    # libc6: nfpm adds no shlib dependencies; without it lintian reports
    # missing-dependency-on-libc (error). 2.34 = the highest GLIBC_ version the
    # Go/CGO binaries reference (F15); bookworm has 2.36.
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

lintian hygiene (the three errors an nfpm-built deb otherwise raises): (1) the `libc6`
dependency above; (2) the two autostart entries `/etc/xdg/autostart/gswitch-tray.desktop` and
`gswitch-daemon.desktop` (`nfpm.yaml:143-150`, plain entries ⇒ `file-in-etc-not-marked-as-conffile`)
are split like `default.conf` (`:129-139`): an unchanged plain entry with `packager: rpm` and a
`type: config` entry with `packager: deb`, so the RPM payload and `build-rpm.sh`'s assertions
stay byte-for-byte as today; (3) `debian-changelog-file-missing` (non-native `-1` revision, no
changelog) is suppressed with `lintian --suppress-tags debian-changelog-file-missing` in
`build-deb.sh` (an nfpm `changelog:` file may replace the suppression later, Q-X6).

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

F14: upstream's prerm (`remove in-favour` under `dpkg -i`, plain `remove` under `apt install`)
has already disabled the user unit when our `preinst` runs, and nothing on deb runs before it.
Mitigation:

- `preinst.sh`: for `$1 = install` (first install of gswitch-auto, also a reinstall after
  `apt remove gswitch-auto`) and when `dpkg-query -W -f='${db:Status-Status}' gswitch` prints
  anything but an empty string or `not-installed` — i.e. upstream gswitch **is or was**
  installed and dpkg still knows it: `half-configured`/`half-installed`/`installed` while
  `dpkg -i` replaces it in this run, **`config-files`** after apt removed it in its separate
  dpkg run just before (F14; rev. 3 — the implementation at `1124225` skips `config-files`, so
  on the documented apt path the unit stays disabled and stopped; WPX6 follow-up) — write
  `/run/gswitch-auto/replacing.<uid>` for every logged-in user with a session bus (same loop as
  `scripts/pretrans.sh:27-40`, without the `is-enabled` test — it is already false). Then the
  upstream `preinstall.sh` body (stop/disable the legacy system unit) verbatim between the
  markers `# --- upstream scripts/preinstall.sh begin ---` / `end`. CI cannot observe the
  difference (no logged-in users, the loop is empty); MX1 does.
- `postinst.sh`: for every `replacing.<uid>` marker: `systemctl --user daemon-reload`, and if
  `gswitch.service` is not enabled ⇒ `enable --now` + log line (as `posttrans.sh:43-51`), remove
  the markers; then the upstream `postinstall.sh` body verbatim between the same markers; then
  the extension hint (`posttrans.sh:67-75`, with "log out and back in (or Alt+F2, r on X11)").
- Limitation documented in `docs/FORK.md` and README (heuristic "upstream is or was
  installed"): a logged-in user gets `gswitch.service` enabled and started by the first
  install of gswitch-auto when upstream gswitch is being replaced **or** was removed earlier
  without `purge` (its `config-files` remain) — even if the unit had been disabled
  deliberately; `apt purge gswitch` beforehand, or `systemctl --user disable --now
  gswitch.service` afterwards, undoes it. Plain upgrades of gswitch-auto are not affected
  (`$1 = upgrade`). This is not a safety problem either way: a stopped daemon types nothing,
  and auto mode is off by default.
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
and re-executes itself inside:

```
podman run --rm --userns=keep-id --security-opt label=disable \
  -v "$repo:/src:ro" -w /src -v "$repo/builds/deb:/src/builds/deb" -v "$out:/out" \
  -v "$upstream_ref:/upstream-ref:ro" \
  -v gswitch-auto-gomod:/tmp/gomod -v gswitch-auto-gocache:/tmp/go-build \
  -e HOME=/tmp -e GOMODCACHE=/tmp/gomod -e GOCACHE=/tmp/go-build -e GOFLAGS=-buildvcs=false \
  -e GSWITCH_VERSION -e GSWITCH_RELEASE … $DEB_IMAGE scripts/build-deb.sh --no-container \
  --out /out --upstream-unit /upstream-ref/gswitch.service …
```

Why these flags: the checkout is mounted read-only, so the container build cannot modify the
host tree; its only writable mounts are `builds/deb` (created on the host first, bind-mounted
over the read-only tree) and the output directory at `/out` (rev. 5).
`--security-opt label=disable` instead of `:Z` — a `:Z` mount relabels every
file of the checkout to `container_file_t` on the SELinux host, a persistent side effect on the
user's tree; with `--userns=keep-id` the uid-1000 process has no writable `$HOME` inside
`golang:1.26-bookworm` and `/go` (GOPATH/GOMODCACHE) is root-owned, so `go build`/`go test` and
`git config --global --add safe.directory /src` (run first so `version.sh` works) would fail
without `HOME=/tmp`, `GOMODCACHE`, `GOCACHE`; the two named volumes keep the module and build
caches between runs so `make deb` does not re-download every module. The build needs network
access for the module download (the tree has no `vendor/`); CI uses the same image with its
own cache action. Inside: preflight (tools,
every `src:` with `packager: deb` or no packager exists), `scripts/version.sh --verify-deb`,
`scripts/check-extension.sh`, the scriptlet-marker check (§3.8.2), `go test ./...` (unless
skipped), CGO build with `-X main.version=$V` into `builds/deb/` (never over the Fedora
binaries in `builds/`), `nfpm package --packager deb --target dist/gswitch-auto_<V>-<R>_amd64.deb`,
`sha256sum` into `dist/checksums-deb.txt`, then assertions with `dpkg-deb -I`/`-c`/`-e`:
Package/Version/Architecture, Depends/Recommends/Provides/Replaces/Conflicts lines, files
(binaries 0755, user unit, udev rule, `default.conf` listed in `conffiles`, legacy
`metadata.json`/`extension.js`, **no** ESM-only marker — the packaged `extension.js` contains
`function init(` and the packaged `metadata.json` lists `"43"` in `shell-version`), `/usr/share/doc/gswitch-auto/copyright`, licences, no
`/usr/share/licenses`, no `/usr/lib/systemd/system/gswitch.service`; packaged user unit
byte-identical to `configs/gswitch.service`; `lintian --fail-on error --suppress-tags
debian-changelog-file-missing` (§3.8.1; warnings and the remaining info tags are advisory and
listed in the build log, never fatal).
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
syntax; the re-enable on the apt path (F14, §3.8.2) is observable only with a logged-in user
(MX1). `release.yml` adds a `deb` job (same container) that runs in parallel with the RPM
job; both upload their package and checksum file as artifacts, and a `publish` job (`needs:
[release, deb]`) creates the release once with all four assets and the Fedora and Debian
install notes (rev. 4: the earlier `gh release create` in the rpm job followed by `gh release
upload` left a public RPM-only release behind whenever the deb build or lintian failed). The
`build-test` job's apt list (`ci.yml:40-48`: `xvfb`, `x11-utils`, `xdotool`, …) gains
`x11-xkb-utils`, because the §6.2 `xvfb-run` switch test calls `setxkbmap`; its step
"X11 integration tests (Xvfb)" runs that command (rev. 4).

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
# X11 input-method clients whose password fields look like ordinary text fields
# (rev. 4, F22, K-X4): XIM clients and Qt applications
rxvt
aterm
eterm
mlterm
kterm
st
emacs                     # GNU Emacs uses XIM on X11
sun-awt-x11-*             # Java AWT/Swing windows
*.exe                     # Wine applications
pinentry-qt
pinentry-qt5
ksshaskpass
org.kde.ksshaskpass
kwalletmanager5
kleopatra
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
  F8 password behaviour, the dpkg replacement caveat with the rev. 3 heuristic of §3.8.2, and
  that `layout-source=ibus` is refused on X11 below GNOME Shell 49.3 and harmful on Wayland
  below 49.3 — it misjudges every word typed in the second layout, §2.1 note).
- `docs/FORK.md` (Russian): build section (`:44`) adds `make deb` and the container; release
  checklist (`:93`) adds the deb asset; rebase checklist (`:106`) adds the scriptlet re-sync
  (§3.8.2) and the legacy extension sync; modified-files table (`:140`) is updated (no new
  upstream files are modified by this addendum: `switcher.go` hooks are unchanged); install /
  rollback (`:179`) gets the Debian section (`apt install ./gswitch_0.8.0_linux_amd64.deb` if
  the user keeps it, else `apt remove gswitch-auto`); limitations (`:217`) list strict mode,
  polling latency and the unit re-enable heuristic ("upstream is or was installed", including
  its `config-files`, §3.8.2).
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
| `internal/gswitch/runtime/layout_glue.go` | `initTracker` `:17-42` (`IBusSourceEnabled` `:22`, `AddSource` `:35`, `AddResync` `:36`) | `var forkProbeShellVersion` hook; selection from `s.platform`; guarded/x11 sources; resync only on GNOME | WPX1 (hook, guard + probe call), WPX7 (x11 source wiring, see §5 note) |
| `internal/gswitch/runtime/gnome_version_glue_test.go` (new) | — | `init()` installs the probe fake; `initTracker` guard tests | WPX1 (WPX7 extends) |
| `internal/gswitch/layoutstate/{x11_source.go,x11_proto.go,x11_diag.go}` (new) | — | §3.1.2 | WPX2 |
| `internal/gswitch/focus/{x11_source.go,merge.go,diag.go}` (new) | — | §3.2.1–§3.2.3 | WPX3 |
| `shell-extension/legacy/<uuid>/{metadata.json,extension.js}` (new), `shell-extension/README.md` | — | §3.6 | WPX4 |
| `shell-extension/gswitch-focus@vabuter.github.io/extension.js` (ESM, fork-owned) | constructor IBusManager connections; `destroy()` | `'ready'` handler (rev. 3, §3.6) | WPX4 |
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
| `.github/workflows/ci.yml` | apt list `:40-48`; after `install-test` (`:103-162`) | `x11-xkb-utils`; `deb`, `install-test-deb` jobs | WPX6 |
| `.github/workflows/release.yml` | after `Publish GitHub release` (`:73`) | `deb` job + upload | WPX6 |
| `internal/gswitch/runtime/platform.go` (new) | — | §3.4 | WPX7 |
| `internal/gswitch/runtime/fork.go` | fields `:90-98`; `initFork` `:116-134` (`selectPlatform` inserted between `:119` and `:120`; `forkFocusPolicy(cfg, s.platform.Wayland)` at `:120`; `initTracker` `:123`, `initFocusClient` `:130`); `startFork` `:147-152`; `stopFork` `:178-181` | `platform`, merged focus, start/close | WPX7 |
| `internal/gswitch/runtime/focus_glue.go` | consts `:14-16`; `initFocusClient` `:21-32`; `forkFocusPolicy` `:36-45` (signature `(c *userconfig.Config, wayland bool)`); `focusState` `:71-76`; `focusSourceName` `:94-103` | merged source, `x11` value, `RequirePurpose`; `FocusPolicyForDiagnostics` | WPX7 |
| `internal/gswitch/runtime/auto_control.go` | consts `:26-34`; `buildAutoEngine` `:83-88`; `autoAvailability` `:417-427`; `statusSnapshot` `:443-457` (`:454-455` keys on `s.focusMerged`) | new reasons, focus-aware availability from the live policy, platform warnings, x11 identity in the status | WPX7 |
| `internal/gswitch/runtime/userconfig_apply.go` | `applyUserConfig` `:164` (policy swap `:177`) | `forkFocusPolicy(c, s.platform.Wayland)` on reload | WPX7 |
| `internal/gswitch/cli.go` | switch `:56-57`; help `:99-120`; `runLayoutState` `:129-146`; `parseLayoutStateArgs` `:149-172` | `--source`, `--focus-state` via `runtime.PlatformForDiagnostics`/`FocusPolicyForDiagnostics` | WPX7 |
| `internal/gswitch/userconfig/templates/exceptions-apps.txt` | whole file | §3.9 | WPX7 |
| `README.md`, `docs/FORK.md`, `CLAUDE.md`, `docs/NOTES.md` | §3.10 | docs | WPX7 |

---

## 5. Work packages and ownership

Rules as SPEC §5: a WP modifies only the files it owns; cross-WP dependencies are on the
interfaces of §3; fakes for another WP's API are allowed, edits to its files are not. Every WP
ends with `gofmt -l` empty, `go build ./... && go vet ./... && go test ./...` green and the
commit format of the task.

**Unexported identifiers in shared packages (rev. 3).** File ownership alone does not keep
parallel WPs mergeable: a Go package is one namespace, so two WPs that each add an unexported
package-level name to the same package build alone and fail once merged (WPX1 and WPX2 both
declared `sleepCtx` in `layoutstate`; the merged package did not compile until `e9e75c0`). A
package is **shared** when two WPs of the same phase add or modify Go files in it — Phase A:
`layoutstate` (WPX1, WPX2) and `focus` (WPX3, WPX4 tests, WPX5); `runtime` has only WPX1 in
Phase A and only WPX7 in Phase B; a follow-up round that runs several WPs in parallel
(revision 3's list) applies the same test. In a shared package:

1. Every **new unexported package-level identifier** — `func`, `type`, `var`, `const`,
   including each name inside a grouped `var (…)`/`const (…)` block; in production **and** test
   files — starts with a namespace of its WP from the table below. Methods and struct fields are
   scoped by their type and exempt; exported names are fixed by §3 and need no prefix.
2. Generic helpers are **not** exempt: a context-aware sleep, backoff/retry/timeout constants,
   session-type predicates, X connection dialling, reply bounding. A WP that needs one either
   declares its own namespaced copy (`x11SleepCtx`, `x11RetryMin`), or uses a helper that
   already exists in the WP's base tree (e.g. `ibusBackoffInitial`/`ibusBackoffMax` of
   `ibus_source.go`, `clientRetryMin`/`clientRetryMax` of `client.go`, and `sleepCtx` for a
   WP based on `e9e75c0` or later). A helper added by another WP of the *same* phase is never
   referenced; the duplicate is folded into the owner's copy only in the merge commit or a
   later phase.
3. Owned shared helpers (may be referenced by any WP whose base contains them; nobody else
   declares the name): `layoutstate.sleepCtx` (`gnome_version.go`, WPX1; used by
   `GuardedIBusSource` and through `X11Source`'s `sleep` hook, §3.1.1/§3.1.2);
   `layoutstate.ibusBackoff*` (base tree); `focus.clientRetry*`, `focus.clientCloseTimeout`
   (base tree; aliased by WPX3's `x11Retry*`/`x11CloseTimeout`).
4. Check before finishing (run per shared package; every printed name must carry the WP's
   namespace): `git diff <base> -- <pkg>/ | grep -E '^\+(func|type|var|const) [a-z]|^\+\t[a-z][A-Za-z0-9_]* +(=|[A-Za-z*\[])'`.
   The names merged up to `1124225` keep their spelling (e.g. WPX1's `probeShellVersion`,
   `parseVersionNumber`, `errServiceUnknown`; WPX2's `inX11Session`, `reversedPair`,
   `awaitReply`; WPX3's `isX11Session`, `openX11Session`, `mergeStates`, `describeMerged`);
   they belong to their owners and are not redeclared or repurposed by another WP.

| Package | WP | Namespaces of new unexported names (production) | Test helpers |
|---|---|---|---|
| `layoutstate` | WPX1 | `shell…`, `guard…`, `gv…` (+ owner of `sleepCtx`) | `gvTest…` |
| `layoutstate` | WPX2 | `x11…`, `xkb…`, `errX11…`/`errXKB…` | `x11LayoutTest…` |
| `focus` | WPX3 | `x11…`, `errX11…`, `merge…`/`merged…`, `focusDiag…` | `x11FocusTest…` |
| `focus` | WPX4 | — (no production Go) | `legacyExtTest…` |
| `focus` | WPX5 | `policy…`, `purpose…`, `strict…` | `strictTest…` |
| `runtime` | WPX1 | `gv…`, `forkProbe…`/`forkShell…` | `gvTest…` |
| `runtime` | WPX7 | `platform…` (+ the fork's existing `fork…` prefix for glue in `fork.go`/`*_glue.go`) | `platformTest…` |

Phase plan:

```
Phase A (parallel): WPX1  WPX2  WPX3  WPX4  WPX5  WPX6
Phase B (serial):   WPX7 (after merge of A)  →  WPX6 finalisation (CI/deb on the integrated tree)
Rev. 3 follow-ups (parallel, list in the header): WPX1  WPX2  WPX4  WPX6  WPX7
                    — `layoutstate` is shared by WPX1 and WPX2 again, so the rule above applies
```

### WPX1 — GNOME Shell version guard
- **Owns (new)**: `internal/gswitch/layoutstate/gnome_version.go`, `gnome_version_test.go`,
  `gnome_version_bus_test.go` (gated), `internal/gswitch/runtime/gnome_version_glue_test.go`.
  **(modified)**: `internal/gswitch/runtime/layout_glue.go` — (a) the package-level seam
  `var forkProbeShellVersion = layoutstate.ProbeShellVersion` (like `forkGeteuid`), and
  (b) `initTracker` only, where it runs `forkProbeShellVersion` (2 s) when `IBusSourceEnabled`
  is true, applies `IBusSourceAllowed`, wraps the IBus source in `GuardedIBusSource` when
  deferred, logs the decision, and leaves a clearly marked hook comment `// WPX7: X11 source
  selection goes here`; WPX7 later folds the probe call into `selectPlatform`'s `probe`
  parameter using the same variable (WPX7 owns `layout_glue.go` in Phase B).
- **Test seam**: `gnome_version_glue_test.go` has `func init() { forkProbeShellVersion =
  gvTestProbe }` where `gvTestProbe` returns the package variable `gvTestShellInfo` (default
  `ShellInfo{Version: "50.6"}`). Because test-file `init` runs for the whole package test
  binary, every existing `NewSwitcher`-based test with a GNOME env (`fork_test.go`,
  `switcher_fork_test.go`, which WPX1 may not edit) keeps today's IBus source and never calls
  the live session bus (the real probe would wait up to 2 s and, on this host, query the
  running gnome-shell). Tests needing another answer set `gvTestShellInfo` and restore it with
  `t.Cleanup`.
- **Depends**: none.
- **Tests**: `ParseShellVersion` table; `IBusEngineTracksLayout` boundary (`43.9` f, `49.2` f,
  `49.3` t, `49.rc` f, `50.6` t); `IBusSourceAllowed` matrix (auto/ibus × ok ≥/ok </err);
  `GuardedIBusSource` with a fake probe and injected sleep: err×3 then ok ≥ ⇒ inner `Run`
  called once, `Query` delegates; ok < ⇒ inner never run, `Available(true)` never sent, one log
  line, `Query` errors; ctx cancel while pending ⇒ returns; gated `GSWITCH_DBUS_TEST=1`: a fake
  `org.gnome.Shell` object with a `ShellVersion` property on `dbus-run-session` ⇒ `ProbeShellVersion`
  returns it, and no name ⇒ `ErrShellUnavailable`; in `runtime/gnome_version_glue_test.go`,
  `initTracker` unit tests through `NewSwitcher` with a GNOME env and the probe seam: probe
  `43.9` ⇒ `tracker == nil` and the log line; probe error ⇒ tracker with a guarded source
  (`State() == GuardPending`); probe `50.6` (the seam's default) ⇒ unguarded IBus source as
  today.
- **Acceptance**: on the Fedora host `gswitch --layout-state` (after WPX7) still reports the
  IBus source; no message is ever sent to `org.freedesktop.IBus` on the session bus (C20).

### WPX2 — X11 XKB layout source
- **Owns (new)**: `internal/gswitch/layoutstate/x11_source.go`, `x11_proto.go`,
  `x11_diag.go`, `x11_source_test.go`, `x11_proto_test.go`, `x11_integration_test.go` (gated).
- **Depends**: none (`detect.SessionEnv` for `X11SourceEnabled`; `xgb`/`xproto` already in `go.mod`).
- **Tests**: unit with a fake connection interface (`x11Conn`: `QueryExtension`, `UseExtension`,
  `GetState`, `RulesNames`, `Close`): initial group ⇒ `Available("x11", true)` +
  `Engine("x11", "xkb:us::")`; group 1 ⇒ `Engine("x11", "xkb:ru::")`; identical polls ⇒ **no**
  further `Engine` call (and, through a real `Tracker`: after `NoteSwitchHint` the state stays
  `Pending` across identical polls with the generation unchanged, then a real group change
  confirms it); table re-read after out-of-range group; `_XKB_RULES_NAMES` missing ⇒
  `Fallback` + one warning; dial failure ⇒ `Available("x11", false)` + backoff 200 ms→5 s with
  injected sleep; a `GetState` reply that never arrives ⇒ `Available("x11", false)` within
  `ReplyTimeout`, the connection closed and a reconnect (fake conn with a blocking reply);
  after a reconnect `Engine` is reported again; `Query` on live and temporary connections; `ParseXKBRulesNames` table (`evdev\0pc105\0us,ru\0,\0grp:alt_shift_toggle\0`,
  variants `dvorak,`, no trailing NUL, three layouts incl. GNOME's `us,ru,us`+`,,`, empty);
  reversed-pair warning on the first two entries (`ru,us,us` vs pair `us,ru` ⇒ one warning,
  `us,ru,us` vs `us,ru` ⇒ none; rev. 3); queued events drained once per poll (fake conn with
  pending events ⇒ `DrainEvents` called, replies keep flowing; rev. 3); `EngineForGroup`; request bytes
  equal upstream's (the golden bytes of `TestXKBStateRequestEncoding`,
  `runtime/x11_layout_switch_test.go:149-156`); `X11SourceEnabled` matrix
  (env nil with process env, env x11 GNOME, env x11 non-GNOME with `auto` ⇒ false and with
  `x11` ⇒ true, env wayland, euid 0, `"x11"` with/without display).
  Gated `GSWITCH_X11_TEST=1`: connect to `$DISPLAY`, `Query` returns the engine of the first
  `_XKB_RULES_NAMES` layout and `Run` reports `Available(true)`+`Engine` within 1 s (read-only;
  passes on the host's Xwayland, F16); additionally with `GSWITCH_X11_TEST_SWITCH=1` (container
  only): a second connection sends `XkbLatchLockState` (minor 5; 16 bytes, length field 4,
  per `/usr/include/X11/extensions/XKBproto.h` `xkbLatchLockStateReq`: `[opcode, 5, 4,0,
  deviceSpec 0x00,0x01, affectModLocks 0, modLocks 0, lockGroup 1, groupLock 1,
  affectModLatches 0, modLatches 0, pad 0, latchGroup 0, groupLatch 0,0]` — note the pad byte
  between `modLatches` and `latchGroup`; without it the request is 15 bytes and the server
  answers `BadLength`) to lock group 1 and expects index 1 within 200 ms, then restores group 0.
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
  `internal/gswitch/focus/metadata_test.go`; for the rev. 3 `'ready'` handler also the ESM
  `shell-extension/gswitch-focus@vabuter.github.io/extension.js` (that change only). (`packaging/nfpm.yaml` belongs to WPX6; WPX4
  tells WPX6 the exact entries in §3.6/§3.8.1 and tests against them once merged.)
- **Depends**: WPX6's nfpm entries for the packaging check (the script skips the packaging
  section with a notice while the legacy entries are absent — also under `--strict`, which
  governs only the node check, §3.6 — so WPX4's `go test` is green alone).
- **Tests**: `metadata_test.go` for both trees (uuid, shell-version 50 / 43+44, no session-modes,
  legacy has `function init(` and no `import `/`export ` lines, ESM has `export default class`);
  helper equality (`hintMask`, `securePurpose`, `purposeTable`, `IFACE_XML`); both files
  connect IBusManager `'ready'` (rev. 3, §3.6); the node helper
  test runs on both files; `check-extension.sh` run from the test when `bash` exists (`--strict`
  in CI).
- **Acceptance**: `node --check` of both copies; `gnome-extensions pack` of the legacy tree
  succeeds (manual, on the Debian machine); on 43.9 verify in Looking Glass (Alt+F2 `lg`) that
  `Main.inputMethod.currentFocus`, `Main.inputMethod.contentPurpose` and
  `Main.inputMethod.contentHints` exist (the pairing rule in `_onIBusContentType` and
  `_initialPurpose`, `extension.js:275-305`, depend on them; a missing getter only makes strict
  deny more — the safe direction — but would make shell-owned entries invisible on X11, so it
  must be known, not assumed).

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
  `shellcheck` (CI); deb assertions of §3.8.3 including `lintian --fail-on error
  --suppress-tags debian-changelog-file-missing` (libc6 dependency, autostart conffiles, §3.8.1);
  the `install-test-deb` job; `build-rpm.sh` still passes on the host with the deb-only entries
  present and the RPM payload unchanged (`rpm -qplv` diff against the previous build).
- **Acceptance**: `make deb` on the host produces `dist/gswitch-auto_<V>-1_amd64.deb` and
  `dist/checksums-deb.txt` via podman; `make rpm` unchanged.

### WPX7 — Integration (after merge)
- **Owns (new)**: `internal/gswitch/runtime/platform.go`, `platform_test.go`,
  `x11_fork_test.go`. **(modified)**: `runtime/{fork.go,layout_glue.go,focus_glue.go,
  auto_control.go,userconfig_apply.go,fork_test.go,switcher_fork_test.go,gnome_version_glue_test.go}`,
  `internal/gswitch/cli.go`, `cli_test.go` (new), `internal/gswitch/userconfig/templates/exceptions-apps.txt`,
  `internal/gswitch/ipc/introspect.go` (comments only), `README.md`, `docs/FORK.md`,
  `CLAUDE.md`, `docs/NOTES.md`.
- **Depends**: all of Phase A.
- **Tests**: `TestSelectPlatformMatrix` (every row of §2.1/§2.2 with fake env/probe/euid,
  incl. the rev. 3 rows `X11 GNOME` × `ibus` × {≥ 49.3, < 49.3 ⇒ refused with
  `layout-source-unsupported` and only the refusal warning, probe failed},
  incl. `Wayland` true/false and the non-GNOME X11 `auto` ⇒ no source row);
  `TestInitForkPlatformBeforePolicy` (the init-order test required by §3.4: `NewSwitcher` with
  an X11 GNOME env, default settings and the probe seam ⇒ `s.platform.X11Session == true`,
  `s.focusPolicy.Load().RequirePurpose == true`, and `forkFocusDecision(focus.State{},
  s.focusPolicy.Load())` returns `(false, "focus-unavailable")`; the same with a Wayland GNOME
  env ⇒ `RequirePurpose == false`; with an env where neither `XDG_SESSION_TYPE` nor
  `WAYLAND_DISPLAY` is set ⇒ `RequirePurpose == true` (fail-closed));
  `TestInitTrackerX11Session` (X11 GNOME env ⇒ exactly one source, `x11`, resync present;
  X11 non-GNOME + `auto` ⇒ `tracker == nil`, reason `layout-source-unsupported`; + `x11` ⇒
  one source, no resync); `TestInitTrackerNeverTwoPushSources`; `TestStatusAvailabilityStrict`
  (strict + ext absent ⇒ `focus-extension-required`, locked ⇒ available with
  `FocusSuppressedReason=locked`, `focus-source=off` ⇒ `focus-source-off`; after a reload that
  sets `auto-strict-purpose=false` the same snapshot reports available — the live policy, not
  the start value); `TestStatusSnapshotX11Identity` (no extension client, x11 source only ⇒
  `FocusedApp`/`FocusedWmClass` populated, `FocusSource == "x11"`);
  `TestPlatformForDiagnostics` (temp config dir, fake probe);
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
podman run --rm --userns=keep-id --security-opt label=disable -v "$PWD:/src:ro" -w /src \
  -v gswitch-auto-gomod:/tmp/gomod -v gswitch-auto-gocache:/tmp/go-build \
  -e HOME=/tmp -e GOMODCACHE=/tmp/gomod -e GOCACHE=/tmp/go-build -e GOFLAGS=-buildvcs=false \
  localhost/gswitch-auto-deb:bookworm \
  sh -c 'Xvfb :99 -screen 0 1024x768x24 -noreset >/dev/null 2>&1 & sleep 1; export DISPLAY=:99; \
         setxkbmap -layout us,ru -option grp:alt_shift_toggle; \
         GSWITCH_X11_TEST=1 GSWITCH_X11_TEST_SWITCH=1 go test ./internal/gswitch/layoutstate/ ./internal/gswitch/focus/ -run X11 -v'
```

(`Xvfb` and `x11-xkb-utils` are in the image, §3.8.3; the mount flags are those of §3.8.3 —
never `:Z`.) CI's `build-test` job already installs `xvfb` (`ci.yml:48`) and gains
`x11-xkb-utils` for `setxkbmap` (§3.8.5); it runs the same command with `xvfb-run -a -s
'-screen 0 1024x768x24 -noreset'` (rev. 4: without `-noreset` Xvfb regenerates when its last
client disconnects, so the keymap `setxkbmap` loaded is gone — one group, the lock step fails —
and a connection dialled during the regeneration is reset).

### 6.3 Manual test script — Debian 12, GNOME on Xorg (user)

Prerequisites: `gswitch-auto_<V>-1_amd64.deb` copied to the machine; no other gswitch process in
the foreground; gedit or gnome-text-editor as the test field; a terminal (gnome-terminal) and
`xterm` installed for MX7; `zenity` (GTK3 on bookworm) for the password steps of MX4/MX8/MX9;
`xdotool` for MX6 (`sudo apt install xdotool`). Conventions (rev. 3):

- "`busctl … X`" reads the property with `busctl --user get-property
  io.github.vabuter.GswitchAuto /io/github/vabuter/GswitchAuto io.github.vabuter.GswitchAuto X`.
  A value that depends on the focused window (`FocusSuppressedReason`) is read as `sleep 5;
  busctl … X` started from a terminal, then the window under test is focused within 5 s (the
  terminal itself is denied as `terminal`); for xterm, run it inside xterm. The same applies to
  the `gdbus … GetFocus` calls of MX4.
- Steps marked *(debug)* need the decision lines, which only `gswitch --debug` prints (F20; the
  journal never carries them): `systemctl --user stop gswitch.service`, then `gswitch --debug
  2>&1 | grep --line-buffered -E 'auto:|Auto-detected|confirmation'` in a spare terminal;
  afterwards Ctrl+C and `systemctl --user start gswitch.service`.
- A GTK3 password entry (`GtkEntry`, visibility FALSE) refuses Ctrl+C, and the bookworm zenity
  3.44 password dialog has no reveal icon. Password results are read from `zenity --password |
  od -c`: zenity prints the entered text when the dialog is confirmed with Enter/OK. Unchanged
  input prints `g   h   b   d   t   n` followed by a space; a correction prints `320 277 321 200
  320 270 320 262 320 265 321 202` (UTF-8 `привет`) followed by a space. Wait ~1 s after the
  Space before pressing Enter: a key pressed during the hold-off cancels a pending fix
  (`Observe` ⇒ `Cancel("input")`), which would make every result "unchanged". A fix also
  leaves the layout switched, so the top-bar indicator is a second witness.

- **MX1 Install.** `sudo apt install ./gswitch-auto_<V>-1_amd64.deb` (over the upstream `.deb`
  if it is installed; on a machine that never had upstream gswitch run `systemctl --user enable
  --now gswitch.service` afterwards). Expect `dpkg -s gswitch` ⇒ not installed (or
  `config-files`), `gswitch --version` ⇒ `gswitch version <V>`, `systemctl --user is-enabled
  gswitch.service` ⇒ `enabled` and `is-active` ⇒ `active`; over upstream the apt output shows
  `gswitch-auto: enabled gswitch.service for <user> (the removal of the gswitch package had
  disabled it)` (apt path of F14, §3.8.2 — if the line is missing and the unit is disabled,
  record it: the build lacks the rev. 3 `config-files` rule; then `systemctl --user enable --now
  gswitch.service`). `systemctl --user cat gswitch.service` identical to upstream's.
  `journalctl --user -u gswitch.service -b | grep 'layout sources:'` ⇒ `layout sources: x11
  (GNOME Shell 43.9); focus sources: extension, x11; strict purpose: true` (format of §2; the
  fragments to check are `layout sources: x11`, `focus sources: extension, x11` and `strict
  purpose: true`).
- **MX2 Layout switch key.** `gswitch --detect-layout-switch` ⇒ `scancodes: [186]`, `source:
  gnome`, raw `XF86Launch7`; `gsettings get org.gnome.desktop.wm.keybindings switch-input-source`
  now also contains `'XF86Launch7'` (added by the daemon, F9). Super+Space still switches.
- **MX3 X11 layout tracker.** `xprop -root _XKB_RULES_NAMES` ⇒ `"evdev", "pc105", "us,ru…",
  ",…", "…"`: the layouts field **starts with** `us,ru`; GNOME 43 appends the locale's layout
  (F18), so `us,ru,us` (en_US) or `us,ru,ru` (ru_RU) with variants `,,` is expected — record
  the exact value. `gswitch --layout-state --watch 20` ⇒ `Session: x11`, `GNOME Shell: 43.9`,
  source `x11`, a table that starts with `us,ru` (e.g. `us,ru,us`), group 0 ⇒ `xkb:us::`
  index 0, no pair-order warning; press Super+Space twice during the watch ⇒ two lines, index
  1 then 0, each within ~50 ms; `ibus engine` prints `xkb:us::eng` in both layouts (expected and
  unused, F2). `gswitch --layout-state --source ibus` ⇒ the note that the IBus source is
  disabled for GNOME Shell 43.9 (forced for this run). With `layout-source=ibus` in
  `~/.config/gswitch/auto.conf` and a daemon restart the start log shows the refusal warning of
  §2.1 and `AutoUnavailableReason` ⇒ `"layout-source-unsupported"`; restore `auto` and restart.
- **MX4 Extension.** Log out and in (or Alt+F2, `r`, Enter). `LC_ALL=C.UTF-8 LANGUAGE=
  gnome-extensions info gswitch-focus@vabuter.github.io` ⇒ `State: ENABLED` (GNOME 43/44; 45+
  print `ACTIVE`; a new extension shows `INITIALIZED` or `DISABLED` until it is enabled from the
  tray settings or with `gnome-extensions enable …`); the installed file is the legacy variant (`grep -c 'function
  init' /usr/share/gnome-shell/extensions/gswitch-focus@vabuter.github.io/extension.js` ⇒ 1).
  `gdbus call --session --dest org.gnome.Shell.Extensions.GswitchFocus --object-path
  /org/gnome/Shell/Extensions/GswitchFocus --method org.gnome.Shell.Extensions.GswitchFocus.GetFocus`
  (prefixed with `sleep 5;` for any window other than the terminal itself) with gnome-terminal
  focused ⇒ `('org.gnome.Terminal.desktop', 'Gnome-terminal', 'terminal')`;
  with gedit ⇒ `(…, 'Gedit', 'normal')`; with `zenity --password` (GTK3 `GtkEntry`) ⇒ purpose
  `none` (F8, expected on X11: the IBus im-module never focuses the context); with Settings →
  Users → Password (gnome-control-center 43 is GTK4, whose IM client widget is `GtkText`, not
  covered by F8) ⇒ `password` **or** `none` — both are denied by the policy (`password-field`
  resp. `purpose-unknown`), record which one was observed. IBus loss (F19, §3.6; a sanity
  check, timing-dependent): open `zenity --password`, then in a terminal run `sleep 5; ibus
  restart; sleep 3; gdbus call … GetFocus`; within the first 5 s click into gedit's text, and
  right after the restart (seconds 5–8) click into the zenity entry ⇒ purpose `unknown` or
  `none`, **never** `normal` (a stale `normal` from gedit is what the `ready(false)` reset
  prevents).
- **MX5 Focus diagnostics.** `gswitch --focus-state --watch 15` while switching between
  gedit, gnome-terminal and xterm ⇒ merged lines show `app source: extension`, purposes
  `normal` / `terminal` / `normal`, policy `allowed` / `denied (terminal)` / `denied
  (excluded-app)` (rev. 4: xterm is an XIM client; IBus pushes `(FREE_FORM, 0)` on its
  focus-in, F22, so only the exclusion list stops it).
  `busctl … FocusSource` ⇒ `"active"`.
- **MX6 Auto-correction.** Toggle auto on (LShift+RShift; tray shows on). In gedit, layout us:
  `ghbdtn ` ⇒ `привет ` and the layout indicator shows ru; `руддщ ` (ru) ⇒ `hello `; `hello `
  unchanged; `ghbdtn` + Enter unchanged; `GTK ` unchanged; `ghbdtn? ` ⇒ `привет, `. (No undo
  here: undo teaches the word, (d).) *(debug)* one `auto:` line per Space (`arm`/`fire`/`skip
  reason=…`). Layout switches (K-X1): (a) a mid-word switch the daemon cannot see — in a
  terminal run `sleep 4; xdotool key super+space` (XTEST events never pass through evdev),
  focus gedit (layout us), type `ghbd`, wait until the indicator shows ru, type `tn ` ⇒ no
  correction (the text stays `ghbdет `); *(debug)* `auto: skip reason=layout-changed`. Switch
  back with Super+Space. (b) A switch, then a word — press Super+Space (indicator ru), wait
  ~0.2 s, type the keys `hello` and Space ⇒ `руддщ ` is replaced by `hello ` and the layout is
  us again. (A first letter typed within the ≤ 50 ms poll window after a switch may make the
  word skip as `layout-changed`: the safe direction.) Super+Space typed *inside* a word is
  not a K-X1 case: the daemon sees the Meta key, which taints the word (`taint.go:150-151`),
  and its Space ends the word. (c) Switch latency and lost keys (rev. 4, F21): after the
  daemon's first start `gsettings get org.gnome.desktop.wm.keybindings switch-input-source`
  also lists `'<Shift>XF86Launch7'` (log: `Added <Shift>XF86Launch7 …`). With *(debug)*
  running, type `ghbdtn `, and as soon as the fix appears type `rfr ltkf ` at normal speed ⇒
  the screen shows `привет как дела ` with no letter missing, the input-source popup never
  appears, and the time from the Space to the fixed text is the hold-off plus at most
  ~100 ms (never ~1.5 s; `auto: arm` → `auto: fire` in the debug log). A fix that skips with
  `input-pending` because a key was still down is the documented safe direction, not a failure.
  (d) Undo, last because it changes the word list: `ntcn ` ⇒ `тест `, double Shift right after
  the fix ⇒ `ntcn ` and the layout back, as fast as the fix. With the defaults (`auto-learn-on-undo`,
  count 1) `ntcn` is now an exception word (`grep -x ntcn ~/.config/gswitch/exceptions-words.txt`
  once the tray has saved it); remove it under Settings → Auto correction → Exception words.
  Undoing a `ghbdtn` fix instead would make it an exception, and MX8/MX9, which reuse `ghbdtn`,
  would then pass without testing anything.
- **MX7 Strict mode.** In `xterm` type `ghbdtn ` ⇒ **no** correction; `busctl … FocusSuppressedReason`
  (run inside xterm) ⇒ `"excluded-app"` (rev. 4: xterm is an XIM client and reports `normal`,
  F22, so strict mode alone would allow it; the exclusion template that the tray seeds,
  `userconfig.EnsureFiles`, §3.9, lists `xterm`, which matches its `WM_CLASS` `XTerm`).
  Remove `xterm` under Settings → Auto correction → Excluded applications, Apply ⇒ `ghbdtn ` in
  xterm ⇒ `привет ` **even with strict mode on** (the documented K-X4 limit); add `xterm` back,
  Apply. Settings → Auto correction: clear "Correct only in fields that report their type",
  Apply ⇒ `grep auto-strict-purpose ~/.config/gswitch/auto.conf` ⇒ `false`; xterm still gets
  no correction with reason `"excluded-app"`. Re-check the strict box,
  Apply ⇒ the file says `true` (not `auto`: the tray writes the explicit value once toggled);
  hand-edit back to `auto-strict-purpose=auto` ⇒ the daemon logs the reload and behaves as
  `true`.
- **MX8 Extension absent in strict mode (strict on = default).** `gnome-extensions disable
  gswitch-focus@vabuter.github.io` ⇒ within a second `AutoUnavailableReason` ⇒
  `"focus-extension-required"`, `FocusSource` ⇒ `"x11"`, the tray status says "Unavailable:
  strict mode needs the focus extension…" and the hint line appears. **Password field under
  test**: `zenity --password | od -c`, type `ghbdtn ` into the entry and press Enter ⇒ `od`
  prints the unchanged `g h b d t n` + space, the indicator still shows en; `busctl …
  FocusSuppressedReason` ⇒ `"focus-unavailable"` and `busctl … AutoUnavailableReason` ⇒
  `"focus-extension-required"`; *(debug)* `auto: skip reason=focus:focus-unavailable`. The
  same in gedit ⇒ unchanged with the same two values (no corrections anywhere while the
  extension is absent). `gswitch --focus-state` shows `app source: x11` with the `WM_CLASS` of
  the focused window (`Zenity`, `Gedit`) while the policy line says `denied
  (focus-unavailable)`. Then set strict off (MX7) ⇒ corrections return everywhere except
  excluded apps: in gnome-terminal (class `Gnome-terminal`) and xterm no correction
  (`excluded-app`, from the X11 identity). Re-enable the extension; restore strict (`true` or
  `auto`).
- **MX9 Password and password manager — with strict on (default).** (a) `zenity --password |
  od -c` (GTK3, extension active), `ghbdtn ` + Enter ⇒ unchanged bytes, indicator stays en;
  `sleep 5; busctl … FocusSuppressedReason` with the dialog focused ⇒ `"purpose-unknown"`
  (purpose `none`, F8); *(debug)* `auto: skip reason=focus:purpose-unknown`. (b) Settings →
  Users → Password (GTK4): `ghbdtn ` ⇒ the indicator stays en, reason `"password-field"` or
  `"purpose-unknown"` (whichever MX4 recorded; *(debug)* the matching `skip reason=focus:…`);
  cancel the dialog. (c) **Strict off ⇒ the GTK3 password entry IS corrected (K-X5)**: set
  `auto-strict-purpose` to false (MX7), `zenity --password | od -c`, `ghbdtn ` + Enter ⇒ `od`
  prints the `привет` bytes + space and the indicator shows ru — this is the documented hole
  of §2.2 row "strict `false` (explicit) ⇒ fail-open" and of K-X5, not a defect; the design
  offers no password protection on X11 without strict mode. **Re-enable strict** (tray
  checkbox or `auto-strict-purpose=auto`) and verify (a) again. (d) KeePassXC (if installed):
  no correction with or without the extension and in either strict mode (`keepassxc` in
  `exceptions-apps.txt` matches `WM_CLASS`).
- **MX10 Upstream gestures.** Auto off and on: `ghbdtn` + double-Shift ⇒ `привет`; again ⇒
  back; held-Shift phrase; select `Hello` + Ctrl + held Shift + double-tap other Shift ⇒ `hELLO`.
  The X11 confirmation path is silent on success (F20), so it is checked through its
  precondition and the absence of its error: *(debug)* the start shows `Auto-detected layout
  switch keys: … ([186]) from gnome`, which with MX2's raw `XF86Launch7` makes
  `shouldVerifyGNOMEX11LayoutSwitch` true; after every double-Shift there is no `GNOME X11
  layout switch confirmation:` error and no `GNOME Wayland` text. Selection chord
  LShift+RAlt+RCtrl on a selected `ghbdtn` ⇒ `привет`, layout unchanged.
- **MX11 Lock.** `(sleep 8; busctl … FocusSuppressedReason) > ~/mx11.txt &`, lock the screen
  (Super+L) within 8 s, unlock after ~10 s ⇒ `~/mx11.txt` reads `"locked"`
  (`Disabling("locked")` from the legacy extension); auto works after unlock.
- **MX12 Robustness.** `systemctl --user restart gswitch.service` ⇒ state preserved (tray
  running). Change `sources` order in GNOME Settings (ru first), log out/in ⇒ `--layout-state`
  table starts with `ru,us` (e.g. `ru,us,us`, F18), group 0 ⇒ `xkb:ru::`, correction still
  right (mapping by name). No pair-order warning when `layout1`/`layout2` are unset in
  `/etc/gswitch/default.conf` (the pair follows `sources`); with them fixed to `us`/`ru` the
  warning `X11 layout table ru,us,… differs from the configured pair us,ru` is expected (rule
  on the first two entries, §3.1.2).
- **MX13 Removal / rollback.** `sudo apt remove gswitch-auto` ⇒ `/usr/bin/gswitch` gone, unit
  disabled; or `sudo apt install ./gswitch_0.8.0_linux_amd64.deb` (if kept) ⇒ upstream back,
  `systemctl --user enable --now gswitch.service`.

Fedora host regression after the merge: SPEC §6.4 M3, M5, M6, M8b and M12 unchanged;
`gswitch --layout-state` shows `GNOME Shell: 50.6`, source `ibus`.

---

## 7. Risks, mitigations, open questions

| # | Risk | Mitigation |
|---|---|---|
| K-X1 | Polling latency (≤ 50 ms) may classify a Super+Space switch late. The automatic switch itself: upstream's modifier-less `XF86Launch7` would keep GNOME's popup open for 1.5 s, swallowing typing (F21) | Fire gates re-read the tracker (safe direction: a late change only skips a word); `PollInterval` tunable; events deferred (Q-X1); the auto path switches with the fork-owned `<Shift>XF86Launch7` (§2.1 note), other modifier-less bindings get one press, a 2.5 s window and discarded input (`TestAutoSwitchPlanPerPlatform` pins the timings); MX6 (a)/(b) exercise a mid-word switch the daemon cannot see and a switch before a word, MX6 (c) the switch latency and lost keys |
| K-X2 | `_XKB_RULES_NAMES` order or content differs from the daemon's pair (explicit `layout1`/`layout2`, > 2 sources, GNOME's appended locale layout — `us,ru,us`/`us,ru,ru`, F18 —, non-GNOME WMs that do not set the property) | Mapping by name, not position; the third (locale) entry is never locked by GNOME and maps by name too; the pair-order warning compares the first two entries (§3.1.2); `Fallback` to the pair with a warning; `--layout-state` prints the table; MX3, MX12 |
| K-X3 | `DISPLAY`/`XAUTHORITY`/`XDG_SESSION_TYPE` missing in the user service environment (non-GDM, custom `.xsession`): the §0 rule with a nil `SessionEnv` needs `XDG_SESSION_TYPE=x11` in the `systemd --user` environment and has no `DISPLAY` fallback | F10 (present on Debian 12 GDM/X11) + `setupX11Environment` for `DISPLAY`/`XAUTHORITY`; a missing `XDG_SESSION_TYPE` selects **no** X11 source (auto unavailable, `layout-source-unsupported`, logged) and strict mode (fail-closed, §3.3) — never fail-open; documented remedy `systemctl --user import-environment DISPLAY XAUTHORITY XDG_SESSION_TYPE`; the sources log the connect error and retry |
| K-X4 | Strict mode is no protection for clients that never report a content type: IBus pushes `(FREE_FORM, 0)` on every focus-in (F22), so XIM clients (xterm, urxvt, st, Emacs, Java, Wine — GNOME 43 runs `ibus-daemon --xim`) and Qt applications (no focus-out for a password `QLineEdit`) are reported as `normal` and corrected, password prompts included, unless excluded. Apps that never focus IBus at all report `none` and are skipped (false negatives) | The default exclusion template lists XIM terminals, Emacs, Java (`sun-awt-x11-*`), Wine (`*.exe`) and Qt password prompts (§3.9, `TestAppsTemplateCoversUntrustedX11Clients`); README and the tray tooltip state that strict mode does not cover them; MX5/MX7 |
| K-X5 | Strict mode off on X11: password fields of GTK apps look like "no field" (F8) and are then corrected like any text. Qt password fields are corrected in either mode (F22, K-X4). Strict mode on: a purpose left over from the previous field when the IBus connection drops (no `focus-out`, F19), or from a GNOME Shell entry when no ibus-daemon runs | Default strict on X11; exclusion template lists password managers and Qt password prompts; README warns. The extension resets the purpose to `unknown` on `ready(false)` and, without IBus, whenever the focus leaves the Shell's entries (§3.6), which strict mode denies |
| K-X6 | Two extension trees drift | `check-extension.sh` helper/IFACE equality; FORK.md rebase checklist; shared tests on both files |
| K-X7 | GNOME 43 loads extensions only at login; a user with the ESM tree installed from the RPM on a 43 machine (or vice versa) gets OUT_OF_DATE | Packages ship the matching tree only; tray shows `FocusExtOutOfDate`; `install-extension.sh --legacy` for dev |
| K-X8 | dpkg replacement disables the user unit before our `preinst` (F14); under `apt install` upstream is already removed (`config-files`) when `preinst` runs | Marker in `preinst` (upstream status anything but unknown/`not-installed`, including `config-files`, rev. 3) + `enable --now` in `postinst` for logged-in users; documented heuristic "upstream is or was installed"; MX1 (CI has no logged-in users) |
| K-X9 | Upstream scriptlet text embedded in `scripts/deb/*.sh` goes stale after a rebase | Build-time identity check against `scripts/{pre,post}install.sh`; checklist item |
| K-X10 | Go 1.26 toolchain only via container; rootless podman bind mount uid/`.git` ownership; no writable `$HOME`/`/go` for the keep-id user; `:Z` relabelling of the checkout | `--userns=keep-id`, `--security-opt label=disable`, `HOME=/tmp`, `GOMODCACHE`/`GOCACHE` on named volumes, `safe.directory`, `GOFLAGS=-buildvcs=false` (§3.8.3); CI uses the same image |
| K-X16 | The X server stops answering (or an unregistered XKB error swallows a reply, F11/§3.1.2 item 5) while the tracker stays `Confirmed` on a stale index | Every reply bounded by `ReplyTimeout`; timeout ⇒ `Available(false)` (tracker `Unknown`, auto unavailable) and reconnect |
| K-X11 | `xgb.NewEventFuncs` global map (F11) if someone later adds XKB events | Decision recorded in §3.1.2; any event-based source must register at process start |
| K-X12 | The IBus guard waits on gnome-shell at login; a non-GNOME Wayland session with `layout-source=auto` has no source | Guard with backoff; clear reason `layout-source-unsupported`; `layout-source=ibus` override |
| K-X13 | `_NET_ACTIVE_WINDOW` lags or points at a transient (menus, override-redirect never set it) | Only app identity is derived; a stale identity can only deny (excluded app) or allow a non-excluded app, same as the extension's `window:N` ids |
| K-X14 | Two sources would double-report (future X11 GNOME ≥ 49.3) | Selection never adds `ibus` on X11 with `auto` (§2.1) |
| K-X15 | `policykit-1` is transitional on bookworm (lintian: `depends-on-obsolete-package`, an error) | Depends on `polkitd` and `pkexec` directly; `build-deb.sh` asserts both and rejects `policykit-1`; reviewed per release |

Open questions (deferred, default chosen):

| # | Question | Default |
|---|---|---|
| Q-X1 | `XkbSelectEvents`/`StateNotify` instead of polling | Polling (§3.1.2); revisit if xgb gains an XKB package or if latency is noticeable |
| Q-X2 | Should strict mode also require the extension to have reported *this window's* purpose after the last `FocusChanged` (purpose staleness)? | No: the extension emits `focus-out` ⇒ `none` on every IBus focus change while IBus is connected, and resets the purpose to `unknown` when the IBus connection drops (`ready(false)` has no accompanying `focus-out`, F19; §3.6, rev. 3) — both are denied by strict mode. Residual: none known; revisit if a toolkit is found that switches fields without an IBus focus change |
| Q-X3 | Ship `.deb` for arm64 | No (amd64 only, as the RPM) |
| Q-X4 | `layout-source=x11` on Wayland via Xwayland for non-GNOME compositors | Refused with a warning (F16) |
| Q-X5 | Tray checkbox vs tri-state control for `auto-strict-purpose` | Checkbox that preserves `auto` until toggled (§3.5) |
| Q-X6 | Ship a Debian changelog (nfpm `changelog:`) instead of suppressing `debian-changelog-file-missing` | Suppress for now (§3.8.1); revisit when the release process generates changelogs |
| Q-X7 | Dial the X `net.Conn` ourselves (`xgb.NewConnNetWithCookieHex`, own `DISPLAY` parsing and Xauthority lookup) so a hung server's socket can be closed directly instead of leaking until the server closes it | Yes (rev. 4): the leak was one goroutine and socket per reconnect attempt; `internal/gswitch/x11conn` does it for both X11 sources (§3.1.2 item 5) |
