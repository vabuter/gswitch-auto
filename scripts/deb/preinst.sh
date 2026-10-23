#!/bin/sh
# gswitch-auto preinst script (DEB only; the RPM uses pretrans.sh/posttrans.sh).
#
# dpkg runs it as "preinst install [old-version]" or "preinst upgrade
# <old-version>". When gswitch-auto replaces the upstream gswitch package
# (Conflicts + Replaces + Provides), dpkg has already run the upstream
# package's "prerm remove in-favour gswitch-auto <version>" before this
# script, so upstream preremove.sh has disabled and stopped gswitch.service
# in every active user manager; nothing on deb runs earlier (there is no
# %pretrans equivalent). Record every logged-in user with a session bus;
# postinst.sh enables the unit again for them after the installation.
#
# Marker: /run/gswitch-auto/replacing.<uid>, containing the user name.
#
# Known limitation (docs/FORK.md): the replaced package's enablement state
# can no longer be read at this point, so a user who had upstream gswitch
# installed with the unit deliberately disabled gets it enabled by the
# replacement. Plain upgrades of gswitch-auto ($1 = upgrade) never touch it.
#
# Every command is guarded with `command -v`: the script must exit 0 even in
# an empty environment (env -i PATH=/nonexistent /bin/sh scripts/deb/preinst.sh).
# The section between the "upstream scripts/preinstall.sh" markers is a
# verbatim copy of scripts/preinstall.sh without its final "exit 0";
# scripts/build-deb.sh fails when the two drift apart.

marker_dir=/run/gswitch-auto

record_replacing() {
    [ "${1:-}" = install ] || return 0
    for cmd in dpkg-query loginctl mkdir rm; do
        command -v "$cmd" >/dev/null 2>&1 || return 0
    done
    # The upstream package takes part in this run when dpkg still tracks
    # its files: "half-configured" right after its prerm (dpkg's state during
    # a conflict-driven replacement), "installed" or "half-installed"
    # otherwise. "not-installed", "config-files" or an unknown package mean
    # that no replacement is happening.
    status=$(dpkg-query -W -f="\${db:Status-Status}" gswitch 2>/dev/null) || return 0
    case "$status" in
        '' | not-installed | config-files) return 0 ;;
    esac
    mkdir -p -m 0700 "$marker_dir" 2>/dev/null || return 0
    # Drop markers left behind by an installation that failed after its preinst.
    rm -f "$marker_dir"/replacing.* 2>/dev/null || true
    loginctl list-users --no-legend 2>/dev/null | while read -r uid user _; do
        case "$uid" in
            '' | *[!0-9]*) continue ;;
        esac
        [ -n "$user" ] || continue
        [ -S "/run/user/$uid/bus" ] || continue
        printf '%s\n' "$user" >"$marker_dir/replacing.$uid" 2>/dev/null || true
    done
}

record_replacing "$@"

# --- upstream scripts/preinstall.sh begin ---
#!/bin/sh
# Stop the legacy system-scope daemon before an upgrade removes its unit file.
# DEB and RPM both run this hook before unpacking the new package.

if command -v systemctl >/dev/null 2>&1; then
    systemctl stop gswitch.service 2>/dev/null || true
    systemctl disable gswitch.service 2>/dev/null || true
fi

# --- upstream scripts/preinstall.sh end ---

exit 0
