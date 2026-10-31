#!/usr/bin/env bats
# platform: host-agnostic -- tested on Linux with KVM only
#
# bin/gate-vm.sh boots, drives and stops a QEMU guest. Here every command it runs is a stub:
# PATH is the stub directory, /usr/bin and /bin (no sbin); shutdown reboot halt poweroff
# systemctl sudo refuse; ssh and scp record argv in $STUB_LOG and print canned output, never
# running the remote command; kill only records. The "QEMU" pid is the test shell, never signaled.

bats_require_minimum_version 1.5.0

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    SCRIPT="$REPO/bin/gate-vm.sh"
    STUBS="$BATS_TEST_TMPDIR/stubs"
    STATE="$BATS_TEST_TMPDIR/state"
    BOX="$BATS_TEST_TMPDIR/box"
    export STUB_LOG="$BATS_TEST_TMPDIR/stub.log"
    export STUB_QEMU_ARGV="$BATS_TEST_TMPDIR/qemu.argv"
    export HOME="$BATS_TEST_TMPDIR/home"
    mkdir -p "$STUBS" "$STATE" "$BOX" "$HOME"
    : > "$STUB_LOG"
    for f in box_0.img OVMF_CODE.fd OVMF_VARS.fd opencore.img; do : > "$BOX/$f"; done
    # The box's Vagrantfile names its release on a line of its own (templates/*/box.Vagrantfile.pkrtpl);
    # a 10.9 box unless a test says otherwise. A state dir a 10.9 box booted records it, as boot does,
    # for the tests that drive a guest without booting one.
    box_release 10.9
    echo 10.9 > "$STATE/release"
    for c in shutdown reboot halt poweroff systemctl sudo; do
        cat > "$STUBS/$c" <<'STUB'
#!/bin/sh
echo "stub: ${0##*/} refused"
exit 1
STUB
    done
    # a stand-in QEMU: records argv, "daemonizes" by writing the pidfile with a pid this test owns
    cat > "$STUBS/qemu-system-x86_64" <<'STUB'
#!/bin/sh
[ -z "${STUB_QEMU_FAIL:-}" ] || { echo "qemu: failed" >&2; exit 1; }
rm -f "$STUB_LOG.dead" "$STUB_LOG.pending" "$STUB_LOG.zombie"
printf 'qemu-system-x86_64 %s\n' "$*" > "$STUB_LOG.args"
printf '%s\n' "$@" > "$STUB_QEMU_ARGV"
pid=""; prev=""
for a in "$@"; do [ "$prev" = -pidfile ] && pid="$a"; prev="$a"; done
echo "${STUB_QEMU_PID:-999999}" > "$pid"
STUB
    cat > "$STUBS/qemu-img" <<'STUB'
#!/bin/sh
printf 'qemu-img %s\n' "$*" >> "$STUB_LOG"
case "$1" in create) for last; do :; done; : > "$last";; esac
STUB
    # ssh/scp: record, never run the remote command. STUB_SSH_UP=1 reaches the guest.
    # STUB_BOOTTIMES: file of canned boottimes, one per kern.boottime call; the last repeats;
    # a line "down" fails that call.
    cat > "$STUBS/ssh" <<'STUB'
#!/bin/sh
printf 'ssh %s\n' "$*" >> "$STUB_LOG"
[ -n "${STUB_SSH_UP:-}" ] || { echo "ssh: connect to host 127.0.0.1: Connection refused" >&2; exit 255; }
case "$*" in
*shutdown*) [ -z "${STUB_SHUTDOWN_SAYS:-}" ] || echo "$STUB_SHUTDOWN_SAYS" >&2; exit "${STUB_SHUTDOWN_RC:-0}" ;;
*kern.boottime*)
    [ -n "${STUB_BOOTTIMES:-}" ] || exit 0
    n=$(cat "$STUB_BOOTTIMES.n" 2>/dev/null || echo 0)
    n=$((n + 1)); echo "$n" > "$STUB_BOOTTIMES.n"
    total=$(wc -l < "$STUB_BOOTTIMES")
    [ "$n" -le "$total" ] || n=$total
    line=$(sed -n "${n}p" "$STUB_BOOTTIMES")
    [ "$line" != down ] || exit 255
    echo "{ sec = $line, usec = 0 } Sun Oct 12 10:00:00 2026"
    ;;
esac
exit 0
STUB
    cat > "$STUBS/scp" <<'STUB'
#!/bin/sh
printf 'scp %s\n' "$*" >> "$STUB_LOG"
[ -n "${STUB_SSH_UP:-}" ] || exit 255
exit 0
STUB
    cat > "$STUBS/python3" <<'STUB'
#!/bin/sh
printf "python3 %s\n" "$*" >> "$STUB_LOG"
[ -z "${STUB_PY_FAIL:-}" ] || exit 1
cat > /dev/null
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
    export GATE_KVM="$BATS_TEST_TMPDIR/kvm"; : > "$GATE_KVM"
    export GATE_POLL_INTERVAL=1 GATE_KILL_WAIT=1
    export GATE_SSH_TIMEOUT=2 GATE_RESTART_TIMEOUT=2 GATE_SHUTDOWN_TIMEOUT=2 GATE_CMD_TIMEOUT=5
    # The stand-in uses the existing test shell pid; no teardown signal is needed.
    OWNED=$$ # existing test shell: only presence probes, never signaled
    export STUB_QEMU_PID=$OWNED STUB_PS_QEMU=$OWNED
    # a pid that was live and is gone: a "stale" pid that cannot be anyone else's right now
    sleep 0 & REAPED=$!
    wait "$REAPED" || true
    export STUB_DEAD_PID=$REAPED
    echo 'f33.[literal]*' > "$STATE/uuid"
    printf 'qemu-system-x86_64 -uuid f33.[literal]* -pidfile %s/qemu.pid\n' "$STATE" > "$STUB_LOG.args"
}

# box_release <release>: the box's Vagrantfile, as its template renders it, naming <release>
box_release() {
    printf '%s\n' 'Vagrant.configure("2") do |config|' > "$BOX/Vagrantfile"
    printf '# guest-macosx-release: %s\n' "$1" >> "$BOX/Vagrantfile"
    printf '%s\n' '  config.vm.guest = :darwin' 'end' >> "$BOX/Vagrantfile"
}

