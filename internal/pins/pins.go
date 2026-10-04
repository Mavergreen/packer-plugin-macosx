// Package pins reads what this repository pins: the source registry
// (assets/pins/sources.tsv), whole-file component versions
// (components/*/version), and the ingredient digest over all of it: one
// sha256 that moves whenever any pin does. lib/vendor.sh, the
// repository's pin-maintenance tool, reads and writes the same registry.
package pins

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"

	"github.com/Mavergreen/packer-plugin-macosx"
)

type Source struct{ Name, URL, SHA256 string }

type Registry struct {
	rows []Source
	// fields counts each row's tab-separated fields: the ingredient digest
	// counts only rows with three or more, exactly as awk NF >= 3 does.
	fields []int
}

// Parse reads sources.tsv: name<TAB>url<TAB>sha256, lines starting with #
// are comments, blank lines are ignored.
func Parse(r io.Reader) (*Registry, error) {
	reg := &Registry{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		s := Source{Name: f[0]}
		if len(f) > 1 {
			s.URL = f[1]
		}
		if len(f) > 2 {
			s.SHA256 = f[2]
		}
		reg.rows = append(reg.rows, s)
		reg.fields = append(reg.fields, len(f))
	}
	return reg, sc.Err()
}

// Embedded is the registry this binary was built with.
func Embedded() (*Registry, error) {
	f, err := macosx.Files.Open("assets/pins/sources.tsv")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f)
}

// Lookup is the first row named exactly name. A source whose checksum is
// not yet known (TOFU, or no column at all) is refused: this binary
// verifies what it downloads, and pinning a new source is a developer's
// job (lib/vendor.sh's pin_checksum).
func (r *Registry) Lookup(name string) (Source, error) {
	if name == "" || strings.HasPrefix(name, "#") {
		return Source{}, fmt.Errorf("no source named %q", name)
	}
	for _, s := range r.rows {
		if s.Name != name {
			continue
		}
		if s.SHA256 == "" || s.SHA256 == "TOFU" {
			return Source{}, fmt.Errorf("source %s is not pinned (sha256 %q); pin it before fetching", name, s.SHA256)
		}
		return s, nil
	}
	return Source{}, fmt.Errorf("no source named %q in the registry", name)
}

func (r *Registry) Rows() []Source { return append([]Source(nil), r.rows...) }

// Complete reports whether Rows()[i] had three or more tab-separated
// fields in sources.tsv: the same test Ingredients uses (awk NF >= 3), so
// a caller that needs it (internal/inputs' boot-stack listing) need not parse
// sources.tsv a second time.
func (r *Registry) Complete(i int) bool { return i >= 0 && i < len(r.fields) && r.fields[i] >= 3 }

// ComponentVersion is a components/<name>/version file's pin: the first
// line that is not blank once # comments and all whitespace are removed.
func ComponentVersion(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		line = strings.Join(strings.Fields(line), "")
		if line != "" {
			return line
		}
	}
	return ""
}

// Ingredients is what the ingredient digest is over: every registry row
// with three or more fields and a name, every component version, and
// config.plist's checksum, as name<TAB>value, sorted bytewise.
func Ingredients() ([]string, error) {
	reg, err := Embedded()
	if err != nil {
		return nil, err
	}
	var rows []string
	for i, s := range reg.rows {
		if reg.fields[i] >= 3 && s.Name != "" {
			rows = append(rows, s.Name+"\t"+s.SHA256)
		}
	}
	versions, err := fs.Glob(macosx.Files, "components/*/version")
	if err != nil {
		return nil, err
	}
	for _, v := range versions {
		data, err := fs.ReadFile(macosx.Files, v)
		if err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(strings.TrimPrefix(v, "components/"), "/version")
		rows = append(rows, name+"\t"+ComponentVersion(data))
	}
	plist, err := fs.ReadFile(macosx.Files, "assets/firmware/config.plist")
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(plist)
	rows = append(rows, "config.plist\t"+hex.EncodeToString(sum[:]))
	sort.Strings(rows)
	return rows, nil
}

// Digest is sha256sum over the rows, one per line, each newline-terminated.
func Digest(rows []string) string {
	h := sha256.New()
	for _, r := range rows {
		io.WriteString(h, r+"\n")
	}
	return hex.EncodeToString(h.Sum(nil))
}
