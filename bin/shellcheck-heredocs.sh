#!/usr/bin/env bash
# platform: host-agnostic
# Usage: shellcheck-heredocs.sh [ROOT]
#
# Run shellcheck on every shell script embedded as a heredoc in ROOT's shell
# files (default: the repository), as bin/heredoc-scripts.sh finds them, with
# each finding named by its host file, its delimiter and the host's own
# line number: "In assets/guest/firstboot.sh (heredoc
# GUEST_MACOSX_SHUTDOWN_EOF) line 1290:". SC1091 is left out, as in
# bin/run-tests.sh's own shellcheck step. Fails on any finding, and on any
# heredoc bin/heredoc-scripts.sh refuses (an unquoted one, an unterminated
# one).
set -u
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
root=${1:-$here/..}
tmp=$(mktemp -d "${TMPDIR:-/tmp}/shellcheck-heredocs.XXXXXX") || exit 2
trap 'rm -rf "$tmp"' EXIT

status=0
manifest=$("$here/heredoc-scripts.sh" "$root" "$tmp/scripts") || status=1
n=0
while IFS="$(printf '\t')" read -r host _line delim shell f; do
    [ -n "$host" ] || continue
    n=$((n + 1))
    label="$host (heredoc $delim)"
    # cd beside it, so its copied .shellcheckrc (the host's) applies.
    if ! report=$(cd "$(dirname "$f")" && shellcheck -s "$shell" -e SC1091 "$f" 2>&1); then
        status=1
        printf '%s\n' "${report//"$f"/"$label"}"
    fi
done <<< "$manifest"

if [ "$status" -eq 0 ]; then
    echo "$n embedded shell script(s) pass shellcheck"
fi
exit "$status"
