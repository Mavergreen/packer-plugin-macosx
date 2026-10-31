#!/usr/bin/env bats
# platform: host-agnostic
#
# assets/guest/autoinstall/autoinstall.sh's own behaviour: it runs inside
# the OS X Installer environment during an unattended install, so it is a
# shell script, and these tests run it: the static checks and the
# disk-selection behaviour, driven against a stub `diskutil`, since picking
# the wrong disk erases the installer media and "I read the code carefully"
# is not enough.
#
# The plist and cross-file checks are Go's (internal/media/inject_test.go):
# they read the embedded bytes directly and need no shell to run.

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    AUTO="$REPO/assets/guest/autoinstall/autoinstall.sh"
}

# Writes a stub `diskutil` that answers `list`, `list /dev/diskN` and
# `info /dev/diskN` from a table of "<id> <bytes> <slices>" lines, which it
# reads from diskutil.table beside it (a quoted heredoc, so the stub is the
# script that runs, and the lint reads it).
make_diskutil() {
    local spec
    mkdir -p "$BATS_TEST_TMPDIR/stub"
    STUB="$BATS_TEST_TMPDIR/stub/diskutil"
    : > "$STUB.table"
    for spec in "$@"; do
        printf '%s\n' "$spec" >> "$STUB.table"
    done
    cat > "$STUB" <<'STUB'
#!/bin/sh
table="$0.table"
# disk <id> <slices>: what `diskutil list` prints for it.
disk() {
    echo "/dev/$1"
    echo "   0:  something  *size  $1"
    i=1
    while [ "$i" -le "$2" ]; do
        echo "   $i:  Apple_HFS name  size  ${1}s$i"
        i=$((i + 1))
    done
}
case "$1 ${2:-}" in
"list ")
    while read -r id _ slices; do disk "$id" "$slices"; done < "$table"
    exit 0
    ;;
"partitionDisk /dev/"*)
    # partitionDisk /dev/diskN 1 GPTFormat JHFS+ <name> 100%: the volume
    # appears where disk arbitration mounts it.
    mkdir -p "$GUEST_MACOSX_VOLUMES/$6" && echo "Finished partitioning on ${2#/dev/}"
    exit $?
    ;;
"list /dev/"* | "info /dev/"*)
    while read -r id bytes slices; do
        [ "/dev/$id" = "$2" ] || continue
        if [ "$1" = list ]; then
            disk "$id" "$slices"
        else
            echo "   Total Size: x GB ($bytes Bytes) (exactly n 512-Byte-Units)"
        fi
        exit 0
    done < "$table"
    ;;
esac
echo "unhandled: $*" >&2
exit 1
STUB
    chmod +x "$STUB"
}

# SERIAL_WAIT=0: a dry run that started a follower anyway (a regression
# "a dry run starts no follower" is there to catch) then ends at once,
# rather than stalling every selection test for the port's 120 s.
run_selection() {
    run env GUEST_MACOSX_AUTOINSTALL_DRY_RUN=1 \
        GUEST_MACOSX_DISKUTIL="$STUB" \
        GUEST_MACOSX_AUTOINSTALL_LOG="$BATS_TEST_TMPDIR/auto.log" \
        GUEST_MACOSX_TARGET_WAIT=0 \
        GUEST_MACOSX_SERIAL_WAIT=0 \
        "$AUTO"
}

@test "autoinstall.sh partitions GPT, never the older Apple scheme" {
    run grep -c 'GPT' "$AUTO"
    [ "$output" -ge 1 ]
    run grep -ci 'APM\|Apple_partition_scheme' "$AUTO"
    [ "$output" = "0" ]
}

@test "autoinstall.sh refuses to erase when the target is ambiguous" {
    run grep -cE 'refus|ambiguous|more than one' "$AUTO"
    [ "$output" -ge 1 ]
}

@test "autoinstall.sh never hardcodes the first disk" {
    run grep -cE '/dev/disk0([^0-9]|$)' "$AUTO"
    [ "$output" = "0" ]
}

@test "the only unpartitioned disk is the one chosen" {
    # The template's own layout: a blank 60 GB target, the 6.7 GB
    # installer media, and the 200 MB OpenCore image.
    make_diskutil "disk0 64424509440 0" "disk1 6686769152 1" "disk2 201326592 1"
    run_selection
    [ "$status" -eq 0 ]
    [ "${lines[${#lines[@]}-1]}" = "disk0" ]
}

@test "the chosen disk is not assumed to be the first one" {
    make_diskutil "disk0 6686769152 1" "disk1 201326592 1" "disk2 64424509440 0"
    run_selection
    [ "$status" -eq 0 ]
    [ "${lines[${#lines[@]}-1]}" = "disk2" ]
}

