# shellcheck shell=sh
# This runs under busybox ash inside the microVM, not under the host's
# shell, and it is sourced-by-path rather than executed -- hence a shell
# directive instead of a shebang.
#
# Runs as uid 0 inside the privops microVM, the finished media mounted at
# $MQG_MNT. Prints one "MQG-SUM-MEDIA <sha256>  <name>" line per file in
# System/Installation/Packages and nothing else; the host compares them
# against assets/pins/apple-packages.sha256.
#
# WHY THE READ HAPPENS HERE, AND IN A MICROVM OF ITS OWN
#
# Three media builds in six, copied through a host mount, put a corrupt
# copy of Apple's Essentials.pkg on the media -- 3.2 GB, half of everything
# there -- while the copy reported success and the entry and byte counts
# matched. A check that compared source to destination through the same
# mount that had just written the file passed on that media: it read the
# page cache, not the disk.
#
# So this is a separate boot from the one that did the writing. Not merely
# a fresh mount -- a fresh kernel, with no page cache at all, reading
# through virtio from the file on the host. Every byte has to come off the
# disk. That is strictly more than a fresh mount on the host could
# promise, and it is the check docs/host-profile.md G20 exists to keep
# working.

d=$MQG_MNT/System/Installation/Packages

if [ ! -d "$d" ]; then
    echo "no Packages directory on the media at $d"
    exit 1
fi

n=0
cd "$d" || exit 1
for f in *; do
    [ -f "$f" ] || continue
    echo "MQG-SUM-MEDIA $($B sha256sum "$f")"
    n=$((n + 1))
done
echo "checksummed $n files in System/Installation/Packages"
[ "$n" -gt 0 ] || exit 1
