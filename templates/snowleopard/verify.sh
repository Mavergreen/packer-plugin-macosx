#!/bin/sh
# platform: macOS-only -- runs in the guest over SSH, never on the build host
# Guest-side: what the guest is, printed into the build's log, and then
# judged. The template runs this over SSH as the build's own user, after
# firstboot-wait.sh, as `sh verify.sh` -- 10.6's /bin/sh, which is its
# bash 3.2 in POSIX mode -- so it is POSIX sh throughout (and
# bin/bash32-check.sh covers it too).
#
# WHAT IT PRINTS. sw_vers and the receipts say which updates went in; the
# machdep.cpu lines say what CPU the guest decided it got (the template's
# cpu variable names only what QEMU was asked for); sha256-64MiB is the
# "and it computed something correct" half, with one right answer
# (3b6a07d0d404fab4e23b6d34bc6696a6a312dd92821332385e5af7c01c421351);
# diskbus measures docs/host-profile.md G16's SATA/AHCI claim.
#
# WHAT IT REQUIRES. After printing everything, it exits non-zero -- which
# fails the build before a broken guest is packaged as a box -- unless
# every one of these holds, and it names each one that does not:
#
#   - `sw_vers -productVersion` is 10.6.8, or 10.6 with updates=none;
#   - /private/var/db/.guest-macosx-firstboot/.done exists: the first-boot payload
#     finished (it writes the marker last);
#   - `sudo -n true` succeeds: passwordless sudo, which the template's
#     shutdown_command (`sudo shutdown -h now`) and the box's halt
#     trigger both need;
#   - when loginwindow has an autoLoginUser, /etc/kcpassword is there and
#     not empty, and the first boot's log says it verified the password
#     (`dscl . -authonly`) and wrote kcpassword from it; when it has none,
#     there is no /etc/kcpassword; and firstboot.conf no longer holds the
#     account password. The first boot is 10.9's, shared, and these are
#     the same checks as templates/mavericks/verify.sh's (the error 9 they
#     guard against was MEASURED on 10.9; on 10.6, REASONED). The default
#     build sets no password (the owner's decision, 2026-10-15), so they
#     hold trivially there;
#   - with UPDATES=security, the receipt of Security Update 2013-004 is
#     there (com.apple.pkg.update.security.10.6.8.10K1136.2013.004, read
#     2026-10-04 from the package's own PackageInfo), and CoreFoundation is
#     2013-004's 6.6.6, not the combo's 6.6.5: the combo's deferred install,
#     run after 2013-004, put 6.6.5 back over it (of the 26 files the two
#     both carry, all 19 compared were the combo's; MEASURED 2026-10-15,
#     docs/decisions/0023);
#   - the first boot's log does not say it stopped the updates
#     ("updates: STOPPED", "updates: cannot ..."): an update cut short, or a
#     list of them it could not write, leaves the rest uninstalled whether
#     or not a receipt or sw_vers shows it (docs/decisions/0023);
#   - the first boot's log does not say it could not hold kextd off
#     ("kextd: COULD NOT unload"), and, with UPDATES=security, it says it
#     held kextd off ("kextd: held off until the restart:") for the combo,
#     which installs a kernel: otherwise the combo went in under 10.6.0's
#     kernel with kextd free to hand it 10.6.8's kexts, the race that
#     panicked a first boot (F24; MEASURED 2026-10-18, docs/decisions/0026);
#   - /Library/Preferences/com.apple.keyboardtype.plist has an entry for
#     QEMU's usb-kbd ("1-1575-0"): without it Keyboard Setup Assistant
#     opens for that keyboard, over 10.6's login window at every boot and
#     in every 10.9 desktop session (docs/decisions/0035); the first boot
#     writes it;
#   - no crash report from the first boot: nothing in
#     /Library/Logs/DiagnosticReports, and no /var/log/launchd-pid1.crash,
#     launchd's own (F16: every first boot left three, from installing
#     2013-004 before the combo's restart; docs/decisions/0023);
#   - where BootCache.kext is one measured to deadlock (10.6.8's 31.1),
#     /usr/sbin/BootCacheControl is not there: the first boot set it aside,
#     so launchd starts no boot cache, which can shut itself down inside its
#     own strategy hook in a boot's first minute and deadlock HFS there
#     (F22; MEASURED 2026-10-18, docs/decisions/0025); and with
#     UPDATES=security, the kext is 31.1, the combo's: another, or one this
#     cannot read, is one nobody has measured.
#   - /etc/rc.server is the first boot's, the wait for the kernel's first
#     IPv6 address before launchctl binds sockets: without it launchctl can
#     bind ssh's [::] before there is one, and drop ssh's sockets, IPv4 too,
#     for the whole boot (F18; MEASURED 2026-10-19, docs/decisions/0029).
#     Its log's last line is not one that gave up (no IPv6 address in 10 s,
#     or a clock it could not read), and, with UPDATES=security, there is
#     one: the box has restarted since the first boot wrote the script.
#
# UPDATES is the template's updates variable, passed in by the
# provisioner's environment_vars. Unset or unknown is a failure: this
# script cannot tell "no updates were asked for" from "nobody said".