@test "two blank disks erase nothing" {
    # The failure that matters. Two candidates means we cannot tell the
    # target from something else blank, so nothing is touched.
    make_diskutil "disk0 64424509440 0" "disk1 64424509440 0" "disk2 201326592 1"
    run_selection
    [ "$status" -ne 0 ]
    [[ "$output" == *"REFUSING TO PROCEED"* ]] || false
    [[ "$output" == *"more than one"* ]] || false
}

@test "no blank disk erases nothing" {
    make_diskutil "disk0 6686769152 1" "disk1 201326592 1"
    run_selection
    [ "$status" -ne 0 ]
    [[ "$output" == *"REFUSING TO PROCEED"* ]] || false
}

@test "a blank disk too small to be the target is not chosen" {
    # Guards against a future profile attaching a small scratch disk and
    # this quietly erasing it.
    make_diskutil "disk0 268435456 0" "disk1 6686769152 1"
    run_selection
    [ "$status" -ne 0 ]
    [[ "$output" == *"REFUSING TO PROCEED"* ]] || false
}

@test "the installer media is never a candidate, whatever its size" {
    # Partitioned is partitioned: the media is 6.7 GB and would pass a
    # size test on its own.
    make_diskutil "disk0 6686769152 1"
    run_selection
    [[ "$output" == *"already partitioned"* ]] || false
    [ "$status" -ne 0 ]
}

@test "the selection log says why each disk was rejected" {
    make_diskutil "disk0 64424509440 0" "disk1 6686769152 1" "disk2 201326592 1"
    run_selection
    run cat "$BATS_TEST_TMPDIR/auto.log"
    [[ "$output" == *"disk1: 6686769152 bytes, 1 partitions -- skipped"* ]] || false
    [[ "$output" == *"disk0: 64424509440 bytes, unpartitioned -- CANDIDATE"* ]] || false
}

@test "the selection log records the PATH autoinstall.sh runs with" {
    # rc.install runs rc.cdrom.local with launchd's default PATH, read from
    # the DVD's and Base System's rc scripts but logged by no build: this
    # line is what shows it (F23 review).
    make_diskutil "disk0 64424509440 0"
    run_selection
    run cat "$BATS_TEST_TMPDIR/auto.log"
    [[ "$output" == *"guest-macosx-autoinstall: PATH: "* ]] || false
}

# The hook's other job, run alone (GUEST_MACOSX_AUTOINSTALL_ONLY): following
# its own log and syslog to the serial port. Nothing it leaves running may
# write to the target volume once the installer runs (docs/decisions/0027).

run_follower() {
    run env GUEST_MACOSX_AUTOINSTALL_ONLY=stream_logs \
        GUEST_MACOSX_AUTOINSTALL_LOG="$BATS_TEST_TMPDIR/auto.log" \
        GUEST_MACOSX_STTY=true \
        "$AUTO"
}

# A UDP port nothing here listens on, for the follower's syslog: below every
# host's ephemeral range (Linux 32768-60999, macOS and NetBSD 49152-65535),
# where the host's own outgoing sockets land, and free when it is picked.
# 40000-59999 put it among Linux's: 12 of them were bound at a time, and a
# collision turns the follower's "syslog on" into "no syslog on".
syslog_port() {
    perl -MSocket -e 'for (1 .. 50) {
        my $p = 20000 + int(rand(12768));
        socket(my $s, PF_INET, SOCK_DGRAM, 0) or die "socket: $!\n";
        if (bind($s, pack_sockaddr_in($p, inet_aton("127.0.0.1")))) { print "$p\n"; exit 0 }
    } die "no free UDP port in 20000-32767\n"'
}

@test "the follower sends the hook's log and syslog's messages to the serial port, tagged" {
    : > "$BATS_TEST_TMPDIR/port"
    printf 'first\n' > "$BATS_TEST_TMPDIR/auto.log"
    p=$(syslog_port)
    # A partial line is held until its end comes; syslog's messages are
    # taken as they arrive.
    ( sleep 1.5
      printf 'half a li' >> "$BATS_TEST_TMPDIR/auto.log"
      printf '<13>Oct 18 22:16:58 localhost OSInstaller[263]: Volume passed consistency checks.' > "/dev/udp/127.0.0.1/$p"
      sleep 1.5
      printf 'ne\n' >> "$BATS_TEST_TMPDIR/auto.log" ) &
    bg=$!
    GUEST_MACOSX_SERIAL_PORTS="$BATS_TEST_TMPDIR/nothing $BATS_TEST_TMPDIR/port" GUEST_MACOSX_SYSLOG_PORT=$p \
        GUEST_MACOSX_FOLLOW_FOR=5 run_follower
    wait "$bg"
    [ "$status" -eq 0 ]
    run cat "$BATS_TEST_TMPDIR/port"
    [[ "$output" == *"follower: syslog on 127.0.0.1:$p"* ]] || { echo "$output"; false; }
    [[ "$output" == *"hook: first"* ]] || false
    [[ "$output" == *"hook: half a line"* ]] || false
    [[ "$output" == *"hook: guest-macosx-autoinstall: serial: following"* ]] || false
    [[ "$output" == *"syslog: <13>Oct 18 22:16:58 localhost OSInstaller[263]: Volume passed consistency checks."* ]] || false
    [ "$(grep -c '^hook: half' "$BATS_TEST_TMPDIR/port")" -eq 1 ]
}

