package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	observabilityTestDomain      = "example.com"
	observabilityTestAdminEmail  = "admin@example.com"
	observabilityTestSystemToken = "system-token"
	observabilityTestHost        = "203.0.113.10"
)

func TestObservabilityComposeUsesPinnedReadOnlyServicesAndPangolinLabels(t *testing.T) {
	config := observabilityConfig{
		BaseDomain:    observabilityTestDomain,
		AdminEmail:    observabilityTestAdminEmail,
		AdminPassword: "beszel-password",
		SystemToken:   observabilityTestSystemToken,
	}
	compose := observabilityComposeFile(config)
	for _, expected := range []string{
		"image: docker.io/henrygd/beszel:0.18.7",
		"image: docker.io/henrygd/beszel-agent:0.18.7",
		"image: docker.io/amir20/dozzle:v10.6.6",
		"image: docker.io/fnsys/dockhand:latest",
		"image: docker.io/tecnativa/docker-socket-proxy:v0.4.2",
		"TRUSTED_AUTH_HEADER: \"Remote-Email\"",
		"DOCKER_HOST: \"tcp://socket-proxy:2375\"",
		"DOCKER_HOST: \"tcp://dockhand-socket-proxy:2375\"",
		"HOST_DATA_DIR: \"/opt/servestead/stacks/observability/dockhand_data\"",
		"\"127.0.0.1:3003:3000\"",
		"DOZZLE_AUTH_PROVIDER: \"forward-proxy\"",
		"DOZZLE_ENABLE_ACTIONS: \"false\"",
		"DOZZLE_ENABLE_SHELL: \"false\"",
		"pangolin.public-resources.servestead-beszel.full-domain=beszel." + observabilityTestDomain,
		"pangolin.public-resources.servestead-dozzle.full-domain=dozzle." + observabilityTestDomain,
		"pangolin.public-resources.servestead-dockhand.full-domain=dockhand." + observabilityTestDomain,
		"pangolin.public-resources.servestead-beszel.auth.sso-users[0]=" + observabilityTestAdminEmail,
		"pangolin.public-resources.servestead-beszel.targets[0].hostname=beszel",
		"pangolin.public-resources.servestead-beszel.targets[0].healthcheck.enabled=true",
		"pangolin.public-resources.servestead-beszel.targets[0].healthcheck.hostname=beszel",
		"pangolin.public-resources.servestead-beszel.targets[0].healthcheck.port=8090",
		"pangolin.public-resources.servestead-beszel.targets[0].healthcheck.scheme=http",
		"pangolin.public-resources.servestead-beszel.targets[0].healthcheck.path=/",
		"pangolin.public-resources.servestead-dozzle.targets[0].healthcheck.enabled=true",
		"pangolin.public-resources.servestead-dozzle.targets[0].healthcheck.hostname=dozzle",
		"pangolin.public-resources.servestead-dozzle.targets[0].healthcheck.port=8080",
		"pangolin.public-resources.servestead-dozzle.targets[0].healthcheck.scheme=http",
		"pangolin.public-resources.servestead-dozzle.targets[0].healthcheck.path=/healthcheck",
		"pangolin.public-resources.servestead-dockhand.targets[0].healthcheck.path=/api/auth/session",
		"POST: \"1\"",
		"EXEC: \"1\"",
		"dockhand.hidden=true",
		"internal: true",
		"external: true",
	} {
		if !strings.Contains(compose, expected) {
			t.Fatalf("compose file missing %q:\n%s", expected, compose)
		}
	}
	for _, unexpected := range []string{"8080:8080"} {
		if strings.Contains(compose, unexpected) {
			t.Fatalf("compose file unexpectedly contains %q:\n%s", unexpected, compose)
		}
	}
}

