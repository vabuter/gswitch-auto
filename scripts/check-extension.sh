#!/usr/bin/env bash
# Consistency checks for the bundled GNOME Shell extension trees (SPEC §3.5.1,
# §3.13; SPEC-X11 §3.6):
#
#   shell-extension/<uuid>/          ESM variant, GNOME Shell 50 (shipped by the RPM)
#   shell-extension/legacy/<uuid>/   legacy variant, GNOME Shell 43/44 (shipped by the .deb)
#
#   - exactly these two extension directories, both named like
#     focus.ExtensionUUID in internal/gswitch/focus/state.go
#   - per tree: metadata.json parses, uuid == directory name, shell-version
#     contains "50" (ESM) / "43" and "44" (legacy), no session-modes or
#     settings-schema; name, description, url and version equal in both trees
#   - the ESM extension.js contains `export default class` and no `imports.`;
#     the legacy extension.js contains `function init(` and no line starting
#     with `import ` or `export `
#   - the shared helpers hintMask, securePurpose and purposeTable, the
#     IFACE_XML template literal and the purpose/hint tables are byte-identical
#     in both trees
#   - nfpm.yaml installs the ESM files with `packager: rpm` and the legacy
#     files with `packager: deb` (mode 0644), creates the destination
#     directory once for both packagers (type dir, mode 0755) and never lists
#     it as a tree. While nfpm.yaml has no legacy entries yet, the packager
#     checks are skipped with a notice (not a failure, also under --strict).
#   - `node --check` of every ESM .js file as .mjs and of every legacy .js
#     file as .cjs (CommonJS rejects import/export statements)
#
# Usage: scripts/check-extension.sh [--strict]
#   --strict   fail instead of skipping the JavaScript check when node is absent
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
readonly repo_root
readonly ext_root=shell-extension
readonly legacy_root=shell-extension/legacy
readonly state_go=internal/gswitch/focus/state.go
readonly nfpm_yaml=packaging/nfpm.yaml
readonly esm_shell_versions="50"
readonly legacy_shell_versions="43 44"

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
skip() {
    printf 'skip  %s\n' "$*"
}

command -v python3 >/dev/null 2>&1 || {
    printf 'check-extension: python3 is required\n' >&2
    exit 1
}

# --- focus.ExtensionUUID ---------------------------------------------------
if [[ ! -f $state_go ]]; then
    printf 'check-extension: %s not found (focus.ExtensionUUID is defined there)\n' "$state_go" >&2
    exit 1
fi
go_uuid=$(sed -n 's/^[[:space:]]*ExtensionUUID[[:space:]]*=[[:space:]]*"\([^"]*\)".*$/\1/p' "$state_go" | head -n 1)
if [[ -z $go_uuid ]]; then
    printf 'check-extension: ExtensionUUID constant not found in %s\n' "$state_go" >&2
    exit 1
fi
readonly go_uuid

# --- directory layout ------------------------------------------------------
if [[ ! -d $ext_root ]]; then
    printf 'check-extension: %s/ not found: the GNOME Shell extension is not in this tree yet\n' "$ext_root" >&2
    exit 1
fi

# list_dirs PARENT: the names of the directories directly under PARENT.
list_dirs() {
    local d
    shopt -s nullglob
    for d in "$1"/*/; do
        d=${d%/}
        printf '%s\n' "${d##*/}"
    done
    shopt -u nullglob
}

esm_dirs=$(list_dirs "$ext_root" | grep -vx -- "${legacy_root##*/}" || true)
legacy_dirs=$(list_dirs "$legacy_root")
if [[ $esm_dirs != "$go_uuid" ]]; then
    printf 'check-extension: expected exactly one ESM extension directory %s/%s/ (plus %s/), found: %s\n' \
        "$ext_root" "$go_uuid" "$legacy_root" "${esm_dirs:-none}" >&2
    exit 1
fi
if [[ $legacy_dirs != "$go_uuid" ]]; then
    printf 'check-extension: expected exactly one legacy extension directory %s/%s/, found: %s\n' \
        "$legacy_root" "$go_uuid" "${legacy_dirs:-none}" >&2
    exit 1
