package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"servestead/backend/resources"
)

const observabilityStackDirectory = "/opt/servestead/stacks/observability"
const observabilityRepositoryDirectory = "/opt/servestead/repository"
const observabilityEnvironmentPath = "/etc/servestead/observability.env"
const applicationDataDirectory = "/data"
const stackEnvironmentDirectory = "/etc/servestead/stacks"
const dockerComposeYAMLPath = "/docker-compose.yml"
const composeConfigQuietSuffix = " config --quiet"
const restartNewtCommand = "docker start aegis-newt >/dev/null"
const servesteadProjectPrefix = "servestead-"
const dockhandGitStackPrefix = "Dockhand Git stack "
const shellThenSuffix = "; then"
const stackTaskNameSuffix = " stack"
const observabilityBeszelID = servesteadProjectPrefix + "beszel"
const observabilityDockhandID = servesteadProjectPrefix + "dockhand"
const observabilityDozzleID = servesteadProjectPrefix + "dozzle"

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
	ProfileID         string
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
	return runTasksWithReporter(ctx, client, config.SSHUser, taskRunOptions{runID: runID, stage: "observability", tasks: observabilityTasks(config), progress: progress, reporter: reporter})
}

func observabilityTasks(config observabilityConfig) []Task {
	composePath := observabilityStackDirectory + dockerComposeYAMLPath
	composeCommand := "docker compose -f " + shellQuote(composePath)
	declarative := config.RepositoryCommit != "" && config.RepositoryCompose != ""
	if declarative {
		composePath = observabilityRepositoryDirectory + "/" + observabilityComposeRepositoryPath
		composeCommand = "docker compose --env-file " + shellQuote(observabilityEnvironmentPath) + " -p observability -f " + shellQuote(composePath)
	}
	group := firstNonEmpty(config.SSHUser, "root")
	tasks := []Task{
		{Name: "Prepare observability directories", Apply: commandScript(
			remoteInstallDirectoryCommand("/opt/servestead/stacks", "root", group, 0750),
			remoteInstallDirectoryCommand(observabilityStackDirectory, "root", group, 0750),
			remoteInstallDirectoryCommand(observabilityStackDirectory+"/beszel_data", "root", group, 0750),
			remoteInstallDirectoryCommand(observabilityStackDirectory+"/agent_keys", "root", group, 0750),
			remoteInstallDirectoryCommand(observabilityStackDirectory+"/dockhand_data", "root", group, 0750),
			remoteInstallDirectoryCommand("/etc/servestead", "root", group, 0750),
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
				"[ ! -f "+shellQuote(observabilityStackDirectory+dockerComposeYAMLPath)+" ] || docker compose -f "+shellQuote(observabilityStackDirectory+dockerComposeYAMLPath)+composeConfigQuietSuffix,
				composeCommand+composeConfigQuietSuffix,
			)},
		)
	} else {
		tasks = append(tasks,
			Task{Name: "Write observability compose file", Apply: remoteWriteFileCommand(composePath, observabilityComposeFile(config), "root", group, 0600)},
			Task{Name: "Validate observability compose file", Apply: commandScript(composeCommand + composeConfigQuietSuffix)},
		)
	}
	tasks = append(tasks,
		Task{Name: "Reconcile Pangolin observability resources", Apply: observabilityResourceReconcileCommand(config, composeCommand)},
		Task{Name: "Start observability" + stackTaskNameSuffix, Apply: commandScript(
			"start_result=0",
			composeCommand+" pull && "+composeCommand+" up -d --remove-orphans || start_result=$?",
			restartNewtCommand,
			"exit \"$start_result\"",
		)},
		Task{Name: "Verify Pangolin observability resources", Apply: observabilityResourceVerifyCommand(config)},
		Task{Name: "Verify observability" + stackTaskNameSuffix, Apply: commandScript(
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
			"rm -f " + shellQuote(observabilityStackDirectory+dockerComposeYAMLPath),
		)})
	}
	return tasks
}

