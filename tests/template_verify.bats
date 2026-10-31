#!/usr/bin/env bats
# platform: host-agnostic
#
# templates/mavericks/verify.sh's verdict. It runs in the guest, over SSH, after the
# first boot; a wrong verdict either packages a broken guest as a box or
# fails a good build. These run it under this host's /bin/sh -- a stricter
# POSIX sh than 10.9's bash 3.2, which is the point -- with stubs for the
# macOS commands it judges by: sw_vers, sudo, pkgutil and defaults. Every other
# command it only prints the output of, and a missing one there prints
# nothing, which is what these expect.

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    STUB="$BATS_TEST_TMPDIR/stub"
    mkdir -p "$STUB"
    MARKER="$BATS_TEST_TMPDIR/.done"

    # sw_vers: the product version is $STUB_VERSION.
    cat > "$STUB/sw_vers" <<'EOF'
#!/bin/sh
case ${1:-} in
    -productVersion) echo "$STUB_VERSION" ;;
    *) printf 'ProductName:\tMac OS X\nProductVersion:\t%s\nBuildVersion:\t13F1911\n' "$STUB_VERSION" ;;
esac
EOF
    # sudo: passwordless when $STUB_SUDO is "ok", refused otherwise. What it
    # runs knows it runs as root (STUB_AS_ROOT).
    cat > "$STUB/sudo" <<'EOF'
#!/bin/sh
[ "$STUB_SUDO" = ok ] || { echo "sudo: a password is required" >&2; exit 1; }
[ "$1" = -n ] && shift
STUB_AS_ROOT=1 exec "$@"
EOF
    # grep: the real one, except that a file named in $STUB_ROOT_ONLY -- root's,
    # mode 600, as the guest's firstboot.conf is -- is unreadable to it unless
    # sudo ran it.
    REAL_GREP=$(command -v grep)
    export REAL_GREP
    cat > "$STUB/grep" <<'EOF'
#!/bin/sh
if [ -z "${STUB_AS_ROOT:-}" ]; then
    for a in "$@"; do
        for f in ${STUB_ROOT_ONLY:-}; do
            if [ "$a" = "$f" ]; then
                echo "grep: $a: Permission denied" >&2
                exit 2
            fi
        done
    done
fi
exec "$REAL_GREP" "$@"
EOF
    chmod +x "$STUB/grep"
    # pkgutil --pkgs: the receipts in $STUB_PKGS, one per word.
    cat > "$STUB/pkgutil" <<'EOF'
#!/bin/sh
for p in $STUB_PKGS; do echo "$p"; done
EOF
    # defaults read <loginwindow> autoLoginUser: $STUB_AUTOLOGIN, or no such key.
    # defaults read <keyboardtype> keyboardtype: the dictionary, as defaults prints it, holding
    # $STUB_KEYBOARDTYPE's entries ("key = value;" each, one per line), or no such key.
    cat > "$STUB/defaults" <<'EOF'
#!/bin/sh
if [ "$1 $2 $3" = "read /Library/Preferences/com.apple.keyboardtype keyboardtype" ]; then
    [ -n "${STUB_KEYBOARDTYPE:-}" ] || { echo "does not exist" >&2; exit 1; }
    echo "{"
    printf '%s\n' "$STUB_KEYBOARDTYPE" | sed 's/^/    /'
    echo "}"
    exit 0
fi
if [ "$1 $3" != "read autoLoginUser" ] || [ -z "${STUB_AUTOLOGIN:-}" ]; then
    echo "does not exist" >&2
    exit 1
fi
echo "$STUB_AUTOLOGIN"
EOF
    # plutil -convert xml1 -o - <file>: <file>.xml, what plutil would print of it. The file itself
    # is binary, as Apple's is (a bplist00 header), so a verify.sh that reads it raw finds no
    # XML. $STUB_PLUTIL_FAILS: plutil cannot read it.
    cat > "$STUB/plutil" <<'EOF'
#!/bin/sh
[ -z "${STUB_PLUTIL_FAILS:-}" ] || { echo "plutil: cannot read it" >&2; exit 1; }
[ "$1 $2 $3 $4" = "-convert xml1 -o -" ] && [ -f "$5.xml" ] || exit 1
cat "$5.xml"
EOF
    chmod +x "$STUB/sw_vers" "$STUB/sudo" "$STUB/pkgutil" "$STUB/defaults" "$STUB/plutil"
    # /etc/kcpassword, absent: the default build sets no password (the
    # owner's decision, 2026-10-15).
    KC="$BATS_TEST_TMPDIR/kcpassword"
    # The first boot's log, and its conf with no password left in it.
    FBLOG="$BATS_TEST_TMPDIR/guest-macosx-firstboot.log"
    echo "2026-10-15T00:00:00Z guest-macosx-firstboot: no account secret configured; the account is reached by SSH key" > "$FBLOG"
    # A 10.9 first boot with updates holds kextd off for 2016-004, which installs a kernel, and
    # restarts into it (docs/decisions/0036): verify() adds those lines to the log unless a test
    # says otherwise (MAV_FB_LINES: held, restarted, both, the default, or none).
    MAV_FB_LINES=both
    FBCONF="$BATS_TEST_TMPDIR/firstboot.conf"
    printf 'GUEST_MACOSX_FB_USER=vagrant\n' > "$FBCONF"

    # A good guest, which each test then breaks one way.
    export STUB_VERSION=10.9.5 STUB_SUDO=ok
    # Keyboard Setup Assistant's record of QEMU's keyboard, as the first boot seeds it.
    export STUB_KEYBOARDTYPE='"1-1575-0" = 40;'
    export STUB_PKGS="com.apple.pkg.BaseSystemBinaries com.apple.pkg.update.security.2016-004Mavericks.13F1911"
    date > "$MARKER"
    HELPER="$BATS_TEST_TMPDIR/guest-macosx-shutdown"
    cat > "$HELPER" <<'STUB'
