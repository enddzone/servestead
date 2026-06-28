package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
)

type setupMode int

const (
	setupModeProviderKey setupMode = iota
	setupModeBootstrapHarden
	setupModeHardenOnly
	setupModeNetwork
	setupModeProxy
	setupModeDoctor
	setupModeFullRun
	setupModeObservability
)

type setupConfig struct {
	Mode                    setupMode
	Host                    string
	InitialSSHUser          string
	AdminUser               string
	AdminPublicKeyPath      string
	PrivateKeyPath          string
	ProviderKeyPath         string
	ProviderKeyComment      string
	BaseDomain              string
	LetsEncryptEmail        string
	ServerSecret            string
	PangolinSetupToken      string
	PangolinAdminEmail      string
	PangolinAdminPassword   string
	NewtID                  string
	NewtSecret              string
	BeszelAdminPassword     string
	BeszelSystemToken       string
	BeszelHubPrivateKey     string
	BeszelHubPublicKey      string
	ConfigRepositoryPath    string
	GitHubRepositoryURL     string
	ConfigRepositoryCommit  string
	ConfigRepositoryOrigin  string
	ConfigRepositoryCompose string
	ConfigRepositorySHA256  string
	GitHubToken             string
	Stacks                  []configuredStack
	ProfileID               string
}

type setupCLIOptions struct {
	IP                    string
	ProfileID             string
	Name                  string
	Fresh                 bool
	Yes                   bool
	InitialSSHUser        string
	AdminUser             string
	PrivateKeyPath        string
	BaseDomain            string
	LetsEncryptEmail      string
	PangolinAdminEmail    string
	PangolinAdminPassword string
	ConfigRepositoryPath  string
	GitHubRepositoryURL   string
}

type preflightCheck struct {
	Name     string
	Detail   string
	OK       bool
	Required bool
}

const setupUsage = `Usage of setup:
  aegisnode setup [--ip <ipv4-or-hostname>]

Launches guided setup. With --ip, setup creates or selects a saved profile, collects all full-run values before remote execution, then runs bootstrap, hardening, Docker networking, and reverse proxy deployment end to end.
`

const doctorUsage = `Usage of doctor:
  aegisnode doctor [--admin-public-key <path>] [--private-key <path>]

Runs local preflight checks for built-in SSH/key support and optional key files without contacting a server.
`

func runSetup(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprint(stderr, setupUsage)
		flags.PrintDefaults()
	}
	options := setupCLIOptions{}
	flags.StringVar(&options.IP, "ip", "", "target VPS IPv4 address or hostname for profile-aware full setup")
	flags.StringVar(&options.Name, "name", "", "profile name when creating a profile")
	flags.BoolVar(&options.Fresh, "fresh", false, "create a fresh profile even when the IP already has saved profiles")
	flags.BoolVar(&options.Yes, "yes", false, "run without the final interactive review when all required values are provided")
	flags.StringVar(&options.InitialSSHUser, "initial-ssh-user", "", "initial SSH user for bootstrap")
	flags.StringVar(&options.AdminUser, "admin-user", "", "administrative SSH user")
	flags.StringVar(&options.PrivateKeyPath, "private-key", "", "path to the private key used for setup")
	flags.StringVar(&options.BaseDomain, "domain", "", "base domain for Pangolin, for example example.com")
	flags.StringVar(&options.LetsEncryptEmail, "email", "", "Let's Encrypt account email")
	flags.StringVar(&options.PangolinAdminEmail, "pangolin-admin-email", "", "Pangolin administrator email (defaults to --email)")
	flags.StringVar(&options.ConfigRepositoryPath, "config-repo", "", "local Git repository containing declarative stack configuration")
	flags.StringVar(&options.GitHubRepositoryURL, "github-repo", "", "GitHub HTTPS repository to clone for declarative stack configuration")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	if options.IP != "" || len(args) > 0 {
		store, err := newDefaultProfileStore()
		if err != nil {
			return err
		}
		profile, state, config, err := prepareProfileSetup(options, store, stderr)
		if err != nil {
			return err
		}
		if shouldUseProfileRunView(options, stderr) {
			return runProfileSetupPlanWithRunView(ctx, store, profile, state, config, stdout, stderr)
		}
		return runProfileSetupPlan(ctx, store, profile, state, config, stdout, stderr)
	}

	store, err := newDefaultProfileStore()
	if err != nil {
		return err
	}
	if !isInteractiveWriter(stderr) {
		return errors.New("interactive setup requires a terminal; use setup --ip with --domain, --email, and --yes for scripts")
	}
	request, err := collectSetupRequest(store, stderr)
	if err != nil {
		return err
	}
	if request.Legacy {
		if err := runSetupPlan(ctx, request.LegacyConfig, stdout, stderr); err != nil {
			return err
		}
		return nil
	}
	if request.Stage != "" {
		profile, state, config, err := prepareProfileStageSetup(request.ProfileOptions, store, request.Stage)
		if err != nil {
			return err
		}
		return runProfileSetupStagePlanWithRunView(ctx, store, profile, state, config, request.Stage, stdout, stderr)
	}
	profile, state, config, err := prepareProfileSetup(request.ProfileOptions, store, stderr)
	if err != nil {
		return err
	}
	if shouldUseProfileRunView(request.ProfileOptions, stderr) {
		return runProfileSetupPlanWithRunView(ctx, store, profile, state, config, stdout, stderr)
	}
	return runProfileSetupPlan(ctx, store, profile, state, config, stdout, stderr)
}

func runDoctor(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprint(stderr, doctorUsage)
		flags.PrintDefaults()
	}
	config := setupConfig{Mode: setupModeDoctor}
	flags.StringVar(&config.AdminPublicKeyPath, "admin-public-key", "", "optional path to validate an ED25519 admin public key")
	flags.StringVar(&config.PrivateKeyPath, "private-key", "", "optional path to validate an SSH private key")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	return runPreflight(config, stdout)
}

func collectSetupRequest(store ProfileStore, output io.Writer) (setupRequest, error) {
	for {
		profiles, err := loadProfileChoices(store)
		if err != nil {
			return setupRequest{}, err
		}
		model := newProfileSetupModel(profiles)
		model.profileStore = store
		program := tea.NewProgram(model, tea.WithOutput(output), tea.WithAltScreen())
		finalModel, err := program.Run()
		if err != nil {
			return setupRequest{}, fmt.Errorf("run setup TUI: %w", err)
		}
		result, ok := finalModel.(profileSetupModel)
		if !ok {
			return setupRequest{}, errors.New("setup TUI returned an unexpected model")
		}
		if result.cancelled {
			return setupRequest{}, errors.New("setup cancelled")
		}
		if result.deleteProfileID != "" {
			if err := store.Delete(result.deleteProfileID); err != nil {
				return setupRequest{}, err
			}
			continue
		}
		if result.legacy {
			config, err := collectLegacySetupConfig(output)
			if err != nil {
				return setupRequest{}, err
			}
			return setupRequest{Legacy: true, LegacyConfig: config}, nil
		}
		if !result.done {
			return setupRequest{}, errors.New("setup did not complete")
		}
		if result.singleStage != "" {
			options, err := result.optionsForSelectedProfile()
			if err != nil {
				return setupRequest{}, err
			}
			return setupRequest{ProfileOptions: options, Stage: result.singleStage}, nil
		}
		options, err := result.optionsFromInputs()
		if err != nil {
			return setupRequest{}, err
		}
		return setupRequest{ProfileOptions: options}, nil
	}
}

func collectLegacySetupConfig(output io.Writer) (setupConfig, error) {
	model := newSetupModel()
	program := tea.NewProgram(model, tea.WithOutput(output), tea.WithAltScreen())
	finalModel, err := program.Run()
	if err != nil {
		return setupConfig{}, fmt.Errorf("run setup TUI: %w", err)
	}
	result, ok := finalModel.(setupModel)
	if !ok {
		return setupConfig{}, errors.New("setup TUI returned an unexpected model")
	}
	if result.cancelled {
		return setupConfig{}, errors.New("setup cancelled")
	}
	if !result.done {
		return setupConfig{}, errors.New("setup did not complete")
	}
	return result.config, nil
}

type setupRequest struct {
	Legacy         bool
	LegacyConfig   setupConfig
	ProfileOptions setupCLIOptions
	Stage          string
}

type profileChoice struct {
	Profile Profile
	State   ProfileState
	Secrets ProfileSecrets
}

func loadProfileChoices(store ProfileStore) ([]profileChoice, error) {
	summaries, err := store.List()
	if err != nil {
		return nil, err
	}
	choices := make([]profileChoice, 0, len(summaries))
	for _, summary := range summaries {
		profile, state, err := store.Load(summary.ID)
		if err != nil {
			return nil, err
		}
		secrets, err := store.LoadSecrets(summary.ID)
		if err != nil {
			return nil, err
		}
		choices = append(choices, profileChoice{Profile: profile, State: state, Secrets: secrets})
	}
	return choices, nil
}

type profileSetupScreen int

const (
	profileSetupScreenPicker profileSetupScreen = iota
	profileSetupScreenDashboard
	profileSetupScreenIntake
	profileSetupScreenAdvanced
	profileSetupScreenRepository
	profileSetupScreenRepositoryDetails
	profileSetupScreenStacks
	profileSetupScreenStackCompose
	profileSetupScreenStackEditor
	profileSetupScreenStackResourceEditor
	profileSetupScreenStackEnvironment
	profileSetupScreenStackDeleteConfirm
	profileSetupScreenStackDiff
	profileSetupScreenStackCommit
	profileSetupScreenReview
	profileSetupScreenDeleteConfirm
)

var setupStageOrder = []string{"bootstrap", "harden", "network", "proxy", "observability"}
var dashboardStageOrder = []string{"bootstrap", "harden", "platform"}

type pangolinRegistrationStatus string

const (
	pangolinRegistrationUnknown     pangolinRegistrationStatus = ""
	pangolinRegistrationChecking    pangolinRegistrationStatus = "checking"
	pangolinRegistrationIncomplete  pangolinRegistrationStatus = "incomplete"
	pangolinRegistrationComplete    pangolinRegistrationStatus = "complete"
	pangolinRegistrationUnavailable pangolinRegistrationStatus = "unavailable"
)

type pangolinRegistrationStatusMsg struct {
	profileID string
	complete  bool
	err       error
}

type profileListItem struct {
	kind        string
	index       int
	title       string
	description string
}

func (item profileListItem) Title() string       { return item.title }
func (item profileListItem) Description() string { return item.description }
func (item profileListItem) FilterValue() string { return item.title + " " + item.description }

type profileSetupModel struct {
	screen                profileSetupScreen
	profiles              []profileChoice
	profileList           list.Model
	repositoryList        list.Model
	stageTable            table.Model
	progress              progress.Model
	planViewport          viewport.Model
	help                  help.Model
	selectedIndex         int
	deleteProfileID       string
	singleStage           string
	pangolinStatus        pangolinRegistrationStatus
	pangolinError         string
	showSetupToken        bool
	fresh                 bool
	inputs                []textinput.Model
	advanced              []textinput.Model
	repositoryInputs      []textinput.Model
	stackComposeInput     textinput.Model
	stackTable            table.Model
	stacks                []editableStack
	stackInputs           []textinput.Model
	stackServices         []composeServiceSummary
	stackCompose          string
	stackOriginalName     string
	stackResources        []stackPublicResource
	stackResourceTable    table.Model
	stackResourceInputs   []textinput.Model
	stackResourceIndex    int
	stackEnvironmentInput textinput.Model
	stackEnvironment      string
	stackEnvironmentKeys  []string
	stackEnvironmentDirty bool
	profileStore          ProfileStore
	stackNotice           string
	stackGitStatus        string
	stackHead             string
	stackNeedsPush        bool
	stackSyncStatus       string
	stackDiffViewport     viewport.Model
	stackCommitInput      textinput.Model
	repositoryMode        string
	focus                 int
	err                   string
	width                 int
	height                int
	done                  bool
	legacy                bool
	cancelled             bool
}

func newProfileSetupModel(profiles []profileChoice) profileSetupModel {
	items := make([]list.Item, 0, len(profiles)+2)
	for index, choice := range profiles {
		status := latestProfileStatus(choice.State)
		if status == "" {
			status = "no runs yet"
		}
		items = append(items, profileListItem{
			kind:        "profile",
			index:       index,
			title:       firstNonEmpty(choice.Profile.Name, choice.Profile.IP),
			description: fmt.Sprintf("%s - %s - updated %s", choice.Profile.IP, status, choice.Profile.UpdatedAt.Local().Format("2006-01-02 15:04")),
		})
	}
	items = append(items,
		profileListItem{kind: "new", title: "Set up a new server profile", description: "Collect IP, SSH key, domain, and email before running the full setup plan."},
		profileListItem{kind: "legacy", title: "Advanced legacy setup paths", description: "Open key generation, one-off hardening, network, proxy, or doctor modes."},
	)

	delegate := list.NewDefaultDelegate()
	delegate.SetHeight(2)
	profileList := list.New(items, delegate, 82, 14)
	profileList.Title = "AegisNode profiles"
	profileList.SetShowStatusBar(false)
	profileList.SetFilteringEnabled(false)
	profileList.DisableQuitKeybindings()

	repositoryList := list.New([]list.Item{
		profileListItem{
			kind:        "create",
			title:       "Create a new local repository",
			description: "AegisNode creates and commits the scaffold after confirmation, before any SSH commands run.",
		},
		profileListItem{
			kind:        "existing",
			title:       "Use an existing local checkout",
			description: "Select a Git repository already present on this computer.",
		},
		profileListItem{
			kind:        "github",
			title:       "Clone a GitHub repository",
			description: "Clone a GitHub HTTPS repository after confirmation, before any SSH commands run.",
		},
	}, delegate, 82, 14)
	repositoryList.Title = "Observability configuration repository"
	repositoryList.SetShowStatusBar(false)
	repositoryList.SetFilteringEnabled(false)
	repositoryList.DisableQuitKeybindings()

	model := profileSetupModel{
		screen:            profileSetupScreenPicker,
		profiles:          profiles,
		profileList:       profileList,
		repositoryList:    repositoryList,
		stageTable:        newProfileStageTable(nil),
		stackTable:        newStackTable(nil, "", nil),
		stackDiffViewport: viewport.New(100, 18),
		progress:          progress.New(progress.WithWidth(42)),
		planViewport:      viewport.New(82, 10),
		help:              help.New(),
		selectedIndex:     -1,
		width:             82,
		height:            24,
	}
	model.inputs = setupProfileInputs(setupCLIOptions{})
	model.advanced = setupAdvancedInputs(setupCLIOptions{})
	model.repositoryInputs = setupRepositoryInputs(setupCLIOptions{})
	model.repositoryMode = "create"
	model.stackComposeInput = newSetupInputs([]setupInputField{{
		label: "Docker Compose file", placeholder: "/path/to/docker-compose.yml",
	}})[0]
	model.stackEnvironmentInput = newSetupInputs([]setupInputField{{
		label: "Runtime .env file", placeholder: "/path/to/.env",
	}})[0]
	model.stackCommitInput = newSetupInputs([]setupInputField{{
		label: "Commit message", placeholder: "Update application stacks",
	}})[0]
	model.inputs[0].Focus()
	return model
}

func setupProfileInputs(options setupCLIOptions) []textinput.Model {
	return newSetupInputs([]setupInputField{
		{label: "Server IP or hostname", placeholder: "203.0.113.10", value: options.IP},
		{label: "AegisNode private key", placeholder: defaultKeygenConfig().Path, value: firstNonEmpty(options.PrivateKeyPath, defaultKeygenConfig().Path)},
		{label: "Base domain", placeholder: "example.com", value: options.BaseDomain},
		{label: "Let's Encrypt email", placeholder: "admin@example.com", value: options.LetsEncryptEmail},
	})
}

func setupAdvancedInputs(options setupCLIOptions) []textinput.Model {
	return newSetupInputs([]setupInputField{
		{label: "Profile name", placeholder: "production-vps", value: options.Name},
		{label: "Initial SSH user", value: firstNonEmpty(options.InitialSSHUser, "root")},
		{label: "Admin SSH user", value: firstNonEmpty(options.AdminUser, "aegisadmin")},
		{label: "Pangolin admin email", placeholder: "defaults to Let's Encrypt email", value: options.PangolinAdminEmail},
		{label: "Pangolin admin password", placeholder: "generated for fresh installs", value: options.PangolinAdminPassword, secret: true},
	})
}

func setupRepositoryInputs(options setupCLIOptions) []textinput.Model {
	return newSetupInputs([]setupInputField{
		{label: "Local checkout path", placeholder: "/path/to/aegisnode-config", value: options.ConfigRepositoryPath},
		{label: "GitHub HTTPS URL", placeholder: "https://github.com/owner/repository.git", value: options.GitHubRepositoryURL},
	})
}

func newProfileStageTable(state *ProfileState) table.Model {
	return table.New(
		table.WithColumns([]table.Column{
			{Title: "Stage", Width: 16},
			{Title: "Status", Width: 12},
			{Title: "Last error", Width: 42},
		}),
		table.WithRows(profileStageRows(state)),
		table.WithHeight(len(dashboardStageOrder)+1),
		table.WithWidth(78),
		table.WithFocused(true),
	)
}

func profileStageRows(state *ProfileState) []table.Row {
	labels := map[string]string{
		"bootstrap": "Bootstrap",
		"harden":    "Harden",
		"platform":  "Platform",
	}
	completed := map[string]bool{}
	if state != nil {
		completed = completedSetupStages(*state)
	}
	activeStages := map[string]SetupStageStatus{}
	if state != nil {
		if run, ok := state.Runs[state.ActiveRunID]; ok {
			activeStages = run.Stages
		}
	}
	rows := []table.Row{}
	for _, stage := range dashboardStageOrder {
		status := stageStatusPending
		lastError := ""
		if dashboardStageComplete(stage, completed) {
			status = stageStatusComplete
		}
		for _, internalStage := range dashboardInternalStages(stage) {
			stageState, ok := activeStages[internalStage]
			if !ok {
				continue
			}
			if stageState.Status == stageStatusFailed {
				status = stageStatusFailed
				lastError = stageState.LastError
				break
			}
			if stageState.Status == stageStatusRunning {
				status = stageStatusRunning
				lastError = stageState.LastError
			}
		}
		rows = append(rows, table.Row{labels[stage], status, truncateForTable(lastError, 42)})
	}
	return rows
}

func dashboardInternalStages(stage string) []string {
	if stage == "platform" {
		return []string{"network", "proxy", "observability"}
	}
	return []string{stage}
}

func dashboardStageComplete(stage string, completed map[string]bool) bool {
	for _, internalStage := range dashboardInternalStages(stage) {
		if !completed[internalStage] {
			return false
		}
	}
	return true
}

func newStackTable(stacks []editableStack, baseDomain string, state *ProfileState) table.Model {
	rows := make([]table.Row, 0, len(stacks))
	for _, stack := range stacks {
		resource := "(private)"
		if len(stack.Metadata.PublicResources) == 1 {
			public := stack.Metadata.PublicResources[0]
			host := public.Subdomain
			if baseDomain != "" {
				host += "." + baseDomain
			}
			resource = fmt.Sprintf("%s → %s:%d", host, public.Service, public.Port)
		} else if len(stack.Metadata.PublicResources) > 1 {
			resource = fmt.Sprintf("%d public resources", len(stack.Metadata.PublicResources))
		}
		rows = append(rows, table.Row{stack.Name, standaloneStackStatus(stack.Name, state), resource})
	}
	return table.New(
		table.WithColumns([]table.Column{
			{Title: "Stack", Width: 18},
			{Title: "Status", Width: 10},
			{Title: "Public resource", Width: 44},
		}),
		table.WithRows(rows),
		table.WithHeight(clampInt(len(rows)+1, 2, 13)),
		table.WithWidth(78),
		table.WithFocused(true),
	)
}

func standaloneStackStatus(name string, state *ProfileState) string {
	if state == nil {
		return "unknown"
	}
	stage := "stack:" + name
	if run, ok := state.Runs[state.ActiveRunID]; ok {
		if current, ok := run.Stages[stage]; ok && current.Status != "" && current.Status != stageStatusPending {
			return current.Status
		}
	}
	if completedSetupStages(*state)[stage] {
		return stageStatusComplete
	}
	return "unknown"
}

func truncateForTable(value string, width int) string {
	value = strings.TrimSpace(value)
	if len(value) <= width {
		return value
	}
	if width <= 1 {
		return value[:width]
	}
	return value[:width-1] + "."
}

func latestProfileStatus(state ProfileState) string {
	if state.ActiveRunID == "" {
		return ""
	}
	run, ok := state.Runs[state.ActiveRunID]
	if !ok {
		return ""
	}
	return run.Status
}

func profileCompletion(state *ProfileState) float64 {
	if state == nil {
		return 0
	}
	completed := 0
	completedStages := completedSetupStages(*state)
	for _, stage := range dashboardStageOrder {
		if dashboardStageComplete(stage, completedStages) {
			completed++
		}
	}
	return float64(completed) / float64(len(dashboardStageOrder))
}

func (model profileSetupModel) Init() tea.Cmd {
	return textinput.Blink
}

