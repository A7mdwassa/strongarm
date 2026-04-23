// nla_scanner.go - Integrated NLA scanning for RDP spray

package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// =============================================================================
// TPDU Constants
// =============================================================================
const (
	TPDUConnectionRequest = 224
	TPDUConnectionConfirm = 208
	TPDUData              = 240

	TypeRDPNegReq     = 1
	TypeRDPNegRsp     = 2
	TypeRDPNegFailure = 3

	ProtocolRDP    = 0
	ProtocolSSL    = 1
	ProtocolHybrid = 2

	DefaultNLATimeout = 5 * time.Second
)

// Pre-computed RDP request packet
var rdpRequestPacket = []byte{
	// TPKT Header (4 bytes)
	0x03, 0x00, 0x00, 0x14, // Version 3, Length 20

	// TPDU Header (3 bytes)
	0x0f, 0xe0, 0x00, // LI=15, Code=0xe0, DST-REF high byte

	// CR-TPDU (9 bytes)
	0x00, // DST-REF low byte
	0x00, 0x00, // SRC-REF
	0x00,       // Class
	0x01,       // Type (RDP_NEG_REQ)
	0x00,       // Flags
	0x08, 0x00, // Length
	0x03, 0x00, 0x00, 0x00, // requestedProtocols (HYBRID | SSL)
	0x00, // Padding
}

// =============================================================================
// NLA Status Types
// =============================================================================

// NLAScanStatus represents the result of an NLA scan
type NLAScanStatus byte

const (
	NLAStatusNLA NLAScanStatus = iota // NLA enabled (Hybrid)
	NLAStatusSSL                       // SSL only
	NLAStatusRDP                       // Standard RDP only (NoNLA)
	NLAStatusTimeout
	NLAStatusError
)

func (s NLAScanStatus) String() string {
	switch s {
	case NLAStatusNLA:
		return "NLA"
	case NLAStatusSSL:
		return "SSL"
	case NLAStatusRDP:
		return "RDP"
	case NLAStatusTimeout:
		return "TIMEOUT"
	case NLAStatusError:
		return "ERROR"
	}
	return "UNKNOWN"
}

// NLAResult holds scan result for a single target
type NLAResult struct {
	Target string
	Status NLAScanStatus
	Error  error
}

// NLAScanResults holds all scan results
type NLAScanResults struct {
	mu       sync.RWMutex
	NLA      []string
	SSL      []string
	RDP      []string
	Timeout  []string
	Errors   []string
	Progress int64
	Total    int64
}

// NewNLAScanResults creates a new results container
func NewNLAScanResults() *NLAScanResults {
	return &NLAScanResults{
		NLA:     make([]string, 0),
		SSL:     make([]string, 0),
		RDP:     make([]string, 0),
		Timeout: make([]string, 0),
		Errors:  make([]string, 0),
	}
}

// Add adds a result
func (r *NLAScanResults) Add(target string, status NLAScanStatus, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	switch status {
	case NLAStatusNLA:
		r.NLA = append(r.NLA, target)
	case NLAStatusSSL:
		r.SSL = append(r.SSL, target)
	case NLAStatusRDP:
		r.RDP = append(r.RDP, target)
	case NLAStatusTimeout:
		r.Timeout = append(r.Timeout, target)
	case NLAStatusError:
		r.Errors = append(r.Errors, target)
	}

	atomic.AddInt64(&r.Progress, 1)
}

// GetBruteTargets returns targets that should be brute-forced
// If bruteNLAOnly is true, only NLA targets are returned
// Otherwise, both NLA and RDP targets are returned
func (r *NLAScanResults) GetBruteTargets(bruteNLAOnly bool) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if bruteNLAOnly {
		// Only NLA-enabled targets
		result := make([]string, 0, len(r.NLA)+len(r.SSL))
		result = append(result, r.NLA...)
		result = append(result, r.SSL...)
		return result
	}

	// All reachable targets (NLA + SSL + RDP)
	result := make([]string, 0, len(r.NLA)+len(r.SSL)+len(r.RDP))
	result = append(result, r.NLA...)
	result = append(result, r.SSL...)
	result = append(result, r.RDP...)
	return result
}

