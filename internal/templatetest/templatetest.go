// Package templatetest is what every template's own tests share: a real
// packer binary, the qemu and vagrant plugins seeded from
// $PACKER_PLUGIN_PATH, this checkout's plugin installed beside them, and a
// copy of exactly the files a release's template archive carries. It is
// imported only by tests.
package templatetest

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	"github.com/zclconf/go-cty/cty/function/stdlib"
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
		"GUEST_MACOSX_PLUGIN_BIN="+filepath.Join(t.TempDir(), "packer-plugin-macosx"),
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

// CheckBuildUUID holds templates/<osName>/<file> to giving the build's
// QEMU a host UUID of its own: a local build_uuid = uuidv4(), which Packer
// evaluates once per build, passed as qemuargs' ["-uuid", local.build_uuid].
// OpenCore makes it the guest's system-id (SystemUUID OEM), so the build's
// guest has a host UUID before its first daemon asks, and no two builds
// share one (docs/decisions/0024).
func CheckBuildUUID(t *testing.T, osName, file string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(RepoRoot(t), "templates", osName, file))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !regexp.MustCompile(`(?m)^\s*build_uuid\s*=\s*uuidv4\(\)\s*$`).MatchString(src) {
		t.Errorf("%s: no local build_uuid = uuidv4()", file)
	}
	q := strings.Index(src, "qemuargs = [")
	if q < 0 {
		t.Fatalf("%s: no qemuargs", file)
	}
	end := strings.Index(src[q:], "\n  ]")
	if end < 0 {
		t.Fatalf("%s: qemuargs does not end", file)
	}
	args := src[q : q+end]
	if n := strings.Count(args, `["-uuid", local.build_uuid]`); n != 1 {
		t.Errorf("%s: qemuargs passes [\"-uuid\", local.build_uuid] %d times, want once", file, n)
	}
	if n := strings.Count(args, `"-uuid"`); n != 1 {
		t.Errorf("%s: qemuargs names -uuid %d times, want once", file, n)
	}
}

// buildMACFuncs are the Packer functions the build_mac locals call, as
// Packer defines them (sha256: the hex SHA-256 of a string).
var buildMACFuncs = map[string]function.Function{
	"sha256": function.New(&function.Spec{
		Params: []function.Parameter{{Name: "str", Type: cty.String}},
		Type:   function.StaticReturnType(cty.String),
		Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
			return cty.StringVal(fmt.Sprintf("%x", sha256.Sum256([]byte(args[0].AsString())))), nil
		},
	}),
	"substr": stdlib.SubstrFunc,
	"format": stdlib.FormatFunc,
}

// derivedMAC is the one rule (docs/decisions/0024): 52:54:00, then three
// bytes of the UUID's SHA-256, skipping six hex digits past each leading
// "1234", so never 52:54:00:12:34:xx, QEMU's own default range.
func derivedMAC(uuid string) string {
	h := fmt.Sprintf("%x", sha256.Sum256([]byte(uuid)))
	i := 0
	for h[i:i+4] == "1234" {
		i += 6
	}
	return "52:54:00:" + h[i:i+2] + ":" + h[i+2:i+4] + ":" + h[i+4:i+6]
}

// CheckBuildMAC holds templates/<osName>/<file> to giving the build's NIC
// an address of its own, derived from local.build_uuid by the one rule:
// qemuargs' NIC row is ["-device", "${var.nic},netdev=net0,mac=${local.build_mac}"],
// and the template's own build_uuid_sha, build_mac_at and build_mac
// locals, evaluated here for several UUIDs (one whose SHA-256 begins
// 1234), give what derivedMAC gives.
func CheckBuildMAC(t *testing.T, osName, file string) {
	t.Helper()
	path := filepath.Join(RepoRoot(t), "templates", osName, file)
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(src), `["-device", "${var.nic},netdev=net0,mac=${local.build_mac}"]`); n != 1 {
		t.Errorf("%s: the NIC row passes mac=${local.build_mac} %d times, want once", file, n)
	}
	f, diags := hclsyntax.ParseConfig(src, path, hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing %s: %v", path, diags)
	}
	attrs := map[string]*hclsyntax.Attribute{}
	for _, b := range f.Body.(*hclsyntax.Body).Blocks {
		if b.Type == "locals" {
			for k, a := range b.Body.Attributes {
				attrs[k] = a
			}
		}
	}
	for _, k := range []string{"build_uuid_sha", "build_mac_at", "build_mac"} {
		if attrs[k] == nil {
			t.Fatalf("%s: no local.%s", file, k)
		}
	}
	for _, uuid := range []string{
		"a0db3eed-cbc6-41be-8172-3d17a6d5e58b",
		"00000000-0000-4000-8000-0000000063b0", // SHA-256 1234fe56dedb...
		"b83f5860-efee-4aaa-adb5-e9866104fe46",
	} {
		locals := map[string]cty.Value{"build_uuid": cty.StringVal(uuid)}
		for _, k := range []string{"build_uuid_sha", "build_mac_at", "build_mac"} {
			v, diags := attrs[k].Expr.Value(&hcl.EvalContext{
				Variables: map[string]cty.Value{"local": cty.ObjectVal(locals)},
				Functions: buildMACFuncs,
			})
			if diags.HasErrors() {
				t.Fatalf("%s: local.%s for %s: %v", file, k, uuid, diags)
			}
			locals[k] = v
		}
		if got, want := locals["build_mac"].AsString(), derivedMAC(uuid); got != want {
			t.Errorf("%s: local.build_mac for %s is %s, want %s", file, uuid, got, want)
		}
	}
}

