// Package stub is a stand-in for the Anthropic Messages API. It replays
// scripted turns and records every request, so a spec sees exactly which tools
// and text reach the model, with no credential and no spend. Point the CLI at
// it with claude.Config.BaseURL.
//
// It answers POST /v1/messages with a streamed message
// (https://docs.anthropic.com/en/api/messages-streaming) and
// POST /v1/messages/count_tokens with a count of one.
package stub

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
)

// Turn is one model turn: a call to a tool, or closing text.
type Turn struct {
	tool  string
	input any
	text  string
}

// Tool is a turn that calls the tool name with input, which is marshalled as
// JSON.
func Tool(name string, input any) Turn { return Turn{tool: name, input: input} }

// Text is a turn that ends the conversation with text.
func Text(text string) Turn { return Turn{text: text} }

// Model is a running stub. Its methods are safe for concurrent use.
type Model struct {
	// URL is the stub's base URL, for ANTHROPIC_BASE_URL.
	URL string

	server *http.Server

	mu            sync.Mutex
	turns         []Turn
	responder     func(request json.RawMessage) Turn
	tokensPerTurn int
	requests      []json.RawMessage
	credentials   []string
	released      chan struct{}
}

// Start serves turns in order, then answers "{}" to every later request. Each
// turn reports one output token.
func Start(turns ...Turn) (*Model, error) { return Costing(1, turns...) }

// Costing is [Start] with each turn reporting tokensPerTurn output tokens.
func Costing(tokensPerTurn int, turns ...Turn) (*Model, error) {
	return serve(&Model{turns: slices.Clone(turns), tokensPerTurn: tokensPerTurn})
}

// Answering answers each request by calling responder with its body, however
// sessions interleave. The stub holds its lock while responder runs, so
// responder must not call the Model's methods.
func Answering(responder func(request json.RawMessage) Turn) (*Model, error) {
	return serve(&Model{responder: responder, tokensPerTurn: 1})
}

func serve(m *Model) (*Model, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("stub model: cannot listen: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/messages", m.messages)
	mux.HandleFunc("POST /v1/messages/count_tokens", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens":1}`))
	})
	m.URL = "http://" + listener.Addr().String()
	m.server = &http.Server{Handler: mux}
	go func() { _ = m.server.Serve(listener) }()
	return m, nil
}

// Close stops the stub.
func (m *Model) Close() error { return m.server.Close() }

// Hold leaves every answer waiting until [Model.Release], so a spec can act
// during a run.
func (m *Model) Hold() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.released == nil {
		m.released = make(chan struct{})
	}
}

// Release sends every answer held since [Model.Hold].
func (m *Model) Release() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.released != nil {
		close(m.released)
		m.released = nil
	}
}

// Requests is the body of every request, in order.
func (m *Model) Requests() []json.RawMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.requests)
}

// Credentials is how requests authenticated, as "header: value", sorted, with
// no repeats.
func (m *Model) Credentials() []string {
	m.mu.Lock()
	seen := slices.Clone(m.credentials)
	m.mu.Unlock()
	slices.Sort(seen)
	return slices.Compact(seen)
}

// ToolsOffered is the names of the tools offered across every request,
// sorted, with no repeats.
func (m *Model) ToolsOffered() []string {
	names := []string{}
	for _, raw := range m.Requests() {
		var request struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if json.Unmarshal(raw, &request) == nil {
			for _, tool := range request.Tools {
				names = append(names, tool.Name)
			}
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// ToolResults is the content of every tool result in the last request, which
// holds the whole conversation, as JSON text.
func (m *Model) ToolResults() []string {
	requests := m.Requests()
	if len(requests) == 0 {
		return nil
	}
	var last struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(requests[len(requests)-1], &last) != nil {
		return nil
	}
	results := []string{}
	for _, message := range last.Messages {
		var blocks []struct {
			Type    string          `json:"type"`
			Content json.RawMessage `json:"content"`
		}
		// A message's content is a string or a list of blocks.
		if json.Unmarshal(message.Content, &blocks) != nil {
			continue
		}
		for _, block := range blocks {
			if block.Type == "tool_result" {
				results = append(results, string(block.Content))
			}
		}
	}
	return results
}

func (m *Model) messages(w http.ResponseWriter, r *http.Request) {
	var body json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "not JSON", http.StatusBadRequest)
		return
	}
	turn, tokens, number, held := m.next(body, r.Header)
	if held != nil {
		select {
		case <-held:
		case <-r.Context().Done():
			return
		}
	}
	var request struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &request)
	if request.Model == "" {
		request.Model = "stub"
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.Write([]byte(stream(turn, request.Model, tokens, number)))
}

// next records a request and picks its answer.
func (m *Model) next(body json.RawMessage, header http.Header) (turn Turn, tokens, number int, held chan struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, body)
	for _, name := range []string{"authorization", "x-api-key"} {
		if value := header.Get(name); value != "" {
			m.credentials = append(m.credentials, name+": "+value)
		}
	}
	switch {
	case m.responder != nil:
		turn = m.responder(body)
	case len(m.turns) > 0:
		turn, m.turns = m.turns[0], m.turns[1:]
	default:
		turn = Text("{}")
	}
	return turn, m.tokensPerTurn, len(m.requests), m.released
}

// stream is a turn as the server-sent events of one streamed message.
func stream(turn Turn, model string, tokens, number int) string {
	usage := map[string]int{"input_tokens": 1, "output_tokens": tokens}
	var block, delta map[string]any
	stop := "end_turn"
	if turn.tool != "" {
		input, err := json.Marshal(turn.input)
		if err != nil {
			input = []byte("{}")
		}
		block = map[string]any{"type": "tool_use", "id": fmt.Sprintf("toolu_%d", number), "name": turn.tool, "input": map[string]any{}}
		delta = map[string]any{"type": "input_json_delta", "partial_json": string(input)}
		stop = "tool_use"
	} else {
		block = map[string]any{"type": "text", "text": ""}
		delta = map[string]any{"type": "text_delta", "text": turn.text}
	}
	message := map[string]any{
		"id": fmt.Sprintf("msg_stub_%d", number), "type": "message", "role": "assistant", "model": model, "content": []any{},
		"stop_reason": nil, "stop_sequence": nil, "usage": usage,
	}
	var out strings.Builder
	event := func(name string, data any) {
		encoded, _ := json.Marshal(data)
		fmt.Fprintf(&out, "event: %s\ndata: %s\n\n", name, encoded)
	}
	event("message_start", map[string]any{"type": "message_start", "message": message})
	event("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": block})
	event("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": delta})
	event("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	event("message_delta", map[string]any{
		"type": "message_delta", "delta": map[string]any{"stop_reason": stop, "stop_sequence": nil}, "usage": usage,
	})
	event("message_stop", map[string]any{"type": "message_stop"})
	return out.String()
}
