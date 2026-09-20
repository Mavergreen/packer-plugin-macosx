//go:build unix

package proc

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// TestNewProcessGroupSetsSetpgid: the exec.Cmd Exec runs asks for a
// process group of the child's own exactly when the Cmd does.
func TestNewProcessGroupSetsSetpgid(t *testing.T) {
	for _, own := range []bool{false, true} {
		cmd := Exec{}.command(context.Background(), Cmd{Name: "true", NewProcessGroup: own})
		got := cmd.SysProcAttr != nil && cmd.SysProcAttr.Setpgid
		if got != own {
			t.Errorf("NewProcessGroup=%v: Setpgid=%v", own, got)
		}
	}
}

// TestNewProcessGroupPutsTheChildInItsOwnGroup: the child's process
// group is its own pid, not this process's group -- so a terminal's
// SIGINT to the foreground group never reaches it. Asked of ps, which
// every unix target this plugin cross-builds for has; skipped without it.
func TestNewProcessGroupPutsTheChildInItsOwnGroup(t *testing.T) {
	if _, err := exec.LookPath("ps"); err != nil {
		t.Skip("no ps")
	}
	ours := syscall.Getpgrp()
	for _, own := range []bool{false, true} {
		var out strings.Builder
		err := Exec{}.Run(context.Background(), Cmd{
			Name: "sh", Args: []string{"-c", `echo $$; ps -o pgid= -p $$`},
			Stdout: &out, NewProcessGroup: own,
		})
		if err != nil {
			t.Fatal(err)
		}
		f := strings.Fields(out.String())
		if len(f) != 2 {
			t.Fatalf("output %q", out.String())
		}
		pid, _ := strconv.Atoi(f[0])
		pgid, _ := strconv.Atoi(f[1])
		if own && (pgid != pid || pgid == ours) {
			t.Errorf("own group: pid %d, pgid %d, ours %d", pid, pgid, ours)
		}
		if !own && pgid != ours {
			t.Errorf("inherited group: pgid %d, want ours, %d", pgid, ours)
		}
	}
}
