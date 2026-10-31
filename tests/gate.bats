#!/usr/bin/env bats
# platform: host-agnostic -- tested on Linux with KVM only
#
# bin/gate.sh is the boot gate's one command. Here it never builds, boots, downloads or signals
# anything real: PATH is the stub directory, /usr/bin and /bin (no sbin); shutdown reboot halt
# poweroff systemctl sudo refuse; ssh and scp record argv in $STUB_LOG and never run the remote
# command; packer is a stand-in that writes canned logs and a tiny fake box; bin/gate-vm.sh,
# bin/dev-install.sh, bin/verify-changed-sources.sh and bin/gate-varstore.sh are replaced through
# the seams GUEST_MACOSX_GATE_VM, GUEST_MACOSX_GATE_DEV_INSTALL, GUEST_MACOSX_GATE_VERIFY_SOURCES
# and GUEST_MACOSX_GATE_VARSTORE. The only processes a test signals are ones it started itself.

bats_require_minimum_version 1.5.0

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    SCRIPT="$REPO/bin/gate.sh"
    REAL_PYTHON3=$(command -v python3 || true)
    STUBS="$BATS_TEST_TMPDIR/stubs"
    WORK="$BATS_TEST_TMPDIR/work"
    EV="$WORK/evidence"
    export STUB_LOG="$BATS_TEST_TMPDIR/stub.log"
    export STUB_DIR="$BATS_TEST_TMPDIR/stubstate"
    export HOME="$BATS_TEST_TMPDIR/home"
    export TMPDIR="$BATS_TEST_TMPDIR/tmp"
    mkdir -p "$STUBS" "$STUB_DIR" "$HOME" "$TMPDIR" "$WORK"
    # the work dir as an earlier run left it: marked as the gate's own
    : > "$WORK/.guest-macosx-gate-work"
    : > "$STUB_LOG"
    unset MAVERICKS_BUILD_ROOT GATE_SSH_PORT GATE_ACCEL
    for c in shutdown reboot halt poweroff systemctl sudo; do
        cat > "$STUBS/$c" <<'STUB'
#!/bin/sh
echo "stub: ${0##*/} refused"
exit 1
STUB
    done
    # the gate needs these to exist; it never runs them itself, and here they refuse
    for c in qemu-system-x86_64 qemu-img python3 go dmg2img mkfs.hfsplus nasm iasl curl; do
        cat > "$STUBS/$c" <<'STUB'
#!/bin/sh
echo "stub: ${0##*/} refused"
exit 1
STUB
    done
    # kill (as `env kill`): only records; a test never signals a process it did not start
    cat > "$STUBS/kill" <<'STUB'
#!/bin/sh
printf "kill %s\n" "$*" >> "$STUB_LOG"
exit 0
STUB
    for c in ssh scp; do
        cat > "$STUBS/$c" <<'STUB'
#!/bin/sh
printf '%s %s\n' "${0##*/}" "$*" >> "$STUB_LOG"
exit 0
STUB
    done

    # packer: records argv, cwd and the env it was given; never touches the network.
    #   STUB_BUILD_FAIL=<base|layer>:<started|noqemu|launch|nospace>  that build fails so
    #   STUB_BUILD_HANG=<base|layer>[:started]  that build signals $STUB_DIR/hanging and sleeps
    #       (after saying QEMU started, with :started); STUB_HANG_IGNORE_TERM=1 ignores TERM
    #   STUB_BUILD_FAIL kinds also: guestfull (the guest's own disk fills)
    #   STUB_PACKER_INSTALL_FAIL=<source>  `plugins install <source>` fails
    #   STUB_PLUGIN_TAMPER=1  `plugins install --path` installs different bytes
    #   STUB_EXTRA_INSTALLED=<path>  `plugins installed` lists this too
    cat > "$STUBS/packer" <<'STUB'
#!/bin/sh
printf 'packer %s (cwd %s) cache=%s plugins=%s\n' "$*" "$PWD" "${PACKER_CACHE_DIR:-}" "${PACKER_PLUGIN_PATH:-}" >> "$STUB_LOG"
[ -z "${PACKER_GITHUB_API_TOKEN:-}" ] || printf 'packer %s: had the token\n' "$*" >> "$STUB_LOG"
case "$1 $2" in
"plugins install")
    if [ "$3" = --path ]; then
        d="$PACKER_PLUGIN_PATH/github.com/mavergreen/macosx"
        mkdir -p "$d"
        if [ -n "${STUB_PLUGIN_TAMPER:-}" ]; then echo tampered > "$d/packer-plugin-macosx_v0.1.0-dev_x5.0_linux_amd64"
        else cp "$4" "$d/packer-plugin-macosx_v0.1.0-dev_x5.0_linux_amd64"; fi
        exit 0
    fi
    [ "$3" != "${STUB_PACKER_INSTALL_FAIL:-}" ] || { echo "Error: GitHub API rate limit exceeded"; exit 1; }
    name=${3##*/}
    mkdir -p "$PACKER_PLUGIN_PATH/$3"
    echo "$name" > "$PACKER_PLUGIN_PATH/$3/packer-plugin-${name}_v1.1.6_x5.0_linux_amd64"
    exit 0 ;;
"plugins installed")
    find "$PACKER_PLUGIN_PATH" -type f | sort
    [ -z "${STUB_EXTRA_INSTALLED:-}" ] || echo "$STUB_EXTRA_INSTALLED"
    exit 0 ;;
esac
[ "$1" = build ] || { echo "stub packer: unexpected $*" >&2; exit 2; }
for last; do :; done
case "$last" in
*/templates/mavericks) which=base; src=mavericks; box=mavericks-10.9.5-libvirt.box
    started='==> qemu.mavericks: Starting VM, booting from CD-ROM' ;;
*/templates/mavericks-aquatransport) which=layer; src=mavericks-aquatransport; box=mavericks-10.9.5-aquatransport-libvirt.box
    started='==> qemu.mavericks-aquatransport: Starting VM, booting disk image' ;;
*/templates/snowleopard) which=base; src=snowleopard; box=snowleopard-10.6-libvirt.box
    started='==> qemu.snowleopard: Starting VM, booting from CD-ROM' ;;
*) echo "stub packer: unknown template $last" >&2; exit 2 ;;
esac
echo "qemu.$src: output will be in this color."
if [ "${STUB_BUILD_HANG:-}" = "$which" ] || [ "${STUB_BUILD_HANG:-}" = "$which:started" ]; then
    [ "${STUB_BUILD_HANG:-}" = "$which" ] || echo "$started"
    echo "$$" > "$STUB_DIR/hang.pid"
    : > "$STUB_DIR/hanging"
    [ -z "${STUB_HANG_IGNORE_TERM:-}" ] || trap '' TERM
    exec sleep 300
fi
case "${STUB_BUILD_FAIL:-}" in
"$which:started")
    echo "$started"
    echo "==> qemu.$src: Waiting for SSH to become available..."
    echo "Build 'qemu.$src' errored after 38 minutes 2 seconds: Timeout waiting for SSH."
    exit 1 ;;
"$which:noqemu")
    echo "==> qemu.$src: Retrieving ISO"
    echo "Build 'qemu.$src' errored after 4 seconds: macosx-download: GET https://swcdn.apple.com/...: 503 Service Unavailable"
    exit 1 ;;
"$which:launch")
    echo "$started"
    echo "==> qemu.$src: Error launching VM: Qemu failed to start. Please run with PACKER_LOG=1 to get more info."
    echo "Build 'qemu.$src' errored after 6 seconds: Error launching VM: Qemu failed to start."
    exit 1 ;;
"$which:killed")
    echo "$started"
    kill -KILL $$ ;;
"$which:guestfull")
    echo "$started"
    echo "    qemu.$src: cp: /Users/vagrant/big: No space left on device"
    echo "==> qemu.$src: Provisioning step had errors: Running the cleanup provisioner, if present..."
    echo "Build 'qemu.$src' errored after 30 minutes: Script exited with non-zero exit status: 1. Allowed exit codes are: [0]"
    exit 1 ;;
"$which:nospace")
    echo "$started"
    echo "==> qemu.$src: write /mnt/work/output-$src/packer-$src: no space left on device"
    echo "Build 'qemu.$src' errored after 20 minutes: write output-$src/packer-$src: no space left on device"
    exit 1 ;;
esac
echo "$started"
mkdir -p "output-$src" output "$STUB_DIR/box-$which"
: > "output-$src/packer-$src"
for f in box_0.img OVMF_CODE.fd OVMF_VARS.fd opencore.img; do echo "$which $f" > "$STUB_DIR/box-$which/$f"; done
# the box's Vagrantfile names its release, as its template renders it (STUB_BOX_RELEASE: another)
case $src in snowleopard) rel=10.6 ;; *) rel=10.9 ;; esac
printf '# rendered by templates/%s\n# guest-macosx-release: %s\n' "$src" "${STUB_BOX_RELEASE:-$rel}" > "$STUB_DIR/box-$which/Vagrantfile"
tar -czf "output/$box" -C "$STUB_DIR/box-$which" .
# what a real build leaves in the cache: Apple's media
mkdir -p "$PACKER_CACHE_DIR"
: > "$PACKER_CACHE_DIR/InstallESD.dmg"
: > "$PACKER_CACHE_DIR/installer.iso"
echo "Build 'qemu.$src' finished after 1 second."
STUB

    # the stand-in for bin/dev-install.sh: "builds" a plugin where GUEST_MACOSX_PLUGIN_BIN says
    cat > "$BATS_TEST_TMPDIR/dev-install" <<'STUB'
#!/bin/sh
printf 'dev-install GUEST_MACOSX_PLUGIN_BIN=%s PACKER_PLUGIN_PATH=%s\n' "$GUEST_MACOSX_PLUGIN_BIN" "$PACKER_PLUGIN_PATH" >> "$STUB_LOG"
[ -z "${STUB_DEV_INSTALL_FAIL:-}" ] || { echo "go: build failed" >&2; exit 1; }
if [ -n "${STUB_DEV_INSTALL_HANG:-}" ]; then echo "$$" > "$STUB_DIR/hang.pid"; exec sleep 300; fi
echo "the plugin built from the checkout $$" > "$GUEST_MACOSX_PLUGIN_BIN"
packer plugins install --path "$GUEST_MACOSX_PLUGIN_BIN" github.com/mavergreen/macosx
STUB
    cat > "$BATS_TEST_TMPDIR/verify-sources" <<'STUB'
#!/bin/sh
echo "verify-changed-sources" >> "$STUB_LOG"
[ -z "${STUB_VERIFY_HANG:-}" ] || { echo "$$" > "$STUB_DIR/hang.pid"; exec sleep 300; }
exit "${STUB_VERIFY_RC:-0}"
STUB

    # the stand-in for bin/gate-varstore.sh: records its arguments, never boots.
    #   STUB_VARSTORE_RC  its status (a warning first when not 0), STUB_VARSTORE_SAYS that warning
    cat > "$BATS_TEST_TMPDIR/gate-varstore" <<'STUB'