teardown() {
    [ ! -f "$STATE/qmp.dir" ] || case "$(cat "$STATE/qmp.dir")" in /tmp/gate-qmp.example) ;; *) rm -rf "$(cat "$STATE/qmp.dir")" ;; esac
}

@test "safety: the dangerous names refuse, and the stub ssh never runs the remote command" {
    for c in shutdown reboot halt poweroff systemctl sudo; do
        run "$STUBS/$c" now
        [ "$status" -eq 1 ]
        [ "$output" = "stub: $c refused" ]
        [ "$(command -v "$c")" = "$STUBS/$c" ]
    done
    marker="$BATS_TEST_TMPDIR/ran"
    STUB_SSH_UP=1 run "$STUBS/ssh" host "touch $marker"
    [ ! -e "$marker" ]
    [[ "$PATH" != *sbin* ]]
    # scp: nothing is copied or executed locally
    echo data > "$BATS_TEST_TMPDIR/src"
    STUB_SSH_UP=1 run "$STUBS/scp" "$BATS_TEST_TMPDIR/src" "$BATS_TEST_TMPDIR/dst"
    [ ! -e "$BATS_TEST_TMPDIR/dst" ]
    STUB_SSH_UP=1 run "$STUBS/scp" -o "ProxyCommand=touch $marker" "$BATS_TEST_TMPDIR/src" vagrant@127.0.0.1:/x
    [ ! -e "$marker" ]
    grep -q '^scp ' "$STUB_LOG"
}

@test "no command, or an unknown one, is a usage error" {
    run "$SCRIPT"
    [ "$status" -eq 2 ]
    run "$SCRIPT" frobnicate "$STATE"
    [ "$status" -eq 2 ]
    run "$SCRIPT" boot "$STATE"
    [ "$status" -eq 2 ]
}

@test "boot: the QEMU argv, with an overlay, snapshot=on, kvm, serial log, qmp and the ssh port" {
    export STUB_SSH_UP=1
    run "$SCRIPT" boot "$STATE" "$BOX"
    [ "$status" -eq 0 ]
    argv=$(cat "$STUB_QEMU_ARGV")
    [[ "$argv" == *$'\n-accel\nkvm\n'* ]]
    [[ "$argv" == *"q35,vmport=off"* ]]
    [[ "$argv" == *$'\n-cpu\nPenryn,vendor=GenuineIntel,+ssse3,+sse4.1,+sse4.2\n'* ]]
    [[ "$argv" == *$'\n-m\n4096M\n'* ]]
    [[ "$argv" == *$'\n-smp\n2\n'* ]]
    grep -qxF "if=pflash,format=raw,unit=0,readonly=on,file=$BOX/OVMF_CODE.fd" "$STUB_QEMU_ARGV"
    grep -qxF "if=pflash,format=raw,unit=1,snapshot=on,file=$BOX/OVMF_VARS.fd" "$STUB_QEMU_ARGV"
    grep -qxF "id=opencore,if=none,format=raw,snapshot=on,file=$BOX/opencore.img" "$STUB_QEMU_ARGV"
    grep -qxF "id=target,if=none,format=qcow2,detect-zeroes=unmap,discard=unmap,file=$STATE/overlay.qcow2" "$STUB_QEMU_ARGV"
    grep -qxF "ide-hd,bus=ide.0,drive=target" "$STUB_QEMU_ARGV"
    grep -qxF "user,id=net0,hostfwd=tcp:127.0.0.1:2223-:22" "$STUB_QEMU_ARGV"
    # the NIC's address, derived from this boot's -uuid (derived_mac), never QEMU's one default
    grep -qxF "e1000-82545em,netdev=net0,mac=$(bash -c ". '$REPO/lib/common.sh'; derived_mac $(cat "$STATE/uuid")")" "$STUB_QEMU_ARGV"
    ! grep -qF "52:54:00:12:34:56" "$STUB_QEMU_ARGV" || false
    grep -qxF "VGA,vgamem_mb=64" "$STUB_QEMU_ARGV"
    grep -qxF "file:$STATE/serial.log" "$STUB_QEMU_ARGV"
    qdir=$(cat "$STATE/qmp.dir")
    grep -qxF "unix:$qdir/qmp.sock,server=on,wait=off" "$STUB_QEMU_ARGV"
    [ "${#qdir}" -lt 60 ]
    grep -qxF "$STATE/qemu.pid" "$STUB_QEMU_ARGV"
    grep -qxF -- "-daemonize" "$STUB_QEMU_ARGV"
    grep -qxF -- "-display" "$STUB_QEMU_ARGV"
    grep -q "^qemu-img create -f qcow2 -F qcow2 -b $BOX/box_0.img $STATE/overlay.qcow2" "$STUB_LOG"
    [[ "$(ls -l "$STATE/ssh-key")" == -rw-------* ]]
    cmp "$STATE/ssh-key" "$REPO/templates/mavericks/vagrant-standard-insecure-first-boot-only.key.rsa"
}

@test "boot: QEMU gets a -uuid of its own, fresh at every boot, kept in the state dir (docs/decisions/0024)" {
    export STUB_SSH_UP=1
    run "$SCRIPT" boot "$STATE" "$BOX"
    [ "$status" -eq 0 ]
    first=$(cat "$STATE/uuid")
    [[ "$first" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]]
    [[ "$(cat "$STUB_QEMU_ARGV")" == *$'\n-uuid\n'"$first"$'\n'* ]]
    [ "$(grep -cxF -- -uuid "$STUB_QEMU_ARGV")" -eq 1 ]
    [[ "$output" == *"QEMU's -uuid: $first"* ]]
    # a second boot is a fresh guest on a fresh overlay: a machine of its own
    rm -f "$STATE/qemu.pid"
    run "$SCRIPT" boot "$STATE" "$BOX"
    [ "$status" -eq 0 ]
    [ "$(cat "$STATE/uuid")" != "$first" ]
    [[ "$(cat "$STUB_QEMU_ARGV")" == *$'\n-uuid\n'"$(cat "$STATE/uuid")"$'\n'* ]]
}

