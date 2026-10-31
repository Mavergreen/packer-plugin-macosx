#!/usr/bin/env bats
# platform: host-agnostic
#
# No test may hang. bin/run-tests.sh and CI's test step give bats a
# per-test timeout (BATS_TEST_TIMEOUT), the same number in both, so a test
# whose code loops forever fails by name instead of holding the run until a
# human notices. The number is generous: the slowest test took 20 s on the
# primary host (`bats --timing tests/`, 2026-10-19), and the timeout is six
# times that, for slower runners and parallel jobs. bats runs its countdown
# as a child of each test's shell, so a test must never `wait` for every
# child: it names the job it waits for (the last test here).

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
}

# The stubs some tests define in their own shell (signals_on's kill and ps,
# last_looks' look and sleep) go before bats ends the test: its own end
# kills its timeout countdown with kill, and a stub one left the
# countdown's sleep running after the test (the watchdog caught it).
teardown() {
    unset -f kill ps sleep look 2>/dev/null || true
}

# The default, as bin/run-tests.sh sets it.
default_timeout() {
    sed -n 's/^export BATS_TEST_TIMEOUT="\${BATS_TEST_TIMEOUT:\{0,1\}-\([0-9]*\)}"$/\1/p' "$REPO/bin/run-tests.sh"
}

# exported_timeout: BATS_TEST_TIMEOUT after bin/run-tests.sh's export line,
# from what the caller's environment had.
exported_timeout() {
    local line
    line=$(grep '^export BATS_TEST_TIMEOUT=' "$REPO/bin/run-tests.sh")
    eval "$line"
    printf '%s' "$BATS_TEST_TIMEOUT"
}

@test "bin/run-tests.sh gives bats a per-test timeout by default, and keeps one the caller set" {
    t=$(default_timeout)
    [ -n "$t" ] || { echo "bin/run-tests.sh exports no BATS_TEST_TIMEOUT default"; false; }
    [ "$t" -ge 60 ]
    [ "$(unset BATS_TEST_TIMEOUT; exported_timeout)" = "$t" ]
    [ "$(BATS_TEST_TIMEOUT=30; exported_timeout)" = 30 ]
    # Set but empty is bats' "no timeout", the only way to turn it off.
    [ -z "$(BATS_TEST_TIMEOUT=; exported_timeout)" ]
    # Set before the suite runs.
    [ "$(grep -n '^export BATS_TEST_TIMEOUT=' "$REPO/bin/run-tests.sh" | cut -d: -f1)" -lt "$(grep -n 'bats tests/' "$REPO/bin/run-tests.sh" | head -1 | cut -d: -f1)" ]
}

# ci_step <name> [<workflow>]: the lines of the step <name> in <workflow>
# (ci.yml), from its "- name:" to the next step or job, and no further.
ci_step() {
    awk -v n="$1" '
        f && (/^[[:space:]]*- name:/ || /^  [^ ]/) { exit }
        $0 ~ "^[[:space:]]*- name: " && substr($0, index($0, "- name: ") + 8) == n { f = 1 }
        f' "${2:-$REPO/.github/workflows/ci.yml}"
}

# ci_test_step_ok [<workflow>]: whether its Test suite step gives bats
# bin/run-tests.sh's timeout, and runs shipyard's runner under the watchdog.
ci_test_step_ok() {
    local t step
    t=$(default_timeout)
    step=$(ci_step "Test suite" "$@")
    grep -qE "^[[:space:]]+BATS_TEST_TIMEOUT: \"$t\"$" <<< "$step" &&
        grep -qE '^[[:space:]]+run: bin/bats-watchdog.sh --run sh "\$SHIPYARD_SCRIPTS/run-repo-tests.sh"$' <<< "$step"
}

@test "CI's test step gives bats the same per-test timeout, and runs the suite under the watchdog" {
    ci_test_step_ok
}

@test "CI's test step counts only when its own lines say so, not a later step's" {
    # The timeout and the watchdog moved to the step after it.
    awk '/- name: Test suite/ { print; skip = 1; next }
         skip && /- name:/ { skip = 0; print; print "        env:"; print "          BATS_TEST_TIMEOUT: \"120\""; print "        run: bin/bats-watchdog.sh --run sh \"$SHIPYARD_SCRIPTS/run-repo-tests.sh\""; next }
         skip { next } { print }' "$REPO/.github/workflows/ci.yml" > "$BATS_TEST_TMPDIR/moved.yml"
    ! grep -q 'run-repo-tests' <<< "$(ci_step "Test suite" "$BATS_TEST_TMPDIR/moved.yml")" || false
    ! ci_test_step_ok "$BATS_TEST_TMPDIR/moved.yml"
}

@test "this bats honours BATS_TEST_TIMEOUT: a test that outlives it fails, named, as a timeout" {
    mkdir -p "$BATS_TEST_TMPDIR/t"
    # printf, not a heredoc: bats rewrites every line that starts with @test,
    # a heredoc's included.
    printf '%s\n' '@test "a test that would hang" {' '    sleep 30' '}' > "$BATS_TEST_TMPDIR/t/hang.bats"
    # The bats a user runs, not the bats-core internals this test runs under
    # (bats puts its libexec first on PATH).
    outer=$(printf '%s\n' "$PATH" | tr ':' '\n' | grep -v 'bats-core' | paste -sd: -)
    b=$(PATH=$outer command -v bats)
    [ -n "$b" ]
    SECONDS=0
    # A clean environment: the outer bats's own BATS_* variables would steer the inner one.
    run env -i PATH="$outer" HOME="$HOME" TMPDIR="$BATS_TEST_TMPDIR" BATS_TEST_TIMEOUT=2 "$b" "$BATS_TEST_TMPDIR/t/hang.bats"
    [ "$status" -ne 0 ] || { echo "status $status: $output"; false; }
    [[ "$output" == *"not ok 1 a test that would hang"*"timeout"* ]] || { echo "$output"; false; }
    [ "$SECONDS" -lt 25 ]
}

# bare_waits <file>...: each line with a wait for every child (or for any
# one, -n): wait as a command (after a line's start, an operator, a
# keyword, builtin, command or time), with no pid or job after it.
bare_waits() {
    grep -nE '(^|[;&|{(]|(^|[[:space:]])(then|do|else|!|if|while|until|builtin|command|time))[[:space:]]*wait([[:space:]]*($|[;&|)}#<>])|[[:space:]]+[0-9]*[<>]|[[:space:]]+-n[[:space:]]*($|[;&|)}#<>]))' "$@"
}

