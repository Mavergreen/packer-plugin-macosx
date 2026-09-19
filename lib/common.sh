# shellcheck shell=bash
# Shared helpers. Sourced, never executed.
#
# Deliberately does not set shell options: a library that mutates the
# caller's environment behaves differently depending on who sourced it.

: "${MQG_LOG_PREFIX:=mqg}"

log()  { printf '%s: %s\n'            "$MQG_LOG_PREFIX" "$*" >&2; }
warn() { printf '%s: warning: %s\n'   "$MQG_LOG_PREFIX" "$*" >&2; }
die()  { printf '%s: error: %s\n'     "$MQG_LOG_PREFIX" "$*" >&2; exit 1; }

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
