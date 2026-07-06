package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/a-h/templ"
	"github.com/pkg/browser"

	"servestead/frontend"
)

const (
	uiDefaultAddress = "127.0.0.1:0"
	uiSessionCookie  = "servestead_ui_session"
)

type uiOptions struct {
	Address string
	NoOpen  bool
}

func runUI(ctx context.Context, args []string, stdout, stderr io.Writer, _ getenvFunc) error {
	options, err := parseUIOptions(args, stderr)
	if err != nil {
		return err
	}
	if err := validateUIAddress(options.Address); err != nil {
		return err
	}
	store, err := newDefaultProfileStore()
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", options.Address)
	if err != nil {
		return fmt.Errorf("listen for UI: %w", err)
	}
	defer listener.Close()

	token := randomURLToken()
	server := newWebServer(store, token)
	httpServer := &http.Server{Handler: server.routes()}
	errs := make(chan error, 1)
	go func() {
		err := httpServer.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
			return
		}
		errs <- nil
	}()

	url := serverURL(listener.Addr(), token)
	fmt.Fprintf(stdout, "Servestead UI: %s\n", url)
	if !options.NoOpen {
		if err := browser.OpenURL(url); err != nil {
			fmt.Fprintf(stderr, "warning: open browser: %v\n", err)
		}
	}

	select {
	case <-ctx.Done():
	case <-server.done:
	case err := <-errs:
		return err
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown UI: %w", err)
	}
	return <-errs
}

func parseUIOptions(args []string, stderr io.Writer) (uiOptions, error) {
	flags := flag.NewFlagSet("ui", flag.ContinueOnError)
	flags.SetOutput(stderr)
	options := uiOptions{Address: uiDefaultAddress}
	flags.StringVar(&options.Address, "addr", uiDefaultAddress, "loopback address for the local web UI")
	flags.BoolVar(&options.NoOpen, "no-open", false, "print the UI URL without opening a browser")
	if err := flags.Parse(args); err != nil {
		return uiOptions{}, err
	}
	if flags.NArg() != 0 {
		return uiOptions{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	return options, nil
}

func validateUIAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("--addr must be host:port: %w", err)
	}
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return nil
	}
	return errors.New("--addr must bind localhost, 127.0.0.1, or ::1")
}

func serverURL(address net.Addr, token string) string {
	host, port, err := net.SplitHostPort(address.String())
	if err != nil {
		return "http://127.0.0.1/ui?token=" + token
	}
	if host == "::" || host == "" {
		host = "127.0.0.1"
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("http://%s:%s/ui?token=%s", host, port, token)
}

type webServer struct {
	store       ProfileStore
	manager     *webRunManager
	token       string
	session     string
	csrf        string
	done        chan struct{}
	drafts      map[string]webDraft
	shutdownMux sync.Once
	mu          sync.Mutex
}

type webDraft struct {
	PangolinAdminPassword string
	GitHubToken           string
}

func newWebServer(store ProfileStore, token string) *webServer {
	broker := newWebEventBroker()
	server := &webServer{
		store:   store,
		token:   token,
		session: randomURLToken(),
		csrf:    randomURLToken(),
		done:    make(chan struct{}),
		drafts:  map[string]webDraft{},
	}
	server.manager = newWebRunManager(store, broker)
	server.manager.recoveryHTML = server.recoveryHTML
	return server
}

func (server *webServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/assets/", http.FileServer(http.FS(frontend.Assets)))
	mux.HandleFunc("/ui", server.withAuth(server.handleUI))
	mux.HandleFunc("/setup", server.withAuth(server.handleSetup))
	mux.HandleFunc("/setup/start", server.withAuth(server.handleStart))
	mux.HandleFunc("/setup/intent", server.withAuth(server.handleIntent))
	mux.HandleFunc("/setup/profile-values", server.withAuth(server.handleProfileValues))
	mux.HandleFunc("/setup/repository", server.withAuth(server.handleRepository))
	mux.HandleFunc("/setup/review", server.withAuth(server.handleReview))
	mux.HandleFunc("/setup/run", server.withAuth(server.handleRun))
	mux.HandleFunc("/setup/cancel", server.withAuth(server.handleCancel))
	mux.HandleFunc("/setup/retry", server.withAuth(server.handleRetry))
	mux.HandleFunc("/setup/credentials", server.withAuth(server.handleCredentials))
	mux.HandleFunc("/ops/profiles", server.withAuth(server.handleOpsProfiles))
	mux.HandleFunc("/ops/profiles/", server.withAuth(server.handleOpsProfile))
	mux.HandleFunc("/ops/cloud/provision", server.withAuth(server.handleOpsCloudProvision))
	mux.HandleFunc("/events/runs/", server.withAuth(server.handleRunEvents))
	mux.HandleFunc("/shutdown", server.withAuth(server.handleShutdown))
	return mux
}

func (server *webServer) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if !server.authorized(response, request) {
			return
		}
		if request.Method == http.MethodPost && !server.validCSRF(request) {
			http.Error(response, "invalid CSRF token", http.StatusForbidden)
			return
		}
		next(response, request)
	}
}

func (server *webServer) handleUI(response http.ResponseWriter, request *http.Request) {
	if !requireMethod(response, request, http.MethodGet) {
		return
	}
	data := server.homeData(request.Context(), request.URL.Query().Get("profile"), "", "")
	server.renderAppShell(response, request, "Servestead Command Center", "Command Center", "home", data.SelectedProfileID, frontend.HomePanel(data))
}

func (server *webServer) handleSetup(response http.ResponseWriter, request *http.Request) {
	if !requireMethod(response, request, http.MethodGet) {
		return
	}
	server.renderShell(response, request, frontend.StartPanel(server.startData("", "")))
}

func (server *webServer) handleStart(response http.ResponseWriter, request *http.Request) {
	if !requireMethod(response, request, http.MethodGet) {
		return
	}
	server.render(response, request, frontend.StartPanel(server.startData("", "")))
}

