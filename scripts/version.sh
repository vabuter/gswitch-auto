#!/usr/bin/env bash
# gswitch-auto package version (docs/FORK.md, SPEC §3.13, SPEC-X11 §3.8.4).
#
#   HEAD at tag v<base>+auto.<N>, clean tree  ->  <base>+auto.<N>
#   commits after that tag, or a dirty tree   ->  <base>+auto.<N>.dev<n>.g<sha>[.dirty]
#   no fork tag for the current base yet      ->  <base>+auto.0.dev<n>.g<sha>[.dirty]
#
# <base> is the upstream gswitch release the fork is based on: the nearest
# reachable v<X.Y.Z> tag. "~" versions sort below upstream (rpm and dpkg
# alike) and are forbidden; so is "-", which starts the Debian revision.
# GSWITCH_VERSION, when set, overrides the computation (it is still validated).
#
# Usage:
#   scripts/version.sh            print the version
#   scripts/version.sh --base     print the upstream base version
#   scripts/version.sh --verify   check the rpm ordering rules, the computed
#                                 version and the Obsoletes bound in
#                                 packaging/nfpm.yaml; prints the version last
#   scripts/version.sh --verify-deb
#                                 the same ordering rules as dpkg compares
#                                 them (plus the deb-specific pairs); used by
#                                 build-deb.sh; prints the version last
#   scripts/version.sh --vercmp A B
#                                 compare two [E:]V[-R] strings as rpm does;
#                                 prints -1, 0 or 1 (used by build-rpm.sh)
#   scripts/version.sh --debcmp A B
#                                 compare two [E:]V[-R] strings as dpkg does
#                                 (dpkg --compare-versions, or a Python port
#                                 of its algorithm); prints -1, 0 or 1
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
readonly repo_root
readonly nfpm_yaml="$repo_root/packaging/nfpm.yaml"
readonly version_re='^([0-9]+\.[0-9]+\.[0-9]+)\+auto\.([0-9]+)(\.dev[0-9]+\.g[0-9a-f]+(\.dirty)?)?$'

die() {
    printf 'version.sh: %s\n' "$*" >&2
    exit 1
}

usage() {
    sed -n '2,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//'
}

validate() {
    [[ $1 =~ $version_re ]] ||
        die "invalid version '$1' (expected <X.Y.Z>+auto.<N>[.dev<n>.g<sha>[.dirty]]; '~' and '-' are not allowed)"
}

git_in_repo() {
    git -C "$repo_root" "$@"
}

# Nearest upstream release tag (vX.Y.Z, no "+auto." and no pre-release suffix).
upstream_tag() {
    git_in_repo describe --tags --abbrev=0 --match 'v[0-9]*' \
        --exclude '*+*' --exclude '*-*' HEAD 2>/dev/null
}

# Nearest fork release tag (vX.Y.Z+auto.N).
fork_tag() {
    git_in_repo describe --tags --abbrev=0 --match 'v[0-9]*+auto.*' HEAD 2>/dev/null
}

