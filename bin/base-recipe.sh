#!/bin/sh
# platform: host-agnostic
#
# Print the base recipe: the SHA-256 of what the base image is built from, as of REF (default
# HEAD). Every release publishes it as packer-plugin-macosx_<tag>_mavericks_base_recipe.txt, so
# mavericks-vm can re-key its cached base image only when this value moves. A release that
# changes only the layer template, the docs or the tests prints the same recipe as the one before.
#
# Computed from git objects alone (mode, type, blob id and path of each tracked file), so any
# checkout reproduces it and nothing has to be built. Test files (_test.go, testdata/) are left
# out: they are not in the image.
#
# usage: base-recipe.sh [REF]
set -eu

ref="${1:-HEAD}"

listing="$(git ls-tree -r "$ref" -- go.mod go.sum cmd datasource internal assets templates/mavericks)" || {
    echo "base-recipe: cannot list $ref" >&2
    exit 1
}
if [ -z "$listing" ]; then
    echo "base-recipe: $ref holds none of the base image's sources" >&2
    exit 1
fi

filtered="$(printf '%s\n' "$listing" | grep -v -e '_test\.go$' -e '/testdata/' || true)"

if command -v sha256sum >/dev/null 2>&1; then
    printf '%s\n' "$filtered" | sha256sum | cut -d' ' -f1
else
    printf '%s\n' "$filtered" | shasum -a 256 | cut -d' ' -f1
fi
