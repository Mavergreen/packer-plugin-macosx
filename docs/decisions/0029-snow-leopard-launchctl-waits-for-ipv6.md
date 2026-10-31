# 0029 — 10.6's launchctl waits for IPv6 before it binds sockets

Date: 2026-10-19
Status: proposed, on measurement

## Decision

On 10.6, `firstboot.sh` writes `/etc/rc.server` (`set_ipv6_wait`), a
script launchctl runs at every boot after fsck and before it loads a
single job, and waits for (`fwexec`: no time limit of its own). It checks
every 50 ms until the guest has an IPv6 address (`ifconfig -a` shows an
`inet6` line), and appends one line per boot to
`/private/var/log/guest-macosx-rc.server.log` saying which: "IPv6 address
already there; no wait", "waited N checks 50 ms apart for the first IPv6
address", "still no IPv6 address after N checks in 10 s; going on, and
launchctl's IPv6 binds can fail this boot", or "cannot read the clock (date
+%s); not waiting, ...". The log keeps its last 100 lines (about 11 KB).

- **The bound is the clock, not a count:** it gives up once `date +%s` says
  10 s have passed. Those are whole seconds, so that is 9 to 10 s of real
  time, and the longest the script can hold a boot is 10 s plus the one
  check then running (an `ifconfig -a | grep` and a 50 ms sleep). A clock it
  cannot read ends the wait at once. Nothing runs before it that sets the
  clock (REASONED: launchctl has not started a job yet, so no `ntpd`).
- **Why the bound is never reached on a working 10.6** (REASONED from xnu-1456.1.26
  and 1504.15.3): lo0's `::1` is added by the kernel itself, `ip6_init2` →
  `in6_ifattach(lo_ifp)` → `in6_ifattach_loopback`, and `in6_ifattach` stops
  short of it only when memory runs out, when NDP or the scope ids cannot
  attach, or when the interface lacks `IFF_MULTICAST`, which lo0 has (its
  flags read `UP,LOOPBACK,RUNNING,MULTICAST` in the guests here). Nothing a
  user sets is on that path: Network preferences' "Configure IPv6: Off" is
  configd's, per service (en0, not lo0), and configd starts after
  `rc.server` anyway. So the limit is reached only on a kernel that never
  attaches lo0, a system broken already, and then it is logged. MEASURED:
  5-11 checks at every one of 168 boots here, never the limit.
- **Where:** 10.6's main line, with updates or without (the first boot runs
  it either way, before the updates; no update writes `/etc/rc.server`).
- **The precondition, checked at every boot:** no IPv6 address yet. A boot
  that has one waits for nothing, and says so.
