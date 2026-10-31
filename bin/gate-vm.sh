#!/usr/bin/env bash
# platform: host-agnostic -- tested on Linux with KVM only
# Boot a Mac OS X box (10.9 or 10.6) under QEMU, run commands in the guest over SSH, restart it,
# shut it down. Every wait is bounded and every failure names what failed.
#
#   usage: bin/gate-vm.sh boot       <state-dir> <box-dir>   box-dir: box_0.img OVMF_CODE.fd OVMF_VARS.fd
#                                                            opencore.img Vagrantfile
#          bin/gate-vm.sh ssh        <state-dir> <command...>
#          bin/gate-vm.sh put        <state-dir> <local> <remote>
#          bin/gate-vm.sh restart    <state-dir>
#          bin/gate-vm.sh shutdown   <state-dir>
#          bin/gate-vm.sh screenshot <state-dir> <out.png>
#          bin/gate-vm.sh kill       <state-dir>
#
#   exit: 0 pass, 1 a check ran and failed, 2 usage, 3 could not run (environment)
#   env:  GATE_ACCEL kvm|hvf|nvmm (kvm), GATE_SSH_PORT (2223), GATE_SSH_TIMEOUT (600),
#         GATE_RESTART_TIMEOUT (600), GATE_SHUTDOWN_TIMEOUT (180), GATE_CMD_TIMEOUT (300),
#         GATE_CPU, GATE_MEM (4096), GATE_SMP (2 on 10.9, 1 on 10.6); seams: GATE_KVM (/dev/kvm),
#         GATE_POLL_INTERVAL (5 seconds between polls), GATE_KILL_WAIT (10 s total cleanup polling)
#
# THE RELEASE is the box's own word: its Vagrantfile's `# guest-macosx-release: <release>` line,
# which its template renders (templates/*/box.Vagrantfile.pkrtpl), read at boot and kept in
# <state-dir>/release for the commands after it. Everything below is the same for both releases
# but three things, each what that box's own Vagrantfile does, for the reason it gives
# (docs/decisions/0014, "The boot gate"):
#   - 10.6's SSH client also accepts ssh-rsa: its sshd is Apple's OpenSSH 5.2, whose host keys and
#     signatures are ssh-rsa alone, which a current client refuses (MEASURED 2026-10-04);
#   - 10.6 shuts down with a bare /sbin/shutdown: its images carry no guest-macosx-shutdown, whose
#     two causes it does not have (assets/guest/firstboot.sh; 0.4 s shutdowns, MEASURED 2026-10-14);
#   - 10.6 boots one CPU: with two, 10.6 under KVM panics on a busy host, a documented deviation
#     whose cause is not established (the owner's ruling R49, F26).
#
# The QEMU argv is what the box boots with: templates/mavericks/box.Vagrantfile.pkrtpl (q35,
# vmport=off; Penryn CPU from variables.pkr.hcl; e1000-82545em; ide-hd target; the pflash pair;
# OpenCore's disk on virtio-blk with bootindex=0; EHCI alone at 0x1d.0, with usb-kbd and usb-tablet; VGA:
# docs/decisions/0035) and mavericks-vm's scripts/boot.sh, which agree. The accelerator is `-accel`, where the Vagrantfile puts
# `accel=` in -machine: the same setting.
# Cleanup polling: GATE_KILL_WAIT=N permits at most N one-second sleeps,
# max(N-1, 0) after TERM and the remaining budget after KILL. Default 10 means
# 9 + 1; zero has no asynchronous grace. Process inspection/signal command
# overhead is additional; this is a sleep budget, not a wall-clock deadline.
set -euo pipefail

GUEST_MACOSX_REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
export GUEST_MACOSX_REPO_ROOT
GUEST_MACOSX_LOG_PREFIX=gate-vm
export GUEST_MACOSX_LOG_PREFIX
# shellcheck source=../lib/common.sh
. "$GUEST_MACOSX_REPO_ROOT/lib/common.sh"

KEY_SRC=$GUEST_MACOSX_REPO_ROOT/templates/mavericks/vagrant-standard-insecure-first-boot-only.key.rsa

