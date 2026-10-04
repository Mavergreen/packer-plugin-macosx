package main

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/hashicorp/packer-plugin-sdk/plugin"
)

// TestDescribeListsAllThreeDatasources builds the plugin binary and runs
// its "describe" command, the same way `packer init`/`packer plugins`
// would. This is the plugin's outermost seam: it fails until main.go
// exists and registers all three stub data sources.
func TestDescribeListsEveryDatasource(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "packer-plugin-macosx")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	out, err := exec.Command(bin, "describe").Output()
	if err != nil {
		t.Fatalf("describe: %v", err)
	}

	var desc plugin.SetDescription
	if err := json.Unmarshal(out, &desc); err != nil {
		t.Fatalf("decoding describe output: %v\n%s", err, out)
	}

	got := append([]string(nil), desc.Datasources...)
	sort.Strings(got)
	want := []string{"mavericks-firmware", "mavericks-installesd", "mavericks-media", "snowleopard-firmware", "snowleopard-installer", "snowleopard-media"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("datasources = %v; want %v", got, want)
	}
	if desc.Version == "" {
		t.Error("version is empty")
	}
}
