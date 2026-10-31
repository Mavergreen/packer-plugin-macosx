#!/bin/sh
# platform: macOS-only -- runs in the guest's OS X Installer environment, never on the build host
# Prepare the target disk for an unattended Mavericks install.
#
# WHERE THIS RUNS
#
# Injected onto the installer media as /private/etc/rc.cdrom.local, which
# Apple's own /etc/rc.install sources early in the installer environment:
#
#     39: if [ -x /etc/rc.cdrom.local ]; then
#     40:     /etc/rc.cdrom.local
#
# This is a supported hook, not a hole we found. Note that /etc/rc.cdrom
# does NOT source it -- rc.cdrom reaches rc.install via `launchctl load
# -D system`, so reading only rc.cdrom makes the hook look absent. It is
# not: rc.install's lines 39-40, above, are where it runs.
#
# WHAT IT DOES, AND WHAT IT DELIBERATELY DOES NOT
#
# It prepares the disk and nothing else. The install itself is Apple's:
# /System/Installation/Packages/Extras/minstallconfig.xml puts the OS X
# Installer into its own automated mode, and rc.install reboots when that
# finishes. So there is no `installer -pkg` call here, and no `shutdown`
# -- both would be us reimplementing something already present.
#
# Once it hands off, nothing of ours writes to the target volume again.
# The installer's first step is a volume consistency check that unmounts
# the target, repairs it and has it mounted again, and a write of ours there
# in that window fails the whole install, either way the generic "The
# Installer encountered an error that caused the installation to fail"
# (MEASURED 2026-10-19, docs/decisions/0027):
#
#   - a file of ours open on the target: "Verify/Repair failed with error
#     -10000 (Could not unmount disk)";
#   - a file of ours written into /Volumes/<name> once it is unmounted:
#     disk arbitration cannot remove the mount point, mounts the volume at
#     "/Volumes/<name> 1", and the installer goes on at /Volumes/<name>,
#     now a folder on the 512 KB RAM disk under /Volumes, until PackageKit
#     runs out of space there ("destination=/Volumes/.OSInstallSandbox-tmp").
#
# What this script leaves running after it exits -- the log follower below
# -- reads its log in /var/tmp and a socket, and writes the serial port:
# never the target.
#
# By the time this runs, rc.cdrom has already remounted the root (the
# media) read-only and mounted union RAM disks over some of it -- 10.6's
# over /Volumes, /var/tmp, /var/run, /var/db, /Library/Preferences and
# /Library/ColorSync/Profiles/Displays; 10.9's over those and more, /var/log
# and /System/Installation among them. So /var/tmp is writable and the
# media is not; and on 10.6, /var/log is not either.
#
# CHOOSING THE TARGET
#
# Two disks are attached as the installer sees them: the installer media
# we booted from and the disk to install onto. (OpenCore's EFI image is a
# third disk, but on virtio-blk, which the installer has no driver for:
# docs/decisions/0035.) Upstream (timsutton/osx-vm-templates) hardcodes
# diskN with a fallback to the next one, which is fine for a VM with one
# disk and unsafe here.
#
# The key is being UNPARTITIONED. The media carries a partition table, as
# would any other disk the installer could see; a freshly created target
# has nothing but zeroes,
# so `diskutil list` shows it with no slices at all. Size is a secondary
# guard. If that does not name exactly one disk, this script erases
# nothing and stops -- see the comment on `give_up` for why stopping means
# sleeping rather than exiting.

set -u

VOLNAME=${GUEST_MACOSX_TARGET_VOLUME:-Mavericks}

# 16 GiB. A secondary guard only: the primary test is that the disk has no
# partitions. Mavericks itself lands at about 8.5 GiB, so anything smaller
# than this is not a disk anybody meant to install onto.
MIN_BYTES=${GUEST_MACOSX_TARGET_MIN_BYTES:-17179869184}

# How long to wait for the target to show up, in seconds. Disk arbitration
# is a launchd job and the SATA ports are probed asynchronously, so the
# first scan can genuinely run before the disk exists.
WAIT_SECONDS=${GUEST_MACOSX_TARGET_WAIT:-120}

