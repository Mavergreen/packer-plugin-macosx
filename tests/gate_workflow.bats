#!/usr/bin/env bats
# platform: host-agnostic
# shellcheck disable=SC2016  # the single-quoted text is Python, and ${{ }} in it is GitHub's
#
# .github/workflows/gate.yml runs the boot gate in CI and reports the one required check `gate`;
# gate/scope.tsv says which ingredient paths it gates. These read the files only (PyYAML, which
# shipyard's install action provides, as the conventions gate needs it), so they need no stubs and
# are exempt from the stub-safety assertion (R9).

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
}

# py <python>: run it with `gate` (gate.yml), `jobs` (its jobs), `on` (its triggers), `release`
# (release.yml), `ci` (ci.yml), `repackage` (repackage-on-ingredient-bump.yml) and `steps(job)`;
# an assert that fails prints what it saw.
py() {
    python3 -I -c '
import sys, yaml, re
root = sys.argv[1]
def load(name):
    with open(root + "/.github/workflows/" + name) as f:
        return yaml.safe_load(f)
gate = load("gate.yml")
release = load("release.yml")
ci = load("ci.yml")
repackage = load("repackage-on-ingredient-bump.yml")
jobs = gate["jobs"]
on = gate.get("on", gate.get(True))
def steps(job):
    return jobs[job].get("steps", [])
def uses(job, action):
    return [s for s in steps(job) if s.get("uses", "").startswith(action + "@")]
def runs(job):
    return "\n".join(s.get("run", "") for s in steps(job))
exec(sys.argv[2])
' "$REPO" "$1"
}

@test "the final job is named gate, runs always, and waits for every other job" {
    py '
g = [j for j, v in jobs.items() if v.get("name") == "gate"]
assert g == ["gate"], g
assert jobs["gate"]["if"] == "always()", jobs["gate"].get("if")
others = sorted(j for j in jobs if j != "gate")
assert sorted(jobs["gate"]["needs"]) == others, jobs["gate"]["needs"]
'
}

@test "the jobs are scope, candidate, candidate-retry, control and gate" {
    py 'assert sorted(jobs) == ["candidate", "candidate-retry", "control", "gate", "scope"], sorted(jobs)'
}

@test "candidate, its retry and the control each run bin/gate-ci.sh run, under 240 minutes" {
    py '
for j in ["candidate", "candidate-retry", "control"]:
    assert "bin/gate-ci.sh run " in runs(j), (j, runs(j))
    assert jobs[j]["timeout-minutes"] == 240, (j, jobs[j].get("timeout-minutes"))
for j in ["scope", "gate"]:
    assert "gate-ci.sh run" not in runs(j), j
'
}

@test "the retry runs once, only when the candidate could not run, and nothing retries it" {
    py '
assert jobs["candidate"]["if"].strip() == "needs.scope.outputs.decision == '"'"'gated'"'"'", jobs["candidate"]["if"]
r = jobs["candidate-retry"]["if"]
assert "needs.candidate.outputs.exit == '"'"'3'"'"'" in r, r
assert "!cancelled()" in r, r
# a cancelled or timed-out candidate also exits 3 (gate.sh traps the signal): never retried
assert "needs.candidate.result == '"'"'success'"'"' && needs.candidate.outputs.exit == '"'"'3'"'"'" in r, r
for j, v in jobs.items():
    assert "candidate-retry.outputs.exit == '"'"'3'"'"'" not in str(v.get("if", "")), j
'
}

@test "the control runs only when the effective candidate failed a check" {
    py '
c = jobs["control"]["if"]
assert "needs.candidate.outputs.exit == '"'"'1'"'"'" in c, c
assert "needs.candidate-retry.outputs.exit == '"'"'1'"'"'" in c, c
assert "needs.candidate-retry.result == '"'"'skipped'"'"'" in c, c
assert "!cancelled()" in c, c
# each branch needs the job it reads to have finished, not been cancelled or timed out
assert "needs.candidate-retry.result == '"'"'skipped'"'"' && needs.candidate.result == '"'"'success'"'"' && needs.candidate.outputs.exit == '"'"'1'"'"'" in c, c
assert "needs.candidate-retry.result == '"'"'success'"'"' && needs.candidate-retry.outputs.exit == '"'"'1'"'"'" in c, c
'
}

