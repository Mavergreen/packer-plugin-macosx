// Package privops does privileged filesystem work without host privilege:
// it boots a busybox initramfs under QEMU, with the host's own kernel,
// attaches the images as virtio disks, and runs a payload script there
// as uid 0. Building macOS installer media needs root-owned HFS+ files,
// and Linux gives an unprivileged user no way to write them; a VM where
// we are genuinely root does.
//
// The host side is Go; the guest side is busybox shell -- /init
// (assets/privops/init.sh) and the caller's payload. The two speak
// through the disks and through marker lines on the serial console.
package privops

import (
	"bytes"
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/Mavergreen/packer-plugin-mavericks/internal/proc"
)

// DefaultModules are loaded in the guest, with their dependencies:
// hfsplus and the NLS it needs, and virtio for distributions that build
// it as modules (Debian builds it in; staging a built-in module costs a
// warning).
var DefaultModules = []string{"nls_base", "nls_utf8", "hfsplus", "virtio", "virtio_ring", "virtio_pci", "virtio_blk"}

// Backend is the qemu-linux privops backend on one host. Where to look
// for the kernel and modules are fields here, for tests; none is an
// environment variable.
//
// Make one with NewBackend, and change what a test needs afterwards. A
// zero field is not a default -- an empty BootDir is the working
// directory, and a zero MemMiB is passed to QEMU as -m 0 -- except
// Timeout, whose zero Run reads as DefaultTimeout, and KVMDevice, whose
// "" is not checked. The staged modules are kept for the Backend's
// lifetime, and its copies', only when NewBackend made it.
type Backend struct {
	Runner     proc.Runner
	QEMU       string        // the qemu binary (config.QEMU)
	BootDir    string        // where kernels are: /boot
	ModulesDir string        // where modules are: /lib/modules
	KVer       string        // the running kernel's release, uname -r
	Modules    []string      // loaded in the guest: DefaultModules
	MemMiB     int           // the microVM's memory: 512
	Timeout    time.Duration // the bound on one pass: 15m
	GOOS       string        // runtime.GOOS: the backend runs on linux only
	KVMDevice  string        // what -enable-kvm opens: /dev/kvm; "" is not checked
	Log        func(string, ...any)

	// gzipLevel compresses the initramfs; 0 means gzip.BestCompression
	// (gzip -9). Only tests set it: level 9 under the race
	// detector costs seconds per archive.
	gzipLevel int
	// staged is the modules, once resolved and read (stagedModules); nil
	// stages them afresh on every Run.
	staged *moduleCache
}

// NewBackend is a Backend for this host: /boot, /lib/modules, /dev/kvm,
// the running kernel's release, 512 MiB, a 15-minute bound on a pass.
// Off Linux it asks nothing, and Missing says why it cannot run.
func NewBackend(r proc.Runner, qemu string, log func(string, ...any)) (Backend, error) {
	b := Backend{Runner: r, QEMU: qemu, BootDir: "/boot", ModulesDir: "/lib/modules", KVMDevice: "/dev/kvm",
		Modules: DefaultModules, MemMiB: 512, Timeout: DefaultTimeout, GOOS: runtime.GOOS, Log: log,
		staged: &moduleCache{}}
	if b.GOOS != "linux" {
		return b, nil
	}
	var out bytes.Buffer
	if err := r.Run(context.Background(), proc.Cmd{Name: "uname", Args: []string{"-r"}, Stdout: &out}); err != nil {
		return b, fmt.Errorf("cannot ask uname for the kernel release: %w", err)
	}
	b.KVer = strings.TrimSpace(out.String())
	return b, nil
}
