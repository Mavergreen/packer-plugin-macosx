# 0017: The boot gate

Date: 2026-11-19

Status: accepted (the design, approved by the owner 2026-10-12; its
local half has run since 2026-10-13). The CI half, `gate.yml`, landed with
this decision and was proven on GitHub's runners on 2026-11-20 ("Measured
on GitHub", below). A real Renovate PR (#10, VirtualSMC) ran it on
2026-11-22, and `gate` became a required check on main the same day; Aqua
Transport bumps automerge behind it from then on. Amended 2026-11-23: 10.6
is gated in CI too, and the boot stack automerges behind `gate` ("Amended
2026-11-23", below).

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
- On GitHub (`gate.yml`), on GitHub's hosted KVM, once for each release
  the change touches (`gate/scope.tsv`; amended 2026-11-23, below):
  - on Renovate's PRs that move a `gate:<releases>` row of
    `gate/scope.tsv`, booting only the releases that row names
    (`assets/pins/sources.tsv`: 10.9 and 10.6;
    `assets/firmware/config.plist`: 10.9;
    `assets/firmware/snowleopard/config.plist`: 10.6; Aqua Transport's
    pin: 10.9); its paths are exactly
    `repackage-on-ingredient-bump.yml`'s, each `skip` row saying why;
  - before every release, a person's and the repackage's alike:
    `release.yml`'s job `release gate` calls `gate.yml` between `build`
    and `publish`, and `publish` needs it, so nothing publishes unless the
    exact commit being released passes. It is not named `gate`: it is
    skipped on every run that does not publish, and a skipped check would
    satisfy the required one. Not on a push to main that is not a
    release: a deviation from the family's run-the-build-on-every-push,
    because this repo publishes only on request or on an ingredient move
    (`docs/decisions/0012`), so gating the publish already means nothing
    ships ungated;
  - by a `release.yml` dispatch with `gate_only`, on any ref: the gate
    without publishing (`gate_only` wins over `local_release`), which is
    how a branch proves it on GitHub's runners before `gate.yml` is on
    main (GitHub dispatches a workflow only from the default branch's
    file);
  - by dispatching `gate.yml` itself, with `force` to gate whatever
    changed.
  Its one required check is named `gate`, and runs whatever happened before
  it: a skipped check would count as passing a required one of its name.

### What it does not cover

- **Aqua Transport on 10.6**: it has no 10.6 half (ADR 0016), so its
  bumps gate 10.9 alone.
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

On `ubuntu-latest` with KVM, through release.yml's `gate_only`, before
this ADR's PR merged:

- 2026-11-20, 10.9, run 44690297252 on the merged commits: Passed, every
  step, 10 restarts. The candidate job took 49 min 27 s: plugin 22 s, base
  1,903, layer 478, varstore 3, boot 32, host-uuid 25, restarts 33 to 46,
  shutdown 26.
- 2026-11-19, run 44601148068, an earlier build of the same commits: the
  same steps passed (base 2,195 s, layer 516, job 54 min 55 s). But the
  verdict read "no steps.tsv": `download-artifact` given a pattern that
  matches one artifact extracts it flat into `path`, not into
  `path/<name>`. The final job now downloads each artifact by name into
  its own directory.
- 2026-11-19, the broken bump: run 44601275729 had VirtualSMC set to
  `Enabled=false`. Its candidate never answered SSH and packer gave up
  after an hour. The control, built from the last release, passed.
  `gate` went red with `Regression: build-base -- packer failed after QEMU
  started: Timeout waiting for SSH.`
- Disk: preflight saw 89 GiB free in `/mnt/gate`. The gate does not
  record the runner's peak use, so it is not measured here. The local
  cold peak is 44 to 49 GiB (see the 60 GiB floor in `bin/gate.sh`).

## Amended 2026-11-23: 10.6 in CI

Status: accepted, on the owner's rulings of 2026-11-23.

What changed: `gate.yml` runs `gate-release.yml` once for each release a
change touches, `gate/scope.tsv` names them (`gate:<releases>`), and the
required check `gate` gives one verdict over all of them. 10.6 is gated
here like 10.9, so the owner's ruling R59 (a boot-stack change is gated on
10.6 before it merges) is met in CI, and the boot stack's Renovate rules
(OpenCore, Lilu, VirtualSMC) automerge behind `gate`. This replaces "10.6
in CI: CI has no 10.6 disc" under "What it does not cover".

Why, the owner's rulings:

- Parity with 10.9: every gated run installs 10.6 from the disc. No cached
  media, no persisted VM.
- The disc's URL is pinned in the repository, as `snowleopard-disc-10A432`
  in `assets/pins/sources.tsv`, the way shipyard pins 10.9's SDK from
  phracker/MacOSX-SDKs. The project enables fetching from where the disc
  already is (archive.org) and does not redistribute it. `bin/gate-ci.sh`
  fetches it with `cmd/fetch-pinned`, and the bytes are checked against the
  pinned sha256 and, by `internal/disc.Verify`, by content.
- The fetch is a parallel ranged download, 64 MiB pieces, 8 at a time.
  Measured on a GitHub runner: 5.6 GB in 69 s, against about 13 minutes
  for a single stream.

What does not change: the plugin itself still takes the user's own disc
(`installer`; ADR 0014, item 1). Only CI fetches the pinned copy.