#!/bin/sh
echo "varstore $*" >> "$STUB_LOG"
[ ! -d "$2" ] || echo "varstore: $2 already existed" >> "$STUB_LOG"
mkdir -p "$2" && : > "$2/vars-full.fd"
rc=${STUB_VARSTORE_RC:-0}
[ "$rc" -eq 0 ] || echo "gate-varstore: warning: ${STUB_VARSTORE_SAYS:-FAIL: 799963 flash bytes programmed}" >&2
echo "gate-varstore: method: trace" >&2
exit "$rc"
STUB

    # the stand-in for bin/gate-vm.sh: records, never boots or connects.
    #   STUB_VM_BOOT_RC  boot's status (a warning first when not 0)
    #   STUB_VM_FAIL=<check>:<n>  the n-th call of guest check <check> fails, with
    #       STUB_VM_FAIL_RC (1) and STUB_VM_FAIL_LINE
    #   STUB_VM_HANG=<check>:<n>  that call signals $STUB_DIR/hanging and sleeps
    #   STUB_VM_RESTART_FAIL=<k>  the k-th restart fails as "did not restart"
    #   STUB_VM_SHUTDOWN_RC  shutdown's status
    cat > "$BATS_TEST_TMPDIR/gate-vm" <<'STUB'
#!/bin/sh
printf 'vm %s\n' "$*" >> "$STUB_LOG"
cmd=$1 state=$2
shift 2
count() { n=$(cat "$STUB_DIR/count.$1" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" > "$STUB_DIR/count.$1"; echo "$n"; }
case $cmd in
boot)
    mkdir -p "$state"
    # the release, from the box's own line, as bin/gate-vm.sh records it
    sed -n 's/^# guest-macosx-release: //p' "$1/Vagrantfile" > "$state/release"
    echo "serial: the guest booted" > "$state/serial.log"
    : > "$state/overlay.qcow2"
    echo "$$" > "$state/qemu.pid.stub"
    if [ -n "${STUB_VM_BOOT_HANG:-}" ]; then
        echo "$$" > "$STUB_DIR/hang.pid"; : > "$STUB_DIR/hanging"; exec sleep 300
    fi
    rc=${STUB_VM_BOOT_RC:-0}
    case $rc in
    0) echo "gate-vm: the guest answered SSH after 40s" >&2 ;;
    3) echo "gate-vm: warning: QEMU is already running (pid 4242, $state/qemu.pid); never two guests on one overlay" >&2 ;;
    *) echo "gate-vm: warning: boot: SSH did not answer within 600s; the end of the serial log follows" >&2 ;;
    esac
    exit "$rc" ;;
put) exit "${STUB_VM_PUT_RC:-0}" ;;
restart)
    n=$(count restart)
    if [ -n "${STUB_VM_RESTART_RC:-}" ]; then
        echo "gate-vm: warning: restart: the stand-in could not run" >&2
        exit "$STUB_VM_RESTART_RC"
    fi
    if [ "$n" = "${STUB_VM_RESTART_FAIL:-}" ]; then
        echo "gate-vm: warning: restart: the guest answered on the same boot (did not restart)" >&2
        exit 1
    fi
    exit 0 ;;
shutdown)
    rc=${STUB_VM_SHUTDOWN_RC:-0}
    [ "$rc" = 0 ] || echo "gate-vm: warning: shutdown: QEMU still running after 180s" >&2
    exit "$rc" ;;
screenshot)
    echo "not really a png" > "$1"
    # STUB_VM_LEAK: something drops a disk, an ISO and a link to an image into the evidence
    if [ -n "${STUB_VM_LEAK:-}" ]; then
        mkdir -p "${1%/*}/guest"
        : > "${1%/*}/leak.qcow2"; : > "${1%/*}/guest/leak.iso"; : > "${1%/*}/guest/leak.log.dmg"
        ln -s "$state/overlay.qcow2" "${1%/*}/leak.log"
    fi
    exit 0 ;;
kill) exit 0 ;;
ssh)
    case "$*" in
    *guest-checks.sh*)
        for a; do [ "${prev:-}" = /Users/vagrant/gate/guest-checks.sh ] && check=$a; prev=$a; done
        n=$(count "$check")
        if [ "$check:$n" = "${STUB_VM_HANG:-}" ]; then
            echo "$$" > "$STUB_DIR/hang.pid"
            : > "$STUB_DIR/hanging"
            [ -z "${STUB_HANG_IGNORE_TERM:-}" ] || trap '' TERM
            exec sleep 300
        fi
        if [ "$check:$n" = "${STUB_VM_FAIL:-}" ]; then
            echo "ok os"
            echo "${STUB_VM_FAIL_LINE:-FAIL $check: the stand-in failed call $n}"
            echo "FAIL second: never the note"
            exit "${STUB_VM_FAIL_RC:-1}"
        fi
        echo "ok $check"
        exit 0 ;;
    *"cat '/Library/Logs/DiagnosticReports/configd"*) echo "configd crashed"; exit 0 ;;
    *"cat '/Users/vagrant/Library/Logs/DiagnosticReports/configd"*) echo "user configd crashed"; exit 0 ;;
    *"cat '/Users/vagrant/Library/Logs/DiagnosticReports/Kernel"*) echo "panic text"; exit 0 ;;
    *"cat '/Library/Logs/DiagnosticReports/diskarbitrationd,"*) echo "stall text"; exit 0 ;;
    *"cat '/Library/Logs/DiagnosticReports/odd name;rm.crash'") echo "odd text"; exit 0 ;;
    *"cat '/Library/Logs/DiagnosticReports/it'\\''s_2026-10-12-101500_host.crash'") echo "quote text"; exit 0 ;;
    *"for f in"*DiagnosticReports*)
        echo "/Library/Logs/DiagnosticReports/configd_2026-10-12-101500_host.crash"
        echo "/Users/vagrant/Library/Logs/DiagnosticReports/Kernel_2026-10-12-101600_host.panic"
        echo "/Users/vagrant/Library/Logs/DiagnosticReports/configd_2026-10-12-101500_host.crash"
        echo "/Library/Logs/DiagnosticReports/disk.img"
        echo "/Library/Logs/DiagnosticReports/odd name;rm.crash"
        echo "/Library/Logs/DiagnosticReports/it's_2026-10-12-101500_host.crash"
        echo "/Library/Logs/DiagnosticReports/diskarbitrationd,ManagedClient_2026-10-13-101842_mavericks.shutdownStall"
        exit 0 ;;
    *"test -f /var/log/launchd-pid1.crash"*) [ -n "${STUB_VM_PID1CRASH:-}" ]; exit ;;
    *"cat /var/log/launchd-pid1.crash"*) echo "Sampling process 1 for 1 second"; exit 0 ;;
    *system.log*) echo "Oct 12 10:00:00 host kernel[0]: syslog line"; exit 0 ;;
    *) exit 0 ;;
    esac ;;
