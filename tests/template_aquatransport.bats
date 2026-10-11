#!/usr/bin/env bats
#
# templates/mavericks-aquatransport/'s two guest scripts. install.sh puts
# Aqua Transport on the base box's guest, without tap.kext; verify.sh
# judges the result, and a wrong verdict either packages a guest whose
# Security.framework or trust store is broken, or fails a good build.
# These run them under this host's /bin/sh -- a stricter POSIX sh than
# 10.9's bash 3.2, which is the point -- with stubs for the macOS commands
# they use.
#
# NOTHING HERE REACHES THE HOST. The stubs simulate a guest on this
# machine, so: the stub sudo runs only the stub installer and refuses
# everything else; the scripts' PATH is the stubs, /usr/bin and /bin,
# never an sbin; shutdown, reboot, halt and poweroff are stubs that fail;
# and verify.sh's curl is a stub, named through its MQG_VERIFY_CURL seam,
# so no test fetches anything.

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    TMPL="$REPO/templates/mavericks-aquatransport"
    STUB="$BATS_TEST_TMPDIR/stub"
    LOG="$BATS_TEST_TMPDIR/log"
    mkdir -p "$STUB"
    : > "$LOG"
    export LOG

    for name in shutdown reboot halt poweroff systemctl; do
        printf '#!/bin/sh\necho "stub: %s refused" >&2\nexit 1\n' "$name" > "$STUB/$name"
    done

    # sw_vers: $STUB_VERSION and $STUB_BUILD.
    cat > "$STUB/sw_vers" <<'EOF'
#!/bin/sh
case ${1:-} in
    -productVersion) echo "$STUB_VERSION" ;;
    -buildVersion) echo "$STUB_BUILD" ;;
    *) printf 'ProductName:\tMac OS X\nProductVersion:\t%s\nBuildVersion:\t%s\n' "$STUB_VERSION" "$STUB_BUILD" ;;
esac
EOF
    # pkgutil: --pkgs lists $STUB_PKGS, one per word; --pkg-info
    # Wowfunhappy.AquaTransport reports $STUB_AT_VERSION, and fails as
    # pkgutil does for a package with no receipt.
    cat > "$STUB/pkgutil" <<'EOF'
#!/bin/sh
case ${1:-} in
    --pkgs) for p in $STUB_PKGS; do echo "$p"; done ;;
    --pkg-info)
        for p in $STUB_PKGS; do
            if [ "$p" = "$2" ]; then
                printf 'package-id: %s\nversion: %s\nvolume: /\nlocation: \ninstall-time: 1760000000\n' \
                    "$2" "$STUB_AT_VERSION"
                exit 0
            fi
        done
        echo "No receipt for '$2' found at '/'." >&2
        exit 1
        ;;
    *) exit 1 ;;
esac
EOF
    # kextstat: a header, then $STUB_KEXTS, one bundle ID per word.
    cat > "$STUB/kextstat" <<'EOF'
#!/bin/sh
echo "Index Refs Address            Size       Wired      Name (Version) <Linked Against>"
i=1
for k in $STUB_KEXTS; do
    echo "  $i    0 0xffffff7f80000000 0x1000     0x1000     $k (1.0) <1>"
    i=$((i + 1))
done
EOF
    # curl: fails for the URL in $STUB_CURL_FAIL, as curl does when it
    # cannot verify a peer; -V says what it is.
    cat > "$STUB/curl" <<'EOF'
#!/bin/sh
[ "${1:-}" = -V ] && { echo "curl 7.30.0 (stub)"; exit 0; }
for url; do :; done
echo "curl $*" >> "$LOG"
if [ "$url" = "${STUB_CURL_FAIL:-}" ]; then
    echo "curl: (60) SSL certificate problem: Invalid certificate chain" >&2
    exit 60
