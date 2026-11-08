package broker_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/broker"
	"github.com/ivy/almanac/deliveries"
	"github.com/ivy/almanac/simforge"
)

const secret = "it's a secret"

var body = []byte(`{"action": "opened", "number": 1}`)

// sign is simforge's signature, which shares no code with the broker's check.
func sign(body []byte) string { return simforge.Sign(secret, body) }

// memoryLog is a Log that keeps deliveries in memory, deduplicated by ID.
type memoryLog struct {
	mu       sync.Mutex
	logged   []deliveries.Delivery
	notReady error
	stall    bool
}

func (m *memoryLog) Append(ctx context.Context, d deliveries.Delivery) (bool, error) {
	if m.stall {
		<-ctx.Done()
		return false, ctx.Err()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, l := range m.logged {
		if l.DeliveryID == d.DeliveryID {
			return false, nil
		}
	}
	m.logged = append(m.logged, d)
	return true, nil
}

func (m *memoryLog) Ready(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.notReady
}

func (m *memoryLog) breakDown(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.notReady = err
}

func (m *memoryLog) all() []deliveries.Delivery {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]deliveries.Delivery(nil), m.logged...)
}

// serving starts a broker over log, and returns its URL.
func serving(log broker.Log, logs io.Writer) string {
	logger := slog.New(slog.NewTextHandler(logs, nil))
	server := httptest.NewServer(broker.New(broker.NewGitHub(secret), log, logger))
	DeferCleanup(server.Close)
	return server.URL
}

// post sends a pull_request delivery with an ID and an optional signature.
func post(url string, body []byte, signature, id string) int {
	GinkgoHelper()
	request, err := http.NewRequest(http.MethodPost, url+"/webhooks/github", bytes.NewReader(body))
	Expect(err).NotTo(HaveOccurred())
	request.Header.Set("X-GitHub-Event", "pull_request")
	request.Header.Set("X-GitHub-Delivery", id)
	if signature != "" {
		request.Header.Set("X-Hub-Signature-256", signature)
	}
	response, err := http.DefaultClient.Do(request)
	Expect(err).NotTo(HaveOccurred())
	Expect(response.Body.Close()).To(Succeed())
	return response.StatusCode
}

func get(url string) int {
	GinkgoHelper()
	response, err := http.Get(url)
	Expect(err).NotTo(HaveOccurred())
	Expect(response.Body.Close()).To(Succeed())
	return response.StatusCode
}

// accept runs the GitHub source on headers and a body signed with the secret.
func accept(event string, body []byte) (deliveries.Delivery, error) {
	header := http.Header{}
	header.Set("X-GitHub-Event", event)
	header.Set("X-GitHub-Delivery", "d1")
	header.Set("X-Hub-Signature-256", sign(body))
	return broker.NewGitHub(secret).Accept(header, body)
}

