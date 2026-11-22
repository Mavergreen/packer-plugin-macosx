#!/usr/bin/env bash
# platform: host-agnostic
# What .github/workflows/pins-bot.yml computes and writes on a Renovate
# branch, so that a bump arrives whole instead of red.
#
#   usage: bin/pins-bot.sh fill <base-ref>
#          bin/pins-bot.sh apply <name=sha256>...
#          bin/pins-bot.sh sync <base-ref>
#
# Renovate moves a URL in assets/pins/sources.tsv, or a components/*/version,
# and nothing else. It cannot compute the sha256 beside a URL, and it does not
# know that the goldens and the ingredient digest repeat every pin
# (internal/pins/testdata/golden/README.md). So:
#
#   fill   downloads each boot-stack source (BOOT_STACK below) whose row moved
#          since this branch left <base-ref>, and prints name=sha256, one per
#          line, for each whose pinned checksum is not what it downloaded.
#          Prints nothing when none moved or all are current. Exits 1, having
#          printed nothing, when a download fails, the base is not a revision,
#          or a row outside the boot stack moved: those are a person's to pin
#          (Apple's media never moves; bin/verify-changed-sources.sh says why).
#          The only command here that reads upstream's bytes, and it only
#          hashes them.
#   apply  writes those checksums into their sources.tsv rows and touches no
#          other byte; refuses a name outside the boot stack, a value that is
#          not 64 hex digits, or a name given twice, before writing anything.
#   sync   carries every pin that moved since <base-ref> (a sources.tsv URL
#          or checksum, a component version) into the goldens that repeat it,
#          then recomputes the ingredient digest into its golden and into
#          TestTheIngredientDigestIsPinned. A golden row is rewritten only when
#          it holds the base's value for that pin, so a fixture holding some
#          other value on purpose is left alone.
#
# apply and sync print "changed" or "unchanged". Neither fetches anything or
# needs shipyard: the bot's push job, which holds a token that can write,
# runs only them.
set -euo pipefail

GUEST_MACOSX_REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
export GUEST_MACOSX_REPO_ROOT
# shellcheck source=../lib/common.sh
. "$GUEST_MACOSX_REPO_ROOT/lib/common.sh"
# shellcheck source=../lib/sources.sh
. "$GUEST_MACOSX_REPO_ROOT/lib/sources.sh"

# shellcheck disable=SC2034  # read by log()/warn()/die() at call time
GUEST_MACOSX_LOG_PREFIX=pins-bot

# The rows .github/renovate.json moves in sources.tsv.
BOOT_STACK="opencorepkg-src lilu-release virtualsmc-release"

TSV=assets/pins/sources.tsv
INGREDIENTS=internal/pins/testdata/golden/ingredients.txt
DIGEST=internal/pins/testdata/golden/ingredients-digest.txt
DIGEST_TEST=internal/pins/pins_test.go
LISTINGS=internal/inputs/testdata/golden
cd "$GUEST_MACOSX_REPO_ROOT"

usage() { echo "usage: pins-bot.sh fill <base-ref> | apply <name=sha256>... | sync <base-ref>" >&2; exit 2; }
hex64() { [[ $1 =~ ^[0-9a-f]{64}$ ]]; }
boot_stack() { case " $BOOT_STACK " in *" $1 "*) return 0 ;; esac; return 1; }

# merge_base <ref>: where this branch left <ref>.
merge_base() {
    git rev-parse --verify -q "$1^{commit}" >/dev/null || die "$1 is not a revision"
    git merge-base "$1" HEAD || die "no merge base with $1"
}

# rows <file>: "name<TAB>url<TAB>sha256" for every registry row.
rows() { awk -F'\t' -v OFS='\t' '$0 !~ /^#/ && $1 != "" && NF >= 2 { print $1, $2, $3 }' "$1"; }

# replace_file <file> <content-file>: prints 1 when <file> changed.
replace_file() {
    if cmp -s "$2" "$1"; then rm -f "$2"; else cat "$2" > "$1"; rm -f "$2"; echo 1; fi
}

fill() {
    local base old work name url sha got out=''
    base=$(merge_base "$1")
    old=$(mktemp "${TMPDIR:-/tmp}/pins-bot.XXXXXX")
    work=$(mktemp -d "${TMPDIR:-/tmp}/pins-bot.XXXXXX")
    # shellcheck disable=SC2064  # expand now: these locals are gone at exit
    trap "rm -rf '$old' '$work'" EXIT
    git show "$base:$TSV" > "$old" 2>/dev/null || : > "$old"
    local moved=
    while IFS=$'\t' read -r name url sha; do
        grep -qxF "$name"$'\t'"$url"$'\t'"$sha" <(rows "$old") && continue
        boot_stack "$name" || die "$name moved, and only the boot stack ($BOOT_STACK) is the bot's to pin"
        moved="$moved $name"
    done < <(rows "$TSV")
    [ -n "$moved" ] || { log "no boot-stack source moved since $base"; return 0; }
    # Only now, with something to fetch, is shipyard needed (shipyard_curl).
    # shellcheck source=../lib/vendor.sh
    . "$GUEST_MACOSX_REPO_ROOT/lib/vendor.sh"
    require_cmd sha256sum
    for name in $moved; do
        url=$(source_field "$TSV" "$name" url)
        sha=$(source_field "$TSV" "$name" sha256)
        log "fetching $name from $url"
        shipyard_curl "$url" "$work/$name" || die "download failed for $name"
        got=$(sha256sum "$work/$name" | cut -d' ' -f1)
        if [ "$got" = "$sha" ]; then
            log "$name is already pinned to what it serves"
        else
            out="$out$name=$got"$'\n'
        fi
    done
    printf '%s' "$out"
}

