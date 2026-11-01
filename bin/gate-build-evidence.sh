#!/usr/bin/env bash
# platform: host-agnostic -- screenshot and qmp need only python3 (qmp tested on Linux only); disk needs Linux (qemu-storage-daemon's FUSE export, fusermount, 7z) and warns and reads nothing where a tool is missing
#   bin/gate.sh, which calls this, reads a QEMU's /proc and `ps -o etimes` only where /proc exists.
# What a build that failed after QEMU started leaves for a person, for bin/gate.sh. Evidence only:
# whatever this cannot get it says so (a warning) and exits 0, so it never changes a verdict.
#
#   usage: bin/gate-build-evidence.sh screenshot <host:port> <out.png>
#          bin/gate-build-evidence.sh disk       <image.qcow2> <out-dir>
#          bin/gate-build-evidence.sh qmp        <qmp.sock> <out-dir> <seconds>
#
#   qmp         what the live QEMU thinks, over the QMP socket the templates open beside their output
#               directory, read twice <seconds> apart so that counters show whether anything moves.
#               Into <out-dir>: read-1.txt and read-2.txt (QMP status, KVM, vCPUs, block devices and
#               their counters, the PCI tree, and through the human monitor the interrupt state
#               (irq, pic, ioapic, each lapic), the registers and halted state of every vCPU, USB and
#               the device tree), screen-1.png and screen-2.png (QEMU's own screendump; a .ppm when
#               QEMU cannot do PNG), and delta.txt (what differs between the two reads). It only
#               asks: no command that stops, resets or writes the guest. The guest must still be
#               running, and no one else may hold the socket (QMP serves one client).
#   screenshot  one frame of the guest's screen over VNC, the port Packer's qemu builder names
#               ("vnc://127.0.0.1:59xx"; no password): a PNG. It needs no QMP socket, so it also works
#               for a build whose template opened none (qmp's screendump is the other way to a frame).
#   disk        the guest's logs, read from its disk with no root and no boot: the image's HFS+
#               partition (the first Apple HFS+ one in the GPT) as a read-only file through
#               qemu-storage-daemon's FUSE export, read with 7z. Into <out-dir>, as text only:
#               system.log, install.log, the first boot's own logs, each crash/panic/hang report as
#               report-<name>.txt, launchd-overrides.txt (whether sshd was switched on, from
#               overrides.plist) and firstboot-state.txt (what the first boot's state directory
#               holds). Never the image, nor any part of it. The image must not be in use: stop the
#               guest first. The daemon is stopped by the pid it wrote.
#   env:  GATE_BUILD_EVIDENCE_TOOLS (the commands disk needs: qemu-img qemu-storage-daemon 7z python3)
#         GATE_BUILD_EVIDENCE_QMP_TIMEOUT (seconds QMP waits for one answer, default 20)
set -euo pipefail

# standalone: this file also ships inside each template zip, where nothing else of this repository is
warn() { printf 'gate-build-evidence: warning: %s\n' "$*" >&2; }

TOOLS=${GATE_BUILD_EVIDENCE_TOOLS:-qemu-img qemu-storage-daemon 7z python3}
# a log can be big (a first boot that spins fills it): the end is what shows how it stopped
KEEP_BYTES=$((8 * 1024 * 1024))

usage() {
    cat >&2 <<'EOF'
usage: gate-build-evidence.sh screenshot <host:port> <out.png>
       gate-build-evidence.sh disk <image.qcow2> <out-dir>
       gate-build-evidence.sh qmp <qmp.sock> <out-dir> <seconds>
EOF
    exit 2
}

