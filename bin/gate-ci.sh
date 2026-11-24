#!/usr/bin/env bash
# platform: host-agnostic -- tested on Linux with KVM only
# What .github/workflows/gate.yml runs around bin/gate.sh, so the workflow holds no logic of its own
# (decision 0017: CI runs the local command; it does not reimplement it in YAML).
#
#   bin/gate-ci.sh run --release R --checkout DIR --work-dir DIR --evidence-dir DIR --media-dir DIR
#                      --setup OUTCOME
#       One gate run on this runner: DIR/bin/gate.sh --release R, with the given work and evidence
#       dirs. R is a release lib/common.sh knows (GUEST_MACOSX_RELEASES). For 10.6 this script
#       first fetches CI's pinned copy of the install disc (snowleopard-disc-10A432) into the media
#       dir with `go run ./cmd/fetch-pinned`, and gives its path to gate.sh as --installer. A media
#       dir that is, or is inside, the work or evidence dir is a usage error for any release (exit
#       2, nothing run): the evidence dir is uploaded, and gate.sh refuses one in its work dir.
#       10.9 takes the media dir and leaves it unused: its gate.sh fetches its own InstallESD. The
#       fetcher is always this checkout's, run from this checkout's root, even when DIR is a control
#       ref's older checkout: that ref may have no fetcher, or pin another disc, and the control
#       must boot the disc the candidate booted. Writes exit=<gate.sh's exit> to $GITHUB_OUTPUT and itself exits 0: the
#       next job, not this one, decides (a failed job in a called workflow would fail a release
#       whose retry passed). OUTCOME is the host-setup step's (steps.host.outcome); a DIR with no
#       bin/gate.sh (a ref from before the gate, or no ref at all), or else an OUTCOME but success,
#       or else a 10.6 media dir that could not be made or a disc that could not be fetched (named
#       by the fetcher's last fetch-pinned: line), is exit=3 with a whole steps.tsv (and
#       release.txt) whose preflight is infra saying why, so the verdict names it rather than "no
#       steps.tsv". Seam: GUEST_MACOSX_GATE_FETCH_PINNED (a script run in place of the fetcher,
#       with the same arguments, from the same directory).
#
#   bin/gate-ci.sh control-ref --event EVENT --given REF --pr-base SHA
#       The control's ref, on stdout: REF when given; for a pull_request its base; for a release
#       (workflow_call) the newest v0.* tag before HEAD; for a dispatch the merge base with
#       origin/main. None found is empty output and a warning: the control then says it had no ref.
#
#   bin/gate-ci.sh verdict --decision D --reason R --scope-job RESULT
#                          [--candidate DIR --candidate-job RESULT --candidate-exit N
#                           --retry DIR --retry-job RESULT --retry-exit N --control DIR]
#       The required check's verdict as Markdown on stdout, its first line the verdict; exit 0 to
#       pass, 1 to fail, 2 on usage. D is the scope job's decision (gated, not-gated,
#       pins-unfilled; empty when the scope job failed). When gated, the candidate is the retry's
#       evidence if the retry ran, else the candidate's; with no steps.tsv there it is "CI itself
#       failed"; else bin/gate-summary.sh <candidate> <control> decides by its exit (0 is a
#       Passed: line), always given the control dir, so the local-only "Failed:" line never
#       appears in CI. A RESULT is a job's needs.<job>.result; an empty N is "none".
#
#   bin/gate-ci.sh verdict --decision D --reason R --scope-job RESULT --releases "R ..." --artifacts DIR
#       The same verdict over every release a gated change gates, each decided as above from the
#       artifacts DIR/gate-<release>-<leg> (leg: candidate, candidate-retry, control), each that
#       leg's evidence plus a `job` file (its RESULT) and an `exit` file (its N, bare or as exit=N).
#       A leg with no artifact is a job that never ran (RESULT skipped, N none); a release with no
#       artifact at all fails, naming it, as does an artifact whose release.txt names another
#       release. The first line is "Passed: <release>, ..." when every release passed, else the
#       first failing release's own first line prefixed "<release>: "; under it, a "## <release>"
#       section each. Every release must be one lib/common.sh knows, each once (else exit 2); the
#       one-release flags are refused beside these. Not gated and pins unfilled need no releases.
set -euo pipefail

