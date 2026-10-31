#!/usr/bin/env bash
# Builds the gswitch-auto Debian package (SPEC-X11 §3.8.3; the RPM is built by
# scripts/build-rpm.sh).
#
# Usage: scripts/build-deb.sh [--version V] [--revision R] [--out DIR]
#                             [--skip-tests] [--no-container] [--image IMG]
#
#   --version V     package version (default: output of scripts/version.sh)
#   --revision R    Debian revision (default: 1)
#   --out DIR       output directory for the .deb and checksums-deb.txt
#                   (default: dist)
#   --skip-tests    do not run `go test ./...`
#   --no-container  build on this machine instead of inside the image: needs
#                   the Go toolchain of go.mod, gcc, GTK 3 and X11 headers,
#                   nfpm, dpkg-deb, dpkg, lintian and python3 (what the
#                   image provides); CI uses it inside the same image
#   --image IMG     image tag (default: localhost/gswitch-auto-deb:bookworm)
#
# Environment: PODMAN (container engine, default: podman), NFPM (nfpm
# executable; default: nfpm on PATH, then ~/go/bin/nfpm, then
# `go run github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.48.0`).
#
# Without --no-container the script builds IMG from
# packaging/debian12.Containerfile (Debian 12 userland, Go 1.26, nfpm,
# lintian) and re-runs itself inside it with --no-container. The checkout is
# mounted at /src, the container runs as the invoking user
# (--userns=keep-id; label=disable instead of :Z so the checkout is never
# relabelled on SELinux hosts), and the Go module and build caches live on
# the named volumes gswitch-auto-gomod and gswitch-auto-gocache. The module
# download needs network access (the tree has no vendor/).
#
# Steps inside: preflight (tools, every source path the deb packages, Debian
# version rules, extension consistency, scriptlet marker identity and
# empty-environment tolerance) -> go test ./... -> CGO build of
# builds/deb/gswitch and builds/deb/gswitch-tray with -X main.version=V
# (never over the host binaries in builds/) -> nfpm -> sha256
# checksums-deb.txt -> assertions on the package (dpkg-deb -I/-c/-e/-x) ->
# lintian (error tags are fatal; warnings and info tags are listed, never
# fatal).
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
readonly repo_root
readonly nfpm_yaml=packaging/nfpm.yaml
readonly containerfile=packaging/debian12.Containerfile
readonly nfpm_module=github.com/goreleaser/nfpm/v2/cmd/nfpm
readonly nfpm_version=v2.48.0
readonly extension_uuid=gswitch-focus@vabuter.github.io
readonly default_image=localhost/gswitch-auto-deb:bookworm

version=""
revision=1
out=dist
skip_tests=false
in_container=true
image=$default_image

die() {
    printf 'build-deb: %s\n' "$*" >&2
    exit 1
}

step() {
    printf '\n==> %s\n' "$*"
}

usage() {
    sed -n '2,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//'
}

need_arg() {
    [[ $# -ge 2 && -n $2 ]] || die "$1 needs a value"
}

while [[ $# -gt 0 ]]; do
    case $1 in
        --version)
            need_arg "$@"
            version=$2
            shift 2
            ;;
        --version=*)
            version=${1#*=}
            shift
            ;;
        --revision)
            need_arg "$@"
            revision=$2
            shift 2
            ;;
        --revision=*)
            revision=${1#*=}
            shift
            ;;
        --out)
            need_arg "$@"
            out=$2
            shift 2
            ;;
        --out=*)
            out=${1#*=}
            shift
            ;;
        --skip-tests)
            skip_tests=true
            shift
            ;;
        --no-container)
            in_container=false
            shift
            ;;
        --image)
            need_arg "$@"
            image=$2
            shift 2
            ;;
        --image=*)
            image=${1#*=}
            shift
            ;;
        -h | --help)
            usage
            exit 0
            ;;
        *)
            usage >&2
            die "unknown argument: $1"
            ;;
    esac
done

cd "$repo_root"

