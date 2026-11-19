# 0017: The boot gate

Date: 2026-11-19

Status: accepted (the design, approved by the owner 2026-10-12; its
local half has run since 2026-10-13). The CI half, `gate.yml`, lands with
this decision and is proven on GitHub's runners before it is required
("Measured on GitHub", below, is filled in then).

## Context

This plugin's ingredients move without a person: Renovate bumps
OpenCorePkg, Lilu, VirtualSMC and Aqua Transport, and an ingredient that
moves on main cuts a release unattended
(`repackage-on-ingredient-bump.yml`), which mavericks-vm then takes up. Until
now nothing in CI built or booted a guest, so those bumps could not be
merged without a person building and booting one, and the month's two
worst bugs were intermittent (a hang about 1 boot in 4, another 1 build
in 3): one boot proves little.

## Decision

One command, `bin/gate.sh`, builds a box from the checkout, with the plugin
built from the checkout, boots it, checks it from inside, restarts it N
times re-checking each time, and shuts it down. CI runs the same command
(`.github/workflows/gate.yml`); it does not reimplement it. Every step
writes a row of `steps.tsv` (step, result, seconds, note), and
`bin/gate-summary.sh` turns one run, or a candidate and a control, into a
verdict whose first line a person reads.

### The steps

In this order, always all of them: `preflight plugin build-base
build-layer varstore boot versions health reach host-uuid restart-1` …
`restart-N shutdown`. A result is `pass`, `fail` (the check ran and
failed), `infra` (it could not run its check: a download, the disk, a
tool), `skip` (every row after the first that is neither pass nor n/a), or
`n/a`.

- `build-base`, `build-layer`: from scratch, never a cached image, never a
  bootloader swapped into an old one.
- `varstore`: the firmware programs only the flash bytes that change, on a
  nearly full store (`docs/decisions/0032`).
- `versions`: what the guest runs is the checkout's pins: OpenCore's
  version from NVRAM, Lilu's and VirtualSMC's from `kextstat`, Aqua
  Transport's from `pkgutil`.
- `health`: 10.9.5 (13F1911); no crash, hang, panic, spin, diag or
  shutdown-stall report from the build on, but those
  `gate/crash-allowlist.tsv` names, each with its reason and the owner's
  approval (it is empty).
- `reach`: the guest's own curl fetches github.com,
  release-assets.githubusercontent.com and downloads.claude.ai.
- `host-uuid`: the firmware names the host's UUID (`docs/decisions/0024`).
- `restart-1`…`restart-N`: each a real restart (`kern.boottime` moves),
  SSH back and the checks again.
- `shutdown`: the guest powers off when asked, within a bound.

A step is `n/a` only where the release has nothing for it to check, with
the reason as its note: on 10.6, `build-layer` and `reach`
(`docs/decisions/0014`, item 9).

### The verdicts

The first line of the summary is one of (`bin/gate-summary.sh`):

    Passed: every step, <N> restarts
    Passed: every step that applies, <N> restarts; not applicable: <step>, ...
    Regression: <step> -- <note>
    No clear regression: CI itself failed at <step> -- <note>
    No clear regression: main fails it too -- the control failed <step> -- <note>
    No clear regression: the control could not run -- <step> -- <note>
    Failed: <step> -- <note> (no control run; run bin/gate.sh on main to compare)

`Regression` needs a control: when the candidate fails a check, the same
gate runs on the control's tree, and only a control that passed that step
makes it the candidate's. The last line is for a local run with no control;
CI always names a control, so it never reports it. CI adds three of its
own (`bin/gate-ci.sh`): `Not gated: <why>` (passes),
`Pins not filled yet: <what to do>` (fails), and
`No clear regression: CI itself failed at <job> -- no steps.tsv (job <result>, gate.sh exit <n>)`
when a run left nothing to read (a cancelled or timed-out job, a usage
error).

### N = 10

Ten restarts miss a failure of one boot in four 0.75^10 ≈ 5.6% of the time,
at about 40 s a restart (README, "Check a change"). A run that could not run
its check is retried once, on a fresh runner; a check that ran and failed is
never retried: a failing verdict is the verdict.

