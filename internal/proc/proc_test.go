package proc

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestExecReportsTheExitStatus(t *testing.T) {
	err := Exec{}.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "exit 3"}})
	var xe *ExitError
	if !errors.As(err, &xe) || xe.Code != 3 || !strings.Contains(xe.Error(), "sh -c 'exit 3'") {
		t.Fatalf("err = %v", err)
	}
}

func TestCancellingTheContextStopsTheChildPolitely(t *testing.T) {
	// SIGTERM, not SIGKILL: QEMU flushes its disks on SIGTERM.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := Exec{GracePeriod: 2 * time.Second}.Run(ctx, Cmd{Name: "sleep", Args: []string{"30"}})
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("err=%v after %v", err, time.Since(start))
	}
}

func TestExecPassesTheEnvironmentItIsGiven(t *testing.T) {
	var out strings.Builder
	err := Exec{}.Run(context.Background(), Cmd{
		Name: "sh", Args: []string{"-c", `printf %s "$MAVERICKS_PROC_TEST"`},
		Env:    []string{"MAVERICKS_PROC_TEST=a\tb", "PATH=/usr/bin:/bin"},
		Stdout: &out,
	})
	if err != nil || out.String() != "a\tb" {
		t.Fatalf("out %q, err %v", out.String(), err)
	}
}

func TestFakeRecordsAndAnswers(t *testing.T) {
	f := &Fake{Paths: map[string]string{"qemu-img": "/usr/bin/qemu-img"}}
	_ = f.Run(context.Background(), Cmd{Name: "qemu-img", Args: []string{"create"}})
	if len(f.Calls) != 1 || f.Calls[0].String() != "qemu-img create" {
		t.Fatalf("calls %v", f.Calls)
	}
	if _, err := f.LookPath("packer"); !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("missing tool: err = %v", err)
	}
}

func TestCmdStringQuotesWhatNeedsQuoting(t *testing.T) {
	c := Cmd{Name: "qemu", Args: []string{"-drive", "file=/a b/c", "-m", "4096"}}
	if got := c.String(); got != "qemu -drive 'file=/a b/c' -m 4096" {
		t.Fatalf("got %q", got)
	}
}
