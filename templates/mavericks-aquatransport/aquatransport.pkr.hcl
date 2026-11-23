# The pin: the Aqua Transport download this template installs, and
# nothing else. Upstream's one URL is overwritten in place with each
# release, so the version and the SHA-256 say which download the pin means,
# and the URL is the copy of it kept by Mavergreen/aquatransport-mirror,
# one release per (version, SHA-256), derived from the two. The data source
# "macosx-download" refuses any other bytes, naming the pinned and the
# served SHA-256.
#
# RENOVATE REWRITES THIS FILE. Its regex manager captures
# aquatransport_version, then aquatransport_sha256 further on, in one
# match, and moves both together; the URL follows them. Keep the one
# locals block, the three names, the version before the SHA-256, and the
# version and SHA-256 plain strings (TestThePinIsWellFormed).

locals {
  aquatransport_version = "2026.11.21"
  aquatransport_url     = "https://github.com/Mavergreen/aquatransport-mirror/releases/download/${local.aquatransport_version}-${substr(local.aquatransport_sha256, 0, 12)}/AquaTransport.dmg"
  aquatransport_sha256  = "9e6624a30d3913f5427df302317f49a1f52a7a675b1a3a0d417e319299b709a7"
}
