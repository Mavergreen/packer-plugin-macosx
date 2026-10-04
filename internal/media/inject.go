package media

import (
	"archive/tar"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Mavergreen/packer-plugin-macosx"
)

// FirstbootPkgName is the first-boot payload's name on the media -- fixed,
// not taken from its source, so OSInstall.collection cannot drift from it.
const FirstbootPkgName = "mqg-firstboot.pkg"

// An AutoinstallFile is one of Apple's unattended-install hooks, which
// /etc/rc.install reads (assets/guest/autoinstall/): its source, where it goes
// on the media, and its mode -- rc.cdrom.local must be executable or
// rc.install skips it silently.
type AutoinstallFile struct {
	Source, Dest string
	Mode         int64
}

// AutoinstallFiles are the hooks, their sources under
// assets/guest/autoinstall/.
var AutoinstallFiles = []AutoinstallFile{
	{"autoinstall.sh", "private/etc/rc.cdrom.local", 0o755},
	{"minstallconfig.xml", "System/Installation/Packages/Extras/minstallconfig.xml", 0o644},
	{"OSInstall.collection", "System/Installation/Packages/OSInstall.collection", 0o644},
}

// AutoinstallFilesFor is a release's hooks: Mavericks' AutoinstallFiles,
// or for "snowleopard" the same three with minstallconfig.xml in /etc,
// where 10A432's /etc/rc.install looks for it (MEASURED 2026-10-04).
func AutoinstallFilesFor(release string) []AutoinstallFile {
	if release != "snowleopard" {
		return AutoinstallFiles
	}
	files := append([]AutoinstallFile(nil), AutoinstallFiles...)
	for i := range files {
		if files[i].Source == "minstallconfig.xml" {
			files[i].Dest = "private/etc/minstallconfig.xml"
		}
	}
	return files
}

// Injectables is what a build adds to the media beyond Apple's files.
type Injectables struct {
	Release      string // "" or "mavericks", or "snowleopard": where the hooks go
	Autoinstall  bool
	FirstbootPkg string   // installed by the OS installer, listed in OSInstall.collection
	ExtraPkgs    []string // carried beside it, NOT listed: firstboot.sh installs them
}

var tarTime = time.Unix(0, 0)

// Enabled is whether there is anything to inject: Autoinstall, or any
// package, which implies it: a first-boot or extra package needs the
// unattended-install hooks to be installed at all. A package named and then dropped
// would be media built without it, and nothing to say so.
func (in Injectables) Enabled() bool {
	return in.Autoinstall || in.FirstbootPkg != "" || len(in.ExtraPkgs) > 0
}

// packages is the packages to carry, by their name on the media, and
// refuses two that would land on one name.
func (in Injectables) packages() (map[string]string, error) {
	pkgs := map[string]string{}
	if in.FirstbootPkg != "" {
		pkgs[FirstbootPkgName] = in.FirstbootPkg
	}
	for _, e := range in.ExtraPkgs {
		base := filepath.Base(e)
		if prev, dup := pkgs[base]; dup {
			return nil, fmt.Errorf("%s and %s would both be System/Installation/Packages/%s on the media", prev, e, base)
		}
		pkgs[base] = e
	}
	return pkgs, nil
}

// WriteTar writes the injectables as a tar of FILES ONLY, named for
// where they go on the media: a directory entry would set the mode of a
// directory Apple's media already has (it made five of them
// group-writable once). The guest untars it onto the volume before the
// ownership pass, which is what makes these root-owned. When nothing is
// Enabled it writes an empty tar. log, if not nil, hears of each file.
func (in Injectables) WriteTar(w io.Writer, log func(string, ...any)) error {
	tw := tar.NewWriter(w)
	if !in.Enabled() {
		return tw.Close()
	}
	pkgs, err := in.packages()
	if err != nil {
		return err
	}
	add := func(name string, mode int64, data []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(data)),
			ModTime: tarTime, Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
			return err
		}
		if _, err := tw.Write(data); err != nil {
			return err
		}
		if log != nil {
			log("  %s (%o, %d bytes)", name, mode, len(data))
		}
		return nil
	}
	for _, f := range AutoinstallFilesFor(in.Release) {
		data, err := fs.ReadFile(macosx.Files, "assets/guest/autoinstall/"+f.Source)
		if err != nil {
			return err
		}
		if f.Source == "OSInstall.collection" && in.FirstbootPkg != "" {
			var n int
			if data, n, err = AddToCollection(data, "/System/Installation/Packages/"+FirstbootPkgName); err != nil {
				return err
			}
			if log != nil {
				log("OSInstall.collection now lists %d package(s)", n)
			}
		}
		if err := add(f.Dest, f.Mode, data); err != nil {
			return err
		}
	}
	var names []string
	for n := range pkgs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		data, err := os.ReadFile(pkgs[n])
		if err != nil {
			return err
		}
		if err := add("System/Installation/Packages/"+n, 0o644, data); err != nil {
			return err
		}
	}
	return tw.Close()
}

// AddToCollection inserts one package into OSInstall.collection as a
// line of text before the first </array> -- not a plist round-trip, which
// would drop the comment explaining why OSInstall.mpkg is listed twice --
// and then parses the result, because an unparseable collection fails the
// install with a dialog and nothing written. A collection that already
// lists entry comes back unchanged. It returns the collection and how
// many packages it lists.
func AddToCollection(collection []byte, entry string) ([]byte, int, error) {
	line := "\t<string>" + entry + "</string>\n"
	text := string(collection)
	if !strings.Contains(text, line) {
		i := strings.Index(text, "</array>")
		if i < 0 {
			return nil, 0, errors.New("OSInstall.collection has no </array> to add the payload before")
		}
		text = text[:i] + line + text[i:]
	}
	pkgs, err := collectionEntries([]byte(text))
	if err != nil {
		return nil, 0, fmt.Errorf("OSInstall.collection does not parse after the edit: %w", err)
	}
	found := false
	for _, p := range pkgs {
		found = found || p == entry
	}
	if !found {
		return nil, 0, fmt.Errorf("%s is not in OSInstall.collection after editing", entry)
	}
	return []byte(text), len(pkgs), nil
}

// collectionEntries is the plist's top-level array of strings:
// <plist><array><string>...</string>...</array></plist>. A strict
// decoder, so an element left open or closed out of order is an error.
func collectionEntries(b []byte) ([]string, error) {
	d := xml.NewDecoder(bytes.NewReader(b))
	d.Strict = true
	var (
		stack    []string
		entries  []string
		cur      strings.Builder
		sawArray bool
	)
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			stack = append(stack, t.Name.Local)
			sawArray = sawArray || pathIs(stack, "plist", "array")
			cur.Reset()
		case xml.EndElement:
			if pathIs(stack, "plist", "array", "string") {
				entries = append(entries, cur.String())
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			cur.Write(t)
		}
	}
	if len(stack) != 0 {
		return nil, fmt.Errorf("<%s> is never closed", stack[len(stack)-1])
	}
	if !sawArray {
		return nil, errors.New("no top-level <array>")
	}
	return entries, nil
}

func pathIs(stack []string, want ...string) bool {
	if len(stack) != len(want) {
		return false
	}
	for i := range want {
		if stack[i] != want[i] {
			return false
		}
	}
	return true
}
