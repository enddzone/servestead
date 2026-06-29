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
const applicationDataDirectory = "/data"
const stackEnvironmentDirectory = "/etc/aegisnode/stacks"

const (
	beszelImage      = "docker.io/henrygd/beszel:0.18.7"
	beszelAgentImage = "docker.io/henrygd/beszel-agent:0.18.7"
	dozzleImage      = "docker.io/amir20/dozzle:v10.6.6"
	dockhandImage    = "docker.io/fnsys/dockhand:latest"
)

const dockhandLocalAPI = "http://127.0.0.1:3003/api"
const dockhandEnvironmentName = "local-vps"
const dockhandEnvironmentHost = "dockhand-socket-proxy"
const dockhandEnvironmentPort = 2375

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
	RepositoryBranch  string
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
			"install -d -m 0750 -o root -g "+shellQuote(group)+" "+shellQuote(observabilityStackDirectory+"/dockhand_data"),
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
			"for service in beszel beszel-agent dozzle dockhand dockhand-socket-proxy; do",
			"  printf '%s\\n' \"$running\" | grep -Fx \"$service\" >/dev/null",
			"done",
			composeCommand+" ps",
		)},
		dockhandEnvironmentReconcileTask(config),
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
	environmentPath := stackEnvironmentDirectory + "/" + stack.Name + ".env"
	composeCommand := "docker compose --env-file " + shellQuote(environmentPath) +
		" -p " + shellQuote("aegisnode-"+stack.Name) +
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
		stackDataDirectoryTask(stack),
		stackEnvironmentTask(stack, environmentPath),
		Task{Name: "Generate " + stack.Name + " deployment override", Apply: commandScript(
			"install -d -m 0750 -o root -g "+shellQuote(group)+" /opt/aegisnode/generated",
			remoteWriteFileCommand(overridePath, stack.Override, "root", group, 0640),
		)},
	)
	validationCommands := []string{composeCommand + " config --quiet"}
	validationName := "Validate " + stack.Name + " Compose"
	if len(stack.Resources) > 0 {
		validationName += " and Pangolin labels"
		validationCommands = append(validationCommands, composeCommand+" config --format json | python3 -c "+shellQuote(`import json,sys
data=json.load(sys.stdin)
labels={}
for service in data["services"].values():
 labels.update(service.get("labels",{}))
required=[".full-domain",".targets[0].hostname",".targets[0].port"]
ok=all(any(key.endswith(suffix) for key in labels) for suffix in required)
sys.exit(0 if ok else 1)`))
	}
	tasks = append(tasks, Task{Name: validationName, Apply: commandScript(validationCommands...)})
	if len(stack.Resources) > 0 {
		tasks = append(tasks, Task{Name: "Start " + stack.Name + " stack and reconcile Pangolin", Apply: commandScript(
			"docker stop aegis-newt >/dev/null",
			"start_result=0",
			composeCommand+" pull && "+composeCommand+" up -d --remove-orphans || start_result=$?",
			"docker start aegis-newt >/dev/null",
			"exit \"$start_result\"",
		)})
		tasks = append(tasks, Task{Name: "Verify " + stack.Name + " Pangolin public resources", Apply: stackResourceVerifyCommand(config, stack)})
	} else {
		tasks = append(tasks, Task{Name: "Start " + stack.Name + " stack", Apply: commandScript(
			composeCommand+" pull",
			composeCommand+" up -d --remove-orphans",
		)})
	}
	tasks = append(tasks, Task{Name: "Verify " + stack.Name + " stack", Apply: commandScript(
		"expected=\"$("+composeCommand+" config --services)\"",
		"running=\"$("+composeCommand+" ps --services --status running)\"",
		"for service in $expected; do printf '%s\\n' \"$running\" | grep -Fx \"$service\" >/dev/null; done",
		composeCommand+" ps",
	)})
	if shouldReconcileDockhandGitStacks(config) {
		tasks = append(tasks, dockhandGitStackReconcileTask(config, stack))
	}
	return tasks
}

func stackDataDirectoryTask(stack configuredStack) Task {
	dataDirectory := applicationDataDirectory + "/" + stack.Name
	return Task{Name: "Prepare " + stack.Name + " data directory", Apply: commandScript(
		"install -d -m 0755 -o root -g root "+shellQuote(applicationDataDirectory),
		"if [ ! -e "+shellQuote(dataDirectory)+" ]; then",
		"  install -d -m 0750 -o 1000 -g 1000 "+shellQuote(dataDirectory),
		"fi",
	)}
}

