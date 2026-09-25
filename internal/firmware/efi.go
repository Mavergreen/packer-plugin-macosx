package firmware

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	vmguest "github.com/Mavergreen/packer-plugin-mavericks"
	"github.com/Mavergreen/packer-plugin-mavericks/internal/diskimg"
	"github.com/Mavergreen/packer-plugin-mavericks/internal/fetch"
	"github.com/Mavergreen/packer-plugin-mavericks/internal/proc"
)

// configDir is where a derived config.plist goes: build/config.
func (b *Builder) configDir() string { return filepath.Join(b.Paths.Build(), "config") }

// verifySums checks every file dir/SHA256SUMS lists, as `sha256sum -c`,
// and that it lists every name in required: a file the list leaves out
// is a file nothing checked.
func verifySums(dir string, required []string) error {
	sums := filepath.Join(dir, "SHA256SUMS")
	data, err := os.ReadFile(sums)
	if err != nil {
		return err
	}
	listed := map[string]bool{}
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		want, name, ok := strings.Cut(line, "  ")
		if !ok || len(want) != 64 || name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
			return fmt.Errorf("%s: a line that is not '<sha256>  <name>': %q", sums, line)
		}
		got, err := fetch.SHA256File(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("%s does not match %s -- rebuild", filepath.Join(dir, name), sums)
		}
		listed[name] = true
	}
	for _, n := range required {
		if !listed[n] {
			return fmt.Errorf("%s does not list %s -- rebuild", sums, n)
		}
	}
	return nil
}

// VerifyOVMF checks OVMF_CODE.fd and OVMF_VARS.fd in dir (build/firmware)
// against dir/SHA256SUMS before anything copies or boots them: a caller
// that hands a template one of Builder.OVMF's outputs verifies them
// unmodified first. A missing or non-matching sums file is refused,
// naming the file. Builder.OVMF writes dir/SHA256SUMS with WriteSums,
// whose format is byte for byte sha256sum's own ("<hex>  <name>"), so one
// written by sha256sum is accepted here unmodified.
func VerifyOVMF(dir string) error {
	if err := verifySums(dir, []string{"OVMF_CODE.fd", "OVMF_VARS.fd"}); err != nil {
		return fmt.Errorf("%w -- rebuild the firmware", err)
	}
	return nil
}

// ShippedFiles is what a mavericks-firmware store entry ships, and what
// its SHA256SUMS lists: the pflash pair and the OpenCore image.
var ShippedFiles = []string{"OVMF_CODE.fd", "OVMF_VARS.fd", "opencore.img"}

// VerifyShipped checks every one of ShippedFiles in dir against
// dir/SHA256SUMS, as VerifyOVMF does the pflash pair: a store entry
// reused from the cache is checked before a template boots any of it,
// since the store's "complete" marker says only that the build finished.
func VerifyShipped(dir string) error {
	if err := verifySums(dir, ShippedFiles); err != nil {
		return fmt.Errorf("%w -- rebuild the firmware", err)
	}
	return nil
}

// Fits says whether a payload of that many bytes belongs in an image of
// mib MiB, with room to spare: 1 MiB before the partition and ~4 MiB of
// FAT32 overhead reserved, then the payload twice over, so a driver or a
// kext can double without anyone redoing this arithmetic.
func Fits(mib int, payload int64) bool {
	capacity := int64(mib-1)*1024*1024 - 4*1024*1024
	return capacity > 0 && payload*2 <= capacity
}

// efiDirs is the image's directory tree, parents first.
var efiDirs = []string{"/EFI", "/EFI/BOOT", "/EFI/OC", "/EFI/OC/Drivers", "/EFI/OC/Kexts",
	"/EFI/OC/ACPI", "/EFI/OC/Tools", "/EFI/OC/Resources"}

