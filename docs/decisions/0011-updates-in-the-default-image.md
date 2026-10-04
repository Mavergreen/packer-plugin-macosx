# 0011 — Apple's post-10.9.5 updates, and the default

Date: 2026-09-22
Status: accepted

## Context

A 10.9.5 guest could carry Apple's post-10.9.5 updates or not. MEASURED
2026-09-21 on a live 10.9.5 guest (build 13F34), asked by `softwareupdate
-l` against Apple's servers: Apple still serves 10.9 updates, five of them.
Three are downloadable as standalone packages from Apple's CDN, over
plain HTTP, with no account: Security Update 2016-004, Safari 9.1.3 and
iTunes 12.6.2. `docs/open-questions.md` Q1 has the rest.

## Decision

The template's `updates` variable, passed to `mavericks-media`:

| `updates` | installs | for |
|---|---|---|
| `none` | nothing | a guest with nothing after 10.9.5, and the baseline any performance measurement compares against |
| **`security`** (the default) | Security Update 2016-004, the last one Apple shipped for 10.9 | a guest anyone actually runs |
| `all` | + Safari 9.1.3 and iTunes 12.6.2 | opt-in: these are applications, not the OS |

**`security` is the default** because a guest someone runs should have the
last security update its operating system ever received. The one reason
not to install it, that an update might change something and make a
performance number unattributable, is a reason about the baseline, and the
baseline keeps `none`.

## How the updates reach the guest

**Not by `softwareupdate`.** Nothing in this project runs it to fetch
anything, at build time or at first boot. It would reach Apple's servers
during the build, make the build depend on whatever Apple serves that day,
and a 2013 OS talking to 2026 servers may simply hang. (The first boot
does run `softwareupdate --schedule off`, the opposite act.)

Instead, every package is a standalone `.pkg` pinned by sha256 in
`assets/pins/sources.tsv`, fetched by `mavericks-media` into the shared
download cache and verified there, like every other ingredient. The
packages ride on the installer media, outside `OSInstall.collection`. The
first-boot payload's `postinstall` copies them to the target volume at
install time, and `firstboot.sh` installs them with `installer -pkg …
-target /` on the booted system. They are never republished;
`bin/no-apple-bytes.sh` is the gate.

## What the packages themselves settled

Each was read out of the packages rather than assumed, and each shaped
the implementation.

### 1. The updates install BEFORE the family's OpenSSH

Security Update 2016-004's payload contains `./usr/bin/ssh` and
`./usr/sbin/sshd`. The OpenSSH System-Replace package puts symlinks at
those paths. In the other order the update would overwrite them, and the
guest would quietly fall back to OpenSSH 6.2, which a modern client
refuses (`docs/configuration-register.md`, "Guest software"). So updates
go first and OpenSSH second, and `firstboot.sh`'s check that OpenSSH is
usable judges the state the guest is actually left in.
`/usr/libexec/sshd-keygen-wrapper` is not in the payload, so the wrapper
the first boot writes survives. MEASURED 2026-09-22: in a finished
`security` guest, `/usr/bin/ssh` and `/usr/sbin/sshd` are the symlinks,
and `ssh -V` says `OpenSSH_10.5p1, LibreSSL 4.3.2`.

### 2. `sw_vers` is not the witness; the build number and the receipt are

`sw_vers -productVersion` still says **10.9.5** after 2016-004: it is a
security update, not a point release. A zero exit from `installer` is not
evidence either. What moves, MEASURED 2026-09-22 from the running guest:

- **a receipt**: `pkgutil --pkgs` gains
  `com.apple.pkg.update.security.2016-004Mavericks.13F1911`;
- **the build number**: the update carries `SystemVersion.plist`, so
  `sw_vers -buildVersion` goes **13F34 → 13F1911**.

`templates/mavericks/verify.sh` fails the build, before a box is made, if a guest
built with `security` or `all` lacks that receipt.

### 3. The media's free space is not spare room

The media partition is Apple's reference size rounded up to whole MiB plus
a 512 MiB margin. MEASURED 2026-09-22, read out of the HFS+ volume header
of media carrying no updates: **483.8 MiB free**. 2016-004 is 353.8 MiB,
and `all` is 685 MiB, which would not have fitted at all and would have
failed as a short write inside a 7 GB image. So `mavericks-media` enlarges
the partition by the packages' size plus 64 MiB for their catalog entries;
with nothing to carry it adds nothing, and the partition stays 6759 MiB
(7,087,325,184 bytes). The `security` media came out with 548.0 MiB free.

## Built and measured

MEASURED 2026-09-22 on the primary host, both values end to end, both
guests installed unattended, booted with no installer media attached and
answered SSH:

| | `none` | `security` | difference |
|---|---|---|---|
| media build | 108 s | 119 s | **+11 s** |
| install, until SSH | 819 s | 963 s | **+144 s** (+18%) |
| of which, the update itself | — | 92 s (from the guest's own log) | |
| media partition | 7,087,325,184 B | 7,525,629,952 B | +418 MiB |
| free on the media | 483.8 MiB | 548.0 MiB | |
| files on the media | 39,415 | 39,416 | +1 |
| finished qcow2 | 8,960,737,280 B | 10,685,710,336 B | **+1.61 GiB** |

Plus a one-time 354 MB download. The 144 s is more than the 92 s the
`installer` run took; the rest is the first boot replacing 6,891 files and
rebuilding the kernel and dyld caches.

MEASURED 2026-09-27 with the plugin and template: a default (`security`)
build's guest reports `13F1911` and the 2016-004 receipt
(`docs/test-hosts.md`).

## What `none` keeps

`none` adds no rows to the media's input listing, no packages to the
media and no update lines to the guest's `firstboot.conf`, and its
partition geometry is the base one above.

## The ingredients with a reason and no pin

`iBooksDelta-1.0.1` and `RemoteDesktopClient-3.8.4` were offered to the
live guest by Apple and are in **no catalogue anyone has found**: not in
`index-10.9.merged-1.sucatalog`'s package URLs, and not in any of its 333
distribution files, all fetched and searched. There is no URL to pin.
`INGREDIENTS.md` records them anyway, with that as the reason.

## Renovate

**No datasource, and none is needed.** These are frozen artifacts from a
discontinued product line: Apple shipped 10.9's last security update in
2016 and will not ship another. What could go stale is not the bytes but
the claim that 2016-004 is the last one. That claim was already wrong once
(the briefs this project started from said 2016-001) and was corrected by
asking a live guest. `docs/open-questions.md` Q1 records how, so the next
person re-checks rather than inherits.

## Not built

**`all` has never been built.** All seven packages are pinned, fetched and
checksum-verified, and are staged in the order iTunes' own product lists
them, but no guest has been built with them, so "iTunes 12.6.2's five
packages install cleanly in that order on a 10.9.5 guest" is untested.
