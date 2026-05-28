package rosseti

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"time"
)

const (
	DataURL    = "https://www.rosseti-sib.ru/local/templates/rosseti/components/is/proxy/shutdown_schedule_table/data.php"
	RegionsURL = "https://www.rosseti-sib.ru/local/templates/rosseti/components/is/proxy/shutdown_schedule_table/regions.php"
	maxRetries = 3
)

var httpClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:    2,
		IdleConnTimeout: 30 * time.Second,
	},
}

func newRequest(method, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Referer", "https://www.rosseti-sib.ru/otkluchenie-energii/")
	req.Header.Set("Origin", "https://www.rosseti-sib.ru")
	return req, nil
}

func postJSON(url string, data []byte, target interface{}) error {
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(math.Pow(2, float64(attempt))) * time.Second
			log.Printf("Retry %d/%d after %v", attempt+1, maxRetries, backoff)
			time.Sleep(backoff)
		}

		req, err := newRequest("POST", url, bytes.NewReader(data))
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		resp, err := httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("http do: %w", err)
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("read body: %w", err)
			continue
		}

		if resp.StatusCode != 200 {
			lastErr = fmt.Errorf("http %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
			continue
		}

		if err := json.Unmarshal(body, target); err != nil {
			lastErr = fmt.Errorf("unmarshal: %w", err)
			continue
		}
		return nil
	}
	return fmt.Errorf("all retries failed: %w", lastErr)
}

func FetchData() ([]ShutdownRecord, error) {
	var records []ShutdownRecord
	if err := postJSON(DataURL, []byte("region="), &records); err != nil {
		return nil, err
	}
	return records, nil
}

func FetchRegions() ([]Region, error) {
	req, err := newRequest("GET", RegionsURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get regions: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	var regions []Region
	if err := json.Unmarshal(body, &regions); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return regions, nil
}
