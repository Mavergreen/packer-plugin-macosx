#!/usr/bin/env bats
# platform: host-agnostic -- tested on Linux with KVM only
#
# bin/gate-varstore.sh boots a box's firmware alone on a nearly full variable store and counts
# the flash bytes it programs (docs/decisions/0032). Here QEMU is a stand-in: it records argv,
# "daemonizes" by writing the pidfile with the test shell pid, writes canned trace
# lines and OVMF's "BdsDxe:" serial line; ps reports QEMU for that pid only; kill (as `env kill`)
# only records. python3 is the host's: the fill is the code under test. PATH is the stub
# directory, /usr/bin and /bin (no sbin); shutdown reboot halt poweroff systemctl sudo refuse.

bats_require_minimum_version 1.5.0

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    SCRIPT="$REPO/bin/gate-varstore.sh"
    STUBS="$BATS_TEST_TMPDIR/stubs"
    BOX="$BATS_TEST_TMPDIR/box"
    STATE="$BATS_TEST_TMPDIR/state"
    export STUB_LOG="$BATS_TEST_TMPDIR/stub.log"
    export STUB_QEMU_ARGV="$BATS_TEST_TMPDIR/qemu.argv"
    mkdir -p "$STUBS" "$BOX"
    : > "$STUB_LOG"
    : > "$BOX/OVMF_CODE.fd"
    # a firmware volume header of 0x48 bytes, then an authenticated variable store of 64 KiB
    # holding one live variable, its free space erased
    python3 -I - "$BOX/OVMF_VARS.fd" <<'PY'
import struct, sys
d = bytearray(b"\0" * 0x48)
struct.pack_into("<H", d, 0x30, 0x48)
size = 0x10000
store = bytearray(b"\xff" * size)
store[0:16] = bytes.fromhex("782cf3aa7b949a43a1802e144ec37792")
struct.pack_into("<IBB", store, 16, size, 0x5A, 0xFE)
store[22:28] = b"\0" * 6
name = "Lang\0".encode("utf-16-le"); data = b"eng\0"
h = struct.pack("<HBBIQ16sIII16s", 0x55AA, 0x3F, 0, 7, 0, b"\0" * 16, 0, len(name), len(data), b"\x11" * 16)
rec = h + name + data
store[28:28 + len(rec)] = rec
open(sys.argv[1], "wb").write(bytes(d + store))
PY
    for c in shutdown reboot halt poweroff systemctl sudo; do
        cat > "$STUBS/$c" <<'STUB'
#!/bin/sh
echo "stub: ${0##*/} refused"
exit 1
STUB
    done
    # the stand-in QEMU. Like OVMF, it tells a full store by its contents, not its name: a store
    # still holding the guard's GateFill records is full, and booting it reclaims it (the file
    # becomes the box's own store again), so a second boot of the same copy is a pristine one.
    #   STUB_NO_EVENT=1     `-trace help` does not list pflash_data_write
    #   STUB_LONG_HELP=1    enough events after the match to exceed a pipe buffer
    #   STUB_SILENT=1       the trace file stays empty
    #   STUB_FAIL=1         QEMU does not start
    #   STUB_DIES=1         QEMU starts, and its pid is gone at once
    #   STUB_NO_RECLAIM=1   a full store is left as it was
    #   STUB_SLOW_ONCE=<s>  the first boot of a full store takes this long (a burst of host load)
    #   STUB_BYTES_FULL / STUB_BYTES_PRISTINE   trace lines written on each kind of store (8355 / 6000)
    #   STUB_BDS_FULL / STUB_BDS_PRISTINE       seconds to the "BdsDxe:" line (0 / 0; "never")
    cat > "$STUBS/qemu-system-x86_64" <<'STUB'
#!/bin/sh
if [ "$1 $2" = "-trace help" ]; then
    echo pflash_io_write
    [ -n "${STUB_NO_EVENT:-}" ] || echo pflash_data_write
    if [ -n "${STUB_LONG_HELP:-}" ]; then
        # A reader that exits at the match breaks the producer's pipe. Keep
        # the failure: pipefail must not mistake it for an absent event.
        "$STUB_PYTHON3" -I -c 'import sys; sys.stdout.write("other_event\n" * 1048576)' || exit $?
    fi
    echo pflash_data_write_block
    exit 0
