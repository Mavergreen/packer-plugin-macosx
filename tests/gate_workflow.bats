#!/usr/bin/env bats
# platform: host-agnostic
# shellcheck disable=SC2016  # the single-quoted text is Python, and ${{ }} in it is GitHub's
#
# .github/workflows/gate.yml runs the boot gate in CI and reports the one required check `gate`;
# .github/workflows/gate-release.yml is one release's legs (candidate, its retry, the control), which
# gate.yml runs once per release a change gates; gate/scope.tsv says which ingredient paths it
# gates. These read the files only (PyYAML, which shipyard's install action provides, as the
# conventions gate needs it), so they need no stubs and are exempt from the stub-safety assertion
# (R9).

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
}

# py <python>: run it with `gate` (gate.yml), `jobs` (its jobs), `on` (its triggers), `rel`
# (gate-release.yml), `legs` (its jobs), `ron` (its triggers), `release` (release.yml), `ci`
# (ci.yml), `repackage` (repackage-on-ingredient-bump.yml), `job(name)` (a job of either file, whose
# job names do not overlap) and `steps(name)`; an assert that fails prints what it saw.
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
rel = load("gate-release.yml")
jobs = gate["jobs"]
legs = rel["jobs"]
on = gate.get("on", gate.get(True))
ron = rel.get("on", rel.get(True))
LEGS = ["candidate", "candidate-retry", "control"]
assert not set(jobs) & set(legs), set(jobs) & set(legs)
def job(name):
    return jobs[name] if name in jobs else legs[name]
def steps(name):
    return job(name).get("steps", [])
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

@test "the jobs are scope, legs and gate; a release's legs are candidate, candidate-retry and control" {
    py '
assert sorted(jobs) == ["gate", "legs", "scope"], sorted(jobs)
assert sorted(legs) == LEGS, sorted(legs)
'
}

@test "gate-release.yml is only called, with the release, the control ref and the caller" {
    py '
assert set(ron) == {"workflow_call"}, set(ron)
i = ron["workflow_call"]["inputs"]
assert set(i) == {"release", "control_ref", "caller"}, set(i)
assert i["release"]["required"] is True and i["release"]["type"] == "string", i["release"]
assert i["control_ref"]["type"] == "string" and i["control_ref"].get("default") == "", i["control_ref"]
assert i["caller"]["type"] == "string" and i["caller"]["required"] is True, i["caller"]
assert rel["permissions"] == {"contents": "read"}, rel.get("permissions")
'
}

@test "legs runs gate-release.yml once per release scope names, all to the end, only when gated" {
    py '
l = jobs["legs"]
assert l["uses"] == "./.github/workflows/gate-release.yml", l
assert l["needs"] == "scope" or l["needs"] == ["scope"], l["needs"]
assert l["if"] == "needs.scope.outputs.decision == '"'"'gated'"'"'", l.get("if")
st = l["strategy"]
assert st["fail-fast"] is False, st
assert st["matrix"] == {"release": "${{ fromJSON(needs.scope.outputs.releases) }}"}, st["matrix"]
assert l["with"] == {"release": "${{ matrix.release }}", "control_ref": "${{ needs.scope.outputs.control_ref }}",
                     "caller": "${{ needs.scope.outputs.event }}"}, l["with"]
assert "steps" not in l and "runs-on" not in l, l
'
}

@test "scope outputs the releases as a JSON array and a list, both from bin/gate-ci.sh scope-releases" {
    py '
o = jobs["scope"]["outputs"]
for k in ["decision", "reason", "control_ref", "releases", "release_list", "event"]:
    assert o[k] == "${{ steps.scope.outputs." + k + " }}", (k, o)
r = runs("scope")
assert "bin/gate-ci.sh scope-releases" in r, r
assert "fromJSON" not in r and "toJSON" not in r, r
'
}

