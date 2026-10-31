#!/usr/bin/env bash
# platform: host-agnostic -- tested on Linux with KVM only
# The gate's varstore guard: boot a box's firmware alone on a nearly full UEFI variable store and
# check the store's reclaim stays cheap (docs/decisions/0032).
#
# OVMF's boot manager rewrites its console variables on every boot, so a store that lives across
# warm restarts fills, and at the 61st the variable driver reclaims it. Before patch 0006 that
# reclaim programmed about 800,000 flash bytes one at a time, and under QEMU it held a restart
# 41-57 s, or minutes on a busy host. Here the box's own OVMF_VARS.fd is copied with all but
# GATE_VARSTORE_FREE bytes of its store taken by deleted records, so the firmware's first boot on
# it reclaims at once; QEMU runs the box's OVMF_CODE.fd with no guest disk and no OpenCore (no
# Apple bytes), until OVMF's first "BdsDxe:" line on the serial port.
#
# How it measures, and it says which:
#   trace   QEMU's pflash_data_write trace event, one line per programmed byte (the `log` trace
#           backend, QEMU's default): fail when more than GATE_VARSTORE_LIMIT bytes were
#           programmed before that line. With 0006 a reclaim programs about 9,000; without,
#           about 800,000.
#   timing  when this QEMU has no such event, or recorded none: the same boot on the box's own,
#           pristine store and on a freshly filled one, each twice, alternately; fail when the
#           full store's faster boot reaches "BdsDxe:" more than GATE_VARSTORE_SLOWER seconds
#           after the pristine store's faster one (with 0006, about 1 s against 1 s idle;
#           without, 31-33 s against 1 s on this project's KVM host). It can only see a reclaim
#           that costs the host more than GATE_VARSTORE_SLOWER seconds; trace has no such limit.
#
#   usage: bin/gate-varstore.sh <box-dir> <state-dir>
#          box-dir holds OVMF_CODE.fd and OVMF_VARS.fd; state-dir is created, and must not exist
#          or be empty
#   exit:  0 pass, 1 the check ran and failed, 2 usage, 3 could not run (environment)
#   env:   GATE_ACCEL kvm|hvf|nvmm (kvm), GATE_VARSTORE_LIMIT (100000 bytes),
#          GATE_VARSTORE_FREE (2000 bytes), GATE_VARSTORE_TIMEOUT (120 s, each boot to "BdsDxe:"),
#          GATE_VARSTORE_SLOWER (10 s), GATE_KILL_WAIT (10 s total cleanup polling)
#
# On TERM or INT it says so, stops its QEMU and exits 3. Any other fatal
# signal (HUP, when the session running the gate goes away) still stops
# its QEMU, through the EXIT trap.
#
# Portable to the hosts the gate names (Linux KVM, macOS HVF, NetBSD nvmm): POSIX tools, bash,
# python3 and QEMU; no /proc, no GNU-only flags.
# Cleanup polling: GATE_KILL_WAIT=N permits at most N one-second sleeps,
# max(N-1, 0) after TERM and the remaining budget after KILL. Default 10 means
# 9 + 1; zero has no asynchronous grace. Process inspection/signal command
# overhead is additional; this is a sleep budget, not a wall-clock deadline.
set -euo pipefail

GUEST_MACOSX_REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
GUEST_MACOSX_LOG_PREFIX=gate-varstore
export GUEST_MACOSX_REPO_ROOT GUEST_MACOSX_LOG_PREFIX
# shellcheck source=../lib/common.sh
. "$GUEST_MACOSX_REPO_ROOT/lib/common.sh"

accel=${GATE_ACCEL:-kvm}
limit=${GATE_VARSTORE_LIMIT:-100000}
free=${GATE_VARSTORE_FREE:-2000}
bds_timeout=${GATE_VARSTORE_TIMEOUT:-120}
slower=${GATE_VARSTORE_SLOWER:-10}
kill_wait=${GATE_KILL_WAIT:-10}

usage() {
    printf 'usage: %s <box-dir> <state-dir>\n' "${0##*/}" >&2
    exit 2
}
[ "$#" -eq 2 ] || usage
box=$1 state=$2
for n in "$limit" "$free" "$bds_timeout" "$slower" "$kill_wait"; do
    case $n in ''|*[!0-9]*) warn "a limit wants a whole number (got '$n')"; exit 2 ;; esac
