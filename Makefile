# Build, test, and lint for Almanac. Tools come from mise.toml: run `mise install` once.

ALMANAC := almanacs/renovate

# Queries are checked against crates/almanac/.sqlx, so a build needs no database.
export SQLX_OFFLINE := true

# Database tests start PostgreSQL through podman.
export DOCKER_HOST ?= unix:///run/user/$(shell id -u)/podman/podman.sock
export TESTCONTAINERS_RYUK_DISABLED ?= true

DB_TESTS := binary(store_postgres) or binary(workflows_on_postgres) or binary(broker_flow) or binary(daemon)
SANDBOX_TESTS := binary(podman) or binary(real_cli)
SLOW_TESTS := $(DB_TESTS) or $(SANDBOX_TESTS) or binary(scaffold)

.PHONY: all build package test test-db test-sandbox test-e2e test-scaffold test-fast lint fmt generate generated sqlx-prepare check-public verify-units check-isolated-demo check clean

all: build

build:
	cargo build --workspace --locked
	cd $(ALMANAC) && cargo build --locked

# Release binaries in dist/, for docs/deploying.md.
package:
	cargo build --release --locked --package broker
	cd $(ALMANAC) && cargo build --release --locked
	mkdir -p dist
	cp target/release/almanac-broker $(ALMANAC)/target/release/almanac-renovate dist/

# Every test. almanacs keep their own Cargo workspaces, so each is tested on its own.
test:
	cargo nextest run --workspace --locked --no-tests=pass -E 'not ($(SLOW_TESTS))'
	cd $(ALMANAC) && cargo nextest run --locked --no-tests=pass -E 'not (binary(daemon) or binary(e2e))'

# The real broker and daemon as processes, on PostgreSQL, with the real CLI and Podman. Only the
# model and GitHub are simulated.
test-e2e:
	podman build -q -t localhost/almanac-fixer:test images/fixer
	cargo build --locked --package broker
	cd $(ALMANAC) && cargo nextest run --locked -E 'binary(e2e)'

# Scaffolds an almanac and compiles it, which takes a while.
test-scaffold:
	cargo nextest run --workspace --locked -E 'binary(scaffold)'

# Isolation, proved with real Podman and the real CLI against a stub model. No model is called.
test-sandbox:
	podman build -q -t localhost/almanac-fixer:test images/fixer
	cargo nextest run --workspace --locked -E '$(SANDBOX_TESTS)'

# The conformance suite and every workflow test, against a real PostgreSQL.
test-db:
	cargo nextest run --workspace --locked -E '$(DB_TESTS)'
	cd $(ALMANAC) && cargo nextest run --locked -E 'binary(daemon)'

# The tests quick enough for a commit hook. Compile-fail cases are left to `make test`.
test-fast:
	cargo nextest run --workspace --locked --no-tests=pass --exclude fails -E 'not ($(SLOW_TESTS))'

lint:
	cargo fmt --all --check
	cargo clippy --workspace --all-targets --locked -- -D warnings
	cd $(ALMANAC) && cargo fmt --check && cargo clippy --all-targets --locked -- -D warnings

fmt:
	cargo fmt --all
	cd $(ALMANAC) && cargo fmt

generate:
	cargo xtask generate

# Refreshes .sqlx after a query or migration changes. Needs DATABASE_URL, with the migrations in
# crates/almanac and crates/deliveries applied.
sqlx-prepare:
	cd crates/almanac && SQLX_OFFLINE=false cargo sqlx prepare

check-public:
	tools/check-public.sh

verify-units:
	tools/verify-units.sh

generated:
	cargo xtask generate --check

# The optional synthetic demo is a standalone workspace, like the Renovate almanac.
check-isolated-demo:
	python -m unittest discover -s contrib/isolated-demo -p 'test_*.py'
	for script in contrib/isolated-demo/*.sh contrib/isolated-demo/runtime/*.sh contrib/isolated-demo/runtime/almanac-bootstrap contrib/isolated-demo/runtime/nested-runtime-setup contrib/isolated-demo/runtime/start-private-runtime; do bash -n "$$script" || exit; done
	cargo fmt --manifest-path contrib/isolated-demo/Cargo.toml --check
	cargo clippy --manifest-path contrib/isolated-demo/Cargo.toml --all-targets --locked -- -D warnings
	cargo test --manifest-path contrib/isolated-demo/Cargo.toml --locked

# The gate. A change is not done until this passes.
check: lint generated test test-db test-sandbox test-e2e test-scaffold check-public verify-units check-isolated-demo
	cargo deny --locked check

clean:
	cargo clean
	cd $(ALMANAC) && cargo clean