# GUEST_MACOSX_VERIFY_MARKER is a test seam (tests/template_verify.bats), never
# set by the template.
MARKER=${GUEST_MACOSX_VERIFY_MARKER:-/private/var/db/.guest-macosx-firstboot/.done}
# GUEST_MACOSX_VERIFY_KCPASSWORD, GUEST_MACOSX_VERIFY_FBLOG and GUEST_MACOSX_VERIFY_CONF, the same.
KCPASSWORD_FILE=${GUEST_MACOSX_VERIFY_KCPASSWORD:-/private/etc/kcpassword}
FBLOG=${GUEST_MACOSX_VERIFY_FBLOG:-/private/var/log/guest-macosx-firstboot.log}
FBCONF=${GUEST_MACOSX_VERIFY_CONF:-/private/var/db/.guest-macosx-firstboot/firstboot.conf}
SECUPD_RECEIPT=com.apple.pkg.update.security.10.6.8.10K1136.2013.004
REPORTS=${GUEST_MACOSX_VERIFY_REPORTS:-/Library/Logs/DiagnosticReports}
PID1CRASH=${GUEST_MACOSX_VERIFY_PID1CRASH:-/var/log/launchd-pid1.crash}
CFVERSION=${GUEST_MACOSX_VERIFY_CFVERSION:-/System/Library/Frameworks/CoreFoundation.framework/Versions/A/Resources/version.plist}
# 2013-004's CoreFoundation (read 2026-10-15 from its Payload's version.plist).
SECUPD_CF=6.6.6
# What launchctl runs to start the boot cache, and the kext's version; the
# BootCache.kext versions MEASURED to deadlock (docs/decisions/0025), the
# same list as assets/guest/firstboot.sh's (a test holds them equal), and
# 10.6.8's own.
BOOTCACHE_CONTROL=${GUEST_MACOSX_VERIFY_BOOTCACHE_CONTROL:-/usr/sbin/BootCacheControl}
BOOTCACHE_KEXT_PLIST=${GUEST_MACOSX_VERIFY_BOOTCACHE_PLIST:-/System/Library/Extensions/BootCache.kext/Contents/Info.plist}
BOOTCACHE_DEADLOCKS=31.1
COMBO_BOOTCACHE=31.1
# /etc/rc.server, and the line the first boot's carries (the same as
# assets/guest/firstboot.sh's RC_SERVER_OURS; a test holds them equal), and
# the log it writes at every boot (docs/decisions/0029).
RC_SERVER=${GUEST_MACOSX_VERIFY_RC_SERVER:-/private/etc/rc.server}
RC_SERVER_OURS="# guest-macosx: wait for IPv6 before launchctl binds sockets (docs/decisions/0029)"
RC_SERVER_LOG=${GUEST_MACOSX_VERIFY_RC_SERVER_LOG:-/private/var/log/guest-macosx-rc.server.log}