# test_code: every file a test's own shell runs: the tests, their helpers,
# and the lib/ files they source.
test_code() {
    find "$REPO/tests" -type f \( -name '*.bats' -o -name '*.bash' \) -print
    find "$REPO/lib" -type f -name '*.sh' -print
}

@test "the bare-wait check knows a bare wait in each of its forms, and only those" {
    # Spelled through $w, so this file does not trip its own check.
    w=wait
    printf '%s\n' "$w" "    $w || true" "    $w 2>/dev/null" "    sleep 1 & $w" "    $w # all" \
        "    { $w; }" "    foo; $w" "    $w -n" "    if x; then $w; fi" \
        "    if $w; then" "    while $w; do" "    until $w; do" "    builtin $w" "    command $w" "    time $w" > "$BATS_TEST_TMPDIR/bare"
    printf '%s\n' '    wait $!' '    wait "$bg"' '    wait "$bp" || status=$?' '    # wait for it' '    await' \
        "    $w -n \"\$a\"" '    waited=1' > "$BATS_TEST_TMPDIR/named"
    run bare_waits "$BATS_TEST_TMPDIR/bare"
    [ "${#lines[@]}" -eq 15 ] || { echo "caught only: $output"; false; }
    run bare_waits "$BATS_TEST_TMPDIR/named"
    [ "$status" -eq 1 ] || { echo "caught: $output"; false; }
}

# job_specs <file>...: each line that names a job by number, as kill and
# wait take one: a test's first job is bats' countdown, not its own.
job_specs() {
    grep -nE '(^|[;&|{(]|[[:space:]])(kill|wai[t]|fg|bg|disown)([[:space:]]+(-s[[:space:]]+[A-Za-z0-9]+|-[A-Za-z0-9]+|--))*[[:space:]]+["'"'"']?%' "$@"
}

@test "the job-number check knows a job named by number, and only that" {
    j=%
    printf '%s\n' "    kill ${j}1 2>/dev/null" "    wait ${j}1" "    kill -9 ${j}+" "    fg ${j}${j}" \
        "    kill -s TERM ${j}1" "    kill -- ${j}1" "    kill \"${j}1\"" "    kill '${j}1'" > "$BATS_TEST_TMPDIR/jobs"
    printf '%s\n' "    printf '${j}s' x" '    kill "$bg"' "    date +${j}s" "    kill -0 \$!" > "$BATS_TEST_TMPDIR/pids"
    run job_specs "$BATS_TEST_TMPDIR/jobs"
    [ "${#lines[@]}" -eq 8 ] || { echo "caught only: $output"; false; }
    run job_specs "$BATS_TEST_TMPDIR/pids"
    [ "$status" -eq 1 ] || { echo "caught: $output"; false; }
}

@test "no test names a job by number: its first job is bats' timeout countdown" {
    # "kill %1" after a test's own "cmd &" ends the countdown instead (bats
    # starts it first), and leaves the countdown's sleep running after the
    # test, holding its output; cmd lives on as well. Name the pid ($!).
    files=$(test_code)
    # shellcheck disable=SC2086 # paths, one per word
    run job_specs $files
    [ "$status" -eq 1 ] || { echo "job named by number: $output"; false; }
}

@test "no test waits for every child: bats' timeout countdown is one, and a bare wait would sit it out" {
    # bats-exec-test starts its countdown (a sleep of BATS_TEST_TIMEOUT) as a
    # child of the test's own shell, so `wait` with no pid waits for it too:
    # a test that waits for its own background job must name the job.
    files=$(test_code)
    [ -n "$files" ]
    # shellcheck disable=SC2086 # paths, one per word
    run bare_waits $files
    [ "$status" -eq 1 ] || { echo "bare wait: $output"; false; }
}

# outer_bats: the bats a user runs, not the bats-core internals this test runs
# under (bats puts its libexec first on PATH), and that PATH.
outer_bats() {
    OUTER_PATH=$(printf '%s\n' "$PATH" | tr ':' '\n' | grep -v 'bats-core' | paste -sd: -)
    OUTER_BATS=$(PATH=$OUTER_PATH command -v bats)
    [ -n "$OUTER_BATS" ]
}

# watched <file> [<limit>] [<VAR=value>...]: run_bats_watched (below), the
# watchdog's limit <limit> seconds (its default without one).
watched() {
    local f=$1 l=${2-}
    shift
    [ $# -eq 0 ] || shift
    run_bats_watched "$f" ${l:+BATS_WATCHDOG_LIMIT="$l"} "$@"
}

# ours_if_alive <pidfile> <args>: kills the process named in <pidfile> if it
# is still there and still runs <args>: a test's leftover, never another's.
ours_if_alive() {
    local p
    [ -s "$1" ] || return 0
    p=$(cat "$1")
    case $(ps -ww -o args= -p "$p" 2>/dev/null) in "$2"*) kill -KILL "$p" 2>/dev/null || true ;; esac
}

@test "the watchdog ends a test whose grandchild loops, all of it, names it, and the run goes on" {
    mkdir -p "$BATS_TEST_TMPDIR/t"
    # A loop in a grandchild under run: bats' own timeout kills only the
    # test's direct children, and this one would outlive it, holding the
    # test's output open (MEASURED 2026-10-19). It ends by itself at 90 s,
    # so without a watchdog this test fails slowly rather than hangs.
    printf '%s\n' '@test "loops in a grandchild" {' '    run sh -c "i=0; while [ \$i -lt 90 ]; do sleep 1; i=\$((i + 1)); done"' '    [ "$status" -ne 0 ]' '}' \
        '@test "the one after it" {' '    true' '}' > "$BATS_TEST_TMPDIR/t/loop.bats"
    watched "$BATS_TEST_TMPDIR/t/loop.bats" 3
    [ "$status" -ne 0 ]
    [[ "$output" == *"not ok 1 loops in a grandchild"* ]] || { echo "$output"; false; }
    [[ "$output" == *"ok 2 the one after it"* ]] || { echo "$output"; false; }
    [[ "$WD" == *"bats-watchdog: a test ran past 3s; ended it and all it started: "*"loop.bats"*"loops_in_a_grandchild"* ]] || { echo "$WD"; false; }
    # Once: run's subshell is a bats-exec-test too, and is not a test.
    [ "$(printf '%s\n' "$WD" | grep -c 'ran past')" -eq 1 ] || { echo "$WD"; false; }
    [ "$took" -lt 30 ]
}

