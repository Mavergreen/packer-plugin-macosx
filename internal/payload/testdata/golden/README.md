# Golden files for internal/payload

| File | What it is | Test |
|---|---|---|
| `reference.pkg.gz` | a flat package (xar) with one `Scripts` member, `postinstall` holding `#!/bin/sh\necho hello\nexit 0\n`, identifier `com.mqg.firstboot`, version `1.0`, written independently of `flatPackage`, then gzip'd (`gzip -9`) so the tracked blob does not start with a flat package's own "xar!" magic (bin/no-apple-bytes.sh's byte check) | `TestSameContentsAsTheReferencePackage` (`container_test.go`) |
| `fixed-rsa.pub`, `fixed-ed25519.pub` | one RSA and one Ed25519 public key, generated once and kept: a golden comparison needs the same key every run | the three below |
| `postinstall-defaults.golden` | the postinstall assembled from `DefaultConfig()` and `fixed-rsa.pub` | `TestConfDefaultsMatchThePostinstallGoldenByteForByte` (`payload_test.go`) |
| `postinstall-openssh-updates.golden` | the same, with `fixed-ed25519.pub`, the test's two fixed-content OpenSSH packages at tag `10.5p1-mavericks.2`, and one staged update, as for updates `security` | `TestConfWithOpenSSHAndUpdatesMatchesThePostinstallGolden` (`payload_test.go`) |
| `postinstall-password.golden` | the defaults, with the password `hunter2` | `TestConfWithPasswordMatchesThePostinstallGolden` (`payload_test.go`) |

`TestSameContentsAsTheReferencePackage` compares structure, not bytes:
the same TOC (bar the Scripts member's compressed length and
checksums), the same PackageInfo, the same decompressed cpio. Two
deflate implementations need not agree on the compressed bytes.

## When one changes

The three `postinstall-*.golden` files are the whole
`assets/guest/postinstall` template with the generated `firstboot.conf`,
`assets/guest/firstboot.sh` and `assets/guest/com.mqg.firstboot.plist`
substituted into it as heredocs (`payload.Postinstall`), so a deliberate
change to any of those changes the goldens too:

1. Rewrite them from what the code now produces:
   `go test ./internal/payload -run PostinstallGolden -update`.
2. Review `git diff internal/payload/testdata/golden/`: the only change
   must be the deliberate one, carried into each golden verbatim. A
   change anywhere else (the heredoc framing, a line of the conf nobody
   meant to touch) is a regression, not a golden to accept.
3. Commit the goldens in the same commit as the change, and say in its
   message which change they carry.

A change to these guest assets also changes the media data source's
input listing (their `payload:` rows), so every cached media is rebuilt:
not a change to make in passing.

`reference.pkg.gz` and the two keys never change: a failing
`TestSameContentsAsTheReferencePackage` is `flatPackage` drifting from
the flat-package format.
