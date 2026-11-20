#!/usr/bin/env bats
# platform: host-agnostic

# bin/mirror-aquatransport.sh: one release of Mavergreen/aquatransport-mirror per (version, digest),
# never replaced. gh is a stub on PATH that logs its arguments and keeps the "release" as a JSON
# file in $BATS_TEST_TMPDIR; no test touches the network.

REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
SCRIPT="$REPO/bin/mirror-aquatransport.sh"

setup() {
    STUBS="$BATS_TEST_TMPDIR/stubs"
    mkdir -p "$STUBS"
    export GH_LOG="$BATS_TEST_TMPDIR/gh.log"
    export STUB_RELEASE="$BATS_TEST_TMPDIR/release.json"
    : > "$GH_LOG"
    cat > "$STUBS/gh" <<'STUB'
#!/bin/sh
printf '%s\n' "$*" | tr '\n' ' ' | sed 's/ $//' >> "$GH_LOG"; echo >> "$GH_LOG"
asset_json() { printf '{"tag_name":"%s","assets":[{"name":"AquaTransport.dmg","digest":"sha256:%s"}]}\n' "$1" "$(sha256sum "$2" | cut -d' ' -f1)"; }
case "$1 $2" in
"api repos/"*)
    [ -f "$STUB_RELEASE" ] || { echo "gh: Not Found (HTTP 404)" >&2; exit 1; }
    cat "$STUB_RELEASE" ;;
"release create")
    asset_json "$3" "$4" > "$STUB_RELEASE" ;;
"release upload")
    asset_json "$3" "$4" > "$STUB_RELEASE" ;;
*) echo "gh stub: unexpected: $*" >&2; exit 2 ;;
esac
STUB
    chmod +x "$STUBS/gh"
    export PATH="$STUBS:$PATH"
    export GH_TOKEN=test-token
    DMG="$BATS_TEST_TMPDIR/AquaTransport.dmg"
    printf 'pretend dmg bytes\n' > "$DMG"
    DIGEST=$(sha256sum "$DMG" | cut -d' ' -f1)
    observed 2026.11.17 "$DIGEST"
}

observed() {
    printf '{"releases":[{"version":"%s","digest":"%s"}]}\n' "$1" "$2" > "$BATS_TEST_TMPDIR/observed.json"
}

tag() { printf '2026.11.17-%s' "$(printf '%s' "$DIGEST" | cut -c1-12)"; }

@test "mirror_tag is the version and the digest's first 12 characters" {
    MIRROR_AQUATRANSPORT_LIB=1 run sh -c '. "$1"; mirror_tag 2026.11.17 933d5d34d3026b945f7c7df0485eca8abee16cb3022a463b90d9498705bb5568' sh "$SCRIPT"
    [ "$status" -eq 0 ]
    [ "$output" = "2026.11.17-933d5d34d302" ]
}

@test "a new digest for a known version is its own tag" {
    MIRROR_AQUATRANSPORT_LIB=1 run sh -c '. "$1"; mirror_tag 2026.11.17 aaaaaaaaaaaabbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' sh "$SCRIPT"
    [ "$output" = "2026.11.17-aaaaaaaaaaaa" ]
}

@test "a DMG whose sha256 is not the observed digest fails, and calls gh nothing that writes" {
    observed 2026.11.17 cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc
    run "$SCRIPT" "$BATS_TEST_TMPDIR/observed.json" "$DMG"
    [ "$status" -eq 1 ]
    [[ "$output" == *"$DIGEST"* ]] || false
    ! grep -Eq '^release ' "$GH_LOG" || false
}

@test "a new tag creates one release, titled the tag, notes naming the full digest and the date, with AquaTransport.dmg" {
    run "$SCRIPT" "$BATS_TEST_TMPDIR/observed.json" "$DMG"
    [ "$status" -eq 0 ]
    [ "$(grep -c '^release create ' "$GH_LOG")" -eq 1 ]
    line=$(grep '^release create ' "$GH_LOG")
    [[ "$line" == "release create $(tag) $DMG --repo Mavergreen/aquatransport-mirror --title $(tag) --notes "* ]] || false
    [[ "$line" == *"SHA-256: $DIGEST"* ]] || false
    [[ "$line" == *"First seen: $(date -u +%Y-%m-%d)"* ]] || false
}