#!/bin/sh
exit 0
STUB
    chmod 755 "$HELPER"
    # UserEventAgent's plugins, without the telemetry one (set aside at install).
    UEPLUGINS="$BATS_TEST_TMPDIR/UserEventPlugins"
    mkdir -p "$UEPLUGINS/com.apple.time.plugin"
    # The install's own log, which says it moved the plugin.
    POSTLOG="$BATS_TEST_TMPDIR/postinstall.log"
    echo "telemetry plugin set aside: /Volumes/x/System/Library/UserEventPlugins-disabled/com.apple.telemetry.plugin" > "$POSTLOG"
    # launchd's daemons, without systemstats' three (set aside with the plugin).
    DAEMONS="$BATS_TEST_TMPDIR/LaunchDaemons"
    mkdir -p "$DAEMONS"
    touch "$DAEMONS/com.apple.spindump.plist"
    # aslmanager's on-demand job, with the first boot's ThrottleInterval 0 (docs/decisions/0031).
    aslmanager_job '<key>ThrottleInterval</key>
	<integer>0</integer>'
    # The ByHost settings' boot job, its LaunchDaemon, and the build's record
    # of the screensaver's idleTime (docs/decisions/0024).
    BYHOST_HELPER="$BATS_TEST_TMPDIR/guest-macosx-byhost"
    cat > "$BYHOST_HELPER" <<'STUB'
#!/bin/sh
exit 0
STUB
    chmod 755 "$BYHOST_HELPER"
    BYHOST_PLIST="$BATS_TEST_TMPDIR/guest-macosx.byhost.plist"
    printf '<plist><dict><key>Label</key><string>guest-macosx.byhost</string></dict></plist>\n' > "$BYHOST_PLIST"
    BYHOST_TABLE="$BATS_TEST_TMPDIR/byhost.tsv"
    printf 'vagrant\tcom.apple.screensaver\tidleTime\tstring\t0\n' > "$BYHOST_TABLE"
    export GUEST_MACOSX_VERIFY_BYHOST_HELPER="$BYHOST_HELPER" GUEST_MACOSX_VERIFY_BYHOST_PLIST="$BYHOST_PLIST" GUEST_MACOSX_VERIFY_BYHOST_TABLE="$BYHOST_TABLE"
}

# MAV_KEXTD_HELD, MAV_RESTARTED: the lines a 10.9 first boot logs when it holds kextd off for
# 2016-004 and when it restarts into it (firstboot.sh's hold_kextd_for, restart_into_updates).
MAV_KEXTD_HELD="2026-10-28T13:34:28Z guest-macosx-firstboot: kextd: held off until the restart: guest-macosx-update-01-SecUpd2016-004Mavericks.pkg installs a kernel (guest-macosx-update-01-SecUpd2016-004Mavericks.pkg), and kextd would hand its kexts to the running kernel (13.4.0) whenever it found its caches out of date; launchd starts it again with the new kernel"
MAV_RESTARTED="2026-10-28T13:40:09Z guest-macosx-firstboot: restarting into the updated system: guest-macosx-update-01-SecUpd2016-004Mavericks.pkg installed a kernel, and kextd stays off until that kernel runs"

# aslmanager_job [extra plist lines]: 10.9.5's com.apple.aslmanager.plist: a binary plist, as
# Apple's is, beside the XML plutil prints of it (the stub's .xml).
aslmanager_job() {
    printf 'bplist00\324\001\002\003\004' > "$DAEMONS/com.apple.aslmanager.plist"
    cat > "$DAEMONS/com.apple.aslmanager.plist.xml" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>EnableTransactions</key>
	<false/>
	<key>Label</key>
	<string>com.apple.aslmanager</string>
	<key>MachServices</key>
	<dict>
		<key>com.apple.aslmanager</key>
		<true/>
	</dict>
	<key>POSIXSpawnType</key>
	<string>Interactive</string>
	<key>Program</key>
	<string>/usr/sbin/aslmanager</string>
	<key>ProgramArguments</key>
	<array>
		<string>aslmanager</string>
	</array>
	${1:-}
</dict>
</plist>
EOF
}

verify() {  # $1 = UPDATES, or "-" to leave it unset
    # This fake volume has no netbiosd job/binary; initialization is inert.
    echo 'guest-macosx-firstboot: netbiosd HUP: no regular job/executable; left as it is' >> "$FBLOG"
    case $MAV_FB_LINES in both | held) echo "$MAV_KEXTD_HELD" >> "$FBLOG" ;; esac
    case $MAV_FB_LINES in both | restarted) echo "$MAV_RESTARTED" >> "$FBLOG" ;; esac
    if [ "$1" = - ]; then
        run env -u UPDATES PATH="$STUB:$PATH" GUEST_MACOSX_VERIFY_MARKER="$MARKER" GUEST_MACOSX_VERIFY_HELPER="$HELPER" GUEST_MACOSX_VERIFY_UEPLUGINS="$UEPLUGINS" GUEST_MACOSX_VERIFY_POSTLOG="$POSTLOG" GUEST_MACOSX_VERIFY_LAUNCHDAEMONS="$DAEMONS" GUEST_MACOSX_VERIFY_KCPASSWORD="$KC" GUEST_MACOSX_VERIFY_FBLOG="$FBLOG" GUEST_MACOSX_VERIFY_CONF="$FBCONF" /bin/sh "$REPO/templates/mavericks/verify.sh"
    else
        run env UPDATES="$1" PATH="$STUB:$PATH" GUEST_MACOSX_VERIFY_MARKER="$MARKER" GUEST_MACOSX_VERIFY_HELPER="$HELPER" GUEST_MACOSX_VERIFY_UEPLUGINS="$UEPLUGINS" GUEST_MACOSX_VERIFY_POSTLOG="$POSTLOG" GUEST_MACOSX_VERIFY_LAUNCHDAEMONS="$DAEMONS" GUEST_MACOSX_VERIFY_KCPASSWORD="$KC" GUEST_MACOSX_VERIFY_FBLOG="$FBLOG" GUEST_MACOSX_VERIFY_CONF="$FBCONF" /bin/sh "$REPO/templates/mavericks/verify.sh"
    fi
}

@test "verify.sh passes a good guest, and says what it checked" {
    verify security
    [ "$status" -eq 0 ]
    [[ "$output" == *"verify: ok: 10.9.5, first boot finished, passwordless sudo for "*", guest-macosx-shutdown, telemetry plugin and systemstats jobs set aside, aslmanager unthrottled, keyboard type 40, updates=security as asked"* ]] || false
    [[ "$output" == *"updates=security"* ]] || false
    [[ "$output" != *FAILED* ]] || false
}