func configuredStackTasks(config observabilityConfig, stack configuredStack, group string) []Task {
	composePath := observabilityRepositoryDirectory + "/stacks/" + stack.Name + "/compose.yaml"
	overridePath := "/opt/servestead/generated/" + stack.Name + ".pangolin.yaml"
	deploymentPath := observabilityRepositoryDirectory + ".stack-" + stack.Name + ".deployment"
	composeCommand := "docker compose -p " + shellQuote(servesteadProjectPrefix+stack.Name) +
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
			"if test -f " + shellQuote(deploymentPath) + shellThenSuffix,
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
		tasks = append(tasks, Task{Name: "Deploy committed " + stack.Name + stackTaskNameSuffix, Apply: commandScript(deployCommands...)})
	}
	tasks = append(tasks,
		stackDataDirectoryTask(stack),
		Task{Name: "Generate " + stack.Name + " deployment override", Apply: commandScript(
			remoteInstallDirectoryCommand("/opt/servestead/generated", "root", group, 0750),
			remoteWriteFileCommand(overridePath, stack.Override, "root", group, 0640),
		)},
	)
	validationCommands := []string{composeCommand + composeConfigQuietSuffix}
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
	tasks = append(tasks, stackComposeTask(validationName, validationCommands, stack))
	if len(stack.Resources) > 0 {
		tasks = append(tasks, stackComposeTask("Start "+stack.Name+stackTaskNameSuffix+" and reconcile Pangolin", []string{
			"docker stop aegis-newt >/dev/null",
			"start_result=0",
			composeCommand + " pull && " + composeCommand + " up -d --remove-orphans || start_result=$?",
			restartNewtCommand,
			"exit \"$start_result\"",
		}, stack))
		tasks = append(tasks, Task{Name: "Verify " + stack.Name + " Pangolin public resources", Apply: stackResourceVerifyCommand(config, stack)})
	} else {
		tasks = append(tasks, stackComposeTask("Start "+stack.Name+stackTaskNameSuffix, []string{
			composeCommand + " pull",
			composeCommand + " up -d --remove-orphans",
		}, stack))
	}
	tasks = append(tasks, stackComposeTask("Verify "+stack.Name+stackTaskNameSuffix, []string{
		"expected=\"$(" + composeCommand + " config --services)\"",
		"running=\"$(" + composeCommand + " ps --services --status running)\"",
		"for service in $expected; do printf '%s\\n' \"$running\" | grep -Fx \"$service\" >/dev/null; done",
		composeCommand + " ps",
	}, stack))
	if shouldReconcileDockhandGitStacks(config) {
		tasks = append(tasks, dockhandGitStackReconcileTask(config, stack))
		if len(stack.SecretValues) > 0 {
			tasks = append(tasks, dockhandStackSecretReconcileTask(stack))
		}
	}
	return tasks
}

func stackComposeTask(name string, commands []string, stack configuredStack) Task {
	taskCommands := append(stackSecretEnvironmentPrelude(stack.SecretValues), commands...)
	task := Task{Name: name, Apply: commandScript(taskCommands...)}
	if len(stack.SecretValues) > 0 {
		task.Stdin = stackSecretEnvironmentPayload(stack.SecretValues)
	}
	return task
}

func stackSecretEnvironmentPrelude(secrets SecretSet) []string {
	if len(secrets) == 0 {
		return nil
	}
	exportScript := `import json,re,shlex,sys
data=json.load(sys.stdin)
pattern=re.compile(r'^[A-Za-z_][A-Za-z0-9_]*$')
for key in sorted(data):
 if not pattern.match(key):
  raise SystemExit("invalid environment key: "+key)
 print("export "+key+"="+shlex.quote(str(data[key])))`
	return []string{
		`secret_environment_payload="$(cat)"`,
		`eval "$(printf '%s' "$secret_environment_payload" | python3 -c ` + shellQuote(exportScript) + `)"`,
		`unset secret_environment_payload`,
	}
}

func stackSecretEnvironmentPayload(secrets SecretSet) string {
	keys := secretSetKeys(secrets)
	var builder strings.Builder
	builder.WriteByte('{')
	for index, key := range keys {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(jsonString(key))
		builder.WriteByte(':')
		builder.WriteString(jsonString(secrets[key]))
	}
	builder.WriteByte('}')
	return builder.String()
}

