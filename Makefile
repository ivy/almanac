# Build, test, and lint for Almanac. Tools come from mise.toml: run `mise install` once.

ALMANAC := almanacs/renovate

.PHONY: all build test test-fast lint fmt generate check-public check clean

all: build

build:
	cargo build --workspace --locked
	cd $(ALMANAC) && cargo build --locked

# Every test. almanacs keep their own Cargo workspaces, so each is tested on its own.
test:
	cargo nextest run --workspace --locked --no-tests=pass
	cd $(ALMANAC) && cargo nextest run --locked --no-tests=pass

# The tests quick enough for a commit hook. Compile-fail cases are left to `make test`.
test-fast:
	cargo nextest run --workspace --locked --no-tests=pass --exclude fails

lint:
	cargo fmt --all --check
	cargo clippy --workspace --all-targets --locked -- -D warnings
	cd $(ALMANAC) && cargo fmt --check && cargo clippy --all-targets --locked -- -D warnings

fmt:
	cargo fmt --all
	cd $(ALMANAC) && cargo fmt

generate:
	python3 -I tools/gen_github.py specs/github/api.github.com.json crates/github/projection.toml > crates/github/src/lib.rs

check-public:
	tools/check-public.sh

# The gate. A change is not done until this passes.
check: lint test check-public
	cargo deny --locked check

clean:
	cargo clean
	cd $(ALMANAC) && cargo clean
