#!/usr/bin/env bash
# platform: host-agnostic
# Whether ci.yml's go-macos job runs on a change: its go-macos-scope job,
# on Linux, so a change that needs no Mac spends none. Prints one line on
# stdout and exits 0:
#
#   run <reason>    go-macos runs
#   skip <reason>   it is skipped, and a skipped job passes the required
#                   check of its name
#
# It skips only when every file <head> changed since it forked from <base>
# is one no Go test reads: a *.md file, anything under docs/ or
# release-notes/, or .github/renovate.json. Anything else runs it, and so
# does a base that is not a commit (a push that made a branch, whose
# "before" is all zeros) or an empty diff: when it cannot tell, it runs.
#
#   usage: bin/go-macos-scope.sh <base-sha> <head-sha>
#   exit:  0 with the line above; 2 on usage
set -euo pipefail

GUEST_MACOSX_REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
export GUEST_MACOSX_REPO_ROOT
GUEST_MACOSX_LOG_PREFIX=go-macos-scope
export GUEST_MACOSX_LOG_PREFIX
# shellcheck source=../lib/common.sh
. "$GUEST_MACOSX_REPO_ROOT/lib/common.sh"

[ "$#" -eq 2 ] || { echo "usage: bin/go-macos-scope.sh <base-sha> <head-sha>" >&2; exit 2; }
base=$1
head=$2
cd "$GUEST_MACOSX_REPO_ROOT"

say() { printf '%s %s\n' "$1" "$2"; exit 0; }

if [ -z "$base" ] || ! git rev-parse --verify -q "$base^{commit}" >/dev/null; then
    say run "no base to compare with"
fi
changed=$(git diff --name-only "$base...$head") || say run "cannot diff $base...$head"
[ -n "$changed" ] || say run "no changed file to judge"

while IFS= read -r f; do
    case $f in
        *.md | docs/* | release-notes/* | .github/renovate.json) ;;
        *) say run "$f changed" ;;
    esac
done <<EOF
$changed
EOF
say skip "only docs, release notes or renovate.json changed"
