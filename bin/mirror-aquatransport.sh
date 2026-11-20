#!/bin/sh
# platform: host-agnostic -- CI's Linux runner only (gh, sha256sum, python3), never 10.9
#
# Mirror one observed Aqua Transport download to Mavergreen/aquatransport-mirror. Upstream
# overwrites its one URL in place, so a pinned version is fetchable only from a copy kept the day
# it was seen. Each (version, digest) is its own release, tagged <version>-<digest[:12]>, holding
# the DMG as AquaTransport.dmg; a version whose bytes upstream replaces gets a new release beside
# the old one. Nothing is ever deleted or replaced: a tag whose asset has other bytes is a failure
# for a person to look at. The DMG is hashed and uploaded, never opened.
#
# The DMG bundles OWL, which is GPLv3, so each release also carries its source:
# AquaTransport-source-<commit>.tar.gz, GitHub's tarball of UPSTREAM_REPO (default
# Wowfunhappy/AquaTransport) at the tip of main when the download was first seen. Upstream tags
# no builds and the binaries carry no stamp, so that is the best match, not a proof; the notes say
# so. The tarball must hold deps/owl/COPYING, or nothing is written.
#
#   mirror-aquatransport.sh [--first-seen YYYY-MM-DD] [--note TEXT] [--source-commit SHA]
#                           OBSERVED.json DMG
#       OBSERVED.json holds exactly one release, as observe-aquatransport.sh writes it; DMG must
#       hash to its digest. A tag that exists with that digest and a source asset is success with
#       nothing fetched or written; one missing either asset gets what it lacks; a new tag is
#       created with both. After any write the release is read back, and anything but that digest
#       on AquaTransport.dmg, or no source asset, fails. Needs GH_TOKEN with contents: write on
#       MIRROR_REPO (default Mavergreen/aquatransport-mirror). --first-seen (default today, UTC),
#       --note and --source-commit (default main's tip now) are for a backfill.
#
# gh release create with assets makes a draft, uploads, then publishes, and the tag exists only
# once it is published. A run cut short mid-upload leaves a draft that releases/tags/TAG cannot
# see, so the next run makes its own; the leftover draft is harmless and a person deletes it. The
# mirror's immutable releases refuse any change to a published release, so a published release
# missing an asset (which gh does not leave) fails the upload for a person to look at.
#
# Sourcing this file (MIRROR_AQUATRANSPORT_LIB=1) defines the functions and runs nothing.

mirror_tag() {
    printf '%s-%s\n' "$1" "$(printf '%s' "$2" | cut -c1-12)"
}

mirror_fail() {
    echo "mirror-aquatransport: $*" >&2
    exit 1
}

source_asset() {
    printf 'AquaTransport-source-%s.tar.gz\n' "$1"
}

# release_state TAG: print "DIGEST SOURCE": the sha256 of the release's AquaTransport.dmg and the
# name of its AquaTransport-source-*.tar.gz, each "-" if it has none; exit 3 if there is no such
# release, 1 on any other failure.
release_state() {
    err=$(mktemp "${TMPDIR:-/tmp}/mirror-aquatransport.XXXXXX") || return 1
    if json=$(gh api "repos/$repo/releases/tags/$1" 2>"$err"); then
        rm -f "$err"
        printf '%s' "$json" | python3 -I -c '
import json, sys
import re
dmg = src = "-"
for a in json.load(sys.stdin).get("assets", []):
    n, d = a.get("name") or "", a.get("digest") or ""
    if n == "AquaTransport.dmg" and d.startswith("sha256:") and len(d) > 7:
        dmg = d[len("sha256:"):]
    elif n == "AquaTransport.dmg":
        dmg = "?"
    elif re.fullmatch(r"AquaTransport-source-[0-9a-f]{40}\.tar\.gz", n):
        src = n
print(dmg, src)
' || return 1
        return 0
    fi
    if grep -q 'HTTP 404' "$err"; then rm -f "$err"; return 3; fi
    cat "$err" >&2
    rm -f "$err"
    return 1
}

# fetch_source DIR: resolve the source commit (--source-commit, else main's tip now), fetch its
# tarball into DIR as source_asset's name, check it holds deps/owl/COPYING, and print its path.
fetch_source() {
    commit=$source_commit
    if [ -z "$commit" ]; then
        commit=$(gh api "repos/$upstream/commits/main" --jq .sha) ||
            mirror_fail "could not read $upstream main's tip"
        printf '%s\n' "$commit" | grep -Eq '^[0-9a-f]{40}$' ||
            mirror_fail "$upstream main's tip '$commit' is not a commit id"
    fi
    out="$1/$(source_asset "$commit")"
    gh api "repos/$upstream/tarball/$commit" > "$out" ||
        mirror_fail "could not fetch $upstream at $commit"
    tar tzf "$out" 2>/dev/null | grep -Eq '^[^/]+/deps/owl/COPYING$' ||
        mirror_fail "$upstream at $commit has no deps/owl/COPYING: not the source of this DMG's OWL"
    printf '%s\n' "$out"
}

