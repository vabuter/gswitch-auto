#!/bin/sh
# gswitch-auto pretrans script
#
# Executed before the RPM transaction (%pretrans; there is no DEB equivalent).
#
# gswitch-auto replaces the upstream gswitch package through Obsoletes. rpm
# then erases the old package with $1=0, so upstream's preremove.sh disables
# and stops gswitch.service in every active user manager, although the same
# unit file stays installed by gswitch-auto. Record the users whose unit is
# enabled now; posttrans.sh enables it again for them after the transaction.
#
# Marker: /run/gswitch-auto/enabled.<uid>, containing the user name.
#
# Every command is guarded with `command -v`: the script must exit 0 even in an
# empty environment (env -i PATH=/nonexistent /bin/sh scripts/pretrans.sh).

marker_dir=/run/gswitch-auto

for cmd in loginctl runuser systemctl mkdir rm; do
    command -v "$cmd" >/dev/null 2>&1 || exit 0
done

mkdir -p -m 0700 "$marker_dir" 2>/dev/null || exit 0
# Drop markers left behind by a transaction that failed after its pretrans.
rm -f "$marker_dir"/enabled.* 2>/dev/null || true

loginctl list-users --no-legend 2>/dev/null | while read -r uid user _; do
    case "$uid" in
        '' | *[!0-9]*) continue ;;
    esac
    [ -n "$user" ] || continue
    runtime_dir="/run/user/$uid"
    [ -S "$runtime_dir/bus" ] || continue
    if runuser -u "$user" -- env \
        XDG_RUNTIME_DIR="$runtime_dir" \
        DBUS_SESSION_BUS_ADDRESS="unix:path=$runtime_dir/bus" \
        systemctl --user is-enabled --quiet gswitch.service 2>/dev/null; then
        printf '%s\n' "$user" >"$marker_dir/enabled.$uid" 2>/dev/null || true
    fi
done

exit 0
