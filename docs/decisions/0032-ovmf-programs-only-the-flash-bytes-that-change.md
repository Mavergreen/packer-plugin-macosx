# 0032 — OVMF programs only the flash bytes that change, so a variable reclaim no longer holds a restart for a minute

Date: 2026-10-25
Status: accepted (the owner, 2026-10-26)

## Decision

The OVMF build carries one more patch, after 0005:
`assets/firmware/patches/0006-ovmf-program-only-the-flash-bytes-that-change.patch`.
It is ours; upstream has nothing like it (READ: tianocore/edk2's history
of the file to 2026-10-25). The build skips it when `QemuFlash.c`
already carries its marker comment,
`packer-plugin-macosx: assets/firmware/patches/0006`, and refuses a tree
where it did not take ("still programs every byte of a flash write, the
ones that already hold their value too"). `internal/firmware`'s tests pin
its diff by sha256.

It changes two functions of `OvmfPkg/QemuFlashFvbServicesRuntimeDxe/QemuFlash.c`,
the driver that programs QEMU's flash:

- `QemuFlashWrite` compares the buffer with what the flash holds and
  programs only the runs of bytes that differ, each run through up to 16
  unchanged bytes, with the same two writes per byte as before
  (`WRITE_BYTE_CMD`, then the byte), and returns the device to read-array
  mode after each run, as it did after each call.
- `QemuFlashEraseBlock` now ends with `READ_ARRAY_CMD` too. The comparison
  reads the array, and an erase leaves the device in status mode, where a
  read returns the status register (READ: QEMU 8.2.2 `pflash_cfi01.c`,
  `pflash_read`). With this, every function of the driver that writes the
  device leaves it in read-array mode, as `QemuFlashDetected` does on the
  path where it reports a flash (its ROM and RAM paths report none, and
  the driver is then never used).
- A byte left alone already holds the value programming it would store,
  so the flash ends exactly as it did. MEASURED: after a forced reclaim,
  a boot and a shutdown, the whole varstore file was byte-identical with
  and without the patch, 5 of 5 pairs on 10.9 and 3 of 3 on 10.6.8; after
  an ordinary cold boot, 3 of 3.

**A fix, not a gated workaround**, so it has no precondition to detect
and nothing to log about one:

- Skipping is right everywhere. Not programming a byte that already
  holds its value is a no-op on any CFI flash, where a program can only
  clear bits, and on QEMU, whose `pflash_data_write` stores the value
  outright (READ: `pflash_cfi01.c` v8.2.2).
- It is inert where there is nothing to skip. A cold boot on the
  pristine store programmed 6,636 bytes on main and 6,635 with 0006
  (MEASURED, 3 of 3 each; the review's firmware-only boots: 6,716 and
  6,715).
- Bytes that differ are programmed exactly as before: the same order,
  the same two writes each.

**The invariant it relies on:** the device is in read-array mode at every
entry to `QemuFlashWrite`. In the pinned file it holds after QEMU's reset,
after `QemuFlashDetected`'s flash path, after every `QemuFlashWrite` and,
with 0006, after every `QemuFlashEraseBlock`; `QemuFlashRead` writes
nothing. A later audk could add a writer that ends in another mode, and
0006 would still apply: the comparison would then read QEMU's status
register (0x80) until the next run, and skip a leading 0x80 it should
program. Nothing at build time checks the source for this.
`INGREDIENTS.md` says to re-read `QemuFlash.c` on every audk bump for
exactly this, and the gate's `varstore` step (below) catches its effect.

The patch's comments were reworded after the measurements below (one
comment line now gives 41-57 s); its code is the code measured, and the
test pins the reworded diff.

Nothing else changes: not the FVB protocol, the fault-tolerant write, the
variable driver, nor how often a reclaim happens. An ordinary boot is as
fast as before (cold boots to OVMF's "BdsDxe: starting": 2.16-2.19 s
on main, 2.19-2.23 s with 0006, 3 of 3 each; the warm-restart medians
below).

## The guard: the gate's `varstore` step (the owner, 2026-10-26)

The gate's restarts never reach the 61st, and the firmware tests pin
only the patch's text, so `bin/gate.sh` runs `bin/gate-varstore.sh` on
the layer box's firmware after the builds and before the guest's first
boot. What it measures:

- It copies the box's own `OVMF_VARS.fd` with all but 2,000 bytes of the
  store taken by deleted records (the live variables kept), and boots
  QEMU on the box's `OVMF_CODE.fd` and that copy alone: no guest disk, no
  OpenCore, no Apple bytes. The firmware's first write that does not fit
  reclaims, and the step waits for OVMF's first "BdsDxe:" serial line,
  at most 120 s.
