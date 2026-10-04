# `config.plist`: every setting that is not a default, and why

`config.plist` is **our** OpenCore configuration. It is checked in as text so
that every setting is diffable, attributable and arguable.

## Where it came from, and where it deliberately did not

It was derived from **OpenCorePkg 1.0.7's own `Docs/Sample.plist`**, in the
same source tree `mavericks-firmware` builds. Anything not listed below is
that sample's value, unchanged.

It was **not** derived from khronokernel's `config.plist`, the one inside the
reference `EFI-LEGACY.img` from a UTM bundle that booted 10.9 before this
project built its own boot stack. That file targets OpenCore 0.6.6 — five
years and roughly twenty schema revisions stale. Carrying it forward would
have meant carrying settings nobody here can explain, which is the opposite
of building the boot stack from source. The reference config is used as
**evidence** instead: where it made a choice, that choice is cited below and
either adopted with a reason or rejected with a reason.

Validation is `ocvalidate` from the same build. `mavericks-firmware` runs it
on the config it writes onto the EFI image (this file, with the `smbios`
model set) and refuses a config it rejects. By hand, from the firmware
workspace in Packer's cache (`docs/decisions/0003`):

```
<cache>/mavericks/firmware-build/build/OpenCorePkg-1.0.7/Utilities/ocvalidate/ocvalidate assets/firmware/config.plist
```

It announces that it is "only compatible with OpenCore version 1.0.7", which
is exactly why it is the validator used rather than eyeballing the plist.
It reports **no issues**.

The file is serialised by Python's `plistlib` with sorted keys, so it
round-trips byte-identically and a `git diff` shows only what actually
changed. Regenerating by hand and re-sorting will not produce spurious
churn.

## How to read this document

Each entry says what changed, **why**, and what the evidence is. Three kinds
of evidence appear:

- **Observation** — something this project watched happen; the results
  are in `docs/configuration-register.md`.
- **Reference config** — what khronokernel's 0.6.6 config does. Suggestive,
  not binding: it was verified under TCG on Apple Silicon, and one of its
  settings was found wrong for KVM (the next section).
- **Property of 10.9** — a fact about the guest OS, e.g. that it predates a
  technology entirely.

Anything with no entry below is a 1.0.7 sample default we did not touch. If
you are looking for a setting and cannot find it here, that is the answer:
nobody chose it, and changing it is fair game.

---

## The two load-bearing settings

### `PlatformInfo > Generic > SystemProductName` = `iMac14,2`

**Evidence: observation.** The reference config sets `MacPro5,1`. Under KVM
that produces an immediate kernel panic in `AppleTyMCEDriver`, the Xeon
machine-check driver, which loads *because* the SMBIOS says the machine is a
Xeon Mac Pro and then faults writing a machine-check register QEMU does not
provide. See `docs/configuration-register.md`, `docs/host-profile.md` G14, and
`docs/decisions/0010`.

Changing the model to `iMac14,2` attacks what the driver matches on rather
than trying to stop it loading, and the panic disappeared.

Note the honest caveat: `iMac14,2` is a 2013 Haswell iMac and the guest CPU
is advertised as Penryn. That pairing is odd and 10.9 evidently tolerates
it. It is the first model that worked, not a model that was shown to be
best.

### `UEFI > Drivers` — `OpenHfsPlus.efi`, never `HfsPlus.efi`

**Evidence: `docs/decisions/0002-openhfsplus-over-apple-hfsplus.md`.** OVMF
cannot read HFS+, so OpenCore needs an HFS+ driver to see the installed
system at all. The reference image uses `HfsPlusLegacy.efi`, which is
extracted from Apple firmware: no source, no way to rebuild it, Tier 2 by
construction. Removing it is the reason the boot stack is built from
source at all.

`OpenHfsPlus.efi` is built from pinned source by `mavericks-firmware`.
Decision 0002 expected it to be slower and asked for the cost to be
measured: it is 3.3 s on a ~46 s boot (decision 0002, "Measured cost").

Three drivers are loaded, in this order:

| Driver | Why |
|---|---|
| `OpenRuntime.efi` | Provides `OC_FIRMWARE_RUNTIME`. Every `Booter > Quirks` entry below needs it; without it they silently do nothing. |
| `OpenPartitionDxe.efi` | Reads Apple's partition layout (APM, and the Apple Boot partition scheme). In the reference image too. |
| `OpenHfsPlus.efi` | The HFS+ filesystem driver, per above. |

