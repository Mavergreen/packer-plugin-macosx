#!/usr/bin/env bats
# platform: host-agnostic
#
# firstboot.sh's set_ipv6_wait, and the /etc/rc.server it writes
# (docs/decisions/0029).
#
# 10.6's launchctl binds the sockets of the jobs Bonjour advertises (ssh's
# among them) about a second into the boot; the kernel refuses every IPv6
# bind, [::] too, until it has its first IPv6 address, which it adds to lo0
# from a timer of its own; and launchctl, at the first bind that fails,
# drops all of the job's sockets, IPv4 too. When the bind wins the race,
# sshd has no listener for the whole boot (F18; MEASURED 2026-10-19).
# launchctl runs /etc/rc.server before it loads any job, so the first boot
# writes one that waits there for that address, and logs at every boot
# whether it had to.
#
# SAFETY: these run firstboot's function, and the script it writes, on this
# machine with every path in the test's own directory and
# PATH=stubs:/usr/bin:/bin (no sbin): the script's own PATH line and LOG
# line are pointed at the test's before it runs, and its ifconfig and sleep
# are stubs.

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    FB="$REPO/assets/guest/firstboot.sh"
    FNS="$BATS_TEST_TMPDIR/fns.sh"
    : > "$FNS"
    for f in say set_ipv6_wait; do
        fn=$(awk "/^$f\\(\\) \\{/,/^\\}/" "$FB")
        [ -n "$fn" ] || { echo "firstboot.sh has no function $f" >&2; return 1; }
        printf '%s\n' "$fn" >> "$FNS"
    done
    OURS=$(sed -n 's/^RC_SERVER_OURS="\(.*\)"$/\1/p' "$FB")
    [ -n "$OURS" ] || { echo "firstboot.sh sets no RC_SERVER_OURS" >&2; return 1; }
    G="$BATS_TEST_TMPDIR/guest"
    STUB="$BATS_TEST_TMPDIR/stub"
    mkdir -p "$G/etc" "$STUB"
}

write() {
    run env -i PATH="${WRITE_PATH:-/usr/bin:/bin}" G="$G" FNS="$FNS" OURS="$OURS" /bin/sh -c '
        . "$FNS"
        LOG=$G/guest-macosx-firstboot.log
        RC_SERVER=$G/etc/rc.server
        RC_SERVER_OURS=$OURS
        set_ipv6_wait'
}

# rc_server <v6-after> [initial-cs]: the rc.server set_ipv6_wait writes,
# made runnable here: its PATH is the stubs' then the host's (no sbin), its
# log the test's. A fake clock, in centiseconds, in $STUB/clock (starting at
# [initial-cs], default 0): `date +%s` reads it (whole seconds, as date's
# own), `sleep` advances it by its argument, and `ifconfig` by
# $STUB_IFCONFIG_CS (default 1) a call. ifconfig prints no inet6 line for its
# first <v6-after> calls (all of them when that is "never"), an inet6 line
# after; "never" still ends at call 5000, so a wait that lost its bound ends
# and fails its test rather than spin.
rc_server() {
    write
    [ "$status" -eq 0 ]
    sed -e "s|^PATH=.*|PATH=$STUB:/usr/bin:/bin|" \
        -e "s|^LOG=.*|LOG=$G/guest-macosx-rc.server.log|" \
        "$G/etc/rc.server" > "$BATS_TEST_TMPDIR/rc.server"
    grep -q "^PATH=$STUB:" "$BATS_TEST_TMPDIR/rc.server"
    grep -q "^LOG=$G/" "$BATS_TEST_TMPDIR/rc.server"
    echo "${2:-0}" > "$STUB/clock"
    rm -f "$STUB/ifconfig.calls" "$STUB/sleep.args"
    cat > "$STUB/ifconfig" <<'EOF'
#!/bin/sh
n=$(cat "$STUB_DIR/ifconfig.calls" 2>/dev/null || echo 0)
n=$((n + 1))
echo "$n" > "$STUB_DIR/ifconfig.calls"
echo $(( $(cat "$STUB_DIR/clock") + ${STUB_IFCONFIG_CS:-1} )) > "$STUB_DIR/clock"
echo "lo0: flags=8049<UP,LOOPBACK,RUNNING,MULTICAST> mtu 16384"
echo "	inet 127.0.0.1 netmask 0xff000000 "
if { [ "$STUB_V6_AFTER" != never ] && [ "$n" -gt "$STUB_V6_AFTER" ]; } || [ "$n" -ge 5000 ]; then
    echo "	inet6 ::1 prefixlen 128 "
fi
EOF
    cat > "$STUB/sleep" <<'EOF'
#!/bin/sh
echo "$*" >> "$STUB_DIR/sleep.args"
echo $(( $(cat "$STUB_DIR/clock") + $(echo "$1" | awk '{ printf "%d", $1 * 100 }') )) > "$STUB_DIR/clock"
EOF
    cat > "$STUB/date" <<'EOF'
#!/bin/sh
if [ "$*" = +%s ] && [ -n "${STUB_NO_CLOCK:-}" ]; then
    echo "date: illegal time format" >&2
    exit 1
elif [ "$*" = +%s ]; then
    echo $(( 1760000000 + $(cat "$STUB_DIR/clock") / 100 ))
else
    echo 2026-10-19T00:00:00Z
fi
EOF
    chmod +x "$STUB/ifconfig" "$STUB/sleep" "$STUB/date"
    run env -i STUB_DIR="$STUB" STUB_V6_AFTER="$1" STUB_IFCONFIG_CS="${STUB_IFCONFIG_CS:-1}" STUB_NO_CLOCK="${STUB_NO_CLOCK:-}" \
        /bin/sh "$BATS_TEST_TMPDIR/rc.server"
}

