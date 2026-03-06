// honeypot.go - RDP Honeypot Detection

package main

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/whiterabb17/strongarm/grdp"
	"github.com/whiterabb17/strongarm/grdp/glog"
)

// HoneypotDetector tracks honeypot detection results
type HoneypotDetector struct {
	mu        sync.RWMutex
	honeypots map[string]bool // target -> is honeypot
	tested    int64
	detected  int64
}

// NewHoneypotDetector creates a new honeypot detector
func NewHoneypotDetector() *HoneypotDetector {
	return &HoneypotDetector{
		honeypots: make(map[string]bool),
	}
}

// IsHoneypot checks if a target is marked as a honeypot
func (h *HoneypotDetector) IsHoneypot(target string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.honeypots[target]
}

// MarkHoneypot marks a target as a honeypot
func (h *HoneypotDetector) MarkHoneypot(target string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.honeypots[target] = true
	atomic.AddInt64(&h.detected, 1)
}

// MarkClean marks a target as verified clean (not a honeypot)
func (h *HoneypotDetector) MarkClean(target string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	// Only mark if not already tested
	if _, exists := h.honeypots[target]; !exists {
		h.honeypots[target] = false
	}
	atomic.AddInt64(&h.tested, 1)
}

// GetStats returns detection statistics
func (h *HoneypotDetector) GetStats() (tested, detected int64) {
	return atomic.LoadInt64(&h.tested), atomic.LoadInt64(&h.detected)
}

// GetAllHoneypots returns all detected honeypot targets
func (h *HoneypotDetector) GetAllHoneypots() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var honeypots []string
	for target, isHoneypot := range h.honeypots {
		if isHoneypot {
			honeypots = append(honeypots, target)
		}
	}
	return honeypots
}

// SaveHoneypots saves detected honeypots to file
func (h *HoneypotDetector) SaveHoneypots() error {
	h.mu.RLock()
	defer h.mu.RUnlock()

	file, err := createFile("honeypots.txt")
	if err != nil {
		return err
	}
	defer file.Close()

	for target := range h.honeypots {
		if h.honeypots[target] {
			file.WriteString(target + "\n")
		}
	}

	return nil
}

// CheckHoneypot tests if a target is a honeypot using fake credentials
// Returns: true if honeypot, false if legitimate, error if test failed
func CheckHoneypot(target string) (bool, error) {
	// Obviously fake credentials that should NEVER work on real systems
	fakeUser := "HONEYPOT_TEST_USER"
	fakePass := "HONEYPOT_TEST_PASSWORD_12345"

	client := grdp.NewClient(target, glog.NONE)
	defer client.Close()

	// Set short timeout for honeypot test
	done := make(chan bool, 1)
	var loginSuccess bool

	go func() {
		err := client.LoginForSSL(".", fakeUser, fakePass)
		if err != nil {
			err = client.LoginForRDP(".", fakeUser, fakePass)
		}
		loginSuccess = (err == nil)
		done <- true
	}()

	select {
	case <-done:
		// If fake credentials succeeded, it's definitely a honeypot
		return loginSuccess, nil
	case <-time.After(10 * time.Second):
		// Timeout - can't determine, assume legitimate
		return false, fmt.Errorf("honeypot test timeout")
	}
}

// TestTargetForHoneypot tests a single target and updates detector
func TestTargetForHoneypot(target string, detector *HoneypotDetector) {
	isHoneypot, err := CheckHoneypot(target)
	if err != nil {
		if verbose {
			logVerbose("Honeypot test inconclusive for %s: %v", target, err)
		}
		return
	}

	if isHoneypot {
		detector.MarkHoneypot(target)
		if verbose {
			logVerbose("🚨 HONEYPOT DETECTED: %s", target)
		}
	} else {
		detector.MarkClean(target)
	}
}

// RunHoneypotScan scans all targets for honeypots before brute-forcing
func RunHoneypotScan(targets []string, threadCount int) *HoneypotDetector {
	if len(targets) == 0 {
		return NewHoneypotDetector()
	}

	if threadCount <= 0 {
		threadCount = 50 // Moderate concurrency for honeypot testing
	}

	detector := NewHoneypotDetector()
	var wg sync.WaitGroup
	sem := make(chan struct{}, threadCount)

	fmt.Println()
	fmt.Println("================================================")
	fmt.Println("         HONEYPOT DETECTION SCAN                ")
	fmt.Println("================================================")
	fmt.Printf("Testing %d targets for honeypot indicators...\n", len(targets))
	fmt.Println()

	progress := int64(0)
	total := int64(len(targets))

	// Progress display
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				current := atomic.LoadInt64(&progress)
				percent := float64(current) / float64(total) * 100
				_, detected := detector.GetStats()
				fmt.Printf("\rHoneypot Scan: [%-50s] %d/%d (%.1f%%) | Honeypots: %d",
					makeProgressbar(percent),
					current, total, percent, detected)
			case <-done:
				return
			}
		}
	}()

	// Start workers
	for _, target := range targets {
		wg.Add(1)
		sem <- struct{}{}

		go func(t string) {
			defer wg.Done()
			defer func() { <-sem }()

			TestTargetForHoneypot(t, detector)
			atomic.AddInt64(&progress, 1)
		}(target)
	}

	wg.Wait()
	close(done)

	fmt.Println()
	fmt.Println()

	_, detected := detector.GetStats()
	fmt.Printf("✅ Honeypot scan complete: %d honeypots detected\n", detected)
	fmt.Println()

	return detector
}

func makeProgressbar(percent float64) string {
	filled := int(percent / 2)
	if filled > 50 {
		filled = 50
	}
	return fmt.Sprintf("%s%s", makeString("=", filled), makeString(" ", 50-filled))
}

func makeString(s string, count int) string {
	result := ""
	for i := 0; i < count; i++ {
		result += s
	}
	return result
}
