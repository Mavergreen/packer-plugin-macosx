#!/bin/bash
# platform: host-agnostic -- the fixture's OpenCorePkg build_oc.tool: the one line patch 0001 replaces, as upstream wrote it
# shellcheck disable=SC2015 # upstream's line, verbatim: the patch must find it as it is
src=$(curl -LfsS https://raw.githubusercontent.com/acidanthera/ocbuild/master/efibuild.sh) && eval "$src" || exit 1
