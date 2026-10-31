#!/bin/sh
# platform: macOS-only -- runs in the guest over SSH, never on the build host
# Guest-side: what the guest is, printed into the build's log, and then
# judged. The template runs this over SSH as the build's own user, after
# firstboot-wait.sh, as `sh verify.sh` -- 10.9's /bin/sh, which is its
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
#   - `sw_vers -productVersion` is 10.9.5;
#   - /private/var/db/.guest-macosx-firstboot/.done exists: the first-boot payload
#     finished (it writes the marker last);
#   - `sudo -n true` succeeds: passwordless sudo, which the template's
#     shutdown_command (`sudo /usr/local/sbin/guest-macosx-shutdown -h`) and the
#     box's halt trigger both need;
#   - /usr/local/sbin/guest-macosx-shutdown is an executable file: every shutdown
#     and restart goes through it, or 10.9 can stall 20 s on the way down
#     (docs/decisions/0019); the first boot writes it;
#   - /System/Library/UserEventPlugins/com.apple.telemetry.plugin is not
#     there: UserEventAgent (System) crashed in 11 of 335 boots in the runs
#     that saw it, while it loaded it (docs/decisions/0020); the install
#     sets it aside;
#   - the install's own log says it did ("telemetry plugin set aside:"):
#     the first boot moves the plugin again, so the end state alone cannot
#     show that the install-time move, the one that protects the build's
#     own first boot, worked;
#   - with the plugin set aside, none of systemstats' three jobs
#     (com.apple.systemstatsd, com.apple.systemstats.daily,
#     com.apple.systemstats.analysis) is in /System/Library/LaunchDaemons:
#     without the plugin, a request to systemstatsd (the daily run's, at
#     00:15) holds it dirty for the rest of the boot, and every later
#     shutdown 20 s (docs/decisions/0022); the install sets them aside;
#   - aslmanager's job, when it is the on-demand com.apple.aslmanager
#     service, sets ThrottleInterval 0: with launchd's default 10 s, a
#     boot's syslogd can wait on a throttled aslmanager for 10 s, writing
#     nothing, and a shutdown then loses the boot's whole system.log and
#     ASL store (docs/decisions/0031); the first boot sets it;
#   - when loginwindow has an autoLoginUser, /etc/kcpassword is there and
#     not empty, and the first boot's log says it verified the password
#     (`dscl . -authonly`) and wrote kcpassword from it: loginwindow logs in
#     by itself only with the password in that file, and with it missing,
#     or empty, fails at every boot ("Failed to authenticate user <user>
#     (error: 9)") and stays at the login window (MEASURED 2026-10-15);
#     the first boot sets autoLoginUser only when it also writes kcpassword;
#   - when it has none, there is no /etc/kcpassword: an obfuscated
#     password that nothing uses (the first boot removes it);
#   - firstboot.conf no longer holds the account password: the first boot
#     removes it once auto-login is done. The default build sets no
#     password (the owner's decision, 2026-10-15), so these three hold
#     trivially there;
#   - /Library/Preferences/com.apple.keyboardtype.plist has an entry for
#     QEMU's usb-kbd ("1-1575-0"): without it Keyboard Setup Assistant
#     opens for that keyboard, over 10.6's login window at every boot and
#     in every 10.9 desktop session (docs/decisions/0035); the first boot
#     writes it;
#   - with UPDATES other than none, the receipt of Security Update
#     2016-004 is there (com.apple.pkg.update.security.2016-004Mavericks.*,
#     MEASURED 2026-09-22, docs/decisions/0011; the only update both
#     "security" and "all" carry);
#   - with UPDATES other than none, the first boot's log says it held
#     kextd off for 2016-004, which installs a kernel ("kextd: held off
#     until the restart:"), and never that it could not ("kextd: COULD NOT
#     unload"), and that it restarted into the updated system ("restarting
#     into the updated system:"): without them the build's provisioners and
#     its shutdown ran on the boot that installed the update, under the old
#     kernel, whose request for a prelinked kernel can hold that shutdown
#     for minutes (F29, docs/decisions/0036).
#
# UPDATES is the template's updates variable, passed in by the
# provisioner's environment_vars. Unset or unknown is a failure: this
# script cannot tell "no updates were asked for" from "nobody said".

