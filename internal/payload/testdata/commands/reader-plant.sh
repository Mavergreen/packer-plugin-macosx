#!/bin/sh
# platform: host-agnostic -- never run: commandwords_test.go reads it, and each command word it should find is named for a number
one -x | two && three; four
x=$(five --arg) y=1 six
if seven; then eight; fi
run_with_timeout 30 nine -v
echo "$(ten)" 'not eleven' "not twelve"
case $1 in
    notpattern) thirteen ;;
esac
for notword in a b; do fourteen "$notword"; done
cat > f <<'TEXT'
notinheredoc
TEXT
cat > s <<'SCRIPT'
#!/bin/sh
fifteen
SCRIPT
helper() { sixteen; }
helper
seventeen --long \
    notcontinued
/usr/bin/eighteen > /dev/null 2>&1
perl -e 'my $x;
print notperl;'
x="$(nineteen a | twenty)"
twentyone "$x" && \
    twentytwo
for y in $(twentythree); do printf '%s\n' "$y"; done
case $(twentyfour) in *) ;; esac
TWENTYFIVE=${OVERRIDE:-/usr/bin/twentyfive}
"$TWENTYFIVE" -x
python - <<'PY'
import subprocess
subprocess.call(['/bin/twentysix', '-a'])
PY
nohup twentyseven &
env FOO=1 twentyeight