@test "the control checks out its ref beside the candidate's tree and runs that ref's gate" {
    py '
co = uses("control", "actions/checkout")
refs = [s.get("with", {}).get("ref") for s in co]
assert "${{ needs.scope.outputs.control_ref }}" in refs, refs
ctl = [s for s in co if s.get("with", {}).get("ref")][0]
assert ctl["with"]["path"] == "control", ctl
assert "--checkout control " in runs("control"), runs("control")
gos = uses("control", "actions/setup-go")
assert gos and gos[0]["with"]["go-version-file"] == "control/go.mod", gos
'
}

@test "every checkout has the whole history (verify-changed-sources needs a base)" {
    py '
for j in jobs:
    co = uses(j, "actions/checkout")
    assert co, j
    for s in co:
        assert s.get("with", {}).get("fetch-depth") == 0, (j, s)
'
}

@test "the gate jobs set up go, packer 1.16.2 as ci.yml does, shipyard and the host" {
    py '
civ = [s["with"]["version"] for s in ci["jobs"]["template"]["steps"] if s.get("uses", "").startswith("hashicorp/setup-packer@")]
cia = [s["uses"] for s in ci["jobs"]["template"]["steps"] if s.get("uses", "").startswith("hashicorp/setup-packer@")]
for j in ["candidate", "candidate-retry", "control"]:
    p = uses(j, "hashicorp/setup-packer")
    assert [s["uses"] for s in p] == cia and [s["with"]["version"] for s in p] == civ == ["1.16.2"], (j, p)
    assert uses(j, "actions/setup-go"), j
    assert uses(j, "Mavergreen/shipyard/.github/actions/install"), j
    hs = [s for s in steps(j) if s.get("run", "").strip() == "bin/gate-host-setup.sh"]
    assert len(hs) == 1 and hs[0].get("continue-on-error") is True and hs[0].get("id") == "host", (j, hs)
    assert "--setup ${{ steps.host.outcome }}" in runs(j), j
    assert "sudo mkdir -p /mnt/gate" in runs(j), j
assert uses("scope", "Mavergreen/shipyard/.github/actions/install"), "scope"
'
}

@test "the gate step has the API token for packer plugins install" {
    py '
for j in ["candidate", "candidate-retry", "control"]:
    s = [s for s in steps(j) if "gate-ci.sh run" in s.get("run", "")][0]
    assert s["env"]["PACKER_GITHUB_API_TOKEN"] == "${{ github.token }}", (j, s)
'
}

@test "artifacts: only the evidence dir, always, 30 days, distinct names none of release.yml's" {
    py '
names = []
for j in jobs:
    for s in uses(j, "actions/upload-artifact"):
        w = s["with"]
        assert w["path"] == "${{ runner.temp }}/gate-evidence/", (j, w)
        assert s["if"] == "always()", (j, s)
        assert w["retention-days"] == 30, (j, w)
        names.append(w["name"])
assert sorted(names) == ["gate-candidate", "gate-candidate-retry", "gate-control"], names
assert len(set(names)) == len(names), names
rel = set(re.findall(r"name: *(\S+)", open(sys.argv[1] + "/.github/workflows/release.yml").read()))
assert not rel & set(names), rel & set(names)
'
}

@test "the evidence dir is never the work dir, nor under it" {
    py '
for j in ["candidate", "candidate-retry", "control"]:
    r = runs(j)
    assert "--work-dir /mnt/gate " in r and "--evidence-dir \"$RUNNER_TEMP/gate-evidence\"" in r, (j, r)
'
}

