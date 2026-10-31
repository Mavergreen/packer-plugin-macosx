package macosx_test

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// The internal names became release- and project-neutral on 2026-10-17
// (the guest-macosx stem: /usr/local/sbin/guest-macosx-shutdown,
// GUEST_MACOSX_FB_*, guest-macosx.firstboot). TestNoOldInternalNameSurvives
// keeps the old stem from coming back; oldnames_plant_test.go shows what
// it catches and what it leaves alone.

// oldStem is the stem the names had before, spelled so that no file here
// carries it.
var oldStem = "m" + "qg"

// oldNameRe is the old stem as a token: every old name was one, delimited
// by something not a letter or digit (the stem, then "-shutdown"; upper
// case, then "_FB_USER"; "com.", then the stem; a leading dot; a trailing
// "_" or ":"), or by a string escape (a Go string's "\n" just before an
// upper-case heredoc delimiter). The same three letters inside a word --
// base64, a PGP key's armour -- are not a name.
var oldNameRe = regexp.MustCompile(`(?i)(?:^|[^a-z0-9]|\\[nrt])` + oldStem + `(?:[^a-z0-9]|$)`)

// renameNote is the phrase the paragraph recording the rename carries, in
// each document that measured something under the old names.
const renameNote = "renamed on 2026-10-17"

// renameNotes is every file allowed such a paragraph, and how many it has:
// exactly that many paragraphs containing renameNote, which alone may name
// the old stem. No other file gets an exemption, whatever it says.
var renameNotes = map[string]int{
	"docs/configuration-register.md":                                             1,
	"docs/decisions/0019-every-shutdown-through-guest-macosx-shutdown.md":        1,
	"docs/decisions/0021-guest-macosx-shutdown-waits-for-jobs-still-starting.md": 1,
	"docs/decisions/0022-systemstats-set-aside-with-its-telemetry.md":            1,
	"release-notes/v0.20261031.1.md":                                             1,
}

var releaseNoteRe = regexp.MustCompile(`^release-notes/v0\.([0-9]+)\.([0-9]+)\.md$`)

// releasedBeforeTheRename is whether path is the release note of a release
// made before the rename: released notes stay exactly as written.
func releasedBeforeTheRename(path string) bool {
	m := releaseNoteRe.FindStringSubmatch(path)
	if m == nil {
		return false
	}
	base, n := m[1], m[2]
	if base != "20261031" {
		return base < "20261031"
	}
	return len(n) == 1 && n < "1"
}

// minRun is the shortest run of text oldNameFindings reads inside
// decompressed binary content. In random bytes a printable run that long
// is about one in ten million positions, so noise never reaches the
// matcher; a name embedded in a real file (a package's PackageInfo) is in
// a run far longer.
const minRun = 16

func printable(c byte) bool { return c >= 0x20 && c < 0x7f || c == '\t' || c == '\n' || c == '\r' }

// runFindings is every run of text at least minRun long in r that holds
// the old stem as a token, read a chunk at a time (some goldens are
// hundreds of megabytes uncompressed).
func runFindings(r io.Reader) []string {
	var out []string
	var run []byte
	check := func() {
		if len(run) >= minRun {
			if loc := oldNameRe.FindIndex(run); loc != nil {
				lo, hi := max(loc[0]-30, 0), min(loc[1]+30, len(run))
				out = append(out, fmt.Sprintf("%q", run[lo:hi]))
			}
		}
		run = run[:0]
	}
	buf := make([]byte, 1<<20)
	for {
		n, err := r.Read(buf)
		for _, c := range buf[:n] {
			if printable(c) {
				run = append(run, c)
				if len(run) >= 1<<20 { // a long text: check it, keep an overlap
					tail := append([]byte(nil), run[len(run)-64:]...)
					check()
					run = append(run, tail...)
				}
				continue
			}
			check()
		}
		if err != nil {
			break
		}
	}
	check()
	return out
}