accel=${GATE_ACCEL:-kvm}
port=${GATE_SSH_PORT:-2223}
ssh_timeout=${GATE_SSH_TIMEOUT:-600}
restart_timeout=${GATE_RESTART_TIMEOUT:-600}
shutdown_timeout=${GATE_SHUTDOWN_TIMEOUT:-180}
cmd_timeout=${GATE_CMD_TIMEOUT:-300}
cpu=${GATE_CPU:-Penryn,vendor=GenuineIntel,+ssse3,+sse4.1,+sse4.2}
mem=${GATE_MEM:-4096}
smp=${GATE_SMP:-}
kvm=${GATE_KVM:-/dev/kvm}
every=${GATE_POLL_INTERVAL:-5}
kill_wait=${GATE_KILL_WAIT:-10}
RELEASE=           # the box's, from boot: box_release; after it, state_release

usage() {
    printf 'usage: %s boot|ssh|put|restart|shutdown|screenshot|kill <state-dir> [args...]\n' "${0##*/}" >&2
    exit 2
}

# The guest's pid from <state>/qemu.pid, when it is a number.
pid_of() {
    local p
    p=$(cat "$1/qemu.pid" 2>/dev/null) || return 1
    case $p in ''|*[!0-9]*) return 1 ;; esac
    # 0 would signal the caller's process group, 1 is init: neither is ever QEMU
    [ "$p" -gt 1 ] || return 1
    printf '%s\n' "$p"
}

# is_qemu <pid>: the process is QEMU. A stale pidfile's pid can be reused by anything.
is_qemu() { ps -p "$1" -o comm= 2>/dev/null | grep -q qemu; }

# alive <pid>: a live QEMU, for polling only; signaling also requires owned_qemu.
alive() { kill -0 "$1" 2>/dev/null && is_qemu "$1"; }

# A denied kill -0 is not proof of exit (a stale PID may belong to another user).
process_present() {
    local stat
    # ps stat on Linux/macOS/NetBSD: a zombie has exited even while its
    # parent has yet to reap it. Missing/unknown state is never proof of exit.
    stat=$(ps -p "$1" -o stat= 2>/dev/null) || stat=
    read -r stat <<< "$stat"
    case $stat in Z*) return 1 ;; esac
    env kill -0 "$1" 2>/dev/null || ps -p "$1" -o pid= >/dev/null 2>&1
}

# Wide argv is portable across Linux, macOS and NetBSD. Match literal option/value
# boundaries (not regexes); comm alone cannot distinguish another run's QEMU.
owned_qemu() {
    local args
    [ -n "$uuid" ] || return 1
    is_qemu "$1" || return 1
    args=$(ps -ww -p "$1" -o args= 2>/dev/null) ||
        args=$(ps -ww -p "$1" -o command= 2>/dev/null) || return 1
    case " $args " in *" -uuid $uuid "*) ;; *) return 1 ;; esac
    case " $args " in *" -pidfile $state/qemu.pid "*) ;; *) return 1 ;; esac
}

# The socket path of a unix socket is limited (108 bytes on Linux, 104 on macOS), and a state
# dir deep in a work tree can exceed it, so QMP lives in a short directory of its own.
qmp_sock() {
    local d
    d=$(cat "$1/qmp.dir" 2>/dev/null) || return 1
    printf '%s/qmp.sock\n' "$d"
}
qmp_cleanup() {
    local d
    d=$(cat "$1/qmp.dir" 2>/dev/null) || return 0
    if is_gate_qmp_dir "$d"; then
        rm -rf "$d"
    else
        warn "not removing $d: not a QMP directory gate-vm.sh made"
    fi
    rm -f "$1/qmp.dir"
}

