package privops

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/Mavergreen/packer-plugin-macosx"
	"github.com/Mavergreen/packer-plugin-macosx/internal/proc"
)

// newc is a cpio "newc" archive being written: what the kernel unpacks as
// an initramfs. The host needs no cpio(1) for it.
// Every member is owned by 0:0 with mtime 0, so the same inputs make the
// same archive.
type newc struct {
	buf bytes.Buffer
	ino uint32
}

// add appends one member: a 110-byte header of "070701" and thirteen
// 8-digit hex fields (ino, mode, uid, gid, nlink, mtime, filesize,
// devmajor, devminor, rdevmajor, rdevminor, namesize, check), the name
// with its NUL, and the data, each padded to four bytes.
func (w *newc) add(name string, mode uint32, data []byte) {
	w.ino++
	namez := name + "\x00"
	fmt.Fprintf(&w.buf, "070701%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X",
		w.ino, mode, 0, 0, 1, 0, len(data), 0, 0, 0, 0, len(namez), 0)
	w.buf.WriteString(namez)
	w.pad()
	w.buf.Write(data)
	w.pad()
}

func (w *newc) pad() {
	for w.buf.Len()%4 != 0 {
		w.buf.WriteByte(0)
	}
}

const (
	modeDir  = 0o040755
	modeExec = 0o100755
	modeFile = 0o100644
)

// buildInitramfs is the gzip'd newc archive the guest boots: busybox,
// /init, the payload, the modules in load order, and the disk roles, one
// per line in attach order (/dev/vdb is line 1). The roles and the order
// are files rather than kernel arguments because what the guest needs is
// the answer, not the request, and a cmdline carries neither a path with
// a space nor an order reliably.
func (b Backend) buildInitramfs(ctx context.Context, payload []byte, roles []string) ([]byte, error) {
	bbPath, err := b.Runner.LookPath("busybox")
	if err != nil {
		return nil, fmt.Errorf("cannot stage busybox: %w", err)
	}
	bb, err := os.ReadFile(bbPath)
	if err != nil {
		return nil, fmt.Errorf("cannot stage busybox: %w", err)
	}
	initScript, err := fs.ReadFile(macosx.Files, "assets/privops/init.sh")
	if err != nil {
		return nil, err
	}
	mods, order, err := b.stagedModules(ctx)
	if err != nil {
		return nil, err
	}
	w := &newc{}
	for _, d := range []string{"bin", "dev", "proc", "sys", "mnt", "lib", "lib/modules"} {
		w.add(d, modeDir, nil)
	}
	w.add("bin/busybox", modeExec, bb)
	for _, name := range order {
		w.add("lib/modules/"+name, modeFile, mods[name])
	}
	w.add("lib/modules/load-order", modeFile, []byte(joinLines(order)))
	w.add("disk-roles", modeFile, []byte(joinLines(roles)))
	w.add("init", modeExec, initScript)
	w.add("payload.sh", modeFile, payload)
	w.add("TRAILER!!!", 0, nil)

	var gz bytes.Buffer
	level := b.gzipLevel
	if level == 0 {
		level = gzip.BestCompression
	}
	zw, err := gzip.NewWriterLevel(&gz, level)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(w.buf.Bytes()); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return gz.Bytes(), nil
}

func joinLines(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return strings.Join(s, "\n") + "\n"
}

// moduleCache is a Backend's staged modules, shared by its copies, and
// what they were staged for.
type moduleCache struct {
	mu    sync.Mutex
	key   string
	mods  map[string][]byte
	order []string
}

// stagedModules is stageModules, once per Backend: the modules a host has
// do not change between the passes of one build, and staging them again
// on every pass repeated every "built into the kernel" line four times.
// The answer is kept for this module list, tree and release only, and a
// failure is not kept: the next Run tries again.
func (b Backend) stagedModules(ctx context.Context) (map[string][]byte, []string, error) {
	if b.staged == nil {
		return b.stageModules(ctx)
	}
	c := b.staged
	c.mu.Lock()
	defer c.mu.Unlock()
	key := strings.Join(append([]string{b.ModulesDir, b.KVer}, b.Modules...), "\x00")
	if c.mods != nil && c.key == key {
		return c.mods, c.order, nil
	}
	mods, order, err := b.stageModules(ctx)
	if err != nil {
		return nil, nil, err
	}
	c.key, c.mods, c.order = key, mods, order
	return mods, order, nil
}

