package simforge_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/simforge"
)

// Every field the forge returns must be one the pinned spec declares, with the declared type,
// so a client tested against it reads nothing GitHub would not send.
var _ = Describe("Shapes", func() {
	var (
		sim    *simforge.Forge
		number int
		sha    string
	)

	BeforeEach(func() {
		sim = simforge.Start()
		DeferCleanup(sim.Close)
		sim.AddRepo(simforge.PublicRepo(repo))
		p := simforge.RenovatePull(branch, "Update lodash")
		p.Labels = []string{"dependencies"}
		number = sim.AddPull(repo, p)
		sha = sim.Pull(repo, number).HeadSHA
		sim.AddSuite(repo, simforge.FinishedSuite(sha, branch, "failure", "boom"))
	})

	It("answers reads as the spec declares", func(ctx SpecContext) {
		call(http.MethodPost, sim.URL()+"/repos/"+repo+"/statuses/"+sha, map[string]any{"state": "pending", "context": "almanac/trust"})
		call(http.MethodPost, sim.URL()+"/repos/"+repo+"/issues", map[string]any{"title": "t", "body": "b", "labels": []string{"almanac"}})
		sim.PullRequest(ctx, repo, number, "opened")

		reads := map[string]string{
			"/users/{username}/repos":                              "/users/owner/repos",
			"/user/repos":                                          "/user/repos",
			"/installation/repositories":                           "/installation/repositories",
			"/repos/{owner}/{repo}":                                "/repos/" + repo,
			"/repos/{owner}/{repo}/pulls":                          "/repos/" + repo + "/pulls",
			"/repos/{owner}/{repo}/pulls/{pull_number}":            fmt.Sprintf("/repos/%s/pulls/%d", repo, number),
			"/repos/{owner}/{repo}/pulls/{pull_number}/commits":    fmt.Sprintf("/repos/%s/pulls/%d/commits", repo, number),
			"/repos/{owner}/{repo}/commits/{ref}/check-suites":     "/repos/" + repo + "/commits/" + sha + "/check-suites",
			"/repos/{owner}/{repo}/commits/{ref}/check-runs":       "/repos/" + repo + "/commits/" + sha + "/check-runs",
			"/repos/{owner}/{repo}/commits/{ref}/status":           "/repos/" + repo + "/commits/" + sha + "/status",
			"/repos/{owner}/{repo}/issues":                         "/repos/" + repo + "/issues",
			"/repos/{owner}/{repo}/issues/{issue_number}/comments": "/repos/" + repo + "/issues/1/comments",
			"/app/hook/deliveries":                                 "/app/hook/deliveries",
		}
		var found []string
		for template, path := range reads {
			schema := responseSchema(template, "get", "200")
			found = append(found, problems(schema, getJSON(sim, path), template)...)
		}
		Expect(found).To(BeEmpty())
	})

	It("answers writes as the spec declares", func() {
		writes := []struct {
			template, path string
			body           map[string]any
		}{
			{"/repos/{owner}/{repo}/statuses/{sha}", "/statuses/" + sha, map[string]any{"state": "success", "context": "c"}},
			{"/repos/{owner}/{repo}/git/blobs", "/git/blobs", map[string]any{"content": "x"}},
			{"/repos/{owner}/{repo}/git/trees", "/git/trees", map[string]any{"tree": []any{}}},
			{"/repos/{owner}/{repo}/git/commits", "/git/commits", map[string]any{"message": "m", "tree": "a"}},
			{"/repos/{owner}/{repo}/issues", "/issues", map[string]any{"title": "t"}},
			{"/repos/{owner}/{repo}/issues/{issue_number}/comments", "/issues/1/comments", map[string]any{"body": "b"}},
			{"/repos/{owner}/{repo}/issues/{issue_number}/assignees", "/issues/1/assignees", map[string]any{"assignees": []string{"a"}}},
		}
		var found []string
		for _, w := range writes {
			response, body := call(http.MethodPost, sim.URL()+"/repos/"+repo+w.path, w.body)
			Expect(response.StatusCode).To(Equal(http.StatusCreated), w.template)
			found = append(found, problems(responseSchema(w.template, "post", "201"), decoded(body), w.template)...)
		}
		Expect(found).To(BeEmpty())
	})

	It("emits webhooks as the spec declares", func(ctx SpecContext) {
		hook := newReceiver()
		sim.DeliverTo(hook.url, "s")
		suite := sim.AddSuite(repo, simforge.FinishedSuite(sha, branch, "success", ""))
		emitted := []string{"pull-request-opened", "pull-request-edited", "pull-request-labeled", "pull-request-synchronize", "check-suite-completed", "status"}
		for _, action := range []string{"opened", "edited", "labeled", "synchronize"} {
			sim.PullRequest(ctx, repo, number, action)
		}
		sim.SuiteCompleted(ctx, repo, suite)
		sim.StatusChanged(ctx, repo, sha, "ci/build", "failure")

		var found []string
		for i, webhook := range emitted {
			schema := lookup(githubSpec(), "x-webhooks", webhook, "post", "requestBody", "content", "application/json", "schema")
			Expect(schema).NotTo(BeNil(), "webhook %s is not in the spec", webhook)
			found = append(found, problems(schema, hook.payload(i), webhook)...)
		}
		Expect(found).To(BeEmpty())
	})
})