# GUEST_MACOSX_VERIFY_MARKER is a test seam (tests/template_verify.bats), never
# set by the template.
MARKER=${GUEST_MACOSX_VERIFY_MARKER:-/private/var/db/.guest-macosx-firstboot/.done}
# GUEST_MACOSX_VERIFY_HELPER, the same.
HELPER=${GUEST_MACOSX_VERIFY_HELPER:-/usr/local/sbin/guest-macosx-shutdown}
# GUEST_MACOSX_VERIFY_UEPLUGINS, the same.
UEPLUGINS=${GUEST_MACOSX_VERIFY_UEPLUGINS:-/System/Library/UserEventPlugins}
# GUEST_MACOSX_VERIFY_POSTLOG, the same: the install's own log.
POSTLOG=${GUEST_MACOSX_VERIFY_POSTLOG:-/private/var/db/.guest-macosx-firstboot/postinstall.log}
# GUEST_MACOSX_VERIFY_LAUNCHDAEMONS, the same.
DAEMONS=${GUEST_MACOSX_VERIFY_LAUNCHDAEMONS:-/System/Library/LaunchDaemons}
# GUEST_MACOSX_VERIFY_KCPASSWORD, the same.
KCPASSWORD_FILE=${GUEST_MACOSX_VERIFY_KCPASSWORD:-/private/etc/kcpassword}
# GUEST_MACOSX_VERIFY_FBLOG and GUEST_MACOSX_VERIFY_CONF, the same: the first boot's log and conf.
FBLOG=${GUEST_MACOSX_VERIFY_FBLOG:-/private/var/log/guest-macosx-firstboot.log}
FBCONF=${GUEST_MACOSX_VERIFY_CONF:-/private/var/db/.guest-macosx-firstboot/firstboot.conf}
SYSTEMSTATS_JOBS="com.apple.systemstatsd com.apple.systemstats.daily com.apple.systemstats.analysis"
SECUPD_RECEIPT=com.apple.pkg.update.security.2016-004Mavericks

sw_vers
echo "hostname=$(hostname)"
echo "id=$(id)"
echo "hw=$(sysctl -n hw.model) $(sysctl -n hw.ncpu)cpu $(sysctl -n hw.memsize)"
echo "cpubrand=$(sysctl -n machdep.cpu.brand_string)"
echo "cpufeatures=$(sysctl -n machdep.cpu.features)"
echo "cpuextfeatures=$(sysctl -n machdep.cpu.extfeatures)"
echo "cpuleaf7=$(sysctl -n machdep.cpu.leaf7_features 2>/dev/null)"
echo "sha256-64MiB=$(dd if=/dev/zero bs=1m count=64 2>/dev/null | openssl dgst -sha256)"
echo "receipts=$(pkgutil --pkgs 2>/dev/null | grep -icE "^com\.apple\.pkg\.(update|Safari|iTunes)") post-10.9.5 package(s)"
echo "updatepkgs=$(pkgutil --pkgs 2>/dev/null | grep -iE "^com\.apple\.pkg\.(update|Safari|iTunes)" | xargs echo)"
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
echo "telemetry-plugin=$([ -e "$UEPLUGINS/com.apple.telemetry.plugin" ] && echo STILL-THERE || echo set-aside)"
echo "telemetry-plugin-install=$(grep '^telemetry plugin' "$POSTLOG" 2>&1)"
echo "systemstats-jobs-install=$(grep '^systemstats jobs' "$POSTLOG" 2>&1)"
ASLMANAGER_PLIST=$DAEMONS/com.apple.aslmanager.plist
# Apple's job is a binary plist: read through plutil, never raw. A job there that plutil cannot
# read is a failure below, not a pass.
aslm_xml=$(plutil -convert xml1 -o - "$ASLMANAGER_PLIST" 2>/dev/null)
aslm_ti=$(printf '%s\n' "$aslm_xml" | grep -A1 '<key>ThrottleInterval</key>' | sed -n 's/.*<integer>\(.*\)<\/integer>.*/\1/p')
# The first boot's cases (firstboot.sh's unthrottle_aslmanager), in its order: not the on-demand
# service, left alone; KeepAlive or RunAtLoad, left throttled; a ThrottleInterval of its own, kept.
aslm_service=
printf '%s\n' "$aslm_xml" | grep -q '<key>com.apple.aslmanager</key>' && aslm_service=1
aslm_left=
for aslm_key in KeepAlive RunAtLoad; do
    if [ -n "$aslm_service" ] && [ -z "$aslm_left" ] && printf '%s\n' "$aslm_xml" | grep -q "<key>$aslm_key</key>"; then
        aslm_left=$aslm_key
    fi
