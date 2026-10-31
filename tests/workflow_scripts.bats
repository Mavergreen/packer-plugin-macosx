#!/usr/bin/env bats
# platform: host-agnostic
#
# A workflow's `run:` steps are shell scripts too, run by GitHub with the
# shell the step, its job or the workflow names (bash by default). The lint
# reads each one: bin/workflow-scripts.sh extracts them, at the workflow's
# own line numbers, and bin/shellcheck-workflows.sh lints them, from
# bin/run-tests.sh's shellcheck step.

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    T="$BATS_TEST_TMPDIR/tree"
    W="$T/.github/workflows"
    mkdir -p "$W"
    OUT="$BATS_TEST_TMPDIR/out"
}

_line() { printf '%s\n' "$2" >> "$W/$1"; }

# wf.yml: a block step with a finding (an unquoted $1, line 9) and an
# inline step, under job build.
_wf() {
    _line wf.yml 'on: push'
    _line wf.yml 'jobs:'
    _line wf.yml '  build:'
    _line wf.yml '    runs-on: ubuntu-latest   # a comment'
    _line wf.yml '    steps:'
    _line wf.yml '      - uses: actions/checkout@v4'
    _line wf.yml '      - name: Build'
    _line wf.yml '        run: |'
    _line wf.yml '          rm -f $1'
    _line wf.yml '          echo "${{ github.sha }}"'
    _line wf.yml '      - run: echo done   # a comment'
}

@test "each run: step is extracted at the workflow's lines, with its job and step" {
    _wf
    run "$REPO/bin/workflow-scripts.sh" "$T" "$OUT"
    [ "$status" -eq 0 ]
    [[ "$output" == *".github/workflows/wf.yml"$'\t'"9"$'\t'"job build, step 2 (Build)"$'\t'"bash"$'\t'* ]] || false
    [[ "$output" == *".github/workflows/wf.yml"$'\t'"11"$'\t'"job build, step 3"$'\t'"bash"$'\t'* ]] || false
    f=$(printf '%s\n' "$output" | sed -n 1p | cut -f5)
    [ "$(sed -n 9p "$f")" = 'rm -f $1' ]
    [ "$(sed -n 10p "$f")" = 'echo "_GHA_EXPR_"' ]
}

@test "shellcheck-workflows fails a finding, naming the workflow's line" {
    command -v shellcheck >/dev/null || skip "shellcheck not installed"
    _wf
    run "$REPO/bin/shellcheck-workflows.sh" "$T"
    [ "$status" -ne 0 ]
    [[ "$output" == *".github/workflows/wf.yml (job build, step 2 (Build)) line 9"* ]] || false
    [[ "$output" == *SC2086* ]] || false
}

@test "the shell: the step's, else its job's default, else the workflow's, else bash" {
    _line wf.yml 'defaults:'
    _line wf.yml '  run:'
    _line wf.yml '    shell: sh'
    _line wf.yml 'jobs:'
    _line wf.yml '  a:'
    _line wf.yml '    runs-on: ubuntu-latest'
    _line wf.yml '    defaults:'
    _line wf.yml '      run:'
    _line wf.yml '        shell: bash'
    _line wf.yml '    steps:'
    _line wf.yml '      - run: echo job-default'
    _line wf.yml '      - run: echo own'
    _line wf.yml '        shell: sh -e {0}'
    _line wf.yml '  b:'
    _line wf.yml '    runs-on: macos-latest'
    _line wf.yml '    steps:'
    _line wf.yml '      - run: echo workflow-default'
    _line wf.yml '      - run: print(1)'
    _line wf.yml '        shell: python'
    # With no defaults, a Windows runner's shell is pwsh.
    _line win.yml 'jobs:'
    _line win.yml '  c:'
    _line win.yml '    runs-on: windows-latest'
    _line win.yml '    steps:'
    _line win.yml '      - run: Write-Output hi'
    run "$REPO/bin/workflow-scripts.sh" "$T" "$OUT"
    [ "$status" -eq 0 ]
    [[ "$output" == *$'\t'"11"$'\t'"job a, step 1"$'\t'"bash"$'\t'* ]] || false
    [[ "$output" == *$'\t'"12"$'\t'"job a, step 2"$'\t'"sh"$'\t'* ]] || false
    [[ "$output" == *$'\t'"17"$'\t'"job b, step 1"$'\t'"sh"$'\t'* ]] || false
    # python, and pwsh (a Windows runner's default), are not shell scripts.
    [[ "$output" != *"job b, step 2"* ]] || false
    [[ "$output" != *"job c"* ]] || false
}

@test "a shell this lint does not know fails loudly" {
    _line wf.yml 'jobs:'
    _line wf.yml '  a:'
    _line wf.yml '    runs-on: ubuntu-latest'
    _line wf.yml '    steps:'
    _line wf.yml '      - run: echo hi'
    _line wf.yml '        shell: zsh {0}'
    run "$REPO/bin/workflow-scripts.sh" "$T" "$OUT"
    [ "$status" -ne 0 ]
    [[ "$output" == *"wf.yml:5: shell zsh {0} is not one this lint knows"* ]] || false
}

@test "a run: it cannot read exactly fails loudly: folded, indicated, continued, escaped" {
    _line wf.yml 'jobs:'
    _line wf.yml '  a:'
    _line wf.yml '    runs-on: ubuntu-latest'
    _line wf.yml '    steps:'
    _line wf.yml '      - run: >'
    _line wf.yml '          echo folded'
    _line wf.yml '      - run: |2'
    _line wf.yml '          echo indicated'
    _line wf.yml '      - run: echo one'
    _line wf.yml '          echo continued'
    _line wf.yml '      - run: "echo \"escaped\""'
    run "$REPO/bin/workflow-scripts.sh" "$T" "$OUT"
    [ "$status" -ne 0 ]
    [[ "$output" == *"wf.yml:5: a folded"* ]] || false
    [[ "$output" == *"wf.yml:7: a run: block with an indentation indicator"* ]] || false
    [[ "$output" == *"wf.yml:9: a run: that continues"* ]] || false
    [[ "$output" == *"wf.yml:11: a double-quoted run: with an escape"* ]] || false
}

@test "a block that is not a run: is skipped whole, whatever it holds" {
    _line wf.yml 'jobs:'
    _line wf.yml '  a:'
    _line wf.yml '    runs-on: ubuntu-latest'
    _line wf.yml '    steps:'
    _line wf.yml '      - uses: actions/github-script@v7'
    _line wf.yml '        with:'
    _line wf.yml '          script: |'
    _line wf.yml '            run: not a key'
    _line wf.yml '            - run: nor this'
    _line wf.yml '      - run: echo real'
    run "$REPO/bin/workflow-scripts.sh" "$T" "$OUT"
    [ "$status" -eq 0 ]
    [ "$(printf '%s\n' "$output" | wc -l)" -eq 1 ]
    [[ "$output" == *$'\t'"10"$'\t'"job a, step 2"$'\t'* ]] || false
}

@test "the repository's workflow steps all pass shellcheck" {
    command -v shellcheck >/dev/null || skip "shellcheck not installed"
    run "$REPO/bin/shellcheck-workflows.sh" "$REPO"
    [ "$status" -eq 0 ]
    [[ "$output" == *"workflow run: step(s) pass shellcheck"* ]] || false
}

@test "the suite runs shellcheck-workflows" {
    grep -q 'bin/shellcheck-workflows.sh' "$REPO/bin/run-tests.sh"
}
