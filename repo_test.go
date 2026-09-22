package vmguest_test

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/Mavergreen/packer-plugin-mavericks/internal/pins"
)

// Two tests that encode a mistake the family has already made once. The pin lives in a
// FILE so Renovate can move it, and Renovate compares it with a
// versioning that keeps the -mavericks.N -- default versioning coerces N
// away, every repackage then compares equal, and the pin silently never
// moves again.

var opensshPinRe = regexp.MustCompile(`^[0-9]+\.[0-9]+p[0-9]+-mavericks\.[0-9]+$`)

func opensshPin(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("components/openssh/version")
	if err != nil {
		t.Fatal(err)
	}
	return pins.ComponentVersion(b)
}

// "the OpenSSH version is pinned in a file, not in a workflow": a release
// tag of Mavergreen/openssh, <upstream>-mavericks.N.
func TestTheOpenSSHVersionIsPinnedInAFile(t *testing.T) {
	if tag := opensshPin(t); !opensshPinRe.MatchString(tag) {
		t.Fatalf("components/openssh/version pins %q, not <upstream>-mavericks.N", tag)
	}
}

type renovateManager struct {
	ManagerFilePatterns []string `json:"managerFilePatterns"`
	MatchStrings        []string `json:"matchStrings"`
	DepNameTemplate     string   `json:"depNameTemplate"`
	DatasourceTemplate  string   `json:"datasourceTemplate"`
	VersioningTemplate  string   `json:"versioningTemplate"`
}

// "renovate tracks the OpenSSH pin with a versioning that keeps
// -mavericks.N": exactly one manager reads components/openssh/version,
// from Mavergreen/openssh's GitHub releases, with a regex: versioning
// that captures N. Both regexes are run against the pin itself: the manager must find it, and the versioning must parse it
// with the N in a group of its own.
func TestRenovateTracksTheOpenSSHPin(t *testing.T) {
	b, err := os.ReadFile(".github/renovate.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		CustomManagers []renovateManager `json:"customManagers"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf(".github/renovate.json: %v", err)
	}
	var mgrs []renovateManager
	for _, m := range cfg.CustomManagers {
		for _, p := range m.ManagerFilePatterns {
			if strings.Contains(p, "components/openssh") {
				mgrs = append(mgrs, m)
				break
			}
		}
	}
	if len(mgrs) != 1 {
		t.Fatalf("%d managers read components/openssh/version; want exactly 1", len(mgrs))
	}
	m := mgrs[0]
	if m.DatasourceTemplate != "github-releases" || m.DepNameTemplate != "Mavergreen/openssh" {
		t.Errorf("manager tracks %s %s; want github-releases Mavergreen/openssh", m.DatasourceTemplate, m.DepNameTemplate)
	}
	vt, ok := strings.CutPrefix(m.VersioningTemplate, "regex:")
	if !ok || !strings.Contains(vt, "mavericks") {
		t.Fatalf("versioningTemplate = %q; want a regex: versioning that names -mavericks.N", m.VersioningTemplate)
	}

	tag := opensshPin(t)
	found := false
	for _, s := range m.MatchStrings {
		re, err := regexp.Compile("(?m)" + s)
		if err != nil {
			t.Fatalf("matchString %q: %v", s, err)
		}
		if sm := re.FindStringSubmatch(tag + "\n"); sm != nil && sm[re.SubexpIndex("currentValue")] == tag {
			found = true
		}
	}
	if !found {
		t.Errorf("no matchString captures the pin %q as currentValue", tag)
	}
	vre, err := regexp.Compile(vt)
	if err != nil {
		t.Fatalf("versioningTemplate %q: %v", vt, err)
	}
	sm := vre.FindStringSubmatch(tag)
	if sm == nil {
		t.Fatalf("versioningTemplate %q does not parse the pin %q", vt, tag)
	}
	n := tag[strings.LastIndex(tag, ".")+1:]
	if !strings.Contains(vt, `-mavericks\.(?<`) || sm[len(sm)-1] != n {
		t.Errorf("versioningTemplate %q has no group of its own capturing the N of -mavericks.N (%q)", vt, n)
	}
}
