#!/usr/bin/env bash
# platform: host-agnostic
# Usage: bats-watchdog.sh --run COMMAND [ARG...]
#
# Runs COMMAND (bats, or a runner of it) as a job in a process group of
# its own, watches it, and exits with its status: or with 1, saying why,
# when it ended anything; or with 2, having run nothing, when it cannot
# watch. bin/run-tests.sh and CI's test step run the suite so.
#
# Two ways a run hangs, and what this does about each:
#
# 1. A TEST THAT NEVER ENDS. bats' own timeout kills only the test's direct
#    children. A loop in a grandchild, as under `run`, outlives its parent,
#    holds the test's output open, and the run waits for it for good
#    (MEASURED 2026-10-19: an unbounded wait in a test of /etc/rc.server
#    held bats 693 s, past its 120 s timeout, until the loop was killed by
#    hand). So a test (a bats-exec-test that is not a test's own subshell)
#    seen running for LIMIT seconds, below bats' timeout, is ended with
#    everything under it, and named; bats reports it "not ok" and goes on.
#
# 2. WHAT A TEST LEFT RUNNING. A job a test put in the background and never
#    waited for outlives the test, under no test, and holds its output, so
#    bats waits at the end of the file or run. It is found by its process
#    group, which the kernel keeps: a process keeps its group when its
#    parent exits, and no process outside the run can join a group made
#    under it. This keeps the groups made under COMMAND's job (the job's
#    own, from job control, and any whose leader is in the job's tree:
#    shipyard's runner makes one per file), never its own or its caller's,
#    and only while each has members (regroup): an empty group's number
#    can go to anyone's. A member of such a group
#    that is outside the job's tree, in a group with no test running, seen
#    so on two looks in a row, is a leftover: named, ended, and the run
#    fails. A daemon a test starts, outside the tree, is left alone while a
#    test runs in its group. After COMMAND ends, the groups are looked over
#    a last two times, a second apart, for a leftover that held nothing.
#
#    A process that left the run's groups (setsid, job control, a QEMU's
#    -daemonize; tests/test_timeout.bats bans them in test code) is not
#    seen. If it holds the run's output, the run goes on with no test
#    running for LIMIT seconds: this says it stalled, ends the run's
#    groups, and fails it, unable to say what held it.
#
# WHAT IT DOES NOT REACH. setup_file, teardown_file and a test file's own
# top-level code run in bats-exec-file, and setup_suite and teardown_suite
# in bats-exec-suite, outside every test, beyond both bats' timeout and
# the limit here; tests/test_timeout.bats forbids all four.
#
# It asks ps for one listing only, the same on Linux, macOS and NetBSD:
# `ps -ww -A -o pid= -o ppid= -o pgid= -o args=` (-ww: no width limit; ps
# cuts args at $COLUMNS, tty or not). It reads no environment.
#
#   env: BATS_WATCHDOG_LIMIT  seconds a test may run (default: a little
#                             below BATS_TEST_TIMEOUT, limit_for)
#        BATS_WATCHDOG_POLL   seconds between looks (default 5)
#
# Sourced, it defines its functions and does nothing else (for its tests).

# default_limit <timeout> <poll>: below a bats timeout of that many
# seconds, so this ends the test first, while bats can still say so: by 10
# s from 20 s up and a tenth below, and always by a poll and 2 s more (the
# time to see it and end it), or bats' countdown can fire as it does and
# the test's result line is lost (measured under load, with 1 s to spare).
# A timeout too short for that leaves under half of it to a test, and is
# refused, with exit 2.
default_limit() {
    local t=$1 p=$2 m l
    if [ "$t" -ge 20 ]; then m=10; else m=$(((t + 9) / 10)); fi
    [ "$m" -ge $((p + 2)) ] || m=$((p + 2))
    l=$((t - m))
    if [ "$l" -lt 1 ] || [ $((l * 2)) -lt "$t" ]; then
        echo "bats-watchdog: BATS_TEST_TIMEOUT=$t is too short to end a test before bats does, polling every ${p}s; raise it, or lower BATS_WATCHDOG_POLL" >&2
        exit 2
    fi
    echo "$l"
}

