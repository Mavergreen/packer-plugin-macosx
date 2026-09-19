#!/usr/bin/env bash
# Fail if any shell file here uses a bash feature newer than 3.2.
#
# WHY 3.2, WHICH IS FROM 2006
#
# Stock OS X 10.9 Mavericks ships /bin/bash 3.2.57 -- the last GPLv2
# release, frozen by Apple in 2007 and never updated. The sibling project
# mavericks-vm-host is back-porting Hypervisor.framework to 10.9, with a
# prepackaged QEMU alongside it, so modern QEMU gets hardware acceleration
# there. When that lands, the obvious next thing someone will want is to
# work on THIS project from a Mavericks host, to build and run a Mavericks
# guest -- see docs/test-hosts.md and decisions/0007. The plugin itself is
# a Go binary; the repository's own tooling and tests are bash, and keeping
# them runnable there costs about fifteen lines today. Reopening it after a
# year of accumulated bash 4 habits costs a great deal more.
#
# WHAT THE RULE IS NOT
#
# This is not "we only support bash 3.2". Every shebang in this repository
# is `#!/usr/bin/env bash`, so a pkgsrc or Homebrew bash 5 earlier in PATH
# satisfies every script here regardless of what is in /bin. The floor
# binds against exactly one thing: WHAT APPLE SHIPPED. It says a 10.9 user
# who has not installed a newer bash can still run these scripts.
#
# Know that before you either over-apply this rule (it has nothing to say
# about anything but shell syntax) or delete it (the bash in /bin on 10.9
# is not going to get any newer).
#
# HOW IT CHECKS
#
# Line-oriented pattern matching, in-process, with no external tools.
# This is not shellcheck's job, and it cannot be made to be: as of 0.9 it
# has no bash-version floor to express. Its `--shell` flag selects a
# dialect (sh, bash, dash, ksh), not a version, and the SC3xxx portability
# checks are all-or-nothing against `sh` -- turning those on would reject
# the bash-3.2 features this project does use and rightly wants.
#
# (If you rewrite the paragraph above, do not let a line begin with that
# tool's name. A comment whose first word is that name is read as a
# directive, and the file then fails SC1073 for a sentence about it.)
#
# Comment lines are skipped, so prose about `mapfile` does not trip the
# check on a file that no longer calls it. A line carrying the marker
# `bash32-allow` is skipped too; the pattern table below needs it, since
# the patterns necessarily spell out the words they look for.
#
# LIMITS
#
# This is textual. It catches the constructs listed below, which is what
# the codebase actually reached for; it cannot catch a bash 4 builtin
# invoked through a variable, and it cannot see runtime-only breakage at
# all. The one runtime-only trap worth knowing about, because this
# codebase hit it: before bash 4.4, expanding an EMPTY array under `set -u`
# -- `"${arr[@]}"` -- is an "unbound variable" error rather than nothing.
# Write `${arr[@]+"${arr[@]}"}` wherever the array can be empty. No grep
# can tell which arrays those are, so that one is on the reader.
set -euo pipefail

MQG_REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# shellcheck source=../lib/common.sh
. "$MQG_REPO_ROOT/lib/common.sh"

# "<extended regex>|<what to write instead>". Each pattern is matched
# against one line at a time with `[[ =~ ]]`, which is bash 3.0 and so
# usable here.
#
# Every entry carries the bash32-allow marker: a pattern for `mapfile` has
# to contain the string `mapfile`, and without the marker this table would
# be the first thing the check reported.
#
# SC2016 (expressions do not expand in single quotes) is exactly the point
# here: every entry is a literal regex and a literal sentence, and the `$`
# and backticks in them are meant to reach the reader unexpanded.
# shellcheck disable=SC2016
BASH4_PATTERNS=(
    '(^|[^[:alnum:]_-])(mapfile|readarray)([^[:alnum:]_-]|$)|bash 4.0 mapfile/readarray: use `while IFS= read -r` fed by `< <(...)`'  # bash32-allow
    '(declare|local|typeset)[[:space:]]+(-[a-zA-Z]*A[a-zA-Z]*)([[:space:]]|$)|bash 4.0 associative array: use parallel arrays plus a lookup, or a `case`'  # bash32-allow
    '(declare|local|typeset)[[:space:]]+(-[a-zA-Z]*n[a-zA-Z]*)([[:space:]]|$)|bash 4.3 nameref: pass the name and use the array directly'  # bash32-allow
    '\$\{[^}]*\^\^|bash 4.0 uppercase expansion: use tr'  # bash32-allow
    '\$\{[^}]*[^,],,|bash 4.0 lowercase expansion: use tr'  # bash32-allow
    '(^|[[:space:]])coproc([[:space:]]|$)|bash 4.0 coproc: use explicit fifos or a temporary file'  # bash32-allow
    '&>>|bash 4.0 append-both redirect: use `>>file 2>&1`'  # bash32-allow
    ';;&|bash 4.0 case fallthrough: repeat the branch'  # bash32-allow
    '(^|[[:space:]])wait[[:space:]]+-n([[:space:]]|$)|bash 4.3 `wait -n`: wait on specific pids'  # bash32-allow
    '\$\{[^}]*@[QEPAaKk]\}|bash 4.4 parameter transformation: use printf %q or a case'  # bash32-allow
    '(^|[[:space:]])globstar([[:space:]]|$)|bash 4.0 globstar: use find'  # bash32-allow
    '\[\[[[:space:]]+-v[[:space:]]|bash 4.2 `[[ -v ]]`: use `${var+set}`'  # bash32-allow
    '(^|[[:space:]])lastpipe([[:space:]]|$)|bash 4.2 lastpipe: use `< <(...)` instead of a pipe'  # bash32-allow
    '\$\{?EPOCHSECONDS|bash 5.0 EPOCHSECONDS: use `date +%s`'  # bash32-allow
    '(^|[[:space:]])printf[^#]*%\([^)]*\)T|bash 4.2 printf %(fmt)T: use `date`'  # bash32-allow
)

