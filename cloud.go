package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type provisionConfig struct {
	Name   string
	Region string
	Size   string
	Image  string
	SSHKey string
}

type server struct {
	ID   string
	IPv4 string
}

type cloudProvider interface {
	Create(context.Context, provisionConfig) (server, error)
}

type apiClient struct {
	http         *http.Client
	baseURL      string
	token        string
	pollInterval time.Duration
}

func (client apiClient) request(ctx context.Context, method, path string, body, response any) error {
	var requestBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		requestBody = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, requestBody)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+client.token)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	httpResponse, err := client.http.Do(request)
	if err != nil {
		return err
	}
	defer httpResponse.Body.Close()

	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(httpResponse.Body, 64<<10))
		return fmt.Errorf("API returned %s: %s", httpResponse.Status, apiErrorMessage(message))
	}
	if response == nil {
		return nil
	}
	if err := json.NewDecoder(httpResponse.Body).Decode(response); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func apiErrorMessage(body []byte) string {
	var payload struct {
		Message string `json:"message"`
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil {
		if payload.Message != "" {
			return payload.Message
		}
		if payload.Error.Message != "" {
			return payload.Error.Message
		}
	}
	message := strings.TrimSpace(string(body))
	if message == "" {
		return "empty error response"
	}
	return message
}

func wait(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type hetznerProvider struct{ apiClient }

func newHetznerProvider(httpClient *http.Client, baseURL, token string) *hetznerProvider {
	return &hetznerProvider{apiClient{http: httpClient, baseURL: baseURL, token: token, pollInterval: 2 * time.Second}}
}

type hetznerServer struct {
	ID        int64  `json:"id"`
	Status    string `json:"status"`
	PublicNet struct {
		IPv4 struct {
			IP string `json:"ip"`
		} `json:"ipv4"`
	} `json:"public_net"`
}

func (provider *hetznerProvider) Create(ctx context.Context, config provisionConfig) (server, error) {
	body := map[string]any{
		"name":        config.Name,
		"location":    config.Region,
		"server_type": config.Size,
		"image":       config.Image,
		"ssh_keys":    []string{config.SSHKey},
	}
	var result struct {
		Server hetznerServer `json:"server"`
	}
	if err := provider.request(ctx, http.MethodPost, "/servers", body, &result); err != nil {
		return server{}, err
	}
	if result.Server.ID == 0 {
		return server{}, errors.New("API response did not include a server ID")
	}
	return provider.waitForIPv4(ctx, result.Server)
}

func (provider *hetznerProvider) waitForIPv4(ctx context.Context, created hetznerServer) (server, error) {
	for {
		if created.Status == "running" && created.PublicNet.IPv4.IP != "" {
			return server{ID: strconv.FormatInt(created.ID, 10), IPv4: created.PublicNet.IPv4.IP}, nil
		}
		if err := wait(ctx, provider.pollInterval); err != nil {
			return server{}, fmt.Errorf("wait for server %d: %w", created.ID, err)
		}
		var result struct {
			Server hetznerServer `json:"server"`
		}
		if err := provider.request(ctx, http.MethodGet, "/servers/"+strconv.FormatInt(created.ID, 10), nil, &result); err != nil {
			return server{}, err
		}
		created = result.Server
	}
}

type digitalOceanProvider struct{ apiClient }

func newDigitalOceanProvider(httpClient *http.Client, baseURL, token string) *digitalOceanProvider {
	return &digitalOceanProvider{apiClient{http: httpClient, baseURL: baseURL, token: token, pollInterval: 2 * time.Second}}
}

type digitalOceanDroplet struct {
	ID       int64  `json:"id"`
	Status   string `json:"status"`
	Networks struct {
		V4 []struct {
			IPAddress string `json:"ip_address"`
			Type      string `json:"type"`
		} `json:"v4"`
	} `json:"networks"`
}

func (provider *digitalOceanProvider) Create(ctx context.Context, config provisionConfig) (server, error) {
	body := map[string]any{
		"name":     config.Name,
		"region":   config.Region,
		"size":     config.Size,
		"image":    config.Image,
		"ssh_keys": []any{digitalOceanSSHKey(config.SSHKey)},
	}
	var result struct {
		Droplet digitalOceanDroplet `json:"droplet"`
	}
	if err := provider.request(ctx, http.MethodPost, "/droplets", body, &result); err != nil {
		return server{}, err
	}
	if result.Droplet.ID == 0 {
		return server{}, errors.New("API response did not include a droplet ID")
	}
	return provider.waitForIPv4(ctx, result.Droplet)
}

func digitalOceanSSHKey(value string) any {
	if id, err := strconv.ParseInt(value, 10, 64); err == nil {
		return id
	}
	return value
}

func (provider *digitalOceanProvider) waitForIPv4(ctx context.Context, created digitalOceanDroplet) (server, error) {
	for {
		if created.Status == "active" {
			for _, network := range created.Networks.V4 {
				if network.Type == "public" && network.IPAddress != "" {
					return server{ID: strconv.FormatInt(created.ID, 10), IPv4: network.IPAddress}, nil
				}
			}
		}
		if err := wait(ctx, provider.pollInterval); err != nil {
			return server{}, fmt.Errorf("wait for droplet %d: %w", created.ID, err)
		}
		var result struct {
			Droplet digitalOceanDroplet `json:"droplet"`
		}
		if err := provider.request(ctx, http.MethodGet, "/droplets/"+strconv.FormatInt(created.ID, 10), nil, &result); err != nil {
			return server{}, err
		}
		created = result.Droplet
	}
}
