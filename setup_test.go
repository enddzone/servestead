package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
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
	t.Setenv("AEGISNODE_TEST_HOME", "/tmp/aegis-home")
	model := setupModel{
		mode:   setupModeBootstrapHarden,
		inputs: setupInputs(setupModeBootstrapHarden),
	}
	values := []string{
		"203.0.113.10",
		"root",
		"aegisadmin",
		"$AEGISNODE_TEST_HOME/id_ed25519",
	}
	for index, value := range values {
		model.inputs[index].SetValue(value)
	}

	config, err := model.configFromInputs()
	if err != nil {
		t.Fatal(err)
	}
	if config.Host != "203.0.113.10" || config.InitialSSHUser != "root" || config.AdminUser != "aegisadmin" {
		t.Fatalf("unexpected config: %+v", config)
	}
	if config.AdminPublicKeyPath != "/tmp/aegis-home/id_ed25519.pub" || config.PrivateKeyPath != "/tmp/aegis-home/id_ed25519" {
		t.Fatalf("unexpected key paths: %+v", config)
	}
}

func TestSetupProviderKeyConfigFromInputs(t *testing.T) {
	t.Setenv("AEGISNODE_TEST_HOME", "/tmp/aegis-home")
	model := setupModel{
		mode:   setupModeProviderKey,
		inputs: setupInputs(setupModeProviderKey),
	}
	model.inputs[0].SetValue("$AEGISNODE_TEST_HOME/provider")
	model.inputs[1].SetValue("provider-comment")

	config, err := model.configFromInputs()
	if err != nil {
		t.Fatal(err)
	}
	if config.ProviderKeyPath != "/tmp/aegis-home/provider" || config.ProviderKeyComment != "provider-comment" {
		t.Fatalf("unexpected config: %+v", config)
	}
}

