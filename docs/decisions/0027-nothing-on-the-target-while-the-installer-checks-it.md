# 0027 — Nothing of ours on the target while the installer checks it

Date: 2026-10-19
Status: proposed, on measurement

## Decision

Once the autoinstall hook (`assets/guest/autoinstall/autoinstall.sh`,
the media's `/etc/rc.cdrom.local`) hands off to the OS X Installer,
nothing of ours writes to the target volume.

- **The copier loop is gone.** It ran for the length of every install:
  every 5 s, `cp /var/log/*.log` into `/Volumes/<name>/.guest-macosx-logs/`,
  `cp` of the hook's own log to `/Volumes/<name>/.guest-macosx-autoinstall.log`,
  `sync`. `mirror_log` still copies the hook's own log onto the target, but
  only while the hook runs, before rc.install starts the installer; it is
  off (`MIRROR=0`) in the one thing the hook leaves running.
- **The install's logs go to the serial port instead, live.** The hook
  starts a follower (Perl, as rc.install itself uses; the installer
  environment has no `tail`, and its perl no modules) that prints, tagged,
  the hook's own log (`hook:`) and syslog's messages (`syslog:`): the
  installer's own (`install.*`), every daemon's at `notice` and above, and
  the kernel's. syslog reaches it on 127.0.0.1:32377: the 10.6 media's
  `syslog.conf` gets one line more at assembly
  (`assets/privops/disc/assemble-disc.sh`), `*.notice;install.*;kern.*
  @127.0.0.1:32377`, as the disc's own line forwards `install.*` to
  instlogd's 32376. The follower writes the port at 115200 baud, holding it
  open so the setting stays.
- **The template keeps the port as a file beside the output directory:**
  `["-serial", "file:${local.install_log}"]`, `install_log =
  "${local.output_directory}-install.log"`, so `output-snowleopard-install.log`.
  Packer deletes the output directory, the disk in it, when a build fails;
  this file it leaves, until the next build's QEMU starts it afresh (its
  `file:` chardev truncates).
- **Preconditions, logged:** the serial port is waited for, up to 120 s
  (kextd matches the serial driver after boot), and the hook's log says
  "serial: following ... to /dev/cu.serial1 (there after 6s)" or "serial: no
  port (...) after 120s; the logs are not followed". The follower's first
  line says "follower: syslog on 127.0.0.1:32377" or "follower: no syslog
  on 127.0.0.1:32377: <why>" (10.9's media has no forward; nothing comes).

