# shellcheck shell=sh
# Runs as uid 0 inside the privops microVM: the user's verified installer
# disc mounted read-only as $MQG_SRC1, the new media's empty volume at
# $MQG_MNT, and the injectables' tar on the raw disk $MQG_RAW2. A retail
# 10.6 disc's volume is already the booted installer, so this copies it
# whole -- there is no BaseSystem to lay out, as there is for 10.9 -- and
# then unpacks the unattended-install hooks and packages over it, before
# fix-ownership.sh's pass makes it all root's.
T0=$($B date +%s)
step() { echo "[$(( $($B date +%s) - T0 ))s] $*"; }
step "copying the disc's volume onto the media"
$B cp -a "$MQG_SRC1/." "$MQG_MNT/" || { echo "cp of the disc failed"; exit 1; }
if [ -n "${MQG_RAW2:-}" ]; then
    step "injecting the files the host staged"
    if ! $B tar xvf "$MQG_RAW2" -C "$MQG_MNT" > /inject.log 2>&1; then
        $B sed 's/^/  /' /inject.log
        echo "extracting the injectables failed"
        exit 1
    fi
    $B sed 's/^/  /' /inject.log
fi
echo "free space on the media:"
$B df -h "$MQG_MNT" | $B sed 's/^/  /'
step "done"
