// Package docs carries the documents an almanac binary shows at run time: the
// long form of each error code, which explain prints.
//
// It exists so the pages can stay with the rest of the documentation and still
// be embedded: an embed reaches only files at or below its own package.
package docs

import "embed"

// Errors holds errors/<code>.md for every error code, with each "::" in the
// code written as ".". A code only the Go almanac has is in errors-go/ instead,
// so that the Rust almanac's check of errors/ does not count it.
//
//go:embed errors/*.md errors-go/*.md
var Errors embed.FS
