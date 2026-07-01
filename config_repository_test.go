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

const (
	configRepositoryTestProfileID  = "profile-1"
	configRepositoryTestDomain     = "example.com"
	configRepositoryTestAdminEmail = "admin@example.com"
	configRepositoryTestGitEmail   = "test@example.com"
	configRepositoryTestNoGit      = "git is not installed"
)

func TestDefaultConfigRepositoryPathUsesXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := defaultConfigRepositoryPath(configRepositoryTestProfileID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, filepath.Join("servestead", "repositories", configRepositoryTestProfileID)) {
		t.Fatalf("unexpected repository path: %s", path)
	}
}

func TestPrepareConfigRepositoryInitializesAndDeploysExactCommit(t *testing.T) {
	requireGit(t)
	repository := filepath.Join(t.TempDir(), "repository")
	scaffold := observabilityComposeFile(observabilityConfig{
		BaseDomain: configRepositoryTestDomain,
		AdminEmail: configRepositoryTestAdminEmail,
	})
	revision, err := prepareConfigRepository(context.Background(), repository, "", "", configRepositoryTestProfileID, scaffold)
	if err != nil {
		t.Fatal(err)
	}
	if revision.Compose != scaffold || revision.Commit == "" || revision.ComposeSHA == "" {
		t.Fatalf("unexpected revision: %+v", revision)
	}
	author := gitOutput(t, repository, "show", "-s", "--format=%an <%ae>")
	if strings.TrimSpace(author) != "Servestead <servestead@localhost>" {
		t.Fatalf("unexpected scaffold author: %s", author)
	}

	if err := os.WriteFile(filepath.Join(repository, "notes.txt"), []byte("unrelated\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareConfigRepository(context.Background(), repository, "", "", configRepositoryTestProfileID, scaffold); err != nil {
		t.Fatalf("unrelated working-tree change blocked deployment: %v", err)
	}
	composePath := filepath.Join(repository, filepath.FromSlash(observabilityComposeRepositoryPath))
	if err := os.WriteFile(composePath, []byte(scaffold+"\n# edited\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareConfigRepository(context.Background(), repository, "", "", configRepositoryTestProfileID, scaffold); err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("expected dirty Compose rejection, got %v", err)
	}
}

func TestPrepareSuppliedRepositoryScaffoldsThenRequiresReview(t *testing.T) {
	requireGit(t)
	repository := t.TempDir()
	runGitCommand(t, repository, "init", "-b", "main")
	scaffold := observabilityComposeFile(observabilityConfig{BaseDomain: configRepositoryTestDomain, AdminEmail: configRepositoryTestAdminEmail})
	_, err := prepareConfigRepository(context.Background(), repository, "", "", configRepositoryTestProfileID, scaffold)
	if !errors.Is(err, errRepositoryReviewRequired) {
		t.Fatalf("expected review-required error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(repository, filepath.FromSlash(observabilityComposeRepositoryPath))); statErr != nil {
		t.Fatal(statErr)
	}
}

func TestPrepareConfigRepositoryRefreshesManagedObservabilityScaffoldForReview(t *testing.T) {
	requireGit(t)
	repository := t.TempDir()
	runGitCommand(t, repository, "init", "-b", "main")
	composePath := filepath.Join(repository, filepath.FromSlash(observabilityComposeRepositoryPath))
	if err := os.MkdirAll(filepath.Dir(composePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(composePath, []byte(legacyManagedObservabilityCompose()), 0600); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, repository, "add", observabilityComposeRepositoryPath)
	runGitCommand(t, repository, "-c", "user.name=Test", "-c", "user.email="+configRepositoryTestGitEmail, "commit", "-m", "Legacy observability")

	scaffold := observabilityComposeFile(observabilityConfig{BaseDomain: configRepositoryTestDomain, AdminEmail: configRepositoryTestAdminEmail})
	_, err := prepareConfigRepository(context.Background(), repository, "", "", configRepositoryTestProfileID, scaffold)
	if !errors.Is(err, errRepositoryReviewRequired) {
		t.Fatalf("expected review-required error, got %v", err)
	}
	data, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != scaffold {
		t.Fatal("managed observability scaffold was not refreshed")
	}
	status := gitOutput(t, repository, "status", "--short", "--", observabilityComposeRepositoryPath)
	if !strings.Contains(status, observabilityComposeRepositoryPath) {
		t.Fatalf("refreshed scaffold was not left for review: %q", status)
	}
}

func TestResolveConfigRepositoryBranchPrefersCurrentOriginBranch(t *testing.T) {
	requireGit(t)
	repository := t.TempDir()
	runGitCommand(t, repository, "init", "-b", "main")

	branch, err := resolveConfigRepositoryBranch(context.Background(), repository, "  origin/feature\n  origin/main\n")
	if err != nil {
		t.Fatal(err)
	}
	if branch != "main" {
		t.Fatalf("expected main, got %q", branch)
	}
}

func TestResolveConfigRepositoryBranchRejectsAmbiguousDetachedCheckout(t *testing.T) {
	requireGit(t)
	repository := t.TempDir()
	runGitCommand(t, repository, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, repository, "add", "README.md")
	runGitCommand(t, repository, "-c", "user.name=Test", "-c", "user.email="+configRepositoryTestGitEmail, "commit", "-m", "Initial")
	runGitCommand(t, repository, "checkout", "--detach", "HEAD")

	_, err := resolveConfigRepositoryBranch(context.Background(), repository, "  origin/feature\n  origin/main\n")
	if err == nil || !strings.Contains(err.Error(), "detached") {
		t.Fatalf("expected detached checkout error, got %v", err)
	}
}

func TestEnsureConfigRepositoryScaffoldIsIdempotent(t *testing.T) {
	requireGit(t)
	repository := t.TempDir()
	runGitCommand(t, repository, "init", "-b", "main")
	scaffold := observabilityComposeFile(observabilityConfig{BaseDomain: configRepositoryTestDomain, AdminEmail: configRepositoryTestAdminEmail})
	created, err := ensureConfigRepositoryScaffold(context.Background(), repository, scaffold)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("missing repository scaffold was not created")
	}
	created, err = ensureConfigRepositoryScaffold(context.Background(), repository, scaffold)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("existing repository scaffold was recreated")
	}
	data, err := os.ReadFile(filepath.Join(repository, filepath.FromSlash(observabilityComposeRepositoryPath)))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != scaffold {
		t.Fatal("repository scaffold content changed")
	}
}

func legacyManagedObservabilityCompose() string {
	return `services:
  beszel:
    image: docker.io/henrygd/beszel:0.18.7
    networks:
      - servestead-public
    labels:
      - pangolin.public-resources.servestead-beszel.name=Beszel

  beszel-agent:
    image: docker.io/henrygd/beszel-agent:0.18.7
    networks:
      - servestead-public

  dozzle:
    image: docker.io/amir20/dozzle:v10.6.6
    networks:
      - servestead-public
    labels:
      - pangolin.public-resources.servestead-dozzle.name=Dozzle

networks:
  servestead-public:
    external: true
`
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
		BaseDomain:    configRepositoryTestDomain,
		AdminEmail:    configRepositoryTestAdminEmail,
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

func TestPrepareConfigRepositoryCheckoutRejectsInvalidInputs(t *testing.T) {
	if _, err := prepareConfigRepositoryCheckout(context.Background(), filepath.Join(t.TempDir(), "repository"), "git@github.com:example/repo.git", ""); err == nil {
		t.Fatal("invalid GitHub repository URL was accepted")
	}

	repository := t.TempDir()
	if _, err := prepareConfigRepositoryCheckout(context.Background(), repository, "", ""); err == nil || !strings.Contains(err.Error(), "not a Git repository") {
		t.Fatalf("existing non-Git repository returned unexpected error: %v", err)
	}
}

func TestResolveConfigRepositoryRemoteUsesPushedOriginBranch(t *testing.T) {
	requireGit(t)
	repository := t.TempDir()
	runGitCommand(t, repository, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, repository, "add", "README.md")
	runGitCommand(t, repository, "-c", "user.name=Test", "-c", "user.email="+configRepositoryTestGitEmail, "commit", "-m", "Initial")
	runGitCommand(t, repository, "remote", "add", "origin", "https://github.com/enddzone/servestead.git")
	runGitCommand(t, repository, "update-ref", "refs/remotes/origin/main", "HEAD")

	commit := strings.TrimSpace(gitOutput(t, repository, "rev-parse", "HEAD"))
	origin, branch, err := resolveConfigRepositoryRemote(context.Background(), repository, "https://github.com/enddzone/servestead.git", commit)
	if err != nil {
		t.Fatal(err)
	}
	if origin != "https://github.com/enddzone/servestead.git" || branch != "main" {
		t.Fatalf("unexpected remote resolution: origin=%q branch=%q", origin, branch)
	}

	_, _, err = resolveConfigRepositoryRemote(context.Background(), repository, "https://github.com/enddzone/other.git", commit)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched GitHub repository returned unexpected error: %v", err)
	}

	runGitCommand(t, repository, "update-ref", "-d", "refs/remotes/origin/main")
	_, _, err = resolveConfigRepositoryRemote(context.Background(), repository, "", commit)
	if err == nil || !strings.Contains(err.Error(), "not been pushed") {
		t.Fatalf("unpushed commit returned unexpected error: %v", err)
	}
}

func TestResolveConfigRepositoryRemoteRejectsInvalidOrigin(t *testing.T) {
	requireGit(t)
	repository := t.TempDir()
	runGitCommand(t, repository, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, repository, "add", "README.md")
	runGitCommand(t, repository, "-c", "user.name=Test", "-c", "user.email="+configRepositoryTestGitEmail, "commit", "-m", "Initial")
	runGitCommand(t, repository, "remote", "add", "origin", "https://gitlab.com/enddzone/servestead.git")

	commit := strings.TrimSpace(gitOutput(t, repository, "rev-parse", "HEAD"))
	_, _, err := resolveConfigRepositoryRemote(context.Background(), repository, "", commit)
	if err == nil || !strings.Contains(err.Error(), "origin") {
		t.Fatalf("invalid origin returned unexpected error: %v", err)
	}
}

func TestLoadCommittedStacksIncludesFilesAndSkipsObservability(t *testing.T) {
	requireGit(t)
	repository := t.TempDir()
	runGitCommand(t, repository, "init", "-b", "main")

	files := map[string]string{
		"stacks/observability/compose.yaml": "services:\n  placeholder:\n    image: example/placeholder\n",
		"stacks/site/compose.yaml":          testApplicationCompose,
		"stacks/site/config/app.conf":       "enabled=true\n",
		"stacks/site/servestead.yaml": `version: 1
public_resources:
  - id: web
    service: web
    name: Site
    subdomain: site
    port: 80
    protocol: http
    sso: true
    healthcheck:
      enabled: true
      path: /health
`,
	}
	for name, content := range files {
		path := filepath.Join(repository, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runGitCommand(t, repository, "add", "stacks")
	runGitCommand(t, repository, "-c", "user.name=Test", "-c", "user.email="+configRepositoryTestGitEmail, "commit", "-m", "Add stacks")

	stacks, err := loadCommittedStacks(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if len(stacks) != 1 {
		t.Fatalf("expected one application stack, got %+v", stacks)
	}
	stack := stacks[0]
	if stack.Name != "site" || stack.Compose != testApplicationCompose || stack.Metadata.Version != 1 || stack.ComposeSHA256 == "" {
		t.Fatalf("unexpected stack: %+v", stack)
	}
	if stack.Files["compose.yaml"] != testApplicationCompose || stack.Files["config/app.conf"] != "enabled=true\n" {
		t.Fatalf("committed stack files were not loaded: %+v", stack.Files)
	}
	if !strings.Contains(stack.MetadataContent, "subdomain: site") {
		t.Fatalf("metadata content was not loaded: %q", stack.MetadataContent)
	}
}

func TestLoadCommittedStackRejectsInvalidNameAndMissingCompose(t *testing.T) {
	repository := newConfigRepositoryWithInitialCommit(t)
	if _, _, err := loadCommittedStack(context.Background(), repository, "Bad_Name"); err == nil || !strings.Contains(err.Error(), "lowercase DNS") {
		t.Fatalf("invalid stack name returned unexpected error: %v", err)
	}
	if _, _, err := loadCommittedStack(context.Background(), repository, "site"); err == nil || !strings.Contains(err.Error(), "committed compose.yaml") {
		t.Fatalf("missing stack compose returned unexpected error: %v", err)
	}
}

func TestLoadCommittedStackRejectsMissingMetadata(t *testing.T) {
	repository := newConfigRepositoryWithInitialCommit(t)
	commitConfigRepositoryFile(t, repository, "stacks/site/compose.yaml", testApplicationCompose, "Add stack compose")
	if _, _, err := loadCommittedStack(context.Background(), repository, "site"); err == nil || !strings.Contains(err.Error(), "is not configured") {
		t.Fatalf("missing stack metadata returned unexpected error: %v", err)
	}
}

func TestLoadCommittedStackRejectsInvalidMetadataAndCompose(t *testing.T) {
	repository := newConfigRepositoryWithInitialCommit(t)
	commitConfigRepositoryFile(t, repository, "stacks/site/compose.yaml", testApplicationCompose, "Add stack compose")
	commitConfigRepositoryFile(t, repository, "stacks/site/servestead.yaml", "version: [\n", "Add invalid metadata")
	if _, _, err := loadCommittedStack(context.Background(), repository, "site"); err == nil || !strings.Contains(err.Error(), "metadata") {
		t.Fatalf("invalid stack metadata returned unexpected error: %v", err)
	}

	commitConfigRepositoryFile(t, repository, "stacks/site/compose.yaml", "services: [\n", "Add invalid compose")
	commitConfigRepositoryFile(t, repository, "stacks/site/servestead.yaml", "version: 1\n", "Add valid metadata")
	if _, _, err := loadCommittedStack(context.Background(), repository, "site"); err == nil || !strings.Contains(err.Error(), "stack site") {
		t.Fatalf("invalid stack compose returned unexpected error: %v", err)
	}
}

func TestLoadCommittedStackRejectsInvalidResource(t *testing.T) {
	repository := newConfigRepositoryWithInitialCommit(t)
	invalidResource := `version: 1
public_resources:
  - id: web
    service: missing
    name: Web
    subdomain: web
    port: 80
    protocol: http
`
	commitConfigRepositoryFile(t, repository, "stacks/site/compose.yaml", testApplicationCompose, "Add stack compose")
	commitConfigRepositoryFile(t, repository, "stacks/site/servestead.yaml", invalidResource, "Add invalid resource")
	if _, _, err := loadCommittedStack(context.Background(), repository, "site"); err == nil || !strings.Contains(err.Error(), "metadata") {
		t.Fatalf("invalid stack resource returned unexpected error: %v", err)
	}
}

func newConfigRepositoryWithInitialCommit(t *testing.T) string {
	t.Helper()
	requireGit(t)
	repository := t.TempDir()
	runGitCommand(t, repository, "init", "-b", "main")
	commitConfigRepositoryFile(t, repository, "README.md", "test\n", "Initial")
	return repository
}

func commitConfigRepositoryFile(t *testing.T, repository, name, content, message string) {
	t.Helper()
	path := filepath.Join(repository, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, repository, "add", name)
	runGitCommand(t, repository, "-c", "user.name=Test", "-c", "user.email="+configRepositoryTestGitEmail, "commit", "-m", message)
}

func TestValidateObservabilityComposeRejectsMissingServices(t *testing.T) {
	err := validateObservabilityCompose([]byte("services: {}\n"))
	if err == nil || !strings.Contains(err.Error(), "required service") {
		t.Fatalf("missing services returned unexpected error: %v", err)
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := trustedGitExecutable(); err != nil {
		t.Skip(configRepositoryTestNoGit)
	}
}

func runGitCommand(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	gitPath, err := trustedGitExecutable()
	if err != nil {
		t.Skip(configRepositoryTestNoGit)
	}
	command := exec.Command(gitPath, append([]string{"-C", directory}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", arguments[0], err, output)
	}
}

func gitOutput(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	gitPath, err := trustedGitExecutable()
	if err != nil {
		t.Skip(configRepositoryTestNoGit)
	}
	command := exec.Command(gitPath, append([]string{"-C", directory}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", arguments[0], err, output)
	}
	return string(output)
}