GUEST_MACOSX_REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
export GUEST_MACOSX_REPO_ROOT
GUEST_MACOSX_LOG_PREFIX=gate-ci
export GUEST_MACOSX_LOG_PREFIX
# shellcheck source=../lib/common.sh
. "$GUEST_MACOSX_REPO_ROOT/lib/common.sh"

usage_die() { printf '%s: %s\n' "$GUEST_MACOSX_LOG_PREFIX" "$*" >&2; exit 2; }
usage() { usage_die "usage: bin/gate-ci.sh run|control-ref|verdict ... (see its header)"; }

opt_release='' opt_media_dir='' opt_checkout='' opt_work_dir='' opt_evidence_dir='' opt_setup=''
opt_event='' opt_given='' opt_pr_base=''
opt_decision='' opt_reason='' opt_scope_job='' opt_candidate='' opt_candidate_job='' opt_candidate_exit=''
opt_retry='' opt_retry_job='' opt_retry_exit='' opt_control='' opt_releases='' opt_artifacts=''
given=" "

# opts <names> -- <args>: set opt_<name> for each --<name> VALUE among <names>; any other argument
# is a usage error. A value may be empty (a job's output that was never set).
opts() {
    local names=" "
    while [ "$1" != -- ]; do names="$names$1 "; shift; done
    shift
    while [ "$#" -gt 0 ]; do
        case $names in *" ${1#--} "*) ;; *) usage_die "unknown argument: $1" ;; esac
        [ "$#" -ge 2 ] || usage_die "$1 needs a value"
        case $1 in
            --release) opt_release=$2 ;;
            --media-dir) opt_media_dir=$2 ;;
            --checkout) opt_checkout=$2 ;;
            --work-dir) opt_work_dir=$2 ;;
            --evidence-dir) opt_evidence_dir=$2 ;;
            --setup) opt_setup=$2 ;;
            --event) opt_event=$2 ;;
            --given) opt_given=$2 ;;
            --pr-base) opt_pr_base=$2 ;;
            --decision) opt_decision=$2 ;;
            --reason) opt_reason=$2 ;;
            --scope-job) opt_scope_job=$2 ;;
            --candidate) opt_candidate=$2 ;;
            --candidate-job) opt_candidate_job=$2 ;;
            --candidate-exit) opt_candidate_exit=$2 ;;
            --retry) opt_retry=$2 ;;
            --retry-job) opt_retry_job=$2 ;;
            --retry-exit) opt_retry_exit=$2 ;;
            --control) opt_control=$2 ;;
            --releases) opt_releases=$2 ;;
            --artifacts) opt_artifacts=$2 ;;
        esac
        given="$given${1#--} "
        shift 2
    done
}

# need <name>...: each --<name> was given
need() {
    local n
    for n in "$@"; do
        case $given in *" $n "*) ;; *) usage_die "--$n is required" ;; esac
    done
}

# placeholder <evidence-dir> <note>: a whole steps.tsv of the release being run whose preflight
# could not run, in bin/gate.sh's step order with its default 10 restarts (tests/gate_ci.bats holds
# them equal). Its release.txt is written as gate.sh writes one, so bin/gate-summary.sh compares a
# 10.6 placeholder with a 10.6 control rather than taking it for a 10.9 run.
placeholder() {
    local s
    mkdir -p "$1"
    printf '%s\n' "$opt_release" > "$1/release.txt"
    printf 'preflight\tinfra\t0\t%s\n' "$2" > "$1/steps.tsv"
    for s in plugin build-base build-layer varstore boot versions health reach host-uuid \
        restart-1 restart-2 restart-3 restart-4 restart-5 restart-6 restart-7 restart-8 restart-9 \
        restart-10 shutdown; do
        printf '%s\tskip\t0\t\n' "$s" >> "$1/steps.tsv"
    done
}

output() { printf '%s\n' "$1" >> "${GITHUB_OUTPUT:-/dev/null}"; }

# fetch_disc <media-dir> <stdout-file> <stderr-file>: this checkout's fetcher, run from this
# checkout's root so `go run` finds this module whatever the caller's directory is
fetch_disc() {
    (
        cd "$GUEST_MACOSX_REPO_ROOT" || exit 1
        if [ -n "${GUEST_MACOSX_GATE_FETCH_PINNED:-}" ]; then
            "$GUEST_MACOSX_GATE_FETCH_PINNED" snowleopard-disc-10A432 "$1"
        else
            go run "$GUEST_MACOSX_REPO_ROOT/cmd/fetch-pinned" snowleopard-disc-10A432 "$1"
        fi
    ) > "$2" 2> "$3"
}

