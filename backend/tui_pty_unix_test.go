//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/creack/pty"
)

const (
	tuiDecoyProfileIP = "198.51.100.77"
	enterAltScreen    = "\x1b[?1049h"
	exitAltScreen     = "\x1b[?1049l"
	hideCursor        = "\x1b[?25l"
	showCursor        = "\x1b[?25h"
	ptyFilterHelper   = "SERVESTEAD_PTY_FILTER_HELPER"
	ptyFilterCtrlC    = "SERVESTEAD_PTY_FILTER_CTRL_C"
	ptyRunResult      = "SERVESTEAD_PTY_RUN_RESULT"
	ptyCloudCancel    = "SERVESTEAD_PTY_CLOUD_CANCEL"
)

type servesteadPTYTestCase struct {
	name          string
	input         []byte
	resize        bool
	wantExitCode  int
	wantErrorText string
}

func TestServesteadSetupPTY(t *testing.T) {
	binary := buildServesteadPTYBinary(t)

	tests := []servesteadPTYTestCase{
		{
			name:         "q exits cleanly after resize",
			input:        []byte("q"),
			resize:       true,
			wantExitCode: 0,
		},
		{
			name:          "ctrl-c cancels and restores the terminal",
			input:         []byte{3},
			wantExitCode:  1,
			wantErrorText: "error: setup cancelled",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testServesteadPTYCase(t, binary, test)
		})
	}
}

func TestProfileWorkspaceNavigationPTY(t *testing.T) {
	binary := buildServesteadPTYBinary(t)
	sessionRoot := t.TempDir()
	configRoot := filepath.Join(sessionRoot, "config")
	seedPTYNavigationProfile(t, configRoot)
	environment := isolatedPTYEnvironment(
		configRoot,
		filepath.Join(sessionRoot, "home"),
		filepath.Join(sessionRoot, "xdg"),
	)
	session := startPTYTestSession(t, binary, []string{"setup"}, environment)

	session.waitFor(t, "Servestead profiles", "profile picker")
	mark := session.capture.Len()
	session.write(t, []byte{'\r'}, "open seeded profile")
	session.waitForAfter(t, "Dashboard for PTY profile", mark, "profile dashboard")

	mark = session.capture.Len()
	session.write(t, []byte("h"), "open run history")
	session.waitForAfter(t, "Run history for PTY profile", mark, "run history")

	mark = session.capture.Len()
	session.write(t, []byte{27}, "return from run history")
	session.waitForAfter(t, "Dashboard for PTY profile", mark, "dashboard after history")

	mark = session.capture.Len()
	session.write(t, []byte("s"), "open stack manager")
	session.waitForAfter(t, "Standalone stacks", mark, "stack manager")

	mark = session.capture.Len()
	session.write(t, []byte{27}, "return from stack manager")
	session.waitForAfter(t, "Dashboard for PTY profile", mark, "dashboard after stacks")

	mark = session.capture.Len()
	session.write(t, []byte{27}, "return to profile picker")
	session.waitForAfter(t, "Servestead profiles", mark, "profile picker after dashboard")
	session.write(t, []byte("q"), "exit profile picker")

	output, exitCode := session.finish(t)
	if exitCode != 0 {
		t.Fatalf("profile workspace exited with code %d:\n%q", exitCode, output)
	}
	assertPTYTerminalRestored(t, output)
}