@test "candidate, its retry and the control each run bin/gate-ci.sh run, under 240 minutes" {
    py '
for j in LEGS:
    assert "bin/gate-ci.sh run " in runs(j), (j, runs(j))
    assert legs[j]["timeout-minutes"] == 240, (j, legs[j].get("timeout-minutes"))
for j in ["scope", "gate"]:
    assert "gate-ci.sh run" not in runs(j), j
'
}

@test "the retry runs once, only when the candidate could not run, and nothing retries it" {
    py '
# legs runs only when gated, so the candidate itself always runs
assert "if" not in legs["candidate"], legs["candidate"].get("if")
r = legs["candidate-retry"]["if"]
assert "needs.candidate.outputs.exit == '"'"'3'"'"'" in r, r
assert "!cancelled()" in r, r
# a cancelled or timed-out candidate also exits 3 (gate.sh traps the signal): never retried
assert "needs.candidate.result == '"'"'success'"'"' && needs.candidate.outputs.exit == '"'"'3'"'"'" in r, r
for j, v in list(jobs.items()) + list(legs.items()):
    assert "candidate-retry.outputs.exit == '"'"'3'"'"'" not in str(v.get("if", "")), j
'
}

@test "the control runs only when the effective candidate failed a check" {
    py '
c = legs["control"]["if"]
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
assert "${{ inputs.control_ref }}" in refs, refs
ctl = [s for s in co if s.get("with", {}).get("ref")][0]
assert ctl["with"]["path"] == "control", ctl
assert "--checkout control " in runs("control"), runs("control")
'
}

# bin/gate-ci.sh fetches 10.6's disc with this checkout's `go run ./cmd/fetch-pinned`, in every
# leg, the control's too: the Go must be the one this checkout's go.mod asks for (a v0.* control's
# go.mod asked for an older one, which refuses this module), and it builds an older ref's plugin too.
@test "every leg sets up the Go this checkout's go.mod names, the control's included" {
    py '
for j in LEGS:
    gos = uses(j, "actions/setup-go")
    assert len(gos) == 1 and gos[0]["with"] == {"go-version-file": "go.mod"}, (j, gos)
'
}

