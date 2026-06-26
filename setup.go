package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type setupMode int

const (
	setupModeProviderKey setupMode = iota
	setupModeBootstrapHarden
	setupModeHardenOnly
	setupModeNetwork
	setupModeProxy
	setupModeDoctor
)

type setupConfig struct {
	Mode               setupMode
	Host               string
	InitialSSHUser     string
	AdminUser          string
	AdminPublicKeyPath string
	PrivateKeyPath     string
	ProviderKeyPath    string
	ProviderKeyComment string
	BaseDomain         string
	LetsEncryptEmail   string
	PostgresPassword   string
}

type preflightCheck struct {
	Name     string
	Detail   string
	OK       bool
	Required bool
}

const setupUsage = `Usage of setup:
  aegisnode setup

Launches the guided terminal UI. This is the recommended workflow for key creation, existing-VPS setup, server hardening, Docker networking, reverse proxy deployment, and preflight checks.
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
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	config, err := collectSetupConfig(stderr)
	if err != nil {
		return err
	}
	if err := runSetupPlan(ctx, config, stdout, stderr); err != nil {
		return err
	}
	return nil
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

func collectSetupConfig(output io.Writer) (setupConfig, error) {
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
	if result.cancelled {
		return setupConfig{}, errors.New("setup cancelled")
	}
	if !result.done {
		return setupConfig{}, errors.New("setup did not complete")
	}
	return result.config, nil
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
			"--server-secret", config.PostgresPassword,
		}, stdout, stderr)
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

func preflightChecks(config setupConfig) []preflightCheck {
	checks := []preflightCheck{
		nativeCapabilityCheck("native ED25519 key generation"),
	}
	if config.Mode == setupModeProviderKey {
		return checks
	}
	checks = append(checks, nativeCapabilityCheck("native SSH runner"))

	privateKeyRequired := config.Mode == setupModeBootstrapHarden || config.Mode == setupModeHardenOnly || config.Mode == setupModeNetwork || config.Mode == setupModeProxy
	checks = append(checks, fileCheck("private key", config.PrivateKeyPath, privateKeyRequired))

	publicKeyRequired := config.Mode == setupModeBootstrapHarden
	checks = append(checks, adminPublicKeyCheck(config.AdminPublicKeyPath, publicKeyRequired))
	return checks
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
	setupTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	setupHelpStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	setupErrorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
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
	case "ctrl+c", "esc":
		model.cancelled = true
		return model, tea.Quit
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
	case "enter", "y", "Y":
		model.done = true
		return model, tea.Quit
	case "b", "B":
		model.err = ""
		if model.mode == setupModeDoctor {
			model.step = setupStepMode
			return model, nil
		}
		model.step = setupStepInput
		return model, nil
	case "n", "N", "q":
		model.cancelled = true
		return model, tea.Quit
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
		builder.WriteString(setupHelpStyle.Render("Use arrow keys, then Enter. Esc cancels."))
	case setupStepInput:
		builder.WriteString(setupModeOptions()[int(model.mode)].Label)
		builder.WriteString("\n")
		builder.WriteString(setupHelpStyle.Render(setupModeOptions()[int(model.mode)].Description))
		builder.WriteString("\n\n")
		if model.mode == setupModeProviderKey {
			builder.WriteString("AegisNode will write the private key and matching .pub file, then print the public key for Hetzner or DigitalOcean.")
		} else if model.mode == setupModeProxy {
			builder.WriteString("Enter the target host, domain, Let's Encrypt email, and database password for the reverse proxy stack.")
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
		builder.WriteString(setupHelpStyle.Render("Enter advances. Tab changes field. Esc cancels."))
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
		builder.WriteString(setupHelpStyle.Render("Enter or y runs it. b edits. n cancels."))
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
			setupInputField{label: "Server secret", placeholder: "long random secret", secret: true},
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
		config.PostgresPassword = value(5)
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
			PostgresPassword: config.PostgresPassword,
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
			"- Connect to %s as %s with %s.\n- Deploy Traefik, Pangolin, and Gerbil for %s.\n- Required DNS: A %s -> %s and A *.%s -> %s.\n",
			config.Host,
			config.AdminUser,
			config.PrivateKeyPath,
			config.BaseDomain,
			config.BaseDomain,
			config.Host,
			config.BaseDomain,
			config.Host,
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