func TestLegacySetupNavigationPTY(t *testing.T) {
	binary := buildServesteadPTYBinary(t)
	sessionRoot := t.TempDir()
	environment := isolatedPTYEnvironment(
		filepath.Join(sessionRoot, "config"),
		filepath.Join(sessionRoot, "home"),
		filepath.Join(sessionRoot, "xdg"),
	)
	session := startPTYTestSession(t, binary, []string{"setup"}, environment)

	session.waitFor(t, "Servestead profiles", "profile picker")
	mark := session.capture.Len()
	session.write(t, []byte("jj\r"), "open advanced legacy setup")
	session.waitForAfter(t, "Choose what you want to do.", mark, "legacy setup menu")

	mark = session.capture.Len()
	session.write(t, []byte{'\r'}, "open legacy key setup")
	session.waitForAfter(t, "Private key path:", mark, "legacy setup input")

	mark = session.capture.Len()
	session.write(t, []byte{27}, "return to legacy setup menu")
	session.waitForAfter(t, "Choose what you want to do.", mark, "legacy menu after input")
	session.write(t, []byte("q"), "exit legacy setup")

	output, exitCode := session.finish(t)
	if exitCode != 0 {
		t.Fatalf("legacy setup exited with code %d:\n%q", exitCode, output)
	}
	if strings.Count(output, enterAltScreen) < 2 || strings.Count(output, exitAltScreen) < 2 {
		t.Fatalf("legacy handoff did not run both TUI programs in the alternate screen: %q", output)
	}
	assertPTYTerminalRestored(t, output)
}

func TestProfileRunResultsPTY(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		mode string
		want string
		key  []byte
	}{
		{name: "completion acknowledged with q", mode: "complete", want: "Run complete.", key: []byte("q")},
		{name: "failure acknowledged with escape", mode: "failure", want: "synthetic remote failure", key: []byte{27}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := append(os.Environ(), ptyRunResult+"="+test.mode, "TERM=xterm-256color")
			session := startPTYTestSession(t, executable, []string{"-test.run=^TestProfileRunResultPTYHelper$", "-test.v"}, environment)
			session.waitFor(t, test.want, "profile run result")
			session.write(t, test.key, "acknowledge profile run result")
			output, exitCode := session.finish(t)
			if exitCode != 0 {
				t.Fatalf("profile run result helper exited with code %d:\n%q", exitCode, output)
			}
			assertPTYTerminalRestored(t, output)
		})
	}
}

func TestProfileRunResultPTYHelper(t *testing.T) {
	mode := os.Getenv(ptyRunResult)
	if mode == "" {
		t.Skip("PTY helper subprocess")
	}
	messages := make(chan tea.Msg, 1)
	switch mode {
	case "complete":
		messages <- profileRunFinishedMsg{}
	case "failure":
		messages <- profileRunFinishedMsg{err: errors.New("synthetic remote failure"), status: runStatusFailed, stage: "stacks"}
	default:
		t.Fatalf("unknown PTY run result mode %q", mode)
	}
	model := newProfileRunModel(
		Profile{Name: "PTY profile", IP: "203.0.113.45"},
		setupConfig{Host: "203.0.113.45"},
		"pty-run",
		nil,
		"stacks",
		messages,
		func() {},
	)
	model.allowReturn = mode == "failure"
	finalModel, err := tea.NewProgram(model, tea.WithWindowSize(80, 24)).Run()
	if err != nil {
		t.Fatalf("run profile result PTY helper: %v", err)
	}
	result, ok := finalModel.(profileRunModel)
	if !ok || !result.done {
		t.Fatalf("unexpected profile result helper model: %#v", finalModel)
	}
	if mode == "complete" && (result.err != nil || result.returnToSetup) {
		t.Fatalf("unexpected completed run result: %+v", result)
	}
	if mode == "failure" && (result.err == nil || result.err.Error() != "synthetic remote failure" || !result.returnToSetup) {
		t.Fatalf("unexpected failed run result: %+v", result)
	}
}