@test "inert: the watchdog leaves a test under its limit alone, and says nothing" {
    mkdir -p "$BATS_TEST_TMPDIR/t"
    # Several polls long: a test that ends within one poll is never seen
    # running, so it would pass even if the watchdog ignored its limit.
    printf '%s\n' '@test "takes four seconds" {' '    run sleep 4' '    [ "$status" -eq 0 ]' '}' > "$BATS_TEST_TMPDIR/t/short.bats"
    watched "$BATS_TEST_TMPDIR/t/short.bats" 20
    [ "$status" -eq 0 ] || { echo "$output"; false; }
    [[ "$output" == *"ok 1 takes four seconds"* ]] || { echo "$output"; false; }
    [ -z "$WD" ] || { echo "$WD"; false; }
}

@test "bin/run-tests.sh runs its bats under the watchdog" {
    grep -qF 'if ! "$repo_root/bin/bats-watchdog.sh" --run bats tests/ | tee "$batslog"; then' "$REPO/bin/run-tests.sh"
}

@test "the watchdog's default limit is below bats' own timeout by more than a poll, or it refuses" {
    # shellcheck source=../bin/bats-watchdog.sh
    source "$REPO/bin/bats-watchdog.sh"
    [ "$(default_limit 120 5)" -eq 110 ]
    [ "$(default_limit 6 1)" -eq 3 ]
    # It must end a test, stop its tree included, before bats' countdown
    # fires: a poll and 2 s short of the timeout, at least (measured under
    # load: with 1 s to spare a test's result line was lost).
    for tp in "8 1" "15 5" "19 5" "20 5" "21 5" "60 5" "120 5" "600 5" "60 20"; do
        set -- $tp
        l=$(default_limit "$1" "$2")
        [ "$l" -ge 1 ] && [ "$l" -le $(($1 - $2 - 2)) ] || { echo "T=$1 poll=$2 gives a limit of $l"; false; }
    done
    # Too short for the poll: refused, saying how to fix it.
    for tp in "1 1" "2 1" "5 1" "8 5" "10 5" "30 20"; do
        set -- $tp
        run default_limit "$1" "$2"
        [ "$status" -eq 2 ] && [[ "$output" == *"BATS_TEST_TIMEOUT=$1"*"BATS_WATCHDOG_POLL"* ]] || { echo "T=$1 poll=$2: $status $output"; false; }
    done
}

@test "a short timeout: the watchdog ends the test before bats' countdown fires, and bats names it" {
    mkdir -p "$BATS_TEST_TMPDIR/t"
    printf '%s\n' '@test "loops in a grandchild" {' '    run sh -c "i=0; while [ \$i -lt 60 ]; do sleep 1; i=\$((i + 1)); done"' '}' \
        '@test "the one after it" {' '    true' '}' > "$BATS_TEST_TMPDIR/t/loop.bats"
    watched "$BATS_TEST_TMPDIR/t/loop.bats" '' BATS_TEST_TIMEOUT=6
    [ "$status" -ne 0 ]
    [[ "$output" == *"not ok 1 loops in a grandchild"* ]] || { echo "$output"; false; }
    [[ "$output" == *"ok 2 the one after it"* ]] || { echo "$output"; false; }
    [[ "$output" == *"a test ran past 3s"* ]] || { echo "$output"; false; }
    [[ "$output" != *"left behind"* ]] || { echo "$output"; false; }
}

@test "a short BATS_TEST_TIMEOUT does not make the watchdog end every test at its first look" {
    mkdir -p "$BATS_TEST_TMPDIR/t"
    printf '%s\n' '@test "takes three seconds" {' '    run sleep 3' '    [ "$status" -eq 0 ]' '}' > "$BATS_TEST_TMPDIR/t/short.bats"
    watched "$BATS_TEST_TMPDIR/t/short.bats" '' BATS_TEST_TIMEOUT=8
    [ "$status" -eq 0 ] || { echo "$output"; false; }
    [[ "$output" == *"ok 1 takes three seconds"* ]] || { echo "$output"; false; }
    [ -z "$WD" ] || { echo "$WD"; false; }
}

# run_watched <env assignment>... -- <command>...: bin/bats-watchdog.sh --run
# <command>, polling every second; sets status, output (stdout and stderr),
# and took.
run_watched() {
    local envs=(BATS_WATCHDOG_POLL=1 TMPDIR="$BATS_TEST_TMPDIR")
    while [ "$1" != -- ]; do envs+=("$1"); shift; done
    shift
    SECONDS=0
    run env "${envs[@]}" "$REPO/bin/bats-watchdog.sh" --run "$@"
    took=$SECONDS
}

@test "the watchdog refuses a limit or a poll that is not a positive number of seconds, and runs nothing" {
    for e in BATS_WATCHDOG_LIMIT=2m BATS_WATCHDOG_LIMIT=0 BATS_TEST_TIMEOUT=2m BATS_WATCHDOG_POLL=x BATS_WATCHDOG_POLL=0; do
        rm -f "$BATS_TEST_TMPDIR/ran"
        run_watched "$e" -- touch "$BATS_TEST_TMPDIR/ran"
        [ "$status" -eq 2 ] && [[ "$output" == *"${e%%=*}"*"'${e#*=}'"*"nothing was run, and the run fails"* ]] || { echo "$e: status $status: $output"; false; }
        [ ! -e "$BATS_TEST_TMPDIR/ran" ]
    done
    [ "$took" -lt 3 ]
}

@test "the watchdog stops, and says so, when ps lists nothing, rather than watching nothing" {
    stubs="$BATS_TEST_TMPDIR/stubs"
    mkdir -p "$stubs"
    cat > "$stubs/ps" <<'STUB'
#!/bin/sh
exit 1
STUB
    chmod +x "$stubs/ps"
    PATH="$stubs:$PATH" run_watched -- touch "$BATS_TEST_TMPDIR/ran"
    [ "$status" -eq 2 ] && [[ "$output" == *"ps listed no processes"* ]] || { echo "status $status: $output"; false; }
    [ ! -e "$BATS_TEST_TMPDIR/ran" ]
}

@test "a narrow COLUMNS does not blind the watchdog, nor cut the test's name from its words" {
    mkdir -p "$BATS_TEST_TMPDIR/t"
    printf '%s\n' '@test "sleeps past its limit" {' '    sleep 12' '}' > "$BATS_TEST_TMPDIR/t/long.bats"
    watched "$BATS_TEST_TMPDIR/t/long.bats" 2 COLUMNS=60
    [ "$status" -ne 0 ]
    [[ "$output" == *"not ok 1 sleeps past its limit"* ]] || { echo "$output"; false; }
    [[ "$WD" == *"ran past 2s"*"long.bats"*"sleeps_past_its_limit"* ]] || { echo "$WD"; false; }
    [ "$took" -lt 11 ]
}