fi
EOF
    # hdiutil: attach "mounts" a volume holding AquaTransport.pkg at the
    # -mountpoint it was given; detach empties it again.
    cat > "$STUB/hdiutil" <<'EOF'
#!/bin/sh
echo "hdiutil $*" >> "$LOG"
case $1 in
    attach)
        while [ $# -gt 0 ]; do
            [ "$1" = -mountpoint ] && { mkdir -p "$2"; : > "$2/AquaTransport.pkg"; }
            shift
        done
        ;;
    detach) rm -f "$2/AquaTransport.pkg" ;;
esac
EOF
    # installer: logs its arguments, and keeps a copy of the choice
    # changes it was handed, which install.sh removes afterwards.
    cat > "$STUB/installer" <<'EOF'
#!/bin/sh
echo "installer $*" >> "$LOG"
while [ $# -gt 0 ]; do
    if [ "$1" = -applyChoiceChangesXML ]; then cp "$2" "$LOG.choices"; fi
    shift
done
exit "${STUB_INSTALLER_STATUS:-0}"
EOF
    # sudo: the stub installer, and nothing else.
    cat > "$STUB/sudo" <<EOF
#!/bin/sh
[ "\$1" = -n ] && shift
case \$1 in
    installer) shift; exec "$STUB/installer" "\$@" ;;
    *) echo "stub sudo: refusing \$1" >&2; exit 1 ;;
