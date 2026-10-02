# Vagrant's insecure RSA keypair

Copied byte for byte from the Vagrant 2.4.9 gem, at
`/opt/vagrant/embedded/gems/gems/vagrant-2.4.9/keys/`:

| File here | Source file |
|---|---|
| `vagrant.pub.rsa` | `keys/vagrant.pub.rsa` |
| `vagrant-standard-insecure-first-boot-only.key.rsa` | `keys/vagrant.key.rsa` |

## What these are

HashiCorp publishes this RSA keypair for base-box creators to embed as a
new box's default authorized key. Vagrant's own
`keys/README.md` (same source directory) says so directly: "These keys
are the 'insecure' public/private keypair we offer to base box creators
for use in their base boxes so that vagrant installations can
automatically SSH into the boxes." `vagrant up` replaces this key with a
freshly generated one on the box's first boot, so it is meant to be
public and is not a secret to protect -- the same reasoning that lets it
travel inside the Vagrant gem itself and inside every other public
Vagrant base box.

That is also why `TestNoEmbeddedAssetCarriesAPublicKey` and
`TestFirstbootShNeverEmbedsASecret` (`internal/payload/payload_test.go`)
are scoped away from this directory: the guarantees those tests hold
everywhere else in the embedded tree -- no embedded public key, no
embedded private key or hardcoded password -- are exactly the two
properties this directory exists to violate, on purpose, for a keypair
whose entire point is to be public.

## Licence

Vagrant itself (2.4.3 and later) is HashiCorp's Business Source License
(`/opt/vagrant/embedded/gems/gems/vagrant-2.4.9/LICENSE`), which
restricts competing hosted offerings of Vagrant, not use of a plugin
like this one, and says nothing about these two files specifically.
The keypair's own accompanying text, quoted above, is what actually
governs them: an explicit invitation to base-box creators to copy them
in, which is exactly what this directory does.

## Used by

- `payload.VagrantDefaults()` uses `vagrant.pub.rsa` only, as the
  default authorized key -- not the RSA+Ed25519 pair Vagrant's own
  `vagrant.pub` combines, because `payload.validateKey` refuses an
  Ed25519 line outright when the guest has no replacement OpenSSH
  installed (stock OS X 10.9's OpenSSH 6.2 cannot parse one), which is
  `VagrantDefaults`'s own default (`OpenSSHPkgs` is empty).
- `payload.VagrantPrivateKey()` returns
  `vagrant-standard-insecure-first-boot-only.key.rsa`, the matching
  private half, for the build's own SSH communicator to log in with.
