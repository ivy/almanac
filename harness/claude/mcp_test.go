package claude_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/harness/claude"
	"github.com/ivy/almanac/sandbox"
	"github.com/ivy/almanac/sandbox/fake"
)

var _ = Describe("The sandbox's MCP endpoint", func() {
	var (
		sb            *fake.Sandbox
		id            sandbox.ID
		url, bearer   string
		exitCode      int
		printed       string
		authorization string
	)

	BeforeEach(func(ctx SpecContext) {
		exitCode, printed = 0, "out\n"
		sb = fake.Answering(func(string) sandbox.Output {
			return sandbox.Output{ExitCode: exitCode, Stdout: printed, Stderr: "err\n"}
		})
		var err error
		id, err = sb.Create(ctx, sandbox.AgentSpec("image"))
		Expect(err).NotTo(HaveOccurred())
		var stop func() error
		url, bearer, stop, err = claude.ServeTools(sb, id, time.Minute)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(stop)
		authorization = bearer
	})

	post := func(ctx context.Context, body any) (int, map[string]any) {
		encoded, err := json.Marshal(body)
		Expect(err).NotTo(HaveOccurred())
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
		Expect(err).NotTo(HaveOccurred())
		request.Header.Set("Content-Type", "application/json")
		if authorization != "" {
			request.Header.Set("Authorization", authorization)
		}
		response, err := http.DefaultClient.Do(request)
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = response.Body.Close() }()
		raw, err := io.ReadAll(response.Body)
		Expect(err).NotTo(HaveOccurred())
		var answer map[string]any
		_ = json.Unmarshal(raw, &answer)
		return response.StatusCode, answer
	}

	call := func(ctx context.Context, name string, arguments map[string]any) map[string]any {
		status, answer := post(ctx, map[string]any{
			"jsonrpc": "2.0", "id": 7, "method": "tools/call",
			"params": map[string]any{"name": name, "arguments": arguments},
		})
		Expect(status).To(Equal(http.StatusOK))
		Expect(answer["id"]).To(BeEquivalentTo(7))
		return answer["result"].(map[string]any)
	}

	It("answers no one without its token", func(ctx SpecContext) {
		for _, wrong := range []string{"", "Bearer wrong", bearer + "x"} {
			authorization = wrong
			status, _ := post(ctx, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
			Expect(status).To(Equal(http.StatusUnauthorized))
		}
		authorization = bearer
		_ = call(ctx, "exec", map[string]any{"command": "true"})
		Expect(sb.Commands()).To(HaveLen(1), "only the authorized call ran")
	})

	It("offers exactly exec, read_file and write_file", func(ctx SpecContext) {
		status, answer := post(ctx, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
		Expect(status).To(Equal(http.StatusOK))
		var names []string
		for _, tool := range answer["result"].(map[string]any)["tools"].([]any) {
			names = append(names, tool.(map[string]any)["name"].(string))
		}
		Expect(names).To(Equal([]string{"exec", "read_file", "write_file"}))
	})

	It("offers tools and nothing else when initialized", func(ctx SpecContext) {
		_, answer := post(ctx, map[string]any{
			"jsonrpc": "2.0", "id": 0, "method": "initialize",
			"params": map[string]any{"protocolVersion": "2025-06-18"},
		})
		result := answer["result"].(map[string]any)
		Expect(result["protocolVersion"]).To(Equal("2025-06-18"))
		Expect(result["capabilities"]).To(Equal(map[string]any{"tools": map[string]any{}}))
	})

	It("accepts a notification without answering it", func(ctx SpecContext) {
		status, _ := post(ctx, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
		Expect(status).To(Equal(http.StatusAccepted))
	})

	It("runs exec in the sandbox and shows its exit code and output", func(ctx SpecContext) {
		result := call(ctx, "exec", map[string]any{"command": "cargo build"})
		Expect(sb.Calls()).To(Equal([]fake.Command{{Sandbox: id, Line: "cargo build"}}))
		Expect(result["isError"]).To(BeFalse())
		Expect(result["content"]).To(Equal([]any{map[string]any{"type": "text", "text": "exit 0\nout\nerr\n"}}))
	})

	It("marks a command that failed as an error", func(ctx SpecContext) {
		exitCode = 1
		result := call(ctx, "exec", map[string]any{"command": "false"})
		Expect(result["isError"]).To(BeTrue())
	})

	It("quotes paths and content for the sandbox's shell", func(ctx SpecContext) {
		call(ctx, "read_file", map[string]any{"path": "it's.txt"})
		call(ctx, "write_file", map[string]any{"path": "a b/c", "content": "x'; rm -rf /; '"})
		call(ctx, "read_file", map[string]any{"path": "/etc/passwd"})
		Expect(sb.Commands()).To(Equal([]string{
			`cat -- '/work/it'\''s.txt'`,
			`mkdir -p "$(dirname -- '/work/a b/c')" && printf %s 'x'\''; rm -rf /; '\''' > '/work/a b/c'`,
			`cat -- '/etc/passwd'`,
		}))
	})

	It("refuses a tool it does not have", func(ctx SpecContext) {
		result := call(ctx, "Bash", map[string]any{"command": "true"})
		Expect(result["isError"]).To(BeTrue())
		Expect(sb.Commands()).To(BeEmpty())
	})
})
