#!/usr/bin/env bash
# platform: host-agnostic
# Usage: list-shell-files.sh [DIR [PATTERN...]]
#
# Print the repo's shell scripts, one per line, relative to DIR (default:
# the current directory): every file whose name matches a PATTERN (globs,
# default '*.sh'), and every file with no extension whose first line is a
# shell's shebang, as lib/shebang.sh's shebang_shell judges one (any path,
# env and its options, spaces, arguments; sh, bash, dash, ksh and their
# kin) -- assets/guest/postinstall, say, which the OS X Installer runs by
# that name. A file with a NUL in its first 512 bytes is a binary,
# whatever it starts with, and is not listed. A script in a shell
# the linter cannot read (zsh, csh, ...) is an error: it is named on
# stderr, and the listing exits 1, so neither lint pass skips it silently.
#
# bin/run-tests.sh's shellcheck step lists *.sh; bin/bash32-check.sh lists
# *.sh, *.bats and *.bash; both get the shebang scripts too, so both passes
# read the same files.
#
# In a git checkout whose top level is DIR: git's tracked files and
# untracked-but-not-ignored ones, so a script not yet `git add`ed is linted
# and gitignored scratch is not. Anywhere else (a `git archive` extraction,
# a release tarball, or a tree inside some other repository's work tree,
# whose ignore rules are not this tree's -- TMPDIR in a dotfiles $HOME, or
# in a repository's gitignored scratch): walk the tree, pruning .git, so
# linting never silently disappears.
set -u
set -o pipefail
# shellcheck source=../lib/shebang.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/lib/shebang.sh"
cd "${1:-.}" || exit 1
[ "$#" -gt 0 ] && shift
[ "$#" -gt 0 ] || set -- '*.sh'

# script_shell <file>: shebang_shell of its first line, if it has no
# extension and is not a binary; fails otherwise.
script_shell() {
    local first all text
    case ${1##*/} in *.*) return 1 ;; esac
    [ -f "$1" ] || return 1
    all=$(head -c 512 "$1" | wc -c)
    text=$(head -c 512 "$1" | tr -d '\000' | wc -c)
    [ "$all" -eq "$text" ] || return 1
    IFS= read -r first < "$1" || [ -n "$first" ] || return 1
    shebang_shell "$first"
}

# shebang_scripts: read file names on stdin, print the shell scripts; name
# each one in a shell shellcheck cannot lint, and fail if there was one.
shebang_scripts() {
    local f sh bad=0
    while IFS= read -r f; do
        sh=$(script_shell "$f") || continue
        case $sh in
            '!'*)
                printf 'list-shell-files: %s is a %s script, which shellcheck cannot lint: write it for sh, bash, dash or ksh\n' "$f" "${sh#!}" >&2
                bad=1
                ;;
            *) printf '%s\n' "$f" ;;
        esac
    done
    return "$bad"
}

here=$(pwd -P)
top=$(git rev-parse --show-toplevel 2>/dev/null) && top=$(cd "$top" && pwd -P)
{
    if [ -n "$top" ] && [ "$top" = "$here" ]; then
        git ls-files --cached --others --exclude-standard -- "$@"
        git ls-files --cached --others --exclude-standard | shebang_scripts
    else
        names=()
        for p in "$@"; do
            [ "${#names[@]}" -eq 0 ] || names+=(-o)
            names+=(-name "$p")
        done
        find . -path ./.git -prune -o \( "${names[@]}" \) -print
        find . -path ./.git -prune -o -type f -print | shebang_scripts
    fi
} | awk '!seen[$0]++'
