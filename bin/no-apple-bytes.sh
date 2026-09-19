#!/usr/bin/env bash
# The one conformance check that is ours alone: A RELEASE MUST CONTAIN NO
# APPLE-DERIVED BYTES.
#
# WHY THIS IS A GATE AND NOT A HABIT
#
# "Never publish the guest image" and "the OS comes from Apple only" are
# the two rules this project cannot get wrong once. Both are currently
# satisfied by construction: we ship a recipe, and the recipe fetches
# Apple's InstallESD.dmg at RUNTIME, on the user's machine, from Apple.
# Nothing Apple-derived is ever committed.
#
# Satisfied-by-construction is exactly the condition under which a rule
# quietly stops being true. A 6 GB .dmg committed "just for a minute" to
# debug something, a BaseSystem extract left in a fixtures directory, a
# kext copied out of a guest -- each is one `git add -A` away, and none of
# them looks wrong in a diff that is already large. The cost of checking is
# a second per CI run. The cost of finding out later is a DMCA notice.
#
# WHAT IT CHECKS
#
# Exactly what a release would contain: what `git archive REF` would
# package -- REF's tree, not the working directory. Give it a ref (a tag,
# almost always) and it checks exactly that tree via `git ls-tree`,
# `git cat-file` and `git show REF:path`. Give it nothing and it checks
# the index instead, which is today's pre-release approximation of the
# same tree. Either way, an untracked InstallESD.dmg beside the repo is
# how this project is MEANT to work (see decisions/0003: what the build
# downloads and makes lives outside the repo) and must never redden
# either mode.
#
# A ref that does not resolve is a FAILURE, not a pass: cannot-verify is
# not the same answer as verified-clean, and a release gate that
# green-lights because it could not find the tag is worse than no gate.
# The same rule applies one level down, per file: a blob this script
# cannot read (a stripped loose object, a listing command that itself
# failed) is flagged rather than silently skipped -- see check 0 below.
#
#   usage: bin/no-apple-bytes.sh [ref]     (default: the working tree's index)
set -euo pipefail

MQG_REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# shellcheck source=../lib/common.sh
. "$MQG_REPO_ROOT/lib/common.sh"

# shellcheck disable=SC2034  # read by log()/warn()/die() at call time
MQG_LOG_PREFIX=no-apple-bytes

MAX_TRACKED_BYTES=${MQG_MAX_TRACKED_BYTES:-2097152}

cd "$MQG_REPO_ROOT"

git rev-parse --git-dir >/dev/null 2>&1 \
    || die "not a git repository -- a release is what git would archive," \
           "so there is nothing to check here"

# Resolved to a full SHA once, up front, rather than re-resolved by every
# `git show`/`git cat-file` call below -- a tag or branch given as $1 could
# otherwise move mid-run and every check would not be looking at the same
# tree throughout.
ref_arg=${1:-}
MQG_NAB_REF=""
if [ -n "$ref_arg" ]; then
    MQG_NAB_REF=$(git rev-parse --verify --quiet "${ref_arg}^{commit}") \
        || die "no such ref: $ref_arg -- cannot verify a release that" \
               "does not resolve, which is a failure, never a pass"
fi

# The three primitives, switched once on whether a ref was given, so every
# check below reads the same regardless of mode. Ref mode is exactly what
# `git archive` packages; no-ref mode is the index, today's behavior.
#
# Index mode spells stage 0 out -- ":0:$1", not ":$1". Git reads ":0:evil"
# as stage 0 of "evil", so a tracked file NAMED "0:evil" was never read:
# with a benign "evil" beside it, the byte check looked at the wrong blob
# and a Mach-O in "0:evil" passed (MEASURED). With the stage
# given, everything after the second colon is the path. Ref mode's
# "<sha>:<path>" never had that ambiguity and is unchanged.
blob_show() {  # $1 = path -- the committed content, never the working tree
    if [ -n "$MQG_NAB_REF" ]; then
        git show "$MQG_NAB_REF:$1" 2>/dev/null
    else
        git show ":0:$1" 2>/dev/null
    fi
}

blob_size() {  # $1 = path
    if [ -n "$MQG_NAB_REF" ]; then
        git cat-file -s "$MQG_NAB_REF:$1" 2>/dev/null
    else
        git cat-file -s ":0:$1" 2>/dev/null
    fi
}

