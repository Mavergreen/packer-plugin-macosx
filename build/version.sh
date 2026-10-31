#!/usr/bin/env bash
# platform: host-agnostic
# The version scheme: YYYYMMDD.N, tagged v0.YYYYMMDD.N.
#
# spec: docs/decisions/0012-version-scheme.md
#
# This product is its OWN upstream -- it is not a repackage of somebody
# else's release -- so it takes the family's self-upstream shape
# (mavericks-porthole, mavericks-magic-trackpad2) and drops the
# -mavericks suffix, which has no slot to fill. UPSTREAM_VERSION holds
# this product's own version line as a date; N counts the releases cut
# on that line, INCLUDING ingredient-only repackages. So the date is not
# the release date -- it is the version line's name.
#
# The family's shared scripts/version.sh and resolve-version.sh are not
# usable here: both hardcode the literal "-mavericks." that a repackage
# of someone else's release needs. mavericks-porthole inlines the equivalent logic in its
# release.yml; ours is a committed script instead, so that it can be
# tested (tests/version.bats).
#
# The TAG is that version as Packer's semver: v0.<line>.<N>. packer init
# resolves a plugin by a three-component semver tag, and a 0 major keeps
# the date out of the major slot (Go reads a major of 2 or more as a /vN
# module path). The family version and the tag can never disagree: the
# tag is "v0." and the version, and N is counted from those tags alone.
#
# A DEV BUILD (bin/dev-install.sh) is no release: it takes the N no
# release ever takes, DEV_N, and stays -dev. Packer matches a -dev plugin
# by its version without the -dev and runs a release of that version over
# it, so a dev build at any release's N, past or future, would run that
# release in its place; at DEV_N it outranks every release on its line,
# and a template's ~> 0.<line>.1 still takes it.
set -eu

DEV_N=999999

MAVERICKS_ROOT=${MAVERICKS_ROOT:-$(cd "$(dirname "$0")/.." && pwd)}
export MAVERICKS_ROOT

mode=${1:-}
case $mode in
    auto|local|dev) ;;
    *) printf 'version.sh: usage: version.sh <auto|local|dev>\n' >&2
       printf '  auto   this version line, releasing only if it has no tag yet\n' >&2
       printf '  local  the next N on this line, always releasing (a repackage)\n' >&2
       printf '  dev    a dev build'"'"'s, N=%s, which no release takes; never releasing\n' "$DEV_N" >&2
       exit 2 ;;
esac

uv=$MAVERICKS_ROOT/UPSTREAM_VERSION
[ -f "$uv" ] || { printf 'version.sh: no %s\n' "$uv" >&2; exit 1; }

base=$(tr -d '[:space:]' < "$uv")
if [ -z "$base" ]; then
    printf 'version.sh: %s is empty -- it must hold this product'"'"'s own version line as YYYYMMDD\n' "$uv" >&2
    exit 1
fi
case $base in
    [0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]) ;;
    *) printf 'version.sh: %s reads "%s"; it must be a bare YYYYMMDD date\n' "$uv" "$base" >&2
       exit 1 ;;
esac

# The highest N already tagged on this line. Arithmetic, not sort -V:
# sort -V is one of the two constructs shipyard shipped that the 10.9
# base system lacks (check-shell-portability.sh), and lexical comparison
# would put .10 before .2.
maxn=0
for t in $(git -C "$MAVERICKS_ROOT" tag --list "v0.$base.*" 2>/dev/null); do
    n=${t##*.}
    case $n in
        ''|*[!0-9]*) continue ;;
    esac
    [ "$n" -gt "$maxn" ] && maxn=$n
done

if [ "$mode" = dev ]; then
    printf 'FULL=%s.%s\nTAG=v0.%s.%s-dev\nRELEASE=no\n' "$base" "$DEV_N" "$base" "$DEV_N"
    exit 0
elif [ "$mode" = local ]; then
    n=$((maxn + 1)); release=yes
elif [ "$maxn" -eq 0 ]; then
    n=1; release=yes
else
    n=$maxn; release=no
fi

if [ "$n" -ge "$DEV_N" ]; then
    printf 'version.sh: N would be %s on line %s, and %s and above are the dev builds'"'"' (docs/decisions/0012)\n' "$n" "$base" "$DEV_N" >&2
    exit 1
fi

full="$base.$n"
# VERSION_NO_WRITE=1 computes and prints but writes nothing: reporting a
# version is a read, and a read-only checkout must still answer (MEASURED:
# a `chmod a-w` clone failed with "cannot create .../VERSION: Permission
# denied"). The release path leaves it unset, and VERSION is written as
# before.
if [ "${VERSION_NO_WRITE:-0}" != 1 ]; then
    printf '%s\n' "$full" > "$MAVERICKS_ROOT/VERSION"
fi
printf 'FULL=%s\nTAG=v0.%s\nRELEASE=%s\n' "$full" "$full" "$release"
