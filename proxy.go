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
	"strings"
)

const proxyStackDirectory = "/opt/aegisnode/proxy"
const proxyDockerSubnet = "172.30.0.0/24"
const aegisPublicNetwork = "aegis-public"

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
	flags.StringVar(&config.SSHUser, "ssh-user", "aegisadmin", "administrative SSH user")
	flags.StringVar(&config.PrivateKeyPath, "private-key", "", "path to the administrative private key")
	flags.StringVar(&config.BaseDomain, "domain", "", "base domain for Pangolin, for example example.com")
	flags.StringVar(&config.LetsEncryptEmail, "email", "", "Let's Encrypt account email")
	flags.StringVar(&config.ServerSecret, "server-secret", "", "Pangolin server secret")
	flags.StringVar(&config.ServerSecret, "postgres-password", "", "deprecated alias for --server-secret")
	flags.StringVar(&config.SetupToken, "setup-token", "", "Pangolin initial admin setup token (generated when omitted)")
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
		return errors.New("--setup-token must contain exactly 32 lowercase letters or digits")
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
			`  echo 'Docker bridge firewall/NAT is disabled; rerun "aegisnode network" before deploying proxy.' >&2`,
			`  exit 1`,
			`fi`,
		)},
		{Name: "Prepare proxy stack directories", Apply: commandScript(
			"install -d -m 0750 -o root -g "+shellQuote(stackGroup)+" "+shellQuote("/opt/aegisnode"),
			"install -d -m 0750 -o root -g "+shellQuote(stackGroup)+" "+shellQuote(proxyStackDirectory),
			"install -d -m 0750 -o root -g "+shellQuote(stackGroup)+" "+shellQuote(proxyStackDirectory+"/config"),
			"install -d -m 0750 -o root -g "+shellQuote(stackGroup)+" "+shellQuote(proxyStackDirectory+"/config/db"),
			"install -d -m 0750 -o root -g "+shellQuote(stackGroup)+" "+shellQuote(proxyStackDirectory+"/config/letsencrypt"),
			"install -d -m 0750 -o root -g "+shellQuote(stackGroup)+" "+shellQuote(proxyStackDirectory+"/config/traefik"),
			"install -d -m 0750 -o root -g "+shellQuote(stackGroup)+" "+shellQuote(proxyStackDirectory+"/config/traefik/logs"),
		)},
		{Name: "Create shared application network", Apply: commandScript(
			"docker network inspect " + shellQuote(aegisPublicNetwork) + " >/dev/null 2>&1 || docker network create " + shellQuote(aegisPublicNetwork),
		)},
		{Name: "Write Pangolin application config", Apply: remoteWriteFileCommand(proxyStackDirectory+"/config/config.yml", pangolinConfigFile(config), "root", stackGroup, 0640)},
		{Name: "Write Traefik static config", Apply: remoteWriteFileCommand(proxyStackDirectory+"/config/traefik/traefik_config.yml", traefikStaticConfigFile(config), "root", stackGroup, 0640)},
		{Name: "Write Traefik dynamic config", Apply: remoteWriteFileCommand(proxyStackDirectory+"/config/traefik/dynamic_config.yml", traefikDynamicConfigFile(config), "root", stackGroup, 0640)},
		{Name: "Write Pangolin reverse proxy compose file", Apply: remoteWriteFileCommand(composePath, pangolinComposeFile(config), "root", stackGroup, 0640)},
		{Name: "Allow proxy and Pangolin tunnel ingress", Apply: commandScript(
			`public_interface="$(ip -4 route show default 0.0.0.0/0 | awk '{print $5; exit}')"`,
			`test -n "$public_interface"`,
			`egress_interface="$public_interface"`,
			installUFWMasqueradeBlockCommand("AegisNode UFW MASQUERADE TRANSLATIONS", "172.17.0.0/16", "172.18.0.0/16", proxyDockerSubnet),
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
	return strings.Join([]string{
		"services:",
		"  pangolin:",
		"    image: " + pangolinImage,
		"    container_name: pangolin",
		"    restart: unless-stopped",
		"    security_opt:",
		"      - no-new-privileges:true",
		"    environment:",
		"      PANGOLIN_SETUP_TOKEN: " + yamlDoubleQuote(config.SetupToken),
		"    volumes:",
		"      - ./config:/app/config",
		"    ports:",
		"      - \"127.0.0.1:3000:3000\"",
		"    healthcheck:",
		"      test: [\"CMD\", \"curl\", \"-f\", \"http://localhost:3001/api/v1/\"]",
		"      interval: \"10s\"",
		"      timeout: \"10s\"",
		"      retries: 15",
		"",
		"  gerbil:",
		"    image: " + gerbilImage,
		"    container_name: gerbil",
		"    restart: unless-stopped",
		"    depends_on:",
		"      pangolin:",
		"        condition: service_healthy",
		"    command:",
		"      - --reachableAt=http://gerbil:3004",
		"      - --generateAndSaveKeyTo=/var/config/key",
		"      - --remoteConfig=http://pangolin:3001/api/v1/",
		"    volumes:",
		"      - ./config:/var/config",
		"    cap_add:",
		"      - NET_ADMIN",
		"      - SYS_MODULE",
		"    ports:",
		"      - \"51820:51820/udp\"",
		"      - \"21820:21820/udp\"",
		"      - \"443:443\"",
		"      - \"80:80\"",
		"",
		"  traefik:",
		"    image: " + traefikImage,
		"    container_name: traefik",
		"    restart: unless-stopped",
		"    network_mode: service:gerbil",
		"    depends_on:",
		"      pangolin:",
		"        condition: service_healthy",
		"    command:",
		"      - --configFile=/etc/traefik/traefik_config.yml",
		"    volumes:",
		"      - ./config/traefik:/etc/traefik:ro",
		"      - ./config/letsencrypt:/letsencrypt",
		"      - ./config/traefik/logs:/var/log/traefik",
		"",
		"  socket-proxy:",
		"    image: " + socketProxyImage,
		"    container_name: aegis-socket-proxy",
		"    restart: unless-stopped",
		"    security_opt:",
		"      - no-new-privileges:true",
		"    environment:",
		"      CONTAINERS: \"1\"",
		"      EVENTS: \"1\"",
		"      INFO: \"1\"",
		"      NETWORKS: \"1\"",
		"      PING: \"1\"",
		"      VERSION: \"1\"",
		"      POST: \"0\"",
		"    volumes:",
		"      - /var/run/docker.sock:/var/run/docker.sock:ro",
		"    networks:",
		"      - " + aegisPublicNetwork,
		"",
		"  newt:",
		"    image: " + newtImage,
		"    container_name: aegis-newt",
		"    restart: unless-stopped",
		"    depends_on:",
		"      - socket-proxy",
		"    environment:",
		"      PANGOLIN_ENDPOINT: " + yamlDoubleQuote("https://pangolin."+config.BaseDomain),
		"      NEWT_ID: " + yamlDoubleQuote(config.NewtID),
		"      NEWT_SECRET: " + yamlDoubleQuote(config.NewtSecret),
		"      DOCKER_SOCKET: \"tcp://socket-proxy:2375\"",
		"      DOCKER_ENFORCE_NETWORK_VALIDATION: \"true\"",
		"    networks:",
		"      - " + aegisPublicNetwork,
		"",
		"networks:",
		"  default:",
		"    driver: bridge",
		"    name: pangolin",
		"    ipam:",
		"      config:",
		"        - subnet: " + proxyDockerSubnet,
		"  " + aegisPublicNetwork + ":",
		"    external: true",
		"",
	}, "\n")
}

func pangolinBootstrapCommand(config proxyConfig) string {
	adminPayload := fmt.Sprintf(`{"email":%s,"password":%s,"setupToken":%s}`,
		jsonString(config.AdminEmail), jsonString(config.AdminPassword), jsonString(config.SetupToken))
	loginPayload := fmt.Sprintf(`{"email":%s,"password":%s}`,
		jsonString(config.AdminEmail), jsonString(config.AdminPassword))
	sitePayload := fmt.Sprintf(`{"name":"local-vps","niceId":"local-vps","type":"newt","subnet":"100.89.1.0/24","newtId":%s,"secret":%s}`,
		jsonString(config.NewtID), jsonString(config.NewtSecret))
	return commandScript(
		`api='http://127.0.0.1:3000/api/v1'`,
		`pangolin_request() {`,
		`  label="$1"`,
		`  shift`,
		`  response_file="$(mktemp)"`,
		`  status="$(curl -sS -o "$response_file" -w '%{http_code}' "$@")" || {`,
		`    result=$?`,
		`    echo "Pangolin $label request failed before receiving a response." >&2`,
		`    cat "$response_file" >&2`,
		`    rm -f "$response_file"`,
		`    return "$result"`,
		`  }`,
		`  case "$status" in`,
		`    2??) cat "$response_file" ;;`,
		`    *)`,
		`      echo "Pangolin $label failed (HTTP $status):" >&2`,
		`      cat "$response_file" >&2`,
		`      echo >&2`,
		`      rm -f "$response_file"`,
		`      return 1`,
		`      ;;`,
		`  esac`,
		`  rm -f "$response_file"`,
		`}`,
		`for attempt in $(seq 1 60); do curl -fsS "$api/" >/dev/null && break; sleep 2; done`,
		`curl -fsS "$api/" >/dev/null`,
		`setup="$(curl -fsS "$api/auth/initial-setup-complete")"`,
		`if printf '%s' "$setup" | grep -Eq '"complete"[[:space:]]*:[[:space:]]*false'; then`,
		`  pangolin_request 'administrator creation' -X PUT "$api/auth/set-server-admin" -H 'Content-Type: application/json' -H 'X-CSRF-Token: x-csrf-protection' --data `+shellQuote(adminPayload)+` >/dev/null`,
		`fi`,
		`cookie_file="$(mktemp)"`,
		`trap 'rm -f "$cookie_file"' EXIT`,
		`pangolin_request 'administrator login' -c "$cookie_file" -X POST "$api/auth/login" -H 'Content-Type: application/json' -H 'X-CSRF-Token: x-csrf-protection' --data `+shellQuote(loginPayload)+` >/dev/null`,
		`orgs="$(curl -fsS -b "$cookie_file" "$api/orgs")"`,
		`if ! printf '%s' "$orgs" | grep -Eq '"orgId"[[:space:]]*:[[:space:]]*"aegisnode"'; then`,
		`  pangolin_request 'organization creation' -b "$cookie_file" -X PUT "$api/org" -H 'Content-Type: application/json' -H 'X-CSRF-Token: x-csrf-protection' --data '{"orgId":"aegisnode","name":"AegisNode","subnet":"100.89.0.0/16","utilitySubnet":"100.88.0.0/16"}' >/dev/null`,
		`fi`,
		`sites="$(curl -fsS -b "$cookie_file" "$api/org/aegisnode/sites?pageSize=100")"`,
		`if ! printf '%s' "$sites" | grep -Eq '"niceId"[[:space:]]*:[[:space:]]*"local-vps"'; then`,
		`  pangolin_request 'Newt site creation' -b "$cookie_file" -X PUT "$api/org/aegisnode/site" -H 'Content-Type: application/json' -H 'X-CSRF-Token: x-csrf-protection' --data `+shellQuote(sitePayload)+` >/dev/null`,
		`fi`,
	)
}

func jsonString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func printPangolinSetupGuidance(output io.Writer, baseDomain, setupToken string) {
	if setupToken == "" {
		return
	}
	fmt.Fprintf(output, "Pangolin initial setup: https://pangolin.%s/auth/initial-setup\n", baseDomain)
	fmt.Fprintf(output, "Pangolin setup token: %s\n", setupToken)
	fmt.Fprintln(output, "The token is valid only until the initial server admin is registered.")
}

func requiredDNSGuidance(baseDomain, host string) string {
	return fmt.Sprintf(
		"Required DNS: A pangolin.%s -> %s, A beszel.%s -> %s, A dozzle.%s -> %s",
		baseDomain, host, baseDomain, host, baseDomain, host,
	)
}

func traefikStaticConfigFile(config proxyConfig) string {
	return strings.Join([]string{
		"api:",
		"  insecure: true",
		"  dashboard: true",
		"providers:",
		"  http:",
		"    endpoint: \"http://pangolin:3001/api/v1/traefik-config\"",
		"    pollInterval: \"5s\"",
		"  file:",
		"    filename: \"/etc/traefik/dynamic_config.yml\"",
		"experimental:",
		"  plugins:",
		"    badger:",
		"      moduleName: \"github.com/fosrl/badger\"",
		"      version: \"v1.4.0\"",
		"log:",
		"  level: \"INFO\"",
		"  format: \"common\"",
		"certificatesResolvers:",
		"  letsencrypt:",
		"    acme:",
		"      httpChallenge:",
		"        entryPoint: web",
		"      email: " + yamlDoubleQuote(config.LetsEncryptEmail),
		"      storage: \"/letsencrypt/acme.json\"",
		"      caServer: \"https://acme-v02.api.letsencrypt.org/directory\"",
		"entryPoints:",
		"  web:",
		"    address: \":80\"",
		"  websecure:",
		"    address: \":443\"",
		"    transport:",
		"      respondingTimeouts:",
		"        readTimeout: \"30m\"",
		"    http:",
		"      tls:",
		"        certResolver: \"letsencrypt\"",
		"      encodedCharacters:",
		"        allowEncodedSlash: true",
		"        allowEncodedQuestionMark: true",
		"serversTransport:",
		"  insecureSkipVerify: true",
		"ping:",
		"  entryPoint: \"web\"",
		"",
	}, "\n")
}

func traefikDynamicConfigFile(config proxyConfig) string {
	dashboardHost := "pangolin." + config.BaseDomain
	return strings.Join([]string{
		"http:",
		"  middlewares:",
		"    badger:",
		"      plugin:",
		"        badger:",
		"          disableForwardAuth: true",
		"    redirect-to-https:",
		"      redirectScheme:",
		"        scheme: https",
		"  routers:",
		"    main-app-router-redirect:",
		"      rule: \"Host(`" + dashboardHost + "`)\"",
		"      service: next-service",
		"      entryPoints:",
		"        - web",
		"      middlewares:",
		"        - redirect-to-https",
		"        - badger",
		"    next-router:",
		"      rule: \"Host(`" + dashboardHost + "`) && !PathPrefix(`/api/v1`)\"",
		"      service: next-service",
		"      entryPoints:",
		"        - websecure",
		"      middlewares:",
		"        - badger",
		"      tls:",
		"        certResolver: letsencrypt",
		"    api-router:",
		"      rule: \"Host(`" + dashboardHost + "`) && PathPrefix(`/api/v1`)\"",
		"      service: api-service",
		"      entryPoints:",
		"        - websecure",
		"      middlewares:",
		"        - badger",
		"      tls:",
		"        certResolver: letsencrypt",
		"    ws-router:",
		"      rule: \"Host(`" + dashboardHost + "`)\"",
		"      service: api-service",
		"      entryPoints:",
		"        - websecure",
		"      middlewares:",
		"        - badger",
		"      tls:",
		"        certResolver: letsencrypt",
		"  services:",
		"    next-service:",
		"      loadBalancer:",
		"        servers:",
		"          - url: \"http://pangolin:3002\"",
		"    api-service:",
		"      loadBalancer:",
		"        servers:",
		"          - url: \"http://pangolin:3000\"",
		"tcp:",
		"  serversTransports:",
		"    pp-transport-v1:",
		"      proxyProtocol:",
		"        version: 1",
		"    pp-transport-v2:",
		"      proxyProtocol:",
		"        version: 2",
		"",
	}, "\n")
}

func pangolinConfigFile(config proxyConfig) string {
	dashboardHost := "pangolin." + config.BaseDomain
	return strings.Join([]string{
		"gerbil:",
		"  start_port: 51820",
		"  base_endpoint: " + yamlSingleQuote(dashboardHost),
		"app:",
		"  dashboard_url: " + yamlSingleQuote("https://"+dashboardHost),
		"  log_level: info",
		"  telemetry:",
		"    anonymous_usage: true",
		"domains:",
		"  domain1:",
		"    base_domain: " + yamlSingleQuote(config.BaseDomain),
		"server:",
		"  secret: " + yamlSingleQuote(config.ServerSecret),
		"  cors:",
		"    origins:",
		"      - " + yamlSingleQuote("https://"+dashboardHost),
		"    methods:",
		"      - GET",
		"      - POST",
		"      - PUT",
		"      - DELETE",
		"      - PATCH",
		"    allowed_headers:",
		"      - X-CSRF-Token",
		"      - Content-Type",
		"    credentials: false",
		"flags:",
		"  require_email_verification: false",
		"  disable_signup_without_invite: true",
		"  disable_user_create_org: false",
		"  allow_raw_resources: true",
		"",
	}, "\n")
}

func yamlSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func yamlDoubleQuote(value string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `"`, `\"`) + `"`
}
