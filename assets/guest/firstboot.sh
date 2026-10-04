#!/bin/sh
# What Setup Assistant would have done, done once, with nobody watching.
#
# WHERE THIS RUNS
#
# On the installed Mavericks system, as root, on its first boot, launched by
# /Library/LaunchDaemons/com.mqg.firstboot.plist. Both this script and that
# plist are put there at install time by ./postinstall, which the OS X
# Installer runs because mqg-firstboot.pkg is listed in
# OSInstall.collection -- so the payload is installed *as part of the
# install*, not injected into a finished volume afterwards.
#
# WHAT IT IS A SPECIFICATION OF
#
# What Setup Assistant actually asked on a 10.9.5 install driven by hand:
# region, keyboard, no transfer, skip Apple ID, agree, create the account,
# skip iCloud Keychain, decline diagnostics. That list was written down so
# this script could be written against something observed rather than
# remembered.
#
# IT RUNS EXACTLY ONCE
#
# The last thing it does is remove its own LaunchDaemon. A first-boot script
# that runs on every boot silently undoes manual changes for the rest of the
# image's life, and the symptom turns up long after the cause. Removing the
# plist is therefore not tidiness, it is the safety property.
#
# NOTHING HERE MAY HANG FOREVER
#
# This is a 2013 OS that may be talking to Apple's 2026 servers. Every step
# runs under run_with_timeout and every failure is non-fatal: a payload that
# skips a nicety is better than a payload that never finishes and leaves an
# image with no account on it.

PATH=/usr/bin:/bin:/usr/sbin:/sbin
export PATH

CONF_DIR=/private/var/db/.mqg-firstboot
LOG=/private/var/log/mqg-firstboot.log
DAEMON=/Library/LaunchDaemons/com.mqg.firstboot.plist
DONE_MARKER=$CONF_DIR/.done

# Defaults, overridden by the conf file the build writes next to this
# script. They are here so the script is readable on its own and so a hand
# run in a rescue shell does something sensible.
MQG_FB_USER=mavsuser
MQG_FB_UID=501
MQG_FB_GID=20
MQG_FB_ADMIN_GID=80
MQG_FB_REALNAME="Mavericks User"
MQG_FB_SHELL=/bin/bash
MQG_FB_HOSTNAME=mavericks
MQG_FB_AUTOLOGIN=1
MQG_FB_SUDO=0
MQG_FB_OPENSSH=0
MQG_FB_OPENSSH_TAG=
# Read by ./postinstall, which sources this same conf file at install time
# to learn which packages to copy off the media. Declared here so the
# defaults are all in one place and a hand run in a rescue shell has them.
# shellcheck disable=SC2034
MQG_FB_EXTRA_PKGS=
# Apple's post-10.9.5 updates. `none` is the value a conf file that says
# nothing about updates produces, and that is deliberate: a conf file
# built with updates "none" has no updates lines at all, so the performance
# baseline (docs/decisions/0011) does not move when the default does.
MQG_FB_UPDATES=none
# shellcheck disable=SC2034
MQG_FB_UPDATE_PKGS=

[ -r "$CONF_DIR/firstboot.conf" ] && . "$CONF_DIR/firstboot.conf"

say() {
    echo "mqg-firstboot: $*"
    echo "$(date -u '+%Y-%m-%dT%H:%M:%SZ') mqg-firstboot: $*" >> "$LOG" 2>/dev/null
}

# 10.9 has no timeout(1), so here is one: run the command in the background
# with a watchdog beside it, and kill it if it outstays its welcome.
#
# Used for everything, not only the network-touching steps, because the
# distinction is less clean than it looks -- systemsetup and dscl both talk
# to launchd and opendirectoryd, either of which can be slow or wedged on a
# system that has never booted before.
run_with_timeout() {
    _limit=$1
    shift
    say "run (${_limit}s limit): $*"
    "$@" >> "$LOG" 2>&1 &
    _pid=$!
    (
        _n=0
        while [ "$_n" -lt "$_limit" ]; do
            kill -0 "$_pid" 2>/dev/null || exit 0
            sleep 1
            _n=$((_n + 1))
        done
        echo "mqg-firstboot: TIMED OUT after ${_limit}s: $*" >> "$LOG"
        kill -9 "$_pid" 2>/dev/null
    ) &
    _dog=$!
    wait "$_pid" 2>/dev/null
    _rc=$?
    kill "$_dog" 2>/dev/null
    [ "$_rc" -eq 0 ] || say "  -> rc=$_rc (continuing; nothing here is fatal)"
    return 0
}

