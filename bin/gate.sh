#!/usr/bin/env bash
# platform: host-agnostic -- tested on Linux with KVM only
# The boot gate, from a checkout: build a Mac OS X 10.9.5 base box and the Aqua Transport layer
# over it with the plugin built from this checkout, boot the layer, prove inside the guest that it
# runs this checkout's pins, check its health and reachability, restart it N times re-checking
# each time, and shut it down; then the same for 10.6.8, from the user's own disc. Nothing else
# reimplements it.
#
#   usage: bin/gate.sh [--release both|10.9|10.6] [--installer FILE] [--accel kvm|hvf|nvmm]
#                      [--work-dir DIR] [--evidence-dir DIR] [--restarts N] [--packer-cache DIR]
#                      [--ssh-port P]
#
#   --release       both (the default): 10.9, then 10.6, the whole gate each, in the same work
#                   dir one after the other, each release's evidence in <evidence>/<release>/ and
#                   a combined verdict in <evidence>/summary.md that passes only when both pass.
#                   The boot stack (OpenCore, Lilu, VirtualSMC, OVMF) is both releases', so a
#                   change to it is gated on both (the owner's ruling R52, 2026-10-28). 10.9 or
#                   10.6 gates that release alone, on purpose: 10.6 is never skipped in silence.
#   --installer     your own retail 10.6 install disc image, the snowleopard template's
#                   installer: required for both and for 10.6, refused for 10.9. Read in place,
#                   never copied into the evidence; never inside the work or evidence dir.
#   --accel         QEMU's accelerator (kvm). Only kvm is tested; hvf and nvmm say so.
#   --work-dir      ${MAVERICKS_BUILD_ROOT:-${TMPDIR:-/tmp}/mm-build}/gate. Needs 60 GiB free.
#                   Marked as the gate's own with a .guest-macosx-gate-work file; a directory that is
#                   not empty and not so marked is refused. An earlier run's builds, boxes,
#                   guest and plugins there are removed.
#   --evidence-dir  <work>/evidence, emptied first. A directory given here must be empty, and
#                   may not be the work dir, above it, or inside what the gate removes there.
#   --restarts      how many times the guest restarts (10)
#   --packer-cache  PACKER_CACHE_DIR, kept as it is; by default a fresh <work>/packer-cache.
#                   Never inside the evidence dir, nor the evidence dir inside it.
#                   A selected cache may not overlap paths the gate removes or writes.
#                   The default cache must not be a symlink.
#   --ssh-port      the host port forwarded to the guest's SSH (2223); it must be free
#
#   out:  <evidence>/steps.tsv (step, result, seconds, note; every step, always, in the order
#         preflight plugin build-base build-layer varstore boot versions health reach host-uuid
#         restart-1..N shutdown; a step the release has nothing for is n/a, its note saying why),
#         <evidence>/<step>.log, <evidence>/summary.md (bin/gate-summary.sh); on any
#         non-pass also screen.png, serial.log, guest/ (the guest's crash, hang, panic, spin and
#         diag reports, named system-<name> or user-<user>-<name>) and system.log (its last 500
#         lines). Never an image, box, disk or DMG.
#   exit: 0 pass, 1 a check ran and failed, 2 usage, 3 could not run the check (CI or the host);
#         for both releases, 0 when both passed, 1 when either failed, 3 otherwise
#
# THE RELEASES. The same steps and the same checks on both, but where 10.6 has nothing to check,
# each said here and in docs/decisions/0014 ("The boot gate"):
#   - no layer: 10.6 has no Aqua Transport layer yet (the parity design's G5), so build-layer is
#     n/a, the gate boots the base box, and versions holds the boot stack to its pins but has no
#     Aqua Transport pin to hold;
#   - no reach: without that layer 10.6's system curl has no TLS the three hosts accept, so reach,
#     which proves the layer's TLS, is n/a;
#   - its own release: health wants 10.6.8 (10K549), the updates=security build, not 10.9.5
#     (13F1911).
# What bin/gate-vm.sh does differently for a 10.6 box (ssh-rsa, a bare shutdown, one CPU) it reads
# from the box itself; boot fails if the box is not the release the gate built.
#   env:  GATE_BUILD_TIMEOUT (seconds, each build; by default 7200 the base, 2700 the layer),
#         GATE_FETCH_TIMEOUT (seconds, each of dev-install, a plugin install, the pin check;
#         1200). GATE_SSH_TIMEOUT, GATE_RESTART_TIMEOUT, GATE_SHUTDOWN_TIMEOUT, GATE_CMD_TIMEOUT
#         and GATE_KVM reach bin/gate-vm.sh; PACKER_GITHUB_API_TOKEN reaches `packer plugins
#         install` only, never the build.
#         Seams: GUEST_MACOSX_GATE_VM, GUEST_MACOSX_GATE_DEV_INSTALL, GUEST_MACOSX_GATE_VERIFY_SOURCES,
#         GUEST_MACOSX_GATE_VARSTORE (the scripts run in place of bin/gate-vm.sh, bin/dev-install.sh,
#         bin/verify-changed-sources.sh, bin/gate-varstore.sh),
#         GATE_REQUIRED_COMMANDS, GATE_SPACE_AVAILABLE_KIB (preflight, instead of df),
#         GATE_BUILD_SPACE_AVAILABLE_KIB (during and after a build, instead of df),
#         GATE_CHILD_STOP_WAIT (30: seconds a child gets between TERM and KILL),
#         GATE_KILL_AFTER (60: timeout -k, seconds between a limit's TERM and its KILL),
#         GUEST_MACOSX_GATE_TEST_ABORT_AFTER (a step after which gate.sh stops as a bug would),
#         GUEST_MACOSX_GATE_TEST_SIGNAL_AT_START (a release: a both-release run sends itself TERM
#         just after starting that release's run, before it knows the run's pid).
set -euo pipefail

GUEST_MACOSX_REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
export GUEST_MACOSX_REPO_ROOT
GUEST_MACOSX_LOG_PREFIX=gate
export GUEST_MACOSX_LOG_PREFIX
# shellcheck source=../lib/common.sh
. "$GUEST_MACOSX_REPO_ROOT/lib/common.sh"

