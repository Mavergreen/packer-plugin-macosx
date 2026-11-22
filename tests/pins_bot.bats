#!/usr/bin/env bats
# platform: host-agnostic
# shellcheck disable=SC2154  # $stderr is set by bats run --separate-stderr
#
# bin/pins-bot.sh: what .github/workflows/pins-bot.yml computes and writes on
# a Renovate branch. fill downloads the boot stack's moved sources and prints
# their checksums; apply writes those into sources.tsv; sync carries every
# moved pin into the goldens and the ingredient digest.

bats_require_minimum_version 1.5.0

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
}

# A throwaway repository holding just what bin/pins-bot.sh reads and writes:
# the script, the lib files and build/msc.sh it sources, a registry, a
# component version, and the goldens that repeat them. Its one commit is the
# base a Renovate branch would start from.
make_repo() {
    local dir="$BATS_TEST_TMPDIR/planted" g
    mkdir -p "$dir/bin" "$dir/lib" "$dir/build" "$dir/assets/pins" \
        "$dir/components/openssh" "$dir/internal/pins/testdata/golden" \
        "$dir/internal/inputs/testdata/golden"
    cp "$REPO/bin/pins-bot.sh" "$dir/bin/"
    cp "$REPO/lib/common.sh" "$REPO/lib/vendor.sh" "$REPO/lib/sources.sh" "$dir/lib/"
    cp "$REPO/build/msc.sh" "$dir/build/"
    printf 'v1\n' > "$dir/lilu-1.zip"
    printf 'v2\n' > "$dir/lilu-2.zip"
    printf 'mac\n' > "$dir/apple.pkg"
    LILU1=$(sha256sum "$dir/lilu-1.zip" | cut -d' ' -f1)
    LILU2=$(sha256sum "$dir/lilu-2.zip" | cut -d' ' -f1)
    APPLE=$(sha256sum "$dir/apple.pkg" | cut -d' ' -f1)
    printf '%s\n' \
        "# name	url	sha256" \
        "lilu-release	file://$dir/lilu-1.zip	$LILU1" \
        "apple-thing	file://$dir/apple.pkg	$APPLE" \
        > "$dir/assets/pins/sources.tsv"
    printf '1.0p1-mavericks.1\n' > "$dir/components/openssh/version"
    g=$dir/internal/pins/testdata/golden
    printf '%s\n' "apple-thing	$APPLE" "lilu-release	$LILU1" "openssh	1.0p1-mavericks.1" > "$g/ingredients.txt"
    sha256sum "$g/ingredients.txt" | cut -d' ' -f1 > "$g/ingredients-digest.txt"
    printf '%s\n' "package pins" "" "func TestTheIngredientDigestIsPinned(t *testing.T) {" \
        "	const want = \"$(cat "$g/ingredients-digest.txt")\"" "}" > "$dir/internal/pins/pins_test.go"
    g=$dir/internal/inputs/testdata/golden
    printf '%s\n' "source-url:lilu-release	file://$dir/lilu-1.zip" "source:lilu-release	$LILU1" \
        "component:openssh	1.0p1-mavericks.1" "openssh-enabled	1" > "$g/listing-efi.txt"
    # A fixture whose rows differ from the pins on purpose: sync never touches it.
    printf '%s\n' "source-url:lilu-release	" "source:lilu-release	feed" > "$g/listing-empty-url.txt"
    git -C "$dir" init -q
    git -C "$dir" config user.email t@example.invalid
    git -C "$dir" config user.name t
    git -C "$dir" add -A
    git -C "$dir" -c commit.gpgsign=false commit -qm base
    BASE=$(git -C "$dir" rev-parse HEAD)
    DIR=$dir
}

# What Renovate does to a Lilu bump: the URL moves, the checksum does not.
renovate_moves_lilu() {
    sed -i "s|lilu-1.zip|lilu-2.zip|" "$DIR/assets/pins/sources.tsv"
    git -C "$DIR" -c commit.gpgsign=false commit -qam renovate
}

@test "fill prints the moved boot-stack source's real checksum" {
    make_repo
    renovate_moves_lilu
    run --separate-stderr "$DIR/bin/pins-bot.sh" fill "$BASE"
    [ "$status" -eq 0 ]
    [ "$output" = "lilu-release=$LILU2" ] || { echo "$output"; false; }
}

@test "fill prints nothing when no source moved" {
    make_repo
    run --separate-stderr "$DIR/bin/pins-bot.sh" fill "$BASE"
    [ "$status" -eq 0 ]
    [ -z "$output" ]
}

@test "fill prints nothing when the moved source is already pinned" {
    make_repo
    sed -i "s|lilu-1.zip	$LILU1|lilu-2.zip	$LILU2|" "$DIR/assets/pins/sources.tsv"
    git -C "$DIR" -c commit.gpgsign=false commit -qam pinned
    run --separate-stderr "$DIR/bin/pins-bot.sh" fill "$BASE"
    [ "$status" -eq 0 ]
    [ -z "$output" ]
}

@test "fill refuses a moved source that is not the boot stack's, and fetches nothing" {
    make_repo
    rm "$DIR/apple.pkg"
    printf 'mac2\n' > "$DIR/apple2.pkg"
    sed -i "s|apple.pkg|apple2.pkg|" "$DIR/assets/pins/sources.tsv"
    git -C "$DIR" -c commit.gpgsign=false commit -qam apple
    run --separate-stderr "$DIR/bin/pins-bot.sh" fill "$BASE"
    [ "$status" -eq 1 ]
    [ -z "$output" ]
    [[ "$stderr" == *"apple-thing"* ]] || false
}

