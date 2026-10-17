# gswitch-auto — Implementation Specification (v1)

Status: contract for parallel implementers (v1, review round 2 applied). Date: 2026-10-17. Base: upstream gswitch v0.8.0
(fork tree at `/home/vab/p/gswitch-auto`, `internal/` and `cmd/` byte-identical to tag v0.8.0;
all `file:line` references below are to that tree unless marked otherwise).

This document merges the eight area plans (render-detector, layout-tracker, auto-engine, chords,
user-config, ipc-tray, shell-extension, packaging) and applies every correction from the
verification round. Where the area plans disagreed, this spec decides; implementers follow this
spec, not the area plans. Names (packages, types, config keys, D-Bus names, file names) are
normative: use them exactly.

Conventions: code, comments, commit messages and this spec are English (D1); UI strings exist in
English and Russian. "Run goroutine" = the goroutine executing `Switcher.Run()`'s main loop
(`switcher.go:455-464`); it owns the `Converter`, the auto engine and all key-path state.

---

## 1. Goals, non-goals, environment, decisions, requirements

### 1.1 Goals

1. Punto-Switcher-like automatic correction of the last word typed in the wrong layout, for the
   `us`+`ru` pair, triggered by Space only, on GNOME Shell 50 / Wayland / IBus.
2. Keep every upstream gesture working byte-for-byte (double-Shift word, held-Shift phrase,
   Ctrl+double-Shift selection, custom convert key) and keep X11/KDE builds compiling and safe.
3. Runtime control: chord toggle, tray state, persisted setting, settings GUI, exception lists,
   application exclusions, service management.
4. Ship as one RPM `gswitch-auto` that replaces upstream `gswitch`.

### 1.2 Non-goals (v1)

- No keyboard grab (`EVIOCGRAB`). `InputReader.GrabAll` (`input_reader.go:401`) stays unused.
  Stage-1 option, see §7.
- No pointer-device reading (mouse clicks stay invisible: `input_reader.go:220` rejects devices
  without KEY_A and `configs/70-gswitch.rules` grants uaccess to keyboards only).
- No Backspace-based undo (Backspace is delivered to the app before the daemon sees it).
  Undo = the user's "convert word" gesture right after an auto fix.
- No more than two layouts; no non-`us`/`ru` language models; no Ukrainian model (licence).
- No network, no LLM, no model regeneration (embedded punto-rs blobs are used unchanged).
- No upstream PR; the fork is rebased onto upstream releases (`docs/FORK.md`).
- No D-Bus activation of the daemon; the systemd user unit is the only launcher.
- No change to `/etc/gswitch/default.conf` semantics or to upstream `config.Config` fields.

### 1.3 Target environment (verified on the host, 2026-10-16/17)

| Item | Value |
|---|---|
| OS / shell | Fedora 44, kernel 7.2.9-200.fc44, GNOME Shell 50.5 (mutter 50.5), Wayland, libinput 1.31.3 |
| IBus | 1.5.34-4.fc44, `use-global-engine` true, engines `xkb:us::eng`, `xkb:ru::rus` |
| IBus address file | `~/.config/ibus/bus/<machine-id>-unix-wayland-0` (`IBUS_ADDRESS=unix:path=/home/vab/.cache/ibus/dbus-…`, `IBUS_DAEMON_PID=`) |
| GNOME bindings | `switch-input-source=['<Alt>Shift_L']`, `switch-input-source-backward=['<Shift>Alt_L']`; `xkb-options=['grp:shift_alt_toggle']` is a no-op (not an xkeyboard-config option); detected layout-switch set `[56,42]` |
| xkeyboard-config | 2.47; `ru` default section is `winkeys` (`symbols/ru:9-12`) |
| Keyboard | `/dev/input/event3` "AT Translated Set 2 keyboard", LED caps mask 0x7 |
| Daemon | upstream gswitch 0.8.0 as user unit `gswitch.service`; env has `HOME`, `WAYLAND_DISPLAY=wayland-0`, `DISPLAY=:0`, `DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus`, no `XDG_CONFIG_HOME` |
| Sandbox (enforced) | private mount+user namespace; `/home`, `/run/user/1000` read-only; rw: `/tmp`, `/dev`, `/proc`, `/sys` |
| Toolchain | **present**: golang 1.26.9 (`/usr/bin/go`, satisfies `go 1.25.5`), gtk3-devel 3.24.52, libxkbcommon-devel 1.13.1, libX11-devel; nfpm v2.48.0 at `~/go/bin/nfpm` (**not on PATH**); make, gcc, pkg-config, python3+rpm. Absent: goreleaser, golangci-lint, rpmbuild, go-task |
| Repo | origin `git@github.com:vabuter/gswitch-auto.git` (public), upstream `https://github.com/arumata/gswitch.git`; `upstream/main` (bcec457) is 14 commits after v0.8.0 (9058e19): godbus v5.2.2 bump, CodeQL bump, OBS/openSUSE packaging (`packaging/opensuse/*`, `scripts/obs_*`, `.github/workflows/obs.yml`), README commits — no `.go` changes |

### 1.4 Fixed decisions D1–D8 and how this spec realises them

