// go-pstprune detaches folders (and everything below them) from PST files, in place.
//
// Intended as a pre-processing step for Purview exports: the Recoverable-Items / litigation hold folders are cut out of
// the NBT, and the blocks they used are filled with 0x00, so migration tools and users never see them.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/msstnk/go-pstgen/pst"
)

// listFlag is a repeatable string flag.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

func main() {
	var files, paths listFlag
	flag.Var(&files, "file", "PST file or glob, e.g. 'output/*.pst' (repeatable; extra arguments are treated as files too)")
	flag.Var(&paths, "path", "folder path to prune, e.g. 'Recoverable-Items/*' (repeatable; segments are case-insensitive globs)")
	noFill := flag.Bool("no-fill", false, "only detach the nodes from the NBT; do not zero-fill their blocks")
	dryRun := flag.Bool("dry-run", false, "report what would be pruned without modifying any file")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), credit, "\nUsage:\n  go-pstprune --file output/*.pst --path \"Recoverable-Items/*\" [--no-fill] [--dry-run]\n\nFiles are modified in place.\n\nOptions:\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVersion {
		fmt.Println("go-pstprune", versionString())
		return
	}
	// An unquoted glob is expanded by the shell, so the files after the first one arrive as positional arguments.
	files = append(files, flag.Args()...)
	if len(files) == 0 || len(paths) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	var targets []string
	for _, pat := range files {
		m, err := filepath.Glob(pat)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bad --file pattern %q: %v\n", pat, err)
			os.Exit(2)
		}
		if len(m) == 0 {
			m = []string{pat} // let the open fail with a proper message
		}
		targets = append(targets, m...)
	}

	failed := 0
	for _, file := range targets {
		res, err := pst.Prune(file, pst.PruneOptions{Paths: paths, NoFill: *noFill, DryRun: *dryRun})
		switch {
		case errors.Is(err, pst.ErrNoMatch):
			fmt.Printf("%s: no matching folder, skipped\n", file)
		case err != nil:
			fmt.Fprintf(os.Stderr, "%s: %v\n", file, err)
			failed++
		default:
			verb := "pruned"
			if *dryRun {
				verb = "would prune"
			}
			fmt.Printf("%s: %s %d folder(s) [%s] and %d loose item(s): %d nodes, %d table rows", file, verb, len(res.Folders), strings.Join(res.Folders, ", "), res.Items, res.Nodes, res.Rows)
			if !*noFill {
				fmt.Printf(", %d blocks (%.1f MiB) zero-filled", res.Blocks, float64(res.Bytes)/(1<<20))
			}
			if res.Shared > 0 {
				fmt.Printf(", %d shared blocks kept", res.Shared)
			}
			fmt.Println()
		}
	}
	if failed > 0 {
		os.Exit(1)
	}
}