done
if [ -f "$ASLMANAGER_PLIST" ] && [ -z "$aslm_xml" ]; then
    echo "aslmanager-throttle=unreadable"
elif [ -n "$aslm_left" ]; then
    echo "aslmanager-throttle=left:$aslm_left"
elif [ -f "$ASLMANAGER_PLIST" ]; then
    echo "aslmanager-throttle=${aslm_ti:-launchd-default}"
else
    echo "aslmanager-throttle=no-job"
fi
# launchd loads a system LaunchDaemon only when root owns it and no one else can write it; the
# first boot's edit is meant to leave Apple's root:wheel 644 as it was.
if [ -f "$ASLMANAGER_PLIST" ]; then
    # shellcheck disable=SC2012  # one fixed path, read for its mode and owner columns
    echo "aslmanager-plist=$(ls -l "$ASLMANAGER_PLIST" 2>&1 | awk '{print $1, $3 ":" $4}')"
else
    echo "aslmanager-plist=no-job"
fi
echo "updates-asked=${UPDATES:-(unset)}"
# What the first boot did with kextd while 2016-004's kernel went in, and whether it restarted into
# it (docs/decisions/0036): its own log lines, read as root if need be.
fblog_lines() {
    grep -E "$1" "$FBLOG" 2>/dev/null || sudo -n grep -E "$1" "$FBLOG" 2>/dev/null
}
kextd_couldnot=$(fblog_lines 'guest-macosx-firstboot: kextd: COULD NOT unload' | sed -n 1p |
    sed 's/.*guest-macosx-firstboot: //')
kextd_held=$(fblog_lines 'guest-macosx-firstboot: kextd: held off until the restart:' | sed -n 1p)
restarted=$(fblog_lines 'guest-macosx-firstboot: restarting into the updated system:' | sed -n 1p)
if [ -n "$kextd_couldnot" ]; then
    echo "kextd-hold=COULD NOT"
elif [ -n "$kextd_held" ]; then
    echo "kextd-hold=held off for an update that installs a kernel"
else
    echo "kextd-hold=none"
fi
echo "restart-into-updates=$([ -n "$restarted" ] && echo yes || echo no)"

# --- the verdict -------------------------------------------------------------

failures=0
fail() {
    echo "verify: FAILED: $*" >&2
    failures=$((failures + 1))
}

# A firstboot action is not proof that its job/helper survived to packaging.
# Paths are overridable only for the host-side fake-volume tests, like DAEMONS.
verify_netbiosd_hup() {
    nbv_line=$(fblog_lines 'guest-macosx-firstboot: netbiosd HUP:' | tail -n 1)
    echo "netbiosd-hup-install=$nbv_line"
    case $nbv_line in
        *'COULD NOT'*|*'existing wrapper is not ours'*)
            fail "netbiosd HUP initialization failed: $nbv_line"; return ;;
        *'ignored before exec from next boot'*|*'already initialized before exec'*) ;;
        *'left as it is'*) echo 'netbiosd-hup=inert'; return ;;
        *) fail 'no recognized firstboot netbiosd HUP result'; return ;;
    esac
    nbv_plist=${GUEST_MACOSX_VERIFY_NETBIOSD_PLIST:-$DAEMONS/com.apple.netbiosd.plist}
    nbv_bin=${GUEST_MACOSX_VERIFY_NETBIOSD_BIN:-/usr/sbin/netbiosd}
    nbv_wrapper=${GUEST_MACOSX_VERIFY_NETBIOSD_WRAPPER:-/usr/libexec/guest-macosx-netbiosd-hup}
    nbv_pb=${GUEST_MACOSX_VERIFY_PLISTBUDDY:-/usr/libexec/PlistBuddy}
    if [ ! -f "$nbv_plist" ] || [ -L "$nbv_plist" ] ||
        [ ! -f "$nbv_bin" ] || [ -L "$nbv_bin" ] || [ ! -x "$nbv_bin" ]; then
        fail 'netbiosd HUP job/executable is missing or not a regular job'; return
    fi
    if [ ! -f "$nbv_wrapper" ] || [ -L "$nbv_wrapper" ] || [ ! -x "$nbv_wrapper" ] ||
        ! printf '%s\n' '#!/bin/sh' '# guest-macosx: initialize HUP before netbiosd exec (docs/decisions/0037)' "trap '' HUP" 'exec "$@"' | cmp -s "$nbv_wrapper" -; then
        fail 'netbiosd HUP wrapper missing, changed, or not executable'; return
    fi
    # shellcheck disable=SC2012  # one fixed path, mode/numeric owner only
    nbv_mode=$(LC_ALL=C ls -ln "$nbv_wrapper" | awk '{print $1, $3 ":" $4}')
    [ "$nbv_mode" = '-rwxr-xr-x 0:0' ] || fail 'netbiosd HUP wrapper is not root:wheel 755'
    if [ "$("$nbv_pb" -c 'Print :ProgramArguments:0' "$nbv_plist" 2>/dev/null)" != "$nbv_wrapper" ] ||
        [ "$("$nbv_pb" -c 'Print :ProgramArguments:1' "$nbv_plist" 2>/dev/null)" != "$nbv_bin" ] ||
        "$nbv_pb" -c 'Print :ProgramArguments:2' "$nbv_plist" >/dev/null 2>&1; then
        fail 'netbiosd HUP job does not exec the original command exactly once'
    fi
    if nbv_program=$("$nbv_pb" -c 'Print :Program' "$nbv_plist" 2>/dev/null); then
        [ "$nbv_program" = "$nbv_wrapper" ] || fail 'netbiosd HUP job has a conflicting Program'
    fi
    echo 'netbiosd-hup=inspected'
}
verify_netbiosd_hup

