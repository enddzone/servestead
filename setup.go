package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type setupMode int

const (
	setupModeBootstrapHarden setupMode = iota
	setupModeHardenOnly
	setupModeDoctor
)

type setupConfig struct {
	Mode               setupMode
	Host               string
	InitialSSHUser     string
	AdminUser          string
	AdminPublicKeyPath string
	PrivateKeyPath     string
}

type preflightCheck struct {
	Name     string
	Detail   string
	OK       bool
	Required bool
}

type lookPathFunc func(string) (string, error)

const setupUsage = `Usage of setup:
  aegisnode setup

Launches a guided terminal UI for live testing Phase 1 and Phase 2 on an existing Ubuntu VPS. The TUI can bootstrap and harden a server, harden an already-bootstrapped server, or run local preflight checks only.
`

const doctorUsage = `Usage of doctor:
  aegisnode doctor [--admin-public-key <path>] [--private-key <path>]

Runs local preflight checks for required tools, embedded playbooks, and optional key files without contacting a server.
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
	return runPreflight(config, stdout, exec.LookPath)
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
	if err := runPreflight(config, stdout, exec.LookPath); err != nil {
		return err
	}

	switch config.Mode {
	case setupModeDoctor:
		fmt.Fprintln(stdout, "Preflight complete. No remote changes were requested.")
		return nil
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
		fmt.Fprintln(stdout, "Step 2/2: apply Phase 2 hardening.")
		return runHarden(ctx, []string{
			"--host", config.Host,
			"--ssh-user", config.AdminUser,
			"--private-key", config.PrivateKeyPath,
		}, stdout, stderr)
	case setupModeHardenOnly:
		fmt.Fprintln(stdout, "Step 1/1: apply Phase 2 hardening.")
		return runHarden(ctx, []string{
			"--host", config.Host,
			"--ssh-user", config.AdminUser,
			"--private-key", config.PrivateKeyPath,
		}, stdout, stderr)
	default:
		return errors.New("unknown setup mode")
	}
}

func runPreflight(config setupConfig, stdout io.Writer, lookPath lookPathFunc) error {
	checks := preflightChecks(config, lookPath)
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

func preflightChecks(config setupConfig, lookPath lookPathFunc) []preflightCheck {
	checks := []preflightCheck{
		commandCheck("ansible-playbook", true, lookPath),
		commandCheck("ssh", true, lookPath),
		embeddedPlaybookCheck("bootstrap.yml", "playbooks/bootstrap.yml"),
		embeddedPlaybookCheck("hardening.yml", "playbooks/hardening.yml"),
	}

	privateKeyRequired := config.Mode == setupModeBootstrapHarden || config.Mode == setupModeHardenOnly
	checks = append(checks, fileCheck("private key", config.PrivateKeyPath, privateKeyRequired))

	publicKeyRequired := config.Mode == setupModeBootstrapHarden
	checks = append(checks, adminPublicKeyCheck(config.AdminPublicKeyPath, publicKeyRequired))
	return checks
}

func commandCheck(command string, required bool, lookPath lookPathFunc) preflightCheck {
	path, err := lookPath(command)
	if err != nil {
		return preflightCheck{Name: command + " available", Detail: "not found on PATH", OK: false, Required: required}
	}
	return preflightCheck{Name: command + " available", Detail: path, OK: true, Required: required}
}

func embeddedPlaybookCheck(name, path string) preflightCheck {
	if _, err := embeddedPlaybooks.ReadFile(path); err != nil {
		return preflightCheck{Name: "embedded " + name, Detail: err.Error(), OK: false, Required: true}
	}
	return preflightCheck{Name: "embedded " + name, Detail: path, OK: true, Required: true}
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
		builder.WriteString("Choose a guided path. This setup flow does not create billable cloud resources; use it with an existing disposable Ubuntu VPS for live smoke testing.\n\n")
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
		builder.WriteString("Enter the connection details for the target VPS. Preflight checks run before any remote changes are attempted.")
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
		builder.WriteString("Before remote changes, AegisNode will check local dependencies, embedded playbooks, and key files. If a required check fails, it stops before contacting the server.\n")
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
			Label:       "Bootstrap an existing Ubuntu VPS, then apply Phase 2 hardening",
			Description: "Use this for the first live server test after a VPS is available and reachable as root or another initial SSH user.",
		},
		{
			Label:       "Apply Phase 2 hardening to an already-bootstrapped VPS",
			Description: "Use this when the administrative user already exists and you only need sysctl, unattended upgrades, and CrowdSec.",
		},
		{
			Label:       "Run local preflight checks only",
			Description: "Checks local tools and embedded playbooks without making server changes.",
		},
	}
}

func setupInputs(mode setupMode) []textinput.Model {
	fields := []struct {
		label       string
		placeholder string
		value       string
	}{
		{label: "Host", placeholder: "203.0.113.10"},
	}
	if mode == setupModeBootstrapHarden {
		fields = append(fields,
			struct {
				label       string
				placeholder string
				value       string
			}{label: "Initial SSH user", value: "root"},
			struct {
				label       string
				placeholder string
				value       string
			}{label: "Admin user", value: "aegisadmin"},
			struct {
				label       string
				placeholder string
				value       string
			}{label: "Admin public key path", placeholder: "$HOME/.ssh/id_ed25519.pub"},
		)
	} else {
		fields = append(fields, struct {
			label       string
			placeholder string
			value       string
		}{label: "Admin SSH user", value: "aegisadmin"})
	}
	fields = append(fields, struct {
		label       string
		placeholder string
		value       string
	}{label: "Private key path", placeholder: "$HOME/.ssh/id_ed25519"})

	inputs := make([]textinput.Model, 0, len(fields))
	for _, field := range fields {
		input := textinput.New()
		input.Prompt = field.label + ": "
		input.Placeholder = field.placeholder
		input.SetValue(field.value)
		input.CharLimit = 256
		input.Width = 72
		inputs = append(inputs, input)
	}
	return inputs
}

func (model setupModel) configFromInputs() (setupConfig, error) {
	value := func(index int) string {
		return strings.TrimSpace(model.inputs[index].Value())
	}
	config := setupConfig{Mode: model.mode, Host: value(0)}
	if config.Host == "" {
		return setupConfig{}, errors.New("host is required")
	}

	switch model.mode {
	case setupModeBootstrapHarden:
		config.InitialSSHUser = firstNonEmpty(value(1), "root")
		config.AdminUser = firstNonEmpty(value(2), "aegisadmin")
		config.AdminPublicKeyPath = expandUserPath(value(3))
		config.PrivateKeyPath = expandUserPath(value(4))
		if config.AdminPublicKeyPath == "" || config.PrivateKeyPath == "" {
			return setupConfig{}, errors.New("admin public key and private key paths are required")
		}
		if !linuxUsername.MatchString(config.InitialSSHUser) || !linuxUsername.MatchString(config.AdminUser) {
			return setupConfig{}, errors.New("SSH users must be valid Linux usernames")
		}
	case setupModeHardenOnly:
		config.AdminUser = firstNonEmpty(value(1), "aegisadmin")
		config.PrivateKeyPath = expandUserPath(value(2))
		if config.PrivateKeyPath == "" {
			return setupConfig{}, errors.New("private key path is required")
		}
		if !linuxUsername.MatchString(config.AdminUser) {
			return setupConfig{}, errors.New("admin SSH user must be a valid Linux username")
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
	case setupModeDoctor:
		return "- Run local preflight checks.\n"
	case setupModeBootstrapHarden:
		return fmt.Sprintf(
			"- Bootstrap %s as %s using initial user %s.\n- Apply Phase 2 hardening as %s.\n",
			config.Host,
			config.AdminUser,
			config.InitialSSHUser,
			config.AdminUser,
		)
	case setupModeHardenOnly:
		return fmt.Sprintf("- Apply Phase 2 hardening to %s as %s.\n", config.Host, config.AdminUser)
	default:
		return "- Unknown plan.\n"
	}
}