@test "every checkout has the whole history (verify-changed-sources needs a base)" {
    py '
for j in [j for j in list(jobs) + list(legs) if "steps" in job(j)]:
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
for j in LEGS:
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

@test "each gate run names the release it was called for and a media dir outside the work dir" {
    py '
for j in LEGS:
    s = [s for s in steps(j) if "gate-ci.sh run" in s.get("run", "")][0]
    assert "--release ${{ inputs.release }} " in s["run"], (j, s["run"])
    assert "10.9" not in s["run"] and "10.9" not in s.get("name", ""), (j, s)
    assert "--media-dir \"$RUNNER_TEMP/media\" " in s["run"], (j, s["run"])
    assert "--work-dir /mnt/gate " in s["run"], (j, s["run"])
'
}

@test "the gate step has the API token for packer plugins install" {
    py '
for j in LEGS:
    s = [s for s in steps(j) if "gate-ci.sh run" in s.get("run", "")][0]
    assert s["env"]["PACKER_GITHUB_API_TOKEN"] == "${{ github.token }}", (j, s)
'
}

@test "artifacts: only the evidence dir, always, 30 days, overwritten on a re-run, named gate-<release>-<leg>, none of release.yml's" {
    py '
names = []
for j in list(jobs) + list(legs):
    for s in uses(j, "actions/upload-artifact"):
        w = s["with"]
        assert w["path"] == "${{ runner.temp }}/gate-evidence/", (j, w)
        assert s["if"] == "always()", (j, s)
        assert w["retention-days"] == 30, (j, w)
        # a re-run of a leg in the same run uploads its name again
        assert w["overwrite"] is True, (j, w)
        assert w["name"] == "gate-${{ inputs.release }}-" + j, (j, w)
        names.append(j)
assert sorted(names) == LEGS, names
relnames = re.findall(r"name: *(\S+)", open(sys.argv[1] + "/.github/workflows/release.yml").read())
assert not [n for n in relnames if n.startswith("gate-")], relnames
'
}

# The uploaded dir holds one dir, named as the artifact is, so every artifact unpacks to
# gate-<release>-<leg>/ under the one download path, however many there are (see the download test).
@test "each leg gates into gate-evidence/<its artifact name>, never the work dir nor under it" {
    py '
for j in LEGS:
    r = runs(j)
    assert "--work-dir /mnt/gate " in r, (j, r)
    assert "--evidence-dir \"$RUNNER_TEMP/gate-evidence/gate-${{ inputs.release }}-" + j + "\" " in r, (j, r)
'
}

# The verdict reads each leg's job result, gate exit and run attempt from its artifact: a called
# workflow's jobs do not reach the caller one by one, one per release, and an artifact outlives its
# attempt, so a leg a re-run skipped must be told from one it ran. always(), and before the upload,
# so a failed or cancelled leg still says so; plain shell, so it works even when the checkout did
# not.
@test "each leg writes its job status, its gate's exit and its run attempt beside its evidence, always, before uploading" {
    py '
for j in LEGS:
    st = steps(j)
    up = [i for i, s in enumerate(st) if s.get("uses", "").startswith("actions/upload-artifact@")]
    note = [i for i, s in enumerate(st) if "/job\"" in s.get("run", "")]
    assert len(up) == 1 and len(note) == 1 and note[0] == up[0] - 1, (j, up, note)
    s = st[note[0]]
    assert s["if"] == "always()", (j, s)
    e = s["env"]
    assert e["GATE_EVIDENCE"] == "${{ runner.temp }}/gate-evidence/gate-${{ inputs.release }}-" + j, (j, e)
    assert e["GATE_JOB"] == "${{ job.status }}", (j, e)
    assert e["GATE_EXIT"] == "${{ steps.gate.outputs.exit }}", (j, e)
    assert e["GATE_ATTEMPT"] == "${{ github.run_attempt }}", (j, e)
    assert "\"$GATE_EVIDENCE/exit\"" in s["run"] and "bin/" not in s["run"], (j, s["run"])
    assert "printf \x27%s\\n\x27 \"$GATE_ATTEMPT\" > \"$GATE_EVIDENCE/attempt\"" in s["run"], (j, s["run"])
'
}

@test "download-artifact is the family's major, alongside upload-artifact's" {
    py '
d = uses("gate", "actions/download-artifact")
assert {s["uses"] for s in d} == {"actions/download-artifact@v8"}, d
u = {s["uses"] for j in LEGS for s in uses(j, "actions/upload-artifact")}
assert u == {"actions/upload-artifact@v7"}, u
'
}

# Merged, by pattern: a pattern that matches one artifact extracts it flat into `path`, not into
# path/<name>, so a run whose only artifact was gate-candidate read "no steps.tsv" and failed a
# candidate that had passed every step (2026-11-19, run 44601148068). Each artifact holds one dir
# named after itself, so merged they land at path/gate-<release>-<leg> whether one matched or six.
@test "every leg's artifact downloads, merged, into the dir the verdict reads, gated, never failing the job" {
    py '
d = uses("gate", "actions/download-artifact")
assert len(d) == 1, d
s = d[0]
assert s["with"] == {"pattern": "gate-*", "merge-multiple": True, "path": "${{ runner.temp }}/gate-artifacts"}, s
assert s["if"] == "needs.scope.outputs.decision == \x27gated\x27", s
assert s["continue-on-error"] is True, s
assert "--artifacts \"$RUNNER_TEMP/gate-artifacts\"" in runs("gate"), runs("gate")
'
}

@test "the final job decides through bin/gate-ci.sh verdict over every release scope named" {
    py '
r = runs("gate")
assert "bin/gate-ci.sh verdict" in r, r
for opt in ["--decision", "--reason", "--scope-job", "--releases \"$GATE_RELEASES\"", "--artifacts "]:
    assert opt in r, opt
for opt in ["--candidate", "--retry", "--control"]:
    assert opt not in r, opt
e = [s for s in steps("gate") if "gate-ci.sh verdict" in s.get("run", "")][0]["env"]
assert e["GATE_RELEASES"] == "${{ needs.scope.outputs.release_list }}", e
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
assert rel["permissions"] == {"contents": "read"}, rel["permissions"]
for j, v in list(jobs.items()) + list(legs.items()):
    assert "permissions" not in v, j
'
}

# The caller is the event the scope job decided for (a release reads workflow_call), so a pull
# request's new push cancels its old run's boot, per release, and nothing else is ever cancelled.
@test "concurrency: none at the top; each release's legs keyed gate-...-<release>, cancelling only for a pull request (R4)" {
    py '
assert "concurrency" not in gate and "concurrency" not in rel
pr = "inputs.caller == '"'"'pull_request'"'"'"
for j in LEGS:
    c = legs[j]["concurrency"]
    assert c["group"] == "gate-${{ " + pr + " && github.ref || github.run_id }}-${{ inputs.release }}", (j, c)
    assert c["cancel-in-progress"] == "${{ " + pr + " }}", (j, c)
'
}

@test "no job in release.yml is named or keyed gate (the required-check name trap)" {
    py '
for j, v in release["jobs"].items():
    assert j != "gate" and v.get("name") != "gate", j
'
}

@test "release.yml: release-gate calls gate.yml after build, when publishing or on gate_only" {
    py '
j = release["jobs"]["release-gate"]
assert j["name"] == "release gate", j
assert j["needs"] == ["build"], j
want = "needs.build.outputs.publish == '"'"'yes'"'"' || (github.event_name == '"'"'workflow_dispatch'"'"' && inputs.gate_only)"
assert j["if"] == want, j["if"]
assert j["uses"] == "./.github/workflows/gate.yml", j
assert j["with"] == {"caller": "release"}, j
'
}

@test "release.yml: nothing publishes unless the release gate passed" {
    py '
p = release["jobs"]["publish"]
assert p["needs"] == ["build", "release-gate"], p["needs"]
assert p["if"] == "needs.build.outputs.publish == '"'"'yes'"'"'", p["if"]
'
}

@test "release.yml: gate_only runs the gate on any ref and never asks to publish" {
    py '
ron = release.get("on", release.get(True))
g = ron["workflow_dispatch"]["inputs"]["gate_only"]
assert g == {"description": "Run the boot gate on this ref without publishing", "type": "boolean", "default": False}, g
ver = [s for s in release["jobs"]["build"]["steps"] if s.get("id") == "ver"][0]
w = ver["env"]["WANT_RELEASE"]
want = "${{ github.event_name == '"'"'workflow_dispatch'"'"' && inputs.local_release && !inputs.gate_only && github.ref == '"'"'refs/heads/main'"'"' }}"
assert w == want, w
'
}

@test "release.yml's header tells its in-job checks from the boot gate, and why the gate runs only to publish" {
    head -40 "$REPO/.github/workflows/release.yml" > "$BATS_TEST_TMPDIR/header"
    grep -q "gate.yml" "$BATS_TEST_TMPDIR/header"
    grep -q "gate_only" "$BATS_TEST_TMPDIR/header"
    grep -q "docs/decisions/0012" "$BATS_TEST_TMPDIR/header"
    grep -q "docs/decisions/0017" "$BATS_TEST_TMPDIR/header"
    grep -q "deviation" "$BATS_TEST_TMPDIR/header"
}

@test "no doc still says the release wiring is to come" {
    run grep -rn "wired in the change after this" "$REPO/README.md" "$REPO/.github/workflows" "$REPO/docs"
    [ "$status" -eq 1 ]
}

@test "the scope rows are exactly repackage-on-ingredient-bump.yml's paths" {
    py '
want = sorted(repackage[True]["push"]["paths"] if True in repackage else repackage["on"]["push"]["paths"])
got = sorted(l.split("\t")[0] for l in open(sys.argv[1] + "/gate/scope.tsv").read().splitlines() if l and not l.startswith("#"))
assert got == want, (got, want)
'
}

@test "every scope row is path, skip or gate:<releases>, and a reason" {
    # shellcheck source=../lib/common.sh
    . "$REPO/lib/common.sh"
    while IFS= read -r line; do
        case $line in '#'*|'') continue ;; esac
        IFS=$'\t' read -r path what reason <<< "$line"
        [ -n "$path" ]
        [ -n "$reason" ]
        case $what in
            skip) ;;
            gate:?*)
                IFS=, read -r -a rs <<< "${what#gate:}"
                for r in "${rs[@]}"; do known_release "$r"; done ;;
            *) false ;;
        esac
        [ "$(printf '%s' "$line" | awk -F'\t' '{ print NF }')" -eq 3 ]
    done < "$REPO/gate/scope.tsv"
}

