# 0034 — the build's guest reaches nothing off-link

Date: 2026-10-27
Status: accepted (F25; the owner's decisions R45 and R46, 2026-10-27)

## Decision

1. **Every template's guest network is QEMU's user netdev with
   `restrict=on`**: `templates/mavericks/`, `templates/snowleopard/` and
   `templates/mavericks-aquatransport/`,
   `["-netdev", "user,id=net0,restrict=on,hostfwd=tcp:127.0.0.1:{{ .SSHHostPort }}-:22"]`.
   The communicator's forward still works; nothing the guest sends goes
   further than QEMU. It holds for the whole build, the installer
   included, whatever process asks. libslirp then offers the guest no
   router and no DNS server in its DHCP reply (MEASURED, below), so:
   - **a host name** fails at once: CFNetwork answers -1009, "The
     Internet connection appears to be offline", in 0.02 s (MEASURED, 2 of
     2 restricted boots of a 10.9.5 box);
   - **an IP literal** does not. With no router the guest installs an
     interface default route (`default link#4 UCS`), ARPs for the
     address, and slirp never answers, so the connection fails with -1001
     only after **75 s** (MEASURED, 2 of 2). None of the builds' jobs used
     one. A 75 s stall under this netdev is that.
   - 10.9's `kextcache` is slower offline, by many fast failures, not by a
     wait (the cost, below).
2. **The layer is built the same way** (R46). Nothing in it needs the
   guest to fetch: the Aqua Transport DMG is fetched on the host by the
   `macosx-download` data source and uploaded by a `file` provisioner,
   `install.sh` installs it from the DMG, and `verify.sh` checks only the
   guest's own state: 10.9.5 (13F1911), the pinned Aqua Transport's and
   ModernRootCertificates' receipts, and no tap by receipt, `kextstat` or
   on disk. Its three `curl` fetches are gone.
   - **The TLS proof is where the box boots with a network.**
     `bin/gate.sh`'s `reach` step (`gate/guest-checks.sh reach`) and
     mavericks-vm's self-test (`.github/workflows/self-test.yml`, held by
     its `tests/self_test_workflow.bats`) boot the layer box and fetch
     `https://github.com/`, `https://release-assets.githubusercontent.com/`
     and `https://downloads.claude.ai/claude-code-releases/latest` with the
     guest's own `/usr/bin/curl`. A box whose Aqua Transport does not
     work fails there, not at build time.
3. **No build-only revocation setting** (R45). The 10.9 base build's
   first-boot 2016-004 install takes about 200 s longer (the cost, below),
   and that is accepted.
4. **`internal/templatetest.CheckGuestNetwork` holds every template to
   it**: one `-netdev` row as HCL parses it, a user netdev `net0` whose
   only forward is the communicator's, no `guestfwd`, no `-nic`/`-net`,
   and `restrict=on` given once (`TestTheBuildsGuestReachesNothingOffLink`,
   in each of the three templates). Each of these mutations fails it:
   restrict off, absent, `yes`, given twice; a `guestfwd`; a second
   `-netdev`; a `-nic`; the restricted row commented out over an open
   one; a second `hostfwd`; a wrong id.

ADR 0011 already said the guest must not reach Apple during the build,
for the updates. This is the boundary that makes it true for everything
else in the guest.

## Context: F25

A 10.6.8 image held `crlcache.db`/`ocspcache.db` entries for "Apple
Public EV Server RSA CA 1 - G1": something in the guest had made a TLS
connection to Apple during the build. Every guest-originated flow of two
builds of each at HEAD (00e5c59), captured on the guest's NIC
(`-object filter-dump` on `net0`) and matched to the guest's own logs,
read offline from each image. Host load 6.3-10.5 (1 min, 12 CPUs).
MEASURED, unless marked:

