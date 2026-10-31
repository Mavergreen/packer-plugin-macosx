# 0031 — aslmanager is relaunched at once, so 10.9's syslogd writes from the start of a boot

Date: 2026-10-25
Status: proposed, on measurement

## Decision

On 10.9, `firstboot.sh` adds `ThrottleInterval` 0 to aslmanager's own
launchd job, `/System/Library/LaunchDaemons/com.apple.aslmanager.plist`
(`unthrottle_aslmanager`, with `PlistBuddy`), after the updates and from
the next boot on. Nothing else in the job changes.

- **The precondition, checked once:** the job is aslmanager's on-demand
  Mach service (`MachServices:com.apple.aslmanager`), sets neither
  `KeepAlive` nor `RunAtLoad`, and sets no `ThrottleInterval` of its own.
  A job that sets one keeps it ("the job already sets ThrottleInterval
  (N); left as it is"); one that sets `KeepAlive` or `RunAtLoad` is left
  throttled ("sets KeepAlive, so launchd can relaunch it without a waiting
  ask; left throttled, as it is"); a job that is not that service, or no
  plist at all, is not touched; a failed edit is
  logged ("COULD NOT add ThrottleInterval …") and the first boot goes on.
  Each outcome is a line in the first boot's log.
- **Where:** 10.9's main line, after `install_updates`. The plist is
  `com.apple.pkg.Essentials`' alone (`pkgutil --file-info`, MEASURED
  2026-10-25), so Security Update 2016-004 does not replace it; after the
  updates all the same, so nothing they install can undo it. 10.6 is not
  touched, twice over: the call is 10.9's line alone, and 10.6's syslogd
  (syslog-100.2) has no `asl_trigger_aslmanager` at all; its
  `com.apple.aslmanager.plist` is a `WatchPaths` job with no
  `MachServices`, which the precondition would leave alone anyway (READ).
- **Why 0, and what it costs:** launchd throttles a job's relaunch only
  when it ran for less than its `min_run_time`, which `ThrottleInterval`
  sets with no lower clamp, and reads nowhere else (launchd-842's
  `core.c`, `job_start`, READ); 0 means never throttle, and nothing more.
  An on-demand job is relaunched after it exits only while an ask is still
  waiting on its port (`job_keepalive`, READ), so a working aslmanager,
  which takes the ask, answers and exits, is relaunched once per ask and
  cannot loop. A broken one that exits, crashes or fails to start before it
  takes the waiting ask would be relaunched back to back, where the
  default spaces those relaunches 10 s apart; syslogd's ask would wait
  either way. That needs a broken aslmanager, and its crash reports would
  show it. A job that sets `KeepAlive` or `RunAtLoad` could be relaunched
  with no ask at all, so the precondition leaves it throttled. Its clients
  are syslogd, `aslmanager`'s own command line, and at least one other not
  identified: a stuck boot's log shows launchd re-evaluating the throttled
  relaunch at about 5 and 6 s, while syslogd's queue was already waiting.
- **The guard:** `templates/mavericks/verify.sh` prints
  `aslmanager-throttle=<value>|launchd-default|left:<key>|unreadable|no-job`
  and `aslmanager-plist=<mode> <owner>:<group>`, and fails a build whose
  on-demand aslmanager job does not set `ThrottleInterval` 0, or whose job
  `plutil` cannot read (Apple's is a binary plist, read only through
  `plutil -convert xml1`; a job it cannot read cannot be told apart). A job
  the first boot left throttled on purpose (`KeepAlive`, `RunAtLoad`, or a
  `ThrottleInterval` of its own) fails too, since the guest keeps the
  window, and the message says which case it is. Its verdict line says
  "aslmanager unthrottled" only for the service at 0, and otherwise
  "aslmanager: no job" or "aslmanager: not the on-demand service".
- **To remove it:** delete the key (`sudo /usr/libexec/PlistBuddy -c
  'Delete :ThrottleInterval' /System/Library/LaunchDaemons/com.apple.aslmanager.plist`);
  the next boot is as before.

## Context: F9

A restart of a 10.9 guest about 7 s into a warm boot, with ADR 0019's
helper, lost that boot's whole `/var/log/system.log` in 16 of 100 restarts:
no `BOOT_TIME` line, no `SHUTDOWN_TIME`, nothing between the previous
boot's shutdown and the next boot (2026-10-14/15, ADR 0021). ADR 0021's
20 s wait made it 0 of 100, but by removing the condition; the cause was
not known.

### What is lost, and what is not (MEASURED 2026-10-25)

Restarts fired from inside the guest at a set uptime (a RunAtLoad
LaunchDaemon that runs `/sbin/shutdown -r now` at 7-12 s; a token in NVRAM
keeps it to one boot), on a box built from main `ccebab8`:

- Lost together, 11 of 11 times: the boot's lines in `system.log` and its
  messages in the ASL store (`syslog -k Time …` finds none of them, its
  `BOOT_TIME` record included). REASONED from the mechanism below: every
  other `asl.conf` output of syslogd's too (the `/etc/asl` modules' files,
  `install.log`, `authd.log` and the rest), written by the same queue.