VM=${GUEST_MACOSX_GATE_VM:-$GUEST_MACOSX_REPO_ROOT/bin/gate-vm.sh}
DEV_INSTALL=${GUEST_MACOSX_GATE_DEV_INSTALL:-$GUEST_MACOSX_REPO_ROOT/bin/dev-install.sh}
VERIFY_SOURCES=${GUEST_MACOSX_GATE_VERIFY_SOURCES:-$GUEST_MACOSX_REPO_ROOT/bin/verify-changed-sources.sh}
VARSTORE=${GUEST_MACOSX_GATE_VARSTORE:-$GUEST_MACOSX_REPO_ROOT/bin/gate-varstore.sh}
REQUIRED=${GATE_REQUIRED_COMMANDS:-packer qemu-system-x86_64 qemu-img ssh scp python3 go dmg2img mkfs.hfsplus nasm iasl timeout}
KVM=${GATE_KVM:-/dev/kvm}
# 60 GiB: mavericks-vm measured a cold base and layer at 44-49 GiB at the peak, and checks for 55;
# 5 more for the boot overlay, the evidence and the restarts.
NEED_GIB=60
# A build during which the work dir fell below this had the host's disk fill under it. Packer
# deletes its output directory when a build fails, so the room after the failure says little:
# the lowest free space seen while it ran is what counts.
LOW_GIB=2
# Wall-clock limits. mavericks-vm measured a cold base at about 38 min and the layer over it at
# about 14 min on GitHub's runners (docs/measured.md, run 38166708583), its 38 including the
# encrypt and push the gate does not do. About three times each: 38 x 3 = 114, so 120 min for
# the base (and the template's own install_timeout is 1h for the installer alone); 14 x 3 = 42,
# so 45 min for the layer. Fetches are unmeasured: a cold `go build` module download, a plugin
# of a few MB, a changed pin of tens of MB; 20 min each is generous.
BUILD_TIMEOUT_BASE=${GATE_BUILD_TIMEOUT:-7200}
BUILD_TIMEOUT_LAYER=${GATE_BUILD_TIMEOUT:-2700}
FETCH_TIMEOUT=${GATE_FETCH_TIMEOUT:-1200}
KILL_AFTER=${GATE_KILL_AFTER:-60}
STOP_WAIT=${GATE_CHILD_STOP_WAIT:-30}
# Pinned, not the newest ~> 1: classify_build_failure reads qemu v1.1.6's own "Starting VM" lines
# (v1.1.7's binary says the same); vagrant at the version this host's builds have used.
QEMU_PLUGIN=github.com/hashicorp/qemu
QEMU_PLUGIN_VERSION=v1.1.6
VAGRANT_PLUGIN=github.com/hashicorp/vagrant
VAGRANT_PLUGIN_VERSION=v1.1.7
# The guest keeps the checks in the vagrant user's home: 10.9 and 10.6 empty /private/tmp at boot.
GUEST_DIR=/Users/vagrant/gate
# packer-plugin-qemu v1.1.6, builder/qemu/step_run.go:84 and :87 say "Starting VM, booting from
# CD-ROM" (the base) or "... booting disk image" (the layer) as it starts QEMU. QEMU dying at once
# ("Error launching VM: ...", step_run.go:59) comes after that line, so it is a fail like any
# other: most likely the checkout's own template's QEMU arguments; the control run tells.
QEMU_STARTED='Starting VM, booting'
# The one list of report kinds: health judges these (and fails any other file it finds, as a kind
# it does not know), and the evidence keeps every report of them. 10.9 writes a .shutdownStall when
# userspace shutdown stalls (com.apple.shutdown_monitor: every 20 s exit timeout left one, no 3 s
# shutdown did; docs/decisions/0019).
REPORT_KINDS='crash hang panic spin diag shutdownStall'
SENTINEL=.guest-macosx-gate-work
# What the gate removes in the work dir before a run (packer-cache only when it is the default).
MANAGED='vm varstore base box-base layer box-layer plugins plugin-build packer-cache'

usage() {
    cat <<'EOF'
usage: gate.sh [--release both|10.9|10.6] [--installer FILE] [--accel kvm|hvf|nvmm]
               [--work-dir DIR] [--evidence-dir DIR] [--restarts N] [--packer-cache DIR]
               [--ssh-port P]
EOF
}
usage_error() {
    printf '%s: %s\n' "$GUEST_MACOSX_LOG_PREFIX" "$*" >&2
    usage >&2
    exit 2
}

# inside <path> <dir>: <path> is <dir> or below it (both physical, absolute).
inside() {
    case $1/ in "${2%/}/"*) return 0 ;; esac
    return 1
}

release=both
installer=
accel=kvm
work=
ev=
restarts=10
cache=
port=2223
while [ "$#" -gt 0 ]; do
    case $1 in
        -h|--help) usage; exit 0 ;;
        --release|--installer|--accel|--work-dir|--evidence-dir|--restarts|--packer-cache|--ssh-port)
            [ "$#" -ge 2 ] || usage_error "$1 needs a value"
            case $1 in
                --release) release=$2 ;;
                --installer) installer=$2 ;;
                --accel) accel=$2 ;;
                --work-dir) work=$2 ;;
                --evidence-dir) ev=$2 ;;
                --restarts) restarts=$2 ;;
                --packer-cache) cache=$2 ;;
                --ssh-port) port=$2 ;;
            esac
            shift 2 ;;
        *) usage_error "unknown argument: $1" ;;
    esac
done
[ "$release" = both ] || known_release "$release" || usage_error "--release wants both, $(releases_said) (got $release)"
if [ "$release" = both ] && [ -z "$installer" ]; then
    usage_error "a plain gate.sh gates both releases, 10.9 then 10.6, and 10.6 needs your own retail 10.6 install disc: pass --installer <disc image>, or gate one release on purpose with --release 10.9 or --release 10.6 --installer <disc image>"
fi
if [ "$release" != 10.9 ]; then
    [ -n "$installer" ] || usage_error "--release 10.6 needs --installer, your own retail 10.6 install disc image"
    if [ ! -f "$installer" ] || [ ! -r "$installer" ]; then
        usage_error "--installer $installer is not a readable file"
    fi
    # absolute, and every symlink followed: packer runs in a directory of its own, and the check
    # below that the disc is outside the work dir must see where it really is
    installer=$(real_path "$installer") || usage_error "--installer $installer: cannot follow its symlinks"
else
    [ -z "$installer" ] || usage_error "--installer is for 10.6: not for --release 10.9"
fi
case $accel in kvm|hvf|nvmm) ;; *) usage_error "--accel wants kvm, hvf or nvmm (got $accel)" ;; esac
case $restarts in ''|*[!0-9]*|0*) usage_error "--restarts wants a whole number of at least 1 (got $restarts)" ;; esac
case $port in ''|*[!0-9]*|0*) usage_error "--ssh-port wants a port number (got $port)" ;; esac
[ "$port" -le 65535 ] || usage_error "--ssh-port wants a port number (got $port)"
for t in "$BUILD_TIMEOUT_BASE" "$BUILD_TIMEOUT_LAYER" "$FETCH_TIMEOUT" "$STOP_WAIT" "$KILL_AFTER"; do
    case $t in ''|*[!0-9]*|0*) usage_error "a time limit wants a whole number of seconds (got $t)" ;; esac
done
[ -n "${work:-}" ] || work=${MAVERICKS_BUILD_ROOT:-${TMPDIR:-/tmp}/mm-build}/gate
ev_given=true
if [ -z "$ev" ]; then
    ev_given=false
fi
# The gate removes things in its work dir by name: never in a directory that is someone else's.
if [ -d "$work" ] && [ -n "$(ls -A "$work" 2>/dev/null)" ] && [ ! -f "$work/$SENTINEL" ]; then
    usage_error "--work-dir $work is not empty and has no $SENTINEL: not a gate work dir; give an empty or new one"
fi
if [ "$ev_given" = true ] && [ -d "$ev" ] && [ -n "$(ls -A "$ev" 2>/dev/null)" ]; then
    usage_error "--evidence-dir $ev is not empty; give an empty or new directory"
fi
case $accel in hvf|nvmm) warn "--accel $accel is untested; only kvm is" ;; esac

# Physical paths (pwd -P), so no symlink hides that two of them overlap, and find sees the
# evidence dir itself rather than a link to it.
if ! mkdir -p "$work" || ! work=$(cd "$work" && pwd -P); then
    warn "cannot create the work directory $work"
    exit 3
