package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHubTokenCommandsManageProfileToken(t *testing.T) {
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
	tokenPath := filepath.Join(t.TempDir(), "github-token.txt")
	if err := os.WriteFile(tokenPath, []byte("github_pat_profile_secret\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	if err := run(context.Background(), []string{"github-token", "set", "--profile", profile.ID, "--file", tokenPath}, &stdout, &stderr, func(string) string { return "" }); err != nil {
		t.Fatalf("github-token set failed: %v\n%s", err, stderr.String())
	}
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

	stdout.Reset()
	stderr.Reset()
	if err := run(context.Background(), []string{"github-token", "status", "--profile", profile.ID}, &stdout, &stderr, func(string) string { return "" }); err != nil {
		t.Fatalf("github-token status failed: %v\n%s", err, stderr.String())
	}
	for _, expected := range []string{"Profile token: configured", "Environment token: not configured", "Effective source: profile"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("github-token status missing %q:\n%s", expected, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "github_pat_profile_secret") {
		t.Fatal("github-token status leaked the token")
	}

	t.Setenv("SERVESTEAD_GITHUB_TOKEN", "github_pat_env_secret")
	stdout.Reset()
	stderr.Reset()
	if err := run(context.Background(), []string{"github-token", "status", "--profile", profile.ID}, &stdout, &stderr, func(string) string { return "" }); err != nil {
		t.Fatalf("github-token status with env failed: %v\n%s", err, stderr.String())
	}
	for _, expected := range []string{"Environment token: configured", "Effective source: environment"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("github-token status with env missing %q:\n%s", expected, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "github_pat_env_secret") {
		t.Fatal("github-token status leaked the environment token")
	}

	stdout.Reset()
	stderr.Reset()
	if err := run(context.Background(), []string{"github-token", "remove", "--profile", profile.ID}, &stdout, &stderr, func(string) string { return "" }); err != nil {
		t.Fatalf("github-token remove failed: %v\n%s", err, stderr.String())
	}
	secrets, err = store.LoadSecrets(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if secrets.GitHubToken != "" {
		t.Fatalf("GitHub token was not removed: %+v", secrets)
	}
}

func TestGitHubTokenSetFromEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("SERVESTEAD_GITHUB_TOKEN", "github_pat_env_secret")
	store, err := newDefaultProfileStore()
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.Create(Profile{IP: "203.0.113.10"})
	if err != nil {
		t.Fatal(err)
	}

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