// =============================================================================
// NLA Scanner
// =============================================================================

// NLAScanner scans RDP hosts for NLA status
type NLAScanner struct {
	dialer  *net.Dialer
	timeout time.Duration
	retries int
}

// NewNLAScanner creates a new NLA scanner
func NewNLAScanner(timeout time.Duration, retries int) *NLAScanner {
	if retries < 1 {
		retries = 1
	}

	return &NLAScanner{
		dialer: &net.Dialer{
			Timeout:   timeout,
			KeepAlive: 30 * time.Second,
			DualStack: false,
		},
		timeout: timeout,
		retries: retries,
	}
}

// Scan checks the NLA status of a host
func (s *NLAScanner) Scan(target string) NLAScanStatus {
	ts := parseTarget(target)
	host := ts.host
	port := ts.port
	if port == 0 {
		port = 3389
	}

	status, err := s.scanOnce(host, port)
	if err == nil {
		return status
	}

	// Retry with backoff
	for attempt := 2; attempt <= s.retries; attempt++ {
		if !isRetryableError(err) {
			return NLAStatusError
		}
		time.Sleep(time.Duration(attempt*25) * time.Millisecond)
		status, err = s.scanOnce(host, port)
		if err == nil {
			return status
		}
	}

	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		return NLAStatusTimeout
	}

	return NLAStatusError
}

// scanOnce performs a single scan attempt
func (s *NLAScanner) scanOnce(host string, port int) (NLAScanStatus, error) {
	conn, err := s.dialer.Dial("tcp", net.JoinHostPort(host, fmt.Sprintf("%d", port)))
	if err != nil {
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			return NLAStatusTimeout, nil
		}
		return NLAStatusError, err
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(s.timeout))

	_, err = conn.Write(rdpRequestPacket)
	if err != nil {
		return NLAStatusError, err
	}

	buf := make([]byte, 8192)
	n, err := conn.Read(buf)

	if err != nil && err != io.EOF {
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			return NLAStatusTimeout, nil
		}
		return NLAStatusError, err
	}

	if n < 12 {
		return NLAStatusError, errors.New("response too short")
	}

	// Parse TPKT
	if n < 4 {
		return NLAStatusError, errors.New("TPKT too short")
	}
	tpktLen := binary.BigEndian.Uint16(buf[2:4])
	if tpktLen < 4 || int(tpktLen) > n {
		return NLAStatusError, errors.New("incomplete TPKT")
	}
	tpduData := buf[4:tpktLen]

	// Parse TPDU
	if len(tpduData) < 2 {
		return NLAStatusError, errors.New("TPDU too short")
	}
	tpduCode := tpduData[1]

	// Parse CR-TPDU
	if len(tpduData) < 8 {
		return NLAStatusError, errors.New("CR-TPDU too short")
	}
	crTpduType := tpduData[7]

	// Connection Confirm
	if tpduCode == TPDUConnectionConfirm {
		if crTpduType == TypeRDPNegRsp && n >= 19 {
			protocols := binary.LittleEndian.Uint32(buf[15:19])
			if protocols&ProtocolHybrid != 0 {
				return NLAStatusNLA, nil
			}
			if protocols&ProtocolSSL != 0 {
				return NLAStatusSSL, nil
			}
			return NLAStatusRDP, nil
		}
		if crTpduType == TypeRDPNegFailure {
			return NLAStatusRDP, nil
		}
		if crTpduType == TypeRDPNegRsp {
			return NLAStatusNLA, nil
		}
	}

	// Ensure default return path is explicit for clarity and maintainability
	return NLAStatusRDP, nil
}

