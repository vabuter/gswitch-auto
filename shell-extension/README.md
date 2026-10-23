# gswitch Focus Reporter (GNOME Shell extension)

`gswitch-focus@vabuter.github.io` tells the gswitch-auto daemon which
application has the keyboard focus and what kind of text field is focused, so
that automatic layout correction can stay out of terminals, password fields,
the lock screen and applications the user excluded. GNOME Shell's own
`org.gnome.Shell.Introspect` is restricted to portal backends
(`js/misc/introspect.js`), so on Wayland an extension is the only practical
source of this information; on X11 the daemon also reads `_NET_ACTIVE_WINDOW`
itself, but only the extension knows the text-field purpose (strict purpose
mode, SPEC-X11 §3.3).

## Two trees, one extension

GNOME Shell 45 changed the extension format to ES modules, so the same
extension is kept twice, with the same UUID, `metadata.json` fields (except
`shell-version`) and D-Bus interface:

| Tree | Format | `shell-version` | Shipped by |
|---|---|---|---|
| `shell-extension/gswitch-focus@vabuter.github.io/` | ESM (`import … from 'gi://…'`, `export default class … extends Extension`) | `50` | the `gswitch-auto` RPM (Fedora 44) |
| `shell-extension/legacy/gswitch-focus@vabuter.github.io/` | legacy GJS (`imports.gi`, `imports.ui.main`, top-level `function init()` returning the state object) | `43`, `44` | the `gswitch-auto` `.deb` (Debian 12) |

Both install to `/usr/share/gnome-shell/extensions/gswitch-focus@vabuter.github.io`;
a package ships exactly one of them. Installing the wrong tree for the running
Shell makes the extension `OUT_OF_DATE` (the tray reports it).

The legacy file is the ESM file ported line by line. Only three things differ:

- the header: `const {Clutter, Gio, GLib, IBus, Shell} = imports.gi;
  const Main = imports.ui.main; const IBusManager = imports.misc.ibusManager;`
  instead of `import` statements, and `IBusManager.getIBusManager()` instead
  of the named import;
- signal handling: `connect` with stored handler ids and `disconnect` in
  `destroy()` instead of `connectObject`/`disconnectObject`
  (`misc/signalTracker.js` exports only the standalone functions in 43);
- the entry points: a plain `class GswitchFocusExtension` with
  `enable()`/`disable()` and `function init() { return new
  GswitchFocusExtension(); }` instead of `export default class … extends
  Extension`.

Everything else (`IFACE_XML`, the purpose and hint tables, `hintMask`,
`securePurpose`, `purposeTable`, `FocusReporter`) is identical, and
`scripts/check-extension.sh` plus the Go tests in `internal/gswitch/focus`
fail when the shared parts drift. **Edit the ESM tree first, then port the
change to the legacy tree** (K-X6 in SPEC-X11).

Pin `shell-version` for each new GNOME release only after testing.

## What it exposes

