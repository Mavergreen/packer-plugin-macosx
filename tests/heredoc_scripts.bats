#!/usr/bin/env bats
# platform: host-agnostic
#
# A shell script embedded as a heredoc -- firstboot.sh writes the shutdown
# helper and the sshd-keygen wrapper that way, and the suites write their
# stubs that way -- is a shell script all the same, and the lint pass
# reads each one: bin/heredoc-scripts.sh finds them, bin/shellcheck-heredocs.sh
# runs shellcheck on each, bin/bash32-check.sh checks each, all reporting
# the host file's own line numbers.
#
# Fixtures are written a line at a time (_line), never with a heredoc of
# their own: a fixture's deliberately bad heredoc written as a heredoc in
# this file would be one more embedded script for the real lint to fail on.

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    T="$BATS_TEST_TMPDIR/tree"
    mkdir -p "$T"
    OUT="$BATS_TEST_TMPDIR/out"
}

_line() { printf '%s\n' "$2" >> "$T/$1"; }

# host.sh: lines 1-3 are the host, line 4 the heredoc's shebang, line 6 a
# finding (an unquoted $1, SC2086), line 7 its terminator.
_bad() {
    _line host.sh '#!/bin/sh'
    _line host.sh '# a host script'
    _line host.sh "cat > /tmp/helper <<'HELPER_EOF'"
    _line host.sh '#!/bin/sh'
    _line host.sh 'set -u'
    _line host.sh 'rm -f $1'
    _line host.sh 'HELPER_EOF'
}

_clean() {
    _line host.sh '#!/bin/sh'
    _line host.sh "cat > /tmp/helper <<'HELPER_EOF'"
    _line host.sh '#!/bin/sh'
    _line host.sh 'rm -f "$1"'
    _line host.sh 'HELPER_EOF'
}

