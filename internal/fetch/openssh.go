package fetch

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	vmguest "github.com/Mavergreen/packer-plugin-mavericks"
	"github.com/Mavergreen/packer-plugin-mavericks/internal/pins"
)

const DefaultOpenSSHReleases = "https://github.com/Mavergreen/openssh/releases/download"

// maxSumsBytes caps how much of a SHA256SUMS response is read: a captive
// portal or another non-answer is commonly an HTML page, not a truncated
// SUMS file, and there is no reason to buffer an unbounded one.
const maxSumsBytes = 1 << 20 // 1 MiB

type OpenSSHPkgs struct{ Tag, Base, Replace string }

// OpenSSHAsset is one of a release's packages: its asset name and its
// sha256, as the release's SHA256SUMS gives them.
type OpenSSHAsset struct{ Name, SHA256 string }

// OpenSSHRelease is what a release's SHA256SUMS says it holds: the base
// package and the System-Replace package.
type OpenSSHRelease struct {
	Tag           string
	Base, Replace OpenSSHAsset
}

// OpenSSHTag is the release components/openssh/version pins (Renovate
// bumps it; the -mavericks.N suffix is the family's).
func OpenSSHTag() (string, error) {
	b, err := fs.ReadFile(vmguest.Files, "components/openssh/version")
	if err != nil {
		return "", err
	}
	tag := pins.ComponentVersion(b)
	if tag == "" {
		return "", fmt.Errorf("components/openssh/version names no release tag")
	}
	return tag, nil
}

// OpenSSH fetches the release's two packages, verified against the
// release's own SHA256SUMS: OpenSSHRelease, then OpenSSHPackages. A
// returned path is read-only (see Get); never open it for writing.
func (g *Getter) OpenSSH(ctx context.Context, releases, tag string) (OpenSSHPkgs, error) {
	rel, err := g.OpenSSHRelease(ctx, releases, tag)
	if err != nil {
		return OpenSSHPkgs{}, err
	}
	return g.OpenSSHPackages(ctx, releases, rel)
}

// OpenSSHRelease reads the release's SHA256SUMS -- from the cache, where
// it is kept per tag, or fetched once into it -- and returns the two
// packages it names, fetching neither. Nothing constructs an asset name
// from a prefix: the names are whatever SUMS says, so a renamed prefix
// cannot 404 across a pin bump.
func (g *Getter) OpenSSHRelease(ctx context.Context, releases, tag string) (OpenSSHRelease, error) {
	url := releases + "/" + tag
	sumsPath := g.Paths.OpenSSHSums(tag)
	sums, err := g.fetchOpenSSHSums(ctx, url, tag, sumsPath)
	if err != nil {
		return OpenSSHRelease{}, err
	}
	base, replace, want, err := parseOpenSSHSums(sums)
	if err != nil {
		return OpenSSHRelease{}, fmt.Errorf("%s: %w -- release %s looks wrong", sumsPath, err, tag)
	}
	return OpenSSHRelease{
		Tag:     tag,
		Base:    OpenSSHAsset{Name: base, SHA256: want[base]},
		Replace: OpenSSHAsset{Name: replace, SHA256: want[replace]},
	}, nil
}

// OpenSSHPackages fetches rel's two packages by exactly the names and
// sha256s rel gives, so a caller that listed rel gets the packages it
// listed. A returned path is read-only (see Get); never open it for
// writing.
func (g *Getter) OpenSSHPackages(ctx context.Context, releases string, rel OpenSSHRelease) (OpenSSHPkgs, error) {
	url := releases + "/" + rel.Tag
	out := OpenSSHPkgs{Tag: rel.Tag}
	for _, p := range []struct {
		asset OpenSSHAsset
		dst   *string
	}{{rel.Base, &out.Base}, {rel.Replace, &out.Replace}} {
		path, err := g.Get(ctx, Item{Name: p.asset.Name, URL: url + "/" + p.asset.Name, SHA256: p.asset.SHA256})
		if err != nil {
			return OpenSSHPkgs{}, err
		}
		ok, err := HasXarMagic(path)
		if err != nil {
			return OpenSSHPkgs{}, fmt.Errorf("%s: %w", path, err)
		}
		if !ok {
			return OpenSSHPkgs{}, fmt.Errorf("%s is not a flat package (no xar magic)", path)
		}
		*p.dst = path
	}
	g.logf("OpenSSH %s verified: %s, %s", rel.Tag, rel.Base.Name, rel.Replace.Name)
	return out, nil
}

