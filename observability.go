package main

import (
	"context"
	"fmt"
	"io"
	"strings"
)

const observabilityStackDirectory = "/opt/aegisnode/stacks/observability"

const (
	beszelImage      = "docker.io/henrygd/beszel:0.18.7"
	beszelAgentImage = "docker.io/henrygd/beszel-agent:0.18.7"
	dozzleImage      = "docker.io/amir20/dozzle:v10.6.6"
)

type observabilityConfig struct {
	Host             string
	SSHUser          string
	PrivateKeyPath   string
	BaseDomain       string
	AdminEmail       string
	AdminPassword    string
	PangolinPassword string
	SystemToken      string
	HubPrivateKey    string
	HubPublicKey     string
}

type observabilityRemoteClientFactory func(context.Context, observabilityConfig, io.Writer, io.Writer) (remoteClient, error)

var newObservabilityRemoteClient observabilityRemoteClientFactory = func(ctx context.Context, config observabilityConfig, stdout, stderr io.Writer) (remoteClient, error) {
	return newSSHRemoteClient(ctx, config.Host, config.SSHUser, config.PrivateKeyPath, stdout, stderr)
}

func runObservabilityStepsWithReporter(ctx context.Context, client remoteClient, config observabilityConfig, runID string, reporter TaskReporter, progress io.Writer) error {
	return runTasksWithReporter(ctx, client, config.SSHUser, runID, "observability", observabilityTasks(config), progress, reporter)
}

func observabilityTasks(config observabilityConfig) []Task {
	composePath := observabilityStackDirectory + "/docker-compose.yml"
	group := firstNonEmpty(config.SSHUser, "root")
	return []Task{
		{Name: "Prepare observability directories", Apply: commandScript(
			"install -d -m 0750 -o root -g "+shellQuote(group)+" "+shellQuote("/opt/aegisnode/stacks"),
			"install -d -m 0750 -o root -g "+shellQuote(group)+" "+shellQuote(observabilityStackDirectory),
			"install -d -m 0750 -o root -g "+shellQuote(group)+" "+shellQuote(observabilityStackDirectory+"/beszel_data"),
			"install -d -m 0750 -o root -g "+shellQuote(group)+" "+shellQuote(observabilityStackDirectory+"/agent_keys"),
		)},
		{Name: "Write Beszel system configuration", Apply: remoteWriteFileCommand(observabilityStackDirectory+"/beszel_data/config.yml", beszelConfigFile(config), "root", group, 0600)},
		{Name: "Write Beszel Hub private key", Apply: remoteWriteFileCommand(observabilityStackDirectory+"/beszel_data/id_ed25519", config.HubPrivateKey, "root", group, 0600)},
		{Name: "Write Beszel agent public key", Apply: remoteWriteFileCommand(observabilityStackDirectory+"/agent_keys/id_ed25519.pub", config.HubPublicKey+"\n", "root", group, 0600)},
		{Name: "Write observability compose file", Apply: remoteWriteFileCommand(composePath, observabilityComposeFile(config), "root", group, 0600)},
		{Name: "Validate observability compose file", Apply: commandScript(
			"docker compose -f " + shellQuote(composePath) + " config --quiet",
		)},
		{Name: "Reconcile Pangolin observability resources", Apply: observabilityResourceReconcileCommand(config, composePath)},
		{Name: "Start observability stack", Apply: commandScript(
			"start_result=0",
			"docker compose -f "+shellQuote(composePath)+" pull && docker compose -f "+shellQuote(composePath)+" up -d --remove-orphans || start_result=$?",
			"docker start aegis-newt >/dev/null",
			"exit \"$start_result\"",
		)},
		{Name: "Verify Pangolin observability resources", Apply: observabilityResourceVerifyCommand(config)},
		{Name: "Verify observability stack", Apply: commandScript(
			"running=\"$(docker compose -f "+shellQuote(composePath)+" ps --services --status running)\"",
			"for service in beszel beszel-agent dozzle; do",
			"  printf '%s\\n' \"$running\" | grep -Fx \"$service\" >/dev/null",
			"done",
			"docker compose -f "+shellQuote(composePath)+" ps",
		)},
	}
}

