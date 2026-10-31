package payload

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	macosx "github.com/Mavergreen/packer-plugin-macosx"
	"github.com/Mavergreen/packer-plugin-macosx/internal/proc"
)

// The commands each environment the payload runs in has, read from
// Apple's own images (testdata/commands/README.md says how):
//
//	snowleopard-installer.txt  10.6's retail DVD, which the installer boots
//	mavericks-installer.txt    10.9's OS X Base System, on our media
//	snowleopard-installed.txt  10.6.0 as installed, before any update
//	mavericks-installed.txt    10.9.5 as installed
//
// Each line is a path: /bin, /sbin, /usr/bin, /usr/sbin, and for an
// installed system /usr/libexec.
func environmentCommands(t *testing.T, list string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "commands", list))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var paths []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		if l := strings.TrimSpace(s.Text()); l != "" {
			paths = append(paths, l)
		}
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	if len(paths) < 100 {
		t.Fatalf("%s lists %d commands; it is not the list", list, len(paths))
	}
	return paths
}

// onlyThese is a directory holding exactly the commands named in paths:
// each a link to this host's own command of that name, or, for one this
// host lacks, testdata/commands/absent-on-host.sh, which records the call
// in log and succeeds. Nothing else is in it, so with PATH set to it alone
// a command the environment lacks is "not found".
func onlyThese(t *testing.T, paths []string) string {
	t.Helper()
	dir := t.TempDir()
	absent, err := os.ReadFile(filepath.Join("testdata", "commands", "absent-on-host.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		name := filepath.Base(p)
		dst := filepath.Join(dir, name)
		if _, err := os.Lstat(dst); err == nil {
			continue // /bin/x and /usr/bin/x: one is enough
		}
		if real, err := exec.LookPath(name); err == nil {
			if err := os.Symlink(real, dst); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(dst, absent, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// runOnly runs script under interp (with shellArgs before it, and
// scriptArgs after), with PATH holding only list's commands (onlyThese),
// and returns its output and every command it tried that list lacks.
//
// What the shell says was not found is not enough: a call whose stderr
// goes to /dev/null, as postinstall's chmod, chown and grep calls' does,
// says nothing. So it runs with -x, which traces each command to the
// shell's own stderr before the command's redirections apply, and under
// bash every traced command must be a builtin, a function the script
// defines, or one of list's commands. Under dash the trace is not read:
// dash writes a trace line in pieces, and a pipeline's processes, tracing
// at once, interleave them mid-word ("diskutil/usr/bin/grep"); bash writes
// each line whole. The same script runs the same commands under both, so
// the bash run (--posix, the installers' own /bin/sh) is the one that
// judges them, and the dash run is held to "not found" alone.
//
// known names commands the caller stands in for itself (a stub run by its
// path, where the guest runs a listed one); its exit status is returned.
func runOnly(t *testing.T, interp string, shellArgs []string, script string, scriptArgs []string, list string, known []string, env ...string) (string, []string, int) {
	t.Helper()
	dir := t.TempDir()
	absentLog := filepath.Join(dir, "absent-on-host.log")
	bin := onlyThese(t, environmentCommands(t, list))
	// -x after the shell's own long options (bash takes those first)
	args := append(append(append(append([]string{}, shellArgs...), "-x"), script), scriptArgs...)
	cmd := exec.Command(interp, args...)
	cmd.Env = append([]string{"PATH=" + bin, "HOME=" + dir, "TMPDIR=" + dir, "ABSENT_ON_HOST_LOG=" + absentLog, "PS4=" + tracePS4}, env...)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("%s: %v", script, err)
		}
		code = exit.ExitCode()
	}
	combined := stdout.String() + stderr.String()
	src, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	ok := map[string]bool{"[": true, ":": true, ".": true}
	for _, k := range known {
		ok[k] = true
	}
	for _, l := range strings.Split(string(src), "\n") {
		if m := funcDefRe.FindStringSubmatch(l); m != nil {
			ok[m[1]] = true
		}
	}
	for _, p := range environmentCommands(t, list) {
		ok[p] = true
		if inPATH(p) {
			ok[filepath.Base(p)] = true
		}
	}
	dash := !bashLike(interp)
	var misses []string
	for _, l := range strings.Split(stderr.String(), "\n") {
		if strings.Contains(l, "not found") {
			misses = append(misses, l)
			continue
		}
		if dash {
			continue
		}
		m := traceRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		if w := tracedCommand(m[1]); w != "" && !ok[w] && !shellBuiltins[w] {
			misses = append(misses, w+" (traced: "+m[1]+")")
		}
	}
	if b, _ := os.ReadFile(absentLog); len(b) > 0 {
		t.Logf("listed commands this host lacks, called and stood in for:\n%s", b)
	}
	return combined, misses, code
}

// bashLike is whether interp is bash, under whatever name (macOS's sh).
func bashLike(interp string) bool {
	out, err := exec.Command(interp, "-c", "echo ${BASH_VERSION:-}").Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// tracePS4 marks runOnly's trace lines; bash repeats its first character
// once per level of command substitution.
const tracePS4 = "@@xtrace@@ "

// traceRe is a traced line: tracePS4, its "@" repeated per level of command
// substitution, and the command.
var traceRe = regexp.MustCompile(`^@+xtrace@@ (.*)$`)

// tracedCommand is the command word of one traced command: its first word
// past any NAME=value assignments, with the shells' single quoting undone;
// "" for an assignment alone.
func tracedCommand(line string) string {
	for _, w := range traceWords(line) {
		if !assignmentRe.MatchString(w) {
			return w
		}
	}
	return ""
}

// traceWords splits a traced command line into words, as sh and bash
// quote them in a trace: in single quotes, with '\” for a quote.
func traceWords(line string) []string {
	var words []string
	var w strings.Builder
	in, quoted := false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quoted && c == '\'':
			quoted = false
		case quoted:
			w.WriteByte(c)
		case c == '\'':
			quoted, in = true, true
		case c == '\\' && i+1 < len(line):
			i++
			w.WriteByte(line[i])
			in = true
		case c == ' ' || c == '\t':
			if in {
				words = append(words, w.String())
				w.Reset()
				in = false
			}
		default:
			w.WriteByte(c)
			in = true
		}
	}
	if in {
		words = append(words, w.String())
	}
	return words
}

// TestRunOnlySeesAMissingCommandWhateverItsStderr: a command list lacks
// is found though its own stderr goes nowhere, as postinstall's chmod,
// chown and grep calls' do (2>/dev/null), by the bash --posix run (see
// runOnly for why dash's trace is not read); and an unredirected one by
// both.
func TestRunOnlySeesAMissingCommandWhateverItsStderr(t *testing.T) {
	script := filepath.Join(t.TempDir(), "plant")
	if err := os.WriteFile(script, []byte("cat </dev/null\ntr a b </dev/null 2>/dev/null\nx=$(cut -c1 </dev/null 2>/dev/null)\nprintf '%s\\n' \"$x\"\nuniq </dev/null\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, shell := range [][]string{{"sh"}, {"bash", "--posix"}} {
		interp, err := exec.LookPath(shell[0])
		if err != nil {
			t.Skipf("no %s here", shell[0])
		}
		_, misses, _ := runOnly(t, interp, shell[1:], script, nil, "snowleopard-installer.txt", nil)
		got := strings.Join(misses, "\n")
		want := []string{"uniq"} // not found, said on stderr: both shells
		if bashLike(interp) {
			want = append(want, "tr", "cut") // redirected: bash's trace
		}
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: the missing %s was not seen; misses: %q", shell[0], w, misses)
			}
		}
		if strings.Contains(got, "cat") || strings.Contains(got, "printf") {
			t.Errorf("%s: a listed command or a builtin counted as missing: %q", shell[0], misses)
		}
	}
}

// TestTheBuiltPostinstallNeedsOnlyItsInstallersCommands runs postinstall,
// as each release's media builds it, with PATH holding only the commands
// that release's installer environment has: a command it lacks is a
// failure here, on any host, rather than a carried update silently missing
// from a guest (F23: 10.6's DVD has no tr and no dirname). runOnly sees a
// missing command by its trace, so one whose stderr goes nowhere counts
// too. It runs under the host's sh and under bash --posix, the installer's
// own /bin/sh. postinstall must also log the PATH it runs with, which a
// real build's postinstall.log then shows (F23 review: the one link of the
// installers' PATH no log recorded).
func TestTheBuiltPostinstallNeedsOnlyItsInstallersCommands(t *testing.T) {
	for _, tc := range []struct {
		release, list string
		files         func(t *testing.T, media string) []MediaFile
		openssh       bool
	}{
		{"", "mavericks-installer.txt", func(t *testing.T, media string) []MediaFile {
			return []MediaFile{{Path: fakePkg(t, media, "guest-macosx-update-01-SecUpd.pkg"), Name: "guest-macosx-update-01-SecUpd.pkg"}}
		}, true},
		{"snowleopard", "snowleopard-installer.txt", func(t *testing.T, media string) []MediaFile {
			return []MediaFile{
				{Path: fakePkg(t, media, "Part0.pkg"), Name: "Part0.pkg", Member: true},
				{Path: fakeDist(t, media, "guest-macosx-update-01-combo.dist"), Name: "guest-macosx-update-01-combo.dist"},
				{Path: fakePkg(t, media, "guest-macosx-update-02-SecUpd.pkg"), Name: "guest-macosx-update-02-SecUpd.pkg"},
			}
		}, false},
	} {
		for _, shell := range [][]string{{"sh"}, {"bash", "--posix"}} {
			t.Run(tc.list+" "+strings.Join(shell, " "), func(t *testing.T) {
				interp, err := exec.LookPath(shell[0])
				if err != nil {
					t.Skipf("no %s here", shell[0])
				}
				dir := t.TempDir()
				keyPath := filepath.Join(dir, "id_rsa.pub")
				if err := os.WriteFile(keyPath, rsaPubKey(t), 0o644); err != nil {
					t.Fatal(err)
				}
				media := filepath.Join(dir, "media")
				if err := os.MkdirAll(media, 0o755); err != nil {
					t.Fatal(err)
				}
				c := DefaultConfig()
				c.SSHKey = keyPath
				c.Release = tc.release
				c.Updates = "security"
				c.UpdatePkgs = tc.files(t, media)
				if tc.openssh {
					c.OpenSSHTag = "10.5p1-mavericks.6"
					c.OpenSSHPkgs = []string{fakePkg(t, media, "openssh-10.5p1.pkg"), fakePkg(t, media, "openssh-10.5p1-System-Replace.pkg")}
				}
				out := filepath.Join(media, "guest-macosx-firstboot.pkg")
				if _, err := Build(context.Background(), proc.Exec{}, c, out, t.Logf); err != nil {
					t.Fatal(err)
				}
				pkg, err := os.ReadFile(out)
				if err != nil {
					t.Fatal(err)
				}
				postPath := filepath.Join(dir, "postinstall")
				if err := os.WriteFile(postPath, extractPostinstall(t, pkg), 0o755); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(dir, "target")
				if err := os.MkdirAll(target, 0o755); err != nil {
					t.Fatal(err)
				}
				output, misses, code := runOnly(t, interp, shell[1:], postPath, []string{out, "/dest", target, "/"}, tc.list, nil)
				if code != 0 {
					t.Fatalf("postinstall exited %d:\n%s", code, output)
				}
				if !regexp.MustCompile(`(?m)postinstall: PATH: \S+`).MatchString(output) {
					t.Errorf("postinstall does not log its PATH:\n%s", output)
				}
				for _, m := range misses {
					t.Errorf("postinstall ran a command %s's environment does not have: %s", tc.list, m)
				}
				conf := filepath.Join(target, "private", "var", "db", ".guest-macosx-firstboot")
				for _, f := range c.UpdatePkgs {
					if _, err := os.Stat(filepath.Join(conf, "updates", f.Name)); err != nil {
						t.Errorf("%s not carried to the target: %v", f.Name, err)
					}
				}
				for _, p := range c.OpenSSHPkgs {
					if _, err := os.Stat(filepath.Join(conf, "pkgs", filepath.Base(p))); err != nil {
						t.Errorf("%s not carried to the target: %v", filepath.Base(p), err)
					}
				}
				if _, err := os.Stat(filepath.Join(target, "private", "var", "db", ".AppleSetupDone")); err != nil {
					t.Errorf("postinstall did not reach its end: %v", err)
				}
			})
		}
	}
}

// missingCommands is each command words runs that none of paths provides,
// with its lines: a bare name by its base name in one of paths' entries in
// a directory on PATH (inPATH), an absolute path exactly. /usr/local's are this project's own (the
// OpenSSH it installs, the helpers firstboot.sh writes), never Apple's.
func missingCommands(words map[string][]int, paths []string) []string {
	have := map[string]bool{}
	for _, p := range paths {
		have[p] = true
		if inPATH(p) {
			have[filepath.Base(p)] = true
		}
	}
	var missing []string
	for _, w := range sortedWords(words) {
		if strings.HasPrefix(w, "/usr/local/") || have[w] {
			continue
		}
		missing = append(missing, fmt.Sprintf("%s (line %v)", w, words[w]))
	}
	return missing
}

// The scripts the payload runs, statically, against the commands of where
// they run: autoinstall.sh and postinstall in each release's installer,
// firstboot.sh (with the scripts it writes) on each release as installed --
// 10.6.0's own, before any update, since its first boot runs there. A
// command run through a variable the script set to a path ("$DISKUTIL",
// "$PLISTBUDDY") is read as that path, and one it did not is a failure.
func TestThePayloadsScriptsRunOnlyTheirEnvironmentsCommands(t *testing.T) {
	for _, tc := range []struct {
		script string
		lists  []string
	}{
		{"assets/guest/autoinstall/autoinstall.sh", []string{"snowleopard-installer.txt", "mavericks-installer.txt"}},
		{"assets/guest/postinstall", []string{"snowleopard-installer.txt", "mavericks-installer.txt"}},
		{"assets/guest/firstboot.sh", []string{"snowleopard-installed.txt", "mavericks-installed.txt"}},
	} {
		b, err := fs.ReadFile(macosx.Files, tc.script)
		if err != nil {
			t.Fatal(err)
		}
		words, unreadable := commandWords(string(b))
		for _, u := range unreadable {
			t.Errorf("%s, %s", tc.script, u)
		}
		if len(words) < 5 {
			t.Fatalf("%s: read %d commands; the reader is not reading it", tc.script, len(words))
		}
		for _, list := range tc.lists {
			for _, m := range missingCommands(words, environmentCommands(t, list)) {
				t.Errorf("%s runs %s, which %s does not have", tc.script, m, list)
			}
		}
	}
}

func TestTheCommandReaderFindsCommandsAndOnlyCommands(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("testdata", "commands", "reader-plant.sh"))
	if err != nil {
		t.Fatal(err)
	}
	words, unreadable := commandWords(string(script))
	if len(unreadable) > 0 {
		t.Errorf("refused %q", unreadable)
	}
	got := strings.Join(sortedWords(words), " ")
	want := "/bin/twentysix /usr/bin/eighteen /usr/bin/twentyfive cat eight env fifteen five four fourteen nine nineteen nohup one perl python run_with_timeout seven seventeen six sixteen ten thirteen three twenty twentyeight twentyfour twentyone twentyseven twentythree twentytwo two"
	if got != want {
		t.Errorf("commandWords read:\n%s\nwant:\n%s", got, want)
	}
	w, _ := commandWords("awk 1; nosuchcommand x\n")
	missing := missingCommands(w, []string{"/usr/bin/awk"})
	if len(missing) != 1 || !strings.HasPrefix(missing[0], "nosuchcommand") {
		t.Errorf("missingCommands = %v, want nosuchcommand alone", missing)
	}
}