esac
echo "stand-in gate-vm: unexpected $cmd" >&2
exit 2
STUB
    chmod +x "$STUBS"/* "$BATS_TEST_TMPDIR/dev-install" "$BATS_TEST_TMPDIR/verify-sources" "$BATS_TEST_TMPDIR/gate-vm" \
        "$BATS_TEST_TMPDIR/gate-varstore"
    export PATH="$STUBS:/usr/bin:/bin"
    export GUEST_MACOSX_GATE_VM="$BATS_TEST_TMPDIR/gate-vm"
    export GUEST_MACOSX_GATE_DEV_INSTALL="$BATS_TEST_TMPDIR/dev-install"
    export GUEST_MACOSX_GATE_VERIFY_SOURCES="$BATS_TEST_TMPDIR/verify-sources"
    export GUEST_MACOSX_GATE_VARSTORE="$BATS_TEST_TMPDIR/gate-varstore"
    export GATE_KVM="$BATS_TEST_TMPDIR/kvm"; : > "$GATE_KVM"
    export GATE_SPACE_AVAILABLE_KIB=$((80 * 1024 * 1024))
    export GATE_BUILD_SPACE_AVAILABLE_KIB=$((40 * 1024 * 1024))
    if [[ "$BATS_TEST_DESCRIPTION" == F32:* ]]; then
        # Filesystem-only: stop after cleanup, before port checks or any build/VM seam.
        PORT=1
        export GATE_REQUIRED_COMMANDS=guest-macosx-f32-deliberately-missing
    else
        PORT=$(free_port)
    fi
    BG=
}

teardown() {
    [ -z "${BG:-}" ] || kill "$BG" 2>/dev/null || true
    [ -z "${LISTENER:-}" ] || kill "$LISTENER" 2>/dev/null || true
    [ ! -s "$STUB_DIR/hang.pid" ] || kill -KILL "$(cat "$STUB_DIR/hang.pid")" 2>/dev/null || true
    for p in ${OWNED:-}; do kill "$p" 2>/dev/null || true; done
}

# a TCP port on 127.0.0.1 nothing listens on (bash refuses to bind nothing, so ask python)
free_port() {
    if [ -n "$REAL_PYTHON3" ]; then
        "$REAL_PYTHON3" -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
    else
        echo 1
    fi
}

gate() {
    run "$SCRIPT" --release 10.9 --work-dir "$WORK" --restarts 2 --ssh-port "$PORT" "$@"
}

ALL="preflight plugin build-base build-layer varstore boot versions health reach host-uuid restart-1 restart-2 shutdown"

# The steps a 10.6 run marks not applicable (bin/gate.sh's not_applicable).
SL_NA="build-layer reach"

# [NA=<steps>] expect_rows <step> <result>: steps.tsv is every step in order, pass (or n/a, for
# the steps in NA) before <step>, <result> at it, skip after it. With no arguments, every step
# passes or is n/a.
expect_rows() {
    local want="" s seen=false
    for s in $ALL; do
        if [ "$seen" = true ]; then want="$want$s skip"$'\n'
        elif [ "$s" = "${1:-}" ]; then want="$want$s $2"$'\n'; seen=true
        elif [[ " ${NA:-} " == *" $s "* ]]; then want="$want$s n/a"$'\n'
        else want="$want$s pass"$'\n'
        fi
    done
    got=$(awk -F'\t' '{ print $1 " " $2 }' "$EV/steps.tsv")
    [ "$got"$'\n' = "$want" ] || { echo "got:"; echo "$got"; echo "want:"; printf '%s' "$want"; false; }
    awk -F'\t' 'NF != 4 { exit 1 }' "$EV/steps.tsv"
}

note_of() { awk -F'\t' -v s="$1" '$1 == s { print $4 }' "$EV/steps.tsv"; }

@test "safety: the dangerous names refuse, and the stub ssh never runs the remote command" {
    for c in shutdown reboot halt poweroff systemctl sudo; do
        run "$STUBS/$c" now
        [ "$status" -eq 1 ]
        [ "$output" = "stub: $c refused" ]
        [ "$(command -v "$c")" = "$STUBS/$c" ]
    done
    marker="$BATS_TEST_TMPDIR/ran"
    run "$STUBS/ssh" host "touch $marker"
    [ ! -e "$marker" ]
    echo data > "$BATS_TEST_TMPDIR/src"
    run "$STUBS/scp" -o "ProxyCommand=touch $marker" "$BATS_TEST_TMPDIR/src" "$BATS_TEST_TMPDIR/dst"
    [ ! -e "$marker" ]
    [ ! -e "$BATS_TEST_TMPDIR/dst" ]
    [[ "$PATH" != *sbin* ]]
    # the gate's own tools resolve to stand-ins, never to the host's
    for c in packer go qemu-system-x86_64 qemu-img curl; do
        [ "$(command -v "$c")" = "$STUBS/$c" ]
    done
    [ "$GUEST_MACOSX_GATE_VM" != "$REPO/bin/gate-vm.sh" ]
}

@test "a full pass writes every step in order, a summary, and exits 0" {
    gate
    [ "$status" -eq 0 ]
    expect_rows
    [ "$(head -n 1 "$EV/summary.md")" = "Passed: every step, 2 restarts" ]
    for s in $ALL; do [ -f "$EV/$s.log" ]; done
    # a pass collects no failure evidence
    [ ! -e "$EV/screen.png" ]
    [ ! -e "$EV/serial.log" ]
    [[ "$output" == *"Passed: every step, 2 restarts"* ]]
}

@test "the builds: the exact packer commands, from their own directories, with a fresh plugin path and cache" {
    gate --accel kvm
    [ "$status" -eq 0 ]
    grep -qxF "packer build -color=false -var updates=security -var accelerator=kvm -var headless=true $REPO/templates/mavericks (cwd $WORK/base) cache=$WORK/packer-cache plugins=$WORK/plugins" "$STUB_LOG"
    grep -qxF "packer build -color=false -var accelerator=kvm -var headless=true -var base_box=$WORK/box-base $REPO/templates/mavericks-aquatransport (cwd $WORK/layer) cache=$WORK/packer-cache plugins=$WORK/plugins" "$STUB_LOG"
    # each box unpacked, packer's own output gone
    [ "$(cat "$WORK/box-base/box_0.img")" = "base box_0.img" ]
    [ "$(cat "$WORK/box-layer/box_0.img")" = "layer box_0.img" ]
    [ ! -e "$WORK/base" ]
    [ ! -e "$WORK/layer" ]
    # the plugin: built where the gate says, then qemu and vagrant by install, never packer init
    grep -qxF "dev-install GUEST_MACOSX_PLUGIN_BIN=$WORK/plugin-build/packer-plugin-macosx PACKER_PLUGIN_PATH=$WORK/plugins" "$STUB_LOG"
    # pinned: qemu at the version whose "Starting VM" lines classify_build_failure reads
    grep -q '^packer plugins install github.com/hashicorp/qemu v1.1.6 (cwd ' "$STUB_LOG"
    grep -q '^packer plugins install github.com/hashicorp/vagrant v1.1.7 (cwd ' "$STUB_LOG"
    [ "$(grep -c '^packer init' "$STUB_LOG")" -eq 0 ]
}

@test "the GitHub token reaches packer plugins install for qemu and vagrant, never a build" {
    PACKER_GITHUB_API_TOKEN=secret gate
    [ "$status" -eq 0 ]
    grep -qxF "packer plugins install github.com/hashicorp/qemu v1.1.6: had the token" "$STUB_LOG"
    grep -qxF "packer plugins install github.com/hashicorp/vagrant v1.1.7: had the token" "$STUB_LOG"
    [ "$(grep -c 'had the token' "$STUB_LOG")" -eq 2 ]
    grep -q '^packer build' "$STUB_LOG"
}

@test "plugin: a work dir whose path says macosx still finds exactly one macosx plugin" {
    WORK="$BATS_TEST_TMPDIR/trees/packer-plugin-macosx/work"
    EV="$WORK/evidence"
    gate
    [ "$status" -eq 0 ]
    expect_rows
}

@test "the guest: booted from the layer box, the checks staged in /Users/vagrant/gate, run with the checkout's pins" {
    gate
    [ "$status" -eq 0 ]
    grep -qxF "vm boot $WORK/vm $WORK/box-layer" "$STUB_LOG"
    grep -qxF "vm put $WORK/vm $REPO/gate/guest-checks.sh /Users/vagrant/gate/guest-checks.sh" "$STUB_LOG"
    grep -qxF "vm put $WORK/vm $REPO/gate/crash-allowlist.tsv /Users/vagrant/gate/crash-allowlist.tsv" "$STUB_LOG"
    pins=$("$REPO/bin/gate-expected.sh" | awk '{ printf "%s ", $2 }')
    [ "$(grep -cxF "vm ssh $WORK/vm sh /Users/vagrant/gate/guest-checks.sh versions ${pins% }" "$STUB_LOG")" -eq 3 ]
    [ "$(grep -cxF "vm ssh $WORK/vm sh /Users/vagrant/gate/guest-checks.sh health /Users/vagrant/gate/crash-allowlist.tsv 10.9.5 13F1911 crash hang panic spin diag shutdownStall" "$STUB_LOG")" -eq 3 ]
    [ "$(grep -cxF "vm ssh $WORK/vm sh /Users/vagrant/gate/guest-checks.sh reach" "$STUB_LOG")" -eq 3 ]
    # host-uuid on the cold boot only: a restart keeps the NVRAM its boot wrote (docs/decisions/0024)
    [ "$(grep -cxF "vm ssh $WORK/vm sh /Users/vagrant/gate/guest-checks.sh host-uuid" "$STUB_LOG")" -eq 1 ]
    [ "$(grep -cxF "vm restart $WORK/vm" "$STUB_LOG")" -eq 2 ]
    grep -qxF "vm shutdown $WORK/vm" "$STUB_LOG"
}

@test "the default restart count is 10, and the default work dir is under MAVERICKS_BUILD_ROOT" {
    export MAVERICKS_BUILD_ROOT="$BATS_TEST_TMPDIR/root"
    run "$SCRIPT" --release 10.9 --ssh-port "$PORT"
    [ "$status" -eq 0 ]
    tsv="$MAVERICKS_BUILD_ROOT/gate/evidence/steps.tsv"
    [ "$(grep -c '^restart-' "$tsv")" -eq 10 ]
    [ "$(sed -n '$p' "$tsv" | cut -f1)" = shutdown ]
    [ "$(head -n 1 "$MAVERICKS_BUILD_ROOT/gate/evidence/summary.md")" = "Passed: every step, 10 restarts" ]
}

@test "without MAVERICKS_BUILD_ROOT the work dir is \$TMPDIR/mm-build/gate; --evidence-dir puts evidence elsewhere" {
    run "$SCRIPT" --release 10.9 --restarts 1 --ssh-port "$PORT" --evidence-dir "$BATS_TEST_TMPDIR/ev"
    [ "$status" -eq 0 ]
    [ -d "$TMPDIR/mm-build/gate/box-layer" ]
    [ -f "$BATS_TEST_TMPDIR/ev/steps.tsv" ]
    [ ! -e "$TMPDIR/mm-build/gate/evidence" ]
}

@test "--packer-cache: that cache is used and kept; without it the cache is fresh" {
    mkdir -p "$BATS_TEST_TMPDIR/cache" "$WORK/packer-cache"
    echo kept > "$BATS_TEST_TMPDIR/cache/earlier"
    echo stale > "$WORK/packer-cache/earlier"
    gate --packer-cache "$BATS_TEST_TMPDIR/cache"
    [ "$status" -eq 0 ]
    [ "$(cat "$BATS_TEST_TMPDIR/cache/earlier")" = kept ]
    grep -q "^packer build .* cache=$BATS_TEST_TMPDIR/cache plugins=" "$STUB_LOG"
    gate
    [ "$status" -eq 0 ]
    [ ! -e "$WORK/packer-cache/earlier" ]
}

# --- preflight ---

@test "F32: default cache symlink cannot delete an outside directory" {
    mkdir -p "$BATS_TEST_TMPDIR/outside"
    echo kept > "$BATS_TEST_TMPDIR/outside/sentinel"
    ln -s "$BATS_TEST_TMPDIR/outside" "$WORK/packer-cache"
    gate
    [ "$(cat "$BATS_TEST_TMPDIR/outside/sentinel")" = kept ]
    [ "$status" -eq 2 ]
    [ -L "$WORK/packer-cache" ]
    [ ! -s "$STUB_LOG" ]
}

@test "F32: explicit cache under base is preserved on rejection" {
    mkdir -p "$WORK/base/cache"
    echo kept > "$WORK/base/cache/sentinel"
    gate --packer-cache "$WORK/base/cache"
    [ "$(cat "$WORK/base/cache/sentinel")" = kept ]
    [ "$status" -eq 2 ]
    [ ! -s "$STUB_LOG" ]
}

@test "F32: explicit cache symlink below base cannot hide the managed pathname" {
    mkdir -p "$WORK/base" "$BATS_TEST_TMPDIR/outside"
    echo kept > "$BATS_TEST_TMPDIR/outside/sentinel"
    ln -s "$BATS_TEST_TMPDIR/outside" "$WORK/base/cache"
    gate --packer-cache "$WORK/base/cache"
    [ "$status" -eq 2 ]
    [ -L "$WORK/base/cache" ]
    [ "$(cat "$BATS_TEST_TMPDIR/outside/sentinel")" = kept ]
}

@test "F32: explicit cache below default evidence is rejected before cleanup" {
    mkdir -p "$EV/cache"
    echo kept > "$EV/cache/sentinel"
    gate --packer-cache "$EV/cache"
    [ "$(cat "$EV/cache/sentinel")" = kept ]
    [ "$status" -eq 2 ]
}

@test "F32: explicit cache cannot equal or descend from any managed cleanup path" {
    for m in vm varstore base box-base layer box-layer plugins plugin-build check.out space-low expected.txt; do
        for suffix in '' /cache; do
            mkdir -p "$WORK/$m$suffix"
            echo kept > "$WORK/$m$suffix/sentinel"
            gate --packer-cache "$WORK/$m$suffix"
            [ "$status" -eq 2 ] || { echo "$m$suffix: status $status"; false; }
            [ "$(cat "$WORK/$m$suffix/sentinel")" = kept ]
        done
    done
    [ ! -s "$STUB_LOG" ]
}

@test "F32: explicit cache cannot be the work directory or its ancestor" {
    for cache_dir in "$WORK" "$BATS_TEST_TMPDIR" /; do
        gate --evidence-dir "$BATS_TEST_TMPDIR/../f32-evidence-$BATS_TEST_NUMBER" --packer-cache "$cache_dir"
        [ "$status" -eq 2 ] || { echo "$cache_dir: status $status: $output"; false; }
        [[ "$output" == *"overlap"* ]]
    done
    [ ! -s "$STUB_LOG" ]
}

@test "F32: explicit cache aliases into managed directories are rejected" {
    for m in vm varstore base box-base layer box-layer plugins plugin-build; do
        mkdir -p "$WORK/$m/cache"
        ln -s "$WORK/$m" "$BATS_TEST_TMPDIR/alias-$m"
        echo kept > "$WORK/$m/cache/sentinel"
        gate --packer-cache "$BATS_TEST_TMPDIR/alias-$m/cache"
        [ "$status" -eq 2 ] || { echo "$m: status $status"; false; }
        [ "$(cat "$WORK/$m/cache/sentinel")" = kept ]
    done
}

@test "F32: explicit cache cannot overlap existing physical managed aliases" {
    for m in vm varstore base box-base layer box-layer plugins plugin-build; do
        mkdir -p "$BATS_TEST_TMPDIR/outside-$m/child"
        ln -s "$BATS_TEST_TMPDIR/outside-$m/child" "$WORK/$m"
        for suffix in '' /child /child/cache; do
            mkdir -p "$BATS_TEST_TMPDIR/outside-$m$suffix"
            echo kept > "$BATS_TEST_TMPDIR/outside-$m$suffix/sentinel"
            gate --packer-cache "$BATS_TEST_TMPDIR/outside-$m$suffix"
            [ "$status" -eq 2 ] || { echo "$m$suffix: status $status"; false; }
            [ "$(cat "$BATS_TEST_TMPDIR/outside-$m$suffix/sentinel")" = kept ]
            [ -L "$WORK/$m" ]
        done
    done
}

@test "F32: fresh default and normal default cleanup still work" {
    gate
    [ "$status" -eq 3 ]
    [ -d "$WORK/packer-cache" ]
    echo stale > "$WORK/packer-cache/sentinel"
    gate
    [ "$status" -eq 3 ]
    [ -d "$WORK/packer-cache" ]
    [ ! -e "$WORK/packer-cache/sentinel" ]
    [ ! -s "$STUB_LOG" ]
}

@test "F32: external explicit cache is preserved while managed directories are cleaned" {
    mkdir -p "$BATS_TEST_TMPDIR/cache" "$WORK/base"
    echo kept > "$BATS_TEST_TMPDIR/cache/sentinel"
    echo stale > "$WORK/base/sentinel"
    ln -s "$BATS_TEST_TMPDIR/cache" "$BATS_TEST_TMPDIR/cache-alias"
    gate --packer-cache "$BATS_TEST_TMPDIR/cache-alias"
    [ "$status" -eq 3 ]
    [ "$(cat "$BATS_TEST_TMPDIR/cache/sentinel")" = kept ]
    [ ! -e "$WORK/base" ]
    [ ! -s "$STUB_LOG" ]
}

@test "F32: explicitly selected default cache location is preserved" {
    mkdir -p "$WORK/packer-cache"
    echo kept > "$WORK/packer-cache/sentinel"
    gate --packer-cache "$WORK/packer-cache"
    [ "$status" -eq 3 ]
    [ "$(cat "$WORK/packer-cache/sentinel")" = kept ]
    [ ! -s "$STUB_LOG" ]
}

@test "preflight: the commands it checks are the ones the brief lists, each named in its log" {
    gate
    for c in packer qemu-system-x86_64 qemu-img ssh scp python3 go dmg2img mkfs.hfsplus nasm iasl; do
        grep -qxF "found $c: $STUBS/$c" "$EV/preflight.log"
    done
}

@test "preflight: a missing command is infra, by name, and nothing else runs" {
    GATE_REQUIRED_COMMANDS="packer guest-macosx-no-such-command" gate
    [ "$status" -eq 3 ]
    expect_rows preflight infra
    [[ "$(note_of preflight)" == *"guest-macosx-no-such-command"* ]]
    [ "$(grep -c '^packer\|^vm \|^dev-install' "$STUB_LOG")" -eq 0 ]
    [[ "$(head -n 1 "$EV/summary.md")" == "No clear regression: CI itself failed at preflight -- "*"guest-macosx-no-such-command"* ]]
}

@test "preflight: no usable KVM is infra" {
    rm -f "$GATE_KVM"
    gate
    [ "$status" -eq 3 ]
    expect_rows preflight infra
    [[ "$(note_of preflight)" == *"$GATE_KVM"* ]]
}

@test "preflight: less than 60 GiB free in the work dir is infra" {
    GATE_SPACE_AVAILABLE_KIB=$((60 * 1024 * 1024 - 1)) gate
    [ "$status" -eq 3 ]
    expect_rows preflight infra
    [[ "$(note_of preflight)" == *"$WORK has 59 GiB free"*"60 GiB"* ]]
    GATE_SPACE_AVAILABLE_KIB=$((60 * 1024 * 1024)) gate
    [ "$status" -eq 0 ]
}

@test "Review Focus 1: the SSH port already answering is infra naming the port, and no guest is ever talked to" {
    [ -n "$REAL_PYTHON3" ] || skip "needs python3 to hold a port open"
    "$REAL_PYTHON3" -c '
import socket, sys, time
s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("127.0.0.1", 0)); s.listen(5)
open(sys.argv[1], "w").write(str(s.getsockname()[1]))
time.sleep(120)' "$BATS_TEST_TMPDIR/port" 3>&- &
    LISTENER=$!
    for _ in $(seq 1 100); do [ -s "$BATS_TEST_TMPDIR/port" ] && break; sleep 0.1; done
    PORT=$(cat "$BATS_TEST_TMPDIR/port")
    gate
    [ "$status" -eq 3 ]
    expect_rows preflight infra
    [[ "$(note_of preflight)" == *"127.0.0.1:$PORT"* ]]
    [ "$(grep -c '^vm \|^packer build' "$STUB_LOG")" -eq 0 ]
}

@test "preflight: a stale pin is infra, 'pins not filled'" {
    STUB_VERIFY_RC=1 gate
    [ "$status" -eq 3 ]
    expect_rows preflight infra
    [[ "$(note_of preflight)" == *"pins not filled"* ]]
}

@test "preflight: a QEMU an earlier run left on the work dir is infra naming its pid, and its files stay" {
    mkdir -p "$WORK/vm" "$WORK/box-layer"
    : > "$WORK/box-layer/box_0.img"
    sleep 300 3>&- &
    BG=$!
    echo "$BG" > "$WORK/vm/qemu.pid"
    # ps calls the sleep QEMU, as gate-vm.sh's own tests do
    # ps calls the pid in ps.pid, beside it, QEMU
    echo "$BG" > "$STUBS/ps.pid"
    cat > "$STUBS/ps" <<'STUB'
#!/bin/sh
[ "$2" = "$(cat "$(dirname "$0")/ps.pid")" ] && { echo qemu-system-x86_64; exit 0; }
exec /usr/bin/ps "$@"
STUB
    [ -x /usr/bin/ps ] || sed -i 's#/usr/bin/ps#/bin/ps#' "$STUBS/ps"
    chmod +x "$STUBS/ps"
    gate
    [ "$status" -eq 3 ]
    expect_rows preflight infra
    [[ "$(note_of preflight)" == *"pid $BG"* ]]
    [ -f "$WORK/box-layer/box_0.img" ]
    [ -f "$WORK/vm/qemu.pid" ]
    [ "$(grep -c '^vm ' "$STUB_LOG")" -eq 0 ]
}

@test "preflight: an earlier run's qmp.dir that climbs out of /tmp/gate-qmp.* is never removed" {
    qdir=$(mktemp -d /tmp/gate-qmp.XXXXXX)
    mkdir -p "$WORK/vm" "$BATS_TEST_TMPDIR/victim"
    : > "$BATS_TEST_TMPDIR/victim/keep"
    echo "$qdir/../..$BATS_TEST_TMPDIR/victim" > "$WORK/vm/qmp.dir"
    gate
    rm -rf "$qdir"
    [ "$status" -eq 0 ]
    [ -f "$BATS_TEST_TMPDIR/victim/keep" ]
}

# --- plugin ---

@test "plugin: the checkout's plugin failing to build is infra" {
    STUB_DEV_INSTALL_FAIL=1 gate
    [ "$status" -eq 3 ]
    expect_rows plugin infra
}

@test "plugin: packer failing to install the qemu plugin is infra, naming it" {
    STUB_PACKER_INSTALL_FAIL=github.com/hashicorp/qemu gate
    [ "$status" -eq 3 ]
    expect_rows plugin infra
    [[ "$(note_of plugin)" == *"github.com/hashicorp/qemu"* ]]
}

@test "plugin: a second macosx plugin on the path is infra, not the checkout's" {
    STUB_EXTRA_INSTALLED="$HOME/.config/packer/plugins/github.com/mavergreen/macosx/packer-plugin-macosx_v0.20261005.7_x5.0_linux_amd64" gate
    [ "$status" -eq 3 ]
    expect_rows plugin infra
    [[ "$(note_of plugin)" == *"the plugin under test is not the checkout's"* ]]
}

@test "plugin: an installed binary that is not the one just built is infra" {
    STUB_PLUGIN_TAMPER=1 gate
    [ "$status" -eq 3 ]
    expect_rows plugin infra
    [[ "$(note_of plugin)" == *"the plugin under test is not the checkout's"* ]]
}

# --- builds: classify_build_failure ---
# The "QEMU started" line is packer-plugin-qemu v1.1.6's own, builder/qemu/step_run.go:84 and :87:
#   s.ui.Say("Starting VM, booting from CD-ROM")  (the base: disk_image = false)
#   s.ui.Say("Starting VM, booting disk image")   (the layer: disk_image = true)
# and QEMU dying at once is step_run.go:59, "Error launching VM: %s": after "Starting VM", so a
# fail like any other (most likely the checkout's own template's QEMU arguments). The stub packer
# prints them exactly as packer's UI does: "==> qemu.<source>: <message>".

@test "build-base: a failure after QEMU started is a fail, with packer's error as the note" {
    STUB_BUILD_FAIL=base:started gate
    [ "$status" -eq 1 ]
    expect_rows build-base fail
    [[ "$(note_of build-base)" == *"Timeout waiting for SSH"* ]]
    [[ "$(head -n 1 "$EV/summary.md")" == "Failed: build-base -- packer failed after QEMU started: "*"(no control run; run bin/gate.sh on main to compare)" ]]
}

@test "build-layer: a failure after QEMU started is a fail" {
    STUB_BUILD_FAIL=layer:started gate
    [ "$status" -eq 1 ]
    expect_rows build-layer fail
}

@test "build: a failure before QEMU started (a download) is infra" {
    STUB_BUILD_FAIL=base:noqemu gate
    [ "$status" -eq 3 ]
    expect_rows build-base infra
    [[ "$(note_of build-base)" == *"503 Service Unavailable"* ]]
    STUB_BUILD_FAIL=layer:noqemu gate
    [ "$status" -eq 3 ]
    expect_rows build-layer infra
}

@test "build: QEMU failing to launch after Starting VM is a fail (the template's own arguments)" {
    STUB_BUILD_FAIL=layer:launch gate
    [ "$status" -eq 1 ]
    expect_rows build-layer fail
    [[ "$(note_of build-layer)" == *"Error launching VM: Qemu failed to start."* ]]
}

@test "Review Focus 4: the disk filling after QEMU started is infra, 'no space left on device'" {
    STUB_BUILD_FAIL=base:nospace gate
    [ "$status" -eq 3 ]
    expect_rows build-base infra
    [[ "$(note_of build-base)" == *"no space left on device"* ]]
    STUB_BUILD_FAIL=layer:nospace gate
    [ "$status" -eq 3 ]
    expect_rows build-layer infra
}

# --- the guest ---

@test "boot: the guest never answering is a fail, with gate-vm's reason" {
    STUB_VM_BOOT_RC=1 gate
    [ "$status" -eq 1 ]
    expect_rows boot fail
    [[ "$(note_of boot)" == *"SSH did not answer within 600s"* ]]
    # the QEMU this run booted is killed, and its evidence kept
    grep -qxF "vm kill $WORK/vm" "$STUB_LOG"
    grep -qxF "vm screenshot $WORK/vm $EV/screen.png" "$STUB_LOG"
    [ "$(cat "$EV/serial.log")" = "serial: the guest booted" ]
}

@test "boot: refused with 3 (another run's QEMU) is infra, and that QEMU is never killed" {
    STUB_VM_BOOT_RC=3 gate
    [ "$status" -eq 3 ]
    expect_rows boot infra
    [[ "$(note_of boot)" == *"QEMU is already running"* ]]
    [ "$(grep -c '^vm kill\|^vm screenshot\|^vm ssh' "$STUB_LOG")" -eq 0 ]
}

@test "boot: the checks failing to reach the guest is a fail" {
    STUB_VM_PUT_RC=255 gate
    [ "$status" -eq 1 ]
    expect_rows boot fail
    [[ "$(note_of boot)" == *"guest-checks.sh"* ]]
}

@test "varstore: the layer box's firmware, in its own state dir, before the guest's first boot; the state goes after" {
    mkdir -p "$WORK/varstore" && : > "$WORK/varstore/left-by-an-earlier-run"
    gate
    [ "$status" -eq 0 ]
    expect_rows
    grep -qxF "varstore $WORK/box-layer $WORK/varstore" "$STUB_LOG"
    ! grep -q "already existed" "$STUB_LOG" || false
    [ ! -e "$WORK/varstore" ]
    # before the boot
    v=$(grep -n "^varstore " "$STUB_LOG" | cut -d: -f1)
    b=$(grep -n "^vm boot " "$STUB_LOG" | cut -d: -f1)
    [ "$v" -lt "$b" ]
    grep -qF "gate-varstore: method: trace" "$EV/varstore.log"
}

@test "varstore: a reclaim that programs every byte is a fail, the guard's warning is the note, and no guest boots" {
    STUB_VARSTORE_RC=1 STUB_VARSTORE_SAYS="FAIL: 799963 flash bytes programmed before \"BdsDxe:\" on a nearly full variable store, more than 100000" gate
    [ "$status" -eq 1 ]
    expect_rows varstore fail
    [ "$(note_of varstore)" = 'FAIL: 799963 flash bytes programmed before "BdsDxe:" on a nearly full variable store, more than 100000' ]
    ! grep -q "^vm boot " "$STUB_LOG" || false
    [ ! -e "$WORK/varstore" ]
}

@test "varstore: the guard unable to run is infra, naming why" {
    STUB_VARSTORE_RC=3 STUB_VARSTORE_SAYS="QEMU did not start" gate
    [ "$status" -eq 3 ]
    expect_rows varstore infra
    [ "$(note_of varstore)" = "bin/gate-varstore.sh could not run (status 3): QEMU did not start" ]
}

@test "versions: a check that ran and failed is a fail; the note is the first FAIL line" {
    STUB_VM_FAIL=versions:1 STUB_VM_FAIL_LINE="FAIL lilu: the guest runs 1.6.9, the checkout pins 1.7.0" gate
    [ "$status" -eq 1 ]
    expect_rows versions fail
    [ "$(note_of versions)" = "lilu: the guest runs 1.6.9, the checkout pins 1.7.0" ]
    [ "$(head -n 1 "$EV/summary.md")" = "Failed: versions -- lilu: the guest runs 1.6.9, the checkout pins 1.7.0 (no control run; run bin/gate.sh on main to compare)" ]
    grep -qxF "vm kill $WORK/vm" "$STUB_LOG"
}

@test "health: fail" {
    STUB_VM_FAIL=health:1 gate
    [ "$status" -eq 1 ]
    expect_rows health fail
    [ "$(note_of health)" = "health: the stand-in failed call 1" ]
}

@test "reach: fail" {
    STUB_VM_FAIL=reach:1 gate
    [ "$status" -eq 1 ]
    expect_rows reach fail
}

@test "host-uuid: a cold boot without its host UUID is a fail; the note is the first FAIL line" {
    STUB_VM_FAIL=host-uuid:1 STUB_VM_FAIL_LINE="FAIL host-uuid: the kernel had no host UUID when this boot's daemons asked: x" gate
    [ "$status" -eq 1 ]
    expect_rows host-uuid fail
    [ "$(note_of host-uuid)" = "host-uuid: the kernel had no host UUID when this boot's daemons asked: x" ]
}

@test "a check whose SSH dies (255) or times out (124) after a good boot is a fail, said so" {
    STUB_VM_FAIL=health:1 STUB_VM_FAIL_RC=255 STUB_VM_FAIL_LINE="nothing" gate
    [ "$status" -eq 1 ]
    expect_rows health fail
    [[ "$(note_of health)" == *"SSH"* ]]
    STUB_VM_FAIL=reach:1 STUB_VM_FAIL_RC=124 STUB_VM_FAIL_LINE="nothing" gate
    [ "$status" -eq 1 ]
    expect_rows reach fail
    [[ "$(note_of reach)" == *"did not finish"* ]]
}

@test "a check's usage error (2) is infra: the gate itself is broken" {
    STUB_VM_FAIL=versions:1 STUB_VM_FAIL_RC=2 gate
    [ "$status" -eq 3 ]
    expect_rows versions infra
}

@test "restart-1: a restart that did not happen is a fail, naming the restart" {
    STUB_VM_RESTART_FAIL=1 gate
    [ "$status" -eq 1 ]
    expect_rows restart-1 fail
    [ "$(note_of restart-1)" = "restart 1 of 2: restart: the guest answered on the same boot (did not restart)" ]
}

@test "restart-2: a check failing after the restart is a fail naming which check" {
    STUB_VM_FAIL=health:3 STUB_VM_FAIL_LINE="FAIL reports: configd_2026-10-12-101500_host.crash" gate
    [ "$status" -eq 1 ]
    expect_rows restart-2 fail
    [ "$(note_of restart-2)" = "restart 2 of 2: health failed -- reports: configd_2026-10-12-101500_host.crash" ]
    # every check still ran after that restart
    [ "$(grep -c 'guest-checks.sh reach' "$STUB_LOG")" -eq 3 ]
}

@test "shutdown: the guest not powering off is a fail" {
    STUB_VM_SHUTDOWN_RC=1 gate
    [ "$status" -eq 1 ]
    expect_rows shutdown fail
    [[ "$(note_of shutdown)" == *"QEMU still running after 180s"* ]]
}

# --- evidence ---

@test "a failure collects the screen, the serial log, the guest's reports and system.log" {
    STUB_VM_FAIL=health:1 gate
    [ "$status" -eq 1 ]
    [ -f "$EV/screen.png" ]
    [ -f "$EV/serial.log" ]
    # named by where they came from, so the same name in two places never overwrites
    [ "$(cat "$EV/guest/system-configd_2026-10-12-101500_host.crash")" = "configd crashed" ]
    [ "$(cat "$EV/guest/user-vagrant-configd_2026-10-12-101500_host.crash")" = "user configd crashed" ]
    [ "$(cat "$EV/guest/user-vagrant-Kernel_2026-10-12-101600_host.panic")" = "panic text" ]
    [ "$(cat "$EV/system.log")" = "Oct 12 10:00:00 host kernel[0]: syslog line" ]
    grep -qxF "vm ssh $WORK/vm tail -n 500 /var/log/system.log" "$STUB_LOG"
    # every kind health judges is kept: a shutdown stall, its commas and all
    [ "$(cat "$EV/guest/system-diskarbitrationd,ManagedClient_2026-10-13-101842_mavericks.shutdownStall")" = "stall text" ]
    # any name is fetched, quoted for the guest's shell as one word, and kept under a tame name
    [ "$(cat "$EV/guest/system-odd_name_rm.crash")" = "odd text" ]
    [ "$(cat "$EV/guest/system-it_s_2026-10-12-101500_host.crash")" = "quote text" ]
    # a file of no report kind stays in the guest (health has failed it, by name)
    [ "$(grep -c 'disk.img' "$STUB_LOG")" -eq 0 ]
}

@test "evidence never holds an image, box, disk, DMG or ISO, wherever one is planted" {
    mkdir -p "$WORK/evidence/sub"
    for f in a.img b.box c.qcow2 d.dmg e.iso; do
        : > "$WORK/$f"
        : > "$WORK/evidence/$f"
        : > "$WORK/evidence/sub/$f"
    done
    ln -s "$WORK/a.img" "$WORK/evidence/link.log"
    STUB_VM_LEAK=1 STUB_VM_FAIL=reach:1 gate
    [ "$status" -eq 1 ]
    [ -f "$WORK/box-layer/box_0.img" ]
    [ -f "$WORK/packer-cache/InstallESD.dmg" ]
    [ -f "$WORK/vm/overlay.qcow2" ]
    run find "$EV" \( -name '*.img' -o -name '*.box' -o -name '*.qcow2' -o -name '*.dmg' -o -name '*.iso' -o -type l \)
    [ -z "$output" ]
    # only the allowlist: tsv md txt log png, and the guest's text reports
    run find "$EV" -type f ! \( -name '*.tsv' -o -name '*.md' -o -name '*.txt' -o -name '*.log' -o -name '*.png' -o -path "$EV/guest/*.crash" -o -path "$EV/guest/*.panic" -o -path "$EV/guest/*.shutdownStall" \)
    [ -z "$output" ]
    [ ! -e "$EV/guest/disk.img" ]
}

@test "a non-empty --evidence-dir is refused before anything runs" {
    mkdir -p "$BATS_TEST_TMPDIR/ev"
    : > "$BATS_TEST_TMPDIR/ev/keep"
    gate --evidence-dir "$BATS_TEST_TMPDIR/ev"
    [ "$status" -eq 2 ]
    [ -f "$BATS_TEST_TMPDIR/ev/keep" ]
    [ ! -s "$STUB_LOG" ]
}

# --- interrupts (Review Focus 5) ---

# start_gate <env...>: run the gate in the background with SIGINT at its default, as a terminal
# would leave it (a background job of a non-interactive shell starts with SIGINT ignored, which
# bash cannot trap); wait until the stand-in that hangs says so.
start_gate() {
    [ -n "$REAL_PYTHON3" ] || skip "needs python3 to start the gate with SIGINT at its default"
    env "$@" "$REAL_PYTHON3" -c 'import os, signal, sys
signal.signal(signal.SIGINT, signal.SIG_DFL)
os.execv(sys.argv[1], sys.argv[1:])' "$SCRIPT" --release 10.9 --work-dir "$WORK" --restarts 2 --ssh-port "$PORT" \
        > "$BATS_TEST_TMPDIR/gate.out" 2>&1 3>&- &
    BG=$!
    for _ in $(seq 1 100); do [ -e "$STUB_DIR/hanging" ] && return 0; sleep 0.1; done
    echo "the stand-in never hung"; cat "$BATS_TEST_TMPDIR/gate.out"; false
}

stop_gate() { # signal
    kill -"$1" "$BG"
    st=0
    wait "$BG" || st=$?
    BG=
}

@test "Review Focus 5: TERM during a check kills the QEMU this run booted, writes infra 'interrupted', keeps evidence" {
    start_gate STUB_VM_HANG=health:1
    stop_gate TERM
    [ "$st" -eq 3 ]
    expect_rows health infra
    [ "$(note_of health)" = interrupted ]
    grep -qxF "vm kill $WORK/vm" "$STUB_LOG"
    [ -f "$EV/serial.log" ]
    [ -f "$EV/health.log" ]
    [ "$(head -n 1 "$EV/summary.md")" = "No clear regression: CI itself failed at health -- interrupted" ]
    # the step's own child (the stand-in, hanging) is stopped too
    run kill -0 "$(cat "$STUB_DIR/hang.pid")"
    [ "$status" -ne 0 ]
}

@test "Review Focus 5: Ctrl-C (INT) during the base build: infra 'interrupted', no QEMU of ours to kill" {
    start_gate STUB_BUILD_HANG=base
    stop_gate INT
    [ "$st" -eq 3 ]
    expect_rows build-base infra
    [ "$(note_of build-base)" = interrupted ]
    [ "$(grep -c '^vm ' "$STUB_LOG")" -eq 0 ]
    [ -f "$EV/summary.md" ]
    run kill -0 "$(cat "$STUB_DIR/hang.pid")"
    [ "$status" -ne 0 ]
}

# --- the accelerator and usage ---

@test "--accel hvf and nvmm warn that they are untested; the run still writes every row" {
    for a in hvf nvmm; do
        gate --accel "$a"
        [[ "$output" == *"warning: --accel $a is untested; only kvm is"* ]]
        [ "$(wc -l < "$EV/steps.tsv")" -eq 13 ]
    done
    gate --accel kvm
    [[ "$output" != *"untested"* ]]
}

@test "usage errors exit 2 and write nothing" {
    for args in "--frobnicate" "--accel tcg" "--restarts 0" "--restarts x" "--ssh-port 70000" \
                "--ssh-port x" "--accel" "extra"; do
        # shellcheck disable=SC2086
        run "$SCRIPT" --release 10.9 --work-dir "$WORK" $args
        [ "$status" -eq 2 ] || { echo "$args: $status"; false; }
    done
    [ ! -e "$WORK/evidence/steps.tsv" ]
    [ ! -s "$STUB_LOG" ]
    run "$SCRIPT" --help
    [ "$status" -eq 0 ]
    [[ "$output" == *"usage: gate.sh"* ]]
}

# --- fix round 1 ---

@test "Review Focus 4: the guest's own disk filling (ENOSPC only in its output, the host roomy) is a fail" {
    STUB_BUILD_FAIL=base:guestfull gate
    [ "$status" -eq 1 ]
    expect_rows build-base fail
    [[ "$(note_of build-base)" == "the guest's disk filled: "*"No space left on device"* ]]
}

@test "Review Focus 4: the host below 2 GiB free at the failure is infra, whatever the log says" {
    GATE_BUILD_SPACE_AVAILABLE_KIB=$((1024 * 1024)) STUB_BUILD_FAIL=base:guestfull gate
    [ "$status" -eq 3 ]
    expect_rows build-base infra
    [[ "$(note_of build-base)" == *"no space left on device"*"1 GiB free"* ]]
    GATE_BUILD_SPACE_AVAILABLE_KIB=$((1024 * 1024)) STUB_BUILD_FAIL=layer:started gate
    [ "$status" -eq 3 ]
    expect_rows build-layer infra
    [[ "$(note_of build-layer)" == *"the host's disk"*"1 GiB free"* ]]
}

@test "builds: a build that outlives GATE_BUILD_TIMEOUT before QEMU started is infra, after it a fail" {
    GATE_BUILD_TIMEOUT=2 STUB_BUILD_HANG=base gate
    [ "$status" -eq 3 ]
    expect_rows build-base infra
    [[ "$(note_of build-base)" == *"did not finish within 2s"* ]]
    GATE_BUILD_TIMEOUT=2 STUB_BUILD_HANG=layer:started gate
    [ "$status" -eq 1 ]
    expect_rows build-layer fail
    [[ "$(note_of build-layer)" == *"did not finish within 2s"* ]]
}

@test "fetches: dev-install, or verify-changed-sources, outliving GATE_FETCH_TIMEOUT is infra" {
    GATE_FETCH_TIMEOUT=2 STUB_DEV_INSTALL_HANG=1 gate
    [ "$status" -eq 3 ]
    expect_rows plugin infra
    [[ "$(note_of plugin)" == *"did not finish within 2s"* ]]
    GATE_FETCH_TIMEOUT=2 STUB_VERIFY_HANG=1 gate
    [ "$status" -eq 3 ]
    expect_rows preflight infra
    [[ "$(note_of preflight)" == *"did not finish within 2s"* ]]
}

@test "the evidence dir may not be the work dir, above it, or inside what the gate deletes" {
    # the same new dir for both, and evidence above the work dir; the cache elsewhere, so only
    # the work/evidence check can catch them
    run "$SCRIPT" --release 10.9 --work-dir "$BATS_TEST_TMPDIR/same" --evidence-dir "$BATS_TEST_TMPDIR/same" \
        --packer-cache "$BATS_TEST_TMPDIR/cache" --restarts 2 --ssh-port "$PORT"
    [ "$status" -eq 2 ]
    [[ "$output" == *"holds the work dir"* ]]
    mkdir -p "$BATS_TEST_TMPDIR/above"
    run "$SCRIPT" --release 10.9 --work-dir "$BATS_TEST_TMPDIR/above/w" --evidence-dir "$BATS_TEST_TMPDIR/above" \
        --packer-cache "$BATS_TEST_TMPDIR/cache" --restarts 2 --ssh-port "$PORT"
    [ "$status" -eq 2 ]
    [[ "$output" == *"holds the work dir"* ]]
    for m in vm box-layer packer-cache plugins; do
        run "$SCRIPT" --release 10.9 --work-dir "$WORK" --evidence-dir "$WORK/$m/ev" \
            --packer-cache "$BATS_TEST_TMPDIR/cache" --restarts 2 --ssh-port "$PORT"
        [ "$status" -eq 2 ] || { echo "$m: $status"; echo "$output"; false; }
        [[ "$output" == *"is inside $WORK/$m"* ]]
        [ ! -e "$WORK/$m/ev/steps.tsv" ]
    done
    [ ! -s "$STUB_LOG" ]
}

@test "the packer cache may not be inside the evidence, nor the evidence inside it" {
    mkdir -p "$BATS_TEST_TMPDIR/ev"
    gate --evidence-dir "$BATS_TEST_TMPDIR/ev" --packer-cache "$BATS_TEST_TMPDIR/ev/cache"
    [ "$status" -eq 2 ]
    rm -rf "$BATS_TEST_TMPDIR/ev"; mkdir -p "$BATS_TEST_TMPDIR/ev"
    gate --evidence-dir "$BATS_TEST_TMPDIR/cache/ev" --packer-cache "$BATS_TEST_TMPDIR/cache"
    [ "$status" -eq 2 ]
    [ ! -s "$STUB_LOG" ]
}

@test "a symlinked evidence dir is pruned like any other" {
    mkdir -p "$BATS_TEST_TMPDIR/real-ev"
    ln -s "$BATS_TEST_TMPDIR/real-ev" "$BATS_TEST_TMPDIR/ev-link"
    STUB_VM_LEAK=1 STUB_VM_FAIL=reach:1 gate --evidence-dir "$BATS_TEST_TMPDIR/ev-link"
    [ "$status" -eq 1 ]
    [ -f "$BATS_TEST_TMPDIR/real-ev/steps.tsv" ]
    run find "$BATS_TEST_TMPDIR/real-ev" \( -name '*.qcow2' -o -name '*.iso' -o -name '*.dmg' -o -type l \)
    [ -z "$output" ]
}

@test "a work dir that is not empty and not the gate's own is refused, untouched; the gate marks its own" {
    mkdir -p "$BATS_TEST_TMPDIR/mine/vm"
    : > "$BATS_TEST_TMPDIR/mine/vm/precious"
    run "$SCRIPT" --release 10.9 --work-dir "$BATS_TEST_TMPDIR/mine" --restarts 2 --ssh-port "$PORT"
    [ "$status" -eq 2 ]
    [[ "$output" == *".guest-macosx-gate-work"* ]]
    [ -f "$BATS_TEST_TMPDIR/mine/vm/precious" ]
    [ ! -e "$BATS_TEST_TMPDIR/mine/evidence" ]
    mkdir -p "$BATS_TEST_TMPDIR/new"
    run "$SCRIPT" --release 10.9 --work-dir "$BATS_TEST_TMPDIR/new" --restarts 2 --ssh-port "$PORT"
    [ "$status" -eq 0 ]
    [ -f "$BATS_TEST_TMPDIR/new/.guest-macosx-gate-work" ]
}

@test "an unexpected stop of gate.sh itself still writes every row: infra, 'stopped unexpectedly'" {
    GUEST_MACOSX_GATE_TEST_ABORT_AFTER=build-base gate
    [ "$status" -eq 3 ]
    expect_rows build-layer infra
    [ "$(note_of build-layer)" = "gate.sh stopped unexpectedly (status 1)" ]
    [ -f "$EV/summary.md" ]
}

@test "restart: bin/gate-vm.sh restart that could not run (3) is infra" {
    STUB_VM_RESTART_RC=3 gate
    [ "$status" -eq 3 ]
    expect_rows restart-1 infra
    [ "$(note_of restart-1)" = "restart 1 of 2: bin/gate-vm.sh restart exited 3: restart: the stand-in could not run" ]
}

@test "Review Focus 5: TERM during boot kills the QEMU this run was booting" {
    start_gate STUB_VM_BOOT_HANG=1
    stop_gate TERM
    [ "$st" -eq 3 ]
    expect_rows boot infra
    [ "$(note_of boot)" = interrupted ]
    grep -qxF "vm kill $WORK/vm" "$STUB_LOG"
}

@test "on a signal every row is written before the gate waits for its child to stop" {
    start_gate STUB_VM_HANG=health:1 STUB_HANG_IGNORE_TERM=1 GATE_CHILD_STOP_WAIT=6
    kill -TERM "$BG"
    complete=
    for _ in $(seq 1 30); do
        if [ "$(wc -l < "$EV/steps.tsv")" -eq 13 ] && kill -0 "$BG" 2>/dev/null; then complete=1; break; fi
        sleep 0.1
    done
    st=0; wait "$BG" || st=$?; BG=
    [ -n "$complete" ]
    [ "$st" -eq 3 ]
    expect_rows health infra
}

@test "a build killed outright: its orphaned QEMU (only one naming this work dir, and only QEMU) is stopped" {
    mkdir -p "$BATS_TEST_TMPDIR/x$WORK"
    sleep 300 3>&- & q1=$!
    sleep 300 3>&- & q2=$!
    sleep 300 3>&- & n1=$!
    # a QEMU of another run whose path merely ends in this work dir's
    sleep 300 3>&- & q3=$!
    # QEMUs whose command line names no path: one working in this work dir, one in a lookalike
    (cd "$WORK" && exec sleep 300) 3>&- & q4=$!
    (cd "$BATS_TEST_TMPDIR/x$WORK" && exec sleep 300) 3>&- & q5=$!
    OWNED="$q1 $q2 $n1 $q3 $q4 $q5"
    # The process table this ps reports, beside it: its pids are known only
    # now, and the stub is a quoted heredoc, so the script that runs is the
    # text below (bin/heredoc-scripts.sh).
    {
        echo "$q1 qemu-system-x86_64 -drive file=$WORK/base/output-mavericks/packer-mavericks"
        echo "$q2 qemu-system-x86_64 -drive file=/elsewhere/disk.qcow2"
        echo "$n1 python3 $WORK/base/notqemu"
        echo "$q3 qemu-system-x86_64 -drive file=/x$WORK/vm/overlay.qcow2,if=none"
        echo "$q4 qemu-system-x86_64 -drive file=output-mavericks/packer-mavericks"
        echo "$q5 qemu-system-x86_64 -drive file=output-mavericks/packer-mavericks"
    } > "$STUBS/ps.table"
    cat > "$STUBS/ps" <<'STUB'
#!/bin/sh
table="$(dirname "$0")/ps.table"
if [ "$*" = "-eo pid=,args=" ]; then
    cat "$table"
    exit 0
fi
if [ "$1" = -p ]; then
    # A pid in the table: its command, the first word of its args.
    comm=$(awk -v p="$2" '$1 == p { print $2; exit }' "$table")
    if [ -n "$comm" ]; then
        echo "$comm"
        exit 0
    fi
fi
[ -x /usr/bin/ps ] && exec /usr/bin/ps "$@"
exec /bin/ps "$@"
STUB
    chmod +x "$STUBS/ps"
    start_gate STUB_BUILD_HANG=base:started STUB_HANG_IGNORE_TERM=1 GATE_CHILD_STOP_WAIT=1
    stop_gate TERM
    [ "$st" -eq 3 ]
    expect_rows build-base infra
    grep -qxF "kill -TERM $q1" "$STUB_LOG"
    grep -qxF "kill -TERM $q4" "$STUB_LOG"
    for p in $q2 $n1 $q3 $q5; do
        [ "$(grep -c "^kill .*\b$p\b" "$STUB_LOG")" -eq 0 ] || { echo "signalled $p"; false; }
    done
    grep -q "stopped an orphaned QEMU (pid $q1)" "$EV/evidence.log"
}

@test "a build that ignores TERM: timeout's whole process group is killed, so packer is never orphaned" {
    start_gate STUB_BUILD_HANG=base:started STUB_HANG_IGNORE_TERM=1 GATE_CHILD_STOP_WAIT=1
    hung=$(cat "$STUB_DIR/hang.pid")
    stop_gate TERM
    [ "$st" -eq 3 ]
    # the stand-in packer (a child of timeout, which ignores TERM) is gone too
    for _ in $(seq 1 20); do kill -0 "$hung" 2>/dev/null || break; sleep 0.1; done
    run kill -0 "$hung"
    [ "$status" -ne 0 ]
}

@test "a packer killed from outside (137, before its limit) is infra, 'was killed', never a timeout" {
    STUB_BUILD_FAIL=base:killed gate
    [ "$status" -eq 3 ]
    expect_rows build-base infra
    [[ "$(note_of build-base)" == *"was killed (status 137: out of memory, or another process)"* ]]
    [[ "$(note_of build-base)" != *"did not finish"* ]]
}

@test "a build that needed timeout's KILL (137 at its limit) is a timeout" {
    GATE_BUILD_TIMEOUT=2 GATE_KILL_AFTER=1 STUB_HANG_IGNORE_TERM=1 STUB_BUILD_HANG=base:started gate
    [ "$status" -eq 1 ]
    expect_rows build-base fail
    [[ "$(note_of build-base)" == *"did not finish within 2s"* ]]
}

# --- 10.6 (--release 10.6) ---

# sl_gate [args...]: the gate on 10.6, from a stand-in disc image (the stubs never read it)
sl_gate() {
    : > "$BATS_TEST_TMPDIR/disc.iso"
    gate --release 10.6 --installer "$BATS_TEST_TMPDIR/disc.iso" "$@"
}

@test "10.6: the base from the user's disc, no layer; build-layer and reach not applicable, said why; the rest pass" {
    sl_gate
    [ "$status" -eq 0 ]
    NA=$SL_NA expect_rows
    [ "$(note_of build-layer)" = "10.6 has no Aqua Transport layer yet (G5): the gate boots the base box" ]
    [ "$(note_of reach)" = "10.6 has no TLS layer yet (G5): its curl, on OpenSSL 0.9.8, fails all three hosts with a TLS protocol-version alert (MEASURED 2026-10-29)" ]
    [ "$(head -n 1 "$EV/summary.md")" = "Passed: every step that applies, 2 restarts; not applicable: build-layer, reach" ]
    grep -qxF "packer build -color=false -var updates=security -var installer=$BATS_TEST_TMPDIR/disc.iso -var accelerator=kvm -var headless=true $REPO/templates/snowleopard (cwd $WORK/base) cache=$WORK/packer-cache plugins=$WORK/plugins" "$STUB_LOG"
    [ "$(grep -c '^packer build' "$STUB_LOG")" -eq 1 ]
    [ ! -e "$WORK/box-layer" ]
    grep -qxF "release: 10.6" "$EV/preflight.log"
}

@test "10.6: the base box's firmware and guest, the same checks but Aqua Transport's pin and reach, 10.6.8 (10K549)" {
    sl_gate
    [ "$status" -eq 0 ]
    grep -qxF "varstore $WORK/box-base $WORK/varstore" "$STUB_LOG"
    grep -qxF "vm boot $WORK/vm $WORK/box-base" "$STUB_LOG"
    pins=$("$REPO/bin/gate-expected.sh" | awk '$1 != "aquatransport" { printf "%s ", $2 }')
    [ "$(grep -cxF "vm ssh $WORK/vm sh /Users/vagrant/gate/guest-checks.sh versions ${pins% }" "$STUB_LOG")" -eq 3 ]
    [ "$(grep -cxF "vm ssh $WORK/vm sh /Users/vagrant/gate/guest-checks.sh health /Users/vagrant/gate/crash-allowlist.tsv 10.6.8 10K549 crash hang panic spin diag shutdownStall" "$STUB_LOG")" -eq 3 ]
    [ "$(grep -c "guest-checks.sh reach" "$STUB_LOG")" -eq 0 ]
    [ "$(grep -cxF "vm ssh $WORK/vm sh /Users/vagrant/gate/guest-checks.sh host-uuid" "$STUB_LOG")" -eq 1 ]
    [ "$(grep -cxF "vm restart $WORK/vm" "$STUB_LOG")" -eq 2 ]
    grep -qxF "vm shutdown $WORK/vm" "$STUB_LOG"
}

@test "10.9, the default: the layer over the base, every step applies, Aqua Transport's pin and reach, 10.9.5 (13F1911)" {
    gate --release 10.9
    [ "$status" -eq 0 ]
    expect_rows
    [ "$(grep -c $'\tn/a\t' "$EV/steps.tsv")" -eq 0 ]
    grep -qxF "release: 10.9" "$EV/preflight.log"
    [ "$(grep -c '^packer build' "$STUB_LOG")" -eq 2 ]
    [ "$(grep -c -- '-var installer=' "$STUB_LOG")" -eq 0 ]
}

@test "10.6: a failure after a step not applicable is the verdict, and the steps after it skip" {
    STUB_VM_FAIL=health:2 sl_gate
    [ "$status" -eq 1 ]
    NA=$SL_NA expect_rows restart-1 fail
    [[ "$(note_of restart-1)" == "restart 1 of 2: health failed -- "* ]]
    STUB_BUILD_FAIL=base:started sl_gate
    [ "$status" -eq 1 ]
    NA=$SL_NA expect_rows build-base fail
    [ "$(note_of build-layer)" = "" ]
}

@test "10.6: a gate stopped unexpectedly still writes every row, the step not applicable as such" {
    GUEST_MACOSX_GATE_TEST_ABORT_AFTER=build-base sl_gate
    [ "$status" -eq 3 ]
    got=$(awk -F'\t' 'NR <= 5 { print $1 " " $2 }' "$EV/steps.tsv" | tr '\n' ' ')
    [ "$got" = "preflight pass plugin pass build-base pass build-layer n/a varstore infra " ]
}

@test "boot: a box whose release is not the one the gate built is a fail, naming both" {
    STUB_BOX_RELEASE=10.6 gate
    [ "$status" -eq 1 ]
    expect_rows boot fail
    [ "$(note_of boot)" = "the box $WORK/box-layer is Mac OS X 10.6 by its Vagrantfile; the gate built 10.9" ]
    STUB_BOX_RELEASE=10.9 sl_gate
    [ "$status" -eq 1 ]
    NA=$SL_NA expect_rows boot fail
    [ "$(note_of boot)" = "the box $WORK/box-base is Mac OS X 10.9 by its Vagrantfile; the gate built 10.6" ]
}

@test "--release and --installer: an unknown release, 10.6 without a disc, a disc on 10.9, or no such disc is a usage error" {
    for args in "--release 10.4" "--release 10.6" "--installer $BATS_TEST_TMPDIR/disc.iso" \
        "--release 10.6 --installer $BATS_TEST_TMPDIR/no-such-disc.iso" "--release 10.6 --installer $BATS_TEST_TMPDIR"; do
        : > "$BATS_TEST_TMPDIR/disc.iso"
        # shellcheck disable=SC2086  # words
        gate $args
        [ "$status" -eq 2 ] || { echo "$args: $status"; false; }
    done
    run "$SCRIPT" --release 10.4
    [[ "$output" == *"--release wants both, 10.9 or 10.6 (got 10.4)"* ]]
    run "$SCRIPT" --release 10.6
    [[ "$output" == *"--release 10.6 needs --installer, your own retail 10.6 install disc image"* ]]
    run "$SCRIPT" --release 10.9 --installer "$BATS_TEST_TMPDIR/disc.iso"
    [[ "$output" == *"--installer is for 10.6: not for --release 10.9"* ]]
    run "$SCRIPT" --release 10.6 --installer "$BATS_TEST_TMPDIR/no-such-disc.iso"
    [[ "$output" == *"--installer $BATS_TEST_TMPDIR/no-such-disc.iso is not a readable file"* ]]
    [ ! -e "$EV/steps.tsv" ]
    [ "$(grep -c '^packer\|^vm ' "$STUB_LOG")" -eq 0 ]
}

@test "a failure keeps launchd's own crash sample, /var/log/launchd-pid1.crash, when the guest has one" {
    STUB_VM_PID1CRASH=1 STUB_VM_FAIL=health:1 gate
    [ "$status" -eq 1 ]
    [ "$(cat "$EV/guest/system-launchd-pid1.crash")" = "Sampling process 1 for 1 second" ]
    rm -f "$STUB_DIR"/count.*
    STUB_VM_FAIL=health:1 gate
    [ "$status" -eq 1 ]
    [ ! -e "$EV/guest/system-launchd-pid1.crash" ]
}

# --- both releases: a plain bin/gate.sh (R52) ---

# both [args...]: bin/gate.sh with no --release, the stand-in disc unless args say otherwise
both() {
    : > "$BATS_TEST_TMPDIR/disc.iso"
    run "$SCRIPT" --work-dir "$WORK" --restarts 2 --ssh-port "$PORT" "$@"
}

@test "both: a plain bin/gate.sh with no disc refuses, naming both ways on, and nothing runs" {
    both
    [ "$status" -eq 2 ]
    [[ "$output" == *"a plain gate.sh gates both releases, 10.9 then 10.6, and 10.6 needs your own retail 10.6 install disc: pass --installer <disc image>, or gate one release on purpose with --release 10.9 or --release 10.6 --installer <disc image>"* ]]
    [ ! -e "$EV" ]
    [ ! -s "$STUB_LOG" ]
}

@test "both: 10.9 then 10.6, each with its own evidence, steps and verdict; the combined verdict passes when both do" {
    both --installer "$BATS_TEST_TMPDIR/disc.iso"
    [ "$status" -eq 0 ]
    EV=$WORK/evidence/10.9 expect_rows
    EV=$WORK/evidence/10.6 NA=$SL_NA expect_rows
    [ "$(cat "$WORK/evidence/10.9/release.txt")" = 10.9 ]
    [ "$(cat "$WORK/evidence/10.6/release.txt")" = 10.6 ]
    [ "$(head -n 1 "$WORK/evidence/10.9/summary.md")" = "Passed: every step, 2 restarts" ]
    [ "$(head -n 1 "$WORK/evidence/10.6/summary.md")" = "Passed: every step that applies, 2 restarts; not applicable: build-layer, reach" ]
    [ "$(head -n 1 "$WORK/evidence/summary.md")" = "Passed: both releases -- 10.9: Passed: every step, 2 restarts; 10.6: Passed: every step that applies, 2 restarts; not applicable: build-layer, reach" ]
    grep -qxF "## 10.9 (exit 0)" "$WORK/evidence/summary.md"
    grep -qxF "## 10.6 (exit 0)" "$WORK/evidence/summary.md"
    [ ! -e "$WORK/evidence/steps.tsv" ]
    # in that order: 10.9's base and layer, then 10.6's base
    [ "$(grep '^packer build' "$STUB_LOG" | sed 's/.*templates\/\([a-z-]*\) .*/\1/' | tr '\n' ' ')" = "mavericks mavericks-aquatransport snowleopard " ]
    [[ "$output" == *"Passed: both releases"* ]]
    # --release both says the same thing
    rm -f "$STUB_DIR"/count.*
    both --release both --installer "$BATS_TEST_TMPDIR/disc.iso"
    [ "$status" -eq 0 ]
}