| 10.6.8 (`updates = security`, 1 vCPU) | builds | what |
|---|---|---|
| `XProtectUpdater` (the combo's `com.apple.xprotectupdater`, `RunAtLoad`) | 2 of 2, at each boot after the combo | `GET http://configuration.apple.com/configurations/macosx/xprotect/1/clientConfiguration.plist` (answered, 693 bytes), then HTTPS to the same host, which fails ("An SSL error has occurred", -1200): 10.6's TLS meets 2026's server. |
| `crsud` (2013-004's `com.apple.crsud`, `RunAtLoad`, every 4 h) | started 2 of 2, at the last boot, while the provisioners ran; scanned 1 of 2 | **What it is for:** its binary's strings name "Found the following required updates", `PKInstallClient`, `com.apple.crsud.DownloadAndInstallUpdates` and "Install finished!": it downloads and installs the updates Apple's catalog marks required. REASONED: an unrestricted 10.6.8 build would install whatever the catalog marked required, if 10.6's TLS could reach it. That is ADR 0011's concern head-on. **The build that scanned:** "crsud: Starting", "offline" 7 s into the boot, then 20 s later HTTPS to `swscan.apple.com` (its catalog), "retries exhausted" after. That TLS made `ocspd` POST to `ocsp.apple.com/ocsp03-apevsrsa1g101` and fetch `crl.apple.com/apevsrsa1g1.crl` and `crl3.digicert.com/DigiCertGlobalRootG2.crl`: **F25's cache entries**. `crlcache.db` and `ocspcache.db` were last modified within a few seconds of that fetch (the image keeps modification times only, and the guest's clock ran about 3 s off the host's). The TLS client is `crsud` by correlation: 1-3 s after "crsud: Starting", its own log lines, and `swscan.apple.com` and the catalog URL format in its binary. **The other build:** `crsud` asked for `com.apple.ServiceManagement.daemons.modify` (`secure.log`), and `launchdadd`, acting on it, logged "Could not send MIG reply" because its caller was gone. Its strings name that path: "Syncing up xprotect and crsud preferences...", "Disabling crsud service - xprotect was found disabled...". That it took this path is REASONED; why it varies between builds is not established. That image has no `ocspcache.db`. |

| 10.9.5 (`updates = security`, 2 vCPUs) | builds | what |
|---|---|---|
| `kextcache` (in the OS installer and, at first boot, kextd's and 2016-004's), validating `/Library/Extensions`' Developer ID kexts | 2 of 2 | `ocspd`: `ocsp.apple.com/ocsp04-devid01` for four serials, which are SoftRAID's, HighPoint's (IOP and RR share it), Promise's and CalDigit's signing certificates (found in those kexts' signatures), and `crl.apple.com` `root.crl`, `codesigning.crl`, `timestamp.crl`, `www.apple.com/appleca/{root,codesigning}.crl`, each burst 1-6 s after a `kextcache` started. |
| `ocspd` for `ocsp03-swupdateca01`, `ocsp03-applerootca`, `timestamp.crl`: `installer` checking 2016-004's signature, or `softwareupdated` (REASONED, not settled) | 2 of 2 | The requests came 2-5 s after the installer's trust evaluation of 2016-004, and also inside `softwareupdated`'s first-run scan (2 s after its catalog fetch), whose products are signed under the same Software Update CA. Timing does not separate the two. `pkgutil --check-signature` of 2016-004, online, sent no OCSP (1 of 1), a weak point against PackageKit, since `installer` may use another policy. The change does not depend on which it is. |
| `softwareupdate_firstrun_tasks` and `softwareupdated` ("Fire periodic check after upgrade to 10.9.5") at first boot, before `firstboot.sh` turns the schedule off | 2 of 2 | HTTPS to `swscan.apple.com` (117 KB) and four parallel HTTPS to `swdist.apple.com` (2.2 MB); the image kept 962 files (15.6 MB) under `…/C/com.apple.SoftwareUpdate.SUCatalogDataManager/` and `/Library/Updates/ProductMetadata.plist` (1.1 MB). |
| `apsd` | 2 of 2 | HTTPS to `albert.apple.com` (device activation), and DNS for its push couriers, with no connection to them. The image's `/Library/Keychains/apsd.keychain` holds a certificate issued by "Apple iPhone Device CA", and `com.apple.apsd.plist` a `MachineUniqueIdentifier`. **That is stale fetched state, not a shared identity.** On a box whose image carried an activation, `apsd` logged "Stored identifier doesn't match host" and "Certificate doesn't match host" at boot (3 of 3), and online it activated again and stored a new certificate (1 of 1; SHA-1 `1A38…` before, `7BFC…` after), its `MachineUniqueIdentifier` moving from the image's to this boot's NIC address. Every machine has its own MAC and UUID (ADR 0024), so each box re-keys as itself. That box's stored identifier was a MAC; these builds stored a UUID-shaped one (`51BC2929-0851-5EB1-…`), and that the same check applies to it is REASONED. |
| the OS installer's own reachability check | 2 of 2 | DNS only, `www.apple.com`. |

So the brief's guess was not borne out as it stood: the 10.6.8 cache
entries are `crsud`'s, and most of 10.9's traffic is not about signatures
at all. Whether PackageKit's own signature check of 2016-004 fetches
revocation is not settled (above). None of it came from the installer
media (the caches were written during the build, at the fetch), from NTP,
iCloud or the communicator.

## Why the netdev, and not the guest or the one step

- **The installer is a guest too.** 10.9's first fetch is `kextcache` in
  the OS installer, before any file of ours is on the target. Nothing in
  `firstboot.sh` can be earlier.
- **It is many jobs, not one step.** Five on 10.9 and two on 10.6.8, each
  Apple's, each with its own trigger. Disabling them by name is an
  allowlist: the next update or OS brings another, and the image would
  ship with them off. At the NIC, one line covers every process, including
  ones nobody has named yet.
- **Nothing the builds need is off-link.** Every ingredient is fetched on
  the host by the data sources and pinned (ADR 0011), the layer's DMG
  included; the communicator's connection is inbound, through the
  forward. Four base builds and one layer build with it, below, finished
  with `verify.sh` passing.

This is not a workaround in the project's sense: nothing is detected and
nothing is papered over. It is the build's own policy (ADR 0011) put where
it can be enforced, so it always applies.

## Measured with it

Two builds of each base template from a snapshot of HEAD with only the
`-netdev` row changed, the same capture. Host load 6.3-11.4. A flow is one
guest port to one IPv4 address: a DNS question through QEMU's resolver
counts, since QEMU forwards it to the host's. The HEAD counts are IPv4
only. **IPv6, counted separately:** the HEAD 10.9 builds also sent IPv6
TCP SYNs off-link, 18 and 12, to Apple's 2620:149::/32 and Akamai's
2600:141b::/32 on ports 80 and 443, and nothing came back; the 10.6.8
builds sent none. With `restrict=on` there were 0 global IPv6 frames in
all 4 builds (libslirp drops restricted UDPv6, and TCP of either family
that has no forward: READ, `src/udp6.c`, `src/tcp_input.c`).

| | HEAD (N 2 each) | `restrict=on` (N 2 each) |
|---|---|---|
| guest-originated IPv4 flows off-link, 10.6.8 | 44 and 24 (10 and 6 of them TCP) | **0 and 0** (only on-link mDNS, NetBIOS, DHCP, IPv6 ND) |
| guest-originated IPv4 flows off-link, 10.9 | 67 and 63 (16 and 16 TCP) | **0 and 0** |
| guest-originated IPv6 off-link, 10.6.8 / 10.9 | 0 / 18 and 12 SYNs, unanswered | **0 / 0** |
| DHCP reply | router and DNS 10.0.2.3 | **neither** |
| `ocspcache.db` in the image | 10.6.8: 1 of 2; 10.9: 2 of 2 | **none** (`crlcache.db` only as ocspd creates it empty at the first boot) |
| 10.9: SU catalog cache, `ProductMetadata.plist`, apsd device certificate | 2 of 2 | **0 of 2** (`apsd.keychain` with no certificate; apsd logs "The Internet connection appears to be offline" and retries) |
| what the guest's jobs logged | | 10.6.8: XProtectUpdater -1009 "offline"; crsud "offline", "retries exhausted" in 20 s (in the build where it scanned). 10.9: apsd -1009. |
| `verify.sh`, build rc | ok, 0 | ok, 0 |
| 10.6.8: VM start → SSH; SSH → halt; whole build | 1058/1095 s; 236/273 s; 1465/1415 s | 1079/1170 s; 250/226 s; 1376/1436 s |
| 10.9: VM start → SSH; SSH → halt; whole build | 1256/1173 s; 265/241 s; 1765/1444 s | 1334/1352 s; 194/203 s; 1561/1586 s |
| 10.9: 2016-004's install at first boot (`installer`'s own total) | 133/133 s | **334/333 s** |

