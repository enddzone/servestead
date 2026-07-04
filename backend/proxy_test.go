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

const (
	proxyTestHost            = "203.0.113.10"
	proxyTestDomain          = "example.com"
	proxyTestAdminEmail      = "admin@example.com"
	proxyTestSetupToken      = "0123456789abcdefghijklmnopqrstuv"
	proxyTestUnexpectedError = "unexpected error: %v"
	proxyTestHostRulePrefix  = "rule: \"Host(`pangolin."
)

func TestProxyCommandsDeployPhase4Stack(t *testing.T) {
	config := proxyConfig{
		SSHUser:          "servestead",
		BaseDomain:       proxyTestDomain,
		LetsEncryptEmail: proxyTestAdminEmail,
		ServerSecret:     "secret password",
	}
	tasks := proxyTasks(config)
	joined := strings.Join(taskScripts(tasks), "\n")
	assertTaskNames(t, taskNames(tasks), []string{
		"Validate Docker Compose is available",
		"Validate Docker bridge firewall support",
		"Prepare proxy stack directories",
		"Create shared application network",
		"Write Pangolin application config",
		"Write Traefik static config",
		"Write Traefik dynamic config",
		"Write Pangolin reverse proxy compose file",
		"Allow proxy and Pangolin tunnel ingress",
		"Start Pangolin reverse proxy stack",
		"Bootstrap Pangolin organization and site",
		"Verify Pangolin reverse proxy stack",
	})
	for _, expected := range []string{
		"docker compose version >/dev/null",
		"grep -Eq '\"iptables\"[[:space:]]*:[[:space:]]*false' /etc/docker/daemon.json",
		"rerun \"servestead network\" before deploying proxy",
		"install -d -m 0750 -o root -g 'servestead' '/opt/servestead'",
		"install -d -m 0750 -o root -g 'servestead' '/opt/servestead/proxy'",
		"/opt/servestead/proxy/config/letsencrypt",
		"/opt/servestead/proxy/config/traefik/logs",
		"chown 'root:servestead' '/opt/servestead/proxy/config/config.yml.servestead.tmp'",
		"chown 'root:servestead' '/opt/servestead/proxy/config/traefik/traefik_config.yml.servestead.tmp'",
		"chown 'root:servestead' '/opt/servestead/proxy/config/traefik/dynamic_config.yml.servestead.tmp'",
		"chmod '0640' '/opt/servestead/proxy/docker-compose.yml.servestead.tmp'",
		"/opt/servestead/proxy/docker-compose.yml",
		"# START Servestead UFW MASQUERADE TRANSLATIONS",
		"-A POSTROUTING -s 172.17.0.0/16 -o ${egress_interface} -j MASQUERADE",
		"-A POSTROUTING -s 172.18.0.0/16 -o ${egress_interface} -j MASQUERADE",
		"-A POSTROUTING -s 172.30.0.0/24 -o ${egress_interface} -j MASQUERADE",
		"ufw route allow from '172.30.0.0/24' to any",
		"ufw allow 80/tcp",
		"ufw allow 443/tcp",
		"ufw allow in on \"$public_interface\" to any port 51820 proto udp comment 'Pangolin Tunnel Entrance'",
		"ufw allow in on \"$public_interface\" to any port 21820 proto udp comment 'Pangolin Session Tunnel Entrance'",
		"docker compose -f '/opt/servestead/proxy/docker-compose.yml' pull",
		"docker compose -f '/opt/servestead/proxy/docker-compose.yml' down --remove-orphans || true",
		"docker compose -f '/opt/servestead/proxy/docker-compose.yml' up -d --remove-orphans",
		"docker compose -f '/opt/servestead/proxy/docker-compose.yml' ps --services --status running",
		"for service in pangolin gerbil traefik socket-proxy newt; do",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("proxy commands missing %q:\n%s", expected, joined)
		}
	}
}