@test "both: one release failing fails the combined verdict, and the other release still runs" {
    # the stand-in counts checks across both runs: versions' first call is 10.9's
    STUB_VM_FAIL=versions:1 both --installer "$BATS_TEST_TMPDIR/disc.iso"
    [ "$status" -eq 1 ]
    EV=$WORK/evidence/10.9 expect_rows versions fail
    EV=$WORK/evidence/10.6 NA=$SL_NA expect_rows
    [[ "$(head -n 1 "$WORK/evidence/summary.md")" == "Not passed: 10.9: Failed: versions -- "*"; 10.6: Passed: every step that applies, 2 restarts; not applicable: build-layer, reach" ]]
    rm -f "$STUB_DIR"/count.*
    # host-uuid runs once a release: its second call is 10.6's
    STUB_VM_FAIL=host-uuid:2 both --installer "$BATS_TEST_TMPDIR/disc.iso"
    [ "$status" -eq 1 ]
    EV=$WORK/evidence/10.9 expect_rows
    EV=$WORK/evidence/10.6 NA=$SL_NA expect_rows host-uuid fail
    [[ "$(head -n 1 "$WORK/evidence/summary.md")" == "Not passed: 10.9: Passed: every step, 2 restarts; 10.6: Failed: host-uuid -- "* ]]
    grep -qxF "## 10.6 (exit 1)" "$WORK/evidence/summary.md"
}

