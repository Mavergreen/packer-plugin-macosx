package firmware

// Release is one Mac OS X release's boot stack: the config.plist its
// EFI image carries, its default SMBIOS model, the models there is
// evidence for, and the decision record that holds that evidence. The
// OVMF and OpenCore builds are the same for every release.
type Release struct {
	Name          string // as data sources and listings name it: "mavericks"
	Version       string // as a person names it: "10.9"
	ConfigPlist   string // embedded path
	DefaultSMBIOS string
	Models        []SMBIOSModel
	Decision      string // where the models' evidence is recorded
}

// Mavericks is 10.9's boot stack, the plugin's first.
var Mavericks = Release{
	Name:          "mavericks",
	Version:       "10.9",
	ConfigPlist:   "assets/firmware/config.plist",
	DefaultSMBIOS: DefaultSMBIOS,
	Models:        SMBIOSModels,
	Decision:      "docs/decisions/0010",
}

// SnowLeopard is 10.6's: Mavericks' config.plist with RebuildAppleMemoryMap
// on (assets/firmware/README.md) and models from before 10.6.0's August
// 2009 release, since a retail disc has no drivers for later Macs.
var SnowLeopard = Release{
	Name:          "snowleopard",
	Version:       "10.6",
	ConfigPlist:   "assets/firmware/snowleopard/config.plist",
	DefaultSMBIOS: "iMac9,1",
	Models:        SnowLeopardModels,
	Decision:      "docs/decisions/0014",
}

// SnowLeopardModels is 10.6's tested-options table.
var SnowLeopardModels = []SMBIOSModel{
	{"iMac9,1", "VERIFIED", `The default. MEASURED 2026-10-04 on pet-power-plant (i7-8700B, KVM): the 10.6.0 retail installer (10A432) booted with this model under OpenCore 1.0.7 once RebuildAppleMemoryMap was on, installed unattended, rebooted into the installed system, ran the first-boot payload and answered SSH. An early-2009 Core 2 iMac, which a Penryn guest CPU matches, and which shipped before 10.6.0, so the retail disc carries its drivers.`},
	{"Macmini3,1", "NOT-TESTED", `The fallback, if iMac9,1 ever stops working: an early-2009 Core 2 Mac mini, also older than 10.6.0. Nothing has been installed with it.`},
}
