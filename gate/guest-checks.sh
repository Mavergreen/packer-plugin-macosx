#!/bin/sh
# platform: macOS-only -- runs in the 10.9 or 10.6 guest over SSH, never on the build host
# Guest-side checks of the boot gate, run as `sh guest-checks.sh <subcommand>`
# from /Users/vagrant/gate/ (10.9 and 10.6 empty /private/tmp at boot). Both releases' /bin/sh
# is bash 3.2 in POSIX mode, so this is POSIX sh with sed, grep -E and awk only.
#
#   versions <opencore> <lilu> <virtualsmc> [<aquatransport>]
#                                       (no Aqua Transport pin: 10.6, which has no layer yet)
#   health <allowlist-file> <os> <build> <kind>...
#                                       (the release and build bin/gate.sh expects, and the kinds
#                                       of report it judges and keeps)
#   host-uuid
#   reach
#   boottime
#
# Each check prints `ok <name>` or `FAIL <name>: <why>`; exit 0 if none failed,
# 1 otherwise, 2 on a usage error. boottime prints kern.boottime's seconds.
#
# GUEST_MACOSX_GATE_ROOT (prefix for the report directories and /var/log), GUEST_MACOSX_GATE_CURL
# and GUEST_MACOSX_GATE_SETTLE (host-uuid's 45 s) are test seams, never set in the guest; commands
# are found on PATH.

ROOT=${GUEST_MACOSX_GATE_ROOT:-}
CURL=${GUEST_MACOSX_GATE_CURL:-/usr/bin/curl}
OC_VAR=4D1FDA02-38C7-4A6A-9CC6-4BCCA8B30102:opencore-version
SETTLE=${GUEST_MACOSX_GATE_SETTLE:-45}
URLS="https://github.com/ https://release-assets.githubusercontent.com/ https://downloads.claude.ai/claude-code-releases/latest"
TAB=$(printf '\t')

failed=0
ok() { echo "ok $1"; }
fail() { echo "FAIL $1: $2"; failed=1; }
usage() { echo "usage: guest-checks.sh versions <opencore> <lilu> <virtualsmc> [<aquatransport>] | health <allowlist> <os> <build> <kind>... | reach | host-uuid | boottime" >&2; exit 2; }

# kext_version $1: the version kextstat lists for bundle id $1, or nothing.
kext_version() {
    lit=$(printf "%s" "$1" | sed -e "s/[.]/\\\\./g")
    kextstat -l -b "$1" 2>/dev/null | sed -n -e "s/.* $lit (\\([^)]*\\)).*/\\1/p" | sed -n 1p
}

check_kext() { # name bundle-id pin
    have=$(kext_version "$2")
    if [ -z "$have" ]; then
        fail "$1" "$2 is not loaded"
    elif [ "$have" = "$3" ]; then
        ok "$1"
    else
        fail "$1" "the guest runs $have, the checkout pins $3"
    fi
}

