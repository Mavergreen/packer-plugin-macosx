#!/usr/bin/env bats
# platform: host-agnostic -- tested on Linux with KVM only
# shellcheck disable=SC2154  # $stderr is set by bats run --separate-stderr
#
# bin/gate-summary.sh turns steps.tsv fixture files into a Markdown verdict.
# It reads files only (no ssh, sudo, or power commands), so these tests need
# no stubs and are exempt from the stub-safety assertion.

bats_require_minimum_version 1.5.0


setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    S="$REPO/bin/gate-summary.sh"
}

# mkrun <name> <row>...: rows are "step:result:note"; later steps are not
# auto-filled, the fixture is exactly what it is told.
mkrun() {
    local d="$BATS_TEST_TMPDIR/$1"; shift
    mkdir -p "$d"
    : > "$d/steps.tsv"
    local r step res note
    for r in "$@"; do
        step=${r%%:*}; r=${r#*:}; res=${r%%:*}; note=${r#*:}
        printf '%s\t%s\t%s\t%s\n' "$step" "$res" 3 "$note" >> "$d/steps.tsv"
    done
    printf '%s\n' "$d"
}

# full <name> [step:result:note]...: the complete 3-restart run, all pass,
# with the given overrides; every row after the first non-pass becomes skip.
full() {
    local name=$1; shift
    local args=() st o ov res note broke=0
    for st in preflight plugin build-base build-layer varstore boot versions health reach host-uuid \
        restart-1 restart-2 restart-3 shutdown; do
        res=pass; note=
        for o in "$@"; do
            if [ "${o%%:*}" = "$st" ]; then ov=${o#*:}; res=${ov%%:*}; note=${ov#*:}; fi
        done
        if [ "$broke" = 1 ]; then res=skip; note=; fi
        [ "$res" = pass ] || [ "$res" = n/a ] || broke=1
        args+=("$st:$res:$note")
    done
    mkrun "$name" "${args[@]}"
}

allpass() { full "$1"; }

@test "all pass: Passed line first, counts restarts, exit 0" {
    c=$(allpass cand)
    run "$S" "$c"
    [ "$status" -eq 0 ]
    [ "${lines[0]}" = "Passed: every step, 3 restarts" ]
}

@test "a run without a varstore row (a gate from before the guard) is well formed" {
    c=$(mkrun cand preflight:pass: plugin:pass: build-base:pass: build-layer:pass: \
        boot:pass: versions:pass: health:pass: reach:pass: host-uuid:pass: restart-1:pass: shutdown:pass:)
    run "$S" "$c"
    [ "$status" -eq 0 ]
    [ "${lines[0]}" = "Passed: every step, 1 restarts" ]
}

@test "varstore fails, a control from before the guard has no such row and passed: Regression" {
    c=$(full cand "varstore:fail:FAIL: 799963 flash bytes programmed")
    k=$(mkrun ctl preflight:pass: plugin:pass: build-base:pass: build-layer:pass: \
        boot:pass: versions:pass: health:pass: reach:pass: host-uuid:pass: restart-1:pass: shutdown:pass:)
    run "$S" "$c" "$k"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "Regression: varstore -- FAIL: 799963 flash bytes programmed" ]
}

@test "a varstore row anywhere but after build-layer is malformed" {
    c=$(mkrun cand preflight:pass: plugin:pass: build-base:pass: build-layer:pass: \
        boot:pass: varstore:pass: versions:pass: health:pass: reach:pass: host-uuid:pass: restart-1:pass: shutdown:pass:)
    run "$S" "$c"
    [ "$status" -eq 2 ]
}

@test "candidate fail, control passed the step: Regression" {
    c=$(full cand "versions:fail:Lilu is 1.7.2 not 1.7.3")
    k=$(allpass ctl)
    run "$S" "$c" "$k"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "Regression: versions -- Lilu is 1.7.2 not 1.7.3" ]
}

@test "restart fail: Regression needs the control to pass every restart row" {
    c=$(full cand "restart-2:fail:did not restart")
    k=$(allpass ctl)
    run "$S" "$c" "$k"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "Regression: restart-2 -- did not restart" ]
}

