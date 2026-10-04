package privops

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mavergreen/packer-plugin-macosx"
	"github.com/Mavergreen/packer-plugin-macosx/internal/proc"
)

// logRecorder collects what a Backend logs.
type logRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (l *logRecorder) logf(f string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(f, a...))
}

func (l *logRecorder) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

// runFixture is a Backend that can "run" here: a static busybox fixture
// and qemu on the fake PATH, a kernel keyed to 6.1.0-test, an empty
// module tree, and a log that is recorded.
type runFixture struct {
	b    Backend
	fake *proc.Fake
	log  *logRecorder
	root string
	bb   string
}

func newRunFixture(t *testing.T) *runFixture {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "boot/vmlinuz-6.1.0-test"), []byte("kernel"), 0o644)
	if err := os.MkdirAll(filepath.Join(root, "modules", testKVer), 0o755); err != nil {
		t.Fatal(err)
	}
	bb := staticELF(t)
	f := &proc.Fake{Paths: map[string]string{"qemu-system-x86_64": "/usr/bin/qemu-system-x86_64", "busybox": bb}}
	lr := &logRecorder{}
	b := fixtureBackend(root)
	b.Runner, b.Log, b.Timeout = f, lr.logf, time.Minute
	// Level 9 under the race detector costs seconds per test, and these
	// tests read the archive back, whatever its compression.
	b.gzipLevel = gzip.BestSpeed
	return &runFixture{b: b, fake: f, log: lr, root: root, bb: bb}
}

// cpioEntry is one member of a newc archive, as the kernel reads it.
type cpioEntry struct {
	name     string
	mode     uint32
	uid, gid uint32
	data     []byte
}

