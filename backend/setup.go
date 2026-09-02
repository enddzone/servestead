package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"charm.land/bubbles/v2/filepicker"
	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
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
	ConfigRepositoryBranch  string
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
  servestead setup [--ip <ipv4-or-hostname>]

Launches guided setup. With --ip, setup creates or selects a saved profile, collects all full-run values before remote execution, then runs bootstrap, hardening, Docker networking, and reverse proxy deployment end to end.
`

const doctorUsage = `Usage of doctor:
  servestead doctor [--admin-public-key <path>] [--private-key <path>]

Runs local preflight checks for built-in SSH/key support and optional key files without contacting a server.
`

const (
	profileSetupMinWidth          = 80
	profileSetupMinHeight         = 24
	setupStageStackPrefix         = "stack:"
	setupPrivateKeyLabel          = "Servestead private key"
	setupBaseDomainLabel          = "Base domain"
	setupLetsEncryptEmailLabel    = "Let's Encrypt email"
	setupExampleDomainPlaceholder = "example.com"
	setupAdminEmailPlaceholder    = "admin@example.com"
	setupGeneratedPlaceholder     = "generated-placeholder"
	setupNoProfileSelectedMessage = "no profile selected"
	setupNoProfileSelectedView    = "No profile selected."
	setupNoStackSelectedMessage   = "no stack selected"
	setupProfileStoreUnavailable  = "profile store is unavailable"
	setupSelectedPlanHeader       = "Selected plan:"
	setupAdminPublicKeyLabel      = "admin public key"
	setupKeyCtrlA                 = "ctrl+a"
	setupKeyCtrlC                 = "ctrl+c"
	setupKeyCtrlE                 = "ctrl+e"
	setupKeyCtrlS                 = "ctrl+s"
	setupKeyCtrlX                 = "ctrl+x"
	setupKeyShiftTab              = "shift+tab"
	setupFlagHost                 = "--host"
	setupFlagSSHUser              = "--ssh-user"
	setupFlagPrivateKey           = "--private-key"
)

func runSetup(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	options, err := parseSetupOptions(args, stderr)
	if err != nil {
		return err
	}
	if options.IP != "" || len(args) > 0 {
		return runSetupFromOptions(ctx, options, stdout, stderr)
	}
	return runInteractiveSetup(ctx, stdout, stderr)
}

func parseSetupOptions(args []string, stderr io.Writer) (setupCLIOptions, error) {
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
		return setupCLIOptions{}, err
	}
	if flags.NArg() != 0 {
		return setupCLIOptions{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	return options, nil
}

func runSetupFromOptions(ctx context.Context, options setupCLIOptions, stdout, stderr io.Writer) error {
	store, err := newDefaultProfileStore()
	if err != nil {
		return err
	}
	profile, state, config, lock, err := prepareProfileSetupWithOperationLock(options, store, stderr)
	if err != nil {
		return err
	}
	if shouldUseProfileRunView(options, stderr) {
		return runProfileSetupPlanWithRunView(ctx, profileSetupPlanRun{
			store: store, profile: profile, state: state, config: config,
			stdout: stdout, stderr: stderr, lock: lock,
		}, false)
	}
	return runProfileSetupPlan(ctx, store, profile, state, config, stdout, stderr, lock)
}

func runInteractiveSetup(ctx context.Context, stdout, stderr io.Writer) error {
	store, err := newDefaultProfileStore()
	if err != nil {
		return err
	}
	if !isInteractiveWriter(stderr) {
		return errors.New("interactive setup requires a terminal; use setup --ip with --domain, --email, and --yes for scripts")
	}
	resume := setupResume{}
	for {
		request, err := collectSetupRequest(ctx, store, stderr, resume)
		if errors.Is(err, errSetupQuit) {
			return nil
		}
		if err != nil {
			return err
		}
		if request.Legacy {
			return runSetupPlan(ctx, request.LegacyConfig, stdout, stderr)
		}
		if request.Stage != "" {
			nextResume, err := runInteractiveSetupStage(ctx, store, request, stdout, stderr)
			if errors.Is(err, errReturnToSetup) {
				resume = nextResume
				continue
			}
			return err
		}
		nextResume, err := runInteractiveSetupPlan(ctx, store, request, stdout, stderr)
		if errors.Is(err, errReturnToSetup) {
			resume = nextResume
			continue
		}
		return err
	}
}

func runInteractiveSetupStage(ctx context.Context, store ProfileStore, request setupRequest, stdout, stderr io.Writer) (setupResume, error) {
	profile, state, config, lock, err := prepareProfileStageSetupWithOperationLock(request.ProfileOptions, store, request.Stage)
	if err != nil {
		return setupResume{}, err
	}
	err = runProfileSetupStagePlanWithRunView(ctx, profileSetupPlanRun{
		store: store, profile: profile, state: state, config: config,
		stdout: stdout, stderr: stderr, lock: lock,
	}, request.Stage, true)
	return resumeAfterStage(request.ProfileOptions.ProfileID, request.Stage), err
}

func runInteractiveSetupPlan(ctx context.Context, store ProfileStore, request setupRequest, stdout, stderr io.Writer) (setupResume, error) {
	profile, state, config, lock, err := prepareProfileSetupWithOperationLock(request.ProfileOptions, store, stderr)
	if err != nil {
		return setupResume{}, err
	}
	if shouldUseProfileRunView(request.ProfileOptions, stderr) {
		err = runProfileSetupPlanWithRunView(ctx, profileSetupPlanRun{
			store: store, profile: profile, state: state, config: config,
			stdout: stdout, stderr: stderr, lock: lock,
		}, true)
		return setupResume{ProfileID: profile.ID, Screen: profileSetupScreenDashboard}, err
	}
	return setupResume{}, runProfileSetupPlan(ctx, store, profile, state, config, stdout, stderr, lock)
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

func collectSetupRequest(ctx context.Context, store ProfileStore, output io.Writer, resume setupResume) (setupRequest, error) {
	resumeState := resume
	for {
		result, err := runProfileSetupRequestTUI(ctx, store, output, resumeState)
		if err != nil {
			return setupRequest{}, err
		}
		request, nextResume, repeat, err := setupRequestFromResult(ctx, store, output, result)
		if err != nil {
			return setupRequest{}, err
		}
		if repeat {
			resumeState = nextResume
			continue
		}
		return request, nil
	}
}

func runProfileSetupRequestTUI(ctx context.Context, store ProfileStore, output io.Writer, resume setupResume) (profileSetupModel, error) {
	profiles, err := loadProfileChoices(store)
	if err != nil {
		return profileSetupModel{}, err
	}
	model := newProfileSetupModel(profiles)
	model.tuiContext = ctx
	model.profileStore = store
	applySetupResume(&model, resume)
	program := tea.NewProgram(model, tea.WithOutput(output))
	finalModel, err := program.Run()
	if err != nil {
		return profileSetupModel{}, fmt.Errorf("run setup TUI: %w", err)
	}
	result, ok := finalModel.(profileSetupModel)
	if !ok {
		return profileSetupModel{}, errors.New("setup TUI returned an unexpected model")
	}
	return result, nil
}

func setupRequestFromResult(ctx context.Context, store ProfileStore, output io.Writer, result profileSetupModel) (setupRequest, setupResume, bool, error) {
	if result.quit {
		return setupRequest{}, setupResume{}, false, errSetupQuit
	}
	if result.cancelled {
		return setupRequest{}, setupResume{}, false, errors.New("setup cancelled")
	}
	if result.deleteProfileID != "" {
		return setupRequest{}, setupResume{}, true, deleteProfileWhenIdle(store, result.deleteProfileID)
	}
	if result.legacy {
		config, err := collectLegacySetupConfig(output)
		return setupRequest{Legacy: true, LegacyConfig: config}, setupResume{}, false, err
	}
	if result.provision {
		profile, err := collectDigitalOceanProvisionProfile(ctx, store, output)
		if errors.Is(err, errReturnToSetup) {
			return setupRequest{}, setupResume{}, true, nil
		}
		return setupRequest{}, setupResume{ProfileID: profile.ID, Screen: profileSetupScreenDashboard}, true, err
	}
	if !result.done {
		return setupRequest{}, setupResume{}, false, errors.New("setup did not complete")
	}
	return completedSetupRequest(result)
}

func collectDigitalOceanProvisionProfile(ctx context.Context, store ProfileStore, output io.Writer) (Profile, error) {
	model := newDigitalOceanProvisionModel(ctx, store)
	program := tea.NewProgram(model, tea.WithOutput(output))
	finalModel, err := program.Run()
	if err != nil {
		return Profile{}, fmt.Errorf("run provisioning TUI: %w", err)
	}
	result, ok := finalModel.(digitalOceanProvisionModel)
	if !ok {
		return Profile{}, errors.New("provisioning TUI returned an unexpected model")
	}
	return profileFromProvisionResult(result)
}

func profileFromProvisionResult(result digitalOceanProvisionModel) (Profile, error) {
	if result.returnToSetup {
		return Profile{}, errReturnToSetup
	}
	if result.quit {
		return Profile{}, errSetupQuit
	}
	if result.remoteOutcomeUnknown {
		return Profile{}, errors.New(result.err)
	}
	if result.cancelled {
		return Profile{}, errors.New("provisioning cancelled")
	}
	if !result.done {
		return Profile{}, errors.New("provisioning did not complete")
	}
	return result.createdProfile, nil
}

func completedSetupRequest(result profileSetupModel) (setupRequest, setupResume, bool, error) {
	if result.singleStage != "" {
		options, err := result.optionsForSelectedProfile()
		return setupRequest{ProfileOptions: options, Stage: result.singleStage}, setupResume{}, false, err
	}
	options, err := result.optionsFromInputs()
	return setupRequest{ProfileOptions: options}, setupResume{}, false, err
}

type setupResume struct {
	ProfileID string
	Screen    profileSetupScreen
}

func resumeAfterStage(profileID, stage string) setupResume {
	screen := profileSetupScreenDashboard
	if stage == "stacks" || strings.HasPrefix(stage, setupStageStackPrefix) {
		screen = profileSetupScreenStacks
	}
	return setupResume{ProfileID: profileID, Screen: screen}
}

func applySetupResume(model *profileSetupModel, resume setupResume) {
	if resume.ProfileID == "" {
		return
	}
	for index, choice := range model.profiles {
		if choice.Profile.ID != resume.ProfileID {
			continue
		}
		model.selectedIndex = index
		model.fresh = false
		model.setInputsFromChoice(false)
		model.refreshDashboard()
		switch resume.Screen {
		case profileSetupScreenStacks:
			model.screen = profileSetupScreenStacks
			model.refreshStacks()
			model.stackTable.Focus()
		default:
			model.screen = profileSetupScreenDashboard
		}
		return
	}
}

func collectLegacySetupConfig(output io.Writer) (setupConfig, error) {
	model := newSetupModel()
	program := tea.NewProgram(model, tea.WithOutput(output))
	finalModel, err := program.Run()
	if err != nil {
		return setupConfig{}, fmt.Errorf("run setup TUI: %w", err)
	}
	result, ok := finalModel.(setupModel)
	if !ok {
		return setupConfig{}, errors.New("setup TUI returned an unexpected model")
	}
	if result.quit {
		return setupConfig{}, errSetupQuit
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
	profileSetupScreenRunHistory
	profileSetupScreenRunDetail
	profileSetupScreenIntake
	profileSetupScreenAdvanced
	profileSetupScreenRepository
	profileSetupScreenRepositoryDetails
	profileSetupScreenGitHubToken
	profileSetupScreenStacks
	profileSetupScreenStackCompose
	profileSetupScreenStackServices
	profileSetupScreenStackEditor
	profileSetupScreenStackResourceEditor
	profileSetupScreenStackEnvironment
	profileSetupScreenStackReview
	profileSetupScreenStackDeleteConfirm
	profileSetupScreenStackDiff
	profileSetupScreenStackCommit
	profileSetupScreenCloud
	profileSetupScreenCloudConfirm
	profileSetupScreenCloudRunning
	profileSetupScreenCloudSaveRecovery
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
	requestID  uint64
	profileID  string
	baseDomain string
	complete   bool
	err        error
}

type profileDeletedMsg struct {
	profileID string
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
	screen                       profileSetupScreen
	profiles                     []profileChoice
	profileList                  list.Model
	repositoryList               list.Model
	stageTable                   table.Model
	runTable                     table.Model
	progress                     progress.Model
	dashboardViewport            viewport.Model
	planViewport                 viewport.Model
	runLogViewport               viewport.Model
	stackEditorViewport          viewport.Model
	stackReviewViewport          viewport.Model
	help                         help.Model
	selectedIndex                int
	deleteProfileID              string
	deleteProfilePending         bool
	runs                         []SetupRun
	selectedRunID                string
	runLogPath                   string
	runDetailLoading             bool
	runDetailRequestID           uint64
	runDetailCancel              context.CancelFunc
	singleStage                  string
	pangolinStatus               pangolinRegistrationStatus
	pangolinError                string
	pangolinRequestID            uint64
	pangolinRequestDomain        string
	showPangolinAccess           bool
	fresh                        bool
	inputs                       []textinput.Model
	advanced                     []textinput.Model
	repositoryInputs             []textinput.Model
	githubTokenInput             textinput.Model
	githubTokenNotice            string
	deleteProfileInput           textinput.Model
	stackComposeInput            textinput.Model
	stackComposePicker           filepicker.Model
	stackComposeBrowserError     string
	stackComposeManual           bool
	stackComposePath             string
	stackTable                   table.Model
	stacks                       []editableStack
	stackInputs                  []textinput.Model
	stackServices                []composeServiceSummary
	stackServiceTable            table.Model
	stackCompose                 string
	stackOriginalName            string
	stackMetadataMissing         bool
	stackResources               []stackPublicResource
	stackResourceTable           table.Model
	stackResourceInputs          []textinput.Model
	stackResourceIndex           int
	stackResourceReturn          profileSetupScreen
	stackResourceAdvanced        bool
	stackEnvironmentInput        textinput.Model
	stackEnvironmentPicker       filepicker.Model
	stackEnvironmentBrowserError string
	stackEnvironmentMode         stackEnvironmentMode
	stackEnvironmentOptions      []stackEnvironmentOption
	stackEnvironmentCursor       int
	stackEnvironmentReturn       profileSetupScreen
	stackEnvironment             string
	stackEnvironmentOriginal     string
	stackEnvironmentKeys         []string
	stackEnvironmentDirty        bool
	profileStore                 ProfileStore
	profileNotice                string
	stackNotice                  string
	stackGitStatus               string
	stackHead                    string
	stackNeedsPush               bool
	stackSyncStatus              string
	stackDiffViewport            viewport.Model
	stackCommitInput             textinput.Model
	stackDeleteInput             textinput.Model
	stackSpinner                 spinner.Model
	stackOperation               profileStackOperationKind
	stackOperationCancel         context.CancelFunc
	stackOperationCancelling     bool
	tuiContext                   context.Context
	cloudAction                  string
	cloudNotice                  string
	cloudOutcomeUnknownID        string
	cloudCancel                  context.CancelFunc
	cloudCancelling              bool
	cloudRecoveryProfile         Profile
	cloudRecoveryState           ProfileState
	cloudRecoverySaving          bool
	cloudTokenInput              textinput.Model
	cloudConfirmInput            textinput.Model
	repositoryMode               string
	focus                        int
	err                          string
	width                        int
	height                       int
	done                         bool
	legacy                       bool
	provision                    bool
	cancelled                    bool
	quit                         bool
}

type stackEnvironmentMode int

const (
	stackEnvironmentChoose stackEnvironmentMode = iota
	stackEnvironmentBrowse
	stackEnvironmentManual
)

type stackEnvironmentOption struct {
	kind   string
	label  string
	detail string
	path   string
}

func newProfileSetupModel(profiles []profileChoice) profileSetupModel {
	items := profilePickerItems(profiles)

	delegate := list.NewDefaultDelegate()
	delegate.SetHeight(2)
	profileList := list.New(items, delegate, 82, 14)
	profileList.Title = "Servestead profiles"
	profileList.SetShowStatusBar(false)
	profileList.SetFilteringEnabled(false)
	profileList.DisableQuitKeybindings()
	profileList.SetShowHelp(false)

	repositoryList := list.New([]list.Item{
		profileListItem{
			kind:        "create",
			title:       "Create a new local repository",
			description: "Servestead creates and commits the scaffold after confirmation, before any SSH commands run.",
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
	repositoryList.SetShowHelp(false)

	model := profileSetupModel{
		screen:            profileSetupScreenPicker,
		profiles:          profiles,
		profileList:       profileList,
		repositoryList:    repositoryList,
		stageTable:        newProfileStageTable(nil),
		runTable:          newRunHistoryTable(nil),
		stackTable:        newStackTable(nil, "", nil),
		stackServiceTable: newStackServiceTable(nil, nil),
		stackDiffViewport: viewport.New(viewport.WithWidth(100), viewport.WithHeight(18)),
		stackSpinner:      newProfileStackSpinner(),
		progress:          progress.New(progress.WithWidth(42)),
		dashboardViewport: viewport.New(viewport.WithWidth(82), viewport.WithHeight(19)),
		planViewport:      viewport.New(viewport.WithWidth(82), viewport.WithHeight(10)),
		runLogViewport:    viewport.New(viewport.WithWidth(82), viewport.WithHeight(14)),
		stackEditorViewport: viewport.New(
			viewport.WithWidth(82), viewport.WithHeight(19),
		),
		stackReviewViewport: viewport.New(
			viewport.WithWidth(82), viewport.WithHeight(19),
		),
		help:          help.New(),
		selectedIndex: -1,
		width:         82,
		height:        24,
	}
	model.inputs = setupProfileInputs(setupCLIOptions{})
	model.advanced = setupAdvancedInputs(setupCLIOptions{})
	model.repositoryInputs = setupRepositoryInputs(setupCLIOptions{})
	model.repositoryMode = "create"
	model.githubTokenInput = newSetupInputs([]setupInputField{{
		label: "GitHub PAT", placeholder: "paste token", secret: true,
	}})[0]
	model.deleteProfileInput = newSetupInputs([]setupInputField{{
		label: "Confirmation", placeholder: "delete profile-name",
	}})[0]
	model.stackComposeInput = newSetupInputs([]setupInputField{{
		label: "Docker Compose file", placeholder: "/path/to/docker-compose.yml",
	}})[0]
	model.stackEnvironmentInput = newSetupInputs([]setupInputField{{
		label: "Runtime secret .env file", placeholder: "/path/to/.env",
	}})[0]
	model.stackComposePicker = newStackFilePicker(".", []string{".yaml", ".yml"}, false)
	model.stackEnvironmentPicker = newStackFilePicker(".", nil, true)
	model.stackCommitInput = newSetupInputs([]setupInputField{{
		label: "Commit message", placeholder: "Update application stacks",
	}})[0]
	model.stackDeleteInput = newSetupInputs([]setupInputField{{
		label: "Confirmation", placeholder: "delete stack-name",
	}})[0]
	model.cloudTokenInput = newSetupInputs([]setupInputField{{
		label: "DigitalOcean API token", value: firstNonEmpty(os.Getenv("DIGITALOCEAN_ACCESS_TOKEN"), os.Getenv("DIGITALOCEAN_TOKEN")), secret: true,
	}})[0]
	model.cloudConfirmInput = newSetupInputs([]setupInputField{{
		label: "Confirmation",
	}})[0]
	model.inputs[0].Focus()
	return model
}

func profilePickerItems(profiles []profileChoice) []list.Item {
	items := make([]list.Item, 0, len(profiles)+3)
	for index, choice := range profiles {
		status := latestProfileStatus(choice.State)
		if status == "" {
			status = "no runs yet"
		}
		items = append(items, profileListItem{
			kind:        "profile",
			index:       index,
			title:       sanitizeTerminalLine(firstNonEmpty(choice.Profile.Name, choice.Profile.IP)),
			description: fmt.Sprintf("%s - %s - updated %s", sanitizeTerminalLine(choice.Profile.IP), sanitizeTerminalLine(status), choice.Profile.UpdatedAt.Local().Format("2006-01-02 15:04")),
		})
	}
	items = append(items,
		profileListItem{kind: "provision", title: "Provision a new DigitalOcean VPS", description: "Create one billable Droplet, save it as a profile, then return to setup."},
		profileListItem{kind: "new", title: "Set up a new server profile", description: "Collect IP, SSH key, domain, and email before running the full setup plan."},
		profileListItem{kind: "legacy", title: "Advanced legacy setup paths", description: "Open key generation, one-off hardening, network, proxy, or doctor modes."},
	)
	return items
}

func newStackFilePicker(directory string, allowedTypes []string, showHidden bool) filepicker.Model {
	picker := filepicker.New()
	picker.CurrentDirectory = firstNonEmpty(directory, ".")
	picker.AllowedTypes = allowedTypes
	picker.ShowHidden = showHidden
	picker.ShowPermissions = false
	picker.ShowSize = true
	picker.SetHeight(12)
	picker.Styles.Cursor = setupTitleStyle
	picker.Styles.Selected = setupTitleStyle
	return picker
}

const stackFilePickerMaxEntries = 4096

func stackFilePickerDirectoryError(directory string) string {
	if !terminalSingleLineSafe(directory) {
		return "File browser paused because the current directory path contains terminal control characters. Press / to type a safe path or go back."
	}
	file, err := os.Open(directory)
	if err != nil {
		return "File browser unavailable: " + sanitizeTerminalLine(err.Error())
	}
	defer file.Close()
	return inspectStackFilePickerDirectory(file, directory)
}

func inspectStackFilePickerDirectory(file *os.File, directory string) string {
	entryCount := 0
	for {
		entries, readErr := file.ReadDir(128)
		entryCount += len(entries)
		if entryCount > stackFilePickerMaxEntries {
			return fmt.Sprintf("File browser paused because this directory contains more than %d entries. Press / to type a path.", stackFilePickerMaxEntries)
		}
		if warning := stackFilePickerEntriesError(directory, entries); warning != "" {
			return warning
		}
		if errors.Is(readErr, io.EOF) {
			return ""
		}
		if readErr != nil {
			return "File browser unavailable: " + sanitizeTerminalLine(readErr.Error())
		}
	}
}

func stackFilePickerEntriesError(directory string, entries []os.DirEntry) string {
	for _, entry := range entries {
		if !terminalSingleLineSafe(entry.Name()) {
			return "File browser paused because this directory contains a filename with terminal control characters. Press / to type a safe path or go back."
		}
		if entry.Type()&os.ModeSymlink == 0 {
			continue
		}
		target, err := filepath.EvalSymlinks(filepath.Join(directory, entry.Name()))
		if err == nil && !terminalSingleLineSafe(target) {
			return "File browser paused because this directory contains a symlink target with terminal control characters. Press / to type a safe path or go back."
		}
	}
	return ""
}

func terminalSingleLineSafe(value string) bool {
	return sanitizeTerminalText(value) == value && !strings.ContainsAny(value, "\r\n\t")
}

func stackFilePickerBackKey(key tea.KeyMsg) bool {
	switch key.String() {
	case "h", "backspace", "left":
		return true
	default:
		return false
	}
}

func resetStackFilePickerToParent(picker filepicker.Model) (filepicker.Model, tea.Cmd, string) {
	parent := filepath.Dir(picker.CurrentDirectory)
	next := newStackFilePicker(parent, picker.AllowedTypes, picker.ShowHidden)
	browserError := stackFilePickerDirectoryError(parent)
	if browserError != "" {
		return next, nil, browserError
	}
	return next, next.Init(), ""
}

func setupProfileInputs(options setupCLIOptions) []textinput.Model {
	return newSetupInputs([]setupInputField{
		{label: "Server IP or hostname", placeholder: "203.0.113.10", value: options.IP},
		{label: setupPrivateKeyLabel, placeholder: defaultKeygenConfig().Path, value: firstNonEmpty(options.PrivateKeyPath, defaultKeygenConfig().Path)},
		{label: setupBaseDomainLabel, placeholder: setupExampleDomainPlaceholder, value: options.BaseDomain},
		{label: setupLetsEncryptEmailLabel, placeholder: setupAdminEmailPlaceholder, value: options.LetsEncryptEmail},
	})
}

func setupAdvancedInputs(options setupCLIOptions) []textinput.Model {
	return newSetupInputs([]setupInputField{
		{label: "Profile name", placeholder: "production-vps", value: options.Name},
		{label: "Initial SSH user", value: firstNonEmpty(options.InitialSSHUser, "root")},
		{label: "Admin SSH user", value: firstNonEmpty(options.AdminUser, "servestead")},
		{label: "Pangolin admin email", placeholder: "defaults to Let's Encrypt email", value: options.PangolinAdminEmail},
		{label: "Pangolin admin password", placeholder: "generated for fresh installs", value: options.PangolinAdminPassword, secret: true},
	})
}

func setupRepositoryInputs(options setupCLIOptions) []textinput.Model {
	return newSetupInputs([]setupInputField{
		{label: "Local checkout path", placeholder: "/path/to/servestead-config", value: options.ConfigRepositoryPath},
		{label: "GitHub HTTPS URL", placeholder: "https://github.com/owner/repository.git", value: options.GitHubRepositoryURL},
	})
}

func newProfileStageTable(state *ProfileState, secrets ...ProfileSecrets) table.Model {
	return table.New(
		table.WithColumns([]table.Column{
			{Title: "Stage", Width: 16},
			{Title: "Status", Width: 12},
			{Title: "Last error", Width: 42},
		}),
		table.WithRows(profileStageRows(state, secrets...)),
		table.WithHeight(len(dashboardStageOrder)+1),
		table.WithWidth(78),
		table.WithFocused(true),
	)
}

func newRunHistoryTable(runs []SetupRun) table.Model {
	rows := make([]table.Row, 0, len(runs))
	for _, run := range runs {
		rows = append(rows, table.Row{
			sanitizeTerminalLine(run.ID),
			sanitizeTerminalLine(firstNonEmpty(run.Status, runStatusPlanned)),
			run.UpdatedAt.Local().Format("2006-01-02 15:04"),
			sanitizeTerminalLine(profileRunStageSummary(run)),
		})
	}
	return table.New(
		table.WithColumns([]table.Column{
			{Title: "Run", Width: 28},
			{Title: "Status", Width: 10},
			{Title: "Updated", Width: 16},
			{Title: "Stages", Width: 24},
		}),
		table.WithRows(rows),
		table.WithHeight(clampInt(len(rows)+1, 2, 14)),
		table.WithWidth(82),
		table.WithFocused(true),
	)
}

func profileStageRows(state *ProfileState, secrets ...ProfileSecrets) []table.Row {
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
		status, lastError := profileStageRowStatus(stage, completed, activeStages)
		if len(secrets) > 0 {
			lastError = maskProfileSecrets(lastError, secrets[0])
		}
		rows = append(rows, table.Row{labels[stage], status, truncateForTable(lastError, 42)})
	}
	return rows
}

func profileStageRowStatus(stage string, completed map[string]bool, activeStages map[string]SetupStageStatus) (string, string) {
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
			return stageStatusFailed, stageState.LastError
		}
		if stageState.Status == stageStatusCancelled {
			return stageStatusCancelled, ""
		}
		if stageState.Status == stageStatusRunning {
			status = stageStatusRunning
			lastError = stageState.LastError
		}
	}
	return status, lastError
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
		status := standaloneStackStatus(stack.Name, state)
		resource := "(private)"
		if stack.MetadataMissing {
			status = "draft"
			resource = "needs review; press enter"
		} else if len(stack.Metadata.PublicResources) == 1 {
			public := stack.Metadata.PublicResources[0]
			host := public.Subdomain
			if baseDomain != "" {
				host += "." + baseDomain
			}
			resource = fmt.Sprintf("%s → %s:%d", host, public.Service, public.Port)
		} else if len(stack.Metadata.PublicResources) > 1 {
			resource = fmt.Sprintf("%d public resources", len(stack.Metadata.PublicResources))
		}
		rows = append(rows, table.Row{sanitizeTerminalLine(stack.Name), sanitizeTerminalLine(status), sanitizeTerminalLine(resource)})
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
	stage := setupStageStackPrefix + name
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

func firstStackMissingMetadata(stacks []editableStack) (editableStack, bool) {
	for _, stack := range stacks {
		if stack.MetadataMissing {
			return stack, true
		}
	}
	return editableStack{}, false
}

func stackNeedsMetadataMessage(name string) string {
	return fmt.Sprintf("stack %s needs review; press enter, then ctrl+s to create %s", name, stackMetadataFilename)
}

func truncateForTable(value string, width int) string {
	value = sanitizeTerminalLine(value)
	if width <= 0 {
		return ""
	}
	tail := "."
	if width == 1 {
		tail = ""
	}
	return ansi.Truncate(value, width, tail)
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
		return model.updateWindowSize(msg)
	case pangolinRegistrationStatusMsg:
		return model.updatePangolinRegistrationStatus(msg)
	case profileCloudActionMsg:
		return model.applyProfileCloudAction(msg), nil
	case profileCloudSaveMsg:
		return model.applyProfileCloudSave(msg), nil
	case profileDeletedMsg:
		return model.applyProfileDeleted(msg), nil
	case profileStackOperationMsg:
		return model.applyProfileStackOperation(msg)
	case profileRunDetailLoadedMsg:
		return model.applyProfileRunDetailLoaded(msg), nil
	case spinner.TickMsg:
		return model.updateProfileStackSpinner(msg)
	case tea.KeyMsg:
		return model.updateProfileSetupKey(msg)
	default:
		return model.updateProfileSetupMessage(msg)
	}
}

func (model profileSetupModel) updateWindowSize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	model.width = msg.Width
	model.height = msg.Height
	contentWidth := max(20, msg.Width-4)
	navigationHeight := max(4, msg.Height-7)
	model.profileList.SetSize(contentWidth, navigationHeight)
	model.repositoryList.SetSize(contentWidth, navigationHeight)
	model.planViewport.SetWidth(contentWidth)
	model.planViewport.SetHeight(max(3, msg.Height-14))
	model.runLogViewport.SetWidth(contentWidth)
	model.runLogViewport.SetHeight(max(3, msg.Height-13))
	model.stackDiffViewport.SetWidth(contentWidth)
	model.stackDiffViewport.SetHeight(max(3, msg.Height-10))
	model.progress.SetWidth(clampInt(msg.Width-8, 12, 64))
	model.help.SetWidth(contentWidth)
	fieldWidth := max(10, contentWidth-34)
	for _, inputs := range [][]textinput.Model{
		model.inputs,
		model.advanced,
		model.repositoryInputs,
		model.stackInputs,
		model.stackResourceInputs,
	} {
		for index := range inputs {
			inputs[index].SetWidth(fieldWidth)
		}
	}
	standaloneWidth := max(10, contentWidth-28)
	for _, input := range []*textinput.Model{
		&model.githubTokenInput,
		&model.deleteProfileInput,
		&model.stackComposeInput,
		&model.stackEnvironmentInput,
		&model.stackCommitInput,
		&model.stackDeleteInput,
		&model.cloudTokenInput,
		&model.cloudConfirmInput,
	} {
		input.SetWidth(standaloneWidth)
	}
	model.stackComposePicker.SetHeight(max(4, msg.Height-13))
	model.stackEnvironmentPicker.SetHeight(max(4, msg.Height-13))
	model.resizeStageTable()
	model.resizeRunHistoryTable()
	model.resizeStackTable()
	model.resizeStackServiceTable()
	if model.screen == profileSetupScreenDashboard {
		model.syncDashboardViewport()
	}
	return model, nil
}

func (model profileSetupModel) updatePangolinRegistrationStatus(msg pangolinRegistrationStatusMsg) (tea.Model, tea.Cmd) {
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
		return model, nil
	}
	profile := model.profiles[model.selectedIndex].Profile
	if msg.requestID != model.pangolinRequestID || msg.profileID != profile.ID ||
		msg.baseDomain != model.pangolinRequestDomain || msg.baseDomain != profile.BaseDomain {
		return model, nil
	}
	if msg.err != nil {
		model.pangolinStatus = pangolinRegistrationUnavailable
		model.pangolinError = concisePangolinRegistrationError(msg.err)
		return model, nil
	}
	model.pangolinError = ""
	if msg.complete {
		model.pangolinStatus = pangolinRegistrationComplete
		if !completedSetupStages(model.profiles[model.selectedIndex].State)["proxy"] {
			model.advanced[4].SetValue("")
		}
		return model, nil
	}
	model.pangolinStatus = pangolinRegistrationIncomplete
	return model, nil
}

func (model profileSetupModel) updateProfileSetupKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if model.profileStackOperationBusy() {
		return model.updateProfileStackBusyKey(msg)
	}
	if updated, command, handled := model.updateProfileSetupGlobalKey(msg); handled {
		return updated, command
	}
	if model.profileSetupTerminalTooSmall() {
		return model, nil
	}
	return model.updateProfileSetupScreenKey(msg)
}

func (model profileSetupModel) profileSetupTerminalTooSmall() bool {
	return model.width > 0 && (model.width < profileSetupMinWidth || model.height < profileSetupMinHeight)
}

func (model profileSetupModel) updateProfileSetupGlobalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if model.screen == profileSetupScreenDeleteConfirm && model.deleteProfilePending {
		return model, nil, true
	}
	if model.screen == profileSetupScreenCloudSaveRecovery {
		switch msg.String() {
		case setupKeyCtrlC, "q", "esc":
			model.err = "The Droplet was destroyed remotely. Finish saving the local profile state before exiting."
			return model, nil, true
		}
	}
	switch msg.String() {
	case setupKeyCtrlC:
		model.cancelProfileRunDetailLoad()
		if model.screen == profileSetupScreenCloudRunning {
			return model.cancelProfileCloudAction()
		}
		model.cancelled = true
		return model, tea.Quit, true
	case "q":
		if model.screen == profileSetupScreenCloudRunning {
			return model.cancelProfileCloudAction()
		}
		if !profileSetupScreenAcceptsText(model.screen) {
			model.cancelProfileRunDetailLoad()
			model.quit = true
			return model, tea.Quit, true
		}
	case "?":
		if !profileSetupScreenAcceptsText(model.screen) {
			model.help.ShowAll = !model.help.ShowAll
			return model, nil, true
		}
	case "esc":
		if model.screen == profileSetupScreenCloudRunning {
			return model.cancelProfileCloudAction()
		}
		model.goBack()
		model.err = ""
		return model, nil, true
	}
	return model, nil, false
}

func (model profileSetupModel) updateProfileSetupScreenKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch model.screen {
	case profileSetupScreenPicker:
		return model.updateProfilePicker(msg)
	case profileSetupScreenDashboard:
		return model.updateProfileDashboard(msg)
	case profileSetupScreenRunHistory:
		return model.updateRunHistory(msg)
	case profileSetupScreenRunDetail:
		return model.updateRunDetail(msg)
	case profileSetupScreenIntake:
		return model.updateProfileInput(msg, false)
	case profileSetupScreenAdvanced:
		return model.updateProfileInput(msg, true)
	case profileSetupScreenRepository:
		return model.updateRepositoryChoice(msg)
	case profileSetupScreenRepositoryDetails:
		return model.updateRepositoryDetails(msg)
	case profileSetupScreenGitHubToken:
		return model.updateGitHubToken(msg)
	case profileSetupScreenStacks:
		return model.updateStacks(msg)
	case profileSetupScreenStackCompose:
		return model.updateStackCompose(msg)
	case profileSetupScreenStackServices:
		return model.updateStackServices(msg)
	case profileSetupScreenStackEditor:
		return model.updateStackEditor(msg)
	case profileSetupScreenStackResourceEditor:
		return model.updateStackResourceEditor(msg)
	case profileSetupScreenStackEnvironment:
		return model.updateStackEnvironment(msg)
	case profileSetupScreenStackReview:
		return model.updateStackReview(msg)
	case profileSetupScreenStackDeleteConfirm:
		return model.updateStackDeleteConfirm(msg)
	case profileSetupScreenStackDiff:
		return model.updateStackDiff(msg)
	case profileSetupScreenStackCommit:
		return model.updateStackCommit(msg)
	case profileSetupScreenCloud:
		return model.updateProfileCloud(msg)
	case profileSetupScreenCloudConfirm:
		return model.updateProfileCloudConfirm(msg)
	case profileSetupScreenCloudRunning:
		return model, nil
	case profileSetupScreenCloudSaveRecovery:
		return model.updateProfileCloudSaveRecovery(msg)
	case profileSetupScreenReview:
		return model.updateProfileReview(msg)
	case profileSetupScreenDeleteConfirm:
		return model.updateProfileDeleteConfirm(msg)
	default:
		return model, nil
	}
}

func (model profileSetupModel) updateProfileSetupMessage(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch model.screen {
	case profileSetupScreenStackCompose:
		return model.updateStackCompose(msg)
	case profileSetupScreenStackEnvironment:
		return model.updateStackEnvironment(msg)
	default:
		return model, nil
	}
}

func profileSetupScreenAcceptsText(screen profileSetupScreen) bool {
	switch screen {
	case profileSetupScreenIntake, profileSetupScreenAdvanced, profileSetupScreenRepositoryDetails,
		profileSetupScreenGitHubToken, profileSetupScreenStackCompose, profileSetupScreenStackEditor, profileSetupScreenStackResourceEditor,
		profileSetupScreenStackEnvironment, profileSetupScreenStackReview, profileSetupScreenStackCommit,
		profileSetupScreenStackDeleteConfirm, profileSetupScreenCloudConfirm, profileSetupScreenDeleteConfirm:
		return true
	default:
		return false
	}
}

func (model *profileSetupModel) resizeStackTable() {
	contentWidth := max(32, model.width-4)
	stackWidth := clampInt(contentWidth/4, 10, 18)
	statusWidth := clampInt(contentWidth/6, 8, 10)
	resourceWidth := max(10, contentWidth-stackWidth-statusWidth-4)
	model.stackTable.SetColumns([]table.Column{
		{Title: "Stack", Width: stackWidth},
		{Title: "Status", Width: statusWidth},
		{Title: "Public resource", Width: resourceWidth},
	})
	model.stackTable.SetWidth(contentWidth)
	availableHeight := model.height - 12
	if model.screen == profileSetupScreenDashboard {
		availableHeight = model.height - 23
	}
	model.stackTable.SetHeight(clampInt(len(model.stacks)+1, 2, max(2, availableHeight)))
}

func (model *profileSetupModel) resizeStageTable() {
	contentWidth := max(32, model.width-4)
	stageWidth := clampInt(contentWidth/5, 10, 16)
	statusWidth := clampInt(contentWidth/6, 8, 12)
	errorWidth := max(10, contentWidth-stageWidth-statusWidth-4)
	model.stageTable.SetColumns([]table.Column{
		{Title: "Stage", Width: stageWidth},
		{Title: "Status", Width: statusWidth},
		{Title: "Last error", Width: errorWidth},
	})
	model.stageTable.SetWidth(contentWidth)
}

func (model *profileSetupModel) resizeRunHistoryTable() {
	contentWidth := max(48, model.width-4)
	runWidth := clampInt(contentWidth/3, 16, 28)
	statusWidth := clampInt(contentWidth/7, 8, 10)
	updatedWidth := clampInt(contentWidth/5, 11, 16)
	stagesWidth := max(10, contentWidth-runWidth-statusWidth-updatedWidth-6)
	model.runTable.SetColumns([]table.Column{
		{Title: "Run", Width: runWidth},
		{Title: "Status", Width: statusWidth},
		{Title: "Updated", Width: updatedWidth},
		{Title: "Stages", Width: stagesWidth},
	})
	model.runTable.SetWidth(contentWidth)
	model.runTable.SetHeight(clampInt(len(model.runs)+1, 2, max(2, model.height-11)))
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
		case "provision":
			model.provision = true
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
			model.prepareDashboard()
			model.screen = profileSetupScreenDashboard
			pangolinCommand := model.checkPangolinRegistration()
			model, stackCommand := model.startProfileStackRefreshIfConfigured()
			return model, tea.Batch(pangolinCommand, stackCommand)
		}
	}
	var cmd tea.Cmd
	model.profileList, cmd = model.profileList.Update(key)
	return model, cmd
}

func (model profileSetupModel) updateProfileDashboard(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "pgup", "pgdown", "home", "end":
		model.syncDashboardViewport()
		var command tea.Cmd
		model.dashboardViewport, command = model.dashboardViewport.Update(key)
		return model, command
	case "h", "H":
		return model.openRunHistory(), nil
	case "v", "V":
		model.err = ""
		model.refreshPlanPreview()
		model.screen = profileSetupScreenReview
	case "r", "R":
		return model.runSelectedDashboardStage()
	case "e", "E":
		model.err = ""
		model.profileNotice = ""
		model.screen = profileSetupScreenIntake
	case "a", "A":
		model.err = ""
		model.profileNotice = ""
		model.screen = profileSetupScreenAdvanced
	case "s", "S":
		return model.openStacksScreen()
	case "g", "G":
		model.err = ""
		model.githubTokenNotice = ""
		model.githubTokenInput.SetValue("")
		model.githubTokenInput.Focus()
		model.screen = profileSetupScreenGitHubToken
		return model, textinput.Blink
	case "f", "F":
		model.fresh = true
		model.setInputsFromChoice(true)
		model.screen = profileSetupScreenIntake
	case "x", "X":
		model.err = ""
		model.deleteProfileInput.SetValue("")
		model.deleteProfileInput.Focus()
		model.screen = profileSetupScreenDeleteConfirm
		return model, textinput.Blink
	case "p", "P":
		if model.selectedProfileHasPangolinAccess() {
			model.showPangolinAccess = !model.showPangolinAccess
		}
	case "c", "C":
		return model, model.checkPangolinRegistration()
	case "o", "O":
		return model.openProfileCloudScreen()
	default:
		var cmd tea.Cmd
		model.stageTable, cmd = model.stageTable.Update(key)
		return model, cmd
	}
	return model, nil
}

func profileStateHasActiveRun(state ProfileState) bool {
	run, ok := state.Runs[state.ActiveRunID]
	return ok && run.Status == runStatusRunning
}

func (model profileSetupModel) openRunHistory() profileSetupModel {
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
		model.err = setupNoProfileSelectedMessage
		return model
	}
	model.err = ""
	model.selectedRunID = ""
	model.runLogPath = ""
	model.cancelProfileRunDetailLoad()
	model.runs = sortedSetupRuns(model.profiles[model.selectedIndex].State)
	model.runTable = newRunHistoryTable(model.runs)
	model.resizeRunHistoryTable()
	model.runTable.Focus()
	model.screen = profileSetupScreenRunHistory
	return model
}

func (model profileSetupModel) updateRunHistory(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "enter" {
		if model.runDetailLoading {
			return model, nil
		}
		if len(model.runs) == 0 {
			return model, nil
		}
		cursor := model.runTable.Cursor()
		if cursor < 0 || cursor >= len(model.runs) {
			model.err = "no setup run selected"
			return model, nil
		}
		if model.profileStore == nil {
			model.err = setupProfileStoreUnavailable
			return model, nil
		}
		choice := model.profiles[model.selectedIndex]
		run := model.runs[cursor]
		model.err = ""
		model.selectedRunID = run.ID
		model.runDetailLoading = true
		model.runDetailRequestID++
		requestID := model.runDetailRequestID
		parentCtx := model.tuiContext
		if parentCtx == nil {
			parentCtx = context.Background()
		}
		loadCtx, cancel := context.WithCancel(parentCtx)
		model.runDetailCancel = cancel
		store := model.profileStore
		profileID := choice.Profile.ID
		secrets := choice.Secrets
		return model, func() tea.Msg {
			detail, err := loadProfileRunDetailContext(loadCtx, store, profileID, run.ID, secrets)
			return profileRunDetailLoadedMsg{requestID: requestID, profileID: profileID, runID: run.ID, detail: detail, err: err}
		}
	}
	var command tea.Cmd
	model.runTable, command = model.runTable.Update(key)
	return model, command
}

type profileRunDetailLoadedMsg struct {
	requestID uint64
	profileID string
	runID     string
	detail    profileRunDetail
	err       error
}

func (model profileSetupModel) applyProfileRunDetailLoaded(message profileRunDetailLoadedMsg) profileSetupModel {
	if !model.runDetailLoading || message.requestID != model.runDetailRequestID || model.screen != profileSetupScreenRunHistory || message.runID != model.selectedRunID {
		return model
	}
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) || model.profiles[model.selectedIndex].Profile.ID != message.profileID {
		return model
	}
	model.finishProfileRunDetailLoad()
	if message.err != nil {
		if !errors.Is(message.err, context.Canceled) {
			model.err = message.err.Error()
		}
		return model
	}
	model.err = ""
	model.runLogPath = message.detail.Path
	lines := append([]string(nil), message.detail.Lines...)
	if message.detail.Truncated {
		lines = append([]string{fmt.Sprintf("Showing the latest %d saved events; earlier or oversized log data was omitted.", profileRunHistoryMaxEvents)}, lines...)
	}
	if len(lines) == 0 {
		lines = []string{"No log events were recorded for this run."}
	}
	model.runLogViewport.SetContent(strings.Join(lines, "\n"))
	model.runLogViewport.GotoTop()
	model.screen = profileSetupScreenRunDetail
	return model
}

func (model *profileSetupModel) cancelProfileRunDetailLoad() {
	if model.runDetailCancel != nil {
		model.runDetailCancel()
	}
	model.finishProfileRunDetailLoad()
	model.runDetailRequestID++
}

func (model *profileSetupModel) finishProfileRunDetailLoad() {
	if model.runDetailCancel != nil {
		model.runDetailCancel()
	}
	model.runDetailCancel = nil
	model.runDetailLoading = false
}

func (model profileSetupModel) updateRunDetail(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "home", "g":
		model.runLogViewport.GotoTop()
		return model, nil
	case "end", "G":
		model.runLogViewport.GotoBottom()
		return model, nil
	}
	var command tea.Cmd
	model.runLogViewport, command = model.runLogViewport.Update(key)
	return model, command
}

func (model profileSetupModel) runSelectedDashboardStage() (tea.Model, tea.Cmd) {
	stage, err := model.selectedDashboardStage()
	if err != nil {
		model.err = err.Error()
		return model, nil
	}
	model.singleStage = stage
	if stage == "platform" && model.selectedProfileNeedsPlatformIntake() {
		return model.openProfileStageIntake(stage), nil
	}
	if stage == "platform" && (model.pangolinStatus == pangolinRegistrationComplete || model.selectedProfileProxyFailed()) {
		return model.openPangolinCredentialRetry(stage, ""), nil
	}
	model.done = true
	return model, tea.Quit
}

func (model profileSetupModel) selectedProfileProxyFailed() bool {
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
		return false
	}
	choice := model.profiles[model.selectedIndex]
	run, ok := choice.State.Runs[choice.State.ActiveRunID]
	return ok && run.Stages["proxy"].Status == stageStatusFailed
}

func (model profileSetupModel) openStacksScreen() (tea.Model, tea.Cmd) {
	model.err = ""
	model.screen = profileSetupScreenStacks
	model.stackTable.Focus()
	return model.startProfileStackOperation(profileStackOperationRefresh, "", false)
}

func (model profileSetupModel) openProfileCloudScreen() (tea.Model, tea.Cmd) {
	if !model.selectedProfileHasCloud() {
		model.err = "selected profile has no DigitalOcean Droplet metadata"
		return model, nil
	}
	model.err = ""
	model.cloudNotice = ""
	model.screen = profileSetupScreenCloud
	return model, nil
}

func (model profileSetupModel) selectedProfileNeedsPlatformIntake() bool {
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
		return false
	}
	profile := model.profiles[model.selectedIndex].Profile
	return strings.TrimSpace(profile.PrivateKeyPath) == "" ||
		strings.TrimSpace(profile.BaseDomain) == "" ||
		strings.TrimSpace(profile.LetsEncryptEmail) == ""
}

func (model profileSetupModel) openProfileStageIntake(stage string) profileSetupModel {
	model.singleStage = stage
	model.err = "Platform needs a domain and Let's Encrypt email before it can configure proxy and observability."
	model.blurInputs(false)
	model.focus = 2
	if strings.TrimSpace(model.inputs[1].Value()) == "" {
		model.focus = 1
	} else if strings.TrimSpace(model.inputs[2].Value()) != "" && strings.TrimSpace(model.inputs[3].Value()) == "" {
		model.focus = 3
	}
	model.inputs[model.focus].Focus()
	model.screen = profileSetupScreenIntake
	return model
}

func (model profileSetupModel) openPangolinCredentialRetry(stage, notice string) profileSetupModel {
	model.singleStage = stage
	model.err = notice
	model.focus = 3
	for index := range model.advanced {
		model.advanced[index].Blur()
	}
	model.advanced[model.focus].Focus()
	model.screen = profileSetupScreenAdvanced
	return model
}

func (model profileSetupModel) selectedProfileStageFailed(stage string) bool {
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
		return false
	}
	choice := model.profiles[model.selectedIndex]
	run, ok := choice.State.Runs[choice.State.ActiveRunID]
	if !ok {
		return false
	}
	status, ok := run.Stages[stage]
	return ok && status.Status == stageStatusFailed
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
	case setupKeyShiftTab, "up":
		inputs[model.focus].Blur()
		model.focus--
		if model.focus < 0 {
			model.focus = len(inputs) - 1
		}
		inputs[model.focus].Focus()
		model.storeFocusedInputs(inputs, advanced)
		return model, nil
	case setupKeyCtrlA:
		if !advanced {
			model.blurInputs(false)
			model.focus = 0
			model.advanced[0].Focus()
			model.screen = profileSetupScreenAdvanced
			return model, nil
		}
	case setupKeyCtrlE:
		if advanced {
			model.blurInputs(true)
			model.focus = 0
			model.inputs[0].Focus()
			model.screen = profileSetupScreenIntake
			return model, nil
		}
	case setupKeyCtrlS:
		model.storeFocusedInputs(inputs, advanced)
		return model.saveSelectedProfileSettings()
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
	inputs[model.focus], cmd = updateSetupTextInput(inputs[model.focus], key)
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
		return model.moveRepositoryDetailFocus(indexes, 1), nil
	case setupKeyShiftTab, "up":
		return model.moveRepositoryDetailFocus(indexes, -1), nil
	case "enter":
		return model.saveRepositoryDetails()
	}
	var cmd tea.Cmd
	model.repositoryInputs[model.focus], cmd = updateSetupTextInput(model.repositoryInputs[model.focus], key)
	return model, cmd
}

func (model profileSetupModel) updateGitHubToken(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "enter":
		token, err := normalizeGitHubToken(model.githubTokenInput.Value())
		if err != nil {
			model.err = err.Error()
			return model, nil
		}
		return model.saveSelectedGitHubToken(token), nil
	case setupKeyCtrlE:
		token, err := normalizeGitHubToken(os.Getenv("SERVESTEAD_GITHUB_TOKEN"))
		if err != nil {
			model.err = "SERVESTEAD_GITHUB_TOKEN: " + err.Error()
			return model, nil
		}
		model = model.saveSelectedGitHubToken(token)
		if model.err == "" {
			model.githubTokenNotice = "Stored SERVESTEAD_GITHUB_TOKEN in the selected profile."
		}
		return model, nil
	case setupKeyCtrlX:
		return model.removeSelectedGitHubToken(), nil
	}
	var cmd tea.Cmd
	model.githubTokenInput, cmd = updateSetupTextInput(model.githubTokenInput, key)
	return model, cmd
}

func (model profileSetupModel) saveSelectedGitHubToken(token string) profileSetupModel {
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
		model.err = setupNoProfileSelectedMessage
		return model
	}
	if model.profileStore == nil {
		model.err = setupProfileStoreUnavailable
		return model
	}
	profileID := model.profiles[model.selectedIndex].Profile.ID
	choice, err := saveProfileGitHubToken(model.profileStore, profileID, token)
	if err != nil {
		model.err = err.Error()
		return model
	}
	model.profiles[model.selectedIndex] = choice
	model.githubTokenInput.SetValue("")
	model.githubTokenNotice = "Stored GitHub token in the selected profile."
	model.err = ""
	return model
}

func (model profileSetupModel) removeSelectedGitHubToken() profileSetupModel {
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
		model.err = setupNoProfileSelectedMessage
		return model
	}
	if model.profileStore == nil {
		model.err = setupProfileStoreUnavailable
		return model
	}
	profileID := model.profiles[model.selectedIndex].Profile.ID
	choice, err := saveProfileGitHubToken(model.profileStore, profileID, "")
	if err != nil {
		model.err = err.Error()
		return model
	}
	model.profiles[model.selectedIndex] = choice
	model.githubTokenInput.SetValue("")
	model.githubTokenNotice = "Removed stored GitHub token from the selected profile."
	model.err = ""
	return model
}

func (model profileSetupModel) saveSelectedProfileSettings() (tea.Model, tea.Cmd) {
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
		model.err = setupNoProfileSelectedMessage
		return model, nil
	}
	if model.profileStore == nil {
		model.err = setupProfileStoreUnavailable
		return model, nil
	}
	options, err := model.optionsForSelectedProfile()
	if err != nil {
		model.err = err.Error()
		return model, nil
	}
	if err := validateSavedProfileOptions(options); err != nil {
		model.err = err.Error()
		return model, nil
	}

	staleChoice := model.profiles[model.selectedIndex]
	patch := newProfileSettingsPatch(staleChoice.Profile, staleChoice.Secrets, options)
	choice, err := saveProfileSettings(model.profileStore, staleChoice.Profile.ID, patch)
	if err != nil {
		model.err = err.Error()
		return model, nil
	}
	model.profiles[model.selectedIndex] = choice

	model.err = ""
	model.profileNotice = "Saved profile settings."
	model.setInputsFromChoice(false)
	model.prepareDashboard()
	model.screen = profileSetupScreenDashboard
	return model.startProfileStackRefreshIfConfigured()
}

func validateSavedProfileOptions(options setupCLIOptions) error {
	if strings.TrimSpace(options.IP) == "" {
		return errors.New("server IP or hostname is required")
	}
	if options.PrivateKeyPath == "" {
		return errors.New("private key path is required")
	}
	if !linuxUsername.MatchString(firstNonEmpty(options.InitialSSHUser, "root")) || !linuxUsername.MatchString(firstNonEmpty(options.AdminUser, "servestead")) {
		return errors.New("SSH users must be valid Linux usernames")
	}
	if options.BaseDomain != "" && !domainName.MatchString(options.BaseDomain) {
		return errors.New("domain must be a valid base domain such as example.com")
	}
	if options.LetsEncryptEmail != "" && !setupEmailLike(options.LetsEncryptEmail) {
		return errors.New("Let's Encrypt email must be a valid email address")
	}
	if options.PangolinAdminEmail != "" && !setupEmailLike(options.PangolinAdminEmail) {
		return errors.New("Pangolin administrator email must be a valid email address")
	}
	return nil
}

func setupEmailLike(value string) bool {
	return !strings.ContainsAny(value, " \t\r\n") && strings.Contains(value, "@")
}

func (model profileSetupModel) moveRepositoryDetailFocus(indexes []int, direction int) profileSetupModel {
	model.repositoryInputs[model.focus].Blur()
	model.focus = nextVisibleInput(indexes, model.focus, direction)
	model.repositoryInputs[model.focus].Focus()
	return model
}

func (model profileSetupModel) saveRepositoryDetails() (tea.Model, tea.Cmd) {
	if err := model.validateRepositoryDetails(); err != nil {
		model.err = err.Error()
		return model, nil
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

func (model profileSetupModel) validateRepositoryDetails() error {
	switch model.repositoryMode {
	case "existing":
		return validateExistingRepositoryInput(model.repositoryInputs[0].Value())
	case "github":
		return validateGitHubRepositoryInput(model.repositoryInputs[1].Value())
	default:
		return nil
	}
}

func validateExistingRepositoryInput(value string) error {
	path := expandUserPath(strings.TrimSpace(value))
	if path == "" {
		return errors.New("existing repository path is required")
	}
	// The local interactive user deliberately selects this repository path, including paths outside the home directory.
	// codeql[go/path-injection]
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("no Git repository exists at that path; go back and choose create")
		}
		return err
	}
	return nil
}

func validateGitHubRepositoryInput(value string) error {
	repositoryURL := strings.TrimSpace(value)
	if repositoryURL == "" {
		return errors.New("GitHub repository URL is required")
	}
	return validateGitHubRepositoryURL(repositoryURL)
}

func (model profileSetupModel) updateStackCompose(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, isKey := msg.(tea.KeyMsg)
	if model.stackComposeManual {
		if isKey && key.String() == "enter" {
			return model.loadStackCompose(model.stackComposeInput.Value())
		}
		var command tea.Cmd
		model.stackComposeInput, command = updateSetupTextInput(model.stackComposeInput, msg)
		return model, command
	}
	if isKey && key.String() == "/" {
		model.stackComposeManual = true
		model.stackComposeInput.SetValue("")
		model.stackComposeInput.Focus()
		model.err = ""
		return model, textinput.Blink
	}
	if model.stackComposeBrowserError != "" {
		if isKey && stackFilePickerBackKey(key) {
			var command tea.Cmd
			model.stackComposePicker, command, model.stackComposeBrowserError = resetStackFilePickerToParent(model.stackComposePicker)
			model.err = model.stackComposeBrowserError
			return model, command
		}
		return model, nil
	}
	var command tea.Cmd
	model.stackComposePicker, command = model.stackComposePicker.Update(msg)
	model.stackComposeBrowserError = stackFilePickerDirectoryError(model.stackComposePicker.CurrentDirectory)
	if model.stackComposeBrowserError != "" {
		model.err = model.stackComposeBrowserError
		return model, nil
	}
	if selected, path := model.stackComposePicker.DidSelectFile(msg); selected {
		return model.loadStackCompose(path)
	}
	if disabled, path := model.stackComposePicker.DidSelectDisabledFile(msg); disabled {
		model.err = fmt.Sprintf("%s is not a YAML file", sanitizeTerminalLine(filepath.Base(path)))
	}
	return model, command
}

func (model profileSetupModel) loadStackCompose(selectedPath string) (tea.Model, tea.Cmd) {
	path := expandUserPath(strings.TrimSpace(selectedPath))
	if path == "" {
		model.err = "Docker Compose file path is required"
		return model, nil
	}
	compose, err := readBoundedFile(path, "Compose file", stackComposeMaxBytes)
	if err != nil {
		model.err = fmt.Sprintf("read Compose file: %v", err)
		return model, nil
	}
	services, err := inspectComposeServices(compose)
	if err != nil {
		model.err = err.Error()
		return model, nil
	}
	options := withStackAddDefaults(stackAddOptions{Compose: path}, services)
	model.stackComposePath = path
	model.stackCompose = string(compose)
	model.stackServices = services
	model.stackOriginalName = ""
	model.stackMetadataMissing = false
	model.stackResources = nil
	model.stackResourceTable = newStackResourceTable(nil)
	model.stackServiceTable = newStackServiceTable(services, nil)
	model.resizeStackServiceTable()
	model.stackEnvironment = ""
	model.stackEnvironmentOriginal = ""
	model.stackEnvironmentKeys = nil
	model.stackEnvironmentDirty = false
	model.stackInputs = stackEditorInputs(options)
	model.stackComposeInput.Blur()
	model.stackServiceTable.Focus()
	model.focus = 0
	model.err = ""
	model.screen = profileSetupScreenStackServices
	return model, nil
}

func (model profileSetupModel) updateStacks(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "a", "A":
		return model.openStackComposePicker()
	case "enter", "e", "E":
		return model.editSelectedStack()
	case "d", "D":
		return model.confirmSelectedStackDelete()
	case "r", "R":
		return model.runSelectedStack()
	case "v", "V":
		return model.openStackDiff()
	case "g", "G":
		return model.stageStackGitChanges()
	case "c", "C":
		return model.openStackCommit()
	case "y", "Y":
		return model.runStackSync()
	case "p", "P":
		return model.pushStackGitRepository()
	default:
		var command tea.Cmd
		model.stackTable, command = model.stackTable.Update(key)
		return model, command
	}
}

func (model profileSetupModel) openStackComposePicker() (tea.Model, tea.Cmd) {
	directory, err := os.Getwd()
	if err != nil {
		directory = "."
	}
	model.err = ""
	model.stackNotice = ""
	model.stackComposeManual = false
	model.stackComposeInput.SetValue("")
	model.stackComposeInput.Blur()
	model.stackComposePicker = newStackFilePicker(directory, []string{".yaml", ".yml"}, false)
	model.stackComposeBrowserError = stackFilePickerDirectoryError(directory)
	model.err = model.stackComposeBrowserError
	model.screen = profileSetupScreenStackCompose
	if model.stackComposeBrowserError != "" {
		return model, nil
	}
	return model, model.stackComposePicker.Init()
}

func (model profileSetupModel) editSelectedStack() (tea.Model, tea.Cmd) {
	stack, ok := model.selectedStack()
	if !ok {
		model.err = setupNoStackSelectedMessage + "; press a to add one"
		return model, nil
	}
	model.openStackEditor(stack)
	return model, nil
}

func (model profileSetupModel) confirmSelectedStackDelete() (tea.Model, tea.Cmd) {
	_, ok := model.selectedStack()
	if !ok {
		model.err = setupNoStackSelectedMessage
		return model, nil
	}
	model.err = ""
	model.stackDeleteInput.SetValue("")
	model.stackDeleteInput.Focus()
	model.screen = profileSetupScreenStackDeleteConfirm
	return model, textinput.Blink
}

func (model profileSetupModel) runSelectedStack() (tea.Model, tea.Cmd) {
	stack, ok := model.selectedStack()
	if !ok {
		model.err = setupNoStackSelectedMessage
		return model, nil
	}
	if err := model.validateStackReadyForRun(stack); err != nil {
		model.err = err.Error()
		return model, nil
	}
	stage := setupStageStackPrefix + stack.Name
	if model.selectedProfileStageFailed(stage) {
		return model.openPangolinCredentialRetry(stage, "Retrying a failed stack deployment: confirm the Pangolin admin email and password."), nil
	}
	model.singleStage = stage
	model.done = true
	return model, tea.Quit
}

func (model profileSetupModel) validateStackReadyForRun(stack editableStack) error {
	if stack.MetadataMissing {
		return errors.New(stackNeedsMetadataMessage(stack.Name))
	}
	if model.stackGitStatus != "clean" {
		return errors.New("stack changes are uncommitted; press v to review, g to stage, and c to commit")
	}
	if model.stackNeedsPush {
		return errors.New("the stack commit has not been pushed to origin; press p to push it")
	}
	return nil
}

func (model profileSetupModel) openStackDiff() (tea.Model, tea.Cmd) {
	return model.startProfileStackOperation(profileStackOperationDiff, "", false)
}

func (model profileSetupModel) stageStackGitChanges() (tea.Model, tea.Cmd) {
	return model.startProfileStackOperation(profileStackOperationStage, "", false)
}

func (model profileSetupModel) openStackCommit() (tea.Model, tea.Cmd) {
	if stack, ok := firstStackMissingMetadata(model.stacks); ok {
		model.err = stackNeedsMetadataMessage(stack.Name)
		return model, nil
	}
	model.stackCommitInput.SetValue("")
	model.stackCommitInput.Focus()
	model.err = ""
	model.screen = profileSetupScreenStackCommit
	return model, nil
}

func (model profileSetupModel) runStackSync() (tea.Model, tea.Cmd) {
	return model.startProfileStackOperation(profileStackOperationSync, "", false)
}

func (model profileSetupModel) finishStackSync() (tea.Model, tea.Cmd) {
	if stack, ok := firstStackMissingMetadata(model.stacks); ok {
		model.err = stackNeedsMetadataMessage(stack.Name)
		return model, nil
	}
	if model.stackGitStatus != "clean" {
		model.err = "stack changes are uncommitted; press v to review, g to stage, and c to commit"
		return model, nil
	}
	if model.stackNeedsPush {
		model.err = "the stack commit has not been pushed to origin"
		return model, nil
	}
	stage := "stacks"
	if model.selectedProfileStageFailed(stage) {
		return model.openPangolinCredentialRetry(stage, "Retrying a failed stack sync: confirm the Pangolin admin email and password."), nil
	}
	model.singleStage = stage
	model.done = true
	return model, tea.Quit
}

func (model profileSetupModel) pushStackGitRepository() (tea.Model, tea.Cmd) {
	if stack, ok := firstStackMissingMetadata(model.stacks); ok {
		model.err = stackNeedsMetadataMessage(stack.Name)
		return model, nil
	}
	return model.startProfileStackOperation(profileStackOperationPush, "", false)
}

func (model profileSetupModel) updateStackServices(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	serviceIndex := model.stackServiceTable.Cursor()
	if serviceIndex < 0 || serviceIndex >= len(model.stackServices) {
		model.err = "no Compose service selected"
		return model, nil
	}
	serviceName := model.stackServices[serviceIndex].Name
	switch key.String() {
	case " ", "space":
		if indexes := stackResourceIndexesForService(model.stackResources, serviceName); len(indexes) > 0 {
			model.stackResources = removeStackResourcesForService(model.stackResources, serviceName)
			model.refreshStackServiceTable()
			model.err = ""
			return model, nil
		}
		model.openStackResourceEditorForService(-1, serviceIndex, profileSetupScreenStackServices)
		return model, nil
	case "enter", "e", "E":
		indexes := stackResourceIndexesForService(model.stackResources, serviceName)
		if len(indexes) == 0 {
			model.openStackResourceEditorForService(-1, serviceIndex, profileSetupScreenStackServices)
		} else {
			model.openStackResourceEditorForService(indexes[0], serviceIndex, profileSetupScreenStackServices)
		}
		return model, nil
	case "a", "A":
		model.openStackResourceEditorForService(-1, serviceIndex, profileSetupScreenStackServices)
		return model, nil
	case "n", "N":
		model.openStackEnvironment(profileSetupScreenStackReview)
		return model, nil
	}
	var command tea.Cmd
	model.stackServiceTable, command = model.stackServiceTable.Update(key)
	return model, command
}

func (model profileSetupModel) updateStackEditor(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if profileSetupViewportKey(key) {
		model.syncStackEditorViewport()
		var command tea.Cmd
		model.stackEditorViewport, command = model.stackEditorViewport.Update(key)
		return model, command
	}
	if model.focus == 0 {
		switch key.String() {
		case setupKeyCtrlS:
			return model.saveStackEditor()
		case "tab", "down":
			model.stackInputs[0].Blur()
			model.focus = 1
			model.stackResourceTable.Focus()
			return model, nil
		}
		var command tea.Cmd
		model.stackInputs[0], command = updateSetupTextInput(model.stackInputs[0], key)
		return model, command
	}
	switch key.String() {
	case setupKeyCtrlS:
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
		model.openStackEnvironment(profileSetupScreenStackEditor)
		return model, nil
	}
	var command tea.Cmd
	model.stackResourceTable, command = model.stackResourceTable.Update(key)
	return model, command
}

func (model profileSetupModel) saveStackEditor() (tea.Model, tea.Cmd) {
	request, err := model.stackEditorSaveOperationRequest()
	if err != nil {
		return model.withStackEditorError(err), nil
	}
	return model.startProfileStackOperationRequest(request)
}

func (model profileSetupModel) withStackEditorError(err error) profileSetupModel {
	model.err = err.Error()
	return model
}

func (model *profileSetupModel) finishStackEditorSave(result profileStackSaveResult) {
	model.stackNotice = stackEditorSavedNotice(result.OriginalName, result.MetadataMissing, result.ScaffoldCreated)
	model.err = ""
	model.screen = profileSetupScreenStacks
	if model.stackEnvironmentDirty && model.stackGitStatus == "clean" {
		model.stackNotice = "Runtime secrets updated in Git-backed encrypted state. Review and commit the stack changes."
	}
	model.stackTable.Focus()
}

func stackEditorSavedNotice(originalName string, metadataMissing, scaffoldCreated bool) string {
	action := "updated"
	if originalName == "" || metadataMissing {
		action = "added"
	}
	if scaffoldCreated {
		return fmt.Sprintf("Stack %s and repository scaffold prepared. Review and commit them together.", action)
	}
	return fmt.Sprintf("Stack %s. Press v to review the diff, g to stage, then c to commit.", action)
}

func (model profileSetupModel) updateStackResourceEditor(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	visible := stackResourceVisibleInputs(model.stackResourceAdvanced)
	switch key.String() {
	case setupKeyCtrlS:
		return model.saveStackResourceEditor()
	case setupKeyCtrlX:
		model.stackResourceInputs[model.focus].Blur()
		model.stackResourceAdvanced = !model.stackResourceAdvanced
		visible = stackResourceVisibleInputs(model.stackResourceAdvanced)
		if !containsInt(visible, model.focus) {
			model.focus = visible[0]
		}
		model.stackResourceInputs[model.focus].Focus()
		return model, nil
	case "tab", "down":
		model.stackResourceInputs[model.focus].Blur()
		model.focus = nextVisibleInput(visible, model.focus, 1)
		model.stackResourceInputs[model.focus].Focus()
		return model, nil
	case setupKeyShiftTab, "up":
		model.stackResourceInputs[model.focus].Blur()
		model.focus = nextVisibleInput(visible, model.focus, -1)
		model.stackResourceInputs[model.focus].Focus()
		return model, nil
	case "enter":
		if model.focus != visible[len(visible)-1] {
			model.stackResourceInputs[model.focus].Blur()
			model.focus = nextVisibleInput(visible, model.focus, 1)
			model.stackResourceInputs[model.focus].Focus()
			return model, nil
		}
		return model.saveStackResourceEditor()
	}
	var command tea.Cmd
	model.stackResourceInputs[model.focus], command = updateSetupTextInput(model.stackResourceInputs[model.focus], key)
	return model, command
}

func stackResourceVisibleInputs(advanced bool) []int {
	visible := []int{1, 2, 3, 5, 7}
	if advanced {
		visible = append(visible, 0, 4, 6)
	}
	return visible
}

func nextVisibleInput(visible []int, current, direction int) int {
	for index, candidate := range visible {
		if candidate == current {
			return visible[(index+direction+len(visible))%len(visible)]
		}
	}
	return visible[0]
}

func containsInt(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
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
	model.screen = model.stackResourceReturn
	if model.screen == profileSetupScreenStackServices {
		model.refreshStackServiceTable()
	}
	return model, nil
}

func (model *profileSetupModel) openStackEnvironment(returnScreen profileSetupScreen) {
	model.stackEnvironmentReturn = returnScreen
	model.stackEnvironmentMode = stackEnvironmentChoose
	model.stackEnvironmentCursor = 0
	model.stackEnvironmentOptions = nil
	if model.stackEnvironment != "" || len(model.stackEnvironmentKeys) > 0 {
		model.stackEnvironmentOptions = append(model.stackEnvironmentOptions, stackEnvironmentOption{
			kind: "keep", label: "Keep current runtime secrets",
			detail: fmt.Sprintf("%d key(s)", len(model.stackEnvironmentKeys)),
		})
	}
	model.stackEnvironmentOptions = append(model.stackEnvironmentOptions, stackEnvironmentOption{
		kind: "none", label: "No runtime secrets",
	})
	if model.stackComposePath != "" {
		adjacent := filepath.Join(filepath.Dir(model.stackComposePath), ".env")
		if info, err := os.Stat(adjacent); err == nil && !info.IsDir() && terminalSingleLineSafe(adjacent) {
			model.stackEnvironmentOptions = append(model.stackEnvironmentOptions, stackEnvironmentOption{
				kind: "file", label: "Use adjacent .env", detail: adjacent, path: adjacent,
			})
		}
	}
	model.stackEnvironmentOptions = append(model.stackEnvironmentOptions, stackEnvironmentOption{
		kind: "browse", label: "Browse for another file",
	})
	model.stackEnvironmentInput.SetValue("")
	model.stackEnvironmentInput.Blur()
	model.err = ""
	model.screen = profileSetupScreenStackEnvironment
}

func (model profileSetupModel) updateStackEnvironment(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch model.stackEnvironmentMode {
	case stackEnvironmentChoose:
		return model.updateStackEnvironmentChoice(msg)
	case stackEnvironmentManual:
		return model.updateStackEnvironmentManual(msg)
	default:
		return model.updateStackEnvironmentBrowse(msg)
	}
}

func (model profileSetupModel) updateStackEnvironmentChoice(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return model, nil
	}
	switch key.String() {
	case "j", "down":
		if model.stackEnvironmentCursor < len(model.stackEnvironmentOptions)-1 {
			model.stackEnvironmentCursor++
		}
	case "k", "up":
		if model.stackEnvironmentCursor > 0 {
			model.stackEnvironmentCursor--
		}
	case "enter":
		return model.selectStackEnvironmentOption()
	}
	return model, nil
}

func (model profileSetupModel) selectStackEnvironmentOption() (tea.Model, tea.Cmd) {
	if model.stackEnvironmentCursor < 0 || model.stackEnvironmentCursor >= len(model.stackEnvironmentOptions) {
		model.err = "no runtime secret option selected"
		return model, nil
	}
	option := model.stackEnvironmentOptions[model.stackEnvironmentCursor]
	switch option.kind {
	case "keep":
		return model.finishStackEnvironment(model.stackEnvironment, model.stackEnvironmentKeys)
	case "none":
		return model.finishStackEnvironment("", nil)
	case "file":
		return model.loadStackEnvironment(option.path)
	case "browse":
		return model.openStackEnvironmentBrowser()
	default:
		return model, nil
	}
}

func (model profileSetupModel) openStackEnvironmentBrowser() (tea.Model, tea.Cmd) {
	directory := "."
	if model.stackComposePath != "" {
		directory = filepath.Dir(model.stackComposePath)
	} else if current, err := os.Getwd(); err == nil {
		directory = current
	}
	model.stackEnvironmentPicker = newStackFilePicker(directory, nil, true)
	model.stackEnvironmentBrowserError = stackFilePickerDirectoryError(directory)
	model.stackEnvironmentMode = stackEnvironmentBrowse
	model.err = model.stackEnvironmentBrowserError
	if model.stackEnvironmentBrowserError != "" {
		return model, nil
	}
	return model, model.stackEnvironmentPicker.Init()
}

func (model profileSetupModel) updateStackEnvironmentManual(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, isKey := msg.(tea.KeyMsg)
	if isKey && key.String() == "enter" {
		return model.loadStackEnvironment(model.stackEnvironmentInput.Value())
	}
	var command tea.Cmd
	model.stackEnvironmentInput, command = updateSetupTextInput(model.stackEnvironmentInput, msg)
	return model, command
}

func (model profileSetupModel) updateStackEnvironmentBrowse(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, isKey := msg.(tea.KeyMsg)
	if isKey && key.String() == "/" {
		model.stackEnvironmentMode = stackEnvironmentManual
		model.stackEnvironmentInput.SetValue("")
		model.stackEnvironmentInput.Focus()
		model.err = ""
		return model, textinput.Blink
	}
	if model.stackEnvironmentBrowserError != "" {
		if isKey && stackFilePickerBackKey(key) {
			var command tea.Cmd
			model.stackEnvironmentPicker, command, model.stackEnvironmentBrowserError = resetStackFilePickerToParent(model.stackEnvironmentPicker)
			model.err = model.stackEnvironmentBrowserError
			return model, command
		}
		return model, nil
	}
	var command tea.Cmd
	model.stackEnvironmentPicker, command = model.stackEnvironmentPicker.Update(msg)
	model.stackEnvironmentBrowserError = stackFilePickerDirectoryError(model.stackEnvironmentPicker.CurrentDirectory)
	if model.stackEnvironmentBrowserError != "" {
		model.err = model.stackEnvironmentBrowserError
		return model, nil
	}
	if selected, path := model.stackEnvironmentPicker.DidSelectFile(msg); selected {
		return model.loadStackEnvironment(path)
	}
	return model, command
}

func (model profileSetupModel) loadStackEnvironment(path string) (tea.Model, tea.Cmd) {
	path = strings.TrimSpace(path)
	if path == "" {
		model.err = "runtime secret file path is required"
		return model, nil
	}
	environment, keys, err := readStackEnvironmentFile(path)
	if err != nil {
		model.err = err.Error()
		return model, nil
	}
	return model.finishStackEnvironment(environment, keys)
}

func (model profileSetupModel) finishStackEnvironment(environment string, keys []string) (tea.Model, tea.Cmd) {
	model.stackEnvironment = environment
	model.stackEnvironmentDirty = environment != model.stackEnvironmentOriginal
	model.stackEnvironmentKeys = keys
	model.stackEnvironmentInput.Blur()
	model.err = ""
	model.screen = model.stackEnvironmentReturn
	if model.screen == profileSetupScreenStackEditor {
		model.focus = 1
		model.stackResourceTable.Focus()
	} else {
		model.focus = 0
		model.stackInputs[0].Focus()
	}
	return model, nil
}

func (model profileSetupModel) updateStackReview(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if profileSetupViewportKey(key) {
		model.syncStackReviewViewport()
		var command tea.Cmd
		model.stackReviewViewport, command = model.stackReviewViewport.Update(key)
		return model, command
	}
	if key.String() == "enter" {
		return model.saveStackEditor()
	}
	var command tea.Cmd
	model.stackInputs[0], command = updateSetupTextInput(model.stackInputs[0], key)
	return model, command
}

func (model profileSetupModel) updateStackDeleteConfirm(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "enter" {
		stack, ok := model.selectedStack()
		if !ok {
			model.err = setupNoStackSelectedMessage
			return model, nil
		}
		want := "delete " + safeStackConfirmationName(stack.Name)
		if strings.TrimSpace(model.stackDeleteInput.Value()) != want {
			model.err = fmt.Sprintf("type %q exactly to remove this stack", want)
			return model, nil
		}
		model.stackDeleteInput.Blur()
		return model.deleteSelectedStack()
	}
	var command tea.Cmd
	model.stackDeleteInput, command = updateSetupTextInput(model.stackDeleteInput, key)
	return model, command
}

func (model profileSetupModel) deleteSelectedStack() (tea.Model, tea.Cmd) {
	stack, ok := model.selectedStack()
	if !ok {
		return model.stackDeleteError(setupNoStackSelectedMessage), nil
	}
	request, err := model.profileStackRequest(profileStackOperationDelete, "", false)
	if err != nil {
		return model.stackDeleteError(err.Error()), nil
	}
	request.StackName = stack.Name
	return model.startProfileStackOperationRequest(request)
}

func (model profileSetupModel) stackDeleteError(message string) profileSetupModel {
	model.err = message
	model.screen = profileSetupScreenStackDeleteConfirm
	return model
}

func (model profileSetupModel) updateStackDiff(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	var command tea.Cmd
	model.stackDiffViewport, command = model.stackDiffViewport.Update(key)
	return model, command
}

func (model profileSetupModel) updateStackCommit(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "enter" {
		message := strings.TrimSpace(model.stackCommitInput.Value())
		return model.startProfileStackOperation(profileStackOperationCommit, message, false)
	}
	var command tea.Cmd
	model.stackCommitInput, command = updateSetupTextInput(model.stackCommitInput, key)
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
		{label: "Protocol (http/tcp/udp/ssh/rdp/vnc)", value: firstNonEmpty(resource.Protocol, "http")},
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
	model.stackMetadataMissing = stack.MetadataMissing
	repositoryPath, _ := model.selectedRepositoryPath()
	model.stackComposePath = filepath.Join(repositoryPath, "stacks", stack.Name, "compose.yaml")
	model.stackCompose = stack.Compose
	model.stackServices = stack.Services
	model.stackServiceTable = newStackServiceTable(model.stackServices, stack.Metadata.PublicResources)
	model.stackResources = append([]stackPublicResource(nil), stack.Metadata.PublicResources...)
	model.stackResourceTable = newStackResourceTable(model.stackResources)
	model.stackInputs = stackEditorInputs(options)
	model.stackEnvironment = ""
	model.stackEnvironmentOriginal = ""
	model.stackEnvironmentKeys = stack.Metadata.Secrets.KeyNames()
	model.stackEnvironmentDirty = false
	model.focus = 1
	model.stackInputs[0].Blur()
	model.stackResourceTable.Focus()
	model.err = ""
	model.screen = profileSetupScreenStackEditor
}

func (model *profileSetupModel) openStackResourceEditor(index int) {
	serviceIndex := 0
	if index >= 0 && index < len(model.stackResources) {
		for candidateIndex, service := range model.stackServices {
			if service.Name == model.stackResources[index].Service {
				serviceIndex = candidateIndex
				break
			}
		}
	}
	model.openStackResourceEditorForService(index, serviceIndex, profileSetupScreenStackEditor)
}

func (model *profileSetupModel) openStackResourceEditorForService(index, serviceIndex int, returnScreen profileSetupScreen) {
	resource := stackPublicResource{Protocol: "http", SSO: true, Healthcheck: stackResourceHealthcheck{Enabled: true, Path: "/"}}
	if index >= 0 && index < len(model.stackResources) {
		resource = model.stackResources[index]
	} else if serviceIndex >= 0 && serviceIndex < len(model.stackServices) {
		service := model.stackServices[serviceIndex]
		resource.ID = uniqueStackResourceValue(slugifyStackValue(service.Name), model.stackResources, func(candidate stackPublicResource) string {
			return candidate.ID
		})
		resource.Service = service.Name
		resource.Subdomain = uniqueStackResourceValue(slugifyStackValue(service.Name), model.stackResources, func(candidate stackPublicResource) string {
			return candidate.Subdomain
		})
		resource.Name = titleFromSlug(resource.ID)
		if len(service.ContainerPorts) > 0 {
			resource.Port = service.ContainerPorts[0]
		}
	}
	model.stackResourceIndex = index
	model.stackResourceReturn = returnScreen
	model.stackResourceAdvanced = false
	model.stackResourceInputs = stackResourceInputs(resource)
	model.focus = 1
	model.stackResourceInputs[model.focus].Focus()
	model.err = ""
	model.screen = profileSetupScreenStackResourceEditor
}

func uniqueStackResourceValue(base string, resources []stackPublicResource, value func(stackPublicResource) string) string {
	candidate := base
	for suffix := 2; ; suffix++ {
		used := false
		for _, resource := range resources {
			if value(resource) == candidate {
				used = true
				break
			}
		}
		if !used {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d", base, suffix)
	}
}

func stackResourceIndexesForService(resources []stackPublicResource, service string) []int {
	indexes := []int{}
	for index, resource := range resources {
		if resource.Service == service {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

func removeStackResourcesForService(resources []stackPublicResource, service string) []stackPublicResource {
	filtered := make([]stackPublicResource, 0, len(resources))
	for _, resource := range resources {
		if resource.Service != service {
			filtered = append(filtered, resource)
		}
	}
	return filtered
}

func (model *profileSetupModel) refreshStackServiceTable() {
	cursor := model.stackServiceTable.Cursor()
	model.stackServiceTable = newStackServiceTable(model.stackServices, model.stackResources)
	model.resizeStackServiceTable()
	if len(model.stackServices) > 0 {
		model.stackServiceTable.SetCursor(clampInt(cursor, 0, len(model.stackServices)-1))
	}
	model.stackServiceTable.Focus()
}

func (model *profileSetupModel) resizeStackServiceTable() {
	contentWidth := max(40, model.width-4)
	remaining := contentWidth - 8
	serviceWidth := clampInt(remaining/3, 10, 18)
	portsWidth := clampInt(remaining/4, 8, 16)
	hostnameWidth := max(10, remaining-serviceWidth-portsWidth-4)
	model.stackServiceTable.SetColumns([]table.Column{
		{Title: "Public", Width: 8},
		{Title: "Service", Width: serviceWidth},
		{Title: "Ports", Width: portsWidth},
		{Title: "Hostname", Width: hostnameWidth},
	})
	model.stackServiceTable.SetWidth(contentWidth)
}

func newStackServiceTable(services []composeServiceSummary, resources []stackPublicResource) table.Model {
	rows := make([]table.Row, 0, len(services))
	for _, service := range services {
		ports := "none declared"
		if len(service.ContainerPorts) > 0 {
			values := make([]string, len(service.ContainerPorts))
			for index, port := range service.ContainerPorts {
				values[index] = strconv.Itoa(port)
			}
			ports = strings.Join(values, ", ")
		}
		hostnames := []string{}
		for _, resource := range resources {
			if resource.Service == service.Name {
				hostnames = append(hostnames, resource.Subdomain)
			}
		}
		published := "[ ]"
		exposure := "private"
		if len(hostnames) > 0 {
			published = "[x]"
			exposure = strings.Join(hostnames, ", ")
		}
		rows = append(rows, table.Row{published, sanitizeTerminalLine(service.Name), ports, sanitizeTerminalLine(exposure)})
	}
	return table.New(
		table.WithColumns([]table.Column{
			{Title: "Public", Width: 8},
			{Title: "Service", Width: 18},
			{Title: "Ports", Width: 18},
			{Title: "Hostname", Width: 24},
		}),
		table.WithRows(rows),
		table.WithHeight(clampInt(len(rows)+1, 2, 12)),
		table.WithWidth(76),
		table.WithFocused(true),
	)
}

func newStackResourceTable(resources []stackPublicResource) table.Model {
	rows := make([]table.Row, 0, len(resources))
	for _, resource := range resources {
		rows = append(rows, table.Row{
			sanitizeTerminalLine(resource.ID),
			sanitizeTerminalLine(resource.Subdomain),
			fmt.Sprintf("%s:%d", sanitizeTerminalLine(resource.Service), resource.Port),
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
		return "", errors.New(setupNoProfileSelectedMessage)
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
	choice := model.profiles[model.selectedIndex]
	snapshot, err := loadProfileStackRepositorySnapshot(context.Background(), path, choice)
	if err != nil {
		model.err = err.Error()
		return
	}
	model.applyProfileStackSnapshot(snapshot)
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
	if model.deleteProfilePending {
		return model, nil
	}
	if key.String() == "enter" {
		if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
			model.err = setupNoProfileSelectedMessage
			model.screen = profileSetupScreenPicker
			return model, nil
		}
		if model.profileStore == nil {
			model.err = setupProfileStoreUnavailable
			return model, nil
		}
		choice := model.profiles[model.selectedIndex]
		name := safeProfileConfirmationName(choice.Profile)
		want := "delete " + name
		if strings.TrimSpace(model.deleteProfileInput.Value()) != want {
			model.err = fmt.Sprintf("type %q exactly to delete this profile", want)
			return model, nil
		}
		model.deleteProfilePending = true
		model.deleteProfileInput.Blur()
		store := model.profileStore
		profileID := choice.Profile.ID
		return model, func() tea.Msg {
			return profileDeletedMsg{profileID: profileID, err: deleteProfileWhenIdle(store, profileID)}
		}
	}
	var command tea.Cmd
	model.deleteProfileInput, command = updateSetupTextInput(model.deleteProfileInput, key)
	return model, command
}

func (model profileSetupModel) applyProfileDeleted(message profileDeletedMsg) profileSetupModel {
	model.deleteProfilePending = false
	if message.err != nil {
		model.err = sanitizeTerminalLine(message.err.Error())
		model.deleteProfileInput.Focus()
		return model
	}
	for index, choice := range model.profiles {
		if choice.Profile.ID == message.profileID {
			model.profiles = append(model.profiles[:index], model.profiles[index+1:]...)
			break
		}
	}
	model.profileList.SetItems(profilePickerItems(model.profiles))
	model.profileList.Select(0)
	model.selectedIndex = -1
	model.deleteProfileInput.SetValue("")
	model.err = ""
	model.profileNotice = "Profile deleted from local storage. The remote server was not changed."
	model.screen = profileSetupScreenPicker
	return model
}

func (model *profileSetupModel) goBack() {
	if model.screen == profileSetupScreenRunHistory && model.runDetailLoading {
		model.cancelProfileRunDetailLoad()
	}
	if target, ok := profileSetupBackTargets[model.screen]; ok {
		model.screen = target
		return
	}
	switch model.screen {
	case profileSetupScreenPicker:
		return
	case profileSetupScreenIntake:
		model.goBackFromProfileIntake()
	case profileSetupScreenAdvanced:
		model.goBackFromProfileAdvanced()
	case profileSetupScreenStackCompose:
		model.goBackFromStackCompose()
	case profileSetupScreenStackResourceEditor:
		model.screen = model.stackResourceReturn
	case profileSetupScreenStackEnvironment:
		model.goBackFromStackEnvironment()
	case profileSetupScreenStackReview:
		model.stackInputs[0].Blur()
		model.openStackEnvironment(profileSetupScreenStackReview)
	case profileSetupScreenStackCommit:
		model.stackCommitInput.Blur()
		model.screen = profileSetupScreenStacks
	case profileSetupScreenCloudConfirm:
		model.cloudTokenInput.Blur()
		model.cloudConfirmInput.Blur()
		model.screen = profileSetupScreenCloud
	case profileSetupScreenReview:
		model.goBackFromProfileReview()
	}
}

var profileSetupBackTargets = map[profileSetupScreen]profileSetupScreen{
	profileSetupScreenDashboard:          profileSetupScreenPicker,
	profileSetupScreenRunHistory:         profileSetupScreenDashboard,
	profileSetupScreenRunDetail:          profileSetupScreenRunHistory,
	profileSetupScreenGitHubToken:        profileSetupScreenDashboard,
	profileSetupScreenRepository:         profileSetupScreenIntake,
	profileSetupScreenRepositoryDetails:  profileSetupScreenRepository,
	profileSetupScreenStacks:             profileSetupScreenDashboard,
	profileSetupScreenStackServices:      profileSetupScreenStackCompose,
	profileSetupScreenStackEditor:        profileSetupScreenStacks,
	profileSetupScreenStackDeleteConfirm: profileSetupScreenStacks,
	profileSetupScreenStackDiff:          profileSetupScreenStacks,
	profileSetupScreenCloud:              profileSetupScreenDashboard,
	profileSetupScreenCloudRunning:       profileSetupScreenCloudConfirm,
	profileSetupScreenDeleteConfirm:      profileSetupScreenDashboard,
}

func (model *profileSetupModel) goBackFromProfileIntake() {
	if model.selectedIndex >= 0 {
		model.singleStage = ""
		model.screen = profileSetupScreenDashboard
		return
	}
	model.screen = profileSetupScreenPicker
}

func (model *profileSetupModel) goBackFromProfileAdvanced() {
	model.singleStage = ""
	if model.selectedIndex >= 0 {
		model.screen = profileSetupScreenDashboard
		return
	}
	model.screen = profileSetupScreenIntake
}

func (model *profileSetupModel) goBackFromStackCompose() {
	if model.stackComposeManual {
		model.stackComposeManual = false
		model.stackComposeInput.Blur()
		return
	}
	model.screen = profileSetupScreenStacks
}

func (model *profileSetupModel) goBackFromStackEnvironment() {
	switch model.stackEnvironmentMode {
	case stackEnvironmentManual:
		model.stackEnvironmentInput.Blur()
		model.stackEnvironmentMode = stackEnvironmentBrowse
	case stackEnvironmentBrowse:
		model.stackEnvironmentMode = stackEnvironmentChoose
	default:
		model.screen = model.stackEnvironmentReturn
		if model.stackEnvironmentReturn == profileSetupScreenStackReview {
			model.screen = profileSetupScreenStackServices
		}
	}
}

func (model *profileSetupModel) goBackFromProfileReview() {
	if model.selectedIndex >= 0 {
		model.singleStage = ""
		model.screen = profileSetupScreenDashboard
		return
	}
	model.screen = profileSetupScreenIntake
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
	model.prepareDashboard()
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
		return
	}
	path := model.profiles[model.selectedIndex].Profile.ConfigRepositoryPath
	if path != "" {
		if _, err := os.Stat(filepath.Join(expandUserPath(path), ".git")); err == nil {
			model.refreshStacks()
			model.stackTable.Blur()
		}
	}
}

func (model *profileSetupModel) prepareDashboard() {
	model.pangolinStatus = pangolinRegistrationUnknown
	model.pangolinError = ""
	model.showPangolinAccess = false
	model.stacks = nil
	model.stackGitStatus = ""
	model.stackHead = ""
	model.stackNeedsPush = false
	model.stackSyncStatus = ""
	model.cloudNotice = ""
	model.dashboardViewport.GotoTop()
	model.stackTable = newStackTable(nil, "", nil)
	model.resizeStackTable()
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
		model.stageTable = newProfileStageTable(nil)
		return
	}
	state := model.profiles[model.selectedIndex].State
	model.stageTable = newProfileStageTable(&state, model.profiles[model.selectedIndex].Secrets)
}

func (model *profileSetupModel) checkPangolinRegistration() tea.Cmd {
	model.pangolinRequestID++
	requestID := model.pangolinRequestID
	model.pangolinRequestDomain = ""
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
	model.pangolinRequestDomain = profile.BaseDomain
	requestContext := model.tuiContext
	if requestContext == nil {
		requestContext = context.Background()
	}
	return func() tea.Msg {
		complete, err := pangolinInitialSetupComplete(requestContext, pangolinRegistrationHTTPClient, "https://pangolin."+profile.BaseDomain)
		return pangolinRegistrationStatusMsg{
			requestID:  requestID,
			profileID:  profile.ID,
			baseDomain: profile.BaseDomain,
			complete:   complete,
			err:        err,
		}
	}
}

func (model profileSetupModel) selectedProfileHasPangolinAccess() bool {
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
		return false
	}
	choice := model.profiles[model.selectedIndex]
	if model.pangolinStatus == pangolinRegistrationIncomplete {
		return choice.Secrets.PangolinSetupToken != ""
	}
	return choice.Secrets.PangolinAdminPassword != "" &&
		firstNonEmpty(choice.Profile.PangolinAdminEmail, choice.Profile.LetsEncryptEmail) != ""
}

func (model *profileSetupModel) refreshPlanPreview() {
	options, err := model.optionsFromInputs()
	if err != nil {
		model.planViewport.SetContent(err.Error())
		return
	}
	config := model.reviewSetupConfig(options)
	model.planViewport.SetContent(model.reviewPlanSummary(options, config))
	model.planViewport.GotoTop()
}

func (model profileSetupModel) reviewSetupConfig(options setupCLIOptions) setupConfig {
	config := setupConfig{
		Mode:               setupModeFullRun,
		Host:               options.IP,
		InitialSSHUser:     firstNonEmpty(options.InitialSSHUser, "root"),
		AdminUser:          firstNonEmpty(options.AdminUser, "servestead"),
		PrivateKeyPath:     expandUserPath(firstNonEmpty(options.PrivateKeyPath, defaultKeygenConfig().Path)),
		AdminPublicKeyPath: publicKeyPath(expandUserPath(firstNonEmpty(options.PrivateKeyPath, defaultKeygenConfig().Path))),
		BaseDomain:         options.BaseDomain,
		LetsEncryptEmail:   options.LetsEncryptEmail,
		PangolinAdminEmail: firstNonEmpty(options.PangolinAdminEmail, options.LetsEncryptEmail),
		ProfileID:          "(new profile)",
		ServerSecret:       setupGeneratedPlaceholder,
	}
	if !options.Fresh {
		config.ProfileID = firstNonEmpty(options.ProfileID, "(new profile)")
	}
	if model.singleStage != "" {
		config.Mode = setupModeForStage(model.singleStage)
	}
	return config
}

func (model profileSetupModel) reviewPlanSummary(options setupCLIOptions, config setupConfig) string {
	if model.singleStage == "" {
		return setupPlanSummary(config)
	}
	var builder strings.Builder
	stageLabel := sanitizeTerminalLine(profileRunStageLabel(model.singleStage))
	fmt.Fprintf(&builder, "Selected action: %s\n", stageLabel)
	fmt.Fprintf(&builder, "- Target: %s\n", reviewTargetLabel(options))
	fmt.Fprintf(&builder, "- SSH user: %s with %s\n", sanitizeTerminalLine(config.AdminUser), sanitizeTerminalLine(config.PrivateKeyPath))
	if config.BaseDomain != "" {
		fmt.Fprintf(&builder, "- Domain: %s\n", sanitizeTerminalLine(config.BaseDomain))
	}
	if config.LetsEncryptEmail != "" {
		fmt.Fprintf(&builder, "- Let's Encrypt email: %s\n", sanitizeTerminalLine(config.LetsEncryptEmail))
	}
	builder.WriteString("\nWhat will run:\n")
	switch model.singleStage {
	case "platform":
		builder.WriteString("- Network: configure Docker networking and UFW.\n")
		builder.WriteString("- Proxy: deploy Pangolin, Traefik, Gerbil, and Newt.\n")
		builder.WriteString("- Observability: deploy Beszel, Dozzle, and Dockhand from the committed repository configuration.\n")
		builder.WriteString("- Generate or reuse profile secrets without printing them.\n")
		builder.WriteString("\nWhat will not run:\n")
		builder.WriteString("- No new VPS will be provisioned.\n")
		builder.WriteString("- Bootstrap and Harden will not run from this action.\n")
	case "observability":
		builder.WriteString("- Deploy observability services from the committed repository configuration.\n")
	case "stacks":
		builder.WriteString("- Synchronize committed standalone stack configuration with the server.\n")
	default:
		if strings.HasPrefix(model.singleStage, setupStageStackPrefix) {
			fmt.Fprintf(&builder, "- Deploy only the %s standalone stack from committed configuration.\n", sanitizeTerminalLine(strings.TrimPrefix(model.singleStage, setupStageStackPrefix)))
		} else {
			fmt.Fprintf(&builder, "- Run only the selected %s stage.\n", stageLabel)
		}
	}
	if stageUsesRepository(model.singleStage) {
		builder.WriteString("\nRepository preparation:\n")
		fmt.Fprintf(&builder, "- %s\n", model.repositoryReviewLine())
		builder.WriteString("- SSH execution starts only after repository preparation succeeds.\n")
	}
	if model.singleStage == "platform" && config.BaseDomain != "" && config.Host != "" {
		fmt.Fprintf(&builder, "\n%s.\n", sanitizeTerminalLine(requiredDNSGuidance(config.BaseDomain, config.Host)))
	}
	return builder.String()
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
		AdminUser:             firstNonEmpty(value(model.advanced, 2), "servestead"),
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
		options.IP = firstNonEmpty(model.profiles[model.selectedIndex].Profile.IP, options.IP)
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
	if err := validateFullRunConfig(config); err != nil {
		return setupCLIOptions{}, err
	}
	return options, nil
}

func (model profileSetupModel) optionsForSelectedProfile() (setupCLIOptions, error) {
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
		return setupCLIOptions{}, errors.New(setupNoProfileSelectedMessage)
	}
	profile := model.profiles[model.selectedIndex].Profile
	inputValue := func(index int) string {
		if index < 0 || index >= len(model.inputs) {
			return ""
		}
		return strings.TrimSpace(model.inputs[index].Value())
	}
	advancedValue := func(index int) string {
		if index < 0 || index >= len(model.advanced) {
			return ""
		}
		return strings.TrimSpace(model.advanced[index].Value())
	}
	options := setupCLIOptions{
		IP:                    firstNonEmpty(profile.IP, inputValue(0)),
		ProfileID:             profile.ID,
		Name:                  firstNonEmpty(advancedValue(0), profile.Name),
		InitialSSHUser:        firstNonEmpty(advancedValue(1), profile.InitialSSHUser),
		AdminUser:             firstNonEmpty(advancedValue(2), profile.AdminUser),
		PrivateKeyPath:        expandUserPath(firstNonEmpty(inputValue(1), profile.PrivateKeyPath)),
		BaseDomain:            firstNonEmpty(inputValue(2), profile.BaseDomain),
		LetsEncryptEmail:      firstNonEmpty(inputValue(3), profile.LetsEncryptEmail),
		PangolinAdminEmail:    firstNonEmpty(advancedValue(3), profile.PangolinAdminEmail, firstNonEmpty(inputValue(3), profile.LetsEncryptEmail)),
		PangolinAdminPassword: advancedValue(4),
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
		return "", errors.New(setupNoProfileSelectedMessage)
	}
	cursor := model.stageTable.Cursor()
	if cursor < 0 || cursor >= len(dashboardStageOrder) {
		return "", errors.New("no setup stage selected")
	}
	return dashboardStageOrder[cursor], nil
}

func (model profileSetupModel) View() tea.View {
	if model.profileSetupTerminalTooSmall() {
		return altScreenView(fmt.Sprintf(
			"Servestead setup\n\nTerminal too small: %dx%d\nResize to at least %dx%d to continue.",
			model.width,
			model.height,
			profileSetupMinWidth,
			profileSetupMinHeight,
		))
	}
	var builder strings.Builder
	helpText := model.profileSetupHelpText()
	builder.WriteString(setupTitleStyle.Render("Servestead setup"))
	builder.WriteString("\n")
	tagline := "Profile-aware setup manages the server platform and standalone application stacks."
	builder.WriteString(setupHelpStyle.Render(truncateForTable(tagline, max(1, model.width))))
	builder.WriteString("\n\n")
	if model.profileStackOperationBusy() {
		builder.WriteString(model.profileStackOperationView())
		return altScreenView(fitTerminalWidth(builder.String(), model.width))
	}

	switch model.screen {
	case profileSetupScreenPicker:
		builder.WriteString(model.profileList.View())
	case profileSetupScreenDashboard:
		model.syncDashboardViewport()
		builder.WriteString(model.dashboardViewport.View())
	case profileSetupScreenRunHistory:
		builder.WriteString(model.runHistoryView())
	case profileSetupScreenRunDetail:
		builder.WriteString(model.runDetailView())
	case profileSetupScreenIntake:
		builder.WriteString(model.inputView(false))
	case profileSetupScreenAdvanced:
		builder.WriteString(model.inputView(true))
	case profileSetupScreenRepository:
		builder.WriteString(model.repositoryChoiceView())
	case profileSetupScreenRepositoryDetails:
		builder.WriteString(model.repositoryDetailsView())
	case profileSetupScreenGitHubToken:
		builder.WriteString(model.githubTokenView())
	case profileSetupScreenStacks:
		builder.WriteString(model.stacksView())
	case profileSetupScreenStackCompose:
		builder.WriteString("Add application stack\n")
		builder.WriteString(setupHelpStyle.Render("Choose a Docker Compose file. Use the browser or press / to type a path."))
		builder.WriteString("\n\n")
		if model.stackComposeManual {
			builder.WriteString(model.stackComposeInput.View())
		} else {
			builder.WriteString(setupHelpStyle.Render(sanitizeTerminalLine(model.stackComposePicker.CurrentDirectory)))
			builder.WriteString("\n\n")
			if model.stackComposeBrowserError != "" {
				builder.WriteString(setupWarningStyle.Render(sanitizeTerminalText(model.stackComposeBrowserError)))
			} else {
				builder.WriteString(sanitizeTerminalText(model.stackComposePicker.View()))
			}
		}
	case profileSetupScreenStackServices:
		builder.WriteString(model.stackServicesView())
	case profileSetupScreenStackEditor:
		model.syncStackEditorViewport()
		builder.WriteString(model.stackEditorViewport.View())
	case profileSetupScreenStackResourceEditor:
		builder.WriteString(model.stackResourceEditorView())
	case profileSetupScreenStackEnvironment:
		builder.WriteString(model.stackEnvironmentView())
	case profileSetupScreenStackReview:
		model.syncStackReviewViewport()
		builder.WriteString(model.stackReviewViewport.View())
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
	case profileSetupScreenCloud:
		builder.WriteString(model.profileCloudView())
	case profileSetupScreenCloudConfirm:
		builder.WriteString(model.profileCloudConfirmView())
	case profileSetupScreenCloudRunning:
		builder.WriteString(model.profileCloudRunningView())
	case profileSetupScreenCloudSaveRecovery:
		builder.WriteString(model.profileCloudSaveRecoveryView())
	case profileSetupScreenReview:
		builder.WriteString(model.reviewView())
	case profileSetupScreenDeleteConfirm:
		builder.WriteString(model.deleteConfirmView())
	}
	if model.err != "" && !profileSetupErrorInsideViewport(model.screen) {
		builder.WriteString("\n\n")
		builder.WriteString(setupErrorStyle.Render(sanitizeTerminalText(model.err)))
	}
	builder.WriteString("\n\n")
	builder.WriteString(helpText)
	return altScreenView(fitTerminalWidth(builder.String(), model.width))
}

func (model profileSetupModel) profileSetupHelpText() string {
	helpMap := profileSetupHelp{
		screen:               model.screen,
		hasProfile:           model.selectedIndex >= 0,
		hasPangolinAccess:    model.selectedProfileHasPangolinAccess(),
		hasCloud:             model.selectedProfileHasCloud(),
		stackComposeManual:   model.stackComposeManual,
		stackEnvironmentMode: model.stackEnvironmentMode,
		stackEditorFocus:     model.focus,
	}
	return model.help.View(profileSetupHelpView{base: helpMap, expanded: model.help.ShowAll, width: model.help.Width()})
}

func (model *profileSetupModel) syncDashboardViewport() {
	content := model.dashboardView()
	model.syncProfileSetupViewport(&model.dashboardViewport, content)
}

func (model *profileSetupModel) syncStackEditorViewport() {
	model.syncProfileSetupViewport(&model.stackEditorViewport, model.stackEditorView())
}

func (model *profileSetupModel) syncStackReviewViewport() {
	model.syncProfileSetupViewport(&model.stackReviewViewport, model.stackReviewView())
}

func (model *profileSetupModel) syncProfileSetupViewport(target *viewport.Model, content string) {
	helpHeight := max(1, lipgloss.Height(model.profileSetupHelpText()))
	target.SetWidth(max(1, model.width))
	target.SetHeight(max(3, model.height-4-helpHeight))
	if model.err != "" {
		content += "\n\n" + setupErrorStyle.Render(sanitizeTerminalText(model.err))
	}
	target.SetContent(fitTerminalWidth(content, model.width))
}

func profileSetupErrorInsideViewport(screen profileSetupScreen) bool {
	return screen == profileSetupScreenDashboard || screen == profileSetupScreenStackEditor || screen == profileSetupScreenStackReview
}

func profileSetupViewportKey(key tea.KeyMsg) bool {
	switch key.String() {
	case "pgup", "pgdown", "home", "end":
		return true
	default:
		return false
	}
}

func (model profileSetupModel) dashboardView() string {
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
		return setupNoProfileSelectedView
	}
	choice := model.profiles[model.selectedIndex]
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Dashboard for %s (%s)\n\n", sanitizeTerminalLine(firstNonEmpty(choice.Profile.Name, choice.Profile.IP)), sanitizeTerminalLine(choice.Profile.IP)))
	builder.WriteString(fmt.Sprintf("Domain: %s\n", sanitizeTerminalLine(firstNonEmpty(choice.Profile.BaseDomain, "(missing)"))))
	builder.WriteString(fmt.Sprintf("Email:  %s\n", sanitizeTerminalLine(firstNonEmpty(choice.Profile.LetsEncryptEmail, "(missing)"))))
	repositoryPath := choice.Profile.ConfigRepositoryPath
	if repositoryPath == "" {
		builder.WriteString("Config repository: not created; choose one during full setup review.\n\n")
	} else if _, err := os.Stat(expandUserPath(repositoryPath)); errors.Is(err, os.ErrNotExist) {
		builder.WriteString(fmt.Sprintf("Config repository: %s (will be created before the next run)\n", sanitizeTerminalLine(repositoryPath)))
	} else {
		builder.WriteString(fmt.Sprintf("Config repository: %s\n", sanitizeTerminalLine(repositoryPath)))
	}
	builder.WriteString(fmt.Sprintf("GitHub token: %s\n\n", githubTokenStatusSummary(choice.Secrets)))
	if model.profileNotice != "" {
		builder.WriteString(setupHelpStyle.Render(sanitizeTerminalText(model.profileNotice)))
		builder.WriteString("\n\n")
	}
	builder.WriteString(model.pangolinRegistrationView(choice))
	builder.WriteString("\n\n")
	if choice.Profile.Cloud != nil {
		builder.WriteString(model.profileCloudSummary(choice.Profile))
		builder.WriteString("\n\n")
	}
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
	builder.WriteString(setupHelpStyle.Render("Keys: r runs the selected stage; h opens history; s manages stacks; Page Up/Down scrolls."))
	return builder.String()
}

func (model profileSetupModel) runHistoryView() string {
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
		return setupNoProfileSelectedView
	}
	choice := model.profiles[model.selectedIndex]
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Run history for %s\n", sanitizeTerminalLine(firstNonEmpty(choice.Profile.Name, choice.Profile.IP))))
	builder.WriteString(setupHelpStyle.Render("Saved setup runs are ordered newest first. Select one to inspect its masked local log."))
	builder.WriteString("\n\n")
	if model.runDetailLoading {
		builder.WriteString("Loading the bounded saved-log tail. Press Esc to cancel and return to the dashboard.")
		return builder.String()
	}
	if len(model.runs) == 0 {
		builder.WriteString("No setup runs have been recorded for this profile.")
	} else {
		builder.WriteString(model.runTable.View())
	}
	return builder.String()
}

func (model profileSetupModel) runDetailView() string {
	run, ok := model.selectedHistoryRun()
	if !ok {
		return "No setup run selected."
	}
	secrets := ProfileSecrets{}
	if model.selectedIndex >= 0 && model.selectedIndex < len(model.profiles) {
		secrets = model.profiles[model.selectedIndex].Secrets
	}
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Run %s\n\n", sanitizeTerminalLine(run.ID)))
	builder.WriteString(fmt.Sprintf("Status:  %s\n", sanitizeTerminalLine(firstNonEmpty(run.Status, runStatusPlanned))))
	builder.WriteString(fmt.Sprintf("Created: %s\n", formatProfileRunTime(run.CreatedAt)))
	builder.WriteString(fmt.Sprintf("Updated: %s\n", formatProfileRunTime(run.UpdatedAt)))
	builder.WriteString(fmt.Sprintf("Stages:  %s\n", sanitizeTerminalLine(profileRunStageSummary(run))))
	if message := maskProfileSecrets(profileRunErrorSummary(run), secrets); message != "" {
		builder.WriteString(setupErrorStyle.Render("Error: " + message))
		builder.WriteByte('\n')
	}
	builder.WriteString(fmt.Sprintf("Log:     %s\n\n", sanitizeTerminalLine(model.runLogPath)))
	builder.WriteString("Masked event log\n")
	builder.WriteString(model.runLogViewport.View())
	return builder.String()
}

func (model profileSetupModel) selectedHistoryRun() (SetupRun, bool) {
	for _, run := range model.runs {
		if run.ID == model.selectedRunID {
			return run, true
		}
	}
	return SetupRun{}, false
}

func formatProfileRunTime(value time.Time) string {
	if value.IsZero() {
		return "unknown"
	}
	return value.Local().Format("2006-01-02 15:04:05 MST")
}

func (model profileSetupModel) githubTokenView() string {
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
		return setupNoProfileSelectedView
	}
	choice := model.profiles[model.selectedIndex]
	var builder strings.Builder
	builder.WriteString("GitHub repository token\n")
	builder.WriteString(setupHelpStyle.Render("Private repositories require a PAT. Public repositories can also use one to avoid anonymous rate limits. Recommended scope: fine-grained PAT, selected repository only, Contents read-only."))
	builder.WriteString("\n\n")
	builder.WriteString(fmt.Sprintf("Profile token: %s\n", configuredStatus(strings.TrimSpace(choice.Secrets.GitHubToken) != "")))
	builder.WriteString(fmt.Sprintf("Environment token: %s\n", configuredStatus(strings.TrimSpace(os.Getenv("SERVESTEAD_GITHUB_TOKEN")) != "")))
	_, source := effectiveGitHubToken(choice.Secrets)
	builder.WriteString(fmt.Sprintf("Effective source: %s\n", source))
	if model.githubTokenNotice != "" {
		builder.WriteString("\n")
		builder.WriteString(setupHelpStyle.Render(model.githubTokenNotice))
		builder.WriteString("\n")
	}
	builder.WriteString("\n")
	builder.WriteString(model.githubTokenInput.View())
	builder.WriteString("\n\n")
	builder.WriteString(setupHelpStyle.Render("Paste a token and press enter to save. Press Ctrl+E to store SERVESTEAD_GITHUB_TOKEN. Press Ctrl+X to remove the saved profile token."))
	return builder.String()
}

func githubTokenStatusSummary(secrets ProfileSecrets) string {
	_, source := effectiveGitHubToken(secrets)
	switch source {
	case "environment":
		if strings.TrimSpace(secrets.GitHubToken) != "" {
			return "environment override active; profile token configured"
		}
		return "environment override active"
	case "profile":
		return "profile token configured"
	default:
		return "not configured"
	}
}

func configuredStatus(configured bool) string {
	if configured {
		return "configured"
	}
	return "not configured"
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
		builder.WriteString(setupWarningStyle.Render(sanitizeTerminalText(model.stackSyncStatus)))
		advice := " • press y to sync"
		if model.stackSyncStatus == "commit required" {
			advice = " • review, stage, and commit first"
		} else if model.stackSyncStatus == "push required" {
			advice = " • press p to push first"
		} else if model.stackSyncStatus == "review required" {
			advice = " • press enter and save metadata first"
		}
		builder.WriteString(setupHelpStyle.Render(advice))
		builder.WriteString("\n\n")
	}
	if len(model.stacks) == 0 {
		builder.WriteString("No application stacks configured. Press a to import a Compose file, or place one at stacks/<name>/compose.yaml.\n")
	} else {
		builder.WriteString(model.stackTable.View())
	}
	if model.stackNotice != "" {
		builder.WriteString("\n")
		builder.WriteString(setupWarningStyle.Render(sanitizeTerminalText(model.stackNotice)))
	}
	return builder.String()
}

func (model profileSetupModel) stackServicesView() string {
	var builder strings.Builder
	builder.WriteString("Choose public services\n")
	builder.WriteString(setupHelpStyle.Render("Every detected service deploys. Only services with configured routes become public."))
	builder.WriteString("\n\n")
	builder.WriteString(model.stackServiceTable.View())
	builder.WriteString("\n")
	builder.WriteString(fmt.Sprintf("%d public route(s) across %d service(s).\n", len(model.stackResources), publishedStackServiceCount(model.stackResources)))
	return builder.String()
}

func publishedStackServiceCount(resources []stackPublicResource) int {
	services := map[string]struct{}{}
	for _, resource := range resources {
		services[resource.Service] = struct{}{}
	}
	return len(services)
}

func (model profileSetupModel) stackEditorView() string {
	var builder strings.Builder
	title := "Edit stack"
	if model.stackOriginalName == "" {
		title = "Add stack"
	} else if model.stackMetadataMissing {
		title = "Review stack"
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
		builder.WriteString(fmt.Sprintf("  %s: %s\n", sanitizeTerminalLine(service.Name), ports))
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
	builder.WriteString("\nRuntime secrets: ")
	if len(model.stackEnvironmentKeys) == 0 {
		builder.WriteString("none")
	} else {
		builder.WriteString(fmt.Sprintf("%d key(s): %s", len(model.stackEnvironmentKeys), sanitizeTerminalLine(strings.Join(model.stackEnvironmentKeys, ", "))))
	}
	builder.WriteString("\n")
	return builder.String()
}

func (model profileSetupModel) stackEnvironmentView() string {
	var builder strings.Builder
	builder.WriteString("Runtime secrets\n")
	builder.WriteString(setupHelpStyle.Render("Values are written to the encrypted stack secret file in the configuration repository. Only key names are shown after save."))
	builder.WriteString("\n\n")
	switch model.stackEnvironmentMode {
	case stackEnvironmentManual:
		builder.WriteString(model.stackEnvironmentInput.View())
	case stackEnvironmentBrowse:
		builder.WriteString(setupHelpStyle.Render(sanitizeTerminalLine(model.stackEnvironmentPicker.CurrentDirectory)))
		builder.WriteString("\n\n")
		if model.stackEnvironmentBrowserError != "" {
			builder.WriteString(setupWarningStyle.Render(sanitizeTerminalText(model.stackEnvironmentBrowserError)))
		} else {
			builder.WriteString(sanitizeTerminalText(model.stackEnvironmentPicker.View()))
		}
	default:
		for index, option := range model.stackEnvironmentOptions {
			cursor := "  "
			if index == model.stackEnvironmentCursor {
				cursor = "> "
			}
			line := cursor + option.label
			if option.detail != "" {
				line += " — " + sanitizeTerminalLine(option.detail)
			}
			if index == model.stackEnvironmentCursor {
				builder.WriteString(setupTitleStyle.Render(line))
			} else {
				builder.WriteString(line)
			}
			builder.WriteByte('\n')
		}
	}
	return builder.String()
}

func (model profileSetupModel) stackReviewView() string {
	var builder strings.Builder
	builder.WriteString("Review application stack\n")
	builder.WriteString(setupHelpStyle.Render("Saving writes local repository files. It does not deploy or commit."))
	builder.WriteString("\n\n")
	builder.WriteString(model.stackInputs[0].View())
	builder.WriteString("\n\n")
	builder.WriteString(fmt.Sprintf("Compose: %s\n", sanitizeTerminalLine(model.stackComposePath)))
	builder.WriteString(fmt.Sprintf("Services: %d total, %d public, %d private\n",
		len(model.stackServices),
		publishedStackServiceCount(model.stackResources),
		len(model.stackServices)-publishedStackServiceCount(model.stackResources),
	))
	if len(model.stackResources) == 0 {
		builder.WriteString("Routes: none; this stack remains private\n")
	} else {
		builder.WriteString("Routes:\n")
		for _, resource := range model.stackResources {
			builder.WriteString(fmt.Sprintf("  %s: %s:%d → %s\n", sanitizeTerminalLine(resource.ID), sanitizeTerminalLine(resource.Service), resource.Port, sanitizeTerminalLine(resource.Subdomain)))
		}
	}
	if len(model.stackEnvironmentKeys) == 0 {
		builder.WriteString("Runtime secrets: none\n")
	} else {
		builder.WriteString(fmt.Sprintf("Runtime secrets: %d key(s); values hidden\n", len(model.stackEnvironmentKeys)))
	}
	builder.WriteString("\n")
	builder.WriteString(setupWarningStyle.Render("Next: review the diff, stage, and commit from the stack manager."))
	return builder.String()
}

func (model profileSetupModel) stackResourceEditorView() string {
	title := "Add public resource"
	if model.stackResourceIndex >= 0 {
		title = "Edit public resource"
	}
	var builder strings.Builder
	builder.WriteString(title + "\n")
	builder.WriteString(setupHelpStyle.Render("Common route fields are shown first. Press ctrl+x to toggle stable ID, display name, and health check."))
	builder.WriteString("\n\n")
	for _, index := range stackResourceVisibleInputs(model.stackResourceAdvanced) {
		builder.WriteString(model.stackResourceInputs[index].View())
		builder.WriteByte('\n')
	}
	if !model.stackResourceAdvanced {
		builder.WriteString(setupHelpStyle.Render("Advanced fields are using generated defaults."))
		builder.WriteByte('\n')
	}
	return builder.String()
}

func (model profileSetupModel) stackDeleteConfirmView() string {
	stack, ok := model.selectedStack()
	if !ok {
		return "No stack selected."
	}
	repositoryPath := ""
	if model.selectedIndex >= 0 && model.selectedIndex < len(model.profiles) {
		repositoryPath = model.profiles[model.selectedIndex].Profile.ConfigRepositoryPath
	}
	name := safeStackConfirmationName(stack.Name)
	stackPath := sanitizeTerminalLine(filepath.Join(expandUserPath(repositoryPath), "stacks", stack.Name))
	want := "delete " + name
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Remove stack %s?\n\n", name))
	builder.WriteString(fmt.Sprintf("Local path: %s\n", stackPath))
	builder.WriteString("This deletes the local stack directory, including Compose and application files. It does not immediately remove the remote deployment.\n\n")
	builder.WriteString(fmt.Sprintf("Type %q to continue:\n", want))
	builder.WriteString(model.stackDeleteInput.View())
	return builder.String()
}

func (model profileSetupModel) pangolinRegistrationView(choice profileChoice) string {
	proxyComplete := completedSetupStages(choice.State)["proxy"]
	var builder strings.Builder
	builder.WriteString(model.pangolinRegistrationStatusText(proxyComplete))
	if !proxyComplete {
		return builder.String()
	}
	builder.WriteString(model.pangolinRegistrationAccessText(choice))
	return builder.String()
}

func (model profileSetupModel) pangolinRegistrationStatusText(proxyComplete bool) string {
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
			builder.WriteString(setupHelpStyle.Render(sanitizeTerminalText(model.pangolinError)))
		}
	default:
		if proxyComplete {
			builder.WriteString("Pangolin registration: not checked.")
		} else {
			builder.WriteString("Pangolin registration: waiting for Proxy deployment.")
		}
	}
	return builder.String()
}

func (model profileSetupModel) pangolinRegistrationAccessText(choice profileChoice) string {
	if model.pangolinStatus == pangolinRegistrationIncomplete {
		return model.pangolinInitialSetupAccessText(choice)
	}
	username := firstNonEmpty(choice.Profile.PangolinAdminEmail, choice.Profile.LetsEncryptEmail)
	if choice.Secrets.PangolinAdminPassword == "" || username == "" {
		var builder strings.Builder
		builder.WriteString("\n")
		builder.WriteString(setupWarningStyle.Render("No saved Pangolin administrator credentials. Enter the current email and password in Advanced setup before running Platform, Observability, or stacks."))
		return builder.String()
	}
	if model.showPangolinAccess {
		var builder strings.Builder
		builder.WriteString("\n")
		printTerminalPangolinAdminCredentials(&builder, choice.Profile, choice.Secrets)
		return builder.String()
	}
	return "\n" + setupHelpStyle.Render("Press p to reveal the saved Pangolin admin username and password.")
}

func (model profileSetupModel) pangolinInitialSetupAccessText(choice profileChoice) string {
	if choice.Secrets.PangolinSetupToken == "" {
		return "\n" + setupWarningStyle.Render("No saved setup token. Run Platform once to generate and deploy one.")
	}
	if !model.showPangolinAccess {
		return "\n" + setupHelpStyle.Render("Press p to reveal the saved setup token and initial-setup URL.")
	}
	var builder strings.Builder
	builder.WriteString("\n")
	printTerminalPangolinInitialSetupAccess(&builder, choice.Profile.BaseDomain, choice.Secrets.PangolinSetupToken)
	return builder.String()
}

func printTerminalPangolinAdminCredentials(output io.Writer, profile Profile, secrets ProfileSecrets) {
	fmt.Fprintf(output, "Pangolin URL: https://pangolin.%s\n", sanitizeTerminalLine(profile.BaseDomain))
	fmt.Fprintf(output, "Username: %s\n", sanitizeTerminalLine(firstNonEmpty(profile.PangolinAdminEmail, profile.LetsEncryptEmail)))
	fmt.Fprintf(output, "Password: %s\n", terminalCredentialDisplay(secrets.PangolinAdminPassword))
}

func printTerminalPangolinInitialSetupAccess(output io.Writer, baseDomain, setupToken string) {
	fmt.Fprintf(output, "Pangolin initial setup: https://pangolin.%s/auth/initial-setup\n", sanitizeTerminalLine(baseDomain))
	fmt.Fprintf(output, "Setup token: %s\n", terminalCredentialDisplay(setupToken))
}

func terminalCredentialDisplay(value string) string {
	if sanitizeTerminalLine(value) == value {
		return value
	}
	return strconv.QuoteToASCII(value) + " (escaped; use pangolin-credentials for exact bytes)"
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
		if model.selectedIndex >= 0 {
			builder.WriteString("\n")
			builder.WriteString(setupHelpStyle.Render("Press ctrl+s to save profile settings without starting a run."))
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
	if model.selectedIndex >= 0 {
		builder.WriteString(setupHelpStyle.Render("Press ctrl+s to save profile settings without starting a run."))
		builder.WriteString("\n")
	}
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
	if model.singleStage == "" {
		builder.WriteString("Review full setup plan\n\n")
	} else {
		builder.WriteString(fmt.Sprintf("Review %s run\n\n", profileRunStageLabel(model.singleStage)))
	}
	builder.WriteString(model.planViewport.View())
	builder.WriteString("\n")
	builder.WriteString("\nRun\n")
	builder.WriteString("- ")
	builder.WriteString(model.runReviewLine())
	builder.WriteString("\n")
	if model.singleStage == "platform" {
		builder.WriteString("- No new VPS will be provisioned. Bootstrap and Harden will not run from this action.\n")
	}
	builder.WriteString("\nProfile\n")
	builder.WriteString("- ")
	builder.WriteString(model.profileReviewLine())
	builder.WriteString("\n")
	if model.singleStage == "" || stageUsesRepository(model.singleStage) {
		builder.WriteString("\nRepository preparation\n")
		builder.WriteString("- ")
		builder.WriteString(model.repositoryReviewLine())
		builder.WriteString("\n")
	}
	builder.WriteString("\nExecution order\n")
	builder.WriteString("- Run local preflight checks before contacting the server.\n")
	if model.singleStage == "" || stageUsesRepository(model.singleStage) {
		builder.WriteString("- Prepare the local configuration repository before SSH execution.\n")
	}
	builder.WriteString("- SSH execution starts only after repository preparation succeeds.\n")
	return builder.String()
}

func (model profileSetupModel) runReviewLine() string {
	if model.singleStage == "" {
		return "Full setup can run Bootstrap, Harden, Platform, and committed stack deployment as needed."
	}
	if model.singleStage == "platform" {
		return "Platform runs Network, Proxy, and Observability for the selected profile."
	}
	return fmt.Sprintf("Run only %s for the selected profile.", profileRunStageLabel(model.singleStage))
}

func (model profileSetupModel) profileReviewLine() string {
	if model.selectedIndex >= 0 && !model.fresh {
		return "Resume the selected saved profile."
	}
	if model.fresh {
		return "Create a fresh profile for this server and preserve the existing saved profile."
	}
	return "Create a new saved profile for this server."
}

func (model profileSetupModel) repositoryReviewLine() string {
	switch model.repositoryMode {
	case "existing":
		return fmt.Sprintf("Use existing local checkout at %s.", sanitizeTerminalLine(model.repositoryInputs[0].Value()))
	case "github":
		path := firstNonEmpty(sanitizeTerminalLine(model.repositoryInputs[0].Value()), "the profile default path under Servestead's config directory")
		return fmt.Sprintf("Clone %s into %s, then use the committed configuration.", sanitizeTerminalLine(model.repositoryInputs[1].Value()), path)
	default:
		path := sanitizeTerminalLine(model.repositoryInputs[0].Value())
		if path == "" {
			return "Create and commit a new local configuration repository at the profile default path under Servestead's config directory."
		}
		return fmt.Sprintf("Create and commit a new local configuration repository at %s.", path)
	}
}

func reviewTargetLabel(options setupCLIOptions) string {
	label := sanitizeTerminalLine(firstNonEmpty(options.Name, options.ProfileID, options.IP, "(unnamed profile)"))
	ip := sanitizeTerminalLine(options.IP)
	if ip != "" && label != ip {
		return fmt.Sprintf("%s (%s)", label, ip)
	}
	return label
}

func stageUsesRepository(stage string) bool {
	return stage == "platform" || stage == "observability" || stage == "stacks" || strings.HasPrefix(stage, setupStageStackPrefix)
}

func (model profileSetupModel) deleteConfirmView() string {
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
		return setupNoProfileSelectedView
	}
	profile := model.profiles[model.selectedIndex].Profile
	name := safeProfileConfirmationName(profile)
	want := "delete " + name
	path := "unavailable"
	if resolver, ok := model.profileStore.(interface {
		ProfileDirectory(string) (string, error)
	}); ok {
		if resolved, err := resolver.ProfileDirectory(profile.ID); err == nil {
			path = resolved
		}
	}
	var builder strings.Builder
	builder.WriteString("Delete saved profile?\n\n")
	builder.WriteString(fmt.Sprintf("Profile: %s\n", name))
	builder.WriteString(fmt.Sprintf("IP:      %s\n", sanitizeTerminalLine(profile.IP)))
	builder.WriteString(fmt.Sprintf("Path:    %s\n", sanitizeTerminalLine(path)))
	builder.WriteString("\nThis removes only local profile files, saved secrets, state, and run logs. It does not change the remote server.\n\n")
	if model.deleteProfilePending {
		builder.WriteString("Deleting local profile data...")
		return builder.String()
	}
	builder.WriteString(fmt.Sprintf("Type %q to continue:\n", want))
	builder.WriteString(model.deleteProfileInput.View())
	return builder.String()
}

func safeProfileConfirmationName(profile Profile) string {
	if name := sanitizeTerminalLine(profile.Name); name != "" {
		return name
	}
	if address := sanitizeTerminalLine(profile.IP); address != "" {
		return address
	}
	return "(unnamed profile)"
}

func safeStackConfirmationName(name string) string {
	if safeName := sanitizeTerminalLine(name); safeName != "" {
		return safeName
	}
	return "(unnamed stack)"
}

type profileSetupHelp struct {
	screen               profileSetupScreen
	hasProfile           bool
	hasPangolinAccess    bool
	hasCloud             bool
	stackComposeManual   bool
	stackEnvironmentMode stackEnvironmentMode
	stackEditorFocus     int
}

type profileSetupHelpView struct {
	base     profileSetupHelp
	expanded bool
	width    int
}

func (view profileSetupHelpView) ShortHelp() []key.Binding {
	bindings := view.compactShortHelp(view.base.ShortHelp())
	if !profileSetupScreenAcceptsText(view.base.screen) {
		bindings = append(bindings, key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "more")))
	}
	return bindings
}

func (view profileSetupHelpView) FullHelp() [][]key.Binding {
	bindings := append([]key.Binding(nil), view.base.ShortHelp()...)
	if !profileSetupScreenAcceptsText(view.base.screen) {
		label := "more"
		if view.expanded {
			label = "less"
		}
		bindings = append(bindings, key.NewBinding(key.WithKeys("?"), key.WithHelp("?", label)))
	}
	if view.width < 100 || len(bindings) < 6 {
		return [][]key.Binding{bindings}
	}
	middle := (len(bindings) + 1) / 2
	return [][]key.Binding{bindings[:middle], bindings[middle:]}
}

func (view profileSetupHelpView) compactShortHelp(bindings []key.Binding) []key.Binding {
	if view.width <= 0 {
		return bindings
	}
	reserve := 0
	if !profileSetupScreenAcceptsText(view.base.screen) {
		reserve = len(" • ? more")
	}
	available := max(1, view.width-reserve)
	compact := make([]key.Binding, 0, len(bindings))
	used := 0
	for _, binding := range bindings {
		help := binding.Help()
		itemWidth := lipgloss.Width(help.Key) + 1 + lipgloss.Width(help.Desc)
		if len(compact) > 0 {
			itemWidth += len(" • ")
		}
		if used+itemWidth > available {
			break
		}
		compact = append(compact, binding)
		used += itemWidth
	}
	return compact
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
			key.NewBinding(key.WithKeys("pgup", "pgdown"), key.WithHelp("pgup/pgdn", "scroll")),
			key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "review")),
			key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "run stage")),
			key.NewBinding(key.WithKeys("h"), key.WithHelp("h", "history")),
			key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "check Pangolin")),
			key.NewBinding(key.WithKeys("j", "k"), key.WithHelp("j/k", "stage")),
			key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit")),
			key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "advanced")),
			key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "stacks")),
			key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "GitHub token")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
			key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		}
		if helpMap.hasProfile {
			bindings = append(bindings, key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "fresh")))
			bindings = append(bindings, key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "delete")))
		}
		if helpMap.hasPangolinAccess {
			bindings = append(bindings, key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "reveal")))
		}
		if helpMap.hasCloud {
			bindings = append(bindings, key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "cloud")))
		}
		return bindings
	case profileSetupScreenRunHistory:
		return []key.Binding{
			key.NewBinding(key.WithKeys("j", "k"), key.WithHelp("j/k", "select")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "inspect")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
			key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		}
	case profileSetupScreenRunDetail:
		return []key.Binding{
			key.NewBinding(key.WithKeys("j", "k"), key.WithHelp("j/k", "scroll")),
			key.NewBinding(key.WithKeys("pgup", "pgdown"), key.WithHelp("pgup/pgdn", "page")),
			key.NewBinding(key.WithKeys("home", "end"), key.WithHelp("home/end", "jump")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
			key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		}
	case profileSetupScreenGitHubToken:
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "save")),
			key.NewBinding(key.WithKeys(setupKeyCtrlE), key.WithHelp(setupKeyCtrlE, "store env")),
			key.NewBinding(key.WithKeys(setupKeyCtrlX), key.WithHelp(setupKeyCtrlX, "remove")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		}
	case profileSetupScreenIntake:
		return profileIntakeHelp(helpMap.hasProfile)
	case profileSetupScreenAdvanced:
		return profileAdvancedHelp(helpMap.hasProfile)
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
		if helpMap.stackComposeManual {
			return []key.Binding{
				key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "inspect")),
				key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "browser")),
			}
		}
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select/open")),
			key.NewBinding(key.WithKeys("j", "k"), key.WithHelp("j/k", "select")),
			key.NewBinding(key.WithKeys("h"), key.WithHelp("h", "parent")),
			key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "type path")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		}
	case profileSetupScreenStackServices:
		return []key.Binding{
			key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "public/private")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "configure")),
			key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "another route")),
			key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "next")),
			key.NewBinding(key.WithKeys("j", "k"), key.WithHelp("j/k", "service")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		}
	case profileSetupScreenStackEditor:
		if helpMap.stackEditorFocus == 0 {
			return []key.Binding{
				key.NewBinding(key.WithKeys("pgup", "pgdown"), key.WithHelp("pgup/pgdn", "scroll")),
				key.NewBinding(key.WithKeys(setupKeyCtrlS), key.WithHelp(setupKeyCtrlS, "save")),
				key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "routes")),
				key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
			}
		}
		return []key.Binding{
			key.NewBinding(key.WithKeys("pgup", "pgdown"), key.WithHelp("pgup/pgdn", "scroll")),
			key.NewBinding(key.WithKeys(setupKeyCtrlS), key.WithHelp(setupKeyCtrlS, "save")),
			key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add route")),
			key.NewBinding(key.WithKeys("e", "enter"), key.WithHelp("e", "edit route")),
			key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "remove route")),
			key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "runtime env")),
			key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "name/routes")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		}
	case profileSetupScreenStackResourceEditor:
		return []key.Binding{
			key.NewBinding(key.WithKeys(setupKeyCtrlS), key.WithHelp(setupKeyCtrlS, "save route")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "next/save")),
			key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "field")),
			key.NewBinding(key.WithKeys(setupKeyCtrlX), key.WithHelp(setupKeyCtrlX, "advanced")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		}
	case profileSetupScreenStackEnvironment:
		if helpMap.stackEnvironmentMode == stackEnvironmentManual {
			return []key.Binding{
				key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "use file")),
				key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "browser")),
			}
		}
		if helpMap.stackEnvironmentMode == stackEnvironmentBrowse {
			return []key.Binding{
				key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select/open")),
				key.NewBinding(key.WithKeys("j", "k"), key.WithHelp("j/k", "select")),
				key.NewBinding(key.WithKeys("h"), key.WithHelp("h", "parent")),
				key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "type path")),
				key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "choices")),
			}
		}
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "choose")),
			key.NewBinding(key.WithKeys("j", "k"), key.WithHelp("j/k", "select")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		}
	case profileSetupScreenStackReview:
		return []key.Binding{
			key.NewBinding(key.WithKeys("pgup", "pgdown"), key.WithHelp("pgup/pgdn", "scroll")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "save locally")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		}
	case profileSetupScreenStackDeleteConfirm:
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "remove")),
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
	case profileSetupScreenCloud:
		return []key.Binding{
			key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "restart")),
			key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "destroy")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
			key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		}
	case profileSetupScreenCloudConfirm:
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "run")),
			key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "field")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		}
	case profileSetupScreenCloudRunning:
		return []key.Binding{
			key.NewBinding(key.WithKeys(setupKeyCtrlC), key.WithHelp(setupKeyCtrlC, "cancel")),
			key.NewBinding(key.WithKeys("q", "esc"), key.WithHelp("q/esc", "cancel")),
		}
	case profileSetupScreenCloudSaveRecovery:
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "retry local save")),
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
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "delete")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		}
	default:
		return nil
	}
}

func (helpMap profileSetupHelp) FullHelp() [][]key.Binding {
	return [][]key.Binding{helpMap.ShortHelp()}
}

func profileIntakeHelp(hasProfile bool) []key.Binding {
	bindings := []key.Binding{
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "next")),
		key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "field")),
		key.NewBinding(key.WithKeys(setupKeyCtrlA), key.WithHelp(setupKeyCtrlA, "advanced")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
	}
	return prependProfileSaveHelp(bindings, hasProfile)
}

func profileAdvancedHelp(hasProfile bool) []key.Binding {
	bindings := []key.Binding{
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "review")),
		key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "field")),
		key.NewBinding(key.WithKeys(setupKeyCtrlE), key.WithHelp(setupKeyCtrlE, "intake")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
	}
	return prependProfileSaveHelp(bindings, hasProfile)
}

func prependProfileSaveHelp(bindings []key.Binding, hasProfile bool) []key.Binding {
	if !hasProfile {
		return bindings
	}
	return append([]key.Binding{key.NewBinding(key.WithKeys(setupKeyCtrlS), key.WithHelp(setupKeyCtrlS, "save"))}, bindings...)
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
	program := tea.NewProgram(model, tea.WithOutput(output))
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
	width     int
	height    int
	err       string
	done      bool
	cancelled bool
}

func newFullRunModel(config setupConfig) fullRunModel {
	inputs := newSetupInputs([]setupInputField{
		{label: setupPrivateKeyLabel, placeholder: defaultKeygenConfig().Path, value: firstNonEmpty(config.PrivateKeyPath, defaultKeygenConfig().Path)},
		{label: setupBaseDomainLabel, placeholder: setupExampleDomainPlaceholder, value: config.BaseDomain},
		{label: setupLetsEncryptEmailLabel, placeholder: setupAdminEmailPlaceholder, value: config.LetsEncryptEmail},
	})
	inputs[0].Focus()
	return fullRunModel{
		config: config,
		inputs: inputs,
		width:  profileSetupMinWidth,
		height: profileSetupMinHeight,
	}
}

func (model fullRunModel) Init() tea.Cmd {
	return textinput.Blink
}

func (model fullRunModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		model.width = size.Width
		model.height = size.Height
		fieldWidth := max(10, size.Width-34)
		for index := range model.inputs {
			model.inputs[index].SetWidth(fieldWidth)
		}
		return model, nil
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return model, nil
	}
	switch key.String() {
	case setupKeyCtrlC, "esc":
		model.cancelled = true
		return model, tea.Quit
	}
	if model.setupTerminalTooSmall() {
		return model, nil
	}
	switch key.String() {
	case "tab", "down":
		model.inputs[model.focus].Blur()
		model.focus = (model.focus + 1) % len(model.inputs)
		model.inputs[model.focus].Focus()
		return model, nil
	case setupKeyShiftTab, "up":
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
	model.inputs[model.focus], cmd = updateSetupTextInput(model.inputs[model.focus], key)
	return model, cmd
}

func (model fullRunModel) View() tea.View {
	if model.setupTerminalTooSmall() {
		return setupMinimumSizeView("Servestead full setup", model.width, model.height)
	}
	var builder strings.Builder
	builder.WriteString(setupTitleStyle.Render("Servestead full setup"))
	builder.WriteString("\n\n")
	builder.WriteString(fmt.Sprintf("Profile target: %s\n", sanitizeTerminalLine(model.config.Host)))
	builder.WriteString("Enter the values needed before the full setup run starts.\n\n")
	for _, input := range model.inputs {
		builder.WriteString(input.View())
		builder.WriteString("\n")
	}
	if model.err != "" {
		builder.WriteString("\n")
		builder.WriteString(setupErrorStyle.Render(sanitizeTerminalText(model.err)))
		builder.WriteString("\n")
	}
	builder.WriteString("\n")
	builder.WriteString(setupHelpStyle.Render("Enter advances. Tab changes field. Esc cancels."))
	return altScreenView(builder.String())
}

func (model fullRunModel) setupTerminalTooSmall() bool {
	return model.width < profileSetupMinWidth || model.height < profileSetupMinHeight
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
	return prepareResolvedProfileSetup(options, store, output, profile, state)
}

func prepareResolvedProfileSetup(
	options setupCLIOptions,
	store ProfileStore,
	output io.Writer,
	profile Profile,
	state ProfileState,
) (Profile, ProfileState, setupConfig, error) {
	applySetupOptionsToProfile(&profile, options)

	config, profile, err := completeProfileSetupConfig(profileSetupConfig(profile, options), profile, options, output)
	if err != nil {
		return Profile{}, ProfileState{}, setupConfig{}, err
	}
	if err := validateFullRunConfig(config); err != nil {
		return Profile{}, ProfileState{}, setupConfig{}, err
	}

	secrets, err := prepareProfileSetupSecrets(store, profile.ID, state, options, false)
	if err != nil {
		return Profile{}, ProfileState{}, setupConfig{}, err
	}
	applyProfileSecretsToConfig(&config, secrets)
	applyProfileSetupConfigToProfile(&profile, config)
	if err := store.Save(profile, state); err != nil {
		return Profile{}, ProfileState{}, setupConfig{}, err
	}
	return profile, state, config, nil
}

func prepareProfileSetupWithOperationLock(
	options setupCLIOptions,
	store ProfileStore,
	output io.Writer,
) (Profile, ProfileState, setupConfig, profileOperationLock, error) {
	if options.IP == "" && options.ProfileID == "" {
		return Profile{}, ProfileState{}, setupConfig{}, nil, errors.New("--ip is required for profile-aware setup")
	}
	profile, state, lock, err := resolveAndLockSetupProfile(options, store)
	if err != nil {
		return Profile{}, ProfileState{}, setupConfig{}, nil, err
	}
	preparedProfile, preparedState, config, err := prepareResolvedProfileSetup(options, store, output, profile, state)
	if err != nil {
		releaseProfileOperationLock(lock)
		return Profile{}, ProfileState{}, setupConfig{}, nil, err
	}
	return preparedProfile, preparedState, config, lock, nil
}

func resolveAndLockSetupProfile(options setupCLIOptions, store ProfileStore) (Profile, ProfileState, profileOperationLock, error) {
	if options.ProfileID != "" && !options.Fresh {
		return lockAndLoadProfile(store, options.ProfileID)
	}
	profile, _, err := resolveSetupProfile(options, store)
	if err != nil {
		return Profile{}, ProfileState{}, nil, err
	}
	return lockAndLoadProfile(store, profile.ID)
}

func profileSetupConfig(profile Profile, options setupCLIOptions) setupConfig {
	privateKeyPath := expandUserPath(profile.PrivateKeyPath)
	return setupConfig{
		Mode:                 setupModeFullRun,
		Host:                 profile.IP,
		InitialSSHUser:       profile.InitialSSHUser,
		AdminUser:            profile.AdminUser,
		PrivateKeyPath:       privateKeyPath,
		AdminPublicKeyPath:   publicKeyPath(privateKeyPath),
		BaseDomain:           profile.BaseDomain,
		LetsEncryptEmail:     profile.LetsEncryptEmail,
		PangolinAdminEmail:   firstNonEmpty(profile.PangolinAdminEmail, profile.LetsEncryptEmail),
		ProfileID:            profile.ID,
		ConfigRepositoryPath: profile.ConfigRepositoryPath,
		GitHubRepositoryURL:  options.GitHubRepositoryURL,
	}
}

func completeProfileSetupConfig(config setupConfig, profile Profile, options setupCLIOptions, output io.Writer) (setupConfig, Profile, error) {
	if config.BaseDomain != "" && config.LetsEncryptEmail != "" {
		return config, profile, nil
	}
	if options.Yes || !isInteractiveWriter(output) {
		return setupConfig{}, Profile{}, validateFullRunConfig(config)
	}
	config, err := collectFullRunConfig(output, config)
	if err != nil {
		return setupConfig{}, Profile{}, err
	}
	profile.BaseDomain = config.BaseDomain
	profile.LetsEncryptEmail = config.LetsEncryptEmail
	profile.PrivateKeyPath = config.PrivateKeyPath
	return config, profile, nil
}

func applyProfileSetupConfigToProfile(profile *Profile, config setupConfig) {
	profile.PangolinAdminEmail = config.PangolinAdminEmail
	profile.PrivateKeyPath = config.PrivateKeyPath
	profile.BaseDomain = config.BaseDomain
	profile.LetsEncryptEmail = config.LetsEncryptEmail
}

func prepareProfileStageSetup(options setupCLIOptions, store ProfileStore, stage string) (Profile, ProfileState, setupConfig, error) {
	if options.ProfileID == "" {
		return Profile{}, ProfileState{}, setupConfig{}, errors.New("a saved profile is required for one-time stage runs")
	}
	profile, state, err := store.Load(options.ProfileID)
	if err != nil {
		return Profile{}, ProfileState{}, setupConfig{}, err
	}
	return prepareLoadedProfileStageSetup(options, store, stage, profile, state)
}

func prepareLoadedProfileStageSetup(
	options setupCLIOptions,
	store ProfileStore,
	stage string,
	profile Profile,
	state ProfileState,
) (Profile, ProfileState, setupConfig, error) {
	applySetupOptionsToProfile(&profile, options)
	config := profileStageSetupConfig(profile, options, stage)
	if err := validateStageRunConfig(stage, config); err != nil {
		return Profile{}, ProfileState{}, setupConfig{}, err
	}
	if stageNeedsProfileSecrets(stage) {
		secrets, err := prepareProfileSetupSecrets(store, profile.ID, state, options, true)
		if err != nil {
			return Profile{}, ProfileState{}, setupConfig{}, err
		}
		applyProfileSecretsToConfig(&config, secrets)
	}
	if err := store.Save(profile, state); err != nil {
		return Profile{}, ProfileState{}, setupConfig{}, err
	}
	return profile, state, config, nil
}

func prepareProfileStageSetupWithOperationLock(
	options setupCLIOptions,
	store ProfileStore,
	stage string,
) (Profile, ProfileState, setupConfig, profileOperationLock, error) {
	if options.ProfileID == "" {
		return Profile{}, ProfileState{}, setupConfig{}, nil, errors.New("a saved profile is required for one-time stage runs")
	}
	profile, state, lock, err := lockAndLoadProfile(store, options.ProfileID)
	if err != nil {
		return Profile{}, ProfileState{}, setupConfig{}, nil, err
	}
	preparedProfile, preparedState, config, err := prepareLoadedProfileStageSetup(options, store, stage, profile, state)
	if err != nil {
		releaseProfileOperationLock(lock)
		return Profile{}, ProfileState{}, setupConfig{}, nil, err
	}
	return preparedProfile, preparedState, config, lock, nil
}

func profileStageSetupConfig(profile Profile, options setupCLIOptions, stage string) setupConfig {
	privateKeyPath := expandUserPath(firstNonEmpty(profile.PrivateKeyPath, defaultKeygenConfig().Path))
	return setupConfig{
		Mode:                 setupModeForStage(stage),
		Host:                 profile.IP,
		InitialSSHUser:       firstNonEmpty(profile.InitialSSHUser, "root"),
		AdminUser:            firstNonEmpty(profile.AdminUser, "servestead"),
		PrivateKeyPath:       privateKeyPath,
		AdminPublicKeyPath:   publicKeyPath(privateKeyPath),
		BaseDomain:           profile.BaseDomain,
		LetsEncryptEmail:     profile.LetsEncryptEmail,
		PangolinAdminEmail:   firstNonEmpty(profile.PangolinAdminEmail, profile.LetsEncryptEmail),
		ProfileID:            profile.ID,
		ConfigRepositoryPath: profile.ConfigRepositoryPath,
		GitHubRepositoryURL:  options.GitHubRepositoryURL,
	}
}

func stageNeedsProfileSecrets(stage string) bool {
	return stage == "proxy" || stage == "observability" || stage == "platform" || stage == "stacks" || strings.HasPrefix(stage, setupStageStackPrefix)
}

func prepareProfileSetupSecrets(store ProfileStore, profileID string, state ProfileState, options setupCLIOptions, forceSetupToken bool) (ProfileSecrets, error) {
	secrets, err := store.LoadSecrets(profileID)
	if err != nil {
		return ProfileSecrets{}, err
	}
	if err := secrets.EnsureServerSecret(); err != nil {
		return ProfileSecrets{}, fmt.Errorf("generate server secret: %w", err)
	}
	completed := completedSetupStages(state)
	passwordOverride := firstNonEmpty(options.PangolinAdminPassword, os.Getenv("PANGOLIN_ADMIN_PASSWORD"))
	if passwordOverride != "" {
		secrets.PangolinAdminPassword = passwordOverride
	}
	if completed["proxy"] && secrets.PangolinAdminPassword == "" {
		return ProfileSecrets{}, errors.New("existing Pangolin registration has no saved administrator password; enter it in Advanced setup or set PANGOLIN_ADMIN_PASSWORD once")
	}
	if passwordOverride == "" && !completed["proxy"] && !pangolinPasswordValid(secrets.PangolinAdminPassword) {
		secrets.PangolinAdminPassword = ""
	}
	if err := secrets.EnsureComposeWiringSecrets(); err != nil {
		return ProfileSecrets{}, fmt.Errorf("generate Pangolin wiring secrets: %w", err)
	}
	if forceSetupToken || secrets.PangolinSetupToken != "" || !completed["proxy"] {
		if err := secrets.EnsurePangolinSetupToken(); err != nil {
			return ProfileSecrets{}, fmt.Errorf("generate Pangolin setup token: %w", err)
		}
	}
	if err := store.SaveSecrets(profileID, secrets); err != nil {
		return ProfileSecrets{}, err
	}
	return secrets, nil
}

func applyProfileSecretsToConfig(config *setupConfig, secrets ProfileSecrets) {
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

func setupModeForStage(stage string) setupMode {
	if strings.HasPrefix(stage, setupStageStackPrefix) {
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
		return setupModeProxy
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
		return validateBootstrapStageConfig(config)
	case "harden", "network":
		return validateAdminStageConfig(config)
	case "proxy", "platform":
		return validateProxyStageConfig(stage, config)
	case "observability", "stacks":
		return validateRepositoryStageConfig(config, "repository synchronization")
	default:
		if isConfiguredStackStage(stage) {
			return validateRepositoryStageConfig(config, "stack deployment")
		}
		return fmt.Errorf("unknown setup stage: %s", stage)
	}
}

func validateBootstrapStageConfig(config setupConfig) error {
	if config.AdminPublicKeyPath == "" {
		return errors.New(setupAdminPublicKeyLabel + " is required for the bootstrap stage")
	}
	if !linuxUsername.MatchString(config.InitialSSHUser) || !linuxUsername.MatchString(config.AdminUser) {
		return errors.New("SSH users must be valid Linux usernames")
	}
	return nil
}

func validateAdminStageConfig(config setupConfig) error {
	if !linuxUsername.MatchString(config.AdminUser) {
		return errors.New("admin SSH user must be a valid Linux username")
	}
	return nil
}

func validateProxyStageConfig(stage string, config setupConfig) error {
	if config.BaseDomain == "" || config.LetsEncryptEmail == "" {
		return fmt.Errorf("profile domain and Let's Encrypt email are required for %s; edit the profile before running it", profileRunStageLabel(stage))
	}
	return validateProxyConfig(proxyConfig{
		Host:             config.Host,
		SSHUser:          config.AdminUser,
		PrivateKeyPath:   config.PrivateKeyPath,
		BaseDomain:       config.BaseDomain,
		LetsEncryptEmail: config.LetsEncryptEmail,
		ServerSecret:     firstNonEmpty(config.ServerSecret, setupGeneratedPlaceholder),
		SetupToken:       firstNonEmpty(config.PangolinSetupToken, "00000000000000000000000000000000"),
		AdminEmail:       firstNonEmpty(config.PangolinAdminEmail, config.LetsEncryptEmail),
	})
}

func validateRepositoryStageConfig(config setupConfig, label string) error {
	if config.BaseDomain == "" || config.PangolinAdminEmail == "" {
		return fmt.Errorf("profile domain and Pangolin administrator email are required for %s", label)
	}
	return nil
}

func isConfiguredStackStage(stage string) bool {
	return strings.HasPrefix(stage, setupStageStackPrefix) && stackSlugPattern.MatchString(strings.TrimPrefix(stage, setupStageStackPrefix))
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
	options.AdminUser = firstNonEmpty(options.AdminUser, source.AdminUser, "servestead")
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
		AdminUser:            firstNonEmpty(options.AdminUser, "servestead"),
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
	profile.AdminUser = firstNonEmpty(options.AdminUser, profile.AdminUser, "servestead")
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
		ServerSecret:     firstNonEmpty(config.ServerSecret, setupGeneratedPlaceholder),
		SetupToken:       firstNonEmpty(config.PangolinSetupToken, "00000000000000000000000000000000"),
		AdminEmail:       firstNonEmpty(config.PangolinAdminEmail, config.LetsEncryptEmail),
	})
}

func shouldUseProfileRunView(options setupCLIOptions, output io.Writer) bool {
	return !options.Yes && isInteractiveWriter(output)
}

type tuiPresentedError struct {
	err error
}

var (
	errReturnToSetup = errors.New("return to setup")
	errSetupQuit     = errors.New("setup quit")
)

func (presented tuiPresentedError) Error() string {
	return presented.err.Error()
}

func (presented tuiPresentedError) Unwrap() error {
	return presented.err
}

func runProfileSetupPlan(
	ctx context.Context,
	store ProfileStore,
	profile Profile,
	state ProfileState,
	config setupConfig,
	stdout io.Writer,
	stderr io.Writer,
	heldLocks ...profileOperationLock,
) error {
	var heldLock profileOperationLock
	if len(heldLocks) > 0 {
		heldLock = heldLocks[0]
	}
	lockedRun, lock, err := lockProfileSetupPlanRun(profileSetupPlanRun{
		store: store, profile: profile, state: state, config: config,
		stdout: stdout, stderr: stderr, lock: heldLock,
	})
	if err != nil {
		return err
	}
	defer releaseProfileOperationLock(lock)
	profile = lockedRun.profile
	state = lockedRun.state

	fmt.Fprintln(stdout, setupSelectedPlanHeader)
	fmt.Fprint(stdout, setupPlanSummary(config))
	fmt.Fprintln(stdout)
	if err := runPreflight(config, stdout); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Preparing the configuration repository before SSH execution...")
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
		store:        store,
		profile:      profile,
		state:        &state,
		runID:        runID,
		secretValues: setupConfigSecretValues(config),
	}

	stageRun := setupStageRun{
		profile: profile, config: config, runID: runID,
		reporter: reporter, stdout: stdout, stderr: stderr,
	}
	if err := runFullSetupStages(ctx, stageRun, completedStages); err != nil {
		reporter.finishRun(profileRunStatusForError(err), err, setupStageForError(err))
		if reporter.err != nil {
			return reporter.err
		}
		return err
	}
	reporter.finishRun(runStatusComplete, nil, "")
	if reporter.err != nil {
		return reporter.err
	}
	printSSHLoginGuidance(stdout, config)
	fmt.Fprintf(stdout, "\nProxy URL: https://pangolin.%s\n", config.BaseDomain)
	fmt.Fprintf(stdout, "Beszel URL: https://beszel.%s\nDozzle URL: https://dozzle.%s\nDockhand URL: https://dockhand.%s\n", config.BaseDomain, config.BaseDomain, config.BaseDomain)
	fmt.Fprintln(stdout, requiredDNSGuidance(config.BaseDomain, config.Host))
	fmt.Fprintf(stdout, "Retrieve Pangolin login with: servestead pangolin-credentials --profile %s\n", config.ProfileID)
	return nil
}

type profileSetupPlanRun struct {
	store   ProfileStore
	profile Profile
	state   ProfileState
	config  setupConfig
	stdout  io.Writer
	stderr  io.Writer
	lock    profileOperationLock
}

func lockProfileSetupPlanRun(run profileSetupPlanRun) (profileSetupPlanRun, profileOperationLock, error) {
	if run.lock != nil {
		return run, run.lock, nil
	}
	profile, state, lock, err := lockAndLoadProfile(run.store, firstNonEmpty(run.profile.ID, run.config.ProfileID))
	if err != nil {
		return profileSetupPlanRun{}, nil, err
	}
	run.profile = profile
	run.state = state
	run.lock = lock
	return run, lock, nil
}

func runProfileSetupPlanWithRunView(ctx context.Context, run profileSetupPlanRun, allowReturn bool) error {
	result, err := runProfileSetupWithRunView(ctx, run, "", allowReturn)
	if err != nil {
		return err
	}
	printSSHLoginGuidance(run.stdout, result.config)
	fmt.Fprintf(run.stdout, "\nProxy URL: https://pangolin.%s\n", result.config.BaseDomain)
	fmt.Fprintf(run.stdout, "Beszel URL: https://beszel.%s\nDozzle URL: https://dozzle.%s\nDockhand URL: https://dockhand.%s\n", result.config.BaseDomain, result.config.BaseDomain, result.config.BaseDomain)
	fmt.Fprintln(run.stdout, requiredDNSGuidance(result.config.BaseDomain, result.config.Host))
	fmt.Fprintf(run.stdout, "Retrieve Pangolin login with: servestead pangolin-credentials --profile %s\n", result.config.ProfileID)
	return nil
}

func runProfileSetupWithRunView(ctx context.Context, run profileSetupPlanRun, stage string, allowReturn bool) (profileRunModel, error) {
	lockedRun, lock, err := lockProfileSetupPlanRun(run)
	if err != nil {
		return profileRunModel{}, err
	}
	lock = guardProfileOperationLock(lock)
	defer releaseProfileOperationLock(lock)
	lockedRun.lock = lock
	run = lockedRun

	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	messages := make(chan tea.Msg, 256)
	model := newPreparingProfileRunModel(runContext, run, stage, messages, cancel, allowReturn)
	program := tea.NewProgram(model, tea.WithOutput(run.stderr))
	finalModel, err := program.Run()
	if err != nil {
		return profileRunModel{}, fmt.Errorf("run setup TUI: %w", err)
	}
	result, ok := finalModel.(profileRunModel)
	if !ok {
		return profileRunModel{}, errors.New("setup run TUI returned an unexpected model")
	}
	if result.returnToSetup {
		return result, errReturnToSetup
	}
	if result.cancelled {
		return result, errors.New("setup cancelled")
	}
	if result.err != nil {
		return result, tuiPresentedError{err: result.err}
	}
	if result.profileReporter != nil && result.profileReporter.err != nil {
		return result, tuiPresentedError{err: result.profileReporter.err}
	}
	return result, nil
}

func runProfileSetupStagePlan(ctx context.Context, run profileSetupPlanRun, stage string) error {
	lockedRun, lock, err := lockProfileSetupPlanRun(run)
	if err != nil {
		return err
	}
	defer releaseProfileOperationLock(lock)
	run = lockedRun

	fmt.Fprintf(run.stdout, "Selected one-time stage: %s\n\n", profileRunStageLabel(stage))
	if err := runPreflight(run.config, run.stdout); err != nil {
		return err
	}
	if stage == "observability" || stage == "platform" || stage == "stacks" || strings.HasPrefix(stage, setupStageStackPrefix) {
		fmt.Fprintln(run.stdout, "Preparing the configuration repository before SSH execution...")
		run.profile, run.config, err = prepareDeclarativeSetup(ctx, run.store, run.profile, run.state, run.config)
		if err != nil {
			return err
		}
		fmt.Fprintf(run.stdout, "Configuration repository ready: %s at %s\n\n", run.config.ConfigRepositoryPath, run.config.ConfigRepositoryCommit)
	}

	runID := newSetupRunID()
	run.state.ActiveRunID = runID
	run.state.Runs[runID] = newSetupRunForStage(runID, stage, completedSetupStages(run.state))
	if err := run.store.Save(run.profile, run.state); err != nil {
		return err
	}

	reporter := &profileRunReporter{
		store:        run.store,
		profile:      run.profile,
		state:        &run.state,
		runID:        runID,
		secretValues: setupConfigSecretValues(run.config),
	}
	stageRun := setupStageRun{
		profile: run.profile, config: run.config, runID: runID,
		reporter: reporter, stdout: run.stdout, stderr: run.stderr,
	}
	if err := runSetupStage(ctx, stageRun, stage); err != nil {
		reporter.finishRun(profileRunStatusForError(err), err, stage)
		if reporter.err != nil {
			return reporter.err
		}
		return err
	}
	if stage == "stacks" {
		run.state.StackRepositoryCommit = run.config.ConfigRepositoryCommit
	}
	reporter.finishRun(runStatusComplete, nil, "")
	if reporter.err != nil {
		return reporter.err
	}
	printStageCompletionGuidance(run.stdout, run.config, stage)
	return nil
}

func runProfileSetupStagePlanWithRunView(ctx context.Context, run profileSetupPlanRun, stage string, allowReturn bool) error {
	result, err := runProfileSetupWithRunView(ctx, run, stage, allowReturn)
	if err != nil {
		return err
	}
	printStageCompletionGuidance(run.stdout, result.config, stage)
	return nil
}

type profileFailureView struct {
	profile         Profile
	config          setupConfig
	completedStages map[string]bool
	stage           string
	output          string
	err             error
	tuiOutput       io.Writer
	allowReturn     bool
}

func newProfileFailureView(run profileSetupPlanRun, stage, output string, runErr error, allowReturn bool) profileFailureView {
	return profileFailureView{
		profile:         run.profile,
		config:          run.config,
		completedStages: completedSetupStages(run.state),
		stage:           stage,
		output:          output,
		err:             runErr,
		tuiOutput:       run.stderr,
		allowReturn:     allowReturn,
	}
}

func runProfileFailureView(view profileFailureView) error {
	model := newProfileRunFailureModel(view.profile, view.config, view.completedStages, view.stage, view.output, view.err, view.allowReturn)
	program := tea.NewProgram(model, tea.WithOutput(view.tuiOutput))
	finalModel, err := program.Run()
	if err != nil {
		return fmt.Errorf("run setup TUI: %w", err)
	}
	if _, ok := finalModel.(profileRunModel); !ok {
		return errors.New("setup run TUI returned an unexpected model")
	}
	result := finalModel.(profileRunModel)
	if result.returnToSetup {
		return errReturnToSetup
	}
	return tuiPresentedError{err: view.err}
}

func newProfileRunFailureModel(profile Profile, config setupConfig, completedStages map[string]bool, stage, output string, runErr error, allowReturn bool) profileRunModel {
	model := newProfileRunModel(profile, config, "preparation", completedStages, stage, nil, nil)
	model.done = true
	model.err = runErr
	model.allowReturn = allowReturn
	if stage != "" {
		model.setStageStatus(stage, stageStatusFailed)
	}
	appendProfileRunOutput(&model, output)
	model.appendRunLog("Run failed: " + runErr.Error())
	return model
}

func appendProfileRunOutput(model *profileRunModel, output string) {
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		model.appendRunLog(line)
	}
}

type setupStageRun struct {
	profile  Profile
	config   setupConfig
	runID    string
	reporter TaskReporter
	stdout   io.Writer
	stderr   io.Writer
}

func runFullSetupStages(ctx context.Context, run setupStageRun, completedStages map[string]bool) error {
	stages := []fullSetupStage{
		{key: "bootstrap", skip: "Step 1/5: bootstrap administrative access already complete; skipping.", run: func() error {
			return runBootstrapSetupStage(ctx, run, "Step 1/5: bootstrap administrative access.")
		}},
		{key: "harden", skip: "Step 2/5: harden server already complete; skipping.", run: func() error {
			return runHardeningSetupStage(ctx, run, "Step 2/5: harden server.")
		}},
		{key: "network", skip: "Step 3/5: configure Docker networking and UFW already complete; skipping.", run: func() error {
			return runNetworkSetupStage(ctx, run, "Step 3/5: configure Docker networking and UFW.")
		}},
		{key: "proxy", skip: "Step 4/5: deploy Pangolin and reverse proxy stack already complete; skipping.", run: func() error {
			return runProxySetupStage(ctx, run, "Step 4/5: deploy Pangolin and reverse proxy stack.")
		}},
		{key: "observability", skip: "Step 5/5: deploy observability stack already complete; skipping.", run: func() error {
			return runObservabilitySetupStage(ctx, run, "Step 5/5: deploy observability stack.", false)
		}},
	}
	for _, stage := range stages {
		if err := runFullSetupStage(stage, completedStages, run.stdout); err != nil {
			return setupStageExecutionError{stage: stage.key, err: err}
		}
	}
	return runFullConfiguredStackStages(ctx, run, completedStages)
}

func runSetupStage(ctx context.Context, run setupStageRun, stage string) error {
	switch stage {
	case "stacks":
		return runStackRepositorySyncStage(ctx, run)
	case "platform":
		return runPlatformSetupStage(ctx, run)
	case "bootstrap":
		return runBootstrapSetupStage(ctx, run, "One-time stage: bootstrap administrative access.")
	case "harden":
		return runHardeningSetupStage(ctx, run, "One-time stage: harden server.")
	case "network":
		return runNetworkSetupStage(ctx, run, "One-time stage: configure Docker networking and UFW.")
	case "proxy":
		return runProxySetupStage(ctx, run, "One-time stage: deploy Pangolin and reverse proxy stack.")
	case "observability":
		return runObservabilitySetupStage(ctx, run, "One-time stage: deploy observability stack.", true)
	default:
		stack, ok := configuredStackForStage(run.config.Stacks, stage)
		if ok {
			return runConfiguredStackStage(ctx, run, stack)
		}
		if strings.HasPrefix(stage, setupStageStackPrefix) {
			return fmt.Errorf("stack %q is not present in the committed configuration", strings.TrimPrefix(stage, setupStageStackPrefix))
		}
		return fmt.Errorf("unknown setup stage: %s", stage)
	}
}

type fullSetupStage struct {
	key  string
	skip string
	run  func() error
}

type setupStageExecutionError struct {
	stage string
	err   error
}

func (failure setupStageExecutionError) Error() string {
	return failure.err.Error()
}

func (failure setupStageExecutionError) Unwrap() error {
	return failure.err
}

func setupStageForError(err error) string {
	var failure setupStageExecutionError
	if errors.As(err, &failure) {
		return failure.stage
	}
	return ""
}

func runFullSetupStage(stage fullSetupStage, completedStages map[string]bool, stdout io.Writer) error {
	if completedStages[stage.key] {
		fmt.Fprintln(setupStageWriter(stdout, stage.key, "stdout"), stage.skip)
		return nil
	}
	return stage.run()
}

func runFullConfiguredStackStages(ctx context.Context, run setupStageRun, completedStages map[string]bool) error {
	for _, stack := range run.config.Stacks {
		stackStage := setupStageStackPrefix + stack.Name
		if completedStages[stackStage] {
			fmt.Fprintf(setupStageWriter(run.stdout, stackStage, "stdout"), "Standalone %s stack already complete; skipping.\n", stack.Name)
			continue
		}
		if err := runConfiguredStackStage(ctx, run, stack); err != nil {
			return setupStageExecutionError{stage: stackStage, err: err}
		}
	}
	return nil
}

func runBootstrapSetupStage(ctx context.Context, run setupStageRun, message string) error {
	adminPublicKey, err := os.ReadFile(run.config.AdminPublicKeyPath)
	if err != nil {
		return fmt.Errorf("read admin public key: %w", err)
	}
	stageStdout := setupStageWriter(run.stdout, "bootstrap", "stdout")
	stageStderr := setupStageWriter(run.stderr, "bootstrap", "stderr")
	bootstrapConfig := bootstrapConfig{
		Host:               run.config.Host,
		SSHUser:            run.config.InitialSSHUser,
		AdminUser:          run.config.AdminUser,
		AdminPublicKeyPath: run.config.AdminPublicKeyPath,
		PrivateKeyPath:     run.config.PrivateKeyPath,
	}
	fmt.Fprintln(stageStdout, message)
	client, err := newBootstrapRemoteClient(ctx, bootstrapConfig, stageStdout, stageStderr)
	if err != nil {
		return err
	}
	if err := runBootstrapStepsWithReporter(ctx, client, bootstrapConfig, strings.TrimSpace(string(adminPublicKey)), run.runID, run.reporter, stageStdout); err != nil {
		_ = client.Close()
		return fmt.Errorf("bootstrap failed: %w", err)
	}
	return client.Close()
}

func runHardeningSetupStage(ctx context.Context, run setupStageRun, message string) error {
	stageStdout := setupStageWriter(run.stdout, "harden", "stdout")
	stageStderr := setupStageWriter(run.stderr, "harden", "stderr")
	hardeningConfig := hardeningConfig{Host: run.profile.IP, SSHUser: run.config.AdminUser, PrivateKeyPath: run.config.PrivateKeyPath}
	fmt.Fprintln(stageStdout, message)
	client, err := newHardeningRemoteClient(ctx, hardeningConfig, stageStdout, stageStderr)
	if err != nil {
		return err
	}
	if err := runHardeningStepsWithReporter(ctx, client, hardeningConfig, run.runID, run.reporter, stageStdout); err != nil {
		_ = client.Close()
		return fmt.Errorf("hardening failed: %w", err)
	}
	return client.Close()
}

func runNetworkSetupStage(ctx context.Context, run setupStageRun, message string) error {
	sshPort, err := sshPortForHost(run.config.Host)
	if err != nil {
		return err
	}
	stageStdout := setupStageWriter(run.stdout, "network", "stdout")
	stageStderr := setupStageWriter(run.stderr, "network", "stderr")
	networkConfig := networkConfig{Host: run.profile.IP, SSHUser: run.config.AdminUser, SSHPort: sshPort, PrivateKeyPath: run.config.PrivateKeyPath}
	fmt.Fprintln(stageStdout, message)
	client, err := newNetworkRemoteClient(ctx, networkConfig, stageStdout, stageStderr)
	if err != nil {
		return err
	}
	if err := runNetworkStepsWithReporter(ctx, client, networkConfig, run.runID, run.reporter, stageStdout); err != nil {
		_ = client.Close()
		return fmt.Errorf("network configuration failed: %w", err)
	}
	return client.Close()
}

func runProxySetupStage(ctx context.Context, run setupStageRun, message string) error {
	stageStdout := setupStageWriter(run.stdout, "proxy", "stdout")
	stageStderr := setupStageWriter(run.stderr, "proxy", "stderr")
	proxyConfig := setupProxyConfig(run.profile, run.config)
	fmt.Fprintln(stageStdout, message)
	client, err := newProxyRemoteClient(ctx, proxyConfig, stageStdout, stageStderr)
	if err != nil {
		return err
	}
	if err := runProxyStepsWithReporter(ctx, client, proxyConfig, run.runID, run.reporter, stageStdout); err != nil {
		_ = client.Close()
		return fmt.Errorf("proxy deployment failed: %w", err)
	}
	return client.Close()
}

func setupProxyConfig(profile Profile, config setupConfig) proxyConfig {
	return proxyConfig{
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
}

func runObservabilitySetupStage(ctx context.Context, run setupStageRun, message string, includeStacks bool) error {
	stageStdout := setupStageWriter(run.stdout, "observability", "stdout")
	stageStderr := setupStageWriter(run.stderr, "observability", "stderr")
	observabilityConfig := setupObservabilityConfig(run.profile, run.config, includeStacks)
	fmt.Fprintln(stageStdout, message)
	client, err := newObservabilityRemoteClient(ctx, observabilityConfig, stageStdout, stageStderr)
	if err != nil {
		return err
	}
	if err := runObservabilityStepsWithReporter(ctx, client, observabilityConfig, run.runID, run.reporter, stageStdout); err != nil {
		_ = client.Close()
		return fmt.Errorf("observability deployment failed: %w", err)
	}
	return client.Close()
}

func setupObservabilityConfig(profile Profile, config setupConfig, includeStacks bool) observabilityConfig {
	observabilityConfig := observabilityConfig{
		ProfileID: firstNonEmpty(config.ProfileID, profile.ID),
		Host:      profile.IP, SSHUser: config.AdminUser, PrivateKeyPath: config.PrivateKeyPath,
		BaseDomain: config.BaseDomain, AdminEmail: config.PangolinAdminEmail,
		AdminPassword: config.BeszelAdminPassword, PangolinPassword: config.PangolinAdminPassword, SystemToken: config.BeszelSystemToken,
		HubPrivateKey: config.BeszelHubPrivateKey, HubPublicKey: config.BeszelHubPublicKey,
		RepositoryCommit: config.ConfigRepositoryCommit, RepositoryBranch: config.ConfigRepositoryBranch, RepositoryOrigin: config.ConfigRepositoryOrigin,
		RepositoryCompose: config.ConfigRepositoryCompose, RepositorySHA256: config.ConfigRepositorySHA256, GitHubToken: config.GitHubToken,
	}
	if includeStacks {
		observabilityConfig.Stacks = config.Stacks
	}
	return observabilityConfig
}

func runStackRepositorySyncStage(ctx context.Context, run setupStageRun) error {
	stage := "stacks"
	stageStdout := setupStageWriter(run.stdout, stage, "stdout")
	stageStderr := setupStageWriter(run.stderr, stage, "stderr")
	syncConfig := setupObservabilityConfig(run.profile, run.config, true)
	fmt.Fprintln(stageStdout, "One-time action: synchronize committed stack configuration.")
	client, err := newObservabilityRemoteClient(ctx, syncConfig, stageStdout, stageStderr)
	if err != nil {
		return err
	}
	tasks := stackRepositoryReconcileTasks(syncConfig, firstNonEmpty(run.config.AdminUser, "root"))
	if err := runTasksWithReporter(ctx, client, run.config.AdminUser, taskRunOptions{runID: run.runID, stage: stage, tasks: tasks, progress: stageStdout, reporter: run.reporter}); err != nil {
		_ = client.Close()
		return fmt.Errorf("stack repository synchronization failed: %w", err)
	}
	return client.Close()
}

func runPlatformSetupStage(ctx context.Context, run setupStageRun) error {
	fmt.Fprintln(setupStageWriter(run.stdout, "platform", "stdout"), "One-time action: configure Network, Proxy, and Observability.")
	if err := runNetworkSetupStage(ctx, run, "One-time stage: configure Docker networking and UFW."); err != nil {
		return err
	}
	if err := runProxySetupStage(ctx, run, "One-time stage: deploy Pangolin and reverse proxy stack."); err != nil {
		return err
	}
	run.config.Stacks = nil
	return runObservabilitySetupStage(ctx, run, "One-time stage: deploy observability stack.", false)
}

func configuredStackForStage(stacks []configuredStack, stage string) (configuredStack, bool) {
	if !strings.HasPrefix(stage, setupStageStackPrefix) {
		return configuredStack{}, false
	}
	name := strings.TrimPrefix(stage, setupStageStackPrefix)
	for _, stack := range stacks {
		if stack.Name == name {
			return stack, true
		}
	}
	return configuredStack{}, false
}

func runConfiguredStackStage(ctx context.Context, run setupStageRun, stack configuredStack) error {
	stage := setupStageStackPrefix + stack.Name
	stageStdout := setupStageWriter(run.stdout, stage, "stdout")
	stageStderr := setupStageWriter(run.stderr, stage, "stderr")
	observabilityConfig := observabilityConfig{
		ProfileID: firstNonEmpty(run.config.ProfileID, run.profile.ID),
		Host:      run.profile.IP, SSHUser: run.config.AdminUser, PrivateKeyPath: run.config.PrivateKeyPath,
		BaseDomain: run.config.BaseDomain, AdminEmail: run.config.PangolinAdminEmail,
		PangolinPassword: run.config.PangolinAdminPassword, RepositoryCommit: run.config.ConfigRepositoryCommit,
		RepositoryBranch: run.config.ConfigRepositoryBranch, RepositoryOrigin: run.config.ConfigRepositoryOrigin,
		RepositoryCompose: run.config.ConfigRepositoryCompose, RepositorySHA256: run.config.ConfigRepositorySHA256,
		GitHubToken: run.config.GitHubToken,
	}
	fmt.Fprintf(stageStdout, "One-time action: deploy standalone %s stack.\n", stack.Name)
	client, err := newObservabilityRemoteClient(ctx, observabilityConfig, stageStdout, stageStderr)
	if err != nil {
		return err
	}
	tasks := configuredStackTasks(observabilityConfig, stack, firstNonEmpty(run.config.AdminUser, "root"))
	if err := runTasksWithReporter(ctx, client, run.config.AdminUser, taskRunOptions{runID: run.runID, stage: stage, tasks: tasks, progress: stageStdout, reporter: run.reporter}); err != nil {
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
		fmt.Fprintf(stdout, "Retrieve Pangolin login with: servestead pangolin-credentials --profile %s\n", config.ProfileID)
	case "observability":
		fmt.Fprintf(stdout, "\nBeszel URL: https://beszel.%s\nDozzle URL: https://dozzle.%s\nDockhand URL: https://dockhand.%s\n", config.BaseDomain, config.BaseDomain, config.BaseDomain)
	case "platform":
		fmt.Fprintf(stdout, "\nProxy URL: https://pangolin.%s\n", config.BaseDomain)
		fmt.Fprintf(stdout, "Beszel URL: https://beszel.%s\nDozzle URL: https://dozzle.%s\nDockhand URL: https://dockhand.%s\n", config.BaseDomain, config.BaseDomain, config.BaseDomain)
		fmt.Fprintln(stdout, requiredDNSGuidance(config.BaseDomain, config.Host))
	case "stacks":
		fmt.Fprintf(stdout, "\nStack repository synchronized at %s.\n", config.ConfigRepositoryCommit)
	default:
		if strings.HasPrefix(stage, setupStageStackPrefix) {
			fmt.Fprintf(stdout, "\nStandalone stack %s deployed from committed configuration.\n", strings.TrimPrefix(stage, setupStageStackPrefix))
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
	mu       sync.Mutex
	messages chan<- tea.Msg
	dropped  uint64
}

func (reporter *profileRunUIReporter) Report(event TaskEvent) {
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	message := profileRunEventMsg{event: event}
	if event.Type == TaskLogLine {
		if !reporter.flushDroppedLogMarker(event, false) {
			reporter.dropped++
			return
		}
		select {
		case reporter.messages <- message:
		default:
			reporter.dropped++
		}
		return
	}
	reporter.flushDroppedLogMarker(event, true)
	reporter.messages <- message
}

func (reporter *profileRunUIReporter) flushDroppedLogMarker(event TaskEvent, wait bool) bool {
	if reporter.dropped == 0 {
		return true
	}
	noun := "lines"
	if reporter.dropped == 1 {
		noun = "line"
	}
	marker := profileRunEventMsg{event: TaskEvent{
		Type:   TaskLogLine,
		RunID:  event.RunID,
		Stage:  event.Stage,
		Stream: "servestead",
		Line:   fmt.Sprintf("[%d live log %s omitted; see saved run history]", reporter.dropped, noun),
		Time:   event.Time,
	}}
	if wait {
		reporter.messages <- marker
		reporter.dropped = 0
		return true
	}
	select {
	case reporter.messages <- marker:
		reporter.dropped = 0
		return true
	default:
		return false
	}
}

type profileRunOutput struct {
	reporter TaskReporter
	runID    string
	writers  *profileRunWriterSet
}

func (output profileRunOutput) Write(data []byte) (int, error) {
	writer := output.WriterForStage("", "stdout")
	return writer.Write(data)
}

func (output profileRunOutput) WriterForStage(stage string, stream string) io.Writer {
	if output.writers == nil {
		return &profileRunLogWriter{reporter: output.reporter, runID: output.runID, stage: stage, stream: stream}
	}
	return output.writers.writer(output.reporter, output.runID, stage, stream)
}

func newProfileRunOutput(reporter TaskReporter, runID string) profileRunOutput {
	return profileRunOutput{
		reporter: reporter,
		runID:    runID,
		writers:  &profileRunWriterSet{writers: map[string]*profileRunLogWriter{}},
	}
}

func (output profileRunOutput) Flush() {
	if output.writers != nil {
		output.writers.flush()
	}
}

type profileRunWriterSet struct {
	mu      sync.Mutex
	writers map[string]*profileRunLogWriter
}

func (set *profileRunWriterSet) writer(reporter TaskReporter, runID, stage, stream string) *profileRunLogWriter {
	key := stage + "\x00" + stream
	set.mu.Lock()
	defer set.mu.Unlock()
	if writer := set.writers[key]; writer != nil {
		return writer
	}
	writer := &profileRunLogWriter{reporter: reporter, runID: runID, stage: stage, stream: stream}
	set.writers[key] = writer
	return writer
}

func (set *profileRunWriterSet) flush() {
	set.mu.Lock()
	writers := make([]*profileRunLogWriter, 0, len(set.writers))
	for _, writer := range set.writers {
		writers = append(writers, writer)
	}
	set.mu.Unlock()
	for _, writer := range writers {
		writer.Flush()
	}
}

type profileRunLogWriter struct {
	mu            sync.Mutex
	reporter      TaskReporter
	runID         string
	stage         string
	stream        string
	partial       []byte
	lineTruncated bool
}

const (
	profileRunLogLineByteLimit       = 64 * 1024
	profileRunLogPersistByteLimit    = 8 * 1024 * 1024
	profileRunLogPersistEventLimit   = 10_000
	profileRunLogDisplayColumnLimit  = 4096
	profileRunLogLineTruncatedMarker = "[line omitted: exceeded 64 KiB]"
	profileRunLogPersistenceMarker   = "[additional log output omitted after the saved-run limit]"
)

func (writer *profileRunLogWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	written := len(data)
	for len(data) > 0 {
		newline := bytes.IndexByte(data, '\n')
		if newline < 0 {
			writer.appendFragment(data)
			break
		}
		writer.appendFragment(data[:newline])
		writer.finishLine()
		data = data[newline+1:]
	}
	return written, nil
}

func (writer *profileRunLogWriter) Flush() {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if len(writer.partial) == 0 && !writer.lineTruncated {
		return
	}
	writer.finishLine()
}

func (writer *profileRunLogWriter) appendFragment(fragment []byte) {
	remaining := profileRunLogLineByteLimit - len(writer.partial)
	if remaining <= 0 {
		writer.lineTruncated = writer.lineTruncated || len(fragment) > 0
		return
	}
	if len(fragment) > remaining {
		writer.partial = append(writer.partial, fragment[:remaining]...)
		writer.lineTruncated = true
		return
	}
	writer.partial = append(writer.partial, fragment...)
}

func (writer *profileRunLogWriter) finishLine() {
	line := boundedProfileRunLogLine(writer.partial, writer.lineTruncated)
	writer.partial = writer.partial[:0]
	writer.lineTruncated = false
	writer.reportLine(line)
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

func boundedProfileRunLogLine(line []byte, truncated bool) string {
	text := strings.ToValidUTF8(string(line), "�")
	if truncated || len(text) > profileRunLogLineByteLimit {
		return profileRunLogLineTruncatedMarker
	}
	return text
}

type profileRunEventMsg struct {
	event TaskEvent
}

type profileRunFinishedMsg struct {
	err    error
	status string
	stage  string
}

type profileRunPreparationRequest struct {
	run   profileSetupPlanRun
	stage string
}

type profileRunPreparedMsg struct {
	run                          profileSetupPlanRun
	stage                        string
	output                       string
	secretValues                 []string
	repositoryPreparationStarted bool
	repositoryPrepared           bool
	err                          error
}

var runProfilePreparation = defaultRunProfilePreparation

func startProfileRunPreparationCommand(ctx context.Context, request profileRunPreparationRequest) tea.Cmd {
	return func() tea.Msg {
		return runProfilePreparation(ctx, request)
	}
}

func defaultRunProfilePreparation(ctx context.Context, request profileRunPreparationRequest) (result profileRunPreparedMsg) {
	result.run = request.run
	result.stage = request.stage
	result.secretValues = setupConfigSecretValues(request.run.config)
	var output strings.Builder
	defer func() {
		result.output = output.String()
	}()

	if request.stage == "" {
		fmt.Fprintln(&output, setupSelectedPlanHeader)
		fmt.Fprint(&output, setupPlanSummary(request.run.config))
		fmt.Fprintln(&output)
	} else {
		fmt.Fprintf(&output, "Selected one-time stage: %s\n\n", profileRunStageLabel(request.stage))
	}
	if err := ctx.Err(); err != nil {
		result.err = err
		return result
	}
	if err := runPreflight(request.run.config, &output); err != nil {
		result.err = err
		return result
	}
	if err := ctx.Err(); err != nil {
		result.err = err
		return result
	}
	if request.stage != "" && !stageUsesRepository(request.stage) {
		return result
	}
	if request.run.store == nil {
		result.err = errors.New(setupProfileStoreUnavailable)
		return result
	}

	profileSecrets, err := request.run.store.LoadSecrets(request.run.profile.ID)
	if err != nil {
		result.err = err
		return result
	}
	result.secretValues = append(result.secretValues, profileSecretValues(profileSecrets)...)
	if token, _ := effectiveGitHubToken(profileSecrets); token != "" {
		result.secretValues = append(result.secretValues, token)
	}

	fmt.Fprintf(&output, "Preparing configuration repository: %s\n", firstNonEmpty(
		request.run.config.ConfigRepositoryPath,
		request.run.profile.ConfigRepositoryPath,
		"profile default",
	))
	result.repositoryPreparationStarted = true
	preparedProfile, preparedConfig, err := prepareDeclarativeSetup(
		ctx,
		request.run.store,
		request.run.profile,
		request.run.state,
		request.run.config,
	)
	result.run.profile = preparedProfile
	result.run.config = preparedConfig
	result.secretValues = append(result.secretValues, setupConfigSecretValues(preparedConfig)...)
	if err != nil {
		if ctx.Err() != nil {
			result.err = ctx.Err()
		} else {
			result.err = err
		}
		return result
	}
	result.repositoryPrepared = true
	if err := ctx.Err(); err != nil {
		result.err = err
		return result
	}
	fmt.Fprintf(&output, "Configuration repository ready: %s at %s\n", preparedConfig.ConfigRepositoryPath, preparedConfig.ConfigRepositoryCommit)
	return result
}

const (
	profileRunMinWidth  = 64
	profileRunMinHeight = 21
)

type profileRunStageView struct {
	Key       string
	Label     string
	Status    string
	Current   string
	Completed int
	Total     int
}

type profileRunModel struct {
	profile         Profile
	config          setupConfig
	runID           string
	messages        chan tea.Msg
	start           tea.Cmd
	runContext      context.Context
	cancel          context.CancelFunc
	profileReporter *profileRunReporter
	operationLock   profileOperationLock
	spinner         spinner.Model
	progress        progress.Model
	logViewport     viewport.Model
	stages          []profileRunStageView
	totalTasks      int
	completedTasks  int
	currentStage    string
	currentTask     string
	logLines        []string
	pendingLogLines int
	secretValues    []string
	stageFilter     string
	runLabel        string
	width           int
	height          int
	preparing       bool
	remoteStarted   bool
	done            bool
	cancelled       bool
	allowReturn     bool
	returnToSetup   bool
	err             error
}

func newProfileRunModel(profile Profile, config setupConfig, runID string, completedStages map[string]bool, stageFilter string, messages chan tea.Msg, cancel context.CancelFunc) profileRunModel {
	stages, totalTasks, completedTasks := profileRunStageViews(config, completedStages, stageFilter)
	runSpinner := spinner.New()
	runSpinner.Spinner = spinner.Dot
	return profileRunModel{
		profile:        profile,
		config:         config,
		runID:          runID,
		messages:       messages,
		cancel:         cancel,
		spinner:        runSpinner,
		progress:       progress.New(progress.WithWidth(48)),
		logViewport:    viewport.New(viewport.WithWidth(88), viewport.WithHeight(10)),
		stages:         stages,
		totalTasks:     totalTasks,
		completedTasks: completedTasks,
		secretValues:   setupConfigSecretValues(config),
		stageFilter:    stageFilter,
		runLabel:       profileRunLabel(stageFilter),
		width:          92,
		height:         28,
		remoteStarted:  true,
	}
}

func newPreparingProfileRunModel(
	ctx context.Context,
	run profileSetupPlanRun,
	stage string,
	messages chan tea.Msg,
	cancel context.CancelFunc,
	allowReturn bool,
) profileRunModel {
	model := newProfileRunModel(run.profile, run.config, "preparation", completedSetupStages(run.state), stage, messages, cancel)
	model.allowReturn = allowReturn
	model.preparing = true
	model.remoteStarted = false
	model.runContext = ctx
	model.operationLock = run.lock
	model.start = startProfileRunPreparationCommand(ctx, profileRunPreparationRequest{run: run, stage: stage})
	model.appendRunLog(profileRunPreparationStartedLine(stage))
	return model
}

func profileRunPreparationStartedLine(stage string) string {
	if stage == "" || stageUsesRepository(stage) {
		return "Starting local preflight and configuration repository preparation before SSH execution."
	}
	return "Starting local preflight before SSH execution."
}

func profileRunStageViews(config setupConfig, completedStages map[string]bool, stageFilter string) ([]profileRunStageView, int, int) {
	runStages, stageTotals := profileRunStageKeysAndTotals(config, stageFilter)
	stages := make([]profileRunStageView, 0, len(runStages))
	for _, stage := range runStages {
		stages = append(stages, profileRunStageView{Key: stage, Label: profileRunStageLabel(stage), Status: stageStatusPending, Total: stageTotals[stage]})
	}
	return markCompletedProfileRunStages(stages, completedStages, stageFilter)
}

func profileRunStageKeysAndTotals(config setupConfig, stageFilter string) ([]string, map[string]int) {
	stageTotals := setupRunStageTaskTotals(config)
	switch {
	case stageFilter == "platform":
		return []string{"network", "proxy", "observability"}, stageTotals
	case stageFilter == "stacks":
		stageTotals["stacks"] = stackRepositorySyncTaskTotal(config)
		return []string{"stacks"}, stageTotals
	case strings.HasPrefix(stageFilter, setupStageStackPrefix):
		stageTotals[stageFilter] = configuredStackStageTaskTotal(config, strings.TrimPrefix(stageFilter, setupStageStackPrefix))
		return []string{stageFilter}, stageTotals
	case stageFilter == "":
		return fullProfileRunStageKeys(config, stageTotals), stageTotals
	default:
		return []string{stageFilter}, stageTotals
	}
}

func fullProfileRunStageKeys(config setupConfig, stageTotals map[string]int) []string {
	runStages := append([]string(nil), setupStageOrder...)
	for _, stack := range config.Stacks {
		stage := setupStageStackPrefix + stack.Name
		runStages = append(runStages, stage)
		stageTotals[stage] = configuredStackTaskTotal(config, stack)
	}
	return runStages
}

func stackRepositorySyncTaskTotal(config setupConfig) int {
	syncConfig := observabilityConfig{
		BaseDomain: config.BaseDomain, AdminEmail: config.PangolinAdminEmail,
		PangolinPassword: config.PangolinAdminPassword,
		RepositoryCommit: config.ConfigRepositoryCommit, RepositoryBranch: config.ConfigRepositoryBranch, RepositoryOrigin: config.ConfigRepositoryOrigin,
		RepositoryCompose: config.ConfigRepositoryCompose, RepositorySHA256: config.ConfigRepositorySHA256,
		GitHubToken: config.GitHubToken, Stacks: config.Stacks,
	}
	return len(stackRepositoryReconcileTasks(syncConfig, firstNonEmpty(config.AdminUser, "root")))
}

func configuredStackStageTaskTotal(config setupConfig, name string) int {
	for _, stack := range config.Stacks {
		if stack.Name == name {
			return configuredStackTaskTotal(config, stack)
		}
	}
	return 0
}

func configuredStackTaskTotal(config setupConfig, stack configuredStack) int {
	return len(configuredStackTasks(observabilityConfig{
		RepositoryCommit: config.ConfigRepositoryCommit,
		RepositoryBranch: config.ConfigRepositoryBranch,
		RepositoryOrigin: config.ConfigRepositoryOrigin,
		BaseDomain:       config.BaseDomain,
		AdminEmail:       config.PangolinAdminEmail,
		PangolinPassword: config.PangolinAdminPassword,
	}, stack, firstNonEmpty(config.AdminUser, "root")))
}

func markCompletedProfileRunStages(stages []profileRunStageView, completedStages map[string]bool, stageFilter string) ([]profileRunStageView, int, int) {
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
	return stages, totalTasks, completedTasks
}

func profileRunLabel(stageFilter string) string {
	if stageFilter == "" {
		return "full setup"
	}
	return profileRunStageLabel(stageFilter)
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
			ServerSecret:     firstNonEmpty(config.ServerSecret, setupGeneratedPlaceholder),
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
			RepositoryCommit: config.ConfigRepositoryCommit, RepositoryBranch: config.ConfigRepositoryBranch, RepositoryOrigin: config.ConfigRepositoryOrigin,
			RepositoryCompose: config.ConfigRepositoryCompose, RepositorySHA256: config.ConfigRepositorySHA256,
			GitHubToken: config.GitHubToken,
		})),
	}
}

func (model profileRunModel) Init() tea.Cmd {
	if model.done {
		return nil
	}
	commands := []tea.Cmd{model.spinner.Tick}
	if model.start != nil {
		commands = append(commands, model.start)
	}
	if !model.preparing && model.messages != nil {
		commands = append(commands, waitForProfileRunMessage(model.messages))
	}
	return tea.Batch(commands...)
}

func (model profileRunModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return model.updateWindowSize(msg)
	case tea.KeyMsg:
		return model.updateKey(msg)
	case spinner.TickMsg:
		return model.updateSpinner(msg)
	case profileRunEventMsg:
		model.applyTaskEvent(msg.event)
		return model, waitForProfileRunMessage(model.messages)
	case profileRunPreparedMsg:
		return model.updatePrepared(msg)
	case profileRunFinishedMsg:
		return model.updateFinished(msg)
	}
	return model, nil
}

func (model profileRunModel) updateWindowSize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	followLogs := model.logViewport.AtBottom()
	model.width = msg.Width
	model.height = msg.Height
	model.progress.SetWidth(clampInt(msg.Width-26, 24, 60))
	model.logViewport.SetWidth(clampInt(msg.Width-4, 40, 100))
	model.logViewport.SetHeight(clampInt(msg.Height-len(model.stages)-13, 3, 18))
	if followLogs || model.logViewport.AtBottom() {
		model.logViewport.GotoBottom()
		model.pendingLogLines = 0
	}
	return model, nil
}

func (model profileRunModel) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "end":
		model.logViewport.GotoBottom()
		model.pendingLogLines = 0
		return model, nil
	case "up", "k", "down", "j", "pgup", "pgdown":
		var cmd tea.Cmd
		model.logViewport, cmd = model.logViewport.Update(msg)
		if model.logViewport.AtBottom() {
			model.pendingLogLines = 0
		}
		return model, cmd
	}
	if model.done {
		return model.updateDoneKey(msg)
	}
	switch msg.String() {
	case setupKeyCtrlC:
		return model.cancelRun()
	case "q":
		return model, nil
	default:
		return model, nil
	}
}

func (model profileRunModel) updateDoneKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", setupKeyCtrlC:
		return model, tea.Quit
	case "esc":
		if model.allowReturn {
			model.returnToSetup = true
		}
		return model, tea.Quit
	default:
		return model, nil
	}
}

func (model profileRunModel) cancelRun() (tea.Model, tea.Cmd) {
	if model.cancelled {
		return model, nil
	}
	if model.cancel != nil {
		model.cancel()
	}
	model.cancelled = true
	if model.preparing {
		model.appendRunLog("Cancelling local preparation before SSH execution...")
	} else {
		model.appendRunLog("Cancellation requested. The active remote task may have applied partially; wait for it to stop, then review the saved run before retrying.")
	}
	return model, nil
}

func (model profileRunModel) updateSpinner(msg spinner.TickMsg) (tea.Model, tea.Cmd) {
	if model.done {
		return model, nil
	}
	var cmd tea.Cmd
	model.spinner, cmd = model.spinner.Update(msg)
	return model, cmd
}

func (model profileRunModel) updateFinished(msg profileRunFinishedMsg) (tea.Model, tea.Cmd) {
	releaseProfileOperationLock(model.operationLock)
	model.operationLock = nil
	model.done = true
	if msg.stage != "" {
		model.setStageStatus(msg.stage, msg.status)
	}
	model.clearCurrentTask(model.currentStage)
	if errors.Is(msg.err, context.Canceled) {
		model.cancelled = true
		model.err = nil
		if model.remoteStarted {
			model.appendRunLog("Run cancelled. The active remote task may have applied partially; review the saved run and remote state before retrying.")
		} else {
			model.appendRunLog("Run cancelled before remote SSH execution started.")
		}
		return model, nil
	}
	model.cancelled = false
	model.err = msg.err
	if msg.err != nil {
		model.appendRunLog("Run failed: " + msg.err.Error())
		return model, nil
	}
	model.appendRunLog("Run complete.")
	return model, nil
}

func (model profileRunModel) updatePrepared(msg profileRunPreparedMsg) (tea.Model, tea.Cmd) {
	if !model.preparing || msg.stage != model.stageFilter {
		return model, nil
	}
	model.preparing = false
	model.start = nil
	model.profile = msg.run.profile
	model.config = msg.run.config
	model.secretValues = append(setupConfigSecretValues(msg.run.config), msg.secretValues...)
	appendProfileRunOutput(&model, msg.output)
	if model.cancelled {
		if msg.err != nil && !errors.Is(msg.err, context.Canceled) {
			return model.updateFinished(profileRunFinishedMsg{err: msg.err})
		}
		model.appendRepositoryCancellationNotice(msg)
		return model.updateFinished(profileRunFinishedMsg{err: context.Canceled})
	}
	if msg.err != nil {
		if errors.Is(msg.err, context.Canceled) {
			model.appendRepositoryCancellationNotice(msg)
		}
		return model.updateFinished(profileRunFinishedMsg{err: msg.err})
	}
	model.remoteStarted = true

	completedStages := completedSetupStages(msg.run.state)
	model.stages, model.totalTasks, model.completedTasks = profileRunStageViews(msg.run.config, completedStages, model.stageFilter)
	model.logViewport.SetHeight(clampInt(model.height-len(model.stages)-13, 3, 18))
	model.currentStage = ""
	model.currentTask = ""
	model.runID = newSetupRunID()
	if msg.run.state.Runs == nil {
		msg.run.state.Runs = map[string]SetupRun{}
	}
	msg.run.state.ActiveRunID = model.runID
	if model.stageFilter == "" {
		msg.run.state.Runs[model.runID] = newSetupRun(model.runID, completedStages)
	} else {
		msg.run.state.Runs[model.runID] = newSetupRunForStage(model.runID, model.stageFilter, completedStages)
	}

	if model.messages == nil {
		model.messages = make(chan tea.Msg, 256)
	}
	profileReporter := &profileRunReporter{
		store:        msg.run.store,
		profile:      msg.run.profile,
		state:        &msg.run.state,
		runID:        model.runID,
		secretValues: model.secretValues,
	}
	model.profileReporter = profileReporter
	liveReporter := &profileRunUIReporter{messages: model.messages}
	reporter := &synchronizedTaskReporter{reporters: []TaskReporter{profileReporter, liveReporter}}
	command := profileRunCommand{
		stageRun: setupStageRun{
			profile:  msg.run.profile,
			config:   msg.run.config,
			runID:    model.runID,
			reporter: reporter,
		},
		completedStages: completedStages,
		stage:           model.stageFilter,
		profileReporter: profileReporter,
		messages:        model.messages,
		initialize:      true,
		operationLock:   model.operationLock,
	}
	runContext := model.runContext
	if runContext == nil {
		runContext = context.Background()
	}
	var start tea.Cmd
	if model.stageFilter == "" {
		start = startProfileRunCommand(runContext, command)
	} else {
		start = startProfileStageRunCommand(runContext, command)
	}
	return model, tea.Batch(start, waitForProfileRunMessage(model.messages))
}

func (model *profileRunModel) appendRepositoryCancellationNotice(msg profileRunPreparedMsg) {
	if !msg.repositoryPreparationStarted {
		return
	}
	if msg.repositoryPrepared {
		model.appendRunLog("Local repository preparation completed. No remote SSH commands started.")
		return
	}
	model.appendRunLog("Local repository preparation stopped. Review the local configuration repository before retrying; no remote SSH commands started.")
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

type profileRunCommand struct {
	stageRun        setupStageRun
	completedStages map[string]bool
	stage           string
	profileReporter *profileRunReporter
	messages        chan<- tea.Msg
	initialize      bool
	operationLock   profileOperationLock
}

func startProfileRunCommand(ctx context.Context, command profileRunCommand) tea.Cmd {
	return func() tea.Msg {
		go func() {
			if err := command.initializeRun(ctx); err != nil {
				command.sendFinished(err, profileRunStatusForError(err), "")
				return
			}
			if err := ctx.Err(); err != nil {
				command.finish(err)
				return
			}
			output := newProfileRunOutput(command.stageRun.reporter, command.stageRun.runID)
			stageRun := command.stageRun
			stageRun.stdout = output
			stageRun.stderr = output
			err := runFullSetupStages(ctx, stageRun, command.completedStages)
			output.Flush()
			command.finish(err)
		}()
		return nil
	}
}

func startProfileStageRunCommand(ctx context.Context, command profileRunCommand) tea.Cmd {
	return func() tea.Msg {
		go func() {
			if err := command.initializeRun(ctx); err != nil {
				command.sendFinished(err, profileRunStatusForError(err), "")
				return
			}
			if err := ctx.Err(); err != nil {
				command.finishStage(err)
				return
			}
			output := newProfileRunOutput(command.stageRun.reporter, command.stageRun.runID)
			stageRun := command.stageRun
			stageRun.stdout = output
			stageRun.stderr = output
			err := runSetupStage(ctx, stageRun, command.stage)
			output.Flush()
			command.finishStage(err)
		}()
		return nil
	}
}

func (command profileRunCommand) initializeRun(ctx context.Context) error {
	if !command.initialize {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if command.profileReporter == nil || command.profileReporter.store == nil || command.profileReporter.state == nil {
		return errors.New(setupProfileStoreUnavailable)
	}
	return command.profileReporter.store.Save(command.profileReporter.profile, *command.profileReporter.state)
}

func (command profileRunCommand) finish(err error) {
	status := profileRunStatusForError(err)
	stage := command.profileReporter.finishRun(status, err, setupStageForError(err))
	command.sendFinished(command.preferredError(err), status, stage)
}

func (command profileRunCommand) finishStage(err error) {
	if err != nil {
		status := profileRunStatusForError(err)
		stage := command.profileReporter.finishRun(status, err, command.stage)
		command.sendFinished(command.preferredError(err), status, stage)
		return
	}
	if command.stage == "stacks" {
		command.profileReporter.state.StackRepositoryCommit = command.stageRun.config.ConfigRepositoryCommit
	}
	command.profileReporter.finishRun(runStatusComplete, nil, "")
	command.sendFinished(command.preferredError(nil), runStatusComplete, "")
}

func profileRunStatusForError(err error) string {
	if errors.Is(err, context.Canceled) {
		return runStatusCancelled
	}
	if err != nil {
		return runStatusFailed
	}
	return runStatusComplete
}

func (command profileRunCommand) preferredError(err error) error {
	if command.profileReporter.err != nil {
		return command.profileReporter.err
	}
	return err
}

func (command profileRunCommand) sendFinished(err error, status string, stage string) {
	releaseProfileOperationLock(command.operationLock)
	command.messages <- profileRunFinishedMsg{err: err, status: status, stage: stage}
	close(command.messages)
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
		model.appendRunLog(event.Line)
	case TaskSucceeded:
		model.completedTasks++
		model.incrementStageCompleted(event.Stage)
		model.clearCurrentTask(event.Stage)
	case TaskFailed:
		model.setStageStatus(event.Stage, stageStatusFailed)
		model.appendRunLog(fmt.Sprintf("%s failed: %s", event.TaskName, event.Error))
		model.clearCurrentTask(event.Stage)
	case TaskCancelled:
		model.setStageStatus(event.Stage, stageStatusCancelled)
		model.appendRunLog(fmt.Sprintf("%s cancelled.", event.TaskName))
		model.clearCurrentTask(event.Stage)
	case TaskRunCompleted:
		model.setStageStatus(event.Stage, stageStatusComplete)
		model.clearCurrentTask(event.Stage)
		model.appendRunLog(fmt.Sprintf("%s complete.", profileRunStageLabel(event.Stage)))
	}
}

func (model *profileRunModel) clearCurrentTask(stage string) {
	model.clearStageCurrent(stage)
	model.currentStage = ""
	model.currentTask = ""
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
	line = maskSecretValues(line, model.secretValues)
	line = ansi.Truncate(line, profileRunLogDisplayColumnLimit, profileRunLogLineTruncatedMarker)
	if line == "" {
		return
	}
	followLogs := model.logViewport.AtBottom()
	offset := model.logViewport.YOffset()
	model.logLines = append(model.logLines, line)
	removed := 0
	if len(model.logLines) > 200 {
		removed = len(model.logLines) - 200
		model.logLines = model.logLines[len(model.logLines)-200:]
	}
	model.logViewport.SetContent(strings.Join(model.logLines, "\n"))
	if followLogs {
		model.logViewport.GotoBottom()
		model.pendingLogLines = 0
		return
	}
	model.logViewport.SetYOffset(max(0, offset-removed))
	model.pendingLogLines++
}

func (model profileRunModel) View() tea.View {
	minimumHeight := model.minimumHeight()
	if model.width < profileRunMinWidth || model.height < minimumHeight {
		return model.minimumSizeView(minimumHeight)
	}

	var builder strings.Builder
	builder.WriteString(setupTitleStyle.Render("Servestead setup run"))
	builder.WriteString("\n")
	builder.WriteString(setupHelpStyle.Render(fmt.Sprintf("%s (%s)", sanitizeTerminalLine(firstNonEmpty(model.profile.Name, model.profile.IP)), sanitizeTerminalLine(model.profile.IP))))
	builder.WriteString("\n\n")

	builder.WriteString(model.statusLabel())
	builder.WriteString("\n")
	builder.WriteString(model.progress.ViewAs(model.taskProgress()))
	builder.WriteString(fmt.Sprintf("  Tasks: %d / %d\n", model.completedTasks, model.totalTasks))
	builder.WriteString(fmt.Sprintf("Current: %s\n\n", model.currentTaskLabel()))

	builder.WriteString(model.stagesView())
	builder.WriteString("\n")
	builder.WriteString(model.logHeading())
	builder.WriteString("\n")
	builder.WriteString(model.logViewport.View())
	builder.WriteString("\n\n")
	builder.WriteString(model.footerView())
	return altScreenView(fitTerminalWidth(builder.String(), model.width))
}

func (model profileRunModel) statusLabel() string {
	switch {
	case model.done && model.cancelled:
		return "Cancelled"
	case model.done && model.err == nil:
		return "Complete"
	case model.done:
		return "Failed"
	case model.cancelled:
		return model.spinner.View() + " Cancelling setup"
	case model.preparing:
		return model.spinner.View() + " Preparing " + model.runLabel
	default:
		return model.spinner.View() + " Running " + model.runLabel
	}
}

func (model profileRunModel) stagesView() string {
	var builder strings.Builder
	builder.WriteString("Stages\n")
	for _, stage := range model.stages {
		builder.WriteString(fmt.Sprintf("  %-10s %-9s %2d/%-2d", sanitizeTerminalLine(stage.Label), sanitizeTerminalLine(stage.Status), stage.Completed, stage.Total))
		if stage.Current != "" {
			builder.WriteString("  " + sanitizeTerminalLine(stage.Current))
		}
		builder.WriteString("\n")
	}
	return builder.String()
}

func (model profileRunModel) logHeading() string {
	if model.pendingLogLines == 0 {
		return "Logs"
	}
	lineLabel := "line"
	if model.pendingLogLines != 1 {
		lineLabel = "lines"
	}
	return fmt.Sprintf("Logs (%d new %s, End to follow)", model.pendingLogLines, lineLabel)
}

func (model profileRunModel) footerView() string {
	var builder strings.Builder
	if model.done && model.err != nil {
		builder.WriteString(setupErrorStyle.Render(maskSecretValues(model.err.Error(), model.secretValues)))
		builder.WriteString("\n")
	}
	builder.WriteString(setupHelpStyle.Render(model.helpText()))
	return builder.String()
}

func (model profileRunModel) helpText() string {
	if !model.done {
		if model.preparing {
			return "Ctrl+C requests cancellation before SSH starts.\nq exits after the run. j/k or up/down scrolls logs; End follows."
		}
		return "Ctrl+C requests cancellation; remote changes may be partial.\nq exits after the run. j/k or up/down scrolls logs; End follows."
	}
	scrollHelp := "j/k or up/down scroll logs. End follows.\n"
	if model.cancelled {
		exitHelp := "esc/q exits."
		if model.allowReturn {
			exitHelp = "esc returns to setup. q exits."
		}
		return scrollHelp + "Review the saved run and remote state before retrying. " + exitHelp
	}
	if model.allowReturn {
		return scrollHelp + "esc returns to setup. q exits."
	}
	if model.err != nil {
		return scrollHelp + "esc/q exits. Run setup again to retry."
	}
	return scrollHelp + "esc/q exits."
}

func (model profileRunModel) minimumHeight() int {
	return profileRunMinHeight + max(0, len(model.stages)-len(setupStageOrder))
}

func (model profileRunModel) minimumSizeView(minimumHeight int) tea.View {
	var builder strings.Builder
	builder.WriteString(setupTitleStyle.Render("Servestead setup run"))
	builder.WriteString("\n\n")
	builder.WriteString(setupErrorStyle.Render("Terminal too small"))
	builder.WriteString("\n")
	builder.WriteString(fmt.Sprintf("Resize to at least %d x %d.\n", profileRunMinWidth, minimumHeight))
	builder.WriteString(fmt.Sprintf("Current size: %d x %d.\n\n", model.width, model.height))
	switch {
	case model.done && model.cancelled:
		builder.WriteString("Run cancelled. Remote work may have been partially applied. Resize to review results before retrying.\nq exits.")
	case model.done && model.err != nil:
		builder.WriteString("Run failed. Resize to view results.\nq exits.")
	case model.done:
		builder.WriteString("Run complete. Resize to view results.\nq exits.")
	case model.cancelled:
		builder.WriteString("Cancellation is in progress.")
	case model.preparing:
		builder.WriteString("Preparation active. Ctrl+C requests cancellation before SSH execution starts.")
	default:
		builder.WriteString("Run active. Ctrl+C requests cancellation; remote work may already be partially applied.")
	}
	return altScreenView(builder.String())
}

func (model profileRunModel) taskProgress() float64 {
	if model.totalTasks == 0 {
		return 0
	}
	return float64(model.completedTasks) / float64(model.totalTasks)
}

func (model profileRunModel) currentTaskLabel() string {
	if model.preparing {
		if model.cancelled {
			return "waiting for local preparation to stop"
		}
		if model.stageFilter == "" || stageUsesRepository(model.stageFilter) {
			return "local preflight and configuration repository"
		}
		return "local preflight checks"
	}
	if model.currentTask == "" {
		if model.done {
			return "none"
		}
		if model.cancelled {
			return "waiting for active task to stop"
		}
		if model.completedTasks > 0 {
			return "waiting for next remote task"
		}
		return "waiting for first remote task"
	}
	return fmt.Sprintf("%s - %s", sanitizeTerminalLine(profileRunStageLabel(model.currentStage)), sanitizeTerminalLine(model.currentTask))
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
		if strings.HasPrefix(stage, setupStageStackPrefix) {
			return "Stack " + sanitizeTerminalLine(strings.TrimPrefix(stage, setupStageStackPrefix))
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
	store                   ProfileStore
	profile                 Profile
	state                   *ProfileState
	runID                   string
	secretValues            []string
	lastStage               string
	persistedLogBytes       int
	persistedLogEvents      int
	logPersistenceTruncated bool
	err                     error
}

func (reporter *profileRunReporter) Report(event TaskEvent) {
	if reporter.err != nil {
		return
	}
	event.Line = maskSecretValues(event.Line, reporter.secretValues)
	event.Error = maskSecretValues(event.Error, reporter.secretValues)
	if event.Type == TaskLogLine {
		event.Line = sanitizeTerminalLine(boundedProfileRunLogLine([]byte(event.Line), false))
		if !reporter.acceptPersistedLogEvent(event) {
			return
		}
	}
	if event.Stage != "" {
		reporter.lastStage = event.Stage
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
	case TaskCancelled:
		stage.Status = stageStatusCancelled
		stage.LastEnded = event.Time
		stage.LastError = ""
		run.Status = runStatusCancelled
	}
	run.Stages[event.Stage] = stage
	reporter.state.Runs[reporter.runID] = run
	if err := reporter.store.Save(reporter.profile, *reporter.state); err != nil {
		reporter.err = err
	}
}

func (reporter *profileRunReporter) acceptPersistedLogEvent(event TaskEvent) bool {
	eventSize := 256 + len(event.RunID) + len(event.Stage) + len(event.Stream) + len(event.TaskName) + len(event.Line)
	if reporter.persistedLogEvents < profileRunLogPersistEventLimit &&
		reporter.persistedLogBytes+eventSize <= profileRunLogPersistByteLimit {
		reporter.persistedLogEvents++
		reporter.persistedLogBytes += eventSize
		return true
	}
	if reporter.logPersistenceTruncated {
		return false
	}
	reporter.logPersistenceTruncated = true
	marker := event
	marker.Line = profileRunLogPersistenceMarker
	marker.Time = time.Now().UTC()
	if err := reporter.store.AppendRunEvent(reporter.profile.ID, reporter.runID, marker); err != nil {
		reporter.err = err
	}
	return false
}

func (reporter *profileRunReporter) finishRun(status string, runErr error, preferredStage string) string {
	if reporter.err != nil {
		return ""
	}
	run := reporter.state.Runs[reporter.runID]
	terminalStage := ""
	if status == runStatusFailed || status == runStatusCancelled {
		terminalStage = terminalStageForRun(run, status, preferredStage, reporter.lastStage)
		if err := reporter.recordTerminalStage(&run, status, terminalStage, runErr); err != nil {
			reporter.err = err
			return terminalStage
		}
	}
	run.Status = status
	run.UpdatedAt = time.Now().UTC()
	reporter.state.Runs[reporter.runID] = run
	if err := reporter.store.Save(reporter.profile, *reporter.state); err != nil {
		reporter.err = err
	}
	return terminalStage
}

func (reporter *profileRunReporter) recordTerminalStage(run *SetupRun, status, stageName string, runErr error) error {
	if stageName == "" {
		return nil
	}
	wantedStatus := stageStatusFailed
	eventType := TaskFailed
	errorText := ""
	if runErr != nil {
		errorText = maskSecretValues(runErr.Error(), reporter.secretValues)
	}
	if status == runStatusCancelled {
		wantedStatus = stageStatusCancelled
		eventType = TaskCancelled
		errorText = ""
	}
	stage := run.Stages[stageName]
	if stage.Status == wantedStatus {
		if wantedStatus == stageStatusFailed && stage.LastError == "" {
			stage.LastError = errorText
			run.Stages[stageName] = stage
		}
		return nil
	}
	now := time.Now().UTC()
	event := TaskEvent{
		Type:     eventType,
		RunID:    reporter.runID,
		Stage:    stageName,
		TaskName: profileRunStageLabel(stageName),
		Error:    errorText,
		Time:     now,
	}
	if err := reporter.store.AppendRunEvent(reporter.profile.ID, reporter.runID, event); err != nil {
		return err
	}
	stage.Status = wantedStatus
	stage.LastEnded = now
	stage.LastError = errorText
	run.Stages[stageName] = stage
	return nil
}

func terminalStageForRun(run SetupRun, status string, preferredStage string, lastStage string) string {
	ordered := orderedSetupRunStages(run)
	wantedStatus := stageStatusFailed
	if status == runStatusCancelled {
		wantedStatus = stageStatusCancelled
	}
	if last, ok := run.Stages[lastStage]; lastStage != "" && ok && last.Status == wantedStatus {
		return lastStage
	}
	for _, stage := range ordered {
		if run.Stages[stage].Status == wantedStatus {
			return stage
		}
	}
	if _, ok := run.Stages[preferredStage]; preferredStage != "" && ok {
		return preferredStage
	}
	for _, stage := range ordered {
		if run.Stages[stage].Status == stageStatusRunning {
			return stage
		}
	}
	if _, ok := run.Stages[lastStage]; lastStage != "" && ok {
		return lastStage
	}
	for _, stage := range ordered {
		if run.Stages[stage].Status == stageStatusPending {
			return stage
		}
	}
	return ""
}

func orderedSetupRunStages(run SetupRun) []string {
	ordered := make([]string, 0, len(run.Stages))
	seen := map[string]bool{}
	for _, stage := range setupStageOrder {
		if _, ok := run.Stages[stage]; ok {
			ordered = append(ordered, stage)
			seen[stage] = true
		}
	}
	var extra []string
	for stage := range run.Stages {
		if !seen[stage] {
			extra = append(extra, stage)
		}
	}
	sort.Strings(extra)
	return append(ordered, extra...)
}

func runSetupPlan(ctx context.Context, config setupConfig, stdout, stderr io.Writer) error {
	fmt.Fprintln(stdout, setupSelectedPlanHeader)
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
			setupFlagHost, config.Host,
			setupFlagSSHUser, config.InitialSSHUser,
			"--admin-user", config.AdminUser,
			"--admin-public-key", config.AdminPublicKeyPath,
			setupFlagPrivateKey, config.PrivateKeyPath,
		}, stdout, stderr); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "Step 2/2: harden server.")
		if err := runHarden(ctx, []string{
			setupFlagHost, config.Host,
			setupFlagSSHUser, config.AdminUser,
			setupFlagPrivateKey, config.PrivateKeyPath,
		}, stdout, stderr); err != nil {
			return err
		}
		printSSHLoginGuidance(stdout, config)
		return nil
	case setupModeHardenOnly:
		fmt.Fprintln(stdout, "Step 1/1: harden server.")
		if err := runHarden(ctx, []string{
			setupFlagHost, config.Host,
			setupFlagSSHUser, config.AdminUser,
			setupFlagPrivateKey, config.PrivateKeyPath,
		}, stdout, stderr); err != nil {
			return err
		}
		printSSHLoginGuidance(stdout, config)
		return nil
	case setupModeNetwork:
		fmt.Fprintln(stdout, "Step 1/1: configure Docker networking and UFW.")
		if err := runNetwork(ctx, []string{
			setupFlagHost, config.Host,
			setupFlagSSHUser, config.AdminUser,
			setupFlagPrivateKey, config.PrivateKeyPath,
		}, stdout, stderr); err != nil {
			return err
		}
		printSSHLoginGuidance(stdout, config)
		return nil
	case setupModeProxy:
		fmt.Fprintln(stdout, "Step 1/1: deploy Pangolin and reverse proxy stack.")
		return runProxy(ctx, []string{
			setupFlagHost, config.Host,
			setupFlagSSHUser, config.AdminUser,
			setupFlagPrivateKey, config.PrivateKeyPath,
			"--domain", config.BaseDomain,
			"--email", config.LetsEncryptEmail,
			"--server-secret", config.ServerSecret,
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
	check := fileCheck(setupAdminPublicKeyLabel, path, required)
	if !check.OK {
		return check
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return preflightCheck{Name: setupAdminPublicKeyLabel, Detail: err.Error(), OK: false, Required: required}
	}
	fields := strings.Fields(strings.TrimSpace(string(data)))
	if len(fields) < 2 || fields[0] != "ssh-ed25519" {
		return preflightCheck{Name: setupAdminPublicKeyLabel, Detail: "must be an ssh-ed25519 public key", OK: false, Required: required}
	}
	return preflightCheck{Name: setupAdminPublicKeyLabel, Detail: path, OK: true, Required: required}
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
	width     int
	height    int
	err       string
	config    setupConfig
	done      bool
	cancelled bool
	quit      bool
}

var (
	setupTitleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	setupHelpStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	setupWarningStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	setupErrorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
)

func altScreenView(content string) tea.View {
	view := tea.NewView(content)
	view.AltScreen = true
	return view
}

func fitTerminalWidth(content string, width int) string {
	if width <= 0 {
		return content
	}
	return lipgloss.NewStyle().Width(width).Render(content)
}

func newSetupModel() setupModel {
	return setupModel{
		step:   setupStepMode,
		width:  profileSetupMinWidth,
		height: profileSetupMinHeight,
	}
}

func (model setupModel) Init() tea.Cmd {
	return textinput.Blink
}

func (model setupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		model.width = size.Width
		model.height = size.Height
		fieldWidth := max(10, size.Width-34)
		for index := range model.inputs {
			model.inputs[index].SetWidth(fieldWidth)
		}
		return model, nil
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return model, nil
	}

	switch key.String() {
	case setupKeyCtrlC:
		model.cancelled = true
		return model, tea.Quit
	case "q":
		if model.step != setupStepInput {
			model.quit = true
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
	if model.setupTerminalTooSmall() {
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
		fieldWidth := max(10, model.width-34)
		for index := range model.inputs {
			model.inputs[index].SetWidth(fieldWidth)
		}
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
	case setupKeyShiftTab, "up":
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
	model.inputs[model.focus], cmd = updateSetupTextInput(model.inputs[model.focus], key)
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

func (model setupModel) View() tea.View {
	if model.setupTerminalTooSmall() {
		return setupMinimumSizeView("Servestead setup", model.width, model.height)
	}
	var builder strings.Builder
	builder.WriteString(setupTitleStyle.Render("Servestead setup"))
	builder.WriteString("\n\n")

	switch model.step {
	case setupStepMode:
		builder.WriteString(model.modeStepView())
	case setupStepInput:
		builder.WriteString(model.inputStepView())
	case setupStepConfirm:
		builder.WriteString(model.confirmStepView())
	}
	return altScreenView(builder.String())
}

func (model setupModel) setupTerminalTooSmall() bool {
	return model.width < profileSetupMinWidth || model.height < profileSetupMinHeight
}

func setupMinimumSizeView(title string, width, height int) tea.View {
	var builder strings.Builder
	builder.WriteString(setupTitleStyle.Render(title))
	builder.WriteString("\n\n")
	builder.WriteString(setupErrorStyle.Render("Terminal too small"))
	builder.WriteString("\n")
	builder.WriteString(fmt.Sprintf("Resize to at least %d x %d.\n", profileSetupMinWidth, profileSetupMinHeight))
	builder.WriteString(fmt.Sprintf("Current size: %d x %d.\n", width, height))
	builder.WriteString("\nCtrl+C cancels. Resize to continue.")
	return altScreenView(builder.String())
}

func (model setupModel) modeStepView() string {
	var builder strings.Builder
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
	return builder.String()
}

func (model setupModel) inputStepView() string {
	var builder strings.Builder
	builder.WriteString(setupModeOptions()[int(model.mode)].Label)
	builder.WriteString("\n")
	builder.WriteString(setupHelpStyle.Render(setupModeOptions()[int(model.mode)].Description))
	builder.WriteString("\n\n")
	builder.WriteString(setupInputIntro(model.mode))
	builder.WriteString("\n\n")
	for _, input := range model.inputs {
		builder.WriteString(input.View())
		builder.WriteString("\n")
	}
	if model.err != "" {
		builder.WriteString("\n")
		builder.WriteString(setupErrorStyle.Render(sanitizeTerminalText(model.err)))
		builder.WriteString("\n")
	}
	builder.WriteString("\n")
	builder.WriteString(setupHelpStyle.Render("Enter advances. Tab changes field. Esc goes back."))
	return builder.String()
}

func setupInputIntro(mode setupMode) string {
	switch mode {
	case setupModeProviderKey:
		return "Servestead will write the private key and matching .pub file, then print the public key for DigitalOcean."
	case setupModeProxy:
		return "Enter the target host, domain, and Let's Encrypt email. Servestead generates the Pangolin server secret."
	default:
		return "Enter the target host and confirm the SSH key. Servestead uses the matching .pub file for the admin account."
	}
}

func (model setupModel) confirmStepView() string {
	var builder strings.Builder
	builder.WriteString("Review plan:\n\n")
	builder.WriteString(setupPlanSummary(model.config))
	builder.WriteString("\n")
	builder.WriteString(setupConfirmText(model.mode))
	builder.WriteString("\n")
	builder.WriteString(setupHelpStyle.Render("r runs it. e edits. Esc goes back. q quits."))
	return builder.String()
}

func setupConfirmText(mode setupMode) string {
	if mode == setupModeProviderKey {
		return "Servestead will create an unencrypted local ED25519 keypair for non-interactive SSH automation. It will not contact your cloud provider; you will copy the printed public key into the provider UI.\n"
	}
	return "Before remote changes, Servestead will check built-in SSH/key support and key files. If a required check fails, it stops before contacting the server.\n"
}

type setupModeOption struct {
	Label       string
	Description string
}

func setupModeOptions() []setupModeOption {
	return []setupModeOption{
		{
			Label:       "Prepare the Servestead SSH key",
			Description: "Generate the ED25519 keypair used for provider login and later servestead access.",
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
			setupInputField{label: "Admin user", value: "servestead"},
		)
	} else {
		fields = append(fields, setupInputField{label: "Admin SSH user", value: "servestead"})
	}
	fields = append(fields, setupInputField{label: setupPrivateKeyLabel, placeholder: defaultKeygenConfig().Path, value: defaultKeygenConfig().Path})
	if mode == setupModeProxy {
		fields = append(fields,
			setupInputField{label: setupBaseDomainLabel, placeholder: setupExampleDomainPlaceholder},
			setupInputField{label: setupLetsEncryptEmailLabel, placeholder: setupAdminEmailPlaceholder},
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
		input.CharLimit = 256
		input.SetWidth(72)
		if field.secret {
			input.EchoMode = textinput.EchoPassword
		}
		input.SetValue(field.value)
		input = sanitizeSetupTextInput(input)
		inputs = append(inputs, input)
	}
	return inputs
}

func updateSetupTextInput(input textinput.Model, msg tea.Msg) (textinput.Model, tea.Cmd) {
	input, command := input.Update(msg)
	return sanitizeSetupTextInput(input), command
}

func sanitizeSetupTextInput(input textinput.Model) textinput.Model {
	if input.EchoMode != textinput.EchoNormal {
		return input
	}
	value := []rune(input.Value())
	cursor := input.Position()
	sanitized := make([]rune, 0, len(value))
	removedBeforeCursor := 0
	for index, character := range value {
		if unicode.Is(unicode.Cf, character) {
			if index < cursor {
				removedBeforeCursor++
			}
			continue
		}
		sanitized = append(sanitized, character)
	}
	if len(sanitized) == len(value) {
		return input
	}
	input.SetValue(string(sanitized))
	input.SetCursor(cursor - removedBeforeCursor)
	return input
}

func (model setupModel) configFromInputs() (setupConfig, error) {
	value := setupInputValue(func(index int) string {
		return strings.TrimSpace(model.inputs[index].Value())
	})
	switch model.mode {
	case setupModeProviderKey:
		return providerKeySetupConfig(value)
	case setupModeBootstrapHarden:
		return bootstrapHardenSetupConfig(value)
	case setupModeHardenOnly:
		return adminOnlySetupConfig(setupModeHardenOnly, value)
	case setupModeNetwork:
		return adminOnlySetupConfig(setupModeNetwork, value)
	case setupModeProxy:
		return proxySetupConfigFromInputs(value)
	default:
		return setupConfig{}, errors.New("unknown setup mode")
	}
}

type setupInputValue func(int) string

func providerKeySetupConfig(value setupInputValue) (setupConfig, error) {
	config := setupConfig{
		Mode:               setupModeProviderKey,
		ProviderKeyPath:    expandUserPath(value(0)),
		ProviderKeyComment: value(1),
	}
	if config.ProviderKeyPath == "" {
		return setupConfig{}, errors.New("private key path is required")
	}
	return config, nil
}

func bootstrapHardenSetupConfig(value setupInputValue) (setupConfig, error) {
	config := setupConfig{
		Mode:           setupModeBootstrapHarden,
		Host:           value(0),
		InitialSSHUser: firstNonEmpty(value(1), "root"),
		AdminUser:      firstNonEmpty(value(2), "servestead"),
		PrivateKeyPath: expandUserPath(value(3)),
	}
	config.AdminPublicKeyPath = publicKeyPath(config.PrivateKeyPath)
	if err := validateHostAndPrivateKey(config); err != nil {
		return setupConfig{}, err
	}
	if !linuxUsername.MatchString(config.InitialSSHUser) || !linuxUsername.MatchString(config.AdminUser) {
		return setupConfig{}, errors.New("SSH users must be valid Linux usernames")
	}
	return config, nil
}

func adminOnlySetupConfig(mode setupMode, value setupInputValue) (setupConfig, error) {
	config := setupConfig{
		Mode:           mode,
		Host:           value(0),
		AdminUser:      firstNonEmpty(value(1), "servestead"),
		PrivateKeyPath: expandUserPath(value(2)),
	}
	if err := validateHostAndPrivateKey(config); err != nil {
		return setupConfig{}, err
	}
	if !linuxUsername.MatchString(config.AdminUser) {
		return setupConfig{}, errors.New("admin SSH user must be a valid Linux username")
	}
	return config, nil
}

func proxySetupConfigFromInputs(value setupInputValue) (setupConfig, error) {
	config, err := adminOnlySetupConfig(setupModeProxy, value)
	if err != nil {
		return setupConfig{}, err
	}
	config.BaseDomain = value(3)
	config.LetsEncryptEmail = value(4)
	if err := populateProxySetupSecrets(&config); err != nil {
		return setupConfig{}, err
	}
	return config, validateProxyConfig(proxyConfig{
		Host:             config.Host,
		SSHUser:          config.AdminUser,
		PrivateKeyPath:   config.PrivateKeyPath,
		BaseDomain:       config.BaseDomain,
		LetsEncryptEmail: config.LetsEncryptEmail,
		ServerSecret:     config.ServerSecret,
		SetupToken:       config.PangolinSetupToken,
	})
}

func validateHostAndPrivateKey(config setupConfig) error {
	if config.Host == "" {
		return errors.New("host is required")
	}
	if config.PrivateKeyPath == "" {
		return errors.New("private key path is required")
	}
	return nil
}

func populateProxySetupSecrets(config *setupConfig) error {
	serverSecret, err := GenerateServerSecret()
	if err != nil {
		return fmt.Errorf("generate server secret: %w", err)
	}
	setupToken, err := GeneratePangolinSetupToken()
	if err != nil {
		return fmt.Errorf("generate Pangolin setup token: %w", err)
	}
	config.ServerSecret = serverSecret
	config.PangolinSetupToken = setupToken
	return nil
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
	host := sanitizeTerminalLine(config.Host)
	initialSSHUser := sanitizeTerminalLine(config.InitialSSHUser)
	adminUser := sanitizeTerminalLine(config.AdminUser)
	privateKeyPath := sanitizeTerminalLine(config.PrivateKeyPath)
	adminPublicKeyPath := sanitizeTerminalLine(config.AdminPublicKeyPath)
	baseDomain := sanitizeTerminalLine(config.BaseDomain)
	profileID := sanitizeTerminalLine(firstNonEmpty(config.ProfileID, "(unsaved)"))
	repositoryPath := sanitizeTerminalLine(firstNonEmpty(config.ConfigRepositoryPath, "the profile's default repository"))
	switch config.Mode {
	case setupModeProviderKey:
		return fmt.Sprintf(
			"- Generate the Servestead ED25519 keypair at %s.\n- Print the public key and provider registration guidance.\n",
			sanitizeTerminalLine(config.ProviderKeyPath),
		)
	case setupModeDoctor:
		return "- Run local preflight checks.\n"
	case setupModeBootstrapHarden:
		return fmt.Sprintf(
			"- Connect to %s as %s with %s.\n- Install %s using %s.\n- Harden the server as %s.\n",
			host,
			initialSSHUser,
			privateKeyPath,
			adminUser,
			adminPublicKeyPath,
			adminUser,
		)
	case setupModeHardenOnly:
		return fmt.Sprintf("- Harden %s as %s using %s.\n", host, adminUser, privateKeyPath)
	case setupModeNetwork:
		return fmt.Sprintf(
			"- Connect to %s as %s with %s.\n- Configure Docker networking and UFW policy.\n",
			host,
			adminUser,
			privateKeyPath,
		)
	case setupModeProxy:
		return fmt.Sprintf(
			"- Connect to %s as %s with %s.\n- Deploy Traefik, Pangolin, Gerbil, Newt, Beszel, Dozzle, and Dockhand for %s.\n- %s.\n",
			host,
			adminUser,
			privateKeyPath,
			baseDomain,
			sanitizeTerminalLine(requiredDNSGuidance(config.BaseDomain, config.Host)),
		)
	case setupModeFullRun:
		return fmt.Sprintf(
			"- Use profile %s for %s.\n- Connect first as %s, create or update %s, then harden the server.\n- Configure Docker networking and UFW as %s.\n- Deploy Traefik, Pangolin, Gerbil, Newt, Beszel, Dozzle, and Dockhand for %s.\n- Deploy committed observability configuration from %s.\n- Pangolin and observability secrets are generated, saved, and reused without printing them.\n- %s.\n",
			profileID,
			host,
			initialSSHUser,
			adminUser,
			adminUser,
			baseDomain,
			repositoryPath,
			sanitizeTerminalLine(requiredDNSGuidance(config.BaseDomain, config.Host)),
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
