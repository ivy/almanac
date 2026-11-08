package github_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/github"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/hostile/reveal"
	"github.com/ivy/almanac/simforge"
)

const (
	repo   = "owner/repo"
	branch = "renovate/lodash-4.x"
)

var slug = hostile.MustParseSlug(repo)

// quick retries without waiting long, so a spec of a failure is fast.
func quick() github.Retry {
	return github.Retry{Attempts: 4, Backoff: time.Millisecond, LongestWait: 5 * time.Second}
}

func client(sim *simforge.Forge, options ...github.Option) *github.Client {
	return github.New(sim.URL(), github.Token("token"), append([]github.Option{github.Retrying(quick())}, options...)...)
}

// scripted is a forge with one repository and one open Renovate pull request.
func scripted() (*simforge.Forge, uint64) {
	sim := simforge.Start()
	DeferCleanup(sim.Close)
	sim.AddRepo(simforge.PublicRepo(repo))
	number := sim.AddPull(repo, simforge.RenovatePull(branch, "Update lodash"))
	return sim, uint64(number)
}

// privateKey is an RSA key in PKCS #8 PEM, as `openssl genrsa` writes one, made once.
var privateKey = sync.OnceValues(func() (*rsa.PrivateKey, string) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		panic(err)
	}
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
})

func app() github.App {
	_, encoded := privateKey()
	return github.App{ID: "1", PrivateKey: encoded, Installation: 7}
}

// decoded is hostile JSON read for a spec's assertions.
func decoded(h hostile.Of[json.RawMessage]) map[string]any {
	var value map[string]any
	Expect(json.Unmarshal(reveal.Value(h), &value)).To(Succeed())
	return value
}

func decodedAll(items []hostile.Of[json.RawMessage]) []map[string]any {
	out := make([]map[string]any, len(items))
	for i, item := range items {
		out[i] = decoded(item)
	}
	return out
}

// haveCode matches a *diag.Error with code.
func haveCode(code diag.Code) OmegaMatcher {
	return WithTransform(func(err error) diag.Code {
		var d *diag.Error
		if errors.As(err, &d) {
			return d.Code
		}
		return ""
	}, Equal(code))
}

// receiver counts webhooks posted to it.
type receiver struct {
	url string
	mu  sync.Mutex
	n   int
}

func newReceiver() *receiver {
	r := &receiver{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = io.Copy(io.Discard, req.Body)
		r.mu.Lock()
		defer r.mu.Unlock()
		r.n++
	}))
	DeferCleanup(server.Close)
	r.url = server.URL
	return r
}

func (r *receiver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// silentForge accepts connections and never answers, like a forge that stalled mid-request.
func silentForge() string {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	Expect(err).NotTo(HaveOccurred())
	var held []net.Conn
	var mu sync.Mutex
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, conn)
			mu.Unlock()
		}
	}()
	DeferCleanup(func() {
		_ = listener.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range held {
			_ = conn.Close()
		}
	})
	return "http://" + listener.Addr().String()
}

// recorder is a forge that answers every request with the same status and body, and keeps
// what it was sent, for what simforge cannot script.
type recorder struct {
	mu       sync.Mutex
	requests []*http.Request
}

func newRecorder(status int, body string) (*recorder, string) {
	r := &recorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = io.Copy(io.Discard, req.Body)
		r.mu.Lock()
		r.requests = append(r.requests, req)
		r.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	DeferCleanup(server.Close)
	return r, server.URL
}

func (r *recorder) headers() []http.Header {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []http.Header
	for _, req := range r.requests {
		out = append(out, req.Header)
	}
	return out
}

func (r *recorder) path(i int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requests[i].URL.Path
}
