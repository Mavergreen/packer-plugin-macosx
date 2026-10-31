#!/bin/sh
# platform: host-agnostic -- never run: commandwords_test.go reads it; each construct is one the command reader must read, or refuse naming its line
su - "$1" -c "alpha -x $2"
su - "$1" -c 'beta -q'
su - "$1" -c "$(gamma)"
sh -c "$1"
sudo -V 2>/dev/null
sudo -u nobody delta
printf '%s\n' a | xargs epsilon
find /tmp -name x -exec zeta {} \;
find /tmp -name y -print
cat > /tmp/unquoted <<EOF2
value $(eta)
EOF2
cat > /tmp/job.plist <<'PLIST'
<array><string>/bin/sh</string><string>-c</string><string>theta</string></array>
PLIST
