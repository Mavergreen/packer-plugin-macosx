// Command fetch-pinned fetches one source of the embedded registry
// (assets/pins/sources.tsv) into a cache directory, verified against its
// pinned checksum, and prints the file's path.
//
// It is for the repository's own CI, which needs a pinned source the
// plugin never fetches for anyone (10.6's install disc: the plugin takes
// the user's own, docs/decisions/0014). It is deliberately not part of
// the plugin binary or of what a release builds: .goreleaser.yml builds
// ./cmd/packer-plugin-macosx only.
//
//	go run ./cmd/fetch-pinned <name> <dir>
//
// Exit 0 with the path on stdout; 1 on a fetch or verify failure; 2 on
// bad usage or a name the registry does not pin.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/Mavergreen/packer-plugin-macosx/internal/config"
	"github.com/Mavergreen/packer-plugin-macosx/internal/fetch"
	"github.com/Mavergreen/packer-plugin-macosx/internal/pins"
)

// run is main without the process: reg and base are injected so a test
// can use a registry of its own and a server of its own. base's Paths is
// replaced by args[1].
func run(ctx context.Context, args []string, reg *pins.Registry, base *fetch.Getter, stdout, stderr io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(stderr, "usage: fetch-pinned <name> <dir>")
		return 2
	}
	name, dir := args[0], args[1]
	// Looked up first so that a name the registry does not pin is a usage
	// error, told apart from a download that failed.
	if _, err := reg.Lookup(name); err != nil {
		fmt.Fprintln(stderr, "fetch-pinned:", err)
		return 2
	}
	g := *base
	g.Paths = config.Paths{Home: dir}
	if g.Log == nil {
		g.Log = func(format string, a ...any) { fmt.Fprintf(stderr, format+"\n", a...) }
	}
	path, err := g.Pinned(ctx, reg, name)
	if err != nil {
		fmt.Fprintln(stderr, "fetch-pinned:", err)
		return 1
	}
	fmt.Fprintln(stdout, path)
	return 0
}

func main() {
	reg, err := pins.Embedded()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fetch-pinned:", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], reg, &fetch.Getter{}, os.Stdout, os.Stderr))
}