func (model profileSetupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		model.width = msg.Width
		model.height = msg.Height
		contentWidth := max(40, msg.Width-4)
		navigationHeight := max(8, msg.Height-7)
		model.profileList.SetSize(contentWidth, navigationHeight)
		model.repositoryList.SetSize(contentWidth, navigationHeight)
		model.planViewport.Width = contentWidth
		model.planViewport.Height = max(6, msg.Height-14)
		model.stackDiffViewport.Width = contentWidth
		model.stackDiffViewport.Height = max(6, msg.Height-10)
		model.progress.Width = clampInt(msg.Width-8, 24, 64)
		model.resizeStackTable()
		return model, nil
	case pangolinRegistrationStatusMsg:
		if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) || model.profiles[model.selectedIndex].Profile.ID != msg.profileID {
			return model, nil
		}
		if msg.err != nil {
			model.pangolinStatus = pangolinRegistrationUnavailable
			model.pangolinError = concisePangolinRegistrationError(msg.err)
		} else if msg.complete {
			model.pangolinStatus = pangolinRegistrationComplete
			model.pangolinError = ""
			if !completedSetupStages(model.profiles[model.selectedIndex].State)["proxy"] {
				model.advanced[4].SetValue("")
			}
		} else {
			model.pangolinStatus = pangolinRegistrationIncomplete
			model.pangolinError = ""
		}
		return model, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			model.cancelled = true
			return model, tea.Quit
		case "q":
			if !profileSetupScreenAcceptsText(model.screen) {
				model.cancelled = true
				return model, tea.Quit
			}
		case "esc":
			model.goBack()
			model.err = ""
			return model, nil
		}

		switch model.screen {
		case profileSetupScreenPicker:
			return model.updateProfilePicker(msg)
		case profileSetupScreenDashboard:
			return model.updateProfileDashboard(msg)
		case profileSetupScreenIntake:
			return model.updateProfileInput(msg, false)
		case profileSetupScreenAdvanced:
			return model.updateProfileInput(msg, true)
		case profileSetupScreenRepository:
			return model.updateRepositoryChoice(msg)
		case profileSetupScreenRepositoryDetails:
			return model.updateRepositoryDetails(msg)
		case profileSetupScreenStacks:
			return model.updateStacks(msg)
		case profileSetupScreenStackCompose:
			return model.updateStackCompose(msg)
		case profileSetupScreenStackEditor:
			return model.updateStackEditor(msg)
		case profileSetupScreenStackResourceEditor:
			return model.updateStackResourceEditor(msg)
		case profileSetupScreenStackEnvironment:
			return model.updateStackEnvironment(msg)
		case profileSetupScreenStackDeleteConfirm:
			return model.updateStackDeleteConfirm(msg)
		case profileSetupScreenStackDiff:
			return model.updateStackDiff(msg)
		case profileSetupScreenStackCommit:
			return model.updateStackCommit(msg)
		case profileSetupScreenReview:
			return model.updateProfileReview(msg)
		case profileSetupScreenDeleteConfirm:
			return model.updateProfileDeleteConfirm(msg)
		default:
			return model, nil
		}
	default:
		return model, nil
	}
}

func profileSetupScreenAcceptsText(screen profileSetupScreen) bool {
	switch screen {
	case profileSetupScreenIntake, profileSetupScreenAdvanced, profileSetupScreenRepositoryDetails,
		profileSetupScreenStackCompose, profileSetupScreenStackEditor, profileSetupScreenStackResourceEditor,
		profileSetupScreenStackEnvironment, profileSetupScreenStackCommit:
		return true
	default:
		return false
	}
}

func (model *profileSetupModel) resizeStackTable() {
	contentWidth := max(78, model.width-4)
	resourceWidth := max(38, contentWidth-34)
	model.stackTable.SetColumns([]table.Column{
		{Title: "Stack", Width: 18},
		{Title: "Status", Width: 10},
		{Title: "Public resource", Width: resourceWidth},
	})
	model.stackTable.SetWidth(contentWidth)
	availableHeight := model.height - 12
	if model.screen == profileSetupScreenDashboard {
		availableHeight = model.height - 23
	}
	model.stackTable.SetHeight(clampInt(len(model.stacks)+1, 2, max(2, availableHeight)))
}

func (model profileSetupModel) updateProfilePicker(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "enter":
		selected, ok := model.profileList.SelectedItem().(profileListItem)
		if !ok {
			return model, nil
		}
		switch selected.kind {
		case "legacy":
			model.legacy = true
			return model, tea.Quit
		case "new":
			model.selectedIndex = -1
			model.fresh = false
			model.setInputsFromOptions(setupCLIOptions{})
			model.screen = profileSetupScreenIntake
			return model, nil
		case "profile":
			model.selectedIndex = selected.index
			model.fresh = false
			model.setInputsFromChoice(false)
			model.refreshDashboard()
			model.screen = profileSetupScreenDashboard
			return model, model.checkPangolinRegistration()
		}
	}
	var cmd tea.Cmd
	model.profileList, cmd = model.profileList.Update(key)
	return model, cmd
}

func (model profileSetupModel) updateProfileDashboard(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "v", "V":
		model.err = ""
		model.refreshPlanPreview()
		model.screen = profileSetupScreenReview
	case "r", "R":
		stage, err := model.selectedDashboardStage()
		if err != nil {
			model.err = err.Error()
			return model, nil
		}
		model.singleStage = stage
		proxyRetry := false
		if model.selectedIndex >= 0 && model.selectedIndex < len(model.profiles) {
			choice := model.profiles[model.selectedIndex]
			if run, ok := choice.State.Runs[choice.State.ActiveRunID]; ok {
				proxyRetry = run.Stages["proxy"].Status == stageStatusFailed
			}
		}
		if stage == "platform" && (model.pangolinStatus == pangolinRegistrationComplete || proxyRetry) {
			model.err = ""
			model.focus = 3
			model.advanced[model.focus].Focus()
			model.screen = profileSetupScreenAdvanced
			return model, nil
		}
		model.done = true
		return model, tea.Quit
	case "e", "E":
		model.err = ""
		model.screen = profileSetupScreenIntake
	case "a", "A":
		model.err = ""
		model.screen = profileSetupScreenAdvanced
	case "s", "S":
		model.err = ""
		model.screen = profileSetupScreenStacks
		model.refreshStacks()
		model.stackTable.Focus()
	case "f", "F":
		model.fresh = true
		model.setInputsFromChoice(true)
		model.screen = profileSetupScreenIntake
	case "x", "X":
		model.err = ""
		model.screen = profileSetupScreenDeleteConfirm
	case "t", "T":
		if model.selectedProfileHasSetupToken() {
			model.showSetupToken = !model.showSetupToken
		}
	case "c", "C":
		return model, model.checkPangolinRegistration()
	default:
		var cmd tea.Cmd
		model.stageTable, cmd = model.stageTable.Update(key)
		return model, cmd
	}
	return model, nil
}

func (model profileSetupModel) updateProfileInput(key tea.KeyMsg, advanced bool) (tea.Model, tea.Cmd) {
	inputs := model.inputs
	if advanced {
		inputs = model.advanced
	}
	switch key.String() {
	case "tab", "down":
		inputs[model.focus].Blur()
		model.focus = (model.focus + 1) % len(inputs)
		inputs[model.focus].Focus()
		model.storeFocusedInputs(inputs, advanced)
		return model, nil
	case "shift+tab", "up":
		inputs[model.focus].Blur()
		model.focus--
		if model.focus < 0 {
			model.focus = len(inputs) - 1
		}
		inputs[model.focus].Focus()
		model.storeFocusedInputs(inputs, advanced)
		return model, nil
	case "ctrl+a":
		if !advanced {
			model.blurInputs(false)
			model.focus = 0
			model.advanced[0].Focus()
			model.screen = profileSetupScreenAdvanced
			return model, nil
		}
	case "ctrl+e":
		if advanced {
			model.blurInputs(true)
			model.focus = 0
			model.inputs[0].Focus()
			model.screen = profileSetupScreenIntake
			return model, nil
		}
	case "enter":
		if model.focus < len(inputs)-1 {
			inputs[model.focus].Blur()
			model.focus++
			inputs[model.focus].Focus()
			model.storeFocusedInputs(inputs, advanced)
			return model, nil
		}
		model.storeFocusedInputs(inputs, advanced)
		if _, err := model.optionsFromInputs(); err != nil {
			model.err = err.Error()
			return model, nil
		}
		if advanced && model.singleStage != "" {
			model.done = true
			return model, tea.Quit
		}
		model.err = ""
		model.screen = profileSetupScreenRepository
		return model, nil
	}
	var cmd tea.Cmd
	inputs[model.focus], cmd = inputs[model.focus].Update(key)
	model.storeFocusedInputs(inputs, advanced)
	return model, cmd
}

func (model profileSetupModel) updateRepositoryChoice(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "enter" {
		selected, ok := model.repositoryList.SelectedItem().(profileListItem)
		if !ok {
			return model, nil
		}
		model.repositoryMode = selected.kind
		model.err = ""
		switch selected.kind {
		case "create":
			model.repositoryInputs[0].SetValue("")
			model.repositoryInputs[1].SetValue("")
			model.refreshPlanPreview()
			model.screen = profileSetupScreenReview
			return model, nil
		case "existing":
			model.repositoryInputs[1].SetValue("")
			model.focus = 0
			model.repositoryInputs[0].Focus()
		case "github":
			model.focus = 1
			model.repositoryInputs[1].Focus()
		}
		model.screen = profileSetupScreenRepositoryDetails
		return model, nil
	}
	var cmd tea.Cmd
	model.repositoryList, cmd = model.repositoryList.Update(key)
	return model, cmd
}

func (model profileSetupModel) updateRepositoryDetails(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	indexes := model.repositoryDetailIndexes()
	switch key.String() {
	case "tab", "down":
		model.repositoryInputs[model.focus].Blur()
		position := 0
		for index, inputIndex := range indexes {
			if inputIndex == model.focus {
				position = index
				break
			}
		}
		model.focus = indexes[(position+1)%len(indexes)]
		model.repositoryInputs[model.focus].Focus()
		return model, nil
	case "shift+tab", "up":
		model.repositoryInputs[model.focus].Blur()
		position := 0
		for index, inputIndex := range indexes {
			if inputIndex == model.focus {
				position = index
				break
			}
		}
		position--
		if position < 0 {
			position = len(indexes) - 1
		}
		model.focus = indexes[position]
		model.repositoryInputs[model.focus].Focus()
		return model, nil
	case "enter":
		if model.repositoryMode == "existing" {
			path := expandUserPath(strings.TrimSpace(model.repositoryInputs[0].Value()))
			if path == "" {
				model.err = "existing repository path is required"
				return model, nil
			}
			if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					model.err = "no Git repository exists at that path; go back and choose create"
				} else {
					model.err = err.Error()
				}
				return model, nil
			}
		}
		if model.repositoryMode == "github" {
			repositoryURL := strings.TrimSpace(model.repositoryInputs[1].Value())
			if repositoryURL == "" {
				model.err = "GitHub repository URL is required"
				return model, nil
			}
			if err := validateGitHubRepositoryURL(repositoryURL); err != nil {
				model.err = err.Error()
				return model, nil
			}
		}
		if _, err := model.optionsFromInputs(); err != nil {
			model.err = err.Error()
			return model, nil
		}
		model.repositoryInputs[model.focus].Blur()
		model.err = ""
		model.refreshPlanPreview()
		model.screen = profileSetupScreenReview
		return model, nil
	}
	var cmd tea.Cmd
	model.repositoryInputs[model.focus], cmd = model.repositoryInputs[model.focus].Update(key)
	return model, cmd
}

func (model profileSetupModel) updateStackCompose(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "enter" {
		path := expandUserPath(strings.TrimSpace(model.stackComposeInput.Value()))
		if path == "" {
			model.err = "Docker Compose file path is required"
			return model, nil
		}
		services, err := inspectComposeFile(path)
		if err != nil {
			model.err = err.Error()
			return model, nil
		}
		compose, err := os.ReadFile(path)
		if err != nil {
			model.err = fmt.Sprintf("read Compose file: %v", err)
			return model, nil
		}
		options := withStackAddDefaults(stackAddOptions{Compose: path}, services)
		resources := []stackPublicResource{}
		if resource, ok := suggestedStackResource(options.Name, services); ok {
			resources = append(resources, resource)
		}
		model.stackCompose = string(compose)
		model.stackServices = services
		model.stackOriginalName = ""
		model.stackResources = resources
		model.stackResourceTable = newStackResourceTable(resources)
		model.stackEnvironment = ""
		model.stackEnvironmentKeys = nil
		model.stackEnvironmentDirty = false
		model.stackInputs = stackEditorInputs(options)
		model.stackComposeInput.Blur()
		model.focus = 0
		model.stackInputs[0].Focus()
		model.err = ""
		model.screen = profileSetupScreenStackEditor
		return model, nil
	}
	var command tea.Cmd
	model.stackComposeInput, command = model.stackComposeInput.Update(key)
	return model, command
}

func (model profileSetupModel) updateStacks(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "a", "A":
		model.err = ""
		model.stackNotice = ""
		model.stackComposeInput.SetValue("")
		model.stackComposeInput.Focus()
		model.screen = profileSetupScreenStackCompose
	case "enter", "e", "E":
		stack, ok := model.selectedStack()
		if !ok {
			model.err = "no stack selected; press a to add one"
			return model, nil
		}
		model.openStackEditor(stack)
	case "d", "D":
		if _, ok := model.selectedStack(); !ok {
			model.err = "no stack selected"
			return model, nil
		}
		model.err = ""
		model.screen = profileSetupScreenStackDeleteConfirm
	case "r", "R":
		stack, ok := model.selectedStack()
		if !ok {
			model.err = "no stack selected"
			return model, nil
		}
		model.singleStage = "stack:" + stack.Name
		model.done = true
		return model, tea.Quit
	case "v", "V":
		repositoryPath, err := model.selectedRepositoryPath()
		if err != nil {
			model.err = err.Error()
			return model, nil
		}
		diff, err := stackRepositoryDiff(context.Background(), repositoryPath)
		if err != nil {
			model.err = err.Error()
			return model, nil
		}
		model.stackDiffViewport.SetContent(diff)
		model.stackDiffViewport.GotoTop()
		model.err = ""
		model.screen = profileSetupScreenStackDiff
	case "g", "G":
		repositoryPath, err := model.selectedRepositoryPath()
		if err != nil {
			model.err = err.Error()
			return model, nil
		}
		if err := stageStackChanges(context.Background(), repositoryPath); err != nil {
			model.err = err.Error()
			return model, nil
		}
		model.stackNotice = "All changes under stacks/ are staged."
		model.refreshStacks()
	case "c", "C":
		model.stackCommitInput.SetValue("")
		model.stackCommitInput.Focus()
		model.err = ""
		model.screen = profileSetupScreenStackCommit
	case "y", "Y":
		switch {
		case model.stackGitStatus != "clean":
			model.err = "stack changes are uncommitted; press v to review, g to stage, and c to commit"
		case model.stackNeedsPush:
			model.err = "the stack commit has not been pushed to origin"
		default:
			model.singleStage = "stacks"
			model.done = true
			return model, tea.Quit
		}
	case "p", "P":
		repositoryPath, err := model.selectedRepositoryPath()
		if err != nil {
			model.err = err.Error()
			return model, nil
		}
		if err := pushStackRepository(context.Background(), repositoryPath); err != nil {
			model.err = err.Error()
			return model, nil
		}
		model.stackNotice = "Pushed the current configuration branch to origin."
		model.refreshStacks()
	default:
		var command tea.Cmd
		model.stackTable, command = model.stackTable.Update(key)
		return model, command
	}
	return model, nil
}

func (model profileSetupModel) updateStackEditor(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if model.focus == 0 {
		switch key.String() {
		case "ctrl+s":
			return model.saveStackEditor()
		case "tab", "down":
			model.stackInputs[0].Blur()
			model.focus = 1
			model.stackResourceTable.Focus()
			return model, nil
		}
		var command tea.Cmd
		model.stackInputs[0], command = model.stackInputs[0].Update(key)
		return model, command
	}
	switch key.String() {
	case "ctrl+s":
		return model.saveStackEditor()
	case "tab":
		model.stackResourceTable.Blur()
		model.focus = 0
		model.stackInputs[0].Focus()
		return model, nil
	case "a", "A":
		model.openStackResourceEditor(-1)
		return model, nil
	case "e", "E", "enter":
		if len(model.stackResources) == 0 {
			model.openStackResourceEditor(-1)
		} else {
			model.openStackResourceEditor(model.stackResourceTable.Cursor())
		}
		return model, nil
	case "d", "D":
		index := model.stackResourceTable.Cursor()
		if index >= 0 && index < len(model.stackResources) {
			model.stackResources = append(model.stackResources[:index], model.stackResources[index+1:]...)
			model.stackResourceTable = newStackResourceTable(model.stackResources)
		}
		return model, nil
	case "n", "N":
		model.stackEnvironmentInput.SetValue("")
		model.stackEnvironmentInput.Focus()
		model.err = ""
		model.screen = profileSetupScreenStackEnvironment
		return model, nil
	}
	var command tea.Cmd
	model.stackResourceTable, command = model.stackResourceTable.Update(key)
	return model, command
}

func (model profileSetupModel) saveStackEditor() (tea.Model, tea.Cmd) {
	name := strings.TrimSpace(model.stackInputs[0].Value())
	metadata := stackMetadata{Version: 1, PublicResources: model.stackResources}
	if err := validateStackMetadata(name, metadata, model.stackServices); err != nil {
		model.err = err.Error()
		return model, nil
	}
	options := stackAddOptions{Name: name, Resources: model.stackResources}
	repositoryPath, err := model.selectedRepositoryPath()
	if err != nil {
		model.err = err.Error()
		return model, nil
	}
	if err := writeEditableStack(repositoryPath, model.stackOriginalName, options, []byte(model.stackCompose)); err != nil {
		model.err = err.Error()
		return model, nil
	}
	environmentChanged := model.stackEnvironmentDirty ||
		(model.stackOriginalName != "" && model.stackOriginalName != options.Name)
	if model.selectedIndex >= 0 && model.selectedIndex < len(model.profiles) {
		choice := &model.profiles[model.selectedIndex]
		if choice.Secrets.StackEnvironments == nil {
			choice.Secrets.StackEnvironments = map[string]string{}
		}
		if model.stackOriginalName != "" && model.stackOriginalName != options.Name {
			delete(choice.Secrets.StackEnvironments, model.stackOriginalName)
		}
		if model.stackEnvironment == "" {
			delete(choice.Secrets.StackEnvironments, options.Name)
		} else {
			choice.Secrets.StackEnvironments[options.Name] = model.stackEnvironment
		}
		if environmentChanged && model.profileStore != nil {
			if err := model.profileStore.SaveSecrets(choice.Profile.ID, choice.Secrets); err != nil {
				model.err = err.Error()
				return model, nil
			}
		}
	}
	action := "updated"
	if model.stackOriginalName == "" {
		action = "added"
	}
	model.stackNotice = fmt.Sprintf("Stack %s. Press v to review the diff, g to stage, then c to commit.", action)
	model.err = ""
	model.screen = profileSetupScreenStacks
	model.refreshStacks()
	if environmentChanged && model.stackGitStatus == "clean" {
		model.stackNotice = "Runtime environment updated outside Git. Press r to deploy this stack."
	}
	model.stackTable.Focus()
	return model, nil
}

func (model profileSetupModel) updateStackResourceEditor(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "ctrl+s":
		return model.saveStackResourceEditor()
	case "tab", "down":
		model.stackResourceInputs[model.focus].Blur()
		model.focus = (model.focus + 1) % len(model.stackResourceInputs)
		model.stackResourceInputs[model.focus].Focus()
		return model, nil
	case "shift+tab", "up":
		model.stackResourceInputs[model.focus].Blur()
		model.focus--
		if model.focus < 0 {
			model.focus = len(model.stackResourceInputs) - 1
		}
		model.stackResourceInputs[model.focus].Focus()
		return model, nil
	case "enter":
		if model.focus < len(model.stackResourceInputs)-1 {
			model.stackResourceInputs[model.focus].Blur()
			model.focus++
			model.stackResourceInputs[model.focus].Focus()
			return model, nil
		}
		return model.saveStackResourceEditor()
	}
	var command tea.Cmd
	model.stackResourceInputs[model.focus], command = model.stackResourceInputs[model.focus].Update(key)
	return model, command
}

func (model profileSetupModel) saveStackResourceEditor() (tea.Model, tea.Cmd) {
	resource, err := stackResourceFromInputs(model.stackResourceInputs)
	if err != nil {
		model.err = err.Error()
		return model, nil
	}
	resources := append([]stackPublicResource(nil), model.stackResources...)
	if model.stackResourceIndex < 0 {
		resources = append(resources, resource)
	} else if model.stackResourceIndex < len(resources) {
		resources[model.stackResourceIndex] = resource
	} else {
		model.err = "selected public resource no longer exists"
		return model, nil
	}
	name := strings.TrimSpace(model.stackInputs[0].Value())
	if err := validateStackMetadata(name, stackMetadata{Version: 1, PublicResources: resources}, model.stackServices); err != nil {
		model.err = err.Error()
		return model, nil
	}
	model.stackResources = resources
	model.stackResourceTable = newStackResourceTable(resources)
	model.stackResourceTable.Focus()
	model.focus = 1
	model.err = ""
	model.screen = profileSetupScreenStackEditor
	return model, nil
}

func (model profileSetupModel) updateStackEnvironment(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "enter" {
		path := strings.TrimSpace(model.stackEnvironmentInput.Value())
		if path == "" {
			model.stackEnvironment = ""
			model.stackEnvironmentKeys = nil
		} else {
			environment, keys, err := readStackEnvironmentFile(path)
			if err != nil {
				model.err = err.Error()
				return model, nil
			}
			model.stackEnvironment = environment
			model.stackEnvironmentKeys = keys
		}
		model.stackEnvironmentDirty = true
		model.stackEnvironmentInput.Blur()
		model.focus = 1
		model.stackResourceTable.Focus()
		model.err = ""
		model.screen = profileSetupScreenStackEditor
		return model, nil
	}
	var command tea.Cmd
	model.stackEnvironmentInput, command = model.stackEnvironmentInput.Update(key)
	return model, command
}

