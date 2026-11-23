package fetch

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mavergreen/packer-plugin-macosx/internal/config"
)

const testPiece = 1024

// rangeServer serves body, honouring a single "bytes=a-b" Range the way a
// CDN or archive.org does, and counts what it was asked for. hook, when
// set, sees every request first, with the start offset it asked for (-1
// for an unranged request), and answers it itself by returning true.
type rangeServer struct {
	*httptest.Server
	body     []byte
	hook     func(w http.ResponseWriter, r *http.Request, start int64) bool
	requests atomic.Int32
	ranged   atomic.Int32
	inflight atomic.Int32

	mu   sync.Mutex
	hits map[int64]int // by start offset
}

func newRangeServer(t *testing.T, body []byte, hook func(http.ResponseWriter, *http.Request, int64) bool) *rangeServer {
	s := &rangeServer{body: body, hook: hook, hits: map[int64]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *rangeServer) serve(w http.ResponseWriter, r *http.Request) {
	s.inflight.Add(1)
	defer s.inflight.Add(-1)
	s.requests.Add(1)
	start, end, ok := parseTestRange(r.Header.Get("Range"), int64(len(s.body)))
	if !ok {
		start = -1
	}
	s.mu.Lock()
	s.hits[start]++
	s.mu.Unlock()
	if s.hook != nil && s.hook(w, r, start) {
		return
	}
	w.Header().Set("ETag", `"v1"`)
	if start < 0 {
		w.Write(s.body)
		return
	}
	s.ranged.Add(1)
	writePiece(w, s.body, start, end, int64(len(s.body)))
}

func (s *rangeServer) hitsAt(start int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[start]
}

func writePiece(w http.ResponseWriter, body []byte, start, end, total int64) {
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
	w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
	w.WriteHeader(http.StatusPartialContent)
	w.Write(body[start : end+1])
}

func parseTestRange(h string, size int64) (start, end int64, ok bool) {
	if _, err := fmt.Sscanf(h, "bytes=%d-%d", &start, &end); err != nil {
		return 0, 0, false
	}
	if end >= size {
		end = size - 1
	}
	return start, end, true
}

func randomBody(t *testing.T, n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func rangedGetter(t *testing.T) *Getter {
	return &Getter{Paths: config.Paths{Home: t.TempDir()}, Backoff: time.Millisecond, Log: func(string, ...any) {}, PieceSize: testPiece, Parallel: 3}
}

// assertNothingLeft fails if dest exists or any temp is left beside it.
func assertNothingLeft(t *testing.T, dest string) {
	t.Helper()
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("%s exists after a failed Get (%v)", dest, err)
	}
	ents, err := os.ReadDir(filepath.Dir(dest))
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".part") {
			t.Fatalf("a failed Get left %s behind", e.Name())
		}
	}
}

func TestRangedGetAssemblesPiecesAndVerifies(t *testing.T) {
	body := randomBody(t, 5*testPiece)
	srv := newRangeServer(t, body, nil)
	g := rangedGetter(t)
	it := Item{Name: "x", URL: srv.URL + "/x.iso", SHA256: sum(body)}
	p, err := g.Get(context.Background(), it)
	if err != nil || p != g.Paths.CacheFile(it.SHA256, "x.iso") {
		t.Fatalf("%q %v", p, err)
	}
	b, err := os.ReadFile(p)
	if err != nil || sum(b) != it.SHA256 {
		t.Fatalf("the assembled file does not verify (%v)", err)
	}
	if n := srv.ranged.Load(); n < 2 {
		t.Fatalf("the server saw %d ranged requests, want at least 2", n)
	}
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("a ranged download's mode is %v (%v), want 0644", fi.Mode().Perm(), err)
	}
}

// TestRangedPiecesCarryTheItemsHeaders: Apple's CDN wants its AssetToken
// cookie on every request, so every piece carries Item.Header, not only
// the first.
func TestRangedPiecesCarryTheItemsHeaders(t *testing.T) {
	body := randomBody(t, 5*testPiece)
	var bare atomic.Int32
	srv := newRangeServer(t, body, func(w http.ResponseWriter, r *http.Request, start int64) bool {
		if r.Header.Get("Cookie") != "AssetToken=abc" {
			bare.Add(1)
			w.WriteHeader(403)
			return true
		}
		return false
	})
	g := rangedGetter(t)
	h := http.Header{}
	h.Set("Cookie", "AssetToken=abc")
	if _, err := g.Get(context.Background(), Item{Name: "x", URL: srv.URL + "/x.iso", SHA256: sum(body), Header: h}); err != nil {
		t.Fatal(err)
	}
	if bare.Load() != 0 || srv.ranged.Load() != 5 {
		t.Fatalf("%d request(s) lacked the item's headers; %d ranged", bare.Load(), srv.ranged.Load())
	}
}

