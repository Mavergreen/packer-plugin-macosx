#!/usr/bin/env bats
# platform: host-agnostic
# shellcheck disable=SC2016  # the single-quoted text is Python, and ${{ }} in it is GitHub's
#
# bin/go-macos-scope.sh: whether ci.yml's go-macos job, the one on a scarce
# macOS runner, runs on a change. It runs unless every changed file is one no
# Go test reads (docs, release notes, renovate.json); when it does not, the
# job is skipped, and a skipped job passes the required check of its name.

bats_require_minimum_version 1.5.0

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
}

# A throwaway repository: the script, the lib it sources, and one commit of
# code and docs to change from.
make_repo() {
    DIR="$BATS_TEST_TMPDIR/planted"
    mkdir -p "$DIR/bin" "$DIR/lib" "$DIR/docs/decisions" "$DIR/release-notes" \
        "$DIR/.github" "$DIR/internal/lock"
    cp "$REPO/bin/go-macos-scope.sh" "$DIR/bin/"
    cp "$REPO/lib/common.sh" "$DIR/lib/"
    printf 'package lock\n' > "$DIR/internal/lock/lock.go"
    printf '# r\n' > "$DIR/README.md"
    printf '# d\n' > "$DIR/docs/decisions/0001-x.md"
    printf '# n\n' > "$DIR/release-notes/v1.md"
    printf '{}\n' > "$DIR/.github/renovate.json"
    printf 'module m\n' > "$DIR/go.mod"
    git -C "$DIR" init -q
    git -C "$DIR" config user.email t@example.invalid
    git -C "$DIR" config user.name t
    git -C "$DIR" add -A
    git -C "$DIR" -c commit.gpgsign=false commit -qm base
    BASE=$(git -C "$DIR" rev-parse HEAD)
}

# change FILE...: append to each and commit; HEAD is then the change.
change() {
    local f
    for f in "$@"; do printf 'x\n' >> "$DIR/$f"; done
    git -C "$DIR" add -A
    git -C "$DIR" -c commit.gpgsign=false commit -qm change
    HEAD=$(git -C "$DIR" rev-parse HEAD)
}

@test "docs, release notes and renovate.json alone: skip, naming why" {
    make_repo
    change README.md docs/decisions/0001-x.md release-notes/v1.md .github/renovate.json
    run --separate-stderr "$DIR/bin/go-macos-scope.sh" "$BASE" "$HEAD"
    [ "$status" -eq 0 ]
    [[ "$output" == "skip "* ]] || { echo "$output"; false; }
}

@test "a Go file among docs: run, naming it" {
    make_repo
    change README.md internal/lock/lock.go
    run --separate-stderr "$DIR/bin/go-macos-scope.sh" "$BASE" "$HEAD"
    [ "$status" -eq 0 ]
    [ "$output" = "run internal/lock/lock.go changed" ] || { echo "$output"; false; }
}

@test "any file that is not a doc runs it, not only Go files" {
    make_repo
    change go.mod
    run --separate-stderr "$DIR/bin/go-macos-scope.sh" "$BASE" "$HEAD"
    [[ "$output" == "run go.mod changed" ]] || { echo "$output"; false; }
}

@test "a new file that is not a doc runs it" {
    make_repo
    mkdir -p "$DIR/assets/guest"
    printf 'x\n' > "$DIR/assets/guest/new.sh"
    git -C "$DIR" add -A
    git -C "$DIR" -c commit.gpgsign=false commit -qm add
    run --separate-stderr "$DIR/bin/go-macos-scope.sh" "$BASE" "$(git -C "$DIR" rev-parse HEAD)"
    [[ "$output" == "run assets/guest/new.sh changed" ]] || { echo "$output"; false; }
}

@test "a base that is not a commit (a new branch's all-zero before): run" {
    make_repo
    change README.md
    run --separate-stderr "$DIR/bin/go-macos-scope.sh" 0000000000000000000000000000000000000000 "$HEAD"
    [ "$status" -eq 0 ]
    [[ "$output" == "run "* ]] || { echo "$output"; false; }
}

@test "an empty base: run" {
    make_repo
    change README.md
    run --separate-stderr "$DIR/bin/go-macos-scope.sh" "" "$HEAD"
    [[ "$output" == "run "* ]] || { echo "$output"; false; }
}

@test "nothing changed: run, since there is nothing to say it is safe to skip" {
    make_repo
    run --separate-stderr "$DIR/bin/go-macos-scope.sh" "$BASE" "$BASE"
    [[ "$output" == "run "* ]] || { echo "$output"; false; }
}

@test "a wrong argument count is a usage error" {
    run --separate-stderr "$REPO/bin/go-macos-scope.sh" one
    [ "$status" -eq 2 ]
}

# The workflow: a Linux job decides, and go-macos runs only on its say-so
# and only the packages whose behaviour is the host's.
py() {
    python3 -I -c '
import sys, yaml
with open(sys.argv[1] + "/.github/workflows/ci.yml") as f:
    ci = yaml.safe_load(f)
jobs = ci["jobs"]
exec(sys.argv[2])
' "$REPO" "$1"
}

@test "go-macos waits on a Linux scope job, and skips only on its successful skip" {
    py '
s = jobs["go-macos-scope"]
assert s["runs-on"] == "ubuntu-latest", s["runs-on"]
assert "bin/go-macos-scope.sh" in "\n".join(x.get("run", "") for x in s["steps"])
m = jobs["go-macos"]
assert m["needs"] == "go-macos-scope", m.get("needs")
want = "${{ !cancelled() && !(needs.go-macos-scope.result == '"'"'success'"'"' && needs.go-macos-scope.outputs.decision == '"'"'skip'"'"') }}"
assert m["if"] == want, m.get("if")
'
}

@test "go-macos tests only the packages that behave differently on a Mac" {
    py '
m = jobs["go-macos"]
runs = "\n".join(x.get("run", "") for x in m["steps"])
want = "go test -race ./internal/lock/... ./internal/proc/... ./datasource/qmpdir/... ./internal/payload/..."
assert want in runs, runs
'
}

@test "INGREDIENTS.md declares go-macos as a macos-runner deviation with its exit" {
    grep -q '^- macos-runner:ci.yml:go-macos: .*exit' "$REPO/INGREDIENTS.md"
}
