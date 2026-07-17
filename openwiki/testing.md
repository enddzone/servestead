---
type: Testing Guide
title: Servestead Testing
description: Guide to the Servestead Go test suite, including test commands, backend test inventory, table-driven and mock-based patterns, race detection, coverage, linting, frontend template checks, and untested integration boundaries.
tags: [servestead, testing, golang, coverage, linting, ci]
---

# Testing

Servestead has a comprehensive Go test suite. All tests are in `backend/` alongside the source code, following standard Go test conventions (`_test.go` suffix, `package main`).

## Running Tests

```sh
# Run all tests with race detector (as CI does)
go test -race ./backend/...

# Run a specific test
go test -run TestBootstrap ./backend/

# Generate coverage profile
go test -race -coverprofile=coverage.out ./backend/
```

CI runs `go test -race` with coverage profile generation. The coverage profile is uploaded as an artifact and consumed by the SonarCloud analysis job.

## Test File Inventory

| File | Tests |
|---|---|
| `bootstrap_test.go` | Bootstrap task construction, config validation, ED25519 enforcement |
| `cli_test.go` | CLI dispatch, help/usage, unknown commands, flag parsing |
| `cloud_test.go` | DigitalOcean provider: catalog, create, SSH key upload, reboot, destroy, polling |
| `command_test.go` | Shell command builders (remoteWriteFileCommand, privilegedCommand, aptInstallCommand) |
| `config_repository_test.go` | Repository init, scaffold, commit, stack loading, GitHub cloning, drift detection |
| `github_token_commands_test.go` | GitHub token set/status/remove CLI commands |
| `hardening_test.go` | Hardening task list, sysctl config, SSH config, CrowdSec install |
| `keygen_test.go` | ED25519 key generation, existing key handling, force flag |
| `network_test.go` | Network task list, Docker config, UFW rules, masquerade |
| `observability_test.go` | Observability deployment, Pangolin resource reconciliation, Dockhand environment |
| `pangolin_credentials_test.go` | Credential retrieval, initial-setup-complete check |
| `pangolin_registration_test.go` | Pangolin API registration flow |
| `profile_test.go` | Profile store: create, load, save, delete, resolve by IP, secrets, run events |
| `provision_tui_test.go` | TUI model: screen transitions, catalog loading, droplet creation, confirmation |
| `proxy_test.go` | Proxy task list, config validation, credential generation, Pangolin bootstrap |
| `remote_test.go` | SSH client: host key callback, command execution, stdin piping |
| `secret_commands_test.go` | Secrets CLI: init, status, export-key, import-key |
| `secrets_test.go` | SOPS/age encryption/decryption, identity management, provider abstraction |
| `setup_test.go` | Setup orchestrator: mode dispatch, stage execution, profile setup, full-run stages |
| `stack_test.go` | Stack parsing, metadata, override generation, public resource labels, env management |
| `tasks_test.go` | Task execution, TaskEvent emission, reporter interface |
| `tui_key_test.go` | TUI key binding helpers |
| `web_ui_test.go` | Web UI: home/setup panels, address validation (loopback only), auth middleware, CSRF, draft secrets, run lifecycle (start/cancel/retry), SSE broker, ops panel (stacks CRUD, GitOps, runs, access, cloud actions), frontend asset assertions |

## Testing Patterns

- **Table-driven tests**: Standard Go pattern with subtests (`t.Run`)
- **`t.TempDir()`**: Used for filesystem isolation — profiles, repositories, key files
- **Mock providers**: Custom mocks with `t.Cleanup()` to swap and restore globals (e.g., `recordingSecretProvider` in `stack_test.go` swaps the global `secretProviderForName`)
- **`t.Helper()`**: Marked on helper functions to improve error reporting
- **Inline YAML**: Test fixtures use inline YAML constants (e.g., `testApplicationCompose` with nginx/api/worker services)
- **Bubble Tea testing**: TUI tests import `charm.land/bubbletea/v2` and input components (`textinput`, `spinner`) to test model state transitions
- **HTTP testing**: `httptest` and `net/http` for web UI tests
- **Race detector**: CI runs `go test -race` to catch concurrent access issues

## Coverage

- **Coverage profile**: Generated as `coverage.out` and uploaded as a CI artifact
- **SonarCloud**: Analyzes coverage with exclusions for `backend/setup.go` and `frontend/**` (these are large generated/templated files)
- **golangci-lint**: Run with `tests: true` — linters analyze test code too

## Linting Before Tests

Per `AGENTS.md`, always run `golangci-lint` after making changes:

```sh
golangci-lint fmt        # format check
golangci-lint lint       # lint check
go vet ./backend/...     # go vet
```

The CI pipeline runs all three before `go test -race`.

## Frontend Template Tests

The `templ generate` step in CI verifies that committed `_templ.go` files match the `.templ` sources. If templates are modified, always run:

```sh
go tool templ generate
```

and commit the generated files. CI will fail if the generated output doesn't match.

## What's Not Tested

- **Remote SSH execution against live servers**: Tests validate command construction and task lists, but do not connect to real servers
- **DigitalOcean API calls**: Tests use mock providers; the `cloud_test.go` tests validate the provider logic without making real API calls
- **Provisioning is billable**: The test suite does not create real Droplets
- **Docker Compose deployment**: Stack tests validate parsing, override generation, and label construction, but do not run `docker compose up`