# box_release <box>: the release its Vagrantfile names, on stdout; otherwise a warning naming what
# is wrong, and status 1.
box_release() {
    local vf=$1/Vagrantfile lines n
    [ -f "$vf" ] || { warn "the box has no Vagrantfile ($1)"; return 1; }
    n=$(grep -c '^# guest-macosx-release: ' "$vf" || true)
    lines=$(sed -n 's/^# guest-macosx-release: //p' "$vf")
    case $n in
        0) warn "$vf names no release (no '# guest-macosx-release: ' line): a box built before the gate knew releases; rebuild it"; return 1 ;;
        1) ;;
        *) warn "$vf names more than one release: $(printf '%s' "$lines" | tr '\n' ' ')"; return 1 ;;
    esac
    known_release "$lines" || { warn "$vf names release '$lines'; gate-vm.sh knows $(releases_said)"; return 1; }
    printf '%s\n' "$lines"
}

# state_release <state>: the release its boot recorded; a usage error without one, since nothing
# about that guest is known.
state_release() {
    local r
    r=$(cat "$1/release" 2>/dev/null) || r=
    known_release "$r" || { warn "no release recorded in $1/release (got '$r'; boot it with gate-vm.sh boot)"; exit 2; }
    RELEASE=$r
}

# sshopts <state>: SSHOPTS for ssh and scp, by RELEASE.
sshopts() {
    SSHOPTS=(-i "$1/ssh-key" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null
             -o IdentitiesOnly=yes -o ConnectTimeout=10 -o LogLevel=ERROR
             -o ServerAliveInterval=15 -o ServerAliveCountMax=8)
    # 10.6 alone: Apple's OpenSSH 5.2 offers ssh-rsa (SHA-1) host keys and signatures and nothing
    # newer, which a current client refuses unless told; its box's Vagrantfile tells `vagrant ssh`
    # the same (templates/snowleopard/box.Vagrantfile.pkrtpl).
    if [ "$RELEASE" = 10.6 ]; then
        SSHOPTS+=(-o HostKeyAlgorithms=+ssh-rsa -o PubkeyAcceptedAlgorithms=+ssh-rsa)
    fi
}

# gsh <state> <seconds> <command...>: the guest, bounded by `timeout`.
gsh() {
    local state=$1 secs=$2
    shift 2
    sshopts "$state"
    timeout "$secs" ssh "${SSHOPTS[@]}" -p "$port" vagrant@127.0.0.1 "$@"
}

# boottime <state>: the guest's kern.boottime seconds on stdout; fails when it cannot be read.
boottime() {
    local out sec
    out=$(gsh "$1" 30 sysctl -n kern.boottime 2>/dev/null) || return 1
    # "{ sec = 1760263200, usec = 0 } Sun Oct 12 ..." -- anchored so usec never matches
    sec=$(printf '%s\n' "$out" | sed -n 's/.*[{ ]sec = \([0-9][0-9]*\).*/\1/p' | head -n 1)
    [ -n "$sec" ] || return 1
    printf '%s\n' "$sec"
}

need_state() {
    [ -d "$1" ] || { warn "no state directory: $1"; exit 2; }
}

abs_dir() { ( cd "$1" && pwd ); }

# guest_down <state> -r|-h: ask the guest to shut down. The connection dropping (255) or the
# command timing out (124) is expected; anything else is a refusal worth stopping for.
#
# Through the box's own /usr/local/sbin/guest-macosx-shutdown (assets/guest/firstboot.sh), never a bare
# `shutdown`: launchd's userspace shutdown waits out a job's 20 s exit timeout, and the shutdown
# monitor files a .shutdownStall report, when loginwindow's ManagedClient still holds a
# DiskArbitration session, or when UserEventAgent still has a preference to write and cfprefsd is
# already gone, or when a job is still starting. The helper stops the first two, UserEventAgent
# while cfprefsd still runs (docs/decisions/0019), and first waits out the third (0021).
#
# 105 s: the helper waits at most 30 s for jobs still starting, 30 s for UserEventAgent (System)
# to go and 30 s more for any other UserEventAgent or AppleIDAuthAgent to settle, then shuts
# down; 15 s more covers the rest. What it says on the way (the line each bounded wait ends in)
# is logged here, so a slow guest reads as one.
#
# 10.6 alone: a bare `sudo /sbin/shutdown -r|-h now`, as its box's halt trigger. Its images carry
# no guest-macosx-shutdown: it has neither cause (no cfprefsd, no XPC activity scheduler), and its
# shutdowns 70 s after boot, ManagedClient running, took 0.4 s (assets/guest/firstboot.sh, MEASURED
# 2026-10-14). The same 105 s bounds it: a limit, never a wait.
guest_down() {
    local out rc=0 cmd
    if [ "$RELEASE" = 10.6 ]; then
        cmd="sudo /sbin/shutdown $2 now"
    else
        cmd="sudo /usr/local/sbin/guest-macosx-shutdown $2"
    fi
    out=$(gsh "$1" 105 "$cmd" 2>&1) || rc=$?
    case $rc in
        0|124|255) [ -z "$out" ] || log "the guest said: $out" ;;
        *) warn "$cmd failed in the guest (status $rc): $out"; exit 1 ;;
    esac
}

