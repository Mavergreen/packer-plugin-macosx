#!/usr/bin/env bats
# platform: host-agnostic -- tested on Linux with KVM only
#
# bin/gate-build-evidence.sh reads what a failed build leaves, for bin/gate.sh: the guest's screen
# over the VNC port Packer names (screenshot), and the guest's logs from its disk with no root and
# no boot (disk). Evidence only: whatever it cannot get, it says so and exits 0. Here the VNC server
# is a few lines of Python speaking RFB 3.8; qemu-img, qemu-storage-daemon, 7z and fusermount3 are
# stand-ins; python3 is the host's. PATH is the stub directory, /usr/bin and /bin (no sbin).

bats_require_minimum_version 1.5.0

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    SCRIPT="$REPO/bin/gate-build-evidence.sh"
    STUBS="$BATS_TEST_TMPDIR/stubs"
    OUT="$BATS_TEST_TMPDIR/out"
    export STUB_LOG="$BATS_TEST_TMPDIR/stub.log"
    export TMPDIR="$BATS_TEST_TMPDIR/tmp"
    mkdir -p "$STUBS" "$TMPDIR"
    : > "$STUB_LOG"
    SERVER=
}

teardown() {
    [ -z "${SERVER:-}" ] || kill "$SERVER" 2>/dev/null || true
    [ -z "${DAEMON:-}" ] || kill "$DAEMON" 2>/dev/null || true
}

# vnc_server <w> <h>: a VNC server on a free port ($PORT), one frame of w x h pixels, pixel (x, y)
# = BGRA (x, y, 7, 0) as QEMU's format; it serves one client and exits.
vnc_server() {
    python3 -I - "$1" "$2" "$BATS_TEST_TMPDIR/port" <<'PY' 3>&- &
import socket, struct, sys
w, h = int(sys.argv[1]), int(sys.argv[2])
srv = socket.socket(); srv.bind(("127.0.0.1", 0)); srv.listen(1)
open(sys.argv[3], "w").write(str(srv.getsockname()[1]))
c, _ = srv.accept()
f = c.makefile("rwb", buffering=0)
f.write(b"RFB 003.008\n"); f.read(12)
f.write(bytes([1, 1])); f.read(1)
f.write(struct.pack(">I", 0))
f.read(1)
name = b"QEMU"
f.write(struct.pack(">HH", w, h) + struct.pack(">BBBBHHHBBBxxx", 32, 24, 0, 1, 255, 255, 255, 16, 8, 0) + struct.pack(">I", len(name)) + name)
f.read(20)  # SetPixelFormat
f.read(8)   # SetEncodings (one)
f.read(10)  # FramebufferUpdateRequest
px = b"".join(bytes((7, y, x, 0)) for y in range(h) for x in range(w))
f.write(struct.pack(">BxH", 0, 1) + struct.pack(">HHHHi", 0, 0, w, h, 0) + px)
import time; time.sleep(1)
PY
    SERVER=$!
    for _ in $(seq 100); do [ -s "$BATS_TEST_TMPDIR/port" ] && break; sleep 0.05; done
    PORT=$(cat "$BATS_TEST_TMPDIR/port")
}

