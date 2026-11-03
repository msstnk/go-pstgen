package main

import "github.com/msstnk/go-pstgen/internal/buildinfo"

// version is the release version of go-pstgen (tag releases as v<version>).
const version = "v0.1.8"

var credit = buildinfo.Credit("go-pstgen", version, "dummy PST file generator")
