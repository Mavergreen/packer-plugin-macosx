package inputs

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Mavergreen/packer-plugin-macosx/internal/fetch"
	"github.com/Mavergreen/packer-plugin-macosx/internal/pins"
)

// repoRoot is the checkout this test binary was built from.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, here, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(here), "..", "..")
}

func requireTools(t *testing.T, tools ...string) {
	t.Helper()
	for _, c := range tools {
		if _, err := exec.LookPath(c); err != nil {
			t.Skipf("%s not available; CI runs this", c)
		}
	}
}

// golden reads testdata/golden/<name> (testdata/golden/README.md says
// what each file is).
func golden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "internal", "inputs", "testdata", "golden", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// compilerLineFrom pulls the "compiler" row's value out of a golden
// listing, so RepoRows can be handed the same value without probing gcc
// itself (the test is about the listing, not the compiler probe).
func compilerLineFrom(t *testing.T, listing string) string {
	t.Helper()
	for _, line := range strings.Split(listing, "\n") {
		if k, v, ok := strings.Cut(line, "\t"); ok && k == "compiler" {
			return v
		}
	}
	t.Fatal("no compiler row in the golden listing")
	return ""
}

// TestListingsMatchTheirGoldens holds each part's input listing equal
// to testdata/golden/listing-<part>.txt, byte for byte: the rows whose
// digest names a data source's output, and whose change rebuilds it.
func TestListingsMatchTheirGoldens(t *testing.T) {
	reg, err := pins.Embedded()
	if err != nil {
		t.Fatal(err)
	}

	securityStamp, err := UpdatesStamp(reg, "security")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		part   string
		extras []Row
	}{
		{"esd", nil},
		{"payload", nil},
		{"opencore", nil},
		{"ovmf", nil},
		{"efi", []Row{{"smbios", "iMac14,2"}, {"opencore-artifacts", "absent"}}},
		{"media", append([]Row{{"payload", "absent"}, {"openssh-enabled", "1"}}, securityStamp...)},
	} {
		t.Run(tc.part, func(t *testing.T) {
			want := golden(t, "listing-"+tc.part+".txt")

			compiler := ""
			if tc.part == "opencore" || tc.part == "ovmf" {
				compiler = compilerLineFrom(t, want)
			}

			repo, err := RepoRows(reg, tc.part, compiler)
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Join(Listing(repo, tc.extras), "\n") + "\n"
			if got != want {
				t.Fatalf("listing differs for part %s:\ngot:\n%s\ngolden:\n%s", tc.part, got, want)
			}
		})
	}
}

// TestESDListingWithoutAURLMatchesItsGolden: a registry whose ESD row is
// missing, or has an empty URL, lists source-url empty where source:
// says ABSENT.
func TestESDListingWithoutAURLMatchesItsGolden(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "assets", "pins", "sources.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	const esd = "apple-installesd-10.9.5"
	for _, tc := range []struct {
		name   string
		golden string
		edit   func(f []string) string // the ESD row, rewritten; "" drops it
	}{
		{"no row", "listing-esd-no-row.txt", func([]string) string { return "" }},
		{"an empty URL", "listing-esd-empty-url.txt", func(f []string) string { return f[0] + "\t\t" + f[2] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var lines []string
			for _, line := range strings.Split(string(data), "\n") {
				if f := strings.Split(line, "\t"); f[0] == esd {
					if line = tc.edit(f); line == "" {
						continue
					}
				}
				lines = append(lines, line)
			}
			text := strings.Join(lines, "\n")
			want := golden(t, tc.golden)
			reg, err := pins.Parse(strings.NewReader(text))
			if err != nil {
				t.Fatal(err)
			}
			repo, err := RepoRows(reg, "esd", "")
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Join(Listing(repo, nil), "\n") + "\n"
			if got != want {
				t.Fatalf("got:\n%s\ngolden:\n%s", got, want)
			}
			if !strings.Contains(got, "source-url:"+esd+"\t\n") {
				t.Errorf("source-url is not empty:\n%s", got)
			}
		})
	}
}

// TestRepoRowsRefusesAnUnknownPart: a part repoParts does not have is
// an error naming it and the parts there are, never an empty listing
// that would hash like a part with no inputs.
func TestRepoRowsRefusesAnUnknownPart(t *testing.T) {
	reg, err := pins.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"install", "firmware", "nosuch", ""} {
		rows, err := RepoRows(reg, s, "")
		if err == nil || !strings.Contains(err.Error(), "no such part") ||
			!strings.Contains(err.Error(), strings.Join(repoParts, " ")) || rows != nil {
			t.Errorf("RepoRows(%q) = %v, %v", s, rows, err)
		}
	}
}

func TestUpdatesStamp(t *testing.T) {
	reg, err := pins.Embedded()
	if err != nil {
		t.Fatal(err)
	}

	rows, err := UpdatesStamp(reg, "none")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("none: got %v, want no rows", rows)
	}

	for _, selection := range []string{"security", "all"} {
		rows, err := UpdatesStamp(reg, selection)
		if err != nil {
			t.Fatal(err)
		}
		names, err := fetch.UpdateNames(selection)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1+len(names) {
			t.Fatalf("%s: got %d rows, want %d", selection, len(rows), 1+len(names))
		}
		if rows[0] != (Row{"updates", selection}) {
			t.Fatalf("%s: rows[0] = %v", selection, rows[0])
		}
		for i, n := range names {
			src, err := reg.Lookup(n)
			if err != nil {
				t.Fatal(err)
			}
			if want := (Row{"update:" + n, src.SHA256}); rows[i+1] != want {
				t.Fatalf("%s[%d]: got %v, want %v (install order)", selection, i, rows[i+1], want)
			}
		}
	}
}

// TestDigestIsSha256sumOfTheSortedText: Digest is
// `printf '%s\n' … | LC_ALL=C sort | sha256sum`.
func TestDigestIsSha256sumOfTheSortedText(t *testing.T) {
	requireTools(t, "bash", "sha256sum")

	extras := []Row{{"b", "2"}, {"a", "1"}, {"c", "3"}}
	rows := Listing(nil, extras)
	got := Digest(rows)

	var quoted []string
	for _, r := range rows {
		quoted = append(quoted, "'"+strings.ReplaceAll(r, "'", `'\''`)+"'")
	}
	script := "printf '%s\\n' " + strings.Join(quoted, " ") + " | LC_ALL=C sort | sha256sum"
	out, err := exec.Command("bash", "-c", script).Output()
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Fields(string(out))[0]
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}
