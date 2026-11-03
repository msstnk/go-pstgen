// go-pstprune detaches folders (and everything below them) from PST files, in place.
//
// Intended as a pre-processing step for Purview exports: the Recoverable-Items / litigation hold folders are cut out of
// the NBT, and the blocks they used are filled with 0x00, so migration tools and users never see them.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/msstnk/go-pstgen/internal/buildinfo"
	"github.com/msstnk/go-pstgen/pst"
)

// listFlag is a repeatable string flag.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// run is main with its inputs and outputs made explicit; it returns the exit status.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("go-pstprune", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var files, paths listFlag
	fs.Var(&files, "file", "PST file or glob, e.g. 'output/*.pst' (repeatable; extra arguments are treated as files too)")
	fs.Var(&paths, "path", "folder path to prune, e.g. 'Recoverable-Items/*' (repeatable; segments are case-insensitive globs)")
	noFill := fs.Bool("no-fill", false, "only detach the nodes from the NBT; do not zero-fill their blocks")
	dryRun := fs.Bool("dry-run", false, "report what would be pruned without modifying any file")
	countItems := fs.Bool("count-items", false, "print the number of items per folder as TSV (file, folder, count) and exit; read-only, --path is ignored")
	showVersion := fs.Bool("version", false, "print the version and exit")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), credit, "\nUsage:\n  go-pstprune --file output/*.pst --path \"Recoverable-Items/*\" [--no-fill] [--dry-run]\n  go-pstprune --count-items --file output/*.pst\n\nFiles are modified in place.\n\nOptions:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, "go-pstprune", buildinfo.String(version))
		return 0
	}
	// An unquoted glob is expanded by the shell, so the files after the first one arrive as positional arguments.
	files = append(files, fs.Args()...)
	if len(files) == 0 || (len(paths) == 0 && !*countItems) {
		fs.Usage()
		return 2
	}
	var targets []string
	for _, pat := range files {
		m, err := filepath.Glob(pat)
		if err != nil {
			fmt.Fprintf(stderr, "bad --file pattern %q: %v\n", pat, err)
			return 2
		}
		if len(m) == 0 {
			m = []string{pat} // let the open fail with a proper message
		}
		targets = append(targets, m...)
	}

	failed := 0
	if *countItems {
		for _, file := range targets {
			counts, err := pst.CountItems(file)
			if err != nil {
				fmt.Fprintf(stderr, "%s: %v\n", file, err)
				failed++
				continue
			}
			for _, c := range counts {
				fmt.Fprintln(stdout, c.TSV(file))
			}
		}
		if failed > 0 {
			return 1
		}
		return 0
	}
	for _, file := range targets {
		res, err := pst.Prune(file, pst.PruneOptions{Paths: paths, NoFill: *noFill, DryRun: *dryRun})
		switch {
		case errors.Is(err, pst.ErrNoMatch):
			fmt.Fprintf(stdout, "%s: no matching folder, skipped\n", file)
		case err != nil:
			fmt.Fprintf(stderr, "%s: %v\n", file, err)
			failed++
		default:
			verb := "pruned"
			if *dryRun {
				verb = "would prune"
			}
			fmt.Fprintf(stdout, "%s: %s %d folder(s) [%s] and %d loose item(s): %d nodes, %d table rows", file, verb, len(res.Folders), strings.Join(res.Folders, ", "), res.Items, res.Nodes, res.Rows)
			if !*noFill {
				fmt.Fprintf(stdout, ", %d blocks (%.1f MiB) zero-filled", res.Blocks, float64(res.Bytes)/(1<<20))
			}
			if res.Shared > 0 {
				fmt.Fprintf(stdout, ", %d shared blocks kept", res.Shared)
			}
			fmt.Fprintln(stdout)
		}
	}
	if failed > 0 {
		return 1
	}
	return 0
}
