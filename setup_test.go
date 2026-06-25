package main

import (
	"bytes"
	"errors"
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
	err := runPreflight(config, &output, func(command string) (string, error) {
		return "/usr/bin/" + command, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"[ok] ansible-playbook available",
		"[ok] ssh available",
		"[ok] embedded bootstrap.yml",
		"[ok] embedded hardening.yml",
		"[ok] private key",
		"[ok] admin public key",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("preflight output missing %q:\n%s", expected, output.String())
		}
	}
}

func TestRunPreflightFailsWhenRequiredCommandIsMissing(t *testing.T) {
	var output bytes.Buffer
	err := runPreflight(setupConfig{Mode: setupModeDoctor}, &output, func(command string) (string, error) {
		if command == "ansible-playbook" {
			return "", errors.New("missing")
		}
		return "/usr/bin/" + command, nil
	})
	if err == nil || err.Error() != "preflight checks failed" {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(output.String(), "[fail] ansible-playbook available") {
		t.Fatalf("missing failed command in output:\n%s", output.String())
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
		"$AEGISNODE_TEST_HOME/id_ed25519.pub",
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

func TestSetupPlanSummaryGivesGuidance(t *testing.T) {
	summary := setupPlanSummary(setupConfig{
		Mode:           setupModeBootstrapHarden,
		Host:           "203.0.113.10",
		InitialSSHUser: "root",
		AdminUser:      "aegisadmin",
	})
	if !strings.Contains(summary, "Bootstrap 203.0.113.10") || !strings.Contains(summary, "Apply Phase 2 hardening") {
		t.Fatalf("unexpected summary: %q", summary)
	}
}
