#!/usr/bin/env bats
# platform: host-agnostic
#
# assets/guest/postinstall's helpers, run on their own. The OS X Installer
# runs the script as /bin/sh -- on 10.9 and 10.6, bash 3.2 in POSIX mode --
# so each helper runs here under sh and under bash --posix.

setup() {
    REPO="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
    POST="$REPO/assets/guest/postinstall"
    D="$BATS_TEST_TMPDIR/dir"
    mkdir -p "$D"
}

# _fn <name>: the function, as postinstall defines it.
_fn() { sed -n "/^$1() {\$/,/^}\$/p" "$POST"; }

# _names_in <shell> [<option>]: names_in "$D", run by that shell.
_names_in() {
    local prog
    prog="$(_fn names_in)
names_in \"\$1\""
    LC_ALL=C "$@" -c "$prog" names_in-test "$D"
}

@test "names_in lists dot-files, and names starting with two dots, beside the rest" {
    [ -n "$(_fn names_in)" ]
    touch "$D/.hid" "$D/..two" "$D/vis" "$D/with space"
    for sh in sh bash; do
        opt=
        [ "$sh" = bash ] && opt=--posix
        out=$(_names_in "$sh" ${opt:+"$opt"})
        [ "$out" = "vis with space .hid ..two " ] || { echo "$sh: [$out]"; false; }
    done
}

@test "names_in prints nothing for an empty directory, and says when there is none" {
    for sh in sh bash; do
        opt=
        [ "$sh" = bash ] && opt=--posix
        out=$(_names_in "$sh" ${opt:+"$opt"})
        [ -z "$out" ] || { echo "$sh: [$out]"; false; }
    done
    rmdir "$D"
    [ "$(_names_in sh)" = "(no such directory)" ]
}
