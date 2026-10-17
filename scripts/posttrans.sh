#!/bin/sh
# gswitch-auto posttrans script
#
# Executed after the RPM transaction (%posttrans; there is no DEB equivalent).
#
# pretrans.sh recorded the users whose gswitch.service was enabled before the
# transaction. When gswitch-auto replaced the upstream gswitch package, the
# erase of the old package disabled and stopped the unit (upstream
# preremove.sh with $1=0). Enable and start it again for those users, but only
# where it is no longer enabled, so plain gswitch-auto upgrades (where the
# unit stays enabled and postinstall.sh already restarted it) are untouched.
# Then remove the markers and print the GNOME Shell extension hint.
#
# Every command is guarded with `command -v`; the script always exits 0.

marker_dir=/run/gswitch-auto
extension_uuid=gswitch-focus@vabuter.github.io

user_systemctl() {
    runuser -u "$user" -- env \
        XDG_RUNTIME_DIR="$runtime_dir" \
        DBUS_SESSION_BUS_ADDRESS="unix:path=$runtime_dir/bus" \
        systemctl --user "$@" 2>/dev/null
}

restore_units() {
    for cmd in runuser systemctl; do
        command -v "$cmd" >/dev/null 2>&1 || return 0
    done

    for marker in "$marker_dir"/enabled.*; do
        [ -f "$marker" ] || continue
        uid=${marker##*/enabled.}
        case "$uid" in
            '' | *[!0-9]*) continue ;;
        esac
        user=""
        read -r user 2>/dev/null <"$marker" || true
        [ -n "$user" ] || continue
        runtime_dir="/run/user/$uid"
        [ -S "$runtime_dir/bus" ] || continue

        user_systemctl daemon-reload || true
        if ! user_systemctl is-enabled --quiet gswitch.service; then
            if user_systemctl enable --now gswitch.service; then
                echo "gswitch-auto: re-enabled gswitch.service for $user"
            else
                echo "gswitch-auto: could not re-enable gswitch.service for $user;"
                echo "  run: systemctl --user enable --now gswitch.service"
            fi
        fi
    done
}

cleanup_markers() {
    if command -v rm >/dev/null 2>&1; then
        rm -f "$marker_dir"/enabled.* 2>/dev/null || true
    fi
    if command -v rmdir >/dev/null 2>&1; then
        rmdir "$marker_dir" 2>/dev/null || true
    fi
}

restore_units
cleanup_markers

echo ""
echo "gswitch-auto: automatic correction is off by default; toggle it with"
echo "LShift+RShift or from the tray menu."
echo "Application exclusions need the GNOME Shell extension"
echo "$extension_uuid: log out and back in, then enable it in"
echo "the tray settings (Auto correction) or with:"
echo "  gnome-extensions enable $extension_uuid"
echo ""

exit 0