@test "candidate infra: CI itself failed, control not needed" {
    c=$(full cand "build-base:infra:no space left on device")
    run "$S" "$c"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "No clear regression: CI itself failed at build-base -- no space left on device" ]
}

@test "control fails too: main fails it too" {
    c=$(full cand "boot:fail:no ssh")
    k=$(full ctl "versions:fail:wrong version")
    run "$S" "$c" "$k"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "No clear regression: main fails it too -- the control failed versions -- wrong version" ]
}

@test "control infra: the control could not run" {
    c=$(full cand "boot:fail:no ssh")
    k=$(full ctl "preflight:infra:port 2222 taken")
    run "$S" "$c" "$k"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "No clear regression: the control could not run -- preflight -- port 2222 taken" ]
}

@test "control directory missing: the control could not run" {
    c=$(full cand "boot:fail:no ssh")
    run "$S" "$c" "$BATS_TEST_TMPDIR/nope"
    [ "$status" -eq 1 ]
    [[ "${lines[0]}" == "No clear regression: the control could not run -- "* ]]
}

@test "candidate failed and no control asked for (a local run): Failed, saying there was no control" {
    c=$(full cand "boot:fail:no ssh")
    run "$S" "$c"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "Failed: boot -- no ssh (no control run; run bin/gate.sh on main to compare)" ]
}

@test "candidate failed, a control asked for but its directory empty or blank: the control could not run" {
    c=$(full cand "boot:fail:no ssh")
    mkdir -p "$BATS_TEST_TMPDIR/empty"
    run "$S" "$c" "$BATS_TEST_TMPDIR/empty"
    [ "$status" -eq 1 ]
    [[ "${lines[0]}" == "No clear regression: the control could not run -- control -- no steps.tsv in $BATS_TEST_TMPDIR/empty" ]]
    run "$S" "$c" ""
    [ "$status" -eq 1 ]
    [[ "${lines[0]}" == "No clear regression: the control could not run -- "* ]]
}

@test "one directory alone, candidate infra: still CI itself, never Failed" {
    c=$(full cand "build-base:infra:disk")
    run "$S" "$c"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "No clear regression: CI itself failed at build-base -- disk" ]
}

@test "empty note prints (no note)" {
    c=$(full cand preflight:infra:)
    run "$S" "$c"
    [ "${lines[0]}" = "No clear regression: CI itself failed at preflight -- (no note)" ]
}

@test "malformed steps.tsv: exit 2 naming the line" {
    d="$BATS_TEST_TMPDIR/bad"; mkdir -p "$d"
    printf 'preflight\tpass\t1\t\nplugin\tpass\t2\n' > "$d/steps.tsv"
    run --separate-stderr "$S" "$d"
    [ "$status" -eq 2 ]
    [[ "$stderr" == *"steps.tsv"*"line 2"* ]]
}

@test "unknown result word: exit 2 naming the line" {
    d="$BATS_TEST_TMPDIR/bad2"; mkdir -p "$d"
    printf 'preflight\tmaybe\t1\tx\n' > "$d/steps.tsv"
    run --separate-stderr "$S" "$d"
    [ "$status" -eq 2 ]
    [[ "$stderr" == *"line 1"* ]]
}

@test "candidate whose first non-pass is skip: exit 2 naming it" {
    c=$(mkrun cand preflight:pass: plugin:skip: build-base:skip:)
    run --separate-stderr "$S" "$c"
    [ "$status" -eq 2 ]
    [[ "$stderr" == *"plugin"* ]]
}

@test "missing candidate steps.tsv: exit 2" {
    run "$S" "$BATS_TEST_TMPDIR/nope"
    [ "$status" -eq 2 ]
}

@test "no arguments or too many: exit 2" {
    run "$S"
    [ "$status" -eq 2 ]
    run "$S" a b c
    [ "$status" -eq 2 ]
}

