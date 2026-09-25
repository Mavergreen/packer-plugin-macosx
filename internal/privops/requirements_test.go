package privops

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Mavergreen/packer-plugin-mavericks/internal/proc"
)

const testKVer = "6.1.0-test"

// fixtureBackend is a Backend looking at root/boot and root/modules for
// release 6.1.0-test, with nothing on its fake PATH.
func fixtureBackend(root string) Backend {
	return Backend{
		Runner:     &proc.Fake{Paths: map[string]string{}},
		QEMU:       "qemu-system-x86_64",
		BootDir:    filepath.Join(root, "boot"),
		ModulesDir: filepath.Join(root, "modules"),
		KVer:       testKVer,
		Modules:    DefaultModules,
		MemMiB:     512,
		GOOS:       "linux",
	}
}

// Where distributions put the kernel: each layout alone is found, and a
// kernel keyed to the running release beats a generic name even when
// both are there.
func TestKernelDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []string
		want  string // "" means no kernel
	}{
		{"Debian", []string{"boot/vmlinuz-6.1.0-test"}, "boot/vmlinuz-6.1.0-test"},
		{"Arch", []string{"boot/vmlinuz-linux"}, "boot/vmlinuz-linux"},
		{"Arch's copy under the modules", []string{"modules/6.1.0-test/vmlinuz"}, "modules/6.1.0-test/vmlinuz"},
		{"Gentoo", []string{"boot/kernel-6.1.0-test"}, "boot/kernel-6.1.0-test"},
		{"Alpine", []string{"boot/vmlinuz"}, "boot/vmlinuz"},
		{"Gentoo, another release", []string{"boot/kernel-other"}, "boot/kernel-other"},
		{"none", nil, ""},
		{"keyed beats generic", []string{"boot/vmlinuz-linux", "boot/vmlinuz-6.1.0-test"}, "boot/vmlinuz-6.1.0-test"},
		{"the modules' copy beats generic", []string{"boot/vmlinuz", "modules/6.1.0-test/vmlinuz"}, "modules/6.1.0-test/vmlinuz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, f := range tc.files {
				write(t, filepath.Join(root, f), []byte("kernel"), 0o644)
			}
			b := fixtureBackend(root)
			got, err := b.Kernel()
			if tc.want == "" {
				if err == nil || got != "" {
					t.Fatalf("found %q with no kernel present", got)
				}
				if !strings.Contains(err.Error(), "6.1.0-test") {
					t.Errorf("error does not name the release: %v", err)
				}
				return
			}
			if err != nil || got != filepath.Join(root, tc.want) {
				t.Fatalf("Kernel() = %q, %v; want %s", got, err, tc.want)
			}
		})
	}
}

// A kernel image nobody can read is not a kernel this backend can boot.
func TestAnUnreadableKernelIsSkipped(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-0 file")
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "boot/vmlinuz-6.1.0-test"), []byte("kernel"), 0)
	write(t, filepath.Join(root, "boot/vmlinuz"), []byte("kernel"), 0o644)
	got, err := fixtureBackend(root).Kernel()
	if err != nil || got != filepath.Join(root, "boot/vmlinuz") {
		t.Fatalf("Kernel() = %q, %v", got, err)
	}
}

// goldenKernel reads testdata/golden/kernel-<slug>.txt: two blocks,
// "CANDIDATES" and "KERNEL", the kernel candidates in order and the one
// chosen from them for the matching fixture (an empty KERNEL block: none),
// with the fixture's own tempdir root written as the placeholder @ROOT@
// (substituted back in below).
func goldenKernel(t *testing.T, slug, root string) (candidates []string, kernel string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repo(t), "internal", "privops", "testdata", "golden", "kernel-"+slug+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(b), "@ROOT@", root)
	_, rest, ok := strings.Cut(text, "CANDIDATES\n")
	if !ok {
		t.Fatalf("no CANDIDATES block in %s", slug)
	}
	c, k, ok := strings.Cut(rest, "KERNEL\n")
	if !ok {
		t.Fatalf("no KERNEL block in %s", slug)
	}
	if c = strings.TrimSuffix(c, "\n"); c != "" {
		candidates = strings.Split(c, "\n")
	}
	return candidates, strings.TrimSuffix(k, "\n")
}

