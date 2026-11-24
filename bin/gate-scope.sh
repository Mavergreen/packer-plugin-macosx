#!/usr/bin/env bash
# platform: host-agnostic -- tested on Linux with KVM only
# Whether CI runs the boot gate on a change, and on which releases: .github/workflows/gate.yml's
# scope job. Prints one line on stdout and exits 0:
#
#   gated <reason>           the gate runs
#   not-gated <reason>       it does not, and the check `gate` passes saying why
#   pins-unfilled <reason>   a moved assets/pins/sources.tsv pin does not verify, so the gate would
#                            only fail fetching it; the check `gate` fails saying what to do
#
# When gated, a second line names the releases the gate boots, in GUEST_MACOSX_RELEASES order:
#
#   releases <r> [<r>...]    every release for a release or a forced dispatch; otherwise those of
#                            the gate:<releases> rows a changed file fell under
#
# The rules, in order:
#   - a release (event workflow_call, which gate.yml learns from its `caller` input, because a
#     called workflow's github.event_name is its caller's): gated, whatever changed
#     (decision 0017: every release is gated);
#   - a workflow_dispatch with GATE_FORCE=true: gated;
#   - a branch not renovate/*: not gated (decision 0017: a person runs the gate locally on
#     theirs);
#   - no changed file under a `gate:` row of gate/scope.tsv: not gated, naming any `skip` row a
#     changed file fell under, and its reason;
#   - bin/verify-changed-sources.sh <base> fails: pins-unfilled;
#   - else gated, naming the gated paths that changed.
# What changed is what <head> changed since it forked from <base> (git diff <base>...<head>), not
# what moved on <base> since.
#
#   usage: bin/gate-scope.sh <base-sha> <head-sha> <branch> <event>
#   env:   GATE_FORCE (true on a forced dispatch); GUEST_MACOSX_GATE_VERIFY_SOURCES, the script run
#          in place of bin/verify-changed-sources.sh (a test seam)
#   exit:  0 with the lines above; 2 on usage, a base or head that is not a commit, or a scope.tsv
#          row that is neither skip nor gate: with releases each in GUEST_MACOSX_RELEASES
set -euo pipefail

GUEST_MACOSX_REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
export GUEST_MACOSX_REPO_ROOT
GUEST_MACOSX_LOG_PREFIX=gate-scope
export GUEST_MACOSX_LOG_PREFIX
# shellcheck source=../lib/common.sh
. "$GUEST_MACOSX_REPO_ROOT/lib/common.sh"

VERIFY_SOURCES=${GUEST_MACOSX_GATE_VERIFY_SOURCES:-$GUEST_MACOSX_REPO_ROOT/bin/verify-changed-sources.sh}
SCOPE=$GUEST_MACOSX_REPO_ROOT/gate/scope.tsv
PINS_UNFILLED='re-pin the changed assets/pins/sources.tsv row on this branch (bin/verify-changed-sources.sh names it); the pins bot is not built yet'

usage_die() { printf '%s: %s\n' "$GUEST_MACOSX_LOG_PREFIX" "$*" >&2; exit 2; }

[ "$#" -eq 4 ] || usage_die "usage: bin/gate-scope.sh <base-sha> <head-sha> <branch> <event>"
base=$1
head=$2
branch=$3
event=$4

cd "$GUEST_MACOSX_REPO_ROOT"

say() { printf '%s %s\n' "$1" "$2"; exit 0; }

# gated_on <reason> <releases>: say gated, then the releases among <releases> (comma-separated)
# in GUEST_MACOSX_RELEASES order, so the order never depends on which row came first.
gated_on() {
    local r out=
    for r in $GUEST_MACOSX_RELEASES; do
        case ",$2," in *",$r,"*) out="$out $r" ;; esac
    done
    printf 'gated %s\nreleases%s\n' "$1" "$out"
    exit 0
}

all_releases=$(printf '%s' "$GUEST_MACOSX_RELEASES" | tr ' ' ,)
case $event in
    workflow_call) gated_on "a release" "$all_releases" ;;
    workflow_dispatch) [ "${GATE_FORCE:-}" != true ] || gated_on "dispatched with force" "$all_releases" ;;
esac
case $branch in
    renovate/*) ;;
    *) say not-gated "not a Renovate update" ;;
esac

for c in "$base" "$head"; do
    git rev-parse -q --verify "$c^{commit}" >/dev/null || usage_die "$c is not a commit here"
done
changed=$(git diff --name-only "$base...$head") || usage_die "cannot diff $base...$head"

[ -f "$SCOPE" ] || usage_die "$SCOPE: no such file"
gated=
releases=
skipped=
while IFS=$'\t' read -r path what reason; do
    case $path in '#'*|'') continue ;; esac
    # Every row is checked, not only those a change fell under, so a bad row fails the first run
    # after it lands rather than waiting for a change to its path.
    case $what in
        skip) ;;
        gate:?*)
            # Each comma-separated release, an empty one (a doubled or trailing comma) included.
            rest="${what#gate:},"
            while [ -n "$rest" ]; do
                r=${rest%%,*}
                rest=${rest#*,}
                known_release "$r" || usage_die "$SCOPE: $path says '$what': '$r' is not one of $(releases_said)"
            done ;;
        *) usage_die "$SCOPE: $path says '$what', not skip or gate:<releases>" ;;
    esac
    hit=false
    while IFS= read -r f; do
        [ -n "$f" ] || continue
        # shellcheck disable=SC2254  # the row's path is a glob, matched as one on purpose
        case $f in $path) hit=true; break ;; esac
    done <<EOF
$changed
EOF
    [ "$hit" = true ] || continue
    case $what in
        gate:*) gated="${gated:+$gated, }$path"; releases="$releases,${what#gate:}" ;;
        skip) skipped="$skipped; $path is not gated: $reason" ;;
    esac
done < "$SCOPE"

[ -n "$gated" ] || say not-gated "no gated path changed$skipped"

log "changed: $gated; checking the moved pins verify against $base"
"$VERIFY_SOURCES" "$base" >&2 || say pins-unfilled "$PINS_UNFILLED"
gated_on "changed: $gated" "$releases"