if [ -e "$DONE_MARKER" ]; then
    say "already ran (marker $DONE_MARKER exists); doing nothing"
    exit 0
fi

mkdir -p "$(dirname "$LOG")" 2>/dev/null
say "starting on $(sw_vers -productVersion 2>/dev/null) build $(sw_vers -buildVersion 2>/dev/null)"

# --- Setup Assistant -------------------------------------------------------
#
# ./postinstall already created this at install time, which is what actually
# wins the race against loginwindow. Doing it again here is belt and braces
# and makes this script correct when run by hand on a system that has not
# had the package installed.
touch /private/var/db/.AppleSetupDone 2>/dev/null
touch /private/var/db/.AppleDiagnosticsSetupDone 2>/dev/null
say ".AppleSetupDone present: $([ -e /private/var/db/.AppleSetupDone ] && echo yes || echo no)"

# --- wait for Open Directory ----------------------------------------------
#
# dscl needs opendirectoryd, and a LaunchDaemon with RunAtLoad can easily
# beat it there on a system booting for the very first time.
waited=0
while ! dscl . -list /Users >/dev/null 2>&1; do
    if [ "$waited" -ge 120 ]; then
        say "opendirectoryd never answered in ${waited}s; the account cannot be created"
        break
    fi
    sleep 2
    waited=$((waited + 2))
done
say "opendirectoryd answered after ${waited}s"

# --- the account -----------------------------------------------------------
#
# Setup Assistant, on a hand-driven install, produced uid 501, gid 20
# (staff), and membership of group 80 (admin). This reproduces that rather
# than inventing a different shape of account.
#
# Idempotent on purpose: the payload is also tested against a clone that
# already has this account, and an account half-created by an interrupted
# run must be repairable by running this again.
if dscl . -read "/Users/$MQG_FB_USER" >/dev/null 2>&1; then
    say "account $MQG_FB_USER already exists; updating it rather than failing"
else
    say "creating account $MQG_FB_USER"
    run_with_timeout 60 dscl . -create "/Users/$MQG_FB_USER"
fi

run_with_timeout 60 dscl . -create "/Users/$MQG_FB_USER" UserShell "$MQG_FB_SHELL"
run_with_timeout 60 dscl . -create "/Users/$MQG_FB_USER" RealName "$MQG_FB_REALNAME"
run_with_timeout 60 dscl . -create "/Users/$MQG_FB_USER" UniqueID "$MQG_FB_UID"
run_with_timeout 60 dscl . -create "/Users/$MQG_FB_USER" PrimaryGroupID "$MQG_FB_GID"
run_with_timeout 60 dscl . -create "/Users/$MQG_FB_USER" \
    NFSHomeDirectory "/Users/$MQG_FB_USER"
run_with_timeout 60 dscl . -append /Groups/admin GroupMembership "$MQG_FB_USER"
run_with_timeout 60 dscl . -append /Groups/admin GroupMembers \
    "$(dscl . -read "/Users/$MQG_FB_USER" GeneratedUID 2>/dev/null | awk 'NR==1{print $2}')"

# The secret, if there is one, arrives in the conf file the build writes --
# never in this script, and never in the repository. An image with no
# secret in it is the default: MQG_FB_PASSWORD unset means the account has
# none, which is right for a local image that is never published and is
# reached by key.
if [ -n "${MQG_FB_PASSWORD:-}" ]; then
    run_with_timeout 60 dscl . -passwd "/Users/$MQG_FB_USER" "$MQG_FB_PASSWORD"
