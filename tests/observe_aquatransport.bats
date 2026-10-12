#!/usr/bin/env bats
# platform: host-agnostic

# The AquaTransport observer: bin/observe-aquatransport.sh and its workflow. curl and 7z are
# stubs on PATH inside $BATS_TEST_TMPDIR; no test touches the network.

REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
SCRIPT="$REPO/bin/observe-aquatransport.sh"
FIXTURE="$REPO/tests/fixtures/aquatransport/Distribution"
WORKFLOW="$REPO/.github/workflows/aquatransport-observer.yml"

setup() {
    STUBS="$BATS_TEST_TMPDIR/stubs"
    mkdir -p "$STUBS"
    export STUB_DISTRIBUTION="$FIXTURE"
    export STUB_DMG_PKG=yes
    export STUB_BODY="$BATS_TEST_TMPDIR/body"
    head -c 2097152 /dev/zero > "$STUB_BODY"
    cat > "$STUBS/curl" <<'STUB'
#!/bin/sh
out=
while [ $# -gt 0 ]; do
    case "$1" in
    -o) out=$2; shift ;;
    esac
    shift
done
cp "$STUB_BODY" "$out"
STUB
    cat > "$STUBS/7z" <<'STUB'
#!/bin/sh
cmd=$1; shift
outdir=; file=
for a in "$@"; do
    case "$a" in
    -o*) outdir=${a#-o} ;;
    -*) ;;
    *) [ -n "$file" ] || file=$a ;;
    esac
done
case "$cmd:$file" in
l:*.dmg)
    head -c 5 "$file" | grep -q '<html' && exit 2
    echo "AquaTransport/Readme.rtf"
    [ "$STUB_DMG_PKG" = yes ] && echo "AquaTransport/AquaTransport.pkg"
    exit 0 ;;
x:*.dmg)
    mkdir -p "$outdir/AquaTransport"
    : > "$outdir/AquaTransport/AquaTransport.pkg"
    exit 0 ;;
x:*.pkg)
    cp "$STUB_DISTRIBUTION" "$outdir/Distribution"
    exit 0 ;;
esac
exit 2
STUB
    chmod +x "$STUBS/curl" "$STUBS/7z"
    export PATH="$STUBS:$PATH"
    export AQUATRANSPORT_URL=https://example.invalid/AquaTransport.dmg
}

sha_of_body() { sha256sum "$STUB_BODY" | cut -d' ' -f1; }

@test "aquatransport_version prints the Wowfunhappy.AquaTransport pkg-ref's version, not another's" {
    OBSERVE_AQUATRANSPORT_LIB=1 run sh -c '. "$1"; aquatransport_version "$2"' sh "$SCRIPT" "$FIXTURE"
    [ "$status" -eq 0 ]
    [ "$output" = "2026.10.09" ]
}

@test "a good download writes exactly the JSON shape, with the body's SHA-256" {
    run "$SCRIPT" "$BATS_TEST_TMPDIR/out.json"
    [ "$status" -eq 0 ]
    run python3 -I -c 'import json,sys; d=json.load(open(sys.argv[1])); print(json.dumps(d, sort_keys=True))' "$BATS_TEST_TMPDIR/out.json"
    [ "$output" = "{\"releases\": [{\"digest\": \"$(sha_of_body)\", \"version\": \"2026.10.09\"}]}" ]
}

@test "an HTML body writes nothing and fails" {
    { echo '<html>'; head -c 2097152 /dev/zero | tr '\0' 'x'; } > "$STUB_BODY"
    run "$SCRIPT" "$BATS_TEST_TMPDIR/out.json"
    [ "$status" -ne 0 ]
    [ ! -e "$BATS_TEST_TMPDIR/out.json" ]
}

@test "an HTTP error (curl -f exits 22) writes nothing and fails" {
    printf '#!/bin/sh\nexit 22\n' > "$STUBS/curl"
    run "$SCRIPT" "$BATS_TEST_TMPDIR/out.json"
    [ "$status" -ne 0 ]
    [ ! -e "$BATS_TEST_TMPDIR/out.json" ]
}

@test "observe mode rejects extra arguments" {
    run "$SCRIPT" "$BATS_TEST_TMPDIR/out.json" extra
    [ "$status" -ne 0 ]
    [ ! -e "$BATS_TEST_TMPDIR/out.json" ]
}

@test "a 10-byte body writes nothing and fails" {
    printf '0123456789' > "$STUB_BODY"
    run "$SCRIPT" "$BATS_TEST_TMPDIR/out.json"
    [ "$status" -ne 0 ]
    [ ! -e "$BATS_TEST_TMPDIR/out.json" ]
}

