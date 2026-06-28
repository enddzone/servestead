package main

import (
	"context"
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
			Service: "web", Name: "My App", Subdomain: "app", Port: 80,
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
		"- aegis-public",
		"pangolin.public-resources.aegisnode-my-app-web.name=My App",
		"pangolin.public-resources.aegisnode-my-app-web.full-domain=app.example.com",
		"pangolin.public-resources.aegisnode-my-app-web.auth.sso-users[0]=admin@example.com",
		"pangolin.public-resources.aegisnode-my-app-web.targets[0].hostname=web",
		"pangolin.public-resources.aegisnode-my-app-web.targets[0].port=80",
		"pangolin.public-resources.aegisnode-my-app-web.targets[0].healthcheck.path=/health",
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
		PublicResources: []stackPublicResource{{
			Service: "web", Name: "Site", Subdomain: "site", Port: 80, Protocol: "http",
		}},
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
  - service: web
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

func TestConfiguredStackTasksExplainValidationAndReconciliation(t *testing.T) {
	stack := configuredStack{
		Name: "site", Compose: testApplicationCompose, Metadata: "version: 1\n",
		ComposeSHA256: "abc", Override: "services:\n  web:\n",
		Resources: []stackPublicResource{{Service: "web", Subdomain: "site", Port: 80}},
	}
	tasks := configuredStackTasks(observabilityConfig{
		RepositoryCommit: "commit", BaseDomain: "example.com",
		AdminEmail: "admin@example.com", PangolinPassword: "password",
	}, stack, "aegisadmin")
	names := make([]string, len(tasks))
	for index, task := range tasks {
		names[index] = task.Name
	}
	joined := strings.Join(names, "\n")
	for _, expected := range []string{
		"Deploy committed site stack",
		"Generate site Pangolin override",
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

func TestConfiguredStacksRejectReservedAndDuplicateSubdomains(t *testing.T) {
	err := validateConfiguredStackSet([]configuredStack{{
		Name: "site", Resources: []stackPublicResource{{Service: "web", Subdomain: "beszel"}},
	}})
	if err == nil || !strings.Contains(err.Error(), "conflicts with observability") {
		t.Fatalf("expected reserved subdomain rejection, got %v", err)
	}
	err = validateConfiguredStackSet([]configuredStack{
		{Name: "one", Resources: []stackPublicResource{{Service: "web", Subdomain: "app"}}},
		{Name: "two", Resources: []stackPublicResource{{Service: "api", Subdomain: "app"}}},
	})
	if err == nil || !strings.Contains(err.Error(), "conflicts with one") {
		t.Fatalf("expected duplicate subdomain rejection, got %v", err)
	}
}

func TestEditableStackLifecycle(t *testing.T) {
	repository := t.TempDir()
	options := stackAddOptions{
		Name: "site", Service: "web", Port: 80, Subdomain: "site",
		DisplayName: "Site", HealthPath: "/health", SSO: true,
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
	options.Subdomain = "app"
	options.DisplayName = "Renamed Site"
	options.SSO = false
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

func TestStackRepositoryDiffStageAndCommit(t *testing.T) {
	requireGit(t)
	repository := t.TempDir()
	runGitCommand(t, repository, "init")
	runGitCommand(t, repository, "config", "user.name", "Test")
	runGitCommand(t, repository, "config", "user.email", "test@example.com")
	options := stackAddOptions{
		Name: "site", Service: "web", Port: 80, Subdomain: "site",
		DisplayName: "Site", HealthPath: "/", SSO: true,
	}
	if err := writeEditableStack(repository, "", options, []byte(testApplicationCompose)); err != nil {
		t.Fatal(err)
	}

	diff, err := stackRepositoryDiff(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Untracked: stacks/site/compose.yaml", "services:", "aegisnode.yaml"} {
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
		"/opt/aegisnode/generated/*.pangolin.yaml",
		`desired=' kept '`,
		`com.docker.compose.project="$project"`,
		"docker rm -f",
		`rm -f -- /opt/aegisnode/generated/"$name".pangolin.yaml`,
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
			Service: "web", Name: "Site", Subdomain: "site", Port: 80, Protocol: "http",
		}},
	}, services, Profile{BaseDomain: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	stack := configuredStack{
		Name: "site", Compose: testApplicationCompose, Metadata: "version: 1\n",
		ComposeSHA256: "abc", Override: override,
		Resources: []stackPublicResource{{Service: "web", Subdomain: "site", Port: 80}},
	}
	tasks := stackRepositoryReconcileTasks(observabilityConfig{
		BaseDomain: "example.com", AdminEmail: "admin@example.com",
		PangolinPassword: "password", RepositoryCommit: "commit", Stacks: []configuredStack{stack},
	}, "aegisadmin")
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
