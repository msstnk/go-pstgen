package main

import "github.com/msstnk/go-pstgen/internal/buildinfo"

// version is the release version of go-pstprune (tag releases as v<version>).
const version = "v0.1.7"

var credit = buildinfo.Credit("go-pstprune", version, "PST folder pruner")
