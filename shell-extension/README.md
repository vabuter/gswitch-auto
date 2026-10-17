# gswitch Focus Reporter (GNOME Shell extension)

`gswitch-focus@vabuter.github.io` tells the gswitch-auto daemon which
application has the keyboard focus and what kind of text field is focused, so
that automatic layout correction can stay out of terminals, password fields,
the lock screen and applications the user excluded. GNOME Shell's own
`org.gnome.Shell.Introspect` is restricted to portal backends
(`js/misc/introspect.js`), so on Wayland an extension is the only practical
source of this information.

Supported: GNOME Shell 50 (`metadata.json` pins `shell-version`; bump it for
each new GNOME release after testing).

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

## Installing

The `gswitch-auto` RPM installs the extension to
`/usr/share/gnome-shell/extensions/gswitch-focus@vabuter.github.io`. The tray's
settings window (Auto correction → Excluded applications) shows its state and
enables it.

GNOME Shell scans extension directories only at login and caches extension
modules, so after the first install and after every code upgrade **log out and
back in**. `EnableExtension` fails for an extension the running Shell has not
seen; the tray (and the script below) then add the UUID to
`org.gnome.shell enabled-extensions` so that it starts at the next login.

For development, from the repository root:

```bash
scripts/install-extension.sh             # pack + gnome-extensions install --force (user copy)
scripts/install-extension.sh --link      # symlink this checkout instead
scripts/install-extension.sh --uninstall # remove the user copy
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
gdbus monitor --session --dest org.gnome.Shell.Extensions.GswitchFocus
journalctl --user -b -g gswitch-focus    # extension warnings, if any
```

`node --check extension.js` and `python3 -m json.tool metadata.json` are the
offline syntax checks; the Go tests in `internal/gswitch/focus` check that the
directory name, `metadata.json` and the D-Bus names match the daemon.
