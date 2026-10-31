#!/usr/bin/env bats
# platform: host-agnostic
#
# bin/bats-bang-check.sh refuses a `! cmd` line in the middle of a test.
# bats runs a test under `set -e`, and bash's `set -e` ignores a negated
# pipeline, so a mid-test `! grep -q x f` that finds x fails nothing: the
# test goes on and passes. Only a test's last command counts, because bats
# checks the test's status. Write `! cmd || false`, or `run cmd` with a
# status check, instead.
#
# Fixtures are built one line at a time through _line, so that no line of
# this file begins with the bang the check looks for.

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    SCAN="$BATS_TEST_TMPDIR/scan"
    mkdir -p "$SCAN"
}

# _line <fixture name> <line> -- append one line to a fixture file.
_line() { printf '%s\n' "$2" >> "$SCAN/$1"; }

# Run the check over $SCAN rather than over the repository.
_check() {
    run env GUEST_MACOSX_BATS_BANG_ROOT="$SCAN" "$REPO/bin/bats-bang-check.sh"
}

@test "the premise: under bats, a mid-test bang that is false fails nothing" {
    _line premise.bats '@test "x" {'
    _line premise.bats '    ! true'
    _line premise.bats '    true'
    _line premise.bats '}'
    run bats "$SCAN/premise.bats"
    [ "$status" -eq 0 ]
}

@test "bats-bang-check passes on this repository" {
    run "$REPO/bin/bats-bang-check.sh"
    [ "$status" -eq 0 ] || { echo "$output"; false; }
    [[ "$output" == *"no mid-test bang"* ]] || false
}

@test "bats-bang-check fails on a bang in the middle of a test, naming the line" {
    _line bad.bats '@test "x" {'
    _line bad.bats '    true'
    _line bad.bats '    ! grep -q x /dev/null'
    _line bad.bats '    true'
    _line bad.bats '}'
    _check
    [ "$status" -ne 0 ]
    [[ "$output" == *"bad.bats:3:"* ]] || false
    [[ "$output" == *"|| false"* ]] || false
}

@test "a bang as a test's last command is fine: bats checks its status" {
    _line ok.bats '@test "x" {'
    _line ok.bats '    true'
    _line ok.bats '    ! grep -q x /dev/null'
    _line ok.bats ''
    _line ok.bats '    # a comment after it'
    _line ok.bats '}'
    _check
    [ "$status" -eq 0 ] || { echo "$output"; false; }
}

@test "a bang with a fallback that fails is fine" {
    _line ok.bats '@test "x" {'
    _line ok.bats '    ! grep -q x /dev/null || false'
    _line ok.bats '    ! grep -q x /dev/null || return 1'
    _line ok.bats '    ! grep -q x /dev/null || { echo "x is there" >&2; return 1; }'
    _line ok.bats '    true'
    _line ok.bats '}'
    _check
    [ "$status" -eq 0 ] || { echo "$output"; false; }
}

@test "a fallback that does not fail is not one" {
    _line bad.bats '@test "x" {'
    _line bad.bats '    ! grep -q x /dev/null || true'
    _line bad.bats '    true'
    _line bad.bats '}'
    _check
    [ "$status" -ne 0 ]
    [[ "$output" == *"bad.bats:2:"* ]] || false
}

@test "the bangs bash honours are fine: an if, a while, a test, run !" {
    _line ok.bats '@test "x" {'
    _line ok.bats '    if ! grep -q x /dev/null; then true; fi'
    _line ok.bats '    while ! true; do false; done'
    _line ok.bats '    [ ! -e /nonexistent ]'
    _line ok.bats '    [[ ! -e /nonexistent ]]'
    _line ok.bats '    run ! grep -q x /dev/null'
    _line ok.bats '    if [ -n x ] &&'
    _line ok.bats '        ! grep -q x /dev/null; then'
    _line ok.bats '        true'
    _line ok.bats '    fi'
    _line ok.bats '    true'
    _line ok.bats '}'
    _check
    [ "$status" -eq 0 ] || { echo "$output"; false; }
}

