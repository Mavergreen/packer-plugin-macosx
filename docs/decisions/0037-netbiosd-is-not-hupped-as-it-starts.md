# 0037: netbiosd inherits ignored HUP before exec, not after startup

Date: 2026-10-30

Status: Accepted by the owner, 2026-10-30. Replaces the unaccepted F27
proposal to remove SMB preferences' `RunAtLoad`.

## Context

F27 is netbiosd dying of HUP before its own signal setup, then launchd waiting
out the default respawn throttle. In the earlier 200-restart series it died
six times and held the shutdown helper's startup settle wait. This is not a
reason to stop publishing SMB preferences at boot.

**MEASURED:** with the old removal proposal, saved `NetBIOSName` was still
`F27BASE`, but `scutil` returned `No such key` for transient `com.apple.smb`.
A preference edit need not occur on every boot. Removing `RunAtLoad` loses
boot reconstruction; an HUP-free restart series did not prove equivalence.
That proposal was never accepted or shipped.

Private disassembly of smbx-276.92.2's netbiosd shows dispatch signal sources
and `signal(signum, SIG_IGN)` installed before the first preference load.
Before that setup, the native process can still have default HUP disposition.
**MEASURED:** a deterministic BSD-stopped stock process had `hup_ignored=0`
and died when sent one HUP then CONT. An ignored disposition inherited across
exec survives that window. POSIX exec preserving SIG_IGN is the mechanism,
not a longer timeout or faster startup.

## Decision

On 10.9, after updates, `initialize_netbiosd_hup` recognizes:

- the executable SHA-256
  `946d5e1d843c55c902cbefc493ff072e88e4eb00077d6edf3b077ee5b7a5c95a`,
  the actual 10.9.5 netbiosd measured;
- a regular, non-symlink executable and `com.apple.netbiosd.plist`, its matching label,
  original executable as its sole argument, an absent or matching `Program`,
  and both inherited name/datagram socket groups.

It installs `/usr/libexec/guest-macosx-netbiosd-hup`, root:wheel 755:

```sh
#!/bin/sh
# guest-macosx: initialize HUP before netbiosd exec (docs/decisions/0037)
trap '' HUP
exec "$@"
```

It stages a complete plist edit and atomically replaces the job: the wrapper
as argv[0], the original executable as argv[1], and the optional `Program`
changed to the wrapper. Exec reconstructs exactly the original native argv.
There is no live unload/rebind: launchd must retain real socket ownership.
The change takes effect on the next boot, not retroactively on this boot.

No SMB preference plist, `RunAtLoad`, LaunchEvent, throttle, socket, daemon
user, or native executable is changed. The native process still installs its
own late HUP source. Other signals are not initialized by this wrapper.
Unknown binaries, custom arguments/programs, missing socket groups, missing
jobs and foreign wrappers are left alone with a reason in the log. A known
configuration is idempotent. A failed staged edit leaves the original job
unchanged and is logged as `COULD NOT`; verification rejects that build.
A pre-existing wrapper must be regular and non-symlink and match the exact
content, numeric owner/group 0:0 and mode 755; it is never overwritten to
bypass a mismatch. File kinds are checked before reading bytes: a foreign
FIFO must not block initialization.

**10.6:** the job and executable are absent in the measured 10.6.8 guest.
The call is scoped to 10.9 and the function is independently inert without
the files. 10.6's older ManagedClient and Samba are not altered.

## Evidence and limits

Private evidence is under
`.superpowers/findings/2026-10-13-vm-reliability/f27/`:

- `controlled-v5`: valid stock BSD-stop/early-HUP death control. The initial
  ignore arm stopped on an unsupported SMB helper flag; not a service pass.
- `controlled-v6`: inherited ignore and immediate same-PID survival, but
  live socket rebinding produced bind errors; not a functional service pass.
- `runtime-functional`: explicit/fallback names, genuine late HUP callbacks,
  simulated offline preference reconstruction, 10/10 warm boots, health,
  shutdown, and zero HUP deaths after the warm-series marker.
- `controlled-cold`: exit 2 before target snapshot, no HUP injection result.
  Corrected observer-output readiness is not a guest retry or proof that the
  original failed harness passed.
- `controlled-cold-v2`: genuine cold socket ownership; forced early HUP,
  same-PID/starttime survival, external name registration; real late callback,
  explicit/fallback/restore and offline-reconstruction cases; 10/10 warm
  boots, zero warm HUP deaths, health and clean shutdown, exit 0.
- `controlled-cold-final`: the pre-review proposed firstboot function, not a
  separately handwritten equivalent, installed the generic wrapper/argv.
  Native PID129, BSD state4, HUP ignored before injection; same PID and start
  after CONT; external F27EARLY name verified. The complete semantics series
  and another 10/10 warm boots, zero warm HUP deaths, health and clean
  shutdown passed, exit 0.
- `proposal-validation`: after independent review strengthened only the
  foreign-file guards, the current extracted initializer was idempotent in
  an actual 10.9 guest (job/helper bytes unchanged), and the current verifier
  inspected its installed helper/argv/ownership/mode successfully. A separate
  actual 10.6 guest had neither job nor executable and initialization was
  inert. Both guests shut down cleanly, driver exit 0.

Readiness is the actual `State:/Network/NetBIOS` `RegisteredName`, then one
external UDP-forwarded NBSTAT request with its original three-second bound.
Late edits are spaced for the stock preference job's ten-second policy;
these are semantics tests, not reload-latency or race-frequency comparisons.
The offline case unloads preference sync, changes the persistent value, and
reboots: it is not a literal powered-off filesystem edit. Probe scripts and
DTrace fixtures are disposable and are not shipped.

The three ten-warm-boot series are separate profiles, not one controlled
before/after rate experiment. Fresh builds of the final approved integrated
payload and whole-branch release validation are still required. Neither HVF
nor NVMM has been newly measured here; the change uses guest POSIX signal
semantics, not a host-engine facility.

Host tests cover preconditions and inert cases, actual generated-wrapper exec
and argument preservation, idempotence, ownership/content conflicts, atomic
edit failures, call order, and the verifier rejecting a successful action log
with a missing/changed helper or wrong job. The pre-review full proposal suite passed
830/830 with lint, Bash 3.2 and Bats-bang checks. Independent read-only review
found a foreign-FIFO hang; the bounded regression failed before the guard
fix and passed after it. The analogous executable-file guard was strengthened
as well. Current focused tests pass 29/29, eight selected mutations are
killed, and the stable final proposal passes 833/833 plus lint, Bash 3.2,
Bats-bang, Go vet and Go tests. A separate follow-up reviewer approved the
complete source diff with no substantive issues, checking shell syntax,
whitespace, all changed goldens, input listing and recipe digest. The
reviewers did not independently measure native evidence. The owner approved
landing on 2026-10-30; the final fresh-image integrated gate is still required.

## Consequences

A four-line guest shell helper and one exec replace the vulnerable initial
signal disposition. No service or preference publication is suppressed and
no respawn protection is removed. The build log says what acted or why it
was inert; packaging checks the installed helper and argv, not just that log.
The wrapper remains for later boots; a future unrecognized native version
needs a new applicability review (the firstboot installer will not apply this
to an unknown binary in a new build). Removing the measure means restoring
the original argv and optional Program, then deleting our helper.