func beszelConfigFile(config observabilityConfig) string {
	return strings.Join([]string{
		"systems:",
		"  - name: local-vps",
		"    host: beszel-agent",
		"    port: 45876",
		"    token: " + yamlSingleQuote(config.SystemToken),
		"    users:",
		"      - " + yamlSingleQuote(config.AdminEmail),
		"",
	}, "\n")
}

func observabilityComposeFile(config observabilityConfig) string {
	labels := func(key, name, host string, port int) []string {
		prefix := "pangolin.public-resources." + key
		return []string{
			"      - " + prefix + ".name=" + name,
			"      - " + prefix + ".protocol=http",
			"      - " + prefix + ".full-domain=" + host + "." + config.BaseDomain,
			"      - " + prefix + ".auth.sso-enabled=true",
			"      - " + prefix + ".auth.sso-users[0]=" + config.AdminEmail,
			"      - " + prefix + ".targets[0].hostname=" + host,
			fmt.Sprintf("      - %s.targets[0].port=%d", prefix, port),
			"      - " + prefix + ".targets[0].method=http",
		}
	}
	lines := []string{
		"services:",
		"  beszel:",
		"    image: " + beszelImage,
		"    container_name: beszel",
		"    restart: unless-stopped",
		"    security_opt:",
		"      - no-new-privileges:true",
		"    environment:",
		"      USER_EMAIL: " + yamlDoubleQuote(config.AdminEmail),
		"      USER_PASSWORD: " + yamlDoubleQuote(config.AdminPassword),
		"      TRUSTED_AUTH_HEADER: \"Remote-Email\"",
		"      APP_URL: " + yamlDoubleQuote("https://beszel."+config.BaseDomain),
		"      DISABLE_PASSWORD_AUTH: \"true\"",
		"    expose:",
		"      - \"8090\"",
		"    volumes:",
		"      - ./beszel_data:/beszel_data",
		"    networks:",
		"      - " + aegisPublicNetwork,
		"    labels:",
	}
	lines = append(lines, labels("aegisnode-beszel", "Beszel", "beszel", 8090)...)
	lines = append(lines,
		"",
		"  beszel-agent:",
		"    image: "+beszelAgentImage,
		"    container_name: beszel-agent",
		"    restart: unless-stopped",
		"    depends_on:",
		"      - beszel",
		"    environment:",
		"      HUB_URL: \"http://beszel:8090\"",
		"      TOKEN: "+yamlDoubleQuote(config.SystemToken),
		"      KEY_FILE: \"/keys/id_ed25519.pub\"",
		"      DOCKER_HOST: \"tcp://socket-proxy:2375\"",
		"    volumes:",
		"      - ./agent_keys:/keys:ro",
		"      - /proc:/host/proc:ro",
		"      - /sys:/host/sys:ro",
		"      - /etc/os-release:/etc/os-release:ro",
		"    networks:",
		"      - "+aegisPublicNetwork,
		"",
		"  dozzle:",
		"    image: "+dozzleImage,
		"    container_name: dozzle",
		"    restart: unless-stopped",
		"    security_opt:",
		"      - no-new-privileges:true",
		"    environment:",
		"      DOCKER_HOST: \"tcp://socket-proxy:2375\"",
		"      DOZZLE_AUTH_PROVIDER: \"forward-proxy\"",
		"      DOZZLE_ENABLE_ACTIONS: \"false\"",
		"      DOZZLE_ENABLE_SHELL: \"false\"",
		"    expose:",
		"      - \"8080\"",
		"    networks:",
		"      - "+aegisPublicNetwork,
		"    labels:",
	)
	lines = append(lines, labels("aegisnode-dozzle", "Dozzle", "dozzle", 8080)...)
	lines = append(lines,
		"",
		"networks:",
		"  "+aegisPublicNetwork+":",
		"    external: true",
		"",
	)
	return strings.Join(lines, "\n")
}

