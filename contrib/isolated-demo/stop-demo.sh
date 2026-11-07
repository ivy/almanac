#!/usr/bin/env bash
set -eu
here="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
exec python3 "$here/demo.py" stop
