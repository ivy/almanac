# A listen address is refused

## What happened

A daemon was given an address to listen on that is not a loopback IP address and port, such as `127.0.0.1:8790`, or the host would not let it listen there, usually because another process already holds the port.

## Why it matters

A daemon's endpoints are for a supervisor or a person on the same host. Anything that must reach them from elsewhere goes through an ingress in front, so a daemon never listens beyond loopback. One that cannot listen cannot be watched, so it does not start.

## What to do next

Set the variable the error names to a free loopback address and port, or stop whatever holds the port. `ss -ltnp` shows what listens where.
