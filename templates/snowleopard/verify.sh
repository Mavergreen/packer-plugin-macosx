#!/bin/sh
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
#   - /private/var/db/.mqg-firstboot/.done exists: the first-boot payload
#     finished (it writes the marker last);
#   - `sudo -n true` succeeds: passwordless sudo, which the template's
#     shutdown_command (`sudo shutdown -h now`) and the box's halt
#     trigger both need;
#   - with UPDATES=security, the receipt of Security Update 2013-004 is
#     there (com.apple.pkg.update.security.10.6.8.10K1136.2013.004, read
#     2026-10-04 from the package's own PackageInfo).
#
# UPDATES is the template's updates variable, passed in by the
# provisioner's environment_vars. Unset or unknown is a failure: this
# script cannot tell "no updates were asked for" from "nobody said".

# MQG_VERIFY_MARKER is a test seam (tests/template_verify.bats), never
# set by the template.
MARKER=${MQG_VERIFY_MARKER:-/private/var/db/.mqg-firstboot/.done}
SECUPD_RECEIPT=com.apple.pkg.update.security.10.6.8.10K1136.2013.004

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
echo "firstboot-daemon=$([ -e /Library/LaunchDaemons/com.mqg.firstboot.plist ] && echo STILL-THERE || echo removed)"
echo "firstboot-ran=$(cat "$MARKER" 2>&1)"
echo "autologin=$(defaults read /Library/Preferences/com.apple.loginwindow autoLoginUser 2>&1)"
echo "diskbus=$(diskutil info disk0 2>/dev/null | grep -i Protocol | sed -e "s/.*: *//")"
echo "updates-asked=${UPDATES:-(unset)}"

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
        "(its log is /private/var/log/mqg-firstboot.log in the guest)"
fi

if ! sudo -n true 2>/dev/null; then
    fail "'sudo -n true' fails for $me: no passwordless sudo, so the" \
        "shutdown_command 'sudo shutdown -h now' cannot run (the first boot" \
        "writes /etc/sudoers.d/$me; see /private/var/log/mqg-firstboot.log)"
fi

case ${UPDATES:-} in
    none)
        ;;
    security)
        if ! pkgutil --pkgs 2>/dev/null | grep -qxF "$SECUPD_RECEIPT"; then
            fail "updates=$UPDATES, but pkgutil --pkgs has no $SECUPD_RECEIPT receipt:" \
                "Security Update 2013-004 did not install (see" \
                "/private/var/log/mqg-firstboot.log in the guest)"
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

if [ "$failures" -gt 0 ]; then
    echo "verify: $failures check(s) failed; this guest will not be packaged as a box" >&2
    exit 1
fi
echo "verify: ok: $want, first boot finished, passwordless sudo for $me, updates=$UPDATES as asked"