# seconds_or_refuse <name> <value>: <value>, if it is a whole number of
# seconds, 1 or more; otherwise says so and exits 2. A watchdog that went on
# with a limit it cannot compare with would never fire.
seconds_or_refuse() {
    case $2 in
        '' | *[!0-9]*) ;;
        *) if [ "$2" -ge 1 ]; then echo "$2"; return 0; fi ;;
    esac
    echo "bats-watchdog: $1 must be a whole number of seconds, 1 or more, not '$2'" >&2
    exit 2
}

# limit_for <BATS_WATCHDOG_LIMIT> <poll>: the limit a test may run for: the
# first when it is given, else default_limit of BATS_TEST_TIMEOUT (120 when
# unset), else, when that is set but empty (bats' own "no timeout"),
# nothing: no limit. Leftovers are still ended. Exits 2 for a value that is
# not seconds.
limit_for() {
    local t
    if [ -n "$1" ]; then
        seconds_or_refuse BATS_WATCHDOG_LIMIT "$1"
    elif [ -n "${BATS_TEST_TIMEOUT-120}" ]; then
        t=$(seconds_or_refuse BATS_TEST_TIMEOUT "${BATS_TEST_TIMEOUT-120}") || exit 2
        default_limit "$t" "$2"
    fi
}

# snapshot: every process, as "pid ppid pgid born args", into $table, born
# being when it started on ps's own clock: this script's age, from its own
# row ($clock; $SECONDS without one), less its elapsed time, POSIX's
# [[dd-]hh:]mm:ss (procps, macOS and NetBSD alike), so a step of the wall
# clock, which $SECONDS follows in some bash, cannot make a new process
# look old. One it cannot read counts as born now, so is never signalled
# (see olds).
# Exits 2 when ps lists none, rather than watch nothing.
snapshot() {
    table=$(ps -ww -A -o pid= -o ppid= -o pgid= -o etime= -o args= 2>/dev/null) || table=
    [ -n "$table" ] || {
        echo "bats-watchdog: ps listed no processes; stopping rather than watching nothing" >&2
        exit 2
    }
    table=$(awk -v me="$$" -v fallback="$SECONDS" '
        function age(e,  d, n, a) {
            if (e !~ /^([0-9]+-)?([0-9]+:)?([0-9]+:)?[0-9]+$/) return -1
            d = 0
            if (index(e, "-")) { d = substr(e, 1, index(e, "-") - 1); e = substr(e, index(e, "-") + 1) }
            n = split(e, a, ":")
            if (n == 3) return d * 86400 + a[1] * 3600 + a[2] * 60 + a[3]
            if (n == 2) return d * 86400 + a[1] * 60 + a[2]
            return d * 86400 + a[1]
        }
        { line[NR] = $0; s[NR] = age($4); if ($1 == me && s[NR] >= 0) now = s[NR] }
        END {
            if (now == "") now = fallback
            print now
            for (i = 1; i <= NR; i++) { $0 = line[i]; $4 = (s[i] < 0 ? now : now - s[i]); print }
        }' <<EOT
$table
EOT
)
    clock=${table%%"$nl"*}
    table=${table#*"$nl"}
}
nl='
'

# olds: " pid pid ... ", each process in $table that was there at the last
# look, at $last_look: started 2 s before it, at least, as etime and
# $SECONDS each round down a second. Nothing else is ever signalled: a
# process that started since may have any pid, a reused one among them.
olds() { awk -v l="$last_look" 'BEGIN { printf " " } $4 <= l - 2 { printf "%s ", $1 }' <<EOT
$table
EOT
}

# field_of <pid> <n>: its nth field in $table (2 ppid, 3 pgid, 4 born, 5
# args, to the end of the line).
field_of() { awk -v p="$1" -v n="$2" '$1 == p { if (n == 5) { $1 = $2 = $3 = $4 = ""; sub(/^ +/, "") } else $0 = $n; print; exit }' <<EOT
$table
EOT
}

# below <pid>: " <pid> " and every pid under it in $table, space-separated.
below() {
    awk -v r="$1" '{ pp[$1] = $2 }
        END {
            printf " "
            for (p in pp) {
                q = p; n = 0
                while (q > 1 && n < 64) { if (q == r) { printf "%s ", p; break }; q = pp[q]; n++ }
            }
        }' <<EOT
$table
EOT
}

# descendants <pid>: every pid under it in $table, deepest first.
descendants() {
    local c
    awk -v p="$1" '$2 == p { print $1 }' <<EOT | while read -r c; do
$table
EOT
        descendants "$c"
        echo "$c"
    done
}

# Filter unrelated rows in awk before the shell loops below. Re-reading
# the whole host table in bash for every tracked group made a look take
# seconds under load, enough for bats' short countdown to beat this one.
# The filters change neither ownership nor the age checks before signals.
# live_tests <tree>: "pid pgid args" for each test in $table whose pid is
# in <tree> (" pid pid ... "). A test's own subshells (run's among them)
# carry its bats-exec-test argv too; only the outermost is the test, so it
# is named once, whichever pid is lower.
live_tests() {
    local tree=$1 pid ppid pgid args
    while read -r pid ppid pgid _ args; do
        case $args in *bats-exec-test*) ;; *) continue ;; esac
        case $tree in *" $pid "*) ;; *) continue ;; esac
        case $(field_of "$ppid" 5) in *bats-exec-test*) continue ;; esac
        echo "$pid $pgid $args"
    done < <(awk 'index($0, "bats-exec-test")' <<EOT
$table
EOT
)
}

