#!/bin/sh
# gswitch-auto postinst script (DEB only; the RPM uses pretrans.sh/posttrans.sh).
#
# dpkg runs it as "postinst configure [most-recently-configured-version]".
# preinst.sh recorded the users who were logged in while gswitch-auto
# replaced the upstream gswitch package (removed in this dpkg run, or by apt
# in the run just before; either way its prerm disabled and stopped
# gswitch.service for them). Enable and start the unit again for those users,
# but only where it is not enabled, so plain gswitch-auto upgrades (no
# markers; upstream postinstall.sh below restarts a running unit) stay
# untouched. Then remove the markers, run the upstream postinstall.sh body
# and print the GNOME Shell extension hint.
#
# Every command is guarded with `command -v`; the script always exits 0
# (env -i PATH=/nonexistent /bin/sh scripts/deb/postinst.sh). The section
# between the "upstream scripts/postinstall.sh" markers is a verbatim copy
# of scripts/postinstall.sh without its final "exit 0"; scripts/build-deb.sh
# fails when the two drift apart.

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

    for marker in "$marker_dir"/replacing.*; do
        [ -f "$marker" ] || continue
        uid=${marker##*/replacing.}
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
                echo "gswitch-auto: enabled gswitch.service for $user (the removal of the gswitch package had disabled it)"
            else
                echo "gswitch-auto: could not enable gswitch.service for $user;"
                echo "  run: systemctl --user enable --now gswitch.service"
            fi
        fi
    done
}

cleanup_markers() {
    if command -v rm >/dev/null 2>&1; then
        rm -f "$marker_dir"/replacing.* 2>/dev/null || true
    fi
    if command -v rmdir >/dev/null 2>&1; then
        rmdir "$marker_dir" 2>/dev/null || true
    fi
}

restore_units
cleanup_markers

# --- upstream scripts/postinstall.sh begin ---
#!/bin/sh
# gswitch postinstall script
#
# This script is executed after the gswitch package is installed.
# It reloads systemd, udev, and updates icon/desktop caches.
#
# Compatible with: DEB (postinst), RPM (%post), nFPM (postinstall)
#
# Note: This script uses graceful degradation - commands may fail
# (e.g., on systems without systemd) without causing installation failure.

# Stop and disable the legacy system-scope root service during migration, then
# reload systemd so the removed unit is forgotten.
if command -v systemctl >/dev/null 2>&1; then
    # Keep stop and disable separate: `disable --now` can return early when an
    # older package already removed the unit file but systemd still has the
    # root service loaded and running.
    systemctl stop gswitch.service 2>/dev/null || true
    systemctl disable gswitch.service 2>/dev/null || true
    rm -f /etc/systemd/system/gswitch.service \
        /usr/lib/systemd/system/gswitch.service \
        /lib/systemd/system/gswitch.service
    systemctl daemon-reload || true
fi

# Reload udev rules for /dev/uinput access
if command -v udevadm >/dev/null 2>&1; then
    # Remove the old late-sorting rule left by pre-non-root packages or
    # manual installations. uaccess tags must exist before 73-seat-late.rules.
    rm -f /usr/lib/udev/rules.d/90-gswitch.rules \
        /etc/udev/rules.d/90-gswitch.rules
    udevadm control --reload-rules || true
    # Some distributions create /dev/uinput as a static node before the
    # kernel device exists. Loading the module emits the udev event needed to
    # apply the new uaccess rule immediately, without waiting for a reboot.
    if command -v modprobe >/dev/null 2>&1; then
        modprobe uinput || true
    fi
    udevadm trigger --subsystem-match=misc --action=change || true
    udevadm trigger --subsystem-match=input --action=change || true
fi

# Reload active user managers. Restart gswitch where it was already running,
# and recover a failed instance after package files or permissions changed.
# Do not enable or start an inactive service the user explicitly disabled.
if command -v loginctl >/dev/null 2>&1 && command -v runuser >/dev/null 2>&1; then
    loginctl list-users --no-legend 2>/dev/null | while read -r uid user _; do
        runtime_dir="/run/user/$uid"
        if [ -S "$runtime_dir/bus" ]; then
            runuser -u "$user" -- env \
                XDG_RUNTIME_DIR="$runtime_dir" \
                DBUS_SESSION_BUS_ADDRESS="unix:path=$runtime_dir/bus" \
                systemctl --user daemon-reload 2>/dev/null || true
            if runuser -u "$user" -- env \
                XDG_RUNTIME_DIR="$runtime_dir" \
                DBUS_SESSION_BUS_ADDRESS="unix:path=$runtime_dir/bus" \
                systemctl --user is-failed --quiet gswitch.service 2>/dev/null; then
                runuser -u "$user" -- env \
                    XDG_RUNTIME_DIR="$runtime_dir" \
                    DBUS_SESSION_BUS_ADDRESS="unix:path=$runtime_dir/bus" \
                    systemctl --user reset-failed gswitch.service 2>/dev/null || true
                runuser -u "$user" -- env \
                    XDG_RUNTIME_DIR="$runtime_dir" \
                    DBUS_SESSION_BUS_ADDRESS="unix:path=$runtime_dir/bus" \
                    systemctl --user start gswitch.service 2>/dev/null || true
            else
                runuser -u "$user" -- env \
                    XDG_RUNTIME_DIR="$runtime_dir" \
                    DBUS_SESSION_BUS_ADDRESS="unix:path=$runtime_dir/bus" \
                    systemctl --user try-restart gswitch.service 2>/dev/null || true
            fi
        fi
    done
fi

# Update GTK icon cache for tray application
if command -v gtk-update-icon-cache >/dev/null 2>&1; then
    gtk-update-icon-cache -f -t /usr/share/icons/hicolor 2>/dev/null || true
fi

# Update desktop database for tray application
if command -v update-desktop-database >/dev/null 2>&1; then
    update-desktop-database /usr/share/applications 2>/dev/null || true
fi

# Print post-installation message
echo ""
echo "gswitch installed successfully."
echo ""
echo "Enable the service once from the graphical user session:"
echo "  systemctl --user enable --now gswitch.service"
echo ""
echo "The tray application will start automatically on next login."
echo "To start it now, run: gswitch-tray"
echo ""

# --- upstream scripts/postinstall.sh end ---

echo "gswitch-auto: automatic correction is off by default; toggle it with"
echo "LShift+RShift or from the tray menu."
echo "Application exclusions need the GNOME Shell extension"
echo "$extension_uuid: log out and back in (or press Alt+F2, r on X11),"
echo "then enable it in the tray settings (Auto correction) or with:"
echo "  gnome-extensions enable $extension_uuid"
echo ""

exit 0
