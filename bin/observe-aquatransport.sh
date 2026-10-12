#!/bin/sh
# platform: host-agnostic -- CI's Linux runner only (curl, sha256sum, 7z, python3), never 10.9
#
# Observe Wowfunhappy's Aqua Transport. https://mavericksforever.com/downloads/AquaTransport.dmg is
# overwritten in place, with no versioned releases, so a scheduled workflow downloads it, reads
# the version out of the package's Distribution file and records the DMG's SHA-256. Renovate
# reads the recorded JSON from the aquatransport-observed branch as a custom datasource.
#
#   observe-aquatransport.sh OUT.json
#       Write {"releases":[{"version":"<v>","digest":"<sha256>"}]} for the current download.
#       A download is real only if it is at least 1 MiB, `7z l` lists AquaTransport.pkg and the
#       version matches YYYY.MM.DD. Otherwise exit non-zero and write nothing.
#   observe-aquatransport.sh --merge EXISTING.json NEW.json OUT.json
#       Write the union: EXISTING's releases, with NEW's version replaced by NEW's entry or
#       added, sorted by version ascending. A missing or empty EXISTING means just NEW.
#       Malformed JSON: exit non-zero and write nothing. Keeping every version seen, each with
#       its last-seen digest, lets Renovate find a digest for a stale-version PR too.
#
# Sourcing this file (OBSERVE_AQUATRANSPORT_LIB=1) defines the functions and runs nothing.
# AQUATRANSPORT_URL overrides the download URL (tests).

aquatransport_version() {
    # The exact id only: ...AquaTransport.ModernRootCertificates has its own version.
    sed -n 's/.*<pkg-ref id="Wowfunhappy\.AquaTransport" version="\([^"]*\)".*/\1/p' "$1" | head -n 1
}

observe_fail() {
    echo "observe-aquatransport: $*" >&2
    exit 1
}

observe() {
    out=$1
    url=${AQUATRANSPORT_URL:-https://mavericksforever.com/downloads/AquaTransport.dmg}
    work=$(mktemp -d "${TMPDIR:-/tmp}/observe-aquatransport.XXXXXX") || exit 1
    trap 'rm -rf "$work"' EXIT
    mkdir "$work/dmg" "$work/pkg" || exit 1

    curl -fsSL --retry 3 --max-time 600 -o "$work/AquaTransport.dmg" "$url" || observe_fail "download failed: $url"
    size=$(wc -c < "$work/AquaTransport.dmg")
    [ "$size" -ge 1048576 ] || observe_fail "download is only $size bytes"
    7z l "$work/AquaTransport.dmg" 2>/dev/null | grep -Eq '(^|[ /])AquaTransport\.pkg$' ||
        observe_fail "the download lists no AquaTransport.pkg"
    7z x -y "-o$work/dmg" "$work/AquaTransport.dmg" >/dev/null 2>&1 || observe_fail "cannot unpack the DMG"
    pkg=$(find "$work/dmg" -name AquaTransport.pkg -type f | head -n 1)
    [ -n "$pkg" ] || observe_fail "the DMG holds no AquaTransport.pkg"
    7z x -y "-o$work/pkg" "$pkg" Distribution >/dev/null 2>&1 || observe_fail "cannot unpack AquaTransport.pkg"
    [ -f "$work/pkg/Distribution" ] || observe_fail "AquaTransport.pkg holds no Distribution"

    version=$(aquatransport_version "$work/pkg/Distribution")
    printf '%s\n' "$version" | grep -Eq '^[0-9]{4}\.[0-9]{2}\.[0-9]{2}$' ||
        observe_fail "no valid version in Distribution (got '$version')"
    digest=$(sha256sum "$work/AquaTransport.dmg" | cut -d' ' -f1)

    printf '{"releases":[{"version":"%s","digest":"%s"}]}\n' "$version" "$digest" > "$work/out.json" || exit 1
    mv "$work/out.json" "$out" 2>/dev/null || cp "$work/out.json" "$out" || exit 1
}

merge() {
    existing=$1 new=$2 out=$3
    tmp=$(mktemp "${TMPDIR:-/tmp}/observe-aquatransport-merge.XXXXXX") || exit 1
    if python3 -I -c '
import json, os, re, sys
existing, new = sys.argv[1], sys.argv[2]
def load(path, allow_empty):
    if not os.path.exists(path) or os.path.getsize(path) == 0:
        if allow_empty:
            return []
        raise SystemExit("missing " + path)
    d = json.load(open(path))
    rels = d["releases"]
    for r in rels:
        if not (isinstance(r["version"], str) and isinstance(r["digest"], str)):
            raise ValueError("bad release")
        if not re.fullmatch(r"[0-9a-f]{64}", r["digest"]):
            raise ValueError("bad digest")
        if not re.fullmatch(r"[0-9]{4}\.[0-9]{2}\.[0-9]{2}", r["version"]):
            raise ValueError("bad version")
    return rels
by = {r["version"]: r["digest"] for r in load(existing, True)}
for r in load(new, False):
    by[r["version"]] = r["digest"]
rels = [{"version": v, "digest": by[v]} for v in sorted(by)]
print(json.dumps({"releases": rels}, separators=(",", ":")))
' "$existing" "$new" > "$tmp"; then
        mv "$tmp" "$out" 2>/dev/null || { cp "$tmp" "$out" && rm -f "$tmp"; } || exit 1
    else
        rm -f "$tmp"
        observe_fail "cannot merge $existing and $new"
    fi
}

if [ -z "${OBSERVE_AQUATRANSPORT_LIB:-}" ]; then
    case "${1:-}" in
    --merge)
        [ $# -eq 4 ] || observe_fail "usage: $0 --merge EXISTING.json NEW.json OUT.json"
        merge "$2" "$3" "$4" ;;
    -*|"") observe_fail "usage: $0 OUT.json | --merge EXISTING.json NEW.json OUT.json" ;;
    *) [ $# -eq 1 ] || observe_fail "usage: $0 OUT.json"
       observe "$1" ;;
    esac
fi