// oldNameFindings is what is wrong with the file at path (relative to the
// repository root) holding b: its name, or its content, carrying the old
// stem as a token. A release note from before the rename is not read. A
// gzip'd file is read decompressed, as runs of text. Any other binary file
// (a NUL byte, or not UTF-8) is skipped. A text file is read by paragraph
// (lines up to a blank one), and renameNotes says which may be exempt.
func oldNameFindings(path string, b []byte) []string {
	var out []string
	if oldNameRe.MatchString(path) {
		out = append(out, path+": the file's name carries the old stem")
	}
	if releasedBeforeTheRename(path) {
		return out
	}
	if len(b) >= 2 && b[0] == 0x1f && b[1] == 0x8b {
		if zr, err := gzip.NewReader(bytes.NewReader(b)); err == nil {
			for _, f := range runFindings(zr) {
				out = append(out, path+" (decompressed): the old stem survives: "+f)
			}
		}
		return out
	}
	if bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b) {
		return out
	}
	want, listed := renameNotes[path]
	notes := 0
	for i, para := range strings.Split(string(b), "\n\n") {
		if listed && strings.Contains(para, renameNote) {
			notes++
			continue
		}
		for j, line := range strings.Split(para, "\n") {
			if oldNameRe.MatchString(line) {
				out = append(out, fmt.Sprintf("%s (paragraph %d, line %d of it): the old stem survives: %s", path, i+1, j+1, line))
			}
		}
	}
	if listed && notes != want {
		out = append(out, fmt.Sprintf("%s: %d paragraphs say %q, and renameNotes allows exactly %d", path, notes, renameNote, want))
	}
	return out
}

// projectFiles is what the project is: in a checkout whose top level is
// this directory, git's tracked and untracked-not-ignored files (so
// gitignored scratch is not), as bin/list-shell-files.sh lists them;
// anywhere else -- a release tarball, or a tree inside some other
// repository's work tree -- a walk pruning .git.
func projectFiles(t *testing.T) []string {
	t.Helper()
	here, err := filepath.EvalSymlinks(".")
	if err != nil {
		t.Fatal(err)
	}
	if here, err = filepath.Abs(here); err != nil {
		t.Fatal(err)
	}
	if top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output(); err == nil {
		if top, err := filepath.EvalSymlinks(strings.TrimSpace(string(top))); err == nil && top == here {
			out, err := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
			if err == nil {
				var files []string
				for _, f := range strings.Split(string(out), "\x00") {
					if f != "" {
						files = append(files, f)
					}
				}
				return files
			}
		}
	}
	var files []string
	err = filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return fs.SkipDir
		}
		if !d.IsDir() {
			files = append(files, filepath.ToSlash(path))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// Nothing may bring the old stem back, in any case, in any file's name or
// content -- inside gzip'd goldens too -- but in a release note written
// before then, or in the one paragraph each of renameNotes' documents has
// for recording that what it measured ran under the old names.
func TestNoOldInternalNameSurvives(t *testing.T) {
	files := projectFiles(t)
	if len(files) < 100 {
		t.Fatalf("found %d files; the listing is not finding the tree", len(files))
	}
	seen := map[string]bool{}
	// Read in parallel: decompressing the gzip'd disk-image goldens is
	// most of the time, about 1.4 GB of output.
	var mu sync.Mutex
	var findings []string
	paths := make(chan string)
	var wg sync.WaitGroup
	for range runtime.GOMAXPROCS(0) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range paths {
				b, err := os.ReadFile(path)
				if os.IsNotExist(err) {
					continue // tracked, deleted in the working tree
				}
				var f []string
				if err != nil {
					f = []string{path + ": " + err.Error()}
				} else {
					f = oldNameFindings(path, b)
				}
				mu.Lock()
				findings = append(findings, f...)
				mu.Unlock()
			}
		}()
	}
	for _, path := range files {
		seen[path] = true
		paths <- path
	}
	close(paths)
	wg.Wait()
	sort.Strings(findings)
	for _, f := range findings {
		t.Error(f)
	}
	for path := range renameNotes {
		if !seen[path] {
			t.Errorf("renameNotes lists %s, which is not in the tree", path)
		}
	}
}