cmd_screenshot() {
    [ "$#" -eq 2 ] || usage
    [[ $1 =~ ^[A-Za-z0-9.-]+:[0-9]+$ ]] || usage
    local out=$2 tmp
    command -v python3 >/dev/null 2>&1 || { warn "screenshot: no python3"; return 0; }
    tmp=$out.partial
    if timeout 60 python3 -I - "$1" "$tmp" <<'PY'
import socket, struct, sys, zlib

host, port = sys.argv[1].rsplit(":", 1)
s = socket.create_connection((host, int(port)), timeout=20)

def rd(n):
    b = b""
    while len(b) < n:
        c = s.recv(n - len(b))
        if not c:
            raise EOFError("the VNC server closed the connection")
        b += c
    return b

rd(12)
s.sendall(b"RFB 003.008\n")
types = rd(rd(1)[0])
if 1 not in types:
    sys.exit("the VNC server wants a password (security types %r)" % list(types))
s.sendall(b"\x01")
if struct.unpack(">I", rd(4))[0] != 0:
    sys.exit("the VNC server refused the handshake")
s.sendall(b"\x01")  # ClientInit: shared
w, h = struct.unpack(">HH", rd(4))
rd(16)
rd(struct.unpack(">I", rd(4))[0])
# 32 bits per pixel, little-endian true colour, red<<16 | green<<8 | blue
s.sendall(struct.pack(">BxxxBBBBHHHBBBxxx", 0, 32, 24, 0, 1, 255, 255, 255, 16, 8, 0))
s.sendall(struct.pack(">BxHi", 2, 1, 0))  # SetEncodings: raw alone
s.sendall(struct.pack(">BBHHHH", 3, 0, 0, 0, w, h))
fb = bytearray(w * h * 3)
seen = bytearray(w * h)
got = 0
while got < w * h:
    t = rd(1)[0]
    if t != 0:
        sys.exit("unexpected VNC message %d" % t)
    rd(1)
    for _ in range(struct.unpack(">H", rd(2))[0]):
        x, y, rw, rh, enc = struct.unpack(">HHHHi", rd(12))
        if enc != 0:
            sys.exit("unexpected VNC encoding %d" % enc)
        px = rd(rw * rh * 4)
        for j in range(rh):
            row = px[j * rw * 4:(j + 1) * rw * 4]
            rgb = bytearray(rw * 3)
            rgb[0::3] = row[2::4]
            rgb[1::3] = row[1::4]
            rgb[2::3] = row[0::4]
            d = ((y + j) * w + x) * 3
            fb[d:d + rw * 3] = rgb
            for i in range(rw):
                o = (y + j) * w + x + i
                if not seen[o]:
                    seen[o] = 1
                    got += 1

def chunk(t, d):
    return struct.pack(">I", len(d)) + t + d + struct.pack(">I", zlib.crc32(t + d))

raw = b"".join(b"\0" + bytes(fb[y * w * 3:(y + 1) * w * 3]) for y in range(h))
png = b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 2, 0, 0, 0)) \
    + chunk(b"IDAT", zlib.compress(raw, 6)) + chunk(b"IEND", b"")
open(sys.argv[2], "wb").write(png)
PY
    then
        mv "$tmp" "$out"
    else
        rm -f "$tmp"
        warn "screenshot: could not capture the screen at $1"
    fi
    return 0
}

# --- qmp ---

cmd_qmp() {
    [ "$#" -eq 3 ] || usage
    case $3 in ''|*[!0-9]*) usage ;; esac
    command -v python3 >/dev/null 2>&1 || { warn "qmp: no python3"; return 0; }
    [ -S "$1" ] || { warn "qmp: no QMP socket at $1"; return 0; }
    mkdir -p "$2"
    GATE_BUILD_EVIDENCE_QMP_TIMEOUT=${GATE_BUILD_EVIDENCE_QMP_TIMEOUT:-20} \
        python3 -I - "$1" "$2" "$3" <<'PY' || warn "qmp: could not read the QEMU at $1 (a partial read is in $2)"
import difflib, json, os, socket, struct, sys, time, zlib

sock_path, out, gap = sys.argv[1], os.path.abspath(sys.argv[2]), int(sys.argv[3])
timeout = float(os.environ["GATE_BUILD_EVIDENCE_QMP_TIMEOUT"])

# What a stall is told apart by: whether QEMU still runs the vCPUs and what they are doing
# (registers, halted), whether any block device has work it never finished (counters), and
# whether an interrupt is pending or masked (irq counts, pic, ioapic, lapic) -- a lost interrupt
# and a lost completion look alike from inside the guest. All of it is a question.
QMP = [
    ("query-status", None),
    ("query-kvm", None),
    ("query-cpus-fast", None),
    ("query-block", None),
    ("query-blockstats", None),
    ("query-pci", None),
]
HMP = [
    "info irq", "info pic", "info ioapic",
    "info registers -a", "info cpus", "info usb", "info qtree", "info block -n",
    "info chardev", "info network",
]

s = socket.socket(socket.AF_UNIX)
s.settimeout(timeout)
# connect relative to the socket's directory: sun_path is 104 bytes on macOS and 108 on Linux, and a
# work directory's absolute path can be longer than that
os.chdir(os.path.dirname(os.path.abspath(sock_path)))
s.connect(os.path.basename(sock_path))
buf = b""

def msg():
    global buf
    while b"\n" not in buf:
        c = s.recv(65536)
        if not c:
            raise EOFError("QEMU closed the QMP socket")
        buf += c
    line, buf = buf.split(b"\n", 1)
    return json.loads(line)