// The candidates, and the kernel chosen from them, are the goldens', for
// each fixture root.
func TestKernelCandidatesMatchTheirGolden(t *testing.T) {
	for _, tc := range []struct {
		name  string
		slug  string
		files []string
	}{
		{"nothing, so the glob stays literal", "nothing", nil},
		{"the glob expands", "glob-expands", []string{"boot/kernel-other", "boot/kernel-zzz"}},
		{"keyed and generic", "keyed-and-generic", []string{"boot/vmlinuz-linux", "boot/kernel-6.1.0-test", "boot/vmlinuz"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, f := range tc.files {
				write(t, filepath.Join(root, f), []byte("kernel"), 0o644)
			}
			b := fixtureBackend(root)
			want, kernel := goldenKernel(t, tc.slug, root)
			if got := b.KernelCandidates(); !reflect.DeepEqual(got, want) {
				t.Fatalf("candidates\n got %q\nwant %q", got, want)
			}
			got, gerr := b.Kernel()
			if (kernel == "") != (gerr != nil) || kernel != got {
				t.Fatalf("kernel: golden %q, Go %q (%v)", kernel, got, gerr)
			}
		})
	}
}

// Only a path carrying the running release counts as keyed: a generic
// name may be another build, whose modules would not load.
func TestOnlyKeyedPathsCountAsKeyed(t *testing.T) {
	root := t.TempDir()
	b := fixtureBackend(root)
	for p, want := range map[string]bool{
		"boot/vmlinuz-6.1.0-test":    true,
		"modules/6.1.0-test/vmlinuz": true,
		"boot/kernel-6.1.0-test":     true,
		"boot/vmlinuz-linux":         false,
		"boot/vmlinuz":               false,
		"boot/kernel-other":          false,
	} {
		if got := b.KernelIsKeyed(filepath.Join(root, p)); got != want {
			t.Errorf("KernelIsKeyed(%s) = %v, want %v", p, got, want)
		}
	}
}

// Linkage is read from the ELF headers: PT_INTERP is dynamic,
// none is static, and anything else is unknown.
func TestBusyboxLinkage(t *testing.T) {
	if got := BusyboxLinkage(staticELF(t)); got != "static" {
		t.Errorf("static fixture: %s", got)
	}
	if got := BusyboxLinkage(dynamicELF(t)); got != "dynamic" {
		t.Errorf("dynamic fixture: %s", got)
	}
	text := filepath.Join(t.TempDir(), "busybox")
	write(t, text, []byte("#!/bin/sh\necho I am not busybox\n"), 0o755)
	if got := BusyboxLinkage(text); got != "unknown" {
		t.Errorf("a script: %s", got)
	}
	if got := BusyboxLinkage(filepath.Join(t.TempDir(), "nothing")); got != "unknown" {
		t.Errorf("a missing file: %s", got)
	}
}

// Every unmet requirement is named, not just the first: a report naming
// one of several costs a round trip per guess.
func TestMissingNamesEveryRequirement(t *testing.T) {
	root := t.TempDir()
	b := fixtureBackend(root)
	want := []string{
		"qemu-system-x86_64 (not on PATH)",
		"busybox (not on PATH)",
		"a readable kernel image for 6.1.0-test (looked for: " + strings.Join(b.KernelCandidates(), " ") + ")",
	}
	if got := b.Missing(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Missing()\n got %q\nwant %q", got, want)
	}
	for _, l := range b.Missing() {
		if strings.Contains(l, "cpio") {
			t.Errorf("cpio is not a requirement (the initramfs is written in Go): %q", l)
		}
	}
	if !strings.Contains(want[2], filepath.Join(root, "boot", "kernel-*")) {
		t.Errorf("the kernel line does not list the glob: %q", want[2])
	}
}

// A dynamic busybox passes every other check and then cannot exec /init
// inside the microVM: it is named as the wrong busybox, not a missing one.
func TestMissingReportsADynamicBusyboxAsTheWrongOne(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "boot/vmlinuz-6.1.0-test"), []byte("kernel"), 0o644)
	dyn := dynamicELF(t)
	b := fixtureBackend(root)
	b.Runner = &proc.Fake{Paths: map[string]string{"qemu-system-x86_64": "/usr/bin/qemu-system-x86_64", "busybox": dyn}}
	want := []string{"a statically linked busybox: " + dyn + " is dynamically linked, and the initramfs has no loader or libraries for it (Debian: busybox-static)"}
	if got := b.Missing(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Missing()\n got %q\nwant %q", got, want)
	}
}

// A busybox whose linkage cannot be read is not reported: refusing to
// build on a guess would be a worse failure than the one this catches.
func TestMissingDoesNotGuessAtAnUnknownBusybox(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "boot/vmlinuz-6.1.0-test"), []byte("kernel"), 0o644)
	script := filepath.Join(root, "busybox")
	write(t, script, []byte("#!/bin/sh\n"), 0o755)
	b := fixtureBackend(root)
	b.Runner = &proc.Fake{Paths: map[string]string{"qemu-system-x86_64": "/q", "busybox": script}}
	if got := b.Missing(); len(got) != 0 {
		t.Fatalf("Missing() = %q", got)
	}
}

