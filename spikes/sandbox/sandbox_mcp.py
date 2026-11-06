#!/usr/bin/env python3
"""The agent's only hands: an MCP server whose tools run inside one sandbox. It speaks through
`podman exec` here. Against Daytona the same three tools would call its process and file APIs."""
import json
import subprocess
import sys

container = sys.argv[1]
TOOLS = [
    {"name": "exec", "description": "Run a shell command in the sandbox.",
     "inputSchema": {"type": "object", "properties": {"command": {"type": "string"}}, "required": ["command"]}},
    {"name": "write_file", "description": "Write a file in the sandbox.",
     "inputSchema": {"type": "object", "properties": {"path": {"type": "string"}, "content": {"type": "string"}},
                     "required": ["path", "content"]}},
]


def run(args, stdin=None):
    done = subprocess.run(["podman", "exec", "-i", "-w", "/work", container, *args], input=stdin,
                          capture_output=True, text=True, timeout=300)
    return f"exit {done.returncode}\n{done.stdout}{done.stderr}"


def call(name, arguments):
    if name == "exec":
        return run(["sh", "-c", arguments["command"]])
    if name == "write_file":
        return run(["sh", "-c", 'cat > "$1"', "sh", arguments["path"]], arguments["content"])
    return f"unknown tool {name}"


for line in sys.stdin:
    message = json.loads(line)
    method, ident = message.get("method"), message.get("id")
    if ident is None:
        continue
    if method == "initialize":
        result = {"protocolVersion": message["params"]["protocolVersion"], "capabilities": {"tools": {}},
                  "serverInfo": {"name": "sandbox", "version": "0"}}
    elif method == "tools/list":
        result = {"tools": TOOLS}
    elif method == "tools/call":
        text = call(message["params"]["name"], message["params"].get("arguments", {}))
        result = {"content": [{"type": "text", "text": text}]}
    else:
        result = {}
    print(json.dumps({"jsonrpc": "2.0", "id": ident, "result": result}), flush=True)