# Which files to check: every shell file in the tree, found by walking it
# rather than by a hardcoded list, so a new script directory is covered the
# day it appears. .git is the one directory skipped, as it is in the
# lint pass bin/run-tests.sh makes.
#
# `while read` into an array, not `mapfile`: this script has to obey its
# own rule, and a lint that could not lint itself would be the wrong kind
# of joke.
#
# MQG_BASH32_ROOT points the walk somewhere else. That exists so the tests
# can run this check over a directory holding a deliberate violation: a
# gate nobody has ever seen fire is a gate nobody knows works.
SCAN_ROOT=${MQG_BASH32_ROOT:-$MQG_REPO_ROOT}

shell_files() {
    find "$SCAN_ROOT" \
        -path "$SCAN_ROOT/.git" -prune \
        -o \( -name '*.sh' -o -name '*.bats' -o -name '*.bash' \) -print
}

# Every pattern joined into one alternation. Testing a line against the
# fifteen patterns individually means fifteen regex compilations per line
# and several seconds over the tree; testing this one first, and only
# falling through to the individual patterns on the rare line that matches,
# makes the common case a single test. The patterns are EREs already, so
# joining them with `|` is just a larger ERE.
COMBINED=""
for entry in "${BASH4_PATTERNS[@]}"; do
    if [ -n "$COMBINED" ]; then
        COMBINED="$COMBINED|${entry%|*}"
    else
        COMBINED="${entry%|*}"
    fi
done

# check_file <path> -- print one report line per violation. Returns 0
# whether or not it found any; the caller counts the lines.
#
# No pipe into `grep -q` anywhere in here, deliberately, and no grep at
# all. `grep -q` exits the moment it matches, SIGPIPEs whoever is still
# writing, and under `set -o pipefail` the pipeline then reports 141 -- so
# an `if` takes the FALSE branch *because* the match succeeded. That bug
# made an earlier gate in this repository fail open exactly when it found
# a violation.
# Matching in-process with `[[ =~ ]]` has no pipe, no signal, and no
# exit-status subtlety to get wrong.
check_file() {
    local path=$1 line lineno=0 entry regex advice trimmed
    while IFS= read -r line || [ -n "$line" ]; do
        lineno=$(( lineno + 1 ))
        # Skip comment lines: prose explaining why a file no longer uses
        # mapfile is not a use of mapfile.
        trimmed="${line#"${line%%[![:space:]]*}"}"
        case $trimmed in '#'*) continue ;; esac
        # Skip explicitly marked lines (the pattern table above).
        case $line in *bash32-allow*) continue ;; esac
        # The fast path: almost every line matches nothing.
        [[ $line =~ $COMBINED ]] || continue
        for entry in "${BASH4_PATTERNS[@]}"; do
            regex=${entry%|*}
            advice=${entry##*|}
            if [[ $line =~ $regex ]]; then
                printf 'BASH4  %s:%s: %s\n' \
                    "${path#"$SCAN_ROOT/"}" "$lineno" "$advice"
            fi
        done
    done < "$path"
    return 0
}

files=()
while IFS= read -r f; do
    [ -n "$f" ] || continue
    files+=("$f")
done < <(shell_files)

if [ "${#files[@]}" -eq 0 ]; then
    die "no shell files found under $SCAN_ROOT -- nothing was checked"
fi

# Accumulated rather than printed as they are found, so the summary line
# and the exit status are decided in one place. `$( )` strips trailing
# newlines, hence the explicit separator.
findings=""
for f in "${files[@]}"; do
    one=$(check_file "$f")
    [ -n "$one" ] || continue
    findings="$findings$one
"
done

if [ -n "$findings" ]; then
    printf '%s' "$findings"
    die "the lines above use bash features newer than 3.2 (see the comment at the top of ${BASH_SOURCE[0]##*/})"
fi

log "${#files[@]} shell files use nothing newer than bash 3.2"
exit 0
