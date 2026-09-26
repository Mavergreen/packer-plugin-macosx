# shellcheck shell=sh
# This runs under busybox ash inside the microVM, not under the host's
# shell, and it is sourced-by-path rather than executed -- hence a shell
# directive instead of a shebang.
#
# Runs as uid 0 inside the privops microVM.
#   $MQG_SRC1  the ESD volume, read-only
#   $MQG_RAW2  a raw disk the host will read back as a plain file
#   $MQG_MNT   the target image. NOT TOUCHED HERE, and mounted only
#              because the backend always mounts its first disk.
#
# WHY THIS PASS EXISTS AT ALL
#
# The media's root filesystem is the contents of BaseSystem.dmg, which
# lives *inside* the ESD volume and is UDIF-compressed: only dmg2img can
# decode it, dmg2img runs on the host, and the host cannot read the ESD
# volume without mounting it -- which is precisely what a headless host
# cannot do (docs/host-profile.md G26: udisks2 grants loop-setup to a user
# at a seat, and a headless host has none).
#
# So the one file the host still needs out of the ESD comes out the only
# way anything can leave the microVM without a host mount: written to a
# raw disk, which is a plain file on the other side. The host truncates it
# to the length reported here and runs dmg2img on it.
#
# The digest is printed so the host can check its own read of that file
# against what the guest sent. A silent short write would otherwise turn
# into a dmg2img failure that says nothing about where the bytes went.

src=$MQG_SRC1/BaseSystem.dmg

if [ ! -f "$src" ]; then
    echo "no BaseSystem.dmg on the ESD volume at $MQG_SRC1"
    $B ls "$MQG_SRC1" | $B sed 's/^/  /'
    exit 1
fi

bytes=$($B stat -c %s "$src")
echo "BaseSystem.dmg on the ESD: $bytes bytes"

# bs=1M rather than dd's 512-byte default: 470 MB in 512-byte reads is
# nearly a million syscalls through virtio for no reason.
if ! $B dd if="$src" of="$MQG_RAW2" bs=1M conv=notrunc 2>/dev/null; then
    echo "dd of BaseSystem.dmg onto $MQG_RAW2 failed"
    exit 1
fi
$B sync

echo "MQG-BASESYSTEM-BYTES $bytes"
echo "MQG-BASESYSTEM-SHA256 $($B sha256sum "$src" | $B cut -d' ' -f1)"