@test "a watchdog told to stop ends its run, and leaves nothing behind holding its caller's output" {
    # Its sleeps between looks are a second each, their output on /dev/null.
    SECONDS=0
    # shellcheck disable=SC2016 # for the inner bash
    BATS_WATCHDOG_POLL=6 bash -c '"$1" --run sleep 30 & w=$!; sleep 1; kill "$w"; wait "$w"' _ "$REPO/bin/bats-watchdog.sh" 2>&1 | cat > /dev/null
    [ "$SECONDS" -lt 6 ] || { echo "the pipe closed ${SECONDS}s after the start"; false; }
}

@test "a test that forks as it is ended leaves nothing behind" {
    mkdir -p "$BATS_TEST_TMPDIR/t"
    # A TERM starts a sleep that would outlive the test, holding its output.
    # The test's tree is killed outright, but should anything in it fork as
    # it goes, the child is in the run's group, outside the tree: a leftover.
    printf '%s\n' '@test "forks when told to end" {' \
        '    bash -c '\''trap "sleep 30 &" TERM; i=0; while [ $i -lt 600 ]; do sleep 0.1 & wait $!; i=$((i + 1)); done'\''' \
        '}' > "$BATS_TEST_TMPDIR/t/fork.bats"
    watched "$BATS_TEST_TMPDIR/t/fork.bats" 2
    [ "$status" -ne 0 ]
    [[ "$output" == *"not ok 1 forks when told to end"* ]] || { echo "$output"; false; }
    [ "$(printf '%s\n' "$WD" | grep -c 'ran past')" -eq 1 ] || { echo "$WD"; false; }
    [ "$took" -lt 15 ] || { echo "took ${took}s: $WD"; false; }
}

@test "the watchdog ends what a test left running behind it, names it, and fails the run" {
    mkdir -p "$BATS_TEST_TMPDIR/t"
    # Each outlives its test, holding the test's output open, so bats waits
    # for it; neither bats' timeout nor a test's tree reaches it once its
    # test has ended. They end by themselves at 60 s.
    printf '%s\n' \
        '@test "leaves a loop behind" {' \
        '    ( i=0; while [ $i -lt 60 ]; do sleep 1; i=$((i + 1)); done ) &' \
        "    echo \$! > '$BATS_TEST_TMPDIR/loop.pid'" '}' \
        '@test "leaves a command behind" {' \
        '    sleep 60 &' \
        "    echo \$! > '$BATS_TEST_TMPDIR/sleep.pid'" '}' > "$BATS_TEST_TMPDIR/t/orphan.bats"
    watched "$BATS_TEST_TMPDIR/t/orphan.bats" 20
    ours_if_alive "$BATS_TEST_TMPDIR/sleep.pid" "sleep 60"
    ours_if_alive "$BATS_TEST_TMPDIR/loop.pid" "bash"
    [ "$took" -lt 20 ] || { echo "took ${took}s: $output"; false; }
    [[ "$output" == *"ok 1 leaves a loop behind"*"ok 2 leaves a command behind"* ]] || { echo "$output"; false; }
    [ "$(printf '%s\n' "$WD" | grep -c '^bats-watchdog: left behind by a test')" -eq 2 ] || { echo "$WD"; false; }
    # The loop, a test's subshell, by its test; the sleep by its args.
    [[ "$WD" == *"left behind by a test"*"orphan.bats test_leaves_a_loop_behind"* ]] || { echo "$WD"; false; }
    [[ "$WD" == *"left behind by a test"*"sleep 60"* ]] || { echo "$WD"; false; }
    [ "$status" -eq 1 ] || { echo "status $status"; false; }
}

@test "the watchdog leaves alone what a test starts apart from itself while that test still runs" {
    mkdir -p "$BATS_TEST_TMPDIR/t"
    # A daemon, as the autoinstall hook's follower is: its parent exits, so
    # it is outside the run's tree, but its test is still there to end it.
    printf '%s\n' '@test "starts a daemon, then ends it" {' \
        "    ( sleep 30 & echo \$! > '$BATS_TEST_TMPDIR/daemon.pid' )" \
        '    sleep 4' \
        "    kill \"\$(cat '$BATS_TEST_TMPDIR/daemon.pid')\"" '}' > "$BATS_TEST_TMPDIR/t/daemon.bats"
    watched "$BATS_TEST_TMPDIR/t/daemon.bats" 20
    ours_if_alive "$BATS_TEST_TMPDIR/daemon.pid" "sleep 30"
    [ "$status" -eq 0 ] || { echo "$output"; false; }
    [ -z "$WD" ] || { echo "$WD"; false; }
}

@test "--run runs a command under the watchdog and gives back its status" {
    run_watched -- sh -c 'exit 3'
    [ "$status" -eq 3 ] || { echo "status $status: $output"; false; }
    run_watched -- true
    [ "$status" -eq 0 ] && [ -z "$output" ] || { echo "status $status: $output"; false; }
}

@test "--run ends, names and fails a quiet leftover of the run's last test, after the run" {
    mkdir -p "$BATS_TEST_TMPDIR/t"
    # It holds nothing open, so bats ends at once, before the watchdog has
    # looked twice; it would run on after the run.
    printf '%s\n' '@test "leaves a quiet command behind" {' '    sleep 47 > /dev/null 2>&1 3>&- &' \
        "    echo \$! > '$BATS_TEST_TMPDIR/sleep.pid'" '}' > "$BATS_TEST_TMPDIR/t/quiet.bats"
    watched "$BATS_TEST_TMPDIR/t/quiet.bats"
    p=$(cat "$BATS_TEST_TMPDIR/sleep.pid")
    left=$(ps -ww -o args= -p "$p" 2>/dev/null || true)
    ours_if_alive "$BATS_TEST_TMPDIR/sleep.pid" "sleep 47"
    [[ "$output" == *"ok 1 leaves a quiet command behind"* ]] || { echo "$output"; false; }
    [[ "$WD" == *"left behind by a test"*"sleep 47"* ]] || { echo "$output"; false; }
    [ "$status" -ne 0 ] || { echo "status $status: $output"; false; }
    [[ "$left" != "sleep 47"* ]] || { echo "still running after the run: $left"; false; }
}

# outside_tests <file>...: each line that defines a setup_file,
# teardown_file, setup_suite or teardown_suite, in any form.
outside_tests() {
    grep -nE '^[[:space:]]*(function[[:space:]]+)?(setup|teardown)_(file|suite)([[:space:]]*\(\)|[[:space:]]*\{|[[:space:]]*$)' "$@"
}

