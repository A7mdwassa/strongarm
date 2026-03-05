// connection_pool.go - Connection monitoring and throttling

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
	throttleWarnings  int64
	globalRateLimit   int64 // Global rate limit counter
	lastGlobalCheck   int64 // Unix timestamp of last rate check
}

var globalMonitor = &ConnectionMonitor{
	startTime: time.Now(),
}

// IncrementActive increments the active connection counter and tracks peak
func (cm *ConnectionMonitor) IncrementActive() {
	current := atomic.AddInt64(&cm.activeConnections, 1)
	atomic.AddInt64(&cm.totalAttempted, 1)

	// Atomically track peak connections
	for {
		peak := atomic.LoadInt64(&cm.peakConnections)
		if current <= peak {
			break
		}
		if atomic.CompareAndSwapInt64(&cm.peakConnections, peak, current) {
			break
		}
	}

	// Update global rate limit counter
	atomic.AddInt64(&cm.globalRateLimit, 1)
}

// DecrementActive decrements the active connection counter
func (cm *ConnectionMonitor) DecrementActive() {
	atomic.AddInt64(&cm.activeConnections, -1)
}

// RecordSuccess increments the success counter
func (cm *ConnectionMonitor) RecordSuccess() {
	atomic.AddInt64(&cm.totalSucceeded, 1)
}

// RecordFailure increments the failure counter
func (cm *ConnectionMonitor) RecordFailure() {
	atomic.AddInt64(&cm.totalFailed, 1)
}

// GetActiveCount returns the current number of active connections
func (cm *ConnectionMonitor) GetActiveCount() int64 {
	return atomic.LoadInt64(&cm.activeConnections)
}

// GetPeakCount returns the peak number of active connections
func (cm *ConnectionMonitor) GetPeakCount() int64 {
	return atomic.LoadInt64(&cm.peakConnections)
}

// ShouldThrottle determines if we should slow down based on active connections
// Windows ephemeral port range is typically 16384-32767 (16384 ports)
// We throttle well before exhaustion to prevent connection failures
func (cm *ConnectionMonitor) ShouldThrottle() bool {
	active := cm.GetActiveCount()

	// Conservative thresholds for Windows
	// Default ephemeral port range: 16384-32767 (16384 ports)
	// Start throttling at 50% utilization
	const THROTTLE_THRESHOLD = 8000

	return active > THROTTLE_THRESHOLD
}

// GetThrottleDelay returns the delay to apply based on connection pressure
func (cm *ConnectionMonitor) GetThrottleDelay() time.Duration {
	active := cm.GetActiveCount()

	// Windows default ephemeral port range: 16384-32767 (16384 ports)
	// Adjust thresholds based on port exhaustion risk
	switch {
	case active > 10000:
		// Critical: 60%+ port utilization - aggressive throttle
		atomic.AddInt64(&cm.throttleWarnings, 1)
		return 2 * time.Second
	case active > 8000:
		// High: 50%+ port utilization
		return 1 * time.Second
	case active > 6000:
		// Medium: 35%+ port utilization
		return 500 * time.Millisecond
	case active > 4000:
		// Light: 25%+ port utilization
		return 200 * time.Millisecond
	default:
		return 0
	}
}

// ShouldApplyGlobalRateLimit checks if we should apply global rate limiting
// This prevents overwhelming target networks with too many attempts per second
func (cm *ConnectionMonitor) ShouldApplyGlobalRateLimit() bool {
	// Rate limit: max 1000 attempts per second globally
	const GLOBAL_RATE_LIMIT = 1000

	currentTime := time.Now().Unix()
	lastCheck := atomic.LoadInt64(&cm.lastGlobalCheck)

	// Reset counter every second
	if currentTime != lastCheck {
		atomic.CompareAndSwapInt64(&cm.lastGlobalCheck, lastCheck, currentTime)
		atomic.StoreInt64(&cm.globalRateLimit, 0)
		return false
	}

	rate := atomic.LoadInt64(&cm.globalRateLimit)
	return rate > GLOBAL_RATE_LIMIT
}

// GetGlobalRateLimitDelay returns the delay to apply for global rate limiting
func (cm *ConnectionMonitor) GetGlobalRateLimitDelay() time.Duration {
	const GLOBAL_RATE_LIMIT = 1000

	rate := atomic.LoadInt64(&cm.globalRateLimit)
	if rate <= GLOBAL_RATE_LIMIT {
		return 0
	}

	// Progressive delay based on how much we exceed the limit
	excess := rate - GLOBAL_RATE_LIMIT
	if excess > 500 {
		return 500 * time.Millisecond
	}
	if excess > 200 {
		return 200 * time.Millisecond
	}
	return 50 * time.Millisecond
}

// GetStats returns a formatted statistics string
func (cm *ConnectionMonitor) GetStats() string {
	active := atomic.LoadInt64(&cm.activeConnections)
	attempted := atomic.LoadInt64(&cm.totalAttempted)
	succeeded := atomic.LoadInt64(&cm.totalSucceeded)
	failed := atomic.LoadInt64(&cm.totalFailed)
	peak := atomic.LoadInt64(&cm.peakConnections)
	warnings := atomic.LoadInt64(&cm.throttleWarnings)

	elapsed := time.Since(cm.startTime).Seconds()
	attemptsPerSec := float64(attempted) / mathMax(1, elapsed)

	successRate := 0.0
	if attempted > 0 {
		successRate = float64(succeeded) / float64(attempted) * 100
	}

	return fmt.Sprintf(`
=== Connection Monitor ===
🔗 Active Connections: %d
📊 Peak Connections:    %d
✅ Successful:          %d
❌ Failed:              %d
📈 Success Rate:        %.2f%%
⚡ Rate:                %.2f/sec
⏱️  Uptime:             %.0fs
⚠️  Throttle Warnings:  %d
========================
`, active, peak, succeeded, failed, successRate, attemptsPerSec, elapsed, warnings)
}

// PrintStats prints current statistics to console
func (cm *ConnectionMonitor) PrintStats() {
	fmt.Println(cm.GetStats())
}

// GetSummary returns a brief summary for monitoring
func (cm *ConnectionMonitor) GetSummary() map[string]int64 {
	return map[string]int64{
		"active":    atomic.LoadInt64(&cm.activeConnections),
		"peak":      atomic.LoadInt64(&cm.peakConnections),
		"attempted": atomic.LoadInt64(&cm.totalAttempted),
		"succeeded": atomic.LoadInt64(&cm.totalSucceeded),
		"failed":    atomic.LoadInt64(&cm.totalFailed),
	}
}

// mathMax returns the maximum of two float64 values
func mathMax(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
