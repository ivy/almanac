package claude

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/ivy/almanac/sandbox"
)

// The agent's only hands: an MCP endpoint on localhost whose tools are sandbox
// calls. It lives in this process, so it reaches any sandbox.Sandbox, local or
// remote, and the sandbox needs no network. It is part of the trust boundary
// and exposes nothing else.
//
// It speaks the least of MCP's Streamable HTTP transport the CLI needs
// (https://modelcontextprotocol.io/specification/2025-06-18/basic/transports):
// one POST per JSON-RPC message, answered with one JSON response. It keeps no
// sessions and offers no resources, prompts or streams.

// serverName is the MCP server's name, which prefixes each tool's name.
const serverName = "sandbox"

// maxRequestBytes bounds one JSON-RPC request. A write_file call carries a
// whole file.
const maxRequestBytes = 16 << 20

// toolNames are the only tools a sandboxed session has.
var toolNames = []string{"exec", "read_file", "write_file"}

// allowedTools is how the CLI names the sandbox's tools.
func allowedTools() []string {
	names := make([]string, len(toolNames))
	for i, name := range toolNames {
		names[i] = "mcp__" + serverName + "__" + name
	}
	return names
}

// toolServer is a running endpoint for one session's sandbox. config is the
// CLI's --mcp-config for it.
type toolServer struct {
	config string
	server *http.Server
}

// serveTools starts an endpoint whose tools run in sandbox id. Only a request
// bearing its token, which only the CLI's config holds, is answered.
func serveTools(sb sandbox.Sandbox, id sandbox.ID, timeout time.Duration) (*toolServer, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("%w: cannot listen for the sandbox's tools: %w", ErrSandbox, err)
	}
	token := rand.Text()
	handler := &tools{sandbox: sb, id: id, timeout: timeout, authorization: "Bearer " + token}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()

	config, err := json.Marshal(map[string]any{"mcpServers": map[string]any{
		serverName: map[string]any{
			"type":    "http",
			"url":     "http://" + listener.Addr().String() + "/",
			"headers": map[string]string{"Authorization": "Bearer " + token},
		},
	}})
	if err != nil {
		_ = server.Close()
		return nil, err
	}
	return &toolServer{config: string(config), server: server}, nil
}

// Close stops the endpoint and cancels any tool call still running.
func (t *toolServer) Close() error { return t.server.Close() }

// tools answers JSON-RPC requests for one sandbox.
type tools struct {
	sandbox       sandbox.Sandbox
	id            sandbox.ID
	timeout       time.Duration
	authorization string
}

type rpcRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		ProtocolVersion json.RawMessage `json:"protocolVersion"`
		Name            string          `json:"name"`
		Arguments       map[string]any  `json:"arguments"`
	} `json:"params"`
}

func (t *tools) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	given := r.Header.Get("Authorization")
	if subtle.ConstantTimeCompare([]byte(given), []byte(t.authorization)) != 1 {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var request rpcRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes)).Decode(&request); err != nil {
		http.Error(w, "not a JSON-RPC request", http.StatusBadRequest)
		return
	}
	// A notification has no id and gets no answer.
	if len(request.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var result any
	switch request.Method {
	case "initialize":
		result = map[string]any{
			"protocolVersion": request.Params.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": serverName, "version": "0"},
		}
	case "tools/list":
		result = map[string]any{"tools": toolList()}
	case "tools/call":
		text, failed := t.call(r.Context(), request.Params.Name, request.Params.Arguments)
		result = map[string]any{
			"content": []map[string]string{{"type": "text", "text": text}},
			"isError": failed,
		}
	default:
		result = map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
}

func toolList() []map[string]any {
	text := map[string]string{"type": "string"}
	tool := func(name, description string, properties map[string]any, required ...string) map[string]any {
		return map[string]any{
			"name":        name,
			"description": description,
			"inputSchema": map[string]any{"type": "object", "properties": properties, "required": required},
		}
	}
	return []map[string]any{
		tool("exec", "Run a shell command in the checkout. There is no network.",
			map[string]any{"command": text}, "command"),
		tool("read_file", "Read a file in the checkout.",
			map[string]any{"path": text}, "path"),
		tool("write_file", "Replace a file in the checkout.",
			map[string]any{"path": text, "content": text}, "path", "content"),
	}
}

// call runs a tool in the sandbox, and returns what the model is shown and
// whether the call failed. Every argument came from the model, so each is
// quoted for the sandbox's shell, and the sandbox is what contains it.
func (t *tools) call(ctx context.Context, name string, arguments map[string]any) (string, bool) {
	text := func(key string) string {
		value, _ := arguments[key].(string)
		return value
	}
	var command string
	switch name {
	case "exec":
		command = text("command")
	case "read_file":
		command = "cat -- " + quoted(inside(text("path")))
	case "write_file":
		path := quoted(inside(text("path")))
		command = fmt.Sprintf(`mkdir -p "$(dirname -- %s)" && printf %%s %s > %s`, path, quoted(text("content")), path)
	default:
		return fmt.Sprintf("unknown tool `%s`", name), true
	}
	out, err := t.sandbox.Exec(ctx, t.id, command, t.timeout)
	if err != nil {
		return err.Error(), true
	}
	shown := fmt.Sprintf("exit %d\n%s%s", out.ExitCode, out.Stdout, out.Stderr)
	if out.TimedOut {
		shown += fmt.Sprintf("\n[killed: the command ran past its %s limit]", t.timeout)
	}
	if out.Truncated {
		shown = "[only the end of the output is shown]\n" + shown
	}
	return shown, out.ExitCode != 0 || out.TimedOut
}

// quoted quotes text for the sandbox's shell.
func quoted(text string) string {
	return "'" + strings.ReplaceAll(text, "'", `'\''`) + "'"
}

// inside is path in the checkout, unless it is absolute.
func inside(path string) string {
	if strings.HasPrefix(path, "/") {
		return path
	}
	return sandbox.Workdir + "/" + path
}