@test "the setup_file check knows each form of the four, and only those" {
    f=_file s=_suite
    printf '%s\n' "setup$f() {" "setup$f() { export X=1; }" "function teardown$f {" "teardown$f ()" \
        "setup$s() {" "  teardown$s() { :; }" > "$BATS_TEST_TMPDIR/outside"
    printf '%s\n' 'setup() {' "# setup$f is not here" "my_setup$f() {" "    setup${f}_done=1" > "$BATS_TEST_TMPDIR/inside"
    run outside_tests "$BATS_TEST_TMPDIR/outside"
    [ "${#lines[@]}" -eq 6 ] || { echo "caught only: $output"; false; }
    run outside_tests "$BATS_TEST_TMPDIR/inside"
    [ "$status" -eq 1 ] || { echo "caught: $output"; false; }
}

@test "no test file has a setup_file or teardown_file, nor a setup_suite: neither timeout reaches them" {
    # bats runs them in bats-exec-file and bats-exec-suite, outside every
    # test, so neither its BATS_TEST_TIMEOUT nor the watchdog would end one
    # that hung.
    files=$(find "$REPO/tests" -type f \( -name '*.bats' -o -name '*.bash' \) -print)
    # shellcheck disable=SC2086 # paths, one per word
    run outside_tests $files
    [ "$status" -eq 1 ] || { echo "runs outside every test: $output"; false; }
}

@test "an empty BATS_TEST_TIMEOUT, bats' no timeout, is the watchdog's no limit too" {
    # shellcheck source=../bin/bats-watchdog.sh
    source "$REPO/bin/bats-watchdog.sh"
    [ "$(unset BATS_TEST_TIMEOUT; limit_for '' 5)" = 110 ]
    [ "$(BATS_TEST_TIMEOUT=8 limit_for '' 1)" = 5 ]
    [ "$(BATS_TEST_TIMEOUT=8 limit_for 5 1)" = 5 ]
    [ -z "$(BATS_TEST_TIMEOUT='' limit_for '' 5)" ]
}

@test "of a test and its own subshells, the watchdog picks the test, whichever pid is lower" {
    # shellcheck source=../bin/bats-watchdog.sh
    source "$REPO/bin/bats-watchdog.sh"
    # A test (900) whose run subshell (850, a bats-exec-test too) got the
    # lower pid, as it does once pids wrap. Only a table: no process here is
    # real, and nothing is signalled.
    table="700 1 700 0 bash bin/run-tests.sh
800 700 800 0 bash /usr/libexec/bats-core/bats-exec-file x.bats
850 900 800 0 bash /usr/libexec/bats-core/bats-exec-test --dummy-flag x.bats test_a 1 1 1
900 800 800 0 bash /usr/libexec/bats-core/bats-exec-test --dummy-flag x.bats test_a 1 1 1"
    run live_tests " 800 850 900 "
    [ "$output" = "900 800 bash /usr/libexec/bats-core/bats-exec-test --dummy-flag x.bats test_a 1 1 1" ] || { echo "$output"; false; }
}

# group_leavers <file>...: each line that starts a process outside its
# parent's process group: setsid, setpgrp or setpgid by name (a command,
# perl's, python's os.), job control (set -m, set -o monitor), a QEMU
# told to -daemonize, or GNU timeout without --foreground (it moves itself,
# and so its command, to a group of its own: measured). The watchdog finds what a test leaves running by its
# process group, so one of these hides it.
#
# WHAT IT CANNOT SEE: a compiled program that calls setsid(2) or
# setpgid(2) itself, a tool that daemonizes on its own (ssh -f, an agent),
# a command spelled at run time, and the programs outside tests/ and lib/
# that a test runs (bin/gate-vm.sh's QEMU among them: tests stub QEMU), and
# this file, which names them. The watchdog then says the run stalled, if
# what escaped holds its output.
group_leavers() {
    grep -nHE '(^|[^A-Za-z_])(setsid|setpgrp|setpgid)([^A-Za-z_]|$)|(^|[;&|({[:space:]])set[[:space:]]+(-m|-o[[:space:]]+monitor)([[:space:]]|;|$)|qemu[-A-Za-z0-9_]*[[:space:]].*-daemonize|(^|[;&|({[:space:]])set[[:space:]]+-[A-Za-z]*m|(^|[/[:space:];&|(])(ba)?sh[[:space:]]+-[A-Za-z]*[mi]|(^|[/[:space:];&|(])g?timeout[[:space:]]+(-|[0-9])' "$@" |
        grep -vE -- '--foreground|^[^:]*:[0-9]+:[[:space:]]*#'
}

@test "the group-leaving check knows each way out of a process group it bans, and only those" {
    s=set m=-m
    printf '%s\n' "    ${s}sid sleep 30 &" "    perl -e '${s}pgrp; sleep 9'" "    python3 -c 'import os; os.${s}sid()'" \
        "    ( set $m; sleep 9 & )" "    set -o monitor" "    qemu-system-x86_64 -daemonize -m 64" "if x; then set $m; fi" \
        "    timeout 60 env x" "    run timeout 20 cmd" "    timeout -s KILL 5 cmd" \
        "    timeout --kill-after=5 10 cmd" "    /usr/bin/timeout 10 cmd" "    gtimeout 10 cmd" "    set -e${m#-}" "    set ${m}u" \
        "    bash $m -c x" "    bash -i -c x" "    sh -ic x" > "$BATS_TEST_TMPDIR/leave"
    printf '%s\n' '    grep -qxF -- "-daemonize" "$f"' '    # a stand-in QEMU daemonizes' '    upset -m x' '    offset=1' \
        "    ${s}up_done=1" "    reset" "    timeout --foreground 60 env x" "    # a timeout after 4s" \
        "    timeout -k 5 --foreground 10 cmd" "    set -euo pipefail" "    bash -c x" "    sh -ec x" "    # set -m here" \
        "    echo 'the timeout countdown, 120 of it'" > "$BATS_TEST_TMPDIR/stay"
    run group_leavers "$BATS_TEST_TMPDIR/leave"
    [ "${#lines[@]}" -eq 18 ] || { echo "caught only: $output"; false; }
    run group_leavers "$BATS_TEST_TMPDIR/stay"
    [ "$status" -eq 1 ] || { echo "caught: $output"; false; }
}

@test "no test leaves its process group: the watchdog finds leftovers by it" {
    # All the test code but this file, which names each way out to ban it.
    files=$(test_code | grep -v '/tests/test_timeout\.bats$')
    # shellcheck disable=SC2086 # paths, one per word
    run group_leavers $files
    [ "$status" -eq 1 ] || { echo "leaves its process group: $output"; false; }
}

