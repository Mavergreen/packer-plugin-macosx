#!/usr/bin/env bats

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
}

@test "disk images are ignored wherever they appear" {
    for f in scratch.qcow2 somewhere/base.qcow2 images/installer.img \
             images/installer.dmg some.iso; do
        run git -C "$REPO" check-ignore -q "$f"
        [ "$status" -eq 0 ] || { echo "not ignored: $f"; return 1; }
    done
}

@test "source files are not ignored" {
    for f in lib/common.sh cmd/packer-plugin-mavericks/main.go internal/config/config.go; do
        run git -C "$REPO" check-ignore -q "$f"
        [ "$status" -eq 0 ] && { echo "wrongly ignored: $f"; return 1; }
    done
    return 0
}
