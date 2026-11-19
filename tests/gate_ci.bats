#!/usr/bin/env bats
# platform: host-agnostic
# shellcheck disable=SC2154  # $stderr is set by bats run --separate-stderr
#
# bin/gate-ci.sh is what .github/workflows/gate.yml runs around bin/gate.sh: `run` (one gate run on
# a runner, its exit for the next job), `control-ref` (what the control checks out) and `verdict`
# (the one line the required check `gate` reports). The checkout's bin/gate.sh here is a stand-in
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
# given; every row after the first non-pass becomes skip.
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
        [ "$res" = pass ] || broke=1
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
    run "$CI"
    [ "$status" -eq 2 ]
    run "$CI" run --checkout "$BATS_TEST_TMPDIR"
    [ "$status" -eq 2 ]
    run "$CI" nonsense
    [ "$status" -eq 2 ]
}

@test "run: gates 10.9 alone, with the work and evidence dirs it is given, and passes the API token" {
    fake_checkout "$BATS_TEST_TMPDIR/co" 0 writes-steps
    PACKER_GITHUB_API_TOKEN=tok run "$CI" run --checkout "$BATS_TEST_TMPDIR/co" \
        --work-dir /mnt/gate --evidence-dir "$BATS_TEST_TMPDIR/ev" --setup success
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
        run "$CI" run --checkout "$BATS_TEST_TMPDIR/co$rc" --work-dir /mnt/gate \
            --evidence-dir "$BATS_TEST_TMPDIR/ev$rc" --setup success
        [ "$status" -eq 0 ]
        grep -qx "exit=$rc" "$OUT"
    done
}

@test "run: a host setup that failed is exit 3 and a steps.tsv saying so, so the retry runs" {
    fake_checkout "$BATS_TEST_TMPDIR/co" 0
    run "$CI" run --checkout "$BATS_TEST_TMPDIR/co" --work-dir /mnt/gate \
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
    run "$CI" run --checkout "$BATS_TEST_TMPDIR/old" --work-dir /mnt/gate \
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
    run "$CI" run --checkout "$BATS_TEST_TMPDIR/old" --work-dir /mnt/gate \
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
    run "$CI" run --checkout "$BATS_TEST_TMPDIR/absent" --work-dir /mnt/gate \
        --evidence-dir "$BATS_TEST_TMPDIR/ctl" --setup skipped
    [ "$status" -eq 0 ]
    grep -qx 'exit=3' "$OUT"
    [ "$(head -n 1 "$BATS_TEST_TMPDIR/ctl/steps.tsv" | cut -f4)" = "the ref has no bin/gate.sh: it predates the gate, or there was no ref to check out" ]
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

@test "verdict: a candidate with no steps.tsv is CI itself failing, naming the job's result and gate.sh's exit" {
    verdict --decision gated --reason "a release" --scope-job success \
        --candidate "$BATS_TEST_TMPDIR/none" --candidate-job cancelled --candidate-exit '' \
        --retry "$BATS_TEST_TMPDIR/none2" --retry-job skipped --retry-exit '' \
        --control "$BATS_TEST_TMPDIR/none3"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "No clear regression: CI itself failed at candidate -- no steps.tsv (job cancelled, gate.sh exit none)" ]
}

@test "verdict: gate.sh's usage error (exit 2, no steps.tsv) is CI itself failing too" {
    verdict --decision gated --reason x --scope-job success \
        --candidate "$BATS_TEST_TMPDIR/none" --candidate-job success --candidate-exit 2 \
        --retry "$BATS_TEST_TMPDIR/none2" --retry-job skipped --retry-exit '' \
        --control "$BATS_TEST_TMPDIR/none3"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "No clear regression: CI itself failed at candidate -- no steps.tsv (job success, gate.sh exit 2)" ]
}

