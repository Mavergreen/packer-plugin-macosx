#!/usr/bin/env bats
# platform: host-agnostic
# F27: fake volume and PlistBuddy only. No host privilege or live launchd.
setup() {
    REPO=$(cd "$BATS_TEST_DIRNAME/.." && pwd)
    FB=$REPO/assets/guest/firstboot.sh
    G=$BATS_TEST_TMPDIR/guest
    mkdir -p "$G/stub" "$G/daemons" "$G/libexec"
    PL=$G/daemons/com.apple.netbiosd.plist
    BIN=$G/netbiosd
    WRAPPER=$G/libexec/guest-macosx-netbiosd-hup
    FNS=$G/functions.sh
    for f in say netbiosd_hup_wrapper initialize_netbiosd_hup; do
        awk "/^$f\\(\\) \\{/,/^\\}/" "$FB" >> "$FNS"
    done
    for f in fblog_lines fail verify_netbiosd_hup; do
        awk "/^$f\\(\\) \\{/,/^\\}/" "$REPO/templates/mavericks/verify.sh" >> "$FNS"
    done
    TEST_PYTHON=$(command -v python3)
    export TEST_PYTHON
    cat > "$G/stub/PlistBuddy" <<'SH'
#!/bin/sh
exec "$TEST_PYTHON" - "$@" <<'PY'
import os,sys
from pathlib import Path
cmd=sys.argv[2]; p=Path(sys.argv[3])
with open(os.environ['CALLS'],'a') as f: f.write('PB '+cmd+'\n')
if os.environ.get('PB_FAIL') and cmd.startswith(os.environ['PB_FAIL']): sys.exit(1)
try: rows=[s.split('\t',2) for s in p.read_text().splitlines()]
except OSError: sys.exit(1)
parts=cmd.split(' ',3); op=parts[0]; key=parts[1].lstrip(':')
found=[i for i,r in enumerate(rows) if r[0]==key]
if op=='Print':
    if not found: sys.exit(1)
    print(rows[found[0]][2]); sys.exit(0)
if op=='Set' and found:
    rows[found[0]][2]=cmd.split(' ',2)[2]
elif op=='Add' and not found:
    rows.append([key,parts[2],parts[3]])
else: sys.exit(1)
p.write_text(''.join('\t'.join(r)+'\n' for r in rows))
PY
SH
    cat > "$G/stub/shasum" <<'SH'
#!/bin/sh
printf '%s  %s\n' "${STUB_HASH:-946d5e1d843c55c902cbefc493ff072e88e4eb00077d6edf3b077ee5b7a5c95a}" "$3"
SH
    cat > "$G/stub/chown" <<'SH'
#!/bin/sh
printf 'chown %s\n' "$*" >> "$CALLS"
[ -z "${CHOWN_FAIL:-}" ]
SH
    cat > "$G/stub/ls" <<'SH'
#!/bin/sh
# Model guest root:wheel without changing any host ownership.
/bin/ls "$@" | awk -v bad="${BAD_OWNER:-}" '{ $3=bad?42:0; $4=0; print }'
SH
    cat > "$G/stub/launchctl" <<'SH'
#!/bin/sh
printf 'UNEXPECTED launchctl %s\n' "$*" >> "$CALLS"
exit 99
SH
    cat > "$G/stub/sudo" <<'SH'
#!/bin/sh
# No privilege: only the fake-volume grep fallback.
[ "$1" = -n ] && shift
exec "$@"
SH
    chmod +x "$G/stub/"*
    printf '#!%s\n' "$TEST_PYTHON" > "$BIN"
    cat >> "$BIN" <<'PY'
import os,signal,sys
assert signal.getsignal(signal.SIGHUP)==signal.SIG_IGN
os.kill(os.getpid(),signal.SIGHUP)
print(repr(sys.argv[1:]))
PY
    chmod +x "$BIN"
    printf 'Label\tstring\tcom.apple.netbiosd\nProgramArguments:0\tstring\t%s\nSockets:NetbiosNameServices\tdict\tname socket\nSockets:NetbiosDatagramServices\tdict\tdatagram socket\n' "$BIN" > "$PL"
    cp "$PL" "$G/original"
    CALLS=$G/calls
    : > "$CALLS"
}
apply() {
    prefix=()
    if [ -n "${APPLY_BOUND:-}" ]; then
        prefix=("$TEST_PYTHON" -c 'import os,signal,subprocess,sys
p=subprocess.Popen(sys.argv[2:],start_new_session=True)
try: sys.exit(p.wait(timeout=float(sys.argv[1])))
except subprocess.TimeoutExpired:
    try: os.killpg(p.pid,signal.SIGKILL)
    except ProcessLookupError: pass
    p.wait()
    print("bounded fake-volume initializer timed out")
    sys.exit(124)' "$APPLY_BOUND")
    fi
    run "${prefix[@]}" env -i PATH="$G/stub:/usr/bin:/bin" TEST_PYTHON="$TEST_PYTHON" G="$G" FNS="$FNS" \
        CALLS="$CALLS" STUB_HASH="${STUB_HASH:-}" PB_FAIL="${PB_FAIL:-}" BAD_OWNER="${BAD_OWNER:-}" CHOWN_FAIL="${CHOWN_FAIL:-}" \
        /bin/sh -c '. "$FNS"; LOG=$G/log; NETBIOSD_PLIST=$G/daemons/com.apple.netbiosd.plist; NETBIOSD_BIN=$G/netbiosd; NETBIOSD_HUP_WRAPPER=$G/libexec/guest-macosx-netbiosd-hup; PLISTBUDDY=$G/stub/PlistBuddy; initialize_netbiosd_hup'
}
inert() {
    [ "$status" -eq 0 ]
    cmp "$PL" "$G/before"
    [ ! -e "$WRAPPER" ]
    run grep -E 'PB (Set|Add)|UNEXPECTED' "$CALLS"
    [ "$status" -eq 1 ]
}
verify_netbiosd() {
    run env -i PATH="$G/stub:/usr/bin:/bin" TEST_PYTHON="$TEST_PYTHON" G="$G" FNS="$FNS" CALLS="$CALLS" BAD_OWNER="${BAD_OWNER:-}" \
        /bin/sh -c '. "$FNS"; FBLOG=$G/log; DAEMONS=$G/daemons; GUEST_MACOSX_VERIFY_NETBIOSD_BIN=$G/netbiosd; GUEST_MACOSX_VERIFY_NETBIOSD_WRAPPER=$G/libexec/guest-macosx-netbiosd-hup; GUEST_MACOSX_VERIFY_PLISTBUDDY=$G/stub/PlistBuddy; failures=0; verify_netbiosd_hup; exit "$failures"'
}
@test "stock job gets pre-exec ignored HUP; sockets and other keys unchanged" {
    printf 'Extra\tstring\tkeep me\n' >> "$PL"
    apply
    [ "$status" -eq 0 ]
    grep -Fx "ProgramArguments:0	string	$WRAPPER" "$PL"
    grep -Fx "ProgramArguments:1	string	$BIN" "$PL"
    grep -Fx 'Extra	string	keep me' "$PL"
    grep -Fx 'Sockets:NetbiosNameServices	dict	name socket' "$PL"
    grep -Fx 'Sockets:NetbiosDatagramServices	dict	datagram socket' "$PL"
    [ "$(wc -l < "$PL")" -eq 6 ]
    grep -q 'ignored before exec from next boot' "$G/log"
    grep -q '^chown 0:0 ' "$CALLS"
    run grep UNEXPECTED "$CALLS"
    [ "$status" -eq 1 ]
}
@test "generated wrapper really inherits HUP ignore across exec and preserves arguments" {
    apply
    [ "$status" -eq 0 ]
    # Ensure this test cannot pass merely because the harness ignored HUP.
    run "$TEST_PYTHON" -c 'import os,signal,sys; signal.signal(signal.SIGHUP,signal.SIG_DFL); os.execv(sys.argv[1],sys.argv[1:])' "$WRAPPER" "$BIN" 'one argument with spaces' two
    [ "$status" -eq 0 ]
    [ "$output" = "['one argument with spaces', 'two']" ]
}
@test "optional stock Program is changed too" {
    printf 'Program\tstring\t%s\n' "$BIN" >> "$PL"
    apply
    [ "$status" -eq 0 ]
    grep -Fx "Program	string	$WRAPPER" "$PL"
}
@test "idempotent: no repeated edit, wrapper overwrite, or live reload" {
    apply
    [ "$status" -eq 0 ]
    cp "$PL" "$G/after"
    cp "$WRAPPER" "$G/wrapper-after"
    : > "$CALLS"
    apply
    [ "$status" -eq 0 ]
    cmp "$PL" "$G/after"
    cmp "$WRAPPER" "$G/wrapper-after"
    grep -q 'already initialized' "$G/log"
    run grep -E 'PB (Set|Add)|chown|UNEXPECTED' "$CALLS"
    [ "$status" -eq 1 ]
}
@test "inert for an unknown binary revision" {
    cp "$PL" "$G/before"
    STUB_HASH=unknown apply
    inert
    grep -q 'not the measured version' "$G/log"
}
@test "inert without the executable" {
    chmod -x "$BIN"
    cp "$PL" "$G/before"
    apply
    inert
}
@test "inert without a job (as on 10.6)" {
    rm "$PL"
    apply
    [ "$status" -eq 0 ]
    [ ! -e "$WRAPPER" ]
    [ ! -s "$CALLS" ]
}
@test "inert for a symlinked job" {
    mv "$PL" "$G/target"
    ln -s "$G/target" "$PL"
    cp "$PL" "$G/before"
    apply
    inert
}
@test "inert for a different label" {
    grep -v '^Label[[:space:]]' "$PL" > "$G/rest"
    mv "$G/rest" "$PL"
    printf 'Label\tstring\tcustom\n' >> "$PL"
    cp "$PL" "$G/before"
    apply
    inert
}
@test "inert for a different executable argument" {
    grep -v '^ProgramArguments:0[[:space:]]' "$PL" > "$G/rest"
    mv "$G/rest" "$PL"
    printf 'ProgramArguments:0\tstring\t/custom\n' >> "$PL"
    cp "$PL" "$G/before"
    apply
    inert
}
@test "inert for an extra argument, including empty" {
    for arg in custom ''; do
        cp "$G/original" "$PL"
        printf 'ProgramArguments:1\tstring\t%s\n' "$arg" >> "$PL"
        cp "$PL" "$G/before"
        apply
        inert
    done
}
@test "inert for a custom or empty Program" {
    for program in /custom ''; do
        cp "$G/original" "$PL"
        printf 'Program\tstring\t%s\n' "$program" >> "$PL"
        cp "$PL" "$G/before"
        apply
        inert
    done
}
@test "inert without both inherited socket groups" {
    for group in NetbiosNameServices NetbiosDatagramServices; do
        grep -v "^Sockets:$group" "$G/original" > "$PL"
        cp "$PL" "$G/before"
        apply
        inert
    done
}
@test "unknown wrapper contents are never overwritten" {
    printf 'custom\n' > "$WRAPPER"
    cp "$PL" "$G/before"
    apply
    [ "$status" -eq 0 ]
    cmp "$PL" "$G/before"
    [ "$(< "$WRAPPER")" = custom ]
    grep -q 'existing wrapper is not ours' "$G/log"
}
@test "a foreign FIFO wrapper is rejected without opening or waiting on it" {
    mkfifo "$WRAPPER"
    cp "$PL" "$G/before"
    APPLY_BOUND=2 apply
    [ "$status" -eq 0 ]
    cmp "$PL" "$G/before"
    [ -p "$WRAPPER" ]
    grep -q 'existing wrapper is not ours' "$G/log"
}
@test "an executable FIFO is not treated as a native binary" {
    rm "$BIN"
    mkfifo "$BIN"
    chmod +x "$BIN"
    cp "$PL" "$G/before"
    APPLY_BOUND=2 apply
    inert
}
@test "a symlink at the wrapper path is never followed or replaced" {
    printf 'custom\n' > "$G/custom"
    ln -s "$G/custom" "$WRAPPER"
    apply
    [ "$status" -eq 0 ]
    [ -L "$WRAPPER" ]
    [ "$(< "$G/custom")" = custom ]
    cmp "$PL" "$G/original"
}
@test "changed ownership or mode of an existing wrapper prevents job edits" {
    apply
    [ "$status" -eq 0 ]
    cp "$G/original" "$PL"
    BAD_OWNER=1 apply
    [ "$status" -eq 0 ]
    cmp "$PL" "$G/original"
    chmod 777 "$WRAPPER"
    apply
    [ "$status" -eq 0 ]
    cmp "$PL" "$G/original"
}
@test "failed ownership setup leaves the job unchanged" {
    CHOWN_FAIL=1 apply
    [ "$status" -eq 0 ]
    cmp "$PL" "$G/original"
    [ ! -e "$WRAPPER" ]
    grep -q 'COULD NOT install wrapper' "$G/log"
}
@test "a staged argv edit failure cannot break the original job" {
    PB_FAIL='Add :ProgramArguments:1' apply
    [ "$status" -eq 0 ]
    cmp "$PL" "$G/original"
    grep -q 'COULD NOT stage job edit' "$G/log"
    run find "$G/daemons" -name '*.guest-macosx.*'
    [ "$status" -eq 0 ]
    [ -z "$output" ]
}
@test "a failed optional Program edit is atomic too" {
    printf 'Program\tstring\t%s\n' "$BIN" >> "$PL"
    cp "$PL" "$G/before"
    PB_FAIL='Set :Program ' apply
    [ "$status" -eq 0 ]
    cmp "$PL" "$G/before"
}
@test "firstboot runs this only on 10.9 after updates, not by reloading a job" {
    line=$(grep -n '^    initialize_netbiosd_hup$' "$FB" | cut -d: -f1)
    [ -n "$line" ]
    [ "$(awk -v n="$((line-1))" 'NR==n' "$FB")" = 'if [ -z "$GUEST_MACOSX_FB_RELEASE" ]; then' ]
    upd=$(grep -n '^    install_updates$' "$FB" | head -1 | cut -d: -f1)
    [ "$upd" -lt "$line" ]
    run grep -E 'launchctl|smb.preferences.plist' "$FNS"
    [ "$status" -eq 1 ]
}
@test "verifier checks the installed helper and job, not just the action log" {
    apply
    [ "$status" -eq 0 ]
    verify_netbiosd
    [ "$status" -eq 0 ]
    [[ "$output" == *'netbiosd-hup=inspected'* ]]
}
@test "verifier rejects a missing or changed helper despite a successful action log" {
    apply
    [ "$status" -eq 0 ]
    cp "$WRAPPER" "$G/good-wrapper"
    rm "$WRAPPER"
    verify_netbiosd
    [ "$status" -ne 0 ]
    cp "$G/good-wrapper" "$WRAPPER"
    printf '# changed\n' >> "$WRAPPER"
    chmod +x "$WRAPPER"
    verify_netbiosd
    [ "$status" -ne 0 ]
}
@test "verifier rejects a missing job or native executable" {
    apply
    [ "$status" -eq 0 ]
    mv "$PL" "$G/saved-job"
    verify_netbiosd
    [ "$status" -ne 0 ]
    mv "$G/saved-job" "$PL"
    chmod -x "$BIN"
    verify_netbiosd
    [ "$status" -ne 0 ]
}
@test "verifier rejects a helper with unsafe ownership or mode" {
    apply
    [ "$status" -eq 0 ]
    BAD_OWNER=1 verify_netbiosd
    [ "$status" -ne 0 ]
    chmod 777 "$WRAPPER"
    verify_netbiosd
    [ "$status" -ne 0 ]
}
@test "verifier rejects wrong argv or a conflicting Program" {
    apply
    [ "$status" -eq 0 ]
    cp "$PL" "$G/good-job"
    cp "$G/original" "$PL"
    verify_netbiosd
    [ "$status" -ne 0 ]
    cp "$G/good-job" "$PL"
    printf 'Program\tstring\t/custom\n' >> "$PL"
    verify_netbiosd
    [ "$status" -ne 0 ]
}
@test "verifier rejects an extra argument" {
    apply
    [ "$status" -eq 0 ]
    printf 'ProgramArguments:2\tstring\textra\n' >> "$PL"
    verify_netbiosd
    [ "$status" -ne 0 ]
}
@test "verifier rejects missing or failed initialization, but accepts deliberate inert cases" {
    : > "$G/log"
    verify_netbiosd
    [ "$status" -ne 0 ]
    echo 'guest-macosx-firstboot: netbiosd HUP: COULD NOT stage job edit; job left as it is' > "$G/log"
    verify_netbiosd
    [ "$status" -ne 0 ]
    echo 'guest-macosx-firstboot: netbiosd HUP: executable not the measured version; left as it is' > "$G/log"
    verify_netbiosd
    [ "$status" -eq 0 ]
    [[ "$output" == *'netbiosd-hup=inert'* ]]
}