func TestSetupPlanSummaryGivesGuidance(t *testing.T) {
	summary := setupPlanSummary(setupConfig{
		Mode:               setupModeBootstrapHarden,
		Host:               "203.0.113.10",
		InitialSSHUser:     "root",
		AdminUser:          "aegisadmin",
		PrivateKeyPath:     "/tmp/aegis-key",
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
	t.Setenv("AEGISNODE_TEST_HOME", "/tmp/aegis-home")
	model := setupModel{
		mode:   setupModeNetwork,
		inputs: setupInputs(setupModeNetwork),
	}
	values := []string{
		"203.0.113.10",
		"aegisadmin",
		"$AEGISNODE_TEST_HOME/id_ed25519",
	}
	for index, value := range values {
		model.inputs[index].SetValue(value)
	}

	config, err := model.configFromInputs()
	if err != nil {
		t.Fatal(err)
	}
	if config.Host != "203.0.113.10" || config.AdminUser != "aegisadmin" || config.PrivateKeyPath != "/tmp/aegis-home/id_ed25519" {
		t.Fatalf("unexpected config: %+v", config)
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
	t.Setenv("AEGISNODE_TEST_HOME", "/tmp/aegis-home")
	model := setupModel{
		mode:   setupModeProxy,
		inputs: setupInputs(setupModeProxy),
	}
	values := []string{
		"203.0.113.10",
		"aegisadmin",
		"$AEGISNODE_TEST_HOME/id_ed25519",
		"example.com",
		"admin@example.com",
	}
	for index, value := range values {
		model.inputs[index].SetValue(value)
	}

	config, err := model.configFromInputs()
	if err != nil {
		t.Fatal(err)
	}
	if config.Host != "203.0.113.10" || config.AdminUser != "aegisadmin" || config.PrivateKeyPath != "/tmp/aegis-home/id_ed25519" {
		t.Fatalf("unexpected SSH config: %+v", config)
	}
	if config.BaseDomain != "example.com" || config.LetsEncryptEmail != "admin@example.com" || config.ServerSecret == "" || config.PangolinSetupToken == "" {
		t.Fatalf("unexpected proxy config: %+v", config)
	}
	if strings.Contains(setupPlanSummary(config), config.ServerSecret) || strings.Contains(setupPlanSummary(config), config.PangolinSetupToken) {
		t.Fatalf("setup summary exposes a generated secret")
	}
}

func TestSetupPlanSummaryIncludesProxyGuidance(t *testing.T) {
	summary := setupPlanSummary(setupConfig{
		Mode:             setupModeProxy,
		Host:             "203.0.113.10",
		AdminUser:        "aegisadmin",
		PrivateKeyPath:   "/tmp/aegis-key",
		BaseDomain:       "example.com",
		LetsEncryptEmail: "admin@example.com",
		ServerSecret:     "secret",
	})
	for _, expected := range []string{
		"Deploy Traefik, Pangolin, Gerbil, Newt, Beszel, and Dozzle",
		"Required DNS: A pangolin.example.com -> 203.0.113.10, A beszel.example.com -> 203.0.113.10, A dozzle.example.com -> 203.0.113.10",
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
		if config.BaseDomain != "example.com" || config.LetsEncryptEmail != "admin@example.com" || config.ServerSecret != "secret" {
			t.Fatalf("unexpected proxy config: %+v", config)
		}
		return client, nil
	}

	var stdout, stderr bytes.Buffer
	err := runSetupPlan(context.Background(), setupConfig{
		Mode:             setupModeProxy,
		Host:             "203.0.113.10",
		AdminUser:        "aegisadmin",
		PrivateKeyPath:   privateKey,
		BaseDomain:       "example.com",
		LetsEncryptEmail: "admin@example.com",
		ServerSecret:     "secret",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if len(client.commands) != len(proxyTasks(proxyConfig{SSHUser: "aegisadmin", BaseDomain: "example.com", LetsEncryptEmail: "admin@example.com", ServerSecret: "secret"})) {
		t.Fatalf("unexpected proxy command count: %d", len(client.commands))
	}
	if !strings.Contains(stdout.String(), "Step 1/1: deploy Pangolin and reverse proxy stack.") {
		t.Fatalf("missing setup step output:\n%s", stdout.String())
	}
}

func TestPrepareProfileSetupGeneratesPersistentSecret(t *testing.T) {
	store := newFileProfileStore(t.TempDir())

	profile, state, config, err := prepareProfileSetup(setupCLIOptions{
		IP:               "203.0.113.10",
		PrivateKeyPath:   "/tmp/aegis-key",
		BaseDomain:       "example.com",
		LetsEncryptEmail: "admin@example.com",
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
		IP:               "203.0.113.10",
		BaseDomain:       "example.com",
		LetsEncryptEmail: "admin@example.com",
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
		IP:               "203.0.113.10",
		InitialSSHUser:   "root",
		AdminUser:        "aegisadmin",
		PrivateKeyPath:   "/tmp/aegis-key",
		BaseDomain:       "example.com",
		LetsEncryptEmail: "admin@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	state := ProfileState{
		ActiveRunID: "legacy-run",
		Runs: map[string]SetupRun{
			"legacy-run": {
				ID:     "legacy-run",
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
		IP: "203.0.113.10", AdminUser: "aegisadmin", PrivateKeyPath: "/tmp/aegis-key",
		BaseDomain: "example.com", LetsEncryptEmail: "admin@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	state := ProfileState{
		ActiveRunID: "failed-run",
		Runs: map[string]SetupRun{"failed-run": {
			ID: "failed-run", Status: runStatusFailed,
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

func TestAdvancedSetupMasksPangolinPassword(t *testing.T) {
	inputs := setupAdvancedInputs(setupCLIOptions{PangolinAdminPassword: "secret"})
	if inputs[4].EchoMode != textinput.EchoPassword {
		t.Fatal("Pangolin administrator password input is not masked")
	}
}

func TestFailedProxyRetryCollectsPangolinCredentials(t *testing.T) {
	state := ProfileState{
		ActiveRunID: "failed-run",
		Runs: map[string]SetupRun{"failed-run": {
			Stages: map[string]SetupStageStatus{"proxy": {Status: stageStatusFailed}},
		}},
	}
	model := newProfileSetupModel([]profileChoice{{
		Profile: Profile{ID: "profile-1", BaseDomain: "example.com", LetsEncryptEmail: "admin@example.com"},
		State:   state,
	}})
	model.selectedIndex = 0
	model.setInputsFromChoice(false)
	model.stageTable.SetCursor(3)
	updated, _ := model.updateProfileDashboard(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	result := updated.(profileSetupModel)
	if result.screen != profileSetupScreenAdvanced || result.singleStage != "proxy" {
		t.Fatalf("failed Proxy retry did not request credentials: screen=%d stage=%q", result.screen, result.singleStage)
	}
}

func TestProfileSetupModelResumesSelectedProfile(t *testing.T) {
	choice := profileChoice{
		Profile: Profile{
			ID:               "profile-1",
			Name:             "production",
			IP:               "203.0.113.10",
			InitialSSHUser:   "root",
			AdminUser:        "aegisadmin",
			PrivateKeyPath:   "/tmp/aegis-key",
			BaseDomain:       "example.com",
			LetsEncryptEmail: "admin@example.com",
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
	if options.ProfileID != "profile-1" || options.IP != "203.0.113.10" {
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
			ID:               "profile-1",
			Name:             "production",
			IP:               "203.0.113.10",
			PrivateKeyPath:   "/tmp/aegis-key",
			BaseDomain:       "example.com",
			LetsEncryptEmail: "admin@example.com",
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

func TestProfileSetupModelCollectsNewProfileInputs(t *testing.T) {
	model := newProfileSetupModel(nil)
	model.setInputsFromOptions(setupCLIOptions{})
	values := []string{"203.0.113.10", "/tmp/aegis-key", "example.com", "admin@example.com"}
	for index, value := range values {
		model.inputs[index].SetValue(value)
	}
	model.advanced[0].SetValue("production")
	model.advanced[1].SetValue("ubuntu")
	model.advanced[2].SetValue("aegisadmin")

	options, err := model.optionsFromInputs()
	if err != nil {
		t.Fatal(err)
	}
	if options.ProfileID != "" || options.IP != "203.0.113.10" || options.Name != "production" {
		t.Fatalf("unexpected new profile options: %+v", options)
	}
	if options.InitialSSHUser != "ubuntu" || options.AdminUser != "aegisadmin" {
		t.Fatalf("unexpected advanced users: %+v", options)
	}
}

func TestProfileSetupModelFreshUsesAdminAsInitialUser(t *testing.T) {
	choice := profileChoice{
		Profile: Profile{
			ID:               "profile-1",
			Name:             "production",
			IP:               "203.0.113.10",
			InitialSSHUser:   "root",
			AdminUser:        "aegisnode",
			PrivateKeyPath:   "/tmp/aegis-key",
			BaseDomain:       "example.com",
			LetsEncryptEmail: "admin@example.com",
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
	if !options.Fresh || options.ProfileID != "profile-1" {
		t.Fatalf("fresh setup should keep source profile id for seeding: %+v", options)
	}
	if options.InitialSSHUser != "aegisnode" || options.AdminUser != "aegisnode" {
		t.Fatalf("fresh setup should use admin user for existing server login: %+v", options)
	}
}

func TestProfileSetupModelDeleteConfirmation(t *testing.T) {
	choice := profileChoice{
		Profile: Profile{
			ID: "profile-1",
			IP: "203.0.113.10",
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
	if result.deleteProfileID != "profile-1" {
		t.Fatalf("delete confirmation did not capture profile id: %+v", result)
	}
}

func TestPrepareProfileSetupLoadsSelectedProfileID(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	first, err := store.Create(Profile{
		ID:               "first-profile",
		Name:             "first",
		IP:               "203.0.113.10",
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
		IP:               "203.0.113.10",
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
		IP:               "203.0.113.10",
		InitialSSHUser:   "root",
		AdminUser:        "aegisnode",
		PrivateKeyPath:   "/tmp/aegis-key",
		BaseDomain:       "example.com",
		LetsEncryptEmail: "admin@example.com",
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
		IP:               "203.0.113.10",
		ProfileID:        source.ID,
		Fresh:            true,
		Name:             "fresh",
		PrivateKeyPath:   "/tmp/aegis-key",
		BaseDomain:       "example.com",
		LetsEncryptEmail: "admin@example.com",
	}, store, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if profile.ID == source.ID {
		t.Fatalf("fresh setup reused source profile id: %+v", profile)
	}
	if config.InitialSSHUser != "aegisnode" || config.AdminUser != "aegisnode" {
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
		IP:               "203.0.113.10",
		InitialSSHUser:   "root",
		AdminUser:        "aegisnode",
		PrivateKeyPath:   "/tmp/aegis-key",
		BaseDomain:       "example.com",
		LetsEncryptEmail: "admin@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, state, config, err := prepareProfileSetup(setupCLIOptions{
		IP:               "203.0.113.10",
		ProfileID:        source.ID,
		Fresh:            true,
		Name:             "fresh",
		PrivateKeyPath:   "/tmp/aegis-key",
		BaseDomain:       "example.com",
		LetsEncryptEmail: "admin@example.com",
	}, store, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if config.InitialSSHUser != "root" || config.AdminUser != "aegisnode" {
		t.Fatalf("fresh setup should keep initial user until bootstrap is complete: %+v", config)
	}
	if completedSetupStages(state)["bootstrap"] {
		t.Fatalf("fresh setup should not seed bootstrap from incomplete source: %+v", state)
	}
}

func TestRunProfileSetupPlanExecutesFullRunAndPersistsState(t *testing.T) {
	originalBootstrap := newBootstrapRemoteClient
	originalHardening := newHardeningRemoteClient
	originalNetwork := newNetworkRemoteClient
	originalProxy := newProxyRemoteClient
	originalObservability := newObservabilityRemoteClient
	defer func() {
		newBootstrapRemoteClient = originalBootstrap
		newHardeningRemoteClient = originalHardening
		newNetworkRemoteClient = originalNetwork
		newProxyRemoteClient = originalProxy
		newObservabilityRemoteClient = originalObservability
	}()

	directory := t.TempDir()
	privateKey := filepath.Join(directory, "id_ed25519")
	publicKey := filepath.Join(directory, "id_ed25519.pub")
	if err := os.WriteFile(privateKey, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publicKey, []byte("ssh-ed25519 AAAATEST user@example\n"), 0600); err != nil {
		t.Fatal(err)
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

	store := newFileProfileStore(t.TempDir())
	profile, state, config, err := prepareProfileSetup(setupCLIOptions{
		IP:               "203.0.113.10",
		PrivateKeyPath:   privateKey,
		BaseDomain:       "example.com",
		LetsEncryptEmail: "admin@example.com",
	}, store, io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := runProfileSetupPlan(context.Background(), store, profile, state, config, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	for index, client := range clients {
		if len(client.commands) == 0 {
			t.Fatalf("stage %d did not run", index)
		}
	}
	_, loadedState, err := store.Load(profile.ID)
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
	if strings.Contains(stdout.String(), config.ServerSecret) {
		t.Fatalf("stdout exposed generated server secret")
	}

	commandCounts := make([]int, len(clients))
	for index, client := range clients {
		commandCounts[index] = len(client.commands)
	}
	profile, state, config, err = prepareProfileSetup(setupCLIOptions{
		IP:               "203.0.113.10",
		PrivateKeyPath:   privateKey,
		BaseDomain:       "example.com",
		LetsEncryptEmail: "admin@example.com",
	}, store, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if err := runProfileSetupPlan(context.Background(), store, profile, state, config, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
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
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("second run output missing %q:\n%s", expected, stdout.String())
		}
	}
}

func TestProfileRunModelRendersTaskProgressAndLogs(t *testing.T) {
	model := newProfileRunModel(
		Profile{Name: "production", IP: "203.0.113.10"},
		setupConfig{
			Host:               "203.0.113.10",
			InitialSSHUser:     "root",
			AdminUser:          "aegisadmin",
			PrivateKeyPath:     "/tmp/aegis-key",
			AdminPublicKeyPath: "/tmp/aegis-key.pub",
			BaseDomain:         "example.com",
			LetsEncryptEmail:   "admin@example.com",
			ServerSecret:       "secret",
		},
		"run-1",
		map[string]bool{"bootstrap": true},
		"",
		make(chan tea.Msg),
		func() {},
	)
	model.applyTaskEvent(TaskEvent{Type: TaskStarted, RunID: "run-1", Stage: "harden", TaskName: "Validate sysctl keys"})
	model.applyTaskEvent(TaskEvent{Type: TaskLogLine, RunID: "run-1", Stage: "harden", Stream: "stdout", Line: "remote output"})
	model.applyTaskEvent(TaskEvent{Type: TaskSucceeded, RunID: "run-1", Stage: "harden", TaskName: "Validate sysctl keys"})

	view := model.View()
	for _, expected := range []string{
		"AegisNode setup run",
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
}

func TestProfileSetupModelSelectsSingleStageRun(t *testing.T) {
	model := newProfileSetupModel([]profileChoice{{
		Profile: Profile{
			ID:             "profile-1",
			Name:           "production",
			IP:             "203.0.113.10",
			AdminUser:      "aegisadmin",
			PrivateKeyPath: "/tmp/aegis-key",
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
	if options.ProfileID != "profile-1" || options.IP != "203.0.113.10" {
		t.Fatalf("unexpected profile options: %+v", options)
	}
}

func TestProfileSetupModelDashboardUsesVForReview(t *testing.T) {
	model := newProfileSetupModel([]profileChoice{{
		Profile: Profile{
			ID:               "profile-1",
			Name:             "production",
			IP:               "203.0.113.10",
			InitialSSHUser:   "root",
			AdminUser:        "aegisadmin",
			PrivateKeyPath:   "/tmp/aegis-key",
			BaseDomain:       "example.com",
			LetsEncryptEmail: "admin@example.com",
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
			ID:             "profile-1",
			Name:           "production",
			IP:             "203.0.113.10",
			PrivateKeyPath: "/tmp/aegis-key",
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
		ID:             "profile-1",
		Name:           "production",
		IP:             "203.0.113.10",
		AdminUser:      "aegisadmin",
		PrivateKeyPath: "/tmp/aegis-key",
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
		if config.Host != "203.0.113.10" || config.SSHUser != "aegisadmin" || config.PrivateKeyPath != privateKey {
			t.Fatalf("unexpected hardening config: %+v", config)
		}
		return client, nil
	}

	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{
		ID:             "profile-1",
		Name:           "production",
		IP:             "203.0.113.10",
		AdminUser:      "aegisadmin",
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
	if err := runProfileSetupStagePlan(context.Background(), store, profile, state, config, "harden", &stdout, &stderr); err != nil {
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