LOG=${GUEST_MACOSX_AUTOINSTALL_LOG:-/var/tmp/guest-macosx-autoinstall.log}

# Where disk arbitration mounts volumes. Overridable only so
# tests/guest_assets.bats can run the whole script against a stub
# diskutil and watch what it writes there.
VOLUMES=${GUEST_MACOSX_VOLUMES:-/Volumes}
VOLUME_LOG_NAME=.guest-macosx-autoinstall.log

# Set to 1 to print the disk this would erase and exit without touching
# it. This exists so tests/guest_assets.bats can drive the selection logic
# against recorded `diskutil list` output. Choosing the wrong disk here
# destroys the installer media, and "I read it carefully" is not a test.
DRY_RUN=${GUEST_MACOSX_AUTOINSTALL_DRY_RUN:-0}

# How long give_up halts, in seconds. Only tests shorten it.
GIVE_UP_SLEEP=${GUEST_MACOSX_GIVE_UP_SLEEP:-9999999}

# Overridable so those same tests can substitute a stub. Nothing in the
# installer environment sets it.
DISKUTIL=${GUEST_MACOSX_DISKUTIL:-/usr/sbin/diskutil}
GREP=/usr/bin/grep
SED=/usr/bin/sed
HEAD=/usr/bin/head
LOGGER=/usr/bin/logger

# Progress goes to stderr, never stdout. `find_candidates` prints disk
# identifiers on stdout for its caller to capture, and a stray progress
# line mixed into that would become a "disk" the script then tried to
# erase.
say() {
    echo "guest-macosx-autoinstall: $*" >&2
    echo "guest-macosx-autoinstall: $*" >> "$LOG" 2>/dev/null
    "$LOGGER" -t guest-macosx-autoinstall -p install.info "$*" 2>/dev/null
    mirror_log
}

# Copy the log onto the target volume, once there is one. That is the copy
# that survives the reboot and can be read from the host afterwards; the
# one in /var/tmp dies with the RAM disk. Only while this script runs, before
# the installer starts: MIRROR=0 in what outlives it (see the top).
MIRROR=1
mirror_log() {
    if [ "$MIRROR" = 1 ] && [ -d "$VOLUMES/$VOLNAME" ]; then
        cp "$LOG" "$VOLUMES/$VOLNAME/$VOLUME_LOG_NAME" 2>/dev/null
    fi
    return 0
}

# THE LOGS, LIVE, ON THE SERIAL PORT
#
# A copy on the target volume outlives the installer only as long as the
# disk does, and a failed build's disk does not: Packer deletes the output
# directory, the disk in it, when a build fails. So the logs go, a line at
# a time as they are written, to the guest's first serial port, which the
# template gives QEMU as a file on the host, outside that directory. A
# failed install leaves its own account behind.
#
# Two logs: this script's own, and syslog -- the installer's own lines
# (facility install), and every daemon's notices and the kernel's, which
# say what the installer's disk work did. Neither release keeps the
# installer's lines in a file: 10.6's /var/log is on the read-only root
# (rc.cdrom's RAM disks, above, leave it out), so its syslogd writes
# nothing at all, and 10.9's asl.conf writes system.log to its /var/log RAM
# disk but sends install.* nowhere but instlogd. They live only in
# instlogd's memory until the installer copies them onto the target, which
# a failed install never does.
# The media's syslog.conf forwards them to SYSLOG_PORT on 127.0.0.1 instead
# (assets/privops/disc/assemble-disc.sh, assets/privops/assemble.sh), as
# Apple's own line forwards install.* to instlogd's, and the follower takes
# them there: no file, no disk, nothing that touches the disks the
# installer is working on. Making /var/log writable with a RAM disk over it,
# as rc.cdrom does its own, was MEASURED to fail the install a third of the
# time: the volume check's remount lost its probe of the target ("unable to
# probe"), then "Could not create mutable product" (docs/decisions/0027).
#
# The port is a precondition, not an assumption: kextd matches the serial
# driver to it after boot, so it appears late, or never (a VM without one).
# The follower takes syslog from its first second all the same -- it is
# also the watch for the installer's failure (below) -- holding what it
# has until the port appears, up to SERIAL_WAIT, and logs which: the port
# and how long it took, or that there was none.
#
# The follower is Perl because the installer environment has no tail
# (internal/payload/testdata/commands/snowleopard-installer.txt); rc.install
# itself runs perl, though one with no modules, so the socket's constants
# are spelled out. It prints each complete line of a followed file once,
# holding a partial line until the rest arrives, and each syslog message as
# it comes, each tagged with where it came from. It sets the port to 115200
# baud (QEMU paces its UART at the line's speed; a fresh open gets 9600),
# holding it open so the setting stays.
SERIAL_PORTS=${GUEST_MACOSX_SERIAL_PORTS:-/dev/cu.serial1 /dev/cu.serial}
SERIAL_WAIT=${GUEST_MACOSX_SERIAL_WAIT:-120}
SYSLOG_PORT=${GUEST_MACOSX_SYSLOG_PORT:-32377}
# How long the follower runs, in seconds; 0, the default, until the
# installer environment ends. Only tests bound it.
FOLLOW_FOR=${GUEST_MACOSX_FOLLOW_FOR:-0}
STTY=${GUEST_MACOSX_STTY:-/bin/stty}
PERL=${GUEST_MACOSX_PERL:-/usr/bin/perl}