# named <args>: a test's name from bats-exec-test argv ("... --dummy-flag
# <file> <name> <n> <n> <try>"), else the args as they are.
named() { echo "${1#*--dummy-flag }"; }

# regroup <tree>: updates $groups, the run's process groups, and $members,
# theirs ("pid:pgid"), from $table. A group is the run's when made under
# it: its leader (pid = pgid) in <tree>. It stays the run's while it has
# members, and only while it can be the same group as at the last look:
# one of its members is in <tree>, or was a member then and is the same
# process, there since then (olds); or, for the job's own group, its leader
# was. And never once its number leads a group started since the last
# look. The kernel gives a group's number to no other while it has a
# member; once it is empty, the number can go to anyone's group, at once,
# which must not then be taken for the run's. Sets $emptied: the groups
# with no member at all in $table.
regroup() {
    local tree=$1 pid ppid pgid args g kept=' ' now=' ' old new=' '
    old=$(olds)
    emptied=' '
    while read -r pid ppid pgid _ args; do
        case $tree in *" $pid "*) ;; *) continue ;; esac
        [ "$pid" = "$pgid" ] || continue
        case $groups in *" $pgid "*) ;; *) groups="$groups$pgid " ;; esac
    done < <(awk '$1 == $3' <<EOT
$table
EOT
)
    for g in $groups; do
        case " $(awk '{ printf "%s ", $3 }' <<EOT
$table
EOT
)" in *" $g "*) ;; *) emptied="$emptied$g " ;; esac
        while read -r pid ppid pgid _ args; do
            [ "$pgid" = "$g" ] || continue
            now="$now$pid:$g "
            case $tree in *" $pid "*) kept="$kept$g "; continue ;; esac
            # A leader outside the tree, started since the last look: its
            # group is a new one, with an old number.
            if [ "$pid" = "$g" ]; then
                case $old in *" $pid "*) ;; *) new="$new$g " ;; esac
            fi
            # The job's own, whose leader this watched until it ended, a
            # poll ago at most: what a short run left in it, unseen till now.
            # ("job:job" stays in $members while the group is kept.)
            [ "$g" != "$job" ] || case $members in *" $g:$g "*) kept="$kept$g " ;; esac
            case $old in *" $pid "*) ;; *) continue ;; esac
            case $members in *" $pid:$g "*) kept="$kept$g " ;; esac
        done < <(awk -v g="$g" '$3 == g' <<EOT
$table
EOT
)
    done
    groups=' '
    for g in $kept; do
        case $new in *" $g "*) continue ;; esac
        case $groups in *" $g "*) ;; *) groups="$groups$g " ;; esac
    done
    case $groups in *" $job "*) case $now in *" $job:$job "*) ;; *) now="$now$job:$job " ;; esac ;; esac
    members=$now
}