@test "both: a release that could not run is no pass either: status 3 when nothing failed" {
    STUB_VM_BOOT_RC=3 both --installer "$BATS_TEST_TMPDIR/disc.iso"
    [ "$status" -eq 3 ]
    [[ "$(head -n 1 "$WORK/evidence/summary.md")" == "Not passed: 10.9: No clear regression: CI itself failed at boot -- "*"; 10.6: No clear regression: CI itself failed at boot -- "* ]]
}

@test "both: --evidence-dir holds each release's evidence beside the combined summary" {
    both --installer "$BATS_TEST_TMPDIR/disc.iso" --evidence-dir "$BATS_TEST_TMPDIR/ev"
    [ "$status" -eq 0 ]
    [ -f "$BATS_TEST_TMPDIR/ev/10.9/steps.tsv" ]
    [ -f "$BATS_TEST_TMPDIR/ev/10.6/steps.tsv" ]
    [[ "$(head -n 1 "$BATS_TEST_TMPDIR/ev/summary.md")" == "Passed: both releases -- "* ]]
}

@test "both: interrupted during 10.9, its run stops as ever, 10.6 never starts, and the verdict says so" {
    [ -n "$REAL_PYTHON3" ] || skip "needs python3 to start the gate with SIGINT at its default"
    : > "$BATS_TEST_TMPDIR/disc.iso"
    env STUB_VM_HANG=health:1 "$REAL_PYTHON3" -c 'import os, signal, sys
signal.signal(signal.SIGINT, signal.SIG_DFL)
os.execv(sys.argv[1], sys.argv[1:])' "$SCRIPT" --installer "$BATS_TEST_TMPDIR/disc.iso" --work-dir "$WORK" \
        --restarts 2 --ssh-port "$PORT" > "$BATS_TEST_TMPDIR/gate.out" 2>&1 3>&- &
    BG=$!
    for _ in $(seq 1 100); do [ -e "$STUB_DIR/hanging" ] && break; sleep 0.1; done
    [ -e "$STUB_DIR/hanging" ] || { cat "$BATS_TEST_TMPDIR/gate.out"; false; }
    stop_gate INT
    [ "$st" -eq 3 ]
    EV=$WORK/evidence/10.9 expect_rows health infra
    [ "$(EV=$WORK/evidence/10.9 note_of health)" = interrupted ]
    [ ! -e "$WORK/evidence/10.6/steps.tsv" ]
    [ "$(grep -c 'templates/snowleopard' "$STUB_LOG")" -eq 0 ]
    [ "$(head -n 1 "$WORK/evidence/summary.md")" = "Not passed: 10.9: No clear regression: CI itself failed at health -- interrupted; 10.6: did not run (interrupted)" ]
    # the 10.9 run's own status, never the 143 of the wait the signal cut short
    grep -qxF "## 10.9 (exit 3)" "$WORK/evidence/summary.md"
    grep -qxF "## 10.6 (did not run)" "$WORK/evidence/summary.md"
    run kill -0 "$(cat "$STUB_DIR/hang.pid")"
    [ "$status" -ne 0 ]
}

