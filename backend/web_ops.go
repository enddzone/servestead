package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"servestead/frontend"
)

const opsGitTimeout = 3 * time.Second

var newWebCloudProvider = func(token string) cloudProvider {
	return newDigitalOceanProvider(token)
}

func (server *webServer) handleOpsProfiles(response http.ResponseWriter, request *http.Request) {
	if !requireMethod(response, request, http.MethodGet) {
		return
	}
	server.renderOpsShell(response, request, frontend.OpsProfilesPanel(server.opsProfilesData(request.Context(), request.URL.Query().Get("profile"), "", "")))
}

func (server *webServer) handleOpsProfile(response http.ResponseWriter, request *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(request.URL.Path, opsProfilePathPrefix), "/"), "/")
	if len(parts) < 2 || parts[0] == "" {
		http.NotFound(response, request)
		return
	}
	profileID := parts[0]
	if len(parts) == 2 && parts[1] == "delete" {
		server.handleOpsProfileDelete(response, request, profileID)
		return
	}
	if request.Method == http.MethodGet && len(parts) == 2 && parts[1] == "drawer" {
		server.render(response, request, frontend.OpsProfileDrawer(server.opsProfileDrawerData(request.Context(), profileID, "", "")))
		return
	}
	if request.Method == http.MethodGet && len(parts) == 2 && parts[1] == "diagnostics" {
		server.render(response, request, frontend.OpsDiagnosticsDrawer(server.opsDiagnosticsDrawerData(request.Context(), profileID, "", "")))
		return
	}
	switch parts[1] {
	case "stacks":
		server.handleOpsStackRoute(response, request, profileID, parts[2:])
	case "gitops":
		server.handleOpsGitOpsRoute(response, request, profileID, parts[2:])
	case "runs":
		server.handleOpsRunsRoute(response, request, profileID, parts[2:])
	case "access":
		server.handleOpsAccessRoute(response, request, profileID, parts[2:])
	case "cloud":
		server.handleOpsCloudRoute(response, request, profileID, parts[2:])
	default:
		http.NotFound(response, request)
	}
}

func (server *webServer) handleOpsProfileDelete(response http.ResponseWriter, request *http.Request, profileID string) {
	if !requireMethod(response, request, http.MethodPost) {
		return
	}
	profile, _, err := server.store.Load(profileID)
	if err != nil {
		server.renderOpsShell(response, request, frontend.OpsProfilesPanel(server.opsProfilesData(request.Context(), "", "", err.Error())))
		return
	}
	expected := "delete " + firstNonEmpty(profile.Name, profile.IP, profile.ID)
	if strings.TrimSpace(request.FormValue("confirm")) != expected {
		message := fmt.Sprintf("Type %q to confirm local profile deletion.", expected)
		server.renderOpsShell(response, request, frontend.OpsProfilesPanel(server.opsProfilesData(request.Context(), profile.ID, "", message)))
		return
	}
	server.manager.mu.Lock()
	activeRun := server.manager.active[profile.ID] != nil
	server.manager.mu.Unlock()
	if activeRun {
		message := "Cancel or wait for the active run before deleting this profile."
		server.renderOpsShell(response, request, frontend.OpsProfilesPanel(server.opsProfilesData(request.Context(), profile.ID, "", message)))
		return
	}
	if err := server.store.Delete(profile.ID); err != nil {
		server.renderOpsShell(response, request, frontend.OpsProfilesPanel(server.opsProfilesData(request.Context(), profile.ID, "", err.Error())))
		return
	}
	http.Redirect(response, request, "/ops/profiles", http.StatusSeeOther)
}

func (server *webServer) handleOpsStackRoute(response http.ResponseWriter, request *http.Request, profileID string, parts []string) {
	switch {
	case request.Method == http.MethodGet && len(parts) == 0:
		server.render(response, request, frontend.OpsStacksPanel(server.opsStacksData(request.Context(), profileID, "", "")))
	case request.Method == http.MethodGet && len(parts) == 1 && parts[0] == "new":
		server.render(response, request, frontend.OpsStackEditorPanel(server.opsStackEditorData(profileID, "", "", "")))
	case request.Method == http.MethodGet && len(parts) == 2 && parts[1] == "edit":
		server.render(response, request, frontend.OpsStackEditorPanel(server.opsStackEditorData(profileID, parts[0], "", "")))
	case request.Method == http.MethodPost && len(parts) == 1 && parts[0] == "save":
		server.handleOpsStackSave(response, request, profileID)
	case request.Method == http.MethodPost && len(parts) == 2 && parts[1] == "remove":
		server.handleOpsStackRemove(response, request, profileID, parts[0])
	default:
		http.NotFound(response, request)
	}
}

func (server *webServer) handleOpsGitOpsRoute(response http.ResponseWriter, request *http.Request, profileID string, parts []string) {
	switch {
	case request.Method == http.MethodGet && len(parts) == 0:
		server.render(response, request, frontend.OpsGitOpsPanel(server.opsGitOpsData(request.Context(), profileID, "", "")))
	case request.Method == http.MethodGet && len(parts) == 1 && parts[0] == "review":
		server.renderAppShell(response, request, "Servestead GitOps Review", "GitOps Review", "gitops", profileID, frontend.OpsGitOpsReviewPanel(server.opsGitOpsReviewData(request.Context(), profileID, "", "")))
	case request.Method == http.MethodPost && len(parts) == 1:
		server.handleOpsGitOpsAction(response, request, profileID, parts[0])
	default:
		http.NotFound(response, request)
	}
}

func (server *webServer) handleOpsRunsRoute(response http.ResponseWriter, request *http.Request, profileID string, parts []string) {
	switch {
	case request.Method == http.MethodPost && len(parts) == 1 && parts[0] == "stage":
		server.handleOpsRunStage(response, request, profileID)
	case request.Method == http.MethodGet && len(parts) == 0:
		server.render(response, request, frontend.OpsRunsPanel(server.opsRunsData(profileID, request.URL.Query().Get("q"), "", "")))
	case request.Method == http.MethodGet && len(parts) == 1:
		server.render(response, request, frontend.OpsRunDetailPanel(server.opsRunDetailData(profileID, parts[0], "", "")))
	default:
		http.NotFound(response, request)
	}
}

func (server *webServer) handleOpsAccessRoute(response http.ResponseWriter, request *http.Request, profileID string, parts []string) {
	switch {
	case request.Method == http.MethodGet && len(parts) == 0:
		server.render(response, request, frontend.OpsAccessPanel(server.opsAccessData(profileID, "", "")))
	case request.Method == http.MethodPost && len(parts) == 1:
		server.handleOpsAccessAction(response, request, profileID, parts[0])
	default:
		http.NotFound(response, request)
	}
}