stream_logs() {
    MIRROR=0
    # shellcheck disable=SC2016,SC2086 # Perl's own $s; SERIAL_PORTS is a list
    "$PERL" -e '
        my ($for, $sport, $log, $hook, $wait, $stty, @ports) = @ARGV;
        my $start = time;
        # A line in the hook log, as say writes it; the follower prints it
        # from there like any other.
        sub note {
            if (open(my $l, ">>", $log)) { print $l "guest-macosx-autoinstall: $_[0]\n"; close($l); }
            print STDERR "guest-macosx-autoinstall: $_[0]\n";
        }
        # AF_INET and SOCK_DGRAM are 2 on both; a BSD sockaddr_in leads
        # with its length.
        my $sa = $^O eq "darwin" ? pack("CCnC4x8", 16, 2, $sport, 127, 0, 0, 1)
                                 : pack("SnC4x8", 2, $sport, 127, 0, 0, 1);
        my ($out, $held, $portdone, $failed) = (undef, "", 0, 0);
        sub emit {
            if ($out) { print $out $_[0]; }
            elsif (!$portdone && length($held) < 4000000) { $held .= $_[0]; }
        }
        my $udp;
        if (socket($udp, 2, 2, 0) && bind($udp, $sa)) {
            emit("follower: syslog on 127.0.0.1:$sport\n");
        } else {
            emit("follower: no syslog on 127.0.0.1:$sport: $!\n");
            undef $udp;
        }
        my ($fh, $part) = (undef, "");
        my $end = $for > 0 ? time + $for : 0;
        while (!$end || time < $end) {
            if (!$portdone) {
                my $port;
                for my $p (@ports) {
                    if (-w $p && open(my $o, ">>", $p)) { ($out, $port) = ($o, $p); last; }
                }
                if ($out) {
                    my $old = select($out); $| = 1; select($old);
                    my $ok = 0;
                    if (open(my $in, "<", $port) && open(my $save, "<&", \*STDIN)) {
                        open(STDIN, "<&", $in);
                        $ok = system($stty, "115200", "clocal", "-crtscts") == 0;
                        open(STDIN, "<&", $save);
                    }
                    note($ok ? "serial: $port at 115200 baud"
                             : "serial: stty could not set $port to 115200 baud; it keeps its own speed, and a slow one drops syslog lines in bursts");
                    note("serial: following $log and syslog on 127.0.0.1:$sport to $port (there after " . (time - $start) . "s)");
                    print $out $held;
                    ($held, $portdone) = ("", 1);
                } elsif (time - $start >= $wait) {
                    note("serial: no port (@ports) after " . (time - $start) . "s; the logs are not followed, only watched for the installer\x27s failure");
                    ($held, $portdone) = ("", 1);
                }
            }
            # The refusal, once it has started its answer, reaped (WNOHANG is
            # 1 on both). Not with SIGCHLD ignored: 10.6 perl passes that on
            # through exec, and the refusal could then wait for none of its
            # own children (MEASURED).
            1 while waitpid(-1, 1) > 0;
            if (!$fh && open(my $h, "<", $log)) { $fh = $h; }
            if ($fh) {
                seek($fh, 0, 1);
                while (defined(my $l = <$fh>)) {
                    $part .= $l;
                    next unless $part =~ /\n\z/;
                    emit("hook: $part");
                    $part = "";
                }
            }
            my $rin = "";
            vec($rin, fileno($udp), 1) = 1 if $udp;
            my $wait1 = 1;
            while (select(my $rout = $rin, undef, undef, $wait1) > 0) {
                last unless $udp && defined(recv($udp, my $m, 8192, 0));
                $m =~ s/\n*\z/\n/;
                emit("syslog: $m");
                $wait1 = 0;
                # The installer has failed: end the build (refuse_logins),
                # once, in a process of its own. Only the line the installer
                # itself logs, as syslog forwards it: "<pri>Mon dd hh:mm:ss
                # host OSInstaller[pid]: Install failed..."; not a PackageKit
                # line about one package, nor another process quoting it.
                next if $failed
                    || $m !~ /^<\d+>\w{3} [ \d]\d \d\d:\d\d:\d\d \S+ OSInstaller\[\d+\]: Install failed/;
                $failed = 1;
                (my $why = $m) =~ s/^<\d+>//;
                chomp $why;
                my $pid = fork;
                if (!defined $pid) {
                    note("follower: could not start the refusal: fork: $!; the build waits out install_timeout");
                } elsif ($pid == 0) {
                    $ENV{GUEST_MACOSX_AUTOINSTALL_ONLY} = "refuse_logins";
                    open(STDOUT, ">", "/dev/null");
                    exec("/bin/sh", $hook, "the installer said: $why");
                    note("follower: could not start the refusal: /bin/sh $hook: $!; the build waits out install_timeout");
                    exit 127;
                }
            }
        }' "$FOLLOW_FOR" "$SYSLOG_PORT" "$LOG" "$0" "$SERIAL_WAIT" "$STTY" $SERIAL_PORTS
}