@test "a single-release run records its release beside its steps" {
    gate
    [ "$(cat "$EV/release.txt")" = 10.9 ]
    sl_gate
    [ "$(cat "$EV/release.txt")" = 10.6 ]
}

@test "the disc is never where the gate removes things: inside the work dir, the default evidence dir included, it is refused and kept" {
    # checked before the gate removes anything: the default evidence dir, <work>/evidence, goes first
    for d in evidence packer-cache base; do
        mkdir -p "$WORK/$d"
        echo disc > "$WORK/$d/disc.iso"
        for r in 10.6 both; do
            run "$SCRIPT" --work-dir "$WORK" --release "$r" --installer "$WORK/$d/disc.iso" --ssh-port "$PORT"
            [ "$status" -eq 2 ] || { echo "$d $r: $status $output"; false; }
            [[ "$output" == *"--installer $WORK/$d/disc.iso is inside the work dir $WORK, which the gate removes things in; keep your disc elsewhere"* ]]
            [ "$(cat "$WORK/$d/disc.iso")" = disc ]
        done
    done
    [ ! -s "$STUB_LOG" ]
}

@test "the disc's symlinks are followed before that check: a link from outside to a disc inside the work dir is refused" {
    mkdir -p "$WORK/base" "$BATS_TEST_TMPDIR/elsewhere"
    echo disc > "$WORK/base/disc.iso"
    ln -s "$WORK/base/disc.iso" "$BATS_TEST_TMPDIR/elsewhere/disc.iso"
    ln -s ../elsewhere/disc.iso "$BATS_TEST_TMPDIR/elsewhere/rel.iso"
    for l in disc.iso rel.iso; do
        run "$SCRIPT" --work-dir "$WORK" --release 10.6 --installer "$BATS_TEST_TMPDIR/elsewhere/$l" --ssh-port "$PORT"
        [ "$status" -eq 2 ] || { echo "$l: $status $output"; false; }
        [[ "$output" == *"--installer $WORK/base/disc.iso is inside the work dir"* ]]
    done
    [ "$(cat "$WORK/base/disc.iso")" = disc ]
    # a link to a disc outside it is fine, and packer is given the disc itself
    echo disc > "$BATS_TEST_TMPDIR/elsewhere/real.iso"
    ln -s real.iso "$BATS_TEST_TMPDIR/elsewhere/link.iso"
    gate --release 10.6 --installer "$BATS_TEST_TMPDIR/elsewhere/link.iso"
    [ "$status" -eq 0 ]
    grep -q -- "-var installer=$(cd "$BATS_TEST_TMPDIR/elsewhere" && pwd -P)/real.iso " "$STUB_LOG"
}

