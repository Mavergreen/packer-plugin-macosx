#!/usr/bin/env bats
#
# bin/verify-changed-sources.sh diffs assets/pins/sources.tsv against a
# base ref and re-fetches only the pins that changed.

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
}

# A throwaway git repository carrying just enough of the tree for
# bin/verify-changed-sources.sh to run: the script itself, the two lib
# files it sources, and a registry pinning one file:// "upstream" at the
# given checksum.
make_repo() {
    local dir="$BATS_TEST_TMPDIR/planted"
    mkdir -p "$dir/bin" "$dir/lib" "$dir/assets/pins"
    cp "$REPO/bin/verify-changed-sources.sh" "$dir/bin/"
    cp "$REPO/lib/common.sh" "$dir/lib/"
    cp "$REPO/lib/vendor.sh" "$dir/lib/"
    printf '%s\n' \
        "thing	file://$dir/upstream.zip	$1" \
        > "$dir/assets/pins/sources.tsv"
    printf 'payload\n' > "$dir/upstream.zip"
    git -C "$dir" init -q
    git -C "$dir" config user.email t@example.invalid
    git -C "$dir" config user.name t
    git -C "$dir" add -A
    git -C "$dir" -c commit.gpgsign=false commit -qm "$2"
    printf '%s\n' "$dir"
}

@test "verify-changed-sources fetches and verifies a pin that changed" {
    dir="$(make_repo wrongsum "unpinned")"
    got=$(sha256sum "$dir/upstream.zip" | cut -d' ' -f1)
    base=$(git -C "$dir" rev-parse HEAD)

    printf '%s\n' "thing	file://$dir/upstream.zip	$got" \
        > "$dir/assets/pins/sources.tsv"
    git -C "$dir" add -A
    git -C "$dir" -c commit.gpgsign=false commit -qm "pin"

    run bash -c "cd '$dir' && ./bin/verify-changed-sources.sh '$base'"
    [ "$status" -eq 0 ]
    [[ "$output" == *"verifying thing"* ]]
}

@test "verify-changed-sources fails a changed pin whose checksum is wrong" {
    dir="$(make_repo PLACEHOLDER "first")"
    base=$(git -C "$dir" rev-parse HEAD)

    sed -i "s/PLACEHOLDER/$(printf '0%.0s' $(seq 64))/" "$dir/assets/pins/sources.tsv"
    git -C "$dir" add -A
    git -C "$dir" -c commit.gpgsign=false commit -qm "a wrong pin"

    run bash -c "cd '$dir' && ./bin/verify-changed-sources.sh '$base'"
    [ "$status" -ne 0 ]
    [[ "$output" == *"checksum mismatch"* ]]
}

@test "verify-changed-sources fetches nothing when no pin moved" {
    dir="$(make_repo PLACEHOLDER "first")"
    got=$(sha256sum "$dir/upstream.zip" | cut -d' ' -f1)
    sed -i "s/PLACEHOLDER/$got/" "$dir/assets/pins/sources.tsv"
    git -C "$dir" add -A
    git -C "$dir" -c commit.gpgsign=false commit -qm "pin the checksum"
    base=$(git -C "$dir" rev-parse HEAD)

    printf 'unrelated\n' > "$dir/README"
    git -C "$dir" add -A
    git -C "$dir" -c commit.gpgsign=false commit -qm "something else"

    run bash -c "cd '$dir' && ./bin/verify-changed-sources.sh '$base'"
    [ "$status" -eq 0 ]
    [[ "$output" == *"nothing to fetch"* ]]
}