sw_vers
echo "hostname=$(hostname)"
echo "id=$(id)"
echo "hw=$(sysctl -n hw.model) $(sysctl -n hw.ncpu)cpu $(sysctl -n hw.memsize)"
echo "cpubrand=$(sysctl -n machdep.cpu.brand_string)"
echo "cpufeatures=$(sysctl -n machdep.cpu.features)"
echo "cpuextfeatures=$(sysctl -n machdep.cpu.extfeatures)"
echo "cpuleaf7=$(sysctl -n machdep.cpu.leaf7_features 2>/dev/null)"
echo "sha256-64MiB=$(dd if=/dev/zero bs=1m count=64 2>/dev/null | openssl dgst -sha256)"
echo "receipts=$(pkgutil --pkgs 2>/dev/null | grep -icE "^com\.apple\.pkg\.update") update package(s)"
echo "updatepkgs=$(pkgutil --pkgs 2>/dev/null | grep -iE "^com\.apple\.pkg\.update" | xargs echo)"
echo "sshd=$(launchctl list | grep -c com.openssh.sshd) job(s)"
echo "ssh=$(ssh -V 2>&1)"
# shellcheck disable=SC2011  # host key filenames are never non-alphanumeric
echo "hostkeys=$(ls /usr/local/etc/ssh_host_*_key /etc/ssh_host_*_key 2>/dev/null | xargs -n1 basename | xargs echo)"
echo "setupdone=$([ -e /var/db/.AppleSetupDone ] && echo yes || echo no)"
echo "firstboot-daemon=$([ -e /Library/LaunchDaemons/guest-macosx.firstboot.plist ] && echo STILL-THERE || echo removed)"
echo "firstboot-ran=$(cat "$MARKER" 2>&1)"
autologin=$(defaults read /Library/Preferences/com.apple.loginwindow autoLoginUser 2>/dev/null)
echo "autologin=${autologin:-off}"
echo "diskbus=$(diskutil info disk0 2>/dev/null | grep -i Protocol | sed -e "s/.*: *//")"
echo "updates-asked=${UPDATES:-(unset)}"
cf=$(grep -A1 '<key>CFBundleShortVersionString</key>' "$CFVERSION" 2>/dev/null |
    sed -n 's/.*<string>\(.*\)<\/string>.*/\1/p')
echo "corefoundation=${cf:-unreadable}"
bcv=$(grep -A1 '<key>CFBundleVersion</key>' "$BOOTCACHE_KEXT_PLIST" 2>/dev/null |
    sed -n 's/.*<string>\(.*\)<\/string>.*/\1/p')
echo "bootcache=${bcv:-unreadable}, BootCacheControl $([ -e "$BOOTCACHE_CONTROL" ] && echo in place || echo set aside)"
if grep -qxF "$RC_SERVER_OURS" "$RC_SERVER" 2>/dev/null; then
    rcserver=ours
elif [ -e "$RC_SERVER" ]; then
    rcserver="not ours"
else
    rcserver=none
fi
rclast=$(tail -n 1 "$RC_SERVER_LOG" 2>/dev/null | sed 's/^[^ ]* rc.server: //')
echo "rc-server=$rcserver; its last boot: ${rclast:-none yet}"
# DiagnosticReports is root:admin, 0770: readable to the build's user, an
# admin, and to sudo -n when not.
reports=$( (ls -A "$REPORTS" 2>/dev/null || sudo -n ls -A "$REPORTS" 2>/dev/null) | xargs echo)
echo "reports=${reports:-none}"
# What the first boot did with kextd while an update's kernel went in
# (docs/decisions/0026): its own log line for each, read as root if need be.
fblog_lines() {
    grep -E "$1" "$FBLOG" 2>/dev/null || sudo -n grep -E "$1" "$FBLOG" 2>/dev/null
}
kextd_couldnot=$(fblog_lines 'guest-macosx-firstboot: kextd: COULD NOT unload' | sed -n 1p |
    sed 's/.*guest-macosx-firstboot: //')