func (server *webServer) handleOpsCloudRoute(response http.ResponseWriter, request *http.Request, profileID string, parts []string) {
	switch {
	case request.Method == http.MethodGet && len(parts) == 0:
		server.render(response, request, frontend.OpsCloudPanel(server.opsCloudData(profileID, "", "")))
	case request.Method == http.MethodPost && len(parts) == 1:
		server.handleOpsCloudAction(response, request, profileID, parts[0])
	default:
		http.NotFound(response, request)
	}
}

func (server *webServer) opsProfilesData(ctx context.Context, selected string, notice string, errorText string) frontend.OpsProfilesData {
	summaries, err := server.store.List()
	selected = server.defaultProfileID(selected)
	data := frontend.OpsProfilesData{CSRFToken: server.csrf, Selected: selected, Notice: notice, Error: errorText}
	if err != nil {
		data.Error = err.Error()
		return data
	}
	for _, summary := range summaries {
		profile, state, err := server.store.Load(summary.ID)
		if err != nil {
			data.Rows = append(data.Rows, frontend.OpsProfileRow{
				ID:         summary.ID,
				Name:       firstNonEmpty(summary.Name, summary.ID),
				IP:         summary.IP,
				GitState:   "unavailable",
				CloudState: "unknown",
				NextAction: err.Error(),
				UpdatedAt:  formatWebTime(summary.UpdatedAt),
			})
			continue
		}
		gitState := server.opsGitSummary(ctx, profile.ConfigRepositoryPath)
		activeRunStatus := server.opsActiveRunStatus(profile.ID, state)
		setupStatus := opsSetupStatus(state)
		cloudState := opsCloudState(profile)
		data.Rows = append(data.Rows, frontend.OpsProfileRow{
			ID:              profile.ID,
			Name:            firstNonEmpty(profile.Name, profile.IP, profile.ID),
			IP:              profile.IP,
			BaseDomain:      firstNonEmpty(profile.BaseDomain, notConfiguredLabel),
			ActiveRunStatus: activeRunStatus,
			SetupStatus:     setupStatus,
			GitState:        gitState,
			CloudState:      cloudState,
			NextAction:      opsNextAction(profile, activeRunStatus, setupStatus, gitState, cloudState),
			UpdatedAt:       formatWebTime(profile.UpdatedAt),
		})
	}
	if selected != "" {
		data.SelectedPanel = server.opsProfileDrawerData(ctx, selected, "", "")
		data.HasSelected = true
	}
	return data
}

func (server *webServer) opsProfileDrawerData(ctx context.Context, profileID string, notice string, errorText string) frontend.OpsProfileDrawerData {
	profile, state, err := server.store.Load(profileID)
	if err != nil {
		return frontend.OpsProfileDrawerData{CSRFToken: server.csrf, ProfileID: profileID, Error: err.Error()}
	}
	gitState := server.opsGitSummary(ctx, profile.ConfigRepositoryPath)
	activeRunStatus := server.opsActiveRunStatus(profile.ID, state)
	setupStatus := opsSetupStatus(state)
	cloudState := opsCloudState(profile)
	return frontend.OpsProfileDrawerData{
		CSRFToken:        server.csrf,
		ProfileID:        profile.ID,
		Name:             firstNonEmpty(profile.Name, profile.IP, profile.ID),
		IP:               firstNonEmpty(profile.IP, notConfiguredLabel),
		BaseDomain:       firstNonEmpty(profile.BaseDomain, notConfiguredLabel),
		LetsEncryptEmail: firstNonEmpty(profile.LetsEncryptEmail, notConfiguredLabel),
		RepositoryPath:   firstNonEmpty(profile.ConfigRepositoryPath, notConfiguredLabel),
		ActiveRunStatus:  activeRunStatus,
		SetupStatus:      setupStatus,
		GitState:         gitState,
		CloudState:       cloudState,
		NextAction:       opsNextAction(profile, activeRunStatus, setupStatus, gitState, cloudState),
		UpdatedAt:        formatWebTime(profile.UpdatedAt),
		RecentRuns:       server.opsRecentRunRows(profile.ID, 5),
		Notice:           notice,
		Error:            errorText,
	}
}

func (server *webServer) opsDiagnosticsDrawerData(ctx context.Context, profileID string, notice string, errorText string) frontend.OpsDiagnosticsDrawerData {
	profile, _, err := server.store.Load(profileID)
	data := frontend.OpsDiagnosticsDrawerData{
		CSRFToken: server.csrf,
		ProfileID: profileID,
		Runs:      server.opsRecentRunRows(profileID, 5),
		GitOps:    server.opsGitOpsData(ctx, profileID, "", ""),
		Cloud:     server.opsCloudData(profileID, "", ""),
		Notice:    notice,
		Error:     errorText,
	}
	if err != nil {
		data.Error = firstNonEmpty(data.Error, err.Error())
		return data
	}
	data.Name = firstNonEmpty(profile.Name, profile.IP, profile.ID)
	return data
}

func (server *webServer) opsStacksData(ctx context.Context, profileID string, notice string, errorText string) frontend.OpsStacksData {
	profile, _, err := server.store.Load(profileID)
	data := frontend.OpsStacksData{CSRFToken: server.csrf, ProfileID: profileID, Notice: notice, Error: errorText}
	if err != nil {
		data.Error = err.Error()
		return data
	}
	data.RepositoryPath = profile.ConfigRepositoryPath
	if profile.ConfigRepositoryPath == "" {
		data.Error = firstNonEmpty(data.Error, "profile has no configuration repository")
		return data
	}
	gitState := server.opsGitSummary(ctx, profile.ConfigRepositoryPath)
	stacks, err := loadEditableStacks(profile.ConfigRepositoryPath)
	if err != nil {
		data.Error = firstNonEmpty(data.Error, err.Error())
		return data
	}
	for _, stack := range stacks {
		metadata := "ready"
		if stack.MetadataMissing {
			metadata = "metadata missing"
		}
		data.Rows = append(data.Rows, frontend.OpsStackRow{
			Name:                stack.Name,
			PublicResourceCount: len(stack.Metadata.PublicResources),
			MetadataStatus:      metadata,
			GitState:            gitState,
			Eligible:            !stack.MetadataMissing && gitState == "clean",
		})
	}
	return data
}

