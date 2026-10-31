#!/usr/bin/env bats
# platform: host-agnostic
#
# firstboot.sh's seed_keyboard_type (docs/decisions/0035).
#
# QEMU's usb-kbd is no keyboard Mac OS X knows, so Keyboard Setup Assistant
# opens to identify it: on 10.6.8 over the login window at every boot, on
# 10.9.5 in every desktop session. Quitting it writes one entry into
# /Library/Preferences/com.apple.keyboardtype.plist's keyboardtype
# dictionary, "1-1575-0" = 40, and with that entry there it does not open
# (MEASURED 2026-10-27 on both releases). The first boot writes that entry
# only when there is none, never changes one that is there, keeps every
# other keyboard's, and logs which.
#
# SAFETY: these run firstboot's function on this machine with every path in
# the test's own directory, PATH=/usr/bin:/bin (no sbin); defaults is a
# stand-in, never the host's, that keeps one "key<TAB>value" line per entry
# of the keyboardtype dictionary and prints it as defaults does.

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    FB="$REPO/assets/guest/firstboot.sh"
    FNS="$BATS_TEST_TMPDIR/fns.sh"
    : > "$FNS"
    for f in say seed_keyboard_type; do
        fn=$(awk "/^$f\\(\\) \\{/,/^\\}/" "$FB")
        [ -n "$fn" ] || { echo "firstboot.sh has no function $f" >&2; return 1; }
        printf '%s\n' "$fn" >> "$FNS"
    done
    G="$BATS_TEST_TMPDIR/guest"
    mkdir -p "$G/Preferences" "$G/stub"
    DOM="$G/Preferences/com.apple.keyboardtype"
    cat > "$G/stub/defaults" <<'STUB'
#!/bin/sh
# defaults read <domain> keyboardtype | defaults write <domain> keyboardtype -dict-add <key> -int <n>
printf 'defaults %s\n' "$*" >> "$CALLS"
f="$2.plist"
case $1 in
read)
    [ -f "$f" ] || { echo "The domain/default pair of ($2, $3) does not exist" >&2; exit 1; }
    echo "{"
    while IFS='	' read -r k v; do printf '    "%s" = %s;\n' "$k" "$v"; done < "$f"
    echo "}" ;;
write)
    [ -z "${STUB_WRITE_FAILS:-}" ] || exit 1
    [ "$3 $4 $6" = "keyboardtype -dict-add -int" ] || exit 2
    grep -v "^$5	" "$f" > "$f.new" 2>/dev/null
    printf '%s\t%s\n' "$5" "$7" >> "$f.new"
    mv "$f.new" "$f" ;;
*) exit 1 ;;
esac
STUB
    chmod +x "$G/stub/defaults"
    CALLS="$G/calls"
    : > "$CALLS"
}

seed() {
    run env -i PATH="/usr/bin:/bin" G="$G" FNS="$FNS" CALLS="$CALLS" STUB_WRITE_FAILS="${STUB_WRITE_FAILS:-}" /bin/sh -c '
        . "$FNS"
        LOG=$G/guest-macosx-firstboot.log
        KEYBOARDTYPE_DOMAIN=$G/Preferences/com.apple.keyboardtype
        DEFAULTS=$G/stub/defaults
        QEMU_KEYBOARD_TYPE_KEY=1-1575-0
        QEMU_KEYBOARD_TYPE=40
        seed_keyboard_type'
}

@test "the domain, the tool, and QEMU's keyboard as the assistant names it" {
    grep -qx 'KEYBOARDTYPE_DOMAIN=/Library/Preferences/com.apple.keyboardtype' "$FB"
    grep -qx 'DEFAULTS=/usr/bin/defaults' "$FB"
    grep -qx 'QEMU_KEYBOARD_TYPE_KEY=1-1575-0' "$FB"
    grep -qx 'QEMU_KEYBOARD_TYPE=40' "$FB"
}

@test "firstboot seeds it once, on both releases" {
    [ "$(grep -c '^seed_keyboard_type$' "$FB")" -eq 1 ]
    # at the top level, inside no release's own branch
    awk '/^if /{d++} /^fi$/{d--} /^seed_keyboard_type$/{print d; exit}' "$FB" | grep -qx 0
}

@test "no plist: QEMU's keyboard is added as ANSI, and the log says why" {
    seed
    [ "$status" -eq 0 ]
    [ "$(cat "$DOM.plist")" = "1-1575-0	40" ]
    grep -q "keyboard type: $DOM had no entry for QEMU's keyboard; 1-1575-0 = 40 (ANSI) added, so Keyboard Setup Assistant does not open for it" "$G/guest-macosx-firstboot.log"
}

@test "another keyboard's entry is kept beside the one added" {
    printf '5-1452-0\t41\n' > "$DOM.plist"
    seed
    [ "$status" -eq 0 ]
    grep -qx '5-1452-0	41' "$DOM.plist"
    grep -qx '1-1575-0	40' "$DOM.plist"
}

@test "inert: an entry for QEMU's keyboard is left as it is, whatever its value, and the log names it" {
    printf '1-1575-0\t42\n' > "$DOM.plist"
    seed
    [ "$status" -eq 0 ]
    [ "$(cat "$DOM.plist")" = "1-1575-0	42" ]
    run grep -q '^defaults write' "$CALLS"
    [ "$status" -eq 1 ]
    grep -q "keyboard type: $DOM already has QEMU's keyboard (1-1575-0 = 42); left as it is" "$G/guest-macosx-firstboot.log"
}

@test "run twice: the second run finds the entry and changes nothing" {
    seed
    seed
    [ "$status" -eq 0 ]
    [ "$(grep -c '^defaults write' "$CALLS")" -eq 1 ]
    grep -q "already has QEMU's keyboard (1-1575-0 = 40); left as it is" "$G/guest-macosx-firstboot.log"
}

@test "a key that only ends like QEMU's is another keyboard's" {
    printf '11-1575-0\t41\n' > "$DOM.plist"
    seed
    grep -qx '11-1575-0	41' "$DOM.plist"
    grep -qx '1-1575-0	40' "$DOM.plist"
}

@test "a write that fails is logged as a failure, and the run goes on" {
    STUB_WRITE_FAILS=1 seed
    [ "$status" -eq 0 ]
    [ ! -e "$DOM.plist" ]
    grep -q "keyboard type: COULD NOT add 1-1575-0 to $DOM; Keyboard Setup Assistant opens for QEMU's keyboard until it is quit once" "$G/guest-macosx-firstboot.log"
}
