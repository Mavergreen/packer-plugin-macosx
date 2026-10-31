#!/usr/bin/env bats
# platform: host-agnostic
#
# firstboot.sh's ByHost settings (docs/decisions/0024). A ByHost preference
# lives in a file named for the host: ~/Library/Preferences/ByHost/
# <domain>.<host>.plist, where <host> is the host UUID, or, for a UUID of
# the guest's own making (00000000-0000-1000-8000-<MAC>), that MAC in lower
# case. The build sets the user's screensaver idleTime that way, under the
# build VM's own host UUID, and every machine has its own UUID: so in a box
# the build's file is not this host's, and the setting is not there.
#
# So firstboot records each ByHost setting it makes, and installs
# guest-macosx-byhost, run at every boot by a LaunchDaemon: it waits for the
# host UUID, logs where it came from, and for each recorded setting this
# host's file lacks, adds it; a setting this host's file already has, from
# the build or from the user, it leaves alone, and says so.
#
# SAFETY: PATH=stubs:/usr/bin:/bin (no sbin); shutdown, reboot, halt, sudo,
# su, chown and logger are stubs; PlistBuddy is a stand-in that keeps
# "key<TAB>type<TAB>value" lines; every path is under the test's own root.

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    FB="$REPO/assets/guest/firstboot.sh"
    STUB="$BATS_TEST_TMPDIR/stub"
    ROOT="$BATS_TEST_TMPDIR/root"
    mkdir -p "$STUB" "$ROOT/Library/Preferences/guest-macosx" "$ROOT/Users/vagrant/Library/Preferences/ByHost"
    HELPER="$BATS_TEST_TMPDIR/guest-macosx-byhost"
    awk '/<<.GUEST_MACOSX_BYHOST_EOF.$/ { on = 1; next } /^GUEST_MACOSX_BYHOST_EOF$/ { on = 0 } on' "$FB" > "$HELPER"
    [ -s "$HELPER" ] || { echo "firstboot.sh writes no guest-macosx-byhost" >&2; return 1; }
    for c in shutdown reboot halt sudo; do
        cat > "$STUB/$c" <<'STUB'
#!/bin/sh
echo "stub: ${0##*/} refused"
exit 1
STUB
    done
    export CALLS="$BATS_TEST_TMPDIR/calls"
    : > "$CALLS"
    cat > "$STUB/logger" <<'EOF'
#!/bin/sh
printf 'logger %s\n' "$*" >> "$CALLS"
EOF
    cat > "$STUB/chown" <<'EOF'
#!/bin/sh
printf 'chown %s\n' "$*" >> "$CALLS"
EOF
    cat > "$STUB/su" <<'EOF'
#!/bin/sh
printf 'su %s\n' "$*" >> "$CALLS"
EOF
    # ioreg: IOPlatformUUID once STUB_UUID_AFTER calls have answered without it
    cat > "$STUB/ioreg" <<'EOF'
#!/bin/sh
case "$*" in
*IOPlatformExpertDevice*)
    n=$(cat "$CALLS.ioreg" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" > "$CALLS.ioreg"
    [ "$n" -gt "${STUB_UUID_AFTER:-0}" ] || exit 0
    [ -z "${STUB_PLATFORM_UUID:-}" ] || echo "      \"IOPlatformUUID\" = \"$STUB_PLATFORM_UUID\"" ;;
*"-n platform"*) [ -z "${STUB_SYSTEM_ID:-}" ] || echo '      "system-id" = <52d8b023c5af480d8fbf822b915f5331>' ;;
esac
exit 0
EOF
    cat > "$STUB/PlistBuddy" <<'EOF'
#!/bin/sh
# PlistBuddy -c "Print :key" file | PlistBuddy -c "Add :key type value" file
printf 'PlistBuddy %s\n' "$*" >> "$CALLS"
cmd=$2; file=$3
case $cmd in
"Print :"*) k=${cmd#Print :}; [ -f "$file" ] && grep -q "^$k	" "$file" ;;
"Add :"*) printf '%s\n' "${cmd#Add :}" | { read -r k ty v; printf '%s\t%s\t%s\n' "$k" "$ty" "$v" >> "$file"; } ;;
*) exit 1 ;;
esac
EOF
    cat > "$STUB/sleep" <<'STUB'
#!/bin/sh
exit 0
STUB
    # ps: no session of anyone's, unless a test says otherwise
    cat > "$STUB/ps" <<'EOF'
