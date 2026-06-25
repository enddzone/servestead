# AegisNode Refactor Plan: Native Task Engine

## Context

AegisNode is currently a Go CLI/TUI for provisioning and hardening Ubuntu VPS instances.

Recent architecture changes removed local runtime dependencies on:

- `ansible-playbook`
- local `ssh`
- local `ssh-keygen`

The current implementation uses:

- Native Go SSH transport in `remote.go` via `golang.org/x/crypto/ssh`
- In-process ED25519 key generation in `keygen.go`
- Provider API clients in `cloud.go`
- Guided workflow in `setup.go`
- Direct command entry points in `bootstrap.go` and `hardening.go`

The old embedded Ansible playbooks have been deleted:

- `playbooks/bootstrap.yml`
- `playbooks/hardening.yml`

The problem: `bootstrap.go` and especially `hardening.go` now contain long shell-script strings. This works, but it will become hard to maintain as hardening grows and especially if AegisNode supports more operating systems.

The goal of this refactor is **not** to reintroduce local Ansible as a required runtime dependency. The goal is to keep the dependency-free native runner while making the remote configuration logic structured, testable, idempotent, and easier to extend.

## Decision Summary

Do not switch back to required local Ansible right now.

Instead, refactor toward a small internal task engine:

```text
Go CLI/TUI
  -> Plan builder: setup, bootstrap, harden
  -> Task engine: named tasks with check/apply/verify behavior
  -> OS backend: Ubuntu first, later Debian/Rocky/etc.
  -> Native SSH transport
```

Ansible may be useful later as:

- an optional advanced backend
- an export format for generated plans
- a reference implementation for task semantics

But it should not become the default required runtime unless the native task engine becomes an obvious poor clone of Ansible after multiple OS targets.

## Current Behavior To Preserve

### Key Management

The TUI should remain opinionated and simple:

- Default key path: `$HOME/.ssh/aegisnode_ed25519`
- `setup` asks for one private key path and derives `<private-key>.pub`
- `keygen` creates the private key and matching `.pub` file without local `ssh-keygen`
- Manual login guidance should include `ssh -i <key> <user>@<host>`

### Bootstrap

Current `bootstrap` behavior:

- Connects as initial SSH user, default `root`
- Installs bootstrap packages: `curl`, `git`, `gnupg2`, `sudo`
- Creates admin group
- Creates admin user, default `aegisadmin`
- Adds admin user to `sudo`
- Locks admin password
- Writes `/etc/sudoers.d/<admin-user>` after `visudo -cf`
- Creates `/home/<admin-user>/.ssh`
- Writes `/home/<admin-user>/.ssh/authorized_keys`

Bootstrap intentionally does **not** disable root SSH. Hardening handles that after admin key access exists.

### Hardening

Current `harden` behavior:

- Validates target OS:
  - Ubuntu
  - Ubuntu version >= 22.04
  - kernel version >= 5.15
- Validates all configured sysctl keys exist before writing sysctl config
- Applies pending upgrades:
  - `apt-get update`
  - `apt-get full-upgrade -y`
  - `apt-get autoremove -y`
  - prints a message if `/var/run/reboot-required` exists
- Installs prerequisite packages:
  - `apt-transport-https`
  - `ca-certificates`
  - `curl`
  - `gnupg`
  - `iptables`
  - `unattended-upgrades`
- Writes SSH hardening drop-in:
  - `/etc/ssh/sshd_config.d/99-aegisnode-hardening.conf`
  - `PermitRootLogin no`
  - `PasswordAuthentication no`
  - `KbdInteractiveAuthentication no`
  - `PubkeyAuthentication yes`
- Locks root password with `passwd -l root`
- Creates `/run/sshd` before validating sshd config
- Runs `/usr/sbin/sshd -t`
- Reloads SSH with `systemctl reload ssh || systemctl reload sshd`
- Writes sysctl config:
  - `/etc/sysctl.d/99-vps-hardening.conf`
- Runs `sysctl --system`
- Enables unattended upgrades:
  - `/etc/apt/apt.conf.d/20auto-upgrades`
- Configures CrowdSec apt repository and keyring
- Installs `crowdsec`
- Enables and starts `crowdsec`
- Installs CrowdSec firewall bouncer:
  - `crowdsec-firewall-bouncer-nftables` if `iptables -V` reports `nf_tables`
  - otherwise `crowdsec-firewall-bouncer-iptables`