else
    say "no account secret configured; the account is reached by SSH key"
fi

say "group $MQG_FB_ADMIN_GID (admin) now: $(dscl . -read /Groups/admin GroupMembership 2>/dev/null)"
say "group $MQG_FB_ADMIN_GID gid check: $(dscl . -read /Groups/admin PrimaryGroupID 2>/dev/null)"

# --- home directory --------------------------------------------------------
if [ -d "/Users/$MQG_FB_USER" ]; then
    say "/Users/$MQG_FB_USER already exists"
else
    run_with_timeout 120 createhomedir -c -u "$MQG_FB_USER"
fi
if [ ! -d "/Users/$MQG_FB_USER" ]; then
    say "createhomedir produced nothing; falling back to the user template"
    mkdir -p "/Users/$MQG_FB_USER"
    ditto "/System/Library/User Template/English.lproj" "/Users/$MQG_FB_USER" \
        >> "$LOG" 2>&1
fi
chown -R "$MQG_FB_UID:$MQG_FB_GID" "/Users/$MQG_FB_USER" 2>/dev/null

# --- the SSH key -----------------------------------------------------------
#
# The key is a build-time parameter (mavericks-media's authorized_key,
# Vagrant's insecure key by default), so it travels in the package rather
# than in the repository.
if [ -s "$CONF_DIR/authorized_keys" ]; then
    mkdir -p "/Users/$MQG_FB_USER/.ssh"
    cp "$CONF_DIR/authorized_keys" "/Users/$MQG_FB_USER/.ssh/authorized_keys"
    chmod 700 "/Users/$MQG_FB_USER/.ssh"
    chmod 600 "/Users/$MQG_FB_USER/.ssh/authorized_keys"
    chown -R "$MQG_FB_UID:$MQG_FB_GID" "/Users/$MQG_FB_USER/.ssh"
    say "installed $(wc -l < "$CONF_DIR/authorized_keys") authorized key line(s)"
else
    say "no authorized_keys in the package; SSH will accept no key"
fi