@test "a DMG without AquaTransport.pkg writes nothing and fails" {
    STUB_DMG_PKG=no run "$SCRIPT" "$BATS_TEST_TMPDIR/out.json"
    [ "$status" -ne 0 ]
    [ ! -e "$BATS_TEST_TMPDIR/out.json" ]
}

@test "a Distribution without the pkg-ref writes nothing and fails" {
    grep -v 'id="Wowfunhappy.AquaTransport" version' "$FIXTURE" > "$BATS_TEST_TMPDIR/d"
    STUB_DISTRIBUTION="$BATS_TEST_TMPDIR/d" run "$SCRIPT" "$BATS_TEST_TMPDIR/out.json"
    [ "$status" -ne 0 ]
    [ ! -e "$BATS_TEST_TMPDIR/out.json" ]
}

@test "a version like latest writes nothing and fails" {
    sed 's/version="2026.10.09"/version="latest"/' "$FIXTURE" > "$BATS_TEST_TMPDIR/d"
    STUB_DISTRIBUTION="$BATS_TEST_TMPDIR/d" run "$SCRIPT" "$BATS_TEST_TMPDIR/out.json"
    [ "$status" -ne 0 ]
    [ ! -e "$BATS_TEST_TMPDIR/out.json" ]
}

# --merge EXISTING.json NEW.json OUT.json

merge_versions() {
    python3 -I -c 'import json,sys; print(" ".join(r["version"]+"="+r["digest"] for r in json.load(open(sys.argv[1]))["releases"]))' "$1"
}

@test "--merge appends a new version, sorted ascending" {
    echo '{"releases":[{"version":"2026.10.09","digest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},{"version":"2026.08.01","digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}' > "$BATS_TEST_TMPDIR/e.json"
    echo '{"releases":[{"version":"2026.11.02","digest":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}]}' > "$BATS_TEST_TMPDIR/n.json"
    run "$SCRIPT" --merge "$BATS_TEST_TMPDIR/e.json" "$BATS_TEST_TMPDIR/n.json" "$BATS_TEST_TMPDIR/o.json"
    [ "$status" -eq 0 ]
    [ "$(merge_versions "$BATS_TEST_TMPDIR/o.json")" = "2026.08.01=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 2026.10.09=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb 2026.11.02=cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc" ]
}

@test "--merge replaces the same version's digest" {
    echo '{"releases":[{"version":"2026.10.09","digest":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"},{"version":"2026.08.01","digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}' > "$BATS_TEST_TMPDIR/e.json"
    echo '{"releases":[{"version":"2026.10.09","digest":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}]}' > "$BATS_TEST_TMPDIR/n.json"
    run "$SCRIPT" --merge "$BATS_TEST_TMPDIR/e.json" "$BATS_TEST_TMPDIR/n.json" "$BATS_TEST_TMPDIR/o.json"
    [ "$status" -eq 0 ]
    [ "$(merge_versions "$BATS_TEST_TMPDIR/o.json")" = "2026.08.01=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 2026.10.09=eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee" ]
}

@test "--merge with an empty or missing existing file is just the new entry" {
    echo '{"releases":[{"version":"2026.10.09","digest":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}]}' > "$BATS_TEST_TMPDIR/n.json"
    : > "$BATS_TEST_TMPDIR/empty.json"
    run "$SCRIPT" --merge "$BATS_TEST_TMPDIR/empty.json" "$BATS_TEST_TMPDIR/n.json" "$BATS_TEST_TMPDIR/o1.json"
    [ "$status" -eq 0 ]
    [ "$(merge_versions "$BATS_TEST_TMPDIR/o1.json")" = "2026.10.09=eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee" ]
    run "$SCRIPT" --merge "$BATS_TEST_TMPDIR/absent.json" "$BATS_TEST_TMPDIR/n.json" "$BATS_TEST_TMPDIR/o2.json"
    [ "$status" -eq 0 ]
    [ "$(merge_versions "$BATS_TEST_TMPDIR/o2.json")" = "2026.10.09=eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee" ]
}

@test "--merge with malformed existing JSON fails and writes nothing" {
    echo '{"releases":[' > "$BATS_TEST_TMPDIR/e.json"
    echo '{"releases":[{"version":"2026.10.09","digest":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}]}' > "$BATS_TEST_TMPDIR/n.json"
    run "$SCRIPT" --merge "$BATS_TEST_TMPDIR/e.json" "$BATS_TEST_TMPDIR/n.json" "$BATS_TEST_TMPDIR/o.json"
    [ "$status" -ne 0 ]
    [ ! -e "$BATS_TEST_TMPDIR/o.json" ]
}

