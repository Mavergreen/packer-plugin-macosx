package privops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Mavergreen/packer-plugin-mavericks/internal/config"
	"github.com/Mavergreen/packer-plugin-mavericks/internal/proc"
)

// DefaultTimeout bounds one microVM pass when a Backend names no bound of
// its own. It is a wall-clock bound on someone else's hardware: 300s was
// fine on a 6-core Coffee Lake and expired on a 2-core Broadwell doing the
// same work, so the media data source takes its own privops_timeout.
const DefaultTimeout = 15 * time.Minute

// A Disk is an extra image the microVM gets after the target: "ro" is
// mounted read-only as $MQG_SRC<n>, "raw" is handed over as a block
// device $MQG_RAW<n> and not mounted -- which is how a payload gives a
// file back to the host.
type Disk struct{ Role, Path string }

var (
	// ansi is a terminal escape: a CSI sequence (ESC [ ... final), a
	// two-byte ESC Fe sequence (ESC @ through _, but for [), or ESC c,
	// the reset that precedes the guest's first line on the console --
	// stripping CSI alone would leave that reset's "c" on the line.
	ansi       = regexp.MustCompile(`\x1b(\[[0-9;?]*[a-zA-Z]|[@-Z\\-_]|c)`)
	kernelLine = regexp.MustCompile(`^\[[ 0-9.]+\]`)
)

func (b Backend) logf(f string, a ...any) {
	if b.Log != nil {
		b.Log(f, a...)
	}
}

// Run boots the microVM with target attached read-write as /dev/vda and
// the disks after it, runs payload as uid 0, and returns the console --
// the only channel out besides the disks. The console is streamed to a
// file, not captured through a pipe: -nographic hands QEMU stdin and
// stdout, and capturing them has produced zero bytes on a host where
// the same command printed normally. stdin is /dev/null for the same
// reason: a step must not compete with its caller for the terminal.
func (b Backend) Run(ctx context.Context, target string, payload []byte, disks []Disk) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Every unmet requirement is reported before refusing, so that one
	// run tells the whole story.
	if m := b.Missing(); len(m) > 0 {
		for _, l := range m {
			b.logf("  missing: %s", l)
		}
		return nil, fmt.Errorf("the privops microVM cannot run on this host: %d requirement(s) above are unmet -- nothing here installs anything", len(m))
	}
	if !config.RegularFile(target) {
		return nil, fmt.Errorf("privops: no such image: %s", target)
	}
	var roles []string
	for _, d := range disks {
		switch d.Role {
		case "ro", "raw":
		default:
			return nil, fmt.Errorf("privops: unknown disk role %q", d.Role)
		}
		if !config.RegularFile(d.Path) {
			return nil, fmt.Errorf("privops: no such image: %s", d.Path)
		}
		roles = append(roles, d.Role)
	}
	kernel, err := b.Kernel()
	if err != nil {
		return nil, err
	}
	if !b.KernelIsKeyed(kernel) {
		b.logf("warning: %s is not keyed to the running kernel (%s): if it is a different build, none of the modules staged from %s will load",
			kernel, b.KVer, filepath.Join(b.ModulesDir, b.KVer))
	}
	tmp, err := os.MkdirTemp("", "mavericks-privops-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	initrd, err := b.buildInitramfs(ctx, payload, roles)
	if err != nil {
		return nil, err
	}
	initrdPath := filepath.Join(tmp, "initramfs.cpio.gz")
	if err := os.WriteFile(initrdPath, initrd, 0o600); err != nil {
		return nil, err
	}
	args := []string{"-enable-kvm", "-m", fmt.Sprint(b.MemMiB), "-nographic", "-no-reboot",
		"-kernel", kernel, "-initrd", initrdPath,
		"-append", "console=ttyS0 loglevel=3 panic=1 mqg_modules=" + strings.Join(b.Modules, ","),
		"-drive", "file=" + driveFile(target) + ",format=raw,if=virtio"}
	// readonly=on is belt and braces over the guest's own mount -o ro: a
	// source QEMU will not write is one a buggy payload cannot corrupt.
	for _, d := range disks {
		spec := "file=" + driveFile(d.Path) + ",format=raw,if=virtio"
		if d.Role == "ro" {
			spec += ",readonly=on"
		}
		args = append(args, "-drive", spec)
	}
	consolePath := filepath.Join(tmp, "console.txt")
	cf, err := os.Create(consolePath)
	if err != nil {
		return nil, err
	}
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		cf.Close()
		return nil, err
	}
	timeout := b.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	b.logf("running privileged operations in a QEMU microVM (no host root)")
	runErr := b.Runner.Run(rctx, proc.Cmd{Name: b.QEMU, Args: args, Stdin: devnull, Stdout: cf, Stderr: cf})
	devnull.Close()
	cf.Close()
	console, rerr := os.ReadFile(consolePath)
	if rerr != nil {
		return nil, fmt.Errorf("cannot read the microVM's console: %w", rerr)
	}

	// A timeout is distinguished from a guest that ran and failed: the
	// remedies are unrelated. Only a QEMU that did not finish is either:
	// one that exited 0 finished, whenever its deadline passes.
	if runErr != nil {
		if ctx.Err() != nil {
			return console, ctx.Err()
		}
		if errors.Is(rctx.Err(), context.DeadlineExceeded) {
			b.logTail(console)
			return console, fmt.Errorf("the microVM did not finish within %s -- raise --privops-timeout if this host is slower, or see the console above if it hung", timeout)
		}
	}
	b.logPayload(console)
	text := string(console)
	if strings.Contains(text, "MQG-PRIVOPS-SOURCE-MOUNT-FAILED") {
		for _, l := range cleanLines(console) {
			if strings.Contains(l, "MQG-PRIVOPS-SOURCE-MOUNT-FAILED") {
				b.logf("  %s", l)
			}
		}
		return console, errors.New("a disk the microVM was given was not there, or held no mountable HFS+ volume -- the conversion that produced it is the suspect, not the target image, which was not written to")
	}
	if !strings.Contains(text, "MQG-PRIVOPS-OK") {
		b.logTail(console)
		if runErr != nil {
			return console, fmt.Errorf("privileged operations failed inside the microVM: %w", runErr)
		}
		return console, errors.New("privileged operations failed inside the microVM")
	}
	if !strings.Contains(text, "MQG-PRIVOPS-OK rc=0") {
		return console, errors.New("the payload script reported a failure inside the microVM")
	}
	b.logf("privileged operations completed and the image unmounted cleanly")
	return console, nil
}

