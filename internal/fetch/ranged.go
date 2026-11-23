package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// pieceSize is PieceSize, defaulted: 0 or negative means 64 MiB.
func (g *Getter) pieceSize() int64 {
	if g.PieceSize <= 0 {
		return 64 << 20
	}
	return g.PieceSize
}

// parallel is Parallel, defaulted: 0 or negative means 8.
func (g *Getter) parallel() int {
	if g.Parallel <= 0 {
		return 8
	}
	return g.Parallel
}

// parseContentRange reads a "bytes start-end/total" Content-Range. A
// total of "*" (unknown) is refused: pieces are only worth asking for
// when their number is known.
func parseContentRange(h string) (start, end, total int64, err error) {
	var rest string
	if n, _ := fmt.Sscanf(h, "bytes %d-%d/%s", &start, &end, &rest); n != 3 {
		return 0, 0, 0, fmt.Errorf("unreadable Content-Range %q", h)
	}
	if _, err := fmt.Sscanf(rest, "%d", &total); err != nil || start < 0 || end < start || total <= end {
		return 0, 0, 0, fmt.Errorf("unreadable Content-Range %q", h)
	}
	return start, end, total, nil
}

// errUpstreamChanged is wrapped by a piece's error when its answer
// describes a different file than the first piece's did. Bytes from two
// versions of a file can never verify, so the pieces stop; but the
// source may well hold still for a fresh attempt, so attempt retries it.
var errUpstreamChanged = errors.New("the source changed mid-fetch")

// errRangeIgnored is wrapped by a later piece's error when it was
// answered with a 2xx other than 206: like errUpstreamChanged it stops
// the pieces, and attempt retries it from the first byte.
var errRangeIgnored = errors.New("a later piece's answer ignored its Range")

// firstPieceTotal is the whole file's size from the 206 answering
// attempt's request for its first piece. The answer must be exactly the
// bytes asked for (all of them, if the file is smaller than a piece): a
// shorter one streamed as if it were the whole file would surface as a
// checksum mismatch, blaming the URL for a server's partial answer. On
// an error attempt asks for the whole file instead.
func firstPieceTotal(url string, resp *http.Response, piece int64) (int64, error) {
	start, end, total, err := parseContentRange(resp.Header.Get("Content-Range"))
	if err != nil {
		return 0, fmt.Errorf("%s: %w", url, err)
	}
	if want := min(piece, total) - 1; start != 0 || end != want {
		return 0, fmt.Errorf("%s: asked for bytes 0-%d, got %d-%d/%d", url, want, start, end, total)
	}
	return total, nil
}

// rangedRest fills f, already holding nothing, with the total bytes at
// it.URL: piece 0 from first (the body of attempt's own ranged request),
// the rest by ranged requests of their own, Parallel pieces at a time,
// each written at its offset. Each piece is retried on its own, so one
// bad connection costs one piece, not the whole file; each answer must
// still describe the same file (its total, and its ETag when the first
// answer had one), or the fetch fails with errUpstreamChanged. A piece
// that fails for good stops the rest. classifyFirst is attempt's own
// classification of a failed read of first, which knows whether
// attempt's stall watch fired. rangedRest returns only once every
// request it made has ended, so nothing writes to f after it returns.
func (g *Getter) rangedRest(ctx context.Context, it Item, f *os.File, total int64, etag string, first io.ReadCloser, classifyFirst func(error) (bool, error)) error {
	if err := f.Truncate(total); err != nil {
		return err
	}
	piece := g.pieceSize()
	pieces := (total + piece - 1) / piece

	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// first's request was made under attempt's context, not pctx, so a
	// read of it stops only when its body is closed.
	stopFirst := context.AfterFunc(pctx, func() { first.Close() })
	defer stopFirst()

	var prog *pieceProgress
	if total > progressThreshold {
		prog = &pieceProgress{total: total, name: it.Name, log: g.logf, next: time.Now().Add(10 * time.Second)}
	}
	var (
		next     atomic.Int64
		failOnce sync.Once
		failErr  error
		wg       sync.WaitGroup
	)
	next.Store(1) // piece 0 is first's
	fail := func(err error) {
		failOnce.Do(func() {
			failErr = err
			cancel()
		})
	}
	get := func(i int64, r io.Reader) bool {
		start := i * piece
		end := min(start+piece, total) - 1
		if err := g.getPiece(pctx, it, f, start, end, total, etag, r, classifyFirst); err != nil {
			fail(err)
			return false
		}
		prog.add(end - start + 1)
		return true
	}
	worker := func(r io.Reader) {
		defer wg.Done()
		if r != nil && !get(0, r) {
			return
		}
		for pctx.Err() == nil {
			i := next.Add(1) - 1
			if i >= pieces || !get(i, nil) {
				return
			}
		}
	}
	workers := int64(g.parallel())
	if workers > pieces {
		workers = pieces
	}
	wg.Add(int(workers))
	go worker(first)
	for w := int64(1); w < workers; w++ {
		go worker(nil)
	}
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s: %w", it.URL, err)
	}
	return failErr
}