fi
# The user's disc is read in place, never where the gate removes things: checked before the first
# removal (the default evidence dir, just below). Inside the work dir covers that evidence dir; a
# disc inside a given --evidence-dir made it non-empty, refused above.
if [ -n "$installer" ] && inside "$installer" "$work"; then
    usage_error "--installer $installer is inside the work dir $work, which the gate removes things in; keep your disc elsewhere"
fi
cache_fresh=false
if [ -z "$cache" ]; then
    cache=$work/packer-cache
    cache_fresh=true
    [ ! -L "$cache" ] || usage_error "the default packer cache $cache is a symlink; give a separate --packer-cache to preserve it"
fi
if ! mkdir -p "$cache" || ! cache_named=$(cd -L "$cache" && pwd -L) || ! cache=$(cd "$cache" && pwd -P); then
    warn "cannot create the packer cache $cache"
    exit 3
fi
if [ "$cache_fresh" = true ]; then
    [ "$cache" = "$work/packer-cache" ] || usage_error "the default packer cache is outside its owned location"
else
    # Check both the names cleanup uses and their existing physical aliases. A cache
    # above a managed path is unsafe too: the gate would remove or write part of it.
    # packer-cache itself is preserved when explicitly selected, unlike these paths.
    for m in $MANAGED check.out space-low expected.txt "$SENTINEL" evidence; do
        [ "$m" != packer-cache ] || continue
        [ "$m" != evidence ] || [ "$ev_given" = false ] || continue
        managed=$work/$m
        if inside "$cache_named" "$managed" || inside "$managed" "$cache_named" ||
            inside "$cache" "$managed" || inside "$managed" "$cache"; then
            usage_error "the packer cache $cache and managed path $managed overlap"
        fi
        if [ -e "$managed" ] || [ -L "$managed" ]; then
            if [ -d "$managed" ]; then
                managed=$(cd "$managed" && pwd -P)
            else
                managed=$(real_path "$managed") || usage_error "cannot resolve managed path $managed"
            fi
            if inside "$cache" "$managed" || inside "$managed" "$cache"; then
                usage_error "the packer cache $cache and managed path $work/$m ($managed) overlap"
            fi
        fi
    done
fi
if [ "$ev_given" = false ]; then
    ev=$work/evidence
    rm -rf "$ev"
fi
if ! mkdir -p "$ev" || ! ev=$(cd "$ev" && pwd -P); then
    warn "cannot create the evidence directory $ev"
    exit 3
fi
# Evidence is published; images, boxes and the packer cache must never be under it.
if inside "$work" "$ev"; then
    usage_error "the evidence dir $ev holds the work dir $work: images would land in it"
fi
for m in $MANAGED; do
    if inside "$ev" "$work/$m"; then
        usage_error "the evidence dir $ev is inside $work/$m, which the gate removes and fills with images"
    fi
done
if inside "$cache" "$ev" || inside "$ev" "$cache"; then
    usage_error "the packer cache $cache and the evidence dir $ev overlap: Apple's media would land in the evidence"
fi
: > "$work/$SENTINEL"

# --- both releases: this script once per release, one after the other ---

# shellcheck disable=SC2317  # a trap
both_stop() {
    trap '' INT TERM
    both_interrupted=true
    both_trapped=true
    # A background child of a script has SIGINT ignored, and so cannot trap it: TERM, which its
    # on_signal handles as an interrupt. No child yet: run_both sends it once it has one.
    [ -z "$both_child" ] || kill -TERM "$both_child" 2>/dev/null || true
}

# both_line <release>: the first line of that release's summary.md, or why there is none
both_line() {
    local l
    l=$(head -n 1 "$ev/$1/summary.md" 2>/dev/null || true)
    if [ -n "$l" ]; then
        printf '%s' "$l"
    elif [ "$both_interrupted" = true ]; then
        printf 'did not run (interrupted)'
    else
        printf 'no summary (bin/gate.sh --release %s stopped without one)' "$1"
    fi
}

# both_wait: wait for both_child, and set rc to its own exit status. A trap cuts a wait short
# (status 128 + the signal), so after one the child is waited for again: bash keeps a reaped
# child's status for the next wait. A second wait that finds no such child (127: the trap came
# after the child's own status was already returned) keeps the status the first one gave.
both_wait() {
    local r2
    rc=0
    wait "$both_child" || rc=$?
    while [ "$both_trapped" = true ]; do
        both_trapped=false
        r2=0
        wait "$both_child" || r2=$?
        [ "$r2" -eq 127 ] || rc=$r2
    done
}

run_both() {
    local r rc v args verdict="" sep="" all=0 failed=false out=$ev/summary.md
    both_child=
    both_interrupted=false
    both_trapped=false
    trap both_stop INT TERM
    for r in $GUEST_MACOSX_RELEASES; do
        printf -v "both_rc_${r//./_}" '%s' none
        [ "$both_interrupted" = false ] || continue
        mkdir -p "$ev/$r"
        args=(--release "$r" --work-dir "$work" --evidence-dir "$ev/$r" --accel "$accel"
              --restarts "$restarts" --ssh-port "$port")
        [ "$cache_fresh" = true ] || args+=(--packer-cache "$cache")
        [ "$r" = 10.9 ] || args+=(--installer "$installer")
        log "gating Mac OS X $r (evidence: $ev/$r)"
        "$GUEST_MACOSX_REPO_ROOT/bin/gate.sh" "${args[@]}" &
        # the test seam: a signal in the window between the start and the line below
        [ "${GUEST_MACOSX_GATE_TEST_SIGNAL_AT_START:-}" != "$r" ] || kill -TERM "$$"
        both_child=$!
        # a signal between the start and the line above found no child to stop: stop it now. One
        # in the child's first moments, before its own traps, ends it at once, with no evidence:
        # its verdict then says it stopped without a summary.
        [ "$both_interrupted" = false ] || kill -TERM "$both_child" 2>/dev/null || true
        both_wait
        both_child=
        printf -v "both_rc_${r//./_}" '%s' "$rc"
    done
    trap '' INT TERM
    for r in $GUEST_MACOSX_RELEASES; do
        v=both_rc_${r//./_}
        case ${!v} in 0) ;; 1) all=1; failed=true ;; *) all=1 ;; esac
        verdict="$verdict$sep$r: $(both_line "$r")"
        sep="; "
    done
    if [ "$all" -eq 0 ]; then
        verdict="Passed: both releases -- $verdict"
        rc=0
    else
        verdict="Not passed: $verdict"
        if [ "$failed" = true ]; then rc=1; else rc=3; fi
    fi
    {
        printf '%s\n' "$verdict"
        for r in $GUEST_MACOSX_RELEASES; do
            v=both_rc_${r//./_}
            if [ "${!v}" = none ]; then
                printf '\n## %s (did not run)\n\n' "$r"
            else
                printf '\n## %s (exit %s)\n\n' "$r" "${!v}"
            fi
            if [ -f "$ev/$r/summary.md" ]; then cat "$ev/$r/summary.md"; else both_line "$r"; echo; fi
        done
    } > "$out"
    log "$verdict"
    log "evidence: $ev"
    return "$rc"
}

if [ "$release" = both ]; then
    rc=0
    run_both || rc=$?
    exit "$rc"
