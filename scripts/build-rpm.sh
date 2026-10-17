#!/usr/bin/env bash
# Builds the gswitch-auto RPM with nfpm (SPEC §3.13; replaces upstream's
# GoReleaser flow, see docs/FORK.md).
#
# Usage: scripts/build-rpm.sh [--version V] [--release R] [--out DIR]
#                             [--skip-tests] [--nfpm PATH]
#
#   --version V   package version (default: output of scripts/version.sh)
#   --release R   RPM release number (default: 1)
#   --out DIR     output directory for the RPM and checksums.txt (default: dist)
#   --skip-tests  do not run `go test ./...`
#   --nfpm PATH   nfpm executable; default: nfpm on PATH, then ~/go/bin/nfpm,
#                 then `go run github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.48.0`
#
# Steps: preflight (tools, every packaged source path, version rules, extension
# consistency) -> go test ./... -> CGO build of builds/gswitch and
# builds/gswitch-tray with -X main.version=V -> nfpm -> sha256 checksums.txt ->
# metadata assertions on the RPM (rpm -qp).
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
readonly repo_root
readonly nfpm_yaml=packaging/nfpm.yaml
readonly nfpm_module=github.com/goreleaser/nfpm/v2/cmd/nfpm
readonly nfpm_version=v2.48.0
readonly expected_license='MIT AND BSD-3-Clause-Modification AND LicenseRef-Fedora-Public-Domain'
readonly extension_uuid=gswitch-focus@vabuter.github.io

version=""
release=1
out=dist
skip_tests=false
nfpm_opt=""