func TestRangedGetRetriesOnlyTheFailedPiece(t *testing.T) {
	body := randomBody(t, 5*testPiece)
	var failed atomic.Bool
	srv := newRangeServer(t, body, func(w http.ResponseWriter, r *http.Request, start int64) bool {
		if start == 3*testPiece && failed.CompareAndSwap(false, true) {
			w.WriteHeader(500)
			return true
		}
		return false
	})
	g := rangedGetter(t)
	if _, err := g.Get(context.Background(), Item{Name: "x", URL: srv.URL + "/x.iso", SHA256: sum(body)}); err != nil {
		t.Fatal(err)
	}
	for i := int64(0); i < 5; i++ {
		want := 1
		if i == 3 {
			want = 2
		}
		if got := srv.hitsAt(i * testPiece); got != want {
			t.Errorf("piece %d was requested %d time(s), want %d", i, got, want)
		}
	}
}

func TestServerIgnoringRangeIsStreamedWhole(t *testing.T) {
	body := randomBody(t, 5*testPiece)
	srv := newRangeServer(t, body, func(w http.ResponseWriter, r *http.Request, start int64) bool {
		w.Write(body) // a plain 200, Range or no Range
		return true
	})
	g := rangedGetter(t)
	p, err := g.Get(context.Background(), Item{Name: "x", URL: srv.URL + "/x.iso", SHA256: sum(body)})
	if err != nil {
		t.Fatal(err)
	}
	if n := srv.requests.Load(); n != 1 {
		t.Fatalf("the server saw %d requests, want exactly 1", n)
	}
	if b, _ := os.ReadFile(p); sum(b) != sum(body) {
		t.Fatal("wrong bytes")
	}
}

// TestUpstreamChangingMidFetchFailsAndRenamesNothing: a piece whose
// answer describes a different file (another ETag, another total) stops
// the pieces, since bytes from two versions can never verify. A source
// that then holds still is fetched afresh from piece 0 and succeeds; one
// that keeps changing fails, leaving nothing behind.
func TestUpstreamChangingMidFetchFailsAndRenamesNothing(t *testing.T) {
	for name, change := range map[string]func(w http.ResponseWriter, body []byte, start, end int64){
		"etag": func(w http.ResponseWriter, body []byte, start, end int64) {
			w.Header().Set("ETag", `"v2"`)
			writePiece(w, body, start, end, int64(len(body)))
		},
		"total": func(w http.ResponseWriter, body []byte, start, end int64) {
			w.Header().Set("ETag", `"v1"`)
			writePiece(w, body, start, end, int64(len(body))+testPiece)
		},
	} {
		for _, always := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/always=%v", name, always), func(t *testing.T) {
				body := randomBody(t, 5*testPiece)
				var changed atomic.Bool
				srv := newRangeServer(t, body, func(w http.ResponseWriter, r *http.Request, start int64) bool {
					if start != 2*testPiece || (!always && !changed.CompareAndSwap(false, true)) {
						return false
					}
					change(w, body, start, start+testPiece-1)
					return true
				})
				g := rangedGetter(t)
				it := Item{Name: "x", URL: srv.URL + "/x.iso", SHA256: sum(body)}
				p, err := g.Get(context.Background(), it)
				if always {
					if !errors.Is(err, errUpstreamChanged) {
						t.Fatalf("err = %v, want errUpstreamChanged", err)
					}
					assertNothingLeft(t, g.Paths.CacheFile(it.SHA256, "x.iso"))
					return
				}
				if err != nil {
					t.Fatalf("a source that held still after one change must be fetched: %v", err)
				}
				if b, _ := os.ReadFile(p); sum(b) != it.SHA256 {
					t.Fatal("wrong bytes after the fresh attempt")
				}
				if n := srv.hitsAt(0); n != 2 {
					t.Fatalf("piece 0 was requested %d time(s), want 2 (a fresh attempt)", n)
				}
			})
		}
	}
}

// TestALaterPieceThrottledIsRetriedOnItsOwn: parallel pieces are what
// draw a 429 from a mirror mid-fetch, and it says to come back later,
// so that piece alone is asked for again rather than failing the Get.
func TestALaterPieceThrottledIsRetriedOnItsOwn(t *testing.T) {
	body := randomBody(t, 5*testPiece)
	var throttled atomic.Bool
	srv := newRangeServer(t, body, func(w http.ResponseWriter, r *http.Request, start int64) bool {
		if start == 3*testPiece && throttled.CompareAndSwap(false, true) {
			w.WriteHeader(http.StatusTooManyRequests)
			return true
		}
		return false
	})
	g := rangedGetter(t)
	if _, err := g.Get(context.Background(), Item{Name: "x", URL: srv.URL + "/x.iso", SHA256: sum(body)}); err != nil {
		t.Fatalf("a piece throttled once must be asked for again: %v", err)
	}
	if n0, n3 := srv.hitsAt(0), srv.hitsAt(3*testPiece); n0 != 1 || n3 != 2 {
		t.Fatalf("piece 0 was requested %d time(s), piece 3 %d; want 1 and 2", n0, n3)
	}
}

