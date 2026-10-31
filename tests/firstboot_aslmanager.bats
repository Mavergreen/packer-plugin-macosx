#!/usr/bin/env bats
# platform: host-agnostic
#
# firstboot.sh's unthrottle_aslmanager (docs/decisions/0031).
#
# 10.9's syslogd asks aslmanager, synchronously, to check the logs as it
# starts: first on its main thread as it sets up its outputs, then on the
# one queue that writes every log file, as it first opens the ASL store.
# aslmanager is launched on demand, answers, runs its maintenance and
# exits, so an ask that comes after that exit, within 10 s of its start, is
# throttled by launchd for the rest of its default 10 s, and syslogd writes
# nothing of the boot until then; a shutdown in that window loses the
# boot's whole system.log and ASL store (MEASURED 2026-10-25, F9).
# ThrottleInterval 0 in aslmanager's own job lets launchd relaunch it at
# once. Only an on-demand com.apple.aslmanager job (no KeepAlive, no
# RunAtLoad) without a ThrottleInterval of its own is changed, and the log
# says which.
#
# SAFETY: these run firstboot's function on this machine with every path in
# the test's own directory, PATH=/usr/bin:/bin (no sbin); PlistBuddy is a
# stand-in that keeps one "key<TAB>type<TAB>value" line per key.

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    FB="$REPO/assets/guest/firstboot.sh"
    FNS="$BATS_TEST_TMPDIR/fns.sh"
    : > "$FNS"
    for f in say unthrottle_aslmanager; do
        fn=$(awk "/^$f\\(\\) \\{/,/^\\}/" "$FB")
        [ -n "$fn" ] || { echo "firstboot.sh has no function $f" >&2; return 1; }
        printf '%s\n' "$fn" >> "$FNS"
    done
    G="$BATS_TEST_TMPDIR/guest"
    mkdir -p "$G/LaunchDaemons" "$G/stub"
    PL="$G/LaunchDaemons/com.apple.aslmanager.plist"
    cat > "$G/stub/PlistBuddy" <<'EOF'
#!/bin/sh
# PlistBuddy -c "Print :key" file | PlistBuddy -c "Add :key type value" file
printf 'PlistBuddy %s\n' "$*" >> "$CALLS"
cmd=$2; file=$3
[ -z "${STUB_ADD_FAILS:-}" ] || case $cmd in "Add :"*) exit 1 ;; esac
case $cmd in
"Print :"*)
    k=${cmd#Print :}
    [ -f "$file" ] || exit 1
    line=$(grep "^$k	" "$file") || exit 1
    printf '%s\n' "$line" | cut -f3 ;;
"Add :"*) printf '%s\n' "${cmd#Add :}" | { read -r k ty v; printf '%s\t%s\t%s\n' "$k" "$ty" "$v" >> "$file"; } ;;
*) exit 1 ;;
esac
EOF
    chmod +x "$G/stub/PlistBuddy"
    CALLS="$G/calls"
    : > "$CALLS"
}

# job [key type value]...: aslmanager's job as 10.9.5 ships it (a Mach service, on demand), plus
# any keys given.
job() {
    printf 'Label\tstring\tcom.apple.aslmanager\nMachServices:com.apple.aslmanager\tbool\ttrue\n' > "$PL"
    while [ "$#" -ge 3 ]; do
        printf '%s\t%s\t%s\n' "$1" "$2" "$3" >> "$PL"
        shift 3
    done
}

unthrottle() {
    run env -i PATH="/usr/bin:/bin" G="$G" FNS="$FNS" CALLS="$CALLS" STUB_ADD_FAILS="${STUB_ADD_FAILS:-}" /bin/sh -c '
        . "$FNS"
        LOG=$G/guest-macosx-firstboot.log
        ASLMANAGER_PLIST=$G/LaunchDaemons/com.apple.aslmanager.plist
        PLISTBUDDY=$G/stub/PlistBuddy
        unthrottle_aslmanager'
}

@test "the paths are 10.9's" {
    grep -qx 'ASLMANAGER_PLIST=/System/Library/LaunchDaemons/com.apple.aslmanager.plist' "$FB"
    grep -qx 'PLISTBUDDY=/usr/libexec/PlistBuddy' "$FB"
}