func TestProfileCloudCancellationPTY(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	environment := append(os.Environ(), ptyCloudCancel+"=1", "TERM=xterm-256color")
	session := startPTYTestSession(t, executable, []string{"-test.run=^TestProfileCloudCancellationPTYHelper$", "-test.v"}, environment)

	session.waitFor(t, "Confirm DigitalOcean restart", "cloud confirmation")
	session.write(t, []byte{'\r'}, "start injected cloud action")
	session.waitFor(t, "Running DigitalOcean restart action", "running cloud action")
	session.write(t, []byte{3}, "cancel injected cloud action")
	session.waitFor(t, "Check DigitalOcean before retrying.", "cancelled cloud result")
	session.write(t, []byte("q"), "exit cloud actions")

	output, exitCode := session.finish(t)
	if exitCode != 0 {
		t.Fatalf("cloud cancellation helper exited with code %d:\n%q", exitCode, output)
	}
	assertPTYTerminalRestored(t, output)
}

func TestProfileCloudCancellationPTYHelper(t *testing.T) {
	if os.Getenv(ptyCloudCancel) != "1" {
		t.Skip("PTY helper subprocess")
	}
	fake := newDelayedCloudProvider("reboot")
	restore := replaceProvisionCloudProvider(fake)
	defer restore()

	model := newProfileSetupModel([]profileChoice{activeCloudProfileChoice()})
	model.selectedIndex = 0
	model.screen = profileSetupScreenCloudConfirm
	model.cloudAction = "restart"
	model.cloudTokenInput.SetValue("local-test-token")
	model.cloudConfirmInput.SetValue("restart 84")
	model.cloudTokenInput.Blur()
	model.cloudConfirmInput.Focus()
	model.focus = 1
	model.tuiContext = context.Background()

	finalModel, err := tea.NewProgram(model, tea.WithWindowSize(80, 24)).Run()
	if err != nil {
		t.Fatalf("run cloud cancellation PTY helper: %v", err)
	}
	result, ok := finalModel.(profileSetupModel)
	if !ok || !result.quit || result.cancelled || result.screen != profileSetupScreenCloud ||
		!strings.Contains(result.cloudNotice, "remote outcome is unknown") || fake.calls != 1 {
		t.Fatalf("unexpected cloud cancellation helper result: %#v", finalModel)
	}
	select {
	case <-fake.cancelled:
	default:
		t.Fatal("injected cloud provider did not observe cancellation")
	}
}

func TestProvisionFilterNavigationPTY(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestProvisionFilterNavigationPTYHelper$", "-test.v")
	command.Env = append(os.Environ(), ptyFilterHelper+"=1", "TERM=xterm-256color")
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start provisioning helper in a PTY: %v", err)
	}
	capture := newPTYCapture()
	readerDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(capture, terminal)
		close(readerDone)
	}()
	waited := false
	t.Cleanup(func() { cleanupPTYSession(command, terminal, cancel, readerDone, waited) })

	if !capture.waitFor("Choose a region", 5*time.Second) {
		t.Fatalf("provision region list did not render:\n%q", capture.String())
	}
	if _, err := terminal.WriteString("/ny"); err != nil {
		t.Fatalf("type provisioning filter: %v", err)
	}
	if !capture.waitFor("Filter: ny", 5*time.Second) {
		t.Fatalf("real PTY did not decode filter input:\n%q", capture.String())
	}
	if _, err := terminal.Write([]byte{27}); err != nil {
		t.Fatalf("clear provisioning filter: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := terminal.Write([]byte{27}); err != nil {
		t.Fatalf("navigate back from provisioning region: %v", err)
	}
	if !capture.waitFor("Enter the token, Droplet name", 5*time.Second) {
		t.Fatalf("Esc did not navigate back through the real PTY:\n%q", capture.String())
	}
	if _, err := terminal.Write([]byte{27}); err != nil {
		t.Fatalf("return from provisioning to setup: %v", err)
	}

	waitErr := command.Wait()
	waited = true
	if ctx.Err() != nil {
		t.Fatalf("provisioning helper hung: %v", ctx.Err())
	}
	_ = terminal.Close()
	waitForPTYReader(t, readerDone)
	if waitErr != nil {
		t.Fatalf("provisioning helper failed: %v\n%s", waitErr, capture.String())
	}
	assertPTYTerminalRestored(t, capture.String())
}

