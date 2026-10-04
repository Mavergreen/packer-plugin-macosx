package template

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/Mavergreen/packer-plugin-macosx/internal/payload"
)

// TestVagrantKeyMatchesEmbedded guards
// templates/mavericks/vagrant-standard-insecure-first-boot-only.key.rsa: templates/mavericks/
// ships as its own release artifact, so it carries its own copy of
// Vagrant's insecure private key rather than reaching outside itself for
// assets/vagrant/vagrant-standard-insecure-first-boot-only.key.rsa. This
// test is what keeps the two copies
// from drifting apart -- payload.VagrantPrivateKey() reads the very same
// embedded file production code already trusts.
func TestVagrantKeyMatchesEmbedded(t *testing.T) {
	root := repoRoot(t)
	want := payload.VagrantPrivateKey()
	got, err := os.ReadFile(filepath.Join(root, "templates", "mavericks", "vagrant-standard-insecure-first-boot-only.key.rsa"))
	if err != nil {
		t.Fatalf("reading templates/mavericks/vagrant-standard-insecure-first-boot-only.key.rsa: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Error("templates/mavericks/vagrant-standard-insecure-first-boot-only.key.rsa does not match the embedded assets/vagrant/vagrant-standard-insecure-first-boot-only.key.rsa byte for byte")
	}
}