All three are `Enabled`, `LoadEarly: false`, no arguments — the sample's
shape for each. The sample ships fifty driver entries, forty-seven of them
disabled examples; those are removed rather than left disabled, so the list
is the list.

---

## Kernel

### `Kernel > Add` — `Lilu.kext`, then `VirtualSMC.kext`

**Evidence: observation.** SMC emulation is what makes `DSMOS has arrived`
appear; without it 10.9 will not finish booting. That line appears with
OpenCore-injected kexts and no `-device isa-applesmc` anywhere, which is why
this project needs no OSK string at all.

Order is load-bearing: `VirtualSMC.kext`'s `Info.plist` declares
`OSBundleLibraries > as.vit9696.Lilu = 1.2.0`, so Lilu must be injected
first.

`MinKernel` is `8.0.0` and `MaxKernel` is empty, matching the sample's own
entries for these two kexts and also matching what the kexts themselves
declare (see below). `Arch` is `Any`; both binaries are fat with x86_64 and
i386 slices.

**The reference image ships a third SMC kext, `FakeSMC-32.kext`, and we do
not.** `FakeSMC` and `VirtualSMC` are *alternative* SMC emulators from
different projects; shipping both is unusual and at least one is probably
redundant. The rule here is that nothing ships without having seen a boot
fail without it, so we started with two, to add back only on evidence. The
boot works without `FakeSMC-32.kext`: it was redundant
(`docs/configuration-register.md`).

#### Which releases, and whether they still support 10.9

`assets/pins/sources.tsv` pins:

| Kext | Release | Why this version |
|---|---|---|
| `Lilu` | 1.7.2 | The current release as of 2026-09-17. Pinned to the exact tag, never `latest`. |
| `VirtualSMC` | 1.3.7 | The current release as of 2026-09-17, and it requires Lilu ≥ 1.2.0, which 1.7.2 satisfies. |

These are the only things in the assembled EFI image that are not built from
source: **Tier 1**, acidanthera release binaries, pinned by tag and by
SHA-256. We cannot make them Tier 0 without building them, which needs Xcode.

acidanthera has been dropping old-OS support over time, so "current release"
is not by itself a reason to believe these load on 10.9. That was checked
concretely, by reading each `Contents/Info.plist` rather than by assuming:

```
Lilu 1.7.2        OSBundleLibraries_x86_64: com.apple.kpi.* = 10.0.0
VirtualSMC 1.3.7  OSBundleLibraries_x86_64: com.apple.kpi.* = 10.0.0
                                            as.vit9696.Lilu = 1.2.0
```

A `com.apple.kpi.*` version is a Darwin version. `10.0.0` is Darwin 10, which
is Mac OS X 10.6. **10.9 is Darwin 13**, comfortably above that floor, so
both kexts still declare support for it. (The non-architecture-specific
`OSBundleLibraries` in each declares `8.0.0`, Darwin 8 = 10.4, which is where
the `MinKernel: 8.0.0` above comes from.) Neither Mach-O carries an
`LC_VERSION_MIN_MACOSX` or `LC_BUILD_VERSION` load command, so there is no
second, stricter minimum hiding in the binary.

Had either declared a minimum above 13, that would have been a real
constraint needing older pinned releases — a decision, not a workaround.
It did not.

### `Kernel > Block` — empty, deliberately

**Evidence: observation, and an unresolved question.** The reference
config ships a `Kernel > Block` entry for `com.apple.driver.AppleTyMCEDriver`
with `Enabled: false`. Flipped to `true`, **nothing changed** — same panic,
same backtrace. Why it had no effect is still unknown
(`docs/configuration-register.md`, the `Kernel > Block` row).

So we ship no block at all. Carrying forward a setting that was observed to
do nothing would be cargo cult, and it would also confound the evidence that
the SMBIOS change alone is what fixed the panic.

### `Kernel > Emulate > DummyPowerManagement` = `true`

**Evidence: reference config, corroborated by observation.** The reference
sets it, and the verbose boot shows the expected consequence:

```
ACPI_SMC_PlatformPlugin::start - waitForService(AppleIntelCPUPowerManagement) timed out
```