- **The guard:** `tests/guest_assets.bats` runs the whole hook, not as a dry
  run, against a stub `diskutil` whose `partitionDisk` makes the volume
  under a fake `/Volumes` (`GUEST_MACOSX_VOLUMES`), with the serial port
  appearing only after the hook has exited; it fails on anything under the
  target that changed or appeared after the handoff, on anything left
  running but the follower and its perl, and on either of those holding a
  file or its cwd there. The copier respelled, one delayed `cp`, the
  follower's mirror turned back on, and perl appending to a target path
  each fail it (the F21 review's mutations m1b, m2, m2b, m2d).

## Context: F21

A 10.6 build at ace862e, on a host at load 17-30 (24-28 busy loops on 12
CPUs), ended at the installer's "Install Failed" sheet: "The Installer
encountered an error that caused the installation to fail. Contact the
software manufacturer for assistance." About 2 minutes in, 4 MB on the
target: before the installer had written anything of its own. The next
build, same plugin, template and media, succeeded (1 of 3 failed). The
installer's own log was never read, and could not have been: see
"Observability" below.

## The mechanism (MEASURED 2026-10-19)

The installer's first step on the target is a volume consistency check
(OSInstall's `OSVolumeFsckQueueElement`): "Performing volume consistency
check", `API_beginFilesystemRepairForDisk` (DiskManagement's
`DMRepairDisk`), disk arbitration waiting about 10 s on the installer's own
unmount-approval callbacks ("Mac OS X Installer [263]:... not responding"),
the target unmounted, `fsck_hfs -fy -f -x /dev/disk0s2`, the target mounted
again, "Volume passed consistency checks", then "Creating mutable product
on /Volumes/<name>/Mac OS X Install Data". A write of ours to the target in
that window fails the install, two ways, both with F21's sheet and F21's
4 MB:

- **A: a file of ours open on the target.** The unmount fails, the check
  fails, the install fails:

      OSInstaller: Performing volume consistency check on 'Snow Leopard'.
      Mac OS X Installer: API_beginFilesystemRepairForDisk
      diskarbitrationd: Mac OS X Installer [275]:19739 not responding.
      OSInstaller: Checking Volume
      OSInstaller: Verify/Repair failed with error -10000 (Could not unmount disk)
      OSInstaller: Install failed: The Installer encountered an error that caused the installation to fail. ...
      diskmanagementd: APICALLBACK_dmAsyncFinishedForDisk_FSCK_result dmErr=-10000 detailErr=0

  A probe holding one file open on the target from the handoff: 8 of 8
  failed, idle (`obs03`, `obs04`, `v01`-`v04`: a Perl debug file kept open
  there; `h01`, `h02`: a shell holding `.gm-holder`; `h02`'s log above, the
  others have only the sheet: their probes had no way out for syslog yet).
  Nothing held open, the same probes otherwise: 2 of 2 passed (`obs01`,
  `obs02`).
- **C: a file of ours written into `/Volumes/<name>` once it is
  unmounted.** Disk arbitration cannot remove the mount point, mounts the
  target at `/Volumes/<name> 1`, and the installer carries on at
  `/Volumes/<name>`, which is now a folder on the 512 KB RAM disk rc.cdrom
  put over `/Volumes`: PackageKit extracts into
  `/Volumes/.OSInstallSandbox-tmp`, runs out of space (errno 28), and the
  install fails. `lh02`'s once-a-second record of the mount table and of
  `/Volumes`, with the installer's lines between:

      syslog: OSInstaller: Checking Volume
      hook: hb 30 00:15:23  | .,..,.Trashes,Mac OS X Install DVD,Snow Leopard,
      syslog: OSInstaller: Volume repair complete.
      hook: hb 32 00:15:25  | .,..,.Trashes,Mac OS X Install DVD,Snow Leopard,Snow Leopard 1,
      syslog: OSInstaller: Volume disk0s2 mounted at /Volumes/Snow Leopard is ready
      syslog: OSInstaller: PackageKit: Extracting /System/Installation/Packages/BaseSystem.pkg (destination=/Volumes/.OSInstallSandbox-tmp/Root, uid=0)
      hook: hb 33 00:15:26 /dev/disk0s2 on /Volumes/Snow Leopard 1 (hfs, local, journaled) | ...,.OSInstallSandbox-tmp,...,Snow Leopard,Snow Leopard 1,
      syslog: OSInstaller: Install failed: The Installer encountered an error ...

  HEAD's copier loop exactly as it was, but with no `sleep` between its
  passes: 3 of 10 failed this way, idle (`l001`, `l005`, `lh02`), none by A.
  In a run with no copier the mount point is gone while `fsck_hfs` runs
  and comes back with the mount (`fah01`'s record, a run with a RAM disk
  over `/var/log`; see "Observability").

HEAD's loop, with its `sleep 5`, has a file of ours on the target for the
length of a `cp` every 5 s (REASONED: milliseconds, idle), and each way
needs that to meet a moment: the unmount (A), or the gap between the
unmount and the mount point's removal (C). Load stretches both. Measured,
HEAD's loop as it was: 0 of 8 failed under 26 busy loops (`l502`-`l509`,
HEAD's loop plus the follower), and HEAD's own media 0 of 21 (1 idle; 20
loaded: QEMU on one core shared with 1 or 3 busy loops, 5; 26 busy loops,
7, and 4 more through `packer build`; 40 busy loops, 2; 26 and a 2 GiB
`dd conv=fsync` repeated beside the target, 2). F21's own install is not measured: its log never existed.
That it was A or C is REASONED: the same sheet, at the same step, 4 MB on
the target, and the copier loop the only writer of ours to the target there.
Its 1 in 3 is one failure, from a host under heavier and less even load
than any here.

What the loop bought: nothing. On 10.6 `/var/log` is on the read-only root
(10.6's rc.cdrom puts RAM disks over `/Volumes`, `/var/tmp`, `/var/run`,
`/var/db`, `/Library/Preferences` and `/Library/ColorSync/Profiles/Displays`,
not `/var/log`), so `/var/log/*.log` was the
media's own four empty logs in every run; the hook's log stops changing at
the handoff; and the installer copies its own log onto the target itself
("Creating installation log on volume") once it is past the check.

## Observability

10.6's installer environment keeps no text log at all: syslogd's
`install.*`, `system.log` and `kernel.log` files are on the read-only
root, so it writes none (no `install.log` in a 10.6 run here without the RAM disk below), and the
installer's lines live in instlogd's memory until the installer copies
them onto the target, which a failed install never does. Hence the
forward to the follower.

Measured and rejected: making `/var/log` writable with a union RAM disk
over it (hdik, newfs_hfs, mount, as rc.cdrom does its own). 6 of 20
installs failed with it, idle: after the check's `fsck_hfs`,
"diskarbitrationd: unable to probe /dev/disk0s2 (status code 0xFFFFFFFC)"
(also 0x47, 0xFFFFFFFA), the target not mounted again, "Could not create
mutable product", the same sheet (with the logs followed and syslogd told
to reopen its files, 2 of 3 and 1 of 5; not followed, 2 of 6; not even
told, 1 of 6). Without the RAM disk, 0 of 11 (the follower alone, 0 of 5;
syslogd told to reopen its files, 0 of 6). Disk arbitration's probe of the
target after the check's `fsck_hfs` fails when a disk has been added during
the install (the mechanism inside disk arbitration is not traced); so
nothing of ours adds one.

