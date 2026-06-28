package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
)

const observabilityStackDirectory = "/opt/aegisnode/stacks/observability"
const observabilityRepositoryDirectory = "/opt/aegisnode/repository"
const observabilityEnvironmentPath = "/etc/aegisnode/observability.env"

const (
	beszelImage      = "docker.io/henrygd/beszel:0.18.7"
	beszelAgentImage = "docker.io/henrygd/beszel-agent:0.18.7"
	dozzleImage      = "docker.io/amir20/dozzle:v10.6.6"
)

type observabilityConfig struct {
	Host              string
	SSHUser           string
	PrivateKeyPath    string
	BaseDomain        string
	AdminEmail        string
	AdminPassword     string
	PangolinPassword  string
	SystemToken       string
	HubPrivateKey     string
	HubPublicKey      string
	RepositoryCommit  string
	RepositoryOrigin  string
	RepositoryCompose string
	RepositorySHA256  string
	GitHubToken       string
	Stacks            []configuredStack
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
	composeCommand := "docker compose -f " + shellQuote(composePath)
	declarative := config.RepositoryCommit != "" && config.RepositoryCompose != ""
	if declarative {
		composePath = observabilityRepositoryDirectory + "/" + observabilityComposeRepositoryPath
		composeCommand = "docker compose --env-file " + shellQuote(observabilityEnvironmentPath) + " -p observability -f " + shellQuote(composePath)
	}
	group := firstNonEmpty(config.SSHUser, "root")
	tasks := []Task{
		{Name: "Prepare observability directories", Apply: commandScript(
			"install -d -m 0750 -o root -g "+shellQuote(group)+" "+shellQuote("/opt/aegisnode/stacks"),
			"install -d -m 0750 -o root -g "+shellQuote(group)+" "+shellQuote(observabilityStackDirectory),
			"install -d -m 0750 -o root -g "+shellQuote(group)+" "+shellQuote(observabilityStackDirectory+"/beszel_data"),
			"install -d -m 0750 -o root -g "+shellQuote(group)+" "+shellQuote(observabilityStackDirectory+"/agent_keys"),
			"install -d -m 0750 -o root -g "+shellQuote(group)+" /etc/aegisnode",
		)},
		{Name: "Write Beszel system configuration", Apply: remoteWriteFileCommand(observabilityStackDirectory+"/beszel_data/config.yml", beszelConfigFile(config), "root", group, 0600)},
		{Name: "Write Beszel Hub private key", Apply: remoteWriteFileCommand(observabilityStackDirectory+"/beszel_data/id_ed25519", config.HubPrivateKey, "root", group, 0600)},
		{Name: "Write Beszel agent public key", Apply: remoteWriteFileCommand(observabilityStackDirectory+"/agent_keys/id_ed25519.pub", config.HubPublicKey+"\n", "root", group, 0600)},
	}
	if declarative {
		tasks = append(tasks,
			Task{Name: "Write observability environment", Apply: remoteWriteFileCommand(observabilityEnvironmentPath, observabilityEnvironmentFile(config), "root", "root", 0600)},
			observabilityRepositoryTask(config, group),
			Task{Name: "Validate observability compose file", Apply: commandScript(
				"[ ! -f "+shellQuote(observabilityStackDirectory+"/docker-compose.yml")+" ] || docker compose -f "+shellQuote(observabilityStackDirectory+"/docker-compose.yml")+" config --quiet",
				composeCommand+" config --quiet",
			)},
		)
	} else {
		tasks = append(tasks,
			Task{Name: "Write observability compose file", Apply: remoteWriteFileCommand(composePath, observabilityComposeFile(config), "root", group, 0600)},
			Task{Name: "Validate observability compose file", Apply: commandScript(composeCommand + " config --quiet")},
		)
	}
	tasks = append(tasks,
		Task{Name: "Reconcile Pangolin observability resources", Apply: observabilityResourceReconcileCommand(config, composeCommand)},
		Task{Name: "Start observability stack", Apply: commandScript(
			"start_result=0",
			composeCommand+" pull && "+composeCommand+" up -d --remove-orphans || start_result=$?",
			"docker start aegis-newt >/dev/null",
			"exit \"$start_result\"",
		)},
		Task{Name: "Verify Pangolin observability resources", Apply: observabilityResourceVerifyCommand(config)},
		Task{Name: "Verify observability stack", Apply: commandScript(
			"running=\"$("+composeCommand+" ps --services --status running)\"",
			"for service in beszel beszel-agent dozzle; do",
			"  printf '%s\\n' \"$running\" | grep -Fx \"$service\" >/dev/null",
			"done",
			composeCommand+" ps",
		)},
	)
	for _, stack := range config.Stacks {
		tasks = append(tasks, configuredStackTasks(config, stack, group)...)
	}
	if declarative {
		tasks = append(tasks, Task{Name: "Remove migrated legacy compose file", Apply: commandScript(
			"rm -f " + shellQuote(observabilityStackDirectory+"/docker-compose.yml"),
		)})
	}
	return tasks
}

