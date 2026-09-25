# 0004 — The boot stack: what it is made of, and how it is rebuilt

Date: 2026-09-17
Status: accepted

## Context

Everything the guest runs before the macOS kernel starts is the **boot
stack**: the firmware, the bootloader, its configuration, its drivers and
the kexts it injects. `mavericks-firmware` builds it. This document records
what the boot stack is made of, where each part comes from, and what
"rebuilt from source" does and does not promise, so that the question "is
this the same boot stack" has an answer someone can check.

## The tiers

- **Tier 0**: built from pinned source by `mavericks-firmware`.
- **Tier 1**: vanilla upstream release binaries, pinned to an exact
  version and checksummed.
- **Tier 2**: someone else's custom blob. Never in the boot path.

Two Tier 2 artifacts are pinned in `assets/pins/sources.tsv` as evidence
rather than ingredients: `utm-bundle` (the UTM bundle that booted 10.9
under TCG before this project built its own boot stack) and
`opencore-legacy-img` (khronokernel's OpenCore 0.6.6 image inside it).
Nothing the plugin builds or boots reads either one. Deriving an image
from a Tier 2 artifact does not launder it.

## The boot path

| Component | Tier | How it is made | Pinned to |
|---|---|---|---|
| **Firmware** `OVMF_CODE.fd` | **0** | EDK II's `build -a X64 -b RELEASE -t GCC -p OvmfPkg/OvmfPkgX64.dsc`, in the same EDK II tree OpenCore builds in | `acidanthera/audk` `0672a009e9ca85753d240324d761341adf0291b3` |
| **Firmware** `OVMF_VARS.fd` | **0** | the same build; the template copies it per build (`efivars.fd`), so the one in the cache is never booted in place | the same |
| `OVMF.fd` | **0** | the same build; the combined image, which nothing downstream reads | the same |
| **Bootloader** `BOOTx64.efi` (= `Bootstrap.efi`) | **0** | OpenCorePkg's `build_oc.tool` | OpenCorePkg `1.0.7` |
| **Bootloader** `OpenCore.efi` | **0** | the same | OpenCorePkg `1.0.7` |
| `OpenRuntime.efi`, `OpenPartitionDxe.efi` | **0** | the same | OpenCorePkg `1.0.7` |
| **HFS+ driver** `OpenHfsPlus.efi` | **0** | the same (`Staging/OpenHfsPlus`, in the default target) | OpenCorePkg `1.0.7` |
| **Config** `assets/firmware/config.plist` | **0** | ours, derived from 1.0.7's `Sample.plist`; every divergence explained in `assets/firmware/README.md` | this repository |
| **Kext** `Lilu.kext` | **1** | acidanthera's release zip | Lilu **1.7.2**, zip `53967d7d…` |
| **Kext** `VirtualSMC.kext` | **1** | acidanthera's release zip | VirtualSMC **1.3.7**, zip `12f1d379…` |
| **EFI image** `opencore.img` | **0** | a 192 MiB GPT disk with one FAT32 EFI System Partition, written by `internal/diskimg` with no loop device, no mount and no root | assembled from the rows above |

**No Tier 2 row.** That is the point of the table.

`assets/firmware/README.md` records why these kext versions, and how their
Darwin 13 (10.9) support was checked by reading their `Info.plist`s.
Building them from source would need Xcode.

### What the Tier 0 rows are pinned through

| Input | Pin |
|---|---|
| OpenCorePkg | release tarball for tag `1.0.7` (`opencorepkg-src`) |
| `ocbuild/efibuild.sh` | commit `e9ed49cb7a4f7fa2830c024a13d63de27c2e0d1a` (`ocbuild-efibuild`) |
| `acidanthera/audk` (EDK II) | commit `0672a009e9ca85753d240324d761341adf0291b3` (`audk-src`) |
| audk's 12 submodules | the commits audk's gitlinks name (`audk-*`); all twelve, because EDK II's `build.py` validates every `[Includes]` path in every `.dec` it parses |
| `assets/firmware/patches/0001-*` | makes `build_oc.tool` read the pinned `efibuild.sh` instead of fetching one off `master` |
| `assets/firmware/patches/0002-*`, `0003-*` | state OvmfPkg's C dialect, and stop inheriting upstream's `-Werror` (below) |

Every pin lives in `assets/pins/sources.tsv` with its sha256.
`internal/firmware/pins.go` declares which commit each audk tarball must
name, and `mavericks-firmware` refuses a registry URL that names another,
so the two cannot quietly disagree.

`OpenCore.efi` and `OVMF_CODE.fd` come out of one EDK II tree. A second
tree would mean a second list of submodule pins, a second thing that can
drift.

The build runs with no network. Upstream's `build_oc.tool` ends by
fetching `efibuild.sh` off `master` and `eval`ing it, and that script
clones audk at `master`; patch `0001` and `OFFLINE_MODE` replace both with
the pinned tarballs. MEASURED 2026-09-17 on the primary host: builds inside
an `unshare -rn` namespace with no network completed, cold and warm, and
produced checksums identical to a networked build of the same pins.

## What "rebuilt from source" promises, as measured

The same pinned sources give the same bytes **only when three more things
are the same**, and each was found by measurement:

1. **The build date.** `OpenCore.efi` embeds it. MEASURED 2026-09-20: a
   cold rebuild of the same sources by the same compiler on a later day
   differed from the earlier one in exactly two bytes, the day of an
   embedded `2026-09-17`. The other seven artifacts were identical across
   days.
2. **The build directory.** EDK II writes each module's debug-symbol path
   into the PE image it emits. MEASURED 2026-09-21: two cold builds of the
   same sources by the same compiler on the same day, at two different
   directories, agreed on one of the eight artifacts, `OVMF_VARS.fd`, which
   holds no code. The path is also limited: `docs/decisions/0003`.
3. **The compiler.** MEASURED 2026-09-20/21: gcc 16.2.1 on `squirrel-zapper`
   built `OVMF_CODE.fd` as `e3d0c6f5…`, where gcc 13.3.0 on the primary host
   built `195c4dcf…` from the same pins and flags.

With all three held, the build is deterministic. MEASURED 2026-09-25 on the
primary host (gcc 13.3.0): two cold builds of the same pins at the same
build directory on the same day produced identical checksums for all eight
artifacts. The EFI image is deterministic by construction:
`internal/diskimg` derives its GPT GUIDs and FAT volume serial from fixed
seeds rather than drawing them at random.

So the boot stack is reproducible **per toolchain, per build directory and
per UTC day**, not across them. The eight checksums MEASURED on the
primary host on 2026-09-19, gcc 13.3.0, at one fixed build directory, cold
builds before and after `-Wno-error` (identical either way):

| Artifact | sha256 |
|---|---|
| `OVMF_CODE.fd` | `195c4dcff2abf2f5aea08c290f057432704a250eab56b0b887ac0f8503ee58d2` |
| `OVMF_VARS.fd` | `5d2ac383371b408398accee7ec27c8c09ea5b74a0de0ceea6513388b15be5d1e` |
| `OVMF.fd` | `e44f708330318e8963baea94c91ed265761794e565e6a48db8821e92e4cbda17` |
| `BOOTx64.efi` | `eb05c27990e7162011b2ef5229d3e2b8be23a8e0bfd79d77c1891cee175e0094` |
| `OpenCore.efi` | `a6e91a7a995f8792e987da25c2c7061e2dc7907f8c2eef7dec61ebd00ff3669a` |
| `OpenRuntime.efi` | `d5bece452e5c2180b7f588b40b12c2fe64663548dbbe0de01038c3db45083a5d` |
| `OpenPartitionDxe.efi` | `e0ee5f238725685eff2f423558b933497c5475c257f747aa281e2d88018723ea` |
| `OpenHfsPlus.efi` | `93f491375fbd4c0541b55d64b8d4e2f01cafde4f66b7f520f943d3351a45040a` |

`OVMF_VARS.fd`, which holds no code, is the one row expected to match
anywhere. The others are a record of one build, useful for comparing a
build made under the same conditions, and nothing more.

## The compiler: a declared range, not a pin

Every input is pinned except the one that translates them: the host's C
compiler.

### The dialect is stated: `-std=gnu17`

EDK II sets no `-std`, so without one every file is compiled in whatever
dialect the host compiler defaults to. MEASURED 2026-09-20 on
`squirrel-zapper` (EndeavourOS, a C23-default gcc): the OpenCore build
failed:

```
OpenCorePkg/Library/OcAppleImg4Lib/libDER_config.h:31:17:
  error: two or more data types in declaration specifiers
   31 | typedef BOOLEAN bool;
```

`bool` is a keyword in C23, GCC 15 defaults to `gnu23`, and EDK II
compiled with `-Werror`. Scoping `-std=gnu17` to that one library only
moved the error to OpenCorePkg's vendored zlib (`old-style function
definition`), so the dialect is set for the whole platform.

The quieter half: OvmfPkg compiles clean under C23 and comes out
different. MEASURED 2026-09-20 on the primary host, the same tree and the
same gcc 13.3.0, with a `gcc` wrapper that prepends `-std=c2x` (a faithful
stand-in for a C23 default, since an explicit `-std` later on the command
line still wins), four cold builds:

| compiler default | `-std=gnu17` stated | OpenCore | `OVMF_CODE.fd` |
|---|---|---|---|
| gnu17 | no | builds | `195c4dcf…` |
| C23 | no | **fails**, `libDER_config.h:31` | `3373692a…` |
| gnu17 | yes | builds, identical | `195c4dcf…` |
| C23 | yes | **builds, identical** | `195c4dcf…` |

The dialect reaches OpenCorePkg through upstream's own
`$(OCPKG_BUILD_OPTIONS)` hook in `OpenCorePkg.dsc`, passed as
`-D OCPKG_BUILD_OPTIONS=…` in `BUILD_ARGUMENTS`. `efibuild.sh` splits
`BUILD_ARGUMENTS` on spaces and commas, so the two flags are joined with a
tab, which survives the split and reaches the generated makefiles as a
space. OvmfPkg has no such hook and gets patch `0002`. `internal/firmware`
checks that the flags took: after the OpenCore build, in a generated
makefile; before the OVMF build, in the patched `.dsc`.

### Upstream's `-Werror` is not inherited: `-Wno-error`

MEASURED 2026-09-20 on `squirrel-zapper`, gcc 16.2.1, with the dialect
stated: OpenCore built clean in 397 s, and OVMF failed in 26 s:

```
MdeModulePkg/Library/CustomizedDisplayLib/CustomizedDisplayLib.c:435:18:
  error: variable 'Count' set but not used [-Werror=unused-but-set-variable=]
```

A new compiler invents new warnings, and `-Werror` turns each into a build
failure in code this project does not own and cannot patch without
forking EDK II. `-Werror` is upstream's discipline for upstream's own
development; a downstream consumer pinning one commit gains nothing from
inheriting it. So the firmware builds append `-Wno-error`, through the
same two seams as the dialect (patch `0003` for OvmfPkg).

- **The warnings are still printed.** `-Wno-error` cancels the promotion
  to errors and nothing else; `-Wall` is untouched and every diagnostic
  still reaches the build logs.
- **Firmware only.** The repository's own code keeps every gate it has:
  `go vet`, staticcheck, shellcheck and `bin/bash32-check.sh`. The
  argument is about C this project did not write.
- **It does not change the artifacts.** MEASURED 2026-09-19: the eight
  checksums above, identical before and after.

### The range: gcc 13 through 16

`internal/firmware/compiler.go` declares the range, and `mavericks-firmware`
checks the host's compiler against it before it builds:

| GCC | Status | Evidence |
|---|---|---|
| **13.3.0** | **VERIFIED** | the primary host, repeatedly, from cold trees; every checksum above |
| **14.2.0** | **VERIFIED** | `ap-juicer`, 2026-09-21: the boot stack built, and a guest installed on it and answered SSH |
| **15.x** | **NOT TESTED** | inside the range by interpolation between two verified neighbours; nobody has built with a GCC 15 |
| **16.2.1** | **VERIFIED** | `squirrel-zapper`, 2026-09-21: with both fixes, a complete build, and a guest installed on it, booted without installer media and answered SSH. Its artifacts differ from gcc 13's (`OVMF_CODE.fd` `e3d0c6f5…` against `195c4dcf…`), which is what an unpinned compiler means, not a defect |
| below 13 | **NOT TESTED** | never tried, which is not "known to fail" |

| Compiler | What happens |
|---|---|
| inside the range | one log line |
| **below the floor** | **refused**, before any source is unpacked, naming what was found and that the project has not tested it |
| **above the ceiling** | **warns and builds.** Refusing would refuse every new distribution. The warning says that up here the failure mode is usually not an error (OvmfPkg compiled clean under C23 and emitted different bytes), so a green build is not proof |
| cannot tell | warns and builds, naming what it could not parse: `gcc` on macOS is clang |

`mavericks-firmware`'s `compiler` option (`"<name> <version>"`) tells the
check what to believe instead of asking the compiler. It changes the
judgement and nothing else. The compiler line EDK II will actually run is
itself a row of the firmware's input listing, so a changed compiler
rebuilds the firmware rather than reusing a cached one.

**Why a range and not a pinned toolchain.** A pinned toolchain, in a
container or bootstrapped, is the only thing that makes "the same sources
produce the same bytes" true across hosts, and it is the right answer if
these images ever have to be independently verifiable. It is also a large
amount of machinery against a requirement nobody has written down. The
range is the cheapest change that turns a silent break into a clear
message, and it says which toolchain to pin the day one is needed. **The
range moves only on evidence**: a complete build and install on a new
version.

### ccache: off by default

`mavericks-firmware`'s `ccache` option compiles through ccache, if it is
installed, by putting a `gcc` wrapper first on `PATH`; the cache lives
under the firmware workspace, never the repository.

- MEASURED 2026-09-21 on the primary host, gcc 13.3.0: two cold builds at
  the same build directory on the same day, one straight and one through a
  `PATH` wrapper that runs the real compiler, produced identical checksums
  for all eight artifacts. The wrapper also answers `--version` as the
  compiler it wraps, so the compiler row of the listing does not move.
- NOT MEASURED: ccache itself. It is not installed on any host this has
  run on, so nobody has compared a cache hit against a cold compile.

So it is off by default (`firmware.CcacheDefault`), and it is not a row in
the input listing: installing ccache must not rebuild the firmware. A
comparison of a cache hit against a cold compile, on a host that has
ccache, is what would move the default.

## What a host needs to build it

`bash`, `make`, `git` (the patches are applied with `git apply`),
`python3`, `nasm`, `iasl`, `zip` and a gcc inside the range, plus the
`uuid/uuid.h` header that BaseTools includes. `mavericks-firmware` checks
for each before it builds and names whatever is missing. These are
build-time tools, not shipped components: nothing in the boot path above
is a distribution package.
