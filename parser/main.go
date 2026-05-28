package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"rosseti-bot/rosseti"
)

const (
	targetRegion = "19"
	targetRaion  = "Алтайский р-н"
	targetGorod  = "д Кайбалы"
)

func filterKaybaly(records []rosseti.ShutdownRecord) []rosseti.ShutdownRecord {
	raionLower := strings.ToLower(targetRaion)
	gorodLower := strings.ToLower(targetGorod)
	var result []rosseti.ShutdownRecord
	for _, r := range records {
		if r.Region == targetRegion &&
			strings.Contains(strings.ToLower(r.Raion), raionLower) &&
			strings.Contains(strings.ToLower(r.Gorod), gorodLower) {
			result = append(result, r)
		}
	}
	return result
}

func main() {
	botURL := os.Getenv("BOT_SERVICE_URL")
	if botURL == "" {
		log.Fatal("BOT_SERVICE_URL is required")
	}
	apiKey := os.Getenv("PARSER_API_KEY")

	botURL = strings.TrimRight(botURL, "/") + "/api/outages"

	log.Println("Fetching data from Rosseti API...")
	records, err := rosseti.FetchData()
	if err != nil {
		log.Fatalf("fetch data: %v", err)
	}
	log.Printf("Fetched %d records total", len(records))

	records = filterKaybaly(records)
	log.Printf("Filtered for Кайбалы: %d records", len(records))

	payload := rosseti.OutagesPayload{Outages: records}
	body, err := json.Marshal(payload)
	if err != nil {
		log.Fatalf("marshal: %v", err)
	}

	req, err := http.NewRequest("POST", botURL, bytes.NewReader(body))
	if err != nil {
		log.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("send to bot: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		log.Fatalf("bot returned %d: %s", resp.StatusCode, string(respBody))
	}

	log.Printf("Bot response: %s", strings.TrimSpace(string(respBody)))
	log.Println("Parser done")
}
