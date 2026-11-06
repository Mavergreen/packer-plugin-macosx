#!/usr/bin/env bash
# platform: host-agnostic
# Say, before the suite runs, whether this host has shipyard. The suites
# that load lib/vendor.sh need it (that file fetches with shipyard's
# shipyard_curl); on a host without it they fail, and twenty failures are a
# poor way to learn one missing install. Always exits 0: those tests still
# run, and their failures still fail the suite.
#
#   usage: bin/shipyard-check.sh
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

# shellcheck source=../build/msc.sh
if where=$(. "$repo_root/build/msc.sh" 2>/dev/null && printf '%s' "$SHIPYARD_SCRIPTS"); then
    echo "shipyard: $where"
    exit 0
fi

echo "shipyard not found: these suites need it and will fail:"
# shellcheck disable=SC2016 # the literal text the suites use, not an expansion
(cd "$repo_root" && grep -lF '"$REPO/lib/vendor.sh"' tests/*.bats) | sed 's/^/  /'
echo "Install the shipyard pkg (it provides shipyard-cmake), or set SHIPYARD_SCRIPTS" \
    "to a shipyard checkout's scripts/ directory."
