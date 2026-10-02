package vmguest

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// The embedded copies must be the files in this tree: a binary built from
// this commit carries exactly what this commit says.
func TestEmbeddedFilesAreTheFilesOnDisk(t *testing.T) {
	want := []string{
		"assets/pins/sources.tsv",
		"components/openssh/version",
		"assets/firmware/config.plist",
		"assets/pins/apple-packages.sha256",
		"assets/guest/firstboot.sh",
		"assets/guest/postinstall",
		"assets/guest/com.mqg.firstboot.plist",
		"assets/firmware/patches/0001-build_oc-source-pinned-efibuild.patch",
		"assets/firmware/patches/0002-ovmf-pin-the-c-dialect.patch",
		"assets/firmware/patches/0003-firmware-drop-werror.patch",
		"assets/privops/assemble.sh",
		"assets/privops/content-digest.sh",
		"assets/privops/extract-basesystem.sh",
		"assets/privops/fix-ownership.sh",
		"assets/privops/verify-packages.sh",
		"assets/guest/autoinstall/autoinstall.sh",
		"assets/guest/autoinstall/minstallconfig.xml",
		"assets/guest/autoinstall/OSInstall.collection",
		"assets/privops/init.sh",
		"assets/vagrant/vagrant.pub.rsa",
		"assets/vagrant/vagrant-standard-insecure-first-boot-only.key.rsa",
	}
	for _, p := range want {
		emb, err := fs.ReadFile(Files, p)
		if err != nil {
			t.Errorf("%s not embedded: %v", p, err)
			continue
		}
		disk, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(emb, disk) || len(emb) == 0 {
			t.Errorf("%s: embedded copy differs from the file on disk", p)
		}
	}
}

// Every file under assets/ must be embedded -- every patch, guest script,
// privops payload and pin file -- so a new one cannot be forgotten from
// the //go:embed line. That matters beyond the binary: the data sources'
// listings (internal/inputs' treeRows) list what is embedded, so a guest,
// autoinstall or privops file on disk but not embedded would silently be
// left out of the listing that names a store entry. The two exceptions are documentation and
// lint configuration, which nothing reads at run time.
func TestEveryAssetOnDiskIsEmbedded(t *testing.T) {
	notEmbedded := map[string]bool{"README.md": true, ".shellcheckrc": true}
	n := 0
	err := filepath.WalkDir("assets", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || notEmbedded[d.Name()] {
			return err
		}
		n++
		if _, err := fs.ReadFile(Files, filepath.ToSlash(p)); err != nil {
			t.Errorf("%s is on disk but not embedded", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("found no files under assets/")
	}
}
