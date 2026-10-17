#!/usr/bin/env bash
# Consistency checks for the bundled GNOME Shell extension (SPEC §3.5.1, §3.13):
#
#   - exactly one extension directory under shell-extension/
#   - metadata.json parses (python3 -m json.tool)
#   - metadata uuid == directory name == focus.ExtensionUUID in
#     internal/gswitch/focus/state.go == the nfpm destination directory
#   - shell-version contains "50"
#   - every .js file passes `node --check` (checked as an ES module)
#
# Usage: scripts/check-extension.sh [--strict]
#   --strict   fail instead of skipping the JavaScript check when node is absent
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
readonly repo_root
readonly ext_root=shell-extension
readonly state_go=internal/gswitch/focus/state.go
readonly nfpm_yaml=packaging/nfpm.yaml
readonly required_shell_version=50

strict=false
case "${1:-}" in
    '') ;;
    --strict) strict=true ;;
    -h | --help)
        sed -n '2,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//'
        exit 0
        ;;
    *)
        printf 'usage: %s [--strict]\n' "$0" >&2
        exit 2
        ;;
esac

cd "$repo_root"

errors=0
fail() {
    printf 'check-extension: %s\n' "$*" >&2
    errors=$((errors + 1))
}
ok() {
    printf 'ok    %s\n' "$*"
}

command -v python3 >/dev/null 2>&1 || {
    printf 'check-extension: python3 is required\n' >&2
    exit 1
}

if [[ ! -d $ext_root ]]; then
    printf 'check-extension: %s/ not found: the GNOME Shell extension is not in this tree yet\n' "$ext_root" >&2
    exit 1
fi

shopt -s nullglob
dirs=("$ext_root"/*/)
shopt -u nullglob
if ((${#dirs[@]} != 1)); then
    printf 'check-extension: expected exactly one extension directory under %s/, found %d\n' \
        "$ext_root" "${#dirs[@]}" >&2
    exit 1
fi
ext_dir=${dirs[0]%/}
dir_name=${ext_dir##*/}
meta="$ext_dir/metadata.json"

# --- metadata.json ---------------------------------------------------------
uuid=""
shell_versions=""
if [[ ! -f $meta ]]; then
    fail "$meta not found"
elif ! python3 -m json.tool "$meta" >/dev/null; then
    fail "$meta is not valid JSON"
else
    ok "$meta parses"
    fields=$(python3 - "$meta" <<'EOF'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as f:
    meta = json.load(f)
uuid = meta.get("uuid")
versions = meta.get("shell-version")
if not isinstance(uuid, str) or not uuid:
    sys.exit("metadata.json: uuid must be a non-empty string")
if not isinstance(versions, list) or not all(isinstance(v, str) for v in versions):
    sys.exit("metadata.json: shell-version must be a list of strings")
print(uuid)
print(" ".join(versions))
EOF
    ) || fields=""
    if [[ -z $fields ]]; then
        fail "$meta: missing or malformed uuid / shell-version"
    else
        uuid=$(sed -n 1p <<<"$fields")
        shell_versions=$(sed -n 2p <<<"$fields")
    fi
fi

if [[ -n $uuid ]]; then
    if [[ $uuid == "$dir_name" ]]; then
        ok "uuid $uuid == directory name"
    else
        fail "uuid '$uuid' != directory name '$dir_name'"
    fi

    if [[ " $shell_versions " == *" $required_shell_version "* ]]; then
        ok "shell-version [$shell_versions] contains \"$required_shell_version\""
    else
        fail "shell-version [$shell_versions] does not contain \"$required_shell_version\""
    fi
fi

# --- focus.ExtensionUUID ---------------------------------------------------
if [[ ! -f $state_go ]]; then
    fail "$state_go not found (focus.ExtensionUUID is defined there)"
else
    go_uuid=$(sed -n 's/^[[:space:]]*ExtensionUUID[[:space:]]*=[[:space:]]*"\([^"]*\)".*$/\1/p' "$state_go" | head -n 1)
    if [[ -z $go_uuid ]]; then
        fail "ExtensionUUID constant not found in $state_go"
    elif [[ $go_uuid != "$dir_name" ]]; then
        fail "focus.ExtensionUUID '$go_uuid' != directory name '$dir_name'"
    else
        ok "focus.ExtensionUUID == directory name"
    fi
fi

# --- packaging -------------------------------------------------------------
if [[ ! -f $nfpm_yaml ]]; then
    fail "$nfpm_yaml not found"
else
    name_re=${dir_name//./\\.}
    if grep -Eq "^[[:space:]]*-?[[:space:]]*src:[[:space:]]*$ext_root/$name_re/?[[:space:]]*$" "$nfpm_yaml" &&
        grep -Eq "^[[:space:]]*dst:[[:space:]]*/usr/share/gnome-shell/extensions/$name_re/?[[:space:]]*$" "$nfpm_yaml"; then
        ok "$nfpm_yaml installs $ext_root/$dir_name to /usr/share/gnome-shell/extensions/$dir_name"
    else
        fail "$nfpm_yaml does not map $ext_root/$dir_name to /usr/share/gnome-shell/extensions/$dir_name"
    fi
fi

# --- JavaScript syntax -----------------------------------------------------
if [[ ! -f $ext_dir/extension.js ]]; then
    fail "$ext_dir/extension.js not found"
fi
if command -v node >/dev/null 2>&1; then
    # GNOME Shell extensions are ES modules. `node --check file.js` treats an
    # ambiguous .js file as CommonJS-or-detected-ESM and exits 0 on some ESM
    # syntax errors (node 22), so check a .mjs copy instead.
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    while IFS= read -r -d '' js; do
        copy="$tmp/$(printf '%s' "${js#"$ext_dir"/}" | tr '/' '_').mjs"
        cp "$js" "$copy"
        if node --check "$copy"; then
            ok "node --check $js"
        else
            fail "node --check $js failed"
        fi
    done < <(find "$ext_dir" -type f -name '*.js' -print0)
elif [[ $strict == true ]]; then
    fail "node not found (required with --strict)"
else
    printf 'skip  JavaScript syntax check: node not found\n'
fi

if ((errors > 0)); then
    printf 'check-extension: %d problem(s) found\n' "$errors" >&2
    exit 1
fi
printf 'extension %s: all checks passed\n' "$dir_name"