# A stub cat for set_ipv6_wait's write, in $STUB/cat-<kind>/cat.
stub_cat() {
    mkdir -p "$STUB/cat-$1"
    case $1 in
    fails) cat > "$STUB/cat-fails/cat" <<'EOF'
#!/bin/sh
# Writes the first three lines, then fails, as a full disk would.
head -n 3
exit 1
EOF
    ;;
    short) cat > "$STUB/cat-short/cat" <<'EOF'
#!/bin/sh
# Writes all but the last line and says it succeeded.
sed '$d'
EOF
    ;;
    esac
    chmod +x "$STUB/cat-$1/cat"
}

@test "10.6: the rc.server is written, executable, ours on its second line, and the log says why" {
    write
    [ "$status" -eq 0 ]
    [ -x "$G/etc/rc.server" ]
    [ "$(sed -n 1p "$G/etc/rc.server")" = "#!/bin/sh" ]
    [ "$(sed -n 2p "$G/etc/rc.server")" = "$OURS" ]
    [ ! -e "$G/etc/rc.server.tmp" ]
    grep -q "rc.server: new $G/etc/rc.server: at every boot launchctl waits there for the kernel's first IPv6 address before it binds sockets, so ssh's cannot lose its listener (F18)" "$G/guest-macosx-firstboot.log"
}

@test "the marker firstboot.sh looks for is the line the script it writes carries" {
    [ "$OURS" = "# guest-macosx: wait for IPv6 before launchctl binds sockets (docs/decisions/0029)" ]
    grep -qxF "$OURS" <(awk '/^set_ipv6_wait\(\) \{/,/^\}/' "$FB")
}

@test "run twice: ours is replaced by ours, and the log says so" {
    write
    write
    [ "$status" -eq 0 ]
    [ "$(sed -n 2p "$G/etc/rc.server")" = "$OURS" ]
    grep -q "rc.server: replaced $G/etc/rc.server: at every boot" "$G/guest-macosx-firstboot.log"
}

@test "inert: an rc.server that is not ours (Mac OS X Server's) is left as it was, and the log says so" {
    cat > "$G/etc/rc.server" <<'EOF'
#!/bin/sh
sysctl -w kern.maxvnodes=150000
EOF
    before=$(cat "$G/etc/rc.server")
    write
    [ "$status" -eq 0 ]
    [ "$(cat "$G/etc/rc.server")" = "$before" ]
    grep -q "rc.server: $G/etc/rc.server is there and not ours; left alone, so launchctl can still bind sockets before IPv6 is up and lose ssh's listener for a boot (F18)" "$G/guest-macosx-firstboot.log"
}

@test "an rc.server that cannot be written is logged as a failure, nothing half-written is left, and the run goes on" {
    [ "$(id -u)" -ne 0 ] || skip "root can write it anyway"
    chmod 555 "$G/etc"
    write
    chmod 755 "$G/etc"
    [ "$status" -eq 0 ]
    [ ! -e "$G/etc/rc.server" ]
    [ ! -e "$G/etc/rc.server.tmp" ]
    grep -q "rc.server: COULD NOT write $G/etc/rc.server (the write failed); launchctl can bind sockets before IPv6 is up and lose ssh's listener for a boot (F18)" "$G/guest-macosx-firstboot.log"
}

@test "inert at boot: an IPv6 address already there, no wait, and the log says so" {
    rc_server 0
    [ "$status" -eq 0 ]
    [ ! -e "$STUB/sleep.args" ]
    [ "$(cat "$STUB/ifconfig.calls")" = 1 ]
    grep -q "rc.server: IPv6 address already there; no wait$" "$G/guest-macosx-rc.server.log"
}

@test "at boot: no IPv6 address yet, it waits 50 ms at a time until there is one, and logs how long" {
    rc_server 3
    [ "$status" -eq 0 ]
    [ "$(cat "$STUB/sleep.args" | tr '\n' ' ')" = "0.05 0.05 0.05 " ]
    grep -q "rc.server: waited 3 checks 50 ms apart for the first IPv6 address$" "$G/guest-macosx-rc.server.log"
}

