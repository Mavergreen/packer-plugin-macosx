//go:build unix

package proc

import "syscall"

// sysProcAttr is Cmd.NewProcessGroup's setpgid: the child leads a new
// process group, whose id is its pid.
func sysProcAttr(c Cmd) *syscall.SysProcAttr {
	if !c.NewProcessGroup {
		return nil
	}
	return &syscall.SysProcAttr{Setpgid: true}
}
