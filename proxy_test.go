package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProxyCommandsDeployPhase4Stack(t *testing.T) {
	config := proxyConfig{
		SSHUser:          "aegisadmin",
		BaseDomain:       "example.com",
		LetsEncryptEmail: "admin@example.com",
		ServerSecret:     "secret password",
	}
	tasks := proxyTasks(config)
	joined := strings.Join(taskScripts(tasks), "\n")
	assertTaskNames(t, taskNames(tasks), []string{
		"Validate Docker Compose is available",
		"Validate Docker bridge firewall support",
		"Prepare proxy stack directories",
		"Write Pangolin application config",
		"Write Traefik static config",
		"Write Traefik dynamic config",
		"Write Pangolin reverse proxy compose file",
		"Allow proxy and Pangolin tunnel ingress",
		"Start Pangolin reverse proxy stack",
		"Verify Pangolin reverse proxy stack",
	})
	for _, expected := range []string{
		"docker compose version >/dev/null",
		"grep -Eq '\"iptables\"[[:space:]]*:[[:space:]]*false' /etc/docker/daemon.json",
		"rerun \"aegisnode network\" before deploying proxy",
		"install -d -m 0750 -o root -g 'aegisadmin' '/opt/aegisnode'",
		"install -d -m 0750 -o root -g 'aegisadmin' '/opt/aegisnode/proxy'",
		"/opt/aegisnode/proxy/config/letsencrypt",
		"/opt/aegisnode/proxy/config/traefik/logs",
		"chown 'root:aegisadmin' '/opt/aegisnode/proxy/config/config.yml.aegisnode.tmp'",
		"chown 'root:aegisadmin' '/opt/aegisnode/proxy/config/traefik/traefik_config.yml.aegisnode.tmp'",
		"chown 'root:aegisadmin' '/opt/aegisnode/proxy/config/traefik/dynamic_config.yml.aegisnode.tmp'",
		"chmod '0640' '/opt/aegisnode/proxy/docker-compose.yml.aegisnode.tmp'",
		"/opt/aegisnode/proxy/docker-compose.yml",
		"# START AegisNode UFW MASQUERADE TRANSLATIONS",
		"-A POSTROUTING -s 172.17.0.0/16 -o ${egress_interface} -j MASQUERADE",
		"-A POSTROUTING -s 172.18.0.0/16 -o ${egress_interface} -j MASQUERADE",
		"-A POSTROUTING -s 172.30.0.0/24 -o ${egress_interface} -j MASQUERADE",
		"ufw route allow from '172.30.0.0/24' to any",
		"ufw allow 80/tcp",
		"ufw allow 443/tcp",
		"ufw allow in on \"$public_interface\" to any port 51820 proto udp comment 'Pangolin Tunnel Entrance'",
		"ufw allow in on \"$public_interface\" to any port 21820 proto udp comment 'Pangolin Session Tunnel Entrance'",
		"docker compose -f '/opt/aegisnode/proxy/docker-compose.yml' pull",
		"docker compose -f '/opt/aegisnode/proxy/docker-compose.yml' down --remove-orphans || true",
		"docker compose -f '/opt/aegisnode/proxy/docker-compose.yml' up -d --remove-orphans",
		"docker compose -f '/opt/aegisnode/proxy/docker-compose.yml' ps --services --status running",
		"for service in pangolin gerbil traefik; do",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("proxy commands missing %q:\n%s", expected, joined)
		}
	}
}

func TestPangolinComposeFileContainsConfiguredServices(t *testing.T) {
	compose := pangolinComposeFile(proxyConfig{
		BaseDomain:       "example.com",
		LetsEncryptEmail: "admin@example.com",
		ServerSecret:     "secret password",
		SetupToken:       "0123456789abcdefghijklmnopqrstuv",
	})
	for _, expected := range []string{
		"image: docker.io/fosrl/pangolin:latest",
		"PANGOLIN_SETUP_TOKEN: \"0123456789abcdefghijklmnopqrstuv\"",
		"image: docker.io/fosrl/gerbil:latest",
		"image: docker.io/traefik:v3.6",
		"./config:/app/config",
		"--reachableAt=http://gerbil:3004",
		"--generateAndSaveKeyTo=/var/config/key",
		"--remoteConfig=http://pangolin:3001/api/v1/",
		"network_mode: service:gerbil",
		"--configFile=/etc/traefik/traefik_config.yml",
		"./config/traefik:/etc/traefik:ro",
		"./config/letsencrypt:/letsencrypt",
		"\"21820:21820/udp\"",
		"\"443:443\"",
		"name: pangolin",
		"ipam:",
		"subnet: 172.30.0.0/24",
	} {
		if !strings.Contains(compose, expected) {
			t.Fatalf("compose file missing %q:\n%s", expected, compose)
		}
	}
	for _, unexpected := range []string{
		"providers.docker",
		"/var/run/docker.sock",
		"postgres:",
		"DB_CONNECTION_STRING",
		"traefik.enable",
	} {
		if strings.Contains(compose, unexpected) {
			t.Fatalf("compose file unexpectedly contains %q:\n%s", unexpected, compose)
		}
	}
}

