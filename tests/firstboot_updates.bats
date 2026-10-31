#!/usr/bin/env bats
# platform: host-agnostic
#
# firstboot.sh's Snow Leopard updates, one boot each (docs/decisions/0023).
#
# 10.6.8's combo replaces part of the running 10.6.0 under it, and defers
# the rest (CoreFoundation, Security, CoreServices, ApplicationServices) to
# /etc/rc.deferred_install, which launchd runs at the end of the next
# shutdown. Security Update 2013-004 installed before that restart ran its
# new Security.framework against 10.6.0's securityd: MRTAgent aborted, MRT
# could not remove its own jobs, launchd 10.6.0 crashed at the shutdown on
# them and took configd with it, and the combo's deferred install then put
# its older CoreFoundation and Security back over 2013-004's (MEASURED
# 2026-10-15: 3 of 3 first boots so). Combo, restart, 2013-004, restart:
# 0 crash reports, and 2013-004's files stay.
#
# So on 10.6 firstboot.sh installs one update, and restarts before the next
# when the update asks for a restart (its metadata) or left a deferred
# install. Where neither is so, it installs the next on the same boot, as
# before: that is the case below that shows it is inert.
#
# SAFETY: firstboot runs as root on a guest, and these tests run its
# functions on this machine. They get PATH=stubs:/usr/bin:/bin (no sbin),
# every path is in the test's own directory, and every name that could
# touch the host (shutdown, reboot, halt, killall, sudo, launchctl,
# installer, defaults) is a stub. shutdown records that it was asked and
# does nothing else, but notes whether kextd's job was loaded then;
# launchctl answers only for kextd's job, unloading, loading, starting and
# listing it in the test's own directory ($G/kextd-loaded).

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    FB="$REPO/assets/guest/firstboot.sh"
    FNS="$BATS_TEST_TMPDIR/fns.sh"
    : > "$FNS"
    for f in say run_with_timeout update_wants_restart kext_caches_current \
        kext_caches_are_current snow_leopard_updates snow_leopard_updates_after \
        finish_snow_leopard set_bootcache_aside update_installs_kernel hold_kextd_for \
        install_updates remote_login_on restart_into_updates kernelcache_current \
        kernelcache_is_current; do
        fn=$(awk "/^$f\\(\\) \\{/,/^\\}/" "$FB")
        [ -n "$fn" ] || { echo "firstboot.sh has no function $f" >&2; return 1; }
        printf '%s\n' "$fn" >> "$FNS"
    done

    STUB="$BATS_TEST_TMPDIR/stub"
    G="$BATS_TEST_TMPDIR/guest"
    mkdir -p "$STUB" "$G/conf/updates"
    export CALLS="$G/calls"
    : > "$CALLS"
    # installer: records the package; a combo (*combo*) leaves a deferred
    # install, as 10.6.8's does, unless STUB_NO_DEFERRED is set, and, as its
    # postinstall actions do after its own kextcache run, changes
    # /System/Library/Extensions, unless STUB_KEXTS_CURRENT is set.
    # It also notes, in $G/during, what the list of updates left and the
    # update in progress were while it ran.
    cat > "$STUB/installer" <<'EOF'
#!/bin/sh
pkg=
while [ $# -gt 0 ]; do [ "$1" = -pkg ] && pkg=$2; shift; done
echo "installer $(basename "$pkg")" >> "$CALLS"
[ -z "${STUB_INSTALLER_HANG:-}" ] || exec /bin/sleep 60
[ -z "${STUB_INSTALLER_STATUS:-}" ] || exit "$STUB_INSTALLER_STATUS"
printf 'left=%s current=%s kextd=%s\n' \
    "$(tr '\n' ' ' < "$G/conf/updates.left" 2>/dev/null | sed 's/ $//')" \
    "$(cat "$G/conf/updates.current" 2>/dev/null)" \
    "$([ -e "$G/kextd-loaded" ] && echo loaded || echo off)" >> "$G/during"
case $pkg in
    *combo*)
        [ -n "${STUB_NO_DEFERRED:-}" ] || : > "$G/rc.deferred_install"
        [ -n "${STUB_KEXTS_CURRENT:-}" ] || touch -t 202601010200 "$G/Extensions"
        # 10.6.8's BootCache (31.1) and its own BootCacheControl, over
        # 10.6.0's, as the combo installs them (docs/decisions/0025).
        printf '<key>CFBundleVersion</key>\n<string>31.1</string>\n' > "$G/BootCache.plist"
        : > "$G/sbin/BootCacheControl"
        ;;
esac
exit 0
EOF
    # stat -f %m <file>: 10.6's BSD stat, the file's mtime in seconds,
    # whichever stat this host has (GNU's -f is something else).
    cat > "$STUB/stat" <<'EOF'
#!/bin/sh
if [ $# -ne 3 ] || [ "$1" != -f ] || [ "$2" != %m ]; then
    echo "stub stat: only -f %m <file>, not: $*" >&2
    exit 2
fi
[ -e "$3" ] || { echo "stat: $3: stat: No such file or directory" >&2; exit 1; }
/usr/bin/stat -c %Y "$3" 2>/dev/null || /usr/bin/stat -f %m "$3"
EOF
    # kextcache: records its arguments; -system-caches writes the three
    # caches kextd's rescan decision reads as the real one does
    # (updateDirectoryCaches, OSKext.c's _OSKextWriteCache): each at the
    # extensions folder's mtime + 1 s, which is what kextd calls current.
    # STUB_KC_SKIP=<file> leaves that one as it was, as a kextcache run that
    # failed halfway would.
    cat > "$STUB/kextcache" <<'EOF'
#!/bin/sh
echo "kextcache $*" >> "$CALLS"
if [ "$1" = -system-caches ]; then
    t=$(( $(stat -f %m "$G/Extensions") + 1 ))
    stamp=$(/bin/date -u -d "@$t" +%Y%m%d%H%M.%S 2>/dev/null ||
        /bin/date -u -r "$t" +%Y%m%d%H%M.%S)
    for f in KextIdentifiers.plist.gz IOKitPersonalities_x86_64.ioplist.gz \
        KextPropertyValues_OSBundleHelper_x86_64.plist.gz; do
        [ "$f" = "${STUB_KC_SKIP:-}" ] || TZ=UTC0 touch -t "$stamp" "$G/kc/$f"
    done
fi
# -update-volume / on 10.9 (where $G/kernelcache is there): the prelinked kernel, stamped as the
# real one stamps it, at the newest of its extensions folders' and its kernel's mtimes + 1 s
# (kext_tools' check_kext_boot_cache_file). STUB_KC_SKIP=kernelcache leaves it.
if [ "$1 $2" = "-update-volume /" ] && [ -e "$G/kernelcache" ] && [ "${STUB_KC_SKIP:-}" != kernelcache ]; then
    t=0
    for d in "$G/Extensions" "$G/LExtensions"; do
        m=$(stat -f %m "$d"); [ $((m + 1)) -gt "$t" ] && t=$((m + 1))
    done
    k=$(stat -f %m "$G/mach_kernel"); [ "$k" -gt "$t" ] && t=$((k + 1))
    stamp=$(/bin/date -u -d "@$t" +%Y%m%d%H%M.%S 2>/dev/null ||
        /bin/date -u -r "$t" +%Y%m%d%H%M.%S)
    TZ=UTC0 touch -t "$stamp" "$G/kernelcache"
fi
exit 0
EOF
    # uname -m: the running kernel's architecture, as 10.6's K64 prints it.
    # uname -r: 10.6.0's kernel release.
    cat > "$STUB/uname" <<'EOF'
#!/bin/sh
case "$*" in -m) echo x86_64 ;; -r) echo 10.0.0 ;;
    *) echo "stub uname: only -m or -r, not: $*" >&2; exit 2 ;; esac
EOF
    # xar -xf <pkg> PackageInfo|Bom: the PackageInfo or Bom a test put
    # beside <pkg> (<pkg>.PackageInfo, <pkg>.Bom).
    cat > "$STUB/xar" <<'EOF'
