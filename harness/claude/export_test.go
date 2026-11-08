package claude

import (
	"encoding/json"
	"time"

	"github.com/ivy/almanac/sandbox"
)

// Exposed to the specs in claude_test.

// Args is the CLI's command line for a session, with toolsConfig as its
// --mcp-config if it is not empty.
func Args(c Config, prompt, toolsConfig string) []string {
	var tools *toolServer
	if toolsConfig != "" {
		tools = &toolServer{config: toolsConfig}
	}
	return c.args(prompt, tools)
}

// Environ is a session's environment, with home as its HOME.
func Environ(c Config, home string) []string { return c.environ(home) }

// ParseAnswer reads an agent's closing text as JSON.
var ParseAnswer = parseAnswer

// ServeTools starts the MCP endpoint for sandbox id, and returns its URL, the
// Authorization header it wants, and a function that stops it.
func ServeTools(sb sandbox.Sandbox, id sandbox.ID, timeout time.Duration) (url, authorization string, stop func() error, err error) {
	t, err := serveTools(sb, id, timeout)
	if err != nil {
		return "", "", nil, err
	}
	var config struct {
		MCPServers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(t.config), &config); err != nil {
		_ = t.Close()
		return "", "", nil, err
	}
	server := config.MCPServers[serverName]
	return server.URL, server.Headers["Authorization"], t.Close, nil
}
