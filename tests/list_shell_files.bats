#!/usr/bin/env bats
# platform: host-agnostic
#
# bin/list-shell-files.sh feeds run-tests.sh's shellcheck step: in a git
# checkout it skips gitignored scratch and keeps unadded scripts; outside
# one (a git archive extraction) it still finds every script.

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    T="$BATS_TEST_TMPDIR/tree"
    mkdir -p "$T/bin" "$T/scratch" "$T/.git-not"
    echo : > "$T/bin/tracked.sh"
    echo : > "$T/bin/unadded.sh"
    echo : > "$T/scratch/ignored.sh"
    echo 'scratch/' > "$T/.gitignore"
}

@test "in a git checkout: tracked and unadded scripts, not ignored ones" {
    git -C "$T" init -q
    git -C "$T" add bin/tracked.sh
    run "$REPO/bin/list-shell-files.sh" "$T"
    [ "$status" -eq 0 ]
    [[ "$output" == *"bin/tracked.sh"* ]] || false
    [[ "$output" == *"bin/unadded.sh"* ]] || false
    [[ "$output" != *"ignored.sh"* ]] || false
}

@test "outside a git checkout: walks the tree, with no git error" {
    run "$REPO/bin/list-shell-files.sh" "$T"
    [ "$status" -eq 0 ]
    [[ "$output" == *"bin/tracked.sh"* ]] || false
    [[ "$output" == *"scratch/ignored.sh"* ]] || false
    [[ "$output" != *fatal* ]] || false
}

@test "patterns after DIR choose which files are listed, in and out of git" {
    # bin/bash32-check.sh scans .bats and .bash as well as .sh, through
    # this same listing, so the two passes agree on what the project's
    # shell is. With no patterns, *.sh, as shellcheck wants.
    echo : > "$T/bin/suite.bats"
    echo : > "$T/scratch/ignored.bats"
    run "$REPO/bin/list-shell-files.sh" "$T" '*.sh' '*.bats'
    [ "$status" -eq 0 ]
    [[ "$output" == *"bin/suite.bats"* ]] || false
    [[ "$output" == *"scratch/ignored.bats"* ]] || false
    run "$REPO/bin/list-shell-files.sh" "$T"
    [[ "$output" != *"suite.bats"* ]] || false
    git -C "$T" init -q
    run "$REPO/bin/list-shell-files.sh" "$T" '*.sh' '*.bats'
    [ "$status" -eq 0 ]
    [[ "$output" == *"bin/suite.bats"* ]] || false
    [[ "$output" == *"bin/tracked.sh"* ]] || false
    [[ "$output" != *"ignored"* ]] || false
}

@test "a tree inside another repository's work tree, ignored there, is walked" {
    # TMPDIR inside a dotfiles $HOME, or inside a repository's ignored
    # scratch: that repository's rules are not this tree's. git is used
    # only when DIR is its own work tree's top level.
    E="$BATS_TEST_TMPDIR/enclosing"
    mkdir -p "$E"
    git -C "$E" init -q
    echo 'inner/' > "$E/.gitignore"
    mkdir -p "$E/inner"
    cp -R "$T" "$E/inner/tree"
    run "$REPO/bin/list-shell-files.sh" "$E/inner/tree"
    [ "$status" -eq 0 ]
    [[ "$output" == *"bin/tracked.sh"* ]] || false
    [[ "$output" == *"scratch/ignored.sh"* ]] || false
    [[ "$output" != *fatal* ]] || false
}

# _script <file> <shebang> [<line>...]: write a fixture whose first line
# is <shebang>. The shebang is an argument, not printf's format: these are
# the shebang matcher's test data, not stubs to run, so they are not
# written as heredocs (bin/heredoc-scripts.sh), and some of them -- zsh, a
# binary -- could not be.
_script() {
    local f=$1
    shift
    printf '%s\n' "$@" > "$f"
}

# _shebang_fixtures: an extensionless script for each shebang the listing
# knows, one with no shebang, one with a shebang for another language, and
# a binary that starts like a script, each tracked and also not.
_shebang_fixtures() {
    mkdir -p "$T/assets"
    _script "$T/assets/postinstall" '#!/bin/sh' 'echo hi'
    _script "$T/bin/bashtool" '#!/bin/bash' 'echo hi'
    _script "$T/bin/envbash" '#!/usr/bin/env bash' 'echo hi'
    _script "$T/bin/envsh" '#!/usr/bin/env sh' 'echo hi'
    _script "$T/bin/withflag" '#!/bin/sh -e' 'echo hi'
    _script "$T/bin/spaced" '#! /bin/sh' 'echo hi'
    _script "$T/bin/twospaces" '#!/usr/bin/env  bash' 'echo hi'
    _script "$T/bin/envdashs" '#!/usr/bin/env -S bash -e' 'echo hi'
    _script "$T/bin/dashtool" '#!/bin/dash' 'echo hi'
    _script "$T/bin/kshtool" '#!/bin/ksh' 'echo hi'
    _script "$T/bin/localbash" '#!/usr/local/bin/bash' 'echo hi'
    printf 'echo no shebang\n'               > "$T/bin/noshebang"
    _script "$T/bin/pytool" '#!/usr/bin/env python3' 'print(1)'
    { printf '%s\n' '#!/bin/sh'; printf '\000\001\002binary\n'; } > "$T/bin/binary"
    _script "$T/bin/notsh" '#!/bin/shell-not'
    _script "$T/scratch/ignored-tool" '#!/bin/sh' 'echo hi'
}

_check_shebang_listing() {
    for f in assets/postinstall bin/bashtool bin/envbash bin/envsh bin/withflag \
             bin/spaced bin/twospaces bin/envdashs bin/dashtool bin/kshtool bin/localbash; do
        [[ "$output" == *"$f"* ]] || { echo "missing: $f"; false; }
    done
    for f in noshebang pytool binary notsh; do
        [[ "$output" != *"bin/$f"* ]] || { echo "listed: $f"; false; }
    done
}

@test "extensionless scripts are listed by their shebang, in a git checkout" {
    _shebang_fixtures
    git -C "$T" init -q
    git -C "$T" add bin/bashtool
    run "$REPO/bin/list-shell-files.sh" "$T"
    [ "$status" -eq 0 ]
    _check_shebang_listing
    [[ "$output" != *"ignored-tool"* ]] || false
    # Patterns choose by name; a shebang script is listed whatever they are.
    run "$REPO/bin/list-shell-files.sh" "$T" '*.bats'
    _check_shebang_listing
}

@test "extensionless scripts are listed by their shebang, outside a git checkout" {
    _shebang_fixtures
    run "$REPO/bin/list-shell-files.sh" "$T"
    [ "$status" -eq 0 ]
    _check_shebang_listing
    [[ "$output" == *"scratch/ignored-tool"* ]] || false
}

@test "a script in a shell shellcheck cannot lint fails the listing loudly, naming it" {
    # Listing it would have shellcheck refuse it, and only where shellcheck
    # is installed; leaving it out would be a silent gap. So: an error.
    _script "$T/bin/fine" '#!/bin/sh' 'echo hi'
    _script "$T/bin/zshtool" '#!/usr/bin/zsh' 'echo hi'
    run "$REPO/bin/list-shell-files.sh" "$T"
    [ "$status" -ne 0 ]
    [[ "$output" == *"bin/zshtool"*"zsh"* ]] || false
    run env GUEST_MACOSX_BASH32_ROOT="$T" "$REPO/bin/bash32-check.sh"
    [ "$status" -ne 0 ]
    [[ "$output" == *"bin/zshtool"* ]] || false
}
