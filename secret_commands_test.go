package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretsCommandsManageProfileIdentity(t *testing.T) {
	store, profile := newSecretsCommandTestProfile(t, "203.0.113.10")

	var stdout, stderr strings.Builder
	runSecretsCommand(t, []string{"init", "--profile", profile.ID}, &stdout, &stderr)
	assertSecretsCommandOutput(t, stdout.String(), "Created stack secret identity.", "Recipient: age1")
	secrets, err := store.LoadSecrets(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	identity := strings.TrimSpace(secrets.StackSecretIdentity)
	if !strings.HasPrefix(identity, "AGE-SECRET-KEY-") || !strings.HasPrefix(secrets.StackSecretRecipient, "age1") {
		t.Fatalf("stack secret identity was not saved: %+v", secrets)
	}

	runSecretsCommand(t, []string{"export-key", "--profile", profile.ID}, &stdout, &stderr)
	if strings.TrimSpace(stdout.String()) != identity {
		t.Fatalf("export-key did not print the saved identity:\n%s", stdout.String())
	}

	_, importedProfile := newSecretsCommandProfile(t, store, "203.0.113.11")
	keyPath := filepath.Join(t.TempDir(), "stack-secret-key.txt")
	if err := os.WriteFile(keyPath, []byte(identity+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runSecretsCommand(t, []string{"import-key", "--profile", importedProfile.ID, "--file", keyPath}, &stdout, &stderr)
	assertSecretsCommandOutput(t, stdout.String(), "Imported stack secret identity.", secrets.StackSecretRecipient)
	importedSecrets, err := store.LoadSecrets(importedProfile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if importedSecrets.StackSecretIdentity != identity || importedSecrets.StackSecretRecipient != secrets.StackSecretRecipient {
		t.Fatalf("import-key did not save the imported identity: %+v", importedSecrets)
	}
}

func newSecretsCommandTestProfile(t *testing.T, ip string) (ProfileStore, Profile) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	store, err := newDefaultProfileStore()
	if err != nil {
		t.Fatal(err)
	}
	return newSecretsCommandProfile(t, store, ip)
}

func newSecretsCommandProfile(t *testing.T, store ProfileStore, ip string) (ProfileStore, Profile) {
	t.Helper()
	profile, err := store.Create(Profile{IP: ip})
	if err != nil {
		t.Fatal(err)
	}
	return store, profile
}

func runSecretsCommand(t *testing.T, args []string, stdout, stderr *strings.Builder) {
	t.Helper()
	stdout.Reset()
	stderr.Reset()
	if err := runSecrets(context.Background(), args, stdout, stderr); err != nil {
		t.Fatalf("secrets %s failed: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
}

func assertSecretsCommandOutput(t *testing.T, output string, expected ...string) {
	t.Helper()
	if !containsAll(output, expected...) {
		t.Fatalf("secrets output missing expected content %v:\n%s", expected, output)
	}
}
