#!/usr/bin/env bats
# platform: host-agnostic
#
# firstboot.sh's settle_xprotect_schedule (docs/decisions/0045).
#
# Security Update 2013-004's XProtectUpdaterInit, on the boot after the
# update, unloads XProtect's job to give its plist a daily time and loads
# it again; crsud, starting for the first time on that same boot, turns
# itself off for good if it asks whether XProtect is enabled in between.
# So firstboot runs XProtectUpdaterInit itself, on the boot that installed
# the update, while the plist has no daily time; on any other guest it
# does nothing but say so.
#
# SAFETY: these run firstboot's function on this machine with every path in
# the test's own directory, PATH=/usr/bin:/bin (no sbin); the
# XProtectUpdaterInit they run is the test's own script.

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    FB="$REPO/assets/guest/firstboot.sh"
    FNS="$BATS_TEST_TMPDIR/fns.sh"
    : > "$FNS"
    for f in say run_with_timeout settle_xprotect_schedule; do
        fn=$(awk "/^$f\\(\\) \\{/,/^\\}/" "$FB")
        [ -n "$fn" ] || { echo "firstboot.sh has no function $f" >&2; return 1; }
        printf '%s\n' "$fn" >> "$FNS"
    done
    G="$BATS_TEST_TMPDIR/guest"
    mkdir -p "$G"
    PLIST="$G/com.apple.xprotectupdater.plist"
    INIT="$G/XProtectUpdaterInit"
}

# interval: XProtect's plist as 2013-004 leaves it, with no daily time.
interval() {
    printf '<plist version="1.0">\n<dict>\n\t<key>StartInterval</key>\n\t<integer>86400</integer>\n</dict>\n</plist>\n' > "$PLIST"
}

# daily: XProtect's plist with the daily time XProtectUpdaterInit writes.
daily() {
    printf '<plist version="1.0">\n<dict>\n\t<key>StartCalendarInterval</key>\n\t<dict/>\n</dict>\n</plist>\n' > "$PLIST"
}

# init [works|fails]: an XProtectUpdaterInit that notes it ran and, unless
# it fails, gives the plist its daily time, as Apple's does. It finds G and
# PLIST in the environment settle runs it with.
init() {
    echo "${1:-works}" > "$G/init-mode"
    cat > "$INIT" <<'EOF'
#!/bin/sh
echo ran >> "$G/init-ran"
[ "$(cat "$G/init-mode")" = works ] || exit 1
printf '<plist version="1.0">\n<dict>\n\t<key>StartCalendarInterval</key>\n\t<dict/>\n</dict>\n</plist>\n' > "$PLIST"
EOF
    chmod 755 "$INIT"
}

settle() {
    run env -i PATH="/usr/bin:/bin" G="$G" FNS="$FNS" PLIST="$PLIST" INIT="$INIT" /bin/sh -c '
        . "$FNS"
        LOG=$G/guest-macosx-firstboot.log
        XPROTECT_PLIST=$PLIST
        XPROTECT_INIT=$INIT
        settle_xprotect_schedule'
}

@test "a plist with no daily time: XProtectUpdaterInit runs once, and the log says it took" {
    interval
    init
    settle
    [ "$status" -eq 0 ]
    [ "$(cat "$G/init-ran")" = ran ]
    grep -q '<key>StartCalendarInterval</key>' "$PLIST"
    grep -q "XProtect: $PLIST has no daily time; running $INIT now, before crsud first starts" "$G/guest-macosx-firstboot.log"
    grep -q "XProtect: $PLIST has its daily time" "$G/guest-macosx-firstboot.log"
}

@test "run twice: the second run finds the daily time and leaves XProtect alone" {
    interval
    init
    settle
    settle
    [ "$status" -eq 0 ]
    [ "$(wc -l < "$G/init-ran")" -eq 1 ]
    grep -q "XProtect: $PLIST already has a daily time; nothing to settle" "$G/guest-macosx-firstboot.log"
}

@test "inert: a plist that already has its daily time is left alone, and the log says so" {
    daily
    init
    settle
    [ "$status" -eq 0 ]
    [ ! -e "$G/init-ran" ]
    grep -q "XProtect: $PLIST already has a daily time; nothing to settle" "$G/guest-macosx-firstboot.log"
}

@test "inert: no XProtectUpdaterInit (no 2013-004, or 10.9) runs nothing, and the log says so" {
    interval
    settle
    [ "$status" -eq 0 ]
    grep -q "XProtect: no $INIT; nothing to settle" "$G/guest-macosx-firstboot.log"
    run grep -c StartCalendarInterval "$PLIST"
    [ "$output" = 0 ]
}

@test "an XProtectUpdaterInit that fails is logged as a failure, and the run goes on" {
    interval
    init fails
    settle
    [ "$status" -eq 0 ]
    [ "$(cat "$G/init-ran")" = ran ]
    grep -q "XProtect: COULD NOT settle $PLIST; the next boot's $INIT unloads XProtect, and crsud can turn itself off if it checks then" "$G/guest-macosx-firstboot.log"
}

@test "firstboot.sh names Apple's own paths for XProtect's job and its init" {
    grep -qx 'XPROTECT_PLIST=/System/Library/LaunchDaemons/com.apple.xprotectupdater.plist' "$FB"
    grep -qx 'XPROTECT_INIT=/usr/libexec/XProtectUpdaterInit' "$FB"
}
