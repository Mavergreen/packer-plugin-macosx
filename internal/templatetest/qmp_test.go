package templatetest

import (
	"strings"
	"testing"
)

const qmpGood = `
data "macosx-qmp-dir" "qmp" {
  dir = "output-x-qmp"
}
locals {
  output_directory = "output-x"
}
source "qemu" "x" {
  output_directory = local.output_directory
  qemuargs = [
    ["-qmp", "unix:${data.macosx-qmp-dir.qmp.socket},server,nowait"],
  ]
}
`

func TestQMPSocketProblems(t *testing.T) {
	if p := QMPSocketProblems([]byte(qmpGood), "x.pkr.hcl"); len(p) != 0 {
		t.Fatalf("a right template has problems: %v", p)
	}
	cases := []struct{ name, src, want string }{
		{"no qemuargs", strings.Replace(qmpGood, `  qemuargs = [
    ["-qmp", "unix:${data.macosx-qmp-dir.qmp.socket},server,nowait"],
  ]
`, "", 1), "no qemuargs"},
		{"qemuargs not a list", strings.Replace(qmpGood, `[
    ["-qmp", "unix:${data.macosx-qmp-dir.qmp.socket},server,nowait"],
  ]`, `local.x`, 1), "not a list"},
		{"no data source", strings.Replace(qmpGood, `data "macosx-qmp-dir" "qmp" {
  dir = "output-x-qmp"
}`, "", 1), "0 macosx-qmp-dir data sources"},
		{"dir not beside the output directory", strings.Replace(qmpGood, `"output-x-qmp"`, `"elsewhere"`, 1), `want "output-x-qmp"`},
		{"socket beside the output directory, outside the private one", strings.Replace(qmpGood, `${data.macosx-qmp-dir.qmp.socket}`, `output-x-qmp.sock`, 1), "the qmp-dir data source's socket"},
		{"two rows", strings.Replace(qmpGood, `  ]`, `    ["-qmp", "unix:${data.macosx-qmp-dir.qmp.socket},server,nowait"],
  ]`, 1), "-qmp 2 times"},
		{"the builder's own socket", strings.Replace(qmpGood, "output_directory = local.output_directory", "output_directory = local.output_directory\n  qmp_enable = true", 1), "qmp_enable"},
		{"a path too long", strings.Replace(strings.Replace(qmpGood, `"output-x-qmp"`, `"`+strings.Repeat("x", 100)+`"`, 1), `output_directory = "output-x"`, `output_directory = "`+strings.Repeat("x", 100)+`"`, 1), "over the 103"},
	}
	for _, c := range cases {
		p := QMPSocketProblems([]byte(c.src), "x.pkr.hcl")
		if len(p) == 0 {
			t.Errorf("%s: no problem found", c.name)
			continue
		}
		if !strings.Contains(strings.Join(p, "\n"), c.want) {
			t.Errorf("%s: problems %v do not mention %q", c.name, p, c.want)
		}
	}
}
