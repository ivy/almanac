// Package e2e holds the Renovate almanac's end-to-end specs, and nothing else. They run the
// almanac-broker and almanac-renovate binaries as processes, on PostgreSQL, with the real
// claude CLI against a stub model and rootless Podman. Only GitHub, the model, the package
// registry and the advisory feed are simulated. `make go-test-e2e` runs them.
package e2e
