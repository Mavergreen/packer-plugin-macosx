# 0015 — The RTC is declared with the two ports QEMU answers

Date: 2026-10-10
Status: accepted, on measurement

## Decision

Both releases' `config.plist` carry one `ACPI > Patch`: in the DSDT, the
RTC's (`PNP0B00`) `IO (Decode16, 0x0070, 0x0070, 0x01, 0x08)`, bytes
`47 01 70 00 70 00 01 08`, becomes length `0x02`. It is the only ACPI
patch, and `internal/firmware`'s
`TestEveryReleaseShrinksTheRTCToTheBankQEMUHas` holds it.

## Context

10.6 guests hung at random after they booted. Sitting at the login window
without logging in, with sshd seemingly accepting connections and never
sending a banner, a 10.6.8 build timed out waiting for SSH on its second
boot, and a 10.6.0 box would not come up under `vagrant up`. The same
images booted under TCG, slowly. Host kernels, the date, and other
guests running beside it were all suspected; none was it.

What happens, in order:

1. **QEMU declares eight RTC ports and answers two.** Its DSDT gives the
   RTC ports `0x70` through `0x77`, "following what physical hardware
   does, even though qemu only responds to the first two ports"
   (`hw/rtc/mc146818rtc.c`, `rtc_build_aml`, 8.2.2). Its CMOS is one
   128-byte bank.
2. **`AppleRTC` takes eight ports to mean two banks**, and does not log
   its `RTC: Only single RAM bank (128 bytes)`. It keeps the sleep trace
   point in the second bank, behind ports `0x72`/`0x73`, which nothing
   answers: every byte reads `0xFF`.
3. **The kernel takes all ones for a sleep failure.** `AppleRTC` hands the
   root domain the two words through `IOPMRegisterNVRAMTracePointHandler`
   (xnu `IOPMrootDomain::callPlatformFunction`), which logs `Sleep failure
   code 0xffffffff 0xffffffff` and publishes `PMStatusCode` =
   `0xFFFFFFFFFFFFFFFF`: a failure in every phase, every PCI device at
   fault. On every boot, of both releases.
4. **10.6's `configd` crashes on it.** Its PowerManagement plugin
   (`PowerManagement.bundle` 160.0.0), once `AppleRTC` matches, turns
   each set bit of `PMStatusCode`'s upper word into an index into the
   root domain's `PCITopLevel`, without checking the array's length. The
   guest's array has 7 entries; bits 7 through 31 read past its end, and
   `CFGetTypeID` faults on whatever is there. launchd restarts `configd`
   every 10 s; whether a restart survives is what the heap happens to
   hold past the array.
5. **No `configd`, no network.** No DHCP, no address: QEMU's user-mode
   network accepts the host side of the forwarded port and its SYN to
   the guest is never answered, which looks from the host like an sshd
   that sends no banner. Without `configd`, loginwindow does not
   auto-login either.

10.9's `powerd` has the same code, and reads the same all-ones value,
but survives it twice over: it consults `PCITopLevel` only when the
failure phase is a driver phase (`0x14` or `0x27`; all ones is `0xFF`),
and its loop is bounded by the array's length. What it does do, every
boot, is publish a bogus `State:/IOKit/PowerManagement/PriorBootSleepFailure`
with every field -1.

## Measurements

All 2026-10-10 on `pet-power-plant` (i7-8700B, KVM, host kernel
7.2.9-2), one guest at a time, every guest a fresh copy of its disk.

Booted until SSH answered or 300 s passed, in three sets:

| Guest | Without the patch | With it |
|---|---|---|
| 10.6.0 box (10A432), built 2026-10-04 | 5 of 10 answered: 5 of 7 in a row, after three earlier tries that day had all hung. They took 35 to 71 s, four of them after `configd` crashed twice | 20 of 20, in 26 to 34 s, no crash |
| The 2026-10-06 build's 10.6.8 disk (10K549), the one that hung | 7 of 10 answered, in 24 to 48 s | 20 of 20, in 16 or 17 s, no crash |
| 10.9.5 box (13F1911), built 2026-10-07 | 10 of 10, in 64 or 65 s, each publishing the bogus `PriorBootSleepFailure` | 10 of 10, in 64 or 65 s, without it |