@test "verify.sh wants no update receipt when no updates were asked for" {
    export STUB_PKGS="com.apple.pkg.BaseSystemBinaries"
    verify none
    [ "$status" -eq 0 ]
}

@test "verify.sh passes a 10.9 guest whose first boot held kextd off for 2016-004 and restarted into it, and says so" {
    verify security
    [ "$status" -eq 0 ]
    [[ "$output" == *"kextd-hold=held off for an update that installs a kernel"* ]] || false
    [[ "$output" == *"restart-into-updates=yes"* ]] || false
}

@test "verify.sh fails a 10.9 guest whose first boot could not hold kextd off, naming the line" {
    echo "2026-10-28T13:34:28Z guest-macosx-firstboot: kextd: COULD NOT unload com.apple.kextd; guest-macosx-update-01-SecUpd2016-004Mavericks.pkg installs a kernel (guest-macosx-update-01-SecUpd2016-004Mavericks.pkg), and kextd may hand that kernel's kexts to the running one before the restart" >> "$FBLOG"
    verify security
    [ "$status" -eq 1 ]
    [[ "$output" == *"kextd-hold=COULD NOT"* ]] || false
    [[ "$output" == *"says the first boot could not hold kextd off: \"kextd: COULD NOT unload com.apple.kextd;"* ]] || false
}

@test "verify.sh fails a 10.9 guest with updates whose first boot never held kextd off, naming what it costs" {
    MAV_FB_LINES=restarted
    verify security
    [ "$status" -eq 1 ]
    [[ "$output" == *"kextd-hold=none"* ]] || false
    [[ "$output" == *"updates=security, but $FBLOG never says kextd was held off for 2016-004"* ]] || false
}

@test "verify.sh fails a 10.9 guest with updates whose first boot never restarted into them, naming what it costs" {
    MAV_FB_LINES=held
    verify security
    [ "$status" -eq 1 ]
    [[ "$output" == *"restart-into-updates=no"* ]] || false
    [[ "$output" == *"updates=security, but $FBLOG never says the first boot restarted into them"* ]] || false
}

@test "verify.sh, inert: a 10.9 guest without updates needs neither the hold nor the restart" {
    export STUB_PKGS="com.apple.pkg.BaseSystemBinaries"
    MAV_FB_LINES=none
    verify none
    [ "$status" -eq 0 ]
    [[ "$output" == *"kextd-hold=none"* ]] || false
    [[ "$output" == *"restart-into-updates=no"* ]] || false
}

@test "verify.sh prints everything before its verdict, even a failing one" {
    export STUB_VERSION=10.10
    verify security
    [ "$status" -ne 0 ]
    for line in "ProductVersion:" "hostname=" "id=" "receipts=" "updatepkgs=" \
        "firstboot-ran=" "diskbus=" "updates-asked=security"; do
        [[ "$output" == *"$line"* ]] || false
    done
}

@test "verify.sh fails a guest that is not 10.9.5, naming the version" {
    export STUB_VERSION=10.9.4
    verify security
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAILED: sw_vers -productVersion is '10.9.4', not 10.9.5"* ]] || false
}

@test "verify.sh fails a guest whose first boot never finished, naming the marker" {
    rm -f "$MARKER"
    verify security
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAILED: $MARKER does not exist: the first-boot payload never finished"* ]] || false
}

@test "verify.sh fails a guest without passwordless sudo, naming shutdown_command" {
    export STUB_SUDO=no
    verify security
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAILED: 'sudo -n true' fails for"* ]] || false
    [[ "$output" == *"shutdown_command 'sudo /usr/local/sbin/guest-macosx-shutdown -h' cannot run"* ]] || false
}

@test "verify.sh fails a guest without the shutdown helper, naming what it is for" {
    for broken in missing not-executable; do
        case $broken in
            missing) rm -f "$HELPER" ;;
            not-executable) cat > "$HELPER" <<'STUB'
#!/bin/sh
STUB
                            chmod 644 "$HELPER" ;;
        esac
        verify security
        [ "$status" -eq 1 ]
        [[ "$output" == *"FAILED: $HELPER is not an executable file"* ]] || false
        [[ "$output" == *"docs/decisions/0019"* ]] || false
    done
}

@test "verify.sh fails a guest whose UserEventAgent would load the telemetry plugin, naming why" {
    mkdir -p "$UEPLUGINS/com.apple.telemetry.plugin/Contents"
    verify security
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAILED: $UEPLUGINS/com.apple.telemetry.plugin is there"* ]] || false
    [[ "$output" == *"docs/decisions/0020"* ]] || false
    [[ "$output" == *"$POSTLOG"* && "$output" == *"/private/var/log/guest-macosx-firstboot.log"* ]] || false
}

@test "verify.sh fails a guest whose install never set the telemetry plugin aside, even with it gone now" {
    for broken in missing without-the-line could-not; do
        case $broken in
            missing) rm -f "$POSTLOG" ;;
            without-the-line) echo "target volume: /" > "$POSTLOG" ;;
            could-not) echo "telemetry plugin: COULD NOT set it aside; UserEventAgent will load it" > "$POSTLOG" ;;
        esac
        verify security
        [ "$status" -eq 1 ]
        [[ "$output" == *"FAILED: $POSTLOG has no 'telemetry plugin set aside:' line"* ]] || false
        [[ "$output" == *"/private/var/log/guest-macosx-firstboot.log"* ]] || false
    done
}

@test "verify.sh accepts an install that found the telemetry plugin already absent" {
    echo "telemetry plugin set aside: not present at install" > "$POSTLOG"
    verify security
    [ "$status" -eq 0 ]
    [[ "$output" != *FAILED* ]] || false
}

@test "verify.sh fails a guest that would load a systemstats job without the telemetry plugin, naming why" {
    for job in com.apple.systemstatsd com.apple.systemstats.daily com.apple.systemstats.analysis; do
        touch "$DAEMONS/$job.plist"
        verify security
        [ "$status" -eq 1 ]
        [[ "$output" == *"FAILED: $DAEMONS/$job.plist is there"* ]] || false
        [[ "$output" == *"docs/decisions/0022"* ]] || false
        rm -f "$DAEMONS/$job.plist"
    done
}

