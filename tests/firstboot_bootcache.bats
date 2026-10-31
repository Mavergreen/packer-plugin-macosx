#!/usr/bin/env bats
# platform: host-agnostic
#
# firstboot.sh's set_bootcache_aside (docs/decisions/0025).
#
# 10.6.8's BootCache.kext (31.1) shuts its boot cache down by itself
# inside its own strategy hook, on the reading thread, once a read takes
# its hit rate under 50 % after 500 lookups: it closes
# /var/db/BootCache.data there, and HFS's inactive call wants a journal
# transaction while that thread holds a B-tree's lock (the catalog's, the
# attributes file's) shared. A writer holding the journal that wants that
# lock, or the reader itself, never moves again (MEASURED 2026-10-18,
# F22). The hook is installed only when launchctl runs `BootCacheControl
# start`, and launchctl runs it only from /usr/sbin, so on a guest with
# that BootCache it is moved aside; any other BootCache, and a guest with
# none, is left alone, and the log says which.
#
# SAFETY: these run firstboot's function on this machine with every path in
# the test's own directory, PATH=stubs:/usr/bin:/bin (no sbin).

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    FB="$REPO/assets/guest/firstboot.sh"
    FNS="$BATS_TEST_TMPDIR/fns.sh"
    : > "$FNS"
    for f in say set_bootcache_aside; do
        fn=$(awk "/^$f\\(\\) \\{/,/^\\}/" "$FB")
        [ -n "$fn" ] || { echo "firstboot.sh has no function $f" >&2; return 1; }
        printf '%s\n' "$fn" >> "$FNS"
    done
    # The versions measured to deadlock, as firstboot.sh has them.
    DEADLOCKS=$(sed -n 's/^BOOTCACHE_DEADLOCKS="\(.*\)"$/\1/p' "$FB")
    [ -n "$DEADLOCKS" ] || { echo "firstboot.sh sets no BOOTCACHE_DEADLOCKS" >&2; return 1; }
    G="$BATS_TEST_TMPDIR/guest"
    mkdir -p "$G/sbin" "$G/BootCache.kext/Contents"
}

# kext <version>: BootCache.kext's Info.plist, as 10.6.8's is laid out.
kext() {
    cat > "$G/BootCache.kext/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>CFBundleIdentifier</key>
	<string>com.apple.BootCache</string>
	<key>CFBundleShortVersionString</key>
	<string>$1</string>
	<key>CFBundleVersion</key>
	<string>$1</string>
</dict>
</plist>
EOF
}

control() {
    printf 'BootCacheControl\n' > "$G/sbin/BootCacheControl"
    chmod 755 "$G/sbin/BootCacheControl"
}

aside() {
    run env -i PATH="/usr/bin:/bin" G="$G" FNS="$FNS" DEADLOCKS="$DEADLOCKS" /bin/sh -c '
        . "$FNS"
        LOG=$G/guest-macosx-firstboot.log
        BOOTCACHE_CONTROL=$G/sbin/BootCacheControl
        BOOTCACHE_KEXT_PLIST=$G/BootCache.kext/Contents/Info.plist
        BOOTCACHE_DEADLOCKS=$DEADLOCKS
        set_bootcache_aside'
}

@test "the measured BootCache is 10.6.8's, 31.1" {
    [ "$DEADLOCKS" = 31.1 ]
}

@test "BootCache 31.1: BootCacheControl is set aside, whole, and the log says why" {
    kext 31.1
    control
    aside
    [ "$status" -eq 0 ]
    [ ! -e "$G/sbin/BootCacheControl" ]
    [ "$(cat "$G/sbin/BootCacheControl-disabled")" = BootCacheControl ]
    [ -x "$G/sbin/BootCacheControl-disabled" ]
    grep -q "BootCache: BootCache.kext 31.1 deadlocks HFS when it shuts its cache down; BootCacheControl set aside to $G/sbin/BootCacheControl-disabled, so launchd starts no boot cache" "$G/guest-macosx-firstboot.log"
}

@test "BootCache 31.1 run twice: the second run says it is already aside, and keeps the first's" {
    kext 31.1
    control
    aside
    aside
    [ "$status" -eq 0 ]
    [ "$(cat "$G/sbin/BootCacheControl-disabled")" = BootCacheControl ]
    grep -q "BootCache: $G/sbin/BootCacheControl already set aside ($G/sbin/BootCacheControl-disabled); launchd starts no boot cache" "$G/guest-macosx-firstboot.log"
    run grep -c "nothing to set aside" "$G/guest-macosx-firstboot.log"
    [ "$output" = 0 ]
}

@test "inert: another BootCache (10.6.0's, 31) is left in place, and the log names its version" {
    kext 31
    control
    aside
    [ "$status" -eq 0 ]
    [ -e "$G/sbin/BootCacheControl" ]
    [ ! -e "$G/sbin/BootCacheControl-disabled" ]
    grep -q "BootCache: BootCache.kext is 31, not one measured to deadlock (31.1); BootCacheControl left in place" "$G/guest-macosx-firstboot.log"
}

@test "inert: a version that only starts with 31.1 is not 31.1" {
    kext 31.10
    control
    aside
    [ -e "$G/sbin/BootCacheControl" ]
    grep -q "BootCache.kext is 31.10, not one measured" "$G/guest-macosx-firstboot.log"
}

@test "inert: versions that 31.1 only starts or ends with are not 31.1 (3, 1, 1.1)" {
    for v in 3 1 1.1 31.; do
        kext "$v"
        control
        aside
        [ -e "$G/sbin/BootCacheControl" ]
        grep -q "BootCache.kext is $v, not one measured" "$G/guest-macosx-firstboot.log"
    done
}

@test "verify.sh holds the same list of BootCache versions as firstboot.sh" {
    v=$(sed -n 's/^BOOTCACHE_DEADLOCKS=//p' "$REPO/templates/snowleopard/verify.sh" | tr -d '"')
    [ -n "$v" ]
    [ "$v" = "$DEADLOCKS" ]
}

@test "inert: no BootCache.kext Info.plist to read leaves BootCacheControl in place, and says so" {
    control
    aside
    [ "$status" -eq 0 ]
    [ -e "$G/sbin/BootCacheControl" ]
    grep -q "BootCache: BootCache.kext is unreadable, not one measured to deadlock (31.1); BootCacheControl left in place" "$G/guest-macosx-firstboot.log"
}

@test "inert: no BootCacheControl, nothing to set aside, and the log says so" {
    kext 31.1
    aside
    [ "$status" -eq 0 ]
    [ ! -e "$G/sbin/BootCacheControl-disabled" ]
    grep -q "BootCache: no $G/sbin/BootCacheControl, so launchd starts no boot cache; nothing to set aside" "$G/guest-macosx-firstboot.log"
}

@test "a BootCacheControl that cannot be moved is left, logged as a failure, and the run goes on" {
    [ "$(id -u)" -ne 0 ] || skip "root can move it anyway"
    kext 31.1
    control
    chmod 555 "$G/sbin"
    aside
    chmod 755 "$G/sbin"
    [ "$status" -eq 0 ]
    [ -e "$G/sbin/BootCacheControl" ]
    grep -q "BootCache: COULD NOT set $G/sbin/BootCacheControl aside; BootCache 31.1 starts at the next boot and can deadlock in its first minute" "$G/guest-macosx-firstboot.log"
}