func (server *webServer) handleReview(response http.ResponseWriter, request *http.Request) {
	if !requireMethod(response, request, http.MethodGet) {
		return
	}
	server.render(response, request, frontend.ReviewPanel(frontend.ReviewData{ProfileFormData: frontend.ProfileFormData{CSRFToken: server.csrf}, RunLine: "No setup draft is loaded."}))
}

func (server *webServer) handleShutdown(response http.ResponseWriter, request *http.Request) {
	if !requireMethod(response, request, http.MethodPost) {
		return
	}
	server.shutdownMux.Do(func() { close(server.done) })
	response.WriteHeader(http.StatusAccepted)
	_, _ = io.WriteString(response, "shutting down")
}

func requireMethod(response http.ResponseWriter, request *http.Request, method string) bool {
	if request.Method == method {
		return true
	}
	response.Header().Set("Allow", method)
	http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

func (server *webServer) authorized(response http.ResponseWriter, request *http.Request) bool {
	if request.URL.Path == "/ui" && request.Method == http.MethodGet {
		token := request.URL.Query().Get("token")
		if token != "" && server.validToken(token) {
			// codeql[go/cookie-secure-not-set] This UI is forced to loopback HTTP; Secure would make the local session unusable.
			http.SetCookie(response, &http.Cookie{
				Name:     uiSessionCookie,
				Value:    server.session,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
			})
			http.Redirect(response, request, "/ui", http.StatusSeeOther)
			return false
		}
	}
	cookie, err := request.Cookie(uiSessionCookie)
	if err == nil && cookie.Value == server.session {
		return true
	}
	http.Error(response, "servestead ui token required", http.StatusUnauthorized)
	return false
}

func (server *webServer) validToken(token string) bool {
	return token == server.token
}

func (server *webServer) validCSRF(request *http.Request) bool {
	return request.FormValue("csrf") == server.csrf
}

func (server *webServer) renderShell(response http.ResponseWriter, request *http.Request, content templ.Component) {
	server.renderAppShell(response, request, "Servestead Setup Workbench", "Setup Workbench", "setup", server.defaultProfileID(request.URL.Query().Get("profile")), content)
}

func (server *webServer) renderAppShell(response http.ResponseWriter, request *http.Request, title string, heading string, active string, activeProfileID string, content templ.Component) {
	server.render(response, request, frontend.Shell(frontend.ShellData{
		Title:         title,
		Heading:       heading,
		ActiveSection: active,
		ActiveProfile: activeProfileID,
		CSRFToken:     server.csrf,
		Profiles:      server.profileOptions(),
	}, content))
}

func (server *webServer) renderOpsShell(response http.ResponseWriter, request *http.Request, content templ.Component) {
	server.renderAppShell(response, request, "Servestead Profile Diagnostics", "Profile Diagnostics", "profiles", server.defaultProfileID(request.URL.Query().Get("profile")), content)
}

func (server *webServer) render(response http.ResponseWriter, request *http.Request, component templ.Component) {
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := component.Render(request.Context(), response); err != nil {
		http.Error(response, err.Error(), http.StatusInternalServerError)
	}
}

func (server *webServer) recoveryHTML(runID, profileID, kind, message string) string {
	var builder strings.Builder
	err := frontend.RecoveryPanel(frontend.RecoveryData{
		CSRFToken: server.csrf,
		ProfileID: profileID,
		RunID:     runID,
		Kind:      kind,
		Message:   message,
		CanRetry:  true,
	}).Render(context.Background(), &builder)
	if err != nil {
		return ""
	}
	return builder.String()
}

func (server *webServer) startData(notice, errorText string) frontend.StartData {
	return frontend.StartData{CSRFToken: server.csrf, Profiles: server.profileOptions(), Notice: notice, Error: errorText}
}

func (server *webServer) homeData(ctx context.Context, selected string, notice string, errorText string) frontend.HomeData {
	data := frontend.HomeData{
		CSRFToken: server.csrf,
		Greeting:  homeGreeting(time.Now()),
		NodeName:  "Local Node",
		Profiles:  server.profileOptions(),
		Notice:    notice,
		Error:     errorText,
	}
	selected = server.defaultProfileID(selected)
	data.SelectedProfileID = selected
	data.Commands = []frontend.CommandItem{
		{Label: "Setup", Detail: "Complete setup, configure stacks, and harden security.", URL: "/setup", Tone: "pink"},
		{Label: "Profiles & Diagnostics", Detail: "Review profiles, check health, runs, access, and logs.", URL: "/ops/profiles", Tone: "blue"},
		{Label: "GitOps Review", Detail: "Review repository state, sync status, and drift.", URL: "#", Tone: "mauve", Disabled: selected == ""},
		{Label: "Access & Cloud", Detail: "Manage access controls, secrets, and cloud settings.", URL: "#", Tone: "peach", Disabled: selected == ""},
	}
	if selected != "" {
		data.Commands[2].URL = "/ops/profiles/" + selected + "/gitops/review"
		data.Commands[3].URL = "/ops/profiles?profile=" + selected
	}
	if selected == "" {
		return data
	}
	profile, state, err := server.store.Load(selected)
	if err != nil {
		data.Error = firstNonEmpty(data.Error, err.Error())
		return data
	}
	gitState := server.opsGitSummary(ctx, profile.ConfigRepositoryPath)
	activeRunStatus := server.opsActiveRunStatus(profile.ID, state)
	setupStatus := opsSetupStatus(state)
	cloudState := opsCloudState(profile)
	data.HasProfile = true
	data.SelectedProfile = firstNonEmpty(profile.Name, profile.IP, profile.ID)
	data.SelectedAddress = firstNonEmpty(profile.IP, "not configured")
	data.SelectedDomain = firstNonEmpty(profile.BaseDomain, "not configured")
	data.SelectedRepository = firstNonEmpty(profile.ConfigRepositoryPath, "not configured")
	data.Region = profileRegion(profile)
	data.Uptime = profileUptime(profile)
	data.ActiveRunStatus = activeRunStatus
	data.SetupStatus = setupStatus
	data.GitState = gitState
	data.CloudState = cloudState
	data.HealthStatus, data.HealthDetail = homeHealth(activeRunStatus, setupStatus)
	data.SetupProgressLabel, data.SetupProgress = homeSetupProgress(setupStatus, gitState, state)
	data.NextAction = opsNextAction(profile, activeRunStatus, setupStatus, gitState, cloudState)
	data.UpdatedAt = formatWebTime(profile.UpdatedAt)
	data.Issues = server.homeIssues(profile, state, gitState, setupStatus)
	data.Activities = append(data.Activities, frontend.HomeActivity{
		Tone:   "mauve",
		Title:  "Profile updated",
		Detail: data.SelectedProfile,
		Time:   data.UpdatedAt,
		URL:    "/ops/profiles?profile=" + profile.ID,
	})
	if run, ok := latestSetupRun(state); ok {
		data.Activities = append(data.Activities, frontend.HomeActivity{
			Tone:   statusTone(run.Status),
			Title:  "Latest run " + run.Status,
			Detail: opsRunStageSummary(run),
			Time:   formatWebTime(run.UpdatedAt),
			URL:    "/ops/profiles?profile=" + profile.ID,
		})
	}
	return data
}

func homeGreeting(now time.Time) string {
	hour := now.Local().Hour()
	switch {
	case hour < 12:
		return "Good morning"
	case hour < 17:
		return "Good afternoon"
	default:
		return "Good evening"
	}
}

func profileRegion(profile Profile) string {
	if profile.Cloud != nil && profile.Cloud.Region != "" {
		return profile.Cloud.Region
	}
	return "local"
}

func profileUptime(profile Profile) string {
	if profile.Cloud == nil || profile.Cloud.CreatedAt.IsZero() {
		return "not tracked"
	}
	duration := time.Since(profile.Cloud.CreatedAt)
	if duration < 0 {
		return "just created"
	}
	days := int(duration.Hours()) / 24
	hours := int(duration.Hours()) % 24
	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	return fmt.Sprintf("%dh", hours)
}

func homeHealth(activeRunStatus string, setupStatus string) (string, string) {
	switch {
	case activeRunStatus == runStatusRunning:
		return "Running", "Setup or stack work is currently active."
	case setupStatus == runStatusFailed || setupStatus == runStatusCancelled:
		return "Needs attention", "Review recovery or recent run details."
	case setupStatus == runStatusComplete || strings.Contains(setupStatus, "complete"):
		return "Healthy", "No incidents detected"
	default:
		return "Ready", "Setup is ready to continue."
	}
}

func homeSetupProgress(setupStatus string, gitState string, state ProfileState) (string, int) {
	switch {
	case setupStatus == runStatusComplete || strings.Contains(setupStatus, "complete"):
		return "Step 5 of 5 · Complete", 100
	case setupStatus == runStatusFailed || setupStatus == runStatusCancelled:
		return "Step 4 of 5 · Recovery needed", max(20, completedHomeProgress(state))
	case gitState == "changes pending" || gitState == "needs push":
		return "Step 3 of 5 · GitOps sync", 62
	default:
		return "Step 3 of 5 · GitOps sync", max(40, completedHomeProgress(state))
	}
}

func completedHomeProgress(state ProfileState) int {
	completed := completedSetupStages(state)
	stageNames := []string{"bootstrap", "harden", "network", "proxy", "observability"}
	count := 0
	for _, stage := range stageNames {
		if completed[stage] {
			count++
		}
	}
	return count * 20
}

func (server *webServer) homeIssues(profile Profile, state ProfileState, gitState string, setupStatus string) []frontend.HomeIssue {
	issues := []frontend.HomeIssue{}
	if gitState == "changes pending" || gitState == "needs push" {
		issues = append(issues, frontend.HomeIssue{
			Tone:        "peach",
			Title:       "Stack drift detected",
			Detail:      "Repository state needs review before stack sync.",
			ActionLabel: "Review drift",
			URL:         "/ops/profiles/" + profile.ID + "/gitops/review",
		})
	}
	secrets, err := server.store.LoadSecrets(profile.ID)
	if err == nil && (secrets.PangolinAdminPassword == "" || secrets.GitHubToken == "") {
		issues = append(issues, frontend.HomeIssue{
			Tone:        "mauve",
			Title:       "Secrets pending review",
			Detail:      "One or more optional credentials are not configured.",
			ActionLabel: "Review secrets",
			URL:         "/ops/profiles?profile=" + profile.ID + "#ops-detail",
		})
	}
	if run, ok := latestSetupRun(state); ok && (run.Status == runStatusFailed || run.Status == runStatusCancelled) {
		issues = append(issues, frontend.HomeIssue{
			Tone:        "red",
			Title:       "Last run needs review",
			Detail:      opsRunStageSummary(run),
			ActionLabel: "Open runs",
			URL:         "/ops/profiles?profile=" + profile.ID + "#ops-detail",
		})
	}
	if setupStatus == "not run" && len(issues) == 0 {
		issues = append(issues, frontend.HomeIssue{
			Tone:        "blue",
			Title:       "Setup is ready",
			Detail:      "Continue the guided setup when you are ready.",
			ActionLabel: "Resume setup",
			URL:         "/setup",
		})
	}
	if len(issues) > 3 {
		return issues[:3]
	}
	return issues
}

func (server *webServer) defaultProfileID(preferred string) string {
	if strings.TrimSpace(preferred) != "" {
		return strings.TrimSpace(preferred)
	}
	summaries, err := server.store.List()
	if err != nil {
		return ""
	}
	var selected ProfileSummary
	for _, summary := range summaries {
		if selected.ID == "" || summary.UpdatedAt.After(selected.UpdatedAt) {
			selected = summary
		}
	}
	return selected.ID
}

func statusTone(status string) string {
	switch status {
	case runStatusComplete:
		return "green"
	case runStatusFailed, runStatusCancelled:
		return "red"
	case runStatusRunning:
		return "blue"
	default:
		return "peach"
	}
}

func (server *webServer) profileOptions() []frontend.ProfileOption {
	summaries, err := server.store.List()
	if err != nil {
		return nil
	}
	options := make([]frontend.ProfileOption, 0, len(summaries))
	for _, summary := range summaries {
		options = append(options, frontend.ProfileOption{
			ID:     summary.ID,
			Name:   firstNonEmpty(summary.Name, summary.IP),
			IP:     summary.IP,
			Status: firstNonEmpty(summary.LastStatus, "no runs yet"),
		})
	}
	return options
}

func (server *webServer) handleIntent(response http.ResponseWriter, request *http.Request) {
	if !requireMethod(response, request, http.MethodPost) {
		return
	}
	intent := request.FormValue("intent")
	switch intent {
	case "existing", "":
		server.render(response, request, frontend.ProfileValuesPanel(server.newProfileForm("existing", "full")))
	case "resume":
		profileID := request.FormValue("profile_id")
		if profileID == "" {
			server.render(response, request, frontend.StartPanel(server.startData("", "choose a saved profile to resume")))
			return
		}
		data, err := server.profileFormFromProfile(profileID, "resume", "platform")
		if err != nil {
			server.render(response, request, frontend.StartPanel(server.startData("", err.Error())))
			return
		}
		server.render(response, request, frontend.ProfileValuesPanel(data))
	case "provision":
		server.render(response, request, frontend.StartPanel(server.startData("DigitalOcean provisioning stays in the existing setup TUI until the cloud phase.", "")))
	default:
		server.render(response, request, frontend.StartPanel(server.startData("", "unknown setup intent")))
	}
}

func (server *webServer) newProfileForm(intent, target string) frontend.ProfileFormData {
	return frontend.ProfileFormData{
		CSRFToken:           server.csrf,
		DraftID:             randomURLToken(),
		Intent:              intent,
		Target:              target,
		PrivateKeyPath:      defaultKeygenConfig().Path,
		InitialSSHUser:      "root",
		AdminUser:           "servestead",
		PangolinAdminStatus: "generated for fresh installs",
	}
}

func (server *webServer) profileFormFromProfile(profileID, intent, target string) (frontend.ProfileFormData, error) {
	profile, _, err := server.store.Load(profileID)
	if err != nil {
		return frontend.ProfileFormData{}, err
	}
	secrets, _ := server.store.LoadSecrets(profile.ID)
	status := "not configured"
	if secrets.PangolinAdminPassword != "" {
		status = "configured"
	}
	return frontend.ProfileFormData{
		CSRFToken:           server.csrf,
		DraftID:             randomURLToken(),
		Intent:              intent,
		Target:              target,
		ProfileID:           profile.ID,
		Name:                profile.Name,
		IP:                  profile.IP,
		PrivateKeyPath:      profile.PrivateKeyPath,
		BaseDomain:          profile.BaseDomain,
		LetsEncryptEmail:    profile.LetsEncryptEmail,
		InitialSSHUser:      firstNonEmpty(profile.InitialSSHUser, "root"),
		AdminUser:           firstNonEmpty(profile.AdminUser, "servestead"),
		PangolinAdminEmail:  profile.PangolinAdminEmail,
		PangolinAdminStatus: status,
		ConfigRepository:    profile.ConfigRepositoryPath,
	}, nil
}

func (server *webServer) handleProfileValues(response http.ResponseWriter, request *http.Request) {
	if !requireMethod(response, request, http.MethodPost) {
		return
	}
	data := server.profileFormFromRequest(request)
	server.saveDraftPassword(data.DraftID, request.FormValue("pangolin_admin_password"))
	options := setupOptionsFromProfileForm(data)
	if err := validateSavedProfileOptions(options); err != nil {
		data.Errors = []string{err.Error()}
		server.render(response, request, frontend.ProfileValuesPanel(data))
		return
	}
	config := setupConfig{
		Mode:               setupModeFullRun,
		Host:               options.IP,
		InitialSSHUser:     options.InitialSSHUser,
		AdminUser:          options.AdminUser,
		PrivateKeyPath:     options.PrivateKeyPath,
		AdminPublicKeyPath: publicKeyPath(options.PrivateKeyPath),
		BaseDomain:         options.BaseDomain,
		LetsEncryptEmail:   options.LetsEncryptEmail,
		PangolinAdminEmail: firstNonEmpty(options.PangolinAdminEmail, options.LetsEncryptEmail),
		ServerSecret:       setupGeneratedPlaceholder,
	}
	if data.Target == "full" {
		if err := validateFullRunConfig(config); err != nil {
			data.Errors = []string{err.Error()}
			server.render(response, request, frontend.ProfileValuesPanel(data))
			return
		}
	} else if err := validateStageRunConfig("platform", config); err != nil {
		data.Errors = []string{err.Error()}
		server.render(response, request, frontend.ProfileValuesPanel(data))
		return
	}
	server.render(response, request, frontend.RepositoryPanel(frontend.RepositoryFormData{ProfileFormData: data, RepositoryMode: "create"}))
}

func (server *webServer) handleRepository(response http.ResponseWriter, request *http.Request) {
	if !requireMethod(response, request, http.MethodPost) {
		return
	}
	data := server.profileFormFromRequest(request)
	if err := server.saveDraftGitHubToken(data.DraftID, request.FormValue("github_pat")); err != nil {
		data.Errors = []string{err.Error()}
		server.render(response, request, frontend.RepositoryPanel(frontend.RepositoryFormData{ProfileFormData: data, RepositoryMode: request.FormValue("repository_mode")}))
		return
	}
	data.ConfigRepository = expandUserPath(strings.TrimSpace(request.FormValue("config_repo")))
	data.GitHubRepositoryURL = strings.TrimSpace(request.FormValue("github_repo"))
	mode := firstNonEmpty(request.FormValue("repository_mode"), "create")
	var validationErr error
	switch mode {
	case "existing":
		validationErr = validateExistingRepositoryInput(data.ConfigRepository)
	case "github":
		validationErr = validateGitHubRepositoryInput(data.GitHubRepositoryURL)
	case "create":
	default:
		validationErr = fmt.Errorf("unknown repository mode %q", mode)
	}
	if validationErr != nil {
		data.Errors = []string{validationErr.Error()}
		server.render(response, request, frontend.RepositoryPanel(frontend.RepositoryFormData{ProfileFormData: data, RepositoryMode: mode}))
		return
	}
	review := server.reviewData(data, mode)
	server.render(response, request, frontend.ReviewPanel(review))
}

func (server *webServer) reviewData(data frontend.ProfileFormData, repositoryMode string) frontend.ReviewData {
	options := setupOptionsFromProfileForm(data)
	config := setupConfig{
		Mode:               setupModeFullRun,
		Host:               options.IP,
		InitialSSHUser:     options.InitialSSHUser,
		AdminUser:          options.AdminUser,
		PrivateKeyPath:     options.PrivateKeyPath,
		AdminPublicKeyPath: publicKeyPath(options.PrivateKeyPath),
		BaseDomain:         options.BaseDomain,
		LetsEncryptEmail:   options.LetsEncryptEmail,
		PangolinAdminEmail: firstNonEmpty(options.PangolinAdminEmail, options.LetsEncryptEmail),
		ProfileID:          firstNonEmpty(options.ProfileID, "(new profile)"),
		ServerSecret:       setupGeneratedPlaceholder,
	}
	plan := setupPlanSummary(config)
	runLine := "Full setup can run Bootstrap, Harden, Platform, and committed stack deployment as needed."
	if data.Target == "platform" {
		plan = "Selected action: Platform\n- Network: configure Docker networking and UFW.\n- Proxy: deploy Pangolin, Traefik, Gerbil, and Newt.\n- Observability: deploy Beszel, Dozzle, and Dockhand from committed configuration.\n- No new VPS will be provisioned.\n"
		runLine = "Platform runs Network, Proxy, and Observability for the selected profile."
	}
	repositoryLine := "Create or reuse the profile default repository."
	switch repositoryMode {
	case "existing":
		repositoryLine = "Use existing checkout at " + data.ConfigRepository + "."
	case "github":
		repositoryLine = "Clone " + data.GitHubRepositoryURL + "."
	}
	return frontend.ReviewData{ProfileFormData: data, RepositoryMode: repositoryMode, Plan: plan, RepositoryLine: repositoryLine, RunLine: runLine}
}

func (server *webServer) handleRun(response http.ResponseWriter, request *http.Request) {
	if !requireMethod(response, request, http.MethodPost) {
		return
	}
	data := server.profileFormFromRequest(request)
	options := setupOptionsFromProfileForm(data)
	options.Yes = true
	var (
		profile Profile
		state   ProfileState
		config  setupConfig
		err     error
		stage   string
	)
	if data.Target == "platform" {
		stage = "platform"
		profile, state, config, err = prepareProfileStageSetup(options, server.store, stage)
	} else {
		profile, state, config, err = prepareProfileSetup(options, server.store, io.Discard)
	}
	if err != nil {
		data.Errors = []string{err.Error()}
		server.render(response, request, frontend.ProfileValuesPanel(data))
		return
	}
	if err := server.saveDraftSecrets(profile.ID, data.DraftID); err != nil {
		data.Errors = []string{err.Error()}
		server.render(response, request, frontend.RepositoryPanel(frontend.RepositoryFormData{ProfileFormData: data, RepositoryMode: request.FormValue("repository_mode")}))
		return
	}
	runID, err := server.manager.Start(request.Context(), webRunRequest{Profile: profile, State: state, Config: config, Stage: stage})
	if err != nil {
		http.Error(response, err.Error(), http.StatusConflict)
		return
	}
	server.render(response, request, frontend.RunPanel(frontend.RunData{
		CSRFToken: server.csrf,
		ProfileID: profile.ID,
		RunID:     runID,
		Target:    firstNonEmpty(stage, "full"),
		Status:    runStatusRunning,
		StreamURL: "/events/runs/" + runID,
	}))
}

func (server *webServer) handleCancel(response http.ResponseWriter, request *http.Request) {
	if !requireMethod(response, request, http.MethodPost) {
		return
	}
	runID := request.FormValue("run_id")
	if runID == "" {
		http.Error(response, "run_id is required", http.StatusBadRequest)
		return
	}
	if !server.manager.Cancel(runID) {
		http.Error(response, "run is not active", http.StatusNotFound)
		return
	}
	_, _ = io.WriteString(response, "cancelling")
}

func (server *webServer) handleRetry(response http.ResponseWriter, request *http.Request) {
	if !requireMethod(response, request, http.MethodPost) {
		return
	}
	runID := request.FormValue("run_id")
	nextRunID, profileID, err := server.manager.Retry(request.Context(), runID)
	if err != nil {
		http.Error(response, err.Error(), http.StatusBadRequest)
		return
	}
	server.render(response, request, frontend.RunPanel(frontend.RunData{
		CSRFToken: server.csrf,
		ProfileID: profileID,
		RunID:     nextRunID,
		Status:    runStatusRunning,
		StreamURL: "/events/runs/" + nextRunID,
	}))
}

func (server *webServer) handleCredentials(response http.ResponseWriter, request *http.Request) {
	if !requireMethod(response, request, http.MethodPost) {
		return
	}
	profileID := request.FormValue("profile_id")
	if profileID == "" {
		http.Error(response, "profile_id is required", http.StatusBadRequest)
		return
	}
	profile, state, err := server.store.Load(profileID)
	if err != nil {
		http.Error(response, err.Error(), http.StatusNotFound)
		return
	}
	secrets, err := server.store.LoadSecrets(profileID)
	if err != nil {
		http.Error(response, err.Error(), http.StatusInternalServerError)
		return
	}
	if email := strings.TrimSpace(request.FormValue("pangolin_admin_email")); email != "" {
		if !setupEmailLike(email) {
			http.Error(response, "Pangolin administrator email must be a valid email address", http.StatusBadRequest)
			return
		}
		profile.PangolinAdminEmail = email
	}
	if password := request.FormValue("pangolin_admin_password"); strings.TrimSpace(password) != "" {
		secrets.PangolinAdminPassword = strings.TrimSpace(password)
	}
	if token := strings.TrimSpace(request.FormValue("github_pat")); token != "" {
		normalized, err := normalizeGitHubToken(token)
		if err != nil {
			http.Error(response, err.Error(), http.StatusBadRequest)
			return
		}
		secrets.GitHubToken = normalized
	}
	if err := server.store.Save(profile, state); err != nil {
		http.Error(response, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := server.store.SaveSecrets(profileID, secrets); err != nil {
		http.Error(response, err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = io.WriteString(response, `<div class="notice">Credentials saved. Retry when ready.</div>`)
}

func (server *webServer) handleRunEvents(response http.ResponseWriter, request *http.Request) {
	if !requireMethod(response, request, http.MethodGet) {
		return
	}
	runID := strings.TrimPrefix(request.URL.Path, "/events/runs/")
	if runID == "" {
		http.NotFound(response, request)
		return
	}
	lastID, _ := strconv.Atoi(request.Header.Get("Last-Event-ID"))
	events, unsubscribe := server.manager.broker.Subscribe(runID, lastID)
	defer unsubscribe()
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-cache")
	response.Header().Set("Connection", "keep-alive")
	flusher, _ := response.(http.Flusher)
	for {
		select {
		case event := <-events:
			data, err := json.Marshal(event)
			if err != nil {
				continue
			}
			fmt.Fprintf(response, "id: %d\nevent: %s\ndata: %s\n\n", event.ID, event.Type, data)
			if flusher != nil {
				flusher.Flush()
			}
			if event.Type == "done" {
				return
			}
		case <-request.Context().Done():
			return
		}
	}
}

func (server *webServer) profileFormFromRequest(request *http.Request) frontend.ProfileFormData {
	return frontend.ProfileFormData{
		CSRFToken:           server.csrf,
		DraftID:             firstNonEmpty(request.FormValue("draft_id"), randomURLToken()),
		Intent:              firstNonEmpty(request.FormValue("intent"), "existing"),
		Target:              firstNonEmpty(request.FormValue("target"), "full"),
		ProfileID:           strings.TrimSpace(request.FormValue("profile_id")),
		Name:                strings.TrimSpace(request.FormValue("name")),
		IP:                  strings.TrimSpace(request.FormValue("ip")),
		PrivateKeyPath:      expandUserPath(strings.TrimSpace(request.FormValue("private_key"))),
		BaseDomain:          strings.TrimSpace(request.FormValue("domain")),
		LetsEncryptEmail:    strings.TrimSpace(request.FormValue("email")),
		InitialSSHUser:      firstNonEmpty(strings.TrimSpace(request.FormValue("initial_ssh_user")), "root"),
		AdminUser:           firstNonEmpty(strings.TrimSpace(request.FormValue("admin_user")), "servestead"),
		PangolinAdminEmail:  strings.TrimSpace(request.FormValue("pangolin_admin_email")),
		PangolinAdminStatus: "masked",
		ConfigRepository:    expandUserPath(strings.TrimSpace(request.FormValue("config_repo"))),
		GitHubRepositoryURL: strings.TrimSpace(request.FormValue("github_repo")),
	}
}

func setupOptionsFromProfileForm(data frontend.ProfileFormData) setupCLIOptions {
	return setupCLIOptions{
		IP:                   data.IP,
		ProfileID:            data.ProfileID,
		Name:                 data.Name,
		InitialSSHUser:       firstNonEmpty(data.InitialSSHUser, "root"),
		AdminUser:            firstNonEmpty(data.AdminUser, "servestead"),
		PrivateKeyPath:       data.PrivateKeyPath,
		BaseDomain:           data.BaseDomain,
		LetsEncryptEmail:     data.LetsEncryptEmail,
		PangolinAdminEmail:   data.PangolinAdminEmail,
		ConfigRepositoryPath: data.ConfigRepository,
		GitHubRepositoryURL:  data.GitHubRepositoryURL,
	}
}

func (server *webServer) saveGitHubToken(profileID string, value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	token, err := normalizeGitHubToken(value)
	if err != nil {
		return err
	}
	secrets, err := server.store.LoadSecrets(profileID)
	if err != nil {
		return err
	}
	secrets.GitHubToken = token
	return server.store.SaveSecrets(profileID, secrets)
}

func (server *webServer) saveDraftPassword(draftID string, value string) {
	if draftID == "" || strings.TrimSpace(value) == "" {
		return
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	draft := server.drafts[draftID]
	draft.PangolinAdminPassword = strings.TrimSpace(value)
	server.drafts[draftID] = draft
}

func (server *webServer) saveDraftGitHubToken(draftID string, value string) error {
	if draftID == "" || strings.TrimSpace(value) == "" {
		return nil
	}
	token, err := normalizeGitHubToken(value)
	if err != nil {
		return err
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	draft := server.drafts[draftID]
	draft.GitHubToken = token
	server.drafts[draftID] = draft
	return nil
}

func (server *webServer) saveDraftSecrets(profileID string, draftID string) error {
	if profileID == "" || draftID == "" {
		return nil
	}
	server.mu.Lock()
	draft := server.drafts[draftID]
	delete(server.drafts, draftID)
	server.mu.Unlock()
	if draft.PangolinAdminPassword == "" && draft.GitHubToken == "" {
		return nil
	}
	secrets, err := server.store.LoadSecrets(profileID)
	if err != nil {
		return err
	}
	if draft.PangolinAdminPassword != "" {
		secrets.PangolinAdminPassword = draft.PangolinAdminPassword
	}
	if draft.GitHubToken != "" {
		secrets.GitHubToken = draft.GitHubToken
	}
	return server.store.SaveSecrets(profileID, secrets)
}

type webRunRequest struct {
	Profile Profile
	State   ProfileState
	Config  setupConfig
	Stage   string
}

type webRunHistory struct {
	ProfileID string
	Stage     string
}

type webRun struct {
	runID     string
	profileID string
	cancel    context.CancelFunc
}

type webRunManager struct {
	store        ProfileStore
	broker       *webEventBroker
	active       map[string]*webRun
	history      map[string]webRunHistory
	runFunc      func(context.Context, webRunRequest, string)
	recoveryHTML func(runID, profileID, kind, message string) string
	mu           sync.Mutex
}

func newWebRunManager(store ProfileStore, broker *webEventBroker) *webRunManager {
	manager := &webRunManager{
		store:   store,
		broker:  broker,
		active:  map[string]*webRun{},
		history: map[string]webRunHistory{},
	}
	manager.runFunc = manager.run
	return manager
}

func (manager *webRunManager) Start(_ context.Context, request webRunRequest) (string, error) {
	manager.mu.Lock()
	if active := manager.active[request.Profile.ID]; active != nil {
		manager.mu.Unlock()
		return "", fmt.Errorf("profile %s already has an active run", request.Profile.ID)
	}
	runID := newSetupRunID()
	completed := completedSetupStages(request.State)
	request.State.ActiveRunID = runID
	if request.Stage == "" {
		request.State.Runs[runID] = newSetupRun(runID, completed)
	} else {
		request.State.Runs[runID] = newSetupRunForStage(runID, request.Stage, completed)
	}
	if err := manager.store.Save(request.Profile, request.State); err != nil {
		manager.mu.Unlock()
		return "", err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	manager.active[request.Profile.ID] = &webRun{runID: runID, profileID: request.Profile.ID, cancel: cancel}
	manager.history[runID] = webRunHistory{ProfileID: request.Profile.ID, Stage: request.Stage}
	manager.mu.Unlock()

	manager.broker.Emit(webEvent{Type: "status", RunID: runID, Status: runStatusRunning, Line: "Run queued."})
	go manager.runFunc(runCtx, request, runID)
	return runID, nil
}

func (manager *webRunManager) Cancel(runID string) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	for _, active := range manager.active {
		if active.runID == runID {
			active.cancel()
			return true
		}
	}
	return false
}

func (manager *webRunManager) Retry(ctx context.Context, runID string) (string, string, error) {
	manager.mu.Lock()
	history, ok := manager.history[runID]
	manager.mu.Unlock()
	if !ok {
		return "", "", fmt.Errorf("run %s cannot be retried from this UI session", runID)
	}
	var (
		profile Profile
		state   ProfileState
		config  setupConfig
		err     error
	)
	if history.Stage == "" {
		profile, state, config, err = prepareProfileSetup(setupCLIOptions{ProfileID: history.ProfileID, Yes: true}, manager.store, io.Discard)
	} else {
		profile, state, config, err = prepareProfileStageSetup(setupCLIOptions{ProfileID: history.ProfileID}, manager.store, history.Stage)
	}
	if err != nil {
		return "", history.ProfileID, err
	}
	nextRunID, err := manager.Start(ctx, webRunRequest{Profile: profile, State: state, Config: config, Stage: history.Stage})
	return nextRunID, history.ProfileID, err
}

func (manager *webRunManager) run(ctx context.Context, request webRunRequest, runID string) {
	defer manager.finishActive(request.Profile.ID)
	config := request.Config
	profile := request.Profile
	state := request.State
	preparation := manager.broker.LineWriter(runID, "preparation", "stdout")
	fmt.Fprintln(preparation, "Running local preflight checks.")
	if err := runPreflight(config, preparation); err != nil {
		if ctx.Err() != nil {
			manager.cancelRun(profile, &state, runID)
			return
		}
		manager.fail(profile, &state, runID, err)
		return
	}
	if request.Stage == "" || stageUsesRepository(request.Stage) {
		fmt.Fprintln(preparation, "Preparing the configuration repository before SSH execution.")
		var err error
		profile, config, err = prepareDeclarativeSetup(ctx, manager.store, profile, state, config)
		if err != nil {
			if ctx.Err() != nil {
				manager.cancelRun(profile, &state, runID)
				return
			}
			manager.fail(profile, &state, runID, err)
			return
		}
		fmt.Fprintf(preparation, "Configuration repository ready: %s at %s\n", config.ConfigRepositoryPath, config.ConfigRepositoryCommit)
	}
	profileReporter := &profileRunReporter{store: manager.store, profile: profile, state: &state, runID: runID}
	reporter := &synchronizedTaskReporter{reporters: []TaskReporter{profileReporter, manager.broker}}
	output := profileRunOutput{reporter: reporter, runID: runID}
	stageRun := setupStageRun{profile: profile, config: config, runID: runID, reporter: reporter, stdout: output, stderr: output}
	var err error
	if request.Stage == "" {
		err = runFullSetupStages(ctx, stageRun, completedSetupStages(state))
	} else {
		err = runSetupStage(ctx, stageRun, request.Stage)
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			profileReporter.finishRun(runStatusCancelled)
			manager.emitTerminalStatus(runID, runStatusCancelled, "Run cancelled.")
			return
		}
		profileReporter.finishRun(runStatusFailed)
		manager.broker.Emit(webEvent{Type: "status", RunID: runID, Status: runStatusFailed, Line: "Run failed: " + err.Error()})
		manager.recover(runID, profile.ID, err)
		manager.broker.Emit(webEvent{Type: "done", RunID: runID, Status: runStatusFailed})
		return
	}
	if request.Stage == "stacks" {
		state.StackRepositoryCommit = config.ConfigRepositoryCommit
	}
	profileReporter.finishRun(runStatusComplete)
	manager.emitTerminalStatus(runID, runStatusComplete, "Run complete.")
}

func (manager *webRunManager) finishActive(profileID string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	delete(manager.active, profileID)
}

func (manager *webRunManager) fail(profile Profile, state *ProfileState, runID string, err error) {
	if run, ok := state.Runs[runID]; ok {
		run.Status = runStatusFailed
		run.UpdatedAt = time.Now().UTC()
		state.Runs[runID] = run
		_ = manager.store.Save(profile, *state)
	}
	manager.broker.Emit(webEvent{Type: "log", RunID: runID, Line: "Run failed: " + err.Error()})
	manager.broker.Emit(webEvent{Type: "status", RunID: runID, Status: runStatusFailed, Line: "Run failed: " + err.Error()})
	manager.recover(runID, profile.ID, err)
	manager.broker.Emit(webEvent{Type: "done", RunID: runID, Status: runStatusFailed})
}

func (manager *webRunManager) cancelRun(profile Profile, state *ProfileState, runID string) {
	if run, ok := state.Runs[runID]; ok {
		run.Status = runStatusCancelled
		run.UpdatedAt = time.Now().UTC()
		state.Runs[runID] = run
		_ = manager.store.Save(profile, *state)
	}
	manager.emitTerminalStatus(runID, runStatusCancelled, "Run cancelled.")
}

func (manager *webRunManager) emitTerminalStatus(runID, status, line string) {
	manager.broker.Emit(webEvent{Type: "status", RunID: runID, Status: status, Line: line})
	if line != "" {
		manager.broker.Emit(webEvent{Type: "log", RunID: runID, Line: line})
	}
	manager.broker.Emit(webEvent{Type: "done", RunID: runID, Status: status})
}

func (manager *webRunManager) recover(runID, profileID string, err error) {
	kind, message := classifyWebRecovery(err)
	event := webEvent{Type: "recovery", RunID: runID, ProfileID: profileID, Kind: kind, Message: message, Error: err.Error()}
	if manager.recoveryHTML != nil {
		event.HTML = manager.recoveryHTML(runID, profileID, kind, message)
	}
	manager.broker.Emit(event)
}

func classifyWebRecovery(err error) (string, string) {
	text := err.Error()
	lower := strings.ToLower(text)
	switch {
	case errors.Is(err, errRepositoryReviewRequired) || strings.Contains(lower, "uncommitted") || strings.Contains(lower, "review and commit"):
		return "dirty_repository", "Review and commit the configuration repository changes, then retry."
	case strings.Contains(lower, "credential") || strings.Contains(lower, "pangolin administrator") || strings.Contains(lower, "github token"):
		return "missing_credentials", "Save the missing credentials, then retry."
	case strings.Contains(lower, "domain") || strings.Contains(lower, "email"):
		return "bad_profile_values", "Fix the domain or email values, then retry."
	default:
		return "run_failed", text
	}
}

type webEvent struct {
	ID        int       `json:"-"`
	Type      string    `json:"type"`
	RunID     string    `json:"run_id"`
	ProfileID string    `json:"profile_id,omitempty"`
	Status    string    `json:"status,omitempty"`
	Stage     string    `json:"stage,omitempty"`
	TaskName  string    `json:"task_name,omitempty"`
	TaskIndex int       `json:"task_index,omitempty"`
	TaskTotal int       `json:"task_total,omitempty"`
	Stream    string    `json:"stream,omitempty"`
	Line      string    `json:"line,omitempty"`
	Error     string    `json:"error,omitempty"`
	Kind      string    `json:"kind,omitempty"`
	Message   string    `json:"message,omitempty"`
	HTML      string    `json:"html,omitempty"`
	Time      time.Time `json:"time"`
}

type webEventBroker struct {
	mu          sync.Mutex
	nextID      int
	buffer      map[string][]webEvent
	subscribers map[string]map[chan webEvent]bool
}

func newWebEventBroker() *webEventBroker {
	return &webEventBroker{buffer: map[string][]webEvent{}, subscribers: map[string]map[chan webEvent]bool{}}
}

func (broker *webEventBroker) Report(event TaskEvent) {
	web := webEvent{
		Type:      "status",
		RunID:     event.RunID,
		Stage:     event.Stage,
		TaskName:  event.TaskName,
		TaskIndex: event.TaskIndex,
		TaskTotal: event.TaskTotal,
		Stream:    event.Stream,
		Line:      event.Line,
		Error:     event.Error,
		Time:      event.Time,
	}
	switch event.Type {
	case TaskLogLine:
		web.Type = "log"
	case TaskFailed:
		web.Status = runStatusFailed
	case TaskRunCompleted:
		web.Status = runStatusComplete
	default:
		web.Status = runStatusRunning
	}
	broker.Emit(web)
}

func (broker *webEventBroker) Emit(event webEvent) {
	broker.mu.Lock()
	broker.nextID++
	event.ID = broker.nextID
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	buffer := append(broker.buffer[event.RunID], event)
	if len(buffer) > 500 {
		buffer = buffer[len(buffer)-500:]
	}
	broker.buffer[event.RunID] = buffer
	for subscriber := range broker.subscribers[event.RunID] {
		select {
		case subscriber <- event:
		default:
		}
	}
	broker.mu.Unlock()
}

func (broker *webEventBroker) Subscribe(runID string, lastID int) (<-chan webEvent, func()) {
	broker.mu.Lock()
	replay := make([]webEvent, 0, len(broker.buffer[runID]))
	for _, event := range broker.buffer[runID] {
		if event.ID > lastID {
			replay = append(replay, event)
		}
	}
	channel := make(chan webEvent, len(replay)+128)
	for _, event := range replay {
		channel <- event
	}
	if broker.subscribers[runID] == nil {
		broker.subscribers[runID] = map[chan webEvent]bool{}
	}
	broker.subscribers[runID][channel] = true
	broker.mu.Unlock()
	return channel, func() {
		broker.mu.Lock()
		delete(broker.subscribers[runID], channel)
		close(channel)
		broker.mu.Unlock()
	}
}

func (broker *webEventBroker) LineWriter(runID string, stage string, stream string) io.Writer {
	return &webEventWriter{broker: broker, runID: runID, stage: stage, stream: stream}
}

type webEventWriter struct {
	broker  *webEventBroker
	runID   string
	stage   string
	stream  string
	partial string
}

func (writer *webEventWriter) Write(data []byte) (int, error) {
	text := writer.partial + string(data)
	lines := strings.Split(text, "\n")
	writer.partial = lines[len(lines)-1]
	for _, line := range lines[:len(lines)-1] {
		if line != "" {
			writer.broker.Emit(webEvent{Type: "log", RunID: writer.runID, Stage: writer.stage, Stream: writer.stream, Line: line})
		}
	}
	return len(data), nil
}

func randomURLToken() string {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return base64.RawURLEncoding.EncodeToString(data)
}
