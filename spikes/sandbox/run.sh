#!/usr/bin/env bash
# Runs the chosen design end to end against a stub model, then checks what happened.
# Needs podman and the claude CLI. Makes no model call and reads no real credential.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
work="$(mktemp -d)"
image="docker.io/library/rust:1-alpine"
sandbox="almanac-spike-sandbox"
prefetch="almanac-spike-prefetch"
port=18765
failures=0

cleanup() {
  [[ -n "${stub_pid:-}" ]] && kill "$stub_pid" 2>/dev/null || true
  podman rm -f "$sandbox" "$prefetch" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

check() {
  local name="$1" result="$2"
  if [[ "$result" == yes ]]; then
    echo "PASS  $name"
  else
    echo "FAIL  $name"
    failures=$((failures + 1))
  fi
}

# Dependencies arrive before the agent exists: a separate, agent-free step with network access
# fetches from the lockfile. Fetching runs no package code.
podman run -d --name "$prefetch" --stop-timeout=1 "$image" sleep 600 >/dev/null
podman exec "$prefetch" mkdir /work
podman cp "$here/project/." "$prefetch:/work"
podman exec -w /work "$prefetch" cargo fetch --locked 2>&1 | tail -1
podman exec "$prefetch" tar -C /usr/local/cargo -cf /tmp/registry.tar registry
podman cp "$prefetch:/tmp/registry.tar" "$work/registry.tar"
podman rm -f "$prefetch" >/dev/null

# The sandbox has no network, no mounts, and no capabilities, and runs as an unprivileged user.
# Everything enters and leaves through copy calls, before start or while running.
stage="$work/stage"
mkdir -p "$stage/.cargo"
cp -r "$here/project/." "$stage/"
tar -C "$stage/.cargo" -xf "$work/registry.tar"
podman create --name "$sandbox" --network=none --cap-drop=all --security-opt=no-new-privileges \
  --user 1000:1000 -e CARGO_HOME=/work/.cargo -w /work "$image" sleep 600 >/dev/null
podman cp "$stage" "$sandbox:/work"
podman start "$sandbox" >/dev/null

# A stand-in for host credentials, so the check needs no real one.
fake_home="$work/home"
mkdir -p "$fake_home/.claude" "$work/canary" "$work/cwd"
echo 'canary-credential' > "$fake_home/.claude/.credentials.json"
printf 'A MARKER FROM THE OPERATOR MEMORY\n' > "$fake_home/.claude/CLAUDE.md"
printf 'A MARKER FROM THE WORKING DIRECTORY\n' > "$work/cwd/CLAUDE.md"

sed -e "s#\$CANARY_DIR#$work/canary#" -e "s#\$HOST_CREDENTIALS#$fake_home/.claude/.credentials.json#" \
  "$here/script.json" > "$work/script.json"
python3 -I "$here/stub_model.py" "$work/script.json" "$work/requests.jsonl" "$port" &
stub_pid=$!
sleep 1

cat > "$work/mcp.json" <<JSON
{"mcpServers": {"sandbox": {"command": "python3", "args": ["-I", "$here/sandbox_mcp.py", "$sandbox"],
  "env": {"HOME": "$HOME", "XDG_RUNTIME_DIR": "${XDG_RUNTIME_DIR:-/run/user/$(id -u)}", "PATH": "$PATH"}}}}
JSON

(
  cd "$work/cwd"
  env -i PATH="$PATH" HOME="$fake_home" CANARY_DIR="$work/canary" \
    ANTHROPIC_BASE_URL="http://127.0.0.1:$port" ANTHROPIC_API_KEY=stub \
    claude -p "Fix the build." ${HARNESS_FLAGS:---bare} --tools "" --strict-mcp-config --mcp-config "$work/mcp.json" \
    --allowedTools mcp__sandbox__exec mcp__sandbox__write_file \
    --no-session-persistence --output-format json --max-turns 12 > "$work/result.json" 2> "$work/stderr.txt" || true
)

python3 -I - "$work" > "$work/facts.txt" <<'PY'
import json, sys
work = sys.argv[1]
requests = [json.loads(l) for l in open(f"{work}/requests.jsonl")]
messages = [r for r in requests if r["path"].startswith("/v1/messages") and "count_tokens" not in r["path"]]
last = messages[-1]["body"]
tools = sorted({t.get("name", "") for r in messages for t in r["body"].get("tools", [])})
print("requests", len(messages))
print("tools", json.dumps(tools))
text = json.dumps([r["body"] for r in messages])
print("operator_memory_leaked", "OPERATOR MEMORY" in text)
print("cwd_memory_leaked", "WORKING DIRECTORY" in text)
results = []
for message in last["messages"]:
    if isinstance(message["content"], list):
        for block in message["content"]:
            if block.get("type") == "tool_result":
                content = block.get("content")
                results.append(content if isinstance(content, str) else " ".join(c.get("text", "") for c in content))
for i, r in enumerate(results):
    print(f"result{i}", json.dumps(r[:400]))
PY
cat "$work/facts.txt"
echo "--- checks"

tools="$(grep '^tools ' "$work/facts.txt")"
all="$(cat "$work/facts.txt")"
[[ "$tools" == 'tools ["mcp__sandbox__exec", "mcp__sandbox__write_file"]' ]] && r=yes || r=no
check "the model is offered only sandbox tools" "$r"
[[ ! -e "$work/canary/host-tool-ran" ]] && r=yes || r=no
check "a scripted call to a host tool did nothing" "$r"
grep -q 'operator_memory_leaked False' <<<"$all" && grep -q 'cwd_memory_leaked False' <<<"$all" && r=yes || r=no
check "no operator memory or project file reached the model" "$r"
grep -q 'No such file' <<<"$all" && ! grep -q 'canary-credential' <<<"$all" && r=yes || r=no
check "host credentials are unreadable from the sandbox" "$r"
grep -q 'wget-exit=[1-9]' <<<"$all" && grep -q 'model-exit=[1-9]' <<<"$all" && r=yes || r=no
check "the sandbox reaches neither the internet nor the model" "$r"
grep -q 'Finished' <<<"$all" && grep -q '42' <<<"$all" && r=yes || r=no
check "the agent edited and built code offline" "$r"
podman cp "$sandbox:/work/src/main.rs" "$work/main.rs"
grep -q 'format(42)' "$work/main.rs" && r=yes || r=no
check "the edit can be read back out of the sandbox" "$r"

exit "$failures"