@test "table lists both runs and the log excerpt follows it" {
    c=$(full cand "boot:fail:no ssh")
    k=$(allpass ctl)
    seq 1 60 | sed 's/^/line /' > "$c/boot.log"
    run "$S" "$c" "$k"
    out=$output
    [ "$(printf '%s\n' "$out" | grep -c '^| boot | fail |')" -eq 1 ]
    printf '%s\n' "$out" | grep -q '^### Candidate'
    printf '%s\n' "$out" | grep -q '^### Control'
    printf '%s\n' "$out" | grep -q '^line 60$'
    printf '%s\n' "$out" | grep -q '^line 21$'
    [ "$(printf '%s\n' "$out" | grep -c '^line 20$')" -eq 0 ]
    t=$(printf '%s\n' "$out" | grep -n '^| boot | fail |' | cut -d: -f1)
    l=$(printf '%s\n' "$out" | grep -n '^line 60$' | cut -d: -f1)
    [ "$l" -gt "$t" ]
}

# --- structure: a truncated run must never read as a pass ---

@test "empty steps.tsv: exit 2" {
    d="$BATS_TEST_TMPDIR/e"; mkdir -p "$d"; : > "$d/steps.tsv"
    run --separate-stderr "$S" "$d"
    [ "$status" -eq 2 ]
    [[ "$stderr" == *empty* ]]
}

@test "only preflight pass: exit 2 naming the missing step" {
    c=$(mkrun cand preflight:pass:)
    run --separate-stderr "$S" "$c"
    [ "$status" -eq 2 ]
    [[ "$stderr" == *plugin* ]]
}

@test "shutdown missing: exit 2 naming shutdown" {
    c=$(mkrun cand preflight:pass: plugin:pass: build-base:pass: build-layer:pass: \
        boot:pass: versions:pass: health:pass: reach:pass: host-uuid:pass: restart-1:pass:)
    run --separate-stderr "$S" "$c"
    [ "$status" -eq 2 ]
    [[ "$stderr" == *shutdown* ]]
}

@test "restart-2 missing: exit 2 naming restart-2" {
    c=$(mkrun cand preflight:pass: plugin:pass: build-base:pass: build-layer:pass: \
        boot:pass: versions:pass: health:pass: reach:pass: host-uuid:pass: restart-1:pass: restart-3:pass: shutdown:pass:)
    run --separate-stderr "$S" "$c"
    [ "$status" -eq 2 ]
    [[ "$stderr" == *restart-2* ]]
}

@test "empty step field: exit 2 naming line 1" {
    c=$(mkrun cand :pass:)
    run --separate-stderr "$S" "$c"
    [ "$status" -eq 2 ]
    [[ "$stderr" == *"line 1"* ]]
}

@test "out of order and duplicate rows: exit 2" {
    c=$(mkrun cand plugin:pass: preflight:pass:)
    run -2 "$S" "$c"
    c=$(mkrun cand2 preflight:pass: preflight:pass:)
    run -2 "$S" "$c"
}

@test "candidate with a pass after its first non-pass: exit 2" {
    c=$(mkrun cand preflight:pass: plugin:fail:x build-base:pass: build-layer:skip: boot:skip: \
        versions:skip: health:skip: reach:skip: host-uuid:skip: restart-1:skip: shutdown:skip:)
    run --separate-stderr "$S" "$c"
    [ "$status" -eq 2 ]
    [[ "$stderr" == *build-base* ]]
}

@test "truncated all-pass control is not a Regression" {
    c=$(full cand "versions:fail:bad")
    k=$(mkrun ctl preflight:pass: plugin:pass: build-base:pass: build-layer:pass: boot:pass: versions:pass:)
    run "$S" "$c" "$k"
    [ "$status" -eq 1 ]
    [[ "${lines[0]}" == "No clear regression: the control could not run -- health -- "* ]]
}

@test "control passes restart-2 but fails restart-3, candidate fails restart-2: main fails it too" {
    c=$(full cand "restart-2:fail:did not restart")
    k=$(full ctl "restart-3:fail:also bad")
    run "$S" "$c" "$k"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "No clear regression: main fails it too -- the control failed restart-3 -- also bad" ]
}

