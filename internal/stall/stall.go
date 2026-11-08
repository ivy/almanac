// Package stall gives the HTTP clients that reach outside services a limit on how long a peer
// may stop answering, so a stalled connection fails its request instead of holding a run, or a
// scheduled scan, forever.
//
// There is no overall deadline. A large download that keeps moving is not stalled, and lands.
package stall

import (
	"context"
	"net"
	"net/http"
	"time"
)

const (
	// ConnectTimeout is how long a connection may take to open.
	ConnectTimeout = 10 * time.Second
	// ReadTimeout is how long a request may wait for its next byte.
	ReadTimeout = 30 * time.Second
)

// Client is an HTTP client that fails a request whose peer takes longer than ConnectTimeout to
// connect, or longer than ReadTimeout to send its next byte.
func Client() *http.Client { return After(ReadTimeout) }

// After is Client with stall in place of ReadTimeout, so a spec of a stalled peer need not wait
// half a minute.
func After(stall time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: ConnectTimeout}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		opened, err := dialer.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &conn{Conn: opened, stall: stall}, nil
	}
	return &http.Client{Transport: transport}
}

// conn fails a read that waits longer than stall.
type conn struct {
	net.Conn
	stall time.Duration
}

func (c *conn) Read(p []byte) (int, error) {
	if err := c.SetReadDeadline(time.Now().Add(c.stall)); err != nil {
		return 0, err
	}
	return c.Conn.Read(p)
}
