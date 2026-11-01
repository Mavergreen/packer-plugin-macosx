package qmpdir

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/zclconf/go-cty/cty"
)

func configured(t *testing.T, dir string) *Datasource {
	t.Helper()
	d := new(Datasource)
	if err := d.Configure(map[string]any{"dir": dir}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	return d
}

func out(t *testing.T, v cty.Value) (path, socket string) {
	t.Helper()
	m := v.AsValueMap()
	p, s := m["path"], m["socket"]
	if p == cty.NilVal || s == cty.NilVal {
		t.Fatalf("output %#v lacks path or socket", m)
	}
	return p.AsString(), s.AsString()
}

func inTemp(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
}

// The directory is private whatever the umask: QEMU makes the QMP socket inside it with the umask's mode,
// and whoever can connect controls the VM.
func TestExecuteCreatesAnOwnerOnlyDirectoryWhateverTheUmask(t *testing.T) {
	inTemp(t)
	// a restrictive umask takes bits from the 0700 Mkdir asks for (0277 leaves 0500, which the owner
	// cannot even make a file in), so the mode only comes right by an explicit chmod
	old := syscall.Umask(0o277)
	defer syscall.Umask(old)
	v, err := configured(t, "output-x-qmp").Execute()
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat("output-x-qmp")
	if err != nil {
		t.Fatal(err)
	}
	if !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Errorf("mode %v, want a directory with 0700", fi.Mode())
	}
	p, s := out(t, v)
	if p != "output-x-qmp" || s != "output-x-qmp/qmp.sock" {
		t.Errorf("path %q, socket %q: want the configured path as given (relative, so the socket's path stays short) and the socket inside it", p, s)
	}
}

// Data sources run on packer build and validate -evaluate-datasources, possibly beside a build in flight: the second run,
// change nothing.
func TestExecuteIsIdempotentAndLeavesWhatIsInside(t *testing.T) {
	inTemp(t)
	ds := configured(t, "d")
	if _, err := ds.Execute(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("d/qmp.sock", []byte("live"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ds.Execute(); err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if b, err := os.ReadFile("d/qmp.sock"); err != nil || string(b) != "live" {
		t.Errorf("what was inside was touched: %q, %v", b, err)
	}
}

func TestExecuteRepairsADirectoryThatIsOursButNotPrivate(t *testing.T) {
	inTemp(t)
	if err := os.Mkdir("d", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod("d", 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := configured(t, "d").Execute(); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Lstat("d")
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("mode %v after Execute, want 0700", fi.Mode().Perm())
	}
}

func TestExecuteRefusesASymlinkEvenToADirectory(t *testing.T) {
	inTemp(t)
	if err := os.Mkdir("elsewhere", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("elsewhere", "d"); err != nil {
		t.Fatal(err)
	}
	_, err := configured(t, "d").Execute()
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("err = %v, want a refusal naming the symbolic link", err)
	}
}

func TestExecuteRefusesWhatIsNotADirectory(t *testing.T) {
	inTemp(t)
	if err := os.WriteFile("d", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := configured(t, "d").Execute(); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("err = %v, want a refusal: not a directory", err)
	}
}

func TestExecuteRefusesADirectoryAnotherUserOwns(t *testing.T) {
	inTemp(t)
	if err := os.Mkdir("d", 0o700); err != nil {
		t.Fatal(err)
	}
	was := effectiveUID
	effectiveUID = func() int { return os.Geteuid() + 1 }
	defer func() { effectiveUID = was }()
	_, err := configured(t, "d").Execute()
	if err == nil || !strings.Contains(err.Error(), "owned by another user") {
		t.Errorf("err = %v, want a refusal: owned by another user", err)
	}
	fi, _ := os.Lstat("d")
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("the refused directory's mode was changed to %v", fi.Mode().Perm())
	}
}

// sun_path holds 104 bytes on macOS, the NUL included, and 108 on Linux: the socket's path, as QEMU
// binds it, must be 103 bytes or fewer.
func TestExecuteRefusesASocketPathTooLongForAUnixSocket(t *testing.T) {
	inTemp(t)
	fits := strings.Repeat("a", 103-len("/qmp.sock"))
	if _, err := configured(t, fits).Execute(); err != nil {
		t.Errorf("a socket path of 103 bytes: %v", err)
	}
	long := fits + "a"
	_, err := configured(t, long).Execute()
	if err == nil || !strings.Contains(err.Error(), "104") {
		t.Errorf("a socket path of 104 bytes: err = %v, want a refusal naming the limit", err)
	}
	if _, serr := os.Lstat(long); serr == nil {
		t.Errorf("the too-long directory %q was created", long)
	}
}

func TestConfigureRequiresADirAndRejectsUnknownKeys(t *testing.T) {
	if err := new(Datasource).Configure(map[string]any{}); err == nil {
		t.Error("no dir: want an error")
	}
	if err := new(Datasource).Configure(map[string]any{"dir": "d", "mode": "0755"}); err == nil {
		t.Error("an unknown key: want an error")
	}
}

// the check and the use went by path, so a directory swapped in between was used; the checks are made
// on the opened directory, and a parent anyone can write in (a rename there swaps the directory under
// the checks, and the socket's path with it) is refused, sticky or not
func TestExecuteRefusesAParentAnyoneCanWriteIn(t *testing.T) {
	for _, mode := range []os.FileMode{0o777, 0o777 | os.ModeSticky, 0o703} {
		inTemp(t)
		if err := os.Mkdir("parent", 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod("parent", mode); err != nil {
			t.Fatal(err)
		}
		_, err := configured(t, filepath.Join("parent", "d")).Execute()
		if err == nil || !strings.Contains(err.Error(), "writable by everyone") {
			t.Errorf("parent %v: err = %v, want a refusal saying it is writable by everyone", mode, err)
		}
		if _, serr := os.Lstat(filepath.Join("parent", "d")); serr == nil {
			t.Errorf("parent %v: the directory was made", mode)
		}
	}
}

func TestExecuteRefusesTheWorkingDirectoryItselfWhenAnyoneCanWriteInIt(t *testing.T) {
	inTemp(t)
	if err := os.Chmod(".", 0o1777); err != nil {
		t.Fatal(err)
	}
	if _, err := configured(t, "d").Execute(); err == nil || !strings.Contains(err.Error(), "writable by everyone") {
		t.Errorf("err = %v, want a refusal (building straight in /tmp)", err)
	}
}

// a group-writable parent is what Ubuntu's user-private groups with umask 002 make everywhere
func TestExecuteAllowsAGroupWritableParent(t *testing.T) {
	inTemp(t)
	if err := os.Mkdir("parent", 0o775); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod("parent", 0o775); err != nil {
		t.Fatal(err)
	}
	if _, err := configured(t, filepath.Join("parent", "d")).Execute(); err != nil {
		t.Errorf("a group-writable parent: %v", err)
	}
}

// the directory is opened without following a link and as a directory, and judged on what was opened:
// a symlink to a directory of ours is refused as it is for any other target
func TestExecuteRefusesALinkToADirectoryWeOwnToo(t *testing.T) {
	inTemp(t)
	if err := os.Mkdir("mine", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("mine", "d"); err != nil {
		t.Fatal(err)
	}
	if _, err := configured(t, "d").Execute(); err == nil {
		t.Error("a symlink to a directory of ours was accepted")
	}
}

func TestExecuteDoesNotMakeAMissingParent(t *testing.T) {
	inTemp(t)
	if _, err := configured(t, filepath.Join("p", "d")).Execute(); err == nil {
		t.Error("a missing parent: want an error, the parent is the build's own directory and is not ours to make")
	}
}
