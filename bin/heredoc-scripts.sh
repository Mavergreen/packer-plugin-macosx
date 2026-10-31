#!/usr/bin/env bash
# platform: host-agnostic
# Usage: heredoc-scripts.sh ROOT OUTDIR
#
# Find every shell script embedded as a heredoc in ROOT's shell files (the
# files bin/list-shell-files.sh lists for bin/bash32-check.sh), and write
# each one under OUTDIR as a file the lint tools can read. firstboot.sh
# writes the guest's shutdown helper and sshd-keygen wrapper this way; the
# suites write their stubs this way. A heredoc is a shell script when its
# first line is a shell's shebang, as lib/shebang.sh's shebang_shell judges
# one; any other heredoc -- python, a plist, text -- is left alone. A
# heredoc inside an embedded script's body (a stub that writes a stub) is
# found the same way, in the body as extracted: its delimiter is named
# "OUTER > INNER".
#
# Prints one line per script, tab-separated:
#   <host file> <host line of its shebang> <delimiter> <shell> <extracted file>
# where <shell> is shellcheck's -s for it (sh, bash, dash or ksh). The
# extracted file keeps the host's line numbers: the lines before the body
# are blank, and so is the shebang's (the shell is given to the tools
# instead), so a finding at line N of it is at line N of the host. The
# nearest .shellcheckrc above the host file is copied beside it, so the
# host's lint settings apply to its heredocs too.
#
# DELIMITERS. Any word the shell takes is read: letters, digits, dashes and
# the rest, quoted ('E', "E", \E, or quoted in part), up to an unquoted
# blank or operator character. The quote characters are removed, as the
# shell removes them, to give the terminating line.
#
# QUOTED DELIMITERS ONLY. An unquoted delimiter (<<EOF) has its body
# expanded by the outer shell -- $vars, `commands`, backslashes -- before
# anything runs it, so its text is not the script that runs, and linting
# the text would lint the wrong thing (and what it expands to can depend
# on values known only at run time). So a shell heredoc must have a quoted
# delimiter, and one that does not is an error, named with its host line:
# write the values the script needs some other way (the environment, a
# file beside it, its own $0).
#
# Also errors, each named with its host line: a shell heredoc in a shell
# the linter cannot read (zsh, csh, ...); a shell heredoc with no
# terminator; and a line holding a second heredoc operator when either
# heredoc is a shell script -- the first's body, or the second's (the line
# after the first's terminator), starts with a shell's shebang -- since
# only the first would be linted: split them. And a printf or echo whose
# format string starts with a shell's shebang: it writes a shell script no
# lint can read, so write it as a quoted heredoc instead. (A shebang passed
# as an argument -- printf '%s\n' "$shebang", a fixture of the shebang
# matcher's own tests -- is data, not a script's text.) A comment line is
# not read.
set -u

root=${1:?usage: heredoc-scripts.sh ROOT OUTDIR}
out=${2:?usage: heredoc-scripts.sh ROOT OUTDIR}
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=../lib/shebang.sh
. "$here/../lib/shebang.sh"
root=$(cd "$root" && pwd) || exit 2
mkdir -p "$out" || exit 2
out=$(cd "$out" && pwd) || exit 2

status=0
n=0
# printf or echo as a command, its options, and a quoted format string
# starting #!: the text after the quote is BASH_REMATCH[6].
stub_re='(^|[;&|({[:space:]])(printf|echo)(([[:space:]]+-[A-Za-z-]*)*)[[:space:]]+(['"'"'"])(#!.*)$'