func (model profileSetupModel) updateStackDeleteConfirm(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "y", "Y":
		stack, ok := model.selectedStack()
		if !ok {
			model.err = "no stack selected"
			model.screen = profileSetupScreenStacks
			return model, nil
		}
		repositoryPath, err := model.selectedRepositoryPath()
		if err != nil {
			model.err = err.Error()
			model.screen = profileSetupScreenStacks
			return model, nil
		}
		if err := removeEditableStack(repositoryPath, stack.Name); err != nil {
			model.err = err.Error()
			model.screen = profileSetupScreenStacks
			return model, nil
		}
		if model.selectedIndex >= 0 && model.selectedIndex < len(model.profiles) {
			choice := &model.profiles[model.selectedIndex]
			delete(choice.Secrets.StackEnvironments, stack.Name)
			if model.profileStore != nil {
				if err := model.profileStore.SaveSecrets(choice.Profile.ID, choice.Secrets); err != nil {
					model.err = err.Error()
					model.screen = profileSetupScreenStacks
					return model, nil
				}
			}
		}
		model.stackNotice = fmt.Sprintf("Stack %s removed. Review and commit the deletion before deployment.", stack.Name)
		model.err = ""
		model.refreshStacks()
		model.screen = profileSetupScreenStacks
	case "n", "N":
		model.screen = profileSetupScreenStacks
	}
	return model, nil
}

func (model profileSetupModel) updateStackDiff(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	var command tea.Cmd
	model.stackDiffViewport, command = model.stackDiffViewport.Update(key)
	return model, command
}

func (model profileSetupModel) updateStackCommit(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "enter" {
		repositoryPath, err := model.selectedRepositoryPath()
		if err != nil {
			model.err = err.Error()
			return model, nil
		}
		message := strings.TrimSpace(model.stackCommitInput.Value())
		if err := commitStackChanges(context.Background(), repositoryPath, message); err != nil {
			model.err = err.Error()
			return model, nil
		}
		model.stackCommitInput.Blur()
		model.stackNotice = fmt.Sprintf("Committed stack changes: %s. Press y to synchronize the server.", message)
		model.err = ""
		model.screen = profileSetupScreenStacks
		model.refreshStacks()
		model.stackTable.Focus()
		return model, nil
	}
	var command tea.Cmd
	model.stackCommitInput, command = model.stackCommitInput.Update(key)
	return model, command
}

func stackEditorInputs(options stackAddOptions) []textinput.Model {
	return newSetupInputs([]setupInputField{{label: "Stack name", value: options.Name}})
}

func stackResourceInputs(resource stackPublicResource) []textinput.Model {
	sso := "no"
	if resource.SSO {
		sso = "yes"
	}
	return newSetupInputs([]setupInputField{
		{label: "Resource ID", value: resource.ID},
		{label: "Service to publish", value: resource.Service},
		{label: "Container port", value: func() string {
			if resource.Port == 0 {
				return ""
			}
			return strconv.Itoa(resource.Port)
		}()},
		{label: "Public subdomain", value: resource.Subdomain},
		{label: "Pangolin display name", value: resource.Name},
		{label: "Protocol (http/https)", value: firstNonEmpty(resource.Protocol, "http")},
		{label: "Health-check path (blank disables)", value: resource.Healthcheck.Path},
		{label: "Require Pangolin SSO (yes/no)", value: sso},
	})
}

func stackResourceFromInputs(inputs []textinput.Model) (stackPublicResource, error) {
	ssoValue := strings.ToLower(strings.TrimSpace(inputs[7].Value()))
	if ssoValue != "yes" && ssoValue != "no" {
		return stackPublicResource{}, errors.New("Require Pangolin SSO must be yes or no")
	}
	path := strings.TrimSpace(inputs[6].Value())
	resource := stackPublicResource{
		ID: strings.TrimSpace(inputs[0].Value()), Service: strings.TrimSpace(inputs[1].Value()),
		Subdomain: strings.TrimSpace(inputs[3].Value()), Name: strings.TrimSpace(inputs[4].Value()),
		Protocol: strings.ToLower(strings.TrimSpace(inputs[5].Value())), SSO: ssoValue == "yes",
		Healthcheck: stackResourceHealthcheck{Enabled: path != "", Path: path},
	}
	resource.Port, _ = strconv.Atoi(strings.TrimSpace(inputs[2].Value()))
	return resource, nil
}

func (model *profileSetupModel) openStackEditor(stack editableStack) {
	options := stackAddOptions{Name: stack.Name}
	model.stackOriginalName = stack.Name
	model.stackCompose = stack.Compose
	model.stackServices = stack.Services
	model.stackResources = append([]stackPublicResource(nil), stack.Metadata.PublicResources...)
	model.stackResourceTable = newStackResourceTable(model.stackResources)
	model.stackInputs = stackEditorInputs(options)
	model.stackEnvironment = ""
	model.stackEnvironmentKeys = nil
	model.stackEnvironmentDirty = false
	if model.selectedIndex >= 0 && model.selectedIndex < len(model.profiles) {
		model.stackEnvironment = model.profiles[model.selectedIndex].Secrets.StackEnvironments[stack.Name]
		_, keys, _ := readStackEnvironmentContent(model.stackEnvironment)
		model.stackEnvironmentKeys = keys
	}
	model.focus = 1
	model.stackInputs[0].Blur()
	model.stackResourceTable.Focus()
	model.err = ""
	model.screen = profileSetupScreenStackEditor
}

func (model *profileSetupModel) openStackResourceEditor(index int) {
	resource := stackPublicResource{Protocol: "http", SSO: true, Healthcheck: stackResourceHealthcheck{Enabled: true, Path: "/"}}
	if index >= 0 && index < len(model.stackResources) {
		resource = model.stackResources[index]
	} else if len(model.stackServices) > 0 {
		service := model.stackServices[0]
		resource.ID = slugifyStackValue(service.Name)
		resource.Service = service.Name
		resource.Subdomain = slugifyStackValue(service.Name)
		resource.Name = titleFromSlug(service.Name)
		if len(service.ContainerPorts) > 0 {
			resource.Port = service.ContainerPorts[0]
		}
	}
	model.stackResourceIndex = index
	model.stackResourceInputs = stackResourceInputs(resource)
	model.focus = 0
	model.stackResourceInputs[0].Focus()
	model.err = ""
	model.screen = profileSetupScreenStackResourceEditor
}

func newStackResourceTable(resources []stackPublicResource) table.Model {
	rows := make([]table.Row, 0, len(resources))
	for _, resource := range resources {
		rows = append(rows, table.Row{
			resource.ID,
			resource.Subdomain,
			fmt.Sprintf("%s:%d", resource.Service, resource.Port),
		})
	}
	return table.New(
		table.WithColumns([]table.Column{
			{Title: "ID", Width: 18},
			{Title: "Subdomain", Width: 22},
			{Title: "Target", Width: 24},
		}),
		table.WithRows(rows),
		table.WithHeight(clampInt(len(rows)+1, 2, 10)),
		table.WithWidth(70),
		table.WithFocused(true),
	)
}

func (model profileSetupModel) selectedStack() (editableStack, bool) {
	cursor := model.stackTable.Cursor()
	if cursor < 0 || cursor >= len(model.stacks) {
		return editableStack{}, false
	}
	return model.stacks[cursor], true
}

func (model profileSetupModel) selectedRepositoryPath() (string, error) {
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
		return "", errors.New("no profile selected")
	}
	path := strings.TrimSpace(model.profiles[model.selectedIndex].Profile.ConfigRepositoryPath)
	if path == "" {
		return "", errors.New("configuration repository is not ready; run Platform once before managing stacks")
	}
	if _, err := os.Stat(filepath.Join(expandUserPath(path), ".git")); err != nil {
		return "", errors.New("configuration repository is not ready; run Platform once before managing stacks")
	}
	return path, nil
}

func (model *profileSetupModel) refreshStacks() {
	path, err := model.selectedRepositoryPath()
	if err != nil {
		model.err = err.Error()
		return
	}
	stacks, err := loadEditableStacks(path)
	if err != nil {
		model.err = err.Error()
		return
	}
	model.stacks = stacks
	choice := model.profiles[model.selectedIndex]
	model.stackTable = newStackTable(stacks, choice.Profile.BaseDomain, &choice.State)
	model.resizeStackTable()
	status, err := stackRepositoryStatus(context.Background(), path)
	if err != nil {
		model.err = err.Error()
		return
	}
	model.stackGitStatus = status
	if status != "clean" {
		model.stackHead = ""
		model.stackNeedsPush = false
		model.stackSyncStatus = "commit required"
		model.err = ""
		return
	}
	head, err := stackRepositoryHead(context.Background(), path)
	if err != nil {
		model.err = err.Error()
		return
	}
	model.stackHead = head
	needsPush, err := stackRepositoryNeedsPush(context.Background(), path, head)
	if err != nil {
		model.err = err.Error()
		return
	}
	model.stackNeedsPush = needsPush
	switch {
	case needsPush:
		model.stackSyncStatus = "push required"
	case choice.State.StackRepositoryCommit != head:
		model.stackSyncStatus = "sync required"
	default:
		model.stackSyncStatus = "in sync"
	}
	model.err = ""
}

func inspectComposeFile(path string) ([]composeServiceSummary, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read Compose file: %w", err)
	}
	return inspectComposeServices(data)
}

func (model profileSetupModel) repositoryDetailIndexes() []int {
	if model.repositoryMode == "existing" {
		return []int{0}
	}
	return []int{1, 0}
}

func (model profileSetupModel) updateProfileReview(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "r", "R":
		if _, err := model.optionsFromInputs(); err != nil {
			model.err = err.Error()
			return model, nil
		}
		model.done = true
		return model, tea.Quit
	case "e", "E":
		model.err = ""
		model.focus = 0
		model.inputs[0].Focus()
		model.screen = profileSetupScreenIntake
	case "a", "A":
		model.err = ""
		model.focus = 0
		model.advanced[0].Focus()
		model.screen = profileSetupScreenAdvanced
	case "c", "C":
		model.err = ""
		model.screen = profileSetupScreenRepository
	case "d", "D":
		if model.selectedIndex >= 0 {
			model.screen = profileSetupScreenDashboard
		}
	}
	return model, nil
}

func (model profileSetupModel) updateProfileDeleteConfirm(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "y", "Y":
		if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
			model.err = "no profile selected"
			model.screen = profileSetupScreenPicker
			return model, nil
		}
		model.deleteProfileID = model.profiles[model.selectedIndex].Profile.ID
		return model, tea.Quit
	case "n", "N":
		model.screen = profileSetupScreenDashboard
	}
	return model, nil
}

func (model *profileSetupModel) goBack() {
	switch model.screen {
	case profileSetupScreenPicker:
		return
	case profileSetupScreenDashboard:
		model.screen = profileSetupScreenPicker
	case profileSetupScreenIntake:
		if model.selectedIndex >= 0 {
			model.screen = profileSetupScreenDashboard
		} else {
			model.screen = profileSetupScreenPicker
		}
	case profileSetupScreenAdvanced:
		model.singleStage = ""
		if model.selectedIndex >= 0 {
			model.screen = profileSetupScreenDashboard
		} else {
			model.screen = profileSetupScreenIntake
		}
	case profileSetupScreenRepository:
		model.screen = profileSetupScreenIntake
	case profileSetupScreenRepositoryDetails:
		model.screen = profileSetupScreenRepository
	case profileSetupScreenStacks:
		model.screen = profileSetupScreenDashboard
	case profileSetupScreenStackCompose:
		model.screen = profileSetupScreenStacks
	case profileSetupScreenStackEditor:
		model.screen = profileSetupScreenStacks
	case profileSetupScreenStackResourceEditor, profileSetupScreenStackEnvironment:
		model.screen = profileSetupScreenStackEditor
	case profileSetupScreenStackDeleteConfirm:
		model.screen = profileSetupScreenStacks
	case profileSetupScreenStackDiff:
		model.screen = profileSetupScreenStacks
	case profileSetupScreenStackCommit:
		model.stackCommitInput.Blur()
		model.screen = profileSetupScreenStacks
	case profileSetupScreenReview:
		if model.selectedIndex >= 0 {
			model.screen = profileSetupScreenDashboard
		} else {
			model.screen = profileSetupScreenIntake
		}
	case profileSetupScreenDeleteConfirm:
		model.screen = profileSetupScreenDashboard
	}
}

func (model *profileSetupModel) setInputsFromChoice(fresh bool) {
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
		return
	}
	choice := model.profiles[model.selectedIndex]
	options := setupCLIOptions{
		IP:                    choice.Profile.IP,
		ProfileID:             choice.Profile.ID,
		Name:                  choice.Profile.Name,
		InitialSSHUser:        choice.Profile.InitialSSHUser,
		AdminUser:             choice.Profile.AdminUser,
		PrivateKeyPath:        choice.Profile.PrivateKeyPath,
		BaseDomain:            choice.Profile.BaseDomain,
		LetsEncryptEmail:      choice.Profile.LetsEncryptEmail,
		PangolinAdminEmail:    firstNonEmpty(choice.Profile.PangolinAdminEmail, choice.Profile.LetsEncryptEmail),
		PangolinAdminPassword: choice.Secrets.PangolinAdminPassword,
		ConfigRepositoryPath:  choice.Profile.ConfigRepositoryPath,
		Fresh:                 fresh,
	}
	if fresh {
		options.ProfileID = ""
		options.Name = choice.Profile.Name + " fresh"
		options.ConfigRepositoryPath = ""
		if completedSetupStages(choice.State)["bootstrap"] && options.AdminUser != "" {
			options.InitialSSHUser = options.AdminUser
		}
	}
	model.setInputsFromOptions(options)
}

func (model *profileSetupModel) setInputsFromOptions(options setupCLIOptions) {
	model.inputs = setupProfileInputs(options)
	model.advanced = setupAdvancedInputs(options)
	model.repositoryInputs = setupRepositoryInputs(options)
	switch {
	case options.GitHubRepositoryURL != "":
		model.repositoryMode = "github"
		model.repositoryList.Select(2)
	case options.ConfigRepositoryPath != "":
		if _, err := os.Stat(expandUserPath(options.ConfigRepositoryPath)); errors.Is(err, os.ErrNotExist) {
			model.repositoryMode = "create"
			model.repositoryList.Select(0)
		} else {
			model.repositoryMode = "existing"
			model.repositoryList.Select(1)
		}
	default:
		model.repositoryMode = "create"
		model.repositoryList.Select(0)
	}
	model.focus = 0
	model.inputs[0].Focus()
}

func (model *profileSetupModel) blurInputs(advanced bool) {
	inputs := model.inputs
	if advanced {
		inputs = model.advanced
	}
	for index := range inputs {
		inputs[index].Blur()
	}
	model.storeFocusedInputs(inputs, advanced)
}

func (model *profileSetupModel) storeFocusedInputs(inputs []textinput.Model, advanced bool) {
	if advanced {
		model.advanced = inputs
		return
	}
	model.inputs = inputs
}

func (model *profileSetupModel) refreshDashboard() {
	model.pangolinStatus = pangolinRegistrationUnknown
	model.pangolinError = ""
	model.showSetupToken = false
	model.stacks = nil
	model.stackGitStatus = ""
	model.stackHead = ""
	model.stackNeedsPush = false
	model.stackSyncStatus = ""
	model.stackTable = newStackTable(nil, "", nil)
	model.resizeStackTable()
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
		model.stageTable = newProfileStageTable(nil)
		return
	}
	state := model.profiles[model.selectedIndex].State
	model.stageTable = newProfileStageTable(&state)
	path := model.profiles[model.selectedIndex].Profile.ConfigRepositoryPath
	if path != "" {
		if _, err := os.Stat(filepath.Join(expandUserPath(path), ".git")); err == nil {
			model.refreshStacks()
			model.stackTable.Blur()
		}
	}
}

func (model *profileSetupModel) checkPangolinRegistration() tea.Cmd {
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
		return nil
	}
	choice := model.profiles[model.selectedIndex]
	if choice.Profile.BaseDomain == "" {
		model.pangolinStatus = pangolinRegistrationUnknown
		model.pangolinError = ""
		return nil
	}
	model.pangolinStatus = pangolinRegistrationChecking
	model.pangolinError = ""
	profile := choice.Profile
	return func() tea.Msg {
		complete, err := pangolinInitialSetupComplete(context.Background(), pangolinRegistrationHTTPClient, "https://pangolin."+profile.BaseDomain)
		return pangolinRegistrationStatusMsg{profileID: profile.ID, complete: complete, err: err}
	}
}

func (model profileSetupModel) selectedProfileHasSetupToken() bool {
	return model.selectedIndex >= 0 &&
		model.selectedIndex < len(model.profiles) &&
		model.profiles[model.selectedIndex].Secrets.PangolinSetupToken != ""
}

func (model *profileSetupModel) refreshPlanPreview() {
	options, err := model.optionsFromInputs()
	if err != nil {
		model.planViewport.SetContent(err.Error())
		return
	}
	config := setupConfig{
		Mode:               setupModeFullRun,
		Host:               options.IP,
		InitialSSHUser:     firstNonEmpty(options.InitialSSHUser, "root"),
		AdminUser:          firstNonEmpty(options.AdminUser, "aegisadmin"),
		PrivateKeyPath:     expandUserPath(firstNonEmpty(options.PrivateKeyPath, defaultKeygenConfig().Path)),
		AdminPublicKeyPath: publicKeyPath(expandUserPath(firstNonEmpty(options.PrivateKeyPath, defaultKeygenConfig().Path))),
		BaseDomain:         options.BaseDomain,
		LetsEncryptEmail:   options.LetsEncryptEmail,
		ProfileID:          "(new profile)",
		ServerSecret:       "generated-placeholder",
	}
	if !options.Fresh {
		config.ProfileID = firstNonEmpty(options.ProfileID, "(new profile)")
	}
	model.planViewport.SetContent(setupPlanSummary(config))
	model.planViewport.GotoTop()
}

func (model profileSetupModel) optionsFromInputs() (setupCLIOptions, error) {
	value := func(inputs []textinput.Model, index int) string {
		return strings.TrimSpace(inputs[index].Value())
	}
	options := setupCLIOptions{
		IP:                    value(model.inputs, 0),
		PrivateKeyPath:        expandUserPath(value(model.inputs, 1)),
		BaseDomain:            value(model.inputs, 2),
		LetsEncryptEmail:      value(model.inputs, 3),
		Name:                  value(model.advanced, 0),
		InitialSSHUser:        firstNonEmpty(value(model.advanced, 1), "root"),
		AdminUser:             firstNonEmpty(value(model.advanced, 2), "aegisadmin"),
		PangolinAdminEmail:    value(model.advanced, 3),
		PangolinAdminPassword: value(model.advanced, 4),
		Fresh:                 model.fresh,
	}
	switch model.repositoryMode {
	case "create":
		options.ConfigRepositoryPath = expandUserPath(strings.TrimSpace(model.repositoryInputs[0].Value()))
	case "existing":
		options.ConfigRepositoryPath = expandUserPath(strings.TrimSpace(model.repositoryInputs[0].Value()))
	case "github":
		options.ConfigRepositoryPath = expandUserPath(strings.TrimSpace(model.repositoryInputs[0].Value()))
		options.GitHubRepositoryURL = strings.TrimSpace(model.repositoryInputs[1].Value())
	}
	if model.selectedIndex >= 0 && model.selectedIndex < len(model.profiles) {
		options.ProfileID = model.profiles[model.selectedIndex].Profile.ID
		options.IP = model.profiles[model.selectedIndex].Profile.IP
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
		ServerSecret:       "generated-placeholder",
	}
	if err := validateFullRunConfig(config); err != nil {
		return setupCLIOptions{}, err
	}
	return options, nil
}

func (model profileSetupModel) optionsForSelectedProfile() (setupCLIOptions, error) {
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
		return setupCLIOptions{}, errors.New("no profile selected")
	}
	profile := model.profiles[model.selectedIndex].Profile
	options := setupCLIOptions{
		IP:                    profile.IP,
		ProfileID:             profile.ID,
		Name:                  profile.Name,
		InitialSSHUser:        profile.InitialSSHUser,
		AdminUser:             profile.AdminUser,
		PrivateKeyPath:        profile.PrivateKeyPath,
		BaseDomain:            profile.BaseDomain,
		LetsEncryptEmail:      profile.LetsEncryptEmail,
		PangolinAdminEmail:    firstNonEmpty(strings.TrimSpace(model.advanced[3].Value()), profile.PangolinAdminEmail, profile.LetsEncryptEmail),
		PangolinAdminPassword: strings.TrimSpace(model.advanced[4].Value()),
	}
	switch model.repositoryMode {
	case "create", "existing":
		options.ConfigRepositoryPath = expandUserPath(strings.TrimSpace(model.repositoryInputs[0].Value()))
	case "github":
		options.ConfigRepositoryPath = expandUserPath(strings.TrimSpace(model.repositoryInputs[0].Value()))
		options.GitHubRepositoryURL = strings.TrimSpace(model.repositoryInputs[1].Value())
	}
	return options, nil
}

func (model profileSetupModel) selectedDashboardStage() (string, error) {
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
		return "", errors.New("no profile selected")
	}
	cursor := model.stageTable.Cursor()
	if cursor < 0 || cursor >= len(dashboardStageOrder) {
		return "", errors.New("no setup stage selected")
	}
	return dashboardStageOrder[cursor], nil
}

