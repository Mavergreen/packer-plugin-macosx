# shellcheck shell=sh
# This runs under busybox ash inside the microVM, not under the host's
# shell, and it is sourced-by-path rather than executed -- hence a shell
# directive instead of a shebang.
#
# shellcheck disable=SC2016  # the awk programs are single-quoted on purpose
#
# Runs as uid 0 inside the privops microVM, media mounted at $MQG_MNT,
# busybox at $B.
#
# Media copied unprivileged arrives owned by the building user, and
# launchd refuses to load daemons from a directory it does not trust:
# "Dubious ownership on file (skipping)" then "nothing found to load". Apple
# ships this media uniformly root-owned, so that is what we restore.
#
# The catch: chown clears setuid and setgid bits. The reference media has
# six such files and one of them -- Install.framework's `runner` -- belongs
# to the installer itself. So record them first and restore them after.
#
# The list lives in a shell variable rather than a temp file, because the
# initramfs has no writable /tmp and six entries do not need one.

SPECIAL=$($B find "$MQG_MNT" -type f \( -perm -4000 -o -perm -2000 \) 2>/dev/null)
n_before=$(printf '%s\n' "$SPECIAL" | $B grep -c . )
echo "special-mode files before chown: $n_before"

MODES=""
if [ "$n_before" -gt 0 ]; then
    MODES=$(printf '%s\n' "$SPECIAL" | while read -r f; do
        [ -n "$f" ] && printf '%s\t%s\n' "$($B stat -c '%a' "$f")" "$f"
    done)
    printf '%s\n' "$MODES" | $B head -8 | $B sed 's/^/  recorded: /'
fi

echo "ownership before: $($B ls -ldn "$MQG_MNT/System/Library/LaunchDaemons" | $B awk '{print $3":"$4}')"
$B chown -R 0:0 "$MQG_MNT" || exit 1
echo "ownership after:  $($B ls -ldn "$MQG_MNT/System/Library/LaunchDaemons" | $B awk '{print $3":"$4}')"

restored=0
if [ -n "$MODES" ]; then
    OLDIFS=$IFS; IFS='
'
    for line in $MODES; do
        mode=${line%%	*}
        path=${line#*	}
        [ -n "$mode" ] && $B chmod "$mode" "$path" && restored=$((restored + 1))
    done
    IFS=$OLDIFS
fi
echo "special modes restored: $restored"
echo "special-mode files after: $($B find "$MQG_MNT" -type f \( -perm -4000 -o -perm -2000 \) 2>/dev/null | $B grep -c .)"
echo "runner: $($B ls -ln "$MQG_MNT/System/Library/PrivateFrameworks/Install.framework/Versions/A/Resources/runner" 2>/dev/null | $B awk '{print $1}')"

# The unattended-install hooks, if this build injected them. Reported here
# because this is the step that makes them root-owned, and because
# rc.install skips rc.cdrom.local silently unless it is executable -- a
# mode of 755 in this output is the evidence that the media will actually
# install unattended.
for f in private/etc/rc.cdrom.local \
         System/Installation/Packages/Extras/minstallconfig.xml \
         System/Installation/Packages/OSInstall.collection; do
    if [ -e "$MQG_MNT/$f" ]; then
        echo "autoinstall: $($B ls -ln "$MQG_MNT/$f" | $B awk '{print $1, $3":"$4}') $f"
    else
        echo "autoinstall: absent $f"
    fi
done
