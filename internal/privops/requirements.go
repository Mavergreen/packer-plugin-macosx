package privops

import (
	"debug/elf"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// KernelCandidates is every path a kernel image might be at, most
// specific first. The first three carry the running release, so the
// modules staged from ModulesDir/KVer load into them; the rest are
// generic names distributions use (Arch's vmlinuz-linux, Alpine's
// vmlinuz, Gentoo's kernel-*), tried only when no keyed one exists,
// because a kernel of another release rejects every module and the
// failure then looks like an HFS+ problem.
func (b Backend) KernelCandidates() []string {
	c := []string{
		filepath.Join(b.BootDir, "vmlinuz-"+b.KVer),
		filepath.Join(b.ModulesDir, b.KVer, "vmlinuz"),
		filepath.Join(b.BootDir, "kernel-"+b.KVer),
		filepath.Join(b.BootDir, "vmlinuz-linux"),
		filepath.Join(b.BootDir, "vmlinuz"),
	}
	return append(c, b.kernelGlob()...)
}

// kernelGlob is BootDir/kernel-*: every name in BootDir that starts
// kernel-, sorted, or the pattern itself when none does, so the
// candidates still say where they looked. The directory is read rather
// than globbed so that a BootDir holding a glob character is taken
// literally.
func (b Backend) kernelGlob() []string {
	var names []string
	if ents, err := os.ReadDir(b.BootDir); err == nil {
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), "kernel-") {
				names = append(names, filepath.Join(b.BootDir, e.Name()))
			}
		}
	}
	if len(names) == 0 {
		return []string{filepath.Join(b.BootDir, "kernel-*")}
	}
	sort.Strings(names)
	return names
}

// Kernel is the first candidate that is a readable regular file.
func (b Backend) Kernel() (string, error) {
	for _, c := range b.KernelCandidates() {
		if readableFile(c) {
			return c, nil
		}
	}
	return "", fmt.Errorf("no readable kernel image for %s (looked for: %s)", b.KVer, strings.Join(b.KernelCandidates(), " "))
}

// readableFile reports whether p is a regular file this process can
// read: symlinks followed, and read permission proved by opening it.
func readableFile(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	fi, err := f.Stat()
	return err == nil && fi.Mode().IsRegular()
}

// KernelIsKeyed says whether path carries the running release. A generic
// name may well be the running kernel -- on a host with one kernel
// installed it always is -- so this decides whether to warn, not whether
// to proceed.
func (b Backend) KernelIsKeyed(path string) bool {
	for _, k := range b.KernelCandidates()[:3] {
		if path == k {
			return true
		}
	}
	return false
}

// BusyboxLinkage reads an executable's ELF headers, not ldd's opinion of
// them: a PT_INTERP program header names a
// dynamic loader, which an initramfs holding one binary does not have.
// static, dynamic, or unknown (not ELF, or unreadable) -- and unknown is
// not reported as missing: refusing to build on a guess would be a worse
// failure than the one this catches.
func BusyboxLinkage(path string) string {
	f, err := elf.Open(path)
	if err != nil {
		return "unknown"
	}
	defer f.Close()
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return "dynamic"
		}
	}
	return "static"
}

// Missing is one line per unmet requirement, and nothing when the backend
// can run here. Every one is named, not just the first: a report naming
// one of several costs a round trip per guess. A KVM device this user
// cannot write is among them, since QEMU runs with -enable-kvm; cpio is
// not: the initramfs is written in Go.
func (b Backend) Missing() []string {
	if b.GOOS != "linux" {
		return []string{fmt.Sprintf("the qemu-linux privops backend (it boots a Linux kernel with its own modules; this host is %s)", b.GOOS)}
	}
	var m []string
	if _, err := b.Runner.LookPath(b.QEMU); err != nil {
		m = append(m, b.QEMU+" (not on PATH)")
	}
	if bb, err := b.Runner.LookPath("busybox"); err != nil {
		m = append(m, "busybox (not on PATH)")
	} else if BusyboxLinkage(bb) == "dynamic" {
		// Named as a different requirement from a missing busybox,
		// because "install busybox" is the wrong advice to a reader who
		// has one.
		m = append(m, fmt.Sprintf("a statically linked busybox: %s is dynamically linked, and the initramfs has no loader or libraries for it (Debian: busybox-static)", bb))
	}
	if _, err := b.Kernel(); err != nil {
		m = append(m, fmt.Sprintf("a readable kernel image for %s (looked for: %s)", b.KVer, strings.Join(b.KernelCandidates(), " ")))
	}
	if l := b.kvmMissing(); l != "" {
		m = append(m, l)
	}
	return m
}

// kvmMissing is why QEMU's -enable-kvm would fail here, or "". Access is
// asked with the real uid, as open(2) will check it; a device node is
// never opened here, since opening one is not a question.
func (b Backend) kvmMissing() string {
	if b.KVMDevice == "" || syscall.Access(b.KVMDevice, 2) == nil { // 2 is W_OK
		return ""
	}
	if _, err := os.Stat(b.KVMDevice); err != nil {
		return b.KVMDevice + " does not exist (is the kvm module loaded? on a VM, is nested virtualisation on?)"
	}
	return b.KVMDevice + " is not writable by this user (is this user in group kvm? on a VM, is nested virtualisation on?)"
}