// EFIImage assembles OpenCore's EFI image -- the artifacts, the kexts and
// config.plist on a FAT32 EFI System Partition -- and writes it to
// build/opencore.img with a .sha256 sidecar, without sgdisk or mtools,
// and deterministic.
// model "" means DefaultSMBIOS; debug turns on DebugSettings.
//
// config.plist ships verbatim unless model asks for a different SMBIOS
// or debug is on, when a copy with SystemProductName and/or the three
// debug settings changed (and nothing else) is written to build/config/,
// checked by the ocvalidate this OpenCore built, and shipped instead:
// the repository config's checksum is a row of the firmware's input
// listing, and a default build must ship exactly that file.
func (b *Builder) EFIImage(ctx context.Context, model string, debug bool) (string, error) {
	if model == "" {
		model = DefaultSMBIOS
	}
	if !SMBIOSWellformed(model) {
		return "", fmt.Errorf("SMBIOS %q is not a usable model identifier (letters, digits, comma, dot, dash, underscore; 64 at most)", model)
	}
	SMBIOSCheck(model, b.logf)
	plist, err := b.config(ctx, model, debug)
	if err != nil {
		return "", err
	}

	art := b.artifactsDir()
	if _, err := os.Stat(art); err != nil {
		return "", fmt.Errorf("no artifacts at %s -- build the OpenCore artifacts first", art)
	}
	if err := verifySums(art, ShipNames()); err != nil {
		return "", err
	}
	payload := int64(len(plist))
	files := map[string]string{
		"/EFI/BOOT/BOOTx64.efi": filepath.Join(art, "BOOTx64.efi"),
		"/EFI/OC/OpenCore.efi":  filepath.Join(art, "OpenCore.efi"),
	}
	order := []string{"/EFI/BOOT/BOOTx64.efi", "/EFI/OC/OpenCore.efi"}
	for _, d := range EFIDrivers {
		files["/EFI/OC/Drivers/"+d] = filepath.Join(art, d)
		order = append(order, "/EFI/OC/Drivers/"+d)
	}
	for _, p := range order {
		fi, err := os.Stat(files[p])
		if err != nil || !fi.Mode().IsRegular() {
			return "", fmt.Errorf("missing %s -- build the OpenCore artifacts", files[p])
		}
		payload += fi.Size()
	}
	for _, k := range Kexts {
		bundle := filepath.Join(b.kextsDir(), k.Name+".kext")
		if err := checkKext(bundle, k.Name); err != nil {
			return "", fmt.Errorf("%w -- remove %s and rebuild the EFI image", err, bundle)
		}
		n, err := treeSize(bundle)
		if err != nil {
			return "", err
		}
		payload += n
	}
	if !Fits(EFIImageMiB, payload) {
		return "", fmt.Errorf("a payload of %d bytes does not fit in %d MiB with headroom -- raise EFIImageMiB", payload, EFIImageMiB)
	}
	b.logf("payload is %d bytes; the image is %d MiB", payload, EFIImageMiB)

	fat, err := diskimg.NewFAT("EFI", serial("opencore"))
	if err != nil {
		return "", err
	}
	for _, d := range efiDirs {
		if err := fat.Mkdir(d); err != nil {
			return "", err
		}
	}
	for _, p := range order {
		data, err := os.ReadFile(files[p])
		if err != nil {
			return "", err
		}
		if err := fat.WriteFile(p, data); err != nil {
			return "", err
		}
	}
	if err := fat.WriteFile("/EFI/OC/config.plist", plist); err != nil {
		return "", err
	}
	for _, k := range Kexts {
		if err := addTree(fat, filepath.Join(b.kextsDir(), k.Name+".kext"), "/EFI/OC/Kexts/"+k.Name+".kext"); err != nil {
			return "", err
		}
	}

	out := b.Paths.OpenCoreImage()
	sum, err := writeImage(out, fat)
	if err != nil {
		return "", err
	}
	b.logf("built %s (sha256 %s)", out, sum)
	return out, nil
}

// config is the config.plist to ship for model and debug: the
// repository's, verbatim, or a derived copy, validated.
func (b *Builder) config(ctx context.Context, model string, debug bool) ([]byte, error) {
	base, err := fs.ReadFile(vmguest.Files, "assets/firmware/config.plist")
	if err != nil {
		return nil, err
	}
	have := ProductName(base)
	if model == have && !debug {
		b.logf("smbios: %s, as assets/firmware/config.plist has it", model)
		b.logf("debug: off, as assets/firmware/config.plist has it")
		return base, nil
	}
	derived := base
	name := "config-" + model
	if model != have {
		if derived, err = SetProductName(derived, model); err != nil {
			return nil, err
		}
	}
	if debug {
		if derived, err = SetDebug(derived, true); err != nil {
			return nil, err
		}
		name += "-debug"
	}
	path := filepath.Join(b.configDir(), name+".plist")
	if err := writeFileAtomic(path, derived, 0o644); err != nil {
		return nil, err
	}
	// The ocvalidate this OpenCore built checks the schema of exactly the
	// OpenCore that will read the config; one on PATH is the fallback.
	ocv := b.ocvalidate()
	if fi, err := os.Stat(ocv); err != nil || fi.Mode()&0o111 == 0 {
		ocv, _ = b.Runner.LookPath("ocvalidate")
	}
	if ocv == "" {
		b.logf("warning: no ocvalidate found; shipping the derived config unvalidated")
	} else if err := b.Runner.Run(ctx, proc.Cmd{Name: ocv, Args: []string{path}, Stderr: logWriter{b}}); err != nil {
		return nil, fmt.Errorf("ocvalidate rejected the derived config at %s: %w", path, err)
	} else {
		b.logf("ocvalidate accepts the derived config")
	}
	if model != have {
		b.logf("smbios: SystemProductName %s -> %s (%s)", have, model, path)
		b.logf("smbios: serial, board serial, ROM and UUID are unchanged -- OpenCore derives the board id from the product name (Automatic=true)")
	} else {
		b.logf("smbios: %s, as assets/firmware/config.plist has it", model)
	}
	if debug {
		b.logf("debug: on -- AppleDebug true, DisplayLevel 2147483714, boot-args with debug=0x100 (%s)", path)
	} else {
		b.logf("debug: off, as assets/firmware/config.plist has it")
	}
	return derived, nil
}