func TestTheCommandReaderRecognizesCheckedTimeout(t *testing.T) {
	for _, script := range []string{
		"run_with_timeout 3600 installer -pkg x\n",
		"if run_with_timeout --checked 3600 installer -pkg x; then true; fi\n",
	} {
		words, unreadable := commandWords(script)
		if len(unreadable) != 0 || strings.Join(sortedWords(words), " ") != "installer run_with_timeout" {
			t.Errorf("%q: words=%v, unreadable=%v", script, words, unreadable)
		}
	}
}

// inPATH is whether p is in one of the directories every script here runs
// with on PATH: the installers' (launchd's default), and firstboot.sh's
// own PATH=/usr/bin:/bin:/usr/sbin:/sbin. /usr/libexec, in the installed
// lists, is not one of them: a command there runs only by its path.
func inPATH(p string) bool {
	switch filepath.Dir(p) {
	case "/bin", "/sbin", "/usr/bin", "/usr/sbin":
		return true
	}
	return false
}

func TestABareCommandCountsOnlyFromADirectoryOnPATH(t *testing.T) {
	list := []string{"/usr/libexec/PlistBuddy", "/usr/bin/awk"}
	w, _ := commandWords("awk 1\nPlistBuddy -c x f\n/usr/libexec/PlistBuddy -c y f\n")
	missing := missingCommands(w, list)
	if len(missing) != 1 || !strings.HasPrefix(missing[0], "PlistBuddy ") {
		t.Errorf("missingCommands = %q, want the bare PlistBuddy alone", missing)
	}
}

