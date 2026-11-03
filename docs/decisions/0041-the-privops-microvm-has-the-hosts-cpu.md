# 0041: The privops microVM has the host's CPU

Date: 2026-11-03

Status: Accepted by the owner, 2026-11-03.

## Context

The media data source does its privileged filesystem work in a QEMU
microVM (`internal/privops`). The microVM boots the host's own kernel, with
the host's own statically linked busybox as init, and writes a disk image.
It ran with `-enable-kvm` and no `-cpu`, so it had QEMU's default CPU model,
qemu64, which has no AVX.

GitHub's `ubuntu-latest` runners are moving to Ubuntu 26.04. Its
busybox-static (1:1.37.0-7ubuntu1) is compiled with AVX throughout: objdump
decodes 7488 VEX-encoded instructions in it. A cold base build on such a
runner failed in the microVM's first pass. Init was killed by SIGILL on a
`vmovdqa`, and the kernel panicked ("Attempted to kill init!
exitcode=0x00000004"; mavericks-vm Actions run 41565179330, on an AMD EPYC
9V45). A probe built the same kind of microVM on eight 26.04 runners with
different CPUs: AMD EPYC 7763 and 9V74, and Intel Xeon Platinum 8573C among
them (mavericks-vm Actions run 41569584948). Under qemu64, busybox died of
SIGILL on all eight. Under `-cpu host`, it booted on all eight and ran every
applet tried. So the failure follows the runner's OS, not its CPU. Builds
that hit the cache never start the microVM and were unaffected.

## Decision

The microVM runs with `-cpu host`.

Everything the microVM executes was installed on the host, for the host's
CPU, so the host's CPU is the one it can run on. Its output is a filesystem
image, and nothing in it depends on the CPU that wrote it. The guest's
machine shape is a separate QEMU, with its own pinned `-cpu` line, and is
untouched: this concerns only the throwaway VM that does privileged file
work without host root.

This is not a workaround that would need to detect its need (it does not
paper over a fault). It corrects a mismatch: the microVM ran host binaries
on a CPU model chosen by nobody, which happened to offer less than the
hosts they came from. It needs no probe, and it adds no boot.

## Consequences

- Cold base builds work on Ubuntu 26.04 runners again, and on any host
  whose busybox needs more than qemu64 offers.
- `-cpu host` needs KVM, which the microVM already requires (`-enable-kvm`,
  and the requirements check on `/dev/kvm`); it adds no new requirement.
- `internal/privops/run_test.go` `TestRunGivesTheMicroVMTheHostsCPU` holds
  the flag.
