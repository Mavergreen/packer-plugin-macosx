#!/usr/bin/env bats
# platform: host-agnostic
# shellcheck disable=SC2154  # $stderr is set by bats run --separate-stderr
#
# bin/gate-host-setup.sh readies one of GitHub's Linux runners for the boot gate: apt packages,
# /dev/kvm, ignore_msrs, the running kernel's image. It must never do any of that to a machine that
# is not such a runner. Here nothing reaches the host: PATH is the stub directory, /usr/bin and /bin
# (no sbin), and shutdown reboot halt poweroff systemctl sudo are stubs that refuse; ssh and scp
# record their argv and never run the remote command.

bats_require_minimum_version 1.5.0

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    S="$REPO/bin/gate-host-setup.sh"
    STUBS="$BATS_TEST_TMPDIR/stubs"
    export STUB_LOG="$BATS_TEST_TMPDIR/stub.log"
    mkdir -p "$STUBS"
    : > "$STUB_LOG"
    for c in shutdown reboot halt poweroff systemctl sudo; do
        cat > "$STUBS/$c" <<'STUB'
#!/bin/sh
printf '%s %s\n' "${0##*/}" "$*" >> "$STUB_LOG"
echo "stub: ${0##*/} refused"
exit 1
STUB
    done
    for c in ssh scp; do
        cat > "$STUBS/$c" <<'STUB'
#!/bin/sh
printf '%s %s\n' "${0##*/}" "$*" >> "$STUB_LOG"
exit 0
STUB
    done
    chmod +x "$STUBS"/*
    export PATH="$STUBS:/usr/bin:/bin"
    unset GITHUB_ACTIONS RUNNER_ENVIRONMENT GATE_DEBS_DIR
    export GITHUB_OUTPUT="$BATS_TEST_TMPDIR/github-output" GITHUB_ENV="$BATS_TEST_TMPDIR/github-env"
}

@test "safety: the dangerous names refuse, and the stub ssh never runs the remote command" {
    for c in shutdown reboot halt poweroff systemctl sudo; do
        run "$STUBS/$c" now
        [ "$status" -eq 1 ]
        [ "$output" = "stub: $c refused" ]
        [ "$(command -v "$c")" = "$STUBS/$c" ]
    done
    marker="$BATS_TEST_TMPDIR/ran"
    run "$STUBS/ssh" host "touch $marker"
    [ ! -e "$marker" ]
    run "$STUBS/scp" -o "ProxyCommand=touch $marker" a b
    [ ! -e "$marker" ]
    [[ "$PATH" != *sbin* ]] || false
}

@test "its header says it runs on GitHub's Linux runners only" {
    [ "$(sed -n 2p "$S")" = "# platform: host-agnostic -- GitHub's Linux runners only (apt, sudo)" ]
}

@test "--packages prints mavericks-vm's install-tools list and fuse3, one a line" {
    run "$S" --packages
    [ "$status" -eq 0 ]
    [ "$output" = "qemu-system-x86
qemu-utils
zstd
rsync
unzip
nasm
acpica-tools
uuid-dev
dmg2img
hfsprogs
busybox-static
fuse3" ]
}

@test "anything else on the command line is a usage error" {
    run "$S" --nonsense
    [ "$status" -eq 2 ]
    [ ! -s "$STUB_LOG" ]
}

@test "outside a GitHub-hosted runner it refuses, and runs no sudo" {
    run --separate-stderr "$S"
    [ "$status" -eq 2 ]
    [[ "$stderr" == *"GitHub-hosted"* ]] || false
    [ ! -s "$STUB_LOG" ]
    GITHUB_ACTIONS=true RUNNER_ENVIRONMENT=self-hosted run --separate-stderr "$S"
    [ "$status" -eq 2 ]
    [ ! -s "$STUB_LOG" ]
}

@test "on a runner, an apt step that fails stops it, naming the step, before /dev/kvm is touched" {
    GITHUB_ACTIONS=true RUNNER_ENVIRONMENT=github-hosted GATE_APT_TRIES=1 GATE_APT_TIMEOUT=10 \
        run "$S"
    [ "$status" -eq 1 ]
    [[ "$output" == *"::error title=gate-host-setup::apt-get update failed"* ]] || false
    grep -q '^sudo ' "$STUB_LOG"
    run grep -E 'chmod|tee' "$STUB_LOG"
    [ "$status" -eq 1 ]
}
