package media

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/Mavergreen/packer-plugin-macosx/internal/pins"
)

// recipeGoldens is where the goldens that pin the first-boot payload, the media's assembly and its disk layout live, relative to
// the repository root: the expected outputs of the Go that shapes what
// mavericks-media puts in its store entry.
var recipeGoldens = []string{"internal/payload/testdata/golden", "internal/media/testdata/golden", "internal/diskimg/testdata/golden"}

// goldensDigest is the first 16 hex digits of pins.Digest over one
// "path<TAB>sha256" row per file under dirs (recursively), sorted. A
// golden's README.md is left out: documentation changes nothing a build
// makes.
func goldensDigest(t *testing.T, dirs ...string) string {
	t.Helper()
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	root := filepath.Join(filepath.Dir(here), "..", "..")
	var rows []string
	for _, dir := range dirs {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || d.Name() == "README.md" {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(b)
			rows = append(rows, filepath.ToSlash(rel)+"\t"+hex.EncodeToString(sum[:]))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(rows) == 0 {
		t.Fatalf("no goldens under %v", dirs)
	}
	sort.Strings(rows)
	return pins.Digest(rows)[:16]
}

// TestRecipePinsTheGoldens: recipe is a row in every listing, so it is
// what makes a user's cached entry, built by an older plugin, be rebuilt
// by a newer one whose code shapes the output differently. The goldens
// are where such a change shows, so recipe carries their digest: a golden
// that changes without recipe changing too fails here.
func TestRecipePinsTheGoldens(t *testing.T) {
	got := goldensDigest(t, recipeGoldens...)
	_, pinned, ok := strings.Cut(recipe, " goldens:")
	if !ok {
		t.Fatalf("recipe %q is not \"<number> goldens:<digest>\"", recipe)
	}
	if pinned != got {
		t.Fatalf("the goldens under %v changed (digest %s), but recipe still says %q.\n"+
			"Bump recipe's number and set its goldens part to %s: a changed golden is\n"+
			"code that shapes mavericks-media's output differently, and without a new recipe\n"+
			"every user's cached entry from the old code would be reused as if nothing moved.",
			recipeGoldens, got, recipe, got)
	}
}
