#!/bin/sh
# platform: host-agnostic -- a stand-in diskutil for autoinstall.sh's dry run: it answers list, list /dev/diskN and info /dev/diskN from "<id> <bytes> <slices>" lines in $0.table, with builtins alone (its PATH is an installer's)
table="$0.table"
# disk <id> <slices>: what `diskutil list` prints for it.
disk() {
    echo "/dev/$1"
    echo "   0:  something  *size  $1"
    i=1
    while [ "$i" -le "$2" ]; do
        echo "   $i:  Apple_HFS name  size  ${1}s$i"
        i=$((i + 1))
    done
}
case "$1 ${2:-}" in
"list ")
    while read -r id _ slices; do disk "$id" "$slices"; done < "$table"
    exit 0
    ;;
"list /dev/"* | "info /dev/"*)
    while read -r id bytes slices; do
        [ "/dev/$id" = "$2" ] || continue
        if [ "$1" = list ]; then
            disk "$id" "$slices"
        else
            echo "   Total Size: x GB ($bytes Bytes) (exactly n 512-Byte-Units)"
        fi
        exit 0
    done < "$table"
    ;;
esac
echo "unhandled: $*" >&2
exit 1