fi
state=$work/vm

export PACKER_PLUGIN_PATH="$work/plugins" PACKER_CACHE_DIR="$cache" CHECKPOINT_DISABLE=1
# GitHub's API, for `packer plugins install`, runs out of unauthenticated requests on a shared
# runner; the token goes to that and nothing else: never to the build of the checkout's tree, as
# mavericks-vm's scripts/build.sh does.
gh_token=${PACKER_GITHUB_API_TOKEN:-}
unset PACKER_GITHUB_API_TOKEN
export GATE_ACCEL="$accel" GATE_SSH_PORT="$port"

# Each release's base template, the box the guest boots, and the release health wants: 10.9's
# layer box, over the base; 10.6's base box (no layer yet). templates/*/verify.sh hold each build
# to the same.
case $release in
    10.9) BASE_TEMPLATE=mavericks; BOOT_BOX=$work/box-layer; WANT_OS=10.9.5; WANT_BUILD=13F1911 ;;
    10.6) BASE_TEMPLATE=snowleopard; BOOT_BOX=$work/box-base; WANT_OS=10.6.8; WANT_BUILD=10K549 ;;
esac

# not_applicable <step>: the reason this release has nothing for the step to check, on stdout; or
# status 1, it applies. 10.9 has none; 10.6 these two alone (gate-summary.sh refuses n/a on any
# other step).
not_applicable() {
    [ "$release" = 10.6 ] || return 1
    case $1 in
        build-layer) echo "10.6 has no Aqua Transport layer yet (G5): the gate boots the base box" ;;
        reach) echo "10.6 has no TLS layer yet (G5): its curl, on OpenSSL 0.9.8, fails all three hosts with a TLS protocol-version alert (MEASURED 2026-10-29)" ;;
        *) return 1 ;;
    esac
}

# host-uuid runs on the cold boot only: a restart keeps the NVRAM its boot wrote, so it could not
# tell a box that names no host UUID at boot from one that does (docs/decisions/0024).
# varstore boots the box's firmware alone on a nearly full variable store, before the guest's
# first boot: no restart here reaches the 61st that fills the store (docs/decisions/0032).
STEPS=(preflight plugin build-base build-layer varstore boot versions health reach host-uuid)
i=1
while [ "$i" -le "$restarts" ]; do
    STEPS+=("restart-$i")
    i=$((i + 1))
done
STEPS+=(shutdown)

tsv=$ev/steps.tsv
: > "$tsv"
# which release this run is, for bin/gate-summary.sh, which never compares two releases' runs
printf '%s\n' "$release" > "$ev/release.txt"
current=           # the step running now
started=$SECONDS
note=
child=             # the background command a step is waiting on
sampler=           # the background sampler of free space during a build
booted=false       # this run may have a QEMU on $state: the only one it ever kills
interrupted=false
finished=false
exp_opencore=''
exp_lilu=''
exp_virtualsmc=''
exp_aquatransport=''

# --- steps.tsv: the file is the state, so a trap never sees a half-updated counter ---

rows_written() { awk 'END { print NR }' "$tsv"; }
# outcome_now: the first result written so far that is neither pass nor n/a, or pass
outcome_now() { awk -F'\t' '$2 != "pass" && $2 != "n/a" { print $2; f = 1; exit } END { if (!f) print "pass" }' "$tsv"; }

write_row() { # <step> <result> <seconds> <note>
    local n
    n=$(printf '%s' "$4" | tr '\t\n\r' '   ')
    printf '%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$n" >> "$tsv"
}

# fill_rows <note>: the step that was running (or due) is infra with <note>; every later one skip.
# A step not applicable, due before any infra, is n/a as ever.
fill_rows() {
    local s n why
    n=$(rows_written)
    while [ "$n" -lt "${#STEPS[@]}" ]; do
        s=${STEPS[$n]}
        if [ "$(outcome_now)" = pass ] && [ "$s" != "$current" ] && why=$(not_applicable "$s"); then
            write_row "$s" n/a 0 "$why"
        elif [ "$(outcome_now)" = pass ]; then
            if [ "$s" = "$current" ]; then
                write_row "$s" infra $((SECONDS - started)) "$1"
            else
                write_row "$s" infra 0 "$1"
            fi
            log "$s: infra -- $1"
        else
            write_row "$s" skip 0 ""
        fi
        n=$((n + 1))
    done
    current=
}

# --- running things ---

# run_child <cmd...>: run a command so a signal reaches the gate at once, not when it finishes
# (bash runs a trap only after the foreground command returns: a packer build is an hour).
run_child() {
    local rc=0
    "$@" 8>&- 9>&- &
    child=$!
    wait "$child" || rc=$?
    child=
    return "$rc"
}

# timed_out <status> <started-at> <limit>: the command outlived its limit. timeout exits 124
# after its TERM worked, and 137 after its KILL (-k) -- but 137 is also anything else's KILL (the
# OOM killer, a person), so 137 counts only once the limit had passed.
timed_out() {
    [ "$1" -eq 124 ] && return 0
    [ "$1" -eq 137 ] && [ $((SECONDS - $2)) -ge "$3" ]
}

# killed_note <what> <status>: what to say for a KILL that was not a timeout's
killed_note() {
    printf '%s was killed (status %s: out of memory, or another process)' "$1" "$2"
}

is_qemu() { ps -p "$1" -o comm= 2>/dev/null | grep -q qemu; }

# shellcheck disable=SC2317  # reached from the traps
# stop_orphans: a packer killed outright leaves its QEMU running. Stop each QEMU process whose
# command line names this run's work dir (or, on Linux, whose working directory is in it), and
# no other process.
stop_orphans() {
    local pid args cwd i
    ps -eo pid=,args= 2>/dev/null | while read -r pid args; do
        case $pid in ''|*[!0-9]*) continue ;; esac
        if [ "$pid" -le 1 ] || [ "$pid" -eq "$$" ]; then continue; fi
        # the work dir as a whole path: at the start of an argument, or after = or , (a
        # QEMU of another run under /var$work/... must never match)
        case " $args" in
            *[\ =,]"$work/"*) ;;
            *)
                cwd=$(readlink "/proc/$pid/cwd" 2>/dev/null) || continue
                inside "$cwd" "$work" || continue ;;
        esac
        is_qemu "$pid" || continue
        # `env kill`: kill from PATH, so tests can stand a stub in front of it
        env kill -TERM "$pid" 2>/dev/null || true
        i=0
        while kill -0 "$pid" 2>/dev/null && [ "$i" -lt "$STOP_WAIT" ]; do
            sleep 1
            i=$((i + 1))
        done
        if kill -0 "$pid" 2>/dev/null && is_qemu "$pid"; then
            env kill -KILL "$pid" 2>/dev/null || true
        fi
        echo "stopped an orphaned QEMU (pid $pid): $args"
    done >> "$ev/evidence.log" 2>&1
}

# shellcheck disable=SC2317  # reached from the traps
# pgid_of <pid>
pgid_of() { ps -o pgid= -p "$1" 2>/dev/null | tr -d ' '; }

