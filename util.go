// util.go - Utility functions

package main

import (
	"bufio"
	"encoding/gob"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// targetStruct represents a parsed target
type targetStruct struct {
	host   string
	port   int
	scheme string
	url    string
}

// workerState tracks individual worker progress
type workerState struct {
	WorkerId       int
	WorkerProgress int32
}

// task represents a work unit for a worker
type task struct {
	targetsRaw      []string
	target          targetStruct
	usernames       []string
	passwords       []string
	numberOfWorkers int
}

// writeGob serializes and writes an object to a file
func writeGob(filePath string, object interface{}) error {
	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	encoder := gob.NewEncoder(file)
	if err := encoder.Encode(object); err != nil {
		return fmt.Errorf("failed to encode: %w", err)
	}

	return nil
}

// readGob reads and deserializes an object from a file
func readGob(filePath string, object interface{}) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	decoder := gob.NewDecoder(file)
	if err := decoder.Decode(object); err != nil {
		return fmt.Errorf("failed to decode: %w", err)
	}

	return nil
}

// parseTarget parses a target string into a targetStruct
// Supports formats: host, host:port, scheme://host:port/url
func parseTarget(targetString string) targetStruct {
	var target targetStruct
	tempString := targetString

	// Extract scheme if present
	if strings.Contains(targetString, "://") {
		parts := strings.SplitN(targetString, "://", 2)
		target.scheme = parts[0]
		tempString = parts[1]
	}

	// Extract host and port
	if strings.Contains(tempString, ":") {
		parts := strings.SplitN(tempString, ":", 2)
		target.host = parts[0]

		// Handle port with optional path
		if strings.Contains(parts[1], "/") {
			portParts := strings.SplitN(parts[1], "/", 2)
			target.port, _ = strconv.Atoi(portParts[0])
			target.url = portParts[1]
		} else {
			target.port, _ = strconv.Atoi(parts[1])
		}
	} else {
		// No port specified
		if strings.Contains(tempString, "/") {
			parts := strings.SplitN(tempString, "/", 2)
			target.host = parts[0]
			target.url = parts[1]
			target.port = 0
		} else {
			target.host = tempString
			target.port = 0
		}
	}

	return target
}

// stringifyTarget converts a targetStruct to a string
func stringifyTarget(target targetStruct) string {
	port := target.port
	if port == 0 {
		port = 3389 // Default RDP port
	}
	return target.host + ":" + strconv.Itoa(port)
}

// loadList loads a list of strings from a file, one per line
// Skips empty lines, comments, and duplicates
func loadList(pathToFile string) []string {
	var items []string
	seen := make(map[string]bool)

	file, err := os.Open(pathToFile)
	if err != nil {
		fmt.Printf("⚠️  Error opening file %s: %v\n", pathToFile, err)
		return items
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNum := 0
	duplicateCount := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Skip duplicates
		if seen[line] {
			duplicateCount++
			if verbose {
				logVerbose("Duplicate entry in %s (line %d): %s", pathToFile, lineNum, line)
			}
			continue
		}
		seen[line] = true
		items = append(items, line)
	}

	if err := scanner.Err(); err != nil {
		fmt.Printf("⚠️  Error reading file %s: %v\n", pathToFile, err)
	}

	if duplicateCount > 0 && verbose {
		logVerbose("Skipped %d duplicate entries in %s", duplicateCount, pathToFile)
	}

	return items
}

// dispatchTask splits a large task into smaller work units for workers
func dispatchTask(taskToDispatch task) []task {
	var tasks []task
	totalTargets := len(taskToDispatch.targetsRaw)

	if totalTargets == 0 {
		return tasks
	}

	targetsPerWorker := int(math.Ceil(float64(totalTargets) / float64(taskToDispatch.numberOfWorkers)))

	for i := 0; i < taskToDispatch.numberOfWorkers; i++ {
		start := i * targetsPerWorker
		end := start + targetsPerWorker

		if start >= totalTargets {
			break
		}
		if end > totalTargets {
			end = totalTargets
		}

		workUnit := task{
			targetsRaw:      taskToDispatch.targetsRaw[start:end],
			usernames:       taskToDispatch.usernames,
			passwords:       taskToDispatch.passwords,
			numberOfWorkers: 1,
		}
		tasks = append(tasks, workUnit)
	}

	return tasks
}

// GetWANIP retrieves the public IP address
func GetWANIP() (string, error) {
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get("https://api.ipify.org")
	if err != nil {
		return "", fmt.Errorf("failed to get WAN IP: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("WAN IP service returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	return strings.TrimSpace(string(body)), nil
}

// saveProgress saves the current task progress to disk
func saveProgress() {
	if err := writeGob("./progress.gob", currentTask); err != nil {
		fmt.Printf("⚠️  Error saving progress: %v\n", err)
	}
}

// monitorCurrentTask periodically saves progress
func monitorCurrentTask() {
	ticker := time.NewTicker(600 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		saveProgress()
	}
}

// clear clears the terminal screen
func clear() {
	// ANSI escape codes for clearing screen and moving cursor to home
	fmt.Print("\033[H\033[2J")
}

// logVerbose prints verbose log messages if verbose mode is enabled
func logVerbose(format string, args ...interface{}) {
	if !verbose {
		return
	}
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	fmt.Printf("[%s] [VERBOSE] %s\n", timestamp, fmt.Sprintf(format, args...))
}

// Global credential deduplication map with optimized access
var reportedCredentials = &sync.Map{}

// writeCredentialsImmediately writes credentials to file with fsync for crash safety
// Optimized: Uses atomic operations and batched flushes
var fileWriteMutex sync.Mutex

func writeCredentialsImmediately(credentials string) bool {
	// Check if already reported (deduplication) - use AtomicLoadAndStore pattern
	if _, exists := reportedCredentials.Load(credentials); exists {
		return false
	}
	reportedCredentials.Store(credentials, true)

	fileWriteMutex.Lock()
	defer fileWriteMutex.Unlock()

	// Open with proper flags and buffering
	file, err := os.OpenFile("goods.txt", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Printf("⚠️  Failed to open goods.txt: %v\n", err)
		return false
	}
	defer file.Close()

	// Write with minimal buffering - use smaller buffer for faster response
	data := []byte(credentials + "\n") // Ensure newline at end
	if _, err := file.Write(data); err != nil {
		fmt.Printf("⚠️  Failed to write to goods.txt: %v\n", err)
		return false
	}

	// Force sync to disk - critical for crash recovery
	if err := file.Sync(); err != nil {
		fmt.Printf("⚠️  Failed to sync goods.txt: %v\n", err)
	}
	return true
}

// loadSuccessfulTargets loads already discovered credentials from goods.txt
// Returns a map of target -> flag for quick lookup
func loadSuccessfulTargets() *sync.Map {
	successfulTargets := &sync.Map{}

	file, err := os.Open("goods.txt")
	if err != nil {
		// File doesn't exist yet - that's ok
		return successfulTargets
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	count := 0

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// Parse target from format: host:port@username:password
		// We only need the host:port part
		parts := strings.SplitN(line, "@", 2)
		if len(parts) >= 2 {
			target := parts[0] // host:port
			var flag int32 = 1
			successfulTargets.Store(target, &flag)
			count++
		}
	}

	if verbose && count > 0 {
		logVerbose("Loaded %d already-compromised targets from goods.txt", count)
	}

	return successfulTargets
}

// createFile creates a file and returns the handle
func createFile(filename string) (*os.File, error) {
	return os.Create(filename)
}