func stackDataDirectoryTask(stack configuredStack) Task {
	dataDirectory := applicationDataDirectory + "/" + stack.Name
	return Task{Name: "Prepare " + stack.Name + " data directory", Apply: commandScript(
		"install -d -m 0755 -o root -g root "+shellQuote(applicationDataDirectory),
		"if ! test -e "+shellQuote(dataDirectory)+shellThenSuffix,
		"  install -d -m 0750 -o 1000 -g 1000 "+shellQuote(dataDirectory),
		"fi",
	)}
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
	loginPayload := pangolinLoginJSON(config.AdminEmail, config.PangolinPassword)
	selectResources := `import json,sys
resources=json.load(sys.stdin)["data"]["resources"]
names=sys.argv[1:]
for resource in resources:
 nice_id=resource.get("niceId","")
 if any(nice_id.startswith("` + servesteadProjectPrefix + `"+name+"-") for name in names):
  print(resource["resourceId"])`
	commands := []string{
		"desired=" + shellQuote(desired),
		"removed=''",
		"for candidate in " + shellQuote(observabilityRepositoryDirectory) + ".stack-*.deployment /opt/servestead/generated/*.pangolin.yaml; do",
		"  test -e \"$candidate\" || continue",
		"  case \"$candidate\" in",
		"    " + shellQuote(observabilityRepositoryDirectory) + ".stack-*.deployment) name=\"${candidate#" + observabilityRepositoryDirectory + ".stack-}\"; name=\"${name%.deployment}\" ;;",
		"    /opt/servestead/generated/*.pangolin.yaml) name=\"${candidate#/opt/servestead/generated/}\"; name=\"${name%.pangolin.yaml}\" ;;",
		"  esac",
		"  case \"$name\" in ''|*[!a-z0-9-]*) continue ;; esac",
		"  case \"$desired\" in *\" $name \"*) continue ;; esac",
		"  case \" $removed \" in *\" $name \"*) continue ;; esac",
		"  removed=\"$removed $name\"",
		"done",
		"test -n \"$removed\" || exit 0",
		`cookie_file="$(mktemp)"`,
		`cleanup_removed_stacks() { rm -f "$cookie_file"; ` + restartNewtCommand + ` 2>&1 || true; }`,
		"trap cleanup_removed_stacks EXIT",
		"docker stop aegis-newt >/dev/null 2>&1 || true",
		"for name in $removed; do",
		"  project=\"" + servesteadProjectPrefix + "$name\"",
		"  containers=\"$(docker ps -aq --filter label=com.docker.compose.project=\"$project\")\"",
		"  test -z \"$containers\" || docker rm -f $containers",
		"  networks=\"$(docker network ls -q --filter label=com.docker.compose.project=\"$project\")\"",
		"  test -z \"$networks\" || docker network rm $networks",
		"  rm -rf -- " + shellQuote(observabilityRepositoryDirectory) + "/stacks/\"$name\"",
		"  rm -f -- /opt/servestead/generated/\"$name\".pangolin.yaml " + shellQuote(observabilityRepositoryDirectory) + ".stack-\"$name\".deployment",
		"  rm -f -- " + shellQuote(stackEnvironmentDirectory) + "/\"$name\".env",
		"done",
		`api='http://127.0.0.1:3000/api/v1'`,
	}
	commands = append(commands, pangolinLoginCommand(loginPayload)...)
	commands = append(commands,
		`resources="$(curl -fsS -b "$cookie_file" "$api/org/servestead/resources?pageSize=100")"`,
		`delete_ids="$(printf '%s' "$resources" | python3 -c `+shellQuote(selectResources)+` $removed)"`,
		`for resource_id in $delete_ids; do`,
		`  curl -fsS -b "$cookie_file" -X DELETE "$api/resource/$resource_id" -H 'X-CSRF-Token: x-csrf-protection' >/dev/null`,
		`done`,
		restartNewtCommand,
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
 if name.startswith("` + servesteadProjectPrefix + `") and name not in desired:
  print(stack["id"])`
	commands := dockhandEnvironmentCommandPrelude("")
	commands = append(commands,
		`if ! dockhand_available`+shellThenSuffix+` echo 'Dockhand API is unavailable; skipping stale Dockhand Git stack cleanup.'; exit 0; fi`,
		`dockhand_environment_id="$(dockhand_ensure_environment)" || { echo 'Dockhand environment could not be prepared; skipping stale Dockhand Git stack cleanup.' >&2; exit 0; }`,
		`stacks="$(dockhand_request GET "$dockhand_api/git/stacks?env=$dockhand_environment_id")" || { echo '`+dockhandGitStackPrefix+`listing failed; skipping cleanup.' >&2; exit 0; }`,
		`delete_ids="$(printf '%s' "$stacks" | python3 -c `+shellQuote(selectDeletes)+` `+shellQuote(desired)+`)"`,
		`for stack_id in $delete_ids; do`,
		`  dockhand_request DELETE "$dockhand_api/git/stacks/$stack_id" >/dev/null || echo "`+dockhandGitStackPrefix+`$stack_id could not be deleted; continuing." >&2`,
		`done`,
	)
	return Task{Name: "Remove Dockhand Git stacks deleted from committed configuration", Apply: commandScript(commands...)}
}

func dockhandGitStackReconcileTask(config observabilityConfig, stack configuredStack) Task {
	return Task{Name: "Reconcile " + stack.Name + " Dockhand Git stack", Apply: dockhandGitStackReconcileCommand(config, stack)}
}

func dockhandStackSecretReconcileTask(stack configuredStack) Task {
	return Task{
		Name:  "Reconcile " + stack.Name + " Dockhand secret environment",
		Apply: dockhandStackSecretReconcileCommand(stack),
		Stdin: dockhandStackSecretPayload(stack.SecretValues),
	}
}

func dockhandStackSecretPayload(secrets SecretSet) string {
	keys := secretSetKeys(secrets)
	var builder strings.Builder
	builder.WriteString(`{"variables":[`)
	for index, key := range keys {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(`{"key":`)
		builder.WriteString(jsonString(key))
		builder.WriteString(`,"value":`)
		builder.WriteString(jsonString(secrets[key]))
		builder.WriteString(`,"isSecret":true}`)
	}
	builder.WriteString(`]}`)
	return builder.String()
}

func dockhandStackSecretReconcileCommand(stack configuredStack) string {
	stackName := dockhandGitStackName(stack.Name)
	commands := dockhandEnvironmentCommandPrelude("")
	commands = append(commands,
		`secret_payload="$(cat)"`,
		`if ! dockhand_available; then echo `+shellQuote("Dockhand API is unavailable; cannot reconcile secret environment for "+stack.Name+".")+` >&2; exit 1; fi`,
		`dockhand_environment_id="$(dockhand_ensure_environment)" || { echo `+shellQuote("Dockhand environment could not be prepared; cannot reconcile secret environment for "+stack.Name+".")+` >&2; exit 1; }`,
		`dockhand_stack_name=`+shellQuote(stackName),
		`response_file="$(mktemp)"`,
		`cleanup_secret_reconcile() { rm -f "$response_file"; }`,
		`trap cleanup_secret_reconcile EXIT`,
		`secret_env_url="$dockhand_api/stacks/$dockhand_stack_name/env?env=$dockhand_environment_id"`,
		`status="$(printf '%s' "$secret_payload" | curl -sS -o "$response_file" -w '%{http_code}' -X PUT "$secret_env_url" -H 'Content-Type: application/json' --data-binary @-)"`,
		`curl_result=$?`,
		`if [ "$curl_result" -ne 0 ]; then echo `+shellQuote("Dockhand secret environment request failed for "+stack.Name+".")+` >&2; exit "$curl_result"; fi`,
		`case "$status" in`,
		`  2??) echo `+shellQuote("Dockhand secret environment updated for "+stack.Name+".")+` ;;`,
		`  *) echo `+shellQuote("Dockhand secret environment update failed for "+stack.Name+" (HTTP ")+`"$status"`+shellQuote(").")+` >&2; exit 1 ;;`,
		`esac`,
	)
	return commandScript(commands...)
}

func dockhandGitStackReconcileCommand(config observabilityConfig, stack configuredStack) string {
	stackName := dockhandGitStackName(stack.Name)
	composePath := "stacks/" + stack.Name + "/compose.yaml"
	contextDir := "stacks/" + stack.Name
	commitPrefix := config.RepositoryCommit
	if len(commitPrefix) > 7 {
		commitPrefix = commitPrefix[:7]
	}
	payloadTemplate := mustJSON(struct {
		StackName      string  `json:"stackName"`
		RepoName       string  `json:"repoName"`
		EnvironmentID  int     `json:"environmentId"`
		URL            string  `json:"url"`
		Branch         string  `json:"branch"`
		ComposePath    string  `json:"composePath"`
		ContextDir     string  `json:"contextDir"`
		EnvFilePath    *string `json:"envFilePath"`
		AutoUpdate     bool    `json:"autoUpdate"`
		WebhookEnabled bool    `json:"webhookEnabled"`
		DeployNow      bool    `json:"deployNow"`
		BuildOnDeploy  bool    `json:"buildOnDeploy"`
		NoBuildCache   bool    `json:"noBuildCache"`
		RepullImages   bool    `json:"repullImages"`
		ForceRedeploy  bool    `json:"forceRedeploy"`
	}{
		StackName:   stackName,
		RepoName:    stackName,
		URL:         config.RepositoryOrigin,
		Branch:      config.RepositoryBranch,
		ComposePath: composePath,
		ContextDir:  contextDir,
	})
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
		`if ! dockhand_available`+shellThenSuffix+` echo `+shellQuote("Dockhand API is unavailable; skipping Dockhand Git stack reconciliation for "+stack.Name+".")+`; exit 0; fi`,
		`dockhand_environment_id="$(dockhand_ensure_environment)" || { echo `+shellQuote("Dockhand environment could not be prepared; skipping "+stack.Name+".")+` >&2; exit 0; }`,
		`dockhand_stack_payload_template=`+shellQuote(payloadTemplate),
		`desired_payload="$(printf '%s' "$dockhand_stack_payload_template" | python3 -c `+shellQuote(setEnvironmentID)+` "$dockhand_environment_id")"`,
		`stacks="$(dockhand_request GET "$dockhand_api/git/stacks?env=$dockhand_environment_id")" || { echo `+shellQuote(dockhandGitStackPrefix+"listing failed; skipping "+stack.Name+".")+` >&2; exit 0; }`,
		`existing="$(printf '%s' "$stacks" | python3 -c `+shellQuote(selectExisting)+` `+shellQuote(stackName)+`)"`,
		`if test -n "$existing" && ! printf '%s' "$existing" | python3 -c `+shellQuote(validateExisting)+` "$desired_payload"`+shellThenSuffix,
		`  stack_id="$(printf '%s' "$existing" | python3 -c `+shellQuote(extractID)+`)"`,
		`  dockhand_request DELETE "$dockhand_api/git/stacks/$stack_id" >/dev/null || { echo `+shellQuote(dockhandGitStackPrefix+stack.Name+" exists with incompatible settings and could not be recreated; continuing.")+` >&2; exit 0; }`,
		`  existing=''`,
		`fi`,
		`if test -z "$existing"`+shellThenSuffix,
		`  existing="$(dockhand_request POST "$dockhand_api/git/stacks" "$desired_payload")" || { echo `+shellQuote(dockhandGitStackPrefix+stack.Name+" could not be created; continuing.")+` >&2; exit 0; }`,
		`fi`,
		`if printf '%s' "$existing" | python3 -c `+shellQuote(commitMatches)+` `+shellQuote(commitPrefix)+shellThenSuffix,
		`  echo `+shellQuote(dockhandGitStackPrefix+stack.Name+" already matches "+commitPrefix+".")+`; exit 0`,
		`fi`,
		`stack_id="$(printf '%s' "$existing" | python3 -c `+shellQuote(extractID)+`)"`,
		`sync_result="$(dockhand_request POST "$dockhand_api/git/stacks/$stack_id/sync")" || { echo `+shellQuote(dockhandGitStackPrefix+stack.Name+" sync failed; continuing.")+` >&2; exit 0; }`,
		`sync_commit="$(printf '%s' "$sync_result" | python3 -c `+shellQuote(extractCommit)+`)"`,
		`case "$sync_commit" in`,
		`  `+commitPrefix+`*) echo `+shellQuote(dockhandGitStackPrefix+stack.Name+" synced to "+commitPrefix+".")+` ;;`,
		`  *) echo `+shellQuote(dockhandGitStackPrefix+stack.Name+" synced, but did not report the committed Servestead revision "+commitPrefix+".")+` >&2 ;;`,
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
		`  2) echo 'Dockhand authentication is enabled and Servestead has no Dockhand API session; skipping Dockhand environment setup.' >&2; exit 0 ;;`,
		`  *) echo 'Dockhand API did not become available after deployment.' >&2; exit 1 ;;`,
		`esac`,
		`dockhand_environment_id="$(dockhand_ensure_environment)" || { echo 'Dockhand local Docker environment could not be created or updated.' >&2; exit 1; }`,
		`test_result="$(dockhand_request POST "$dockhand_api/environments/$dockhand_environment_id/test")"`,
		`if ! printf '%s' "$test_result" | python3 -c `+shellQuote(checkSuccess)+shellThenSuffix,
		`  echo 'Dockhand local Docker environment did not pass its connection test.' >&2`,
		`  printf '%s\n' "$test_result" >&2`,
		`  exit 1`,
		`fi`,
		`containers="$(dockhand_request GET "$dockhand_api/containers?env=$dockhand_environment_id")"`,
		`if ! printf '%s' "$containers" | python3 -c `+shellQuote(checkContainers)+shellThenSuffix,
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
		`  if test -n "$existing"`+shellThenSuffix,
		`    environment_id="$(printf '%s' "$existing" | python3 -c `+shellQuote(extractID)+`)" || return 1`,
		`    if ! printf '%s' "$existing" | python3 -c `+shellQuote(environmentMatches)+` "$dockhand_environment_payload"`+shellThenSuffix,
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
	var publicIPValue *string
	if trimmed := strings.TrimSpace(publicIP); trimmed != "" {
		publicIPValue = &trimmed
	}
	return mustJSON(struct {
		Name             string   `json:"name"`
		ConnectionType   string   `json:"connectionType"`
		Host             string   `json:"host"`
		Port             int      `json:"port"`
		Protocol         string   `json:"protocol"`
		TLSSkipVerify    bool     `json:"tlsSkipVerify"`
		Icon             string   `json:"icon"`
		CollectActivity  bool     `json:"collectActivity"`
		CollectMetrics   bool     `json:"collectMetrics"`
		HighlightChanges bool     `json:"highlightChanges"`
		Labels           []string `json:"labels"`
		PublicIP         *string  `json:"publicIp"`
	}{
		Name:             dockhandEnvironmentName,
		ConnectionType:   "direct",
		Host:             dockhandEnvironmentHost,
		Port:             dockhandEnvironmentPort,
		Protocol:         "http",
		Icon:             "server",
		CollectActivity:  true,
		CollectMetrics:   true,
		HighlightChanges: true,
		Labels:           []string{"servestead"},
		PublicIP:         publicIPValue,
	})
}

func dockhandCommandPrelude() []string {
	return []string{
		`dockhand_api=` + shellQuote(dockhandLocalAPI),
		`dockhand_request() {`,
		`  method="$1"`,
		`  url="$2"`,
		`  response_file="$(mktemp)"`,
		`  if test "$#" -eq 3` + shellThenSuffix,
		`    status="$(curl -sS -o "$response_file" -w '%{http_code}' -X "$method" "$url" -H 'Content-Type: application/json' --data "$3")"`,
		`  else`,
		`    status="$(curl -sS -o "$response_file" -w '%{http_code}' -X "$method" "$url")"`,
		`  fi`,
		`  result=$?`,
		`  if test "$result" -ne 0` + shellThenSuffix,
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
		`  if printf '%s' "$session" | grep -Eq '"authEnabled"[[:space:]]*:[[:space:]]*true' && ! printf '%s' "$session" | grep -Eq '"authenticated"[[:space:]]*:[[:space:]]*true'` + shellThenSuffix,
		`    echo 'Dockhand authentication is enabled and Servestead has no Dockhand API session; skipping Dockhand API reconciliation.' >&2`,
		`    return 2`,
		`  fi`,
		`  return 0`,
		`}`,
		`dockhand_wait_available() {`,
		`  for attempt in $(seq 1 30); do`,
		`    status=0`,
		`    dockhand_available || status=$?`,
		`    if test "$status" -eq 0 || test "$status" -eq 2` + shellThenSuffix + ` return "$status"; fi`,
		`    sleep 2`,
		`  done`,
		`  return 1`,
		`}`,
	}
}

func dockhandGitStackName(stackName string) string {
	return servesteadProjectPrefix + stackName
}

func stackResourceVerifyCommand(config observabilityConfig, stack configuredStack) string {
	specs := make([]stackResourceVerificationSpec, 0, len(stack.Resources))
	for _, resource := range stack.Resources {
		specs = append(specs, stackResourceVerificationSpec{
			NiceID: servesteadProjectPrefix + stack.Name + "-" + resource.ID,
			Domain: resource.Subdomain + "." + config.BaseDomain,
		})
	}
	specification := mustJSON(specs)
	loginPayload := pangolinLoginJSON(config.AdminEmail, config.PangolinPassword)
	verify := `import json,sys
