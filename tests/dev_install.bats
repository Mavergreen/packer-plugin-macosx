#!/usr/bin/env bats
#
# bin/dev-install.sh builds the plugin and hands it to `packer plugins
# install`. It is exercised here with a stub `packer` that only records
# its arguments -- the real binary is exercised by a real build, with the
# real thing on the real PACKER_PLUGIN_PATH (docs/test-hosts.md).

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    STUB_DIR="$BATS_TEST_TMPDIR/stub"
    mkdir -p "$STUB_DIR"
    RECORD="$BATS_TEST_TMPDIR/packer-args"
    cat > "$STUB_DIR/packer" <<STUB
#!/usr/bin/env bash
printf '%s\n' "\$*" > "$RECORD"
STUB
    chmod +x "$STUB_DIR/packer"
}

teardown() {
    rm -f "$REPO/packer-plugin-macosx"
}

@test "dev-install builds the plugin and installs it with the stub packer" {
    run env PACKER="$STUB_DIR/packer" "$REPO/bin/dev-install.sh"
    [ "$status" -eq 0 ]
    [ -x "$REPO/packer-plugin-macosx" ]
    [ -f "$RECORD" ]
    run cat "$RECORD"
    [ "$output" = "plugins install --path ./packer-plugin-macosx github.com/mavergreen/macosx" ]
}

@test "dev-install uses PACKER, not a hardcoded name" {
    run env PACKER="$STUB_DIR/nonexistent-packer" "$REPO/bin/dev-install.sh"
    [ "$status" -ne 0 ]
    [[ "$output" == *"nonexistent-packer"* ]]
}

@test "dev-install builds where MQG_PLUGIN_BIN says, so two builds at once never share a binary" {
    bin="$BATS_TEST_TMPDIR/elsewhere/packer-plugin-macosx"
    mkdir -p "${bin%/*}"
    run env PACKER="$STUB_DIR/packer" MQG_PLUGIN_BIN="$bin" "$REPO/bin/dev-install.sh"
    [ "$status" -eq 0 ]
    [ -x "$bin" ]
    [ ! -e "$REPO/packer-plugin-macosx" ]
    run cat "$RECORD"
    [ "$output" = "plugins install --path $bin github.com/mavergreen/macosx" ]
}
