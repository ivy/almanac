# A scan could not finish

## What happened

A workflow's scan failed while listing current subjects, usually because a forge could not be reached, or because `backfill --repo` named a repository the scan cannot see.

## Why it matters

Events found before the failure are kept, and scanning again is safe, because scanned events have stable delivery IDs. Anything not yet found stays unnoticed until a scan completes.

## What to do next

Fix what the error names and run `backfill` again. For a repository the scan cannot see, check its name and that it is not archived. A GitHub App sees only the repositories it is installed on, so add the repository to the installation.
