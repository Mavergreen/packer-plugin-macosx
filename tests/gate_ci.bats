#!/usr/bin/env bats
# platform: host-agnostic
# shellcheck disable=SC2154  # $stderr is set by bats run --separate-stderr
#
# bin/gate-ci.sh is what .github/workflows/gate.yml (and gate-release.yml, its legs) runs around
# bin/gate.sh: `run` (one gate run on a runner, its exit for the next job), `control-ref` (what the
# control checks out), `scope-releases` (the releases the legs run, as the scope job's outputs) and
# `verdict` (the one line the required check `gate` reports). The checkout's bin/gate.sh here is a stand-in
# that records its arguments; bin/gate-summary.sh is the real one. No ssh, sudo or power command is
# reachable, so these tests need no stubs and are exempt from the stub-safety assertion (R9).

bats_require_minimum_version 1.5.0

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    CI="$REPO/bin/gate-ci.sh"
    OUT="$BATS_TEST_TMPDIR/github-output"
    : > "$OUT"
    export GITHUB_OUTPUT="$OUT"
    export FAKE_LOG_DIR="$BATS_TEST_TMPDIR"
}

# full <dir> [step:result:note]...: a complete 10.9 run of 3 restarts, every step pass but those
# given; every row after the first non-pass becomes skip. A step given as n/a is neither.
full() {
    local d=$1; shift
    local st o ov res note broke=0
    mkdir -p "$d"
    : > "$d/steps.tsv"
    for st in preflight plugin build-base build-layer varstore boot versions health reach host-uuid \
        restart-1 restart-2 restart-3 shutdown; do
        res=pass; note=
        for o in "$@"; do
            if [ "${o%%:*}" = "$st" ]; then ov=${o#*:}; res=${ov%%:*}; note=${ov#*:}; fi
        done
        if [ "$broke" = 1 ]; then res=skip; note=; fi
        case $res in pass|n/a) ;; *) broke=1 ;; esac
        printf '%s\t%s\t%s\t%s\n' "$st" "$res" 3 "$note" >> "$d/steps.tsv"
    done
}

# fake_checkout <dir> <exit> [writes-steps]: a checkout whose bin/gate.sh records its arguments
# and the API token in $FAKE_LOG_DIR, writes a passing steps.tsv into --evidence-dir when asked,
# and exits <exit>; what it does is read from files beside it
fake_checkout() {
    mkdir -p "$1/bin"
    printf '%s\n' "$2" > "$1/bin/exit"
    printf '%s\n' "${3:-}" > "$1/bin/mode"
    cat > "$1/bin/gate.sh" <<'EOF'
#!/usr/bin/env bash
here=$(dirname "$0")
printf '%s\n' "$@" > "$FAKE_LOG_DIR/gate.args"
printf '%s\n' "${PACKER_GITHUB_API_TOKEN:-}" > "$FAKE_LOG_DIR/gate.token"
ev=
while [ "$#" -gt 0 ]; do
    [ "$1" != --evidence-dir ] || ev=$2
    shift
done
if [ "$(cat "$here/mode")" = writes-steps ]; then
    mkdir -p "$ev"
    printf 'preflight\tpass\t1\t\n' > "$ev/steps.tsv"
fi
exit "$(cat "$here/exit")"
EOF
    chmod +x "$1/bin/gate.sh"
}

# --- run ----------------------------------------------------------------------------------------

@test "run: usage errors are exit 2" {
    fake_checkout "$BATS_TEST_TMPDIR/co" 0
    run "$CI"
    [ "$status" -eq 2 ]
    run "$CI" run --release 10.9 --media-dir "$BATS_TEST_TMPDIR/media" \
        --checkout "$BATS_TEST_TMPDIR"
    [ "$status" -eq 2 ]
    # --release and --media-dir are each required, even for 10.9, which uses no media dir
    run "$CI" run --media-dir "$BATS_TEST_TMPDIR/media" --checkout "$BATS_TEST_TMPDIR/co" \
        --work-dir /mnt/gate --evidence-dir "$BATS_TEST_TMPDIR/ev" --setup success
    [ "$status" -eq 2 ]
    run "$CI" run --release 10.9 --checkout "$BATS_TEST_TMPDIR/co" \
        --work-dir /mnt/gate --evidence-dir "$BATS_TEST_TMPDIR/ev" --setup success
    [ "$status" -eq 2 ]
    [ ! -e "$BATS_TEST_TMPDIR/gate.args" ]
    [ ! -s "$OUT" ]
    run "$CI" nonsense
    [ "$status" -eq 2 ]
}