@test "boot: the box's devices, the one shape (docs/decisions/0035): OpenCore on virtio-blk, USB for input alone" {
    export STUB_SSH_UP=1
    run "$SCRIPT" boot "$STATE" "$BOX"
    [ "$status" -eq 0 ]
    # Every device and drive line that is USB, a host controller, an input device or OpenCore's,
    # exactly these, in this order: OpenCore's disk on virtio-blk, booted first; EHCI with no
    # companion; a usb-kbd and a usb-tablet on it. No XHCI, UHCI, OHCI or usb-storage.
    got=$(grep -E 'usb|hci|kbd|mouse|tablet|keyboard|opencore|bootindex|virtio' "$STUB_QEMU_ARGV")
    want="id=opencore,if=none,format=raw,snapshot=on,file=$BOX/opencore.img
virtio-blk-pci,drive=opencore,bootindex=0,bus=pcie.0,addr=0x10
ich9-usb-ehci1,id=ehci,bus=pcie.0,addr=0x1d.0
usb-kbd,bus=ehci.0
usb-tablet,bus=ehci.0"
    [ "$got" = "$want" ]
    # and no USB from q35 itself
    run grep -qxE -- '-usb|-usbdevice' "$STUB_QEMU_ARGV"
    [ "$status" -eq 1 ]
    run grep -qE 'usb=on' "$STUB_QEMU_ARGV"
    [ "$status" -eq 1 ]
}

@test "boot: GATE_ACCEL=hvf, GATE_SSH_PORT and GATE_CPU change the argv, and hvf needs no /dev/kvm" {
    export STUB_SSH_UP=1 GATE_ACCEL=hvf GATE_SSH_PORT=2999 GATE_CPU=SandyBridge GATE_KVM="$BATS_TEST_TMPDIR/nokvm"
    run "$SCRIPT" boot "$STATE" "$BOX"
    [ "$status" -eq 0 ]
    argv=$(cat "$STUB_QEMU_ARGV")
    [[ "$argv" == *$'\n-accel\nhvf\n'* ]]
    [[ "$argv" == *$'\n-cpu\nSandyBridge\n'* ]]
    grep -qxF "user,id=net0,hostfwd=tcp:127.0.0.1:2999-:22" "$STUB_QEMU_ARGV"
    grep -q 'vagrant@127.0.0.1' "$STUB_LOG"
    grep -q -- '-p 2999' "$STUB_LOG"
}

@test "boot: a live qemu.pid is refused with status 3, naming the pid, and nothing starts" {
    echo "$OWNED" > "$STATE/qemu.pid"
    run "$SCRIPT" boot "$STATE" "$BOX"
    [ "$status" -eq 3 ]
    [[ "$output" == *"$OWNED"* ]]
    [ ! -e "$STUB_QEMU_ARGV" ]
}

@test "boot: a stale qemu.pid does not block" {
    export STUB_SSH_UP=1
    echo "$REAPED" > "$STATE/qemu.pid"
    run "$SCRIPT" boot "$STATE" "$BOX"
    [ "$status" -eq 0 ]
}

@test "boot: a qemu.pid naming a live non-QEMU process is stale: warned about and not an obstacle" {
    export STUB_SSH_UP=1 STUB_PS_QEMU=""
    echo "$OWNED" > "$STATE/qemu.pid"
    run "$SCRIPT" boot "$STATE" "$BOX"
    [ "$status" -eq 0 ]
    [[ "$output" == *"stale qemu.pid names pid $OWNED, which is not QEMU"* ]]
    [ -e "$STUB_QEMU_ARGV" ]
}

@test "boot: QEMU dying before SSH answers is status 1, naming it" {
    export STUB_QEMU_PID=$REAPED STUB_PS_QEMU=$REAPED GATE_SSH_TIMEOUT=30
    run "$SCRIPT" boot "$STATE" "$BOX"
    [ "$status" -eq 1 ]
    [[ "$output" == *"exited before SSH answered"* ]]
}

@test "boot: a state directory that cannot be created is status 3" {
    : > "$BATS_TEST_TMPDIR/afile"
    run "$SCRIPT" boot "$BATS_TEST_TMPDIR/afile/state" "$BOX"
    [ "$status" -eq 3 ]
}

@test "a missing state directory is a usage error, except for screenshot, which is never fatal" {
    for c in ssh put restart shutdown; do
        run "$SCRIPT" $c "$BATS_TEST_TMPDIR/nonexistent" a b
        [ "$status" -eq 2 ] || { echo "$c: $status"; false; }
    done
    run "$SCRIPT" screenshot "$BATS_TEST_TMPDIR/nonexistent" "$BATS_TEST_TMPDIR/o.png"
    [ "$status" -eq 0 ]
    [[ "$output" == *warning* ]]
}

@test "a deep state directory still gets a short QMP socket path" {
    export STUB_SSH_UP=1
    deep="$STATE/$(printf 'd%.0s' $(seq 1 60))/$(printf 'e%.0s' $(seq 1 60))"
    run "$SCRIPT" boot "$deep" "$BOX"
    [ "$status" -eq 0 ]
    sock=$(grep '^unix:' "$STUB_QEMU_ARGV" | sed 's/^unix://; s/,.*//')
    [ "${#sock}" -lt 100 ]
    [ -d "$(dirname "$sock")" ]
    run "$SCRIPT" kill "$deep"
    [ ! -d "$(dirname "$sock")" ]
    [ ! -e "$deep/qmp.dir" ]
}

@test "boot: a missing box file, or no /dev/kvm, is status 3" {
    rm "$BOX/opencore.img"
    run "$SCRIPT" boot "$STATE" "$BOX"
    [ "$status" -eq 3 ]
    [[ "$output" == *opencore.img* ]]
    : > "$BOX/opencore.img"
    rm "$GATE_KVM"
    run "$SCRIPT" boot "$STATE" "$BOX"
    [ "$status" -eq 3 ]
    [[ "$output" == *"$GATE_KVM"* ]]
    [ ! -e "$STUB_QEMU_ARGV" ]
}

@test "boot: QEMU failing to start is status 3" {
    export STUB_QEMU_FAIL=1
    run "$SCRIPT" boot "$STATE" "$BOX"
    [ "$status" -eq 3 ]
}