@test "verify.sh does not ask for systemstats' jobs aside while the telemetry plugin is in place" {
    mkdir -p "$UEPLUGINS/com.apple.telemetry.plugin/Contents"
    touch "$DAEMONS/com.apple.systemstatsd.plist" "$DAEMONS/com.apple.systemstats.daily.plist"
    verify security
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAILED: $UEPLUGINS/com.apple.telemetry.plugin is there"* ]] || false
    [[ "$output" != *"systemstatsd.plist is there"* ]] || false
    [[ "$output" != *"docs/decisions/0022"* ]] || false
}

@test "verify.sh fails a guest whose aslmanager has launchd's default throttle, naming the lost log" {
    aslmanager_job
    verify security
    [ "$status" -eq 1 ]
    [[ "$output" == *"aslmanager-throttle=launchd-default"* ]] || false
    [[ "$output" == *"FAILED: $DAEMONS/com.apple.aslmanager.plist has ThrottleInterval unset (launchd's 10 s), not 0"*"loses the boot's whole system.log and ASL store (docs/decisions/0031"* ]] || false
}

@test "verify.sh fails a guest whose aslmanager has a throttle other than 0, naming it" {
    aslmanager_job '<key>ThrottleInterval</key>
	<integer>10</integer>'
    verify security
    [ "$status" -eq 1 ]
    [[ "$output" == *"aslmanager-throttle=10"* ]] || false
    [[ "$output" == *"FAILED: $DAEMONS/com.apple.aslmanager.plist has ThrottleInterval 10, not 0"* ]] || false
    [[ "$output" == *"the first boot leaves a job's own ThrottleInterval as it is"* ]] || false
    [[ "$output" != *"the first boot sets it"* ]] || false
}

@test "verify.sh fails a guest whose aslmanager job the first boot left throttled for KeepAlive or RunAtLoad, naming the key" {
    for key in KeepAlive RunAtLoad; do
        aslmanager_job "<key>$key</key>
	<true/>"
        verify security
        [ "$status" -eq 1 ]
        [[ "$output" == *"aslmanager-throttle=left:$key"* ]] || false
        [[ "$output" == *"FAILED: $DAEMONS/com.apple.aslmanager.plist sets $key, so the first boot left it throttled (launchd's 10 s)"*"(docs/decisions/0031"* ]] || false
        [[ "$output" != *"has ThrottleInterval unset"* ]] || false
    done
}

@test "verify.sh passes a guest with aslmanager's ThrottleInterval 0, and prints it" {
    verify security
    [ "$status" -eq 0 ]
    [[ "$output" == *"aslmanager-throttle=0"* ]] || false
}

@test "verify.sh prints the edited aslmanager job's mode and owner, the evidence launchd will load it" {
    chmod 644 "$DAEMONS/com.apple.aslmanager.plist"
    verify security
    [ "$status" -eq 0 ]
    [[ "$output" == *"aslmanager-plist=-rw-r--r-- $(id -un):$(id -gn)"* ]] || false
    rm -f "$DAEMONS/com.apple.aslmanager.plist"
    verify security
    [[ "$output" == *"aslmanager-plist=no-job"* ]] || false
}

@test "verify.sh fails a guest whose aslmanager job plutil cannot read, naming it, whatever the job says" {
    for job in "" '<key>ThrottleInterval</key>
	<integer>0</integer>'; do
        aslmanager_job "$job"
        STUB_PLUTIL_FAILS=1 verify security
        [ "$status" -eq 1 ]
        [[ "$output" == *"aslmanager-throttle=unreadable"* ]] || false
        [[ "$output" == *"FAILED: cannot read $DAEMONS/com.apple.aslmanager.plist (plutil -convert xml1): this cannot tell whether syslogd's boot-time ask of aslmanager can wait out launchd's throttle (docs/decisions/0031)"* ]] || false
    done
}

@test "verify.sh reads aslmanager's binary job through plutil, not raw" {
    aslmanager_job
    [ "$(head -c 8 "$DAEMONS/com.apple.aslmanager.plist")" = bplist00 ]
    ! grep -q '<key>' "$DAEMONS/com.apple.aslmanager.plist" || false
    verify security
    [ "$status" -eq 1 ]
    [[ "$output" == *"has ThrottleInterval unset (launchd's 10 s), not 0"* ]] || false
}

@test "inert: verify.sh asks nothing of an aslmanager job that is not the on-demand service, or of none" {
    printf 'bplist00' > "$DAEMONS/com.apple.aslmanager.plist"
    printf '<plist><dict><key>Label</key><string>com.apple.aslmanager</string></dict></plist>\n' > "$DAEMONS/com.apple.aslmanager.plist.xml"
    verify security
    [ "$status" -eq 0 ]
    [[ "$output" != *"aslmanager.plist has ThrottleInterval"* ]] || false
    [[ "$output" == *"verify: ok: "*", aslmanager: not the on-demand service, keyboard type 40, updates="* ]] || false
    [[ "$output" != *"aslmanager unthrottled"* ]] || false
    rm -f "$DAEMONS/com.apple.aslmanager.plist" "$DAEMONS/com.apple.aslmanager.plist.xml"
    verify security
    [ "$status" -eq 0 ]
    [[ "$output" == *"aslmanager-throttle=no-job"* ]] || false
    [[ "$output" == *"verify: ok: "*", aslmanager: no job, keyboard type 40, updates="* ]] || false
    [[ "$output" != *"aslmanager unthrottled"* ]] || false
}

@test "verify.sh fails a guest missing the security update it was asked for" {
    export STUB_PKGS="com.apple.pkg.BaseSystemBinaries"
    for updates in security all; do
        verify "$updates"
        [ "$status" -eq 1 ]
        [[ "$output" == *"FAILED: updates=$updates, but pkgutil --pkgs has no com.apple.pkg.update.security.2016-004Mavericks.* receipt"* ]] || false
    done
}

@test "verify.sh fails when nobody said which updates to expect" {
    verify -
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAILED: UPDATES is not set"* ]] || false
    [[ "$output" == *'environment_vars = ["UPDATES=${var.updates}"]'* ]] || false

    verify bogus
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAILED: UPDATES is 'bogus', not one of none, security, all"* ]] || false
}

