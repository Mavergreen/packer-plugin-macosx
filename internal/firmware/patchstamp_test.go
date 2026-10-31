package firmware

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mavergreen/packer-plugin-macosx"
)

// A tree's OVMF patches are skipped when their markers are already there,
// and the tree is kept while its audk commit is the pinned one. So without
// a record of which patch set a tree was patched with, a dropped or
// revised patch would live on in a cached tree while the firmware's cache
// key said otherwise (docs/decisions/0018). .guest-macosx-ovmf-patches is that
// record: one "<patch>\t<sha256>" line per OVMF patch the tree has had,
// in order.

// wantStamp is what a tree patched with all of this binary's OVMF patches
// is stamped with, computed here from the embedded files.
func wantStamp(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, n := range []string{"0002-ovmf-pin-the-c-dialect.patch", "0003-firmware-drop-werror.patch",
		"0004-ovmf-reserve-the-igvm-parameter-area.patch", "0005-ovmf-never-read-igvm-parameters.patch",
		"0006-ovmf-program-only-the-flash-bytes-that-change.patch"} {
		data, err := fs.ReadFile(macosx.Files, "assets/firmware/patches/"+n)
		if err != nil {
			t.Fatal(err)
		}
		s := sha256.Sum256(data)
		b.WriteString(n + "\t" + hex.EncodeToString(s[:]) + "\n")
	}
	return b.String()
}

func (f *fixture) stampPath() string { return filepath.Join(f.udk(), ".guest-macosx-ovmf-patches") }

func TestAFreshTreeIsStampedWithNoOVMFPatches(t *testing.T) {
	f := ovmfFixture(t)
	b, err := os.ReadFile(f.stampPath())
	if err != nil || len(b) != 0 {
		t.Errorf("a fresh tree's stamp = %q, %v; want empty", b, err)
	}
}

func TestOVMFStampsThePatchSetItApplied(t *testing.T) {
	f := ovmfFixture(t)
	if _, err := f.ovmf(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(f.stampPath()); string(b) != wantStamp(t) {
		t.Errorf("stamp =\n%s\nwant\n%s", b, wantStamp(t))
	}
}

// Each patch is stamped before it is applied: the stamp names the patch
// set a tree may hold, so a build that dies between applying a patch and
// recording it cannot leave a tree whose stamp is a prefix of a later,
// revised set while the tree holds the old revision. (Stamped after, a
// crash there left the stamp at k with patch k+1 in the tree; revise k+1
// keeping its marker, and the stale k+1 was kept and stamped as new.)
func TestOVMFStampsEachPatchBeforeApplyingIt(t *testing.T) {
	f := ovmfFixture(t)
	if _, err := f.ovmf(); err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(wantStamp(t), "\n")
	// stdins[0] is OpenCore's 0001; the OVMF patches follow.
	if len(f.stampsAt) != 6 {
		t.Fatalf("%d applies, want 0001 and five OVMF patches", len(f.stampsAt))
	}
	for i := 0; i < 5; i++ {
		if got, want := f.stampsAt[i+1], strings.Join(lines[:i+1], ""); got != want {
			t.Errorf("as patch %d was applied the stamp was\n%s\nwant it to name it already:\n%s", i+2, got, want)
		}
	}
}

// A build that failed on 0004 has stamped 0004 as attempted: the tree may
// hold it. The same plugin finishes the job; a revised 0004 reassembles.
func TestOVMFStampsAsItGoes(t *testing.T) {
	f := ovmfFixture(t)
	preDsc(f)
	f.patchesTake = false
	if _, err := f.ovmf(); err == nil {
		t.Fatal("0004 did not take, and OVMF went on")
	}
	lines := strings.SplitAfter(wantStamp(t), "\n")
	if b, _ := os.ReadFile(f.stampPath()); string(b) != lines[0]+lines[1]+lines[2] {
		t.Errorf("stamp after 0004 failed =\n%s\nwant 0002, 0003 and the attempted 0004", b)
	}
	f.patchesTake = true
	if _, err := f.ovmf(); err != nil {
		t.Fatalf("the same plugin could not finish the tree: %v", err)
	}
	if b, _ := os.ReadFile(f.stampPath()); string(b) != wantStamp(t) {
		t.Errorf("stamp after finishing =\n%s", b)
	}
}

func TestOVMFInterruptedRevisitPreservesPatchProvenance(t *testing.T) {
	f := ovmfFixture(t)
	if _, err := f.ovmf(); err != nil {
		t.Fatal(err)
	}
	want := wantStamp(t)
	// Preserve the fully patched file, but make the first patch's read fail.
	// This stops the old loop immediately after its first (truncating) write,
	// without a race, permission assumptions, or a production fault hook.
	dsc := filepath.Join(f.udk(), filepath.FromSlash(OVMFDsc))
	if err := os.Rename(dsc, dsc+".saved"); err != nil {
		t.Fatal(err)
	}
	n, builds := len(f.stdins), len(f.calls("bash"))
	if _, err := f.ovmf(); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("interrupted revisit: %v; want missing first patch target", err)
	}
	if err := os.Rename(dsc+".saved", dsc); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(f.stampPath()); err != nil || string(got) != want {
		t.Errorf("interrupted revisit stamp = %q, %v; want full provenance %q", got, err, want)
	}
	if len(f.stdins) != n || len(f.calls("bash")) != builds {
		t.Fatal("interrupted revisit applied a patch or built firmware")
	}
	stamp, err := ovmfPatchStamp()
	if err != nil {
		t.Fatal(err)
	}
	// Model a new binary revising 0006's bytes while keeping its name and
	// marker. Exercise the same full-hash acceptance gate used by both builds.
	revised := append([]string(nil), stamp...)
	revised[len(revised)-1] = ovmfPatches[len(ovmfPatches)-1].patch + "\t" + strings.Repeat("0", 64) + "\n"
	if f.b.patchStampFits(revised) {
		t.Error("revised late patch accepted the interrupted tree's stale contents")
	}
	// Model a binary dropping 0006, including OVMF refusal and OpenCore's
	// actual reuse/reassembly decision with the old marker still present.
	original := ovmfPatches
	t.Cleanup(func() { ovmfPatches = original })
	ovmfPatches = ovmfPatches[:len(ovmfPatches)-1]
	if _, err := f.ovmf(); err == nil || !strings.Contains(err.Error(), "reassemble the OpenCore tree") {
		t.Errorf("removed late patch: %v; want stale-tree refusal", err)
	}
	if len(f.stdins) != n || len(f.calls("bash")) != builds {
		t.Error("removed late patch patched or built the stale tree")
	}
	f.mustOpenCore()
	flash, err := os.ReadFile(filepath.Join(f.udk(), filepath.FromSlash(qemuFlashC)))
	if err != nil || strings.Contains(string(flash), marker0006) {
		t.Errorf("reassembly kept stale 0006: %q, %v", flash, err)
	}
	if _, err := f.ovmf(); err != nil {
		t.Fatalf("building the reassembled tree: %v", err)
	}
}