With the decision: 0 of 9 failed (`fu01` idle, `fu02`-`fu09` under 26 busy
loops), each with about 850 syslog lines on the serial file and the
installer past its check ("Volume passed consistency checks", BaseSystem
extracting into `/Volumes/Snow Leopard/.OSInstallSandbox-tmp`). And full
builds: see "Builds" below.

## Builds

`updates = security`, from cached installer inputs, each verify ok (10.6.8,
16 update receipts):

| build | code | host | to SSH | installer's own total | first boot | whole build |
|---|---|---|---|---|---|---|
| `idle1` | main 6b09c98 | idle | 15 min 21 s | 565 s | 4 min 41 s | 19 min 19 s |
| `fixidle1` | this | idle | 15 min 29 s | 568 s | 4 min 50 s | 19 min 7 s |
| `fixload1` | this | 26 busy loops, load median 28.8 | 92 min 37 s | 3361 s | 32 min 19 s | 2 h 17 min 47 s |

`fixload1` ran with `install_timeout = 3h` to measure: under the default
`1h` it would have timed out 2 minutes into its first boot, with the guest
doing nothing but installing, slowly. Its serial file has 1,617 syslog lines,
`fixidle1`'s 1,591. (Both builds' plugin was this tree less one comment
line in `autoinstall.sh`, added after.)

## Consequences

- A failed 10.6 install explains itself in `output-snowleopard-install.log`
  (as `h02`'s and `lh02`'s did, above). The build itself, which used to
  wait out `install_timeout` for SSH (the installer's sheet waits for a
  click, and Packer's qemu builder does not watch QEMU), now ends in a
  minute or two, saying why: `docs/decisions/0028`.
- A box's target keeps the hook's own log, `.guest-macosx-autoinstall.log`,
  as of the handoff, and no longer a `.guest-macosx-logs` folder (it held
  only the media's empty logs).
- 10.9 runs the same hook, and since the F21 follow-up has the same log:
  the Mavericks template keeps the port as `output-mavericks-install.log`,
  and 10.9's media gets the same forward (`assets/privops/assemble.sh`;
  10.9's `/var/log` is a RAM disk, but its `asl.conf` writes no file of the
  installer's lines). Its installer makes the same check ("Performing
  volume consistency check on 'Mavericks'" ... "Volume repair complete"),
  but a file held open on the target does not fail it: `mavfail-holder`
  (MEASURED once, 2026-10-19) logged "diskmanagementd: unmount blocked by
  dissenter PID=522 (/bin/sh)", then "Volume passed consistency checks",
  and extracted onto the target. Whether HEAD's copier could fail 10.9 the
  second way (C) is not measured; the copier is gone there too.
- The forward is one more line in an Apple file on the media, made at
  assembly from the disc's own copy: no Apple bytes are carried here.

## What would change it

- A failed install whose serial file lacks the installer's lines: the
  forward or the follower is broken, and the build's evidence with it.
- Something of ours that must write onto the target during the install:
  it waits until the installer is past its check, and is measured the way
  A and C were.
- A disk of ours added during the install, as the rejected RAM disk: it
  failed the install here, and would need measuring the same way.
