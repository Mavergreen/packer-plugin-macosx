# The configuration register

**Every setting this project makes, why it has its value, how we know it
is doing its job, and what would have to be true to change it.**

`docs/host-profile.md` §4 is the **generalization ledger**: what is
specific to a host, and what another host would have to do to falsify it.
This register is the other axis. It asks of every setting, portable or
not:

1. **What it is set to**, and where (a template variable, the template's
   `qemuargs`, `config.plist`, the firmware build, the guest's first boot).
2. **Why, and on whose word.** Every row carries one of three words:
   - **MEASURED**: someone here ran the experiment, and the row says when
     and what came out.
   - **INHERITED**: it came from prior art, a bundle or a guide, named
     and dated. Four inherited claims were proven wrong here (the
     `usb-tablet` kext, "DNS needs configuring", Security Update
     2016-001, and `kvm.ignore_msrs=1`) and one right (no virtio-net in
     stock 10.9).
   - **REASONED**: derived from a property of the guest, the host or a
     specification, and never run. *A reasoned cause wearing the clothes
     of a measurement is the expensive kind of wrong*, which is why the
     word exists.
3. **How we know it is doing its job**: a test, a measurement, a ledger
   entry, or **nothing**, which is an honest and common answer.
4. **What would change it**: the falsifier, written before anyone tries.

Most rows carry more than one word (inherited, and since measured), and
the word in each row is the authority. Roughly half of what is set has
never been tested; §12 ranks the half that matters. The three most
load-bearing settings, the `-cpu` line, the SMBIOS model and the NIC, are
all measured, each with an ADR.

The reference configuration several rows inherit from is the **UTM
bundle** that booted 10.9 under TCG on Apple Silicon before this project
built its own boot stack: Adam Kostarelas's
`Mavericks-OSX-10.9-Config.utm.zip` (pinned as `utm-bundle`, inspected
2026-09-17), carrying khronokernel's OpenCore 0.6.6 image. Its settings:
`q35` with `vmport=off`; `Penryn` with exactly `ssse3`, `sse4.1` and
`sse4.2`; 8 CPUs and 8192 MB; the OpenCore image on USB and the target on
IDE; `usb-net`; `SystemProductName` `MacPro5,1`; `FakeSMC-32`, `Lilu` and
`VirtualSMC`; and no `-device isa-applesmc` at all.

---

## 1. Host state

| Knob | Value | Why, and on whose word | How we know | What would change it |
|---|---|---|---|---|
| `kvm.ignore_msrs` | **not needed; do not set it** | **INHERITED and misread.** Guides cite "Somlo and OSX-KVM". Read on 2026-09-21 (`docs/prior-art.md`), Somlo's claim was about MSR `0x199` on **Yosemite 10.10**, on **kernels older than 4.7**, fixed upstream in Linux 4.7 (2016-07-24). OSX-KVM has never given a reason. | **MEASURED**: guests installed and answered SSH on `squirrel-zapper` and `ap-juicer`, both with `ignore_msrs=N` (G21). On the primary host, where it was set, `report_ignored_msrs=Y` logged every MSR it swallowed for a macOS guest across 2.5 days: `0xe7 0xe8 0x300 0x3f8 0x3f9 0x3fa 0x60d 0x61d 0x621 0x690 0x6b0`, all power and energy telemetry, read after the system is up. `0x199` never appears; nothing in the machine-check range does. | A named MSR in `dmesg` with `report_ignored_msrs=1`, and a guest that misbehaves without it. |
| `kvm.report_ignored_msrs` | `Y`, the kernel's default | Nobody chose it. | It is the only reason the row above could be answered with data. | Leave it on. Upstream KVM (Bonzini, 2024-12-19) calls `ignore_msrs=1` with `report_ignored_msrs=0` "not a supported configuration". |

---

## 2. Accelerator, machine and CPU

| Knob | Value | Why, and on whose word | How we know | What would change it |
|---|---|---|---|---|
| `accelerator` | `kvm` | **REASONED**: the host is x86 with VT-x (G2). The variable also accepts tcg, hvf, whpx, xen, hax, nvmm and none. | Every build. | Only `kvm` has built a guest. Another value is a value plus a measurement (`docs/decisions/0005`). |
| machine | `q35,vmport=off` | **INHERITED** from the UTM bundle, and from Somlo's page (`-machine q35` from his 2014-05-16 revision). | **MEASURED, sideways**: `ide-hd` on q35's `ide.N` presents to the guest as SATA/AHCI (G16, confirmed on three QEMUs); on `pc` it would be legacy IDE. | `pc` has never been booted. It would need a full install. Low cost of being wrong. |
| `vmport=off` | off | **INHERITED** from the bundle. | **Nothing.** Never tested with it on. | It hides VMware's backdoor I/O port, and 10.9 has no VMware tools, so REASONED: no difference either way. One boot would tell; nobody has run it. |
| `cpu` | `Penryn,+ssse3,+sse4.1,+sse4.2` | **INHERITED** from the bundle; **since MEASURED** (`docs/decisions/0009`). Not from Somlo, whose `Penryn` is dated on his page "as of Sierra" (10.12). | **MEASURED**: complete installs on two hosts and two QEMUs. `+ssse3` and `+sse4.1` are redundant with `Penryn`; `+sse4.2` is not; **10.9 does not need SSE4.1**: `Conroe` installed on `ap-juicer` on 2026-09-21. | A second `Conroe` install on another host. See G3, G25. |
| `cpus` | `2` | **INHERITED** from the original bring-up brief. Its stated reason, "Somlo reports 10.9's first boot after install fails without SMP", has no source: the archaeology found no such claim on his page in any revision (he runs `-smp 4,cores=2`, from 2017, for Sierra). | **MEASURED 2026-09-21**: an installed guest boots at **1** CPU, answers SSH (in 40 s against 20 s at 2) and computes the right SHA-256 over 64 MiB. | **A full install at `cpus = 1`**, about 20 minutes. The first-boot-after-install claim is untested either way. |
| `memory` | `4096` | **INHERITED** from the bring-up brief; corroborated by Kostarelas's observation (8 GB assigned, ~2.9 GB used at idle), not a measurement. | **MEASURED 2026-09-21**: an installed guest boots and passes the same checks at **2048** and at **1024** MB (SSH at 40 s). | **An install at less.** The installer unpacks into a ramdisk, so the install is where memory is used. The cheapest untested row with the most leverage for a small CI runner. |
| `disk_size` | `60G` | **REASONED**: a round number that holds a ~9–11 GB install with room. | Every guest is 8.9–10.7 GB of qcow2, and qcow2 is sparse, so the headroom costs nothing on disk. | A filesystem without sparse files, or a budget where the virtual size matters. 10.9's installer choosing its target does not depend on the size beyond the 16 GiB floor in `autoinstall.sh`. |

---

## 3. SMBIOS

| Knob | Value | Why, and on whose word | How we know | What would change it |
|---|---|---|---|---|
| `smbios` (`SystemProductName`) | `iMac14,2` | **MEASURED, in the weakest useful sense**: the bundle's `MacPro5,1` panicked, and this was the first value that worked. Not shown to be best. | **MEASURED**: complete installs on three hosts, each then booting without installer media and answering SSH; the installed guest reports `hw.model=iMac14,2`. | Nothing needs to. `MacPro5,1` is unusable **under KVM on any host** and usable under TCG, and the cause is measured: `docs/decisions/0010`. |
| identity fields | `SystemSerialNumber` `W00000000001`, `SystemUUID` all zeros, `MLB` `M0000000000000001`, `ROM` placeholder: the sample's own | **REASONED, deliberately.** A plausible serial is what you mint to pass a VM off as a Mac to Apple's servers; this guest talks to none. | Three installs, so nothing in 10.9's boot consults them. | Something in the guest needing a well-formed serial. Nothing does. |
| `PlatformInfo > Automatic` | `true` | **REASONED**: OpenCore derives the board id and firmware features from the product name. | The derivation is the measurement: `MacPro5,1` printed board id `Mac-F221BEC8`, which nobody typed. | Nothing. It is why `smbios` can change one field alone. |

---

## 4. Firmware and boot stack

| Knob | Value | Why, and on whose word | How we know | What would change it |
|---|---|---|---|---|
| firmware | this project's own OVMF, from the pinned `acidanthera/audk` tree | **MEASURED 2026-09-17**: Debian's stock OVMF 2024.02 renders its own boot manager but, with OpenCore, never boots macOS; it fails identically with khronokernel's reference OpenCore 0.6.6, so it is not this build's doing. Our own OVMF worked on the first boot. | Every build since. | It is Tier 0 by decision (`docs/decisions/0004`); a stock firmware that worked would not change that. |
| firmware wiring | split `pflash`: CODE read-only, VARS a writable per-build copy (`efivars.fd`) | **MEASURED**: CODE 3,653,632 + VARS 540,672 bytes = exactly 4 MiB, which QEMU needs for a pflash pair. The template never boots the cached VARS in place. | **MEASURED 2026-09-17**: EFI variables persist across power cycles, from 0 variables to 24, including ones macOS wrote. The box attaches its VARS with `snapshot=on`, so a box keeps no variables between runs. | Nothing. `-bios` loses variable persistence. |
| HFS+ driver | `OpenHfsPlus.efi`, never Apple's | **REASONED** from provenance (`docs/decisions/0002`). | **MEASURED**: it costs 3.3 s at boot, and Apple's driver does not load on this firmware at all. | Nothing: the alternative does not work here. |
| drivers | `OpenRuntime`, `OpenPartitionDxe`, `OpenHfsPlus` | **REASONED** per driver (`assets/firmware/README.md`). The sample's other 47 disabled entries are removed, so the list is the list. | Every boot. Nobody has removed one to see what breaks. | One boot each. `OpenPartitionDxe` is the likeliest to be redundant on a GPT-only layout. |
| `Kernel > Add` | `Lilu.kext` 1.7.2, then `VirtualSMC.kext` 1.3.7 | **MEASURED** (order: `VirtualSMC` declares a dependency on Lilu ≥ 1.2.0) and **REASONED** (necessity). | **MEASURED**: `DSMOS has arrived` with OpenCore-injected kexts and **no `isa-applesmc`**, so this project needs no OSK string. | The kexts dropping 10.9. Checked by reading their `Info.plist`s: both declare `com.apple.kpi.* = 10.0.0` (Darwin 10); 10.9 is Darwin 13. |
| `FakeSMC-32.kext` | **not shipped**, though the bundle had it | **MEASURED 2026-09-17**: the guest boots without it. | The boot that works. | Nothing. |
| `Kernel > Block` | **empty** | **MEASURED, negatively**: the bundle's disabled block for `AppleTyMCEDriver`, enabled, changed nothing. | The panic that did not go away. | The community's remedy for that symptom is a personality-override kext, not a bundle-id block (`docs/decisions/0010`). Untried. |

---

## 5. Devices

| Knob | Value | Why, and on whose word | How we know | What would change it |
|---|---|---|---|---|
| USB controllers | `ich9-usb-ehci1` and three `ich9-usb-uhci` companions at `0x1d.*` | **MEASURED 2026-09-17**: 10.9's `AppleUSBXHCI` cannot drive QEMU's XHCI at all; keyboard and mouse were both dead on it, while OVMF drove it fine. All three companions are needed: EHCI speaks only high speed. | **CONFIRMED** under QEMU 8.2.2, 11.0.2 and 11.1.1 (G13). Portable. | A QEMU whose XHCI 10.9 can drive. |
| input | `usb-kbd` and `usb-mouse` on the EHCI bus | **MEASURED**: both work on EHCI. `usb-tablet` was once believed not to work on 10.9 (INHERITED from a 2016 kext README); MEASURED 2026-09-17, `usb-tablet` on EHCI tracks the host pointer on a guest that never had the kext, because the kext's own author fixed QEMU in 2017 (`docs/prior-art.md`). | The installs that work. | `usb-tablet` would give absolute pointing without a grab. It has not been tried in the template; one boot would tell. |
| `nic` | `e1000-82545em` | **MEASURED 2026-09-21** (`docs/decisions/0008`): 174 MB/s down and 23.3 MB/s up, against `usb-net`'s 1.24 and 1.27 at `10baseT`. **140x.** `virtio-net-pci` gets no interface in stock 10.9. | The guest names the link speed itself, so the ranking should be portable; the ceiling is this host's slirp. | A QEMU that cannot offer the device. **A NIC is build-time state**: a guest boots only with the NIC it was installed with. |
| network | user-mode (slirp), `hostfwd` on `127.0.0.1` only | **REASONED**: no root and no host bridge. | Every SSH-answering guest. "DNS needs pointing at 1.1.1.1" (INHERITED) was **refuted**: DNS worked untouched. `ping` from the guest fails on the primary host because `net.ipv4.ping_group_range` is empty there, a host artifact, not a guest fault. | Throughput: a bridge or `passt` would raise the ceiling, and needs privileges this project does not ask for. |
| display | `VGA,vgamem_mb=64` | **INHERITED** from the bundle's shape, and Kostarelas's observation that ~3 MB of VRAM made video a slideshow. | **MEASURED, partly**: with 64 MB, 10.9 still offered one resolution; the resolution is a firmware setting (§7), not a VRAM one. Nothing has measured what `vgamem_mb` buys. | Nobody has tried the default 16. |
| `headless` | `true` | **REASONED**: an unattended build needs no window. | Every build. The box boots headless too, and `MAVERICKS_DISPLAY` asks it for a window. | A reason to watch the install. |

---

## 6. Storage attachment

| Knob | Value | Why, and on whose word | How we know | What would change it |
|---|---|---|---|---|
| target disk | `ide-hd` on `ide.0` | **INHERITED** shape, **MEASURED** effect. | The guest reports connection bus SATA (G16), on three QEMUs. | A machine type other than q35. |
| OpenCore image | `usb-storage` on the EHCI bus, `snapshot=on` | **MEASURED**: stock OVMF never enumerated it over USB; our OVMF does, as the reference firmware did. | Every boot. | Nothing. |
| installer media | `ide-hd` on `ide.1`, **not** `ide-cd`, `snapshot=on` | **MEASURED**: the media is a GPT disk image; attached as a CD, OpenCore classified it ATAPI, which the `ScanPolicy` (§7) correctly excludes: "OCB: System has no boot entries". | The installs. | Nothing. |
| `snapshot=on` | on the OpenCore image and the installer media | **MEASURED 2026-09-18**: without it every boot rewrote the OpenCore image (four boots, four checksums), and macOS wrote a `.Spotlight-V100` store onto the media. | `docs/decisions/0006`; G20. | Nothing. An input a run modifies is not an input. |

---

## 7. OpenCore `config.plist`

**Every divergence from OpenCorePkg 1.0.7's `Docs/Sample.plist` is listed,
with its evidence, in `assets/firmware/README.md`.** This section adds
columns 3 and 4 where the answer is interesting.

| Setting | Value | How we know | What would change it |
|---|---|---|---|
| `Misc > Security > ScanPolicy` | `0x10203` (66051): file-system lock, device lock, HFS, SATA | **MEASURED 2026-09-17**: with `0` (scan everything) and an empty NVRAM, the picker listed the OpenCore disk itself, made it the default, timed out into it, re-entered `BOOTx64.efi`, got `EFI_ALREADY_STARTED` and hung, so every boot needed a keypress. With `0x10203` the picker has one entry, the macOS disk, and the timeout boots it. `0x10202` is refused by OpenCore (`Invalid ScanPolicy`): a filesystem bit needs the filesystem lock bit, which `ocvalidate` catches in a millisecond. | It is declarative about what is meant to boot: an unexpected volume cannot become the default. A layout that boots from something other than HFS+ on SATA. |
| `Misc > Boot > Timeout` | `5` | The unattended installs: with one entry, the timeout boots it. | Nothing needs it shorter. |
| `Misc > Security > SecureBootModel` | `Disabled` | **REASONED** from 10.9: Apple Secure Boot arrived with the T2 in 2018. | Nothing. Cost of being wrong: zero. |
| `Misc > Security > Vault` | `Optional` | **MEASURED, negatively**: with the sample's `Secure`, OpenCore refuses to boot without a `vault.plist` and `vault.sig`, which this build does not produce. | Producing a vault. The config is reproducible, not tamper-evident. |
| `Misc > Security > ExposeSensitiveData` | `6`, the sample's, **kept on purpose** | Bit `0x2` writes OpenCore's version into NVRAM: the only direct evidence of *which* bootloader ran. | Do not turn it off. |
| `Misc > Debug > Target` | `67`: log to a file on the EFI partition | **MEASURED 2026-09-17**: the file log was the one instrument that gave a direct answer during firmware bring-up (`EFI_ALREADY_STARTED`). A RELEASE build logs only warnings and errors, so a clean boot writes an empty log. | REASONED: with the OpenCore image attached `snapshot=on`, the log lasts only as long as the VM runs. |
| `Misc > Debug > DisableWatchDog` | `true` | **REASONED** from `docs/decisions/0002`: the watchdog would turn a slow `OpenHfsPlus` into reboot loops. The slowness was then **measured at 3.3 s**, nowhere near a watchdog timeout. | **A bring-up accommodation that has outlived its reason.** Turning it back on is a one-boot experiment nobody has run; off, a hung boot hangs instead of rebooting. |
| `Misc > Debug > AppleDebug` | **Off by default**: `false`, the sample's. `true` with `debug = true`. | **A debugging aid**, per OpenCore's `Configuration.pdf`: on, it writes `boot.efi`'s own debug log into OpenCore's log, which `Target = 67` puts on the EFI partition, so a boot that stalls inside Apple's booter can say where. It was on through firmware bring-up; nothing records a boot it diagnosed. | A boot that fails in `boot.efi`: turn it on. |
| `Misc > Debug > DisplayLevel` | **Off by default**: `2147483650` (`0x80000002`: `DEBUG_ERROR`, `DEBUG_WARN`), the sample's. `2147483714` (`0x80000042`, adding `DEBUG_INFO`) with `debug = true`. | **A debugging aid**: the EDK II levels OpenCore prints on screen. **REASONED** from `Configuration.pdf`: only DEBUG and NOOPT builds emit `DEBUG_INFO`, and this OpenCore is a RELEASE build (`EDKTarget`), so on it the extra bit shows nothing more. It is in the switch because bring-up ran with it, and a DEBUG build would show it. | A DEBUG build of OpenCore. |
| `Misc > Boot > HideAuxiliary` | `false` | **REASONED**: an entry hidden behind a keystroke is indistinguishable from one OpenCore never found. | With `ScanPolicy` narrowed, it hides nothing that would be listed. |
| `Booter > Quirks`: `AllowRelocationBlock`, `AvoidRuntimeDefrag`, `EnableWriteUnprotector`, `ProvideCustomSlide`, `EnableSafeModeSlide` | on | **INHERITED from a 0.6.6 config under TCG on Apple Silicon, never re-tested on 1.0.7 under KVM.** Only `AllowRelocationBlock` differs from the sample. | **Nothing has tested any of the five**: no boot has misbehaved. The largest untested block (§12). |
| `Booter > Quirks`: `SetupVirtualMap`, `FixupAppleEfiImages` | the sample's (on), **over the bundle's** | **REASONED**: OVMF setups generally use `SetupVirtualMap`; `FixupAppleEfiImages` did not exist in 0.6.6. | The first two to flip, one at a time, if a boot ever fails in the booter. |
| `Kernel > Emulate > DummyPowerManagement` | `true` | **INHERITED**, and the boot log shows its consequence: `waitForService(AppleIntelCPUPowerManagement) timed out`, harmless, where the driver would otherwise panic on a virtual CPU. | Never tested off. |
| `Kernel > Quirks > PanicNoKextDump` | `true` | **MEASURED, as method**: every panic diagnosis here came from a backtrace read off the screen, which a kext dump scrolls away. | Nothing. |
| `UEFI > Output > Resolution` | `1280x800@32` | **MEASURED 2026-09-17**: the resolution macOS gets is the framebuffer OpenCore sets; the sample's `Max` came up at 4096x2160 with no display driver at all, and `1024x768` gave 1024x768. The guest cannot change it at runtime. 1280x800 is **REASONED**: a CPU draws every pixel. | Nothing has measured what a resolution costs. |
| `UEFI > APFS > EnableJumpstart` | `false` | **REASONED**: 10.9 cannot mount APFS. | Nothing. |
| `SetApfsTrimTimeout` | the sample's `-1` (the bundle sets it) | **REASONED**: 10.9 predates APFS; a setting copied rather than chosen, and declined. | Nothing. |
| NVRAM `boot-args` | `-v keepsyms=1` by default. `-v keepsyms=1 debug=0x100` with `debug = true`: **`debug=0x100` is off by default**. | **MEASURED, as method**: every boot diagnosis came from reading boot text, and `keepsyms=1` names the symbols in a backtrace, so both stay on either way. `debug=0x100` is **a debugging aid**: **REASONED** from XNU's `osfmk/kern/debug.h`, where `0x100` is `DB_LOG_PI_SCRN`, which leaves a panic's text on screen instead of the grey restart screen. It was added during firmware bring-up; nothing records a measurement of what it changes. | A build that wants a quiet boot drops `-v`. |
| NVRAM `prev-lang:kbd` | `en-US:0` | The unattended install: no language picker. | A non-US guest. |
| NVRAM `run-efi-updater` | `No` | **REASONED**: keeps Apple's EFI updater from flashing firmware that does not exist. Never tested without. | Unknown: the one row here whose failure would be interesting. |
| `NVRAM > Delete` | empty | Every build starts from a fresh copy of the variable store, and a box keeps none between runs (§4), so no variable outlives a run. | A variable store that persists. |

---

## 8. Build toolchain

| Knob | Value | Why, and on whose word | How we know | What would change it |
|---|---|---|---|---|
| C dialect | `-std=gnu17`, for OpenCorePkg and OvmfPkg | **MEASURED, by a refutation** (G22, `docs/decisions/0004`): a C23-default gcc fails OpenCorePkg outright and builds a different `OVMF_CODE.fd`. | The builds on gcc 13.3.0, 14.2.0 and 16.2.1. | Nothing. It is not a pinned compiler: the same sources still compile to different bytes on different compilers. |
| `-Wno-error` | on both firmware builds | **MEASURED, by a second refutation**: gcc 16 invented a warning EDK II's `-Werror` made fatal. | The warnings are still printed; the eight artifacts are identical with and without it on gcc 13.3.0. | Nothing. Upstream keeps its discipline; this build stops inheriting it. |
| compiler range | **gcc 13 through 16, verified at 13.3.0, 14.2.0 and 16.2.1** | Measured at the three points; 15 inside by interpolation, never seen. Below 13 is refused, above 16 warns. | `internal/firmware/compiler.go` and its tests hold the constants. | A complete build and install on a new version. The failure mode up there is silent: a green build with different bytes. |
| `ccache` | off | **MEASURED**, the seam only: builds through a `PATH` wrapper are identical to straight ones. ccache itself was never measured (not installed on any host). | `docs/decisions/0004`. | A cache hit compared against a cold compile. |

---

## 9. Guest software and first boot

| Knob | Value | Why, and on whose word | How we know | What would change it |
|---|---|---|---|---|
| `user` | `vagrant`, uid 501, gid 20, in `admin` | **REASONED**: Vagrant's convention for a base box's account, in the shape Setup Assistant produced on a hand-driven 10.9.5 install (uid 501, gid 20, group 80). | **MEASURED 2026-09-27**: `verify.sh` found `vagrant` in admin. | A user who wants another name; the variable takes any name sudo can read a `sudoers.d` file for (no dot). |
| passwordless `sudo` | on, for `user` | **REASONED**: the build's `shutdown_command` and the box's halt trigger both need it without a prompt; the account has no password. | **MEASURED**: `sudo -n true` succeeds, `verify.sh` checks it, and the shutdown took 5 s. | Nothing. |
| `authorized_key` | Vagrant's insecure RSA key, which Vagrant replaces at the first `vagrant up` | **REASONED**: the box's consumer is Vagrant. RSA, not Ed25519, because a guest built without the family's OpenSSH runs 6.2p2, which cannot parse an Ed25519 line. | **MEASURED 2026-09-27**: `vagrant up` replaced it. | A user's own key: `authorized_key` and `ssh_private_key_file`. |
| `openssh` | `true`: the family's OpenSSH 10.5p1 replaces 10.9's 6.2p2 at first boot | **MEASURED**: stock 6.2p2 cannot read an Ed25519 `authorized_keys` line (added in 6.5, 2014) and offers only host keys a modern client refuses. | Every build: `OpenSSH_10.5p1, LibreSSL 4.3.2`, and an Ed25519 key authenticating with no client options. | Nothing. SSH is the guest's whole interface. |
| `updates` | `security` | `docs/decisions/0011`. | **MEASURED 2026-09-27**: build 13F1911 and the 2016-004 receipt. | See the ADR. |
| hostname | `mavericks` | **REASONED**: Setup Assistant would derive `Maverickss-iMac` from a full name. | The guests report it. | Nothing. |
| auto-login | on | **INHERITED from Setup Assistant**, which turned it on for a single-user system on a hand-driven install; an unattended boot must not stall at a login window. | The guests boot to a desktop. | Nothing, but note it grants a desktop to anyone who can boot the disk: one more reason it is never published. |
| sleep, screensaver | off | **REASONED**: a guest that sleeps stops answering SSH; a screensaver spends the CPU. | Nothing measured. | Nothing. |
| software-update schedule | off | **REASONED**: a guest that reaches out on its own is not reproducible, and 10.9 against 2026 servers may hang. | Nothing measured. | Nothing. |

---

## 10. The build and the box

| Knob | Value | Why, and on whose word | How we know | What would change it |
|---|---|---|---|---|
| no `boot_command` | none at all | **MEASURED**: Apple's installer reads `rc.cdrom.local`, `minstallconfig.xml` and `OSInstall.collection` off the media, so the install runs with nobody typing. | Every build. | Nothing. |
| `install_timeout` | `1h` | **REASONED** from the measured installs: 13–23 minutes on the primary host, 22 on a 2-core Broadwell, 28 on a 2006 Xeon. | Every build has answered SSH inside it. | A slower host, or TCG. |
| first-boot wait | up to 300 s for the payload's `.done` marker, never fatal | **MEASURED**: SSH answering is not the first boot being finished; with a modern sshd answering at once, the window before the payload finishes shrank to about a second. | `verify.sh` then fails the build if the marker is missing. | Nothing. |
| `verify.sh` | fails the build unless 10.9.5, first boot finished, passwordless sudo, and the 2016-004 receipt when updates were asked for | **REASONED**: a broken guest must not become a box. | **MEASURED 2026-09-27**: "verify: ok". | Nothing. |
| `shutdown_command` | `sudo shutdown -h now` | **MEASURED**: 10.9 ignores the ACPI power button, so a shutdown has to come from inside. | The guest powered off in 5 s. | Nothing. |
| box halt trigger | `before :halt`: the guest's own `sudo /sbin/shutdown -h now` | **MEASURED 2026-09-27**: without it, `vagrant halt` pressed the power button, waited 60 s and quit QEMU under a running guest (no `SHUTDOWN_TIME` logged). With it: 3–23 s, clean. | `docs/test-hosts.md`. | A vagrant-qemu that shuts a guest down itself. |

---

## 11. One-variable boots

MEASURED 2026-09-21 on the primary host (i7-8700B, QEMU 8.2.2, KVM), each
a qcow2 overlay on an installed guest (e1000, OpenSSH 10.5p1), booted with
no installer media, asked over SSH what it is, and discarded:

| Changed | SSH | Guest reported | 64 MiB SHA-256 |
|---|---|---|---|
| 1 CPU | 40 s | `hw=iMac14,2 1cpu 4294967296` | correct |
| 2048 MB | 40 s | `hw=iMac14,2 2cpu 2147483648` | correct |
| 1024 MB | 40 s | `hw=iMac14,2 2cpu 1073741824` | correct |
| `-cpu Nehalem` | 40 s | `Intel Core i7 9xx (Nehalem Class Core i7)`, `POPCNT` gained | correct |

All four also reported `diskbus=SATA`, 10.9.5 (13F34), the first-boot
daemon removed and `OpenSSH_10.5p1`. The correct hash is
`3b6a07d0…c421351`, so each guest did real work. **None is an install**,
so none may be written as VERIFIED: a NIC showed that "boots with X" and
"installs with X" are different claims in this guest.

---

## 12. Where a wrong answer would cost most

Ranked by cost times the likelihood of being wrong.

### 1. The five inherited `Booter > Quirks`

**None has been tested on 1.0.7 under KVM.** They decide whether
`boot.efi` is loaded and relocated correctly, so a wrong one does not
produce a warning; it produces a hang or a panic that looks like something
else. They came from another bootloader major version, on another
hypervisor and another CPU architecture. The way to change them, one at a
time, is written down, and has never run because nothing has failed.

### 2. `memory` and `cpus` for the *install*

Both came from a bring-up brief, and neither has been tested at install
time. A small CI runner has about 7 GB and 3 cores. The boots above
suggest 4096/2 is not a floor, and one 20-minute install at less would
say.

### 3. The compiler above the ceiling

The range is gcc 13–16. The failure mode above it is silent: a green build
with different bytes, already measured for C23 and `OVMF_CODE.fd`. A wrong
answer does not break a build; it breaks the claim that the boot stack is
what `docs/decisions/0004` says it is.

### Below the line

`vmport=off`, `SecureBootModel`, `SetApfsTrimTimeout`, `EnableJumpstart`,
`disk_size`, `vgamem_mb`, `usb-tablet`, `DisableWatchDog`: all untested or
barely tested, all cheap to be wrong about. `run-efi-updater=No` is the
odd one: untested, and the one whose failure could be interesting.