func TestOpenCoreKeepsATreeStampedWithThisPatchSet(t *testing.T) {
	for name, stamp := range map[string]func(*testing.T) string{
		"all of it":      wantStamp,
		"a prefix of it": func(t *testing.T) string { return strings.SplitAfter(wantStamp(t), "\n")[0] },
		"none of it":     func(*testing.T) string { return "" },
	} {
		f := ovmfFixture(t)
		if err := os.WriteFile(f.stampPath(), []byte(stamp(t)), 0o644); err != nil {
			t.Fatal(err)
		}
		sentinel := filepath.Join(f.udk(), "sentinel")
		if err := os.WriteFile(sentinel, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		f.mustOpenCore()
		if _, err := os.Stat(sentinel); err != nil {
			t.Errorf("%s: the tree was reassembled: %v", name, err)
		}
	}
}

// staleStamps are trees this binary must not reuse: patched by a binary
// whose 0005 differed, by one that carried a patch this one dropped, or
// by one from before the stamp existed, which cannot say.
func staleStamps(t *testing.T) map[string]*string {
	lines := strings.SplitAfter(wantStamp(t), "\n")
	revised := lines[0] + lines[1] + lines[2] + "0005-ovmf-never-read-igvm-parameters.patch\t" + strings.Repeat("0", 64) + "\n"
	dropped := wantStamp(t) + "0007-gone.patch\t" + strings.Repeat("1", 64) + "\n"
	return map[string]*string{"0005 revised": &revised, "a patch dropped": &dropped, "no stamp": nil}
}

func TestOpenCoreReassemblesATreeStampedWithAnotherPatchSet(t *testing.T) {
	for name, stamp := range staleStamps(t) {
		f := ovmfFixture(t)
		if _, err := f.ovmf(); err != nil {
			t.Fatal(err)
		}
		if stamp == nil {
			if err := os.Remove(f.stampPath()); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(f.stampPath(), []byte(*stamp), 0o644); err != nil {
			t.Fatal(err)
		}
		f.mustOpenCore()
		c, _ := os.ReadFile(filepath.Join(f.udk(), filepath.FromSlash(igvmC)))
		if strings.Contains(string(c), marker0005) || strings.Contains(string(c), "PlatformIgvmParamReserve") {
			t.Errorf("%s: the tree still carries the old patches:\n%s", name, c)
		}
		if b, err := os.ReadFile(f.stampPath()); err != nil || len(b) != 0 {
			t.Errorf("%s: the reassembled tree's stamp = %q, %v; want empty", name, b, err)
		}
		n := len(f.stdins)
		if _, err := f.ovmf(); err != nil {
			t.Fatal(err)
		}
		got0005 := false
		for _, p := range f.stdins[n:] {
			got0005 = got0005 || strings.Contains(string(p), "+// "+marker0005)
		}
		if !got0005 {
			t.Errorf("%s: 0005 was not applied to the reassembled tree", name)
		}
	}
}

// OVMF on its own, without OpenCore's assembly first, refuses such a tree
// rather than skip patches whose markers an older set left behind.
func TestOVMFRefusesATreeStampedWithAnotherPatchSet(t *testing.T) {
	for name, stamp := range staleStamps(t) {
		f := ovmfFixture(t)
		if stamp == nil {
			if err := os.Remove(f.stampPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(f.stampPath(), []byte(*stamp), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := f.ovmf()
		if err == nil || !strings.Contains(err.Error(), "reassemble the OpenCore tree") {
			t.Errorf("%s: err = %v", name, err)
		}
		if len(f.applies(f.udk(), "-p1", "-")) != 0 || len(f.calls("bash")) != 0 {
			t.Errorf("%s: patched or built a tree with another patch set", name)
		}
	}
}