func (model profileSetupModel) View() string {
	var builder strings.Builder
	builder.WriteString(setupTitleStyle.Render("AegisNode setup"))
	builder.WriteString("\n")
	builder.WriteString(setupHelpStyle.Render("Profile-aware setup manages the server platform and standalone application stacks."))
	builder.WriteString("\n\n")

	switch model.screen {
	case profileSetupScreenPicker:
		builder.WriteString(model.profileList.View())
	case profileSetupScreenDashboard:
		builder.WriteString(model.dashboardView())
	case profileSetupScreenIntake:
		builder.WriteString(model.inputView(false))
	case profileSetupScreenAdvanced:
		builder.WriteString(model.inputView(true))
	case profileSetupScreenRepository:
		builder.WriteString(model.repositoryChoiceView())
	case profileSetupScreenRepositoryDetails:
		builder.WriteString(model.repositoryDetailsView())
	case profileSetupScreenStacks:
		builder.WriteString(model.stacksView())
	case profileSetupScreenStackCompose:
		builder.WriteString("Add application stack\n")
		builder.WriteString(setupHelpStyle.Render("Choose any Docker Compose file. AegisNode will inspect its services and guide Pangolin configuration next."))
		builder.WriteString("\n\n")
		builder.WriteString(model.stackComposeInput.View())
	case profileSetupScreenStackEditor:
		builder.WriteString(model.stackEditorView())
	case profileSetupScreenStackResourceEditor:
		builder.WriteString(model.stackResourceEditorView())
	case profileSetupScreenStackEnvironment:
		builder.WriteString("Import runtime environment\n")
		builder.WriteString(setupHelpStyle.Render("The file is saved in the profile secret store and is never copied into Git. Leave the path blank to remove it."))
		builder.WriteString("\n\n")
		builder.WriteString(model.stackEnvironmentInput.View())
	case profileSetupScreenStackDeleteConfirm:
		builder.WriteString(model.stackDeleteConfirmView())
	case profileSetupScreenStackDiff:
		builder.WriteString("Stack repository diff\n\n")
		builder.WriteString(model.stackDiffViewport.View())
	case profileSetupScreenStackCommit:
		builder.WriteString("Commit staged stack changes\n")
		builder.WriteString(setupHelpStyle.Render("Only staged changes under stacks/ are included."))
		builder.WriteString("\n\n")
		builder.WriteString(model.stackCommitInput.View())
	case profileSetupScreenReview:
		builder.WriteString(model.reviewView())
	case profileSetupScreenDeleteConfirm:
		builder.WriteString(model.deleteConfirmView())
	}
	if model.err != "" {
		builder.WriteString("\n\n")
		builder.WriteString(setupErrorStyle.Render(model.err))
	}
	builder.WriteString("\n\n")
	builder.WriteString(model.help.View(profileSetupHelp{
		screen:        model.screen,
		hasProfile:    model.selectedIndex >= 0,
		hasSetupToken: model.selectedProfileHasSetupToken(),
	}))
	return builder.String()
}

func (model profileSetupModel) dashboardView() string {
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
		return "No profile selected."
	}
	choice := model.profiles[model.selectedIndex]
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Dashboard for %s (%s)\n\n", firstNonEmpty(choice.Profile.Name, choice.Profile.IP), choice.Profile.IP))
	builder.WriteString(fmt.Sprintf("Domain: %s\n", firstNonEmpty(choice.Profile.BaseDomain, "(missing)")))
	builder.WriteString(fmt.Sprintf("Email:  %s\n", firstNonEmpty(choice.Profile.LetsEncryptEmail, "(missing)")))
	repositoryPath := choice.Profile.ConfigRepositoryPath
	if repositoryPath == "" {
		builder.WriteString("Config repository: not created; choose one during full setup review.\n\n")
	} else if _, err := os.Stat(expandUserPath(repositoryPath)); errors.Is(err, os.ErrNotExist) {
		builder.WriteString(fmt.Sprintf("Config repository: %s (will be created before the next run)\n\n", repositoryPath))
	} else {
		builder.WriteString(fmt.Sprintf("Config repository: %s\n\n", repositoryPath))
	}
	builder.WriteString(model.pangolinRegistrationView(choice))
	builder.WriteString("\n\n")
	builder.WriteString(model.progress.ViewAs(profileCompletion(&choice.State)))
	builder.WriteString("\n\n")
	builder.WriteString(model.stageTable.View())
	builder.WriteString("\n\n")
	builder.WriteString(fmt.Sprintf("Standalone stacks (%d)\n", len(model.stacks)))
	if len(model.stacks) == 0 {
		builder.WriteString(setupHelpStyle.Render("No stacks found in the profile configuration repository."))
	} else {
		builder.WriteString(model.stackTable.View())
	}
	builder.WriteString("\n")
	builder.WriteString(setupHelpStyle.Render("Platform runs Network, Proxy, and Observability. Press r to run an action; press s to manage stacks."))
	return builder.String()
}

func (model profileSetupModel) stacksView() string {
	var builder strings.Builder
	builder.WriteString("Standalone stacks\n")
	builder.WriteString(setupHelpStyle.Render("Each stack owns its Compose file and public-resource metadata. Changes remain local until reviewed and committed."))
	builder.WriteString("\n\n")
	builder.WriteString("Git: ")
	if model.stackGitStatus == "clean" {
		builder.WriteString("clean\n\n")
	} else {
		changeCount := len(strings.Split(model.stackGitStatus, "\n"))
		builder.WriteString(fmt.Sprintf("%d change(s) • v diff • g stage all • c commit\n\n", changeCount))
	}
	builder.WriteString("Remote: ")
	switch model.stackSyncStatus {
	case "in sync":
		builder.WriteString("in sync\n\n")
	case "":
		builder.WriteString("unknown\n\n")
	default:
		builder.WriteString(setupWarningStyle.Render(model.stackSyncStatus))
		advice := " • press y to sync"
		if model.stackSyncStatus == "commit required" {
			advice = " • review, stage, and commit first"
		} else if model.stackSyncStatus == "push required" {
			advice = " • press p to push first"
		}
		builder.WriteString(setupHelpStyle.Render(advice))
		builder.WriteString("\n\n")
	}
	if len(model.stacks) == 0 {
		builder.WriteString("No application stacks configured. Press a to add one.\n")
	} else {
		builder.WriteString(model.stackTable.View())
	}
	if model.stackNotice != "" {
		builder.WriteString("\n")
		builder.WriteString(setupWarningStyle.Render(model.stackNotice))
	}
	return builder.String()
}

func (model profileSetupModel) stackEditorView() string {
	var builder strings.Builder
	title := "Edit stack"
	if model.stackOriginalName == "" {
		title = "Add stack"
	}
	builder.WriteString(title + "\n")
	builder.WriteString(setupHelpStyle.Render("The Compose file is preserved. Add zero or more Pangolin public resources; zero keeps the stack private."))
	builder.WriteString("\n\nDetected services:\n")
	for _, service := range model.stackServices {
		ports := "no declared ports"
		if len(service.ContainerPorts) > 0 {
			values := make([]string, len(service.ContainerPorts))
			for index, port := range service.ContainerPorts {
				values[index] = strconv.Itoa(port)
			}
			ports = strings.Join(values, ", ")
		}
		builder.WriteString(fmt.Sprintf("  %s: %s\n", service.Name, ports))
	}
	builder.WriteString("\n")
	builder.WriteString("\n")
	builder.WriteString(model.stackInputs[0].View())
	builder.WriteString("\n\nPublic resources\n")
	if len(model.stackResources) == 0 {
		builder.WriteString(setupHelpStyle.Render("None. The stack is private."))
		builder.WriteString("\n")
	} else {
		builder.WriteString(model.stackResourceTable.View())
	}
	builder.WriteString("\nRuntime environment: ")
	if len(model.stackEnvironmentKeys) == 0 {
		builder.WriteString("none")
	} else {
		builder.WriteString(fmt.Sprintf("%d key(s): %s", len(model.stackEnvironmentKeys), strings.Join(model.stackEnvironmentKeys, ", ")))
	}
	builder.WriteString("\n")
	return builder.String()
}

func (model profileSetupModel) stackResourceEditorView() string {
	title := "Add public resource"
	if model.stackResourceIndex >= 0 {
		title = "Edit public resource"
	}
	var builder strings.Builder
	builder.WriteString(title + "\n")
	builder.WriteString(setupHelpStyle.Render("Resource IDs are stable Pangolin identifiers. The health-check path can be blank."))
	builder.WriteString("\n\n")
	for _, input := range model.stackResourceInputs {
		builder.WriteString(input.View())
		builder.WriteByte('\n')
	}
	return builder.String()
}

func (model profileSetupModel) stackDeleteConfirmView() string {
	stack, ok := model.selectedStack()
	if !ok {
		return "No stack selected."
	}
	return fmt.Sprintf(
		"Remove stack %s?\n\nThis deletes its local directory, including Compose and application files. Commit the deletion, then press y in the stack manager to remove the remote deployment.\n",
		stack.Name,
	)
}

func (model profileSetupModel) pangolinRegistrationView(choice profileChoice) string {
	proxyComplete := completedSetupStages(choice.State)["proxy"]
	var builder strings.Builder
	switch {
	case model.pangolinStatus == pangolinRegistrationChecking:
		builder.WriteString("Pangolin registration: checking server...")
	case model.pangolinStatus == pangolinRegistrationIncomplete:
		builder.WriteString(setupWarningStyle.Render("ACTION REQUIRED: Pangolin initial admin registration is incomplete."))
	case model.pangolinStatus == pangolinRegistrationComplete:
		builder.WriteString("Pangolin registration: complete.")
		if !proxyComplete {
			builder.WriteString("\n")
			builder.WriteString(setupWarningStyle.Render("Existing administrator credentials are required to finish Platform setup."))
		}
	case model.pangolinStatus == pangolinRegistrationUnavailable:
		builder.WriteString(setupWarningStyle.Render("Pangolin registration: unable to verify."))
		if model.pangolinError != "" {
			builder.WriteString("\n")
			builder.WriteString(setupHelpStyle.Render(model.pangolinError))
		}
	default:
		if proxyComplete {
			builder.WriteString("Pangolin registration: not checked.")
		} else {
			builder.WriteString("Pangolin registration: waiting for Proxy deployment.")
		}
	}

	if !proxyComplete {
		return builder.String()
	}
	if choice.Secrets.PangolinSetupToken == "" {
		if model.pangolinStatus == pangolinRegistrationIncomplete {
			builder.WriteString("\n")
			builder.WriteString(setupWarningStyle.Render("No saved token. Run Platform once to generate and deploy one."))
		}
		return builder.String()
	}
	if model.showSetupToken {
		builder.WriteString("\n")
		builder.WriteString(fmt.Sprintf("Initial setup URL: https://pangolin.%s/auth/initial-setup\n", choice.Profile.BaseDomain))
		builder.WriteString(fmt.Sprintf("Setup token: %s", choice.Secrets.PangolinSetupToken))
		if model.pangolinStatus == pangolinRegistrationComplete {
			builder.WriteString("\n")
			builder.WriteString(setupHelpStyle.Render("This one-time token is no longer valid after registration."))
		}
	} else {
		builder.WriteString("\n")
		builder.WriteString(setupHelpStyle.Render("Press t to reveal the saved setup token and initial-setup URL."))
	}
	return builder.String()
}

func (model profileSetupModel) inputView(advanced bool) string {
	var builder strings.Builder
	if advanced {
		builder.WriteString("Advanced values\n")
		builder.WriteString(setupHelpStyle.Render("Use this only when defaults need to change."))
		builder.WriteString("\n\n")
		for _, input := range model.advanced {
			builder.WriteString(input.View())
			builder.WriteString("\n")
		}
		return builder.String()
	}
	builder.WriteString("Upfront setup intake\n")
	builder.WriteString(setupHelpStyle.Render("All required values are collected before any remote command runs."))
	builder.WriteString("\n\n")
	for _, input := range model.inputs {
		builder.WriteString(input.View())
		builder.WriteString("\n")
	}
	builder.WriteString("\n")
	builder.WriteString("Server secret: generated and saved in the profile secrets file.\n")
	return builder.String()
}

func (model profileSetupModel) repositoryChoiceView() string {
	var builder strings.Builder
	builder.WriteString(model.repositoryList.View())
	builder.WriteString("\n")
	builder.WriteString(setupHelpStyle.Render("Repository creation or cloning occurs only after plan confirmation and before any SSH commands run."))
	return builder.String()
}

func (model profileSetupModel) repositoryDetailsView() string {
	var builder strings.Builder
	switch model.repositoryMode {
	case "existing":
		builder.WriteString("Use an existing local checkout\n\n")
		builder.WriteString(model.repositoryInputs[0].View())
	case "github":
		builder.WriteString("Clone a GitHub repository\n")
		builder.WriteString(setupHelpStyle.Render("The local path is optional; leave it blank to use the profile default."))
		builder.WriteString("\n\n")
		builder.WriteString(model.repositoryInputs[1].View())
		builder.WriteString("\n")
		builder.WriteString(model.repositoryInputs[0].View())
	}
	return builder.String()
}

func (model profileSetupModel) reviewView() string {
	var builder strings.Builder
	builder.WriteString("Review full setup plan\n\n")
	builder.WriteString(model.planViewport.View())
	builder.WriteString("\n")
	if model.selectedIndex >= 0 && !model.fresh {
		builder.WriteString("Profile action: resume selected profile.\n")
	} else if model.fresh {
		builder.WriteString("Profile action: create a fresh profile for this server.\n")
	} else {
		builder.WriteString("Profile action: create a new profile.\n")
	}
	switch model.repositoryMode {
	case "create":
		path := firstNonEmpty(strings.TrimSpace(model.repositoryInputs[0].Value()), "the profile default path")
		builder.WriteString(fmt.Sprintf("Repository action: create and commit a new local repository at %s.\n", path))
	case "existing":
		builder.WriteString(fmt.Sprintf("Repository action: use existing checkout at %s.\n", strings.TrimSpace(model.repositoryInputs[0].Value())))
	case "github":
		path := firstNonEmpty(strings.TrimSpace(model.repositoryInputs[0].Value()), "the profile default path")
		builder.WriteString(fmt.Sprintf("Repository action: clone %s into %s.\n", strings.TrimSpace(model.repositoryInputs[1].Value()), path))
	}
	builder.WriteString("After confirmation, AegisNode prepares the repository first. SSH execution starts only after repository preparation succeeds.\n")
	return builder.String()
}

func (model profileSetupModel) deleteConfirmView() string {
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
		return "No profile selected."
	}
	profile := model.profiles[model.selectedIndex].Profile
	var builder strings.Builder
	builder.WriteString("Delete saved profile?\n\n")
	builder.WriteString(fmt.Sprintf("Profile: %s\n", firstNonEmpty(profile.Name, profile.IP)))
	builder.WriteString(fmt.Sprintf("IP:      %s\n", profile.IP))
	builder.WriteString("\nThis removes only local profile files, saved secrets, state, and run logs. It does not change the remote server.\n")
	return builder.String()
}

type profileSetupHelp struct {
	screen        profileSetupScreen
	hasProfile    bool
	hasSetupToken bool
}

func (helpMap profileSetupHelp) ShortHelp() []key.Binding {
	switch helpMap.screen {
	case profileSetupScreenPicker:
		return []key.Binding{
			key.NewBinding(key.WithKeys("j", "k"), key.WithHelp("j/k", "select")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
			key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		}
	case profileSetupScreenDashboard:
		bindings := []key.Binding{
			key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "review")),
			key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "run stage")),
			key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "check Pangolin")),
			key.NewBinding(key.WithKeys("j", "k"), key.WithHelp("j/k", "stage")),
			key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit")),
			key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "advanced")),
			key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "stacks")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
			key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		}
		if helpMap.hasProfile {
			bindings = append(bindings, key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "fresh")))
			bindings = append(bindings, key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "delete")))
		}
		if helpMap.hasSetupToken {
			bindings = append(bindings, key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "setup token")))
		}
		return bindings
	case profileSetupScreenIntake:
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "next")),
			key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "field")),
			key.NewBinding(key.WithKeys("ctrl+a"), key.WithHelp("ctrl+a", "advanced")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		}
	case profileSetupScreenAdvanced:
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "review")),
			key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "field")),
			key.NewBinding(key.WithKeys("ctrl+e"), key.WithHelp("ctrl+e", "intake")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		}
	case profileSetupScreenRepository:
		return []key.Binding{
			key.NewBinding(key.WithKeys("j", "k"), key.WithHelp("j/k", "select")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "choose")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		}
	case profileSetupScreenRepositoryDetails:
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "review")),
			key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "field")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		}
	case profileSetupScreenStacks:
		return []key.Binding{
			key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "sync repo")),
			key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "diff")),
			key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "stage all")),
			key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "commit")),
			key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "push")),
			key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add")),
			key.NewBinding(key.WithKeys("e", "enter"), key.WithHelp("e", "edit")),
			key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "remove")),
			key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "deploy one")),
			key.NewBinding(key.WithKeys("j", "k"), key.WithHelp("j/k", "select")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		}
	case profileSetupScreenStackCompose:
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "inspect")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		}
	case profileSetupScreenStackEditor:
		return []key.Binding{
			key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "save")),
			key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add route")),
			key.NewBinding(key.WithKeys("e", "enter"), key.WithHelp("e", "edit route")),
			key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "remove route")),
			key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "runtime env")),
			key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "name/routes")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		}
	case profileSetupScreenStackResourceEditor:
		return []key.Binding{
			key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "save route")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "next/save")),
			key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "field")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		}
	case profileSetupScreenStackEnvironment:
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "import/remove")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		}
	case profileSetupScreenStackDeleteConfirm:
		return []key.Binding{
			key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "remove")),
			key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "keep")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		}
	case profileSetupScreenStackDiff:
		return []key.Binding{
			key.NewBinding(key.WithKeys("j", "k"), key.WithHelp("j/k", "scroll")),
			key.NewBinding(key.WithKeys("pgup", "pgdown"), key.WithHelp("pgup/pgdn", "page")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		}
	case profileSetupScreenStackCommit:
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "commit")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		}
	case profileSetupScreenReview:
		return []key.Binding{
			key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "run")),
			key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "repository")),
			key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "advanced")),
			key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
			key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		}
	case profileSetupScreenDeleteConfirm:
		return []key.Binding{
			key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "delete")),
			key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "keep")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
			key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		}
	default:
		return nil
	}
}

func (helpMap profileSetupHelp) FullHelp() [][]key.Binding {
	return [][]key.Binding{helpMap.ShortHelp()}
}

func clampInt(value, minimum, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func collectFullRunConfig(output io.Writer, config setupConfig) (setupConfig, error) {
	model := newFullRunModel(config)
	program := tea.NewProgram(model, tea.WithOutput(output), tea.WithAltScreen())
	finalModel, err := program.Run()
	if err != nil {
		return setupConfig{}, fmt.Errorf("run setup intake TUI: %w", err)
	}
	result, ok := finalModel.(fullRunModel)
	if !ok {
		return setupConfig{}, errors.New("setup intake TUI returned an unexpected model")
	}
	if result.cancelled {
		return setupConfig{}, errors.New("setup cancelled")
	}
	if !result.done {
		return setupConfig{}, errors.New("setup intake did not complete")
	}
	return result.configFromInputs()
}

type fullRunModel struct {
	config    setupConfig
	inputs    []textinput.Model
	focus     int
	err       string
	done      bool
	cancelled bool
}

func newFullRunModel(config setupConfig) fullRunModel {
	inputs := newSetupInputs([]setupInputField{
		{label: "AegisNode private key", placeholder: defaultKeygenConfig().Path, value: firstNonEmpty(config.PrivateKeyPath, defaultKeygenConfig().Path)},
		{label: "Base domain", placeholder: "example.com", value: config.BaseDomain},
		{label: "Let's Encrypt email", placeholder: "admin@example.com", value: config.LetsEncryptEmail},
	})
	inputs[0].Focus()
	return fullRunModel{config: config, inputs: inputs}
}

func (model fullRunModel) Init() tea.Cmd {
	return textinput.Blink
}

func (model fullRunModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return model, nil
	}
	switch key.String() {
	case "ctrl+c", "esc":
		model.cancelled = true
		return model, tea.Quit
	case "tab", "down":
		model.inputs[model.focus].Blur()
		model.focus = (model.focus + 1) % len(model.inputs)
		model.inputs[model.focus].Focus()
		return model, nil
	case "shift+tab", "up":
		model.inputs[model.focus].Blur()
		model.focus--
		if model.focus < 0 {
			model.focus = len(model.inputs) - 1
		}
		model.inputs[model.focus].Focus()
		return model, nil
	case "enter":
		if model.focus < len(model.inputs)-1 {
			model.inputs[model.focus].Blur()
			model.focus++
			model.inputs[model.focus].Focus()
			return model, nil
		}
		if _, err := model.configFromInputs(); err != nil {
			model.err = err.Error()
			return model, nil
		}
		model.done = true
		return model, tea.Quit
	}
	var cmd tea.Cmd
	model.inputs[model.focus], cmd = model.inputs[model.focus].Update(key)
	return model, cmd
}

func (model fullRunModel) View() string {
	var builder strings.Builder
	builder.WriteString(setupTitleStyle.Render("AegisNode full setup"))
	builder.WriteString("\n\n")
	builder.WriteString(fmt.Sprintf("Profile target: %s\n", model.config.Host))
	builder.WriteString("Enter the values needed before the full setup run starts.\n\n")
	for _, input := range model.inputs {
		builder.WriteString(input.View())
		builder.WriteString("\n")
	}
	if model.err != "" {
		builder.WriteString("\n")
		builder.WriteString(setupErrorStyle.Render(model.err))
		builder.WriteString("\n")
	}
	builder.WriteString("\n")
	builder.WriteString(setupHelpStyle.Render("Enter advances. Tab changes field. Esc cancels."))
	return builder.String()
}