blob_exists() {  # $1 = path -- true only if the blob can actually be read
    if [ -n "$MQG_NAB_REF" ]; then
        git cat-file -e "$MQG_NAB_REF:$1" 2>/dev/null
    else
        git cat-file -e ":0:$1" 2>/dev/null
    fi
}

# The tracked-file list, built once into a scratch file rather than piped
# straight into each `while read`, for two reasons:
#
#   1. `-z` (NUL-delimited), not the default newline-delimited listing.
#      Without it, `git ls-tree`/`git ls-files` C-QUOTES any path holding a
#      non-ASCII byte, a double quote, a backslash or a tab -- `é.dmg`
#      comes back as the literal eleven characters `"\303\251.dmg"`, which
#      matches none of section 1's `*.dmg` patterns, and is not a path
#      `git show`/`git cat-file` can open either (they want the real
#      bytes, not their octal-escaped spelling). `-z` sidesteps quoting
#      entirely, and `read -r -d ''` on the reading end handles it --
#      both bash 3.0-era features.
#   2. The listing command's OWN exit status is checked, not `|| true`'d
#      away. A `git ls-tree`/`git ls-files` that fails outright would
#      otherwise print nothing, every `while read` loop below would then
#      iterate zero times, and the script would exit 0 having "checked" an
#      empty list -- cannot-verify reading as clean, same failure mode as
#      the unresolved-ref case above, one layer down.
MQG_NAB_TMPDIR=$(mktemp -d) || die "cannot create a scratch directory"
trap 'rm -rf "$MQG_NAB_TMPDIR"' EXIT
MQG_NAB_LIST=$MQG_NAB_TMPDIR/tracked
if [ -n "$MQG_NAB_REF" ]; then
    git ls-tree -r -z --name-only "$MQG_NAB_REF" > "$MQG_NAB_LIST" \
        || die "git ls-tree failed for $MQG_NAB_REF -- cannot verify," \
               "which is a failure, never a pass"
else
    git ls-files -z > "$MQG_NAB_LIST" \
        || die "git ls-files failed -- cannot verify, which is a failure," \
               "never a pass"
fi

# Nothing listed is nothing checked. An empty or missing index (a fresh
# `git init`, or everything `git rm --cached`) would otherwise end in "no
# Apple-derived bytes in the tracked tree (0 files)" and exit 0 -- a pass
# on a check that looked at nothing. Applied in both modes: a ref whose
# tree is empty is not a release anyone can have meant either.
nfiles=$(tr -cd '\0' < "$MQG_NAB_LIST" | wc -c | tr -d ' ')
[ "$nfiles" -gt 0 ] \
    || die "no tracked files to check -- cannot verify, which is a failure," \
           "never a pass"

status=0
flag() {
    printf 'APPLE-DERIVED  %s\n' "$1" >&2
    printf '               %s\n' "$2" >&2
    status=1
}

# 0. Readability. Every check below reads a blob by path. If the blob is
#    gone -- a stripped loose object, a corrupt pack -- `git cat-file`
#    fails, and left unhandled that failure looks exactly like "nothing to
#    flag here": cannot-verify passing as clean, the one failure mode this
#    whole script exists to refuse. Checked once, explicitly, before
#    sections 1-3 read a single byte, so a read failure downstream can be
#    treated as already-handled rather than re-litigated three times with
#    three different `|| continue`s that would otherwise swallow it.
while IFS= read -r -d '' f; do
    [ -n "$f" ] || continue
    blob_exists "$f" \
        || flag "$f" "cannot be read -- cannot verify is a failure, never a pass"
done < "$MQG_NAB_LIST"

