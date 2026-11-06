# Build, test, and lint for Almanac. Tools come from mise.toml: run `mise install` once.

ALMANAC := almanacs/renovate

# Queries are checked against crates/almanac/.sqlx, so a build needs no database.
export SQLX_OFFLINE := true

# Database tests start PostgreSQL through podman.
export DOCKER_HOST ?= unix:///run/user/$(shell id -u)/podman/podman.sock
export TESTCONTAINERS_RYUK_DISABLED ?= true

DB_TESTS := binary(store_postgres) or binary(workflows_on_postgres)

.PHONY: all build test test-db test-fast lint fmt generate sqlx-prepare check-public check clean

all: build

build:
	cargo build --workspace --locked
	cd $(ALMANAC) && cargo build --locked

# Every test. almanacs keep their own Cargo workspaces, so each is tested on its own.
test:
	cargo nextest run --workspace --locked --no-tests=pass -E 'not ($(DB_TESTS))'
	cd $(ALMANAC) && cargo nextest run --locked --no-tests=pass

# The conformance suite and every workflow test, against a real PostgreSQL.
test-db:
	cargo nextest run --workspace --locked -E '$(DB_TESTS)'

# The tests quick enough for a commit hook. Compile-fail cases are left to `make test`.
test-fast:
	cargo nextest run --workspace --locked --no-tests=pass --exclude fails -E 'not ($(DB_TESTS))'

lint:
	cargo fmt --all --check
	cargo clippy --workspace --all-targets --locked -- -D warnings
	cd $(ALMANAC) && cargo fmt --check && cargo clippy --all-targets --locked -- -D warnings

fmt:
	cargo fmt --all
	cd $(ALMANAC) && cargo fmt

generate:
	python3 -I tools/gen_github.py specs/github/api.github.com.json crates/github/projection.toml > crates/github/src/events.rs

# Refreshes .sqlx after a query or migration changes. Needs DATABASE_URL, migrated.
sqlx-prepare:
	cd crates/almanac && SQLX_OFFLINE=false cargo sqlx prepare

check-public:
	tools/check-public.sh

# The gate. A change is not done until this passes.
check: lint test test-db check-public
	cargo deny --locked check

clean:
	cargo clean
	cd $(ALMANAC) && cargo clean
