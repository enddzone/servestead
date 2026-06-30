package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/digitalocean/godo"
)

func TestDigitalOceanCreate(t *testing.T) {
	var createBody map[string]any
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/v2/droplets":
			if err := json.NewDecoder(request.Body).Decode(&createBody); err != nil {
				t.Fatal(err)
			}
			writeJSON(t, response, `{"droplet":{"id":84,"name":"aegis-02","status":"new","networks":{"v4":[]},"created_at":"2026-06-30T12:00:00Z"}}`)
		case request.Method == http.MethodGet && request.URL.Path == "/v2/droplets/84":
			polls++
			writeJSON(t, response, `{"droplet":{"id":84,"name":"aegis-02","status":"active","region":{"slug":"nyc3"},"size_slug":"s-1vcpu-1gb","image":{"slug":"ubuntu-24-04-x64"},"networks":{"v4":[{"ip_address":"10.0.0.2","type":"private"},{"ip_address":"198.51.100.84","type":"public"}]},"created_at":"2026-06-30T12:00:00Z"}}`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	provider := newTestDigitalOceanProvider(t, server)
	provider.pollInterval = time.Millisecond
	created, err := provider.Create(context.Background(), provisionConfig{
		Name: "aegis-02", Region: "nyc3", Size: "s-1vcpu-1gb", Image: "ubuntu-24-04-x64", SSHKey: "12345",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "84" || created.IPv4 != "198.51.100.84" || created.Region != "nyc3" || created.Size != "s-1vcpu-1gb" {
		t.Fatalf("unexpected server: %+v", created)
	}
	if polls != 1 {
		t.Fatalf("expected one status poll, got %d", polls)
	}
	keys, ok := createBody["ssh_keys"].([]any)
	if !ok || len(keys) != 1 || keys[0] != float64(12345) {
		t.Fatalf("expected numeric SSH key ID, got %#v", createBody["ssh_keys"])
	}
	if createBody["region"] != "nyc3" || createBody["size"] != "s-1vcpu-1gb" || createBody["image"] != "ubuntu-24-04-x64" {
		t.Fatalf("unexpected create body: %#v", createBody)
	}
}

func TestDigitalOceanCatalogIncludesCostAndKeys(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v2/regions":
			writeJSON(t, response, `{"regions":[{"slug":"nyc3","name":"New York 3","available":true,"sizes":["s-1vcpu-1gb"]}]}`)
		case request.Method == http.MethodGet && request.URL.Path == "/v2/sizes":
			writeJSON(t, response, `{"sizes":[{"slug":"s-1vcpu-1gb","memory":1024,"vcpus":1,"disk":25,"transfer":1,"price_monthly":6,"price_hourly":0.00893,"regions":["nyc3"],"available":true}]}`)
		case request.Method == http.MethodGet && request.URL.Path == "/v2/images" && request.URL.Query().Get("type") == "distribution":
			writeJSON(t, response, `{"images":[{"slug":"ubuntu-24-04-x64","name":"Ubuntu 24.04","distribution":"Ubuntu","status":"available","regions":["nyc3"],"min_disk_size":7},{"slug":"debian-12-x64","name":"Debian","distribution":"Debian","status":"available","regions":["nyc3"],"min_disk_size":7}]}`)
		case request.Method == http.MethodGet && request.URL.Path == "/v2/account/keys":
			writeJSON(t, response, `{"ssh_keys":[{"id":10,"name":"servestead","fingerprint":"aa:bb","public_key":"ssh-ed25519 AAAA test"}]}`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	catalog, err := newTestDigitalOceanProvider(t, server).Catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Regions) != 1 || catalog.Regions[0].Slug != "nyc3" {
		t.Fatalf("unexpected regions: %+v", catalog.Regions)
	}
	if len(catalog.Sizes) != 1 || catalog.Sizes[0].PriceMonthly != 6 || catalog.Sizes[0].PriceHourly != 0.00893 {
		t.Fatalf("unexpected sizes: %+v", catalog.Sizes)
	}
	if len(catalog.Images) != 1 || catalog.Images[0].Slug != "ubuntu-24-04-x64" {
		t.Fatalf("unexpected images: %+v", catalog.Images)
	}
	if len(catalog.SSHKeys) != 1 || catalog.SSHKeys[0].ID != 10 {
		t.Fatalf("unexpected keys: %+v", catalog.SSHKeys)
	}
}

func TestDigitalOceanCreateSSHKeyRebootAndDestroy(t *testing.T) {
	var actionBody map[string]any
	destroyed := false
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/v2/account/keys":
			var body map[string]string
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["name"] != "servestead-key" || !strings.HasPrefix(body["public_key"], "ssh-ed25519 ") {
				t.Fatalf("unexpected key create body: %#v", body)
			}
			writeJSON(t, response, `{"ssh_key":{"id":11,"name":"servestead-key","fingerprint":"cc:dd"}}`)
		case request.Method == http.MethodPost && request.URL.Path == "/v2/droplets/84/actions":
			if err := json.NewDecoder(request.Body).Decode(&actionBody); err != nil {
				t.Fatal(err)
			}
			writeJSON(t, response, `{"action":{"id":1,"status":"in-progress","type":"reboot"}}`)
		case request.Method == http.MethodDelete && request.URL.Path == "/v2/droplets/84":
			destroyed = true
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	provider := newTestDigitalOceanProvider(t, server)
	key, err := provider.CreateSSHKey(context.Background(), "servestead-key", "ssh-ed25519 AAAA test")
	if err != nil {
		t.Fatal(err)
	}
	if key.ID != 11 || key.Fingerprint != "cc:dd" {
		t.Fatalf("unexpected created key: %+v", key)
	}
	if err := provider.Reboot(context.Background(), "84"); err != nil {
		t.Fatal(err)
	}
	if actionBody["type"] != "reboot" {
		t.Fatalf("unexpected reboot body: %#v", actionBody)
	}
	if err := provider.Destroy(context.Background(), "84"); err != nil {
		t.Fatal(err)
	}
	if !destroyed {
		t.Fatal("destroy endpoint was not called")
	}
}

func TestDigitalOceanAPIErrorMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusUnauthorized)
		writeJSON(t, response, `{"message":"token rejected"}`)
	}))
	defer server.Close()

	provider := newTestDigitalOceanProvider(t, server)
	_, err := provider.Create(context.Background(), provisionConfig{})
	if err == nil || !strings.Contains(err.Error(), "token rejected") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func newTestDigitalOceanProvider(t *testing.T, server *httptest.Server) *digitalOceanProvider {
	t.Helper()
	client := godo.NewClient(server.Client())
	baseURL, err := url.Parse(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	client.BaseURL = baseURL
	return newDigitalOceanProviderFromClient(client)
}

func writeJSON(t *testing.T, response http.ResponseWriter, body string) {
	t.Helper()
	response.Header().Set("Content-Type", "application/json")
	if _, err := response.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
}