func stackEnvironmentTask(stack configuredStack, path string) Task {
	script := commandScript(
		"install -d -m 0700 -o root -g root "+shellQuote(stackEnvironmentDirectory),
		"temporary="+shellQuote(path+".aegisnode.tmp"),
		"cat > \"$temporary\"",
		"chown root:root \"$temporary\"",
		"chmod 0600 \"$temporary\"",
		"mv \"$temporary\" "+shellQuote(path),
	)
	if stack.Environment == "" {
		return Task{Name: "Write " + stack.Name + " environment", Apply: remoteWriteFileCommand(path, "", "root", "root", 0600)}
	}
	return Task{Name: "Write " + stack.Name + " environment", Apply: script, Stdin: stack.Environment}
}

func stackRepositoryReconcileTasks(config observabilityConfig, group string) []Task {
	tasks := []Task{}
	if config.RepositoryOrigin != "" {
		tasks = append(tasks, observabilityRepositoryTask(config, group))
	}
	tasks = append(tasks, removedStackCleanupTask(config))
	if shouldReconcileDockhandGitStacks(config) {
		tasks = append(tasks, removedDockhandGitStackCleanupTask(config))
	}
	for _, stack := range config.Stacks {
		tasks = append(tasks, configuredStackTasks(config, stack, group)...)
	}
	return tasks
}

func shouldReconcileDockhandGitStacks(config observabilityConfig) bool {
	return config.RepositoryOrigin != "" && config.RepositoryBranch != ""
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
	commands := []string{
		"desired=" + shellQuote(desired),
		"removed=''",
		"for candidate in " + shellQuote(observabilityRepositoryDirectory) + ".stack-*.deployment /opt/aegisnode/generated/*.pangolin.yaml; do",
		"  [ -e \"$candidate\" ] || continue",
		"  case \"$candidate\" in",
		"    " + shellQuote(observabilityRepositoryDirectory) + ".stack-*.deployment) name=\"${candidate#" + observabilityRepositoryDirectory + ".stack-}\"; name=\"${name%.deployment}\" ;;",
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
		"  rm -rf -- " + shellQuote(observabilityRepositoryDirectory) + "/stacks/\"$name\"",
		"  rm -f -- /opt/aegisnode/generated/\"$name\".pangolin.yaml " + shellQuote(observabilityRepositoryDirectory) + ".stack-\"$name\".deployment",
		"  rm -f -- " + shellQuote(stackEnvironmentDirectory) + "/\"$name\".env",
		"done",
		`api='http://127.0.0.1:3000/api/v1'`,
	}
	commands = append(commands, pangolinLoginCommand(loginPayload)...)
	commands = append(commands,
		`resources="$(curl -fsS -b "$cookie_file" "$api/org/aegisnode/resources?pageSize=100")"`,
		`delete_ids="$(printf '%s' "$resources" | python3 -c `+shellQuote(selectResources)+` $removed)"`,
		`for resource_id in $delete_ids; do`,
		`  curl -fsS -b "$cookie_file" -X DELETE "$api/resource/$resource_id" -H 'X-CSRF-Token: x-csrf-protection' >/dev/null`,
		`done`,
		"docker start aegis-newt >/dev/null",
		`rm -f "$cookie_file"`,
		"trap - EXIT",
	)
	script := commandScript(commands...)
	return Task{Name: "Remove stacks deleted from committed configuration", Apply: script}
}