@test "run: gates 10.9 alone, with the work and evidence dirs it is given, and passes the API token" {
    fake_checkout "$BATS_TEST_TMPDIR/co" 0 writes-steps
    PACKER_GITHUB_API_TOKEN=tok run "$CI" run --release 10.9 --media-dir "$BATS_TEST_TMPDIR/media" \
        --checkout "$BATS_TEST_TMPDIR/co" --work-dir /mnt/gate \
        --evidence-dir "$BATS_TEST_TMPDIR/ev" --setup success
    [ "$status" -eq 0 ]
    [ "$(cat "$BATS_TEST_TMPDIR/gate.args")" = "--release
10.9
--work-dir
/mnt/gate
--evidence-dir
$BATS_TEST_TMPDIR/ev" ]
    [ "$(cat "$BATS_TEST_TMPDIR/gate.token")" = tok ]
    grep -qx 'exit=0' "$OUT"
}

@test "run: records gate.sh's exit for the next job, and itself exits 0 so the job stays green" {
    for rc in 1 2 3; do
        : > "$OUT"
        fake_checkout "$BATS_TEST_TMPDIR/co$rc" "$rc"
        run "$CI" run --release 10.9 --media-dir "$BATS_TEST_TMPDIR/media" \
            --checkout "$BATS_TEST_TMPDIR/co$rc" --work-dir /mnt/gate \
            --evidence-dir "$BATS_TEST_TMPDIR/ev$rc" --setup success
        [ "$status" -eq 0 ]
        grep -qx "exit=$rc" "$OUT"
    done
}

@test "run: a host setup that failed is exit 3 and a steps.tsv saying so, so the retry runs" {
    fake_checkout "$BATS_TEST_TMPDIR/co" 0
    run "$CI" run --release 10.9 --media-dir "$BATS_TEST_TMPDIR/media" \
        --checkout "$BATS_TEST_TMPDIR/co" --work-dir /mnt/gate \
        --evidence-dir "$BATS_TEST_TMPDIR/ev" --setup failure
    [ "$status" -eq 0 ]
    grep -qx 'exit=3' "$OUT"
    [ ! -e "$BATS_TEST_TMPDIR/gate.args" ]
    run "$REPO/bin/gate-summary.sh" "$BATS_TEST_TMPDIR/ev"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "No clear regression: CI itself failed at preflight -- the runner's host setup (bin/gate-host-setup.sh) did not succeed: failure; see the job's log" ]
}

@test "run: a ref with no bin/gate.sh (it predates the gate) writes a whole steps.tsv, never a crash" {
    mkdir -p "$BATS_TEST_TMPDIR/old"
    run "$CI" run --release 10.9 --media-dir "$BATS_TEST_TMPDIR/media" \
        --checkout "$BATS_TEST_TMPDIR/old" --work-dir /mnt/gate \
        --evidence-dir "$BATS_TEST_TMPDIR/ctl" --setup success
    [ "$status" -eq 0 ]
    grep -qx 'exit=3' "$OUT"
    full "$BATS_TEST_TMPDIR/cand" "versions:fail:virtualsmc: want 1.3.8"
    run "$REPO/bin/gate-summary.sh" "$BATS_TEST_TMPDIR/cand" "$BATS_TEST_TMPDIR/ctl"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "No clear regression: the control could not run -- preflight -- the ref has no bin/gate.sh: it predates the gate, or there was no ref to check out" ]
}

@test "run: the placeholder is bin/gate.sh's step order with its default restart count" {
    mkdir -p "$BATS_TEST_TMPDIR/old"
    run "$CI" run --release 10.9 --media-dir "$BATS_TEST_TMPDIR/media" \
        --checkout "$BATS_TEST_TMPDIR/old" --work-dir /mnt/gate \
        --evidence-dir "$BATS_TEST_TMPDIR/ctl" --setup success
    [ "$status" -eq 0 ]
    fixed=$(sed -n 's/^STEPS=(\(.*\))$/\1/p' "$REPO/bin/gate.sh")
    n=$(sed -n 's/^restarts=\([0-9][0-9]*\)$/\1/p' "$REPO/bin/gate.sh")
    [ -n "$fixed" ]
    [ -n "$n" ]
    want=$fixed
    i=1
    while [ "$i" -le "$n" ]; do want="$want restart-$i"; i=$((i + 1)); done
    want="$want shutdown"
    [ "$(cut -f1 "$BATS_TEST_TMPDIR/ctl/steps.tsv" | tr '\n' ' ')" = "$want " ]
}

@test "run: no control ref at all (its setup skipped) still says the ref has no gate, not a setup failure" {
    run "$CI" run --release 10.9 --media-dir "$BATS_TEST_TMPDIR/media" \
        --checkout "$BATS_TEST_TMPDIR/absent" --work-dir /mnt/gate \
        --evidence-dir "$BATS_TEST_TMPDIR/ctl" --setup skipped
    [ "$status" -eq 0 ]
    grep -qx 'exit=3' "$OUT"
    [ "$(head -n 1 "$BATS_TEST_TMPDIR/ctl/steps.tsv" | cut -f4)" = "the ref has no bin/gate.sh: it predates the gate, or there was no ref to check out" ]
}

# fake_fetcher [fails]: a stand-in for `go run ./cmd/fetch-pinned` (GUEST_MACOSX_GATE_FETCH_PINNED)
# that records its arguments and the directory it ran in, then either makes a file in the dir it
# is given and prints its path, or says why it could not, after go's and its own progress lines, as
# the real one does, and exits 1. The file's name has a literal %20, as the 10.6 disc's cache name
# does.
fake_fetcher() {
    local f=$BATS_TEST_TMPDIR/fake-fetch-pinned
    cat > "$f" <<'SH'
#!/usr/bin/env bash
printf '%s\n' "$@" > "$FAKE_LOG_DIR/fetch.args"
if [ "${FAKE_FETCH_FAILS:-}" = 1 ]; then
    # as the real one says it: go's own lines and progress first, the error last
    printf 'go: downloading go1.26.11 (linux/amd64)\n' >&2
    printf 'snowleopard-disc-10A432: 100 of 7000 MiB\n' >&2
    printf 'fetch-pinned: snowleopard-disc-10A432: sha256 mismatch\n\n' >&2
    exit 1
fi
if [ "${FAKE_FETCH_FAILS:-}" = 2 ]; then
    # killed before it could say why: go's own line, then go run's
    printf 'go: downloading go1.26.11 (linux/amd64)\nsignal: killed\n\n' >&2
    exit 1
fi
mkdir -p "$2"
p="$2/Mac%20OS%20X%2010.6.dmg"
: > "$p"
printf '%s\n' "$p"
SH
    chmod +x "$f"
    export GUEST_MACOSX_GATE_FETCH_PINNED="$f"
}