### The evidence

`steps.tsv`, a log per step and `summary.md`; on any failure also the
guest's screen, its serial log, its crash, hang, panic, spin, diag and
stall reports, and the end of its system log; on a build that fails after
QEMU started, what `bin/gate-build-evidence.sh` reads from it
(`docs/decisions/0040`). Never an image, a box, a disk, a DMG or Apple's
media: the evidence dir may not be, contain or sit inside the work dir or
the packer cache, and CI uploads only the evidence dir, kept 30 days.

### Where it runs

- Locally, on both releases by default (the owner's ruling R52: the boot
  stack is both releases', so a change to it is gated on both, and
  skipping 10.6 is never silent). KVM is the only tested accelerator;
  `--accel hvf` and `nvmm` are accepted and say they are untested.
- On GitHub (`gate.yml`), `--release 10.9` only, on GitHub's hosted KVM:
  - on Renovate's PRs that move a `gate` row of `gate/scope.tsv`
    (`assets/pins/sources.tsv`, `assets/firmware/config.plist`, Aqua
    Transport's pin); its paths are exactly
    `repackage-on-ingredient-bump.yml`'s, each `skip` row saying why;
  - before every release, a person's and the repackage's alike
    (`release.yml` calls `gate.yml`; wired in the change after this one);
  - by dispatch, with `force` to gate whatever changed.
  Its one required check is named `gate`, and runs whatever happened before
  it: a skipped check would count as passing a required one of its name.

### What it does not cover

- **10.6 in CI**: CI has no 10.6 disc. A boot-stack change gets a local
  `bin/gate.sh --release 10.6 --installer <disc>` before it merges (the
  owner's ruling R59), so those PRs do not automerge.
- **HVF and NVMM**: accepted, untested.
- **PRs that are not Renovate's**: whoever lands one runs the gate locally
  first; so does a push to main that touches an ingredient path, since it
  cuts a release by itself.
- **OpenSSH bumps** (`components/**`): OpenSSH has its own on-VM smoke test
  before it releases (the owner, 2026-10-12); the release they trigger is
  gated.
- **A setup step that fails before the gate can run** (the checkout,
  shipyard's install, setup-go, setup-packer): it is not retried, and reads
  `No clear regression: CI itself failed at candidate -- no steps.tsv (job
  failure, gate.sh exit none)`, red and named. Only readying the runner
  (`bin/gate-host-setup.sh`: apt, a missing module) is retried on a fresh
  runner, as the faults on record there were one runner's. The others fail
  before this tree's scripts can say anything, and an outage of GitHub or a
  download site is not one runner's, so a retry would mostly repeat it.
- **Failure A** (`docs/decisions/0040`): an installer that stalls in a cold
  10.9 base build, about 2% of builds on EPYC 7763 runners, reads as
  `build-base fail` ("Timeout waiting for SSH"). It is not retried or
  reclassified, which would hide it: the verdict then rests on the control,
  and about one gated run in fifty goes red as a `Regression: build-base`
  when the control is lucky. A person reads it; nothing merges or publishes
  on it.

## Measured

- First baseline, 2026-10-13 (KVM, 12 CPUs, cold): 42 min 11 s, peak
  37 GiB used in the work dir's filesystem. It found shutdown stalls and a
  warm-restart firmware hang, which the reliability work then root-caused
  and fixed before the gate went further.
- 10.9 alone, N = 10, 2026-11-01, N 5: 2,675 to 3,265 s; the base build
  1,852 to 2,431 s, the layer 336 to 348 s, each restart about 40 s
  (README). Both releases, N 1: 5,425 s.
- 2026-11-08: the 10.9 gate passed every step with 10 restarts on
  OpenSSH 10.6p1-mavericks.5 (`docs/configuration-register.md`).
- 2026-11-18, 10.9, N = 2: base 1,520 s, layer 347, varstore 2, boot 31,
  host-uuid 19, restarts 35 and 43, shutdown 24.

### Measured on GitHub

Not yet: the first CI runs fill in wall time, each step's seconds, the
runner's peak disk use, and a deliberately broken bump's verdict.
