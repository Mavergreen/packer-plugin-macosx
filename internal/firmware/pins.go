// Package firmware builds, from pinned source, what the guest boots before
// its kernel: OpenCore (build_oc.tool), OVMF (EDK II's build) and the
// OpenCore EFI image; its golden tests hold what it builds from and
// ships (testdata/golden/README.md).
//
// firmware owns build/ under its workspace (Builder.Paths), except the
// files config.Paths already names there (firmware/OVMF_*.fd and
// opencore.img). A tree an earlier build left there is built on
// incrementally, not replaced.
package firmware

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Mavergreen/packer-plugin-macosx/internal/pins"
)

// The pinned build. These are what "reproducible" means for the firmware
// (docs/decisions/0004): the same sources, translated by the same
// compiler, in the same build directory, give the same bytes.
const (
	OCVersion     = "1.0.7"
	OCBuildCommit = "e9ed49cb7a4f7fa2830c024a13d63de27c2e0d1a"
	AudkCommit    = "0672a009e9ca85753d240324d761341adf0291b3"

	Arch         = "X64"     // a 64-bit guest; IA32 would double the build for nothing
	EDKToolchain = "GCC"     // CLANGPDB needs clang, which the hosts do not have
	EDKTarget    = "RELEASE" // DEBUG logs on every boot and is slower

	// CStd is the C dialect every firmware file is compiled in. EDK II
	// sets none, and GCC 15 defaults to gnu23, where OpenCorePkg 1.0.7's
	// libDER does not compile (docs/decisions/0004 has the whole story).
	CStd = "gnu17"
	// NoWerror stops upstream's -Werror turning a newer compiler's new
	// warnings into build failures in code we do not own. The warnings
	// are still printed.
	NoWerror = "-Wno-error"

	OVMFDsc = "OvmfPkg/OvmfPkgX64.dsc"

	// EFIImageMiB is the OpenCore EFI image's size; Fits checks the
	// payload against it before anything is written.
	EFIImageMiB = 192
)

// A Submodule is one of audk's git submodules, which a GitHub archive
// tarball leaves out: the registry source that holds it, the commit its
// URL must name, and where it goes under the UDK tree.
type Submodule struct{ Source, Commit, Path string }

// Submodules is every submodule audk's gitlinks name, not just the ones
// compiled: build.py validates every [Includes] path of every .dec it
// parses. audk-brotli appears twice, at one commit.
var Submodules = []Submodule{
	{"audk-openssl", "aea7aaf2abb04789f5868cbabec406ea43aa84bf", "CryptoPkg/Library/OpensslLib/openssl"},
	{"audk-brotli", "e230f474b87134e8c6c85b630084c612057f253e", "BaseTools/Source/C/BrotliCompress/brotli"},
	{"audk-brotli", "e230f474b87134e8c6c85b630084c612057f253e", "MdeModulePkg/Library/BrotliCustomDecompressLib/brotli"},
	{"audk-mbedtls", "8c89224991adff88d53cd380f42a2baa36f91454", "CryptoPkg/Library/MbedTlsLib/mbedtls"},
	{"audk-oniguruma", "4ef89209a239c1aea328cf13c05a2807e5c146d1", "MdeModulePkg/Universal/RegularExpressionDxe/oniguruma"},
	{"audk-libfdt", "cfff805481bdea27f900c32698171286542b8d3c", "MdePkg/Library/BaseFdtLib/libfdt"},
	{"audk-mipisyst", "370b5944c046bab043dd8b133727b2135af7747a", "MdePkg/Library/MipiSysTLib/mipisyst"},
	{"audk-jansson", "e9ebfa7e77a6bee77df44e096b100e7131044059", "RedfishPkg/Library/JsonLib/jansson"},
	{"audk-libspdm", "98ef964e1e9a0c39c7efb67143d3a13a819432e0", "SecurityPkg/DeviceSecurity/SpdmLib/libspdm"},
	{"audk-cmocka", "1cc9cde3448cdd2e000886a26acf1caac2db7cf1", "UnitTestFrameworkPkg/Library/CmockaLib/cmocka"},
	{"audk-googletest", "86add13493e5c881d7e4ba77fb91c1f57752b3a4", "UnitTestFrameworkPkg/Library/GoogleTestLib/googletest"},
	{"audk-subhook", "83d4e1ebef3588fae48b69a7352cc21801cb70bc", "UnitTestFrameworkPkg/Library/SubhookLib/subhook"},
}

// An Artifact is a file build_oc.tool produces and the name it ships
// under. Bootstrap.efi becomes the fallback boot path's BOOTx64.efi.
type Artifact struct{ Built, Ship string }

// Artifacts is what the OpenCore build ships, in the order its
// SHA256SUMS lists them.
var Artifacts = []Artifact{
	{"OpenCore.efi", "OpenCore.efi"},
	{"Bootstrap.efi", "BOOTx64.efi"},
	{"OpenRuntime.efi", "OpenRuntime.efi"},
	{"OpenPartitionDxe.efi", "OpenPartitionDxe.efi"},
	{"OpenHfsPlus.efi", "OpenHfsPlus.efi"},
}