@test "run: 10.6 fetches its pinned disc into the media dir, then gates with it as the installer" {
    fake_checkout "$BATS_TEST_TMPDIR/co" 0 writes-steps
    fake_fetcher
    run "$CI" run --release 10.6 --media-dir "$BATS_TEST_TMPDIR/media" \
        --checkout "$BATS_TEST_TMPDIR/co" --work-dir /mnt/gate \
        --evidence-dir "$BATS_TEST_TMPDIR/ev" --setup success
    [ "$status" -eq 0 ]
    [ "$(cat "$BATS_TEST_TMPDIR/fetch.args")" = "snowleopard-disc-10A432
$BATS_TEST_TMPDIR/media" ]
    [ "$(cat "$BATS_TEST_TMPDIR/gate.args")" = "--release
10.6
--installer
$BATS_TEST_TMPDIR/media/Mac%20OS%20X%2010.6.dmg
--work-dir
/mnt/gate
--evidence-dir
$BATS_TEST_TMPDIR/ev" ]
    grep -qx 'exit=0' "$OUT"
}

@test "run: 10.9 fetches nothing: its gate.sh fetches its own InstallESD" {
    fake_checkout "$BATS_TEST_TMPDIR/co" 0
    fake_fetcher
    run "$CI" run --release 10.9 --media-dir "$BATS_TEST_TMPDIR/media" \
        --checkout "$BATS_TEST_TMPDIR/co" --work-dir /mnt/gate \
        --evidence-dir "$BATS_TEST_TMPDIR/ev" --setup success
    [ "$status" -eq 0 ]
    [ ! -e "$BATS_TEST_TMPDIR/fetch.args" ]
    [ ! -e "$BATS_TEST_TMPDIR/media" ]
    grep -qx -- '--release' "$BATS_TEST_TMPDIR/gate.args"
    run ! grep -qx -- '--installer' "$BATS_TEST_TMPDIR/gate.args"
}

@test "run: a release the gate does not know is a usage error, and runs nothing" {
    fake_checkout "$BATS_TEST_TMPDIR/co" 0
    fake_fetcher
    run --separate-stderr "$CI" run --release 10.5 --media-dir "$BATS_TEST_TMPDIR/media" \
        --checkout "$BATS_TEST_TMPDIR/co" --work-dir /mnt/gate \
        --evidence-dir "$BATS_TEST_TMPDIR/ev" --setup success
    [ "$status" -eq 2 ]
    [[ "$stderr" == *"10.5"* ]] || false
    [ ! -e "$BATS_TEST_TMPDIR/fetch.args" ]
    [ ! -e "$BATS_TEST_TMPDIR/gate.args" ]
    [ ! -s "$OUT" ]
}

@test "run: a 10.6 disc that could not be fetched is exit 3 and a 10.6 steps.tsv naming the disc" {
    fake_checkout "$BATS_TEST_TMPDIR/co" 0
    fake_fetcher
    FAKE_FETCH_FAILS=1 run "$CI" run --release 10.6 --media-dir "$BATS_TEST_TMPDIR/media" \
        --checkout "$BATS_TEST_TMPDIR/co" --work-dir /mnt/gate \
        --evidence-dir "$BATS_TEST_TMPDIR/ev" --setup success
    [ "$status" -eq 0 ]
    grep -qx 'exit=3' "$OUT"
    [ ! -e "$BATS_TEST_TMPDIR/gate.args" ]
    [ "$(cat "$BATS_TEST_TMPDIR/ev/release.txt")" = 10.6 ]
    run "$REPO/bin/gate-summary.sh" "$BATS_TEST_TMPDIR/ev"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "No clear regression: CI itself failed at preflight -- could not fetch the 10.6 disc: fetch-pinned: snowleopard-disc-10A432: sha256 mismatch" ]
}

@test "run: a fetcher that never said fetch-pinned: is named by its last line" {
    fake_checkout "$BATS_TEST_TMPDIR/co" 0
    fake_fetcher
    FAKE_FETCH_FAILS=2 run "$CI" run --release 10.6 --media-dir "$BATS_TEST_TMPDIR/media" \
        --checkout "$BATS_TEST_TMPDIR/co" --work-dir /mnt/gate \
        --evidence-dir "$BATS_TEST_TMPDIR/ev" --setup success
    [ "$status" -eq 0 ]
    grep -qx 'exit=3' "$OUT"
    [ "$(head -n 1 "$BATS_TEST_TMPDIR/ev/steps.tsv" | cut -f4)" = "could not fetch the 10.6 disc: signal: killed" ]
}

@test "run: a 10.6 host setup that failed fetches nothing, and its placeholder is a 10.6 run" {
    fake_checkout "$BATS_TEST_TMPDIR/co" 0
    fake_fetcher
    run "$CI" run --release 10.6 --media-dir "$BATS_TEST_TMPDIR/media" \
        --checkout "$BATS_TEST_TMPDIR/co" --work-dir /mnt/gate \
        --evidence-dir "$BATS_TEST_TMPDIR/ev" --setup failure
    [ "$status" -eq 0 ]
    grep -qx 'exit=3' "$OUT"
    [ ! -e "$BATS_TEST_TMPDIR/fetch.args" ]
    [ "$(cat "$BATS_TEST_TMPDIR/ev/release.txt")" = 10.6 ]
    # a 10.6 candidate's placeholder compares with a 10.6 control, never as a 10.9 run
    full "$BATS_TEST_TMPDIR/ctl"
    printf '10.6\n' > "$BATS_TEST_TMPDIR/ctl/release.txt"
    run "$REPO/bin/gate-summary.sh" "$BATS_TEST_TMPDIR/ev" "$BATS_TEST_TMPDIR/ctl"
    [ "$status" -eq 1 ]
    [[ "${lines[0]}" == "No clear regression: CI itself failed at preflight -- "* ]] || false
}

@test "run: a control's older checkout gets the disc this checkout's fetcher fetched" {
    # the control runs the control ref's gate.sh, but the fetch is this checkout's, run from this
    # checkout's root: an older ref may have no fetcher, or one that pins another disc
    fake_checkout "$BATS_TEST_TMPDIR/control" 0
    fake_fetcher
    export FAKE_FETCH_INNER="$GUEST_MACOSX_GATE_FETCH_PINNED"
    cat > "$FAKE_FETCH_INNER.wrap" <<'SH'
#!/usr/bin/env bash
pwd > "$FAKE_LOG_DIR/fetch.pwd"
exec "$FAKE_FETCH_INNER" "$@"
SH
    chmod +x "$FAKE_FETCH_INNER.wrap"
    GUEST_MACOSX_GATE_FETCH_PINNED="$FAKE_FETCH_INNER.wrap"
    cd "$BATS_TEST_TMPDIR"
    run "$CI" run --release 10.6 --media-dir media \
        --checkout control --work-dir /mnt/gate --evidence-dir "$BATS_TEST_TMPDIR/ev" --setup success
    [ "$status" -eq 0 ]
    [ "$(cat "$BATS_TEST_TMPDIR/fetch.pwd")" = "$REPO" ]
    [ "$(sed -n 4p "$BATS_TEST_TMPDIR/gate.args")" = "$BATS_TEST_TMPDIR/media/Mac%20OS%20X%2010.6.dmg" ]
    [ -f "$BATS_TEST_TMPDIR/media/Mac%20OS%20X%2010.6.dmg" ]
}

@test "run: a media dir in the evidence or work dir is a usage error, before anything runs" {
    # the evidence dir is uploaded as an artifact: the disc must never be swept into it
    fake_checkout "$BATS_TEST_TMPDIR/co" 0
    fake_fetcher
    mkdir -p "$BATS_TEST_TMPDIR/ev" "$BATS_TEST_TMPDIR/work"
    for m in "$BATS_TEST_TMPDIR/ev" "$BATS_TEST_TMPDIR/ev/media" "$BATS_TEST_TMPDIR/ev/./x/../media" \
        "$BATS_TEST_TMPDIR/work" "$BATS_TEST_TMPDIR/work/media/" "$BATS_TEST_TMPDIR/work/new/media"; do
        for r in 10.6 10.9; do
            run --separate-stderr "$CI" run --release "$r" --media-dir "$m" \
                --checkout "$BATS_TEST_TMPDIR/co" --work-dir "$BATS_TEST_TMPDIR/work" \
                --evidence-dir "$BATS_TEST_TMPDIR/ev" --setup success
            [ "$status" -eq 2 ]
            [[ "$stderr" == *"--media-dir"* ]] || false
        done
    done
    # a relative media dir is resolved before it is compared
    cd "$BATS_TEST_TMPDIR"
    run "$CI" run --release 10.6 --media-dir work/media \
        --checkout "$BATS_TEST_TMPDIR/co" --work-dir "$BATS_TEST_TMPDIR/work" \
        --evidence-dir "$BATS_TEST_TMPDIR/ev" --setup success
    [ "$status" -eq 2 ]
    [ ! -e "$BATS_TEST_TMPDIR/fetch.args" ]
    [ ! -e "$BATS_TEST_TMPDIR/gate.args" ]
    [ ! -e "$BATS_TEST_TMPDIR/ev/steps.tsv" ]
    [ ! -s "$OUT" ]
    # a sibling whose name merely starts with the work dir's is not inside it
    run "$CI" run --release 10.6 --media-dir "$BATS_TEST_TMPDIR/work-media" \
        --checkout "$BATS_TEST_TMPDIR/co" --work-dir "$BATS_TEST_TMPDIR/work" \
        --evidence-dir "$BATS_TEST_TMPDIR/ev" --setup success
    [ "$status" -eq 0 ]
    grep -qx 'exit=0' "$OUT"
}

@test "run: a media dir that cannot be made is exit 3 and a steps.tsv saying so, not a crash" {
    fake_checkout "$BATS_TEST_TMPDIR/co" 0
    fake_fetcher
    : > "$BATS_TEST_TMPDIR/a-file"
    run "$CI" run --release 10.6 --media-dir "$BATS_TEST_TMPDIR/a-file/media" \
        --checkout "$BATS_TEST_TMPDIR/co" --work-dir /mnt/gate \
        --evidence-dir "$BATS_TEST_TMPDIR/ev" --setup success
    [ "$status" -eq 0 ]
    grep -qx 'exit=3' "$OUT"
    [ ! -e "$BATS_TEST_TMPDIR/fetch.args" ]
    [ ! -e "$BATS_TEST_TMPDIR/gate.args" ]
    [ "$(cat "$BATS_TEST_TMPDIR/ev/release.txt")" = 10.6 ]
    [[ "$(head -n 1 "$BATS_TEST_TMPDIR/ev/steps.tsv" | cut -f4)" == "could not prepare the media dir: "?* ]] || false
}

# --- control-ref --------------------------------------------------------------------------------

mkrepo() {
    R="$BATS_TEST_TMPDIR/repo"
    mkdir -p "$R/bin" "$R/lib"
    cp "$CI" "$REPO/bin/gate-summary.sh" "$R/bin/"
    cp "$REPO/lib/common.sh" "$R/lib/"
    git -C "$R" init -q -b main
    git -C "$R" config user.email t@example.invalid
    git -C "$R" config user.name t
    git -C "$R" config commit.gpgsign false
    git -C "$R" add -A
    git -C "$R" commit -qm one
    git -C "$R" tag v0.20261031.1
    echo 2 > "$R/f"; git -C "$R" add f; git -C "$R" commit -qm two
    git -C "$R" tag v0.20261105.3
    git -C "$R" tag not-a-release
    echo 3 > "$R/f"; git -C "$R" commit -qam three
}

@test "control-ref: a ref given wins" {
    mkrepo
    run "$R/bin/gate-ci.sh" control-ref --event workflow_call --given abc123 --pr-base def456
    [ "$status" -eq 0 ]
    [ "$output" = abc123 ]
}

@test "control-ref: a PR's control is its base" {
    mkrepo
    run "$R/bin/gate-ci.sh" control-ref --event pull_request --given '' --pr-base def456
    [ "$output" = def456 ]
}

@test "control-ref: a release's control is the newest v0.* release before HEAD" {
    mkrepo
    run "$R/bin/gate-ci.sh" control-ref --event workflow_call --given '' --pr-base ''
    [ "$status" -eq 0 ]
    [ "$output" = v0.20261105.3 ]
}

@test "control-ref: a release whose HEAD is itself tagged still compares with the release before" {
    mkrepo
    git -C "$R" tag v0.20261105.4
    run "$R/bin/gate-ci.sh" control-ref --event workflow_call --given '' --pr-base ''
    [ "$output" = v0.20261105.3 ]
}

@test "control-ref: a dispatch's control is the merge base with origin/main" {
    mkrepo
    base=$(git -C "$R" rev-parse HEAD)
    git -C "$R" update-ref refs/remotes/origin/main "$base"
    git -C "$R" checkout -q -b renovate/x
    echo 4 > "$R/f"; git -C "$R" commit -qam four
    run "$R/bin/gate-ci.sh" control-ref --event workflow_dispatch --given '' --pr-base ''
    [ "$output" = "$base" ]
}

@test "control-ref: none to be found is empty output and a warning, not a failure" {
    mkrepo
    git -C "$R" tag -d v0.20261031.1 v0.20261105.3 >/dev/null
    run --separate-stderr "$R/bin/gate-ci.sh" control-ref --event workflow_call --given '' --pr-base ''
    [ "$status" -eq 0 ]
    [ -z "$output" ]
    [[ "$stderr" == *"no control ref"* ]] || false
}

# --- verdict ------------------------------------------------------------------------------------

verdict() { run --separate-stderr "$CI" verdict "$@"; }

@test "verdict: not gated passes, saying why" {
    verdict --decision not-gated --reason "not a Renovate update" --scope-job success
    [ "$status" -eq 0 ]
    [ "${lines[0]}" = "Not gated: not a Renovate update" ]
}

@test "verdict: pins not filled fails, saying what to do" {
    verdict --decision pins-unfilled --reason "re-pin it" --scope-job success
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "Pins not filled yet: re-pin it" ]
}

