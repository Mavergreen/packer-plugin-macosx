# 0018 — OVMF neither lends nor reads the IGVM parameter page

Date: 2026-10-13
Status: accepted, on measurement

## Decision

The OVMF build carries two more patches, applied in this order. Each one
is skipped when the tree already carries it.

| Patch | Whose | What it does | Skipped when `Igvm.c` has |
|---|---|---|---|
| `assets/firmware/patches/0004-ovmf-reserve-the-igvm-parameter-area.patch` | upstream EDK II's, byte for byte; `acidanthera/audk` has not merged it | marks the page at guest-physical `0x812000`, OVMF's "IGVM parameter area", as runtime services data, so an OS that honours the memory map leaves it alone | `PlatformIgvmParamReserve` (an audk that has merged it) |
| `assets/firmware/patches/0005-ovmf-never-read-igvm-parameters.patch` | ours; upstream will never carry it | OVMF never takes that page's contents: no memory map and no vCPU count from it, whatever bytes are there | its own marker comment, `packer-plugin-macosx: assets/firmware/patches/0005` |

`internal/firmware`'s tests pin each patch's diff by sha256. 0004's is
the sha256 of upstream's own diff of `a8210e06`, so the claim "byte for
byte" is checked, not just stated.

| 0004's source | |
|---|---|
| Upstream commit | tianocore/edk2 `a8210e06a2a19814e750e17919f44b18c6fb17c6`, "OvmfPkg/PlatformInitLib: reserve igvm parameter area" (Gerd Hoffmann) |
| Merged | `e4f99a85545f41ef7ae23cd939484d69a84b6176`, 2025-12-10, [edk2#11866](https://github.com/tianocore/edk2/pull/11866) |
| Reported upstream as | "commit 5e8db785: intermittent crashes and hangs on reboot" (edk2-devel, 2025-12-05); the reporter confirmed the fix |
| In audk | not at the pinned `0672a009`, nor on audk `master` on 2026-10-13 |

**0005 is one line:** `mIgvmParamsInvalid`, a static flag in
`OvmfPkg/Library/PlatformInitLib/Igvm.c`, starts `TRUE` instead of `FALSE`.

- `PlatformIgvmMemoryMapFind` and `PlatformIgvmVpCount` are the only code
  that reads the parameter area. Each tests this flag before it reads
  anything, and nothing sets the flag back to `FALSE`.
- So the memory-map check reports no map, and E820 comes from fw_cfg's
  `etc/e820`.
- The vCPU count reports none, so `PlatformMaxCpuCountInitialization`
  asks fw_cfg and the CPU-hotplug block.
- That is what every cold boot already did, with a zeroed page.

Why it is safe here: QEMU 8.2.2 has no IGVM loader
(`qemu-system-x86_64 -object help` lists no `igvm-cfg`), and the plugin
never passes one. Nothing can ever have put real parameters in that page.

The rejected alternative is setting `PcdOvmfIgvmParamBase` to 0. That
value comes from a MEMFD region in `OvmfPkg/Include/Fdf/MemFd.fdf.inc`,
which the reset vector's IGVM metadata also describes. Changing it would
move the firmware's memory layout to change one decision.

**Why both.** 0004 alone fixes 10.9, whose kernel honours the memory map.
It cannot fix an OS that writes over reserved memory anyway, and 10.6
does exactly that (below). 0005 alone would fix both releases. 0004 stays
because it is upstream's, it costs nothing, and it keeps the page as a
cold boot left it for anything else that might look there. Neither patch
is needed once both of these hold:
- the audk pin carries 0004;
- OVMF only takes the area when a loader says it filled it.

## Context

About one warm restart in ten to twenty-five (`sudo shutdown -r now` in
the guest) never came back. The gate's `--restarts 10` failed on many runs
because of it. No cold boot ever hung.

On a hung guest:
- vCPU 0 spun in OVMF's PEI phase, in MpInitLib's `TimedWaitForApFinish`,
  waiting `MAX_UINT32` µs (about 71 minutes) for dozens of APs in a 2-CPU
  guest.
- vCPU 1, the one real AP, had checked in and halted.
- A QMP `system_reset` hung the same way.
- A new QEMU process booted the same disk.

What happens, in order:

1. **OVMF reads a fixed page of RAM as IGVM parameters on every boot.**
   Since edk2 `5e8db785` and `38370cf4` (2025-09), PEI's `PlatformInitLib`
   looks at `PcdOvmfIgvmParamBase`, `0x812000`, for what an IGVM loader
   would put there:
   - a memory map, which starts the page (`PlatformIgvmMemoryMapCheck`,
     reached from `AddressWidthInitialization`);
   - a vCPU count at `+0xC0` (`PlatformIgvmVpCount`, from
     `MaxCpuCountInitialization`, which runs later).

   Plain QEMU loads no IGVM file and puts nothing there.
2. **Nothing reserved that page, so the OS used it.** It was ordinary free
   memory in the map OVMF handed on. Snapshots of it while 10.9 ran held
   code, strings and pointers, different each boot.
3. **A cold boot finds zeros; a warm restart finds the last OS's bytes.**
   A new QEMU process starts with zeroed RAM. A reset, from the guest or
   from QMP, leaves RAM as it was.
4. **Sometimes those bytes pass OVMF's checks.** Exactly as `Igvm.c` has
   them:
   - **The memory map.** Entry 0's page count (offset 8) decides.
     - Zero: there is no map, and nothing is marked invalid.
     - Nonzero, with a type (offset 16) below 16 and a zero reserved field
       (offset 20): the bytes are taken as a memory map.
     - Anything else: the area is marked invalid.
   - **The count.** Unless the area was marked invalid, the dword at
     `+0xC0` is the vCPU count whenever it is from 1 to 65535. A value of
     65536 or more marks the area invalid instead.

   So a page with **no** map and a small dword at `+0xC0` is taken just
   like a page with a plausible map. `PlatformMaxCpuCountInitialization`
   returns that count before it asks fw_cfg or the CPU-hotplug block. That
   is why both read sanely by hand after a hang.
5. **MpInitLib waits for CPUs that do not exist.** With that count as
   `PcdCpuBootLogicalProcessorNumber`, MpInitLib waits for count − 1 APs
   to check in, with the 71-minute timeout. Its own comment says a missing
   AP hangs it "by design". A count above the guest's real vCPU count
   hangs.

The debug firmware's log of a hung boot, against the healthy boot just
before it on the same QEMU process:

| | Healthy warm boot | Hung warm boot |
|---|---|---|
| IGVM memory map | `memory map sanity check failed, ignoring` | taken: `IgvmMemoryMap[0]: PageStart 7C8, PageCount 10F119371, EntryType 7`, and 343 more garbage entries |
| vCPU count | `BootCpuCount=2 MaxCpuCount=2` (fw_cfg) | `PlatformIgvmVpCount: vp-count=1992` |
| AP stacks | 64 KB (2 cpus) | 63744 KB (1992 cpus) |
| End of the log | `MpInitLib: Find 2 processors in system.` | `CpuMpPei: 5-Level Paging = 0`, then nothing |

The dword at `0x8120C0` read 1992 in guest memory after that hang. The
release firmware's earlier hangs waited for 49 APs, which is a count of
50. The debug firmware (same tree, `-b DEBUG`, `isa-debugcon` on `0x402`)
hung on 1 of 13 warm restarts before it was stopped.

### Snow Leopard: 0004 cannot hold the page

- **The test.** A 10.6.8 box (10K549, built 2026-10-10, 1 vCPU) with
  0004's firmware came back from 10 of 10 warm restarts.
- **The page was not zero.** It held the same bytes before every restart:
  a table of 64-bit kernel pointers (`0xffffff80004f50ea`, …).
- **Why.** 10.6 has no KASLR. Its kernel occupies fixed low physical
  memory from `0x200000` up, and OpenCore's relocation block
  (`AllowRelocationBlock`) copies it there at `ExitBootServices`, over
  whatever the firmware reserved. Under 10.6.8 the page holds the
  kernel's own image (`__DATA,__const`), and what OVMF reads after a
  restart depends on the kernel build, not on chance.
- **Not under 10.6.0.** 10.6.0's image ends at `0x771018`, so there the
  page is not the kernel image, yet 10.6.0 uses it under 0004 too
  (MEASURED; why is not established): a kernel object on a box's
  restarts; at a build's restart after the 10.6.8 combo, x86 code or
  data, different in each of the 4 builds sampled; at 2 vCPUs on a
  loaded host, now and then a page that hangs the firmware (2 restarts
  in 50). That combo restart most likely hung this way once, before
  0005, with `MACH Reboot` left on the screen (REASONED, ADR 0030).
- **Why 10.6.8 was safe anyway.** Every 64-bit kernel pointer puts
  `0xffffff80` in entry 0's reserved field, so the area is marked invalid
  and the count is never read.
- **The risk.** A kernel whose bytes there gave a zero page count, or a
  plausible map, and a small dword at `+0xC0` would hang every warm
  restart. A plausible map alone can hang it too, whatever the count:
  two of 10.6.0's own pages did (MEASURED 2026-10-25, ADR 0030).
- **The fix.** 0005 removes that dependence on the kernel's bytes, and
  on whatever 10.6.0 left there.

10.9's kernel is slid and leaves the page alone, as its restarts show.

## Measurements

All 2026-10-13 and 14 on `pet-power-plant` (QEMU 8.2.2, KVM, host kernel
7.2.9), one guest at a time.
- **The guests.** 10.9: the gate's layer box, with the gate's QEMU command
  line through `bin/gate-vm.sh`, 2 vCPUs and q35. 10.6: the box above.
- **Each restart.** `sudo killall ManagedClient; sudo shutdown -r now`, 10 s
  after the previous one answered SSH.
- **A hang.** SSH did not answer on a new boot within 300 s. QEMU was then
  killed and a new QEMU process booted for the next trial.
- **The page.** It was saved over QMP before every restart.

### Warm restarts, by chance

| Release firmware | `OVMF_CODE.fd` | Guest | Warm restarts | Hung |
|---|---|---|---|---|
| As shipped (audk `0672a009`) | `afbdc8c0…` | 10.9 | 100 | **4** (restarts 19, 54, 73, 92) |
| 0004 | `37b09daa…` | 10.9 | 150 | **0** |
| 0004 + 0005 | `92e4db1d…` | 10.9 | 150 | **0** |
| 0004 | `37b09daa…` | 10.6.8 | 10 | 0 |
| 0004 + 0005 | `92e4db1d…` | 10.6.8 | 30 | **0** |

**The shipped firmware's 100 pages**, classified by the rule in step 4:

| Page before the restart | Restarts | Hung |
|---|---|---|
| marked invalid by entry 0 | 78 | 0 |
| no map (page count 0), count 0 or ≥ 65536 | 14 | 0 |
| a plausible map, count 0 or ≥ 65536 (0, 0, 11711121) | 3 | 0 |
| a plausible map and a count from 1 to 65535 | 5 | 4 (counts 59, 1992, 4, 3) |

The fifth page with a count, 48, booted. That page most likely changed in
the seconds between the save and the reset. One of the three plausible-map
pages booted on that garbage memory map in 51 s, against the usual 21 s.

**With 0004, under 10.9**, the page read all zero before 150 of 150
restarts, and again before 150 of 150 with both patches. Under 10.6.8 it
held the same kernel bytes before all 40 restarts on either firmware,
which 0005 makes irrelevant.

**The odds.** If the shipped and 0004 runs had the same hang rate, the
chance of 0 of the 4 hangs landing in the 150 is 2.5% (one-sided Fisher).
The runs earlier on 2026-10-13 hung on 7 of about 70 warm restarts, many
of them 30 to 86 s after boot, against about 20 s here.

### Warm restarts with the hanging bytes planted

Chance aside, the direct test was to plant the bytes and restart.
- **The bytes.** The page saved from restart 19's hang: entry 0 page count
  `0x10290ec11`, type 10, reserved 0, and 59 at `+0xC0`.
- **The plant.** The page went to `0x812000` in the running 10.9 guest
  through QEMU's gdbstub in physical-memory mode, and was read back over
  QMP to confirm it was there.
- **The restart.** A warm restart as above.

| Release firmware | Trials | Planted and confirmed | Hung |
|---|---|---|---|
| 0004 | 3 | 3 of 3 | **3** |
| 0004 + 0005 | 10 | 10 of 10 | **0** |

Each 0004 hang matched the shipped firmware's hangs: vCPU 0 spun in PEI
with CR3 `0x800000`, vCPU 1 halted, and the page still held the planted
bytes. 0004 keeps the OS out of the page, but it does not stop OVMF
reading whatever is there. 0005 does.

### The firmware images

All three were built in the same tree and directory.
- **0004 and 0005** change only `PEIFV`, the volume `PlatformInitLib` is in.
- **`DXEFV` and `OVMF_VARS.fd`** (`5d2ac383…`) are byte-identical in all
  three.

## Alternatives considered

- **0004 alone.** It is upstream's whole fix and settles 10.9. It leaves
  10.6 depending on what its kernel image holds at `0x812000`.
- **0005 alone.** It would be enough today. 0004 stays because it is
  upstream's, so it will arrive with audk eventually, and it costs
  nothing.
- **Set `PcdOvmfIgvmParamBase` to 0.** It moves the MEMFD layout that the
  reset vector's IGVM metadata also describes (above).
- **Ignore the area unless an IGVM loader filled it.** Nothing marks a
  non-IGVM boot, so upstream's firmware cannot tell. This project can,
  because it never uses one: that is 0005.
- **Clear the page in the reset vector or SEC.** That would break real
  IGVM boots, which put their parameters there before the first
  instruction. It would also add code where 0005 changes one value.
- **Work around it in QEMU**, by clearing RAM on reset or avoiding warm
  resets. The defect is OVMF's, and the guest's own `reboot` is a warm
  reset.
- **Move the audk pin.** audk `master` has neither fix, and audk moves
  only with OpenCore (INGREDIENTS.md).

## What would change this

- **An audk pin that has merged edk2 `e4f99a85545f`.** The build then
  finds `PlatformIgvmParamReserve` already there and skips 0004; delete
  0004 then. 0005 still applies, because its marker is its own.
- **An upstream change to how OVMF decides whether IGVM parameters are
  present**, such as a signature. 0005 may then be unnecessary. Re-read
  both patches then.
- **A QEMU or plugin that loads IGVM files.** 0005 would then throw away
  real parameters. Drop 0005 before using one.
- **Any change to the OVMF patch set: dropping 0005, revising it, or
  revising 0004.** A patch whose marker is already in the file is skipped,
  so a cached EDK II tree must not keep an old patch set. The tree records
  which patches it may hold in `.guest-macosx-ovmf-patches`, one name and sha256 per
  line. Each line is written before its patch is applied, so a build that
  dies in between leaves a patch recorded and not applied, never the
  reverse. OpenCore's assembly reassembles a tree whose record is not a
  prefix of this plugin's set, and the OVMF build refuses one. A tree from
  before the record existed is reassembled once.
- **An audk bump.** 0005 holds only while three things are true:
  - every reader of `PcdOvmfIgvmParamBase` tests `mIgvmParamsInvalid`
    before it reads the PCD;
  - the flag starts `TRUE`;
  - nothing sets it `FALSE`.

  Upstream could add a reader, and 0005 would still apply cleanly, because
  its hunk's context is the struct above the flag. So before every OVMF
  build, `checkIGVMReaders` (`internal/firmware/igvm.go`) checks all three
  in the patched tree, with comments and string literals stripped first.
  - It reads EDK II's style and nothing else. A function is `Name (`
    alone on a line, a parameter per line, `)` alone, then `{` alone.
    Every top-level brace block must be such a function or a `typedef`,
    and every use of the PCD or the flag must sit in a function body it
    recognised. A header in any other style fails the check rather than
    hide a reader.
  - A reader counts as guarded only by `if (mIgvmParamsInvalid) {`,
    `return …;`, `}` as the body's first statement, after nothing but
    local declarations, and before its read. A body that names the PCD
    may not jump (a `goto` or a label).
  - A backslash-newline anywhere in `Igvm.c` fails, and so does a
    `#define` or `#undef` over `TRUE`, `FALSE`, `if`, `return`, the flag
    or the PCD.
  - The flag may appear only in its one `= TRUE` declaration, in such
    guards, and in `mIgvmParamsInvalid = TRUE;`. Any other use, however
    spaced, could clear it, so it fails.
  - A preprocessor directive inside a function fails, and so do
    unbalanced braces.
  - The only exception is 0004's `PlatformIgvmParamReserve`, which passes
    the address on and reads nothing. It is exempt only while its body
    digests to what was read from 0004.
  - No other C source may name the PCD in code. A comment that names it
    reads nothing.

  The check is a tripwire for drift, not a proof. It fails closed, and it
  cannot see code that reaches the page without naming the PCD. If it
  fails, the build stops and names the reader. Re-read `Igvm.c` against
  this record then.
- **A hang in PEI after a warm restart with both patches.** The cause
  would be something other than this page, because OVMF no longer reads
  it.
