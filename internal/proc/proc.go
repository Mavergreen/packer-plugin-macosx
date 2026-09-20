// Package proc is the one way this project runs another program, so that
// every command can be tested with a fake that records what would have
// run.
package proc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

type Cmd struct {
	Name   string
	Args   []string
	Dir    string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// ExtraFiles are inherited by the child starting at fd 3, the way
	// os/exec.Cmd.ExtraFiles works: a caller that wants a QEMU boot to
	// hold a locked state file for as long as it runs, say, hands it an
	// inherited fd, which keeps the flock held even if this process is
	// killed before it can release it deliberately.
	ExtraFiles []*os.File
	// Env is the child's whole environment, as exec.Cmd.Env: nil means
	// this process's own. The firmware builds use it to hand upstream's
	// build scripts their settings (ARCHS, BUILD_ARGUMENTS, ...).
	Env []string
	// NewProcessGroup starts the child in a process group of its own
	// (setpgid), so the signals a terminal sends its foreground group --
	// Ctrl-C's SIGINT, a closed terminal's SIGHUP -- reach this process
	// and not the child, which a long-running build VM needs to outlive
	// a Ctrl-C of its caller. Unix only; elsewhere it does nothing.
	NewProcessGroup bool
}

// String is the command as a shell would need it typed, for logs and
// error messages.
func (c Cmd) String() string {
	parts := []string{quote(c.Name)}
	for _, a := range c.Args {
		parts = append(parts, quote(a))
	}
	return strings.Join(parts, " ")
}

func quote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`*?[]{}()<>|&;#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

type Runner interface {
	Run(ctx context.Context, c Cmd) error
	LookPath(name string) (string, error)
}

// ExitError is a program that ran and failed.
type ExitError struct {
	Cmd  string
	Code int
}

func (e *ExitError) Error() string { return fmt.Sprintf("%s: exit status %d", e.Cmd, e.Code) }

// Exec runs real programs. Cancelling the context sends SIGTERM, and
// SIGKILL only if the program is still running GracePeriod later (default
// 10s): QEMU flushes its disks on SIGTERM.
type Exec struct{ GracePeriod time.Duration }

func (x Exec) Run(ctx context.Context, c Cmd) error {
	err := x.command(ctx, c).Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ctx.Err() == nil {
		return &ExitError{Cmd: c.String(), Code: ee.ExitCode()}
	}
	if err != nil {
		return fmt.Errorf("%s: %w", c.String(), err)
	}
	return nil
}

// command is the exec.Cmd Run runs for c.
func (x Exec) command(ctx context.Context, c Cmd) *exec.Cmd {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr = c.Dir, c.Stdin, c.Stdout, c.Stderr
	cmd.ExtraFiles = c.ExtraFiles
	cmd.Env = c.Env
	cmd.SysProcAttr = sysProcAttr(c)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = x.GracePeriod
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = 10 * time.Second
	}
	return cmd
}

func (Exec) LookPath(name string) (string, error) { return exec.LookPath(name) }

// Fake records every command instead of running it. Handle, if set,
// decides each command's result; Paths answers LookPath.
type Fake struct {
	Calls  []Cmd
	Handle func(Cmd) error
	Paths  map[string]string
}

func (f *Fake) Run(_ context.Context, c Cmd) error {
	f.Calls = append(f.Calls, c)
	if f.Handle != nil {
		return f.Handle(c)
	}
	return nil
}

func (f *Fake) LookPath(name string) (string, error) {
	if p, ok := f.Paths[name]; ok {
		return p, nil
	}
	return "", fmt.Errorf("%s: %w", name, exec.ErrNotFound)
}
