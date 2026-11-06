#!/usr/bin/env bash
# Verifies the example systemd units. The binaries are not installed on a build host, so a copy
# points ExecStart at one that exists, which checks everything else about the unit.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

export XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
work="$(mktemp -d)"
trap 'rm -rf -- "$work"' EXIT

cp contrib/systemd/almanac-broker.service "$work/"
cp contrib/systemd/almanac@.service "$work/almanac@renovate.service"
sed -i 's#^ExecStart=%h/.local/bin/[^ ]*#ExecStart=/usr/bin/true#' "$work"/*.service
systemd-analyze verify --user --man=no "$work"/*.service
