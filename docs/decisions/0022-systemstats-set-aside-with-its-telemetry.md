# 0022 — systemstats' jobs are set aside with the telemetry plugin

Date: 2026-10-15
Status: accepted, on measurement

Names: this project's internal names were renamed on 2026-10-17, from
the `mqg` stem to `guest-macosx` (the shutdown helper was `mqg-shutdown`).
The measurements below ran under the old names, and nothing else about
what ran has changed.

## Decision

Where a 10.9 guest's telemetry plugin is set aside (ADR 0020), the install
also moves systemstats' three launchd jobs, whole, from
`/System/Library/LaunchDaemons/` to `/System/Library/LaunchDaemons-disabled/`:

- `com.apple.systemstatsd.plist`: the daemon that keeps the statistics;
- `com.apple.systemstats.daily.plist`: `systemstats --daily`, at 00:15 local
  time every day;
- `com.apple.systemstats.analysis.plist`: `systemstats --xpc`, the
  on-demand service that answers questions about them.

It is `assets/guest/postinstall`'s "THE SYSTEMSTATS JOBS", just after the
plugin's own move, so launchd never loads them, the build's own first boot
included (REASONED: the target's launchd has never run at install time, and
no real install has yet run this block; the first build proves it). It checks its precondition there, on the target: if the plugin is
still in `UserEventPlugins` (its move failed), it leaves the jobs in place
and says so in `postinstall.log` ("systemstats jobs left in place: the
telemetry plugin is still in UserEventPlugins"); otherwise it moves the ones
present and says which ("systemstats jobs set aside, with the telemetry
plugin: …"). A volume with none of them is left alone: a 10.9 volume says so
("systemstats jobs set aside: none in LaunchDaemons at install"), and one
that says it is 10.6 says nothing. `templates/mavericks/verify.sh` fails a build whose
guest has the plugin set aside and any of the three plists in
`LaunchDaemons`; with the plugin in place, it does not ask.

To restore them: move the three back to `LaunchDaemons`, and the plugin back
to `UserEventPlugins` (ADR 0020), and restart. Security Update 2016-004
carries `systemstatsd` and `systemstats` but not their plists (MEASURED
2026-10-15: the Bom of `SecUpd2016-004Mavericks.pkg` lists both and has no
`LaunchDaemons` entry at all), so the update does not undo this, and the
first boot does not need to move them again.

The precondition is judged once, at install time. If the install-time move of
the plugin fails, the jobs are left in place; `firstboot.sh` then sets the
plugin aside at first boot (`set_telemetry_plugin_aside /`), and the guest
has the plugin aside with the jobs loaded: this decision's problem. That
combination is left to `verify.sh` by design, which fails that build twice,
on the postinstall log's missing "telemetry plugin set aside:" line and on
the plists. No box ships so (REASONED from the two checks; not exercised).

## Context

From a guest's first 00:15 local time on, every shutdown of that boot held
20 s and left a `.shutdownStall` report naming `systemstatsd`.
The guests' time zone is the installer's default, US/Pacific (nothing in
this project sets one), so that is 07:15 or 08:15 UTC. It hits any guest up
across that minute: a long-lived `vagrant up`, a CI runner, a gate or build
whose boot spans it. The daily run also writes a `powerstats_*.diag` to
`/Library/Logs/DiagnosticReports`, which the boot gate fails on by itself.

### The mechanism

From `systemstatsd`'s own code (it is 3.7 KB of x86-64, disassembled from the
guest's copy), its launchd log, its transaction count
(`_vproc_transaction_count_for_pid`) and its dirty flags
(`proc_get_dirty`), and dtrace on it from before its `xpc_track_activity`:

1. `systemstats --daily` runs `spindump -system_stats
   -microstackshots_starttime …`, whose `libsystem_stats`
   (`systemstats_get_microstackshots`) sends `com.apple.systemstatsd` a
   request with a reply connection, and waits.
2. launchd starts `systemstatsd` on demand. Its `main` calls
   `xpc_track_activity`, which sets SIGTERM to `SIG_IGN` and enables
   transactions (`_vproc_transactions_enable`): the 10.9 model, in which a
   dirty job is expected to finish its work and go clean, and launchd then
   kills it.
3. Its request handler queues the request, calls `xpc_transaction_begin`,
   and posts `com.apple.telemetry.immediate`: a request to the telemetry
   plugin, in `UserEventAgent (System)`, to send its data now. It answers
   queued requests, and calls `xpc_transaction_end`, only in the handler of
   its `com.apple.telemetry` event stream, when that data arrives. Nothing
   else ends the transaction: no timeout, no cancellation when the client
   goes.
4. With the plugin set aside, nothing listens for that notification and no
   event ever arrives. `spindump` gives up after 30 s ("Timed out waiting
   for system stats microstackshots after 30 seconds") and exits;
   `systemstatsd` stays dirty (flags 7: tracked, idle-exit, dirty), with one
   transaction open, for the rest of the boot: 60 minutes watched, every second, with no change, and no
   `xpc_transaction_end` and no telemetry data in that hour (dtrace).
5. At the shutdown launchd sends it SIGTERM, which is discarded
   (`signal-discard`, dtrace), and waits its 20 s exit timeout: "Exit timeout
   elapsed (20 seconds). Killing", "Idle-exit job exited while dirty".

So the window does not depend on how much data there is, and it does not
close: it is the rest of the boot. It is ADR 0020's cost, unforeseen there:
the plugin's consumer and its consumer's clients stayed, and the plugin
was what answered them.

`systemstats --xpc` (the analysis job) is the same shape, REASONED from its
code and not measured: for a `process_usage` request it posts
`com.apple.systemstats.sample-processes-now` and waits, with no timeout, for
`…sample-processes-done`, which only the telemetry plugin posts. Nothing in a
CI guest asks it; Activity Monitor's Energy tab would (REASONED).

### Other periodic jobs

10.9 has 22 launchd daemons that run on a schedule: 16 with
`StartCalendarInterval` or `StartInterval` (3 of them disabled) and 6 with
an XPC activity interval (`periodic-daily`, `-weekly`, `-monthly`,
`softwareupdated`, `applessdstatistics`, and `backupd-auto`, disabled).
Each enabled one but the systemstats job and `locationd` (always running)
was started with `launchctl start` (what its timer does), in a guest with
this decision applied; once it had finished, or after 150 s, a census of
every dirty process and open transaction was taken, and the guest restarted
through `guest-macosx-shutdown`. 16 jobs, **0 stalls** and no reports; none left a
process dirty that was not dirty before, apart from the probe's own SSH
sessions' per-user launchd and distnoted. Only `DiagnosticReportCleanUpDaemon`
of them has `EnableTransactions` in its plist, and it was done in 4 s. A job
still running at the restart (`periodic-*`, `aslmanager` and
`softwareupdated` may have been, at 150 s) is not transaction-tracked, so the
shutdown's SIGTERM ends it. The scheduled LaunchAgents load in a login
session, which these guests do not have (one, noticeboard's, also loads in
the login window's); they were not tested.
None has systemstats' shape: a client's request that a daemon holds open
for an answer that cannot come.

## Alternatives

- **Wait for `systemstatsd` in `guest-macosx-shutdown`.** It cannot finish: the data
  it waits for never comes.
- **Kill a dirty `systemstatsd` in `guest-macosx-shutdown`.** It works, but it leaves
  a job that hangs every day, and the daily `.diag` report, to be cleaned up
  at each shutdown, and it is ADR 0021's "lower the exit timeout" in another
  form.
- **Put the telemetry plugin back.** It is ADR 0020's crash.
- **Disable only the daily job.** That removes the one periodic client, and
  the report. `systemstatsd` would still hang for any other client
  (`spindump -system_stats` by hand, the analysis job), so the daemon goes
  too.
- **`launchctl unload -w` at first boot.** launchd has loaded the jobs by
  then, and a build that crosses 00:15 before `firstboot.sh` reaches that
  line would still run the daily job. The install-time move mirrors ADR
  0020's and is checked the same way.

## Cost

No system statistics in the guest, which ADR 0020 already gave up: without
the plugin, `systemstatsd`'s database stays empty and the daily report says
nothing (its `.diag` is 379 bytes, "Steps: 0"). No daily power report
(`com.apple.SystemStats.Daily`), which 10.9 submits to Apple only with
diagnostics sharing on. Activity Monitor's Energy tab has no history
(REASONED).

## Measurements

2026-10-15, the boot gate's layer box (built at 2fa97c1, the plugin set
aside), a fresh overlay and a cold boot per trial, HEAD's helper (c010368)
installed. The guest clock is stepped to 10/17 00:14:50, which runs the
daily job at once (launchd runs a calendar job whose time a clock step
passes); at 00:15:45 `systemstatsd`'s state is read, then the guest is
restarted through the helper. A stall is "Exit timeout elapsed" in
launchd's shutdown log. Before and after alternate trial by trial; the
two mechanism arms alternate with each other, in a later run.

| Arm | Stalls | Restart | `systemstatsd` at 00:15:45 | `powerstats` report |
|---|---|---|---|---|
| as built (before) | **12 of 12**, all `com.apple.systemstatsd` | 43-44 s | dirty, 1 transaction, 12 of 12 | 12 of 12 |
| this decision's end state (after): the three moved by hand in a booted guest, then a restart | **0 of 12** | 24-26 s | not running | 0 of 12 |
| the plugin back in place (stock 10.9) | 0 of 4 | 24 s | running, clean, no transaction, 4 of 4 | 4 of 4 |
| only `systemstatsd` set aside | 0 of 4 | 24-26 s | not running | 4 of 4 |

Fisher's exact test, one-sided: before against after p = 3.7e-7; before
against stock p = 5.5e-4. With the plugin in place the daily run's spindump
got its data and wrote its report within 5 s of the run's start; without
it, spindump wrote "Timed out waiting for system stats microstackshots
after 30 seconds" and then its report. With only `systemstatsd` set aside,
its lookup fails at once and nothing waits, which is why the daemon has to
go; the report stays, which is why the daily job goes too.

In a guest that had run its first daily report, the next two (the clock
stepped a day at a time, once with a restart between) sent `systemstatsd`
nothing and wrote no report. So the trigger measured is a guest's first
00:15, and every `vagrant up` and every gate run starts from a box that has
never run it. Whether a guest left up for days, its clock running
normally, sends later requests is not measured; this decision covers them
either way.

## What would change this

The telemetry plugin coming back (ADR 0020's crash fixed some other way):
then the precondition is absent, and the jobs stay. A `.shutdownStall` naming
`systemstatsd`, or a `powerstats` report, from a box built with this decision.
