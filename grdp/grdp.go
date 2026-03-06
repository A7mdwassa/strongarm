// grdp/grdp.go - Optimized RDP client with improved timeout and connection handling

package grdp

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/whiterabb17/strongarm/grdp/core"
	"github.com/whiterabb17/strongarm/grdp/glog"
	"github.com/whiterabb17/strongarm/grdp/protocol/nla"
	"github.com/whiterabb17/strongarm/grdp/protocol/pdu"
	"github.com/whiterabb17/strongarm/grdp/protocol/sec"
	"github.com/whiterabb17/strongarm/grdp/protocol/t125"
	"github.com/whiterabb17/strongarm/grdp/protocol/tpkt"
	"github.com/whiterabb17/strongarm/grdp/protocol/x224"
)

// Client represents an RDP client connection
type Client struct {
	Host   string
	tpkt   *tpkt.TPKT
	x224   *x224.X224
	mcs    *t125.MCSClient
	sec    *sec.Client
	pdu    *pdu.Client
	conn   net.Conn
	mu     sync.Mutex
	closed bool
}

// NewClient creates a new RDP client
func NewClient(host string, logLevel glog.LEVEL) *Client {
	glog.SetLevel(logLevel)
	return &Client{Host: host}
}

// Close cleanly closes all client connections and resources
func (g *Client) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.closed {
		return nil
	}

	g.closed = true

	// Close underlying connection with proper cleanup
	if g.conn != nil {
		// Set immediate deadline to force any blocking reads/writes to fail
		g.conn.SetDeadline(time.Now())

		// Try to set TCP keepalive to 0 (disable) and linger options
		if tcpConn, ok := g.conn.(*net.TCPConn); ok {
			// Disable keepalive to speed up close
			tcpConn.SetKeepAlive(false)
			// Set linger to 0 for immediate close (forces RST instead of FIN)
			// This releases port immediately but is less graceful
			tcpConn.SetLinger(0)
		}

		g.conn.Close()
		g.conn = nil
	}

	// Clear protocol layers
	if g.tpkt != nil {
		g.tpkt.Close()
		g.tpkt = nil
	}

	// Nil out references for GC
	g.x224 = nil
	g.mcs = nil
	g.sec = nil
	g.pdu = nil

	return nil
}

// isClosed checks if the client is already closed (must be called with lock held)
func (g *Client) isClosed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.closed
}

// LoginForSSL attempts authentication using SSL/TLS (NLA)
// Returns nil on success, error on failure
func (g *Client) LoginForSSL(domain, user, pwd string) error {
	// Quick connection timeout for speed
	conn, err := net.DialTimeout("tcp", g.Host, 3*time.Second)
	if err != nil {
		return fmt.Errorf("connection failed: %v", err)
	}

	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		conn.Close()
		return errors.New("client closed")
	}
	g.conn = conn
	g.mu.Unlock()

	// Set aggressive timeouts for faster failure detection
	conn.SetDeadline(time.Now().Add(8 * time.Second))

	// Initialize protocol stack
	g.tpkt = tpkt.New(core.NewSocketLayer(conn), nla.NewNTLMv2(domain, user, pwd))
	g.x224 = x224.New(g.tpkt)
	g.mcs = t125.NewMCSClient(g.x224)
	g.sec = sec.NewClient(g.mcs)
	g.pdu = pdu.NewClient(g.sec)

	g.sec.SetUser(user)
	g.sec.SetPwd(pwd)
	g.sec.SetDomain(domain)

	g.tpkt.SetFastPathListener(g.sec)
	g.sec.SetFastPathListener(g.pdu)
	g.pdu.SetFastPathSender(g.tpkt)

	// Connect X224 layer
	if err := g.x224.Connect(); err != nil {
		g.Close()
		return fmt.Errorf("x224 connect failed: %v", err)
	}

	// Wait for authentication result with timeout
	done := make(chan error, 1)
	var resultErr error

	g.pdu.On("error", func(e error) {
		resultErr = e
		select {
		case done <- e:
		default:
		}
	})

	g.pdu.On("close", func() {
		resultErr = errors.New("connection closed")
		select {
		case done <- resultErr:
		default:
		}
	})

	g.pdu.On("success", func() {
		select {
		case done <- nil:
		default:
		}
	})

	g.pdu.On("ready", func() {
		select {
		case done <- nil:
		default:
		}
	})

	// Wait for result or timeout
	select {
	case err := <-done:
		if err != nil {
			g.Close()
		}
		return err
	case <-time.After(5 * time.Second):
		g.Close()
		return errors.New("authentication timeout")
	}
}

