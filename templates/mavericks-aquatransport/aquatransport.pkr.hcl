# The pin: the Aqua Transport download this template installs, and
# nothing else. The URL is the only one there is, and it is overwritten in
# place, so the version and the SHA-256 say which download the pin means.
# The data source "macosx-download" refuses any other bytes, naming the
# pinned and the served SHA-256.
#
# RENOVATE REWRITES THIS FILE. Its regex manager captures
# aquatransport_version, then aquatransport_sha256 further on, in one
# match, and moves both together. Keep the one locals block, the three
# names, the version before the SHA-256, and plain strings
# (TestThePinIsWellFormed).

locals {
  aquatransport_version = "2026.10.09"
  aquatransport_url     = "https://mavericksforever.com/downloads/AquaTransport.dmg"
  aquatransport_sha256  = "7b91d3e4531e1f91137dcc050d632db05f577cda3dc8ddd75cad97627685bc36"
}
