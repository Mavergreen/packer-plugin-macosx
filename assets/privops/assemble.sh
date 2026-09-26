# shellcheck shell=sh
# This runs under busybox ash inside the microVM, not under the host's
# shell, and it is sourced-by-path rather than executed -- hence a shell
# directive instead of a shebang.
#
# shellcheck disable=SC2016  # the awk program is single-quoted on purpose
#
# THE WHOLE HFS+ ASSEMBLY, INSIDE THE MICROVM.
#
# Not on the host. There, udisks2 would have to attach a loop device and
# mount the volumes under /run/media/$USER. That needs a desktop seat
# (docs/host-profile.md G26) -- polkit refuses loop-setup over SSH -- and
# it puts file-browser windows and notification popups on the screen of
# anyone who has one. Here there is no loop device and no host mount at
# all.
#
# Runs as uid 0 with:
#   $MQG_MNT   the target volume, read-write
#   $MQG_SRC1  the BaseSystem volume, read-only
#   $MQG_SRC2  the ESD volume, read-only
#   $MQG_RAW3  optional: a tar of files to inject, already named for
#              where they go on the media. Absent when the build was not
#              asked for any.
#
# The numbering is the order the media build (internal/media/build.go,
# the assembly pass) attaches them.
#
# NOTHING HERE CHOWNS ANYTHING. Ownership is assets/privops/fix-ownership.sh
# in a pass of its own, after every write, because it has to record the
# setuid and setgid modes before the chown that strips them and restore
# them afterwards -- a destructive step must not run before the code that
# makes it reversible.

BS=$MQG_SRC1
ESD=$MQG_SRC2
PKGLINK=$MQG_MNT/System/Installation/Packages

# Elapsed seconds since this payload started, so a build on a host nobody
# has tried can say WHICH step was slow rather than that the pass took a
# minute. There is no `time` worth the name in here and no way to watch
# from outside: the console is not printed until the guest has powered off.
T0=$($B date +%s)
step() { echo "[$(( $($B date +%s) - T0 ))s] $*"; }

# Counted before and after, and printed. A copy that silently moved
# nothing looks exactly like a copy that worked, until something
# downstream fails for a reason that makes no sense.
#
# "bytes in files" and not `du`: this counts regular files only, where the
# host's old `du -sb` counted every entry including directories, and HFS+
# reports a size for each of those. The two numbers differ by about 143 MB
# on this media -- roughly 12,000 directories -- and nothing is missing.
# Said here because an unexplained 143 MB drop in a log line is exactly
# the sort of thing that costs somebody an afternoon.
count_tree() {
    echo "$2: $($B find "$1" | $B grep -c .) entries, $(
        $B find "$1" -type f 2>/dev/null \
            | $B xargs -r "$B" stat -c %s 2>/dev/null \
            | $B awk '{ t += $1 } END { print t + 0 }') bytes in files"
}

count_tree "$BS" "BaseSystem source"

# `cp -a`, not rsync: there is no rsync in a busybox initramfs, and none is
# needed. Busybox's -a is -dpR, which preserves symlinks, modes and times,
# and -- measured, because the volume has no room to find out the hard way
# -- HARDLINKS. BaseSystem hardlinks several binaries; copying each one
# again would not fit.
#
# Two things the host path had to work around and this one does not:
#
#   * /.file, mode 0000, the marker OS X looks for to decide a volume has
#     a filesystem on it. The host could not read it and recreated it by
#     hand; uid 0 simply copies it.
#   * ownership. udisks mounted hfsplus uid=<you>,gid=<you>, so every file
#     arrived owned by the building user whatever flags rsync was given.
#     Here the source's own ownership is what lands.
step "copying BaseSystem onto the target volume"
$B cp -a "$BS/." "$MQG_MNT/" || { echo "cp of BaseSystem failed"; exit 1; }
count_tree "$MQG_MNT" "after BaseSystem"

# On the ESD this is a symlink to /System/Installation/PackagesLink, which
# resolves through the ESD volume. On installer media there is no ESD
# volume, so the real directory goes here instead.
if [ ! -L "$PKGLINK" ]; then
    echo "expected a Packages symlink at $PKGLINK"
    exit 1
fi
step "replacing the Packages symlink with the ESD's real Packages"
$B rm -f "$PKGLINK" || exit 1
$B mkdir -p "$PKGLINK" || exit 1
$B cp -a "$ESD/Packages/." "$PKGLINK/" \
    || { echo "cp of Packages failed"; exit 1; }

step "copying BaseSystem.dmg and BaseSystem.chunklist"
$B cp -a "$ESD/BaseSystem.dmg" "$ESD/BaseSystem.chunklist" \
    "$MQG_MNT/System/Installation/" \
    || { echo "cp of BaseSystem.dmg/chunklist failed"; exit 1; }

# The unattended-install hooks and any packages this build carries, as a
# tar the host built with the paths and modes they are to have. Extracted
# straight onto the media rather than into the initramfs first: the
# initramfs is RAM, and the OpenSSH packages alone are twelve megabytes.
#
# Done BEFORE the ownership pass, deliberately. Anything injected after it
# would be the one uid-1000 file on otherwise root-owned media -- exactly
# the state that made launchd say "Dubious ownership on file (skipping)"
# and load nothing at all.
#
# NOT `tar ... | sed`, and that is not style. busybox ash has no
# `pipefail`, so the status of a pipeline is the LAST command's -- sed's,
# which succeeds whatever tar did. A tar failure would be swallowed and
# the media would ship without its install hooks. This project has written
# that lesson down twice already (a screenshot piped into `head -1`, and
# a command substitution that ate a fatal exit), and it did not travel.
if [ -n "${MQG_RAW3:-}" ]; then
    step "injecting the files the host staged"
    if ! $B tar xvf "$MQG_RAW3" -C "$MQG_MNT" > /inject.log 2>&1; then
        $B sed 's/^/  /' /inject.log
        echo "extracting the injectables failed"
        exit 1
    fi
    $B sed 's/^/  /' /inject.log
fi

# The ESD's Packages, checked where they are read rather than where they
# land, so that a bad dmg2img conversion is told apart from a bad copy.
# The host compares these against assets/pins/apple-packages.sha256 --
# Apple's own values, a constant, not something this build measured.
step "checksumming the ESD's Packages"
( cd "$ESD/Packages" || exit 1
  for f in *; do
      [ -f "$f" ] || continue
      echo "MQG-SUM-ESD $($B sha256sum "$f")"
  done )

step "counting the finished volume"
count_tree "$MQG_MNT" "final volume"
echo "free space on the target volume:"
$B df -h "$MQG_MNT" | $B sed 's/^/  /'
step "done"
