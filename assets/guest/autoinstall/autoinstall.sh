#!/bin/sh
# Prepare the target disk for an unattended Mavericks install.
#
# WHERE THIS RUNS
#
# Injected onto the installer media as /private/etc/rc.cdrom.local, which
# Apple's own /etc/rc.install sources early in the installer environment:
#
#     39: if [ -x /etc/rc.cdrom.local ]; then
#     40:     /etc/rc.cdrom.local
#
# This is a supported hook, not a hole we found. Note that /etc/rc.cdrom
# does NOT source it -- rc.cdrom reaches rc.install via `launchctl load
# -D system`, so reading only rc.cdrom makes the hook look absent. It is
# not: rc.install's lines 39-40, above, are where it runs.
#
# WHAT IT DOES, AND WHAT IT DELIBERATELY DOES NOT
#
# It prepares the disk and nothing else. The install itself is Apple's:
# /System/Installation/Packages/Extras/minstallconfig.xml puts the OS X
# Installer into its own automated mode, and rc.install reboots when that
# finishes. So there is no `installer -pkg` call here, and no `shutdown`
# -- both would be us reimplementing something already present.
#
# By the time this runs, rc.cdrom has already mounted RAM disks over
# /Volumes, /var/tmp and /var/run, and remounted the root (the media)
# read-only. So /var/tmp is writable and the media is not.
#
# CHOOSING THE TARGET
#
# Three disks are attached: the installer media we booted from, the
# OpenCore EFI image, and the disk to install onto. Upstream
# (timsutton/osx-vm-templates) hardcodes diskN with a fallback to the next
# one, which is fine for a VM with one disk and unsafe here.
#
# The key is being UNPARTITIONED. The media and the OpenCore image both
# carry partition tables; a freshly created target has nothing but zeroes,
# so `diskutil list` shows it with no slices at all. Size is a secondary
# guard. If that does not name exactly one disk, this script erases
# nothing and stops -- see the comment on `give_up` for why stopping means
# sleeping rather than exiting.

set -u

VOLNAME=${MQG_TARGET_VOLUME:-Mavericks}

# 16 GiB. A secondary guard only: the primary test is that the disk has no
# partitions. Mavericks itself lands at about 8.5 GiB, so anything smaller
# than this is not a disk anybody meant to install onto.
MIN_BYTES=${MQG_TARGET_MIN_BYTES:-17179869184}

# How long to wait for the target to show up, in seconds. Disk arbitration
# is a launchd job and the SATA ports are probed asynchronously, so the
# first scan can genuinely run before the disk exists.
WAIT_SECONDS=${MQG_TARGET_WAIT:-120}

LOG=${MQG_AUTOINSTALL_LOG:-/var/tmp/mqg-autoinstall.log}
VOLUME_LOG_NAME=.mqg-autoinstall.log

# Set to 1 to print the disk this would erase and exit without touching
# it. This exists so tests/guest_assets.bats can drive the selection logic
# against recorded `diskutil list` output. Choosing the wrong disk here
# destroys the installer media, and "I read it carefully" is not a test.
DRY_RUN=${MQG_AUTOINSTALL_DRY_RUN:-0}

# Overridable so those same tests can substitute a stub. Nothing in the
# installer environment sets it.
DISKUTIL=${MQG_DISKUTIL:-/usr/sbin/diskutil}
GREP=/usr/bin/grep
SED=/usr/bin/sed
HEAD=/usr/bin/head
LOGGER=/usr/bin/logger

# Progress goes to stderr, never stdout. `find_candidates` prints disk
# identifiers on stdout for its caller to capture, and a stray progress
# line mixed into that would become a "disk" the script then tried to
# erase.
say() {
    echo "mqg-autoinstall: $*" >&2
    echo "mqg-autoinstall: $*" >> "$LOG" 2>/dev/null
    "$LOGGER" -t mqg-autoinstall -p install.info "$*" 2>/dev/null
    mirror_log
}