@test "download-artifact is the family's major, alongside upload-artifact's" {
    py '
d = uses("gate", "actions/download-artifact")
assert {s["uses"] for s in d} == {"actions/download-artifact@v8"}, d
u = {s["uses"] for j in jobs for s in uses(j, "actions/upload-artifact")}
assert u == {"actions/upload-artifact@v7"}, u
'
}

# By name, each into its own dir: a pattern that matches one artifact extracts it flat into `path`,
# not into path/<name>, so a run whose only artifact was gate-candidate read "no steps.tsv" and failed
# a candidate that had passed every step (2026-11-19, run 44601148068).
@test "each evidence artifact downloads by name into the dir the verdict reads, gated, never failing the job" {
    py '
d = uses("gate", "actions/download-artifact")
got = {s["with"]["name"]: s["with"]["path"] for s in d}
want = {n: "${{ runner.temp }}/gate-artifacts/" + n for n in ["gate-candidate", "gate-candidate-retry", "gate-control"]}
assert got == want, got
for s in d:
    assert "pattern" not in s["with"] and "merge-multiple" not in s["with"], s
    assert s["if"] == "needs.scope.outputs.decision == \x27gated\x27", s
    assert s["continue-on-error"] is True, s
r = runs("gate")
for n in want:
    assert "\"$a/" + n + "\"" in r, (n, r)
'
}

@test "the final job decides through bin/gate-ci.sh verdict, handing it every job's result" {
    py '
r = runs("gate")
assert "bin/gate-ci.sh verdict" in r, r
for opt in ["--decision", "--reason", "--scope-job", "--candidate ", "--candidate-job", "--candidate-exit",
            "--retry ", "--retry-job", "--retry-exit", "--control "]:
    assert opt in r, opt
assert "GITHUB_STEP_SUMMARY" in r, r
'
}

@test "triggers: PRs to main with no paths filter, a dispatch with force, a call with caller and control-ref" {
    py '
assert on["pull_request"] == {"branches": ["main"]}, on["pull_request"]
f = on["workflow_dispatch"]["inputs"]["force"]
assert f["type"] == "boolean" and f["default"] is False, f
c = on["workflow_call"]["inputs"]
assert c["caller"]["default"] == "release" and c["caller"]["type"] == "string", c
assert c["control-ref"]["required"] is False, c
assert set(on) == {"pull_request", "workflow_dispatch", "workflow_call"}, set(on)
'
}

@test "scope learns a call from the caller input, never from github.event_name alone (R3)" {
    py '
s = [s for s in steps("scope") if "bin/gate-scope.sh" in s.get("run", "")][0]
e = s["env"]["GATE_EVENT"]
assert e == "${{ inputs.caller != '"''"' && '"'"'workflow_call'"'"' || github.event_name }}", e
assert s["env"]["GATE_FORCE"] == "${{ inputs.force }}", s["env"]
'
}

@test "permissions are contents: read alone" {
    py '
assert gate["permissions"] == {"contents": "read"}, gate["permissions"]
for j, v in jobs.items():
    assert "permissions" not in v, j
'
}

@test "concurrency: none at the top; the gate runs keyed gate-, cancelling only for pull_request (R4)" {
    py '
assert "concurrency" not in gate
for j in ["candidate", "candidate-retry", "control"]:
    c = jobs[j]["concurrency"]
    assert c["group"].startswith("gate-"), (j, c)
    assert "github.event_name == '"'"'pull_request'"'"'" in c["group"] and "github.run_id" in c["group"], (j, c)
    assert c["cancel-in-progress"] == "${{ github.event_name == '"'"'pull_request'"'"' }}", (j, c)
'
}

@test "no job in release.yml is named or keyed gate (the required-check name trap)" {
    py '
for j, v in release["jobs"].items():
    assert j != "gate" and v.get("name") != "gate", j
'
}

