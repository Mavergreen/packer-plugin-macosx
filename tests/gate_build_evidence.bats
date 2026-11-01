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