# --- passwordless sudo ------------------------------------------------------
#
# Vagrant's darwin guest capability halts a box with `sudo shutdown -h
# now`, and expects that to need no prompt. Written only when the build
# asked for it (MQG_FB_SUDO=1): a build that never mentions Sudo gets no
# sudoers.d file at all, and the account's sudo rule is whatever stock
# 10.9 gives it (none, without a password).
#
# THE INCLUDE LINE, CHECKED RATHER THAN ASSUMED, AND CHECKED WHOLE
#
# REASONED, not measured against a booted 10.9 guest's /etc/sudoers: every
# sudo release since 1.7.2 (2009) ships `#includedir /private/etc/sudoers.d`
# in its default /etc/sudoers, and Apple's 10.9 sudo is 1.7.4p6 -- newer
# than that. Checked here anyway, and appended if somehow missing, guarded
# against a second run finding it already there.
#
# The append never touches the live file directly. It works on a COPY:
# copy /etc/sudoers aside, append the include line to the copy, then run
# `visudo -cf` over the WHOLE candidate -- not just the one new line --
# because a file that was already subtly broken, or an append that
# landed inside an existing line for want of a trailing newline, is
# exactly what a check of the diff alone would miss. Only a candidate
# visudo accepts in full is installed back over /etc/sudoers; any
# rejection leaves the live file untouched and is logged, loudly.
#
# THE FRAGMENT ITSELF: VALIDATED BEFORE IT TAKES EFFECT, NEVER AFTER,
# AND ONLY WRITTEN WHERE IT WOULD ACTUALLY BE READ
#
# sudo refuses to run AT ALL while any file it reads fails to parse --
# including on the very system this script is trying to make reachable.
# So the fragment is checked with `visudo -cf` at a throwaway path first,
# and installed into /etc/sudoers.d -- mode 0440, owner root:wheel, set
# in the same step as the copy rather than after it (a chmod/chown
# after a plain cp is a window, however short, with the wrong
# permissions in place) -- only once that check passes, and only if
# /etc/sudoers is confirmed to include that directory: a fragment sudo
# never reads is a fragment that does nothing, silently. Any rejection
# is loud and non-fatal: the account keeps whatever sudo rule it already
# had, which is worse than passwordless sudo but not worse than a guest
# that can no longer sudo at all.
if [ "$MQG_FB_SUDO" = "1" ]; then
    SUDOERS_MAIN=/private/etc/sudoers
    SUDOERS_DIR=/private/etc/sudoers.d
    SUDOERS_INCLUDE="#includedir $SUDOERS_DIR"

    if [ -f "$SUDOERS_MAIN" ] && ! grep -qF "$SUDOERS_INCLUDE" "$SUDOERS_MAIN" 2>/dev/null; then
        SUDOERS_MAIN_CANDIDATE=$(mktemp "${TMPDIR:-/tmp}/mqg-sudoers-main.XXXXXX")
        cp "$SUDOERS_MAIN" "$SUDOERS_MAIN_CANDIDATE"
        echo "$SUDOERS_INCLUDE" >> "$SUDOERS_MAIN_CANDIDATE"
        if visudo -cf "$SUDOERS_MAIN_CANDIDATE" >> "$LOG" 2>&1; then
            install -m 0440 -o root -g wheel "$SUDOERS_MAIN_CANDIDATE" "$SUDOERS_MAIN"
            say "sudoers: added '$SUDOERS_INCLUDE' to $SUDOERS_MAIN"
        else
            say "sudoers: $SUDOERS_MAIN plus '$SUDOERS_INCLUDE' does not pass visudo -cf;" \
                "NOT touching $SUDOERS_MAIN (see $LOG for its complaint)"
        fi
        rm -f "$SUDOERS_MAIN_CANDIDATE"
    fi

    if [ -f "$SUDOERS_MAIN" ] && grep -qF "$SUDOERS_INCLUDE" "$SUDOERS_MAIN" 2>/dev/null; then
        mkdir -p "$SUDOERS_DIR" 2>/dev/null
        SUDOERS_CANDIDATE=$(mktemp "${TMPDIR:-/tmp}/mqg-sudoers.XXXXXX")
        printf '%s ALL=(ALL) NOPASSWD: ALL\n' "$MQG_FB_USER" > "$SUDOERS_CANDIDATE"
        if visudo -cf "$SUDOERS_CANDIDATE" >> "$LOG" 2>&1; then
            SUDOERS_FILE="$SUDOERS_DIR/$MQG_FB_USER"
            install -m 0440 -o root -g wheel "$SUDOERS_CANDIDATE" "$SUDOERS_FILE"
            say "sudoers: wrote $SUDOERS_FILE (passwordless sudo for $MQG_FB_USER)"
        else
            say "sudoers: visudo rejected the candidate; NOT installing it (see $LOG for its complaint)"
        fi
        rm -f "$SUDOERS_CANDIDATE"
    else
        say "sudoers: $SUDOERS_MAIN does not include $SUDOERS_DIR;" \
            "not writing a fragment it would never read"
    fi
else
    say "sudoers: not requested; $MQG_FB_USER keeps whatever sudo rule it already had"
fi

