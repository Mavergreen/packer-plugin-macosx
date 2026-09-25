package privops

import (
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// fixtureDir holds what is built once per test binary and shared by every
// test: building a Go program costs seconds, not milliseconds.
var (
	fixtureDir  string
	staticOnce  sync.Once
	staticPath  string
	staticError string
)

func TestMain(m *testing.M) {
	code := m.Run()
	if fixtureDir != "" {
		os.RemoveAll(fixtureDir)
	}
	os.Exit(code)
}

// repo is the repository root, found from this file's location, as
// internal/diskimg's tests find it.
func repo(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// staticELF is a statically linked executable made for the test: a Go
// program built with CGO_ENABLED=0, which on Linux is a static ELF with
// no PT_INTERP. It stands in for a static busybox without committing
// busybox's bytes. Skips when the toolchain cannot build one.
func staticELF(t *testing.T) string {
	t.Helper()
	staticOnce.Do(func() {
		dir, err := os.MkdirTemp("", "mavericks-privops-test-")
		if err != nil {
			staticError = err.Error()
			return
		}
		fixtureDir = dir
		src := filepath.Join(dir, "main.go")
		if err := os.WriteFile(src, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
			staticError = err.Error()
			return
		}
		out := filepath.Join(dir, "static-busybox")
		gobin, err := exec.LookPath("go")
		if err != nil {
			staticError = "go not on PATH"
			return
		}
		cmd := exec.Command(gobin, "build", "-o", out, src)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GO111MODULE=off", "GOFLAGS=")
		if b, err := cmd.CombinedOutput(); err != nil {
			staticError = err.Error() + ": " + string(b)
			return
		}
		staticPath = out
	})
	if staticPath == "" {
		t.Skipf("cannot build a static ELF fixture: %s", staticError)
	}
	return staticPath
}

// dynamicELF is an executable with a PT_INTERP header on this host:
// /bin/sh, when it is one. Skips otherwise.
func dynamicELF(t *testing.T) string {
	t.Helper()
	for _, p := range []string{"/bin/sh", "/usr/bin/env", "/bin/ls"} {
		f, err := elf.Open(p)
		if err != nil {
			continue
		}
		dynamic := false
		for _, prog := range f.Progs {
			if prog.Type == elf.PT_INTERP {
				dynamic = true
			}
		}
		f.Close()
		if dynamic {
			return p
		}
	}
	t.Skip("no dynamically linked ELF executable on this host")
	return ""
}

// write makes path, and its directories, holding data.
func write(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}
