#!/usr/bin/env python3
"""A local stand-in for the Messages API. It replays a script of tool calls and records every
request, so a run shows exactly which tools and text reach the model, with no credential and
no spend."""
import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

script_path, log_path, port = sys.argv[1], sys.argv[2], int(sys.argv[3])
steps = json.load(open(script_path))
state = {"next": 0}


def reply_blocks():
    """The next scripted turn: a tool call, or text when the script is finished."""
    if state["next"] < len(steps):
        step = steps[state["next"]]
        state["next"] += 1
        block = {"type": "tool_use", "id": f"toolu_{state['next']}", "name": step["tool"], "input": step["input"]}
        return [block], "tool_use"
    return [{"type": "text", "text": "finished"}], "end_turn"


def events(blocks, stop_reason, model):
    message = {"id": "msg_stub", "type": "message", "role": "assistant", "model": model, "content": [],
               "stop_reason": None, "stop_sequence": None, "usage": {"input_tokens": 1, "output_tokens": 1}}
    yield "message_start", {"type": "message_start", "message": message}
    for i, block in enumerate(blocks):
        if block["type"] == "tool_use":
            start = dict(block, input={})
            delta = {"type": "input_json_delta", "partial_json": json.dumps(block["input"])}
        else:
            start = {"type": "text", "text": ""}
            delta = {"type": "text_delta", "text": block["text"]}
        yield "content_block_start", {"type": "content_block_start", "index": i, "content_block": start}
        yield "content_block_delta", {"type": "content_block_delta", "index": i, "delta": delta}
        yield "content_block_stop", {"type": "content_block_stop", "index": i}
    yield "message_delta", {"type": "message_delta", "delta": {"stop_reason": stop_reason, "stop_sequence": None},
                            "usage": {"output_tokens": 1}}
    yield "message_stop", {"type": "message_stop"}


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_POST(self):
        length = int(self.headers.get("content-length", 0))
        body = json.loads(self.rfile.read(length) or b"{}")
        with open(log_path, "a") as log:
            log.write(json.dumps({"path": self.path, "body": body}) + "\n")
        if not self.path.startswith("/v1/messages") or "count_tokens" in self.path:
            self.send_response(200)
            self.send_header("content-type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"input_tokens": 1}')
            return
        blocks, stop_reason = reply_blocks()
        model = body.get("model", "stub")
        self.send_response(200)
        if body.get("stream"):
            self.send_header("content-type", "text/event-stream")
            self.end_headers()
            for name, data in events(blocks, stop_reason, model):
                self.wfile.write(f"event: {name}\ndata: {json.dumps(data)}\n\n".encode())
            return
        self.send_header("content-type", "application/json")
        self.end_headers()
        message = {"id": "msg_stub", "type": "message", "role": "assistant", "model": model, "content": blocks,
                   "stop_reason": stop_reason, "stop_sequence": None, "usage": {"input_tokens": 1, "output_tokens": 1}}
        self.wfile.write(json.dumps(message).encode())

    def do_GET(self):
        self.send_response(404)
        self.end_headers()


ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()
