#!/usr/bin/env bats
#
# firstboot.sh's sudo_reads_includedir: whether a guest's sudo reads
# /etc/sudoers.d, which decides between writing a fragment there and
# appending the rule to /etc/sudoers itself. 10.6's sudo is 1.7.0 and does
# not (MEASURED 2026-10-04); #includedir arrived in 1.7.2; 10.9's is
# 1.7.4p6.

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    fn=$(awk '/^sudo_reads_includedir\(\) \{/,/^\}/' "$REPO/assets/guest/firstboot.sh")
    [ -n "$fn" ]
    eval "$fn"
}

@test "sudo 1.7.0, 10.6's, does not read sudoers.d" {
    run sudo_reads_includedir 1.7.0
    [ "$status" -ne 0 ]
}

@test "sudo 1.6.9p17 does not read sudoers.d" {
    run sudo_reads_includedir 1.6.9p17
    [ "$status" -ne 0 ]
}

@test "sudo 1.7.2 reads sudoers.d" {
    run sudo_reads_includedir 1.7.2
    [ "$status" -eq 0 ]
}

@test "sudo 1.7.4p6, 10.9's, reads sudoers.d" {
    run sudo_reads_includedir 1.7.4p6
    [ "$status" -eq 0 ]
}

@test "sudo 1.8.0 and 2.0 read sudoers.d" {
    run sudo_reads_includedir 1.8.0
    [ "$status" -eq 0 ]
    run sudo_reads_includedir 2.0
    [ "$status" -eq 0 ]
}

@test "an unreadable version is treated as one that does not" {
    run sudo_reads_includedir ""
    [ "$status" -ne 0 ]
    run sudo_reads_includedir "unknown"
    [ "$status" -ne 0 ]
}