# packaged_sources PACKAGER: every src: path of packaging/nfpm.yaml that the
# given packager uses, one per line: contents entries without a packager or
# with this one, plus the maintainer scripts after the packager block and
# the overrides are applied (the same merge nfpm performs). Line based on
# purpose: python3-yaml is not available everywhere this runs.
packaged_sources() {
    python3 - "$nfpm_yaml" "$1" <<'EOF'
import re
import sys

path, packager = sys.argv[1], sys.argv[2]
key_re = re.compile(r"^\s*(?:-\s+)?([a-z_]+):\s*(\S*)\s*$")
script_re = re.compile(r"^(pre|post)(install|remove|trans)$")
section = None       # current top-level key
override = None      # packager block inside "overrides:"
entry = None         # current contents entry
entries = []
scripts = {}         # top-level "scripts:"
packager_scripts = {}  # "rpm:"/"deb:" blocks (pretrans, posttrans, ...)
override_scripts = {}  # "overrides: <packager>: scripts:"
with open(path, encoding="utf-8") as f:
    for line in f:
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        indent = len(line) - len(line.lstrip(" "))
        m = key_re.match(line)
        if indent == 0 and m:
            section, override, entry = m.group(1), None, None
            continue
        if section == "contents":
            if re.match(r"^\s*-\s", line):
                entry = {}
                entries.append(entry)
            if m and entry is not None:
                entry[m.group(1)] = m.group(2)
        elif section == "overrides":
            if indent == 2 and m and m.group(2) == "":
                override = m.group(1)
            elif m and script_re.match(m.group(1)) and override == packager:
                override_scripts[m.group(1)] = m.group(2)
        elif section == "scripts" and m and script_re.match(m.group(1)):
            scripts[m.group(1)] = m.group(2)
        elif section == packager and m and script_re.match(m.group(1)):
            packager_scripts[m.group(1)] = m.group(2)

paths = set()
for e in entries:
    if e.get("src") and e.get("packager", packager) == packager:
        paths.add(e["src"])
merged = dict(scripts)
merged.update(packager_scripts)
merged.update(override_scripts)
paths.update(v for v in merged.values() if v)
for p in sorted(paths):
    print(p)
EOF
}

