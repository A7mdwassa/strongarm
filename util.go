//util.go
package main

import (
	"bufio"
	"encoding/gob"
	"fmt"
	"io/ioutil"
	"net/http"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

type targetStruct struct {
	host   string
	port   int
	scheme string
	url    string
}

type workerState struct {
	WorkerId       int
	WorkerProgress int32 // Changed to int32 for atomic operations
}

type task struct {
	targetsRaw      []string
	target          targetStruct
	usernames       []string
	passwords       []string
	numberOfWorkers int
}

func writeGob(filePath string, object interface{}) error {
	file, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := gob.NewEncoder(file)
	return encoder.Encode(object)
}

func readGob(filePath string, object interface{}) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	decoder := gob.NewDecoder(file)
	return decoder.Decode(object)
}

func parseTarget(targetString string) targetStruct {
	var target targetStruct
	tempString := targetString

	if strings.Contains(targetString, "://") {
		s := strings.Split(targetString, "://")
		target.scheme = s[0]
		tempString = s[1]
	}

	if strings.Contains(tempString, ":") {
		s := strings.Split(tempString, ":")
		target.host = s[0]
		tempString = s[1]
		if strings.Contains(tempString, "/") {
			tempStringSlice := strings.Split(tempString, "/")
			target.port, _ = strconv.Atoi(tempStringSlice[0])
			target.url = strings.Join(tempStringSlice[1:], "/")
		} else {
			target.port, _ = strconv.Atoi(tempString)
		}
	} else {
		if strings.Contains(tempString, "/") {
			tempStringSlice := strings.Split(tempString, "/")
			target.host = tempStringSlice[0]
			target.url = strings.Join(tempStringSlice[1:], "/")
			target.port = 0
		} else {
			target.host = tempString
			target.port = 0
		}
	}

	return target
}

func stringifyTarget(targetToStringify targetStruct) string {
	portString := strconv.Itoa(targetToStringify.port)
	return targetToStringify.host + ":" + portString
}

func loadList(pathToFile string) []string {
	var loadedItems []string
	file, err := os.Open(pathToFile)
	if err != nil {
		fmt.Printf("Error opening file %s: %v\n", pathToFile, err)
		return loadedItems
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			loadedItems = append(loadedItems, line)
		}
	}
	return loadedItems
}

func loadList1(pathToFile string) []string {
	var loadedItems []string

	file, err := os.Open(pathToFile)
	if err != nil {
		fmt.Printf("Error opening file %s: %v\n", pathToFile, err)
		return loadedItems
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// If there is no colon, append default port :3389
		if !strings.Contains(line, ":") {
			line = line + ":3389"
		}

		loadedItems = append(loadedItems, line)
	}

	return loadedItems
}

func dispatchTask(taskToDispatch task) []task {
	var tasksToReturn []task
	totalTargets := len(taskToDispatch.targetsRaw)
	targetsPerWorker := int(math.Ceil(float64(totalTargets) / float64(taskToDispatch.numberOfWorkers)))

	for i := 0; i < taskToDispatch.numberOfWorkers; i++ {
		start := i * targetsPerWorker
		end := start + targetsPerWorker
		if end > totalTargets {
			end = totalTargets
		}
		if start >= totalTargets {
			break
		}

		task := task{
			targetsRaw:      taskToDispatch.targetsRaw[start:end],
			usernames:       taskToDispatch.usernames,
			passwords:       taskToDispatch.passwords,
			numberOfWorkers: 1,
		}
		tasksToReturn = append(tasksToReturn, task)
	}
	return tasksToReturn
}

func GetWANIP() (string, error) {
	resp, err := http.Get("https://api.ipify.org")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(body), nil
}


func saveProgress() {
	err := writeGob("./progress.gob", currentTask)
	if err != nil {
		fmt.Printf("Error saving progress: %v\n", err)
	}
}

func monitorCurrentTask() {
	for {
		saveProgress()
		time.Sleep(600 * time.Second)
	}
}

func clear() {
	fmt.Print("\033[H\033[2J")
}