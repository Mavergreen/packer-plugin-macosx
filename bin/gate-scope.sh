#!/usr/bin/env bash
# platform: host-agnostic -- tested on Linux with KVM only
# Whether CI runs the boot gate on a change: .github/workflows/gate.yml's scope job. Prints one line
# on stdout and exits 0:
#
#   gated <reason>           the gate runs
#   not-gated <reason>       it does not, and the check `gate` passes saying why
#   pins-unfilled <reason>   a moved assets/pins/sources.tsv pin does not verify, so the gate would
#                            only fail fetching it; the check `gate` fails saying what to do
#
# The rules, in order:
#   - a release (event workflow_call, which gate.yml learns from its `caller` input, because a
#     called workflow's github.event_name is its caller's): gated, whatever changed
#     (decision 0017: every release is gated);
#   - a workflow_dispatch with GATE_FORCE=true: gated;
#   - a branch not renovate/*: not gated (decision 0017: a person runs the gate locally on
#     theirs);
#   - no changed file under a `gate` row of gate/scope.tsv: not gated, naming any `skip` row a
#     changed file fell under, and its reason;
#   - bin/verify-changed-sources.sh <base> fails: pins-unfilled;
#   - else gated, naming the gated paths that changed.
# What changed is what <head> changed since it forked from <base> (git diff <base>...<head>), not
# what moved on <base> since.
#
#   usage: bin/gate-scope.sh <base-sha> <head-sha> <branch> <event>
#   env:   GATE_FORCE (true on a forced dispatch); GUEST_MACOSX_GATE_VERIFY_SOURCES, the script run
#          in place of bin/verify-changed-sources.sh (a test seam)
#   exit:  0 with the line above; 2 on usage, or a base or head that is not a commit
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

case $event in
    workflow_call) say gated "a release" ;;
    workflow_dispatch) [ "${GATE_FORCE:-}" != true ] || say gated "dispatched with force" ;;
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
skipped=
while IFS=$'\t' read -r path what reason; do
    case $path in '#'*|'') continue ;; esac
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
        gate) gated="${gated:+$gated, }$path" ;;
        skip) skipped="$skipped; $path is not gated: $reason" ;;
        *) usage_die "$SCOPE: $path says '$what', not gate or skip" ;;
    esac
done < "$SCOPE"

[ -n "$gated" ] || say not-gated "no gated path changed$skipped"

log "changed: $gated; checking the moved pins verify against $base"
"$VERIFY_SOURCES" "$base" >&2 || say pins-unfilled "$PINS_UNFILLED"
say gated "changed: $gated"
