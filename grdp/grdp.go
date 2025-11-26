//grdp/grdp.go
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

type Client struct {
	Host string
	tpkt *tpkt.TPKT
	x224 *x224.X224
	mcs  *t125.MCSClient
	sec  *sec.Client
	pdu  *pdu.Client
	conn net.Conn // ADD THIS - store the underlying connection
	mu   sync.Mutex // ADD THIS - for thread-safe closing
	closed bool // ADD THIS - track if already closed
}

func NewClient(host string, logLevel glog.LEVEL) *Client {
	glog.SetLevel(logLevel)
	return &Client{Host: host}
}

func (g *Client) Close() error {
    g.mu.Lock()
    defer g.mu.Unlock()
    
    if g.closed {
        return nil
    }
    
    g.closed = true
    
    var lastErr error
    
    // Force close underlying connection FIRST (most important!)
    if g.conn != nil {
        // Set immediate deadline to force any blocking reads/writes to fail
        g.conn.SetDeadline(time.Now().Add(50 * time.Millisecond))
        
        if err := g.conn.Close(); err != nil {
            glog.Debug("Error closing connection:", err)
            lastErr = err
        }
        g.conn = nil
    }
    
    // Then close TPKT layer (might fail, that's ok)
    if g.tpkt != nil {
        if err := g.tpkt.Close(); err != nil {
            glog.Debug("Error closing TPKT:", err)
        }
        g.tpkt = nil
    }
    
    // Clear all references to help GC
    g.x224 = nil
    g.mcs = nil
    g.sec = nil
    g.pdu = nil
    
    return lastErr
}

func (g *Client) LoginForSSL(domain, user, pwd string) error {
	// Reduced timeout for faster failure detection
	conn, err := net.DialTimeout("tcp", g.Host, 5*time.Second)
	if err != nil {
		return fmt.Errorf("connection failed: %v", err)
	}
	g.conn = conn // STORE the connection for later closing
	
	// Set read/write deadlines for faster timeout
	conn.SetDeadline(time.Now().Add(10 * time.Second))

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

	err = g.x224.Connect()
	if err != nil {
	    g.Close()
		return fmt.Errorf("x224 connect failed: %v", err)
	}

	// Use channel with timeout instead of WaitGroup for faster response
	done := make(chan error, 1)
	
	g.pdu.On("error", func(e error) {
		select {
		case done <- e:
		default:
		}
	})
	g.pdu.On("close", func() {
		select {
		case done <- errors.New("connection closed"):
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

	// Wait with timeout
	select {
	case err = <-done:
	    if err != nil {
			g.Close() // Clean up on error
		}
		return err
	case <-time.After(4 * time.Second):
	    g.Close()
		return errors.New("authentication timeout")
	}
}

func (g *Client) LoginForRDP(domain, user, pwd string) error {
	// Reduced timeout for faster failure detection
	conn, err := net.DialTimeout("tcp", g.Host, 3*time.Second)
	if err != nil {
		return fmt.Errorf("connection failed: %v", err)
	}
	g.conn = conn // STORE the connection for later closing

	// Set read/write deadlines
	conn.SetDeadline(time.Now().Add(5 * time.Second))

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

	g.x224.SetRequestedProtocol(x224.PROTOCOL_RDP)

	err = g.x224.Connect()
	if err != nil {
	    g.Close()
		return fmt.Errorf("x224 connect failed: %v", err)
	}

	wg := &sync.WaitGroup{}
	breakFlag := false
	updateCount := 0
	wg.Add(1)

	g.pdu.On("error", func(e error) {
		err = e
		g.pdu.Emit("done")
	})
	g.pdu.On("close", func() {
		err = errors.New("connection closed")
		g.pdu.Emit("done")
	})
	g.pdu.On("success", func() {
		err = nil
		g.pdu.Emit("done")
	})
	g.pdu.On("update", func(rectangles []pdu.BitmapData) {
		updateCount++
	})
	g.pdu.On("done", func() {
		if !breakFlag {
			breakFlag = true
			wg.Done()
		}
	})

	// Reduced wait time
	time.Sleep(3 * time.Second)
	if !breakFlag {
		breakFlag = true
		wg.Done()
	}
	wg.Wait()

    if err != nil {
			g.Close() // Clean up on error
    }

	if updateCount > 50 {
		return nil
	}
	return errors.New("authentication failed")
}

func Login(target, domain, username, password string) error {
	g := NewClient(target, glog.NONE)
	defer g.Close()
	err := g.LoginForSSL(domain, username, password)
	if err == nil {
		return nil
	}
	
	err = g.LoginForRDP(domain, username, password)
	if err == nil {
		return nil
	}
	return err
}