# run_bats_watched <file> [<VAR=value>...]: <file> under a bats of its own,
# run by bin/bats-watchdog.sh --run in a clean environment, polling every
# second, with the assignments after the file for both (a later PATH wins).
# Sets status, output (both's), WD (the watchdog's lines) and took.
run_bats_watched() {
    local f=$1
    shift
    outer_bats
    SECONDS=0
    run env -i PATH="$OUTER_PATH" HOME="$HOME" TMPDIR="$BATS_TEST_TMPDIR" BATS_WATCHDOG_POLL=1 "$@" \
        "$REPO/bin/bats-watchdog.sh" --run "$OUTER_BATS" "$f"
    took=$SECONDS
    WD=$(printf '%s\n' "$output" | grep '^bats-watchdog:' || true)
}

@test "the watchdog asks ps for one listing only, the one every system has" {
    # pid, ppid, pgid, etime and args, -A and -ww: POSIX keywords, procps,
    # macOS and NetBSD alike. No environment, no -p: a ps that has only
    # this does.
    mkdir -p "$BATS_TEST_TMPDIR/stubs" "$BATS_TEST_TMPDIR/t"
    cat > "$BATS_TEST_TMPDIR/stubs/ps" <<'STUB'
#!/bin/sh
[ "$*" = "-ww -A -o pid= -o ppid= -o pgid= -o etime= -o args=" ] && exec "$REAL_PS" "$@"
echo "$*" >> "$PS_ASKED"
exit 1
STUB
    chmod +x "$BATS_TEST_TMPDIR/stubs/ps"
    printf '%s\n' '@test "loops in a grandchild" {' '    run sh -c "i=0; while [ \$i -lt 60 ]; do sleep 1; i=\$((i + 1)); done"' '}' \
        '@test "leaves a command behind" {' '    sleep 60 &' "    echo \$! > '$BATS_TEST_TMPDIR/sleep.pid'" '}' > "$BATS_TEST_TMPDIR/t/both.bats"
    outer_bats
    run_bats_watched "$BATS_TEST_TMPDIR/t/both.bats" BATS_WATCHDOG_LIMIT=3 PATH="$BATS_TEST_TMPDIR/stubs:$OUTER_PATH" \
        REAL_PS="$(command -v ps)" PS_ASKED="$BATS_TEST_TMPDIR/asked"
    ours_if_alive "$BATS_TEST_TMPDIR/sleep.pid" "sleep 60"
    [ ! -e "$BATS_TEST_TMPDIR/asked" ] || { echo "asked ps otherwise: $(sort -u "$BATS_TEST_TMPDIR/asked")"; false; }
    [[ "$output" == *"not ok 1 loops in a grandchild"* ]] || { echo "$output"; false; }
    [[ "$WD" == *"left behind"*"sleep 60"* ]] || { echo "$output"; false; }
    [ "$status" -ne 0 ]
    [ "$took" -lt 20 ]
}

@test "the watchdog never ends a process of its own or its caller's group, nor any outside the run's groups" {
    # All of it in a process group of its own (a job of a shell with job
    # control), so whatever goes wrong here, it is this group's.
    cat > "$BATS_TEST_TMPDIR/caller.sh" <<'CALLER'
#!/usr/bin/env bash
# The watchdog's caller, leading a group of its own: beside it, in its
# group, a sleep that is nothing to do with the run.
sleep 20 &
echo $! > "$D/caller-group.pid"
# shellcheck disable=SC2016 # for the inner sh
BATS_WATCHDOG_POLL=1 "$WD" --run sh -c 'sleep 30 > /dev/null 2>&1 & echo $! > "$D/left.pid"'
echo $? > "$D/rc"
CALLER
    chmod +x "$BATS_TEST_TMPDIR/caller.sh"
    # And one in this test's own group, outside the run as well.
    sleep 20 &
    other=$!
    # shellcheck disable=SC2016 # for the inner bash
    D=$BATS_TEST_TMPDIR WD=$REPO/bin/bats-watchdog.sh bash -c 'set -m; "$1" & wait "$!"' _ "$BATS_TEST_TMPDIR/caller.sh"
    caller_alive=0 other_alive=0 left_alive=0
    kill -0 "$(cat "$BATS_TEST_TMPDIR/caller-group.pid")" 2>/dev/null && caller_alive=1
    kill -0 "$other" 2>/dev/null && other_alive=1
    ours_if_alive "$BATS_TEST_TMPDIR/left.pid" "sleep 30" && [ ! -s "$BATS_TEST_TMPDIR/left.pid" ] || true
    p=$(cat "$BATS_TEST_TMPDIR/left.pid" 2>/dev/null || true)
    kill "$(cat "$BATS_TEST_TMPDIR/caller-group.pid")" "$other" 2>/dev/null || true
    wait "$other" 2>/dev/null || true
    [ "$caller_alive" = 1 ] || { echo "ended its caller's group's sleep"; false; }
    [ "$other_alive" = 1 ] || { echo "ended a sleep of another group"; false; }
    [ "$(cat "$BATS_TEST_TMPDIR/rc")" -ne 0 ] || { echo "the run did not fail"; false; }
    [ -n "$p" ]
}

@test "a leftover that left the run's process groups, holding its output, makes the run fail as stalled" {
    mkdir -p "$BATS_TEST_TMPDIR/t"
    m=-m
    # Job control in a subshell: the sleep leads a group of its own, as a
    # setsid would make it, and holds the test's output.
    printf '%s\n' '@test "leaves its group behind" {' "    ( set $m; sleep 30 & echo \$! > '$BATS_TEST_TMPDIR/sleep.pid' )" '}' > "$BATS_TEST_TMPDIR/t/away.bats"
    run_bats_watched "$BATS_TEST_TMPDIR/t/away.bats" BATS_WATCHDOG_LIMIT=3
    ours_if_alive "$BATS_TEST_TMPDIR/sleep.pid" "sleep 30"
    [ "$status" -ne 0 ] || { echo "status $status: $output"; false; }
    [[ "$WD" == *"stalled"* || "$WD" == *"left behind"* ]] || { echo "$output"; false; }
    [ "$took" -lt 15 ] || { echo "took ${took}s: $output"; false; }
}