# nearest_rc <host path relative to root>: the nearest .shellcheckrc at or
# above the host's directory, up to root, or nothing.
nearest_rc() {
    local d
    d=$(dirname "$root/$1")
    while :; do
        if [ -f "$d/.shellcheckrc" ]; then
            printf '%s\n' "$d/.shellcheckrc"
            return 0
        fi
        [ "$d" = "$root" ] && return 0
        case $d in "$root"/*) d=$(dirname "$d") ;; *) return 0 ;; esac
    done
}

# untab <text>: text without its leading tabs (what <<- strips).
untab() { printf '%s' "${1#"${1%%[!	]*}"}"; }

# parse_op <text>: find the first heredoc operator in text. On success sets
# OP_DASH (1 for <<-), OP_QUOTED (1 if any of its word was quoted),
# OP_DELIM (the word, quotes removed: the terminating line) and OP_REST
# (the text after the word). A here-string (<<<) is not one.
parse_op() {
    local s=$1 w c part quoted
    while :; do
        case $s in *'<<'*) ;; *) return 1 ;; esac
        s=${s#*'<<'}
        case $s in
            '<'*)
                s=${s#'<'} # <<<, a here-string: past it
                continue
                ;;
        esac
        OP_DASH=
        case $s in '-'*) OP_DASH=1 s=${s#-} ;; esac
        s="${s#"${s%%[![:space:]]*}"}"
        w='' quoted=''
        while [ -n "$s" ]; do
            c=${s:0:1}
            case $c in
                "'" | '"')
                    quoted=1
                    s=${s:1}
                    part=${s%%"$c"*}
                    [ "$part" != "$s" ] || return 1 # no closing quote on this line
                    w=$w$part
                    s=${s#*"$c"}
                    ;;
                \\)
                    quoted=1
                    w=$w${s:1:1}
                    s=${s:2}
                    ;;
                ' ' | '	' | '<' | '>' | '|' | '&' | ';' | '(' | ')') break ;;
                *)
                    w=$w$c
                    s=${s:1}
                    ;;
            esac
        done
        if [ -n "$w" ]; then
            OP_QUOTED=$quoted OP_DELIM=$w OP_REST=$s
            return 0
        fi
    done
}

# safe <text>: text with anything but letters, digits, . _ - made _, for a
# file name.
safe() { printf '%s' "$1" | tr -c 'A-Za-z0-9._-' '_'; }

# scan <host path relative to root> <file to read> <delimiter prefix>
# The file is the host itself, or a body extracted from it, whose line
# numbers are the host's.
scan() {
    local host=$1 file=$2 within=$3 lines=() l i count trimmed opline
    local dash quoted delim rest first sh j end k dir f rc body second label
    while IFS= read -r l || [ -n "$l" ]; do
        lines+=("$l")
    done < "$file"
    count=${#lines[@]}
    i=0
    while [ "$i" -lt "$count" ]; do
        l=${lines[$i]}
        i=$((i + 1))
        # opline: the operator's host line, and the 0-based index of the
        # line after it, where the body starts.
        opline=$i
        trimmed="${l#"${l%%[![:space:]]*}"}"
        case $trimmed in '#'*) continue ;; esac
        if [[ $l =~ $stub_re ]]; then
            body=${BASH_REMATCH[6]}
            first=${body%%'\n'*}
            first=${first%%"${BASH_REMATCH[5]}"*}
            if shebang_shell "$first" > /dev/null; then
                printf '%s:%s: %s writes a shell script (%s), which no lint can read there: write it as a quoted heredoc (cat > FILE <<'"'"'EOF'"'"')\n' \
                    "$host" "$opline" "${BASH_REMATCH[2]}" "$first" >&2
                status=1
            fi
        fi
        parse_op "$l" || continue
        dash=$OP_DASH quoted=$OP_QUOTED delim=$OP_DELIM rest=$OP_REST
        [ "$opline" -lt "$count" ] || continue
        first=${lines[$opline]}
        [ -n "$dash" ] && first=$(untab "$first")
        # The terminator: the first line after the operator's that is delim.
        end=-1
        j=$opline
        while [ "$j" -lt "$count" ]; do
            body=${lines[$j]}
            [ -n "$dash" ] && body=$(untab "$body")
            if [ "$body" = "$delim" ]; then
                end=$j
                break
            fi
            j=$((j + 1))
        done
        label="$within$delim"
        if parse_op "$rest"; then
            second=
            if [ "$end" -ge 0 ] && [ $((end + 1)) -lt "$count" ]; then
                second=$(untab "${lines[$((end + 1))]}")
            fi
            if shebang_shell "$first" > /dev/null || shebang_shell "$second" > /dev/null; then
                printf '%s:%s: two heredocs on one line: split them (only the first would be linted, and one is a shell script)\n' \
                    "$host" "$opline" >&2
                status=1
                continue
            fi
        fi
        sh=$(shebang_shell "$first") || continue
        if [ "$end" -lt 0 ]; then
            printf '%s:%s: shell heredoc %s never ends: no line is %s\n' \
                "$host" "$opline" "$label" "$delim" >&2
            status=1
            return 0
        fi
        i=$((end + 1))
        case $sh in
            '!'*)
                printf '%s:%s: heredoc %s is a %s script, which shellcheck cannot lint: write it for sh, bash, dash or ksh\n' \
                    "$host" "$opline" "$label" "${sh#!}" >&2
                status=1
                continue
                ;;
        esac
        if [ -z "$quoted" ]; then
            printf '%s:%s: shell heredoc %s is unquoted, so the outer shell expands its text before it runs, and the text is not what runs: quote it (<<'"'"'%s'"'"') and pass values some other way\n' \
                "$host" "$opline" "$label" "$delim" >&2
            status=1
            continue
        fi
        n=$((n + 1))
        dir=$out/$n
        mkdir -p "$dir"
        f=$dir/$(basename "$host").$(safe "$label").$sh
        rc=$(nearest_rc "$host")
        [ -n "$rc" ] && cp "$rc" "$dir/.shellcheckrc"
        {
            k=0
            while [ "$k" -le "$opline" ]; do # host lines 1..opline+1, the shebang's the last: blank
                printf '\n'
                k=$((k + 1))
            done
            k=$((opline + 1))
            while [ "$k" -lt "$end" ]; do
                body=${lines[$k]}
                [ -n "$dash" ] && body=$(untab "$body")
                printf '%s\n' "$body"
                k=$((k + 1))
            done
        } > "$f"
        printf '%s\t%s\t%s\t%s\t%s\n' "$host" "$((opline + 1))" "$label" "$sh" "$f"
        # Heredocs inside it, found at the same line numbers.
        scan "$host" "$f" "$label > "
    done
}

# The listing whole first, so that its own refusal (a script in a shell
# the linter cannot read) fails this rather than vanishing.
listing=$("$here/list-shell-files.sh" "$root" '*.sh' '*.bats' '*.bash') || status=1
while IFS= read -r host; do
    host=${host#./}
    if [ -z "$host" ] || [ ! -f "$root/$host" ]; then continue; fi
    scan "$host" "$root/$host" ""
done <<< "$listing"

exit "$status"