- Not lost: files other processes wrote in the same seconds. The probe's
  own two files in `/var/db`, written at 6.6 s and just before the
  shutdown, were there after every lost boot (11 of 11), contents intact.
  No journal replay at the next boot (no `jnl:` line), and `fsck_hfs`'s
  boot-time checks found nothing.
- Read offline after a halt at 8 s in a boot syslogd had not written (1
  of 13 halts): the volume cleanly unmounted, its journal empty,
  `fsck.hfsplus -n` clean, and `system.log` on the disk the size the guest
  saw at the halt, with no line of that boot.
- The disk was never the place it was lost. Just before each shutdown the
  probe read the live `system.log` (what the kernel's cache holds): in
  every lost boot it checked, 10 of 10, the file held **no line of that
  boot** and was the size the next boot found it; in every kept boot it
  checked, 80 of 80 stock and 70 of 70 with this decision, the boot's
  `BOOT_TIME` and 150-200 lines after it were there already. syslogd had
  written nothing.

### The mechanism (MEASURED 2026-10-25; READ in syslog-217.1.4 and launchd-842.92.1)

1. READ: as it starts, syslogd asks aslmanager to check the logs, twice.
   Each ask is `asl_trigger_aslmanager()`, an XPC message sent with
   `xpc_connection_send_message_with_reply_sync()`, which waits for the
   answer.
   - The first is on syslogd's **main thread**, as it sets up its outputs,
     before its inputs and Mach server start: `main` → `init_modules` →
     `asl_action_init` → `_asl_action_configure` →
     `_act_file_checkpoint_all` (`syslogd.c:651`, `asl_action.c:1699,
     1669, 1021`).
   - The second is on its serial **"ASL Action Queue"**, the one queue that
     writes every message to every output, as the first message reaches
     the ASL store: `asl_out_message` → `_send_to_asl_store` →
     `db_save_message` → `db_asl_open` (`asl_action.c:1284`,
     `dbserver.c:513, 157`).
2. READ: aslmanager is launched on demand. On a connection's first
   message it replies, runs its whole maintenance pass (`cli_main`) for a
   root client, and then exits, whatever the client does
   (`aslmanager.c`, `accept_connection`). How long that pass takes depends
   on `/var/log/asl` and the log files, which grow over a run: REASONED, a
   plausible source of the round-to-round variation below, not measured.
3. MEASURED: when the second ask reaches launchd after that aslmanager has
   finished and exited, but within 10 s of its start, launchd throttles the
   relaunch for the rest of the 10 s: "(com.apple.aslmanager):
   Throttling respawn: Will start in 10 seconds", logged at about 1 s.
   syslogd's ASL Action Queue waits in `asl_trigger_aslmanager` →
   `xpc_connection_send_message_with_reply_sync` →
   `_dispatch_semaphore_wait_slow` (`sample syslogd` at 8-10 s, 4 of 4
   stuck boots), and every message of the boot queues in memory behind it.
4. MEASURED: at about 12 s (12.0-12.2 s, 4 of 4) aslmanager starts, replies, and
   syslogd writes the whole backlog at once. A boot that is not shut down
   loses nothing; its first 12 s are written late.
5. READ, and MEASURED in its effect: syslogd has no SIGTERM handler (`syslogd.c` installs one only for
   SIGHUP). A shutdown inside those 10 s SIGTERMs it, and the backlog is
   gone: the boot's whole log.

Whether the second ask comes before or after the first aslmanager's exit
is a race. The fix covers the first ask too: a syslogd that launchd
relaunched within 10 s of an aslmanager run would block its own start the
same way (REASONED; not seen). syslogd's other synchronous asks on the
writing queue (a rotation, the midnight checkpoint, a SIGHUP reset) go to
the same job, and are covered the same way (READ). The boots found stuck at 8 s all had the throttle line (4 of
4); the kept boots of the stock rounds that recorded it had none (37 of
37). How often the race goes the wrong way moved from round to round (0 to
5 lost in 10), for reasons not chased: the fix takes the throttle away,
whatever sets the race.

10.10's syslogd (syslog-267) asks from its main queue instead, at most once
per interval (`trigger_aslmanager`, `ASLMANAGER_DELAY`): Apple's own fix,
which a 10.9 guest cannot have. `ThrottleInterval` is the job's own
launchd key, documented in `launchd.plist(5)`.

### Rates

All on this host (QEMU 8.2.2/KVM, one VM at a time; 1-minute load 0.6-3.2,
mostly another user's work), 10.9.5 guests restarted by the in-guest
trigger at 7, 8, 9, 10 and 12 s into a warm boot (cycled), each restart's
previous boot taken down late through `guest-macosx-shutdown`. "Lost" is a
boot whose `BOOT_TIME` is in neither `system.log` (or its rotated copy) nor
the ASL store (one fix-arm boot whose `system.log` syslogd rotated at
local midnight, its `BOOT_TIME` in the ASL store, is kept).

| | box | restarts | lost | written by the shutdown (live check) |
|---|---|---|---|---|
| stock | main `ccebab8`'s box | 30 (3 rounds) | **7** | 14 of 14 kept, 0 of 6 lost (the first round's 10 ran before the check existed) |
| stock / fix, rounds alternated | the same box; "fix" adds `ThrottleInterval` 0 by hand after the cold boot | 40 / 40 | **3 / 0** | 37 of 40 / 40 of 40 |
| stock / fix, rounds alternated | main's box / a box built from this change | 30 / 30 | **1 / 0** | 29 of 30 / 30 of 30 |
| **all stock / all fix** | | **100 / 70** | **11 / 0** | kept 80 of 80, lost 0 of 10 / 70 of 70 |

The case rests on the mechanism, and on the induced result, more than on
the restart counts:

- **Induced, in a running guest:** two `asl_trigger_aslmanager()` calls
  1 s apart, as syslogd makes them (`ctypes`, as root): with launchd's
  default the second waited **9.0 s, 5 of 5**; with `ThrottleInterval` 0,
  **0.002-0.011 s, 8 of 8** (5 at 1 s apart, 3 at 0.1 s). p = 0.0008.
- **The restarts, alternated rounds** (the fair comparison: the rate moved
  from round to round, 0 to 5 of 10): **4 of 70 against 0 of 70**,
  Fisher one-sided p = 0.06. Pooled with the first run's 30 stock
  restarts, which had no fix arm: 11 of 100 against 0 of 70, p = 0.002.
- **Not shut down** (the probe at 8 s, then left running): syslogd had not
  written the boot in 4 of 27 stock boots, and wrote it at 12.0-12.2 s.
- The box built from this change: its first boot's log says
  "aslmanager: launchd throttled its relaunch 10 s, and syslogd's first
  writes with it; ThrottleInterval 0 added to …" (that build's wording;
  it now reads "the job had launchd's default 10 s throttle, which can hold
  syslogd's first writes; ThrottleInterval 0 added to …"), its plist carries
  `<integer>0</integer>`, and the build's `verify.sh` printed
  `aslmanager-throttle=0`.

Earlier, over SSH as soon as it answered (about 9 s into these boots, the
helper's old pace or a bare `shutdown`), 0 of 19 were lost: SSH answers
too late in this box's boots to land in the window often.

## What it fixes for which shutdown

Every shutdown from about 1 s to about 12 s into a boot could lose the
boot's log, whoever asked for it: 11 of 100 here, 16 of 100 in F7, at 7 to
12 s (MEASURED); that the window opens at about 1 s is REASONED from the
throttle line's time. With this decision there is no such window.
Before it, ADR 0021's wait already kept every shutdown that goes through
`guest-macosx-shutdown` out of it: the wait ends only when no job launchd
started is under 20 s old, and on every boot the boot's own jobs are, until
at least 20 s in. That covers the build's `shutdown_command`, the box's
`vagrant halt`, the layer template's restart and the boot gate. It never
covered a shutdown that does not go through the helper (a user's own
`sudo reboot` or `shutdown`, an update's restart), nor a helper that cannot
tell the jobs' ages (it then waits for nothing, and says so). 10.9's first
boot has no restart of its own.

## Alternatives

- **The helper waits for its own `logger` line to reach `system.log`
  before it shuts down.** It would cover the helper's shutdowns, which ADR
  0021's wait already covers, and nothing else; the cause would stay, and
  every boot would still write its first 12 s late.
- **A KeepAlive or RunAtLoad aslmanager**, so it is always there to answer:
  it would idle in `dispatch_main()` until the first request, but it exits
  after serving each one by design, so the next ask after a served one
  would meet the same 10 s throttle. It does not help.
- **A smaller ThrottleInterval (1 s):** the same, with a 1 s window left
  at about 1 s into a boot. Nothing needs the throttle.
- **Replacing syslogd** with a later one: not Apple's 10.9, and not ours
  to ship.
