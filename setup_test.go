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
		"secret",
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
	if config.BaseDomain != "example.com" || config.LetsEncryptEmail != "admin@example.com" || config.PostgresPassword != "secret" {
		t.Fatalf("unexpected proxy config: %+v", config)
	}
	if model.inputs[5].EchoMode != textinput.EchoPassword {
		t.Fatalf("server secret input is not masked")
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
		PostgresPassword: "secret",
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
		if config.BaseDomain != "example.com" || config.LetsEncryptEmail != "admin@example.com" || config.PostgresPassword != "secret" {
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
		PostgresPassword: "secret",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if len(client.commands) != len(proxyTasks(proxyConfig{SSHUser: "aegisadmin", BaseDomain: "example.com", LetsEncryptEmail: "admin@example.com", PostgresPassword: "secret"})) {
		t.Fatalf("unexpected proxy command count: %d", len(client.commands))
	}
	if !strings.Contains(stdout.String(), "Step 1/1: deploy Pangolin and reverse proxy stack.") {
		t.Fatalf("missing setup step output:\n%s", stdout.String())
	}
}