# shellcheck disable=SC2317  # reached from the traps
# signal_child <sig>: the child, and everything in its process group when it leads one of its own.
# GNU timeout makes itself a group leader and does not pass a KILL on, so KILLing timeout alone
# would orphan packer and its plugins. Never a group that is gate.sh's own.
signal_child() {
    local g own
    g=$(pgid_of "$child")
    own=$(pgid_of "$$")
    if [ -n "$g" ] && [ "$g" = "$child" ] && [ "$g" != "$own" ]; then
        kill -"$1" -- -"$g" 2>/dev/null || true
    else
        kill -"$1" "$child" 2>/dev/null || true
    fi
}

# shellcheck disable=SC2317  # reached from the traps
# stop_child: TERM the command a step waits on (packer stops its own QEMU on TERM), bounded;
# after a KILL, the QEMU it may have left.
stop_child() {
    if [ -n "$sampler" ]; then
        kill "$sampler" 2>/dev/null || true
        sampler=
    fi
    [ -n "$child" ] || return 0
    local i=0 killed=false
    signal_child TERM
    while kill -0 "$child" 2>/dev/null && [ "$i" -lt "$STOP_WAIT" ]; do
        sleep 1
        i=$((i + 1))
    done
    if kill -0 "$child" 2>/dev/null; then
        signal_child KILL
        killed=true
    fi
    wait "$child" 2>/dev/null || true
    child=
    [ "$killed" = false ] || stop_orphans
}

# vm_note <log>: gate-vm.sh's last warning in a step's log, which names what failed.
vm_note() {
    sed -n 's/^gate-vm: warning: //p' "$1" | tail -n 1
}

# --- the steps ---

port_answers() {
    # A connection refused (status 1) is a free port; one accepted, or one that hangs, is not.
    # shellcheck disable=SC2016  # $1 is the inner shell's
    timeout 5 bash -c 'exec 3<>"/dev/tcp/127.0.0.1/$1"' _ "$1" 2>/dev/null && return 0
    [ "$?" -ne 1 ]
}

step_preflight() {
    local p c found missing='' avail line rc=0 t0
    # An earlier run's QEMU still on this work dir: its boxes are under it; never touch them.
    if p=$(cat "$state/qemu.pid" 2>/dev/null); then
        case $p in ''|*[!0-9]*) ;; *)
            if [ "$p" -gt 1 ] && kill -0 "$p" 2>/dev/null && is_qemu "$p"; then
                note="QEMU from an earlier run (pid $p, $state/qemu.pid) is still running; stop it first: bin/gate-vm.sh kill $state"
                return 3
            fi ;;
        esac
    fi
    log "removing what an earlier run left in $work"
    if p=$(cat "$state/qmp.dir" 2>/dev/null); then
        ! is_gate_qmp_dir "$p" || rm -rf "$p"
    fi
    rm -rf "$state" "$work/varstore" "$work/base" "$work/box-base" "$work/layer" "$work/box-layer" \
        "$work/plugins" "$work/plugin-build" "$work/check.out" "$work/space-low"
    if [ "$cache_fresh" = true ]; then
        rm -rf "$cache"
        mkdir -p "$cache"
    fi

    for c in $REQUIRED; do
        if found=$(command -v "$c"); then
            echo "found $c: $found"
        else
            missing="$missing $c"
        fi
    done
    [ -z "$missing" ] || { note="missing commands:$missing"; return 3; }

    case $accel in
        kvm) [ -w "$KVM" ] || { note="$KVM is not writable: this host cannot run KVM"; return 3; } ;;
        hvf)
            if [ "$(uname -s)" != Darwin ] || [ "$(/usr/sbin/sysctl -n kern.hv_support 2>/dev/null)" != 1 ]; then
                note="hvf needs macOS with Hypervisor.framework (kern.hv_support = 1)"
                return 3
            fi ;;
        nvmm) [ -e /dev/nvmm ] || { note="nvmm needs /dev/nvmm (NetBSD with nvmm loaded)"; return 3; } ;;
    esac
    echo "accelerator: $accel"
    echo "release: $release"

    avail=${GATE_SPACE_AVAILABLE_KIB:-$(df -Pk "$work" | awk 'NR == 2 { print $4 }')}
    case $avail in ''|*[!0-9]*) note="cannot tell how much room $work has (df said '$avail')"; return 3 ;; esac
    if [ "$avail" -lt $((NEED_GIB * 1024 * 1024)) ]; then
        note="$work has $((avail / 1024 / 1024)) GiB free; the gate needs $NEED_GIB GiB"
        return 3
    fi
    echo "room: $((avail / 1024 / 1024)) GiB free in $work"

    if port_answers "$port"; then
        note="127.0.0.1:$port is already in use (a local guest, another gate run?); pick a free --ssh-port, the gate never talks to another guest"
        return 3
    fi
    echo "port: 127.0.0.1:$port is free"

    t0=$SECONDS
    run_child timeout -k "$KILL_AFTER" "$FETCH_TIMEOUT" "$VERIFY_SOURCES" || rc=$?
    if timed_out "$rc" "$t0" "$FETCH_TIMEOUT"; then
        note="bin/verify-changed-sources.sh did not finish within ${FETCH_TIMEOUT}s"
        return 3
    fi
    [ "$rc" -ne 137 ] || { note=$(killed_note bin/verify-changed-sources.sh 137); return 3; }
    [ "$rc" -eq 0 ] || { note="pins not filled: bin/verify-changed-sources.sh failed (preflight.log)"; return 3; }
    "$GUEST_MACOSX_REPO_ROOT/bin/gate-expected.sh" > "$work/expected.txt" \
        || { note="pins not readable: bin/gate-expected.sh failed (preflight.log)"; return 3; }
    while read -r c line; do
        case $c in
            opencore) exp_opencore=$line ;;
            lilu) exp_lilu=$line ;;
            virtualsmc) exp_virtualsmc=$line ;;
            aquatransport) exp_aquatransport=$line ;;
        esac
    done < "$work/expected.txt"
    rm -f "$work/expected.txt"
    if [ -z "$exp_opencore" ] || [ -z "$exp_lilu" ] || [ -z "$exp_virtualsmc" ] || [ -z "$exp_aquatransport" ]; then
        note="pins not readable: bin/gate-expected.sh did not name all four versions"
        return 3
    fi
    echo "pins: opencore $exp_opencore, lilu $exp_lilu, virtualsmc $exp_virtualsmc, aquatransport $exp_aquatransport"
    return 0
}

# shellcheck disable=SC2317  # run through run_child
# install_plugin <source> <version>: run in a subshell (run_child), so the token is exported there
# alone, and never on a command line where ps shows it.
install_plugin() {
    [ -z "$gh_token" ] || export PACKER_GITHUB_API_TOKEN="$gh_token"
    exec timeout -k "$KILL_AFTER" "$FETCH_TIMEOUT" packer plugins install "$1" "$2"
}