- Enables and starts `crowdsec-firewall-bouncer`
- Runs `cscli bouncers list`

### UX Constraint

Do not expose implementation phase labels in the TUI.

User-facing TUI language should be outcome-oriented:

- Prepare the AegisNode SSH key
- Set up an existing Ubuntu VPS
- Harden an already set-up VPS
- Run local preflight checks only

Avoid wording like "Phase 1", "Phase 2", etc. in TUI prompts, labels, summaries, or setup output.

## Refactor Goals

1. Replace long ad hoc shell strings with typed tasks.
2. Keep current behavior unchanged during the first refactor pass.
3. Make hardening steps individually named and testable.
4. Improve idempotency by expressing checks and apply steps separately where practical.
5. Make future OS support explicit through backend interfaces.
6. Keep the native SSH runner and no-local-dependency posture.
7. Keep changes surgical: do not redesign the CLI/TUI at the same time.

## Non-Goals

Do not do these in the first refactor:

- Do not add Ansible back as a required dependency.
- Do not add a plugin system.
- Do not add multi-host orchestration.
- Do not implement non-Ubuntu support yet.
- Do not change cloud provider behavior.
- Do not change TUI layout beyond what the task engine requires.
- Do not add background daemons or agents on the target.

## Proposed Architecture

### Core Types

Create a new file, likely `tasks.go`, with minimal primitives:

```go
type Task struct {
    Name        string
    Description string
    Check       RemoteCommand // optional
    Apply       RemoteCommand
    Verify      RemoteCommand // optional
}

type RemoteCommand struct {
    Script     string
    Privileged bool
}

type TaskResult struct {
    Name    string
    Changed bool
    Skipped bool
}
```

The first pass may omit `Changed` if current `remoteClient.Run` cannot capture exit status/output cleanly. Do not overbuild.

A simpler first-pass type is acceptable:

```go
type Task struct {
    Name  string
    Apply string
}
```

But prefer leaving room for `Check` and `Verify`, because idempotency is the point of the refactor.

### Runner

Add a task runner:

```go
func runTasks(ctx context.Context, client remoteClient, sshUser string, tasks []Task) error
```

Initial behavior:

- Print task names to stdout before each task.
- Run `Apply` using existing `privilegedCommand`.
- Stop on first failure.
- Wrap errors with task name:
  - `task "Validate Ubuntu version" failed: ...`

This gives much better live diagnostics than a single `remote command failed`.

### Remote Client Enhancement

Current `remoteClient`:

```go
type remoteClient interface {
    Run(ctx context.Context, command string) error
    Close() error
}
```

For a better task engine, consider changing to:

```go
type remoteClient interface {
    Run(ctx context.Context, command string) (CommandResult, error)
    Close() error
}

type CommandResult struct {
    Stdout string
    Stderr string
    ExitCode int
}
```

However, this is a larger change because current SSH execution streams directly to user stdout/stderr. A staged approach is better:

1. First refactor command generation into tasks while keeping `Run(ctx, command) error`.
2. Later add a separate `RunCapture` method for checks/queries.

Possible interim interface:

```go
type remoteClient interface {
    Run(ctx context.Context, command string) error
    Close() error
}
```

Do not block the first refactor on result capture.

### OS Backend

Create a backend abstraction after tasks exist:

```go
type OSBackend interface {
    SupportedCheck() Task
    BootstrapTasks(config bootstrapConfig, adminPublicKey string) []Task
    HardeningTasks(config hardeningConfig) []Task
}
```

Initial backend:

```go
type UbuntuBackend struct{}
```

All existing behavior moves into `UbuntuBackend`.

Later backends:

- `DebianBackend`
- `RockyBackend`
- `AlmaBackend`

Do not implement them now.

### Task Categories

Suggested file split:

- `tasks.go`: generic task and runner types
- `ubuntu_bootstrap.go`: Ubuntu bootstrap tasks
- `ubuntu_hardening.go`: Ubuntu hardening tasks
- `ubuntu_packages.go`: apt-specific helper tasks
- `ubuntu_ssh.go`: sshd hardening helpers
- `ubuntu_crowdsec.go`: CrowdSec repository and bouncer helpers
- `ubuntu_sysctl.go`: sysctl settings and validation helpers

Keep package `main` for now. Do not introduce subpackages unless the files become unmanageable.

## Migration Plan

### Step 1: Introduce Task Type And Runner

Create `tasks.go`.

Minimal implementation:

```go
type Task struct {
    Name  string
    Apply string
}

func runTasks(ctx context.Context, client remoteClient, sshUser string, tasks []Task) error {
    for _, task := range tasks {
        if err := client.Run(ctx, privilegedCommand(sshUser, task.Apply)); err != nil {
            return fmt.Errorf("%s: %w", task.Name, err)
        }
    }
    return nil
}
```

Verification:

- Add tests with `recordingRemoteClient`.
- Ensure task names are included in errors.
- Existing `go test ./...` passes.

### Step 2: Convert Hardening Commands To Tasks

Replace:

```go
func hardeningCommands() []string
```

with:

```go
func hardeningTasks() []Task
```

Each current command block becomes one named task:

- Validate supported Ubuntu release
- Validate sysctl keys
- Apply package upgrades
- Install hardening prerequisites
- Write sshd hardening config
- Validate and reload SSH
- Write sysctl hardening config
- Reload sysctl settings
- Enable unattended upgrades
- Configure CrowdSec keyring
- Configure CrowdSec repository
- Install CrowdSec and firewall bouncer

Keep shell content identical initially.

Verification:

- Existing hardening tests should assert task names and key script content.
- `go test ./...`
- `go vet ./...`
- `go build -o /tmp/aegisnode .`

### Step 3: Convert Bootstrap Commands To Tasks

Replace:

```go
func bootstrapCommands(config bootstrapConfig, adminPublicKey string) []string
```

with:

```go
func bootstrapTasks(config bootstrapConfig, adminPublicKey string) []Task
```

Suggested task names:

- Install bootstrap packages
- Create administrative group and user
- Configure passwordless sudo
- Create administrative SSH directory
- Install administrative public key

Verification:

- Existing bootstrap tests updated to inspect task content.
- `go test ./...`

### Step 4: Extract Ubuntu Helpers

Move helper content out of `hardening.go` and `bootstrap.go`.

Examples:

```go
func ubuntuAptInstallTask(name string, packages ...string) Task
func ubuntuWriteFileTask(name, path, content, owner, group string, mode os.FileMode) Task
func ubuntuSystemdEnableNowTask(service string) Task
```

Keep helper names boring and explicit. Avoid clever generic abstractions.

Verification:

- Tests remain focused on generated tasks.
- No behavior changes.

### Step 5: Introduce UbuntuBackend

Add:

```go
type UbuntuBackend struct{}

func (UbuntuBackend) BootstrapTasks(config bootstrapConfig, adminPublicKey string) []Task
func (UbuntuBackend) HardeningTasks(config hardeningConfig) []Task
```

At first, select Ubuntu backend directly. Do not implement OS detection yet.

```go
backend := UbuntuBackend{}
tasks := backend.HardeningTasks(config)
```

Verification:

- Existing tests pass.
- Backend-specific tests added.

### Step 6: Add OS Detection Later

Only after the backend shape is stable, add OS detection.

Possible command:

```sh
. /etc/os-release
printf '%s %s\n' "$ID" "$VERSION_ID"
```

This likely requires `RunCapture`, so do not force it into the first pass.

Potential future type:

```go
type OSRelease struct {
    ID        string
    VersionID string
}
```

Behavior:

- If Ubuntu >= 22.04: use `UbuntuBackend`
- Otherwise return a clear unsupported OS error

## Idempotency Improvements To Add After Structural Refactor

Do not try to solve all of these while moving code. Add them once task structure is in place.

### Package Tasks

Current package tasks are acceptable because `apt-get install -y` is mostly idempotent.

Potential improvements:

- split `apt-get update` from package installation
- reduce repeated `apt-get update`
- add task names around upgrades so failures are easier to identify

### File Tasks

Current `remoteWriteFileCommand` always writes and moves the file.

Improve later:

- write temp file
- compare with existing target using `cmp -s`
- only `mv` if content differs
- emit `changed` status when result capture exists

### Service Tasks

Current service commands always run `systemctl enable --now`.

This is acceptable but can be improved later with checks:

- `systemctl is-enabled`
- `systemctl is-active`

### SSH Hardening

Keep the safe order:

1. Write drop-in
2. Ensure `/run/sshd`
3. Validate with `/usr/sbin/sshd -t`
4. Reload SSH

Never reload SSH before validation succeeds.

### Root SSH Lockout

Keep root SSH disabling in hardening, not bootstrap.

Reason:

- Bootstrap may still need root as recovery path.
- Hardening happens after admin key access is installed.

Future enhancement:

- Before disabling root SSH, verify admin key login in a separate connection.
- This may require opening a second SSH connection as `aegisadmin`.

## Testing Strategy

### Unit Tests

Maintain and expand:

- `bootstrap_test.go`
- `hardening_test.go`
- `remote_test.go`
- `setup_test.go`
- `keygen_test.go`

Add tests for:

- task names
- task order
- critical script snippets
- error wrapping includes task name
- TUI summaries do not contain `Phase`
- hardening contains SSH lockout directives
- hardening creates `/run/sshd` before `sshd -t`
- CrowdSec installs firewall bouncer
- package upgrade task exists

### Integration Tests

Do not add live VPS tests to normal CI.

Optional manual smoke test checklist:

```sh
go build -o bin/aegisnode .
bin/aegisnode doctor
bin/aegisnode setup
ssh -i ~/.ssh/aegisnode_ed25519 aegisadmin@<ip>
ssh -i ~/.ssh/aegisnode_ed25519 root@<ip>   # should fail after hardening
```

Remote verification commands:

```sh
sudo sshd -T | grep -E 'permitrootlogin|passwordauthentication|kbdinteractiveauthentication|pubkeyauthentication'
sudo systemctl status crowdsec --no-pager
sudo systemctl status crowdsec-firewall-bouncer --no-pager
sudo cscli bouncers list
test ! -f /var/run/reboot-required || echo reboot required
```

### Required Local Verification Before Finalizing Refactor

Run:

```sh
go test ./...
go vet ./...
go build -o /tmp/aegisnode .
go test -race ./...
```

## Risk Register

### Locking Out SSH

Risk:

- Disabling root/password SSH before admin key works can lock out the operator.

Mitigation:

- Keep root SSH disabling in hardening after bootstrap.
- Future: verify a second admin-user SSH connection before disabling root SSH.

### Package Upgrades Trigger Reboot Requirement

Risk:

- Kernel or libc updates may require reboot.

Mitigation:

- Current behavior prints if `/var/run/reboot-required` exists.
- Future: surface this clearly in TUI final output.

### CrowdSec Bouncer Package Differences

Risk:

- Package names or repository setup can vary by distro/release.

Mitigation:

- Keep CrowdSec logic in Ubuntu backend.
- Add backend-specific bouncer task later for other OSes.

### Over-Abstraction

Risk:

- A task engine can become a half-baked Ansible clone.

Mitigation:

- Keep the first pass minimal.
- Only add abstractions that remove current duplication or make OS backend support concrete.

### Hidden Behavior Changes

Risk:

- Refactor changes command order or shell behavior.

Mitigation:

- First pass should preserve current script content and order.
- Tests should assert critical order and snippets.

## Suggested First PR Scope

Keep the first PR narrow:

- Add `Task`
- Add `runTasks`
- Convert `hardeningCommands` to `hardeningTasks`
- Convert `bootstrapCommands` to `bootstrapTasks`
- Preserve all current shell content and behavior
- Update tests

Do **not** add OS detection or non-Ubuntu support in the first PR.

## Future Enhancements After Refactor

1. Add `RunCapture` support for OS detection and richer checks.
2. Add an explicit `verify` command that audits target state without changing it.
3. Add admin SSH verification before root lockout.
4. Add clear reboot-required reporting to setup final output.
5. Add `cloud-init` generation for first-boot bootstrap on providers that support user data.
6. Add optional Ansible playbook export if users want to run or audit tasks externally.
7. Add Debian backend.
8. Add Rocky/Alma backend.

## Important Current Files

- `remote.go`: native SSH transport, known_hosts handling, remote shell helpers.
- `bootstrap.go`: admin user bootstrap command generation.
- `hardening.go`: current hardening command generation; main refactor target.
- `setup.go`: guided workflow; keep user-facing language outcome-based, not phase-based.
- `keygen.go`: in-process ED25519 key generation.
- `README.md`: operator-facing behavior.
- `memory/progress.md`: implementation tracking, may mention phases internally.

## Final Notes For The Next Session

The next session should start by refactoring structure, not changing behavior.

Recommended first concrete change:

1. Add `tasks.go`.
2. Convert `runHardeningSteps` to consume `[]Task`.
3. Rename `hardeningCommands()` to `hardeningTasks()`.
4. Keep every current shell line intact.
5. Update tests to validate task names and scripts.

That gives immediate maintainability value and creates the foundation for backend support without taking on a larger design rewrite.
