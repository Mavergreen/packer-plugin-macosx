package installesd

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zclconf/go-cty/cty"

	"github.com/Mavergreen/packer-plugin-macosx/internal/config"
	"github.com/Mavergreen/packer-plugin-macosx/internal/fetch"
	"github.com/Mavergreen/packer-plugin-macosx/internal/pins"
)

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func decodeOutput(t *testing.T, v cty.Value) DatasourceOutput {
	t.Helper()
	m := v.AsValueMap()
	path, ok1 := m["path"]
	sha, ok2 := m["sha256"]
	if !ok1 || !ok2 {
		t.Fatalf("output %#v is missing path or sha256", m)
	}
	return DatasourceOutput{Path: path.AsString(), SHA256: sha.AsString()}
}

func TestConfigureDefaultsCacheDirAndRejectsUnknownKeys(t *testing.T) {
	var empty Datasource
	if err := empty.Configure(map[string]interface{}{}); err != nil {
		t.Fatalf("Configure(no cache_dir): %v", err)
	}
	if empty.config.CacheDir != "" {
		t.Fatalf("cache_dir = %q, want empty (Execute defaults it to packer.CachePath)", empty.config.CacheDir)
	}

	var set Datasource
	if err := set.Configure(map[string]interface{}{"cache_dir": "/tmp/somewhere"}); err != nil {
		t.Fatalf("Configure(cache_dir set): %v", err)
	}
	if set.config.CacheDir != "/tmp/somewhere" {
		t.Fatalf("cache_dir = %q, want /tmp/somewhere", set.config.CacheDir)
	}

	var unknown Datasource
	if err := unknown.Configure(map[string]interface{}{"bogus": "y"}); err == nil {
		t.Fatal("an unknown configuration key must be refused")
	}
}

// fakeOSRecoveryAndCDN stands in for osrecovery.apple.com and the CDN it
// hands out a token for: any handshake is accepted (the handshake's own
// protocol is internal/fetch's to test), and the asset is served only
// with the token the handshake returned.
func fakeOSRecoveryAndCDN(t *testing.T, asset []byte) (srv *httptest.Server, posts *int32) {
	t.Helper()
	var n int32
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "001~0A1B2C3D4E5F60718293A4B5C6D7E8F9"})
		case r.Method == http.MethodPost && r.URL.Path == "/InstallationPayload/OSInstaller":
			atomic.AddInt32(&n, 1)
			io.Copy(io.Discard, r.Body)
			fmt.Fprintf(w, "AU: %s/content/InstallESD.dmg\nAT: tok123\n", srv.URL)
		case r.URL.Path == "/content/InstallESD.dmg":
			if r.Header.Get("Cookie") != "AssetToken=tok123" {
				w.WriteHeader(403)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(asset)))
			if r.Method == http.MethodGet {
				w.Write(asset)
			}
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

