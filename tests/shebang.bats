#!/usr/bin/env bats
# platform: host-agnostic
#
# lib/shebang.sh's shebang_shell is the one shebang matcher the lint uses:
# bin/list-shell-files.sh finds extensionless scripts with it, and
# bin/heredoc-scripts.sh finds embedded ones. It prints the shell
# the script is linted as (sh, bash, dash or ksh), "!<name>" for a
# shell it cannot lint (which the lint then refuses, loudly), and fails for
# a line that is not a shell's shebang.

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    # shellcheck source=../lib/shebang.sh
    . "$REPO/lib/shebang.sh"
}

_is() {
    run shebang_shell "$1"
    [ "$status" -eq 0 ] || { echo "not a shell's shebang: $1"; false; }
    [ "$output" = "$2" ] || { echo "$1: $output, want $2"; false; }
}

_not() {
    run shebang_shell "$1"
    [ "$status" -ne 0 ] || { echo "taken for a shell's shebang: $1 ($output)"; false; }
}

@test "the shells shellcheck lints, each as itself" {
    _is '#!/bin/sh' sh
    _is '#!/bin/bash' bash
    _is '#!/bin/dash' dash
    _is '#!/bin/ksh' ksh
    _is '#!/bin/mksh' ksh
    _is '#!/bin/ash' dash
    _is '#!/bin/busybox sh' dash
}

@test "any path, spaces, arguments and env's options" {
    _is '#! /bin/sh' sh
    _is '#!  /usr/local/bin/bash -e' bash
    _is '#!/usr/bin/env bash' bash
    _is '#!/usr/bin/env  bash' bash
    _is '#!/usr/bin/env sh' sh
    _is '#!/usr/bin/env -S bash -eu' bash
    _is '#!/usr/bin/env -i PATH=/bin dash' dash
    _is '#!/usr/bin/env -u HOME ksh' ksh
    _is '#!/bin/sh -e' sh
}

@test "shells shellcheck cannot lint are named, for the lint to refuse" {
    _is '#!/usr/bin/zsh' '!zsh'
    _is '#!/usr/bin/env zsh' '!zsh'
    _is '#!/bin/csh -f' '!csh'
    _is '#!/bin/tcsh' '!tcsh'
    _is '#!/usr/bin/env fish' '!fish'
    _is '#!/bin/yash' '!yash'
}

@test "anything else is not a shell's shebang" {
    _not '#!/usr/bin/env python3'
    _not '#!/usr/bin/env bats'
    _not '#!/bin/shell-not'
    _not '#!/bin/bash5'
    _not '#!'
    _not '# !/bin/sh'
    _not 'echo #!/bin/sh'
    _not ''
}