- **Method `trace`:** QEMU's `pflash_data_write` trace event, one line
  per programmed byte (the `log` trace backend, QEMU's default). It
  fails when more than 100,000 bytes were programmed before that line,
  or when the line never came. MEASURED on this project's KVM host,
  2026-10-26: 0006's firmware 8,355 bytes and the line after 1 s, the
  step 2-3 s, 2 of 2; main's 799,963 bytes, the step 38 s, 2 of 2 (the
  review's sketch of it: the line after 32-37 s).
- **Method `timing`**, when this QEMU does not list the event, or lists
  it and records none (another trace backend): the same boot on the
  box's own store and on a freshly filled one, each twice, alternately
  (the owner, R42, 2026-10-26); it fails when the full store's faster
  boot reaches "BdsDxe:" more than 10 s after the pristine store's
  faster one. MEASURED with a QEMU that hid the event, 2026-10-26:
  - idle: 0006 1 s against 1 s (3 of 3); main 32 s against 1 s, a fail;
  - 12 busy loops (load 8-13): 0006 9/4, 8/3, 6/5 and 8/4 s, so at most
    5 s of the 10 used; main never reached the line within 120 s, a
    fail;
  - 16 busy loops (load 16-18): 0006 8/5, 6/5, 9/4 and 6/4 s, at most
    5 s used.
  Comparing one boot of each, as the first version did, would have used
  up to 8 s in the same runs (11 against 3 s), and the review saw 12
  against 4 s once. The trace method at load 17 counted 8,355 bytes, as
  idle.
- **The timing method's limit:** it can only see a reclaim that costs
  the host more than 10 s. A host that programs flash faster than about
  12.5 µs a byte (800,000 bytes in under 10 s) would pass main by
  timing; this host takes 47-69 µs, and HVF and nvmm hosts are not
  measured. The trace method has no such limit.
- It says which method it used, in its log; a fail's note names the
  count or the times. It needs only `python3` and QEMU, which the gate
  already requires: POSIX tools, no `/proc`, no GNU-only flags. QMP's
  `query-blockstats` is no counter here: pflash keeps no block
  accounting (the review measured 0 writes on both firmwares).

## Context: F4

Some warm restarts of a 10.9 guest took 2-4 times as long as the rest,
all of the extra time before the kernel started: 66, 72, 73 and 81 s
against about 21 s in F1's runs of 2026-10-13 and 14, with the IGVM page
zero, so not `docs/decisions/0018`'s hang. Both of F1's runs had one at
restart 61 and one at restart 123.

### The mechanism (MEASURED 2026-10-25; READ in audk `0672a009` and QEMU 8.2.2)

1. MEASURED: OVMF's boot manager rewrites the console variables on every
   boot. `PlatformInitializeConsole` adds its platform consoles (the
   debug-agent UART, a second serial port, the QEMU ramfb) to `ConOut`,
   `ConIn` and `ErrOut`, and the boot manager deletes each one that does
   not connect (READ: `OvmfPkg/Library/PlatformBootManagerLib`,
   `EfiBootManagerConnectConsoleVariable`). Each change appends a record
   and marks the old one deleted: 14 records and two `MTC` writes, 3,592
   bytes programmed before OpenCore starts, on every warm boot but the
   reclaims (10.9: 148 of 150, the other two the reclaims; 10.6.8: the
   same), and 75 more before sshd answers.
   Nothing more is written while the guest runs (10.9: 0 bytes between
   sshd's banner and the next restart, 149 of 149 boots). 10.9's own
   NVRAM writes reach the store at shutdown, through runtime
   `SetVariable`; the review of 0006 measured those on 10.9 only (three
   `nvram` sets and one delete, 107 bytes programmed, the stores after
   byte-identical with and without 0006, 2 of 2), which also shows the
   comparison's reads work through the OS's virtual mapping. 10.6 was
   not measured at runtime.
2. The store is 256 KiB (`OVMF_VARS.fd`, the 4 MiB flash layout). The
   box and the boot gate attach it `snapshot=on`, so it lives as long as
   the QEMU process: each warm restart leaves about 3.6 KiB more of
   deleted records, and a cold start begins from the pristine store.
3. MEASURED, and READ in `Variable.c`: at the 61st warm restart of one
   QEMU process the variable driver reclaims, at ReadyToBoot, between
   OVMF's console and its "BdsDxe: loading" line. `ReclaimForOS` reclaims
   when fewer than `PcdMaxAuthVariableSize` (0x8400, 33 KiB) of the
   store are left, counting deleted records as used (the driver adds up
   every record it finds at start). A cold boot leaves 6,972 bytes of
   records, each warm boot 3,644 more, and 262,044 − 6,972 − 61 × 3,644
   is the first to fall below 33,792. A reclaim leaves the 2,560 bytes of
   live variables, so the next comes 62 restarts later. Restarts 61 and
   123 of a QEMU process, then: all six reclaims of the main-firmware
   series below, and both of F1's runs.
   (A write that does not fit reclaims the same way, from
   `UpdateVariable`; the forced runs below take that path.) The reclaim
   rewrites the whole store through the fault-tolerant-write spare: the
   spare, the store, then the spare again for the FTW working area (256,
   256 and 264 KiB). That is 798,300 programmed bytes, 98.9 % of them
   0xFF onto blocks just erased.
4. READ and MEASURED: QEMU programs a byte in `pflash_cfi01` on two
   guest writes, each a KVM exit, and `pflash_update` writes the byte's
   512-byte sector of the varstore file synchronously with `blk_pwrite`,
   from the vCPU thread. On this host, idle, that is 67-69 µs a byte with
   the store `snapshot=on` (a temporary qcow2) and 47 µs on a raw file;
   at a load of 14-16 it was 669 µs a byte.
5. So, idle, the reclaim held the restart 41-57 s between OVMF's console
   and its "BdsDxe: loading" line. At a load of 14-16 it would take about
   nine minutes (798,300 × 669 µs, REASONED from the measured rate): the
   guest showed the TianoCore logo with SSH silent for 300 s, the
   harness's limit, 2 of 2 times, 57 % through the reclaim at the first.

### Counts

Warm restarts in one QEMU process, 2 vCPUs, 10 s after the previous
boot's sshd greeted, through the box's own restart command; times on the
host clock from the command to sshd's banner.

| Guest, host | Firmware | Restarts | Median | Slow | Host load (1 min) |
|---|---|---|---|---|---|
| 10.9, idle | main (`7b87d3c8`) | 150 | 29.9 s | 2 in the firmware, restarts 61 and 123: 83.2 and 83.4 s (reclaims of 54.9 and 55.4 s); 3 at 38-39 s in the shutdown | 0.7-3.9 |
| 10.6.8, idle | main | 150 | 26.0 s | 2 in the firmware, restarts 61 and 123: 65.0 and 79.2 s (reclaims of 41.4 and 56.8 s) | 0.6-1.5 |
| 10.9, loaded (12 busy loops) | main | 150 | 53.2 s | 2 never answered within 300 s of the reset, restarts 61 and 122, each the 61st warm restart of its QEMU process, in the reclaim | 12.9-16.4 |
| 10.9, idle | 0006 | 150 | 30.1 s | none in the firmware: the reclaims at restarts 61 and 123 programmed 8,981 bytes and took OVMF's console to "BdsDxe: loading" 1.94 and 2.03 s (median 1.36 s); 9 at 38-39 s in the shutdown | 0.8-5.4 |
| 10.9, loaded (12 busy loops) | 0006 | 150 | 54.2 s | none in the firmware: the reclaims at restarts 61 and 123 took OVMF's console to "BdsDxe: loading" 9.5 and 10.1 s (median 3.8 s), restarts of 66.7 and 55.7 s; all 150 answered | 13.0-17.5 |

The 38-39 s restarts are the shutdown helper's own 20 s wait for jobs
still starting (`docs/decisions/0021`, "waited 20.1s for jobs still
starting"), on both firmwares, before the reset; not this.

**What was not measured.** 10.6.8 with 0006 was measured only on forced
reclaims (3 of 3), not in a warm-restart series; 10.6.0, the install
media, was not measured with either firmware. Both are covered by
REASONING: the reclaim runs before OpenCore, so the guest OS plays no
part, and 10.6.8 churns the same 3,592 bytes a boot and reclaimed at the
same restarts as 10.9 on main. A Snow Leopard build runs in one QEMU
process on a writable `efivars.fd` and restarts far fewer than 61 times;
should its installer churn more, a reclaim on a raw file costs about
38 s (47 µs a byte), not a hang, and 0006 covers it the same way.

Forced reclaims: a cold boot on the box's store with all but 2,000 bytes
taken by deleted 4 KiB records (state 0x3C, as a superseded `ConOut`
leaves them), alternated by firmware.

| Guest | Firmware | Boots | QEMU start → OVMF's console | Bytes programmed |
|---|---|---|---|---|
| 10.9 | main | 5 | 36.6-38.2 s | 801,122 |
| 10.9 | 0006 | 5 | 1.15-1.28 s | 9,452 |
| 10.6.8 | main | 3 | 39.4-40.9 s | 801,122 |
| 10.6.8 | 0006 | 3 | 1.14-1.19 s | 9,452 |

## Alternatives

- **A bigger store** only moves the reclaim later; it is as slow when it
  comes.
- **Keeping OVMF's boot manager from rewriting the consoles** would be a
  deeper change to upstream's boot policy, for a churn every OVMF guest
  has; the reclaim would still come, later.
- **QEMU** is the host's, not ours to ship or patch.
- **CFI buffered programming** (write-to-buffer, `0xE8`), where QEMU
  writes the file once per buffer flush, not once per byte. QEMU 8.2.2
  logs its own "Write to buffer emulation is flawed" on that path
  (`pflash_cfi01.c`, `LOG_UNIMP`), reworked only on later masters; and it
  would still program every 0xFF byte. Rejected.
- **A fresh store per boot** (no `snapshot=on` persistence across warm
  restarts) is not available: the store belongs to the QEMU process, and
  Mac OS X's own writes to NVRAM must last across a warm restart.

## What would change this

- QEMU programming a byte without a synchronous file write, or edk2
  programming the flash some cheaper way: 0006 can then go.
- Upstream: offering 0006 to edk2 is queued for after the release
  (the owner, R41, 2026-10-26); whether and when is the owner's call.
- An audk whose `QemuFlashWrite` or `QemuFlashEraseBlock` changed: 0006
  fails to apply and the build stops and says so.
