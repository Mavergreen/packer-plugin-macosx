#!/usr/bin/env bats
# platform: host-agnostic

# bin/mirror-aquatransport.sh: one release of Mavergreen/aquatransport-mirror per (version, digest),
# never replaced, carrying the DMG and the upstream source it was most likely built from. gh is a
# stub on PATH that logs its arguments, answers upstream's main tip and tarball, and keeps the
# "release" as a JSON file in $BATS_TEST_TMPDIR; no test touches the network.

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
case "$1 $2" in
"api repos/Wowfunhappy/AquaTransport/commits/main")
    echo "$STUB_SHA" ;;
"api repos/Wowfunhappy/AquaTransport/tarball/"*)
    cat "$STUB_TARBALL" ;;
"api repos/"*)
    [ -f "$STUB_RELEASE" ] || { echo "gh: Not Found (HTTP 404)" >&2; exit 1; }
    cat "$STUB_RELEASE" ;;
"release create"|"release upload")
    mode=$2 tag=$3; shift 3
    [ "$mode" = upload ] || rm -f "$STUB_RELEASE"
    python3 -I -c '
import hashlib, json, os, sys
p, tag = sys.argv[1], sys.argv[2]
r = json.load(open(p)) if os.path.exists(p) else {"tag_name": tag, "assets": []}
files = sys.argv[3:]
for f in files[:files.index("--repo")] if "--repo" in files else files:
    r["assets"].append({"name": os.path.basename(f), "digest": "sha256:" + hashlib.sha256(open(f, "rb").read()).hexdigest()})
json.dump(r, open(p, "w"))
' "$STUB_RELEASE" "$tag" "$@" ;;
*) echo "gh stub: unexpected: $*" >&2; exit 2 ;;
esac
STUB
    chmod +x "$STUBS/gh"
    export STUB_SHA=0123456789abcdef0123456789abcdef01234567
    export STUB_TARBALL="$BATS_TEST_TMPDIR/upstream.tar.gz"
    mkdir -p "$BATS_TEST_TMPDIR/up/Wowfunhappy-AquaTransport-0123456/deps/owl"
    echo 'GNU GENERAL PUBLIC LICENSE' > "$BATS_TEST_TMPDIR/up/Wowfunhappy-AquaTransport-0123456/deps/owl/COPYING"
    tar czf "$STUB_TARBALL" -C "$BATS_TEST_TMPDIR/up" Wowfunhappy-AquaTransport-0123456
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

# release_with ASSET...: the stub's release for $(tag), each ASSET "name" (the DMG gets $DIGEST).
release_with() {
    python3 -I -c '
import json, sys
tag, digest, names = sys.argv[1], sys.argv[2], sys.argv[3:]
print(json.dumps({"tag_name": tag, "assets": [{"name": n, "digest": "sha256:" + (digest if n == "AquaTransport.dmg" else "0" * 64)} for n in names]}))
' "$(tag)" "$DIGEST" "$@" > "$STUB_RELEASE"
}

SRC="AquaTransport-source-0123456789abcdef0123456789abcdef01234567.tar.gz"


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

@test "a new tag creates one release, titled the tag, notes naming the full digest and the date, with the DMG and main's source" {
    run "$SCRIPT" "$BATS_TEST_TMPDIR/observed.json" "$DMG"
    [ "$status" -eq 0 ]
    [ "$(grep -c '^release create ' "$GH_LOG")" -eq 1 ]
    ! grep -q '^release upload ' "$GH_LOG" || false
    line=$(grep '^release create ' "$GH_LOG")
    [[ "$line" == "release create $(tag) $DMG "*"/$SRC --repo Mavergreen/aquatransport-mirror --title $(tag) --notes "* ]] || false
    [[ "$line" == *"SHA-256: $DIGEST"* ]] || false
    [[ "$line" == *"First seen: $(date -u +%Y-%m-%d)"* ]] || false
    [[ "$line" == *"GPLv3"* ]] || false
    [[ "$line" == *"Source: https://github.com/Wowfunhappy/AquaTransport/tree/$STUB_SHA"* ]] || false
    [[ "$line" == *"tip of main when this download was first seen"* ]] || false
    [[ "$line" == *"not a proven build commit"* ]] || false
    python3 -I -c 'import json,sys; a=[x["name"] for x in json.load(open(sys.argv[1]))["assets"]]; assert sorted(a)==sorted(["AquaTransport.dmg",sys.argv[2]]), a' "$STUB_RELEASE" "$SRC"
}

@test "--source-commit uses that commit and never asks for main's tip" {
    other=89abcdef0123456789abcdef0123456789abcdef
    run "$SCRIPT" --source-commit "$other" "$BATS_TEST_TMPDIR/observed.json" "$DMG"
    [ "$status" -eq 0 ]
    ! grep -q 'commits/main' "$GH_LOG" || false
    grep -q "^api repos/Wowfunhappy/AquaTransport/tarball/$other\$" "$GH_LOG"
    grep '^release create ' "$GH_LOG" | grep -q "/AquaTransport-source-$other.tar.gz "
}

@test "a --source-commit that is not 40 hex digits fails before any gh call" {
    run "$SCRIPT" --source-commit b2d04d3 "$BATS_TEST_TMPDIR/observed.json" "$DMG"
    [ "$status" -ne 0 ]
    [ ! -s "$GH_LOG" ]
}