@test "verify.sh passes a guest with auto-login off" {
    unset STUB_AUTOLOGIN
    verify security
    [ "$status" -eq 0 ]
    [[ "$output" == *"autologin=off"* ]] || false
}

# autologin_on: the guest a build with a verified password leaves.
autologin_on() {
    export STUB_AUTOLOGIN=vagrant
    printf '0123456789ab' > "$KC"
    {
        echo "2026-10-15T00:00:00Z guest-macosx-firstboot: account secret configured; dscl . -authonly: verified"
        echo "2026-10-15T00:00:00Z guest-macosx-firstboot: auto-login: on: vagrant's password, which dscl . -authonly verified, is in /private/etc/kcpassword"
    } > "$FBLOG"
}

@test "verify.sh passes a guest with auto-login on, kcpassword there, and the first boot saying it verified the password" {
    autologin_on
    verify security
    [ "$status" -eq 0 ]
    [[ "$output" == *"autologin=vagrant"* ]] || false
}

@test "verify.sh fails a guest whose auto-login has no kcpassword, naming what it costs" {
    export STUB_AUTOLOGIN=vagrant
    verify security
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAILED: autoLoginUser is vagrant but $KC is missing or empty"* ]] || false
    [[ "$output" == *"(error: 9)"* ]] || false
}

@test "verify.sh fails a guest whose auto-login has an empty kcpassword" {
    # MEASURED 2026-10-15: an empty kcpassword failed with error 9 on 4 of 4 boots.
    autologin_on
    : > "$KC"
    verify security
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAILED: autoLoginUser is vagrant but $KC is missing or empty"* ]] || false
}

@test "verify.sh fails a guest whose kcpassword the first boot did not write from a verified password" {
    for broken in not-verified not-on no-log; do
        autologin_on
        case $broken in
            not-verified) sed -i.bak -e '/dscl . -authonly: verified/d' "$FBLOG" ;;
            not-on) sed -i.bak -e '/auto-login: on:/d' "$FBLOG" ;;
            no-log) rm -f "$FBLOG" ;;
        esac
        verify security
        [ "$status" -eq 1 ]
        [[ "$output" == *"FAILED: autoLoginUser is vagrant and $KC is there, but $FBLOG does not say"* ]] || false
    done
}

@test "verify.sh fails a guest with a kcpassword and auto-login off: a password nothing uses" {
    unset STUB_AUTOLOGIN
    printf '0123456789ab' > "$KC"
    verify security
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAILED: $KC is there with auto-login off"* ]] || false
}

# Both releases: a box's host UUID is never the build's, so the build's ByHost
# settings hold only through the boot job (docs/decisions/0024).
@test "verify.sh, both releases, fails a guest without the ByHost boot job or the build's record, naming each" {
    for rel in mavericks snowleopard; do
        for broken in helper plist table; do
            setup
            case $broken in
                helper) chmod 644 "$BYHOST_HELPER" ;;
                plist) rm "$BYHOST_PLIST" ;;
                table) : > "$BYHOST_TABLE" ;;
            esac
            if [ "$rel" = mavericks ]; then verify security; else verify_sl; fi
            [ "$status" -eq 1 ] || { echo "$rel $broken: $output"; false; }
            case $broken in
                helper) [[ "$output" == *"FAILED: $BYHOST_HELPER is not an executable file"* ]] ;;
                plist) [[ "$output" == *"FAILED: $BYHOST_PLIST is not there"* ]] ;;
                table) [[ "$output" == *"FAILED: $BYHOST_TABLE does not record the screensaver's idleTime"* ]] ;;
            esac || { echo "$rel $broken: $output"; false; }
        done
    done
}

@test "verify.sh fails a guest whose firstboot.conf still holds the password" {
    printf 'GUEST_MACOSX_FB_USER=vagrant\nGUEST_MACOSX_FB_PASSWORD=hunter2\n' > "$FBCONF"
    verify security
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAILED: $FBCONF still holds the account password"* ]] || false
    [[ "$output" != *hunter2* ]] || false
}

# cf_version <version>: the guest's CoreFoundation version.plist says so.
cf_version() {
    printf '<?xml version="1.0" encoding="UTF-8"?>\n<plist version="1.0">\n<dict>\n\t<key>BuildVersion</key>\n\t<string>1</string>\n\t<key>CFBundleShortVersionString</key>\n\t<string>%s</string>\n</dict>\n</plist>\n' "$1" > "$BATS_TEST_TMPDIR/cf-version.plist"
}

# KEXTD_HELD: the line a first boot logs when it holds kextd off for the
# combo (docs/decisions/0026), as fix1's build logged it.
KEXTD_HELD="guest-macosx-firstboot: kextd: held off until the restart: guest-macosx-update-01-041-98179.English.dist installs a kernel (SUBaseSystemCombo10.6.8.pkg), and kextd would hand its kexts to the running kernel (10.0.0) whenever it found its caches out of date; launchd starts it again with the new kernel"

