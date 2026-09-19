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
