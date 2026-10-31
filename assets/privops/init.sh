#!/bin/busybox sh
# platform: host-agnostic -- runs under busybox in the privops Linux microVM, never on the build host
B=/bin/busybox
$B mount -t proc none /proc
$B mount -t sysfs none /sys
$B mount -t devtmpfs none /dev
# The host resolved dependencies and wrote the insertion order here.
# Falls back to the gm_modules= list the host also puts on the kernel
# command line.
if [ -f /lib/modules/load-order ]; then
    GUEST_MACOSX_MODS=$($B cat /lib/modules/load-order)
else
    GUEST_MACOSX_MODS=$($B cat /proc/cmdline | $B tr ' ' '\n' | $B sed -n 's/^gm_modules=//p' | $B tr ',' ' ')
fi
for m in $GUEST_MACOSX_MODS; do
    m=${m%.ko}
    if [ -f "/lib/modules/$m.ko" ]; then
        # insmod's stderr is NOT discarded. A module that fails to load is
        # the difference between "HFS+ is unsupported here" and "we never
        # loaded the driver", and those look identical from the outside.
        if $B insmod "/lib/modules/$m.ko" 2>/insmod.err; then
            echo "GUEST-MACOSX-PRIVOPS-INSMOD ok $m"
        else
            echo "GUEST-MACOSX-PRIVOPS-INSMOD FAILED $m: $($B cat /insmod.err)"
        fi
    fi
done
# Try partitions before the whole disk. A bare filesystem image lives at
# /dev/vda, but a GPT-partitioned disk -- which is what real installer
# media is -- puts it on /dev/vda1. Trying only the whole disk fails with
# a bare "mount failed" that says nothing about why.
#
# And check that the mount is WRITABLE, which is not the same as the mount
# succeeding. Linux's hfsplus driver silently falls back to read-only for a
# volume whose header does not say it was cleanly unmounted -- the normal
# state of any media a QEMU guest has booted, because powering a VM off is
# not a clean unmount. The symptom is a successful mount followed by
# "Read-only file system" from every chown, which reads like a permissions
# problem and is not one.
#
# -o force is tried, and for this particular cause it does NOT help:
# hfsplus_fill_super applies force only to the SOFTLOCK and JOURNALED
# branches, and takes the "was not cleanly unmounted" branch first. The
# repair is to mark the volume clean in its two volume headers, which
# media.MarkClean (internal/media/hfs.go) does from the host without
# privilege. Say so here rather than leave a reader to find that out from
# kernel source.
GUEST_MACOSX_DEV=
for d in /dev/vda1 /dev/vda2 /dev/vda; do
    [ -b "$d" ] || continue
    $B mount -t hfsplus "$d" /mnt 2>/dev/null || continue
    if $B touch /mnt/.guest-macosx-writable 2>/dev/null; then
        $B rm -f /mnt/.guest-macosx-writable
        GUEST_MACOSX_DEV=$d
        break
    fi
    echo "GUEST-MACOSX-PRIVOPS-READONLY $d (volume not marked cleanly unmounted;"
    echo "  see media.MarkClean in internal/media) -- trying -o force anyway"
    $B umount /mnt 2>/dev/null
    if $B mount -t hfsplus -o force "$d" /mnt 2>/dev/null &&
       $B touch /mnt/.guest-macosx-writable 2>/dev/null; then
        $B rm -f /mnt/.guest-macosx-writable
        GUEST_MACOSX_DEV=$d
        break
    fi
    $B umount /mnt 2>/dev/null
