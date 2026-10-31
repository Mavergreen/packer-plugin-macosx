#!/usr/bin/env bats
# platform: host-agnostic
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
    export STUB_PLUGINS="$BATS_TEST_TMPDIR/plugins"
    # Quoted, so this is the script that runs (bin/heredoc-scripts.sh). It
    # appends each call to RECORD, $BATS_TEST_TMPDIR/packer-args, beside
    # its own directory, and answers the two calls dev-install makes:
    #   plugins install --path <bin> <source>  copies <bin> into
    #       STUB_PLUGINS and says where, as packer does (STUB_TAMPER=1:
    #       other bytes than <bin>)
    #   plugins required <dir>  the macosx line, the installs in Packer's
    #       order, the one Packer runs last: STUB_RUNS_INSTEAD after the
    #       stub's own install, STUB_REQUIRED_NONE=1 no install at all
    cat > "$STUB_DIR/packer" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$(dirname "$0")/../packer-args"
dest=$STUB_PLUGINS/packer-plugin-macosx_v0.0.0-dev_x5.0_stub
case "$1 $2" in
"plugins install")
    mkdir -p "$STUB_PLUGINS"
    if [ "${STUB_TAMPER:-0}" = 1 ]; then printf 'other bytes\n' > "$dest"; else cp "$4" "$dest"; fi
    printf 'Successfully installed plugin %s from %s to %s\n' "$5" "$4" "$dest"
    ;;
"plugins required")
    line='macosx github.com/mavergreen/macosx "~> 0.20261005.1"'
    [ "${STUB_REQUIRED_NONE:-0}" = 1 ] || line="$line $dest"
    [ -z "${STUB_RUNS_INSTEAD:-}" ] || line="$line $STUB_RUNS_INSTEAD"
    printf '%s\n' 'qemu github.com/hashicorp/qemu "~> 1"' "$line"
    ;;
esac
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
    grep -qxF "plugins install --path ./packer-plugin-macosx github.com/mavergreen/macosx" "$RECORD"
}

@test "dev-install uses PACKER, not a hardcoded name" {
    run env PACKER="$STUB_DIR/nonexistent-packer" "$REPO/bin/dev-install.sh"
    [ "$status" -ne 0 ]
    [[ "$output" == *"nonexistent-packer"* ]] || false
}

@test "dev-install builds where GUEST_MACOSX_PLUGIN_BIN says, so two builds at once never share a binary" {
    bin="$BATS_TEST_TMPDIR/elsewhere/packer-plugin-macosx"
    mkdir -p "${bin%/*}"
    run env PACKER="$STUB_DIR/packer" GUEST_MACOSX_PLUGIN_BIN="$bin" "$REPO/bin/dev-install.sh"
    [ "$status" -eq 0 ]
    [ -x "$bin" ]
    [ ! -e "$REPO/packer-plugin-macosx" ]
    grep -qxF "plugins install --path $bin github.com/mavergreen/macosx" "$RECORD"
}

@test "a dev build is this line's, at the N no release takes, so a template's ~> takes it over every release" {
    # Packer matches a -dev plugin by its version without the -dev, and
    # runs a release of that version over it: a dev build at a release's N
    # would run that release instead (docs/decisions/0012).
    run env PACKER="$STUB_DIR/packer" "$REPO/bin/dev-install.sh"
    [ "$status" -eq 0 ]
    line=$(tr -d '[:space:]' < "$REPO/UPSTREAM_VERSION")
    run "$REPO/packer-plugin-macosx" describe
    [[ "$output" == *"\"version\":\"0.$line.999999-dev\""* ]] || false
}

@test "dev-install asks packer, for every template that requires the plugin, which one it would run" {
    run env PACKER="$STUB_DIR/packer" "$REPO/bin/dev-install.sh"
    [ "$status" -eq 0 ]
    n=0
    for f in "$REPO"/templates/*/*.pkr.hcl; do
        grep -q 'source *= *"github.com/mavergreen/macosx"' "$f" || continue
        d=${f%/*}
        grep -qxF "plugins required templates/${d##*/}/" "$RECORD" || { echo "not asked: ${d##*/}"; return 1; }
        n=$((n + 1))
    done
    [ "$n" -ge 3 ]
}

@test "dev-install fails loudly when packer would run another macosx plugin than the build" {
    other="$BATS_TEST_TMPDIR/packer-plugin-macosx_v0.20261005.999999_x5.0_released"
    : > "$other"
    run env PACKER="$STUB_DIR/packer" STUB_RUNS_INSTEAD="$other" "$REPO/bin/dev-install.sh"
    [ "$status" -ne 0 ]
    [[ "$output" == *"not the build"* ]] || false
    [[ "$output" == *"$other"* ]] || false
    [[ "$output" == *"templates/"* ]] || false
}

@test "dev-install fails loudly when packer would run no macosx plugin at all" {
    run env PACKER="$STUB_DIR/packer" STUB_REQUIRED_NONE=1 "$REPO/bin/dev-install.sh"
    [ "$status" -ne 0 ]
    [[ "$output" == *"not the build"* ]] || false
}

@test "dev-install fails loudly when what packer installed is not the binary it built" {
    run env PACKER="$STUB_DIR/packer" STUB_TAMPER=1 "$REPO/bin/dev-install.sh"
    [ "$status" -ne 0 ]
    [[ "$output" == *"$STUB_PLUGINS/packer-plugin-macosx_v0.0.0-dev_x5.0_stub"* ]] || false
    [[ "$output" == *"not the binary just built"* ]] || false
}

@test "dev-install fails loudly when packer does not say where it installed the plugin" {
    cat > "$STUB_DIR/packer" <<'STUB'
#!/usr/bin/env bash
printf 'installed, somewhere\n'
STUB
    run env PACKER="$STUB_DIR/packer" "$REPO/bin/dev-install.sh"
    [ "$status" -ne 0 ]
    [[ "$output" == *"installed, somewhere"* ]] || false
}