func (server *webServer) opsStackEditorData(profileID string, stackName string, notice string, errorText string) frontend.OpsStackEditorData {
	profile, _, err := server.store.Load(profileID)
	data := frontend.OpsStackEditorData{CSRFToken: server.csrf, ProfileID: profileID, Notice: notice, Error: errorText}
	if err != nil {
		data.Error = err.Error()
		return data
	}
	data.RepositoryPath = profile.ConfigRepositoryPath
	if stackName == "" {
		data.Compose = defaultWebStackCompose()
		data.Resources = []frontend.OpsStackResourceData{defaultWebStackResource()}
		return data
	}
	stacks, err := loadEditableStacks(profile.ConfigRepositoryPath)
	if err != nil {
		data.Error = firstNonEmpty(data.Error, err.Error())
		return data
	}
	for _, stack := range stacks {
		if stack.Name != stackName {
			continue
		}
		data.OriginalName = stack.Name
		data.Name = stack.Name
		data.Compose = stack.Compose
		data.Resources = webStackResourceData(stack.Metadata.PublicResources)
		if len(data.Resources) == 0 {
			data.Resources = []frontend.OpsStackResourceData{{Protocol: "http", SSO: true, HealthPath: "/"}}
		}
		return data
	}
	data.Error = firstNonEmpty(data.Error, "stack not found")
	return data
}

func (server *webServer) handleOpsStackSave(response http.ResponseWriter, request *http.Request, profileID string) {
	if !requireMethod(response, request, http.MethodPost) {
		return
	}
	data := server.opsStackEditorFromRequest(profileID, request)
	resources, err := parseWebStackResources(request)
	if err != nil {
		data.Error = err.Error()
		server.render(response, request, frontend.OpsStackEditorPanel(data))
		return
	}
	savedName, err := server.saveWebStack(profileID, data.OriginalName, data.Name, data.Compose, resources)
	if err != nil {
		data.Error = err.Error()
		server.render(response, request, frontend.OpsStackEditorPanel(data))
		return
	}
	server.render(response, request, frontend.OpsStacksPanel(server.opsStacksData(request.Context(), profileID, "Stack "+savedName+" saved. Stage and commit the repository changes before deploying.", "")))
}

func (server *webServer) handleOpsStackRemove(response http.ResponseWriter, request *http.Request, profileID string, stackName string) {
	if !requireMethod(response, request, http.MethodPost) {
		return
	}
	if strings.TrimSpace(request.FormValue("confirm")) != stackName {
		server.render(response, request, frontend.OpsStacksPanel(server.opsStacksData(request.Context(), profileID, "", "type "+stackName+" to confirm removal")))
		return
	}
	if err := server.removeWebStack(profileID, stackName); err != nil {
		server.render(response, request, frontend.OpsStacksPanel(server.opsStacksData(request.Context(), profileID, "", err.Error())))
		return
	}
	server.render(response, request, frontend.OpsStacksPanel(server.opsStacksData(request.Context(), profileID, "Stack "+stackName+" removed. Stage and commit the repository changes to publish the removal.", "")))
}

func (server *webServer) opsStackEditorFromRequest(profileID string, request *http.Request) frontend.OpsStackEditorData {
	profile, _, _ := server.store.Load(profileID)
	_ = request.ParseForm()
	return frontend.OpsStackEditorData{
		CSRFToken:      server.csrf,
		ProfileID:      profileID,
		RepositoryPath: profile.ConfigRepositoryPath,
		OriginalName:   strings.TrimSpace(request.FormValue("original_name")),
		Name:           strings.TrimSpace(request.FormValue("name")),
		Compose:        request.FormValue("compose"),
		Resources:      webStackResourceFormData(request),
	}
}

func (server *webServer) saveWebStack(profileID string, originalName string, name string, composeText string, resources []stackPublicResource) (string, error) {
	profile, _, err := server.store.Load(profileID)
	if err != nil {
		return "", err
	}
	repositoryPath := profile.ConfigRepositoryPath
	if repositoryPath == "" {
		return "", errors.New("profile has no configuration repository")
	}
	name = strings.TrimSpace(name)
	originalName = strings.TrimSpace(originalName)
	if name == "" {
		return "", errors.New("stack name is required")
	}
	if name == "observability" || originalName == "observability" {
		return "", errors.New("observability is managed by Servestead and cannot be edited as a custom stack")
	}
	compose := []byte(strings.TrimSpace(composeText) + "\n")
	if strings.TrimSpace(composeText) == "" {
		return "", errors.New("compose.yaml is required")
	}
	services, err := inspectComposeServices(compose)
	if err != nil {
		return "", err
	}
	secrets, err := server.existingStackSecrets(repositoryPath, originalName)
	if err != nil {
		return "", err
	}
	if secrets.HasSecrets() && originalName != "" && originalName != name {
		secrets.Source = defaultStackSecretSource(name)
	}
	metadata := stackMetadata{Version: 1, PublicResources: resources, Secrets: secrets}
	if err := validateStackMetadata(name, metadata, services); err != nil {
		return "", err
	}
	stacksDirectory := filepath.Join(expandUserPath(repositoryPath), "stacks")
	if _, err := prepareEditableStackDestination(stacksDirectory, originalName, name); err != nil {
		return "", err
	}
	if err := writeEditableStackFiles(stacksDirectory, name, metadata, compose); err != nil {
		return "", err
	}
	return name, nil
}

func (server *webServer) removeWebStack(profileID string, name string) error {
	profile, _, err := server.store.Load(profileID)
	if err != nil {
		return err
	}
	if profile.ConfigRepositoryPath == "" {
		return errors.New("profile has no configuration repository")
	}
	name = strings.TrimSpace(name)
	if !stackSlugPattern.MatchString(name) || name == "observability" {
		return errors.New("stack name must be a lowercase DNS label")
	}
	directory := filepath.Join(expandUserPath(profile.ConfigRepositoryPath), "stacks", name)
	if _, err := os.Stat(directory); err != nil {
		return fmt.Errorf("stack %q is not present: %w", name, err)
	}
	return os.RemoveAll(directory)
}

func (server *webServer) existingStackSecrets(repositoryPath string, originalName string) (stackSecretMetadata, error) {
	if strings.TrimSpace(originalName) == "" {
		return stackSecretMetadata{}, nil
	}
	stacks, err := loadEditableStacks(repositoryPath)
	if err != nil {
		return stackSecretMetadata{}, err
	}
	for _, stack := range stacks {
		if stack.Name == originalName {
			return stack.Metadata.Secrets, nil
		}
	}
	return stackSecretMetadata{}, nil
}

