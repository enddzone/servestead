package main

import (
	"bytes"
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