var _ = Describe("Receiving a webhook", func() {
	var (
		log *memoryLog
		url string
	)

	BeforeEach(func() {
		log = &memoryLog{}
		url = serving(log, GinkgoWriter)
	})

	It("a signed delivery is logged with its routing kind and exact body", func() {
		Expect(post(url, body, sign(body), "d1")).To(Equal(http.StatusAccepted))
		logged := log.all()
		Expect(logged).To(HaveLen(1))
		Expect(logged[0].Kind).To(Equal("github.pull_request.opened"))
		Expect(logged[0].Source).To(Equal("github"))
		Expect(logged[0].DeliveryID).To(Equal("d1"))
		Expect(logged[0].Body).To(Equal(body))
	})

	It("a forged webhook gets 401 and nothing is persisted", func() {
		forged := "sha256=" + strings.Repeat("0", 64)
		Expect(post(url, body, forged, "d1")).To(Equal(http.StatusUnauthorized))
		Expect(post(url, body, "", "d2")).To(Equal(http.StatusUnauthorized))
		Expect(post(url, body, "garbage", "d3")).To(Equal(http.StatusUnauthorized))
		Expect(post(url, body, simforge.Sign("another secret", body), "d4")).To(Equal(http.StatusUnauthorized))
		Expect(log.all()).To(BeEmpty())
	})

	It("a tampered body fails verification", func() {
		tampered := []byte(`{"action": "closed", "number": 1}`)
		Expect(post(url, tampered, sign(body), "d1")).To(Equal(http.StatusUnauthorized))
		Expect(log.all()).To(BeEmpty())
	})

	It("an oversized payload is refused", func() {
		oversized := bytes.Repeat([]byte(" "), broker.MaxBody+1)
		Expect(post(url, oversized, sign(oversized), "d1")).To(Equal(http.StatusRequestEntityTooLarge))
		Expect(log.all()).To(BeEmpty())
	})

	It("a redelivered duplicate is acknowledged and not appended", func() {
		Expect(post(url, body, sign(body), "d1")).To(Equal(http.StatusAccepted))
		Expect(post(url, body, sign(body), "d1")).To(Equal(http.StatusAccepted))
		Expect(log.all()).To(HaveLen(1))
	})

	It("a delivery id that could poison a log is refused", func() {
		Expect(post(url, body, sign(body), "bad id")).To(Equal(http.StatusBadRequest))
		Expect(post(url, body, sign(body), strings.Repeat("a", 129))).To(Equal(http.StatusBadRequest))
		Expect(post(url, body, sign(body), "")).To(Equal(http.StatusBadRequest))
		Expect(log.all()).To(BeEmpty())
	})

	It("health needs nothing and readiness asks the log", func() {
		Expect(get(url + "/healthz")).To(Equal(http.StatusOK))
		Expect(get(url + "/readyz")).To(Equal(http.StatusOK))
		log.breakDown(errors.New("the database is down"))
		Expect(get(url + "/healthz")).To(Equal(http.StatusOK))
		Expect(get(url + "/readyz")).To(Equal(http.StatusServiceUnavailable))
	})

	It("a log that does not answer gets 503 well within GitHub's ten seconds", func() {
		stalled := &memoryLog{stall: true}
		logger := slog.New(slog.NewTextHandler(GinkgoWriter, nil))
		handler := broker.New(broker.NewGitHub(secret), stalled, logger).WithAppendTimeout(50 * time.Millisecond)
		server := httptest.NewServer(handler)
		DeferCleanup(server.Close)
		started := time.Now()
		Expect(post(server.URL, body, sign(body), "d1")).To(Equal(http.StatusServiceUnavailable))
		Expect(time.Since(started)).To(BeNumerically("<", 5*time.Second))
		Expect(broker.AppendTimeout).To(BeNumerically("<", 10*time.Second))
	})
})

var _ = Describe("Reading a GitHub delivery", func() {
	It("an event with no action routes by event alone", func() {
		delivery, err := accept("status", []byte(`{"state": "failure"}`))
		Expect(err).NotTo(HaveOccurred())
		Expect(delivery.Kind).To(Equal("github.status"))
	})

	It("a hostile action cannot shape the kind", func() {
		for _, payload := range []string{
			`{"action": "opened.evil kind"}`,
			`{"action": 7}`,
			`{"action": ""}`,
			`["opened"]`,
		} {
			delivery, err := accept("pull_request", []byte(payload))
			Expect(err).NotTo(HaveOccurred(), payload)
			Expect(delivery.Kind).To(Equal("github.pull_request"), payload)
		}
	})

	It("a signed body that is not JSON is refused", func() {
		_, err := accept("status", []byte("not json"))
		Expect(err).To(Equal(broker.Malformed))
	})

	It("a signature failure is logged without the payload", func() {
		var captured syncBuffer
		url := serving(&memoryLog{}, &captured)
		hostile := []byte("HOSTILE PAYLOAD TEXT")
		forged := "sha256=" + strings.Repeat("0", 64)
		Expect(post(url, hostile, forged, "d1")).To(Equal(http.StatusUnauthorized))
		Expect(captured.String()).To(ContainSubstring("refused a delivery"))
		Expect(captured.String()).NotTo(ContainSubstring("HOSTILE"))
	})
})

var _ = Describe("The broker's dependencies", func() {
	It("the broker depends on no workflow or agent code", func() {
		// Every package of this module the broker's binary links, which is the
		// log, diagnostics and their pages. The depguard rule in .golangci.yml
		// refuses an import of workflow, runtime or engine at lint time; this
		// refuses anything else that would make the broker know about workflows.
		out, err := exec.Command("go", "list", "-deps", "github.com/ivy/almanac/cmd/almanac-broker").Output()
		Expect(err).NotTo(HaveOccurred())
		var ours []string
		for _, pkg := range strings.Fields(string(out)) {
			if strings.HasPrefix(pkg, "github.com/ivy/almanac") {
				ours = append(ours, pkg)
			}
		}
		Expect(ours).To(ConsistOf(
			"github.com/ivy/almanac/broker",
			"github.com/ivy/almanac/deliveries",
			"github.com/ivy/almanac/diag",
			"github.com/ivy/almanac/docs",
			"github.com/ivy/almanac/cmd/almanac-broker",
		))
	})
})

// syncBuffer is a bytes.Buffer that the server's goroutines may write to.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