func parseWebStackResources(request *http.Request) ([]stackPublicResource, error) {
	_ = request.ParseForm()
	rows := webStackResourceFormData(request)
	resources := make([]stackPublicResource, 0, len(rows))
	for _, row := range rows {
		if webStackResourceBlank(row) {
			continue
		}
		port, err := strconv.Atoi(strings.TrimSpace(row.Port))
		if err != nil {
			return nil, fmt.Errorf("resource %q port must be a number", firstNonEmpty(row.ID, row.Service, row.Subdomain, "new"))
		}
		path := strings.TrimSpace(row.HealthPath)
		resources = append(resources, stackPublicResource{
			ID:        strings.TrimSpace(row.ID),
			Service:   strings.TrimSpace(row.Service),
			Name:      strings.TrimSpace(row.Name),
			Subdomain: strings.TrimSpace(row.Subdomain),
			Port:      port,
			Protocol:  strings.ToLower(firstNonEmpty(strings.TrimSpace(row.Protocol), "http")),
			SSO:       row.SSO,
			Healthcheck: stackResourceHealthcheck{
				Enabled: path != "",
				Path:    path,
			},
		})
	}
	return resources, nil
}

func webStackResourceFormData(request *http.Request) []frontend.OpsStackResourceData {
	_ = request.ParseForm()
	ids := request.PostForm["resource_id"]
	services := request.PostForm["resource_service"]
	subdomains := request.PostForm["resource_subdomain"]
	names := request.PostForm["resource_name"]
	ports := request.PostForm["resource_port"]
	protocols := request.PostForm["resource_protocol"]
	ssos := request.PostForm["resource_sso"]
	healthPaths := request.PostForm["resource_health_path"]
	count := maxWebFormRows(ids, services, subdomains, names, ports, protocols, ssos, healthPaths)
	rows := make([]frontend.OpsStackResourceData, 0, count)
	for index := range count {
		sso := formValueAt(ssos, index)
		rows = append(rows, frontend.OpsStackResourceData{
			ID:         strings.TrimSpace(formValueAt(ids, index)),
			Service:    strings.TrimSpace(formValueAt(services, index)),
			Subdomain:  strings.TrimSpace(formValueAt(subdomains, index)),
			Name:       strings.TrimSpace(formValueAt(names, index)),
			Port:       strings.TrimSpace(formValueAt(ports, index)),
			Protocol:   strings.ToLower(firstNonEmpty(strings.TrimSpace(formValueAt(protocols, index)), "http")),
			SSO:        sso == "" || sso == "yes",
			HealthPath: strings.TrimSpace(formValueAt(healthPaths, index)),
		})
	}
	if len(rows) == 0 {
		rows = []frontend.OpsStackResourceData{{Protocol: "http", SSO: true, HealthPath: "/"}}
	}
	return rows
}

func webStackResourceBlank(row frontend.OpsStackResourceData) bool {
	return row.ID == "" && row.Service == "" && row.Subdomain == "" && row.Name == "" && row.Port == "" && row.HealthPath == ""
}

func webStackResourceData(resources []stackPublicResource) []frontend.OpsStackResourceData {
	rows := make([]frontend.OpsStackResourceData, 0, len(resources))
	for _, resource := range resources {
		rows = append(rows, frontend.OpsStackResourceData{
			ID:         resource.ID,
			Service:    resource.Service,
			Subdomain:  resource.Subdomain,
			Name:       resource.Name,
			Port:       strconv.Itoa(resource.Port),
			Protocol:   firstNonEmpty(resource.Protocol, "http"),
			SSO:        resource.SSO,
			HealthPath: resource.Healthcheck.Path,
		})
	}
	return rows
}

func maxWebFormRows(values ...[]string) int {
	count := 0
	for _, value := range values {
		if len(value) > count {
			count = len(value)
		}
	}
	return count
}

func formValueAt(values []string, index int) string {
	if index < 0 || index >= len(values) {
		return ""
	}
	return values[index]
}

func defaultWebStackCompose() string {
	return `services:
  app:
    image: nginx:alpine
    expose:
      - "80"
`
}

func defaultWebStackResource() frontend.OpsStackResourceData {
	return frontend.OpsStackResourceData{
		ID: "app", Service: "app", Name: "App", Subdomain: "app",
		Port: "80", Protocol: "http", SSO: true, HealthPath: "/",
	}
}

func (server *webServer) opsGitOpsData(ctx context.Context, profileID string, notice string, errorText string) frontend.OpsGitOpsData {
	profile, _, err := server.store.Load(profileID)
	data := frontend.OpsGitOpsData{
		CSRFToken:  server.csrf,
		ProfileID:  profileID,
		State:      "unavailable",
		NextAction: "Resolve repository access before continuing.",
		Notice:     notice,
		Error:      errorText,
	}
	if err != nil {
		data.Error = err.Error()
		return data
	}
	data.RepositoryPath = profile.ConfigRepositoryPath
	if profile.ConfigRepositoryPath == "" {
		data.Error = firstNonEmpty(data.Error, "profile has no configuration repository")
		data.Diff = "No stack changes."
		data.State = notConfiguredLabel
		data.NextAction = "Configure a repository in Setup before managing stack changes."
		return data
	}
	gitCtx, cancel := context.WithTimeout(ctx, opsGitTimeout)
	defer cancel()
	status, err := stackRepositoryStatus(gitCtx, profile.ConfigRepositoryPath)
	if err != nil {
		data.Error = firstNonEmpty(data.Error, err.Error())
	} else {
		data.Status = status
	}
	head, err := stackRepositoryHead(gitCtx, profile.ConfigRepositoryPath)
	if err != nil {
		data.Head = "unavailable"
	} else {
		data.Head = head
		needsPush, err := stackRepositoryNeedsPush(gitCtx, profile.ConfigRepositoryPath, head)
		if err == nil {
			data.NeedsPush = needsPush
		}
	}
	diff, err := stackRepositoryDiff(gitCtx, profile.ConfigRepositoryPath)
	if err != nil {
		data.Error = firstNonEmpty(data.Error, err.Error())
		data.Diff = "Diff unavailable."
	} else {
		data.Diff = diff
	}
	data.State = opsGitWorkingTreeState(data.Status)
	data.NextAction = opsGitNextAction(data.State, data.NeedsPush, data.Error)
	return data
}