@test "a group the run made is kept while it has members, and dropped once empty: its number may go to a stranger" {
    # shellcheck source=../bin/bats-watchdog.sh
    source "$REPO/bin/bats-watchdog.sh"
    # Only tables: no process here is real, and nothing is signalled.
    job=100 groups=" 100 " members=" 100:100 "
    tree=" 100 101 900 "
    # A group made under the run (its leader, 900, in the tree), with a member outside it.
    table="100 1 100 bash bats
101 100 100 bash bats-exec-file x.bats
900 101 900 sh -c x
905 1 900 sleep 30"
    regroup "$tree"
    [[ "$groups" == *" 900 "* ]] || { echo "not kept: $groups"; false; }
    # A process of the tree that joins a group it did not make (a stranger's,
    # 700) does not make that group the run's.
    table="$table
102 101 700 sh -c y"
    regroup " 100 101 102 900 "
    [[ "$groups" != *" 700 "* ]] || { echo "kept a group it did not make: $groups"; false; }
    # The group empties: dropped.
    table="100 1 100 bash bats
101 100 100 bash bats-exec-file x.bats"
    regroup " 100 101 "
    [[ "$groups" != *" 900 "* ]] || { echo "kept an empty group: $groups"; false; }
    # Its number goes to a stranger's group, outside the tree: not the run's.
    table="$table
900 1 900 bash -i
950 900 900 qemu-system-x86_64"
    regroup " 100 101 "
    [[ "$groups" != *" 900 "* ]] || { echo "took a stranger's group for the run's: $groups"; false; }
    # And a kept group replaced between two looks, all its members new and
    # none in the tree, is dropped too.
    groups=" 100 800 " members=" 100:100 800:800 805:800 "
    table="100 1 100 bash bats
951 1 800 sleep 9
952 1 800 sleep 9"
    regroup " 100 "
    [[ "$groups" != *" 800 "* ]] || { echo "kept a replaced group: $groups"; false; }
    [[ "$groups" == *" 100 "* ]]
}

@test "--run of a command with nothing left over costs no second last look" {
    # shellcheck source=../bin/bats-watchdog.sh
    source "$REPO/bin/bats-watchdog.sh"
    # look and sleep only write down that they were called.
    look() { echo look >> "$BATS_TEST_TMPDIR/calls"; found=$LOOK_FINDS; LOOK_FINDS=''; }
    sleep() { echo "sleep $*" >> "$BATS_TEST_TMPDIR/calls"; }
    LOOK_FINDS='' last_looks
    [ "$(cat "$BATS_TEST_TMPDIR/calls")" = look ] || { cat "$BATS_TEST_TMPDIR/calls"; false; }
    : > "$BATS_TEST_TMPDIR/calls"
    # A leftover found: looks again, a second later, until it is gone.
    LOOK_FINDS=" 905:100 " last_looks
    [ "$(tr '\n' ' ' < "$BATS_TEST_TMPDIR/calls")" = "look sleep 1 look " ] || { cat "$BATS_TEST_TMPDIR/calls"; false; }
}

@test "a hang-up to the watchdog reaches its run, which it then ends and fails" {
    # In a throwaway process group of its own, like every kill path here.
    # shellcheck disable=SC2016 # for the inner bash
    D=$BATS_TEST_TMPDIR WD=$REPO/bin/bats-watchdog.sh bash -c 'set -m
        ( BATS_WATCHDOG_POLL=1 "$WD" --run sh -c "echo \$\$ > \"$D/job.pid\"; exec sleep 30"; echo $? > "$D/rc" ) &
        sleep 1.5
        kill -HUP "$(ps -ww -A -o pid= -o ppid= -o pgid= -o args= | awk -v p=$! '\''$2 == p && /bats-watchdog/ { print $1 }'\'')"
        wait' 2>/dev/null
    j=$(cat "$BATS_TEST_TMPDIR/job.pid")
    alive=0
    kill -0 "$j" 2>/dev/null && alive=1
    ours_if_alive "$BATS_TEST_TMPDIR/job.pid" "sleep 30"
    [ "$alive" = 0 ] || { echo "the run went on, unwatched, after the hang-up"; false; }
    [ "$(cat "$BATS_TEST_TMPDIR/rc")" -ne 0 ]
}

# signals_on <listing>: sources the watchdog with a ps that prints
# <listing> ("pid ppid pgid etime args") and a kill that only writes down
# what it would have sent (to $BATS_TEST_TMPDIR/signals) and answers kill -0
# with yes: nothing here is real, and nothing is signalled.
signals_on() {
    # shellcheck source=../bin/bats-watchdog.sh
    source "$REPO/bin/bats-watchdog.sh"
    LISTING=$1
    ps() { printf '%s\n' "$LISTING"; }
    kill() { [ "$1" = -0 ] || echo "$*" >> "$BATS_TEST_TMPDIR/signals"; }
    : > "$BATS_TEST_TMPDIR/signals"
}

@test "a kept group whose number a new leader took within one poll is not the run's" {
    # 900 and its member 905 were there at the last look (t=95); by now
    # (t=100) both are gone, and pid 900 leads a stranger's group, its
    # leader started since: with no look between, only the start tells.
    signals_on "100 1 100 00:30 bash bats
101 100 100 00:30 bash bats-exec-file x.bats
900 1 900 00:01 bash -i
950 900 900 00:01 qemu-system-x86_64"
    SECONDS=100
    snapshot
    job=100 groups=" 100 900 " members=" 100:100 101:100 900:900 905:900 " last_look=95
    regroup " 100 101 "
    [[ "$groups" != *" 900 "* ]] || { echo "took a stranger's group for the run's: $groups"; false; }
    [[ "$groups" == *" 100 "* ]]
    # So too the job's own group, kept after its leader ended for what the
    # run left in it: once a new leader has its number, it is not the run's.
    job=900 groups=" 900 " members=" 900:900 905:900 " last_look=95
    regroup " "
    [[ "$groups" != *" 900 "* ]] || { echo "took a stranger's group for the job's: $groups"; false; }
    # And a member whose pid was there at the last look, but which started
    # since, is a new process: it keeps no group.
    signals_on "100 1 100 00:30 bash bats
805 1 800 00:01 sleep 9"
    snapshot
    job=100 groups=" 100 800 " members=" 100:100 805:800 " last_look=95
    regroup " 100 "
    [[ "$groups" != *" 800 "* ]] || { echo "a new process kept a group: $groups"; false; }
}