#!/bin/sh
[ "$1" = -xf ] || exit 1
case $3 in PackageInfo | Bom) ;; *) exit 1 ;; esac
[ -f "$2.$3" ] || exit 1
cp "$2.$3" "$3"
EOF
    # lsbom -s -f <Bom>: the paths of the files a Bom lists; a test's Bom is
    # that list. STUB_LSBOM_FAIL makes it fail as on a Bom it cannot parse:
    # a complaint, a partial list, and a non-zero status.
    cat > "$STUB/lsbom" <<'EOF'
#!/bin/sh
[ "$1 $2" = "-s -f" ] || { echo "stub lsbom: only -s -f <Bom>, not: $*" >&2; exit 2; }
if [ -n "${STUB_LSBOM_FAIL:-}" ]; then
    echo .
    echo "lsbom: $3: bad Bom" >&2
    exit 1
fi
cat "$3"
EOF
    # launchctl unload|load <kextd's plist>, start|list com.apple.kextd:
    # kextd's job, loaded while $G/kextd-loaded is there. load puts it back
    # (as launchd would, and kextd would then send the disk's personalities
    # to the running kernel); start answers as launchctl does, failing for
    # a job that is not loaded. Records each call in $G/launchctl;
    # STUB_LAUNCHCTL_FAIL keeps the job loaded, as an unload that did not
    # happen would. Anything else is refused.
    cat > "$STUB/launchctl" <<'EOF'
#!/bin/sh
echo "launchctl $*" >> "$G/launchctl"
case "$*" in
    "unload $G/com.apple.kextd.plist")
        [ -n "${STUB_LAUNCHCTL_FAIL:-}" ] && exit 1
        rm -f "$G/kextd-loaded"; exit 0 ;;
    "load $G/com.apple.kextd.plist" | "load -w $G/com.apple.kextd.plist")
        : > "$G/kextd-loaded"; exit 0 ;;
    "start com.apple.kextd" | "list com.apple.kextd") [ -e "$G/kextd-loaded" ]; exit ;;
    "load -w $G/ssh.plist") echo "launchctl $*" >> "$CALLS"; exit 0 ;;
esac
echo "stub launchctl refused: $*" >&2
exit 99
EOF
    # shutdown: records that it was asked, and, in $G/at-shutdown, whether
    # kextd's job was loaded when it was; fails if STUB_SHUTDOWN_FAIL is
    # set, as a shutdown that does not happen would.
    cat > "$STUB/shutdown" <<'EOF'
#!/bin/sh
echo "shutdown $*" >> "$CALLS"
if [ -e "$G/kextd-loaded" ]; then echo kextd=loaded; else echo kextd=off; fi >> "$G/at-shutdown"
[ -z "${STUB_SHUTDOWN_FAIL:-}" ] || exit 1
exit 0
EOF
    # 10.9's shutdown helper (firstboot.sh's write_shutdown_helper), as
    # SHUTDOWN_HELPER names it: records the call, and kextd's state, as the
    # shutdown stub does; fails if STUB_SHUTDOWN_FAIL is set.
    cat > "$G/guest-macosx-shutdown" <<'EOF'
#!/bin/sh
echo "guest-macosx-shutdown $*" >> "$CALLS"
if [ -e "$G/kextd-loaded" ]; then echo kextd=loaded; else echo kextd=off; fi >> "$G/at-shutdown"
[ -z "${STUB_SHUTDOWN_FAIL:-}" ] || exit 1
exit 0
EOF
    chmod +x "$G/guest-macosx-shutdown"
    cat > "$STUB/systemsetup" <<'EOF'
#!/bin/sh
echo "systemsetup $*" >> "$CALLS"
[ "$1" = -getremotelogin ] && echo "Remote Login: On"
exit 0
EOF
    cat > "$STUB/defaults" <<'EOF'
#!/bin/sh
echo "defaults $*" >> "$CALLS"
exit 0
EOF
    cat > "$STUB/sw_vers" <<'STUB'
#!/bin/sh
echo 10K549
STUB
    cat > "$STUB/pkgutil" <<'STUB'
#!/bin/sh
exit 0
STUB
    cat > "$STUB/date" <<'STUB'
#!/bin/sh
echo 2026-10-15T00:00:00Z
STUB
    for name in reboot halt killall sudo; do
        cat > "$STUB/$name" <<'STUB'