@test "boot: SSH never answering is status 1 within the limit, with the serial log's end" {
    for i in $(seq 1 60); do echo "serial line $i"; done > "$STATE/serial.log"
    SECONDS=0
    run "$SCRIPT" boot "$STATE" "$BOX"
    [ "$status" -eq 1 ]
    [ "$SECONDS" -lt 10 ]
    [[ "$output" == *"SSH did not answer within 2s"* ]]
    [[ "$output" == *"serial line 60"* ]]
    [[ "$output" == *"serial line 21"* ]]
    [[ "$output" != *"serial line 20"$'\n'* ]]
}

@test "ssh: the options, the key, the command, and no ~/.ssh/config written" {
    export STUB_SSH_UP=1
    run "$SCRIPT" ssh "$STATE" uname -a
    [ "$status" -eq 0 ]
    line=$(grep '^ssh ' "$STUB_LOG" | tail -n 1)
    [[ "$line" == *"-i $STATE/ssh-key"* ]]
    [[ "$line" == *"-p 2223"* ]]
    [[ "$line" == *"StrictHostKeyChecking=no"* ]]
    [[ "$line" == *"UserKnownHostsFile=/dev/null"* ]]
    [[ "$line" == *"IdentitiesOnly=yes"* ]]
    [[ "$line" == *"ConnectTimeout=10"* ]]
    [[ "$line" == *"LogLevel=ERROR"* ]]
    [[ "$line" == *"ServerAliveInterval=15"* ]]
    [[ "$line" == *"ServerAliveCountMax=8"* ]]
    [[ "$line" == *"vagrant@127.0.0.1 uname -a" ]]
    [ ! -e "$HOME/.ssh" ]
}

@test "ssh: the guest's exit status comes back" {
    cat > "$STUBS/ssh" <<'STUB'
#!/bin/sh
exit 7
STUB
    run "$SCRIPT" ssh "$STATE" false
    [ "$status" -eq 7 ]
}

@test "put: scp with the same options" {
    export STUB_SSH_UP=1
    run "$SCRIPT" put "$STATE" /tmp/local.txt /Users/vagrant/gate/x
    [ "$status" -eq 0 ]
    line=$(grep '^scp ' "$STUB_LOG")
    [[ "$line" == *"-i $STATE/ssh-key"* ]]
    [[ "$line" == *"-P 2223"* ]]
    [[ "$line" == *"StrictHostKeyChecking=no"* ]]
    [[ "$line" == *"ServerAliveInterval=15"* ]]
    [[ "$line" == *"/tmp/local.txt vagrant@127.0.0.1:/Users/vagrant/gate/x" ]]
    [ ! -e "$HOME/.ssh" ]
}

@test "restart: passes when the second boottime differs, and asks the guest itself" {
    export STUB_SSH_UP=1 STUB_BOOTTIMES="$BATS_TEST_TMPDIR/bt"
    printf '%s\n' 1000 down 2000 > "$STUB_BOOTTIMES"
    run "$SCRIPT" restart "$STATE"
    [ "$status" -eq 0 ]
    grep -q 'sysctl -n kern.boottime' "$STUB_LOG"
    grep -q 'sudo /usr/local/sbin/guest-macosx-shutdown -r' "$STUB_LOG"
    [[ "$output" != *refused* ]]
}

@test "restart, shutdown: through the guest's own guest-macosx-shutdown, or 10.9 can stall 20 s" {
    # The box's /usr/local/sbin/guest-macosx-shutdown (assets/guest/firstboot.sh) stops what would hold
    # launchd's userspace shutdown for its 20 s exit timeout: loginwindow's ManagedClient, and an
    # unwritten preference in UserEventAgent (docs/decisions/0019). The gate asks for exactly
    # that, nothing more and nothing of its own.
    export STUB_SSH_UP=1 STUB_BOOTTIMES="$BATS_TEST_TMPDIR/bt"
    printf '%s\n' 1000 2000 > "$STUB_BOOTTIMES"
    run "$SCRIPT" restart "$STATE"
    [ "$status" -eq 0 ]
    line=$(grep 'guest-macosx-shutdown' "$STUB_LOG")
    [[ "$line" == *" sudo /usr/local/sbin/guest-macosx-shutdown -r" ]]
    run grep -q 'shutdown -r now\|killall' "$STUB_LOG"
    [ "$status" -eq 1 ]
    : > "$STUB_LOG"
    sleep 1 & short=$!
    export STUB_PS_QEMU="$OWNED $short"
    echo "$short" > "$STATE/qemu.pid"
    run "$SCRIPT" shutdown "$STATE"
    [ "$status" -eq 0 ]
    line=$(grep 'guest-macosx-shutdown' "$STUB_LOG")
    [[ "$line" == *" sudo /usr/local/sbin/guest-macosx-shutdown -h" ]]
    run grep -q 'shutdown -h now\|killall' "$STUB_LOG"
    [ "$status" -eq 1 ]
}

@test "restart, shutdown: what guest-macosx-shutdown says reaches the step's log" {
    # Its bounded waits end in a line like this one, which system.log keeps in the guest; the
    # gate's log must have it too, or a slow guest's shutdown reads as a plain one.
    export STUB_SSH_UP=1 STUB_BOOTTIMES="$BATS_TEST_TMPDIR/bt"
    export STUB_SHUTDOWN_SAYS="guest-macosx-shutdown: UserEventAgent (LoginWindow) still dirty after 30s; shutting down anyway"
    printf '%s\n' 1000 2000 > "$STUB_BOOTTIMES"
    run "$SCRIPT" restart "$STATE"
    [ "$status" -eq 0 ]
    [[ "$output" == *"the guest said: guest-macosx-shutdown: UserEventAgent (LoginWindow) still dirty after 30s; shutting down anyway"* ]]
}

@test "restart, shutdown: the guest command outlasts guest-macosx-shutdown's three 30 s waits" {
    # A stand-in timeout(1) that records its limit and runs the stub ssh: never the real one.
    cat > "$STUBS/timeout" <<'STUB'
#!/bin/sh
echo "timeout $1" >> "$STUB_LOG"
shift
exec "$@"
STUB
    chmod +x "$STUBS/timeout"
    export STUB_SSH_UP=1 STUB_BOOTTIMES="$BATS_TEST_TMPDIR/bt"
    printf '%s\n' 1000 2000 > "$STUB_BOOTTIMES"
    run "$SCRIPT" restart "$STATE"
    [ "$status" -eq 0 ]
    limit=$(grep -B1 'guest-macosx-shutdown -r' "$STUB_LOG" | sed -n 's/^timeout \([0-9]*\)$/\1/p')
    [ -n "$limit" ]
    [ "$limit" -ge 105 ]
}

