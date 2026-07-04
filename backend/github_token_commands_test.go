package main

import (
	"context"
	"errors"
	"flag"
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

	runGitHubTokenCLI(t, []string{"github-token", "status", "--profile", profile.ID}, &stdout, &stderr)
	assertGitHubTokenOutput(t, stdout.String(), "Profile token: not configured", "Environment token: configured", "Effective source: environment")
	if strings.Contains(stdout.String(), "github_pat_env_secret") {
		t.Fatal("github-token status leaked the environment token")
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

func TestGitHubTokenCommandValidation(t *testing.T) {
	t.Setenv("SERVESTEAD_GITHUB_TOKEN", "")
	cases := []struct {
		name       string
		args       []string
		wantErr    string
		wantStdout string
		wantStderr string
		wantHelp   bool
	}{
		{name: "missing command", wantErr: "github-token command is required", wantStderr: "servestead github-token set"},
		{name: "unknown command", args: []string{"wat"}, wantErr: "unknown github-token command", wantStderr: "servestead github-token set"},
		{name: "help", args: []string{"help"}, wantStdout: "servestead github-token set", wantHelp: true},
		{name: "set unexpected argument", args: []string{"set", "--profile", "profile-1", "--file", "token.txt", "extra"}, wantErr: "unexpected arguments"},
		{name: "set missing profile", args: []string{"set", "--file", "token.txt"}, wantErr: "--profile is required"},
		{name: "set missing token source", args: []string{"set", "--profile", "profile-1"}, wantErr: "exactly one of --file or --from-env is required"},
		{name: "set conflicting token sources", args: []string{"set", "--profile", "profile-1", "--file", "token.txt", "--from-env"}, wantErr: "exactly one of --file or --from-env is required"},
		{name: "set empty env token", args: []string{"set", "--profile", "profile-1", "--from-env"}, wantErr: "GitHub token is empty"},
		{name: "status unexpected argument", args: []string{"status", "--profile", "profile-1", "extra"}, wantErr: "unexpected arguments"},
		{name: "remove missing profile", args: []string{"remove"}, wantErr: "--profile is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertGitHubTokenValidation(t, tc.args, tc.wantErr, tc.wantStdout, tc.wantStderr, tc.wantHelp)
		})
	}
}

func assertGitHubTokenValidation(t *testing.T, args []string, wantErr, wantStdout, wantStderr string, wantHelp bool) {
	t.Helper()
	var stdout, stderr strings.Builder
	err := runGitHubToken(args, &stdout, &stderr)
	assertGitHubTokenValidationError(t, err, wantErr, wantHelp)
	assertStringContains(t, stdout.String(), wantStdout, "stdout")
	assertStringContains(t, stderr.String(), wantStderr, "stderr")
}

func assertGitHubTokenValidationError(t *testing.T, err error, wantErr string, wantHelp bool) {
	t.Helper()
	if wantHelp {
		if !errors.Is(err, flag.ErrHelp) {
			t.Fatalf("expected help error, got %v", err)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("expected %q error, got %v", wantErr, err)
	}
}

func assertStringContains(t *testing.T, value, expected, stream string) {
	t.Helper()
	if expected != "" && !strings.Contains(value, expected) {
		t.Fatalf("%s missing %q:\n%s", stream, expected, value)
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