@test "an existing tag whose asset digest matches is a no-op that succeeds" {
    printf '{"tag_name":"%s","assets":[{"name":"AquaTransport.dmg","digest":"sha256:%s"}]}\n' "$(tag)" "$DIGEST" > "$STUB_RELEASE"
    run "$SCRIPT" "$BATS_TEST_TMPDIR/observed.json" "$DMG"
    [ "$status" -eq 0 ]
    ! grep -Eq '^release ' "$GH_LOG" || false
}

@test "an existing tag whose asset digest differs fails, naming both digests, and never deletes or uploads" {
    bad=dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd
    printf '{"tag_name":"%s","assets":[{"name":"AquaTransport.dmg","digest":"sha256:%s"}]}\n' "$(tag)" "$bad" > "$STUB_RELEASE"
    run "$SCRIPT" "$BATS_TEST_TMPDIR/observed.json" "$DMG"
    [ "$status" -eq 1 ]
    [[ "$output" == *"$DIGEST"* ]] || false
    [[ "$output" == *"$bad"* ]] || false
    ! grep -Eq '^release ' "$GH_LOG" || false
}

@test "an existing tag with no asset uploads it with gh release upload, without --clobber" {
    printf '{"tag_name":"%s","assets":[]}\n' "$(tag)" > "$STUB_RELEASE"
    run "$SCRIPT" "$BATS_TEST_TMPDIR/observed.json" "$DMG"
    [ "$status" -eq 0 ]
    [ "$(grep -c '^release upload ' "$GH_LOG")" -eq 1 ]
    grep -qx "release upload $(tag) $DMG --repo Mavergreen/aquatransport-mirror" "$GH_LOG"
    ! grep -q '^release create ' "$GH_LOG" || false
}

@test "a write that does not leave the observed digest on the release fails" {
    cat > "$STUBS/gh" <<'STUB'
#!/bin/sh
printf '%s\n' "$*" >> "$GH_LOG"
case "$1 $2" in
"api repos/"*)
    [ -f "$STUB_RELEASE" ] || { echo "gh: Not Found (HTTP 404)" >&2; exit 1; }
    cat "$STUB_RELEASE" ;;
"release create")
    printf '{"tag_name":"%s","assets":[]}\n' "$3" > "$STUB_RELEASE" ;;
esac
STUB
    run "$SCRIPT" "$BATS_TEST_TMPDIR/observed.json" "$DMG"
    [ "$status" -eq 1 ]
}

@test "a gh read failure other than not-found fails without writing" {
    cat > "$STUBS/gh" <<'STUB'
#!/bin/sh
printf '%s\n' "$*" >> "$GH_LOG"
echo "gh: Bad credentials (HTTP 401)" >&2
exit 1
STUB
    run "$SCRIPT" "$BATS_TEST_TMPDIR/observed.json" "$DMG"
    [ "$status" -eq 1 ]
    ! grep -Eq '^release ' "$GH_LOG" || false
}

@test "--first-seen and --note reach the release notes" {
    run "$SCRIPT" --first-seen 2026-10-25 --note "(backfilled 2026-11-20 from a build cache)" "$BATS_TEST_TMPDIR/observed.json" "$DMG"
    [ "$status" -eq 0 ]
    line=$(grep '^release create ' "$GH_LOG")
    [[ "$line" == *"First seen: 2026-10-25"* ]] || false
    [[ "$line" == *"(backfilled 2026-11-20 from a build cache)"* ]] || false
}

@test "a malformed --first-seen fails before any gh call" {
    run "$SCRIPT" --first-seen 25/10/2026 "$BATS_TEST_TMPDIR/observed.json" "$DMG"
    [ "$status" -ne 0 ]
    [ ! -s "$GH_LOG" ]
}

@test "observed JSON with other than one valid release fails before any gh call" {
    printf '{"releases":[]}\n' > "$BATS_TEST_TMPDIR/observed.json"
    run "$SCRIPT" "$BATS_TEST_TMPDIR/observed.json" "$DMG"
    [ "$status" -ne 0 ]
    [ ! -s "$GH_LOG" ]
}

@test "MIRROR_REPO overrides the repo" {
    MIRROR_REPO=Someone/else run "$SCRIPT" "$BATS_TEST_TMPDIR/observed.json" "$DMG"
    [ "$status" -eq 0 ]
    grep -q -- '--repo Someone/else' "$GH_LOG"
    grep -q '^api repos/Someone/else/releases/tags/' "$GH_LOG"
}

@test "the script never deletes a release or clobbers an asset" {
    ! grep -Eq -- 'release delete|--clobber|-X *DELETE|--method *DELETE' "$SCRIPT" || false
}
