# 0039: Accept the remaining native and historical limits; defer broader comparisons

Date: 2026-10-30

Status: Accepted by the owner, 2026-10-30.

## Context

The final reliability review left three observations without a proposed
feature-preserving fix, and an unfinished comparative/targeting programme.
Successful boot gates do not supply their missing causal evidence. The
owner explicitly agreed to F5, accepted F10 and F12, and deferred that full
programme after reviewing the remaining scope.

## Decisions

### F5: keep the native critical updater's shutdown behaviour

Keep Snow Leopard's native critical-update feature and shutdown grace.
Do not add a disabling override, forced-termination wrapper or altered
shutdown deadline to obtain a faster result.

Three historical early shutdowns held for crsud for 16.4–16.7 seconds.
The final ordinary default gate supplied an uninstrumented positive
observation: halt requested at kernel uptime 10.956588 seconds, native
crsud PID 104 exited 12.664017 seconds after TERM, without a recorded exit
timeout or KILL. QEMU exited after 15 host seconds. No guest probe, sampler
or enablement change ran in that gate. The inspected current stock
executable explicitly initializes SIGTERM to SIG_IGN; normal TERM therefore
cannot be assumed to interrupt its remaining work.

This is an accepted native completion/grace delay, not a fix or rate claim.
The exact outstanding operation remains unidentified. A libdispatch warning
and late network-service errors are retained observations, not an isolated
cause. Historical executable hashes were not recovered.

Earlier tests with an inactive updater were not positive controls. An
instrumented enabled-job fixture reached an exit timeout but lost its
asynchronous sampler; possible target suspension makes it unsuitable as
unprobed timeout evidence. The old base image had a persistent disabled
crsud override; the final fresh image had no such entry. Neither variation
proves its writer. ADR 0034 already records a possible native XProtect/crsud
policy-sync path in an unrestricted control. No new isolation regression,
universal disabled runtime state or successful update completion is inferred.

### F10: accept the excluded experimental observation

The historical experimental descendant answered cold SSH about ten seconds
later, then stopped doing so after another clean experimental boot. Its
construction and surviving backing chains establish that its COW probes
did not mutate the original parent and are not release inputs.

Accept it as an experimental-only observation with unresolved cause and
unrecovered original ancestor task state. Do not claim a pending-task cause
was proved, a guest fix shipped, or that the delay cannot recur elsewhere.

### F12: accept the historical bounded warnings

Accept the two recorded 30-second shutdown-helper give-ups, neither with a
shutdown stall, as unresolved historical bounded warnings. The target boots'
PID/UID/argument/check-in/exit histories were not retained. The terminal
printed process name is not proof of continuous young ManagedClient
presence or a relaunch loop. A single delayed replacement could suffice,
but that is not attribution. F28's separate disposition in ADR 0038 does not
explain these cases. No new retry, warning suppression or allowlist ships.

### Explicitly defer the complete comparison/targeting programme

For this release accept the current scoped defaults and recorded guard
proof, and defer the unfinished programme, including:

- Large-N ordinary 1–2-minute and ten-minute shutdown baselines on both
  releases, with no measures and with each measure removed separately.
- Actual GUI/session shutdown comparisons.
- Alternative upstream EDKII/OpenCore settings, USB topology, CPU counts,
  NICs and disk controllers, and real 10.9 hardware corroboration.
- Additional per-environment runtime detection, action-or-no-op logging and
  inertness proof for each intervention across those configurations.

This is not merely deferral of the expensive uptime matrix. In particular,
the 10.9 helper still uses unconditional `killall ManagedClient` at shutdown;
the telemetry/statistics quarantine remains an OS-scoped build default, not
a universal runtime hardware/hypervisor need detector. Existing age/dirty
checks, absence fixtures and targeted causal controls retain their actual
scope; neither those nor the default gate stand in for the deferred proof.

No new credential, auto-login, physical-machine access or GUI fixture
permission follows from this decision. F15's earlier authorization crossing
is not retroactively authorized by technical success or this acceptance.

## Validation and release boundary

At functional source 0b4a734, the host suite passed 873/873 plus lint,
Bash 3.2 checks and Go vet/tests. A fresh combined native gate passed both
releases with one restart each and unchanged bounds, without retries.
10.6's layer/TLS checks remain explicitly not applicable. Subsequent
release-note changes affect documentation/VCS metadata, not native inputs.
These are recorded measurements, not a stress run or qualification of a
future release-version artifact.

The private investigation reports retain failures and evidence limits.
Images, Apple executables, diagnostic proxies and probes do not ship.
Remaining artifact housekeeping, final version/artifact checks and explicit
history-rewrite, push, tag and publication approvals are separate tasks.

Reconsider these dispositions if updater work demonstrably fails to finish,
the historical warnings acquire reproducible process histories or stalls,
the experimental delay appears in release inputs, or broader runtime support
requires the deferred comparisons and targeting work. Do not silently expand
these acceptances into claims of cause prevention or universal correctness.