fi
[ -z "${STUB_FAIL:-}" ] || { echo "qemu: could not initialize kvm" >&2; exit 1; }
rm -f "$STUB_LOG.dead" "$STUB_LOG.pending" "$STUB_LOG.zombie"
printf 'qemu-system-x86_64 %s\n' "$*" > "$STUB_LOG.args"
printf '%s\n' "$@" >> "$STUB_QEMU_ARGV"
echo "--" >> "$STUB_QEMU_ARGV"
pid=""; ser=""; tr=""; vars=""; prev=""
for a in "$@"; do
    case $prev in
        -pidfile) pid=$a ;;
        -serial) ser=${a#file:} ;;
        -D) tr=$a ;;
    esac
    case $a in *unit=1,file=*) vars=${a#*unit=1,file=} ;; esac
    prev=$a
done
if "$STUB_PYTHON3" -I -c 'import sys; sys.exit(0 if "GateFill".encode("utf-16-le") in open(sys.argv[1], "rb").read() else 1)' "$vars"; then
    n=${STUB_BYTES_FULL:-8355}; delay=${STUB_BDS_FULL:-0}
    if [ -n "${STUB_SLOW_ONCE:-}" ] && [ ! -e "$STUB_LOG.slowed" ]; then delay=$STUB_SLOW_ONCE; : > "$STUB_LOG.slowed"; fi
    echo "stub qemu: full store $vars" >> "$STUB_LOG"
    [ -n "${STUB_NO_RECLAIM:-}" ] || cp "$STUB_RECLAIMED" "$vars"
else
    n=${STUB_BYTES_PRISTINE:-6000}; delay=${STUB_BDS_PRISTINE:-0}
    echo "stub qemu: pristine store $vars" >> "$STUB_LOG"
fi
if [ -n "$tr" ] && [ -z "${STUB_SILENT:-}" ]; then
    # yes | head, not a shell loop: 799963 lines took 16-22 s under load, inside the boot's own timeout
    if [ "$n" -gt 0 ]; then yes "1@1.0:pflash_data_write system.flash1: data offset:0x0 size:1 value:0x00ff" | head -n "$n"; fi > "$tr"
fi
if [ -n "${STUB_DIES:-}" ]; then echo "$STUB_DEAD_PID" > "$pid"; exit 0; fi
echo "$STUB_QEMU_PID" > "$pid"
if [ "$delay" != never ]; then
    ( sleep "$delay"; printf 'BdsDxe: loading Boot0000 "EFI Internal Shell"\r\n' >> "$ser" ) &
fi
exit 0
STUB
    # Full argv comes from the fake launch, never from the host's process table.
    cat > "$STUBS/ps" <<'STUB'
#!/bin/sh
[ "$1" != -ww ] || shift
pid=$2
[ "$pid" != "$STUB_DEAD_PID" ] || exit 1
[ ! -e "$STUB_LOG.dead" ] || exit 1
case " ${STUB_PS_QEMU-$STUB_QEMU_PID} " in *" $pid "*) ;; *) echo sleep; exit 0 ;; esac
[ "$4" != pid= ] || { echo "$pid"; exit 0; }
if [ "$4" = stat= ]; then
    [ "${STUB_STAT:-S}" != unknown ] || exit 1
    if [ -e "$STUB_LOG.zombie" ]; then echo 'Z+'; else echo "${STUB_STAT:-S}"; fi
    exit 0
fi
[ "$4" != comm= ] || { echo qemu-system-x86_64; exit 0; }
[ "${STUB_IDENTITY:-}" != unknown ] || exit 1
[ "${STUB_ARGS_UNSUPPORTED:-}" != 1 ] || [ "$4" != args= ] || exit 1
if [ "${STUB_IDENTITY:-}" = unrelated ] || [ -e "$STUB_LOG.reused" ]; then
    echo 'qemu-system-x86_64 -uuid unrelated -pidfile /other/qemu.pid'
elif [ "${STUB_IDENTITY:-}" = truncated ]; then
    echo 'qemu-system-x86_64 -uuid'
else
    cat "$STUB_LOG.args"
fi
STUB
    cat > "$STUBS/kill" <<'STUB'
#!/bin/sh
if [ "$1" = -0 ]; then
    [ "$2" != "$STUB_DEAD_PID" ] && [ ! -e "$STUB_LOG.dead" ]
    exit $?
fi
printf "kill %s\n" "$*" >> "$STUB_LOG"
if [ "$1" = -KILL ]; then
    case ${STUB_KILL_EXIT:-immediate} in
        immediate) : > "$STUB_LOG.dead" ;;
        delayed|zombie) : > "$STUB_LOG.pending" ;;
        never) ;;
    esac
