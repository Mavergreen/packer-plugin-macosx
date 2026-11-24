#!/usr/bin/env bats
# platform: host-agnostic
# shellcheck disable=SC2154  # $stderr is set by bats run --separate-stderr
#
# bin/gate-scope.sh decides, from what a PR changed, whether CI runs the boot gate on it. It reads
# git and gate/scope.tsv, and runs bin/verify-changed-sources.sh, here a stand-in that records its
# arguments: no ssh, sudo or power command is reachable, so these tests need no stubs and are
# exempt from the stub-safety assertion (the plan's ruling R9).

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    R="$BATS_TEST_TMPDIR/repo"
    mkdir -p "$R/bin" "$R/lib" "$R/gate" "$R/assets/pins" "$R/assets/firmware/snowleopard" \
        "$R/components/openssh" "$R/templates/mavericks-aquatransport"
    cp "$REPO/bin/gate-scope.sh" "$R/bin/"
    cp "$REPO/lib/common.sh" "$R/lib/"
    cp "$REPO/gate/scope.tsv" "$R/gate/"
    echo pins > "$R/assets/pins/sources.tsv"
    echo plist > "$R/assets/firmware/config.plist"
    echo plist > "$R/assets/firmware/snowleopard/config.plist"
    echo 10.6p1 > "$R/components/openssh/version"
    echo hcl > "$R/templates/mavericks-aquatransport/aquatransport.pkr.hcl"
    echo readme > "$R/README.md"
    git -C "$R" init -q -b main
    git -C "$R" config user.email t@example.invalid
    git -C "$R" config user.name t
    git -C "$R" config commit.gpgsign false
    git -C "$R" add -A
    git -C "$R" commit -qm base
    BASE=$(git -C "$R" rev-parse HEAD)
    git -C "$R" checkout -q -b renovate/thing

    VERIFY="$BATS_TEST_TMPDIR/verify"
    VERIFY_LOG="$BATS_TEST_TMPDIR/verify.log"
    export VERIFY_LOG
    cat > "$VERIFY" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$VERIFY_LOG"
echo "verify-changed-sources: thing: does not match its pinned checksum." >&2
exit "${VERIFY_RC:-0}"
EOF
    chmod +x "$VERIFY"
    export GUEST_MACOSX_GATE_VERIFY_SOURCES="$VERIFY"
    unset GATE_FORCE VERIFY_RC
}

# change <path>...: commit a change to each path on the current branch; HEAD is the head
change() {
    local p
    for p in "$@"; do echo changed >> "$R/$p"; done
    git -C "$R" add -A
    git -C "$R" commit -qm change
    HEAD=$(git -C "$R" rev-parse HEAD)
}

scope() { run --separate-stderr "$R/bin/gate-scope.sh" "$@"; }

bats_require_minimum_version 1.5.0

@test "usage: anything but four arguments is exit 2" {
    scope "$BASE" HEAD renovate/thing
    [ "$status" -eq 2 ]
    scope "$BASE" HEAD renovate/thing pull_request extra
    [ "$status" -eq 2 ]
}

@test "a base or head that is not a commit is exit 2, naming it" {
    change README.md
    scope 0000000000000000000000000000000000000000 "$HEAD" renovate/thing pull_request
    [ "$status" -eq 2 ]
    [[ "$stderr" == *0000000000000000000000000000000000000000* ]] || false
}

@test "a release (workflow_call) is gated on every release, whatever changed and whatever the branch" {
    change README.md
    scope "$BASE" "$HEAD" main workflow_call
    [ "$status" -eq 0 ]
    [ "${#lines[@]}" -eq 2 ]
    [ "${lines[0]}" = "gated a release" ]
    [ "${lines[1]}" = "releases 10.9 10.6" ]
    [ ! -e "$VERIFY_LOG" ]
}

@test "a dispatch with force is gated on every release" {
    change README.md
    GATE_FORCE=true scope "$BASE" "$HEAD" main workflow_dispatch
    [ "$status" -eq 0 ]
    [ "${#lines[@]}" -eq 2 ]
    [ "${lines[0]}" = "gated dispatched with force" ]
    [ "${lines[1]}" = "releases 10.9 10.6" ]
}

@test "force counts only on a dispatch: a pull_request with GATE_FORCE set is judged as usual" {
    change README.md
    GATE_FORCE=true scope "$BASE" "$HEAD" feature/x pull_request
    [ "$output" = "not-gated not a Renovate update" ]
}

@test "a dispatch without force on a branch not Renovate's is not gated" {
    change assets/firmware/config.plist
    GATE_FORCE=false scope "$BASE" "$HEAD" main workflow_dispatch
    [ "$status" -eq 0 ]
    [ "$output" = "not-gated not a Renovate update" ]
}

@test "a PR not from Renovate is not gated, even when it touches a gated path" {
    change assets/firmware/config.plist
    scope "$BASE" "$HEAD" feature/firmware pull_request
    [ "$status" -eq 0 ]
    [ "$output" = "not-gated not a Renovate update" ]
    [ ! -e "$VERIFY_LOG" ]
}

@test "a Renovate PR that changes no ingredient path is not gated" {
    change README.md
    scope "$BASE" "$HEAD" renovate/thing pull_request
    [ "$status" -eq 0 ]
    [ "$output" = "not-gated no gated path changed" ]
    [ ! -e "$VERIFY_LOG" ]
}

@test "a Renovate PR touching only components/openssh/version is not gated, and says why" {
    change components/openssh/version
    scope "$BASE" "$HEAD" renovate/openssh pull_request
    [ "$status" -eq 0 ]
    [ "${#lines[@]}" -eq 1 ]
    [ "$output" = "not-gated no gated path changed; components/** is not gated: OpenSSH has its own on-VM smoke test before it releases; the owner, 2026-10-12" ]
}

