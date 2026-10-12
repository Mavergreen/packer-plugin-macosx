#!/usr/bin/env bats
# platform: host-agnostic
#
# .github/renovate.json's Aqua Transport pin. The download's URL is
# overwritten in place, so Renovate reads the observer's branch
# (aquatransport-observer.yml) and moves the version and SHA-256 together.
# A drifted pattern or datasource silently stops the pin from moving, and a
# lost automerge: false lands an unbooted Security.framework patch unseen.

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
}

# pyget WHAT: print one field of renovate.json. WHAT is a fixed key:
# manager.<field>, rule.<field>, or datasource.<field>. A missing manager or
# rule fails with a readable message, not a traceback.
pyget() {
    python3 -I -c '
import json, sys
c = json.load(open(sys.argv[1]))
what, field = sys.argv[2].split(".", 1)
dep = "Wowfunhappy/AquaTransport"
if what == "manager":
    found = [x for x in c.get("customManagers", []) if x.get("depNameTemplate") == dep]
elif what == "rule":
    found = [x for x in c.get("packageRules", []) if dep in x.get("matchDepNames", [])]
elif what == "datasource":
    found = [c.get("customDatasources", {}).get("aquatransport", {})] if "aquatransport" in c.get("customDatasources", {}) else []
else:
    sys.exit("pyget: unknown selector " + what)
if not found:
    sys.exit("renovate.json has no " + what + " for " + dep)
if field not in found[0]:
    sys.exit("the " + what + " for " + dep + " has no field " + field)
print(found[0][field])
' "$REPO/.github/renovate.json" "$1"
}

@test "the manager reads the template's pin file" {
    [ "$(pyget manager.managerFilePatterns | python3 -I -c 'import ast,sys;print(ast.literal_eval(sys.stdin.read())[0])')" = '/^templates/mavericks-aquatransport/aquatransport\.pkr\.hcl$/' ]
}

@test "the manager uses the observer's datasource, name and versioning" {
    [ "$(pyget manager.datasourceTemplate)" = "custom.aquatransport" ]
    [ "$(pyget manager.versioningTemplate)" = 'regex:^(?<major>\d{4})\.(?<minor>\d{2})\.(?<patch>\d{2})$' ]
}

@test "the datasource is the observer's branch, exactly" {
    [ "$(pyget datasource.defaultRegistryUrlTemplate)" = "https://raw.githubusercontent.com/Mavergreen/packer-plugin-macosx/aquatransport-observed/aquatransport.json" ]
    [ "$(pyget datasource.format)" = "json" ]
}

@test "the regex captures currentValue and currentDigest, and matches the real pin" {
    # Python's re proves the pattern's captures against the real pin file;
    # RE2 (Renovate's engine) acceptance is proved by the local Renovate
    # lookup recorded in .superpowers/sdd/plan-1-plugin/task-6-report.md.
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
    [ "$(pyget rule.automerge)" = "False" ]
    [ -n "$(pyget rule.description)" ]
}

@test "the manager says why it exists" {
    [ -n "$(pyget manager.description)" ]
}

@test "repackage-on-ingredient-bump watches the pin file" {
    grep -qx '      - templates/mavericks-aquatransport/aquatransport.pkr.hcl' \
        "$REPO/.github/workflows/repackage-on-ingredient-bump.yml"
}
