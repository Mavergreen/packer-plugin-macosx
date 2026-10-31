#!/usr/bin/env bats
# platform: host-agnostic -- tested on Linux with KVM only
#
# bin/gate-expected.sh prints the four versions a checkout pins. It reads
# files only: it never runs ssh, sudo or anything that reaches the guest or
# the host's power state, so these tests need no stubs.

bats_require_minimum_version 1.5.0

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
}

make_fixture() {
    local dir="$BATS_TEST_TMPDIR/fixture"
    mkdir -p "$dir/assets/pins" "$dir/templates/mavericks-aquatransport"
    printf '%s\t%s\t%s\n' \
        opencorepkg-src https://example.invalid/OpenCorePkg/archive/refs/tags/1.0.8.tar.gz aa \
        lilu-release https://example.invalid/Lilu/releases/download/1.7.3/Lilu-1.7.3-RELEASE.zip bb \
        virtualsmc-release https://example.invalid/VirtualSMC/releases/download/1.3.8/VirtualSMC-1.3.8-RELEASE.zip cc \
        > "$dir/assets/pins/sources.tsv"
    printf 'locals {\n  aquatransport_version = "2026.11.01"\n}\n' \
        > "$dir/templates/mavericks-aquatransport/aquatransport.pkr.hcl"
    printf '%s\n' "$dir"
}

@test "the real checkout's pins" {
    tsv=$REPO/assets/pins/sources.tsv
    oc=$(awk -F'\t' '$1=="opencorepkg-src"{print $2}' "$tsv" | sed 's|.*/||; s|\.tar\.gz$||')
    lilu=$(awk -F'\t' '$1=="lilu-release"{print $2}' "$tsv" | sed 's|/[^/]*$||; s|.*/||')
    vsmc=$(awk -F'\t' '$1=="virtualsmc-release"{print $2}' "$tsv" | sed 's|/[^/]*$||; s|.*/||')
    aqua=$(grep -o 'aquatransport_version *= *"[^"]*"' \
        "$REPO/templates/mavericks-aquatransport/aquatransport.pkr.hcl" | sed 's/.*"\(.*\)"/\1/')
    run "$REPO/bin/gate-expected.sh"
    [ "$status" -eq 0 ]
    [ "$output" = "opencore $oc
lilu $lilu
virtualsmc $vsmc
aquatransport $aqua" ]
}

@test "a fixture tree with other versions" {
    dir=$(make_fixture)
    run "$REPO/bin/gate-expected.sh" "$dir"
    [ "$status" -eq 0 ]
    [ "$output" = "opencore 1.0.8
lilu 1.7.3
virtualsmc 1.3.8
aquatransport 2026.11.01" ]
}

@test "a missing pin is named" {
    dir=$(make_fixture)
    grep -v '^lilu-release' "$dir/assets/pins/sources.tsv" > "$dir/x"
    mv "$dir/x" "$dir/assets/pins/sources.tsv"
    run --separate-stderr "$REPO/bin/gate-expected.sh" "$dir"
    [ "$status" -eq 1 ]
    [[ "$stderr" == *lilu-release* ]]
}

drop_pin() {
    grep -v "^$2" "$1/assets/pins/sources.tsv" > "$1/x"
    mv "$1/x" "$1/assets/pins/sources.tsv"
}

@test "a missing opencorepkg-src pin is named" {
    dir=$(make_fixture)
    drop_pin "$dir" opencorepkg-src
    run --separate-stderr "$REPO/bin/gate-expected.sh" "$dir"
    [ "$status" -eq 1 ]
    [[ "$stderr" == *opencorepkg-src* ]]
}

@test "a missing virtualsmc-release pin is named" {
    dir=$(make_fixture)
    drop_pin "$dir" virtualsmc-release
    run --separate-stderr "$REPO/bin/gate-expected.sh" "$dir"
    [ "$status" -eq 1 ]
    [[ "$stderr" == *virtualsmc-release* ]]
}

@test "a missing aquatransport.pkr.hcl is named" {
    dir=$(make_fixture)
    rm "$dir/templates/mavericks-aquatransport/aquatransport.pkr.hcl"
    run --separate-stderr "$REPO/bin/gate-expected.sh" "$dir"
    [ "$status" -eq 1 ]
    [[ "$stderr" == *aquatransport_version* ]]
}

@test "a missing aquatransport_version is named" {
    dir=$(make_fixture)
    printf 'locals {\n}\n' > "$dir/templates/mavericks-aquatransport/aquatransport.pkr.hcl"
    run --separate-stderr "$REPO/bin/gate-expected.sh" "$dir"
    [ "$status" -eq 1 ]
    [[ "$stderr" == *aquatransport_version* ]]
}

@test "a URL with no parseable version is named" {
    dir=$(make_fixture)
    sed 's|tags/1.0.8.tar.gz|tags/.tar.gz|' "$dir/assets/pins/sources.tsv" > "$dir/x"
    mv "$dir/x" "$dir/assets/pins/sources.tsv"
    run --separate-stderr "$REPO/bin/gate-expected.sh" "$dir"
    [ "$status" -eq 1 ]
    [[ "$stderr" == *"opencorepkg-src: no version"* ]]
}

@test "more than one argument is a usage error" {
    run --separate-stderr "$REPO/bin/gate-expected.sh" a b
    [ "$status" -eq 2 ]
    [[ "$stderr" == *usage:* ]]
}