func configuredStackTasks(config observabilityConfig, stack configuredStack, group string) []Task {
	composePath := observabilityRepositoryDirectory + "/stacks/" + stack.Name + "/compose.yaml"
	overridePath := "/opt/aegisnode/generated/" + stack.Name + ".pangolin.yaml"
	deploymentPath := observabilityRepositoryDirectory + ".stack-" + stack.Name + ".deployment"
	composeCommand := "docker compose -p " + shellQuote("aegisnode-"+stack.Name) +
		" -f " + shellQuote(composePath) + " -f " + shellQuote(overridePath)

	tasks := []Task{}
	if config.RepositoryOrigin == "" {
		files := stack.Files
		if len(files) == 0 {
			files = map[string]string{"compose.yaml": stack.Compose, stackMetadataFilename: stack.Metadata}
		}
		names := make([]string, 0, len(files))
		for name := range files {
			names = append(names, name)
		}
		sort.Strings(names)
		manifestLines := make([]string, 0, len(names))
		deployCommands := []string{
			"if [ -f " + shellQuote(deploymentPath) + " ]; then",
			"  (cd " + shellQuote(filepath.Dir(composePath)) + " && sha256sum -c " + shellQuote(deploymentPath) + ") || { echo 'remote " + stack.Name + " stack files have drifted' >&2; exit 1; }",
			"  while IFS= read -r entry; do managed_file=\"${entry#*  }\"; rm -f -- " + shellQuote(filepath.Dir(composePath)) + "/\"$managed_file\"; done < " + shellQuote(deploymentPath),
			"fi",
		}
		for _, name := range names {
			remotePath := filepath.Join(filepath.Dir(composePath), filepath.FromSlash(name))
			deployCommands = append(deployCommands, remoteWriteFileCommand(remotePath, files[name], "root", group, 0640))
			manifestLines = append(manifestLines, sha256Hex(files[name])+"  "+name)
		}
		deployCommands = append(deployCommands, remoteWriteFileCommand(
			deploymentPath, strings.Join(manifestLines, "\n")+"\n", "root", group, 0640,
		))
		tasks = append(tasks, Task{Name: "Deploy committed " + stack.Name + " stack", Apply: commandScript(deployCommands...)})
	}
	tasks = append(tasks,
		Task{Name: "Generate " + stack.Name + " Pangolin override", Apply: commandScript(
			"install -d -m 0750 -o root -g "+shellQuote(group)+" /opt/aegisnode/generated",
			remoteWriteFileCommand(overridePath, stack.Override, "root", group, 0640),
		)},
		Task{Name: "Validate " + stack.Name + " Compose and Pangolin labels", Apply: commandScript(
			composeCommand+" config --quiet",
			composeCommand+" config --format json | python3 -c "+shellQuote(`import json,sys
data=json.load(sys.stdin)
labels={}
for service in data["services"].values():
 labels.update(service.get("labels",{}))
required=[".full-domain",".targets[0].hostname",".targets[0].port"]
ok=all(any(key.endswith(suffix) for key in labels) for suffix in required)
sys.exit(0 if ok else 1)`),
		)},
		Task{Name: "Start " + stack.Name + " stack and reconcile Pangolin", Apply: commandScript(
			"docker stop aegis-newt >/dev/null",
			"start_result=0",
			composeCommand+" pull && "+composeCommand+" up -d --remove-orphans || start_result=$?",
			"docker start aegis-newt >/dev/null",
			"exit \"$start_result\"",
		)},
		Task{Name: "Verify " + stack.Name + " Pangolin public resources", Apply: stackResourceVerifyCommand(config, stack)},
		Task{Name: "Verify " + stack.Name + " stack", Apply: commandScript(
			"expected=\"$("+composeCommand+" config --services)\"",
			"running=\"$("+composeCommand+" ps --services --status running)\"",
			"for service in $expected; do printf '%s\\n' \"$running\" | grep -Fx \"$service\" >/dev/null; done",
			composeCommand+" ps",
		)},
	)
	return tasks
}

