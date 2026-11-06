# Sandbox spike

The proof behind [ADR 1](../../docs/adrs/0001-agent-sandbox.md). It is throwaway: the real implementation belongs in the library.

```bash
spikes/sandbox/run.sh
```

It needs podman and the `claude` CLI. It makes no model call and reads no real credential: `stub_model.py` stands in for the model and `sandbox_mcp.py` gives the agent its only tools. Set `HARNESS_FLAGS` to try other harness flags in place of `--bare`.