@test "the scope rows are exactly repackage-on-ingredient-bump.yml's paths" {
    py '
want = sorted(repackage[True]["push"]["paths"] if True in repackage else repackage["on"]["push"]["paths"])
got = sorted(l.split("\t")[0] for l in open(sys.argv[1] + "/gate/scope.tsv").read().splitlines() if l and not l.startswith("#"))
assert got == want, (got, want)
'
}

@test "every scope row is path, gate or skip, and a reason; every skip says why" {
    while IFS= read -r line; do
        case $line in '#'*|'') continue ;; esac
        IFS=$'\t' read -r path what reason <<< "$line"
        [ -n "$path" ]
        [ "$what" = gate ] || [ "$what" = skip ]
        if [ "$what" = skip ]; then [ -n "$reason" ]; fi
        [ "$(printf '%s' "$line" | awk -F'\t' '{ print NF }')" -eq 3 ]
    done < "$REPO/gate/scope.tsv"
}

@test "the scope rows gate the boot stack and Aqua Transport, and skip OpenSSH and 10.6, saying why" {
    s="$REPO/gate/scope.tsv"
    grep -qx "assets/pins/sources.tsv	gate	.*" "$s"
    grep -qx "assets/firmware/config.plist	gate	.*" "$s"
    grep -qx "templates/mavericks-aquatransport/aquatransport.pkr.hcl	gate	.*" "$s"
    grep -qxF "components/**	skip	OpenSSH has its own on-VM smoke test before it releases; the owner, 2026-10-12" "$s"
    grep -qxF "assets/firmware/snowleopard/config.plist	skip	10.6 is gated by hand: a boot-stack change gets a local bin/gate.sh --release 10.6 --installer <disc> before merge (the owner's rulings R52, R59); CI has no 10.6 disc" "$s"
}

@test "renovate's Packer manager reads gate.yml as well as ci.yml" {
    python3 -I -c '
import json, re, sys
c = json.load(open(sys.argv[1]))
m = [x for x in c["customManagers"] if x.get("depNameTemplate") == "hashicorp/packer"]
assert len(m) == 1, m
pats = m[0]["managerFilePatterns"]
assert len(pats) == 1, pats
rx = re.compile(pats[0].strip("/"))
for f in [".github/workflows/ci.yml", ".github/workflows/gate.yml"]:
    assert rx.search(f), f
assert not rx.search(".github/workflows/release.yml")
' "$REPO/.github/renovate.json"
}

@test "ci.yml's header no longer says CI never builds an image, and points to gate.yml" {
    run grep -n "does NOT check: that an image builds" "$REPO/.github/workflows/ci.yml"
    [ "$status" -eq 1 ]
    head -30 "$REPO/.github/workflows/ci.yml" | grep -q "gate.yml"
}

@test "candidate, its retry and the control run the same steps, but for what each must differ in" {
    py '
import json
noref = "needs.scope.outputs.control_ref != " + chr(39) * 2
def norm(job):
    out = []
    for st in steps(job):
        st = json.loads(json.dumps(st))
        w = st.get("with", {})
        if job == "control":
            if w.get("path") == "control":
                continue                          # the control ref, beside this tree
            if st.get("if") == noref:
                del st["if"]                      # no control ref: nothing to ready
            elif st.get("if", "").startswith(noref + " && "):
                st["if"] = st["if"][len(noref + " && "):]
            if w.get("go-version-file") == "control/go.mod":
                w["go-version-file"] = "go.mod"
        if "name" in w and w["name"].startswith("gate-"):
            w["name"] = "ARTIFACT"
        if "run" in st:
            st["run"] = re.sub(r"--checkout \S+", "--checkout DIR", st["run"])
        out.append(st)
    return out
a, b, c = norm("candidate"), norm("candidate-retry"), norm("control")
assert a == b, (a, b)
assert a == c, (a, c)
'
}