compute_version() {
    if [[ -n ${GSWITCH_VERSION:-} ]]; then
        validate "$GSWITCH_VERSION"
        printf '%s\n' "$GSWITCH_VERSION"
        return
    fi

    git_in_repo rev-parse --is-inside-work-tree >/dev/null 2>&1 ||
        die "$repo_root is not a git checkout; set GSWITCH_VERSION=<X.Y.Z>+auto.<N>"

    local up fork ref base auto
    up=$(upstream_tag || true)
    fork=$(fork_tag || true)

    # Prefer the fork tag unless a newer upstream release was merged after it
    # (then the fork has no tag for the new base yet: auto.0).
    if [[ -n $fork ]] && { [[ -z $up ]] || git_in_repo merge-base --is-ancestor "$up" "$fork"; }; then
        local t=${fork#v}
        [[ $t =~ ^([0-9]+\.[0-9]+\.[0-9]+)\+auto\.([0-9]+)$ ]] ||
            die "malformed fork tag '$fork' (expected v<X.Y.Z>+auto.<N>)"
        ref=$fork
        base=${BASH_REMATCH[1]}
        auto=${BASH_REMATCH[2]}
    elif [[ -n $up ]]; then
        local t=${up#v}
        [[ $t =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
            die "malformed upstream tag '$up' (expected v<X.Y.Z>)"
        ref=$up
        base=$t
        auto=0
    else
        die "no upstream release tag (v<X.Y.Z>) is reachable from HEAD; run 'git fetch upstream --tags' or set GSWITCH_VERSION"
    fi

    local since sha dirty=""
    since=$(git_in_repo rev-list --count "$ref..HEAD")
    sha=$(git_in_repo rev-parse --short=7 HEAD)
    git_in_repo update-index -q --refresh >/dev/null 2>&1 || true
    if ! git_in_repo diff-index --quiet HEAD --; then
        dirty=".dirty"
    fi

    local v
    if [[ $auto != 0 && $since == 0 && -z $dirty ]]; then
        v="$base+auto.$auto"
    else
        v="$base+auto.$auto.dev$since.g$sha$dirty"
    fi
    validate "$v"
    printf '%s\n' "$v"
}

base_of() {
    [[ $1 =~ $version_re ]] || die "invalid version '$1'"
    printf '%s\n' "${BASH_REMATCH[1]}"
}

# vercmp A B -> prints -1, 0 or 1 comparing two [E:]V[-R] strings like rpm.
vercmp() {
    local a=$1 b=$2 out=""
    local allowed='^[0-9A-Za-z.+~^_:-]+$'
    [[ $a =~ $allowed && $b =~ $allowed ]] || die "vercmp: unexpected characters in '$a' / '$b'"

    if command -v rpm >/dev/null 2>&1; then
        # rpm.vercmp compares one version string; split V-R ourselves so the
        # release is compared separately, exactly as rpm compares EVRs.
        local va=$a ra="" vb=$b rb=""
        if [[ $a == *-* ]]; then va=${a%-*} ra=${a##*-}; fi
        if [[ $b == *-* ]]; then vb=${b%-*} rb=${b##*-}; fi
        out=$(rpm --eval "%{lua:
            local c = rpm.vercmp('$va', '$vb')
            if c == 0 and '$ra' ~= '' and '$rb' ~= '' then c = rpm.vercmp('$ra', '$rb') end
            if c < 0 then print(-1) elseif c > 0 then print(1) else print(0) end
        }" 2>/dev/null || true)
    fi
    if [[ $out != -1 && $out != 0 && $out != 1 ]] && command -v python3 >/dev/null 2>&1; then
        out=$(python3 - "$a" "$b" 2>/dev/null <<'EOF' || true
import sys
import rpm

def evr(s):
    e, _, rest = s.rpartition(":")
    v, _, r = rest.partition("-")
    return (e or "0", v, r or None)

c = rpm.labelCompare(evr(sys.argv[1]), evr(sys.argv[2]))
print(-1 if c < 0 else (1 if c > 0 else 0))
EOF
)
    fi
    [[ $out == -1 || $out == 0 || $out == 1 ]] ||
        die "cannot compare versions: install rpm (with Lua, any rpm >= 4.7) or python3-rpm"
    printf '%s\n' "$out"
}

# debcmp A B -> prints -1, 0 or 1 comparing two [E:]V[-R] strings like dpkg.
debcmp() {
    local a=$1 b=$2 out=""
    local allowed='^[0-9A-Za-z.+~:-]+$'
    [[ $a =~ $allowed && $b =~ $allowed ]] || die "debcmp: unexpected characters in '$a' / '$b'"

    if command -v dpkg >/dev/null 2>&1; then
        if dpkg --compare-versions "$a" lt "$b" 2>/dev/null; then
            out=-1
        elif dpkg --compare-versions "$a" eq "$b" 2>/dev/null; then
            out=0
        elif dpkg --compare-versions "$a" gt "$b" 2>/dev/null; then
            out=1
        fi
    fi
    if [[ -z $out ]] && command -v python3 >/dev/null 2>&1; then
        out=$(python3 - "$a" "$b" 2>/dev/null <<'EOF' || true
import sys

# Port of dpkg's lib/dpkg/version.c (order, verrevcmp, dpkg_version_compare):
# "~" sorts before anything, even the end of the string; letters sort before
# non-letters; digit runs compare numerically; epoch, upstream version and
# revision are compared in turn, a missing revision being "".


def order(c):
    if c.isdigit():
        return 0
    if c.isalpha():
        return ord(c)
    if c == "~":
        return -1
    if c:
        return ord(c) + 256
    return 0


def verrevcmp(a, b):
    i = j = 0
    while i < len(a) or j < len(b):
        first_diff = 0
        while (i < len(a) and not a[i].isdigit()) or (j < len(b) and not b[j].isdigit()):
            ac = order(a[i]) if i < len(a) else 0
            bc = order(b[j]) if j < len(b) else 0
            if ac != bc:
                return ac - bc
            i += 1
            j += 1
        while i < len(a) and a[i] == "0":
            i += 1
        while j < len(b) and b[j] == "0":
            j += 1
        while i < len(a) and a[i].isdigit() and j < len(b) and b[j].isdigit():
            if not first_diff:
                first_diff = ord(a[i]) - ord(b[j])
            i += 1
            j += 1
        if i < len(a) and a[i].isdigit():
            return 1
        if j < len(b) and b[j].isdigit():
            return -1
        if first_diff:
            return first_diff
    return 0


def parse(s):
    epoch, sep, rest = s.partition(":")
    if not sep:
        epoch, rest = "0", s
    version, sep, revision = rest.rpartition("-")
    if not sep:
        version, revision = rest, ""
    return int(epoch), version, revision


ea, va, ra = parse(sys.argv[1])
eb, vb, rb = parse(sys.argv[2])
c = (ea > eb) - (ea < eb) or verrevcmp(va, vb) or verrevcmp(ra, rb)
print(-1 if c < 0 else (1 if c > 0 else 0))
EOF
)
    fi
    [[ $out == -1 || $out == 0 || $out == 1 ]] ||
        die "cannot compare Debian versions: install dpkg or python3"
    printf '%s\n' "$out"
}

# --- ordering rules --------------------------------------------------------
# check() compares with $cmp_fn (vercmp for rpm, debcmp for dpkg) and counts
# failures in $failures; both are set by verify() and verify_deb().
cmp_fn=vercmp
failures=0

check() { # check A OP B, OP in < = >
    local a=$1 op=$2 b=$3 want got
    case $op in
        '<') want=-1 ;;
        '=') want=0 ;;
        '>') want=1 ;;
        *) die "check: bad operator $op" ;;
    esac
    got=$("$cmp_fn" "$a" "$b")
    if [[ $got == "$want" ]]; then
        printf 'ok    %s %s %s\n' "$a" "$op" "$b"
    else
        printf 'FAIL  %s %s %s (%s says %s)\n' "$a" "$op" "$b" "$cmp_fn" "$got" >&2
        failures=$((failures + 1))
    fi
}

expect_invalid() {
    if (validate "$1") 2>/dev/null; then
        printf 'FAIL  %s is accepted as a version\n' "$1" >&2
        failures=$((failures + 1))
    else
        printf 'ok    %s is rejected\n' "$1"
    fi
}

# The ordering rules shared by --verify and --verify-deb (SPEC §3.13,
# verified against rpm 6.0.2 when written; SPEC-X11 §3.8.4 for dpkg). Sets
# the caller's `v` to the computed version.
ordering_rules() {
    check '0.8.0+auto.1-1' '>' '0.8.0-1'
    check '0.8.0+auto.1-1' '>' '0.8.0-2'
    check '0.8.0+auto.1-1' '<' '0.8.1-1'
    check '0.8.0+auto.0.dev5.gabc1234-1' '>' '0.8.0-1'
    check '0.8.0+auto.0.dev5.gabc1234-1' '<' '0.8.0+auto.1-1'
    check '0.8.0+auto.1.dev3.gabc1234-1' '>' '0.8.0+auto.1-1'
    check '0.8.0+auto.1.dev3.gabc1234-1' '<' '0.8.0+auto.2-1'
    check '0.8.0+auto.1.dev3.gabc1234.dirty-1' '>' '0.8.0+auto.1.dev3.gabc1234-1'
    check '0.8.0+auto.9-1' '<' '0.8.0+auto.10-1'
    check '0.8.0+auto.1-1' '<' '0.9'
    # Why "~" is forbidden: it sorts below the upstream release it is based on.
    check '0.8.0~auto.1-1' '<' '0.8.0-1'
    expect_invalid '0.8.0~auto.1'
    expect_invalid '0.8.0-auto.1'
    expect_invalid '0.8.0'

    local base major minor patch
    v=$(compute_version)
    base=$(base_of "$v")
    IFS=. read -r major minor patch <<<"$base"
    check "$v-1" '>' "$base-1"
    check "$v-1" '>' "$base-2"
    check "$v-1" '<' "$major.$minor.$((patch + 1))-1"
}

verify() {
    local v
    cmp_fn=vercmp
    failures=0
    ordering_rules

    # Obsoletes bound: upstream releases of the base must be obsoleted.
    if [[ -f $nfpm_yaml ]]; then
        local bound base
        base=$(base_of "$v")
        bound=$(sed -n 's/^[[:space:]]*-[[:space:]]*gswitch < \([0-9][0-9.]*\)[[:space:]]*$/\1/p' "$nfpm_yaml" | head -n 1)
        if [[ -z $bound ]]; then
            printf 'FAIL  no "gswitch < X" replaces entry in %s\n' "${nfpm_yaml#"$repo_root"/}" >&2
            failures=$((failures + 1))
        else
            check "$base-1" '<' "$bound"
        fi
    else
        printf 'skip  Obsoletes bound: %s not found\n' "${nfpm_yaml#"$repo_root"/}" >&2
    fi

    if ((failures > 0)); then
        die "$failures check(s) failed"
    fi
    printf '%s\n' "$v"
}

verify_deb() {
    local v
    cmp_fn=debcmp
    failures=0
    ordering_rules

    # dpkg grammar: "-" would start the revision and "~" sorts below the base
    # (validate() rejects both; the deb build relies on it, so say so).
    if [[ $v == *-* || $v == *~* ]]; then
        printf 'FAIL  %s contains "-" or "~"\n' "$v" >&2
        failures=$((failures + 1))
    else
        printf 'ok    %s contains neither "-" nor "~"\n' "$v"
    fi

    if ((failures > 0)); then
        die "$failures check(s) failed"
    fi
    printf '%s\n' "$v"
}

case "${1:-}" in
    '') compute_version ;;
    --base) base_of "$(compute_version)" ;;
    --verify) verify ;;
    --verify-deb) verify_deb ;;
    --vercmp)
        [[ $# -eq 3 && -n $2 && -n $3 ]] || die "usage: version.sh --vercmp A B"
        vercmp "$2" "$3"
        ;;
    --debcmp)
        [[ $# -eq 3 && -n $2 && -n $3 ]] || die "usage: version.sh --debcmp A B"
        debcmp "$2" "$3"
        ;;
    -h | --help) usage ;;
    *)
        usage >&2
        exit 2
        ;;
esac