@test "the follower logs the port it found and how long it waited" {
    : > "$BATS_TEST_TMPDIR/port"
    GUEST_MACOSX_SERIAL_PORTS="$BATS_TEST_TMPDIR/port" GUEST_MACOSX_SYSLOG_PORT=$(syslog_port) \
        GUEST_MACOSX_FOLLOW_FOR=1 run_follower
    run cat "$BATS_TEST_TMPDIR/auto.log"
    [[ "$output" == *"serial: following $BATS_TEST_TMPDIR/auto.log and syslog on 127.0.0.1:"*" to $BATS_TEST_TMPDIR/port (there after 0s)"* ]] || false
}

@test "with no serial port the follower says so" {
    GUEST_MACOSX_SERIAL_PORTS="$BATS_TEST_TMPDIR/no-such-port" GUEST_MACOSX_SERIAL_WAIT=0 \
        GUEST_MACOSX_FOLLOW_FOR=1 GUEST_MACOSX_SYSLOG_PORT="$(syslog_port)" run_follower
    [ "$status" -eq 0 ]
    run cat "$BATS_TEST_TMPDIR/auto.log"
    [[ "$output" == *"serial: no port ($BATS_TEST_TMPDIR/no-such-port) after 0s; the logs are not followed, only watched for the installer's failure"* ]] || false
}

@test "a follower with no syslog port says so and follows the hook's log" {
    : > "$BATS_TEST_TMPDIR/port"
    echo "a line" > "$BATS_TEST_TMPDIR/auto.log"
    # A port taken by someone else, as instlogd has its own.
    p=$(syslog_port)
    # shellcheck disable=SC2016 # Perl's own $s
    perl -e 'my $sa = $^O eq "darwin" ? pack("CCnC4x8", 16, 2, $ARGV[0], 127, 0, 0, 1)
                                      : pack("SnC4x8", 2, $ARGV[0], 127, 0, 0, 1);
             socket(my $s, 2, 2, 0) or die; bind($s, $sa) or die; sleep 4' "$p" &
    bg=$!
    sleep 1
    GUEST_MACOSX_SERIAL_PORTS="$BATS_TEST_TMPDIR/port" GUEST_MACOSX_SYSLOG_PORT=$p GUEST_MACOSX_FOLLOW_FOR=2 run_follower
    wait "$bg"
    run cat "$BATS_TEST_TMPDIR/port"
    [[ "$output" == *"follower: no syslog on 127.0.0.1:$p"* ]] || false
    [[ "$output" == *"hook: a line"* ]] || false
}

@test "nothing left running after the handoff writes to the target volume" {
    # The installer unmounts the target to check it; an open file of ours
    # there fails the install. The follower turns the log's mirror off
    # first thing and names no volume, and the copier loop is gone.
    run sed -n '/^stream_logs() {/,/^}/p' "$AUTO"
    [ "${lines[1]}" = "    MIRROR=0" ]
    [[ "$output" != *"/Volumes"* ]] || false
    run grep -c 'while :' "$AUTO"
    [ "$output" = 1 ] # the target's wait, which touches no /Volumes (the port's is the follower's own)
    run grep -n -A3 'while :' "$AUTO"
    [[ "$output" != *"cp "* ]] || false
    run grep -c '\.guest-macosx-logs' "$AUTO"
    [ "$output" = 0 ]
    run grep -A2 '^mirror_log() {' "$AUTO"
    [[ "$output" == *'[ "$MIRROR" = 1 ]'* ]] || false
}

# What the whole hook left running: every process carrying this run's
# token in its environment (read from /proc, by pid; the token, not a
# command line, says what is the hook's), one "<pid> <comm>" a line.
hook_leftovers() {
    local p
    for p in /proc/[0-9]*; do
        if { tr '\0' '\n' < "$p/environ"; } 2>/dev/null | grep -qx "GUEST_MACOSX_TEST_TOKEN=$TOKEN"; then
            echo "${p#/proc/} $(cat "$p/comm" 2>/dev/null)"
        fi
    done
}

teardown() {
    local pid _
    [ -n "${TOKEN:-}" ] || return 0
    hook_leftovers | while read -r pid _; do kill "$pid" 2>/dev/null; done
}

