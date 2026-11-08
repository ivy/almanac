# An input file is not a report

## What happened

A file named on the command line as a report, such as `compare --rust FILE`, is not what `report --format json` prints: it is not JSON, lacks a field every report has, or names a repository, branch or state no report would.

## Why it matters

Nothing was compared. A comparison of a file that is not a report would mark differences that are not there.

## What to do next

Write the report again with `almanac-renovate --format json report > FILE`, from the Rust binary or the Go one, and pass the file unchanged. The error's message says what is wrong with it.