// CheckInstallLog holds templates/<osName>/<file> to keeping the install's
// serial log beside the output directory, never in it (docs/decisions/0027):
// local.install_log is "${local.output_directory}-install.log", and the
// source's qemuargs, as HCL parses it (so a commented-out row is no row),
// passes exactly one -serial, ["-serial", "file:${local.install_log}"].
// Packer deletes the output directory when a build fails, and a failed
// install is the build whose log matters.
func CheckInstallLog(t *testing.T, osName, file string) {
	t.Helper()
	path := filepath.Join(RepoRoot(t), "templates", osName, file)
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, diags := hclsyntax.ParseConfig(src, path, hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing %s: %v", path, diags)
	}
	body := f.Body.(*hclsyntax.Body)
	locals := map[string]*hclsyntax.Attribute{}
	var qemuargs *hclsyntax.Attribute
	for _, b := range body.Blocks {
		switch {
		case b.Type == "locals":
			for k, a := range b.Body.Attributes {
				locals[k] = a
			}
		case b.Type == "source" && len(b.Labels) == 2 && b.Labels[0] == "qemu":
			qemuargs = b.Body.Attributes["qemuargs"]
		}
	}
	for _, k := range []string{"output_directory", "install_log"} {
		if locals[k] == nil {
			t.Fatalf("%s: no local.%s", file, k)
		}
	}
	out, d := locals["output_directory"].Expr.Value(nil)
	if d.HasErrors() || out.Type() != cty.String {
		t.Fatalf("%s: local.output_directory is not a string: %v", file, d)
	}
	ctx := &hcl.EvalContext{Variables: map[string]cty.Value{
		"local": cty.ObjectVal(map[string]cty.Value{"output_directory": out}),
	}}
	logv, d := locals["install_log"].Expr.Value(ctx)
	if d.HasErrors() {
		t.Fatalf("%s: local.install_log: %v", file, d)
	}
	if got, want := logv.AsString(), out.AsString()+"-install.log"; got != want {
		t.Errorf("%s: local.install_log is %q, want %q (beside %q, not in it)", file, got, want, out.AsString())
	}
	if qemuargs == nil {
		t.Fatalf("%s: the qemu source has no qemuargs", file)
	}
	rows, ok := qemuargs.Expr.(*hclsyntax.TupleConsExpr)
	if !ok {
		t.Fatalf("%s: qemuargs is not a list", file)
	}
	ctx.Variables["local"] = cty.ObjectVal(map[string]cty.Value{"output_directory": out, "install_log": logv})
	serials := 0
	for _, r := range rows.Exprs {
		row, ok := r.(*hclsyntax.TupleConsExpr)
		if !ok || len(row.Exprs) == 0 {
			continue
		}
		first, d := row.Exprs[0].Value(nil)
		if d.HasErrors() || first.Type() != cty.String || first.AsString() != "-serial" {
			continue
		}
		serials++
		if len(row.Exprs) != 2 {
			t.Errorf("%s: a -serial row with %d items, want 2", file, len(row.Exprs))
			continue
		}
		v, d := row.Exprs[1].Value(ctx)
		if d.HasErrors() {
			t.Errorf("%s: -serial's value: %v", file, d)
			continue
		}
		if got, want := v.AsString(), "file:"+logv.AsString(); got != want {
			t.Errorf("%s: -serial %q, want %q", file, got, want)
		}
	}
	if serials != 1 {
		t.Errorf("%s: qemuargs passes -serial %d times, want once: the hook follows the logs to the first port", file, serials)
	}
}