# Every source the deb needs, except the binaries built below.
check_sources() {
    local -a sources missing=()
    local src
    mapfile -t sources < <(packaged_sources deb | grep -v '^builds/' | sort -u)
    ((${#sources[@]} > 0)) || die "no src entries for the deb packager found in $nfpm_yaml"
    for src in "${sources[@]}"; do
        [[ -e $src ]] || missing+=("$src")
    done
    if ((${#missing[@]} > 0)); then
        printf 'build-deb: missing packaged source(s) listed in %s:\n' "$nfpm_yaml" >&2
        printf '  %s\n' "${missing[@]}" >&2
        die "the tree is incomplete (shell-extension/legacy/ comes with the legacy extension work, SPEC-X11 §3.6); nothing was built"
    fi
    printf 'ok    all %d packaged source paths exist\n' "${#sources[@]}"
}

resolve_version() {
    [[ $revision =~ ^[1-9][0-9]*$ ]] || die "invalid revision '$revision' (expected a positive integer)"
    if [[ -z $version ]]; then
        version=$(scripts/version.sh) || die "cannot compute the version; pass --version V"
    fi
    # Validates the format (no "~", no "-"); exits non-zero with a message otherwise.
    version=$(GSWITCH_VERSION=$version scripts/version.sh)
    printf 'version %s, revision %s\n' "$version" "$revision"
}

# --- container mode ----------------------------------------------------------
if [[ $in_container == true ]]; then
    step "Preflight (host)"
    podman=${PODMAN:-podman}
    command -v "$podman" >/dev/null 2>&1 ||
        die "$podman not found (Fedora: dnf install podman); set PODMAN, or use --no-container on a machine with the Debian build tools"
    command -v python3 >/dev/null 2>&1 || die "python3 not found"
    [[ -f $nfpm_yaml ]] || die "$nfpm_yaml not found"
    [[ -f $containerfile ]] || die "$containerfile not found"
    resolve_version
    check_sources

    step "Container image $image"
    "$podman" build -t "$image" -f "$containerfile" packaging/

    mkdir -p "$out"
    out_abs=$(cd "$out" && pwd)
    mount_args=()
    case $out_abs in
        "$repo_root") inner_out=. ;;
        "$repo_root"/*) inner_out=${out_abs#"$repo_root"/} ;;
        *)
            inner_out=/out
            mount_args=(-v "$out_abs:/out")
            ;;
    esac
    inner_args=(--no-container --version "$version" --revision "$revision" --out "$inner_out" --image "$image")
    if [[ $skip_tests == true ]]; then
        inner_args+=(--skip-tests)
    fi

    step "Build inside $image"
    "$podman" run --rm --userns=keep-id --security-opt label=disable \
        -v "$repo_root:/src" -w /src "${mount_args[@]}" \
        -v gswitch-auto-gomod:/tmp/gomod -v gswitch-auto-gocache:/tmp/go-build \
        -e HOME=/tmp -e GOMODCACHE=/tmp/gomod -e GOCACHE=/tmp/go-build \
        -e GOFLAGS=-buildvcs=false \
        -e "GSWITCH_VERSION=$version" -e "GSWITCH_RELEASE=$revision" \
        "$image" scripts/build-deb.sh "${inner_args[@]}"
    exit 0
fi

# --- preflight ---------------------------------------------------------------
step "Preflight"

missing_tools=()
for tool in go gcc dpkg-deb dpkg lintian sha256sum python3 find cmp diff awk tar; do
    command -v "$tool" >/dev/null 2>&1 || missing_tools+=("$tool")
done
if ((${#missing_tools[@]} > 0)); then
    die "missing tools: ${missing_tools[*]} (Debian: apt install gcc dpkg-dev lintian python3 diffutils, plus the Go toolchain of go.mod, which bookworm's golang-go 1.19 is not; or drop --no-container to build inside the podman image)"
fi

[[ -f $nfpm_yaml ]] || die "$nfpm_yaml not found"

goarch=$(go env GOARCH)
[[ $goarch == amd64 ]] || die "only amd64 is packaged ($nfpm_yaml arch: amd64); this machine builds $goarch"

# In a container the checkout belongs to another user (the host user under
# podman without keep-id, the runner user in a CI container job), which git
# treats as "dubious" without this. It runs first, before scripts/version.sh
# reads the tags; never touch the global git config outside a container.
if [[ -f /run/.containerenv || -f /.dockerenv ]] && command -v git >/dev/null 2>&1; then
    git config --global --add safe.directory "$repo_root" 2>/dev/null || true
fi

resolve_version
check_sources

nfpm_cmd=()
if [[ -n ${NFPM:-} ]]; then
    resolved=$(command -v "$NFPM" 2>/dev/null) || die "NFPM=$NFPM: not found or not executable"
    nfpm_cmd=("$resolved")
elif command -v nfpm >/dev/null 2>&1; then
    nfpm_cmd=("$(command -v nfpm)")
elif [[ -x ${HOME:-/nonexistent}/go/bin/nfpm ]]; then
    nfpm_cmd=("$HOME/go/bin/nfpm")
else
    nfpm_cmd=(go run "$nfpm_module@$nfpm_version")
fi
printf 'nfpm: %s\n' "${nfpm_cmd[*]}"

# The version is injected with -X; inside the container the checkout may be
# a worktree whose git directory is outside the mount, so never stamp VCS
# information into the binaries.
export GOFLAGS=${GOFLAGS:--buildvcs=false}

step "Version rules (dpkg)"
GSWITCH_VERSION=$version scripts/version.sh --verify-deb

step "Extension consistency"
scripts/check-extension.sh

step "Maintainer scripts"
# check_scriptlet_markers DEB_SCRIPT UPSTREAM: the lines of DEB_SCRIPT between
# "# --- upstream UPSTREAM begin ---" and "# --- upstream UPSTREAM end ---"
# must be UPSTREAM without its final "exit 0" line (SPEC-X11 §3.8.2), so
# upstream scriptlet changes cannot drift silently after a rebase.
check_scriptlet_markers() {
    local deb_script=$1 upstream=$2
    local begin="# --- upstream $upstream begin ---" end="# --- upstream $upstream end ---"
    [[ -f $deb_script ]] || die "$deb_script not found"
    [[ -f $upstream ]] || die "$upstream not found"
    grep -qxF -- "$begin" "$deb_script" || die "$deb_script: marker '$begin' not found"
    grep -qxF -- "$end" "$deb_script" || die "$deb_script: marker '$end' not found"
    [[ $(tail -n 1 "$upstream") == 'exit 0' ]] ||
        die "$upstream no longer ends with 'exit 0'; update $deb_script and this check"
    if diff -u \
        <(awk -v b="$begin" -v e="$end" '$0 == e { p = 0 } p { print } $0 == b { p = 1 }' "$deb_script") \
        <(sed '$d' "$upstream"); then
        printf 'ok    %s embeds %s verbatim\n' "$deb_script" "$upstream"
    else
        die "$deb_script: the section between the markers differs from $upstream minus its final 'exit 0'; re-sync it (docs/FORK.md rebase checklist)"
    fi
}
check_scriptlet_markers scripts/deb/preinst.sh scripts/preinstall.sh
check_scriptlet_markers scripts/deb/postinst.sh scripts/postinstall.sh
for s in scripts/deb/preinst.sh scripts/deb/postinst.sh; do
    if env -i PATH=/nonexistent /bin/sh "$s" >/dev/null 2>&1; then
        printf 'ok    %s exits 0 in an empty environment\n' "$s"
    else
        die "$s fails in an empty environment (env -i PATH=/nonexistent /bin/sh $s)"
    fi
done

# --- test and build ----------------------------------------------------------
if [[ $skip_tests == true ]]; then
    step "Tests skipped (--skip-tests)"
else
    step "go test ./..."
    go test ./...
fi

step "Build binaries (builds/deb)"
mkdir -p builds/deb
ldflags="-s -w -X main.version=$version"
CGO_ENABLED=1 go build -trimpath -ldflags "$ldflags" -o builds/deb/gswitch ./cmd/gswitch
CGO_ENABLED=1 go build -trimpath -ldflags "$ldflags" -o builds/deb/gswitch-tray ./cmd/gswitch-tray
got=$(builds/deb/gswitch --version)
[[ $got == "gswitch version $version" ]] || die "builds/deb/gswitch --version printed '$got', want 'gswitch version $version'"
printf 'ok    %s\n' "$got"

# --- package -----------------------------------------------------------------
step "Package"
mkdir -p "$out"
out=$(cd "$out" && pwd)
deb_name="gswitch-auto_${version}-${revision}_amd64.deb"
deb_file="$out/$deb_name"
rm -f "$deb_file"
GSWITCH_VERSION=$version GSWITCH_RELEASE=$revision \
    "${nfpm_cmd[@]}" package --config "$nfpm_yaml" --packager deb --target "$deb_file"
[[ -f $deb_file ]] || die "nfpm did not produce $deb_file"
(cd "$out" && sha256sum "$deb_name" >checksums-deb.txt)
printf 'wrote %s and %s/checksums-deb.txt\n' "$deb_file" "$out"

# --- assertions --------------------------------------------------------------
step "Package assertions"

failures=0
# expect DESCRIPTION COMMAND... : COMMAND is an executable or a function.
expect() {
    local desc=$1
    shift
    if "$@"; then
        printf 'ok    %s\n' "$desc"
    else
        printf 'FAIL  %s\n' "$desc" >&2
        failures=$((failures + 1))
    fi
}
fail() {
    printf 'FAIL  %s\n' "$1" >&2
    failures=$((failures + 1))
}
# field NAME: the control field NAME.
field() {
    dpkg-deb -f "$deb_file" "$1"
}
# relation_has FIELD ENTRY: the comma-separated FIELD lists ENTRY exactly.
relation_has() {
    field "$1" | tr ',' '\n' | sed 's/^[[:space:]]*//; s/[[:space:]]*$//' | grep -qxF -- "$2"
}

expect "Package is gswitch-auto" test "$(field Package)" = gswitch-auto
expect "Version is $version-$revision" test "$(field Version)" = "$version-$revision"
expect "Architecture is amd64" test "$(field Architecture)" = amd64
expect "Section is utils" test "$(field Section)" = utils
expect "Priority is optional" test "$(field Priority)" = optional
expect "Maintainer is 'Name <address>' (lintian: maintainer-address-malformed)" \
    grep -Eq '^[^<>]+ <[^<>@[:space:]]+@[^<>@[:space:]]+>$' <<<"$(field Maintainer)"
expect "Homepage is set" test -n "$(field Homepage)"
for dep in 'libc6 (>= 2.34)' systemd libgtk-3-0 libx11-6 libglib2.0-bin policykit-1 x11-xkb-utils; do
    expect "Depends lists $dep" relation_has Depends "$dep"
done
for rec in libgtk-3-bin desktop-file-utils gnome-shell-extension-appindicator ibus wl-clipboard; do
    expect "Recommends lists $rec" relation_has Recommends "$rec"
done
expect "Suggests lists gnome-shell-extension-prefs" relation_has Suggests gnome-shell-extension-prefs
for rec in $(field Recommends | tr ',' '\n' | sed 's/^[[:space:]]*//; s/[[:space:]]*$//'); do
    if relation_has Suggests "$rec"; then
        fail "$rec is listed in both Recommends and Suggests"
    fi
done
expect "Provides gswitch (= $version-$revision)" test "$(field Provides)" = "gswitch (= $version-$revision)"
expect "Replaces gswitch" test "$(field Replaces)" = gswitch
expect "Conflicts gswitch" test "$(field Conflicts)" = gswitch

listing=$(dpkg-deb -c "$deb_file")
# entry_mode PATH: the mode column of the absolute PATH in the data archive,
# "" when absent. dpkg-deb -c lists members as ./usr/bin/gswitch, directories
# with a trailing slash.
entry_mode() {
    awk -v p="./${1#/}" '$NF == p || $NF == p "/" { print $1; exit }' <<<"$listing"
}
# expect_entry PATH MODE: PATH is packaged with the symbolic MODE.
expect_entry() {
    local got
    got=$(entry_mode "$1")
    if [[ $got == "$2" ]]; then
        printf 'ok    %s is %s\n' "$1" "$2"
    elif [[ -z $got ]]; then
        fail "$1 is not packaged"
    else
        fail "$1 is $got, want $2"
    fi
}
# expect_absent PATH: PATH is not packaged.
expect_absent() {
    if [[ -n $(entry_mode "$1") ]]; then
        fail "$1 must not be packaged"
    else
        printf 'ok    no %s\n' "$1"
    fi
}

expect_entry /usr/bin/gswitch -rwxr-xr-x
expect_entry /usr/bin/gswitch-tray -rwxr-xr-x
for f in \
    /usr/lib/systemd/user/gswitch.service \
    /usr/lib/udev/rules.d/70-gswitch.rules \
    /etc/gswitch/default.conf \
    /etc/xdg/autostart/gswitch-tray.desktop \
    /etc/xdg/autostart/gswitch-daemon.desktop \
    /usr/share/applications/gswitch-tray.desktop \
    /usr/share/polkit-1/actions/com.github.arumata.gswitch.policy \
    /usr/share/icons/hicolor/16x16/apps/gswitch.png \
    /usr/share/icons/hicolor/22x22/apps/gswitch.png \
    /usr/share/icons/hicolor/24x24/apps/gswitch.png \
    /usr/share/icons/hicolor/32x32/apps/gswitch.png \
    /usr/share/icons/hicolor/48x48/apps/gswitch.png \
    /usr/share/icons/hicolor/64x64/apps/gswitch.png \
    /usr/share/icons/hicolor/128x128/apps/gswitch.png \
    /usr/share/icons/hicolor/256x256/apps/gswitch.png \
    /usr/share/icons/hicolor/scalable/apps/gswitch.svg \
    "/usr/share/gnome-shell/extensions/$extension_uuid/metadata.json" \
    "/usr/share/gnome-shell/extensions/$extension_uuid/extension.js" \
    /usr/share/doc/gswitch-auto/copyright \
    /usr/share/doc/gswitch-auto/LICENSE \
    /usr/share/doc/gswitch-auto/punto-rs-LICENSE \
    /usr/share/doc/gswitch-auto/hunspell-ru-LICENSE \
    /usr/share/doc/gswitch-auto/THIRD-PARTY-NOTICES.md \
    /usr/share/doc/gswitch-auto/clipboard-LICENSE \
    /usr/share/doc/gswitch-auto/README.md; do
    expect_entry "$f" -rw-r--r--
done
# gnome-shell runs as the user: the extension must be world-readable
# whatever the builder's umask was.
expect_entry "/usr/share/gnome-shell/extensions/$extension_uuid" drwxr-xr-x
expect_entry /usr/share/doc/gswitch-auto drwxr-xr-x
expect_absent /usr/lib/systemd/system/gswitch.service
expect_absent /usr/share/licenses/gswitch-auto
expect_absent /usr/share/licenses
expect "every entry is owned by root/root" \
    test -z "$(awk '$2 != "root/root"' <<<"$listing")"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
dpkg-deb -e "$deb_file" "$tmp/control"
dpkg-deb -x "$deb_file" "$tmp/root"

conffiles=$(cat "$tmp/control/conffiles" 2>/dev/null || true)
for c in /etc/gswitch/default.conf /etc/xdg/autostart/gswitch-tray.desktop /etc/xdg/autostart/gswitch-daemon.desktop; do
    expect "$c is a conffile" grep -qxF -- "$c" <<<"$conffiles"
done
expect "conffiles lists exactly three files" test "$(grep -c . <<<"$conffiles")" = 3
expect "control has md5sums" test -s "$tmp/control/md5sums"
for s in preinst postinst prerm postrm; do
    expect "control has an executable $s" test -x "$tmp/control/$s"
done
expect "preinst embeds scripts/preinstall.sh" \
    grep -qxF -- '# --- upstream scripts/preinstall.sh begin ---' "$tmp/control/preinst"
expect "postinst embeds scripts/postinstall.sh" \
    grep -qxF -- '# --- upstream scripts/postinstall.sh begin ---' "$tmp/control/postinst"
expect "prerm is upstream scripts/preremove.sh" cmp -s "$tmp/control/prerm" scripts/preremove.sh
expect "postrm is upstream scripts/postremove.sh" cmp -s "$tmp/control/postrm" scripts/postremove.sh

# The packaged user unit must be byte-identical to upstream's (SPEC D2).
base=$(GSWITCH_VERSION=$version scripts/version.sh --base)
if git rev-parse -q --verify "refs/tags/v$base" >/dev/null 2>&1 &&
    git show "v$base:configs/gswitch.service" >"$tmp/upstream.service" 2>/dev/null; then
    reference="configs/gswitch.service of upstream tag v$base"
else
    printf 'note  tag v%s not available; comparing the unit with configs/gswitch.service\n' "$base"
    cp configs/gswitch.service "$tmp/upstream.service"
    reference="configs/gswitch.service"
fi
expect "packaged user unit is byte-identical to $reference" \
    cmp -s "$tmp/root/usr/lib/systemd/user/gswitch.service" "$tmp/upstream.service"
expect "packaged default.conf is configs/default.conf" \
    cmp -s "$tmp/root/etc/gswitch/default.conf" configs/default.conf

# The deb ships the legacy (non-ESM) extension tree for GNOME 43/44
# (SPEC-X11 §3.6): a script with init() and no ES module syntax.
ext_dir="$tmp/root/usr/share/gnome-shell/extensions/$extension_uuid"
expect "packaged extension.js is the legacy variant (function init)" \
    grep -q 'function init(' "$ext_dir/extension.js"
if grep -Eq '^(import |export )' "$ext_dir/extension.js"; then
    fail "packaged extension.js contains ES module syntax (import/export): not the legacy variant"
else
    printf 'ok    packaged extension.js has no ES module syntax\n'
fi
expect "packaged metadata.json uuid is $extension_uuid" \
    test "$(python3 -c 'import json, sys; print(json.load(open(sys.argv[1], encoding="utf-8")).get("uuid", ""))' "$ext_dir/metadata.json")" = "$extension_uuid"

expect "copyright file is DEP-5" \
    grep -q '^Format: https://www.debian.org/doc/packaging-manuals/copyright-format/1.0/$' \
    "$tmp/root/usr/share/doc/gswitch-auto/copyright"

# --- lintian ------------------------------------------------------------------
step "lintian"
# Errors fail the build. debian-changelog-file-missing is suppressed (SPEC-X11
# §3.8.1, Q-X6); warnings and info tags are advisory and listed here.
lintian_rc=0
lintian --allow-root --no-tag-display-limit --fail-on error \
    --suppress-tags debian-changelog-file-missing "$deb_file" || lintian_rc=$?
if ((lintian_rc == 0)); then
    printf 'ok    lintian reports no errors (warnings and info tags above are advisory)\n'
else
    fail "lintian exited with $lintian_rc (error tags above)"
fi

if ((failures > 0)); then
    die "$failures package assertion(s) failed for $deb_file"
fi

step "Done"
printf '%s\n' "$deb_file"
cat "$out/checksums-deb.txt"
