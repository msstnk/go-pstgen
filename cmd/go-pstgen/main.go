// go-pstgen generates dummy PSTs in bulk, in pure Go, modeled on the Purview export format.
//
// Output: <output_dir>/<UPN>.pst (split into <UPN>-2.pst, <UPN>-3.pst ... when split_size_mb is exceeded)
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/msstnk/go-pstgen/internal/config"
	"github.com/msstnk/go-pstgen/internal/gen"
)

func main() {
	cfgPath := flag.String("config", "config.json", "JSON config file")
	initConfig := flag.Bool("init", false, "write a minimal config.json to the current directory and exit")
	summary := flag.String("summary", "summary.tsv", "write the per-PST item counts (file, folder, count; TSV) to this file after generating; fails if it already exists. Empty disables it")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), credit, "\nUsage:\n  go-pstgen [--config config.json] [--summary summary.tsv] | --init | --version\n\nOptions:\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVersion {
		fmt.Println("go-pstgen", versionString())
		return
	}
	if *initConfig {
		if err := config.WriteTemplate("config.json"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("wrote config.json")
		return
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// Fail before generating anything rather than after hours of work
	if *summary != "" {
		if err := gen.CheckSummaryPath(*summary); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	files, runErr := gen.Run(cfg)
	if runErr != nil {
		fmt.Fprintln(os.Stderr, runErr)
	}
	if *summary != "" && len(files) > 0 {
		if err := gen.WriteSummary(*summary, files); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("wrote", *summary)
	}
	if runErr != nil {
		os.Exit(1)
	}
}