func TestRenderedComposeFilesPassDockerComposeValidation(t *testing.T) {
	dockerPath := testDockerPath(t)
	if err := exec.Command(dockerPath, "compose", "version").Run(); err != nil {
		t.Skip("Docker Compose plugin is not installed")
	}
	files := map[string]string{
		"proxy.yml": pangolinComposeFile(proxyConfig{
			BaseDomain: observabilityTestDomain, SetupToken: "0123456789abcdefghijklmnopqrstuv",
			NewtID: "newtidentifier1", NewtSecret: "newt-secret",
		}),
		"observability.yml": observabilityComposeFile(observabilityConfig{
			BaseDomain: observabilityTestDomain, AdminEmail: observabilityTestAdminEmail,
			AdminPassword: "beszel-password", SystemToken: observabilityTestSystemToken,
		}),
	}
	for name, content := range files {
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command(dockerPath, "compose", "-f", path, "config", "--quiet").CombinedOutput(); err != nil {
			t.Fatalf("%s failed Docker Compose validation: %v\n%s", name, err, output)
		}
	}
}

func TestPinnedObservabilityImagesHavePublishedManifests(t *testing.T) {
	if os.Getenv("SERVESTEAD_VERIFY_IMAGE_MANIFESTS") != "1" {
		t.Skip("set SERVESTEAD_VERIFY_IMAGE_MANIFESTS=1 to query the container registry")
	}
	dockerPath := testDockerPath(t)
	for _, image := range []string{beszelImage, beszelAgentImage, dozzleImage} {
		if output, err := exec.Command(dockerPath, "manifest", "inspect", image).CombinedOutput(); err != nil {
			t.Fatalf("pinned image %s has no published manifest: %v\n%s", image, err, output)
		}
	}
}

func TestBeszelConfigPreconfiguresLocalSystem(t *testing.T) {
	config := beszelConfigFile(observabilityConfig{
		AdminEmail:  observabilityTestAdminEmail,
		SystemToken: observabilityTestSystemToken,
	})
	for _, expected := range []string{
		"name: local-vps",
		"host: beszel-agent",
		"token: '" + observabilityTestSystemToken + "'",
		"- '" + observabilityTestAdminEmail + "'",
	} {
		if !strings.Contains(config, expected) {
			t.Fatalf("Beszel config missing %q:\n%s", expected, config)
		}
	}
}

func TestObservabilityTasksValidateAndVerifyStack(t *testing.T) {
	joined := strings.Join(taskScripts(observabilityTasks(observabilityConfig{
		Host:             observabilityTestHost,
		SSHUser:          "servestead",
		AdminEmail:       observabilityTestAdminEmail,
		PangolinPassword: "pangolin-password",
		BaseDomain:       observabilityTestDomain,
	})), "\n")
	for _, expected := range []string{
		"docker compose -f '/opt/servestead/stacks/observability/docker-compose.yml' config --quiet",
		"/opt/servestead/stacks/observability/beszel_data/id_ed25519",
		"/opt/servestead/stacks/observability/agent_keys/id_ed25519.pub",
		"docker stop aegis-newt",
		"down --remove-orphans",
		`nice_id`,
		`servestead-dockhand`,
		`DELETE "$api/resource/$resource_id"`,
		"docker start aegis-newt",
		`targets[0].get("hcEnabled") is True`,
		`"$api/resource/$resource_id/targets"`,
		"did not converge to exactly one managed Beszel, Dozzle, and Dockhand resource with health checks enabled",
		"for service in beszel beszel-agent dozzle dockhand dockhand-socket-proxy; do",
		`"name":"local-vps"`,
		`"connectionType":"direct"`,
		`"host":"dockhand-socket-proxy"`,
		`"port":2375`,
		`"publicIp":"` + observabilityTestHost + `"`,
		`"$dockhand_api/environments/$dockhand_environment_id/test"`,
		`"$dockhand_api/containers?env=$dockhand_environment_id"`,
		"Dockhand local Docker environment $dockhand_environment_id is connected and lists containers.",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("observability tasks missing %q:\n%s", expected, joined)
		}
	}
	for _, task := range observabilityTasks(observabilityConfig{
		Host:             observabilityTestHost,
		SSHUser:          "servestead",
		AdminEmail:       observabilityTestAdminEmail,
		PangolinPassword: "pangolin-password",
		BaseDomain:       observabilityTestDomain,
	}) {
		if strings.Contains(task.Name, "Dockhand") {
			command := exec.Command(testShellPath, "-n")
			command.Stdin = strings.NewReader(task.Apply)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("%s is not valid shell: %v\n%s\n%s", task.Name, err, output, task.Apply)
			}
		}
	}
}