# verify_sl [updates]: Snow Leopard's verify.sh, on a good 10.6.8 guest with
# updates "security" (or [updates]), against the same stubs and the same
# auto-login files, no crash report, and 2013-004's CoreFoundation (6.6.6)
# unless a test said otherwise. With updates "security", the first boot's
# log says it held kextd off for the combo unless the log already says
# something about kextd, or SL_NO_KEXTD_LOG is set.
verify_sl() {
    if [ "${1:-security}" = security ] && [ -z "${SL_NO_KEXTD_LOG:-}" ] &&
        ! grep -q "guest-macosx-firstboot: kextd: " "$FBLOG"; then
        echo "2026-10-18T17:00:12Z $KEXTD_HELD" >> "$FBLOG"
    fi
    mkdir -p "$BATS_TEST_TMPDIR/DiagnosticReports"
    [ -e "$BATS_TEST_TMPDIR/cf-version.plist" ] || cf_version 6.6.6
    [ -e "$BATS_TEST_TMPDIR/BootCache.plist" ] || bootcache_version 31.1
    [ -e "$BATS_TEST_TMPDIR/rc.server" ] || [ -n "${SL_NO_RC_SERVER:-}" ] || rc_server_ours
    run env UPDATES="${1:-security}" STUB_VERSION="${STUB_SL_VERSION:-10.6.8}" \
        STUB_PKGS="com.apple.pkg.update.security.10.6.8.10K1136.2013.004" \
        PATH="$STUB:$PATH" GUEST_MACOSX_VERIFY_MARKER="$MARKER" GUEST_MACOSX_VERIFY_KCPASSWORD="$KC" \
        GUEST_MACOSX_VERIFY_FBLOG="$FBLOG" GUEST_MACOSX_VERIFY_CONF="$FBCONF" \
        GUEST_MACOSX_VERIFY_REPORTS="$BATS_TEST_TMPDIR/DiagnosticReports" \
        GUEST_MACOSX_VERIFY_PID1CRASH="$BATS_TEST_TMPDIR/launchd-pid1.crash" \
        GUEST_MACOSX_VERIFY_CFVERSION="$BATS_TEST_TMPDIR/cf-version.plist" \
        GUEST_MACOSX_VERIFY_BOOTCACHE_CONTROL="$BATS_TEST_TMPDIR/BootCacheControl" \
        GUEST_MACOSX_VERIFY_BOOTCACHE_PLIST="$BATS_TEST_TMPDIR/BootCache.plist" \
        GUEST_MACOSX_VERIFY_RC_SERVER="$BATS_TEST_TMPDIR/rc.server" \
        GUEST_MACOSX_VERIFY_RC_SERVER_LOG="$BATS_TEST_TMPDIR/guest-macosx-rc.server.log" \
        /bin/sh "$REPO/templates/snowleopard/verify.sh"
}

# rc_server_ours: the guest's /etc/rc.server is the first boot's (its second
# line firstboot.sh's RC_SERVER_OURS), and its log says this boot waited for
# nothing. A good 10.6 guest has it (docs/decisions/0029).
rc_server_ours() {
    {
        cat <<'EOF'
#!/bin/sh
EOF
        sed -n 's/^RC_SERVER_OURS="\(.*\)"$/\1/p' "$REPO/assets/guest/firstboot.sh"
    } > "$BATS_TEST_TMPDIR/rc.server"
    echo "2026-10-19T17:00:00Z rc.server: IPv6 address already there; no wait" > "$BATS_TEST_TMPDIR/guest-macosx-rc.server.log"
}

# bootcache_version <version>: the guest's BootCache.kext Info.plist says so.
# A good 10.6.8 guest has 31.1 and no BootCacheControl (set aside).
bootcache_version() {
    printf '<plist version="1.0">\n<dict>\n\t<key>CFBundleVersion</key>\n\t<string>%s</string>\n</dict>\n</plist>\n' "$1" > "$BATS_TEST_TMPDIR/BootCache.plist"
}

@test "10.6's verify.sh passes a guest whose BootCacheControl is set aside, and says so" {
    verify_sl
    [ "$status" -eq 0 ]
    [[ "$output" == *"bootcache=31.1, BootCacheControl set aside"* ]] || false
}

@test "10.6's verify.sh fails a guest that would start BootCache 31.1, naming the deadlock" {
    : > "$BATS_TEST_TMPDIR/BootCacheControl"
    verify_sl
    [ "$status" -eq 1 ]
    [[ "$output" == *"bootcache=31.1, BootCacheControl in place"* ]] || false
    [[ "$output" == *"FAILED: $BATS_TEST_TMPDIR/BootCacheControl is in place with BootCache.kext 31.1"*"deadlock"*"0025"* ]] || false
}

@test "10.6's verify.sh fails a 10.6.8 guest whose BootCache is not 31.1, or unreadable, naming what it found" {
    for v in 31 31.2 ""; do
        if [ -n "$v" ]; then bootcache_version "$v"; else printf 'bplist00' > "$BATS_TEST_TMPDIR/BootCache.plist"; fi
        verify_sl
        [ "$status" -eq 1 ]
        [[ "$output" == *"FAILED: BootCache.kext is ${v:-unreadable}, not 10.6.8's 31.1"* ]] || false
    done
}

@test "inert: 10.6's verify.sh leaves another BootCache's BootCacheControl alone (10.6.0's 31, updates none)" {
    bootcache_version 31
    : > "$BATS_TEST_TMPDIR/BootCacheControl"
    cf_version 6.6
    STUB_SL_VERSION=10.6 verify_sl none
    [ "$status" -eq 0 ]
    [[ "$output" == *"bootcache=31, BootCacheControl in place"* ]] || false
}

@test "10.6's verify.sh passes a guest with the first boot's rc.server, and prints what this boot's said" {
    verify_sl
    [ "$status" -eq 0 ]
    [[ "$output" == *"rc-server=ours; its last boot: IPv6 address already there; no wait"* ]] || false
    # 10.6 without updates: verify runs on the first boot, which wrote it,
    # so no boot has run it yet.
    rm -f "$BATS_TEST_TMPDIR/guest-macosx-rc.server.log"
    cf_version 6.6
    bootcache_version 31
    STUB_SL_VERSION=10.6 verify_sl none
    [ "$status" -eq 0 ]
    [[ "$output" == *"rc-server=ours; its last boot: none yet"* ]] || false
}

@test "10.6's verify.sh fails a guest with no rc.server, or another's, naming F18" {
    for broken in none other; do
        rm -f "$BATS_TEST_TMPDIR/rc.server"
        if [ "$broken" = other ]; then
            cat > "$BATS_TEST_TMPDIR/rc.server" <<'EOF'
#!/bin/sh
sysctl -w kern.maxvnodes=150000
EOF
        fi
        SL_NO_RC_SERVER=1 verify_sl
        [ "$status" -eq 1 ]
        want=none; [ "$broken" = other ] && want="not ours"
        [[ "$output" == *"rc-server=$want"* ]] || false
        [[ "$output" == *"FAILED: $BATS_TEST_TMPDIR/rc.server is $want, not the first boot's wait for IPv6"*"F18, docs/decisions/0029"* ]] || false
    done
}

