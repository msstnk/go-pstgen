// go-pstgen generates dummy PSTs in bulk, in pure Go, modeled on the Purview export format.
//
// Output: <output_dir>/<UPN>.pst (split into <UPN>-2.pst, <UPN>-3.pst ... when split_size_mb is exceeded)
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/msstnk/go-pstgen/internal/buildinfo"
	"github.com/msstnk/go-pstgen/internal/config"
	"github.com/msstnk/go-pstgen/internal/gen"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// run is main with its inputs and outputs made explicit; it returns the exit status.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("go-pstgen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "config.json", "JSON config file")
	initConfig := fs.Bool("init", false, "write a minimal config.json to the current directory and exit")
	summary := fs.String("summary", "summary.tsv", "write the per-PST item counts (file, folder, count; TSV) to this file after generating; fails if it already exists. Empty disables it")
	showVersion := fs.Bool("version", false, "print the version and exit")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), credit, "\nUsage:\n  go-pstgen [--config config.json] [--summary summary.tsv] | --init | --version\n\nOptions:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, "go-pstgen", buildinfo.String(version))
		return 0
	}
	if *initConfig {
		if err := config.WriteTemplate("config.json"); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, "wrote config.json")
		return 0
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	// Fail before generating anything rather than after hours of work
	if *summary != "" {
		if err := gen.CheckSummaryPath(*summary); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	files, runErr := gen.Run(cfg)
	if runErr != nil {
		fmt.Fprintln(stderr, runErr)
	}
	if *summary != "" && len(files) > 0 {
		if err := gen.WriteSummary(*summary, files); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, "wrote", *summary)
	}
	if runErr != nil {
		return 1
	}
	return 0
}
