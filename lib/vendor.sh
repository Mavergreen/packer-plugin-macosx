# shellcheck shell=bash
# The third-party artifact registry.
#
# assets/pins/sources.tsv is tab-separated: name, url, sha256. A sha256 of TOFU
# means "not yet known": trust on first use, until fetch_source fetches it
# once and pin_checksum pins what it got, for a human to review and commit.
#
# Requires lib/common.sh to be sourced first.

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

# pin_checksum <tsv> <name> <sha256>
# Only ever promotes TOFU to a real value. Refuses to change a pinned one,
# because a checksum that silently changes is the whole problem.
pin_checksum() {
    local tsv=$1 name=$2 sha=$3 current tmp mode
    current=$(source_field "$tsv" "$name" sha256) || exit 1
    if [ "$current" != "TOFU" ]; then
        die "source $name is already pinned to $current; refusing to change it"
    fi
    # Create the temp file next to the target rather than under mktemp's
    # default directory (usually /tmp): on this project /tmp and the repo
    # can be different filesystems, and more importantly mktemp's 600 mode
    # would otherwise clobber the original file's permissions -- mv/rename
    # replaces the destination inode wholesale, it does not preserve the
    # destination's old mode. Explicitly carrying the mode over avoids
    # silently tightening (or loosening) sources.tsv's permissions.
    tmp=$(mktemp "$(dirname -- "$tsv")/.$(basename -- "$tsv").XXXXXX") \
        || die "cannot create temp file for $tsv"
    mode=$(stat -c '%a' "$tsv" 2>/dev/null || stat -f '%Lp' "$tsv" 2>/dev/null)
    awk -F'\t' -v OFS='\t' -v n="$name" -v s="$sha" \
        '$0 !~ /^#/ && $1 == n { $3 = s } { print }' "$tsv" > "$tmp"
    [ -n "$mode" ] && chmod "$mode" "$tmp"
    mv -- "$tmp" "$tsv" || die "cannot replace $tsv (left as $tmp)"
}

# fetch_source <tsv> <name> <destdir>
# Downloads if absent, then verifies. On TOFU, pins and tells the operator
# to commit.
fetch_source() {
    local tsv=$1 name=$2 destdir=$3 url sha dest got filename
    url=$(source_field "$tsv" "$name" url) || exit 1
    sha=$(source_field "$tsv" "$name" sha256) || exit 1
    mkdir -p "$destdir"

    # basename on a URL with no filename component silently falls back to
    # something else -- the bare hostname, or a directory name -- so a
    # typo'd or truncated URL would download to a confidently-wrong
    # filename. Guessing the *right* name is speculative and out of
    # scope; refusing to proceed never is. This only rejects the two
    # "there is nothing sensible to call this" shapes -- no path at all
    # beyond the host, or a path ending in "/" -- rather than policing the
    # character content of the name: a query string like "?v=2" is ugly
    # appended to a filename but not nonsensical, and rejecting it would
    # refuse a working (if ugly) URL for no safety gain.
    case $url in
        *://*/*) : ;;
        *) die "cannot derive a filename from $url" \
               "-- it has no path; give the source a URL ending in a filename" ;;
    esac
    filename=${url##*/}
    if [ -z "$filename" ]; then
        die "cannot derive a filename from $url" \
            "-- it ends in \"/\"; give the source a URL ending in a filename"
    fi
    dest="$destdir/$filename"

    if [ ! -f "$dest" ]; then
        log "fetching $name from $url"
        curl -fSL --retry 3 -o "$dest.part" "$url" \
            || die "download failed for $name"
        mv "$dest.part" "$dest" \
            || die "cannot move downloaded file into place: $dest"
    else
        log "$name already present at $dest"
    fi

    if [ "$sha" = "TOFU" ]; then
        got=$(sha256_file "$dest")
        pin_checksum "$tsv" "$name" "$got"
        warn "pinned $name to $got on first use"
        warn "review and commit the change to $tsv"
    else
        verify_sha256 "$dest" "$sha"
        log "$name verified against pinned checksum"
    fi

    printf '%s\n' "$dest"
}
