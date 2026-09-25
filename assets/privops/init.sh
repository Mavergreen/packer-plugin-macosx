#!/bin/busybox sh
B=/bin/busybox
$B mount -t proc none /proc
$B mount -t sysfs none /sys
$B mount -t devtmpfs none /dev
# The host resolved dependencies and wrote the insertion order here.
# Falls back to the mqg_modules= list the host also puts on the kernel
# command line.
if [ -f /lib/modules/load-order ]; then
    MQG_MODS=$($B cat /lib/modules/load-order)
else
    MQG_MODS=$($B cat /proc/cmdline | $B tr ' ' '\n' | $B sed -n 's/^mqg_modules=//p' | $B tr ',' ' ')
fi
for m in $MQG_MODS; do
    m=${m%.ko}
    if [ -f "/lib/modules/$m.ko" ]; then
        # insmod's stderr is NOT discarded. A module that fails to load is
        # the difference between "HFS+ is unsupported here" and "we never
        # loaded the driver", and those look identical from the outside.
        if $B insmod "/lib/modules/$m.ko" 2>/insmod.err; then
            echo "MQG-PRIVOPS-INSMOD ok $m"
        else
            echo "MQG-PRIVOPS-INSMOD FAILED $m: $($B cat /insmod.err)"
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
MQG_DEV=
for d in /dev/vda1 /dev/vda2 /dev/vda; do
    [ -b "$d" ] || continue
    $B mount -t hfsplus "$d" /mnt 2>/dev/null || continue
    if $B touch /mnt/.mqg-writable 2>/dev/null; then
        $B rm -f /mnt/.mqg-writable
        MQG_DEV=$d
        break
    fi
    echo "MQG-PRIVOPS-READONLY $d (volume not marked cleanly unmounted;"
    echo "  see media.MarkClean in internal/media) -- trying -o force anyway"
    $B umount /mnt 2>/dev/null
    if $B mount -t hfsplus -o force "$d" /mnt 2>/dev/null &&
       $B touch /mnt/.mqg-writable 2>/dev/null; then
        $B rm -f /mnt/.mqg-writable
        MQG_DEV=$d
        break
    fi
    $B umount /mnt 2>/dev/null
done
# The extra disks the host attached, in order, with the role it gave each.
#
# "ro" is mounted read-only and handed to the payload as $MQG_SRC<n>; "raw"
# is handed over as $MQG_RAW<n> and not touched. The roles come from the
# host because only the host knows them: a scratch disk a payload is about
# to dd onto holds no filesystem, and "we tried to mount it and could not"
# must not be how that is discovered -- it is indistinguishable from a
# source image whose filesystem is broken, which is a real failure.
#
# Candidates are tried LARGEST FIRST. dmg2img output carries an Apple
# partition map with small driver partitions around the volume, and which
# number the volume lands on is not ours to predict -- the host's old
# udisks path picked the largest hfsplus partition for exactly this reason.
mqg_sources=0
mqg_bad=
if [ -s /disk-roles ]; then
    while read -r mqg_role; do
        [ -n "$mqg_role" ] || continue
        mqg_sources=$((mqg_sources + 1))
        mqg_letter=$($B echo bcdefghijkl | $B cut -c$mqg_sources)
        mqg_dev=/dev/vd$mqg_letter
        if [ ! -b "$mqg_dev" ]; then
            # The host said it attached this disk and the guest cannot see
            # it, so something is wrong with QEMU or virtio -- not with
            # the payload, which must not be run as though the disk were
            # merely empty.
            echo "MQG-PRIVOPS-SOURCE-MOUNT-FAILED $mqg_sources $mqg_dev no such block device ($mqg_role)"
            $B cat /proc/partitions | $B sed 's/^/  /'
            mqg_bad=1
            break
        fi
        if [ "$mqg_role" = raw ]; then
            eval "MQG_RAW$mqg_sources=\$mqg_dev; export MQG_RAW$mqg_sources"
            echo "MQG-PRIVOPS-DISK $mqg_sources $mqg_dev raw"
            continue
        fi
        mqg_mnt=/src$mqg_sources
        $B mkdir -p "$mqg_mnt"
        mqg_base=${mqg_dev##*/}
        mqg_got=
        for mqg_part in $(
            for s in /sys/class/block/$mqg_base /sys/class/block/$mqg_base*; do
                [ -f "$s/size" ] || continue
                echo "$($B cat "$s/size") ${s##*/}"
            done | $B sort -rn -u | $B awk '{print "/dev/" $2}'
        ); do
            [ -b "$mqg_part" ] || continue
            $B mount -t hfsplus -o ro "$mqg_part" "$mqg_mnt" 2>/dev/null \
                || continue
            mqg_got=$mqg_part
            break
        done
        if [ -z "$mqg_got" ]; then
            # Named as its own failure, not folded into the target's. A
            # source image that will not mount and a target that will not
            # mount have nothing to do with each other, and the first
            # version of the target's own diagnostic exists because a bare
            # "mount failed" cost five remote runs.
            echo "MQG-PRIVOPS-SOURCE-MOUNT-FAILED $mqg_sources $mqg_dev"
            $B cat /proc/partitions | $B sed 's/^/  /'
            mqg_bad=1
            break
        fi
        eval "MQG_SRC$mqg_sources=\$mqg_mnt; export MQG_SRC$mqg_sources"
        echo "MQG-PRIVOPS-DISK $mqg_sources $mqg_got ro $mqg_mnt"
    done < /disk-roles
fi

if [ -n "$mqg_bad" ] && [ -n "$MQG_DEV" ]; then
    # The target mounted and a source did not: unmount cleanly anyway, so
    # the image is not left marked dirty for the next attempt, and say
    # nothing that looks like success.
    $B umount /mnt 2>/dev/null
elif [ -n "$MQG_DEV" ]; then
    echo "MQG-PRIVOPS-MOUNTED $MQG_DEV"
    MQG_MNT=/mnt; export MQG_MNT B
    $B sh /payload.sh
    rc=$?
    $B sync
    $B umount /mnt && echo "MQG-PRIVOPS-OK rc=$rc" || echo "MQG-PRIVOPS-UNMOUNT-FAILED"
else
    # A bare "mount failed" sent this project on two wrong hunts across
    # five remote runs. Say what was actually there: no block device at all
    # means virtio never loaded, and is a different problem entirely from a
    # device present whose filesystem would not mount.
    echo "MQG-PRIVOPS-MOUNT-FAILED"
    echo "MQG-PRIVOPS-DIAG block devices in /dev:"
    $B ls -l /dev/vd* /dev/sd* 2>&1 | $B sed 's/^/  /'
    echo "MQG-PRIVOPS-DIAG /proc/partitions:"
    $B cat /proc/partitions 2>&1 | $B sed 's/^/  /'
    echo "MQG-PRIVOPS-DIAG filesystems the kernel knows:"
    $B grep -c . /proc/filesystems 2>/dev/null
    $B grep hfs /proc/filesystems 2>&1 | $B sed 's/^/  /'
    echo "MQG-PRIVOPS-DIAG modules loaded:"
    $B cat /proc/modules 2>&1 | $B cut -d" " -f1 | $B sed 's/^/  /'
    for d in /dev/vda1 /dev/vda2 /dev/vda; do
        [ -b "$d" ] || continue
        echo "MQG-PRIVOPS-DIAG mount $d says:"
        $B mount -t hfsplus "$d" /mnt 2>&1 | $B sed 's/^/  /'
    done
fi
$B poweroff -f