func removedDockhandGitStackCleanupTask(config observabilityConfig) Task {
	names := make([]string, 0, len(config.Stacks))
	for _, stack := range config.Stacks {
		names = append(names, dockhandGitStackName(stack.Name))
	}
	sort.Strings(names)
	desired := `[]`
	if len(names) > 0 {
		desired = `["` + strings.Join(names, `","`) + `"]`
	}
	selectDeletes := `import json,sys
desired=set(json.loads(sys.argv[1]))
for stack in json.load(sys.stdin):
 name=stack.get("stackName","")
 if name.startswith("aegisnode-") and name not in desired:
  print(stack["id"])`
	commands := dockhandEnvironmentCommandPrelude("")
	commands = append(commands,
		`if ! dockhand_available; then echo 'Dockhand API is unavailable; skipping stale Dockhand Git stack cleanup.'; exit 0; fi`,
		`dockhand_environment_id="$(dockhand_ensure_environment)" || { echo 'Dockhand environment could not be prepared; skipping stale Dockhand Git stack cleanup.' >&2; exit 0; }`,
		`stacks="$(dockhand_request GET "$dockhand_api/git/stacks?env=$dockhand_environment_id")" || { echo 'Dockhand Git stack listing failed; skipping cleanup.' >&2; exit 0; }`,
		`delete_ids="$(printf '%s' "$stacks" | python3 -c `+shellQuote(selectDeletes)+` `+shellQuote(desired)+`)"`,
		`for stack_id in $delete_ids; do`,
		`  dockhand_request DELETE "$dockhand_api/git/stacks/$stack_id" >/dev/null || echo "Dockhand Git stack $stack_id could not be deleted; continuing." >&2`,
		`done`,
	)
	return Task{Name: "Remove Dockhand Git stacks deleted from committed configuration", Apply: commandScript(commands...)}
}

func dockhandGitStackReconcileTask(config observabilityConfig, stack configuredStack) Task {
	return Task{Name: "Reconcile " + stack.Name + " Dockhand Git stack", Apply: dockhandGitStackReconcileCommand(config, stack)}
}

func dockhandGitStackReconcileCommand(config observabilityConfig, stack configuredStack) string {
	stackName := dockhandGitStackName(stack.Name)
	composePath := "stacks/" + stack.Name + "/compose.yaml"
	contextDir := "stacks/" + stack.Name
	commitPrefix := config.RepositoryCommit
	if len(commitPrefix) > 7 {
		commitPrefix = commitPrefix[:7]
	}
	payloadTemplate := fmt.Sprintf(
		`{"stackName":%s,"repoName":%s,"environmentId":0,"url":%s,"branch":%s,"composePath":%s,"contextDir":%s,"envFilePath":null,"autoUpdate":false,"webhookEnabled":false,"deployNow":false,"buildOnDeploy":false,"noBuildCache":false,"repullImages":false,"forceRedeploy":false}`,
		jsonString(stackName),
		jsonString(stackName),
		jsonString(config.RepositoryOrigin),
		jsonString(config.RepositoryBranch),
		jsonString(composePath),
		jsonString(contextDir),
	)
	setEnvironmentID := `import json,sys
payload=json.load(sys.stdin)
payload["environmentId"]=int(sys.argv[1])
print(json.dumps(payload,separators=(",",":")))`
	selectExisting := `import json,sys
name=sys.argv[1]
for stack in json.load(sys.stdin):
 if stack.get("stackName")==name:
  print(json.dumps(stack))
  break`
	validateExisting := `import json,sys
stack=json.load(sys.stdin)
desired=json.loads(sys.argv[1])
ok=(stack.get("environmentId")==desired["environmentId"] and
    stack.get("repoUrl")==desired["url"] and
    stack.get("repoBranch")==desired["branch"] and
    stack.get("composePath")==desired["composePath"] and
    (stack.get("contextDir") or None)==desired["contextDir"] and
    not bool(stack.get("autoUpdate")) and
    not bool(stack.get("webhookEnabled")))
sys.exit(0 if ok else 1)`
	extractID := `import json,sys
print(json.load(sys.stdin)["id"])`
	commitMatches := `import json,sys
stack=json.load(sys.stdin)
expected=sys.argv[1]
actual=str(stack.get("lastCommit") or "")
sys.exit(0 if actual[:7]==expected else 1)`
	extractCommit := `import json,sys
data=json.load(sys.stdin)
print(str(data.get("commit") or ""))`
	commands := dockhandEnvironmentCommandPrelude("")
	commands = append(commands,
		`if ! dockhand_available; then echo `+shellQuote("Dockhand API is unavailable; skipping Dockhand Git stack reconciliation for "+stack.Name+".")+`; exit 0; fi`,
		`dockhand_environment_id="$(dockhand_ensure_environment)" || { echo `+shellQuote("Dockhand environment could not be prepared; skipping "+stack.Name+".")+` >&2; exit 0; }`,
		`dockhand_stack_payload_template=`+shellQuote(payloadTemplate),
		`desired_payload="$(printf '%s' "$dockhand_stack_payload_template" | python3 -c `+shellQuote(setEnvironmentID)+` "$dockhand_environment_id")"`,
		`stacks="$(dockhand_request GET "$dockhand_api/git/stacks?env=$dockhand_environment_id")" || { echo `+shellQuote("Dockhand Git stack listing failed; skipping "+stack.Name+".")+` >&2; exit 0; }`,
		`existing="$(printf '%s' "$stacks" | python3 -c `+shellQuote(selectExisting)+` `+shellQuote(stackName)+`)"`,
		`if [ -n "$existing" ] && ! printf '%s' "$existing" | python3 -c `+shellQuote(validateExisting)+` "$desired_payload"; then`,
		`  stack_id="$(printf '%s' "$existing" | python3 -c `+shellQuote(extractID)+`)"`,
		`  dockhand_request DELETE "$dockhand_api/git/stacks/$stack_id" >/dev/null || { echo `+shellQuote("Dockhand Git stack "+stack.Name+" exists with incompatible settings and could not be recreated; continuing.")+` >&2; exit 0; }`,
		`  existing=''`,
		`fi`,
		`if [ -z "$existing" ]; then`,
		`  existing="$(dockhand_request POST "$dockhand_api/git/stacks" "$desired_payload")" || { echo `+shellQuote("Dockhand Git stack "+stack.Name+" could not be created; continuing.")+` >&2; exit 0; }`,
		`fi`,
		`if printf '%s' "$existing" | python3 -c `+shellQuote(commitMatches)+` `+shellQuote(commitPrefix)+`; then`,
		`  echo `+shellQuote("Dockhand Git stack "+stack.Name+" already matches "+commitPrefix+".")+`; exit 0`,
		`fi`,
		`stack_id="$(printf '%s' "$existing" | python3 -c `+shellQuote(extractID)+`)"`,
		`sync_result="$(dockhand_request POST "$dockhand_api/git/stacks/$stack_id/sync")" || { echo `+shellQuote("Dockhand Git stack "+stack.Name+" sync failed; continuing.")+` >&2; exit 0; }`,
		`sync_commit="$(printf '%s' "$sync_result" | python3 -c `+shellQuote(extractCommit)+`)"`,
		`case "$sync_commit" in`,
		`  `+commitPrefix+`*) echo `+shellQuote("Dockhand Git stack "+stack.Name+" synced to "+commitPrefix+".")+` ;;`,
		`  *) echo `+shellQuote("Dockhand Git stack "+stack.Name+" synced, but did not report the committed AegisNode revision "+commitPrefix+".")+` >&2 ;;`,
		`esac`,
	)
	return commandScript(commands...)
}

