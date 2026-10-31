#!/usr/bin/env bats
# platform: host-agnostic
#
# bin/bash32-check.sh is the gate that keeps this codebase runnable under
# the bash 3.2 that stock OS X 10.9 ships in /bin. A gate nobody has seen
# fire is a gate nobody knows works, so most of what follows is about
# making it fire.
#
# Fixtures are built one line at a time through _line, rather than with a
# heredoc or a multi-line printf, for a reason that is easy to miss: the
# violating strings have to sit on lines that can each carry a trailing
# `# bash32-allow` marker. Without it this file -- which necessarily spells
# out every construct the check looks for -- would be the first thing the
# check reported, and the suite could never go green. The marker cannot
# instead go inside the fixture text, because then the check would skip the
# fixture line and the test would pass for the wrong reason.

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    SCAN="$BATS_TEST_TMPDIR/scan"
    mkdir -p "$SCAN"
}

# _line <fixture name> <line> -- append one line to a fixture file.
_line() { printf '%s\n' "$2" >> "$SCAN/$1"; }

# Run the check over $SCAN rather than over the repository.
_check() {
    run env GUEST_MACOSX_BASH32_ROOT="$SCAN" "$REPO/bin/bash32-check.sh"
}

@test "bash32-check passes on this repository" {
    run "$REPO/bin/bash32-check.sh"
    [ "$status" -eq 0 ]
    [[ "$output" == *"nothing newer than bash 3.2"* ]] || false
}

@test "bash32-check fails on mapfile" {  # bash32-allow (the title names it; bats tolerates this)
    _line bad.sh '#!/usr/bin/env bash'
    _line bad.sh 'mapfile -t x < <(echo hi)'                   # bash32-allow
    _check
    [ "$status" -ne 0 ]
    [[ "$output" == *"bad.sh:2"* ]] || false
    [[ "$output" == *"read -r"* ]] || false
}

@test "bash32-check fails on readarray, mapfile's other name" {  # bash32-allow (the title names it; bats tolerates this)
    _line bad.sh '#!/usr/bin/env bash'
    _line bad.sh 'readarray -t x < f'                          # bash32-allow
    _check
    [ "$status" -ne 0 ]
    [[ "$output" == *"bad.sh:2"* ]] || false
}

@test "bash32-check fails on an associative array" {
    _line bad.sh '#!/usr/bin/env bash'
    _line bad.sh 'declare -A m=()'                             # bash32-allow
    _check
    [ "$status" -ne 0 ]
    [[ "$output" == *"associative array"* ]] || false
}

@test "bash32-check fails on a nameref" {
    _line bad.sh '#!/usr/bin/env bash'
    _line bad.sh '    local -n ref=$1'                         # bash32-allow
    _check
    [ "$status" -ne 0 ]
    [[ "$output" == *"nameref"* ]] || false
}

@test "bash32-check fails on case-modifying expansion" {
    _line bad.sh '#!/usr/bin/env bash'
    _line bad.sh 'u=${v^^}'                                    # bash32-allow
    _check
    [ "$status" -ne 0 ]
    [[ "$output" == *"uppercase"* ]] || false
}

@test "bash32-check checks .bats files too" {
    # The test suite is shell as much as the scripts are, and a bats file
    # that only ran under bash 4 would break a 10.9 host just as surely.
    _line bad.bats '@test "x" {'
    _line bad.bats '    mapfile -t e < <(echo hi)'             # bash32-allow
    _line bad.bats '}'
    _check
    [ "$status" -ne 0 ]
    [[ "$output" == *"bad.bats"* ]] || false
}

@test "bash32-check ignores a comment that merely mentions mapfile" {  # bash32-allow (the title names it; bats tolerates this)
    # Otherwise the very comments explaining why a file stopped using
    # mapfile would keep the check red forever.
    _line ok.sh '#!/usr/bin/env bash'
    _line ok.sh '# mapfile is bash 4, and so is declare -A.'   # bash32-allow
    _line ok.sh '    # an indented comment counts as one too'
    _line ok.sh 'x=1'
    _check
    [ "$status" -eq 0 ]
}

@test "bash32-check honours the bash32-allow marker" {
    _line ok.sh '#!/usr/bin/env bash'
    _line ok.sh 'p=mapfile  # bash32-allow: named, not called'  # bash32-allow
    _check
    [ "$status" -eq 0 ]
}

@test "bash32-check does not fire on bash 3.2 constructs it must allow" {
    # [[ =~ ]] is bash 3.0, arrays are bash 2, `+=` on an array is 3.1, and
    # the replacement idiom itself had better pass.
    _line ok.sh '#!/usr/bin/env bash'
    _line ok.sh 'x=()'
    _line ok.sh 'while IFS= read -r l; do'
    _line ok.sh '    [ -n "$l" ] || continue'
    _line ok.sh '    x+=("$l")'
    _line ok.sh 'done < <(echo hi)'
    _line ok.sh '[[ $l =~ ^h ]] && printf "%s\n" "${x[@]}"'
    _line ok.sh 'local -a y=()'
    _line ok.sh 'declare -r z=1'
    _line ok.sh 'case $x in a) ;; b) ;; esac'
    _check
    [ "$status" -eq 0 ]
}