@test "verdict: a scope that decided nothing is CI itself failing at scope" {
    verdict --decision '' --reason '' --scope-job failure
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "No clear regression: CI itself failed at scope -- it decided nothing (job failure)" ]
}

@test "verdict: an unknown decision is a usage error" {
    verdict --decision maybe --reason x --scope-job success
    [ "$status" -eq 2 ]
}

# --- verdict over several releases --------------------------------------------------------------

# leg <release> <leg> <job> <exit> [step:result:note]...: the artifact gate-<release>-<leg> in $A
# as the gate job downloads it: that leg's evidence (a whole run of <release>, its release.txt
# included, every step pass but those given), its job's result in `job` and gate-ci.sh's exit in
# `exit`, and the run attempt it ran in, 1, in `attempt` (`attempt_of` changes it). A 10.6 run's
# build-layer and reach are n/a, as bin/gate.sh writes them.
leg() {
    local r=$1 l=$2 j=$3 x=$4 d
    shift 4
    d=$A/gate-$r-$l
    if [ "$r" = 10.6 ]; then
        full "$d" "build-layer:n/a:no layer for 10.6" "reach:n/a:no network check for 10.6" "$@"
    else
        full "$d" "$@"
    fi
    printf '%s\n' "$r" > "$d/release.txt"
    printf '%s\n' "$j" > "$d/job"
    printf 'exit=%s\n' "$x" > "$d/exit"
    printf '1\n' > "$d/attempt"
}