@test "after the handoff, nothing the hook leaves running writes to the target or holds anything there" {
    [ -d /proc/self/fd ] || skip "needs /proc to see the hook's processes"
    # The whole hook, as the installer environment runs it (not a dry run),
    # against a stub diskutil whose partitionDisk makes the volume under a
    # fake /Volumes. The serial port appears only after the hook has exited,
    # so everything the follower does, it does after the handoff, as it
    # would when kextd is slow to match the port.
    make_diskutil "disk0 64424509440 0" "disk1 6686769152 1"
    V="$BATS_TEST_TMPDIR/Volumes"
    mkdir -p "$V"
    TOKEN="guest-assets-$$-$RANDOM"
    port="$BATS_TEST_TMPDIR/port"
    timeout --foreground 60 env GUEST_MACOSX_TEST_TOKEN="$TOKEN" \
        GUEST_MACOSX_DISKUTIL="$STUB" GUEST_MACOSX_VOLUMES="$V" \
        GUEST_MACOSX_AUTOINSTALL_LOG="$BATS_TEST_TMPDIR/auto.log" GUEST_MACOSX_TARGET_WAIT=0 \
        GUEST_MACOSX_SERIAL_PORTS="$port" GUEST_MACOSX_SERIAL_WAIT=30 \
        GUEST_MACOSX_SYSLOG_PORT="$(syslog_port)" GUEST_MACOSX_FOLLOW_FOR=45 GUEST_MACOSX_STTY=true \
        "$AUTO" > "$BATS_TEST_TMPDIR/hook.out" 2>&1
    [ "$?" -eq 0 ] || { cat "$BATS_TEST_TMPDIR/hook.out"; false; }
    target="$V/Mavericks"
    [ -f "$target/.guest-macosx-autoinstall.log" ] # the handoff's own copy, before the installer
    touch "$BATS_TEST_TMPDIR/handoff"
    sleep 1
    : > "$port"
    # The follower finds the port (it looks each second; a loaded host may
    # take longer), and works on after the handoff for a few seconds more.
    # (A writer that came back to the target only every 20 s could slip
    # past this window; the leftover and fd checks below are the other net.)
    local n=0
    until grep -q "serial: following" "$BATS_TEST_TMPDIR/auto.log" || [ "$n" -ge 30 ]; do
        sleep 1
        n=$((n + 1))
    done
    sleep 6
    grep -q "^hook: guest-macosx-autoinstall: serial: following" "$port"
    # Nothing under the target changed or appeared since the handoff.
    run find "$target" -newer "$BATS_TEST_TMPDIR/handoff"
    [ "$status" -eq 0 ]
    [ -z "$output" ] || { echo "written after the handoff: $output"; false; }
    # What is left is the follower and its perl, and neither has a file, a
    # directory or its cwd on the target.
    run hook_leftovers
    echo "$output"
    [ "$(printf '%s\n' "$output" | awk '{ print $2 }' | sort | tr '\n' ' ')" = "autoinstall.sh perl " ]
    local pid comm
    while read -r pid comm; do
        if readlink "/proc/$pid/cwd" | grep -q "^$V"; then echo "$pid $comm: cwd on $V"; false; fi
        if find "/proc/$pid/fd" -maxdepth 1 -type l -exec readlink {} \; 2>/dev/null | grep -q "^$V"; then
            echo "$pid $comm holds a file on $V"; false
        fi
    done <<< "$output"
}

@test "a dry run starts no follower" {
    make_diskutil "disk0 64424509440 0"
    GUEST_MACOSX_SERIAL_WAIT=0 run_selection
    [ "$status" -eq 0 ]
    run cat "$BATS_TEST_TMPDIR/auto.log"
    [[ "$output" != *"serial:"* ]] || false
}

# A media assembly's forward step, run on the host: the lines from its
# "forwarding syslog" step to that step's fi, busybox's $B empty, the media
# a directory. $1: the disc's syslog.conf, as printf's format; the media's
# is $BATS_TEST_TMPDIR/mnt/private/etc/syslog.conf. $2: the assembly,
# 10.6's (disc/assemble-disc.sh, the default) or 10.9's (assemble.sh).
assemble_disc() {
    local mnt="$BATS_TEST_TMPDIR/mnt" script="$REPO/assets/privops/${2:-disc/assemble-disc.sh}"
    mkdir -p "$mnt/private/etc"
    # shellcheck disable=SC2059 # the format is the fixture
    printf "$1" > "$mnt/private/etc/syslog.conf"
    sed -n '/step "forwarding syslog/,/^    fi$/p' "$script" > "$BATS_TEST_TMPDIR/forward.sh"
    [ -s "$BATS_TEST_TMPDIR/forward.sh" ]
    run env B= GUEST_MACOSX_MNT="$mnt" sh -c 'step() { :; }; . "$1"' sh "$BATS_TEST_TMPDIR/forward.sh"
}