# A FAILED INSTALL ENDS THE BUILD
#
# The build waits for SSH -- the template's install_timeout, an hour --
# and, in Packer's qemu builder (v1.1.7; packer-plugin-sdk v0.6.11's
# StepConnectSSH), for nothing else: it does not watch QEMU while it waits,
# so a guest that powered itself off would cost the whole hour too. What
# it does count is a handshake error that says "authenticate": both
# templates set ssh_handshake_attempts, and that many end the build.
#
# So once the install has failed -- the installer logged "Install failed",
# or this script gave up -- port 22 answers every connection with an SSH
# version line and one SSH_MSG_DISCONNECT, before any key exchange, whose
# text says what failed and where its log is, and that nobody can
# authenticate. Packer's client (x/crypto's ssh) reports that text as its
# error, and its "authenticate" counts: the build fails within a minute or
# two, saying why (docs/decisions/0028). No sshd, no key, nothing but perl,
# which both installer environments have.
#
# Only here: the media puts this script at /etc/rc.cdrom.local. Anywhere
# else -- a test, on any host -- it starts nothing unless the perl and the
# port are both named (GUEST_MACOSX_PERL, GUEST_MACOSX_REFUSE_PORT), so no
# test can bind a host's port 22.
INSTALLER_HOOK=/etc/rc.cdrom.local
REFUSE_PORT=${GUEST_MACOSX_REFUSE_PORT:-22}
REFUSE_ADDR=${GUEST_MACOSX_REFUSE_ADDR:-0.0.0.0}
REFUSE_PIDFILE=${GUEST_MACOSX_REFUSE_PIDFILE:-/var/tmp/guest-macosx-refuse.pid}

