#!/usr/bin/env bash
# Refuses to publish staged content that identifies private infrastructure.
# Names come from the gitignored private/denylist.txt, one extended regex per
# line, because committing the list would publish the names. specs/ is upstream
# text and is skipped.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

pathspec=(. ':(exclude)specs' ':(exclude)tools/check-public.sh')
found=0

machine='/home/[a-z]|/Users/[A-Za-z]|~/src/|\.ts\.net\b|\b192\.168\.[0-9]+\.[0-9]+\b|\b100\.(6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7])\.[0-9]+\.[0-9]+\b'
if git grep --cached -nIE "$machine" -- "${pathspec[@]}"; then
  found=1
fi

if [[ -f private/denylist.txt ]]; then
  if git grep --cached -nIwiE -f private/denylist.txt -- "${pathspec[@]}"; then
    found=1
  fi
else
  echo "check-public: private/denylist.txt is missing, so private names were not checked" >&2
fi

if [[ $found -ne 0 ]]; then
  echo "check-public: the matches above must not be published. See 'Working in the open' in AGENTS.md." >&2
  exit 1
fi