func stackRepositoryReconcileTasks(config observabilityConfig, group string) []Task {
	tasks := []Task{}
	if config.RepositoryOrigin != "" {
		tasks = append(tasks, observabilityRepositoryTask(config, group))
	}
	tasks = append(tasks, removedStackCleanupTask(config))
	for _, stack := range config.Stacks {
		tasks = append(tasks, configuredStackTasks(config, stack, group)...)
	}
	return tasks
}

func removedStackCleanupTask(config observabilityConfig) Task {
	names := make([]string, 0, len(config.Stacks))
	for _, stack := range config.Stacks {
		names = append(names, stack.Name)
	}
	sort.Strings(names)
	desired := " " + strings.Join(names, " ") + " "
	loginPayload := fmt.Sprintf(`{"email":%s,"password":%s}`,
		jsonString(config.AdminEmail), jsonString(config.PangolinPassword))
	selectResources := `import json,sys
resources=json.load(sys.stdin)["data"]["resources"]
names=sys.argv[1:]
for resource in resources:
 nice_id=resource.get("niceId","")
 if any(nice_id.startswith("aegisnode-"+name+"-") for name in names):
  print(resource["resourceId"])`
	script := commandScript(
		"desired="+shellQuote(desired),
		"removed=''",
		"for candidate in "+shellQuote(observabilityRepositoryDirectory)+".stack-*.deployment /opt/aegisnode/generated/*.pangolin.yaml; do",
		"  [ -e \"$candidate\" ] || continue",
		"  case \"$candidate\" in",
		"    "+shellQuote(observabilityRepositoryDirectory)+".stack-*.deployment) name=\"${candidate#"+observabilityRepositoryDirectory+".stack-}\"; name=\"${name%.deployment}\" ;;",
		"    /opt/aegisnode/generated/*.pangolin.yaml) name=\"${candidate#/opt/aegisnode/generated/}\"; name=\"${name%.pangolin.yaml}\" ;;",
		"  esac",
		"  case \"$name\" in ''|*[!a-z0-9-]*) continue ;; esac",
		"  case \"$desired\" in *\" $name \"*) continue ;; esac",
		"  case \" $removed \" in *\" $name \"*) continue ;; esac",
		"  removed=\"$removed $name\"",
		"done",
		"[ -n \"$removed\" ] || exit 0",
		`cookie_file="$(mktemp)"`,
		`cleanup_removed_stacks() { rm -f "$cookie_file"; docker start aegis-newt >/dev/null 2>&1 || true; }`,
		"trap cleanup_removed_stacks EXIT",
		"docker stop aegis-newt >/dev/null 2>&1 || true",
		"for name in $removed; do",
		"  project=\"aegisnode-$name\"",
		"  containers=\"$(docker ps -aq --filter label=com.docker.compose.project=\"$project\")\"",
		"  [ -z \"$containers\" ] || docker rm -f $containers",
		"  networks=\"$(docker network ls -q --filter label=com.docker.compose.project=\"$project\")\"",
		"  [ -z \"$networks\" ] || docker network rm $networks",
		"  rm -rf -- "+shellQuote(observabilityRepositoryDirectory)+"/stacks/\"$name\"",
		"  rm -f -- /opt/aegisnode/generated/\"$name\".pangolin.yaml "+shellQuote(observabilityRepositoryDirectory)+".stack-\"$name\".deployment",
		"done",
		`api='http://127.0.0.1:3000/api/v1'`,
		`curl -fsS -c "$cookie_file" -X POST "$api/auth/login" -H 'Content-Type: application/json' -H 'X-CSRF-Token: x-csrf-protection' --data `+shellQuote(loginPayload)+` >/dev/null`,
		`resources="$(curl -fsS -b "$cookie_file" "$api/org/aegisnode/resources?pageSize=100")"`,
		`delete_ids="$(printf '%s' "$resources" | python3 -c `+shellQuote(selectResources)+` $removed)"`,
		`for resource_id in $delete_ids; do`,
		`  curl -fsS -b "$cookie_file" -X DELETE "$api/resource/$resource_id" -H 'X-CSRF-Token: x-csrf-protection' >/dev/null`,
		`done`,
		"docker start aegis-newt >/dev/null",
		`rm -f "$cookie_file"`,
		"trap - EXIT",
	)
	return Task{Name: "Remove stacks deleted from committed configuration", Apply: script}
}