done
case $accel in kvm|hvf|nvmm) ;; *) warn "GATE_ACCEL wants kvm, hvf or nvmm (got $accel)"; exit 2 ;; esac
for f in OVMF_CODE.fd OVMF_VARS.fd; do
    [ -f "$box/$f" ] || { warn "the box has no $f ($box)"; exit 3; }
done
if [ -e "$state" ] && [ -n "$(ls -A "$state" 2>/dev/null)" ]; then
    warn "the state directory $state is not empty"
    exit 2
fi
mkdir -p "$state" || { warn "cannot create $state"; exit 3; }
state=$(cd "$state" && pwd -P)
box=$(cd "$box" && pwd -P)

is_qemu() { ps -p "$1" -o comm= 2>/dev/null | grep -q qemu; }
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

uuid=

# stop_qemu: verify this boot's UUID and pidfile before TERM and KILL.
stop_qemu() {
    local p i=0 term_wait=$kill_wait
    p=$(cat "$state/qemu.pid" 2>/dev/null) || return 0
    case $p in ''|*[!0-9]*) return 0 ;; esac
    [ "$p" -gt 1 ] || return 0
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
trap 'stop_qemu' EXIT
trap 'warn "interrupted: stopping this run'"'"'s QEMU"; trap - EXIT; stop_qemu; exit 3' INT TERM

# The box's store, all but $free bytes of it taken by deleted 4 KiB records (state 0x3C, as a
# superseded ConOut leaves them). The live variables are kept, so the firmware boots as from the
# box's own store, and its first write that does not fit reclaims.
fill() {
    python3 -I - "$box/OVMF_VARS.fd" "$state/vars-full.fd" "$free" <<'PY'
import struct, sys
src, dst, want = sys.argv[1], sys.argv[2], int(sys.argv[3])
d = bytearray(open(src, "rb").read())
store = struct.unpack_from("<H", d, 0x30)[0]          # the firmware volume header's length
auth = bytes.fromhex("782cf3aa7b949a43a1802e144ec37792")
if bytes(d[store:store + 16]) != auth:
    sys.exit("%s: not an authenticated variable store" % src)
end = store + struct.unpack_from("<I", d, store + 16)[0]
p = store + 28
while p + 60 <= end and struct.unpack_from("<H", d, p)[0] == 0x55AA:
    ns, ds = struct.unpack_from("<II", d, p + 36)
    p += (60 + ns + ds + 3) & ~3
if any(b != 0xFF for b in d[p:end]):
    sys.exit("%s: the store's free space is not erased" % src)
name = "GateFill\0".encode("utf-16-le")
guid = bytes.fromhex("f4f4f4f400000040800000000000f400")
while end - p - want >= 64:
    tot = min(4096, end - p - want) & ~3
    ds = tot - 60 - len(name)
    hdr = struct.pack("<HBBIQ16sIII16s", 0x55AA, 0x3C, 0, 7, 0, b"\0" * 16, 0, len(name), ds, guid)
    d[p:p + tot] = hdr + name + b"\xa5" * ds
    p += tot
open(dst, "wb").write(d)
print("the store: %d of %d bytes free" % (end - p, end - store - 28))
PY
}
fill || { warn "could not fill a copy of $box/OVMF_VARS.fd"; exit 3; }

# boot <vars-file> <tag> [trace]: QEMU on the box's code and <vars-file>, until "BdsDxe:" on the
# serial port or $bds_timeout s; sets secs (to the line, or empty) and stops QEMU.
boot() {
    local vars=$1 tag=$2 t0 tr=()
    [ "${3:-}" != trace ] || tr=(-trace enable=pflash_data_write -D "$state/trace-$tag.log")
    rm -f "$state/qemu.pid" "$state/serial-$tag.log"
    : > "$state/serial-$tag.log"
    uuid=$(new_uuid) || { warn "cannot make a run UUID"; exit 3; }
    t0=$SECONDS
    qemu-system-x86_64 -name gate-varstore -uuid "$uuid" -machine q35 -accel "$accel" -m 512 -smp 1 \
        -nodefaults -display none -net none -parallel none \
        -drive "if=pflash,format=raw,unit=0,readonly=on,file=$box/OVMF_CODE.fd" \
        -drive "if=pflash,format=raw,unit=1,file=$vars" \
        -serial "file:$state/serial-$tag.log" ${tr[@]+"${tr[@]}"} \
        -daemonize -pidfile "$state/qemu.pid" 2>> "$state/qemu.err" \
        || { warn "QEMU did not start (see $state/qemu.err): $(tail -n 1 "$state/qemu.err" 2>/dev/null)"; exit 3; }
    secs=
    while [ $((SECONDS - t0)) -lt "$bds_timeout" ]; do
        if grep -q 'BdsDxe:' "$state/serial-$tag.log" 2>/dev/null; then
            secs=$((SECONDS - t0))
            break
        fi
        p=$(cat "$state/qemu.pid" 2>/dev/null || true)
        case $p in ''|*[!0-9]*) ;; *) alive "$p" || { warn "QEMU exited before OVMF's \"BdsDxe:\" line"; exit 3; } ;; esac
        sleep 1
    done
    stop_qemu || { trap - EXIT; exit 3; }
}