@test "restart: a clock step of a second is not a restart" {
    export STUB_SSH_UP=1 STUB_BOOTTIMES="$BATS_TEST_TMPDIR/bt"
    printf '%s\n' 1000 1001 > "$STUB_BOOTTIMES"
    run "$SCRIPT" restart "$STATE"
    [ "$status" -eq 1 ]
    [[ "$output" == *"did not restart"* ]]
}

@test "restart, shutdown: a sudo shutdown the guest refuses fails at once with its status" {
    export STUB_SSH_UP=1 STUB_SHUTDOWN_RC=5 STUB_BOOTTIMES="$BATS_TEST_TMPDIR/bt" GATE_RESTART_TIMEOUT=30 GATE_SHUTDOWN_TIMEOUT=30
    printf '%s\n' 1000 > "$STUB_BOOTTIMES"
    SECONDS=0
    run "$SCRIPT" restart "$STATE"
    [ "$status" -eq 1 ]
    [[ "$output" == *"sudo /usr/local/sbin/guest-macosx-shutdown -r failed in the guest (status 5)"* ]]
    echo "$OWNED" > "$STATE/qemu.pid"
    run "$SCRIPT" shutdown "$STATE"
    [ "$status" -eq 1 ]
    [[ "$output" == *"sudo /usr/local/sbin/guest-macosx-shutdown -h failed in the guest (status 5)"* ]]
    [ "$SECONDS" -lt 10 ]
}

@test "restart: the same boottime until the limit is status 1, did not restart" {
    export STUB_SSH_UP=1 STUB_BOOTTIMES="$BATS_TEST_TMPDIR/bt"
    printf '%s\n' 1000 > "$STUB_BOOTTIMES"
    SECONDS=0
    run "$SCRIPT" restart "$STATE"
    [ "$status" -eq 1 ]
    [ "$SECONDS" -lt 10 ]
    [[ "$output" == *"restart: the guest answered on the same boot (did not restart)"* ]]
}

@test "restart: no answer after the shutdown is status 1, never came back" {
    export STUB_SSH_UP=1 STUB_BOOTTIMES="$BATS_TEST_TMPDIR/bt"
    printf '%s\n' 1000 down > "$STUB_BOOTTIMES"
    run "$SCRIPT" restart "$STATE"
    [ "$status" -eq 1 ]
    [[ "$output" == *"restart: SSH never came back within 2s"* ]]
}

@test "restart: a usec is never mistaken for sec" {
    export STUB_SSH_UP=1
    cat > "$STUBS/ssh" <<'STUB'
#!/bin/sh
echo "{ sec = 1000, usec = 2000 } Sun"
STUB
    run "$SCRIPT" restart "$STATE"
    [ "$status" -eq 1 ]
    [[ "$output" == *"did not restart"* ]]
}

@test "restart: a guest whose boottime cannot be read is status 1" {
    export STUB_SSH_UP=1
    cat > "$STUBS/ssh" <<'STUB'
#!/bin/sh
echo garbage
STUB
    run "$SCRIPT" restart "$STATE"
    [ "$status" -eq 1 ]
    [[ "$output" == *"kern.boottime"* ]]
}

@test "shutdown: waits for the QEMU pid to vanish" {
    export STUB_SSH_UP=1
    sleep 1 & short=$!
    export STUB_PS_QEMU="$OWNED $short"
    echo "$short" > "$STATE/qemu.pid"
    run "$SCRIPT" shutdown "$STATE"
    [ "$status" -eq 0 ]
    grep -q 'sudo /usr/local/sbin/guest-macosx-shutdown -h' "$STUB_LOG"
}

@test "shutdown: QEMU still running at the limit is status 1" {
    export STUB_SSH_UP=1
    echo "$OWNED" > "$STATE/qemu.pid"
    run "$SCRIPT" shutdown "$STATE"
    [ "$status" -eq 1 ]
    [[ "$output" == *"shutdown: QEMU still running after 2s"* ]]
}

@test "screenshot: a QMP screendump through python3, and a failure is only a warning" {
    run "$SCRIPT" screenshot "$STATE" "$BATS_TEST_TMPDIR/out.png"
    [ "$status" -eq 0 ]
    [[ "$output" == *"no QMP socket"* ]]
    echo /tmp/gate-qmp.example > "$STATE/qmp.dir"
    run "$SCRIPT" screenshot "$STATE" "$BATS_TEST_TMPDIR/out.png"
    [ "$status" -eq 0 ]
    grep -q "python3 .*/tmp/gate-qmp.example/qmp.sock" "$STUB_LOG"
    STUB_PY_FAIL=1 run "$SCRIPT" screenshot "$STATE" "$BATS_TEST_TMPDIR/out.png"
    [ "$status" -eq 0 ]
    [[ "$output" == *warning* ]]
}

@test "kill: signals the owned pid through TERM and KILL" {
    echo "$OWNED" > "$STATE/qemu.pid"
    run "$SCRIPT" kill "$STATE"
    [ "$status" -eq 0 ]
    grep -q "^kill -TERM $OWNED" "$STUB_LOG"
    grep -qxF "kill -KILL $OWNED" "$STUB_LOG"
    [ ! -e "$STATE/qemu.pid" ]
    : > "$STUB_LOG"
    rm -f "$STATE/qemu.pid"
    run "$SCRIPT" kill "$STATE"
    [ "$status" -eq 0 ]
    run "$SCRIPT" kill "$BATS_TEST_TMPDIR/nonexistent"
    [ "$status" -eq 0 ]
    [ "$(grep -c '^kill ' "$STUB_LOG")" -eq 0 ]
}