me=$(id -un 2>/dev/null)
# An update that went in with kextd free to rebuild under the old kernel (docs/decisions/0036).
if [ -n "$kextd_couldnot" ]; then
    fail "$FBLOG says the first boot could not hold kextd off: \"$kextd_couldnot\"" \
        "(F29, docs/decisions/0036)"
fi

version=$(sw_vers -productVersion 2>/dev/null)
if [ "$version" != "10.9.5" ]; then
    fail "sw_vers -productVersion is '$version', not 10.9.5: this is not the OS the template installs"
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
        "shutdown_command 'sudo /usr/local/sbin/guest-macosx-shutdown -h' cannot run (the first boot" \
        "writes /etc/sudoers.d/$me; see /private/var/log/guest-macosx-firstboot.log)"
fi

if [ ! -f "$HELPER" ] || [ ! -x "$HELPER" ]; then
    fail "$HELPER is not an executable file: every shutdown and restart goes" \
        "through it, or 10.9 can stall 20 s on the way down (docs/decisions/0019;" \
        "the first boot writes it, see /private/var/log/guest-macosx-firstboot.log)"
fi

if [ -e "$UEPLUGINS/com.apple.telemetry.plugin" ]; then
    fail "$UEPLUGINS/com.apple.telemetry.plugin is there: UserEventAgent (System)" \
        "crashed in 11 of 335 boots in the runs that saw it, while it loads it, and the" \
        "boot gate fails on the report (docs/decisions/0020; the install and the first" \
        "boot set it aside, see $POSTLOG and" \
        "/private/var/log/guest-macosx-firstboot.log in the guest)"
fi

if ! grep -q '^telemetry plugin set aside: ' "$POSTLOG" 2>/dev/null; then
    fail "$POSTLOG has no 'telemetry plugin set aside:' line: the install never" \
        "moved the plugin, so the build's own first boot loaded it, and the first boot" \
        "moving it later (/private/var/log/guest-macosx-firstboot.log) hides that" \
        "(docs/decisions/0020)"
fi

# Asked only where the plugin is set aside: with it in place, systemstatsd
# gets its data and answers, and its jobs belong where they are.
if [ ! -e "$UEPLUGINS/com.apple.telemetry.plugin" ]; then
    for job in $SYSTEMSTATS_JOBS; do
        if [ -e "$DAEMONS/$job.plist" ]; then
            fail "$DAEMONS/$job.plist is there, with the telemetry plugin set aside:" \
                "a request to systemstatsd (systemstats --daily's, at 00:15) then holds it" \
                "dirty for the rest of the boot, and every later shutdown waits out its" \
                "20 s and leaves a report the boot gate fails on (docs/decisions/0022; the" \
                "install sets it aside, see $POSTLOG in the guest)"
        fi
    done
