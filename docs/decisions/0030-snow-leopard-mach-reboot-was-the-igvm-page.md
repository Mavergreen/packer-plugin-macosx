# 0030 — 10.6's "MACH Reboot" hang was most likely OVMF reading the IGVM page

Date: 2026-10-24, revised 2026-10-25
Status: accepted, by elimination (G4: the owner, 2026-10-25)

## Decision

Nothing changes in what the plugin builds. The one `MACH Reboot` hang
(ADR 0015's build `snowleopard-3`, 2026-10-10) is attributed, most
likely, to the hang ADR 0018 fixed: after the guest's reset, OVMF took
leftover RAM at guest-physical `0x812000` for IGVM parameters and never
left PEI. Patch 0005 (OVMF never reads that page), in every build since
2026-10-14, removes that cause.

What is root-caused, and what is inferred:
- **Measured: the reset was not lost.** 10.6 resets with one write of
  `0x0F` to `0xCF9`, and QEMU reset on it in every one of 597 traced
  restarts. A write lost on purpose is made again 30 s later by xnu's
  own timer, and that one resets (6 of 6). So `snowleopard-3`, which sat
  at `MACH Reboot` for an hour, would have had to lose two writes 30 s
  apart; none of 597 was lost.
- **Measured: 10.6.0 can leave a page there that hangs the old
  firmware, with that build's exact screen.** 2 of 50 restarts of a
  10.6.0 box at 2 vCPUs on a loaded host left one. Written back at the
  reset write on firmware built from the same source as
  `snowleopard-3`'s, each hung it in PEI, `MACH Reboot` still on the VNC
  screen (one 3 of 3 at 1 vCPU, the other 1 of 1 at 2); this tree's
  firmware booted.
- **Measured: at a build's restart after the 10.6.8 combo, the page
  holds different bytes in every build** (4 builds, 4 pages), so it is
  not fixed by the restart.
- **Inferred: that `snowleopard-3`'s page was such a page.** That guest
  was stopped without a dump. It had 1 vCPU, and at 1 vCPU no natural
  10.6.0 page sampled here (230 box restarts, the 4 builds' combo
  restarts) would have been taken at all: each is rejected as an
  invalid map. What singles out this hang over any other firmware hang
  after a warm reset is that it is the one such hang known in this
  firmware, that 10.6.0 is shown to set it off, and that 0005 removed
  it.

ADR 0018's claim that under 10.6 the page always holds the kernel's own
image is corrected: that holds for 10.6.8's kernel, not for 10.6.0's.
So is its rule that only a count above the vCPU count hangs: a memory
map taken alone can hang too (below). ADR 0015's open item is closed by
this record.

No workaround is added. The guest's reset path and QEMU's reset control
were read and measured; neither loses or ignores a reset.

## Context

`snowleopard-3`, built before ADR 0018, timed out waiting for SSH. Its
screen, read over VNC an hour later, ended `done` / `MACH Reboot`, and its
QEMU used one host CPU flat out. That was the restart after the 10.6.8
combo update, under 10.6.0's kernel; the guest had 1 vCPU and Packer's VNC
display. ADR 0015 read it as the guest asking for a reset that QEMU never
made.

On that evidence alone, a lost reset and a hang after the reset look the
same: with VNC on, either leaves the guest's last frame and a spinning
vCPU. What would tell them apart, the registers or a trace, was not
captured for that build. What is measured here, on other restarts: QEMU
reset the machine every time, a lost write is retried and resets, and
pages 10.6.0 leaves behind can hang the old firmware after the reset
with that screen. What is reasoned: that the firmware of that build
hung on its own page.

### How 10.6 restarts

MEASURED (10.6.0's AppleACPIPlatform disassembled; a breakpoint and the
stack in a running guest), READ (xnu-1456.1.26 source, a neighbour of
the running 1456.1.25; 10.6.8's helpers by symbol name only):
- xnu's `halt_all_cpus(TRUE)` prints `MACH Reboot` and calls
  `PEHaltRestart(kPERestartCPU)`, which arms a 30 s thread call,
  `IOShutdownNotificationsTimedOut`, that calls the platform's
  `haltRestart` again; then it notifies power-management clients and
  calls `AppleACPIPlatformExpert::haltRestart` (READ).
- That syncs NVRAM (`IODTNVRAM::sync`, by its vtable slot: inferred),
  then reaches the reset through `PE_halt_restart`, which on these
  guests is AppleSMC's `smcHandlePEHaltRestart` passing on to
  AppleACPIPlatform's handler (MEASURED, on the stack). 10.6.0's
  AppleACPIPlatform 1.3 (`performSystemReset`) does the following, and
  10.6.8's 1.3.6 (`platformHaltRestart`) does the same by its symbols:
  if the FADT says `RESET_REG_SUP`, `AcpiHwLowLevelWrite(8, ResetValue,
  &ResetRegister)`, which is one `out %al,(%dx)`; on success, `jmp .`
  forever. There is no keyboard-controller fallback, no
  `0x02`-then-`0x06` sequence; the only second write is the 30 s
  thread call's.
- In a 10.6.0 guest, at that `out` (a hardware breakpoint): FADT flags
  `0x84a5` (`RESET_REG_SUP` set), the reset register SystemIO, 8 bits, at
  `0xCF9`, value `0x0F`; `RAX` `0xF`, `RDX` `0xCF9`, interrupts on.

### A lost write is made again 30 s later (MEASURED)

At that breakpoint the `out` was skipped (`set $pc += 1`), so the first
write never reached QEMU, and the breakpoint kept. On 1 vCPU (3 trials)
and 2 vCPUs (3), 10.6.0 box, idle host:
- The same `out` was hit again 30.0 s later every time (30.00-30.04 s),
  with the same `0xF` to `0xCF9`, from a thread-call thread
  (`call_continuation` → the thread-call loop → AppleSMC's handler →
  `performSystemReset`), not the restart command's (`boot` →
  `host_reboot` → `halt_all_cpus` → `PEHaltRestart`).
- QEMU then reset at once (one `lpc-reset-control` write, `cpu_reset`
  0.1 ms later), and the guest answered SSH again: 6 of 6.
- Meanwhile the screen showed `MACH Reboot` and nothing more. On 1 vCPU
  the spinning thread was preempted for the timer; on 2 vCPUs the
  timer ran on the other vCPU, or the spinning thread moved to it.

So one lost write costs a 30 s pause, not a hang.

### What QEMU does with it (8.2.2, READ; MEASURED by its trace)

- `ich9_rst_cnt_write` (`hw/isa/lpc_ich9.c`): any value with bit 2 set
  calls `qemu_system_reset_request(SHUTDOWN_CAUSE_GUEST_RESET)`; any other
  only keeps bits 1 and 3. `0x0F` resets. Bit 3 (`FULL_RST`) changes
  nothing: QEMU makes the same system reset either way, and keeps RAM,
  which is why the page is still there for OVMF to read. The request
  sets `reset_requested`, stops the writing vCPU, and the main loop
  pauses every vCPU, resets the machine and resumes. A second request
  while one is pending only sets the same flag again. With `-no-reboot`
  the request becomes a shutdown; Packer's builds do not pass it.
- Upstream changed nothing in that handler or that request through
  `master` of 2026-10-24 (refactors only). `b10166b104`, "ich9: fix
  default value of 'No Reboot' bit in GCS", is the TCO watchdog's, not
  `0xCF9`'s.
- QEMU's own trace (`memory_region_ops_write`, `cpu_reset`) on every
  measured restart below: one write of `0xf` to `0xcf9`,
  `lpc-reset-control`, then `cpu_reset` 0.08-8.1 ms later. Never a write
  without a reset, and never a reset without the write.

### Why the screen kept `MACH Reboot` (MEASURED)

A reset does not clear the VGA's framebuffer, and the firmware hangs
before it draws. What a viewer then sees depends on QEMU's display: with
a VNC server, as Packer's builds have, the guest's last frame stays; with
`-display none` alone, a screendump a minute later read "Guest has not
initialized the display (yet)", as ADR 0018's 10.9 hangs did.

The reproductions: a 10.6.0 box of this tree on firmware built from the
same source as `snowleopard-3`'s (`afbdc8c0…`, F1's rebuild from before
ADR 0018; OVMF's bytes depend on the build directory, ADR 0006, and the
box and OpenCore configuration were this tree's), a VNC display as
Packer has, a breakpoint on the reset `out`, a page written to
`0x812000` there, then the write let go. QEMU reset at once every time;
then:

| Page planted | vCPUs | Old firmware | This tree's (0005) |
|---|---|---|---|
| F1's hanging 10.9 page (a map, count 59) | 1 | 3 of 3 hung: spun at `RIP` `0x7fcfe4b5`, `CR3` `0x800000` (F1's signature) | 3 of 3 came back |
| a 10.6.0 page from restart 3 below (a map, count 350) | 1 | 3 of 3 hung: spun at `RIP` `0x2012`, `CR3` `0x800000` | 1 of 1 came back |
| the same, its count zeroed by hand | 1 | 1 of 1 hung, the same way | |
| a 10.6.0 page from restart 21 below (a map, count ≥ 65536) | 2 | 1 of 1 hung: reset over and over in PEI (96 resets in 74 s), `CR3` `0x800000` | |
| the 17 other distinct 10.6.0 pages OVMF would not reject outright | 2 | 17 of 17 came back | |
| none | 1 | 1 of 1 came back | |