@test "kill: a pidfile naming a live process that is not QEMU signals nothing" {
    export STUB_PS_QEMU=""
    echo "$OWNED" > "$STATE/qemu.pid"
    run "$SCRIPT" kill "$STATE"
    [ "$status" -eq 3 ]
    [ "$(grep -c '^kill ' "$STUB_LOG")" -eq 0 ]
    kill -0 "$OWNED"
}

@test "kill: pidfiles naming 0, 1 or junk signal nothing" {
    for v in 0 1 -1 "" abc "12 34"; do
        printf '%s\n' "$v" > "$STATE/qemu.pid"
        run "$SCRIPT" kill "$STATE"
        [ "$status" -eq 0 ]
    done
    [ "$(grep -c '^kill ' "$STUB_LOG")" -eq 0 ]
}

@test "kill: a qmp.dir that climbs out of /tmp/gate-qmp.* is never removed" {
    qdir=$(mktemp -d /tmp/gate-qmp.XXXXXX)
    mkdir -p "$BATS_TEST_TMPDIR/victim"
    : > "$BATS_TEST_TMPDIR/victim/keep"
    echo "$qdir/../..$BATS_TEST_TMPDIR/victim" > "$STATE/qmp.dir"
    run "$SCRIPT" kill "$STATE"
    rm -rf "$qdir"
    [ "$status" -eq 0 ]
    [ -f "$BATS_TEST_TMPDIR/victim/keep" ]
    rm -f "$STATE/qmp.dir"
}

@test "boot: the box's release is its Vagrantfile's guest-macosx-release line, kept in the state dir" {
    export STUB_SSH_UP=1
    for r in 10.9 10.6; do
        box_release "$r"
        rm -f "$STATE/qemu.pid" "$STATE/release"
        run "$SCRIPT" boot "$STATE" "$BOX"
        [ "$status" -eq 0 ] || { echo "$r: $output"; false; }
        [ "$(cat "$STATE/release")" = "$r" ]
        [[ "$output" == *"the box is Mac OS X $r ($BOX/Vagrantfile)"* ]]
    done
}

@test "boot: a box that names no release, two, or one gate-vm.sh does not know is status 3, and nothing starts" {
    rm "$BOX/Vagrantfile"
    run "$SCRIPT" boot "$STATE" "$BOX"
    [ "$status" -eq 3 ]
    [[ "$output" == *"the box has no Vagrantfile ($BOX)"* ]]
    printf 'Vagrant.configure("2") do |config|\nend\n' > "$BOX/Vagrantfile"
    run "$SCRIPT" boot "$STATE" "$BOX"
    [ "$status" -eq 3 ]
    [[ "$output" == *"$BOX/Vagrantfile names no release (no '# guest-macosx-release: ' line): a box built before the gate knew releases; rebuild it"* ]]
    box_release 10.9
    echo '# guest-macosx-release: 10.6' >> "$BOX/Vagrantfile"
    run "$SCRIPT" boot "$STATE" "$BOX"
    [ "$status" -eq 3 ]
    [[ "$output" == *"$BOX/Vagrantfile names more than one release"* ]]
    for r in 10.4 '10.9 ' 10.9.5 ''; do
        box_release "$r"
        run "$SCRIPT" boot "$STATE" "$BOX"
        [ "$status" -eq 3 ] || { echo "'$r': $output"; false; }
        [[ "$output" == *"$BOX/Vagrantfile names release '$r'; gate-vm.sh knows 10.9 or 10.6"* ]]
    done
    [ ! -e "$STUB_QEMU_ARGV" ]
}

@test "boot: 10.9 boots two CPUs, 10.6 one (docs/decisions/0014, R49); GATE_SMP overrides either" {
    export STUB_SSH_UP=1
    run "$SCRIPT" boot "$STATE" "$BOX"
    [ "$status" -eq 0 ]
    [[ "$(cat "$STUB_QEMU_ARGV")" == *$'\n-smp\n2\n'* ]]
    box_release 10.6
    rm -f "$STATE/qemu.pid"
    run "$SCRIPT" boot "$STATE" "$BOX"
    [ "$status" -eq 0 ]
    [[ "$(cat "$STUB_QEMU_ARGV")" == *$'\n-smp\n1\n'* ]]
    for r in 10.9 10.6; do
        box_release "$r"
        rm -f "$STATE/qemu.pid"
        GATE_SMP=3 run "$SCRIPT" boot "$STATE" "$BOX"
        [ "$status" -eq 0 ]
        [[ "$(cat "$STUB_QEMU_ARGV")" == *$'\n-smp\n3\n'* ]]
    done
}

@test "boot: everything else in the argv is the same for both releases" {
    export STUB_SSH_UP=1
    run "$SCRIPT" boot "$STATE" "$BOX"
    [ "$status" -eq 0 ]
    # the per-boot values aside: the UUID, and the NIC address derived from it
    grep -v -e "$(cat "$STATE/uuid")" -e 'mac=' -e 'qmp.sock' -e '^-smp$' -e '^[12]$' "$STUB_QEMU_ARGV" > "$BATS_TEST_TMPDIR/argv-10.9"
    box_release 10.6
    rm -f "$STATE/qemu.pid"
    run "$SCRIPT" boot "$STATE" "$BOX"
    [ "$status" -eq 0 ]
    grep -v -e "$(cat "$STATE/uuid")" -e 'mac=' -e 'qmp.sock' -e '^-smp$' -e '^[12]$' "$STUB_QEMU_ARGV" > "$BATS_TEST_TMPDIR/argv-10.6"
    cmp "$BATS_TEST_TMPDIR/argv-10.9" "$BATS_TEST_TMPDIR/argv-10.6"
}

