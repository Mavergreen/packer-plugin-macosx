package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Mavergreen/packer-plugin-mavericks/internal/pins"
)

// registry parses one row into a *pins.Registry, for Pinned's tests.
func registry(t *testing.T, name, url, sha string) *pins.Registry {
	t.Helper()
	reg, err := pins.Parse(strings.NewReader(name + "\t" + url + "\t" + sha + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestPinnedDownloadsVerifiesAndCaches(t *testing.T) {
	body := []byte("firmware bytes")
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write(body)
	}))
	defer srv.Close()
	g := getter(t)
	reg := registry(t, "x", srv.URL+"/dir/x.tar.gz", sum(body))

	p, err := g.Pinned(context.Background(), reg, "x")
	if err != nil {
		t.Fatal(err)
	}
	if want := g.Paths.CacheFile(sum(body), "x.tar.gz"); p != want {
		t.Fatalf("p = %q, want %q", p, want)
	}
	if b, err := os.ReadFile(p); err != nil || string(b) != string(body) {
		t.Fatalf("content = %q, %v", b, err)
	}
	if _, err := g.Pinned(context.Background(), reg, "x"); err != nil || hits != 1 {
		t.Fatalf("second Pinned must use the cache: hits=%d err=%v", hits, err)
	}
}

func TestPinnedRefusesAnUnpinnedSource(t *testing.T) {
	g := getter(t)
	reg := registry(t, "x", "https://example.test/dir/x.tar.gz", "TOFU")
	if _, err := g.Pinned(context.Background(), reg, "x"); err == nil ||
		!strings.Contains(err.Error(), "x") || !strings.Contains(err.Error(), "pinned") {
		t.Fatalf("err = %v", err)
	}
}

func TestPinnedRefusesAURLWithNoFilename(t *testing.T) {
	g := getter(t)
	for _, url := range []string{"https://example.test", "https://example.test/dir/"} {
		reg := registry(t, "x", url, sum([]byte("x")))
		if _, err := g.Pinned(context.Background(), reg, "x"); err == nil ||
			!strings.Contains(err.Error(), "cannot derive a filename") {
			t.Fatalf("url %s: err = %v", url, err)
		}
	}
}

// TestPinnedKeepsAQueryStringInTheFilename: the query string is part of
// the cached filename. validateFilename refuses a "/" but allows a "?".
func TestPinnedKeepsAQueryStringInTheFilename(t *testing.T) {
	body := []byte("versioned asset")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer srv.Close()
	g := getter(t)
	reg := registry(t, "x", srv.URL+"/f.zip?v=2", sum(body))
	p, err := g.Pinned(context.Background(), reg, "x")
	if err != nil {
		t.Fatal(err)
	}
	if want := g.Paths.CacheFile(sum(body), "f.zip?v=2"); p != want {
		t.Fatalf("p = %q, want %q", p, want)
	}
}