// getPiece puts bytes start-end into f at start, retrying under g's
// policy. Its first try reads first, when given, classifying a failure
// with classifyFirst; any retry asks the server again.
func (g *Getter) getPiece(ctx context.Context, it Item, f *os.File, start, end, total int64, etag string, first io.Reader, classifyFirst func(error) (bool, error)) error {
	what := fmt.Sprintf("%s: bytes %d-%d", it.Name, start, end)
	return g.retryPolicy().do(ctx, what, func() (bool, error) {
		if first != nil {
			r := first
			first = nil
			if err := copyPiece(f, start, end, r); err != nil {
				if ctx.Err() != nil {
					return false, err
				}
				return classifyFirst(err)
			}
			return false, nil
		}
		return g.fetchPiece(ctx, it, f, start, end, total, etag)
	})
}

// fetchPiece is one ranged request for bytes start-end, written into f
// at start. Like attempt it aborts (and retries) a piece that stalls,
// sends it.Header, and retries server errors but not client errors;
// unlike attempt it retries a 429 too, as only a later piece is asked
// for alongside others.
func (g *Getter) fetchPiece(ctx context.Context, it Item, f *os.File, start, end, total int64, etag string) (retry bool, err error) {
	stall := g.stallDuration()
	pieceCtx, watch := newStallWatch(ctx, stall)
	defer watch.Stop()

	req, err := http.NewRequestWithContext(pieceCtx, http.MethodGet, it.URL, nil)
	if err != nil {
		return false, err
	}
	addHeaders(req, it.Header)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	resp, err := g.client().Do(req)
	if err != nil {
		return classifyAttemptErr(ctx, watch, stall, it.URL, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusPartialContent:
	case resp.StatusCode/100 == 2:
		// Somewhere that ignores Range answered (a replica behind the
		// same name, say): these bytes have no place in f, but a fresh
		// attempt may reach a server that honours Range, or stream the
		// whole file from one that does not.
		return false, fmt.Errorf("%s: bytes %d-%d: HTTP %s: %w", it.URL, start, end, resp.Status, errRangeIgnored)
	default:
		// Parallel pieces are what draw a 429 mid-fetch, and it says to
		// come back later, so this piece alone is asked for again after
		// the policy's backoff, as a server error is.
		retry := resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests
		return retry, fmt.Errorf("%s: bytes %d-%d: HTTP %s", it.URL, start, end, resp.Status)
	}
	gs, ge, gt, err := parseContentRange(resp.Header.Get("Content-Range"))
	if err != nil {
		return false, fmt.Errorf("%s: bytes %d-%d: %w", it.URL, start, end, err)
	}
	if gt != total {
		return false, fmt.Errorf("%s: %w: its size was %d, now %d", it.URL, errUpstreamChanged, total, gt)
	}
	if got := resp.Header.Get("ETag"); etag != "" && got != etag {
		return false, fmt.Errorf("%s: %w: its ETag was %q, now %q", it.URL, errUpstreamChanged, etag, got)
	}
	if gs != start || ge != end {
		return false, fmt.Errorf("%s: asked for bytes %d-%d, got %d-%d", it.URL, start, end, gs, ge)
	}
	if err := copyPiece(f, start, end, watch.Reader(resp.Body)); err != nil {
		return classifyAttemptErr(ctx, watch, stall, it.URL, err)
	}
	return false, nil
}

// copyPiece writes exactly bytes start-end from r into f at start. A body
// that ends early is io.ErrUnexpectedEOF, as a short streamed body is.
func copyPiece(f *os.File, start, end int64, r io.Reader) error {
	_, err := io.CopyN(io.NewOffsetWriter(f, start), r, end-start+1)
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// pieceProgress logs a ranged download's progress, counting a piece once
// it is in: bytes of a piece that failed and was asked for again are not
// counted twice. A nil pieceProgress logs nothing.
type pieceProgress struct {
	mu          sync.Mutex
	done, total int64
	name        string
	log         func(string, ...any)
	next        time.Time
}

func (p *pieceProgress) add(n int64) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done += n
	if time.Now().After(p.next) {
		p.log("%s: %d of %d MiB", p.name, p.done>>20, p.total>>20)
		p.next = time.Now().Add(10 * time.Second)
	}
}