def call(cmd, args=None):
    req = {"execute": cmd}
    if args:
        req["arguments"] = args
    s.sendall(json.dumps(req).encode() + b"\n")
    while True:
        m = msg()
        if "return" in m or "error" in m:
            return m

msg()  # the greeting
r = call("qmp_capabilities")
if "error" in r:
    raise SystemExit("qmp_capabilities: %r" % r)

def show(m):
    if "error" in m:
        return "ERROR %s: %s\n" % (m["error"].get("class"), m["error"].get("desc"))
    v = m["return"]
    return v.replace("\r\n", "\n") if isinstance(v, str) else json.dumps(v, indent=1, sort_keys=True) + "\n"

# An AHCI command the guest issued and the controller has not completed is a bit in a port's CI
# (PxCI), and one that completed but whose interrupt was not taken is IS with the line asserted:
# these registers tell a lost completion from a lost interrupt, which no counter does. Read-only: a
# memory read of the controller's BAR 5.
def ahci(pci):
    found = []
    for bus in pci.get("return", []) if isinstance(pci.get("return"), list) else []:
        for d in bus.get("devices", []):
            if d.get("class_info", {}).get("class") != 0x106:
                continue
            for r in d.get("regions", []):
                if r.get("bar") == 5 and r.get("type") == "memory":
                    found.append((bus.get("bus", 0), d["slot"], d["function"], r["address"]))
    if not found:
        return "no SATA controller with a BAR 5 in query-pci\n"
    out = ""
    for b, slot, fn, addr in found:
        m = call("human-monitor-command", {"command-line": "xp /256xw 0x%x" % addr})
        if "error" in m:
            out += show(m)
            continue
        words = []
        for line in m["return"].splitlines():
            if ":" in line:
                words += [int(w, 16) for w in line.split(":", 1)[1].split() if w.startswith("0x")]
        if len(words) < 256:
            out += "short read: %d words\n%s" % (len(words), m["return"])
            continue
        out += "ahci 0000:%02x:%02x.%d BAR5 0x%x: CAP=0x%08x GHC=0x%08x IS=0x%08x PI=0x%08x VS=0x%08x\n" % (
            b, slot, fn, addr, words[0], words[1], words[2], words[3], words[4])
        for p in range(6):
            w = words[(0x100 + 0x80 * p) // 4:(0x100 + 0x80 * (p + 1)) // 4]
            ci, sact = w[14], w[13]
            line = "ahci port %d: CI=0x%08x SACT=0x%08x IS=0x%08x IE=0x%08x CMD=0x%08x TFD=0x%08x SSTS=0x%08x" % (
                p, ci, sact, w[4], w[5], w[6], w[8], w[10])
            if ci or sact:
                slots = [str(i) for i in range(32) if (ci | sact) >> i & 1]
                line += "  OUTSTANDING: commands issued and not completed (slots %s)" % ",".join(slots)
            out += line + "\n"
    return out

def ppm_to_png(src, dst):
    b = open(src, "rb").read()
    fields, pos = [], 0
    while len(fields) < 4:
        while pos < len(b) and b[pos:pos + 1].isspace():
            pos += 1
        if pos >= len(b):
            raise ValueError("the PPM ends inside its header")
        if b[pos:pos + 1] == b"#":
            pos = b.index(b"\n", pos)
            continue
        end = pos
        while end < len(b) and not b[end:end + 1].isspace():
            end += 1
        fields.append(b[pos:end])
        pos = end
    magic, w, h, maxv = fields[0], int(fields[1]), int(fields[2]), int(fields[3])
    if magic != b"P6" or maxv != 255:
        raise ValueError("not an 8-bit P6 PPM")
    px = b[pos + 1:pos + 1 + w * h * 3]
    if len(px) != w * h * 3:
        raise ValueError("the PPM holds %d bytes of pixels, not the %d its header says" % (len(px), w * h * 3))
    def chunk(t, d):
        return struct.pack(">I", len(d)) + t + d + struct.pack(">I", zlib.crc32(t + d))
    raw = b"".join(b"\0" + px[y * w * 3:(y + 1) * w * 3] for y in range(h))
    open(dst, "wb").write(b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 2, 0, 0, 0))
                          + chunk(b"IDAT", zlib.compress(raw, 6)) + chunk(b"IEND", b""))

