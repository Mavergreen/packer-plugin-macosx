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
#   mirror-aquatransport.sh [--first-seen YYYY-MM-DD] [--note TEXT] OBSERVED.json DMG
#       OBSERVED.json holds exactly one release, as observe-aquatransport.sh writes it; DMG must
#       hash to its digest. A tag that exists with that digest is success with nothing written;
#       one with no asset gets the asset; a new tag is created. After any write the release is
#       read back, and anything but that digest on AquaTransport.dmg fails. Needs GH_TOKEN with
#       contents: write on MIRROR_REPO (default Mavergreen/aquatransport-mirror). --first-seen
#       (default today, UTC) and --note go into the release notes, for a backfill.
#
# Sourcing this file (MIRROR_AQUATRANSPORT_LIB=1) defines the functions and runs nothing.

mirror_tag() {
    printf '%s-%s\n' "$1" "$(printf '%s' "$2" | cut -c1-12)"
}

mirror_fail() {
    echo "mirror-aquatransport: $*" >&2
    exit 1
}

# release_digest TAG: print the sha256 of the release's AquaTransport.dmg ("" if it has none);
# exit 3 if there is no such release, 1 on any other failure.
release_digest() {
    err=$(mktemp "${TMPDIR:-/tmp}/mirror-aquatransport.XXXXXX") || return 1
    if json=$(gh api "repos/$repo/releases/tags/$1" 2>"$err"); then
        rm -f "$err"
        printf '%s' "$json" | python3 -I -c '
import json, sys
for a in json.load(sys.stdin).get("assets", []):
    if a.get("name") == "AquaTransport.dmg":
        d = a.get("digest") or ""
        print(d[len("sha256:"):] if d.startswith("sha256:") else "")
        break
' || return 1
        return 0
    fi
    if grep -q 'HTTP 404' "$err"; then rm -f "$err"; return 3; fi
    cat "$err" >&2
    rm -f "$err"
    return 1
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

    have=$(release_digest "$tag"); rc=$?
    case $rc in
    0)
        if [ "$have" = "$digest" ]; then
            echo "mirror-aquatransport: $repo $tag already holds $digest"
            return 0
        fi
        [ -z "$have" ] || mirror_fail "$repo $tag holds sha256 $have, not $digest: left as it is for a person to look at"
        gh release upload "$tag" "$dmg" --repo "$repo" || mirror_fail "could not upload to $repo $tag" ;;
    3)
        notes="AquaTransport.dmg, byte-exact as downloaded from https://mavericksforever.com/downloads/AquaTransport.dmg.

SHA-256: $digest
First seen: $first_seen"
        [ -z "$note" ] || notes="$notes
$note"
        gh release create "$tag" "$dmg" --repo "$repo" --title "$tag" --notes "$notes" ||
            mirror_fail "could not create $repo $tag" ;;
    *) mirror_fail "could not read $repo $tag" ;;
    esac

    have=$(release_digest "$tag") || mirror_fail "could not read $repo $tag back"
    [ "$have" = "$digest" ] || mirror_fail "$repo $tag holds '${have}' after the write, not $digest"
    echo "mirror-aquatransport: $repo $tag now holds $digest"
}

if [ -z "${MIRROR_AQUATRANSPORT_LIB:-}" ]; then
    repo=${MIRROR_REPO:-Mavergreen/aquatransport-mirror}
    first_seen=$(date -u +%Y-%m-%d) note=
    while [ $# -gt 0 ]; do
        case $1 in
        --first-seen) [ $# -ge 2 ] || mirror_fail "--first-seen needs a date"
            printf '%s\n' "$2" | grep -Eq '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' ||
                mirror_fail "--first-seen '$2' is not YYYY-MM-DD"
            first_seen=$2; shift 2 ;;
        --note) [ $# -ge 2 ] || mirror_fail "--note needs text"
            note=$2; shift 2 ;;
        -*) mirror_fail "unknown option $1" ;;
        *) break ;;
        esac
    done
    [ $# -eq 2 ] || mirror_fail "usage: $0 [--first-seen YYYY-MM-DD] [--note TEXT] OBSERVED.json DMG"
    mirror "$1" "$2"
fi
