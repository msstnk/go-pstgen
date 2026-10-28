package main

import "runtime/debug"

// version is the release version of go-pstgen (tag releases as v<version>).
const version = "v0.1.4"

// versionString is the --version output: version plus the VCS commit id embedded by the Go toolchain,
// suffixed "-dirty" for builds from a tree with uncommitted changes. Builds without VCS info (e.g. go install pkg@version) show the version only.
func versionString() string {
	var rev, modified string
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, kv := range info.Settings {
			switch kv.Key {
			case "vcs.revision":
				rev = kv.Value
			case "vcs.modified":
				modified = kv.Value
			}
		}
	}
	if rev == "" {
		return version
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if modified == "true" {
		rev += "-dirty"
	}
	return version + " (commit " + rev + ")"
}

const credit = `go-pstgen ` + version + ` - dummy PST file generator
Copyright (c) 2026 msstnk. Released under the MIT License.
Written from the public [MS-PST] specification; not affiliated with or endorsed by Microsoft.
https://github.com/msstnk/go-pstgen
`