The layer, built restricted over a restricted 10.9 base box, is measured
under "The layer, measured" below.

### The cost: 10.9's kextcache, offline, fails many fetches one by one

2016-004's install takes 200 s longer offline, 2 of 2, all of it in its
`kextcache -update-volume / -Installer`. Probed on a box built with this
(the same image each time, a fresh overlay per boot, alternated, host
load 6.4-7.5), `kextcache -system-prelinked-kernel`:

| guest network | N | seconds |
|---|---|---|
| restricted | 2 | 234.8, 236.3 |
| open | 2 | 17.1, 16.8 |
| restricted, twice in one boot | 1 | 237.6, then 221.5 |

**The mechanism.** `kextcache` checks every kext's signature, and each
chain's revocation check calls `ocspdCRLFetch`. MEASURED with dtrace on
`kextcache`'s calls into ocspd: **1,562 calls per run**, restricted or
open, on either image. A failed fetch is not cached, so every chain of
every kext asks again.

| | box built with this, restricted (1 run) | box built before this, restricted (2 runs) | box built before this, open (1 run) |
|---|---|---|---|
| `ocspdCRLFetch` calls | 1,562 | 1,562, 1,562 | 1,562 |
| their total | 222.3 s | 108.4 s, 108.0 s | 0.7 s |
| the longest | 184 ms | 178 ms, 186 ms | 183 ms |
| distribution | all 100-184 ms | about half under 25 ms, the rest 100-186 ms | all but one under 25 ms |
| `kextcache` wall | 243 s | 132 s (the second run) | 21 s |

