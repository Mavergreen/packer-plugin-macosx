# Open questions

Questions whose answers would change what the plugin builds or what its
documentation may claim. Each says what is known, what is not, and what
would settle it. Settings whose provenance is the question belong in
`docs/configuration-register.md`; host-specific hypotheses in
`docs/host-profile.md` §4.

Two questions are answered and kept, because each corrected a claim this
project had inherited and believed.

---

## Q1. Should the guest carry Apple's post-10.9.5 updates? ANSWERED 2026-09-22

**Yes, by default: `updates = "security"`.** `none` stays available as the
baseline; `all` adds two applications. `docs/decisions/0011` has the
decision and its measurements.

What the answer rests on, MEASURED 2026-09-21 on a live 10.9.5 guest
(build 13F34), against Apple's servers:

- **Apple still serves 10.9 updates.** `softwareupdate -l` returned five.
- **Three are standalone `.pkg` files on Apple's CDN**, over plain HTTP
  with no account, so they are pinned and checksummed like every other
  ingredient rather than fetched by `softwareupdate`: **Security Update
  2016-004** (370,988,463 bytes, sha256
  `fd71517772928b35e773276b300ef30e0d264ed9d030bf3862625cab5513d1b5`,
  fetched and hashed), Safari 9.1.3 (63,197,064 bytes) and iTunes 12.6.2
  (five packages, 284,285,780 bytes). Every size matched the KiB figure
  the guest printed, exactly, which is how they are known to be the same
  artifacts.
- `iBooksDelta-1.0.1` and `RemoteDesktopClient-3.8.4` are in no catalogue
  found: not in `index-10.9.merged-1.sucatalog`'s package URLs, and not in
  any of its 333 distribution files, all fetched and searched.
- **The briefs this project started from were wrong about which update is
  last.** They said 2016-001; the guest said **2016-004**. Re-check the
  claim by asking a live guest, not by reading a write-up.

**Never run `softwareupdate` at build time.** It reaches Apple's servers
during the build, which makes the build depend on the day, and a 2013 OS
talking to 2026 servers may hang.

---

## Q3. Can an arm64 host build the same firmware?

**Unknown, and untestable on an x86_64 host.** EDK II compiles x86_64
firmware. On an x86_64 Debian-family host the native compiler *is* the
cross compiler: MEASURED 2026-09-22 on the primary host, `gcc` and
`x86_64-linux-gnu-gcc` resolve to the same file (same device, same inode,
`/usr/bin/x86_64-linux-gnu-gcc-13`), so comparing them measures nothing.
Only a host of another architecture can ask it.

Three claims are tangled together:

| | Claim | Why it might fail |
|---|---|---|
| **A** | a cross-gcc's code generation does not depend on the architecture it runs on | it should not; reproducible cross-builds rest on it |
| **B** | Debian's `gcc-13-x86-64-linux-gnu` built *for arm64* generates the same code as the one built for amd64 | separately built packages of the same source and version |
| **C** | EDK II's BaseTools produce the same PE images when they are aarch64 binaries | `GenFw` post-processes the compiler's output rather than compiling it; nothing about compiler determinism covers it, and it is the one silent failure |

**What would settle it:** one pair of builds on an arm64 host with the
same gcc version as the primary host (`13.3.0-6ubuntu2~24.04.1`, Ubuntu
24.04), at the same build directory, on the same UTC day
(`docs/decisions/0004` says why each matters), then the eight artifact
checksums, plus the intermediate `.dll` files. `GenFw` turns `.dll` into
`.efi`, so a matching `.dll` with a differing `.efi` is claim C failing,
and a differing `.dll` is A or B.

It matters because a CI runner on Apple Silicon is arm64: the answer
decides whether such a runner can build the firmware, or must be handed
one built elsewhere.

---

## Q5. Do the inherited `Booter` quirks matter?

Five `Booter > Quirks` came from an OpenCore 0.6.6 configuration under TCG
on Apple Silicon and have never been tested on OpenCore 1.0.7 under KVM.
Nothing has failed, so nothing has been flipped. **Turning each off, one
boot at a time,** would say which are needed
(`docs/configuration-register.md` §12).