resources=json.load(sys.stdin)["data"]["resources"]
specs=json.load(open(sys.argv[1], encoding="utf-8"))
ok=True
for spec in specs:
 matches=[r for r in resources if r.get("niceId")==spec["nice_id"] and r.get("fullDomain")==spec["domain"]]
 if len(matches)!=1:
  ok=False
sys.exit(0 if ok else 1)`
	return commandScript(mustRenderResourceTemplate(resources.ObservabilityStackResourceVerifyScript, struct {
		FailureMessage        string
		PangolinLoginCommands []string
		Specification         string
		VerifyResources       string
	}{
		FailureMessage:        "Pangolin did not create the expected public resources for stack " + stack.Name,
		PangolinLoginCommands: pangolinLoginCommand(loginPayload),
		Specification:         specification,
		VerifyResources:       verify,
	}))
}

func pangolinLoginCommand(loginPayload string) []string {
	script := mustRenderResourceTemplate(resources.ObservabilityPangolinLoginScript, struct {
		LoginPayload string
	}{LoginPayload: loginPayload})
	return strings.Split(strings.TrimSuffix(script, "\n"), "\n")
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func observabilityEnvironmentFile(config observabilityConfig) string {
	return mustRenderResourceTemplate(resources.ObservabilityEnvironment, config)
}

func observabilityRepositoryTask(config observabilityConfig, group string) Task {
	deploymentPath := observabilityRepositoryDirectory + ".deployment"
	verifySnapshot := commandScript(
		"if test -e "+shellQuote(observabilityRepositoryDirectory)+" && ! test -d "+shellQuote(observabilityRepositoryDirectory+"/.git")+" && ! test -f "+shellQuote(deploymentPath)+shellThenSuffix,
		"  echo 'remote configuration directory is not managed by Servestead' >&2",
		"  exit 1",
		"fi",
		"if test -f "+shellQuote(deploymentPath)+" && ! test -d "+shellQuote(observabilityRepositoryDirectory+"/.git")+shellThenSuffix,
		"  recorded_hash=\"$(sed -n 's/^sha256=//p' "+shellQuote(deploymentPath)+")\"",
		"  actual_hash=\"$(sha256sum "+shellQuote(observabilityRepositoryDirectory+"/"+observabilityComposeRepositoryPath)+" | awk '{print $1}')\"",
		"  [ \"$recorded_hash\" = \"$actual_hash\" ] || { echo 'remote configuration snapshot has drifted' >&2; exit 1; }",
		"fi",
	)
	if config.RepositoryOrigin == "" {
		metadata := "commit=" + config.RepositoryCommit + "\nsha256=" + config.RepositorySHA256 + "\n"
		return Task{Name: "Deploy committed configuration snapshot", Apply: commandScript(
			verifySnapshot,
			"if test -d "+shellQuote(observabilityRepositoryDirectory+"/.git")+shellThenSuffix+" echo 'remote Git checkout exists; refusing snapshot overwrite' >&2; exit 1; fi",
			remoteInstallDirectoryCommand(observabilityRepositoryDirectory+"/stacks/observability", "root", group, 0750),
			remoteWriteFileCommand(observabilityRepositoryDirectory+"/"+observabilityComposeRepositoryPath, config.RepositoryCompose, "root", group, 0640),
			remoteWriteFileCommand(deploymentPath, metadata, "root", group, 0640),
		)}
	}

	script := commandScript(
		"IFS= read -r SERVESTEAD_GITHUB_TOKEN || true",
		"export SERVESTEAD_GITHUB_TOKEN",
		"askpass=\"$(mktemp)\"",
		"checkout=\"$(mktemp -d)\"",
		"cleanup_git_credentials() { rm -f \"$askpass\"; rm -rf \"$checkout\"; unset SERVESTEAD_GITHUB_TOKEN; }",
		"trap cleanup_git_credentials EXIT",
		"servestead_github_checkout_help() {",
		"  if [ -n \"$SERVESTEAD_GITHUB_TOKEN\" ]; then",
		"    echo 'GitHub checkout failed. A GitHub token was provided, but GitHub rejected it or the repository is not accessible.' >&2",
		"    echo 'Check that the token is not expired and can read this repository.' >&2",
		"  else",
		"    echo 'GitHub checkout failed. No GitHub token was provided.' >&2",
		"  fi",
		"  echo 'Private GitHub repositories require a personal access token; public repositories can also use one to avoid anonymous rate limits.' >&2",
		"  echo 'Recommended token: fine-grained PAT, selected repository only, Contents: Read-only.' >&2",
		"  echo "+shellQuote("Store it locally with: "+githubTokenSetCommand(config.ProfileID))+" >&2",
		"  echo 'Or set SERVESTEAD_GITHUB_TOKEN before launching Servestead.' >&2",
		"}",
		"printf '%s\\n' '#!/bin/sh' 'case \"$1\" in *Username*) printf \"%s\\\\n\" x-access-token;; *) printf \"%s\\\\n\" \"$SERVESTEAD_GITHUB_TOKEN\";; esac' >\"$askpass\"",
		"chmod 0700 \"$askpass\"",
		"export GIT_ASKPASS=\"$askpass\" GIT_TERMINAL_PROMPT=0",
		verifySnapshot,
		"if test -d "+shellQuote(observabilityRepositoryDirectory+"/.git")+shellThenSuffix,
		"  test -z \"$(git -C "+shellQuote(observabilityRepositoryDirectory)+" status --porcelain)\" || { echo 'remote configuration checkout is dirty' >&2; exit 1; }",
		"  [ \"$(git -C "+shellQuote(observabilityRepositoryDirectory)+" remote get-url origin)\" = "+shellQuote(config.RepositoryOrigin)+" ] || { echo 'remote configuration origin differs' >&2; exit 1; }",
		"  git -C "+shellQuote(observabilityRepositoryDirectory)+" fetch --prune origin || { servestead_github_checkout_help; exit 1; }",
		"else",
		"  git clone --no-checkout -- "+shellQuote(config.RepositoryOrigin)+" \"$checkout/repository\" || { servestead_github_checkout_help; exit 1; }",
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
	return mustRenderResourceTemplate(resources.ObservabilityBeszelConfig, config)
}

func observabilityComposeFile(config observabilityConfig) string {
	return mustRenderResourceTemplate(resources.ObservabilityCompose, struct {
		observabilityConfig
		ServesteadPublicNetwork     string
		BeszelAgentImage            string
		BeszelImage                 string
		BeszelLabels                []string
		BeszelURL                   string
		DockhandDataDirectory       string
		DockhandImage               string
		DockhandLabels              []string
		DozzleImage                 string
		DozzleLabels                []string
		ObservabilityStackDirectory string
		SocketProxyImage            string
	}{
		observabilityConfig:         config,
		ServesteadPublicNetwork:     servesteadPublicNetwork,
		BeszelAgentImage:            beszelAgentImage,
		BeszelImage:                 beszelImage,
		BeszelLabels:                observabilityComposeLabels(config, observabilityBeszelID, "Beszel", "beszel", 8090, "/"),
		BeszelURL:                   "https://beszel." + config.BaseDomain,
		DockhandDataDirectory:       observabilityStackDirectory + "/dockhand_data",
		DockhandImage:               dockhandImage,
		DockhandLabels:              observabilityComposeLabels(config, observabilityDockhandID, "Dockhand", "dockhand", 3000, "/api/auth/session"),
		DozzleImage:                 dozzleImage,
		DozzleLabels:                observabilityComposeLabels(config, observabilityDozzleID, "Dozzle", "dozzle", 8080, "/healthcheck"),
		ObservabilityStackDirectory: observabilityStackDirectory,
		SocketProxyImage:            socketProxyImage,
	})
}

func observabilityComposeLabels(config observabilityConfig, key, name, host string, port int, healthPath string) []string {
	prefix := "pangolin.public-resources." + key
	return []string{
		prefix + ".name=" + name,
		prefix + ".protocol=http",
		prefix + ".full-domain=" + host + "." + config.BaseDomain,
		prefix + ".auth.sso-enabled=true",
		prefix + ".auth.sso-users[0]=" + config.AdminEmail,
		prefix + ".targets[0].hostname=" + host,
		fmt.Sprintf("%s.targets[0].port=%d", prefix, port),
		prefix + ".targets[0].method=http",
		prefix + ".targets[0].healthcheck.enabled=true",
		prefix + ".targets[0].healthcheck.hostname=" + host,
		fmt.Sprintf("%s.targets[0].healthcheck.port=%d", prefix, port),
		prefix + ".targets[0].healthcheck.scheme=http",
		prefix + ".targets[0].healthcheck.path=" + healthPath,
	}
}

func observabilityResourceReconcileCommand(config observabilityConfig, composeCommand string) string {
	loginPayload := pangolinLoginJSON(config.AdminEmail, config.PangolinPassword)
	specs := observabilityResourceSpecs(config.BaseDomain)
	selectDeletes := `import json,sys