# Copy the log onto the target volume, once there is one. That is the copy
# that survives the reboot and can be read from the host afterwards; the
# one in /var/tmp dies with the RAM disk.
mirror_log() {
    if [ -d "/Volumes/$VOLNAME" ]; then
        cp "$LOG" "/Volumes/$VOLNAME/$VOLUME_LOG_NAME" 2>/dev/null
    fi
    return 0
}

# Stop, loudly, without rebooting.
#
# Exiting instead would be worse than it looks. minstallconfig.xml is
# already on the media, so rc.install would launch the automated installer
# against a target volume that does not exist, fail, and take the
# `Minstaller` branch -- which is `/sbin/reboot`. That is a reboot loop
# that erases its own evidence every 40 seconds.
#
# rc.install sources this script synchronously, so sleeping here stops the
# installer from ever starting. The console text stays on the framebuffer,
# where anyone looking at the guest's display can read it. A hung VM is a bad
# outcome; a hung VM that says why is a diagnosable one.
give_up() {
    say "REFUSING TO PROCEED: $*"
    say "nothing has been erased. Halting here rather than rebooting:"
    say "a failed automated install reboots, and a reboot loop would erase"
    say "this message. Read the console, then power the VM off."
    if [ "$DRY_RUN" = 1 ]; then
        exit 3
    fi
    sleep 9999999
}

# Every whole disk, as bare identifiers. `diskutil list` prints one
# /dev/diskN line per disk, then its slices indented below.
whole_disks() {
    "$DISKUTIL" list 2>/dev/null | "$SED" -n 's|^/dev/\(disk[0-9][0-9]*\).*|\1|p'
}

# How many slices a disk has. A disk with a partition table has at least
# one; a disk of zeroes has none, and that is the whole selection rule.
slice_count() {
    "$DISKUTIL" list "/dev/$1" 2>/dev/null \
        | "$GREP" -c -E "[[:space:]]$1s[0-9]+[[:space:]]*\$"
}

# Size in bytes. `diskutil info` renders it as
# "Total Size:  64.4 GB (64424509440 Bytes) (exactly ...)"; older and
# newer wordings say "Disk Size", so match either and take the parenthesised
# byte count, which is exact.
disk_bytes() {
    "$DISKUTIL" info "/dev/$1" 2>/dev/null \
        | "$GREP" -E '(Total|Disk) Size:' \
        | "$SED" -n 's/.*(\([0-9][0-9]*\) Bytes).*/\1/p' \
        | "$HEAD" -1
}

# Prints the candidates it found, one per line, and logs its reasoning for
# every disk it rejected. The reasoning is the point: when this picks the
# wrong disk, or no disk, the log says exactly which test each disk failed.
find_candidates() {
    for disk in $(whole_disks); do
        slices=$(slice_count "$disk")
        bytes=$(disk_bytes "$disk")
        [ -n "$bytes" ] || bytes=0
        if [ "$slices" -ne 0 ]; then
            say "  $disk: $bytes bytes, $slices partitions -- skipped, already partitioned"
        elif [ "$bytes" -ne 0 ] && [ "$bytes" -lt "$MIN_BYTES" ]; then
            say "  $disk: $bytes bytes, unpartitioned -- skipped, smaller than $MIN_BYTES"
        else
            say "  $disk: $bytes bytes, unpartitioned -- CANDIDATE"
            echo "$disk"
        fi
    done
}

say "started; target volume will be named $VOLNAME"
say "disks as the installer environment sees them:"
"$DISKUTIL" list >> "$LOG" 2>&1
"$DISKUTIL" list >&2

waited=0
candidates=
count=0
while :; do
    candidates=$(find_candidates)
    count=$(echo "$candidates" | "$GREP" -c .)
    [ "$count" -ge 1 ] && break
    [ "$waited" -ge "$WAIT_SECONDS" ] && break
    say "no candidate yet; waiting (${waited}s of ${WAIT_SECONDS}s)"
    sleep 5
    waited=$((waited + 5))
done

if [ "$count" -eq 0 ]; then
    give_up "no unpartitioned disk of at least $MIN_BYTES bytes appeared in ${WAIT_SECONDS}s"