@test "--merge refuses an invalid NEW and writes nothing" {
    good="$(printf 'e%.0s' $(seq 64))"
    echo '{"releases":[]}' > "$BATS_TEST_TMPDIR/e.json"
    n=0
    for bad in '{}' \
        "{\"releases\":[{\"version\":\"latest\",\"digest\":\"$good\"}]}" \
        '{"releases":[{"version":"2026.10.09","digest":5}]}' \
        '{"releases":[{"version":"2026.10.09","digest":"xyz"}]}'; do
        n=$((n+1))
        echo "$bad" > "$BATS_TEST_TMPDIR/n.json"
        run "$SCRIPT" --merge "$BATS_TEST_TMPDIR/e.json" "$BATS_TEST_TMPDIR/n.json" "$BATS_TEST_TMPDIR/o$n.json"
        [ "$status" -ne 0 ]
        [ ! -e "$BATS_TEST_TMPDIR/o$n.json" ]
    done
}

# The workflow

wf() {
    python3 -I -c '
import sys, yaml
w = yaml.safe_load(open(sys.argv[1]))
exec(sys.argv[2])
' "$WORKFLOW" "$1"
}

@test "workflow: top-level permissions are empty, observe reads only, publish alone writes" {
    run wf '
assert w["permissions"] == {}, w["permissions"]
assert w["jobs"]["observe"]["permissions"] == {"contents": "read"}
assert w["jobs"]["publish"]["permissions"] == {"contents": "write"}
assert w["jobs"]["publish"]["needs"] == "observe"
assert [j for j, v in w["jobs"].items() if "write" in v.get("permissions", {}).values()] == ["publish"]
print("ok")'
    [ "$status" -eq 0 ]
    [ "$output" = ok ]
}

@test "workflow: publish never checks out or pushes main" {
    run wf '
for step in w["jobs"]["publish"]["steps"]:
    ref = str(step.get("with", {}).get("ref", ""))
    assert ref != "main" and "refs/heads/main" not in ref, step
    run = step.get("run", "")
    for line in run.splitlines():
        assert not ("git push" in line and "main" in line.split("git push", 1)[1].split()[:3] ), line
print("ok")'
    [ "$status" -eq 0 ]
    [ "$output" = ok ]
    run grep -nE 'git (push|checkout|switch)[^#]*[ :]main\b' "$WORKFLOW"
    [ "$status" -ne 0 ]
}

@test "workflow: triggers are one daily cron and workflow_dispatch" {
    run wf '
on = w.get("on", w.get(True))
assert "workflow_dispatch" in on
cron = [c["cron"] for c in on["schedule"]]
assert len(cron) == 1 and cron[0].split()[2:] == ["*", "*", "*"], cron
print("ok")'
    [ "$status" -eq 0 ]
    [ "$output" = ok ]
}

@test "workflow: publish runs nothing from the downloaded artifact and checks out only github.sha" {
    run wf '
steps = w["jobs"]["publish"]["steps"]
for s in steps:
    run = s.get("run", "")
    for line in run.splitlines():
        import re
        assert not re.search(r"(\bsh|\bbash|\bsource|\bpython3?( -\w+)*|^\s*\.|\./)\s+\S*observed/\S*\.(sh|py)", line), line
    if "checkout" in s.get("uses", ""):
        assert s["with"]["ref"] == "${{ github.sha }}" and s["with"]["persist-credentials"] is False, s
assert any("checkout" in s.get("uses", "") for s in steps)
print("ok")'
    [ "$status" -eq 0 ]
    [ "$output" = ok ]
}

@test "workflow: both jobs have a timeout, and the download's curl has a --max-time" {
    run wf '
for j in ("observe", "publish"):
    t = w["jobs"][j].get("timeout-minutes")
    assert isinstance(t, int) and 0 < t <= 30, (j, t)
print("ok")'
    [ "$status" -eq 0 ]
    [ "$output" = ok ]
    run grep -nE 'curl .*--max-time [0-9]+' "$REPO/bin/observe-aquatransport.sh"
    [ "$status" -eq 0 ]
}

@test "workflow: GH_TOKEN is only in the step that pushes; the merge step never has it" {
    run wf '
import re
steps = w["jobs"]["publish"]["steps"]
assert "GH_TOKEN" not in str(w["jobs"]["publish"].get("env", {})) and "env" not in w
merge = [s for s in steps if "--merge" in s.get("run", "")]
assert len(merge) == 1, merge
m = merge[0]
assert "GH_TOKEN" not in str(m.get("env", {})) and "GH_TOKEN" not in m["run"] and "github.token" not in str(m) and "secrets." not in str(m), m
assert "git push" not in m["run"] and "extraheader" not in m["run"], m
holders = [s for s in steps if "GH_TOKEN" in str(s.get("env", {}))]
assert len(holders) == 1 and "git" in holders[0]["run"] and "push" in holders[0]["run"], holders
assert steps.index(holders[0]) > steps.index(m)
assert "--merge" not in holders[0]["run"]
print("ok")'
    [ "$status" -eq 0 ]
    [ "$output" = ok ]
}
