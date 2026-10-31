package privops

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Mavergreen/packer-plugin-macosx/internal/proc"
)

// What Ubuntu's Azure kernels build in, of DefaultModules; nls_utf8 and
// hfsplus are modules. 6.17's hfsplus is in linux-modules-extra, which a
// runner need not have; 7.0's is in linux-modules, which every kernel has
// (measured 2026-10-19 from the archive's packages).
var azureBuiltin = []string{"kernel/fs/nls/nls_base.ko", "kernel/drivers/virtio/virtio.ko",
	"kernel/drivers/virtio/virtio_ring.ko", "kernel/drivers/virtio/virtio_pci.ko", "kernel/drivers/block/virtio_blk.ko"}

// installed puts release rel under root: its kernel image when kernelMode
// is not -1, its modules.builtin when builtin is not nil, and each module
// file named.
func installed(t *testing.T, root, rel string, kernelMode os.FileMode, builtin []string, modules ...string) {
	t.Helper()
	dir := filepath.Join(root, "modules", rel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if kernelMode != ^os.FileMode(0) {
		write(t, filepath.Join(root, "boot", "vmlinuz-"+rel), []byte("kernel"), kernelMode)
	}
	if builtin != nil {
		write(t, filepath.Join(dir, "modules.builtin"), []byte(joinLines(builtin)), 0o644)
	}
	for _, m := range modules {
		write(t, filepath.Join(dir, "kernel", m), []byte(m), 0o644)
	}
}

const noKernel = ^os.FileMode(0)

// newFixtureBackend is newBackend on root/boot and root/modules, the
// running release being running, its log recorded.
func newFixtureBackend(t *testing.T, root, running string) (Backend, *logRecorder) {
	t.Helper()
	uname := &proc.Fake{Handle: func(c proc.Cmd) error {
		if c.Name == "uname" {
			_, _ = c.Stdout.Write([]byte(running + "\n"))
		}
		return nil
	}}
	lr := &logRecorder{}
	b, err := newBackend(uname, "qemu-system-x86_64", lr.logf, filepath.Join(root, "boot"), filepath.Join(root, "modules"))
	if err != nil {
		t.Fatal(err)
	}
	if b.GOOS != "linux" {
		t.Skip("the qemu-linux backend runs on Linux only")
	}
	return b, lr
}

// A running kernel whose modules have every one the microVM loads is the
// one it boots, however new another installed release is.
func TestTheRunningKernelIsBootedWhenItsModulesHaveThem(t *testing.T) {
	root := t.TempDir()
	installed(t, root, "7.0.0-1017-azure", 0o644, azureBuiltin, "fs/nls/nls_utf8.ko.zst", "fs/hfsplus/hfsplus.ko.zst")
	installed(t, root, "7.1.0-1-azure", 0o644, azureBuiltin, "fs/nls/nls_utf8.ko.zst", "fs/hfsplus/hfsplus.ko.zst")
	b, lr := newFixtureBackend(t, root, "7.0.0-1017-azure")
	if b.KVer != "7.0.0-1017-azure" {
		t.Fatalf("KVer = %q", b.KVer)
	}
	want := []string{"booting the running kernel, 7.0.0-1017-azure, whose modules have every one the microVM loads"}
	if !reflect.DeepEqual(lr.all(), want) {
		t.Fatalf("log %q", lr.all())
	}
}

// Without a modules.builtin nothing says a module is absent rather than
// built in: the running kernel is booted, as before, and the log says the
// question could not be asked.
func TestTheRunningKernelIsBootedWhenNothingSaysWhatItBuildsIn(t *testing.T) {
	root := t.TempDir()
	installed(t, root, "6.1.0-test", 0o644, nil)
	installed(t, root, "7.0.0-1017-azure", 0o644, azureBuiltin, "fs/nls/nls_utf8.ko.zst", "fs/hfsplus/hfsplus.ko.zst")
	b, lr := newFixtureBackend(t, root, "6.1.0-test")
	if b.KVer != "6.1.0-test" {
		t.Fatalf("KVer = %q", b.KVer)
	}
	want := []string{"booting the running kernel, 6.1.0-test: with no " + filepath.Join(root, "modules", "6.1.0-test", "modules.builtin") +
		", which modules it lacks is not known"}
	if !reflect.DeepEqual(lr.all(), want) {
		t.Fatalf("log %q", lr.all())
	}
	if got := b.Missing(); strings.Contains(strings.Join(got, "\n"), "module for") {
		t.Fatalf("Missing() names a module nobody knows is absent: %q", got)
	}
}

// Ubuntu's Azure 6.17 kernel without linux-modules-extra has no hfsplus:
// the microVM boots the newest other release whose kernel image it can
// read and whose modules have every one it loads, and says so. Newest is
// by number, not by string (6.10 is newer than 6.8). A release is
// passed over without a readable kernel image keyed to it, without a
// modules.builtin, or lacking a module itself.
func TestTheNewestCompleteReleaseIsBootedWhenTheRunningKernelLacksAModule(t *testing.T) {
	root := t.TempDir()
	complete := []string{"fs/nls/nls_utf8.ko.zst", "fs/hfsplus/hfsplus.ko.zst"}
	installed(t, root, "6.17.0-1022-azure", 0o644, azureBuiltin, "fs/nls/nls_utf8.ko.zst")
	installed(t, root, "6.8.0-1007-azure", 0o644, azureBuiltin, complete...)
	installed(t, root, "6.10.0-1-azure", 0o644, azureBuiltin, complete...)
	installed(t, root, "7.0.0-1017-azure", noKernel, azureBuiltin, complete...)
	installed(t, root, "7.1.0-1-azure", 0o644, nil, complete...)
	installed(t, root, "7.2.0-1-azure", 0o644, azureBuiltin, "fs/nls/nls_utf8.ko.zst")
	if os.Geteuid() != 0 { // root reads a mode-0 file
		installed(t, root, "7.3.0-1-azure", 0, azureBuiltin, complete...)
	}
	write(t, filepath.Join(root, "modules", "7.4.0-1-azure"), []byte("not a tree"), 0o644)
	b, lr := newFixtureBackend(t, root, "6.17.0-1022-azure")
	if b.KVer != "6.10.0-1-azure" {
		t.Fatalf("KVer = %q, want 6.10.0-1-azure", b.KVer)
	}
	if k, err := b.Kernel(); err != nil || k != filepath.Join(root, "boot", "vmlinuz-6.10.0-1-azure") {
		t.Fatalf("Kernel() = %q, %v", k, err)
	}
	want := []string{"the running kernel, 6.17.0-1022-azure, lacks hfsplus (no module, and not in its modules.builtin): " +
		"booting 6.10.0-1-azure instead, the newest installed release whose modules have every one the microVM loads"}
	if !reflect.DeepEqual(lr.all(), want) {
		t.Fatalf("log %q", lr.all())
	}
}

// With no release to turn to, the running kernel stays, and Missing names
// the module before the microVM boots, rather than its mount failing.
func TestAModuleNoInstalledReleaseHasIsMissing(t *testing.T) {
	root := t.TempDir()
	installed(t, root, "6.17.0-1022-azure", 0o644, azureBuiltin)
	b, lr := newFixtureBackend(t, root, "6.17.0-1022-azure")
	if b.KVer != "6.17.0-1022-azure" {
		t.Fatalf("KVer = %q", b.KVer)
	}
	want := []string{"the running kernel, 6.17.0-1022-azure, lacks nls_utf8, hfsplus (no module, and not in its modules.builtin), " +
		"and no other installed release with a readable kernel image has them all"}
	if !reflect.DeepEqual(lr.all(), want) {
		t.Fatalf("log %q", lr.all())
	}
	b.Runner = &proc.Fake{Paths: map[string]string{"qemu-system-x86_64": "/usr/bin/qemu-system-x86_64", "busybox": staticELF(t)}}
	b.KVMDevice = ""
	dir := filepath.Join(root, "modules", "6.17.0-1022-azure")
	remedy := "(Ubuntu: linux-modules-extra-6.17.0-1022-azure; or another installed kernel that has it, its image under " +
		filepath.Join(root, "boot") + " readable by this user)"
	wantMissing := []string{
		"the nls_utf8 module for 6.17.0-1022-azure: not under " + dir + ", and not built in by its modules.builtin " + remedy,
		"the hfsplus module for 6.17.0-1022-azure: not under " + dir + ", and not built in by its modules.builtin " + remedy,
	}
	if got := b.Missing(); !reflect.DeepEqual(got, wantMissing) {
		t.Fatalf("Missing()\n got %q\nwant %q", got, wantMissing)
	}
}

func TestNewerRelease(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"6.10.0-1-azure", "6.8.0-1007-azure", true},
		{"6.8.0-1007-azure", "6.10.0-1-azure", false},
		{"7.0.0-1017-azure", "6.17.0-1022-azure", true},
		{"6.17.0-1022-azure", "6.17.0-1021-azure", true},
		{"6.17.0-1022-azure", "6.17.0-1022-azure", false},
		{"7.2.11-3-t2-noble", "7.2.9-2-t2-noble", true},
		// equal numbers written differently decide nothing: the next run does
		{"1.01.4", "1.1.3", true},
		{"1.01.2", "1.1.3", false},
	} {
		if got := newerRelease(tc.a, tc.b); got != tc.want {
			t.Errorf("newerRelease(%s, %s) = %v", tc.a, tc.b, got)
		}
	}
}

// A module named twice is lacking once, and named once.
func TestAModuleNamedTwiceIsMissingOnce(t *testing.T) {
	root := t.TempDir()
	installed(t, root, "6.17.0-1022-azure", 0o644, azureBuiltin, "fs/nls/nls_utf8.ko.zst")
	b := fixtureBackend(root)
	b.KVer, b.Modules = "6.17.0-1022-azure", []string{"hfsplus", "nls_utf8", "hfsplus"}
	if lack, known := b.lacking(b.KVer); !known || !reflect.DeepEqual(lack, []string{"hfsplus"}) {
		t.Fatalf("lacking = %q, %v", lack, known)
	}
}