func (server *webServer) opsGitOpsReviewData(ctx context.Context, profileID string, notice string, errorText string) frontend.OpsGitOpsReviewData {
	profile, _, err := server.store.Load(profileID)
	base := server.opsGitOpsData(ctx, profileID, notice, errorText)
	data := frontend.OpsGitOpsReviewData{
		CSRFToken:      server.csrf,
		ProfileID:      profileID,
		RepositoryPath: base.RepositoryPath,
		Status:         firstNonEmpty(base.Status, "unavailable"),
		State:          base.State,
		Head:           firstNonEmpty(base.Head, "unavailable"),
		NeedsPush:      base.NeedsPush,
		NextAction:     base.NextAction,
		Diff:           base.Diff,
		Notice:         base.Notice,
		Error:          base.Error,
	}
	if err != nil {
		data.Error = firstNonEmpty(data.Error, err.Error())
		return data
	}
	data.ProfileName = firstNonEmpty(profile.Name, profile.IP, profile.ID)
	data.BaseDomain = profile.BaseDomain
	data.Commits = server.opsRecentCommits(ctx, profile.ConfigRepositoryPath, 5)
	data.Runs = server.opsRecentRunRows(profileID, 5)
	return data
}

func (server *webServer) opsRecentCommits(ctx context.Context, repositoryPath string, limit int) []frontend.OpsCommitRow {
	if strings.TrimSpace(repositoryPath) == "" {
		return nil
	}
	gitCtx, cancel := context.WithTimeout(ctx, opsGitTimeout)
	defer cancel()
	output, err := runGit(gitCtx, expandUserPath(repositoryPath), nil, "log", "-n", strconv.Itoa(limit), "--pretty=format:%h%x1f%s%x1f%ci")
	if err != nil {
		return nil
	}
	rows := []frontend.OpsCommitRow{}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\x1f", 3)
		row := frontend.OpsCommitRow{Hash: parts[0]}
		if len(parts) > 1 {
			row.Message = parts[1]
		}
		if len(parts) > 2 {
			row.When = parts[2]
		}
		rows = append(rows, row)
	}
	return rows
}

func opsGitWorkingTreeState(status string) string {
	if status == "clean" {
		return "clean"
	}
	if strings.TrimSpace(status) == "" {
		return "unavailable"
	}
	return gitChangesPending
}

func opsGitNextAction(state string, needsPush bool, errorText string) string {
	if errorText != "" {
		return "Resolve repository access before continuing."
	}
	if state == gitChangesPending {
		return "Review and stage the working tree changes."
	}
	if needsPush {
		return "Push the current commit before running the remote stack sync."
	}
	return "The repository is clean and ready for a stack run."
}

func ternaryString(condition bool, yes string, no string) string {
	if condition {
		return yes
	}
	return no
}

func (server *webServer) handleOpsGitOpsAction(response http.ResponseWriter, request *http.Request, profileID string, action string) {
	if !requireMethod(response, request, http.MethodPost) {
		return
	}
	renderAction := func(notice string, errorText string) {
		if request.FormValue("view") == "review" {
			server.render(response, request, frontend.OpsGitOpsReviewPanel(server.opsGitOpsReviewData(request.Context(), profileID, notice, errorText)))
			return
		}
		server.render(response, request, frontend.OpsGitOpsPanel(server.opsGitOpsData(request.Context(), profileID, notice, errorText)))
	}
	profile, _, err := server.store.Load(profileID)
	if err != nil {
		renderAction("", err.Error())
		return
	}
	gitCtx, cancel := context.WithTimeout(request.Context(), opsGitTimeout)
	defer cancel()
	switch action {
	case "stage":
		err = stageStackChanges(gitCtx, profile.ConfigRepositoryPath)
	case "commit":
		err = commitStackChanges(gitCtx, profile.ConfigRepositoryPath, request.FormValue("message"))
	case "push":
		err = pushStackRepository(gitCtx, profile.ConfigRepositoryPath)
	default:
		http.NotFound(response, request)
		return
	}
	if err != nil {
		renderAction("", err.Error())
		return
	}
	renderAction("GitOps "+action+" complete.", "")
}

func (server *webServer) handleOpsRunStage(response http.ResponseWriter, request *http.Request, profileID string) {
	if !requireMethod(response, request, http.MethodPost) {
		return
	}
	stage := strings.TrimSpace(request.FormValue("stage"))
	if stage != "stacks" && !validStackStage(stage) {
		http.Error(response, "stage must be stacks or stack:<name>", http.StatusBadRequest)
		return
	}
	profile, state, config, err := prepareProfileStageSetup(setupCLIOptions{ProfileID: profileID, Yes: true}, server.store, stage)
	if err != nil {
		server.render(response, request, frontend.OpsProfileDrawer(server.opsProfileDrawerData(request.Context(), profileID, "", err.Error())))
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
		Target:    stage,
		Status:    runStatusRunning,
		StreamURL: runEventsPathPrefix + runID,
	}))
}

func (server *webServer) opsRunsData(profileID string, query string, notice string, errorText string) frontend.OpsRunsData {
	_, state, err := server.store.Load(profileID)
	data := frontend.OpsRunsData{CSRFToken: server.csrf, ProfileID: profileID, Query: query, Notice: notice, Error: errorText}
	if err != nil {
		data.Error = err.Error()
		return data
	}
	runs := make([]SetupRun, 0, len(state.Runs))
	for _, run := range state.Runs {
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].UpdatedAt.After(runs[j].UpdatedAt)
	})
	for _, run := range runs {
		row := frontend.OpsRunRow{
			ID:        run.ID,
			Status:    run.Status,
			Stages:    opsRunStageSummary(run),
			Error:     opsRunErrorSummary(run),
			CreatedAt: formatWebTime(run.CreatedAt),
			UpdatedAt: formatWebTime(run.UpdatedAt),
		}
		if !server.runMatchesQuery(profileID, run, row, query) {
			continue
		}
		data.Rows = append(data.Rows, row)
	}
	return data
}

func (server *webServer) opsRecentRunRows(profileID string, limit int) []frontend.OpsRunRow {
	runs := server.opsRunsData(profileID, "", "", "")
	if limit > 0 && len(runs.Rows) > limit {
		return runs.Rows[:limit]
	}
	return runs.Rows
}

