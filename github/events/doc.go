// Package events holds GitHub's webhook events as workflows read them, generated from
// projection.toml and checked against specs/github/api.github.com.json (R-37). A field's text
// is hostile unless the projection reads it as a Slug or a Sha, and an enumerated field reads a
// value GitHub adds later as Unknown (R-38).
package events

//go:generate go run ../../tools/eventgen -spec ../../specs/github/api.github.com.json -projection projection.toml -out events.go