func dockhandEnvironmentReconcileTask(config observabilityConfig) Task {
	checkSuccess := `import json,sys
data=json.load(sys.stdin)
sys.exit(0 if data.get("success") is True else 1)`
	checkContainers := `import json,sys
containers=json.load(sys.stdin)
sys.exit(0 if isinstance(containers,list) and len(containers)>0 else 1)`
	commands := dockhandEnvironmentCommandPrelude(config.Host)
	commands = append(commands,
		`set +e; dockhand_wait_available; dockhand_status=$?; set -e`,
		`case "$dockhand_status" in`,
		`  0) ;;`,
		`  2) echo 'Dockhand authentication is enabled and AegisNode has no Dockhand API session; skipping Dockhand environment setup.' >&2; exit 0 ;;`,
		`  *) echo 'Dockhand API did not become available after deployment.' >&2; exit 1 ;;`,
		`esac`,
		`dockhand_environment_id="$(dockhand_ensure_environment)" || { echo 'Dockhand local Docker environment could not be created or updated.' >&2; exit 1; }`,
		`test_result="$(dockhand_request POST "$dockhand_api/environments/$dockhand_environment_id/test")"`,
		`if ! printf '%s' "$test_result" | python3 -c `+shellQuote(checkSuccess)+`; then`,
		`  echo 'Dockhand local Docker environment did not pass its connection test.' >&2`,
		`  printf '%s\n' "$test_result" >&2`,
		`  exit 1`,
		`fi`,
		`containers="$(dockhand_request GET "$dockhand_api/containers?env=$dockhand_environment_id")"`,
		`if ! printf '%s' "$containers" | python3 -c `+shellQuote(checkContainers)+`; then`,
		`  echo 'Dockhand local Docker environment is connected but returned no visible containers.' >&2`,
		`  exit 1`,
		`fi`,
		`echo "Dockhand local Docker environment $dockhand_environment_id is connected and lists containers."`,
	)
	return Task{Name: "Configure Dockhand local environment", Apply: commandScript(commands...)}
}