@test "both: a signal as a release's run starts, before the gate knows its pid, still stops that run" {
    : > "$BATS_TEST_TMPDIR/disc.iso"
    STUB_VM_HANG=health:1 GUEST_MACOSX_GATE_TEST_SIGNAL_AT_START=10.9 both --installer "$BATS_TEST_TMPDIR/disc.iso"
    [ "$status" -eq 3 ]
    [ ! -e "$WORK/evidence/10.6/steps.tsv" ]
    # the 10.9 run was stopped, never left to hang to its end: no row of it is a pass after the signal
    [[ "$(head -n 1 "$WORK/evidence/summary.md")" == "Not passed: 10.9: "*"; 10.6: did not run (interrupted)" ]]
    [ ! -e "$STUB_DIR/hanging" ] || { run kill -0 "$(cat "$STUB_DIR/hang.pid")"; [ "$status" -ne 0 ]; }
    [ "$(grep -c '^vm restart' "$STUB_LOG")" -eq 0 ]
}

@test "a link whose .. leads, through a symlinked directory, to a disc inside the work dir is refused, and the disc kept" {
    mkdir -p "$WORK/base/sub" "$BATS_TEST_TMPDIR/out"
    echo disc > "$WORK/base/disc.iso"
    ln -s ../disc.iso "$WORK/base/sub/y"
    ln -s "$WORK/base/sub" "$BATS_TEST_TMPDIR/out/sym"
    echo decoy > "$BATS_TEST_TMPDIR/out/disc.iso"
    for r in 10.6 both; do
        run "$SCRIPT" --work-dir "$WORK" --release "$r" --installer "$BATS_TEST_TMPDIR/out/sym/y" --ssh-port "$PORT"
        [ "$status" -eq 2 ] || { echo "$r: $status $output"; false; }
        [[ "$output" == *"--installer $WORK/base/disc.iso is inside the work dir"* ]]
    done
    [ "$(cat "$WORK/base/disc.iso")" = disc ]
    [ ! -s "$STUB_LOG" ]
}
