# Optional nested development runtime

This Linux amd64 example captures the development-only changes needed for real Podman database, sandbox and Claude-CLI tests inside a rootless outer container. It is not a production image, a minimal security profile, or a guarantee on every kernel/runtime. This portable recipe is an example derived from a tested development runtime; a fresh build of this exact exported recipe has not been validated. Prefer native Linux Podman when available. Never modify a production profile to make these tests pass.

```sh
podman build -t localhost/almanac-isolated-dev -f contrib/isolated-demo/runtime/Containerfile contrib/isolated-demo/runtime
podman volume create almanac-isolated-work
bash contrib/isolated-demo/runtime/run-workspace.sh
```

The launcher uses a private named `/work` volume (no host source, credentials or engine socket), private PID/mount/network namespaces, pasta networking, read-only root, temporary `/run` and `/tmp`, no-new-privileges, default seccomp, SELinux `container_engine_t` with private volume relabel, and an outer 2 CPU/4 GiB limit. It drops all capabilities then adds the explicitly reviewed development set. The host must support rootless Podman, this SELinux policy and pasta. The example does not install host policy or disable SELinux/seccomp. Namespace and MCS peer isolation remain operator verification requirements, especially with shared rootless user namespaces.

`SYS_ADMIN`, `NET_ADMIN`, `MKNOD` and the other listed capabilities are broad development exceptions, not production recommendations. In the tested nested runtime, outer `SYS_PTRACE` was additionally required for Podman exec/copy into non-root inner containers: without it, procfs namespace/root access failed with EACCES. This is a trusted-development workaround, not evidence that it is a minimum or safe general sandbox grant. Do not replace these exceptions with `--privileged`, host namespaces, an unconfined seccomp/SELinux profile, or a host engine socket. No provider credentials belong in this runtime.

At startup `nested-runtime-setup` creates private state directories and bind-remounts only `/proc/sys/net` writable inside the outer private namespaces, verifying its parent `/proc/sys` remains read-only. This is required for inner bridge networking and needs the reviewed outer capabilities; ordinary project commands must not repeat or widen it. Startup starts the inner Podman API service on `/run/podman/podman.sock`. Workspace teardown must kill the service, supervisor and every descendant.

Inner Podman uses VFS, `/work/.local/share/containers`, volatile `/run/containers`, disabled inner cgroups, cgroupfs manager, file events and k8s-file logs. The outer cgroup still limits descendants. Buildah uses chroot isolation. Temporary build data stays at `/work/tmp`. No host runtime socket is used. Default seccomp, no-new-privileges, and inner sandbox capability restrictions remain in place.

The image pins Fedora by digest, mise 2026.11.3 and Claude Code 2.1.319 with SHA256 verification, plus jj 0.45.1 and Bun 1.4.2. Fedora RPMs resolve from signed repositories during build, so rebuilds can differ. The `openssl` executable is necessary for test key generation; OpenSSL headers alone are insufficient. `systemd-analyze`, a C/C++ linker, Podman and crun are also included. Claude auto-update is disabled and no auth/config is installed.

Inside the container, clone the public checkout into `/work/almanac`, review its mise configuration, then run `almanac-bootstrap /work/almanac`. This explicitly trusts the checkout, installs its locked tools plus rustfmt/clippy, and runs the complete `mise exec -- make check`. To build just the demo after setup, pass a command after that checkout path. Use writable workspace Cargo/Rustup/mise homes; do not run with another user's home or the immutable image tool cache. The checkout remains the source of Rust/hk/pkl/testing tool pins.

After successful setup, follow the parent demo README. Autostart, if desired, should explicitly invoke launch after tools/binaries/runtime are ready, and fixture-live must remain opt-in. Do not put logs, fixture exports, gate evidence, environment snapshots, private paths, runtime credentials or deployed infrastructure identifiers in Git.
