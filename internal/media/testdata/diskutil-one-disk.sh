#!/bin/sh
# platform: host-agnostic -- a stand-in diskutil: one empty 20 GiB disk, disk1, for autoinstall.sh's dry run
case "$1 ${2:-}" in
"list ") echo /dev/disk1; echo "   0:  something  *size  disk1" ;;
"list /dev/disk1") echo /dev/disk1; echo "   0:  something  *size  disk1" ;;
"info /dev/disk1") echo "   Total Size: 21.5 GB (21474836480 Bytes) (exactly n 512-Byte-Units)" ;;
*) echo "unhandled: $*" >&2; exit 1 ;;
esac