esac
EOF
    chmod +x "$STUB"/*

    # A good guest, which each test then breaks one way.
    export STUB_VERSION=10.9.5 STUB_BUILD=13F1911 STUB_AT_VERSION=2026.10.09
    export STUB_PKGS="com.apple.pkg.BaseSystemBinaries Wowfunhappy.AquaTransport Wowfunhappy.AquaTransport.ModernRootCertificates"
    export STUB_KEXTS="com.apple.kpi.bsd com.apple.driver.AppleACPIPlatform"
    unset STUB_CURL_FAIL STUB_INSTALLER_STATUS
}

verify() {  # $1 = AQUATRANSPORT_VERSION, or "-" to leave it unset
    if [ "$1" = - ]; then
        run env -u AQUATRANSPORT_VERSION PATH="$STUB:/usr/bin:/bin" MQG_VERIFY_CURL="$STUB/curl" \
            /bin/sh "$TMPL/verify.sh"
    else
        run env AQUATRANSPORT_VERSION="$1" PATH="$STUB:/usr/bin:/bin" MQG_VERIFY_CURL="$STUB/curl" \
            /bin/sh "$TMPL/verify.sh"
    fi
}

URLS="https://github.com/ https://release-assets.githubusercontent.com/ https://downloads.claude.ai/claude-code-releases/latest"

# --- verify.sh ---------------------------------------------------------------

@test "verify.sh passes a good guest, and says what it checked" {
    verify 2026.10.09
    [ "$status" -eq 0 ]
    [[ "$output" == *"verify: ok: 10.9.5 (13F1911), Aqua Transport 2026.10.09"* ]] || false
    [[ "$output" != *FAILED* ]] || false
}

@test "verify.sh fetches each of the three URLs with the system's own curl, quietly" {
    verify 2026.10.09
    [ "$status" -eq 0 ]
    for url in $URLS; do
        grep -qxF "curl -sS -o /dev/null $url" "$LOG"
    done
    [ "$(grep -c '^curl ' "$LOG")" -eq 3 ]
    run grep -F 'CURL=${MQG_VERIFY_CURL:-/usr/bin/curl}' "$TMPL/verify.sh"
    [ "$status" -eq 0 ]
}

@test "verify.sh prints key=value diagnostics before its verdict, even a failing one" {
    export STUB_VERSION=10.9.4
    verify 2026.10.09
    [ "$status" -ne 0 ]
    for line in "ProductVersion:" "aquatransport-asked=2026.10.09" "aquatransport-version=2026.10.09" \
        "receipts=Wowfunhappy.AquaTransport Wowfunhappy.AquaTransport.ModernRootCertificates" \
        "tap-kexts=" "curl=curl 7.30.0"; do
        [[ "$output" == *"$line"* ]] || { echo "missing: $line"; false; }
    done
    first_verdict=$(grep -n '^verify: ' <<< "$output" | head -n 1 | cut -d: -f1)
    last_diag=$(grep -n '^[a-z-]*=' <<< "$output" | tail -n 1 | cut -d: -f1)
    [ "$last_diag" -lt "$first_verdict" ]
}

@test "verify.sh fails a guest with another Aqua Transport version, naming both" {
    export STUB_AT_VERSION=2026.09.21
    verify 2026.10.09
    [ "$status" -eq 1 ]
    [[ "$output" == *"verify: FAILED: Wowfunhappy.AquaTransport is version '2026.09.21', not the pinned 2026.10.09"* ]] || false
}

@test "verify.sh fails a guest with no Aqua Transport receipt at all" {
    export STUB_PKGS="com.apple.pkg.BaseSystemBinaries Wowfunhappy.AquaTransport.ModernRootCertificates"
    verify 2026.10.09
    [ "$status" -eq 1 ]
    [[ "$output" == *"verify: FAILED: pkgutil has no Wowfunhappy.AquaTransport receipt: Aqua Transport is not installed"* ]] || false
    [ "$(grep -c 'verify: FAILED:' <<< "$output")" -eq 1 ]
}

@test "verify.sh fails a guest without ModernRootCertificates" {
    export STUB_PKGS="com.apple.pkg.BaseSystemBinaries Wowfunhappy.AquaTransport"
    verify 2026.10.09
    [ "$status" -eq 1 ]
    [[ "$output" == *"verify: FAILED: pkgutil --pkgs has no Wowfunhappy.AquaTransport.ModernRootCertificates receipt"* ]] || false
}

@test "verify.sh fails a guest with tap's receipt" {
    export STUB_PKGS="$STUB_PKGS net.sf.tuntaposx.tap"
    verify 2026.10.09
    [ "$status" -eq 1 ]
    [[ "$output" == *"verify: FAILED: pkgutil --pkgs has net.sf.tuntaposx.tap: installer_choice_3 was not deselected"* ]] || false
}

@test "verify.sh fails a guest with a tap kext loaded, naming it" {
    export STUB_KEXTS="$STUB_KEXTS net.sf.tuntaposx.tap"
    verify 2026.10.09
    [ "$status" -eq 1 ]
    [[ "$output" == *"verify: FAILED: kextstat lists a tap kext: "*"net.sf.tuntaposx.tap"* ]] || false
}

@test "verify.sh fails a guest that cannot fetch any one of the three URLs, naming it" {
    for url in $URLS; do
        export STUB_CURL_FAIL=$url
        verify 2026.10.09
        [ "$status" -eq 1 ]
        [[ "$output" == *"verify: FAILED: /usr/bin/curl -sS -o /dev/null $url fails: curl: (60) SSL certificate problem"* ]] || false
        [ "$(grep -c 'verify: FAILED:' <<< "$output")" -eq 1 ]
    done
}

@test "verify.sh fails a guest that is not 10.9.5, naming the version" {
    export STUB_VERSION=10.9.4
    verify 2026.10.09
    [ "$status" -eq 1 ]
    [[ "$output" == *"verify: FAILED: sw_vers -productVersion is '10.9.4', not 10.9.5"* ]] || false
}

@test "verify.sh fails a guest that is not build 13F1911, naming the build" {
    export STUB_BUILD=13F34
    verify 2026.10.09
    [ "$status" -eq 1 ]
    [[ "$output" == *"verify: FAILED: sw_vers -buildVersion is '13F34', not 13F1911"* ]] || false
}

@test "verify.sh fails when nobody said which version to expect" {
    verify -
    [ "$status" -eq 1 ]
    [[ "$output" == *"verify: FAILED: AQUATRANSPORT_VERSION is not set"* ]] || false
}

@test "verify.sh names every failing check, not just the first" {
    export STUB_VERSION=10.9.4 STUB_BUILD=13F34 STUB_AT_VERSION=2026.09.21
    export STUB_PKGS="Wowfunhappy.AquaTransport net.sf.tuntaposx.tap" STUB_KEXTS=net.sf.tuntaposx.tap
    export STUB_CURL_FAIL=https://github.com/
    verify 2026.10.09
    [ "$status" -eq 1 ]
    [ "$(grep -c 'verify: FAILED:' <<< "$output")" -eq 7 ]
    [[ "$output" == *"verify: 7 check(s) failed; this guest will not be packaged as a box"* ]] || false
}

@test "the template passes verify.sh the pinned version" {
    run grep -F 'environment_vars = ["AQUATRANSPORT_VERSION=${local.aquatransport_version}"]' \
        "$TMPL/mavericks-aquatransport.pkr.hcl"
    [ "$status" -eq 0 ]
}

# --- install.sh --------------------------------------------------------------

run_install() {
    DMG="$BATS_TEST_TMPDIR/AquaTransport.dmg"
    printf 'not really a disk image\n' > "$DMG"
    run env PATH="$STUB:/usr/bin:/bin" TMPDIR="$BATS_TEST_TMPDIR" MQG_AQUATRANSPORT_DMG="$DMG" \
        /bin/sh "$TMPL/install.sh"
}

@test "install.sh installs the package from the DMG onto /, with choice changes" {
    run_install
    [ "$status" -eq 0 ]
    run grep '^installer ' "$LOG"
    [ "${#lines[@]}" -eq 1 ]
    [[ "$output" == *" -applyChoiceChangesXML "* ]] || false
    [[ "$output" == *" -target /" ]] || false
    [[ "$output" == "installer -pkg $BATS_TEST_TMPDIR/"*"/AquaTransport.pkg "* ]] || false
}

@test "install.sh's choice changes deselect installer_choice_3, tap.kext, and nothing else" {
    run_install
    [ "$status" -eq 0 ]
    # The plist's one dict, its keys and values in order, tags stripped.
    got=$(sed -n -e '/<dict>/,/<\/dict>/p' "$LOG.choices" | sed -e 's/<[^>]*>//g' -e 's/^[[:space:]]*//' | grep -v '^$' | xargs echo)
    [ "$got" = "attributeSetting 0 choiceAttribute selected choiceIdentifier installer_choice_3" ]
    [ "$(grep -c '<dict>' "$LOG.choices")" -eq 1 ]
    grep -q '<integer>0</integer>' "$LOG.choices"
}

@test "install.sh mounts the DMG read-only and out of sight, then detaches it" {
    run_install
    [ "$status" -eq 0 ]
    attach=$(grep '^hdiutil attach ' "$LOG")
    [[ "$attach" == *" -nobrowse "* ]] || false
    [[ "$attach" == *" -readonly "* ]] || false
    [[ "$attach" == *" -mountpoint "* ]] || false
    [[ "$attach" == *" $DMG" ]] || false
    mnt=${attach#*-mountpoint }
    mnt=${mnt%% *}
    grep -qxF "hdiutil detach $mnt" "$LOG"
    [ ! -e "$mnt" ]
}

@test "install.sh removes the DMG and its choice changes afterwards" {
    run_install
    [ "$status" -eq 0 ]
    [ ! -e "$DMG" ]
    run find "$BATS_TEST_TMPDIR" -maxdepth 1 -name 'aquatransport*'
    [ -z "$output" ]
}

@test "install.sh fails when the installer does, and still detaches" {
    export STUB_INSTALLER_STATUS=1
    run_install
    [ "$status" -ne 0 ]
    grep -q '^hdiutil detach ' "$LOG"
}