@test "10.6's verify.sh fails a security build whose rc.server never ran, or gave up, naming F18" {
    # With updates, the box has restarted since the first boot wrote it (the
    # combo's restart, at least), so its log has this build's last boot.
    rc_server_ours
    rm -f "$BATS_TEST_TMPDIR/guest-macosx-rc.server.log"
    verify_sl
    [ "$status" -eq 1 ]
    [[ "$output" == *"rc-server=ours; its last boot: none yet"* ]] || false
    [[ "$output" == *"FAILED: $BATS_TEST_TMPDIR/guest-macosx-rc.server.log has no line: no boot since the first has run the wait for IPv6"*"F18, docs/decisions/0029"* ]] || false
    echo "2026-10-19T17:00:00Z rc.server: still no IPv6 address after 166 checks in 10 s; going on, and launchctl's IPv6 binds can fail this boot" > "$BATS_TEST_TMPDIR/guest-macosx-rc.server.log"
    verify_sl
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAILED: the wait for IPv6 gave up on this build's last boot"*"F18, docs/decisions/0029"* ]] || false
}

@test "10.6's verify.sh fails any build whose last boot's wait gave up; inert without updates when no boot has run it" {
    cf_version 6.6
    bootcache_version 31
    rc_server_ours
    echo "2026-10-19T17:00:00Z rc.server: still no IPv6 address after 166 checks in 10 s; going on, and launchctl's IPv6 binds can fail this boot" > "$BATS_TEST_TMPDIR/guest-macosx-rc.server.log"
    STUB_SL_VERSION=10.6 verify_sl none
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAILED: the wait for IPv6 gave up on this build's last boot"* ]] || false
    rm -f "$BATS_TEST_TMPDIR/guest-macosx-rc.server.log"
    STUB_SL_VERSION=10.6 verify_sl none
    [ "$status" -eq 0 ]
}

@test "10.6's verify.sh looks for the same rc.server line firstboot.sh writes" {
    v=$(sed -n 's/^RC_SERVER_OURS=//p' "$REPO/templates/snowleopard/verify.sh" | tr -d '"')
    f=$(sed -n 's/^RC_SERVER_OURS=//p' "$REPO/assets/guest/firstboot.sh" | tr -d '"')
    [ -n "$v" ]
    [ "$v" = "$f" ]
}

@test "10.6's verify.sh fails a guest whose first boot left a crash report, naming it" {
    verify_sl
    [ "$status" -eq 0 ]
    mkdir -p "$BATS_TEST_TMPDIR/DiagnosticReports"
    touch "$BATS_TEST_TMPDIR/DiagnosticReports/MRTAgent_2026-10-15-182709_localhost.crash"
    verify_sl
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAILED: "*"MRTAgent_2026-10-15-182709_localhost.crash"* ]] || false
}

@test "10.6's verify.sh fails a guest whose launchd crashed, naming its sample" {
    touch "$BATS_TEST_TMPDIR/launchd-pid1.crash"
    verify_sl
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAILED: $BATS_TEST_TMPDIR/launchd-pid1.crash"* ]] || false
}

@test "10.6's verify.sh fails a guest whose CoreFoundation is not 2013-004's, naming the deferred install" {
    cf_version 6.6.5
    verify_sl
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAILED: CoreFoundation is 6.6.5, not 2013-004's 6.6.6"*"deferred install"* ]] || false
}

@test "10.6's verify.sh fails a guest whose first boot stopped its updates, naming the line" {
    for line in \
        "updates: STOPPED: 01-combo.dist installer failed (rc=37); installing none of the rest" \
        "updates: STOPPED: 02-SecUpd.pkg installer failed (rc=137); installing none of the rest" \
        "updates: STOPPED: 01-combo.dist was being installed when this guest last went down, and never finished; installing none of the rest (02-SecUpd.pkg)" \
        "updates: cannot write /private/var/db/.guest-macosx-firstboot/updates.left; installing none of them" \
        "updates: cannot update /private/var/db/.guest-macosx-firstboot/updates.left or /private/var/db/.guest-macosx-firstboot/updates.current; stopping the updates, 02-SecUpd.pkg and the rest not installed"; do
        cp "$FBLOG" "$FBLOG.good"
        echo "2026-10-17T00:00:00Z guest-macosx-firstboot: $line" >> "$FBLOG"
        verify_sl
        [ "$status" -eq 1 ]
        [[ "$output" == *"FAILED: $FBLOG says the first boot stopped the updates"*"${line%%;*}"* ]] || false
        mv -f "$FBLOG.good" "$FBLOG"
    done
    verify_sl
    [ "$status" -eq 0 ]
}

@test "10.6's verify.sh passes a guest whose first boot held kextd off for the combo, and says so" {
    verify_sl
    [ "$status" -eq 0 ]
    [[ "$output" == *"kextd-hold=held off for an update that installs a kernel"* ]] || false
}

@test "10.6's verify.sh fails a guest whose first boot could not hold kextd off, naming the line" {
    echo "2026-10-18T17:00:12Z guest-macosx-firstboot: kextd: COULD NOT unload com.apple.kextd; guest-macosx-update-01-041-98179.English.dist installs a kernel (SUBaseSystemCombo10.6.8.pkg), and kextd may hand that kernel's kexts to the running one before the restart" >> "$FBLOG"
    verify_sl
    [ "$status" -eq 1 ]
    [[ "$output" == *"kextd-hold=COULD NOT"* ]] || false
    [[ "$output" == *"FAILED: $FBLOG says the first boot could not hold kextd off: \"kextd: COULD NOT unload com.apple.kextd;"*"0026"* ]] || false
}

@test "10.6's verify.sh fails a guest that could not hold kextd off even where it also held it once" {
    {
        echo "2026-10-18T17:00:12Z $KEXTD_HELD"
        echo "2026-10-18T17:09:44Z guest-macosx-firstboot: kextd: COULD NOT unload com.apple.kextd; x.pkg installs a kernel (x.pkg), and kextd may hand that kernel's kexts to the running one before the restart"
    } >> "$FBLOG"
    verify_sl
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAILED: $FBLOG says the first boot could not hold kextd off"* ]] || false
}

