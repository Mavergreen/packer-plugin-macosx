# shellcheck shell=bash
# platform: host-agnostic
# The one shebang matcher the lint uses: bin/list-shell-files.sh finds
# extensionless shell scripts with it, bin/heredoc-scripts.sh embedded
# ones, and the Go guard against shell scripts in Go strings
# (gostrings_test.go) asks it too. Sourced, never executed; sets no shell
# options.

# shebang_shell <line> -- if line is a shell's shebang, print the shell to
# lint it as, a value of shellcheck's -s (sh, bash, dash or ksh), and
# succeed; for a shell that tool cannot lint (zsh, csh, tcsh, fish, yash),
# print "!<name>" and succeed, so the caller can refuse it loudly; for any
# other line, print nothing and fail.
#
# The interpreter may be any path (/bin/sh, /usr/local/bin/bash), with
# spaces after the #! and between words, and arguments after it; env may
# come first, with its options (-S, -i, -u NAME, ...) and NAME=value
# assignments; busybox may too ("#!/bin/busybox sh"). ash and busybox's sh
# are linted as dash, which is what shellcheck itself does with them, and
# mksh, pdksh, oksh and ksh93 as ksh.
shebang_shell() {
    local rest word name noglob=
    case $1 in '#!'*) ;; *) return 1 ;; esac
    rest=${1#'#!'}
    # Split it into words, with globbing off for the split and put back as
    # the caller had it.
    case $- in *f*) noglob=1 ;; esac
    set -f
    # shellcheck disable=SC2086 # the split into words is the point, and set -f keeps it from globbing
    set -- $rest
    [ -n "$noglob" ] || set +f
    [ "$#" -gt 0 ] || return 1
    name=${1##*/}
    shift
    if [ "$name" = env ]; then
        while [ "$#" -gt 0 ]; do
            case $1 in
                -u | -C | -P | --unset | --chdir) shift ;; # an option taking a value
                -* | *=*) ;;
                *) break ;;
            esac
            shift
        done
        [ "$#" -gt 0 ] || return 1
        name=${1##*/}
        shift
    fi
    if [ "$name" = busybox ]; then
        [ "$#" -gt 0 ] || return 1
        name=$1
        [ "$name" = sh ] && name=ash
    fi
    case $name in
        sh) word="sh" ;;
        bash) word="bash" ;;
        dash | ash) word="dash" ;;
        ksh | mksh | pdksh | oksh | ksh93) word="ksh" ;;
        zsh | csh | tcsh | fish | yash) word="!$name" ;;
        *) return 1 ;;
    esac
    printf '%s\n' "$word"
}