func dockhandEnvironmentCommandPrelude(publicIP string) []string {
	selectEnvironment := `import json,sys
name=sys.argv[1]
for environment in json.load(sys.stdin):
 if environment.get("name")==name:
  print(json.dumps(environment))
  break`
	environmentMatches := `import json,sys
environment=json.load(sys.stdin)
desired=json.loads(sys.argv[1])
ok=(environment.get("name")==desired["name"] and
    environment.get("connectionType")==desired["connectionType"] and
    (environment.get("host") or "")==desired["host"] and
    int(environment.get("port") or 0)==desired["port"] and
    (environment.get("protocol") or "")==desired["protocol"] and
    bool(environment.get("collectActivity"))==desired["collectActivity"] and
    bool(environment.get("collectMetrics"))==desired["collectMetrics"] and
    bool(environment.get("highlightChanges"))==desired["highlightChanges"])
sys.exit(0 if ok else 1)`
	extractID := `import json,sys
print(json.load(sys.stdin)["id"])`
	commands := dockhandCommandPrelude()
	commands = append(commands,
		`dockhand_environment_payload=`+shellQuote(dockhandEnvironmentPayload(publicIP)),
		`dockhand_ensure_environment() {`,
		`  environments="$(dockhand_request GET "$dockhand_api/environments")" || return 1`,
		`  existing="$(printf '%s' "$environments" | python3 -c `+shellQuote(selectEnvironment)+` `+shellQuote(dockhandEnvironmentName)+`)" || return 1`,
		`  if [ -n "$existing" ]; then`,
		`    environment_id="$(printf '%s' "$existing" | python3 -c `+shellQuote(extractID)+`)" || return 1`,
		`    if ! printf '%s' "$existing" | python3 -c `+shellQuote(environmentMatches)+` "$dockhand_environment_payload"; then`,
		`      existing="$(dockhand_request PUT "$dockhand_api/environments/$environment_id" "$dockhand_environment_payload")" || return 1`,
		`    fi`,
		`  else`,
		`    existing="$(dockhand_request POST "$dockhand_api/environments" "$dockhand_environment_payload")" || return 1`,
		`  fi`,
		`  environment_id="$(printf '%s' "$existing" | python3 -c `+shellQuote(extractID)+`)" || return 1`,
		`  printf '%s\n' "$environment_id"`,
		`}`,
	)
	return commands
}

func dockhandEnvironmentPayload(publicIP string) string {
	publicIPValue := "null"
	if strings.TrimSpace(publicIP) != "" {
		publicIPValue = jsonString(strings.TrimSpace(publicIP))
	}
	return fmt.Sprintf(
		`{"name":%s,"connectionType":"direct","host":%s,"port":%d,"protocol":"http","tlsSkipVerify":false,"icon":"server","collectActivity":true,"collectMetrics":true,"highlightChanges":true,"labels":["aegisnode"],"publicIp":%s}`,
		jsonString(dockhandEnvironmentName),
		jsonString(dockhandEnvironmentHost),
		dockhandEnvironmentPort,
		publicIPValue,
	)
}