Every hang: SSH silent, and the VNC screen still ending `done` /
`MACH Reboot` 70 s later (read by eye on two, the rest by OCR to
`done`): `snowleopard-3`'s screen. QEMU's CPU use was measured at
100 % for F1's page only.

So a taken memory map can hang the firmware without a count (MEASURED),
and a count of 65536 or more, which ADR 0018 has marking the area
invalid, does not save it (MEASURED); REASONED: the map is checked in
`AddressWidthInitialization`, before the count is read, as ADR 0018
orders them. Which code they hang in was not traced.

### Why 10.6.0 is exposed and 10.6.8 is not (MEASURED)

10.6 has no KASLR and its kernel sits at physical `0x200000`. 10.6.8's
image runs to `0x8cb000`, so `0x812000` is its `__DATA,__const` (vtables:
kernel pointers, rejected by OVMF's check every time, as ADR 0018 says).
10.6.0's image ends at `0x771018` (`__LINKEDIT`), so there the page is
other memory, and what it holds depends on the boot.

Saved before each restart and again within a second after QEMU's reset
(what OVMF then reads). All but two rows were taken on this tree's
firmware, which reserves the page (0004); only the 1-vCPU idle box
(the old firmware's 20 pages held the same object as HEAD's 300) and
the F24 harness were also sampled on the old firmware. "Taken" is what
OVMF's check would not reject (no map, or a map); "hung" is measured by
writing the page back on the old firmware, as above.

| 10.6.0 restart | Pages | What `0x812000` held | On a pre-0005 OVMF |
|---|---|---|---|
| a box's, 1 vCPU, idle (old firmware and HEAD's) | 320 | the same kernel object every boot (`e0 … 02 … b6 01 …`), its other fields varying | all rejected (entry 0's type `0x1b6`) |
| a box's, 1 vCPU, host load 15-20 | 120 | that object in 26; other memory in 94 | all rejected |
| a box's, 2 vCPUs, idle | 120 | never that object | 13 taken (7 restarts), counts 0; the 5 distinct pages after a reset came back 5 of 5 |
| a box's, 2 vCPUs, host load 14-27 | 100 | never that object | after the reset: 14 of 50 taken; 2 of those hung (restarts 3 and 21), 12 came back |
| the combo's, F24's harness on that box, 10.6.8 kexts loaded into 10.6.0 (AudioAUUC 1.57, AppleSMBusPCI) | 12 | the object | all rejected; the old firmware came back 12 of 12 |
| the installer's, into a build's first boot | 5 (5 builds) | 5 different pages: random-looking bytes, or sparse data | all rejected |
| the combo's, in a build: the restart `snowleopard-3` hung at, in 4 builds of this tree (not `snowleopard-3`'s own page, which was not saved) | 4 (4 builds) | 4 different pages: x86 code in 3, UTF-16 text and zeros in 1; no two alike (3434-3988 of 4096 bytes differ) | all rejected (entry 0's type `0xa752` to `0xe8ee894c`) |

Under 10.6.8 the page held the same kernel vtables before and after
every one of 240 box restarts (480 pages), and at each build's first
10.6.8 restart, as ADR 0018 says.

So under 10.6.0 the page is whatever the kernel last kept there, and
that changes with the vCPU count, the load and what the boot did: at a
build's combo restart, a different page in each build sampled. At 2
vCPUs on a loaded host, 2 restarts in 50 left one that hangs the old
firmware (95 % interval 0.5-14 %); at 1 vCPU, none in 230 box restarts
was even taken. That is 10.9's case in ADR 0018, where 8 pages in 100
passed the map check, 5 also had a count, and 4 of those hung. Whether a
page that hangs comes up at a 1-vCPU combo restart 1 build in 3, as that
day's builds did, or far less often, 4 samples cannot say (95 % upper
bound 53 %).

