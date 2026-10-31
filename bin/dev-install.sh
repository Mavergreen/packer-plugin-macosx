#!/usr/bin/env bash
# platform: host-agnostic
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

GUEST_MACOSX_REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# shellcheck source=../lib/common.sh
. "$GUEST_MACOSX_REPO_ROOT/lib/common.sh"

# shellcheck disable=SC2034  # read by log()/warn()/die() at call time
GUEST_MACOSX_LOG_PREFIX=dev-install

cd "$GUEST_MACOSX_REPO_ROOT"

PACKER=${PACKER:-packer}
require_cmd go "$PACKER"

# GUEST_MACOSX_PLUGIN_BIN: where the binary is built. Two builds at once -- the
# two templates' tests, run in parallel -- must not write one file.
BIN=${GUEST_MACOSX_PLUGIN_BIN:-./packer-plugin-macosx}

log "building $BIN"
# The version: this line's, at the N no release ever takes, still -dev
# (build/version.sh dev; docs/decisions/0012). Packer matches a -dev
# plugin by its version without the -dev, and runs a release of that
# version over it: at a release's N, past or future, `packer init` would
# fetch that release and Packer would run it in place of this build. At
# this N the build outranks every release on its line, and each
# template's ~> 0.<line>.1 still takes it (tests/dev_install.bats).
full=$(sh build/version.sh dev | sed -n 's/^FULL=//p')
[ -n "$full" ] || die "build/version.sh reported no version"
go build -ldflags "-X github.com/Mavergreen/packer-plugin-macosx/version.Version=0.$full" \
    -o "$BIN" ./cmd/packer-plugin-macosx

log "installing with $PACKER plugins install"
said=$("$PACKER" plugins install --path "$BIN" github.com/mavergreen/macosx)
printf '%s\n' "$said"
# "Successfully installed plugin <source> from <bin> to <path>"
dest=${said##* to }
if [ "$dest" = "$said" ] || [ ! -f "$dest" ]; then
    die "$PACKER plugins install did not say where it installed the plugin: $said"
fi
cmp -s "$dest" "$BIN" || die "$dest, where $PACKER installed the plugin, is not the binary just built ($BIN)"

# The plugin Packer will run is the last install it lists for a template,
# the one its constraint takes. Asked of every template that requires the
# plugin, it must be this build, or whoever runs a template would test
# another plugin and never know.
for tpl in templates/*/; do
    grep -qs 'source *= *"github.com/mavergreen/macosx"' "$tpl"*.pkr.hcl || continue
    line=$("$PACKER" plugins required "$tpl" | awk '$2 == "github.com/mavergreen/macosx"')
    case $line in
        *" $dest") ;;
        *) die "for $tpl, $PACKER would run a macosx plugin that is not the build, $dest: \"$line\" (the last one listed is the one it runs)" ;;
    esac
done
log "for every template, $PACKER runs this build: $dest"
