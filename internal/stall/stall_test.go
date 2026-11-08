package stall_test

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/internal/stall"
	"github.com/ivy/almanac/internal/stall/stalltest"
)

var _ = Describe("A stall-limited client", func() {
	// The limits of the Rust clients' http_client, in crates/github/src/client.rs,
	// crates/registry/src/http.rs and crates/advisory/src/cve_now.rs.
	It("gives up after 10s to connect or 30s without a byte", func() {
		Expect(stall.ConnectTimeout).To(Equal(10 * time.Second))
		Expect(stall.ReadTimeout).To(Equal(30 * time.Second))
	})

	It("fails a request to a peer that stops answering", func(ctx SpecContext) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, stalltest.Silent(), http.NoBody)
		Expect(err).NotTo(HaveOccurred())
		_, err = stall.After(100 * time.Millisecond).Do(request)
		var timeout net.Error
		Expect(errors.As(err, &timeout)).To(BeTrue(), "%v", err)
		Expect(timeout.Timeout()).To(BeTrue())
	}, SpecTimeout(10*time.Second))

	It("lets a slow download land while it keeps moving", func(ctx SpecContext) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			for range 10 {
				_, _ = w.Write([]byte("x"))
				w.(http.Flusher).Flush()
				time.Sleep(50 * time.Millisecond)
			}
		}))
		DeferCleanup(server.Close)
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, http.NoBody)
		Expect(err).NotTo(HaveOccurred())
		response, err := stall.After(200 * time.Millisecond).Do(request)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(response.Body.Close)
		body, err := io.ReadAll(response.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(body)).To(Equal("xxxxxxxxxx"), "half a second in all, four times the stall limit")
	}, SpecTimeout(10*time.Second))
})