# attempt_of <release> <leg> <n>: that leg's artifact ran in run attempt <n>
attempt_of() { printf '%s\n' "$3" > "$A/gate-$1-$2/attempt"; }

verdicts() {
    run --separate-stderr "$CI" verdict --decision gated --reason x --scope-job success --artifacts "$A" "$@"
}

@test "verdicts: every release passing is Passed, naming them, exit 0, a section each" {
    A=$BATS_TEST_TMPDIR/a
    leg 10.9 candidate success 0
    leg 10.6 candidate success 0
    verdicts --releases "10.9 10.6"
    [ "$status" -eq 0 ]
    [ "${lines[0]}" = "Passed: 10.9, 10.6" ]
    [[ "$output" == *"| restart-3 | pass |"* ]] || false
    [[ "$output" == *$'\n## 10.9\nPassed: every step, 3 restarts\n'* ]] || false
    [[ "$output" == *$'\n## 10.6\nPassed: every step that applies, 3 restarts; not applicable: build-layer, reach\n'* ]] || false
}

@test "verdicts: one release regressing fails, its own first line prefixed with it" {
    A=$BATS_TEST_TMPDIR/a
    leg 10.9 candidate success 0
    leg 10.6 candidate success 1 "versions:fail:virtualsmc: want 1.3.8"
    leg 10.6 control success 0
    verdicts --releases "10.9 10.6"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "10.6: Regression: versions -- virtualsmc: want 1.3.8" ]
    [[ "$output" == *$'\n## 10.9\nPassed: every step, 3 restarts\n'* ]] || false
    [[ "$output" == *$'\n## 10.6\nRegression: versions -- virtualsmc: want 1.3.8\n'* ]] || false
}

