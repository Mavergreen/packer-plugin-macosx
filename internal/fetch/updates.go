package fetch

import (
	"context"
	"fmt"
	"path/filepath"
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

// snowLeopardSources is 10.6's updateSources: Apple's 10.6.8 combo
// product's client packages in its distribution's order, then 10.6's last
// security update (assets/pins/sources.tsv says which catalogue products).
var snowLeopardSources = map[string][]string{
	"none": nil,
	"security": {
		"apple-subasesystem-combo-10.6.8",
		"apple-client-combo-10.6.8",
		"apple-rosetta-combo-10.6.8",
		"apple-qt7-combo-10.6.8",
		"apple-x11-combo-10.6.8",
		"apple-secupd-2013-004-snowleopard",
	},
}

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
type Update struct{ Name, Path, Staged string }

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
	names, err := UpdateNames(selection)
	if err != nil {
		return nil, err
	}
	var out []Update
	for i, n := range names {
		src, err := reg.Lookup(n)
		if err != nil {
			return nil, err
		}
		path, err := g.Get(ctx, Item{Name: n, URL: src.URL, SHA256: src.SHA256})
		if err != nil {
			return nil, err
		}
		ok, err := HasXarMagic(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if !ok {
			return nil, fmt.Errorf("%s is not a flat package (no xar magic)", path)
		}
		out = append(out, Update{Name: n, Path: path, Staged: StagedName(i+1, path)})
	}
	return out, nil
}
