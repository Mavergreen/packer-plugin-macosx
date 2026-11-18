# 0046: 10.6.8 waits out its lock timeouts under a hypervisor, as 10.7 does

Date: 2026-11-18

Status: accepted (the owner's ruling R74, 2026-11-18), on measurement

## Context

F26: 10.6.8 at two vCPUs on a busy host panics, at boot and after warm
restarts, with `Spinlock acquisition timed out` on the kernel pmap's lock,
raised from `pmap_enter`. That is why `cpus` defaulted to 1 on 10.6
(`docs/decisions/0014`).

The reliability work found the mechanism (MEASURED on `pet-power-plant`,
KVM, 2026-11-17 and 18):

- The lock holder is in `pmap_flush_tlbs`, waiting for the other CPU to
  acknowledge a TLB shootdown. `pmap_remove` and `pmap_protect` do one
  synchronous shootdown per 2 MB chunk, all under the pmap lock, and drop
  and retake it within two instructions, so the spinner never gets in.
- The spinner has interrupts on and acknowledges every shootdown it is
  sent, 20 to 202 of them in one stall. Its single 250 ms deadline (`hw_lock_to`, measured in
  TSC ticks) covers the whole series.
- On hardware a round trip takes microseconds. Here it takes 2 to 14 ms,
  because each half needs the other vCPU's host thread to be running. The
  host often stacks both vCPU threads on one CPU. With no competing load
  on their CPUs, long holds almost vanish.

So nothing is deadlocked. The guest kernel assumes that a remote CPU
answers within microseconds of wall-clock time, and a time-sliced vCPU
breaks that assumption. A longer `slto_us` only moved the edge (1 panic in
50 restarts at 1 s), and the owner ruled it out.

Apple met the same problem in the next release. xnu-1504.15.3 (10.6.8)
has:

```c
boolean_t machine_timeout_suspended(void) {
	return (mp_recent_debugger_activity() || panic_active() || pmap_tlb_flush_timeout || spinlock_timed_out);
}
```

xnu-1699.32.7 (10.7.5) sets `virtualized = ((cpuid_features() &
CPUID_FEATURE_VMM) != 0)` in `ml_init_lock_timeout()` and returns
`(virtualized || ...)`. xnu-2050 (10.8) and xnu-2422.115.4 (10.9.5) do the
same. Our 10.9 guests have run that way all along. Under a hypervisor
every caller keeps waiting where it would have panicked.

## Decision

`snowleopard/config.plist` carries one `Kernel > Patch`, which gives
10.6.8's `machine_timeout_suspended()` the `virtualized` term.

The patch:

- **Matches** `Identifier` `kernel`, `Arch` `x86_64`, `Base`
  `_machine_timeout_suspended`, `MinKernel` = `MaxKernel` = `10.8.0`
  (Darwin 10.8.0, xnu-1504.15.3, the only 10.6.8 kernel), `Count` 1.
- **Find** is the whole stock function, 63 bytes. They occur once in the
  kernel.
- **Replace** is the same function rewritten in place, at the same
  length. It first tests `CPUID.1:ECX[31]` (the hypervisor-present bit,
  `CPUID_FEATURE_VMM`), read from the kernel's own cached feature word
  (`cpuid_cpu_info.cpuid_features`, bit 63, the word
  `cpuid_features()` returns), the way Apple's term reads it. Then come
  the four stock terms, with the same call and data targets; the cheaper
  memory reads replace `lea` plus `mov`, to make room. It returns TRUE when
  any of the five is true.
- **The bytes** were derived from the binary (`/mach_kernel`, sha256
  `99d81722…`), not from the source. Every displacement was checked
  against the stock function's own targets, and `internal/firmware`'s
  `TestSnowLeopardSuspendsLockTimeoutsUnderAHypervisor` pins them.

It is inline: nothing inlines `machine_timeout_suspended` (READ: 43
`call`s to it, no other code reads both `pmap_tlb_flush_timeout` and
`spinlock_timed_out`). So the one function covers every caller.

What each caller does under a hypervisor now (READ; these are the
callers 10.7 to 10.9 have too):

- `usimple_lock`: re-enables preemption and spins again, instead of an
  NMI and `Spinlock acquisition timed out`;
- `pmap_flush_tlbs`: keeps waiting for the acknowledgements, instead of
  `TLB invalidation IPI timeout`;
- `wait_queue_lock` (inlined at about 38 sites in IPC, wait-queue and
  semaphore code): keeps spinning, instead of `wait queue deadlock`;
- `clear_wait_internal`: keeps retrying, instead of `clear_wait_internal:
  deadlock`;
- `interrupt()`: its latency panic needs the
  `-interrupt_latency_assert_enable` boot argument, which we never set.

The term detects its own need. On real hardware the VMM bit is clear, the
term is false, and the function behaves as stock. On another kernel the
`MinKernel`/`MaxKernel` range or the `Find` bytes do not match, and
OpenCore changes nothing.

## Not covered: 10.6.0

10.6.0 (`updates = none`, and the installer) keeps its stock kernel:

- Its kernel (xnu-1456.1.25, the retail disc) has no
  `machine_timeout_suspended()` at all (READ, from the disc's
  `mach_kernel`).
- Its `usimple_lock` inlines `while (mp_recent_debugger_activity() &&
  !hw_lock_to(...))`. That retry calls `hw_lock_to` again without the
  `enable_preemption()` 10.6.8 added, and `hw_lock_to` returns from a
  timeout with preemption still disabled. Forcing that loop on would leak
  a preemption level on every retry (READ, from its x86_64 code).

So there is no change in Apple's own shape for 10.6.0. Its panics at two
vCPUs (spinlock, double fault, `thread_invoke: preemption_level`) stay as
`docs/decisions/0014` records them. 10.6.0 stays at one CPU: the owner
ruled out a separate 10.6.0 patch (R75, 2026-11-18). So does every
build, which boots 10.6.0's kernel before the updates go in.

## Cost

As on 10.7 through 10.9, a real deadlock under a hypervisor now hangs
instead of panicking. A lock that is never released leaves its waiter
spinning, with no panic screen to read. On the evidence above, the stalls
seen are slow, not stuck: every one ended.

## Measurements

MEASURED 2026-11-18, `pet-power-plant` (i7-8700B, 6 cores and 12
threads, KVM). One 10.6.8 box was built from main (`de74746`, every
variable default). It was booted with two OpenCore images that differ
only in this patch: A, the box's own, and V, its `config.plist` with the
patch.

Every booted guest's facts record the function's bytes, read from guest
memory:

- A: the stock bytes, 64 of 64 boots that answered;
- V: the patched bytes, 65 of 65;
- `machdep.cpu.features` ends `... xAPIC VMM` in every boot.

So OpenCore applied the patch to the kernel that booted, and the guest
sees the VMM bit under QEMU's `-cpu Penryn` on KVM. On hvf and nvmm
nothing has been measured.

The load was two vCPUs and 16 busy loops on the host (1-minute load 18
to 20). The arms alternated in blocks of 10 warm restarts or 5 cold
boots, 14 blocks in 3 h. Three restart blocks per arm ran with the F26b
sampler and two without:

| | A (stock) | V (patched) |
|---|---|---|
| warm restarts failed | 4 / 50 (3.2–18.8 %) | 0 / 50 (0–7.1 %) |
| dedicated cold boots failed | 1 / 10 | 0 / 10 |
| all cold boots (fresh QEMUs) failed | 2 / 20 | 0 / 15 |
| every boot failed | 6 / 70 (4.0–17.5 %) | 0 / 65 (0–5.6 %) |

The failed boots:

- Fisher's exact test on every boot gives p = 0.028. Restarts alone give
  p = 0.12.
- All six A failures were the F26 panic: `Spinlock acquisition timed out`
  on lock `0xffffff800087e920`, from `pmap_enter+0x145`.

The stalls themselves did not go away (sampled guest time):

- Lock holds of 100 ms or more:
  - A: 32.7 per 10 min. That is diluted by the time its panicked guests
    sat frozen; its one sampled block with no panic ran at 62.4.
  - V: 53.4 per 10 min.
- Holds of 250 ms or more: A 39, V 40.
- Longest hold: A 1.58 s, V 1.19 s.

So the patch does not shorten the stalls. It survives them. Three V
stalls caught a vCPU spinning in `hw_lock_to` called from
`usimple_lock`'s retry loop: the patched path, taken after the 250 ms
deadline had passed. Each stall ended (0.42 to 0.70 s holds), and the
boot went on.

Boots took no longer:

- Time to the SSH greeting under that load: A median 51 s (restarts) and
  52 s (cold), V 52 s and 52 s, longest 60 s in V and 67 s in A.
- On an idle host, cold boots, with the probe's few-second resolution:
  at one vCPU (the template's default), A and V both answered 10 of 10,
  median 28 s (21 to 29); at two vCPUs the same, 10 of 10 each, median
  28 s.

## Consequences

- The owner's ruling R75 (2026-11-18): a 10.6.8 box (`updates =
  security`, the default) boots two CPUs by default; a 10.6.0 box
  (`updates = none`) one; every build one, since it boots 10.6.0's
  kernel first (`docs/decisions/0014` item 8). `cpus` is now the box's
  count alone, unset by default. For 10.6.8 at two vCPUs, the panic F26
  measured is gone, with the stalls left behind it.
- `docs/decisions/0014` (item 8 and its measurements), the `cpus`
  description in `templates/snowleopard/variables.pkr.hcl`,
  `docs/configuration-register.md` and `assets/firmware/README.md` say so.
- When 10.6 gets another kernel (none is expected), the patch finds
  nothing and does nothing, and the test still pins this one.
