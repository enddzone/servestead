package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

var pangolinRegistrationHTTPClient = &http.Client{Timeout: 5 * time.Second}

func pangolinInitialSetupComplete(ctx context.Context, client *http.Client, dashboardURL string) (bool, error) {
	url := strings.TrimRight(dashboardURL, "/") + "/api/v1/auth/initial-setup-complete"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	response, err := client.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return false, fmt.Errorf("Pangolin returned %s", response.Status)
	}
	var payload struct {
		Data *struct {
			Complete *bool `json:"complete"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return false, fmt.Errorf("decode Pangolin response: %w", err)
	}
	if payload.Data == nil || payload.Data.Complete == nil {
		return false, fmt.Errorf("Pangolin response did not include setup completion state")
	}
	return *payload.Data.Complete, nil
}

func concisePangolinRegistrationError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if len(message) <= 80 {
		return message
	}
	return message[:79] + "."
}
