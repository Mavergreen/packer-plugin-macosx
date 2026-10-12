#!/bin/sh
# platform: macOS-only -- runs in the guest over SSH, never on the build host
# Guest-side: what Aqua Transport left in the guest, printed into the
# build's log, and then judged. The template runs this over SSH as the
# build's own user, after install.sh and a reboot, as `sh verify.sh` --
# 10.9's /bin/sh, which is its bash 3.2 in POSIX mode -- so it is POSIX sh
# throughout (and bin/bash32-check.sh covers it too).
#
# WHY IT MATTERS. Aqua Transport patches Security.framework, and a broken
# patch takes down every process that loads Security; a box made from
# such a guest is worse than none. So this runs in a fresh boot, and the
# build packages nothing unless it passes.
#
# WHAT IT REQUIRES. After printing everything, it exits non-zero -- which
# fails the build before a broken guest is packaged as a box -- unless
# every one of these holds, and it names each one that does not:
#
#   - `sw_vers` says 10.9.5, build 13F1911: the base box is the base
#     template's, with its default updates=security (Security Update
#     2016-004 makes 10.9.5 13F1911), and the install left it so;
#   - `pkgutil --pkg-info Wowfunhappy.AquaTransport` reports version
#     $AQUATRANSPORT_VERSION, the pinned one;
#   - Wowfunhappy.AquaTransport.ModernRootCertificates has a receipt: the
#     roots github.com, GitHub's release downloads and downloads.claude.ai
#     chain to;
#   - tap, by its IDs and its paths: net.sf.tuntaposx.tap has no receipt,
#     kextstat lists no kext of that bundle ID, and there is no tap.kext
#     in /Library/Extensions or /System/Library/Extensions: install.sh
#     deselected it. (Not every name holding "tap": 10.9's own
#     IOATAPIProtocolTransport, say, is no tap.)
#   - the system's own /usr/bin/curl, which links Secure Transport,
#     fetches each of the done-check's three URLs.
#
# AQUATRANSPORT_VERSION is the template's pin, passed in by the
# provisioner's environment_vars. Unset is a failure: this script cannot
# tell which version it should find.

# MQG_VERIFY_CURL and MQG_VERIFY_ROOT are test seams
# (tests/template_aquatransport.bats), never set by the template: the
# check is of /usr/bin/curl, not of whichever curl is first in PATH, and
# the files it looks for are the guest's own, under /.
CURL=${MQG_VERIFY_CURL:-/usr/bin/curl}
ROOT=${MQG_VERIFY_ROOT:-}
URLS="https://github.com/ https://release-assets.githubusercontent.com/ https://downloads.claude.ai/claude-code-releases/latest"
AT_PKG=Wowfunhappy.AquaTransport
ROOTS_PKG=Wowfunhappy.AquaTransport.ModernRootCertificates
TAP_ID=net.sf.tuntaposx.tap
TAP_KEXTS="/Library/Extensions/tap.kext /System/Library/Extensions/tap.kext"
SECURITY=/System/Library/Frameworks/Security.framework/Versions/A

# pkg_version $1: the version pkgutil records for package $1, or nothing.
pkg_version() {
    pkgutil --pkg-info "$1" 2>/dev/null | sed -n -e 's/^version: //p'
}

sw_vers
echo "aquatransport-asked=${AQUATRANSPORT_VERSION:-(unset)}"
echo "aquatransport-version=$(pkg_version "$AT_PKG")"
echo "modernroots-version=$(pkg_version "$ROOTS_PKG")"
echo "receipts=$(pkgutil --pkgs 2>/dev/null | grep -E '^(Wowfunhappy\.|net\.sf\.tuntaposx\.)' | xargs echo)"
echo "tap-kexts=$(kextstat 2>/dev/null | grep -F "$TAP_ID" | xargs echo)"
echo "security-original=$([ -e "$ROOT$SECURITY/Security.original" ] && echo yes || echo no)"
echo "curl=$("$CURL" -V 2>&1 | head -n 1)"

# --- the verdict -------------------------------------------------------------

failures=0
fail() {
    echo "verify: FAILED: $*" >&2
    failures=$((failures + 1))
}

version=$(sw_vers -productVersion 2>/dev/null)
if [ "$version" != "10.9.5" ]; then
    fail "sw_vers -productVersion is '$version', not 10.9.5: this is not the base template's OS"
fi

build=$(sw_vers -buildVersion 2>/dev/null)
if [ "$build" != "13F1911" ]; then
    fail "sw_vers -buildVersion is '$build', not 13F1911: build the base box with" \
        "updates=security (its default) or all"
fi

if ! pkgutil --pkg-info "$AT_PKG" >/dev/null 2>&1; then
    fail "pkgutil has no $AT_PKG receipt: Aqua Transport is not installed" \
        "(see /var/log/install.log in the guest)"
elif [ -z "${AQUATRANSPORT_VERSION:-}" ]; then
    fail "AQUATRANSPORT_VERSION is not set: the template's verify.sh provisioner must pass" \
        "environment_vars = [\"AQUATRANSPORT_VERSION=\${local.aquatransport_version}\"]," \
        "so this can tell which version to expect"
else
    installed=$(pkg_version "$AT_PKG")
    if [ "$installed" != "$AQUATRANSPORT_VERSION" ]; then
        fail "$AT_PKG is version '$installed', not the pinned $AQUATRANSPORT_VERSION:" \
            "the DMG holds another Aqua Transport than the pin says"
    fi
fi

pkgs=$(pkgutil --pkgs 2>/dev/null)
if ! printf '%s\n' "$pkgs" | grep -qxF "$ROOTS_PKG"; then
    fail "pkgutil --pkgs has no $ROOTS_PKG receipt: the modern roots are not installed" \
        "(see /var/log/install.log in the guest)"
fi
if printf '%s\n' "$pkgs" | grep -qxF "$TAP_ID"; then
    fail "pkgutil --pkgs has $TAP_ID: installer_choice_3 was not deselected (install.sh)"
fi

tap=$(kextstat 2>/dev/null | grep -F "$TAP_ID")
if [ -n "$tap" ]; then
    fail "kextstat lists $TAP_ID: $(printf '%s\n' "$tap" | xargs echo)"
fi
for kext in $TAP_KEXTS; do
    if [ -e "$ROOT$kext" ]; then
        fail "$kext exists: installer_choice_3 was not deselected (install.sh)"
    fi
done

# A failed fetch is named by curl's first line, its error: 10.9.5's curl
# 7.30 follows a certificate error with a paragraph of advice on CA
# bundles, which would bury the other verdicts.
for url in $URLS; do
    if ! err=$("$CURL" -sS -o /dev/null "$url" 2>&1); then
        fail "/usr/bin/curl -sS -o /dev/null $url fails: $(printf '%s\n' "$err" | head -n 1)"
    fi
done

if [ "$failures" -gt 0 ]; then
    echo "verify: $failures check(s) failed; this guest will not be packaged as a box" >&2
    exit 1
fi
echo "verify: ok: 10.9.5 (13F1911), Aqua Transport $AQUATRANSPORT_VERSION with ModernRootCertificates," \
    "no tap, and /usr/bin/curl reaches $(echo "$URLS" | wc -w | tr -d ' ') hosts"