func TestNothingIsMissingWhenAllIsMet(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "boot/vmlinuz-6.1.0-test"), []byte("kernel"), 0o644)
	b := fixtureBackend(root)
	b.Runner = &proc.Fake{Paths: map[string]string{"qemu-system-x86_64": "/usr/bin/qemu-system-x86_64", "busybox": staticELF(t)}}
	if got := b.Missing(); len(got) != 0 {
		t.Fatalf("Missing() = %q", got)
	}
}

// The backend boots a Linux kernel with its own modules: on another OS
// that is the one thing missing, and nothing else is probed.
func TestMissingOnAnotherOS(t *testing.T) {
	f := &proc.Fake{}
	b := fixtureBackend(t.TempDir())
	b.Runner = f
	b.GOOS = "darwin"
	want := []string{"the qemu-linux privops backend (it boots a Linux kernel with its own modules; this host is darwin)"}
	if got := b.Missing(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Missing() = %q", got)
	}
	if len(f.Calls) != 0 {
		t.Fatalf("probed on darwin: %v", f.Calls)
	}
}

// NewBackend asks uname for the running release and fills in the
// default kernel and module directories.
func TestNewBackendReadsTheReleaseFromUname(t *testing.T) {
	f := &proc.Fake{Handle: func(c proc.Cmd) error {
		if c.Name == "uname" && reflect.DeepEqual(c.Args, []string{"-r"}) {
			_, _ = c.Stdout.Write([]byte("6.1.0-test\n"))
		}
		return nil
	}}
	b, err := NewBackend(f, "qemu-system-x86_64", nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.GOOS != "linux" {
		if len(f.Calls) != 0 {
			t.Fatalf("asked uname on %s: %v", b.GOOS, f.Calls)
		}
		return
	}
	if b.KVer != "6.1.0-test" || b.BootDir != "/boot" || b.ModulesDir != "/lib/modules" || b.KVMDevice != "/dev/kvm" ||
		b.MemMiB != 512 || b.Timeout != DefaultTimeout || DefaultTimeout != 15*time.Minute || !reflect.DeepEqual(b.Modules, DefaultModules) {
		t.Fatalf("%+v", b)
	}
	if strings.Join(DefaultModules, " ") != "nls_base nls_utf8 hfsplus virtio virtio_ring virtio_pci virtio_blk" {
		t.Fatalf("DefaultModules = %v", DefaultModules)
	}
}

// The microVM boots with -enable-kvm: a KVM device this user cannot open
// is a requirement unmet, named with what to look at. "" is not checked.
func TestMissingNamesTheKVMDevice(t *testing.T) {
	met := func(t *testing.T) Backend {
		root := t.TempDir()
		write(t, filepath.Join(root, "boot/vmlinuz-6.1.0-test"), []byte("kernel"), 0o644)
		b := fixtureBackend(root)
		b.Runner = &proc.Fake{Paths: map[string]string{"qemu-system-x86_64": "/usr/bin/qemu-system-x86_64", "busybox": staticELF(t)}}
		return b
	}
	t.Run("absent", func(t *testing.T) {
		b := met(t)
		b.KVMDevice = filepath.Join(t.TempDir(), "kvm")
		want := []string{b.KVMDevice + " does not exist (is the kvm module loaded? on a VM, is nested virtualisation on?)"}
		if got := b.Missing(); !reflect.DeepEqual(got, want) {
			t.Fatalf("Missing()\n got %q\nwant %q", got, want)
		}
	})
	t.Run("present, not writable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root writes a mode-0444 file")
		}
		b := met(t)
		b.KVMDevice = filepath.Join(t.TempDir(), "kvm")
		write(t, b.KVMDevice, nil, 0o444)
		want := []string{b.KVMDevice + " is not writable by this user (is this user in group kvm? on a VM, is nested virtualisation on?)"}
		if got := b.Missing(); !reflect.DeepEqual(got, want) {
			t.Fatalf("Missing()\n got %q\nwant %q", got, want)
		}
	})
	t.Run("writable", func(t *testing.T) {
		b := met(t)
		b.KVMDevice = filepath.Join(t.TempDir(), "kvm")
		write(t, b.KVMDevice, nil, 0o666)
		if got := b.Missing(); len(got) != 0 {
			t.Fatalf("Missing() = %q", got)
		}
	})
	t.Run("not checked", func(t *testing.T) {
		if got := met(t).Missing(); len(got) != 0 {
			t.Fatalf("Missing() = %q with KVMDevice unset", got)
		}
	})
}