# abs_path <path>: an absolute path for <path>, which need not exist yet: its longest existing
# ancestor resolved physically, the rest with . and .. taken lexically, so two spellings of one
# directory compare equal
abs_path() {
    local p=$1 part head='' rest='' d
    local -a parts out
    case $p in /*) ;; *) p=$PWD/$p ;; esac
    IFS=/ read -r -a parts <<< "$p"
    out=()
    for part in ${parts[@]+"${parts[@]}"}; do
        case $part in
            ''|.) ;;
            ..) [ "${#out[@]}" -eq 0 ] || unset "out[$((${#out[@]} - 1))]" ;;
            *) out+=("$part") ;;
        esac
    done
    for part in ${out[@]+"${out[@]}"}; do
        if [ -z "$rest" ] && [ -d "$head/$part" ]; then head=$head/$part; else rest=$rest/$part; fi
    done
    d=$(cd "${head:-/}" && pwd -P)
    printf '%s\n' "${d%/}$rest"
}

# within <path> <dir>: path is dir or inside it (both absolute, from abs_path)
within() { case "$1/" in "${2%/}/"*) return 0 ;; esac; return 1; }

cmd_run() {
    opts release checkout work-dir evidence-dir media-dir setup -- "$@"
    need release checkout work-dir evidence-dir media-dir setup
    known_release "$opt_release" || usage_die "--release $opt_release: want $(releases_said)"
    # refused here, before anything runs: the evidence dir is uploaded as an artifact, which the
    # disc must never be swept into, and gate.sh's own refusal of a media dir in its work dir would
    # read as a gate failure rather than as this call's mistake
    local m d
    m=$(abs_path "$opt_media_dir")
    for d in "$opt_evidence_dir" "$opt_work_dir"; do
        ! within "$m" "$(abs_path "$d")" ||
            usage_die "--media-dir $opt_media_dir is in $d: give it a directory of its own"
    done
    local rc=0 installer=() media out err why
    # no gate.sh first: with no control ref, the control job skips readying the runner too
    if [ ! -f "$opt_checkout/bin/gate.sh" ]; then
        warn "$opt_checkout has no bin/gate.sh"
        placeholder "$opt_evidence_dir" "the ref has no bin/gate.sh: it predates the gate, or there was no ref to check out"
        rc=3
    elif [ "$opt_setup" != success ]; then
        warn "the host setup did not succeed ($opt_setup): this runner could not run the gate"
        placeholder "$opt_evidence_dir" "the runner's host setup (bin/gate-host-setup.sh) did not succeed: $opt_setup; see the job's log"
        rc=3
    elif [ "$opt_release" = 10.6 ]; then
        # the fetcher runs from this checkout's root, so a relative media dir is made absolute here
        if ! why=$(mkdir -p "$opt_media_dir" 2>&1) || ! media=$(cd "$opt_media_dir" 2>&1 && pwd); then
            why=$(printf '%s\n%s\n' "$why" "${media:-}" | grep -v '^[[:space:]]*$' | tail -n 1 | tr '\t' ' ' || true)
            [ -n "$why" ] || why="cannot make or enter $opt_media_dir"
            warn "could not prepare the media dir: $why"
            placeholder "$opt_evidence_dir" "could not prepare the media dir: $why"
            rc=3
        else
            out=$(mktemp "${TMPDIR:-/tmp}/gate-ci.XXXXXX") || die "cannot create a temp file"
            err=$(mktemp "${TMPDIR:-/tmp}/gate-ci.XXXXXX") || die "cannot create a temp file"
            log "fetching the 10.6 disc into $media"
            if fetch_disc "$media" "$out" "$err" && [ -f "$(head -n 1 "$out")" ]; then
                # the path is quoted throughout: the disc's cache name has a literal %20 in it
                installer=(--installer "$(head -n 1 "$out")")
            else
                # the fetcher's error is its last line that says fetch-pinned:, after go's own
                # lines (a toolchain download) and its progress; with none (killed before it could
                # say), the last line said at all. A tab in the note would split its steps.tsv row.
                why=$( { grep '^fetch-pinned:' "$err" || true; } | tail -n 1 | tr '\t' ' ')
                [ -n "$why" ] ||
                    why=$( { grep -v '^[[:space:]]*$' "$err" || true; } | tail -n 1 | tr '\t' ' ')
                [ -n "$why" ] || why="the fetcher printed no path to a file"
                warn "could not fetch the 10.6 disc: $why"
                placeholder "$opt_evidence_dir" "could not fetch the 10.6 disc: $why"
                rc=3
            fi
            cat "$err" >&2
            rm -f "$out" "$err"
        fi
    fi
    if [ "$rc" -eq 0 ]; then
        log "running $opt_checkout/bin/gate.sh --release $opt_release"
        "$opt_checkout/bin/gate.sh" --release "$opt_release" ${installer[@]+"${installer[@]}"} \
            --work-dir "$opt_work_dir" --evidence-dir "$opt_evidence_dir" || rc=$?
    fi
    log "gate.sh exit $rc"
    output "exit=$rc"
    return 0
}

cmd_control_ref() {
    opts event given pr-base -- "$@"
    need event given pr-base
    local ref=
    cd "$GUEST_MACOSX_REPO_ROOT"
    if [ -n "$opt_given" ]; then
        ref=$opt_given
    else
        case $opt_event in
            pull_request) ref=$opt_pr_base ;;
            workflow_call) ref=$(git describe --tags --abbrev=0 --match 'v0.*' HEAD^ 2>/dev/null || true) ;;
            workflow_dispatch) ref=$(git merge-base origin/main HEAD 2>/dev/null || true) ;;
            *) usage_die "--event $opt_event: want pull_request, workflow_call or workflow_dispatch" ;;
        esac
    fi
    [ -n "$ref" ] || warn "no control ref for a $opt_event here: a control would have nothing to check out"
    printf '%s\n' "$ref"
}

# verdict_one <candidate> <candidate-job> <candidate-exit> <retry> <retry-job> <retry-exit> <control>:
# one release's verdict as Markdown on stdout, its first line the verdict; returns 0 for a pass and
# 1 for anything else. The candidate is the retry's evidence if the retry ran, else the candidate's.
verdict_one() {
    local dir=$1 job=candidate result=$2 ex=$3
    case $5 in
        ''|skipped) ;;
        *) dir=$4 job=candidate-retry result=$5 ex=$6 ;;
    esac
    if [ ! -f "$dir/steps.tsv" ]; then
        printf 'No clear regression: CI itself failed at %s -- no steps.tsv (job %s, gate.sh exit %s)\n' \
            "$job" "$result" "${ex:-none}"
        return 1
    fi
    local out err rc=0
    err=$(mktemp "${TMPDIR:-/tmp}/gate-ci.XXXXXX") || die "cannot create a temp file"
    out=$("$GUEST_MACOSX_REPO_ROOT/bin/gate-summary.sh" "$dir" "$7" 2>"$err") || rc=$?
    if [ "$rc" -eq 2 ]; then
        printf 'No clear regression: CI itself failed at summary -- %s\n' "$(head -n 1 "$err")"
        rm -f "$err"
        return 1
    fi
    rm -f "$err"
    printf '%s\n' "$out"
    [ "$rc" -eq 0 ] || return 1
    return 0
}

# leg_file <artifact-dir> <file> <no-artifact> <no-file>: one line the leg's job wrote beside its
# evidence; else <no-artifact> for a leg with no artifact (a job that never ran uploaded nothing),
# or <no-file> for an artifact without the file (a job that ran, then stopped before writing it).
# An exit written as GITHUB_OUTPUT has it (exit=N) is read as its N.
leg_file() {
    local v
    if [ -f "$1/$2" ]; then
        v=$(head -n 1 "$1/$2")
        printf '%s\n' "${v#exit=}"
    elif [ -d "$1" ]; then
        printf '%s\n' "$4"
    else
        printf '%s\n' "$3"
    fi
}

# verdict_release <release>: verdict_one for one release's artifacts in the artifacts dir
verdict_release() {
    local r=$1 base=$opt_artifacts/gate-$1 l got any=false
    for l in candidate candidate-retry control; do
        [ -d "$base-$l" ] || continue
        any=true
        # the artifact's name and the run inside it come from different places in the workflow: a
        # disagreement is its wiring at fault, and either release's verdict read from it would lie
        # gate.sh and the placeholder always write release.txt, so a candidate or retry run without
        # one was miswired too (gate-summary.sh would take it for 10.9); only a control, whose ref
        # may predate releases, can lack it
        if [ "$l" != control ] && [ -f "$base-$l/steps.tsv" ] && [ ! -f "$base-$l/release.txt" ]; then
            printf 'No clear regression: CI itself failed at wiring -- gate-%s-%s holds a run with no release.txt, not a %s run\n' \
                "$r" "$l" "$r"
            return 1
        fi
        if [ -f "$base-$l/release.txt" ]; then
            got=$(head -n 1 "$base-$l/release.txt")
            if [ "$got" != "$r" ]; then
                printf 'No clear regression: CI itself failed at wiring -- gate-%s-%s holds a %s run, not %s\n' \
                    "$r" "$l" "$got" "$r"
                return 1
            fi
        fi
    done
    if [ "$any" = false ]; then
        printf "No clear regression: CI itself failed at candidate -- no evidence from %s's gate at all (no gate-%s-* artifact)\n" \
            "$r" "$r"
        return 1
    fi
    verdict_one "$base-candidate" "$(leg_file "$base-candidate" job skipped unknown)" \
        "$(leg_file "$base-candidate" exit '' '')" "$base-candidate-retry" \
        "$(leg_file "$base-candidate-retry" job skipped unknown)" \
        "$(leg_file "$base-candidate-retry" exit '' '')" "$base-control"
}

# fail_line <line>: the verdict that is no gate-summary.sh's
fail_line() { printf '%s\n' "$1"; exit 1; }

cmd_verdict() {
    opts decision reason scope-job candidate candidate-job candidate-exit retry retry-job \
        retry-exit control releases artifacts -- "$@"
    need decision reason scope-job
    local r seen=" " n rc=0 out first='' passed='' body=''
    case $given in
        *" releases "*|*" artifacts "*)
            need releases artifacts
            for n in candidate candidate-job candidate-exit retry retry-job retry-exit control; do
                case $given in *" $n "*) usage_die "--$n is one release's: give --releases and --artifacts alone" ;; esac
            done
            for r in $opt_releases; do
                known_release "$r" || usage_die "--releases names $r: want $(releases_said)"
                case $seen in *" $r "*) usage_die "--releases names $r twice" ;; esac
                seen="$seen$r "
            done
            ;;
    esac
    case $opt_decision in
        not-gated) printf 'Not gated: %s\n' "$opt_reason"; exit 0 ;;
        pins-unfilled) fail_line "Pins not filled yet: $opt_reason" ;;
        '') fail_line "No clear regression: CI itself failed at scope -- it decided nothing (job $opt_scope_job)" ;;
        gated) ;;
        *) usage_die "--decision $opt_decision: want gated, not-gated or pins-unfilled" ;;
    esac
    case $given in
        *" releases "*) ;;
        *)
            need candidate candidate-job candidate-exit retry retry-job retry-exit control
            verdict_one "$opt_candidate" "$opt_candidate_job" "$opt_candidate_exit" "$opt_retry" \
                "$opt_retry_job" "$opt_retry_exit" "$opt_control" || rc=$?
            exit "$rc"
            ;;
    esac
    [ "$seen" != " " ] || usage_die "--releases names no release: a gated change gates at least one"
    # every release is decided, so the summary shows each one's evidence, but the first line is the
    # first failure's in the order given: the one a person reads, and the check's annotation, names it
    for r in $opt_releases; do
        rc=0
        out=$(verdict_release "$r") || rc=$?
        if [ "$rc" -eq 0 ]; then
            passed="$passed${passed:+, }$r"
        elif [ -z "$first" ]; then
            first="$r: $(printf '%s\n' "$out" | head -n 1)"
        fi
        body="$body"$'\n'"## $r"$'\n'"$out"$'\n'
    done
    if [ -n "$first" ]; then
        printf '%s\n%s' "$first" "$body"
        exit 1
    fi
    printf 'Passed: %s\n%s' "$passed" "$body"
    exit 0
}

[ "$#" -ge 1 ] || usage
sub=$1
shift
case $sub in
    run) cmd_run "$@" ;;
    control-ref) cmd_control_ref "$@" ;;
    verdict) cmd_verdict "$@" ;;
    *) usage ;;
esac