fi
ok "extension directories: $ext_root/$go_uuid (ESM), $legacy_root/$go_uuid (legacy) == focus.ExtensionUUID"
readonly esm_dir=$ext_root/$go_uuid
readonly legacy_dir=$legacy_root/$go_uuid
readonly esm_js=$esm_dir/extension.js
readonly legacy_js=$legacy_dir/extension.js

# --- metadata.json ---------------------------------------------------------
# check_metadata KIND DIR "V1 V2 ...": checks DIR/metadata.json and sets
# meta_shared to "name|description|url|version" (empty on failure) for the
# cross-tree comparison.
meta_shared=""
check_metadata() {
    local kind=$1 dir=$2 required=$3
    local meta=$dir/metadata.json dir_name=${dir##*/}
    local fields uuid versions extra v
    meta_shared=""
    if [[ ! -f $meta ]]; then
        fail "$meta not found"
        return
    fi
    if ! python3 -m json.tool "$meta" >/dev/null; then
        fail "$meta is not valid JSON"
        return
    fi
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
if (not isinstance(versions, list) or not versions
        or not all(isinstance(v, str) for v in versions)):
    sys.exit("metadata.json: shell-version must be a non-empty list of strings")
for key in ("name", "description", "url"):
    if not isinstance(meta.get(key), str) or not meta[key]:
        sys.exit(f"metadata.json: {key} must be a non-empty string")
if not isinstance(meta.get("version"), int):
    sys.exit("metadata.json: version must be an integer")
print(uuid)
print(" ".join(versions))
print(" ".join(k for k in ("session-modes", "settings-schema") if k in meta))
print("|".join([meta["name"], meta["description"], meta["url"], str(meta["version"])]))
EOF
    ) || fields=""
    if [[ -z $fields ]]; then
        fail "$meta: missing or malformed fields"
        return
    fi
    uuid=$(sed -n 1p <<<"$fields")
    versions=$(sed -n 2p <<<"$fields")
    extra=$(sed -n 3p <<<"$fields")
    if [[ $uuid == "$dir_name" ]]; then
        ok "$kind: uuid $uuid == directory name == focus.ExtensionUUID"
    else
        fail "$kind: uuid '$uuid' != directory name '$dir_name'"
    fi
    for v in $required; do
        if [[ " $versions " == *" $v "* ]]; then
            ok "$kind: shell-version [$versions] contains \"$v\""
        else
            fail "$kind: shell-version [$versions] does not contain \"$v\""
        fi
    done
    # Default session mode only: the Shell must disable the extension on lock
    # so that the daemon sees Disabling("locked"); no settings schema.
    if [[ -n $extra ]]; then
        fail "$kind: metadata declares $extra (must not)"
    else
        ok "$kind: no session-modes / settings-schema"
    fi
    meta_shared=$(sed -n 4p <<<"$fields")
}

check_metadata ESM "$esm_dir" "$esm_shell_versions"
esm_meta=$meta_shared
check_metadata legacy "$legacy_dir" "$legacy_shell_versions"
legacy_meta=$meta_shared
if [[ -n $esm_meta && -n $legacy_meta ]]; then
    if [[ $esm_meta == "$legacy_meta" ]]; then
        ok "metadata name, description, url and version equal in both trees"
    else
        fail "metadata name/description/url/version differ: ESM '$esm_meta' vs legacy '$legacy_meta'"
    fi
fi

# --- extension.js format ---------------------------------------------------
if [[ ! -f $esm_js ]]; then
    fail "$esm_js not found"
else
    if grep -q 'export default class' "$esm_js"; then
        ok "ESM: $esm_js has 'export default class'"
    else
        fail "ESM: $esm_js has no 'export default class'"
    fi
    if grep -q 'imports\.' "$esm_js"; then
        fail "ESM: $esm_js uses the legacy 'imports.' namespace"
    else
        ok "ESM: $esm_js has no 'imports.'"
    fi
