# Implementation Progress

Last updated: 2026-06-25

Source of truth for planned work: `implementation_plan.html`.

## Current status

- Overall blueprint: **9 of 15 tasks complete (60%)**
- Phase 1 — Local CLI Coordinator & VPS Bootstrapping: **complete**
- Phase 2 — Operating System & Kernel Hardening: **complete**
- Phase 2.5 — Guided Live-Test UX: **complete**
- Phase 3 — Resolving Docker & UFW Conflict: **complete**
- Phases 4–5: **not started**

The interactive HTML checklist uses browser-local storage and was not edited. This file records repository implementation progress independently of browser state.

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
- The network runner writes `/etc/docker/daemon.json` with `"iptables": false`, ensures the administrative SSH user has passwordless sudo and Docker group membership, enables IPv4 forwarding, replaces only the AegisNode-managed UFW NAT block, preserves SSH access on the configured SSH port, denies incoming and routed traffic by default, allows HTTP/HTTPS ingress, allows routed traffic from Docker bridge CIDRs, enables UFW, and restarts Docker.
- Added a guided setup path for Docker networking and UFW without adding the step to baseline hardening.
- Automated verification: `go test ./...`, `go test -race ./...`, `go vet ./...`, and `go build -o /tmp/aegisnode .`.

### Phase 4 — Not started

- [ ] Deploy Traefik, Gerbil, Pangolin, and PostgreSQL.
- [ ] Configure required DNS records.
- [ ] Start the stack and verify certificate issuance.

### Phase 5 — Not started

- [ ] Harden sshd authentication and root access.
- [ ] Block external SSH while allowing tunnel access.
- [ ] Configure and verify the Pangolin client tunnel.

## Next implementation entry point

Phase 4 should deploy Traefik, Gerbil, Pangolin, and PostgreSQL, configure required DNS records, start the stack, and verify certificate issuance.
