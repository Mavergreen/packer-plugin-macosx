// Package inputs is what a data source consumed: the repository-side
// inputs of each part it builds (RepoRows, by the name of that part --
// "esd", "opencore", "media" and so on), the updates stamp
// (UpdatesStamp), and the sorted, digestible listing they make together
// (Listing, Digest), which names a data source's store entry.
package inputs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	vmguest "github.com/Mavergreen/packer-plugin-mavericks"
	"github.com/Mavergreen/packer-plugin-mavericks/internal/fetch"
	"github.com/Mavergreen/packer-plugin-mavericks/internal/firmware"
	"github.com/Mavergreen/packer-plugin-mavericks/internal/pins"
)

// Row is one "key<TAB>value" line of an input listing.
type Row struct{ Key, Value string }

func (r Row) line() string { return r.Key + "\t" + r.Value }

// repoParts is the parts a data source builds that have repository-side
// inputs to declare: installesd lists esd; firmware lists opencore, ovmf
// and efi; media lists payload and media.
var repoParts = []string{"esd", "opencore", "ovmf", "efi", "payload", "media"}

// RepoRows is the part's repository-side inputs: the pins, patches and
// embedded files it builds from (the payload's are the three files it
// embeds: com.mqg.firstboot.plist, firstboot.sh, postinstall). compiler
// is the firmware build's compiler line, or "unknown" when there is
// none; only opencore and ovmf use it.
func RepoRows(reg *pins.Registry, part, compiler string) ([]Row, error) {
	if compiler == "" {
		compiler = "unknown"
	}
	switch part {
	case "esd":
		return []Row{
			sourcePin(reg, "apple-installesd-10.9.5"),
			sourceURLPin(reg, "apple-installesd-10.9.5"),
		}, nil
	case "opencore":
		return bootStackListing(reg, compiler, nil)
	case "ovmf":
		return bootStackListing(reg, compiler, []Row{
			{"ovmf-build", firmware.OVMFDsc + " " + firmware.Arch + " " + firmware.EDKToolchain + " " + firmware.EDKTarget},
		})
	case "efi":
		return efiRows(reg)
	case "payload":
		return payloadRows()
	case "media":
		return mediaRows(reg)
	default:
		return nil, fmt.Errorf("no such part %q; parts are: %s", part, strings.Join(repoParts, " "))
	}
}

// bootStackListing is boot_stack_pins plus assets/firmware/patches/ plus the
// compiler line, shared by opencore and ovmf (the same tree, the same
// patches, the same compiler); extra is ovmf's own ovmf-build row, or nil
// for opencore.
func bootStackListing(reg *pins.Registry, compiler string, extra []Row) ([]Row, error) {
	rows := bootStackPins(reg)
	patches, err := treeRows("patch", "assets/firmware/patches")
	if err != nil {
		return nil, err
	}
	rows = append(rows, patches...)
	rows = append(rows,
		Row{"compiler", compiler},
		Row{"build-options", strings.ReplaceAll(firmware.BuildOptions(), "\t", " ")},
	)
	return append(rows, extra...), nil
}

// bootStackPins is the boot stack's pinned sources: OpenCorePkg,
// ocbuild's efibuild.sh, and every audk-* row of the registry -- rows
// with three or more fields, the same count pins.Ingredients uses via
// Registry.Complete.
func bootStackPins(reg *pins.Registry) []Row {
	rows := []Row{sourcePin(reg, "opencorepkg-src"), sourcePin(reg, "ocbuild-efibuild")}
	for i, s := range reg.Rows() {
		if !strings.HasPrefix(s.Name, "audk-") || !reg.Complete(i) {
			continue
		}
		sha := s.SHA256
		if sha == "" {
			sha = "ABSENT"
		}
		rows = append(rows, Row{"source:" + s.Name, sha})
	}
	return rows
}

func efiRows(reg *pins.Registry) ([]Row, error) {
	sum, err := embeddedSHA256("assets/firmware/config.plist")
	if err != nil {
		return nil, err
	}
	return []Row{
		{"config.plist", sum},
		sourcePin(reg, "lilu-release"),
		sourcePin(reg, "virtualsmc-release"),
	}, nil
}

// payloadRows lists assets/guest's own top-level files -- currently
// com.mqg.firstboot.plist, firstboot.sh and postinstall -- as "payload:"
// rows; treeRows does not recurse, so assets/guest/autoinstall/ (mediaRows'
// own listing, below) is not among them. The listing is of what the
// binary embeds, not of the checkout: a new file in assets/guest/ joins
// the payload listing only once it is also named on embed.go's go:embed
// line, and one in assets/guest/autoinstall/ joins media's the same way.
// TestEveryAssetOnDiskIsEmbedded makes forgetting that a test failure.
func payloadRows() ([]Row, error) {
	rows, err := treeRows("payload", "assets/guest")
	if err != nil {
		return nil, err
	}
	tag, err := fetch.OpenSSHTag()
	if err != nil {
		return nil, err
	}
	return append(rows, Row{"component:openssh", tag}), nil
}

