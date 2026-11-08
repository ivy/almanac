// Package stalltest gives specs a peer that stalls, for proving a client gives up on it.
package stalltest

import (
	"net"
	"sync"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// Silent is the URL of a server that accepts connections and never answers, like a service
// that stalled mid-request. It closes when the spec ends.
func Silent() string {
	ginkgo.GinkgoHelper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	var (
		mu   sync.Mutex
		held []net.Conn
	)
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
	ginkgo.DeferCleanup(func() {
		_ = listener.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range held {
			_ = conn.Close()
		}
	})
	return "http://" + listener.Addr().String()
}