kextd_held=$(fblog_lines 'guest-macosx-firstboot: kextd: held off until the restart:' | sed -n 1p)
if [ -n "$kextd_couldnot" ]; then
    echo "kextd-hold=COULD NOT"
elif [ -n "$kextd_held" ]; then
    echo "kextd-hold=held off for an update that installs a kernel"
else
    echo "kextd-hold=none"
fi

# --- the verdict -------------------------------------------------------------

failures=0
fail() {
    echo "verify: FAILED: $*" >&2
    failures=$((failures + 1))
}

me=$(id -un 2>/dev/null)

version=$(sw_vers -productVersion 2>/dev/null)
want=10.6.8
if [ "${UPDATES:-}" = "none" ]; then
    want=10.6
fi
if [ "$version" != "$want" ]; then
    fail "sw_vers -productVersion is '$version', not $want: this is not the OS the template installs with updates=${UPDATES:-(unset)}"
fi

# The marker's directory is root's; were it ever unreadable to this user,
# ask again through sudo before calling the first boot unfinished.
if [ -e "$MARKER" ] || sudo -n test -e "$MARKER" 2>/dev/null; then
    :
else
    fail "$MARKER does not exist: the first-boot payload never finished" \
        "(its log is /private/var/log/guest-macosx-firstboot.log in the guest)"
fi

if ! sudo -n true 2>/dev/null; then
    fail "'sudo -n true' fails for $me: no passwordless sudo, so the" \
        "shutdown_command 'sudo shutdown -h now' cannot run (the first boot" \
        "writes /etc/sudoers.d/$me; see /private/var/log/guest-macosx-firstboot.log)"
fi

# has <fixed string> <file>: grep -qF, through sudo when the file is root's.
has() {
    grep -qF -- "$1" "$2" 2>/dev/null || sudo -n grep -qF -- "$1" "$2" 2>/dev/null
}

if [ -n "$autologin" ]; then
    if [ ! -s "$KCPASSWORD_FILE" ]; then
        fail "autoLoginUser is $autologin but $KCPASSWORD_FILE is missing or empty:" \
            "loginwindow logs in by itself only with the password in it, so it fails at" \
            "every boot (\"Failed to authenticate user <$autologin> (error: 9)\") and the" \
            "login window stays (the first boot sets one only with the other, see" \
            "$FBLOG in the guest)"
    elif ! has "dscl . -authonly: verified" "$FBLOG" ||
        ! has "auto-login: on: $autologin's password" "$FBLOG"; then
        fail "autoLoginUser is $autologin and $KCPASSWORD_FILE is there, but $FBLOG" \
            "does not say the first boot verified a password with dscl . -authonly and" \
            "wrote kcpassword from it: whatever is in it, the first boot did not put it" \
            "there, and loginwindow may fail with it (error: 9)"
    fi
elif [ -e "$KCPASSWORD_FILE" ]; then
    fail "$KCPASSWORD_FILE is there with auto-login off: a password, obfuscated but not" \
        "encrypted, that nothing uses (the first boot removes it, see $FBLOG in the guest)"
fi

if has "GUEST_MACOSX_FB_PASSWORD=" "$FBCONF"; then
    fail "$FBCONF still holds the account password in plain text: the first boot" \
        "removes it once auto-login is done (see $FBLOG in the guest)"
fi

case ${UPDATES:-} in
    none)
        ;;
    security)
        if ! pkgutil --pkgs 2>/dev/null | grep -qxF "$SECUPD_RECEIPT"; then
            fail "updates=$UPDATES, but pkgutil --pkgs has no $SECUPD_RECEIPT receipt:" \
                "Security Update 2013-004 did not install (see" \
                "/private/var/log/guest-macosx-firstboot.log in the guest)"
        fi
        ;;
    '')
        fail "UPDATES is not set: the template's verify.sh provisioner must pass" \
            "environment_vars = [\"UPDATES=\${var.updates}\"], so this can tell which" \
            "updates to expect"
        ;;
    *)
        fail "UPDATES is '$UPDATES', not one of none, security"
        ;;
