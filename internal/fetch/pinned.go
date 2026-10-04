package fetch

import (
	"context"

	"github.com/Mavergreen/packer-plugin-macosx/internal/pins"
)

// Pinned fetches the registry's source name into the cache and returns
// its path, for inputs that are neither Apple's nor the guest's OpenSSH
// -- the firmware's tarballs, efibuild.sh and the kext releases. The file
// is named by its URL's last path element. The returned path is
// read-only, as every path Get returns is.
func (g *Getter) Pinned(ctx context.Context, reg *pins.Registry, name string) (string, error) {
	src, err := reg.Lookup(name)
	if err != nil {
		return "", err
	}
	filename, err := Filename(src.URL)
	if err != nil {
		return "", err
	}
	return g.Get(ctx, Item{Name: name, URL: src.URL, SHA256: src.SHA256, Filename: filename})
}