// mediaRows lists the autoinstall hooks the media carries, the pinned
// ESD, the OpenSSH release, and -- as "privops:" rows -- the privops
// microVM's own scripts (assets/privops/*.sh): assemble.sh lays out the
// media's volume and content-digest.sh names what is on it, so a change
// to either reshapes the media as surely as a changed hook does.
func mediaRows(reg *pins.Registry) ([]Row, error) {
	rows, err := treeRows("autoinstall", "assets/guest/autoinstall")
	if err != nil {
		return nil, err
	}
	privops, err := treeRows("privops", "assets/privops")
	if err != nil {
		return nil, err
	}
	rows = append(rows, privops...)
	rows = append(rows, sourcePin(reg, "apple-installesd-10.9.5"))
	tag, err := fetch.OpenSSHTag()
	if err != nil {
		return nil, err
	}
	return append(rows, Row{"component:openssh", tag}), nil
}

// sourcePin is source:<name> with the registry row's sha256, or ABSENT
// when the row is missing or unpinned -- an input that vanished is a
// change, and an empty value would hash the same as a missing line.
func sourcePin(reg *pins.Registry, name string) Row {
	sha, _, ok := findSource(reg, name)
	if !ok || sha == "" {
		sha = "ABSENT"
	}
	return Row{"source:" + name, sha}
}

// sourceURLPin is source-url:<name>: the URL is how to get the artifact,
// not what it is, but esd is the one ingredient whose URL is listed
// beside its checksum (Apple's transfer is plain HTTP, and a bumped URL
// is where that would show up first). Unlike sourcePin, a missing row or
// an empty URL is listed empty, not ABSENT -- a listing that is held to
// its golden byte for byte.
func sourceURLPin(reg *pins.Registry, name string) Row {
	_, url, _ := findSource(reg, name)
	return Row{"source-url:" + name, url}
}

// findSource is the registry's first row named name -- unlike
// Registry.Lookup, it does not refuse an unpinned source: a listing must say ABSENT
// about one, not fail to list at all.
func findSource(reg *pins.Registry, name string) (sha, url string, ok bool) {
	for _, s := range reg.Rows() {
		if s.Name == name {
			return s.SHA256, s.URL, true
		}
	}
	return "", "", false
}

// treeRows is one "prefix:<path relative to dir>" row per embedded file
// directly inside dir (not its subdirectories), with its sha256. dir is a
// directory inside vmguest.Files, which carries only the files this
// binary actually embeds -- so this yields only the files it builds
// from, never whatever else a checkout holds. Not
// recursing matters for assets/guest: it holds payload's three files
// directly and media's assets/guest/autoinstall/ as a subdirectory, and
// the two must not bleed into each other's listing.
func treeRows(prefix, dir string) ([]Row, error) {
	var rows []Row
	err := fs.WalkDir(vmguest.Files, dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir {
				return fs.SkipDir
			}
			return nil
		}
		sum, err := embeddedSHA256(path)
		if err != nil {
			return err
		}
		rows = append(rows, Row{prefix + ":" + strings.TrimPrefix(path, dir+"/"), sum})
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func embeddedSHA256(path string) (string, error) {
	data, err := fs.ReadFile(vmguest.Files, path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Listing is RepoRows plus extras, sorted bytewise as "key\tvalue", so
// its digest does not depend on row order.
func Listing(repo, extras []Row) []string {
	rows := make([]string, 0, len(repo)+len(extras))
	for _, r := range repo {
		rows = append(rows, r.line())
	}
	for _, r := range extras {
		rows = append(rows, r.line())
	}
	sort.Strings(rows)
	return rows
}

// Digest is the sha256 of the sorted listing, each row
// newline-terminated: pins.Digest, the ingredient digest's own hashing.
func Digest(rows []string) string { return pins.Digest(rows) }

// UpdatesStamp is the updates' rows: none for "none"; otherwise
// updates=<selection> and one update:<name>=<pinned sha256> row per
// package name, in install order (fetch.UpdateNames's order).
func UpdatesStamp(reg *pins.Registry, selection string) ([]Row, error) {
	if selection == "none" {
		return nil, nil
	}
	names, err := fetch.UpdateNames(selection)
	if err != nil {
		return nil, err
	}
	rows := []Row{{"updates", selection}}
	for _, n := range names {
		src, err := reg.Lookup(n)
		if err != nil {
			return nil, err
		}
		rows = append(rows, Row{"update:" + n, src.SHA256})
	}
	return rows, nil
}