@test "ssh, put, boot: 10.6 accepts ssh-rsa, as its box's Vagrantfile does, for Apple's OpenSSH 5.2; 10.9 never" {
    export STUB_SSH_UP=1
    run "$SCRIPT" ssh "$STATE" true
    [ "$status" -eq 0 ]
    run "$SCRIPT" put "$STATE" /tmp/local.txt /Users/vagrant/gate/x
    [ "$status" -eq 0 ]
    run grep -c 'ssh-rsa' "$STUB_LOG"
    [ "$output" = 0 ]
    : > "$STUB_LOG"
    echo 10.6 > "$STATE/release"
    run "$SCRIPT" ssh "$STATE" true
    [ "$status" -eq 0 ]
    run "$SCRIPT" put "$STATE" /tmp/local.txt /Users/vagrant/gate/x
    [ "$status" -eq 0 ]
    for c in ssh scp; do
        line=$(grep "^$c " "$STUB_LOG")
        [[ "$line" == *"-o HostKeyAlgorithms=+ssh-rsa -o PubkeyAcceptedAlgorithms=+ssh-rsa"* ]] || { echo "$c: $line"; false; }
    done
    # and the boot's own wait for SSH, from the box's line, before any state says so
    : > "$STUB_LOG"
    rm -f "$STATE/release"
    box_release 10.6
    run "$SCRIPT" boot "$STATE" "$BOX"
    [ "$status" -eq 0 ]
    grep -q '^ssh .*-o HostKeyAlgorithms=+ssh-rsa -o PubkeyAcceptedAlgorithms=+ssh-rsa' "$STUB_LOG"
}

@test "restart, shutdown: 10.6 through a bare /sbin/shutdown, as its box's halt trigger: it has no guest-macosx-shutdown" {
    # 10.6 has neither cause guest-macosx-shutdown stops, and its images do not carry it: its
    # shutdowns took 0.4 s, ManagedClient running (assets/guest/firstboot.sh, MEASURED 2026-10-14).
    export STUB_SSH_UP=1 STUB_BOOTTIMES="$BATS_TEST_TMPDIR/bt"
    echo 10.6 > "$STATE/release"
    printf '%s\n' 1000 2000 > "$STUB_BOOTTIMES"
    run "$SCRIPT" restart "$STATE"
    [ "$status" -eq 0 ]
    line=$(grep 'shutdown' "$STUB_LOG")
    [[ "$line" == *" sudo /sbin/shutdown -r now" ]]
    run grep -q 'guest-macosx-shutdown' "$STUB_LOG"
    [ "$status" -eq 1 ]
    : > "$STUB_LOG"
    sleep 1 & short=$!
    export STUB_PS_QEMU="$OWNED $short"
    echo "$short" > "$STATE/qemu.pid"
    run "$SCRIPT" shutdown "$STATE"
    [ "$status" -eq 0 ]
    line=$(grep 'shutdown' "$STUB_LOG")
    [[ "$line" == *" sudo /sbin/shutdown -h now" ]]
    run grep -q 'guest-macosx-shutdown' "$STUB_LOG"
    [ "$status" -eq 1 ]
    # a refusal names the command that was refused
    STUB_SHUTDOWN_RC=5 run "$SCRIPT" restart "$STATE"
    [ "$status" -eq 1 ]
    [[ "$output" == *"sudo /sbin/shutdown -r now failed in the guest (status 5)"* ]]
}

@test "ssh, put, restart, shutdown: a state dir with no release, or a release gate-vm.sh does not know, is a usage error" {
    export STUB_SSH_UP=1
    for v in none 10.4 ''; do
        if [ "$v" = none ]; then rm -f "$STATE/release"; else printf '%s\n' "$v" > "$STATE/release"; fi
        for c in ssh restart shutdown; do
            if [ "$c" = ssh ]; then run "$SCRIPT" ssh "$STATE" true; else run "$SCRIPT" "$c" "$STATE"; fi
            [ "$status" -eq 2 ] || { echo "$c '$v': $status $output"; false; }
            [[ "$output" == *"no release recorded in $STATE/release (got '${v#none}'; boot it with gate-vm.sh boot)"* ]] || { echo "$c '$v': $output"; false; }
        done
        run "$SCRIPT" put "$STATE" /tmp/a /tmp/b
        [ "$status" -eq 2 ]
    done
    [ "$(grep -c '^ssh \|^scp ' "$STUB_LOG")" -eq 0 ]
}

@test "F33: unrelated QEMU, truncated and unknown identity never receive signals" {
    for identity in unrelated truncated unknown; do
        echo "$OWNED" > "$STATE/qemu.pid"
        STUB_IDENTITY=$identity run "$SCRIPT" kill "$STATE"
        [ "$(grep -c '^kill ' "$STUB_LOG")" -eq 0 ]
        [ "$status" -ne 0 ]
        [[ "$output" == *ownership* ]]
    done
}

@test "F33: PID reused after TERM never receives KILL" {
    echo "$OWNED" > "$STATE/qemu.pid"
    STUB_REUSE=1 run "$SCRIPT" kill "$STATE"
    grep -qxF "kill -TERM $OWNED" "$STUB_LOG"
    ! grep -q 'kill -KILL' "$STUB_LOG" || false
    [ "$status" -ne 0 ]
    [ -e "$STATE/qemu.pid" ]
}

@test "F33: owned TERM exit cleans pidfile with spaces in state path" {
    mv "$STATE" "$STATE with spaces"
    STATE="$STATE with spaces"
    printf 'qemu-system-x86_64 -uuid f33.[literal]* -pidfile %s/qemu.pid\n' "$STATE" > "$STUB_LOG.args"
    echo "$OWNED" > "$STATE/qemu.pid"
    STUB_TERM_EXITS=1 run "$SCRIPT" kill "$STATE"
    [ "$status" -eq 0 ]
    grep -qxF "kill -TERM $OWNED" "$STUB_LOG"
    ! grep -q 'kill -KILL' "$STUB_LOG" || false
    [ ! -e "$STATE/qemu.pid" ]
}

@test "F33: old state without UUID refuses a live QEMU" {
    echo "$OWNED" > "$STATE/qemu.pid"
    rm "$STATE/uuid"
    run "$SCRIPT" kill "$STATE"
    [ "$status" -eq 3 ]
    ! grep -q '^kill ' "$STUB_LOG" || false
    [ -e "$STATE/qemu.pid" ]
}

@test "F33: UUID and pidfile matches are literal and complete" {
    echo "$OWNED" > "$STATE/qemu.pid"
    for args in "-uuid f33Xliteral -pidfile $STATE/qemu.pid" \
                "-uuid f33.[literal]*extra -pidfile $STATE/qemu.pid" \
                "-uuid f33.[literal]* -pidfile $STATE/qemu.pid.other"; do
        echo "qemu-system-x86_64 $args" > "$STUB_LOG.args"
        run "$SCRIPT" kill "$STATE"
        [ "$status" -eq 3 ]
        ! grep -q '^kill ' "$STUB_LOG" || false
    done
}

