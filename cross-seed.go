package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

var (
	CrossSeedEnabled        = getEnv("CROSS_SEED_ENABLED", "false")
	CrossSeedURL            = getEnv("CROSS_SEED_URL", "required")
	CrossSeedAPIKey         = getEnv("CROSS_SEED_API_KEY", "required")
	CrossSeedSleepInterval  = getEnv("CROSS_SEED_SLEEP_INTERVAL", "30")
	PushoverEnabled         = getEnv("PUSHOVER_ENABLED", "false")
	PushoverUserKey         = getEnv("PUSHOVER_USER_KEY", "required")
	PushoverToken           = getEnv("PUSHOVER_TOKEN", "required")
)

func getEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}

func setQbVars(args []string) (string, string, string, string, string) {
	return args[0], args[1], args[2], args[3], args[4]
}

func sendPushoverNotification(releaseName, releaseCat, releaseIndexer, releaseSize string) {
	pushoverMessage := fmt.Sprintf("<b>%s</b><small>\n<b>Category:</b> %s</small><small>\n<b>Indexer:</b> %s</small><small>\n<b>Size:</b> %s</small>",
		releaseName, releaseCat, releaseIndexer, releaseSize)

	jsonData := map[string]interface{}{
		"token":   PushoverToken,
		"user":    PushoverUserKey,
		"title":   "Torrent Downloaded",
		"message": pushoverMessage,
		"priority": -2,
		"html":     1,
	}

	jsonBody, err := json.Marshal(jsonData)
	if err != nil {
		fmt.Printf("Error marshalling JSON: %v\n", err)
		return
	}

	resp, err := http.Post("https://api.pushover.net/1/messages.json", "application/json", bytes.NewBuffer(jsonBody))
	if err != nil {
		fmt.Printf("Error sending Pushover notification: %v\n", err)
		return
	}
	defer resp.Body.Close()

	body, _ := ioutil.ReadAll(resp.Body)
	fmt.Printf("Pushover notification response: %v\n", string(body))
}

func searchCrossSeed(releaseInfoHash string) {
	client := &http.Client{}
	data := fmt.Sprintf("infoHash=%s&includeSingleEpisodes=true", releaseInfoHash)
	req, err := http.NewRequest("POST", fmt.Sprintf("%s/api/webhook", CrossSeedURL), strings.NewReader(data))
	if err != nil {
		fmt.Printf("Error creating request: %v\n", err)
		return
	}
	req.Header.Add("X-Api-Key", CrossSeedAPIKey)
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("Error sending cross-seed search request: %v\n", err)
		return
	}
	defer resp.Body.Close()

	body, _ := ioutil.ReadAll(resp.Body)
	fmt.Printf("Cross-seed search response: %v\n", string(body))

	sleepInterval, _ := time.ParseDuration(CrossSeedSleepInterval + "s")
	time.Sleep(sleepInterval)
}

func main() {
	args := os.Args[1:]

	releaseName, releaseInfoHash, releaseCat, releaseSize, releaseIndexer := setQbVars(args)

	if PushoverEnabled == "true" {
		sendPushoverNotification(releaseName, releaseCat, releaseIndexer, releaseSize)
	}

	if CrossSeedEnabled == "true" {
		searchCrossSeed(releaseInfoHash)
	}
}
