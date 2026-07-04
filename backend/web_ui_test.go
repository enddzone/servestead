package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"servestead/frontend"
)

func TestWebUIOptionsAddressAndURLHelpers(t *testing.T) {
	options, err := parseUIOptions([]string{"--addr", "localhost:8080", "--no-open"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if options.Address != "localhost:8080" || !options.NoOpen {
		t.Fatalf("parsed options = %+v", options)
	}
	if _, err := parseUIOptions([]string{"extra"}, io.Discard); err == nil {
		t.Fatal("unexpected positional argument was accepted")
	}
	for _, address := range []string{"127.0.0.1:0", "localhost:8080", "[::1]:9090"} {
		if err := validateUIAddress(address); err != nil {
			t.Fatalf("validateUIAddress(%q): %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:8080", "example.com:8080", "missing-port"} {
		if err := validateUIAddress(address); err == nil {
			t.Fatalf("validateUIAddress(%q) succeeded, want error", address)
		}
	}
	if got := serverURL(&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080}, "token"); got != "http://127.0.0.1:8080/ui?token=token" {
		t.Fatalf("IPv4 server URL = %q", got)
	}
	if got := serverURL(&net.TCPAddr{IP: net.ParseIP("::1"), Port: 9090}, "token"); got != "http://[::1]:9090/ui?token=token" {
		t.Fatalf("IPv6 server URL = %q", got)
	}
	if got := serverURL(staticAddr("not host port"), "token"); got != "http://127.0.0.1/ui?token=token" {
		t.Fatalf("fallback server URL = %q", got)
	}
}

func TestWebUIAuthTokenCookieAndCSRF(t *testing.T) {
	server := newWebServer(newFileProfileStore(t.TempDir()), "test-token")
	httpServer := httptest.NewServer(server.routes())
	defer httpServer.Close()

	response, err := http.Get(httpServer.URL + "/ui")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want %d", response.StatusCode, http.StatusUnauthorized)
	}

	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	response, err = client.Get(httpServer.URL + "/ui?token=test-token")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("token status = %d, want %d", response.StatusCode, http.StatusSeeOther)
	}
	cookies := response.Cookies()
	if len(cookies) == 0 || cookies[0].Name != uiSessionCookie {
		t.Fatalf("session cookie was not set: %#v", cookies)
	}

	request, _ := http.NewRequest(http.MethodGet, httpServer.URL+"/ui", nil)
	request.AddCookie(cookies[0])
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body := readResponseBody(t, response)
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "Setup Workbench") {
		t.Fatalf("authorized /ui status/body = %d/%q", response.StatusCode, body)
	}

	request, _ = http.NewRequest(http.MethodPost, httpServer.URL+"/setup/intent", strings.NewReader("intent=existing"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(cookies[0])
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d, want %d", response.StatusCode, http.StatusForbidden)
	}

	form := url.Values{"csrf": {server.csrf}, "intent": {"existing"}}
	request, _ = http.NewRequest(http.MethodPost, httpServer.URL+"/setup/intent", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(cookies[0])
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body = readResponseBody(t, response)
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "Required values") {
		t.Fatalf("valid CSRF status/body = %d/%q", response.StatusCode, body)
	}
}

func TestWebUIBootstrapTokenCanOpenMultipleBrowsers(t *testing.T) {
	server := newWebServer(newFileProfileStore(t.TempDir()), "test-token")
	httpServer := httptest.NewServer(server.routes())
	defer httpServer.Close()
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}

	first, err := client.Get(httpServer.URL + "/ui?token=test-token")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Body.Close()
	second, err := client.Get(httpServer.URL + "/ui?token=test-token")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()

	if first.StatusCode != http.StatusSeeOther || second.StatusCode != http.StatusSeeOther {
		t.Fatalf("token reuse statuses = %d, %d; want %d", first.StatusCode, second.StatusCode, http.StatusSeeOther)
	}
	if len(first.Cookies()) == 0 || len(second.Cookies()) == 0 {
		t.Fatalf("both token opens should mint sessions: first=%#v second=%#v", first.Cookies(), second.Cookies())
	}
}

