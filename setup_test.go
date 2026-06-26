package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if config.BaseDomain != "example.com" || config.LetsEncryptEmail != "admin@example.com" || config.ServerSecret == "" {
		t.Fatalf("unexpected proxy config: %+v", config)
	}
	if strings.Contains(setupPlanSummary(config), config.ServerSecret) {
		t.Fatalf("setup summary exposes generated server secret")
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
		"Deploy Traefik, Pangolin, and Gerbil",
		"Required DNS: A example.com -> 203.0.113.10 and A *.example.com -> 203.0.113.10",
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
	if config.Mode != setupModeFullRun || config.ServerSecret == "" {
		t.Fatalf("unexpected full-run config: %+v", config)
	}
	if strings.Contains(setupPlanSummary(config), config.ServerSecret) {
		t.Fatalf("full-run summary exposes generated server secret")
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
}

func TestRunProfileSetupPlanExecutesFullRunAndPersistsState(t *testing.T) {
	originalBootstrap := newBootstrapRemoteClient
	originalHardening := newHardeningRemoteClient
	originalNetwork := newNetworkRemoteClient
	originalProxy := newProxyRemoteClient
	defer func() {
		newBootstrapRemoteClient = originalBootstrap
		newHardeningRemoteClient = originalHardening
		newNetworkRemoteClient = originalNetwork
		newProxyRemoteClient = originalProxy
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
		{}, {}, {}, {},
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
	for _, stage := range []string{"bootstrap", "harden", "network", "proxy"} {
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
		"Step 1/4: bootstrap administrative access already complete; skipping.",
		"Step 2/4: harden server already complete; skipping.",
		"Step 3/4: configure Docker networking and UFW already complete; skipping.",
		"Step 4/4: deploy Pangolin and reverse proxy stack already complete; skipping.",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("second run output missing %q:\n%s", expected, stdout.String())
		}
	}
}
