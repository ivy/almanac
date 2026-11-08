package stub_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/harness/claude/stub"
)

var _ = Describe("The stub model", func() {
	post := func(ctx context.Context, model *stub.Model, body string) string {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, model.URL+"/v1/messages", strings.NewReader(body))
		Expect(err).NotTo(HaveOccurred())
		request.Header.Set("x-api-key", "stub-key")
		response, err := http.DefaultClient.Do(request)
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = response.Body.Close() }()
		Expect(response.Header.Get("Content-Type")).To(Equal("text/event-stream"))
		raw, err := io.ReadAll(response.Body)
		Expect(err).NotTo(HaveOccurred())
		return string(raw)
	}

	start := func(turns ...stub.Turn) *stub.Model {
		model, err := stub.Start(turns...)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(model.Close)
		return model
	}

	It("replays its turns in order, then answers {}", func(ctx SpecContext) {
		model := start(stub.Tool("Bash", map[string]string{"command": "ls"}), stub.Text("done"))
		first := post(ctx, model, `{"model": "m"}`)
		Expect(first).To(ContainSubstring(`"name":"Bash"`))
		Expect(first).To(ContainSubstring(`"partial_json":"{\"command\":\"ls\"}"`))
		Expect(first).To(ContainSubstring(`"stop_reason":"tool_use"`))
		Expect(post(ctx, model, `{}`)).To(ContainSubstring(`"text":"done"`))
		Expect(post(ctx, model, `{}`)).To(ContainSubstring(`"text":"{}"`))
	})

	It("records every request, how it authenticated, and the tools it offered", func(ctx SpecContext) {
		model := start()
		post(ctx, model, `{"tools": [{"name": "b"}, {"name": "a"}]}`)
		post(ctx, model, `{"tools": [{"name": "a"}], "messages": [
			{"role": "user", "content": "hi"},
			{"role": "user", "content": [{"type": "tool_result", "content": "exit 0"}]}]}`)
		Expect(model.Requests()).To(HaveLen(2))
		Expect(model.Credentials()).To(Equal([]string{"x-api-key: stub-key"}))
		Expect(model.ToolsOffered()).To(Equal([]string{"a", "b"}))
		Expect(model.ToolResults()).To(Equal([]string{`"exit 0"`}))
	})

	It("holds its answers until released", func(ctx SpecContext) {
		model := start(stub.Text("held"))
		model.Hold()
		answered := make(chan string, 1)
		go func() {
			defer GinkgoRecover()
			answered <- post(ctx, model, `{}`)
		}()
		Eventually(model.Requests).Should(HaveLen(1))
		Consistently(answered, 100*time.Millisecond).ShouldNot(Receive())
		model.Release()
		Eventually(answered).Should(Receive(ContainSubstring("held")))
	})

	It("answers from the request when given a responder", func(ctx SpecContext) {
		model, err := stub.Answering(func(request json.RawMessage) stub.Turn {
			if strings.Contains(string(request), "second") {
				return stub.Text("two")
			}
			return stub.Text("one")
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(model.Close)
		Expect(post(ctx, model, `{"n": "second"}`)).To(ContainSubstring(`"text":"two"`))
		Expect(post(ctx, model, `{"n": "first"}`)).To(ContainSubstring(`"text":"one"`))
	})
})