# Each release's media: 10.6's disc assembly, 10.9's BaseSystem one.
check_forward() {
    local hook
    hook=$(sed -n 's/^SYSLOG_PORT=\${GUEST_MACOSX_SYSLOG_PORT:-\([0-9]*\)}$/\1/p' "$AUTO")
    [ -n "$hook" ]
    assemble_disc 'install.*\t\t\t\t\t\t@127.0.0.1:32376\n' "$1"
    [ "$status" -eq 0 ]
    run cat "$BATS_TEST_TMPDIR/mnt/private/etc/syslog.conf"
    # The disc's own lines kept, the forward last, its selector whole.
    [ "${lines[0]}" = "$(printf 'install.*\t\t\t\t\t\t@127.0.0.1:32376')" ]
    [ "${lines[${#lines[@]}-1]}" = "$(printf '*.notice;install.*;kern.*\t@127.0.0.1:%s' "$hook")" ]
    # On a line of its own whatever the disc's last line lacks.
    rm -r "$BATS_TEST_TMPDIR/mnt"
    assemble_disc 'kern.*\t/var/log/kernel.log' "$1"
    [ "$status" -eq 0 ]
    run cat "$BATS_TEST_TMPDIR/mnt/private/etc/syslog.conf"
    [ "${lines[0]}" = "$(printf 'kern.*\t/var/log/kernel.log')" ]
    [[ "${lines[${#lines[@]}-1]}" == "*.notice;install.*;kern.*"* ]] || false
}

# Only on media that carries the hook: inside the injectables' block ($2:
# RAW2 for 10.6's disc, RAW3 for 10.9's).
check_forward_only_with_the_hook() {
    local f="$REPO/assets/privops/$1"
    run sed -n "/^if \[ -n \"\\\${GUEST_MACOSX_$2:-}\" \]; then/,/^fi/p" "$f"
    [[ "$output" == *"private/etc/syslog.conf"* ]] || false
    run sed "/^if \[ -n \"\\\${GUEST_MACOSX_$2:-}\" \]; then/,/^fi/d" "$f"
    [[ "$output" != *"syslog.conf"* ]] || false
}

@test "the 10.6 media's syslog.conf forwards install.*, notices and the kernel to the follower's port" {
    check_forward disc/assemble-disc.sh
}

@test "the 10.9 media's syslog.conf forwards install.*, notices and the kernel to the follower's port" {
    check_forward assemble.sh
}

@test "media without the hook gets no forward" {
    check_forward_only_with_the_hook disc/assemble-disc.sh RAW2
    check_forward_only_with_the_hook assemble.sh RAW3
}

# A failed install ends the build (docs/decisions/0028): refuse_logins
# answers a port with an SSH version line and one SSH_MSG_DISCONNECT that
# says why (internal/payload/refuse_test.go holds it to Packer's client).
# Here it answers a free loopback port, never 22, with the host's perl
# named: off the installer environment it starts nothing it is not given.
refuse_env() {
    export GUEST_MACOSX_PERL
    GUEST_MACOSX_PERL=$(command -v perl)
    # below every host's ephemeral range, as syslog_port says why
    export GUEST_MACOSX_REFUSE_PORT=$((20000 + RANDOM % 12768))
    export GUEST_MACOSX_REFUSE_ADDR=127.0.0.1
    export GUEST_MACOSX_REFUSE_PIDFILE="$BATS_TEST_TMPDIR/refuse.pid"
    REFUSE_PIDFILE=$GUEST_MACOSX_REFUSE_PIDFILE
}

# Stops the answer a test started, by its pidfile, if that is a perl.
stop_refusal() {
    local pid
    pid=$(cat "${REFUSE_PIDFILE:-/nonexistent}" 2>/dev/null) || return 0
    case $pid in '' | *[!0-9]*) return 0 ;; esac
    [ "$(cat "/proc/$pid/comm" 2>/dev/null || ps -p "$pid" -o comm= 2>/dev/null)" = perl ] && kill "$pid"
    return 0
}

run_refuse() {
    run env GUEST_MACOSX_AUTOINSTALL_ONLY=refuse_logins \
        GUEST_MACOSX_AUTOINSTALL_LOG="$BATS_TEST_TMPDIR/auto.log" \
        "$AUTO" "${1:-the installer said: Oct 19 07:31:17 localhost OSInstaller[243]: Install failed: x}"
}