@test "a bang after && or ; in the middle of a test is refused too" {
    _line bad.bats '@test "x" {'
    _line bad.bats '    true && ! grep -q x /dev/null'
    _line bad.bats '    true; ! grep -q x /dev/null'
    _line bad.bats '    true'
    _line bad.bats '}'
    _check
    [ "$status" -ne 0 ]
    [[ "$output" == *"bad.bats:2:"* ]] || false
    [[ "$output" == *"bad.bats:3:"* ]] || false
}

@test "a bang on a continued line is judged with the line it continues" {
    _line bad.bats '@test "x" {'
    _line bad.bats '    ! grep -q x \'
    _line bad.bats '        /dev/null'
    _line bad.bats '    true'
    _line bad.bats '}'
    _line ok.bats '@test "y" {'
    _line ok.bats '    ! grep -q x \'
    _line ok.bats '        /dev/null || false'
    _line ok.bats '    true'
    _line ok.bats '}'
    _check
    [ "$status" -ne 0 ]
    [[ "$output" == *"bad.bats:2:"* ]] || false
    [[ "$output" != *"ok.bats"* ]] || false
}

@test "a bang mid-loop is refused even as the loop ends the test: only the last pass counts" {
    _line bad.bats '@test "x" {'
    _line bad.bats '    for i in 1 2; do'
    _line bad.bats '        ! grep -q x /dev/null'
    _line bad.bats '    done'
    _line bad.bats '}'
    _check
    [ "$status" -ne 0 ]
    [[ "$output" == *"bad.bats:3:"* ]] || false
}

@test "a helper function's mid-body bang is refused; its last line is its status" {
    _line bad.bats 'helper() {'
    _line bad.bats '    ! grep -q x /dev/null'
    _line bad.bats '    true'
    _line bad.bats '}'
    _line ok.bats 'helper() {'
    _line ok.bats '    true'
    _line ok.bats '    ! grep -q x /dev/null'
    _line ok.bats '}'
    _check
    [ "$status" -ne 0 ]
    [[ "$output" == *"bad.bats:2:"* ]] || false
    [[ "$output" != *"ok.bats"* ]] || false
}

@test "a heredoc's body is another script, not the test's" {
    h='<<' # not one here: the check would read this file's rest as its body
    _line ok.bats '@test "x" {'
    _line ok.bats "    cat > stub $h'EOF'"
    _line ok.bats '! grep -q x /dev/null'
    _line ok.bats 'true'
    _line ok.bats 'EOF'
    _line ok.bats "    cat > stub2 $h-EOF"
    _line ok.bats '	! grep -q x /dev/null'
    _line ok.bats '	true'
    _line ok.bats '	EOF'
    _line ok.bats '    true'
    _line ok.bats '}'
    _check
    [ "$status" -eq 0 ] || { echo "$output"; false; }
}

@test "a line outside any test or function is not judged" {
    _line ok.bats '! true'
    _line ok.bats 'true'
    _check
    [ "$status" -eq 0 ] || { echo "$output"; false; }
}

@test "the bats-bang-allow marker skips a line" {
    _line ok.bats '@test "x" {'
    _line ok.bats '    ! grep -q x /dev/null  # bats-bang-allow: why'
    _line ok.bats '    true'
    _line ok.bats '}'
    _check
    [ "$status" -eq 0 ] || { echo "$output"; false; }
}

@test "bats-bang-check reports every violating file, not just the first" {
    for f in a b; do
        _line "$f.bats" '@test "x" {'
        _line "$f.bats" '    ! true'
        _line "$f.bats" '    true'
        _line "$f.bats" '}'
    done
    _check
    [ "$status" -ne 0 ]
    [[ "$output" == *"a.bats:2:"* ]] || false
    [[ "$output" == *"b.bats:2:"* ]] || false
}

@test "bats-bang-check refuses to pass on nothing" {
    _check
    [ "$status" -ne 0 ]
    [[ "$output" == *"no .bats files"* ]] || false
}

@test "bin/run-tests.sh runs it" {
    grep -qF '"$repo_root/bin/bats-bang-check.sh"' "$REPO/bin/run-tests.sh"
}
