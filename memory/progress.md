# Implementation Progress

Last updated: 2026-06-26

Source of truth for planned work: `implementation_plan.html`.
UX overhaul handoff source: `ux_overhaul_plan.html`.

## Current status

- Overall blueprint: **12 of 15 repository tasks implemented (80%)**
- UX overhaul: **complete (24 of 24 UX checklist items implemented)**
- Phase 1 — Local CLI Coordinator & VPS Bootstrapping: **complete**
- Phase 2 — Operating System & Kernel Hardening: **complete**
- Phase 2.5 — Guided Live-Test UX: **complete**
- Phase 3 — Resolving Docker & UFW Conflict: **complete**
- Phase 4 — Pangolin & Reverse Proxy: **complete for repository automation; live DNS/ACME validation requires an external domain**
- Phase 5: **not started**

The interactive HTML checklist uses browser-local storage. `ux_overhaul_plan.html` now defaults all completed UX overhaul tasks to checked when no browser-local checklist exists. This file records repository implementation progress independently of browser state.

## UX overhaul backend handoff — 2026-06-26

Implemented backend pieces from `ux_overhaul_plan.html`:

- `aegisnode setup --ip <host>` creates or reuses a saved profile and runs bootstrap, harden, network, and proxy as one full setup plan.
- Profiles are stored under `filepath.Join(os.UserConfigDir(), "aegisnode")`.
- Profile data is split into `profile.json`, `state.json`, `secrets.json`, and per-run JSONL logs.
- Profile directories use `0700`; JSON files and secrets use `0600`; JSON saves are atomic.
- Corrupt JSON load errors include the affected file path and do not delete data.
- `GenerateServerSecret()` uses 32 random bytes encoded as base64url.
- Profile-aware setup generates, saves, and reuses the Pangolin `server.secret` without printing it.
- Internal proxy config now uses `ServerSecret`; direct `proxy --server-secret` and deprecated `--postgres-password` remain compatible.
- `TaskReporter` and structured `TaskEvent` are wired through bootstrap, harden, network, and proxy stage execution.
- Run/stage state is updated on start, success, failure, retry, and skip/resume.
- Second and later profile-aware setup runs skip any stage marked complete in prior profile state and print explicit messages such as `bootstrap administrative access already complete; skipping.`
- `setup --ip --domain ... --email ... --yes` supports non-interactive scripted execution when all required values are available.

Backend verification:

- `go test ./...` passes.
- Tests cover profile persistence/permissions/corrupt JSON, generated secret shape and reuse, structured task event order, full-run execution, state persistence, no secret exposure in normal output, and second-run skip behavior.

UI/dashboard follow-up implementation:

- `aegisnode setup` without `--ip` now opens a profile-first TUI instead of the legacy mode-first menu.
- The new TUI lists saved profiles, opens a dashboard backed by `ProfileState`, collects required full-run values up front, exposes advanced SSH/profile fields, supports fresh profile creation, and renders a final plan review before remote execution starts.
- Saved profiles can be deleted from the dashboard with confirmation. Deletion removes only local profile files, secrets, state, and run logs.
- Fresh profile creation from an existing bootstrapped profile seeds bootstrap as complete and uses the saved admin user for remaining stages, avoiding root login on already-hardened servers.
- The TUI uses `bubbles/list` for profile selection, `bubbles/table` for stage state, `bubbles/progress` for completion and live run progress, `bubbles/viewport` for the plan preview and live log scrollback, `bubbles/spinner` for active runs, and `bubbles/help`/`key` for footer hints.
- The older one-off setup modes remain reachable through an advanced legacy setup entry.
- Direct command compatibility and scripted `setup --ip --domain ... --email ... --yes` behavior are preserved.
- Interactive profile-aware setup now runs remote execution inside a Bubble Tea command loop. The run view shows spinner status, task progress, current stage/task, per-stage rows, and inline stdout/stderr/task logs while structured events continue to persist profile state and JSONL logs.
- Saved profile dashboards support one-time stage runs from the stage table: `j`/`k` selects Bootstrap, Harden, Network, or Proxy, `r` runs the selected stage even when prior profile state marks it complete, and `v` opens the full-plan review. `esc` goes back; `q` quits from navigation and run screens while remaining normal text inside focused input fields. Full-run resume behavior still skips previously completed stages.
- Non-interactive and `--yes` profile-aware runs intentionally keep the existing script-friendly stdout/stderr runner path.
- Live run view tests cover event-driven rendering and structured log-line conversion; `go test ./...` passes after the final UX implementation.

## Phase checklist

### Phase 1 — Complete

- [x] Define the Go coordinator architecture with a native SSH remote runner.
- [x] Read cloud API credentials from the environment and create Ubuntu instances on Hetzner or DigitalOcean.
- [x] Bootstrap a target through its initial SSH user and install an administrative ED25519 public key.

Verification completed on 2026-06-23:

