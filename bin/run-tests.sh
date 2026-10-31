#!/usr/bin/env bash
# platform: host-agnostic
# Run the whole test suite. shellcheck is optional: it is not installed on
# every host, and needing a package install to run tests is a bad trade.
# bats is mandatory: it is how this script runs the suite at all.
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

status=0

# No test may hang: a loop that lost its bound must show up as a red test,
# by name, not as a run that never ends. bats fails any test that runs
# longer than BATS_TEST_TIMEOUT seconds, but kills only that test's direct
# children, and a grandchild that loops on (one under `run`) holds the run
# open anyway; so bin/bats-watchdog.sh, 10 s sooner, ends such a test and
# everything it started, and names it. The slowest test took 20 s on the
# primary host (`bats --timing tests/`, 2026-10-19); six times that leaves
# room for slower runners and parallel jobs. A caller's own value wins,
# even an empty one, which is bats' "no timeout" (and the watchdog's "no
# limit"). CI's test step does the same (tests/test_timeout.bats holds them
# equal).
export BATS_TEST_TIMEOUT="${BATS_TEST_TIMEOUT-120}"

echo "== bats =="
# GUEST_MACOSX_SKIP_BATS=1: CI runs the suite through shipyard's run-repo-tests.sh and
# wants only the lint half from here.
if [ "${GUEST_MACOSX_SKIP_BATS:-0}" = 1 ]; then
    echo "skipped (GUEST_MACOSX_SKIP_BATS=1)"
elif ! command -v bats >/dev/null 2>&1; then
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
    batslog=$(mktemp "${TMPDIR:-/tmp}/run-tests.XXXXXX")
    # Under the watchdog, which fails the run if it cannot watch it, or
    # ended anything (a test reported ok can have left a job running).
    if ! "$repo_root/bin/bats-watchdog.sh" --run bats tests/ | tee "$batslog"; then
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
    # Collect scripts rather than from a hardcoded glob list, so new script
    # directories are picked up automatically and the check still runs on
    # files that exist but aren't `git add`ed yet; gitignored scratch is
    # skipped, and outside a git checkout the tree is walked; an
    # extensionless script is found by its shebang (bin/list-shell-files.sh).
    # `while read` rather than `mapfile`, which is bash 4 -- see
    # bin/bash32-check.sh.
    sh_files=()
    if ! sh_list=$("$repo_root/bin/list-shell-files.sh" "$repo_root"); then
        status=1 # it said why
    fi
    while IFS= read -r sh_file; do
        [ -n "$sh_file" ] || continue
        sh_files+=("$sh_file")
    done <<< "$sh_list"
    # SC1091: shellcheck cannot follow dynamically-computed source paths.
    if [ "${#sh_files[@]}" -eq 0 ]; then
        echo "no shell files found"
        status=1
    else
        if ! shellcheck -e SC1091 "${sh_files[@]}"; then
            status=1
        fi
    fi
    # The shell scripts embedded in those files as heredocs (the guest's
    # shutdown helper and sshd-keygen wrapper, the suites' stubs), each
    # linted as the script it is, its findings at the host's own lines.
    if ! "$repo_root/bin/shellcheck-heredocs.sh" "$repo_root"; then
        status=1
    fi
    # And the workflows' run: steps, each with the shell GitHub runs it with.
    if ! "$repo_root/bin/shellcheck-workflows.sh" "$repo_root"; then
        status=1
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

echo
echo "== bats-bang-check =="
# No test may have a `! cmd` line before its last: bats runs a test under
# `set -e`, which ignores a negated pipeline, so such a line checks
# nothing. Mandatory, like bash32-check, and for the same reasons.
if ! "$repo_root/bin/bats-bang-check.sh"; then
    status=1
fi

exit "$status"
