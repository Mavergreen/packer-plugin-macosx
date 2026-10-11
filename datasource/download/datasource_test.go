package download

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zclconf/go-cty/cty"
)

var body = []byte("dmg bytes")

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func serve(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	t.Cleanup(s.Close)
	return s
}

func configured(t *testing.T, url, pin, cache string) *Datasource {
	t.Helper()
	d := new(Datasource)
	if err := d.Configure(map[string]interface{}{"url": url, "sha256": pin, "cache_dir": cache}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	return d
}

func out(t *testing.T, v cty.Value) DatasourceOutput {
	t.Helper()
	m := v.AsValueMap()
	return DatasourceOutput{Path: m["path"].AsString(), SHA256: m["sha256"].AsString()}
}

func TestDownloadFetchesAndVerifies(t *testing.T) {
	s := serve(t)
	v, err := configured(t, s.URL+"/AquaTransport.dmg", sum(body), t.TempDir()).Execute()
	if err != nil {
		t.Fatal(err)
	}
	o := out(t, v)
	got, err := os.ReadFile(o.Path)
	if err != nil || string(got) != "dmg bytes" {
		t.Fatalf("file %s = %q, %v", o.Path, got, err)
	}
	if o.SHA256 != sum(body) {
		t.Fatalf("sha256 = %s, want %s", o.SHA256, sum(body))
	}
}

func TestDownloadRefusesAMovedFile(t *testing.T) {
	s := serve(t)
	cache := t.TempDir()
	pin := strings.Repeat("0", 64)
	_, err := configured(t, s.URL+"/x.dmg", pin, cache).Execute()
	if err == nil {
		t.Fatal("a wrong pin must be refused")
	}
	for _, want := range []string{
		"want " + pin,
		"got " + sum(body),
		"the file at this URL has changed since it was pinned; a newer release of packer-plugin-macosx may pin it",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	filepath.WalkDir(cache, func(p string, de os.DirEntry, _ error) error {
		if !de.IsDir() {
			t.Errorf("file left behind: %s", p)
		}
		return nil
	})
}

func TestDownloadRequiresURLAndSHA256(t *testing.T) {
	for field, cfg := range map[string]map[string]interface{}{
		"url":    {"sha256": sum(body)},
		"sha256": {"url": "https://h/x.dmg"},
	} {
		err := new(Datasource).Configure(cfg)
		if err == nil || !strings.Contains(err.Error(), field) {
			t.Errorf("missing %s: error = %v, want it named", field, err)
		}
	}
}

func TestDownloadReusesTheCache(t *testing.T) {
	s := serve(t)
	cache := t.TempDir()
	url := s.URL + "/x.dmg"
	if _, err := configured(t, url, sum(body), cache).Execute(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	v, err := configured(t, url, sum(body), cache).Execute()
	if err != nil {
		t.Fatalf("second Execute with the server stopped: %v", err)
	}
	if _, err := os.Stat(out(t, v).Path); err != nil {
		t.Fatal(err)
	}
}
