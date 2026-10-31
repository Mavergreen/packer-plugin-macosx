package macosx_test

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"math/rand"
	"strings"
	"testing"
)

// Planted cases for TestNoOldInternalNameSurvives's matcher, both ways: what
// it must catch, and what it must leave alone. None of them touches the tree.

var (
	old      = oldStem
	oldUpper = strings.ToUpper(oldStem)
)

func gz(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// randomBlob is a deterministic 1.4 MB of random bytes that holds the old
// stem as a substring somewhere (about half of all such blobs do), so a
// substring match would fail on it and a token match must not.
func randomBlob(t *testing.T, accept func([]byte) bool) []byte {
	t.Helper()
	for seed := int64(1); seed < 1000; seed++ {
		b := make([]byte, 1400*1024)
		rand.New(rand.NewSource(seed)).Read(b)
		if accept(b) {
			return b
		}
	}
	t.Fatal("no seed under 1000 gave a blob holding the stem")
	return nil
}

func holdsStem(b []byte) bool { return bytes.Contains(bytes.ToLower(b), []byte(oldStem)) }

func TestTheOldNameMatcherCatchesThePlants(t *testing.T) {
	xar := append([]byte("xar!\x00\x1c\x00\x01\x00\x00"), []byte(`<pkg-info format-version="2" identifier="com.`+old+`.firstboot" version="1.0">`)...)
	xar = append(xar, 0, 0, 0)
	note := "Names: renamed on 2026-10-17, from `" + old + "`."
	for _, c := range []struct {
		name, path string
		body       []byte
	}{
		{"a script calling the old helper", "bin/x.sh", []byte("set -e\nexec /usr/local/sbin/" + old + "-shutdown -h\n")},
		{"the reviewer's planted script, its comment saying renamed", "bin/planted.sh", []byte("set -e\n# The helper was renamed on 2026-10-17.\nexec /usr/local/sbin/" + old + "-shutdown -h\n")},
		{"an old conf variable", "assets/guest/firstboot.sh", []byte("x=1\n" + oldUpper + "_FB_USER=vagrant\n")},
		{"the old label", "x.plist", []byte("<string>com." + old + ".firstboot</string>\n")},
		{"an old marker file", "internal/firmware/x.go", []byte(`marker := ".` + old + `-prepared"` + "\n")},
		{"an old shell local", "assets/privops/init.sh", []byte(old + "_mnt=/src1\n")},
		{"a Go string's \\n before an old heredoc delimiter", "internal/payload/x.go", []byte(`b := "key\n` + oldUpper + `_EOF_KEYS\n"` + "\n")},
		{"an old name in an undocumented doc paragraph", "docs/planted.md", []byte("# x\n\nThe first boot reads " + oldUpper + "_FB_USER.\n")},
		{"an allowlisted doc whose old name is outside its note", "docs/decisions/0019-every-shutdown-through-guest-macosx-shutdown.md", []byte("# 0019\n\n" + note + "\n\nRun " + old + "-shutdown.\n")},
		{"an allowlisted doc with two notes", "docs/configuration-register.md", []byte("# r\n\n" + note + "\n\n" + note + "\n")},
		{"an allowlisted doc with no note", "release-notes/v0.20261031.1.md", []byte("Nothing here.\n")},
		{"a later release note", "release-notes/v0.20261031.2.md", []byte("Use " + old + "-shutdown.\n")},
		{"gzip'd text carrying the old identifier", "internal/x/testdata/golden/p.gz", gz(t, []byte("identifier com."+old+".firstboot\n"))},
		{"a gzip'd binary package carrying the old identifier", "internal/payload/testdata/golden/reference.pkg.gz", gz(t, xar)},
		{"a file named with the old stem", "tests/" + old + "-probe.txt", []byte("nothing\n")},
	} {
		if got := oldNameFindings(c.path, c.body); len(got) == 0 {
			t.Errorf("%s (%s): not caught", c.name, c.path)
		}
	}
}

func TestTheOldNameMatcherLeavesTheRestAlone(t *testing.T) {
	blob := randomBlob(t, holdsStem)
	b64 := randomBlob(t, func(b []byte) bool { return holdsStem([]byte(base64.StdEncoding.EncodeToString(b))) })
	pem := []byte("-----BEGIN PUBLIC KEY-----\n" + base64.StdEncoding.EncodeToString(b64) + "\n-----END PUBLIC KEY-----\n")
	note := "Names: renamed on 2026-10-17, from `" + old + "-shutdown`."
	for _, c := range []struct {
		name, path string
		body       []byte
	}{
		{"a random 1.4 MB blob holding the stem's letters", "internal/x/testdata/golden/blob.bin", blob},
		{"the same blob gzip'd", "internal/x/testdata/golden/blob.img.gz", gz(t, blob)},
		{"base64 holding the stem's letters", "assets/keys/k.pem", pem},
		{"the letters inside a word", "docs/x.md", []byte("x" + oldUpper + "NBGy and s" + old + "1 and " + old + "x\n")},
		{"an allowlisted doc's one note", "docs/decisions/0021-guest-macosx-shutdown-waits-for-jobs-still-starting.md", []byte("# 0021\n\n" + note + "\n\nThe helper is guest-macosx-shutdown.\n")},
		{"a release note from before the rename", "release-notes/v0.20261005.8.md", []byte("Use " + old + "-shutdown.\n")},
	} {
		if got := oldNameFindings(c.path, c.body); len(got) != 0 {
			t.Errorf("%s (%s): caught, and should not be: %v", c.name, c.path, got)
		}
	}
	if !holdsStem(pem) {
		t.Fatal("the base64 plant does not hold the stem's letters, so it shows nothing")
	}
}