# nap <seconds>: sleeps, a second at a time, until then or until the job
# ends, its output on /dev/null, so nothing outlives this holding its
# caller's stdout and stderr.
nap() {
    local n=0
    while [ "$n" -lt "$1" ] && kill -0 "$job" 2>/dev/null; do
        sleep 1 > /dev/null 2>&1
        n=$((n + 1))
    done
}

# look: one look at the run: ends a test past its limit, and leftovers
# seen on this look and the last; says when the run has stalled.
look() {
    local tree pid ppid pgid args tests g t now candidates='' firsts='' busy='' old c before
    snapshot
    tree=$(below "$job")
    before="$groups|$members"
    regroup "$tree"
    # ps's listing is not one instant: it reads each process in turn, so a
    # group whose members end as it goes, while one starts after it began,
    # can look empty though it never was (MEASURED: 34 of 100 such groups),
    # and the run's quiet leftover with it. A group that looks empty is
    # looked at again, in a listing of its own, before it is let go.
    if [ "$emptied" != ' ' ]; then
        groups=${before%%|*} members=${before#*|}
        snapshot
        tree=$(below "$job")
        regroup "$tree"
    fi
    now=$clock
    old=$(olds)
    # Tests: each first seen when, and past the limit, ended.
    tests=$(live_tests "$tree")
    while read -r pid pgid args; do
        [ -n "$pid" ] || continue
        busy="$busy $pgid "
        t=$(awk -v k="$pid" '{ for (i = 1; i <= NF; i++) { split($i, a, ":"); if (a[1] == k) print a[2] } }' <<EOT
$first
EOT
)
        [ -n "$t" ] || t=$now
        firsts="$firsts $pid:$t"
        [ -n "$limit" ] || continue
        [ $((now - t)) -ge "$limit" ] || continue
        case $old in *" $pid "*) ;; *) continue ;; esac
        case " $ended_tests " in
            *" $pid "*) ;;
            *)
                echo "bats-watchdog: a test ran past ${limit}s; ended it and all it started: $(named "$args")" >&2
                ended_tests="$ended_tests $pid"
                ended=1
                # Tell the shell first: killing run's subshell can itself
                # start its EXIT trap. A TERM arriving during that trap
                # terminates bash before bats prints the result. Send it
                # only once, never again on a later poll during teardown.
                kill -TERM "$pid" 2>/dev/null
                ;;
        esac
        # What started since the last look is left: once its parent is
        # gone, it is outside the tree, and the next looks end it.
        for c in $(descendants "$pid"); do
            case $old in *" $c "*) kill -KILL "$c" 2>/dev/null ;; esac
        done
    done <<EOT