func TestWebUIProvisioningIsDeferred(t *testing.T) {
	server, cookie := newAuthenticatedWebTestServer(t)
	form := url.Values{"csrf": {server.server.csrf}, "intent": {"provision"}}
	response := postWebForm(t, server.url+"/setup/intent", cookie, form)
	body := readResponseBody(t, response)
	if !strings.Contains(body, "provisioning stays in the existing setup TUI") {
		t.Fatalf("deferred provisioning notice missing: %q", body)
	}
}

func TestWebUIStartReviewAndShutdownRoutes(t *testing.T) {
	server, cookie := newAuthenticatedWebTestServer(t)

	response := authenticatedWebRequest(t, server.url+"/setup/start", http.MethodGet, cookie, nil)
	body := readResponseBody(t, response)
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "What are you setting up?") {
		t.Fatalf("start status/body = %d/%q", response.StatusCode, body)
	}

	response = authenticatedWebRequest(t, server.url+"/setup/review", http.MethodGet, cookie, nil)
	body = readResponseBody(t, response)
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "No setup draft is loaded.") {
		t.Fatalf("review status/body = %d/%q", response.StatusCode, body)
	}

	response = authenticatedWebRequest(t, server.url+"/shutdown", http.MethodGet, cookie, nil)
	_ = readResponseBody(t, response)
	if response.StatusCode != http.StatusMethodNotAllowed || response.Header.Get("Allow") != http.MethodPost {
		t.Fatalf("shutdown GET status/allow = %d/%q", response.StatusCode, response.Header.Get("Allow"))
	}

	response = postWebForm(t, server.url+"/shutdown", cookie, url.Values{"csrf": {server.server.csrf}})
	body = readResponseBody(t, response)
	if response.StatusCode != http.StatusAccepted || !strings.Contains(body, "shutting down") {
		t.Fatalf("shutdown POST status/body = %d/%q", response.StatusCode, body)
	}
	select {
	case <-server.server.done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not close done channel")
	}
}

func TestWebUIResumeIntentLoadsSavedProfile(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{
		Name:               "production",
		IP:                 "203.0.113.10",
		PrivateKeyPath:     "/tmp/servestead_ed25519",
		BaseDomain:         "example.com",
		LetsEncryptEmail:   "ops@example.com",
		PangolinAdminEmail: "ops@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSecrets(profile.ID, ProfileSecrets{PangolinAdminPassword: "Secret1!x"}); err != nil {
		t.Fatal(err)
	}
	server := newWebServer(store, "token")
	httpServer := httptest.NewServer(server.routes())
	defer httpServer.Close()
	cookie := &http.Cookie{Name: uiSessionCookie, Value: server.session}

	response := postWebForm(t, httpServer.URL+"/setup/intent", cookie, url.Values{"csrf": {server.csrf}, "intent": {"resume"}})
	body := readResponseBody(t, response)
	if !strings.Contains(body, "choose a saved profile to resume") {
		t.Fatalf("missing resume profile error: %q", body)
	}

	response = postWebForm(t, httpServer.URL+"/setup/intent", cookie, url.Values{"csrf": {server.csrf}, "intent": {"resume"}, "profile_id": {profile.ID}})
	body = readResponseBody(t, response)
	if !strings.Contains(body, "production") || !strings.Contains(body, "configured") {
		t.Fatalf("resume profile form missing saved values: %q", body)
	}

	response = postWebForm(t, httpServer.URL+"/setup/intent", cookie, url.Values{"csrf": {server.csrf}, "intent": {"unknown"}})
	body = readResponseBody(t, response)
	if !strings.Contains(body, "unknown setup intent") {
		t.Fatalf("unknown intent response missing error: %q", body)
	}
}