fi
if [[ ! -f $legacy_js ]]; then
    fail "$legacy_js not found"
else
    if grep -q 'function init(' "$legacy_js"; then
        ok "legacy: $legacy_js has 'function init('"
    else
        fail "legacy: $legacy_js has no 'function init('"
    fi
    if grep -qE '^(import|export) ' "$legacy_js"; then
        fail "legacy: $legacy_js has import/export statements"
    else
        ok "legacy: $legacy_js has no import/export statements"
    fi
fi

# --- shared code -----------------------------------------------------------
if [[ -f $esm_js && -f $legacy_js ]]; then
    # One "ok<TAB>msg" or "fail<TAB>msg" line per shared item.
    shared_report=$(python3 - "$esm_js" "$legacy_js" <<'EOF'
import re
import sys

esm, legacy = sys.argv[1], sys.argv[2]
items = [
    ("function", "hintMask"),
    ("function", "securePurpose"),
    ("function", "purposeTable"),
    ("const", "IFACE_XML"),
    ("const", "IBUS_PURPOSE_NAMES"),
    ("const", "CLUTTER_PURPOSE_NAMES"),
    ("const", "IBUS_SECRET_HINTS"),
    ("const", "CLUTTER_SECRET_HINTS"),
]
texts = {}
for path in (esm, legacy):
    with open(path, encoding="utf-8") as f:
        texts[path] = f.read()


def extract(text, kind, name):
    if kind == "function":
        pattern = r"(?ms)^function " + re.escape(name) + r"\(.*?^}$"
    else:
        pattern = r"(?ms)^const " + re.escape(name) + r" = .*?;$"
    m = re.search(pattern, text)
    return m.group(0) if m else None


for kind, name in items:
    a = extract(texts[esm], kind, name)
    b = extract(texts[legacy], kind, name)
    if a is None or b is None:
        missing = [p for p, t in ((esm, a), (legacy, b)) if t is None]
        print(f"fail\ttop-level {kind} {name} not found in {' and '.join(missing)}")
    elif a != b:
        print(f"fail\ttop-level {kind} {name} differs between {esm} and {legacy}")
    else:
        print(f"ok\ttop-level {kind} {name} is byte-identical in both trees")
EOF
    ) || shared_report=""
    if [[ -z $shared_report ]]; then
        fail "shared-code comparison of $esm_js and $legacy_js produced no result"
    else
        while IFS=$'\t' read -r status msg; do
            case "$status" in
                ok) ok "$msg" ;;
                *) fail "$msg" ;;
            esac
        done <<<"$shared_report"
    fi
fi

# --- packaging -------------------------------------------------------------
if [[ ! -f $nfpm_yaml ]]; then
    fail "$nfpm_yaml not found"