refuse_logins() {
    MIRROR=0
    say "INSTALL FAILED: $*"
    if [ "$0" != "$INSTALLER_HOOK" ] \
            && { [ -z "${GUEST_MACOSX_PERL:-}" ] || [ -z "${GUEST_MACOSX_REFUSE_PORT:-}" ]; }; then
        say "refuse: not the installer environment ($0), and GUEST_MACOSX_PERL and GUEST_MACOSX_REFUSE_PORT not both named; nothing started"
        return 1
    fi
    if [ -s "$REFUSE_PIDFILE" ] && kill -0 "$(cat "$REFUSE_PIDFILE")" 2>/dev/null; then
        say "refuse: already answering (pid $(cat "$REFUSE_PIDFILE"))"
        return 0
    fi
    rm -f "$REFUSE_PIDFILE"
    release=$(echo "$VOLNAME" | awk '{ s = tolower($0); gsub(/ /, "", s); print s }')
    # The installer's line ends in its own full stop; one is enough.
    why=$*
    why=${why%.}
    text="INSTALL FAILED: $why. Nobody can authenticate here: this guest is the installer, stopped. Its log is output-$release-install.log, beside the build's output directory."
    # shellcheck disable=SC2016 # Perl's own $s
    "$PERL" -e '
        my ($addr, $port, $text, $pidfile) = @ARGV;
        my @a = split(/\./, $addr);
        # AF_INET 2, SOCK_STREAM 1, on both; SOL_SOCKET and SO_REUSEADDR
        # differ (0xffff and 4 on BSD, 1 and 2 on Linux).
        my ($sa, $sol, $reuse) = $^O eq "darwin"
            ? (pack("CCnC4x8", 16, 2, $port, @a), 0xffff, 4)
            : (pack("SnC4x8", 2, $port, @a), 1, 2);
        socket(my $s, 2, 1, 0) or die "refuse: socket: $!\n";
        setsockopt($s, $sol, $reuse, 1);
        bind($s, $sa) or die "refuse: bind $addr:$port: $!\n";
        listen($s, 5) or die "refuse: listen: $!\n";
        $SIG{PIPE} = "IGNORE";
        # SSH_MSG_DISCONNECT (1), reason 14 (no more authentication methods),
        # the text, no language; padded as an unencrypted packet must be.
        my $payload = pack("C N N/a* N/a*", 1, 14, $text, "");
        my $pad = 8 - ((5 + length($payload)) % 8);
        $pad += 8 if $pad < 4;
        my $answer = "SSH-2.0-guest-macosx-install-failed\r\n"
            . pack("N C", 1 + length($payload) + $pad, $pad) . $payload . ("\0" x $pad);
        if (open(my $p, ">", $pidfile)) { print $p "$$\n"; close($p); }
        while (1) {
            accept(my $c, $s) or next;
            syswrite($c, $answer);
            # Let the client say its piece before the close, so it reads the
            # disconnect rather than a reset.
            my $rin = "";
            vec($rin, fileno($c), 1) = 1;
            for (1 .. 20) {
                last unless select(my $r = $rin, undef, undef, 0.25) > 0 && sysread($c, my $buf, 4096);
            }
            close($c);
        }' "$REFUSE_ADDR" "$REFUSE_PORT" "$text" "$REFUSE_PIDFILE" >> "$LOG" 2>&1 &
    waited=0
    while [ ! -s "$REFUSE_PIDFILE" ] && [ "$waited" -lt 10 ]; do
        sleep 1
        waited=$((waited + 1))
    done
    if [ -s "$REFUSE_PIDFILE" ]; then
        say "refuse: port $REFUSE_PORT answers every connection with a disconnect saying so (pid $(cat "$REFUSE_PIDFILE")); the build ends on ssh_handshake_attempts"
    else
        say "refuse: no answer on port $REFUSE_PORT (its error is above); the build waits out install_timeout"
        return 1
    fi
}