fi

# Asked only of aslmanager's on-demand service: a job without that Mach
# service is not the one syslogd waits on. One that cannot be read cannot
# be told apart, so it fails.
if [ -f "$ASLMANAGER_PLIST" ] && [ -z "$aslm_xml" ]; then
    fail "cannot read $ASLMANAGER_PLIST (plutil -convert xml1): this cannot tell whether" \
        "syslogd's boot-time ask of aslmanager can wait out launchd's throttle (docs/decisions/0031)"
elif [ -n "$aslm_left" ]; then
    # The first boot leaves such a job alone on purpose, and the guest still has the window: a
    # box must not ship with it, so the build fails, saying why the key is missing.
    fail "$ASLMANAGER_PLIST sets $aslm_left, so the first boot left it throttled (launchd's 10 s):" \
        "syslogd's boot-time ask of aslmanager can wait out that throttle, writing nothing, and" \
        "a shutdown in it loses the boot's whole system.log and ASL store (docs/decisions/0031;" \
        "see $FBLOG in the guest)"
elif [ -n "$aslm_service" ] && [ -n "$aslm_ti" ] && [ "$aslm_ti" != 0 ]; then
    fail "$ASLMANAGER_PLIST has ThrottleInterval $aslm_ti, not 0, and the first boot leaves a" \
        "job's own ThrottleInterval as it is: syslogd's boot-time ask of aslmanager can wait out" \
        "that throttle, writing nothing, and a shutdown in it loses the boot's whole system.log" \
        "and ASL store (docs/decisions/0031; see $FBLOG in the guest)"
elif [ -n "$aslm_service" ] && [ -z "$aslm_ti" ]; then
    fail "$ASLMANAGER_PLIST has ThrottleInterval unset (launchd's 10 s), not 0:" \
        "syslogd's boot-time ask of aslmanager can then wait out launchd's throttle, writing" \
        "nothing, and a shutdown in it loses the boot's whole system.log and ASL store" \
        "(docs/decisions/0031; the first boot sets it, see $FBLOG in the guest)"
fi
# What the verdict says of it: only an unthrottled service is "unthrottled".
if [ ! -f "$ASLMANAGER_PLIST" ]; then
    aslm_verdict="aslmanager: no job"
elif [ -n "$aslm_xml" ] && [ -z "$aslm_service" ]; then
    aslm_verdict="aslmanager: not the on-demand service"
else
    aslm_verdict="aslmanager unthrottled"
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
    security|all)
        if ! pkgutil --pkgs 2>/dev/null | grep -q '^com\.apple\.pkg\.update\.security\.2016-004Mavericks'; then
            fail "updates=$UPDATES, but pkgutil --pkgs has no $SECUPD_RECEIPT.* receipt:" \
                "Security Update 2016-004 did not install (see" \
                "/private/var/log/guest-macosx-firstboot.log in the guest)"
        fi
        # 2016-004 installs a kernel (docs/decisions/0036): it goes in with kextd held off, and the
        # first boot restarts into it, so the build's provisioners and shutdown run under its kernel.
        if [ -z "$kextd_held" ]; then
            fail "updates=$UPDATES, but $FBLOG never says kextd was held off for 2016-004" \
                "(\"kextd: held off until the restart:\"): it installs a kernel, and the old one" \
                "asks kextd for a prelinked kernel that can hold the build's shutdown" \
                "(F29, docs/decisions/0036)"
        fi
        if [ -z "$restarted" ]; then
            fail "updates=$UPDATES, but $FBLOG never says the first boot restarted into them" \
                "(\"restarting into the updated system:\"): this boot may still run the old" \
                "kernel, whose request for a prelinked kernel can hold the build's shutdown" \
                "(F29, docs/decisions/0036)"
        fi
        ;;
    '')
        fail "UPDATES is not set: the template's verify.sh provisioner must pass" \
            "environment_vars = [\"UPDATES=\${var.updates}\"], so this can tell which" \
            "updates to expect"
        ;;
    *)
        fail "UPDATES is '$UPDATES', not one of none, security, all"
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
echo "verify: ok: 10.9.5, first boot finished, passwordless sudo for $me, guest-macosx-shutdown, telemetry plugin and systemstats jobs set aside, $aslm_verdict, keyboard type $kbd_type, updates=$UPDATES as asked"