# --- Apple's post-10.9.5 updates ------------------------------------------
#
# WHY THESE ARE HERE AND NOT RUN BY `softwareupdate`
#
# `softwareupdate` would talk to Apple's servers from inside the guest, on
# every first boot, in 2026. The answer would be whatever Apple serves that
# day, the image would stop being reproducible, and a 2013 OS negotiating
# with 2026 servers may hang. Every package installed below is a standalone
# .pkg pinned by checksum in assets/pins/sources.tsv, fetched on the build
# host by mavericks-media, carried on the installer media and copied here
# by ./postinstall.
#
# WHY BEFORE OPENSSH, WHICH IS NOT A STYLE CHOICE
#
# Security Update 2016-004's own Payload contains ./usr/bin/ssh and
# ./usr/sbin/sshd -- read out of the package, not assumed. Installed after
# the OpenSSH System-Replace package it would overwrite the symlinks that
# package puts at those paths, and the guest would quietly fall back to
# OpenSSH 6.2 -- or, if launchd's ssh job ended up pointing at something
# that no longer resolved, answer nothing at all. So: updates first, the
# family's OpenSSH second, and the openssh_usable check below then judges
# the state the guest is actually left in.
#
# WHAT SUCCESS LOOKS LIKE, AND WHAT IT DOES NOT
#
# `sw_vers` still says 10.9.5 afterwards, because ProductVersion IS 10.9.5
# -- 2016-004 is a security update, not a point release. A zero exit from
# `installer` is not evidence either. The two witnesses this script records
# are the ones that actually move:
#
#   * a RECEIPT: `pkgutil --pkgs` gains
#     com.apple.pkg.update.security.2016-004Mavericks.13F1911
#   * a BUILD NUMBER: the update carries SystemVersion.plist, so
#     `sw_vers -buildVersion` goes 13F34 -> 13F1911
#
# Both are logged below, before and after, so the log says what happened
# rather than what was attempted.
#
# A RESTART IS WANTED AND IS NOT TAKEN HERE
#
# 2016-004's PackageInfo declares postinstall-action="restart". This script
# does not reboot: the build shuts the guest down once it has checked it,
# and every later boot is a cold one, so the restart happens anyway, once,
# without this script racing the build that is waiting for SSH.
say "updates: $MQG_FB_UPDATES"
say "updates: build before: $(sw_vers -buildVersion 2>&1)"

if [ -n "$MQG_FB_UPDATE_PKGS" ]; then
    _upd_count=0
    _upd_missing=0
    _upd_n=0
    for _u in $MQG_FB_UPDATE_PKGS; do
        _upd_n=$((_upd_n + 1))
        # MQG_FB_UPDATE_IF_<n>: a path that must exist for update n to go
        # in, the check Apple's own distribution makes before a package
        # that updates an optional component (10.6.8's X11, say).
        eval "_upd_if=\${MQG_FB_UPDATE_IF_$_upd_n:-}"
        if [ -n "$_upd_if" ] && [ ! -e "$_upd_if" ]; then
            say "updates: not installing $_u: $_upd_if is not on this system"
            continue
        fi
        if [ -f "$CONF_DIR/updates/$_u" ]; then
            _upd_count=$((_upd_count + 1))
            # 3600s: 2016-004 is 354 MB of package over 6891 files, and it
            # rebuilds the kernel and dyld caches on the way out. The
            # OpenSSH base package gets 900s and is 12 MB.
            run_with_timeout 3600 installer -verbose \
                -pkg "$CONF_DIR/updates/$_u" -target /
        else
            _upd_missing=$((_upd_missing + 1))
            say "updates: NOT ON THE TARGET VOLUME: $_u"
        fi
    done
    say "updates: installed $_upd_count package(s), $_upd_missing missing"
    say "updates: build after: $(sw_vers -buildVersion 2>&1)"
    say "updates: sw_vers still reports: $(sw_vers -productVersion 2>&1)" \
        "(expected -- a security update is not a point release)"
    say "updates: receipts: $(pkgutil --pkgs 2>/dev/null \
        | grep -i -E 'update|safari|itunes' | tr '\n' ' ')"
    # Keeping 685 MB of installed package on a 60 GB image would be the
    # only trace of it that costs anything.
    rm -rf "$CONF_DIR/updates"
else
    say "updates: none requested; this image is stock 10.9.5 (13F34)"
fi