# qmp_server: a QMP server on the unix socket $SOCK, for one client. It records every command (with
# the time it came) in $QMP_LOG. query-status says running; query-blockstats and each human-monitor
# "info ..." carry a counter that goes up with every call, so two reads differ; screendump writes
# the file it was named. QMP_BAD=<command> answers that command with an error; QMP_MUTE=1 accepts
# and says nothing (a QEMU that does not answer); QMP_NOPNG=1 refuses screendump's format argument; QMP_PPM=empty|header|short makes the PPM it then writes truncated.
qmp_server() {
    SOCK=${1:-$BATS_TEST_TMPDIR}/q.sock
    QMP_LOG=$BATS_TEST_TMPDIR/qmp.log
    : > "$QMP_LOG"
    python3 -I - "$SOCK" "$QMP_LOG" <<'PY' 3>&- &
import json, os, socket, sys, time
path, log = sys.argv[1:3]
srv = socket.socket(socket.AF_UNIX)
os.chdir(os.path.dirname(path)); srv.bind(os.path.basename(path)); srv.listen(1)
c, _ = srv.accept()
if os.environ.get("QMP_MUTE"):
    time.sleep(60)
    sys.exit(0)
f = c.makefile("rwb", buffering=0)
def send(o): f.write(json.dumps(o).encode() + b"\r\n")
send({"QMP": {"version": {"qemu": {"major": 9}}, "capabilities": ["oob"]}})
n = 0
bad = os.environ.get("QMP_BAD")
for line in f:
    req = json.loads(line)
    cmd, args = req["execute"], req.get("arguments", {})
    shown = cmd + (" " + args["command-line"] if cmd == "human-monitor-command" else "")
    with open(log, "a") as l:
        l.write("%.3f %s\n" % (time.time(), shown))
    n += 1
    if cmd == "qmp_capabilities":
        send({"return": {}})
    elif shown == bad or cmd == bad:
        send({"error": {"class": "GenericError", "desc": "no such thing"}})
    elif cmd == "query-status":
        send({"return": {"status": "running", "running": True}})
    elif cmd == "query-cpus-fast":
        send({"return": [{"cpu-index": 0, "thread-id": 1}, {"cpu-index": 1, "thread-id": 2}]})
    elif cmd == "query-pci" and not os.environ.get("QMP_NO_SATA"):
        send({"return": [{"bus": 0, "devices": [
            {"bus": 0, "slot": 2, "function": 0, "class_info": {"class": 768, "desc": "VGA controller"}, "regions": [{"bar": 0, "type": "memory", "address": 4026531840, "size": 67108864}]},
            {"bus": 0, "slot": 31, "function": 2, "class_info": {"class": 262, "desc": "SATA controller"},
             "regions": [{"bar": 4, "type": "io", "address": 24832, "size": 32}, {"bar": 5, "type": "memory", "address": 2215247872, "size": 4096}]}]}]})
    elif cmd == "human-monitor-command" and args["command-line"].startswith("xp /"):
        # AHCI's registers: word 14 of port 0 (offset 0x100 + 0x38) is its CI, as the test says
        words = [0] * 256
        words[0] = 0x40141f05
        words[(0x100 + 0x04) // 4] = 0x00000000
        words[(0x100 + 0x38) // 4] = int(os.environ.get("AHCI_CI", "0"), 16)
        words[(0x100 + 0x34) // 4] = int(os.environ.get("AHCI_SACT", "0"), 16)
        words[(0x100 + 0x10) // 4] = 0x00000040
        words[(0x180 + 0x38) // 4] = 0x00000000
        base = int(args["command-line"].split()[-1], 16)
        out = "".join("%016x: %s\r\n" % (base + i * 4, " ".join("0x%08x" % w for w in words[i:i + 4])) for i in range(0, 256, 4))
        send({"return": out})
    elif cmd == "query-blockstats":
        send({"return": [{"device": "target", "stats": {"rd_operations": n}}]})
    elif cmd == "human-monitor-command":
        send({"return": "OUT[%s] n=%d\r\n" % (args["command-line"], n)})
    elif cmd == "screendump":
        if "format" in args and os.environ.get("QMP_NOPNG"):
            send({"error": {"class": "GenericError", "desc": "Parameter 'format' is unexpected"}})
            continue
        if "format" in args:
            open(args["filename"], "wb").write(b"SCREEN png")
        else:
            ppm = {"": b"P6\n2 1\n255\n" + bytes((255, 0, 0, 0, 0, 255)), "empty": b"", "header": b"P6\n2", "short": b"P6\n2 1\n255\n\xff"}
            open(args["filename"], "wb").write(ppm[os.environ.get("QMP_PPM", "")])
        send({"return": {}})
    else:
        send({"return": {"echo": cmd}})
PY
    SERVER=$!
    for _ in $(seq 100); do [ -S "$SOCK" ] && break; sleep 0.05; done
}

@test "qmp: both reads ask the live QEMU for its status, block counters, PCI tree, interrupts, vCPU state and USB" {
    qmp_server
    run -0 "$SCRIPT" qmp "$SOCK" "$OUT" 1
    for c in qmp_capabilities query-status query-kvm query-cpus-fast query-block query-blockstats query-pci \
        "human-monitor-command info irq" "human-monitor-command info pic" "human-monitor-command info ioapic" \
        "human-monitor-command info lapic 0" "human-monitor-command info lapic 1" \
        "human-monitor-command info registers -a" "human-monitor-command info cpus" \
        "human-monitor-command info usb" "human-monitor-command info qtree" screendump; do
        [ "$(grep -c " $c\$" "$QMP_LOG")" -ge 1 ] || { echo "never asked: $c"; false; }
    done
    [ "$(grep -c ' query-blockstats$' "$QMP_LOG")" -eq 2 ]
    [ "$(grep -c ' human-monitor-command info irq$' "$QMP_LOG")" -eq 2 ]
    [ "$(grep -c ' human-monitor-command info registers -a$' "$QMP_LOG")" -eq 2 ]
    [ "$(grep -c ' screendump$' "$QMP_LOG")" -eq 2 ]
}

# spec: independent review of ssh-window, 2026-11-02, finding 3 -- sun_path holds 104 bytes on macOS and 108 on
#       Linux; a gate work dir deep enough made connect() fail, and the evidence was lost without a word
@test "qmp: a socket whose absolute path is longer than AF_UNIX allows is still read, relative to its directory" {
    long=$BATS_TEST_TMPDIR/$(printf 'd%.0s' $(seq 1 60))/$(printf 'e%.0s' $(seq 1 60))
    mkdir -p "$long"
    qmp_server "$long"
    [ "${#SOCK}" -gt 120 ]
    run -0 "$SCRIPT" qmp "$SOCK" "$OUT" 1
    [[ "$output" != *warning* ]] || { echo "$output"; false; }
    [ -f "$OUT/read-1.txt" ]
    [ -f "$OUT/read-2.txt" ]
}

@test "qmp: a relative output directory still lands where it was named, though the socket's directory is entered" {
    qmp_server
    mkdir -p "$BATS_TEST_TMPDIR/cwd"
    cd "$BATS_TEST_TMPDIR/cwd"
    run -0 "$SCRIPT" qmp "$SOCK" rel-out 1
    [ -f "$BATS_TEST_TMPDIR/cwd/rel-out/read-1.txt" ]
}

@test "qmp: the SATA controller's registers are read from its BAR 5 and decoded per port: what the guest issued and the device has not finished" {
    qmp_server
    run -0 "$SCRIPT" qmp "$SOCK" "$OUT" 1
    grep -q ' human-monitor-command xp /256xw 0x840a0000$' "$QMP_LOG" || { cat "$QMP_LOG"; false; }
    grep -qx '== ahci registers ==' "$OUT/read-1.txt"
    grep -q '^ahci 0000:00:1f.2 BAR5 0x840a0000: CAP=0x40141f05' "$OUT/read-1.txt"
    grep -q '^ahci port 0: CI=0x00000000 SACT=0x00000000 IS=0x00000040' "$OUT/read-1.txt"
}

@test "qmp: an AHCI command issued and not completed (CI set) is called out, and one that is not is not" {
    AHCI_CI=0x00000003 qmp_server
    run -0 "$SCRIPT" qmp "$SOCK" "$OUT" 1
    grep -q '^ahci port 0: CI=0x00000003 .*OUTSTANDING: commands issued and not completed (slots 0,1)' "$OUT/read-1.txt" || { cat "$OUT/read-1.txt" | grep ahci; false; }
    ! grep -q 'port 1: .*OUTSTANDING' "$OUT/read-1.txt" || false
}

@test "qmp: a QEMU with no SATA controller has no AHCI read, and the rest is still read" {
    QMP_NO_SATA=1 qmp_server
    QMP_NO_SATA=1 run -0 "$SCRIPT" qmp "$SOCK" "$OUT" 1
    ! grep -q ' xp /' "$QMP_LOG" || false
    grep -qx '== ahci registers ==' "$OUT/read-1.txt"
    grep -q 'no SATA controller' "$OUT/read-1.txt"
    grep -q 'OUT\[info usb\]' "$OUT/read-1.txt"
}

@test "qmp: the two reads are the interval apart, and each is a file of text a person can read" {
    qmp_server
    run -0 "$SCRIPT" qmp "$SOCK" "$OUT" 2
    first=$(grep ' query-status$' "$QMP_LOG" | sed -n 1p | cut -d' ' -f1)
    second=$(grep ' query-status$' "$QMP_LOG" | sed -n 2p | cut -d' ' -f1)
    python3 -I -c 'import sys; a, b = map(float, sys.argv[1:]); assert 1.9 <= b - a < 6, b - a' "$first" "$second"
    grep -qx '== query-status ==' "$OUT/read-1.txt"
    grep -q '"status": "running"' "$OUT/read-1.txt"
    grep -qx '== info irq ==' "$OUT/read-2.txt"
    grep -q 'OUT\[info irq\]' "$OUT/read-1.txt"
    grep -q 'OUT\[info irq\]' "$OUT/read-2.txt"
    grep -q '^read 1 at ' "$OUT/read-1.txt"
    grep -q '^read 2 at ' "$OUT/read-2.txt"
}

@test "qmp: the delta shows what moved between the reads, and says so when nothing did" {
    qmp_server
    run -0 "$SCRIPT" qmp "$SOCK" "$OUT" 1
    grep -q '^[-+].*OUT\[info irq\] n=' "$OUT/delta.txt"
    grep -q '"rd_operations"' "$OUT/delta.txt"
    # the timestamps in the headings are not movement
    ! grep -q '^[-+]read [12] at' "$OUT/delta.txt" || false
}

@test "qmp: nothing moving between the reads is said in so many words" {
    python3 -I - "$BATS_TEST_TMPDIR/same.sock" <<'PY' 3>&- &
import json, socket, sys
srv = socket.socket(socket.AF_UNIX); srv.bind(sys.argv[1]); srv.listen(1)
c, _ = srv.accept(); f = c.makefile("rwb", buffering=0)
f.write(b'{"QMP": {}}\r\n')
for line in f:
    f.write(b'{"return": {"same": 1}}\r\n')
PY
    SERVER=$!
    for _ in $(seq 100); do [ -S "$BATS_TEST_TMPDIR/same.sock" ] && break; sleep 0.05; done
    run -0 "$SCRIPT" qmp "$BATS_TEST_TMPDIR/same.sock" "$OUT" 1
    grep -qi 'nothing changed between the reads' "$OUT/delta.txt"
}

@test "qmp: the screen is taken over QMP as a PNG into the output, twice" {
    qmp_server
    run -0 "$SCRIPT" qmp "$SOCK" "$OUT" 1
    [ "$(cat "$OUT/screen-1.png")" = "SCREEN png" ]
    [ "$(cat "$OUT/screen-2.png")" = "SCREEN png" ]
}

@test "qmp: a QEMU without PNG screendumps still gives a PNG (converted from its PPM), and no PPM is left" {
    QMP_NOPNG=1 qmp_server
    QMP_NOPNG=1 run -0 "$SCRIPT" qmp "$SOCK" "$OUT" 1
    [ ! -e "$OUT/screen-1.ppm" ]
    python3 -I - "$OUT/screen-1.png" <<'PY'
import struct, sys, zlib
b = open(sys.argv[1], "rb").read()
assert b[:8] == b"\x89PNG\r\n\x1a\n", "not a PNG"
pos, chunks = 8, {}
while pos < len(b):
    n, t = struct.unpack(">I4s", b[pos:pos + 8])
    data = b[pos + 8:pos + 8 + n]
    assert struct.unpack(">I", b[pos + 8 + n:pos + 12 + n])[0] == zlib.crc32(t + data)
    chunks.setdefault(t, []).append(data)
    pos += 12 + n
assert struct.unpack(">II", chunks[b"IHDR"][0][:8]) == (2, 1)
raw = zlib.decompress(b"".join(chunks[b"IDAT"]))
assert raw == b"\0" + bytes((255, 0, 0, 0, 0, 255)), raw
PY
}

# spec: independent re-review of ssh-window, 2026-11-02, minor 1 -- the PPM's header was read with a loop that ends
#       at whitespace, and an empty or cut-short file has none: the read never finished
@test "qmp: a truncated or empty PPM costs the screen only, in bounded time, and the rest of the read is kept" {
    for kind in empty header short; do
        rm -rf "$OUT"
        QMP_NOPNG=1 QMP_PPM=$kind qmp_server
        QMP_NOPNG=1 QMP_PPM=$kind run -0 timeout --foreground 30 "$SCRIPT" qmp "$SOCK" "$OUT" 1
        [ -f "$OUT/read-1.txt" ] || { echo "$kind: no read-1"; false; }
        [ -f "$OUT/read-2.txt" ] || { echo "$kind: no read-2"; false; }
        grep -q 'OUT\[info usb\]' "$OUT/read-2.txt"
        grep -q 'screen: could not convert' "$OUT/read-1.txt" || { echo "$kind: $(cat "$OUT/read-1.txt" | tail -3)"; false; }
        [ ! -e "$OUT/screen-1.ppm" ]
        kill "$SERVER" 2>/dev/null || true
        rm -f "$SOCK"
    done
}

@test "qmp: a command QEMU refuses is recorded as its error and the rest still run" {
    QMP_BAD="human-monitor-command info ioapic" qmp_server
    QMP_BAD="human-monitor-command info ioapic" run -0 "$SCRIPT" qmp "$SOCK" "$OUT" 1
    grep -q 'GenericError' "$OUT/read-1.txt"
    grep -q 'no such thing' "$OUT/read-1.txt"
    grep -q 'OUT\[info usb\]' "$OUT/read-1.txt"
    grep -q 'OUT\[info usb\]' "$OUT/read-2.txt"
}

@test "qmp: no socket is a warning and no files, not a failure" {
    run -0 "$SCRIPT" qmp "$BATS_TEST_TMPDIR/none.sock" "$OUT" 1
    [[ "$output" == *"warning"*"qmp: no QMP socket at $BATS_TEST_TMPDIR/none.sock"* ]] || false
    [ ! -e "$OUT" ]
}

@test "qmp: a QEMU that accepts and says nothing costs a bounded wait and a warning, not a hang" {
    QMP_MUTE=1 qmp_server
    GATE_BUILD_EVIDENCE_QMP_TIMEOUT=2 QMP_MUTE=1 run -0 timeout --foreground 30 "$SCRIPT" qmp "$SOCK" "$OUT" 1
    [[ "$output" == *"warning"*"qmp"* ]] || false
}

@test "qmp: it asks, and never changes anything: no command that stops, resets, quits or writes the guest" {
    qmp_server
    run -0 "$SCRIPT" qmp "$SOCK" "$OUT" 1
    ! grep -Eq ' (stop|cont|quit|system_reset|system_powerdown|inject-nmi|pmemsave|memsave|device_del|migrate|human-monitor-command (stop|cont|quit|system_|nmi|pmemsave|memsave|migrate|device_del))' "$QMP_LOG" || false
}

@test "qmp: usage is 2 for a missing argument or a bad interval" {
    run "$SCRIPT" qmp "$BATS_TEST_TMPDIR/x.sock"
    [ "$status" -eq 2 ]
    run "$SCRIPT" qmp "$BATS_TEST_TMPDIR/x.sock" "$OUT" soon
    [ "$status" -eq 2 ]
}

# spec: owner ruling R70, 2026-11-02 -- the reader ships inside each template zip, alone (mavericks-vm runs it
#       from the unzipped template directory), so it may not source anything from this repository
@test "the reader stands alone: copied by itself into an empty directory it reads a QEMU over QMP" {
    qmp_server
    mkdir "$BATS_TEST_TMPDIR/alone"; cp "$SCRIPT" "$BATS_TEST_TMPDIR/alone/gate-build-evidence.sh"
    cd "$BATS_TEST_TMPDIR/alone"
    run -0 bash ./gate-build-evidence.sh qmp "$SOCK" "$OUT" 1
    [ -f "$OUT/read-2.txt" ]
    run -0 bash ./gate-build-evidence.sh qmp "$BATS_TEST_TMPDIR/none.sock" "$OUT/x" 1
    [[ "$output" == "gate-build-evidence: warning: qmp: no QMP socket at "* ]] || { echo "$output"; false; }
}

@test "usage: no command, an unknown one, and the wrong number of arguments are 2" {
    run "$SCRIPT"
    [ "$status" -eq 2 ]
    run "$SCRIPT" frob a b
    [ "$status" -eq 2 ]
    run "$SCRIPT" screenshot 127.0.0.1:1
    [ "$status" -eq 2 ]
    run "$SCRIPT" disk only-one
    [ "$status" -eq 2 ]
    run "$SCRIPT" screenshot not-a-port "$OUT.png"
    [ "$status" -eq 2 ]
}

@test "screenshot: one frame of the guest's screen, a PNG with the pixels' own colours" {
    vnc_server 5 3
    run "$SCRIPT" screenshot "127.0.0.1:$PORT" "$OUT.png"
    [ "$status" -eq 0 ]
    python3 -I - "$OUT.png" <<'PY'
import struct, sys, zlib
b = open(sys.argv[1], "rb").read()
assert b[:8] == b"\x89PNG\r\n\x1a\n", "not a PNG"
pos, chunks = 8, {}
while pos < len(b):
    n, t = struct.unpack(">I4s", b[pos:pos + 8])
    data = b[pos + 8:pos + 8 + n]
    assert struct.unpack(">I", b[pos + 8 + n:pos + 12 + n])[0] == zlib.crc32(t + data), "bad crc " + t.decode()
    chunks.setdefault(t, []).append(data)
    pos += 12 + n
w, h, depth, ctype = struct.unpack(">IIBB", chunks[b"IHDR"][0][:10])
assert (w, h, depth, ctype) == (5, 3, 8, 2), (w, h, depth, ctype)
raw = zlib.decompress(b"".join(chunks[b"IDAT"]))
assert len(raw) == h * (1 + w * 3)
for y in range(h):
    row = raw[y * (1 + w * 3):(y + 1) * (1 + w * 3)]
    assert row[0] == 0
    for x in range(w):
        # pixel bytes were B, G, R, X = 7, y, x: red x, green y, blue 7
        assert tuple(row[1 + x * 3:4 + x * 3]) == (x, y, 7), (x, y, tuple(row[1 + x * 3:4 + x * 3]))
PY
}

@test "screenshot: nobody listening is a warning and no file, not a failure" {
    p=$(python3 -I -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')
    run -0 "$SCRIPT" screenshot "127.0.0.1:$p" "$OUT.png"
    [[ "$output" == *"warning"*"screenshot"* ]] || false
    [ ! -e "$OUT.png" ]
}

# --- disk ---

# stand-ins for the disk reader's tools. qemu-img dd writes a GPT whose second partition is
# Apple HFS+ at sector 409600; the daemon "exports" nothing, as a process the helper must stop;
# 7z "extracts" the files its pattern names; fusermount3 records.
disk_stubs() {
    cat > "$STUBS/qemu-img" <<'STUB'
#!/bin/sh
printf 'qemu-img %s\n' "$*" >> "$STUB_LOG"
for a; do case $a in of=*) of=${a#of=} ;; esac; done
python3 -I - "$of" "${STUB_GPT:-hfs}" <<'PY'
import struct, sys, uuid
b = bytearray(34 * 512)
b[512:520] = b"EFI PART"
struct.pack_into("<QII", b, 512 + 72, 2, 128, 128)
def ent(i, t, first, last):
    e = bytearray(128)
    e[:16] = t.bytes_le
    struct.pack_into("<QQ", e, 32, first, last)
    b[1024 + i * 128:1024 + (i + 1) * 128] = e
ent(0, uuid.UUID("c12a7328-f81f-11d2-ba4b-00a0c93ec93b"), 40, 409599)
if sys.argv[2] == "hfs":
    ent(1, uuid.UUID("48465300-0000-11aa-aa11-00306543ecac"), 409600, 409600 + 2047)
open(sys.argv[1], "wb").write(b)
PY
STUB
    cat > "$STUBS/qemu-storage-daemon" <<'STUB'
#!/bin/sh
if [ "$1" = --serve ]; then
    trap 'exit 0' TERM
    while :; do sleep 0.2; done
fi
printf 'qemu-storage-daemon %s\n' "$*" >> "$STUB_LOG"
for a; do
    case $a in
    --pidfile) next=pid ;;
    --export) next=exp ;;
    *) case $next in
        pid) pidfile=$a ;;
        exp) mnt=$(printf '%s' "${a#*mountpoint=}" | sed 's/,,/\001/g; s/,.*//; s/\001/,/g') ;;
       esac
       next= ;;
    esac
done
: > "$mnt"
"$0" --serve &
echo $! > "$pidfile"
STUB
    cat > "$STUBS/7z" <<'STUB'
#!/bin/sh
printf '7z %s\n' "$*" >> "$STUB_LOG"
cmd=$1
for a; do case $a in -o*) out=${a#-o} ;; esac; last=$a; done
[ -z "${STUB_7Z_SLEEP:-}" ] || sleep "$STUB_7Z_SLEEP"
case $cmd:$last in
e:*private/var/log/system.log) echo "Oct 12 10:00:00 host kernel[0]: the install's own line" > "$out/system.log"
    echo "0083;0;Safari;" > "$out/system.log:com.apple.quarantine" ;;
e:*private/var/log/system.log.*) printf 'Oct 11 09:00:00 host: a rotated line\n' | gzip > "$out/system.log.1.gz"
    printf 'not gzip at all' > "$out/system.log.2.gz" ;;
