# 0028 — A failed install ends the build, saying why

Date: 2026-10-19
Status: accepted (the disconnect MEASURED in real guests, 2026-10-19)

## Decision

When an unattended install fails, the build ends within a minute or two,
not at `install_timeout` (an hour), and Packer's own error says what
failed and where the install's log is (`output-<release>-install.log`,
`docs/decisions/0027`). Both releases, one code path.

- **The guest answers SSH with a disconnect.** Once the install has failed,
  the media's autoinstall hook (`refuse_logins` in
  `assets/guest/autoinstall/autoinstall.sh`) answers every connection to
  port 22 with an SSH version line and one unencrypted SSH_MSG_DISCONNECT
  (reason 14, no more authentication methods), before any key exchange,
  whose text reads "INSTALL FAILED: <the installer's line>. Nobody can
  authenticate here: this guest is the installer, stopped. Its log is
  output-<release>-install.log, beside the build's output directory." It
  is a few lines of perl, with no module: no sshd, no key, no disk.
- **Both templates set `ssh_handshake_attempts = 10`.** Packer's client
  (x/crypto's ssh) reports a pre-key-exchange disconnect as `ssh: handshake
  failed: ssh: disconnect, reason 14: <the text>`; the text says
  "authenticate", so Packer counts it, and after ten it ends the build with
  that text as its "original error".
- **"Failed" is one of two things, both logged:** the installer's own line
  in syslog, `<pri>Mon dd hh:mm:ss host OSInstaller[<pid>]: Install
  failed...` (anchored: not PackageKit's line about one package, not
  another process quoting it, not other casings), which the hook's
  follower (0027) acts on once; or the hook giving up before the installer
  starts (`give_up`, which answers before it halts). The follower binds
  syslog from its first second and holds lines until the serial port
  appears, so it watches whether or not a port ever does.
- **Only in the installer environment.** The answer starts only from
  `/etc/rc.cdrom.local`, where the media puts the hook; anywhere else, on
  any host, only with the perl and the port both named
  (`GUEST_MACOSX_PERL`, `GUEST_MACOSX_REFUSE_PORT`), so no test can bind a
  host's port 22 or run a tool it was not given. The check is the place,
  not the OS.
- **Each step is logged**; an answer that cannot start (the port taken, a
  fork that fails) says why, and the build waits out `install_timeout` as
  before.
- **`install_timeout` stays `1h`**; its description says what a busy host
  costs (0027: 93 minutes under 26 busy loops) and that a failed install
  ends the build sooner.

## Context

0027 made a failed install explain itself, in `output-<release>-install.log`
beside the output directory. The build still waited out its hour for SSH:
the installer's "Install Failed" sheet waits for a click that never comes.

What Packer's qemu builder (v1.1.7, with packer-plugin-sdk v0.6.11's
`StepConnectSSH`) honestly allows while it waits for SSH, from its source:

- It does **not** watch QEMU. The driver's end channel is read only by the
  shutdown step after provisioning; a guest that powers itself off leaves
  the SSH wait retrying connection-refused until `ssh_timeout`.
- It has no boot_command, QMP or shutdown hook that runs during the wait,
  and it sets no `BannerCallback`, so a server's banner is dropped.
- It ends the wait on: a login; `ssh_timeout`; an interrupt; or, only when
  the template sets `ssh_handshake_attempts` (with `ssh_timeout` set it
  defaults to none), that many handshake errors containing
  "authenticate" -- a string match the SDK itself calls brittle, which
  this decision leans on.

## History of this decision (MEASURED 2026-10-19)

The first form (540dd72..b81e357) started a real sshd that refused every
login: the environment's own on 10.9, and on 10.6, which has none, the
disc's, taken out of `BSD.pkg` onto a 320 MB RAM disk. Real guests proved
it: a planted failure ended its build 81 s after "Install failed" on 10.6
(a file held open on the target) and 66 s on 10.9 (a target with 2 GB
free), each with its log kept, and good builds of both counted no refused
login. They also found two faults, each fixed then: 10.6's perl passed an
ignored SIGCHLD through exec, so the refusal's `mount` could wait for no
child, and 10.9's sshd (6.2) offered aes128-gcm with a libcrypto that has
none ("fatal: matching cipher is not supported"). And 10.9's installer
survives a file held open on the target ("unmount blocked by dissenter",
then on).

The F21 re-review then showed the build's error could carry the reason
(a pre-key-exchange DISCONNECT, measured against x/crypto v0.57.0), and
that the "not macOS" guard on the sshd let a test on a Mac run the Mac's
own sshd. The owner adopted the disconnect: it removes the sshd, the RAM
disk and the BSD.pkg extraction, and the false-trigger case where a RAM
disk would race a good install (0027's B).

Tested without a VM: `internal/payload/refuse_test.go` runs `refuse_logins`
and connects with x/crypto's ssh v0.57.0 (the qemu plugin's), twice; each
error carries "authenticate", "disconnect, reason 14", the installer's
line and the log's name. `tests/guest_assets.bats` reads the answer byte by
byte, and holds the trigger, the guard, the follower's early watch and
`give_up`'s order.

Then real guests (MEASURED 2026-10-19, from main at 132812d and e027da1;
"Install failed" timed by the guest's clock, 3 h behind the host's as its
first refused handshake shows):

| build | planted | result |
|---|---|---|
| 10.6 | a file held open on the target | "Could not unmount disk", "Install failed"; the build errored 68 s later, after 10 refusals, Packer's error ending in the guest's text and `output-snowleopard-install.log`; the log kept |
| 10.9 | a target with 2 GB free | "Install failed: There is not enough space"; errored 69 s later, the same way, naming `output-mavericks-install.log` |
| 10.6 | nothing | verify ok; SSH after 15 min 21 s; 0 refusals counted; the hook started no answer |
| 10.9 | nothing | verify ok; SSH after 18 min 10 s; 0 refusals counted; the hook started no answer |

The 10.6 failure found one fault, fixed (e027da1) before the other three:
the installer's line ends in a full stop and the answer added a second
("assistance.. Nobody").

## Consequences

- A failed install's build fails with Packer's "authentication error"
  wrapper around the guest's own words: "INSTALL FAILED: ... Its log is
  output-<release>-install.log".
- A key that does not match, or a guest whose real sshd refuses the
  build's key for any other reason, also fails after ten refusals rather
  than at the hour.
- An install that fails some other way (the installer crashing, rc.install
  rebooting into a loop) still waits out `install_timeout`; its log is
  still kept.

## What would change it

- A Packer qemu builder that watches QEMU, or reads a guest-to-host
  channel: a failed guest could then simply power off.
- An SDK that stops matching "authenticate" (the disconnect would no
  longer count; `refuse_test.go` names the dependency).
- A good build failing on refused logins: then something answered SSH
  before the key was in place.
