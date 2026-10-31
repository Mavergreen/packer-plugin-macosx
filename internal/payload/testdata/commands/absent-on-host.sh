#!/bin/sh
# platform: host-agnostic -- the stand-in for a command the guest's environment has and this host does not: it records the call and succeeds
printf '%s\n' "${0##*/} $*" >> "$ABSENT_ON_HOST_LOG"
