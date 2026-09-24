#!/usr/bin/env bats
#
# The checks that guard what this project ships, and what it is made of.
#
# Two of them pass by construction today -- the tracked tree carries no
# Apple bytes because the tool fetches at runtime, and no pin has moved
# because nobody has moved one. A check that can only pass is a check
# nobody trusts, so each one here is also fired at a planted violation.

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
}

# A throwaway git repository holding a copy of bin/ and lib/, so a planted
# violation can be committed without touching this one.
#
# Every step below is `|| return 1`: this runs as `dir="$(make_repo)"`,
# a command substitution assigned to a variable, and bash's errexit does
# NOT propagate out of that -- a failing `cp` here would otherwise be
# silently swallowed, `printf` would still print a path, and every test
# that follows would run against a directory quietly missing a file, and
# pass or fail for the wrong reason. The explicit existence checks
# after the copies are the same insurance one step further out: a `cp`
# that "succeeds" against the wrong source (or a future reorganization
# that changes what's copied) still leaves the file where the rest of
# this function, and every caller, expects it.
make_repo() {
    local dir="$BATS_TEST_TMPDIR/planted"
    mkdir -p "$dir/bin" "$dir/lib" "$dir/assets/pins" "$dir/assets/firmware" \
        "$dir/components/openssh" || return 1
    cp "$REPO/bin/no-apple-bytes.sh" "$dir/bin/" || return 1
    cp "$REPO/lib/common.sh" "$dir/lib/" || return 1
    cp "$REPO/assets/pins/sources.tsv" "$dir/assets/pins/" || return 1
    cp "$REPO/assets/firmware/config.plist" "$dir/assets/firmware/" || return 1
    cp "$REPO/components/openssh/version" "$dir/components/openssh/" || return 1
    [ -f "$dir/bin/no-apple-bytes.sh" ] || return 1
    [ -f "$dir/lib/common.sh" ] || return 1
    [ -f "$dir/assets/pins/sources.tsv" ] || return 1
    [ -f "$dir/assets/firmware/config.plist" ] || return 1
    [ -f "$dir/components/openssh/version" ] || return 1
    git -C "$dir" init -q || return 1
    git -C "$dir" config user.email t@example.invalid || return 1
    git -C "$dir" config user.name t || return 1
    git -C "$dir" add -A || return 1
    git -C "$dir" -c commit.gpgsign=false commit -qm first || return 1
    printf '%s\n' "$dir"
}

# --- no Apple-derived bytes ------------------------------------------------

@test "the tracked tree carries no Apple-derived bytes" {
    run "$REPO/bin/no-apple-bytes.sh"
    [ "$status" -eq 0 ]
}

@test "a committed disk image is caught by name" {
    dir="$(make_repo)"
    [ -n "$dir" ]
    printf 'not really\n' > "$dir/InstallESD.dmg"
    git -C "$dir" add -A
    git -C "$dir" -c commit.gpgsign=false commit -qm planted
    run bash -c "cd '$dir' && ./bin/no-apple-bytes.sh"
    [ "$status" -ne 0 ]
    [[ "$output" == *"InstallESD.dmg"* ]]
}

@test "a committed flat package is caught by its bytes, not its name" {
    # The name check is defeated by `mv`. The magic number is not.
    dir="$(make_repo)"
    [ -n "$dir" ]
    mkdir -p "$dir/docs"
    printf 'xar!and then some payload\n' > "$dir/docs/notes.txt"
    git -C "$dir" add -A
    git -C "$dir" -c commit.gpgsign=false commit -qm planted
    run bash -c "cd '$dir' && ./bin/no-apple-bytes.sh"
    [ "$status" -ne 0 ]
    [[ "$output" == *"xar!"* ]]
}

@test "a large committed blob is caught even with neither name nor magic" {
    dir="$(make_repo)"
    [ -n "$dir" ]
    head -c 3000000 /dev/zero > "$dir/docs-appendix"
    git -C "$dir" add -A
    git -C "$dir" -c commit.gpgsign=false commit -qm planted
    run bash -c "cd '$dir' && ./bin/no-apple-bytes.sh"
    [ "$status" -ne 0 ]
    [[ "$output" == *"docs-appendix"* ]]
}