method=timing
# Drain help even after a match: grep -q can give QEMU a broken pipe,
# making pipefail select timing despite the trace event being present.
if qemu-system-x86_64 -trace help 2>/dev/null | grep -x pflash_data_write > /dev/null; then
    method=trace
fi

if [ "$method" = trace ]; then
    boot "$state/vars-full.fd" full trace
    bytes=$(grep -c pflash_data_write "$state/trace-full.log" 2>/dev/null || true)
    bytes=${bytes:-0}
    if [ "$bytes" -eq 0 ] && [ -n "$secs" ]; then
        log "QEMU names the pflash_data_write trace event but recorded none (another trace backend?): timing instead"
        method=timing
    fi
fi

if [ "$method" = trace ]; then
    log "method: trace (QEMU's pflash_data_write events, one per programmed byte)"
    if [ -z "$secs" ]; then
        if [ "$bytes" -eq 0 ]; then
            so_far="the trace recorded none"
        else
            so_far="$bytes flash bytes programmed by then (limit $limit)"
        fi
        warn "FAIL: no \"BdsDxe:\" line within ${bds_timeout}s on a nearly full variable store ($so_far): a reclaim programming every byte (docs/decisions/0032)"
        exit 1
    fi
    if [ "$bytes" -gt "$limit" ]; then
        warn "FAIL: $bytes flash bytes programmed before \"BdsDxe:\" on a nearly full variable store, more than $limit: the reclaim programs every byte again (docs/decisions/0032)"
        exit 1
    fi
    log "pass: $bytes flash bytes programmed before \"BdsDxe:\" (${secs}s) on a nearly full variable store (limit $limit)"
    exit 0
fi

log "method: timing (this QEMU records no pflash_data_write events): the box's own store and the nearly full one, each twice, alternately; the faster of each compared"
cp "$box/OVMF_VARS.fd" "$state/vars-pristine.fd" || { warn "cannot copy $box/OVMF_VARS.fd"; exit 3; }
base=
full=
for round in 1 2; do
    boot "$state/vars-pristine.fd" "pristine-$round"
    [ -n "$secs" ] || { warn "FAIL: no \"BdsDxe:\" line within ${bds_timeout}s on the box's own store: the firmware does not boot"; exit 1; }
    log "the box's own store, boot $round: \"BdsDxe:\" after ${secs}s"
    if [ -z "$base" ] || [ "$secs" -lt "$base" ]; then base=$secs; fi
    # a fresh fill before every full boot: each one reclaims its store, and so did a trace boot above
    fill > /dev/null || { warn "could not fill a copy of $box/OVMF_VARS.fd"; exit 3; }
    boot "$state/vars-full.fd" "full-$round"
    if [ -z "$secs" ]; then
        warn "FAIL: no \"BdsDxe:\" line within ${bds_timeout}s on a nearly full variable store (${base}s on the box's own): a reclaim programming every byte (docs/decisions/0032)"
        exit 1
    fi
    log "the nearly full store, boot $round: \"BdsDxe:\" after ${secs}s"
    if [ -z "$full" ] || [ "$secs" -lt "$full" ]; then full=$secs; fi
done
if [ "$full" -gt $((base + slower)) ]; then
    warn "FAIL: \"BdsDxe:\" after ${full}s at best on a nearly full variable store against ${base}s at best on the box's own, more than ${slower}s slower: the reclaim programs every byte again (docs/decisions/0032)"
    exit 1
fi
log "pass: \"BdsDxe:\" after ${full}s at best on a nearly full variable store, ${base}s at best on the box's own (at most ${slower}s slower)"
exit 0