$tests
EOT
    first=$firsts
    # Leftovers: outside the tree, in a group of the run's with no test in
    # it, now and at the last look.
    while read -r pid ppid pgid _ args; do
        case $groups in *" $pgid "*) ;; *) continue ;; esac
        case $tree$mine in *" $pid "*) continue ;; esac
        case $busy in *" $pgid "*) continue ;; esac
        candidates="$candidates $pid:$pgid "
        case $suspects in *" $pid:$pgid "*) ;; *) continue ;; esac
        case $old in *" $pid "*) ;; *) continue ;; esac
        echo "bats-watchdog: left behind by a test, still running after it ended; ended it: $(named "$args")" >&2
        kill -KILL "$pid" 2>/dev/null
        ended=1
    done < <(awk -v groups="$groups" 'index(groups, " " $3 " ")' <<EOT
$table
EOT
)
    suspects=$candidates found=$candidates
    # Stalled: no test for LIMIT seconds, and nothing of the run's to end,
    # while the job lives: what holds it is outside the run's groups.
    if [ -n "$tests" ] || [ -n "$candidates" ] || [ -z "$limit" ] || [ "$1" = last ]; then
        idle_since=
    elif [ -z "$idle_since" ]; then
        idle_since=$now
    elif [ $((now - idle_since)) -ge "$limit" ]; then
        echo "bats-watchdog: the run stalled: no test for ${limit}s, and something outside its process groups holds it (a setsid, say); ended its groups" >&2
        for g in $groups; do
            while read -r pid ppid pgid _ args; do
                [ "$pgid" = "$g" ] || continue
                case $mine in *" $pid "*) continue ;; esac
                case $old in *" $pid "*) ;; *) continue ;; esac
                kill -KILL "$pid" 2>/dev/null
            done <<EOT
$table
EOT
        done
        ended=1
        idle_since=
    fi
    last_look=$now
}

# last_looks: once the job has ended, a last look, and while it finds a
# leftover, more, a second apart (one is ended once it has been there since
# a look 2 s before), for what holds nothing open; with none, no more.
last_looks() {
    local n=0
    look last
    while [ -n "$found" ] && [ "$n" -lt 6 ]; do
        sleep 1
        look last
        n=$((n + 1))
    done
}

# forward <signal>: what an INT, a TERM or a hang-up to this does: passes
# it to the job's group, and goes on watching until the job ends; before
# there is a job, ends this, having run nothing. The job is this shell's
# only one, and bash signals it (%1) only while it has not reaped its
# leader, whose pid is the group's number: never a number gone to another.
forward() {
    if [ -z "$job" ]; then
        echo "bats-watchdog: $1 before the run started; nothing was run" >&2
        exit 2
    fi
    kill -"$1" %1 2>/dev/null
}

[ "${BASH_SOURCE[0]}" = "$0" ] || return 0
set -u
if [ "${1-}" != --run ] || [ $# -lt 2 ]; then
    echo "usage: bats-watchdog.sh --run COMMAND [ARG...]" >&2
    exit 2
fi
shift
refuse() { echo "bats-watchdog: nothing was run, and the run fails" >&2; exit 2; }
poll=$(seconds_or_refuse BATS_WATCHDOG_POLL "${BATS_WATCHDOG_POLL:-5}") || refuse
limit=$(limit_for "${BATS_WATCHDOG_LIMIT-}" "$poll") || refuse
( snapshot ) || refuse
snapshot
# Never this one, or what it runs under.
mine=" $$ "
p=$$
while p=$(field_of "$p" 2) && [ -n "$p" ] && [ "$p" -gt 1 ]; do
    case $mine in *" $p "*) break ;; esac
    mine="$mine$p "
done
first='' suspects='' found='' ended_tests='' ended=0 idle_since='' job='' members='' last_look=$SECONDS
# Set before the job starts: a signal before it is passed on once it has.
trap 'forward INT' INT
trap 'forward TERM' TERM
trap 'forward HUP' HUP

set -m
"$@" < /dev/null &
job=$!
set +m
# Job control made the job a group of its own, its pid: the run's first.
snapshot
jobs_group=$(field_of "$job" 3)
if [ -n "$jobs_group" ] && [ "$jobs_group" != "$job" ]; then
    echo "bats-watchdog: the job got no process group of its own ($jobs_group); nothing to tell the run by, and the run fails" >&2
    kill -KILL "$job"
    exit 2
fi
groups=" $job " members=" $job:$job " last_look=$clock

while kill -0 "$job" 2>/dev/null; do
    look running
    nap "$poll"
done
rc=0
wait "$job" || rc=$?
last_looks
if [ "$ended" = 1 ]; then
    echo "bats-watchdog: it ended a test that ran too long, what one left running, or a stalled run (above); the run fails" >&2
    [ "$rc" -ne 0 ] || rc=1
fi
exit "$rc"