// The constructs the reader cannot read are refused, each naming its line,
// so a later use is a failure rather than a command no check sees; the ones
// it can (a -c string that is a literal) are read.
func TestTheCommandReaderRefusesWhatItCannotRead(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("testdata", "commands", "unreadable-plant.sh"))
	if err != nil {
		t.Fatal(err)
	}
	words, unreadable := commandWords(string(script))
	got := strings.Join(sortedWords(words), " ")
	if want := "/bin/sh alpha beta cat find gamma sh su sudo xargs"; got != want {
		t.Errorf("commandWords read:\n%s\nwant:\n%s", got, want)
	}
	var lines []string
	for _, u := range unreadable {
		lines = append(lines, strings.SplitN(u, ":", 2)[0])
	}
	if want := "line 5|line 6|line 8|line 9|line 10|line 13|line 16"; strings.Join(lines, "|") != want {
		t.Errorf("refused %q, want lines %s", unreadable, want)
	}
}

// TestAutoinstallShNeedsOnlyItsInstallersCommands runs autoinstall.sh (the
// media's rc.cdrom.local) as a dry run under each installer's command list,
// as postinstall is run, through both of its disk-choice branches: one
// blank disk beside the installer media, which it chooses, and two, which
// it refuses as ambiguous. diskutil is testdata/commands/diskutil-table.sh,
// run by the path GUEST_MACOSX_DISKUTIL gives, where the guest runs
// /usr/sbin/diskutil (in both lists).
func TestAutoinstallShNeedsOnlyItsInstallersCommands(t *testing.T) {
	stub, err := os.ReadFile(filepath.Join("testdata", "commands", "diskutil-table.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script, err := fs.ReadFile(macosx.Files, "assets/guest/autoinstall/autoinstall.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, list := range []string{"snowleopard-installer.txt", "mavericks-installer.txt"} {
		for _, shell := range [][]string{{"sh"}, {"bash", "--posix"}} {
			for _, tc := range []struct {
				name, table, want string
				code              int
			}{
				{"one blank disk", "disk0 64424509440 0\ndisk1 7527727104 2\n", "target is /dev/disk0", 0},
				{"two blank disks", "disk0 64424509440 0\ndisk1 7527727104 2\ndisk2 64424509440 0\n", "more than one disk matches (disk0 disk2 ) -- ambiguous", 3},
			} {
				t.Run(list+" "+strings.Join(shell, " ")+" "+tc.name, func(t *testing.T) {
					interp, err := exec.LookPath(shell[0])
					if err != nil {
						t.Skipf("no %s here", shell[0])
					}
					dir := t.TempDir()
					diskutil := filepath.Join(dir, "diskutil")
					if err := os.WriteFile(diskutil, stub, 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(diskutil+".table", []byte(tc.table), 0o644); err != nil {
						t.Fatal(err)
					}
					path := filepath.Join(dir, "rc.cdrom.local")
					if err := os.WriteFile(path, script, 0o644); err != nil {
						t.Fatal(err)
					}
					output, misses, code := runOnly(t, interp, shell[1:], path, nil, list, []string{diskutil},
						"GUEST_MACOSX_AUTOINSTALL_DRY_RUN=1", "GUEST_MACOSX_DISKUTIL="+diskutil,
						"GUEST_MACOSX_AUTOINSTALL_LOG="+filepath.Join(dir, "auto.log"), "GUEST_MACOSX_TARGET_WAIT=0")
					for _, m := range misses {
						t.Errorf("autoinstall.sh ran a command %s's environment does not have: %s", list, m)
					}
					if code != tc.code || !strings.Contains(output, tc.want) {
						t.Errorf("exit %d, want %d, and output saying %q:\n%s", code, tc.code, tc.want, output)
					}
				})
			}
		}
	}
}