// isRetryableError checks if an error is worth retrying
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "temporary") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "connection refused")
}

// =============================================================================
// NLA Scan Runner
// =============================================================================

// RunNLAScan performs NLA scanning on targets with progress display
func RunNLAScan(targets []string, threadCount int, timeout time.Duration, retries int) *NLAScanResults {
	if len(targets) == 0 {
		return NewNLAScanResults()
	}

	if threadCount <= 0 {
		threadCount = runtime.NumCPU() * 4
	}
	if threadCount > 500 {
		threadCount = 500
	}

	results := NewNLAScanResults()
	results.Total = int64(len(targets))
	scanner := NewNLAScanner(timeout, retries)

	var wg sync.WaitGroup
	jobs := make(chan string, threadCount*2)

	// Progress display
	progressTicker := time.NewTicker(100 * time.Millisecond)
	defer progressTicker.Stop()
	done := make(chan struct{})

	go func() {
		for {
			select {
			case <-progressTicker.C:
				progress := atomic.LoadInt64(&results.Progress)
				total := results.Total
				percent := float64(progress) / float64(total) * 100
				fmt.Printf("\rScanning NLA: [%-50s] %d/%d (%.1f%%)",
					strings.Repeat("=", int(percent/2))+strings.Repeat(" ", 50-int(percent/2)),
					progress, total, percent)
			case <-done:
				return
			}
		}
	}()

	// Start workers
	for i := 0; i < threadCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for target := range jobs {
				status := scanner.Scan(target)
				results.Add(target, status, nil)
			}
		}()
	}

	// Submit jobs
	for _, target := range targets {
		jobs <- target
	}
	close(jobs)

	// Wait for completion
	wg.Wait()
	close(done)

	fmt.Println()

	return results
}

// PrintNLASummary prints NLA scan results summary
func PrintNLASummary(results *NLAScanResults) {
	results.mu.RLock()
	defer results.mu.RUnlock()

	fmt.Println()
	fmt.Println("==================================================")
	fmt.Println("           NLA SCAN RESULTS SUMMARY              ")
	fmt.Println("==================================================")
	fmt.Printf("  NLA Enabled:     %6d\n", len(results.NLA))
	fmt.Printf("  SSL Only:        %6d\n", len(results.SSL))
	fmt.Printf("  RDP Only (NoNLA):%6d\n", len(results.RDP))
	fmt.Printf("  Timeout:         %6d\n", len(results.Timeout))
	fmt.Printf("  Errors:          %6d\n", len(results.Errors))
	fmt.Println("--------------------------------------------------")
	fmt.Printf("  TOTAL:           %6d\n", len(results.NLA)+len(results.SSL)+len(results.RDP)+len(results.Timeout)+len(results.Errors))
	fmt.Println("==================================================")
	fmt.Println()
}

// SaveNLAResults saves NLA scan results to files
func SaveNLAResults(results *NLAScanResults) error {
	results.mu.RLock()
	defer results.mu.RUnlock()

	files := map[string][]string{
		"NLA.txt":        results.NLA,
		"SSL.txt":        results.SSL,
		"RDP_NoNLA.txt":  results.RDP,
		"NLA_TIMEOUT.txt": results.Timeout,
		"NLA_ERROR.txt":   results.Errors,
	}

	for filename, data := range files {
		if err := writeNLAResultsToFile(filename, data); err != nil {
			return fmt.Errorf("error writing %s: %w", filename, err)
		}
	}

	return nil
}

func writeNLAResultsToFile(filename string, data []string) error {
	if len(data) == 0 {
		f, err := os.Create(filename)
		if err != nil {
			return err
		}
		f.Close()
		return nil
	}

	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()

	writer := bufio.NewWriter(f)
	for _, line := range data {
		writer.WriteString(line)
		writer.WriteByte('\n')
	}

	return writer.Flush()
}