// LoginForRDP attempts authentication using standard RDP protocol (fallback)
// Returns nil on success, error on failure
// NOTE: NoNLA authentication is unreliable - we use multiple heuristics
func (g *Client) LoginForRDP(domain, user, pwd string) error {
	// Quick connection timeout
	conn, err := net.DialTimeout("tcp", g.Host, 2*time.Second)
	if err != nil {
		return fmt.Errorf("connection failed: %v", err)
	}

	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		conn.Close()
		return errors.New("client closed")
	}
	g.conn = conn
	g.mu.Unlock()

	// Set aggressive timeouts
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	// Initialize protocol stack
	g.tpkt = tpkt.New(core.NewSocketLayer(conn), nla.NewNTLMv2(domain, user, pwd))
	g.x224 = x224.New(g.tpkt)
	g.mcs = t125.NewMCSClient(g.x224)
	g.sec = sec.NewClient(g.mcs)
	g.pdu = pdu.NewClient(g.sec)

	g.sec.SetUser(user)
	g.sec.SetPwd(pwd)
	g.sec.SetDomain(domain)

	g.tpkt.SetFastPathListener(g.sec)
	g.sec.SetFastPathListener(g.pdu)
	g.pdu.SetFastPathSender(g.tpkt)

	// Use standard RDP protocol (not NLA)
	g.x224.SetRequestedProtocol(x224.PROTOCOL_RDP)

	// Connect X224 layer
	if err := g.x224.Connect(); err != nil {
		g.Close()
		return fmt.Errorf("x224 connect failed: %v", err)
	}

	// Wait for authentication result with multiple indicators
	done := make(chan struct{})
	var authErr error
	var mu sync.Mutex

	// Track multiple success indicators
	var receivedReady bool
	var receivedSuccess bool
	var receivedError bool
	var updateCount int
	var deactivated bool

	g.pdu.On("error", func(e error) {
		mu.Lock()
		authErr = e
		receivedError = true
		mu.Unlock()
		close(done)
	})

	g.pdu.On("close", func() {
		mu.Lock()
		authErr = errors.New("connection closed")
		mu.Unlock()
		close(done)
	})

	g.pdu.On("success", func() {
		mu.Lock()
		receivedSuccess = true
		mu.Unlock()
		// Don't close - wait for ready or more events
	})

	g.pdu.On("ready", func() {
		mu.Lock()
		receivedReady = true
		mu.Unlock()
		close(done)
	})

	g.pdu.On("update", func(rectangles []pdu.BitmapData) {
		mu.Lock()
		updateCount++
		mu.Unlock()
	})

	g.pdu.On("deactivate", func() {
		mu.Lock()
		deactivated = true
		mu.Unlock()
		close(done)
	})

	// Wait for completion or timeout
	select {
	case <-done:
		// Event triggered
	case <-time.After(3 * time.Second):
		// Timeout
	}

	mu.Lock()
	finalReady := receivedReady
	finalSuccess := receivedSuccess
	finalError := receivedError
	finalUpdates := updateCount
	finalDeactivated := deactivated
	mu.Unlock()

	// Close connection
	g.Close()

	// Analyze results with stricter criteria
	// A deactivation after connection usually means login screen (not authenticated)
	if finalDeactivated {
		return errors.New("session deactivated (likely login screen only)")
	}

	// If we got an error, authentication definitely failed
	if finalError {
		return authErr
	}

	// For NoNLA, "ready" is the strongest indicator of successful authentication
	// Screen updates alone are NOT reliable (login screen generates updates)
	if finalReady {
		return nil
	}

	// If we only got updates without ready, it's likely just the login screen
	if finalUpdates > 0 && !finalReady {
		return errors.New("received updates but no ready state (likely login screen)")
	}

	// If we got success but no ready, be conservative
	if finalSuccess && !finalReady {
		return errors.New("success event without ready state")
	}

	return errors.New("authentication failed - no positive confirmation")
}

// Login attempts SSL authentication first, then falls back to RDP
func Login(target, domain, username, password string) error {
	g := NewClient(target, glog.NONE)
	defer g.Close()

	// Try SSL/NLA first (more common in modern environments)
	if err := g.LoginForSSL(domain, username, password); err == nil {
		return nil
	}

	// Fallback to standard RDP
	if err := g.LoginForRDP(domain, username, password); err == nil {
		return nil
	}

	return errors.New("all authentication methods failed")
}
