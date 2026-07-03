package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHubTokenCommandsManageProfileToken(t *testing.T) {
	store, profile := newGitHubTokenTestProfile(t)
	tokenPath := filepath.Join(t.TempDir(), "github-token.txt")
	if err := os.WriteFile(tokenPath, []byte("github_pat_profile_secret\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	runGitHubTokenCLI(t, []string{"github-token", "set", "--profile", profile.ID, "--file", tokenPath}, &stdout, &stderr)
	if strings.Contains(stdout.String(), "github_pat_profile_secret") || strings.Contains(stderr.String(), "github_pat_profile_secret") {
		t.Fatal("github-token set leaked the token")
	}
	secrets, err := store.LoadSecrets(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if secrets.GitHubToken != "github_pat_profile_secret" {
		t.Fatalf("GitHub token was not saved: %+v", secrets)
	}

	runGitHubTokenCLI(t, []string{"github-token", "status", "--profile", profile.ID}, &stdout, &stderr)
	assertGitHubTokenOutput(t, stdout.String(), "Profile token: configured", "Environment token: not configured", "Effective source: profile")
	if strings.Contains(stdout.String(), "github_pat_profile_secret") {
		t.Fatal("github-token status leaked the token")
	}

	t.Setenv("SERVESTEAD_GITHUB_TOKEN", "github_pat_env_secret")
	runGitHubTokenCLI(t, []string{"github-token", "status", "--profile", profile.ID}, &stdout, &stderr)
	assertGitHubTokenOutput(t, stdout.String(), "Environment token: configured", "Effective source: environment")
	if strings.Contains(stdout.String(), "github_pat_env_secret") {
		t.Fatal("github-token status leaked the environment token")
	}

	runGitHubTokenCLI(t, []string{"github-token", "remove", "--profile", profile.ID}, &stdout, &stderr)
	secrets, err = store.LoadSecrets(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if secrets.GitHubToken != "" {
		t.Fatalf("GitHub token was not removed: %+v", secrets)
	}
}

func newGitHubTokenTestProfile(t *testing.T) (ProfileStore, Profile) {
	t.Helper()
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
	return store, profile
}

func runGitHubTokenCLI(t *testing.T, args []string, stdout, stderr *strings.Builder) {
	t.Helper()
	stdout.Reset()
	stderr.Reset()
	if err := run(context.Background(), args, stdout, stderr, func(string) string { return "" }); err != nil {
		t.Fatalf("%s failed: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
}

func assertGitHubTokenOutput(t *testing.T, output string, expected ...string) {
	t.Helper()
	if !containsAll(output, expected...) {
		t.Fatalf("github-token output missing expected content %v:\n%s", expected, output)
	}
}

func TestGitHubTokenSetFromEnv(t *testing.T) {
	t.Setenv("SERVESTEAD_GITHUB_TOKEN", "github_pat_env_secret")
	store, profile := newGitHubTokenTestProfile(t)

	var stdout, stderr strings.Builder
	if err := runGitHubToken([]string{"set", "--profile", profile.ID, "--from-env"}, &stdout, &stderr); err != nil {
		t.Fatalf("github-token set --from-env failed: %v\n%s", err, stderr.String())
	}
	secrets, err := store.LoadSecrets(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if secrets.GitHubToken != "github_pat_env_secret" {
		t.Fatalf("GitHub token was not saved from env: %+v", secrets)
	}
}

func TestNormalizeGitHubTokenRejectsWhitespace(t *testing.T) {
	if _, err := normalizeGitHubToken("github_pat_with space"); err == nil {
		t.Fatal("token with whitespace was accepted")
	}
	if token, err := normalizeGitHubToken(" github_pat_secret\n"); err != nil || token != "github_pat_secret" {
		t.Fatalf("trimmed token was rejected: token=%q err=%v", token, err)
	}
}