@test "control lacking a restart row: the control could not run" {
    c=$(full cand "restart-1:fail:x")
    k=$(mkrun ctl preflight:pass: plugin:pass: build-base:pass: build-layer:pass: \
        boot:pass: versions:pass: health:pass: reach:pass: host-uuid:pass: restart-1:pass: restart-3:pass: shutdown:pass:)
    run "$S" "$c" "$k"
    [ "$status" -eq 1 ]
    [[ "${lines[0]}" == "No clear regression: the control could not run -- restart-2 -- "* ]]
}

@test "control with a pass after its first non-pass: the control could not run, exit 1" {
    c=$(full cand "boot:fail:x")
    k=$(mkrun ctl preflight:pass: plugin:fail:y build-base:pass: build-layer:skip: boot:skip: \
        versions:skip: health:skip: reach:skip: host-uuid:skip: restart-1:skip: shutdown:skip:)
    run "$S" "$c" "$k"
    [ "$status" -eq 1 ]
    [[ "${lines[0]}" == "No clear regression: the control could not run -- build-base -- "* ]]
}

@test "malformed control columns: the control could not run, not exit 2" {
    c=$(full cand "boot:fail:x")
    k="$BATS_TEST_TMPDIR/badctl"; mkdir -p "$k"; printf 'preflight\tpass\n' > "$k/steps.tsv"
    run "$S" "$c" "$k"
    [ "$status" -eq 1 ]
    [[ "${lines[0]}" == "No clear regression: the control could not run -- "* ]]
}

# the notes bin/gate.sh writes for 10.6's two n/a steps, read from it so they never drift
sl_note() { sed -n "s/^ *$1) echo \"\(.*\)\" ;;\$/\1/p" "$REPO/bin/gate.sh"; }

# sl <name> [step:result:note]...: a 10.6 run (full's), its release.txt saying so, build-layer and
# reach n/a with gate.sh's notes
sl() {
    local name=$1 d
    shift
    d=$(full "$name" "build-layer:n/a:$(sl_note build-layer)" "reach:n/a:$(sl_note reach)" "$@")
    echo 10.6 > "$d/release.txt"
    printf '%s\n' "$d"
}

@test "a 10.6 run: build-layer and reach not applicable, with their reasons, passes and names them" {
    [ -n "$(sl_note build-layer)" ] && [ -n "$(sl_note reach)" ]
    c=$(sl cand)
    run "$S" "$c"
    [ "$status" -eq 0 ]
    [ "${lines[0]}" = "Passed: every step that applies, 3 restarts; not applicable: build-layer, reach" ]
    [[ "$output" == *"| build-layer | n/a | 3 | $(sl_note build-layer) |"* ]]
    [[ "$output" == *"| reach | n/a | 3 | $(sl_note reach) |"* ]]
}

@test "a step not applicable is never the first non-pass: a later fail is the verdict" {
    c=$(sl cand "host-uuid:fail:FAIL host-uuid: x")
    run "$S" "$c"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "Failed: host-uuid -- FAIL host-uuid: x (no control run; run bin/gate.sh on main to compare)" ]
    k=$(sl ctl)
    run "$S" "$c" "$k"
    [ "${lines[0]}" = "Regression: host-uuid -- FAIL host-uuid: x" ]
}

@test "n/a after an earlier non-pass is malformed: every row after it is skip" {
    c=$(mkrun cand preflight:pass: plugin:pass: build-base:fail:x build-layer:n/a:y varstore:skip: \
        boot:skip: versions:skip: health:skip: reach:skip: host-uuid:skip: restart-1:skip: shutdown:skip:)
    echo 10.6 > "$c/release.txt"
    run --separate-stderr "$S" "$c"
    [ "$status" -eq 2 ]
    [[ "$stderr" == *"n/a after an earlier non-pass; must be skip"* ]]
}