@test "bash32-check reports a violation late in a long file" {
    # The shape of the bug that made an earlier gate in this repository
    # fail open: a match found only after the writer had produced a lot of
    # output, where a pipe into `grep -q` turns a successful match into
    # exit 141 and a false negative. This check matches in-process so that
    # length cannot change the answer, and this test is what says so.
    _line long.sh '#!/usr/bin/env bash'
    for i in $(seq 1 2000); do _line long.sh "x=$i"; done
    _line long.sh 'declare -A late=()'                         # bash32-allow
    _check
    [ "$status" -ne 0 ]
    [[ "$output" == *"long.sh:2002"* ]] || false
}

@test "bash32-check reports every violating file, not just the first" {
    _line one.sh '#!/usr/bin/env bash'
    _line one.sh 'mapfile -t a < f'                            # bash32-allow
    _line two.sh '#!/usr/bin/env bash'
    _line two.sh 'declare -A b=()'                             # bash32-allow
    _check
    [ "$status" -ne 0 ]
    [[ "$output" == *"one.sh"* ]] || false
    [[ "$output" == *"two.sh"* ]] || false
}

@test "bash32-check skips a gitignored file in a git checkout" {
    # The check scans what the lint pass scans (bin/list-shell-files.sh):
    # gitignored scratch -- agents' scripts, a findings directory -- is not
    # the project's code, and walking it made the check read hundreds of
    # files a clean clone does not have.
    git -C "$SCAN" init -q
    printf 'scratch/\n' > "$SCAN/.gitignore"
    mkdir -p "$SCAN/scratch"
    _line ok.sh '#!/usr/bin/env bash'
    _line scratch/ignored.sh '#!/usr/bin/env bash'
    _line scratch/ignored.sh 'mapfile -t x < f'                 # bash32-allow
    _check
    [ "$status" -eq 0 ]
    [[ "$output" != *"ignored.sh"* ]] || false
}

@test "bash32-check reports an untracked, not-ignored file in a git checkout" {
    # A script not yet `git add`ed is still the project's code.
    git -C "$SCAN" init -q
    printf 'scratch/\n' > "$SCAN/.gitignore"
    _line tracked.sh '#!/usr/bin/env bash'
    git -C "$SCAN" add tracked.sh
    _line unadded.bash '#!/usr/bin/env bash'
    _line unadded.bash 'declare -A m=()'                        # bash32-allow
    _check
    [ "$status" -ne 0 ]
    [[ "$output" == *"unadded.bash:2"* ]] || false
}

@test "bash32-check works with its scan root inside another repository, which ignores it" {
    # As when TMPDIR is inside a work tree (a dotfiles $HOME, a
    # repository's gitignored scratch): the enclosing repository's rules
    # must not empty the listing, or every check above "checked nothing".
    E="$BATS_TEST_TMPDIR/enclosing"
    mkdir -p "$E/ignored/scan"
    git -C "$E" init -q
    printf 'ignored/\n' > "$E/.gitignore"
    sb='#!/usr/bin/env bash' # the matcher's data, not a stub (bin/heredoc-scripts.sh)
    printf '%s\n' "$sb" > "$E/ignored/scan/ok.sh"
    run env GUEST_MACOSX_BASH32_ROOT="$E/ignored/scan" "$REPO/bin/bash32-check.sh"
    [ "$status" -eq 0 ]
    [[ "$output" == *"1 shell files use nothing newer"* ]] || false
    _line bad.sh '#!/usr/bin/env bash'
    _line bad.sh 'declare -A m=()'                             # bash32-allow
    cp "$SCAN/bad.sh" "$E/ignored/scan/"
    run env GUEST_MACOSX_BASH32_ROOT="$E/ignored/scan" "$REPO/bin/bash32-check.sh"
    [ "$status" -ne 0 ]
    [[ "$output" == *"bad.sh:2"* ]] || false
}

@test "this file's fixture checks pass with TMPDIR inside a work tree that ignores it" {
    # The scenario itself: run two of the checks above, through bats, with
    # their BATS_TEST_TMPDIR (and so $SCAN) inside a repository's
    # gitignored scratch.
    E="$BATS_TEST_TMPDIR/enclosing"
    mkdir -p "$E/ignored"
    git -C "$E" init -q
    printf 'ignored/\n' > "$E/.gitignore"
    run env TMPDIR="$E/ignored" bats -f 'fails on mapfile$|reports every violating file' "$BATS_TEST_FILENAME"  # bash32-allow
    [ "$status" -eq 0 ]
    [[ "$output" == *"ok 1 "* ]] || false
    [[ "$output" == *"ok 2 "* ]] || false
    [[ "$output" != *"not ok"* ]] || false
}

@test "bash32-check fails rather than passing when it checked nothing" {
    # An empty result must not read as a clean result: that is how a gate
    # quietly stops guarding anything.
    _check
    [ "$status" -ne 0 ]
    [[ "$output" == *"nothing was checked"* ]] || false
}

@test "the test suite runs bash32-check" {
    # The rule is only a rule if the suite enforces it. A check that has to
    # be remembered is a check that will not be.
    grep -q 'bin/bash32-check.sh' "$REPO/bin/run-tests.sh"
}