@test "verdicts: the first line is the first failing release's, in the order given" {
    A=$BATS_TEST_TMPDIR/a
    leg 10.9 candidate success 1 "health:fail:sshd down"
    leg 10.9 control success 0
    leg 10.6 candidate success 1 "versions:fail:virtualsmc: want 1.3.8"
    leg 10.6 control success 0
    verdicts --releases "10.6 10.9"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "10.6: Regression: versions -- virtualsmc: want 1.3.8" ]
    [[ "$output" == *$'\n## 10.9\nRegression: health -- sshd down\n'* ]] || false
}

@test "verdicts: a release with no artifact at all fails, naming it, though the other passed" {
    A=$BATS_TEST_TMPDIR/a
    leg 10.9 candidate success 0
    verdicts --releases "10.9 10.6"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "10.6: No clear regression: CI itself failed at candidate -- no evidence from 10.6's gate at all (no gate-10.6-* artifact)" ]
    [[ "$output" == *$'\n## 10.6\nNo clear regression: CI itself failed at candidate -- no evidence'* ]] || false
}

@test "verdicts: a retry that ran supersedes the candidate, as with one release" {
    A=$BATS_TEST_TMPDIR/a
    leg 10.6 candidate success 3 "build-base:infra:no space left on device"
    leg 10.6 candidate-retry success 0
    verdicts --releases "10.6"
    [ "$status" -eq 0 ]
    [ "${lines[0]}" = "Passed: 10.6" ]
}

@test "verdicts: a retry that was skipped leaves the candidate's evidence the verdict's" {
    A=$BATS_TEST_TMPDIR/a
    leg 10.9 candidate success 0
    leg 10.9 candidate-retry skipped ''
    rm "$A/gate-10.9-candidate-retry/steps.tsv"
    verdicts --releases "10.9"
    [ "$status" -eq 0 ]
    [ "${lines[0]}" = "Passed: 10.9" ]
}

# Artifacts belong to the run, not the attempt: "Re-run all jobs" leaves the last attempt's
# gate-<release>-candidate-retry and -control in place when this attempt skips them.
@test "verdicts: a failing retry left from an earlier attempt never fails a candidate that now passes" {
    A=$BATS_TEST_TMPDIR/a
    leg 10.9 candidate success 0
    attempt_of 10.9 candidate 2
    leg 10.9 candidate-retry success 3 "preflight:infra:no space left on device"
    verdicts --releases "10.9"
    [ "$status" -eq 0 ]
    [ "${lines[0]}" = "Passed: 10.9" ]
}

@test "verdicts: a passing retry left from an earlier attempt never passes a candidate that now fails" {
    A=$BATS_TEST_TMPDIR/a
    leg 10.9 candidate success 1 "versions:fail:virtualsmc: want 1.3.8"
    attempt_of 10.9 candidate 2
    leg 10.9 candidate-retry success 0
    leg 10.9 control success 0
    attempt_of 10.9 control 2
    verdicts --releases "10.9"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "10.9: Regression: versions -- virtualsmc: want 1.3.8" ]
}

@test "verdicts: a retry or control with no attempt beside a candidate with one is from an earlier attempt" {
    A=$BATS_TEST_TMPDIR/a
    leg 10.9 candidate success 0
    attempt_of 10.9 candidate 2
    leg 10.9 candidate-retry success 3 "preflight:infra:no space left on device"
    rm "$A/gate-10.9-candidate-retry/attempt"
    verdicts --releases "10.9"
    [ "$status" -eq 0 ]
    [ "${lines[0]}" = "Passed: 10.9" ]
    rm -rf "$A"
    leg 10.9 candidate success 1 "versions:fail:virtualsmc: want 1.3.8"
    attempt_of 10.9 candidate 2
    leg 10.9 control success 1 "versions:fail:virtualsmc: want 1.3.8"
    rm "$A/gate-10.9-control/attempt"
    verdicts --releases "10.9"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "10.9: No clear regression: the control could not run -- control -- no steps.tsv in (no control directory given)" ]
}

# A retry runs only after its candidate, in the candidate's attempt, so a retry with no candidate
# beside it (or a candidate that says no attempt beside a retry that says one) is miswired or
# left over: whatever the retry says, it is never this release's verdict.
@test "verdicts: a retry with no candidate artifact beside it fails as wiring, whatever it says" {
    A=$BATS_TEST_TMPDIR/a
    leg 10.9 candidate-retry success 0
    verdicts --releases "10.9"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "10.9: No clear regression: CI itself failed at wiring -- gate-10.9-candidate missing beside gate-10.9-candidate-retry" ]
}