# Stop, loudly, without rebooting.
#
# Exiting instead would be worse than it looks. minstallconfig.xml is
# already on the media, so rc.install would launch the automated installer
# against a target volume that does not exist, fail, and take the
# `Minstaller` branch -- which is `/sbin/reboot`. That is a reboot loop
# that erases its own evidence every 40 seconds.
#
# rc.install sources this script synchronously, so sleeping here stops the
# installer from ever starting. The console text stays on the framebuffer,
# where anyone looking at the guest's display can read it. A hung VM is a bad
# outcome; a hung VM that says why is a diagnosable one.
give_up() {
    say "REFUSING TO PROCEED: $*"
    say "nothing has been erased. Halting here rather than rebooting:"
    say "a failed automated install reboots, and a reboot loop would erase"
    say "this message. Read the console, then power the VM off."
    if [ "$DRY_RUN" = 1 ]; then
        exit 3
    fi
    # A failed install all the same: the build ends on the disconnect.
    refuse_logins "the autoinstall hook gave up: $*"
    sleep "$GIVE_UP_SLEEP"
    exit 3
}

# Every whole disk, as bare identifiers. `diskutil list` prints one
# /dev/diskN line per disk, then its slices indented below.
whole_disks() {
    "$DISKUTIL" list 2>/dev/null | "$SED" -n 's|^/dev/\(disk[0-9][0-9]*\).*|\1|p'
}

# How many slices a disk has. A disk with a partition table has at least
# one; a disk of zeroes has none, and that is the whole selection rule.
slice_count() {
    "$DISKUTIL" list "/dev/$1" 2>/dev/null \
        | "$GREP" -c -E "[[:space:]]$1s[0-9]+[[:space:]]*\$"
}

# Size in bytes. `diskutil info` renders it as
# "Total Size:  64.4 GB (64424509440 Bytes) (exactly ...)"; older and
# newer wordings say "Disk Size", so match either and take the parenthesised
# byte count, which is exact.
disk_bytes() {
    "$DISKUTIL" info "/dev/$1" 2>/dev/null \
        | "$GREP" -E '(Total|Disk) Size:' \
        | "$SED" -n 's/.*(\([0-9][0-9]*\) Bytes).*/\1/p' \
        | "$HEAD" -1
}

# Prints the candidates it found, one per line, and logs its reasoning for
# every disk it rejected. The reasoning is the point: when this picks the
# wrong disk, or no disk, the log says exactly which test each disk failed.
find_candidates() {
    for disk in $(whole_disks); do
        slices=$(slice_count "$disk")
        bytes=$(disk_bytes "$disk")
        [ -n "$bytes" ] || bytes=0
        if [ "$slices" -ne 0 ]; then
            say "  $disk: $bytes bytes, $slices partitions -- skipped, already partitioned"
        elif [ "$bytes" -ne 0 ] && [ "$bytes" -lt "$MIN_BYTES" ]; then
            say "  $disk: $bytes bytes, unpartitioned -- skipped, smaller than $MIN_BYTES"
        else
            say "  $disk: $bytes bytes, unpartitioned -- CANDIDATE"
            echo "$disk"
        fi
    done
}

# Set to stream_logs to run that alone and exit, as tests/guest_assets.bats
# does; and to refuse_logins, which the follower does when the installer
# has failed. Nothing else in the installer environment sets it.
case ${GUEST_MACOSX_AUTOINSTALL_ONLY:-} in
stream_logs)
    stream_logs
    exit $?
    ;;
refuse_logins)
    refuse_logins "$@"
    exit $?
    ;;
esac

say "started; target volume will be named $VOLNAME"
# Not in a dry run: that is the disk selection alone, on a host.
[ "$DRY_RUN" = 1 ] || stream_logs &
say "PATH: $PATH"
say "disks as the installer environment sees them:"
"$DISKUTIL" list >> "$LOG" 2>&1
"$DISKUTIL" list >&2

