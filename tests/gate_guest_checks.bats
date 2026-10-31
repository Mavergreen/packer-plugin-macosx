#!/usr/bin/env bats
# platform: host-agnostic -- tested on Linux with KVM only
#
# gate/guest-checks.sh runs inside the 10.9 or 10.6 guest. Here it runs on this host
# against stub commands driven by env. Nothing may reach the host: PATH is the
# stub directory, /usr/bin and /bin (no sbin), and the dangerous names are
# stubs that refuse.

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    SCRIPT="$REPO/gate/guest-checks.sh"
    STUBS="$BATS_TEST_TMPDIR/stubs"
    export GUEST_MACOSX_GATE_ROOT="$BATS_TEST_TMPDIR/root"
    export STUB_LOG="$BATS_TEST_TMPDIR/stub.log"
    mkdir -p "$STUBS" "$GUEST_MACOSX_GATE_ROOT/Library/Logs/DiagnosticReports"
    : > "$STUB_LOG"
    for c in shutdown reboot halt poweroff systemctl sudo; do
        cat > "$STUBS/$c" <<'STUB'
#!/bin/sh
echo "stub: ${0##*/} refused"
exit 1
STUB
        chmod +x "$STUBS/$c"
    done
    cat > "$STUBS/nvram" <<'STUB'
#!/bin/sh
[ -n "${STUB_NVRAM:-}" ] || { echo "nvram: Error getting variable" >&2; exit 1; }
printf '%s\t%s\n' "$1" "$STUB_NVRAM"
STUB
    cat > "$STUBS/kextstat" <<'STUB'
#!/bin/sh
# kextstat -l -b <id>
case "$3" in
    as.vit9696.Lilu) v=${STUB_LILU:-} ;;
    as.vit9696.VirtualSMC) v=${STUB_VSMC:-} ;;
    *) v= ;;
esac
[ -z "$v" ] || echo "   77    0 0xffffff7f80000000 0x1000 0x1000 $3 ($v) <5 4 3>"
exit 0
STUB
    cat > "$STUBS/pkgutil" <<'STUB'
#!/bin/sh
[ "$1" = --pkg-info ] && [ "$2" = Wowfunhappy.AquaTransport ] && [ -n "${STUB_AQUA:-}" ] || exit 1
printf 'package-id: %s\nversion: %s\nvolume: /\n' "$2" "$STUB_AQUA"
STUB
    cat > "$STUBS/sw_vers" <<'STUB'
#!/bin/sh
case "$1" in
    -productVersion) echo "${STUB_OS:-10.9.5}" ;;
    -buildVersion) echo "${STUB_BUILD:-13F1911}" ;;
esac
STUB
    cat > "$STUBS/sysctl" <<'STUB'
#!/bin/sh
echo "${STUB_BOOTTIME:-}"
STUB
    cat > "$STUBS/ioreg" <<'STUB'
#!/bin/sh
# ioreg -rd1 -c IOPlatformExpertDevice | ioreg -p IODeviceTree -n platform -rd1
case "$*" in
    *IOPlatformExpertDevice*) [ -z "${STUB_PLATFORM_UUID:-}" ] || echo "      \"IOPlatformUUID\" = \"$STUB_PLATFORM_UUID\"" ;;
    *"-n platform"*) [ -z "${STUB_SYSTEM_ID:-}" ] || echo '      "system-id" = <52d8b023c5af480d8fbf822b915f5331>' ;;
esac
exit 0
STUB
    cat > "$STUBS/curl" <<'STUB'