func (server *webServer) opsRunDetailData(profileID string, runID string, notice string, errorText string) frontend.OpsRunDetailData {
	_, state, err := server.store.Load(profileID)
	data := frontend.OpsRunDetailData{CSRFToken: server.csrf, ProfileID: profileID, RunID: runID, Notice: notice, Error: errorText}
	if err != nil {
		data.Error = err.Error()
		return data
	}
	run, ok := state.Runs[runID]
	if !ok {
		data.Error = "run not found"
		return data
	}
	data.Status = run.Status
	events, err := server.loadRunLogEvents(profileID, runID)
	if err != nil {
		data.Error = firstNonEmpty(data.Error, err.Error())
	}
	for _, event := range events {
		line := event.Line
		if line == "" && event.Error != "" {
			line = event.Error
		}
		if line == "" {
			line = strings.TrimSpace(string(event.Type) + " " + event.Stage + " " + event.TaskName)
		}
		if line != "" {
			data.LogLines = append(data.LogLines, server.maskProfileSecrets(profileID, line))
		}
	}
	return data
}

func (server *webServer) handleOpsAccessAction(response http.ResponseWriter, request *http.Request, profileID string, action string) {
	if !requireMethod(response, request, http.MethodPost) {
		return
	}
	switch action {
	case "reveal":
		server.handleOpsAccessReveal(response, request, profileID)
	case "github-token":
		server.handleOpsGitHubToken(response, request, profileID)
	case "pangolin":
		server.handleOpsPangolin(response, request, profileID)
	default:
		http.NotFound(response, request)
	}
}

func (server *webServer) opsAccessData(profileID string, notice string, errorText string) frontend.OpsAccessData {
	profile, _, err := server.store.Load(profileID)
	data := frontend.OpsAccessData{CSRFToken: server.csrf, ProfileID: profileID, Notice: notice, Error: errorText}
	if err != nil {
		data.Error = err.Error()
		return data
	}
	secrets, err := server.store.LoadSecrets(profileID)
	if err != nil {
		data.Error = firstNonEmpty(data.Error, err.Error())
		return data
	}
	data.GitHubStatus = secretStatus(secrets.GitHubToken)
	data.PangolinEmail = profile.PangolinAdminEmail
	data.PangolinStatus = secretStatus(secrets.PangolinAdminPassword)
	return data
}

func (server *webServer) handleOpsAccessReveal(response http.ResponseWriter, request *http.Request, profileID string) {
	secrets, err := server.store.LoadSecrets(profileID)
	if err != nil {
		server.render(response, request, frontend.OpsAccessReveal(frontend.OpsAccessData{Error: err.Error()}))
		return
	}
	secretName := request.FormValue("secret")
	data := frontend.OpsAccessData{CSRFToken: server.csrf, ProfileID: profileID}
	switch secretName {
	case "github_pat":
		data.RevealName = "GitHub PAT"
		data.RevealValue = secrets.GitHubToken
	case "pangolin_password":
		data.RevealName = "Pangolin password"
		data.RevealValue = secrets.PangolinAdminPassword
	default:
		data.Error = "unknown secret"
	}
	if data.Error == "" && data.RevealValue == "" {
		data.Error = "secret is not configured"
	}
	server.render(response, request, frontend.OpsAccessReveal(data))
}

func (server *webServer) handleOpsGitHubToken(response http.ResponseWriter, request *http.Request, profileID string) {
	secrets, err := server.store.LoadSecrets(profileID)
	if err != nil {
		server.render(response, request, frontend.OpsAccessPanel(server.opsAccessData(profileID, "", err.Error())))
		return
	}
	if request.FormValue("remove") == "true" {
		secrets.GitHubToken = ""
	} else {
		token, err := normalizeGitHubToken(request.FormValue("github_pat"))
		if err != nil {
			server.render(response, request, frontend.OpsAccessPanel(server.opsAccessData(profileID, "", err.Error())))
			return
		}
		secrets.GitHubToken = token
	}
	if err := server.store.SaveSecrets(profileID, secrets); err != nil {
		server.render(response, request, frontend.OpsAccessPanel(server.opsAccessData(profileID, "", err.Error())))
		return
	}
	server.render(response, request, frontend.OpsAccessPanel(server.opsAccessData(profileID, "GitHub PAT updated.", "")))
}

func (server *webServer) handleOpsPangolin(response http.ResponseWriter, request *http.Request, profileID string) {
	profile, state, err := server.store.Load(profileID)
	if err != nil {
		server.render(response, request, frontend.OpsAccessPanel(server.opsAccessData(profileID, "", err.Error())))
		return
	}
	secrets, err := server.store.LoadSecrets(profileID)
	if err != nil {
		server.render(response, request, frontend.OpsAccessPanel(server.opsAccessData(profileID, "", err.Error())))
		return
	}
	email := strings.TrimSpace(request.FormValue("pangolin_admin_email"))
	if email != "" {
		if !setupEmailLike(email) {
			server.render(response, request, frontend.OpsAccessPanel(server.opsAccessData(profileID, "", "Pangolin administrator email must be a valid email address")))
			return
		}
		profile.PangolinAdminEmail = email
	}
	password := strings.TrimSpace(request.FormValue("pangolin_admin_password"))
	if password != "" {
		if !pangolinPasswordValid(password) {
			server.render(response, request, frontend.OpsAccessPanel(server.opsAccessData(profileID, "", "Pangolin administrator password must be 8-128 characters with upper, lower, digit, and symbol characters")))
			return
		}
		secrets.PangolinAdminPassword = password
	}
	if err := server.store.Save(profile, state); err != nil {
		server.render(response, request, frontend.OpsAccessPanel(server.opsAccessData(profileID, "", err.Error())))
		return
	}
	if err := server.store.SaveSecrets(profileID, secrets); err != nil {
		server.render(response, request, frontend.OpsAccessPanel(server.opsAccessData(profileID, "", err.Error())))
		return
	}
	server.render(response, request, frontend.OpsAccessPanel(server.opsAccessData(profileID, "Pangolin access updated.", "")))
}

func (server *webServer) handleOpsCloudAction(response http.ResponseWriter, request *http.Request, profileID string, action string) {
	if !requireMethod(response, request, http.MethodPost) {
		return
	}
	switch action {
	case "restart":
		server.handleOpsCloudRestart(response, request, profileID)
	case "destroy":
		server.handleOpsCloudDestroy(response, request, profileID)
	default:
		http.NotFound(response, request)
	}
}

func (server *webServer) opsCloudData(profileID string, notice string, errorText string) frontend.OpsCloudData {
	profile, _, err := server.store.Load(profileID)
	data := frontend.OpsCloudData{CSRFToken: server.csrf, ProfileID: profileID, Notice: notice, Error: errorText}
	if err != nil {
		data.Error = err.Error()
		return data
	}
	data.IP = profile.IP
	if profile.Cloud == nil {
		return data
	}
	data.Provider = profile.Cloud.Provider
	data.ResourceID = profile.Cloud.ResourceID
	data.Name = profile.Cloud.Name
	data.Region = profile.Cloud.Region
	data.Size = profile.Cloud.Size
	data.Image = profile.Cloud.Image
	data.Destroyed = profile.Cloud.DestroyedAt != nil
	return data
}