@test "10.6's verify.sh fails a security guest whose first boot never held kextd off for the combo" {
    SL_NO_KEXTD_LOG=1 verify_sl
    [ "$status" -eq 1 ]
    [[ "$output" == *"kextd-hold=none"* ]] || false
    [[ "$output" == *"FAILED: updates=security, but $FBLOG never says kextd was held off for the combo"*"0026"* ]] || false
    # A log that says the combo installs no kernel is no better.
    echo "2026-10-18T17:00:12Z guest-macosx-firstboot: kextd: guest-macosx-update-01-041-98179.English.dist installs no kernel; kextd left running" >> "$FBLOG"
    verify_sl
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAILED: updates=security, but $FBLOG never says kextd was held off for the combo"* ]] || false
}

@test "10.6's verify.sh reads the kextd lines from a first-boot log only root can read" {
    # Root's, mode 600: only the sudo -n read sees into it.
    echo "2026-10-18T17:00:12Z $KEXTD_HELD" >> "$FBLOG"
    export STUB_ROOT_ONLY="$FBLOG"
    verify_sl
    [ "$status" -eq 0 ]
    [[ "$output" == *"kextd-hold=held off for an update that installs a kernel"* ]] || false
    echo "2026-10-18T17:09:44Z guest-macosx-firstboot: kextd: COULD NOT unload com.apple.kextd; x.pkg installs a kernel (x.pkg), and kextd may hand that kernel's kexts to the running one before the restart" >> "$FBLOG"
    verify_sl
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAILED: $FBLOG says the first boot could not hold kextd off"* ]] || false
}

@test "inert: 10.6's verify.sh asks for no kextd hold with updates none" {
    cf_version 6.6
    bootcache_version 31
    : > "$BATS_TEST_TMPDIR/BootCacheControl"
    STUB_SL_VERSION=10.6 verify_sl none
    [ "$status" -eq 0 ]
    [[ "$output" == *"kextd-hold=none"* ]] || false
}

@test "10.6's verify.sh asks for no particular CoreFoundation with updates none" {
    cf_version 6.6
    STUB_SL_VERSION=10.6 verify_sl none
    [ "$status" -eq 0 ]
}

@test "10.6's verify.sh passes a guest with auto-login off, and says so plainly" {
    unset STUB_AUTOLOGIN
    verify_sl
    [ "$status" -eq 0 ]
    [[ "$output" == *"autologin=off"* ]] || false
    [[ "$output" != *"does not exist"* ]] || false
}

@test "10.6's verify.sh passes a guest with auto-login on as the first boot sets it" {
    autologin_on
    verify_sl
    [ "$status" -eq 0 ]
    [[ "$output" == *"autologin=vagrant"* ]] || false
}

@test "10.6's verify.sh fails auto-login without kcpassword, kcpassword without auto-login, and a password left in the conf" {
    export STUB_AUTOLOGIN=vagrant
    verify_sl
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAILED: autoLoginUser is vagrant but $KC is missing or empty"* ]] || false

    autologin_on
    sed -i.bak -e '/dscl . -authonly: verified/d' "$FBLOG"
    verify_sl
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAILED: autoLoginUser is vagrant and $KC is there, but $FBLOG does not say"* ]] || false

    unset STUB_AUTOLOGIN
    verify_sl
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAILED: $KC is there with auto-login off"* ]] || false

    rm -f "$KC"
    printf 'GUEST_MACOSX_FB_PASSWORD=hunter2\n' > "$FBCONF"
    verify_sl
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAILED: $FBCONF still holds the account password"* ]] || false
}

@test "verify.sh and 10.6's find a password left in a conf only root can read" {
    # The guest's firstboot.conf is root's, mode 600: only the sudo -n read
    # can see into it.
    chmod 600 "$FBCONF"
    export STUB_ROOT_ONLY="$FBCONF"
    printf 'GUEST_MACOSX_FB_USER=vagrant\nGUEST_MACOSX_FB_PASSWORD=hunter2\n' > "$FBCONF"
    verify security
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAILED: $FBCONF still holds the account password"* ]] || false
    verify_sl
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAILED: $FBCONF still holds the account password"* ]] || false

    printf 'GUEST_MACOSX_FB_USER=vagrant\nGUEST_MACOSX_FB_SECRET_FORGOTTEN=1\n' > "$FBCONF"
    verify security
    [ "$status" -eq 0 ]
    verify_sl
    [ "$status" -eq 0 ]
}

@test "verify.sh names every failing check, not just the first" {
    export STUB_VERSION=10.9.4 STUB_SUDO=no STUB_PKGS=""
    rm -f "$MARKER"
    verify security
    [ "$status" -eq 1 ]
    [ "$(grep -c 'verify: FAILED:' <<< "$output")" -eq 4 ]
    [[ "$output" == *"verify: 4 check(s) failed; this guest will not be packaged as a box"* ]] || false
}

@test "the template passes verify.sh the updates it asked for" {
    run grep -F 'environment_vars = ["UPDATES=${var.updates}"]' "$REPO/templates/mavericks/mavericks.pkr.hcl"
    [ "$status" -eq 0 ]
}

@test "verify.sh (both releases) fails a guest with no keyboard type for QEMU's keyboard, naming it" {
    for release in mavericks snowleopard; do
        for kt in "" '"5-1452-0" = 41;' '"11-1575-0" = 40;'; do
            export STUB_KEYBOARDTYPE="$kt"
            if [ "$release" = mavericks ]; then verify security; else verify_sl; fi
            [ "$status" -eq 1 ]
            [[ "$output" == *"keyboardtype=none"* ]] || false
            [[ "$output" == *"FAILED: /Library/Preferences/com.apple.keyboardtype has no entry for QEMU's keyboard (1-1575-0): Keyboard Setup Assistant opens for it (docs/decisions/0035"* ]] || false
        done
    done
}

@test "verify.sh (both releases) takes the keyboard type the first boot found, whatever its value, and prints it" {
    for release in mavericks snowleopard; do
        export STUB_KEYBOARDTYPE='"5-1452-0" = 41;
"1-1575-0" = 42;'
        if [ "$release" = mavericks ]; then verify security; else verify_sl; fi
        [ "$status" -eq 0 ]
        [[ "$output" == *"keyboardtype=42"* ]] || false
        [[ "$output" == *"keyboard type 42, updates=security as asked"* ]] || false
    done
}