cmd_versions() {
    [ $# -eq 3 ] || [ $# -eq 4 ] || usage
    oc=$(nvram "$OC_VAR" 2>/dev/null | awk -F"$TAB" 'NR==1{print $2}')
    digits=$(printf "%s" "$1" | tr -d . | sed -e "s/[^0-9A-Za-z]/\\\\&/g")
    if [ -z "$oc" ]; then
        fail opencore "no opencore-version in NVRAM"
    elif printf '%s\n' "$oc" | grep -Eq "^(REL|DBG)-$digits-"; then
        ok opencore
    else
        fail opencore "the guest reports $oc, want (REL|DBG)-$digits-... for the pinned $1"
    fi
    check_kext lilu as.vit9696.Lilu "$2"
    check_kext virtualsmc as.vit9696.VirtualSMC "$3"
    # 10.6 has no Aqua Transport layer yet (the parity design's G5), so the gate gives no pin, and
    # boots the base box: the boot stack above is the whole of what it pins there.
    if [ $# -eq 3 ]; then
        echo "n/a aquatransport: no pin given (10.6 has no Aqua Transport layer yet)"
        return
    fi
    aq=$(pkgutil --pkg-info Wowfunhappy.AquaTransport 2>/dev/null | sed -n -e 's/^version: //p')
    if [ -z "$aq" ]; then
        fail aquatransport "Wowfunhappy.AquaTransport is not installed"
    elif [ "$aq" = "$4" ]; then
        ok aquatransport
    else
        fail aquatransport "the guest runs $aq, the checkout pins $4"
    fi
}

# allowed $1 process $2 kind: is there a row in the allowlist for both?
allowed() {
    P=$1 awk -F"$TAB" -v k="$2" \
        '/^#/ || NF < 2 { next } $1 == ENVIRON["P"] && $2 == k { found = 1 } END { exit !found }' "$ALLOWLIST"
}

# this_boot_log <sec>: the lines of system.log from this boot's start on. The boot is the newest
# BOOT_TIME line whose <n> is within 10 s of <sec>, the boot's kern.boottime. Not equal to it: xnu
# adds every calendar step to boottime, so an NTP step since the boot moves it, and
# bin/gate-vm.sh allows the same 10 s. The rotated system.log.0.bz2 first, for a boot that
# newsyslog's midnight turnover split. Each release's form is its own, so the line found says
# which it is:
#   10.9  "bootlog[0]: BOOT_TIME <n> ", the boot's first line: the boot starts there.
#   10.6  "bootlog[<pid>]: BOOT_TIME: <n> ", about 6 s in, after its first daemons' lines, which
#         are the ones that say "Error: 35" (docs/decisions/0024): the boot starts at the last
#         "launchd[1]: *** launchd[1] has started up. ***" before it (MEASURED in a 10.6.8 box's
#         system.log, F18, 2026-10-19).
# Exit 1 when neither file holds such a BOOT_TIME; 3 when a 10.6 one has no start line of its own
# before it (after the boot before's BOOT_TIME), rather than read from an earlier boot's.
this_boot_log() {
    { bzcat "$ROOT/var/log/system.log.0.bz2" 2>/dev/null; cat "$ROOT/var/log/system.log" 2>/dev/null; } |
        awk -v want="$1" '
            { lines[NR] = $0 }
            index($0, "com.apple.launchd[1]: *** launchd[1] has started up. ***") { start = NR }
            index($0, "bootlog[0]: BOOT_TIME ") {
                rest = substr($0, index($0, "bootlog[0]: BOOT_TIME ") + 22)
                b = rest + 0
                if (rest ~ /^[0-9]+ / && b - want <= 10 && want - b <= 10) { from = NR }
            }
            /bootlog\[[0-9]+\]: BOOT_TIME: / {
                rest = $0
                sub(/.*bootlog\[[0-9]+\]: BOOT_TIME: /, "", rest)
                b = rest + 0
                if (rest ~ /^[0-9]+ / && b - want <= 10 && want - b <= 10) { from = start ? start : -1 }
                # a start line belongs to the boot whose BOOT_TIME follows it, never a later one
                start = 0
            }
            END {
                if (!from) exit 1
                if (from < 0) exit 3
                for (i = from; i <= NR; i++) print lines[i]
            }'
}

# host-uuid: run once, on a cold boot (a restart keeps the NVRAM its boot wrote, so it cannot
# tell): the kernel had a host UUID (IOPlatformUUID) before the boot's first daemons asked for
# one. Without it, CoreFoundation waits for it (30 s on 10.9, where cfprefsd's wait holds up
# securityd, configd and the kexts after them), then gives up and keys ByHost preferences by zeros
# (docs/decisions/0024). The gate passes QEMU its own -uuid, which OpenCore makes system-id, so a
# boot without system-id fails too: the firmware dropped it, and the guest made one from its MAC.
cmd_host_uuid() {
    [ $# -eq 0 ] || usage
    # CoreFoundation gives up 30 s after a daemon first asks, and cfprefsd asks about 3 s into the
    # boot: judge no earlier than 45 s in, or a boot still waiting would pass.
    # At most SETTLE seconds of waiting, whatever the clock says.
    waited=0
    while [ "$waited" -lt "$SETTLE" ]; do
        sec=$(sysctl -n kern.boottime 2>/dev/null | sed -n -e 's/.*[{ ]sec = \([0-9][0-9]*\).*/\1/p' | sed -n 1p)
        [ -n "$sec" ] || break
        [ $(($(date +%s) - sec)) -lt "$SETTLE" ] || break
        sleep 1
        waited=$((waited + 1))
    done
    uuid=$(ioreg -rd1 -c IOPlatformExpertDevice 2>/dev/null | sed -n -e 's/.*"IOPlatformUUID" = "\([^"]*\)".*/\1/p' | sed -n 1p)
    if [ -z "$uuid" ]; then
        fail host-uuid "IOPlatformExpertDevice has no IOPlatformUUID"
        return
    fi
    sec=$(sysctl -n kern.boottime 2>/dev/null | sed -n -e 's/.*[{ ]sec = \([0-9][0-9]*\).*/\1/p' | sed -n 1p)
    if [ -z "$sec" ]; then
        fail host-uuid "cannot parse kern.boottime"
        return
    fi
    rc=0
    log=$(this_boot_log "$sec") || rc=$?
    if [ "$rc" -eq 3 ]; then
        fail host-uuid "10.6's BOOT_TIME for $sec has no launchd start line before it in /var/log/system.log or system.log.0.bz2: cannot tell where the boot began"
        return
    elif [ "$rc" -ne 0 ]; then
        fail host-uuid "no BOOT_TIME within 10 s of $sec in /var/log/system.log or system.log.0.bz2"
        return
    fi
    line=$(printf '%s\n' "$log" | grep -F 'unable to determine UUID for host' | sed -n 1p)
    if [ -n "$line" ]; then
        fail host-uuid "the kernel had no host UUID when this boot's daemons asked: $line"
        return
    fi
    if ! ioreg -p IODeviceTree -n platform -rd1 2>/dev/null | grep -qF '"system-id"'; then
        fail host-uuid "no system-id: the firmware named no host UUID (did QEMU get a -uuid, and is OpenCore's SystemUUID OEM?); the guest made $uuid itself"
        return
    fi
    echo "ok host-uuid: $uuid, from the firmware's system-id"
}

cmd_health() {
    [ $# -ge 4 ] || usage
    ALLOWLIST=$1
    WANT_OS=$2
    WANT_BUILD=$3
    shift 3
    # The one list of report kinds, bin/gate.sh's: what it judges is what it keeps as evidence.
    KINDS="$*"
    os=$(sw_vers -productVersion 2>/dev/null)
    build=$(sw_vers -buildVersion 2>/dev/null)
    if [ "$os" = "$WANT_OS" ] && [ "$build" = "$WANT_BUILD" ]; then
        ok os
    else
        fail os "the guest is $os ($build), want $WANT_OS ($WANT_BUILD)"
    fi
    if [ ! -r "$ALLOWLIST" ]; then
        fail allowlist "cannot read $ALLOWLIST"
        return
    fi
    # Rows without a reason are refused like panic rows: the header promises one.
    noreason=$(awk -F"$TAB" '/^#/ || NF < 1 || $0 == "" { next } $3 == "" { print $1 " " $2; exit }' "$ALLOWLIST")
    refused=0
    if awk -F"$TAB" '/^#/ { next } $2 == "panic" { found = 1 } END { exit !found }' "$ALLOWLIST"; then
        fail allowlist "a kernel panic cannot be allowlisted"
        refused=1
    fi
    if [ -n "$noreason" ]; then
        fail allowlist "$noreason has no reason"
        refused=1
    fi
    [ "$refused" -eq 1 ] || ok allowlist
    # Scanned: /Library and /Users/*, the places 10.9 and 10.6 write reports for the system
    # and for logged-in users. Not /var/root (the gate never runs as root there).
    # The glob skips dotfiles, deliberately. Every file there is judged: one of a kind the gate
    # does not know fails too, said so, since the gate cannot keep it as evidence.
    bad=
    for f in "$ROOT"/Library/Logs/DiagnosticReports/* "$ROOT"/Users/*/Library/Logs/DiagnosticReports/*; do
        [ -f "$f" ] || continue
        name=${f##*/}
        kind=${name##*.}
        case " $KINDS " in
            *" $kind "*) ;;
            *) bad="${bad:+$bad, }$name (not a kind of report the gate knows: $KINDS)"; continue ;;
        esac
        proc=$(printf '%s\n' "${name%.*}" | sed -e 's/_[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]-[0-9]*_.*$//')
        if [ "$refused" -eq 0 ] && [ "$kind" != panic ] && allowed "$proc" "$kind"; then
            continue
        fi
        bad="${bad:+$bad, }$name"
    done
    # And launchd's own: when pid 1 crashes, 10.6's launchd samples itself into this file and
    # reboots (F16, docs/decisions/0023: MEASURED); nothing lands in DiagnosticReports. 10.9's
    # launchd (launchd-842.92.1's launchd.c) names the same file, but its code that writes it is
    # compiled out (#if 0), so on 10.9 it is REASONED never to appear: checked anyway, as
    # snowleopard's verify.sh checks it, and like a panic it is never allowlisted.
    if [ -e "$ROOT/var/log/launchd-pid1.crash" ]; then
        bad="${bad:+$bad, }/var/log/launchd-pid1.crash (launchd itself crashed, and rebooted the guest: never allowlisted)"
    fi
    if [ -n "$bad" ]; then
        fail reports "$bad"
    else
        ok reports
    fi
}

cmd_reach() {
    [ $# -eq 0 ] || usage
    bad=
    # No -f on purpose: this proves TLS and a connection with the system curl and
    # its certificates, not an HTTP status; a host's bare root URL may answer 4xx.
    # This is the layer's TLS check: its build's guest has no network, so the
    # layer's verify.sh cannot fetch (docs/decisions/0034).
    for u in $URLS; do
        "$CURL" -sS --connect-timeout 20 --max-time 60 -o /dev/null "$u" || bad="${bad:+$bad }$u"
    done
    if [ -n "$bad" ]; then
        fail reach "$bad"
    else
        ok reach
    fi
}

cmd_boottime() {
    [ $# -eq 0 ] || usage
    sec=$(sysctl -n kern.boottime 2>/dev/null | sed -n -e 's/.*[{ ]sec = \([0-9][0-9]*\).*/\1/p' | sed -n 1p)
    if [ -z "$sec" ]; then
        echo "boottime: cannot parse kern.boottime" >&2
        exit 1
    fi
    echo "$sec"
}

[ $# -ge 1 ] || usage
sub=$1
shift
case "$sub" in
    versions) cmd_versions "$@" ;;
    health) cmd_health "$@" ;;
    reach) cmd_reach "$@" ;;
    host-uuid) cmd_host_uuid "$@" ;;
    boottime) cmd_boottime "$@" ;;
    *) usage ;;
esac
exit "$failed"