die() {
    printf 'build-rpm: %s\n' "$*" >&2
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
        --release)
            need_arg "$@"
            release=$2
            shift 2
            ;;
        --release=*)
            release=${1#*=}
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
        --nfpm)
            need_arg "$@"
            nfpm_opt=$2
            shift 2
            ;;
        --nfpm=*)
            nfpm_opt=${1#*=}
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

# --- preflight -------------------------------------------------------------
step "Preflight"

missing_tools=()
for tool in go rpm rpm2cpio cpio sha256sum python3 find; do
    command -v "$tool" >/dev/null 2>&1 || missing_tools+=("$tool")
done
if ((${#missing_tools[@]} > 0)); then
    die "missing tools: ${missing_tools[*]} (Fedora: dnf install golang rpm cpio python3; Debian/Ubuntu: apt install golang rpm rpm2cpio cpio python3)"
fi

[[ -f $nfpm_yaml ]] || die "$nfpm_yaml not found"

goarch=$(go env GOARCH)
[[ $goarch == amd64 ]] || die "only amd64 is packaged ($nfpm_yaml arch: amd64); this host builds $goarch"

[[ $release =~ ^[1-9][0-9]*$ ]] || die "invalid release '$release' (expected a positive integer)"

if [[ -z $version ]]; then
    version=$(scripts/version.sh) || die "cannot compute the version; pass --version V"
fi
# Validates the format (no "~", no "-"); exits non-zero with a message otherwise.
version=$(GSWITCH_VERSION=$version scripts/version.sh)
base=$(GSWITCH_VERSION=$version scripts/version.sh --base)
printf 'version %s, release %s, upstream base %s\n' "$version" "$release" "$base"

# Every source the package needs, except the binaries built below.
mapfile -t sources < <(
    sed -n \
        -e 's/^[[:space:]]*-\{0,1\}[[:space:]]*src:[[:space:]]*\([^[:space:]#]*\).*$/\1/p' \
        -e 's/^[[:space:]]*\(pre\|post\)\(install\|remove\|trans\):[[:space:]]*\([^[:space:]#]*\).*$/\3/p' \
        "$nfpm_yaml" | grep -v '^builds/' | sort -u
)
((${#sources[@]} > 0)) || die "no src entries found in $nfpm_yaml"
missing_sources=()
for src in "${sources[@]}"; do
    [[ -e $src ]] || missing_sources+=("$src")
done
if ((${#missing_sources[@]} > 0)); then
    printf 'build-rpm: missing packaged source(s) listed in %s:\n' "$nfpm_yaml" >&2
    printf '  %s\n' "${missing_sources[@]}" >&2
    die "the tree is incomplete (shell-extension/ and third_party/punto-rs/ come with the extension and detector work); nothing was built"
fi
printf 'ok    all %d packaged source paths exist\n' "${#sources[@]}"

nfpm_cmd=()
if [[ -n $nfpm_opt ]]; then
    resolved=$(command -v "$nfpm_opt" 2>/dev/null) || die "--nfpm $nfpm_opt: not found or not executable"
    nfpm_cmd=("$resolved")
elif command -v nfpm >/dev/null 2>&1; then
    nfpm_cmd=("$(command -v nfpm)")
elif [[ -x $HOME/go/bin/nfpm ]]; then
    nfpm_cmd=("$HOME/go/bin/nfpm")
else
    nfpm_cmd=(go run "$nfpm_module@$nfpm_version")
fi
printf 'nfpm: %s\n' "${nfpm_cmd[*]}"

step "Version rules"
GSWITCH_VERSION=$version scripts/version.sh --verify

step "Extension consistency"
scripts/check-extension.sh

# --- test and build --------------------------------------------------------
if [[ $skip_tests == true ]]; then
    step "Tests skipped (--skip-tests)"
else
    step "go test ./..."
    go test ./...
fi

step "Build binaries"
mkdir -p builds
ldflags="-s -w -X main.version=$version"
CGO_ENABLED=1 go build -trimpath -ldflags "$ldflags" -o builds/gswitch ./cmd/gswitch
CGO_ENABLED=1 go build -trimpath -ldflags "$ldflags" -o builds/gswitch-tray ./cmd/gswitch-tray
got=$(builds/gswitch --version)
[[ $got == "gswitch version $version" ]] || die "builds/gswitch --version printed '$got', want 'gswitch version $version'"
printf 'ok    %s\n' "$got"

# --- package ---------------------------------------------------------------
step "Package"
mkdir -p "$out"
out=$(cd "$out" && pwd)
rpm_name="gswitch-auto-$version-$release.x86_64.rpm"
rpm_file="$out/$rpm_name"
rm -f "$rpm_file"
GSWITCH_VERSION=$version GSWITCH_RELEASE=$release \
    "${nfpm_cmd[@]}" package --config "$nfpm_yaml" --packager rpm --target "$rpm_file"
[[ -f $rpm_file ]] || die "nfpm did not produce $rpm_file"
(cd "$out" && sha256sum "$rpm_name" >checksums.txt)
printf 'wrote %s and %s/checksums.txt\n' "$rpm_file" "$out"

# --- assertions ------------------------------------------------------------
step "RPM assertions"

failures=0
# expect DESCRIPTION COMMAND... : COMMAND must be an executable, not a function.
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
# expect_line DESCRIPTION TEXT LINE : TEXT contains LINE exactly.
expect_line() {
    expect "$1" grep -qxF -- "$3" <<<"$2"
}
# expect_no_line DESCRIPTION TEXT LINE : TEXT does not contain LINE.
expect_no_line() {
    if grep -qxF -- "$3" <<<"$2"; then
        printf 'FAIL  %s\n' "$1" >&2
        failures=$((failures + 1))
    else
        printf 'ok    %s\n' "$1"
    fi
}
q() {
    rpm -qp --nosignature "$@" "$rpm_file" 2>/dev/null
}

obsoletes_bound=$(sed -n 's/^[[:space:]]*-[[:space:]]*gswitch < \([0-9][0-9.]*\)[[:space:]]*$/\1/p' "$nfpm_yaml" | head -n 1)
ident=$(q --qf '%{NAME} %{VERSION} %{RELEASE} %{ARCH}')
provides=$(q --provides)
obsoletes=$(q --obsoletes)
conflicts=$(q --conflicts)
requires=$(q --requires)
license=$(q --qf '%{LICENSE}')
files=$(q --list)
scripts=$(q --scripts)
flags=$(q --qf '[%{FILENAMES} %{FILEFLAGS:fflags}\n]')
modes=$(q --qf '[%{FILENAMES} %{FILEMODES:perms}\n]')

expect "identity is gswitch-auto $version $release x86_64" \
    test "$ident" = "gswitch-auto $version $release x86_64"
expect_line "provides gswitch = $version-$release" "$provides" "gswitch = $version-$release"
expect_line "obsoletes gswitch < ${obsoletes_bound:-?}" "$obsoletes" "gswitch < ${obsoletes_bound:-?}"
expect "no conflicts entry" test -z "$conflicts"
expect_line "requires systemd" "$requires" "systemd"
expect_line "requires gtk3" "$requires" "gtk3"
expect "license is $expected_license" test "$license" = "$expected_license"

for f in \
    /usr/bin/gswitch \
    /usr/bin/gswitch-tray \
    /usr/lib/systemd/user/gswitch.service \
    /usr/lib/udev/rules.d/70-gswitch.rules \
    /etc/gswitch/default.conf \
    /etc/xdg/autostart/gswitch-tray.desktop \
    /etc/xdg/autostart/gswitch-daemon.desktop \
    /usr/share/applications/gswitch-tray.desktop \
    /usr/share/polkit-1/actions/com.github.arumata.gswitch.policy \
    /usr/share/icons/hicolor/scalable/apps/gswitch.svg \
    "/usr/share/gnome-shell/extensions/$extension_uuid/metadata.json" \
    "/usr/share/gnome-shell/extensions/$extension_uuid/extension.js" \
    /usr/share/licenses/gswitch-auto/LICENSE \
    /usr/share/licenses/gswitch-auto/punto-rs-LICENSE \
    /usr/share/licenses/gswitch-auto/hunspell-ru-LICENSE \
    /usr/share/licenses/gswitch-auto/THIRD-PARTY-NOTICES.md \
    /usr/share/licenses/gswitch-auto/clipboard-LICENSE \
    /usr/share/doc/gswitch-auto/README.md; do
    expect_line "contains $f" "$files" "$f"
done
expect_no_line "no legacy /usr/lib/systemd/system/gswitch.service" \
    "$files" /usr/lib/systemd/system/gswitch.service

for s in pretrans preinstall postinstall preuninstall postuninstall posttrans; do
    expect "has a $s scriptlet" grep -q "^$s scriptlet" <<<"$scripts"
done

expect "default.conf is %config" \
    grep -Eq '^/etc/gswitch/default\.conf [a-z]*c[a-z]*$' <<<"$flags"
expect "default.conf is noreplace" \
    grep -Eq '^/etc/gswitch/default\.conf [a-z]*n[a-z]*$' <<<"$flags"
expect "hunspell-ru-LICENSE is %license" \
    grep -Eq '^/usr/share/licenses/gswitch-auto/hunspell-ru-LICENSE [a-z]*l[a-z]*$' <<<"$flags"
expect "README.md is %doc" \
    grep -Eq '^/usr/share/doc/gswitch-auto/README\.md [a-z]*d[a-z]*$' <<<"$flags"
expect_line "/usr/bin/gswitch is 0755" "$modes" "/usr/bin/gswitch -rwxr-xr-x"
expect_line "/usr/bin/gswitch-tray is 0755" "$modes" "/usr/bin/gswitch-tray -rwxr-xr-x"

# The packaged user unit must be byte-identical to upstream's (SPEC D2).
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
(cd "$tmp" && rpm2cpio "$rpm_file" | cpio -idmu --quiet ./usr/lib/systemd/user/gswitch.service)
if git rev-parse -q --verify "refs/tags/v$base" >/dev/null 2>&1 &&
    git show "v$base:configs/gswitch.service" >"$tmp/upstream.service" 2>/dev/null; then
    reference="configs/gswitch.service of upstream tag v$base"
else
    printf 'note  tag v%s not available; comparing the unit with configs/gswitch.service\n' "$base"
    cp configs/gswitch.service "$tmp/upstream.service"
    reference="configs/gswitch.service"
fi
expect "packaged user unit is byte-identical to $reference" \
    cmp -s "$tmp/usr/lib/systemd/user/gswitch.service" "$tmp/upstream.service"

if ((failures > 0)); then
    die "$failures RPM assertion(s) failed for $rpm_file"
fi

step "Done"
printf '%s\n' "$rpm_file"
cat "$out/checksums.txt"
