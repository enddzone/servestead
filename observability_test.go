package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestObservabilityComposeUsesPinnedReadOnlyServicesAndPangolinLabels(t *testing.T) {
	config := observabilityConfig{
		BaseDomain:    "example.com",
		AdminEmail:    "admin@example.com",
		AdminPassword: "beszel-password",
		SystemToken:   "system-token",
	}
	compose := observabilityComposeFile(config)
	for _, expected := range []string{
		"image: docker.io/henrygd/beszel:0.18.7",
		"image: docker.io/henrygd/beszel-agent:0.18.7",
		"image: docker.io/amir20/dozzle:v10.6.6",
		"TRUSTED_AUTH_HEADER: \"Remote-Email\"",
		"DOCKER_HOST: \"tcp://socket-proxy:2375\"",
		"DOZZLE_AUTH_PROVIDER: \"forward-proxy\"",
		"DOZZLE_ENABLE_ACTIONS: \"false\"",
		"DOZZLE_ENABLE_SHELL: \"false\"",
		"pangolin.public-resources.aegisnode-beszel.full-domain=beszel.example.com",
		"pangolin.public-resources.aegisnode-dozzle.full-domain=dozzle.example.com",
		"pangolin.public-resources.aegisnode-beszel.auth.sso-users[0]=admin@example.com",
		"pangolin.public-resources.aegisnode-beszel.targets[0].hostname=beszel",
		"external: true",
	} {
		if !strings.Contains(compose, expected) {
			t.Fatalf("compose file missing %q:\n%s", expected, compose)
		}
	}
	for _, unexpected := range []string{"8080:8080", "/var/run/docker.sock"} {
		if strings.Contains(compose, unexpected) {
			t.Fatalf("compose file unexpectedly contains %q:\n%s", unexpected, compose)
		}
	}
}

func TestRenderedComposeFilesPassDockerComposeValidation(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI is not installed")
	}
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("Docker Compose plugin is not installed")
	}
	files := map[string]string{
		"proxy.yml": pangolinComposeFile(proxyConfig{
			BaseDomain: "example.com", SetupToken: "0123456789abcdefghijklmnopqrstuv",
			NewtID: "newtidentifier1", NewtSecret: "newt-secret",
		}),
		"observability.yml": observabilityComposeFile(observabilityConfig{
			BaseDomain: "example.com", AdminEmail: "admin@example.com",
			AdminPassword: "beszel-password", SystemToken: "system-token",
		}),
	}
	for name, content := range files {
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command("docker", "compose", "-f", path, "config", "--quiet").CombinedOutput(); err != nil {
			t.Fatalf("%s failed Docker Compose validation: %v\n%s", name, err, output)
		}
	}
}

func TestPinnedObservabilityImagesHavePublishedManifests(t *testing.T) {
	if os.Getenv("AEGISNODE_VERIFY_IMAGE_MANIFESTS") != "1" {
		t.Skip("set AEGISNODE_VERIFY_IMAGE_MANIFESTS=1 to query the container registry")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI is not installed")
	}
	for _, image := range []string{beszelImage, beszelAgentImage, dozzleImage} {
		if output, err := exec.Command("docker", "manifest", "inspect", image).CombinedOutput(); err != nil {
			t.Fatalf("pinned image %s has no published manifest: %v\n%s", image, err, output)
		}
	}
}

func TestBeszelConfigPreconfiguresLocalSystem(t *testing.T) {
	config := beszelConfigFile(observabilityConfig{
		AdminEmail:  "admin@example.com",
		SystemToken: "system-token",
	})
	for _, expected := range []string{
		"name: local-vps",
		"host: beszel-agent",
		"token: 'system-token'",
		"- 'admin@example.com'",
	} {
		if !strings.Contains(config, expected) {
			t.Fatalf("Beszel config missing %q:\n%s", expected, config)
		}
	}
}

func TestObservabilityTasksValidateAndVerifyStack(t *testing.T) {
	joined := strings.Join(taskScripts(observabilityTasks(observabilityConfig{
		SSHUser:          "aegisadmin",
		AdminEmail:       "admin@example.com",
		PangolinPassword: "pangolin-password",
		BaseDomain:       "example.com",
	})), "\n")
	for _, expected := range []string{
		"docker compose -f '/opt/aegisnode/stacks/observability/docker-compose.yml' config --quiet",
		"/opt/aegisnode/stacks/observability/beszel_data/id_ed25519",
		"/opt/aegisnode/stacks/observability/agent_keys/id_ed25519.pub",
		"docker stop aegis-newt",
		"down --remove-orphans",
		`nice_id`,
		`DELETE "$api/resource/$resource_id"`,
		"docker start aegis-newt",
		"did not converge to exactly one managed Beszel and Dozzle resource",
		"for service in beszel beszel-agent dozzle; do",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("observability tasks missing %q:\n%s", expected, joined)
		}
	}
}