fi
if [ "$count" -gt 1 ]; then
    give_up "more than one disk matches ($(echo "$candidates" | tr '\n' ' ')) -- ambiguous"
fi

target=$(echo "$candidates" | "$HEAD" -1)
say "target is /dev/$target"

if [ "$DRY_RUN" = 1 ]; then
    echo "$target"
    exit 0
fi

# GPT, one journalled HFS+ volume, filling the disk.
#
# The scheme is the step that matters and the one that is invisible when
# you get it wrong: the older Apple scheme installs perfectly well and then
# will not boot under OpenCore/UEFI, which you discover after a full
# install. A hand-driven install found it the step that matters.
say "partitioning /dev/$target: GPT, one JHFS+ volume named $VOLNAME"
if "$DISKUTIL" partitionDisk "/dev/$target" 1 GPTFormat JHFS+ "$VOLNAME" 100% \
        >> "$LOG" 2>&1; then
    say "partitionDisk succeeded"
elif "$DISKUTIL" eraseDisk JHFS+ "$VOLNAME" GPTFormat "/dev/$target" \
        >> "$LOG" 2>&1; then
    # What upstream uses. Kept as a fallback because the two spellings have
    # drifted between releases and this script may outlive 10.9.
    say "partitionDisk failed; eraseDisk succeeded"
else
    "$DISKUTIL" list >> "$LOG" 2>&1
    give_up "could not put a GPT and a JHFS+ volume on /dev/$target"
fi

waited=0
while [ ! -d "/Volumes/$VOLNAME" ]; do
    if [ "$waited" -ge 60 ]; then
        give_up "/dev/$target was partitioned but /Volumes/$VOLNAME never appeared"
    fi
    sleep 2
    waited=$((waited + 2))
done

say "/Volumes/$VOLNAME is mounted"
"$DISKUTIL" list >> "$LOG" 2>&1

# What the installer is about to read, as it sees it rather than as we
# hoped. rc.cdrom mounts a union RAM disk over /System/Installation, so
# "the file is on the media" and "the file is visible to the installer"
# are two different claims.
{
    echo "--- /System/Installation/Packages/Extras/"
    ls -l /System/Installation/Packages/Extras/ 2>&1
    echo "--- minstallconfig.xml as the installer will read it"
    cat /System/Installation/Packages/Extras/minstallconfig.xml 2>&1
    echo "--- OSInstall.collection as the installer will read it"
    cat /System/Installation/Packages/OSInstall.collection 2>&1
    echo "--- OSInstall.mpkg"
    ls -l /System/Installation/Packages/OSInstall.mpkg 2>&1
    echo "--- /var/log at handoff"
    ls -la /var/log 2>&1
} >> "$LOG" 2>&1

# Ship whatever the installer logs to the target volume while it runs.
#
# /var/log is a RAM disk here, so everything in it evaporates at reboot.
# Without this, a failed automated install can only be read off the screen
# -- and the screen is a modal dialog saying "check the Installer Log",
# with no way to open it that does not involve a human and a mouse. That
# cost an attempt.
#
# The copies land on the target volume, survive the reboot, and are read
# from the host with `qemu-img convert -O raw` and `7z`. On the run that
# worked, /var/log held no .log files at all -- rc.install pipes the
# installer through `logger`, and 10.9's syslog puts that in ASL's binary
# store rather than a text file. The `ls -la /var/log` above is there to
# say where it did go, next time one fails.
mkdir -p "/Volumes/$VOLNAME/.mqg-logs" 2>/dev/null
(
    while :; do
        cp /var/log/*.log "/Volumes/$VOLNAME/.mqg-logs/" 2>/dev/null
        cp "$LOG" "/Volumes/$VOLNAME/$VOLUME_LOG_NAME" 2>/dev/null
        sync
        sleep 5
    done
) &

say "handing off to the OS X Installer, which rc.install runs in automated"
say "mode from Extras/minstallconfig.xml and reboots when it finishes"
mirror_log
exit 0