# The answer, read as a client would: the version line, then the packet's
# payload, as "<type> <reason> <text>", and whether the packet is framed as
# RFC 4253 section 6 says (its length and the 4 before it a multiple of 8,
# at least 4 bytes of padding) -- which x/crypto does not check, and other
# clients do.
read_answer() {
    # shellcheck disable=SC2016 # Perl's own $s
    perl -e '
        my $sa = $^O eq "darwin" ? pack("CCnC4x8", 16, 2, $ARGV[0], 127, 0, 0, 1)
                                 : pack("SnC4x8", 2, $ARGV[0], 127, 0, 0, 1);
        my $s;
        socket($s, 2, 1, 0) or die "socket: $!\n";
        connect($s, $sa) or die "connect: $!\n";
        my $in = "";
        while (sysread($s, my $buf, 4096)) { $in .= $buf; last if length($in) > 64 && $in =~ /\n/; }
        my ($version, $rest) = split(/\r\n/, $in, 2);
        my ($len, $pad) = unpack("N C", $rest);
        my ($type, $reason, $text) = unpack("C N N/a*", substr($rest, 5, $len - 1 - $pad));
        my $framed = ($len + 4) % 8 == 0 && $pad >= 4 && length($rest) == $len + 4 ? "framed" : "misframed";
        print "$version\n$type $reason $text\n$framed\n";' "$1"
}

@test "a failed install answers SSH with a disconnect that says so, and where its log is" {
    refuse_env
    GUEST_MACOSX_TARGET_VOLUME="Snow Leopard" run_refuse
    [ "$status" -eq 0 ]
    run read_answer "$GUEST_MACOSX_REFUSE_PORT"
    stop_refusal
    [ "${lines[0]}" = "SSH-2.0-guest-macosx-install-failed" ]
    [ "${lines[2]}" = framed ]
    # SSH_MSG_DISCONNECT (1), reason 14; "authenticate", which Packer counts.
    [[ "${lines[1]}" == "1 14 INSTALL FAILED: the installer said: Oct 19 07:31:17 localhost OSInstaller[243]: Install failed: x. Nobody can authenticate here"* ]] || false
    [[ "${lines[1]}" == *"Its log is output-snowleopard-install.log, beside the build's output directory."* ]] || false
    run cat "$BATS_TEST_TMPDIR/auto.log"
    [[ "$output" == *"INSTALL FAILED: the installer said: Oct 19 07:31:17 localhost OSInstaller[243]: Install failed: x"* ]] || false
    [[ "$output" == *"refuse: port $GUEST_MACOSX_REFUSE_PORT answers every connection with a disconnect saying so (pid "* ]] || false
}

# The installer's own line ends in a full stop (MEASURED, 10.6, 2026-10-19):
# the answer reads "assistance. Nobody", not "assistance.. Nobody".
@test "the disconnect does not double the installer's full stop" {
    refuse_env
    run_refuse "the installer said: Oct 19 14:56:45 localhost OSInstaller[242]: Install failed: Contact the software manufacturer for assistance."
    run read_answer "$GUEST_MACOSX_REFUSE_PORT"
    stop_refusal
    [[ "${lines[1]}" == *"for assistance. Nobody can authenticate here"* ]] || false
}

@test "the disconnect names each release's log" {
    refuse_env
    run_refuse
    run read_answer "$GUEST_MACOSX_REFUSE_PORT"
    stop_refusal
    [[ "${lines[1]}" == *"Its log is output-mavericks-install.log"* ]] || false
}

@test "refusing twice starts one answer" {
    refuse_env
    run_refuse
    run_refuse
    stop_refusal
    [ "$status" -eq 0 ]
    [ "$(grep -c "answers every connection" "$BATS_TEST_TMPDIR/auto.log")" -eq 1 ]
    grep -q "refuse: already answering (pid " "$BATS_TEST_TMPDIR/auto.log"
}

@test "an answer that cannot start says why, and the build waits as before" {
    refuse_env
    # The port taken by someone else.
    # shellcheck disable=SC2016 # Perl's own $s
    perl -e 'my $sa = $^O eq "darwin" ? pack("CCnC4x8", 16, 2, $ARGV[0], 127, 0, 0, 1) : pack("SnC4x8", 2, $ARGV[0], 127, 0, 0, 1);
             socket(my $s, 2, 1, 0) or die; bind($s, $sa) or die; listen($s, 1); sleep 15' "$GUEST_MACOSX_REFUSE_PORT" &
    bg=$!
    sleep 1
    run_refuse
    kill "$bg" 2>/dev/null || true
    wait "$bg" || true
    [ "$status" -eq 1 ]
    run cat "$BATS_TEST_TMPDIR/auto.log"
    [[ "$output" == *"refuse: bind 127.0.0.1:$GUEST_MACOSX_REFUSE_PORT: "* ]] || false
    [[ "$output" == *"refuse: no answer on port $GUEST_MACOSX_REFUSE_PORT (its error is above); the build waits out install_timeout"* ]] || false
}