@test "Apple's media is registered as a URL, never as a path in the tree" {
    run awk -F'\t' '$1 ~ /^apple-/ { print $2 }' "$REPO/assets/pins/sources.tsv"
    [ -n "$output" ]
    [[ "$output" == http://* || "$output" == https://* ]]
}

@test "no-apple-bytes checks a named ref, which is what a release will pass it" {
    dir="$(make_repo)"
    [ -n "$dir" ]
    run bash -c "cd '$dir' && ./bin/no-apple-bytes.sh HEAD"
    [ "$status" -eq 0 ]
}

@test "a violation on a TAG is caught when that tag is checked" {
    # The release path checks the tag it is about to publish, not the
    # working tree. A planted .dmg at that tag must fail it.
    dir="$(make_repo)"
    [ -n "$dir" ]
    printf 'not really\n' > "$dir/InstallESD.dmg"
    git -C "$dir" add -A
    git -C "$dir" -c commit.gpgsign=false commit -qm planted
    git -C "$dir" tag 20260922.1
    run bash -c "cd '$dir' && ./bin/no-apple-bytes.sh 20260922.1"
    [ "$status" -ne 0 ]
    [[ "$output" == *"InstallESD.dmg"* ]]
}

@test "a clean tag passes even when the WORKING TREE has Apple's media beside it" {
    # This is how the project is meant to work: decisions/0003 puts images
    # on local disk outside the repo. An untracked InstallESD.dmg must not
    # redden a release.
    dir="$(make_repo)"
    [ -n "$dir" ]
    git -C "$dir" tag 20260922.1
    printf 'x\n' > "$dir/InstallESD.dmg"
    run bash -c "cd '$dir' && ./bin/no-apple-bytes.sh 20260922.1"
    [ "$status" -eq 0 ]
}

@test "an unknown ref is a failure, never a pass, and names the ref" {
    # Cannot-verify is a FAILURE. A release gate that green-lights because
    # it could not find the tag is worse than no gate. It should also say
    # WHICH ref it could not find, not just that something failed.
    dir="$(make_repo)"
    [ -n "$dir" ]
    run bash -c "cd '$dir' && ./bin/no-apple-bytes.sh 19700101.1"
    [ "$status" -ne 0 ]
    [[ "$output" == *"19700101.1"* ]]
}

@test "a violation on a TAG is caught even though the same file was removed from the index afterward" {
    # This is the case that distinguishes checking the ref from checking
    # the index: a planted .dmg is committed and tagged, then git-rm'd in a
    # later commit. Checking the TAG must still fail, naming the file --
    # that .dmg really did ship in the tagged tree. Checking with no ref
    # (today's index behavior) must pass, because the index has moved on.
    dir="$(make_repo)"
    [ -n "$dir" ]
    printf 'not really\n' > "$dir/InstallESD.dmg"
    git -C "$dir" add -A
    git -C "$dir" -c commit.gpgsign=false commit -qm planted
    git -C "$dir" tag 20260922.2
    git -C "$dir" rm -q InstallESD.dmg
    git -C "$dir" -c commit.gpgsign=false commit -qm removed
    run bash -c "cd '$dir' && ./bin/no-apple-bytes.sh 20260922.2"
    [ "$status" -ne 0 ]
    [[ "$output" == *"InstallESD.dmg"* ]]
    run bash -c "cd '$dir' && ./bin/no-apple-bytes.sh"
    [ "$status" -eq 0 ]
}

# --- C-quoting, pipefail/SIGPIPE, and unreadable blobs --------------------
#
# Three defects each let the checks above PASS on bytes `git archive
# <tag>` would package, in both modes: a path git C-quotes (non-ASCII, a
# double quote) matched no pattern and could not be opened; `head -c 4`
# SIGPIPEd a large blob's `git show` under pipefail, so the byte check
# skipped anything past a pipe buffer; and an unreadable blob was
# skipped rather than flagged. Each was reproduced against the pre-fix
# script before it was fixed; these tests hold the fixes in place.

@test "a disk image named with a non-ASCII byte is caught in ref mode" {
    # Without -z, `git ls-tree`/`git ls-files` C-quote a non-ASCII path:
    # é.dmg becomes the literal characters "\303\251.dmg" on stdout, which
    # matches none of the *.dmg patterns and is not a path `git show` can
    # open either.
    dir="$(make_repo)"
    [ -n "$dir" ]
    printf 'not really\n' > "$dir/é.dmg"
    git -C "$dir" add -A
    git -C "$dir" -c commit.gpgsign=false commit -qm planted
    git -C "$dir" tag t1
    run bash -c "cd '$dir' && ./bin/no-apple-bytes.sh t1"
    [ "$status" -ne 0 ]
}

@test "a disk image named with a non-ASCII byte is caught in index mode" {
    dir="$(make_repo)"
    [ -n "$dir" ]
    printf 'not really\n' > "$dir/é.dmg"
    git -C "$dir" add -A
    git -C "$dir" -c commit.gpgsign=false commit -qm planted
    run bash -c "cd '$dir' && ./bin/no-apple-bytes.sh"
    [ "$status" -ne 0 ]
}

@test "a large blob named with a non-ASCII byte is caught in ref mode" {
    dir="$(make_repo)"
    [ -n "$dir" ]
    head -c 3000000 /dev/zero > "$dir/ü"
    git -C "$dir" add -A
    git -C "$dir" -c commit.gpgsign=false commit -qm planted
    git -C "$dir" tag t1
    run bash -c "cd '$dir' && ./bin/no-apple-bytes.sh t1"
    [ "$status" -ne 0 ]
}

@test "a large blob named with a non-ASCII byte is caught in index mode" {
    dir="$(make_repo)"
    [ -n "$dir" ]
    head -c 3000000 /dev/zero > "$dir/ü"
    git -C "$dir" add -A
    git -C "$dir" -c commit.gpgsign=false commit -qm planted
    run bash -c "cd '$dir' && ./bin/no-apple-bytes.sh"
    [ "$status" -ne 0 ]
}

@test "a filename holding a double quote is caught, not silently C-quoted away" {
    dir="$(make_repo)"
    [ -n "$dir" ]
    printf 'not really\n' > "$dir/a\"b.dmg"
    git -C "$dir" add -A
    git -C "$dir" -c commit.gpgsign=false commit -qm planted
    run bash -c "cd '$dir' && ./bin/no-apple-bytes.sh"
    [ "$status" -ne 0 ]
}

@test "a Mach-O file well over a pipe buffer is still caught by its bytes, in ref mode" {
    # `head -c 4` exits the instant it has its four bytes. Without
    # `set +o pipefail` scoped to just that capture, the upstream `git
    # show` -- which can be streaming a multi-megabyte blob -- gets
    # SIGPIPE, dies with 141, and the script's own `set -o pipefail` fails
    # the whole pipeline, so anything past a pipe buffer's worth (~64 KiB)
    # silently skipped this check no matter what its header said. 200 KB
    # is comfortably past that and comfortably under the 2 MiB size gate,
    # so this exercises the byte check specifically, not the size check.
    dir="$(make_repo)"
    [ -n "$dir" ]
    { printf '\xcf\xfa\xed\xfe'; head -c 200000 /dev/zero; } > "$dir/innocuous.bin"
    git -C "$dir" add -A
    git -C "$dir" -c commit.gpgsign=false commit -qm planted
    git -C "$dir" tag t1
    run bash -c "cd '$dir' && ./bin/no-apple-bytes.sh t1"
    [ "$status" -ne 0 ]
    [[ "$output" == *"Mach-O"* ]]
}

@test "a Mach-O file well over a pipe buffer is still caught by its bytes, in index mode" {
    dir="$(make_repo)"
    [ -n "$dir" ]
    { printf '\xcf\xfa\xed\xfe'; head -c 200000 /dev/zero; } > "$dir/innocuous.bin"
    git -C "$dir" add -A
    git -C "$dir" -c commit.gpgsign=false commit -qm planted
    run bash -c "cd '$dir' && ./bin/no-apple-bytes.sh"
    [ "$status" -ne 0 ]
    [[ "$output" == *"Mach-O"* ]]
}

@test "a blob that cannot be read is a failure, not a silent skip" {
    # Cannot-verify must never read as clean. Commit a file, tag it, then
    # delete its own loose object so nothing can actually read the blob
    # back -- the corruption case the readability check exists to catch.
    dir="$(make_repo)"
    [ -n "$dir" ]
    printf 'whatever\n' > "$dir/plugin.bin"
    git -C "$dir" add -A
    git -C "$dir" -c commit.gpgsign=false commit -qm planted
    git -C "$dir" tag t1
    sha=$(git -C "$dir" rev-parse "t1:plugin.bin")
    obj="$dir/.git/objects/${sha:0:2}/${sha:2}"
    [ -f "$obj" ]
    rm -f "$obj"
    run bash -c "cd '$dir' && ./bin/no-apple-bytes.sh t1"
    [ "$status" -ne 0 ]
    [[ "$output" == *"plugin.bin"* ]]
    [[ "$output" == *"cannot be read"* ]]
}

@test "index mode with nothing in the index is a failure, not a pass on 0 files" {
    # "no Apple-derived bytes in the tracked tree (0 files)" was a pass.
    # Checking nothing is cannot-verify, which is never a pass. A fresh
    # repository has no index file at all; `git rm --cached` leaves an
    # empty one. Both.
    dir="$BATS_TEST_TMPDIR/empty"
    mkdir -p "$dir/bin" "$dir/lib"
    cp "$REPO/bin/no-apple-bytes.sh" "$dir/bin/"
    cp "$REPO/lib/common.sh" "$dir/lib/"
    git -C "$dir" init -q
    [ ! -e "$dir/.git/index" ]
    run bash -c "cd '$dir' && ./bin/no-apple-bytes.sh"
    [ "$status" -ne 0 ]
    [[ "$output" == *"cannot verify"* ]]
    [[ "$output" != *"no Apple-derived bytes"* ]]

    dir="$(make_repo)"
    [ -n "$dir" ]
    git -C "$dir" rm -rq --cached .
    [ -e "$dir/.git/index" ]
    run bash -c "cd '$dir' && ./bin/no-apple-bytes.sh"
    [ "$status" -ne 0 ]
    [[ "$output" == *"cannot verify"* ]]
}

@test "index mode reads the file named 0:evil, not stage 0 of evil" {
    # `git show ":$f"` parses ":0:evil" as stage 0 of "evil". With a benign
    # "evil" beside it, the byte check read the wrong blob and the
    # Mach-O in "0:evil" passed (MEASURED). ":0:$f" names
    # stage 0 explicitly, so the rest is the path, whatever it holds.
    dir="$(make_repo)"
    [ -n "$dir" ]
    printf 'plain text\n' > "$dir/evil"
    printf '\xcf\xfa\xed\xfe\x00\x00\x00\x00' > "$dir/0:evil"
    git -C "$dir" add -A
    git -C "$dir" -c commit.gpgsign=false commit -qm planted
    run bash -c "cd '$dir' && ./bin/no-apple-bytes.sh"
    [ "$status" -ne 0 ]
    [[ "$output" == *"0:evil"*"Mach-O"* ]]
    # Ref mode already read "<sha>:0:evil" as a path; it still does.
    git -C "$dir" tag t1
    run bash -c "cd '$dir' && ./bin/no-apple-bytes.sh t1"
    [ "$status" -ne 0 ]
    [[ "$output" == *"0:evil"*"Mach-O"* ]]
}

@test "a path holding a space is checked correctly in ref mode" {
    dir="$(make_repo)"
    [ -n "$dir" ]
    mkdir -p "$dir/docs"
    printf 'not really\n' > "$dir/docs/install esd.dmg"
    git -C "$dir" add -A
    git -C "$dir" -c commit.gpgsign=false commit -qm planted
    git -C "$dir" tag t1
    run bash -c "cd '$dir' && ./bin/no-apple-bytes.sh t1"
    [ "$status" -ne 0 ]
    [[ "$output" == *"install esd.dmg"* ]]
}

@test "the byte check runs in ref mode against the ref's own content, not the index" {
    # A flat package planted, tagged, then git-rm'd -- same shape as the
    # index-vs-ref test above, but for section 2 (magic bytes) rather than
    # section 1 (name).
    dir="$(make_repo)"
    [ -n "$dir" ]
    mkdir -p "$dir/docs"
    printf 'xar!payload\n' > "$dir/docs/notes.txt"
    git -C "$dir" add -A
    git -C "$dir" -c commit.gpgsign=false commit -qm planted
    git -C "$dir" tag t1
    git -C "$dir" rm -q docs/notes.txt
    git -C "$dir" -c commit.gpgsign=false commit -qm removed
    run bash -c "cd '$dir' && ./bin/no-apple-bytes.sh t1"
    [ "$status" -ne 0 ]
    [[ "$output" == *"xar!"* ]]
    run bash -c "cd '$dir' && ./bin/no-apple-bytes.sh"
    [ "$status" -eq 0 ]
}

@test "the size check runs in ref mode against the ref's own content, not the index" {
    dir="$(make_repo)"
    [ -n "$dir" ]
    head -c 3000000 /dev/zero > "$dir/docs-appendix"
    git -C "$dir" add -A
    git -C "$dir" -c commit.gpgsign=false commit -qm planted
    git -C "$dir" tag t1
    git -C "$dir" rm -q docs-appendix
    git -C "$dir" -c commit.gpgsign=false commit -qm removed
    run bash -c "cd '$dir' && ./bin/no-apple-bytes.sh t1"
    [ "$status" -ne 0 ]
    [[ "$output" == *"docs-appendix"* ]]
    run bash -c "cd '$dir' && ./bin/no-apple-bytes.sh"
    [ "$status" -eq 0 ]
}

@test "the registry check runs in ref mode against the ref's own assets/pins/sources.tsv" {
    dir="$(make_repo)"
    [ -n "$dir" ]
    printf 'apple-bogus\t/not/a/url\tdeadbeef\n' >> "$dir/assets/pins/sources.tsv"
    git -C "$dir" add -A
    git -C "$dir" -c commit.gpgsign=false commit -qm planted
    git -C "$dir" tag t1
    git -C "$dir" checkout -q HEAD~1 -- assets/pins/sources.tsv
    git -C "$dir" add -A
    git -C "$dir" -c commit.gpgsign=false commit -qm reverted
    run bash -c "cd '$dir' && ./bin/no-apple-bytes.sh t1"
    [ "$status" -ne 0 ]
    [[ "$output" == *"apple-bogus"* ]]
    run bash -c "cd '$dir' && ./bin/no-apple-bytes.sh"
    [ "$status" -eq 0 ]
}

# --- the family wiring -----------------------------------------------------

@test "build/msc.sh is shipyard's template and carries the do-not-edit note" {
    [ -f "$REPO/build/msc.sh" ]
    # The gate compares it byte for byte with
    # $SHIPYARD_SCRIPTS/templates/msc.sh. We cannot reach shipyard from
    # here, so check the marker the template carries: an edited copy is
    # overwhelmingly likely to have lost or reworded it.
    run grep -c 'CANONICAL COPY' "$REPO/build/msc.sh"
    [ "$output" = "1" ]
    run grep -c 'SHIPYARD_SCRIPTS' "$REPO/build/msc.sh"
    [ "$output" -ge 1 ]
}

@test "the marketplace is registered so a contributor's agent loads the conventions" {
    run python3 -c "
import json
cfg = json.load(open('$REPO/.claude/settings.json'))
assert cfg['extraKnownMarketplaces']['mavergreen']['source']['repo'] \
    == 'Mavergreen/shipyard', cfg
assert cfg['enabledPlugins']['mavergreen@mavergreen'] is True, cfg
print('ok')
"
    [ "$status" -eq 0 ]
}

# --- the README as product documentation ----------------------------------

@test "the README leads with what the tool does, not with the host it was built on" {
    run head -12 "$REPO/README.md"
    [[ "$output" == *"packer-plugin-mavericks"* ]]
    [[ "$output" != *"Mac mini 2018"* ]]
    [[ "$output" != *"Linux Mint"* ]]
}

@test "the README states the never-publish rule above the fold" {
    run head -90 "$REPO/README.md"
    [[ "$output" == *"Never publish either one"* ]]
    [[ "$output" == *"Apple"* ]]
}

@test "the README carries no unread-by-a-human marker" {
    # publish-release.yml refuses a first release while that line stands.
    run grep -c 'not been read or edited by a human' "$REPO/README.md" || true
    [ "$output" = "0" ]
}