esac

# The first boot's own word that it stopped the updates (an update cut
# short, a list it could not write or rewrite): the rest never went in,
# whatever the receipts and sw_vers happen to say.
stop_re='guest-macosx-firstboot: updates: (STOPPED|cannot )'
stopped=$( (grep -E "$stop_re" "$FBLOG" 2>/dev/null ||
    sudo -n grep -E "$stop_re" "$FBLOG" 2>/dev/null) | sed -n 1p |
    sed 's/.*guest-macosx-firstboot: //')
if [ -n "$stopped" ]; then
    fail "$FBLOG says the first boot stopped the updates: \"$stopped\"" \
        "(docs/decisions/0023)"
fi

if [ -n "$reports" ]; then
    fail "$REPORTS holds $reports: the first boot left a crash report" \
        "(F16, docs/decisions/0023: read it before shipping this guest)"
fi
if [ -e "$PID1CRASH" ]; then
    fail "$PID1CRASH is there: launchd itself crashed during the first boot" \
        "(F16, docs/decisions/0023)"
fi

# 10.6.0's kextd hands the running kernel every kext's personalities when
# it finds its caches out of date, and during the combo those are 10.6.8's
# (F24, docs/decisions/0026): the first boot holds it off for an update
# that installs a kernel, and where it could not, the update went in
# exposed, so the build fails rather than ship a guest made through it.
if [ -n "$kextd_couldnot" ]; then
    fail "$FBLOG says the first boot could not hold kextd off: \"$kextd_couldnot\"" \
        "(F24, docs/decisions/0026)"
fi
if [ "${UPDATES:-}" = security ] && [ -z "$kextd_held" ]; then
    fail "updates=security, but $FBLOG never says kextd was held off for the combo" \
        "(\"kextd: held off until the restart:\"): it installs a kernel, and went in" \
        "with kextd free to hand 10.6.8's kexts to 10.6.0's kernel (F24, docs/decisions/0026)"
fi

if [ "${UPDATES:-}" = security ] && [ "$cf" != "$SECUPD_CF" ]; then
    fail "CoreFoundation is ${cf:-unreadable}, not 2013-004's $SECUPD_CF: the" \
        "combo's deferred install ran after 2013-004 and put its own files back" \
        "(docs/decisions/0023)"
fi

# 10.6.8's BootCache shuts its cache down inside its own strategy hook
# when its hit rate falls under 50 % in a boot's first minute, and can
# deadlock HFS there (F22, docs/decisions/0025): the first boot sets
# BootCacheControl aside wherever the kext is one measured to.
case " $BOOTCACHE_DEADLOCKS " in
    *" ${bcv:-unreadable} "*)
        if [ -e "$BOOTCACHE_CONTROL" ]; then
            fail "$BOOTCACHE_CONTROL is in place with BootCache.kext $bcv: launchd starts the" \
                "boot cache at every boot, and it can deadlock the file system in the boot's" \
                "first minute (F22, docs/decisions/0025; the first boot sets it aside, see $FBLOG)"
        fi
        ;;
esac
if [ "${UPDATES:-}" = security ] && [ "$bcv" != "$COMBO_BOOTCACHE" ]; then
    fail "BootCache.kext is ${bcv:-unreadable}, not 10.6.8's $COMBO_BOOTCACHE: the first boot" \
        "judged it by a version nobody measured (F22, docs/decisions/0025; see $FBLOG)"
fi

# 10.6's launchctl binds ssh's [::] first, and the kernel refuses IPv6 binds
# until its first IPv6 address, which it adds from a timer of its own:
# when the bind wins, launchctl drops ssh's sockets, IPv4 too, for the boot
# (F18, docs/decisions/0029). The first boot's /etc/rc.server waits for it.
if [ "$rcserver" != ours ]; then
    fail "$RC_SERVER is $rcserver, not the first boot's wait for IPv6: launchctl can bind" \
        "ssh's socket before the kernel has an IPv6 address and leave sshd no listener for a" \
        "whole boot (F18, docs/decisions/0029; see $FBLOG)"
