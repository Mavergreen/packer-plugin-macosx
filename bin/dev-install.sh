#!/usr/bin/env bash
# Build the plugin and install it where a local `packer` will find it.
#
# `packer plugins install --path` is how a plugin binary reaches
# PACKER_PLUGIN_PATH without going through a registry: it copies the
# binary in, named and versioned the way `packer init`'s multi-plugin
# discovery expects. This script is that one step, for anyone (a
# developer, CI, a host measuring a real build) who has a `packer-plugin-macosx`
# checkout and wants `packer` to see it.
#
#   usage: bin/dev-install.sh
#   env:   PACKER              the packer binary to run (default: packer)
#          PACKER_PLUGIN_PATH  where `packer plugins install` writes to,
#                              same as packer's own meaning for it
set -euo pipefail

MQG_REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# shellcheck source=../lib/common.sh
. "$MQG_REPO_ROOT/lib/common.sh"

# shellcheck disable=SC2034  # read by log()/warn()/die() at call time
MQG_LOG_PREFIX=dev-install

cd "$MQG_REPO_ROOT"

PACKER=${PACKER:-packer}
require_cmd go "$PACKER"

log "building ./packer-plugin-macosx"
go build -o packer-plugin-macosx ./cmd/packer-plugin-macosx

log "installing with $PACKER plugins install"
"$PACKER" plugins install --path ./packer-plugin-macosx github.com/mavergreen/macosx
