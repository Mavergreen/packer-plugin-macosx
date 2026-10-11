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
# The DMG is mounted read-only, out of the Finder's sight (-nobrowse), at
# a mount point of its own, and detached, then removed with the choice
# changes, whether the installer succeeded or not: the guest becomes a
# box, and nothing of the install stays in it but what installer put
# there. A failing installer fails the build.

set -eu

# MQG_AQUATRANSPORT_DMG is a test seam (tests/template_aquatransport.bats),
# never set by the template, which uploads the DMG to this path.
DMG=${MQG_AQUATRANSPORT_DMG:-/private/tmp/AquaTransport.dmg}
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

hdiutil attach -nobrowse -readonly -mountpoint "$mnt" "$DMG"

status=0
sudo installer -pkg "$mnt/AquaTransport.pkg" -applyChoiceChangesXML "$choices" -target / || status=$?

hdiutil detach "$mnt"
rm -f "$DMG" "$choices"
rmdir "$mnt" 2>/dev/null || true

if [ "$status" -ne 0 ]; then
    echo "install: installer failed (exit $status); see /var/log/install.log in the guest" >&2
fi
exit "$status"