Unpatched, every guest had RTC length 8, `PMStatusCode` all ones and
the sleep failure in its log; patched, every one had length 2, no
`PMStatusCode` and no such line. Every 10.6 crash report has the same
stack, `CFGetTypeID` called from the plugin at offset `0x2aea`, the
instruction after the unchecked `CFArrayGetValueAtIndex`; the 10.6.8 disk
carries the same plugin, 160.0.0.

The 10.6.8 disk's own `system.log` says what happened to the build. Its
first boot (10.6.0, which installs the update) lost `configd` twice and
went on; its second boot, the one the build waited an hour on, lost it
364 times. A stock boot of the same disk today that hung lost it 24
times in 300 s.

Under TCG, the 10.6.0 box's unpatched boots lost `configd` 8 and 9 times
before one survived. Setting the guest clock back to 2026-10-03, before
the hangs began, changed nothing.

Rebooted with `sudo reboot` until SSH did not answer within 300 s, then
on a fresh copy, all patched, 2026-10-10 and 11:

| Guest | Reboots | Came back |
|---|---|---|
| The 10.6.8 box from build `snowleopard-1` below | 30 | 30, in 30 to 48 s, no crash |
| 10.6.0 box | 70 | 52; 18 sat at the login window with no sshd |

The 10.6.0 failures are 10.6.0's own and are not this patch's: unpatched,
where `configd`'s crashes come on top, the same box came back from 3 of
14 reboots, and 12 of 27 cold boots answered at all. In each patched failure, `launchctl list com.openssh.sshd` showed the
job loaded with no `Listeners` from the first sample of the boot, so
nothing listened on port 22; nothing was logged, on the console or
elsewhere, and `getaddrinfo(NULL, "ssh", AI_PASSIVE)` succeeded when asked
later in the same boot. 10.6.8 did not do it once in 30. No box ships
10.6.0.

Built from scratch with the patch, one at a time, at the time of this
decision:

| Build | Result |
|---|---|
| `snowleopard-1`, `snowleopard-2` | passed, in 1022 and 985 s |
| `snowleopard-3` | timed out waiting for SSH |
| `mavericks-1` | passed, in 1470 s |

`snowleopard-3` failed differently from every hang above. Restarting
after the 10.6.8 update, 10.6.0's kernel printed `MACH Reboot` and
stopped there, spinning one CPU, until the build gave up. 10.6.0's
`AppleACPIPlatformExpert::performSystemReset` writes the FADT's reset
register (`0xCF9` on q35) and then loops forever, waiting for the
machine to reset. That path does not touch the RTC, and the 70 reboots
above all went through it and reset. ADR 0030 finds the reset was not
lost (MEASURED: QEMU resets on that write every time, and xnu writes it
again 30 s later if the first is lost). The firmware then most likely
hung reading leftover RAM as IGVM parameters, as ADR 0018 found for
10.9, leaving `MACH Reboot` on the screen (REASONED: that build's guest
was not dumped). Patch 0005 removes that cause. The item is closed
by ADR 0030, which the owner accepted (2026-10-25).

## Alternatives considered

- **`RTCMemoryFixup`** (Acidanthera) emulates chosen CMOS offsets for
  `AppleRTC`. It needs a kext and a list of offsets, and treats a symptom
  of the declaration this patch corrects.
- **Patching 10.6's PowerManagement plugin** to bound the loop: Apple's
  bytes, which this project does not ship (`bin/no-apple-bytes.sh`), and
  10.9 would still publish its bogus record.
- **10.6 only.** The patch would mend 10.6's crash, and leave 10.9 reading
  a second bank that is not there and reporting a sleep failure on every
  boot. Both releases see the same QEMU.

## What would change this

- A QEMU that answers ports `0x72`/`0x73` with a second bank: the patch
  would then hide a bank that exists.
- A QEMU whose DSDT already says two ports, or describes the RTC in other
  bytes: OpenCore finds nothing to replace, and the patch silently does
  nothing. A guest that again reports `Sleep failure code 0xffffffff
  0xffffffff` is the sign.