| ID | Decision | Realisation |
|---|---|---|
| D1 | Module path `github.com/arumata/gswitch` unchanged; upstream style; English code; UI en+ru | Fork-only i18n IDs use prefix `strGA` (not `strAuto`, which collides with upstream `strAutoDetect*`); required in `en.json`/`ru.json` only; other locales fall back to English (`i18n.go:155-164`). |
| D2 | User config dir `~/.config/gswitch/` written by the tray without polkit; `/etc/gswitch/default.conf` stays upstream; daemon under the upstream user unit | Package `internal/gswitch/userconfig`; files `auto.conf`, `exceptions-words.txt`, `exceptions-apps.txt` (+ upstream's `ui.conf`, `wizard-shown`). The tray is the **only** programmatic writer. The daemon runs under the byte-identical upstream unit (`ProtectHome=read-only`, `ReadWritePaths=/tmp`) and only **reads** the directory. Runtime changes made inside the daemon (chord/D-Bus toggle, learned words) are persisted by the tray on the daemon's behalf through IPC (`AutoEnabledUnsaved`, `PendingLearnedWords`, §3.9.1, §3.11.1); without a running tray they last until the daemon restarts (documented limitation, §7.1 K7). A `ConfigurationDirectory=` unit delta was considered and rejected because it changes the unit D2 fixes (Q11). |
| D3 | Daemon↔tray IPC over the session bus | Daemon owns `io.github.vabuter.GswitchAuto` (§3.9). Tray subscribes to `PropertiesChanged` and signals; no polling of daemon state. Both sides use private session connections, never the shared `dbus.SessionBus()` that fyne systray uses (C27). |
| D4 | Active layout from IBus private bus | Package `internal/gswitch/layoutstate` (§3.4): `GlobalEngineChanged` + `Properties.Get(GlobalEngine)`; gsettings `mru-sources` only as a lower-confidence resync that never enables auto-correction; physical chord as Pending hint. |
| D5 | Port punto-rs Bloom+trigram detector to Go, reuse blobs and eval TSV | Package `internal/gswitch/lang` (§3.3), blobs embedded via `go:embed`, bit-exact golden tests, eval gates. |
| D6 | Shipped GNOME Shell extension for focus | Extension `gswitch-focus@vabuter.github.io` (§3.5), daemon client `internal/gswitch/focus`; absent extension ⇒ app exclusions inactive (fail-open unless `auto-require-focus=true`), GUI hint. |
| D7 | RPM `gswitch-auto` replacing `gswitch`, binaries keep names | `packaging/nfpm.yaml` (§3.13): `Provides: gswitch = V-R`, `Obsoletes: gswitch < 0.9`, **no** `Conflicts:` (an unversioned `Conflicts: gswitch` matches our own Provides — rpmlint `self-conflict`; Obsoletes+Provides already perform the replacement, and a later upstream package would file-conflict on `/usr/bin/gswitch`); `.goreleaser.yml` removed. |
| D8 | X11/KDE compile and fail safe | Auto engine is constructed only when layout pair, detector languages and a layout source are available; otherwise `AutoAvailable=false` with a reason code and manual mode is untouched. Every fork hook on the manual path is nil-safe **and** installed only when its dependency exists (`tracker == nil` ⇒ no `layoutReaderFn`, no `onOwnSwitch`, no chord-flip restore; §3.10.2), so X11/KDE/root/`layout-source=none` run exactly the upstream conversion code. |

### 1.5 Requirements R1–R8 (acceptance pointers)

| ID | Requirement | Where satisfied | Acceptance |
|---|---|---|---|
| R1 | Auto-correct last wrong-layout word on Space only | §2.2, §3.2, §3.8 | engine tests §6.1; manual M6 |
| R2 | LShift+RShift toggles auto mode; visible in tray; persisted | §3.7, §3.10, §3.11 (persisted by the tray, D2) | chord tests, `TestAutoEnabledPersistenceRule`; manual M5 |
| R3 | Double-Shift and all upstream gestures unchanged | §2.3.3 | `go test ./...` upstream suite green + `TestProcessKeyEventChordsKeepUpstreamGestures` + `TestManualConversionWithoutTracker`; manual M10 |
| R4 | LShift+RAlt+RCtrl converts selection (upstream Ctrl+double-Shift stays) | §3.7, §3.10 | chord tests; manual M9 |
| R5 | All hotkeys configurable in GUI | §3.11.3 | GUI tests; manual M11 |
| R6 | User list of excluded apps (Shell extension) | §3.5, §3.6 | focus policy tests, `TestExcludedAppMatchesRawExtensionID`; manual M8, M8b |
| R7 | User list of exception words | §3.3, §3.6 | detector/userconfig tests; manual M7 |
| R8 | GUI for service (start/stop/restart, login, status) and all of the above | §3.11 | GUI tests; manual M11 |

### 1.6 Corrections ledger (refuted or amended claims — must not reappear)

| # | Refuted / amended claim | What this spec does instead |
|---|---|---|
| C1 | "performConversion can be reused verbatim" (03-judgements:201-230) | Auto path uses `Converter.ConvertRange` + `emitConversionEvents` with explicit range and options; bypasses ReverseMode swap (`switcher.go:824-831`), `Flush()` (`:917`) and `CompleteConversion`. |
| C2 | "Undo is free" (03-judgements:136-149) | Explicit `AutoEngine.TryUndo` keyed on `Converter.UndoGeneration()`, the focus `Seq` and a stored copy of the word events; intercept as the **first** statement of `performConversion`, **before** the ReverseMode swap, on the raw `ActionConvertWord` (bare double-Shift / unmodified custom key), so a bare double-Shift undoes in both ReverseMode settings and never becomes a whole-line conversion over mixed layouts (03-judgements:146 item 3). Any other manual conversion disarms the undo (`Cancel("manual")`). |
| C3 | "Space trigger is safe at Push time" (03-judgements:299-333) | Decide at Space K_DOWN, fire only after Space K_UP, empty held set, `EVIOCGKEY` all-up, empty event queue and a 120 ms quiet holdoff (mutter drops uinput keys while the seat key count ≠ 0). |
| C4 | "Buffer equals screen" (03-judgements:249) | `WordTaint` observer (autorepeat incl. Backspace repeat, Delete, 102ND, CapsLock/NumLock mid-word, Meta window, Ctrl+key, unknown keys, input discarded by Flush/drain — C25); clicks remain a documented risk. |
| C5 | "Clear the buffer on an external IBus layout change" | Never call `ClearBuffer` from a layout notification (it breaks upstream undo via `invalidateUndo`, `converter.go:651-654`). Engine only cancels a pending fix / disarms its own undo, and requires the layout generation to be unchanged from word start to fire. |
| C6 | "The daemon can persist the toggle and learned words itself" (earlier draft: via `ConfigurationDirectory=gswitch`) | Not done: it changes the upstream unit that D2 fixes, and under that unit every daemon write under `$HOME` fails with EROFS (`configs/gswitch.service:25,28`). The daemon only reads `~/.config/gswitch`; the tray persists `auto-enabled` and learned words on its behalf (§3.9.1 `AutoEnabledUnsaved`/`PendingLearnedWords`, §3.11.1). |
| C7 | `org.gnome.ScreenSaver` as lock source | Primary lock signal = extension `Disabling("locked")` + vanish-without-notice ⇒ fail closed; ScreenSaver `ActiveChanged` only as late, additive hint. |
| C8 | Introspect allowlist cited at shellDBus.js:28-32 | Correct citation: `misc/introspect.js:7-10` (APP_ALLOWLIST), `:51`, enforced `:126`, `:141` via `misc/util.js:399-409`. Extension remains the only practical focus source (AT-SPI is off on the host). |
| C9 | "EnableExtension works without relogin" | First install and every extension code upgrade need a relogin on Wayland; tray pre-adds UUID to `enabled-extensions` and shows "log out and back in". |
| C10 | "No packaging change for embedded models" | Ship `third_party/punto-rs/{LICENSE,hunspell-ru-LICENSE,NOTICE.md}` under `/usr/share/licenses/gswitch-auto/`; RPM `License: MIT AND BSD-3-Clause-Modification AND LicenseRef-Fedora-Public-Domain`; uk model excluded. |
| C11 | "Tray menu has no checkbox ⇒ title only" | Optional `checkboxTrayBackend` (fyne systray `AddMenuItemCheckbox`) with title fallback for XEmbed. |
| C12 | "RAlt BufKiller empties the buffer before the selection chord" | Chord handler calls `s.converter.ClearBuffer()` explicitly. |
| C13 | "Chord detector must be nil-receiver safe" / reset at Flush | Value-typed zero-safe `ChordDetector`; resync from kernel `EVIOCGKEY` after `Flush()` (`:917`) and inside `convertSelection`'s defer after `drainEventChan()` (`:983-991`). |
| C14 | "Default chords pass validation, soft warning only" | Order-dependent conflict analysis against effective GNOME accelerators (`userconfig.SwitchConflict`); R4 default classified `OrderDependent` ⇒ warning + daemon restores the layout if it flipped during the chord. |
| C15 | "Key picker needs only new contexts" | Also: chord presets in combos, bare Enter/Escape passthrough, focus-out clears pressed set, daemon `SuspendHotkeys` during capture. |
| C16 | MAX_ALT_COST 6.0 (02-designs) | 7.0 (`layout.rs:87`, punto-rs c836c87); eval gates depend on it. |
| C17 | "36 EN/RU assertions" | Port the EN/RU-pair assertions of `tests.rs` (`:78-149`, `:206`, `:217-219`) plus the 7 hyphen cases of `:227-245` re-targeted to EN/RU; drop the vacuous convert_text placeholder. |
| C18 | Line refs | `ConvKey` copied at `switcher.go:123`, `LSKeys` at `:193` (`:187` is the conflict check, `:175` writes detected keys into `Config`); layout reader literal at `switcher.go:860`; `runner.go` is 41 lines (wire between `:23` and `:29`). |
| C19 | godbus auth "EXTERNAL with os.Geteuid()" | Use plain `dbus.Connect(addr, dbus.WithContext(ctx))` (godbus sends `AUTH EXTERNAL` with empty initial response; IBus accepts same-uid peers only). |
| C20 | Session-bus IBus name usable | Never address `org.freedesktop.IBus` on the session bus (activatable with `ibus-daemon --replace`); only `NameOwnerChanged` matches and `GetNameOwner` on `org.freedesktop.DBus`. |
| C21 | "nfpm contents = goreleaser contents" | nfpm.yaml must list `/usr/bin/gswitch` and `/usr/bin/gswitch-tray` explicitly (goreleaser added them via `builds`/`bindir`). |
| C22 | "Fedora file triggers make upstream scriptlets redundant" | Keep upstream scriptlets unchanged (modprobe uinput, udevadm reload/trigger, per-user restart); only icon cache/desktop DB/daemon-reload are duplicated. |
| C23 | ui.conf dir is 0700 | `tray/app.go:352` creates it 0750; every writer (the tray only) does `MkdirAll(0700)` then `Chmod(0700)`; nothing relies on systemd for the mode and no test asserts a systemd-set mode. |
| C24 | Focus change clears the buffer | Not done (would alter R3 behaviour); engine rejects words during which focus changed (Seq check) and `TryUndo` refuses after a focus change. |
| C25 | "Flush/drain only drops keys that are irrelevant to the next word" (refuted: 03-judgements:214-229 point 2, :315-325 item 5) | `afterInputDiscarded` ⇒ `AutoEngine.Reset(keysDown)` ⇒ `WordTaint.MarkDiscarded()`: the word in progress is tainted `discarded` until the next boundary processed **after** the discard (§3.2.4, §3.8). |
| C26 | "The auto replay is confirmed because upstream confirms" (refuted: 03-judgements:130 item 4 — confirmation only with `layout-switch=auto` + GNOME detection, `switcher.go:174-177,859`) | `emitOptions.confirm`: the auto path always confirms through the tracker reader, independent of the verify flags (§3.1.4). |
| C27 | "Sharing `dbus.SessionBus()` in the tray is harmless" | fyne.io/systray v1.12.2 `stayRegistered` (`systray_unix.go:273-305`) receives every signal of the shared connection, returns on `len(Body)<3` and re-registers on a string `Body[2]`. All fork D-Bus clients (both processes) use private connections (`dbus.ConnectSessionBus()`) and close only their own (§3.9.2, §3.5.2). |
| C28 | "A writable `prop` property callback may call the controller synchronously" | godbus v5.2.2 `prop.Properties.Set` holds `p.mut` while running the callback (`prop/prop.go:310-333`) and `SetMust` takes it again (`:341-343`) ⇒ self-deadlock. The callback only enqueues; one Service goroutine owns all property writes and signal emission (§3.9.2). |

---

## 2. Architecture

### 2.1 Components

```
 ┌──────────────── gnome-shell (session) ─────────────────┐      ┌──────── ibus-daemon ────────┐
 │ extension gswitch-focus@vabuter.github.io              │      │ private bus (~/.cache/ibus) │
 │  org.gnome.Shell.Extensions.GswitchFocus (session bus) │      │  GlobalEngineChanged(s)     │
 └───────────────▲────────────────────────────────────────┘      └──────────────▲──────────────┘
                 │ FocusChanged / InputPurposeChanged / Disabling              │
 ┌───────────────┴──────────── gswitch daemon (user unit) ─────────────────────┴──────────────┐
 │ focus.Client ──snapshot──┐                       layoutstate.Tracker (IBusSource,gsettings)│
 │                          ▼                                   │ State{Index,Gen,Confidence} │
 │ evdev ─► InputReader ─► eventChan ─► Run goroutine ──────────┤                             │
 │                                       processKeyEvent        ▼                             │
 │   forkPreProcess: layout hint, AutoEngine.Observe (held set, taint), ChordDetector.Feed    │
 │   upstream: custom key / Ctrl skip / Converter.Push / Process / performConversion          │
 │   Space K_DOWN + ActionNone ─► AutoEngine.OnSpace ─► lastWordRange ─► renderWord           │
 │        ─► vetoes ─► lang.Detector.Decide ─► arm(deadline)                                  │
 │   loop tick ─► AutoEngine.Tick ─► gates ─► Converter.ConvertRange ─► emitConversionEvents  │
 │        ─► VirtualKeyboard (uinput) ─► compositor                                           │
 │ userconfig.Watch(~/.config/gswitch) ─► applyUserConfig (atomic swap)                        │
 │ ipc.Service (io.github.vabuter.GswitchAuto) ◄──────────────────────────────┐               │
 └─────────────────────────────────────────────────────────────────────────── │ ──────────────┘
                                                                              │ session bus
 ┌──────────────────────── gswitch-tray (GTK3) ───────────────────────────────┴──────────────┐
 │ ipc.Client · menu checkbox + badge · Settings: General | Auto correction                   │
 │ userconfig writer (auto.conf, exceptions-*.txt) · focus_extension (org.gnome.Shell.Ext…)   │
 └───────────────────────────────────────────────────────────────────────────────────────────┘
```

### 2.2 Data flow: key event → correction

1. **Read.** `processKeyEvents` (`switcher.go:505`) reads EV_KEY events and `enqueueEvent`s them
   (never dropped, `:1097-1103`). The virtual keyboard is blacklisted (`:430-432`), so the
   daemon never sees its own replay.
2. **Pre-process (fork).** `processKeyEvent` (`:661`) → after `trackSelectionModifiers` (`:664`)
   the single fork hook `if s.forkPreProcess(event) { return }`:
   a. `observeLayoutSwitchChord` — when all `LSKeys` are held simultaneously and `tracker != nil`, `Tracker.NoteSwitchHint()` (layout state → Pending for `layout-hint-timeout-ms`).
   b. `AutoEngine.Observe(code, value, ctrlHeld)` — updates the physically-held set, the
      `WordTaint`, the word-start snapshot (layout generation, focus Seq; re-taken at the first
      text K_DOWN after any word boundary, §3.8); any K_DOWN or K_REPEAT cancels a pending
      (armed) fix; K_UP never cancels.
   c. If hotkeys are suspended (tray capture dialog): return true (event consumed).
   d. `ChordDetector.Feed` — on fire: `Converter.Push` the final key-up, `handleChord`, return true.
3. **Upstream path** unchanged: custom convert key (`:666`), Ctrl+letter skip (`:671-674`),
   `Converter.Push` (`:677`), `Process` (`:680`), gestures, `performConversion`.
4. **Space decision.** In the `ActionNone` case after `Process` for `KEY_SPACE` K_DOWN:
   `AutoEngine.OnSpace(converter)`. Pipeline (each failure logged with a reason code, counted in
   `Skipped`): enabled → `lastWordRange` → taint → focus gate (Seq unchanged since word start,
   policy allows) → layout gate (Confirmed, generation unchanged since word start) → `renderWord`
   over `[Start, TextEnd)` (shown = active layout, alt = other) → lexical veto → shift-shape veto
   (computed over the alt-word core, so edge punctuation such as `ghbdtn?` → `привет,` is not
   vetoed) → `Detector.Decide`.
   On `Fix`: arm with `deadline = now + auto-holdoff-ms` and a copy of the word events.
5. **Fire.** The Run loop waits `min(100 ms, time to deadline)` (replaces the fixed
   `EventLoopTimeoutMs` at `:461`) and calls `AutoEngine.Tick` after each loop iteration. Gates:
   still enabled, min interval, Space released, held set empty, `InputReader.AnyKeyDown()==false`
   (EVIOCGKEY), event channel and overflow empty, buffer tail unchanged, layout index+generation
   unchanged, focus allowed. Space still held at deadline ⇒ re-arm +30 ms (max 1 s total, then
   cancel). Then `events := converter.ConvertRange(r.Start, r.End)` (toggle + backspaces +
   replay incl. the Space) and `emitConversionEvents(events, emitOptions{reader:
   trackerLayoutStateReader{s.tracker}, confirm: true, …})`: the switch is **always** confirmed
   through the tracker-backed reader, whatever `verifyGNOMEWaylandLayoutSwitch` /
   `verifyGNOMEX11LayoutSwitch` say (they are set only for `layout-switch=auto` with a GNOME
   detection, `switcher.go:174-177`; with explicit keys such as `layout-switch=56+42` the manual
   path stays blind exactly as upstream). Confirmation fails closed before any text mutation, as
   upstream `:872-875`; per-event delay `auto-delay-ms`, post-switch pause `auto-switch-delay-ms`,
   no trailing sleep, no `Flush()`, no `CompleteConversion`.
6. **After fire.** `invalidateUndo()` (bumps `UndoGeneration`), record `lastFix`, mark the
   resulting layout change as own, emit IPC `Corrected`, stats. The layout stays switched
   (Punto semantics).
7. **Undo.** The raw convert-word gesture (bare double-Shift or the unmodified custom convert
   key, i.e. `action == ActionConvertWord` **before** the ReverseMode swap) immediately after a
   fix and in the same focus ⇒ `TryUndo` replays `ConvertRange` of the same range (toggle back,
   erase, retype) and optionally learns the shown word as an exception (`auto-learn-on-undo`;
   persisted by the tray, §3.11.1).

### 2.3 Runtime state machines

#### 2.3.1 AutoEngine

```
            Observe(K_DOWN|K_REPEAT any) / layout change / chord / disable / reload / Reset
   ┌───────────────────────────────────────────────────────────────────────┐
   ▼                                                                       │
 IDLE ──OnSpace: pipeline ok, verdict Fix──► ARMED{range, copy, deadline, layoutIdx, gen, seq}
   ▲                                          │ Tick, deadline due
   │                                          ├─ Space still held ─► ARMED (deadline+30ms; >1s ⇒ IDLE "space-held")
   │                                          ├─ any gate fails ───► IDLE (Skipped, reason)
   │                                          └─ gates ok ─► FIRING (sync emit) ─► IDLE + lastFix
   │                                                                          │
   └── TryUndo ok (gen + focus Seq unchanged, copy matches, only shift events after End) ◄┘
```

`lastFix` is disarmed by: any text K_DOWN or Backspace (generation bump), external layout change,
`Cancel(reason)` (chord, focus change, suspension, any manual conversion that is not the undo),
`Reset`, disable, config reload of chords, or a successful undo.

#### 2.3.2 ChordDetector (per configured chord set; exact-set semantics)

```
 IDLE ── K_DOWN making held == binding (nothing else held) ─► ARMED(binding)
 ARMED ── K_UP ─► RELEASING ; ARMED/RELEASING ── any K_DOWN (incl. re-press) ─► BROKEN
 RELEASING ── K_UP that empties held ─► FIRE(action) ─► IDLE
 BROKEN ── held becomes empty ─► IDLE   (never re-arms while BROKEN)
 K_REPEAT ignored. K_DOWN for a code already held ⇒ drop other held entries (lost key-ups).
```

No timers. Upstream double-Shift matching is pure event order (`converter.go:202-330`); the
held-Shift phrase gesture's second tap is a K_DOWN while Shift1 is held, which BREAKS an
LShift+RShift chord, so the chord can never coincide with an upstream gesture.

#### 2.3.3 Gesture interplay (who handles what)

| Event sequence | Result |
|---|---|
| `L↓ L↑ L↓ L↑` after text | upstream gesture; when an auto fix is armed for undo, `TryUndo` (the intercept runs **before** the ReverseMode swap on the raw `ActionConvertWord`, so a bare double-Shift undoes in both modes; with ReverseMode=true the held-Shift gesture falls through to upstream, where it becomes ConvertWord over the same range) |
| `L↓ R↓ R↑ R↓ R↑ L↑` | upstream held-Shift phrase/selection gesture; toggle chord BROKEN at 2nd `R↓` |
| `L↓ R↓ R↑ L↑` or `L↓ R↓ L↑ R↑` | toggle chord fires on last K_UP; converter buffer trimmed (`trimBuffer`), not cleared |
| `Ctrl↓ L↓ L↑ L↓ L↑` | upstream selection conversion (unchanged) |
| `L↓ RAlt↓ RCtrl↓` + releases in any order | selection chord fires; `ClearBuffer`; `deferSelectionUntilModifiersReleased(selectionConvertLayout)` |
| `Ctrl↓ L↓ R↓ R↑ L↑ Ctrl↑` | nothing (not an exact set) |
| any K_DOWN while ARMED | pending auto fix cancelled before upstream sees the key |
| Space while Ctrl/Alt/Meta held | never arms (held-modifier check; Ctrl+Space never reaches Push, `:671`) |

#### 2.3.4 Layout tracker confidence

`Unknown` (no source / IBus down / non-xkb engine / engine not in pair / ambiguous pair) →
`Confirmed` (IBus signal or property read) ; `Confirmed` → `Pending` on physical chord hint →
`Confirmed` on next signal, or `Resync` after the hint timeout (IBus `Query` first ⇒ `Confirmed`;
gsettings only when the query fails ⇒ `Settings`) ; `Settings` (gsettings only) never
enables auto-correction (gnome-shell writes `mru-sources` only on interactive switches,
`keyboard.js:526-527`). Every state change increments `Generation`.

### 2.4 Threading model

| Goroutine | Owns / does | Talks to Run goroutine via |
|---|---|---|
| Run (main loop) | Converter, AutoEngine, ChordDetector state, emission | — |
| processKeyEvents / processDeviceEvents / processInotifyEvents | upstream | `eventChan` |
| layoutstate.Tracker | IBus connection, state | `OnChange` → `enqueueControl(func)`; `State()` mutex read |
| focus.Client | session-bus focus client | `OnChange` → `enqueueControl`; `Snapshot()` atomic |
| userconfig watcher | inotify/poll of `~/.config/gswitch` (read-only for the daemon) | `applyUserConfig` swaps `atomic.Pointer`; engine-affecting parts via `enqueueControl` |
| ipc.Service (godbus goroutines) | D-Bus method handlers | atomics + `enqueueControl`; never touches Converter directly |
| ipc.Service `propLoop` / `requestLoop` (WP6) | the only goroutine that calls `prop.SetMust`/`conn.Emit`; runs `Controller.SetAutoEnabled` for property Sets outside the godbus property lock | `UpdateStatus`/`Notify*` never block (latest-wins slot + bounded queue, §3.9.2) |

`controlChan chan func()` (cap 32) gets a new `case fn := <-s.controlChan: fn()` in the Run
select. Sends are non-blocking (`enqueueControl` returns false when full and logs at debug).
Correctness never depends on delivery of a notification: the engine re-reads `Tracker.State()`
and `focus.Snapshot()` at decision and fire time (generation/Seq checks).

### 2.5 Sandbox constraints

Unit (`configs/gswitch.service:23-40`): `NoNewPrivileges`, `ProtectSystem=strict`,
`ProtectHome=read-only`, `ReadWritePaths=/tmp`, `PrivateTmp=false`, `RestrictSUIDSGID`,
`MemoryDenyWriteExecute`, `RestrictAddressFamilies=AF_UNIX AF_NETLINK`, `RestrictNamespaces`,
`LockPersonality`, `UMask=0077`.

| Need | Satisfied by | Verified |
|---|---|---|
| Read `~/.config/ibus/bus/*`, connect IBus socket in `~/.cache/ibus` | read-only home allows read; `connect()` to AF_UNIX on ro mount works | transient `systemd-run --user` with identical properties returned `xkb:ru::rus` |
| Session bus (`/run/user/1000/bus`) | AF_UNIX allowed; env inherited | live unit env |
| Read `~/.config/gswitch/*`, inotify on it | read-only allowed; inotify works on ro mounts; a missing directory is polled for (the daemon cannot create it) | test under same sandbox |
| Persist `auto-enabled`, learned words | **not done by the daemon**: every write under `$HOME` (temp file, rename, `.lock`) fails with EROFS under `ProtectHome=read-only` + `ReadWritePaths=/tmp` (`configs/gswitch.service:25,28`). The tray persists both on the daemon's behalf (§3.11.1) | unit unchanged (D2) |
| Embedded models (~2.2 MB rodata) | no W^X impact | — |
| EVIOCGKEY / EVIOCGLED | ioctls on already-open O_RDONLY fds | probed on event3 |
| No network | nothing new opens AF_INET | — |

No unit delta: `configs/gswitch.service` stays byte-identical to upstream and is listed among
the files intentionally not modified (§4). Because the daemon never writes under `$HOME`, there
is no bind mount pinning the directory inode: deleting and recreating `~/.config/gswitch` while
the daemon runs is picked up by the watcher (§3.6.4). Root compatibility mode (`sudo gswitch
--run`, euid 0): user config read-only, no IPC (hence no persistence), no IBus (IBus rejects
foreign uids), `tracker == nil`, auto engine never constructed (`root-mode`); manual conversion
runs the upstream path with the gsettings reader even when `verifyGNOMEWaylandLayoutSwitch` is
true (`switcher.go:936-945`), because `layoutReaderFn` is not installed (§3.10.2).

---

## 3. Package-by-package design

### 3.1 runtime foundation (WP0)

#### 3.1.1 `internal/gswitch/runtime/types_fork.go` (new; upstream `types.go` untouched)

```go
// Fork-only key codes and ioctls. Kept out of types.go to minimise rebase conflicts.
const (
	KEY_ESC       = 1
	KEY_TAB       = 15
	KEY_LEFTALT   = 56
	KEY_CAPSLOCK  = 58
	KEY_NUMLOCK   = 69
	KEY_102ND     = 86
	KEY_RIGHTALT  = 100
	KEY_DELETE    = 111
	KEY_RIGHTMETA = 126
)
const (
	EV_LED    = 0x11
	LED_CAPSL = 0x01
	EVIOCGKEY96   = 0x80604518 // EVIOCGKEY(96): 768-bit key state bitmap
	EVIOCGLED8    = 0x80084519 // EVIOCGLED(8)
	EVIOCGBITLED8 = 0x80084531 // EVIOCGBIT(EV_LED, 8)
)
```

#### 3.1.2 `runtime/keystate.go` (new)

```go
// KeysDown returns the union of keys currently down on all open devices (EVIOCGKEY).
// Errors on individual devices are ignored; err only when no device could be queried.
func (ir *InputReader) KeysDown() (map[uint16]bool, error)
// AnyKeyDown reports whether any key is down on any device; ioctl errors count as "not down".
func (ir *InputReader) AnyKeyDown() bool
// CapsLockState queries EVIOCGLED on the first open device advertising LED_CAPSL
// (EVIOCGBIT(EV_LED)). known=false when no device has the LED. Never uses sysfs paths.
func (ir *InputReader) CapsLockState() (on, known bool)
```

All three take `ir.mu.RLock()` and use the existing `Device.FD` (opened O_RDONLY|O_NONBLOCK at
`input_reader.go:214`).

#### 3.1.3 `runtime/converter.go` (modified)

- Extract `Convert` lines 557-610 (backspace pairs, replay with synthetic key-ups, extra shift
  releases) together with the LSKeys toggle prefix (`:505-511`) into:

```go
// ConvertRange returns the events that switch the layout, erase every non-shift,
// non-convert-key event in buffer[start:end) and replay that range. It returns nil only
// when the bounds are invalid (start < 0, end > len(buffer), start >= end). It does NOT
// check for text: a shift-only range yields the toggle plus the replayed shift events,
// exactly as upstream. Convert(action) keeps the whole-buffer HasText() gate (:492),
// computes startIndex exactly as today, then returns c.ConvertRange(startIndex, len(c.buffer)).
func (c *Converter) ConvertRange(start, end int) []KeyEvent
```
  Manual output must stay byte-identical: golden test on every existing fixture **plus** the
  shift-only-tail fixture `[a↓ L↓ Space↓ L↑]` + double-Shift (ConvertWord `startIndex=3`, range
  `[L↑]` has no text; upstream emits the LSKeys toggle and the replayed `L↑`,
  `converter.go:489-611`, and so must the refactor). Auto callers only pass ranges validated by
  `lastWordRange` (≥ 2 text keys).
- Add `undoGen uint64` to `Converter` (`:45-54`), incremented in `invalidateUndo()` (`:638-641`),
  and `func (c *Converter) UndoGeneration() uint64`. Shift pushes do not bump it; text K_DOWN,
  Backspace and `ClearBuffer` do (they already call `invalidateUndo`).

#### 3.1.4 `runtime/switcher.go` refactor (pure, no behaviour change)

Extract `performConversion` lines 848-914 into:

```go
type emitOptions struct {
	reader      layoutStateReader // nil => s.layoutReader()
	delay       time.Duration     // per emitted event (manual: config.Delay)
	switchDelay time.Duration     // pause after a confirmed switch (manual: config.LayoutSwitchDelay)
	// confirm forces the confirmed switch (triggerAndConfirmGNOMEWaylandLayoutSwitch with
	// reader) regardless of s.verifyGNOMEWaylandLayoutSwitch / s.verifyGNOMEX11LayoutSwitch.
	// Requires reader != nil. The auto path always sets it; the manual path never does.
	confirm bool
}
var errConfirmReaderMissing = errors.New("switch confirmation requested without a reader")
// emitConversionEvents releases shifts, performs the (confirmed) layout switch and replays
// events. It returns an error before any text mutation when switch confirmation fails.
// On success it calls s.onOwnSwitch when non-nil.
func (s *Switcher) emitConversionEvents(events []KeyEvent, opts emitOptions) error
func (s *Switcher) layoutReader() layoutStateReader // s.layoutReaderFn() when non-nil, else gnomeWaylandLayoutStateReader{env: s.sessionEnv}
```
Path selection inside `emitConversionEvents` (frozen in Phase 0; WP11 adds no options):
`opts.confirm && opts.reader == nil` ⇒ return `errConfirmReaderMissing` before emitting anything;
`opts.confirm || s.verifyGNOMEWaylandLayoutSwitch` ⇒ Wayland-style confirmed switch with `r :=
opts.reader; if r == nil { r = s.layoutReader() }` (the struct literal at `:860` is replaced by
`r`; the trigger-and-poll helper is session-agnostic, so `confirm` also works on GNOME X11 with
the tracker reader); else `s.verifyGNOMEX11LayoutSwitch` ⇒ upstream X11 path; else blind
(upstream). The manual call never sets `confirm`, so its path selection is byte-identical.
New fields: `layoutReaderFn func() layoutStateReader`, `onOwnSwitch func()` — both nil by
default and nil-checked at every use. `performConversion` keeps: ReverseMode swap, CanUndo log,
`Convert`, then `emitConversionEvents(events, emitOptions{delay: cfg.Delay, switchDelay:
cfg.LayoutSwitchDelay})`, then (only on success) the trailing `LayoutSwitchDelay` sleep (`:916`),
`Flush()` (`:917`), `CompleteConversion` (`:918`). (WP11 later inserts the undo intercept as the
first statement of `performConversion`, before the swap, §4.)

#### 3.1.5 `internal/gswitch/fold` (new leaf package, WP0)

The single normaliser for exception words and application keys; no other word or app
normaliser may exist in the fork (WP1, WP2, WP4, WP5, WP8, WP9 call these).

```go
// Word folds a word exactly as punto-rs shown_word does (lowercase layout.rs:276-284 +
// word_core :310-320 = shown_word :323-328): per-rune
// unicode.ToLower, 'ё'→'е', then strip leading/trailing runes that are not unicode.IsLetter.
// Interior punctuation is kept ("k.,jdm" stays). Returns "" when no letter remains.
func Word(s string) string
// AppKey normalises a desktop app id or a WM_CLASS: strings.TrimSpace, strings.ToLower,
// strip one trailing ".desktop". Idempotent; allocation-free on already-normalised input.
func AppKey(s string) string
```
Tests (`fold_test.go`): `Ghbdtn,`→`ghbdtn`, `ЁЖИК!`→`ежик`, `k.,jdm`→`k.,jdm`, `"привет"`→`привет`,
`123`→``, `org.gnome.Ptyxis.desktop`→`org.gnome.ptyxis`, `  Kitty `→`kitty`, idempotence of both.

### 3.2 Word extraction, rendering, taint (WP2, package runtime)

#### 3.2.1 `runtime/xkbnames.go`

```go
// XKBKeyName maps an evdev scancode to the XKB key name used as LayoutInfo.KeyMap key
// (no angle brackets). X keycode = evdev + 8 (/usr/share/X11/xkb/keycodes/evdev, minimum = 8).
//   41→TLDE, 2..13→AE01..AE12, 16..27→AD01..AD12, 30..40→AC01..AC11, 43→BKSL,
//   44..53→AB01..AB10, 86→LSGT (48 entries); everything else ok=false.
func XKBKeyName(code uint16) (name string, ok bool)
```
Static table (the evdev keycodes file may be absent on minimal systems).

#### 3.2.2 `runtime/layout.go` (modified)

- `LayoutInfo` (`:88-92`) gains `Unmapped map[string]bool` — true when any keysym in the key's
  first group yields 0 from `keysymToRune` (dead_*, NoSymbol, VoidSymbol, Multi_key). KeyMap
  behaviour stays identical (so `NewLayoutConverter` is unaffected); initialise in the literals
  at `:398-402`, `:413-417`, merge in the same-file include branch (`:467-470`), set at
  `:483-486`.

#### 3.2.3 `runtime/auto_word.go`

```go
type wordRange struct {
	Start    int // first event of the word (after previous SPACE/ENTER/KPENTER, or 0)
	TextEnd  int // index of the trailing SPACE K_DOWN; [Start,TextEnd) is the word
	End      int // TextEnd+1; [Start,End) is erased and replayed by ConvertRange
	TextKeys int // non-shift events in [Start,TextEnd)
}
// lastWordRange applies, in order (reason codes in quotes):
//  1. skip trailing shift K_UP events; a trailing shift K_DOWN ⇒ "shift-held"
//  2. next event must be KEY_SPACE K_DOWN ⇒ else "no-trailing-space"
//  3. last non-shift event before it must exist and be a text key ⇒ "empty"/"double-space"/"enter-boundary"
//  4. Start = after previous SPACE/ENTER/KPENTER (or 0)
//  5. every non-shift event in [Start,TextEnd) has XKBKeyName ok ⇒ else "unrenderable-key"
//     (rejects keypad codes 55,71-83,98, Enter, convert-key events)
//  6. no shift held at Start (scan buf[:Start]) ⇒ else "shift-held-at-start"
//  7. 2 <= TextKeys <= maxLetters ⇒ else "too-short"/"too-long" (1-key words never fixed)
func lastWordRange(buf []KeyEvent, maxLetters int) (wordRange, string, bool)

var ErrUnrenderable = errors.New("word contains a key that cannot be rendered")
type renderedWord struct {
	Shown, Alt    string // one rune per text key, equal rune counts
	Keys          int
	// Core = key positions [CoreStart, CoreEnd) left after dropping leading and trailing keys
	// whose shown OR alt rune is not unicode.IsLetter (edge punctuation such as "?"/"," in
	// "ghbdtn?"→"привет,", "!" in "Ghbdtn!"/"руддщ!"). Interior keys are never dropped
	// ("k.,jdm"→"любовь" keeps all six). CoreKeys == 0 when no key qualifies.
	CoreStart, CoreEnd, CoreKeys int
	ShiftedKeys   int  // CORE keys typed with Shift held
	InteriorShift bool // a shifted CORE key at core position > 0
	CapsLock      bool // Caps applied (false when unknown)
}
// renderWord tracks Shift inside [start,end) exactly like the replay (converter.go:579-591);
// level = shift XOR (caps && alphabetic(key)), alphabetic := unicode.IsLetter(r0) &&
// unicode.ToUpper(r0) == r1 (TWO_LEVEL keys such as ',' '<' ignore Caps). Bail with
// ErrUnrenderable on: no XKB name, name missing in either KeyMap, level missing, Unmapped[name].
// Callers pass the word WITHOUT its trailing Space: OnSpace calls
// renderWord(c.buffer, r.Start, r.TextEnd, …). Passing r.End would include KEY_SPACE (57),
// which has no XKBKeyName, and every word would fail with ErrUnrenderable.
func renderWord(buf []KeyEvent, start, end int, shown, alt *LayoutInfo, capsLock bool) (renderedWord, error)

func lexicalVeto(shown string) (string, bool)  // digit anywhere, leading '/', '~', '-', or contains "://", "@", `\`
                                               // NOT all punctuation: "k.,jdm"→"любовь" must stay fixable
func shiftShapeVeto(w renderedWord) bool       // CoreKeys > 0 && ((ShiftedKeys>=2 && ShiftedKeys==CoreKeys) || InteriorShift)
```
Shift-shape is computed from Shift, never from runes, so CapsLock cannot veto everything, and
only over the core, so Shift-typed edge punctuation (ru winkeys `,` = Shift+AB10, `!?:;` =
Shift+AE01/AE07/AE06/AE04, `symbols/ru:16-29`; us `!?:"`) never vetoes a word. Word folding
for exceptions and undo bookkeeping uses `fold.Word` (§3.1.5); WP2 defines no folding helper.

#### 3.2.4 `runtime/taint.go`

```go
var ErrTainted = errors.New("word diverged from screen")
type WordTaint struct{ /* tainted bool; reason string; inWord, pendingBoundary bool; metaDown int */ }
// Observe must see every EV_KEY event before Converter.Push.
func (t *WordTaint) Observe(code uint16, value int32, ctrlHeld bool)
func (t *WordTaint) Tainted() (bool, string)
// Reset clears the taint (a word boundary that is known to match the screen).
func (t *WordTaint) Reset()
// MarkDiscarded taints the word in progress with reason "discarded" because keystrokes that
// the application already received were dropped (InputReader.Flush, drainEventChan,
// end of SuspendHotkeys). The taint survives until the next boundary processed AFTER the
// call (the next text K_DOWN after that boundary clears it; a non-Ctrl BufKiller clears it at
// once). A Space boundary that is then erased by Backspace does not count: the word resumes
// and stays tainted.
func (t *WordTaint) MarkDiscarded()
```
Rules: taint on K_REPEAT of any `Letters` key (includes Backspace 14 — the converter consumes
Backspace repeats at the kernel rate, `converter.go:134`, while mutter repeats at 500/30 ms);
K_DOWN of `KEY_DELETE`, `KEY_102ND`, `KEY_CAPSLOCK`, `KEY_NUMLOCK` while `inWord`; any text key
while `KEY_LEFTMETA`/`KEY_RIGHTMETA` is down; a `Letters` key while `ctrlHeld`; K_DOWN of any
code not in `Letters ∪ Shifts ∪ BufKillers`; `MarkDiscarded()` (input was dropped, C25).
**Word boundaries** (one definition, shared with the engine's word-start snapshot, §3.8):
SPACE/ENTER/KPENTER K_DOWN set `pendingBoundary`; the next text K_DOWN resets (so the engine
reads the taint of the finished word after Observe); Backspace K_DOWN while `pendingBoundary`
clears it (the boundary was erased, the word resumes); a BufKiller K_DOWN except Ctrl (mirrors
`converter.go:105-108`, i.e. every `Push`-side `ClearBuffer`) resets immediately. `Push` also
clears the buffer on a non-Ctrl BufKiller **K_UP**: outside a word that release resets too (and
invalidates the engine's word-start snapshot), but inside a word (a text key buffered since the
last reset) it taints the word `killer-release`, because the letters typed between the killer's
press and release stay on screen (Tab/arrow rollover `Tab↓ c Tab↑ rfpfk␣` shows `crfpfk `
while the buffer holds `rfpfk␣`; a fix would leave `cказал `). A
`MarkDiscarded` taint is cleared only by a boundary observed after it: e.g. the user starts a
word during a selection conversion, `gh` reaches the app but is drained, `bdtn` is buffered ⇒
Space finds `tainted:discarded` and does not turn the screen into `ghидет `. CapsLock before a
word starts does not taint. Taint never counts characters; it only marks the word unfixable.

### 3.3 Language detector (WP1): `internal/gswitch/lang`

Bit-exact port of punto-rs `cli/src/layout.rs` (commit c836c87; MIT, "Copyright (c) 2026
netherguy4", repo github.com/rashevskyv/punto-rs), EN/RU only. No `runtime` import.

```go
type Lang uint8
const ( Unknown Lang = iota; En; Ru )
func LangForLayout(xkbLayoutName string) (Lang, bool) // "us"→En, "ru"→Ru; variants ignored
type Pair struct{ Shown, Alt Lang }

type Options struct {
	MinLetters int     // 3   (auto-min-letters, 2..8)
	MinMargin  float64 // 1.0 bits/letter (auto-margin, 0..5)
	MaxAltCost float64 // 7.0 (auto-max-alt-cost, 3..25.5) — layout.rs:87
	ShortWords bool    // true (auto-short-words): 2-key closed lists (keymap.rs:80-88)
}
func DefaultOptions() Options
func (o Options) Validate() error

type Scores struct{ ShownCost, AltCost float64; ShownKnown, AltKnown bool }
type Verdict uint8
const ( Keep Verdict = iota; Fix; NotCandidate )
type Decision struct {
	Verdict Verdict
	// Fix: "scores" | "short-list" | "compound"
	// Keep: "exception" | "shown-known" | "alt-unknown" | "alt-cost" | "margin"
	// NotCandidate: "digits" | "non-letter" | "too-short" | "unreadable-alt" | "unsupported-pair"
	Reason string
	Scores *Scores // nil when not scored
}
type Detector struct{ /* tables, builtin + user exceptions (atomic.Pointer[map[string]struct{}]), opts (atomic) */ }
func NewDetector(opts Options) (*Detector, error) // validates blob lengths: 27³, 33³, bloom bits = len*8
func (d *Detector) Decide(shown, alt string, pair Pair) Decision
func (d *Detector) Scores(shown, alt string, pair Pair) (Scores, bool)
func (d *Detector) SetOptions(opts Options) error             // atomic swap (hot reload)
func (d *Detector) SetUserExceptions(words []string)          // fold.Word each, drop '#'-lines/empty; atomic swap
func (d *Detector) AddUserException(word string)              // fold.Word, copy-on-write add (learn-on-undo)
func (d *Detector) IsException(shown string, l Lang) bool     // fold.Word(shown) in built-in ∪ user set (punto-rs is_exception, layout.rs:331)
func (d *Detector) Known(l Lang, word string) bool            // debug/CLI
func (d *Detector) Cost(l Lang, word string) (float64, bool)
```

Model (internal, bit-exact): alphabets 26/32 (ё→е), N=27/33, trigram index `(a·N+b)·N+c` over
`^^word$`, cost = Σ table[idx] / count / 10 (`layout.rs:117-122,151-157`); Bloom: 7 probes,
FNV-1a folding each letter **index** as a u64 word (not bytes), seeds `0xcbf29ce484222325` /
`0x84222325cbf29ce4`, `step |= 1`, `bit_i = (first + i·step) mod (len·8)` (`layout.rs:125-148`);
decision `alt_known && !shown_known && alt_cost <= MaxAltCost && shown_cost − alt_cost >=
MinMargin` (`layout.rs:83-87,189-194`), UNREADABLE 25.5; candidate filter, 2-key short lists
(`keymap.rs:80-88`), hyphen compounds (`layout.rs:259-273`), built-in exceptions
(`layout/exceptions.txt`, 20 words, stored as `fold.Word` output). `Decide` mirrors
`wrong_layout` (`layout.rs:234-257`), where the three Fix paths are **OR-ed independently**:

1. pair unsupported ⇒ `NotCandidate("unsupported-pair")`;
2. `IsException(shown)` ⇒ `Keep("exception")`;
3. `sc, why, ok := scores(shown, alt)` — the candidate filter (`digits`, `non-letter` inside the
   alt core, `too-short` = alt core < `MinLetters`, `unreadable-alt`) lives **only** inside this
   step, as `MIN_LETTERS` does inside `scores()` (`layout.rs:199-226`, check at `:211`);
   `ok && shouldSwitch(sc)` ⇒ `Fix("scores")`;
4. `ShortWords && runeCount(shown) == 2 && shortWrong(shown, alt) && !shownKnown(shown)` ⇒
   `Fix("short-list")` — evaluated even when step 3 returned `!ok` (`layout.rs:250-253`), so
   `jy`→`он` is fixed although its alt core is shorter than `MinLetters`;
5. hyphen compound (`layout.rs:259-273`, recursing into steps 3–4 per part) ⇒ `Fix("compound")`
   — also evaluated when step 3 returned `!ok` (`nfr-nfrb` has a non-letter inside the alt core);
6. otherwise `!ok` ⇒ `NotCandidate(why)`, else `Keep` with the first failing `shouldSwitch`
   condition (`shown-known` | `alt-unknown` | `alt-cost` | `margin`).

`shown`/`alt` carry one rune per key, so `runeCount(shown)` equals punto-rs `keys.len()`; a
2-key word with trailing punctuation is 3 keys and, as in punto-rs, leaves the short-list path.
Split into helpers (funlen/gocognit).

Files: `lang.go`, `model.go` (`//go:embed data/en.bin data/ru.bin data/en.bloom data/ru.bloom`),
`detector.go`, `exceptions.go`, `data/{en.bin (19,683 B), ru.bin (35,937 B), en.bloom (444,422 B),
ru.bloom (1,708,013 B), exceptions.txt, README.md}` (blobs copied unchanged from punto-rs
`cli/src/layout/`; `data/README.md` = provenance pointer to `third_party/punto-rs/NOTICE.md`),
`testdata/layout_cases.tsv` (copy of punto-rs `cli/tests/data/layout_cases.tsv`, 2,403 rows,
uk rows skipped), `rows_test.go` (test-only port of `keymap.rs` rows = oracle).
Licences (C10): `third_party/punto-rs/LICENSE` (verbatim MIT), `third_party/punto-rs/hunspell-ru-LICENSE`
(verbatim, (c) 1997-2008 Alexander I. Lebedev, SPDX BSD-3-Clause-Modification),
`third_party/punto-rs/NOTICE.md` (states that `ru.*` is a **modified form** of hunspell-ru
0.99g5 — Bloom filter + letter-trigram derivative, citing punto-rs `cli/LICENSES/README.md:10-15`;
`en.*` derives from Fedora `words` /usr/share/dict/words, public domain; uk model intentionally
excluded: MPL-1.1 hunspell-uk + CC BY-SA FrequencyWords; credits golang.design/x/clipboard).

Golden values (from the Python reference validated against the real punto-rs Rust code; if the Go
port disagrees, the Rust harness is the arbiter): `bloomBits(привет, 13,664,104 bits)` =
`[4112785 6389110 3647715 906320 11829029 441250 11363959]`; `bloomBits(hello, 3,555,376)` =
`[1695371 1975120 573877 853626 1133375 3287508 11881]`; cost(en,ghbdtn)=7.6142857,
cost(ru,привет)=2.8428571, cost(en,hello)=3.7666667, cost(ru,ршщзх)=9.1 (1e-9); Scores(ghbdtn,
привет, us→ru) = {7.6142857, 2.8428571, false, true}; tls = {6.375, 5.175, false, true} (fix
blocked only by exception); dnf = {10.325, 4.275, false, false}.

### 3.4 Layout tracker (WP3): `internal/gswitch/layoutstate`

Imports only `detect`, `config`, godbus. Not imported by anything except `runtime` and `cli`.

```go
type Confidence uint8
const ( Unknown Confidence = iota; Pending; Settings; Confirmed )
type Layout struct{ Name, Variant string }
type State struct {
	Index      int       // 0 = Layout1, 1 = Layout2, -1 = not one of the pair
	Engine     string    // e.g. "xkb:ru::rus"; "" when none
	Confidence Confidence
	Generation uint64    // increments on every state change
	At         time.Time
	Source     string    // "ibus" | "gsettings" | ""
}
type Options struct {
	Layouts     [2]Layout
	HintTimeout time.Duration // layout-hint-timeout-ms, default 400ms
	Logger, DebugLogger func(format string, args ...any)
	Now         func() time.Time
}
func New(opts Options) *Tracker
func (t *Tracker) AddSource(src Source)
func (t *Tracker) AddResync(r Resyncer)
func (t *Tracker) Start(ctx context.Context) // non-blocking
func (t *Tracker) Stop()                     // cancel and wait
func (t *Tracker) State() State              // mutex read, any goroutine
func (t *Tracker) Current() (idx int, known bool) // known == Confirmed && Index >= 0
func (t *Tracker) OnChange(fn func(State))   // called on tracker goroutine; must not block
func (t *Tracker) NoteSwitchHint()           // Pending; after HintTimeout without signal ⇒ Resync. nil receiver ⇒ no-op
// Resync queries every Source in order (Source.Query, 500 ms each); the first success is applied
// like a signal ⇒ Confirmed. Only when every Query fails are the Resyncers consulted ⇒ Settings;
// all fail ⇒ Unknown. (A GlobalEngineChanged that the tracker processed before the Run goroutine
// delivered the physical hint must not leave the state at Settings: Query restores Confirmed.)
func (t *Tracker) Resync(ctx context.Context) error

type Source interface {
	Name() string
	Run(ctx context.Context, sink Sink)               // blocks; reconnects internally
	Query(ctx context.Context) (engine string, err error)
}
type Sink interface { Engine(source, name string); Available(source string, ok bool) }
type Resyncer interface { Current(ctx context.Context) (Layout, error) }

func ParseXKBEngine(name string) (layout, variant string, ok bool) // "xkb:<layout>:<variant>:<lang>"; non-xkb ⇒ ok=false
```

Engine→index mapping: Name must equal; if both variants are non-empty they must be equal; if
exactly one is empty it matches only when the other pair member has a different Name;
identical/ambiguous pair ⇒ `Unknown`. Identical consecutive engines are deduplicated (no
generation bump) — IBus itself only emits on change (`ibusimpl.c:1094-1106`).

`ibus_address.go` ports `ibus_get_socket_path`/`ibus_get_address` (`ibusshare.c:46-73,130-199,228-280`):

```go
type AddressEnv struct {
	IBusAddress, IBusAddressFile, WaylandDisplay, Display, ConfigDir string
	MachineIDPaths []string        // {"/var/lib/dbus/machine-id", "/etc/machine-id"}
	Alive          func(pid int) bool
}
func AddressEnvFromProcess() AddressEnv
func SocketPath(e AddressEnv) (string, error) // <ConfigDir>/ibus/bus/<machine-id>-<host>-<display>; WAYLAND_DISPLAY wins ("unix", verbatim)
func ResolveAddress(e AddressEnv) (addr string, pid int, err error) // IBUS_ADDRESS env wins; parse IBUS_ADDRESS=/IBUS_DAEMON_PID=; dead pid ⇒ ErrIBusNotRunning; glob fallback newest live file
var ErrIBusNotRunning error
```

Constructors used by WP11 (they take the environment, not a resolved address, because the
socket file and pid change on `ibus restart`, M12):

```go
func NewIBusSource(env AddressEnv, logf func(string, ...any)) *IBusSource // re-runs ResolveAddress(env) on every (re)connect
func NewGSettingsResync(env *detect.SessionEnv) *GSettingsResync
```

`ibus_source.go` — `IBusSource` (implements `Source`):
- `dbus.Connect(addr, dbus.WithContext(ctx))` (C19) with a 2 s connect deadline.
- `AddMatchSignal(interface=org.freedesktop.IBus, member=GlobalEngineChanged,
  path=/org/freedesktop/IBus)` (no `sender=`) **before** reading
  `org.freedesktop.DBus.Properties.Get("org.freedesktop.IBus","GlobalEngine")` (avoids the
  deprecated-method g_warning of `GetGlobalEngine`, `ibusimpl.c:1722`; fall back to
  `GetGlobalEngine` only on `UnknownProperty`). `engineNameFromDesc(v any) (string, error)`
  unwraps **every** `dbus.Variant` level first (loop, max 3): `Properties.Get` on the private
  bus returns a double-wrapped value `<<('IBusEngineDesc', {}, 'xkb:ru::rus', …)>>`, the
  `GetGlobalEngine` reply and the signal argument one level; then it takes `IBusEngineDesc`
  struct field [2] (name). Golden tests pin all three shapes. Error "No global engine." ⇒
  `Engine("")`.
- Signal loop exits on channel close (godbus closes on `Close`) and on `conn.Context().Done()`
  ⇒ `Available(false)` ⇒ reconnect loop, backoff 200 ms…5 s, re-running `ResolveAddress`;
  ECONNREFUSED/ENOENT/"No global engine" are retryable.
- Optional accelerator: session bus match `NameOwnerChanged arg0='org.freedesktop.IBus'`
  (sender `org.freedesktop.DBus`) wakes the loop; it uses a private session connection
  (`dbus.ConnectSessionBus()`, C27), never `dbus.SessionBus()`. **Never** send any message to destination
  `org.freedesktop.IBus` on the session bus (C20); unit test asserts it.
- Enabled when `layout-source=auto` && `detect.IsGNOMESession(env)` && euid != 0, or
  `layout-source=ibus` && euid != 0.

`gsettings_resync.go` — `GSettingsResync` (`Resyncer`): `detect.RunGsettings(env, "get",
"org.gnome.desktop.input-sources", "mru-sources")`, fallback `sources`; own tuple parser modelled
on `runtime.parseGnomeInputSources` (`layout.go:187`) to avoid an import cycle; confidence
`Settings` only.

`diag.go` — `func RunDiagnostics(ctx context.Context, w io.Writer, env AddressEnv, layouts [2]Layout, watch time.Duration) error`
for `gswitch --layout-state` (prints socket path, address, pid, current engine, mapped index,
streams changes for `watch`).

`runtime/layout_reader.go` (WP3, runtime package):

```go
// trackerLayoutStateReader implements layoutStateReader (gnome_wayland_layout_switch.go:16-18).
// CurrentState returns fmt.Sprintf("%d@%d", st.Index, st.Generation) when Confirmed, else an
// error (also when tracker is nil). Used directly by the auto path (emitOptions.confirm).
type trackerLayoutStateReader struct{ tracker *layoutstate.Tracker }
// newCompositeLayoutReader returns a func that yields the tracker reader when t != nil and t is
// Confirmed at call time, else gnomeWaylandLayoutStateReader{env}. newCompositeLayoutReader(nil,
// env) is valid and always yields gnomeWaylandLayoutStateReader{env}. WP11 installs it as
// s.layoutReaderFn only when s.tracker != nil (§3.10.2).
func newCompositeLayoutReader(t *layoutstate.Tracker, env *SessionEnv) func() layoutStateReader
```
`triggerAndConfirmGNOMEWaylandLayoutSwitch` (`gnome_wayland_layout_switch.go:43-89`) is reused
unchanged; with the tracker each poll is a mutex read instead of a `gsettings` spawn.

### 3.5 Focused application (WP5): Shell extension + `internal/gswitch/focus`

#### 3.5.1 Extension

Layout: `shell-extension/gswitch-focus@vabuter.github.io/{metadata.json,extension.js}` and
`shell-extension/README.md`. UUID = directory name = `focus.ExtensionUUID`.

`metadata.json`:
```json
{"uuid": "gswitch-focus@vabuter.github.io", "name": "gswitch Focus Reporter",
 "description": "Reports the focused application's desktop id, WM_CLASS and text-field purpose over the session D-Bus for the gswitch-auto daemon. No window titles or text are exposed.",
 "shell-version": ["50"], "version": 1, "url": "https://github.com/vabuter/gswitch-auto"}
```
No `session-modes` (default `["user"]`; the Shell disables it on lock, `extensionSystem.js:575-581`;
do **not** add `unlock-dialog`). No prefs, no schema.

`extension.js` — ESM, `import {Extension} from 'resource:///org/gnome/shell/extensions/extension.js'`,
`export default class GswitchFocusExtension extends Extension { enable(); disable(); }`.
Export idiom as appindicator v64 (`statusNotifierWatcher.js:49-66,123-125,301-304`):
`Gio.DBusExportedObject.wrapJSObject(IFACE_XML, this)`, `.export(Gio.DBus.session, OBJECT_PATH)`,
`Gio.DBus.session.own_name(BUS_NAME, Gio.BusNameOwnerFlags.NONE, null, null)`.
Focus: `global.display.focus_window`, `global.display.connectObject('notify::focus-window', …)`,
`Shell.WindowTracker.get_default().get_window_app(win)?.get_id()`, `win.get_wm_class()`; running
apps: `Shell.AppSystem.get_default().get_running()`. Purpose: primary `getIBusManager()` signals
`set-content-type`/`focus-in`/`focus-out` (`misc/ibusManager.js:202-215,261`); a `focus-out`
sets purpose `none`; a `set-content-type` is accepted only after a `focus-in` (pairing rule);
fallback `Main.inputMethod` `notify::content-purpose` (`ui/keyboard.js:1982-1986`) when IBus is
absent. Map enums by comparing `IBus.InputPurpose.*` / `Clutter.InputContentPurpose.*` constants
(no hard-coded numbers). In `disable()`, **before** unexporting, emit
`Disabling(Main.sessionMode.isLocked ? 'locked' : 'disabled')`, then disconnect everything,
`unown_name`, `unexport`. All callbacks guard on `this._dbusImpl`. No titles, text or pids.

D-Bus interface (session bus; well-known name owned by gnome-shell's connection):

```xml
<!-- name org.gnome.Shell.Extensions.GswitchFocus, path /org/gnome/Shell/Extensions/GswitchFocus -->
<node>
  <interface name="org.gnome.Shell.Extensions.GswitchFocus">
    <property name="Version" type="u" access="read"/>          <!-- 1 -->
    <property name="FocusedApp" type="s" access="read"/>       <!-- Shell.App id, e.g. "org.gnome.Ptyxis.desktop"; "window:N" for windows without a .desktop file (not stable across sessions); "" -->
    <property name="FocusedWmClass" type="s" access="read"/>
    <property name="InputPurpose" type="s" access="read"/>     <!-- normal|alpha|digits|number|phone|url|email|name|password|pin|terminal|date|time|datetime|none|unknown -->
    <method name="GetFocus">
      <arg type="s" name="app_id" direction="out"/>
      <arg type="s" name="wm_class" direction="out"/>
      <arg type="s" name="purpose" direction="out"/>
    </method>
    <method name="ListRunningApps">
      <arg type="a(sss)" name="apps" direction="out"/>         <!-- (id, name, wm_class of first window) -->
    </method>
    <signal name="FocusChanged"><arg type="s" name="app_id"/><arg type="s" name="wm_class"/></signal>
    <signal name="InputPurposeChanged"><arg type="s" name="purpose"/></signal>
    <signal name="Disabling"><arg type="s" name="reason"/></signal> <!-- "locked" | "disabled" -->
  </interface>
</node>
```
`FocusChanged` is emitted on every focus-window change (also within one app).
`PropertiesChanged` for the three string properties.

Relogin (C9): GNOME 50 scans extension dirs only at startup (`_loadExtensions`,
`extensionSystem.js:686-746`); `EnableExtension` returns false for unknown UUIDs (`:273-275`);
`ReloadExtension` is NOT_SUPPORTED (`shellDBus.js:485-489`); GJS caches ES modules, so code
upgrades also need relogin. A user copy in `~/.local/share/gnome-shell/extensions/<uuid>`
shadows the system copy (`extensionSystem.js:717-721`).

`scripts/install-extension.sh` (dev helper): `gnome-extensions pack` + `install --force`, or
`--link` (symlink), `--uninstall`; pre-enables (`gnome-extensions enable` || append UUID to
`org.gnome.shell enabled-extensions` via gsettings); prints the relogin notice and `gdbus`
verification commands.

#### 3.5.2 Daemon client `internal/gswitch/focus`

Constants and types live in `internal/gswitch/focus/state.go` (grepped by `scripts/check-extension.sh`);
client in `client.go`, policy in `policy.go`, metadata test in `metadata_test.go`.

```go
const (
	ExtensionUUID      = "gswitch-focus@vabuter.github.io"
	BusName            = "org.gnome.Shell.Extensions.GswitchFocus"
	ObjectPath         = dbus.ObjectPath("/org/gnome/Shell/Extensions/GswitchFocus")
	Interface          = "org.gnome.Shell.Extensions.GswitchFocus"
	ScreenSaverName    = "org.gnome.ScreenSaver"
	ScreenSaverPath    = dbus.ObjectPath("/org/gnome/ScreenSaver")
	DefaultCallTimeout = 500 * time.Millisecond
)
type Purpose string
// Every constant is typed explicitly (in a const block only the first would otherwise be Purpose).
const (
	PurposeUnknown  Purpose = "unknown"
	PurposeNone     Purpose = "none"
	PurposeNormal   Purpose = "normal"
	PurposePassword Purpose = "password"
	PurposePIN      Purpose = "pin"
	PurposeTerminal Purpose = "terminal"
)
type State struct {
	Available     bool   // extension name currently owned
	EverAvailable bool   // seen at least once since daemon start
	AppID, WmClass string // raw, as reported by the extension (IPC/debug display only)
	AppKey, WmClassKey string // fold.AppKey(AppID), fold.AppKey(WmClass); set by the client on every change
	Purpose       Purpose
	FocusUnknown  bool   // available, but GetFocus failed; cleared by a GetFocus retry or FocusChanged
	Locked        bool
	LockReason    string // "extension-locked" | "extension-vanished" | "screensaver" | ""
	Seq           uint64 // bumps on every change
	UpdatedAt     time.Time
}
type App struct{ ID, Name, WmClass string }
type Source interface{ Snapshot() State }
func Static(st State) Source
var ErrUnavailable = errors.New("focus extension unavailable")
// (No NormalizeAppKey here: the only app normaliser is fold.AppKey, §3.1.5.)

type Options struct {
	Address     string // "" ⇒ dbus.ConnectSessionBus()
	CallTimeout time.Duration
	OnChange    func(State) // client goroutine; must not block
	Logf        func(string, ...any)
}
func New(opts Options) *Client
func (c *Client) Start(ctx context.Context)   // non-blocking; connect/retry 1s..30s
func (c *Client) Snapshot() State             // atomic, lock-free
func (c *Client) Close()                      // idempotent, bounded 1s
func ListRunningApps(ctx context.Context, conn *dbus.Conn) ([]App, error) // used by the tray; ErrUnavailable when unowned

// AppMatcher: implementations MUST normalise their own arguments with fold.AppKey (callers may
// pass raw or normalised strings) and MUST be nil-receiver safe (a nil *userconfig.ListSet stored
// in the interface is non-nil and must return false). userconfig.ListSet implements it.
type AppMatcher interface{ MatchApp(appID, wmClass string) bool }
type Policy struct {
	Excluded         AppMatcher // nil ⇒ none
	SkipPassword     bool       // auto-skip-password (true)
	SkipTerminal     bool       // auto-skip-terminal (true)
	SkipWhenLocked   bool       // auto-skip-locked (true)
	RequireExtension bool       // auto-require-focus (false)
}
func (p Policy) Allow(st State) (ok bool, reason string)
```
Client details: matches are added **before** the initial `NameHasOwner`/`GetFocus` and
`ScreenSaver.GetActive` calls. Lock state (C7): `Disabling("locked")` ⇒ `Locked=true,
LockReason="extension-locked"`; name vanishes without a preceding `Disabling` while
`EverAvailable` ⇒ `Locked=true, LockReason="extension-vanished"` (fail closed);
`Disabling("disabled")` ⇒ `Available=false`, not locked; name reappears ⇒ `Locked=false`
(unless the ScreenSaver still says active). ScreenSaver `ActiveChanged(true)` ⇒ `Locked=true,
"screensaver"` (late ~0.85 s, best effort); `ActiveChanged(false)` clears only a screensaver
lock. Every call uses `CallWithContext` with `CallTimeout`. When `GetFocus` fails as the name
appears (e.g. the Shell is busy with the unlock and the focused window did not change, so no
`FocusChanged` follows), the state is published with `FocusUnknown=true` and the client retries
`GetFocus` on its goroutine with a 250 ms..5 s backoff until it succeeds, a `FocusChanged` arrives
or the name vanishes.

`Policy.Allow` order: Locked && SkipWhenLocked ⇒ deny "locked"; !Available ⇒ RequireExtension ?
deny "focus-unavailable" : allow; FocusUnknown ⇒ deny "focus-unknown" (fail closed while the
extension is up but the focus could not be read); purpose password|pin && SkipPassword ⇒ deny "password-field";
purpose terminal && SkipTerminal ⇒ deny "terminal"; `Excluded != nil &&
Excluded.MatchApp(st.AppKey, st.WmClassKey)` ⇒ deny "excluded-app". Allocation-free (keys are
pre-normalised by the client, and `fold.AppKey` on normalised input does not allocate).
Required tests: a `*userconfig.ListSet` containing `org.gnome.ptyxis` denies a raw
`State{AppID: "org.gnome.Ptyxis.desktop"}` after the client's normalisation **and** when
`MatchApp` is called directly with the raw id; `Policy{Excluded: (*userconfig.ListSet)(nil)}`
allows without panicking (these live in WP11's `runtime/fork_test.go`, the first package that
imports both `focus` and `userconfig`; WP4 and WP5 test their halves separately).

Tray helper `internal/tray/focus_extension.go` (WP5):

```go
type FocusExtensionState int // FocusExtNoShell, FocusExtNotInstalled, FocusExtNeedsRelogin, FocusExtDisabled, FocusExtActive, FocusExtError, FocusExtOutOfDate
type FocusExtensionInfo struct{ State FocusExtensionState; Path string; Errors []string }
// Every function takes a PRIVATE session connection (dbus.ConnectSessionBus(), owned by the
// tray's App.forkConn, closed in Quit). Never dbus.SessionBus(): fyne systray shares that one
// (systray_unix.go:192) and its stayRegistered loop breaks on ExtensionStateChanged (2 args), C27.
func QueryFocusExtension(ctx context.Context, conn *dbus.Conn) (FocusExtensionInfo, error)
func EnableFocusExtension(ctx context.Context, conn *dbus.Conn) (needsRelogin bool, err error)
func DisableFocusExtension(ctx context.Context, conn *dbus.Conn) error
func WatchFocusExtension(ctx context.Context, conn *dbus.Conn, cb func(FocusExtensionInfo)) // ExtensionStateChanged arg0=uuid; RemoveMatchSignal + RemoveSignal on ctx done
func ListRunningAppsForPicker(ctx context.Context, conn *dbus.Conn) ([]focus.App, error) // ids "window:*" are returned with ID="" so the picker stores the WM_CLASS
func stateFromInfo(info map[string]dbus.Variant, dirExists bool) FocusExtensionInfo
func appendToGsettingsList(cur, uuid string) (string, bool)
```
Decision table: `org.gnome.Shell` unowned ⇒ NoShell; `org.gnome.Shell.Extensions.GetExtensionInfo(uuid)`
returns `{}` ⇒ check `/usr/share/gnome-shell/extensions/<uuid>/metadata.json` and
`${XDG_DATA_HOME:-~/.local/share}/gnome-shell/extensions/<uuid>/metadata.json` ⇒ NeedsRelogin
or NotInstalled; else `state` is a **double**: 1 Active, 2|6 Disabled, 3 Error (+`GetExtensionErrors`),
4 OutOfDate. Enable: `EnableExtension(uuid)` true ⇒ done; false ⇒ append to
`org.gnome.shell enabled-extensions` (gsettings) and return `needsRelogin=true`.

### 3.6 User configuration (WP4): `internal/gswitch/userconfig`

Shared by daemon and tray. Never touches `/etc/gswitch/default.conf` and adds no field to
upstream `config.Config` (avoids the second-parser / `trayConfigWriteArgs` drop trap,
03-judgements:393-460).

#### 3.6.1 Location and writers

Directory resolution (both processes): absolute `$XDG_CONFIG_HOME/gswitch` >
`os.UserConfigDir()/gswitch` (= `/home/vab/.config/gswitch`). Root mode: `SessionEnv.XDGConfigHome`
or `SessionEnv.Home/.config`, read-only. Dir 0700 (`MkdirAll` + `Chmod`, C23), files 0600, lock
file `<dir>/.lock` (flock `LOCK_EX`), writes are temp file in dir + `Sync` + `Rename`. Upstream's
`ui.conf` and `wizard-shown` coexist untouched. All write helpers are called **only by the tray**
(and tests); the daemon calls `Load`/`Watch` only — under the upstream unit any daemon write
would fail with EROFS (D2, C6).

| Writer | Writes |
|---|---|
| tray (settings Apply) | `auto.conf` keys that changed vs its loaded snapshot (key-preserving `Update`, never `auto-enabled`); list edits as **diffs** via `ApplyListDiff` (never a whole-file rewrite, so words learned meanwhile survive); seeds missing files from templates (`EnsureFiles`) |
| tray (on the daemon's behalf, §3.11.1) | `auto-enabled` via `SetAutoEnabled` when the daemon reports `AutoEnabledUnsaved=true`; learned words via `AppendListEntry` for each `PendingLearnedWords` entry |
| tray (offline toggle) | `auto-enabled` via `SetAutoEnabled` when no daemon is connected (the daemon reads it at start) |
| daemon | nothing |

Change propagation: tray calls D-Bus `Reload()` after a settings Apply (primary); the daemon also
watches the directory (inotify `IN_CLOSE_WRITE|IN_MOVED_TO|IN_CREATE|IN_DELETE|IN_DELETE_SELF|IN_MOVE_SELF|IN_ATTRIB`,
200 ms debounce, 2 s polling fallback, 10 s stamp re-check; when the directory is missing or was
removed, it polls every 2 s for it to (re)appear and re-adds the watch) and applies only when
`!Config.Equal`. No bind mount is involved, so a removed and recreated directory is picked up
without a daemon restart.

#### 3.6.2 `auto.conf` keys (upstream line grammar: `key=value`, `#` comments, `[section]` lines ignored, quotes stripped, last assignment wins, 64 KiB cap)

| Key | Type / range | Default | Consumer | Hot reload |
|---|---|---|---|---|
| `auto-enabled` | bool | `false` | engine enable flag (R2); written only by the tray (also on the daemon's behalf), applied by the daemon per the echo rule of §3.10.5 | yes |
| `auto-min-letters` | int 2..8 | `3` | `lang.Options.MinLetters` | yes |
| `auto-max-letters` | int 4..64 | `24` | `lastWordRange` | yes |
| `auto-short-words` | bool | `true` | `lang.Options.ShortWords` (2-key closed lists) | yes |
| `auto-margin` | float 0..5 | `1.0` | `lang.Options.MinMargin` | yes |
| `auto-max-alt-cost` | float 3..25.5 | `7.0` | `lang.Options.MaxAltCost` | yes |
| `auto-holdoff-ms` | int 30..1000 | `120` | quiet time after Space K_DOWN | yes |
| `auto-min-interval-ms` | int 0..10000 | `1000` | min time between fixes | yes |
| `auto-delay-ms` | int 0..200 (0 = upstream `delay`) | `3` | per-event replay delay | yes |
| `auto-switch-delay-ms` | int 0..2000 (0 = upstream `layout-switch-delay`) | `60` | pause after confirmed switch | yes |
| `auto-learn-on-undo` | bool | `true` | add undone word to exceptions | yes |
| `auto-learn-undo-count` | int 1..10 | `1` | undos of the same word before learning | yes |
| `auto-require-focus` | bool | `false` | fail closed when extension absent | yes |
| `auto-skip-password` | bool | `true` | purpose password/pin | yes |
| `auto-skip-terminal` | bool | `true` | purpose terminal | yes |
| `auto-skip-locked` | bool | `true` | lock state | yes |
| `hotkey-toggle-auto` | chord or `none` | `42+54` (LShift+RShift) | ChordDetector | yes |
| `hotkey-convert-selection` | chord or `none` | `42+97+100` (LShift+RCtrl+RAlt) | ChordDetector | yes |
| `ipc-expose-words` | bool | `true` | words in `Corrected`/`LastCorrection`/`CorrectionUndone`/`PendingLearnedWords` (applied by WP11 before calling the IPC service; `false` ⇒ learned words stay in daemon memory only; words corrected or learned while `false` are blanked/kept memory-only at capture time, so they stay hidden after a switch back to `true`) | yes |
| `focus-source` | `auto`\|`off` | `auto` | focus client | restart |
| `layout-source` | `auto`\|`ibus`\|`none` | `auto` | tracker sources | restart |
| `layout-hint-timeout-ms` | int 100..2000 | `400` | tracker Pending window | restart |

Rules: unknown keys and upstream keys (`layout-switch`, `convert-key`, `delay`, …) ⇒ warning,
ignored. Bad value ⇒ default + warning. Unreadable file (EACCES) ⇒ defaults with
`auto-enabled=false` + error. Loading never fails the daemon.

Template `templates/auto.conf` (go:embed) lists every key commented with its default.

#### 3.6.3 Exception lists

`exceptions-words.txt`, `exceptions-apps.txt`: UTF-8, LF (CRLF tolerated, BOM stripped), one
entry per line, `#` starts a comment (whole line or trailing), blank lines ignored, deduplicated,
1 MiB / 10,000 entries cap. Words: `fold.Word` (lower-case, `ё→е`, edge punctuation stripped, so
a hand-entered `ghbdtn,` matches), empty after folding or inner whitespace ⇒ rejected with
warning, ≤64 runes; matched against `fold.Word` of the word **as shown on screen before
correction** (punto-rs semantics). Apps: `fold.AppKey` (trim, lower-case, `.desktop` stripped);
entries starting with `window:` are rejected with a warning (Shell ids of windows without a
.desktop file are not stable across sessions; the picker stores the WM_CLASS instead);
`ListSet.MatchApp(appID, wmClass)` itself applies `fold.AppKey` to both arguments (idempotent),
so raw extension ids such as `org.gnome.Ptyxis.desktop` match the seed `org.gnome.ptyxis`;
entries containing `*` or `?` use `path.Match`.
`templates/exceptions-apps.txt` seeds: `org.gnome.terminal`, `org.gnome.console`,
`org.gnome.ptyxis`, `com.mitchellh.ghostty`, `kitty`, `alacritty`, `org.wezfurlong.wezterm`,
`org.keepassxc.keepassxc` (with a comment that exclusions need the Shell extension).

#### 3.6.4 Go API

```go
const (
	DirName = "gswitch"; AutoConfName = "auto.conf"
	WordsName = "exceptions-words.txt"; AppsName = "exceptions-apps.txt"
	MaxAutoConfBytes = 64 << 10; MaxListBytes = 1 << 20; MaxListEntries = 10000
)
func Dir() (string, error)
func DirFor(home, xdgConfigHome string) (string, error) // relative xdgConfigHome ignored

type Settings struct {
	AutoEnabled, ShortWords, LearnOnUndo                bool
	RequireFocus, SkipPassword, SkipTerminal, SkipLocked bool
	ExposeWords                                          bool
	MinLetters, MaxLetters                               int
	Margin, MaxAltCost                                   float64
	HoldoffMs, MinIntervalMs, DelayMs, SwitchDelayMs     int
	LearnUndoCount, LayoutHintTimeoutMs                  int
	FocusSource, LayoutSource                            string
	ToggleChord, SelectionChord                          Chord // nil = disabled ("none")
}
func DefaultSettings() Settings
func (s Settings) Validate() error
func (s Settings) Equal(o Settings) bool
func (s Settings) Values() map[string]string
func DiffSettings(old, new Settings) map[string]string

type ListSet struct{ /* entries map[string]struct{}; globs []string; order []string */ }
// All ListSet methods are nil-receiver safe (nil behaves as an empty set).
func (l *ListSet) Contains(word string) bool              // fold.Word(word) lookup
func (l *ListSet) MatchApp(appID, wmClass string) bool    // fold.AppKey on both args, then exact/glob; implements focus.AppMatcher
func (l *ListSet) Entries() []string
func (l *ListSet) Len() int
func (l *ListSet) Equal(o *ListSet) bool
func NormalizeWord(s string) (string, bool) // fold.Word + validation (non-empty, no inner whitespace, ≤64 runes)
func NormalizeApp(s string) (string, bool)  // fold.AppKey + validation (non-empty, no "window:" prefix)

type Config struct { Dir string; Settings Settings; Words, Apps *ListSet; Warnings []string /* + stamps */ }
// Load never returns a nil *Config, and Words/Apps are always non-nil (empty sets on any error).
func Load(dir string) (*Config, error) // ENOENT ⇒ defaults, nil error; other I/O errors ⇒ err AND usable Config (AutoEnabled=false)
func (c *Config) Equal(o *Config) bool

func Save(dir string, s Settings) error
func Update(dir string, values map[string]string) error // replaces every assignment of a key, appends missing keys, keeps comments/unknown lines/order
func SetAutoEnabled(dir string, enabled bool) error
func EnsureFiles(dir string) error
func ReadListText(dir, name string) (string, error)
func WriteListText(dir, name, text string) error // templates/tests only; the GUI uses ApplyListDiff
func AppendListEntry(dir, name, entry string) (added bool, err error)
func RemoveListEntry(dir, name, entry string) error
// ApplyListDiff re-reads the file under one flock, removes `remove`, appends missing `add`
// (normalised, deduplicated), keeps comments/order of untouched lines, and writes atomically.
// Concurrent appends made after the caller loaded its snapshot are preserved.
func ApplyListDiff(dir, name string, add, remove []string) error

type Options struct{ Debounce, PollInterval time.Duration; ForcePoll bool } // 200ms, 2s
// Watch blocks until ctx is done and then returns nil (non-nil only for invalid arguments). It
// does NOT fire an initial callback: callers Load first. onChange runs on the watcher goroutine,
// only when the loaded Config differs from the previous one (!Equal). A missing directory is
// polled for (PollInterval) and watched once it appears.
func Watch(ctx context.Context, dir string, onChange func(*Config), opts Options) error
```

#### 3.6.5 Chords and conflict analysis (`userconfig/chord.go`, `userconfig/switchconflict.go`)

```go
type Chord []uint16 // sorted ascending, distinct
const ( MinChordKeys = 2; MaxChordKeys = 4 )
func ParseChord(s string) (Chord, error)   // "42+54" (order-insensitive); "none"/"off"/"" ⇒ nil, nil
func (c Chord) String() string             // "42+97+100"; nil ⇒ "none"
func (c Chord) Equal(o Chord) bool         // set equality
func (c Chord) Overlaps(o Chord) bool      // subset either way (incl. equal)
func (c Chord) ContainsAll(keys []uint16) bool
// ValidateChord: 2..4 distinct codes in 1..0x2ff; at least one config.IsModifierKey
// (convert_key.go:6-13: 29,42,54,56,97,100,125,126); every non-modifier key must be in
// ChordExtraKeys. Text keys (letters, digits, punctuation, Space, Enter, Backspace, Tab, Esc,
// arrows, keypad) are rejected: e.g. Shift+A (42+30) would toggle on every capital "A" and
// swallow the letter's K_UP from the converter.
func ValidateChord(c Chord) error
// ChordExtraKeys: F1–F10 (59–68), F11/F12 (87, 88), F13–F24 (183–194), ScrollLock (70),
// Pause (119), Menu/Compose (127).
var ChordExtraKeys map[uint16]bool
// ValidateChords: each valid; toggle and selection must not Overlap; neither may ContainsAll(layoutSwitch)
// when len(layoutSwitch)>0; neither may contain convertKey when convertKey != 0. nil chords skipped.
func ValidateChords(toggle, selection Chord, layoutSwitch []uint16, convertKey uint16) error

type ConflictLevel uint8
const ( ConflictNone ConflictLevel = iota; ConflictOrderDependent; ConflictAlways )
// SwitchConflict evaluates every press order of c against the effective GNOME accelerators
// (forward and backward lists as stored in gsettings, e.g. "<Alt>Shift_L", "<Shift>Alt_L").
// An accelerator <M1><M2>K fires when K is pressed while exactly the modifier set {M1,M2} is
// held; <Alt> is satisfied by LAlt(56) or RAlt(100) (RAlt is Alt_R ⇒ Mod1 on us/ru winkeys),
// <Shift> by 42/54, <Control> by 29/97, <Super> by 125/126; K names map Shift_L→42, Shift_R→54,
// Alt_L→56, Alt_R→100, Control_L→29, Control_R→97. Unknown syntax ⇒ ConflictNone + warning.
func SwitchConflict(c Chord, accels []string) (ConflictLevel, string /* human description of the bad order */)
```
On this host the R4 default `42+97+100` is `ConflictOrderDependent` (RAlt held, then LShift
pressed, before RCtrl ⇒ `<Alt>Shift_L`); the toggle default `42+54` is `ConflictNone`. The
press-time model (mutter fires the accelerator on the key press with exactly the modifier set
held) is **not yet verified on GNOME 50**: run M9 in both press orders, and also check whether
the switch happens on release, before WP4's `SwitchConflict` tests are frozen; the runtime
compensation (§3.10.4) waits for a late flip either way.
`ConflictAlways` is a validation error in the GUI; `ConflictOrderDependent` is a warning ("press
Shift first") and the daemon compensates (§3.10.4).

### 3.7 Chord detector (WP7): `runtime/chord.go`

```go
type ChordAction uint8
const ( ChordNone ChordAction = iota; ChordToggleAuto; ChordConvertSelection )
func (a ChordAction) String() string // "toggle-auto" | "convert-selection"

// ChordDetector is an exact-set chord matcher. The zero value has no bindings and is a
// pass-through (Feed returns false without tracking), so existing tests that build
// &Switcher{config, converter, ...} (switcher_test.go:45,174,200,231, modifier_swap_test.go:47)
// keep working unchanged. Methods are mutex-guarded (Set may be called from the config goroutine).
type ChordDetector struct{ /* mu; bindings []chordBinding; held map[uint16]struct{}; state; armed int */ }
func (d *ChordDetector) Set(bindings map[ChordAction][]uint16) // hot reload; resets state; nil/empty chord = disabled
func (d *ChordDetector) Feed(code uint16, value int32) (ChordAction, bool) // fire on the K_UP that empties held
func (d *ChordDetector) HeldCount() int
func (d *ChordDetector) Resync(down map[uint16]bool) // replace held with kernel truth; state ⇒ IDLE if any armed key missing
func (d *ChordDetector) Reset()
```
State machine: §2.3.2. Keys are scancodes only (InputEvent has no device id; same limitation as
upstream shift tracking). Debug logs name the action only, never key content
(`switcher_test.go:44-64` asserts that for upstream logs).

### 3.8 Auto engine (WP8): `runtime/auto.go`, `runtime/auto_deps.go`

The engine is pure and testable: all environment access goes through `AutoDeps` function values.
It runs only on the Run goroutine except `Stats()`.

```go
// auto_deps.go
type wordJudge interface{ Decide(shown, alt string, pair lang.Pair) lang.Decision } // *lang.Detector
type LayoutSnapshot struct{ Index int; Generation uint64; Confirmed bool }
type FocusSnapshot struct{ Seq uint64; Allowed bool; Reason string }
type AutoSettings struct {
	Enabled        bool
	MaxLetters     int
	Holdoff        time.Duration
	MinInterval    time.Duration
	LearnOnUndo    bool
	LearnUndoCount int
}
type FixEvent struct{ Shown, Alt string; From, To int; At time.Time }
type AutoStats struct{ Armed, Fired, Cancelled, Skipped, Undone, Learned, GarbleRisk uint64; LastReason string }
type AutoDeps struct {
	Layouts   [2]*LayoutInfo       // s.layoutConverter.Layout1/Layout2
	Langs     [2]lang.Lang         // lang.LangForLayout(Name) for each; both must be supported
	Judge     wordJudge
	Layout    func() LayoutSnapshot // from Tracker.State(): Confirmed && Index>=0
	Focus     func() FocusSnapshot  // nil ⇒ {Seq:0, Allowed:true}
	Caps      func() (on, known bool)
	InputIdle func() bool           // eventChan and overflow empty && !InputReader.AnyKeyDown()
	Emit      func(events []KeyEvent) error // emitConversionEvents with auto options
	Settings  func() AutoSettings   // read once per decision / per tick
	OnFix     func(FixEvent)        // IPC + stats hook; may be nil
	OnUndo    func(foldedShown string, learn bool) // learn = count reached and LearnOnUndo
	Now       func() time.Time
	Log       DebugLogger
	LogWords  bool                  // GSWITCH_AUTO_LOG_WORDS=1 under --debug only
}

// auto.go
func NewAutoEngine(d AutoDeps) (*AutoEngine, error) // error when a Layout is nil, a Lang unsupported, or Judge/Layout/Emit/Settings nil
func (e *AutoEngine) Observe(code uint16, value int32, ctrlHeld bool) // every EV_KEY, before Push
func (e *AutoEngine) OnSpace(c *Converter)                          // Space K_DOWN, ActionNone, after Push/Process
func (e *AutoEngine) NextDeadline(now time.Time) (time.Duration, bool)
func (e *AutoEngine) Tick(c *Converter)
func (e *AutoEngine) TryUndo(c *Converter) bool
func (e *AutoEngine) OnLayoutChange(st LayoutSnapshot, own bool) // own ⇒ ignore; external ⇒ Cancel("layout")
func (e *AutoEngine) Cancel(reason string)                         // cancel a pending fix AND disarm lastFix
// Reset is called by afterInputDiscarded (Flush, drainEventChan, end of SuspendHotkeys):
// Cancel("discarded"); held = the kernel key set passed in (nil ⇒ empty); WordTaint.MarkDiscarded()
// (the word in progress stays unfixable until a boundary is processed after the discard, C25);
// the word-start snapshot is invalidated (re-taken at the next text K_DOWN).
func (e *AutoEngine) Reset(keysDown map[uint16]bool)
func (e *AutoEngine) Stats() AutoStats                             // any goroutine (atomics)
```

**Observe** maintains: `held map[uint16]bool` (K_DOWN adds, K_UP removes, K_REPEAT ignored for
the set); `WordTaint.Observe`; word-start snapshot (`wordGen = Layout().Generation`, `wordSeq =
Focus().Seq`) taken on the first text K_DOWN after **any** word boundary as defined in §3.2.4 —
SPACE/ENTER/KPENTER K_DOWN, every non-Ctrl BufKiller K_DOWN (each one empties the converter
buffer) — and after `Reset` (selection/suspension `ClearBuffer`, Flush, drain). A Backspace
that erases a pending Space boundary keeps the previous word's snapshot (the word resumes).
Without the BufKiller/Reset cases the first word after a mid-word Alt+Tab or Alt+Shift would
always be skipped as `focus-changed`/`layout-changed`. `spaceUp` flag on Space K_UP. Any K_DOWN
or K_REPEAT while ARMED ⇒ `Cancel("input")`.

**OnSpace** pipeline (reason codes recorded in `LastReason`, `Skipped++`):
`disabled` → held modifier (Ctrl 29/97, Alt 56/100, Meta 125/126 in `held`) ⇒ `modifier` →
`lastWordRange(c.buffer, MaxLetters)` ⇒ its reason → `WordTaint.Tainted()` ⇒ `tainted:<reason>`
→ focus: `Focus().Seq != wordSeq` ⇒ `focus-changed`; `!Allowed` ⇒ `focus:<reason>` → layout:
`!Confirmed` ⇒ `layout-unknown`; `Generation != wordGen` ⇒ `layout-changed` →
`renderWord(c.buffer, r.Start, r.TextEnd, …)` (word without its Space; shown = `Layouts[Index]`,
alt = `Layouts[1-Index]`, Caps from `Caps()`) ⇒ `unrenderable` →
`lexicalVeto(Shown)` → `shiftShapeVeto` ⇒ `shift-shape` → `Judge.Decide(Shown, Alt,
lang.Pair{Langs[Index], Langs[1-Index]})`; not `Fix` ⇒ `detector:<reason>`. On Fix: ARMED with
`range`, copy of `c.buffer[Start:End]`, `deadline = Now()+Holdoff`, `spaceUp=false`, decided
`Index`/`Generation`/`wordSeq`, `armedAt`; `Armed++`.

**Tick** (deadline due): Space not yet released ⇒ re-arm +30 ms until `armedAt+1s`, then
`Cancel("space-held")`. In practice the kernel's Space K_REPEAT (atkbd delay ~250 ms) cancels
first via Observe (`input`), so the re-arm path only covers holds between the 120 ms holdoff
and the kernel repeat delay. This is intentional: the compositor's own autorepeat (mutter,
500/30 ms on this host) would add spaces the buffer never sees, and a long Space hold is not
worth a special case. Gates in order: `Settings().Enabled`; `Now()-lastFireAt >= MinInterval`
(`rate-limit`); `held` empty and `InputIdle()` (`input-pending`); buffer check: `len(c.buffer) >=
End`, `c.buffer[Start:End]` equals the copy, and every event after `End` is a shift K_UP
(`buffer-changed`); `Layout()` still Confirmed with the decided Index and Generation
(`layout-changed`); `Focus()` Seq unchanged and Allowed (`focus-changed`). Then `events :=
c.ConvertRange(Start, End)`; `err := Emit(events)`; on error: `Skipped++`, reason `emit:<err>`,
no lastFix, buffer untouched. On success: `c.invalidateUndo()`; `lastFix = {Start, End, copy,
gen: c.UndoGeneration(), seq: Focus().Seq, shownFolded: fold.Word(Shown), from: Index}`;
`lastFireAt`; `Fired++`;
count events that arrived during the emission (`len(eventChan)` via `InputIdle()==false`) into
`GarbleRisk`; `OnFix`. The layout stays switched.

**TryUndo** succeeds iff `lastFix` exists, `c.UndoGeneration() == lastFix.gen`, `Focus().Seq ==
lastFix.seq` (an undo in another window would erase foreign text), the buffer still holds the
copy at `[Start,End)` and every event after `End` is a shift event (the double-Shift gesture's
own Shift events are pushed before `Process`; the custom convert key never reaches the buffer —
`processCustomConversionKey` returns before `Push`, `switcher.go:666-668,724-756`, and
`finishSelectionModifierEvent` runs `trimBuffer` before `performConversion`, `:806-813` — so a
convert-key event after `End` never occurs in practice; the check tolerates it anyway). It emits
`c.ConvertRange(Start, End)` through `Emit`, clears `lastFix`, calls `c.invalidateUndo()`,
increments the per-word undo counter (map capped at 64 entries) and calls
`OnUndo(shownFolded, LearnOnUndo && count >= LearnUndoCount)`. A second gesture falls through
to upstream (re-applies the conversion). The caller (`performConversion`) invokes `TryUndo`
only for the raw `ActionConvertWord` before the ReverseMode swap and calls `Cancel("manual")`
for every other manual conversion (§4).

**Logging**: `logDebug("auto: arm|cancel|skip|fire|undo|layout reason=%s keys=%d cost=%.2f/%.2f known=%v/%v caps=%v wait=%s")`;
words only when `LogWords`. Syslog gets counters and reasons only (upstream never logs text,
`textDebugSummary`, `switcher.go:947`).

`runtime/autocheck.go` (WP8): `func RunAutoCheck(args []string, stdout, stderr io.Writer) int` for
`gswitch --auto-check <text> [--shown us|ru]`: loads layouts (config `layout1`/`layout2` else
`GetCurrentLayouts(nil)`), `LoadLayout` both, reverse-maps text to `(code, shift)` events via
the shown layout's KeyMap, appends Space, runs `lastWordRange`/`renderWord`/`Decide` with the
user config's detector options and exceptions, prints JSON `{shown, alt, active, verdict, reason,
shown_cost, alt_cost, shown_known, alt_known}`; exit 2 when unrenderable. No root, no devices.

### 3.9 IPC (WP6): `internal/gswitch/ipc`

Pure Go (godbus v5.2.2 already in `go.mod`), no GTK, no `runtime` import.

#### 3.9.1 Daemon interface

Bus name `io.github.vabuter.GswitchAuto`, object `/io/github/vabuter/GswitchAuto`, interface
`io.github.vabuter.GswitchAuto`. Not bus-activatable. The Service uses its own private session
connection (`dbus.ConnectSessionBus()`, C27) and closes only that one. `RequestName(…, NameFlagDoNotQueue)`;
not primary owner (second instance, e.g. `gswitch --debug` beside the service) ⇒ log
"IPC disabled: bus name owned by another instance" and continue. Root mode ⇒ IPC disabled.

```xml
<node>
  <interface name="io.github.vabuter.GswitchAuto">
    <property name="AutoEnabled" type="b" access="readwrite"/>   <!-- Set ⇒ enqueued; requestLoop calls Controller.SetAutoEnabled(v,"dbus") outside the prop lock -->
    <property name="AutoEnabledUnsaved" type="b" access="read"/> <!-- runtime value differs from auto.conf after a chord/dbus toggle; the tray persists it -->
    <property name="PendingLearnedWords" type="as" access="read"/> <!-- learned (folded) words not yet seen in exceptions-words.txt; [] when ipc-expose-words=false -->
    <property name="AutoAvailable" type="b" access="read"/>
    <property name="AutoUnavailableReason" type="s" access="read"/> <!-- code, see below -->
    <property name="ActiveLayout" type="s" access="read"/>       <!-- "us" | "ru" | "" -->
    <property name="LayoutKnown" type="b" access="read"/>
    <property name="LayoutSource" type="s" access="read"/>       <!-- "ibus" | "gsettings" | "" -->
    <property name="FocusSource" type="s" access="read"/>        <!-- "active" | "absent" | "off" -->
    <property name="FocusedApp" type="s" access="read"/>
    <property name="FocusedWmClass" type="s" access="read"/>
    <property name="InputPurpose" type="s" access="read"/>
    <property name="FocusSuppressed" type="b" access="read"/>
    <property name="FocusSuppressedReason" type="s" access="read"/>
    <property name="LastCorrection" type="(ssst)" access="read"/> <!-- from, to, "us>ru"|"ru>us", unix s; words "" when ipc-expose-words=false -->
    <property name="CorrectionCount" type="u" access="read"/>
    <property name="Stats" type="a{st}" access="read"/>          <!-- armed, fired, cancelled, skipped, undone, learned, garble_risk -->
    <property name="ToggleChord" type="s" access="read"/>        <!-- effective, e.g. "42+54" or "none" -->
    <property name="SelectionChord" type="s" access="read"/>
    <property name="HotkeysSuspended" type="b" access="read"/>
    <property name="ConfigDir" type="s" access="read"/>
    <property name="ConfigWarnings" type="as" access="read"/>
    <property name="Version" type="s" access="read"/>
    <method name="SetAutoEnabled"><arg name="enabled" type="b" direction="in"/></method>
    <method name="Reload"/>                                       <!-- error io.github.vabuter.GswitchAuto.Error.Config -->
    <method name="SuspendHotkeys"><arg name="suspend" type="b" direction="in"/></method>
    <signal name="AutoEnabledChanged"><arg name="enabled" type="b"/><arg name="source" type="s"/></signal> <!-- chord|dbus|config|startup -->
    <signal name="Corrected"><arg name="from" type="s"/><arg name="to" type="s"/><arg name="direction" type="s"/><arg name="time" type="t"/></signal>
    <signal name="CorrectionUndone"><arg name="word" type="s"/><arg name="learned" type="b"/></signal>
  </interface>
</node>
```
`PropertiesChanged` is emitted automatically (`prop.EmitTrue`). `AutoUnavailableReason` codes:
`""`, `degraded` (no layout-switch key), `no-layout-converter`, `unsupported-layouts`,
`layout-source-off`, `layout-unknown`, `root-mode`, `detector-error`. Producer (WP11,
`statusSnapshot()` used by `statusLoop` and by the tracker/focus `OnChange` updates):
`s.auto == nil` ⇒ `AutoAvailable=false`, reason = `s.autoReason` (set by `EnableAuto`);
`s.auto != nil` and tracker confidence `Unknown` or `Settings` ⇒ `AutoAvailable=false`,
reason `layout-unknown`; `s.auto != nil` and confidence `Confirmed` or `Pending` (Pending lasts
≤ `layout-hint-timeout-ms` and must not make the tray flicker) ⇒ `AutoAvailable=true`, reason
`""`. `Stats` and `CorrectionCount` are refreshed at most once per second (no per-keystroke
traffic). Words in `Corrected`, `LastCorrection`, `CorrectionUndone` and `PendingLearnedWords`
are blanked/emptied by WP11 **before** it calls `NotifyCorrected`/`NotifyUndone`/`UpdateStatus`
when `ipc-expose-words=false`; the ipc package never sees user settings. `SuspendHotkeys(true)` records
the caller's unique name; suspension ends on `SuspendHotkeys(false)` from that caller, when the
caller's name vanishes (`NameOwnerChanged`), or after 120 s.

#### 3.9.2 Go API

```go
const ( BusName = "io.github.vabuter.GswitchAuto"; ObjectPath dbus.ObjectPath = "/io/github/vabuter/GswitchAuto"; Interface = BusName
        ErrConfig = BusName + ".Error.Config"; ErrNotAvailable = BusName + ".Error.NotAvailable" )
type Correction struct{ From, To, Direction string; Time time.Time }
type Status struct {
	AutoEnabled, AutoAvailable, LayoutKnown, FocusSuppressed, HotkeysSuspended bool
	AutoEnabledUnsaved  bool
	PendingLearnedWords []string
	UnavailableReason, ActiveLayout, LayoutSource, FocusSource string
	FocusedApp, FocusedWmClass, InputPurpose, FocusSuppressedReason string
	ToggleChord, SelectionChord, ConfigDir, Version string
	ConfigWarnings []string
	Last  Correction
	Count uint32
	Stats map[string]uint64
}
// Controller implementations may call any Service method synchronously from any goroutine,
// including from inside SetAutoEnabled: no Service method blocks or takes the prop lock in the
// caller's goroutine.
type Controller interface {
	AutoEnabled() bool
	SetAutoEnabled(enabled bool, source string) error
	Reload() error
	SuspendHotkeys(suspend bool, owner string) error
	Snapshot() Status
}
func Start(ctx context.Context, ctl Controller, logf func(string, ...any)) (*Service, error)
func (s *Service) UpdateStatus(st Status)                       // non-blocking: latest-wins slot + wake; propLoop diffs ⇒ SetMust per changed property
func (s *Service) NotifyAutoEnabled(enabled bool, source string) // non-blocking: queued (cap 64, drop + debug log when full)
func (s *Service) NotifyCorrected(c Correction)                 // non-blocking, as above
func (s *Service) NotifyUndone(word string, learned bool)       // non-blocking, as above
func (s *Service) Close()                                       // stops both loops, closes the private connection

type ClientStatus struct{ Connected bool; Status }
type ClientCallbacks struct {
	OnStatus  func(ClientStatus)
	OnToggle  func(enabled bool, source string)
	OnCorrect func(Correction)
	OnUndone  func(word string, learned bool)
}
func NewClient(cb ClientCallbacks) *Client
func (c *Client) Start() error
func (c *Client) Stop()
func (c *Client) Status() ClientStatus
func (c *Client) SetAutoEnabled(ctx context.Context, enabled bool) error
func (c *Client) Reload(ctx context.Context) error
func (c *Client) SuspendHotkeys(ctx context.Context, suspend bool) error
```
Service: method handler via `conn.Export`, properties via `prop.Export`,
`introspect.NewIntrospectable`. Handlers never touch the key path directly. **Locking contract**
(godbus v5.2.2 `prop.Properties.Set` holds `p.mut` while it runs `prop.Callback`,
`prop/prop.go:310-333`, and `SetMust` locks the same non-reentrant mutex, `:341-343`):

- The writable `AutoEnabled` callback only type-checks and does a **non-blocking** send of the
  requested value to `requests` (cap 8; full ⇒ return `dbus.MakeFailedError(errBusy)`), then
  returns nil, so godbus stores the value and emits `PropertiesChanged` itself. It never calls
  the Controller or any Service method.
- `requestLoop` (one goroutine) receives from `requests` and calls
  `ctl.SetAutoEnabled(v, "dbus")` with no lock held; a refused or failed request is corrected by
  the next `UpdateStatus` (the property reverts to the true value).
- `propLoop` (one goroutine) is the only caller of `SetMust` and `conn.Emit`: it applies the
  latest `UpdateStatus` snapshot as a diff against the **exported** values (`GetMust`, read
  lock; not a private cache, so a value stored by a property Set that the controller then
  refused is reverted) and emits queued signals in FIFO order (`AutoEnabledChanged` after the
  matching property update).
- Method handlers (`SetAutoEnabled`, `Reload`, `SuspendHotkeys`) run on godbus goroutines without
  the prop lock and may call the Controller synchronously.

Client: its **own private connection** (`dbus.ConnectSessionBus()`; never `dbus.SessionBus()`,
which fyne.io/systray v1.12.2 shares in the tray, `systray_unix.go:192`, and whose
`stayRegistered` loop returns on any signal with `len(Body) < 3` and calls `register()` on a
string `Body[2]`, `:291-306` — `AutoEnabledChanged`, `CorrectionUndone` and `Corrected` would
break or spam it, C27); matches for `NameOwnerChanged(arg0=BusName)`, `PropertiesChanged`
(sender=BusName, path), the three signals; `GetAll` on (re)connect; all calls `CallWithContext`
2 s; owner vanishes ⇒ `Connected=false`; `Stop` removes its matches and closes only its own
connection; reconnect opens a new private connection.

### 3.10 Runtime glue (WP11): `runtime/fork.go`, `runtime/auto_control.go`, `runtime/userconfig_apply.go`, `runtime/layout_glue.go`, `runtime/focus_glue.go`

#### 3.10.1 New Switcher fields (added to the struct at `switcher.go:36-90`)

```go
	// gswitch-auto (fork)
	layoutSpecs      []LayoutSpec                 // resolved pair (set in initLayoutConverter)
	layoutReaderFn   func() layoutStateReader     // WP0
	onOwnSwitch      func()                       // WP0
	controlChan      chan func()                  // cap 32
	forkWG           sync.WaitGroup
	userDir          string
	userCfg          atomic.Pointer[userconfig.Config]  // read-only view; the daemon never writes (D2)
	autoMu           sync.Mutex                   // guards the auto-enabled transition rule (§3.10.5); never held while calling IPC
	fileAutoEnabled  bool                         // auto-enabled as last loaded from auto.conf (autoMu)
	autoUnsaved      atomic.Bool                  // runtime value changed by chord/dbus, not yet seen on disk
	lastAnnounced    [2]time.Time                 // [false],[true]: last chord/dbus SetAutoEnabled of that value (autoMu)
	pendingMu        sync.Mutex
	pendingLearned   []string                     // learned folded words not yet in exceptions-words.txt (cap 64, oldest dropped)
	tracker          *layoutstate.Tracker         // nil: layout-source=none, non-GNOME session, root mode, IBus unsupported
	lsKeysDown       map[uint16]bool
	focusClient      *focus.Client                // nil when focus-source=off or root
	focusPolicy      atomic.Pointer[focus.Policy]
	detector         *lang.Detector
	auto             *AutoEngine                  // nil ⇒ auto unavailable (reason in autoReason)
	autoReason       string
	autoEnabled      atomic.Bool
	chords           ChordDetector                // value type, zero = pass-through
	chordStartLayout layoutstate.State            // zero when tracker == nil
	chordSawAltShift bool                         // during this chord cycle a Shift went down while an Alt was held (or LAlt while a Shift was)
	forkAltDown      uint8                        // bit0 = LAlt(56), bit1 = RAlt(100) physically down (Run goroutine only)
	hotkeysSuspended atomic.Bool
	expectOwnGen     uint64                       // tracker generations <= this are our own switches (Run goroutine only)
	ipcSvc           *ipc.Service                 // nil when IPC disabled
	lastCorrection   atomic.Pointer[ipc.Correction]
```

#### 3.10.2 Lifecycle

```go
func (s *Switcher) initFork()   // end of NewSwitcher (before `cleanupLogger = false`, :221)
func (s *Switcher) startFork()  // Run(), after `s.eventChan = make(...)` (:447)
func (s *Switcher) stopFork()   // Run() after the main loop, and Close(); idempotent
```
`initFork`: `controlChan`; `loadUserConfig()` (dir via `userconfig.DirFor(env.Home,
env.XDGConfigHome)` when `sessionEnv != nil`, else `userconfig.Dir()`; read-only; warnings
logged; never fails); `s.chords.Set(...)` from settings after `applyChordRuntimeChecks` (a chord
that contains all `LSKeys` or the `ConvertKey` is disabled with a warning — never a startup
failure); detector `lang.NewDetector(opts)` + `SetUserExceptions(cfg.Words.Entries())`; tracker
(if `layout-source` permits, GNOME session, euid != 0) = `layoutstate.New(...)` with `Layouts`
from `s.layoutSpecs`, `AddSource(layoutstate.NewIBusSource(layoutstate.AddressEnvFromProcess(),
s.logDebug))`, `AddResync(layoutstate.NewGSettingsResync(s.sessionEnv))`. **Only when
`s.tracker != nil`**: `s.layoutReaderFn = newCompositeLayoutReader(s.tracker, s.sessionEnv)` and
`s.onOwnSwitch = s.noteOwnSwitch`. With `tracker == nil` (root mode, KDE/X11 without GNOME,
`layout-source=none`, IBus unsupported) both stay nil, so `performConversion` →
`emitConversionEvents` uses `gnomeWaylandLayoutStateReader{env}` exactly as upstream even when
`verifyGNOMEWaylandLayoutSwitch` is true, and nothing on the manual path touches a tracker
(`newCompositeLayoutReader(nil, env)` and `noteOwnSwitch` are additionally nil-safe). Focus
client (if `focus-source=auto`, euid != 0); `EnableAuto()`;
`autoEnabled.Store(cfg.Settings.AutoEnabled)`; `s.fileAutoEnabled = cfg.Settings.AutoEnabled`.
Every euid check uses the package variable `forkGeteuid = os.Geteuid` (test hook for
`TestManualConversionWithoutTracker`/`TestEnableAutoReasons`).

`EnableAuto()` refuses (sets `autoReason`, logs once, leaves `s.auto=nil`) when:
`degradedMode` ⇒ `degraded`; `layoutConverter == nil` ⇒ `no-layout-converter`; a layout has no
`lang.LangForLayout` ⇒ `unsupported-layouts`; `tracker == nil` ⇒ `layout-source-off` (or
`root-mode`); detector error ⇒ `detector-error`. Otherwise builds `AutoDeps`:
`Layout` = `Tracker.State()` mapped to `LayoutSnapshot{Index, Generation, Confirmed:
Confidence==Confirmed && Index>=0}`; `Focus` = `focusClient.Snapshot()` + `focusPolicy.Allow`;
`Caps` = `inputReader.CapsLockState`; `InputIdle` = `len(s.eventChan)==0 && overflow empty &&
!inputReader.AnyKeyDown()`; `Emit` = `s.emitConversionEvents(events, emitOptions{delay:
auto-delay-ms or config.Delay, switchDelay: auto-switch-delay-ms or config.LayoutSwitchDelay,
reader: trackerLayoutStateReader{s.tracker}, confirm: true})` — the auto replay is confirmed
through the tracker even when both verify flags are false (explicit `layout-switch` keys; C26);
if the shortcut is dropped the confirmation fails, nothing is typed, no `lastFix` is recorded
and no `Corrected` is emitted; `Settings` from `userCfg.Load()` + `autoEnabled`; `OnFix` ⇒
`lastCorrection`, IPC `Corrected` (words blanked when `ipc-expose-words=false`); `OnUndo` ⇒
`learnWord` + IPC `CorrectionUndone` (word blanked likewise).

`startFork`: `tracker.Start(s.ctx)` (when non-nil); `focusClient.Start(s.ctx)` (when non-nil);
goroutines (on `forkWG`): `watchUserConfig` (`userconfig.Watch(s.ctx, s.userDir,
s.applyUserConfig, Options{})` — blocks until `s.ctx` is done, no initial callback),
`statusLoop` (1 s ticker pushing `UpdateStatus(s.statusSnapshot())` only when changed);
`ipc.Start` (failure ⇒ log, continue without IPC). There is no persist goroutine: the daemon
never writes. Tracker/focus `OnChange` callbacks do a non-blocking `enqueueControl` that calls
`s.onLayoutState(st)` / `s.onFocusState(st)` on the Run goroutine and a non-blocking
`UpdateStatus` (never blocks, §3.9.2).

#### 3.10.3 Key path

```go
// forkPreProcess runs right after trackSelectionModifiers. Returns true when the event is consumed.
func (s *Switcher) forkPreProcess(event *InputEvent) bool {
	s.observeLayoutSwitchChord(event)                    // LSKeys all down ⇒ tracker.NoteSwitchHint() once per press cycle; no-op when tracker == nil
	if s.auto != nil { s.auto.Observe(event.Code, event.Value, s.ctrlPressed.Load()) }
	if s.hotkeysSuspended.Load() { return true }         // capture dialog open: no gestures, no chords, no buffer
	if event.Value == K_DOWN && s.chords.HeldCount() == 0 {
		s.chordSawAltShift = false
		if s.tracker != nil { s.chordStartLayout = s.tracker.State() }
	}
	s.noteChordAltShift(event)                           // keeps forkAltDown bits for 56/100; sets chordSawAltShift on Shift K_DOWN while 56/100 down, or 56 K_DOWN while left/rightShiftPressed
	if action, fired := s.chords.Feed(event.Code, event.Value); fired {
		s.converter.Push(event.Code, event.Value)        // keep shift bookkeeping consistent
		s.handleChord(action)
		return true
	}
	return false
}
// forkBeforeConversion is the first statement of performConversion, BEFORE the ReverseMode swap,
// so it sees the raw gesture: s.auto == nil ⇒ false; action == ActionConvertWord &&
// s.auto.TryUndo(s.converter) ⇒ true (performConversion returns); otherwise
// s.auto.Cancel("manual") (the manual conversion invalidates lastFix) ⇒ false.
func (s *Switcher) forkBeforeConversion(action Action) bool
// statusSnapshot builds ipc.Status (any goroutine; atomics/mutex reads only): availability
// mapping of §3.9.1, words blanked when ipc-expose-words=false, AutoEnabledUnsaved,
// PendingLearnedWords (copy under pendingMu).
func (s *Switcher) statusSnapshot() ipc.Status
func (s *Switcher) loopWait() time.Duration          // min(EventLoopTimeoutMs, auto.NextDeadline)
func (s *Switcher) autoTick()                         // if s.auto != nil && !s.inConversion.Load() { s.auto.Tick(s.converter) }
func (s *Switcher) enqueueControl(fn func()) bool
// afterInputDiscarded runs after InputReader.Flush (:917), after convertSelection's
// drainEventChan (:983-991) and at the end of a hotkey suspension:
//   down, err := s.inputReader.KeysDown()   // (map, error) — never passed as a single value
//   if err == nil { s.chords.Resync(down) } else { s.chords.Reset(); down = nil }
//   if s.auto != nil { s.auto.Reset(down) } // taints the word in progress "discarded" (C25)
//   s.lsKeysDown = nil
func (s *Switcher) afterInputDiscarded()
// noteOwnSwitch: if s.tracker == nil { return }; s.expectOwnGen = s.tracker.State().Generation.
// It is called by emitConversionEvents on success (after a confirmed switch the tracker already
// holds the new generation; after a blind manual switch the IBus signal normally arrives during
// the ≥100 ms replay) and by restoreLayoutIfChordFlipped. No index/target window is kept.
func (s *Switcher) noteOwnSwitch()
func (s *Switcher) onLayoutState(st layoutstate.State) // own iff st.Generation <= s.expectOwnGen ⇒ auto.OnLayoutChange(snap, true); else auto.OnLayoutChange(snap, false). A late signal of a blind manual switch may be classified external: harmless, because the manual path already called Cancel("manual"); at worst the next word is skipped as layout-changed. NEVER ClearBuffer (C5)
func (s *Switcher) onFocusState(st focus.State)      // auto.Cancel("focus") (pending fix and lastFix); IPC status. NEVER ClearBuffer (C24)
```
Hotkey suspension: `SuspendHotkeys(true)` sets the flag and enqueues `converter.ClearBuffer();
auto.Cancel("suspended")`; `false` enqueues `ClearBuffer()` and `afterInputDiscarded()` (keys
typed into the capture dialog were consumed, so the next word restarts tainted until a boundary).

#### 3.10.4 Chords

```go
func (s *Switcher) handleChord(action ChordAction) {
	if s.auto != nil { s.auto.Cancel("chord") }
	switch action {
	case ChordToggleAuto:
		s.converter.trimBuffer()                          // switcher.go:809 precedent; double-Shift right after still works
		_ = s.SetAutoEnabled(!s.autoEnabled.Load(), "chord")
	case ChordConvertSelection:
		s.ctrlShiftSelectArmed.Store(false)
		s.pendingBufferAction = ActionNone
		s.converter.ClearBuffer()                         // explicit (C12)
		s.restoreLayoutIfChordFlipped()                   // C14
		s.deferSelectionUntilModifiersReleased(selectionConvertLayout) // runs via the deferred finishSelectionModifierEvent (:663) in this same event
	}
}
```
`restoreLayoutIfChordFlipped`: no-op when `s.tracker == nil` or `chordStartLayout` was not
Confirmed. If `s.chordSawAltShift` (the press order could fire `<Alt>Shift_L`, either on press
or on release), first wait up to 150 ms on the Run goroutine (poll `tracker.State()` every
10 ms) for `Generation > chordStartLayout.Generation` — the chord fires on the last K_UP, which
can precede the IBus signal; without the wait the flip is missed. Without `chordSawAltShift`
check once, no wait. Then, if the tracker is Confirmed and the Index differs from
`chordStartLayout.Index` and the generation is not own (`> expectOwnGen`), switch back with
`triggerAndConfirmGNOMEWaylandLayoutSwitch(trackerLayoutStateReader{s.tracker}, LSKeys, …,
upstream attempts/delays)` (confirmed, so no target window is needed), `noteOwnSwitch()`, log
"selection chord: restored layout flipped by GNOME binding"; a confirmation failure is logged
and the selection conversion still runs. The added latency (≤150 ms) applies only to chord
cycles in which Alt and Shift overlapped in the risky order. On Wayland
`WaitForSelectionModifiersReleased` is a no-op (`clipboard.go:200-203`); the chord's own full
release is the barrier. Alt is not tracked by `finishSelectionModifierEvent` (`:797-805`) — the
chord fires only on full release, so Alt is already up.

#### 3.10.5 Enable state, persistence, reload (`auto_control.go`, `userconfig_apply.go`)

```go
func (s *Switcher) AutoEnabled() bool
// SetAutoEnabled (under autoMu): atomic store; enqueue auto.Cancel("disabled") when off; for
// source "chord" or "dbus": lastAnnounced[enabled] = now, autoUnsaved = (enabled != fileAutoEnabled);
// for "config"/"startup": autoUnsaved = false. After releasing autoMu: NotifyAutoEnabled +
// UpdateStatus (both non-blocking). Never blocks on IPC, never writes a file.
func (s *Switcher) SetAutoEnabled(enabled bool, source string) error
func (s *Switcher) ReloadUserConfig() error // Load + applyUserConfig; backs D-Bus Reload
// applyUserConfig: pointer swap; detector SetOptions and SetUserExceptions(c.Words ∪ pendingLearned);
// chords.Set after runtime checks; focus policy swap (Excluded = c.Apps, never nil — Load
// guarantees non-nil lists, and ListSet is nil-receiver safe anyway); drop from pendingLearned
// every word now contained in c.Words; auto-enabled rule under autoMu (f = file value,
// r = runtime value, echoWindow = 5 s):
//   fileAutoEnabled = f;
//   f == r ⇒ autoUnsaved = false;
//   f != r && (autoUnsaved || now-lastAnnounced[f] < echoWindow) ⇒ stale file or a late echo of
//          an earlier toggle (e.g. on→off within a second: the tray's write of "true" lands
//          after the daemon is already off) ⇒ ignore f, autoUnsaved = true (the tray writes r);
//   otherwise ⇒ genuine external edit (hand edit, offline tray toggle) ⇒ setAutoEnabledLocked(f,
//          "config") (the body of SetAutoEnabled without taking autoMu; autoMu is not reentrant).
// Notifications are sent after autoMu is released. Then IPC status/ConfigWarnings.
func (s *Switcher) applyUserConfig(c *userconfig.Config)
// learnWord: detector.AddUserException(folded); append to pendingLearned (dedup) so the tray can
// persist it (PendingLearnedWords; empty on the bus when ipc-expose-words=false ⇒ memory only).
func (s *Switcher) learnWord(folded string)
type ipcController struct{ s *Switcher }     // implements ipc.Controller (may call Service synchronously, §3.9.2)
```
Fields that need restart (`focus-source`, `layout-source`, `layout-hint-timeout-ms`) are applied
at the next daemon start; `applyUserConfig` adds a warning when they differ from the running values.

### 3.11 Tray and settings GUI (WP9): `internal/tray`, `internal/i18n`

#### 3.11.1 Daemon client and app wiring

- **D-Bus connections**: the tray never uses `dbus.SessionBus()` for fork code (fyne systray
  owns that shared connection, C27). `ipc.Client` opens its own private connection; `App.forkConn`
  (`dbus.ConnectSessionBus()`, opened in `onTrayReady`, closed in `Quit`) serves
  `focus_extension.go`. Neither ever closes the shared bus.
- `daemon_client.go`: wraps `ipc.Client`; starts in `onTrayReady` (`app.go:108`) after the config
  watcher, stops in `Quit` (`app.go:177`); callbacks hop to the tray UI queue
  (`enqueueUIReliable`) and to GTK (`scheduleGTK`) for the settings window.
- **Persistence on the daemon's behalf** (D2: the daemon cannot write). `daemonPersister`, a
  single goroutine fed latest-wins by `OnStatus` (on connect via `GetAll` and on every
  `PropertiesChanged`): when `AutoEnabledUnsaved` ⇒ `userconfig.SetAutoEnabled(dir,
  st.AutoEnabled)` (skip if the same value was written < 2 s ago; retry after 5 s while still
  unsaved); for each `PendingLearnedWords` entry ⇒ `userconfig.AppendListEntry(dir, WordsName,
  w)` (idempotent, deduplicating). The daemon's watcher then sees the file, clears
  `AutoEnabledUnsaved` and drops the words from `PendingLearnedWords`. Writes happen off the GTK
  thread; failures are shown with `strGAErrorPersist` and retried. Chord toggles and learned
  words made while no tray runs are reconciled when the tray (re)connects, unless the daemon was
  restarted in between (then they are lost — documented in README and `docs/FORK.md`).
- `App.ToggleAuto()`: connected ⇒ `client.SetAutoEnabled(!state)` (persisted through the path
  above); else `userconfig.SetAutoEnabled(dir, !fileState)` (daemon reads it at start or, if it
  is running without IPC, through its watcher as an external edit).
- A second `userconfig.Watch` in the tray refreshes the window/menu on hand edits and on its own
  persistence writes.

#### 3.11.2 Tray menu and icon

- `backend_checkbox.go`: optional interfaces
  `type checkableTrayMenuItem interface{ trayMenuItem; SetChecked(bool) }`,
  `type checkboxTrayBackend interface{ AddMenuItemCheckbox(title, tooltip string, checked bool) trayMenuItem }`.
  StatusNotifier backend (`backend_status_notifier.go`, fyne.io/systray v1.12.2
  `AddMenuItemCheckbox`/`Check`/`Uncheck`) implements them; XEmbed does not ⇒ fallback to a plain
  item whose title carries the state (`strGAMenuOn/Off`). No change to `backend.go` interfaces.
- `tray.go`: item `mAutoToggle` after the service item (`onReady`, `tray.go:106`); click in
  `handleEvents` (`:217`) ⇒ `go app.ToggleAuto()`; `applyAutoState(ipc.ClientStatus)`: checked
  state, title (`strGAMenuUnavailable` when `!AutoAvailable` or not connected), tooltip suffix
  ("auto: on/off") in `applyLayout` (`:373`) and `applyDetectionStatus` (`:429`), icon via
  `GetNormalIconAuto` in `normalIcon` (`:401`). `settings_language.go` `Tray.applyLanguage`
  (`:75`, reached via `UpdateLanguage` at `:73`; the file has 103 lines) retitles it.
- `icon_auto.go`: `GetNormalIconAuto(mode TrayIconMode, code string, autoOn bool) []byte` — green
  12 px dot with 1 px dark outline at top-left of the 48 px raster, cached per `mode|code|on`
  (same `iconCacheMu` pattern as `overlayCache`).

#### 3.11.3 Settings window

`settings_window.go` edits are limited to: struct fields, `build()` (`:100`) wraps the existing
sections in a `gtk.Notebook` page "General" and adds page "Auto correction" from
`createAutoPage()`; default size 560×720; `createServiceSection` (`:204`) gains Start/Stop
buttons beside Restart (reusing `ServiceManager.Start/Stop`, `service.go:119,124`, and existing
`strButtonStart/strButtonStop`), sensitivity by status, 2 s refresh while visible; `loadConfig`
(`:653`) also loads `userconfig` and the extension status; `onApplyClicked` (`:1099`) adds an
independent branch: collect `userconfig.Settings` + lists, validate, `Update(dir,
DiffSettings(loaded, new))` (excluding `auto-enabled`), `ApplyListDiff(dir, name, added,
removed)` for each changed list (diff against the snapshot loaded into the editor — never a
whole-file rewrite, so a word learned while the window is open survives Apply), then
`client.Reload()` — no pkexec, no service restart for user-level keys; errors ⇒
`strGAErrorSave` / `strGAErrorApply`. When the tray's watcher reports a list change while the
editor has no unsaved edits, the editor reloads; with unsaved edits it keeps them (the diff
merges on Apply). Upstream's pkexec branch is untouched.

Auto page (`settings_auto.go`, `settings_auto_lists.go`, `settings_hotkeys.go`):
- Frame "Automatic correction": `autoEnabledCheck` (acts immediately via `ToggleAuto` path, not
  Apply), live status label (Active / Off / Service not running / No connection / Unavailable:
  reason / last correction), `minLettersSpin` 2..8 (adjustment kept in a field, GC comment at
  `settings_window.go:64-73`), `shortWordsCheck`, `learnOnUndoCheck`, `skipTerminalCheck`,
  `skipPasswordCheck`, `requireFocusCheck`, hint (Space only; undo with double Shift — true in
  both ReverseMode settings because the undo intercept runs on the raw gesture, §4).
- Frame "Hotkeys": two rows (Toggle auto mode / Convert selection), each a `gtk.ComboBoxText`
  with presets "Default (LShift+RShift)" / "Default (LShift+RCtrl+RAlt)", "Disabled", "Custom…"
  (opens the key picker), plus a keycap label (`settings_shortcuts.go:12-29` CSS) and a warning
  label for `ConflictOrderDependent` ("press Shift first"). Validation: `userconfig.ValidateChords`
  with the layout-switch set from config or `DetectionInfo.Scancodes` (new field, filled from the
  detect JSON in `detection_status.go`), `SwitchConflict` with gsettings `switch-input-source` and
  `switch-input-source-backward` (`ConflictAlways` ⇒ error).
- Frame "Exception words": `TreeView` over `ListStore(string)` with editable cells, entry + Add,
  Remove, "Add last corrected word" (from `LastCorrection`).
- Frame "Excluded applications": extension status row (from `QueryFocusExtension` +
  `WatchFocusExtension`; button Enable / "Enable at next login"; relogin dialog text), list editor,
  "Add running…" dialog over `ListRunningAppsForPicker` (multi-select; stores
  `NormalizeApp(ID)`, or the WM_CLASS when the Shell id is `window:N`/empty; falls back to
  desktop-file scan when unavailable), hint when the extension is not active.

Key picker (`key_picker.go`): new contexts `KeyPickerForChordToggleAuto`,
`KeyPickerForChordConvertSelection` (after the consts at `:109-114`) with titles, hints and
warnings; `keySelectionValid` ⇒ `userconfig.ValidateChord(codes) == nil`; messages for too
short / needs modifier / too long / text key not allowed (`strGAKeyPickerTextKey`); in chord contexts a bare Enter(28)/KP_Enter(96)/Escape(1) with
nothing else held passes through (confirm/cancel); `focus-out-event`/`grab-broken-event` clear
`capture.pressed` (keep `saved`); around `dialog.Run()` call `client.SuspendHotkeys(true/false)`
when connected. `keyPickerCapture` semantics (`:28-51`) and `sortScancodes`+`'+'` result
(`:311-320`) unchanged.

#### 3.11.4 i18n

- All fork strings in new `internal/tray/strings_ga.go` as `const strGA… = "strGA…"` IDs.
- `internal/i18n/i18n.go`: `const ForkKeyPrefix = "strGA"`, `var ForkLocales = []string{"en", "ru"}`.
- `i18n_test.go` `TestShippedCatalogsComplete` (`:84-93`) skips keys with `ForkKeyPrefix` for
  locales outside `ForkLocales`; new `TestForkKeysPresentInForkLocales` (en+ru, equal parameters
  per `sameParameters`) and `TestFallbackToEnglishForForkKey`.
- Minimum ID set (WP9 may add more with the same prefix): `strGATabGeneral`, `strGATabAuto`,
  `strGASectionAuto`, `strGACheckEnabled`, `strGAStatusActive`, `strGAStatusOff`,
  `strGAStatusDaemonDown`, `strGAStatusNoIPC`, `strGAStatusUnavailable` (%s),
  `strGAReason{Degraded,NoLayoutConverter,UnsupportedLayouts,LayoutSourceOff,LayoutUnknown,RootMode,DetectorError}`,
  `strGALabelMinLetters`, `strGACheckShortWords`, `strGACheckLearnOnUndo`, `strGACheckSkipTerminal`,
  `strGACheckSkipPassword`, `strGACheckRequireFocus`, `strGAHintTrigger`, `strGASectionHotkeys`,
  `strGALabelToggleChord`, `strGALabelSelectionChord`, `strGAPresetDefault` (%s), `strGAPresetDisabled`,
  `strGAPresetCustom`, `strGAHintChords`, `strGAChordOrderWarning` (%s), `strGAChordConflictSwitch`,
  `strGAChordOverlap`, `strGAChordConvertKey`, `strGAKeyPickerTitleToggle`, `strGAKeyPickerTitleSelection`,
  `strGAKeyPickerHintChord`, `strGAKeyPickerTooShort`, `strGAKeyPickerNeedsModifier`, `strGAKeyPickerTooLong`,
  `strGAKeyPickerTextKey`, `strGAErrorPersist` (%s), `strGAHintNotPersisted`,
  `strGASectionWords`, `strGAWordsHint`, `strGASectionApps`, `strGAAppsHint`, `strGAButtonAdd`,
  `strGAButtonRemove`, `strGAButtonAddRunning`, `strGAButtonAddLast`, `strGARunningAppsTitle`,
  `strGARunningAppsError` (%s), `strGAExt{Active,Disabled,NeedsRelogin,NotInstalled,Error,OutOfDate,NotGnome}`,
  `strGAButtonEnableExtension`, `strGAButtonEnableNextLogin`, `strGAReloginDialog`, `strGAErrorSave`,
  `strGAErrorApply` (%s), `strGAErrorToggle`, `strGAMenuOn`, `strGAMenuOff`, `strGAMenuUnavailable`,
  `strGAMenuHint` (%s), `strGATooltipOn`, `strGATooltipOff`, `strGALastCorrection` (%s %s),
  `strGALastCorrectionNone`, `strGAConfigWarnings` (%s).
- Printf parameters limited to `%s`/`%d` (`validate.go:56-91`).

### 3.12 CLI (WP11 edits `internal/gswitch/cli.go`)

Add to the switch at `cli.go:25-50` and `runHelp`:
- `--auto-check <text> [--shown us|ru]` ⇒ `os.Exit(gsruntime.RunAutoCheck(args[2:], os.Stdout, os.Stderr))`
- `--layout-state [--watch SECONDS]` ⇒ `layoutstate.RunDiagnostics(...)` with layouts from config
  or `GetCurrentLayouts(nil)`, default watch 10 s.
Both are read-only and need neither root nor `/dev/input`.

### 3.13 Packaging (WP10)

- **Identity**: RPM `gswitch-auto`; `Version: <upstream>+auto.<N>` (first `0.8.0+auto.1`),
  `Release: 1`; dev builds `0.8.0+auto.1.dev<n>.g<sha>[.dirty]`, pre-tag `0.8.0+auto.0.dev<n>.g<sha>`
  (rpm 6.0.2 vercmp verified: `0.8.0+auto.1-1` > `0.8.0-1`, > `0.8.0-2`, < `0.8.1-1`; `~` forms
  sort below upstream and are forbidden). Tags `v0.8.0+auto.N`; workflows trigger on `tags: ['v*']`
  with `if: contains(github.ref_name, '+auto.')` (`+` is a glob repeat operator in GitHub filters).
- **`packaging/nfpm.yaml`** (single source; `.goreleaser.yml` is `git rm`-ed): `version_schema:
  none`; `provides: [gswitch = ${GSWITCH_VERSION}-${GSWITCH_RELEASE}]`; `replaces: [gswitch < 0.9]`
  (= RPM Obsoletes); **no** `conflicts:` (an unversioned `Conflicts: gswitch` would match our
  own `Provides` — rpmlint `self-conflict`; Obsoletes+Provides already replace the package and
  a later upstream RPM would file-conflict on `/usr/bin/gswitch`); `depends: [systemd, gtk3]`; `recommends:
  [desktop-file-utils, wl-clipboard]`; `suggests: [gnome-shell-extension-appindicator]`; `license:
  MIT AND BSD-3-Clause-Modification AND LicenseRef-Fedora-Public-Domain`; homepage
  `https://github.com/vabuter/gswitch-auto`. Contents: **`builds/gswitch` → `/usr/bin/gswitch` and
  `builds/gswitch-tray` → `/usr/bin/gswitch-tray` (0755) explicitly** (C21) + every rpm-applicable
  entry of `.goreleaser.yml:108-211` (unit, udev rule, `default.conf` as `config|noreplace`,
  autostart/app desktop files, polkit policy, icons) + `shell-extension/gswitch-focus@vabuter.github.io`
  → `/usr/share/gnome-shell/extensions/gswitch-focus@vabuter.github.io` (`type: tree`) +
  licences (`type: license`) `LICENSE`, `third_party/punto-rs/LICENSE` → `punto-rs-LICENSE`,
  `third_party/punto-rs/hunspell-ru-LICENSE`, `third_party/punto-rs/NOTICE.md` →
  `THIRD-PARTY-NOTICES.md`, clipboard licence under `/usr/share/licenses/gswitch-auto/` +
  `README.md` → `/usr/share/doc/gswitch-auto/README.md` (`type: doc`). No `Requires: gnome-shell`.
- **Scriptlets**: upstream `scripts/{preinstall,postinstall,preremove,postremove}.sh` unchanged
  (C22: they do real work — modprobe uinput, udevadm reload/trigger, per-user restart). New
  `scripts/pretrans.sh` records `/run/gswitch-auto/enabled.<uid>` for logged-in users whose unit
  is enabled (because upstream `preremove.sh:17-21,29-39` disables the unit when rpm erases the
  obsoleted `gswitch` with `$1=0`); new `scripts/posttrans.sh` re-enables/starts the unit for
  those users, removes markers, prints the "log out and back in, then enable the extension" hint.
  All scripts exit 0 and guard every command with `command -v`.
- **Scripts/tools**: `scripts/build-rpm.sh [--version V] [--release R] [--out DIR] [--skip-tests]
  [--nfpm PATH]` (CGO build with `-X main.version=$V` for `cmd/gswitch/main.go:10` and
  `cmd/gswitch-tray/main.go:14`, `go test ./...`, `check-extension.sh`, nfpm from PATH, `--nfpm`,
  `~/go/bin/nfpm`, or `go run github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.48.0`; sha256 checksums;
  `rpm -qp` assertions); `scripts/version.sh [--verify]`; `scripts/check-extension.sh` (one dir
  under `shell-extension/`, metadata parses, uuid == dir name == `focus.ExtensionUUID` grep in
  `internal/gswitch/focus/state.go` == nfpm dst, `shell-version` contains "50"); `Makefile`
  (targets build test vet lint rpm install ext-check version clean; `NFPM ?= $(HOME)/go/bin/nfpm`).
- **CI**: `ci.yml` adds `rpm` apt package, RPM build + metadata assertions, extension check
  (`python3 -m json.tool`, `node --check`), `shellcheck scripts/*.sh`, and job `install-test` in
  `registry.fedoraproject.org/fedora:44` (install upstream v0.8.0 RPM from GitHub, then ours;
  assert obsoletion, `rpm -V`, `--version`). `release.yml` rewritten (build script + `gh release
  create`; OBS job removed). `obs.yml` (guarded `github.repository == 'arumata/gswitch'`),
  `codeql.yml`, `dependabot.yml` untouched.
- **Docs**: `docs/FORK.md` (Russian operator notes: versioning, release and rebase checklist —
  `git fetch upstream --tags`, archive tag, `rerere`, `--force-with-lease`, bump base and Obsoletes
  bound —, list of modified upstream files, install/rollback incl. `dnf swap gswitch-auto
  ./gswitch_0.8.0_linux_amd64.rpm`); README "About this fork" section at the top, badges, config
  reference for `~/.config/gswitch`, D-Bus interface, extension and privacy notes, punto-rs and
  hunspell-ru credits, `--auto-check`/`--layout-state`, the persistence limitation (toggles and
  learned words are saved by the tray; without a running tray they last until the daemon
  restarts); SECURITY.md (session bus + IBus AF_UNIX only, the daemon keeps upstream's sandbox
  and writes nothing under `$HOME`, learned words written by the tray to `exceptions-words.txt`
  0600, words on the session bus — including `PendingLearnedWords` — gated by
  `ipc-expose-words`, fork issue tracker); CONTRIBUTING.md (fork note, `strGA` i18n rule, Fedora build deps, make targets);
  CLAUDE.md build section (`make build/test/rpm/install`, nfpm path, no goreleaser).
- `.golangci.yml`: ireturn allow entries if needed; G304 exclusion for `userconfig/.*\.go`;
  funlen exclusion for `ipc/introspect.*\.go`. Until then, WPs use inline `//nolint`/`#nosec`
  with reasons.

---

## 4. Integration points in existing files (v0.8.0 line numbers)

| File | Function / location | Change | WP |
|---|---|---|---|
| `internal/gswitch/runtime/converter.go` | `Converter` struct `:45-54`; `Convert` `:489-611` (toggle `:505-511`, scan `:516-552`, emit `:557-610`); `invalidateUndo` `:638-641` | `undoGen` field; extract `ConvertRange(start,end)`; `Convert` = scan + `ConvertRange(startIndex, len)`; `UndoGeneration()`; `invalidateUndo` bumps gen | WP0 |
| `runtime/switcher.go` | `performConversion` `:823-920`; reader literal `:860` | extract `:848-914` into `emitConversionEvents(events, emitOptions)`; `layoutReader()`; fields `layoutReaderFn`, `onOwnSwitch` | WP0 |
| `runtime/layout.go` | `LayoutInfo` `:88-92`; literals `:398-402`, `:413-417`; include merge `:467-470`; key parse `:474-491` (drop at `:483-486`) | `Unmapped map[string]bool` | WP2 |
| `runtime/switcher.go` | struct `:36-90` | fork fields (§3.10.1) | WP11 |
| `runtime/switcher.go` | `NewSwitcher` before `cleanupLogger = false` `:221` | `s.initFork()` | WP11 |
| `runtime/switcher.go` | `initLayoutConverter` `:272-288` (after `loadLayoutPair`, `:278-281`) | `s.layoutSpecs = layouts` | WP11 |
| `runtime/switcher.go` | `Run` after `s.eventChan = make(...)` `:447` | `s.startFork()` | WP11 |
| `runtime/switcher.go` | `Run` select `:455-464` | add `case fn := <-s.controlChan: fn()`; replace `time.After(EventLoopTimeoutMs * time.Millisecond)` (`:461`) with `time.After(s.loopWait())`; call `s.autoTick()` after the select | WP11 |
| `runtime/switcher.go` | `Run` after loop, before `s.inputReader.Close()` (`:466-469`) | `s.stopFork()` | WP11 |
| `runtime/switcher.go` | `processKeyEvent` after `s.trackSelectionModifiers(event)` `:664` | `if s.forkPreProcess(event) { return }` | WP11 |
| `runtime/switcher.go` | `processKeyEvent`, `if action != ActionNone { … }` block `:681-718` | append `else if s.auto != nil && event.Code == KEY_SPACE && event.Value == K_DOWN { s.auto.OnSpace(s.converter) }` | WP11 |
| `runtime/switcher.go` | `performConversion`, first statement, **before** the ReverseMode swap `:824-831` | `if s.forkBeforeConversion(action) { return }` where `forkBeforeConversion` (in `fork.go`) does: `s.auto == nil` ⇒ false; raw `action == ActionConvertWord && s.auto.TryUndo(s.converter)` ⇒ true; otherwise `s.auto.Cancel("manual")` ⇒ false | WP11 |
| `runtime/switcher.go` | `performConversion` after `s.inputReader.Flush()` `:917` | `s.afterInputDiscarded()` | WP11 |
| `runtime/switcher.go` | `convertSelection` defer `:983-991`, after `s.drainEventChan()` | `s.afterInputDiscarded()` | WP11 |
| `runtime/switcher.go` | `Close` `:1159-1173` | `s.stopFork()` | WP11 |
| `runtime/runner.go` | `Run` `:12-34` | no change required (fork init lives in `NewSwitcher`/`Run`); optional debug print of user-config warnings between `:23` and `:29` | WP11 |
| `internal/gswitch/cli.go` | `Execute` switch `:25-50`, `runHelp` | `--auto-check`, `--layout-state` | WP11 |
| `internal/tray/settings_window.go` | struct `:18-81`; `build` `:100`; `createServiceSection` `:204`; `Show` `:639`; `loadConfig` `:653`; `onApplyClicked` `:1099`; `updateServiceStatus` `:1494` | notebook, Start/Stop, user-config load/apply branch | WP9 |
| `internal/tray/tray.go` | struct `:19-53`; `onReady` `:106`; `handleEvents` `:217`; `applyLayout` `:373`; `normalIcon` `:401`; `applyDetectionStatus` `:429` | auto menu item, badge, tooltip | WP9 |
| `internal/tray/app.go` | struct `:18-31`; `onTrayReady` `:108`; `Quit` `:177`; dir creation `:352` (0750) | daemon client, `ToggleAuto`, user-config watcher; chmod 0700 in writers | WP9 |
| `internal/tray/backend_status_notifier.go` | menu item creation | implement `checkboxTrayBackend` | WP9 |
| `internal/tray/key_picker.go` | contexts `:109-114`; title/hint/warning `:117-145`; `keySelectionValid` `:147-155`; messages `:159-170`; key-press handler `:284-293`; dialog run `:301-302` | chord contexts, passthrough, focus-out, SuspendHotkeys | WP9 |
| `internal/tray/detection_status.go` | `DetectionInfo`; JSON mapping | `Scancodes []uint16` | WP9 |
| `internal/tray/settings_language.go` | `Tray.applyLanguage` `:75` (`UpdateLanguage` `:73`; file is 103 lines) | retitle auto item | WP9 |
| `internal/i18n/i18n.go`, `i18n_test.go` `:84-93`, `locales/en.json`, `locales/ru.json` | — | fork prefix policy + strings | WP9 |
| `.goreleaser.yml` | whole file | `git rm` (mirrored into `packaging/nfpm.yaml`) | WP10 |
| `.github/workflows/ci.yml`, `release.yml`, `.golangci.yml`, `README.md`, `.github/SECURITY.md`, `CONTRIBUTING.md`, `CLAUDE.md` | see §3.13 | packaging/docs | WP10 |

Upstream files intentionally **not** modified: `types.go`, `input_reader.go`,
`gnome_wayland_layout_switch.go` (+ its test), `config/*`, `deps.go`, `scripts/{pre,post}*.sh`,
`configs/gswitch.service` (D2: the daemon keeps upstream's sandbox), `configs/70-gswitch.rules`,
polkit policy, desktop files, `default.conf`, `obs.yml`. `docs/FORK.md`'s list of modified
upstream files is generated from the table above and must not list the unit.

---

## 5. Work packages and file ownership

Rules: a WP may modify only the files it owns. Cross-WP dependencies are on the **interfaces in
§3** only; a WP may develop against stubs/fakes of another WP's API but must not commit edits to
files it does not own. Every WP ends with `go build ./... && go vet ./... && go test ./...`
green (upstream ~320 tests unchanged) and `gofmt`/`goimports` clean. Commit messages in English.

**Identifier namespacing in shared packages.** WPs that add files to the same package in the
same phase (`runtime`: WP0, WP2, WP3, WP7, then WP8, WP11; `tray`: WP5, WP9) must not collide on
unexported names. The production identifiers named in §3 are normative; every other new
unexported identifier — above all test helpers and fakes (`ev`, `down`, `up`, `fakeClock`,
`fakeSource`, …) — carries the WP prefix: WP0 `emitTest*`/`keystateTest*`, WP2
`wordTest*`/`renderTest*`/`taintTest*`, WP3 `layoutReaderTest*`, WP7 `chordTest*`, WP8
`autoTest*`, WP11 `forkTest*`; tray: WP5 `focusExtTest*`, WP9 `gaTest*`. Shared helpers are not
created across WPs; duplicate small helpers under different prefixes are acceptable.

### Phase plan

```
Phase 0 (serial):   WP0
Phase 1 (parallel): WP1  WP2  WP3  WP4  WP5  WP6  WP7  WP10(scaffold)
Phase 2 (parallel): WP8 (needs WP0,WP1,WP2,WP4)   WP9 (needs WP4,WP5,WP6 APIs)
Phase 3 (serial):   WP11 (needs all)  →  WP10 finalisation (CI/RPM on the integrated tree)
```
No two WPs in the same phase own the same file.

### WP0 — Runtime foundation
- **Owns (new)**: `runtime/types_fork.go`, `runtime/keystate.go`, `runtime/keystate_test.go`,
  `runtime/emit_test.go`, `internal/gswitch/fold/{fold.go,fold_test.go}`. **(modified)**:
  `runtime/converter.go`, `runtime/converter_test.go` (additions only), `runtime/switcher.go`
  (only the `performConversion` extraction and two fields of §3.1.4).
- **Depends**: none.
- **Tests**: golden capture of `Convert(ActionConvertWord|ActionConvertAll)` output on every
  existing fixture **before** the refactor, compared after, including the shift-only-tail fixture
  `[a↓ L↓ Space↓ L↑]` + double-Shift (§3.1.3); `ConvertRange` invalid bounds ⇒ nil, shift-only
  range ⇒ toggle + replayed shifts (not nil); `emitConversionEvents` path selection table:
  manual opts × {verifyWayland, verifyX11, neither} unchanged, `confirm=true` with both verify
  flags false ⇒ confirm path with the given reader (fake reader that never changes ⇒ error, no
  Backspace emitted), `confirm=true, reader=nil` ⇒ `errConfirmReaderMissing` and zero emitted
  events; `onOwnSwitch == nil` ⇒ no panic; `fold` table (§3.1.5);
  `UndoGeneration` bumps on text K_DOWN, Backspace, ClearBuffer, not on shift pushes;
  `emitConversionEvents` with a fake vKeyboard + fake reader emits exactly what
  `performConversion` emitted before (shift releases, confirm path, delays via injected sleep if
  needed); `KeysDown/AnyKeyDown/CapsLockState` on an `InputReader` with no devices ⇒ empty/false/
  `known=false`, closed fd ⇒ no panic.
- **Acceptance**: zero behaviour change for manual mode; all existing tests green.

### WP1 — Language detector
- **Owns**: `internal/gswitch/lang/**` (incl. `data/`, `testdata/`), `third_party/punto-rs/{LICENSE,hunspell-ru-LICENSE,NOTICE.md}`.
- **Depends**: WP0 (`fold.Word` for exception folding).
- **Tests**: §6.2 (`model_test.go`, `detector_test.go`, `eval_test.go`, `BenchmarkDecide`).
- **Acceptance**: golden values bit-exact; eval gates met; `Decide` follows the step order of
  §3.3 (short-list and compound not gated by the candidate filter); no `runtime` import (only
  `fold`); no file I/O except `embed`.

### WP2 — Word extraction, rendering, taint
- **Owns (new)**: `runtime/xkbnames.go`, `runtime/xkbnames_test.go`, `runtime/auto_word.go`,
  `runtime/auto_word_test.go`, `runtime/render_xkb_test.go`, `runtime/taint.go`,
  `runtime/taint_test.go`. **(modified)**: `runtime/layout.go`, `runtime/layout_test.go` (additions).
- **Depends**: WP0 (constants in `types_fork.go`).
- **Tests**: `XKBKeyName` 48 entries, bijective, expected X keycodes (TLDE 49, AE01 10, AD01 24,
  AC01 38, BKSL 51, AB01 52, LSGT 94) and cross-check against `/usr/share/X11/xkb/keycodes/evdev`
  minus 8 (skip if absent); `lastWordRange` table (single trailing space ok; double space;
  `word⏎ `; trailing LShift K_UP after Space ok; trailing Shift K_DOWN rejected; keypad/Enter/
  KPASTERISK inside rejected; 1 key rejected; > max rejected; shift held at start rejected; word
  at buffer start; word after previous Enter); `renderWord` with fixture KeyMaps (shift pairs,
  Caps alphabetic vs two-level, Caps+Shift, Unmapped ⇒ ErrUnrenderable, digits/punctuation
  verbatim, equal rune counts, ShiftedKeys/InteriorShift); real XKB (skip without
  `/usr/share/X11/xkb`): us/ru rows equal punto-rs `keymap.rs` rows for 47 keys × 2 levels;
  `ghbdtn`→`привет`, `k.,jdm`→`любовь`, reverse with ru active; `lexicalVeto` (digits, `/x`, `~`,
  `-x`, `://`, `@`, `\`; not `k.,jdm`, not `ghbdtn/`); core bounds (`ghbdtn?` core 0..6,
  `"Ghbdtn` drops the leading quote, `k.,jdm` keeps all 6); `shiftShapeVeto`: vetoed `GTK`,
  `GTK,`, `getValue`, `iPhone`; **not** vetoed `Привет`, `Ghbdtn`, `ghbdtn?` (us, `?` =
  Shift+AB10 ⇒ ru `,`), `Ghbdtn!`, `руддщ!` (ru, `!` = Shift+AE01), `hello?` typed on ru,
  `РуддщЖ` (intended `Hello:`); `renderWord(buf, r.Start, r.TextEnd)` on a buffer ending in
  Space succeeds while `renderWord(…, r.End)` returns `ErrUnrenderable` (documents the bound);
  taint rules one by one + boundary/reset semantics (§3.2.4), including `MarkDiscarded`: taint
  survives text keys until a boundary, is cleared by the text K_DOWN after that boundary, and
  is cleared by a non-Ctrl BufKiller; `Unmapped` set for a fixture with `dead_acute`.
- **Acceptance**: `NewLayoutConverter` behaviour unchanged (existing layout tests green).

### WP3 — Layout tracker
- **Owns (new)**: `internal/gswitch/layoutstate/**`, `runtime/layout_reader.go`,
  `runtime/layout_reader_test.go`.
- **Depends**: none.
- **Tests**: tracker with fake Source/clock (initial Unknown; xkb:us::eng→0, xkb:ru::rus→1,
  variants; non-pair/`libpinyin`/`""` ⇒ Unknown; dedup without callback/generation bump; hint ⇒
  Pending ⇒ signal ⇒ Confirmed with timer cancelled; hint timeout ⇒ one Resync; Available(false)
  ⇒ Unknown + callback; ambiguous pair; OnChange never blocks `Engine()`); address tests
  (`WAYLAND_DISPLAY=wayland-0` ⇒ `<mid>-unix-wayland-0`; `DISPLAY=:0` ⇒ `-unix-0`; `host:1.0` ⇒
  `-host-1`; `IBUS_ADDRESS_FILE`/`XDG_CONFIG_HOME` overrides; machine-id precedence; env wins;
  comments; dead pid ⇒ `ErrIBusNotRunning`; glob fallback newest live); source tests
  (`engineNameFromDesc` valid/malformed/nested variant; signal handling ignores other members;
  backoff 200 ms…5 s with injected sleep; "No global engine." ⇒ `Engine("")`; no message to
  `org.freedesktop.IBus` on the session connection); `ParseXKBEngine` table; composite reader
  picks tracker when Confirmed and gsettings otherwise; `triggerAndConfirmGNOMEWaylandLayoutSwitch`
  with the tracker reader returns nil after a simulated signal and errors when unchanged;
  `newCompositeLayoutReader(nil, env)` yields `gnomeWaylandLayoutStateReader{env}` and
  `trackerLayoutStateReader{nil}.CurrentState()` errors without panicking; `engineNameFromDesc`
  golden for the double-wrapped `Properties.Get` value, the single-wrapped `GetGlobalEngine`
  reply and the signal argument; hint race: signal processed **before** `NoteSwitchHint` ⇒
  Pending ⇒ timeout ⇒ `Resync` calls `Source.Query` first ⇒ Confirmed (not Settings); Query
  failing ⇒ gsettings ⇒ Settings; `NewIBusSource` re-resolves the address after a simulated
  `ibus restart` (address file and pid change); integration test gated by `GSWITCH_IBUS_TEST=1`.
- **Acceptance**: no `runtime` import inside `layoutstate`; `gswitch --layout-state` logic
  available as `RunDiagnostics`.

### WP4 — User configuration
- **Owns**: `internal/gswitch/userconfig/**` (incl. `templates/`). (`configs/gswitch.service` is
  not modified by any WP.)
- **Depends**: WP0 (`fold`), `config.IsModifierKey`.
- **Tests**: Load defaults on missing dir/file; every key round-trips; invalid values ⇒ default +
  warning (one subtest per key); upstream/unknown keys warned and ignored; CRLF/BOM/quotes/
  sections/duplicates (last wins); 64 KiB cap; EACCES ⇒ error + AutoEnabled=false (skip as root);
  `Dir`/`DirFor` precedence (relative XDG ignored); lists normalisation (Cyrillic, ё→е, edge
  punctuation stripped via `fold.Word` so `ghbdtn,` == `ghbdtn`, trailing comment, inner
  whitespace rejected, 65 runes rejected, `.desktop` stripped, `window:3` rejected), dedupe,
  caps, `MatchApp` exact/WM_CLASS/glob **with raw arguments** (`org.gnome.Ptyxis.desktop`,
  `Kitty`) matching normalised entries, `(*ListSet)(nil).MatchApp/Contains/Len` safe, `Load`
  returns non-nil `Words`/`Apps` on ENOENT, EACCES and parse errors; `ApplyListDiff` preserves an
  entry appended concurrently after the caller's snapshot, removes only listed entries, keeps
  comments; `Update` preserves comments/unknown lines/order and replaces
  duplicates; `Save` creates from template with 0600/0700 (and chmods an existing 0750 dir);
  atomic write leaves no temp on failure; `AppendListEntry` dedupe + missing trailing newline;
  `RemoveListEntry` keeps comments; concurrency 2×50 appends + Save + SetAutoEnabled under flock;
  `Watch` (rename ⇒ callback < 1 s; identical rewrite ⇒ none; no initial callback; dir missing at
  start then created ⇒ callback; dir removed/recreated recovers; ForcePoll; ctx cancel ⇒ returns
  nil); chords (`ParseChord` table: none/empty/`42+54`/`42+100+97` == `42+97+100`/dup/5
  keys/0/0x300/non-numeric/lone 42 rejected/two letters rejected; `ValidateChord`: `42+30`
  (Shift+A), `42+57` (Shift+Space), `29+28` rejected, `42+88` (Shift+F12) and `97+119` accepted;
  `String` round trip; `Overlaps`; `ValidateChords` overlap, layout-switch superset `[56,42]` vs
  `{42,56,97}`, convert-key member); `SwitchConflict` (`42+97+100` vs
  `['<Alt>Shift_L','<Shift>Alt_L']` ⇒ OrderDependent with the RAlt-first order described;
  `42+54` ⇒ None; `56+42` ⇒ Always; unknown accelerator syntax ⇒ None + warning).
- **Acceptance**: `go test ./internal/gswitch/config` and `./internal/tray` unchanged and green;
  no write helper is referenced from `internal/gswitch/runtime` (grep test in WP11).
  `SwitchConflict` expectations are frozen only after M9 has been run in both press orders on
  GNOME 50 (§3.6.5).

### WP5 — Focus: Shell extension, daemon client, tray helper
- **Owns**: `shell-extension/**`, `scripts/install-extension.sh`, `internal/gswitch/focus/**`,
  `internal/tray/focus_extension.go`, `internal/tray/focus_extension_test.go`.
- **Depends**: WP0 (`fold`); `AppMatcher` is an interface.
- **Tests**: `policy_test.go` (every rule incl. `EverAvailable` vanish ⇒ locked, `Disabling`
  reasons, require-extension, empty app id allowed, glob via a fake matcher, `Excluded == nil`,
  Allow passes `AppKey`/`WmClassKey` to the matcher, each `Purpose*` constant has type `Purpose`
  — compile-time `var _ Purpose = PurposeNone` etc.); `client_test.go`
  over a private `dbus-daemon --session` (skip if absent) with a fake extension and fake
  ScreenSaver: present at start, appears later (matches before NameHasOwner), FocusChanged ⇒ Seq++
  and one OnChange, InputPurposeChanged, `Disabling("locked")` then vanish ⇒ Locked,
  `FocusChanged("org.gnome.Ptyxis.desktop","org.gnome.Ptyxis")` ⇒ `AppKey == "org.gnome.ptyxis"`,
  `WmClassKey == "org.gnome.ptyxis"`; vanish without Disabling ⇒ Locked "extension-vanished",
  `Disabling("disabled")` ⇒ not locked,
  reappear ⇒ unlocked, GetFocus timeout (Start non-blocking, Close < 1 s), ListRunningApps decode
  and ErrUnavailable, no bus ⇒ no panic, Close idempotent; `metadata_test.go` (uuid ==
  `ExtensionUUID` == directory name, shell-version contains "50"); tray `stateFromInfo` table
  (`{}`+dir ⇒ NeedsRelogin, `{}` no dir ⇒ NotInstalled, 1.0 Active, 2.0/6.0 Disabled, 3.0 Error,
  4.0 OutOfDate), `appendToGsettingsList` (`['a', 'b']`, `@as []`, no duplicate), and
  `ListRunningAppsForPicker` maps `window:N` ids to `ID=""` (picker stores the WM_CLASS).
- **Acceptance**: `node --check extension.js` passes; `gnome-extensions pack` succeeds (manual);
  manual M2.

### WP6 — IPC
- **Owns**: `internal/gswitch/ipc/**`.
- **Depends**: none.
- **Tests**: handler tests with a fake Controller (SetAutoEnabled via method and property Set,
  Reload error ⇒ `Error.Config`, SuspendHotkeys owner recorded, watchdog expiry with injected
  clock, caller-vanish release); introspection XML parses (`encoding/xml`) and matches §3.9.1;
  `UpdateStatus` diff emits only changed properties (fake prop sink); `UpdateStatus`/`Notify*`
  return immediately while `propLoop` is blocked (non-blocking contract);
  **`TestPropertySetAutoEnabledNoDeadlock`** — mandatory, not gated by `GSWITCH_DBUS_TEST`
  (skipped only when no `dbus-daemon` binary exists): real `prop.Export` on a private
  `dbus-daemon --session`, a Controller whose `SetAutoEnabled` synchronously calls
  `svc.NotifyAutoEnabled` and `svc.UpdateStatus`; a second connection calls
  `org.freedesktop.DBus.Properties.Set(AutoEnabled, true)` 100× interleaved with
  `UpdateStatus` from another goroutine; every Set returns < 1 s, `Get` then reflects the
  controller's value, and `Close` returns < 1 s (fakes alone cannot catch the godbus lock
  re-entry, C28); gated
  `GSWITCH_DBUS_TEST=1` under `dbus-run-session`: Service↔Client round trip (GetAll,
  property Set, AutoEnabledChanged/Corrected/CorrectionUndone delivery, owner loss ⇒
  Connected=false ⇒ reconnect), second instance not primary owner.
- **Acceptance**: no `runtime`/GTK import; all calls bounded by contexts; no `dbus.SessionBus(`
  token in non-test files of `internal/gswitch/ipc` (source scan test); `Client.Stop` leaves an
  existing `dbus.SessionBus()` connected (gated D-Bus test).

### WP7 — Chord detector
- **Owns (new)**: `runtime/chord.go`, `runtime/chord_test.go`.
- **Depends**: none.
- **Tests**: fires on full release for `L↓R↓R↑L↑` and `L↓R↓L↑R↑` exactly once on the last K_UP;
  held-Shift double tap `L↓R↓R↑R↓R↑L↑` never fires and ends IDLE; exact set only
  (`Ctrl↓L↓R↓R↑L↑Ctrl↑`, `L↓R↓A↓A↑R↑L↑` never fire); 3-key chord fires in all 6 press × 6 release
  orders; K_REPEAT ignored; stale K_DOWN for a held code drops other held entries; `Resync`/`Reset`;
  zero value is pass-through; `Set` replaces bindings; no re-arm while BROKEN.
- **Acceptance**: no dependency outside package `runtime`'s existing types; the zero value and
  a `Set(nil)` detector never return `fired=true`; the upstream switcher tests that build
  `&Switcher{...}` literals pass unchanged with WP7's file present.

### WP8 — Auto engine
- **Owns (new)**: `runtime/auto.go`, `runtime/auto_deps.go`, `runtime/auto_test.go`,
  `runtime/autocheck.go`, `runtime/autocheck_test.go`.
- **Depends**: WP0 (`ConvertRange`, `UndoGeneration`, `invalidateUndo`, `fold`), WP1 (`lang` API), WP2
  (`lastWordRange`, `renderWord`, vetoes, `WordTaint`), WP4 (`userconfig.Load` in `autocheck.go` only). WP3/WP5 are reached only through
  `AutoDeps` function values (no import of `layoutstate`/`focus` in these files).
- **Tests** (fake clock, fake Judge/Layout/Focus/Caps/InputIdle, recording Emit): §6.1 list.
- **Acceptance**: engine never calls `ClearBuffer`; all hooks are nil-safe at the Switcher level
  (`s.auto == nil` ⇒ upstream behaviour).

### WP9 — Tray GUI and i18n
- **Owns (new)**: `internal/tray/{daemon_client.go, daemon_client_test.go, settings_auto.go,
  settings_auto_lists.go, settings_auto_test.go, settings_hotkeys.go, settings_hotkeys_test.go,
  tray_auto.go, tray_auto_test.go, icon_auto.go, icon_auto_test.go, backend_checkbox.go,
  strings_ga.go}`. **(modified)**: `internal/tray/{settings_window.go, tray.go, app.go,
  backend_status_notifier.go, key_picker.go, key_picker_test.go, detection_status.go,
  settings_language.go, tray_initial_appearance_test.go}`, `internal/i18n/{i18n.go, i18n_test.go,
  locales/en.json, locales/ru.json}`.
- **Depends**: WP4 (`userconfig`), WP5 (`focus_extension.go`, `focus.App`), WP6 (`ipc.Client`).
- **Tests**: non-GTK: `formatChordLabel`, preset/combo mapping, conflict message selection,
  `DetectionInfo.Scancodes` use, `TestKeySelectionValidForChordContext`,
  `TestKeyPickerCaptureChordKeepsLastFullSet` (hardware 50+62 ⇒ saved {42,54}), Enter
  passthrough, focus-out clears pressed; `applyAutoState` with `recordingTrayBackend` (checkbox
  state, title fallback, icon switch, tooltip); menu click ⇒ `ToggleAuto`; `applyLanguage`
  retitles; `ToggleAuto` offline writes `auto-enabled` via userconfig into a temp dir;
  `daemonPersister`: `AutoEnabledUnsaved=true` ⇒ one `auto-enabled` write of the latest value
  (two rapid statuses ⇒ the last value wins), `PendingLearnedWords=[a,b]` ⇒ both appended once,
  repeated statuses are idempotent, write failure ⇒ `strGAErrorPersist` and retry; settings
  Apply uses `ApplyListDiff` (a word appended to the file after the editor loaded survives
  Apply); source scan: no `dbus.SessionBus(` in fork tray files (`daemon_client.go`,
  `focus_extension.go`, `settings_auto*.go`, `tray_auto.go`); badge icon
  48×48 PNG, differs from base, cached; i18n policy tests (§3.11.4). GTK (`GSWITCH_GTK_TEST=1`
  under `xvfb-run`, like `modifier_swap_test.go:16-19`): auto page round trip through a temp
  user dir, validation errors, Start/Stop sensitivity per `ServiceStatus`, notebook has 2 pages.
- **Acceptance**: upstream tray tests green; auto-only changes neither call pkexec nor restart the
  service.

### WP10 — Packaging, CI, docs
- **Owns**: `packaging/nfpm.yaml`, `scripts/{build-rpm.sh, version.sh, pretrans.sh,
  posttrans.sh, check-extension.sh}`, `Makefile`, `.goreleaser.yml` (delete),
  `.github/workflows/{ci.yml, release.yml}`, `.golangci.yml`, `docs/FORK.md`, `README.md`,
  `.github/SECURITY.md`, `CONTRIBUTING.md`, `CLAUDE.md`.
- **Depends**: paths only (§3.13); final run after WP11.
- **Tests**: `scripts/version.sh --verify` (vercmp pairs of §3.13); RPM metadata assertions
  (provides `gswitch = V-1`, obsoletes `gswitch < 0.9`, **no** conflicts entry; files: both
  binaries, user unit, udev rule, polkit policy, extension `metadata.json`,
  `hunspell-ru-LICENSE`; scripts contain `pretrans` and `posttrans`; no
  `/usr/lib/systemd/system/gswitch.service`; the packaged user unit is byte-identical to
  `configs/gswitch.service` of upstream v0.8.0); `check-extension.sh`; `shellcheck`;
  `env -i PATH=/nonexistent sh scripts/pretrans.sh` exits 0; fedora:44 install-test job.
- **Acceptance**: `make rpm` produces `dist/gswitch-auto-0.8.0+auto.<N>…x86_64.rpm` +
  `checksums.txt` on the host (nfpm via `~/go/bin/nfpm`).

### WP11 — Integration
- **Owns (new)**: `runtime/fork.go`, `runtime/auto_control.go`, `runtime/userconfig_apply.go`,
  `runtime/layout_glue.go`, `runtime/focus_glue.go`, `runtime/fork_test.go`,
  `runtime/switcher_fork_test.go`. **(modified)**: `runtime/switcher.go` (hooks of §4 only),
  `runtime/runner.go` (optional), `internal/gswitch/cli.go`.
- **Depends**: all WPs.
- **Tests**: `TestProcessKeyEventChordsKeepUpstreamGestures` (word/phrase/Ctrl+double-Shift/
  Ctrl+held+double from `switcher_test.go:97-168` give the same `selectionHandler` calls with
  default chords; toggle count 0); `TestProcessKeyEventToggleChord` (`L↓R↓R↑L↑` toggles once, no
  trailing shift events in `GetBuffer()`, then `a b c` + double-Shift ⇒ `Process()` returns
  `ActionConvertWord`); `TestProcessKeyEventSelectionChord` (3 release orders × ConvertKey 0/119 ⇒
  one `selectionConvertLayout` call; buffer cleared); restore-on-flip with a fake tracker state;
  `TestSpaceArmsAndTickFires` with fake engine deps through the real hooks;
  `TestAutoEmitConfirmsWithoutVerifyFlags` (both verify flags false, explicit LSKeys: the auto
  Emit goes through the tracker reader; a tracker that never changes ⇒ no Backspace emitted, no
  `lastFix`, no `Corrected`, `Skipped` reason `emit:…`); `TestUndoIntercept`: ReverseMode
  false/true — bare double-Shift right after a fix undoes in **both** modes; with ReverseMode=true
  the held-Shift gesture falls through to upstream; a manual ConvertAll after a fix calls
  `Cancel("manual")` and a later double-Shift does not `TryUndo`; `TestSuspendHotkeysConsumesEvents`;
  `TestOnLayoutStateNeverClearsBuffer`; `TestOwnSwitchRecognised` (generation path only);
  `TestApplyUserConfigDisablesChordContainingLSKeys`;
  **`TestManualConversionWithoutTracker`** — runs the real `initFork()` on a test Switcher with
  a fake vKeyboard, a temp user dir and each of: `layout-source=none`; a KDE `SessionEnv`; root
  mode (injectable `geteuid` returning 0); and a GNOME Wayland env with
  `verifyGNOMEWaylandLayoutSwitch=true` plus a stubbed `runGsettingsForGNOMEWayland`. Asserts
  `tracker == nil`, `auto == nil`, `layoutReaderFn == nil`, `onOwnSwitch == nil`, then feeds
  `a b c L↓ L↑ L↓ L↑` through `processKeyEvent` and requires exactly the events (and gsettings
  reads) that the same Switcher without `initFork()` emits — no panic, no tracker access;
  **`TestAfterInputDiscardedTaintsCurrentWord`** — with a fake engine: `afterInputDiscarded()`
  (as called after Flush and after `convertSelection`'s drain), then `b d t n Space` ⇒ no arm,
  `LastReason == "tainted:discarded"`; the next word `h e l l o Space` is evaluated normally;
  also checks `chords.Resync` with `KeysDown` erroring ⇒ `chords.Reset()`;
  `TestFirstWordAfterBufKillerNotSkipped` (`g h` Alt+Tab-style BufKiller, focus Seq bump, then
  `ghbdtn Space` arms — the snapshot is re-taken after the BufKiller);
  `TestRestoreOnFlipWaitsForLateSignal` (fake tracker whose generation bumps 60 ms after the
  chord's last K_UP, `chordSawAltShift=true` ⇒ restore emitted; without the bump ⇒ none;
  `chordSawAltShift=false` ⇒ no wait); `TestAutoEnabledPersistenceRule` (chord toggle ⇒
  `AutoEnabledUnsaved`; file catches up ⇒ cleared; on→off within 1 s with the stale "true"
  landing afterwards ⇒ ignored as echo, state stays off, Unsaved=true; hand edit ⇒ applied with
  source `config`); `TestLearnedWordsPendingUntilFileHasThem` (learn ⇒ `PendingLearnedWords`
  contains it; `ipc-expose-words=false` ⇒ empty on the bus, still in the detector; after the file
  contains it ⇒ dropped); `TestDaemonNeverWrites` (temp dir made read-only: chord toggle, learn,
  reload produce no write attempt and no warning; plus a source scan that `runtime` never calls
  `userconfig.{Save,Update,SetAutoEnabled,AppendListEntry,RemoveListEntry,ApplyListDiff,
  WriteListText,EnsureFiles}`); `TestExcludedAppMatchesRawExtensionID` (real `userconfig.ListSet`
  + real `focus.Policy`, §3.5.2); `TestDefaultMinLettersAgree`
  (`userconfig.DefaultSettings().MinLetters == lang.DefaultOptions().MinLetters`);
  `TestStatusAvailabilityMapping` (tracker Unknown/Settings ⇒ `layout-unknown`, Pending ⇒ still
  available); `TestEnableAutoReasons` (degraded, nil converter, unsupported pair, no tracker,
  root mode); existing switcher tests unchanged (Switcher literals without fork fields keep
  working).
- **Acceptance**: §6.4 manual script passes on the host; `gswitch --debug` shows one `auto:` line
  per Space.

---

## 6. Test and evaluation plan

### 6.1 Unit tests — auto engine (WP8, `runtime/auto_test.go`)

1. Space K_DOWN with Fix verdict arms; fires only after holdoff **and** Space K_UP **and** empty
   held set **and** `InputIdle()==true`; Space still held at deadline ⇒ re-arm +30 ms; > 1 s ⇒
   cancel `space-held`.
2. Any K_DOWN (letter, LShift, Ctrl, Meta) or K_REPEAT during holdoff cancels; K_UP of a letter
   pressed before Space does not cancel; Space while Ctrl/Alt/Meta held never arms.
3. Pipeline order and reasons: disabled, modifier, each `lastWordRange` reason, tainted,
   focus-changed, focus:<reason>, layout-unknown, layout-changed, unrenderable, lexical veto,
   shift-shape, detector:<reason> — each ⇒ `Skipped`, expected `LastReason`, no Emit.
4. Fire gates: rate limit, input pending, buffer changed (length, content, non-shift event after
   End, halved buffer), layout index/generation changed, focus Seq changed ⇒ skip; Emit error ⇒ no
   lastFix, buffer untouched.
5. Emitted events for `ghbdtn␣` equal `ConvertRange(Start, End)`: LSKeys toggle, 7 Backspace
   pairs, 7 replays with key-ups; a trailing LShift K_UP after Space is not part of the range.
6. Undo: after a fix, `TryUndo` re-emits the same range when only shift events follow; a text
   key or Backspace in between disarms (generation bump) ⇒ false; external layout change
   disarms; a focus Seq change since the fix ⇒ false; `Cancel(any)` disarms; learn-on-undo with
   count 1 calls `OnUndo(word, true)`, with count 2 only on the second undo of the same folded
   word (`fold.Word`, so `Ghbdtn` and `ghbdtn` count together).
7. `OnLayoutChange(own=true)` keeps state; external change cancels pending and disarms undo;
   `Reset(keys)` replaces the held set with `keys` (nil ⇒ empty) so a later Space can fire, but
   the word in progress is tainted `discarded`: `b d t n Space` right after `Reset` ⇒ no arm,
   reason `tainted:discarded`; after the next boundary (`Space`, then `h e l l o Space`) the
   engine evaluates normally; a non-Ctrl BufKiller after `Reset` also clears the taint; the
   engine never calls `ClearBuffer`.
8. Stats counters and `GarbleRisk` increment when `InputIdle()` is false right after Emit.
9. Word-start snapshot boundaries: `g h` + BufKiller (Alt/Tab) + focus Seq bump + `ghbdtn Space`
   arms (snapshot re-taken after the BufKiller); `g h` + Space + Backspace (boundary erased) +
   `bdtn Space` keeps the original snapshot.
10. Punctuation (real XKB layouts, skipped without `/usr/share/X11/xkb`): `ghbdtn?␣` (us) arms
    and the emitted range retypes it as `привет,␣`; `Ghbdtn!␣` arms; `руддщ!␣` (ru) arms ⇒
    `hello!␣`; `GTK␣`, `GTK,␣`, `getValue␣` are skipped with `shift-shape`.
11. Integrated pipeline eval (`autocheck_test.go`, real XKB, skipped without it): every
    `layout_cases.tsv` en/ru wrong-layout row is evaluated through `lastWordRange` →
    `renderWord` → vetoes → `Decide`, bare and with the intended layout's `,` `.` `!` `?` keys
    appended. Gates: false fixes == 0 on both splits; ru_wrong/en_wrong recall with punctuation
    ≥ the WP1 bare-word recall − 2 rows per split; 2-key short words with punctuation are logged,
    not gated (3 keys leave the short-list path, as in punto-rs).

### 6.2 Detector bit-exactness and evaluation (WP1)

- `model_test.go`: embedded sizes 19,683 / 35,937 / 444,422 / 1,708,013; `bloomBits` golden lists;
  `known` golden (привет, hello true; ршщзх, ghbdtn false); cost golden (1e-9); Bloom fill ratio
  0.49–0.51.
- `detector_test.go`: all EN/RU-pair assertions of punto-rs `cli/src/layout/tests.rs`
  (`:78-149`, `:206`, `:217-219`) + the 7 hyphen cases of `:227-245` re-targeted to EN/RU
  (`e-mail`, `t-shirt`, `self-made`, `well-known`, `x-ray`, `-elm` keep; `nfr-nfrb` fix); the
  vacuous `convert_text` placeholder is not ported (C17). Plus: `tls` Scores
  {6.375, 5.175, false, true} blocked only by exception; `dnf` {10.325, 4.275, false, false};
  `MinLetters=4` ⇒ `ghb` NotCandidate; `ShortWords=false` ⇒ `jy` NotCandidate; `MinLetters=8`
  ⇒ `jy` still `Fix("short-list")` (step 4 is not gated by the candidate filter) and `nfr-nfrb`
  still `Fix("compound")` although step 3 alone returns `non-letter`; `Decide("ghbdtn?",
  "привет,")` == `Decide("ghbdtn", "привет")` verdict (edge punctuation stripped by the core);
  `IsException("Ghbdtn,")` true after `SetUserExceptions(["ghbdtn"])`;
  `SetUserExceptions(["ghbdtn"])` ⇒ Keep `exception`; `ёжик` normalised to `ежик`;
  `AddUserException`; unsupported pair ⇒ NotCandidate.
- `eval_test.go` over `testdata/layout_cases.tsv` (en/ru rows only, 606 per split; keys built
  with the `rows_test.go` oracle, rendered, `Decide` with `DefaultOptions()` and built-in
  exceptions). **Hard gates**: false fixes == 0 on both splits. Recall gates: test split
  ru_wrong ≥ 100/105 (measured 102), en_wrong ≥ 93/100 (95), 2-key short_wrong ≥ 13/26 (14);
  dev split ru_wrong ≥ 99/105 (101), en_wrong ≥ 92/101 (94), short_wrong ≥ 12/24 (13).
  `t.Logf` exact counts. `GSWITCH_EVAL_GRID=1` prints margin {0,0.5,1,1.5,2,3} × maxAlt
  {4,5,6,7,99} false-fix/recall grid.
- `BenchmarkDecide("ghbdtn")`: expect < 2 µs, no allocations beyond the rune slices (backs the
  "microseconds" claim the research flagged as unmeasured).
- If the eval false-fix gate fails at `MinLetters=3`, WP1 sets `lang.DefaultOptions().MinLetters`
  to 4 in its own files and reports it; the gate itself is not relaxed. The coupled changes are
  assigned to Phase 3, when WP1/WP4 are finished: WP11 raises `userconfig.DefaultSettings()`'s
  `MinLetters` and the template comment (explicit ownership exception for this one value; no
  other WP is active), `TestDefaultMinLettersAgree` keeps both in sync, and WP10's finalisation
  records the decision in `docs/FORK.md`.

### 6.3 Integration and CI

- `go build ./... && go vet ./... && go test ./...` on every WP merge (upstream ~320 tests
  unchanged). `golangci-lint run` (v2.6.0 as in `ci.yml:79`) in CI.
- Gated suites: `GSWITCH_IBUS_TEST=1` (live IBus), `GSWITCH_DBUS_TEST=1` (`dbus-run-session`),
  `GSWITCH_GTK_TEST=1` (`xvfb-run`); focus client tests need `dbus-daemon` (present on the host
  and ubuntu runners).
- Extension: `python3 -m json.tool`, `node --check`, uuid checks (`check-extension.sh`).
- RPM: metadata assertions, `version.sh --verify`, fedora:44 install-over-upstream job.

### 6.4 Manual test script (user, GNOME 50 Wayland, after `make rpm`)

Prerequisite: no other gswitch instance in the foreground. Steps M1–M13 (plus M8b); each lists the action and
the expected observation. Use gnome-text-editor (or gedit) as the test field unless stated.

- **M1 Install.** `make rpm && sudo dnf install ./dist/gswitch-auto-*.rpm` while upstream runs.
  Expect `rpm -q gswitch` ⇒ not installed; `gswitch --version` ⇒ `0.8.0+auto.N`;
  `systemctl --user is-enabled gswitch.service` ⇒ `enabled`, `is-active` ⇒ `active`;
  `systemctl --user cat gswitch.service` ⇒ identical to upstream (no `ConfigurationDirectory`).
  Restart the tray (`pkill -x gswitch-tray; gio launch
  /usr/share/applications/gswitch-tray.desktop`); then `ls -ld ~/.config/gswitch` ⇒
  `drwx------ vab` (created/chmodded by the tray).
- **M2 Extension.** Log out and back in. `gnome-extensions info gswitch-focus@vabuter.github.io`
  ⇒ `State: ACTIVE` (if not: tray Settings → Auto correction → Enable). `gdbus call --session
  --dest org.gnome.Shell.Extensions.GswitchFocus --object-path /org/gnome/Shell/Extensions/GswitchFocus
  --method org.gnome.Shell.Extensions.GswitchFocus.GetFocus` with a terminal focused ⇒
  `('org.gnome.Ptyxis.desktop', 'org.gnome.Ptyxis', 'terminal')` (or the installed terminal).
- **M3 Layout tracker.** `gswitch --layout-state` ⇒ socket path, address, pid, `xkb:us::eng` → 0;
  press Alt+Shift during the watch ⇒ `xkb:ru::rus` → 1 within tens of ms.
- **M4 Detector.** `gswitch --auto-check ghbdtn --shown us` ⇒ verdict fix, alt `привет`;
  `--auto-check hello` ⇒ keep; `--auto-check ghb2` ⇒ not-candidate; `--auto-check руддщ --shown ru`
  ⇒ fix, alt `hello`.
- **M5 Toggle (R2).** Tray running. Press LShift+RShift together and release. Expect tray
  checkbox/badge on, within ~1 s `grep auto-enabled ~/.config/gswitch/auto.conf` ⇒ `true`
  (written by the tray), `busctl --user get-property io.github.vabuter.GswitchAuto
  /io/github/vabuter/GswitchAuto io.github.vabuter.GswitchAuto AutoEnabledUnsaved` ⇒ `false`.
  Repeat ⇒ off. Toggle twice within one second ⇒ final state off both in the daemon
  (`AutoEnabled`) and in the file (echo rule). `busctl --user set-property … AutoEnabled b true`
  returns immediately (no hang) and the tray follows. With the tray quit (`pkill -x
  gswitch-tray`): toggle ⇒ `AutoEnabledUnsaved` ⇒ `true`; start the tray ⇒ file updated, flag
  `false`.
- **M6 Auto-correction (R1).** Auto on, layout `us`. Type `ghbdtn ` then pause ⇒ `привет ` and
  layout is `ru`; continue typing in Russian. Type (in ru) `руддщ ` ⇒ `hello `, layout `us`.
  Type `hello ` in us ⇒ unchanged. Type `ghbdtn` + Enter ⇒ unchanged (Enter never triggers).
  Type `ghbdtn ` and immediately keep typing the next word without pause ⇒ no correction (cancel
  on key-down); note it in the debug log (`gswitch --debug` with the service stopped, or
  `journalctl`). Type `nfr ` ⇒ `так `. Type `GTK ` ⇒ unchanged (shift-shape). Hold `n` to
  autorepeat inside a word ⇒ that word is never corrected. Punctuation (layout `us`): `ghbdtn? `
  ⇒ `привет, `; `Ghbdtn! ` ⇒ `Привет! `; in `ru`: `руддщ! ` ⇒ `hello! `. CapsLock on, layout
  `us`: `GHBDTN ` ⇒ `ПРИВЕТ ` (CapsLock does not trigger the shift-shape veto); CapsLock off
  again. Explicit layout-switch keys: `sudo gswitch --configure` with `layout-switch=56+42`,
  restart the service ⇒ `ghbdtn ` still becomes `привет ` (auto replay is tracker-confirmed);
  restore `layout-switch=auto`.
- **M7 Exceptions (R7).** Add `Ghbdtn,` to Exception words in the GUI, Apply ⇒ stored as
  `ghbdtn`; typing `ghbdtn ` is left alone. Remove it. Let a fix happen, then double-Shift
  immediately ⇒ word restored and layout switched back; with learn-on-undo on, the word appears
  in `~/.config/gswitch/exceptions-words.txt` (appended by the tray) and in the list. Keep the
  settings window open while doing this, then change something else and Apply ⇒ the learned
  word is still in the file (diff-based Apply).
- **M8 Exclusions (R6).** In the terminal type `ghbdtn ` ⇒ no correction (terminal purpose /
  seeded list). Focus a password field (Settings → Users → Password) ⇒ no correction. Add
  gnome-text-editor via "Add running…" ⇒ no corrections there; remove ⇒ corrections return. Lock
  the screen, unlock ⇒ `FocusSuppressedReason` was `locked` during the lock (busctl), auto works
  again after unlock. Add `org.gnome.Ptyxis.desktop` (raw, as shown by M2) by hand to
  `exceptions-apps.txt` ⇒ still matches (normalised).
- **M8b Extension absent (D6).** `gnome-extensions disable gswitch-focus@vabuter.github.io`.
  Expect: GUI Auto correction tab shows the extension hint (Disabled, Enable button);
  `busctl --user get-property … FocusSource` ⇒ `absent`; app exclusions inactive (corrections
  happen in the excluded editor); auto still works elsewhere; with `auto-require-focus=true`
  (Apply) ⇒ no corrections at all, `FocusSuppressedReason` ⇒ `focus-unavailable`. Re-enable the
  extension (it reports again without relogin, since the code did not change) and reset
  `auto-require-focus=false`.
- **M9 Selection chord (R4).** Select `ghbdtn` in the editor; press LShift, then RAlt, then RCtrl;
  release all ⇒ selection becomes `привет`, layout unchanged. Repeat pressing RAlt first ⇒ same
  text result; the daemon log shows "restored layout flipped by GNOME binding" and the layout is
  unchanged. Also record (`gswitch --layout-state --watch 20` in a second terminal) whether
  GNOME 50 switches on the press of LShift or on a release in the RAlt-first order; feed the
  result back into WP4's `SwitchConflict` tests before they are frozen. Ctrl+double-Shift
  selection conversion still works.
- **M10 Upstream gestures (R3).** With auto off and on: type `ghbdtn`, double-Shift ⇒ `привет`;
  double-Shift again ⇒ back; held-Shift phrase gesture converts the line; select `Hello` and
  press Ctrl + held Shift + double-tap the other Shift ⇒ `hELLO` (case swap). Configure a custom
  convert key (`sudo gswitch --configure`, e.g. Pause), restart the service: Pause converts the
  last word, Shift+Pause the line, Ctrl+Pause the selection. Set `reverse-mode=true`, restart:
  double-Shift converts the line and held-Shift the word as upstream; with auto on, after an auto
  fix a bare double-Shift **undoes** it (M7 hint holds in both modes) and does not convert the
  whole line. Restore the original config.
- **M11 GUI (R5, R8).** Settings → General: Stop ⇒ status stopped, tray item "service not
  running", Start ⇒ running; Restart; "Start at login" checkbox toggles `systemctl --user
  is-enabled`. Auto correction tab: change both hotkeys via Custom… (picker suspends daemon
  hotkeys while open), Apply ⇒ new chords work immediately without service restart; the selection
  chord shows the "press Shift first" warning; min letters 4 ⇒ `nfr ` no longer fixed.
- **M12 Robustness.** `ibus restart` ⇒ log shows IBus unavailable → reconnect → Confirmed; auto
  resumes. `systemctl --user restart gswitch.service` ⇒ auto-enabled state preserved (tray was
  running when it was toggled). `rm -r ~/.config/gswitch` and let the tray recreate it (open
  Settings, Apply) ⇒ the daemon picks the new files up without a restart.
- **M13 Rollback (optional).** `sudo dnf swap gswitch-auto ./gswitch_0.8.0_linux_amd64.rpm`, then
  `systemctl --user enable --now gswitch.service`.

---

## 7. Risks, mitigations, open questions

### 7.1 Risks and chosen mitigations

| # | Risk | Mitigation (chosen) |
|---|---|---|
| K1 | No grab: a key pressed during the ~200–300 ms replay interleaves with backspaces; mutter drops uinput keys while the seat key count ≠ 0 | Fire only with nothing held (engine set + EVIOCGKEY), empty queue, Space released, 120 ms quiet; cancel on any K_DOWN/K_REPEAT; short replay (3 ms/event, 60 ms post-switch, no trailing sleep, no Flush); `GarbleRisk` counter; grab deferred (Q1) |
| K2 | Buffer ≠ screen (mouse clicks, caret moves, app-side autocorrect, keys dropped by Flush/drain) | Taint observer for keyboard-visible cases incl. `discarded` after Flush/drain (C25); focus Seq check; damage bounded to one word; undo via double-Shift; pointer tracking deferred (Q2). Accepted residue: a word typed right after a caret-moving BufKiller (arrows, Home/End) may be glued to on-screen text left of the caret |
| K3 | Detector false positives (Bloom ~0.75 % false "known") | Margin + alt-cost gates, exceptions, lexical/shift-shape vetoes, terminal/password suppression; eval hard gate 0 false fixes; `auto-min-letters` adjustable |
| K4 | Short-word recall ~50 % (linux.words junk inflates shown_known) | Accepted (safe direction); model regeneration deferred (Q5) |
| K5 | Layout source wrong/stale: non-chord switches (Super+Space, panel click) have a short stale window; gnome-shell's fallback to `xkb:us::eng` for engines missing in IBus | Require Confirmed + unchanged generation from word start to fire; Pending on physical chord; log mismatch between resolved layouts and IBus engine at startup |
| K6 | D-Bus activation hazard of `org.freedesktop.IBus` on the session bus | Never address it (C20); unit test |
| K7 | Persistence depends on the tray (D2: the daemon cannot write) | Tray autostarts with the session and reconciles on (re)connect via `AutoEnabledUnsaved`/`PendingLearnedWords`; toggles/learned words made while no tray runs survive until the daemon restarts; documented in README/`docs/FORK.md`; tray write failures shown (`strGAErrorPersist`) |
| K8 | Concurrent writes to `auto.conf`/`exceptions-words.txt` (tray Apply, tray persister, hand edits) | Single programmatic writer process (tray); flock + key-preserving `Update`; list edits as `ApplyListDiff`; daemon echo rule for `auto-enabled` (§3.10.5) |
| K9 | Extension needs relogin; pinned `shell-version: ["50"]` goes OUT_OF_DATE on GNOME 51 | Tray states incl. NeedsRelogin/OutOfDate; fail-open by default, `auto-require-focus` for fail-closed; bump metadata per Fedora release |
| K10 | Lock screen while extension disabled | `Disabling("locked")` + vanish ⇒ Locked (fail closed); ScreenSaver as late hint; passwords rarely contain Space |
| K11 | Input purpose coverage is best effort (XWayland/XIM, some Electron) | App exclusions + detector + undo; never advertised as password protection |
| K12 | R4 default chord collides with `<Alt>Shift_L` in RAlt-first order | `SwitchConflict` warning in GUI; daemon restores the layout if it flipped during the chord, waiting ≤150 ms for a late IBus signal when Alt and Shift overlapped (§3.10.4); press/release timing verified in M9 before WP4 tests freeze; chord kept as the fixed requirement |
| K13 | Chords not grabbed: apps see the modifier presses | Modifier-only defaults are harmless; validation requires ≥1 modifier and rejects text keys (only modifiers and `ChordExtraKeys`) |
| K14 | Lost key-ups after `Flush`/drain make chords or the held set stale | `afterInputDiscarded` resync from EVIOCGKEY; stale-down resync in the detector |
| K15 | `auto-delay-ms`/`auto-switch-delay-ms` unmeasured on GNOME 50 | Tracker-confirmed switch before typing on every auto fix (`emitOptions.confirm`, independent of the verify flags); values tunable; 0 falls back to upstream delays; tune with `--debug` during M6 |
| K16 | Privacy: learned words written to disk; corrected/learned words on the session bus | Files 0600 in a 0700 dir; `ipc-expose-words` (also empties `PendingLearnedWords`, so learned words then stay in daemon memory only); documented in README/SECURITY; daemon never logs words unless `GSWITCH_AUTO_LOG_WORDS=1` under `--debug` |
| K17 | Rebase friction (switcher.go, converter.go, tray files, README, workflows) | Hooks are single lines calling methods in new files; constants in `types_fork.go`; `docs/FORK.md` lists modified upstream files; `.goreleaser.yml` deletion signals packaging drift |
| K18 | Licence obligations (hunspell-ru modification marking) | `third_party/punto-rs/*` shipped under `/usr/share/licenses/gswitch-auto/`; uk excluded |
| K19 | Obsoletes erase path: upstream `preremove.sh` disables the unit with `$1=0` | `%pretrans`/`%posttrans` marker handoff; verify on the host (M1); fallback `systemctl --user enable --now gswitch.service` |
| K20 | `gswitch --debug` beside the service: two daemons both act | Debug instance logs "IPC disabled"; docs tell to stop the service first (upstream practice) |
| K21 | Two keyboards sharing scancodes collapse in held sets | Same as upstream shift tracking; accepted |
| K22 | `$XDG_CONFIG_HOME` set in the shell but not in the systemd user environment ⇒ daemon/tray disagree on the dir | Daemon exposes `ConfigDir`; tray compares with its own and shows a warning |
| K23 | Fork D-Bus traffic disturbing fyne systray's shared connection (C27) | Private connections for `ipc.Client`, the focus-extension helpers and the daemon's own clients; source-scan tests forbid `dbus.SessionBus(` in fork files |
| K24 | godbus property-lock re-entry (C28) | Service owns all property writes in `propLoop`; property Set only enqueues; mandatory real-bus deadlock test |

### 7.2 Open questions (deferred) with the default chosen

| # | Question | Default for v1 |
|---|---|---|
| Q1 | Keyboard grab with held-key refusal, timestamp partition and re-injection (punto-rs style) | Not implemented; no config key; revisit if `GarbleRisk` is significant in practice |
| Q2 | Pointer-button taint (needs a udev uaccess rule for mice and a second reader) | Not implemented; risk K2 accepted |
| Q3 | `auto-min-letters` 3 vs 4 | 3 (punto-rs parity, eval hard gate); raise to 4 if the Go eval gate fails |
| Q4 | Learn-on-undo threshold | `auto-learn-on-undo=true`, `auto-learn-undo-count=1` |
| Q5 | Regenerate Bloom models at ≥20 bits/word (needs `unmunch` from hunspell-devel) | Use punto-rs blobs unchanged |
| Q6 | XEmbed checkbox via `gtk_check_menu_item` | Title fallback only |
| Q7 | Sender allowlist on the extension interface | None (app ids only, same scope as portals) |
| Q8 | Clear the converter buffer on focus change (Punto behaviour) | No (keeps R3 exact); engine Seq check instead |
| Q9 | Publishing a .deb from the same nfpm.yaml | Keep deb overrides, do not publish |
| Q10 | Using `Tracker.WaitChange` instead of 50 ms polling in switch confirmation | Polling reused (minimal upstream diff); optimisation later |
| Q11 | Let the daemon persist directly via `ConfigurationDirectory=gswitch` in the user unit | Not in v1: it changes the upstream unit fixed by D2. Possible later only with the user's explicit sign-off recorded in §1.4 and the unit listed in `docs/FORK.md`'s modified files |
