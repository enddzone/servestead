package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHetznerCreate(t *testing.T) {
	t.Helper()
	var createBody map[string]any
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("unexpected authorization header: %q", request.Header.Get("Authorization"))
		}
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/servers":
			if err := json.NewDecoder(request.Body).Decode(&createBody); err != nil {
				t.Fatal(err)
			}
			writeJSON(t, response, `{"server":{"id":42,"status":"initializing","public_net":{"ipv4":{"ip":""}}}}`)
		case request.Method == http.MethodGet && request.URL.Path == "/servers/42":
			polls++
			writeJSON(t, response, `{"server":{"id":42,"status":"running","public_net":{"ipv4":{"ip":"203.0.113.42"}}}}`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	provider := newHetznerProvider(server.Client(), server.URL, "test-token")
	provider.pollInterval = time.Millisecond
	created, err := provider.Create(context.Background(), provisionConfig{
		Name: "aegis-01", Region: "fsn1", Size: "cx23", Image: "ubuntu-24.04", SSHKey: "admin-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "42" || created.IPv4 != "203.0.113.42" {
		t.Fatalf("unexpected server: %+v", created)
	}
	if polls != 1 {
		t.Fatalf("expected one status poll, got %d", polls)
	}
	if createBody["name"] != "aegis-01" || createBody["location"] != "fsn1" || createBody["server_type"] != "cx23" || createBody["image"] != "ubuntu-24.04" {
		t.Fatalf("unexpected create body: %#v", createBody)
	}
	keys, ok := createBody["ssh_keys"].([]any)
	if !ok || len(keys) != 1 || keys[0] != "admin-key" {
		t.Fatalf("unexpected SSH keys: %#v", createBody["ssh_keys"])
	}
}

func TestDigitalOceanCreate(t *testing.T) {
	var createBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/droplets":
			if err := json.NewDecoder(request.Body).Decode(&createBody); err != nil {
				t.Fatal(err)
			}
			writeJSON(t, response, `{"droplet":{"id":84,"status":"new","networks":{"v4":[]}}}`)
		case request.Method == http.MethodGet && request.URL.Path == "/droplets/84":
			writeJSON(t, response, `{"droplet":{"id":84,"status":"active","networks":{"v4":[{"ip_address":"10.0.0.2","type":"private"},{"ip_address":"198.51.100.84","type":"public"}]}}}`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	provider := newDigitalOceanProvider(server.Client(), server.URL, "test-token")
	provider.pollInterval = time.Millisecond
	created, err := provider.Create(context.Background(), provisionConfig{
		Name: "aegis-02", Region: "nyc3", Size: "s-1vcpu-1gb", Image: "ubuntu-24-04-x64", SSHKey: "12345",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "84" || created.IPv4 != "198.51.100.84" {
		t.Fatalf("unexpected server: %+v", created)
	}
	keys, ok := createBody["ssh_keys"].([]any)
	if !ok || len(keys) != 1 || keys[0] != float64(12345) {
		t.Fatalf("expected numeric SSH key ID, got %#v", createBody["ssh_keys"])
	}
}

func TestAPIErrorMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusUnauthorized)
		writeJSON(t, response, `{"error":{"message":"token rejected"}}`)
	}))
	defer server.Close()

	provider := newHetznerProvider(server.Client(), server.URL, "bad-token")
	_, err := provider.Create(context.Background(), provisionConfig{})
	if err == nil || err.Error() != "API returned 401 Unauthorized: token rejected" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func writeJSON(t *testing.T, response http.ResponseWriter, body string) {
	t.Helper()
	response.Header().Set("Content-Type", "application/json")
	if _, err := response.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
}
