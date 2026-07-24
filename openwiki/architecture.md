---
type: Architecture Guide
title: Servestead Architecture
description: Technical architecture of the Servestead Go CLI, including command dispatch, setup orchestration, Bubble Tea and web interfaces, SSH task execution, embedded resources, profile persistence, and system data flow.
tags: [servestead, architecture, golang, ssh, tui, web-ui]
---

# Architecture

Servestead is a single-binary Go CLI (`package main` in `backend/`) that orchestrates remote server configuration over SSH. It has no runtime dependency on local Ansible, OpenSSH clients, or `ssh-keygen` — all SSH operations use `golang.org/x/crypto/ssh` and all key generation uses `golang.org/x/crypto/ssh` + `crypto/ed25519`.

## Entry Point and CLI Dispatch

**`backend/main.go`** — Calls `run()` with `os.Args[1:]`, stdout/stderr, and `os.Getenv`. On error, checks for `tuiPresentedError` (TUI already displayed the error); otherwise prints to stderr and exits 1.

**`backend/cli.go`** — Central command dispatch:
- `run()` (line 41): If no args, prints usage. If `help`/`-h`/`--help`, prints usage and returns nil. Otherwise looks up the handler in `cliHandlers()` and calls it.
- `cliHandlers()` (line 62): Returns a `map[string]cliHandler` where `cliHandler` is `func() error`. Each handler is a closure capturing context, args, and I/O writers. Commands: `provision`, `bootstrap`, `harden`, `network`, `proxy`, `pangolin-credentials`, `github-token`, `secrets`, `keygen`, `stack`, `setup`, `ui`, `doctor`.
- `getenvFunc` (line 38): `func(string) string` — injected environment lookup for testability.

Individual handlers (e.g., `runProvision` at line 87) parse flags with `flag.NewFlagSet`, validate, construct config structs, and invoke the relevant business logic.

## Setup Orchestrator

**`backend/setup.go`** (~6,700 lines — the largest file) is the heart of Servestead. It supports two paths:

### Non-interactive path (`--ip` or explicit flags)
`runSetup()` (line 139) → `runSetupFromOptions()` → `runSetupPlan()` (line 5978) or `runProfileSetupPlan()` (line 4616).

### Interactive TUI path (no `--ip`)
`runSetup()` → `runInteractiveSetup()` (line 197) → `collectSetupRequest()` + `profileSetupModel` TUI.

### Stage execution
`runFullSetupStages()` (line 4946) defines 5 ordered stages, each wrapped in a `fullSetupStage` (line 5000) with a `key`, `skip` message, and `run` function:

1. **Bootstrap** → `runBootstrapSetupStage()` (line 5028)
2. **Harden** → `runHardeningSetupStage()` (line 5054)
3. **Network** → `runNetworkSetupStage()` (line 5070)
4. **Proxy** → `runProxySetupStage()` (line 5090)
5. **Observability** → `runObservabilitySetupStage()` (line 5122)

Completed stages are skipped on retry. After the 5 core stages, configured stack stages run via `runFullConfiguredStackStages()`.

`runSetupStage()` (line 4972) dispatches a single named stage — supports `bootstrap`, `harden`, `network`, `proxy`, `observability`, `stacks`, `platform` (network+proxy+observability combined), and dynamic `stack:<name>` stages.

### Key types
- `setupMode` (line 32): 8 modes — `setupModeProviderKey`, `setupModeBootstrapHarden`, `setupModeHardenOnly`, `setupModeNetwork`, `setupModeProxy`, `setupModeDoctor`, `setupModeFullRun`, `setupModeObservability`.
- `setupConfig` (line 45): Master config with ~25 fields (host, SSH keys, domain, Pangolin/Beszel credentials, Git repo details, stacks, profile ID).
- `setupStageRun` (line 4937): Runtime context for stage execution — holds `Profile`, `setupConfig`, `runID`, `TaskReporter`, and I/O writers.