func stackResourceVerifyCommand(config observabilityConfig, stack configuredStack) string {
	specs := make([]string, 0, len(stack.Resources))
	for _, resource := range stack.Resources {
		specs = append(specs, fmt.Sprintf(
			`{"nice_id":%s,"domain":%s}`,
			jsonString("aegisnode-"+stack.Name+"-"+slugifyStackValue(resource.Service)),
			jsonString(resource.Subdomain+"."+config.BaseDomain),
		))
	}
	specification := "[" + strings.Join(specs, ",") + "]"
	loginPayload := fmt.Sprintf(`{"email":%s,"password":%s}`,
		jsonString(config.AdminEmail), jsonString(config.PangolinPassword))
	verify := `import json,sys
resources=json.load(sys.stdin)["data"]["resources"]
specs=json.loads(sys.argv[1])
ok=True
for spec in specs:
 matches=[r for r in resources if r.get("niceId")==spec["nice_id"] and r.get("fullDomain")==spec["domain"]]
 if len(matches)!=1:
  ok=False
sys.exit(0 if ok else 1)`
	return commandScript(
		`api='http://127.0.0.1:3000/api/v1'`,
		`cookie_file="$(mktemp)"`,
		`trap 'rm -f "$cookie_file"' EXIT`,
		`curl -fsS -c "$cookie_file" -X POST "$api/auth/login" -H 'Content-Type: application/json' -H 'X-CSRF-Token: x-csrf-protection' --data `+shellQuote(loginPayload)+` >/dev/null`,
		`for attempt in $(seq 1 30); do`,
		`  resources="$(curl -fsS -b "$cookie_file" "$api/org/aegisnode/resources?pageSize=100")"`,
		`  if printf '%s' "$resources" | python3 -c `+shellQuote(verify)+` `+shellQuote(specification)+`; then exit 0; fi`,
		`  sleep 2`,
		`done`,
		`echo `+shellQuote("Pangolin did not create the expected public resources for stack "+stack.Name)+` >&2`,
		`exit 1`,
	)
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func observabilityEnvironmentFile(config observabilityConfig) string {
	return strings.Join([]string{
		"BESZEL_ADMIN_PASSWORD=" + config.AdminPassword,
		"BESZEL_SYSTEM_TOKEN=" + config.SystemToken,
		"",
	}, "\n")
}

func observabilityRepositoryTask(config observabilityConfig, group string) Task {
	deploymentPath := observabilityRepositoryDirectory + ".deployment"
	verifySnapshot := commandScript(
		"if [ -e "+shellQuote(observabilityRepositoryDirectory)+" ] && [ ! -d "+shellQuote(observabilityRepositoryDirectory+"/.git")+" ] && [ ! -f "+shellQuote(deploymentPath)+" ]; then",
		"  echo 'remote configuration directory is not managed by AegisNode' >&2",
		"  exit 1",
		"fi",
		"if [ -f "+shellQuote(deploymentPath)+" ] && [ ! -d "+shellQuote(observabilityRepositoryDirectory+"/.git")+" ]; then",
		"  recorded_hash=\"$(sed -n 's/^sha256=//p' "+shellQuote(deploymentPath)+")\"",
		"  actual_hash=\"$(sha256sum "+shellQuote(observabilityRepositoryDirectory+"/"+observabilityComposeRepositoryPath)+" | awk '{print $1}')\"",
		"  [ \"$recorded_hash\" = \"$actual_hash\" ] || { echo 'remote configuration snapshot has drifted' >&2; exit 1; }",
		"fi",
	)
	if config.RepositoryOrigin == "" {
		metadata := "commit=" + config.RepositoryCommit + "\nsha256=" + config.RepositorySHA256 + "\n"
		return Task{Name: "Deploy committed configuration snapshot", Apply: commandScript(
			verifySnapshot,
			"if [ -d "+shellQuote(observabilityRepositoryDirectory+"/.git")+" ]; then echo 'remote Git checkout exists; refusing snapshot overwrite' >&2; exit 1; fi",
			"install -d -m 0750 -o root -g "+shellQuote(group)+" "+shellQuote(observabilityRepositoryDirectory+"/stacks/observability"),
			remoteWriteFileCommand(observabilityRepositoryDirectory+"/"+observabilityComposeRepositoryPath, config.RepositoryCompose, "root", group, 0640),
			remoteWriteFileCommand(deploymentPath, metadata, "root", group, 0640),
		)}
	}

	script := commandScript(
		"IFS= read -r AEGISNODE_GITHUB_TOKEN || true",
		"export AEGISNODE_GITHUB_TOKEN",
		"askpass=\"$(mktemp)\"",
		"checkout=\"$(mktemp -d)\"",
		"cleanup_git_credentials() { rm -f \"$askpass\"; rm -rf \"$checkout\"; unset AEGISNODE_GITHUB_TOKEN; }",
		"trap cleanup_git_credentials EXIT",
		"printf '%s\\n' '#!/bin/sh' 'case \"$1\" in *Username*) printf \"%s\\\\n\" x-access-token;; *) printf \"%s\\\\n\" \"$AEGISNODE_GITHUB_TOKEN\";; esac' >\"$askpass\"",
		"chmod 0700 \"$askpass\"",
		"export GIT_ASKPASS=\"$askpass\" GIT_TERMINAL_PROMPT=0",
		verifySnapshot,
		"if [ -d "+shellQuote(observabilityRepositoryDirectory+"/.git")+" ]; then",
		"  [ -z \"$(git -C "+shellQuote(observabilityRepositoryDirectory)+" status --porcelain)\" ] || { echo 'remote configuration checkout is dirty' >&2; exit 1; }",
		"  [ \"$(git -C "+shellQuote(observabilityRepositoryDirectory)+" remote get-url origin)\" = "+shellQuote(config.RepositoryOrigin)+" ] || { echo 'remote configuration origin differs' >&2; exit 1; }",
		"  git -C "+shellQuote(observabilityRepositoryDirectory)+" fetch --prune origin",
		"else",
		"  git clone --no-checkout -- "+shellQuote(config.RepositoryOrigin)+" \"$checkout/repository\"",
		"  rm -rf "+shellQuote(observabilityRepositoryDirectory),
		"  mv \"$checkout/repository\" "+shellQuote(observabilityRepositoryDirectory),
		"fi",
		"git -C "+shellQuote(observabilityRepositoryDirectory)+" cat-file -e "+shellQuote(config.RepositoryCommit+"^{commit}"),
		"git -C "+shellQuote(observabilityRepositoryDirectory)+" checkout --detach --force "+shellQuote(config.RepositoryCommit),
		"actual_hash=\"$(sha256sum "+shellQuote(observabilityRepositoryDirectory+"/"+observabilityComposeRepositoryPath)+" | awk '{print $1}')\"",
		"[ \"$actual_hash\" = "+shellQuote(config.RepositorySHA256)+" ] || { echo 'checked-out configuration hash differs' >&2; exit 1; }",
		remoteWriteFileCommand(deploymentPath, "commit="+config.RepositoryCommit+"\nsha256="+config.RepositorySHA256+"\n", "root", group, 0640),
	)
	return Task{
		Name:  "Check out committed GitHub configuration",
		Apply: script,
		Stdin: config.GitHubToken + "\n",
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
	labels := func(key, name, host string, port int, healthPath string) []string {
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
			"      - " + prefix + ".targets[0].healthcheck.enabled=true",
			"      - " + prefix + ".targets[0].healthcheck.hostname=" + host,
			fmt.Sprintf("      - %s.targets[0].healthcheck.port=%d", prefix, port),
			"      - " + prefix + ".targets[0].healthcheck.scheme=http",
			"      - " + prefix + ".targets[0].healthcheck.path=" + healthPath,
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
		"      USER_PASSWORD: ${BESZEL_ADMIN_PASSWORD}",
		"      TRUSTED_AUTH_HEADER: \"Remote-Email\"",
		"      APP_URL: " + yamlDoubleQuote("https://beszel."+config.BaseDomain),
		"      DISABLE_PASSWORD_AUTH: \"true\"",
		"    expose:",
		"      - \"8090\"",
		"    volumes:",
		"      - " + observabilityStackDirectory + "/beszel_data:/beszel_data",
		"    networks:",
		"      - " + aegisPublicNetwork,
		"    labels:",
	}
	lines = append(lines, labels("aegisnode-beszel", "Beszel", "beszel", 8090, "/")...)
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
		"      TOKEN: ${BESZEL_SYSTEM_TOKEN}",
		"      KEY_FILE: \"/keys/id_ed25519.pub\"",
		"      DOCKER_HOST: \"tcp://socket-proxy:2375\"",
		"    volumes:",
		"      - "+observabilityStackDirectory+"/agent_keys:/keys:ro",
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
	lines = append(lines, labels("aegisnode-dozzle", "Dozzle", "dozzle", 8080, "/healthcheck")...)
	lines = append(lines,
		"",
		"networks:",
		"  "+aegisPublicNetwork+":",
		"    external: true",
		"",
	)
	return strings.Join(lines, "\n")
}

func observabilityResourceReconcileCommand(config observabilityConfig, composeCommand string) string {
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
		composeCommand+" down --remove-orphans >/dev/null 2>&1 || true",
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
 else:
  print(canonical[0]["resourceId"])
sys.exit(0 if ok else 1)`
	verifyTargets := `import json,sys
targets=json.load(sys.stdin)["data"]["targets"]
ok=len(targets)==1 and targets[0].get("hcEnabled") is True
sys.exit(0 if ok else 1)`
	return commandScript(
		`api='http://127.0.0.1:3000/api/v1'`,
		`cookie_file="$(mktemp)"`,
		`trap 'rm -f "$cookie_file"' EXIT`,
		`curl -fsS -c "$cookie_file" -X POST "$api/auth/login" -H 'Content-Type: application/json' -H 'X-CSRF-Token: x-csrf-protection' --data `+shellQuote(loginPayload)+` >/dev/null`,
		`for attempt in $(seq 1 30); do`,
		`  resources="$(curl -fsS -b "$cookie_file" "$api/org/aegisnode/resources?pageSize=100")"`,
		`  if resource_ids="$(printf '%s' "$resources" | python3 -c `+shellQuote(verifyResources)+` `+shellQuote(specs)+`)"; then`,
		`    healthchecks_enabled=1`,
		`    for resource_id in $resource_ids; do`,
		`      targets="$(curl -fsS -b "$cookie_file" "$api/resource/$resource_id/targets")"`,
		`      if ! printf '%s' "$targets" | python3 -c `+shellQuote(verifyTargets)+`; then healthchecks_enabled=0; fi`,
		`    done`,
		`    if [ "$healthchecks_enabled" = "1" ]; then exit 0; fi`,
		`  fi`,
		`  sleep 2`,
		`done`,
		`echo 'Pangolin did not converge to exactly one managed Beszel and Dozzle resource with health checks enabled.' >&2`,
		`exit 1`,
	)
}

func observabilityResourceSpecs(baseDomain string) string {
	return fmt.Sprintf(
		`[{"name":"Beszel","domain":%s,"nice_id":"aegisnode-beszel"},{"name":"Dozzle","domain":%s,"nice_id":"aegisnode-dozzle"}]`,
		jsonString("beszel."+baseDomain), jsonString("dozzle."+baseDomain),
	)
}
