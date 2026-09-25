package fetch

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mavergreen/packer-plugin-mavericks/internal/pins"
)

// Known answer, computed 2026-09-25 with an openssl/xxd/od pipeline on
// these inputs -- not by this code.
func TestDeriveKeyKnownAnswer(t *testing.T) {
	got, err := deriveKey("0123456789ABCDEF", "001~0A1B2C3D4E5F60718293A4B5C6D7E8F9")
	if err != nil || got != "1DAC930DE453BFCF3A196011CEB952D85B53C72E6D8C8340E7D35D1F3CD5079F" {
		t.Fatalf("%s %v", got, err)
	}
	if _, err := deriveKey("0123456789ABCDEF", "no-tilde"); err == nil {
		t.Fatal("a server id without ~ must be refused")
	}
}

// fakeApple is osrecovery plus the CDN: it checks the handshake the way
// Apple's servers must be satisfied, then serves the asset only with the
// token.
func fakeApple(t *testing.T, asset []byte) (*httptest.Server, *pins.Registry) {
	const serverID = "001~0A1B2C3D4E5F60718293A4B5C6D7E8F9"
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: serverID})
		case r.Method == "POST" && r.URL.Path == "/InstallationPayload/OSInstaller":
			c, err := r.Cookie("session")
			body, _ := io.ReadAll(r.Body)
			want := "cid=0123456789ABCDEF\nsn=C0243070168G3M91F\nbid=Mac-3CBD00234E554E41\nk=1DAC930DE453BFCF3A196011CEB952D85B53C72E6D8C8340E7D35D1F3CD5079F"
			if err != nil || c.Value != serverID || r.Header.Get("Content-Type") != "text/plain" || string(body) != want {
				w.WriteHeader(403)
				return
			}
			fmt.Fprintf(w, "AP: x\nAU: %s/content/InstallESD.dmg\nAT: tok123\n", srv.URL)
		case r.URL.Path == "/content/InstallESD.dmg":
			if r.Header.Get("Cookie") != "AssetToken=tok123" {
				w.WriteHeader(403)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(asset)))
			if r.Method == "GET" {
				w.Write(asset)
			}
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	reg, _ := pins.Parse(strings.NewReader(fmt.Sprintf("%s\t%s/content/InstallESD.dmg\t%s\n", ESDSource, srv.URL, sum(asset))))
	return srv, reg
}

func fixedRand() io.Reader {
	return bytes.NewReader([]byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xAB, 0xCD, 0xEF})
}

func TestInstallESDHandshakesDownloadsAndVerifies(t *testing.T) {
	asset := []byte("not really Apple's installer")
	srv, reg := fakeApple(t, asset)
	g := getter(t)
	p, err := g.InstallESD(context.Background(), reg, Recovery{Base: srv.URL, Rand: fixedRand()})
	if err != nil || !strings.HasSuffix(p, "/InstallESD.dmg") {
		t.Fatalf("%q %v", p, err)
	}
}

func TestAnOfferOfADifferentOSIsRefused(t *testing.T) {
	asset := []byte("x")
	srv, _ := fakeApple(t, asset)
	reg, _ := pins.Parse(strings.NewReader(fmt.Sprintf("%s\thttp://oscdn.apple.com/other/InstallESD.dmg\t%s\n", ESDSource, sum(asset))))
	_, err := getter(t).InstallESD(context.Background(), reg, Recovery{Base: srv.URL, Rand: fixedRand()})
	if err == nil || !strings.Contains(err.Error(), "not the Mavericks") {
		t.Fatalf("err = %v", err)
	}
}

// TestACachedInstallerNeedsNoHandshake: a verified InstallESD.dmg
// already in the cache is returned without contacting Apple at all.
func TestACachedInstallerNeedsNoHandshake(t *testing.T) {
	asset := []byte("already here")
	_, reg := fakeApple(t, asset)
	g := getter(t)
	src, err := reg.Lookup(ESDSource)
	if err != nil {
		t.Fatal(err)
	}
	fn, err := Filename(src.URL)
	if err != nil {
		t.Fatal(err)
	}
	want := g.Paths.CacheFile(src.SHA256, fn)
	if err := writeCacheFile(want, asset); err != nil {
		t.Fatal(err)
	}
	// An unreachable recovery server proves no handshake happened.
	p, err := g.InstallESD(context.Background(), reg, Recovery{Base: "http://127.0.0.1:1"})
	if err != nil || p != want {
		t.Fatalf("p = %q, %v; want the cache path %q", p, err, want)
	}
	if b, err := os.ReadFile(p); err != nil || string(b) != string(asset) {
		t.Fatalf("cached content = %q, %v", b, err)
	}
}

// TestInstallESDWithARottenCacheNeverHandshakes: a rotten cached file
// must be an error before any network call to osrecovery, not a reason to
// re-handshake and re-download 5 GB.
func TestInstallESDWithARottenCacheNeverHandshakes(t *testing.T) {
	asset := []byte("good bytes")
	_, reg := fakeApple(t, asset)
	var contacted atomic.Bool
	recovery := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contacted.Store(true)
		w.WriteHeader(500)
	}))
	defer recovery.Close()

	g := getter(t)
	src, err := reg.Lookup(ESDSource)
	if err != nil {
		t.Fatal(err)
	}
	fn, err := Filename(src.URL)
	if err != nil {
		t.Fatal(err)
	}
	dest := g.Paths.CacheFile(src.SHA256, fn)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("rotted"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = g.InstallESD(context.Background(), reg, Recovery{Base: recovery.URL})
	if err == nil || !strings.Contains(err.Error(), dest) {
		t.Fatalf("err = %v, want it to name %s", err, dest)
	}
	if strings.Contains(err.Error(), "nothing was renamed into place") {
		t.Fatalf("err = %v: a rotten file IS at the final name -- this claim is false here", err)
	}
	if contacted.Load() {
		t.Fatal("a rotten cache entry must not trigger a handshake")
	}
}