e:*private/var/log/guest-macosx-firstboot.log) echo "firstboot: remote login: on from the next boot" > "$out/guest-macosx-firstboot.log" ;;
e:*Library/Logs/DiagnosticReports/*) echo "a panic" > "$out/Kernel_2026-10-31.panic"; echo "a crash" > "$out/configd_2026.crash" ;;
e:*launchd.db/com.apple.launchd/overrides.plist)
    python3 -I -c 'import plistlib, sys; plistlib.dump({"com.openssh.sshd": {"Disabled": False}}, open(sys.argv[1], "wb"), fmt=plistlib.FMT_BINARY)' "$out/overrides.plist" ;;
l:*) echo "2026-10-31 10:00:00 .....  57  57  Macintosh HD/private/var/db/.guest-macosx-firstboot/marker" ;;
esac
exit 0
STUB
    cat > "$STUBS/fusermount3" <<'STUB'
#!/bin/sh
printf 'fusermount3 %s\n' "$*" >> "$STUB_LOG"
exit 0
STUB
    chmod +x "$STUBS"/*
    export PATH="$STUBS:/usr/bin:/bin"
    : > "$BATS_TEST_TMPDIR/disk.qcow2"
}

@test "disk: the guest's logs, reports and sshd override read from the image, each as text" {
    disk_stubs
    run -0 "$SCRIPT" disk "$BATS_TEST_TMPDIR/disk.qcow2" "$OUT"
    [ "$(cat "$OUT/system.log")" = "Oct 12 10:00:00 host kernel[0]: the install's own line" ]
    [ -f "$OUT/guest-macosx-firstboot.log" ]
    # HFS+ extended attributes, which 7z extracts as streams, are not logs
    [ -z "$(find "$OUT" -name '*:*')" ]
    # reports keep their names, as .txt: the evidence holds text, and a report is text
    [ "$(cat "$OUT/report-Kernel_2026-10-31.panic.txt")" = "a panic" ]
    [ "$(cat "$OUT/report-configd_2026.crash.txt")" = "a crash" ]
    # launchd's overrides, binary plist, as text: whether sshd was on
    grep -q "com.openssh.sshd" "$OUT/launchd-overrides.txt"
    grep -q "Disabled" "$OUT/launchd-overrides.txt"
    # what the first boot left (its marker), listed
    grep -q "guest-macosx-firstboot/marker" "$OUT/firstboot-state.txt"
    # the export is the HFS+ partition alone, read-only, and nothing in the output is an image
    grep -q 'qemu-storage-daemon .*offset=209715200' "$STUB_LOG"
    grep -q 'qemu-storage-daemon .*size=1048576' "$STUB_LOG"
    grep -q 'read-only=on' "$STUB_LOG"
    [ -z "$(find "$OUT" -type f ! -name '*.log' ! -name '*.txt')" ]
}

# spec: independent re-review of ssh-window, 2026-11-02, minor 6 -- rotated logs are gzip, which the evidence's
#       prune deletes as not text: they are kept as the text they hold, or not at all
@test "disk: a rotated system.log.N.gz is kept as text, and one that is not gzip is dropped, never copied as bytes" {
    disk_stubs
    run -0 "$SCRIPT" disk "$BATS_TEST_TMPDIR/disk.qcow2" "$OUT"
    # named so that the evidence's prune, which keeps *.log and drops the rest, keeps it
    [ "$(cat "$OUT/system.log.1.log")" = "Oct 11 09:00:00 host: a rotated line" ]
    [ -z "$(find "$OUT" -name '*.gz')" ]
    [ ! -e "$OUT/system.log.2" ] && [ ! -e "$OUT/system.log.2.log" ]
}

@test "disk: the daemon is stopped by the pid it wrote, the mount undone, nothing left in TMPDIR" {
    disk_stubs
    run -0 "$SCRIPT" disk "$BATS_TEST_TMPDIR/disk.qcow2" "$OUT"
    grep -q '^fusermount3 -u ' "$STUB_LOG"
    # the daemon the stub started is gone (its pid was in the pidfile; the helper killed it)
    for _ in $(seq 50); do
        [ -z "$(ps -eo comm= | grep -x 'qemu-storage-da')" ] && break
        sleep 0.1
    done
    [ -z "$(ps -eo comm= | grep -x 'qemu-storage-da')" ]
    [ -z "$(ls -A "$TMPDIR")" ]
}

# spec: independent review of ssh-window, 2026-11-02, finding 5 -- the gate runs this under timeout, whose TERM
#       was feared to skip the EXIT trap and leave the daemon and its FUSE mount behind. Measured: bash runs
#       an EXIT trap on a fatal TERM, so this holds without a TERM trap; the test keeps it so
@test "disk: a TERM mid-read still undoes the mount, stops the daemon and removes the scratch directory" {
    disk_stubs
    STUB_7Z_SLEEP=30 "$SCRIPT" disk "$BATS_TEST_TMPDIR/disk.qcow2" "$OUT" > /dev/null 2>&1 3>&- &
    pid=$!
    for _ in $(seq 100); do grep -q '^7z ' "$STUB_LOG" && break; sleep 0.1; done
    grep -q '^7z ' "$STUB_LOG"
    kill -TERM "$pid"
    wait "$pid" || true
    grep -q '^fusermount3 -u ' "$STUB_LOG" || { cat "$STUB_LOG"; false; }
    for _ in $(seq 50); do
        [ -z "$(ps -eo comm= | grep -x 'qemu-storage-da')" ] && break
        sleep 0.1
    done
    [ -z "$(ps -eo comm= | grep -x 'qemu-storage-da')" ]
    [ -z "$(ls -A "$TMPDIR")" ]
}

# spec: QEMU's option syntax: a comma in a value is written as two; one comma ends the value and starts
#       another option, so an image or scratch path with a comma exported something else, or nothing
@test "disk: commas in the image's path and in the mount point are doubled in QEMU's options" {
    disk_stubs
    mkdir -p "$BATS_TEST_TMPDIR/a,b"; : > "$BATS_TEST_TMPDIR/a,b/di,sk.qcow2"
    export TMPDIR="$BATS_TEST_TMPDIR/t,mp"; mkdir -p "$TMPDIR"
    run -0 "$SCRIPT" disk "$BATS_TEST_TMPDIR/a,b/di,sk.qcow2" "$OUT"
    grep -qF "filename=$BATS_TEST_TMPDIR/a,,b/di,,sk.qcow2,read-only=on" "$STUB_LOG" || { cat "$STUB_LOG"; false; }
    grep -qF "mountpoint=$BATS_TEST_TMPDIR/t,,mp/" "$STUB_LOG" || { cat "$STUB_LOG"; false; }
    [ -f "$OUT/system.log" ]
}

@test "disk: an image with no HFS+ partition is a warning, not a failure, and no mount" {
    disk_stubs
    STUB_GPT=none run -0 "$SCRIPT" disk "$BATS_TEST_TMPDIR/disk.qcow2" "$OUT"
    [[ "$output" == *"warning"*"HFS+"* ]] || false
    [ "$(grep -c '^qemu-storage-daemon' "$STUB_LOG")" -eq 0 ]
}

@test "disk: a missing tool is a warning naming it, not a failure" {
    disk_stubs
    GATE_BUILD_EVIDENCE_TOOLS="qemu-img qemu-storage-daemon guest-macosx-no-such-7z python3" \
        run -0 "$SCRIPT" disk "$BATS_TEST_TMPDIR/disk.qcow2" "$OUT"
    [[ "$output" == *"warning"*"guest-macosx-no-such-7z"* ]] || false
    [ "$(grep -c '^qemu-' "$STUB_LOG")" -eq 0 ]
}

@test "disk: an image that is not there is a warning, not a failure" {
    disk_stubs
    run -0 "$SCRIPT" disk "$BATS_TEST_TMPDIR/nope.qcow2" "$OUT"
    [[ "$output" == *"warning"*"nope.qcow2"* ]] || false
}
