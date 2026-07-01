package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

const (
	setupTestHost                    = "203.0.113.10"
	setupTestDomain                  = "example.com"
	setupTestEmail                   = "admin@example.com"
	setupTestPrivateKey              = "/tmp/aegis-key"
	setupTestHome                    = "/tmp/aegis-home"
	setupTestHomePrivateKey          = "/tmp/aegis-home/id_ed25519"
	setupTestEnvPrivateKey           = "$SERVESTEAD_TEST_HOME/id_ed25519"
	setupTestUnexpectedConfigMessage = "unexpected config: %+v"
	setupTestProfileID               = "profile-1"
	setupTestFailedRunID             = "failed-run"
	setupTestLegacyRunID             = "legacy-run"
	setupTestOldPassword             = "old-password"
	setupTestNewAdminEmail           = "new-admin@example.com"
	setupTestNewPassword             = "new-password"
	setupTestComposeFilename         = "compose.yaml"
	setupTestAPIKeyEnvironment       = "API_KEY=secret\n"
)

func TestRunPreflightPassesWithRequiredKeys(t *testing.T) {
	directory := t.TempDir()
	privateKey := filepath.Join(directory, "id_ed25519")
	if err := os.WriteFile(privateKey, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	publicKey := filepath.Join(directory, "id_ed25519.pub")
	if err := os.WriteFile(publicKey, []byte("ssh-ed25519 AAAATEST user@example\n"), 0600); err != nil {
		t.Fatal(err)
	}

	config := setupConfig{
		Mode:               setupModeBootstrapHarden,
		PrivateKeyPath:     privateKey,
		AdminPublicKeyPath: publicKey,
	}
	var output bytes.Buffer
	err := runPreflight(config, &output)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"[ok] native ED25519 key generation",
		"[ok] native SSH runner",
		"[ok] private key",
		"[ok] admin public key",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("preflight output missing %q:\n%s", expected, output.String())
		}
	}
}

