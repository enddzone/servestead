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

const testApplicationCompose = `services:
  web:
    image: nginx:alpine
    ports:
      - "127.0.0.1:8080:80"
  api:
    image: example/api
    expose:
      - "3000"
  worker:
    image: example/worker
`

type recordingSecretProvider struct {
	values  map[string]SecretSet
	deleted []string
}

func installRecordingSecretProvider(t *testing.T) *recordingSecretProvider {
	t.Helper()
	provider := &recordingSecretProvider{values: map[string]SecretSet{}}
	original := secretProviderForName
	secretProviderForName = func(name string) (SecretProvider, error) {
		if name != stackSecretProviderAge {
			return nil, errors.New("unexpected provider")
		}
		return provider, nil
	}
	t.Cleanup(func() {
		secretProviderForName = original
	})
	return provider
}

func (provider *recordingSecretProvider) Name() string {
	return stackSecretProviderAge
}

func (provider *recordingSecretProvider) Capabilities() SecretCapabilities {
	return SecretCapabilities{Read: true, Write: true, Delete: true, List: true}
}

func (provider *recordingSecretProvider) GetStackSecrets(_ context.Context, ref StackSecretRef) (SecretSet, error) {
	values, ok := provider.values[ref.Source]
	if !ok {
		return nil, errors.New("missing recorded secrets")
	}
	return copySecretSet(values), nil
}

func (provider *recordingSecretProvider) PutStackSecrets(_ context.Context, ref StackSecretRef, values SecretSet) error {
	provider.values[ref.Source] = copySecretSet(values)
	path, err := stackSecretPath(ref)
	if err != nil {
		return err
	}
	content := "version: 1\nkind: servestead-stack-secrets\nstack: " + ref.StackName + "\nvalues:\n"
	for _, key := range secretSetKeys(values) {
		content += "  " + key + ": ENC[recorded]\n"
	}
	return atomicWriteFile(path, []byte(content), 0600)
}

