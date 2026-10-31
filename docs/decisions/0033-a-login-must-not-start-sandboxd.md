# 0033 — a login must not start sandboxd: the family OpenSSH compiles sshd-auth's sandbox in process

Date: 2026-10-26
Status: accepted (F14; the owner, 2026-10-26: R43 the openssh fix, R44 no release before the pin bump; the pin moved to 10.6p1-mavericks.3 on 2026-10-27)

## Decision

1. **The root fix is in Mavergreen/openssh, not here.** Its `sshd-auth`
   applies upstream's Darwin sandbox, `sandbox_init(kSBXProfilePureComputation,
   SANDBOX_NAMED)`. On 10.9 that builtin is fetched from `sandboxd` by the
   kernel, so launchd starts `sandboxd` on demand at the first login of
   every boot. A new patch there, `sandbox-darwin-in-process.patch`
   (`-D__APPLE_SANDBOX_IN_PROCESS__`), applies a sandbox compiled in
   process from a string, `(version 1) (deny default)`, which needs no
   `sandboxd`. Before the chroot it loads libsandbox, which
   `sandbox_init()` needs after it, and a failure to load it is fatal.
   There is no fallback to the builtin: the builtin needs libsandbox after
   the chroot too, so wherever the load fails the builtin fails as well
   (MEASURED; see Limits). Its test is check 4 of that repository's
   `tests/apple-patches-mavericks.sh`, which fails when a login applies the
   builtin or leaves `sandboxd` running.
2. **This repository bumps `components/openssh/version` to the release
   that carries it, and releases nothing until then.** No release of this
   plugin is cut while the pin names an OpenSSH without
   `sandbox-darwin-in-process.patch`: one would ship a known, measured
   19 s delay. The bump is this gate's exit. It touches
   `components/openssh/version`; the openssh rows of
   `internal/inputs/testdata/golden/listing-payload.txt` and
   `listing-media.txt`; `internal/pins/testdata/golden/ingredients.txt`;
   the ingredient digest (`internal/pins/testdata/golden/ingredients-digest.txt`
   and `internal/pins/pins_test.go`); the register's `openssh` row and ADR
   0021's note, which then say it has landed, with the version; and the
   next release note. Until the bump, nothing here works around it:
   `guest-macosx-shutdown` (ADR 0021) keeps waiting for every young job,
   `sandboxd` included. See Alternatives.
3. **The helper names what held its wait.** "waited N s for jobs still
   starting: …" lists at most eight names, in order, so the job that held
   the wait longest was often hidden behind "and 62 more". It now ends
   "; the last to settle: …" with the jobs still young at its last look,
   unless that was every job it waited for, or `ps` failed mid-wait,
   which leaves the jobs it saw last unsettled
   (`TestTheShutdownHelperNamesTheLastJobsToSettle`,
   `TestTheShutdownHelperNamesNoLastJobWhenAllSettleTogether`,
   `TestTheShutdownHelperNamesNoLastJobWhenPsFailsLater`).

## Context: F14

A halt or restart whose SSH connection is the boot's first takes about
20 s longer than one after an earlier login.

| 10.9.5 base box (HEAD 74808f9, OpenSSH 10.6p1-mavericks.2); host load 0.4-2.5 (1 min, 12 CPUs) | N | helper's first wait | command to QEMU exit / command returns |
|---|---|---|---|
| halt, 90 s after a cold start, the boot's first connection | 20 | 19.0-19.6 s for `sandboxd` in 19; none in 1 | 20.4-21.0 s (19); 1.4 s (1) |
| halt, the same, one login 30 s earlier | 20 | none, 20 of 20 | 1.3-1.5 s |
| warm restart, 90 s after the last, the boot's first connection | 20 | 19.1-20.1 s for `sandboxd`, 20 of 20 | 19.5-20.6 s |
| warm restart, the same, one login 30 s earlier | 20 | none, 20 of 20 | 0.4-0.5 s |

MEASURED, unless marked:

- **Who starts it.** `sandboxd` is a launchd job with `OnDemand`,
  `EnableTransactions` and one MachService, `com.apple.sandboxd`, which is
  host special port 14: the kernel's port to it. In 41 of 41 boots sampled
  every 0.5 s it first appeared, a child of launchd, in a sample that held
  the boot's first `launchproxy /usr/sbin/sshd -i`, which in 35 of them
  the sample before did not.
- **Why.** In a test process, `sandbox_init(kSBXProfilePureComputation,
  SANDBOX_NAMED)` started `sandboxd` every time (5 of 5, `sandboxd` killed
  before each); the same process skipping the call, 0 of 1. dtrace put
  `sandboxd`'s exec inside that process's `__mac_syscall("Sandbox", 1)`,
  which took 8 ms. In a harness built from the patched `sandbox-darwin.c`
  that runs `sshd-auth`'s order (init, chroot, drop to `_sshd`, sandbox,
  rlimits), the builtin started `sandboxd` 3 of 3 times and the
  in-process string 0 of 3. That the kernel fetches the builtin profile
  from `sandboxd` is REASONED: Sandbox.kext carries "Sandbox builtin
  lookup failed (get_sandboxd_port)", and the timing fits. Not every
  profile does it.
  Applied by a process that then exited at once, these did not start it:
  the builtin `no-internet` (0 of 2), Apple's `org.openssh.sshd.sb` with
  `SANDBOX_NAMED_EXTERNAL` (0 of 4), and a profile string (0 of 8, 4 of
  them after a chroot).
- **Why the helper waits.** It waits until no job launchd started is under
  20 s old, whatever its name (ADR 0021), and `sandboxd` is about 1 s old
  when the helper starts. It is "started" only at 20 s.
- **Why the wait buys nothing.** `proc_get_dirty` read `sandboxd` as
  tracked, allowed to idle-exit and clean (flags 3) in every sample of
  every boot, bar a 0 in its first sample in 3 of 41. launchd's shutdown
  log: "Sent job SIGKILL. Job was killed cleanly." for it in 40 of 40
  shutdowns of the restart run, and in 10 of 10 restarts made with the
  test limit at 1 s, which met `sandboxd` 1-2 s old ("Userspace shutdown
  took approximately 0 seconds", no stall, no report). It did not
  idle-exit (watched to 94 s old), so later logins do not pay.

Who pays (REASONED from the above): any halt or restart that comes
within 20 s of the boot's first login, its own connection included: a
`vagrant ssh -c 'sudo guest-macosx-shutdown -h'` of a guest nobody has
logged in to since it booted, say, or a build's or the gate's restart
soon after a boot's first login.

## The fix, measured

The patch cannot be built on the investigation's host (Mavergreen/openssh
builds only on macOS). So its behaviour was emulated in the guest: a
dylib, `DYLD_INSERT_LIBRARIES` in a probe overlay's `ssh.plist` only, that
loads libsandbox when `sshd-auth` starts (before its chroot) and turns its
`sandbox_init(kSBXProfilePureComputation, SANDBOX_NAMED)` into
`sandbox_init("(version 1) (deny default)", 0)`, logging each.

| The same box with the emulated change; host load 0.3-3.3 | N | helper's first wait | command to QEMU exit / returns |
|---|---|---|---|
| halt, first connection | 20 | none, 20 of 20 | 1.4-1.6 s |
| warm restart, first connection | 20 | none in 19; 19.5 s for `dirhelper` in 1 (below) | 0.5-0.7 s (19) |
| warm restart, one login 30 s earlier | 20 | none, 20 of 20 | 0.4-0.5 s |

- Every login of the restart run went through the in-process profile (62
  logged) and every one worked. No `sandboxd` ran and nothing logged a
  denial, so `sshd-auth` does nothing the in-process profile refuses.
- Through the same emulation in a `-ddd` test `sshd` with `UsePAM=yes`,
  `ssh -v` logins by public key, password and keyboard-interactive (PAM)
  each worked 3 of 3, logged no denial and left no `sandboxd`; the
  builtin's control login started it (1 of 1). A wrong password failed
  cleanly.
- The one wait: the first login ever through the new profile made
  dirhelper create `_sshd`'s per-user directory
  (`/var/folders/zz/…n000009c00002b`, absent from the stock image). It
  happened once, on the image's first such login, and never after. A
  box built with the fixed OpenSSH makes that directory in its build,
  whose provisioners log in long before the build's last shutdown
  (REASONED).

## Not this: the gate's occasional 20 s waits

At the gate's pace (F4's loop: three logins as soon as sshd answers, then
the restart 10 s later), the first of those logins starts `sandboxd`, which
is 11-12 s old when the helper looks: as old as the boot's last wave, so
it does not hold the wait. 15 of 200 such restarts waited 18.6-20.5 s
instead of 10.8-12.4 s (host load 0.6-1.8). The helper with this
decision's "last to settle" named them in its own series:

| Jobs that held a 20 s wait | stock helper (N 100) | this helper (N 100) |
|---|---|---|
| `netbiosd`, respawned 10 s after it "Exited abnormally: Hangup: 1" early in the boot | 4 | 2 |
| `ManagedClient` (the daemon), respawned 10 s after "Check-in of Mach service failed. Already active: com.apple.ManagedClient.agent" and exit 7 | 1 seen, 3 started after the look and not named | 5 |

Both are jobs that died in their first second and that launchd started
again after its 10 s throttle: really starting when the helper looks, so
its wait is doing its job. Why each dies is not established here (F14
report, new findings).

## Alternatives

- **Exempt `sandboxd` in the helper.** It is clean and launchd kills it at
  once (measured). Rejected: it is an allowlist by name. ADR 0021 waits
  for every young job because names were what it could not trust, and the
  next on-demand job a login starts would need the same.
- **Wait only for young jobs that are dirty or untracked.** A clean job is
  SIGKILLed, so it cannot hold the shutdown at that instant. Rejected: a
  young job can turn dirty after the look. AppleIDAuthAgent is clean from
  about 3 s after it starts until its account check at 10-15 s, which
  holds a preference write for 3-4 s (ADR 0021).
- **Start `sandboxd` at boot** (a job that applies the builtin once). Rejected:
  another young job in the boot wave, to hide one the login makes.
- **Apple's own `__APPLE_SANDBOX_NAMED_EXTERNAL__`** (OpenSSH-186/189, 10.9:
  `sandbox_init("/System/Library/Sandbox/Profiles/org.openssh.sshd.sb",
  SANDBOX_NAMED_EXTERNAL)`). The family build passes that define, but
  upstream has no such `#ifdef`, so it does nothing. Brought back, it
  fails every login: `sshd-auth` has chrooted to `/var/empty` before it
  applies its sandbox, and the file is not there ("No such file or
  directory", MEASURED).
- **No sandbox, or `--with-sandbox=rlimit`.** Rejected: weaker, for a delay.

## Packaged fix in this plugin's guest (MEASURED 2026-10-29)

Five fresh cold-boot overlays of the plugin's freshly built 10.9.5
(13F1911) Aqua Transport 2026.10.25 layer, KVM, two CPUs. The measurement
launcher waited for a TCP SSH banner, not an authenticated `true`, so the
first authenticated connection both checked the installed package and
delivered `guest-macosx-shutdown -h`. All five found no running
`/usr/libexec/sandboxd` immediately before the halt; none logged
`waited ... sandboxd`; all five reached clean QEMU exit. No failed guest
trial, retry, timeout increase or added allowlist.

Both `ssh -V` and `sshd -V` print `OpenSSH_10.6p1, LibreSSL 4.4.0`, which
does not distinguish package .2 from .3. The installed receipt, including
`pkgutil --file-info` for `sshd-auth`, identifies `dev.mavergreen.openssh`
version **10.6p1-mavericks.3** in all five guests. The auth binary's
SHA-256 is `b4ac816fdff0ce40ee3c6830105e152c37cf8d3ad0d1edb2644176eb9d6ffc98`.
This verifies the package and behavior, not a package suffix in the SSH
banner; the latter is not printed.

First authenticated command to QEMU exit: 21, 21, 22, 20 and 22 s. The
helper still waited 18.5-19.6 s for other newly started jobs, last to settle
**diskmanagementd** in every trial. This confirms no named sandboxd wait,
not that all first-login halts are now quick. Sandboxd was checked at one
point before each halt, not continuously traced throughout the boot.

Retained local text evidence: `.superpowers/findings/2026-10-13-vm-reliability/`
`f14/resume-first-login/`, with setup, binary hashes and limits in
`f14-resume-report.md`. A host launcher permission error occurred before
any VM boot and was corrected; its separate evidence is retained rather
than counted as a guest trial. No F27/F28/F29 proposal was in these guests.
This does not replace a final integrated build after those decisions.

10.6 keeps Apple's OpenSSH 5.2, not this package (the release difference
below remains deliberate). A separate stock 10.6.8 one-CPU check on the
same date passed ten warm restarts, health checks and final shutdown; it
was a parity check, not a new paired sandbox experiment.