mirror() {
    observed=$1 dmg=$2
    read_out=$(python3 -I -c '
import json, re, sys
rels = json.load(open(sys.argv[1]))["releases"]
if len(rels) != 1:
    raise SystemExit("want exactly one release")
v, d = rels[0]["version"], rels[0]["digest"]
if not re.fullmatch(r"[0-9]{4}\.[0-9]{2}\.[0-9]{2}", v) or not re.fullmatch(r"[0-9a-f]{64}", d):
    raise SystemExit("bad version or digest")
print(v, d)
' "$observed") || mirror_fail "cannot read one release from $observed"
    version=${read_out% *} digest=${read_out#* }
    [ -f "$dmg" ] || mirror_fail "no DMG at $dmg"
    got=$(sha256sum "$dmg" | cut -d' ' -f1)
    [ "$got" = "$digest" ] || mirror_fail "$dmg has sha256 $got, not the observed $digest"
    tag=$(mirror_tag "$version" "$digest")

    state=$(release_state "$tag"); rc=$?
    have=${state% *} have_src=${state#* }
    work=$(mktemp -d "${TMPDIR:-/tmp}/mirror-aquatransport.XXXXXX") || mirror_fail "no temporary directory"
    trap 'rm -rf "$work"' EXIT
    # gh names an asset after its file, so the upload is always AquaTransport.dmg.
    if [ "${dmg##*/}" != AquaTransport.dmg ]; then
        cp "$dmg" "$work/AquaTransport.dmg" || mirror_fail "could not copy $dmg"
        dmg=$work/AquaTransport.dmg
    fi
    case $rc in
    0)
        [ "$have" != "?" ] ||
            mirror_fail "$repo $tag has an AquaTransport.dmg with no digest (an upload cut short?): left as it is for a person to look at"
        [ "$have" = "$digest" ] || [ "$have" = - ] ||
            mirror_fail "$repo $tag holds sha256 $have, not $digest: left as it is for a person to look at"
        if [ "$have" = "$digest" ] && [ "$have_src" != - ]; then
            echo "mirror-aquatransport: $repo $tag already holds $digest and $have_src"
            return 0
        fi
        set --
        [ "$have" = "$digest" ] || set -- "$dmg"
        if [ "$have_src" = - ]; then src=$(fetch_source "$work") || exit 1; set -- "$@" "$src"; fi
        gh release upload "$tag" "$@" --repo "$repo" || mirror_fail "could not upload to $repo $tag" ;;
    3)
        src=$(fetch_source "$work") || exit 1
        commit=${src##*/AquaTransport-source-} commit=${commit%.tar.gz}
        notes="AquaTransport.dmg, byte-exact as downloaded from https://mavericksforever.com/downloads/AquaTransport.dmg.

SHA-256: $digest
First seen: $first_seen

The DMG bundles OWL (Open Wireless Link), which is GPLv3. $(source_asset "$commit") is the source of https://github.com/$upstream at that commit.
Source: https://github.com/$upstream/tree/$commit
It is the tip of main when this download was first seen. Upstream tags no builds, so this is the best match, not a proven build commit."
        [ -z "$note" ] || notes="$notes
$note"
        gh release create "$tag" "$dmg" "$src" --repo "$repo" --title "$tag" --notes "$notes" ||
            mirror_fail "could not create $repo $tag" ;;
    *) mirror_fail "could not read $repo $tag" ;;
    esac

    state=$(release_state "$tag") || mirror_fail "could not read $repo $tag back"
    have=${state% *} have_src=${state#* }
    [ "$have" = "$digest" ] || mirror_fail "$repo $tag holds '${have}' after the write, not $digest"
    [ "$have_src" != - ] || mirror_fail "$repo $tag has no source asset after the write"
    echo "mirror-aquatransport: $repo $tag now holds $digest and $have_src"
}

if [ -z "${MIRROR_AQUATRANSPORT_LIB:-}" ]; then
    repo=${MIRROR_REPO:-Mavergreen/aquatransport-mirror}
    upstream=${UPSTREAM_REPO:-Wowfunhappy/AquaTransport}
    first_seen=$(date -u +%Y-%m-%d) note='' source_commit=''
    while [ $# -gt 0 ]; do
        case $1 in
        --first-seen) [ $# -ge 2 ] || mirror_fail "--first-seen needs a date"
            printf '%s\n' "$2" | grep -Eq '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' ||
                mirror_fail "--first-seen '$2' is not YYYY-MM-DD"
            first_seen=$2; shift 2 ;;
        --note) [ $# -ge 2 ] || mirror_fail "--note needs text"
            note=$2; shift 2 ;;
        --source-commit) [ $# -ge 2 ] || mirror_fail "--source-commit needs a commit id"
            printf '%s\n' "$2" | grep -Eq '^[0-9a-f]{40}$' ||
                mirror_fail "--source-commit '$2' is not a full 40-hex commit id"
            source_commit=$2; shift 2 ;;
        -*) mirror_fail "unknown option $1" ;;
        *) break ;;
        esac
    done
    [ $# -eq 2 ] || mirror_fail "usage: $0 [--first-seen YYYY-MM-DD] [--note TEXT] [--source-commit SHA] OBSERVED.json DMG"
    mirror "$1" "$2"
fi