// githubSpec is the pinned OpenAPI description, read once for the suite.
var githubSpec = sync.OnceValue(func() map[string]any {
	text, err := os.ReadFile("../specs/github/api.github.com.json")
	Expect(err).NotTo(HaveOccurred())
	var spec map[string]any
	Expect(json.Unmarshal(text, &spec)).To(Succeed())
	return spec
})

func responseSchema(template, method, status string) any {
	schema := lookup(githubSpec(), "paths", template, method, "responses", status, "content", "application/json", "schema")
	Expect(schema).NotTo(BeNil(), "%s %s %s is not in the spec", method, template, status)
	return schema
}

func lookup(value any, keys ...string) any {
	for _, key := range keys {
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = object[key]
	}
	return value
}

// resolve follows a schema's $ref, a JSON pointer into the spec.
func resolve(schema any) map[string]any {
	object, _ := schema.(map[string]any)
	pointer, ok := object["$ref"].(string)
	if !ok {
		return object
	}
	var keys []string
	for _, key := range strings.Split(strings.TrimPrefix(pointer, "#/"), "/") {
		keys = append(keys, strings.NewReplacer("~1", "/", "~0", "~").Replace(key))
	}
	return resolve(lookup(githubSpec(), keys...))
}

func typeMatches(declared, value any) bool {
	one := func(t any) bool {
		switch t {
		case "string":
			_, ok := value.(string)
			return ok
		case "integer":
			n, ok := value.(float64)
			return ok && n == float64(int64(n))
		case "number":
			_, ok := value.(float64)
			return ok
		case "boolean":
			_, ok := value.(bool)
			return ok
		case "object":
			_, ok := value.(map[string]any)
			return ok
		case "array":
			_, ok := value.([]any)
			return ok
		case "null":
			return value == nil
		}
		return false
	}
	switch t := declared.(type) {
	case string:
		return one(t)
	case []any:
		for _, each := range t {
			if one(each) {
				return true
			}
		}
		return false
	}
	return true
}

// problems lists where value departs from schema, each with its path. Empty means it conforms.
func problems(schema, value any, at string) []string {
	s := resolve(schema)
	for _, key := range []string{"anyOf", "oneOf"} {
		options, ok := s[key].([]any)
		if !ok {
			continue
		}
		var best []string
		for i, option := range options {
			found := problems(option, value, at)
			if len(found) == 0 {
				return nil
			}
			if i == 0 || len(found) < len(best) {
				best = found
			}
		}
		return best
	}
	if parts, ok := s["allOf"].([]any); ok {
		merged := map[string]any{}
		for _, part := range parts {
			properties, _ := resolve(part)["properties"].(map[string]any)
			for name, declared := range properties {
				merged[name] = declared
			}
		}
		return problems(map[string]any{"type": "object", "properties": merged}, value, at)
	}
	declared, typed := s["type"]
	if value == nil {
		if nullable, _ := s["nullable"].(bool); nullable || !typed || typeMatches(declared, value) {
			return nil
		}
		return []string{at + " is null but not nullable"}
	}
	if typed && !typeMatches(declared, value) {
		return []string{fmt.Sprintf("%s is %v but the spec says %v", at, value, declared)}
	}
	var found []string
	if fields, ok := value.(map[string]any); ok {
		if properties, ok := s["properties"].(map[string]any); ok {
			for name, field := range fields {
				declared, ok := properties[name]
				if !ok {
					found = append(found, fmt.Sprintf("%s.%s is not in the spec", at, name))
					continue
				}
				found = append(found, problems(declared, field, at+"."+name)...)
			}
		}
	}
	if items, ok := value.([]any); ok {
		if declared, ok := s["items"]; ok {
			for i, item := range items {
				found = append(found, problems(declared, item, fmt.Sprintf("%s[%d]", at, i))...)
			}
		}
	}
	return found
}
