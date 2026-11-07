#!/usr/bin/env bash
# Operator opt-in: broad capabilities for a disposable trusted development runtime only.
set -euo pipefail
exec podman run --rm -it --name almanac-isolated-dev \
  --network=pasta --dns=1.1.1.1 --cpus=2 --memory=4g --memory-swap=4g \
  --read-only --tmpfs /tmp:rw,nosuid,nodev,size=512m \
  --tmpfs /run:rw,nosuid,nodev,size=64m \
  --security-opt=no-new-privileges --security-opt=label=type:container_engine_t \
  --cap-drop=ALL --cap-add=CHOWN --cap-add=DAC_OVERRIDE --cap-add=FOWNER \
  --cap-add=FSETID --cap-add=KILL --cap-add=NET_BIND_SERVICE --cap-add=SETFCAP \
  --cap-add=SETGID --cap-add=SETPCAP --cap-add=SETUID --cap-add=SYS_CHROOT \
  --cap-add=SYS_ADMIN --cap-add=MKNOD --cap-add=NET_ADMIN --cap-add=SYS_PTRACE \
  --sysctl=net.ipv4.ip_forward=1 \
  --volume almanac-isolated-work:/work:Z \
  localhost/almanac-isolated-dev bash /usr/local/bin/start-private-runtime
