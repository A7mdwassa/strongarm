// Create new file: connection_pool.go

package main

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// ConnectionMonitor tracks active connections and system resources
type ConnectionMonitor struct {
	activeConnections int64
	totalAttempted    int64
	totalSucceeded    int64
	totalFailed       int64
	mu                sync.RWMutex
	startTime         time.Time
	peakConnections   int64
}

var globalMonitor = &ConnectionMonitor{
	startTime: time.Now(),
}

// IncrementActive - call when creating a connection
func (cm *ConnectionMonitor) IncrementActive() {
	current := atomic.AddInt64(&cm.activeConnections, 1)
	atomic.AddInt64(&cm.totalAttempted, 1)
	
	// Track peak
	for {
		peak := atomic.LoadInt64(&cm.peakConnections)
		if current <= peak {
			break
		}
		if atomic.CompareAndSwapInt64(&cm.peakConnections, peak, current) {
			break
		}
	}
}

// DecrementActive - call when closing a connection
func (cm *ConnectionMonitor) DecrementActive() {
	atomic.AddInt64(&cm.activeConnections, -1)
}

// RecordSuccess - call on successful login
func (cm *ConnectionMonitor) RecordSuccess() {
	atomic.AddInt64(&cm.totalSucceeded, 1)
}

// RecordFailure - call on failed login
func (cm *ConnectionMonitor) RecordFailure() {
	atomic.AddInt64(&cm.totalFailed, 1)
}

// GetActiveCount returns current active connections
func (cm *ConnectionMonitor) GetActiveCount() int64 {
	return atomic.LoadInt64(&cm.activeConnections)
}

// ShouldThrottle determines if we should slow down based on active connections
func (cm *ConnectionMonitor) ShouldThrottle() bool {
	active := cm.GetActiveCount()
	
	// On Windows, throttle if we have more than 5000 active connections
	// This prevents port exhaustion
	const MAX_SAFE_CONNECTIONS = 5000000
	
	return active > MAX_SAFE_CONNECTIONS
}

// GetThrottleDelay returns how long to wait before creating new connection
func (cm *ConnectionMonitor) GetThrottleDelay() time.Duration {
	active := cm.GetActiveCount()
	
	switch {
	case active > 8000:
		return 500 * time.Millisecond // Severe throttle
	case active > 6000:
		return 200 * time.Millisecond // Heavy throttle
	case active > 5000:
		return 100 * time.Millisecond // Light throttle
	default:
		return 0 // No throttle
	}
}

// GetStats returns formatted statistics
func (cm *ConnectionMonitor) GetStats() string {
	active := atomic.LoadInt64(&cm.activeConnections)
	attempted := atomic.LoadInt64(&cm.totalAttempted)
	succeeded := atomic.LoadInt64(&cm.totalSucceeded)
	failed := atomic.LoadInt64(&cm.totalFailed)
	peak := atomic.LoadInt64(&cm.peakConnections)
	
	elapsed := time.Since(cm.startTime).Seconds()
	attemptsPerSec := float64(attempted) / elapsed
	
	return fmt.Sprintf(`
=== Connection Monitor ===
🔗 Active: %d
📊 Peak: %d
✅ Succeeded: %d
❌ Failed: %d
📈 Rate: %.2f/sec
⏱️ Uptime: %.0fs
========================
`, active, peak, succeeded, failed, attemptsPerSec, elapsed)
}

// PrintStats prints statistics to console
func (cm *ConnectionMonitor) PrintStats() {
	fmt.Println(cm.GetStats())
}

// MonitorLoop continuously monitors and prints stats
func (cm *ConnectionMonitor) MonitorLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	
	for range ticker.C {
		active := cm.GetActiveCount()
		
		// Warning if too many connections
		if active > 7000 {
			fmt.Printf("⚠️  WARNING: %d active connections - system may be overloaded!\n", active)
		}
		
		// Auto-suggest reducing workers if consistently high
		if active > 6000 {
			fmt.Println("💡 TIP: Consider reducing worker count with -w flag")
		}
	}
}

// ConnectionPool manages a pool of reusable connections (optional - more complex)
type ConnectionPool struct {
	mu          sync.Mutex
	connections map[string][]*PooledConnection
	maxPerHost  int
	maxIdle     time.Duration
}

type PooledConnection struct {
	targetString string
	lastUsed     time.Time
	inUse        bool
	client       interface{} // Would be *grdp.Client
}

func NewConnectionPool(maxPerHost int, maxIdle time.Duration) *ConnectionPool {
	pool := &ConnectionPool{
		connections: make(map[string][]*PooledConnection),
		maxPerHost:  maxPerHost,
		maxIdle:     maxIdle,
	}
	
	// Cleanup routine for idle connections
	go pool.cleanupLoop()
	
	return pool
}

// Get retrieves a connection from pool or creates new one
func (p *ConnectionPool) Get(targetString string) *PooledConnection {
	p.mu.Lock()
	defer p.mu.Unlock()
	
	// Try to get existing connection
	if conns, exists := p.connections[targetString]; exists {
		for _, conn := range conns {
			if !conn.inUse && time.Since(conn.lastUsed) < p.maxIdle {
				conn.inUse = true
				conn.lastUsed = time.Now()
				return conn
			}
		}
	}
	
	// No available connection, return nil (caller creates new one)
	return nil
}

// Put returns a connection to the pool
func (p *ConnectionPool) Put(conn *PooledConnection) {
	p.mu.Lock()
	defer p.mu.Unlock()
	
	conn.inUse = false
	conn.lastUsed = time.Now()
	
	// Add to pool if not at max
	if conns, exists := p.connections[conn.targetString]; exists {
		if len(conns) < p.maxPerHost {
			p.connections[conn.targetString] = append(conns, conn)
		}
	} else {
		p.connections[conn.targetString] = []*PooledConnection{conn}
	}
}

// cleanupLoop removes idle connections
func (p *ConnectionPool) cleanupLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	
	for range ticker.C {
		p.mu.Lock()
		for target, conns := range p.connections {
			active := make([]*PooledConnection, 0)
			for _, conn := range conns {
				if !conn.inUse && time.Since(conn.lastUsed) > p.maxIdle {
					// Close idle connection here
					continue
				}
				active = append(active, conn)
			}
			p.connections[target] = active
		}
		p.mu.Unlock()
	}
}

// GetPoolStats returns pool statistics
func (p *ConnectionPool) GetPoolStats() map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	
	stats := make(map[string]int)
	totalPooled := 0
	totalInUse := 0
	
	for _, conns := range p.connections {
		totalPooled += len(conns)
		for _, conn := range conns {
			if conn.inUse {
				totalInUse++
			}
		}
	}
	
	stats["total_pooled"] = totalPooled
	stats["total_in_use"] = totalInUse
	stats["total_idle"] = totalPooled - totalInUse
	
	return stats
}