func TestWebUIProfileValuesMaskDraftSecrets(t *testing.T) {
	server, cookie := newAuthenticatedWebTestServer(t)
	form := url.Values{
		"csrf":                    {server.server.csrf},
		"draft_id":                {"draft-1"},
		"intent":                  {"existing"},
		"target":                  {"full"},
		"name":                    {"test-vps"},
		"ip":                      {"203.0.113.10"},
		"private_key":             {"/tmp/servestead_ed25519"},
		"domain":                  {"example.com"},
		"email":                   {"admin@example.com"},
		"initial_ssh_user":        {"root"},
		"admin_user":              {"servestead"},
		"pangolin_admin_password": {"SuperSecret1!"},
	}
	response := postWebForm(t, server.url+"/setup/profile-values", cookie, form)
	body := readResponseBody(t, response)
	if strings.Contains(body, "SuperSecret1!") {
		t.Fatalf("password was rendered in response: %q", body)
	}
	server.server.mu.Lock()
	draft := server.server.drafts["draft-1"]
	server.server.mu.Unlock()
	if draft.PangolinAdminPassword != "SuperSecret1!" {
		t.Fatalf("draft password was not retained server-side: %#v", draft)
	}

	invalid := cloneValues(form)
	invalid.Set("domain", "")
	response = postWebForm(t, server.url+"/setup/profile-values", cookie, invalid)
	body = readResponseBody(t, response)
	if !strings.Contains(body, "domain") {
		t.Fatalf("invalid profile values response missing validation error: %q", body)
	}

	platform := cloneValues(form)
	platform.Set("target", "platform")
	platform.Set("profile_id", "profile-1")
	response = postWebForm(t, server.url+"/setup/profile-values", cookie, platform)
	body = readResponseBody(t, response)
	if !strings.Contains(body, "GitOps repository") {
		t.Fatalf("platform profile values did not advance to repository panel: %q", body)
	}
}

func TestWebUIRepositoryReviewModes(t *testing.T) {
	server, cookie := newAuthenticatedWebTestServer(t)
	base := validWebProfileForm(server.server.csrf)

	createForm := cloneValues(base)
	createForm.Set("repository_mode", "create")
	response := postWebForm(t, server.url+"/setup/repository", cookie, createForm)
	body := readResponseBody(t, response)
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "Create or reuse the profile default repository.") {
		t.Fatalf("create repository review status/body = %d/%q", response.StatusCode, body)
	}

	gitDir := filepath.Join(t.TempDir(), ".git")
	if err := os.MkdirAll(gitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	existingForm := cloneValues(base)
	existingForm.Set("repository_mode", "existing")
	existingForm.Set("config_repo", filepath.Dir(gitDir))
	response = postWebForm(t, server.url+"/setup/repository", cookie, existingForm)
	body = readResponseBody(t, response)
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "Use existing checkout at "+filepath.Dir(gitDir)) {
		t.Fatalf("existing repository review status/body = %d/%q", response.StatusCode, body)
	}

	githubForm := cloneValues(base)
	githubForm.Set("repository_mode", "github")
	githubForm.Set("github_repo", "https://github.com/enddzone/servestead.git")
	githubForm.Set("github_pat", "github_pat_secret")
	response = postWebForm(t, server.url+"/setup/repository", cookie, githubForm)
	body = readResponseBody(t, response)
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "Clone https://github.com/enddzone/servestead.git") {
		t.Fatalf("github repository review status/body = %d/%q", response.StatusCode, body)
	}
	server.server.mu.Lock()
	draft := server.server.drafts[githubForm.Get("draft_id")]
	server.server.mu.Unlock()
	if draft.GitHubToken != "github_pat_secret" {
		t.Fatalf("GitHub draft token not saved: %#v", draft)
	}

	invalidForm := cloneValues(base)
	invalidForm.Set("repository_mode", "existing")
	response = postWebForm(t, server.url+"/setup/repository", cookie, invalidForm)
	body = readResponseBody(t, response)
	if !strings.Contains(body, "existing repository path is required") {
		t.Fatalf("invalid repository response missing error: %q", body)
	}
}

