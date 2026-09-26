package media

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	vmguest "github.com/Mavergreen/packer-plugin-mavericks"
)

// TestRequiredFilesMatchTheirGolden: the files installer media must hold,
// in order, are testdata/golden/required-files.txt's.
func TestRequiredFilesMatchTheirGolden(t *testing.T) {
	out, err := os.ReadFile(filepath.Join(repo(t), "internal", "media", "testdata", "golden", "required-files.txt"))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	want := RequiredFiles()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the golden requires\n%s\nRequiredFiles is\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(want) != 19 {
		t.Fatalf("%d required files, want 19", len(want))
	}
}

// pinned is the embedded apple-packages.sha256's non-comment lines.
func pinned(t *testing.T) []string {
	t.Helper()
	b, err := fs.ReadFile(vmguest.Files, "assets/pins/apple-packages.sha256")
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if !strings.HasPrefix(l, "#") {
			lines = append(lines, l)
		}
	}
	return lines
}

func TestApplesPinnedSumsCoverThePackages(t *testing.T) {
	line := regexp.MustCompile(`^[0-9a-f]{64}  \./([^/]+)$`)
	names := map[string]bool{}
	for _, l := range pinned(t) {
		m := line.FindStringSubmatch(l)
		if m == nil {
			t.Fatalf("not a sha256sum line: %q", l)
		}
		names[m[1]] = true
	}
	want := map[string]bool{}
	for _, f := range RequiredFiles() {
		if name, ok := strings.CutPrefix(f, "System/Installation/Packages/"); ok {
			want[name] = true
		}
	}
	if len(want) != 16 || len(names) != len(want) {
		t.Fatalf("%d pinned names, %d packages required (want 16 of each)", len(names), len(want))
	}
	for n := range want {
		if !names[n] {
			t.Errorf("%s is required but has no pinned sum", n)
		}
	}
}

const badSum = "0000000000000000000000000000000000000000000000000000000000000000"

// sumFixtures are the pinned sums without ./, as the microVM prints
// them, and the same with BSD.pkg's value changed and X11redirect.pkg's
// line removed. The two problem names differ at their first character,
// so a locale-aware sort and a bytewise one agree.
func sumFixtures(t *testing.T) (good, bad []byte, wantBad []string) {
	t.Helper()
	var g, b strings.Builder
	var want string
	for _, l := range pinned(t) {
		sum, name, _ := strings.Cut(l, "  ./")
		g.WriteString(sum + "  " + name + "\n")
		switch name {
		case "BSD.pkg":
			want = sum
			b.WriteString(badSum + "  " + name + "\n")
		case "X11redirect.pkg":
		default:
			b.WriteString(sum + "  " + name + "\n")
		}
	}
	return []byte(g.String()), []byte(b.String()), []string{
		"BSD.pkg: FAILED -- " + badSum + " is not what Apple shipped (" + want + ")",
		"X11redirect.pkg: MISSING -- the media does not have it",
	}
}

func TestCheckAppleSums(t *testing.T) {
	good, bad, wantBad := sumFixtures(t)
	problems, err := CheckAppleSums(good)
	if err != nil || len(problems) != 0 {
		t.Fatalf("the pinned values: %q, %v", problems, err)
	}
	problems, err = CheckAppleSums(bad)
	if err == nil {
		t.Fatal("a changed value and a missing line gave no error")
	}
	if strings.Join(problems, "\n") != strings.Join(wantBad, "\n") {
		t.Fatalf("problems\n%s\nwant\n%s", strings.Join(problems, "\n"), strings.Join(wantBad, "\n"))
	}
}

// TestCheckAppleSumsMatchesItsGolden: for each fixture,
// testdata/golden/check-sums-<name>.sums is the sums this test generates
// and check-sums-<name>.problems the problems CheckAppleSums must report
// for them, one per line; it returns an error exactly when there are
// problems.
func TestCheckAppleSumsMatchesItsGolden(t *testing.T) {
	good, bad, _ := sumFixtures(t)
	for name, sums := range map[string][]byte{"good": good, "bad": bad} {
		t.Run(name, func(t *testing.T) {
			golden := filepath.Join(repo(t), "internal", "media", "testdata", "golden", "check-sums-"+name)
			wantSums, err := os.ReadFile(golden + ".sums")
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(wantSums, sums) {
				t.Fatalf("the fixture no longer matches testdata/golden/check-sums-%s.sums", name)
			}
			b, err := os.ReadFile(golden + ".problems")
			if err != nil {
				t.Fatal(err)
			}
			var want []string
			if s := strings.TrimSuffix(string(b), "\n"); s != "" {
				want = strings.Split(s, "\n")
			}
			problems, gerr := CheckAppleSums(sums)
			if strings.Join(want, "\n") != strings.Join(problems, "\n") {
				t.Errorf("the golden reports\n%s\nCheckAppleSums reports\n%s", strings.Join(want, "\n"), strings.Join(problems, "\n"))
			}
			if (gerr != nil) != (len(want) > 0) {
				t.Errorf("CheckAppleSums's error is %v, with %d problems expected", gerr, len(want))
			}
		})
	}
}