#!/bin/sh
echo "$*" >> "$STUB_LOG"
for a in "$@"; do url=$a; done
case "$url" in *"${STUB_CURL_FAIL:-@@none@@}"*) echo "curl: (35) ssl" >&2; exit 35 ;; esac
exit 0
STUB
    chmod +x "$STUBS"/*
    export PATH="$STUBS:/usr/bin:/bin"
    export GUEST_MACOSX_GATE_CURL="$STUBS/curl"
    export STUB_NVRAM=REL-107-2026-09-01 STUB_LILU=1.7.2 STUB_VSMC=1.3.7 STUB_AQUA=2026.10.09
    # This boot, and a system.log that holds it and nothing wrong (docs/decisions/0024).
    export STUB_BOOTTIME='{ sec = 1792253468, usec = 0 } Sat Oct 17 09:11:08 2026'
    export STUB_PLATFORM_UUID=F7B2200F-5289-5585-B893-5A6896A8FBA7 STUB_SYSTEM_ID=1 GUEST_MACOSX_GATE_SETTLE=0
    mkdir -p "$GUEST_MACOSX_GATE_ROOT/var/log"
    SYSLOG="$GUEST_MACOSX_GATE_ROOT/var/log/system.log"
    printf '%s\n' 'Oct 17 09:11:08 localhost bootlog[0]: BOOT_TIME 1792253468 0' \
        'Oct 17 09:11:11 localhost kernel[0]: DSMOS has arrived' > "$SYSLOG"
    ALLOW="$BATS_TEST_TMPDIR/allow.tsv"
    # the kinds of report bin/gate.sh judges and keeps (its REPORT_KINDS)
    KINDS="crash hang panic spin diag shutdownStall"
    # the release bin/gate.sh expects of a 10.9 run (its WANT_OS and WANT_BUILD)
    WANT="10.9.5 13F1911"
    printf '# header\n' > "$ALLOW"
}

versions() { run sh "$SCRIPT" versions 1.0.7 1.7.2 1.3.7 2026.10.09; }
report() { : > "$GUEST_MACOSX_GATE_ROOT/Library/Logs/DiagnosticReports/$1"; }


# bounded <secs> <command...>: the command, killed after secs and failing with
# 124, so a lost bound fails its test by name rather than hanging the run.
# /bin/sleep, not PATH's: a test may stub sleep.
bounded() {
    secs=$1
    shift
    "$@" &
    pid=$!
    n=0
    while kill -0 "$pid" 2>/dev/null; do
        if [ "$n" -ge $((secs * 10)) ]; then
            kill -9 "$pid" 2>/dev/null
            wait "$pid" 2>/dev/null
            echo "still running after ${secs}s: $*"
            return 124
        fi
        /bin/sleep 0.1
        n=$((n + 1))
    done
    wait "$pid"
}

@test "the stand-in guest cannot reach the host: dangerous names are stubs, no sbin" {
    for c in shutdown reboot halt poweroff systemctl sudo; do
        [ "$(command -v $c)" = "$STUBS/$c" ] || { echo "$c resolves to $(command -v $c)"; false; }
        run "$c" now
        [ "$status" -ne 0 ]
        [[ "$output" == "stub: $c refused" ]]
    done
    [[ "$PATH" != *sbin* ]]
}

@test "versions passes on matching versions" {
    versions
    [ "$status" -eq 0 ]
    [ "$output" = "ok opencore
ok lilu
ok virtualsmc
ok aquatransport" ]
}

@test "versions accepts a DBG build" {
    STUB_NVRAM=DBG-107-2026-09-01 versions
    [ "$status" -eq 0 ]
}

@test "versions names a mismatched Lilu" {
    STUB_LILU=1.7.1 versions
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL lilu: the guest runs 1.7.1, the checkout pins 1.7.2"* ]]
    [[ "$output" == *"ok virtualsmc"* ]]
}

@test "versions names a kext that is not loaded" {
    STUB_VSMC='' versions
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL virtualsmc: as.vit9696.VirtualSMC is not loaded"* ]]
}

@test "versions names a missing opencore-version" {
    STUB_NVRAM='' versions
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL opencore: no opencore-version in NVRAM"* ]]
}

@test "versions names an opencore mismatch" {
    STUB_NVRAM=REL-106-2026-09-01 versions
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL opencore: "*"REL-106"* ]]
}

@test "versions names an aquatransport mismatch and a missing package" {
    STUB_AQUA=2026.10.01 versions
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL aquatransport: the guest runs 2026.10.01, the checkout pins 2026.10.09"* ]]
    STUB_AQUA='' versions
    [[ "$output" == *"FAIL aquatransport: Wowfunhappy.AquaTransport is not installed"* ]]
}

@test "versions with the wrong argument count is a usage error" {
    run sh "$SCRIPT" versions 1 2
    [ "$status" -eq 2 ]
    run sh "$SCRIPT" versions 1 2 3 4 5
    [ "$status" -eq 2 ]
}

@test "versions without an Aqua Transport pin (10.6: no layer yet) checks the boot stack alone, and says so" {
    STUB_AQUA='' run sh "$SCRIPT" versions 1.0.7 1.7.2 1.3.7
    [ "$status" -eq 0 ]
    [ "$output" = "ok opencore
ok lilu
ok virtualsmc
n/a aquatransport: no pin given (10.6 has no Aqua Transport layer yet)" ]
    # the boot stack is held as tightly as on 10.9
    STUB_LILU=1.7.1 run sh "$SCRIPT" versions 1.0.7 1.7.2 1.3.7
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL lilu: the guest runs 1.7.1, the checkout pins 1.7.2"* ]]
}

@test "health passes on a clean guest" {
    mkdir -p "$GUEST_MACOSX_GATE_ROOT/Users/vagrant/Library/Logs/DiagnosticReports"
    run sh "$SCRIPT" health "$ALLOW" $WANT $KINDS
    [ "$status" -eq 0 ]
    [[ "$output" == *"ok os"* ]]
    [[ "$output" == *"ok reports"* ]]
}

GIVEUP='localhost cfprefsd[24]: _CFXPreferencesGetByHostIdentifierString: unable to determine UUID for host... Giving up and returning 00000000-0000-0000-0000-000000000000.'

@test "host-uuid passes a boot that had the hypervisor's UUID from the start, and says so" {
    run sh "$SCRIPT" host-uuid
    [ "$status" -eq 0 ]
    [[ "$output" == *"ok host-uuid: F7B2200F-5289-5585-B893-5A6896A8FBA7, from the firmware's system-id"* ]]
}

@test "host-uuid fails a boot whose firmware named no host UUID, though nothing gave up yet" {
    # The gate passes QEMU a -uuid, so no system-id means the firmware dropped it: the guest
    # made its own from the MAC, late (docs/decisions/0024).
    STUB_SYSTEM_ID= STUB_PLATFORM_UUID=00000000-0000-1000-8000-525400123456 run sh "$SCRIPT" host-uuid
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL host-uuid: no system-id: the firmware named no host UUID (did QEMU get a -uuid, and is OpenCore's SystemUUID OEM?); the guest made 00000000-0000-1000-8000-525400123456 itself"* ]]
}

@test "host-uuid fails a boot whose daemons found no host UUID, naming the line" {
    echo "Oct 17 09:11:42 $GIVEUP" >> "$SYSLOG"
    run sh "$SCRIPT" host-uuid
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL host-uuid: the kernel had no host UUID when this boot's daemons asked: Oct 17 09:11:42 $GIVEUP"* ]]
}

@test "a line from an earlier boot does not fail this one" {
    { echo 'Oct 17 08:00:00 localhost bootlog[0]: BOOT_TIME 1792249200 0'
      echo "Oct 17 08:00:30 $GIVEUP"
      cat "$SYSLOG"; } > "$SYSLOG.new"
    mv "$SYSLOG.new" "$SYSLOG"
    run sh "$SCRIPT" host-uuid
    [ "$status" -eq 0 ]
    [[ "$output" == *"ok host-uuid: "* ]]
}

@test "this boot's lines in yesterday's rotated log are read too" {
    { cat "$SYSLOG"; echo "Oct 17 09:11:42 $GIVEUP"; } | bzip2 > "$SYSLOG.0.bz2"
    echo 'Oct 18 00:00:00 localhost newsyslog[400]: logfile turned over' > "$SYSLOG"
    run sh "$SCRIPT" host-uuid
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL host-uuid: the kernel had no host UUID when this boot's daemons asked: "* ]]
}

@test "a clock step since boot moves kern.boottime; within 10 s it is still this boot (as gate-vm.sh)" {
    # xnu adds every calendar step to boottime, so an NTP step leaves kern.boottime a second or
    # two off the BOOT_TIME bootlog wrote.
    for d in 1 -1 10 -10; do
        STUB_BOOTTIME="{ sec = $((1792253468 + d)), usec = 0 }" run sh "$SCRIPT" host-uuid
        [ "$status" -eq 0 ] || { echo "step $d: $output"; false; }
    done
    echo "Oct 17 09:11:42 $GIVEUP" >> "$SYSLOG"
    STUB_BOOTTIME="{ sec = 1792253469, usec = 0 }" run sh "$SCRIPT" host-uuid
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL host-uuid: the kernel had no host UUID when this boot's daemons asked: "* ]]
}

@test "of several boots within 10 s, the newest is this one" {
    { echo 'Oct 17 09:11:00 localhost bootlog[0]: BOOT_TIME 1792253460 0'
      echo "Oct 17 09:11:02 $GIVEUP"
      cat "$SYSLOG"; } > "$SYSLOG.new"
    mv "$SYSLOG.new" "$SYSLOG"
    run sh "$SCRIPT" host-uuid
    [ "$status" -eq 0 ]
}

@test "host-uuid fails when it cannot find this boot in system.log, or the kernel names no host UUID" {
    STUB_BOOTTIME='{ sec = 1792253479, usec = 0 }' run sh "$SCRIPT" host-uuid
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL host-uuid: no BOOT_TIME within 10 s of 1792253479 in /var/log/system.log or system.log.0.bz2"* ]]
    STUB_BOOTTIME='{ sec = 1792240000, usec = 0 } Sat Oct 17 05:26:40 2026' run sh "$SCRIPT" host-uuid
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL host-uuid: no BOOT_TIME within 10 s of 1792240000 in /var/log/system.log or system.log.0.bz2"* ]]
    STUB_BOOTTIME=garbage run sh "$SCRIPT" host-uuid
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL host-uuid: cannot parse kern.boottime"* ]]
    STUB_PLATFORM_UUID= run sh "$SCRIPT" host-uuid
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL host-uuid: IOPlatformExpertDevice has no IOPlatformUUID"* ]]
}

@test "host-uuid waits until the boot is 45 s old, so a boot still waiting cannot pass" {
    now=$(date +%s)
    STUB_BOOTTIME="{ sec = $((now - 3)), usec = 0 }"
    printf 'Oct 17 09:11:08 localhost bootlog[0]: BOOT_TIME %s 0\n' "$((now - 3))" > "$SYSLOG"
    STUB_BOOTTIME=$STUB_BOOTTIME GUEST_MACOSX_GATE_SETTLE=5 run sh "$SCRIPT" host-uuid
    [ "$status" -eq 0 ]
    [ $(($(date +%s) - now)) -ge 2 ]
}

@test "host-uuid's wait is bounded by its 45 s, whatever the clock says" {
    # kern.boottime in the future, after a backward step: the age never reaches 45 s.
    now=$(date +%s)
    STUB_BOOTTIME="{ sec = $((now + 3600)), usec = 0 }" GUEST_MACOSX_GATE_SETTLE=2 run bounded 10 sh "$SCRIPT" host-uuid
    [ $(($(date +%s) - now)) -le 4 ]
    [[ "$output" == *"FAIL host-uuid: no BOOT_TIME within 10 s of $((now + 3600))"* ]]
}

@test "host-uuid takes no arguments" {
    run sh "$SCRIPT" host-uuid extra
    [ "$status" -eq 2 ]
}

@test "health fails on a report not allowlisted, and matches process and kind exactly" {
    mkdir -p "$GUEST_MACOSX_GATE_ROOT/Users/vagrant/Library/Logs/DiagnosticReports"
    : > "$GUEST_MACOSX_GATE_ROOT/Users/vagrant/Library/Logs/DiagnosticReports/configd_2026-10-12-101500_host.crash"
    run sh "$SCRIPT" health "$ALLOW" $WANT $KINDS
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL reports: configd_2026-10-12-101500_host.crash"* ]]

    printf 'configd\tcrash\treason\n' >> "$ALLOW"
    run sh "$SCRIPT" health "$ALLOW" $WANT $KINDS
    [ "$status" -eq 0 ]
    [[ "$output" == *"ok reports"* ]]

    report configd_2026-10-12-101500_host.hang
    run sh "$SCRIPT" health "$ALLOW" $WANT $KINDS
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL reports: configd_2026-10-12-101500_host.hang"* ]]
    rm "$GUEST_MACOSX_GATE_ROOT/Library/Logs/DiagnosticReports/configd_2026-10-12-101500_host.hang"

    report configdx_2026-10-12-101500_host.crash
    run sh "$SCRIPT" health "$ALLOW" $WANT $KINDS
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL reports: configdx_2026-10-12-101500_host.crash"* ]]
}

@test "a shutdown stall is a report like any other: it fails health, named" {
    report 'diskarbitrationd,ManagedClient_2026-10-13-101842_mavericks.shutdownStall'
    run sh "$SCRIPT" health "$ALLOW" $WANT $KINDS
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL reports: diskarbitrationd,ManagedClient_2026-10-13-101842_mavericks.shutdownStall"* ]]
}

@test "a file of a kind the gate does not know fails health, said so: it is not kept as evidence" {
    report 'something_2026-10-13-101842_mavericks.newkind'
    run sh "$SCRIPT" health "$ALLOW" $WANT $KINDS
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL reports: something_2026-10-13-101842_mavericks.newkind (not a kind of report the gate knows: crash hang panic spin diag shutdownStall)"* ]]
    # allowlisting it changes nothing: the gate cannot keep what it does not know
    printf 'something\tnewkind\treason\n' >> "$ALLOW"
    run sh "$SCRIPT" health "$ALLOW" $WANT $KINDS
    [ "$status" -eq 1 ]
}

@test "a kernel panic is never allowlisted" {
    report Kernel_2026-10-12-101500_host.panic
    printf 'Kernel\tpanic\tx\n' >> "$ALLOW"
    run sh "$SCRIPT" health "$ALLOW" $WANT $KINDS
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL allowlist: a kernel panic cannot be allowlisted"* ]]
    [[ "$output" == *"FAIL reports: Kernel_2026-10-12-101500_host.panic"* ]]
}

@test "health names a wrong OS" {
    STUB_BUILD=13F34 run sh "$SCRIPT" health "$ALLOW" $WANT $KINDS
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL os: the guest is 10.9.5 (13F34), want 10.9.5 (13F1911)"* ]]
}

@test "health holds a 10.6 guest to the release it is asked for, as a 10.9 one" {
    STUB_OS=10.6.8 STUB_BUILD=10K549 run sh "$SCRIPT" health "$ALLOW" 10.6.8 10K549 $KINDS
    [ "$status" -eq 0 ]
    [[ "$output" == *"ok os"* ]]
    STUB_OS=10.6.8 STUB_BUILD=10K549 run sh "$SCRIPT" health "$ALLOW" $WANT $KINDS
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL os: the guest is 10.6.8 (10K549), want 10.9.5 (13F1911)"* ]]
    run sh "$SCRIPT" health "$ALLOW" 10.6.8 10K549 $KINDS
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL os: the guest is 10.9.5 (13F1911), want 10.6.8 (10K549)"* ]]
}

@test "health needs an allowlist file, the release and build, and the kinds of report" {
    run sh "$SCRIPT" health
    [ "$status" -eq 2 ]
    run sh "$SCRIPT" health "$ALLOW"
    [ "$status" -eq 2 ]
    run sh "$SCRIPT" health "$ALLOW" $WANT
    [ "$status" -eq 2 ]
    run sh "$SCRIPT" health "$BATS_TEST_TMPDIR/nope.tsv" $WANT $KINDS
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL allowlist: "* ]]
}

@test "the shipped allowlist has a comment header and no rows" {
    run grep -vE '^#|^[[:space:]]*$' "$REPO/gate/crash-allowlist.tsv"
    [ "$status" -eq 1 ]
    run sh "$SCRIPT" health "$REPO/gate/crash-allowlist.tsv" $WANT $KINDS
    [ "$status" -eq 0 ]
}

@test "reach fetches the three hosts with the system curl" {
    run sh "$SCRIPT" reach
    [ "$status" -eq 0 ]
    [ "$(wc -l < "$STUB_LOG")" -eq 3 ]
    for u in https://github.com/ https://release-assets.githubusercontent.com/ https://downloads.claude.ai/claude-code-releases/latest; do
        grep -qxF -- "-sS --connect-timeout 20 --max-time 60 -o /dev/null $u" "$STUB_LOG"
    done
}

@test "reach names the host that failed" {
    STUB_CURL_FAIL=release-assets run sh "$SCRIPT" reach
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL reach: https://release-assets.githubusercontent.com/"* ]]
}

@test "boottime parses sysctl" {
    STUB_BOOTTIME='{ sec = 1760263200, usec = 0 } Sun Oct 12 10:00:00 2025' run sh "$SCRIPT" boottime
    [ "$status" -eq 0 ]
    [ "$output" = 1760263200 ]
}

@test "boottime fails when it cannot parse" {
    STUB_BOOTTIME=garbage run sh "$SCRIPT" boottime
    [ "$status" -eq 1 ]
}

@test "an unknown subcommand is a usage error" {
    run sh "$SCRIPT" frobnicate
    [ "$status" -eq 2 ]
}

@test "a bundle id and a pin match literally, dots included" {
    cat > "$STUBS/kextstat" <<'STUB'
#!/bin/sh
echo "   77    0 0xffffff7f80000000 0x1000 0x1000 as.vit9696XLilu (1.7.2) <5 4 3>"
STUB
    versions
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL lilu: as.vit9696.Lilu is not loaded"* ]]
    STUB_NVRAM=REL-1507-2026-09-01 run sh "$SCRIPT" versions 1.0.7 1.7.2 1.3.7 2026.10.09
    [[ "$output" == *"FAIL opencore: "* ]]
}

@test "a panic report fails on its own, with a clean allowlist" {
    report Kernel_2026-10-12-101500_host.panic
    printf 'Kernel\tcrash\treason\n' >> "$ALLOW"
    run sh "$SCRIPT" health "$ALLOW" $WANT $KINDS
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL reports: Kernel_2026-10-12-101500_host.panic"* ]]
    [[ "$output" != *"FAIL allowlist"* ]]
}

@test "a process name with a backslash is matched literally" {
    report 'a\tb_2026-10-12-101500_host.crash'
    printf '%s\t%s\t%s\n' 'a\tb' crash reason >> "$ALLOW"
    run sh "$SCRIPT" health "$ALLOW" $WANT $KINDS
    [ "$status" -eq 0 ]
}

@test "an allowlist row with no reason is refused" {
    report configd_2026-10-12-101500_host.crash
    printf 'configd\tcrash\n' >> "$ALLOW"
    run sh "$SCRIPT" health "$ALLOW" $WANT $KINDS
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL allowlist: configd crash has no reason"* ]]
    [[ "$output" == *"FAIL reports: configd_2026-10-12-101500_host.crash"* ]]
}

@test "guest-checks.sh is executable" {
    [ -x "$SCRIPT" ]
}

# 10.6's system.log, as a 10.6.8 box's verbose boots wrote it (F18's runs, 2026-10-19): launchd's
# start line opens the boot, the daemons' first lines follow, and bootlog's BOOT_TIME comes about
# 6 s in, after them: "bootlog[<pid>]: BOOT_TIME: <sec> <usec>".
sl_boot() { # <sec> <hh:mm:ss>
    printf '%s\n' "Oct 17 $2 localhost com.apple.launchd[1]: *** launchd[1] has started up. ***" \
        "Oct 17 $2 localhost com.apple.launchd[1]: *** Verbose boot, will log to /dev/console. ***" \
        "Oct 17 $2 localhost mDNSResponder[18]: mDNSResponder mDNSResponder-212.1 (Jul 24 2009 22:34:12) starting"
    [ -z "${SL_ERROR35:-}" ] || echo "Oct 17 $2 localhost $SL_ERROR35"
    echo "Oct 17 $2 snowleopard bootlog[41]: BOOT_TIME: $1 0"
    echo "Oct 17 $2 snowleopard loginwindow[31]: Login Window Application Started"
}
ERROR35='configd[13]: _CFGetHostUUIDString: unable to determine UUID for host. Error: 35'

@test "host-uuid on 10.6: a boot whose daemons found no host UUID fails, though they said so before BOOT_TIME" {
    # 10.6's CoreFoundation does not wait: configd and the rest log Error: 35 about 2 s in, before
    # bootlog's line (docs/decisions/0024), so this boot is read from launchd's start line.
    SL_ERROR35=$ERROR35 sl_boot 1792253468 09:11:08 > "$SYSLOG"
    run sh "$SCRIPT" host-uuid
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL host-uuid: the kernel had no host UUID when this boot's daemons asked: Oct 17 09:11:08 localhost $ERROR35"* ]]
}

@test "host-uuid on 10.6: a clean boot passes, and an earlier boot's Error: 35 does not fail it" {
    sl_boot 1792253468 09:11:08 > "$SYSLOG"
    run sh "$SCRIPT" host-uuid
    [ "$status" -eq 0 ]
    [[ "$output" == *"ok host-uuid: F7B2200F-5289-5585-B893-5A6896A8FBA7, from the firmware's system-id"* ]]
    { SL_ERROR35=$ERROR35 sl_boot 1792249200 08:00:00; sl_boot 1792253468 09:11:08; } > "$SYSLOG"
    run sh "$SCRIPT" host-uuid
    [ "$status" -eq 0 ]
    # within 10 s of kern.boottime, as on 10.9; and the rotated log is read too
    STUB_BOOTTIME='{ sec = 1792253478, usec = 0 }' run sh "$SCRIPT" host-uuid
    [ "$status" -eq 0 ]
    { cat "$SYSLOG"; echo "Oct 17 09:11:42 snowleopard $ERROR35"; } | bzip2 > "$SYSLOG.0.bz2"
    echo 'Oct 18 00:00:00 snowleopard newsyslog[400]: logfile turned over' > "$SYSLOG"
    run sh "$SCRIPT" host-uuid
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL host-uuid: the kernel had no host UUID when this boot's daemons asked: "* ]]
}

@test "host-uuid on 10.6: a BOOT_TIME with no launchd start line before it cannot say where the boot began" {
    sl_boot 1792253468 09:11:08 | grep -v 'has started up' > "$SYSLOG"
    run sh "$SCRIPT" host-uuid
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL host-uuid: 10.6's BOOT_TIME for 1792253468 has no launchd start line before it in /var/log/system.log or system.log.0.bz2: cannot tell where the boot began"* ]]
}

@test "health fails on launchd's own crash sample, /var/log/launchd-pid1.crash, on both releases, never allowlisted" {
    # 10.6's launchd samples itself there when it crashes, then reboots (F16, docs/decisions/0023:
    # MEASURED). 10.9's launchd (launchd-842.92.1) names the same file, but its code that writes it
    # is compiled out, so on 10.9 it is REASONED, from that source alone, never to appear.
    : > "$GUEST_MACOSX_GATE_ROOT/var/log/launchd-pid1.crash"
    printf 'launchd-pid1\tcrash\tno\n' >> "$ALLOW"
    run sh "$SCRIPT" health "$ALLOW" $WANT $KINDS
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL reports: /var/log/launchd-pid1.crash (launchd itself crashed, and rebooted the guest: never allowlisted)"* ]]
    STUB_OS=10.6.8 STUB_BUILD=10K549 run sh "$SCRIPT" health "$ALLOW" 10.6.8 10K549 $KINDS
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL reports: /var/log/launchd-pid1.crash (launchd itself crashed"* ]]
    rm "$GUEST_MACOSX_GATE_ROOT/var/log/launchd-pid1.crash"
    run sh "$SCRIPT" health "$ALLOW" $WANT $KINDS
    [ "$status" -eq 0 ]
}

@test "host-uuid on 10.6: a boot with no launchd start line of its own never borrows an earlier boot's" {
    # the window would reach back into the earlier boot, Error: 35 and all: say so instead
    { SL_ERROR35=$ERROR35 sl_boot 1792249200 08:00:00; sl_boot 1792253468 09:11:08 | grep -v 'has started up'; } > "$SYSLOG"
    run sh "$SCRIPT" host-uuid
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAIL host-uuid: 10.6's BOOT_TIME for 1792253468 has no launchd start line before it"* ]]
    # 10.9's form starts at its own line, whatever came before
    printf '%s\n' "Oct 17 08:00:00 localhost com.apple.launchd[1]: *** launchd[1] has started up. ***" \
        "Oct 17 08:00:01 localhost $ERROR35" 'Oct 17 09:11:08 localhost bootlog[0]: BOOT_TIME 1792253468 0' > "$SYSLOG"
    run sh "$SCRIPT" host-uuid
    [ "$status" -eq 0 ]
}

@test "host-uuid on 10.6: a boot that restarted before its BOOT_TIME line is read from the last launchd start line" {
    # F16's launchd crash rebooted a boot before bootlog ran: two start lines, one BOOT_TIME. The
    # aborted boot's lines (here an Error: 35) are not this boot's.
    { printf '%s\n' "Oct 17 09:10:00 localhost com.apple.launchd[1]: *** launchd[1] has started up. ***" \
          "Oct 17 09:10:02 localhost $ERROR35"
      sl_boot 1792253468 09:11:08; } > "$SYSLOG"
    run sh "$SCRIPT" host-uuid
    [ "$status" -eq 0 ] || { echo "$output"; false; }
}
