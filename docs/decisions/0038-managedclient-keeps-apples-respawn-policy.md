# 0038: ManagedClient keeps Apple's respawn policy

Date: 2026-10-30

Status: Accepted by the owner, 2026-10-30.

## Context

F28 is a rare 10.9 ManagedClient initialization exit 7. Inspection of the
measured Apple executable and public liblaunch/launchd source identifies a
redundant Mach-service check-in: the launchd-origin check automatically
acquires the service's receive right, releases it, then server initialization
explicitly checks in again. Returning the right is asynchronous; the second
check-in can encounter `BOOTSTRAP_SERVICE_ACTIVE` before launchd processes
the return. The process exits and launchd's default throttle delays restart.
This is before application request dispatch, not evidence of recovery of an
operation already being processed.

The recovered proposal set `ThrottleInterval` to zero. It reduced restart
delay but did not prevent the initialization fault. A disposable persistent-
failure fixture measured one start under stock policy versus 65 starts in
approximately two seconds at zero throttle (a second run: 57). That measures
restart amplification, not real ManagedClient production resource costs.

A disposable metadata broker demonstrated a possible cause fix: forward the
real CheckIn/ack but omit precisely the metadata causing the redundant first
acquisition, leaving the native server's explicit acquisition intact.
Passthrough versus transformed profiles measured two versus one agent
check-in RPCs, with the real service, native primary PID, Apple executable,
signing restrictions, signal state and a read-only profile query retained.
However, that solution adds a privileged interpreter/helper process and a
private-ABI dependency. Independent review identified duplex-backpressure
stall risk and first-matching versus first-response semantics to resolve,
beyond installation, fallback, ownership and lifecycle hardening. It is not
a shipping implementation.

## Decision

Leave Apple's ManagedClient behavior and default respawn protection alone.
Accept the known occasional initialization restart and delay for this
release instead of introducing that larger workaround. Do not install the
zero-throttle proposal, metadata broker, diagnostic proxy or fault probes.
Do not suppress the service, weaken signing, change Apple executables or
expand gate/shutdown bounds to conceal the fault.

This is an explicit owner-accepted limitation, **not** a cause-prevention fix
or a claim of zero initialization failures. Neither a generally successful
boot gate nor a server's status-zero reply changes that distinction.

## Evidence and limits

Private evidence is retained under
`.superpowers/findings/2026-10-13-vm-reliability/f28/` and summarized in
`f28-checkin-design.md` at that findings root:

- In `original-request-v3`, one synthetic early SIGKILL before server startup
  was followed by the replacement handling the original queued profile
  query and constructing a status-zero reply after 10.294 seconds, without
  resubmission or throttle/timeout changes.
- In `original-handoff`, one synthetic SIGKILL at the second real check-in,
  after the first right handoff, was followed by replacement service receipt
  and a constructed status-zero reply after 10.780 seconds. The added
  client-receipt observer failed its 20-second readiness check: driver
  exit 1, owned cleanup, complete trace recovered read-only. This was not
  an overall passing experiment or a measured clean halt.
- The observed request is mdmclient's command 5/profile 0, a read-only
  ProfilesForUserAsCPProfile query, not enrollment. Server construction of
  a successful reply does not establish client receipt/decoding, full MDM
  correctness, in-flight recovery or a natural-failure recovery rate.
- The Python prototype's paired diagnostic run passed, including measured
  runtime signing and signal snapshots matching stock; ten host policy
  tests passed. Those results do not establish a production prevention
  rate or remove the review findings.

The two historical F12 30-second shutdown-helper give-ups lack concurrent
process samples. They are not proved to be this fault, nor closed by this
disposition; that finding remains separate. The owner subsequently accepted
F12 as an unresolved historical bounded warning in ADR0039, not as a
consequence proved to be F28.

## Consequences and reconsideration

No new guest component or respawn override is shipped for F28. The native
critical service and its protection remain intact; the occasional delay is
visible evidence, not filtered or allowed away by a new test policy.

Reconsider if failures become materially more frequent, original queued
requests demonstrably fail to recover, or a substantially simpler and
feature-preserving cause fix becomes available. Any broker proposal would
need guarded production implementation, independent review, separate owner
landing approval and fresh integrated validation before shipping.
