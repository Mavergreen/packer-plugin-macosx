#!/usr/bin/env bash
# platform: host-agnostic -- tested on Linux with KVM only
# Turn the gate's steps.tsv (one run, or a candidate and a control) into
# Markdown whose FIRST line is the one verdict a person reads:
#
#   Passed: every step, <N> restarts
#   Passed: every step that applies, <N> restarts; not applicable: <step>, ...
#   Regression: <step> -- <note>
#   No clear regression: CI itself failed at <step> -- <note>
#   No clear regression: main fails it too -- the control failed <step> -- <note>
#   No clear regression: the control could not run -- <step> -- <note>
#   Failed: <step> -- <note> (no control run; run bin/gate.sh on main to compare)
#
# A step is not applicable (n/a) where the release has nothing for it to check, with the reason
# as its note: on a 10.6 run, build-layer and reach alone (bin/gate.sh, docs/decisions/0014). It
# is neither a pass nor a failure; no other step may be one, and on a 10.9 run none may.
#
# The last Failed is for one directory alone (a local run, no control asked for) whose first
# non-pass is a fail. Given two directories, a missing or broken control is "the control could
# not run", except a control of another release, or whose release.txt names no release it knows:
# that is a usage error (exit 2), since there is nothing to compare.
#
# Each run's release is its <dir>/release.txt (bin/gate.sh writes it), or 10.9 when there is none:
# every gate run before the gate knew releases booted 10.9. Runs of two releases are never
# compared: that is a usage error, naming both.
#
# Then a table per run, then the last 40 lines of the first non-pass step's log.
# Exit 0 for Passed, 1 for any other verdict, 2 for usage or a malformed run.
#
#   usage: bin/gate-summary.sh <candidate-dir> [<control-dir>]
set -euo pipefail

GUEST_MACOSX_REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
export GUEST_MACOSX_REPO_ROOT
GUEST_MACOSX_LOG_PREFIX=gate-summary
export GUEST_MACOSX_LOG_PREFIX
# shellcheck source=../lib/common.sh
. "$GUEST_MACOSX_REPO_ROOT/lib/common.sh"

usage_die() { printf '%s: %s\n' "$GUEST_MACOSX_LOG_PREFIX" "$*" >&2; exit 2; }

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
    printf 'usage: %s <candidate-dir> [<control-dir>]\n' "${0##*/}" >&2
    exit 2
fi
cand=$1
ctl=${2:-}
ctl_requested=false
[ "$#" -lt 2 ] || ctl_requested=true

# validate <steps.tsv>: prints what is wrong with the first bad line (every
# line has four tab-separated fields and a known result), or nothing.
validate() {
    awk -F'\t' '
        NF != 4 { printf "line %d has %d columns, want 4\n", NR, NF; exit }
        $2 !~ /^(pass|fail|infra|skip|n\/a)$/ { printf "line %d has result \"%s\", want pass|fail|infra|skip|n/a\n", NR, $2; exit }
    ' "$1"
}

# structure <steps.tsv> <release>: prints "step<TAB>what is wrong" for the first
# structural problem, or nothing. A run is the fixed order preflight plugin
# build-base build-layer varstore boot versions health reach host-uuid
# restart-1..restart-N shutdown (N >= 1, consecutive), every row after the first
# non-pass `skip`. varstore may be missing: a control run of a gate from before it
# (docs/decisions/0032).
structure() {
    awk -F'\t' '
    BEGIN {
        split("preflight plugin build-base build-layer varstore boot versions health reach host-uuid", fixed, " ")
        nfixed = 10; off = 0; restarts = 0; done = 0; broke = 0
    }
    function bad(step, why) { printf "%s\t%s\n", step, why; found = 1; exit }
    {
        if (done) bad($1, "line " NR ": unexpected row after shutdown")
        if (NR + off <= nfixed) {
            want = fixed[NR + off]
            if (want == "varstore" && $1 == "boot") { off = 1; want = fixed[NR + off] }
        } else if ($1 == "shutdown") {
            if (restarts < 1) bad("restart-1", "line " NR ": no restart rows before shutdown")
            want = "shutdown"
        } else {
            want = "restart-" (restarts + 1)
        }
        if ($1 != want) bad(want, "line " NR ": found \"" $1 "\", want " want)
        if ($1 ~ /^restart-/) restarts++
        if ($1 == "shutdown") done = 1
        if (broke && $2 != "skip") bad($1, "line " NR ": " $2 " after an earlier non-pass; must be skip")
        if (!broke && $2 == "skip") bad($1, "line " NR ": skip with no earlier non-pass")
        if ($2 == "n/a" && rel != "10.6") bad($1, "line " NR ": " $1 " is n/a on a " rel " run, where every step applies")
        if ($2 == "n/a" && $1 != "build-layer" && $1 != "reach") bad($1, "line " NR ": " $1 " is n/a, which only build-layer and reach can be")
        if ($2 == "n/a" && $4 == "") bad($1, "line " NR ": n/a with no reason")
        if ($2 != "pass" && $2 != "n/a") broke = 1
    }
    END {
        if (found) exit
        if (NR == 0) bad("preflight", "file is empty")
        if (!done) {
            if (NR + off < nfixed) want = fixed[NR + off + 1]
            else if (restarts < 1) want = "restart-1"
            else want = "shutdown"
            bad(want, "missing step " want)
        }
    }' rel="$2" "$1"
}