func TestProbeReportsTheSizeAndDownloadsNothing(t *testing.T) {
	asset := []byte("0123456789")
	srv, reg := fakeApple(t, asset)
	url, size, err := Recovery{Base: srv.URL, Rand: fixedRand()}.Probe(context.Background(), reg)
	if err != nil || size != int64(len(asset)) || !strings.HasSuffix(url, "/InstallESD.dmg") {
		t.Fatalf("%q %d %v", url, size, err)
	}
}

// flakyFront stands in front of fakeApple's server: fail says, for the
// n-th (1-based) request of a method and path, whether to answer 503
// ("503"), hang past any sane timeout ("hang"), refuse it ("403"), or
// pass it through (""). It counts every request it sees.
func flakyFront(t *testing.T, backend *httptest.Server, fail func(method, path string, n int) string) (*httptest.Server, func(method, path string) int) {
	t.Helper()
	target, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	var mu sync.Mutex
	counts := map[string]int{}
	stop := make(chan struct{})
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		counts[r.Method+" "+r.URL.Path]++
		n := counts[r.Method+" "+r.URL.Path]
		mu.Unlock()
		switch fail(r.Method, r.URL.Path, n) {
		case "503":
			w.WriteHeader(503)
		case "403":
			w.WriteHeader(403)
		case "hang":
			// Drain the body first: only then does the server watch
			// the connection, and notice the client giving up.
			io.Copy(io.Discard, r.Body)
			select {
			case <-r.Context().Done():
			case <-stop:
			}
		default:
			proxy.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(front.Close)
	t.Cleanup(func() { close(stop) }) // runs first: no handler outlives the test
	return front, func(method, path string) int {
		mu.Lock()
		defer mu.Unlock()
		return counts[method+" "+path]
	}
}

// TestTheHandshakeRetriesTransientFailures: a 503 and a request that hangs (cut off by the
// per-request timeout) are both worth another try.
func TestTheHandshakeRetriesTransientFailures(t *testing.T) {
	backend, _ := fakeApple(t, []byte("x"))
	front, count := flakyFront(t, backend, func(method, path string, n int) string {
		switch {
		case method == "GET" && path == "/" && n == 1:
			return "503"
		case method == "POST" && n == 1:
			return "hang"
		}
		return ""
	})
	rc := Recovery{Base: front.URL, Rand: fixedRand(), Backoff: time.Millisecond, Timeout: 200 * time.Millisecond}
	au, tok, err := rc.Handshake(context.Background())
	if err != nil || tok != "tok123" || !strings.HasSuffix(au, "/content/InstallESD.dmg") {
		t.Fatalf("%q %q %v", au, tok, err)
	}
	if got := count("POST", "/InstallationPayload/OSInstaller"); got != 2 {
		t.Fatalf("%d POSTs, want 2 (one hung, one answered)", got)
	}
}

// TestTheHandshakeDoesNotRetryARefusal: a 4xx is an answer, not a
// hiccup; asking again gets the same one.
func TestTheHandshakeDoesNotRetryARefusal(t *testing.T) {
	backend, _ := fakeApple(t, []byte("x"))
	front, count := flakyFront(t, backend, func(method, path string, n int) string {
		if method == "POST" {
			return "403"
		}
		return ""
	})
	rc := Recovery{Base: front.URL, Rand: fixedRand(), Backoff: time.Millisecond}
	if _, _, err := rc.Handshake(context.Background()); err == nil {
		t.Fatal("a refused handshake must be an error")
	}
	if got := count("POST", "/InstallationPayload/OSInstaller"); got != 1 {
		t.Fatalf("%d POSTs, want 1", got)
	}
}

// TestProbeRetriesATransientHEAD: the HEAD Probe sends after the
// handshake gets the same retries.
func TestProbeRetriesATransientHEAD(t *testing.T) {
	asset := []byte("0123456789")
	var heads atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "001~0A1B2C3D4E5F60718293A4B5C6D7E8F9"})
		case r.Method == "POST":
			fmt.Fprintf(w, "AU: %s/content/InstallESD.dmg\nAT: tok123\n", srv.URL)
		case r.Method == "HEAD" && heads.Add(1) == 1:
			w.WriteHeader(503)
		case r.Method == "HEAD":
			w.Header().Set("Content-Length", fmt.Sprint(len(asset)))
		}
	}))
	defer srv.Close()
	reg, err := pins.Parse(strings.NewReader(fmt.Sprintf("%s\t%s/content/InstallESD.dmg\t%s\n", ESDSource, srv.URL, sum(asset))))
	if err != nil {
		t.Fatal(err)
	}
	rc := Recovery{Base: srv.URL, Rand: fixedRand(), Backoff: time.Millisecond}
	_, size, err := rc.Probe(context.Background(), reg)
	if err != nil || size != int64(len(asset)) {
		t.Fatalf("size=%d err=%v", size, err)
	}
	if heads.Load() != 2 {
		t.Fatalf("%d HEADs, want 2", heads.Load())
	}
}
