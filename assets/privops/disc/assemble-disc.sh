# shellcheck shell=sh
# platform: host-agnostic -- runs under busybox in the privops Linux microVM, never on the build host
# Runs as uid 0 inside the privops microVM: the user's verified installer
# disc mounted read-only as $GUEST_MACOSX_SRC1, the new media's empty volume at
# $GUEST_MACOSX_MNT, and the injectables' tar on the raw disk $GUEST_MACOSX_RAW2. A retail
# 10.6 disc's volume is already the booted installer, so this copies it
# whole -- there is no BaseSystem to lay out, as there is for 10.9 -- and
# then unpacks the unattended-install hooks and packages over it, before
# fix-ownership.sh's pass makes it all root's.
T0=$($B date +%s)
step() { echo "[$(( $($B date +%s) - T0 ))s] $*"; }
step "copying the disc's volume onto the media"
$B cp -a "$GUEST_MACOSX_SRC1/." "$GUEST_MACOSX_MNT/" || { echo "cp of the disc failed"; exit 1; }
if [ -n "${GUEST_MACOSX_RAW2:-}" ]; then
    step "injecting the files the host staged"
    if ! $B tar xvf "$GUEST_MACOSX_RAW2" -C "$GUEST_MACOSX_MNT" > /inject.log 2>&1; then
        $B sed 's/^/  /' /inject.log
        echo "extracting the injectables failed"
        exit 1
    fi
    $B sed 's/^/  /' /inject.log
    # syslog's lines, for the hook's log follower (autoinstall.sh's
    # SYSLOG_PORT, which must agree): the booted media's root is read-only,
    # so syslogd's own files are never written, and a failed install's
    # account would be in no file at all. One line more in the disc's
    # syslog.conf forwards them, as the disc's own line forwards install.*
    # to instlogd (docs/decisions/0027).
    step "forwarding syslog to 127.0.0.1:32377 for the install's log follower"
    # The leading newline: a syslog.conf without a final one would otherwise
    # have its last line run into this one.
    if ! $B printf '\n*.notice;install.*;kern.*\t@127.0.0.1:32377\n' >> "$GUEST_MACOSX_MNT/private/etc/syslog.conf"; then
        echo "adding the log follower's forward to syslog.conf failed"
        exit 1
    fi
fi
echo "free space on the media:"
$B df -h "$GUEST_MACOSX_MNT" | $B sed 's/^/  /'
step "done"
