#!/usr/bin/env bats

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    # shellcheck source=/dev/null
    source "$REPO/lib/common.sh"
    # shellcheck source=/dev/null
    source "$REPO/lib/vendor.sh"
    SOURCES="$BATS_TEST_TMPDIR/sources.tsv"
    printf '%s\n' \
        '# name	url	sha256' \
        'thing	https://example.invalid/thing.zip	abc123' \
        'untrusted	https://example.invalid/other.zip	TOFU' \
        > "$SOURCES"
}

@test "source_field returns the url for a known source" {
    run source_field "$SOURCES" thing url
    [ "$status" -eq 0 ]
    [ "$output" = "https://example.invalid/thing.zip" ]
}

@test "source_field returns the recorded checksum" {
    run source_field "$SOURCES" thing sha256
    [ "$status" -eq 0 ]
    [ "$output" = "abc123" ]
}

@test "source_field fails for an unknown source" {
    run source_field "$SOURCES" nosuch url
    [ "$status" -ne 0 ]
    [[ "$output" == *"nosuch"* ]]
}

@test "source_field ignores comment lines" {
    run source_field "$SOURCES" '#' url
    [ "$status" -ne 0 ]
}

@test "pin_checksum replaces TOFU with a real checksum in place" {
    run pin_checksum "$SOURCES" untrusted deadbeef
    [ "$status" -eq 0 ]
    run source_field "$SOURCES" untrusted sha256
    [ "$output" = "deadbeef" ]
}

@test "pin_checksum refuses to overwrite an already-pinned checksum" {
    run pin_checksum "$SOURCES" thing deadbeef
    [ "$status" -ne 0 ]
    [[ "$output" == *"already pinned"* ]]
    run source_field "$SOURCES" thing sha256
    [ "$output" = "abc123" ]
}

@test "pin_checksum preserves the sources file's permissions" {
    chmod 644 "$SOURCES"
    pin_checksum "$SOURCES" untrusted deadbeef
    perms=$(stat -c '%a' "$SOURCES" 2>/dev/null || stat -f '%Lp' "$SOURCES")
    [ "$perms" = "644" ]
}

@test "source_field does an exact match, not a prefix match" {
    printf 'thing-extra\thttps://example.invalid/extra.zip\tdeadbeef\n' \
        >> "$SOURCES"
    run source_field "$SOURCES" thing url
    [ "$output" = "https://example.invalid/thing.zip" ]
    run source_field "$SOURCES" thing-extra url
    [ "$output" = "https://example.invalid/extra.zip" ]
}

@test "source_field does a literal match, not a regex match, on the name" {
    printf 'a.b\thttps://example.invalid/dotted.zip\tdeadbeef\n' >> "$SOURCES"
    run source_field "$SOURCES" 'aXb' url
    [ "$status" -ne 0 ]
    run source_field "$SOURCES" 'a.b' url
    [ "$status" -eq 0 ]
    [ "$output" = "https://example.invalid/dotted.zip" ]
}

@test "fetch_source verifies an already-present file against a pinned checksum" {
    dest="$BATS_TEST_TMPDIR/destdir"
    mkdir -p "$dest"
    printf 'hello\n' > "$dest/thing.zip"
    got=$(sha256_file "$dest/thing.zip")
    printf '%s\thttps://example.invalid/thing.zip\t%s\n' thing "$got" \
        > "$BATS_TEST_TMPDIR/pinned.tsv"
    run fetch_source "$BATS_TEST_TMPDIR/pinned.tsv" thing "$dest"
    [ "$status" -eq 0 ]
    [ "${lines[-1]}" = "$dest/thing.zip" ]
}

@test "fetch_source rejects an already-present file that fails verification" {
    dest="$BATS_TEST_TMPDIR/destdir"
    mkdir -p "$dest"
    printf 'hello\n' > "$dest/thing.zip"
    printf '%s\thttps://example.invalid/thing.zip\t%s\n' thing "wrongsum" \
        > "$BATS_TEST_TMPDIR/pinned.tsv"
    run fetch_source "$BATS_TEST_TMPDIR/pinned.tsv" thing "$dest"
    [ "$status" -ne 0 ]
    [[ "$output" == *"checksum mismatch"* ]]
}

@test "source_field returns empty with status 0 when the sha256 field is missing entirely" {
    printf 'nosum\thttps://example.invalid/nosum.zip\n' >> "$SOURCES"
    run source_field "$SOURCES" nosum sha256
    [ "$status" -eq 0 ]
    [ "$output" = "" ]
}

@test "fetch_source accepts a normal filename URL" {
    dest="$BATS_TEST_TMPDIR/destdir"
    mkdir -p "$dest"
    printf 'hello\n' > "$dest/thing.zip"
    got=$(sha256_file "$dest/thing.zip")
    printf '%s\thttps://example.invalid/thing.zip\t%s\n' thing "$got" \
        > "$BATS_TEST_TMPDIR/normal.tsv"
    run fetch_source "$BATS_TEST_TMPDIR/normal.tsv" thing "$dest"
    [ "$status" -eq 0 ]
    [ "${lines[-1]}" = "$dest/thing.zip" ]
}

@test "fetch_source rejects a trailing-slash URL" {
    printf '%s\thttps://example.invalid/dir/\tTOFU\n' thing \
        > "$BATS_TEST_TMPDIR/trailing.tsv"
    run fetch_source "$BATS_TEST_TMPDIR/trailing.tsv" thing "$BATS_TEST_TMPDIR/destdir"
    [ "$status" -ne 0 ]
    [[ "$output" == *"cannot derive a filename"* ]]
}

@test "fetch_source rejects a bare-host URL with no path at all" {
    printf '%s\thttps://example.invalid\tTOFU\n' thing \
        > "$BATS_TEST_TMPDIR/barehost.tsv"
    run fetch_source "$BATS_TEST_TMPDIR/barehost.tsv" thing "$BATS_TEST_TMPDIR/destdir"
    [ "$status" -ne 0 ]
    [[ "$output" == *"cannot derive a filename"* ]]
}

@test "fetch_source accepts a URL with a query string, keeping it in the filename" {
    dest="$BATS_TEST_TMPDIR/destdir"
    mkdir -p "$dest"
    printf 'hello\n' > "$dest/thing.zip?v=2"
    got=$(sha256_file "$dest/thing.zip?v=2")
    printf '%s\thttps://example.invalid/thing.zip?v=2\t%s\n' thing "$got" \
        > "$BATS_TEST_TMPDIR/query.tsv"
    run fetch_source "$BATS_TEST_TMPDIR/query.tsv" thing "$dest"
    [ "$status" -eq 0 ]
    [ "${lines[-1]}" = "$dest/thing.zip?v=2" ]
}

@test "fetch_source downloads over file://, pins TOFU, and records the checksum" {
    src="$BATS_TEST_TMPDIR/upstream.zip"
    printf 'payload\n' > "$src"
    want=$(sha256_file "$src")
    dest="$BATS_TEST_TMPDIR/destdir"
    printf '%s\tfile://%s\tTOFU\n' thing "$src" > "$BATS_TEST_TMPDIR/tofu.tsv"
    run fetch_source "$BATS_TEST_TMPDIR/tofu.tsv" thing "$dest"
    [ "$status" -eq 0 ]
    [ "${lines[-1]}" = "$dest/upstream.zip" ]
    run source_field "$BATS_TEST_TMPDIR/tofu.tsv" thing sha256
    [ "$output" = "$want" ]
}