cmd_boot() {
    [ "$#" -eq 2 ] || usage
    local state box f p start qdir uuid mac
    mkdir -p "$1" 2>/dev/null || { warn "cannot create the state directory $1"; exit 3; }
    state=$(abs_dir "$1") || { warn "cannot enter the state directory $1"; exit 3; }
    box=$(abs_dir "$2" 2>/dev/null) || { warn "no box directory: $2"; exit 3; }
    if p=$(pid_of "$state"); then
        if alive "$p"; then
            warn "QEMU is already running (pid $p, $state/qemu.pid); never two guests on one overlay"
            exit 3
        elif kill -0 "$p" 2>/dev/null; then
            warn "stale qemu.pid names pid $p, which is not QEMU"
        fi
    fi
    for f in box_0.img OVMF_CODE.fd OVMF_VARS.fd opencore.img; do
        [ -f "$box/$f" ] || { warn "the box has no $f ($box)"; exit 3; }
    done
    RELEASE=$(box_release "$box") || exit 3
    # One CPU on 10.6 (R49), two on 10.9, unless GATE_SMP says otherwise.
    if [ -z "$smp" ]; then
        if [ "$RELEASE" = 10.6 ]; then smp=1; else smp=2; fi
    fi
    case $accel in
        kvm) [ -w "$kvm" ] || { warn "$kvm is not writable: this host cannot run KVM"; exit 3; } ;;
        hvf|nvmm) ;;
        *) warn "GATE_ACCEL wants kvm, hvf or nvmm (got $accel)"; exit 3 ;;
    esac
    install -m 0600 "$KEY_SRC" "$state/ssh-key" || { warn "cannot install the ssh key in $state"; exit 3; }
    printf '%s\n' "$RELEASE" > "$state/release" || { warn "cannot write $state/release"; exit 3; }
    log "the box is Mac OS X $RELEASE ($box/Vagrantfile): $smp CPU(s)"
    rm -f "$state/qemu.pid"
    qmp_cleanup "$state"
    qdir=$(mktemp -d /tmp/gate-qmp.XXXXXX) || { warn "cannot create a QMP socket directory in /tmp"; exit 3; }
    printf '%s\n' "$qdir" > "$state/qmp.dir"
    # A host UUID of its own, fresh at every boot (each boot is a new guest on a new overlay), kept
    # in the state dir for the evidence: QEMU's -uuid, which OpenCore makes the guest's system-id
    # (SystemUUID OEM), so the guest has it before its first daemon asks (docs/decisions/0024).
    uuid=$(new_uuid) || { warn "cannot make a UUID for QEMU's -uuid (no uuidgen, no /dev/urandom?)"; exit 3; }
    printf '%s\n' "$uuid" > "$state/uuid" || { warn "cannot write $state/uuid"; exit 3; }
    # And a NIC address of its own, derived from it as the box Vagrantfiles derive theirs: every
    # machine, distinct, never QEMU's one default.
    mac=$(derived_mac "$uuid") || { warn "cannot derive a NIC address from $uuid (no sha256sum or shasum?)"; exit 3; }
    log "QEMU's -uuid: $uuid; the NIC's address: $mac"
    qemu-img create -f qcow2 -F qcow2 -b "$box/box_0.img" "$state/overlay.qcow2" >/dev/null \
        || { warn "could not create the overlay over $box/box_0.img"; exit 3; }
    qemu-system-x86_64 \
        -name gate -uuid "$uuid" -machine q35,vmport=off -accel "$accel" \
        -cpu "$cpu" -m "${mem}M" -smp "$smp" -parallel none \
        -drive "if=pflash,format=raw,unit=0,readonly=on,file=$box/OVMF_CODE.fd" \
        -drive "if=pflash,format=raw,unit=1,snapshot=on,file=$box/OVMF_VARS.fd" \
        -drive "id=target,if=none,format=qcow2,detect-zeroes=unmap,discard=unmap,file=$state/overlay.qcow2" \
        -device ide-hd,bus=ide.0,drive=target \
        -drive "id=opencore,if=none,format=raw,snapshot=on,file=$box/opencore.img" \
        -device virtio-blk-pci,drive=opencore,bootindex=0,bus=pcie.0,addr=0x10 \
        -netdev "user,id=net0,hostfwd=tcp:127.0.0.1:$port-:22" -device "e1000-82545em,netdev=net0,mac=$mac" \
        -device ich9-usb-ehci1,id=ehci,bus=pcie.0,addr=0x1d.0 -device usb-kbd,bus=ehci.0 -device usb-tablet,bus=ehci.0 \
        -device VGA,vgamem_mb=64 \
        -display none -serial "file:$state/serial.log" \
        -qmp "unix:$qdir/qmp.sock,server=on,wait=off" \
        -daemonize -pidfile "$state/qemu.pid" \
        || { warn "QEMU did not start"; exit 3; }
    p=$(pid_of "$state") || { warn "QEMU started but wrote no pid to $state/qemu.pid"; exit 3; }

    start=$SECONDS
    until gsh "$state" 30 true >/dev/null 2>&1; do
        if [ $((SECONDS - start)) -ge "$ssh_timeout" ]; then
            warn "boot: SSH did not answer within ${ssh_timeout}s; the end of the serial log follows"
            tail -n 40 "$state/serial.log" >&2 2>/dev/null || true
            exit 1
        fi
        if ! alive "$p"; then
            warn "boot: QEMU (pid $p) exited before SSH answered; the end of the serial log follows"
            tail -n 40 "$state/serial.log" >&2 2>/dev/null || true
            exit 1
        fi
        sleep "$every"
    done
    log "the guest answered SSH after $((SECONDS - start))s"
}