@test "the scope rows gate each path on the releases it can break, and skip OpenSSH, saying why" {
    s="$REPO/gate/scope.tsv"
    grep -qx "assets/pins/sources.tsv	gate:10.9,10.6	.*" "$s"
    grep -qx "assets/firmware/config.plist	gate:10.9	.*" "$s"
    grep -qx "assets/firmware/snowleopard/config.plist	gate:10.6	.*" "$s"
    grep -qx "templates/mavericks-aquatransport/aquatransport.pkr.hcl	gate:10.9	.*" "$s"
    grep -qxF "components/**	skip	OpenSSH has its own on-VM smoke test before it releases; the owner, 2026-10-12" "$s"
    run grep -n "gated by hand\|no 10.6 disc" "$s"
    [ "$status" -eq 1 ]
}

@test "the scope step takes its decision and reason from gate-scope.sh's first line, its releases from the second" {
    # gate-scope.sh prints a second line, the releases, when it gates; the step's outputs must
    # not take it into the reason, where it would break $GITHUB_OUTPUT's name=value lines. The
    # real bin/gate-ci.sh makes the releases outputs; only its control-ref is stood in for.
    w="$BATS_TEST_TMPDIR/w"
    mkdir -p "$w/bin"
    python3 -I -c '
import sys, yaml
with open(sys.argv[1] + "/.github/workflows/gate.yml") as f:
    gate = yaml.safe_load(f)
s = [s for s in gate["jobs"]["scope"]["steps"] if "bin/gate-scope.sh" in s.get("run", "")][0]
print(s["run"])
' "$REPO" > "$w/step.sh"
    cat > "$w/bin/gate-scope.sh" <<'EOF'
#!/bin/sh
echo "gated changed: x"
echo "releases 10.9 10.6"
EOF
    cat > "$w/bin/gate-ci.sh" <<'EOF'
#!/bin/sh
case $1 in
    control-ref) echo v0.1 ;;
    *) exec "$GATE_TEST_REPO/bin/gate-ci.sh" "$@" ;;
