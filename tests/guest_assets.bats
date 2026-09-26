#!/usr/bin/env bats
#
# assets/guest/autoinstall/autoinstall.sh's own behaviour: it runs inside
# the OS X Installer environment during an unattended install, so it is a
# shell script, and these tests run it: the static checks and the
# disk-selection behaviour, driven against a stub `diskutil`, since picking
# the wrong disk erases the installer media and "I read the code carefully"
# is not enough.
#
# The plist and cross-file checks are Go's (internal/media/inject_test.go):
# they read the embedded bytes directly and need no shell to run.

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    AUTO="$REPO/assets/guest/autoinstall/autoinstall.sh"
}

# Writes a stub `diskutil` that answers `list`, `list /dev/diskN` and
# `info /dev/diskN` from a table of "<id> <bytes> <slices>" lines.
make_diskutil() {
    local specs=( "$@" ) spec id bytes slices i
    mkdir -p "$BATS_TEST_TMPDIR/stub"
    STUB="$BATS_TEST_TMPDIR/stub/diskutil"
    {
        echo '#!/bin/sh'
        echo 'case "$1 ${2:-}" in'
        printf '"list ")\n'
        for spec in "${specs[@]}"; do
            read -r id bytes slices <<< "$spec"
            printf '  echo "/dev/%s"\n' "$id"
            printf '  echo "   0:  something  *size  %s"\n' "$id"
            for ((i = 1; i <= slices; i++)); do
                printf '  echo "   %d:  Apple_HFS name  size  %ss%d"\n' "$i" "$id" "$i"
            done
        done
        printf '  ;;\n'
        for spec in "${specs[@]}"; do
            read -r id bytes slices <<< "$spec"
            printf '"list /dev/%s")\n' "$id"
            printf '  echo "/dev/%s"\n' "$id"
            printf '  echo "   0:  something  *size  %s"\n' "$id"
            for ((i = 1; i <= slices; i++)); do
                printf '  echo "   %d:  Apple_HFS name  size  %ss%d"\n' "$i" "$id" "$i"
            done
            printf '  ;;\n'
            printf '"info /dev/%s")\n' "$id"
            printf '  echo "   Total Size: x GB (%s Bytes) (exactly n 512-Byte-Units)"\n' "$bytes"
            printf '  ;;\n'
        done
        printf '*) echo "unhandled: $*" >&2; exit 1 ;;\n'
        echo 'esac'
    } > "$STUB"
    chmod +x "$STUB"
}

run_selection() {
    run env MQG_AUTOINSTALL_DRY_RUN=1 \
        MQG_DISKUTIL="$STUB" \
        MQG_AUTOINSTALL_LOG="$BATS_TEST_TMPDIR/auto.log" \
        MQG_TARGET_WAIT=0 \
        "$AUTO"
}

@test "autoinstall.sh partitions GPT, never the older Apple scheme" {
    run grep -c 'GPT' "$AUTO"
    [ "$output" -ge 1 ]
    run grep -ci 'APM\|Apple_partition_scheme' "$AUTO"
    [ "$output" = "0" ]
}

@test "autoinstall.sh refuses to erase when the target is ambiguous" {
    run grep -cE 'refus|ambiguous|more than one' "$AUTO"
    [ "$output" -ge 1 ]
}

@test "autoinstall.sh never hardcodes the first disk" {
    run grep -cE '/dev/disk0([^0-9]|$)' "$AUTO"
    [ "$output" = "0" ]
}

@test "the only unpartitioned disk is the one chosen" {
    # The template's own layout: a blank 60 GB target, the 6.7 GB
    # installer media, and the 200 MB OpenCore image.
    make_diskutil "disk0 64424509440 0" "disk1 6686769152 1" "disk2 201326592 1"
    run_selection
    [ "$status" -eq 0 ]
    [ "${lines[${#lines[@]}-1]}" = "disk0" ]
}

@test "the chosen disk is not assumed to be the first one" {
    make_diskutil "disk0 6686769152 1" "disk1 201326592 1" "disk2 64424509440 0"
    run_selection
    [ "$status" -eq 0 ]
    [ "${lines[${#lines[@]}-1]}" = "disk2" ]
}

@test "two blank disks erase nothing" {
    # The failure that matters. Two candidates means we cannot tell the
    # target from something else blank, so nothing is touched.
    make_diskutil "disk0 64424509440 0" "disk1 64424509440 0" "disk2 201326592 1"
    run_selection
    [ "$status" -ne 0 ]
    [[ "$output" == *"REFUSING TO PROCEED"* ]]
    [[ "$output" == *"more than one"* ]]
}

@test "no blank disk erases nothing" {
    make_diskutil "disk0 6686769152 1" "disk1 201326592 1"
    run_selection
    [ "$status" -ne 0 ]
    [[ "$output" == *"REFUSING TO PROCEED"* ]]
}

@test "a blank disk too small to be the target is not chosen" {
    # Guards against a future profile attaching a small scratch disk and
    # this quietly erasing it.
    make_diskutil "disk0 268435456 0" "disk1 6686769152 1"
    run_selection
    [ "$status" -ne 0 ]
    [[ "$output" == *"REFUSING TO PROCEED"* ]]
}

@test "the installer media is never a candidate, whatever its size" {
    # Partitioned is partitioned: the media is 6.7 GB and would pass a
    # size test on its own.
    make_diskutil "disk0 6686769152 1"
    run_selection
    [[ "$output" == *"already partitioned"* ]]
    [ "$status" -ne 0 ]
}

@test "the selection log says why each disk was rejected" {
    make_diskutil "disk0 64424509440 0" "disk1 6686769152 1" "disk2 201326592 1"
    run_selection
    run cat "$BATS_TEST_TMPDIR/auto.log"
    [[ "$output" == *"disk1: 6686769152 bytes, 1 partitions -- skipped"* ]]
    [[ "$output" == *"disk0: 64424509440 bytes, unpartitioned -- CANDIDATE"* ]]
}
