#!/usr/bin/env bash
# platform: host-agnostic
# Fail if a test here has a `! cmd` line anywhere but at its end.
#
# WHY
#
# bats runs each test under `set -e`, and bash's `set -e` ignores a
# pipeline whose status is inverted with `!` ("The shell does not exit if
# ... the command's return status is being inverted with !", bash(1)). So
# in the middle of a test
#
#     ! grep -q "already existed" "$STUB_LOG"
#
# fails nothing when the grep finds the line: the test goes on, and
# passes. It checks nothing. A test's LAST command is different: bats
# takes the test's status from it, so a bang there works. The same holds
# for a helper function: its last command is its status, a bang before
# that is ignored.
#
# Write `! cmd || false` instead (the `|| false` is the command errexit
# sees fail), or `run cmd` and a status check, or bats' own `run ! cmd`.
# This repository's tests had 25 bangs that checked nothing, all of them
# found by reading (2026-10-28).
#
# HOW IT CHECKS
#
# Line-oriented, in-process, like bin/bash32-check.sh. In each .bats file,
# in the body of each test (`@test ... {` to a `}` in column 0) and of each
# function defined at the top level, the lines are joined where they
# continue (a trailing `\`, `|`, `&&` or `||`) and a heredoc's body is
# skipped, since it is another script. A statement is refused when it is
# not the body's last and
#
#   - it begins with `! `, or
#   - a `! ` follows `&&`, `||` or `;` in it (`a && ! b` is ignored by
#     errexit too),
#
# unless it begins with `if`, `elif`, `while` or `until` (a condition, where
# the bang does what it says), or a fallback that fails follows it: `||`
# then `false`, `return`, `exit`, `fail`, or a `{ ...; }` group holding one
# of those.
#
# A line carrying the marker `bats-bang-allow` is skipped.
#
# LIMITS
#
# Textual: a multi-line single-quoted string (`run bash -c '...'`) is read
# as the test's own lines, and a bang inside one is refused; mark it. A
# one-line test (`@test "x" { ...; }`) is not read at all.
set -euo pipefail

GUEST_MACOSX_REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# shellcheck source=../lib/common.sh
. "$GUEST_MACOSX_REPO_ROOT/lib/common.sh"

# GUEST_MACOSX_BATS_BANG_ROOT points the listing somewhere else, so the
# tests can run this check over a directory holding a deliberate violation.
SCAN_ROOT=${GUEST_MACOSX_BATS_BANG_ROOT:-$GUEST_MACOSX_REPO_ROOT}

# The same listing bin/bash32-check.sh and the shellcheck pass read: in a
# git checkout git's tracked and untracked, not-ignored files; outside one,
# the whole tree.
listing=$("$GUEST_MACOSX_REPO_ROOT/bin/list-shell-files.sh" "$SCAN_ROOT" '*.bats') \
    || die "bin/list-shell-files.sh refused a script (above)"

# The patterns, as variables: `[[ =~ ]]` takes a quoted right-hand side
# literally.
RE_TEST_START='^@test[[:space:]].*\{[[:space:]]*(#.*)?$'
RE_FUNC_START='^[A-Za-z_][A-Za-z0-9_:-]*[[:space:]]*\(\)[[:space:]]*\{[[:space:]]*(#.*)?$'
RE_BODY_END='^\}'
RE_HEREDOC="(^|[^<])<<(-?)[[:space:]]*[\"']?([A-Za-z_][A-Za-z0-9_]*)[\"']?"
RE_CONTINUES='(\\|\||&&|\|\|)[[:space:]]*$'
RE_LEADING_BANG='^![[:space:]]'
RE_LISTED_BANG='(&&|\|\||;)[[:space:]]*![[:space:]]'
RE_CONDITION='^(if|elif|while|until)[[:space:]]'
RE_FALLBACK='\|\|[[:space:]]*(false|return|exit|fail)([[:space:];]|$)'
RE_SQUOTED="^([^']*)'[^']+'(.*)\$"
RE_DQUOTED='^([^"]*)"[^"]+"(.*)$'
RE_FALLBACK_GROUP='\|\|[[:space:]]*\{.*(false|return|exit|fail)'