@test "verdicts: a candidate with no attempt beside a retry with one fails as wiring" {
    A=$BATS_TEST_TMPDIR/a
    leg 10.9 candidate success 3 "build-base:infra:no space left on device"
    rm "$A/gate-10.9-candidate/attempt"
    leg 10.9 candidate-retry success 0
    verdicts --releases "10.9"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "10.9: No clear regression: CI itself failed at wiring -- gate-10.9-candidate has no attempt beside gate-10.9-candidate-retry's" ]
}

@test "verdicts: a control left from an earlier attempt is no control, never a pass for this one" {
    A=$BATS_TEST_TMPDIR/a
    leg 10.9 candidate success 1 "versions:fail:virtualsmc: want 1.3.8"
    attempt_of 10.9 candidate 2
    leg 10.9 control success 1 "versions:fail:virtualsmc: want 1.3.8"
    verdicts --releases "10.9"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "10.9: No clear regression: the control could not run -- control -- no steps.tsv in (no control directory given)" ]
}

@test "verdicts: a leg left from an earlier attempt is not read for wiring either" {
    A=$BATS_TEST_TMPDIR/a
    leg 10.9 candidate success 0
    attempt_of 10.9 candidate 2
    leg 10.9 candidate-retry success 0
    rm "$A/gate-10.9-candidate-retry/release.txt"
    verdicts --releases "10.9"
    [ "$status" -eq 0 ]
    [ "${lines[0]}" = "Passed: 10.9" ]
}

@test "verdicts: a retry of the candidate's attempt, or a later one, still supersedes it" {
    A=$BATS_TEST_TMPDIR/a
    leg 10.6 candidate success 3 "build-base:infra:no space left on device"
    attempt_of 10.6 candidate 2
    leg 10.6 candidate-retry success 0
    attempt_of 10.6 candidate-retry 2
    verdicts --releases "10.6"
    [ "$status" -eq 0 ]
    [ "${lines[0]}" = "Passed: 10.6" ]
    leg 10.6 candidate-retry success 1 "versions:fail:virtualsmc: want 1.3.8"
    attempt_of 10.6 candidate-retry 3
    verdicts --releases "10.6"
    [ "$status" -eq 1 ]
    [[ "${lines[0]}" == "10.6: No clear regression: the control could not run -- "* ]] || false
}

@test "verdicts: a candidate artifact with no steps.tsv names its job's result and gate.sh's exit" {
    A=$BATS_TEST_TMPDIR/a
    leg 10.9 candidate cancelled 2
    rm "$A/gate-10.9-candidate/steps.tsv" "$A/gate-10.9-candidate/release.txt"
    verdicts --releases "10.9"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "10.9: No clear regression: CI itself failed at candidate -- no steps.tsv (job cancelled, gate.sh exit 2)" ]
}

@test "verdicts: an artifact with no job or exit file is a job that ran and said neither" {
    A=$BATS_TEST_TMPDIR/a
    leg 10.9 candidate success 0 "build-base:infra:no space left on device"
    # a candidate with no attempt either, so the retry is not taken for an earlier attempt's
    rm "$A/gate-10.9-candidate/attempt"
    mkdir -p "$A/gate-10.9-candidate-retry"
    verdicts --releases "10.9"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "10.9: No clear regression: CI itself failed at candidate-retry -- no steps.tsv (job unknown, gate.sh exit none)" ]
}

@test "verdicts: a missing candidate artifact beside a retry that never ran is as a candidate that never ran" {
    A=$BATS_TEST_TMPDIR/a
    leg 10.9 control success 0
    verdicts --releases "10.9"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "10.9: No clear regression: CI itself failed at candidate -- no steps.tsv (job skipped, gate.sh exit none)" ]
}

@test "verdicts: an artifact whose evidence is another release's fails, naming both" {
    A=$BATS_TEST_TMPDIR/a
    leg 10.6 candidate success 0
    printf '10.9\n' > "$A/gate-10.6-candidate/release.txt"
    verdicts --releases "10.6"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "10.6: No clear regression: CI itself failed at wiring -- gate-10.6-candidate holds a 10.9 run, not 10.6" ]
}

@test "verdicts: a candidate or retry run with no release.txt fails as wiring, naming the leg and release" {
    A=$BATS_TEST_TMPDIR/a
    leg 10.6 candidate success 0
    rm "$A/gate-10.6-candidate/release.txt"
    verdicts --releases "10.6"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "10.6: No clear regression: CI itself failed at wiring -- gate-10.6-candidate holds a run with no release.txt, not a 10.6 run" ]
    rm -rf "$A"
    leg 10.9 candidate success 3 "build-base:infra:no space left on device"
    leg 10.9 candidate-retry success 0
    rm "$A/gate-10.9-candidate-retry/release.txt"
    verdicts --releases "10.9"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "10.9: No clear regression: CI itself failed at wiring -- gate-10.9-candidate-retry holds a run with no release.txt, not a 10.9 run" ]
}

@test "verdicts: a control with no release.txt (an older ref's) is compared as before" {
    A=$BATS_TEST_TMPDIR/a
    leg 10.9 candidate success 1 "versions:fail:virtualsmc: want 1.3.8"
    leg 10.9 control success 0
    rm "$A/gate-10.9-control/release.txt"
    verdicts --releases "10.9"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "10.9: Regression: versions -- virtualsmc: want 1.3.8" ]
}

@test "verdicts: an exit file holding a bare N is read as that N" {
    A=$BATS_TEST_TMPDIR/a
    leg 10.9 candidate failure 3
    rm "$A/gate-10.9-candidate/steps.tsv"
    printf '3\n' > "$A/gate-10.9-candidate/exit"
    verdicts --releases "10.9"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "10.9: No clear regression: CI itself failed at candidate -- no steps.tsv (job failure, gate.sh exit 3)" ]
}

