// Package templatetest is what every template's own tests share: a real
// packer binary, the qemu and vagrant plugins seeded from
// $PACKER_PLUGIN_PATH, this checkout's plugin installed beside them, and a
// copy of exactly the files a release's template archive carries. It is
// imported only by tests.
package templatetest

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

// RepoRoot is this file's own location, walked up past templates/mavericks/: robust
// to whatever directory `go test` runs from.
func RepoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
}

// PackerBinary is the packer this test drives.
func PackerBinary(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("PACKER")
	if bin == "" {
		bin = "packer"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		t.Skipf("packer not found (%s); set $PACKER to its path to run this test", err)
	}
	return path
}

// SeedPluginDir is where the qemu and vagrant plugins this test needs are
// already downloaded.
func SeedPluginDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("PACKER_PLUGIN_PATH")
	if dir == "" {
		t.Skip("PACKER_PLUGIN_PATH not set; nowhere to find the qemu and vagrant plugins this test needs")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("PACKER_PLUGIN_PATH %s: %v", dir, err)
	}
	return dir
}

// CopyTree copies src onto dst, creating directories as needed and
// preserving each file's mode: the seeded plugin binaries need their +x,
// and dev-install.sh's own `packer plugins install` writes its manifest
// files alongside them, so the copy -- not the shared seed itself --
// takes that write.
func CopyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}

// DevInstall builds the plugin from this checkout and installs it into
// pluginDir with bin/dev-install.sh, the same script a developer or CI
// uses -- not a separate, parallel build path this test invents for
// itself.
func DevInstall(t *testing.T, root, packer, pluginDir string) {
	t.Helper()
	cmd := exec.Command("bash", filepath.Join(root, "bin", "dev-install.sh"))
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"PACKER="+packer,
		"PACKER_PLUGIN_PATH="+pluginDir,
		"MQG_PLUGIN_BIN="+filepath.Join(t.TempDir(), "packer-plugin-macosx"),
		"CHECKPOINT_DISABLE=1",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bin/dev-install.sh: %v\n%s", err, out)
	}
}

// ShippedFiles is the template archive's own file list, read out of
// .goreleaser.yml's "- src: templates/<osName>/<name>" lines: exactly what a
// release carries, which tests/release.bats holds to the tracked
// template files.
func ShippedFiles(t *testing.T, root, osName string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, ".goreleaser.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range regexp.MustCompile(`(?m)^\s*- src: templates/`+regexp.QuoteMeta(osName)+`/(\S+)\s*$`).FindAllStringSubmatch(string(b), -1) {
		names = append(names, m[1])
	}
	if len(names) == 0 {
		t.Fatal(".goreleaser.yml names no template files")
	}
	return names
}

// ShippedCopy copies the files a release's template archive carries --
// and nothing else: no build output, no test -- into a fresh directory,
// and returns it. Validating that copy is what shows templates/mavericks/ is
// self-contained, and a build's leftovers in the checkout's own
// templates/mavericks/ (output-mavericks/, which packer validate refuses) cannot
// affect it.
func ShippedCopy(t *testing.T, root, osName string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "template")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range ShippedFiles(t, root, osName) {
		b, err := os.ReadFile(filepath.Join(root, "templates", osName, n))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, n), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

// DirEmpty reports whether dir has no entries. A PACKER_CACHE_DIR that
// packer validate never asked for stays exactly as empty as one it never
// even created -- both count as "no data source executed".
func DirEmpty(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}