func TestProvisionFilterNavigationPTYHelper(t *testing.T) {
	if os.Getenv(ptyFilterHelper) != "1" {
		t.Skip("PTY helper subprocess")
	}
	model := newDigitalOceanProvisionModel(context.Background(), newFileProfileStore(t.TempDir()))
	model.screen = provisionScreenRegion
	model.regionList = newProvisionList("DigitalOcean regions", []list.Item{
		provisionListItem{index: 0, title: "New York", description: "nyc3"},
		provisionListItem{index: 1, title: "Amsterdam", description: "ams3"},
	})
	finalModel, err := tea.NewProgram(model, tea.WithWindowSize(80, 24)).Run()
	if err != nil {
		t.Fatalf("run provisioning PTY helper: %v", err)
	}
	result, ok := finalModel.(digitalOceanProvisionModel)
	if !ok || !result.returnToSetup || result.cancelled || result.screen != provisionScreenInput || result.regionList.FilterState() == list.Filtering {
		t.Fatalf("unexpected provisioning helper result: %#v", finalModel)
	}
}

func TestProvisionFilterCtrlCPTY(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestProvisionFilterCtrlCPTYHelper$", "-test.v")
	command.Env = append(os.Environ(), ptyFilterCtrlC+"=1", "TERM=xterm-256color")
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start provisioning filter helper in a PTY: %v", err)
	}
	capture := newPTYCapture()
	readerDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(capture, terminal)
		close(readerDone)
	}()
	waited := false
	t.Cleanup(func() { cleanupPTYSession(command, terminal, cancel, readerDone, waited) })

	if !capture.waitFor("Choose a region", 5*time.Second) {
		t.Fatalf("provision region list did not render:\n%q", capture.String())
	}
	if _, err := terminal.WriteString("/ny"); err != nil {
		t.Fatalf("type provisioning filter: %v", err)
	}
	if !capture.waitFor("Filter: ny", 5*time.Second) {
		t.Fatalf("real PTY did not decode filter input:\n%q", capture.String())
	}
	if _, err := terminal.Write([]byte{3}); err != nil {
		t.Fatalf("cancel provisioning while filtering: %v", err)
	}

	waitErr := command.Wait()
	waited = true
	if ctx.Err() != nil {
		t.Fatalf("provisioning filter helper hung: %v", ctx.Err())
	}
	_ = terminal.Close()
	waitForPTYReader(t, readerDone)
	if waitErr != nil {
		t.Fatalf("provisioning filter helper failed: %v\n%s", waitErr, capture.String())
	}
	assertPTYTerminalRestored(t, capture.String())
}

func TestProvisionFilterCtrlCPTYHelper(t *testing.T) {
	if os.Getenv(ptyFilterCtrlC) != "1" {
		t.Skip("PTY helper subprocess")
	}
	model := newDigitalOceanProvisionModel(context.Background(), newFileProfileStore(t.TempDir()))
	model.screen = provisionScreenRegion
	model.regionList = newProvisionList("DigitalOcean regions", []list.Item{
		provisionListItem{index: 0, title: "New York", description: "nyc3"},
		provisionListItem{index: 1, title: "Amsterdam", description: "ams3"},
	})
	finalModel, err := tea.NewProgram(model, tea.WithWindowSize(80, 24)).Run()
	if err != nil {
		t.Fatalf("run provisioning filter PTY helper: %v", err)
	}
	result, ok := finalModel.(digitalOceanProvisionModel)
	if !ok || !result.cancelled || result.screen != provisionScreenRegion {
		t.Fatalf("unexpected provisioning filter helper result: %#v", finalModel)
	}
}