func (model fullRunModel) configFromInputs() (setupConfig, error) {
	config := model.config
	config.PrivateKeyPath = expandUserPath(strings.TrimSpace(model.inputs[0].Value()))
	config.AdminPublicKeyPath = publicKeyPath(config.PrivateKeyPath)
	config.BaseDomain = strings.TrimSpace(model.inputs[1].Value())
	config.LetsEncryptEmail = strings.TrimSpace(model.inputs[2].Value())
	if err := validateFullRunConfig(config); err != nil {
		return setupConfig{}, err
	}
	return config, nil
}

func prepareProfileSetup(options setupCLIOptions, store ProfileStore, output io.Writer) (Profile, ProfileState, setupConfig, error) {
	if options.IP == "" && options.ProfileID == "" {
		return Profile{}, ProfileState{}, setupConfig{}, errors.New("--ip is required for profile-aware setup")
	}

	profile, state, err := resolveSetupProfile(options, store)
	if err != nil {
		return Profile{}, ProfileState{}, setupConfig{}, err
	}
	applySetupOptionsToProfile(&profile, options)

	config := setupConfig{
		Mode:                 setupModeFullRun,
		Host:                 profile.IP,
		InitialSSHUser:       profile.InitialSSHUser,
		AdminUser:            profile.AdminUser,
		PrivateKeyPath:       expandUserPath(profile.PrivateKeyPath),
		AdminPublicKeyPath:   publicKeyPath(expandUserPath(profile.PrivateKeyPath)),
		BaseDomain:           profile.BaseDomain,
		LetsEncryptEmail:     profile.LetsEncryptEmail,
		PangolinAdminEmail:   firstNonEmpty(profile.PangolinAdminEmail, profile.LetsEncryptEmail),
		ProfileID:            profile.ID,
		ConfigRepositoryPath: profile.ConfigRepositoryPath,
		GitHubRepositoryURL:  options.GitHubRepositoryURL,
	}
	if config.BaseDomain == "" || config.LetsEncryptEmail == "" {
		if options.Yes || !isInteractiveWriter(output) {
			return Profile{}, ProfileState{}, setupConfig{}, validateFullRunConfig(config)
		}
		var err error
		config, err = collectFullRunConfig(output, config)
		if err != nil {
			return Profile{}, ProfileState{}, setupConfig{}, err
		}
		profile.BaseDomain = config.BaseDomain
		profile.LetsEncryptEmail = config.LetsEncryptEmail
		profile.PrivateKeyPath = config.PrivateKeyPath
	}

	if err := validateFullRunConfig(config); err != nil {
		return Profile{}, ProfileState{}, setupConfig{}, err
	}

	secrets, err := store.LoadSecrets(profile.ID)
	if err != nil {
		return Profile{}, ProfileState{}, setupConfig{}, err
	}
	if err := secrets.EnsureServerSecret(); err != nil {
		return Profile{}, ProfileState{}, setupConfig{}, fmt.Errorf("generate server secret: %w", err)
	}
	passwordOverride := firstNonEmpty(options.PangolinAdminPassword, os.Getenv("PANGOLIN_ADMIN_PASSWORD"))
	if passwordOverride != "" {
		secrets.PangolinAdminPassword = passwordOverride
	}
	if completedSetupStages(state)["proxy"] && secrets.PangolinAdminPassword == "" {
		return Profile{}, ProfileState{}, setupConfig{}, errors.New("existing Pangolin registration has no saved administrator password; enter it in Advanced setup or set PANGOLIN_ADMIN_PASSWORD once")
	}
	if passwordOverride == "" && !completedSetupStages(state)["proxy"] && !pangolinPasswordValid(secrets.PangolinAdminPassword) {
		secrets.PangolinAdminPassword = ""
	}
	if err := secrets.EnsureComposeWiringSecrets(); err != nil {
		return Profile{}, ProfileState{}, setupConfig{}, fmt.Errorf("generate Pangolin wiring secrets: %w", err)
	}
	if secrets.PangolinSetupToken != "" || !completedSetupStages(state)["proxy"] {
		if err := secrets.EnsurePangolinSetupToken(); err != nil {
			return Profile{}, ProfileState{}, setupConfig{}, fmt.Errorf("generate Pangolin setup token: %w", err)
		}
	}
	if err := store.SaveSecrets(profile.ID, secrets); err != nil {
		return Profile{}, ProfileState{}, setupConfig{}, err
	}
	config.ServerSecret = secrets.ServerSecret
	config.PangolinSetupToken = secrets.PangolinSetupToken
	config.PangolinAdminPassword = secrets.PangolinAdminPassword
	config.NewtID = secrets.NewtID
	config.NewtSecret = secrets.NewtSecret
	config.BeszelAdminPassword = secrets.BeszelAdminPassword
	config.BeszelSystemToken = secrets.BeszelSystemToken
	config.BeszelHubPrivateKey = secrets.BeszelHubPrivateKey
	config.BeszelHubPublicKey = secrets.BeszelHubPublicKey
	profile.PangolinAdminEmail = config.PangolinAdminEmail
	profile.PrivateKeyPath = config.PrivateKeyPath
	profile.BaseDomain = config.BaseDomain
	profile.LetsEncryptEmail = config.LetsEncryptEmail
	if err := store.Save(profile, state); err != nil {
		return Profile{}, ProfileState{}, setupConfig{}, err
	}
	return profile, state, config, nil
}

func prepareProfileStageSetup(options setupCLIOptions, store ProfileStore, stage string) (Profile, ProfileState, setupConfig, error) {
	if options.ProfileID == "" {
		return Profile{}, ProfileState{}, setupConfig{}, errors.New("a saved profile is required for one-time stage runs")
	}
	profile, state, err := store.Load(options.ProfileID)
	if err != nil {
		return Profile{}, ProfileState{}, setupConfig{}, err
	}
	applySetupOptionsToProfile(&profile, options)
	config := setupConfig{
		Mode:                 setupModeForStage(stage),
		Host:                 profile.IP,
		InitialSSHUser:       firstNonEmpty(profile.InitialSSHUser, "root"),
		AdminUser:            firstNonEmpty(profile.AdminUser, "aegisadmin"),
		PrivateKeyPath:       expandUserPath(firstNonEmpty(profile.PrivateKeyPath, defaultKeygenConfig().Path)),
		AdminPublicKeyPath:   publicKeyPath(expandUserPath(firstNonEmpty(profile.PrivateKeyPath, defaultKeygenConfig().Path))),
		BaseDomain:           profile.BaseDomain,
		LetsEncryptEmail:     profile.LetsEncryptEmail,
		PangolinAdminEmail:   firstNonEmpty(profile.PangolinAdminEmail, profile.LetsEncryptEmail),
		ProfileID:            profile.ID,
		ConfigRepositoryPath: profile.ConfigRepositoryPath,
		GitHubRepositoryURL:  options.GitHubRepositoryURL,
	}
	if err := validateStageRunConfig(stage, config); err != nil {
		return Profile{}, ProfileState{}, setupConfig{}, err
	}
	if stage == "proxy" || stage == "observability" || stage == "platform" || stage == "stacks" || strings.HasPrefix(stage, "stack:") {
		secrets, err := store.LoadSecrets(profile.ID)
		if err != nil {
			return Profile{}, ProfileState{}, setupConfig{}, err
		}
		if err := secrets.EnsureServerSecret(); err != nil {
			return Profile{}, ProfileState{}, setupConfig{}, fmt.Errorf("generate server secret: %w", err)
		}
		passwordOverride := firstNonEmpty(options.PangolinAdminPassword, os.Getenv("PANGOLIN_ADMIN_PASSWORD"))
		if passwordOverride != "" {
			secrets.PangolinAdminPassword = passwordOverride
		}
		if completedSetupStages(state)["proxy"] && secrets.PangolinAdminPassword == "" {
			return Profile{}, ProfileState{}, setupConfig{}, errors.New("existing Pangolin registration has no saved administrator password; enter it in Advanced setup or set PANGOLIN_ADMIN_PASSWORD once")
		}
		if passwordOverride == "" && !completedSetupStages(state)["proxy"] && !pangolinPasswordValid(secrets.PangolinAdminPassword) {
			secrets.PangolinAdminPassword = ""
		}
		if err := secrets.EnsurePangolinSetupToken(); err != nil {
			return Profile{}, ProfileState{}, setupConfig{}, fmt.Errorf("generate Pangolin setup token: %w", err)
		}
		if err := secrets.EnsureComposeWiringSecrets(); err != nil {
			return Profile{}, ProfileState{}, setupConfig{}, fmt.Errorf("generate Pangolin wiring secrets: %w", err)
		}
		if err := store.SaveSecrets(profile.ID, secrets); err != nil {
			return Profile{}, ProfileState{}, setupConfig{}, err
		}
		config.ServerSecret = secrets.ServerSecret
		config.PangolinSetupToken = secrets.PangolinSetupToken
		config.PangolinAdminPassword = secrets.PangolinAdminPassword
		config.NewtID = secrets.NewtID
		config.NewtSecret = secrets.NewtSecret
		config.BeszelAdminPassword = secrets.BeszelAdminPassword
		config.BeszelSystemToken = secrets.BeszelSystemToken
		config.BeszelHubPrivateKey = secrets.BeszelHubPrivateKey
		config.BeszelHubPublicKey = secrets.BeszelHubPublicKey
	}
	if err := store.Save(profile, state); err != nil {
		return Profile{}, ProfileState{}, setupConfig{}, err
	}
	return profile, state, config, nil
}

func setupModeForStage(stage string) setupMode {
	if strings.HasPrefix(stage, "stack:") {
		return setupModeObservability
	}
	switch stage {
	case "bootstrap":
		return setupModeBootstrapHarden
	case "harden":
		return setupModeHardenOnly
	case "network":
		return setupModeNetwork
	case "proxy":
		return setupModeProxy
	case "observability":
		return setupModeObservability
	case "stacks":
		return setupModeObservability
	case "platform":
		return setupModeFullRun
	default:
		return setupModeFullRun
	}
}

func validateStageRunConfig(stage string, config setupConfig) error {
	if config.Host == "" || config.PrivateKeyPath == "" {
		return errors.New("profile host and private key are required for one-time stage runs")
	}
	switch stage {
	case "bootstrap":
		if config.AdminPublicKeyPath == "" {
			return errors.New("admin public key is required for the bootstrap stage")
		}
		if !linuxUsername.MatchString(config.InitialSSHUser) || !linuxUsername.MatchString(config.AdminUser) {
			return errors.New("SSH users must be valid Linux usernames")
		}
	case "harden", "network":
		if !linuxUsername.MatchString(config.AdminUser) {
			return errors.New("admin SSH user must be a valid Linux username")
		}
	case "proxy", "platform":
		return validateProxyConfig(proxyConfig{
			Host:             config.Host,
			SSHUser:          config.AdminUser,
			PrivateKeyPath:   config.PrivateKeyPath,
			BaseDomain:       config.BaseDomain,
			LetsEncryptEmail: config.LetsEncryptEmail,
			ServerSecret:     firstNonEmpty(config.ServerSecret, "generated-placeholder"),
			SetupToken:       firstNonEmpty(config.PangolinSetupToken, "00000000000000000000000000000000"),
			AdminEmail:       firstNonEmpty(config.PangolinAdminEmail, config.LetsEncryptEmail),
		})
	case "observability", "stacks":
		if config.BaseDomain == "" || config.PangolinAdminEmail == "" {
			return errors.New("profile domain and Pangolin administrator email are required for repository synchronization")
		}
	default:
		if strings.HasPrefix(stage, "stack:") && stackSlugPattern.MatchString(strings.TrimPrefix(stage, "stack:")) {
			if config.BaseDomain == "" || config.PangolinAdminEmail == "" {
				return errors.New("profile domain and Pangolin administrator email are required for stack deployment")
			}
			return nil
		}
		return fmt.Errorf("unknown setup stage: %s", stage)
	}
	return nil
}

func resolveSetupProfile(options setupCLIOptions, store ProfileStore) (Profile, ProfileState, error) {
	if options.ProfileID != "" && !options.Fresh {
		return store.Load(options.ProfileID)
	}
	matches, err := store.ResolveByIP(options.IP)
	if err != nil {
		return Profile{}, ProfileState{}, err
	}
	if len(matches) == 0 || options.Fresh {
		if options.Fresh {
			sourceProfile, sourceState, found, err := freshProfileSource(options, matches, store)
			if err != nil {
				return Profile{}, ProfileState{}, err
			}
			if found {
				options = inheritFreshSetupOptions(options, sourceProfile, sourceState)
				return createSetupProfile(options, store, freshProfileSeedState(sourceState))
			}
		}
		return createSetupProfile(options, store, ProfileState{})
	}
	return store.Load(matches[0].ID)
}

func freshProfileSource(options setupCLIOptions, matches []ProfileSummary, store ProfileStore) (Profile, ProfileState, bool, error) {
	if options.ProfileID != "" {
		profile, state, err := store.Load(options.ProfileID)
		if err != nil {
			return Profile{}, ProfileState{}, false, err
		}
		return profile, state, true, nil
	}
	if len(matches) == 0 {
		return Profile{}, ProfileState{}, false, nil
	}
	profile, state, err := store.Load(matches[0].ID)
	if err != nil {
		return Profile{}, ProfileState{}, false, err
	}
	return profile, state, true, nil
}

func inheritFreshSetupOptions(options setupCLIOptions, source Profile, sourceState ProfileState) setupCLIOptions {
	options.IP = firstNonEmpty(options.IP, source.IP)
	options.AdminUser = firstNonEmpty(options.AdminUser, source.AdminUser, "aegisadmin")
	if completedSetupStages(sourceState)["bootstrap"] {
		options.InitialSSHUser = firstNonEmpty(options.InitialSSHUser, options.AdminUser, source.AdminUser, source.InitialSSHUser, "root")
	} else {
		options.InitialSSHUser = firstNonEmpty(options.InitialSSHUser, source.InitialSSHUser, "root")
	}
	options.PrivateKeyPath = firstNonEmpty(options.PrivateKeyPath, source.PrivateKeyPath)
	options.BaseDomain = firstNonEmpty(options.BaseDomain, source.BaseDomain)
	options.LetsEncryptEmail = firstNonEmpty(options.LetsEncryptEmail, source.LetsEncryptEmail)
	options.PangolinAdminEmail = firstNonEmpty(options.PangolinAdminEmail, source.PangolinAdminEmail, options.LetsEncryptEmail, source.LetsEncryptEmail)
	return options
}

func freshProfileSeedState(sourceState ProfileState) ProfileState {
	if !completedSetupStages(sourceState)["bootstrap"] {
		return ProfileState{}
	}
	runID := "seed-" + newSetupRunID()
	now := time.Now().UTC()
	return ProfileState{
		ActiveRunID: runID,
		Runs: map[string]SetupRun{
			runID: {
				ID:        runID,
				Status:    runStatusComplete,
				Stages:    map[string]SetupStageStatus{"bootstrap": {Status: stageStatusComplete, LastEnded: now}},
				CreatedAt: now,
				UpdatedAt: now,
			},
		},
	}
}

func createSetupProfile(options setupCLIOptions, store ProfileStore, seedState ProfileState) (Profile, ProfileState, error) {
	profile, err := store.Create(Profile{
		Name:                 firstNonEmpty(options.Name, options.IP),
		IP:                   options.IP,
		InitialSSHUser:       firstNonEmpty(options.InitialSSHUser, "root"),
		AdminUser:            firstNonEmpty(options.AdminUser, "aegisadmin"),
		PrivateKeyPath:       expandUserPath(firstNonEmpty(options.PrivateKeyPath, defaultKeygenConfig().Path)),
		BaseDomain:           options.BaseDomain,
		LetsEncryptEmail:     options.LetsEncryptEmail,
		PangolinAdminEmail:   firstNonEmpty(options.PangolinAdminEmail, options.LetsEncryptEmail),
		ConfigRepositoryPath: options.ConfigRepositoryPath,
	})
	if err != nil {
		return Profile{}, ProfileState{}, err
	}
	loadedProfile, state, err := store.Load(profile.ID)
	if err != nil {
		return Profile{}, ProfileState{}, err
	}
	if len(seedState.Runs) > 0 {
		state = seedState
		if state.Runs == nil {
			state.Runs = map[string]SetupRun{}
		}
		if err := store.Save(loadedProfile, state); err != nil {
			return Profile{}, ProfileState{}, err
		}
	}
	return loadedProfile, state, nil
}

func applySetupOptionsToProfile(profile *Profile, options setupCLIOptions) {
	if options.Name != "" {
		profile.Name = options.Name
	}
	profile.InitialSSHUser = firstNonEmpty(options.InitialSSHUser, profile.InitialSSHUser, "root")
	profile.AdminUser = firstNonEmpty(options.AdminUser, profile.AdminUser, "aegisadmin")
	profile.PrivateKeyPath = expandUserPath(firstNonEmpty(options.PrivateKeyPath, profile.PrivateKeyPath, defaultKeygenConfig().Path))
	profile.BaseDomain = firstNonEmpty(options.BaseDomain, profile.BaseDomain)
	profile.LetsEncryptEmail = firstNonEmpty(options.LetsEncryptEmail, profile.LetsEncryptEmail)
	profile.PangolinAdminEmail = firstNonEmpty(options.PangolinAdminEmail, profile.PangolinAdminEmail, profile.LetsEncryptEmail)
	profile.ConfigRepositoryPath = firstNonEmpty(options.ConfigRepositoryPath, profile.ConfigRepositoryPath)
}

func validateFullRunConfig(config setupConfig) error {
	if config.Host == "" || config.PrivateKeyPath == "" || config.BaseDomain == "" || config.LetsEncryptEmail == "" {
		return errors.New("--ip, --private-key, --domain, and --email are required for full setup")
	}
	if !linuxUsername.MatchString(config.InitialSSHUser) || !linuxUsername.MatchString(config.AdminUser) {
		return errors.New("SSH users must be valid Linux usernames")
	}
	return validateProxyConfig(proxyConfig{
		Host:             config.Host,
		SSHUser:          config.AdminUser,
		PrivateKeyPath:   config.PrivateKeyPath,
		BaseDomain:       config.BaseDomain,
		LetsEncryptEmail: config.LetsEncryptEmail,
		ServerSecret:     firstNonEmpty(config.ServerSecret, "generated-placeholder"),
		SetupToken:       firstNonEmpty(config.PangolinSetupToken, "00000000000000000000000000000000"),
		AdminEmail:       firstNonEmpty(config.PangolinAdminEmail, config.LetsEncryptEmail),
	})
}

func shouldUseProfileRunView(options setupCLIOptions, output io.Writer) bool {
	return !options.Yes && isInteractiveWriter(output)
}

func runProfileSetupPlan(ctx context.Context, store ProfileStore, profile Profile, state ProfileState, config setupConfig, stdout, stderr io.Writer) error {
	fmt.Fprintln(stdout, "Selected plan:")
	fmt.Fprint(stdout, setupPlanSummary(config))
	fmt.Fprintln(stdout)
	if err := runPreflight(config, stdout); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Preparing the configuration repository before SSH execution...")
	var err error
	profile, config, err = prepareDeclarativeSetup(ctx, store, profile, state, config)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Configuration repository ready: %s at %s\n\n", config.ConfigRepositoryPath, config.ConfigRepositoryCommit)

	completedStages := completedSetupStages(state)
	runID := newSetupRunID()
	state.ActiveRunID = runID
	state.Runs[runID] = newSetupRun(runID, completedStages)
	if err := store.Save(profile, state); err != nil {
		return err
	}

	reporter := &profileRunReporter{
		store:   store,
		profile: profile,
		state:   &state,
		runID:   runID,
	}

	if err := runFullSetupStages(ctx, profile, config, runID, completedStages, reporter, stdout, stderr); err != nil {
		reporter.finishRun(runStatusFailed)
		if reporter.err != nil {
			return reporter.err
		}
		return err
	}
	reporter.finishRun(runStatusComplete)
	if reporter.err != nil {
		return reporter.err
	}
	printSSHLoginGuidance(stdout, config)
	fmt.Fprintf(stdout, "\nProxy URL: https://pangolin.%s\n", config.BaseDomain)
	fmt.Fprintf(stdout, "Beszel URL: https://beszel.%s\nDozzle URL: https://dozzle.%s\n", config.BaseDomain, config.BaseDomain)
	fmt.Fprintln(stdout, requiredDNSGuidance(config.BaseDomain, config.Host))
	fmt.Fprintf(stdout, "Retrieve Pangolin login with: aegisnode pangolin-credentials --profile %s\n", config.ProfileID)
	return nil
}

func runProfileSetupPlanWithRunView(ctx context.Context, store ProfileStore, profile Profile, state ProfileState, config setupConfig, stdout, stderr io.Writer) error {
	fmt.Fprintln(stdout, "Selected plan:")
	fmt.Fprint(stdout, setupPlanSummary(config))
	fmt.Fprintln(stdout)
	if err := runPreflight(config, stdout); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Preparing the configuration repository before SSH execution...")
	var err error
	profile, config, err = prepareDeclarativeSetup(ctx, store, profile, state, config)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Configuration repository ready: %s at %s\n\n", config.ConfigRepositoryPath, config.ConfigRepositoryCommit)

	completedStages := completedSetupStages(state)
	runID := newSetupRunID()
	state.ActiveRunID = runID
	state.Runs[runID] = newSetupRun(runID, completedStages)
	if err := store.Save(profile, state); err != nil {
		return err
	}

	profileReporter := &profileRunReporter{
		store:   store,
		profile: profile,
		state:   &state,
		runID:   runID,
	}
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()

	messages := make(chan tea.Msg, 256)
	liveReporter := profileRunUIReporter{messages: messages}
	reporter := &synchronizedTaskReporter{reporters: []TaskReporter{profileReporter, liveReporter}}
	model := newProfileRunModel(profile, config, runID, completedStages, "", messages, cancel)
	model.start = startProfileRunCommand(runContext, profile, config, runID, completedStages, reporter, profileReporter, messages)
	program := tea.NewProgram(model, tea.WithOutput(stderr), tea.WithAltScreen())
	finalModel, err := program.Run()
	if err != nil {
		return fmt.Errorf("run setup TUI: %w", err)
	}
	result, ok := finalModel.(profileRunModel)
	if !ok {
		return errors.New("setup run TUI returned an unexpected model")
	}
	if result.cancelled {
		return errors.New("setup cancelled")
	}
	if result.err != nil {
		return result.err
	}
	if profileReporter.err != nil {
		return profileReporter.err
	}
	printSSHLoginGuidance(stdout, config)
	fmt.Fprintf(stdout, "\nProxy URL: https://pangolin.%s\n", config.BaseDomain)
	fmt.Fprintf(stdout, "Beszel URL: https://beszel.%s\nDozzle URL: https://dozzle.%s\n", config.BaseDomain, config.BaseDomain)
	fmt.Fprintln(stdout, requiredDNSGuidance(config.BaseDomain, config.Host))
	fmt.Fprintf(stdout, "Retrieve Pangolin login with: aegisnode pangolin-credentials --profile %s\n", config.ProfileID)
	return nil
}