// addTree copies a directory tree into the image, keeping its shape:
// OpenCore reads paths inside a kext bundle, so it must arrive as a
// bundle. Directories come before what is in them (WalkDir's lexical
// order puts a parent first).
func addTree(fat *diskimg.FAT, src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := dst
		if rel != "." {
			target = dst + "/" + filepath.ToSlash(rel)
		}
		switch {
		case d.IsDir():
			return fat.Mkdir(target)
		case d.Type().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return fat.WriteFile(target, data)
		}
		return fmt.Errorf("%s: only files and directories can go into the image", p)
	})
}

// treeSize is the bytes of every regular file under dir. Directories
// count for nothing, where `du -sb` counts each one's size too (4096
// bytes a directory on ext4), so Fits is marginally laxer than a du-based
// check: by a few KiB against a margin of the payload again.
func treeSize(dir string) (int64, error) {
	var n int64
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		fi, err := d.Info()
		if err == nil {
			n += fi.Size()
		}
		return err
	})
	return n, err
}

// serial is a FAT volume serial number derived from seed, so the image
// is the same every time (mformat's is random).
func serial(seed string) uint32 {
	s := sha256.Sum256([]byte("mavericks fat serial " + seed))
	return binary.LittleEndian.Uint32(s[:4])
}

// writeImage writes a GPT disk of EFIImageMiB with one EFI System
// Partition holding fat, and its .sha256 sidecar, each to a temp file
// beside where it goes. Both are written and synced before either is
// renamed, then the image and the sidecar are renamed in that order: out
// is never half-written, a failed build leaves the previous image and
// sidecar alone, and the two disagree only between the two renames. It
// returns the image's sha256.
func writeImage(out string, fat *diskimg.FAT) (string, error) {
	const sectors = uint64(EFIImageMiB) * 1024 * 1024 / diskimg.SectorSize
	part := diskimg.Partition{Type: diskimg.TypeEFISystem, GUID: diskimg.DerivedGUID("opencore esp"),
		Name: "EFI", FirstLBA: diskimg.AlignLBA, LastLBA: diskimg.LastUsableLBA(sectors)}
	g, err := diskimg.FAT32Geometry(uint32(part.LastLBA-part.FirstLBA+1), uint32(part.FirstLBA))
	if err != nil {
		return "", err
	}
	var sum string
	img, err := stageFile(out, 0o644, func(f *os.File) error {
		if err := f.Truncate(int64(sectors) * diskimg.SectorSize); err != nil {
			return err
		}
		if err := diskimg.WriteGPT(f, sectors, diskimg.DerivedGUID("opencore disk"), []diskimg.Partition{part}); err != nil {
			return err
		}
		if err := fat.WriteTo(f, int64(part.FirstLBA)*diskimg.SectorSize, g); err != nil {
			return err
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return err
		}
		sum = hex.EncodeToString(h.Sum(nil))
		return nil
	})
	if err != nil {
		return "", err
	}
	side, err := stageFile(out+".sha256", 0o644, func(f *os.File) error {
		_, err := io.WriteString(f, sum+"\n")
		return err
	})
	if err != nil {
		os.Remove(img)
		return "", err
	}
	if err := os.Rename(img, out); err != nil {
		os.Remove(img)
		os.Remove(side)
		return "", err
	}
	if err := os.Rename(side, out+".sha256"); err != nil {
		os.Remove(side)
		return "", err
	}
	return sum, nil
}