@test "at boot: no IPv6 address by 10 s on the clock, it gives up, says so, and the boot goes on" {
    rc_server never
    [ "$status" -eq 0 ]
    # Gone once the clock's whole seconds say 10 have passed, and no later
    # than the one check then running (an ifconfig and a sleep, 6 cs here).
    end=$(cat "$STUB/clock")
    [ "$end" -ge 1000 ] && [ "$end" -le 1006 ] || { echo "ended at ${end} cs"; false; }
    n=$(wc -l < "$STUB/sleep.args")
    grep -q "rc.server: still no IPv6 address after $n checks in 10 s; going on, and launchctl's IPv6 binds can fail this boot$" "$G/guest-macosx-rc.server.log"
}

@test "at boot: the bound is time, not a count of checks: with a slow ifconfig it still gives up at 10 s" {
    # Each ifconfig takes a second: 200 checks would hold the boot 200 s.
    STUB_IFCONFIG_CS=100 rc_server never
    [ "$status" -eq 0 ]
    end=$(cat "$STUB/clock")
    [ "$end" -ge 1000 ] && [ "$end" -le 1105 ] || { echo "ended at ${end} cs"; false; }
    [ "$(cat "$STUB/ifconfig.calls")" -le 11 ]
    grep -q "rc.server: still no IPv6 address after [0-9]* checks in 10 s; going on" "$G/guest-macosx-rc.server.log"
}

@test "at boot: whole seconds on the clock: started late in a second, it waits more than 9 s, at most 10 s and a check" {
    rc_server never 99
    [ "$status" -eq 0 ]
    end=$(cat "$STUB/clock")
    [ $((end - 99)) -gt 900 ] && [ $((end - 99)) -le 1006 ] || { echo "waited $((end - 99)) cs"; false; }
}

@test "at boot: a clock it cannot read ends the wait at once, and the log says so" {
    STUB_NO_CLOCK=1 rc_server never
    [ "$status" -eq 0 ]
    [ ! -e "$STUB/ifconfig.calls" ]
    [ ! -e "$STUB/sleep.args" ]
    grep -q "rc.server: cannot read the clock (date +%s); not waiting, and launchctl's IPv6 binds can fail this boot$" "$G/guest-macosx-rc.server.log"
}

@test "at boot: one line per boot, appended" {
    rc_server 0
    rm -f "$STUB/ifconfig.calls"
    run env -i STUB_DIR="$STUB" STUB_V6_AFTER=2 /bin/sh "$BATS_TEST_TMPDIR/rc.server"
    [ "$status" -eq 0 ]
    [ "$(wc -l < "$G/guest-macosx-rc.server.log")" -eq 2 ]
}

@test "at boot: the log keeps its last 100 lines, so it never grows past that" {
    for i in $(seq 1 150); do echo "old line $i"; done > "$G/guest-macosx-rc.server.log"
    rc_server 0
    [ "$status" -eq 0 ]
    [ "$(wc -l < "$G/guest-macosx-rc.server.log")" -eq 100 ]
    [ "$(sed -n 1p "$G/guest-macosx-rc.server.log")" = "old line 52" ]
    tail -n 1 "$G/guest-macosx-rc.server.log" | grep -q "rc.server: IPv6 address already there; no wait$"
    [ ! -e "$G/guest-macosx-rc.server.log.tmp" ]
}

@test "a write that fails partway installs nothing, keeps no temp file, and is logged" {
    stub_cat fails
    WRITE_PATH="$STUB/cat-fails:/usr/bin:/bin" write
    [ "$status" -eq 0 ]
    [ ! -e "$G/etc/rc.server" ]
    [ ! -e "$G/etc/rc.server.tmp" ]
    grep -q "rc.server: COULD NOT write $G/etc/rc.server (the write failed); launchctl can bind sockets before IPv6 is up and lose ssh's listener for a boot (F18)" "$G/guest-macosx-firstboot.log"
}

@test "a write that says it succeeded but came out short installs nothing, and is logged" {
    stub_cat short
    WRITE_PATH="$STUB/cat-short:/usr/bin:/bin" write
    [ "$status" -eq 0 ]
    [ ! -e "$G/etc/rc.server" ]
    [ ! -e "$G/etc/rc.server.tmp" ]
    grep -q "rc.server: COULD NOT write $G/etc/rc.server (it came out incomplete); launchctl can bind sockets" "$G/guest-macosx-firstboot.log"
}

@test "a failed write over ours keeps ours as it was" {
    write
    before=$(cat "$G/etc/rc.server")
    stub_cat fails
    WRITE_PATH="$STUB/cat-fails:/usr/bin:/bin" write
    [ "$status" -eq 0 ]
    [ "$(cat "$G/etc/rc.server")" = "$before" ]
    grep -q "rc.server: COULD NOT write $G/etc/rc.server (the write failed)" "$G/guest-macosx-firstboot.log"
}
