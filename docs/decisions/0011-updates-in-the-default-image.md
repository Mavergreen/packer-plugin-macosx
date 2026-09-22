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