// readNewc reads a gzip'd newc archive back. The standard library has no
// newc reader: each member is a 110-byte ASCII header, "070701" and
// thirteen 8-digit hex fields, then the NUL-terminated name and the data,
// each padded to a multiple of four bytes.
func readNewc(t *testing.T, gz []byte) []cpioEntry {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatalf("not gzip: %v", err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	align := func(n int) int { return (n + 3) &^ 3 }
	var out []cpioEntry
	for off := 0; ; {
		if off+110 > len(b) {
			t.Fatalf("archive ends at %d without a trailer", off)
		}
		h := b[off : off+110]
		if string(h[:6]) != "070701" {
			t.Fatalf("bad magic %q at %d", h[:6], off)
		}
		field := func(i int) uint32 {
			v, err := strconv.ParseUint(string(h[6+8*i:14+8*i]), 16, 32)
			if err != nil {
				t.Fatalf("header field %d at %d: %v", i, off, err)
			}
			return uint32(v)
		}
		mode, uid, gid, size, namesize := field(1), field(2), field(3), int(field(6)), int(field(11))
		name := string(b[off+110 : off+110+namesize-1])
		if b[off+110+namesize-1] != 0 {
			t.Fatalf("name %q not NUL-terminated", name)
		}
		off = align(off + 110 + namesize)
		data := b[off : off+size]
		off = align(off + size)
		out = append(out, cpioEntry{name: name, mode: mode, uid: uid, gid: gid, data: data})
		if name == "TRAILER!!!" {
			if off != len(b) {
				t.Fatalf("%d bytes after the trailer", len(b)-off)
			}
			return out
		}
	}
}

func byName(es []cpioEntry) map[string]cpioEntry {
	m := map[string]cpioEntry{}
	for _, e := range es {
		m[e.name] = e
	}
	return m
}

func TestInitramfsHoldsWhatTheGuestNeeds(t *testing.T) {
	fx := newRunFixture(t)
	payload := []byte("echo payload\n")
	gz, err := fx.b.buildInitramfs(context.Background(), payload, []string{"ro", "raw"})
	if err != nil {
		t.Fatal(err)
	}
	es := readNewc(t, gz)
	m := byName(es)
	bb, _ := os.ReadFile(fx.bb)
	initScript, _ := fs.ReadFile(macosx.Files, "assets/privops/init.sh")
	for name, want := range map[string]struct {
		mode uint32
		data []byte
	}{
		"bin/busybox": {0o100755, bb},
		"init":        {0o100755, initScript},
		"payload.sh":  {0o100644, payload},
		"disk-roles":  {0o100644, []byte("ro\nraw\n")},
	} {
		e, ok := m[name]
		if !ok {
			t.Errorf("no %s", name)
			continue
		}
		if e.mode != want.mode || !bytes.Equal(e.data, want.data) {
			t.Errorf("%s: mode %o, %d bytes; want %o, %d bytes", name, e.mode, len(e.data), want.mode, len(want.data))
		}
	}
	for _, d := range []string{"dev", "proc", "sys", "mnt", "lib/modules"} {
		if e, ok := m[d]; !ok || e.mode&0o170000 != 0o040000 {
			t.Errorf("no directory %s", d)
		}
	}
	if es[len(es)-1].name != "TRAILER!!!" {
		t.Errorf("last entry %q", es[len(es)-1].name)
	}
	seen := map[string]bool{}
	for _, e := range es {
		if e.uid != 0 || e.gid != 0 {
			t.Errorf("%s is owned by %d:%d", e.name, e.uid, e.gid)
		}
		// The kernel unpacks in order: a directory before what is in it.
		if dir := filepath.Dir(e.name); dir != "." && e.name != "TRAILER!!!" && !seen[dir] {
			t.Errorf("%s comes before its directory %s", e.name, dir)
		}
		seen[e.name] = true
	}

	gz, err = fx.b.buildInitramfs(context.Background(), payload, nil)
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := byName(readNewc(t, gz))["disk-roles"]; !ok || len(e.data) != 0 {
		t.Errorf("disk-roles with no disks: %q (present %v)", e.data, ok)
	}
}

// The archive is newc as cpio(1) itself reads it, not only as this
// package's test reader does: the two could share a misreading.
func TestInitramfsIsReadByCpio(t *testing.T) {
	if _, err := exec.LookPath("cpio"); err != nil {
		t.Skip("cpio not installed")
	}
	fx := newRunFixture(t)
	gz, err := fx.b.buildInitramfs(context.Background(), []byte("true\n"), []string{"ro"})
	if err != nil {
		t.Fatal(err)
	}
	zr, _ := gzip.NewReader(bytes.NewReader(gz))
	raw, _ := io.ReadAll(zr)
	cmd := exec.Command("cpio", "-it", "--quiet")
	cmd.Stdin = bytes.NewReader(raw)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("cpio -it: %v", err)
	}
	want := "bin\ndev\nproc\nsys\nmnt\nlib\nlib/modules\nbin/busybox\nlib/modules/load-order\ndisk-roles\ninit\npayload.sh\n"
	if string(out) != want {
		t.Fatalf("cpio lists\n%s\nwant\n%s", out, want)
	}
}

func gzipped(t *testing.T, data []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	_, _ = zw.Write(data)
	_ = zw.Close()
	return b.Bytes()
}