def read(n):
    started = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    parts = []
    # written as it goes: a QEMU that stops answering halfway leaves what it did say
    f = open(os.path.join(out, "read-%d.txt" % n), "w")
    def put(text, keep=True):
        f.write(text)
        f.flush()
        if keep:
            parts.append(text)
    put("read %d at %s\n" % (n, started), keep=False)
    cpus = []
    pci = {}
    for cmd, args in QMP:
        m = call(cmd, args)
        if cmd == "query-cpus-fast" and isinstance(m.get("return"), list):
            cpus = [c["cpu-index"] for c in m["return"] if isinstance(c, dict) and "cpu-index" in c][:16]
        if cmd == "query-pci":
            pci = m
        put("== %s ==\n%s" % (cmd, show(m)))
    put("== ahci registers ==\n" + ahci(pci))
    for h in HMP + ["info lapic %d" % i for i in cpus]:
        put("== %s ==\n%s" % (h, show(call("human-monitor-command", {"command-line": h}))))
    shot = os.path.join(out, "screen-%d" % n)
    m = call("screendump", {"filename": shot + ".png", "format": "png"})
    if "error" in m:
        # QEMU before 7.1 writes only PPM; it goes as a PNG, which is what the evidence keeps
        m = call("screendump", {"filename": shot + ".ppm"})
        if "return" in m:
            try:
                ppm_to_png(shot + ".ppm", shot + ".png")
            except (ValueError, OSError) as e:
                m = {"return": "screen: could not convert QEMU's PPM: %s" % e}
            finally:
                os.unlink(shot + ".ppm")
    put("== screendump ==\n%s" % show(m))
    f.close()
    return parts

first = read(1)
time.sleep(gap)
second = read(2)
body = lambda parts: [l for p in parts for l in p.splitlines(True)]
d = list(difflib.unified_diff(body(first), body(second), "read 1", "read 2", n=0))
with open(os.path.join(out, "delta.txt"), "w") as f:
    f.write("read 2 came %d s after read 1\n" % gap)
    f.write("".join(d) if d else "nothing changed between the reads\n")
PY
    echo "qmp: read the QEMU at $1 twice, $3 s apart, into $2"
    return 0
}

# --- disk ---

disk_tmp=
disk_mnt=

disk_cleanup() {
    local p
    if [ -n "$disk_mnt" ]; then
        { fusermount3 -u "$disk_mnt" || fusermount -u "$disk_mnt"; } 2>/dev/null || true
        p=$(cat "$disk_mnt.pid" 2>/dev/null || true)
        case $p in ''|*[!0-9]*) ;; *)
            if [ "$p" -gt 1 ] && ps -p "$p" -o comm= 2>/dev/null | grep -q qemu-storage; then
                kill "$p" 2>/dev/null || true
            fi ;;
        esac
    fi
    if [ -n "$disk_tmp" ]; then rm -rf "$disk_tmp"; fi
    disk_tmp=''
    disk_mnt=''
}

# part_of <gpt>: "offset size" in bytes of the first Apple HFS+ partition, or nothing
part_of() {
    python3 -I - "$1" <<'PY' 2>/dev/null || true
import struct, sys, uuid
b = open(sys.argv[1], "rb").read()
hdr = b[512:1024]
if hdr[:8] != b"EFI PART":
    sys.exit(0)
lba, n, sz = struct.unpack("<QII", hdr[72:88])
hfs = uuid.UUID("48465300-0000-11aa-aa11-00306543ecac")
for i in range(n):
    e = b[lba * 512 + i * sz:lba * 512 + (i + 1) * sz]
    if len(e) < 56:
        break
    if uuid.UUID(bytes_le=e[:16]) == hfs:
        first, last = struct.unpack("<QQ", e[32:48])
        print(first * 512, (last - first + 1) * 512)
        break
PY
}

# extract <pattern> <dir>: the files 7z finds under the volume's top directory, flattened into <dir>
extract() {
    mkdir -p "$2"
    7z e -y -aou "-o$2" "$disk_mnt" "$1" >/dev/null 2>&1 || true
}

# keep_end <file>: at most KEEP_BYTES of it, the end
keep_end() {
    local size
    size=$(wc -c < "$1" | tr -d ' ')
    if [ "$size" -gt "$KEEP_BYTES" ]; then
        tail -c "$KEEP_BYTES" "$1" > "$1.tail" && mv "$1.tail" "$1"
    fi
}

# qopt <value>: the value in QEMU's option syntax, where a comma is written as two
qopt() { printf '%s' "$1" | sed 's/,/,,/g'; }

