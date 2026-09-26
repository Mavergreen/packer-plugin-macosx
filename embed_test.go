package vmguest

import (
	"bytes"
	"io/fs"
	"os"
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
