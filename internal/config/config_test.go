package config

import (
	"path/filepath"
	"testing"
)

func TestCacheFileIsContentAddressed(t *testing.T) {
	p := Paths{Home: "/h"}
	if got := p.CacheFile("abc123", "x.zip"); got != filepath.Join("/h", "cache", "abc123", "x.zip") {
		t.Fatalf("got %q", got)
	}
}

func TestOpenSSHSumsIsKeyedByTagNotByServer(t *testing.T) {
	p := Paths{Home: "/h"}
	if got := p.OpenSSHSums("10.5p1-mavericks.2"); got != filepath.Join("/h", "cache", "openssh", "10.5p1-mavericks.2", "SHA256SUMS") {
		t.Fatalf("got %q", got)
	}
}

func TestMediaPaths(t *testing.T) {
	p := Paths{Home: "/h"}
	if p.InstallerMedia() != "/h/build/installer-media.img" || p.MediaWork() != "/h/work/media" {
		t.Fatal(p.InstallerMedia(), p.MediaWork())
	}
}

func TestOpenCoreImageIsUnderBuild(t *testing.T) {
	p := Paths{Home: "/h"}
	if got := p.OpenCoreImage(); got != filepath.Join("/h", "build", "opencore.img") {
		t.Fatalf("got %q", got)
	}
}