#!/bin/sh
printf 'root /sbin/launchd\n'
EOF
    chmod +x "$STUB"/*
    export PATH="$STUB:/usr/bin:/bin"
    export GUEST_MACOSX_BYHOST_ROOT="$ROOT" GUEST_MACOSX_BYHOST_PLISTBUDDY="$STUB/PlistBuddy"
    export STUB_PLATFORM_UUID=F7B2200F-5289-5585-B893-5A6896A8FBA7 STUB_SYSTEM_ID=1
    TAB=$(printf '\t')
    printf 'vagrant\tcom.apple.screensaver\tidleTime\tstring\t0\n' > "$ROOT/Library/Preferences/guest-macosx/byhost.tsv"
    BH="$ROOT/Users/vagrant/Library/Preferences/ByHost"
}


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

@test "a host with no file of its own gets the build's setting, its file the user's, and the log says so" {
    run sh "$HELPER"
    [ "$status" -eq 0 ]
    f="$BH/com.apple.screensaver.F7B2200F-5289-5585-B893-5A6896A8FBA7.plist"
    grep -qxF "idleTime${TAB}string${TAB}0" "$f"
    grep -qxF "chown -h vagrant:staff $f" "$CALLS"
    [[ "$output" == *"host UUID F7B2200F-5289-5585-B893-5A6896A8FBA7, from the firmware's system-id"* ]]
    [[ "$output" == *"vagrant's com.apple.screensaver idleTime: set to 0 for host F7B2200F-5289-5585-B893-5A6896A8FBA7"* ]]
    grep -q '^logger -t guest-macosx-byhost' "$CALLS"
}

@test "a setting this host's file already has, the build's or the user's, is left alone" {
    f="$BH/com.apple.screensaver.F7B2200F-5289-5585-B893-5A6896A8FBA7.plist"
    printf 'idleTime\tinteger\t300\n' > "$f"
    run sh "$HELPER"
    [ "$status" -eq 0 ]
    [ "$(cat "$f")" = "idleTime${TAB}integer${TAB}300" ]
    ! grep -q '^PlistBuddy -c Add' "$CALLS" || false
    ! grep -q '^chown' "$CALLS" || false
    [[ "$output" == *"vagrant's com.apple.screensaver idleTime: already set for host F7B2200F-5289-5585-B893-5A6896A8FBA7"* ]]
}

@test "this host's file without the setting gets it added, its other keys kept" {
    f="$BH/com.apple.screensaver.F7B2200F-5289-5585-B893-5A6896A8FBA7.plist"
    printf 'moduleDict\tdict\tx\n' > "$f"
    run sh "$HELPER"
    [ "$status" -eq 0 ]
    grep -qxF "moduleDict${TAB}dict${TAB}x" "$f"
    grep -qxF "idleTime${TAB}string${TAB}0" "$f"
}

@test "a UUID the guest made from its MAC is named, as CoreFoundation names it, by that MAC in lower case" {
    STUB_SYSTEM_ID= STUB_PLATFORM_UUID=00000000-0000-1000-8000-525400597FA2 STUB_UUID_AFTER=3 run sh "$HELPER"
    [ "$status" -eq 0 ]
    grep -qxF "idleTime${TAB}string${TAB}0" "$BH/com.apple.screensaver.525400597fa2.plist"
    [[ "$output" == *"host UUID 00000000-0000-1000-8000-525400597FA2, made by the guest from its NIC's MAC after 3 s: the firmware named none"* ]]
}

@test "no host UUID within the wait: nothing is written, and the log says why" {
    STUB_PLATFORM_UUID= GUEST_MACOSX_BYHOST_WAIT=2 run bounded 10 sh "$HELPER"
    [ "$status" -eq 1 ]
    [[ "$output" == *"no host UUID after 2 s: the build's ByHost settings were not checked"* ]]
    ! grep -q '^PlistBuddy -c Add' "$CALLS" || false
    [ "$(cat "$CALLS.ioreg")" -eq 3 ]
}

@test "no record of ByHost settings: nothing to do, said so" {
    rm "$ROOT/Library/Preferences/guest-macosx/byhost.tsv"
    run sh "$HELPER"
    [ "$status" -eq 0 ]
    [[ "$output" == *"the build recorded no ByHost settings"* ]]
    ! grep -q '^PlistBuddy' "$CALLS"
}

@test "byhost_setting makes the setting now, for the build's host, and records it for every later one" {
    fn=$(awk '/^byhost_setting\(\) \{/,/^\}/' "$FB")
    [ -n "$fn" ]
    LOG="$BATS_TEST_TMPDIR/log"
    rm "$ROOT/Library/Preferences/guest-macosx/byhost.tsv"
    run sh -c "$fn
BYHOST_TABLE='$ROOT/Library/Preferences/guest-macosx/byhost.tsv' LOG='$LOG'
byhost_setting vagrant com.apple.screensaver idleTime string 0"
    [ "$status" -eq 0 ]
    [ "$(cat "$ROOT/Library/Preferences/guest-macosx/byhost.tsv")" = "vagrant${TAB}com.apple.screensaver${TAB}idleTime${TAB}string${TAB}0" ]
    grep -qxF "su - vagrant -c defaults -currentHost write com.apple.screensaver idleTime -string 0" "$CALLS"
}

@test "firstboot makes the screensaver setting through byhost_setting, and installs the boot job" {
    grep -qE '^byhost_setting "\$GUEST_MACOSX_FB_USER" com\.apple\.screensaver idleTime string 0$' "$FB"
    ! grep -q 'defaults -currentHost write com.apple.screensaver' "$FB" || false
    grep -qE '^write_byhost_helper$' "$FB"
    grep -q '<string>guest-macosx.byhost</string>' "$FB"
}

@test "the helper defines no function the tests take from firstboot.sh by name: theirs would be shadowed" {
    # The firstboot tests take a function out of firstboot.sh with
    # awk '/^name\(\) \{/,/^\}/', which would also take a same-named one
    # written at column 0 inside this heredoc.
    for f in $(awk '/^[a-z_]+\(\) \{/ { sub(/\(\).*/, ""); print }' "$HELPER"); do
        n=$(grep -c "^$f() {" "$FB")
        [ "$n" -eq 1 ] || { echo "$f is defined $n times at column 0 in firstboot.sh"; false; }
    done
}