// TestExecuteFetchesVerifiesAndReusesTheStore: a first Execute talks to
// the fake osrecovery/CDN and returns the pin's path and sha256; a second
// Execute, with the same registry and store, reuses store's built
// directory and makes no HTTP request at all.
func TestExecuteFetchesVerifiesAndReusesTheStore(t *testing.T) {
	asset := []byte("not really Apple's installer, but pinned all the same")
	assetSHA := sum(asset)
	srv, posts := fakeOSRecoveryAndCDN(t, asset)

	reg, err := pins.Parse(strings.NewReader(fmt.Sprintf(
		"%s\t%s/content/InstallESD.dmg\t%s\n", fetch.ESDSource, srv.URL, assetSHA)))
	if err != nil {
		t.Fatal(err)
	}

	savedLoad, savedBase := loadRegistry, recoveryBase
	loadRegistry = func() (*pins.Registry, error) { return reg, nil }
	recoveryBase = srv.URL
	t.Cleanup(func() { loadRegistry, recoveryBase = savedLoad, savedBase })

	var d Datasource
	if err := d.Configure(map[string]interface{}{"cache_dir": t.TempDir()}); err != nil {
		t.Fatal(err)
	}

	v, err := d.Execute()
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	out := decodeOutput(t, v)
	if out.SHA256 != assetSHA {
		t.Fatalf("sha256 = %q, want %q", out.SHA256, assetSHA)
	}
	if !strings.HasSuffix(out.Path, "InstallESD.dmg") {
		t.Fatalf("path = %q, want it to end in InstallESD.dmg", out.Path)
	}
	got, err := os.ReadFile(out.Path)
	if err != nil {
		t.Fatalf("reading %s: %v", out.Path, err)
	}
	if string(got) != string(asset) {
		t.Fatalf("content at %s does not match the fetched asset", out.Path)
	}
	if atomic.LoadInt32(posts) != 1 {
		t.Fatalf("%d handshake POSTs after the first Execute, want 1", atomic.LoadInt32(posts))
	}

	v2, err := d.Execute()
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	out2 := decodeOutput(t, v2)
	if out2 != out {
		t.Fatalf("second Execute = %+v, want the same as the first %+v", out2, out)
	}
	if atomic.LoadInt32(posts) != 1 {
		t.Fatalf("%d handshake POSTs after a second Execute, want still 1 (the store must be reused, no new HTTP request)", atomic.LoadInt32(posts))
	}

	// The entry's listing carries recipe: the row a change to the
	// entry's shape bumps (installesd has no goldens to pin it to).
	b, err := os.ReadFile(filepath.Join(filepath.Dir(out.Path), "inputs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "recipe\t"+recipe+"\n") {
		t.Fatalf("the listing does not carry recipe %q:\n%s", recipe, b)
	}
}

// TestAnOfferOfADifferentOSIsRefused: a registry override whose URL
// doesn't match what the fake osrecovery hands back must fail Execute,
// not silently accept a different installer -- exactly as
// internal/fetch's own TestAnOfferOfADifferentOSIsRefused checks at the
// fetch.Recovery level, but reached here through the data source's own
// Execute.
func TestAnOfferOfADifferentOSIsRefused(t *testing.T) {
	asset := []byte("x")
	srv, _ := fakeOSRecoveryAndCDN(t, asset)

	reg, err := pins.Parse(strings.NewReader(fmt.Sprintf(
		"%s\thttp://oscdn.apple.com/other/InstallESD.dmg\t%s\n", fetch.ESDSource, sum(asset))))
	if err != nil {
		t.Fatal(err)
	}

	savedLoad, savedBase := loadRegistry, recoveryBase
	loadRegistry = func() (*pins.Registry, error) { return reg, nil }
	recoveryBase = srv.URL
	t.Cleanup(func() { loadRegistry, recoveryBase = savedLoad, savedBase })

	var d Datasource
	if err := d.Configure(map[string]interface{}{"cache_dir": t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Execute(); err == nil || !strings.Contains(err.Error(), "not the Mavericks") {
		t.Fatalf("err = %v, want a complaint about the offered OS", err)
	}
}

// TestASharedCacheServesEveryListing: two listings that pin the same
// sha256 and the same filename but different URLs -- as a URL rotation
// would -- sharing the same cache_dir, share one download. fetch's cache
// key is sha256+filename, not the store's own per-listing digest, so the
// second listing's own, separate store entry is built without ever
// dialing its (deliberately unreachable) pinned host: fetch checks its
// shared cache, already holding this sha256+filename from the first
// listing, before it ever handshakes.
func TestASharedCacheServesEveryListing(t *testing.T) {
	asset := []byte("shared across listings, same bytes, different pinned URL")
	assetSHA := sum(asset)
	srv, posts := fakeOSRecoveryAndCDN(t, asset)
	cacheDir := t.TempDir()

	savedLoad, savedBase := loadRegistry, recoveryBase
	t.Cleanup(func() { loadRegistry, recoveryBase = savedLoad, savedBase })

	reg1, err := pins.Parse(strings.NewReader(fmt.Sprintf(
		"%s\t%s/content/InstallESD.dmg\t%s\n", fetch.ESDSource, srv.URL, assetSHA)))
	if err != nil {
		t.Fatal(err)
	}
	loadRegistry = func() (*pins.Registry, error) { return reg1, nil }
	recoveryBase = srv.URL

	var d1 Datasource
	if err := d1.Configure(map[string]interface{}{"cache_dir": cacheDir}); err != nil {
		t.Fatal(err)
	}
	v1, err := d1.Execute()
	if err != nil {
		t.Fatalf("first listing's Execute: %v", err)
	}
	out1 := decodeOutput(t, v1)
	if out1.SHA256 != assetSHA {
		t.Fatalf("sha256 = %q, want %q", out1.SHA256, assetSHA)
	}
	if atomic.LoadInt32(posts) != 1 {
		t.Fatalf("%d handshake POSTs after the first listing, want 1", atomic.LoadInt32(posts))
	}

	// Same sha256, same filename (InstallESD.dmg), a different and
	// deliberately unreachable host: if Execute dialed it at all --
	// handshake or download -- this would fail to connect.
	reg2, err := pins.Parse(strings.NewReader(fmt.Sprintf(
		"%s\thttp://127.0.0.1:1/elsewhere/InstallESD.dmg\t%s\n", fetch.ESDSource, assetSHA)))
	if err != nil {
		t.Fatal(err)
	}
	loadRegistry = func() (*pins.Registry, error) { return reg2, nil }
	recoveryBase = "http://127.0.0.1:1"

	var d2 Datasource
	if err := d2.Configure(map[string]interface{}{"cache_dir": cacheDir}); err != nil {
		t.Fatal(err)
	}
	v2, err := d2.Execute()
	if err != nil {
		t.Fatalf("second, differently-pinned listing's Execute: %v", err)
	}
	out2 := decodeOutput(t, v2)
	if out2.SHA256 != assetSHA {
		t.Fatalf("sha256 = %q, want %q", out2.SHA256, assetSHA)
	}
	if out2.Path == out1.Path {
		t.Fatalf("two listings with different pins must be two different store entries, both landed at %q", out1.Path)
	}
	got, err := os.ReadFile(out2.Path)
	if err != nil || string(got) != string(asset) {
		t.Fatalf("content at %s: %q, %v", out2.Path, got, err)
	}
	if atomic.LoadInt32(posts) != 1 {
		t.Fatalf("%d handshake POSTs after a second, differently-pinned listing sharing the same sha256, want still 1 (no HTTP request at all -- fetch checks its shared cache before ever handshaking again)", atomic.LoadInt32(posts))
	}
}

// TestAPreSeededCacheFileIsUsedWithoutAnyHandshake: a file already present
// at cache_dir/cache/<sha256>/<filename> -- as a release's pre-seeded
// cache might be -- is used as-is. fetch.Getter.InstallESD checks its
// cache before ever handshaking (esd.go: "Cache first: no handshake for a
// file already here"), so Execute must succeed even with an unreachable
// recovery server.
func TestAPreSeededCacheFileIsUsedWithoutAnyHandshake(t *testing.T) {
	asset := []byte("already fetched by someone else, pre-seeded into the shared cache")
	assetSHA := sum(asset)
	cacheDir := t.TempDir()

	const assetURL = "http://example.invalid/content/InstallESD.dmg"
	reg, err := pins.Parse(strings.NewReader(fmt.Sprintf("%s\t%s\t%s\n", fetch.ESDSource, assetURL, assetSHA)))
	if err != nil {
		t.Fatal(err)
	}
	fn, err := fetch.Filename(assetURL)
	if err != nil {
		t.Fatal(err)
	}
	seeded := config.Paths{Home: cacheDir}.CacheFile(assetSHA, fn)
	if err := os.MkdirAll(filepath.Dir(seeded), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(seeded, asset, 0o644); err != nil {
		t.Fatal(err)
	}

	savedLoad, savedBase := loadRegistry, recoveryBase
	loadRegistry = func() (*pins.Registry, error) { return reg, nil }
	recoveryBase = "http://127.0.0.1:1" // unreachable: a handshake here would fail loudly, not hang
	t.Cleanup(func() { loadRegistry, recoveryBase = savedLoad, savedBase })

	var d Datasource
	if err := d.Configure(map[string]interface{}{"cache_dir": cacheDir}); err != nil {
		t.Fatal(err)
	}
	v, err := d.Execute()
	if err != nil {
		t.Fatalf("Execute with a pre-seeded shared cache file: %v", err)
	}
	out := decodeOutput(t, v)
	if out.SHA256 != assetSHA {
		t.Fatalf("sha256 = %q, want %q", out.SHA256, assetSHA)
	}
	got, err := os.ReadFile(out.Path)
	if err != nil || string(got) != string(asset) {
		t.Fatalf("content at %s: %q, %v", out.Path, got, err)
	}
}
