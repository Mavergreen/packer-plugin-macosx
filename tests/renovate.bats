#!/usr/bin/env bats
#
# .github/renovate.json's Aqua Transport pin. The download's URL is
# overwritten in place, so Renovate reads the observer's branch
# (aquatransport-observer.yml) and moves the version and SHA-256 together.
# A drifted pattern or datasource silently stops the pin from moving, and a
# lost automerge: false lands an unbooted Security.framework patch unseen.

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
}

# pyget EXPR: evaluate a Python expression over c (the renovate.json) and
# print it.
pyget() {
    python3 -I -c '
import json, sys
c = json.load(open(sys.argv[1]))
m = [x for x in c["customManagers"] if x.get("depNameTemplate") == "Wowfunhappy/AquaTransport"][0]
r = [x for x in c["packageRules"] if "Wowfunhappy/AquaTransport" in x.get("matchDepNames", [])][0]
print(eval(sys.argv[2]))
' "$REPO/.github/renovate.json" "$1"
}

@test "the manager reads the template's pin file" {
    [ "$(pyget 'm["managerFilePatterns"][0]')" = '/^templates/mavericks-aquatransport/aquatransport\.pkr\.hcl$/' ]
}

@test "the manager uses the observer's datasource, name and versioning" {
    [ "$(pyget 'm["datasourceTemplate"]')" = "custom.aquatransport" ]
    [ "$(pyget 'm["versioningTemplate"]')" = 'regex:^(?<major>\d{4})\.(?<minor>\d{2})\.(?<patch>\d{2})$' ]
}

@test "the datasource is the observer's branch, exactly" {
    [ "$(pyget 'c["customDatasources"]["aquatransport"]["defaultRegistryUrlTemplate"]')" = "https://raw.githubusercontent.com/Mavergreen/packer-plugin-macosx/aquatransport-observed/aquatransport.json" ]
}

@test "the regex captures currentValue and currentDigest, and matches the real pin" {
    run python3 -I -c '
import json, re, sys
c = json.load(open(sys.argv[1]))
m = [x for x in c["customManagers"] if x.get("depNameTemplate") == "Wowfunhappy/AquaTransport"][0]
(s,) = m["matchStrings"]
assert "(?<currentValue>" in s and "(?<currentDigest>[0-9a-f]{64})" in s, s
p = re.sub(r"\(\?<(\w+)>", r"(?P<\1>", s)
t = open(sys.argv[2]).read()
g = re.search(p, t).groupdict()
assert re.fullmatch(r"\d{4}\.\d{2}\.\d{2}", g["currentValue"]), g
assert re.fullmatch(r"[0-9a-f]{64}", g["currentDigest"]), g
' "$REPO/.github/renovate.json" "$REPO/templates/mavericks-aquatransport/aquatransport.pkr.hcl"
    [ "$status" -eq 0 ]
}

@test "Aqua Transport does not automerge, and says why" {
    [ "$(pyget 'r["automerge"]')" = "False" ]
    [ -n "$(pyget 'r["description"]')" ]
}

@test "the manager says why it exists" {
    [ -n "$(pyget 'm["description"]')" ]
}

@test "repackage-on-ingredient-bump watches the pin file" {
    grep -qx '      - templates/mavericks-aquatransport/aquatransport.pkr.hcl' \
        "$REPO/.github/workflows/repackage-on-ingredient-bump.yml"
}