// CheckHandshakeAttempts holds templates/<osName>/<file>'s qemu source to
// waiting install_timeout for SSH and to ending the build after a number
// of refused logins (ssh_handshake_attempts, a literal from 1 to 20): how
// a failed install ends the build at once (docs/decisions/0028). Packer
// counts refusals only when the template sets the attempts, since
// ssh_timeout is set.
func CheckHandshakeAttempts(t *testing.T, osName, file string) {
	t.Helper()
	path := filepath.Join(RepoRoot(t), "templates", osName, file)
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, diags := hclsyntax.ParseConfig(src, path, hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing %s: %v", path, diags)
	}
	for _, b := range f.Body.(*hclsyntax.Body).Blocks {
		if b.Type != "source" || len(b.Labels) != 2 || b.Labels[0] != "qemu" {
			continue
		}
		a := b.Body.Attributes["ssh_handshake_attempts"]
		if a == nil {
			t.Fatalf("%s: no ssh_handshake_attempts: a failed install would wait out install_timeout", file)
		}
		v, d := a.Expr.Value(nil)
		if d.HasErrors() || v.Type() != cty.Number {
			t.Fatalf("%s: ssh_handshake_attempts is not a literal number: %v", file, d)
		}
		n, _ := v.AsBigFloat().Int64()
		if n < 1 || n > 20 {
			t.Errorf("%s: ssh_handshake_attempts = %d, want 1 to 20", file, n)
		}
		to := b.Body.Attributes["ssh_timeout"]
		if to == nil || string(to.Expr.Range().SliceBytes(src)) != "var.install_timeout" {
			t.Errorf("%s: ssh_timeout is not var.install_timeout", file)
		}
		return
	}
	t.Fatalf("%s: no qemu source", file)
}

// CheckGuestNetwork holds templates/<osName>/<file>'s qemu source to one
// guest network, as HCL parses its qemuargs (so a commented-out row is no
// row): exactly one -netdev row, a user (slirp) netdev with id net0, the
// one hostfwd the communicator uses, no guestfwd, and restrict=on, so the
// build's guest reaches nothing off-link while the communicator's forward
// still works (docs/decisions/0034). Every template's build is held to it.
func CheckGuestNetwork(t *testing.T, osName, file string) {
	t.Helper()
	path := filepath.Join(RepoRoot(t), "templates", osName, file)
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, diags := hclsyntax.ParseConfig(src, path, hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing %s: %v", path, diags)
	}
	var qemuargs *hclsyntax.Attribute
	for _, b := range f.Body.(*hclsyntax.Body).Blocks {
		if b.Type == "source" && len(b.Labels) == 2 && b.Labels[0] == "qemu" {
			qemuargs = b.Body.Attributes["qemuargs"]
		}
	}
	if qemuargs == nil {
		t.Fatalf("%s: the qemu source has no qemuargs", file)
	}
	rows, ok := qemuargs.Expr.(*hclsyntax.TupleConsExpr)
	if !ok {
		t.Fatalf("%s: qemuargs is not a list", file)
	}
	var netdevs []string
	for _, r := range rows.Exprs {
		row, ok := r.(*hclsyntax.TupleConsExpr)
		if !ok || len(row.Exprs) == 0 {
			continue
		}
		first, d := row.Exprs[0].Value(nil)
		if d.HasErrors() || first.Type() != cty.String {
			continue
		}
		if first.AsString() == "-nic" || first.AsString() == "-net" {
			t.Errorf("%s: qemuargs passes %s: the guest's one network is the -netdev row", file, first.AsString())
		}
		if first.AsString() != "-netdev" {
			continue
		}
		if len(row.Exprs) != 2 {
			t.Errorf("%s: a -netdev row with %d items, want 2", file, len(row.Exprs))
			continue
		}
		v, d := row.Exprs[1].Value(nil)
		if d.HasErrors() || v.Type() != cty.String {
			t.Errorf("%s: -netdev's value is not a literal string: %v", file, d)
			continue
		}
		netdevs = append(netdevs, v.AsString())
	}
	if len(netdevs) != 1 {
		t.Fatalf("%s: qemuargs passes -netdev %d times, want once", file, len(netdevs))
	}
	parts := strings.Split(netdevs[0], ",")
	if parts[0] != "user" {
		t.Errorf("%s: -netdev %q is not a user netdev", file, netdevs[0])
	}
	opts := map[string][]string{}
	for _, p := range parts[1:] {
		k, v, _ := strings.Cut(p, "=")
		opts[k] = append(opts[k], v)
	}
	if got := opts["id"]; len(got) != 1 || got[0] != "net0" {
		t.Errorf("%s: -netdev %q: id is %v, want net0 (the NIC's netdev=net0)", file, netdevs[0], got)
	}
	if got, want := opts["hostfwd"], "tcp:127.0.0.1:{{ .SSHHostPort }}-:22"; len(got) != 1 || got[0] != want {
		t.Errorf("%s: -netdev %q: hostfwd is %v, want only %s, the communicator's", file, netdevs[0], got, want)
	}
	if got := opts["guestfwd"]; len(got) != 0 {
		t.Errorf("%s: -netdev %q passes guestfwd %v: a way off-link", file, netdevs[0], got)
	}
	restrict := opts["restrict"]
	if len(restrict) > 1 {
		t.Errorf("%s: -netdev %q passes restrict %d times", file, netdevs[0], len(restrict))
	}
	if len(restrict) != 1 || restrict[0] != "on" {
		t.Errorf("%s: -netdev %q: want restrict=on, so the build's guest reaches nothing off-link (docs/decisions/0034)", file, netdevs[0])
	}
}
