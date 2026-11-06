#!/usr/bin/env bats
# platform: host-agnostic
#
# bin/shipyard-check.sh, run by bin/run-tests.sh before bats: the suites
# that load lib/vendor.sh (it fetches with shipyard's shipyard_curl) fail
# on a host without shipyard, so the run says so first, naming them,
# instead of leaving a developer to read twenty failures for the cause.

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
}

@test "without shipyard it names the suites that need it and how to get it, and exits 0" {
    run env -u SHIPYARD_SCRIPTS PATH=/usr/bin:/bin "$REPO/bin/shipyard-check.sh"
    [ "$status" -eq 0 ]
    [[ "$output" == *"shipyard not found"* ]] || false
    [[ "$output" == *"tests/vendor.bats"* ]] || false
    [[ "$output" == *"tests/verify_changed_sources.bats"* ]] || false
    [[ "$output" == *"SHIPYARD_SCRIPTS"* ]] || false
    # only the suites that load it, not this one, whose comments name it
    [[ "$output" != *"tests/shipyard_check.bats"* ]] || false
}

@test "with shipyard it prints where, and nothing about missing suites" {
    fake="$BATS_TEST_TMPDIR/shipyard/scripts"
    mkdir -p "$fake"
    run env SHIPYARD_SCRIPTS="$fake" PATH=/usr/bin:/bin "$REPO/bin/shipyard-check.sh"
    [ "$status" -eq 0 ]
    [ "$output" = "shipyard: $fake" ]
}

@test "bin/run-tests.sh runs it before bats" {
    check=$(grep -n 'bin/shipyard-check.sh' "$REPO/bin/run-tests.sh" | head -n 1 | cut -d: -f1)
    bats_line=$(grep -n '^echo "== bats =="' "$REPO/bin/run-tests.sh" | cut -d: -f1)
    [ -n "$check" ]
    [ -n "$bats_line" ]
    [ "$check" -lt "$bats_line" ]
}
