# 0045: XProtect gets its daily time before crsud first starts

Date: 2026-11-06

Status: Accepted by the owner, 2026-11-06 ("Apple's intent: enabled").

## Context

Whether a 10.6 box runs Apple's critical-update daemon, `crsud`, varied
from build to build. ADR 0034 saw it in one of two builds and could not
say why, and ADR 0039 (F5) recorded an old image with `crsud` off and the
2026-10-30 gate's image with it on. Five known builds: F25's two (one off),
G8's (off), the 2026-10-30 gate's (on) and the 2026-11-05 gate's (off).

Security Update 2013-004 installs two `RunAtLoad` jobs that first run on
the boot after it:

- `com.apple.xprotectupdater-init` runs `/usr/libexec/XProtectUpdaterInit`.
  READ from its x86_64 code: if XProtect's plist
  (`/System/Library/LaunchDaemons/com.apple.xprotectupdater.plist`) has a
  `StartCalendarInterval` it exits; otherwise it removes `StartInterval`,
  runs `launchctl unload` on the plist, writes it with a random daily
  `Hour` and `Minute`, and runs `launchctl load` on it.
- `com.apple.crsud` runs `/usr/libexec/crsud`. READ from its x86_64 code
  (sha256 `4b689243…`): `-[CUPrefs _syncUpXProtectAndCodeGingerSettings]`
  asks `SMJobIsEnabled` for `com.apple.xprotectupdater` and for itself,
  and when XProtect's answer is no and its own yes it calls
  `SMJobSetEnabled(com.apple.crsud, false)` ("Disabling crsud service -
  xprotect was found disabled..."), which launchd keeps in its overrides.

MEASURED 2026-11-06 in a 10.6.8 guest (the 2026-11-05 gate's box), with
`SMJobIsEnabled` called through Python's ctypes: XProtect's job answered
1 loaded, 0 after `launchctl unload`, 1 after `launchctl load`. So
`crsud`'s question, asked while `XProtectUpdaterInit` has the job
unloaded, gets no, and `crsud` turns itself off for good. In that box
the plist's rewrite, `crsud`'s authorization to change launchd's jobs
(`secure.log`) and the overrides file's write are all stamped 20:22:23,
the same second, 7 s into the build's last boot. Its `crsud` is off.

## Decision

`firstboot.sh`'s `settle_xprotect_schedule` runs after each 10.6 update
the first boot installs, on the boot that installed it. Where
`XProtectUpdaterInit` is there and XProtect's plist has no
`StartCalendarInterval`, it runs `XProtectUpdaterInit` then, under a 60 s
bound, and logs whether the plist got its daily time. `crsud`, new with
the same update, is not loaded until the next boot, so nothing asks about
XProtect while the job is unloaded. On that next boot
`XProtectUpdaterInit` finds the daily time and exits, and `crsud`'s
question finds XProtect loaded.

Anywhere else it does nothing but say so: no `XProtectUpdaterInit` (10.9;
10.6 without 2013-004), or a plist that already has its daily time.

Every 10.6 box with `updates = security` so ends with `crsud` on, as
`crsud`'s own sync means it to be while XProtect is on. The owner chose
that over turning it off everywhere or leaving the race (2026-11-06).

`verify.sh` (10.6) reads launchd's overrides through `plutil` (through
`sudo` if need be), prints `crsud=enabled|disabled|unreadable`, and with
`updates = security` fails a build whose `crsud` is off or whose overrides
it cannot read.

## Consequences

- F5 (ADR 0039) now holds on every 10.6 box, not three in five: READ
  from `crsud`'s code, its startup ignores SIGTERM, and a scan that fails
  (10.6's TLS cannot reach Apple's catalog, as `XProtectUpdater`'s -1200
  shows) is retried once after exactly 20 s (`dispatch_time` of
  20,000,000,000 ns) before it exits. A halt in a box's first half-minute
  can wait for that retry, up to launchd's 20 s exit timeout. That is the
  native behaviour the owner chose to keep (ADR 0039).
- `XProtectUpdaterInit` picks XProtect's daily time during the build, not
  at the box's first boot. Every box made from one image shares it.
- `tests/firstboot_xprotect.bats` holds the step and its inert cases;
  `tests/firstboot_updates.bats` holds that it runs after 2013-004's
  install, before the restart, and not without it;
  `tests/template_verify.bats` holds the check.
- MEASURED 2026-11-06, the default 10.6 gate (`bin/gate.sh --release
  10.6`, 10 restarts) on this commit's code, before these lines: every step that applies passed, and
  `verify.sh` printed `crsud=enabled`. Its box, booted after: the first
  boot's log ran the step after 2013-004's install, at 21:18:28Z, and the
  plist got its daily time then (its modification time, the same second);
  after the combo's install it said there was no `XProtectUpdaterInit`.
  launchd's overrides have no `crsud` entry, and `launchctl list` shows
  `com.apple.crsud` loaded, last exit 0.
