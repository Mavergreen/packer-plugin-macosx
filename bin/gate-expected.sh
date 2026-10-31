#!/usr/bin/env bash
# platform: host-agnostic -- tested on Linux with KVM only
# Print the four boot-stack versions this checkout pins, one per line:
#
#   opencore <v>
#   lilu <v>
#   virtualsmc <v>
#   aquatransport <v>
#
# The gate passes these to the guest-side checks, which prove the booted
# guest runs exactly these. Exit 1, naming the pin, when one cannot be read.
#
#   usage: bin/gate-expected.sh [<repo-root>]
set -euo pipefail

GUEST_MACOSX_REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
export GUEST_MACOSX_REPO_ROOT
GUEST_MACOSX_LOG_PREFIX=gate-expected
export GUEST_MACOSX_LOG_PREFIX
# shellcheck source=../lib/common.sh
. "$GUEST_MACOSX_REPO_ROOT/lib/common.sh"
# shellcheck source=../lib/vendor.sh
. "$GUEST_MACOSX_REPO_ROOT/lib/vendor.sh"

if [ "$#" -gt 1 ]; then
    printf 'usage: %s [<repo-root>]\n' "${0##*/}" >&2
    exit 2
fi
root=${1:-$GUEST_MACOSX_REPO_ROOT}
tsv=$root/assets/pins/sources.tsv
hcl=$root/templates/mavericks-aquatransport/aquatransport.pkr.hcl

# release_segment <url>: the path segment above the file name, i.e. the tag
# in .../releases/download/<tag>/<file>.
release_segment() {
    local dir=${1%/*}
    printf '%s\n' "${dir##*/}"
}

url=$(source_field "$tsv" opencorepkg-src url) || exit 1
file=${url##*/}
opencore=${file%.tar.gz}
[ -n "$opencore" ] || die "opencorepkg-src: no version in $url"

url=$(source_field "$tsv" lilu-release url) || exit 1
lilu=$(release_segment "$url")
[ -n "$lilu" ] || die "lilu-release: no version in $url"

url=$(source_field "$tsv" virtualsmc-release url) || exit 1
virtualsmc=$(release_segment "$url")
[ -n "$virtualsmc" ] || die "virtualsmc-release: no version in $url"

[ -f "$hcl" ] || die "aquatransport_version: no such file: $hcl"
aquatransport=$(sed -n '/^[[:space:]]*aquatransport_version[[:space:]]*=/{s/[^"]*"\([^"]*\)".*/\1/p;q;}' "$hcl")
[ -n "$aquatransport" ] || die "aquatransport_version: not found in $hcl"

printf 'opencore %s\nlilu %s\nvirtualsmc %s\naquatransport %s\n' \
    "$opencore" "$lilu" "$virtualsmc" "$aquatransport"