cmd_ssh() {
    [ "$#" -ge 2 ] || usage
    need_state "$1"
    local state rc=0
    state=$(abs_dir "$1")
    state_release "$state"
    shift
    gsh "$state" "$cmd_timeout" "$@" || rc=$?
    if [ "$rc" -eq 124 ]; then
        warn "ssh: the command did not finish within ${cmd_timeout}s: $*"
    fi
    return "$rc"
}

cmd_put() {
    [ "$#" -eq 3 ] || usage
    need_state "$1"
    local state
    state=$(abs_dir "$1")
    state_release "$state"
    sshopts "$state"
    timeout "$cmd_timeout" scp "${SSHOPTS[@]}" -P "$port" "$2" "vagrant@127.0.0.1:$3"
}

cmd_restart() {
    [ "$#" -eq 1 ] || usage
    need_state "$1"
    local state before now start answered_same=false
    state=$(abs_dir "$1")
    state_release "$state"
    before=$(boottime "$state") || { warn "restart: cannot read kern.boottime before the restart"; exit 1; }
    # the connection drops as the guest goes down; that is not a failure
    guest_down "$state" -r
    start=$SECONDS
    while :; do
        if now=$(boottime "$state"); then
            # an NTP step moves kern.boottime a little without a restart
            if [ "$now" -gt $((before + 10)) ]; then
                log "restart: the guest is on a new boot after $((SECONDS - start))s"
                return 0
            fi
            answered_same=true
        else
            answered_same=false
        fi
        if [ $((SECONDS - start)) -ge "$restart_timeout" ]; then
            if [ "$answered_same" = true ]; then
                warn "restart: the guest answered on the same boot (did not restart)"
            else
                warn "restart: SSH never came back within ${restart_timeout}s"
            fi
            exit 1
        fi
        sleep "$every"
    done
}