step_plugin() {
    local bin=$work/plugin-build/packer-plugin-macosx listed mac n rc=0 pair t0
    mkdir -p "$PACKER_PLUGIN_PATH" "${bin%/*}"
    t0=$SECONDS
    run_child timeout -k "$KILL_AFTER" "$FETCH_TIMEOUT" env GUEST_MACOSX_PLUGIN_BIN="$bin" "$DEV_INSTALL" || rc=$?
    if timed_out "$rc" "$t0" "$FETCH_TIMEOUT"; then
        note="bin/dev-install.sh did not finish within ${FETCH_TIMEOUT}s"
        return 3
    fi
    [ "$rc" -ne 137 ] || { note=$(killed_note bin/dev-install.sh 137); return 3; }
    [ "$rc" -eq 0 ] || { note="bin/dev-install.sh could not build and install the checkout's plugin"; return 3; }
    # by name and version, not `packer init`: init could fetch a released macosx plugin
    for pair in "$QEMU_PLUGIN $QEMU_PLUGIN_VERSION" "$VAGRANT_PLUGIN $VAGRANT_PLUGIN_VERSION"; do
        rc=0
        t0=$SECONDS
        # shellcheck disable=SC2086  # "<source> <version>"
        run_child install_plugin $pair || rc=$?
        if timed_out "$rc" "$t0" "$FETCH_TIMEOUT"; then
            note="packer plugins install ${pair% *} did not finish within ${FETCH_TIMEOUT}s"
            return 3
        fi
        [ "$rc" -ne 137 ] || { note=$(killed_note "packer plugins install ${pair% *}" 137); return 3; }
        [ "$rc" -eq 0 ] || { note="packer could not install ${pair% *} ${pair#* }"; return 3; }
    done
    listed=$(packer plugins installed) || { note="packer plugins installed failed"; return 3; }
    printf '%s\n' "$listed"
    # by file name: the plugin path itself may well contain "macosx"
    mac=$(printf '%s\n' "$listed" | awk -F/ '$NF ~ /^packer-plugin-macosx/' || true)
    n=$(printf '%s' "$mac" | grep -c . || true)
    if [ "$n" -ne 1 ]; then
        note="the plugin under test is not the checkout's: packer lists $n macosx plugins"
        return 3
    fi
    if ! cmp -s "$mac" "$bin"; then
        note="the plugin under test is not the checkout's: $mac is not the binary just built"
        return 3
    fi
    echo "the plugin under test: $mac (the checkout's, byte for byte)"
}

# build_avail_kib: the work dir's free KiB now
build_avail_kib() {
    if [ -n "${GATE_BUILD_SPACE_AVAILABLE_KIB:-}" ]; then
        printf '%s\n' "$GATE_BUILD_SPACE_AVAILABLE_KIB"
    else
        df -Pk "$work" 2>/dev/null | awk 'NR == 2 { print $4 }'
    fi
}

# note_low <kib> <file>: keep the lowest value in <file>
note_low() {
    local cur
    case $1 in ''|*[!0-9]*) return 0 ;; esac
    cur=$(cat "$2" 2>/dev/null || true)
    if [ -z "$cur" ] || [ "$1" -lt "$cur" ]; then
        printf '%s\n' "$1" > "$2"
    fi
}

# sample_space <pid> <file>: the lowest free space in the work dir while <pid> runs
sample_space() {
    while kill -0 "$1" 2>/dev/null; do
        note_low "$(build_avail_kib)" "$2"
        sleep 5
    done
}

# classify_build_failure <log> <lowest-free-kib> <status> <started-at> <limit>: sets note; returns
# 3 when the build could not run its check (the host's disk filled; packer killed from outside;
# QEMU never started: a download, a tool, the host), 1 when it ran and failed (the guest's own
# disk filling among them).
classify_build_failure() {
    local log=$1 low=$2 rc=$3 t0=$4 limit=$5 why line
    why=$(grep -E "errored( after [^:]*)?: " "$log" | tail -n 1 | sed 's/^.*errored[^:]*: //')
    if timed_out "$rc" "$t0" "$limit"; then
        why="did not finish within ${limit}s"
    elif [ "$rc" -eq 137 ]; then
        # the OOM killer or a person, not the checkout's tree
        note=$(killed_note packer 137)
        return 3
    fi
    case $low in ''|*[!0-9]*) low= ;; esac
    if [ -n "$low" ] && [ "$low" -lt $((LOW_GIB * 1024 * 1024)) ]; then
        note="the host's disk had $((low / 1024 / 1024)) GiB free at the lowest during the build"
        if grep -qiF 'no space left on device' "$log"; then
            note="no space left on device: $note"
        fi
        note="$note${why:+; $why}"
        return 3
    fi
    # packer's own lines (==>, -->, Build ... errored) are the host's; the guest's output comes
    # indented ("    qemu.<source>: ..."). ENOSPC on a line of packer's is the host's disk.
    if grep -iF 'no space left on device' "$log" | grep -qv '^    [^ ]'; then
        note="no space left on device${why:+: $why}"
        return 3
    fi
    line=$(grep -iF 'no space left on device' "$log" | head -n 1 | sed 's/^ *//')
    if [ -n "$line" ]; then
        note="the guest's disk filled: $line${why:+ ($why)}"
        return 1
    fi
    if ! grep -qF "$QEMU_STARTED" "$log"; then
        note="packer failed before QEMU started${why:+: $why}"
        return 3
    fi
    note="packer failed after QEMU started${why:+: $why}"
    return 1
}

# step_build <base|layer>
step_build() {
    local which=$1 dir=$work/$1 out=$work/box-$1 log=$ev/build-$1.log box f rc=0 limit low packer_rc t0
    local vars=(-var "accelerator=$accel" -var headless=true)
    local tpl=$GUEST_MACOSX_REPO_ROOT/templates/$BASE_TEMPLATE
    if [ "$which" = base ] && [ "$release" = 10.6 ]; then
        # 10.6.8 with Security Update 2013-004, from the user's own disc (docs/decisions/0014)
        vars=(-var updates=security -var "installer=$installer" "${vars[@]}")
        limit=$BUILD_TIMEOUT_BASE
    elif [ "$which" = base ]; then
        vars=(-var updates=security "${vars[@]}")
        limit=$BUILD_TIMEOUT_BASE
    else
        # the layer template has no `updates`: a -var it does not declare is an error
        tpl=$GUEST_MACOSX_REPO_ROOT/templates/mavericks-aquatransport
        vars+=(-var "base_box=$work/box-base")
        limit=$BUILD_TIMEOUT_LAYER
    fi
    rm -rf "$dir" "$out" "$work/space-low"
    mkdir -p "$dir" "$cache"
    t0=$SECONDS
    ( cd "$dir" && exec timeout -k "$KILL_AFTER" "$limit" packer build -color=false "${vars[@]}" "$tpl" ) 8>&- 9>&- &
    child=$!
    sample_space "$child" "$work/space-low" 8>&- 9>&- &
    sampler=$!
    wait "$child" || rc=$?
    child=
    kill "$sampler" 2>/dev/null || true
    wait "$sampler" 2>/dev/null || true
    sampler=
    note_low "$(build_avail_kib)" "$work/space-low"
    low=$(cat "$work/space-low" 2>/dev/null || true)
    rm -f "$work/space-low"
    # killed outright by timeout -k: packer could not stop its QEMU
    [ "$rc" -ne 137 ] || stop_orphans
    if [ "$rc" -ne 0 ]; then
        packer_rc=$rc
        rc=0
        classify_build_failure "$log" "$low" "$packer_rc" "$t0" "$limit" || rc=$?
        return "$rc"
    fi
    box=
    for f in "$dir"/output/*.box; do
        [ -f "$f" ] || continue
        [ -z "$box" ] || { note="packer left more than one .box in $dir/output"; return 1; }
        box=$f
    done
    [ -n "$box" ] || { note="packer finished but left no .box in $dir/output"; return 1; }
    mkdir -p "$out"
    run_child tar -xzf "$box" -C "$out" || { note="could not unpack ${box##*/} into $out"; return 3; }
    for f in box_0.img OVMF_CODE.fd OVMF_VARS.fd opencore.img Vagrantfile; do
        [ -f "$out/$f" ] || { note="the box ${box##*/} has no $f"; return 1; }
    done
    # packer's output (the box, and the image it was made from) goes once the box is unpacked
    rm -rf "$dir"
    echo "unpacked ${box##*/} into $out"
}