func dockhandCommandPrelude() []string {
	return []string{
		`dockhand_api=` + shellQuote(dockhandLocalAPI),
		`dockhand_request() {`,
		`  method="$1"`,
		`  url="$2"`,
		`  response_file="$(mktemp)"`,
		`  if [ "$#" -eq 3 ]; then`,
		`    status="$(curl -sS -o "$response_file" -w '%{http_code}' -X "$method" "$url" -H 'Content-Type: application/json' --data "$3")"`,
		`  else`,
		`    status="$(curl -sS -o "$response_file" -w '%{http_code}' -X "$method" "$url")"`,
		`  fi`,
		`  result=$?`,
		`  if [ "$result" -ne 0 ]; then`,
		`    rm -f "$response_file"`,
		`    return "$result"`,
		`  fi`,
		`  case "$status" in`,
		`    2??) cat "$response_file" ;;`,
		`    *)`,
		`      echo "Dockhand API $method $url failed (HTTP $status):" >&2`,
		`      cat "$response_file" >&2`,
		`      echo >&2`,
		`      rm -f "$response_file"`,
		`      return 1`,
		`      ;;`,
		`  esac`,
		`  rm -f "$response_file"`,
		`}`,
		`dockhand_available() {`,
		`  session="$(curl -fsS "$dockhand_api/auth/session" 2>/dev/null)" || return 1`,
		`  if printf '%s' "$session" | grep -Eq '"authEnabled"[[:space:]]*:[[:space:]]*true' && ! printf '%s' "$session" | grep -Eq '"authenticated"[[:space:]]*:[[:space:]]*true'; then`,
		`    echo 'Dockhand authentication is enabled and AegisNode has no Dockhand API session; skipping Dockhand API reconciliation.' >&2`,
		`    return 2`,
		`  fi`,
		`  return 0`,
		`}`,
		`dockhand_wait_available() {`,
		`  for attempt in $(seq 1 30); do`,
		`    status=0`,
		`    dockhand_available || status=$?`,
		`    if [ "$status" -eq 0 ] || [ "$status" -eq 2 ]; then return "$status"; fi`,
		`    sleep 2`,
		`  done`,
		`  return 1`,
		`}`,
	}
}

func dockhandGitStackName(stackName string) string {
	return "aegisnode-" + stackName
}