The extension owns `org.gnome.Shell.Extensions.GswitchFocus` on the session
bus (through gnome-shell's connection) and exports
`/org/gnome/Shell/Extensions/GswitchFocus`:

| Member | Kind | Meaning |
|---|---|---|
| `Version` (`u`) | property | interface version, `1` |
| `FocusedApp` (`s`) | property | Shell.App id of the focused window, e.g. `org.gnome.Ptyxis.desktop`; `window:N` for windows without a `.desktop` file (not stable across sessions); `""` when nothing is focused |
| `FocusedWmClass` (`s`) | property | WM_CLASS of the focused window |
| `InputPurpose` (`s`) | property | `normal`, `alpha`, `digits`, `number`, `phone`, `url`, `email`, `name`, `password`, `pin`, `terminal`, `date`, `time`, `datetime`, `none` (no text field) or `unknown` |
| `GetFocus() → (s app_id, s wm_class, s purpose)` | method | the three values above |
| `ListRunningApps() → a(sss)` | method | `(id, name, wm_class of the first window)` per running app; used by the settings window's "Add running…" dialog |
| `FocusChanged(s app_id, s wm_class)` | signal | every focus-window change, also between windows of one app |
| `InputPurposeChanged(s purpose)` | signal | the purpose changed |
| `Disabling(s reason)` | signal | sent just before the extension stops: `locked` (screen lock) or `disabled` |

`PropertiesChanged` is emitted for the three string properties.

Privacy: no window titles, no text, no process ids. Any session-bus client can
read the focused application id, the same scope as GNOME's portals.

## How it works

- Focus: `global.display` `notify::focus-window`, plus `Shell.WindowTracker`
  `notify::focus-app` for late app matching; ids come from
  `Shell.WindowTracker.get_window_app()`.
- Purpose: gnome-shell's IBus manager signals. `focus-out` sets `none`; a
  `set-content-type` counts only while an input context is focused (pairing
  rule). gnome-shell's IBus manager does not re-emit `focus-in` for its own
  input context, which serves every Wayland client and shell entry, so a
  focused `Main.inputMethod` stands in for that `focus-in`. Without IBus,
  `Main.inputMethod` `notify::content-purpose` is the fallback. Enum values
  are looked up in the IBus/Clutter typelibs at run time.
- Secret fields: a field whose content hints include `HIDDEN_TEXT` or
  `PRIVATE` (IBus; Clutter `HIDDEN_TEXT` or `SENSITIVE_DATA`) is reported as
  `password` unless it already reports `password` or `pin`. Qt 6 on Wayland
  reports password line edits as a normal field with these hints. Fields that
  request privacy this way (for example in some private-browsing windows) are
  therefore treated as password fields as well.
- Lock: the extension uses the default `user` session mode, so the Shell
  disables it when the screen locks. It sends `Disabling("locked")` first;
  the daemon treats the session as locked until the extension is back, and
  treats a disappearance without `Disabling` as locked too (fail closed).

Purpose reporting is best effort: some XWayland and Electron clients report
no content type. Exclusion lists and the daemon's other safeguards cover them.

### GNOME Shell 43/44 on X11 (legacy tree)

- Every X11 client talks to IBus through its own input context
  (`GTK_IM_MODULE=ibus`), so `focus-in`/`focus-out`/`set-content-type` arrive
  per application; `Main.inputMethod` only serves the Shell's own entries.
- GTK's IBus module never focuses IBus for a password entry
  (`visibility=FALSE`, ibus 1.5.27 `ibusimcontext.c`): a password field looks
  like "no text field" (`none` after the previous field's `focus-out`). The
  daemon's strict purpose mode (default on X11) corrects only in fields with
  a known non-password purpose, which covers this case.
- Applications without an IBus input context (xterm, Java/SWT, Wine, some
  Electron builds) report no purpose at all; in strict mode they are never
  corrected.
- The Shell scans extension directories once per session and
  `ReloadExtension` is not supported: log out and in after an install, or
  press Alt+F2, type `r`, Enter (X11 only). The lock screen disables
  user-mode extensions exactly as on 50, so `Disabling("locked")` works the
  same.
- Before relying on the legacy tree on a new Shell version, confirm in
  Looking Glass (Alt+F2 `lg`) that `Main.inputMethod.currentFocus`,
  `Main.inputMethod.contentPurpose` and `Main.inputMethod.contentHints`
  exist: `_onIBusContentType` and `_initialPurpose` depend on them.

## Installing

The `gswitch-auto` RPM installs the ESM tree and the `.deb` installs the
legacy tree, both to
`/usr/share/gnome-shell/extensions/gswitch-focus@vabuter.github.io`. The
tray's settings window (Auto correction → Excluded applications) shows the
extension's state and enables it.

GNOME Shell scans extension directories only at login and caches extension
modules, so after the first install and after every code upgrade **log out and
back in** (X11: Alt+F2, `r`). `EnableExtension` fails for an extension the
running Shell has not seen; the tray (and the script below) then add the UUID
to `org.gnome.shell enabled-extensions` so that it starts at the next login.

For development, from the repository root:

```bash
scripts/install-extension.sh                  # pack + gnome-extensions install --force (ESM, user copy)
scripts/install-extension.sh --legacy         # the same with the legacy tree (GNOME 43/44)
scripts/install-extension.sh --link           # symlink this checkout instead (--legacy applies too)
scripts/install-extension.sh --uninstall      # remove the user copy
```

A user copy in `~/.local/share/gnome-shell/extensions` shadows the system
copy.

## Checking

```bash
gnome-extensions info gswitch-focus@vabuter.github.io     # State: ACTIVE
gdbus call --session --dest org.gnome.Shell.Extensions.GswitchFocus \
  --object-path /org/gnome/Shell/Extensions/GswitchFocus \
  --method org.gnome.Shell.Extensions.GswitchFocus.GetFocus
# with a terminal focused: ('org.gnome.Ptyxis.desktop', 'org.gnome.Ptyxis', 'terminal')
# on Debian 12:           ('org.gnome.Terminal.desktop', 'Gnome-terminal', 'terminal')
gdbus monitor --session --dest org.gnome.Shell.Extensions.GswitchFocus
journalctl --user -b -g gswitch-focus    # extension warnings, if any
grep -c 'function init' /usr/share/gnome-shell/extensions/gswitch-focus@vabuter.github.io/extension.js
# 1 = legacy tree installed, 0 = ESM tree
```

Offline checks: `scripts/check-extension.sh` (`make ext-check`) validates both
trees: metadata, the format markers (`export default class` / `function
init(`), the byte-identical shared helpers, the nfpm entries (`packager: rpm`
for the ESM files, `packager: deb` for the legacy files) and `node --check`
of the ESM file as `.mjs` and of the legacy file as `.cjs` (where
`import`/`export` would be syntax errors). The Go tests in
`internal/gswitch/focus` repeat these checks for both trees and run the
script.