@test "a Renovate PR touching only Snow Leopard's config.plist is gated on 10.6 alone" {
    change assets/firmware/snowleopard/config.plist
    scope "$BASE" "$HEAD" renovate/thing pull_request
    [ "$status" -eq 0 ]
    [ "${#lines[@]}" -eq 2 ]
    [ "${lines[0]}" = "gated changed: assets/firmware/snowleopard/config.plist" ]
    [ "${lines[1]}" = "releases 10.6" ]
}

@test "a Renovate PR moving a pin whose checksum is stale is pins-unfilled, verified against the base" {
    change assets/pins/sources.tsv
    VERIFY_RC=1 scope "$BASE" "$HEAD" renovate/thing pull_request
    [ "$status" -eq 0 ]
    [ "${#lines[@]}" -eq 1 ]
    [ "$output" = "pins-unfilled re-pin the changed assets/pins/sources.tsv row on this branch (bin/verify-changed-sources.sh names it); the pins bot is not built yet" ]
    [ "$(cat "$VERIFY_LOG")" = "$BASE" ]
    # what verify-changed-sources said reaches the log, for a person to read
    [[ "$stderr" == *"does not match its pinned checksum"* ]] || false
}

@test "a Renovate PR moving a pin that verifies is gated on every release, naming the path" {
    change assets/pins/sources.tsv
    scope "$BASE" "$HEAD" renovate/thing pull_request
    [ "$status" -eq 0 ]
    [ "${#lines[@]}" -eq 2 ]
    [ "${lines[0]}" = "gated changed: assets/pins/sources.tsv" ]
    [ "${lines[1]}" = "releases 10.9 10.6" ]
}

@test "an Aqua Transport bump is gated on 10.9 alone" {
    change templates/mavericks-aquatransport/aquatransport.pkr.hcl
    scope "$BASE" "$HEAD" renovate/wowfunhappy-aquatransport-2026.x pull_request
    [ "${#lines[@]}" -eq 2 ]
    [ "${lines[0]}" = "gated changed: templates/mavericks-aquatransport/aquatransport.pkr.hcl" ]
    [ "${lines[1]}" = "releases 10.9" ]
}

@test "10.9's config.plist is gated on 10.9 alone" {
    change assets/firmware/config.plist
    scope "$BASE" "$HEAD" renovate/thing pull_request
    [ "${lines[0]}" = "gated changed: assets/firmware/config.plist" ]
    [ "${lines[1]}" = "releases 10.9" ]
}

@test "gated names every gated path that changed, and not the skipped ones" {
    change assets/firmware/config.plist components/openssh/version assets/pins/sources.tsv
    scope "$BASE" "$HEAD" renovate/thing pull_request
    [ "${lines[0]}" = "gated changed: assets/pins/sources.tsv, assets/firmware/config.plist" ]
}

@test "the releases are the union of the rows hit, in GUEST_MACOSX_RELEASES order, not row order" {
    change assets/firmware/snowleopard/config.plist templates/mavericks-aquatransport/aquatransport.pkr.hcl
    scope "$BASE" "$HEAD" renovate/thing pull_request
    [ "${#lines[@]}" -eq 2 ]
    [ "${lines[1]}" = "releases 10.9 10.6" ]
}

@test "a not-gated change prints one line, naming no releases" {
    change README.md
    scope "$BASE" "$HEAD" renovate/thing pull_request
    [ "${#lines[@]}" -eq 1 ]
}

# bad_row <what>: append a row gating README.md as <what> to the copied scope.tsv
bad_row() {
    printf 'README.md\t%s\tbad\n' "$1" >> "$R/gate/scope.tsv"
}

@test "a gate row naming no release is exit 2, naming the row" {
    bad_row gate:
    change assets/pins/sources.tsv
    scope "$BASE" "$HEAD" renovate/thing pull_request
    [ "$status" -eq 2 ]
    [[ "$stderr" == *"README.md"*"gate:"* ]] || false
}

@test "a gate row naming a release not in GUEST_MACOSX_RELEASES is exit 2, naming the row" {
    bad_row gate:10.5
    change assets/pins/sources.tsv
    scope "$BASE" "$HEAD" renovate/thing pull_request
    [ "$status" -eq 2 ]
    [[ "$stderr" == *"README.md"*"gate:10.5"* ]] || false
}

@test "a gate row with an empty release in its list is exit 2, naming the row" {
    bad_row gate:10.9,,10.6
    change assets/pins/sources.tsv
    scope "$BASE" "$HEAD" renovate/thing pull_request
    [ "$status" -eq 2 ]
    [[ "$stderr" == *"README.md"*"gate:10.9,,10.6"* ]] || false
}

@test "a gate row ending in a comma is exit 2, naming the row" {
    bad_row gate:10.9,
    change assets/pins/sources.tsv
    scope "$BASE" "$HEAD" renovate/thing pull_request
    [ "$status" -eq 2 ]
    [[ "$stderr" == *"README.md"*"gate:10.9,"* ]] || false
}

@test "a bare gate row, naming no releases, is exit 2" {
    bad_row gate
    change assets/pins/sources.tsv
    scope "$BASE" "$HEAD" renovate/thing pull_request
    [ "$status" -eq 2 ]
    [[ "$stderr" == *"README.md"*"'gate'"* ]] || false
}

@test "only what the branch changed counts, not what moved on the base since it forked" {
    change README.md
    git -C "$R" checkout -q main
    echo moved >> "$R/assets/firmware/config.plist"
    git -C "$R" commit -qam "main moved"
    newbase=$(git -C "$R" rev-parse HEAD)
    scope "$newbase" "$HEAD" renovate/thing pull_request
    [ "$output" = "not-gated no gated path changed" ]
}