# refused <statement>: whether a bang in it is one errexit ignores.
# Quoted text is taken out first: a `&& ! ` or a `|| false` inside a
# string is not the statement's.
refused() {
    local s=$1
    while [[ $s =~ $RE_SQUOTED ]]; do s="${BASH_REMATCH[1]}''${BASH_REMATCH[2]}"; done
    while [[ $s =~ $RE_DQUOTED ]]; do s="${BASH_REMATCH[1]}''${BASH_REMATCH[2]}"; done
    [[ $s =~ $RE_CONDITION ]] && return 1
    [[ $s =~ $RE_LEADING_BANG ]] || [[ $s =~ $RE_LISTED_BANG ]] || return 1
    [[ $s =~ $RE_FALLBACK ]] && return 1
    [[ $s =~ $RE_FALLBACK_GROUP ]] && return 1
    return 0
}

# check_file <path>: one report line per refused statement.
check_file() {
    local path=$1 name line lineno=0 trimmed in_body=0 delim="" dash=""
    local stmt="" stmt_line=0 cand_line=0 cand_text="" pending=0
    name=${path#"$SCAN_ROOT/"}
    # A candidate is reported once another statement follows it in the
    # same body; one still pending at the body's end was its last.
    while IFS= read -r line || [ -n "$line" ]; do
        lineno=$(( lineno + 1 ))
        if [ -n "$delim" ]; then
            trimmed=$line
            [ -z "$dash" ] || trimmed="${line#"${line%%[!$'\t']*}"}"
            [ "$trimmed" != "$delim" ] || delim=""
            continue
        fi
        # A heredoc's body, from the next line, is another script, in a
        # body or out of one.
        if [[ $line =~ $RE_HEREDOC ]]; then
            dash=${BASH_REMATCH[2]}
            delim=${BASH_REMATCH[3]}
        fi
        if [ "$in_body" -eq 0 ]; then
            if [[ $line =~ $RE_TEST_START ]] || [[ $line =~ $RE_FUNC_START ]]; then
                in_body=1 stmt="" pending=0
            fi
            continue
        fi
        if [[ $line =~ $RE_BODY_END ]]; then
            in_body=0 stmt="" pending=0
            continue
        fi
        trimmed="${line#"${line%%[![:space:]]*}"}"
        if [ -z "$stmt" ]; then
            case $trimmed in ''|'#'*) continue ;; esac
            stmt_line=$lineno
            stmt=$trimmed
        else
            stmt="$stmt $trimmed"
        fi
        [[ $line =~ $RE_CONTINUES ]] && continue
        # A whole statement: the one before it, if a candidate, was not last.
        if [ "$pending" -eq 1 ]; then
            printf 'BANG  %s:%s: %s\n' "$name" "$cand_line" "$cand_text"
            pending=0
        fi
        case $stmt in
            *bats-bang-allow*) ;;
            *) if refused "$stmt"; then
                   pending=1 cand_line=$stmt_line cand_text=$stmt
               fi ;;
        esac
        stmt=""
    done < "$path"
    return 0
}

files=()
while IFS= read -r rel; do
    [ -n "$rel" ] || continue
    rel=${rel#./}
    [ -f "$SCAN_ROOT/$rel" ] || continue
    files+=("$SCAN_ROOT/$rel")
done <<< "$listing"

if [ "${#files[@]}" -eq 0 ]; then
    die "no .bats files found under $SCAN_ROOT -- nothing was checked"
fi

findings=""
for f in "${files[@]}"; do
    one=$(check_file "$f")
    [ -n "$one" ] || continue
    findings="$findings$one
"
done

if [ -n "$findings" ]; then
    printf '%s' "$findings"
    die "the bangs above are not their test's last command, so a test under set -e ignores them: write \`! cmd || false\`, or \`run cmd\` and check \$status (see the comment at the top of ${BASH_SOURCE[0]##*/})"
fi

log "${#files[@]} .bats files have no mid-test bang"
exit 0
