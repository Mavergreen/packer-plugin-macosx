package fetch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Mavergreen/packer-plugin-macosx/internal/config"
	"github.com/Mavergreen/packer-plugin-macosx/internal/pins"
)

// updateSources is each updates selection's packages, in install order.
var updateSources = map[string][]string{
	"none":     nil,
	"security": {"apple-secupd-2016-004"},
	"all": {
		"apple-secupd-2016-004",
		"apple-safari-9.1.3",
		"apple-itunes-12.6.2-corefp",
		"apple-itunes-12.6.2-mobiledevice",
		"apple-itunes-12.6.2-itunesaccess",
		"apple-itunes-12.6.2-itunesx",
		"apple-itunes-12.6.2-coreadi",
	},
}

// snowLeopardSources is 10.6's updateSources: the members of Apple's
// client 10.6.8 combo product (catalogue product 041-98179), then the
// product's distribution, which installs them in one installer run, then
// Security Update 2013-004 (assets/pins/sources.tsv says which products).
var snowLeopardSources = map[string][]string{
	"none": nil,
	"security": append(comboMembers(),
		"apple-combo-10.6.8-dist",
		"apple-secupd-2013-004-snowleopard",
	),
}

// comboMembers is the 10.6.8 combo product's packages, in its
// distribution's order, then the one package of the product that the
// distribution does not name.
func comboMembers() []string {
	var m []string
	for i := 0; i <= 12; i++ {
		m = append(m, fmt.Sprintf("apple-combo-10.6.8-part%d", i))
	}
	return append(m, "apple-combo-10.6.8-subasesystem", "apple-combo-10.6.8-qt7",
		"apple-combo-10.6.8-x11", "apple-combo-10.6.8-rosetta", "apple-combo-10.6.8-meta")
}

// isMember is whether name is carried beside a distribution, which
// installs it, rather than installed by itself. MEASURED 2026-10-04:
// installed one at a time, the combo's packages leave a running 10.6.0
// with its new libSystem and its old dyld, and every process after the
// first crashes.
func isMember(name string) bool { return slices.Contains(comboMembers(), name) }

// UpdateNames is Mavericks' UpdateNamesFor.
func UpdateNames(selection string) ([]string, error) { return UpdateNamesFor("mavericks", selection) }

// UpdateNamesFor is a release's updates selection's packages, in install
// order: "mavericks" or "snowleopard".
func UpdateNamesFor(release, selection string) ([]string, error) {
	sources := updateSources
	if release == "snowleopard" {
		sources = snowLeopardSources
	}
	names, ok := sources[selection]
	if !ok {
		return nil, fmt.Errorf("unknown updates selection %q: choose one of %s", selection, strings.Join(config.UpdateChoicesFor(release), ", "))
	}
	return append([]string(nil), names...), nil
}

// Update is one fetched update. Name is its registry name; Path is the
// cache file, named as Apple names it; Staged is the name the installer
// media presents it under (StagedName) -- and so the name firstboot.conf
// must carry: payload.MediaFile{Path: u.Path, Name: u.Staged}, never the
// base of Path.
type Update struct {
	Name, Path, Staged string
	// Member is a package a distribution among these installs: carried
	// under its own name, never installed by itself.
	Member bool
}

// StagedName is how the installer media presents the n-th update
// (1-based): the install order is legible in the name, and the prefix
// keeps it from colliding with Apple's own packages on the media.
func StagedName(n int, path string) string {
	return fmt.Sprintf("mqg-update-%02d-%s", n, filepath.Base(path))
}

// Updates fetches one selection, verified against the registry, in the
// order the guest must install them. "none" fetches nothing. Each
// Update's Path is read-only (see Get); never open it for writing.
func (g *Getter) Updates(ctx context.Context, reg *pins.Registry, selection string) ([]Update, error) {
	return g.UpdatesFor(ctx, reg, "mavericks", selection)
}

// getUpdate fetches and checks one update: a flat package, or a
// distribution (an installer-gui-script), staged as the nth to install,
// or under its own name if it is a member.
func (g *Getter) getUpdate(ctx context.Context, reg *pins.Registry, name string, n int) (Update, error) {
	src, err := reg.Lookup(name)
	if err != nil {
		return Update{}, err
	}
	path, err := g.Get(ctx, Item{Name: name, URL: src.URL, SHA256: src.SHA256})
	if err != nil {
		return Update{}, err
	}
	if strings.HasSuffix(path, ".dist") {
		b, err := os.ReadFile(path)
		if err != nil {
			return Update{}, err
		}
		if !bytes.Contains(b, []byte("<installer-gui-script")) {
			return Update{}, fmt.Errorf("%s is not a distribution (no installer-gui-script)", path)
		}
	} else {
		ok, err := HasXarMagic(path)
		if err != nil {
			return Update{}, fmt.Errorf("%s: %w", path, err)
		}
		if !ok {
			return Update{}, fmt.Errorf("%s is not a flat package (no xar magic)", path)
		}
	}
	if isMember(name) {
		return Update{Name: name, Path: path, Staged: filepath.Base(path), Member: true}, nil
	}
	return Update{Name: name, Path: path, Staged: StagedName(n, path)}, nil
}

// UpdatesFor is Updates for a release's selection.
func (g *Getter) UpdatesFor(ctx context.Context, reg *pins.Registry, release, selection string) ([]Update, error) {
	names, err := UpdateNamesFor(release, selection)
	if err != nil {
		return nil, err
	}
	var out []Update
	n := 0
	for _, name := range names {
		if !isMember(name) {
			n++
		}
		u, err := g.getUpdate(ctx, reg, name, n)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}
