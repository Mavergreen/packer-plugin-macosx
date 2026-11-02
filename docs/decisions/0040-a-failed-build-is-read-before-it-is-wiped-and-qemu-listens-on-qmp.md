# 0040: A failed build is read before it is wiped, and every build's QEMU listens on a QMP socket

Date: 2026-11-01

Status: Accepted by the owner, 2026-11-01 (rulings R66, R67, R68).

## Context

Two failures happen in cold 10.9 base builds in CI, at about 2% and 3% a
build, and have not been explained. Failure A is an installer that stops
mid-install: Packer says "Timeout waiting for SSH", QEMU is idle, and the
serial log ends at a PackageKit request. Failure B is a guest that
authenticates a connection and then ends every command session on it: the
first `scp` exits 1 with nothing on stderr. By the time either fails, the
build directory is about to be wiped and QEMU is about to be killed, so
what the guest was doing was lost with them. A local measurement could not
reproduce either (six 10.9 base builds, none failed; a 95% upper bound of
about 39%), and the stall rate in CI belongs to a CPU the primary host is
not.

## Decisions

### 1. The gate reads a failed build before it wipes it (R66)

`bin/gate.sh` runs Packer with `-on-error=abort`, `PACKER_LOG=1` into the
build directory, and `-timestamp-ui`. When a build fails after QEMU
started, `bin/gate-build-evidence.sh` copies into
`evidence/<release>/build-base/` or `build-layer/`, before anything is
wiped: Packer's debug log, the installer's serial log, the host's view of
the live QEMU, a screenshot, the QMP read (decision 2), and then, with QEMU
stopped, the guest's own logs read from its disk without root. It never
keeps an image. A failure of the reader never changes the build's verdict.

The reader, `bin/gate-build-evidence.sh`, stands alone and ships in each template zip beside the
template (`templates/<os>/gate-build-evidence.sh`, R70), so the release's SHA256SUMS covers it and a
consumer that verifies the template verifies the reader. mavericks-vm's CI is meant to run it on every
cold build that stalls and keep its text and PNG for 30 days, from the zip it already downloads; that is
a proposal in mavericks-vm's PR #14 until that PR merges against a pin that carries the reader, and
until then no consumer runs it. A pin whose zip has no reader is detected and said so. There is no
further paid hunt: the capture waits for the failures to come to it.

### 2. Every template opens a QMP socket, in a directory only its owner can enter (R67, R71)

All three templates (`mavericks`, `snowleopard`, `mavericks-aquatransport`)
pass `-qmp unix:<socket>,server,nowait` as a `qemuargs` row, where `<socket>` is
the `socket` output of the plugin's `qmp-dir` data source
(`data "macosx-qmp-dir" "qmp" { dir = "<output dir>-qmp" }`). It is always on,
not a variable: an opt-in socket would be off on exactly the builds that fail
unexpectedly.

Who can connect, measured on QEMU 8.2.2 (2026-11-02): QEMU gives a unix
socket the mode 0777 minus the umask of the process that starts it (umask 000:
777; 002: 775; 022: 755; 077: 700), so the socket's own mode cannot be relied
on, and whoever connects to a QMP socket controls the VM (stop, reset, quit,
inspect its memory). R71: the socket is therefore made inside a directory that
only its owner can enter, so it is private under any umask.

The `qmp-dir` data source (`datasource/qmpdir`) makes that directory before
QEMU starts and returns its path and the socket's:
- Where: `<output dir>-qmp`, beside the output directory (not in it: the box
  is made from that directory), relative to the working directory as the
  output directory is. The path is deterministic, not a random temporary
  directory, so that every run of the data source (`packer build` and
  `packer validate -evaluate-datasources`, measured to be the only two:
  plain `validate` and `inspect` do not run data sources) finds the same
  directory and nothing piles up.
- Idempotent, and it never touches what is inside, so a run beside a build in
  flight changes nothing. It makes the directory with mode 0700 (no wider
  window to chmod it narrower) and then checks the directory it opened, not
  its path (`O_NOFOLLOW|O_DIRECTORY`, then `fstat`, `fchmod` on that
  descriptor), so a swap between a check and a use cannot be used: a symbolic
  link is refused (someone may have put it there to have the socket made
  elsewhere), a non-directory is refused, a directory owned by another user
  is refused and left as it is.
- A pre-existing directory of ours with a wider mode is repaired to 0700, not
  refused. This is safe: it is ours, so only we or root could have put anything
  in it, and QEMU unlinks a socket or file at the path before it binds
  (measured on QEMU 8.2.2), so a `qmp.sock` planted by an earlier run is
  replaced, not trusted.