# first_nonpass <steps.tsv>: prints "step<TAB>result<TAB>note", or nothing. A step not
# applicable is neither.
first_nonpass() {
    awk -F'\t' '$2 != "pass" && $2 != "n/a" { print $1 "\t" $2 "\t" $4; exit }' "$1"
}

note_or_none() { if [ -n "$1" ]; then printf '%s' "$1"; else printf '(no note)'; fi; }

# table <title> <steps.tsv>
table() {
    printf '\n### %s\n\n| step | result | seconds | note |\n| --- | --- | --- | --- |\n' "$1"
    awk -F'\t' '{ n = $4; gsub(/\|/, "\\|", n); printf "| %s | %s | %s | %s |\n", $1, $2, $3, n }' "$2"
}

# excerpt <title> <dir> <step>
excerpt() {
    local log=$2/$3.log
    [ -f "$log" ] || return 0
    printf '\n#### %s: last 40 lines of %s.log\n\n```\n' "$1" "$3"
    tail -n 40 "$log"
    printf '```\n'
}

# release_of <dir>: the run's release
release_of() {
    local r
    if [ -f "$1/release.txt" ]; then
        r=$(cat "$1/release.txt")
    else
        r=10.9
    fi
    known_release "$r" || usage_die "$1/release.txt names '$r', not $(releases_said)"
    printf '%s\n' "$r"
}

ctl_tsv=
cand_tsv=$cand/steps.tsv
[ -f "$cand_tsv" ] || usage_die "$cand_tsv: no such file"
cand_bad=$(validate "$cand_tsv")
[ -z "$cand_bad" ] || usage_die "$cand_tsv: $cand_bad"
cand_rel=$(release_of "$cand")
cand_bad=$(structure "$cand_tsv" "$cand_rel")
[ -z "$cand_bad" ] || usage_die "$cand_tsv: ${cand_bad#*	} (step ${cand_bad%%	*})"
ctl_rel=
if [ -n "$ctl" ] && [ -f "$ctl/steps.tsv" ]; then
    ctl_rel=$(release_of "$ctl")
    [ "$ctl_rel" = "$cand_rel" ] || usage_die "the candidate is a $cand_rel run and the control a $ctl_rel run: compare runs of one release"
fi

verdict=
exitcode=1
c_fnp=$(first_nonpass "$cand_tsv")
if [ -z "$c_fnp" ]; then
    n=$(awk -F'\t' '$1 ~ /^restart-/ { n++ } END { print n + 0 }' "$cand_tsv")
    na=$(awk -F'\t' '$2 == "n/a" { printf "%s%s", s, $1; s = ", " }' "$cand_tsv")
    if [ -n "$na" ]; then
        verdict="Passed: every step that applies, $n restarts; not applicable: $na"
    else
        verdict="Passed: every step, $n restarts"
    fi
    exitcode=0
else
    IFS=$'\t' read -r c_step c_res c_note <<EOF2
$c_fnp
EOF2
    c_note=$(note_or_none "$c_note")
    case $c_res in
    skip) usage_die "$cand_tsv: first non-pass step $c_step is skip, with nothing before it failing" ;;
    infra) verdict="No clear regression: CI itself failed at $c_step -- $c_note" ;;
    fail)
        if [ "$ctl_requested" = false ]; then
            verdict="Failed: $c_step -- $c_note (no control run; run bin/gate.sh on main to compare)"
        elif [ -n "$ctl" ] && [ -f "$ctl/steps.tsv" ]; then
            ctl_tsv=$ctl/steps.tsv
            k_bad=$(validate "$ctl_tsv")
            if [ -n "$k_bad" ]; then
                k_bad="control${k_bad:+$'\t'}$k_bad"
            else
                k_bad=$(structure "$ctl_tsv" "$ctl_rel")
            fi
            k_fnp=
            [ -n "$k_bad" ] || k_fnp=$(first_nonpass "$ctl_tsv")
            if [ -n "$k_bad" ]; then
                verdict="No clear regression: the control could not run -- ${k_bad%%	*} -- the control's steps.tsv is incomplete or malformed: ${k_bad#*	}"
            elif [ -z "$k_fnp" ]; then
                verdict="Regression: $c_step -- $c_note"
            else
                IFS=$'\t' read -r k_step k_res k_note <<EOF2
$k_fnp
EOF2
                k_note=$(note_or_none "$k_note")
                case $k_res in
                fail) verdict="No clear regression: main fails it too -- the control failed $k_step -- $k_note" ;;
                *) verdict="No clear regression: the control could not run -- $k_step -- $k_note" ;;
                esac
            fi
        else
            verdict="No clear regression: the control could not run -- control -- no steps.tsv in ${ctl:-(no control directory given)}"
        fi
        ;;
    esac
fi

printf '%s\n' "$verdict"
table "Candidate ($cand_rel)" "$cand_tsv"
[ -z "$ctl_tsv" ] || table "Control ($ctl_rel)" "$ctl_tsv"
if [ -n "${c_step:-}" ]; then
    excerpt Candidate "$cand" "$c_step"
fi
if [ -n "$ctl_tsv" ] && [ -n "${k_step:-}" ]; then
    excerpt Control "$ctl" "$k_step"
fi
exit "$exitcode"