That timeout is harmless and is what this quirk buys: `AppleIntelCPUPower-
Management` cannot drive a virtual CPU, and without the stub it panics
instead of timing out.

### `Kernel > Quirks > PanicNoKextDump` = `true`

**Evidence: observation.** The SMBIOS diagnosis rested entirely on being able
to read a panic backtrace off the screen. Without this quirk a panic is followed
by a dump of every loaded kext, which scrolls the useful part away. The
reference config sets it too.

### `Kernel > Quirks` settings we did **not** copy from the reference

- **`DisableLinkeditJettison`** is on — but it is already 1.0.7's sample
  default, so this is agreement, not a decision.
- **`SetApfsTrimTimeout`** is left at the sample default `-1`. The reference
  sets it. 10.9 predates APFS by four years and cannot mount an APFS volume,
  so the setting has nothing to act on here. This is the clearest example in
  this file of a reference setting that is *copied* rather than *chosen*.

---

## Booter

### `Booter > Quirks > AllowRelocationBlock` = `true`

**Evidence: reference config.** This is the only Booter quirk we had to
change: 1.0.7's sample already enables `AvoidRuntimeDefrag`,
`EnableWriteUnprotector`, `ProvideCustomSlide` and `EnableSafeModeSlide`, the
other four the reference turns on.

`AllowRelocationBlock` lets `boot.efi` be loaded into a relocation block when
the lower memory it wants is occupied. It is specifically a legacy-macOS
accommodation and the reference config, which is the only verified 10.9
configuration we have, enables it.

**None of the five has been re-tested on 1.0.7.** They are inherited from a
0.6.6 configuration on a different hypervisor. If a boot misbehaves in the
booter, change one quirk at a time; none has misbehaved yet.

### Two Booter quirks where we keep 1.0.7's default over the reference's

The reference config leaves `SetupVirtualMap` **off**; 1.0.7's sample has it
**on**, and we keep it on. It corrects `SetVirtualAddresses` handling on
firmware that mishandles it, and it is what OVMF-based setups generally use.
The same applies to `FixupAppleEfiImages`, which did not exist in 0.6.6 at
all.

Both are recorded here because a reader comparing our config to the
reference will notice the difference and deserves to know it was seen. If a
boot fails in the booter, these are the first two things to try flipping —
one at a time.

---

## Misc

### `Misc > Security > ScanPolicy` = `66051` (`0x10203`)

**Evidence: observation.** `OC_SCAN_FILE_SYSTEM_LOCK` (`0x1`) +
`OC_SCAN_DEVICE_LOCK` (`0x2`) + `OC_SCAN_ALLOW_FS_HFS` (`0x200`) +
`OC_SCAN_ALLOW_DEVICE_SATA` (`0x10000`): **HFS+ volumes on SATA devices
only**, which is exactly the macOS disk and the installer media, both
attached `ide-hd` on q35's AHCI.

With `0` (scan everything) and an empty NVRAM, the picker listed the
OpenCore disk itself, made it the default, timed out into it, re-entered
`BOOTx64.efi`, got `EFI_ALREADY_STARTED` and hung, so every boot needed a
keypress. The OpenCore image is FAT on `usb-storage`, excluded twice over,
so the picker has the one entry that should boot, and the timeout
boots it. That is declarative about what is meant to boot, where hiding
the entry or remembering a choice would patch a symptom; an unexpected
volume cannot become the default either.

`0x10202` is refused by OpenCore (`Invalid ScanPolicy`): a filesystem bit
needs the filesystem lock bit. `ocvalidate` catches that in a millisecond,
which is why it runs before anything boots.