#!/bin/sh
echo "stub ${0##*/} refused: $*" >&2
exit 99
STUB
    done
    chmod +x "$STUB"/*
    # bounded <command...>: <command>, killed after 20 s, with a note in
    # $G/timedout: a loop in the code under test (a list of updates that
    # never shrinks) fails its test instead of hanging the suite.
    BOUNDED="$BATS_TEST_TMPDIR/bounded"
    cat > "$BOUNDED" <<'EOF'
#!/bin/sh
"$@" &
pid=$!
(
    sleep 20
    kill -9 "$pid" 2>/dev/null && echo "killed after 20 s: a loop?" > "$G/timedout"
) >/dev/null 2>&1 &
dog=$!
wait "$pid"
rc=$?
kill "$dog" 2>/dev/null
exit "$rc"
EOF
    chmod +x "$BOUNDED"
    : > "$G/daemon.plist"
    # kextd's job, loaded, as launchd loads it at every boot.
    : > "$G/kextd-loaded"
    # 10.6.0's BootCache (31) and the BootCacheControl launchctl runs; the
    # combo's installer stub puts 10.6.8's (31.1) and its own in their
    # place (docs/decisions/0025).
    mkdir -p "$G/sbin"
    : > "$G/sbin/BootCacheControl"
    printf '<key>CFBundleVersion</key>\n<string>31</string>\n' > "$G/BootCache.plist"
    # The kext caches, current as kextd judges them: the cache's mtime the
    # extensions folder's + 1 s.
    kexts 202601010000.00 202601010000.01
}

# The three caches kextd's rescan decision reads (kext_tools' bootcaches.c,
# directoryPlistCachesNeedRebuild), for the stub's x86_64 kernel.
KC_IDS=KextIdentifiers.plist.gz
KC_PERS=IOKitPersonalities_x86_64.ioplist.gz
KC_PROPS=KextPropertyValues_OSBundleHelper_x86_64.plist.gz

# kexts <folder stamp> <cache stamp> [<file>]: the extensions folder's and
# the caches' mtimes, as touch -t takes them; with <file>, only that cache
# at <cache stamp>, the others current (the folder's + 1 s).
kexts() {
    local c
    mkdir -p "$G/Extensions" "$G/kc"
    touch -t "$1" "$G/Extensions"
    for c in "$KC_IDS" "$KC_PERS" "$KC_PROPS"; do
        if [ -z "${3:-}" ] || [ "$c" = "$3" ]; then
            touch -t "$2" "$G/kc/$c"
        else
            touch -t "${1%.*}.01" "$G/kc/$c"
        fi
    done
}

# kcc: kext_caches_current alone, before a restart.
kcc() {
    run env -i PATH="$STUB:/usr/bin:/bin" CALLS="$CALLS" G="$G" FNS="$FNS" /bin/sh -c '
        . "$FNS"; CONF_DIR=$G/conf; LOG=$G/guest-macosx-firstboot.log
        KEXT_DIR=$G/Extensions; KEXT_CACHE_DIR=$G/kc
        kext_caches_current'
}

# update <name> restart|none: an update in conf/updates. A .dist says so in
# its onConclusion; a .pkg in the PackageInfo the stub xar hands out. A
# combo (*combo*) installs a kernel, as 10.6.8's does: a .dist names two
# packages, carried beside it, the second's Bom listing ./mach_kernel; a
# .pkg's own Bom lists it. Any other update's Boms list no kernel.
update() {
    local p="$G/conf/updates/$1" k=
    case $1 in *combo*) k=./mach_kernel ;; esac
    case $1 in
        *.dist)
            if [ "$2" = restart ]; then
                echo '<pkg-ref id="x" onConclusion="RequireRestart"/>' > "$p"
            else
                echo '<pkg-ref id="x" onConclusion="None"/>' > "$p"
            fi
            printf '%s\n' \
                "<pkg-ref id=\"a\" auth=\"Root\">$1.a.pkg</pkg-ref>" \
                "<pkg-ref id=\"b\" auth=\"Root\" active=\"x('/')\">$1.b.pkg</pkg-ref>" >> "$p"
            echo pkg > "$G/conf/updates/$1.a.pkg"
            echo pkg > "$G/conf/updates/$1.b.pkg"
            printf '%s\n' . ./System ./usr/lib/libSystem.B.dylib > "$G/conf/updates/$1.a.pkg.Bom"
            printf '%s\n' . ./System $k > "$G/conf/updates/$1.b.pkg.Bom"
            ;;
        *)
            echo pkg > "$p"
            if [ "$2" = restart ]; then
                echo '<pkg-info postinstall-action="restart"/>' > "$p.PackageInfo"
            else
                echo '<pkg-info postinstall-action="none"/>' > "$p.PackageInfo"
            fi
            printf '%s\n' . ./System $k > "$p.Bom"
            ;;
    esac
}

# guest <script>: <script> run as firstboot.sh runs it, after its functions
# and with its paths, all in the test's own directory, and with
# GUEST_MACOSX_FB_UPDATE_PKGS=$PKGS.
guest() {
    run env -i PATH="$STUB:/usr/bin:/bin" CALLS="$CALLS" G="$G" FNS="$FNS" \
        STUB_NO_DEFERRED="${STUB_NO_DEFERRED:-}" STUB_KEXTS_CURRENT="${STUB_KEXTS_CURRENT:-}" \
        STUB_SHUTDOWN_FAIL="${STUB_SHUTDOWN_FAIL:-}" STUB_LAUNCHCTL_FAIL="${STUB_LAUNCHCTL_FAIL:-}" \
        STUB_LSBOM_FAIL="${STUB_LSBOM_FAIL:-}" STUB_KC_SKIP="${STUB_KC_SKIP:-}" \
        STUB_INSTALLER_STATUS="${STUB_INSTALLER_STATUS:-}" STUB_INSTALLER_HANG="${STUB_INSTALLER_HANG:-}" \
        PKGS="${PKGS:-}" BODY="$1" \
        "$BOUNDED" /bin/sh -c '
            . "$FNS"
            CONF_DIR=$G/conf
            LOG=$G/guest-macosx-firstboot.log
            LOG_COPY=$G/root-guest-macosx-firstboot.log
            DAEMON=$G/daemon.plist
            DONE_MARKER=$CONF_DIR/.done
            UPDATES_LEFT=$CONF_DIR/updates.left
            UPDATES_CURRENT=$CONF_DIR/updates.current
            DEFERRED_INSTALL=$G/rc.deferred_install
            SSHD_OVERRIDES=$G/overrides
            KEXT_DIR=$G/Extensions
            KEXT_CACHE_DIR=$G/kc
            KEXTD_PLIST=$G/com.apple.kextd.plist
            KEXTD_JOB=com.apple.kextd
            BOOTCACHE_CONTROL=$G/sbin/BootCacheControl
            BOOTCACHE_KEXT_PLIST=$G/BootCache.plist
            BOOTCACHE_DEADLOCKS=31.1
            SSH_PLIST=$G/ssh.plist
            KEXT_DIRS="$G/Extensions $G/LExtensions"
            KERNEL_FILE=$G/mach_kernel
            KERNELCACHE=$G/kernelcache
            SHUTDOWN_HELPER=$G/guest-macosx-shutdown
            GUEST_MACOSX_FB_UPDATES=security
            GUEST_MACOSX_FB_UPDATE_PKGS=$PKGS
            eval "$BODY"
        '
}

@test "updates: a failed installer stops the chain and records a verification failure" {
    update 01.pkg none
    update 02.pkg none
    PKGS="01.pkg 02.pkg"
    STUB_INSTALLER_STATUS=37
    guest 'snow_leopard_updates'
    [ "$status" -eq 0 ]
    [[ "$output" == *"updates: STOPPED: 01.pkg installer failed (rc=37)"* ]]
    [ "$(grep -c '^installer ' "$CALLS")" -eq 1 ]
    [ "$(cat "$G/conf/updates.current")" = 01.pkg ]
    grep -q '^02.pkg$' "$G/conf/updates.left"
    # A subsequent invocation must not retry it or install its successor.
    guest 'snow_leopard_updates'
    [ "$status" -eq 0 ]
    [[ "$output" == *"updates: STOPPED:"* ]]
    [ "$(grep -c '^installer ' "$CALLS")" -eq 1 ]
}

@test "updates: a timed out installer stops before any successor" {
    update 01.pkg none
    update 02.pkg none
    PKGS="01.pkg 02.pkg"
    STUB_INSTALLER_HANG=1
    # Test-only short bound; production keeps its original 3600 seconds.
    sed 's/3600 installer/1 installer/g' "$FNS" > "$FNS.new"
    mv "$FNS.new" "$FNS"
    guest 'snow_leopard_updates'
    [ "$status" -eq 0 ]
    [[ "$output" == *"updates: STOPPED: 01.pkg installer failed (rc="* ]]
    grep -q 'TIMED OUT after 1s' "$G/guest-macosx-firstboot.log"
    [ "$(grep -c '^installer ' "$CALLS")" -eq 1 ]
    [ "$(cat "$G/conf/updates.current")" = 01.pkg ]
}

@test "runner: checked status propagates without changing best-effort callers" {
    guest 'run_with_timeout 1 sh -c "exit 37"; echo BEST_EFFORT=$?;
        run_with_timeout --checked 1 sh -c "exit 37"; echo CHECKED=$?'
    [ "$status" -eq 0 ]
    [[ "$output" == *"BEST_EFFORT=0"* ]]
    [[ "$output" == *"CHECKED=37"* ]]
}

# boot <updates...>: one run of firstboot's Snow Leopard update steps, as
# its main line (or its resume) runs them, with GUEST_MACOSX_FB_UPDATE_PKGS=<updates>.
boot() {
    PKGS="$*" guest '
        snow_leopard_updates
        finish_snow_leopard
        echo "fell through" >&2
        exit 3'
}

# A restart applies the deferred install, as launchd does at the shutdown,
# and the next boot's launchd loads kextd's job again from its plist.
restart() {
    rm -f "$G/rc.deferred_install"
    : > "$G/kextd-loaded"
}

@test "a .dist that requires a restart wants one, and one that does not, not" {
    update a.dist restart
    update b.dist none
    run env -i PATH="$STUB:/usr/bin:/bin" G="$G" FNS="$FNS" /bin/sh -c '
        . "$FNS"; CONF_DIR=$G/conf; LOG=$G/log
        update_wants_restart "$G/conf/updates/a.dist" && echo a=yes || echo a=no
        update_wants_restart "$G/conf/updates/b.dist" && echo b=yes || echo b=no'
    [ "$status" -eq 0 ]
    [[ "$output" == *"a=yes"* ]]
    [[ "$output" == *"b=no"* ]]
}

@test "a .pkg whose PackageInfo asks for a restart wants one, and one that does not, not" {
    update a.pkg restart
    update b.pkg none
    run env -i PATH="$STUB:/usr/bin:/bin" G="$G" FNS="$FNS" /bin/sh -c '
        . "$FNS"; CONF_DIR=$G/conf; LOG=$G/log
        update_wants_restart "$G/conf/updates/a.pkg" && echo a=yes || echo a=no
        update_wants_restart "$G/conf/updates/b.pkg" && echo b=yes || echo b=no'
    [ "$status" -eq 0 ]
    [[ "$output" == *"a=yes"* ]]
    [[ "$output" == *"b=no"* ]]
    # It leaves no PackageInfo behind, and xar ran bounded, as every step.
    [ ! -e "$G/conf/.pkginfo" ]
    grep -q "run ([0-9]*s limit): xar -xf $G/conf/updates/a.pkg PackageInfo" "$G/log"
}

@test "a package whose metadata cannot be read is treated as wanting a restart, and says so" {
    echo pkg > "$G/conf/updates/unreadable.pkg"
    run env -i PATH="$STUB:/usr/bin:/bin" G="$G" FNS="$FNS" /bin/sh -c '
        . "$FNS"; CONF_DIR=$G/conf; LOG=$G/log
        update_wants_restart "$G/conf/updates/unreadable.pkg" && echo yes || echo no'
    [[ "$output" == *"yes"* ]]
    grep -q "unreadable.pkg.*restart" "$G/log"
}

@test "10.6.8's combo and 2013-004: the combo, a restart, then 2013-004 and the last restart" {
    update 01-combo.dist restart
    update 02-SecUpd.pkg restart

    boot 01-combo.dist 02-SecUpd.pkg
    [ "$status" -eq 0 ]
    # The combo's actions changed the extensions after its kextcache run:
    # the caches are rebuilt before the restart, so kextd has nothing to
    # rescan under 10.6.0's kernel.
    [ "$(cat "$CALLS")" = "$(printf 'installer 01-combo.dist\nkextcache -system-caches\nkextcache -update-volume /\nshutdown -r now')" ]
    grep -q "kext caches: not current by kextd's test" "$G/guest-macosx-firstboot.log"
    grep -q "kext caches: now up to date" "$G/guest-macosx-firstboot.log"
    # Not finished: the LaunchDaemon stays to run this again, no marker, no
    # Remote Login yet (so no SSH session meets the next restart), and
    # 2013-004 is still to come.
    [ -e "$G/daemon.plist" ]
    [ ! -e "$G/conf/.done" ]
    [ "$(cat "$G/conf/updates.left")" = 02-SecUpd.pkg ]
    grep -q "restarting before the next update (02-SecUpd.pkg).*deferred install" "$G/guest-macosx-firstboot.log"

    restart
    : > "$CALLS"
    boot 01-combo.dist 02-SecUpd.pkg
    [ "$status" -eq 0 ]
    [ "$(sed -n 1p "$CALLS")" = "installer 02-SecUpd.pkg" ]
    # 2013-004 leaves them current: nothing to rebuild, and it says so.
    run grep -q '^kextcache' "$CALLS"
    [ "$status" -ne 0 ]
    grep -q "kext caches: up to date" "$G/guest-macosx-firstboot.log"
    grep -q "defaults write $G/overrides com.openssh.sshd -dict Disabled -bool false" "$CALLS"
    [ "$(tail -1 "$CALLS")" = "shutdown -r now" ]
    [ "$(grep -c '^installer' "$CALLS")" -eq 1 ]
    [ ! -e "$G/daemon.plist" ]
    [ -e "$G/conf/.done" ]
    [ ! -e "$G/conf/updates.left" ]
    [ ! -e "$G/conf/updates" ]
}

@test "an update that asks for a restart but left no deferred install still gets its restart" {
    update 01-a.pkg restart
    update 02-b.pkg none
    boot 01-a.pkg 02-b.pkg
    [ "$(cat "$CALLS")" = "$(printf 'installer 01-a.pkg\nshutdown -r now')" ]
    grep -q "01-a.pkg asks for a restart" "$G/guest-macosx-firstboot.log"
}

@test "a deferred install forces the restart even when the metadata does not ask" {
    STUB_KEXTS_CURRENT=1
    update 01-combo.dist none
    update 02-b.pkg none
    boot 01-combo.dist 02-b.pkg
    [ "$(cat "$CALLS")" = "$(printf 'installer 01-combo.dist\nshutdown -r now')" ]
    grep -q "deferred install" "$G/guest-macosx-firstboot.log"
}

@test "inert: updates that need no restart all go in on one boot, then the one restart, as before" {
    STUB_NO_DEFERRED=1
    update 01-a.pkg none
    update 02-b.pkg none
    update 03-c.dist none
    boot 01-a.pkg 02-b.pkg 03-c.dist
    [ "$status" -eq 0 ]
    [ "$(grep '^installer\|^shutdown' "$CALLS")" = "$(printf 'installer 01-a.pkg\ninstaller 02-b.pkg\ninstaller 03-c.dist\nshutdown -r now')" ]
    [ "$(grep -c 'needs no restart; installing the next one on this boot' "$G/guest-macosx-firstboot.log")" -eq 2 ]
    [ -e "$G/conf/.done" ]
    [ ! -e "$G/daemon.plist" ]
}

@test "one update: installed, then the one restart, with nothing in between" {
    update 01-a.pkg restart
    boot 01-a.pkg
    [ "$(grep '^installer\|^shutdown' "$CALLS")" = "$(printf 'installer 01-a.pkg\nshutdown -r now')" ]
    [ -e "$G/conf/.done" ]
    run grep -q "restarting before the next update" "$G/guest-macosx-firstboot.log"
    [ "$status" -ne 0 ]
}

@test "an update missing from the volume stops its dependent update, without hanging" {
    update 02-b.pkg none
    STUB_NO_DEFERRED=1
    boot 01-gone.pkg 02-b.pkg
    grep -q "updates: STOPPED: NOT ON THE TARGET VOLUME: 01-gone.pkg" "$G/guest-macosx-firstboot.log"
    run grep '^installer' "$CALLS"
    [ "$status" -eq 1 ]
    [ -e "$G/conf/.done" ]
}

@test "the main line resumes the updates before anything else when some are left" {
    run grep -n -e '^if \[ -e "\$UPDATES_LEFT" \]' -e '^set_account_secret$' "$FB"
    [ "$status" -eq 0 ]
    resume=$(echo "$output" | sed -n 1p | cut -d: -f1)
    account=$(echo "$output" | sed -n 2p | cut -d: -f1)
    [ -n "$resume" ] && [ -n "$account" ]
    [ "$resume" -lt "$account" ]
}

@test "inert: kext caches already current get no rebuild before a restart, and the log says so" {
    STUB_KEXTS_CURRENT=1
    update 01-combo.dist restart
    update 02-SecUpd.pkg restart
    boot 01-combo.dist 02-SecUpd.pkg
    [ "$(cat "$CALLS")" = "$(printf 'installer 01-combo.dist\nshutdown -r now')" ]
    grep -q "kext caches: up to date (.*kextd's test); nothing to rebuild before the restart" "$G/guest-macosx-firstboot.log"
}

@test "a missing kext cache counts as one to rebuild" {
    rm -f "$G/kc/$KC_IDS"
    STUB_KEXTS_CURRENT=1
    update 01-a.pkg restart
    boot 01-a.pkg
    grep -q '^kextcache -system-caches' "$CALLS"
    [ "$(tail -1 "$CALLS")" = "shutdown -r now" ]
}

# kextd's own test of its cache, in 10.6.0's source and 10.6.8's alike
# (IOKitUser-502 and -514.16.50, OSKext.c's __OSKextCacheNeedsUpdate): the
# cache is current only when its mtime is the extensions folder's + 1 s.
# Merely newer is not current: kextd rescans.

@test "kextd's test: a cache whose mtime is the extensions folder's + 1 s is current" {
    kexts 202601010200.00 202601010200.01
    kcc
    [ "$status" -eq 0 ]
    [ ! -s "$CALLS" ]
    grep -q "kext caches: up to date" "$G/guest-macosx-firstboot.log"
}

@test "kextd's test: a cache with the extensions folder's own mtime is stale, and rebuilt" {
    kexts 202601010200.00 202601010200.00
    kcc
    [ "$status" -eq 0 ]
    [ "$(cat "$CALLS")" = "$(printf 'kextcache -system-caches\nkextcache -update-volume /')" ]
    grep -q "kext caches: not current by kextd's test" "$G/guest-macosx-firstboot.log"
    grep -q "kext caches: now up to date" "$G/guest-macosx-firstboot.log"
}

@test "kextd's test: a cache 2 s newer than the extensions folder is stale, and rebuilt" {
    kexts 202601010200.00 202601010200.02
    kcc
    [ "$status" -eq 0 ]
    [ "$(cat "$CALLS")" = "$(printf 'kextcache -system-caches\nkextcache -update-volume /')" ]
    grep -q "kext caches: now up to date" "$G/guest-macosx-firstboot.log"
}

@test "kextd's test: a cache older than the extensions folder is stale, and rebuilt" {
    kexts 202601010200.00 202601010100.00
    kcc
    [ "$(sed -n 1p "$CALLS")" = "kextcache -system-caches" ]
}

@test "kextd's test: a cache still not current after the rebuild is logged, and the restart goes ahead" {
    kexts 202601010200.00 202601010200.00
    cat > "$STUB/kextcache" <<'STUB'
#!/bin/sh
echo "kextcache $*" >> "$CALLS"
STUB
    kcc
    [ "$status" -eq 0 ]
    grep -q "kext caches: STILL not current by kextd's test" "$G/guest-macosx-firstboot.log"
}

# The state across the restarts: the list of updates left existing is a
# boot between updates; an update in progress (updates.current) is one the
# guest went down in the middle of.

@test "an update is off the list, and recorded as in progress, while it installs; not after" {
    update 01-combo.dist restart
    update 02-SecUpd.pkg restart
    boot 01-combo.dist 02-SecUpd.pkg
    [ "$status" -eq 0 ]
    [ "$(cat "$G/during")" = "left=02-SecUpd.pkg current=01-combo.dist kextd=off" ]
    [ ! -e "$G/conf/updates.current" ]
    restart
    boot 01-combo.dist 02-SecUpd.pkg
    [ "$(sed -n 2p "$G/during")" = "left= current=02-SecUpd.pkg kextd=loaded" ]
    [ ! -e "$G/conf/updates.current" ]
}

@test "an empty list of updates left resumes into the finish, and installs nothing again" {
    # The last update installed, then the guest went down before its finish.
    update 01-combo.dist restart
    update 02-SecUpd.pkg restart
    : > "$G/conf/updates.left"
    boot 01-combo.dist 02-SecUpd.pkg
    [ "$status" -eq 0 ]
    run grep -c '^installer' "$CALLS"
    [ "$output" = 0 ]
    [ -e "$G/conf/.done" ]
    [ ! -e "$G/daemon.plist" ]
    [ ! -e "$G/conf/updates.left" ]
    [ "$(tail -1 "$CALLS")" = "shutdown -r now" ]
}

@test "an update the guest went down in the middle of stops the updates, and the build fails on its receipt, not on the next update" {
    update 01-combo.dist restart
    update 02-SecUpd.pkg restart
    echo 02-SecUpd.pkg > "$G/conf/updates.left"
    echo 01-combo.dist > "$G/conf/updates.current"
    boot 01-combo.dist 02-SecUpd.pkg
    [ "$status" -eq 0 ]
    run grep -c '^installer' "$CALLS"
    [ "$output" = 0 ]
    grep -q "updates: STOPPED: 01-combo.dist was being installed when this guest last went down" "$G/guest-macosx-firstboot.log"
    grep -q "installing none of the rest (02-SecUpd.pkg)" "$G/guest-macosx-firstboot.log"
    [ -e "$G/conf/.done" ]
    [ ! -e "$G/daemon.plist" ]
    [ ! -e "$G/conf/updates.left" ]
    [ ! -e "$G/conf/updates.current" ]
    [ "$(tail -1 "$CALLS")" = "shutdown -r now" ]
}

@test "a first list that cannot be written installs nothing, and says so" {
    update 01-a.pkg none
    mkdir -p "$G/conf/updates.left.new"
    boot 01-a.pkg
    [ "$status" -eq 0 ]
    run grep -c '^installer' "$CALLS"
    [ "$output" = 0 ]
    grep -q "updates: cannot write $G/conf/updates.left; installing none" "$G/guest-macosx-firstboot.log"
    [ -e "$G/conf/.done" ]
}

# The resume block itself, as firstboot.sh has it, ahead of its main line.
resume() {
    block=$(awk '/^if \[ -e "\$UPDATES_LEFT" \]; then$/,/^fi$/' "$FB")
    [ -n "$block" ]
    PKGS="$*" guest "$block
        echo 'the main line ran'"
}

@test "the resume block, run as firstboot.sh has it: some updates left, it installs them and never reaches the main line" {
    update 01-combo.dist restart
    update 02-SecUpd.pkg restart
    echo 02-SecUpd.pkg > "$G/conf/updates.left"
    resume 01-combo.dist 02-SecUpd.pkg
    [ "$status" -eq 0 ]
    [[ "$output" != *"the main line ran"* ]]
    [ "$(grep '^installer' "$CALLS")" = "installer 02-SecUpd.pkg" ]
    grep -q "resuming the updates after a restart; left: 02-SecUpd.pkg" "$G/guest-macosx-firstboot.log"
    [ -e "$G/conf/.done" ]
}

@test "the resume block, run as firstboot.sh has it: an empty list left still resumes, into the finish" {
    : > "$G/conf/updates.left"
    resume 01-combo.dist 02-SecUpd.pkg
    [ "$status" -eq 0 ]
    [[ "$output" != *"the main line ran"* ]]
    [ ! -s "$CALLS" ] || ! grep -q '^installer' "$CALLS" || false
    [ -e "$G/conf/.done" ]
}

@test "the resume block, run as firstboot.sh has it: no list, a first boot, it falls through to the main line" {
    resume 01-combo.dist 02-SecUpd.pkg
    [ "$status" -eq 0 ]
    [[ "$output" == *"the main line ran"* ]]
    run grep -c . "$CALLS"
    [ "$output" = 0 ]
}

@test "a list of updates that cannot be rewritten stops the updates, rather than looping on its first" {
    STUB_NO_DEFERRED=1
    update 01-a.pkg none
    update 02-b.pkg none
    printf '01-a.pkg\n02-b.pkg\n' > "$G/conf/updates.left"
    # Where the rewritten list would go: a directory, so the rewrite fails,
    # as on a full or read-only disk, and as root too.
    mkdir -p "$G/conf/updates.left.new"
    boot 01-a.pkg 02-b.pkg
    [ "$status" -eq 0 ]
    [ ! -e "$G/timedout" ]
    run grep -c '^installer' "$CALLS"
    [ "$output" = 0 ]
    grep -q "updates: cannot update $G/conf/updates.left.*stopping the updates" "$G/guest-macosx-firstboot.log"
    [ -e "$G/conf/.done" ]
    [ "$(tail -1 "$CALLS")" = "shutdown -r now" ]
}

@test "a restart between updates that does not happen is logged, not silent" {
    STUB_SHUTDOWN_FAIL=1
    update 01-a.pkg restart
    update 02-b.pkg none
    boot 01-a.pkg 02-b.pkg
    [ "$status" -eq 0 ]
    grep -q "restart: shutdown -r now failed (rc=1)" "$G/guest-macosx-firstboot.log"
    grep -q "restart: shutdown -r now failed (rc=1)" "$G/root-guest-macosx-firstboot.log"
}

@test "the last restart that does not happen is logged, not silent" {
    STUB_SHUTDOWN_FAIL=1
    STUB_NO_DEFERRED=1
    update 01-a.pkg none
    boot 01-a.pkg
    [ "$status" -eq 0 ]
    grep -q "restart: shutdown -r now failed (rc=1)" "$G/guest-macosx-firstboot.log"
    grep -q "restart: shutdown -r now failed (rc=1)" "$G/root-guest-macosx-firstboot.log"
}

# The two orderings the state rests on.

@test "the update in progress is recorded before it leaves the list: a record that cannot be written leaves it at the head" {
    STUB_NO_DEFERRED=1
    update 01-a.pkg none
    update 02-b.pkg none
    printf '01-a.pkg\n02-b.pkg\n' > "$G/conf/updates.left"
    # updates.current a dangling symlink into a directory that does not
    # exist: absent to -e, so this is no resume of a cut-short update, and
    # unwritable, as root too.
    ln -s "$G/no-such-dir/updates.current" "$G/conf/updates.current"
    PKGS="01-a.pkg 02-b.pkg" guest 'snow_leopard_updates; echo returned'
    [ "$status" -eq 0 ]
    [[ "$output" == *returned* ]]
    run grep -c '^installer' "$CALLS"
    [ "$output" = 0 ]
    [ "$(sed -n 1p "$G/conf/updates.left")" = 01-a.pkg ]
    grep -q "updates: cannot update .*stopping the updates, 01-a.pkg and the rest not installed" "$G/guest-macosx-firstboot.log"
}

@test "the finish removes the updates' state only once its marker is written" {
    STUB_NO_DEFERRED=1
    update 01-a.pkg none
    printf '' > "$G/conf/updates.left"
    # The marker's path a directory: its write fails, as root too.
    mkdir -p "$G/conf/.done"
    boot 01-a.pkg
    [ "$status" -eq 0 ]
    [ -e "$G/conf/updates.left" ]
    grep -q "cannot write the marker $G/conf/.done; keeping the updates' state" "$G/guest-macosx-firstboot.log"
}

# Every cache kextd's rescan decision reads, each by its folder + 1 rule:
# one stale is a rescan at the restart.

# one_stale <file>: <file> alone stale (the folder's own mtime), the other
# two current: the rebuild runs, and the log names <file> as the stale one.
one_stale() {
    kexts 202601010200.00 202601010200.00 "$1"
    kcc
    [ "$status" -eq 0 ]
    [ "$(cat "$CALLS")" = "$(printf 'kextcache -system-caches\nkextcache -update-volume /')" ]
    grep -q "kext caches: not current by kextd's test.*: $1 (mtime [0-9]*); changed" "$G/guest-macosx-firstboot.log"
    grep -q "kext caches: now up to date" "$G/guest-macosx-firstboot.log"
}

@test "kextd's test, cache by cache: the identifiers cache alone stale triggers the rebuild, and the log names it" {
    one_stale "$KC_IDS"
}

@test "kextd's test, cache by cache: the personalities cache alone stale triggers the rebuild, and the log names it" {
    one_stale "$KC_PERS"
}

@test "kextd's test, cache by cache: the OSBundleHelper values cache alone stale triggers the rebuild, and the log names it" {
    one_stale "$KC_PROPS"
}

@test "kextd's test, cache by cache: a missing personalities cache for the running kernel's arch triggers the rebuild" {
    kexts 202601010200.00 202601010200.01
    rm -f "$G/kc/$KC_PERS"
    kcc
    [ "$(sed -n 1p "$CALLS")" = "kextcache -system-caches" ]
    grep -q "kext caches: not current by kextd's test.*: $KC_PERS (missing)" "$G/guest-macosx-firstboot.log"
}

@test "inert, cache by cache: all three current, nothing rebuilt, and the log names all three" {
    kexts 202601010200.00 202601010200.01
    kcc
    [ "$status" -eq 0 ]
    [ ! -s "$CALLS" ]
    grep -q "kext caches: up to date ($KC_IDS, $KC_PERS, $KC_PROPS each at" "$G/guest-macosx-firstboot.log"
}

@test "kextd's test, cache by cache: a rebuild that leaves one stale says which" {
    kexts 202601010200.00 202601010200.00
    STUB_KC_SKIP=$KC_PROPS
    export STUB_KC_SKIP
    run env -i PATH="$STUB:/usr/bin:/bin" CALLS="$CALLS" G="$G" FNS="$FNS" STUB_KC_SKIP="$STUB_KC_SKIP" /bin/sh -c '
        . "$FNS"; CONF_DIR=$G/conf; LOG=$G/guest-macosx-firstboot.log
        KEXT_DIR=$G/Extensions; KEXT_CACHE_DIR=$G/kc
        kext_caches_current'
    [ "$status" -eq 0 ]
    grep -q "kext caches: STILL not current by kextd's test.*: $KC_PROPS (mtime [0-9]*); restarting anyway" "$G/guest-macosx-firstboot.log"
    run grep "STILL" "$G/guest-macosx-firstboot.log"
    [[ "$output" != *"$KC_IDS"* ]]
    [[ "$output" != *"$KC_PERS"* ]]
}

@test "10.6.8's BootCacheControl is set aside as soon as the combo puts it there, before the combo's restart (docs/decisions/0025)" {
    update 01-combo.dist restart
    update 02-SecUpd.pkg restart
    STUB_KEXTS_CURRENT=1
    boot 01-combo.dist 02-SecUpd.pkg
    [ "$status" -eq 0 ]
    # The boot that installs 2013-004 must not start BootCache 31.1.
    [ ! -e "$G/sbin/BootCacheControl" ]
    [ -e "$G/sbin/BootCacheControl-disabled" ]
    aside=$(grep -n "BootCacheControl set aside to $G/sbin/BootCacheControl-disabled" "$G/guest-macosx-firstboot.log" | cut -d: -f1)
    # On the combo's boot, in the steps before its restart.
    next=$(grep -n "restarting before the next update" "$G/guest-macosx-firstboot.log" | cut -d: -f1)
    [ -n "$aside" ] && [ -n "$next" ] && [ "$aside" -gt "$next" ]
    [ "$(tail -1 "$CALLS")" = "shutdown -r now" ]
    # The finish, after 2013-004, finds it already aside and keeps it so.
    restart
    boot 01-combo.dist 02-SecUpd.pkg
    [ "$status" -eq 0 ]
    [ ! -e "$G/sbin/BootCacheControl" ]
    [ -e "$G/sbin/BootCacheControl-disabled" ]
    grep -q "BootCache: $G/sbin/BootCacheControl already set aside" "$G/guest-macosx-firstboot.log"
    [ "$(tail -1 "$CALLS")" = "shutdown -r now" ]
}

@test "inert: 10.6.0's BootCache (31) is left in place between updates that leave it, and the log names it" {
    # An update needing a restart that installs no BootCache: 10.6.0's 31 stays.
    update 01-a.pkg restart
    update 02-b.pkg restart
    STUB_NO_DEFERRED=1
    boot 01-a.pkg 02-b.pkg
    [ "$status" -eq 0 ]
    [ -e "$G/sbin/BootCacheControl" ]
    [ ! -e "$G/sbin/BootCacheControl-disabled" ]
    grep -q "BootCache: BootCache.kext is 31, not one measured to deadlock (31.1); BootCacheControl left in place" "$G/guest-macosx-firstboot.log"
}

@test "the finish sets 10.6.8's BootCacheControl aside, after the last update and before its restart, when no restart came between (docs/decisions/0025)" {
    update 01-combo.dist none
    STUB_NO_DEFERRED=1
    STUB_KEXTS_CURRENT=1
    boot 01-combo.dist
    [ "$status" -eq 0 ]
    [ ! -e "$G/sbin/BootCacheControl" ]
    [ -e "$G/sbin/BootCacheControl-disabled" ]
    aside=$(grep -n "BootCacheControl set aside to $G/sbin/BootCacheControl-disabled" "$G/guest-macosx-firstboot.log" | cut -d: -f1)
    last=$(grep -n "restarting into the updated system" "$G/guest-macosx-firstboot.log" | cut -d: -f1)
    [ -n "$aside" ] && [ -n "$last" ] && [ "$aside" -lt "$last" ]
    [ "$(tail -1 "$CALLS")" = "shutdown -r now" ]
}

# --- kextd, held off while an update that brings a kernel goes in ---------
#
# 10.6.0's kextd, 300 s after it starts and on every change to
# /System/Library/Extensions after that, rescans when its kext caches are
# out of date and sends every kext's personalities to the running kernel.
# During the combo those are 10.6.8's kexts under 10.6.0's kernel, which
# loads one and panics (F24, docs/decisions/0026). So kextd's job is
# unloaded before an update that installs a kernel, and stays unloaded
# until the restart, when launchd starts it with that kernel.

@test "the combo, which installs a kernel, goes in with kextd held off, and kextd stays off until its restart" {
    update 01-combo.dist restart
    update 02-SecUpd.pkg restart

    boot 01-combo.dist 02-SecUpd.pkg
    [ "$status" -eq 0 ]
    [ "$(cat "$G/launchctl")" = "$(printf 'launchctl unload %s\nlaunchctl list com.apple.kextd' "$G/com.apple.kextd.plist")" ]
    [ "$(cat "$G/during")" = "left=02-SecUpd.pkg current=01-combo.dist kextd=off" ]
    [ ! -e "$G/kextd-loaded" ]
    # Still off when the restart is asked for: nothing brought it back.
    [ "$(cat "$G/at-shutdown")" = kextd=off ]
    grep -q "kextd: held off until the restart: 01-combo.dist installs a kernel (01-combo.dist.b.pkg)" "$G/guest-macosx-firstboot.log"
    # Its caches are still made current before the restart, without kextd.
    [ "$(cat "$CALLS")" = "$(printf 'installer 01-combo.dist\nkextcache -system-caches\nkextcache -update-volume /\nshutdown -r now')" ]

    # 2013-004 installs no kernel: on its boot kextd runs, as before.
    restart
    : > "$G/launchctl"
    : > "$G/during"
    boot 01-combo.dist 02-SecUpd.pkg
    [ "$status" -eq 0 ]
    [ "$(cat "$G/during")" = "left= current=02-SecUpd.pkg kextd=loaded" ]
    [ ! -s "$G/launchctl" ]
    [ "$(sed -n 2p "$G/at-shutdown")" = kextd=loaded ]
    grep -q "kextd: 02-SecUpd.pkg installs no kernel; kextd left running" "$G/guest-macosx-firstboot.log"
}

@test "inert: updates that install no kernel never touch kextd, and the log says so for each" {
    STUB_NO_DEFERRED=1
    update 01-a.pkg none
    update 02-b.dist none

    boot 01-a.pkg 02-b.dist
    [ "$status" -eq 0 ]
    [ ! -e "$G/launchctl" ]
    [ -e "$G/kextd-loaded" ]
    [ "$(grep -c "kextd=loaded" "$G/during")" -eq 2 ]
    grep -q "kextd: 01-a.pkg installs no kernel; kextd left running" "$G/guest-macosx-firstboot.log"
    grep -q "kextd: 02-b.dist installs no kernel; kextd left running" "$G/guest-macosx-firstboot.log"
    # One boot for both, then the one restart, as before.
    [ "$(grep '^installer\|^shutdown' "$CALLS")" = "$(printf 'installer 01-a.pkg\ninstaller 02-b.dist\nshutdown -r now')" ]
}

@test "a flat package that installs a kernel is held off too" {
    update 01-combo.pkg restart
    boot 01-combo.pkg
    [ "$status" -eq 0 ]
    [ "$(cat "$G/during")" = "left= current=01-combo.pkg kextd=off" ]
    [ "$(cat "$G/at-shutdown")" = kextd=off ]
    grep -q "kextd: held off until the restart: 01-combo.pkg installs a kernel (01-combo.pkg)" "$G/guest-macosx-firstboot.log"
}

@test "an update that installs a kernel gets its restart before the next update even when nothing else asks for one" {
    STUB_NO_DEFERRED=1
    update 01-combo.dist none
    update 02-b.pkg none

    boot 01-combo.dist 02-b.pkg
    [ "$status" -eq 0 ]
    [ "$(cat "$CALLS")" = "$(printf 'installer 01-combo.dist\nkextcache -system-caches\nkextcache -update-volume /\nshutdown -r now')" ]
    grep -q "updates: restarting before the next update (02-b.pkg): 01-combo.dist installed a kernel, and kextd stays off until that kernel runs" "$G/guest-macosx-firstboot.log"
    [ ! -e "$G/kextd-loaded" ]
    [ "$(cat "$G/at-shutdown")" = kextd=off ]
}

@test "a Bom that cannot be read counts as a kernel: kextd is held off, and the log says why" {
    update 01-x.dist restart
    rm "$G/conf/updates/01-x.dist.b.pkg.Bom"
    boot 01-x.dist
    [ "$status" -eq 0 ]
    [ "$(cat "$G/during")" = "left= current=01-x.dist kextd=off" ]
    grep -q "kextd: cannot read the Bom of 01-x.dist.b.pkg; taking .*01-x.dist to install a kernel" "$G/guest-macosx-firstboot.log"
    [ ! -e "$G/conf/.bom" ]
}

@test "a .dist that names no package counts as a kernel, and the log says why" {
    echo '<pkg-ref id="x" onConclusion="RequireRestart"/>' > "$G/conf/updates/01-y.dist"
    boot 01-y.dist
    [ "$status" -eq 0 ]
    [ "$(cat "$G/during")" = "left= current=01-y.dist kextd=off" ]
    grep -q "kextd: cannot read the packages of .*01-y.dist; taking it to install a kernel" "$G/guest-macosx-firstboot.log"
}

@test "kextd that will not unload is logged, and the update goes in all the same" {
    STUB_LAUNCHCTL_FAIL=1
    update 01-combo.dist restart
    boot 01-combo.dist
    [ "$status" -eq 0 ]
    [ "$(cat "$G/during")" = "left= current=01-combo.dist kextd=loaded" ]
    grep -q "kextd: COULD NOT unload com.apple.kextd; 01-combo.dist installs a kernel (01-combo.dist.b.pkg)" "$G/guest-macosx-firstboot.log"
    grep -q "^installer 01-combo.dist$" "$CALLS"
}

@test "a Bom that lsbom cannot list counts as a kernel: kextd is held off, and the log says why" {
    STUB_LSBOM_FAIL=1
    update 01-combo.dist restart
    boot 01-combo.dist
    [ "$status" -eq 0 ]
    [ "$(cat "$G/during")" = "left= current=01-combo.dist kextd=off" ]
    [ "$(cat "$G/at-shutdown")" = kextd=off ]
    grep -q "kextd: cannot list the Bom of 01-combo.dist.a.pkg; taking .*01-combo.dist to install a kernel" "$G/guest-macosx-firstboot.log"
    ! grep -q "installs no kernel" "$G/guest-macosx-firstboot.log" || false
    [ ! -e "$G/conf/.bom" ]
}

@test "a Bom lsbom cannot list counts as a kernel even for an update with none, the safe side" {
    STUB_LSBOM_FAIL=1
    update 01-a.pkg restart
    boot 01-a.pkg
    [ "$status" -eq 0 ]
    [ "$(cat "$G/during")" = "left= current=01-a.pkg kextd=off" ]
    grep -q "kextd: cannot list the Bom of 01-a.pkg; taking .*01-a.pkg to install a kernel" "$G/guest-macosx-firstboot.log"
}

# --- 10.9: an update that installs a kernel, then a restart (F29) ----------
#
# 10.9.5's Security Update 2016-004 installs a kernel and asks for a
# restart. Taken on the boot that installed it, the build's provisioners and
# its last shutdown ran under the old kernel, which asks kextd, once per
# boot, for a prelinked kernel; kextd's kextcache then ran again after the
# installer's own, and the build's shutdown waited for it on kextd's reboot
# lock ("'/' updating, delaying reboot"; docs/decisions/0036). So on 10.9 an
# update that installs a kernel goes in with kextd held off, as on 10.6, and
# one that installs a kernel or asks for a restart ends the first boot in a
# restart, through the shutdown helper, with Remote Login on only from the
# boot after it.

# mav <updates...>: firstboot's 10.9 steps, as its main line runs them: the
# updates (first), Remote Login, and the end of the first boot.
mav() {
    [ -e "$G/kernelcache" ] || mav_kernelcache 202601010000.00 202601010000.00 201601110000.00 202601010000.01
    PKGS="$*" guest '
        GUEST_MACOSX_FB_RESTART=0
        install_updates
        remote_login_on
        restart_into_updates
        echo "fell through" >&2
        exit 3'
}

@test "10.9: an update that installs a kernel goes in with kextd held off, and the first boot ends in a restart into it, Remote Login on only from then" {
    update 01-combo.pkg restart
    mav 01-combo.pkg
    [ "$status" -eq 0 ]
    [ "$(cat "$G/during")" = "left= current= kextd=off" ]
    [ "$(cat "$G/at-shutdown")" = kextd=off ]
    [ "$(grep -v '^kextcache' "$CALLS")" = "$(printf 'installer 01-combo.pkg\ndefaults write %s com.openssh.sshd -dict Disabled -bool false\nguest-macosx-shutdown -r' "$G/overrides")" ]
    grep -q "kextd: held off until the restart: 01-combo.pkg installs a kernel (01-combo.pkg)" "$G/guest-macosx-firstboot.log"
    grep -q "remote login: on from the next boot, after the restart into the updates" "$G/guest-macosx-firstboot.log"
    grep -q "restarting into the updated system: 01-combo.pkg installed a kernel, and kextd stays off until that kernel runs" "$G/guest-macosx-firstboot.log"
}

# mav_kernelcache <S/L/E stamp> <L/E stamp> <kernel stamp> <prelinked kernel stamp>: 10.9's
# extensions folders, kernel and prelinked kernel, as touch -t takes their mtimes.
mav_kernelcache() {
    mkdir -p "$G/Extensions" "$G/LExtensions"
    touch -t "$1" "$G/Extensions"
    touch -t "$2" "$G/LExtensions"
    touch -t "$3" "$G/mach_kernel"
    touch -t "$4" "$G/kernelcache"
}

@test "10.9: a prelinked kernel left stale is rebuilt before that restart, without kextd, and the log says so" {
    update 01-combo.pkg restart
    # The combo's installer stub touches the S/L/E folder after the prelinked kernel was made.
    mav 01-combo.pkg
    [ "$status" -eq 0 ]
    [ "$(grep '^kextcache' "$CALLS")" = "kextcache -update-volume /" ]
    run grep -n . "$CALLS"
    [ "$(echo "$output" | grep 'kextcache -update-volume' | cut -d: -f1)" -lt "$(echo "$output" | grep 'guest-macosx-shutdown' | cut -d: -f1)" ]
    grep -q "prelinked kernel: not current by kextd's test" "$G/guest-macosx-firstboot.log"
    grep -q "prelinked kernel: now up to date" "$G/guest-macosx-firstboot.log"
}

@test "10.9, inert: a prelinked kernel current by kextd's test gets no rebuild before that restart, and the log says so" {
    STUB_KEXTS_CURRENT=1
    update 01-combo.pkg restart
    mav 01-combo.pkg
    [ "$status" -eq 0 ]
    run grep -c '^kextcache' "$CALLS"
    [ "$output" = 0 ]
    grep -q "prelinked kernel: up to date ($G/kernelcache at " "$G/guest-macosx-firstboot.log"
}

@test "kextd's 10.9 test: the prelinked kernel is current only at the newest extensions folder's mtime + 1 s" {
    STUB_KEXTS_CURRENT=1
    update 01-combo.pkg restart
    # /Library/Extensions newer than /System/Library/Extensions: the cache at S/L/E + 1 is stale.
    mav_kernelcache 202601010000.00 202601010000.05 201601110000.00 202601010000.01
    mav 01-combo.pkg
    [ "$status" -eq 0 ]
    [ "$(grep '^kextcache' "$CALLS")" = "kextcache -update-volume /" ]
}

@test "kextd's 10.9 test: a kernel newer than the extensions folders sets the time, + 1 s" {
    STUB_KEXTS_CURRENT=1
    update 01-combo.pkg restart
    mav_kernelcache 202601010000.00 202601010000.00 202601010000.10 202601010000.11
    mav 01-combo.pkg
    [ "$status" -eq 0 ]
    run grep -c '^kextcache' "$CALLS"
    [ "$output" = 0 ]
    : > "$CALLS"; rm -f "$G/at-shutdown"; : > "$G/kextd-loaded"; mkdir -p "$G/conf/updates"
    mav_kernelcache 202601010000.00 202601010000.00 202601010000.10 202601010000.01
    update 01-combo.pkg restart
    mav 01-combo.pkg
    [ "$status" -eq 0 ]
    [ "$(grep '^kextcache' "$CALLS")" = "kextcache -update-volume /" ]
}

@test "10.9: a missing prelinked kernel counts as one to rebuild" {
    STUB_KEXTS_CURRENT=1
    update 01-combo.pkg restart
    mav_kernelcache 202601010000.00 202601010000.00 201601110000.00 202601010000.01
    rm "$G/kernelcache"
    : > "$G/kernelcache.gone"
    PKGS=01-combo.pkg guest '
        GUEST_MACOSX_FB_RESTART=0
        install_updates
        remote_login_on
        restart_into_updates'
    [ "$status" -eq 0 ]
    [ "$(grep '^kextcache' "$CALLS")" = "kextcache -update-volume /" ]
    grep -q "prelinked kernel: not current by kextd's test, which wants $G/kernelcache at .* (it is missing)" "$G/guest-macosx-firstboot.log"
}

@test "10.9: a prelinked kernel still stale after its rebuild is logged, and the restart goes ahead" {
    STUB_KC_SKIP=kernelcache
    update 01-combo.pkg restart
    mav 01-combo.pkg
    [ "$status" -eq 0 ]
    grep -q "prelinked kernel: STILL not current by kextd's test" "$G/guest-macosx-firstboot.log"
    grep -q "^guest-macosx-shutdown -r$" "$CALLS"
}

@test "10.9, inert: no updates, kextd untouched, Remote Login on now, and no restart" {
    mav
    [ "$status" -eq 3 ]
    run grep -c kextd "$G/launchctl"
    [ "$output" = 0 ]
    [ -e "$G/kextd-loaded" ]
    [ "$(cat "$CALLS")" = "$(printf 'systemsetup -f -setremotelogin on\nlaunchctl load -w %s\nsystemsetup -getremotelogin' "$G/ssh.plist")" ]
    run grep -c "restarting into the updated system" "$G/guest-macosx-firstboot.log"
    [ "$output" = 0 ]
}

@test "10.9, inert: an update that installs no kernel and asks for no restart leaves kextd alone, Remote Login on now, and no restart" {
    update 01-a.pkg none
    mav 01-a.pkg
    [ "$status" -eq 3 ]
    run grep -c kextd "$G/launchctl"
    [ "$output" = 0 ]
    [ "$(cat "$G/during")" = "left= current= kextd=loaded" ]
    grep -q "kextd: 01-a.pkg installs no kernel; kextd left running" "$G/guest-macosx-firstboot.log"
    [ "$(grep -c '^guest-macosx-shutdown' "$CALLS")" -eq 0 ]
    grep -q "^launchctl load -w $G/ssh.plist$" "$CALLS"
}

@test "10.9: an update that asks for a restart but installs no kernel leaves kextd running, and still gets its restart" {
    update 01-a.pkg restart
    mav 01-a.pkg
    [ "$status" -eq 0 ]
    [ "$(cat "$G/during")" = "left= current= kextd=loaded" ]
    [ "$(cat "$G/at-shutdown")" = kextd=loaded ]
    grep -q "restarting into the updated system: 01-a.pkg asks for a restart" "$G/guest-macosx-firstboot.log"
    run grep -c "^launchctl load -w" "$CALLS"
    [ "$output" = 0 ]
}

@test "10.9: a restart into the updates that does not happen is logged, not silent" {
    STUB_SHUTDOWN_FAIL=1
    update 01-combo.pkg restart
    mav 01-combo.pkg
    [ "$status" -eq 0 ]
    grep -q "restart: $G/guest-macosx-shutdown -r failed (rc=1); nothing else restarts this guest" "$G/guest-macosx-firstboot.log"
}

@test "10.9: an update missing from the volume asks for no restart" {
    mav 01-gone.pkg
    [ "$status" -eq 3 ]
    grep -q "updates: NOT ON THE TARGET VOLUME: 01-gone.pkg" "$G/guest-macosx-firstboot.log"
    run grep -c kextd "$G/launchctl"
    [ "$output" = 0 ]
}

@test "kextd's 10.9 test: a prelinked kernel newer than its folders + 1 s is stale too, and rebuilt" {
    STUB_KEXTS_CURRENT=1
    update 01-combo.pkg restart
    mav_kernelcache 202601010000.00 202601010000.00 201601110000.00 202601010000.03
    mav 01-combo.pkg
    [ "$status" -eq 0 ]
    [ "$(grep '^kextcache' "$CALLS")" = "kextcache -update-volume /" ]
}
