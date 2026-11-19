#!/usr/bin/env bash
# platform: host-agnostic -- GitHub's Linux runners only (apt, sudo)
# Ready one of GitHub's hosted Linux runners for bin/gate.sh: what Mavergreen/mavericks-vm's
# scripts/install-tools.sh does there (89da498), but age, oras and packer, which the gate does not
# use or gate.yml sets up itself (hashicorp/setup-packer, the version ci.yml validates with).
#
#   usage: bin/gate-host-setup.sh | bin/gate-host-setup.sh --packages (prints the apt packages, one
#          a line, for the cache key of their .debs)
#   env:   GATE_DEBS_DIR (the .debs actions/cache keeps for this runner image; unset, apt fetches
#          every run); GATE_APT_TIMEOUT (180) and GATE_APT_TRIES (3), per apt step;
#          GATE_MODULES_DIR (/lib/modules)
#   out:   QEMU and the plugin's build tools from apt; /dev/kvm the runner user's; KVM's
#          ignore_msrs on; the running kernel's image readable; hfsplus for the running kernel when
#          it lacks it; debs-save=true in $GITHUB_OUTPUT when it fetched the .debs for
#          actions/cache to keep
#   exit:  0 ready; 1 a step failed, named with what it printed; 2 usage, or not on a GitHub-hosted
#          runner: it changes the host's /dev/kvm and KVM module, which no other machine wants
set -euo pipefail

GUEST_MACOSX_REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
export GUEST_MACOSX_REPO_ROOT
GUEST_MACOSX_LOG_PREFIX=gate-host-setup
export GUEST_MACOSX_LOG_PREFIX
# shellcheck source=../lib/common.sh
. "$GUEST_MACOSX_REPO_ROOT/lib/common.sh"

# mavericks-vm's list: nasm, iasl and uuid.h for OpenCore and OVMF, dmg2img and mkfs.hfsplus for the
# media, busybox for the plugin's microVM, zstd and rsync, which the gate's builds use as
# mavericks-vm's do.
pkgs=(qemu-system-x86 qemu-utils zstd rsync unzip nasm acpica-tools uuid-dev dmg2img hfsprogs busybox-static)

case $#:${1:-} in
    1:--packages) printf '%s\n' "${pkgs[@]}"; exit 0 ;;
    0:) ;;
    *) printf 'usage: %s [--packages]\n' "${0##*/}" >&2; exit 2 ;;
esac

if [ "${GITHUB_ACTIONS:-}" != true ] || [ "${RUNNER_ENVIRONMENT:-}" != github-hosted ]; then
    printf '%s: refusing: this readies a GitHub-hosted runner (GITHUB_ACTIONS=true, RUNNER_ENVIRONMENT=github-hosted), changing /dev/kvm and the kvm module; on any other machine install the tools yourself (README, "Check a change")\n' \
        "$GUEST_MACOSX_LOG_PREFIX" >&2
    exit 2
fi

# An apt step says what it is, runs under a limit, and on failure or timeout shows what it printed:
# mavericks-vm saw a stalled mirror hang a job for 79 minutes without a word, and an install print
# nothing for 600 s twice. So each has a shorter limit and more than one try, and dpkg finishes,
# between tries, whatever the killed attempt left half done.
apt_opts=(-o DPkg::Lock::Timeout=120 -o Acquire::Retries=3 -o Acquire::http::Timeout=30 -o Acquire::https::Timeout=30)
apt_limit=${GATE_APT_TIMEOUT:-180}
apt_tries=${GATE_APT_TRIES:-3}
apt_out=
apt_why=
apt_try() {  # $1 = what it is, then apt-get's arguments; on failure, apt_why and apt_out say why
    local what=$1 rc i
    shift
    echo "gate-host-setup: $what"
    i=1
    while [ "$i" -le "$apt_tries" ]; do
        rc=0
        # line-buffered, so what a killed apt had printed is not lost
        apt_out=$(timeout "$apt_limit" sudo stdbuf -oL -eL apt-get "${apt_opts[@]}" "$@" 2>&1) || rc=$?
        [ "$rc" -ne 0 ] || return 0
        if [ "$rc" -eq 124 ]; then apt_why="did not finish in ${apt_limit}s"; else apt_why="failed (exit $rc)"; fi
        [ "$i" -lt "$apt_tries" ] || break
        echo "gate-host-setup: $what $apt_why; trying again ($((i + 1)) of $apt_tries)"
        timeout "$apt_limit" sudo dpkg --configure -a >/dev/null 2>&1 || true
        i=$((i + 1))
    done
    apt_why="$apt_why, $apt_tries times"
    return 1
}
apt_step() {
    apt_try "$@" && return 0
    echo "::error title=gate-host-setup::$1 $apt_why; what the last try printed:"
    printf '%s\n' "$apt_out"
    exit 1
}