waited=0
candidates=
count=0
while :; do
    candidates=$(find_candidates)
    count=$(echo "$candidates" | "$GREP" -c .)
    [ "$count" -ge 1 ] && break
    [ "$waited" -ge "$WAIT_SECONDS" ] && break
    say "no candidate yet; waiting (${waited}s of ${WAIT_SECONDS}s)"
    sleep 5
    waited=$((waited + 5))
done

if [ "$count" -eq 0 ]; then
    give_up "no unpartitioned disk of at least $MIN_BYTES bytes appeared in ${WAIT_SECONDS}s"
fi
if [ "$count" -gt 1 ]; then
    # awk, not tr: 10.6's install DVD has no tr (internal/payload/testdata/commands/).
    give_up "more than one disk matches ($(printf '%s\n' "$candidates" | awk 'BEGIN { ORS = " " } { print }')) -- ambiguous"
fi

target=$(echo "$candidates" | "$HEAD" -1)
say "target is /dev/$target"

if [ "$DRY_RUN" = 1 ]; then
    echo "$target"
    exit 0
fi

# GPT, one journalled HFS+ volume, filling the disk.
#
# The scheme is the step that matters and the one that is invisible when
# you get it wrong: the older Apple scheme installs perfectly well and then
# will not boot under OpenCore/UEFI, which you discover after a full
# install. A hand-driven install found it the step that matters.
say "partitioning /dev/$target: GPT, one JHFS+ volume named $VOLNAME"
if "$DISKUTIL" partitionDisk "/dev/$target" 1 GPTFormat JHFS+ "$VOLNAME" 100% \
        >> "$LOG" 2>&1; then
    say "partitionDisk succeeded"
elif "$DISKUTIL" eraseDisk JHFS+ "$VOLNAME" GPTFormat "/dev/$target" \
        >> "$LOG" 2>&1; then
    # What upstream uses. Kept as a fallback because the two spellings have
    # drifted between releases and this script may outlive 10.9.
    say "partitionDisk failed; eraseDisk succeeded"
else
    "$DISKUTIL" list >> "$LOG" 2>&1
    give_up "could not put a GPT and a JHFS+ volume on /dev/$target"
fi

waited=0
while [ ! -d "$VOLUMES/$VOLNAME" ]; do
    if [ "$waited" -ge 60 ]; then
        give_up "/dev/$target was partitioned but $VOLUMES/$VOLNAME never appeared"
    fi
    sleep 2
    waited=$((waited + 2))
done

say "$VOLUMES/$VOLNAME is mounted"
"$DISKUTIL" list >> "$LOG" 2>&1

# What the installer is about to read, as it sees it rather than as we
# hoped. rc.cdrom mounts a union RAM disk over /System/Installation, so
# "the file is on the media" and "the file is visible to the installer"
# are two different claims.
{
    echo "--- /System/Installation/Packages/Extras/"
    ls -l /System/Installation/Packages/Extras/ 2>&1
    echo "--- minstallconfig.xml as the installer will read it"
    cat /System/Installation/Packages/Extras/minstallconfig.xml 2>&1
    echo "--- OSInstall.collection as the installer will read it"
    cat /System/Installation/Packages/OSInstall.collection 2>&1
    echo "--- OSInstall.mpkg"
    ls -l /System/Installation/Packages/OSInstall.mpkg 2>&1
    echo "--- /var/log at handoff"
    ls -la /var/log 2>&1
} >> "$LOG" 2>&1

# Nothing copies logs onto the target from here on (see the top). A copier
# that ran here every 5 s, for the length of the install, was what put
# files of ours on the target in the installer's window; and on 10.6 it
# copied only the media's own empty logs, /var/log being read-only. The
# installer copies its own log onto the target when it starts installing
# ("Creating installation log on volume"), and the follower has every line
# on the serial port as it is written.
say "handing off to the OS X Installer, which rc.install runs in automated"
say "mode from Extras/minstallconfig.xml and reboots when it finishes"
mirror_log
exit 0