# --- the family's own OpenSSH ---------------------------------------------
#
# WHAT THIS FIXES
#
# Stock 10.9 is OpenSSH 6.2p2. Ed25519 arrived in 6.5, so a modern key in
# authorized_keys is a line this sshd cannot parse -- "Permission denied
# (publickey)" from a server that is otherwise working perfectly. And 6.2
# offers only ssh-rsa and ssh-dss host keys, which a 2026 client refuses
# outright. Both cost a full install to find.
#
# Mavergreen/openssh already builds current OpenSSH for 10.9. The
# packages travel on the installer media and ./postinstall copied them to
# $CONF_DIR/pkgs at install time -- read that file for why they are not
# listed in OSInstall.collection.
#
# ORDER MATTERS, TWICE
#
# 1. Base package first, replacement second: the replacement symlinks the
#    system paths at files the base package installs.
# 2. This whole block runs BEFORE Remote Login is enabled. ssh.plist is an
#    inetd-style job: launchd holds the listening socket and execs
#    /usr/libexec/sshd-keygen-wrapper per connection, so the very first
#    connection already gets the new sshd and nothing has to be restarted
#    underneath a live session.
#
# THE WRAPPER WE WRITE OURSELVES, AND WHY
#
# 10.9's /System/Library/LaunchDaemons/ssh.plist names
# /usr/libexec/sshd-keygen-wrapper as its Program. The replacement
# package's postinstall symlinks that path to
# /usr/local/libexec/sshd-keygen-wrapper -- but its payload does not
# contain that file (checked: the published 10.5p1-mavericks.2 Payload has
# no sshd-keygen-wrapper). The symlink therefore dangles, launchd cannot
# exec it, and the guest ends up with NO working sshd at all.
#
# So we write it, before installing the replacement, and the symlink lands
# on a real file. Ours also does the other thing a 10.9 guest needs:
# Apple's wrapper only ever generates rsa1/rsa/dsa host keys in /etc, and a
# modern sshd reads /usr/local/etc/ssh_host_{rsa,ecdsa,ed25519}_key. Without
# an Ed25519 host key the second defect above is only half fixed.
#
# EXIT CONDITION: delete write_sshd_keygen_wrapper when
# Mavergreen/openssh ships a sshd-keygen-wrapper of its own. Until
# then this is a compensation for a sibling defect, stated as one.
say "openssh: requested=$MQG_FB_OPENSSH tag=${MQG_FB_OPENSSH_TAG:-none}"

write_sshd_keygen_wrapper() {
    mkdir -p /usr/local/libexec 2>/dev/null
    cat > /usr/local/libexec/sshd-keygen-wrapper <<'MQG_WRAPPER_EOF'
#!/bin/sh
# Written by mqg-firstboot. Apple's /usr/libexec/sshd-keygen-wrapper, but
# for the OpenSSH in /usr/local: generate whatever host keys are missing,
# then exec the modern sshd. See write_sshd_keygen_wrapper in firstboot.sh.
for _t in rsa ecdsa ed25519; do
    _f="/usr/local/etc/ssh_host_${_t}_key"
    [ -f "$_f" ] || /usr/local/bin/ssh-keygen -q -t "$_t" -f "$_f" \
        -N "" -C "" < /dev/null > /dev/null 2>&1
done
exec /usr/local/sbin/sshd "$@"
MQG_WRAPPER_EOF
    chmod 755 /usr/local/libexec/sshd-keygen-wrapper 2>/dev/null
}