func runProfileSetupStagePlan(ctx context.Context, store ProfileStore, profile Profile, state ProfileState, config setupConfig, stage string, stdout, stderr io.Writer) error {
	fmt.Fprintf(stdout, "Selected one-time stage: %s\n\n", profileRunStageLabel(stage))
	if err := runPreflight(config, stdout); err != nil {
		return err
	}
	if stage == "observability" || stage == "platform" || stage == "stacks" || strings.HasPrefix(stage, "stack:") {
		fmt.Fprintln(stdout, "Preparing the configuration repository before SSH execution...")
		var err error
		profile, config, err = prepareDeclarativeSetup(ctx, store, profile, state, config)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Configuration repository ready: %s at %s\n\n", config.ConfigRepositoryPath, config.ConfigRepositoryCommit)
	}

	runID := newSetupRunID()
	state.ActiveRunID = runID
	state.Runs[runID] = newSetupRunForStage(runID, stage, completedSetupStages(state))
	if err := store.Save(profile, state); err != nil {
		return err
	}

	reporter := &profileRunReporter{
		store:   store,
		profile: profile,
		state:   &state,
		runID:   runID,
	}
	if err := runSetupStage(ctx, profile, config, runID, stage, reporter, stdout, stderr); err != nil {
		reporter.finishRun(runStatusFailed)
		if reporter.err != nil {
			return reporter.err
		}
		return err
	}
	if stage == "stacks" {
		state.StackRepositoryCommit = config.ConfigRepositoryCommit
	}
	reporter.finishRun(runStatusComplete)
	if reporter.err != nil {
		return reporter.err
	}
	printStageCompletionGuidance(stdout, config, stage)
	return nil
}

func runProfileSetupStagePlanWithRunView(ctx context.Context, store ProfileStore, profile Profile, state ProfileState, config setupConfig, stage string, stdout, stderr io.Writer) error {
	fmt.Fprintf(stdout, "Selected one-time stage: %s\n\n", profileRunStageLabel(stage))
	if err := runPreflight(config, stdout); err != nil {
		return err
	}
	if stage == "observability" || stage == "platform" || stage == "stacks" || strings.HasPrefix(stage, "stack:") {
		fmt.Fprintln(stdout, "Preparing the configuration repository before SSH execution...")
		var err error
		profile, config, err = prepareDeclarativeSetup(ctx, store, profile, state, config)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Configuration repository ready: %s at %s\n\n", config.ConfigRepositoryPath, config.ConfigRepositoryCommit)
	}

	runID := newSetupRunID()
	state.ActiveRunID = runID
	state.Runs[runID] = newSetupRunForStage(runID, stage, completedSetupStages(state))
	if err := store.Save(profile, state); err != nil {
		return err
	}

	profileReporter := &profileRunReporter{
		store:   store,
		profile: profile,
		state:   &state,
		runID:   runID,
	}
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()

	messages := make(chan tea.Msg, 256)
	liveReporter := profileRunUIReporter{messages: messages}
	reporter := &synchronizedTaskReporter{reporters: []TaskReporter{profileReporter, liveReporter}}
	model := newProfileRunModel(profile, config, runID, completedSetupStages(state), stage, messages, cancel)
	model.start = startProfileStageRunCommand(runContext, profile, config, runID, stage, reporter, profileReporter, messages)
	program := tea.NewProgram(model, tea.WithOutput(stderr), tea.WithAltScreen())
	finalModel, err := program.Run()
	if err != nil {
		return fmt.Errorf("run setup TUI: %w", err)
	}
	result, ok := finalModel.(profileRunModel)
	if !ok {
		return errors.New("setup run TUI returned an unexpected model")
	}
	if result.cancelled {
		return errors.New("setup cancelled")
	}
	if result.err != nil {
		return result.err
	}
	if profileReporter.err != nil {
		return profileReporter.err
	}
	printStageCompletionGuidance(stdout, config, stage)
	return nil
}

func runFullSetupStages(ctx context.Context, profile Profile, config setupConfig, runID string, completedStages map[string]bool, reporter TaskReporter, stdout, stderr io.Writer) error {
	adminPublicKey, err := os.ReadFile(config.AdminPublicKeyPath)
	if err != nil {
		return fmt.Errorf("read admin public key: %w", err)
	}
	key := strings.TrimSpace(string(adminPublicKey))

	stageStdout := setupStageWriter(stdout, "bootstrap", "stdout")
	stageStderr := setupStageWriter(stderr, "bootstrap", "stderr")
	if completedStages["bootstrap"] {
		fmt.Fprintln(stageStdout, "Step 1/5: bootstrap administrative access already complete; skipping.")
	} else {
		fmt.Fprintln(stageStdout, "Step 1/5: bootstrap administrative access.")
		bootstrapConfig := bootstrapConfig{
			Host:               config.Host,
			SSHUser:            config.InitialSSHUser,
			AdminUser:          config.AdminUser,
			AdminPublicKeyPath: config.AdminPublicKeyPath,
			PrivateKeyPath:     config.PrivateKeyPath,
		}
		bootstrapClient, err := newBootstrapRemoteClient(ctx, bootstrapConfig, stageStdout, stageStderr)
		if err != nil {
			return err
		}
		if err := runBootstrapStepsWithReporter(ctx, bootstrapClient, bootstrapConfig, key, runID, reporter, stageStdout); err != nil {
			_ = bootstrapClient.Close()
			return fmt.Errorf("bootstrap failed: %w", err)
		}
		if err := bootstrapClient.Close(); err != nil {
			return err
		}
	}

	stageStdout = setupStageWriter(stdout, "harden", "stdout")
	stageStderr = setupStageWriter(stderr, "harden", "stderr")
	if completedStages["harden"] {
		fmt.Fprintln(stageStdout, "Step 2/5: harden server already complete; skipping.")
	} else {
		fmt.Fprintln(stageStdout, "Step 2/5: harden server.")
		hardeningConfig := hardeningConfig{Host: profile.IP, SSHUser: config.AdminUser, PrivateKeyPath: config.PrivateKeyPath}
		hardeningClient, err := newHardeningRemoteClient(ctx, hardeningConfig, stageStdout, stageStderr)
		if err != nil {
			return err
		}
		if err := runHardeningStepsWithReporter(ctx, hardeningClient, hardeningConfig, runID, reporter, stageStdout); err != nil {
			_ = hardeningClient.Close()
			return fmt.Errorf("hardening failed: %w", err)
		}
		if err := hardeningClient.Close(); err != nil {
			return err
		}
	}

	stageStdout = setupStageWriter(stdout, "network", "stdout")
	stageStderr = setupStageWriter(stderr, "network", "stderr")
	if completedStages["network"] {
		fmt.Fprintln(stageStdout, "Step 3/5: configure Docker networking and UFW already complete; skipping.")
	} else {
		fmt.Fprintln(stageStdout, "Step 3/5: configure Docker networking and UFW.")
		sshPort, err := sshPortForHost(config.Host)
		if err != nil {
			return err
		}
		networkConfig := networkConfig{Host: profile.IP, SSHUser: config.AdminUser, SSHPort: sshPort, PrivateKeyPath: config.PrivateKeyPath}
		networkClient, err := newNetworkRemoteClient(ctx, networkConfig, stageStdout, stageStderr)
		if err != nil {
			return err
		}
		if err := runNetworkStepsWithReporter(ctx, networkClient, networkConfig, runID, reporter, stageStdout); err != nil {
			_ = networkClient.Close()
			return fmt.Errorf("network configuration failed: %w", err)
		}
		if err := networkClient.Close(); err != nil {
			return err
		}
	}

	stageStdout = setupStageWriter(stdout, "proxy", "stdout")
	stageStderr = setupStageWriter(stderr, "proxy", "stderr")
	if completedStages["proxy"] {
		fmt.Fprintln(stageStdout, "Step 4/5: deploy Pangolin and reverse proxy stack already complete; skipping.")
	} else {
		fmt.Fprintln(stageStdout, "Step 4/5: deploy Pangolin and reverse proxy stack.")
		proxyConfig := proxyConfig{
			Host: profile.IP, SSHUser: config.AdminUser, PrivateKeyPath: config.PrivateKeyPath,
			BaseDomain: config.BaseDomain, LetsEncryptEmail: config.LetsEncryptEmail,
			ServerSecret: config.ServerSecret, SetupToken: config.PangolinSetupToken,
			AdminEmail: config.PangolinAdminEmail, AdminPassword: config.PangolinAdminPassword,
			NewtID: config.NewtID, NewtSecret: config.NewtSecret,
		}
		proxyClient, err := newProxyRemoteClient(ctx, proxyConfig, stageStdout, stageStderr)
		if err != nil {
			return err
		}
		if err := runProxyStepsWithReporter(ctx, proxyClient, proxyConfig, runID, reporter, stageStdout); err != nil {
			_ = proxyClient.Close()
			return fmt.Errorf("proxy deployment failed: %w", err)
		}
		if err := proxyClient.Close(); err != nil {
			return err
		}
	}

	stageStdout = setupStageWriter(stdout, "observability", "stdout")
	stageStderr = setupStageWriter(stderr, "observability", "stderr")
	if completedStages["observability"] {
		fmt.Fprintln(stageStdout, "Step 5/5: deploy observability stack already complete; skipping.")
	} else {
		fmt.Fprintln(stageStdout, "Step 5/5: deploy observability stack.")
		observabilityConfig := observabilityConfig{
			Host: profile.IP, SSHUser: config.AdminUser, PrivateKeyPath: config.PrivateKeyPath,
			BaseDomain: config.BaseDomain, AdminEmail: config.PangolinAdminEmail,
			AdminPassword: config.BeszelAdminPassword, PangolinPassword: config.PangolinAdminPassword, SystemToken: config.BeszelSystemToken,
			HubPrivateKey: config.BeszelHubPrivateKey, HubPublicKey: config.BeszelHubPublicKey,
			RepositoryCommit: config.ConfigRepositoryCommit, RepositoryOrigin: config.ConfigRepositoryOrigin,
			RepositoryCompose: config.ConfigRepositoryCompose, RepositorySHA256: config.ConfigRepositorySHA256, GitHubToken: config.GitHubToken,
		}
		observabilityClient, err := newObservabilityRemoteClient(ctx, observabilityConfig, stageStdout, stageStderr)
		if err != nil {
			return err
		}
		if err := runObservabilityStepsWithReporter(ctx, observabilityClient, observabilityConfig, runID, reporter, stageStdout); err != nil {
			_ = observabilityClient.Close()
			return fmt.Errorf("observability deployment failed: %w", err)
		}
		if err := observabilityClient.Close(); err != nil {
			return err
		}
	}
	for _, stack := range config.Stacks {
		if completedStages["stack:"+stack.Name] {
			fmt.Fprintf(setupStageWriter(stdout, "stack:"+stack.Name, "stdout"), "Standalone %s stack already complete; skipping.\n", stack.Name)
			continue
		}
		if err := runConfiguredStackStage(ctx, profile, config, runID, stack, reporter, stdout, stderr); err != nil {
			return err
		}
	}
	return nil
}

func runSetupStage(ctx context.Context, profile Profile, config setupConfig, runID string, stage string, reporter TaskReporter, stdout, stderr io.Writer) error {
	stageStdout := setupStageWriter(stdout, stage, "stdout")
	stageStderr := setupStageWriter(stderr, stage, "stderr")
	switch stage {
	case "stacks":
		syncConfig := observabilityConfig{
			Host: profile.IP, SSHUser: config.AdminUser, PrivateKeyPath: config.PrivateKeyPath,
			BaseDomain: config.BaseDomain, AdminEmail: config.PangolinAdminEmail,
			PangolinPassword: config.PangolinAdminPassword,
			RepositoryCommit: config.ConfigRepositoryCommit, RepositoryOrigin: config.ConfigRepositoryOrigin,
			RepositoryCompose: config.ConfigRepositoryCompose, RepositorySHA256: config.ConfigRepositorySHA256,
			GitHubToken: config.GitHubToken, Stacks: config.Stacks,
		}
		fmt.Fprintln(stageStdout, "One-time action: synchronize committed stack configuration.")
		client, err := newObservabilityRemoteClient(ctx, syncConfig, stageStdout, stageStderr)
		if err != nil {
			return err
		}
		tasks := stackRepositoryReconcileTasks(syncConfig, firstNonEmpty(config.AdminUser, "root"))
		if err := runTasksWithReporter(ctx, client, config.AdminUser, runID, stage, tasks, stageStdout, reporter); err != nil {
			_ = client.Close()
			return fmt.Errorf("stack repository synchronization failed: %w", err)
		}
		return client.Close()
	case "platform":
		fmt.Fprintln(stageStdout, "One-time action: configure Network, Proxy, and Observability.")
		if err := runSetupStage(ctx, profile, config, runID, "network", reporter, stdout, stderr); err != nil {
			return err
		}
		if err := runSetupStage(ctx, profile, config, runID, "proxy", reporter, stdout, stderr); err != nil {
			return err
		}
		platformConfig := config
		platformConfig.Stacks = nil
		return runSetupStage(ctx, profile, platformConfig, runID, "observability", reporter, stdout, stderr)
	case "bootstrap":
		adminPublicKey, err := os.ReadFile(config.AdminPublicKeyPath)
		if err != nil {
			return fmt.Errorf("read admin public key: %w", err)
		}
		bootstrapConfig := bootstrapConfig{
			Host:               config.Host,
			SSHUser:            config.InitialSSHUser,
			AdminUser:          config.AdminUser,
			AdminPublicKeyPath: config.AdminPublicKeyPath,
			PrivateKeyPath:     config.PrivateKeyPath,
		}
		fmt.Fprintln(stageStdout, "One-time stage: bootstrap administrative access.")
		bootstrapClient, err := newBootstrapRemoteClient(ctx, bootstrapConfig, stageStdout, stageStderr)
		if err != nil {
			return err
		}
		if err := runBootstrapStepsWithReporter(ctx, bootstrapClient, bootstrapConfig, strings.TrimSpace(string(adminPublicKey)), runID, reporter, stageStdout); err != nil {
			_ = bootstrapClient.Close()
			return fmt.Errorf("bootstrap failed: %w", err)
		}
		return bootstrapClient.Close()
	case "harden":
		hardeningConfig := hardeningConfig{Host: profile.IP, SSHUser: config.AdminUser, PrivateKeyPath: config.PrivateKeyPath}
		fmt.Fprintln(stageStdout, "One-time stage: harden server.")
		hardeningClient, err := newHardeningRemoteClient(ctx, hardeningConfig, stageStdout, stageStderr)
		if err != nil {
			return err
		}
		if err := runHardeningStepsWithReporter(ctx, hardeningClient, hardeningConfig, runID, reporter, stageStdout); err != nil {
			_ = hardeningClient.Close()
			return fmt.Errorf("hardening failed: %w", err)
		}
		return hardeningClient.Close()
	case "network":
		sshPort, err := sshPortForHost(config.Host)
		if err != nil {
			return err
		}
		networkConfig := networkConfig{Host: profile.IP, SSHUser: config.AdminUser, SSHPort: sshPort, PrivateKeyPath: config.PrivateKeyPath}
		fmt.Fprintln(stageStdout, "One-time stage: configure Docker networking and UFW.")
		networkClient, err := newNetworkRemoteClient(ctx, networkConfig, stageStdout, stageStderr)
		if err != nil {
			return err
		}
		if err := runNetworkStepsWithReporter(ctx, networkClient, networkConfig, runID, reporter, stageStdout); err != nil {
			_ = networkClient.Close()
			return fmt.Errorf("network configuration failed: %w", err)
		}
		return networkClient.Close()
	case "proxy":
		proxyConfig := proxyConfig{
			Host:             profile.IP,
			SSHUser:          config.AdminUser,
			PrivateKeyPath:   config.PrivateKeyPath,
			BaseDomain:       config.BaseDomain,
			LetsEncryptEmail: config.LetsEncryptEmail,
			ServerSecret:     config.ServerSecret,
			SetupToken:       config.PangolinSetupToken,
			AdminEmail:       config.PangolinAdminEmail,
			AdminPassword:    config.PangolinAdminPassword,
			NewtID:           config.NewtID,
			NewtSecret:       config.NewtSecret,
		}
		fmt.Fprintln(stageStdout, "One-time stage: deploy Pangolin and reverse proxy stack.")
		proxyClient, err := newProxyRemoteClient(ctx, proxyConfig, stageStdout, stageStderr)
		if err != nil {
			return err
		}
		if err := runProxyStepsWithReporter(ctx, proxyClient, proxyConfig, runID, reporter, stageStdout); err != nil {
			_ = proxyClient.Close()
			return fmt.Errorf("proxy deployment failed: %w", err)
		}
		return proxyClient.Close()
	case "observability":
		observabilityConfig := observabilityConfig{
			Host: profile.IP, SSHUser: config.AdminUser, PrivateKeyPath: config.PrivateKeyPath,
			BaseDomain: config.BaseDomain, AdminEmail: config.PangolinAdminEmail,
			AdminPassword: config.BeszelAdminPassword, PangolinPassword: config.PangolinAdminPassword, SystemToken: config.BeszelSystemToken,
			HubPrivateKey: config.BeszelHubPrivateKey, HubPublicKey: config.BeszelHubPublicKey,
			RepositoryCommit: config.ConfigRepositoryCommit, RepositoryOrigin: config.ConfigRepositoryOrigin,
			RepositoryCompose: config.ConfigRepositoryCompose, RepositorySHA256: config.ConfigRepositorySHA256, GitHubToken: config.GitHubToken,
			Stacks: config.Stacks,
		}
		fmt.Fprintln(stageStdout, "One-time stage: deploy observability stack.")
		observabilityClient, err := newObservabilityRemoteClient(ctx, observabilityConfig, stageStdout, stageStderr)
		if err != nil {
			return err
		}
		if err := runObservabilityStepsWithReporter(ctx, observabilityClient, observabilityConfig, runID, reporter, stageStdout); err != nil {
			_ = observabilityClient.Close()
			return fmt.Errorf("observability deployment failed: %w", err)
		}
		return observabilityClient.Close()
	default:
		if strings.HasPrefix(stage, "stack:") {
			name := strings.TrimPrefix(stage, "stack:")
			for _, stack := range config.Stacks {
				if stack.Name == name {
					return runConfiguredStackStage(ctx, profile, config, runID, stack, reporter, stdout, stderr)
				}
			}
			return fmt.Errorf("stack %q is not present in the committed configuration", name)
		}
		return fmt.Errorf("unknown setup stage: %s", stage)
	}
}

func runConfiguredStackStage(ctx context.Context, profile Profile, config setupConfig, runID string, stack configuredStack, reporter TaskReporter, stdout, stderr io.Writer) error {
	stage := "stack:" + stack.Name
	stageStdout := setupStageWriter(stdout, stage, "stdout")
	stageStderr := setupStageWriter(stderr, stage, "stderr")
	observabilityConfig := observabilityConfig{
		Host: profile.IP, SSHUser: config.AdminUser, PrivateKeyPath: config.PrivateKeyPath,
		BaseDomain: config.BaseDomain, AdminEmail: config.PangolinAdminEmail,
		PangolinPassword: config.PangolinAdminPassword, RepositoryCommit: config.ConfigRepositoryCommit,
	}
	fmt.Fprintf(stageStdout, "One-time action: deploy standalone %s stack.\n", stack.Name)
	client, err := newObservabilityRemoteClient(ctx, observabilityConfig, stageStdout, stageStderr)
	if err != nil {
		return err
	}
	tasks := configuredStackTasks(observabilityConfig, stack, firstNonEmpty(config.AdminUser, "root"))
	if err := runTasksWithReporter(ctx, client, config.AdminUser, runID, stage, tasks, stageStdout, reporter); err != nil {
		_ = client.Close()
		return fmt.Errorf("%s stack deployment failed: %w", stack.Name, err)
	}
	return client.Close()
}

