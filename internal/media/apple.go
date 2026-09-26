package media

import (
	"fmt"
	"io/fs"
	"sort"
	"strings"

	vmguest "github.com/Mavergreen/packer-plugin-mavericks"
)

// RequiredFiles is what an install cannot proceed without: the
// bootloader, the installer's disk image and its chunklist, and the
// sixteen files of Packages, in a fixed order.
func RequiredFiles() []string {
	f := []string{
		"System/Library/CoreServices/boot.efi",
		"System/Installation/BaseSystem.dmg",
		"System/Installation/BaseSystem.chunklist",
	}
	for _, p := range []string{"OSInstall.mpkg", "OSInstall.pkg", "OSUpgrade.pkg", "AdditionalEssentials.pkg",
		"AdditionalSpeechVoices.pkg", "AsianLanguagesSupport.pkg", "BaseSystemBinaries.pkg",
		"BaseSystemResources.pkg", "BSD.pkg", "Essentials.pkg", "InstallableMachines.plist",
		"JavaEssentials.pkg", "JavaTools.pkg", "MediaFiles.pkg", "OxfordDictionaries.pkg", "X11redirect.pkg"} {
		f = append(f, "System/Installation/Packages/"+p)
	}
	return f
}

// parseSums reads "<sha256>  <name>" lines, with a leading ./ removed
// from each name, skipping lines of fewer than two fields and, when
// comments is set, lines beginning with #: check_apple_sums's awk skips
// comments in the pinned file only.
func parseSums(b []byte, comments bool) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if comments && strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		m[strings.TrimPrefix(f[1], "./")] = f[0]
	}
	return m
}

// CheckAppleSums holds checksums computed elsewhere -- in the microVM,
// which is what reads the media, since the host mounts nothing --
// against what Apple shipped, a constant (assets/pins/apple-packages.sha256),
// never against the source: a bad byte out of dmg2img would be copied
// faithfully and then verified as correct. It returns one line per
// package that is missing or wrong, sorted, and an error when there are
// any.
func CheckAppleSums(sums []byte) ([]string, error) {
	pinned, err := fs.ReadFile(vmguest.Files, "assets/pins/apple-packages.sha256")
	if err != nil {
		return nil, err
	}
	want, got := parseSums(pinned, true), parseSums(sums, false)
	var problems []string
	for name, w := range want {
		g, ok := got[name]
		switch {
		case !ok:
			problems = append(problems, name+": MISSING -- the media does not have it")
		case g != w:
			problems = append(problems, fmt.Sprintf("%s: FAILED -- %s is not what Apple shipped (%s)", name, g, w))
		}
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		return problems, fmt.Errorf("%d of Apple's %d packages are not what Apple shipped", len(problems), len(want))
	}
	return nil, nil
}