done
# The extra disks the host attached, in order, with the role it gave each.
#
# "ro" is mounted read-only and handed to the payload as $GUEST_MACOSX_SRC<n>; "raw"
# is handed over as $GUEST_MACOSX_RAW<n> and not touched. The roles come from the
# host because only the host knows them: a scratch disk a payload is about
# to dd onto holds no filesystem, and "we tried to mount it and could not"
# must not be how that is discovered -- it is indistinguishable from a
# source image whose filesystem is broken, which is a real failure.
#
# Candidates are tried LARGEST FIRST. dmg2img output carries an Apple
# partition map with small driver partitions around the volume, and which
# number the volume lands on is not ours to predict -- the host's old
# udisks path picked the largest hfsplus partition for exactly this reason.
gm_sources=0
gm_bad=
if [ -s /disk-roles ]; then
    while read -r gm_role; do
        [ -n "$gm_role" ] || continue
        gm_sources=$((gm_sources + 1))
        gm_letter=$($B echo bcdefghijkl | $B cut -c$gm_sources)
        gm_dev=/dev/vd$gm_letter
        if [ ! -b "$gm_dev" ]; then
            # The host said it attached this disk and the guest cannot see
            # it, so something is wrong with QEMU or virtio -- not with
            # the payload, which must not be run as though the disk were
            # merely empty.
            echo "GUEST-MACOSX-PRIVOPS-SOURCE-MOUNT-FAILED $gm_sources $gm_dev no such block device ($gm_role)"
            $B cat /proc/partitions | $B sed 's/^/  /'
            gm_bad=1
            break
        fi
        if [ "$gm_role" = raw ]; then
            eval "GUEST_MACOSX_RAW$gm_sources=\$gm_dev; export GUEST_MACOSX_RAW$gm_sources"
            echo "GUEST-MACOSX-PRIVOPS-DISK $gm_sources $gm_dev raw"
            continue
        fi
        gm_mnt=/src$gm_sources
        $B mkdir -p "$gm_mnt"
        gm_base=${gm_dev##*/}
        gm_got=
        for gm_part in $(
            for s in /sys/class/block/$gm_base /sys/class/block/$gm_base*; do
                [ -f "$s/size" ] || continue
                echo "$($B cat "$s/size") ${s##*/}"
            done | $B sort -rn -u | $B awk '{print "/dev/" $2}'
        ); do
            [ -b "$gm_part" ] || continue
            $B mount -t hfsplus -o ro "$gm_part" "$gm_mnt" 2>/dev/null \
                || continue
            gm_got=$gm_part
            break
        done
        if [ -z "$gm_got" ]; then
            # Named as its own failure, not folded into the target's. A
            # source image that will not mount and a target that will not
            # mount have nothing to do with each other, and the first
            # version of the target's own diagnostic exists because a bare
            # "mount failed" cost five remote runs.
            echo "GUEST-MACOSX-PRIVOPS-SOURCE-MOUNT-FAILED $gm_sources $gm_dev"
            $B cat /proc/partitions | $B sed 's/^/  /'
            gm_bad=1
            break
        fi
        eval "GUEST_MACOSX_SRC$gm_sources=\$gm_mnt; export GUEST_MACOSX_SRC$gm_sources"
        echo "GUEST-MACOSX-PRIVOPS-DISK $gm_sources $gm_got ro $gm_mnt"
    done < /disk-roles
fi

if [ -n "$gm_bad" ] && [ -n "$GUEST_MACOSX_DEV" ]; then
    # The target mounted and a source did not: unmount cleanly anyway, so
    # the image is not left marked dirty for the next attempt, and say
    # nothing that looks like success.
    $B umount /mnt 2>/dev/null
elif [ -n "$GUEST_MACOSX_DEV" ]; then
    echo "GUEST-MACOSX-PRIVOPS-MOUNTED $GUEST_MACOSX_DEV"
    GUEST_MACOSX_MNT=/mnt; export GUEST_MACOSX_MNT B
    $B sh /payload.sh
    rc=$?
    $B sync
    $B umount /mnt && echo "GUEST-MACOSX-PRIVOPS-OK rc=$rc" || echo "GUEST-MACOSX-PRIVOPS-UNMOUNT-FAILED"
else
    # A bare "mount failed" sent this project on two wrong hunts across
    # five remote runs. Say what was actually there: no block device at all
    # means virtio never loaded, and is a different problem entirely from a
    # device present whose filesystem would not mount.
    echo "GUEST-MACOSX-PRIVOPS-MOUNT-FAILED"
    echo "GUEST-MACOSX-PRIVOPS-DIAG block devices in /dev:"
    $B ls -l /dev/vd* /dev/sd* 2>&1 | $B sed 's/^/  /'
    echo "GUEST-MACOSX-PRIVOPS-DIAG /proc/partitions:"
    $B cat /proc/partitions 2>&1 | $B sed 's/^/  /'
    echo "GUEST-MACOSX-PRIVOPS-DIAG filesystems the kernel knows:"
    $B grep -c . /proc/filesystems 2>/dev/null
    $B grep hfs /proc/filesystems 2>&1 | $B sed 's/^/  /'
    echo "GUEST-MACOSX-PRIVOPS-DIAG modules loaded:"
    $B cat /proc/modules 2>&1 | $B cut -d" " -f1 | $B sed 's/^/  /'
    for d in /dev/vda1 /dev/vda2 /dev/vda; do
        [ -b "$d" ] || continue
        echo "GUEST-MACOSX-PRIVOPS-DIAG mount $d says:"
        $B mount -t hfsplus "$d" /mnt 2>&1 | $B sed 's/^/  /'
    done
fi
$B poweroff -f