apply() {
    [ $# -gt 0 ] || usage
    local kv name sha seen=" " tmp
    for kv in "$@"; do
        name=${kv%%=*} sha=${kv#*=}
        [ "$name" != "$kv" ] || die "'$kv' is not name=sha256"
        boot_stack "$name" || die "'$name' is not a boot-stack source ($BOOT_STACK)"
        case $seen in *" $name "*) die "$name given twice" ;; esac
        hex64 "$sha" || die "$name's value '$sha' is not a SHA-256"
        source_field "$TSV" "$name" url >/dev/null || die "$TSV has no $name row"
        seen="$seen$name "
    done
    tmp=$(mktemp "${TMPDIR:-/tmp}/pins-bot.XXXXXX")
    awk -F'\t' -v OFS='\t' -v kvs="$*" '
        BEGIN { n = split(kvs, a, " "); for (i = 1; i <= n; i++) { j = index(a[i], "="); v[substr(a[i], 1, j - 1)] = substr(a[i], j + 1) } }
        $0 !~ /^#/ && ($1 in v) { $3 = v[$1] }
        { print }' "$TSV" > "$tmp"
    [ -n "$(replace_file "$TSV" "$tmp")" ] && echo changed || echo unchanged
}

sync() {
    local base subs old name url sha oldurl oldsha f v ov tmp changed='' digest olddigest
    base=$(merge_base "$1")
    subs=$(mktemp "${TMPDIR:-/tmp}/pins-bot.XXXXXX")
    old=$(mktemp "${TMPDIR:-/tmp}/pins-bot.XXXXXX")
    # shellcheck disable=SC2064  # expand now: these locals are gone at exit
    trap "rm -f '$subs' '$old'" EXIT
    # One "old row<TAB>new row" pair per golden row a moved pin rewrites,
    # with the rows' own tab written as \t so the pair splits cleanly.
    sub() { [ "$2" = "$3" ] || printf '%s\\t%s\t%s\\t%s\n' "$1" "$2" "$1" "$3" >> "$subs"; }
    git show "$base:$TSV" > "$old" 2>/dev/null || : > "$old"
    while IFS=$'\t' read -r name url sha; do
        IFS=$'\t' read -r _ oldurl oldsha < <(awk -F'\t' -v OFS='\t' -v n="$name" '$1 == n { print; exit }' <(rows "$old")) || continue
        sub "source-url:$name" "$oldurl" "$url"
        sub "source:$name" "$oldsha" "$sha"
        sub "$name" "$oldsha" "$sha"
    done < <(rows "$TSV")
    for f in components/*/version; do
        [ -f "$f" ] || continue
        name=${f#components/} name=${name%/version}
        v=$(cat "$f")
        ov=$(git show "$base:$f" 2>/dev/null) || continue
        sub "component:$name" "$ov" "$v"
        sub "$name" "$ov" "$v"
    done
    for f in "$INGREDIENTS" "$LISTINGS"/*.txt; do
        tmp=$(mktemp "${TMPDIR:-/tmp}/pins-bot.XXXXXX")
        # FILENAME, not NR == FNR: an empty substitution list is the usual case.
        awk -v subs="$subs" '
            FILENAME == subs { split($0, p, "\t"); gsub(/\\t/, "\t", p[1]); gsub(/\\t/, "\t", p[2]); s[p[1]] = p[2]; next }
            { print (($0 in s) ? s[$0] : $0) }' "$subs" "$f" > "$tmp"
        [ -n "$(replace_file "$f" "$tmp")" ] && changed=1
    done
    digest=$(sha256sum "$INGREDIENTS" | cut -d' ' -f1)
    olddigest=$(cat "$DIGEST")
    if [ "$digest" != "$olddigest" ]; then
        printf '%s\n' "$digest" > "$DIGEST"
        grep -qxF "	const want = \"$olddigest\"" "$DIGEST_TEST" \
            || die "$DIGEST_TEST does not pin $olddigest, the digest $DIGEST held"
        tmp=$(mktemp "${TMPDIR:-/tmp}/pins-bot.XXXXXX")
        awk -v o="	const want = \"$olddigest\"" -v n="	const want = \"$digest\"" \
            '{ print ($0 == o) ? n : $0 }' "$DIGEST_TEST" > "$tmp"
        replace_file "$DIGEST_TEST" "$tmp" >/dev/null
        changed=1
    fi
    [ -n "$changed" ] && echo changed || echo unchanged
}

case ${1:-} in
    fill)  [ $# -eq 2 ] || usage; fill "$2" ;;
    apply) shift; apply "$@" ;;
    sync)  [ $# -eq 2 ] || usage; sync "$2" ;;
    *)     usage ;;
esac
