#!/usr/bin/env bats
# platform: host-agnostic

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    # shellcheck source=/dev/null
    source "$REPO/lib/common.sh"
}

@test "sha256_file computes a known checksum" {
    printf 'hello\n' > "$BATS_TEST_TMPDIR/f"
    run sha256_file "$BATS_TEST_TMPDIR/f"
    [ "$status" -eq 0 ]
    [ "$output" = "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03" ]
}

@test "sha256_file fails loudly on a missing file" {
    run sha256_file "$BATS_TEST_TMPDIR/nope"
    [ "$status" -ne 0 ]
    [[ "$output" == *"no such file"* ]] || false
}

@test "sha256_file fails loudly on a file it cannot read" {
    if [ "$(id -u)" -eq 0 ]; then
        skip "root bypasses permission checks, so this file would still be readable"
    fi
    printf 'hello\n' > "$BATS_TEST_TMPDIR/unreadable"
    chmod 000 "$BATS_TEST_TMPDIR/unreadable"
    run sha256_file "$BATS_TEST_TMPDIR/unreadable"
    chmod 600 "$BATS_TEST_TMPDIR/unreadable"
    [ "$status" -ne 0 ]
    [[ "$output" == *"cannot read"* ]] || false
}

@test "verify_sha256 accepts a matching checksum" {
    printf 'hello\n' > "$BATS_TEST_TMPDIR/f"
    run verify_sha256 "$BATS_TEST_TMPDIR/f" \
        "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"
    [ "$status" -eq 0 ]
}

@test "verify_sha256 rejects a mismatching checksum and names both values" {
    printf 'hello\n' > "$BATS_TEST_TMPDIR/f"
    run verify_sha256 "$BATS_TEST_TMPDIR/f" "0000000000000000000000000000000000000000000000000000000000000000"
    [ "$status" -ne 0 ]
    [[ "$output" == *"checksum mismatch"* ]] || false
    [[ "$output" == *"5891b5b5"* ]] || false
}

@test "require_cmd succeeds for commands that exist" {
    run require_cmd sh cat
    [ "$status" -eq 0 ]
}

@test "require_cmd fails and names the missing command" {
    run require_cmd sh definitely-not-a-real-command-xyz
    [ "$status" -ne 0 ]
    [[ "$output" == *"definitely-not-a-real-command-xyz"* ]] || false
}

@test "die exits non-zero with its message on stderr" {
    run die "the thing broke"
    [ "$status" -eq 1 ]
    [[ "$output" == *"the thing broke"* ]] || false
}

@test "repo_root resolves to the actual repository root" {
    want=$(git -C "$REPO" rev-parse --show-toplevel)
    got=$(repo_root)
    [ "$got" = "$want" ]
}

@test "log writes to stderr, not stdout, with the guest-macosx: prefix" {
    out=$(log "hello there" 2>/dev/null)
    err=$(log "hello there" 2>&1 1>/dev/null)
    [ -z "$out" ]
    [ "$err" = "guest-macosx: hello there" ]
}

@test "warn writes to stderr, not stdout, with the guest-macosx: warning: prefix" {
    out=$(warn "heads up" 2>/dev/null)
    err=$(warn "heads up" 2>&1 1>/dev/null)
    [ -z "$out" ]
    [ "$err" = "guest-macosx: warning: heads up" ]
}

@test "GUEST_MACOSX_LOG_PREFIX override is honored" {
    run bash -c '
        GUEST_MACOSX_LOG_PREFIX="custom"
        # shellcheck source=/dev/null
        source "'"$REPO"'/lib/common.sh"
        log "hi"
    '
    [ "$status" -eq 0 ]
    [ "$output" = "custom: hi" ]
}

@test "is_gate_qmp_dir: only mktemp's own /tmp/gate-qmp.XXXXXX, never a path that climbs out" {
    run is_gate_qmp_dir /tmp/gate-qmp.a1B2c3
    [ "$status" -eq 0 ]
    for d in /tmp/gate-qmp. /tmp/gate-qmp.. /tmp/gate-qmp.../x /tmp/gate-qmp.x/.. /tmp/gate-qmp.x/../../home \
             /tmp/gate-qmp.x/y /tmp/other /tmp/gate-qmp.x/ "" /tmp/gate-qmp.a-b "/tmp/gate-qmp.a b"; do
        run is_gate_qmp_dir "$d"
        [ "$status" -ne 0 ] || { echo "accepted: '$d'"; false; }
    done
}

# A UUID: 8-4-4-4-12 lower-case hex digits.
uuid_shaped() { [[ "$1" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]]; }

@test "new_uuid prints a fresh lower-case UUID each time (docs/decisions/0024)" {
    a=$(new_uuid); b=$(new_uuid)
    uuid_shaped "$a"
    uuid_shaped "$b"
    [ "$a" != "$b" ]
}

@test "new_uuid makes one from /dev/urandom where the host has no uuidgen and no kernel UUID" {
    bin="$BATS_TEST_TMPDIR/bin"; mkdir -p "$bin"
    for c in od tr sed head; do ln -s "$(command -v $c)" "$bin/$c"; done
    a=$(PATH="$bin" GUEST_MACOSX_KERNEL_UUID=/nonexistent new_uuid)
    b=$(PATH="$bin" GUEST_MACOSX_KERNEL_UUID=/nonexistent new_uuid)
    uuid_shaped "$a"
    [ "$a" != "$b" ]
    # version 4, RFC 4122 variant: what a random UUID says it is. Twenty of
    # them: a raw variant digit is already 8-b a quarter of the time.
    for i in $(seq 1 20); do
        u=$(PATH="$bin" GUEST_MACOSX_KERNEL_UUID=/nonexistent new_uuid)
        [[ "$u" =~ ^.{14}4.{4}[89ab] ]] || { echo "not version 4 / RFC 4122: $u"; false; }
    done
}