// TestALaterPieceAnsweredWholeIsAFreshAttempt: a later piece answered
// with a 2xx other than 206 came from somewhere that ignores Range (a
// replica behind the same name, say), so its bytes cannot be placed; a
// fresh attempt from the first byte may reach a server that honours
// Range, or stream the whole file from one that does not. One that
// keeps answering so fails, leaving nothing behind.
func TestALaterPieceAnsweredWholeIsAFreshAttempt(t *testing.T) {
	for _, always := range []bool{false, true} {
		t.Run(fmt.Sprintf("always=%v", always), func(t *testing.T) {
			body := randomBody(t, 5*testPiece)
			var answered atomic.Bool
			srv := newRangeServer(t, body, func(w http.ResponseWriter, r *http.Request, start int64) bool {
				if start != 2*testPiece || (!always && !answered.CompareAndSwap(false, true)) {
					return false
				}
				w.Write(body) // a plain 200, as if Range were not there
				return true
			})
			g := rangedGetter(t)
			it := Item{Name: "x", URL: srv.URL + "/x.iso", SHA256: sum(body)}
			p, err := g.Get(context.Background(), it)
			if always {
				if err == nil || !strings.Contains(err.Error(), "200") {
					t.Fatalf("err = %v, want one naming the 200", err)
				}
				if n := srv.hitsAt(0); n != 4 {
					t.Fatalf("piece 0 was requested %d time(s), want 4 (every attempt)", n)
				}
				assertNothingLeft(t, g.Paths.CacheFile(it.SHA256, "x.iso"))
				return
			}
			if err != nil {
				t.Fatalf("a piece answered whole once must cost a fresh attempt, not the Get: %v", err)
			}
			if b, _ := os.ReadFile(p); sum(b) != it.SHA256 {
				t.Fatal("wrong bytes after the fresh attempt")
			}
			if n := srv.hitsAt(0); n != 2 {
				t.Fatalf("piece 0 was requested %d time(s), want 2 (a fresh attempt)", n)
			}
		})
	}
}

func TestRangedMismatchNeverTakesItsFinalName(t *testing.T) {
	body := randomBody(t, 5*testPiece)
	srv := newRangeServer(t, body, nil)
	g := rangedGetter(t)
	want := sum([]byte("the bytes that were pinned"))
	_, err := g.Get(context.Background(), Item{Name: "x", URL: srv.URL + "/x.iso", SHA256: want})
	var mm *DownloadMismatchError
	if !errors.As(err, &mm) {
		t.Fatalf("err = %T %v, want a *DownloadMismatchError", err, err)
	}
	if srv.ranged.Load() < 2 {
		t.Fatal("the ranged path was not taken")
	}
	assertNothingLeft(t, g.Paths.CacheFile(want, "x.iso"))
	if _, err := os.Stat(g.Paths.Cache()); !os.IsNotExist(err) {
		t.Fatalf("a failed ranged Get left %s behind (%v)", g.Paths.Cache(), err)
	}
}

