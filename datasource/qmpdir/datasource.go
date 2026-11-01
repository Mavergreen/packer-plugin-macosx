// Package qmpdir is the qmp-dir data source: the owner-only directory a
// template's QMP socket lives in. QEMU makes a unix socket with the mode
// 0777 minus the umask of the process that starts it, and whoever can
// connect to a QMP socket controls the VM, so the socket is made inside a
// directory only its owner can enter (docs/decisions/0040).
package qmpdir

//go:generate packer-sdc mapstructure-to-hcl2 -type Config,DatasourceOutput -output datasource.hcl2spec.go

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"syscall"

	"github.com/hashicorp/hcl/v2/hcldec"
	"github.com/hashicorp/packer-plugin-sdk/hcl2helper"
	packersdk "github.com/hashicorp/packer-plugin-sdk/packer"
	configHelper "github.com/hashicorp/packer-plugin-sdk/template/config"
	"github.com/zclconf/go-cty/cty"
)

// SocketName is the socket's name inside the directory.
const SocketName = "qmp.sock"

// maxSocketPath is the longest unix socket path, in bytes, that binds on
// macOS (sun_path is 104 bytes there, its NUL included); Linux's is 108.
const maxSocketPath = 103

// Config is qmp-dir's HCL configuration: the directory, which the
// template names beside its output directory, relative to the working
// directory as that output directory is.
type Config struct {
	Dir string `mapstructure:"dir"`
}

// DatasourceOutput is Execute's result: the directory as configured, and
// the socket's path inside it, relative when the directory is, so that
// QEMU binds the short relative path and not an absolute one that a deep
// working directory could push past the limit.
type DatasourceOutput struct {
	Path   string `mapstructure:"path"`
	Socket string `mapstructure:"socket"`
}

// Datasource is qmp-dir.
type Datasource struct {
	config Config
}

var _ packersdk.Datasource = new(Datasource)

// effectiveUID is a test seam: a test cannot chown a directory to someone else.
var effectiveUID = os.Geteuid

func (d *Datasource) ConfigSpec() hcldec.ObjectSpec {
	return d.config.FlatMapstructure().HCL2Spec()
}

func (d *Datasource) Configure(raws ...interface{}) error {
	if err := configHelper.Decode(&d.config, nil, raws...); err != nil {
		return err
	}
	if d.config.Dir == "" {
		return fmt.Errorf("qmp-dir: dir is required")
	}
	return nil
}

func (d *Datasource) OutputSpec() hcldec.ObjectSpec {
	return new(DatasourceOutput).FlatMapstructure().HCL2Spec()
}

// Execute makes the directory, or checks and repairs the one that is
// there. Data sources run on `packer build` and `packer validate
// -evaluate-datasources` (not on plain `validate` or `inspect`, measured),
// possibly beside a build in flight, so it is idempotent and never touches
// what is inside the directory.
func (d *Datasource) Execute() (cty.Value, error) {
	dir := d.config.Dir
	socket := dir + "/" + SocketName
	if len(socket) > maxSocketPath {
		return cty.NullVal(cty.EmptyObject), fmt.Errorf("qmp-dir: the socket's path %q is %d bytes; a unix socket's path may be %d (sun_path is 104 bytes on macOS, 108 on Linux); name a shorter directory", socket, len(socket), maxSocketPath)
	}
	if err := ensure(dir); err != nil {
		return cty.NullVal(cty.EmptyObject), err
	}
	log.Printf("qmp-dir: %s is ready, mode 0700", dir)
	return hcl2helper.HCL2ValueFromConfig(DatasourceOutput{Path: dir, Socket: socket}, d.OutputSpec()), nil
}

// ensure makes dir, or checks and repairs the one that is there, on the
// opened directory and not on its path: a check by path and a use by path
// leave room for a swap between them. The parent is the build's own
// directory, not made here, and refused when anyone can write in it, sticky
// or not (building straight in /tmp): a user there could rename our
// directory away and put theirs in its place after QEMU resolves the path.
// A parent only its group can write in is allowed: Ubuntu's user-private
// groups with umask 002 make that the usual case (docs/decisions/0040 says
// what remains).
func ensure(dir string) error {
	parent := filepath.Dir(dir)
	pi, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("qmp-dir: %w", err)
	}
	if pi.Mode().Perm()&0o002 != 0 {
		return fmt.Errorf("qmp-dir: %s is writable by everyone (mode %v); another user could swap the directory for their own under the QMP socket. Build in a directory of your own (not /tmp itself)", parent, pi.Mode())
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return fmt.Errorf("qmp-dir: %w", err)
	}
	fd, err := syscall.Open(dir, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		if li, lerr := os.Lstat(dir); lerr == nil {
			switch {
			case li.Mode()&os.ModeSymlink != 0:
				return fmt.Errorf("qmp-dir: %s is a symbolic link; refusing it (someone may have put it there to have the socket made elsewhere)", dir)
			case !li.IsDir():
				return fmt.Errorf("qmp-dir: %s exists and is not a directory", dir)
			}
		}
		return fmt.Errorf("qmp-dir: opening %s: %w", dir, err)
	}
	f := os.NewFile(uintptr(fd), dir)
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("qmp-dir: %w", err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != effectiveUID() {
		return fmt.Errorf("qmp-dir: %s is owned by another user; refusing it", dir)
	}
	// the umask can only have taken bits away from 0700, and a directory left by an older run may have more
	if fi.Mode().Perm() != 0o700 {
		if err := f.Chmod(0o700); err != nil {
			return fmt.Errorf("qmp-dir: making %s 0700: %w", dir, err)
		}
	}
	return nil
}