func TestWebUISaveDraftSecretsAndGitHubToken(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{IP: "203.0.113.10"})
	if err != nil {
		t.Fatal(err)
	}
	server := newWebServer(store, "token")

	if err := server.saveGitHubToken(profile.ID, "github_pat_direct"); err != nil {
		t.Fatal(err)
	}
	secrets, err := store.LoadSecrets(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if secrets.GitHubToken != "github_pat_direct" {
		t.Fatalf("direct GitHub token not saved: %#v", secrets)
	}

	if err := server.saveDraftGitHubToken("draft-1", "github_pat_draft"); err != nil {
		t.Fatal(err)
	}
	server.saveDraftPassword("draft-1", "Secret1!x")
	if err := server.saveDraftSecrets(profile.ID, "draft-1"); err != nil {
		t.Fatal(err)
	}
	secrets, err = store.LoadSecrets(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if secrets.GitHubToken != "github_pat_draft" || secrets.PangolinAdminPassword != "Secret1!x" {
		t.Fatalf("draft secrets not saved: %#v", secrets)
	}
	server.mu.Lock()
	_, exists := server.drafts["draft-1"]
	server.mu.Unlock()
	if exists {
		t.Fatal("draft secrets were not cleared")
	}
}

func TestWebUICredentialsSaveMasksSecrets(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{IP: "203.0.113.10", BaseDomain: "example.com", LetsEncryptEmail: "admin@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	server := newWebServer(store, "token")
	request := httptest.NewRequest(http.MethodPost, "/setup/credentials", strings.NewReader(url.Values{
		"csrf":                    {server.csrf},
		"profile_id":              {profile.ID},
		"pangolin_admin_email":    {"ops@example.com"},
		"pangolin_admin_password": {"Secret1!x"},
		"github_pat":              {"github_pat_secret"},
	}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: uiSessionCookie, Value: server.session})
	response := httptest.NewRecorder()

	server.routes().ServeHTTP(response, request)

	body := response.Body.String()
	if strings.Contains(body, "Secret1!x") || strings.Contains(body, "github_pat_secret") {
		t.Fatalf("secret leaked in credentials response: %q", body)
	}
	loaded, _, err := store.Load(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PangolinAdminEmail != "ops@example.com" {
		t.Fatalf("Pangolin email not saved: %+v", loaded)
	}
	secrets, err := store.LoadSecrets(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if secrets.PangolinAdminPassword != "Secret1!x" || secrets.GitHubToken != "github_pat_secret" {
		t.Fatalf("secrets not saved: %+v", secrets)
	}

	request = httptest.NewRequest(http.MethodPost, "/setup/credentials", strings.NewReader(url.Values{
		"csrf":                 {server.csrf},
		"profile_id":           {profile.ID},
		"pangolin_admin_email": {"not-an-email"},
	}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: uiSessionCookie, Value: server.session})
	response = httptest.NewRecorder()
	server.routes().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid credential email status = %d", response.Code)
	}
}

func TestWebUIRunStartsFakeRunner(t *testing.T) {
	server, cookie := newAuthenticatedWebTestServer(t)
	started := make(chan webRunRequest, 1)
	server.server.manager.runFunc = func(ctx context.Context, request webRunRequest, runID string) {
		started <- request
		server.server.manager.finishActive(request.Profile.ID)
		server.server.manager.broker.Emit(webEvent{Type: "done", RunID: runID, Status: runStatusComplete})
	}
	form := validWebProfileForm(server.server.csrf)
	server.server.mu.Lock()
	server.server.drafts[form.Get("draft_id")] = webDraft{PangolinAdminPassword: "Secret1!x", GitHubToken: "github_pat_secret"}
	server.server.mu.Unlock()

	response := postWebForm(t, server.url+"/setup/run", cookie, form)
	body := readResponseBody(t, response)
	if response.StatusCode != http.StatusOK || !strings.Contains(body, `data-run-stream="/events/runs/`) {
		t.Fatalf("run response status/body = %d/%q", response.StatusCode, body)
	}
	select {
	case request := <-started:
		if request.Profile.ID == "" || request.Config.Host != "203.0.113.10" {
			t.Fatalf("fake run request missing profile/config: %+v", request)
		}
		secrets, err := server.server.store.LoadSecrets(request.Profile.ID)
		if err != nil {
			t.Fatal(err)
		}
		if secrets.PangolinAdminPassword != "Secret1!x" || secrets.GitHubToken != "github_pat_secret" {
			t.Fatalf("run did not persist draft secrets: %+v", secrets)
		}
	case <-time.After(time.Second):
		t.Fatal("fake run did not start")
	}

	invalid := validWebProfileForm(server.server.csrf)
	invalid.Set("ip", "")
	response = postWebForm(t, server.url+"/setup/run", cookie, invalid)
	body = readResponseBody(t, response)
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "--ip is required") {
		t.Fatalf("invalid run response status/body = %d/%q", response.StatusCode, body)
	}
}

func TestWebUICancelRetryAndEventRoutes(t *testing.T) {
	server, cookie := newAuthenticatedWebTestServer(t)
	cancelled := make(chan struct{})
	server.server.manager.mu.Lock()
	server.server.manager.active["profile-1"] = &webRun{runID: "run-1", profileID: "profile-1", cancel: func() { close(cancelled) }}
	server.server.manager.mu.Unlock()

	response := postWebForm(t, server.url+"/setup/cancel", cookie, url.Values{"csrf": {server.server.csrf}})
	_ = readResponseBody(t, response)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("cancel without run_id status = %d", response.StatusCode)
	}

	response = postWebForm(t, server.url+"/setup/cancel", cookie, url.Values{"csrf": {server.server.csrf}, "run_id": {"missing"}})
	_ = readResponseBody(t, response)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("cancel missing run status = %d", response.StatusCode)
	}

	response = postWebForm(t, server.url+"/setup/cancel", cookie, url.Values{"csrf": {server.server.csrf}, "run_id": {"run-1"}})
	body := readResponseBody(t, response)
	if response.StatusCode != http.StatusOK || body != "cancelling" {
		t.Fatalf("cancel active status/body = %d/%q", response.StatusCode, body)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("cancel did not call active run cancel")
	}

	response = postWebForm(t, server.url+"/setup/retry", cookie, url.Values{"csrf": {server.server.csrf}, "run_id": {"unknown"}})
	_ = readResponseBody(t, response)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("retry unknown status = %d", response.StatusCode)
	}

	profile, err := server.server.store.Create(Profile{
		IP:                 "203.0.113.11",
		PrivateKeyPath:     "/tmp/servestead_ed25519",
		BaseDomain:         "retry.example.com",
		LetsEncryptEmail:   "admin@retry.example.com",
		PangolinAdminEmail: "admin@retry.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.server.store.SaveSecrets(profile.ID, ProfileSecrets{PangolinAdminPassword: "Secret1!x", GitHubToken: "github_pat_retry"}); err != nil {
		t.Fatal(err)
	}
	retryStarted := make(chan string, 1)
	server.server.manager.runFunc = func(ctx context.Context, request webRunRequest, runID string) {
		retryStarted <- runID
		server.server.manager.finishActive(request.Profile.ID)
		server.server.manager.broker.Emit(webEvent{Type: "done", RunID: runID, Status: runStatusComplete})
	}
	server.server.manager.mu.Lock()
	server.server.manager.history["old-run"] = webRunHistory{ProfileID: profile.ID}
	server.server.manager.mu.Unlock()
	response = postWebForm(t, server.url+"/setup/retry", cookie, url.Values{"csrf": {server.server.csrf}, "run_id": {"old-run"}})
	body = readResponseBody(t, response)
	if response.StatusCode != http.StatusOK || !strings.Contains(body, `data-run-stream="/events/runs/`) {
		t.Fatalf("retry success status/body = %d/%q", response.StatusCode, body)
	}
	select {
	case <-retryStarted:
	case <-time.After(time.Second):
		t.Fatal("retry run did not start")
	}

	server.server.manager.broker.Emit(webEvent{Type: "log", RunID: "run-events", Line: "hello"})
	server.server.manager.broker.Emit(webEvent{Type: "done", RunID: "run-events", Status: runStatusComplete})
	response = authenticatedWebRequest(t, server.url+"/events/runs/run-events", http.MethodGet, cookie, nil)
	body = readResponseBody(t, response)
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "event: log") || !strings.Contains(body, "event: done") {
		t.Fatalf("SSE status/body = %d/%q", response.StatusCode, body)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.Contains(contentType, "text/event-stream") {
		t.Fatalf("SSE content type = %q", contentType)
	}

	response = authenticatedWebRequest(t, server.url+"/events/runs/", http.MethodGet, cookie, nil)
	_ = readResponseBody(t, response)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("empty run events status = %d", response.StatusCode)
	}
}

