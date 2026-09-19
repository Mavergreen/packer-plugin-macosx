#!/usr/bin/env bash
# Run the whole test suite. shellcheck is optional: it is not installed on
# every host, and needing a package install to run tests is a bad trade.
# bats is mandatory: it is how this script runs the suite at all.
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

status=0

echo "== bats =="
if ! command -v bats >/dev/null 2>&1; then
    echo "bats not found. Install bats-core (e.g. 'sudo apt install bats'," \
        "or see https://github.com/bats-core/bats-core) and re-run." >&2
    status=1
else
    # Tee, so the skip count can be reported. A skipped test is not a
    # passing test, and bats' own summary line does not distinguish them
    # in a way anyone reads at a glance -- "550 ok" looks identical
    # whether fourteen of them ran or not.
    #
    # An opt-in test -- one that mounts under /run/media/$USER and pops a
    # desktop window, say -- skips on every host that has not opted in,
    # and reads as a pass unless the skips are reported. The count should
    # stay at zero; this is here so that an opt-in test cannot hide.
    batslog=$(mktemp)
    if ! bats tests/ | tee "$batslog"; then
        status=1
    fi
    skipped=$(grep -c '# skip' "$batslog" || true)
    if [ "${skipped:-0}" -gt 0 ]; then
        echo
        echo "$skipped test(s) SKIPPED, not run:"
        grep '# skip' "$batslog" | sed -e 's/^ok [0-9]* /  /' | sort -u
    fi
    rm -f "$batslog"
fi

echo
echo "== shellcheck =="
if command -v shellcheck >/dev/null 2>&1; then
    # Collect scripts by walking the tree rather than a hardcoded glob list,
    # so new script directories are picked up automatically and the check
    # still runs on files that exist but aren't `git add`ed yet.
    # `while read` rather than `mapfile`, which is bash 4 -- see
    # bin/bash32-check.sh.
    sh_files=()
    while IFS= read -r sh_file; do
        [ -n "$sh_file" ] || continue
        sh_files+=("$sh_file")
    done < <(
        find . -path ./.git -prune -o -name '*.sh' -print
    )
    # SC1091: shellcheck cannot follow dynamically-computed source paths.
    if [ "${#sh_files[@]}" -gt 0 ]; then
        if ! shellcheck -e SC1091 "${sh_files[@]}"; then
            status=1
        fi
    fi
else
    echo "shellcheck not installed; skipping."
    echo "Install it to lint shell scripts locally: sudo apt install shellcheck"
fi

echo
echo "== bash32-check =="
# No shell file here may use a bash feature newer than 3.2, which is what
# stock OS X 10.9 ships in /bin. See the comment at the top of that script
# for why the floor exists and what it does *not* mean.
#
# Unlike shellcheck, this is mandatory: it needs nothing installed, and a
# rule enforced only where a tool happens to be present is a rule that
# gets broken on the host that lacks it. It fails the suite rather than
# warning -- a warning in a green run is a warning nobody reads.
if ! "$repo_root/bin/bash32-check.sh"; then
    status=1
fi

exit "$status"