func stackResourceVerifyCommand(config observabilityConfig, stack configuredStack) string {
	specs := make([]string, 0, len(stack.Resources))
	for _, resource := range stack.Resources {
		specs = append(specs, fmt.Sprintf(
			`{"nice_id":%s,"domain":%s}`,
			jsonString("aegisnode-"+stack.Name+"-"+resource.ID),
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
	commands := []string{
		`api='http://127.0.0.1:3000/api/v1'`,
		`cookie_file="$(mktemp)"`,
		`trap 'rm -f "$cookie_file"' EXIT`,
	}
	commands = append(commands, pangolinLoginCommand(loginPayload)...)
	commands = append(commands,
		`for attempt in $(seq 1 30); do`,
		`  resources="$(curl -fsS -b "$cookie_file" "$api/org/aegisnode/resources?pageSize=100")"`,
		`  if printf '%s' "$resources" | python3 -c `+shellQuote(verify)+` `+shellQuote(specification)+`; then exit 0; fi`,
		`  sleep 2`,
		`done`,
		`echo `+shellQuote("Pangolin did not create the expected public resources for stack "+stack.Name)+` >&2`,
		`exit 1`,
	)
	return commandScript(commands...)
}

func pangolinLoginCommand(loginPayload string) []string {
	return []string{
		`login_response="$(mktemp)"`,
		`set +e`,
		`login_status="$(curl -sS -o "$login_response" -w '%{http_code}' -c "$cookie_file" -X POST "$api/auth/login" -H 'Content-Type: application/json' -H 'X-CSRF-Token: x-csrf-protection' --data ` + shellQuote(loginPayload) + `)"`,
		`login_result=$?`,
		`set -e`,
		`if [ "$login_result" -ne 0 ]; then`,
		`  echo 'Pangolin administrator login failed before receiving a response.' >&2`,
		`  if [ -s "$login_response" ]; then cat "$login_response" >&2; echo >&2; fi`,
		`  rm -f "$login_response"`,
		`  exit "$login_result"`,
		`fi`,
		`case "$login_status" in`,
		`  2??) rm -f "$login_response" ;;`,
		`  401)`,
		`    echo 'Pangolin rejected the saved administrator credentials. Run the same setup action once with PANGOLIN_ADMIN_PASSWORD set to the current Pangolin admin password, or enter it in Advanced setup.' >&2`,
		`    if [ -s "$login_response" ]; then cat "$login_response" >&2; echo >&2; fi`,
		`    rm -f "$login_response"`,
		`    exit 1`,
		`    ;;`,
		`  *)`,
		`    echo "Pangolin administrator login failed (HTTP $login_status)." >&2`,
		`    if [ -s "$login_response" ]; then cat "$login_response" >&2; echo >&2; fi`,
		`    rm -f "$login_response"`,
		`    exit 1`,
		`    ;;`,
		`esac`,
	}
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
		"  dockhand:",
		"    image: "+dockhandImage,
		"    container_name: dockhand",
		"    restart: unless-stopped",
		"    depends_on:",
		"      - dockhand-socket-proxy",
		"    security_opt:",
		"      - no-new-privileges:true",
		"    environment:",
		"      DOCKER_HOST: \"tcp://dockhand-socket-proxy:2375\"",
		"      HOST_DATA_DIR: "+yamlDoubleQuote(observabilityStackDirectory+"/dockhand_data"),
		"      COOKIE_SECURE: \"true\"",
		"    expose:",
		"      - \"3000\"",
		"    ports:",
		"      - \"127.0.0.1:3003:3000\"",
		"    volumes:",
		"      - "+observabilityStackDirectory+"/dockhand_data:/app/data",
		"    networks:",
		"      - "+aegisPublicNetwork,
		"      - aegis-dockhand",
		"    labels:",
		"      - dockhand.update=false",
		"      - dockhand.notify=false",
	)
	lines = append(lines, labels("aegisnode-dockhand", "Dockhand", "dockhand", 3000, "/api/auth/session")...)
	lines = append(lines,
		"",
		"  dockhand-socket-proxy:",
		"    image: "+socketProxyImage,
		"    container_name: aegis-dockhand-socket-proxy",
		"    restart: unless-stopped",
		"    security_opt:",
		"      - no-new-privileges:true",
		"    environment:",
		"      CONTAINERS: \"1\"",
		"      EVENTS: \"1\"",
		"      EXEC: \"1\"",
		"      IMAGES: \"1\"",
		"      INFO: \"1\"",
		"      NETWORKS: \"1\"",
		"      PING: \"1\"",
		"      VERSION: \"1\"",
		"      VOLUMES: \"1\"",
		"      POST: \"1\"",
		"    volumes:",
		"      - /var/run/docker.sock:/var/run/docker.sock:ro",
		"    networks:",
		"      - aegis-dockhand",
		"    labels:",
		"      - dockhand.hidden=true",
		"      - dockhand.update=false",
		"      - dockhand.notify=false",
		"",
		"networks:",
		"  "+aegisPublicNetwork+":",
		"    external: true",
		"  aegis-dockhand:",
		"    internal: true",
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
	commands := []string{
		"docker stop aegis-newt >/dev/null",
		composeCommand + " down --remove-orphans >/dev/null 2>&1 || true",
		"sleep 2",
		`api='http://127.0.0.1:3000/api/v1'`,
		`cookie_file="$(mktemp)"`,
		`reconciliation_complete=0`,
		`cleanup_reconciliation() {`,
		`  rm -f "$cookie_file"`,
		`  if [ "$reconciliation_complete" != "1" ]; then docker start aegis-newt >/dev/null 2>&1 || true; fi`,
		`}`,
		`trap cleanup_reconciliation EXIT`,
	}
	commands = append(commands, pangolinLoginCommand(loginPayload)...)
	commands = append(commands,
		`resources="$(curl -fsS -b "$cookie_file" "$api/org/aegisnode/resources?pageSize=100")"`,
		`delete_ids="$(printf '%s' "$resources" | python3 -c `+shellQuote(selectDeletes)+` `+shellQuote(specs)+`)"`,
		`for resource_id in $delete_ids; do`,
		`  curl -fsS -b "$cookie_file" -X DELETE "$api/resource/$resource_id" -H 'X-CSRF-Token: x-csrf-protection' >/dev/null`,
		`done`,
		`reconciliation_complete=1`,
	)
	return commandScript(commands...)
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
	commands := []string{
		`api='http://127.0.0.1:3000/api/v1'`,
		`cookie_file="$(mktemp)"`,
		`trap 'rm -f "$cookie_file"' EXIT`,
	}
	commands = append(commands, pangolinLoginCommand(loginPayload)...)
	commands = append(commands,
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
		`echo 'Pangolin did not converge to exactly one managed Beszel, Dozzle, and Dockhand resource with health checks enabled.' >&2`,
		`exit 1`,
	)
	return commandScript(commands...)
}

func observabilityResourceSpecs(baseDomain string) string {
	return fmt.Sprintf(
		`[{"name":"Beszel","domain":%s,"nice_id":"aegisnode-beszel"},{"name":"Dozzle","domain":%s,"nice_id":"aegisnode-dozzle"},{"name":"Dockhand","domain":%s,"nice_id":"aegisnode-dockhand"}]`,
		jsonString("beszel."+baseDomain), jsonString("dozzle."+baseDomain), jsonString("dockhand."+baseDomain),
	)
}