// OVMFFiles is what the OVMF build ships: the pflash pair and the
// combined image, all three from one build.
var OVMFFiles = []string{"OVMF_CODE.fd", "OVMF_VARS.fd", "OVMF.fd"}

// A Kext is a release binary OpenCore injects: its registry source and
// its bundle name (also the name of the Mach-O inside it).
type Kext struct{ Source, Name string }

// Kexts is in load order: VirtualSMC depends on Lilu, and config.plist
// lists them the same way.
var Kexts = []Kext{{"lilu-release", "Lilu"}, {"virtualsmc-release", "VirtualSMC"}}

// EFIDrivers is what goes in EFI/OC/Drivers, in image order.
var EFIDrivers = []string{"OpenRuntime.efi", "OpenPartitionDxe.efi", "OpenHfsPlus.efi"}

// Headers is every C header the firmware build needs that no tool check
// can see: EDK II's BaseTools include uuid/uuid.h. Each is asked of the compiler, which alone knows its
// own search path.
var Headers = []string{"uuid/uuid.h"}

// openCoreTools is every external program the OpenCore build needs
// before it starts; gcc is the Toolchain's GCC() (GCC_BIN-prefixed).
// bash runs build_oc.tool, efibuild.sh and edksetup.sh. git: efibuild.sh
// insists on it, and patches are applied with it. zip: efibuild.sh will
// not start without it. nasm and iasl: it needs both, and where it
// cannot find them (macOS) it offers to fetch and install them with curl
// and sudo.
func openCoreTools(gcc string) []string {
	return []string{"bash", "make", gcc, "git", "python3", "nasm", "iasl", "zip"}
}

// ovmfTools is every external program the OVMF build needs: bash runs
// edksetup.sh, git applies our dsc patches, nasm assembles the reset
// vector and iasl compiles the ACPI tables.
func ovmfTools(gcc string) []string {
	return []string{"bash", "make", gcc, "git", "python3", "nasm", "iasl"}
}

// Tools is every external program the firmware builds run, for doctor:
// the union of each build's own list, in the order they first appear.
func Tools(gcc string) []string {
	var all []string
	for _, t := range append(openCoreTools(gcc), ovmfTools(gcc)...) {
		if !slices.Contains(all, t) {
			all = append(all, t)
		}
	}
	return all
}

// A Pin is one registry source the OpenCore build needs and the commit
// its URL must name.
type Pin struct{ Source, Commit string }

// BuildOptions is the value of OCPKG_BUILD_OPTIONS: the dialect and
// -Wno-error as ONE build macro. The separator is a tab because
// efibuild.sh splits BUILD_ARGUMENTS on spaces and commas; build.py turns
// the tab back into a space in the generated makefiles.
func BuildOptions() string { return "-std=" + CStd + "\t" + NoWerror }

// OpenCorePins is every pinned EDK II input, each source once, in a fixed
// order (testdata/golden/opencore-pins.txt).
func OpenCorePins() []Pin {
	ps := []Pin{{"ocbuild-efibuild", OCBuildCommit}, {"audk-src", AudkCommit}}
	seen := map[string]bool{}
	for _, s := range Submodules {
		if !seen[s.Source] {
			seen[s.Source] = true
			ps = append(ps, Pin{s.Source, s.Commit})
		}
	}
	return ps
}

// OpenCoreSources is every registry source the OpenCore build reads.
func OpenCoreSources() []string {
	var n []string
	for _, p := range OpenCorePins() {
		n = append(n, p.Source)
	}
	return append(n, "opencorepkg-src")
}

// KextSources is every registry source the EFI image's kexts come from.
func KextSources() []string {
	var n []string
	for _, k := range Kexts {
		n = append(n, k.Source)
	}
	return n
}

// SourceNames is every registry source the firmware needs.
func SourceNames() []string { return append(OpenCoreSources(), KextSources()...) }

// ShipNames is Artifacts' shipped names, in SHA256SUMS order.
func ShipNames() []string {
	var n []string
	for _, a := range Artifacts {
		n = append(n, a.Ship)
	}
	return n
}

// CheckPins checks every source the firmware reads: the registry has it
// pinned to a real checksum (Lookup refuses TOFU), and, for a source
// pinned to a commit, its URL names that commit -- the registry holds the
// URLs and this file holds the commits, and the two must not drift.
func CheckPins(reg *pins.Registry) error {
	for _, n := range SourceNames() {
		if _, err := reg.Lookup(n); err != nil {
			return err
		}
	}
	for _, p := range OpenCorePins() {
		s, _ := reg.Lookup(p.Source)
		if !strings.Contains(s.URL, p.Commit) {
			return fmt.Errorf("%s in the registry does not name commit %s: %s", p.Source, p.Commit, s.URL)
		}
	}
	return nil
}
