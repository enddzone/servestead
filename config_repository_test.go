package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultConfigRepositoryPathUsesXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := defaultConfigRepositoryPath("profile-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, filepath.Join("aegisnode", "repositories", "profile-1")) {
		t.Fatalf("unexpected repository path: %s", path)
	}
}

func TestPrepareConfigRepositoryInitializesAndDeploysExactCommit(t *testing.T) {
	requireGit(t)
	repository := filepath.Join(t.TempDir(), "repository")
	scaffold := observabilityComposeFile(observabilityConfig{
		BaseDomain: "example.com",
		AdminEmail: "admin@example.com",
	})
	revision, err := prepareConfigRepository(context.Background(), repository, "", "", "profile-1", scaffold)
	if err != nil {
		t.Fatal(err)
	}
	if revision.Compose != scaffold || revision.Commit == "" || revision.ComposeSHA == "" {
		t.Fatalf("unexpected revision: %+v", revision)
	}
	author := gitOutput(t, repository, "show", "-s", "--format=%an <%ae>")
	if strings.TrimSpace(author) != "AegisNode <aegisnode@localhost>" {
		t.Fatalf("unexpected scaffold author: %s", author)
	}

	if err := os.WriteFile(filepath.Join(repository, "notes.txt"), []byte("unrelated\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareConfigRepository(context.Background(), repository, "", "", "profile-1", scaffold); err != nil {
		t.Fatalf("unrelated working-tree change blocked deployment: %v", err)
	}
	composePath := filepath.Join(repository, filepath.FromSlash(observabilityComposeRepositoryPath))
	if err := os.WriteFile(composePath, []byte(scaffold+"\n# edited\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareConfigRepository(context.Background(), repository, "", "", "profile-1", scaffold); err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("expected dirty Compose rejection, got %v", err)
	}
}

func TestPrepareSuppliedRepositoryScaffoldsThenRequiresReview(t *testing.T) {
	requireGit(t)
	repository := t.TempDir()
	runGitCommand(t, repository, "init", "-b", "main")
	scaffold := observabilityComposeFile(observabilityConfig{BaseDomain: "example.com", AdminEmail: "admin@example.com"})
	_, err := prepareConfigRepository(context.Background(), repository, "", "", "profile-1", scaffold)
	if !errors.Is(err, errRepositoryReviewRequired) {
		t.Fatalf("expected review-required error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(repository, filepath.FromSlash(observabilityComposeRepositoryPath))); statErr != nil {
		t.Fatal(statErr)
	}
}

func TestValidateGitHubRepositoryURL(t *testing.T) {
	for _, valid := range []string{
		"https://github.com/example/aegis-config",
		"https://github.com/example/aegis-config.git",
	} {
		if err := validateGitHubRepositoryURL(valid); err != nil {
			t.Fatalf("%s: %v", valid, err)
		}
	}
	for _, invalid := range []string{
		"http://github.com/example/repo",
		"https://token@github.com/example/repo",
		"https://gitlab.com/example/repo",
		"https://github.com/example/repo?token=secret",
		"git@github.com:example/repo.git",
	} {
		if err := validateGitHubRepositoryURL(invalid); err == nil {
			t.Fatalf("expected %s to be rejected", invalid)
		}
	}
}

func TestObservabilityScaffoldContainsNoGeneratedSecrets(t *testing.T) {
	config := observabilityConfig{
		BaseDomain:    "example.com",
		AdminEmail:    "admin@example.com",
		AdminPassword: "admin-secret",
		SystemToken:   "system-secret",
	}
	compose := observabilityComposeFile(config)
	if strings.Contains(compose, config.AdminPassword) || strings.Contains(compose, config.SystemToken) {
		t.Fatalf("tracked Compose contains a generated secret:\n%s", compose)
	}
	for _, expected := range []string{"${BESZEL_ADMIN_PASSWORD}", "${BESZEL_SYSTEM_TOKEN}"} {
		if !strings.Contains(compose, expected) {
			t.Fatalf("tracked Compose missing %s", expected)
		}
	}
	if err := validateObservabilityCompose([]byte(compose)); err != nil {
		t.Fatal(err)
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

func runGitCommand(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", arguments[0], err, output)
	}
}

func gitOutput(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", arguments[0], err, output)
	}
	return string(output)
}