func observabilityResourceReconcileCommand(config observabilityConfig, composePath string) string {
	loginPayload := fmt.Sprintf(`{"email":%s,"password":%s}`,
		jsonString(config.AdminEmail), jsonString(config.PangolinPassword))
	specs := observabilityResourceSpecs(config.BaseDomain)
	selectDeletes := `import json,sys
data=json.load(sys.stdin)["data"]["resources"]
specs=json.loads(sys.argv[1])
for spec in specs:
 matches=[r for r in data if r.get("name")==spec["name"] and r.get("fullDomain")==spec["domain"]]
 canonical=[r for r in matches if r.get("niceId")==spec["nice_id"]]
 keep=canonical[0].get("resourceId") if canonical else None
 for resource in matches:
  if resource.get("resourceId") != keep:
   print(resource["resourceId"])`
	return commandScript(
		"docker stop aegis-newt >/dev/null",
		"docker compose -f "+shellQuote(composePath)+" down --remove-orphans >/dev/null 2>&1 || true",
		"sleep 2",
		`api='http://127.0.0.1:3000/api/v1'`,
		`cookie_file="$(mktemp)"`,
		`reconciliation_complete=0`,
		`cleanup_reconciliation() {`,
		`  rm -f "$cookie_file"`,
		`  if [ "$reconciliation_complete" != "1" ]; then docker start aegis-newt >/dev/null 2>&1 || true; fi`,
		`}`,
		`trap cleanup_reconciliation EXIT`,
		`curl -fsS -c "$cookie_file" -X POST "$api/auth/login" -H 'Content-Type: application/json' -H 'X-CSRF-Token: x-csrf-protection' --data `+shellQuote(loginPayload)+` >/dev/null`,
		`resources="$(curl -fsS -b "$cookie_file" "$api/org/aegisnode/resources?pageSize=100")"`,
		`delete_ids="$(printf '%s' "$resources" | python3 -c `+shellQuote(selectDeletes)+` `+shellQuote(specs)+`)"`,
		`for resource_id in $delete_ids; do`,
		`  curl -fsS -b "$cookie_file" -X DELETE "$api/resource/$resource_id" -H 'X-CSRF-Token: x-csrf-protection' >/dev/null`,
		`done`,
		`reconciliation_complete=1`,
	)
}

func observabilityResourceVerifyCommand(config observabilityConfig) string {
	loginPayload := fmt.Sprintf(`{"email":%s,"password":%s}`,
		jsonString(config.AdminEmail), jsonString(config.PangolinPassword))
	specs := observabilityResourceSpecs(config.BaseDomain)
	verifyResources := `import json,sys
data=json.load(sys.stdin)["data"]["resources"]
specs=json.loads(sys.argv[1])
ok=True
for spec in specs:
 matches=[r for r in data if r.get("name")==spec["name"] and r.get("fullDomain")==spec["domain"]]
 canonical=[r for r in matches if r.get("niceId")==spec["nice_id"]]
 if len(matches)!=1 or len(canonical)!=1:
  ok=False
sys.exit(0 if ok else 1)`
	return commandScript(
		`api='http://127.0.0.1:3000/api/v1'`,
		`cookie_file="$(mktemp)"`,
		`trap 'rm -f "$cookie_file"' EXIT`,
		`curl -fsS -c "$cookie_file" -X POST "$api/auth/login" -H 'Content-Type: application/json' -H 'X-CSRF-Token: x-csrf-protection' --data `+shellQuote(loginPayload)+` >/dev/null`,
		`for attempt in $(seq 1 30); do`,
		`  resources="$(curl -fsS -b "$cookie_file" "$api/org/aegisnode/resources?pageSize=100")"`,
		`  if printf '%s' "$resources" | python3 -c `+shellQuote(verifyResources)+` `+shellQuote(specs)+`; then exit 0; fi`,
		`  sleep 2`,
		`done`,
		`echo 'Pangolin did not converge to exactly one managed Beszel and Dozzle resource.' >&2`,
		`exit 1`,
	)
}

func observabilityResourceSpecs(baseDomain string) string {
	return fmt.Sprintf(
		`[{"name":"Beszel","domain":%s,"nice_id":"aegisnode-beszel"},{"name":"Dozzle","domain":%s,"nice_id":"aegisnode-dozzle"}]`,
		jsonString("beszel."+baseDomain), jsonString("dozzle."+baseDomain),
	)
}