@test "a source tarball without deps/owl/COPYING fails and writes nothing" {
    rm -rf "$BATS_TEST_TMPDIR/up"; mkdir -p "$BATS_TEST_TMPDIR/up/x"; echo hi > "$BATS_TEST_TMPDIR/up/x/README"
    tar czf "$STUB_TARBALL" -C "$BATS_TEST_TMPDIR/up" x
    run "$SCRIPT" "$BATS_TEST_TMPDIR/observed.json" "$DMG"
    [ "$status" -eq 1 ]
    [[ "$output" == *"deps/owl/COPYING"* ]] || false
    ! grep -Eq '^release ' "$GH_LOG" || false
}

@test "a main tip that is not a commit id fails and writes nothing" {
    export STUB_SHA=oops
    run "$SCRIPT" "$BATS_TEST_TMPDIR/observed.json" "$DMG"
    [ "$status" -eq 1 ]
    ! grep -Eq '^release |tarball' "$GH_LOG" || false
}

@test "an existing tag whose DMG digest matches and that has its source is a no-op that fetches nothing" {
    release_with AquaTransport.dmg "AquaTransport-source-$STUB_SHA.tar.gz"
    run "$SCRIPT" "$BATS_TEST_TMPDIR/observed.json" "$DMG"
    [ "$status" -eq 0 ]
    ! grep -Eq '^release |commits/main|tarball' "$GH_LOG" || false
}

@test "an existing tag with the DMG but no source uploads only the source" {
    release_with AquaTransport.dmg
    run "$SCRIPT" "$BATS_TEST_TMPDIR/observed.json" "$DMG"
    [ "$status" -eq 0 ]
    [ "$(grep -c '^release upload ' "$GH_LOG")" -eq 1 ]
    grep '^release upload ' "$GH_LOG" | grep -q "^release upload $(tag) [^ ]*/$SRC --repo Mavergreen/aquatransport-mirror\$"
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

@test "an existing tag with no assets uploads both with gh release upload, without --clobber" {
    release_with
    run "$SCRIPT" "$BATS_TEST_TMPDIR/observed.json" "$DMG"
    [ "$status" -eq 0 ]
    [ "$(grep -c '^release upload ' "$GH_LOG")" -eq 1 ]
    grep '^release upload ' "$GH_LOG" | grep -q "^release upload $(tag) $DMG [^ ]*/$SRC --repo Mavergreen/aquatransport-mirror\$"
    ! grep -q '^release create ' "$GH_LOG" || false
}

@test "a write that does not leave the observed digest on the release fails" {
    cat > "$STUBS/gh" <<'STUB'
#!/bin/sh
printf '%s\n' "$*" >> "$GH_LOG"
case "$1 $2" in
"api repos/Wowfunhappy/AquaTransport/commits/main") echo "$STUB_SHA" ;;
"api repos/Wowfunhappy/AquaTransport/tarball/"*) cat "$STUB_TARBALL" ;;
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

@test "a write that leaves the DMG but no source asset fails" {
    cat > "$STUBS/gh" <<'STUB'
#!/bin/sh
printf '%s\n' "$*" >> "$GH_LOG"
case "$1 $2" in
"api repos/Wowfunhappy/AquaTransport/commits/main") echo "$STUB_SHA" ;;
"api repos/Wowfunhappy/AquaTransport/tarball/"*) cat "$STUB_TARBALL" ;;
"api repos/"*)
    [ -f "$STUB_RELEASE" ] || { echo "gh: Not Found (HTTP 404)" >&2; exit 1; }
    cat "$STUB_RELEASE" ;;
"release create")
    printf '{"tag_name":"%s","assets":[{"name":"AquaTransport.dmg","digest":"sha256:%s"}]}\n' "$3" "$(sha256sum "$4" | cut -d' ' -f1)" > "$STUB_RELEASE" ;;
esac
STUB
    run "$SCRIPT" "$BATS_TEST_TMPDIR/observed.json" "$DMG"
    [ "$status" -eq 1 ]
    [[ "$output" == *"source"* ]] || false
}

@test "UPSTREAM_REPO overrides where the source comes from" {
    UPSTREAM_REPO=Someone/fork run "$SCRIPT" "$BATS_TEST_TMPDIR/observed.json" "$DMG"
    grep -q '^api repos/Someone/fork/commits/main' "$GH_LOG"
    ! grep -q 'Wowfunhappy' "$GH_LOG" || false
}

@test "an existing DMG asset with no digest fails, saying so, and writes nothing" {
    printf '{"tag_name":"%s","assets":[{"name":"AquaTransport.dmg","digest":null}]}\n' "$(tag)" > "$STUB_RELEASE"
    run "$SCRIPT" "$BATS_TEST_TMPDIR/observed.json" "$DMG"
    [ "$status" -eq 1 ]
    [[ "$output" == *"AquaTransport.dmg with no digest"* ]] || false
    ! grep -Eq '^release ' "$GH_LOG" || false
}

@test "a DMG under any local name is uploaded as AquaTransport.dmg" {
    cp "$DMG" "$BATS_TEST_TMPDIR/7b91d3e4531e.dmg"
    run "$SCRIPT" "$BATS_TEST_TMPDIR/observed.json" "$BATS_TEST_TMPDIR/7b91d3e4531e.dmg"
    [ "$status" -eq 0 ]
    python3 -I -c 'import json,sys; a=[x["name"] for x in json.load(open(sys.argv[1]))["assets"]]; assert "AquaTransport.dmg" in a, a' "$STUB_RELEASE"
}