func testServesteadPTYCase(t *testing.T, binary string, test servesteadPTYTestCase) {
	t.Helper()
	sessionRoot := t.TempDir()
	configRoot := filepath.Join(sessionRoot, "isolated-config")
	decoyHome := filepath.Join(sessionRoot, "decoy-home")
	decoyXDG := filepath.Join(sessionRoot, "decoy-xdg")
	seedPTYDecoyProfile(t, filepath.Join(decoyXDG, "servestead"))
	seedPTYDecoyProfile(t, filepath.Join(decoyHome, ".config", "servestead"))
	seedPTYDecoyProfile(t, filepath.Join(decoyHome, "Library", "Application Support", "servestead"))

	environment := isolatedPTYEnvironment(configRoot, decoyHome, decoyXDG)
	output, exitCode := runServesteadPTYSession(t, binary, environment, test.input, test.resize)
	assertPTYExit(t, output, exitCode, test)
	if strings.Contains(output, tuiDecoyProfileName) || strings.Contains(output, tuiDecoyProfileIP) {
		t.Fatalf("setup TUI leaked a real profile outside %s: %q", servesteadConfigDirEnv, output)
	}
	assertPTYTerminalRestored(t, output)
	assertPTYStoreEmpty(t, configRoot)
}

func assertPTYExit(t *testing.T, output string, exitCode int, test servesteadPTYTestCase) {
	t.Helper()
	if exitCode != test.wantExitCode {
		t.Fatalf("unexpected exit code: got %d, want %d\nterminal output:\n%q", exitCode, test.wantExitCode, output)
	}
	if test.wantErrorText != "" && !strings.Contains(output, test.wantErrorText) {
		t.Errorf("terminal output does not contain %q: %q", test.wantErrorText, output)
	}
	if test.wantErrorText == "" && strings.Contains(output, "error:") {
		t.Errorf("clean q exit printed an error: %q", output)
	}
}

func assertPTYStoreEmpty(t *testing.T, configRoot string) {
	t.Helper()
	profiles, err := newFileProfileStore(configRoot).List()
	if err != nil {
		t.Fatalf("list isolated profiles after session: %v", err)
	}
	if len(profiles) != 0 {
		t.Fatalf("setup TUI unexpectedly wrote %d profile(s)", len(profiles))
	}
}

func buildServesteadPTYBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "servestead")
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", binary, ".")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build real servestead binary: %v\n%s", err, output)
	}
	return binary
}

func seedPTYDecoyProfile(t *testing.T, root string) {
	t.Helper()
	store := newFileProfileStore(root)
	if _, err := store.Create(Profile{
		ID:   "real-profile-decoy",
		Name: tuiDecoyProfileName,
		IP:   tuiDecoyProfileIP,
	}); err != nil {
		t.Fatalf("create decoy profile in %s: %v", root, err)
	}
}

func seedPTYNavigationProfile(t *testing.T, root string) {
	t.Helper()
	store := newFileProfileStore(root)
	profile, err := store.Create(Profile{
		ID:             "pty-profile",
		Name:           "PTY profile",
		IP:             "203.0.113.45",
		InitialSSHUser: "root",
		AdminUser:      "servestead",
	})
	if err != nil {
		t.Fatalf("create PTY navigation profile: %v", err)
	}
	timestamp := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	const runID = "pty-complete-run"
	state := ProfileState{
		ActiveRunID: runID,
		Runs: map[string]SetupRun{
			runID: {
				ID:     runID,
				Status: runStatusComplete,
				Stages: map[string]SetupStageStatus{
					"bootstrap": {Status: stageStatusComplete, LastStarted: timestamp, LastEnded: timestamp},
				},
				CreatedAt: timestamp,
				UpdatedAt: timestamp,
			},
		},
	}
	if err := store.Save(profile, state); err != nil {
		t.Fatalf("save PTY navigation run history: %v", err)
	}
}

