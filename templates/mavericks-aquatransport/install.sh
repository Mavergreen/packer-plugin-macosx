#!/bin/sh
# Guest-side: install Aqua Transport from the DMG the template uploaded,
# without tap.kext. The template runs this over SSH as the build's own
# user, as `sh install.sh` -- 10.9's /bin/sh, which is its bash 3.2 in
# POSIX mode -- so it is POSIX sh throughout (and bin/bash32-check.sh
# covers it too).
#
# THE CHOICES. AquaTransport.pkg is a distribution with three choices:
# Aqua Transport itself, ModernRootCertificates, and the AirDrop kernel
# extension tap.kext (installer_choice_3, pkg-ref net.sf.tuntaposx.tap),
# all three on by default on 10.9.5. tap is not wanted -- a kext the
# guest has no use for -- so the choice changes below deselect it and
# leave the other two as they are. verify.sh checks the outcome: both
# receipts there, and no tap receipt or kext.
#
# The DMG is mounted as root, as installer runs, read-only, out of the
# Finder's sight (-nobrowse), at a mount point of its own. It is detached
# and removed with the choice changes whether the installer succeeded or
# not: the guest becomes a box, and nothing of the install stays in it
# but what installer put there. A failing installer, or a failing detach,
# fails the build, and each says so; the installer's status wins.
#
# AQUATRANSPORT_DMG is where the template uploaded the DMG, passed in by
# the provisioner's environment_vars, so the path is written down once,
# in the template. Unset, this does nothing and fails.

set -eu

if [ -z "${AQUATRANSPORT_DMG:-}" ]; then
    echo "install: AQUATRANSPORT_DMG is not set: the template's install.sh provisioner" \
        "must pass environment_vars = [\"AQUATRANSPORT_DMG=\${local.aquatransport_guest_dmg}\"]" >&2
    exit 2
fi
DMG=$AQUATRANSPORT_DMG
tmp=${TMPDIR:-/tmp}
tmp=${tmp%/}

mnt=$(mktemp -d "$tmp/aquatransport-mnt.XXXXXX")
choices=$(mktemp "$tmp/aquatransport-choices.XXXXXX")

cat > "$choices" <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<array>
	<dict>
		<key>attributeSetting</key>
		<integer>0</integer>
		<key>choiceAttribute</key>
		<string>selected</string>
		<key>choiceIdentifier</key>
		<string>installer_choice_3</string>
	</dict>
</array>
</plist>
EOF

sudo hdiutil attach -nobrowse -readonly -mountpoint "$mnt" "$DMG"

status=0
sudo installer -pkg "$mnt/AquaTransport.pkg" -applyChoiceChangesXML "$choices" -target / || status=$?

detached=0
sudo hdiutil detach "$mnt" || detached=$?
rm -f "$DMG" "$choices"
rmdir "$mnt" 2>/dev/null || true

if [ "$status" -ne 0 ]; then
    echo "install: installer failed (exit $status); see /var/log/install.log in the guest" >&2
fi
if [ "$detached" -ne 0 ]; then
    echo "install: hdiutil detach $mnt failed (exit $detached)" >&2
    [ "$status" -ne 0 ] || status=$detached
fi
exit "$status"
