package pins

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const tsv = "# comment\n" +
	"alpha\thttps://example.test/a.tar.gz\t1111\n" +
	"\n" +
	"alpha.beta\thttps://example.test/ab.zip\t2222\n" +
	"tofu\thttps://example.test/t.zip\tTOFU\n" +
	"nosha\thttps://example.test/n.zip\n" +
	"alpha\thttps://example.test/second.tar.gz\t3333\n"

func reg(t *testing.T) *Registry {
	r, err := Parse(strings.NewReader(tsv))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestLookupIsExactAndFirstMatchWins(t *testing.T) {
	s, err := reg(t).Lookup("alpha")
	if err != nil || s.URL != "https://example.test/a.tar.gz" || s.SHA256 != "1111" {
		t.Fatalf("%+v %v", s, err)
	}
	if _, err := reg(t).Lookup("alph"); err == nil {
		t.Fatal("a prefix must not match")
	}
	if _, err := reg(t).Lookup("alpha.beta"); err != nil {
		t.Fatal("a dotted name is a literal, not a pattern")
	}
	if _, err := reg(t).Lookup("alphaXbeta"); err == nil {
		t.Fatal("a dot must not match any character")
	}
}

func TestLookupRefusesAnUnpinnedSource(t *testing.T) {
	for _, n := range []string{"tofu", "nosha"} {
		_, err := reg(t).Lookup(n)
		if err == nil || !strings.Contains(err.Error(), n) || !strings.Contains(err.Error(), "pinned") {
			t.Errorf("%s: err = %v", n, err)
		}
	}
	if _, err := reg(t).Lookup("#"); err == nil {
		t.Fatal("a comment is not a source")
	}
}

func TestComponentVersion(t *testing.T) {
	if v := ComponentVersion([]byte("# pin\n\n  10.5p1-mavericks.2  # note\n")); v != "10.5p1-mavericks.2" {
		t.Fatalf("got %q", v)
	}
}

func TestEmbeddedRegistryHasEveryReleasePin(t *testing.T) {
	r, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"apple-installesd-10.9.5", "apple-secupd-2016-004", "opencorepkg-src"} {
		if _, err := r.Lookup(n); err != nil {
			t.Errorf("%s: %v", n, err)
		}
	}
}

// The ingredient digest stands for every pin at once, so the rows and the
// digest over them must not move by a byte unless a pin does:
// testdata/golden/ingredients.txt and ingredients-digest.txt hold them.
func TestIngredientsMatchTheirGolden(t *testing.T) {
	_, here, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(here), "testdata", "golden")
	list, err := os.ReadFile(filepath.Join(root, "ingredients.txt"))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := os.ReadFile(filepath.Join(root, "ingredients-digest.txt"))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := Ingredients()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rows, "\n") + "\n"; got != string(list) {
		t.Fatalf("rows differ from testdata/golden/ingredients.txt:\n%s\nvs\n%s", got, list)
	}
	if got := Digest(rows); got != strings.TrimSpace(string(digest)) {
		t.Fatalf("digest %s, the golden digest %s", got, digest)
	}
}

// TestTheIngredientDigestIsPinned pins the digest itself, in the source:
// it equals testdata/golden/ingredients-digest.txt. It moves only with a
// pin (a row of assets/pins/sources.tsv, a components/*/version) or a
// change to assets/firmware/config.plist, whose checksum is an
// ingredient; update both then, and say which moved in the commit.
func TestTheIngredientDigestIsPinned(t *testing.T) {
	rows, err := Ingredients()
	if err != nil {
		t.Fatal(err)
	}
	const want = "99a36c74743261666fec3b7fadcb678cf5f16a80cfaba6f770a6d7a3981755c8"
	if got := Digest(rows); got != want {
		t.Fatalf("digest %s, want %s", got, want)
	}
}