func printStageCompletionGuidance(stdout io.Writer, config setupConfig, stage string) {
	switch stage {
	case "bootstrap", "harden", "network":
		printSSHLoginGuidance(stdout, config)
	case "proxy":
		fmt.Fprintf(stdout, "\nProxy URL: https://pangolin.%s\n", config.BaseDomain)
		fmt.Fprintln(stdout, requiredDNSGuidance(config.BaseDomain, config.Host))
		fmt.Fprintf(stdout, "Retrieve Pangolin login with: aegisnode pangolin-credentials --profile %s\n", config.ProfileID)
	case "observability":
		fmt.Fprintf(stdout, "\nBeszel URL: https://beszel.%s\nDozzle URL: https://dozzle.%s\n", config.BaseDomain, config.BaseDomain)
	case "platform":
		fmt.Fprintf(stdout, "\nProxy URL: https://pangolin.%s\n", config.BaseDomain)
		fmt.Fprintf(stdout, "Beszel URL: https://beszel.%s\nDozzle URL: https://dozzle.%s\n", config.BaseDomain, config.BaseDomain)
		fmt.Fprintln(stdout, requiredDNSGuidance(config.BaseDomain, config.Host))
	case "stacks":
		fmt.Fprintf(stdout, "\nStack repository synchronized at %s.\n", config.ConfigRepositoryCommit)
	default:
		if strings.HasPrefix(stage, "stack:") {
			fmt.Fprintf(stdout, "\nStandalone stack %s deployed from committed configuration.\n", strings.TrimPrefix(stage, "stack:"))
		}
	}
}

type setupStageWriterProvider interface {
	WriterForStage(stage string, stream string) io.Writer
}

func setupStageWriter(writer io.Writer, stage string, stream string) io.Writer {
	if provider, ok := writer.(setupStageWriterProvider); ok {
		return provider.WriterForStage(stage, stream)
	}
	return writer
}

type synchronizedTaskReporter struct {
	mu        sync.Mutex
	reporters []TaskReporter
}

func (reporter *synchronizedTaskReporter) Report(event TaskEvent) {
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	for _, target := range reporter.reporters {
		if target != nil {
			target.Report(event)
		}
	}
}

type profileRunUIReporter struct {
	messages chan<- tea.Msg
}

func (reporter profileRunUIReporter) Report(event TaskEvent) {
	message := profileRunEventMsg{event: event}
	if event.Type == TaskLogLine {
		select {
		case reporter.messages <- message:
		default:
		}
		return
	}
	reporter.messages <- message
}

type profileRunOutput struct {
	reporter TaskReporter
	runID    string
}

func (output profileRunOutput) Write(data []byte) (int, error) {
	writer := output.WriterForStage("", "stdout")
	return writer.Write(data)
}

func (output profileRunOutput) WriterForStage(stage string, stream string) io.Writer {
	return &profileRunLogWriter{
		reporter: output.reporter,
		runID:    output.runID,
		stage:    stage,
		stream:   stream,
	}
}

type profileRunLogWriter struct {
	reporter TaskReporter
	runID    string
	stage    string
	stream   string
	partial  string
}

func (writer *profileRunLogWriter) Write(data []byte) (int, error) {
	text := writer.partial + string(data)
	lines := strings.Split(text, "\n")
	writer.partial = lines[len(lines)-1]
	for _, line := range lines[:len(lines)-1] {
		writer.reportLine(line)
	}
	return len(data), nil
}

func (writer *profileRunLogWriter) reportLine(line string) {
	if writer.reporter == nil || line == "" {
		return
	}
	writer.reporter.Report(TaskEvent{
		Type:   TaskLogLine,
		RunID:  writer.runID,
		Stage:  writer.stage,
		Stream: writer.stream,
		Line:   line,
		Time:   time.Now(),
	})
}

type profileRunEventMsg struct {
	event TaskEvent
}

type profileRunFinishedMsg struct {
	err error
}

type profileRunStageView struct {
	Key       string
	Label     string
	Status    string
	Current   string
	Completed int
	Total     int
}

type profileRunModel struct {
	profile        Profile
	config         setupConfig
	runID          string
	messages       <-chan tea.Msg
	start          tea.Cmd
	cancel         context.CancelFunc
	spinner        spinner.Model
	progress       progress.Model
	logViewport    viewport.Model
	stages         []profileRunStageView
	totalTasks     int
	completedTasks int
	currentStage   string
	currentTask    string
	logLines       []string
	stageFilter    string
	runLabel       string
	width          int
	height         int
	done           bool
	cancelled      bool
	err            error
}

func newProfileRunModel(profile Profile, config setupConfig, runID string, completedStages map[string]bool, stageFilter string, messages <-chan tea.Msg, cancel context.CancelFunc) profileRunModel {
	stageTotals := setupRunStageTaskTotals(config)
	stages := []profileRunStageView{}
	runStages := append([]string(nil), setupStageOrder...)
	if stageFilter == "platform" {
		runStages = []string{"network", "proxy", "observability"}
	} else if stageFilter == "stacks" {
		runStages = []string{"stacks"}
		syncConfig := observabilityConfig{
			BaseDomain: config.BaseDomain, AdminEmail: config.PangolinAdminEmail,
			PangolinPassword: config.PangolinAdminPassword,
			RepositoryCommit: config.ConfigRepositoryCommit, RepositoryOrigin: config.ConfigRepositoryOrigin,
			RepositoryCompose: config.ConfigRepositoryCompose, RepositorySHA256: config.ConfigRepositorySHA256,
			GitHubToken: config.GitHubToken, Stacks: config.Stacks,
		}
		stageTotals["stacks"] = len(stackRepositoryReconcileTasks(syncConfig, firstNonEmpty(config.AdminUser, "root")))
	} else if strings.HasPrefix(stageFilter, "stack:") {
		runStages = []string{stageFilter}
		name := strings.TrimPrefix(stageFilter, "stack:")
		for _, stack := range config.Stacks {
			if stack.Name == name {
				stageTotals[stageFilter] = len(configuredStackTasks(observabilityConfig{
					RepositoryCommit: config.ConfigRepositoryCommit,
					BaseDomain:       config.BaseDomain,
					AdminEmail:       config.PangolinAdminEmail,
					PangolinPassword: config.PangolinAdminPassword,
				}, stack, firstNonEmpty(config.AdminUser, "root")))
				break
			}
		}
	} else if stageFilter == "" {
		for _, stack := range config.Stacks {
			stage := "stack:" + stack.Name
			runStages = append(runStages, stage)
			stageTotals[stage] = len(configuredStackTasks(observabilityConfig{
				RepositoryCommit: config.ConfigRepositoryCommit,
				BaseDomain:       config.BaseDomain,
				AdminEmail:       config.PangolinAdminEmail,
				PangolinPassword: config.PangolinAdminPassword,
			}, stack, firstNonEmpty(config.AdminUser, "root")))
		}
	}
	for _, stage := range runStages {
		if stageFilter != "" && stageFilter != "platform" && stage != stageFilter {
			continue
		}
		stages = append(stages, profileRunStageView{Key: stage, Label: profileRunStageLabel(stage), Status: stageStatusPending, Total: stageTotals[stage]})
	}
	totalTasks := 0
	completedTasks := 0
	for index := range stages {
		totalTasks += stages[index].Total
		if stageFilter == "" && completedStages[stages[index].Key] {
			stages[index].Status = stageStatusComplete
			stages[index].Completed = stages[index].Total
			completedTasks += stages[index].Total
		}
	}
	runSpinner := spinner.New()
	runSpinner.Spinner = spinner.Dot
	runLabel := "full setup"
	if stageFilter != "" {
		runLabel = profileRunStageLabel(stageFilter)
	}
	return profileRunModel{
		profile:        profile,
		config:         config,
		runID:          runID,
		messages:       messages,
		cancel:         cancel,
		spinner:        runSpinner,
		progress:       progress.New(progress.WithWidth(48)),
		logViewport:    viewport.New(88, 10),
		stages:         stages,
		totalTasks:     totalTasks,
		completedTasks: completedTasks,
		stageFilter:    stageFilter,
		runLabel:       runLabel,
		width:          92,
		height:         28,
	}
}

func setupRunStageTaskTotals(config setupConfig) map[string]int {
	sshPort, err := sshPortForHost(config.Host)
	if err != nil {
		sshPort = defaultSSHPort
	}
	return map[string]int{
		"bootstrap": len(bootstrapTasks(bootstrapConfig{
			Host:               config.Host,
			SSHUser:            config.InitialSSHUser,
			AdminUser:          config.AdminUser,
			AdminPublicKeyPath: config.AdminPublicKeyPath,
			PrivateKeyPath:     config.PrivateKeyPath,
		}, "")),
		"harden": len(hardeningTasks()),
		"network": len(networkTasks(networkConfig{
			Host:           config.Host,
			SSHUser:        config.AdminUser,
			SSHPort:        sshPort,
			PrivateKeyPath: config.PrivateKeyPath,
		})),
		"proxy": len(proxyTasks(proxyConfig{
			Host:             config.Host,
			SSHUser:          config.AdminUser,
			PrivateKeyPath:   config.PrivateKeyPath,
			BaseDomain:       config.BaseDomain,
			LetsEncryptEmail: config.LetsEncryptEmail,
			ServerSecret:     firstNonEmpty(config.ServerSecret, "generated-placeholder"),
			SetupToken:       firstNonEmpty(config.PangolinSetupToken, "00000000000000000000000000000000"),
			AdminEmail:       firstNonEmpty(config.PangolinAdminEmail, config.LetsEncryptEmail),
			AdminPassword:    config.PangolinAdminPassword,
			NewtID:           config.NewtID,
			NewtSecret:       config.NewtSecret,
		})),
		"observability": len(observabilityTasks(observabilityConfig{
			Host: config.Host, SSHUser: config.AdminUser, PrivateKeyPath: config.PrivateKeyPath,
			BaseDomain: config.BaseDomain, AdminEmail: firstNonEmpty(config.PangolinAdminEmail, config.LetsEncryptEmail),
			AdminPassword: config.BeszelAdminPassword, PangolinPassword: config.PangolinAdminPassword, SystemToken: config.BeszelSystemToken,
			HubPrivateKey: config.BeszelHubPrivateKey, HubPublicKey: config.BeszelHubPublicKey,
			RepositoryCommit: config.ConfigRepositoryCommit, RepositoryOrigin: config.ConfigRepositoryOrigin,
			RepositoryCompose: config.ConfigRepositoryCompose, RepositorySHA256: config.ConfigRepositorySHA256,
			GitHubToken: config.GitHubToken,
		})),
	}
}

func (model profileRunModel) Init() tea.Cmd {
	return tea.Batch(model.start, model.spinner.Tick, waitForProfileRunMessage(model.messages))
}

func (model profileRunModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		model.width = msg.Width
		model.height = msg.Height
		model.progress.Width = clampInt(msg.Width-12, 24, 72)
		model.logViewport.Width = clampInt(msg.Width-4, 40, 100)
		model.logViewport.Height = clampInt(msg.Height-16, 6, 18)
		return model, nil
	case tea.KeyMsg:
		if model.done {
			switch msg.String() {
			case "q", "esc", "ctrl+c":
				return model, tea.Quit
			}
		}
		switch msg.String() {
		case "q", "ctrl+c":
			if model.cancel != nil {
				model.cancel()
			}
			model.cancelled = true
			model.appendRunLog("Cancelling setup run...")
			if msg.String() == "q" {
				return model, tea.Quit
			}
			return model, nil
		case "up", "k", "down", "j", "pgup", "pgdown":
			var cmd tea.Cmd
			model.logViewport, cmd = model.logViewport.Update(msg)
			return model, cmd
		}
	case spinner.TickMsg:
		if model.done {
			return model, nil
		}
		var cmd tea.Cmd
		model.spinner, cmd = model.spinner.Update(msg)
		return model, cmd
	case profileRunEventMsg:
		model.applyTaskEvent(msg.event)
		return model, waitForProfileRunMessage(model.messages)
	case profileRunFinishedMsg:
		model.done = true
		model.err = msg.err
		if msg.err != nil {
			model.appendRunLog("Run failed: " + msg.err.Error())
		} else {
			model.appendRunLog("Run complete.")
		}
		return model, nil
	}
	return model, nil
}

func waitForProfileRunMessage(messages <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		message, ok := <-messages
		if !ok {
			return nil
		}
		return message
	}
}

func startProfileRunCommand(ctx context.Context, profile Profile, config setupConfig, runID string, completedStages map[string]bool, reporter TaskReporter, profileReporter *profileRunReporter, messages chan<- tea.Msg) tea.Cmd {
	return func() tea.Msg {
		go func() {
			output := profileRunOutput{reporter: reporter, runID: runID}
			err := runFullSetupStages(ctx, profile, config, runID, completedStages, reporter, output, output)
			if err != nil {
				profileReporter.finishRun(runStatusFailed)
				if profileReporter.err != nil {
					err = profileReporter.err
				}
			} else {
				profileReporter.finishRun(runStatusComplete)
				if profileReporter.err != nil {
					err = profileReporter.err
				}
			}
			messages <- profileRunFinishedMsg{err: err}
			close(messages)
		}()
		return nil
	}
}

func startProfileStageRunCommand(ctx context.Context, profile Profile, config setupConfig, runID string, stage string, reporter TaskReporter, profileReporter *profileRunReporter, messages chan<- tea.Msg) tea.Cmd {
	return func() tea.Msg {
		go func() {
			output := profileRunOutput{reporter: reporter, runID: runID}
			err := runSetupStage(ctx, profile, config, runID, stage, reporter, output, output)
			if err != nil {
				profileReporter.finishRun(runStatusFailed)
				if profileReporter.err != nil {
					err = profileReporter.err
				}
			} else {
				if stage == "stacks" {
					profileReporter.state.StackRepositoryCommit = config.ConfigRepositoryCommit
				}
				profileReporter.finishRun(runStatusComplete)
				if profileReporter.err != nil {
					err = profileReporter.err
				}
			}
			messages <- profileRunFinishedMsg{err: err}
			close(messages)
		}()
		return nil
	}
}

func (model *profileRunModel) applyTaskEvent(event TaskEvent) {
	switch event.Type {
	case TaskRunStarted:
		model.setStageStatus(event.Stage, stageStatusRunning)
		model.currentStage = event.Stage
		model.appendRunLog(fmt.Sprintf("%s started.", profileRunStageLabel(event.Stage)))
	case TaskStarted:
		model.setStageStatus(event.Stage, stageStatusRunning)
		model.currentStage = event.Stage
		model.currentTask = event.TaskName
		model.setStageCurrent(event.Stage, event.TaskName)
		model.appendRunLog(fmt.Sprintf("%s: %s", profileRunStageLabel(event.Stage), event.TaskName))
	case TaskLogLine:
		prefix := profileRunStageLabel(event.Stage)
		if event.Stream != "" {
			prefix += " " + event.Stream
		}
		model.appendRunLog(fmt.Sprintf("%s: %s", prefix, event.Line))
	case TaskSucceeded:
		model.completedTasks++
		model.incrementStageCompleted(event.Stage)
	case TaskFailed:
		model.setStageStatus(event.Stage, stageStatusFailed)
		model.currentStage = event.Stage
		model.currentTask = event.TaskName
		model.appendRunLog(fmt.Sprintf("%s failed: %s", event.TaskName, event.Error))
	case TaskRunCompleted:
		model.setStageStatus(event.Stage, stageStatusComplete)
		model.clearStageCurrent(event.Stage)
		model.appendRunLog(fmt.Sprintf("%s complete.", profileRunStageLabel(event.Stage)))
	}
}

func (model *profileRunModel) setStageStatus(stage string, status string) {
	for index := range model.stages {
		if model.stages[index].Key == stage {
			model.stages[index].Status = status
			return
		}
	}
}

func (model *profileRunModel) setStageCurrent(stage string, current string) {
	for index := range model.stages {
		if model.stages[index].Key == stage {
			model.stages[index].Current = current
			return
		}
	}
}

func (model *profileRunModel) clearStageCurrent(stage string) {
	for index := range model.stages {
		if model.stages[index].Key == stage {
			model.stages[index].Current = ""
			return
		}
	}
}

func (model *profileRunModel) incrementStageCompleted(stage string) {
	for index := range model.stages {
		if model.stages[index].Key == stage {
			if model.stages[index].Completed < model.stages[index].Total {
				model.stages[index].Completed++
			}
			return
		}
	}
}

func (model *profileRunModel) appendRunLog(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	model.logLines = append(model.logLines, line)
	if len(model.logLines) > 200 {
		model.logLines = model.logLines[len(model.logLines)-200:]
	}
	model.logViewport.SetContent(strings.Join(model.logLines, "\n"))
	model.logViewport.GotoBottom()
}

func (model profileRunModel) View() string {
	var builder strings.Builder
	builder.WriteString(setupTitleStyle.Render("AegisNode setup run"))
	builder.WriteString("\n")
	builder.WriteString(setupHelpStyle.Render(fmt.Sprintf("%s (%s)", firstNonEmpty(model.profile.Name, model.profile.IP), model.profile.IP)))
	builder.WriteString("\n\n")

	status := model.spinner.View() + " Running " + model.runLabel
	if model.done && model.err == nil {
		status = "Complete"
	} else if model.done {
		status = "Failed"
	} else if model.cancelled {
		status = model.spinner.View() + " Cancelling setup"
	}
	builder.WriteString(status)
	builder.WriteString("\n")
	builder.WriteString(model.progress.ViewAs(model.taskProgress()))
	builder.WriteString(fmt.Sprintf("  Tasks: %d / %d\n", model.completedTasks, model.totalTasks))
	builder.WriteString(fmt.Sprintf("Current: %s\n\n", model.currentTaskLabel()))

	builder.WriteString("Stages\n")
	for _, stage := range model.stages {
		builder.WriteString(fmt.Sprintf("  %-10s %-9s %2d/%-2d", stage.Label, stage.Status, stage.Completed, stage.Total))
		if stage.Current != "" {
			builder.WriteString("  " + stage.Current)
		}
		builder.WriteString("\n")
	}
	builder.WriteString("\nLogs\n")
	builder.WriteString(model.logViewport.View())
	builder.WriteString("\n\n")
	if model.done {
		if model.err != nil {
			builder.WriteString(setupErrorStyle.Render(model.err.Error()))
			builder.WriteString("\n")
		}
		builder.WriteString(setupHelpStyle.Render("q exits. Run setup again to retry failed stages."))
	} else {
		builder.WriteString(setupHelpStyle.Render("q quits. Ctrl+C cancels. j/k or up/down scroll logs."))
	}
	return builder.String()
}

func (model profileRunModel) taskProgress() float64 {
	if model.totalTasks == 0 {
		return 0
	}
	return float64(model.completedTasks) / float64(model.totalTasks)
}

func (model profileRunModel) currentTaskLabel() string {
	if model.currentTask == "" {
		return "waiting for first remote task"
	}
	return fmt.Sprintf("%s - %s", profileRunStageLabel(model.currentStage), model.currentTask)
}

func profileRunStageLabel(stage string) string {
	switch stage {
	case "bootstrap":
		return "Bootstrap"
	case "harden":
		return "Harden"
	case "network":
		return "Network"
	case "proxy":
		return "Proxy"
	case "observability":
		return "Observability & stacks"
	case "platform":
		return "Platform"
	case "stacks":
		return "Sync stacks"
	default:
		if strings.HasPrefix(stage, "stack:") {
			return "Stack " + strings.TrimPrefix(stage, "stack:")
		}
		return "Run"
	}
}

func newSetupRunID() string {
	return "run-" + time.Now().UTC().Format("20060102t150405.000000000z")
}

func newSetupRun(id string, completedStages map[string]bool) SetupRun {
	now := time.Now().UTC()
	stages := map[string]SetupStageStatus{}
	for _, stage := range setupStageOrder {
		status := stageStatusPending
		if completedStages[stage] {
			status = stageStatusComplete
		}
		stages[stage] = SetupStageStatus{Status: status}
	}
	return SetupRun{ID: id, Status: runStatusPlanned, Stages: stages, CreatedAt: now, UpdatedAt: now}
}

func newSetupRunForStage(id string, selectedStage string, completedStages map[string]bool) SetupRun {
	run := newSetupRun(id, completedStages)
	run.Stages[selectedStage] = SetupStageStatus{Status: stageStatusPending}
	return run
}

func completedSetupStages(state ProfileState) map[string]bool {
	completed := map[string]bool{}
	for _, run := range state.Runs {
		for stage, status := range run.Stages {
			if status.Status == stageStatusComplete {
				completed[stage] = true
			}
		}
	}
	return completed
}

type profileRunReporter struct {
	store   ProfileStore
	profile Profile
	state   *ProfileState
	runID   string
	err     error
}

func (reporter *profileRunReporter) Report(event TaskEvent) {
	if reporter.err != nil {
		return
	}
	if err := reporter.store.AppendRunEvent(reporter.profile.ID, reporter.runID, event); err != nil {
		reporter.err = err
		return
	}
	run := reporter.state.Runs[reporter.runID]
	run.Status = runStatusRunning
	run.UpdatedAt = time.Now().UTC()
	stage := run.Stages[event.Stage]
	switch event.Type {
	case TaskRunStarted, TaskStarted:
		stage.Status = stageStatusRunning
		if stage.LastStarted.IsZero() {
			stage.LastStarted = event.Time
		}
	case TaskRunCompleted:
		stage.Status = stageStatusComplete
		stage.LastEnded = event.Time
		stage.LastError = ""
	case TaskFailed:
		stage.Status = stageStatusFailed
		stage.LastEnded = event.Time
		stage.LastError = event.Error
		run.Status = runStatusFailed
	}
	run.Stages[event.Stage] = stage
	reporter.state.Runs[reporter.runID] = run
	if err := reporter.store.Save(reporter.profile, *reporter.state); err != nil {
		reporter.err = err
	}
}

func (reporter *profileRunReporter) finishRun(status string) {
	if reporter.err != nil {
		return
	}
	run := reporter.state.Runs[reporter.runID]
	run.Status = status
	run.UpdatedAt = time.Now().UTC()
	reporter.state.Runs[reporter.runID] = run
	if err := reporter.store.Save(reporter.profile, *reporter.state); err != nil {
		reporter.err = err
	}
}

