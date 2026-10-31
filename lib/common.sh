# shellcheck shell=bash
# platform: host-agnostic
# Shared helpers. Sourced, never executed.
#
# Deliberately does not set shell options: a library that mutates the
# caller's environment behaves differently depending on who sourced it.

: "${GUEST_MACOSX_LOG_PREFIX:=guest-macosx}"

log()  { printf '%s: %s\n'            "$GUEST_MACOSX_LOG_PREFIX" "$*" >&2; }
warn() { printf '%s: warning: %s\n'   "$GUEST_MACOSX_LOG_PREFIX" "$*" >&2; }
die()  { printf '%s: error: %s\n'     "$GUEST_MACOSX_LOG_PREFIX" "$*" >&2; exit 1; }

# Absolute path of the repository root, derived from this file's location.
repo_root() {
    ( cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd )
}

# Fail if any named command is missing, naming all of them rather than
# stopping at the first.
require_cmd() {
    local missing=0 c
    for c in "$@"; do
        if ! command -v "$c" >/dev/null 2>&1; then
            warn "missing required command: $c"
            missing=1
        fi
    done
    [ "$missing" -eq 0 ] || die "missing required commands"
}

sha256_file() {
    [ -f "$1" ] || die "no such file: $1"
    local sum
    sum=$(sha256sum "$1") || die "cannot read: $1"
    printf '%s\n' "${sum%% *}"
}

# Verify a file against an expected checksum. Reports both values, because
# "checksum mismatch" without the numbers is useless when debugging a
# partial download.
verify_sha256() {
    local file=$1 want=$2 got
    # sha256_file's own `die` only kills the command-substitution subshell
    # below, not this function -- without the explicit `|| exit 1`, a
    # missing/unreadable file would fall through to the mismatch check and
    # print a misleading "checksum mismatch" instead of the real error.
    got=$(sha256_file "$file") || exit 1
    if [ "$got" != "$want" ]; then
        die "checksum mismatch for $file: want $want, got $got"
    fi
}

# is_gate_qmp_dir <path>: the path is a QMP socket directory bin/gate-vm.sh made with
# `mktemp -d /tmp/gate-qmp.XXXXXX`, and nothing else: the suffix is letters and digits only,
# so no `..`, no further `/`. The one rule for anything that removes such a directory.
is_gate_qmp_dir() {
    case $1 in /tmp/gate-qmp.*) ;; *) return 1 ;; esac
    case ${1#/tmp/gate-qmp.} in ''|*[!A-Za-z0-9]*) return 1 ;; esac
    return 0
}

# new_uuid: a fresh random UUID on stdout, lower case, for QEMU's -uuid: uuidgen where the host
# has it (macOS, NetBSD, util-linux), else the Linux kernel's (GUEST_MACOSX_KERNEL_UUID, a test
# seam), else 16 bytes of /dev/urandom shaped as a version 4 UUID. Fails, printing nothing, when
# none of them gives one. Every guest a launcher here starts gets its own: distinct machines have
# distinct host UUIDs (docs/decisions/0024).
new_uuid() {
    local u h v
    u=$(uuidgen 2>/dev/null | tr 'A-F' 'a-f')
    is_uuid "$u" || u=$(tr 'A-F' 'a-f' 2>/dev/null < "${GUEST_MACOSX_KERNEL_UUID:-/proc/sys/kernel/random/uuid}")
    if ! is_uuid "$u"; then
        h=$(od -An -tx1 -N16 /dev/urandom 2>/dev/null | tr -d ' \n')
        [ "${#h}" -eq 32 ] || return 1
        v=$(printf '%x' $(( (0x${h:16:1} & 3) | 8 )))
        u="${h:0:8}-${h:8:4}-4${h:13:3}-$v${h:17:3}-${h:20:12}"
    fi
    is_uuid "$u" || return 1
    printf '%s\n' "$u"
}

# is_uuid <s>: s is 8-4-4-4-12 lower-case hex digits.
is_uuid() {
    case $1 in
        [0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]-[0-9a-f][0-9a-f][0-9a-f][0-9a-f]-[0-9a-f][0-9a-f][0-9a-f][0-9a-f]-[0-9a-f][0-9a-f][0-9a-f][0-9a-f]-[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]) return 0 ;;
    esac
    return 1
}

# derived_mac <uuid>: the NIC address a machine with this host UUID gets, the same rule as the box
# Vagrantfiles' trigger: 52:54:00 (locally administered, QEMU's own prefix), then three bytes of the
# UUID's SHA-256, skipping six hex digits at a time past any "1234", so it is never 52:54:00:12:34:xx,
# the range QEMU gives a NIC with no address of its own (docs/decisions/0024).
derived_mac() {
    local h i=0
    h=$(printf '%s' "$1" | { sha256sum 2>/dev/null || shasum -a 256; } | sed -e 's/ .*//')
    [ "${#h}" -eq 64 ] || return 1
    while [ "${h:$i:4}" = 1234 ]; do i=$((i + 6)); done
    printf '52:54:00:%s:%s:%s\n' "${h:$i:2}" "${h:$((i + 2)):2}" "${h:$((i + 4)):2}"
}

# The Mac OS X releases this project's boxes, gate and launchers know, spelled here alone.
GUEST_MACOSX_RELEASES="10.9 10.6"

# known_release <r>: r is one of GUEST_MACOSX_RELEASES, exactly.
known_release() {
    case $1 in ''|*[!0-9.]*) return 1 ;; esac
    case " $GUEST_MACOSX_RELEASES " in *" $1 "*) return 0 ;; esac
    return 1
}

# releases_said: the releases, as a message names them ("10.9 or 10.6").
releases_said() {
    printf '%s\n' "$GUEST_MACOSX_RELEASES" | sed -e 's/ \([^ ]*\)$/ or \1/' -e 's/ \([^ ]*\) or/, \1 or/g'
}

# real_path <file>: the file's physical path, every symlink on the way followed, the file's own
# included; readlink without -f (which macOS and NetBSD lack), so portable. Fails on a loop.
# Each step is resolved physically (cd -P), as the kernel resolves it: a relative link's ".."
# climbs out of the directory the link really is in, never the one a symlinked path named.
real_path() {
    local p=$1 l n=0 d
    while [ -L "$p" ]; do
        n=$((n + 1))
        [ "$n" -le 40 ] || return 1
        l=$(readlink "$p") || return 1
        case $l in
            /*) p=$l ;;
            *) p=$(cd -P "$(dirname "$p")" 2>/dev/null && pwd -P)/$l || return 1 ;;
        esac
    done
    d=$(cd -P "$(dirname "$p")" 2>/dev/null && pwd -P) || return 1
    printf '%s/%s\n' "$d" "$(basename "$p")"
}