@test "verdicts: a release the gate does not know, or none, or a repeat, is a usage error" {
    A=$BATS_TEST_TMPDIR/a
    mkdir -p "$A"
    verdicts --releases "10.9 10.5"
    [ "$status" -eq 2 ]
    [[ "$stderr" == *"10.5"* ]] || false
    verdicts --releases ""
    [ "$status" -eq 2 ]
    verdicts --releases "10.9 10.9"
    [ "$status" -eq 2 ]
}

@test "verdicts: a gated verdict needs --releases and --artifacts, and the one-release flags are gone" {
    run --separate-stderr "$CI" verdict --decision gated --reason x --scope-job success --releases 10.9
    [ "$status" -eq 2 ]
    run --separate-stderr "$CI" verdict --decision gated --reason x --scope-job success
    [ "$status" -eq 2 ]
    A=$BATS_TEST_TMPDIR/a
    mkdir -p "$A"
    for f in --candidate --candidate-job --candidate-exit --retry --retry-job --retry-exit --control; do
        verdicts --releases 10.9 "$f" x
        [ "$status" -eq 2 ]
        [[ "$stderr" == *"unknown argument: $f"* ]] || false
    done
}

@test "verdicts: gate.sh's usage error (exit 2, no steps.tsv) is CI itself failing" {
    A=$BATS_TEST_TMPDIR/a
    mkdir -p "$A/gate-10.9-candidate"
    printf 'success\n' > "$A/gate-10.9-candidate/job"
    printf 'exit=2\n' > "$A/gate-10.9-candidate/exit"
    verdicts --releases 10.9
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "10.9: No clear regression: CI itself failed at candidate -- no steps.tsv (job success, gate.sh exit 2)" ]
}

@test "verdicts: a candidate failure with no control evidence is never the local-only Failed: line" {
    A=$BATS_TEST_TMPDIR/a
    leg 10.9 candidate success 1 "versions:fail:virtualsmc: want 1.3.8"
    verdicts --releases 10.9
    [ "$status" -eq 1 ]
    [[ "${lines[0]}" == "10.9: No clear regression: the control could not run -- control -- no steps.tsv in "* ]] || false
}

@test "verdicts: a malformed steps.tsv is CI itself failing at the summary, naming what is wrong" {
    A=$BATS_TEST_TMPDIR/a
    leg 10.9 candidate success 1
    printf 'preflight\tmaybe\t1\t\n' > "$A/gate-10.9-candidate/steps.tsv"
    verdicts --releases 10.9
    [ "$status" -eq 1 ]
    [[ "${lines[0]}" == "10.9: No clear regression: CI itself failed at summary -- "*'result "maybe"'* ]] || false
}

@test "verdicts: not gated and pins unfilled are as before, with or without releases" {
    run --separate-stderr "$CI" verdict --decision not-gated --reason "not a Renovate update" --scope-job success \
        --releases "" --artifacts "$BATS_TEST_TMPDIR/none"
    [ "$status" -eq 0 ]
    [ "${lines[0]}" = "Not gated: not a Renovate update" ]
    run --separate-stderr "$CI" verdict --decision pins-unfilled --reason "re-pin it" --scope-job success \
        --releases "" --artifacts "$BATS_TEST_TMPDIR/none"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "Pins not filled yet: re-pin it" ]
}

# --- scope-releases -----------------------------------------------------------------------------

# scope_releases <gate-scope.sh's output>: what the scope job adds to $GITHUB_OUTPUT from it
scope_releases() { run --separate-stderr "$CI" scope-releases <<< "$1"; }

@test "scope-releases: a gated change's releases, as a JSON array for the matrix and a list for the verdict" {
    scope_releases "$(printf 'gated changed: assets/pins/sources.tsv\nreleases 10.9 10.6')"
    [ "$status" -eq 0 ]
    [ "$output" = "$(printf '%s\n' 'releases=["10.9","10.6"]' 'release_list=10.9 10.6')" ]
    scope_releases "$(printf 'gated changed: x\nreleases 10.6')"
    [ "$status" -eq 0 ]
    [ "$output" = "$(printf '%s\n' 'releases=["10.6"]' 'release_list=10.6')" ]
}

# The matrix is never empty, as fromJSON of [] could fail the legs job and with it every change
# that is not gated: not gated, it names every release, and the legs job's `if` skips it anyway.
@test "scope-releases: not gated or pins unfilled verdict no release, yet the matrix is never empty" {
    for line in "not-gated not a Renovate update" "pins-unfilled re-pin it"; do
        scope_releases "$line"
        [ "$status" -eq 0 ]
        [ "$output" = "$(printf '%s\n' 'releases=["10.9","10.6"]' 'release_list=')" ]
    done
}

@test "scope-releases: anything gate-scope.sh does not print is a usage error, and prints nothing" {
    for in in "gated changed: x" "$(printf 'gated x\nreleases')" "$(printf 'gated x\nreleases 10.9 10.5')" \
        "$(printf 'gated x\nreleases 10.9 10.9')" "$(printf 'gated x\n10.9')" \
        "$(printf 'gated x\nreleases 10.9\nmore')" "$(printf 'not-gated x\nreleases 10.9')" \
        "maybe x" "" "$(printf 'gated x\nreleases *')"; do
        scope_releases "$in"
        [ "$status" -eq 2 ] || { echo "for: $in"; false; }
        [ -z "$output" ]
    done
    run --separate-stderr "$CI" scope-releases --releases 10.9 < /dev/null
    [ "$status" -eq 2 ]
}
