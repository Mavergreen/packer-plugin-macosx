# shellcheck shell=sh
# The user's installer disc, mounted read-only as $MQG_SRC1: its build,
# then the SHA-256 of every file in its Packages directory, for
# internal/disc.Verify to hold against assets/pins/snowleopard-packages.sha256.
# Nothing here writes: the target is a scratch volume the backend needs to
# mount, and the disc is never attached read-write.
v=$MQG_SRC1/System/Library/CoreServices/SystemVersion.plist
if [ ! -f "$v" ]; then
    echo "no SystemVersion.plist on the disc at $v"
    exit 1
fi
build=$($B sed -n '/<key>ProductBuildVersion<\/key>/{n;s/.*<string>\(.*\)<\/string>.*/\1/p;}' "$v")
echo "MQG-DISC-BUILD $build"
d=$MQG_SRC1/System/Installation/Packages
if [ ! -d "$d" ]; then
    echo "no Packages directory on the disc at $d"
    exit 1
fi
n=0
cd "$d" || exit 1
for f in *; do
    [ -f "$f" ] || continue
    echo "MQG-SUM-DISC $($B sha256sum "$f")"
    n=$((n + 1))
done
echo "checksummed $n files in System/Installation/Packages"
[ "$n" -gt 0 ] || exit 1