@test "the refusal writes nothing to the target" {
    refuse_env
    mkdir -p "$BATS_TEST_TMPDIR/Volumes/Mavericks"
    GUEST_MACOSX_VOLUMES="$BATS_TEST_TMPDIR/Volumes" run_refuse
    stop_refusal
    [ "$status" -eq 0 ]
    run ls -A "$BATS_TEST_TMPDIR/Volumes/Mavericks"
    [ -z "$output" ]
}

@test "outside the installer environment, on any host, the refusal starts nothing it was not given" {
    # Not /etc/rc.cdrom.local, and the perl or the port unnamed: nothing
    # runs, nothing binds. The same with the host made to look like a Mac
    # (the F21 re-review's simulation): the check is the place, not the OS.
    mkdir -p "$BATS_TEST_TMPDIR/mac"
    cat > "$BATS_TEST_TMPDIR/mac/uname" <<'STUB'
#!/bin/sh
echo Darwin
STUB
    cp "$BATS_TEST_TMPDIR/mac/uname" "$BATS_TEST_TMPDIR/mac/sysctl"
    # A perl that only says it ran, beside the fake Mac's tools.
    cat > "$BATS_TEST_TMPDIR/mac/perl" <<'STUB'
#!/bin/sh
echo "perl ran: $*" >> "$(dirname "$0")/../ran"
STUB
    chmod +x "$BATS_TEST_TMPDIR/mac/"*
    for named in GUEST_MACOSX_PERL="$BATS_TEST_TMPDIR/mac/perl" GUEST_MACOSX_REFUSE_PORT=22; do
        for path in "$PATH" "$BATS_TEST_TMPDIR/mac:$PATH"; do
            run env -u GUEST_MACOSX_PERL -u GUEST_MACOSX_REFUSE_PORT PATH="$path" "$named" \
                GUEST_MACOSX_AUTOINSTALL_ONLY=refuse_logins GUEST_MACOSX_AUTOINSTALL_LOG="$BATS_TEST_TMPDIR/auto.log" \
                GUEST_MACOSX_REFUSE_PIDFILE="$BATS_TEST_TMPDIR/refuse.pid" "$AUTO" x
            [ "$status" -eq 1 ]
        done
    done
    [ ! -e "$BATS_TEST_TMPDIR/ran" ]
    [ ! -e "$BATS_TEST_TMPDIR/refuse.pid" ]
    [ "$(grep -c "refuse: not the installer environment ($AUTO), and GUEST_MACOSX_PERL and GUEST_MACOSX_REFUSE_PORT not both named; nothing started" "$BATS_TEST_TMPDIR/auto.log")" -eq 4 ]
}

# The follower, run alone, with the refusal's perl and port named.
run_watch() {
    run env GUEST_MACOSX_AUTOINSTALL_ONLY=stream_logs GUEST_MACOSX_AUTOINSTALL_LOG="$BATS_TEST_TMPDIR/auto.log" \
        GUEST_MACOSX_STTY=true GUEST_MACOSX_SYSLOG_PORT="$1" "$AUTO"
}

@test "the follower ends the build once when the installer says Install failed, and on nothing else" {
    refuse_env
    : > "$BATS_TEST_TMPDIR/port"
    p=$(syslog_port)
    send() { printf '%s' "$1" > "/dev/udp/127.0.0.1/$p"; sleep 0.3; }
    ( sleep 1.5
      # Lines that are not the installer's own failure: none may end it.
      send '<118>Oct 19 00:47:10 localhost OSInstaller[245]: Volume passed consistency checks.'
      send '<115>Oct 19 00:47:11 localhost OSInstaller[245]: PackageKit: Install Failed: Error Domain=PKInstallErrorDomain'
      send '<5>Oct 19 00:47:12 localhost com.apple.install.cd[59]: guest-macosx-autoinstall: OSInstaller[245]: Install failed: quoted'
      send '<115>Oct 19 00:47:13 localhost OSInstaller[245]: install failed: lower case'
      send '<115>Oct 19 00:47:14 localhost Mac OS X Installer[245]: Install failed: another sender'
      sleep 1.5
      [ ! -s "$REFUSE_PIDFILE" ] || echo "ended before any failure" >> "$BATS_TEST_TMPDIR/early"
      send '<115>Oct  9 00:15:27 localhost OSInstaller[262]: Install failed: The Installer encountered an error'
      send '<115>Oct  9 00:15:28 localhost OSInstaller[262]: Install failed: again'
      # The refusal's shell has long ended: the follower (still running)
      # has reaped it, so no child of the follower is a zombie.
      sleep 2
      for f in /proc/[0-9]*; do
          { tr '\0' '\n' < "$f/environ"; } 2>/dev/null | grep -qx "GUEST_MACOSX_REFUSE_PIDFILE=$REFUSE_PIDFILE" || continue
          [ "$(cat "$f/comm" 2>/dev/null)" = perl ] || continue
          for c in /proc/[0-9]*/stat; do
              # shellcheck disable=SC2046 # the stat's fields, split
              set -- $(sed 's/^[0-9]* ([^)]*) //' "$c" 2>/dev/null)
              [ "${2:-}" = "${f#/proc/}" ] && [ "$1" = Z ] && echo "zombie under ${f#/proc/}" >> "$BATS_TEST_TMPDIR/early"
          done
      done ) &
    bg=$!
    GUEST_MACOSX_SERIAL_PORTS="$BATS_TEST_TMPDIR/port" GUEST_MACOSX_FOLLOW_FOR=9 run_watch "$p"
    wait "$bg"
    stop_refusal
    [ ! -e "$BATS_TEST_TMPDIR/early" ] || { cat "$BATS_TEST_TMPDIR/early"; false; }
    run cat "$BATS_TEST_TMPDIR/auto.log"
    [[ "$output" == *"INSTALL FAILED: the installer said: Oct  9 00:15:27 localhost OSInstaller[262]: Install failed: The Installer encountered an error"* ]] || false
    [ "$(grep -c 'INSTALL FAILED' "$BATS_TEST_TMPDIR/auto.log")" -eq 1 ]
    grep -q "refuse: port $GUEST_MACOSX_REFUSE_PORT answers every connection" "$BATS_TEST_TMPDIR/auto.log"
    # The follower ignores no SIGCHLD to reap: 10.6's perl passes an ignored
    # one through exec, and the refusal could then wait for nothing
    # (MEASURED in a real 10.6 guest).
    run grep -c 'SIG{CHLD}' "$AUTO"
    [ "$output" = 0 ]
}