### Why 10.6.0 at 1 vCPU uses the page under 0004 too (open)

0004 marks the page runtime-services data, yet 10.6.0's 1-vCPU boxes
put the same kernel object there on HEAD's firmware as on the old one.
10.6.0's image does not cover the page, so the reason ADR 0018 gives for
10.6.8 (OpenCore's relocation block copying the kernel over it) does not
explain it. Why is not established; 0005 makes it irrelevant to OVMF.

## Measurements

All 2026-10-24 and 25 on `pet-power-plant` (i7-8700B, 12 threads, KVM,
QEMU 8.2.2, host kernel 7.2.9), one guest at a time, the box's QEMU line
(q35, Penryn, 4 G, e1000-82545em, IDE, xhci with OpenCore, VGA), the
trace on from each restart command to its reset. A restart alternates
`sudo reboot` and `sudo shutdown -r now`, 10 s after the guest answered
SSH. NORESET: no `cpu_reset` within 240 s; NOBOOT: a reset, then no SSH
greeting in 300 s, each dumped (registers, screen) and told apart:
firmware (`CR3` `0x800000`, `RIP` below 4 G) or the kernel (a panic on
the screen). Host load is the 1-minute average over the series;
"loaded" is 16 busy loops of our own, each bounded by `timeout`, on top
of the host's other work.

Per configuration, on this tree's firmware (`7b87d3c8…`, both patches).
The bound is the 95 % upper bound, by the rule of three, on the rate of
this hang's kind (NORESET, or a firmware NOBOOT), none being seen in N:

| Guest | vCPUs | Host load | Restarts | NORESET | NOBOOT | Bound |
|---|---|---|---|---|---|---|
| 10.6.0 box | 1 | 0.6-1.7 | 150 | 0 | 0 | 2.0 % |
| 10.6.0 box | 2 | 0.6-1.6 | 60 | 0 | 0 | 4.9 % |
| 10.6.0 box | 1 | 14.6-20.5 | 60 | 0 | 0 | 4.9 % |
| 10.6.0 box | 2 | 13.9-26.9 | 50 | 0 | 9, every one a kernel panic | 5.8 % |
| 10.6.8 box | 1 | 0.6-7.4 | 100 | 0 | 0 | 2.9 % |
| 10.6.8 box | 2 | 0.6-2.0 | 50 | 0 | 0 | 5.8 % |
| 10.6.8 box | 1 | 13.5-19.3 | 40 | 0 | 0 | 7.2 % |
| 10.6.8 box | 2 | 17.9-26.7 | 50 | 0 | 11, every one a kernel panic | 5.8 % |
| a build's combo restart (`updates = "security"`, 10.6.0 to 10.6.8) | 1 | 1.2-3.7 | 6 builds (4 sampled here, 2 earlier: the final builds of 2026-10-15 on) | 0 | 0 | 39 % |

Only the first row meets spec G4's bar of 0 in 150. The bounds hold for
the configuration they were measured in; a cause specific to one
restart, such as the combo's, is bounded only by that restart's own
count, the last row.

At 2 vCPUs on the loaded host both kernels panic, after resets that
worked: every NOBOOT there followed one `0xcf9` write and its reset,
and both vCPUs were stopped in the kernel (`RIP` `0xffffff80…`). The
screens read by eye show panics: 10.6.0's are ADR 0014's (`Spinlock
acquisition timed out` in `pmap_enter`; `thread_invoke:
preemption_level 2` from `IOConfigThread`); 10.6.8's, `Spinlock
acquisition timed out` on the pmap lock with `configd` and `kextd` both
in `pmap`, 6-8 s into the boot, and every 10.6.8 dump stopped at the
same two addresses. The same
came on cold boots: 5 of 15 for 10.6.0, 5 of 17 for 10.6.8. That is
ADR 0014's two-CPU finding, now on 10.6.8 too (spec G2), not this hang.

Every traced restart (the 580 above in loops, 4 of the combo trials,
7 by breakpoint, and the 6 skip trials' second writes) wrote `0xf` to
`0xcf9` once and reset 0.08-8.1 ms later (median 0.2-0.7 ms per series
idle, 2.4-3.0 ms at 2 vCPUs loaded); on 2 vCPUs the write came from
whichever vCPU ran the shutdown (idle: CPU 0 in 31 of 60, CPU 1 in 29;
loaded: 43 and 57 of 100). Every loop restart reset within 3 s of its
command, so none was a first write lost and rescued by the 30 s timer:
that would show as a reset 30 s late and two writes, which the 240 s
NORESET window alone would not have caught.

Also on this tree's firmware, earlier: F18's 180 10.6.0 and 100 10.6.8
restarts (1 vCPU, load under 2 for 10.6.0, under 6.6 for 10.6.8), every
one of which reset and came back. On firmware with neither patch, ADR
0015's 70 10.6.0 and 30 10.6.8 restarts, and here 20 of the 10.6.0 box
(1 vCPU, idle) and 12 combo trials (F24's harness, kextd not held), none
hanging.

## G4

Spec G4 asks that the hang be root-caused, or shown gone by measurement
on the fixed firmware, a rate of at least 0 in 150 standing in for a fix
only with the owner's agreement. Neither holds outright:
- The cause is inferred, as above: a lost reset is ruled out by
  measurement; that 10.6.0's leftovers hang the old firmware with this
  screen is measured, but at 2 vCPUs, not for that build's 1-vCPU
  restart.
- On the fixed firmware, the configuration that failed (a build's combo
  restart, 1 vCPU) has 6, not 150; the box configurations have 40 to 150
  each.

0005 removes the inferred cause by construction (OVMF never reads the
page; `internal/firmware`'s tests hold it), and the planted pages that
hung the old firmware boot on this tree's. The owner accepted that,
with the counts above, as meeting G4 (2026-10-25), and ADR 0015's item
is closed.

## Alternatives considered

- **A defensive reset in the guest** (a second write, a
  keyboard-controller pulse, `0x02` then `0x06`): nothing to defend.
  The single write resets QEMU every time, and xnu already makes a
  second one if the first is lost.
- **Clearing guest RAM at reset in QEMU**: ADR 0018 already rejected
  it; the firmware's reading of the page is the defect, and 0005
  removes it.
- **More 10.6.0 builds on the old firmware to catch a natural hang**: if
  the rate is near the 1 in 3 of that day, a dozen 20-minute builds
  would show it; if it is near the 2 in 50 seen here at 2 vCPUs, or
  rarer at 1, it needs a hundred or more. The pages show the mechanism
  with 10.6.0's own bytes; a natural hang would only add the build.

## What would change this

- **Dropping or revising patch 0005** (ADR 0018's list): 10.6.0's
  restarts are exposed again, not only 10.9's.
- **A `MACH Reboot` hang on firmware with 0005**: then it is something
  else. The trace will say which: a `cpu_reset` after the `0xcf9`
  write is a firmware or boot hang; no write at all, even 30 s later,
  is the guest's.
- **A sampled 1-vCPU combo-restart page that hangs the old firmware**:
  the attribution is then shown at that restart, not only inferred.
- **The same page at the combo restart in every build**, unlike the 4
  here: the attribution would be refuted, and #8 reopened.
- **A QEMU whose `0xCF9` handler changes**: re-read `ich9_rst_cnt_write`.
