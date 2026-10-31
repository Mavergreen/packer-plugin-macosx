#!/usr/bin/env bash
# platform: host-agnostic
# Usage: workflow-scripts.sh ROOT OUTDIR
#
# Write each `run:` step of ROOT's GitHub workflows (.github/workflows/
# *.yml and *.yaml) under OUTDIR as a file shellcheck can read, with the
# shell GitHub runs it with, and print one line per step, tab-separated:
#   <workflow> <line of its first command> <job/step> <shell> <extracted file>
# where <shell> is shellcheck's -s for it. As with bin/heredoc-scripts.sh,
# the extracted file keeps the workflow's line numbers (blank lines before
# the script), so a finding at line N of it is at line N of the workflow.
#
# THE SHELL. A step's own `shell:`, else its job's `defaults: run: shell:`,
# else the workflow's, else GitHub's default: bash, except on a Windows
# runner (pwsh). bash and sh are linted as themselves, and a custom shell
# command ("bash --noprofile --norc -eo pipefail {0}") by its first word.
# pwsh, powershell, cmd and python steps are not shell scripts and are
# left alone. Any other shell is an error, named with its line: nothing
# may go unlinted silently.
#
# EXPRESSIONS. Each ${{ ... }} is replaced by the word _GHA_EXPR_ before
# linting: GitHub substitutes it before the shell sees the text, and what
# it becomes is only known at run time.
#
# THE YAML IT READS. This is not a YAML parser: it reads the block style
# GitHub workflows are written in -- "key: value" mappings by indentation,
# "- key: value" sequence items, `|` blocks, # comments -- and refuses,
# naming the line, any `run:` it cannot read exactly: a folded (>) block,
# a block with an explicit indentation indicator, a plain scalar that
# continues on the next line, a double-quoted scalar with an escape, or a
# flow-style value. The workflows here are written so that it can.
set -u

root=${1:?usage: workflow-scripts.sh ROOT OUTDIR}
out=${2:?usage: workflow-scripts.sh ROOT OUTDIR}
root=$(cd "$root" && pwd) || exit 2
mkdir -p "$out" || exit 2
out=$(cd "$out" && pwd) || exit 2

status=0
n=0
key_re='^( *)(- +)?([A-Za-z_][A-Za-z0-9_-]*):( +(.*))?$'

# strip_comment <plain scalar>: the value without a trailing # comment.
strip_comment() {
    local v=$1
    case $v in '#'*) v= ;; esac
    v=${v%%[[:space:]]#*}
    printf '%s' "${v%"${v##*[![:space:]]}"}"
}

# shell_for <shell: value> <workflow:line>: print shellcheck's -s for it,
# "-" for a step that is not a shell script; error for anything else.
shell_for() {
    local name=${1%%[[:space:]]*}
    name=${name##*/}
    case $name in
        bash | sh) printf '%s\n' "$name" ;;
        pwsh | powershell | cmd | python) printf -- '-\n' ;;
        *)
            printf '%s: shell %s is not one this lint knows: use bash or sh, or teach bin/workflow-scripts.sh\n' "$2" "$1" >&2
            return 1
            ;;
    esac
}