func TestTraefikConfigFilesContainPangolinRoutes(t *testing.T) {
	staticConfig := traefikStaticConfigFile(proxyConfig{
		LetsEncryptEmail: "admin@example.com",
	})
	for _, expected := range []string{
		"endpoint: \"http://pangolin:3001/api/v1/traefik-config\"",
		"filename: \"/etc/traefik/dynamic_config.yml\"",
		"email: \"admin@example.com\"",
		"storage: \"/letsencrypt/acme.json\"",
		"moduleName: \"github.com/fosrl/badger\"",
	} {
		if !strings.Contains(staticConfig, expected) {
			t.Fatalf("static Traefik config missing %q:\n%s", expected, staticConfig)
		}
	}

	dynamicConfig := traefikDynamicConfigFile(proxyConfig{BaseDomain: "example.com"})
	for _, expected := range []string{
		"rule: \"Host(`pangolin.example.com`)\"",
		"rule: \"Host(`pangolin.example.com`) && !PathPrefix(`/api/v1`)\"",
		"rule: \"Host(`pangolin.example.com`) && PathPrefix(`/api/v1`)\"",
		"url: \"http://pangolin:3002\"",
		"url: \"http://pangolin:3000\"",
		"disableForwardAuth: true",
	} {
		if !strings.Contains(dynamicConfig, expected) {
			t.Fatalf("dynamic Traefik config missing %q:\n%s", expected, dynamicConfig)
		}
	}
}

func TestPangolinConfigFileContainsDashboardSettings(t *testing.T) {
	config := pangolinConfigFile(proxyConfig{
		BaseDomain:   "example.com",
		ServerSecret: "secret",
	})
	for _, expected := range []string{
		"base_endpoint: 'pangolin.example.com'",
		"dashboard_url: 'https://pangolin.example.com'",
		"base_domain: 'example.com'",
		"secret: 'secret'",
		"- 'https://pangolin.example.com'",
		"disable_signup_without_invite: true",
	} {
		if !strings.Contains(config, expected) {
			t.Fatalf("config file missing %q:\n%s", expected, config)
		}
	}
}

func TestRunProxyStepsUsesPrivilegedCommands(t *testing.T) {
	client := &recordingRemoteClient{}
	config := proxyConfig{
		SSHUser:          "aegisadmin",
		BaseDomain:       "example.com",
		LetsEncryptEmail: "admin@example.com",
		ServerSecret:     "secret",
	}
	if err := runProxySteps(context.Background(), client, config, nil); err != nil {
		t.Fatal(err)
	}
	if len(client.commands) != len(proxyTasks(config)) {
		t.Fatalf("unexpected command count: %d", len(client.commands))
	}
	if !strings.HasPrefix(client.commands[0], "sudo sh -c ") {
		t.Fatalf("proxy command did not use sudo: %q", client.commands[0])
	}
}

func TestRunProxyUsesRemoteClientAndPrintsDNSGuidance(t *testing.T) {
	originalFactory := newProxyRemoteClient
	defer func() { newProxyRemoteClient = originalFactory }()

	directory := t.TempDir()
	privateKey := filepath.Join(directory, "id_ed25519")
	if err := os.WriteFile(privateKey, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	client := &recordingRemoteClient{}
	newProxyRemoteClient = func(_ context.Context, config proxyConfig, _, _ io.Writer) (remoteClient, error) {
		return client, nil
	}

	var stdout, stderr bytes.Buffer
	err := runProxy(context.Background(), []string{
		"--host", "203.0.113.10",
		"--private-key", privateKey,
		"--domain", "example.com",
		"--email", "admin@example.com",
		"--server-secret", "secret",
		"--setup-token", "0123456789abcdefghijklmnopqrstuv",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"proxy deployment complete: https://pangolin.example.com",
		"required DNS: A example.com -> 203.0.113.10 and A *.example.com -> 203.0.113.10",
		"Pangolin initial setup: https://pangolin.example.com/auth/initial-setup",
		"Pangolin setup token: 0123456789abcdefghijklmnopqrstuv",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("proxy output missing %q:\n%s", expected, stdout.String())
		}
	}
	if len(client.commands) != len(proxyTasks(proxyConfig{SSHUser: "aegisadmin", BaseDomain: "example.com", LetsEncryptEmail: "admin@example.com", ServerSecret: "secret"})) {
		t.Fatalf("unexpected command count: %d", len(client.commands))
	}
}

func TestValidateProxyConfigRejectsInvalidSetupToken(t *testing.T) {
	err := validateProxyConfig(proxyConfig{
		Host:             "203.0.113.10",
		SSHUser:          "aegisadmin",
		PrivateKeyPath:   "/tmp/key",
		BaseDomain:       "example.com",
		LetsEncryptEmail: "admin@example.com",
		ServerSecret:     "secret",
		SetupToken:       "not-valid",
	})
	if err == nil || err.Error() != "--setup-token must contain exactly 32 lowercase letters or digits" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestProxyRequiresAllDeploymentInputs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"proxy"}, &stdout, &stderr, func(string) string { return "" })
	if err == nil || err.Error() != "--host, --private-key, --domain, --email, and --server-secret are required" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateProxyConfigRejectsInvalidDomain(t *testing.T) {
	err := validateProxyConfig(proxyConfig{
		Host:             "203.0.113.10",
		SSHUser:          "aegisadmin",
		PrivateKeyPath:   "/tmp/key",
		BaseDomain:       "https://example.com",
		LetsEncryptEmail: "admin@example.com",
		ServerSecret:     "secret",
	})
	if err == nil || err.Error() != "--domain must be a valid base domain such as example.com" {
		t.Fatalf("unexpected error: %v", err)
	}
}
