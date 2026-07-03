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

	runSecretsCommand(t, []string{"status", "--profile", profile.ID}, &stdout, &stderr)
	assertSecretsCommandOutput(t, stdout.String(), "Recipient: "+secrets.StackSecretRecipient, "Configuration repository: not configured")

	runSecretsCommand(t, []string{"init", "--profile", profile.ID}, &stdout, &stderr)
	assertSecretsCommandOutput(t, stdout.String(), "Stack secret identity already exists.", "Recipient: "+secrets.StackSecretRecipient)

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

func TestSecretsStatusListsRepositoryStacks(t *testing.T) {
	store, profile := newSecretsCommandTestProfile(t, "203.0.113.13")
	identity, recipient, err := generateStackSecretIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSecrets(profile.ID, ProfileSecrets{StackSecretIdentity: identity, StackSecretRecipient: recipient}); err != nil {
		t.Fatal(err)
	}
	repository := t.TempDir()
	profile.ConfigRepositoryPath = repository
	if err := store.Save(profile, ProfileState{Runs: map[string]SetupRun{}}); err != nil {
		t.Fatal(err)
	}
	if err := writeEditableStack(repository, "", stackAddOptions{Name: "plain"}, []byte(testApplicationCompose)); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	runSecretsCommand(t, []string{"status", "--profile", profile.ID}, &stdout, &stderr)
	assertSecretsCommandOutput(t, stdout.String(), "Configuration repository: "+repository, "Stack secrets: none")

	secretValues := SecretSet{"API_KEY": "secret", "TOKEN": "second-secret"}
	if err := writeEditableStack(repository, "", stackAddOptions{
		Name:    "secret",
		Secrets: ageStackSecretMetadata("secret", secretValues, recipient),
	}, []byte(testApplicationCompose)); err != nil {
		t.Fatal(err)
	}
	runSecretsCommand(t, []string{"status", "--profile", profile.ID}, &stdout, &stderr)
	assertSecretsCommandOutput(t, stdout.String(), "Stack secret: API_KEY, TOKEN")
}

func TestSecretsStatusReportsUninitializedProfile(t *testing.T) {
	_, profile := newSecretsCommandTestProfile(t, "203.0.113.12")

	var stdout, stderr strings.Builder
	runSecretsCommand(t, []string{"status", "--profile", profile.ID}, &stdout, &stderr)
	assertSecretsCommandOutput(t, stdout.String(), "Recipient: not initialized", "Configuration repository: not configured")
}

func TestSecretsCommandsRejectMissingAndInvalidIdentity(t *testing.T) {
	_, profile := newSecretsCommandTestProfile(t, "203.0.113.14")

	var stdout, stderr strings.Builder
	if err := runSecrets(context.Background(), []string{"export-key", "--profile", profile.ID}, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "no stack secret identity") {
		t.Fatalf("export-key should reject missing identity, got %v", err)
	}
	keyPath := filepath.Join(t.TempDir(), "stack-secret-key.txt")
	if err := os.WriteFile(keyPath, []byte("not-an-age-identity"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runSecrets(context.Background(), []string{"import-key", "--profile", profile.ID, "--file", keyPath}, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "stack secret identity is invalid") {
		t.Fatalf("import-key should reject invalid identity, got %v", err)
	}
}

func TestSecretsCommandValidation(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "missing-key.txt")
	cases := []struct {
		name       string
		args       []string
		wantErr    string
		wantStderr string
	}{
		{name: "missing command", wantErr: "secrets command is required", wantStderr: "servestead secrets init"},
		{name: "unknown command", args: []string{"wat"}, wantErr: "unknown secrets command", wantStderr: "servestead secrets init"},
		{name: "init unexpected argument", args: []string{"init", "--profile", "profile-1", "extra"}, wantErr: "unexpected arguments"},
		{name: "status missing profile", args: []string{"status"}, wantErr: "--profile is required"},
		{name: "export unexpected argument", args: []string{"export-key", "--profile", "profile-1", "extra"}, wantErr: "unexpected arguments"},
		{name: "import missing file", args: []string{"import-key", "--profile", "profile-1"}, wantErr: "--profile and --file are required"},
		{name: "import unreadable file", args: []string{"import-key", "--profile", "profile-1", "--file", missingPath}, wantErr: "read stack secret identity"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			err := runSecrets(context.Background(), tc.args, &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected %q error, got %v", tc.wantErr, err)
			}
			if tc.wantStderr != "" && !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Fatalf("stderr missing %q:\n%s", tc.wantStderr, stderr.String())
			}
		})
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