fi
# Its log's last line is this build's last boot. With updates that boot
# came after the first boot wrote the script (the combo's restart, at
# least), so no line means it never ran; without, verify runs on the first
# boot itself, and no line is right. A wait that gave up is a boot whose
# kernel had no IPv6 address for 10 s, which a working 10.6 never is.
if [ "${UPDATES:-}" = security ] && [ -z "$rclast" ]; then
    fail "$RC_SERVER_LOG has no line: no boot since the first has run the wait for IPv6" \
        "(F18, docs/decisions/0029; see $FBLOG)"
fi
case $rclast in
    "still no IPv6 address"* | "cannot read the clock"*)
        fail "the wait for IPv6 gave up on this build's last boot ($rclast): launchctl can have" \
            "bound ssh's socket before the kernel had an IPv6 address (F18, docs/decisions/0029)"
        ;;
esac

# The ByHost settings' boot job (docs/decisions/0024): a box's host UUID is
# never the build's, so the build's ByHost settings (the screensaver's
# idleTime) hold in a box only through guest-macosx-byhost, which adds each
# one the build recorded to this host's file at boot.
BYHOST_HELPER=${GUEST_MACOSX_VERIFY_BYHOST_HELPER:-/usr/local/libexec/guest-macosx-byhost}
BYHOST_PLIST=${GUEST_MACOSX_VERIFY_BYHOST_PLIST:-/Library/LaunchDaemons/guest-macosx.byhost.plist}
BYHOST_TABLE=${GUEST_MACOSX_VERIFY_BYHOST_TABLE:-/Library/Preferences/guest-macosx/byhost.tsv}
if [ ! -f "$BYHOST_HELPER" ] || [ ! -x "$BYHOST_HELPER" ]; then
    fail "$BYHOST_HELPER is not an executable file: the build's ByHost settings" \
        "would not reach a box's own host (docs/decisions/0024; the first boot writes it)"
fi
if [ ! -f "$BYHOST_PLIST" ]; then
    fail "$BYHOST_PLIST is not there: nothing runs guest-macosx-byhost at boot" \
        "(docs/decisions/0024; the first boot writes it)"
fi
if ! grep -q "	com.apple.screensaver	idleTime	" "$BYHOST_TABLE" 2>/dev/null; then
    fail "$BYHOST_TABLE does not record the screensaver's idleTime: a box would" \
        "run the screensaver after 20 min idle (docs/decisions/0024; the first boot records it)"
fi

# Keyboard Setup Assistant's record of QEMU's usb-kbd (docs/decisions/0035): the first boot's
# seed_keyboard_type writes it where there is none.
KEYBOARDTYPE_DOMAIN=/Library/Preferences/com.apple.keyboardtype
QEMU_KEYBOARD_TYPE_KEY=1-1575-0
kbd_type=$(defaults read "$KEYBOARDTYPE_DOMAIN" keyboardtype 2>/dev/null |
    sed -n "s/^[[:space:]]*\"\{0,1\}$QEMU_KEYBOARD_TYPE_KEY\"\{0,1\}[[:space:]]*=[[:space:]]*\([^;]*\);.*/\1/p")
echo "keyboardtype=${kbd_type:-none}"
if [ -z "$kbd_type" ]; then
    fail "$KEYBOARDTYPE_DOMAIN has no entry for QEMU's keyboard ($QEMU_KEYBOARD_TYPE_KEY):" \
        "Keyboard Setup Assistant opens for it (docs/decisions/0035; the first boot writes it," \
        "see /private/var/log/guest-macosx-firstboot.log)"
fi

if [ "$failures" -gt 0 ]; then
    echo "verify: $failures check(s) failed; this guest will not be packaged as a box" >&2
    exit 1
fi
echo "verify: ok: $want, first boot finished, passwordless sudo for $me, keyboard type $kbd_type, updates=$UPDATES as asked"