func (server *webServer) handleOpsCloudRestart(response http.ResponseWriter, request *http.Request, profileID string) {
	profile, _, err := server.store.Load(profileID)
	if err != nil {
		server.render(response, request, frontend.OpsCloudPanel(server.opsCloudData(profileID, "", err.Error())))
		return
	}
	if err := validateCloudAction(profile, request.FormValue("confirm"), "restart"); err != nil {
		server.render(response, request, frontend.OpsCloudPanel(server.opsCloudData(profileID, "", err.Error())))
		return
	}
	provider, err := cloudProviderFromRequest(request)
	if err != nil {
		server.render(response, request, frontend.OpsCloudPanel(server.opsCloudData(profileID, "", err.Error())))
		return
	}
	if err := provider.Reboot(request.Context(), profile.Cloud.ResourceID); err != nil {
		server.render(response, request, frontend.OpsCloudPanel(server.opsCloudData(profileID, "", err.Error())))
		return
	}
	server.render(response, request, frontend.OpsCloudPanel(server.opsCloudData(profileID, "Restart requested.", "")))
}

func (server *webServer) handleOpsCloudDestroy(response http.ResponseWriter, request *http.Request, profileID string) {
	profile, state, err := server.store.Load(profileID)
	if err != nil {
		server.render(response, request, frontend.OpsCloudPanel(server.opsCloudData(profileID, "", err.Error())))
		return
	}
	expected := "destroy " + firstNonEmpty(profile.CloudName(), profile.Name, profile.ID)
	if err := validateCloudAction(profile, request.FormValue("confirm"), expected); err != nil {
		server.render(response, request, frontend.OpsCloudPanel(server.opsCloudData(profileID, "", err.Error())))
		return
	}
	provider, err := cloudProviderFromRequest(request)
	if err != nil {
		server.render(response, request, frontend.OpsCloudPanel(server.opsCloudData(profileID, "", err.Error())))
		return
	}
	if err := provider.Destroy(request.Context(), profile.Cloud.ResourceID); err != nil {
		server.render(response, request, frontend.OpsCloudPanel(server.opsCloudData(profileID, "", err.Error())))
		return
	}
	now := time.Now().UTC()
	profile.Cloud.DestroyedAt = &now
	if err := server.store.Save(profile, state); err != nil {
		server.render(response, request, frontend.OpsCloudPanel(server.opsCloudData(profileID, "", err.Error())))
		return
	}
	server.render(response, request, frontend.OpsCloudPanel(server.opsCloudData(profileID, "Droplet destroyed and profile marked locally.", "")))
}

func (server *webServer) handleOpsCloudProvision(response http.ResponseWriter, request *http.Request) {
	if !requireMethod(response, request, http.MethodPost) {
		return
	}
	provider, err := cloudProviderFromRequest(request)
	if err != nil {
		server.render(response, request, frontend.OpsProfilesPanel(server.opsProfilesData(request.Context(), "", "", err.Error())))
		return
	}
	config := provisionConfig{
		Name:   strings.TrimSpace(request.FormValue("name")),
		Region: firstNonEmpty(strings.TrimSpace(request.FormValue("region")), defaultDigitalOceanRegion),
		Size:   firstNonEmpty(strings.TrimSpace(request.FormValue("size")), defaultDigitalOceanSize),
		Image:  firstNonEmpty(strings.TrimSpace(request.FormValue("image")), defaultDigitalOceanImage),
		SSHKey: strings.TrimSpace(request.FormValue("ssh_key")),
	}
	if config.Name == "" || config.SSHKey == "" {
		server.render(response, request, frontend.OpsProfilesPanel(server.opsProfilesData(request.Context(), "", "", "name and SSH key are required")))
		return
	}
	created, err := provider.Create(request.Context(), config)
	if err != nil {
		server.render(response, request, frontend.OpsProfilesPanel(server.opsProfilesData(request.Context(), "", "", err.Error())))
		return
	}
	createdAt, _ := time.Parse(time.RFC3339, created.CreatedAt)
	profile, err := server.store.Create(Profile{
		Name:                 firstNonEmpty(created.Name, config.Name),
		IP:                   created.IPv4,
		InitialSSHUser:       "root",
		AdminUser:            "servestead",
		PrivateKeyPath:       defaultKeygenConfig().Path,
		ConfigRepositoryPath: "",
		Cloud: &ProfileCloud{
			Provider:     digitalOceanProviderName,
			ResourceID:   created.ID,
			Name:         firstNonEmpty(created.Name, config.Name),
			Region:       firstNonEmpty(created.Region, config.Region),
			Size:         firstNonEmpty(created.Size, config.Size),
			Image:        firstNonEmpty(created.Image, config.Image),
			PriceMonthly: created.PriceMonthly,
			PriceHourly:  created.PriceHourly,
			CreatedAt:    createdAt,
		},
	})
	if err != nil {
		server.render(response, request, frontend.OpsProfilesPanel(server.opsProfilesData(request.Context(), "", "", err.Error())))
		return
	}
	server.render(response, request, frontend.OpsProfilesPanel(server.opsProfilesData(request.Context(), profile.ID, "DigitalOcean profile created. Review setup values before running setup.", "")))
}

func (server *webServer) opsGitSummary(ctx context.Context, repositoryPath string) string {
	if repositoryPath == "" {
		return notConfiguredLabel
	}
	if _, err := os.Stat(expandUserPath(repositoryPath)); err != nil {
		return "repository missing"
	}
	gitCtx, cancel := context.WithTimeout(ctx, opsGitTimeout)
	defer cancel()
	status, err := stackRepositoryStatus(gitCtx, repositoryPath)
	if err != nil {
		return "git unavailable"
	}
	if status == "clean" {
		head, err := stackRepositoryHead(gitCtx, repositoryPath)
		if err == nil {
			needsPush, err := stackRepositoryNeedsPush(gitCtx, repositoryPath, head)
			if err == nil && needsPush {
				return "needs push"
			}
		}
		return "clean"
	}
	return gitChangesPending
}