# step_varstore: bin/gate-varstore.sh on the firmware of the box the guest boots; no guest, no Apple
# bytes. Its state goes once it is done: the filled stores are firmware, not evidence.
step_varstore() {
    local rc=0
    rm -rf "$work/varstore"
    run_child "$VARSTORE" "$BOOT_BOX" "$work/varstore" || rc=$?
    rm -rf "$work/varstore"
    case $rc in
        0) return 0 ;;
        1) note=$(sed -n 's/^gate-varstore: warning: //p' "$ev/varstore.log" | tail -n 1)
           note=${note:-the variable reclaim of the firmware is slow again}
           return 1 ;;
        *) note=$(sed -n 's/^gate-varstore: warning: //p' "$ev/varstore.log" | tail -n 1)
           note="bin/gate-varstore.sh could not run (status $rc)${note:+: $note}"
           return 3 ;;
    esac
}

step_boot() {
    local log=$ev/boot.log rc=0 f got
    booted=true
    run_child "$VM" boot "$state" "$BOOT_BOX" || rc=$?
    case $rc in
        0) ;;
        1) note=$(vm_note "$log"); note=${note:-the guest did not boot}; return 1 ;;
        3)
            # Refused: QEMU may be another run's, so this run never signals it.
            booted=false
            note=$(vm_note "$log"); note=${note:-gate-vm.sh could not boot the guest}; return 3 ;;
        *) note="bin/gate-vm.sh boot exited $rc"; return 3 ;;
    esac
    # what gate-vm.sh read from the box's Vagrantfile, and so logged in and boots as
    got=$(cat "$state/release" 2>/dev/null || true)
    if [ "$got" != "$release" ]; then
        note="the box $BOOT_BOX is Mac OS X ${got:-(no release)} by its Vagrantfile; the gate built $release"
        return 1
    fi
    rc=0
    run_child "$VM" ssh "$state" mkdir -p "$GUEST_DIR" || rc=$?
    for f in guest-checks.sh crash-allowlist.tsv; do
        [ "$rc" -ne 0 ] || run_child "$VM" put "$state" "$GUEST_MACOSX_REPO_ROOT/gate/$f" "$GUEST_DIR/$f" || rc=$?
    done
    if [ "$rc" -ne 0 ]; then
        # the guest answered SSH a moment ago: losing it now is the guest's doing
        note="the guest booted, but the checks (gate/guest-checks.sh) could not be copied to $GUEST_DIR (status $rc)"
        return 1
    fi
}

# run_check <check> [args...]: guest-checks.sh <check> in the guest; its output goes to the
# step's log; sets check_note; returns 0 pass, 1 fail, 3 infra.
#
# The statuses bin/gate-vm.sh ssh passes through: 1 is a check that ran and failed. 255 (ssh
# itself failed) and 124 (the command outlived GATE_CMD_TIMEOUT) are fails too: the guest
# answered SSH at boot or after its restart, so losing it mid-check is the guest's doing (a
# panic, a hang), not the host's; the control run is what tells main's problems from the
# candidate's. 2 is a usage error, from guest-checks.sh or gate-vm.sh: the gate is broken, infra.
run_check() {
    local name=$1 out=$work/check.out rc=0 first
    shift
    echo "== guest-checks.sh $name"
    run_child "$VM" ssh "$state" sh "$GUEST_DIR/guest-checks.sh" "$name" "$@" > "$out" 2>&1 || rc=$?
    cat "$out"
    first=$(sed -n 's/^FAIL //p' "$out" | head -n 1)
    rm -f "$out"
    check_note=
    case $rc in
        0) return 0 ;;
        1) check_note=${first:-guest-checks.sh $name failed without saying why} ;;
        2) check_note="guest-checks.sh $name: a usage error, so the gate itself is broken"; return 3 ;;
        124) check_note="$name did not finish within ${GATE_CMD_TIMEOUT:-300}s" ;;
        255) check_note="SSH to the guest failed during $name" ;;
        *) check_note="guest-checks.sh $name exited $rc${first:+: $first}" ;;
    esac
    return 1
}

check_args() { # <check>: the arguments guest-checks.sh takes for it
    # shellcheck disable=SC2206  # REPORT_KINDS is words
    case $1 in
        versions)
            CHECK_ARGS=("$exp_opencore" "$exp_lilu" "$exp_virtualsmc")
            # no layer on 10.6 yet, so no Aqua Transport to hold to its pin
            [ "$release" = 10.6 ] || CHECK_ARGS+=("$exp_aquatransport") ;;
        health) CHECK_ARGS=("$GUEST_DIR/crash-allowlist.tsv" "$WANT_OS" "$WANT_BUILD" $REPORT_KINDS) ;;
        reach|host-uuid) CHECK_ARGS=() ;;
    esac
}

step_check() {
    local rc=0
    check_args "$1"
    run_check "$1" ${CHECK_ARGS[@]+"${CHECK_ARGS[@]}"} || rc=$?
    note=$check_note
    return "$rc"
}

step_restart() {
    local k=$1 log=$ev/restart-$1.log rc=0 c r worst=0 failed='' first=''
    run_child "$VM" restart "$state" || rc=$?
    case $rc in
        0) ;;
        1) c=$(vm_note "$log"); note="restart $k of $restarts: ${c:-the guest did not come back}"; return 1 ;;
        *) note="restart $k of $restarts: bin/gate-vm.sh restart exited $rc"
           c=$(vm_note "$log")
           [ -z "$c" ] || note="$note: $c"
           return 3 ;;
    esac
    for c in versions health reach; do
        if not_applicable "$c" > /dev/null; then continue; fi
        r=0
        check_args "$c"
        run_check "$c" ${CHECK_ARGS[@]+"${CHECK_ARGS[@]}"} || r=$?
        [ "$r" -ne 0 ] || continue
        failed="$failed${failed:+, }$c"
        [ -n "$first" ] || first="$c: $check_note"
        if [ "$r" -eq 1 ]; then worst=1; elif [ "$worst" -eq 0 ]; then worst=3; fi
    done
    [ -n "$failed" ] || return 0
    case $failed in
        *,*) note="restart $k of $restarts: $failed failed -- $first" ;;
        # one check failed: name it once, then its first FAIL line
        *) note="restart $k of $restarts: $failed failed -- ${first#*: }" ;;
    esac
    return "$worst"
}

step_shutdown() {
    local rc=0
    run_child "$VM" shutdown "$state" || rc=$?
    case $rc in
        0) return 0 ;;
        1) note=$(vm_note "$ev/shutdown.log"); note=${note:-the guest did not power off}; return 1 ;;
        *) note="bin/gate-vm.sh shutdown exited $rc"; return 3 ;;
    esac
}

