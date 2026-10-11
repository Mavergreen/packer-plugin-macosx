#!/usr/bin/env bats
#
# bin/base-recipe.sh names what the base image is built from. A release that changes only the
# layer template, the docs or the tests must publish the same recipe, so a consumer can leave
# its cached base image alone.

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    T="$BATS_TEST_TMPDIR/tree"
    mkdir -p "$T"
    (
        cd "$T" || exit 1
        git init -q . && git config user.email t@example.com && git config user.name t
        mkdir -p components/openssh cmd/x internal/fetch datasource/y assets/pins templates/mavericks \
            templates/mavericks-aquatransport docs
        echo mod > go.mod; echo sum > go.sum
        echo a > cmd/x/main.go; echo a > cmd/x/main_test.go
        echo a > internal/fetch/get.go; echo a > datasource/y/y.go
        mkdir -p internal/fetch/testdata; echo a > internal/fetch/testdata/f
        echo a > assets/pins/sources.tsv; echo 1 > components/openssh/version; echo a > embed.go
        echo a > templates/mavericks/verify.sh
        echo a > templates/mavericks-aquatransport/layer.sh
        echo a > docs/d.md; echo a > README.md
        git add -A && git commit -q -m base
    ) || return 1
    cp "$REPO/bin/base-recipe.sh" "$BATS_TEST_TMPDIR/base-recipe.sh"
}

recipe() { (cd "$T" && sh "$BATS_TEST_TMPDIR/base-recipe.sh" "$@"); }

change() { # change FILE: edit it and commit
    (cd "$T" && echo "$RANDOM" >> "$1" && git add -A && git commit -q -m "change $1")
}

@test "the recipe is 64 hex digits" {
    run recipe
    [ "$status" -eq 0 ]
    [[ "$output" =~ ^[0-9a-f]{64}$ ]]
}

@test "REF defaults to HEAD and can name another commit" {
    first="$(recipe)"
    change internal/fetch/get.go
    [ "$(recipe HEAD~1)" = "$first" ]
    [ "$(recipe HEAD)" != "$first" ]
}

@test "a commit that changes only templates/mavericks-aquatransport/ keeps the recipe" {
    before="$(recipe)"; change templates/mavericks-aquatransport/layer.sh
    [ "$(recipe)" = "$before" ]
}

@test "a commit that changes only docs/ or README.md keeps it" {
    before="$(recipe)"; change docs/d.md; change README.md
    [ "$(recipe)" = "$before" ]
}

@test "a commit that changes only a _test.go or testdata file keeps it" {
    before="$(recipe)"; change cmd/x/main_test.go; change internal/fetch/testdata/f
    [ "$(recipe)" = "$before" ]
}

@test "a commit that changes templates/mavericks/verify.sh changes it" {
    before="$(recipe)"; change templates/mavericks/verify.sh
    [ "$(recipe)" != "$before" ]
}

@test "a commit that changes internal/fetch/get.go changes it" {
    before="$(recipe)"; change internal/fetch/get.go
    [ "$(recipe)" != "$before" ]
}

@test "a commit that changes assets/pins/sources.tsv changes it" {
    before="$(recipe)"; change assets/pins/sources.tsv
    [ "$(recipe)" != "$before" ]
}

@test "go.mod, go.sum, cmd and datasource each change it" {
    for f in go.mod go.sum cmd/x/main.go datasource/y/y.go; do
        before="$(recipe)"; change "$f"
        [ "$(recipe)" != "$before" ] || { echo "$f"; false; }
    done
}

@test "an unknown ref is a failure" {
    run recipe nosuchref
    [ "$status" -ne 0 ]
}

@test "the script is POSIX sh and carries the platform header" {
    head -3 "$REPO/bin/base-recipe.sh" | grep -q '^#!/bin/sh'
    grep -q '^# platform: host-agnostic' "$REPO/bin/base-recipe.sh"
}

@test "a commit that changes components/openssh/version changes it" {
    before="$(recipe)"; change components/openssh/version
    [ "$(recipe)" != "$before" ]
}

@test "a commit that changes embed.go changes it" {
    before="$(recipe)"; change embed.go
    [ "$(recipe)" != "$before" ]
}