The installer media must be attached as `ide-hd`, not `ide-cd`: as a CD,
OpenCore classifies it ATAPI, which this policy excludes ("OCB: System has
no boot entries").

### `Misc > Security > SecureBootModel` = `Disabled`

**Property of 10.9.** Apple Secure Boot arrived with the T2, in 2018. 10.9 is
from 2013 and has no notion of it; leaving the sample's `Default` would ask
OpenCore to enforce a policy the OS cannot participate in.

### `Misc > Security > Vault` = `Optional`

The sample ships `Secure`, which requires a signed `vault.plist` and
`vault.sig` alongside the config. We do not produce those, and with `Secure`
set OpenCore refuses to boot at all. `Optional` is the honest value for a
configuration that is not vaulted. Note what this means: the config on the
EFI image is not tamper-evident. It is reproducible instead — rebuild the
image from this repo and compare.

### `Misc > Debug > DisableWatchDog` = `true`

**Evidence: `docs/decisions/0002`.** `OpenHfsPlus.efi` was expected to be
slower than Apple's driver by an unmeasured amount, and the firmware
watchdog reboots the machine if `boot.efi` takes too long, which would have
turned "slow" into "reboots forever". The cost was then measured at 3.3 s,
nowhere near a watchdog timeout, so this setting has outlived its reason;
turning the watchdog back on is a one-boot experiment nobody has run
(`docs/configuration-register.md` §7).

### `Misc > Debug > Target` = `67`

**Evidence: observation.** `Target = 67` writes OpenCore's log to a file on
the EFI partition as well as the console. During firmware bring-up it gave
the only direct answer (`EFI_ALREADY_STARTED`) where everything else gave
hypotheses. A RELEASE build logs only warnings and errors, so a clean boot
writes an empty log. The OpenCore image is attached `snapshot=on`, so the
log file lasts only as long as the VM runs.

### The debug switch: `AppleDebug`, `DisplayLevel`, `debug=0x100`

Not a change: this file carries the sample's `AppleDebug` = `false`,
`DisplayLevel` = `2147483650` and no `debug=0x100` in `boot-args`. A build
with `debug = true` (the template's variable, `mavericks-firmware`'s
`debug`) ships a copy with exactly these three values changed to what
firmware bring-up ran with -- `AppleDebug` = `true`, `DisplayLevel` =
`2147483714` (adding `DEBUG_INFO`), `boot-args` = `-v keepsyms=1
debug=0x100` -- and nothing else (`internal/firmware/debug.go`). Why each
is a debugging aid is in `docs/configuration-register.md` §7.

### `Misc > Boot > HideAuxiliary` = `false`

The sample hides auxiliary picker entries behind a keystroke. During
bring-up, an entry hidden behind a keystroke is indistinguishable from an
entry that OpenCore never found — and "picker empty" is one of the failure
modes a boot has to be diagnosable from. Show everything.

### `Misc > Security > ExposeSensitiveData` = `6` (sample default, kept on purpose)

Not a change, but worth knowing it is load-bearing: bit `0x2` is what makes
OpenCore write its version into NVRAM, which is how a boot confirms it ran
*our* 1.0.7 build and not some other OpenCore image. Turning this off would
remove the only direct evidence of which bootloader ran.

### `Misc > Tools` — empty

The reference image ships `OpenShell.efi`. The OpenCore build ships five
artifacts (`firmware.Artifacts`) and that is not one of them, so listing it
would name a file that is not there. The `::/EFI/OC/Tools` directory is created
empty for the day we want one.

---

## NVRAM

The sample's NVRAM section is an assortment of example values for a real Mac.
It is replaced rather than edited, keeping three variables under Apple's
boot GUID `7C436110-AB2A-4BBB-A880-FE41995C9F82`:

| Variable | Value | Why |
|---|---|---|
| `boot-args` | `-v keepsyms=1` | The sample's value. Verbose boot: every boot diagnosis so far came from reading the boot text, and `keepsyms=1` is what makes a panic backtrace show symbol names instead of raw addresses. The debug switch adds `debug=0x100` (above). |
| `prev-lang:kbd` | `en-US:0` | Stops the first-boot language picker. The sample's value is `ru-RU:252`. |
| `run-efi-updater` | `No` | Stops Apple's EFI firmware updater from trying to flash firmware that does not exist. |

`NVRAM > Delete` is empty. The split-pflash OVMF gives EFI variables
persistence, but every build starts from its own fresh copy of the variable
store, and the box attaches its copy `snapshot=on`, so no variable outlives
a run for a stale `boot-args` to come from.

The sample's other variables are left out, each for a reason:

- **`csr-active-config`** — System Integrity Protection arrived in 10.11.
  10.9 has no SIP to configure. Copied-not-chosen if left in.
- **`SystemAudioVolume`, `ForceDisplayRotationInEFI`,
  `DefaultBackgroundColor`** — cosmetics for a physical Mac.
- **`rtc-blacklist`** — for working around specific real-hardware RTC bugs.
- **`NVRAM > LegacySchema`** — the whole block. It governs NVRAM emulation
  for firmware with no working variable store, which requires a legacy NVRAM
  driver we do not load.

---

## PlatformInfo

`UpdateSMBIOSMode` is `Create`, which is both the sample default and what the
reference config uses: OpenCore builds new SMBIOS tables rather than
overwriting the firmware's, which is the mode that works when the firmware's
own tables are not Apple-shaped.

`Automatic` is `true`, so the `Generic` section is what takes effect.

**The identity fields are left at the sample's obvious placeholders** —
`SystemSerialNumber` `W00000000001`, `SystemUUID` all zeroes, `MLB`
`M0000000000000001`, `ROM` `112233445566`. This is deliberate, not an
oversight. Generating a plausible serial number is what you do when you want
a VM to pass for a real Mac to Apple's servers; we want the opposite, and
nothing in this project logs into anything. A reader who sees a real-looking
serial number in a future diff should ask why it changed.

---

## UEFI

### `UEFI > APFS > EnableJumpstart` = `false`

**Property of 10.9.** APFS jumpstart loads an APFS driver out of an APFS
container so the firmware can see APFS volumes. 10.9 cannot mount APFS at
all; there is nothing for it to find. The sample defaults it on because
almost everyone running OpenCore is on a much newer macOS.

### Everything else under `UEFI`

`Output > Resolution` is `1280x800@32`. The resolution macOS gets is the
framebuffer OpenCore sets, and the guest cannot change it at runtime:
**evidence: observation**, the sample's `Max` came up at 4096x2160 with no
display driver at all, and `1024x768` gave 1024x768. 1280x800 is reasoned,
not measured: a guest with no graphics acceleration draws every pixel on
the CPU.

The rest are sample defaults, including `Input > KeySupport: true` (the
guest's keyboard is USB through the firmware, `usb-kbd` in the template's
machine), `ConnectDrivers: true`, and `Quirks > RequestBootVarRouting:
true` (which pairs with `OpenRuntime.efi`). `ReservedMemory` and `Unload`
are emptied of the sample's examples.

## `snowleopard/config.plist`: Mac OS X 10.6

`snowleopard-firmware` ships `snowleopard/config.plist`, which is this
file with exactly three differences, held there by
`internal/firmware`'s `TestSnowLeopardConfigDiffersOnlyAsDocumented`:

- **`Booter > Quirks > RebuildAppleMemoryMap` = `true`.** MEASURED
  2026-10-04 on `pet-power-plant` (KVM): without it the 10.6.0 retail
  installer's 64-bit kernel stops for good after `mig_table_max_displ =
  73`, with one CPU or two, and with `idlehalt=0`; with it, the installer
  starts. It came from `jprx/how-to-install-snow-leopard-in-qemu`
  (`docs/prior-art.md`).
- **`PlatformInfo > Generic > SystemProductName` = `iMac9,1`**, a Mac
  older than 10.6.0, whose retail disc therefore has its drivers
  (`internal/firmware`'s `SnowLeopardModels`).
- **The description** names the OS.

**`Booter > Quirks > DevirtualiseMmio` stays `false`**, though the same
guide sets it. MEASURED 2026-10-04: with it on, the install ran to its
end and then the installer's `bless` panicked in `AppleEFIRuntime`
writing NVRAM, leaving a disk OpenCore found no boot entry on; with it
off, `bless` succeeds and the installer reboots into 10.6. The likely
reason, not measured: the quirk takes the runtime mapping away from MMIO
regions, OVMF's NVRAM flash among them.

---

## What is still unverified

This config boots: full unattended installs on three hosts, each then
booting without installer media and answering SSH
(`docs/configuration-register.md`). What that does not establish, written
down so nobody mistakes a working boot for a tested setting:

1. **The five Booter quirks are inherited from 0.6.6 under TCG**, not
   re-tested on 1.0.7 under KVM. No boot has misbehaved, so no quirk has
   been flipped.
2. **Whether each driver is needed.** Nobody has removed one to see what
   breaks; `OpenPartitionDxe` is the likeliest to be redundant.
3. **Why `Kernel > Block` did nothing** (above).
