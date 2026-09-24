#!/usr/bin/env bats
#
# The version scheme: YYYYMMDD.N, the family's self-upstream shape.
# See docs/decisions/0012-version-scheme.md for why this and not
# <upstream>-mavericks.N.

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    # A throwaway repository, so tags can be planted without touching ours.
    DIR="$BATS_TEST_TMPDIR/vt"
    mkdir -p "$DIR/build"
    cp "$REPO/build/version.sh" "$DIR/build/"
    printf '20260922\n' > "$DIR/UPSTREAM_VERSION"
    git -C "$DIR" init -q
    git -C "$DIR" config user.email t@example.invalid
    git -C "$DIR" config user.name t
    git -C "$DIR" add -A
    git -C "$DIR" -c commit.gpgsign=false commit -qm first
}

tag() { git -C "$DIR" tag "$1"; }
ver() { ( cd "$DIR" && sh build/version.sh "$1" ); }

@test "a version line with no tag yet is .1, and it releases" {
    run ver auto
    [ "$status" -eq 0 ]
    [[ "$output" == *"FULL=20260922.1"* ]]
    [[ "$output" == *"TAG=20260922.1"* ]]
    [[ "$output" == *"RELEASE=yes"* ]]
}

@test "auto on an already-released line reports that version and does NOT release" {
    tag 20260922.1
    run ver auto
    [ "$status" -eq 0 ]
    [[ "$output" == *"FULL=20260922.1"* ]]
    [[ "$output" == *"RELEASE=no"* ]]
}

@test "local cuts the next N and releases -- this is the ingredient-bump path" {
    tag 20260922.1
    run ver local
    [ "$status" -eq 0 ]
    [[ "$output" == *"FULL=20260922.2"* ]]
    [[ "$output" == *"RELEASE=yes"* ]]
}

@test "N is compared numerically, not lexically" {
    # The bug this catches: .10 sorting before .2, so the eleventh release
    # of a day silently reuses .3. sort -V is not available on 10.9
    # (check-shell-portability.sh), so the comparison is arithmetic.
    tag 20260922.1
    tag 20260922.2
    tag 20260922.10
    run ver local
    [[ "$output" == *"FULL=20260922.11"* ]]
}

@test "tags from another date-line are not counted" {
    tag 20260801.7
    run ver auto
    [[ "$output" == *"FULL=20260922.1"* ]]
    [[ "$output" == *"RELEASE=yes"* ]]
}

@test "a tag with a non-numeric suffix is ignored rather than breaking the count" {
    tag 20260922.1
    tag 20260922.rc1
    run ver local
    [ "$status" -eq 0 ]
    [[ "$output" == *"FULL=20260922.2"* ]]
}

@test "VERSION is written, and is what FULL says" {
    run ver auto
    [ -f "$DIR/VERSION" ]
    run cat "$DIR/VERSION"
    [ "$output" = "20260922.1" ]
}

@test "VERSION_NO_WRITE=1 prints the same version and writes no VERSION" {
    # For reporting a version, which must work in a read-only checkout.
    # The release path does not set it; the test above covers that it
    # writes.
    run env VERSION_NO_WRITE=1 sh -c "cd '$DIR' && sh build/version.sh auto"
    [ "$status" -eq 0 ]
    [[ "$output" == *"FULL=20260922.1"* ]]
    [ ! -e "$DIR/VERSION" ]
}

@test "an empty UPSTREAM_VERSION fails loudly and names the file" {
    # The family's rule: artifacts named with nothing in front look almost
    # right. "20260922." with no N is the same defect one axis over.
    : > "$DIR/UPSTREAM_VERSION"
    run ver auto
    [ "$status" -ne 0 ]
    [[ "$output" == *"UPSTREAM_VERSION"* ]]
}

@test "a malformed UPSTREAM_VERSION fails loudly and shows what it read" {
    printf '1.2.3\n' > "$DIR/UPSTREAM_VERSION"
    run ver auto
    [ "$status" -ne 0 ]
    [[ "$output" == *"1.2.3"* ]]
    [[ "$output" == *"YYYYMMDD"* ]]
}

@test "an unknown mode is refused rather than guessed at" {
    run ver sometimes
    [ "$status" -ne 0 ]
    [[ "$output" == *"auto"* ]]
    [[ "$output" == *"local"* ]]
}

@test "VERSION is a build product and is not committed" {
    # Family-conventions check 7. A committed VERSION drifts from the tags
    # and makes the tag==VERSION release path impossible to satisfy.
    run git -C "$REPO" ls-files VERSION
    [ -z "$output" ]
    run grep -c '^/VERSION$' "$REPO/.gitignore"
    [ "$output" = "1" ]
}

@test "UPSTREAM_VERSION in this repository is a bare eight-digit date" {
    run cat "$REPO/UPSTREAM_VERSION"
    [[ "$output" =~ ^[0-9]{8}$ ]]
}