// driveFile is path as a value in -drive's option list, which QEMU
// splits on commas: a comma in the value is written twice.
func driveFile(path string) string { return strings.ReplaceAll(path, ",", ",,") }

// cleanLines is the console as lines, without terminal escapes or
// carriage returns, and without the trailing newlines a command
// substitution would have dropped.
func cleanLines(console []byte) []string {
	text := strings.TrimRight(string(console), "\n")
	if text == "" {
		return nil
	}
	var out []string
	for _, l := range strings.Split(text, "\n") {
		out = append(out, strings.ReplaceAll(ansi.ReplaceAllString(l, ""), "\r", ""))
	}
	return out
}

// logPayload logs what the payload printed: the only diagnostic when a
// build goes wrong. Kernel lines, the backend's own markers and blank
// lines are left out.
func (b Backend) logPayload(console []byte) {
	for _, l := range cleanLines(console) {
		if l == "" || kernelLine.MatchString(l) || strings.HasPrefix(l, "MQG-PRIVOPS-") {
			continue
		}
		b.logf("    %s", l)
	}
}

// logTail logs the console's last 20 lines, for a run that ended without
// saying how.
func (b Backend) logTail(console []byte) {
	lines := cleanLines(console)
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	for _, l := range lines {
		b.logf("  %s", l)
	}
}

// Marker is the value printed after "name " at the start of exactly one
// console line, and whether there was exactly one. It is the one rule for
// a marker read as a single value -- a size, a count, a checksum: printed
// on no line, or on two, it is no value, and the caller refuses what it
// was for. Two lines are two answers, and taking either would be a guess.
// (Neither payload prints one twice.)
func Marker(console []byte, name string) (string, bool) {
	if v := Markers(console, name); len(v) == 1 {
		return v[0], true
	}
	return "", false
}

// Markers is every value printed after "name " at the start of a console
// line, in order, with terminal escapes and carriage returns removed: how
// a payload hands an answer -- a checksum, a count -- back to the host.
func Markers(console []byte, name string) []string {
	var v []string
	for _, l := range cleanLines(console) {
		if rest, ok := strings.CutPrefix(l, name+" "); ok {
			v = append(v, rest)
		}
	}
	return v
}
