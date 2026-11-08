package simforge_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/simforge"
)

const repo = "owner/repo"

// scripted is a forge with one repository and one open Renovate pull request.
func scripted() (*simforge.Forge, int) {
	sim := simforge.Start()
	DeferCleanup(sim.Close)
	sim.AddRepo(simforge.PublicRepo(repo))
	number := sim.AddPull(repo, simforge.RenovatePull("renovate/lodash-4.x", "Update lodash"))
	return sim, number
}

// call sends a request with a token and returns the response with its body read.
func call(method, url string, body any) (*http.Response, []byte) {
	var sent io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		Expect(err).NotTo(HaveOccurred())
		sent = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, url, sent)
	Expect(err).NotTo(HaveOccurred())
	request.Header.Set("Authorization", "Bearer token")
	response, err := http.DefaultClient.Do(request)
	Expect(err).NotTo(HaveOccurred())
	read, err := io.ReadAll(response.Body)
	Expect(err).NotTo(HaveOccurred())
	Expect(response.Body.Close()).To(Succeed())
	return response, read
}

// getJSON reads a path that must answer 200, decoded.
func getJSON(sim *simforge.Forge, path string) any {
	response, body := call(http.MethodGet, sim.URL()+path, nil)
	Expect(response.StatusCode).To(Equal(http.StatusOK), string(body))
	return decoded(body)
}

func decoded(body []byte) any {
	var value any
	Expect(json.Unmarshal(body, &value)).To(Succeed(), string(body))
	return value
}

// at walks decoded JSON by object keys and array indexes.
func at(value any, path ...any) any {
	for _, step := range path {
		switch key := step.(type) {
		case string:
			Expect(value).To(BeAssignableToTypeOf(map[string]any{}), "at %v", path)
			value = value.(map[string]any)[key]
		case int:
			Expect(value).To(BeAssignableToTypeOf([]any{}), "at %v", path)
			value = value.([]any)[key]
		}
	}
	return value
}

// received is one webhook as a receiver saw it.
type received struct {
	event, guid, signature string
	body                   []byte
}

// receiver records every webhook posted to it and answers with status.
type receiver struct {
	url string

	mu     sync.Mutex
	status int
	log    []received
}

func newReceiver() *receiver {
	r := &receiver{status: http.StatusOK}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		defer GinkgoRecover()
		body, err := io.ReadAll(req.Body)
		Expect(err).NotTo(HaveOccurred())
		r.mu.Lock()
		defer r.mu.Unlock()
		r.log = append(r.log, received{
			event:     req.Header.Get("X-GitHub-Event"),
			guid:      req.Header.Get("X-GitHub-Delivery"),
			signature: req.Header.Get("X-Hub-Signature-256"),
			body:      body,
		})
		w.WriteHeader(r.status)
	}))
	DeferCleanup(server.Close)
	r.url = server.URL + "/hook"
	return r
}

func (r *receiver) all() []received {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]received(nil), r.log...)
}

func (r *receiver) guids() []string {
	var guids []string
	for _, d := range r.all() {
		guids = append(guids, d.guid)
	}
	return guids
}

func (r *receiver) payload(i int) any {
	return decoded(r.all()[i].body)
}

// verified is the check a broker makes before it trusts a delivery: the HMAC-SHA256 in
// X-Hub-Signature-256, compared in constant time with one computed over the exact body.
// It shares no code with simforge.Sign, so it proves the signature rather than echoing it.
func verified(secret, body []byte, header string) bool {
	digest, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	claimed, err := hex.DecodeString(digest)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(claimed, mac.Sum(nil))
}

// nextLink is the URL a Link header gives for the next page, or empty on the last page.
func nextLink(response *http.Response) string {
	for _, part := range strings.Split(response.Header.Get("Link"), ", ") {
		if target, ok := strings.CutSuffix(part, `>; rel="next"`); ok {
			return strings.TrimPrefix(target, "<")
		}
	}
	return ""
}
