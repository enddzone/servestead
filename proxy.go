package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"servestead/resources"
	"strings"
)

const proxyStackDirectory = "/opt/servestead/proxy"
const proxyDockerSubnet = "172.30.0.0/24"
const servesteadPublicNetwork = "servestead-public"

const (
	pangolinImage    = "docker.io/fosrl/pangolin:1.19.4"
	gerbilImage      = "docker.io/fosrl/gerbil:1.4.2"
	traefikImage     = "docker.io/traefik:v3.6.4"
	newtImage        = "docker.io/fosrl/newt:1.13.0"
	socketProxyImage = "docker.io/tecnativa/docker-socket-proxy:v0.4.2"
)

var domainName = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+$`)
var pangolinSetupToken = regexp.MustCompile(`^[a-z0-9]{32}$`)

type proxyConfig struct {
	Host             string
	SSHUser          string
	PrivateKeyPath   string
	BaseDomain       string
	LetsEncryptEmail string
	ServerSecret     string
	SetupToken       string
	AdminEmail       string
	AdminPassword    string
	NewtID           string
	NewtSecret       string
}

type proxyRemoteClientFactory func(context.Context, proxyConfig, io.Writer, io.Writer) (remoteClient, error)

var newProxyRemoteClient proxyRemoteClientFactory = func(ctx context.Context, config proxyConfig, stdout, stderr io.Writer) (remoteClient, error) {
	return newSSHRemoteClient(ctx, config.Host, config.SSHUser, config.PrivateKeyPath, stdout, stderr)
}

func runProxy(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("proxy", flag.ContinueOnError)
	flags.SetOutput(stderr)
	config := proxyConfig{}
	flags.StringVar(&config.Host, "host", "", "target VPS IPv4 address or hostname")
	flags.StringVar(&config.SSHUser, "ssh-user", "servestead", "administrative SSH user")
	flags.StringVar(&config.PrivateKeyPath, "private-key", "", "path to the administrative private key")
	flags.StringVar(&config.BaseDomain, "domain", "", "base domain for Pangolin, for example example.com")
	flags.StringVar(&config.LetsEncryptEmail, "email", "", "Let's Encrypt account email")
	flags.StringVar(&config.ServerSecret, "server-secret", "", "Pangolin server secret")
	flags.StringVar(&config.ServerSecret, "postgres-password", "", "deprecated alias for --server-secret")
	flags.StringVar(&config.AdminEmail, "pangolin-admin-email", "", "Pangolin administrator email (defaults to --email)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if config.SetupToken == "" {
		generated, err := GeneratePangolinSetupToken()
		if err != nil {
			return fmt.Errorf("generate Pangolin setup token: %w", err)
		}
		config.SetupToken = generated
	}
	config.AdminEmail = firstNonEmpty(config.AdminEmail, config.LetsEncryptEmail)
	generatedPassword, err := generatePassword(32)
	if err != nil {
		return fmt.Errorf("generate Pangolin administrator password: %w", err)
	}
	config.AdminPassword = generatedPassword
	for destination, size := range map[*string]int{
		&config.NewtID:     15,
		&config.NewtSecret: 48,
	} {
		generated, err := generateLowercaseSecret(size)
		if err != nil {
			return fmt.Errorf("generate Pangolin credentials: %w", err)
		}
		*destination = generated
	}
	if err := validateProxyConfig(config); err != nil {
		return err
	}
	if _, err := os.Stat(config.PrivateKeyPath); err != nil {
		return fmt.Errorf("access private key: %w", err)
	}

	client, err := newProxyRemoteClient(ctx, config, stdout, stderr)
	if err != nil {
		return err
	}
	defer client.Close()

	fmt.Fprintf(stdout, "deploying Pangolin reverse proxy stack on %s as %s...\n", config.Host, config.SSHUser)
	if err := runProxySteps(ctx, client, config, stdout); err != nil {
		return fmt.Errorf("proxy deployment failed: %w", err)
	}
	fmt.Fprintf(stdout, "proxy deployment complete: https://pangolin.%s\n", config.BaseDomain)
	fmt.Fprintln(stdout, requiredDNSGuidance(config.BaseDomain, config.Host))
	fmt.Fprintf(stdout, "Pangolin administrator: %s\n", config.AdminEmail)
	fmt.Fprintf(stdout, "Pangolin password: %s\n", config.AdminPassword)
	return nil
}

func validateProxyConfig(config proxyConfig) error {
	config.AdminEmail = firstNonEmpty(config.AdminEmail, config.LetsEncryptEmail)
	if config.Host == "" || config.PrivateKeyPath == "" || config.BaseDomain == "" || config.LetsEncryptEmail == "" || config.ServerSecret == "" {
		return errors.New("--host, --private-key, --domain, --email, and --server-secret are required")
	}
	if !linuxUsername.MatchString(config.SSHUser) {
		return errors.New("--ssh-user must be a valid Linux username")
	}
	if !domainName.MatchString(config.BaseDomain) {
		return errors.New("--domain must be a valid base domain such as example.com")
	}
	if strings.ContainsAny(config.LetsEncryptEmail, " \t\r\n") || !strings.Contains(config.LetsEncryptEmail, "@") {
		return errors.New("--email must be a valid email address")
	}
	if strings.ContainsAny(config.ServerSecret, "\r\n") {
		return errors.New("--server-secret must not contain newlines")
	}
	if !pangolinSetupToken.MatchString(config.SetupToken) {
		return errors.New("Pangolin setup token must contain exactly 32 lowercase letters or digits")
	}
	if strings.ContainsAny(config.AdminEmail, " \t\r\n") || !strings.Contains(config.AdminEmail, "@") {
		return errors.New("--pangolin-admin-email must be a valid email address")
	}
	return nil
}

func runProxySteps(ctx context.Context, client remoteClient, config proxyConfig, progress io.Writer) error {
	return runTasks(ctx, client, config.SSHUser, proxyTasks(config), progress)
}

func runProxyStepsWithReporter(ctx context.Context, client remoteClient, config proxyConfig, runID string, reporter TaskReporter, progress io.Writer) error {
	return runTasksWithReporter(ctx, client, config.SSHUser, runID, "proxy", proxyTasks(config), progress, reporter)
}

func proxyTasks(config proxyConfig) []Task {
	composePath := proxyStackDirectory + "/docker-compose.yml"
	stackGroup := firstNonEmpty(config.SSHUser, "root")
	return []Task{
		{Name: "Validate Docker Compose is available", Apply: commandScript(
			"docker info >/dev/null",
			"docker compose version >/dev/null",
		)},
		{Name: "Validate Docker bridge firewall support", Apply: commandScript(
			`if [ -f /etc/docker/daemon.json ] && grep -Eq '"iptables"[[:space:]]*:[[:space:]]*false' /etc/docker/daemon.json; then`,
			`  echo 'Docker bridge firewall/NAT is disabled; rerun "servestead network" before deploying proxy.' >&2`,
			`  exit 1`,
			`fi`,
		)},
		{Name: "Prepare proxy stack directories", Apply: commandScript(
			"install -d -m 0750 -o root -g "+shellQuote(stackGroup)+" "+shellQuote("/opt/servestead"),
			"install -d -m 0750 -o root -g "+shellQuote(stackGroup)+" "+shellQuote(proxyStackDirectory),
			"install -d -m 0750 -o root -g "+shellQuote(stackGroup)+" "+shellQuote(proxyStackDirectory+"/config"),
			"install -d -m 0750 -o root -g "+shellQuote(stackGroup)+" "+shellQuote(proxyStackDirectory+"/config/db"),
			"install -d -m 0750 -o root -g "+shellQuote(stackGroup)+" "+shellQuote(proxyStackDirectory+"/config/letsencrypt"),
			"install -d -m 0750 -o root -g "+shellQuote(stackGroup)+" "+shellQuote(proxyStackDirectory+"/config/traefik"),
			"install -d -m 0750 -o root -g "+shellQuote(stackGroup)+" "+shellQuote(proxyStackDirectory+"/config/traefik/logs"),
		)},
		{Name: "Create shared application network", Apply: commandScript(
			"docker network inspect " + shellQuote(servesteadPublicNetwork) + " >/dev/null 2>&1 || docker network create " + shellQuote(servesteadPublicNetwork),
		)},
		{Name: "Write Pangolin application config", Apply: remoteWriteFileCommand(proxyStackDirectory+"/config/config.yml", pangolinConfigFile(config), "root", stackGroup, 0640)},
		{Name: "Write Traefik static config", Apply: remoteWriteFileCommand(proxyStackDirectory+"/config/traefik/traefik_config.yml", traefikStaticConfigFile(config), "root", stackGroup, 0640)},
		{Name: "Write Traefik dynamic config", Apply: remoteWriteFileCommand(proxyStackDirectory+"/config/traefik/dynamic_config.yml", traefikDynamicConfigFile(config), "root", stackGroup, 0640)},
		{Name: "Write Pangolin reverse proxy compose file", Apply: remoteWriteFileCommand(composePath, pangolinComposeFile(config), "root", stackGroup, 0640)},
		{Name: "Allow proxy and Pangolin tunnel ingress", Apply: commandScript(
			`public_interface="$(ip -4 route show default 0.0.0.0/0 | awk '{print $5; exit}')"`,
			`test -n "$public_interface"`,
			`egress_interface="$public_interface"`,
			installUFWMasqueradeBlockCommand("Servestead UFW MASQUERADE TRANSLATIONS", "172.17.0.0/16", "172.18.0.0/16", proxyDockerSubnet),
			"ufw allow 80/tcp",
			"ufw allow 443/tcp",
			"ufw route allow from "+shellQuote(proxyDockerSubnet)+" to any",
			`ufw allow in on "$public_interface" to any port 51820 proto udp comment 'Pangolin Tunnel Entrance'`,
			`ufw allow in on "$public_interface" to any port 21820 proto udp comment 'Pangolin Session Tunnel Entrance'`,
			"ufw reload",
		)},
		{Name: "Start Pangolin reverse proxy stack", Apply: commandScript(
			"docker compose -f "+shellQuote(composePath)+" pull",
			"docker compose -f "+shellQuote(composePath)+" down --remove-orphans || true",
			"docker compose -f "+shellQuote(composePath)+" up -d --remove-orphans",
		)},
		{Name: "Bootstrap Pangolin organization and site", Apply: pangolinBootstrapCommand(config)},
		{Name: "Verify Pangolin reverse proxy stack", Apply: commandScript(
			"running=\"$(docker compose -f "+shellQuote(composePath)+" ps --services --status running)\"",
			"for service in pangolin gerbil traefik socket-proxy newt; do",
			"  printf '%s\\n' \"$running\" | grep -Fx \"$service\" >/dev/null",
			"done",
			"docker compose -f "+shellQuote(composePath)+" ps",
		)},
	}
}

func pangolinComposeFile(config proxyConfig) string {
	return mustRenderResourceTemplate(resources.ProxyCompose, struct {
		proxyConfig
		ServesteadPublicNetwork string
		GerbilImage             string
		NewtImage               string
		PangolinEndpoint        string
		PangolinImage           string
		ProxyDockerSubnet       string
		SocketProxyImage        string
		TraefikImage            string
	}{
		proxyConfig:             config,
		ServesteadPublicNetwork: servesteadPublicNetwork,
		GerbilImage:             gerbilImage,
		NewtImage:               newtImage,
		PangolinEndpoint:        "https://pangolin." + config.BaseDomain,
		PangolinImage:           pangolinImage,
		ProxyDockerSubnet:       proxyDockerSubnet,
		SocketProxyImage:        socketProxyImage,
		TraefikImage:            traefikImage,
	})
}

func pangolinBootstrapCommand(config proxyConfig) string {
	adminPayload := fmt.Sprintf(`{"email":%s,"password":%s,"setupToken":%s}`,
		jsonString(config.AdminEmail), jsonString(config.AdminPassword), jsonString(config.SetupToken))
	loginPayload := fmt.Sprintf(`{"email":%s,"password":%s}`,
		jsonString(config.AdminEmail), jsonString(config.AdminPassword))
	sitePayload := fmt.Sprintf(`{"name":"local-vps","niceId":"local-vps","type":"newt","subnet":"100.89.1.0/24","newtId":%s,"secret":%s}`,
		jsonString(config.NewtID), jsonString(config.NewtSecret))
	return commandScript(mustRenderResourceTemplate(resources.ProxyBootstrapPangolinScript, struct {
		AdminPayload string
		LoginPayload string
		SitePayload  string
	}{
		AdminPayload: adminPayload,
		LoginPayload: loginPayload,
		SitePayload:  sitePayload,
	}))
}

func jsonString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func requiredDNSGuidance(baseDomain, host string) string {
	return fmt.Sprintf(
		"Required DNS: A pangolin.%s -> %s, A beszel.%s -> %s, A dozzle.%s -> %s, A dockhand.%s -> %s",
		baseDomain, host, baseDomain, host, baseDomain, host, baseDomain, host,
	)
}

func traefikStaticConfigFile(config proxyConfig) string {
	return mustRenderResourceTemplate(resources.ProxyTraefikStaticConfig, config)
}

func traefikDynamicConfigFile(config proxyConfig) string {
	dashboardHost := "pangolin." + config.BaseDomain
	return mustRenderResourceTemplate(resources.ProxyTraefikDynamicConfig, struct {
		DashboardHost string
	}{DashboardHost: dashboardHost})
}

func pangolinConfigFile(config proxyConfig) string {
	dashboardHost := "pangolin." + config.BaseDomain
	return mustRenderResourceTemplate(resources.ProxyPangolinConfig, struct {
		proxyConfig
		DashboardHost string
		DashboardURL  string
	}{
		proxyConfig:   config,
		DashboardHost: dashboardHost,
		DashboardURL:  "https://" + dashboardHost,
	})
}

func yamlSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func yamlDoubleQuote(value string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `"`, `\"`) + `"`
}
