package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"servestead/frontend"
)

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