@test "F33: dead stale PID is removed without signals" {
    echo "$REAPED" > "$STATE/qemu.pid"
    run "$SCRIPT" kill "$STATE"
    [ "$status" -eq 0 ]
    [ ! -e "$STATE/qemu.pid" ]
    ! grep -q '^kill ' "$STUB_LOG" || false
}

@test "F33: wide command fallback still proves ownership" {
    echo "$OWNED" > "$STATE/qemu.pid"
    STUB_ARGS_UNSUPPORTED=1 STUB_TERM_EXITS=1 run "$SCRIPT" kill "$STATE"
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
    echo "$OWNED" > "$STATE/qemu.pid"
    cleanup_clock
    export GATE_KILL_WAIT=3 STUB_KILL_EXIT=delayed
    run "$SCRIPT" kill "$STATE"
    [ "$status" -eq 0 ] || { echo "$status: $output"; false; }
    grep -qxF "kill -TERM $OWNED" "$STUB_LOG"
    grep -qxF "kill -KILL $OWNED" "$STUB_LOG"
    [ ! -e "$STATE/qemu.pid" ]
    # All cleanup sleeps share the original total budget, including acknowledgment.
    [ "$(awk '/^kill -TERM / { active=1 } active && /^poll 1$/ { n++ } END { print n+0 }' "$STUB_LOG")" -eq 3 ]
}

@test "F33 followup: unconfirmed KILL exit retains PID and fails" {
    echo "$OWNED" > "$STATE/qemu.pid"
    cleanup_clock
    export GATE_KILL_WAIT=3 STUB_KILL_EXIT=never
    run "$SCRIPT" kill "$STATE"
    [ "$status" -eq 3 ] || { echo "$status: $output"; false; }
    grep -qxF "kill -TERM $OWNED" "$STUB_LOG"
    grep -qxF "kill -KILL $OWNED" "$STUB_LOG"
    [ "$(cat "$STATE/qemu.pid")" = "$OWNED" ]
    # All cleanup sleeps share the original total budget, including acknowledgment.
    [ "$(awk '/^kill -TERM / { active=1 } active && /^poll 1$/ { n++ } END { print n+0 }' "$STUB_LOG")" -eq 3 ]
}

@test "F33 followup: zero budget gives no asynchronous grace" {
    echo "$OWNED" > "$STATE/qemu.pid"
    cleanup_clock
    export GATE_KILL_WAIT=0 STUB_KILL_EXIT=delayed
    run "$SCRIPT" kill "$STATE"
    [ "$status" -eq 3 ] || { echo "$status: $output"; false; }
    grep -qxF "kill -TERM $OWNED" "$STUB_LOG"
    grep -qxF "kill -KILL $OWNED" "$STUB_LOG"
    [ "$(cat "$STATE/qemu.pid")" = "$OWNED" ]
    # All cleanup sleeps share the original total budget, including acknowledgment.
    [ "$(awk '/^kill -TERM / { active=1 } active && /^poll 1$/ { n++ } END { print n+0 }' "$STUB_LOG")" -eq 0 ]
}

@test "F33 followup: zombie after KILL is terminal" {
    echo "$OWNED" > "$STATE/qemu.pid"
    cleanup_clock
    export GATE_KILL_WAIT=3 STUB_KILL_EXIT=zombie
    run "$SCRIPT" kill "$STATE"
    [ "$status" -eq 0 ] || { echo "$status: $output"; false; }
    grep -qxF "kill -TERM $OWNED" "$STUB_LOG"
    grep -qxF "kill -KILL $OWNED" "$STUB_LOG"
    [ ! -e "$STATE/qemu.pid" ]
    # All cleanup sleeps share the original total budget, including acknowledgment.
    [ "$(awk '/^kill -TERM / { active=1 } active && /^poll 1$/ { n++ } END { print n+0 }' "$STUB_LOG")" -eq 3 ]
}

@test "F33 followup: unknown state remains conservative" {
    echo "$OWNED" > "$STATE/qemu.pid"
    cleanup_clock
    export GATE_KILL_WAIT=3 STUB_KILL_EXIT=never
    export STUB_STAT=unknown
    run "$SCRIPT" kill "$STATE"
    [ "$status" -eq 3 ] || { echo "$status: $output"; false; }
    grep -qxF "kill -TERM $OWNED" "$STUB_LOG"
    grep -qxF "kill -KILL $OWNED" "$STUB_LOG"
    [ "$(cat "$STATE/qemu.pid")" = "$OWNED" ]
    # All cleanup sleeps share the original total budget, including acknowledgment.
    [ "$(awk '/^kill -TERM / { active=1 } active && /^poll 1$/ { n++ } END { print n+0 }' "$STUB_LOG")" -eq 3 ]
}

@test "F33 followup: one-second budget reserves exactly one KILL poll" {
    echo "$OWNED" > "$STATE/qemu.pid"
    cleanup_clock
    export GATE_KILL_WAIT=1
    export STUB_KILL_EXIT=delayed
    run "$SCRIPT" kill "$STATE"
    [ "$status" -eq 0 ] || { echo "$status: $output"; false; }
    [ ! -e "$STATE/qemu.pid" ]
    [ "$(awk '/^kill -TERM / { phase=1 } /^kill -KILL / { phase=2 } /^poll 1$/ { if (phase==1) term++; if (phase==2) ack++ } END { print term+0, ack+0 }' "$STUB_LOG")" = "0 1" ]
}

@test "F33 followup: default budget reserves exactly one KILL poll" {
    echo "$OWNED" > "$STATE/qemu.pid"
    cleanup_clock
    unset GATE_KILL_WAIT
    export STUB_KILL_EXIT=delayed
    run "$SCRIPT" kill "$STATE"
    [ "$status" -eq 0 ] || { echo "$status: $output"; false; }
    [ ! -e "$STATE/qemu.pid" ]
    [ "$(awk '/^kill -TERM / { phase=1 } /^kill -KILL / { phase=2 } /^poll 1$/ { if (phase==1) term++; if (phase==2) ack++ } END { print term+0, ack+0 }' "$STUB_LOG")" = "9 1" ]
}