// stageModules resolves each module, with its dependencies, the way the
// distribution's own modprobe would load it (modules.dep), falling back
// to a file search when there is no modprobe or it knows nothing; each is
// decompressed, because busybox insmod reads no compressed format and
// resolves no dependencies. A module found neither way is taken to be
// built in: legitimate and common. Dependencies are shared, so each
// object is staged once: inserting one twice is an error insmod reports.
func (b Backend) stageModules(ctx context.Context) (map[string][]byte, []string, error) {
	mods := map[string][]byte{}
	var order []string
	for _, m := range b.Modules {
		deps := b.modprobeDeps(ctx, m)
		if len(deps) == 0 {
			if p := b.findModule(m); p != "" {
				deps = []string{p}
			}
		}
		if len(deps) == 0 {
			b.logf("no %s module under %s -- assuming it is built into the kernel", m, filepath.Join(b.ModulesDir, b.KVer))
			continue
		}
		for _, src := range deps {
			base := filepath.Base(src)
			base = strings.TrimSuffix(base, ".zst")
			base = strings.TrimSuffix(base, ".xz")
			base = strings.TrimSuffix(base, ".gz")
			if _, seen := mods[base]; seen {
				continue
			}
			data, err := b.readModule(ctx, src)
			if err != nil {
				return nil, nil, err
			}
			mods[base] = data
			order = append(order, base)
		}
	}
	return mods, order, nil
}

// modprobeDeps is the insmod lines of modprobe --show-depends: the
// objects to insert, in order. Its exit status is not consulted: what it
// printed is the answer, and "builtin" or nothing sends the caller to the
// file search.
func (b Backend) modprobeDeps(ctx context.Context, m string) []string {
	if _, err := b.Runner.LookPath("modprobe"); err != nil {
		return nil
	}
	var out bytes.Buffer
	_ = b.Runner.Run(ctx, proc.Cmd{Name: "modprobe", Args: []string{"-S", b.KVer, "-n", "--show-depends", m}, Stdout: &out})
	var deps []string
	for _, line := range strings.Split(out.String(), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "insmod" && strings.HasPrefix(line, "insmod ") {
			deps = append(deps, f[1])
		}
	}
	return deps
}

// findModule is the first of m.ko, m.ko.zst, m.ko.xz and m.ko.gz under
// ModulesDir/KVer, in sorted path order ("" when there is none): not
// find's first, which is directory order; sorting makes the choice the
// same on every run.
func (b Backend) findModule(m string) string {
	var found []string
	root := filepath.Join(b.ModulesDir, b.KVer)
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		switch d.Name() {
		case m + ".ko", m + ".ko.zst", m + ".ko.xz", m + ".ko.gz":
			found = append(found, p)
		}
		return nil
	})
	if len(found) == 0 {
		return ""
	}
	sort.Strings(found)
	return found[0]
}

func (b Backend) readModule(ctx context.Context, src string) ([]byte, error) {
	switch {
	case strings.HasSuffix(src, ".gz"):
		f, err := os.Open(src)
		if err != nil {
			return nil, fmt.Errorf("cannot stage %s: %w", src, err)
		}
		defer f.Close()
		zr, err := gzip.NewReader(f)
		if err != nil {
			return nil, fmt.Errorf("cannot decompress %s: %w", src, err)
		}
		data, err := io.ReadAll(zr)
		if err != nil {
			return nil, fmt.Errorf("cannot decompress %s: %w", src, err)
		}
		return data, nil
	case strings.HasSuffix(src, ".xz"):
		return b.decompress(ctx, "xz", []string{"-dc", src}, src)
	case strings.HasSuffix(src, ".zst"):
		return b.decompress(ctx, "zstd", []string{"-dqc", src}, src)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return nil, fmt.Errorf("cannot stage %s: %w", src, err)
	}
	return data, nil
}

// decompress runs tool, which names its own format, on a module.
func (b Backend) decompress(ctx context.Context, tool string, args []string, src string) ([]byte, error) {
	if _, err := b.Runner.LookPath(tool); err != nil {
		return nil, fmt.Errorf("%s is %s-compressed and %s is not installed", src, tool, tool)
	}
	var out bytes.Buffer
	if err := b.Runner.Run(ctx, proc.Cmd{Name: tool, Args: args, Stdout: &out}); err != nil {
		return nil, fmt.Errorf("cannot decompress %s: %w", src, err)
	}
	return out.Bytes(), nil
}
