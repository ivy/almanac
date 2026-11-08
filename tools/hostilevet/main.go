// Command hostilevet is the taint analyzer, run by go vet:
//
//	go build -o bin/hostilevet ./tools/hostilevet
//	go vet -vettool=$PWD/bin/hostilevet ./...
//
// The rules it enforces are in package taint.
package main

import (
	"golang.org/x/tools/go/analysis/unitchecker"

	"github.com/ivy/almanac/tools/hostilevet/taint"
)

func main() { unitchecker.Main(taint.Analyzer) }