@test "the watchdog signals nothing that started since its last look" {
    # At t=100, the last look at t=94. Test 102, first seen at t=80, is past
    # its limit: its child 103 (30 s old) is killed, and 104, a second old,
    # is not, nor is 905, a leftover by its number at the last look but a
    # second old now (its pid had been reused); 907, as old as it was, is.
    # And 108, a test by the number first seen at t=80, is a second old: a
    # new test, with a reused pid, not the one past its limit.
    signals_on "100 1 100 01:00 bash bats
101 100 100 01:00 bash bats-exec-file x.bats
102 101 100 00:30 bash bats-exec-test --dummy-flag x.bats test_a 1 1 1
103 102 100 00:30 sh -c loop
104 103 100 00:01 sleep 1
109 100 999 01:00 bash bats-exec-file y.bats
108 109 999 00:01 bash bats-exec-test --dummy-flag y.bats test_b 1 1 1
905 1 777 00:01 sleep 9
907 1 777 00:40 sleep 40"
    SECONDS=100
    job=100 groups=" 100 777 " members=" 100:100 101:100 102:100 777:777 907:777 905:777 " last_look=94
    limit=5 first=" 102:80 108:80" suspects=" 905:777 907:777 " mine=" 1 " ended_tests='' ended=0 idle_since=''
    look running 2> /dev/null
    sent=$(cat "$BATS_TEST_TMPDIR/signals")
    [[ "$sent" == *"103"* ]] || { echo "the test's old child was not killed: $sent"; false; }
    [[ "$sent" == *"-TERM 102"* ]] || { echo "the test was not told to end: $sent"; false; }
    [[ "$sent" == *"907"* ]] || { echo "the old leftover was not killed: $sent"; false; }
    [[ "$sent" != *"104"* ]] || { echo "signalled 104, started since the last look: $sent"; false; }
    [[ "$sent" != *"905"* ]] || { echo "signalled 905, started since the last look: $sent"; false; }
    [[ "$sent" != *"108"* ]] || { echo "signalled 108, started since the last look: $sent"; false; }
}

@test "a timed-out test gets TERM only once, so its exit trap can finish reporting" {
    signals_on "100 1 100 01:00 bash bats
101 100 100 01:00 bash bats-exec-file x.bats
102 101 100 00:30 bash bats-exec-test --dummy-flag x.bats test_a 1 1 1
103 102 100 00:30 sh -c loop"
    SECONDS=100
    job=100 groups=" 100 " members=" 100:100 101:100 102:100 " last_look=94
    limit=3 first=" 102:80" suspects='' mine=" 1 " ended_tests='' ended=0 idle_since=''
    look running 2> /dev/null
    # The shell is still present on the next poll, running its EXIT trap.
    look running 2> /dev/null
    sent=$(cat "$BATS_TEST_TMPDIR/signals")
    [ "$(grep -c -- '-TERM 102' <<< "$sent")" -eq 1 ] || { echo "$sent"; false; }
    # Killing run's subshell first can start the test's EXIT trap before
    # even the first TERM arrives, losing the result by the same mechanism.
    [ "${sent%%$'\n'*}" = '-TERM 102' ] || { echo "$sent"; false; }
}

@test "a stalled run's groups lose only what was there at the last look" {
    # No test for 10 s, past the limit of 5, and nothing to end: stalled.
    # bats (100) is ended; 703, which it started a second ago, is not.
    signals_on "100 1 100 01:00 bash bats
703 100 100 00:01 sh -c x"
    SECONDS=100
    job=100 groups=" 100 " members=" 100:100 " last_look=94
    limit=5 first='' suspects='' mine=" 1 " ended_tests='' ended=0 idle_since=90
    look running 2> /dev/null
    sent=$(cat "$BATS_TEST_TMPDIR/signals")
    [ "$ended" = 1 ] && [[ "$sent" == *"-KILL 100"* ]] || { echo "no stall: $sent"; false; }
    [[ "$sent" != *"703"* ]] || { echo "signalled 703, started since the last look: $sent"; false; }
}

@test "a signal passed on to the run goes to its job, as this shell knows it, never to a group's number" {
    # The job is this shell's only one: bash signals its group only while it
    # has not reaped the job's leader, so the number is still the job's.
    signals_on "100 1 100 01:00 bash bats
950 1 100 00:00 sleep 9"
    job=100
    forward INT
    sent=$(cat "$BATS_TEST_TMPDIR/signals")
    [ "$sent" = "-INT %1" ] || { echo "sent: $sent"; false; }
}

@test "ps's elapsed time reads the same in each form systems print it" {
    # POSIX's [[dd-]hh:]mm:ss, as procps, macOS and NetBSD print it.
    signals_on "1 0 1 05 a
2 0 2 01:05 b
3 0 3 2:01:05 c
4 0 4 3-02:01:05 d
5 0 5 13-21:18:49 e
6 0 6 - f"
    SECONDS=1000000
    snapshot
    born=$(awk '{ printf "%s ", $4 }' <<< "$table")
    [ "$born" = "999995 999935 992735 733535 -199929 1000000 " ] || { echo "$born"; false; }
}

@test "a group that looks empty in one listing is looked at again: ps's listing is not one instant" {
    # MEASURED 2026-10-24 (Linux, bash 5.2 and 3.2 alike): of 100 groups
    # never empty, a job that starts a sleep and exits, a listing taken
    # meanwhile showed no member in 34: ps reads /proc entry by entry, so
    # members that exit as it goes, and one started after it read the
    # directory, are all missed. So a run's quiet leftover was missed when
    # the first look fell on the run's end (8 of 10 under bash 3.2).
    signals_on "unused"
    # The first listing catches the job's group (100) mid-change: empty;
    # the next shows the leftover the run left in it. (ps runs in a
    # subshell: it counts its listings in a file.)
    ps() {
        echo x >> "$BATS_TEST_TMPDIR/listings"
        if [ "$(wc -l < "$BATS_TEST_TMPDIR/listings")" -eq 1 ]; then
            printf '%s\n' "1 0 1 01:00 init"
        else
            printf '%s\n' "1 0 1 01:00 init" "905 1 100 00:00 sleep 47"
        fi
    }
    SECONDS=100
    job=100 groups=" 100 " members=" 100:100 " last_look=99
    limit=5 first='' suspects='' mine=" 1 " ended_tests='' ended=0 idle_since=''
    look last 2> /dev/null
    [[ "$groups" == *" 100 "* ]] || { echo "dropped the job's group on one listing: $groups"; false; }
    [[ "$found" == *" 905:100 "* ]] || { echo "the leftover is no suspect: $found"; false; }
    [ "$(wc -l < "$BATS_TEST_TMPDIR/listings")" -eq 2 ]
}

@test "when a process started is read on ps's own clock, from the watchdog's own elapsed time" {
    # Its clock and $SECONDS (the wall clock, in some bash) can part: a step
    # back must not make a new process look old. Here $SECONDS reads 5000,
    # as if the clock had jumped; the watchdog's own row says it is 100 s
    # old, so a process 30 s old started at 70 on that clock, and one a
    # second old at 99.
    signals_on "$$ 1 $$ 01:40 bash bats-watchdog.sh
701 1 777 00:30 sleep 30
702 1 777 00:01 sleep 1"
    SECONDS=5000
    snapshot
    last_look=94
    [ "$(olds)" = " $$ 701 " ] || { echo "old: $(olds)"; echo "$table"; false; }
}