- **The precondition, checked once:** an `/etc/rc.server` that is not ours
  (Mac OS X Server's has one) is left alone, and the first boot's log says
  so: "rc.server: /private/etc/rc.server is there and not ours; left alone,
  so launchctl can still bind sockets before IPv6 is up and lose ssh's
  listener for a boot (F18)". Ours is known by its second line, `# guest-macosx:
  wait for IPv6 before launchctl binds sockets (docs/decisions/0029)`
  (`RC_SERVER_OURS`; `firstboot.sh` and `verify.sh` hold the same line, and a
  test holds them equal). The script is put in place only when it was
  written whole: `cat` succeeded, its second line is that marker and its last
  `exit 0`, and `chmod` and `mv` succeeded; otherwise nothing is installed (an
  existing one of ours stays as it was), the temporary file goes, and the log
  says why ("COULD NOT write ... (the write failed)", "(it came out
  incomplete)", "(it could not be put in place)"). The first boot goes on.
- **The guard:** `templates/snowleopard/verify.sh` prints `rc-server=ours|not
  ours|none; its last boot: <that boot's line>` and fails a build whose
  `/etc/rc.server` is not ours, or whose last boot's line gave up (no
  address in 10 s, or no clock), or, with `updates = security`, whose log
  has no line at all: that build has restarted since the first boot wrote
  the script (the combo's restart, at least), so the script never ran. With
  `updates = none`, verify runs on the first boot itself, and no line is
  right.
- **To remove it:** delete `/etc/rc.server`; the next boot is as before.

10.9 is untouched: its kernel configures lo0's IPv6 before launchd starts
(below), and no 10.9 boot was ever seen to lose a listener this way.

## Context: F18

A 10.6.0 boot reached the login window and never answered SSH: a TCP reset
on port 22, for 10 minutes, nothing in `system.log` (f16's harness, 1 of
about 25 boots). The file system kept working.

### The mechanism (MEASURED 2026-10-19)

1. 10.6's launchctl, bootstrapping the system, creates the listening
   sockets of the jobs it loads itself, in two passes: first the jobs with
   no Bonjour sockets, which it submits (mDNSResponder and DirectoryService
   among them), then the jobs whose sockets Bonjour advertises, ssh's among
   them (`load_and_unload_cmd`, `delay_to_second_pass`, launchd-328 and
   329.3.3's `launchctl.c`).
2. For each socket it binds every address `getaddrinfo` gives, for ssh's
   `[::]:22` first, then `0.0.0.0:22`, and at the first call that fails it
   prints the error to the console and **returns** (`sock_dict_edit_entry`):
   the job loads with that socket's list empty, IPv4 included.
3. The kernel refuses every IPv6 bind, even to `[::]`, while it has no IPv6
   address anywhere: `in6_pcbbind`'s `if (!in6_ifaddrs) /* XXX broken! */
   return (EADDRNOTAVAIL);` (xnu-1456.1.26 and 1504.15.3, 10.6.0's and
   10.6.8's lines).
4. Its first IPv6 address, lo0's `::1`, it adds from a timer of its own:
   `ip6_init` arms `ip6_init2` for 1 s after the network stack starts, and
   only `ip6_init2` attaches lo0 and sets `in6_init2done`. launchctl's own
   attempt at `::1`, before any of this, fails on every boot ("Bug:
   launchctl.c:3557 ... ioctl(s6, SIOCAIFADDR_IN6, &ifra6) != -1", errno 6,
   ENXIO: `in6_if_up` refuses until `in6_init2done`).
5. In a guest, launchctl reaches ssh's bind about a second into the boot,
   so it races the timer. When the bind wins, the job loads with `"Listeners"
   = ( )`: no listener on 22 for the whole boot, so a connection is reset; the
   login window draws; `system.log` says nothing (launchctl's error goes to
   the console alone).

Seen (10.6.0, HEAD's box, unless said):
- in every stuck boot, 14 of 14: `launchctl list com.openssh.sshd` with
  `"Listeners" = ( )` from the watcher's first look (6 s) on, no `*.22` in
  `netstat`, nothing on 22 in `lsof`, every other socket job's sockets there,
  en0 at 10.0.2.15; each of the boot's 113-114 probes reset at once, as port
  23 (no listener) is;
- on the console, dumped before the login window covered it: two "bind():
  Can't assign requested address" lines in each of the 13 stuck boots dumped,
  one in each of the 84 boots read that kept the listener (a first-pass
  job's, below);
- on demand: in a running guest, with every IPv6 address removed, `launchctl
  unload` then `load` of `ssh.plist` printed "bind(): Can't assign requested
  address" and loaded the job with no socket, and port 22 reset; with `::1`
  put back, the same two commands gave it both sockets, and sshd greeted;
- with this change, `rc.server` found no IPv6 address yet in every boot, 168
  of 168 on 10.6.0 and 10.6.8, and waited 5 to 11 checks for it (0.25-0.55 s
  of sleeps, plus an `ifconfig` each, not timed): the first address comes after
  launchctl starts, at every boot here.

The second pass also explains a sign F18's own log had: `mDNSResponder`
started 6 s into that boot, not at 1 s. launchctl registers a socket with
Bonjour (`DNSServiceRegister`) only after its `listen`, and that is what
starts `mDNSResponder` at 1 s; a boot that loses ssh's socket never gets
there, and `mDNSResponder` starts later, on another's demand. All 14
stuck boots here started it at 6 s; 402 of the 409 that kept ssh's sockets, at
1-2 s (the other 7, cold boots, at 7 s).

The other bind error, on every 10.6 boot here, is the same race lost by a
first-pass job: CUPS's (`org.cups.cupsd`), whose socket is on `localhost`,
port 631, `127.0.0.1` first, then `::1`, which is not there yet, so it keeps
its IPv4 listener and never gets an IPv6 one. `::1.631`
was missing in every boot without this change (180 of 180 on 10.6.0, 75 of 75
on 10.6.8) and there in every boot with it (93 of 93, 75 of 75), whose
consoles show no "bind()" line at all (168 of 168).

It is Apple's, not QEMU's: launchctl gives up a job's sockets at the first
failed address, and the kernel adds its first IPv6 address on a timer.
REASONED: a 2009 Mac took more than a second from the kernel's network
stack to launchctl's second pass, so the timer always won there; a fast
virtual machine makes it a race.

10.9 does not have the timer: from xnu-2050 (10.8) on, `ip6_init` registers
`ip6_init_delayed` with `net_init_add`, and `bsd_init` runs it
(`net_init_run`) before it starts launchd, so lo0 has `::1` before
launchctl runs (xnu-2422.115.4, 10.9.5's; REASONED from the source, not
measured on 10.9). xnu-1699 (10.7) still has the timer.

### Rates

The harness (`f18/` in the findings, `trial.sh`): a fresh overlay of a box
image booted as the box's Vagrantfile boots it (its own `-uuid` and NIC
address), one cold boot then two in-guest restarts, each probed on port 22
every 2 s for 240 s (a boot with no greeting by then is stuck); a watcher
LaunchDaemon writing launchd's view of ssh, the TCP listeners and
`system.log` to the serial port; the screen dumped every 0.4 s until the
login window, and the last console screen read by `tesseract` for launchctl's
"bind()" lines.

| box | boots | stuck (no listener on 22 for 240 s) |
|---|---|---|
| 10.6.0, HEAD (5d55371), a series | 85 (30 cold, 55 warm) | 4 (all warm) |
| 10.6.0, HEAD, alternated with the next row | 92 (31, 61) | **10** (all warm) |
| 10.6.0, this change | 93 (31, 62) | **0** |
| 10.6.8, this change's box with `/etc/rc.server` deleted, alternated with the next row | 75 (25, 50) | 0 |
| 10.6.8, this change | 75 (25, 50) | 0 |

On 10.6.0, alternated (the same hours, the same host): 10 of 92 against 0
of 93, one-sided Fisher p = 0.0007; with the series, 14 of 180 against 0 of
93, p = 0.0025. Every stuck boot was a warm restart (14 of 118 warm, 0 of 62
cold), but F18's own was a cold one. On 10.6.8 the race is there (CUPS
loses `::1` in every boot without the change, and `rc.server` waits in every
boot with it), but ssh's bind never lost it in 75 boots (0 of 75 against
10.6.0's 10 of 92, p = 0.002; REASONED: 10.6.8's second pass comes later). The
change is the same on both, inert where it finds an address, and costs a
boot 5-11 checks by its own count (0.25-0.55 s of sleeps, plus an
`ifconfig` each, not timed; the boxes measured ran the first version, which
counted to 200 checks rather than reading the clock, and never reached
its limit); SSH's first greeting came at the same seconds with and without
it (probes 2 s apart: cold 21-22 s after QEMU started, warm 7-10 s after
the watcher, on both releases).

## Options

- **Wait in `/etc/rc.server` for the first IPv6 address (chosen).** It
  restores the order launchctl assumes, for every socket job, not only
  ssh's: the kernel has an IPv6 address before launchctl binds anything. It
  costs a boot what is left of the kernel's timer, 5-11 checks measured,
  nothing measurable in when SSH answers.
- **Give ssh's socket `SockFamily` `IPv4`** (an override, or Apple's
  `ssh.plist` edited). The IPv4 listener would never be lost, but every
  boot would lose IPv6 SSH, and the race stays for every other socket job.
- **Two socket entries, IPv4 and IPv6, for ssh.** The IPv4 one would never
  be lost; the IPv6 one still would, in the boots that lose the race.
- **A boot job that reloads ssh's job when it finds no listener.** A repair
  after the fact, for ssh alone, of something that need not happen.
- **Retry SSH, or wait longer for it.** No: the boot never recovers, and the
  owner's rule is no retries for a cause that is known.