@test "fill fails, printing nothing, when a download fails" {
    make_repo
    sed -i "s|lilu-1.zip|lilu-9.zip|" "$DIR/assets/pins/sources.tsv"
    git -C "$DIR" -c commit.gpgsign=false commit -qam missing
    SHIPYARD_FETCH_TRIES=1 run --separate-stderr "$DIR/bin/pins-bot.sh" fill "$BASE"
    [ "$status" -eq 1 ]
    [ -z "$output" ]
}

@test "fill fails when the base is not a revision" {
    make_repo
    run --separate-stderr "$DIR/bin/pins-bot.sh" fill no-such-ref
    [ "$status" -eq 1 ]
    [ -z "$output" ]
}

@test "apply writes only the named row's checksum" {
    make_repo
    renovate_moves_lilu
    before=$(cat "$DIR/assets/pins/sources.tsv")
    run "$DIR/bin/pins-bot.sh" apply "lilu-release=$LILU2"
    [ "$status" -eq 0 ]
    [ "$output" = changed ]
    want=${before/	$LILU1/	$LILU2}
    [ "$(cat "$DIR/assets/pins/sources.tsv")" = "$want" ]
}

@test "apply refuses a name outside the boot stack, a bad value or a repeat, writing nothing" {
    make_repo
    before=$(cat "$DIR/assets/pins/sources.tsv")
    for args in "apple-thing=$LILU2" "lilu-release=xyz" "lilu-release=$LILU2 lilu-release=$LILU2" "nothing"; do
        # shellcheck disable=SC2086  # each case is several words on purpose
        run "$DIR/bin/pins-bot.sh" apply $args
        [ "$status" -eq 1 ] || { echo "allowed: $args"; false; }
    done
    [ "$(cat "$DIR/assets/pins/sources.tsv")" = "$before" ]
}

@test "sync carries a moved checksum and URL into the goldens and the digest" {
    make_repo
    renovate_moves_lilu
    "$DIR/bin/pins-bot.sh" apply "lilu-release=$LILU2" >/dev/null
    run "$DIR/bin/pins-bot.sh" sync "$BASE"
    [ "$status" -eq 0 ]
    [ "$output" = changed ]
    g=$DIR/internal/pins/testdata/golden
    grep -qx "lilu-release	$LILU2" "$g/ingredients.txt"
    digest=$(sha256sum "$g/ingredients.txt" | cut -d' ' -f1)
    [ "$(cat "$g/ingredients-digest.txt")" = "$digest" ]
    grep -qx "	const want = \"$digest\"" "$DIR/internal/pins/pins_test.go"
    l=$DIR/internal/inputs/testdata/golden/listing-efi.txt
    grep -qx "source:lilu-release	$LILU2" "$l"
    grep -qx "source-url:lilu-release	file://$DIR/lilu-2.zip" "$l"
}

@test "sync carries a moved component version into the goldens and the digest" {
    make_repo
    printf '1.0p2-mavericks.2\n' > "$DIR/components/openssh/version"
    git -C "$DIR" -c commit.gpgsign=false commit -qam renovate
    run "$DIR/bin/pins-bot.sh" sync "$BASE"
    [ "$output" = changed ]
    grep -qx "openssh	1.0p2-mavericks.2" "$DIR/internal/pins/testdata/golden/ingredients.txt"
    grep -qx "component:openssh	1.0p2-mavericks.2" "$DIR/internal/inputs/testdata/golden/listing-efi.txt"
    grep -qx "openssh-enabled	1" "$DIR/internal/inputs/testdata/golden/listing-efi.txt"
}

@test "sync leaves a fixture whose rows are not the pinned values alone" {
    make_repo
    before=$(cat "$DIR/internal/inputs/testdata/golden/listing-empty-url.txt")
    renovate_moves_lilu
    "$DIR/bin/pins-bot.sh" apply "lilu-release=$LILU2" >/dev/null
    "$DIR/bin/pins-bot.sh" sync "$BASE" >/dev/null
    [ "$(cat "$DIR/internal/inputs/testdata/golden/listing-empty-url.txt")" = "$before" ]
}

@test "sync is unchanged, and writes nothing, when no pin moved" {
    make_repo
    run "$DIR/bin/pins-bot.sh" sync "$BASE"
    [ "$status" -eq 0 ]
    [ "$output" = unchanged ]
    [ -z "$(git -C "$DIR" status --porcelain)" ]
}

@test "sync on a clone of this repository changes nothing" {
    # Every golden already agrees with the pins at HEAD, so a sync against
    # HEAD itself is the identity: the one real-tree check that the files it
    # names and the line shapes it rewrites are this repository's. It runs
    # on a clone, never the working tree: a sync that broke would rewrite
    # the very goldens that would catch it.
    git clone -q "$REPO" "$BATS_TEST_TMPDIR/clone"
    cp "$REPO/bin/pins-bot.sh" "$BATS_TEST_TMPDIR/clone/bin/"
    run "$BATS_TEST_TMPDIR/clone/bin/pins-bot.sh" sync HEAD
    [ "$status" -eq 0 ]
    [ "$output" = unchanged ]
    [ -z "$(git -C "$BATS_TEST_TMPDIR/clone" status --porcelain -- internal)" ]
}