@test "while it waits for a serial port, the follower already watches for the installer's failure" {
    refuse_env
    p=$(syslog_port)
    ( sleep 2; printf '<115>Oct 19 00:15:27 localhost OSInstaller[262]: Install failed: x' > "/dev/udp/127.0.0.1/$p" ) &
    bg=$!
    GUEST_MACOSX_SERIAL_PORTS="$BATS_TEST_TMPDIR/no-port" GUEST_MACOSX_SERIAL_WAIT=10 GUEST_MACOSX_FOLLOW_FOR=6 \
        run_watch "$p"
    wait "$bg"
    stop_refusal
    grep -q "INSTALL FAILED: the installer said: Oct 19 00:15:27 localhost OSInstaller\[262\]: Install failed: x" "$BATS_TEST_TMPDIR/auto.log"
    grep -q "refuse: port $GUEST_MACOSX_REFUSE_PORT answers" "$BATS_TEST_TMPDIR/auto.log"
}

@test "lines held while the port is awaited reach it when it appears" {
    p=$(syslog_port)
    ( sleep 1; printf '<13>Oct 19 00:00:01 localhost early[1]: before the port' > "/dev/udp/127.0.0.1/$p"
      sleep 1; : > "$BATS_TEST_TMPDIR/late-port" ) &
    bg=$!
    GUEST_MACOSX_SERIAL_PORTS="$BATS_TEST_TMPDIR/late-port" GUEST_MACOSX_SERIAL_WAIT=20 GUEST_MACOSX_FOLLOW_FOR=5 \
        run_watch "$p"
    wait "$bg"
    run cat "$BATS_TEST_TMPDIR/late-port"
    [[ "$output" == *"follower: syslog on 127.0.0.1:$p"* ]] || false
    [[ "$output" == *"syslog: <13>Oct 19 00:00:01 localhost early[1]: before the port"* ]] || false
    grep -q "serial: following .* to $BATS_TEST_TMPDIR/late-port (there after [0-9]*s)" "$BATS_TEST_TMPDIR/auto.log"
}

@test "the hook giving up ends the build before it halts" {
    refuse_env
    make_diskutil "disk1 6686769152 1"
    # It halts for good after; the answer must be up before that, not after.
    run timeout --foreground 20 env GUEST_MACOSX_DISKUTIL="$STUB" GUEST_MACOSX_AUTOINSTALL_LOG="$BATS_TEST_TMPDIR/auto.log" \
        GUEST_MACOSX_TARGET_WAIT=0 GUEST_MACOSX_GIVE_UP_SLEEP=60 \
        GUEST_MACOSX_SERIAL_PORTS="$BATS_TEST_TMPDIR/no-port" GUEST_MACOSX_SERIAL_WAIT=0 GUEST_MACOSX_FOLLOW_FOR=1 \
        GUEST_MACOSX_SYSLOG_PORT="$(syslog_port)" "$AUTO"
    stop_refusal
    [ "$status" -eq 124 ]
    grep -q "INSTALL FAILED: the autoinstall hook gave up: no unpartitioned disk" "$BATS_TEST_TMPDIR/auto.log"
    grep -q "refuse: port $GUEST_MACOSX_REFUSE_PORT answers every connection" "$BATS_TEST_TMPDIR/auto.log"
}