# 1. Shapes. A disk image, an installer package or a kext bundle in the
#    tracked tree is, in this project, almost certainly Apple's -- and the
#    one package we DO build (assets/guest/) is built at runtime into
#    Packer's cache directory, never committed. The exceptions list is deliberately
#    empty; an entry here would need a reason in INGREDIENTS.md's
#    deviations block, like any other.
while IFS= read -r -d '' f; do
    [ -n "$f" ] || continue
    case $f in
        *.dmg|*.DMG|*.sparseimage|*.sparsebundle)
            flag "$f" "a disk image -- Apple's OS media never enters this repo" ;;
        *.pkg|*.mpkg)
            flag "$f" "an installer package -- ours are built at runtime, into Packer's cache directory" ;;
        *.kext|*.kext/*)
            flag "$f" "a kernel extension bundle" ;;
        *.iso|*.cdr|*.img)
            flag "$f" "an optical/disk image" ;;
        *InstallESD*|*BaseSystem*|*OSInstall.mpkg*)
            flag "$f" "named after part of Apple's installer" ;;
    esac
done < "$MQG_NAB_LIST"

# 2. Bytes. A renamed blob defeats the list above, so look at the first
#    few bytes of every tracked file that is not plainly text. These are
#    the magic numbers of the things this project handles.
#
#    The committed content of each file (via `blob_show`), never the
#    working tree, because the release is made from what is COMMITTED. A
#    file whose working copy was cleaned up but whose committed version is
#    a 6 GB dmg still ships one -- and in ref mode, "committed" means AT
#    THAT REF, which may differ from what the index holds today.
while IFS= read -r -d '' f; do
    [ -n "$f" ] || continue
    # `set +o pipefail` here, scoped to this command substitution's own
    # subshell only -- it does not leak to the rest of the script. Without
    # it: `head -c 4` reads its four bytes and exits; `blob_show` (a
    # `git show` that may be streaming a multi-gigabyte blob) is still
    # writing and gets SIGPIPE, dying with 141; under the script's own
    # `set -o pipefail` that 141 fails the whole pipeline; and `|| continue`
    # then skipped the file -- so anything past a pipe buffer's worth
    # (~64 KiB) silently passed this check no matter what its first four
    # bytes were. Section 0 above already turned an unreadable blob into a
    # flag, so nothing here needs to notice that case again; an empty
    # `$head4` simply matches none of the patterns below.
    head4=$(set +o pipefail; blob_show "$f" | head -c 4 | od -An -tx1 | tr -d ' \n')
    case $head4 in
        # "koly" is the HFS+ disk-image trailer, but an Apple .dmg starts
        # with either the UDIF header or zlib; check for the ones that are
        # unambiguous at offset 0.
        78617221)  # "xar!" -- a flat package
            flag "$f" "starts with xar! -- a flat installer package" ;;
        482b0004|48580005)  # "H+", "HX" -- a bare HFS+/HFSX volume
            flag "$f" "starts with an HFS+ volume signature" ;;
        6b6f6c79)  # "koly"
            flag "$f" "starts with koly -- a UDIF disk image trailer" ;;
        cafebabe|cffaedfe|cefaedfe)  # Mach-O, fat or thin
            flag "$f" "is a Mach-O binary -- this repository ships source, not binaries" ;;
    esac
done < "$MQG_NAB_LIST"

# 3. Size. Nothing legitimate here is large, and every Apple artifact is.
#    A 6 GB blob under another name and with a scrubbed header is still a
#    6 GB blob, and this catches it without knowing what it is.
#    (MAX_TRACKED_BYTES is set near the top, shared with archive mode.)
while IFS= read -r -d '' f; do
    [ -n "$f" ] || continue
    size=$(blob_size "$f") || continue
    if [ "$size" -gt "$MAX_TRACKED_BYTES" ]; then
        flag "$f" "$size bytes -- nothing this project authors is that large;" \
             "if it is genuinely ours, raise MQG_MAX_TRACKED_BYTES with a reason"
    fi
done < "$MQG_NAB_LIST"

# 4. The registry's own statement. assets/pins/sources.tsv names Apple's
#    InstallESD as a URL fetched at runtime. If it ever named a path
#    inside the repository instead, the rule would be broken by the
#    registry rather than by a file.
#
#    Read via `blob_show`, not off disk -- the registry AT THE REF being
#    checked is what matters, not whatever the working tree happens to
#    hold right now.
registry_content=$(blob_show assets/pins/sources.tsv) || registry_content=""
if printf '%s\n' "$registry_content" | grep -q '^apple-'; then
    while IFS=$'\t' read -r name url _; do
        case $name in apple-*) : ;; *) continue ;; esac
        case $url in
            http://*|https://*) : ;;
            *) flag "assets/pins/sources.tsv:$name" \
                    "points at $url, not at a URL -- Apple's media must be" \
                    "fetched at runtime, never carried" ;;
        esac
    done <<EOF
$registry_content
EOF
fi

if [ "$status" -eq 0 ]; then
    log "no Apple-derived bytes in the tracked tree ($nfiles files)"
else
    die "the tracked tree carries Apple-derived bytes. A release of this" \
        "project is a RECIPE: it fetches Apple's media at runtime, on the" \
        "user's machine, and carries none of it. See README.md's ground rules."
fi