@test "the LaunchDaemon sends the helper's own stdout nowhere: its lines reach system.log once, by logger" {
    # 10.6's launchd logs a job's stdout to system.log too (MEASURED 2026-10-17: every line twice).
    plist=$(awk "/<<'GUEST_MACOSX_BYHOST_PLIST_EOF'\$/ { on = 1; next } /^GUEST_MACOSX_BYHOST_PLIST_EOF\$/ { on = 0 } on" "$FB")
    [[ "$plist" == *"<key>StandardOutPath</key>"*"<string>/dev/null</string>"* ]]
}

@test "a user whose session started first is said so: the setting applies from its next read" {
    cat > "$STUB/ps" <<'EOF'
#!/bin/sh
[ -z "${STUB_SESSION:-}" ] || printf 'vagrant /usr/sbin/cfprefsd\n'
printf 'root /sbin/launchd\n_spotlight /usr/sbin/cfprefsd\n'
EOF
    chmod +x "$STUB/ps"
    STUB_SESSION=1 run sh "$HELPER"
    [ "$status" -eq 0 ]
    grep -qxF "idleTime${TAB}string${TAB}0" "$BH/com.apple.screensaver.F7B2200F-5289-5585-B893-5A6896A8FBA7.plist"
    [[ "$output" == *"vagrant's session started before this job: it sees the setting from its next read of the domain"* ]]
    rm "$BH/com.apple.screensaver.F7B2200F-5289-5585-B893-5A6896A8FBA7.plist"
    run sh "$HELPER"
    [ "$status" -eq 0 ]
    [[ "$output" != *"session started before"* ]]
}

@test "a missing ByHost folder is made the user's, not root's" {
    rm -r "$BH"
    run sh "$HELPER"
    [ "$status" -eq 0 ]
    [ -d "$BH" ]
    grep -qxF "chown vagrant:staff $BH" "$CALLS"
    [ "$(ls -ld "$BH" | cut -c1-10)" = drwx------ ]
}

@test "a ByHost file that is a symlink is left alone, never followed, and said so" {
    f="$BH/com.apple.screensaver.F7B2200F-5289-5585-B893-5A6896A8FBA7.plist"
    : > "$BATS_TEST_TMPDIR/elsewhere"
    ln -s "$BATS_TEST_TMPDIR/elsewhere" "$f"
    run sh "$HELPER"
    [ "$status" -eq 0 ]
    [ ! -s "$BATS_TEST_TMPDIR/elsewhere" ]
    ! grep -q '^chown' "$CALLS"
    [[ "$output" == *"vagrant's com.apple.screensaver idleTime: $f is a symlink; left alone"* ]]
}
