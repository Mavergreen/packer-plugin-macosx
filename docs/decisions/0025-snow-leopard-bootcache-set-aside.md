# 0025 — 10.6.8's BootCache is set aside

Date: 2026-10-18
Status: proposed, on measurement

## Decision

On a 10.6 guest whose `BootCache.kext` is version 31.1 (10.6.8's),
`firstboot.sh` moves `/usr/sbin/BootCacheControl` to
`/usr/sbin/BootCacheControl-disabled` (`set_bootcache_aside`). launchctl
runs `BootCacheControl` at boot only from `/usr/sbin`
(`do_BootCache_magic`, launchd's `launchctl.c`), so the kext stays loaded
and is never started: its strategy hook is installed only by the start
(`BC_init`), so there is no hook, and no shutdown inside one.

- **Where:** 10.6.8's combo puts its own `BootCacheControl` and
  `BootCache.kext` (both dated 2011-06-07) over 10.6.0's, so with updates
  `snow_leopard_updates` does it before each restart between updates (the
  combo's boot, so the boot that installs 2013-004 already runs without
  it), and `finish_snow_leopard` again before the last restart (where no
  restart came between, the first time; otherwise "already set aside").
  With no updates, 10.6's main line does it.
- **The precondition, checked:** the kext's `CFBundleVersion` is one in
  `BOOTCACHE_DEADLOCKS` (`31.1`, the only one measured). Another version
  (10.6.0's is `31`), an unreadable one, or no `BootCacheControl` at all is
  left as it is, and the first boot's log says which: "BootCache:
  BootCache.kext is 31, not one measured to deadlock (31.1);
  BootCacheControl left in place", "BootCache: no /usr/sbin/BootCacheControl,
  so launchd starts no boot cache; nothing to set aside", or, where it acts,
  "BootCache: BootCache.kext 31.1 deadlocks HFS when it shuts its cache
  down; BootCacheControl set aside to /usr/sbin/BootCacheControl-disabled, so
  launchd starts no boot cache", and on a later call "BootCache:
  /usr/sbin/BootCacheControl already set aside (…-disabled); launchd starts
  no boot cache". A move that fails is logged ("COULD NOT") and the first
  boot goes on.
- **The guard:** `templates/snowleopard/verify.sh` prints
  `bootcache=<version>, BootCacheControl set aside|in place`, fails a build
  whose kext is one in the list (the same list as `firstboot.sh`'s, which a
  test holds equal) with `BootCacheControl` in place, and, with
  `updates = security`, fails one whose kext is not 31.1, or cannot be
  read: a version nobody measured gets looked at, not passed.
- **To restore it:** move `BootCacheControl-disabled` back to
  `/usr/sbin/BootCacheControl` and restart.

10.9 is untouched: its BootCache is another, and nothing there was measured.

## Context: F22

A 10.6.8 box, about a minute after a `vagrant up`, stopped greeting SSH
for good (final2, 1 of 4 boots, 2026-10-17): every connection reached the
guest's TCP stack and sshd never sent its banner; the vCPU idled; the
guest's `system.log` kept nothing written after the hang began, though a
grep in the guest had seen it. Here, with a guest-memory dump of each hang
read against the 10.6.8 kernel's own symbol table (no KASLR on 10.6; the
dump's kext list for the kexts), it is a deadlock in the guest's file
system.

### The mechanism (MEASURED 2026-10-18, seven hung guests)

1. launchctl runs `BootCacheControl start` early in every boot, which has
   BootCache 31.1 hook the root device's strategy routine (`BC_init`), and
   `BootCacheControl stop` 60 s after it began (an `EVFILT_TIMER` of 60 s,
   then `do_BootCache_magic(BOOTCACHE_STOP)`; `/var/db/BootCache.playlist`
   is rewritten at boot + 60 s, 13 of 13 boots that could report it). The
   kext keeps its cache in `/var/db/BootCache.data`.
2. **The trigger is the cache's own low-hit-rate shutdown, not the stop.**
   In `BC_strategy`, once its read-ahead is done and while the cache is
   active, every read checks the counters: past 500 lookups, with hits ×
   100 / lookups ≤ 49, it prints "BootCache: hit rate below threshold (%d
   hits on %d lookups)" (`BootCache+0x26c9`), terminates the read-ahead
   (`+0x1346`) and calls `BC_terminate_cache` (`+0x26f8` → `+0x1c38`),
   whose call at `+0x1d8d` reaches a function at `+0x9ab` that dumps the
   file's pages (`ubc_range_op`) and `vnode_close`s it (`+0x9f2`): all of it
   on the thread whose read it is. (Disassembled from the guest's memory,
   the calls resolved against the kernel's symbols; the same check is in
   the published BootCache-27 and -40, `BC_chit_check_start` 500,
   `BC_chit_low_level` 50.) In all four dumps kept the counters stand just
   past it, flags 0 (terminated): 3339/6679, 3309/6619, 3315/6631 and
   3292/6585, 49.99 % each; and 6 of the 7 hangs began at guest uptime
   40-44 s, as the load did, 16-20 s before the stop, which is a waiter in
   every dump. The stop's own teardown (the control's sysctl, `+0x1e94`)
   runs on `BootCacheControl`'s thread, which holds no B-tree lock; the
   60 s stop ends the window, it does not open it.
3. `vnode_close` drops the last reference: HFS's inactive call on
   `BootCache.data` needs a journal transaction, then the catalog lock.
4. The read in the hook was a read of one of HFS's B-trees, and its thread
   holds that B-tree's lock, shared, for the whole of it: the catalog's
   for a readdir (`getdirentries64` → readdir → `BTSearchRecord` →
   `buf_meta_bread` → `spec_strategy` → BootCache), the attributes file's
   for the extended attribute `stat` reads to see whether a file is
   compressed (`vn_stat` → `decmpfs` → `getxattr` → `BTSearchRecord` → ...
   → BootCache). Two shapes:
   - the reader waits for the journal, whose owner (the mutex's owner
     field) is a writer, `dd` creating or `rm` removing a file, waiting for
     that B-tree's lock exclusively, behind the reader's shared hold (5 of
     7: b001, d03, d13 on the catalog's lock, d04, d15 on the attributes
     file's);
   - the reader owns the journal itself and waits for the catalog lock,
     exclusively, behind its own shared hold: no second thread needed (2 of
     7: c002, d12).

   Which file a lock belongs to is read from its cnode's CNID: 4, the
   catalog (b001, c002, d03), 8, the attributes file (d04); for d12, d13
   (the catalog's) and d15 (the attributes file's) only by the lock's
   address, the same as in the dumps checked (those three dumps were not
   kept).
5. Then everything that needs the held lock queues behind the waiting
   exclusive request (in each dump, every lookup the name cache misses, on
   the catalog lock
   (a launchd thread, in the sandbox's `vnode_lookup`, so no `sshd` for a
   new connection; `BootCacheControl stop` itself), and every write on the
   journal (`syslogd`, `mds`)): `sync` (launchd's update thread and a
   `sync` of the test's) waits in `vnode_iterate`, and nothing written
   since the last sync reaches the disk.

The storage path is clean: QEMU's AHCI trace (`b001`) had every NCQ
command the guest issued finished (6794 of 6794), and in both hangs the
AHCI port's registers, read through QEMU's monitor, showed none
outstanding (`PxSACT` and `PxCI` 0, `PxTFD` 0x50, `PxIS` 0). The vCPU halted in
`machine_idle`.

It is Apple's bug, in `BootCache.kext` 31.1, and nothing in the stacks is
QEMU's. A real Mac running 10.6.8 would meet it whenever the read that
drags the hit rate under 50 % is a B-tree read under a concurrent writer
(REASONED: nothing in the path is virtual hardware's). A box meets it more
than most, because what is done with a box in its first minute (`vagrant
up`, provisioners, a `find` over the disk) is reading what the playlist,
recorded on the build's own last boot, never read: directory walking
(REASONED). final2's own commands, from ~53 s, hung 0 of 17: too near the
stop to drag the rate under 50 % first.

### Rates

The harness (`f22/` in the findings, `run.sh`): a fresh Vagrant machine of
the box each run, an in-guest watchdog writing to the serial port, SSH
probes every 8 s, and a hang is 5 probes in a row with no greeting; on a
hang, QEMU's state, a screenshot and a guest-memory dump.

| box | workload | runs | hangs |
|---|---|---|---|
| HEAD | final2's own commands, from guest uptime ~53 s | 17 | 0 |
| HEAD | final2's `find / -xdev` alone, spanning 50-70 s | 10 | 0 |
| fix | the same | 10 | 0 |
| HEAD | stress (two `find \| grep` readers, a `dd` writer making and removing a file, `logger`, `sync`), from 40-55 s | 25 | 7 |
| fix | the same, alternated with HEAD's last 15 | 15 | 0 |

"HEAD" is a box built from b553b13, "fix" one built from it with this
change. All 7 hangs were read from their dumps as the mechanism above
(the journal's owner a writer in 5, the reader itself in 2). Stress, 7 of 25
against 0 of 15: one-sided Fisher p = 0.026; the alternated 15 pairs alone,
5 against 0, p = 0.021. `vagrant up` took 25-27 s on either box (means
26.0 and 25.9 s, 15 each): no cost of the read-ahead measurable there, on a
host with the box image in its page cache.

## Options

- **Set `BootCacheControl` aside where the kext is 31.1 (chosen).** It
  removes the condition, not the symptom: with no start there is no hook,
  so no shutdown re-entering the file system from inside one. It costs the boot cache's read-ahead;
  see the report for boot times with and without.
- **Leave it, and wait out or retry a hung boot.** No: the guest never
  recovers (10 min, and 2 min more in each hang here), and the owner's rule
  is no retries for a cause that is known.
- **Take `/var/db/BootCache.playlist` away.** With no playlist the hook
  never runs the check (`c_extents` NULL, `+0x267a`), and the first boot
  after an install has none (no "hit rate below threshold" in either
  build's first-boot `kernel.log` here). But the start still hooks the
  device and records a new playlist, which the stop writes 60 s in, so the
  next boot has one again: it would have to be taken away at every
  shutdown. Setting the control aside is one move, once.
- **Delay the stop.** launchctl's 60 s timer is compiled in, and the stop
  is not the trigger anyway.
- **Set 10.6.0's BootCache 31 aside too.** Not done, on measurement.
  31's code is 31.1's in this path, at the same offsets: the same hit-rate
  check at `+0x26a4`-`+0x26c9`, `BC_terminate_cache` at `+0x1c38` whose
  call at `+0x1d8d` reaches a function (`+0xa17` in 31) that
  `ubc_range_op`s and `vnode_close`s `BootCache.data` (disassembled from a
  10.6.0 guest's memory, resolved against 10.6.0's `mach_kernel`). But on
  10.6.0's kernel it did not deadlock:
  - a 10.6.0 box (`updates = none`, from 36e860e) as built: its playlist,
    recorded on the build's one boot, misses at once, and the cache shuts
    itself down in the boot's first second or two ("hit rate below
    threshold (~400 hits on ~800 lookups)" in `kernel.log`, 8 of 8 boots,
    one of them read from a dump's counters): 0 hangs in 8;
  - the same box booted once more, so its playlist is a normal boot's, as
    HEAD's 10.6.8 box's is: under the same stress from 40 s the cache shut
    itself down at 43-45 s in 25 of 25 boots (2873-2909 hits on 5747-5819
    lookups, each logged), the window in which HEAD's 10.6.8 box hung 7 of
    25: 0 hangs in 25 (one-sided Fisher p = 0.005 against 7 of 25).

  What differs is the kernel's side (10.6.0's xnu against 10.6.8's), not
  measured further. The build's own first boot, 10.6.0 installing the
  combo, has no playlist yet: no "hit rate below threshold" in the first
  boot's `kernel.log` of either build read here (one `updates = security`,
  one `updates = none`), so no check runs there. The boot after the combo
  runs 10.6.8's kernel and 31.1, and is where this change now acts.

## On measurement

Proposed on the counts above, the after rows from a box built from this
change (its build's `verify.sh`: `bootcache=31.1, BootCacheControl set
aside`; its first boot's log has the "set aside" line; in each of its 15
stress boots `/usr/sbin/BootCacheControl` was absent and the kext loaded;
that it never started is REASONED: `BC_init`, the only place the hook goes
in, runs only from the start, or at mountroot from a playlist the booter
preloaded, and no dump had one). The next real-guest run owes, on main: a 10.6.8 build's first-boot log with the "set aside"
line, `verify.sh`'s `bootcache=31.1, BootCacheControl set aside`, and a box
whose `ps` shows no `BootCacheControl` 60 s into a boot; and, on a
HEAD-style box under load, `kernel.log`'s "BootCache: hit rate below
threshold", which 31.1 prints before every in-hook shutdown.