else
    # One "src|dst|type|mode|packager" line per contents entry ("-" when absent).
    entries=$(python3 - "$nfpm_yaml" <<'EOF'
import re
import sys

entries = []
entry = None
with open(sys.argv[1], encoding="utf-8") as f:
    for line in f:
        m = re.match(r"^\s*(-\s+)?(src|dst|type|mode|packager):\s*(\S+)\s*$", line)
        if re.match(r"^\s*-\s", line):
            entry = {}
            entries.append(entry)
        if m is not None and entry is not None:
            entry[m.group(2)] = m.group(3)
for e in entries:
    if "dst" in e:
        print("|".join(e.get(k, "-") for k in ("src", "dst", "type", "mode", "packager")))
EOF
    ) || entries=""
    dst_dir=/usr/share/gnome-shell/extensions/$go_uuid

    # has_entry LINE: an entry equal to LINE.
    has_entry() {
        grep -Fxq -- "$1" <<<"$entries"
    }
    # has_entry_prefix PREFIX: an entry starting with PREFIX (no regex).
    has_entry_prefix() {
        awk -v p="$1" 'index($0, p) == 1 { found = 1 } END { exit !found }' <<<"$entries"
    }
    # check_packaged TREE PACKAGER: every file of TREE is installed under
    # dst_dir with mode 0644 and PACKAGER ("-" = any packager, Phase A).
    check_packaged() {
        local tree=$1 packager=$2 file rel line
        while IFS= read -r -d '' file; do
            rel=${file#"$tree"/}
            line="$tree/$rel|$dst_dir/$rel|-|0644|"
            if [[ $packager == - ]]; then
                if has_entry_prefix "$line"; then
                    ok "$nfpm_yaml installs $tree/$rel with mode 0644"
                else
                    fail "$nfpm_yaml does not install $tree/$rel to $dst_dir/$rel with mode 0644"
                fi
            elif has_entry "$line$packager"; then
                ok "$nfpm_yaml installs $tree/$rel with mode 0644 (packager: $packager)"
            else
                fail "$nfpm_yaml does not install $tree/$rel to $dst_dir/$rel with mode 0644 and packager: $packager"
            fi
        done < <(find "$tree" -type f ! -name '.*' -print0 | sort -z)
    }

    if has_entry "-|$dst_dir|dir|0755|-"; then
        ok "$nfpm_yaml creates $dst_dir with mode 0755 for both packagers"
    else
        fail "$nfpm_yaml does not list $dst_dir as a dir entry with mode 0755 and no packager"
    fi
    if grep -Fq -- "|$dst_dir|tree|" <<<"$entries"; then
        fail "$nfpm_yaml packages $dst_dir as a tree (checkout modes, stray files)"
    fi

    if has_entry_prefix "$legacy_dir/"; then
        check_packaged "$esm_dir" rpm
        check_packaged "$legacy_dir" deb
        # Nothing else from shell-extension/ may be packaged, and never with
        # the other packager.
        while IFS='|' read -r src dst _type _mode packager; do
            case "$src|$packager" in
                "$esm_dir"/*"|rpm" | "$legacy_dir"/*"|deb") ;;
                *) fail "$nfpm_yaml: unexpected extension entry $src -> $dst (packager: $packager)" ;;
            esac
        done < <(grep -- "^$ext_root/" <<<"$entries" || true)
    else
        # Phase A (SPEC-X11 §3.6): the deb packaging is not merged yet; the
        # ESM files must still be listed, with any packager.
        check_packaged "$esm_dir" -
        while IFS='|' read -r src dst _type _mode packager; do
            case "$src" in
                "$esm_dir"/*) ;;
                *) fail "$nfpm_yaml: unexpected extension entry $src -> $dst (packager: $packager)" ;;
            esac
        done < <(grep -- "^$ext_root/" <<<"$entries" || true)
        skip "packaging: legacy entries not in nfpm.yaml yet"
    fi
fi

# --- JavaScript syntax -----------------------------------------------------
if command -v node >/dev/null 2>&1; then
    # `node --check file.js` treats an ambiguous .js file as
    # CommonJS-or-detected-ESM and exits 0 on some ESM syntax errors (node 22),
    # so check copies with an explicit module type: the ESM tree as .mjs, the
    # legacy tree as .cjs (where import/export statements are syntax errors).
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    # node_check TREE EXT: `node --check` every .js file of TREE as a .EXT copy.
    node_check() {
        local tree=$1 ext=$2 js copy
        while IFS= read -r -d '' js; do
            copy="$tmp/$(printf '%s' "${js#"$ext_root"/}" | tr '/' '_').$ext"
            cp "$js" "$copy"
            if node --check "$copy"; then
                ok "node --check $js (as .$ext)"
            else
                fail "node --check $js (as .$ext) failed"
            fi
        done < <(find "$tree" -type f -name '*.js' -print0 | sort -z)
    }
    node_check "$esm_dir" mjs
    node_check "$legacy_dir" cjs
elif [[ $strict == true ]]; then
    fail "node not found (required with --strict)"
else
    skip "JavaScript syntax check: node not found"
fi

if ((errors > 0)); then
    printf 'check-extension: %d problem(s) found\n' "$errors" >&2
    exit 1
fi
printf 'extension %s: all checks passed (ESM and legacy trees)\n' "$go_uuid"
