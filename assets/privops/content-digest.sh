# shellcheck shell=sh
# This runs under busybox ash inside the microVM, not under the host's
# shell, and it is sourced-by-path rather than executed -- hence a shell
# directive instead of a shebang.
#
# Runs as uid 0 inside the privops microVM. The volume to digest is
# mounted READ-ONLY at $MQG_SRC1; $MQG_RAW2 is a raw block device the
# listing is written to, which on the host is a plain file.
#
# Prints, on the console:
#   MQG-DIGEST-FILES   <n>       files found
#   MQG-DIGEST-HASHED  <n>       files sha256summed (must equal the above)
#   MQG-DIGEST-BYTES   <n>       total bytes of those files
#   MQG-DIGEST-SIZE    <n>       bytes of listing written to the raw disk
#   MQG-DIGEST-SHA256  <sha256>  of the listing, as the guest read it back
#
# WHY THE LISTING GOES OUT ON A RAW DISK AND NOT THE CONSOLE
#
# It is about four megabytes for real installer media -- 39,000 lines --
# and the console is a 16550 emulated one character at a time. The raw
# disk is the same channel the media build (internal/media/build.go) brings
# BaseSystem.dmg out on, run in the same direction, and the host checks
# the sha256 above against its own read of the file, so a short or torn
# write is caught rather than digested.
#
# WHY NOT AS ROOT... IS NOT AN OPTION, AND IT CHANGES THE ANSWER
#
# A host mount as an ordinary user (udisks) cannot read BaseSystem's
# /.file -- mode 0000, the marker OS X looks for to decide a volume has a
# filesystem on it -- so a digest taken that way has to list it as
# "UNREADABLE-<size>  <path>" instead of hashing it. In here we are uid 0
# and it reads fine, so it gets a real checksum like everything else and
# there is no unreadable category left. Digests taken through such a
# mount, before 2026-09-21, are therefore NOT comparable with these. That
# is a better answer, not merely a different one: a listing with an
# unreadable category says something about who took it.
#
# A file that cannot be read for some other reason -- an I/O error off a
# corrupt volume -- would make sha256sum print nothing for it and carry
# on, which would silently shrink the digest's input. So the files are
# counted before they are hashed and both counts are reported: the host
# refuses a digest whose two numbers disagree.

set -u

LC_ALL=C
export LC_ALL

src=${MQG_SRC1:-}
raw=${MQG_RAW2:-}
[ -n "$src" ] || { echo "no volume to digest (MQG_SRC1 unset)"; exit 1; }
[ -n "$raw" ] || { echo "no raw disk to write the listing to"; exit 1; }
[ -b "$raw" ] || { echo "$raw is not a block device"; exit 1; }

cd "$src" || { echo "cannot enter $src"; exit 1; }

# Three directories are skipped, all of them written BY a volume rather
# than being content OF it: a Spotlight store, an FSEvents log and a
# trash. macOS creates .Spotlight-V100 on the installer media the first
# time a guest boots it, with a fresh UUID in the directory name, which
# made two media built from one ESD produce different digests while every
# one of their 39,414 real files matched. The template attaches the media
# snapshot=on, so the guest cannot write to it at all; this stays because
# media a guest has booted read-write carries the directory, and because a
# digest of "what is on the media" should not include what booting it left
# behind.
$B find . \( -name .Spotlight-V100 -o -name .fseventsd \
             -o -name .Trashes \) -prune -o \
     -type f -print0 > /files0 || { echo "find failed under $src"; exit 1; }

found=$($B tr '\0' '\n' < /files0 | $B grep -c .)

# IN BULK, NOT ONE sha256sum PER FILE. 39,000 separate processes took five
# minutes here and one xargs takes twenty seconds. Sorted afterwards, by
# path, so the order is a property of the content and not of the order the
# catalog happened to be walked in.
$B xargs -0 -r "$B" sha256sum < /files0 2>/sha.err | $B sort -k2 > /listing
hashed=$($B grep -c . /listing)

if [ "$found" != "$hashed" ]; then
    echo "MQG-DIGEST-FILES $found"
    echo "MQG-DIGEST-HASHED $hashed"
    echo "sha256sum could not read every file it was given:"
    $B head -20 /sha.err | $B sed 's/^/  /'
    exit 1
fi

# Sizes in one pass too, for the same reason.
# shellcheck disable=SC2016  # $1 is awk's field, not this shell's argument
bytes=$($B xargs -0 -r "$B" stat -c %s < /files0 2>/dev/null \
        | $B awk '{ total += $1 } END { printf "%.0f\n", total + 0 }')

size=$($B stat -c %s /listing)
# The raw disk's capacity, in 512-byte sectors, straight from the kernel.
# A listing that does not fit would be silently truncated by dd and then
# fail the host's checksum with nothing to say about why.
rawname=${raw##*/}
capacity=$($B cat "/sys/class/block/$rawname/size" 2>/dev/null || echo 0)
capacity=$((capacity * 512))
if [ "$capacity" -gt 0 ] && [ "$size" -gt "$capacity" ]; then
    echo "the listing is $size bytes and the raw disk holds $capacity --"
    echo "the host sized it too small for this volume's file count"
    exit 1
fi

$B dd if=/listing of="$raw" conv=notrunc 2>/dev/null \
    || { echo "cannot write the listing to $raw"; exit 1; }
$B sync

echo "MQG-DIGEST-FILES $found"
echo "MQG-DIGEST-HASHED $hashed"
echo "MQG-DIGEST-BYTES $bytes"
echo "MQG-DIGEST-SIZE $size"
# Read back from the DEVICE, not from /listing: this is the number the
# host compares its own read of the file against, so it has to have made
# the trip through virtio at least once on this side too.
echo "MQG-DIGEST-SHA256 $($B dd if="$raw" bs=4096 count=$(( (size + 4095) / 4096 )) 2>/dev/null \
    | $B head -c "$size" | $B sha256sum | $B cut -d' ' -f1)"
echo "digested $hashed files, $bytes bytes"