func TestRunPreflightFailsWhenRequiredKeyIsMissing(t *testing.T) {
	var output bytes.Buffer
	err := runPreflight(setupConfig{Mode: setupModeHardenOnly}, &output)
	if err == nil || err.Error() != "preflight checks failed" {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(output.String(), "[fail] private key") {
		t.Fatalf("missing failed key check in output:\n%s", output.String())
	}
}

func TestRunSetupWithoutIPRequiresTerminal(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runSetup(context.Background(), nil, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "interactive setup requires a terminal") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSetupConfigFromInputs(t *testing.T) {
	t.Setenv("SERVESTEAD_TEST_HOME", setupTestHome)
	model := setupModel{
		mode:   setupModeBootstrapHarden,
		inputs: setupInputs(setupModeBootstrapHarden),
	}
	values := []string{
		setupTestHost,
		"root",
		"servestead",
		setupTestEnvPrivateKey,
	}
	for index, value := range values {
		model.inputs[index].SetValue(value)
	}

	config, err := model.configFromInputs()
	if err != nil {
		t.Fatal(err)
	}
	if config.Host != setupTestHost || config.InitialSSHUser != "root" || config.AdminUser != "servestead" {
		t.Fatalf(setupTestUnexpectedConfigMessage, config)
	}
	if config.AdminPublicKeyPath != "/tmp/aegis-home/id_ed25519.pub" || config.PrivateKeyPath != setupTestHomePrivateKey {
		t.Fatalf("unexpected key paths: %+v", config)
	}
}

func TestSetupProviderKeyConfigFromInputs(t *testing.T) {
	t.Setenv("SERVESTEAD_TEST_HOME", setupTestHome)
	model := setupModel{
		mode:   setupModeProviderKey,
		inputs: setupInputs(setupModeProviderKey),
	}
	model.inputs[0].SetValue("$SERVESTEAD_TEST_HOME/provider")
	model.inputs[1].SetValue("provider-comment")

	config, err := model.configFromInputs()
	if err != nil {
		t.Fatal(err)
	}
	if config.ProviderKeyPath != "/tmp/aegis-home/provider" || config.ProviderKeyComment != "provider-comment" {
		t.Fatalf(setupTestUnexpectedConfigMessage, config)
	}
}

func TestSetupPlanSummaryGivesGuidance(t *testing.T) {
	summary := setupPlanSummary(setupConfig{
		Mode:               setupModeBootstrapHarden,
		Host:               setupTestHost,
		InitialSSHUser:     "root",
		AdminUser:          "servestead",
		PrivateKeyPath:     setupTestPrivateKey,
		AdminPublicKeyPath: "/tmp/aegis-key.pub",
	})
	if !strings.Contains(summary, "Connect to 203.0.113.10") || !strings.Contains(summary, "Harden the server") {
		t.Fatalf("unexpected summary: %q", summary)
	}
	if strings.Contains(summary, "Phase") {
		t.Fatalf("summary leaks implementation phase language: %q", summary)
	}
}

func TestSetupNetworkConfigFromInputs(t *testing.T) {
	t.Setenv("SERVESTEAD_TEST_HOME", setupTestHome)
	model := setupModel{
		mode:   setupModeNetwork,
		inputs: setupInputs(setupModeNetwork),
	}
	values := []string{
		setupTestHost,
		"servestead",
		setupTestEnvPrivateKey,
	}
	for index, value := range values {
		model.inputs[index].SetValue(value)
	}

	config, err := model.configFromInputs()
	if err != nil {
		t.Fatal(err)
	}
	if config.Host != setupTestHost || config.AdminUser != "servestead" || config.PrivateKeyPath != setupTestHomePrivateKey {
		t.Fatalf(setupTestUnexpectedConfigMessage, config)
	}
}

func TestSetupOptionsIncludeProxyMode(t *testing.T) {
	options := setupModeOptions()
	if int(setupModeProxy) >= len(options) {
		t.Fatalf("setupModeProxy index %d outside options %#v", setupModeProxy, options)
	}
	option := options[int(setupModeProxy)]
	if option.Label != "Deploy Pangolin and reverse proxy" {
		t.Fatalf("unexpected proxy option: %+v", option)
	}
}

func TestSetupProxyConfigFromInputs(t *testing.T) {
	t.Setenv("SERVESTEAD_TEST_HOME", setupTestHome)
	model := setupModel{
		mode:   setupModeProxy,
		inputs: setupInputs(setupModeProxy),
	}
	values := []string{
		setupTestHost,
		"servestead",
		setupTestEnvPrivateKey,
		setupTestDomain,
		setupTestEmail,
	}
	for index, value := range values {
		model.inputs[index].SetValue(value)
	}

	config, err := model.configFromInputs()
	if err != nil {
		t.Fatal(err)
	}
	if config.Host != setupTestHost || config.AdminUser != "servestead" || config.PrivateKeyPath != setupTestHomePrivateKey {
		t.Fatalf("unexpected SSH config: %+v", config)
	}
	if config.BaseDomain != setupTestDomain || config.LetsEncryptEmail != setupTestEmail || config.ServerSecret == "" || config.PangolinSetupToken == "" {
		t.Fatalf("unexpected proxy config: %+v", config)
	}
	if strings.Contains(setupPlanSummary(config), config.ServerSecret) || strings.Contains(setupPlanSummary(config), config.PangolinSetupToken) {
		t.Fatalf("setup summary exposes a generated secret")
	}
}

func TestSetupPlanSummaryIncludesProxyGuidance(t *testing.T) {
	summary := setupPlanSummary(setupConfig{
		Mode:             setupModeProxy,
		Host:             setupTestHost,
		AdminUser:        "servestead",
		PrivateKeyPath:   setupTestPrivateKey,
		BaseDomain:       setupTestDomain,
		LetsEncryptEmail: setupTestEmail,
		ServerSecret:     "secret",
	})
	for _, expected := range []string{
		"Deploy Traefik, Pangolin, Gerbil, Newt, Beszel, Dozzle, and Dockhand",
		"Required DNS: A pangolin.example.com -> 203.0.113.10, A beszel.example.com -> 203.0.113.10, A dozzle.example.com -> 203.0.113.10, A dockhand.example.com -> 203.0.113.10",
	} {
		if !strings.Contains(summary, expected) {
			t.Fatalf("summary missing %q:\n%s", expected, summary)
		}
	}
}

func TestRunSetupPlanRunsProxyMode(t *testing.T) {
	originalFactory := newProxyRemoteClient
	defer func() { newProxyRemoteClient = originalFactory }()

	directory := t.TempDir()
	privateKey := filepath.Join(directory, "id_ed25519")
	if err := os.WriteFile(privateKey, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	client := &recordingRemoteClient{}
	newProxyRemoteClient = func(_ context.Context, config proxyConfig, _, _ io.Writer) (remoteClient, error) {
		if config.BaseDomain != setupTestDomain || config.LetsEncryptEmail != setupTestEmail || config.ServerSecret != "secret" {
			t.Fatalf("unexpected proxy config: %+v", config)
		}
		return client, nil
	}

	var stdout, stderr bytes.Buffer
	err := runSetupPlan(context.Background(), setupConfig{
		Mode:             setupModeProxy,
		Host:             setupTestHost,
		AdminUser:        "servestead",
		PrivateKeyPath:   privateKey,
		BaseDomain:       setupTestDomain,
		LetsEncryptEmail: setupTestEmail,
		ServerSecret:     "secret",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if len(client.commands) != len(proxyTasks(proxyConfig{SSHUser: "servestead", BaseDomain: setupTestDomain, LetsEncryptEmail: setupTestEmail, ServerSecret: "secret"})) {
		t.Fatalf("unexpected proxy command count: %d", len(client.commands))
	}
	if !strings.Contains(stdout.String(), "Step 1/1: deploy Pangolin and reverse proxy stack.") {
		t.Fatalf("missing setup step output:\n%s", stdout.String())
	}
}

func TestPrepareProfileSetupGeneratesPersistentSecret(t *testing.T) {
	store := newFileProfileStore(t.TempDir())

	profile, state, config, err := prepareProfileSetup(setupCLIOptions{
		IP:               setupTestHost,
		PrivateKeyPath:   setupTestPrivateKey,
		BaseDomain:       setupTestDomain,
		LetsEncryptEmail: setupTestEmail,
	}, store, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if profile.ID == "" || state.Runs == nil {
		t.Fatalf("unexpected profile/state: %+v %+v", profile, state)
	}
	if config.Mode != setupModeFullRun || config.ServerSecret == "" || config.PangolinSetupToken == "" {
		t.Fatalf("unexpected full-run config: %+v", config)
	}
	if strings.Contains(setupPlanSummary(config), config.ServerSecret) || strings.Contains(setupPlanSummary(config), config.PangolinSetupToken) {
		t.Fatalf("full-run summary exposes a generated secret")
	}

	_, _, secondConfig, err := prepareProfileSetup(setupCLIOptions{
		IP:               setupTestHost,
		BaseDomain:       setupTestDomain,
		LetsEncryptEmail: setupTestEmail,
	}, store, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if secondConfig.ServerSecret != config.ServerSecret {
		t.Fatalf("setup did not reuse saved secret")
	}
	if secondConfig.PangolinSetupToken != config.PangolinSetupToken {
		t.Fatalf("setup did not reuse saved Pangolin setup token")
	}
}

func TestPrepareCompletedLegacyProfileDoesNotInventUndeployedSetupToken(t *testing.T) {
	t.Setenv("PANGOLIN_ADMIN_PASSWORD", "existing-password")
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{
		IP:               setupTestHost,
		InitialSSHUser:   "root",
		AdminUser:        "servestead",
		PrivateKeyPath:   setupTestPrivateKey,
		BaseDomain:       setupTestDomain,
		LetsEncryptEmail: setupTestEmail,
	})
	if err != nil {
		t.Fatal(err)
	}
	state := ProfileState{
		ActiveRunID: setupTestLegacyRunID,
		Runs: map[string]SetupRun{
			setupTestLegacyRunID: {
				ID:     setupTestLegacyRunID,
				Status: runStatusComplete,
				Stages: map[string]SetupStageStatus{
					"proxy": {Status: stageStatusComplete},
				},
			},
		},
	}
	if err := store.Save(profile, state); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSecrets(profile.ID, ProfileSecrets{ServerSecret: "server-secret"}); err != nil {
		t.Fatal(err)
	}

	_, _, config, err := prepareProfileSetup(setupCLIOptions{ProfileID: profile.ID}, store, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if config.PangolinSetupToken != "" {
		t.Fatalf("invented an undeployed setup token for a completed legacy profile: %q", config.PangolinSetupToken)
	}
	if config.PangolinAdminPassword != "existing-password" {
		t.Fatalf("existing administrator password was not imported")
	}
}

func TestPrepareFailedProxyUsesExplicitPangolinPasswordOverride(t *testing.T) {
	t.Setenv("PANGOLIN_ADMIN_PASSWORD", "existing-admin-password")
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{
		IP: setupTestHost, AdminUser: "servestead", PrivateKeyPath: setupTestPrivateKey,
		BaseDomain: setupTestDomain, LetsEncryptEmail: setupTestEmail,
	})
	if err != nil {
		t.Fatal(err)
	}
	state := ProfileState{
		ActiveRunID: setupTestFailedRunID,
		Runs: map[string]SetupRun{setupTestFailedRunID: {
			ID: setupTestFailedRunID, Status: runStatusFailed,
			Stages: map[string]SetupStageStatus{"proxy": {Status: stageStatusFailed}},
		}},
	}
	if err := store.Save(profile, state); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSecrets(profile.ID, ProfileSecrets{PangolinAdminPassword: "Aa1!stale-generated-password"}); err != nil {
		t.Fatal(err)
	}

	_, _, config, err := prepareProfileSetup(setupCLIOptions{ProfileID: profile.ID}, store, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if config.PangolinAdminPassword != "existing-admin-password" {
		t.Fatalf("password override was ignored: %q", config.PangolinAdminPassword)
	}
}

func TestPrepareProfileStageSetupPersistsChangedPangolinCredentialsForAPI(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{
		ID:                 setupTestProfileID,
		IP:                 setupTestHost,
		AdminUser:          "servestead",
		PrivateKeyPath:     setupTestPrivateKey,
		BaseDomain:         setupTestDomain,
		LetsEncryptEmail:   setupTestEmail,
		PangolinAdminEmail: "old-admin@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	state := ProfileState{
		ActiveRunID: "run-1",
		Runs: map[string]SetupRun{"run-1": {
			ID:     "run-1",
			Status: runStatusComplete,
			Stages: map[string]SetupStageStatus{
				"proxy": {Status: stageStatusComplete},
			},
		}},
	}
	if err := store.Save(profile, state); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSecrets(profile.ID, ProfileSecrets{
		ServerSecret:          "server-secret",
		PangolinAdminPassword: setupTestOldPassword,
	}); err != nil {
		t.Fatal(err)
	}

	_, _, config, err := prepareProfileStageSetup(setupCLIOptions{
		ProfileID:             profile.ID,
		PangolinAdminEmail:    setupTestNewAdminEmail,
		PangolinAdminPassword: setupTestNewPassword,
	}, store, "observability")
	if err != nil {
		t.Fatal(err)
	}
	if config.PangolinAdminEmail != setupTestNewAdminEmail || config.PangolinAdminPassword != setupTestNewPassword {
		t.Fatalf("changed Pangolin credentials were not reflected in setup config: %+v", config)
	}
	loadedProfile, _, err := store.Load(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loadedProfile.PangolinAdminEmail != setupTestNewAdminEmail {
		t.Fatalf("changed Pangolin admin email was not saved: %+v", loadedProfile)
	}
	secrets, err := store.LoadSecrets(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if secrets.PangolinAdminPassword != setupTestNewPassword {
		t.Fatalf("changed Pangolin admin password was not saved: %+v", secrets)
	}
	command := observabilityResourceVerifyCommand(observabilityConfig{
		BaseDomain:       config.BaseDomain,
		AdminEmail:       config.PangolinAdminEmail,
		PangolinPassword: config.PangolinAdminPassword,
	})
	for _, expected := range []string{`"email":"` + setupTestNewAdminEmail + `"`, `"password":"` + setupTestNewPassword + `"`} {
		if !strings.Contains(command, expected) {
			t.Fatalf("Pangolin API login command did not use changed credential %q:\n%s", expected, command)
		}
	}
}

func TestAdvancedSetupMasksPangolinPassword(t *testing.T) {
	inputs := setupAdvancedInputs(setupCLIOptions{PangolinAdminPassword: "secret"})
	if inputs[4].EchoMode != textinput.EchoPassword {
		t.Fatal("Pangolin administrator password input is not masked")
	}
}

func TestFailedProxyRetryCollectsPangolinCredentials(t *testing.T) {
	state := ProfileState{
		ActiveRunID: setupTestFailedRunID,
		Runs: map[string]SetupRun{setupTestFailedRunID: {
			Stages: map[string]SetupStageStatus{"proxy": {Status: stageStatusFailed}},
		}},
	}
	model := newProfileSetupModel([]profileChoice{{
		Profile: Profile{
			ID:               setupTestProfileID,
			IP:               setupTestHost,
			PrivateKeyPath:   setupTestPrivateKey,
			BaseDomain:       setupTestDomain,
			LetsEncryptEmail: setupTestEmail,
		},
		State: state,
	}})
	model.selectedIndex = 0
	model.setInputsFromChoice(false)
	model.stageTable.SetCursor(2)
	updated, _ := model.updateProfileDashboard(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	result := updated.(profileSetupModel)
	if result.screen != profileSetupScreenAdvanced || result.singleStage != "platform" {
		t.Fatalf("failed Platform retry did not request credentials: screen=%d stage=%q", result.screen, result.singleStage)
	}
}

func TestPlatformStageWithMissingProfileValuesOpensIntake(t *testing.T) {
	state := ProfileState{Runs: map[string]SetupRun{}}
	model := newProfileSetupModel([]profileChoice{{
		Profile: Profile{
			ID:             setupTestProfileID,
			IP:             setupTestHost,
			AdminUser:      "servestead",
			PrivateKeyPath: setupTestPrivateKey,
		},
		State: state,
	}})
	model.selectedIndex = 0
	model.setInputsFromChoice(false)
	model.stageTable = newProfileStageTable(&state)
	model.stageTable.SetCursor(2)

	updated, command := model.updateProfileDashboard(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	result := updated.(profileSetupModel)
	if command != nil || result.done || result.screen != profileSetupScreenIntake || result.singleStage != "platform" {
		t.Fatalf("Platform with missing values should open intake: screen=%d stage=%q done=%v", result.screen, result.singleStage, result.done)
	}
	if result.focus != 2 || !strings.Contains(result.err, "domain") {
		t.Fatalf("intake should focus the domain field with guidance: focus=%d err=%q", result.focus, result.err)
	}

	updated, command = result.Update(tea.KeyMsg{Type: tea.KeyEsc})
	result = updated.(profileSetupModel)
	if command != nil || result.screen != profileSetupScreenDashboard || result.singleStage != "" {
		t.Fatalf("leaving Platform intake should clear one-time stage: screen=%d stage=%q", result.screen, result.singleStage)
	}
	result.screen = profileSetupScreenReview
	result.singleStage = "platform"
	updated, command = result.Update(tea.KeyMsg{Type: tea.KeyEsc})
	result = updated.(profileSetupModel)
	if command != nil || result.screen != profileSetupScreenDashboard || result.singleStage != "" {
		t.Fatalf("leaving Platform review should clear one-time stage: screen=%d stage=%q", result.screen, result.singleStage)
	}
}

func TestOptionsForSelectedProfileUsesEditedIntakeValues(t *testing.T) {
	model := newProfileSetupModel([]profileChoice{{
		Profile: Profile{
			ID:             setupTestProfileID,
			IP:             setupTestHost,
			AdminUser:      "servestead",
			PrivateKeyPath: setupTestPrivateKey,
		},
		State: ProfileState{Runs: map[string]SetupRun{}},
	}})
	model.selectedIndex = 0
	model.setInputsFromChoice(false)
	model.inputs[2].SetValue(setupTestDomain)
	model.inputs[3].SetValue(setupTestEmail)

	options, err := model.optionsForSelectedProfile()
	if err != nil {
		t.Fatal(err)
	}
	if options.ProfileID != setupTestProfileID || options.BaseDomain != setupTestDomain || options.LetsEncryptEmail != setupTestEmail {
		t.Fatalf("selected profile options did not include edited intake values: %+v", options)
	}
	if options.PangolinAdminEmail != setupTestEmail {
		t.Fatalf("Pangolin admin email should default to edited Let's Encrypt email: %+v", options)
	}
}

func TestFailedStackSyncRetryCollectsPangolinCredentials(t *testing.T) {
	state := ProfileState{
		ActiveRunID: setupTestFailedRunID,
		Runs: map[string]SetupRun{setupTestFailedRunID: {
			Stages: map[string]SetupStageStatus{"stacks": {Status: stageStatusFailed}},
		}},
	}
	model := newProfileSetupModel([]profileChoice{{
		Profile: Profile{
			ID: setupTestProfileID, IP: setupTestHost, BaseDomain: setupTestDomain,
			LetsEncryptEmail: setupTestEmail, PangolinAdminEmail: setupTestEmail,
		},
		State:   state,
		Secrets: ProfileSecrets{PangolinAdminPassword: setupTestOldPassword},
	}})
	model.selectedIndex = 0
	model.setInputsFromChoice(false)
	model.screen = profileSetupScreenStacks
	model.stackGitStatus = "clean"
	updated, command := model.updateStacks(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	result := updated.(profileSetupModel)
	if command != nil || result.done || result.screen != profileSetupScreenAdvanced || result.singleStage != "stacks" {
		t.Fatalf("failed stack sync retry did not request credentials: screen=%d stage=%q done=%v", result.screen, result.singleStage, result.done)
	}
	if result.focus != 3 || !strings.Contains(result.err, "Pangolin admin email and password") {
		t.Fatalf("stack sync credential prompt is not focused on Pangolin credentials: focus=%d err=%q", result.focus, result.err)
	}
}

func TestFailedSingleStackRetryCollectsPangolinCredentials(t *testing.T) {
	state := ProfileState{
		ActiveRunID: setupTestFailedRunID,
		Runs: map[string]SetupRun{setupTestFailedRunID: {
			Stages: map[string]SetupStageStatus{"stack:site": {Status: stageStatusFailed}},
		}},
	}
	model := newProfileSetupModel([]profileChoice{{
		Profile: Profile{ID: setupTestProfileID, IP: setupTestHost, BaseDomain: setupTestDomain},
		State:   state,
		Secrets: ProfileSecrets{PangolinAdminPassword: setupTestOldPassword},
	}})
	model.selectedIndex = 0
	model.setInputsFromChoice(false)
	model.screen = profileSetupScreenStacks
	model.stackGitStatus = "clean"
	model.stacks = []editableStack{{Name: "site"}}
	model.stackTable = newStackTable(model.stacks, setupTestDomain, &state)
	updated, command := model.updateStacks(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	result := updated.(profileSetupModel)
	if command != nil || result.done || result.screen != profileSetupScreenAdvanced || result.singleStage != "stack:site" {
		t.Fatalf("failed single-stack retry did not request credentials: screen=%d stage=%q done=%v", result.screen, result.singleStage, result.done)
	}
}

func TestProfileSetupModelResumesSelectedProfile(t *testing.T) {
	choice := profileChoice{
		Profile: Profile{
			ID:               setupTestProfileID,
			Name:             "production",
			IP:               setupTestHost,
			InitialSSHUser:   "root",
			AdminUser:        "servestead",
			PrivateKeyPath:   setupTestPrivateKey,
			BaseDomain:       setupTestDomain,
			LetsEncryptEmail: setupTestEmail,
		},
		State: ProfileState{
			ActiveRunID: "run-1",
			Runs: map[string]SetupRun{
				"run-1": {
					ID:     "run-1",
					Status: runStatusComplete,
					Stages: map[string]SetupStageStatus{
						"bootstrap": {Status: stageStatusComplete},
					},
				},
			},
		},
	}
	model := newProfileSetupModel([]profileChoice{choice})
	model.selectedIndex = 0
	model.setInputsFromChoice(false)
	model.inputs[0].SetValue("198.51.100.20")

	options, err := model.optionsFromInputs()
	if err != nil {
		t.Fatal(err)
	}
	if options.ProfileID != setupTestProfileID || options.IP != setupTestHost {
		t.Fatalf("selected profile should preserve profile identity and IP: %+v", options)
	}
}

func TestProfileSetupModelRendersDashboardFromProfileState(t *testing.T) {
	state := ProfileState{
		ActiveRunID: "run-1",
		Runs: map[string]SetupRun{
			"run-1": {
				ID:     "run-1",
				Status: runStatusFailed,
				Stages: map[string]SetupStageStatus{
					"bootstrap": {Status: stageStatusComplete},
					"harden":    {Status: stageStatusFailed, LastError: "ufw command failed"},
					"network":   {Status: stageStatusPending},
					"proxy":     {Status: stageStatusPending},
				},
			},
		},
	}
	model := newProfileSetupModel([]profileChoice{{
		Profile: Profile{
			ID:               setupTestProfileID,
			Name:             "production",
			IP:               setupTestHost,
			PrivateKeyPath:   setupTestPrivateKey,
			BaseDomain:       setupTestDomain,
			LetsEncryptEmail: setupTestEmail,
		},
		State: state,
	}})
	model.selectedIndex = 0
	model.refreshDashboard()
	model.screen = profileSetupScreenDashboard
	view := model.View()
	for _, expected := range []string{"Dashboard for production", "Bootstrap", "complete", "Harden", "failed", "ufw command failed"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("dashboard missing %q:\n%s", expected, view)
		}
	}
}

func TestProfileDashboardStartsGuidedStackAdd(t *testing.T) {
	model, repository, composePath := newGuidedStackAddModel(t)
	result := openGuidedStackAddCompose(t, model, composePath)
	result = addGuidedStackRoute(t, result, 0)
	result = addGuidedStackRoute(t, result, 1)
	result = selectAdjacentStackEnvironment(t, result)
	result.stackInputs[0].SetValue("site")
	updated, _ := result.updateStackReview(tea.KeyMsg{Type: tea.KeyEnter})
	result = updated.(profileSetupModel)
	if result.screen != profileSetupScreenStacks || len(result.stacks) != 1 || result.stacks[0].Name != "site" {
		t.Fatalf("stack review did not save in-session: %+v", result)
	}
	if result.err != "" {
		t.Fatalf("stack review left an error: %s", result.err)
	}
	if len(result.stacks[0].Metadata.PublicResources) != 2 {
		t.Fatalf("multi-service routes were not saved: %+v", result.stacks[0].Metadata.PublicResources)
	}
	if _, err := os.Stat(filepath.Join(repository, filepath.FromSlash(observabilityComposeRepositoryPath))); err != nil {
		t.Fatalf("repository scaffold was not prepared before commit: %v", err)
	}
}

func newGuidedStackAddModel(t *testing.T) (profileSetupModel, string, string) {
	t.Helper()
	requireGit(t)
	directory := t.TempDir()
	repository := filepath.Join(directory, "repository")
	if err := os.MkdirAll(repository, 0700); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, repository, "init")
	composePath := filepath.Join(directory, setupTestComposeFilename)
	if err := os.WriteFile(composePath, []byte(`services:
  web:
    image: nginx
    expose: [80]
  api:
    image: example/api
    expose: [3000]
`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, ".env"), []byte(setupTestAPIKeyEnvironment), 0600); err != nil {
		t.Fatal(err)
	}
	model := newProfileSetupModel([]profileChoice{{
		Profile: Profile{ID: setupTestProfileID, IP: setupTestHost, ConfigRepositoryPath: repository},
		State:   ProfileState{Runs: map[string]SetupRun{}},
	}})
	model.selectedIndex = 0
	model.screen = profileSetupScreenDashboard
	return model, repository, composePath
}

func openGuidedStackAddCompose(t *testing.T, model profileSetupModel, composePath string) profileSetupModel {
	t.Helper()
	updated, _ := model.updateProfileDashboard(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	result := updated.(profileSetupModel)
	if result.screen != profileSetupScreenStacks {
		t.Fatalf("stack shortcut did not open stack manager: %v (%s)", result.screen, result.err)
	}
	updated, _ = result.updateStacks(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	result = updated.(profileSetupModel)
	if result.screen != profileSetupScreenStackCompose {
		t.Fatalf("stack shortcut did not open Compose intake: %v", result.screen)
	}
	if result.stackComposeManual {
		t.Fatal("Compose intake did not start in file-browser mode")
	}
	updated, _ = result.updateStackCompose(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	result = updated.(profileSetupModel)
	if !result.stackComposeManual {
		t.Fatal("manual Compose path fallback did not open")
	}
	result.stackComposeInput.SetValue(composePath)
	updated, _ = result.updateStackCompose(tea.KeyMsg{Type: tea.KeyEnter})
	result = updated.(profileSetupModel)
	if result.screen != profileSetupScreenStackServices || len(result.stackResources) != 0 {
		t.Fatalf("Compose intake did not open private-by-default service selection: %v %+v", result.screen, result.stackResources)
	}
	return result
}

func addGuidedStackRoute(t *testing.T, result profileSetupModel, serviceIndex int) profileSetupModel {
	t.Helper()
	if serviceIndex > 0 {
		for index := 0; index < serviceIndex; index++ {
			updated, _ := result.updateStackServices(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
			result = updated.(profileSetupModel)
		}
	}
	updated, _ := result.updateStackServices(tea.KeyMsg{Type: tea.KeyEnter})
	result = updated.(profileSetupModel)
	if result.screen != profileSetupScreenStackResourceEditor || result.stackResourceInputs[1].Value() != result.stackServices[serviceIndex].Name {
		t.Fatalf("first service route editor did not open: %v %q", result.screen, result.stackResourceInputs[1].Value())
	}
	if serviceIndex == 0 && strings.Contains(result.stackResourceEditorView(), "Resource ID") {
		t.Fatal("advanced route fields were shown by default")
	}
	result = openGuidedStackAdvancedFields(t, result, serviceIndex)
	updated, _ = result.updateStackResourceEditor(tea.KeyMsg{Type: tea.KeyCtrlS})
	result = updated.(profileSetupModel)
	if result.screen != profileSetupScreenStackServices || len(result.stackResources) != serviceIndex+1 {
		t.Fatalf("route was not retained: %+v", result.stackResources)
	}
	return result
}

func openGuidedStackAdvancedFields(t *testing.T, result profileSetupModel, serviceIndex int) profileSetupModel {
	t.Helper()
	if serviceIndex != 0 {
		return result
	}
	updated, _ := result.updateStackResourceEditor(tea.KeyMsg{Type: tea.KeyCtrlX})
	result = updated.(profileSetupModel)
	if !strings.Contains(result.stackResourceEditorView(), "Resource ID") {
		t.Fatal("advanced route fields did not open")
	}
	return result
}

func selectAdjacentStackEnvironment(t *testing.T, result profileSetupModel) profileSetupModel {
	t.Helper()
	updated, _ := result.updateStackServices(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	result = updated.(profileSetupModel)
	if result.screen != profileSetupScreenStackEnvironment || len(result.stackEnvironmentOptions) != 3 {
		t.Fatalf("runtime environment choices were not prepared: %+v", result.stackEnvironmentOptions)
	}
	updated, _ = result.updateStackEnvironment(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	result = updated.(profileSetupModel)
	updated, _ = result.updateStackEnvironment(tea.KeyMsg{Type: tea.KeyEnter})
	result = updated.(profileSetupModel)
	if result.screen != profileSetupScreenStackReview || len(result.stackEnvironmentKeys) != 1 {
		t.Fatalf("adjacent environment did not open review: %v %+v", result.screen, result.stackEnvironmentKeys)
	}
	return result
}

func TestSetupResumeReturnsToStackManagerForStackStages(t *testing.T) {
	requireGit(t)
	repository := t.TempDir()
	runGitCommand(t, repository, "init")
	model := newProfileSetupModel([]profileChoice{{
		Profile: Profile{ID: setupTestProfileID, IP: setupTestHost, ConfigRepositoryPath: repository},
		State:   ProfileState{Runs: map[string]SetupRun{}},
	}})

	applySetupResume(&model, resumeAfterStage(setupTestProfileID, "stacks"))
	if model.selectedIndex != 0 || model.screen != profileSetupScreenStacks {
		t.Fatalf("stack stage did not resume stack manager: %+v", model)
	}

	applySetupResume(&model, resumeAfterStage(setupTestProfileID, "harden"))
	if model.screen != profileSetupScreenDashboard {
		t.Fatalf("non-stack stage did not resume dashboard: %+v", model.screen)
	}
}

func TestStackFilePickersSelectComposeAndShowHiddenEnvironmentFiles(t *testing.T) {
	directory := t.TempDir()
	composePath := filepath.Join(directory, setupTestComposeFilename)
	if err := os.WriteFile(composePath, []byte("services:\n  web:\n    image: nginx\n    expose: [80]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, ".env"), []byte("TOKEN=secret\n"), 0600); err != nil {
		t.Fatal(err)
	}

	model := newProfileSetupModel(nil)
	model.screen = profileSetupScreenStackCompose
	model.stackComposePicker = newStackFilePicker(directory, []string{".yaml", ".yml"}, false)
	message := model.stackComposePicker.Init()()
	updated, _ := model.updateStackCompose(message)
	model = updated.(profileSetupModel)
	updated, _ = model.updateStackCompose(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(profileSetupModel)
	if model.screen != profileSetupScreenStackServices || model.stackComposePath != composePath {
		t.Fatalf("Compose picker did not select the file: %v %q", model.screen, model.stackComposePath)
	}

	picker := newStackFilePicker(directory, nil, true)
	message = picker.Init()()
	picker, _ = picker.Update(message)
	if !strings.Contains(picker.View(), ".env") {
		t.Fatal("environment picker did not show hidden files")
	}
}

func TestStackEditorManagesMultipleResourcesAndRuntimeEnvironment(t *testing.T) {
	model, store, profile, repository := newMultiResourceStackEditorModel(t)
	model = editFirstStackResourceSubdomain(t, model)
	model = saveStackEditorRuntimeEnvironment(t, model)
	assertStackEditorSavedRuntimeEnvironment(t, store, profile.ID, repository)
}

func newMultiResourceStackEditorModel(t *testing.T) (profileSetupModel, ProfileStore, Profile, string) {
	t.Helper()
	requireGit(t)
	repository := t.TempDir()
	runGitCommand(t, repository, "init")
	compose := []byte(`services:
  web:
    image: nginx
    expose: [80]
  api:
    image: example/api
    expose: [3000]
`)
	options := stackAddOptions{
		Name: "suite",
		Resources: []stackPublicResource{
			{ID: "web", Service: "web", Name: "Web", Subdomain: "web", Port: 80, Protocol: "http"},
			{ID: "api", Service: "api", Name: "API", Subdomain: "api", Port: 3000, Protocol: "http"},
		},
	}
	if err := writeEditableStack(repository, "", options, compose); err != nil {
		t.Fatal(err)
	}
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{IP: setupTestHost, ConfigRepositoryPath: repository})
	if err != nil {
		t.Fatal(err)
	}
	state := ProfileState{Runs: map[string]SetupRun{}}
	if err := store.Save(profile, state); err != nil {
		t.Fatal(err)
	}
	stacks, err := loadEditableStacks(repository)
	if err != nil {
		t.Fatal(err)
	}
	model := newProfileSetupModel([]profileChoice{{Profile: profile, State: state}})
	model.profileStore = store
	model.selectedIndex = 0
	model.openStackEditor(stacks[0])
	if model.screen != profileSetupScreenStackEditor || len(model.stackResources) != 2 || model.err != "" {
		t.Fatalf("multi-resource stack did not open: %+v", model.stackResources)
	}
	return model, store, profile, repository
}

func editFirstStackResourceSubdomain(t *testing.T, model profileSetupModel) profileSetupModel {
	t.Helper()
	updated, _ := model.updateStackEditor(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	model = updated.(profileSetupModel)
	if model.screen != profileSetupScreenStackResourceEditor {
		t.Fatalf("resource editor did not open: %v", model.screen)
	}
	model.stackResourceInputs[3].SetValue("app")
	updated, _ = model.updateStackResourceEditor(tea.KeyMsg{Type: tea.KeyCtrlS})
	model = updated.(profileSetupModel)
	if model.stackResources[0].Subdomain != "app" {
		t.Fatalf("resource edit was not retained: %+v", model.stackResources)
	}
	return model
}

func saveStackEditorRuntimeEnvironment(t *testing.T, model profileSetupModel) profileSetupModel {
	t.Helper()
	environmentPath := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(environmentPath, []byte(setupTestAPIKeyEnvironment), 0600); err != nil {
		t.Fatal(err)
	}
	updated, _ := model.updateStackEditor(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	model = updated.(profileSetupModel)
	model.stackEnvironmentMode = stackEnvironmentManual
	model.stackEnvironmentInput.SetValue(environmentPath)
	updated, _ = model.updateStackEnvironment(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(profileSetupModel)
	updated, _ = model.updateStackEditor(tea.KeyMsg{Type: tea.KeyCtrlS})
	model = updated.(profileSetupModel)
	if model.screen != profileSetupScreenStacks || model.err != "" {
		t.Fatalf("stack editor did not save: %v %s", model.screen, model.err)
	}
	return model
}

func assertStackEditorSavedRuntimeEnvironment(t *testing.T, store ProfileStore, profileID, repository string) {
	t.Helper()
	secrets, err := store.LoadSecrets(profileID)
	if err != nil {
		t.Fatal(err)
	}
	if secrets.StackEnvironments["suite"] != setupTestAPIKeyEnvironment {
		t.Fatal("runtime environment was not saved in profile secrets")
	}
	reloaded, err := loadEditableStacks(repository)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded[0].Metadata.PublicResources) != 2 ||
		reloaded[0].Metadata.PublicResources[0].Subdomain != "app" {
		t.Fatalf("multi-resource metadata was not saved: %+v", reloaded[0].Metadata)
	}
}

func TestProfileDashboardDetectsRepositoryStacks(t *testing.T) {
	requireGit(t)
	repository := t.TempDir()
	runGitCommand(t, repository, "init")
	stackDirectory := filepath.Join(repository, "stacks", "arrs")
	if err := os.MkdirAll(stackDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stackDirectory, setupTestComposeFilename), []byte(testApplicationCompose), 0600); err != nil {
		t.Fatal(err)
	}
	metadata := `version: 1
public_resources:
  - id: web
    service: web
    name: Arrs
    subdomain: arrs
    port: 80
    protocol: http
    sso: true
    healthcheck:
      enabled: true
      path: /
`
	if err := os.WriteFile(filepath.Join(stackDirectory, stackMetadataFilename), []byte(metadata), 0600); err != nil {
		t.Fatal(err)
	}
	model := newProfileSetupModel([]profileChoice{{
		Profile: Profile{
			ID: setupTestProfileID, IP: setupTestHost, BaseDomain: setupTestDomain,
			ConfigRepositoryPath: repository,
		},
		State: ProfileState{Runs: map[string]SetupRun{}},
	}})
	model.selectedIndex = 0
	model.screen = profileSetupScreenDashboard
	model.refreshDashboard()
	if model.err != "" {
		t.Fatal(model.err)
	}
	if len(model.stacks) != 1 || model.stacks[0].Name != "arrs" {
		t.Fatalf("dashboard did not detect arrs stack: %+v", model.stacks)
	}
	view := model.View()
	if !strings.Contains(view, "Standalone stacks (1)") || !strings.Contains(view, "arrs") {
		t.Fatalf("dashboard does not show detected stack:\n%s", view)
	}
	model.screen = profileSetupScreenStacks
	updated, command := model.updateStacks(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	blocked := updated.(profileSetupModel)
	if command != nil || blocked.done || !strings.Contains(blocked.err, "uncommitted") {
		t.Fatalf("dirty single-stack deployment was not blocked in the TUI: %+v", blocked)
	}

	updated, command = model.updateStacks(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	blocked = updated.(profileSetupModel)
	if command != nil || blocked.done || !strings.Contains(blocked.err, "uncommitted") {
		t.Fatalf("dirty repository sync was not blocked: %+v", blocked)
	}

	runGitCommand(t, repository, "add", "stacks")
	runGitCommand(t, repository, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "Add arrs")
	model.refreshStacks()
	if model.stackSyncStatus != "sync required" {
		t.Fatalf("committed repository drift not detected: %q", model.stackSyncStatus)
	}
	updated, command = model.updateStacks(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	syncing := updated.(profileSetupModel)
	if command == nil || !syncing.done || syncing.singleStage != "stacks" {
		t.Fatalf("clean repository did not start synchronization: %+v", syncing)
	}
}

func TestProfileStackManagerReviewsComposeOnlyStack(t *testing.T) {
	model, stackDirectory := newComposeOnlyStackManagerModel(t)
	assertComposeOnlyStackDraft(t, model)
	saved := reviewAndSaveComposeOnlyStack(t, model)
	if _, err := os.Stat(filepath.Join(stackDirectory, stackMetadataFilename)); err != nil {
		t.Fatalf("metadata was not created: %v", err)
	}
	if len(saved.stacks) != 1 || saved.stacks[0].MetadataMissing {
		t.Fatalf("saved stack still appears as draft: %+v", saved.stacks)
	}
}

func newComposeOnlyStackManagerModel(t *testing.T) (profileSetupModel, string) {
	t.Helper()
	requireGit(t)
	repository := t.TempDir()
	runGitCommand(t, repository, "init")
	stackDirectory := filepath.Join(repository, "stacks", "seerr")
	if err := os.MkdirAll(stackDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stackDirectory, setupTestComposeFilename), []byte(testApplicationCompose), 0600); err != nil {
		t.Fatal(err)
	}
	state := ProfileState{Runs: map[string]SetupRun{}}
	model := newProfileSetupModel([]profileChoice{{
		Profile: Profile{
			ID: setupTestProfileID, IP: setupTestHost, BaseDomain: setupTestDomain,
			ConfigRepositoryPath: repository,
		},
		State: state,
	}})
	model.selectedIndex = 0
	model.screen = profileSetupScreenStacks
	model.refreshStacks()
	if model.err != "" {
		t.Fatal(model.err)
	}
	return model, stackDirectory
}

func assertComposeOnlyStackDraft(t *testing.T, model profileSetupModel) {
	t.Helper()
	if len(model.stacks) != 1 || !model.stacks[0].MetadataMissing {
		t.Fatalf("compose-only stack was not shown as a draft: %+v", model.stacks)
	}
	view := model.stacksView()
	if !strings.Contains(view, "draft") || !strings.Contains(view, "needs review") {
		t.Fatalf("draft stack guidance missing:\n%s", view)
	}
	updated, command := model.updateStacks(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	blocked := updated.(profileSetupModel)
	if command != nil || blocked.done || !strings.Contains(blocked.err, "needs review") {
		t.Fatalf("draft deployment was not blocked: %+v", blocked)
	}
}

func reviewAndSaveComposeOnlyStack(t *testing.T, model profileSetupModel) profileSetupModel {
	t.Helper()
	updated, command := model.updateStacks(tea.KeyMsg{Type: tea.KeyEnter})
	reviewing := updated.(profileSetupModel)
	if command != nil || reviewing.screen != profileSetupScreenStackEditor || !reviewing.stackMetadataMissing {
		t.Fatalf("draft stack did not open for review: %+v", reviewing)
	}
	updated, command = reviewing.updateStackEditor(tea.KeyMsg{Type: tea.KeyCtrlS})
	saved := updated.(profileSetupModel)
	if command != nil || saved.screen != profileSetupScreenStacks || saved.err != "" {
		t.Fatalf("draft stack did not save: %+v", saved)
	}
	return saved
}

func TestProfileSetupUsesAvailableTerminalHeight(t *testing.T) {
	model := newProfileSetupModel(nil)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 140, Height: 60})
	result := updated.(profileSetupModel)
	if result.profileList.Height() <= 18 {
		t.Fatalf("profile list is still capped at the old height: %d", result.profileList.Height())
	}
	if result.profileList.Height() != 53 {
		t.Fatalf("profile list did not use available height: %d", result.profileList.Height())
	}
}

func TestProfileSetupModelCollectsNewProfileInputs(t *testing.T) {
	model := newProfileSetupModel(nil)
	model.setInputsFromOptions(setupCLIOptions{})
	values := []string{setupTestHost, setupTestPrivateKey, setupTestDomain, setupTestEmail}
	for index, value := range values {
		model.inputs[index].SetValue(value)
	}
	model.advanced[0].SetValue("production")
	model.advanced[1].SetValue("ubuntu")
	model.advanced[2].SetValue("servestead")

	options, err := model.optionsFromInputs()
	if err != nil {
		t.Fatal(err)
	}
	if options.ProfileID != "" || options.IP != setupTestHost || options.Name != "production" {
		t.Fatalf("unexpected new profile options: %+v", options)
	}
	if options.InitialSSHUser != "ubuntu" || options.AdminUser != "servestead" {
		t.Fatalf("unexpected advanced users: %+v", options)
	}
}

func TestProfileSetupRepositoryFlowDefaultsToCreateBeforeSSH(t *testing.T) {
	model := newProfileSetupModel(nil)
	if model.repositoryMode != "create" {
		t.Fatalf("unexpected default repository mode: %s", model.repositoryMode)
	}
	model.screen = profileSetupScreenRepository
	view := model.View()
	for _, expected := range []string{
		"Create a new local repository",
		"Use an existing local checkout",
		"Clone a GitHub repository",
		"after plan confirmation and before any SSH commands run",
	} {
		if !strings.Contains(view, expected) {
			t.Fatalf("repository choice view missing %q:\n%s", expected, view)
		}
	}

	updated, _ := model.updateRepositoryChoice(tea.KeyMsg{Type: tea.KeyEnter})
	result := updated.(profileSetupModel)
	if result.screen != profileSetupScreenReview || result.repositoryMode != "create" {
		t.Fatalf("create choice did not proceed to review: %+v", result)
	}
	review := result.reviewView()
	if !strings.Contains(review, "Repository") ||
		!strings.Contains(review, "Create and commit a new local configuration repository") ||
		!strings.Contains(review, "Prepare the local configuration repository before SSH execution") {
		t.Fatalf("review does not explain repository timing:\n%s", review)
	}
}

func TestProfileSetupPlatformReviewExplainsStageAndRepository(t *testing.T) {
	model := newProfileSetupModel([]profileChoice{{
		Profile: Profile{
			ID:               setupTestProfileID,
			Name:             "production",
			IP:               setupTestHost,
			AdminUser:        "servestead",
			PrivateKeyPath:   setupTestPrivateKey,
			BaseDomain:       setupTestDomain,
			LetsEncryptEmail: setupTestEmail,
		},
		State: ProfileState{Runs: map[string]SetupRun{}},
	}})
	model.selectedIndex = 0
	model.setInputsFromChoice(false)
	model.singleStage = "platform"
	model.screen = profileSetupScreenReview
	model.refreshPlanPreview()

	review := model.reviewView()
	for _, expected := range []string{
		"Review Platform run",
		"Selected action: Platform",
		"Network: configure Docker networking and UFW",
		"Proxy: deploy Pangolin",
		"Observability: deploy Beszel",
		"No new VPS will be provisioned",
		"Bootstrap and Harden will not run",
		"Repository preparation",
		"SSH execution starts only after repository preparation succeeds",
	} {
		if !strings.Contains(review, expected) {
			t.Fatalf("Platform review missing %q:\n%s", expected, review)
		}
	}
	if strings.Contains(review, "Profile action:") || strings.Contains(review, "Repository action:") {
		t.Fatalf("Platform review still uses ambiguous action labels:\n%s", review)
	}
}

func TestProfileSetupRepositoryFlowUsesExistingCheckout(t *testing.T) {
	repository := t.TempDir()
	requireGit(t)
	runGitCommand(t, repository, "init", "-b", "main")
	model := newProfileSetupModel(nil)
	model.repositoryList.Select(1)
	updated, _ := model.updateRepositoryChoice(tea.KeyMsg{Type: tea.KeyEnter})
	result := updated.(profileSetupModel)
	if result.screen != profileSetupScreenRepositoryDetails || result.repositoryMode != "existing" {
		t.Fatalf("existing choice did not request details: %+v", result)
	}
	for index, value := range []string{setupTestHost, setupTestPrivateKey, setupTestDomain, setupTestEmail} {
		result.inputs[index].SetValue(value)
	}
	result.repositoryInputs[0].SetValue(repository)
	updated, _ = result.updateRepositoryDetails(tea.KeyMsg{Type: tea.KeyEnter})
	result = updated.(profileSetupModel)
	if result.screen != profileSetupScreenReview {
		t.Fatalf("existing checkout did not proceed to review: %s", result.err)
	}
	options, err := result.optionsFromInputs()
	if err != nil {
		t.Fatal(err)
	}
	if options.ConfigRepositoryPath != repository {
		t.Fatalf("existing repository was not retained: %+v", options)
	}
}

func TestProfileSetupRepositoryFlowRejectsMissingExistingCheckout(t *testing.T) {
	model := newProfileSetupModel(nil)
	model.repositoryMode = "existing"
	model.repositoryInputs[0].SetValue(filepath.Join(t.TempDir(), "missing"))
	updated, _ := model.updateRepositoryDetails(tea.KeyMsg{Type: tea.KeyEnter})
	result := updated.(profileSetupModel)
	if result.screen == profileSetupScreenReview || !strings.Contains(result.err, "choose create") {
		t.Fatalf("missing checkout was not rejected clearly: %+v", result)
	}
}

func TestProfileSetupModelFreshUsesAdminAsInitialUser(t *testing.T) {
	choice := profileChoice{
		Profile: Profile{
			ID:               setupTestProfileID,
			Name:             "production",
			IP:               setupTestHost,
			InitialSSHUser:   "root",
			AdminUser:        "servestead",
			PrivateKeyPath:   setupTestPrivateKey,
			BaseDomain:       setupTestDomain,
			LetsEncryptEmail: setupTestEmail,
		},
		State: ProfileState{
			ActiveRunID: "run-1",
			Runs: map[string]SetupRun{
				"run-1": {
					ID:     "run-1",
					Status: runStatusComplete,
					Stages: map[string]SetupStageStatus{
						"bootstrap": {Status: stageStatusComplete},
					},
				},
			},
		},
	}
	model := newProfileSetupModel([]profileChoice{choice})
	model.selectedIndex = 0
	model.setInputsFromChoice(false)

	updatedModel, _ := model.updateProfileDashboard(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	result := updatedModel.(profileSetupModel)
	options, err := result.optionsFromInputs()
	if err != nil {
		t.Fatal(err)
	}
	if !options.Fresh || options.ProfileID != setupTestProfileID {
		t.Fatalf("fresh setup should keep source profile id for seeding: %+v", options)
	}
	if options.InitialSSHUser != "servestead" || options.AdminUser != "servestead" {
		t.Fatalf("fresh setup should use admin user for existing server login: %+v", options)
	}
}

func TestProfileSetupModelDeleteConfirmation(t *testing.T) {
	choice := profileChoice{
		Profile: Profile{
			ID: setupTestProfileID,
			IP: setupTestHost,
		},
		State: ProfileState{Runs: map[string]SetupRun{}},
	}
	model := newProfileSetupModel([]profileChoice{choice})
	model.selectedIndex = 0
	model.screen = profileSetupScreenDashboard

	updatedModel, _ := model.updateProfileDashboard(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	result := updatedModel.(profileSetupModel)
	if result.screen != profileSetupScreenDeleteConfirm {
		t.Fatalf("delete key did not open confirmation: %+v", result.screen)
	}
	updatedModel, _ = result.updateProfileDeleteConfirm(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	result = updatedModel.(profileSetupModel)
	if result.deleteProfileID != setupTestProfileID {
		t.Fatalf("delete confirmation did not capture profile id: %+v", result)
	}
}

func TestPrepareProfileSetupLoadsSelectedProfileID(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	first, err := store.Create(Profile{
		ID:               "first-profile",
		Name:             "first",
		IP:               setupTestHost,
		PrivateKeyPath:   "/tmp/first-key",
		BaseDomain:       "first.example.com",
		LetsEncryptEmail: "first@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create(Profile{
		ID:               "second-profile",
		Name:             "second",
		IP:               setupTestHost,
		PrivateKeyPath:   "/tmp/second-key",
		BaseDomain:       "second.example.com",
		LetsEncryptEmail: "second@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}

	profile, _, config, err := prepareProfileSetup(setupCLIOptions{
		ProfileID: second.ID,
	}, store, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if profile.ID != second.ID || profile.ID == first.ID {
		t.Fatalf("selected profile was not loaded: %+v", profile)
	}
	if config.PrivateKeyPath != "/tmp/second-key" || config.BaseDomain != "second.example.com" {
		t.Fatalf("unexpected selected profile config: %+v", config)
	}
}

func TestPrepareFreshProfileSeedsBootstrapFromExistingProfile(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	source, err := store.Create(Profile{
		ID:               "source-profile",
		Name:             "source",
		IP:               setupTestHost,
		InitialSSHUser:   "root",
		AdminUser:        "servestead",
		PrivateKeyPath:   setupTestPrivateKey,
		BaseDomain:       setupTestDomain,
		LetsEncryptEmail: setupTestEmail,
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceState := ProfileState{
		ActiveRunID: "run-1",
		Runs: map[string]SetupRun{
			"run-1": {
				ID:     "run-1",
				Status: runStatusComplete,
				Stages: map[string]SetupStageStatus{
					"bootstrap": {Status: stageStatusComplete},
				},
			},
		},
	}
	if err := store.Save(source, sourceState); err != nil {
		t.Fatal(err)
	}

	profile, state, config, err := prepareProfileSetup(setupCLIOptions{
		IP:               setupTestHost,
		ProfileID:        source.ID,
		Fresh:            true,
		Name:             "fresh",
		PrivateKeyPath:   setupTestPrivateKey,
		BaseDomain:       setupTestDomain,
		LetsEncryptEmail: setupTestEmail,
	}, store, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if profile.ID == source.ID {
		t.Fatalf("fresh setup reused source profile id: %+v", profile)
	}
	if config.InitialSSHUser != "servestead" || config.AdminUser != "servestead" {
		t.Fatalf("fresh setup did not inherit admin login: %+v", config)
	}
	if !completedSetupStages(state)["bootstrap"] {
		t.Fatalf("fresh setup did not seed completed bootstrap: %+v", state)
	}
}

func TestPrepareFreshProfileKeepsInitialUserWhenBootstrapNotComplete(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	source, err := store.Create(Profile{
		ID:               "source-profile",
		Name:             "source",
		IP:               setupTestHost,
		InitialSSHUser:   "root",
		AdminUser:        "servestead",
		PrivateKeyPath:   setupTestPrivateKey,
		BaseDomain:       setupTestDomain,
		LetsEncryptEmail: setupTestEmail,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, state, config, err := prepareProfileSetup(setupCLIOptions{
		IP:               setupTestHost,
		ProfileID:        source.ID,
		Fresh:            true,
		Name:             "fresh",
		PrivateKeyPath:   setupTestPrivateKey,
		BaseDomain:       setupTestDomain,
		LetsEncryptEmail: setupTestEmail,
	}, store, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if config.InitialSSHUser != "root" || config.AdminUser != "servestead" {
		t.Fatalf("fresh setup should keep initial user until bootstrap is complete: %+v", config)
	}
	if completedSetupStages(state)["bootstrap"] {
		t.Fatalf("fresh setup should not seed bootstrap from incomplete source: %+v", state)
	}
}

func TestRunProfileSetupPlanExecutesFullRunAndPersistsState(t *testing.T) {
	clients, restore := replaceSetupPlanRemoteClients()
	defer restore()
	privateKey := writeSetupPlanKeypair(t)
	store := newFileProfileStore(t.TempDir())
	profile, state, config := prepareFullRunTestProfile(t, store, privateKey)

	var stdout, stderr bytes.Buffer
	if err := runProfileSetupPlan(context.Background(), store, profile, state, config, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	assertSetupPlanCompleted(t, store, profile.ID, clients)
	if strings.Contains(stdout.String(), config.ServerSecret) {
		t.Fatalf("stdout exposed generated server secret")
	}

	commandCounts := setupPlanCommandCounts(clients)
	profile, state, config = prepareFullRunTestProfile(t, store, privateKey)
	stdout.Reset()
	stderr.Reset()
	if err := runProfileSetupPlan(context.Background(), store, profile, state, config, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	assertSetupPlanSkippedCompletedStages(t, stdout.String(), clients, commandCounts)
}

func replaceSetupPlanRemoteClients() ([]*recordingRemoteClient, func()) {
	originalBootstrap := newBootstrapRemoteClient
	originalHardening := newHardeningRemoteClient
	originalNetwork := newNetworkRemoteClient
	originalProxy := newProxyRemoteClient
	originalObservability := newObservabilityRemoteClient
	restore := func() {
		newBootstrapRemoteClient = originalBootstrap
		newHardeningRemoteClient = originalHardening
		newNetworkRemoteClient = originalNetwork
		newProxyRemoteClient = originalProxy
		newObservabilityRemoteClient = originalObservability
	}
	clients := []*recordingRemoteClient{
		{}, {}, {}, {}, {},
	}
	newBootstrapRemoteClient = func(_ context.Context, _ bootstrapConfig, _, _ io.Writer) (remoteClient, error) {
		return clients[0], nil
	}
	newHardeningRemoteClient = func(_ context.Context, _ hardeningConfig, _, _ io.Writer) (remoteClient, error) {
		return clients[1], nil
	}
	newNetworkRemoteClient = func(_ context.Context, _ networkConfig, _, _ io.Writer) (remoteClient, error) {
		return clients[2], nil
	}
	newProxyRemoteClient = func(_ context.Context, _ proxyConfig, _, _ io.Writer) (remoteClient, error) {
		return clients[3], nil
	}
	newObservabilityRemoteClient = func(_ context.Context, _ observabilityConfig, _, _ io.Writer) (remoteClient, error) {
		return clients[4], nil
	}
	return clients, restore
}

func writeSetupPlanKeypair(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	privateKey := filepath.Join(directory, "id_ed25519")
	publicKey := filepath.Join(directory, "id_ed25519.pub")
	if err := os.WriteFile(privateKey, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publicKey, []byte("ssh-ed25519 AAAATEST user@example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return privateKey
}

func prepareFullRunTestProfile(t *testing.T, store ProfileStore, privateKey string) (Profile, ProfileState, setupConfig) {
	t.Helper()
	profile, state, config, err := prepareProfileSetup(setupCLIOptions{
		IP:               setupTestHost,
		PrivateKeyPath:   privateKey,
		BaseDomain:       setupTestDomain,
		LetsEncryptEmail: setupTestEmail,
	}, store, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return profile, state, config
}

func assertSetupPlanCompleted(t *testing.T, store ProfileStore, profileID string, clients []*recordingRemoteClient) {
	t.Helper()
	for index, client := range clients {
		if len(client.commands) == 0 {
			t.Fatalf("stage %d did not run", index)
		}
	}
	_, loadedState, err := store.Load(profileID)
	if err != nil {
		t.Fatal(err)
	}
	run := loadedState.Runs[loadedState.ActiveRunID]
	if run.Status != runStatusComplete {
		t.Fatalf("unexpected run state: %+v", run)
	}
	for _, stage := range []string{"bootstrap", "harden", "network", "proxy", "observability"} {
		if run.Stages[stage].Status != stageStatusComplete {
			t.Fatalf("stage %s not complete: %+v", stage, run.Stages[stage])
		}
	}
}

func setupPlanCommandCounts(clients []*recordingRemoteClient) []int {
	commandCounts := make([]int, len(clients))
	for index, client := range clients {
		commandCounts[index] = len(client.commands)
	}
	return commandCounts
}

func assertSetupPlanSkippedCompletedStages(t *testing.T, output string, clients []*recordingRemoteClient, commandCounts []int) {
	t.Helper()
	for index, client := range clients {
		if len(client.commands) != commandCounts[index] {
			t.Fatalf("stage %d reran on completed profile", index)
		}
	}
	for _, expected := range []string{
		"Step 1/5: bootstrap administrative access already complete; skipping.",
		"Step 2/5: harden server already complete; skipping.",
		"Step 3/5: configure Docker networking and UFW already complete; skipping.",
		"Step 4/5: deploy Pangolin and reverse proxy stack already complete; skipping.",
		"Step 5/5: deploy observability stack already complete; skipping.",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("second run output missing %q:\n%s", expected, output)
		}
	}
}

func TestProfileRunModelRendersTaskProgressAndLogs(t *testing.T) {
	model := newProfileRunModel(
		Profile{Name: "production", IP: setupTestHost},
		setupConfig{
			Host:               setupTestHost,
			InitialSSHUser:     "root",
			AdminUser:          "servestead",
			PrivateKeyPath:     setupTestPrivateKey,
			AdminPublicKeyPath: "/tmp/aegis-key.pub",
			BaseDomain:         setupTestDomain,
			LetsEncryptEmail:   setupTestEmail,
			ServerSecret:       "secret",
		},
		"run-1",
		map[string]bool{"bootstrap": true},
		"",
		make(chan tea.Msg),
		func() {
			// The test only needs a non-nil cancel callback.
		},
	)
	model.applyTaskEvent(TaskEvent{Type: TaskStarted, RunID: "run-1", Stage: "harden", TaskName: "Validate sysctl keys"})
	model.applyTaskEvent(TaskEvent{Type: TaskLogLine, RunID: "run-1", Stage: "harden", Stream: "stdout", Line: "remote output"})
	model.applyTaskEvent(TaskEvent{Type: TaskSucceeded, RunID: "run-1", Stage: "harden", TaskName: "Validate sysctl keys"})

	view := model.View()
	for _, expected := range []string{
		"Servestead setup run",
		"production (203.0.113.10)",
		"Tasks:",
		"Current: Harden - Validate sysctl keys",
		"Harden     running",
		"remote output",
	} {
		if !strings.Contains(view, expected) {
			t.Fatalf("run view missing %q:\n%s", expected, view)
		}
	}
	if strings.Contains(view, "Harden stdout: remote output") {
		t.Fatalf("run view should not prefix streamed log output with stage and stream:\n%s", view)
	}
}

func TestProfileRunFailureRemainsInTUIOnEscape(t *testing.T) {
	runErr := errors.New("uncommitted changes under stacks/ block deployment")
	model := newProfileRunFailureModel(
		Profile{Name: "production", IP: setupTestHost},
		setupConfig{Host: setupTestHost, ConfigRepositoryPath: "/tmp/servestead-config"},
		nil,
		"stacks",
		"Preparing configuration repository: /tmp/servestead-config\n",
		runErr,
		true,
	)
	if model.Init() != nil {
		t.Fatal("completed failure model should not start background commands")
	}
	view := model.View()
	for _, expected := range []string{
		"Failed",
		"Sync stacks",
		"Preparing configuration repository: /tmp/servestead-config",
		runErr.Error(),
		"stopped before remote execution",
	} {
		if !strings.Contains(view, expected) {
			t.Fatalf("failure view missing %q:\n%s", expected, view)
		}
	}

	if !strings.Contains(view, "esc returns to setup") {
		t.Fatalf("failure view did not advertise escape return:\n%s", view)
	}

	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	result := updated.(profileRunModel)
	if command == nil || !result.done || result.err == nil || !result.returnToSetup {
		t.Fatalf("escape did not request returning to setup: %+v", result)
	}
	_, command = result.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if command == nil {
		t.Fatal("q should explicitly exit the completed run view")
	}

	presented := tuiPresentedError{err: runErr}
	if !errors.Is(presented, runErr) {
		t.Fatal("presented TUI error does not preserve the underlying failure")
	}
}

func TestProfileRunCompletedEscapeReturnsWhenParentSetupExists(t *testing.T) {
	model := newProfileRunModel(
		Profile{Name: "production", IP: setupTestHost},
		setupConfig{Host: setupTestHost},
		"run-1",
		nil,
		"stacks",
		make(chan tea.Msg),
		func() {
			// The completed model never invokes cancellation.
		},
	)
	model.done = true
	model.allowReturn = true

	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	result := updated.(profileRunModel)
	if command == nil || !result.returnToSetup {
		t.Fatalf("escape did not request return from completed result: %+v", result)
	}
}

func TestProfileSetupModelSelectsSingleStageRun(t *testing.T) {
	model := newProfileSetupModel([]profileChoice{{
		Profile: Profile{
			ID:             setupTestProfileID,
			Name:           "production",
			IP:             setupTestHost,
			AdminUser:      "servestead",
			PrivateKeyPath: setupTestPrivateKey,
		},
		State: ProfileState{
			ActiveRunID: "run-1",
			Runs: map[string]SetupRun{
				"run-1": {
					ID:     "run-1",
					Status: runStatusComplete,
					Stages: map[string]SetupStageStatus{
						"bootstrap": {Status: stageStatusComplete},
						"harden":    {Status: stageStatusComplete},
					},
				},
			},
		},
	}})
	model.selectedIndex = 0
	model.refreshDashboard()
	model.screen = profileSetupScreenDashboard
	model.stageTable.SetCursor(1)

	updatedModel, cmd := model.updateProfileDashboard(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if cmd == nil {
		t.Fatal("single-stage selection should quit the setup picker")
	}
	result := updatedModel.(profileSetupModel)
	if !result.done || result.singleStage != "harden" {
		t.Fatalf("unexpected single-stage result: %+v", result)
	}
	options, err := result.optionsForSelectedProfile()
	if err != nil {
		t.Fatal(err)
	}
	if options.ProfileID != setupTestProfileID || options.IP != setupTestHost {
		t.Fatalf("unexpected profile options: %+v", options)
	}
}

func TestProfileDashboardCombinesPlatformStages(t *testing.T) {
	state := ProfileState{Runs: map[string]SetupRun{
		"run-1": {
			Stages: map[string]SetupStageStatus{
				"network":       {Status: stageStatusComplete},
				"proxy":         {Status: stageStatusComplete},
				"observability": {Status: stageStatusComplete},
			},
		},
	}}
	rows := profileStageRows(&state)
	if len(rows) != 3 {
		t.Fatalf("dashboard should expose three actions, got %#v", rows)
	}
	if rows[2][0] != "Platform" || rows[2][1] != stageStatusComplete {
		t.Fatalf("network, proxy, and observability were not combined: %#v", rows)
	}
}

func TestPlatformStageRunsNetworkProxyAndObservability(t *testing.T) {
	originalNetwork := newNetworkRemoteClient
	originalProxy := newProxyRemoteClient
	originalObservability := newObservabilityRemoteClient
	defer func() {
		newNetworkRemoteClient = originalNetwork
		newProxyRemoteClient = originalProxy
		newObservabilityRemoteClient = originalObservability
	}()

	clients := []*recordingRemoteClient{{}, {}, {}}
	newNetworkRemoteClient = func(_ context.Context, _ networkConfig, _, _ io.Writer) (remoteClient, error) {
		return clients[0], nil
	}
	newProxyRemoteClient = func(_ context.Context, _ proxyConfig, _, _ io.Writer) (remoteClient, error) {
		return clients[1], nil
	}
	newObservabilityRemoteClient = func(_ context.Context, config observabilityConfig, _, _ io.Writer) (remoteClient, error) {
		if len(config.Stacks) != 0 {
			t.Fatalf("Platform should not deploy application stacks: %+v", config.Stacks)
		}
		return clients[2], nil
	}
	config := setupConfig{
		Host: setupTestHost, AdminUser: "servestead", PrivateKeyPath: "/tmp/key",
		BaseDomain: setupTestDomain, LetsEncryptEmail: setupTestEmail,
		ServerSecret: "secret", PangolinSetupToken: "00000000000000000000000000000000",
		PangolinAdminEmail: setupTestEmail,
	}
	stageRun := setupStageRun{
		profile: Profile{IP: config.Host}, config: config, runID: "run-1",
		stdout: io.Discard, stderr: io.Discard,
	}
	if err := runSetupStage(context.Background(), stageRun, "platform"); err != nil {
		t.Fatal(err)
	}
	for index, client := range clients {
		if len(client.commands) == 0 {
			t.Fatalf("Platform component %d did not run", index)
		}
	}
}

func TestStackRepositoryStageRunsWhenAllStacksWereDeleted(t *testing.T) {
	originalObservability := newObservabilityRemoteClient
	defer func() { newObservabilityRemoteClient = originalObservability }()
	client := &recordingRemoteClient{}
	newObservabilityRemoteClient = func(_ context.Context, _ observabilityConfig, _, _ io.Writer) (remoteClient, error) {
		return client, nil
	}
	config := setupConfig{
		Host: setupTestHost, AdminUser: "servestead", PrivateKeyPath: "/tmp/key",
		BaseDomain: setupTestDomain, PangolinAdminEmail: setupTestEmail,
	}
	stageRun := setupStageRun{
		profile: Profile{IP: config.Host}, config: config, runID: "run-1",
		stdout: io.Discard, stderr: io.Discard,
	}
	if err := runSetupStage(context.Background(), stageRun, "stacks"); err != nil {
		t.Fatal(err)
	}
	if len(client.commands) != 1 || !strings.Contains(client.commands[0], ".stack-*.deployment") {
		t.Fatalf("empty repository sync did not reconcile deleted stacks: %#v", client.commands)
	}
}

func TestSuccessfulStackRepositorySyncRecordsCommit(t *testing.T) {
	requireGit(t)
	originalObservability := newObservabilityRemoteClient
	defer func() { newObservabilityRemoteClient = originalObservability }()
	newObservabilityRemoteClient = func(_ context.Context, _ observabilityConfig, _, _ io.Writer) (remoteClient, error) {
		return &recordingRemoteClient{}, nil
	}

	directory := t.TempDir()
	privateKey := filepath.Join(directory, "id_ed25519")
	if err := os.WriteFile(privateKey, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	store := newFileProfileStore(filepath.Join(directory, "profiles"))
	profile, err := store.Create(Profile{
		IP: setupTestHost, AdminUser: "servestead", PrivateKeyPath: privateKey,
		BaseDomain: setupTestDomain, LetsEncryptEmail: setupTestEmail,
		PangolinAdminEmail:   setupTestEmail,
		ConfigRepositoryPath: filepath.Join(directory, "repository"),
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, state, config, err := prepareProfileStageSetup(setupCLIOptions{ProfileID: profile.ID}, store, "stacks")
	if err != nil {
		t.Fatal(err)
	}
	if err := runProfileSetupStagePlan(context.Background(), profileSetupPlanRun{
		store: store, profile: profile, state: state, config: config,
		stdout: io.Discard, stderr: io.Discard,
	}, "stacks"); err != nil {
		t.Fatal(err)
	}
	_, state, err = store.Load(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.StackRepositoryCommit == "" {
		t.Fatal("successful stack synchronization did not record the reconciled commit")
	}
	head, err := stackRepositoryHead(context.Background(), profile.ConfigRepositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	if state.StackRepositoryCommit != head {
		t.Fatalf("recorded commit %q does not match repository HEAD %q", state.StackRepositoryCommit, head)
	}
}

func TestProfileSetupModelDashboardUsesVForReview(t *testing.T) {
	model := newProfileSetupModel([]profileChoice{{
		Profile: Profile{
			ID:               setupTestProfileID,
			Name:             "production",
			IP:               setupTestHost,
			InitialSSHUser:   "root",
			AdminUser:        "servestead",
			PrivateKeyPath:   setupTestPrivateKey,
			BaseDomain:       setupTestDomain,
			LetsEncryptEmail: setupTestEmail,
		},
		State: ProfileState{Runs: map[string]SetupRun{}},
	}})
	model.selectedIndex = 0
	model.setInputsFromChoice(false)
	model.refreshDashboard()
	model.screen = profileSetupScreenDashboard

	updatedModel, _ := model.updateProfileDashboard(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	result := updatedModel.(profileSetupModel)
	if result.screen != profileSetupScreenReview {
		t.Fatalf("v should open review, got screen %d", result.screen)
	}

	updatedModel, _ = model.updateProfileDashboard(tea.KeyMsg{Type: tea.KeyEnter})
	result = updatedModel.(profileSetupModel)
	if result.screen == profileSetupScreenReview {
		t.Fatal("enter should not open review from the dashboard")
	}
}

func TestProfileSetupModelEscapeBackAndQQuit(t *testing.T) {
	model := newProfileSetupModel([]profileChoice{{
		Profile: Profile{
			ID:             setupTestProfileID,
			Name:           "production",
			IP:             setupTestHost,
			PrivateKeyPath: setupTestPrivateKey,
		},
		State: ProfileState{Runs: map[string]SetupRun{}},
	}})
	model.selectedIndex = 0
	model.screen = profileSetupScreenReview

	updatedModel, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Fatal("esc should go back without quitting")
	}
	result := updatedModel.(profileSetupModel)
	if result.screen != profileSetupScreenDashboard || result.cancelled {
		t.Fatalf("esc should return to dashboard without cancelling: %+v", result)
	}

	updatedModel, cmd = result.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatal("q should quit")
	}
	result = updatedModel.(profileSetupModel)
	if !result.cancelled {
		t.Fatalf("q should mark setup cancelled: %+v", result)
	}
}

func TestPrepareProfileStageSetupDoesNotRequireProxyFieldsForHarden(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{
		ID:             setupTestProfileID,
		Name:           "production",
		IP:             setupTestHost,
		AdminUser:      "servestead",
		PrivateKeyPath: setupTestPrivateKey,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, _, config, err := prepareProfileStageSetup(setupCLIOptions{ProfileID: profile.ID}, store, "harden")
	if err != nil {
		t.Fatal(err)
	}
	if config.Mode != setupModeHardenOnly || config.BaseDomain != "" || config.LetsEncryptEmail != "" {
		t.Fatalf("unexpected harden stage config: %+v", config)
	}
}

func TestPrepareProfileStageSetupGeneratesPlatformSecrets(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	directory := t.TempDir()
	privateKey := filepath.Join(directory, "id_ed25519")
	if err := os.WriteFile(privateKey, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	profile, err := store.Create(Profile{
		ID:               setupTestProfileID,
		Name:             "production",
		IP:               setupTestHost,
		AdminUser:        "servestead",
		PrivateKeyPath:   privateKey,
		BaseDomain:       setupTestDomain,
		LetsEncryptEmail: setupTestEmail,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, _, config, err := prepareProfileStageSetup(setupCLIOptions{ProfileID: profile.ID}, store, "platform")
	if err != nil {
		t.Fatal(err)
	}
	if config.Mode != setupModeProxy {
		t.Fatalf("platform stage should use proxy-style preflight mode, got %v", config.Mode)
	}
	if config.ServerSecret == "" || config.PangolinSetupToken == "" || config.PangolinAdminEmail != setupTestEmail {
		t.Fatalf("platform stage did not hydrate generated secrets: %+v", config)
	}
	var output bytes.Buffer
	if err := runPreflight(config, &output); err != nil {
		t.Fatalf("platform preflight should not require missing admin public key: %v\n%s", err, output.String())
	}
	secrets, err := store.LoadSecrets(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if secrets.ServerSecret != config.ServerSecret || secrets.PangolinSetupToken != config.PangolinSetupToken {
		t.Fatalf("generated platform secrets were not persisted: config=%+v secrets=%+v", config, secrets)
	}
}

func TestRunProfileSetupStagePlanRerunsCompletedHardenStage(t *testing.T) {
	originalHardening := newHardeningRemoteClient
	defer func() { newHardeningRemoteClient = originalHardening }()

	directory := t.TempDir()
	privateKey := filepath.Join(directory, "id_ed25519")
	if err := os.WriteFile(privateKey, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	client := &recordingRemoteClient{}
	newHardeningRemoteClient = func(_ context.Context, config hardeningConfig, _, _ io.Writer) (remoteClient, error) {
		if config.Host != setupTestHost || config.SSHUser != "servestead" || config.PrivateKeyPath != privateKey {
			t.Fatalf("unexpected hardening config: %+v", config)
		}
		return client, nil
	}

	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{
		ID:             setupTestProfileID,
		Name:           "production",
		IP:             setupTestHost,
		AdminUser:      "servestead",
		PrivateKeyPath: privateKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	state := ProfileState{
		ActiveRunID: "run-1",
		Runs: map[string]SetupRun{
			"run-1": {
				ID:     "run-1",
				Status: runStatusComplete,
				Stages: map[string]SetupStageStatus{
					"harden": {Status: stageStatusComplete},
				},
			},
		},
	}
	if err := store.Save(profile, state); err != nil {
		t.Fatal(err)
	}
	profile, state, config, err := prepareProfileStageSetup(setupCLIOptions{ProfileID: profile.ID}, store, "harden")
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := runProfileSetupStagePlan(context.Background(), profileSetupPlanRun{
		store: store, profile: profile, state: state, config: config,
		stdout: &stdout, stderr: &stderr,
	}, "harden"); err != nil {
		t.Fatal(err)
	}
	if len(client.commands) != len(hardeningTasks()) {
		t.Fatalf("completed harden stage was not rerun, command count %d", len(client.commands))
	}
	_, loadedState, err := store.Load(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	run := loadedState.Runs[loadedState.ActiveRunID]
	if run.Status != runStatusComplete || run.Stages["harden"].Status != stageStatusComplete {
		t.Fatalf("unexpected one-time run state: %+v", run)
	}
	if !strings.Contains(stdout.String(), "Selected one-time stage: Harden") || strings.Contains(stdout.String(), "already complete; skipping") {
		t.Fatalf("unexpected one-time stage output:\n%s", stdout.String())
	}
}

func TestProfileRunLogWriterEmitsStructuredLogLines(t *testing.T) {
	events := []TaskEvent{}
	writer := &profileRunLogWriter{
		reporter: TaskReporterFunc(func(event TaskEvent) {
			events = append(events, event)
		}),
		runID:  "run-1",
		stage:  "proxy",
		stream: "stderr",
	}
	written, err := writer.Write([]byte("first line\nsecond line\npartial"))
	if err != nil {
		t.Fatal(err)
	}
	if written != len("first line\nsecond line\npartial") {
		t.Fatalf("unexpected written count: %d", written)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 complete log lines, got %#v", events)
	}
	for index, expected := range []string{"first line", "second line"} {
		if events[index].Type != TaskLogLine || events[index].RunID != "run-1" || events[index].Stage != "proxy" || events[index].Stream != "stderr" || events[index].Line != expected {
			t.Fatalf("unexpected event %d: %#v", index, events[index])
		}
	}
}