elif [ "${STUB_TERM_EXITS:-}" = 1 ]; then
    : > "$STUB_LOG.dead"
fi
[ "${STUB_REUSE:-}" != 1 ] || : > "$STUB_LOG.reused"
exit 0
STUB
    chmod +x "$STUBS"/*
    export PATH="$STUBS:/usr/bin:/bin"
    export GATE_KILL_WAIT=1 GATE_VARSTORE_FREE=2000
    unset GATE_ACCEL GATE_VARSTORE_LIMIT GATE_VARSTORE_TIMEOUT GATE_VARSTORE_SLOWER
    export STUB_PYTHON3; STUB_PYTHON3=$(command -v python3)
    export STUB_RECLAIMED="$BATS_TEST_TMPDIR/reclaimed.fd"
    cp "$BOX/OVMF_VARS.fd" "$STUB_RECLAIMED"
    # The stand-in uses the existing test shell pid; no helper needs a teardown signal.
    OWNED=$$ # existing test shell: only presence probes, never signaled
    export STUB_QEMU_PID=$OWNED
    # a pid that was live and is gone
    sleep 0 & DEAD=$!
    wait "$DEAD" || true
    export STUB_DEAD_PID=$DEAD
}


# argv_of <n>: the n-th QEMU's arguments, one per line
# secs_after <text>: the whole seconds printed right after <text> in $output
secs_after() {
    local rest="${output#*"$1"}"
    [ "$rest" != "$output" ] || { echo "no \"$1\" in the output" >&2; return 1; }
    printf '%s\n' "${rest%%s*}"
}

argv_of() { awk -v n="$1" '$0 == "--" { k++; next } k == n - 1' "$STUB_QEMU_ARGV"; }

@test "safety: the dangerous names refuse; QEMU and kill are stand-ins" {
    for c in shutdown reboot halt poweroff systemctl sudo; do
        run "$STUBS/$c" now
        [ "$status" -eq 1 ]
        [ "$(command -v "$c")" = "$STUBS/$c" ]
    done
    [ "$(command -v qemu-system-x86_64)" = "$STUBS/qemu-system-x86_64" ]
    [[ "$PATH" != *sbin* ]]
}

@test "trace: a reclaim of a few thousand bytes passes, and says which method it used" {
    run "$SCRIPT" "$BOX" "$STATE"
    [ "$status" -eq 0 ] || { echo "status $status: $output"; false; }
    [[ "$output" == *"method: trace"* ]]
    [[ "$output" == *"pass: 8355 flash bytes programmed before \"BdsDxe:\""*"(limit 100000)"* ]]
    # one boot, on the filled copy, never the box's own store
    [ "$(grep -c '^--$' "$STUB_QEMU_ARGV")" -eq 1 ]
    argv_of 1 | grep -qxF "if=pflash,format=raw,unit=1,file=$STATE/vars-full.fd"
    argv_of 1 | grep -qxF "if=pflash,format=raw,unit=0,readonly=on,file=$BOX/OVMF_CODE.fd"
    argv_of 1 | grep -qxF "enable=pflash_data_write"
    # the firmware alone: no disk, no network, no devices of QEMU's choosing
    argv_of 1 | grep -qxF -- -nodefaults
    argv_of 1 | grep -qxF -- -net
    # and no disk or device of any shape: no -device at all (not OpenCore's
    # virtio-blk disk, nor the EHCI and its input: ADR 0035), none of the
    # flags that make a disk without one, and every -drive a flash chip
    ! argv_of 1 | grep -qxE -- '-device|-usb|-usbdevice|-hd[a-d]|-cdrom|-fd[ab]|-sd|-mtdblock|-pflash' || false
    [ "$(argv_of 1 | awk 'd { print; d = 0; next } $0 == "-drive" { d = 1 }' | grep -vc '^if=pflash,')" -eq 0 ]
    argv_of 1 | grep -qxF kvm
    # stopped by its pidfile
    grep -qxF "kill -TERM $OWNED" "$STUB_LOG"
    [ ! -e "$STATE/qemu.pid" ]
}

@test "trace: more than the limit programmed is a fail naming the count" {
    STUB_BYTES_FULL=799963 run "$SCRIPT" "$BOX" "$STATE"
    [ "$status" -eq 1 ] || { echo "status $status: $output"; false; }
    [[ "$output" == *"warning: FAIL: 799963 flash bytes programmed before \"BdsDxe:\" on a nearly full variable store, more than 100000"* ]]
}

@test "trace: the limit is GATE_VARSTORE_LIMIT" {
    GATE_VARSTORE_LIMIT=8000 run "$SCRIPT" "$BOX" "$STATE"
    [ "$status" -eq 1 ] || { echo "status $status: $output"; false; }
    [[ "$output" == *"8355 flash bytes"*"more than 8000"* ]]
}

@test "trace: help output after the matching event cannot select timing instead" {
    STUB_LONG_HELP=1 STUB_BYTES_FULL=799963 run "$SCRIPT" "$BOX" "$STATE"
    [ "$status" -eq 1 ] || { echo "status $status: $output"; false; }
    [[ "$output" == *"method: trace"* ]]
    [[ "$output" == *"FAIL: 799963 flash bytes"*"more than 100000"* ]]
    [ "$(grep -c '^--$' "$STUB_QEMU_ARGV")" -eq 1 ]
}

@test "trace: no BdsDxe line in time is a fail, with the count so far" {
    STUB_BDS_FULL=never GATE_VARSTORE_TIMEOUT=2 run "$SCRIPT" "$BOX" "$STATE"
    [ "$status" -eq 1 ] || { echo "status $status: $output"; false; }
    [[ "$output" == *"FAIL: no \"BdsDxe:\" line within 2s on a nearly full variable store (8355 flash bytes programmed by then (limit 100000))"* ]]
    grep -qxF "kill -TERM $OWNED" "$STUB_LOG"
}

@test "timing: a QEMU without the trace event boots the pristine store and a full one, each twice, alternately" {
    STUB_NO_EVENT=1 run "$SCRIPT" "$BOX" "$STATE"
    [ "$status" -eq 0 ] || { echo "status $status: $output"; false; }
    [[ "$output" == *"method: timing"* ]]
    re='pass: "BdsDxe:" after [0-9]+s at best on a nearly full variable store, [0-9]+s at best on the box.s own \(at most 10s slower\)'
    [[ "$output" =~ $re ]] || { echo "$output"; false; }
    [ "$(grep -c '^--$' "$STUB_QEMU_ARGV")" -eq 4 ]
    argv_of 1 | grep -qxF "if=pflash,format=raw,unit=1,file=$STATE/vars-pristine.fd"
    argv_of 2 | grep -qxF "if=pflash,format=raw,unit=1,file=$STATE/vars-full.fd"
    argv_of 3 | grep -qxF "if=pflash,format=raw,unit=1,file=$STATE/vars-pristine.fd"
    argv_of 4 | grep -qxF "if=pflash,format=raw,unit=1,file=$STATE/vars-full.fd"
    # both full boots booted a full store: the second copy was filled afresh
    [ "$(grep -c '^stub qemu: full store' "$STUB_LOG")" -eq 2 ]
    ! grep -qxF "enable=pflash_data_write" "$STUB_QEMU_ARGV" || false
    cmp "$BOX/OVMF_VARS.fd" "$STATE/vars-pristine.fd"
}

@test "timing: the full store much slower than the pristine one is a fail" {
    # 8 s against a 2 s allowance: each boot's time also holds the stub's own start, which took
    # seconds under load, so a 4 s full boot could come within 2 s of the pristine one
    STUB_NO_EVENT=1 STUB_BDS_FULL=8 GATE_VARSTORE_SLOWER=2 run "$SCRIPT" "$BOX" "$STATE"
    [ "$status" -eq 1 ] || { echo "status $status: $output"; false; }
    re='FAIL: "BdsDxe:" after [0-9]+s at best on a nearly full variable store against [0-9]+s at best on the box.s own, more than 2s slower'
    [[ "$output" =~ $re ]] || { echo "$output"; false; }
}

@test "timing: a trace event that records nothing falls back to timing, and says so" {
    STUB_SILENT=1 run "$SCRIPT" "$BOX" "$STATE"
    [ "$status" -eq 0 ] || { echo "status $status: $output"; false; }
    [[ "$output" == *"names the pflash_data_write trace event but recorded none"*"timing instead"* ]]
    [[ "$output" == *"method: timing"* ]]
    [ "$(grep -c '^--$' "$STUB_QEMU_ARGV")" -eq 5 ]
}

@test "timing after a silent trace boots a freshly filled store: the trace boot reclaimed the first" {
    # the full store is slow to boot, as main's reclaim is; the trace boot has reclaimed the
    # first copy, so a timing pass on that copy would boot a compacted store and pass main
    STUB_SILENT=1 STUB_BDS_FULL=8 GATE_VARSTORE_SLOWER=2 run "$SCRIPT" "$BOX" "$STATE"
    [ "$status" -eq 1 ] || { echo "status $status: $output"; false; }
    [[ "$output" == *"timing instead"* ]]
    [[ "$output" == *"FAIL: \"BdsDxe:\""*"more than 2s slower"* ]]
    # every boot meant as a full one booted a full store
    [ "$(grep -c '^stub qemu: full store' "$STUB_LOG")" -eq "$(grep -c "unit=1,file=$STATE/vars-full.fd\$" "$STUB_QEMU_ARGV")" ]
}

@test "timing: no BdsDxe line on the full store is a fail, naming the pristine time" {
    # 6 s, not 2: the pristine boots come first and must make it, the stub's start and all
    STUB_NO_EVENT=1 STUB_BDS_FULL=never GATE_VARSTORE_TIMEOUT=6 run "$SCRIPT" "$BOX" "$STATE"
    [ "$status" -eq 1 ] || { echo "status $status: $output"; false; }
    re='FAIL: no "BdsDxe:" line within 6s on a nearly full variable store \([0-9]+s on the box.s own\)'
    [[ "$output" =~ $re ]] || { echo "$output"; false; }
}

@test "timing: no BdsDxe line on the box's own store is a fail: the firmware does not boot" {
    STUB_NO_EVENT=1 STUB_BDS_PRISTINE=never GATE_VARSTORE_TIMEOUT=2 run "$SCRIPT" "$BOX" "$STATE"
    [ "$status" -eq 1 ] || { echo "status $status: $output"; false; }
    [[ "$output" == *"on the box's own store: the firmware does not boot"* ]]
}

@test "every boot's QEMU is stopped before the next starts: TERM, then KILL when it stays" {
    STUB_NO_EVENT=1 run "$SCRIPT" "$BOX" "$STATE"
    [ "$status" -eq 0 ] || { echo "status $status: $output"; false; }
    boots=$(grep -c '^--$' "$STUB_QEMU_ARGV")
    [ "$(grep -cxF "kill -TERM $OWNED" "$STUB_LOG")" -eq "$boots" ]
    [ "$(grep -cxF "kill -KILL $OWNED" "$STUB_LOG")" -eq "$boots" ]
    # in order: each boot's TERM and KILL before the next boot's QEMU
    awk -v o="$OWNED" '/^stub qemu:/ { if (started && !stopped) exit 1; started = 1; stopped = 0 }
        $0 == "kill -KILL " o { stopped = 1 } END { exit !stopped }' "$STUB_LOG"
    [ ! -e "$STATE/qemu.pid" ]
}

@test "timing compares the faster boot of each store: one slow boot is not a fail" {
    # a 10 s burst against a 5 s allowance: boot 1 alone would fail, boot 2 must not, stub's start and all
    STUB_NO_EVENT=1 STUB_SLOW_ONCE=10 GATE_VARSTORE_SLOWER=5 run "$SCRIPT" "$BOX" "$STATE"
    [ "$status" -eq 0 ] || { echo "status $status: $output"; false; }
    # the script times in whole seconds, so compare the boots with each other, never with 0s
    one="$(secs_after 'the nearly full store, boot 1: "BdsDxe:" after ')"
    two="$(secs_after 'the nearly full store, boot 2: "BdsDxe:" after ')"
    best="$(secs_after 'pass: "BdsDxe:" after ')"
    [ "$two" -lt "$one" ] || { echo "boot 1 ${one}s, boot 2 ${two}s"; false; }
    [ "$best" -eq "$two" ] || { echo "best ${best}s, boot 2 ${two}s"; false; }
}

@test "trace: a silent trace on a firmware that never reaches BDS says the trace recorded none" {
    STUB_SILENT=1 STUB_BDS_FULL=never GATE_VARSTORE_TIMEOUT=2 run "$SCRIPT" "$BOX" "$STATE"
    [ "$status" -eq 1 ] || { echo "status $status: $output"; false; }
    [[ "$output" == *"FAIL: no \"BdsDxe:\" line within 2s on a nearly full variable store (the trace recorded none)"* ]]
    [[ "$output" != *"0 flash bytes"* ]]
}

# Exercise the registered handlers directly without delivering host signals.
# Source only initialization/cleanup definitions; the synthetic boot is already
# owned, and EXIT runs naturally when the test shell exits.
run_cleanup_handler() {
    run bash -c '
        source <(sed "/^fill() {/,$ d; /^GUEST_MACOSX_REPO_ROOT=/c\\GUEST_MACOSX_REPO_ROOT=$5" "$1") "$2" "$3"
        uuid=handler-test
        echo "$STUB_QEMU_PID" > "$state/qemu.pid"
        printf "qemu-system-x86_64 -uuid %s -pidfile %s/qemu.pid\n" "$uuid" "$state" > "$STUB_LOG.args"
        if [ "$4" = TERM ]; then
            handler=$(trap -p TERM)
            eval "${handler/trap -- /set -- }"
            eval "$1"
        else
            # HUP has no dedicated handler; only EXIT cleanup is registered.
            [ -z "$(trap -p HUP)" ] || exit 1
            exit 129
        fi
    ' bash "$SCRIPT" "$BOX" "$STATE" "$1" "$REPO"
}

@test "TERM: registered handler says so, stops its QEMU and exits 3 (no signal delivery)" {
    run_cleanup_handler TERM
    [ "$status" -eq 3 ]
    [[ "$output" == *"interrupted: stopping this run's QEMU"* ]]
    grep -qxF "kill -TERM $OWNED" "$STUB_LOG"
    [ ! -e "$STATE/qemu.pid" ]
}

@test "HUP: EXIT cleanup stops QEMU without a dedicated HUP handler (no signal delivery)" {
    run_cleanup_handler HUP
    [ "$status" -eq 129 ]
    grep -qxF "kill -TERM $OWNED" "$STUB_LOG"
    [ ! -e "$STATE/qemu.pid" ]
}

@test "QEMU gone before the BdsDxe line is could not run, not a fail" {
    STUB_DIES=1 run "$SCRIPT" "$BOX" "$STATE"
    [ "$status" -eq 3 ] || { echo "status $status: $output"; false; }
    [[ "$output" == *"QEMU exited before OVMF's \"BdsDxe:\" line"* ]]
}

@test "the fill: the live variables kept, all but GATE_VARSTORE_FREE bytes taken by deleted records" {
    STUB_NO_RECLAIM=1 run "$SCRIPT" "$BOX" "$STATE"
    [ "$status" -eq 0 ] || { echo "status $status: $output"; false; }
    [[ "$output" == *"the store: 2000 of 65508 bytes free"* ]]
    python3 -I - "$STATE/vars-full.fd" <<'PY'
import struct, sys
d = open(sys.argv[1], "rb").read()
p = 0x48 + 28; end = 0x48 + 0x10000; live = dead = 0
while p + 60 <= end and struct.unpack_from("<H", d, p)[0] == 0x55AA:
    st = d[p + 2]; ns, ds = struct.unpack_from("<II", d, p + 36)
    if st == 0x3F: live += 1
    elif st == 0x3C: dead += 1
    else: sys.exit("state %x" % st)
    p += (60 + ns + ds + 3) & ~3
assert live == 1, live
assert dead > 0, dead
assert end - p == 2000, end - p
assert all(b == 0xFF for b in d[p:end])
PY
    # the box's own store is never written
    ! cmp -s "$BOX/OVMF_VARS.fd" "$STATE/vars-full.fd"
}

@test "a store that is not an authenticated variable store cannot be filled: could not run" {
    head -c 70000 /dev/zero > "$BOX/OVMF_VARS.fd"
    run "$SCRIPT" "$BOX" "$STATE"
    [ "$status" -eq 3 ] || { echo "status $status: $output"; false; }
    [[ "$output" == *"not an authenticated variable store"* ]]
    [ ! -s "$STUB_QEMU_ARGV" ]
}

@test "QEMU failing to start is could not run" {
    STUB_FAIL=1 run "$SCRIPT" "$BOX" "$STATE"
    [ "$status" -eq 3 ] || { echo "status $status: $output"; false; }
    [[ "$output" == *"QEMU did not start"*"could not initialize kvm"* ]]
}

@test "usage: two arguments; a non-empty state dir, a bad accelerator or limit are refused" {
    run "$SCRIPT" "$BOX"
    [ "$status" -eq 2 ] || { echo "status $status: $output"; false; }
    mkdir -p "$STATE"; : > "$STATE/x"
    run "$SCRIPT" "$BOX" "$STATE"
    [ "$status" -eq 2 ] || { echo "status $status: $output"; false; }
    [[ "$output" == *"is not empty"* ]]
    GATE_ACCEL=tcg run "$SCRIPT" "$BOX" "$BATS_TEST_TMPDIR/s2"
    [ "$status" -eq 2 ] || { echo "status $status: $output"; false; }
    GATE_VARSTORE_LIMIT=lots run "$SCRIPT" "$BOX" "$BATS_TEST_TMPDIR/s3"
    [ "$status" -eq 2 ] || { echo "status $status: $output"; false; }
}

@test "a box without its firmware is could not run" {
    rm "$BOX/OVMF_CODE.fd"
    run "$SCRIPT" "$BOX" "$STATE"
    [ "$status" -eq 3 ] || { echo "status $status: $output"; false; }
    [[ "$output" == *"the box has no OVMF_CODE.fd"* ]]
}

@test "GATE_ACCEL reaches QEMU" {
    GATE_ACCEL=hvf run "$SCRIPT" "$BOX" "$STATE"
    [ "$status" -eq 0 ] || { echo "status $status: $output"; false; }
    argv_of 1 | grep -qxF hvf
}

@test "portable: no /proc, and none of the GNU-only flags this project avoids" {
    ! grep -n '/proc/' "$SCRIPT" || false
    ! grep -nE 'sed -i|grep -P|readlink -f|date \+%N|stat -c|sleep 0\.' "$SCRIPT"
}

@test "F33: unrelated QEMU, truncated and unknown identity never receive signals" {
    for identity in unrelated truncated unknown; do
        STUB_IDENTITY=$identity run "$SCRIPT" "$BOX" "$STATE"
        [ "$(grep -c '^kill ' "$STUB_LOG")" -eq 0 ]
        [ "$status" -ne 0 ]
        [[ "$output" == *ownership* ]]
        rm -rf "$STATE"
        mkdir -p "$STATE"
    done
}

@test "F33: PID reused after TERM never receives KILL" {
    STUB_REUSE=1 run "$SCRIPT" "$BOX" "$STATE"
    grep -qxF "kill -TERM $OWNED" "$STUB_LOG"
    ! grep -q 'kill -KILL' "$STUB_LOG" || false
    [ "$status" -ne 0 ]
    [ -e "$STATE/qemu.pid" ]
}

@test "F33: owned TERM exit cleans pidfile with spaces in state path" {
    STATE="$STATE with spaces"
    STUB_TERM_EXITS=1 run "$SCRIPT" "$BOX" "$STATE"
    [ "$status" -eq 0 ]
    grep -qxF "kill -TERM $OWNED" "$STUB_LOG"
    ! grep -q 'kill -KILL' "$STUB_LOG" || false
    [ ! -e "$STATE/qemu.pid" ]
}

@test "F33: wide command fallback still proves ownership" {
    STUB_ARGS_UNSUPPORTED=1 STUB_TERM_EXITS=1 run "$SCRIPT" "$BOX" "$STATE"
    [ "$status" -eq 0 ]
    grep -qxF "kill -TERM $OWNED" "$STUB_LOG"
    [ ! -e "$STATE/qemu.pid" ]
}

# Deterministic acknowledgment model: KILL leaves the process running until the
# next polling sleep. No wall-clock race, native ps, or real signal is involved.
cleanup_clock() {
    cat > "$STUBS/sleep" <<'STUB'
#!/bin/sh
printf 'poll %s\n' "$*" >> "$STUB_LOG"
if [ -e "$STUB_LOG.pending" ]; then
    if [ "${STUB_KILL_EXIT:-}" = zombie ]; then
        : > "$STUB_LOG.zombie"
    else
        : > "$STUB_LOG.dead"
    fi
    rm -f "$STUB_LOG.pending"
fi
STUB
    chmod +x "$STUBS/sleep"
}

@test "F33 followup: delayed KILL exit uses reserved acknowledgment budget" {
    cleanup_clock
    export GATE_KILL_WAIT=3 STUB_KILL_EXIT=delayed
    run "$SCRIPT" "$BOX" "$STATE"
    [ "$status" -eq 0 ] || { echo "$status: $output"; false; }
    grep -qxF "kill -TERM $OWNED" "$STUB_LOG"
    grep -qxF "kill -KILL $OWNED" "$STUB_LOG"
    [ ! -e "$STATE/qemu.pid" ]
    # All cleanup sleeps share the original total budget, including acknowledgment.
    [ "$(awk '/^kill -TERM / { active=1 } active && /^poll 1$/ { n++ } END { print n+0 }' "$STUB_LOG")" -eq 3 ]
}

@test "F33 followup: unconfirmed KILL exit retains PID and fails" {
    cleanup_clock
    export GATE_KILL_WAIT=3 STUB_KILL_EXIT=never
    run "$SCRIPT" "$BOX" "$STATE"
    [ "$status" -eq 3 ] || { echo "$status: $output"; false; }
    grep -qxF "kill -TERM $OWNED" "$STUB_LOG"
    grep -qxF "kill -KILL $OWNED" "$STUB_LOG"
    [ "$(cat "$STATE/qemu.pid")" = "$OWNED" ]
    # All cleanup sleeps share the original total budget, including acknowledgment.
    [ "$(awk '/^kill -TERM / { active=1 } active && /^poll 1$/ { n++ } END { print n+0 }' "$STUB_LOG")" -eq 3 ]
}

@test "F33 followup: zero budget gives no asynchronous grace" {
    cleanup_clock
    export GATE_KILL_WAIT=0 STUB_KILL_EXIT=delayed
    run "$SCRIPT" "$BOX" "$STATE"
    [ "$status" -eq 3 ] || { echo "$status: $output"; false; }
    grep -qxF "kill -TERM $OWNED" "$STUB_LOG"
    grep -qxF "kill -KILL $OWNED" "$STUB_LOG"
    [ "$(cat "$STATE/qemu.pid")" = "$OWNED" ]
    # All cleanup sleeps share the original total budget, including acknowledgment.
    [ "$(awk '/^kill -TERM / { active=1 } active && /^poll 1$/ { n++ } END { print n+0 }' "$STUB_LOG")" -eq 0 ]
}

@test "F33 followup: zombie after KILL is terminal" {
    cleanup_clock
    export GATE_KILL_WAIT=3 STUB_KILL_EXIT=zombie
    run "$SCRIPT" "$BOX" "$STATE"
    [ "$status" -eq 0 ] || { echo "$status: $output"; false; }
    grep -qxF "kill -TERM $OWNED" "$STUB_LOG"
    grep -qxF "kill -KILL $OWNED" "$STUB_LOG"
    [ ! -e "$STATE/qemu.pid" ]
    # All cleanup sleeps share the original total budget, including acknowledgment.
    [ "$(awk '/^kill -TERM / { active=1 } active && /^poll 1$/ { n++ } END { print n+0 }' "$STUB_LOG")" -eq 3 ]
}

@test "F33 followup: unknown state remains conservative" {
    cleanup_clock
    export GATE_KILL_WAIT=3 STUB_KILL_EXIT=never
    export STUB_STAT=unknown
    run "$SCRIPT" "$BOX" "$STATE"
    [ "$status" -eq 3 ] || { echo "$status: $output"; false; }
    grep -qxF "kill -TERM $OWNED" "$STUB_LOG"
    grep -qxF "kill -KILL $OWNED" "$STUB_LOG"
    [ "$(cat "$STATE/qemu.pid")" = "$OWNED" ]
    # All cleanup sleeps share the original total budget, including acknowledgment.
    [ "$(awk '/^kill -TERM / { active=1 } active && /^poll 1$/ { n++ } END { print n+0 }' "$STUB_LOG")" -eq 3 ]
}

@test "F33 followup: one-second budget reserves exactly one KILL poll" {
    cleanup_clock
    export GATE_KILL_WAIT=1
    export STUB_KILL_EXIT=delayed
    run "$SCRIPT" "$BOX" "$STATE"
    [ "$status" -eq 0 ] || { echo "$status: $output"; false; }
    [ ! -e "$STATE/qemu.pid" ]
    [ "$(awk '/^kill -TERM / { phase=1 } /^kill -KILL / { phase=2 } /^poll 1$/ { if (phase==1) term++; if (phase==2) ack++ } END { print term+0, ack+0 }' "$STUB_LOG")" = "0 1" ]
}

@test "F33 followup: default budget reserves exactly one KILL poll" {
    cleanup_clock
    unset GATE_KILL_WAIT
    export STUB_KILL_EXIT=delayed
    run "$SCRIPT" "$BOX" "$STATE"
    [ "$status" -eq 0 ] || { echo "$status: $output"; false; }
    [ ! -e "$STATE/qemu.pid" ]
    [ "$(awk '/^kill -TERM / { phase=1 } /^kill -KILL / { phase=2 } /^poll 1$/ { if (phase==1) term++; if (phase==2) ack++ } END { print term+0, ack+0 }' "$STUB_LOG")" = "9 1" ]
}