## Limits

- `(version 1) (deny default)` gave the same answer as the builtin on all
  20 operations probed in a 10.9.5 guest (file reads and writes,
  `sysctl`, `mach-lookup`, `socket()`, `connect`, `fork`, signals, I/O on
  open descriptors, `time`). Both allow `socket()` and deny `connect`, so
  the rlimits `sshd-auth` sets after its sandbox stay load-bearing.
  Beyond those 20 the equivalence is REASONED: the builtin's text is not
  in libsandbox, so the two cannot be compared rule by rule. GSSAPI
  logins were not tried (no KDC); public key, password and
  keyboard-interactive were (above).
- `sandbox_init()` loads libsandbox by path whatever the profile, and
  `sshd-auth` applies its sandbox after its chroot, where that path is
  gone. Both profiles therefore rely on dyld answering that `dlopen` with
  the image already loaded. That reliance is upstream's, not new: the
  shipped `sshd-auth` links Security.framework, which loads libsandbox
  before `main` (MEASURED, `DYLD_PRINT_LIBRARIES`), and every login on
  today's builtin depends on it. In a harness that does not preload it,
  the builtin dies after the chroot with "image not found" (MEASURED, 3 of
  3). The patch loads it before the chroot, which makes the dependency
  explicit, and a failed load is fatal there, at the first login
  (MEASURED with the path broken, 6 of 6).
- **One cold-boot halt in 20 did not wait**: its helper counted 62 jobs, one
  more than the controls, so `sandboxd` was running and over 20 s old. What
  started it earlier in that boot was not caught (no samples were kept for
  that halt).
- 10.6 does not pay. It keeps Apple's own sshd, whose logins start
  `sandboxd` too (10 of 10 halts found it 0 s old; there it exits again
  between logins), but its shutdowns stay a bare `shutdown`, which waits
  for no young job: 2.3-2.4 s from the command to QEMU's exit, with or
  without a login 30 s before (5 and 5, 1 vCPU, 10.6.8 built at HEAD).