# One runner in six got the archive's .debs at about 70 kB/s on 2026-10-26 (mavericks-vm's
# install-tools.sh), so the .debs this runner image lacks are kept in actions/cache, keyed by the
# image and this list (gate.yml): a hit installs them with no mirror; a miss fetches them once, into
# GATE_DEBS_DIR, for the workflow to save. Offline means apt has no sources at all: --no-download
# refuses local .debs, and a plain install of them fetches them all again.
debs=${GATE_DEBS_DIR:-}
debs_install() {  # the .debs in $debs, offline; on failure, apt_why and apt_out say why
    local f=("$debs"/*.deb)
    [ -e "${f[0]}" ] || return 0
    apt_try "apt-get install of QEMU and the plugin's build tools" \
        install -y -q --no-install-recommends -o Dir::Etc::SourceList=/dev/null \
        -o Dir::Etc::SourceParts=/nonexistent "${f[@]}"
}
from_archive() {
    apt_step "apt-get update" update -qq
    if [ -z "$debs" ]; then
        apt_step "apt-get install of QEMU and the plugin's build tools" \
            install -y -q --no-install-recommends "${pkgs[@]}"
        return
    fi
    echo "gate-host-setup: no apt packages cached for this runner image: fetching them from the archive, for actions/cache to keep"
    rm -rf "$debs" && mkdir -p "$debs/partial"
    apt_step "apt-get download of QEMU and the plugin's build tools" \
        install -y -q --no-install-recommends --download-only -o "Dir::Cache::Archives=$debs" "${pkgs[@]}"
    # apt's lock is root's alone, and actions/cache could not read it
    sudo rm -rf "$debs/lock" "$debs/partial"
    if ! debs_install; then
        echo "::error title=gate-host-setup::apt-get install of QEMU and the plugin's build tools $apt_why; what the last try printed:"
        printf '%s\n' "$apt_out"
        exit 1
    fi
    printf '%s\n' "${pkgs[@]}" > "$debs/packages"
    echo "debs-save=true" >> "${GITHUB_OUTPUT:-/dev/null}"
}

if [ -n "$debs" ] && [ "$(cat "$debs/packages" 2>/dev/null)" = "$(printf '%s\n' "${pkgs[@]}")" ]; then
    echo "gate-host-setup: installing the $(find "$debs" -maxdepth 1 -name '*.deb' | wc -l | tr -d ' ') apt packages cached for this runner image, with no download"
    if ! debs_install; then
        echo "::warning title=gate-host-setup::the apt packages cached for this runner image did not install offline ($apt_why), so they are fetched from the archive again; what apt printed:"
        printf '%s\n' "$apt_out"
        from_archive
    fi
else
    from_archive
fi

# GitHub's runner user is not in the kvm group; the device is root's alone by default
sudo chmod 666 /dev/kvm
# a cpu-model of Nehalem or later makes 10.9's kernel read MSR_FLEX_RATIO (0x194) at boot, which KVM
# emulates on neither vendor: ignored, it reads 0 and the guest boots (docs/decisions/0009)
echo Y | sudo tee /sys/module/kvm/parameters/ignore_msrs >/dev/null
# the plugin's media microVM boots the running kernel, which Ubuntu leaves readable by root alone
rel=$(uname -r)
sudo chmod 644 "/boot/vmlinuz-$rel"

# The microVM loads hfsplus into the kernel it boots, to mount the installer: every gate builds the
# base, so without it the gate cannot run. Ubuntu's Azure kernels through 6.17 keep it in
# linux-modules-extra-<release>, which the runner image does not install; 7.0's have it built as a
# module every kernel carries (mavericks-vm's install-tools.sh).
mods=${GATE_MODULES_DIR:-/lib/modules}
if [ -n "$(find -H "$mods/$rel" -name 'hfsplus.ko*' -print -quit 2>/dev/null)" ] ||
    grep -qs '/hfsplus\.ko$' "$mods/$rel/modules.builtin"; then
    echo "gate-host-setup: the running kernel, $rel, has hfsplus"
elif apt-cache show "linux-modules-extra-$rel" >/dev/null 2>&1; then
    echo "gate-host-setup: the running kernel, $rel, has no hfsplus; installing linux-modules-extra-$rel, which has it"
    apt_step "apt-get install of linux-modules-extra-$rel" \
        install -y -q --no-install-recommends "linux-modules-extra-$rel"
else
    echo "::error title=gate-host-setup::the running kernel, $rel, has no hfsplus module, and the archive has no linux-modules-extra-$rel: the gate's base build would fail mounting the installer"
    exit 1
fi
echo "gate-host-setup: ready"
