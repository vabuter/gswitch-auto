#!/usr/bin/env bash
# Development helper: install the gswitch-focus GNOME Shell extension for the
# current user from this checkout. The RPM installs the system copy under
# /usr/share/gnome-shell/extensions; a user copy installed here shadows it.
#
#   scripts/install-extension.sh             pack and install a copy (gnome-extensions)
#   scripts/install-extension.sh --link      symlink the checkout (edit in place)
#   scripts/install-extension.sh --uninstall remove the user copy
#
# GNOME Shell on Wayland loads new or changed extension code only at login, so
# every install or code change needs a logout/login.
set -euo pipefail

UUID="gswitch-focus@vabuter.github.io"
BUS_NAME="org.gnome.Shell.Extensions.GswitchFocus"
OBJECT_PATH="/org/gnome/Shell/Extensions/GswitchFocus"
SCHEMA="org.gnome.shell"
SYSTEM_DIR="/usr/share/gnome-shell/extensions/$UUID"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC_DIR="$REPO_ROOT/shell-extension/$UUID"

DATA_HOME="${XDG_DATA_HOME:-}"
case "$DATA_HOME" in
/*) ;;
*) DATA_HOME="$HOME/.local/share" ;; # unset or relative: XDG default
esac
DEST_DIR="$DATA_HOME/gnome-shell/extensions/$UUID"

usage() {
    sed -n '2,12s/^# \{0,1\}//p' "${BASH_SOURCE[0]}"
}

have() {
    command -v "$1" >/dev/null 2>&1
}

# remove_item LIST: prints the GVariant string list LIST without the UUID.
remove_item() {
    local list="$1" item="'$UUID'"
    list="${list//", $item"/}"
    list="${list//"$item, "/}"
    list="${list//"$item"/}"
    if [ "$list" = "[]" ]; then
        list="@as []"
    fi
    printf '%s' "$list"
}

# append_item LIST: prints LIST with the UUID appended (LIST must not contain it).
append_item() {
    local list="$1"
    case "$list" in
    "@as []" | "[]") printf "['%s']" "$UUID" ;;
    *) printf "%s, '%s']" "${list%]}" "$UUID" ;;
    esac
}

pre_enable() {
    if have gnome-extensions && gnome-extensions enable "$UUID" >/dev/null 2>&1; then
        echo "Enabled $UUID in the running Shell."
        return 0
    fi
    # The running Shell does not know an extension installed after login:
    # list it in enabled-extensions so that it starts at the next login.
    if ! have gsettings; then
        echo "gsettings not found; enable $UUID after the next login." >&2
        return 0
    fi
    local enabled disabled
    enabled="$(gsettings get "$SCHEMA" enabled-extensions)"
    case "$enabled" in
    *"'$UUID'"*) ;;
    *) gsettings set "$SCHEMA" enabled-extensions "$(append_item "$enabled")" ;;
    esac
    # disabled-extensions takes precedence over enabled-extensions.
    disabled="$(gsettings get "$SCHEMA" disabled-extensions)"
    case "$disabled" in
    *"'$UUID'"*) gsettings set "$SCHEMA" disabled-extensions "$(remove_item "$disabled")" ;;
    esac
    echo "Pre-enabled $UUID in $SCHEMA enabled-extensions."
}

remove_user_copy() {
    # Only ever touch our own extension directory.
    case "$DEST_DIR" in
    */gnome-shell/extensions/"$UUID") ;;
    *)
        echo "refusing to remove unexpected path $DEST_DIR" >&2
        exit 1
        ;;
    esac
    if [ -L "$DEST_DIR" ]; then
        rm -f "$DEST_DIR"
    elif [ -e "$DEST_DIR" ]; then
        rm -rf "$DEST_DIR"
    fi
}

install_copy() {
    remove_user_copy # never write through a --link symlink into the checkout
    if have gnome-extensions; then
        PACK_DIR="$(mktemp -d)"
        trap 'rm -rf "$PACK_DIR"' EXIT
        gnome-extensions pack --force --out-dir "$PACK_DIR" "$SRC_DIR"
        gnome-extensions install --force "$PACK_DIR/$UUID.shell-extension.zip"
    else
        mkdir -p "$DEST_DIR"
        cp "$SRC_DIR/metadata.json" "$SRC_DIR/extension.js" "$DEST_DIR/"
    fi
    echo "Installed $UUID to $DEST_DIR."
}

install_link() {
    remove_user_copy
    mkdir -p "$(dirname "$DEST_DIR")"
    ln -s "$SRC_DIR" "$DEST_DIR"
    echo "Linked $DEST_DIR -> $SRC_DIR."
}

uninstall() {
    if have gnome-extensions; then
        gnome-extensions disable "$UUID" >/dev/null 2>&1 || true
    fi
    remove_user_copy
    echo "Removed the user copy of $UUID."
    if [ -e "$SYSTEM_DIR/metadata.json" ]; then
        echo "The system copy in $SYSTEM_DIR stays installed; re-enable it with: gnome-extensions enable $UUID"
    elif have gsettings; then
        local enabled
        enabled="$(gsettings get "$SCHEMA" enabled-extensions)"
        case "$enabled" in
        *"'$UUID'"*) gsettings set "$SCHEMA" enabled-extensions "$(remove_item "$enabled")" ;;
        esac
    fi
}

print_notice() {
    cat <<EOF

Log out and back in: GNOME Shell on Wayland loads new or changed extension
code only at login. Then verify:

  gnome-extensions info $UUID
  gdbus call --session --dest $BUS_NAME \\
    --object-path $OBJECT_PATH \\
    --method $BUS_NAME.GetFocus
  gdbus introspect --session --dest $BUS_NAME --object-path $OBJECT_PATH
EOF
}

mode="install"
while [ $# -gt 0 ]; do
    case "$1" in
    --link) mode="link" ;;
    --uninstall) mode="uninstall" ;;
    -h | --help)
        usage
        exit 0
        ;;
    *)
        echo "unknown option: $1" >&2
        usage >&2
        exit 2
        ;;
    esac
    shift
done

if [ "$mode" != "uninstall" ] && [ ! -f "$SRC_DIR/metadata.json" ]; then
    echo "extension sources not found in $SRC_DIR" >&2
    exit 1
fi

case "$mode" in
install)
    install_copy
    pre_enable
    print_notice
    ;;
link)
    install_link
    pre_enable
    print_notice
    ;;
uninstall)
    uninstall
    echo "Log out and back in to unload the extension from GNOME Shell."
    ;;
esac
