# shellcheck shell=bash
# platform: host-agnostic
# Reading the third-party artifact registry, assets/pins/sources.tsv
# (tab-separated: name, url, sha256). Needs only lib/common.sh, sourced
# first: no shipyard, so a caller that only reads pins (bin/gate-expected.sh)
# runs on a host without one. lib/vendor.sh, which fetches, sources this.

# source_field <tsv> <name> <url|sha256>
source_field() {
    local tsv=$1 name=$2 field=$3 line url sha
    [ -f "$tsv" ] || die "no such sources file: $tsv"
    case $name in
        '#'*|'') die "invalid source name: $name" ;;
    esac
    line=$(awk -F'\t' -v n="$name" \
        '$0 !~ /^#/ && $1 == n { print; exit }' "$tsv")
    [ -n "$line" ] || die "no such source: $name"
    url=$(printf '%s' "$line" | cut -f2)
    sha=$(printf '%s' "$line" | cut -f3)
    case $field in
        url)    printf '%s\n' "$url" ;;
        sha256) printf '%s\n' "$sha" ;;
        *)      die "unknown field: $field" ;;
    esac
}
