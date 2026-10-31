#!/usr/bin/env bash
# platform: host-agnostic
# Usage: shellcheck-workflows.sh [ROOT]
#
# Run shellcheck on every `run:` step of ROOT's GitHub workflows (default:
# the repository), each with the shell GitHub runs it with, as
# bin/workflow-scripts.sh extracts them, with each finding named by its
# workflow, its job and step, and the workflow's own line number: "In
# .github/workflows/ci.yml (job go, step 5 (Cross-build)) line 130:".
# SC1091 is left out, as in bin/run-tests.sh's own shellcheck step. Fails on
# any finding, and on any step bin/workflow-scripts.sh refuses.
#
# Not bash32-check: these run on GitHub's runners, whose bash is current,
# never on a 10.9 host.
set -u
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
root=${1:-$here/..}
tmp=$(mktemp -d "${TMPDIR:-/tmp}/shellcheck-workflows.XXXXXX") || exit 2
trap 'rm -rf "$tmp"' EXIT

status=0
manifest=$("$here/workflow-scripts.sh" "$root" "$tmp/steps") || status=1
n=0
while IFS="$(printf '\t')" read -r wf _line label shell f; do
    [ -n "$wf" ] || continue
    n=$((n + 1))
    if ! report=$(shellcheck -s "$shell" -e SC1091 "$f" 2>&1); then
        status=1
        printf '%s\n' "${report//"$f"/"$wf ($label)"}"
    fi
done <<< "$manifest"

if [ "$status" -eq 0 ]; then
    echo "$n workflow run: step(s) pass shellcheck"
fi
exit "$status"