// parseOpenSSHSums classifies SHA256SUMS' rows by name -- ".pkg" lines
// only -- into exactly one base package and one System-Replace package,
// validating each row's checksum column (validateSHA256, as for a pin:
// SUMS is not itself pinned, and a malformed column must not
// reach CacheFile).
func parseOpenSSHSums(b []byte) (base, replace string, want map[string]string, err error) {
	want = map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 || !strings.HasSuffix(f[1], ".pkg") {
			continue
		}
		name := f[1]
		if err := validateFilename(name); err != nil {
			return "", "", nil, fmt.Errorf("entry %q: %w", name, err)
		}
		if err := validateSHA256(f[0]); err != nil {
			return "", "", nil, fmt.Errorf("entry for %s: %w", name, err)
		}
		want[name] = f[0]
		if strings.Contains(name, "System-Replace") || strings.Contains(name, "system-replace") {
			if replace != "" {
				return "", "", nil, fmt.Errorf("two replacement packages in SHA256SUMS: %s and %s", replace, name)
			}
			replace = name
		} else {
			if base != "" {
				return "", "", nil, fmt.Errorf("two base packages in SHA256SUMS: %s and %s", base, name)
			}
			base = name
		}
	}
	if err := sc.Err(); err != nil {
		return "", "", nil, err
	}
	if base == "" {
		return "", "", nil, fmt.Errorf("no base .pkg named in SHA256SUMS")
	}
	if replace == "" {
		return "", "", nil, fmt.Errorf("no System-Replace .pkg named in SHA256SUMS")
	}
	return base, replace, want, nil
}

// fetchOpenSSHSums returns the release's own SHA256SUMS: reused from the
// cache when it already parses to exactly one base and one replacement
// package; otherwise fetched from the network with the same retry/backoff
// as a download. Content that does not parse is never persisted -- a cached
// SUMS that somehow fails to parse is an error naming its path, not a
// silent re-fetch, because a hand-edited or hand-restored cache file
// deserves attention, not to be quietly discarded.
//
// SHA256SUMS itself is not pinned: it is trusted as the release's
// statement of its own checksums (INHERITED), and
// it comes over TLS from GitHub.
func (g *Getter) fetchOpenSSHSums(ctx context.Context, url, tag, dest string) ([]byte, error) {
	if b, err := os.ReadFile(dest); err == nil {
		if len(b) > 0 {
			if _, _, _, perr := parseOpenSSHSums(b); perr != nil {
				return nil, fmt.Errorf("%s does not parse as SHA256SUMS: %w (remove it to fetch again)", dest, perr)
			}
			return b, nil
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("%s: %w", dest, err)
	}

	b, err := g.fetchSums(ctx, url+"/SHA256SUMS", tag)
	if err != nil {
		return nil, err
	}
	if _, _, _, perr := parseOpenSSHSums(b); perr != nil {
		return nil, fmt.Errorf("%s/SHA256SUMS: does not parse as SHA256SUMS: %w", url, perr)
	}
	if err := writeCacheFile(dest, b); err != nil {
		return nil, err
	}
	return b, nil
}

// fetchSums fetches url with the same retry/backoff policy as a download,
// and the same stall timeout: a captive portal is as happy to hold a SUMS
// request open as a 5 GB one.
func (g *Getter) fetchSums(ctx context.Context, url, tag string) ([]byte, error) {
	var b []byte
	err := g.retryPolicy().do(ctx, url, func() (bool, error) {
		var retry bool
		var err error
		b, retry, err = g.fetchSumsOnce(ctx, url, tag)
		return retry, err
	})
	return b, err
}

func (g *Getter) fetchSumsOnce(ctx context.Context, url, tag string) ([]byte, bool, error) {
	stall := g.stallDuration()
	attemptCtx, watch := newStallWatch(ctx, stall)
	defer watch.Stop()

	req, err := http.NewRequestWithContext(attemptCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, err
	}
	c := g.Client
	if c == nil {
		c = http.DefaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		retry, werr := classifyAttemptErr(ctx, watch, stall, url, err)
		return nil, retry, werr
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode >= 500, fmt.Errorf("cannot fetch %s (%s) -- is %s a real release?", url, resp.Status, tag)
	}
	var buf bytes.Buffer
	// Read one byte past the cap: reaching it means the real response is
	// too big (or unbounded) rather than silently truncating a response
	// that happens to be exactly the cap, or worse, one just over it.
	src := watch.Reader(resp.Body)
	n, err := io.Copy(&buf, io.LimitReader(src, maxSumsBytes+1))
	if err != nil {
		retry, werr := classifyAttemptErr(ctx, watch, stall, url, err)
		return nil, retry, werr
	}
	if n > maxSumsBytes {
		return nil, false, fmt.Errorf("%s: response is at least %d bytes -- refusing to buffer more of what should be a small text file", url, n)
	}
	return buf.Bytes(), false, nil
}

// writeCacheFile writes b to dest, a cache file this package owns (the
// release's SHA256SUMS), through a fresh temp directory of its own:
// synced, made 0644, then renamed into place, so a reader never sees a
// partial file and nothing is written through a name that already
// exists. (payload has its own writer for its outputs, writeOutput:
// neither package imports the other for it.)
func writeCacheFile(dest string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmpDir, err := os.MkdirTemp(filepath.Dir(dest), ".sums-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	tmp := filepath.Join(tmpDir, filepath.Base(dest))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}