func (server *webServer) opsActiveRunStatus(profileID string, state ProfileState) string {
	server.manager.mu.Lock()
	_, active := server.manager.active[profileID]
	server.manager.mu.Unlock()
	if active {
		return runStatusRunning
	}
	if state.ActiveRunID == "" {
		return "idle"
	}
	if run, ok := state.Runs[state.ActiveRunID]; ok && run.Status != "" {
		return run.Status
	}
	return "idle"
}

func opsSetupStatus(state ProfileState) string {
	latest, ok := latestSetupRun(state)
	if !ok {
		return "not run"
	}
	if latest.Status == runStatusRunning {
		return runStatusRunning
	}
	if latest.Status == runStatusFailed || latest.Status == runStatusCancelled {
		return latest.Status
	}
	if latest.Status == runStatusComplete {
		return runStatusComplete
	}
	completed := completedSetupStages(state)
	switch {
	case completed["stacks"]:
		return "stacks complete"
	case completed["platform"]:
		return "platform complete"
	case completed["harden"]:
		return "harden complete"
	case completed["bootstrap"]:
		return "bootstrap complete"
	default:
		return latest.Status
	}
}

func opsCloudState(profile Profile) string {
	if profile.Cloud == nil {
		return "none"
	}
	if profile.Cloud.DestroyedAt != nil {
		return "destroyed"
	}
	return firstNonEmpty(profile.Cloud.Provider, "cloud") + " active"
}

func opsNextAction(profile Profile, activeRunStatus string, setupStatus string, gitState string, cloudState string) string {
	switch {
	case activeRunStatus == runStatusRunning:
		return "Watch active run"
	case profile.BaseDomain == "" || profile.LetsEncryptEmail == "":
		return "Complete setup values"
	case gitState == gitChangesPending:
		return "Review GitOps"
	case gitState == "needs push":
		return "Push repository"
	case cloudState == "destroyed":
		return "Review cloud state"
	case setupStatus == runStatusComplete || strings.Contains(setupStatus, "complete"):
		return "Sync stacks as needed"
	default:
		return "Run setup"
	}
}

func latestSetupRun(state ProfileState) (SetupRun, bool) {
	var latest SetupRun
	for _, run := range state.Runs {
		if latest.ID == "" || run.UpdatedAt.After(latest.UpdatedAt) {
			latest = run
		}
	}
	return latest, latest.ID != ""
}

func opsRunStageSummary(run SetupRun) string {
	names := make([]string, 0, len(run.Stages))
	for stage, status := range run.Stages {
		if status.Status == stageStatusRunning || status.Status == stageStatusFailed || status.Status == stageStatusComplete {
			names = append(names, stage+":"+status.Status)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "planned"
	}
	return strings.Join(names, ", ")
}

func opsRunErrorSummary(run SetupRun) string {
	for _, status := range run.Stages {
		if status.LastError != "" {
			return status.LastError
		}
	}
	return ""
}

func (server *webServer) runMatchesQuery(profileID string, run SetupRun, row frontend.OpsRunRow, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	if strings.Contains(strings.ToLower(row.ID+" "+row.Status+" "+row.Stages+" "+row.Error), query) {
		return true
	}
	events, err := server.loadRunLogEvents(profileID, run.ID)
	if err != nil {
		return false
	}
	for _, event := range events {
		text := strings.ToLower(event.Stage + " " + event.TaskName + " " + event.Error + " " + server.maskProfileSecrets(profileID, event.Line))
		if strings.Contains(text, query) {
			return true
		}
	}
	return false
}

func (server *webServer) loadRunLogEvents(profileID string, runID string) ([]TaskEvent, error) {
	store, ok := server.store.(*fileProfileStore)
	if !ok {
		return nil, nil
	}
	path, err := store.runLogPath(profileID, runID)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readTaskEventsJSONL(file)
}

func readTaskEventsJSONL(reader io.Reader) ([]TaskEvent, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var events []TaskEvent
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event TaskEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, scanner.Err()
}

func (server *webServer) maskProfileSecrets(profileID string, text string) string {
	secrets, err := server.store.LoadSecrets(profileID)
	if err != nil {
		return text
	}
	for _, secret := range profileSecretValues(secrets) {
		if secret != "" && len(secret) >= 4 {
			text = strings.ReplaceAll(text, secret, "***")
		}
	}
	return text
}

func profileSecretValues(secrets ProfileSecrets) []string {
	return []string{
		secrets.ServerSecret,
		secrets.PangolinSetupToken,
		secrets.PangolinAdminPassword,
		secrets.NewtID,
		secrets.NewtSecret,
		secrets.BeszelAdminPassword,
		secrets.BeszelSystemToken,
		secrets.BeszelHubPrivateKey,
		secrets.BeszelHubPublicKey,
		secrets.GitHubToken,
		secrets.StackSecretIdentity,
		secrets.StackSecretRecipient,
	}
}

func validStackStage(stage string) bool {
	if !strings.HasPrefix(stage, setupStageStackPrefix) {
		return false
	}
	name := strings.TrimPrefix(stage, setupStageStackPrefix)
	return stackSlugPattern.MatchString(name)
}

func secretStatus(value string) string {
	if value == "" {
		return notConfiguredLabel
	}
	return "configured"
}

func cloudProviderFromRequest(request *http.Request) (cloudProvider, error) {
	token := strings.TrimSpace(request.FormValue("token"))
	if token == "" {
		token = strings.TrimSpace(os.Getenv("DIGITALOCEAN_ACCESS_TOKEN"))
	}
	if token == "" {
		token = strings.TrimSpace(os.Getenv("DIGITALOCEAN_TOKEN"))
	}
	if token == "" {
		return nil, errors.New("DigitalOcean token is required")
	}
	return newWebCloudProvider(token), nil
}

func validateCloudAction(profile Profile, confirmation string, expected string) error {
	if profile.Cloud == nil {
		return errors.New("profile has no cloud metadata")
	}
	if profile.Cloud.Provider != digitalOceanProviderName {
		return fmt.Errorf("unsupported cloud provider %q", profile.Cloud.Provider)
	}
	if profile.Cloud.DestroyedAt != nil {
		return errors.New("cloud resource is already marked destroyed")
	}
	if strings.TrimSpace(confirmation) != expected {
		return fmt.Errorf("type %q to confirm", expected)
	}
	return nil
}

func (profile Profile) CloudName() string {
	if profile.Cloud == nil {
		return ""
	}
	return profile.Cloud.Name
}

func formatWebTime(value time.Time) string {
	if value.IsZero() {
		return "never"
	}
	return value.Local().Format("2006-01-02 15:04")
}