func isolatedPTYEnvironment(configRoot, home, xdgConfigHome string) []string {
	removed := map[string]struct{}{
		"CLOUDFLARE_API_KEY":                {},
		"CLOUDFLARE_API_TOKEN":              {},
		"DIGITALOCEAN_ACCESS_TOKEN":         {},
		"DIGITALOCEAN_API_TOKEN":            {},
		"DIGITALOCEAN_TOKEN":                {},
		"DO_API_TOKEN":                      {},
		"GH_TOKEN":                          {},
		"GITHUB_TOKEN":                      {},
		"HOME":                              {},
		"PANGOLIN_ADMIN_PASSWORD":           {},
		"PANGOLIN_TOKEN":                    {},
		"SERVESTEAD_CONFIG_DIR":             {},
		"SERVESTEAD_GITHUB_TOKEN":           {},
		"SERVESTEAD_VERIFY_IMAGE_MANIFESTS": {},
		"SOPS_AGE_KEY":                      {},
		"TERM":                              {},
		"XDG_CONFIG_HOME":                   {},
	}
	environment := make([]string, 0, len(os.Environ())+5)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if _, shouldRemove := removed[name]; shouldRemove {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment,
		servesteadConfigDirEnv+"="+configRoot,
		"HOME="+home,
		"XDG_CONFIG_HOME="+xdgConfigHome,
		"TERM=xterm-256color",
	)
}

type ptyTestSession struct {
	ctx        context.Context
	cancel     context.CancelFunc
	command    *exec.Cmd
	terminal   *os.File
	capture    *ptyCapture
	readerDone chan struct{}
	waited     bool
}

func startPTYTestSession(t *testing.T, executable string, args, environment []string) *ptyTestSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	command := exec.CommandContext(ctx, executable, args...)
	command.Env = environment
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		cancel()
		t.Fatalf("start PTY test session: %v", err)
	}
	capture := newPTYCapture()
	readerDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(capture, terminal)
		close(readerDone)
	}()
	session := &ptyTestSession{
		ctx:        ctx,
		cancel:     cancel,
		command:    command,
		terminal:   terminal,
		capture:    capture,
		readerDone: readerDone,
	}
	t.Cleanup(func() {
		cleanupPTYSession(session.command, session.terminal, session.cancel, session.readerDone, session.waited)
	})
	return session
}

func (session *ptyTestSession) waitFor(t *testing.T, expected, description string) {
	t.Helper()
	if !session.capture.waitFor(expected, 5*time.Second) {
		t.Fatalf("%s did not render %q:\n%q", description, expected, session.capture.String())
	}
}

func (session *ptyTestSession) waitForAfter(t *testing.T, expected string, offset int, description string) {
	t.Helper()
	if !session.capture.waitForAfter(expected, offset, 5*time.Second) {
		t.Fatalf("%s did not render %q after input:\n%q", description, expected, session.capture.String())
	}
}

func (session *ptyTestSession) write(t *testing.T, data []byte, description string) {
	t.Helper()
	if _, err := session.terminal.Write(data); err != nil {
		t.Fatalf("%s: %v", description, err)
	}
}

func (session *ptyTestSession) finish(t *testing.T) (string, int) {
	t.Helper()
	waitErr := session.command.Wait()
	session.waited = true
	if session.ctx.Err() != nil {
		t.Fatalf("PTY test session hung: %v\n%s", session.ctx.Err(), session.capture.String())
	}
	_ = session.terminal.Close()
	waitForPTYReader(t, session.readerDone)
	return session.capture.String(), ptyExitCode(t, waitErr)
}

func runServesteadPTYSession(t *testing.T, binary string, environment []string, input []byte, resize bool) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	command := exec.CommandContext(ctx, binary, "setup")
	command.Env = environment
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start servestead in a PTY: %v", err)
	}

	capture := newPTYCapture()
	readerDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(capture, terminal)
		close(readerDone)
	}()

	waited := false
	t.Cleanup(func() {
		cleanupPTYSession(command, terminal, cancel, readerDone, waited)
	})

	if !capture.waitFor("Servestead profiles", 5*time.Second) {
		t.Fatalf("profile picker did not render\nterminal output:\n%q", capture.String())
	}
	if resize {
		resizeServesteadPTY(t, terminal, capture)
	}
	if _, err := terminal.Write(input); err != nil {
		t.Fatalf("send terminal input: %v", err)
	}

	waitErr := command.Wait()
	waited = true
	if ctx.Err() != nil {
		t.Fatalf("servestead setup hung after terminal input: %v", ctx.Err())
	}
	_ = terminal.Close()
	waitForPTYReader(t, readerDone)
	return capture.String(), ptyExitCode(t, waitErr)
}

