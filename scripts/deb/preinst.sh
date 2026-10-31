#!/bin/sh
# gswitch-auto preinst script (DEB only; the RPM uses pretrans.sh/posttrans.sh).
#
# dpkg runs it as "preinst install [old-version]" or "preinst upgrade
# <old-version>". gswitch-auto replaces the upstream gswitch package
# (Conflicts + Replaces + Provides), and upstream's prerm has always run
# before this script, whichever front end installs us (SPEC-X11 F14):
#
#   dpkg -i ./gswitch-auto_*.deb   upstream gets "prerm remove in-favour
#                                  gswitch-auto <version>" in this run;
#                                  dpkg-query then reports it as
#                                  half-configured or half-installed
#   apt install ./gswitch-auto_*.deb
#                                  apt removes gswitch in a separate dpkg run
#                                  first (plain "prerm remove"); upstream is
#                                  left in state config-files
#
# Either way upstream preremove.sh has disabled and stopped gswitch.service
# in every active user manager, and nothing on deb runs earlier (there is no
# %pretrans equivalent). Record every logged-in user with a session bus;
# postinst.sh enables the unit again for them after the installation.
#
# Marker: /run/gswitch-auto/replacing.<uid>, containing the user name.
#
# Known limitation (docs/FORK.md, heuristic "upstream is or was installed"):
# the replaced package's enablement state can no longer be read here, so on
# the first install of gswitch-auto (also a reinstall after `apt remove
# gswitch-auto`) a logged-in user gets gswitch.service enabled and started
# when upstream gswitch is being replaced or was removed earlier without
# purge (its config-files remain), even if the unit had been disabled
# deliberately. `apt purge gswitch` beforehand, or `systemctl --user disable
# --now gswitch.service` afterwards, undoes it. Plain upgrades of
# gswitch-auto ($1 = upgrade) never touch it.
#
# Every command is guarded with `command -v`: the script must exit 0 even in
# an empty environment (env -i PATH=/nonexistent /bin/sh scripts/deb/preinst.sh).
# The section between the "upstream scripts/preinstall.sh" markers is a
# verbatim copy of scripts/preinstall.sh without its final "exit 0";
# scripts/build-deb.sh fails when the two drift apart.

marker_dir=/run/gswitch-auto

record_replacing() {
    # Markers are valid for one installation only: drop those left behind by
    # an installation that failed after its preinst.
    if command -v rm >/dev/null 2>&1; then
        rm -f "$marker_dir"/replacing.* 2>/dev/null || true
    fi
    [ "${1:-}" = install ] || return 0
    for cmd in dpkg-query loginctl mkdir; do
        command -v "$cmd" >/dev/null 2>&1 || return 0
    done
    # Upstream gswitch is or was installed when dpkg still knows it:
    # half-configured, half-installed or installed while `dpkg -i` replaces
    # it in this run, config-files after apt removed it just before (or after
    # an earlier `apt remove gswitch`). An empty status, "not-installed" or a
    # package dpkg never saw (dpkg-query fails) mean nothing to carry over.
    status=$(dpkg-query -W -f="\${db:Status-Status}" gswitch 2>/dev/null) || return 0
    case "$status" in
        '' | not-installed) return 0 ;;
    esac
    loginctl list-users --no-legend 2>/dev/null | while read -r uid user _; do
        case "$uid" in
            '' | *[!0-9]*) continue ;;
        esac
        [ -n "$user" ] || continue
        [ -S "/run/user/$uid/bus" ] || continue
        # Created only when there is a user to record; postinst.sh removes it.
        mkdir -p -m 0700 "$marker_dir" 2>/dev/null || continue
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