## Terminal UI (Bubble Tea)

Servestead uses the [Charm Bubble Tea](https://github.com/charmbracelet/bubbletea) framework (`charm.land/bubbletea/v2`) with Lipgloss for styling and Bubbles components (filepicker, textinput, list, table, progress, spinner, viewport).

### Provisioning wizard — `backend/provision_tui.go`
- `provisionScreen` (line 24): 9-screen state machine — `Input` → `Loading` → `Region` → `Size` → `Image` → `SSHKey` → `Review` → `Creating` → `Done`.
- `digitalOceanProvisionModel` (line 44): Main Bubble Tea model. Async commands fetch the DO catalog (`loadCatalog`, line 461) and create the Droplet (`createDroplet`, line 477).

### Profile setup wizard — `backend/setup.go`
- `profileSetupScreen` (line 456): ~20 screens covering profile picker, dashboard, intake forms, repository config, stacks management (compose browser, service editor, route editor, environment, review, diff, commit), and cloud operations.
- `profileSetupModel` (line 513): Main setup TUI model with ~70 fields. Implements `Init()` (line 908), `Update()` (line 912), `View()` (line 3239).
- `profileRunModel` (line 5356): Live task execution display — spinner, progress bar, stage status, streaming log output. Receives `TaskEvent` messages via a channel.

The TUI pattern: **screen-based state machine** where `Update()` dispatches key messages to screen-specific handlers, each of which may transition screens or issue async `tea.Cmd`s for cloud API calls or SSH task execution.

## Web UI

**`backend/web_ui.go`** (~1,350 lines) and **`backend/web_ops.go`** (~1,380 lines) implement a local web UI for browser-based operations.

### Server lifecycle
- `runUI()` (`web_ui.go` line 43): Starts a loopback HTTP server (default `127.0.0.1:0` = random port), generates a random URL token for authentication, opens a browser, and blocks until shutdown.
- `webServer` struct (line 136): Holds `ProfileStore`, `webRunManager`, auth tokens (session + CSRF), draft state, and a `done` channel.
- `routes()` (line 168): `http.ServeMux` with routes for `/assets/` (embedded static files), `/ui` (home/command center), `/setup` (setup start panel), `/setup/*` (start, intent, profile-values, repository, review, run, cancel, retry, credentials), `/ops/profiles` + `/ops/profiles/` (delegated to `web_ops.go`), `/ops/cloud/provision`, `/events/runs/` (SSE), and `/shutdown`. Three shell renderers — `renderShell` (setup), `renderOpsShell` (ops), and `renderAppShell` (shared base) — carry the active section and selected profile across navigation.
- **Authentication** (line 190): `withAuth()` middleware checks the session cookie and CSRF token for POST requests.

### Real-time updates
- `webRunManager` (line 1018): Manages async setup runs. `Start()` (line 1039) launches a goroutine, `Cancel()` (line 1067) cancels, `Retry()` (line 1079) retries.
- `webEventBroker` (line 1242): Pub/sub broker implementing `TaskReporter`. Receives `TaskEvent`s from task execution and forwards them as `webEvent`s to SSE subscribers. `Subscribe()` (line 1300) returns a channel for a specific run ID.

### Ops panel — `web_ops.go`
Handlers for profile management, stack CRUD, GitOps (commit/review/sync), run history/detail, access management (Pangolin credentials, GitHub tokens), and cloud actions (restart/destroy/provision). Route dispatch uses URL path splitting — `handleOpsProfile` (line 34) splits path segments and dispatches to sub-handlers.

### Frontend rendering
The web UI renders using [a-h/templ](https://github.com/a-h/templ) components from the `servestead/frontend` package (`frontend/ui.templ`, `frontend/ops.templ`). All assets (CSS, htmx JS, SSE extension) are embedded via `go:embed` in `frontend/assets.go`. Interactive forms use htmx attributes (`hx-get`, `hx-post`, `hx-target`, `hx-swap`) for partial page updates without full reloads.

## SSH Execution Layer

**`backend/remote.go`** (255 lines) and **`backend/tasks.go`** (99 lines)

### SSH client
- `remoteClient` interface (line 23): `Run(ctx, command) error` + `Close() error`.
- `remoteStdinClient` interface (line 28): `RunWithStdin(ctx, command, stdin) error` — for piping stdin (used by secret injection).
- `sshRemoteClient` (line 32): Wraps `golang.org/x/crypto/ssh.Client`. Constructed by `newSSHRemoteClient()` (line 38) which:
  1. Parses host:port (defaults to port 22)
  2. Reads and parses the private key file
  3. Configures SSH with public key auth and a **trust-on-first-use** host key callback (`acceptNewHostKeyCallback()`, line 138) — unknown keys are appended to `~/.ssh/known_hosts`; changed keys fail
  4. Dials with a context-aware `net.Dialer` (30s timeout)
- `RunWithStdin()` (line 79): Creates an SSH session, sets stdout/stderr/stdin, runs in a goroutine, supports **context cancellation** (sends `SIGKILL` on cancel).

### Shell command builders
- `remoteWriteFileCommand()` (line 185): Base64-encodes content, pipes through `base64 -d` to a temp file, then `chown`/`chmod`/`mv` atomically.
- `privilegedCommand()` (line 222): Wraps a script in `sudo sh -c` (or `sh -c` if root).
- `aptInstallCommand()` (line 229): Non-interactive `apt-get install` with dpkg lock timeout (up to 300s).
- `commandScript()` (line 212): Joins lines with `set -e` prefix for fail-fast scripts.
- `shellQuote()` (line 204): Single-quote escaping for safe shell arguments.

### Task execution
- `Task` struct (`tasks.go` line 12): `Name`, `Apply` (shell script), `Stdin` (optional input to pipe).
- `runTasksWithReporter()` (line 56): Iterates over `[]Task`, executing each via `client.Run()` or `client.RunWithStdin()`. Commands are wrapped with `privilegedCommand(sshUser, task.Apply)`. Emits `TaskEvent`s: `run_started` → per-task `task_started` → `log_line`s → `task_succeeded`/`task_failed` → `run_completed`.
- `TaskEvent` (line 33): JSON-serializable event with type, run ID, stage, task index/total/name, stream, line, error, and timestamp.
- `TaskReporter` interface (line 46): `Report(TaskEvent)` — implemented by `webEventBroker` (web UI SSE), `profileRunReporter` (TUI), and `synchronizedTaskReporter` (thread-safe wrapper, setup.go line 5257).
- `writeTaskEventJSONL()` (line 90): Persists task events as JSONL to profile log files for run history.

## Embedded Resources

**`backend/resources/resources.go`** (41 lines) — Uses `//go:embed bootstrap hardening network observability proxy stacks` to embed all deployment scripts and config templates at compile time into `resources.FS` (an `embed.FS`). Exports ~25 string constants for resource paths.

**`backend/resource_renderer.go`** (39 lines):
- `mustReadResource()` (line 13): Reads an embedded file as a string.
- `mustRenderResourceTemplate()` (line 21): Parses an embedded `.tmpl` file as a Go `text/template` with custom functions (`aptGet`, `jsonString`, `join`, `noninteractiveAptGet`, `shellQuote`, `yamlDoubleQuote`, `yamlSingleQuote`) and executes it with arbitrary data.

The pattern: **shell scripts and config files are embedded as templates, rendered at runtime with profile/config-specific values, then transferred to the remote server via SSH** (using `remoteWriteFileCommand` for files or piped via stdin for scripts).

## Profile Storage

**`backend/profile.go`** — Profiles are the persistence layer for servers.

### Data model
- `Profile` struct (line 37): `ID`, `Name`, `IP`, `InitialSSHUser`, `AdminUser`, `PrivateKeyPath`, `BaseDomain`, `LetsEncryptEmail`, `PangolinAdminEmail`, `ConfigRepositoryPath`, `Cloud *ProfileCloud`, `CreatedAt`, `UpdatedAt`.
- `ProfileCloud` (line 53): Cloud metadata — `Provider`, `ResourceID`, `Name`, `Region`, `Size`, `Image`, `PriceMonthly`, `PriceHourly`, `CreatedAt`, `DestroyedAt`.
- `ProfileState` (line 74): `ActiveRunID`, `StackRepositoryCommit`, `Runs` map.
- `SetupRun` / `SetupStageStatus` (lines 80–93): Run lifecycle (`planned`/`running`/`complete`/`failed`/`cancelled`) with per-stage timestamps and errors.
- `ProfileSecrets` (line 95): `ServerSecret`, `PangolinSetupToken`, `PangolinAdminPassword`, `NewtID`, `NewtSecret`, `BeszelAdminPassword`, `BeszelSystemToken`, `BeszelHubPrivateKey`/`PublicKey`, `GitHubToken`, `StackSecretIdentity`/`Recipient`. Each has an `Ensure*` method that generates on first access.

### File layout
- `ProfileStore` interface (line 258): `List`, `ResolveByIP`, `Create`, `Load`, `Save`, `Delete`, `LoadSecrets`, `SaveSecrets`, `AppendRunEvent`.
- `fileProfileStore` (line 270): Root at `$XDG_CONFIG_HOME/servestead/profiles/`. Per-profile directory at `0700` with:
  - `profile.json` — profile data
  - `state.json` — run state
  - `secrets.json` — secrets (owner-only `0600`)
  - `logs/<runID>.jsonl` — run event logs (`0600`)
- `newProfileID()` (line 539): Generated from sanitized IP + UTC timestamp.
- `atomicWriteJSON()` (line 556): 2-space indent, temp file, fsync, atomic rename, `chmod 0600`.
- Path safety: profile/run IDs validated against `storePathComponentPattern` with `..` traversal checks.

## Data Flow Summary

```
main() → run() → cliHandlers[name]()
                    ↓
         ┌────────┴────────┐
         ↓                 ↓
    setup.go           direct commands
  (TUI or web UI)     (provision, bootstrap,
         ↓             harden, proxy, etc.)
    collect config            ↓
         ↓             build Task[] with
    runFullSetupStages()  rendered templates
         ↓             from resources.FS
    per-stage Task[]         ↓
         ↓             runTasksWithReporter()
    SSH via remote.go        ↓
    (sshRemoteClient)  sshRemoteClient.Run()
         ↓                   ↓
    TaskEvents →        remote shell execution
    TUI / web SSE
```

## Source Map

| File | Purpose |
|---|---|
| `backend/main.go` | Entry point, error handling |
| `backend/cli.go` | Command dispatch, flag parsing |
| `backend/setup.go` | Setup orchestrator, TUI models, stage execution |
| `backend/remote.go` | SSH client, shell command builders |
| `backend/tasks.go` | Task execution, TaskEvent, TaskReporter |
| `backend/resource_renderer.go` | Template rendering for embedded resources |
| `backend/resources/resources.go` | go:embed declarations and path constants |
| `backend/profile.go` | Profile data model and file-based store |
| `backend/profile_cloud.go` | Cloud action handlers (reboot/destroy) |
| `backend/web_ui.go` | Web UI server, auth, run manager, SSE broker |
| `backend/web_ops.go` | Web UI ops panel handlers |
| `backend/provision_tui.go` | DigitalOcean provisioning TUI wizard |
| `frontend/ui.templ` | Web UI layout shell (templ) |
| `frontend/ops.templ` | Web UI ops panel components (templ) |
| `frontend/types.go` | View-model structs for templ rendering |
| `frontend/assets.go` | Embedded CSS/JS assets for web UI |