func TestWebEventBrokerReplaysAfterLastEventID(t *testing.T) {
	broker := newWebEventBroker()
	broker.Emit(webEvent{Type: "log", RunID: "run-1", Line: "first"})
	broker.Emit(webEvent{Type: "log", RunID: "run-1", Line: "second"})

	events, unsubscribe := broker.Subscribe("run-1", 1)
	defer unsubscribe()

	select {
	case event := <-events:
		if event.Line != "second" {
			t.Fatalf("replayed event = %q, want second", event.Line)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for replayed event")
	}
}

func TestWebEventBrokerReporterAndLineWriter(t *testing.T) {
	broker := newWebEventBroker()
	events, unsubscribe := broker.Subscribe("run-1", 0)
	defer unsubscribe()

	broker.Report(TaskEvent{Type: TaskStarted, RunID: "run-1", Stage: "platform", TaskIndex: 1, TaskTotal: 2, TaskName: "configure"})
	broker.Report(TaskEvent{Type: TaskLogLine, RunID: "run-1", Stage: "platform", Stream: "stderr", Line: "warning"})
	broker.Report(TaskEvent{Type: TaskFailed, RunID: "run-1", Stage: "platform", Error: "failed"})
	broker.Report(TaskEvent{Type: TaskRunCompleted, RunID: "run-1", Stage: "platform"})
	writer := broker.LineWriter("run-1", "preparation", "stdout")
	n, err := writer.Write([]byte("first line\npartial"))
	if err != nil || n != len("first line\npartial") {
		t.Fatalf("writer.Write n/err = %d/%v", n, err)
	}
	if _, err := writer.Write([]byte(" line\n")); err != nil {
		t.Fatal(err)
	}

	expected := []struct {
		eventType string
		status    string
		line      string
		stream    string
	}{
		{eventType: "status", status: runStatusRunning},
		{eventType: "log", line: "warning", stream: "stderr"},
		{eventType: "status", status: runStatusFailed},
		{eventType: "status", status: runStatusComplete},
		{eventType: "log", line: "first line", stream: "stdout"},
		{eventType: "log", line: "partial line", stream: "stdout"},
	}
	for index, want := range expected {
		select {
		case event := <-events:
			if event.Type != want.eventType || event.Status != want.status || event.Line != want.line || event.Stream != want.stream {
				t.Fatalf("event %d = %+v, want %+v", index, event, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for event %d", index)
		}
	}
}

func TestWebEventBrokerReplaysLargeBufferWithoutBlocking(t *testing.T) {
	broker := newWebEventBroker()
	for index := range 200 {
		broker.Emit(webEvent{Type: "log", RunID: "run-1", Line: strconv.Itoa(index)})
	}

	type subscription struct {
		events      <-chan webEvent
		unsubscribe func()
	}
	subscribed := make(chan subscription, 1)
	go func() {
		events, unsubscribe := broker.Subscribe("run-1", 0)
		subscribed <- subscription{events: events, unsubscribe: unsubscribe}
	}()

	var sub subscription
	select {
	case sub = <-subscribed:
	case <-time.After(time.Second):
		t.Fatal("Subscribe blocked while replaying a large event buffer")
	}
	defer sub.unsubscribe()
	for index := range 200 {
		select {
		case event := <-sub.events:
			if event.Line != strconv.Itoa(index) {
				t.Fatalf("replayed event %d = %q", index, event.Line)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for replayed event %d", index)
		}
	}
}

func TestClassifyWebRecovery(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{err: errRepositoryReviewRequired, want: "dirty_repository"},
		{err: context.Canceled, want: "run_failed"},
		{err: errString("profile domain and Let's Encrypt email are required"), want: "bad_profile_values"},
		{err: errString("missing Pangolin administrator credentials"), want: "missing_credentials"},
	}
	for _, test := range tests {
		got, _ := classifyWebRecovery(test.err)
		if got != test.want {
			t.Fatalf("classifyWebRecovery(%v) = %s, want %s", test.err, got, test.want)
		}
	}
}

func TestWebRunManagerRecoveryEventIncludesRenderedControls(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{IP: "203.0.113.10"})
	if err != nil {
		t.Fatal(err)
	}
	server := newWebServer(store, "token")
	events, unsubscribe := server.manager.broker.Subscribe("run-1", 0)
	defer unsubscribe()

	server.manager.fail(profile, &ProfileState{Runs: map[string]SetupRun{"run-1": {ID: "run-1"}}}, "run-1", errString("missing Pangolin administrator credentials"))

	event := nextWebEventOfType(t, events, "recovery")
	if event.ProfileID != profile.ID {
		t.Fatalf("recovery profile id = %q, want %q", event.ProfileID, profile.ID)
	}
	for _, expected := range []string{
		`hx-post="/setup/credentials"`,
		`hx-post="/setup/retry"`,
		`name="csrf" value="` + server.csrf + `"`,
	} {
		if !strings.Contains(event.HTML, expected) {
			t.Fatalf("recovery HTML missing %q:\n%s", expected, event.HTML)
		}
	}
}

func TestWebRunManagerRejectsDuplicateActiveRun(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{IP: "203.0.113.10"})
	if err != nil {
		t.Fatal(err)
	}
	profile, state, err := store.Load(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	manager := newWebRunManager(store, newWebEventBroker())
	started := make(chan struct{})
	manager.runFunc = func(ctx context.Context, request webRunRequest, runID string) {
		close(started)
		<-ctx.Done()
		manager.finishActive(request.Profile.ID)
		manager.broker.Emit(webEvent{Type: "done", RunID: runID, Status: runStatusCancelled})
	}

	runID, err := manager.Start(context.Background(), webRunRequest{Profile: profile, State: state, Config: setupConfig{}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := manager.Start(context.Background(), webRunRequest{Profile: profile, State: state, Config: setupConfig{}}); err == nil {
		t.Fatal("duplicate active run was allowed")
	}
	if !manager.Cancel(runID) {
		t.Fatal("active run was not cancelled")
	}
}

func TestWebRunManagerRunOutlivesCallerContext(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{IP: "203.0.113.10"})
	if err != nil {
		t.Fatal(err)
	}
	profile, state, err := store.Load(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	manager := newWebRunManager(store, newWebEventBroker())
	started := make(chan struct{})
	cancelledByCaller := make(chan struct{}, 1)
	manager.runFunc = func(ctx context.Context, request webRunRequest, runID string) {
		close(started)
		select {
		case <-ctx.Done():
			cancelledByCaller <- struct{}{}
		case <-time.After(50 * time.Millisecond):
		}
		manager.finishActive(request.Profile.ID)
		manager.broker.Emit(webEvent{Type: "done", RunID: runID, Status: runStatusComplete})
	}
	callerCtx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := manager.Start(callerCtx, webRunRequest{Profile: profile, State: state, Config: setupConfig{}}); err != nil {
		t.Fatal(err)
	}
	<-started
	select {
	case <-cancelledByCaller:
		t.Fatal("run context inherited the completed HTTP request context")
	case <-time.After(75 * time.Millisecond):
	}
}

func TestWebRunManagerFailedRunEmitsTerminalStatus(t *testing.T) {
	broker := newWebEventBroker()
	manager := newWebRunManager(newFileProfileStore(t.TempDir()), broker)
	events, unsubscribe := broker.Subscribe("run-1", 0)
	defer unsubscribe()

	manager.fail(Profile{ID: "profile-1"}, &ProfileState{Runs: map[string]SetupRun{"run-1": {ID: "run-1"}}}, "run-1", errString("git status: context canceled"))

	var sawFailedStatus, sawDone bool
	for !sawFailedStatus || !sawDone {
		select {
		case event := <-events:
			if event.Type == "status" && event.Status == runStatusFailed {
				sawFailedStatus = true
			}
			if event.Type == "done" && event.Status == runStatusFailed {
				sawDone = true
			}
		case <-time.After(time.Second):
			t.Fatalf("missing failed terminal events: status=%v done=%v", sawFailedStatus, sawDone)
		}
	}
}

func TestWebRunManagerCancelRunPersistsAndEmitsTerminalStatus(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{IP: "203.0.113.10"})
	if err != nil {
		t.Fatal(err)
	}
	_, state, err := store.Load(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	state.Runs["run-1"] = SetupRun{ID: "run-1", Status: runStatusRunning}
	if err := store.Save(profile, state); err != nil {
		t.Fatal(err)
	}
	broker := newWebEventBroker()
	manager := newWebRunManager(store, broker)
	events, unsubscribe := broker.Subscribe("run-1", 0)
	defer unsubscribe()

	manager.cancelRun(profile, &state, "run-1")

	_, loadedState, err := store.Load(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loadedState.Runs["run-1"].Status != runStatusCancelled {
		t.Fatalf("run status = %q, want cancelled", loadedState.Runs["run-1"].Status)
	}
	status := nextWebEventOfType(t, events, "status")
	if status.Status != runStatusCancelled {
		t.Fatalf("cancel status event = %+v", status)
	}
	logEvent := nextWebEventOfType(t, events, "log")
	if logEvent.Line != "Run cancelled." {
		t.Fatalf("cancel log event = %+v", logEvent)
	}
	done := nextWebEventOfType(t, events, "done")
	if done.Status != runStatusCancelled {
		t.Fatalf("cancel done event = %+v", done)
	}
}

func TestWebRunManagerRunFailsDuringPreflightWithoutRemoteWork(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	profile := Profile{ID: "profile-1", IP: "203.0.113.10"}
	state := ProfileState{Runs: map[string]SetupRun{"run-1": {ID: "run-1", Status: runStatusRunning}}}
	if err := store.Save(profile, state); err != nil {
		t.Fatal(err)
	}
	broker := newWebEventBroker()
	manager := newWebRunManager(store, broker)
	events, unsubscribe := broker.Subscribe("run-1", 0)
	defer unsubscribe()

	manager.run(context.Background(), webRunRequest{Profile: profile, State: state, Config: setupConfig{}}, "run-1")

	status := nextWebEventOfType(t, events, "status")
	if status.Status != runStatusFailed {
		t.Fatalf("preflight failure status event = %+v", status)
	}
	recovery := nextWebEventOfType(t, events, "recovery")
	if recovery.Kind != "run_failed" {
		t.Fatalf("preflight failure recovery event = %+v", recovery)
	}
	done := nextWebEventOfType(t, events, "done")
	if done.Status != runStatusFailed {
		t.Fatalf("preflight failure done event = %+v", done)
	}
	_, loaded, err := store.Load(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Runs["run-1"].Status != runStatusFailed {
		t.Fatalf("persisted run status = %q, want failed", loaded.Runs["run-1"].Status)
	}
}

func TestFrontendRunLogAssetsKeepFixedScrollableColoredTerminal(t *testing.T) {
	css, err := frontend.Assets.ReadFile("assets/servestead.css")
	if err != nil {
		t.Fatal(err)
	}
	js, err := frontend.Assets.ReadFile("assets/servestead.js")
	if err != nil {
		t.Fatal(err)
	}

	cssText := string(css)
	for _, expected := range []string{
		".run-status-panel, .log-panel",
		"height: min(",
		"overscroll-behavior: contain",
		".run-actions",
		".ansi-green",
	} {
		if !strings.Contains(cssText, expected) {
			t.Fatalf("CSS asset missing %q", expected)
		}
	}

	jsText := string(js)
	for _, expected := range []string{
		"line.innerHTML = renderTerminalLine(data.line)",
		"if (data.html)",
		"function renderTerminalLine",
		"function applyAnsiCodes",
		"function logLineClass",
	} {
		if !strings.Contains(jsText, expected) {
			t.Fatalf("JS asset missing %q", expected)
		}
	}
}

type authenticatedWebTestServer struct {
	server *webServer
	url    string
	close  func()
}

func nextWebEventOfType(t *testing.T, events <-chan webEvent, eventType string) webEvent {
	t.Helper()
	for {
		select {
		case event := <-events:
			if event.Type == eventType {
				return event
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s event", eventType)
		}
	}
}

func newAuthenticatedWebTestServer(t *testing.T) (authenticatedWebTestServer, *http.Cookie) {
	t.Helper()
	server := newWebServer(newFileProfileStore(t.TempDir()), "token")
	httpServer := httptest.NewServer(server.routes())
	t.Cleanup(httpServer.Close)
	return authenticatedWebTestServer{server: server, url: httpServer.URL, close: httpServer.Close}, &http.Cookie{Name: uiSessionCookie, Value: server.session}
}

func postWebForm(t *testing.T, target string, cookie *http.Cookie, form url.Values) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(cookie)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func readResponseBody(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

type errString string

func (err errString) Error() string {
	return string(err)
}

type staticAddr string

func (addr staticAddr) Network() string {
	return "test"
}

func (addr staticAddr) String() string {
	return string(addr)
}

func authenticatedWebRequest(t *testing.T, target, method string, cookie *http.Cookie, form url.Values) *http.Response {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	request, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	request.AddCookie(cookie)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func validWebProfileForm(csrf string) url.Values {
	return url.Values{
		"csrf":             {csrf},
		"draft_id":         {"draft-1"},
		"intent":           {"existing"},
		"target":           {"full"},
		"name":             {"test-vps"},
		"ip":               {"203.0.113.10"},
		"private_key":      {"/tmp/servestead_ed25519"},
		"domain":           {"example.com"},
		"email":            {"admin@example.com"},
		"initial_ssh_user": {"root"},
		"admin_user":       {"servestead"},
	}
}

func cloneValues(values url.Values) url.Values {
	clone := make(url.Values, len(values))
	for key, list := range values {
		clone[key] = append([]string(nil), list...)
	}
	return clone
}
