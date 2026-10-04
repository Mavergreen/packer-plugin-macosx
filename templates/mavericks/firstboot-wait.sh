#!/bin/sh
# Guest-side: waits, bounded, for the first-boot payload to finish before
# verify.sh asks the guest what it is. SSH answering is not the same as
# the first boot being finished -- assets/guest/firstboot.sh turns Remote
# Login on part-way through and writes its marker last, after everything
# else is done -- so this polls for that marker from inside the guest.
#
# Bounded at 5 minutes and never fatal: a payload that has not finished by
# then is for verify.sh to judge, with whatever state the guest is
# actually in -- and verify.sh fails the build when the marker is still
# missing.
#
# Runs under 10.9's /bin/sh (or its bash 3.2); no bash-4-only feature is
# used (bin/bash32-check.sh covers this file too).
set -eu

MARKER=/private/var/db/.mqg-firstboot/.done
LIMIT=300
EVERY=5
waited=0

while [ "$waited" -lt "$LIMIT" ]; do
    if [ -e "$MARKER" ]; then
        echo "firstboot finished after ${waited}s"
        exit 0
    fi
    sleep "$EVERY"
    waited=$((waited + EVERY))
done

echo "firstboot has not finished after ${LIMIT}s; carrying on, so verify.sh can report the real state" >&2
exit 0
