package macosx_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// shellOfShebang is lib/shebang.sh's shebang_shell, the lint's one
// shebang matcher, asked through bash: whether line is a shell's shebang.
func shellOfShebang(t *testing.T, line string) (string, bool) {
	t.Helper()
	out, err := exec.Command("bash", "-c", `. lib/shebang.sh && shebang_shell "$1"`, "shebang_shell", line).Output()
	if err != nil {
		if _, exit := err.(*exec.ExitError); exit {
			return "", false
		}
		t.Fatalf("asking lib/shebang.sh about %q: %v", line, err)
	}
	return strings.TrimSpace(string(out)), true
}

// goStringScripts is every string literal in the Go source src (named
// path) whose text begins with a shell's shebang, as "path:line: shell".
func goStringScripts(t *testing.T, path string, src []byte) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	var found []string
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil || !strings.HasPrefix(s, "#!") {
			return true
		}
		first, _, _ := strings.Cut(s, "\n")
		if sh, ok := shellOfShebang(t, first); ok {
			found = append(found, fmt.Sprintf("%s:%d: %s", path, fset.Position(lit.Pos()).Line, sh))
		}
		return true
	})
	return found
}

// No shell script lives in a Go string, where neither lint pass can read
// it: a production script is a real file the Go embeds (go:embed), and a
// test's stub is a file under its package's testdata/, and the lint reads
// both (bin/list-shell-files.sh). A Go string literal beginning with a
// shell's shebang, by lib/shebang.sh's judgement, fails this.
func TestNoShellScriptInAGoString(t *testing.T) {
	checked := 0
	for _, path := range projectFiles(t) {
		if !strings.HasSuffix(path, ".go") {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, f := range goStringScripts(t, path, b) {
			t.Errorf("%s script in a Go string literal: move it to a file the lint reads (go:embed for the plugin's own, testdata/*.sh for a test's)", f)
		}
	}
	if checked < 50 {
		t.Fatalf("checked %d Go files; the listing is not finding the tree", checked)
	}
}

func TestTheGoStringGuardCatchesThePlants(t *testing.T) {
	src := []byte("package p\n\n" +
		"var a = \"#!/bin/sh\\necho a\\n\"\n" +
		"var b = `#! /usr/bin/env -S bash -e\necho b\n`\n" +
		"var c = \"#!/usr/bin/env python3\\nprint(1)\\n\"\n" +
		"var d = \"# not a shebang\\n#!/bin/sh\\n\"\n" +
		"var e = fmt.Sprintf(\"#!/bin/dash\\nexec %s\\n\", x)\n")
	got := goStringScripts(t, "p.go", src)
	want := []string{"p.go:3: sh", "p.go:4: bash", "p.go:9: dash"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("found %v, want %v", got, want)
	}
}