func runSetupPlan(ctx context.Context, config setupConfig, stdout, stderr io.Writer) error {
	fmt.Fprintln(stdout, "Selected plan:")
	fmt.Fprint(stdout, setupPlanSummary(config))
	fmt.Fprintln(stdout)
	if err := runPreflight(config, stdout); err != nil {
		return err
	}

	switch config.Mode {
	case setupModeDoctor:
		fmt.Fprintln(stdout, "Preflight complete. No remote changes were requested.")
		return nil
	case setupModeProviderKey:
		fmt.Fprintln(stdout, "Step 1/1: generate provider SSH keypair.")
		return generateProviderKeypair(ctx, keygenConfig{
			Path:    config.ProviderKeyPath,
			Comment: config.ProviderKeyComment,
		}, stdout, stderr)
	case setupModeBootstrapHarden:
		fmt.Fprintln(stdout, "Step 1/2: bootstrap administrative access.")
		if err := runBootstrap(ctx, []string{
			"--host", config.Host,
			"--ssh-user", config.InitialSSHUser,
			"--admin-user", config.AdminUser,
			"--admin-public-key", config.AdminPublicKeyPath,
			"--private-key", config.PrivateKeyPath,
		}, stdout, stderr); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "Step 2/2: harden server.")
		if err := runHarden(ctx, []string{
			"--host", config.Host,
			"--ssh-user", config.AdminUser,
			"--private-key", config.PrivateKeyPath,
		}, stdout, stderr); err != nil {
			return err
		}
		printSSHLoginGuidance(stdout, config)
		return nil
	case setupModeHardenOnly:
		fmt.Fprintln(stdout, "Step 1/1: harden server.")
		if err := runHarden(ctx, []string{
			"--host", config.Host,
			"--ssh-user", config.AdminUser,
			"--private-key", config.PrivateKeyPath,
		}, stdout, stderr); err != nil {
			return err
		}
		printSSHLoginGuidance(stdout, config)
		return nil
	case setupModeNetwork:
		fmt.Fprintln(stdout, "Step 1/1: configure Docker networking and UFW.")
		if err := runNetwork(ctx, []string{
			"--host", config.Host,
			"--ssh-user", config.AdminUser,
			"--private-key", config.PrivateKeyPath,
		}, stdout, stderr); err != nil {
			return err
		}
		printSSHLoginGuidance(stdout, config)
		return nil
	case setupModeProxy:
		fmt.Fprintln(stdout, "Step 1/1: deploy Pangolin and reverse proxy stack.")
		return runProxy(ctx, []string{
			"--host", config.Host,
			"--ssh-user", config.AdminUser,
			"--private-key", config.PrivateKeyPath,
			"--domain", config.BaseDomain,
			"--email", config.LetsEncryptEmail,
			"--server-secret", config.ServerSecret,
			"--setup-token", config.PangolinSetupToken,
		}, stdout, stderr)
	case setupModeFullRun:
		return errors.New("full setup requires a saved profile")
	default:
		return errors.New("unknown setup mode")
	}
}

func runPreflight(config setupConfig, stdout io.Writer) error {
	checks := preflightChecks(config)
	fmt.Fprintln(stdout, "Preflight checks:")
	failed := false
	for _, check := range checks {
		status := "ok"
		if !check.OK {
			if check.Required {
				status = "fail"
				failed = true
			} else {
				status = "skip"
			}
		}
		fmt.Fprintf(stdout, "  [%s] %s", status, check.Name)
		if check.Detail != "" {
			fmt.Fprintf(stdout, " - %s", check.Detail)
		}
		fmt.Fprintln(stdout)
	}
	if failed {
		return errors.New("preflight checks failed")
	}
	return nil
}

func isInteractiveWriter(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(file.Fd()) || isatty.IsCygwinTerminal(file.Fd())
}

func preflightChecks(config setupConfig) []preflightCheck {
	checks := []preflightCheck{
		nativeCapabilityCheck("native ED25519 key generation"),
	}
	if config.Mode == setupModeProviderKey {
		return checks
	}
	checks = append(checks, nativeCapabilityCheck("native SSH runner"))
	checks = append(checks, executableCheck("Git CLI", "git"))

	privateKeyRequired := config.Mode == setupModeBootstrapHarden || config.Mode == setupModeHardenOnly || config.Mode == setupModeNetwork || config.Mode == setupModeProxy || config.Mode == setupModeObservability || config.Mode == setupModeFullRun
	checks = append(checks, fileCheck("private key", config.PrivateKeyPath, privateKeyRequired))

	publicKeyRequired := config.Mode == setupModeBootstrapHarden || config.Mode == setupModeFullRun
	checks = append(checks, adminPublicKeyCheck(config.AdminPublicKeyPath, publicKeyRequired))
	return checks
}

func executableCheck(name, executable string) preflightCheck {
	path, err := exec.LookPath(executable)
	if err != nil {
		return preflightCheck{Name: name, Detail: err.Error(), OK: false, Required: true}
	}
	return preflightCheck{Name: name, Detail: path, OK: true, Required: true}
}

func nativeCapabilityCheck(name string) preflightCheck {
	return preflightCheck{Name: name, Detail: "built in", OK: true, Required: true}
}

func fileCheck(name, path string, required bool) preflightCheck {
	path = expandUserPath(path)
	if path == "" {
		return preflightCheck{Name: name, Detail: "not provided", OK: false, Required: required}
	}
	if _, err := os.Stat(path); err != nil {
		return preflightCheck{Name: name, Detail: err.Error(), OK: false, Required: required}
	}
	return preflightCheck{Name: name, Detail: path, OK: true, Required: required}
}

func adminPublicKeyCheck(path string, required bool) preflightCheck {
	check := fileCheck("admin public key", path, required)
	if !check.OK {
		return check
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return preflightCheck{Name: "admin public key", Detail: err.Error(), OK: false, Required: required}
	}
	fields := strings.Fields(strings.TrimSpace(string(data)))
	if len(fields) < 2 || fields[0] != "ssh-ed25519" {
		return preflightCheck{Name: "admin public key", Detail: "must be an ssh-ed25519 public key", OK: false, Required: required}
	}
	return preflightCheck{Name: "admin public key", Detail: path, OK: true, Required: required}
}

type setupStep int

const (
	setupStepMode setupStep = iota
	setupStepInput
	setupStepConfirm
)

type setupModel struct {
	step      setupStep
	selected  int
	mode      setupMode
	inputs    []textinput.Model
	focus     int
	err       string
	config    setupConfig
	done      bool
	cancelled bool
}

var (
	setupTitleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	setupHelpStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	setupWarningStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	setupErrorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
)

func newSetupModel() setupModel {
	return setupModel{step: setupStepMode}
}

func (model setupModel) Init() tea.Cmd {
	return textinput.Blink
}

func (model setupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return model, nil
	}

	switch key.String() {
	case "ctrl+c":
		model.cancelled = true
		return model, tea.Quit
	case "q":
		if model.step != setupStepInput {
			model.cancelled = true
			return model, tea.Quit
		}
	case "esc":
		switch model.step {
		case setupStepMode:
			return model, nil
		case setupStepInput:
			model.step = setupStepMode
		case setupStepConfirm:
			if model.mode == setupModeDoctor {
				model.step = setupStepMode
			} else {
				model.step = setupStepInput
			}
		}
		model.err = ""
		return model, nil
	}

	switch model.step {
	case setupStepMode:
		return model.updateMode(key)
	case setupStepInput:
		return model.updateInput(key)
	case setupStepConfirm:
		return model.updateConfirm(key)
	default:
		return model, nil
	}
}

func (model setupModel) updateMode(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "up", "k":
		if model.selected > 0 {
			model.selected--
		}
	case "down", "j":
		if model.selected < len(setupModeOptions())-1 {
			model.selected++
		}
	case "enter":
		model.mode = setupMode(model.selected)
		model.err = ""
		if model.mode == setupModeDoctor {
			model.config = setupConfig{Mode: setupModeDoctor}
			model.step = setupStepConfirm
			return model, nil
		}
		model.inputs = setupInputs(model.mode)
		model.focus = 0
		model.inputs[0].Focus()
		model.step = setupStepInput
	}
	return model, nil
}

func (model setupModel) updateInput(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "tab", "down":
		model.nextInput()
		return model, nil
	case "shift+tab", "up":
		model.previousInput()
		return model, nil
	case "enter":
		if model.focus < len(model.inputs)-1 {
			model.nextInput()
			return model, nil
		}
		config, err := model.configFromInputs()
		if err != nil {
			model.err = err.Error()
			return model, nil
		}
		model.config = config
		model.err = ""
		model.step = setupStepConfirm
		return model, nil
	}

	var cmd tea.Cmd
	model.inputs[model.focus], cmd = model.inputs[model.focus].Update(key)
	return model, cmd
}

func (model *setupModel) nextInput() {
	model.inputs[model.focus].Blur()
	model.focus = (model.focus + 1) % len(model.inputs)
	model.inputs[model.focus].Focus()
}

func (model *setupModel) previousInput() {
	model.inputs[model.focus].Blur()
	model.focus--
	if model.focus < 0 {
		model.focus = len(model.inputs) - 1
	}
	model.inputs[model.focus].Focus()
}

func (model setupModel) updateConfirm(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "r", "R":
		model.done = true
		return model, tea.Quit
	case "e", "E":
		model.err = ""
		if model.mode == setupModeDoctor {
			model.step = setupStepMode
			return model, nil
		}
		model.step = setupStepInput
		return model, nil
	}
	return model, nil
}

func (model setupModel) View() string {
	var builder strings.Builder
	builder.WriteString(setupTitleStyle.Render("AegisNode setup"))
	builder.WriteString("\n\n")

	switch model.step {
	case setupStepMode:
		builder.WriteString("Choose what you want to do. Key generation is local only. Server setup paths do not create billable cloud resources; use them with an existing disposable Ubuntu VPS for live smoke testing.\n\n")
		for index, option := range setupModeOptions() {
			cursor := " "
			if index == model.selected {
				cursor = ">"
			}
			builder.WriteString(fmt.Sprintf("%s %s\n", cursor, option.Label))
			builder.WriteString(setupHelpStyle.Render("  "+option.Description) + "\n")
		}
		builder.WriteString("\n")
		builder.WriteString(setupHelpStyle.Render("Use j/k, then Enter. q quits."))
	case setupStepInput:
		builder.WriteString(setupModeOptions()[int(model.mode)].Label)
		builder.WriteString("\n")
		builder.WriteString(setupHelpStyle.Render(setupModeOptions()[int(model.mode)].Description))
		builder.WriteString("\n\n")
		if model.mode == setupModeProviderKey {
			builder.WriteString("AegisNode will write the private key and matching .pub file, then print the public key for Hetzner or DigitalOcean.")
		} else if model.mode == setupModeProxy {
			builder.WriteString("Enter the target host, domain, and Let's Encrypt email. AegisNode generates the Pangolin server secret.")
		} else {
			builder.WriteString("Enter the target host and confirm the SSH key. AegisNode uses the matching .pub file for the admin account.")
		}
		builder.WriteString("\n\n")
		for _, input := range model.inputs {
			builder.WriteString(input.View())
			builder.WriteString("\n")
		}
		if model.err != "" {
			builder.WriteString("\n")
			builder.WriteString(setupErrorStyle.Render(model.err))
			builder.WriteString("\n")
		}
		builder.WriteString("\n")
		builder.WriteString(setupHelpStyle.Render("Enter advances. Tab changes field. Esc goes back. q quits."))
	case setupStepConfirm:
		builder.WriteString("Review plan:\n\n")
		builder.WriteString(setupPlanSummary(model.config))
		builder.WriteString("\n")
		if model.mode == setupModeProviderKey {
			builder.WriteString("AegisNode will create an unencrypted local ED25519 keypair for non-interactive SSH automation. It will not contact your cloud provider; you will copy the printed public key into the provider UI.\n")
		} else {
			builder.WriteString("Before remote changes, AegisNode will check built-in SSH/key support and key files. If a required check fails, it stops before contacting the server.\n")
		}
		builder.WriteString("\n")
		builder.WriteString(setupHelpStyle.Render("r runs it. e edits. Esc goes back. q quits."))
	}
	return builder.String()
}

type setupModeOption struct {
	Label       string
	Description string
}

func setupModeOptions() []setupModeOption {
	return []setupModeOption{
		{
			Label:       "Prepare the AegisNode SSH key",
			Description: "Generate the ED25519 keypair used for provider login and later aegisadmin access.",
		},
		{
			Label:       "Set up an existing Ubuntu VPS",
			Description: "Create the admin account, install its key, then apply baseline hardening.",
		},
		{
			Label:       "Harden an already set-up VPS",
			Description: "Use this when the admin account already exists and you only need baseline security settings.",
		},
		{
			Label:       "Configure Docker networking and UFW",
			Description: "Install Docker, preserve bridge NAT, and configure UFW host ingress and routed traffic.",
		},
		{
			Label:       "Deploy Pangolin and reverse proxy",
			Description: "Write and start the Traefik, Pangolin, and Gerbil Compose stack.",
		},
		{
			Label:       "Run local preflight checks only",
			Description: "Checks built-in SSH/key support and local key files without making server changes.",
		},
	}
}

func setupInputs(mode setupMode) []textinput.Model {
	if mode == setupModeProviderKey {
		defaultConfig := defaultKeygenConfig()
		return newSetupInputs([]setupInputField{
			{label: "Private key path", placeholder: defaultConfig.Path, value: defaultConfig.Path},
			{label: "Key comment", placeholder: defaultConfig.Comment, value: defaultConfig.Comment},
		})
	}

	fields := []setupInputField{
		{label: "Host", placeholder: "203.0.113.10"},
	}
	if mode == setupModeBootstrapHarden {
		fields = append(fields,
			setupInputField{label: "Initial SSH user", value: "root"},
			setupInputField{label: "Admin user", value: "aegisadmin"},
		)
	} else {
		fields = append(fields, setupInputField{label: "Admin SSH user", value: "aegisadmin"})
	}
	fields = append(fields, setupInputField{label: "AegisNode private key", placeholder: defaultKeygenConfig().Path, value: defaultKeygenConfig().Path})
	if mode == setupModeProxy {
		fields = append(fields,
			setupInputField{label: "Base domain", placeholder: "example.com"},
			setupInputField{label: "Let's Encrypt email", placeholder: "admin@example.com"},
		)
	}
	return newSetupInputs(fields)
}

type setupInputField struct {
	label       string
	placeholder string
	value       string
	secret      bool
}

func newSetupInputs(fields []setupInputField) []textinput.Model {
	inputs := make([]textinput.Model, 0, len(fields))
	for _, field := range fields {
		input := textinput.New()
		input.Prompt = field.label + ": "
		input.Placeholder = field.placeholder
		input.SetValue(field.value)
		input.CharLimit = 256
		input.Width = 72
		if field.secret {
			input.EchoMode = textinput.EchoPassword
		}
		inputs = append(inputs, input)
	}
	return inputs
}

func (model setupModel) configFromInputs() (setupConfig, error) {
	value := func(index int) string {
		return strings.TrimSpace(model.inputs[index].Value())
	}
	config := setupConfig{Mode: model.mode, Host: value(0)}

	switch model.mode {
	case setupModeProviderKey:
		config.Host = ""
		config.ProviderKeyPath = expandUserPath(value(0))
		config.ProviderKeyComment = value(1)
		if config.ProviderKeyPath == "" {
			return setupConfig{}, errors.New("private key path is required")
		}
	case setupModeBootstrapHarden:
		if config.Host == "" {
			return setupConfig{}, errors.New("host is required")
		}
		config.InitialSSHUser = firstNonEmpty(value(1), "root")
		config.AdminUser = firstNonEmpty(value(2), "aegisadmin")
		config.PrivateKeyPath = expandUserPath(value(3))
		config.AdminPublicKeyPath = publicKeyPath(config.PrivateKeyPath)
		if config.PrivateKeyPath == "" {
			return setupConfig{}, errors.New("private key path is required")
		}
		if !linuxUsername.MatchString(config.InitialSSHUser) || !linuxUsername.MatchString(config.AdminUser) {
			return setupConfig{}, errors.New("SSH users must be valid Linux usernames")
		}
	case setupModeHardenOnly:
		if config.Host == "" {
			return setupConfig{}, errors.New("host is required")
		}
		config.AdminUser = firstNonEmpty(value(1), "aegisadmin")
		config.PrivateKeyPath = expandUserPath(value(2))
		if config.PrivateKeyPath == "" {
			return setupConfig{}, errors.New("private key path is required")
		}
		if !linuxUsername.MatchString(config.AdminUser) {
			return setupConfig{}, errors.New("admin SSH user must be a valid Linux username")
		}
	case setupModeNetwork:
		if config.Host == "" {
			return setupConfig{}, errors.New("host is required")
		}
		config.AdminUser = firstNonEmpty(value(1), "aegisadmin")
		config.PrivateKeyPath = expandUserPath(value(2))
		if config.PrivateKeyPath == "" {
			return setupConfig{}, errors.New("private key path is required")
		}
		if !linuxUsername.MatchString(config.AdminUser) {
			return setupConfig{}, errors.New("admin SSH user must be a valid Linux username")
		}
	case setupModeProxy:
		if config.Host == "" {
			return setupConfig{}, errors.New("host is required")
		}
		config.AdminUser = firstNonEmpty(value(1), "aegisadmin")
		config.PrivateKeyPath = expandUserPath(value(2))
		config.BaseDomain = value(3)
		config.LetsEncryptEmail = value(4)
		serverSecret, err := GenerateServerSecret()
		if err != nil {
			return setupConfig{}, fmt.Errorf("generate server secret: %w", err)
		}
		config.ServerSecret = serverSecret
		setupToken, err := GeneratePangolinSetupToken()
		if err != nil {
			return setupConfig{}, fmt.Errorf("generate Pangolin setup token: %w", err)
		}
		config.PangolinSetupToken = setupToken
		if config.PrivateKeyPath == "" {
			return setupConfig{}, errors.New("private key path is required")
		}
		if !linuxUsername.MatchString(config.AdminUser) {
			return setupConfig{}, errors.New("admin SSH user must be a valid Linux username")
		}
		if err := validateProxyConfig(proxyConfig{
			Host:             config.Host,
			SSHUser:          config.AdminUser,
			PrivateKeyPath:   config.PrivateKeyPath,
			BaseDomain:       config.BaseDomain,
			LetsEncryptEmail: config.LetsEncryptEmail,
			ServerSecret:     config.ServerSecret,
			SetupToken:       config.PangolinSetupToken,
		}); err != nil {
			return setupConfig{}, err
		}
	default:
		return setupConfig{}, errors.New("unknown setup mode")
	}
	return config, nil
}

func expandUserPath(path string) string {
	path = strings.TrimSpace(os.ExpandEnv(path))
	if path == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return home + path[1:]
	}
	return path
}

func setupPlanSummary(config setupConfig) string {
	switch config.Mode {
	case setupModeProviderKey:
		return fmt.Sprintf(
			"- Generate the AegisNode ED25519 keypair at %s.\n- Print the public key and provider registration guidance.\n",
			config.ProviderKeyPath,
		)
	case setupModeDoctor:
		return "- Run local preflight checks.\n"
	case setupModeBootstrapHarden:
		return fmt.Sprintf(
			"- Connect to %s as %s with %s.\n- Install %s using %s.\n- Harden the server as %s.\n",
			config.Host,
			config.InitialSSHUser,
			config.PrivateKeyPath,
			config.AdminUser,
			config.AdminPublicKeyPath,
			config.AdminUser,
		)
	case setupModeHardenOnly:
		return fmt.Sprintf("- Harden %s as %s using %s.\n", config.Host, config.AdminUser, config.PrivateKeyPath)
	case setupModeNetwork:
		return fmt.Sprintf(
			"- Connect to %s as %s with %s.\n- Configure Docker networking and UFW policy.\n",
			config.Host,
			config.AdminUser,
			config.PrivateKeyPath,
		)
	case setupModeProxy:
		return fmt.Sprintf(
			"- Connect to %s as %s with %s.\n- Deploy Traefik, Pangolin, Gerbil, Newt, Beszel, and Dozzle for %s.\n- %s.\n",
			config.Host,
			config.AdminUser,
			config.PrivateKeyPath,
			config.BaseDomain,
			requiredDNSGuidance(config.BaseDomain, config.Host),
		)
	case setupModeFullRun:
		return fmt.Sprintf(
			"- Use profile %s for %s.\n- Connect first as %s, create or update %s, then harden the server.\n- Configure Docker networking and UFW as %s.\n- Deploy Traefik, Pangolin, Gerbil, Newt, Beszel, and Dozzle for %s.\n- Deploy committed observability configuration from %s.\n- Pangolin and observability secrets are generated, saved, and reused without printing them.\n- %s.\n",
			firstNonEmpty(config.ProfileID, "(unsaved)"),
			config.Host,
			config.InitialSSHUser,
			config.AdminUser,
			config.AdminUser,
			config.BaseDomain,
			firstNonEmpty(config.ConfigRepositoryPath, "the profile's default repository"),
			requiredDNSGuidance(config.BaseDomain, config.Host),
		)
	default:
		return "- Unknown plan.\n"
	}
}

func publicKeyPath(privateKeyPath string) string {
	if privateKeyPath == "" {
		return ""
	}
	return privateKeyPath + ".pub"
}

func printSSHLoginGuidance(stdout io.Writer, config setupConfig) {
	if config.Host == "" || config.AdminUser == "" || config.PrivateKeyPath == "" {
		return
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Login command:")
	fmt.Fprintf(stdout, "  ssh -i %s %s@%s\n", shellQuoteForDisplay(config.PrivateKeyPath), config.AdminUser, config.Host)
}

func shellQuoteForDisplay(value string) string {
	if value == "" || strings.ContainsAny(value, " \t\n'\"\\$`!") {
		return shellQuote(value)
	}
	return value
}
