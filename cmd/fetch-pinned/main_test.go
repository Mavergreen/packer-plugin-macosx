package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Mavergreen/packer-plugin-macosx/internal/config"
	"github.com/Mavergreen/packer-plugin-macosx/internal/fetch"
	"github.com/Mavergreen/packer-plugin-macosx/internal/pins"
)

func fixture(t *testing.T, body, sum string) (reg *pins.Registry, g *fetch.Getter, home string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	reg, err := pins.Parse(strings.NewReader("disc\t" + srv.URL + "/a%20disc.dmg\t" + sum + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	home = t.TempDir()
	return reg, &fetch.Getter{Paths: config.Paths{Home: home}, Retries: -1}, home
}

func sum(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func TestPrintsThePathAndExitsZero(t *testing.T) {
	reg, g, _ := fixture(t, "disc bytes", sum("disc bytes"))
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"disc", t.TempDir()}, reg, g, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	path := strings.TrimSpace(out.String())
	if b, err := os.ReadFile(path); err != nil || string(b) != "disc bytes" {
		t.Fatalf("path %q: %q %v", path, b, err)
	}
}

func TestAnUnknownNameExitsTwoNamingIt(t *testing.T) {
	reg, g, _ := fixture(t, "x", sum("x"))
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"nope", t.TempDir()}, reg, g, &out, &errb); code != 2 || !strings.Contains(errb.String(), "nope") {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}

func TestUsageExitsTwo(t *testing.T) {
	reg, g, _ := fixture(t, "x", sum("x"))
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"disc"}, reg, g, &out, &errb); code != 2 {
		t.Fatalf("exit %d", code)
	}
}

func TestAMismatchExitsOneAndLeavesNothing(t *testing.T) {
	reg, g, _ := fixture(t, "other bytes", sum("disc bytes"))
	home := t.TempDir()
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"disc", home}, reg, g, &out, &errb); code != 1 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if out.Len() != 0 || errb.Len() == 0 {
		t.Fatalf("stdout %q stderr %q", out.String(), errb.String())
	}
	if ents, _ := os.ReadDir(home); len(ents) != 0 {
		t.Fatalf("left behind: %v", ents)
	}
}