esac
EOF
    chmod +x "$w/bin/gate-scope.sh" "$w/bin/gate-ci.sh"
    cd "$w"
    GATE_TEST_REPO=$REPO GITHUB_OUTPUT="$w/out" GATE_EVENT=pull_request GATE_PR_BASE=b GATE_PR_HEAD=h \
        GATE_BRANCH=renovate/x GATE_FORCE='' GATE_CONTROL_REF='' run bash "$w/step.sh"
    [ "$status" -eq 0 ]
    [ "$(cat "$w/out")" = "$(printf '%s\n' 'decision=gated' 'reason=changed: x' 'control_ref=v0.1' \
        'event=pull_request' 'releases=["10.9","10.6"]' 'release_list=10.9 10.6')" ]
}

@test "the scope step names no release for the verdict when the change is not gated, yet fills the matrix" {
    w="$BATS_TEST_TMPDIR/w"
    mkdir -p "$w/bin"
    python3 -I -c '
import sys, yaml
with open(sys.argv[1] + "/.github/workflows/gate.yml") as f:
    gate = yaml.safe_load(f)
s = [s for s in gate["jobs"]["scope"]["steps"] if "bin/gate-scope.sh" in s.get("run", "")][0]
print(s["run"])
' "$REPO" > "$w/step.sh"
    cat > "$w/bin/gate-scope.sh" <<'EOF'
#!/bin/sh
echo "not-gated not a Renovate update"
EOF
    cat > "$w/bin/gate-ci.sh" <<'EOF'
#!/bin/sh
exec "$GATE_TEST_REPO/bin/gate-ci.sh" "$@"
EOF
    chmod +x "$w/bin/gate-scope.sh" "$w/bin/gate-ci.sh"
    cd "$w"
    GATE_TEST_REPO=$REPO GITHUB_OUTPUT="$w/out" GATE_EVENT=pull_request GATE_PR_BASE=b GATE_PR_HEAD=h GATE_BRANCH=main \
        GATE_FORCE='' GATE_CONTROL_REF='' run bash "$w/step.sh"
    [ "$status" -eq 0 ]
    [ "$(cat "$w/out")" = "$(printf '%s\n' 'decision=not-gated' 'reason=not a Renovate update' 'control_ref=' \
        'event=pull_request' 'releases=["10.9","10.6"]' 'release_list=')" ]
}