data=json.load(sys.stdin)["data"]["resources"]
specs=json.load(open(sys.argv[1], encoding="utf-8"))
for spec in specs:
 matches=[r for r in data if r.get("name")==spec["name"] and r.get("fullDomain")==spec["domain"]]
 canonical=[r for r in matches if r.get("niceId")==spec["nice_id"]]
 keep=canonical[0].get("resourceId") if canonical else None
 for resource in matches:
  if resource.get("resourceId") != keep:
   print(resource["resourceId"])`
	return commandScript(mustRenderResourceTemplate(resources.ObservabilityResourceReconcileScript, struct {
		ComposeCommand        string
		PangolinLoginCommands []string
		SelectDeletes         string
		Specs                 string
	}{
		ComposeCommand:        composeCommand,
		PangolinLoginCommands: pangolinLoginCommand(loginPayload),
		SelectDeletes:         selectDeletes,
		Specs:                 specs,
	}))
}

func observabilityResourceVerifyCommand(config observabilityConfig) string {
	loginPayload := pangolinLoginJSON(config.AdminEmail, config.PangolinPassword)
	specs := observabilityResourceSpecs(config.BaseDomain)
	verifyResources := `import json,sys
data=json.load(sys.stdin)["data"]["resources"]
specs=json.load(open(sys.argv[1], encoding="utf-8"))
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
	return commandScript(mustRenderResourceTemplate(resources.ObservabilityResourceVerifyScript, struct {
		PangolinLoginCommands []string
		Specs                 string
		VerifyResources       string
		VerifyTargets         string
	}{
		PangolinLoginCommands: pangolinLoginCommand(loginPayload),
		Specs:                 specs,
		VerifyResources:       verifyResources,
		VerifyTargets:         verifyTargets,
	}))
}

func observabilityResourceSpecs(baseDomain string) string {
	specs := []observabilityResourceSpec{
		{Name: "Beszel", Domain: "beszel." + baseDomain, NiceID: observabilityBeszelID},
		{Name: "Dozzle", Domain: "dozzle." + baseDomain, NiceID: observabilityDozzleID},
		{Name: "Dockhand", Domain: "dockhand." + baseDomain, NiceID: observabilityDockhandID},
	}
	return mustJSON(specs)
}

func mustJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(data)
}

type stackResourceVerificationSpec struct {
	NiceID string `json:"nice_id"`
	Domain string `json:"domain"`
}

type observabilityResourceSpec struct {
	Name   string `json:"name"`
	Domain string `json:"domain"`
	NiceID string `json:"nice_id"`
}
