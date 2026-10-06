#!/usr/bin/env bash
# Re-fetch and re-verify every assets/pins/sources.tsv entry this branch
# changed.
#
# WHY THIS EXISTS, AND WHY IT IS NOT OPTIONAL
#
# Each line of assets/pins/sources.tsv is a URL and a sha256 of what that URL
# returns. Renovate can move the URL; it cannot compute the checksum. So a
# bump PR arrives with a new version and an old hash -- a pin that is
# internally inconsistent and will fail at fetch time, half an hour into
# somebody's build.
#
# Without this check that PR is GREEN, because nothing else in the suite
# downloads anything, and the shared Renovate policy automerges green PRs.
# That would turn "track the ingredient" into "break the ingredient
# automatically", which is worse than not tracking it. With this check the
# PR is red until a human fetches the artifact, reads what changed and pins
# the new checksum -- which for the boot stack is the review we want anyway
# (see .github/renovate.json's packageRule).
#
# It is deliberately scoped to CHANGED lines. Re-downloading the whole
# registry on every run is several gigabytes and would make the check the
# reason nobody runs the suite.
#
#   usage: bin/verify-changed-sources.sh [base-ref]
#
# With no base-ref it compares against the merge base with origin/main, and
# skips cleanly when there is no such ref -- a fresh clone with no remote
# has nothing to diff against, and a check that fails there is a check that
# gets deleted.
set -euo pipefail

MQG_REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
export MQG_REPO_ROOT
# shellcheck source=../lib/common.sh
. "$MQG_REPO_ROOT/lib/common.sh"
# shellcheck source=../lib/vendor.sh
. "$MQG_REPO_ROOT/lib/vendor.sh"

# shellcheck disable=SC2034  # read by log()/warn()/die() at call time
MQG_LOG_PREFIX=verify-changed-sources

TSV=$MQG_REPO_ROOT/assets/pins/sources.tsv
cd "$MQG_REPO_ROOT"

base=${1:-}
if [ -z "$base" ]; then
    base=$(git merge-base HEAD origin/main 2>/dev/null || true)
fi
if [ -z "$base" ] || ! git rev-parse --verify "$base" >/dev/null 2>&1; then
    log "no base revision to compare against -- nothing to verify"
    exit 0
fi

old=$(mktemp "${TMPDIR:-/tmp}/verify-changed-sources.XXXXXX") || die "cannot create a temp file"
trap 'rm -f "$old"' EXIT
# A base with no registry at all compares as empty: every pin is new.
git show "$base:assets/pins/sources.tsv" > "$old" 2>/dev/null || : > "$old"

# Names whose url or sha256 column differs from the base revision. Compared
# field by field rather than line by line, so a reflowed comment or a
# reordered file is not mistaken for a changed pin.
changed=
while IFS=$'\t' read -r name url sha; do
    case ${name:-} in '#'*|'') continue ;; esac
    [ -n "${url:-}" ] || continue
    oldline=$(awk -F'\t' -v n="$name" \
        '$0 !~ /^#/ && $1 == n { print; exit }' "$old")
    if [ "$oldline" = "$(printf '%s\t%s\t%s' "$name" "$url" "$sha")" ]; then
        continue
    fi
    changed="$changed $name"
done < "$TSV"

if [ -z "$changed" ]; then
    log "no assets/pins/sources.tsv pin changed since $base -- nothing to fetch"
    exit 0
fi

require_cmd curl sha256sum

work=$(mktemp -d "${TMPDIR:-/tmp}/verify-changed-sources.XXXXXX") || die "cannot create a temp directory"
trap 'rm -f "$old"; rm -rf "$work"' EXIT

status=0
for name in $changed; do
    sha=$(source_field "$TSV" "$name" sha256)
    case $name in
        apple-*)
            # The installesd data source (internal/fetch/esd.go) does an
            # AssetToken handshake with osrecovery.apple.com that plain
            # curl cannot. Apple's media is also the one pin that never
            # moves: 10.9.5 is 10.9.5 forever.
            log "$name: skipped (needs the osrecovery handshake; see" \
                "internal/fetch/esd.go)"
            continue ;;
    esac
    if [ "$sha" = "TOFU" ]; then
        warn "$name: still TOFU -- fetch it once and commit the checksum"
        status=1
        continue
    fi
    log "verifying $name"
    if ! fetch_source "$TSV" "$name" "$work/$name" >/dev/null; then
        warn "$name: does not match its pinned checksum."
        warn "  A version bump needs its sha256 updated in the same commit:"
        warn "  fetch the artifact, sha256sum it, and pin the new value."
        status=1
    fi
done

[ "$status" -eq 0 ] || die "changed pins do not verify"
log "every changed pin verifies"