@test "the extractor lists a quoted shell heredoc, at its host line" {
    _clean
    run "$REPO/bin/heredoc-scripts.sh" "$T" "$OUT"
    [ "$status" -eq 0 ]
    [[ "$output" == "host.sh"$'\t'"3"$'\t'"HELPER_EOF"$'\t'"sh"$'\t'* ]] || false
    # The extracted copy keeps the host's line numbers: body line 1 is host line 3.
    f=${output##*$'\t'}
    [ "$(sed -n 4p "$f")" = 'rm -f "$1"' ]
}

@test "shellcheck-heredocs fails a planted bad heredoc, naming the host's line" {
    command -v shellcheck >/dev/null || skip "shellcheck not installed"
    _bad
    run "$REPO/bin/shellcheck-heredocs.sh" "$T"
    [ "$status" -ne 0 ]
    [[ "$output" == *"host.sh (heredoc HELPER_EOF) line 6"* ]] || false
    [[ "$output" == *SC2086* ]] || false
}

@test "shellcheck-heredocs passes a clean heredoc" {
    command -v shellcheck >/dev/null || skip "shellcheck not installed"
    _clean
    run "$REPO/bin/shellcheck-heredocs.sh" "$T"
    [ "$status" -eq 0 ]
    [[ "$output" == *"1 embedded shell script"* ]] || false
}

@test "a heredoc that is not a shell script is ignored" {
    _line host.sh '#!/bin/sh'
    _line host.sh "python3 - <<'PY_EOF'"
    _line host.sh '#!/usr/bin/env python3'
    _line host.sh 'print(1)'
    _line host.sh 'PY_EOF'
    _line host.sh "cat > notes.txt <<'TXT_EOF'"
    _line host.sh 'rm -f $1 is not shell here'
    _line host.sh 'TXT_EOF'
    run "$REPO/bin/heredoc-scripts.sh" "$T" "$OUT"
    [ "$status" -eq 0 ]
    [ -z "$output" ]
}

@test "an unquoted shell heredoc fails loudly: its text is not what runs" {
    _line host.sh '#!/bin/sh'
    _line host.sh 'cat > /tmp/helper <<HELPER_EOF'
    _line host.sh '#!/bin/sh'
    _line host.sh 'echo "$HOME"'
    _line host.sh 'HELPER_EOF'
    run "$REPO/bin/heredoc-scripts.sh" "$T" "$OUT"
    [ "$status" -ne 0 ]
    [[ "$output" == *"host.sh:2"*"unquoted"* ]] || false
}

@test "an unterminated shell heredoc fails loudly" {
    _line host.sh '#!/bin/sh'
    _line host.sh "cat > /tmp/helper <<'HELPER_EOF'"
    _line host.sh '#!/bin/sh'
    _line host.sh 'echo hi'
    run "$REPO/bin/heredoc-scripts.sh" "$T" "$OUT"
    [ "$status" -ne 0 ]
    [[ "$output" == *"host.sh:2"*"never ends"* ]] || false
}

@test "two heredocs on one line, the first a shell script, fail loudly" {
    _line host.sh '#!/bin/sh'
    _line host.sh "cat <<'A_EOF' > /tmp/a; cat <<'B_EOF' > /tmp/b"
    _line host.sh '#!/bin/sh'
    _line host.sh 'echo a'
    _line host.sh 'A_EOF'
    _line host.sh 'just text'
    _line host.sh 'B_EOF'
    run "$REPO/bin/heredoc-scripts.sh" "$T" "$OUT"
    [ "$status" -ne 0 ]
    [[ "$output" == *"host.sh:2: two heredocs on one line: split them"* ]] || false
    run env GUEST_MACOSX_BASH32_ROOT="$T" "$REPO/bin/bash32-check.sh"
    [ "$status" -ne 0 ]
    [[ "$output" == *"two heredocs on one line"* ]] || false
}

@test "two heredocs on one line, the second a shell script, fail loudly" {
    _line host.sh '#!/bin/sh'
    _line host.sh "cat <<'A_EOF' > /tmp/a; cat <<'B_EOF' > /tmp/b"
    _line host.sh 'just text'
    _line host.sh 'A_EOF'
    _line host.sh '#!/bin/sh'
    _line host.sh 'echo b'
    _line host.sh 'B_EOF'
    run "$REPO/bin/heredoc-scripts.sh" "$T" "$OUT"
    [ "$status" -ne 0 ]
    [[ "$output" == *"host.sh:2: two heredocs on one line: split them"* ]] || false
}

@test "two heredocs on one line, neither a shell script, are left alone" {
    _line host.sh '#!/bin/sh'
    _line host.sh "cat <<'A_EOF' > /tmp/a; cat <<'B_EOF' > /tmp/b"
    _line host.sh 'just text'
    _line host.sh 'A_EOF'
    _line host.sh 'more text'
    _line host.sh 'B_EOF'
    run "$REPO/bin/heredoc-scripts.sh" "$T" "$OUT"
    [ "$status" -eq 0 ]
    [ -z "$output" ]
}

@test "a <<- heredoc is read with its leading tabs stripped, and a bash one as bash" {
    _line host.bats '@test "x" {'
    _line host.bats "	cat > /tmp/s <<-'S_EOF'"
    _line host.bats '	#!/usr/bin/env bash'
    _line host.bats '	echo "$1"'
    _line host.bats '	S_EOF'
    _line host.bats '}'
    run "$REPO/bin/heredoc-scripts.sh" "$T" "$OUT"
    [ "$status" -eq 0 ]
    [[ "$output" == "host.bats"$'\t'"3"$'\t'"S_EOF"$'\t'"bash"$'\t'* ]] || false
    f=${output##*$'\t'}
    [ "$(sed -n 4p "$f")" = 'echo "$1"' ]
}

@test "any delimiter word: digits, dashes, quoted in part" {
    _line host.sh '#!/bin/sh'
    _line host.sh "cat > /a <<'1E'"
    _line host.sh '#!/bin/sh'
    _line host.sh 'echo one'
    _line host.sh '1E'
    _line host.sh "cat > /b <<'E-4'"
    _line host.sh '#!/bin/sh'
    _line host.sh 'echo two'
    _line host.sh 'E-4'
    _line host.sh "cat > /c <<E'X'"
    _line host.sh '#!/bin/sh'
    _line host.sh 'echo three'
    _line host.sh 'EX'
    _line host.sh 'cat > /d <<"A B"'
    _line host.sh '#!/bin/sh'
    _line host.sh 'echo four'
    _line host.sh 'A B'
    run "$REPO/bin/heredoc-scripts.sh" "$T" "$OUT"
    [ "$status" -eq 0 ]
    [[ "$output" == *"host.sh"$'\t'"3"$'\t'"1E"$'\t'"sh"$'\t'* ]] || false
    [[ "$output" == *"host.sh"$'\t'"7"$'\t'"E-4"$'\t'"sh"$'\t'* ]] || false
    [[ "$output" == *"host.sh"$'\t'"11"$'\t'"EX"$'\t'"sh"$'\t'* ]] || false
    [[ "$output" == *"host.sh"$'\t'"15"$'\t'"A B"$'\t'"sh"$'\t'* ]] || false
}

@test "shebangs as lib/shebang.sh reads them: spaced, dash, ksh; zsh fails loudly" {
    _line host.sh '#!/bin/sh'
    _line host.sh "cat > /a <<'A_EOF'"
    _line host.sh '#! /bin/sh'
    _line host.sh 'echo a'
    _line host.sh 'A_EOF'
    _line host.sh "cat > /b <<'B_EOF'"
    _line host.sh '#!/usr/bin/env -S dash -e'
    _line host.sh 'echo b'
    _line host.sh 'B_EOF'
    _line host.sh "cat > /c <<'C_EOF'"
    _line host.sh '#!/bin/ksh'
    _line host.sh 'echo c'
    _line host.sh 'C_EOF'
    run "$REPO/bin/heredoc-scripts.sh" "$T" "$OUT"
    [ "$status" -eq 0 ]
    [[ "$output" == *$'\t'"A_EOF"$'\t'"sh"$'\t'* ]] || false
    [[ "$output" == *$'\t'"B_EOF"$'\t'"dash"$'\t'* ]] || false
    [[ "$output" == *$'\t'"C_EOF"$'\t'"ksh"$'\t'* ]] || false
    _line host.sh "cat > /z <<'Z_EOF'"
    _line host.sh '#!/usr/bin/env zsh'
    _line host.sh 'echo z'
    _line host.sh 'Z_EOF'
    run "$REPO/bin/heredoc-scripts.sh" "$T" "$OUT.2"
    [ "$status" -ne 0 ]
    [[ "$output" == *"host.sh:14: heredoc Z_EOF is a zsh script"* ]] || false
}

@test "a heredoc inside a shell heredoc is linted too, at the host's line" {
    command -v shellcheck >/dev/null || skip "shellcheck not installed"
    _line host.sh '#!/bin/sh'
    _line host.sh "cat > /stub <<'OUTER'"
    _line host.sh '#!/bin/sh'
    _line host.sh "cat > /inner <<'INNER'"
    _line host.sh '#!/bin/sh'
    _line host.sh 'rm -f $1'
    _line host.sh 'INNER'
    _line host.sh 'OUTER'
    run "$REPO/bin/heredoc-scripts.sh" "$T" "$OUT"
    [ "$status" -eq 0 ]
    [[ "$output" == *"host.sh"$'\t'"3"$'\t'"OUTER"$'\t'* ]] || false
    [[ "$output" == *"host.sh"$'\t'"5"$'\t'"OUTER > INNER"$'\t'* ]] || false
    run "$REPO/bin/shellcheck-heredocs.sh" "$T"
    [ "$status" -ne 0 ]
    [[ "$output" == *"host.sh (heredoc OUTER > INNER) line 6"* ]] || false
}

@test "a printf or echo whose format writes a shell's shebang fails loudly: use a quoted heredoc" {
    # A script written by printf or echo is a shell script the lint cannot
    # read; written as a quoted heredoc, it is linted like the rest.
    _line host.sh '#!/bin/sh'
    _line host.sh "printf '#!/bin/sh\\necho stub\\n' > \"\$d/stub\""
    sb='#!/usr/bin/env bash' # in a variable, so this line is not itself one
    _line host.sh "    echo '$sb' > \"\$d/two\""
    _line host.sh "x=1; printf -- \"#! /bin/sh -e\\nexit 0\\n\" > \"\$d/three\""
    run "$REPO/bin/heredoc-scripts.sh" "$T" "$OUT"
    [ "$status" -ne 0 ]
    [[ "$output" == *"host.sh:2: printf writes a shell script"*"quoted heredoc"* ]] || false
    [[ "$output" == *"host.sh:3: echo writes a shell script"* ]] || false
    [[ "$output" == *"host.sh:4: printf writes a shell script"* ]] || false
    run env GUEST_MACOSX_BASH32_ROOT="$T" "$REPO/bin/bash32-check.sh"
    [ "$status" -ne 0 ]
}

@test "a printf of another shebang, or of a shebang as an argument, is not a stub" {
    _line host.sh '#!/bin/sh'
    _line host.sh "printf '#!/usr/bin/env python3\\nprint(1)\\n' > \"\$d/py\""
    _line host.sh "printf '%s\\n' \"\$shebang\" > \"\$d/fixture\""
    _line host.sh "echo '# not a shebang'"
    run "$REPO/bin/heredoc-scripts.sh" "$T" "$OUT"
    [ "$status" -eq 0 ]
}

@test "bash32-check checks a heredoc's body, naming the host's line" {
    _line host.sh '#!/bin/sh'
    _line host.sh "cat > /tmp/helper <<'HELPER_EOF'"
    _line host.sh '#!/usr/bin/env bash'
    _line host.sh 'declare -A m=()'                            # bash32-allow
    _line host.sh 'HELPER_EOF'
    run env GUEST_MACOSX_BASH32_ROOT="$T" "$REPO/bin/bash32-check.sh"
    [ "$status" -ne 0 ]
    [[ "$output" == *"host.sh (heredoc HELPER_EOF):4"* ]] || false
}

@test "the repository's embedded shell scripts all pass shellcheck" {
    command -v shellcheck >/dev/null || skip "shellcheck not installed"
    run "$REPO/bin/shellcheck-heredocs.sh" "$REPO"
    [ "$status" -eq 0 ]
    run "$REPO/bin/heredoc-scripts.sh" "$REPO" "$OUT"
    [[ "$output" == *"assets/guest/firstboot.sh"$'\t'*$'\t'"GUEST_MACOSX_SHUTDOWN_EOF"$'\t'* ]] || false
    [[ "$output" == *"assets/guest/firstboot.sh"$'\t'*$'\t'"GUEST_MACOSX_WRAPPER_EOF"$'\t'* ]] || false
}

@test "the suite runs shellcheck-heredocs" {
    grep -q 'bin/shellcheck-heredocs.sh' "$REPO/bin/run-tests.sh"
}
