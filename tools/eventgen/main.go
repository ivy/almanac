// Command eventgen writes GitHub event types from a projection: a declared list of fields,
// each checked against GitHub's pinned API spec (R-37). go generate runs it in github/events,
// and make go-lint runs it with -check, which fails if the committed code is stale.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "eventgen: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("eventgen", flag.ContinueOnError)
	specPath := flags.String("spec", "", "GitHub's OpenAPI description, as JSON")
	projectionPath := flags.String("projection", "", "the projection, as TOML")
	out := flags.String("out", "", "the Go file to write")
	pkg := flags.String("package", "events", "the package the Go file belongs to")
	check := flags.Bool("check", false, "fail if -out is not what would be written, and write nothing")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *specPath == "" || *projectionPath == "" || *out == "" {
		return errors.New("-spec, -projection and -out are required")
	}
	source, err := fromFiles(*specPath, *projectionPath, *pkg)
	if err != nil {
		return err
	}
	if !*check {
		return os.WriteFile(*out, source, 0o644)
	}
	current, err := os.ReadFile(*out)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if !bytes.Equal(current, source) {
		return fmt.Errorf("%s is stale: run `make go-generate` and commit the result", *out)
	}
	return nil
}

func fromFiles(specPath, projectionPath, pkg string) ([]byte, error) {
	raw, err := os.ReadFile(specPath)
	if err != nil {
		return nil, err
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("%s: %w", specPath, err)
	}
	projection, err := os.ReadFile(projectionPath)
	if err != nil {
		return nil, err
	}
	source, err := generate(document, projection, pkg)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", projectionPath, err)
	}
	return source, nil
}
