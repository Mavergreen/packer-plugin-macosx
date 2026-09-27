#!/usr/bin/env bats
#
# template/verify.sh's verdict. It runs in the guest, over SSH, after the
# first boot; a wrong verdict either packages a broken guest as a box or
# fails a good build. These run it under this host's /bin/sh -- a stricter
# POSIX sh than 10.9's bash 3.2, which is the point -- with stubs for the
# macOS commands it judges by: sw_vers, sudo and pkgutil. Every other
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
    # sudo: passwordless when $STUB_SUDO is "ok", refused otherwise.
    cat > "$STUB/sudo" <<'EOF'
#!/bin/sh
[ "$STUB_SUDO" = ok ] || { echo "sudo: a password is required" >&2; exit 1; }
[ "$1" = -n ] && shift
exec "$@"
EOF
    # pkgutil --pkgs: the receipts in $STUB_PKGS, one per word.
    cat > "$STUB/pkgutil" <<'EOF'
#!/bin/sh
for p in $STUB_PKGS; do echo "$p"; done
EOF
    chmod +x "$STUB/sw_vers" "$STUB/sudo" "$STUB/pkgutil"

    # A good guest, which each test then breaks one way.
    export STUB_VERSION=10.9.5 STUB_SUDO=ok
    export STUB_PKGS="com.apple.pkg.BaseSystemBinaries com.apple.pkg.update.security.2016-004Mavericks.13F1911"
    date > "$MARKER"
}

verify() {  # $1 = UPDATES, or "-" to leave it unset
    if [ "$1" = - ]; then
        run env -u UPDATES PATH="$STUB:$PATH" MQG_VERIFY_MARKER="$MARKER" /bin/sh "$REPO/template/verify.sh"
    else
        run env UPDATES="$1" PATH="$STUB:$PATH" MQG_VERIFY_MARKER="$MARKER" /bin/sh "$REPO/template/verify.sh"
    fi
}

@test "verify.sh passes a good guest, and says what it checked" {
    verify security
    [ "$status" -eq 0 ]
    [[ "$output" == *"verify: ok: 10.9.5, first boot finished, passwordless sudo"* ]]
    [[ "$output" == *"updates=security"* ]]
    [[ "$output" != *FAILED* ]]
}

@test "verify.sh wants no update receipt when no updates were asked for" {
    export STUB_PKGS="com.apple.pkg.BaseSystemBinaries"
    verify none
    [ "$status" -eq 0 ]
}

@test "verify.sh prints everything before its verdict, even a failing one" {
    export STUB_VERSION=10.10
    verify security
    [ "$status" -ne 0 ]
    for line in "ProductVersion:" "hostname=" "id=" "receipts=" "updatepkgs=" \
        "firstboot-ran=" "diskbus=" "updates-asked=security"; do
        [[ "$output" == *"$line"* ]]
    done
}

@test "verify.sh fails a guest that is not 10.9.5, naming the version" {
    export STUB_VERSION=10.9.4
    verify security
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAILED: sw_vers -productVersion is '10.9.4', not 10.9.5"* ]]
}

@test "verify.sh fails a guest whose first boot never finished, naming the marker" {
    rm -f "$MARKER"
    verify security
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAILED: $MARKER does not exist: the first-boot payload never finished"* ]]
}

@test "verify.sh fails a guest without passwordless sudo, naming shutdown_command" {
    export STUB_SUDO=no
    verify security
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAILED: 'sudo -n true' fails for"* ]]
    [[ "$output" == *"shutdown_command 'sudo shutdown -h now' cannot run"* ]]
}

@test "verify.sh fails a guest missing the security update it was asked for" {
    export STUB_PKGS="com.apple.pkg.BaseSystemBinaries"
    for updates in security all; do
        verify "$updates"
        [ "$status" -eq 1 ]
        [[ "$output" == *"FAILED: updates=$updates, but pkgutil --pkgs has no com.apple.pkg.update.security.2016-004Mavericks.* receipt"* ]]
    done
}

@test "verify.sh fails when nobody said which updates to expect" {
    verify -
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAILED: UPDATES is not set"* ]]
    [[ "$output" == *'environment_vars = ["UPDATES=${var.updates}"]'* ]]

    verify bogus
    [ "$status" -eq 1 ]
    [[ "$output" == *"FAILED: UPDATES is 'bogus', not one of none, security, all"* ]]
}

@test "verify.sh names every failing check, not just the first" {
    export STUB_VERSION=10.9.4 STUB_SUDO=no STUB_PKGS=""
    rm -f "$MARKER"
    verify security
    [ "$status" -eq 1 ]
    [ "$(grep -c 'verify: FAILED:' <<< "$output")" -eq 4 ]
    [[ "$output" == *"verify: 4 check(s) failed; this guest will not be packaged as a box"* ]]
}

@test "the template passes verify.sh the updates it asked for" {
    run grep -F 'environment_vars = ["UPDATES=${var.updates}"]' "$REPO/template/mavericks.pkr.hcl"
    [ "$status" -eq 0 ]
}