@test "aslmanager's on-demand job gets ThrottleInterval 0, and the log says why" {
    job
    unthrottle
    [ "$status" -eq 0 ]
    grep -qx "ThrottleInterval	integer	0" "$PL"
    grep -q "aslmanager: the job had launchd's default 10 s throttle, which can hold syslogd's first writes; ThrottleInterval 0 added to $PL, from the next boot" "$G/guest-macosx-firstboot.log"
}

@test "the rest of the job is left as it was" {
    job
    unthrottle
    [ "$(grep -c . "$PL")" -eq 3 ]
    grep -qx "Label	string	com.apple.aslmanager" "$PL"
    grep -qx "MachServices:com.apple.aslmanager	bool	true" "$PL"
}

@test "run twice: the second run finds the key and changes nothing" {
    job
    unthrottle
    unthrottle
    [ "$status" -eq 0 ]
    [ "$(grep -c '^ThrottleInterval' "$PL")" -eq 1 ]
    grep -q "aslmanager: the job already sets ThrottleInterval (0); left as it is" "$G/guest-macosx-firstboot.log"
}

@test "inert: a job that already sets its own ThrottleInterval keeps it, and the log names it" {
    job ThrottleInterval integer 5
    unthrottle
    [ "$status" -eq 0 ]
    grep -qx "ThrottleInterval	integer	5" "$PL"
    [ "$(grep -c '^ThrottleInterval' "$PL")" -eq 1 ]
    ! grep -q '^PlistBuddy -c Add' "$CALLS" || false
    grep -q "aslmanager: the job already sets ThrottleInterval (5); left as it is" "$G/guest-macosx-firstboot.log"
}

@test "inert: a job that is not the on-demand com.apple.aslmanager service is not touched" {
    printf 'Label\tstring\tcom.apple.aslmanager\nStartInterval\tinteger\t3600\n' > "$PL"
    unthrottle
    [ "$status" -eq 0 ]
    ! grep -q '^ThrottleInterval' "$PL" || false
    ! grep -q '^PlistBuddy -c Add' "$CALLS" || false
    grep -q "aslmanager: $PL is not the on-demand com.apple.aslmanager service; left as it is" "$G/guest-macosx-firstboot.log"
}

@test "inert: a job launchd keeps alive or starts at load is not unthrottled, and the log names the key" {
    for key in KeepAlive RunAtLoad; do
        job "$key" bool true
        : > "$G/guest-macosx-firstboot.log"
        unthrottle
        [ "$status" -eq 0 ]
        ! grep -q '^ThrottleInterval' "$PL" || false
        grep -q "aslmanager: $PL sets $key, so launchd can relaunch it without a waiting ask; left throttled, as it is" "$G/guest-macosx-firstboot.log"
    done
}

@test "inert: no plist at all, nothing to change, and the log says so" {
    unthrottle
    [ "$status" -eq 0 ]
    [ ! -e "$PL" ]
    ! grep -q '^PlistBuddy' "$CALLS" || false
    grep -q "aslmanager: no $PL; syslogd has no aslmanager to wait on, nothing to change" "$G/guest-macosx-firstboot.log"
}

@test "a job that cannot be changed is logged as a failure, and the run goes on" {
    job
    STUB_ADD_FAILS=1 unthrottle
    [ "$status" -eq 0 ]
    ! grep -q '^ThrottleInterval' "$PL" || false
    grep -q "aslmanager: COULD NOT add ThrottleInterval to $PL; a shutdown in about a boot's first 12 s can still lose that boot's log" "$G/guest-macosx-firstboot.log"
}

@test "firstboot.sh runs it on 10.9 only, after the updates" {
    call=$(grep -n '^    unthrottle_aslmanager$' "$FB" | cut -d: -f1)
    [ -n "$call" ]
    [ "$(grep -c '^    unthrottle_aslmanager$' "$FB")" -eq 1 ]
    guard=$(sed -n "$((call - 1))p" "$FB")
    [ "$guard" = 'if [ -z "$GUEST_MACOSX_FB_RELEASE" ]; then' ]
    upd=$(grep -n '^    install_updates$' "$FB" | head -n 1 | cut -d: -f1)
    [ -n "$upd" ]
    [ "$upd" -lt "$call" ]
}
