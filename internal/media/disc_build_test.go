package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Mavergreen/packer-plugin-macosx/internal/disc"
	"github.com/Mavergreen/packer-plugin-macosx/internal/privops"
)

func TestAutoinstallFilesForSnowLeopard(t *testing.T) {
	got := map[string]string{}
	for _, f := range AutoinstallFilesFor("snowleopard") {
		got[f.Source] = f.Dest
	}
	want := map[string]string{
		"autoinstall.sh":       "private/etc/rc.cdrom.local",
		"minstallconfig.xml":   "private/etc/minstallconfig.xml",
		"OSInstall.collection": "System/Installation/Packages/OSInstall.collection",
	}
	for s, d := range want {
		if got[s] != d {
			t.Errorf("%s goes to %q; want %q (10A432's /etc/rc.install reads /etc/minstallconfig.xml)", s, got[s], d)
		}
	}
}

func TestMavericksAutoinstallFilesUnchanged(t *testing.T) {
	for _, rel := range []string{"", "mavericks"} {
		if !slices.Equal(AutoinstallFilesFor(rel), AutoinstallFiles) {
			t.Fatalf("release %q: %v; want AutoinstallFiles", rel, AutoinstallFilesFor(rel))
		}
	}
}

func TestASnowLeopardTarCarriesMinstallconfigInEtc(t *testing.T) {
	var b bytes.Buffer
	if err := (Injectables{Autoinstall: true, Release: "snowleopard"}).WriteTar(&b, nil); err != nil {
		t.Fatal(err)
	}
	got := readTar(t, b.Bytes())
	if _, ok := got["private/etc/minstallconfig.xml"]; !ok {
		t.Fatalf("no private/etc/minstallconfig.xml in %v", got)
	}
	if _, ok := got["System/Installation/Packages/Extras/minstallconfig.xml"]; ok {
		t.Fatal("10.6's media carries 10.9's minstallconfig path too")
	}
}

// discVolume is a bare HFS+ volume whose header says usedMiB of it are
// in use: 4096-byte blocks, total and free at their offsets.
func discVolume(t *testing.T, totalMiB, usedMiB int) string {
	t.Helper()
	const bs = 4096
	h := make([]byte, 1024+64)
	copy(h[1024:], "H+")
	binary.BigEndian.PutUint32(h[1024+40:], bs)
	binary.BigEndian.PutUint32(h[1024+44:], uint32(totalMiB<<20/bs))
	binary.BigEndian.PutUint32(h[1024+48:], uint32((totalMiB-usedMiB)<<20/bs))
	p := filepath.Join(t.TempDir(), "snowleopard.hfs")
	if err := os.WriteFile(p, h, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// discVM answers the disc build's passes as the real guest would: the
// media's packages are the known disc's, with bad's sum wrong.
type discVM struct {
	t     *testing.T
	calls []vmCall
	bad   string
}

func (v *discVM) Missing() []string { return nil }

func (v *discVM) Run(_ context.Context, target string, payload []byte, disks []privops.Disk) ([]byte, error) {
	name := ""
	for _, n := range []string{"disc/assemble-disc", "fix-ownership", "verify-packages"} {
		if bytes.Equal(payload, embedded(v.t, "assets/privops/"+n+".sh")) {
			name = n
		}
	}
	if name == "" {
		return nil, errors.New("an unknown payload")
	}
	v.calls = append(v.calls, vmCall{name: name, target: target, disks: append([]privops.Disk(nil), disks...)})
	var con strings.Builder
	if name == "verify-packages" {
		known, err := disc.KnownDiscs()
		if err != nil {
			return nil, err
		}
		for n, s := range known[0].Sums {
			if n == v.bad {
				s = strings.Repeat("0", 64)
			}
			con.WriteString("MQG-SUM-MEDIA " + s + "  " + n + "\r\n")
		}
		con.WriteString("MQG-SUM-MEDIA " + strings.Repeat("1", 64) + "  " + FirstbootPkgName + "\r\n")
	}
	con.WriteString("MQG-PRIVOPS-OK rc=0\r\n")
	return []byte(con.String()), nil
}

func discRig(t *testing.T) (*rig, *discVM) {
	t.Helper()
	g := newRig(t)
	vm := &discVM{t: t}
	g.b.VM = vm
	return g, vm
}

func TestBuildFromVolumeAttachesTheVolumeReadOnly(t *testing.T) {
	g, vm := discRig(t)
	vol := discVolume(t, 8, 2)
	if _, err := g.b.BuildFromVolume(context.Background(), vol, "snowleopard", Options{Force: true}); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range vm.calls {
		names = append(names, c.name)
		for _, d := range c.disks {
			if d.Path == vol && d.Role != "ro" {
				t.Errorf("pass %s attaches the volume as %q", c.name, d.Role)
			}
		}
		if c.target == vol {
			t.Errorf("pass %s writes to the volume", c.name)
		}
	}
	if want := []string{"disc/assemble-disc", "fix-ownership", "verify-packages"}; !slices.Equal(names, want) {
		t.Fatalf("passes %v; want %v", names, want)
	}
}

// TestBuildFromVolumeSizesForTheVolumeAndUpdates: the partition holds
// the volume's used space, the margin a Linux copy needs, and whatever
// ExtraSpaceMiB the updates brought.
func TestBuildFromVolumeSizesForTheVolumeAndUpdates(t *testing.T) {
	g, _ := discRig(t)
	vol := discVolume(t, 64, 3)
	out, err := g.b.BuildFromVolume(context.Background(), vol, "snowleopard", Options{Force: true, ExtraSpaceMiB: 5})
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	want := int64(3+MarginMiB+5) << 20
	if fi.Size() < want || fi.Size() > want+2<<20 {
		t.Fatalf("media is %d bytes; want a partition of %d plus the GPT", fi.Size(), want)
	}
}

func TestBuildFromVolumeRefusesMediaThatIsNotTheDisc(t *testing.T) {
	g, vm := discRig(t)
	vm.bad = "BSD.pkg"
	_, err := g.b.BuildFromVolume(context.Background(), discVolume(t, 8, 2), "snowleopard", Options{Force: true})
	if err == nil || !strings.Contains(err.Error(), "BSD.pkg") {
		t.Fatalf("err = %v; want the damaged package named", err)
	}
}