# scan <workflow path relative to root>
scan() {
    local wf=$1 lines=() l i count indent dash key value item_indent
    local path=() pind=() depth p job step wfshell jobshell
    local runline runtext blockind content k sh label f c sheet
    local -a step_run_line=() step_run_text=() step_shell=() step_job=() step_label=() step_num=()
    while IFS= read -r l || [ -n "$l" ]; do
        lines+=("$l")
    done < "$root/$wf"
    count=${#lines[@]}
    wfshell='' job='' step=-1
    # Per job: its shell default and whether it runs on Windows, as
    # parallel lists (bash 3.2 has no associative arrays).
    local -a jobs=() jobshells=() jobwin=()
    i=0
    while [ "$i" -lt "$count" ]; do
        l=${lines[$i]}
        i=$((i + 1))
        [ -n "${l//[[:space:]]/}" ] || continue
        case ${l#"${l%%[![:space:]]*}"} in '#'*) continue ;; esac
        if ! [[ $l =~ $key_re ]]; then
            continue # a sequence of scalars, a continuation: not a key line
        fi
        indent=${#BASH_REMATCH[1]} dash=${BASH_REMATCH[2]} key=${BASH_REMATCH[3]}
        value=${BASH_REMATCH[5]}
        if [ -n "$dash" ]; then
            item_indent=$indent
            indent=$((indent + ${#dash}))
        fi
        # The path: pop every entry at this indent or deeper.
        depth=${#path[@]}
        while [ "$depth" -gt 0 ] && [ "${pind[$((depth - 1))]}" -ge "$indent" ]; do
            unset "path[$((depth - 1))]" "pind[$((depth - 1))]"
            depth=$((depth - 1))
        done
        if [ -n "$dash" ]; then
            # A new sequence item: drop the item marker of the one before.
            while [ "$depth" -gt 0 ] && [ "${pind[$((depth - 1))]}" -ge "$item_indent" ]; do
                unset "path[$((depth - 1))]" "pind[$((depth - 1))]"
                depth=$((depth - 1))
            done
            path[depth]=-
            pind[depth]=$item_indent
            depth=$((depth + 1))
            if [ "$depth" -eq 4 ] && [ "${path[0]}" = jobs ] && [ "${path[2]}" = steps ]; then
                step=${#step_job[@]}
                step_job[step]=${path[1]}
                step_run_line[step]=''
                step_run_text[step]=''
                step_shell[step]=''
                # Its number within its job, from 1.
                k=0 c=0
                while [ "$k" -le "$step" ]; do
                    [ "${step_job[$k]}" = "${path[1]}" ] && c=$((c + 1))
                    k=$((k + 1))
                done
                step_num[step]=$c
                step_label[step]="job ${path[1]}, step $c"
            fi
        fi
        path[depth]=$key
        pind[depth]=$indent
        depth=$((depth + 1))
        p=$(IFS=/; printf '%s' "${path[*]}")
        value=${value%"${value##*[![:space:]]}"}
        # A block scalar's content: the lines after, more indented.
        content=
        c=$(strip_comment "$value")
        case $c in
            '|' | '|-' | '|+' | '>'* | '|'[0-9-+]*)
                blockind=-1 runtext='' runline=''
                case $c in
                    '>'*)
                        if [[ $p =~ ^jobs/[^/]+/steps/-/run$ ]]; then
                            printf '%s:%s: a folded (>) run: block: write it as |\n' "$wf" "$i" >&2
                            status=1
                        fi
                        ;;
                    '|'[0-9]* | '|'[-+][0-9]*)
                        if [[ $p =~ ^jobs/[^/]+/steps/-/run$ ]]; then
                            printf '%s:%s: a run: block with an indentation indicator: drop it\n' "$wf" "$i" >&2
                            status=1
                        fi
                        ;;
                esac
                while [ "$i" -lt "$count" ]; do
                    c=${lines[$i]}
                    if [ -n "${c//[[:space:]]/}" ]; then
                        k=${c%%[![:space:]]*}
                        [ "${#k}" -gt "$indent" ] || break
                        [ "$blockind" -ge 0 ] || blockind=${#k}
                        c=${c:$blockind}
                    else
                        c=
                    fi
                    [ -n "$runline" ] || runline=$((i + 1))
                    runtext="$runtext$c
"
                    i=$((i + 1))
                done
                content=block
                ;;
        esac
        case $p in
            defaults/run/shell) wfshell=$(strip_comment "$value") ;;
            jobs/*/defaults/run/shell | jobs/*/runs-on)
                job=${path[1]}
                k=0
                while [ "$k" -lt "${#jobs[@]}" ] && [ "${jobs[$k]}" != "$job" ]; do k=$((k + 1)); done
                if [ "$k" -eq "${#jobs[@]}" ]; then
                    jobs[k]=$job jobshells[k]='' jobwin[k]=''
                fi
                case $p in
                    */shell) jobshells[k]=$(strip_comment "$value") ;;
                    *) case $(strip_comment "$value") in *windows*) jobwin[k]=1 ;; esac ;;
                esac
                ;;
            jobs/*/steps/-/shell) step_shell[step]=$(strip_comment "$value") ;;
            jobs/*/steps/-/name) step_label[step]="job ${path[1]}, step ${step_num[$step]} ($(strip_comment "$value"))" ;;
            jobs/*/steps/-/run)
                if [ "$content" = block ]; then
                    step_run_line[step]=$runline
                    step_run_text[step]=$runtext
                    continue
                fi
                # An inline scalar: plain, or quoted on this one line.
                case $value in
                    "'"*"'")
                        value=${value#"'"} value=${value%"'"}
                        value=${value//"''"/"'"}
                        ;;
                    '"'*'"')
                        case $value in *\\*)
                            printf '%s:%s: a double-quoted run: with an escape: write it as a | block\n' "$wf" "$i" >&2
                            status=1
                            continue
                            ;;
                        esac
                        value=${value#'"'} value=${value%'"'}
                        ;;
                    '{'* | '['* | "'"* | '"'*)
                        printf '%s:%s: a run: this lint cannot read exactly: write it as a | block\n' "$wf" "$i" >&2
                        status=1
                        continue
                        ;;
                    *) value=$(strip_comment "$value") ;;
                esac
                if [ "$i" -lt "$count" ]; then
                    c=${lines[$i]}
                    k=${c%%[![:space:]]*}
                    if [ -n "${c//[[:space:]]/}" ] && [ "${#k}" -gt "$indent" ] && ! [[ $c =~ $key_re ]]; then
                        printf '%s:%s: a run: that continues on the next line: write it as a | block\n' "$wf" "$i" >&2
                        status=1
                        continue
                    fi
                fi
                step_run_line[step]=$i
                step_run_text[step]="$value
"
                ;;
        esac
    done

    [ "${#step_job[@]}" -gt 0 ] || return 0 # no steps (a reusable workflow's caller, say)
    for step in "${!step_job[@]}"; do
        [ -n "${step_run_line[$step]}" ] || continue
        job=${step_job[$step]}
        jobshell='' sheet=''
        k=0
        while [ "$k" -lt "${#jobs[@]}" ]; do
            if [ "${jobs[$k]}" = "$job" ]; then
                jobshell=${jobshells[$k]} sheet=${jobwin[$k]}
            fi
            k=$((k + 1))
        done
        sh=${step_shell[$step]:-${jobshell:-$wfshell}}
        if [ -z "$sh" ]; then
            sh=bash
            [ -n "$sheet" ] && sh=pwsh
        fi
        sh=$(shell_for "$sh" "$wf:${step_run_line[$step]}") || { status=1; continue; }
        [ "$sh" != - ] || continue
        n=$((n + 1))
        mkdir -p "$out/$n"
        label=${step_label[$step]}
        f=$out/$n/$(basename "$wf").${job//[!A-Za-z0-9_-]/_}.step${step_num[$step]}.$sh
        {
            k=1
            while [ "$k" -lt "${step_run_line[$step]}" ]; do
                printf '\n'
                k=$((k + 1))
            done
            printf '%s' "${step_run_text[$step]}" | sed 's/\${{[^}]*}}/_GHA_EXPR_/g'
        } > "$f"
        printf '%s\t%s\t%s\t%s\t%s\n' "$wf" "${step_run_line[$step]}" "$label" "$sh" "$f"
    done
}

for wf in "$root"/.github/workflows/*.yml "$root"/.github/workflows/*.yaml; do
    [ -f "$wf" ] || continue
    scan "${wf#"$root"/}"
done
exit "$status"