dispatch() {
    case $1 in
        preflight) step_preflight ;;
        plugin) step_plugin ;;
        build-base) step_build base ;;
        build-layer) step_build layer ;;
        varstore) step_varstore ;;
        boot) step_boot ;;
        versions|health|reach|host-uuid) step_check "$1" ;;
        restart-*) step_restart "${1#restart-}" ;;
        shutdown) step_shutdown ;;
    esac
}

# --- evidence, the end ---

# gsh_evidence <cmd...>: the guest, for evidence only, briefly.
gsh_evidence() {
    GATE_CMD_TIMEOUT=60 "$VM" ssh "$state" "$@"
}

collect_guest_evidence() {
    local list path name kind dest user n=0
    gsh_evidence true || { echo "the guest does not answer SSH: no reports or system.log from it"; return 0; }
    # shellcheck disable=SC2016  # expanded by the guest's shell
    list=$(gsh_evidence 'for f in /Library/Logs/DiagnosticReports/* /Users/*/Library/Logs/DiagnosticReports/*; do [ -f "$f" ] && echo "$f"; done; true') || list=
    while IFS= read -r path; do
        [ -n "$path" ] || continue
        name=${path##*/}
        kind=${name##*.}
        case " $REPORT_KINDS " in *" $kind "*) ;; *) continue ;; esac
        # named by where it came from: the same name under /Library and a user's never collide
        case $path in
            /Library/Logs/DiagnosticReports/"$name") dest=system-$name ;;
            /Users/*/Library/Logs/DiagnosticReports/"$name")
                user=${path#/Users/}
                user=${user%%/*}
                dest=user-$user-$name ;;
            *) echo "not fetching a report from an unexpected place: $path"; continue ;;
        esac
        # any name: a stall's lists the stalled processes with commas, a process may have spaces.
        # Here it is a tame file name; to the guest's shell, one single-quoted word.
        dest=$(printf '%s' "$dest" | tr -c 'A-Za-z0-9._,-' '_')
        n=$((n + 1))
        [ "$n" -le 50 ] || { echo "more than 50 reports; the rest stay in the guest"; break; }
        mkdir -p "$ev/guest"
        gsh_evidence "cat '$(printf '%s' "$path" | sed "s/'/'\\\\''/g")'" > "$ev/guest/$dest" || rm -f "$ev/guest/$dest"
    done <<EOF
$list
EOF
    # launchd's own crash sample, which health fails on, outside DiagnosticReports
    if gsh_evidence test -f /var/log/launchd-pid1.crash; then
        mkdir -p "$ev/guest"
        gsh_evidence cat /var/log/launchd-pid1.crash > "$ev/guest/system-launchd-pid1.crash" \
            || rm -f "$ev/guest/system-launchd-pid1.crash"
    fi
    gsh_evidence tail -n 500 /var/log/system.log > "$ev/system.log" || rm -f "$ev/system.log"
}

# collect_evidence: on a non-pass, what a person needs to see what the guest did.
collect_evidence() {
    if [ "$booted" = true ] && [ "$interrupted" = false ]; then
        "$VM" screenshot "$state" "$ev/screen.png" || true
        collect_guest_evidence || true
    fi
    if [ "$booted" != false ] && [ -f "$state/serial.log" ]; then
        tail -n 20000 "$state/serial.log" > "$ev/serial.log" || true
    fi
}

# prune_evidence: nothing outside the allowlist ever leaves in the evidence. No image, box, disk,
# DMG or ISO: only *.tsv *.md *.txt *.log *.png, and the guest's text reports under guest/, of the
# kinds in REPORT_KINDS.
# -H: should the evidence dir itself be reached through a link, find still descends into it.
prune_evidence() {
    local k kinds=()
    for k in $REPORT_KINDS; do
        kinds+=(${kinds[0]+-o} -name "*.$k")
    done
    find -H "$ev" -mindepth 1 ! -type d ! -type f -exec rm -f {} + 2>/dev/null || true
    find -H "$ev" -type f ! \( -name '*.tsv' -o -name '*.md' -o -name '*.txt' -o -name '*.log' \
        -o -name '*.png' -o \( -path "$ev/guest/*" \( "${kinds[@]}" \) \) \) -exec rm -f {} + 2>/dev/null || true
}

# finish <note>: complete steps.tsv, keep the evidence, stop this run's QEMU, write the summary.
finish() {
    local outcome
    set +e
    trap '' INT TERM
    # a trap can land while a step's output goes to its log: talk to the terminal again
    exec 1>&8 2>&9
    finished=true
    fill_rows "$1"
    outcome=$(outcome_now)
    {
        # interrupted: stop at once, and keep what needs no guest; otherwise the guest's
        # screen and reports first, while it still runs
        if [ "$interrupted" = true ] && [ "$booted" = true ]; then
            "$VM" kill "$state"
            booted=stopped
        fi
        if [ "$outcome" != pass ]; then
            collect_evidence
        fi
        if [ "$booted" = true ]; then
            "$VM" kill "$state"
        fi
    } >> "$ev/evidence.log" 2>&1
    rm -f "$work/check.out" "$work/space-low"
    prune_evidence
    "$GUEST_MACOSX_REPO_ROOT/bin/gate-summary.sh" "$ev" > "$ev/summary.md" 2>> "$ev/evidence.log"
    [ "$?" -ne 2 ] || warn "bin/gate-summary.sh refused $tsv (a bug in the gate)"
    log "$(head -n 1 "$ev/summary.md" 2>/dev/null)"
    log "evidence: $ev"
    case $outcome in
        pass) return 0 ;;
        fail) return 1 ;;
        *) return 3 ;;
    esac
}

# shellcheck disable=SC2317  # a trap
on_signal() {
    trap '' INT TERM
    interrupted=true
    # every row first: a cancelled CI job is killed outright a few seconds after its signal
    fill_rows interrupted
    stop_child
    exit 3
}

# shellcheck disable=SC2317  # a trap
on_exit() {
    local st=$1 rc=0
    trap '' INT TERM
    [ "$finished" = false ] || exit "$st"
    if [ "$interrupted" = true ]; then
        finish interrupted || rc=$?
    else
        stop_child
        finish "gate.sh stopped unexpectedly (status $st)" || rc=$?
    fi
    exit "$rc"
}

exec 8>&1 9>&2
trap 'on_signal' INT TERM
trap 'on_exit $?' EXIT

for s in "${STEPS[@]}"; do
    if [ "$(outcome_now)" != pass ]; then
        write_row "$s" skip 0 ""
        continue
    fi
    if why=$(not_applicable "$s"); then
        write_row "$s" n/a 0 "$why"
        log "$s: n/a -- $why"
        continue
    fi
    current=$s
    started=$SECONDS
    note=
    log "$s: running (log: $ev/$s.log)"
    rc=0
    dispatch "$s" >> "$ev/$s.log" 2>&1 || rc=$?
    case $rc in
        0) result=pass ;;
        1) result=fail ;;
        *) result=infra; note=${note:-the step stopped with status $rc} ;;
    esac
    write_row "$s" "$result" $((SECONDS - started)) "$note"
    current=
    log "$s: $result${note:+ -- $note} ($((SECONDS - started))s)"
    if [ "${GUEST_MACOSX_GATE_TEST_ABORT_AFTER:-}" = "$s" ]; then
        false
    fi
done

rc=0
finish "" || rc=$?
exit "$rc"
