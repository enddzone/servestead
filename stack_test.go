package main

import (
	"bytes"
	"context"
	"errors"
	"io"
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

const (
	stackTestHost            = "203.0.113.10"
	stackTestDomain          = "example.com"
	stackTestAdminEmail      = "admin@example.com"
	stackTestComposeFilename = "compose.yaml"
	stackTestEnvironment     = "API_KEY=secret\n"
	stackTestHealthPath      = "/health"
	stackTestRenamedSite     = "renamed-site"
	stackTestGitEmail        = "test@example.com"
)

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

func TestRunStackDispatchRejectsInvalidCommands(t *testing.T) {
	if err := runStack(context.Background(), nil, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("missing stack subcommand returned unexpected error: %v", err)
	}
	if err := runStack(context.Background(), []string{"unknown"}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "unknown stack command") {
		t.Fatalf("unknown stack subcommand returned unexpected error: %v", err)
	}
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
			Healthcheck: stackResourceHealthcheck{Enabled: true, Path: stackTestHealthPath},
		}},
	}
	override, err := generateStackPangolinOverride("my-app", metadata, services, Profile{
		BaseDomain: stackTestDomain, PangolinAdminEmail: stackTestAdminEmail,
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
		"pangolin.public-resources.servestead-my-app-web.full-domain=app." + stackTestDomain,
		"pangolin.public-resources.servestead-my-app-web.auth.sso-users[0]=" + stackTestAdminEmail,
		"pangolin.public-resources.servestead-my-app-web.targets[0].hostname=web",
		"pangolin.public-resources.servestead-my-app-web.targets[0].port=80",
		"pangolin.public-resources.servestead-my-app-web.targets[0].healthcheck.path=" + stackTestHealthPath,
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
				Healthcheck: stackResourceHealthcheck{Enabled: true, Path: stackTestHealthPath},
			},
			{
				ID: "api", Service: "api", Name: "API", Subdomain: "api", Port: 3000,
				Protocol: "http", SSO: false,
			},
		},
	}
	override, err := generateStackPangolinOverride("suite", metadata, services, Profile{
		BaseDomain: stackTestDomain, PangolinAdminEmail: stackTestAdminEmail,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(override, "  web:\n") != 1 {
		t.Fatalf("web service should have one merged override entry:\n%s", override)
	}
	for _, expected := range []string{
		"pangolin.public-resources.servestead-suite-site.full-domain=site." + stackTestDomain,
		"pangolin.public-resources.servestead-suite-admin.full-domain=admin." + stackTestDomain,
		"pangolin.public-resources.servestead-suite-api.full-domain=api." + stackTestDomain,
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
		IP: stackTestHost, BaseDomain: stackTestDomain,
		PangolinAdminEmail: stackTestAdminEmail, ConfigRepositoryPath: repository,
	})
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	composePath := filepath.Join(directory, stackTestComposeFilename)
	if err := os.WriteFile(composePath, []byte(testApplicationCompose), 0600); err != nil {
		t.Fatal(err)
	}
	environmentPath := filepath.Join(directory, ".env")
	if err := os.WriteFile(environmentPath, []byte(stackTestEnvironment), 0600); err != nil {
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

func TestStackAddInputsRejectsInvalidInputs(t *testing.T) {
	directory := t.TempDir()
	composePath := filepath.Join(directory, stackTestComposeFilename)
	if err := os.WriteFile(composePath, []byte(testApplicationCompose), 0600); err != nil {
		t.Fatal(err)
	}
	invalidComposePath := filepath.Join(directory, "invalid.yaml")
	if err := os.WriteFile(invalidComposePath, []byte("services: ["), 0600); err != nil {
		t.Fatal(err)
	}
	missingPath := filepath.Join(directory, "missing.yaml")
	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "bad flag", args: []string{"--unknown"}, want: "flag provided but not defined"},
		{name: "required flags", args: nil, want: "--profile and --compose are required"},
		{name: "missing compose", args: []string{"--profile", "profile-1", "--compose", missingPath}, want: "read Compose file"},
		{name: "invalid compose", args: []string{"--profile", "profile-1", "--compose", invalidComposePath}, want: "yaml"},
		{name: "invalid publication", args: []string{"--profile", "profile-1", "--compose", composePath, "--publish", "web:not-a-port:site"}, want: "invalid port"},
		{name: "missing env file", args: []string{"--profile", "profile-1", "--compose", composePath, "--env-file", filepath.Join(directory, "missing.env")}, want: "read environment file"},
		{name: "invalid name", args: []string{"--profile", "profile-1", "--compose", composePath, "--name", "Bad_Name"}, want: "stack name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, _, _, err := stackAddInputs(tc.args, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}
}

func TestLoadStackAddProfileAndRepositoryDefaults(t *testing.T) {
	requireGit(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	store, err := newDefaultProfileStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := loadStackAddProfile(stackAddOptions{ProfileID: "missing"}); err == nil || !strings.Contains(err.Error(), "load profile") {
		t.Fatalf("missing profile returned unexpected error: %v", err)
	}

	profile, err := store.Create(Profile{IP: stackTestHost})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = loadStackAddProfile(stackAddOptions{
		ProfileID: profile.ID,
		Resources: []stackPublicResource{{
			ID: "web", Service: "web", Name: "Web", Subdomain: "web", Port: 80, Protocol: "http",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "base domain") {
		t.Fatalf("public stack without base domain returned unexpected error: %v", err)
	}

	profile.BaseDomain = stackTestDomain
	profile.LetsEncryptEmail = stackTestAdminEmail
	var stdout strings.Builder
	revision, scaffoldCreated, err := prepareStackAddRepository(context.Background(), store, profile, ProfileState{Runs: map[string]SetupRun{}}, &stdout)
	if err != nil {
		t.Fatal(err)
	}
	if revision.Path == "" || scaffoldCreated {
		t.Fatalf("unexpected repository preparation: revision=%+v scaffoldCreated=%v", revision, scaffoldCreated)
	}
	if _, err := os.Stat(filepath.Join(revision.Path, filepath.FromSlash(observabilityComposeRepositoryPath))); err != nil {
		t.Fatalf("default repository scaffold was not prepared: %v", err)
	}
}

func TestWriteStackAddFilesHandlesInPlaceComposeAndExistingFiles(t *testing.T) {
	repository := t.TempDir()
	stackDirectory := filepath.Join(repository, "stacks", "site")
	if err := os.MkdirAll(stackDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	composePath := filepath.Join(stackDirectory, stackComposeFilename)
	if err := os.WriteFile(composePath, []byte(testApplicationCompose), 0600); err != nil {
		t.Fatal(err)
	}

	directory, copied, err := writeStackAddFiles(context.Background(), repository, stackAddOptions{
		Name: "site", Compose: composePath,
	}, stackMetadata{Version: 1}, "", nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if directory != stackDirectory || copied {
		t.Fatalf("unexpected in-place write result: directory=%s copied=%v", directory, copied)
	}
	if _, _, err := writeStackAddFiles(context.Background(), repository, stackAddOptions{
		Name: "site", Compose: composePath,
	}, stackMetadata{Version: 1}, "", nil, io.Discard); err == nil || !strings.Contains(err.Error(), "already configured") {
		t.Fatalf("existing metadata returned unexpected error: %v", err)
	}

	sourcePath := filepath.Join(t.TempDir(), stackComposeFilename)
	if err := os.WriteFile(sourcePath, []byte(testApplicationCompose), 0600); err != nil {
		t.Fatal(err)
	}
	otherDirectory := filepath.Join(repository, "stacks", "other")
	if err := os.MkdirAll(otherDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherDirectory, stackComposeFilename), []byte(testApplicationCompose), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err = writeStackAddFiles(context.Background(), repository, stackAddOptions{
		Name: "other", Compose: sourcePath,
	}, stackMetadata{Version: 1}, "", nil, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "already has") {
		t.Fatalf("existing compose returned unexpected error: %v", err)
	}
}

func TestGeneratedStackOverridePassesDockerComposeMerge(t *testing.T) {
	dockerPath := testDockerPath(t)
	if err := exec.Command(dockerPath, "compose", "version").Run(); err != nil {
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
	}, services, Profile{BaseDomain: stackTestDomain})
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	basePath := filepath.Join(directory, stackTestComposeFilename)
	overridePath := filepath.Join(directory, "override.yaml")
	if err := os.WriteFile(basePath, []byte(base), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overridePath, []byte(override), 0600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(dockerPath, "compose", "-f", basePath, "-f", overridePath, "config").CombinedOutput()
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
	taskSet := stackSecretTasks(t, values)
	assertStackDataTask(t, taskSet.data)
	assertStackSecretTasks(t, taskSet)
}

type stackSecretTaskSet struct {
	data            Task
	validation      Task
	start           Task
	gitStack        Task
	secret          Task
	joinedApply     string
	gitStackIndex   int
	secretTaskIndex int
}

func stackSecretTasks(t *testing.T, values SecretSet) stackSecretTaskSet {
	t.Helper()
	stack := configuredStack{
		Name: "site", Override: "services: {}\n", Secrets: ageStackSecretMetadata("site", values, "age1w7e2rccr8prd58v8u4073zut0e3vm94ukf6gucwge50u2m35hgcq0xzvrd"), SecretValues: values,
	}
	tasks := configuredStackTasks(observabilityConfig{
		RepositoryOrigin: "https://github.com/example/config.git",
		RepositoryBranch: "main",
		RepositoryCommit: "abcdef1234567890",
	}, stack, "servestead")
	taskSet := stackSecretTaskSet{
		gitStackIndex:   -1,
		secretTaskIndex: -1,
	}
	for index, task := range tasks {
		taskSet.joinedApply += task.Apply
		if task.Name == "Prepare site data directory" {
			taskSet.data = task
		}
		if strings.HasPrefix(task.Name, "Validate site Compose") {
			taskSet.validation = task
		}
		if strings.HasPrefix(task.Name, "Start site stack") {
			taskSet.start = task
		}
		if task.Name == "Reconcile site Dockhand Git stack" {
			taskSet.gitStack = task
			taskSet.gitStackIndex = index
		}
		if task.Name == "Reconcile site Dockhand secret environment" {
			taskSet.secret = task
			taskSet.secretTaskIndex = index
		}
	}
	return taskSet
}

func assertStackDataTask(t *testing.T, dataTask Task) {
	t.Helper()
	if !strings.Contains(dataTask.Apply, "install -d -m 0755 -o root -g root '/data'") ||
		!strings.Contains(dataTask.Apply, "install -d -m 0750 -o 1000 -g 1000 '/data/site'") ||
		!strings.Contains(dataTask.Apply, "if ! test -e '/data/site'; then") {
		t.Fatalf("data directory convention was not prepared:\n%s", dataTask.Apply)
	}
	command := exec.Command(testShellPath, "-n")
	command.Stdin = strings.NewReader(dataTask.Apply)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("data directory task is not valid shell: %v\n%s\n%s", err, output, dataTask.Apply)
	}
}

func assertStackSecretTasks(t *testing.T, taskSet stackSecretTaskSet) {
	t.Helper()
	if taskSet.secret.Stdin == "" {
		t.Fatal("Dockhand secret task was not created")
	}
	if taskSet.gitStackIndex < 0 || taskSet.secretTaskIndex < 0 || taskSet.gitStackIndex > taskSet.secretTaskIndex {
		t.Fatalf("Dockhand Git stack should be reconciled before secret environment: git=%d secret=%d", taskSet.gitStackIndex, taskSet.secretTaskIndex)
	}
	if taskSet.gitStack.Apply == "" {
		t.Fatal("Dockhand Git stack task was not created")
	}
	command := exec.Command("sh", "-n")
	command.Stdin = strings.NewReader(taskSet.gitStack.Apply)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Dockhand Git stack task is not valid shell: %v\n%s\n%s", err, output, taskSet.gitStack.Apply)
	}
	if !strings.Contains(taskSet.validation.Stdin, "secret-value") ||
		!strings.Contains(taskSet.start.Stdin, "secret-value") ||
		strings.Contains(taskSet.validation.Apply, "secret-value") ||
		strings.Contains(taskSet.start.Apply, "secret-value") {
		t.Fatal("compose secret values were not isolated to task stdin")
	}
	if !strings.Contains(taskSet.secret.Stdin, "secret-value") ||
		strings.Contains(taskSet.secret.Apply, "secret-value") ||
		strings.Contains(taskSet.joinedApply, "secret-value") {
		t.Fatal("secret value was not isolated to Dockhand task stdin")
	}
	if !strings.Contains(taskSet.secret.Apply, "http://127.0.0.1:3003/api") ||
		!strings.Contains(taskSet.secret.Apply, "--data-binary @-") ||
		strings.Contains(taskSet.secret.Apply, "--data '") ||
		strings.Contains(taskSet.secret.Apply, "/etc/servestead/stacks/site.env") ||
		strings.Contains(taskSet.joinedApply, "--env-file") {
		t.Fatalf("Dockhand secret command does not use the expected stdin/local API path:\n%s", taskSet.secret.Apply)
	}
	command = exec.Command("sh", "-n")
	command.Stdin = strings.NewReader(taskSet.secret.Apply)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Dockhand secret task is not valid shell: %v\n%s\n%s", err, output, taskSet.secret.Apply)
	}
}

func TestPrepareConfigRepositoryLoadsCommittedGenericStacks(t *testing.T) {
	requireGit(t)
	repository := filepath.Join(t.TempDir(), "repository")
	scaffold := observabilityComposeFile(observabilityConfig{
		BaseDomain: stackTestDomain, AdminEmail: stackTestAdminEmail,
	})
	if _, err := prepareConfigRepository(context.Background(), repository, "", "", "profile-1", scaffold); err != nil {
		t.Fatal(err)
	}
	stackDirectory := filepath.Join(repository, "stacks", "site")
	if err := os.MkdirAll(stackDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stackDirectory, stackTestComposeFilename), []byte(testApplicationCompose), 0600); err != nil {
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
	runGitCommand(t, repository, "-c", "user.name=Test", "-c", "user.email="+stackTestGitEmail, "commit", "-m", "Add site")

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
		IP: stackTestHost, BaseDomain: stackTestDomain,
		PangolinAdminEmail: stackTestAdminEmail, ConfigRepositoryPath: repository,
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
	if err := os.WriteFile(filepath.Join(stackDirectory, stackTestComposeFilename), []byte(testApplicationCompose), 0600); err != nil {
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
	runGitCommand(t, repository, "-c", "user.name=Test", "-c", "user.email="+stackTestGitEmail, "commit", "-m", "Add site")
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

func TestStackSecretValuesFromRevisionAllowsLocalRepository(t *testing.T) {
	provider := installRecordingSecretProvider(t)
	identity, recipient, err := generateStackSecretIdentity()
	if err != nil {
		t.Fatal(err)
	}
	values := SecretSet{"API_KEY": "secret"}
	metadata := ageStackSecretMetadata("site", values, recipient)
	provider.values[metadata.Source] = values

	loaded, err := stackSecretValuesFromRevision(context.Background(),
		configRepositoryRevision{Path: t.TempDir()},
		repositoryStack{
			Name: "site",
			Metadata: stackMetadata{
				Version: 1,
				Secrets: metadata,
			},
		},
		ProfileSecrets{StackSecretIdentity: identity},
	)
	if err != nil {
		t.Fatal(err)
	}
	if loaded["API_KEY"] != "secret" {
		t.Fatalf("local stack secret values were not loaded: %+v", loaded.Redacted())
	}
}

func TestStackEnvironmentInputsValidateActions(t *testing.T) {
	if _, _, _, _, err := stackEnvironmentInputs(nil, io.Discard); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("missing env action returned unexpected error: %v", err)
	}
	if !isStackEnvironmentAction("set") || !isStackEnvironmentAction("remove") || isStackEnvironmentAction("show") {
		t.Fatal("stack environment action validation is inconsistent")
	}
	action, profileID, stackName, path, err := stackEnvironmentInputs([]string{"set", "--profile", "profile-1", "--stack", "site", "--file", "app.env"}, io.Discard)
	if err != nil || action != "set" || profileID != "profile-1" || stackName != "site" || path != "app.env" {
		t.Fatalf("unexpected env inputs: action=%q profile=%q stack=%q path=%q err=%v", action, profileID, stackName, path, err)
	}
}

func TestStackEnvironmentTargetValidation(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	repository := t.TempDir()
	profile, err := store.Create(Profile{IP: stackTestHost, ConfigRepositoryPath: repository})
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureStackEnvironmentTarget(store, profile.ID, "site"); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("missing stack target returned unexpected error: %v", err)
	}
	stackDirectory := filepath.Join(repository, "stacks", "site")
	if err := os.MkdirAll(stackDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stackDirectory, stackMetadataFilename), []byte("version: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ensureStackEnvironmentTarget(store, profile.ID, "site"); err != nil {
		t.Fatal(err)
	}
}

func TestStackEnvironmentSaveRemove(t *testing.T) {
	provider := installRecordingSecretProvider(t)
	store := newFileProfileStore(t.TempDir())
	repository := t.TempDir()
	stackDirectory := filepath.Join(repository, "stacks", "site")
	if err := os.MkdirAll(stackDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stackDirectory, stackTestComposeFilename), []byte(testApplicationCompose), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stackDirectory, stackMetadataFilename), []byte("version: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	profile, err := store.Create(Profile{IP: stackTestHost, ConfigRepositoryPath: repository})
	if err != nil {
		t.Fatal(err)
	}
	environmentPath := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(environmentPath, []byte(stackTestEnvironment), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	ctx := context.Background()
	if err := setStackEnvironment(ctx, store, profile.ID, "site", "", &output); err == nil || !strings.Contains(err.Error(), "--file is required") {
		t.Fatalf("missing env file returned unexpected error: %v", err)
	}
	if err := setStackEnvironment(ctx, store, profile.ID, "site", environmentPath, &output); err != nil {
		t.Fatal(err)
	}
	metadata, err := readStackMetadataFile(filepath.Join(stackDirectory, stackMetadataFilename))
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Secrets.Provider != stackSecretProviderAge ||
		strings.Join(metadata.Secrets.KeyNames(), ",") != "API_KEY" ||
		provider.values[defaultStackSecretSource("site")]["API_KEY"] != "secret" ||
		!strings.Contains(output.String(), "API_KEY") {
		t.Fatalf("runtime environment was not saved: metadata=%+v values=%+v output=%s", metadata.Secrets, provider.values, output.String())
	}
	if err := removeStackEnvironment(ctx, store, profile.ID, "site", environmentPath, io.Discard); err == nil || !strings.Contains(err.Error(), "--file cannot") {
		t.Fatalf("remove with file returned unexpected error: %v", err)
	}
	if err := removeStackEnvironment(ctx, store, profile.ID, "site", "", io.Discard); err != nil {
		t.Fatal(err)
	}
	metadata, err = readStackMetadataFile(filepath.Join(stackDirectory, stackMetadataFilename))
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Secrets.HasSecrets() || len(provider.deleted) != 1 || provider.deleted[0] != defaultStackSecretSource("site") {
		t.Fatal("runtime environment was not removed")
	}
}

func TestStackAddInputsReadsEnvironmentAndPublications(t *testing.T) {
	directory := t.TempDir()
	composePath := filepath.Join(directory, stackTestComposeFilename)
	if err := os.WriteFile(composePath, []byte(testApplicationCompose), 0600); err != nil {
		t.Fatal(err)
	}
	environmentPath := filepath.Join(directory, ".env")
	if err := os.WriteFile(environmentPath, []byte(stackTestEnvironment), 0600); err != nil {
		t.Fatal(err)
	}
	options, services, metadata, secretValues, keys, err := stackAddInputs([]string{
		"--profile", "profile-1",
		"--compose", composePath,
		"--name", "site",
		"--publish", "web:80:site:web",
		"--env-file", environmentPath,
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if options.Name != "site" || len(services) != 3 || len(metadata.PublicResources) != 1 ||
		secretValues["API_KEY"] != "secret" || len(keys) != 1 || keys[0] != "API_KEY" {
		t.Fatalf("unexpected stack add inputs: options=%+v services=%+v metadata=%+v secrets=%+v keys=%+v", options, services, metadata, secretValues.Redacted(), keys)
	}
	if _, _, _, _, _, err := stackAddInputs([]string{"--profile", "profile-1", "--compose", composePath, "extra"}, io.Discard); err == nil || !strings.Contains(err.Error(), "unexpected arguments") {
		t.Fatalf("unexpected argument was not rejected: %v", err)
	}
}

func TestValidateStackMetadataResourceRejectsInvalidResources(t *testing.T) {
	services, err := inspectComposeServices([]byte(testApplicationCompose))
	if err != nil {
		t.Fatal(err)
	}
	valid := stackPublicResource{
		ID: "web", Service: "web", Name: "Web", Subdomain: "site", Port: 80, Protocol: "http",
		Healthcheck: stackResourceHealthcheck{Enabled: true, Path: "/"},
	}
	cases := []struct {
		name     string
		resource stackPublicResource
		want     string
	}{
		{name: "bad id", resource: stackPublicResource{ID: "Bad", Service: "web", Name: "Web", Subdomain: "bad", Port: 80, Protocol: "http"}, want: "lowercase DNS label"},
		{name: "bad protocol", resource: stackPublicResource{ID: "bad", Service: "web", Name: "Web", Subdomain: "bad", Port: 80, Protocol: "https"}, want: "protocol"},
		{name: "missing health path", resource: stackPublicResource{ID: "bad", Service: "web", Name: "Web", Subdomain: "bad", Port: 80, Protocol: "http", Healthcheck: stackResourceHealthcheck{Enabled: true}}, want: "health checks"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateStackMetadata("site", stackMetadata{Version: 1, PublicResources: []stackPublicResource{tc.resource}}, services)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}
	err = validateStackMetadata("site", stackMetadata{Version: 1, PublicResources: []stackPublicResource{valid, valid}}, services)
	if err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("duplicate resource was not rejected: %v", err)
	}
	other := valid
	other.ID = "api"
	err = validateStackMetadata("site", stackMetadata{Version: 1, PublicResources: []stackPublicResource{valid, other}}, services)
	if err == nil || !strings.Contains(err.Error(), "subdomain") {
		t.Fatalf("duplicate subdomain was not rejected: %v", err)
	}
}

func TestConfiguredStackTasksExplainValidationAndReconciliation(t *testing.T) {
	stack := configuredStack{
		Name: "site", Compose: testApplicationCompose, Metadata: "version: 1\n",
		ComposeSHA256: "abc", Override: "services:\n  web:\n",
		Resources: []stackPublicResource{{ID: "web", Service: "web", Subdomain: "site", Port: 80}},
	}
	tasks := configuredStackTasks(observabilityConfig{
		RepositoryCommit: "commit", BaseDomain: stackTestDomain,
		AdminEmail: stackTestAdminEmail, PangolinPassword: "password",
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
		BaseDomain: stackTestDomain, AdminEmail: stackTestAdminEmail, PangolinPassword: "password",
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
	shell := exec.Command(testShellPath, "-n")
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
			Healthcheck: stackResourceHealthcheck{Enabled: true, Path: stackTestHealthPath},
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

	options.Name = stackTestRenamedSite
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
	if stacks[0].Name != stackTestRenamedSite || resource.Subdomain != "app" || resource.Name != "Renamed Site" || resource.SSO {
		t.Fatalf("unexpected edited stack: %+v", stacks[0])
	}
	if _, err := os.Stat(filepath.Join(repository, "stacks", stackTestRenamedSite, "app.env.example")); err != nil {
		t.Fatalf("rename did not preserve stack-owned files: %v", err)
	}

	if err := removeEditableStack(repository, stackTestRenamedSite); err != nil {
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
	if err := os.WriteFile(filepath.Join(stacksDirectory, stackTestComposeFilename), []byte(testApplicationCompose), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := loadEditableStacks(repository)
	if err == nil || !strings.Contains(err.Error(), "outside a stack directory") ||
		!strings.Contains(err.Error(), filepath.Join("stacks", "<stack-name>", stackTestComposeFilename)) {
		t.Fatalf("misplaced Compose file did not get actionable guidance: %v", err)
	}
	if err := os.Remove(filepath.Join(stacksDirectory, stackTestComposeFilename)); err != nil {
		t.Fatal(err)
	}
	stacks, err := loadEditableStacks(repository)
	if err != nil {
		t.Fatal(err)
	}
	if len(stacks) != 0 {
		t.Fatalf("empty stack directory should be ignored: %+v", stacks)
	}
	if err := os.WriteFile(filepath.Join(stacksDirectory, "seerr", stackTestComposeFilename), []byte(testApplicationCompose), 0600); err != nil {
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
	runGitCommand(t, repository, "config", "user.email", stackTestGitEmail)
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
	for _, expected := range []string{"Untracked: stacks/site/" + stackTestComposeFilename, "services:", "servestead.yaml"} {
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
		AdminEmail: stackTestAdminEmail, PangolinPassword: "password",
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
	command := exec.Command(testShellPath, "-n")
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
	}, services, Profile{BaseDomain: stackTestDomain})
	if err != nil {
		t.Fatal(err)
	}
	stack := configuredStack{
		Name: "site", Compose: testApplicationCompose, Metadata: "version: 1\n",
		ComposeSHA256: "abc", Override: override,
		Resources: []stackPublicResource{{ID: "web", Service: "web", Subdomain: "site", Port: 80}},
	}
	tasks := stackRepositoryReconcileTasks(observabilityConfig{
		BaseDomain: stackTestDomain, AdminEmail: stackTestAdminEmail,
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
		`"composePath":"stacks/site/` + stackTestComposeFilename + `"`,
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
			command := exec.Command(testShellPath, "-n")
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
