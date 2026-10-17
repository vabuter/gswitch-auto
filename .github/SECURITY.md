# Security Policy

This is the security policy of **gswitch-auto**, a fork of
[gswitch](https://github.com/arumata/gswitch) that adds automatic layout
correction. Like upstream, it reads raw keyboard input from `/dev/input/*` and
can inject input via `/dev/uinput`. The daemon runs as the active
graphical-session user, not as root, but those device permissions are
security-sensitive. Security reports are taken seriously.

## Supported Versions

Only the latest gswitch-auto release (`v<upstream>+auto.<N>`) receives
security fixes.

## Reporting a Vulnerability

Please report vulnerabilities in gswitch-auto privately via
[GitHub Security Advisories](https://github.com/vabuter/gswitch-auto/security/advisories/new)
("Report a vulnerability") on the fork. Do not open a public issue for
security problems. If the problem also exists in upstream gswitch (manual mode,
no fork code involved), please report it to upstream as well through
[its security policy](https://github.com/arumata/gswitch/security/policy).

You can expect an initial response within a few days. Once a fix is released,
the advisory will be published with credit to the reporter (unless you prefer
to stay anonymous).

## Security Model

- **No network access.** The codebase contains no network code; nothing is
  ever transmitted anywhere. The fork's language detector runs locally on
  embedded models; there is no LLM and no model download.
- **Local IPC only.** The fork adds two AF_UNIX connections: the user's D-Bus
  session bus (the daemon's `io.github.vabuter.GswitchAuto` interface and the
  GNOME Shell extension's `org.gnome.Shell.Extensions.GswitchFocus`) and the
  IBus private bus (read-only: the active input engine). The daemon never
  addresses the activatable `org.freedesktop.IBus` name on the session bus.
  The systemd unit's `RestrictAddressFamilies=AF_UNIX AF_NETLINK` is
  unchanged.
- **Upstream's sandbox, unchanged.** The packaged systemd user unit is
  byte-identical to upstream's (`ProtectSystem=strict`,
  `ProtectHome=read-only`, `ReadWritePaths=/tmp`, `NoNewPrivileges`, …). The
  daemon writes nothing under `$HOME`; it only reads
  `~/.config/gswitch/`.
- **In-memory buffer only.** Buffered keystrokes live only in process memory
  and the buffer is cleared whenever focus can change (mouse clicks, Tab,
  arrows, Enter, …). Debug mode writes operational metadata to the foreground
  terminal only; it does not log key names, buffer contents, or selected or
  converted text. The fork logs corrected words only under `--debug` with
  `GSWITCH_AUTO_LOG_WORDS=1`.
- **Exception words on disk.** The only words the fork stores are exception
  words: entries the user adds and words learned when an automatic correction
  is undone. The tray (not the daemon) writes them to
  `~/.config/gswitch/exceptions-words.txt` (file 0600, directory 0700).
- **Words on the session bus.** Corrected and learned words are visible to
  other processes of the same user through the daemon's D-Bus interface
  (`Corrected`, `LastCorrection`, `CorrectionUndone`, `PendingLearnedWords`)
  only while `ipc-expose-words=true` in `~/.config/gswitch/auto.conf`; set it
  to `false` to blank them (words learned on undo then stay in daemon memory
  only and are not saved). Words corrected or learned while it is `false`
  stay hidden after it is set back to `true`.
- **GNOME Shell extension.** The bundled extension publishes only the focused
  application's desktop id, WM_CLASS and text-field purpose; no window titles,
  text or process ids. Password-field detection is best effort and is not a
  security boundary.
- **No root daemon.** The packaged daemon is a systemd user service. udev and
  systemd-logind grant the active local session ACLs for keyboard event nodes
  and `/dev/uinput`; permanent membership in the global `input` group is not
  required. The kernel interfaces require permission on their device nodes,
  not UID 0.
- **Sensitive device authority.** Access to raw keyboard events is equivalent
  to keylogger capability within the accessible session, and access to uinput
  permits synthetic input. Running as an ordinary user limits the impact of a
  daemon compromise but does not make these interfaces safe for untrusted
  code. ACL removal does not revoke file descriptors that are already open;
  logging out stops the user service, but concurrent-session/fast-user-switch
  setups require particular care. Device ACLs are granted to a user ID, so
  every unsandboxed process running as that user can exercise the same device
  authority while the ACL is active.
- **What root is still used for.** Package installation installs binaries,
  the systemd user unit, udev rules and the GNOME Shell extension. The tray's
  only privileged runtime entry point is the polkit-gated `--write-config`,
  which writes the fixed system configuration file `/etc/gswitch/default.conf`.
  Service control uses `systemctl --user` and does not cross a privilege
  boundary. The fork's per-user settings need no privileges.
- **Root compatibility mode.** A manual root foreground launch remains
  supported for troubleshooting. Session-specific helpers are then spawned
  with the graphical user's UID, GID, supplementary groups, and environment;
  this is not the packaged or recommended operating mode. Automatic mode and
  the D-Bus interface are disabled in this mode.
- **Reproducible artifacts.** Fork releases are built by CI with nfpm
  (`scripts/build-rpm.sh`) directly from a git tag; every release ships a
  `checksums.txt`.
- **Auditable.** The full source of the released code is in this repository.
