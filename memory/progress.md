# Implementation Progress

Last updated: 2026-06-23

Source of truth for planned work: `implementation_plan.html`.

## Current status

- Overall blueprint: **3 of 15 tasks complete (20%)**
- Phase 1 — Local CLI Coordinator & VPS Bootstrapping: **complete**
- Phases 2–5: **not started**

The interactive HTML checklist uses browser-local storage and was not edited. This file records repository implementation progress independently of browser state.

## Phase checklist

### Phase 1 — Complete

- [x] Define the Go coordinator architecture and embed Ansible YAML playbooks.
- [x] Read cloud API credentials from the environment and create Ubuntu instances on Hetzner or DigitalOcean.
- [x] Bootstrap a target through its initial SSH user and install an administrative ED25519 public key.

Verification completed on 2026-06-23:

- `go test -race ./...`
- `go vet ./...`
- `go build -o /tmp/aegisnode .`
- CLI help smoke test
- Provider calls tested against local HTTP servers; no billable cloud resource was created.
- Embedded playbook extraction and permissions tested.
- Ansible command arguments and JSON variable encoding tested.

Runtime Ansible execution remains environment-dependent because `ansible-playbook` is not installed in the development environment. A live bootstrap also requires a real Ubuntu target and is intentionally not part of automated tests.

### Phase 2 — Not started

- [ ] Deploy `/etc/sysctl.d/99-vps-hardening.conf`.
- [ ] Configure unattended upgrades.
- [ ] Install and register CrowdSec.

### Phase 3 — Not started

- [ ] Configure Docker daemon packet-filter behavior.
- [ ] Add the required UFW forwarding/NAT rules.
- [ ] Establish the default-deny UFW policy and explicit routes.

### Phase 4 — Not started

- [ ] Deploy Traefik, Gerbil, Pangolin, and PostgreSQL.
- [ ] Configure required DNS records.
- [ ] Start the stack and verify certificate issuance.

### Phase 5 — Not started

- [ ] Harden sshd authentication and root access.
- [ ] Block external SSH while allowing tunnel access.
- [ ] Configure and verify the Pangolin client tunnel.

## Next implementation entry point

Phase 2 should add a new embedded playbook rather than expanding `bootstrap.yml`. Before applying sysctl values, validate every setting against the target Ubuntu release and kernel; do not copy the blueprint snippet without checking compatibility. Add idempotence-oriented Ansible checks and Go tests confirming the new playbook is embedded.