@test "renovate's Packer manager reads every workflow that sets up Packer, and no other" {
    python3 -I -c '
import json, os, re, sys
c = json.load(open(sys.argv[1]))
m = [x for x in c["customManagers"] if x.get("depNameTemplate") == "hashicorp/packer"]
assert len(m) == 1, m
pats = m[0]["managerFilePatterns"]
assert len(pats) == 1, pats
rx = re.compile(pats[0].strip("/"))
d = sys.argv[2] + "/.github/workflows/"
for f in sorted(os.listdir(d)):
    sets_up = "hashicorp/setup-packer@" in open(d + f).read()
    assert bool(rx.search(".github/workflows/" + f)) == sets_up, (f, sets_up)
assert rx.search(".github/workflows/gate-release.yml")
' "$REPO/.github/renovate.json" "$REPO"
}

@test "ci.yml's header no longer says CI never builds an image, and points to gate.yml" {
    run grep -n "does NOT check: that an image builds" "$REPO/.github/workflows/ci.yml"
    [ "$status" -eq 1 ]
    head -30 "$REPO/.github/workflows/ci.yml" | grep -q "gate.yml"
}

@test "candidate, its retry and the control run the same steps, but for what each must differ in" {
    py '
import json
noref = "inputs.control_ref != " + chr(39) * 2
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
        if "name" in w and w["name"].startswith("gate-"):
            w["name"] = "ARTIFACT"
        if "run" in st:
            st["run"] = re.sub(r"--checkout \S+", "--checkout DIR", st["run"])
            st["run"] = st["run"].replace("-" + job + "\"", "-LEG\"")
        if "GATE_EVIDENCE" in st.get("env", {}):
            st["env"]["GATE_EVIDENCE"] = st["env"]["GATE_EVIDENCE"].replace("-" + job, "-LEG")
        out.append(st)
    return out
a, b, c = norm("candidate"), norm("candidate-retry"), norm("control")
assert a == b, (a, b)
assert a == c, (a, c)
'
}

@test "gate.yml's header says 10.6 is gated in CI from the pinned disc, never that it is gated by hand" {
    head -40 "$REPO/.github/workflows/gate.yml" > "$BATS_TEST_TMPDIR/header"
    run grep -n "gated by hand\|no 10.6 disc\|--release 10.9" "$BATS_TEST_TMPDIR/header"
    [ "$status" -eq 1 ]
    grep -q "gate-release.yml" "$BATS_TEST_TMPDIR/header"
    grep -q "snowleopard-disc-10A432" "$BATS_TEST_TMPDIR/header"
}
