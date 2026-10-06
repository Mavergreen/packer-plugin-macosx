#!/usr/bin/env bats

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

@test "log writes to stderr, not stdout, with the mqg: prefix" {
    out=$(log "hello there" 2>/dev/null)
    err=$(log "hello there" 2>&1 1>/dev/null)
    [ -z "$out" ]
    [ "$err" = "mqg: hello there" ]
}

@test "warn writes to stderr, not stdout, with the mqg: warning: prefix" {
    out=$(warn "heads up" 2>/dev/null)
    err=$(warn "heads up" 2>&1 1>/dev/null)
    [ -z "$out" ]
    [ "$err" = "mqg: warning: heads up" ]
}

@test "MQG_LOG_PREFIX override is honored" {
    run bash -c '
        MQG_LOG_PREFIX="custom"
        # shellcheck source=/dev/null
        source "'"$REPO"'/lib/common.sh"
        log "hi"
    '
    [ "$status" -eq 0 ]
    [ "$output" = "custom: hi" ]
}