- The parent (the build's own directory, not made here) is refused when it is
  writable by everyone, sticky or not, with a message saying so: in `/tmp`
  itself, another user could rename our directory away and put theirs in its
  place after the checks and before QEMU resolves the path. A parent writable
  by its group only is allowed: Ubuntu-style user-private groups with umask 002
  make that the usual case. RESIDUAL RISK, stated: a member of that group could
  do what the refusal prevents for everyone, swapping the directory between
  the data source's run and QEMU's bind (or, with the directory replaced after
  QEMU binds, nothing: the socket is already ours). It needs a user who is
  already trusted with the build's own directory, and it gives them control of
  the VM that is being built there; it is not closed by this design.
- Path length: QEMU binds the socket by the path it is given, and
  `sun_path` holds 104 bytes on macOS (the NUL included) and 108 on Linux. The
  path is therefore relative, as the output directory is, so that a deep
  working directory cannot lengthen it (`output-mavericks-aquatransport-qmp/qmp.sock`
  is 43 bytes), and the data source refuses a directory whose socket path
  would be over 103 bytes, naming the limit. QEMU resolves it against its working
  directory, which is packer's, as its other relative paths here are. Readers
  that must connect from elsewhere do so relative to the socket's directory
  (`bin/gate-build-evidence.sh qmp` enters it), so a long absolute path to it
  cannot defeat them either.
- Cleanup: nothing removes it. After a build, successful or not, what is left
  is the directory, empty, or holding a stale `qmp.sock` if QEMU was killed
  (QEMU unlinks a stale socket when it next binds one), mode 0700, beside the
  output directory, and no guest data. The next build in that working directory
  reuses it. The gate removes its work directory, and a CI runner's build
  directory goes with the runner; `/templates/*/output-*/`
  is already git-ignored for a checkout's own builds.
- The wrapper directories the gate and mavericks-vm briefly made mode 0700
  (R70 round) were dropped: the socket's privacy now comes from one mechanism,
  and a second one would hide which of them protects.

Why it does not change the guest, measured on the same QEMU (q35, `-S`, no guest OS): the device tree
(`info qtree`) and the PCI bus (`info pci`) are identical with and without the `-qmp` row, with
QEMU's default devices and with `-nodefaults`; the only difference is one more chardev,
`compat_monitor1: filename=disconnected:unix:<path>,server=on` (`info chardev`). It is not a `-device`
or a `-drive`, the machine shape checks of ADR 0035 pass unchanged, and the builder's own `qmp_enable`
and `qmp_socket_path` stay unset so the builder's use of QMP cannot take the path over (the template
test holds the exact row, once).

What the reader does with it (`bin/gate-build-evidence.sh qmp`): it only
asks. Twice, 30 seconds apart: `query-status`, `query-cpus-fast`,
`query-block`, `query-blockstats`, `query-pci`; through the human monitor
the interrupt controllers, registers, USB and device tree; the SATA
controller's registers decoded per port (a port with `PxCI` or `PxSACT` set
is marked OUTSTANDING); and a `screendump`. It writes what moved between
the two reads.

Where there is no socket (a template from before this decision), the
reader warns that there is none and moves on; it never fails the build. The gate and mavericks-vm
find it at `output-*-qmp/qmp.sock` beside the build's output directory.

### 3. What the first capture showed, and what is still open

One failure A was captured (EPYC 7763, 2026-11-01): both vCPUs in HLT at
the same idle address; no interrupt pending at the IOAPIC, the LAPICs or
the i8259 on a level-triggered SATA pin; no block operation since about 100
seconds after boot; the installer's progress bar still animating. That
rules out a lost interrupt. It does not say whether an AHCI command was
outstanding; the register decode in decision 2 was added for that, and no
stall has been captured with it yet. Failure B was captured twice: sshd
accepted the key and nothing after, on every connection; why is not
established.

### 4. No control hunt for failure B (R68)

The rate of B is tracked in consumer CI. If it stays above about 1 in 50,
a control run follows (the same cold builds on the previous pin, without
the socket), because only a control can show that the socket has no effect
on either rate. Until then the socket's effect on the guest is measured only as far as
decision 2 says (the same devices); its effect on the stall rates is not.

## Consequences

- A build that fails after QEMU started leaves the evidence above in the gate,
  and nothing that is an image. One that fails earlier (a download, a tool,
  QEMU never launched) keeps only its `packer-debug.log`: there is no guest to
  read.
- A plugin release carrying the socket is the owner's call.
- The configuration register has a row for the socket
  (`docs/configuration-register.md` §10).