cmd_shutdown() {
    [ "$#" -eq 1 ] || usage
    need_state "$1"
    local state p start
    state=$(abs_dir "$1")
    state_release "$state"
    p=$(pid_of "$state") || { warn "shutdown: no qemu.pid in $state"; exit 3; }
    guest_down "$state" -h
    start=$SECONDS
    while kill -0 "$p" 2>/dev/null; do
        if [ $((SECONDS - start)) -ge "$shutdown_timeout" ]; then
            warn "shutdown: QEMU still running after ${shutdown_timeout}s"
            exit 1
        fi
        sleep "$every"
    done
    rm -f "$state/qemu.pid"
    qmp_cleanup "$state"
    log "shutdown: QEMU exited after $((SECONDS - start))s"
}

cmd_screenshot() {
    [ "$#" -eq 2 ] || usage
    # never fatal: a screenshot is evidence, not a check
    [ -d "$1" ] || { warn "screenshot: no state directory: $1"; return 0; }
    local state out sock
    state=$(abs_dir "$1")
    case $2 in /*) out=$2 ;; *) out=$PWD/$2 ;; esac
    sock=$(qmp_sock "$state") || { warn "screenshot: no QMP socket recorded in $state"; return 0; }
    # QEMU writes the file itself, so the path must be absolute
    timeout 30 python3 -I - "$sock" "$out" <<'PY' || warn "screenshot: could not capture $out"
import json, socket, sys
sock, out = sys.argv[1], sys.argv[2]
s = socket.socket(socket.AF_UNIX)
s.settimeout(10)
s.connect(sock)
f = s.makefile("rw")
json.loads(f.readline())
def call(cmd, **args):
    f.write(json.dumps({"execute": cmd, "arguments": args}) + "\n")
    f.flush()
    while True:
        r = json.loads(f.readline())
        if "return" in r or "error" in r:
            return r
call("qmp_capabilities")
r = call("screendump", filename=out, format="png")
if "error" in r:
    sys.exit(r["error"].get("desc", "screendump failed"))
PY
    return 0
}

cmd_kill() {
    [ "$#" -eq 1 ] || usage
    kill_qemu "$1"
    qmp_cleanup "$1"
    return 0
}

kill_qemu() {
    local p i=0 term_wait=$kill_wait state uuid
    p=$(pid_of "$1") || return 0
    state=$(abs_dir "$1") || return 3
    uuid=$(cat "$state/uuid" 2>/dev/null) || uuid=
    # Recheck ownership immediately before each signal, including escalation:
    # the PID may have been reused while we waited for TERM.
    if ! process_present "$p"; then
        rm -f "$state/qemu.pid"
        return 0
    fi
    owned_qemu "$p" || { warn "cannot establish QEMU ownership for pid $p; refusing to signal"; return 3; }
    env kill -TERM "$p" 2>/dev/null || true
    # One total sleep budget: reserve its last second for KILL acknowledgment.
    # Zero explicitly allows no asynchronous grace; observations still occur.
    [ "$term_wait" -eq 0 ] || term_wait=$((term_wait - 1))
    while process_present "$p" && [ "$i" -lt "$term_wait" ]; do
        sleep 1
        i=$((i + 1))
    done
    if process_present "$p"; then
        owned_qemu "$p" || { warn "cannot establish QEMU ownership for pid $p; refusing to signal"; return 3; }
        env kill -KILL "$p" 2>/dev/null || true
        while process_present "$p" && [ "$i" -lt "$kill_wait" ]; do
            sleep 1
            i=$((i + 1))
        done
    fi
    if process_present "$p"; then
        warn "QEMU pid $p has not exited; retaining pidfile"
        return 3
    fi
    rm -f "$state/qemu.pid"
}

[ "$#" -ge 2 ] || usage
cmd=$1
shift
case $cmd in
    boot) cmd_boot "$@" ;;
    ssh) cmd_ssh "$@" ;;
    put) cmd_put "$@" ;;
    restart) cmd_restart "$@" ;;
    shutdown) cmd_shutdown "$@" ;;
    screenshot) cmd_screenshot "$@" ;;
    kill) cmd_kill "$@" ;;
    *) usage ;;
esac
