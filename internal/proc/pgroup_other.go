//go:build !unix

package proc

import "syscall"

// sysProcAttr does nothing where there are no unix process groups.
func sysProcAttr(Cmd) *syscall.SysProcAttr { return nil }