func (provider *recordingSecretProvider) DeleteStackSecrets(_ context.Context, ref StackSecretRef, _ []string) error {
	provider.deleted = append(provider.deleted, ref.Source)
	delete(provider.values, ref.Source)
	path, err := stackSecretPath(ref)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (provider *recordingSecretProvider) ListStackSecretKeys(_ context.Context, ref StackSecretRef) ([]SecretKeyMeta, error) {
	values, ok := provider.values[ref.Source]
	if !ok {
		return nil, errors.New("missing recorded secrets")
	}
	keys := secretSetKeys(values)
	metas := make([]SecretKeyMeta, 0, len(keys))
	for _, key := range keys {
		metas = append(metas, SecretKeyMeta{Name: key, Required: true})
	}
	return metas, nil
}

func copySecretSet(values SecretSet) SecretSet {
	copied := SecretSet{}
	for key, value := range values {
		copied[key] = value
	}
	return copied
}

func TestInspectComposeServicesFindsContainerPorts(t *testing.T) {
	services, err := inspectComposeServices([]byte(testApplicationCompose))
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 3 {
		t.Fatalf("unexpected services: %+v", services)
	}
	assertServiceSummary(t, services, "web", []int{80}, true)
	assertServiceSummary(t, services, "api", []int{3000}, false)
	assertServiceSummary(t, services, "worker", nil, false)
}

func TestGenerateStackPangolinOverrideOwnsLabelsWithoutRewritingCompose(t *testing.T) {
	services, err := inspectComposeServices([]byte(testApplicationCompose))
	if err != nil {
		t.Fatal(err)
	}
	metadata := stackMetadata{
		Version: 1,
		PublicResources: []stackPublicResource{{
			ID: "web", Service: "web", Name: "My App", Subdomain: "app", Port: 80,
			Protocol: "http", SSO: true,
			Healthcheck: stackResourceHealthcheck{Enabled: true, Path: "/health"},
		}},
	}
	override, err := generateStackPangolinOverride("my-app", metadata, services, Profile{
		BaseDomain: "example.com", PangolinAdminEmail: "admin@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"ports: !reset []",
		"- servestead-public",
		"dockhand.update=false",
		"dockhand.notify=false",
		"pangolin.public-resources.servestead-my-app-web.name=My App",
		"pangolin.public-resources.servestead-my-app-web.full-domain=app.example.com",
		"pangolin.public-resources.servestead-my-app-web.auth.sso-users[0]=admin@example.com",
		"pangolin.public-resources.servestead-my-app-web.targets[0].hostname=web",
		"pangolin.public-resources.servestead-my-app-web.targets[0].port=80",
		"pangolin.public-resources.servestead-my-app-web.targets[0].healthcheck.path=/health",
		"external: true",
	} {
		if !strings.Contains(override, expected) {
			t.Fatalf("override missing %q:\n%s", expected, override)
		}
	}
	if strings.Contains(testApplicationCompose, "pangolin.public-resources") {
		t.Fatal("test Compose unexpectedly contains generated labels")
	}
}

func TestGenerateStackPangolinOverrideGroupsMultipleResourcesByService(t *testing.T) {
	services, err := inspectComposeServices([]byte(testApplicationCompose))
	if err != nil {
		t.Fatal(err)
	}
	metadata := stackMetadata{
		Version: 1,
		PublicResources: []stackPublicResource{
			{
				ID: "site", Service: "web", Name: "Site", Subdomain: "site", Port: 80,
				Protocol: "http", SSO: true,
				Healthcheck: stackResourceHealthcheck{Enabled: true, Path: "/"},
			},
			{
				ID: "admin", Service: "web", Name: "Admin", Subdomain: "admin", Port: 8080,
				Protocol: "http", SSO: true,
				Healthcheck: stackResourceHealthcheck{Enabled: true, Path: "/health"},
			},
			{
				ID: "api", Service: "api", Name: "API", Subdomain: "api", Port: 3000,
				Protocol: "http", SSO: false,
			},
		},
	}
	override, err := generateStackPangolinOverride("suite", metadata, services, Profile{
		BaseDomain: "example.com", PangolinAdminEmail: "admin@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(override, "  web:\n") != 1 {
		t.Fatalf("web service should have one merged override entry:\n%s", override)
	}
	for _, expected := range []string{
		"pangolin.public-resources.servestead-suite-site.full-domain=site.example.com",
		"pangolin.public-resources.servestead-suite-admin.full-domain=admin.example.com",
		"pangolin.public-resources.servestead-suite-api.full-domain=api.example.com",
		"pangolin.public-resources.servestead-suite-admin.targets[0].port=8080",
	} {
		if !strings.Contains(override, expected) {
			t.Fatalf("multi-resource override missing %q:\n%s", expected, override)
		}
	}
}

func TestValidateStackMetadataRejectsUnsupportedPangolinProtocol(t *testing.T) {
	services, err := inspectComposeServices([]byte(testApplicationCompose))
	if err != nil {
		t.Fatal(err)
	}
	err = validateStackMetadata("site", stackMetadata{
		Version: 1,
		PublicResources: []stackPublicResource{{
			ID: "web", Service: "web", Name: "Site", Subdomain: "site", Port: 80,
			Protocol: "https",
		}},
	}, services)
	if err == nil ||
		!strings.Contains(err.Error(), "protocol must be one of http, tcp, udp, ssh, rdp, vnc") ||
		!strings.Contains(err.Error(), "use http for web apps") {
		t.Fatalf("expected actionable protocol validation error, got %v", err)
	}
}

func TestPrivateStackGeneratesDockhandOnlyOverride(t *testing.T) {
	services, err := inspectComposeServices([]byte(testApplicationCompose))
	if err != nil {
		t.Fatal(err)
	}
	override, err := generateStackPangolinOverride(
		"private", stackMetadata{Version: 1}, services, Profile{},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"  web:",
		"  api:",
		"  worker:",
		"dockhand.update=false",
		"dockhand.notify=false",
	} {
		if !strings.Contains(override, expected) {
			t.Fatalf("private stack override missing %q:\n%s", expected, override)
		}
	}
	if strings.Contains(override, "pangolin.public-resources") || strings.Contains(override, servesteadPublicNetwork) {
		t.Fatalf("private stack override should not publish Pangolin resources:\n%s", override)
	}
	tasks := configuredStackTasks(observabilityConfig{}, configuredStack{
		Name: "private", Override: override,
	}, "servestead")
	for _, task := range tasks {
		if strings.Contains(task.Name, "Pangolin") || strings.Contains(task.Apply, "docker stop aegis-newt") {
			t.Fatalf("private stack unnecessarily reconciles Pangolin: %s", task.Name)
		}
	}
}

func TestParseStackPublications(t *testing.T) {
	resources, err := parseStackPublications([]string{"web:80:app", "web:8080:admin:admin"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 2 || resources[0].ID != "web" || resources[1].ID != "admin" ||
		resources[1].Service != "web" || resources[1].Port != 8080 {
		t.Fatalf("unexpected publications: %+v", resources)
	}
	if _, err := parseStackPublications([]string{"web:not-a-port:app"}); err == nil {
		t.Fatal("invalid publication port was accepted")
	}
}

func TestRunStackAddImportsMultiplePublicationsAndEnvironment(t *testing.T) {
	requireGit(t)
	provider := installRecordingSecretProvider(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	store, err := newDefaultProfileStore()
	if err != nil {
		t.Fatal(err)
	}
	repository := filepath.Join(t.TempDir(), "repository")
	if err := os.MkdirAll(repository, 0700); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, repository, "init")
	profile, err := store.Create(Profile{
		IP: "203.0.113.10", BaseDomain: "example.com",
		PangolinAdminEmail: "admin@example.com", ConfigRepositoryPath: repository,
	})
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	composePath := filepath.Join(directory, "compose.yaml")
	if err := os.WriteFile(composePath, []byte(testApplicationCompose), 0600); err != nil {
		t.Fatal(err)
	}
	environmentPath := filepath.Join(directory, ".env")
	if err := os.WriteFile(environmentPath, []byte("API_KEY=secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	err = runStack(context.Background(), []string{
		"add", "--profile", profile.ID, "--compose", composePath, "--name", "suite",
		"--publish", "web:80:app", "--publish", "api:3000:api", "--env-file", environmentPath,
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("stack add failed: %v\n%s", err, stderr.String())
	}
	stacks, err := loadEditableStacks(repository)
	if err != nil {
		t.Fatal(err)
	}
	if len(stacks) != 1 || len(stacks[0].Metadata.PublicResources) != 2 {
		t.Fatalf("multiple publications were not imported: %+v", stacks)
	}
	metadataSecrets := stacks[0].Metadata.Secrets
	if metadataSecrets.Provider != stackSecretProviderAge ||
		metadataSecrets.Source != defaultStackSecretSource("suite") ||
		strings.Join(metadataSecrets.KeyNames(), ",") != "API_KEY" {
		t.Fatalf("stack secret metadata was not imported: %+v", metadataSecrets)
	}
	recorded := provider.values[defaultStackSecretSource("suite")]
	if recorded["API_KEY"] != "secret" {
		t.Fatalf("runtime secret was not imported through provider: %+v", recorded.Redacted())
	}
	if _, err := os.Stat(filepath.Join(repository, filepath.FromSlash(defaultStackSecretSource("suite")))); err != nil {
		t.Fatalf("encrypted secret file was not written: %v", err)
	}
	if strings.Contains(stdout.String(), "API_KEY=secret") {
		t.Fatalf("stack add exposed an environment value:\n%s", stdout.String())
	}
	if fileStore, ok := store.(*fileProfileStore); ok {
		if data, err := os.ReadFile(fileStore.secretsPath(profile.ID)); err == nil &&
			(strings.Contains(string(data), "API_KEY") || strings.Contains(string(data), `"secret"`)) {
			t.Fatalf("profile secrets leaked stack value:\n%s", data)
		}
	}
	if _, err := os.Stat(filepath.Join(repository, filepath.FromSlash(observabilityComposeRepositoryPath))); err != nil {
		t.Fatalf("stack add did not complete the repository scaffold: %v", err)
	}
	if !strings.Contains(stdout.String(), " add stacks\n") || strings.Contains(stdout.String(), "add stacks/suite") {
		t.Fatalf("stack add did not recommend one complete repository commit:\n%s", stdout.String())
	}
}

func TestGeneratedStackOverridePassesDockerComposeMerge(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI is not installed")
	}
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("Docker Compose plugin is not installed")
	}
	base := `services:
  web:
    image: nginx:alpine
    ports:
      - "8080:80"
`
	services, err := inspectComposeServices([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	override, err := generateStackPangolinOverride("site", stackMetadata{
		Version: 1,
		PublicResources: []stackPublicResource{
			{ID: "web", Service: "web", Name: "Site", Subdomain: "site", Port: 80, Protocol: "http"},
			{ID: "admin", Service: "web", Name: "Admin", Subdomain: "admin", Port: 8080, Protocol: "http"},
		},
	}, services, Profile{BaseDomain: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	basePath := filepath.Join(directory, "compose.yaml")
	overridePath := filepath.Join(directory, "override.yaml")
	if err := os.WriteFile(basePath, []byte(base), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overridePath, []byte(override), 0600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("docker", "compose", "-f", basePath, "-f", overridePath, "config").CombinedOutput()
	if err != nil {
		t.Fatalf("Docker Compose merge failed: %v\n%s\nOverride:\n%s", err, output, override)
	}
	rendered := string(output)
	if strings.Contains(rendered, "published: \"8080\"") || strings.Contains(rendered, "8080:80") {
		t.Fatalf("generated override did not remove published port:\n%s", rendered)
	}
}

func TestStackSecretsAreSentToDockhandOverStdin(t *testing.T) {
	values, keys, err := parseEnvironmentSecretSet("# runtime\nAPI_KEY=secret-value\nPORT=3000")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(keys, ",") != "API_KEY,PORT" {
		t.Fatalf("unexpected environment keys: %v", keys)
	}

	stack := configuredStack{
		Name: "site", Override: "services: {}\n", Secrets: ageStackSecretMetadata("site", values, "age1w7e2rccr8prd58v8u4073zut0e3vm94ukf6gucwge50u2m35hgcq0xzvrd"), SecretValues: values,
	}
	tasks := configuredStackTasks(observabilityConfig{
		RepositoryOrigin: "https://github.com/example/config.git",
		RepositoryBranch: "main",
		RepositoryCommit: "abcdef1234567890",
	}, stack, "servestead")
	var dataTask Task
	var validationTask Task
	var startTask Task
	gitStackTaskIndex := -1
	secretTaskIndex := -1
	var gitStackTask Task
	var secretTask Task
	joinedApply := ""
	for index, task := range tasks {
		joinedApply += task.Apply
		if task.Name == "Prepare site data directory" {
			dataTask = task
		}
		if strings.HasPrefix(task.Name, "Validate site Compose") {
			validationTask = task
		}
		if strings.HasPrefix(task.Name, "Start site stack") {
			startTask = task
		}
		if task.Name == "Reconcile site Dockhand Git stack" {
			gitStackTask = task
			gitStackTaskIndex = index
		}
		if task.Name == "Reconcile site Dockhand secret environment" {
			secretTask = task
			secretTaskIndex = index
		}
	}
	if !strings.Contains(dataTask.Apply, "install -d -m 0755 -o root -g root '/data'") ||
		!strings.Contains(dataTask.Apply, "install -d -m 0750 -o 1000 -g 1000 '/data/site'") ||
		!strings.Contains(dataTask.Apply, "if [ ! -e '/data/site' ]; then") {
		t.Fatalf("data directory convention was not prepared:\n%s", dataTask.Apply)
	}
	command := exec.Command("sh", "-n")
	command.Stdin = strings.NewReader(dataTask.Apply)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("data directory task is not valid shell: %v\n%s\n%s", err, output, dataTask.Apply)
	}
	if secretTask.Stdin == "" {
		t.Fatal("Dockhand secret task was not created")
	}
	if gitStackTaskIndex < 0 || secretTaskIndex < 0 || gitStackTaskIndex > secretTaskIndex {
		t.Fatalf("Dockhand Git stack should be reconciled before secret environment: git=%d secret=%d", gitStackTaskIndex, secretTaskIndex)
	}
	if gitStackTask.Apply == "" {
		t.Fatal("Dockhand Git stack task was not created")
	}
	command = exec.Command("sh", "-n")
	command.Stdin = strings.NewReader(gitStackTask.Apply)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Dockhand Git stack task is not valid shell: %v\n%s\n%s", err, output, gitStackTask.Apply)
	}
	if !strings.Contains(validationTask.Stdin, "secret-value") ||
		!strings.Contains(startTask.Stdin, "secret-value") ||
		strings.Contains(validationTask.Apply, "secret-value") ||
		strings.Contains(startTask.Apply, "secret-value") {
		t.Fatal("compose secret values were not isolated to task stdin")
	}
	if !strings.Contains(secretTask.Stdin, "secret-value") ||
		strings.Contains(secretTask.Apply, "secret-value") ||
		strings.Contains(joinedApply, "secret-value") {
		t.Fatal("secret value was not isolated to Dockhand task stdin")
	}
	if !strings.Contains(secretTask.Apply, "http://127.0.0.1:3003/api") ||
		!strings.Contains(secretTask.Apply, "--data-binary @-") ||
		strings.Contains(secretTask.Apply, "--data '") ||
		strings.Contains(secretTask.Apply, "/etc/servestead/stacks/site.env") ||
		strings.Contains(joinedApply, "--env-file") {
		t.Fatalf("Dockhand secret command does not use the expected stdin/local API path:\n%s", secretTask.Apply)
	}
	command = exec.Command("sh", "-n")
	command.Stdin = strings.NewReader(secretTask.Apply)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Dockhand secret task is not valid shell: %v\n%s\n%s", err, output, secretTask.Apply)
	}
}

func TestPrepareConfigRepositoryLoadsCommittedGenericStacks(t *testing.T) {
	requireGit(t)
	repository := filepath.Join(t.TempDir(), "repository")
	scaffold := observabilityComposeFile(observabilityConfig{
		BaseDomain: "example.com", AdminEmail: "admin@example.com",
	})
	if _, err := prepareConfigRepository(context.Background(), repository, "", "", "profile-1", scaffold); err != nil {
		t.Fatal(err)
	}
	stackDirectory := filepath.Join(repository, "stacks", "site")
	if err := os.MkdirAll(stackDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stackDirectory, "compose.yaml"), []byte(testApplicationCompose), 0600); err != nil {
		t.Fatal(err)
	}
	metadata := `version: 1
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
      path: /
`
	if err := os.WriteFile(filepath.Join(stackDirectory, stackMetadataFilename), []byte(metadata), 0600); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, repository, "add", "stacks/site")
	runGitCommand(t, repository, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "Add site")

	revision, err := prepareConfigRepository(context.Background(), repository, "", "", "profile-1", scaffold)
	if err != nil {
		t.Fatal(err)
	}
	if len(revision.Stacks) != 1 || revision.Stacks[0].Name != "site" {
		t.Fatalf("committed stack was not loaded: %+v", revision.Stacks)
	}
}

func TestPrepareDeclarativeSetupAttachesStackSecrets(t *testing.T) {
	requireGit(t)
	provider := installRecordingSecretProvider(t)
	store := newFileProfileStore(t.TempDir())
	repository := filepath.Join(t.TempDir(), "repository")
	profile, err := store.Create(Profile{
		IP: "203.0.113.10", BaseDomain: "example.com",
		PangolinAdminEmail: "admin@example.com", ConfigRepositoryPath: repository,
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, recipient, err := generateStackSecretIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSecrets(profile.ID, ProfileSecrets{StackSecretIdentity: identity, StackSecretRecipient: recipient}); err != nil {
		t.Fatal(err)
	}
	scaffold := observabilityComposeFile(observabilityConfig{
		BaseDomain: profile.BaseDomain, AdminEmail: profile.PangolinAdminEmail,
	})
	if _, err := prepareConfigRepository(context.Background(), repository, "", "", profile.ID, scaffold); err != nil {
		t.Fatal(err)
	}
	stackDirectory := filepath.Join(repository, "stacks", "site")
	if err := os.MkdirAll(stackDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stackDirectory, "compose.yaml"), []byte(testApplicationCompose), 0600); err != nil {
		t.Fatal(err)
	}
	metadata := `version: 1
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
      path: /
secrets:
  provider: age
  source: stacks/site/servestead.secrets.yaml
  recipients:
    - ` + recipient + `
  runtime:
    sink: dockhand
    mode: env
  keys:
    - name: API_KEY
      required: true
`
	if err := os.WriteFile(filepath.Join(stackDirectory, stackMetadataFilename), []byte(metadata), 0600); err != nil {
		t.Fatal(err)
	}
	provider.values[defaultStackSecretSource("site")] = SecretSet{"API_KEY": "secret"}
	if err := atomicWriteFile(filepath.Join(stackDirectory, stackSecretFilename), []byte("encrypted\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, repository, "add", "stacks/site")
	runGitCommand(t, repository, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "Add site")
	runGitCommand(t, repository, "remote", "add", "origin", "https://github.com/example/config.git")
	runGitCommand(t, repository, "update-ref", "refs/remotes/origin/main", "HEAD")
	_, config, err := prepareDeclarativeSetup(
		context.Background(), store, profile, ProfileState{Runs: map[string]SetupRun{}},
		setupConfig{BaseDomain: profile.BaseDomain, PangolinAdminEmail: profile.PangolinAdminEmail},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Stacks) != 1 || config.Stacks[0].SecretValues["API_KEY"] != "secret" {
		t.Fatalf("stack secrets were not attached: %+v", config.Stacks)
	}
}

func TestConfiguredStackTasksExplainValidationAndReconciliation(t *testing.T) {
	stack := configuredStack{
		Name: "site", Compose: testApplicationCompose, Metadata: "version: 1\n",
		ComposeSHA256: "abc", Override: "services:\n  web:\n",
		Resources: []stackPublicResource{{ID: "web", Service: "web", Subdomain: "site", Port: 80}},
	}
	tasks := configuredStackTasks(observabilityConfig{
		RepositoryCommit: "commit", BaseDomain: "example.com",
		AdminEmail: "admin@example.com", PangolinPassword: "password",
	}, stack, "servestead")
	names := make([]string, len(tasks))
	for index, task := range tasks {
		names[index] = task.Name
	}
	joined := strings.Join(names, "\n")
	for _, expected := range []string{
		"Deploy committed site stack",
		"Generate site deployment override",
		"Validate site Compose and Pangolin labels",
		"Start site stack and reconcile Pangolin",
		"Verify site Pangolin public resources",
		"Verify site stack",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("task flow missing %q:\n%s", expected, joined)
		}
	}
}

func TestStackResourceVerifyExplainsRejectedPangolinCredentials(t *testing.T) {
	command := stackResourceVerifyCommand(observabilityConfig{
		BaseDomain: "example.com", AdminEmail: "admin@example.com", PangolinPassword: "password",
	}, configuredStack{
		Name:      "site",
		Resources: []stackPublicResource{{ID: "web", Subdomain: "site"}},
	})
	for _, expected := range []string{
		"Pangolin rejected the saved administrator credentials",
		"PANGOLIN_ADMIN_PASSWORD",
		"-w '%{http_code}'",
	} {
		if !strings.Contains(command, expected) {
			t.Fatalf("verification command missing %q:\n%s", expected, command)
		}
	}
	shell := exec.Command("sh", "-n")
	shell.Stdin = strings.NewReader(command)
	if output, err := shell.CombinedOutput(); err != nil {
		t.Fatalf("verification command is not valid shell: %v\n%s\n%s", err, output, command)
	}
}

func TestConfiguredStacksRejectReservedAndDuplicateSubdomains(t *testing.T) {
	err := validateConfiguredStackSet([]configuredStack{{
		Name: "site", Resources: []stackPublicResource{{ID: "web", Service: "web", Subdomain: "beszel"}},
	}})
	if err == nil || !strings.Contains(err.Error(), "conflicts with observability") {
		t.Fatalf("expected reserved subdomain rejection, got %v", err)
	}
	err = validateConfiguredStackSet([]configuredStack{
		{Name: "one", Resources: []stackPublicResource{{ID: "web", Service: "web", Subdomain: "app"}}},
		{Name: "two", Resources: []stackPublicResource{{ID: "api", Service: "api", Subdomain: "app"}}},
	})
	if err == nil || !strings.Contains(err.Error(), "conflicts with one") {
		t.Fatalf("expected duplicate subdomain rejection, got %v", err)
	}
}

func TestEditableStackLifecycle(t *testing.T) {
	repository := t.TempDir()
	options := stackAddOptions{
		Name: "site",
		Resources: []stackPublicResource{{
			ID: "web", Service: "web", Port: 80, Subdomain: "site", Name: "Site",
			Protocol: "http", SSO: true,
			Healthcheck: stackResourceHealthcheck{Enabled: true, Path: "/health"},
		}},
	}
	if err := writeEditableStack(repository, "", options, []byte(testApplicationCompose)); err != nil {
		t.Fatal(err)
	}
	extraFile := filepath.Join(repository, "stacks", "site", "app.env.example")
	if err := os.WriteFile(extraFile, []byte("KEY=value\n"), 0600); err != nil {
		t.Fatal(err)
	}

	stacks, err := loadEditableStacks(repository)
	if err != nil {
		t.Fatal(err)
	}
	if len(stacks) != 1 || stacks[0].Name != "site" || stacks[0].Metadata.PublicResources[0].Subdomain != "site" {
		t.Fatalf("unexpected added stack: %+v", stacks)
	}

	options.Name = "renamed-site"
	options.Resources[0].Subdomain = "app"
	options.Resources[0].Name = "Renamed Site"
	options.Resources[0].SSO = false
	if err := writeEditableStack(repository, "site", options, []byte(testApplicationCompose)); err != nil {
		t.Fatal(err)
	}
	stacks, err = loadEditableStacks(repository)
	if err != nil {
		t.Fatal(err)
	}
	resource := stacks[0].Metadata.PublicResources[0]
	if stacks[0].Name != "renamed-site" || resource.Subdomain != "app" || resource.Name != "Renamed Site" || resource.SSO {
		t.Fatalf("unexpected edited stack: %+v", stacks[0])
	}
	if _, err := os.Stat(filepath.Join(repository, "stacks", "renamed-site", "app.env.example")); err != nil {
		t.Fatalf("rename did not preserve stack-owned files: %v", err)
	}

	if err := removeEditableStack(repository, "renamed-site"); err != nil {
		t.Fatal(err)
	}
	stacks, err = loadEditableStacks(repository)
	if err != nil {
		t.Fatal(err)
	}
	if len(stacks) != 0 {
		t.Fatalf("removed stack is still listed: %+v", stacks)
	}
}

func TestEditableStackDiscoveryGuidesManualStackLayout(t *testing.T) {
	repository := t.TempDir()
	stacksDirectory := filepath.Join(repository, "stacks")
	if err := os.MkdirAll(filepath.Join(stacksDirectory, "seerr"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stacksDirectory, "compose.yaml"), []byte(testApplicationCompose), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := loadEditableStacks(repository)
	if err == nil || !strings.Contains(err.Error(), "outside a stack directory") ||
		!strings.Contains(err.Error(), filepath.Join("stacks", "<stack-name>", "compose.yaml")) {
		t.Fatalf("misplaced Compose file did not get actionable guidance: %v", err)
	}
	if err := os.Remove(filepath.Join(stacksDirectory, "compose.yaml")); err != nil {
		t.Fatal(err)
	}
	stacks, err := loadEditableStacks(repository)
	if err != nil {
		t.Fatal(err)
	}
	if len(stacks) != 0 {
		t.Fatalf("empty stack directory should be ignored: %+v", stacks)
	}
	if err := os.WriteFile(filepath.Join(stacksDirectory, "seerr", "compose.yaml"), []byte(testApplicationCompose), 0600); err != nil {
		t.Fatal(err)
	}
	stacks, err = loadEditableStacks(repository)
	if err != nil {
		t.Fatal(err)
	}
	if len(stacks) != 1 || stacks[0].Name != "seerr" || !stacks[0].MetadataMissing {
		t.Fatalf("compose-only stack was not loaded as a draft: %+v", stacks)
	}
}

func TestStackRepositoryDiffStageAndCommit(t *testing.T) {
	requireGit(t)
	repository := t.TempDir()
	runGitCommand(t, repository, "init")
	runGitCommand(t, repository, "config", "user.name", "Test")
	runGitCommand(t, repository, "config", "user.email", "test@example.com")
	options := stackAddOptions{
		Name: "site",
		Resources: []stackPublicResource{{
			ID: "web", Service: "web", Port: 80, Subdomain: "site", Name: "Site",
			Protocol: "http", SSO: true,
			Healthcheck: stackResourceHealthcheck{Enabled: true, Path: "/"},
		}},
	}
	if err := writeEditableStack(repository, "", options, []byte(testApplicationCompose)); err != nil {
		t.Fatal(err)
	}

	diff, err := stackRepositoryDiff(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Untracked: stacks/site/compose.yaml", "services:", "servestead.yaml"} {
		if !strings.Contains(diff, expected) {
			t.Fatalf("untracked diff missing %q:\n%s", expected, diff)
		}
	}
	if err := stageStackChanges(context.Background(), repository); err != nil {
		t.Fatal(err)
	}
	diff, err = stackRepositoryDiff(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "Staged changes") || !strings.Contains(diff, "new file mode") {
		t.Fatalf("staged diff is incomplete:\n%s", diff)
	}
	if err := commitStackChanges(context.Background(), repository, "Add site stack"); err != nil {
		t.Fatal(err)
	}
	status, err := stackRepositoryStatus(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if status != "clean" {
		t.Fatalf("repository is not clean after commit: %s", status)
	}
	log, err := runGit(context.Background(), repository, nil, "log", "-1", "--pretty=%s")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(log) != "Add site stack" {
		t.Fatalf("unexpected commit: %q", log)
	}
}

func TestStackRepositorySyncRemovesDeletedDeployments(t *testing.T) {
	task := removedStackCleanupTask(observabilityConfig{
		AdminEmail: "admin@example.com", PangolinPassword: "password",
		Stacks: []configuredStack{{Name: "kept"}},
	})
	for _, expected := range []string{
		".stack-*.deployment",
		"/opt/servestead/generated/*.pangolin.yaml",
		`desired=' kept '`,
		`com.docker.compose.project="$project"`,
		"docker rm -f",
		`rm -f -- /opt/servestead/generated/"$name".pangolin.yaml`,
		`'/etc/servestead/stacks'/"$name".env`,
		`-X DELETE "$api/resource/$resource_id"`,
		"docker start aegis-newt",
	} {
		if !strings.Contains(task.Apply, expected) {
			t.Fatalf("deleted-stack cleanup missing %q:\n%s", expected, task.Apply)
		}
	}
	command := exec.Command("sh", "-n")
	command.Stdin = strings.NewReader(task.Apply)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("deleted-stack cleanup is not valid shell: %v\n%s\n%s", err, output, task.Apply)
	}
}

func TestRunStackRepositorySyncIncludesCleanupAndCurrentStacks(t *testing.T) {
	services, err := inspectComposeServices([]byte(testApplicationCompose))
	if err != nil {
		t.Fatal(err)
	}
	override, err := generateStackPangolinOverride("site", stackMetadata{
		Version: 1,
		PublicResources: []stackPublicResource{{
			ID: "web", Service: "web", Name: "Site", Subdomain: "site", Port: 80, Protocol: "http",
		}},
	}, services, Profile{BaseDomain: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	stack := configuredStack{
		Name: "site", Compose: testApplicationCompose, Metadata: "version: 1\n",
		ComposeSHA256: "abc", Override: override,
		Resources: []stackPublicResource{{ID: "web", Service: "web", Subdomain: "site", Port: 80}},
	}
	tasks := stackRepositoryReconcileTasks(observabilityConfig{
		BaseDomain: "example.com", AdminEmail: "admin@example.com",
		PangolinPassword: "password", RepositoryCommit: "commit", Stacks: []configuredStack{stack},
	}, "servestead")
	if len(tasks) < 2 || tasks[0].Name != "Remove stacks deleted from committed configuration" {
		t.Fatalf("sync does not begin with deleted-stack cleanup: %+v", tasks)
	}
	foundDeploy := false
	for _, task := range tasks {
		if task.Name == "Deploy committed site stack" {
			foundDeploy = true
		}
	}
	if !foundDeploy {
		t.Fatalf("sync did not deploy the current stack: %+v", tasks)
	}
}

func TestDockhandGitStackReconciliationUsesCommittedGitOrigin(t *testing.T) {
	stack := configuredStack{Name: "site", Override: "services: {}\n"}
	config := observabilityConfig{
		RepositoryOrigin: "https://github.com/example/config.git",
		RepositoryBranch: "main",
		RepositoryCommit: "abcdef1234567890",
		Stacks:           []configuredStack{stack},
	}
	tasks := stackRepositoryReconcileTasks(config, "servestead")
	names := strings.Join(taskNames(tasks), "\n")
	joined := strings.Join(taskScripts(tasks), "\n")
	for _, expected := range []string{
		"Remove Dockhand Git stacks deleted from committed configuration",
		"Reconcile site Dockhand Git stack",
	} {
		if !strings.Contains(names, expected) {
			t.Fatalf("Dockhand task flow missing %q:\n%s", expected, names)
		}
	}
	for _, expected := range []string{
		"http://127.0.0.1:3003/api",
		`dockhand_environment_id="$(dockhand_ensure_environment)"`,
		`"$dockhand_api/git/stacks?env=$dockhand_environment_id"`,
		`"$dockhand_api/git/stacks/$stack_id/sync"`,
		`"name":"local-vps"`,
		`"connectionType":"direct"`,
		`"host":"dockhand-socket-proxy"`,
		`"stackName":"servestead-site"`,
		`"repoName":"servestead-site"`,
		`"environmentId":0`,
		`"url":"https://github.com/example/config.git"`,
		`"branch":"main"`,
		`"composePath":"stacks/site/compose.yaml"`,
		`"contextDir":"stacks/site"`,
		`"deployNow":false`,
		`"autoUpdate":false`,
		"abcdef1",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("Dockhand reconciliation missing %q:\n%s", expected, joined)
		}
	}
	for _, task := range tasks {
		if strings.Contains(task.Name, "Dockhand") {
			command := exec.Command("sh", "-n")
			command.Stdin = strings.NewReader(task.Apply)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("%s is not valid shell: %v\n%s\n%s", task.Name, err, output, task.Apply)
			}
		}
	}
}

func assertServiceSummary(t *testing.T, services []composeServiceSummary, name string, ports []int, publishes bool) {
	t.Helper()
	for _, service := range services {
		if service.Name != name {
			continue
		}
		if service.PublishesPorts != publishes || len(service.ContainerPorts) != len(ports) {
			t.Fatalf("unexpected service summary: %+v", service)
		}
		for index := range ports {
			if service.ContainerPorts[index] != ports[index] {
				t.Fatalf("unexpected service ports: %+v", service)
			}
		}
		return
	}
	t.Fatalf("service %s was not found", name)
}
