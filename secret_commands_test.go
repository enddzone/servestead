package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretsCommandsManageProfileIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	store, err := newDefaultProfileStore()
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.Create(Profile{IP: "203.0.113.10"})
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	if err := runSecrets(context.Background(), []string{"init", "--profile", profile.ID}, &stdout, &stderr); err != nil {
		t.Fatalf("secrets init failed: %v\n%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Created stack secret identity.") ||
		!strings.Contains(stdout.String(), "Recipient: age1") {
		t.Fatalf("secrets init did not report the new recipient:\n%s", stdout.String())
	}
	secrets, err := store.LoadSecrets(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	identity := strings.TrimSpace(secrets.StackSecretIdentity)
	if !strings.HasPrefix(identity, "AGE-SECRET-KEY-") || !strings.HasPrefix(secrets.StackSecretRecipient, "age1") {
		t.Fatalf("stack secret identity was not saved: %+v", secrets)
	}

	stdout.Reset()
	stderr.Reset()
	if err := runSecrets(context.Background(), []string{"export-key", "--profile", profile.ID}, &stdout, &stderr); err != nil {
		t.Fatalf("secrets export-key failed: %v\n%s", err, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != identity {
		t.Fatalf("export-key did not print the saved identity:\n%s", stdout.String())
	}

	importedProfile, err := store.Create(Profile{IP: "203.0.113.11"})
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "stack-secret-key.txt")
	if err := os.WriteFile(keyPath, []byte(identity+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if err := runSecrets(context.Background(), []string{"import-key", "--profile", importedProfile.ID, "--file", keyPath}, &stdout, &stderr); err != nil {
		t.Fatalf("secrets import-key failed: %v\n%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Imported stack secret identity.") ||
		!strings.Contains(stdout.String(), secrets.StackSecretRecipient) {
		t.Fatalf("secrets import-key did not report the imported recipient:\n%s", stdout.String())
	}
	importedSecrets, err := store.LoadSecrets(importedProfile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if importedSecrets.StackSecretIdentity != identity || importedSecrets.StackSecretRecipient != secrets.StackSecretRecipient {
		t.Fatalf("import-key did not save the imported identity: %+v", importedSecrets)
	}
}