func cleanupPTYSession(command *exec.Cmd, terminal *os.File, cancel context.CancelFunc, readerDone <-chan struct{}, waited bool) {
	cancel()
	if !waited {
		_ = command.Process.Kill()
		_ = command.Wait()
	}
	_ = terminal.Close()
	select {
	case <-readerDone:
	case <-time.After(time.Second):
	}
}

func resizeServesteadPTY(t *testing.T, terminal *os.File, capture *ptyCapture) {
	t.Helper()
	if err := pty.Setsize(terminal, &pty.Winsize{Rows: 12, Cols: 40}); err != nil {
		t.Fatalf("resize PTY smaller: %v", err)
	}
	if !capture.waitFor("Terminal too small: 40x12", 5*time.Second) {
		t.Fatalf("setup TUI did not react to a PTY resize\nterminal output:\n%q", capture.String())
	}
	if err := pty.Setsize(terminal, &pty.Winsize{Rows: 24, Cols: 80}); err != nil {
		t.Fatalf("restore PTY size: %v", err)
	}
}

func waitForPTYReader(t *testing.T, readerDone <-chan struct{}) {
	t.Helper()
	select {
	case <-readerDone:
	case <-time.After(time.Second):
		t.Fatal("timed out draining PTY output")
	}
}

func ptyExitCode(t *testing.T, waitErr error) int {
	t.Helper()
	if waitErr == nil {
		return 0
	}
	exitError, ok := waitErr.(*exec.ExitError)
	if !ok {
		t.Fatalf("wait for servestead setup: %v", waitErr)
	}
	return exitError.ExitCode()
}

func assertPTYTerminalRestored(t *testing.T, output string) {
	t.Helper()
	for _, transition := range []struct {
		name  string
		enter string
		exit  string
	}{
		{name: "alternate screen", enter: enterAltScreen, exit: exitAltScreen},
		{name: "cursor", enter: hideCursor, exit: showCursor},
	} {
		enteredAt := strings.Index(output, transition.enter)
		exitedAt := strings.LastIndex(output, transition.exit)
		if enteredAt < 0 || exitedAt < 0 {
			t.Errorf("missing %s lifecycle sequences in terminal output: %q", transition.name, output)
			continue
		}
		if exitedAt <= enteredAt {
			t.Errorf("%s was not restored after activation", transition.name)
		}
	}
}

type ptyCapture struct {
	mu      sync.Mutex
	buffer  bytes.Buffer
	changed chan struct{}
}

func newPTYCapture() *ptyCapture {
	return &ptyCapture{changed: make(chan struct{}, 1)}
}

func (capture *ptyCapture) Write(data []byte) (int, error) {
	capture.mu.Lock()
	written, err := capture.buffer.Write(data)
	capture.mu.Unlock()
	select {
	case capture.changed <- struct{}{}:
	default:
	}
	return written, err
}

func (capture *ptyCapture) String() string {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture.buffer.String()
}

func (capture *ptyCapture) Len() int {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture.buffer.Len()
}

func (capture *ptyCapture) waitFor(expected string, timeout time.Duration) bool {
	return capture.waitForAfter(expected, 0, timeout)
}

func (capture *ptyCapture) waitForAfter(expected string, offset int, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		output := capture.String()
		if offset < 0 {
			offset = 0
		}
		if offset <= len(output) && strings.Contains(output[offset:], expected) {
			return true
		}
		select {
		case <-capture.changed:
		case <-timer.C:
			return false
		}
	}
}