- `go test -race ./...`
- `go vet ./...`
- `go build -o /tmp/aegisnode .`
- CLI help smoke test
- Provider calls tested against local HTTP servers; no billable cloud resource was created.
- Native bootstrap command generation and privilege wrapping tested.
- Native ED25519 key generation and SSH key parsing tested.

A live bootstrap still requires a real Ubuntu target and is intentionally not part of automated tests.

### Phase 2 — Complete

- [x] Deploy `/etc/sysctl.d/99-vps-hardening.conf`.
- [x] Apply pending package upgrades.
- [x] Disable root SSH and password-based SSH authentication.
- [x] Configure unattended upgrades.
- [x] Install CrowdSec and a firewall remediation component.

Verification completed on 2026-06-25:

- Added the `harden` command with native remote hardening steps.
- The hardening runner validates Ubuntu release, kernel version, and every requested sysctl key before applying sysctl configuration.
- Hardening applies pending package upgrades, locks the root password, writes an sshd drop-in to disable root/password login, validates sshd config, and reloads SSH.
- CrowdSec repository configuration uses an explicit apt keyring and source-list entry instead of a shell-piped installer script.
- CrowdSec installs the matching firewall bouncer for nftables or iptables so decisions are enforced locally.
- Automated verification: `go test ./...`, `go test -race ./...`, `go vet ./...`, and `go build -o /tmp/aegisnode .`.

### Phase 2.5 — Complete

- [x] Add a Charmbracelet TUI for guided live testing on an existing VPS.
- [x] Add local preflight checks before remote bootstrap or hardening changes.
- [x] Provide non-interactive `doctor` preflight output for quick diagnostics.
- [x] Add provider SSH keypair generation guidance for cloud provisioning.

Verification completed on 2026-06-25:

- Added `setup` for the guided TUI.
- Added `doctor` for direct local preflight checks.
- Added `keygen` and a matching TUI path to generate the AegisNode ED25519 key used for provider login and later administrative access.
- Simplified setup key prompts so the TUI asks for one private key path and derives the matching `.pub` path automatically.
- The TUI explains each path without exposing implementation phase labels, confirms the selected plan, and reports that preflight checks stop execution before remote changes when required local prerequisites fail.

### Phase 3 — Complete

- [x] Configure Docker daemon packet-filter behavior.
- [x] Add the required UFW forwarding/NAT rules.
- [x] Establish the default-deny UFW policy and explicit routes.

Verification completed on 2026-06-25:

- Added the `network` command with native remote Docker/UFW steps separate from `harden`.
- Docker is installed from Docker's official Ubuntu apt repository using a keyring-backed deb822 source file.
- The network runner writes `/etc/docker/daemon.json` with Docker bridge firewall/NAT support enabled, ensures the administrative SSH user has passwordless sudo and Docker group membership, enables IPv4 forwarding, replaces only the AegisNode-managed UFW NAT block, preserves SSH access on the configured SSH port, denies incoming and routed traffic by default, allows HTTP/HTTPS ingress, allows routed traffic from Docker bridge CIDRs, enables UFW, and restarts Docker.
- Added a guided setup path for Docker networking and UFW without adding the step to baseline hardening.
- Automated verification: `go test ./...`, `go test -race ./...`, `go vet ./...`, and `go build -o /tmp/aegisnode .`.

### Phase 4 — Complete

- [x] Deploy Traefik, Gerbil, and Pangolin.
- [x] Configure required DNS records by printing the exact apex and wildcard records the operator must create at their registrar.
- [x] Start the stack and verify the container services are running.

Verification completed on 2026-06-26:

- Added the `proxy` command with native remote deployment steps separate from `network`.
- Added a guided setup path for the proxy deployment with domain, Let's Encrypt email, and masked server secret prompts.
- The proxy runner validates domain, email, password, SSH user, and private key inputs before connecting.
- The deployment writes `/opt/aegisnode/proxy/docker-compose.yml`, Pangolin application config, and Traefik config files, prepares persistent Pangolin and Traefik data directories, opens TCP/80, TCP/443, UDP/51820, and UDP/21820 in UFW, pulls and starts the Compose stack, and verifies Traefik, Pangolin, and Gerbil are running.
- Dashboard routing sends UI traffic to Pangolin port 3002 and API traffic under `/api/v1` to Pangolin port 3000.
- The generated Compose file follows Pangolin's manual community layout with Gerbil enabled, uses Traefik file/http providers, and avoids Docker provider socket access.
- Actual DNS propagation and Let's Encrypt issuance are live-environment checks and are not performed by automated tests.
- Automated verification: `go test ./...`.

### Phase 5 — Not started

- [ ] Harden sshd authentication and root access.
- [ ] Block external SSH while allowing tunnel access.
- [ ] Configure and verify the Pangolin client tunnel.

## Next implementation entry point

Phase 5 should harden sshd access, block external SSH after tunnel verification, and verify Pangolin client tunnel access.