// Modules are staged in the order modprobe gives, with their
// dependencies, decompressed, and each once however often it is named.
func TestInitramfsStagesModulesInModprobesOrder(t *testing.T) {
	fx := newRunFixture(t)
	mdir := filepath.Join(fx.root, "modules", testKVer, "kernel", "fs")
	write(t, filepath.Join(mdir, "nls_base.ko"), []byte("nls_base object"), 0o644)
	write(t, filepath.Join(mdir, "hfsplus.ko.gz"), gzipped(t, []byte("hfsplus object")), 0o644)
	fx.fake.Paths["modprobe"] = "/sbin/modprobe"
	fx.fake.Handle = func(c proc.Cmd) error {
		if c.Name == "modprobe" && reflect.DeepEqual(c.Args, []string{"-S", testKVer, "-n", "--show-depends", "hfsplus"}) {
			fmt.Fprintf(c.Stdout, "insmod %s \ninsmod %s \n", filepath.Join(mdir, "nls_base.ko"), filepath.Join(mdir, "hfsplus.ko.gz"))
		}
		return nil
	}
	fx.b.Modules = []string{"hfsplus", "nls_base", "hfsplus"}
	gz, err := fx.b.buildInitramfs(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := byName(readNewc(t, gz))
	if got := string(m["lib/modules/load-order"].data); got != "nls_base.ko\nhfsplus.ko\n" {
		t.Fatalf("load-order %q", got)
	}
	if got := string(m["lib/modules/hfsplus.ko"].data); got != "hfsplus object" {
		t.Fatalf("hfsplus.ko staged as %q", got)
	}
	if got := string(m["lib/modules/nls_base.ko"].data); got != "nls_base object" {
		t.Fatalf("nls_base.ko staged as %q", got)
	}
	if _, ok := m["lib/modules/hfsplus.ko.gz"]; ok {
		t.Fatal("the compressed module was staged as is")
	}
	n := 0
	for _, c := range fx.fake.Calls {
		if c.Name == "modprobe" {
			n++
		}
	}
	if n != 3 {
		t.Errorf("modprobe asked %d times, want once per module named", n)
	}
}

// With no modprobe, a module is found by name under ModulesDir/KVer.
func TestInitramfsFallsBackToAFileSearch(t *testing.T) {
	fx := newRunFixture(t)
	write(t, filepath.Join(fx.root, "modules", testKVer, "kernel", "fs", "nls_utf8.ko"), []byte("utf8"), 0o644)
	fx.b.Modules = []string{"nls_utf8"}
	gz, err := fx.b.buildInitramfs(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := byName(readNewc(t, gz))
	if string(m["lib/modules/nls_utf8.ko"].data) != "utf8" || string(m["lib/modules/load-order"].data) != "nls_utf8.ko\n" {
		t.Fatalf("staged %q, order %q", m["lib/modules/nls_utf8.ko"].data, m["lib/modules/load-order"].data)
	}
	if len(fx.fake.Calls) != 0 {
		t.Fatalf("ran %v with no modprobe on PATH", fx.fake.Calls)
	}
}

// Built into the kernel is legitimate and common: warn and go on.
func TestAModuleNobodyHasIsAssumedBuiltIn(t *testing.T) {
	fx := newRunFixture(t)
	fx.b.Modules = []string{"virtio_blk"}
	gz, err := fx.b.buildInitramfs(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(byName(readNewc(t, gz))["lib/modules/load-order"].data); got != "" {
		t.Fatalf("load-order %q", got)
	}
	want := "no virtio_blk module under " + filepath.Join(fx.root, "modules", testKVer) + " -- assuming it is built into the kernel"
	if !reflect.DeepEqual(fx.log.all(), []string{want}) {
		t.Fatalf("log %q", fx.log.all())
	}
}

// busybox insmod reads no compressed format: xz and zstd modules go
// through their own tools, and a missing tool is named.
func TestCompressedModulesUseTheirTools(t *testing.T) {
	for _, tc := range []struct{ ext, tool, flags string }{
		{".xz", "xz", "-dc"},
		{".zst", "zstd", "-dqc"},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			fx := newRunFixture(t)
			src := filepath.Join(fx.root, "modules", testKVer, "kernel", "fs", "hfsplus.ko"+tc.ext)
			write(t, src, []byte("compressed"), 0o644)
			fx.b.Modules = []string{"hfsplus"}

			_, err := fx.b.buildInitramfs(context.Background(), nil, nil)
			want := src + " is " + tc.tool + "-compressed and " + tc.tool + " is not installed"
			if err == nil || err.Error() != want {
				t.Fatalf("without %s: err = %v", tc.tool, err)
			}

			fx.fake.Paths[tc.tool] = "/usr/bin/" + tc.tool
			fx.fake.Handle = func(c proc.Cmd) error {
				if c.Name == tc.tool && reflect.DeepEqual(c.Args, []string{tc.flags, src}) {
					_, _ = c.Stdout.Write([]byte("hfsplus via " + tc.tool))
				}
				return nil
			}
			gz, err := fx.b.buildInitramfs(context.Background(), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(byName(readNewc(t, gz))["lib/modules/hfsplus.ko"].data); got != "hfsplus via "+tc.tool {
				t.Fatalf("staged %q", got)
			}
		})
	}
}

// qemuCalls is every command the fake saw that was QEMU.
func qemuCalls(f *proc.Fake) []proc.Cmd {
	var q []proc.Cmd
	for _, c := range f.Calls {
		if c.Name == "qemu-system-x86_64" {
			q = append(q, c)
		}
	}
	return q
}

// console makes the fake QEMU print text on its console.
func (fx *runFixture) console(t *testing.T, text string) {
	fx.fake.Handle = func(c proc.Cmd) error {
		if c.Name != "qemu-system-x86_64" {
			return nil
		}
		if _, ok := c.Stdout.(*os.File); !ok {
			t.Errorf("the console is a %T, not a file", c.Stdout)
		}
		_, err := io.WriteString(c.Stdout, text)
		return err
	}
}

func (fx *runFixture) images(t *testing.T, names ...string) []string {
	var p []string
	for _, n := range names {
		path := filepath.Join(fx.root, n)
		write(t, path, []byte("image"), 0o644)
		p = append(p, path)
	}
	return p
}

// The target is the first disk, read-write; each extra disk follows in
// order, and a source is read-only to QEMU as well as to the guest.
func TestRunDrives(t *testing.T) {
	fx := newRunFixture(t)
	img := fx.images(t, "t.img", "a", "b")
	var initrd []byte
	var stdinN int
	var stdinErr error
	fx.fake.Handle = func(c proc.Cmd) error {
		if c.Name == "qemu-system-x86_64" {
			initrd, _ = os.ReadFile(c.Args[8])
			stdinN, stdinErr = c.Stdin.Read(make([]byte, 1))
			_, _ = io.WriteString(c.Stdout, "MQG-PRIVOPS-OK rc=0\n")
		}
		return nil
	}
	if _, err := fx.b.Run(context.Background(), img[0], []byte("true\n"), []Disk{{"ro", img[1]}, {"raw", img[2]}}); err != nil {
		t.Fatal(err)
	}
	q := qemuCalls(fx.fake)
	if len(q) != 1 {
		t.Fatalf("%d QEMU calls", len(q))
	}
	a := q[0].Args
	kernel := filepath.Join(fx.root, "boot/vmlinuz-6.1.0-test")
	head := []string{"-enable-kvm", "-m", "512", "-nographic", "-no-reboot", "-kernel", kernel, "-initrd"}
	tail := []string{
		"-append", "console=ttyS0 loglevel=3 panic=1 mqg_modules=nls_base,nls_utf8,hfsplus,virtio,virtio_ring,virtio_pci,virtio_blk",
		"-drive", "file=" + img[0] + ",format=raw,if=virtio",
		"-drive", "file=" + img[1] + ",format=raw,if=virtio,readonly=on",
		"-drive", "file=" + img[2] + ",format=raw,if=virtio",
	}
	if len(a) != len(head)+1+len(tail) || !reflect.DeepEqual(a[:len(head)], head) || !reflect.DeepEqual(a[len(head)+1:], tail) {
		t.Fatalf("argv %q", a)
	}
	if filepath.Base(a[len(head)]) != "initramfs.cpio.gz" {
		t.Fatalf("-initrd %s", a[len(head)])
	}
	if roles := string(byName(readNewc(t, initrd))["disk-roles"].data); roles != "ro\nraw\n" {
		t.Fatalf("the initrd QEMU was given has roles %q", roles)
	}
	if _, err := os.Stat(a[len(head)]); !os.IsNotExist(err) {
		t.Errorf("the initramfs outlived the run: %v", err)
	}
	c := q[0]
	if c.Stdout != c.Stderr {
		t.Errorf("stdout and stderr are not the same console file")
	}
	if stdinN != 0 || stdinErr != io.EOF {
		t.Errorf("stdin is not empty: %d, %v", stdinN, stdinErr)
	}
}

// QEMU splits -drive's option list on commas, and reads ",," as one:
// every file= path is escaped, the target's and each disk's.
func TestRunEscapesCommasInDrivePaths(t *testing.T) {
	fx := newRunFixture(t)
	img := fx.images(t, "t,1.img", "a,,b", "c")
	fx.console(t, "MQG-PRIVOPS-OK rc=0\n")
	if _, err := fx.b.Run(context.Background(), img[0], nil, []Disk{{"ro", img[1]}, {"raw", img[2]}}); err != nil {
		t.Fatal(err)
	}
	a := qemuCalls(fx.fake)[0].Args
	var drives []string
	for i := range a {
		if a[i] == "-drive" {
			drives = append(drives, a[i+1])
		}
	}
	esc := func(p string) string { return strings.ReplaceAll(p, ",", ",,") }
	want := []string{
		"file=" + esc(img[0]) + ",format=raw,if=virtio",
		"file=" + esc(img[1]) + ",format=raw,if=virtio,readonly=on",
		"file=" + esc(img[2]) + ",format=raw,if=virtio",
	}
	if !reflect.DeepEqual(drives, want) {
		t.Fatalf("drives\n got %q\nwant %q", drives, want)
	}
	if !strings.Contains(drives[0], "t,,1.img,format") || !strings.Contains(drives[1], "a,,,,b,format") {
		t.Fatalf("not escaped: %q", drives)
	}
}

// Modules are resolved and staged once per Backend, not once per pass:
// a build is four passes, and "assuming it is built into the kernel" for
// each of seven modules on each pass was twenty-eight lines of noise.
func TestModulesAreStagedOncePerBackend(t *testing.T) {
	fx := newRunFixture(t)
	uname := &proc.Fake{Handle: func(c proc.Cmd) error {
		if c.Name == "uname" {
			_, _ = io.WriteString(c.Stdout, testKVer+"\n")
		}
		return nil
	}}
	b, err := NewBackend(uname, "qemu-system-x86_64", fx.log.logf)
	if err != nil {
		t.Fatal(err)
	}
	// The backend NewBackend made, pointed at the fixture's host.
	b.Runner, b.BootDir, b.ModulesDir, b.KVer, b.GOOS, b.KVMDevice, b.gzipLevel =
		fx.fake, fx.b.BootDir, fx.b.ModulesDir, testKVer, "linux", "", fx.b.gzipLevel
	fx.fake.Paths["modprobe"] = "/sbin/modprobe"
	src := filepath.Join(fx.root, "modules", testKVer, "kernel", "fs", "hfsplus.ko")
	write(t, src, []byte("hfsplus"), 0o644)
	var modprobes int
	fx.fake.Handle = func(c proc.Cmd) error {
		switch c.Name {
		case "modprobe":
			modprobes++
		case "qemu-system-x86_64":
			initrd, _ := os.ReadFile(c.Args[8])
			if got := string(byName(readNewc(t, initrd))["lib/modules/hfsplus.ko"].data); got != "hfsplus" {
				t.Errorf("pass staged hfsplus.ko as %q", got)
			}
			_, _ = io.WriteString(c.Stdout, "MQG-PRIVOPS-OK rc=0\n")
		}
		return nil
	}
	img := fx.images(t, "t.img")
	for pass := 0; pass < 2; pass++ {
		if _, err := b.Run(context.Background(), img[0], nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	builtIn := 0
	for _, l := range fx.log.all() {
		if strings.Contains(l, "assuming it is built into the kernel") {
			builtIn++
		}
	}
	if want := len(DefaultModules) - 1; builtIn != want {
		t.Fatalf("logged %d built-in lines over two passes, want %d (once per module):\n%s", builtIn, want, strings.Join(fx.log.all(), "\n"))
	}
	if modprobes != len(DefaultModules) {
		t.Fatalf("modprobe ran %d times over two passes, want %d", modprobes, len(DefaultModules))
	}

	// What the modules are is part of the answer: a Backend whose list
	// changed stages again.
	b.Modules = []string{"hfsplus"}
	if _, err := b.Run(context.Background(), img[0], nil, nil); err != nil {
		t.Fatal(err)
	}
	if modprobes != len(DefaultModules)+1 {
		t.Fatalf("a changed module list was not staged again: %d modprobes", modprobes)
	}
}

func TestRunRefusesAnUnknownRoleAndAMissingImage(t *testing.T) {
	fx := newRunFixture(t)
	img := fx.images(t, "t.img", "a")
	missing := filepath.Join(fx.root, "nothing.img")
	for _, tc := range []struct {
		target string
		disks  []Disk
		want   string
	}{
		{img[0], []Disk{{"rw", img[1]}}, `privops: unknown disk role "rw"`},
		{img[0], []Disk{{"ro", missing}}, "privops: no such image: " + missing},
		{img[0], []Disk{{"raw", missing}}, "privops: no such image: " + missing},
		{missing, nil, "privops: no such image: " + missing},
	} {
		_, err := fx.b.Run(context.Background(), tc.target, nil, tc.disks)
		if err == nil || err.Error() != tc.want {
			t.Errorf("%v: err = %v, want %q", tc.disks, err, tc.want)
		}
	}
	if len(fx.fake.Calls) != 0 {
		t.Fatalf("ran %v", fx.fake.Calls)
	}
}

// The console is streamed to a file, not captured through a pipe:
// -nographic hands QEMU stdin and stdout, and capturing them has produced
// zero bytes on a host where the same command printed normally.
func TestRunStreamsTheConsoleToAFile(t *testing.T) {
	fx := newRunFixture(t)
	img := fx.images(t, "t.img")
	text := "SeaBIOS\nMQG-PRIVOPS-MOUNTED /dev/vda\nMQG-PRIVOPS-OK rc=0\n"
	fx.console(t, text)
	got, err := fx.b.Run(context.Background(), img[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != text {
		t.Fatalf("console %q", got)
	}
}

// What the payload printed is logged, and nothing else: kernel lines,
// escapes, carriage returns, the backend's own markers and blank lines
// are left out.
func TestRunSucceedsOnOKAndLogsThePayloadsLines(t *testing.T) {
	fx := newRunFixture(t)
	img := fx.images(t, "t.img")
	fx.console(t, "[    0.123] foo\n\x1b[2J\x1b[?25l\n\x1bc\x1b[?7l\x1b[2JMQG-PRIVOPS-MOUNTED /dev/vda1\npayload says hi\r\n\nMQG-PRIVOPS-OK rc=0\n")
	if _, err := fx.b.Run(context.Background(), img[0], nil, nil); err != nil {
		t.Fatal(err)
	}
	var payload []string
	for _, l := range fx.log.all() {
		if strings.HasPrefix(l, "    ") {
			payload = append(payload, l)
		}
	}
	if !reflect.DeepEqual(payload, []string{"    payload says hi"}) {
		t.Fatalf("payload lines logged: %q (all: %q)", payload, fx.log.all())
	}
}

func TestRunFailures(t *testing.T) {
	var tail strings.Builder
	for i := 1; i <= 25; i++ {
		fmt.Fprintf(&tail, "console line %d\n", i)
	}
	for _, tc := range []struct {
		name, console, want string
		tail                bool
	}{
		{"the payload failed", "MQG-PRIVOPS-MOUNTED /dev/vda\nMQG-PRIVOPS-OK rc=1\n", "the payload script reported a failure inside the microVM", false},
		{"no OK at all", tail.String(), "privileged operations failed inside the microVM", true},
		{"a source would not mount", "MQG-PRIVOPS-SOURCE-MOUNT-FAILED 1 /dev/vdb\n", "a disk the microVM was given was not there, or held no mountable HFS+ volume", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newRunFixture(t)
			img := fx.images(t, "t.img")
			fx.console(t, tc.console)
			_, err := fx.b.Run(context.Background(), img[0], nil, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if tc.tail {
				// The tail is logged indented two spaces; the payload's
				// lines, logged first, four.
				var got []string
				for _, l := range fx.log.all() {
					if strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "    ") {
						got = append(got, l)
					}
				}
				var want []string
				for i := 6; i <= 25; i++ {
					want = append(want, fmt.Sprintf("  console line %d", i))
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("tail logged %q\nwant %q", got, want)
				}
			}
		})
	}
}

// ctxRunner is a Fake that also sees the context, which proc.Fake's
// Handle does not: it blocks until the context is done and returns its
// error, as proc.Exec does when it has killed the process.
type ctxRunner struct{ *proc.Fake }

func (r ctxRunner) Run(ctx context.Context, c proc.Cmd) error {
	_ = r.Fake.Run(ctx, c)
	if c.Name != "qemu-system-x86_64" {
		return nil
	}
	_, _ = io.WriteString(c.Stdout, "booting\n")
	<-ctx.Done()
	return ctx.Err()
}

func TestRunTimeout(t *testing.T) {
	fx := newRunFixture(t)
	img := fx.images(t, "t.img")
	fx.b.Runner = ctxRunner{fx.fake}
	fx.b.Timeout = 50 * time.Millisecond
	_, err := fx.b.Run(context.Background(), img[0], nil, nil)
	if err == nil || !strings.Contains(err.Error(), "the microVM did not finish within 50ms") || !strings.Contains(err.Error(), "--privops-timeout") {
		t.Fatalf("err = %v", err)
	}

	// Cancelled by the caller -- the caller was interrupted -- is not a timeout.
	fx.b.Timeout = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	_, err = fx.b.Run(ctx, img[0], nil, nil)
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "did not finish") {
		t.Fatalf("cancelled: err = %v", err)
	}
}

// A Backend made without a Timeout is bounded by the default, not by
// zero: a zero bound would expire before QEMU started.
func TestAZeroTimeoutIsTheDefault(t *testing.T) {
	fx := newRunFixture(t)
	img := fx.images(t, "t.img")
	fx.b.Timeout = 0
	fx.console(t, "MQG-PRIVOPS-OK rc=0\n")
	if _, err := fx.b.Run(context.Background(), img[0], nil, nil); err != nil {
		t.Fatal(err)
	}
}

// The requirements are checked before anything else, and each is named.
func TestRunRefusesWhatCannotRunHere(t *testing.T) {
	fx := newRunFixture(t)
	img := fx.images(t, "t.img")
	delete(fx.fake.Paths, "qemu-system-x86_64")
	_, err := fx.b.Run(context.Background(), img[0], nil, nil)
	if err == nil || !strings.Contains(err.Error(), "1 requirement(s)") {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(fx.log.all(), []string{"  missing: qemu-system-x86_64 (not on PATH)"}) {
		t.Fatalf("log %q", fx.log.all())
	}
	if len(fx.fake.Calls) != 0 {
		t.Fatalf("ran %v", fx.fake.Calls)
	}
}

// Marker is the one rule for a marker read as a single value: printed
// on exactly one line, or it is no value -- two lines are two answers.
func TestMarker(t *testing.T) {
	for _, tc := range []struct {
		console string
		want    string
		ok      bool
	}{
		{"x\r\nA 1\r\nB 2\n", "1", true},
		{"A 1\nA 1\n", "", false},
		{"A 1\nA 2\n", "", false},
		{"B 1\n", "", false},
		{"\x1bc\x1b[2JA 7\r\n", "7", true},
	} {
		if got, ok := Marker([]byte(tc.console), "A"); got != tc.want || ok != tc.ok {
			t.Errorf("Marker(%q) = %q, %v; want %q, %v", tc.console, got, ok, tc.want, tc.ok)
		}
	}
}

func TestMarkers(t *testing.T) {
	if got := Markers([]byte("A 1\r\nB x\nA 2\n"), "A"); !reflect.DeepEqual(got, []string{"1", "2"}) {
		t.Fatalf("%q", got)
	}
	if got := Markers([]byte("AB 1\nA\n"), "A"); got != nil {
		t.Fatalf("%q", got)
	}
	// The real console's first guest line comes after a terminal reset
	// (ESC c) and two CSI sequences: the marker is still at its start.
	if got := Markers([]byte("\x1bc\x1b[?7l\x1b[2JA 1\r\n\x1bDA 2\n"), "A"); !reflect.DeepEqual(got, []string{"1", "2"}) {
		t.Fatalf("after escapes: %q", got)
	}
}

// The real thing: the host's kernel, busybox and QEMU boot the initramfs
// this package writes, mount a real HFS+ volume and run a payload as
// root. Runs only where the host can; skips cleanly everywhere else.
func TestTheMicroVMRunsAPayload(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the qemu-linux backend runs on Linux only")
	}
	lr := &logRecorder{}
	b, err := NewBackend(proc.Exec{}, "qemu-system-x86_64", func(f string, a ...any) {
		lr.logf(f, a...)
		t.Logf(f, a...)
	})
	if err != nil {
		t.Skip(err)
	}
	// Missing includes a writable /dev/kvm (NewBackend's KVMDevice).
	if m := b.Missing(); len(m) > 0 {
		t.Skipf("the microVM cannot run here: %q", m)
	}
	mkfs, err := exec.LookPath("mkfs.hfsplus")
	if err != nil {
		t.Skip("mkfs.hfsplus not installed")
	}
	// A comma in the path, which QEMU's -drive would split on unescaped.
	target := filepath.Join(t.TempDir(), "tar,get.img")
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(target, 16<<20); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(mkfs, "-v", "MQG TEST", target).CombinedOutput(); err != nil {
		t.Fatalf("mkfs.hfsplus: %v\n%s", err, out)
	}
	payload := []byte(`echo "MQG-TEST-WROTE $($B sh -c 'echo hi > $MQG_MNT/hello; $B cat $MQG_MNT/hello')"` + "\n")
	start := time.Now()
	console, err := b.Run(context.Background(), target, payload, nil)
	t.Logf("the microVM ran in %v", time.Since(start))
	if err != nil {
		t.Fatalf("%v\nconsole:\n%s", err, console)
	}
	if got := Markers(console, "MQG-TEST-WROTE"); !reflect.DeepEqual(got, []string{"hi"}) {
		t.Fatalf("MQG-TEST-WROTE %q\nconsole:\n%s", got, console)
	}
}

// The initramfs is gzip -9 unless a Backend says
// otherwise, which only tests do.
func TestInitramfsIsGzipLevel9ByDefault(t *testing.T) {
	fx := newRunFixture(t)
	if fx.b.gzipLevel != gzip.BestSpeed {
		t.Fatalf("the fixture's level is %d", fx.b.gzipLevel)
	}
	// A small stand-in for busybox, so that this test's own level-9
	// compression is cheap.
	small := filepath.Join(fx.root, "busybox")
	write(t, small, bytes.Repeat([]byte("busybox "), 4096), 0o755)
	fx.fake.Paths["busybox"] = small
	for _, tc := range []struct{ set, want int }{{0, gzip.BestCompression}, {gzip.BestSpeed, gzip.BestSpeed}} {
		b := fx.b
		b.gzipLevel = tc.set
		gz, err := b.buildInitramfs(context.Background(), []byte("true\n"), nil)
		if err != nil {
			t.Fatal(err)
		}
		zr, err := gzip.NewReader(bytes.NewReader(gz))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(zr)
		var again bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&again, tc.want)
		_, _ = zw.Write(raw)
		_ = zw.Close()
		if !bytes.Equal(gz, again.Bytes()) {
			t.Errorf("gzipLevel %d: not compressed at level %d", tc.set, tc.want)
		}
	}
}

// funcRunner is a Fake whose QEMU is a function that sees the context.
type funcRunner struct {
	*proc.Fake
	qemu func(ctx context.Context, c proc.Cmd) error
}

func (r funcRunner) Run(ctx context.Context, c proc.Cmd) error {
	_ = r.Fake.Run(ctx, c)
	if c.Name != "qemu-system-x86_64" {
		return nil
	}
	return r.qemu(ctx, c)
}

// A pass that printed OK and exited 0 succeeded, even if its deadline
// passed, or its caller cancelled, a moment after: the classification
// as a timeout or a cancel is for a QEMU that did not finish.
func TestAPassThatFinishedIsNotATimeout(t *testing.T) {
	fx := newRunFixture(t)
	img := fx.images(t, "t.img")
	fx.b.Timeout = 20 * time.Millisecond
	fx.b.Runner = funcRunner{fx.fake, func(ctx context.Context, c proc.Cmd) error {
		_, _ = io.WriteString(c.Stdout, "MQG-PRIVOPS-OK rc=0\n")
		<-ctx.Done() // the deadline passes before QEMU's exit is seen
		return nil
	}}
	if _, err := fx.b.Run(context.Background(), img[0], nil, nil); err != nil {
		t.Fatalf("deadline after success: %v", err)
	}

	fx.b.Timeout = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fx.b.Runner = funcRunner{fx.fake, func(_ context.Context, c proc.Cmd) error {
		_, _ = io.WriteString(c.Stdout, "MQG-PRIVOPS-OK rc=0\n")
		cancel() // interrupted just after QEMU exited
		return nil
	}}
	if _, err := fx.b.Run(ctx, img[0], nil, nil); err != nil {
		t.Fatalf("cancel after success: %v", err)
	}
}

// A context that is already done stages nothing and boots nothing.
func TestRunChecksTheContextFirst(t *testing.T) {
	fx := newRunFixture(t)
	img := fx.images(t, "t.img")
	delete(fx.fake.Paths, "qemu-system-x86_64") // not even the requirements are looked at
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := fx.b.Run(ctx, img[0], nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if len(fx.fake.Calls) != 0 || len(fx.log.all()) != 0 {
		t.Fatalf("calls %v, log %q", fx.fake.Calls, fx.log.all())
	}
}