@test "new_uuid lower-cases what uuidgen says" {
    bin="$BATS_TEST_TMPDIR/bin"; mkdir -p "$bin"
    cat > "$bin/uuidgen" <<'STUB'
#!/bin/sh
echo 6ECBD830-F417-56BB-8DBE-244B275C62DC
STUB
    chmod +x "$bin/uuidgen"
    [ "$(PATH="$bin:$PATH" new_uuid)" = 6ecbd830-f417-56bb-8dbe-244b275c62dc ]
}

@test "new_uuid fails rather than print something that is not a UUID" {
    bin="$BATS_TEST_TMPDIR/bin"; mkdir -p "$bin"
    cat > "$bin/uuidgen" <<'STUB'
#!/bin/sh
echo not-a-uuid
STUB
    chmod +x "$bin/uuidgen"
    cat > "$bin/od" <<'STUB'
#!/bin/sh
exit 1
STUB
    chmod +x "$bin/od"
    run env PATH="$bin:$PATH" GUEST_MACOSX_KERNEL_UUID=/nonexistent bash -c "source '$REPO/lib/common.sh'; new_uuid"
    [ "$status" -ne 0 ]
    [ -z "$output" ] || [[ "$output" != *not-a-uuid* ]]
}

@test "derived_mac: a locally administered 52:54:00 address from a UUID's SHA-256, the same every time" {
    [ "$(derived_mac a0db3eed-cbc6-41be-8172-3d17a6d5e58b)" = 52:54:00:7b:68:ca ]
    [ "$(derived_mac a0db3eed-cbc6-41be-8172-3d17a6d5e58b)" = 52:54:00:7b:68:ca ]
    [ "$(derived_mac 00000000-0000-4000-8000-000000000001)" != 52:54:00:7b:68:ca ]
}

@test "derived_mac never gives one of QEMU's own default addresses, 52:54:00:12:34:xx" {
    # this UUID's SHA-256 begins 1234fe56dedb: the next six digits are taken
    [ "$(derived_mac 00000000-0000-4000-8000-0000000063b0)" = 52:54:00:56:de:db ]
}

@test "derived_mac and the box Vagrantfiles' trigger derive the same address (one rule, docs/decisions/0024)" {
    for f in "$REPO/templates/mavericks/box.Vagrantfile.pkrtpl" "$REPO/templates/snowleopard/box.Vagrantfile.pkrtpl"; do
        grep -qF 'h = Digest::SHA256.hexdigest(uuid)' "$f"
        grep -qF 'i += 6 while h[i, 4] == "1234"' "$f"
        grep -qF 'mac = "52:54:00:#{h[i, 2]}:#{h[i + 2, 2]}:#{h[i + 4, 2]}"' "$f"
    done
}

@test "the releases are spelled once: 10.9 and 10.6, and known_release takes exactly those" {
    [ "$GUEST_MACOSX_RELEASES" = "10.9 10.6" ]
    known_release 10.9
    known_release 10.6
    for r in '' 10.4 10.9.5 '10.9 ' ' 10.6' '10.9 10.6' 10 '*'; do
        run known_release "$r"
        [ "$status" -eq 1 ] || { echo "'$r' was known"; false; }
    done
    [ "$(releases_said)" = "10.9 or 10.6" ]
}

@test "real_path follows a file's symlinks to the file itself, portably (no readlink -f)" {
    d="$BATS_TEST_TMPDIR"
    mkdir -p "$d/a/b" "$d/links"
    : > "$d/a/b/disc.iso"
    ln -s ../a/b/disc.iso "$d/links/rel"
    ln -s "$d/links/rel" "$d/links/abs-to-rel"
    ln -s "$d/a" "$d/dirlink"
    want="$(cd "$d/a/b" && pwd -P)/disc.iso"
    [ "$(real_path "$d/links/rel")" = "$want" ]
    [ "$(real_path "$d/links/abs-to-rel")" = "$want" ]
    [ "$(real_path "$d/dirlink/b/disc.iso")" = "$want" ]
    [ "$(real_path "$d/a/b/disc.iso")" = "$want" ]
    ln -s loop1 "$d/links/loop2"; ln -s loop2 "$d/links/loop1"
    run real_path "$d/links/loop1"
    [ "$status" -eq 1 ]
}

@test "real_path resolves a relative link's .. physically, as the kernel does, through a symlinked directory" {
    d="$BATS_TEST_TMPDIR/phys"
    mkdir -p "$d/a" "$d/b/c"
    echo b > "$d/b/disc"
    echo a > "$d/a/disc"
    ln -s ../b/c "$d/a/sym"
    ln -s ../disc "$d/b/c/x"
    # what the kernel reads
    [ "$(cat "$d/a/sym/x")" = b ]
    [ "$(real_path "$d/a/sym/x")" = "$(cd -P "$d/b" && pwd)/disc" ]
}