@test "only build-layer and reach can be not applicable: no other check is ever set aside" {
    for st in preflight plugin build-base varstore boot versions health host-uuid restart-1 shutdown; do
        c=$(full "cand-$st" "$st:n/a:why")
        echo 10.6 > "$c/release.txt"
        run --separate-stderr "$S" "$c"
        [ "$status" -eq 2 ] || { echo "$st: $status ${lines[0]}"; false; }
        [[ "$stderr" == *"$st"*"n/a, which only build-layer and reach can be"* ]]
    done
    # and one that is says why
    c=$(full cand-noreason "build-layer:n/a:")
    echo 10.6 > "$c/release.txt"
    run --separate-stderr "$S" "$c"
    [ "$status" -eq 2 ]
    [[ "$stderr" == *"n/a with no reason (step build-layer)"* ]]
}

@test "runs of different releases are never compared: exit 2 naming both" {
    c=$(full cand "versions:fail:FAIL lilu: x")
    echo 10.6 > "$c/release.txt"
    k=$(allpass ctl)
    echo 10.9 > "$k/release.txt"
    run --separate-stderr "$S" "$c" "$k"
    [ "$status" -eq 2 ]
    [[ "$stderr" == *"the candidate is a 10.6 run and the control a 10.9 run: compare runs of one release"* ]]
    # a run with no release.txt is from before the gate knew releases, when it booted 10.9 alone
    rm "$k/release.txt"
    run --separate-stderr "$S" "$c" "$k"
    [ "$status" -eq 2 ]
    [[ "$stderr" == *"the candidate is a 10.6 run and the control a 10.9 run"* ]]
    echo 10.9 > "$c/release.txt"
    run "$S" "$c" "$k"
    [ "$status" -eq 1 ]
    [ "${lines[0]}" = "Regression: versions -- FAIL lilu: x" ]
}

@test "the tables name each run's release" {
    c=$(allpass cand)
    echo 10.6 > "$c/release.txt"
    run "$S" "$c"
    [[ "$output" == *"### Candidate (10.6)"* ]]
    rm "$c/release.txt"
    run "$S" "$c"
    [[ "$output" == *"### Candidate (10.9)"* ]]
}

@test "a release.txt naming no release the gate knows is malformed: exit 2" {
    c=$(allpass cand)
    echo 10.4 > "$c/release.txt"
    run --separate-stderr "$S" "$c"
    [ "$status" -eq 2 ]
    [[ "$stderr" == *"release.txt names '10.4'"* ]]
}

@test "n/a only on a 10.6 run: a 10.9 run, or one with no release.txt (10.9, from before), is malformed" {
    c=$(full cand "build-layer:n/a:$(sl_note build-layer)" "reach:n/a:$(sl_note reach)")
    echo 10.9 > "$c/release.txt"
    run --separate-stderr "$S" "$c"
    [ "$status" -eq 2 ]
    [[ "$stderr" == *"line 4: build-layer is n/a on a 10.9 run, where every step applies (step build-layer)"* ]]
    rm "$c/release.txt"
    run --separate-stderr "$S" "$c"
    [ "$status" -eq 2 ]
    [[ "$stderr" == *"build-layer is n/a on a 10.9 run, where every step applies"* ]]
    # and a 10.9 control that does so is no control either
    k=$(full ctl "reach:n/a:x")
    c=$(full cand2 "versions:fail:FAIL x")
    run "$S" "$c" "$k"
    [ "$status" -eq 1 ]
    [[ "${lines[0]}" == "No clear regression: the control could not run -- reach -- the control's steps.tsv is incomplete or malformed: "*"n/a on a 10.9 run"* ]]
}

@test "a control whose release.txt names no release the gate knows is a usage error, as the header says" {
    c=$(full cand "versions:fail:FAIL x")
    k=$(allpass ctl)
    echo 10.4 > "$k/release.txt"
    run --separate-stderr "$S" "$c" "$k"
    [ "$status" -eq 2 ]
    [[ "$stderr" == *"ctl/release.txt names '10.4'"* ]]
    head -n 30 "$S" | grep -qF "release.txt names no release it knows"
}