func TestPangolinComposeFileContainsConfiguredServices(t *testing.T) {
	compose := pangolinComposeFile(proxyConfig{
		BaseDomain:       proxyTestDomain,
		LetsEncryptEmail: proxyTestAdminEmail,
		ServerSecret:     "secret password",
		SetupToken:       proxyTestSetupToken,
	})
	for _, expected := range []string{
		"image: docker.io/fosrl/pangolin:1.19.4",
		"PANGOLIN_SETUP_TOKEN: \"" + proxyTestSetupToken + "\"",
		"image: docker.io/fosrl/gerbil:1.4.2",
		"image: docker.io/traefik:v3.6.4",
		"image: docker.io/fosrl/newt:1.13.0",
		"image: docker.io/tecnativa/docker-socket-proxy:v0.4.2",
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
		LetsEncryptEmail: proxyTestAdminEmail,
	})
	for _, expected := range []string{
		"endpoint: \"http://pangolin:3001/api/v1/traefik-config\"",
		"filename: \"/etc/traefik/dynamic_config.yml\"",
		"email: \"" + proxyTestAdminEmail + "\"",
		"storage: \"/letsencrypt/acme.json\"",
		"moduleName: \"github.com/fosrl/badger\"",
	} {
		if !strings.Contains(staticConfig, expected) {
			t.Fatalf("static Traefik config missing %q:\n%s", expected, staticConfig)
		}
	}

	dynamicConfig := traefikDynamicConfigFile(proxyConfig{BaseDomain: proxyTestDomain})
	for _, expected := range []string{
		proxyTestHostRulePrefix + proxyTestDomain + "`)\"",
		proxyTestHostRulePrefix + proxyTestDomain + "`) && !PathPrefix(`/api/v1`)\"",
		proxyTestHostRulePrefix + proxyTestDomain + "`) && PathPrefix(`/api/v1`)\"",
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
		BaseDomain:   proxyTestDomain,
		ServerSecret: "secret",
	})
	for _, expected := range []string{
		"base_endpoint: 'pangolin." + proxyTestDomain + "'",
		"dashboard_url: 'https://pangolin." + proxyTestDomain + "'",
		"base_domain: '" + proxyTestDomain + "'",
		"secret: 'secret'",
		"- 'https://pangolin." + proxyTestDomain + "'",
		"disable_signup_without_invite: true",
	} {
		if !strings.Contains(config, expected) {
			t.Fatalf("config file missing %q:\n%s", expected, config)
		}
	}
}

func TestPangolinBootstrapUsesCSRFProtectedIdempotentAPI(t *testing.T) {
	script := pangolinBootstrapCommand(proxyConfig{
		AdminEmail:    proxyTestAdminEmail,
		AdminPassword: "Aa1!password",
		SetupToken:    proxyTestSetupToken,
		NewtID:        "newtidentifier1",
		NewtSecret:    "newt-secret",
	})
	for _, expected := range []string{
		"/auth/initial-setup-complete",
		"/auth/set-server-admin",
		"X-CSRF-Token: x-csrf-protection",
		"/auth/login",
		`"orgId":"servestead"`,
		`"niceId":"local-vps"`,
		`"newtId":"newtidentifier1"`,
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("bootstrap script missing %q:\n%s", expected, script)
		}
	}
}

func TestRunProxyStepsUsesPrivilegedCommands(t *testing.T) {
	client := &recordingRemoteClient{}
	config := proxyConfig{
		SSHUser:          "servestead",
		BaseDomain:       proxyTestDomain,
		LetsEncryptEmail: proxyTestAdminEmail,
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
		"--host", proxyTestHost,
		"--private-key", privateKey,
		"--domain", proxyTestDomain,
		"--email", proxyTestAdminEmail,
		"--server-secret", "secret",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"proxy deployment complete: https://pangolin." + proxyTestDomain,
		"Required DNS: A pangolin." + proxyTestDomain + " -> " + proxyTestHost +
			", A beszel." + proxyTestDomain + " -> " + proxyTestHost +
			", A dozzle." + proxyTestDomain + " -> " + proxyTestHost +
			", A dockhand." + proxyTestDomain + " -> " + proxyTestHost,
		"Pangolin administrator: " + proxyTestAdminEmail,
		"Pangolin password:",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("proxy output missing %q:\n%s", expected, stdout.String())
		}
	}
	if len(client.commands) != len(proxyTasks(proxyConfig{
		SSHUser: "servestead", BaseDomain: proxyTestDomain, LetsEncryptEmail: proxyTestAdminEmail, ServerSecret: "secret",
	})) {
		t.Fatalf("unexpected command count: %d", len(client.commands))
	}
}

func TestValidateProxyConfigRejectsInvalidSetupToken(t *testing.T) {
	err := validateProxyConfig(proxyConfig{
		Host:             proxyTestHost,
		SSHUser:          "servestead",
		PrivateKeyPath:   "/tmp/key",
		BaseDomain:       proxyTestDomain,
		LetsEncryptEmail: proxyTestAdminEmail,
		ServerSecret:     "secret",
		SetupToken:       "not-valid",
	})
	if err == nil || err.Error() != "Pangolin setup token must contain exactly 32 lowercase letters or digits" {
		t.Fatalf(proxyTestUnexpectedError, err)
	}
}

func TestProxyRequiresAllDeploymentInputs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"proxy"}, &stdout, &stderr, func(string) string { return "" })
	if err == nil || err.Error() != "--host, --private-key, --domain, --email, and --server-secret are required" {
		t.Fatalf(proxyTestUnexpectedError, err)
	}
}

func TestValidateProxyConfigRejectsInvalidDomain(t *testing.T) {
	err := validateProxyConfig(proxyConfig{
		Host:             proxyTestHost,
		SSHUser:          "servestead",
		PrivateKeyPath:   "/tmp/key",
		BaseDomain:       "https://" + proxyTestDomain,
		LetsEncryptEmail: proxyTestAdminEmail,
		ServerSecret:     "secret",
	})
	if err == nil || err.Error() != "--domain must be a valid base domain such as example.com" {
		t.Fatalf(proxyTestUnexpectedError, err)
	}
}