cmd_disk() {
    [ "$#" -eq 2 ] || usage
    local img=$1 out=$2 t off size x f n got=0
    for t in $TOOLS; do
        command -v "$t" >/dev/null 2>&1 || { warn "disk: $t not found; the guest's logs are not read from its disk"; return 0; }
    done
    [ -f "$img" ] || { warn "disk: no image: $img"; return 0; }
    disk_tmp=$(mktemp -d "${TMPDIR:-/tmp}/gbe.XXXXXX") || { warn "disk: cannot make a scratch directory"; return 0; }
    trap disk_cleanup EXIT
    # the first 34 sectors: the GPT header and its 128 entries
    qemu-img dd -f qcow2 -O raw bs=512 count=34 "if=$img" "of=$disk_tmp/gpt" >/dev/null 2>&1 \
        || { warn "disk: cannot read the partition table of $img"; return 0; }
    read -r off size <<EOF || true
$(part_of "$disk_tmp/gpt")
EOF
    case ${off:-}:${size:-} in
        '':*|*:''|*[!0-9:]*) warn "disk: $img has no HFS+ partition in its GPT; nothing to read"; return 0 ;;
    esac
    disk_mnt=$disk_tmp/hfs
    : > "$disk_mnt"
    qemu-storage-daemon \
        --blockdev "driver=file,node-name=f,filename=$(qopt "$img"),read-only=on" \
        --blockdev "driver=qcow2,node-name=q,file=f,read-only=on" \
        --blockdev "driver=raw,node-name=p,file=q,offset=$off,size=$size,read-only=on" \
        --export "type=fuse,id=e,node-name=p,mountpoint=$(qopt "$disk_mnt"),writable=off" \
        --pidfile "$disk_mnt.pid" --daemonize >/dev/null 2>&1 \
        || { warn "disk: qemu-storage-daemon could not export the partition of $img"; return 0; }
    mkdir -p "$out"
    x=$disk_tmp/x
    for n in system.log install.log kernel.log guest-macosx-rc.server.log guest-macosx-firstboot.log; do
        extract "*/private/var/log/$n" "$x/log"
    done
    extract '*/private/var/log/system.log.*' "$x/log"
    extract '*/private/var/db/.guest-macosx-firstboot/postinstall.log' "$x/log"
    extract '*/.guest-macosx-firstboot.log' "$x/log"
    for f in "$x"/log/*; do
        [ -f "$f" ] || continue
        # 7z extracts HFS+'s extended attributes as streams, "system.log:com.apple.quarantine"
        case ${f##*/} in *:*) continue ;; esac
        # rotated logs are gzip: kept as the text they hold (what is not gzip is dropped, not copied as bytes)
        case ${f##*/} in
            *.gz) if gzip -dc "$f" > "${f%.gz}" 2>/dev/null; then rm -f "$f"; f=${f%.gz}; else rm -f "$f" "${f%.gz}"; continue; fi ;;
        esac
        keep_end "$f"
        case ${f##*/} in
            *.log) mv "$f" "$out/${f##*/}" ;;
            *.log.*) mv "$f" "$out/${f##*/}.log" ;;
            *) mv "$f" "$out/${f##*/}.log" ;;
        esac
        got=$((got + 1))
    done
    extract '*/Library/Logs/DiagnosticReports/*' "$x/rep"
    extract '*/Users/*/Library/Logs/DiagnosticReports/*' "$x/rep"
    for f in "$x"/rep/*; do
        [ -f "$f" ] || continue
        case ${f##*/} in *:*) continue ;; esac
        keep_end "$f"
        mv "$f" "$out/report-${f##*/}.txt"
        got=$((got + 1))
    done
    extract '*/private/var/db/launchd.db/com.apple.launchd/overrides.plist' "$x/ov"
    if [ -f "$x/ov/overrides.plist" ]; then
        python3 -I - "$x/ov/overrides.plist" > "$out/launchd-overrides.txt" 2>/dev/null <<'PY' || rm -f "$out/launchd-overrides.txt"
import plistlib, pprint, sys
pprint.pprint(plistlib.load(open(sys.argv[1], "rb")))
PY
        got=$((got + 1))
    fi
    7z l "$disk_mnt" '*/private/var/db/.guest-macosx-firstboot/*' > "$out/firstboot-state.txt" 2>/dev/null \
        || rm -f "$out/firstboot-state.txt"
    echo "disk: read $got files from the guest's disk into $out"
    return 0
}

[ "$#" -ge 1 ] || usage
cmd=$1
shift
case $cmd in
    screenshot) cmd_screenshot "$@" ;;
    disk) cmd_disk "$@" ;;
    qmp) cmd_qmp "$@" ;;
    *) usage ;;
esac