# Everything the replacement package moved aside, put back. Its preinstall
# copies the vanilla binaries and configs here before it touches anything,
# which is what makes a rollback a copy rather than a reinstall of the OS.
restore_vanilla_openssh() {
    _bk=/var/backups/vanilla-openssh
    [ -d "$_bk" ] || { say "no $_bk to restore from"; return 0; }
    cd "$_bk" || return 0
    find . -type f -print | while read -r _rel; do
        _rel=${_rel#./}
        rm -f "/$_rel" 2>/dev/null
        cp -p "$_bk/$_rel" "/$_rel" 2>/dev/null
    done
    cd / || :
    say "restored the vanilla OpenSSH from $_bk"
}

# Would launchd's ssh job actually work? Three questions, in the order they
# fail: is there a modern sshd, does the path ssh.plist execs resolve to
# something runnable, and does that sshd accept its own configuration.
openssh_usable() {
    [ -x /usr/local/sbin/sshd ] || { say "  no /usr/local/sbin/sshd"; return 1; }
    [ -x /usr/libexec/sshd-keygen-wrapper ] \
        || { say "  /usr/libexec/sshd-keygen-wrapper does not resolve"; return 1; }
    /usr/local/sbin/sshd -t -f /usr/local/etc/sshd_config >> "$LOG" 2>&1 \
        || { say "  sshd -t rejected /usr/local/etc/sshd_config"; return 1; }
    return 0
}

if [ "$MQG_FB_OPENSSH" = "1" ]; then
    fb_base=
    fb_replace=
    for _p in "$CONF_DIR"/pkgs/*.pkg; do
        [ -f "$_p" ] || continue
        case $_p in
            *System-Replace*) fb_replace=$_p ;;
            *)                fb_base=$_p ;;
        esac
    done
    if [ -z "$fb_base" ] || [ -z "$fb_replace" ]; then
        say "openssh: packages missing from $CONF_DIR/pkgs" \
            "(base=${fb_base:-none} replace=${fb_replace:-none});" \
            "leaving the stock OpenSSH 6.2 in place"
    else
        run_with_timeout 900 installer -verbose -pkg "$fb_base" -target /
        write_sshd_keygen_wrapper
        run_with_timeout 300 installer -verbose -pkg "$fb_replace" -target /
        # Generate the host keys now rather than on the first connection,
        # so `sshd -t` below has something to validate and so the verdict
        # this script logs is about a system that is actually ready.
        for _t in rsa ecdsa ed25519; do
            _f="/usr/local/etc/ssh_host_${_t}_key"
            [ -f "$_f" ] || run_with_timeout 120 /usr/local/bin/ssh-keygen \
                -q -t "$_t" -f "$_f" -N "" -C ""
        done
        if openssh_usable; then
            say "openssh: $(/usr/local/bin/ssh -V 2>&1) is now the system ssh"
            say "openssh: host keys: $(echo /usr/local/etc/ssh_host_*_key)"
            rm -rf "$CONF_DIR/pkgs"
        else
            say "openssh: the replacement did not come up clean; ROLLING BACK"
            restore_vanilla_openssh
            say "openssh: system ssh is now $(ssh -V 2>&1)"
        fi
    fi
else
    say "openssh: not requested; this image keeps the stock OpenSSH 6.2"
fi

# --- Remote Login ----------------------------------------------------------
#
# Both spellings, because they fail differently. systemsetup is the
# documented one but asks an interactive yes/no question without -f; loading
# the job directly is what actually has to end up true.
run_with_timeout 60 systemsetup -f -setremotelogin on
run_with_timeout 60 launchctl load -w /System/Library/LaunchDaemons/ssh.plist
say "remote login: $(systemsetup -getremotelogin 2>&1)"

# --- sleep, screensaver ----------------------------------------------------
#
# An image that sleeps is an image that stops answering SSH, and an image
# that runs a screensaver spends its CPU on it -- which would land in the
# performance measurements as though it were the guest being slow.
run_with_timeout 60 systemsetup -setsleep Never
run_with_timeout 60 systemsetup -setcomputersleep Never
run_with_timeout 60 systemsetup -setdisplaysleep Never
run_with_timeout 60 systemsetup -setharddisksleep Never

defaults write /Library/Preferences/com.apple.screensaver loginWindowIdleTime 0
su - "$MQG_FB_USER" -c \
    'defaults -currentHost write com.apple.screensaver idleTime 0' \
    >> "$LOG" 2>&1
pmset -a displaysleep 0 disksleep 0 sleep 0 >> "$LOG" 2>&1

# --- software update -------------------------------------------------------
#
# This is the step most likely to hang: a 2013 OS asking Apple's 2026
# servers about updates. Under run_with_timeout, non-fatal. Which
# post-10.9.5 updates a guest carries is decided at build time and
# installed above from pinned packages (docs/decisions/0011). Turning the
# *schedule* off stops the guest reaching out on its own, which a
# reproducible image must not do.
run_with_timeout 120 softwareupdate --schedule off

# --- identity --------------------------------------------------------------
#
# Left to itself, Setup Assistant derived "Maverickss-iMac" from the full
# name. The hostname is set deliberately instead.
run_with_timeout 30 scutil --set ComputerName "$MQG_FB_HOSTNAME"
run_with_timeout 30 scutil --set HostName "$MQG_FB_HOSTNAME"
run_with_timeout 30 scutil --set LocalHostName "$MQG_FB_HOSTNAME"

# --- auto-login ------------------------------------------------------------
#
# Setup Assistant, on a hand-driven install, turned this on for a
# single-user system, and everything unattended leans on it: an unattended
# boot must not stall at a login window. Skipping Setup Assistant means
# nothing turned it on, so we do.
#
# /etc/kcpassword holds the account secret obfuscated -- not encrypted --
# with a fixed key. That is Apple's design, it is why auto-login and "never
# publish the guest image" belong in the same sentence, and it is written
# here only when a secret was configured at build time.
if [ "$MQG_FB_AUTOLOGIN" = "1" ]; then
    defaults write /Library/Preferences/com.apple.loginwindow \
        autoLoginUser "$MQG_FB_USER"
    if [ -n "${MQG_FB_PASSWORD:-}" ]; then
        printf '%s' "$MQG_FB_PASSWORD" | perl -e '
            my @k = (0x7D,0x89,0x52,0x23,0xD2,0xBC,0xDD,0xEA,0xA3,0xB9,0x1F);
            my $p = do { local $/; <STDIN> };
            my @o;
            for my $i (0 .. length($p) - 1) {
                push @o, ord(substr($p, $i, 1)) ^ $k[$i % 11];
            }
            push @o, $k[scalar(@o) % 11] while scalar(@o) % 12;
            print pack("C*", @o);
        ' > /private/etc/kcpassword 2>>"$LOG"
        chmod 600 /private/etc/kcpassword 2>/dev/null
    fi
    say "auto-login set to $MQG_FB_USER"
fi

# --- verdict ---------------------------------------------------------------
say "account: $(id "$MQG_FB_USER" 2>&1)"
say "ssh: $(ssh -V 2>&1)"
say "sshd job: $(launchctl list 2>/dev/null | grep -c com.openssh.sshd) entries"

# Copy the log somewhere a host can find it without a running VM: the log in
# /private/var/log is fine once SSH works, and useless when it does not.
cp "$LOG" /.mqg-firstboot.log 2>/dev/null

# Written BEFORE the LaunchDaemon is removed, so that a machine losing power
# between the two lines still refuses to run this again.
mkdir -p "$CONF_DIR" 2>/dev/null
date -u '+%Y-%m-%dT%H:%M:%SZ' > "$DONE_MARKER"

# --- run exactly once ------------------------------------------------------
#
# REMOVE THE FILE FIRST, AND DO NOT UNLOAD.
#
# `launchctl unload "$DAEMON"` before `rm -f` would unload the job that is
# running this script, which terminates the script: the `rm` would never
# happen, the plist would stay in /Library/LaunchDaemons, and every later
# boot would start firstboot.sh again (MEASURED: exactly that, on a real
# first boot). The marker above is the second guard, and it is the one that
# held when the first one broke. Both are worth keeping for exactly that
# reason.
#
# There is nothing to unload anyway. The job has no KeepAlive, so launchd
# does not restart it, and removing the plist means there is no job at the
# next boot.
say "removing the LaunchDaemon so this never runs again"
rm -f "$DAEMON"
say "LaunchDaemon gone: $([ -e "$DAEMON" ] && echo NO || echo yes)"

# Restart loginwindow so auto-login takes effect on this boot rather than
# the next one. loginwindow started before the account existed, so it has no
# idea about it; this is what turns a login window into a desktop without a
# reboot.
if [ "$MQG_FB_AUTOLOGIN" = "1" ]; then
    say "restarting loginwindow so auto-login applies now"
    killall -HUP loginwindow 2>/dev/null || killall loginwindow 2>/dev/null
fi

say "done"
exit 0