// TestRangedGetStopsOnCancel: a cancel mid-fetch stops every piece, not
// just the one that noticed: Get returns ctx's error only once every
// request it made has ended, so the server sees none still in flight.
func TestRangedGetStopsOnCancel(t *testing.T) {
	body := randomBody(t, 5*testPiece)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	var once sync.Once
	srv := newRangeServer(t, body, func(w http.ResponseWriter, r *http.Request, start int64) bool {
		if start <= 0 {
			return false
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+testPiece-1, len(body)))
		w.Header().Set("Content-Length", fmt.Sprint(testPiece))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(body[start : start+testPiece/2])
		w.(http.Flusher).Flush()
		once.Do(cancel)
		select {
		case <-r.Context().Done():
		case <-release:
		}
		return true
	})
	defer close(release)
	g := rangedGetter(t)
	_, err := g.Get(ctx, Item{Name: "x", URL: srv.URL + "/x.iso", SHA256: sum(body)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(g.Paths.Cache()); !os.IsNotExist(err) {
		t.Fatalf("a cancelled ranged Get left %s behind (%v)", g.Paths.Cache(), err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for srv.inflight.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d request(s) still in flight after Get returned", srv.inflight.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestAPieceFailingForGoodStopsTheOthers: piece 0 is read from the
// attempt's own response, made before the pieces' shared context
// existed, so a sibling's permanent failure must still stop that read
// rather than leave Get waiting out the stall timeout.
func TestAPieceFailingForGoodStopsTheOthers(t *testing.T) {
	body := randomBody(t, 5*testPiece)
	release := make(chan struct{})
	srv := newRangeServer(t, body, func(w http.ResponseWriter, r *http.Request, start int64) bool {
		switch start {
		case 0:
			w.Header().Set("ETag", `"v1"`)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", testPiece-1, len(body)))
			w.Header().Set("Content-Length", fmt.Sprint(testPiece))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(body[:testPiece/2])
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return true
		case 2 * testPiece:
			w.WriteHeader(404)
			return true
		}
		return false
	})
	defer close(release)
	g := rangedGetter(t)
	it := Item{Name: "x", URL: srv.URL + "/x.iso", SHA256: sum(body)}
	done := make(chan error, 1)
	go func() {
		_, err := g.Get(context.Background(), it)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "404") {
			t.Fatalf("err = %v, want the piece's 404", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Get kept reading piece 0 after another piece failed for good")
	}
	assertNothingLeft(t, g.Paths.CacheFile(it.SHA256, "x.iso"))
	deadline := time.Now().Add(10 * time.Second)
	for srv.inflight.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d request(s) still in flight after Get returned", srv.inflight.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestAStalledFirstPieceSaysSo: piece 0 is read from the attempt's own
// response, watched by the attempt's own stall watch; when that fires,
// the retry says the piece stalled, as every other stall does, rather
// than a bare "context canceled".
func TestAStalledFirstPieceSaysSo(t *testing.T) {
	body := randomBody(t, 5*testPiece)
	var stalled atomic.Bool
	srv := newRangeServer(t, body, func(w http.ResponseWriter, r *http.Request, start int64) bool {
		if start != 0 || !stalled.CompareAndSwap(false, true) {
			return false
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", testPiece-1, len(body)))
		w.Header().Set("Content-Length", fmt.Sprint(testPiece))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(body[:testPiece/2])
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		return true
	})
	var mu sync.Mutex
	var logs []string
	g := rangedGetter(t)
	g.StallTimeout = 200 * time.Millisecond
	g.Log = func(format string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, fmt.Sprintf(format, a...))
	}
	if _, err := g.Get(context.Background(), Item{Name: "x", URL: srv.URL + "/x.iso", SHA256: sum(body)}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, l := range logs {
		if strings.Contains(l, fmt.Sprintf("bytes 0-%d", testPiece-1)) && strings.Contains(l, "no data for") {
			return
		}
	}
	t.Fatalf("no retry of piece 0 said it stalled; logs: %q", logs)
}

// TestAnEmptyFileFromARangeHonouringServer: such a server (nginx and
// Apache among them, though not net/http's own ServeContent) has no byte
// 0 to give and answers 416 with "bytes */0". That is an empty file,
// verified as usual.
func TestAnEmptyFileFromARangeHonouringServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			w.Header().Set("Content-Range", "bytes */0")
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		}
	}))
	defer srv.Close()
	g := rangedGetter(t)
	it := Item{Name: "x", URL: srv.URL + "/empty", SHA256: sum(nil)}
	p, err := g.Get(context.Background(), it)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(p); err != nil || fi.Size() != 0 || fi.Mode().Perm() != 0o644 {
		t.Fatalf("the empty file is %v (%v), want 0 bytes, 0644", fi, err)
	}

	// Not empty after all: still a mismatch, never renamed.
	want := sum([]byte("not empty"))
	_, err = g.Get(context.Background(), Item{Name: "x", URL: srv.URL + "/empty", SHA256: want})
	var mm *DownloadMismatchError
	if !errors.As(err, &mm) {
		t.Fatalf("err = %T %v, want a *DownloadMismatchError", err, err)
	}
	assertNothingLeft(t, g.Paths.CacheFile(want, "empty"))
}

// TestAMalformedFirstContentRangeFallsBackToAWholeGet: a 206 that does
// not say plainly which bytes it holds is set aside for the whole file,
// asked for without Range and streamed as ever.
func TestAMalformedFirstContentRangeFallsBackToAWholeGet(t *testing.T) {
	body := randomBody(t, 5*testPiece)
	srv := newRangeServer(t, body, func(w http.ResponseWriter, r *http.Request, start int64) bool {
		if start < 0 {
			return false
		}
		w.Header().Set("Content-Range", "bytes garbage")
		w.WriteHeader(http.StatusPartialContent)
		w.Write(body[:testPiece])
		return true
	})
	g := rangedGetter(t)
	p, err := g.Get(context.Background(), Item{Name: "x", URL: srv.URL + "/x.iso", SHA256: sum(body)})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); sum(b) != sum(body) {
		t.Fatal("wrong bytes")
	}
	if n, whole := srv.requests.Load(), srv.hitsAt(-1); n != 2 || whole != 1 {
		t.Fatalf("%d request(s), %d unranged; want 2, 1", n, whole)
	}
}