The box built before this change carries a populated `crlcache.db` from
its networked build, so about half its calls are answered from it; the
box built with this has only the empty one ocspd made at its first boot,
so every call pays (the restricted-built box is the 2026-10-27 fix-round
build, over a fresh overlay, host load 7.1-9.1).

No call waits seconds: **this is not a per-fetch timeout.** READ,
`security_ocspd` (every 10.9-era tag, `server/ocspdServer.cpp:55-57,1033-1046`):
`ocsp_server_crlFetch` starts the fetch on a dispatch queue, then polls
with `CFRunLoopRunInMode(kCFRunLoopDefaultMode, 0.1, TRUE)` until it
finishes, giving up after `CRL_MAX_DOWNLOAD_WAIT` (3.0 s). A fetch that
fails at once is noticed at the next 0.1 s tick, which is the 100-186 ms
band. Online, ocspd answers from its cache after the first fetch. The
`sample`s taken during the probes above (`kextcache` in
`SecStaticCodeCheckValidityWithErrors` → `SecTrustEvaluate` →
`tpFetchCrlFromNet` → `ocspdCRLFetch`, `ocspd`'s main thread in its run
loop, 3 of 3) fit this poll loop.

**Why failing faster cannot help.** The network side already fails as
fast as it can: with no DNS server, a fetch by host name fails in 0.02 s.
What costs is ocspd's 0.1 s poll, paid once for each of the 1,562 calls.
Only fewer calls would help: a build-only revocation setting
(`com.apple.security.revocation` `CRLStyle` None), set for the build and
restored after it. It is **not adopted** (R45): it is guest emulation
during an Apple install, it is 10.9's alone, and the 10.9 build totals
were not measurably longer at N 2.

`kextutil -n -t` of each Developer ID kext took 0.15-0.97 s either way
(16 runs). With the network, the CRLs and OCSP answers fetched by the
installer phase's and kextd's `kextcache` are in ocspd's cache by the time
2016-004's runs, which is why it was 17 s.

The 10.9 build as a whole was not slower (1561/1586 s against
1765/1444 s): its last phase lost softwareupdated's scan and downloads.
10.6.8's builds were not slower either. With N 2 a side, under a loaded
host, neither total is a measured difference.

**Does the cost reach boxes?** Online, no: a box's network is
unrestricted, and `kextcache` took 21 s (MEASURED). On a host with no
network, not measured: QEMU there still forwards DNS to the host, so a
lookup that hangs could take up to ocspd's 3 s cap per call (READ), and
only when the box rebuilds its kext caches.

### The layer, measured

One build, 2026-10-27 (N 1), of `templates/mavericks-aquatransport/` as
this change leaves it, plus a `-object filter-dump` on `net0`, over a
10.9.5 base box built restricted from the same tree. Host load 7.5-10.9.

| | result |
|---|---|
| guest-originated IPv4 flows off-link | **0**: every TCP frame was the communicator's SSH through the forward (to and from 10.0.2.2); the rest was on-link NetBIOS, mDNS, IGMP, DHCP and ARP |
| global IPv6 frames | **0** (53 IPv6 frames, all link-local or multicast: ND, MLD, mDNS) |
| DHCP reply | no router, no DNS (options 1, 51, 53, 54 only) |
| `verify.sh` | `verify: ok: 10.9.5 (13F1911), Aqua Transport 2026.10.09 with ModernRootCertificates, no tap`; `security-original=yes`; build rc 0 |
| the whole build | 390 s: disk conversion 20 s; SSH 32 s after the wait began; install 12 s; the restart and `verify.sh` 47 s; the zero-fill 4 min 13 s; halt and package 26 s |

ADR 0016's build, with the network and the curl fetches, took 5 min 57 s
(host load not recorded): the same order. The DMG came from the host's Packer
cache through the `file` provisioner, as the template says; nothing in
the build asked the guest to fetch.

## Not changed

- **The box.** A box's own network is the Vagrantfile's, unrestricted:
  once a user boots it, these jobs reach Apple as Apple meant, and the
  image no longer carries anything they fetched at build time.
- **`bin/gate-vm.sh`.** It boots a finished box to test it, not a build,
  and its `reach` step needs the network.
- **`softwareupdate --schedule off`** at first boot stays: it is for the
  box's later life. `firstboot.sh`'s comment no longer claims it is what
  keeps the build's guest quiet.

## What would change it

A build step that needs a guest-side fetch; a QEMU whose `restrict=on` no
longer withholds the router and DNS (the test would still pass; the
capture would show it); the kextcache cost becoming worth a build-only
revocation setting, which would have to detect the offline network it
answers and say so (declined, R45).