@test "verdict: a candidate that passed is Passed, exit 0, with the table under it" {
    full "$BATS_TEST_TMPDIR/cand"
    verdict --decision gated --reason x --scope-job success \
        --candidate "$BATS_TEST_TMPDIR/cand" --candidate-job success --candidate-exit 0 \
        --retry "$BATS_TEST_TMPDIR/none" --retry-job skipped --retry-exit '' \
        --control "$BATS_TEST_TMPDIR/none2"
    [ "$status" -eq 0 ]
    [ "${lines[0]}" = "Passed: every step, 3 restarts" ]
    [[ "$output" == *"| restart-3 | pass |"* ]] || false
}

@test "verdict: when the retry ran, its evidence is the candidate's" {
    full "$BATS_TEST_TMPDIR/cand" "build-base:infra:no space left on device"
    full "$BATS_TEST_TMPDIR/retry"
    verdict --decision gated --reason x --scope-job success \
        --candidate "$BATS_TEST_TMPDIR/cand" --candidate-job success --candidate-exit 3 \
        --retry "$BATS_TEST_TMPDIR/retry" --retry-job success --retry-exit 0 \
        --control "$BATS_TEST_TMPDIR/none"
    [ "$status" -eq 0 ]
    [ "${lines[0]}" = "Passed: every step, 3 restarts" ]
}

@test "verdict: a retry with no steps.tsv is CI itself failing at candidate-retry" {
    full "$BATS_TEST_TMPDIR/cand" "build-base:infra:no space left on device"
    verdict --decision gated --reason x --scope-job success \
        --candidate "$BATS_TEST_TMPDIR/cand" --candidate-job success --candidate-exit 3 \
        --retry "$BATS_TEST_TMPDIR/none" --retry-job failure --retry-exit '' \
        --control "$BATS_TEST_TMPDIR/none2"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "No clear regression: CI itself failed at candidate-retry -- no steps.tsv (job failure, gate.sh exit none)" ]
}

@test "verdict: the candidate fails a check and the control passes it: Regression" {
    full "$BATS_TEST_TMPDIR/cand" "versions:fail:virtualsmc: want 1.3.8, got 1.3.7"
    full "$BATS_TEST_TMPDIR/ctl"
    verdict --decision gated --reason x --scope-job success \
        --candidate "$BATS_TEST_TMPDIR/cand" --candidate-job success --candidate-exit 1 \
        --retry "$BATS_TEST_TMPDIR/none" --retry-job skipped --retry-exit '' \
        --control "$BATS_TEST_TMPDIR/ctl"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "Regression: versions -- virtualsmc: want 1.3.8, got 1.3.7" ]
}

@test "verdict: a candidate failure with no control evidence is never the local-only Failed: line" {
    full "$BATS_TEST_TMPDIR/cand" "versions:fail:virtualsmc: want 1.3.8"
    verdict --decision gated --reason x --scope-job success \
        --candidate "$BATS_TEST_TMPDIR/cand" --candidate-job success --candidate-exit 1 \
        --retry "$BATS_TEST_TMPDIR/none" --retry-job skipped --retry-exit '' \
        --control "$BATS_TEST_TMPDIR/absent"
    [ "$status" -eq 1 ]
    [[ "${lines[0]}" == "No clear regression: the control could not run -- control -- no steps.tsv in "* ]] || false
}

@test "verdict: a malformed steps.tsv is CI itself failing at the summary, naming what is wrong" {
    mkdir -p "$BATS_TEST_TMPDIR/cand"
    printf 'preflight\tmaybe\t1\t\n' > "$BATS_TEST_TMPDIR/cand/steps.tsv"
    verdict --decision gated --reason x --scope-job success \
        --candidate "$BATS_TEST_TMPDIR/cand" --candidate-job success --candidate-exit 1 \
        --retry "$BATS_TEST_TMPDIR/none" --retry-job skipped --retry-exit '' \
        --control "$BATS_TEST_TMPDIR/none2"
    [ "$status" -eq 1 ]
    [[ "${lines[0]}" == "No clear regression: CI itself failed at summary -- "*'result "maybe"'* ]] || false
}

@test "verdict: an unknown decision is a usage error" {
    verdict --decision maybe --reason x --scope-job success
    [ "$status" -eq 2 ]
}
