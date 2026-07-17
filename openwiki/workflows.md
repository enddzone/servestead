---
type: Workflow Guide
title: Servestead Server Lifecycle Workflows
description: End-to-end Servestead server lifecycle from DigitalOcean provisioning through bootstrap, Ubuntu hardening, Docker and UFW networking, Pangolin proxy deployment, observability, and application stack synchronization over SSH.
tags: [servestead, workflows, provisioning, hardening, docker, pangolin]
---

# Server Lifecycle Workflows

Servestead transforms a raw Ubuntu VPS into a fully managed platform through a sequence of stages. Each stage is a set of SSH-executed tasks built from embedded shell scripts and config templates. The stages run in order: **provision → bootstrap → harden → network → proxy → observability → stacks**.

## How Stages Execute

Each stage produces a `[]Task` list (defined in `backend/tasks.go`). `runTasksWithReporter()` iterates the list, wrapping each task's shell script in `privilegedCommand(sshUser, task.Apply)` and executing it via `sshRemoteClient.Run()` or `RunWithStdin()`. Task events (`run_started`, `task_started`, `log_line`, `task_succeeded`/`task_failed`, `run_completed`) are emitted to the active `TaskReporter` (TUI, web SSE, or JSONL log).

Scripts and config files are embedded at compile time (`backend/resources/`) and rendered as Go `text/template` files at runtime with profile-specific values via `mustRenderResourceTemplate()`.

## 1. Provision (DigitalOcean)

**Source:** `backend/cloud.go`

Creates a DigitalOcean Droplet and waits for a public IPv4 address.

| Aspect | Detail |
|---|---|
| Interface | `cloudProvider` (line 86): `Catalog`, `Create`, `CreateSSHKey`, `Reboot`, `Destroy` |
| Implementation | `digitalOceanProvider` (line 94): wraps `godo` SDK |
| Defaults | Region `nyc3`, size `s-1vcpu-1gb`, image `ubuntu-24-04-x64` |
| Flow | `Create()` (line 140) → builds `godo.DropletCreateRequest` → `droplets.Create` → polls `waitForIPv4` (line 189) until `active` |
| Token | `DIGITALOCEAN_ACCESS_TOKEN` or `DIGITALOCEAN_TOKEN` env var |
| Timeout | Default 5 minutes (configurable via `--timeout`) |

The TUI provisioning wizard (`backend/provision_tui.go`) loads the DO catalog (regions, sizes, Ubuntu images, SSH keys), displays prices, requires typed confirmation, then creates the Droplet and saves it as a profile.

**Keygen** (`backend/keygen.go`): Generates an ED25519 keypair at `$HOME/.ssh/servestead_ed25519` (private `0600`, public `0644`) using `ed25519.GenerateKey`. The key is unencrypted (no passphrase) for non-interactive SSH. Can upload the public key to DigitalOcean via `CreateSSHKey`.

## 2. Bootstrap

**Source:** `backend/bootstrap.go`, `backend/resources/bootstrap/`

Creates a passwordless-sudo admin user on the raw VPS. The first SSH connection uses `root` (or a specified initial user) with a trust-on-first-use host key policy.

**`bootstrapConfig`** (line 15): `Host`, `SSHUser` (default `root`), `AdminUser` (default `servestead`), `AdminPublicKeyPath`, `PrivateKeyPath`.

**Tasks** (`bootstrapTasks`, line 89):
1. Install packages: `curl`, `git`, `gnupg2`, `sudo`
2. Create group + user: `groupadd`, `useradd --create-home --shell /bin/bash`, add to `sudo` group, lock password (`passwd -l`)
3. Configure passwordless sudo: writes `/etc/sudoers.d/<admin>` via `write-sudoers.sh.tmpl`, validates with `visudo -cf` before moving
4. Create `~/.ssh` directory (`install -d -m 0700`)
5. Install `authorized_keys` with the admin ED25519 public key (`0600`)

**Enforces ED25519 only** — rejects non-ED25519 public keys (line 56).

Root SSH access is intentionally left enabled until hardening verifies admin key access.

## 3. Harden

**Source:** `backend/hardening.go`, `backend/resources/hardening/`

**`hardeningTasks`** (line 72) — 13 tasks in order:

| # | Task | Details |
|---|---|---|
| 1 | Validate Ubuntu | `supported-ubuntu.sh` — checks `$ID == ubuntu`, version ≥ 22.04, kernel ≥ 5.15 |
| 2 | Validate sysctl keys | Verifies all sysctl keys are known before writing |
| 3 | System upgrade | `system-upgrade.sh.tmpl` — `apt-get update`, `full-upgrade`, `autoremove` (noninteractive, `NEEDRESTART_MODE=a`) |
| 4 | Install prerequisites | `apt-transport-https`, `ca-certificates`, `curl`, `gnupg`, `iptables`, `unattended-upgrades` |
| 5 | Configure swap | `configure-swap.sh` — <2 GiB RAM → 2× RAM; ≤8 GiB → 1× RAM; else 4 GiB. Creates `/swapfile` via `fallocate` (fallback `dd`), adds to `/etc/fstab` |
| 6 | SSH hardening config | `sshd-hardening.conf` — `PermitRootLogin no`, `PasswordAuthentication no`, `KbdInteractiveAuthentication no`, `PubkeyAuthentication yes` |
| 7 | Validate & reload SSH | `reload-ssh.sh` — validates `sshd -t` then reloads |
| 8 | Sysctl config | `sysctl.conf.tmpl` — 16 settings: `rp_filter=1`, `accept_source_route=0`, `accept_redirects=0`, `tcp_syncookies=1`, `dmesg_restrict=1`, `unprivileged_bpf_disabled=1`, `vm.swappiness=10`, etc. |
| 9 | Reload sysctl | `sysctl --system` |
| 10 | Enable unattended upgrades | `20auto-upgrades` with `Update-Package-Lists "1"` and `Unattended-Upgrade "1"` |
| 11–12 | CrowdSec keyring + repo | Downloads GPG key from `packagecloud.io`, dearmors, writes apt source |
| 13 | Install CrowdSec + bouncer | Installs `crowdsec`, enables service; detects nftables vs iptables, installs matching bouncer; enables it |

## 4. Network (Docker + UFW)

**Source:** `backend/network.go`, `backend/resources/network/`

**`networkTasks`** (line 88) — installs Docker and configures the UFW firewall with Docker bridge NAT support:

| # | Task | Details |
|---|---|---|
| 1 | Validate Ubuntu | `supported-ubuntu.sh` |
| 2 | Install prerequisites | `ca-certificates`, `curl`, `gnupg`, `ufw` |
| 3 | Remove conflicting Docker packages | `remove-conflicting-docker-packages.sh.tmpl` |
| 4 | Configure Docker keyring | Downloads Docker GPG key to `/etc/apt/keyrings/docker.asc` |
| 5 | Configure Docker repository | `docker-repository.sh.tmpl` — writes `/etc/apt/sources.list.d/docker.sources` |
| 6 | Install Docker | `docker-ce`, `docker-ce-cli`, `containerd.io`, `docker-compose-plugin` |
| 7 | Admin to docker group | (Non-root only) Ensures sudo + adds admin user to `docker` group |
| 8 | Docker daemon config | `docker-daemon.json` — `overlay2` driver, `json-file` log driver (50 MB × 3), `iptables: true`, `ip-forward-no-drop: true`, `no-new-privileges: true` |
| 9 | Enable IPv4 forwarding | `net.ipv4.ip_forward = 1` + `sysctl --system` |
| 10 | UFW masquerade | `ufw-masquerade.sh.tmpl` — injects NAT `MASQUERADE` into `/etc/ufw/before.rules` for Docker bridge subnets `172.17.0.0/16` and `172.18.0.0/16` |
| 11 | UFW policy | Allow SSH port (auto-detected), `deny incoming`, `allow outgoing`, `deny routed`, open 80/443, allow routed Docker traffic, `ufw --force enable` |
| 12 | Restart Docker | `systemctl enable docker`, `systemctl restart docker`, verify with `docker info` |

Docker group membership applies to new sessions — reconnect before running `docker ps` without `sudo`.

## 5. Proxy (Pangolin Ingress Stack)

**Source:** `backend/proxy.go`, `backend/resources/proxy/`

Deploys a 5-container Docker Compose stack at `/opt/servestead/proxy/`:

| Container | Image | Role |
|---|---|---|
| **Pangolin** | `fosrl/pangolin:1.19.4` | Reverse proxy management dashboard/API; `127.0.0.1:3000` |
| **Gerbil** | `fosrl/gerbil:1.4.2` | WireGuard tunnel relay; UDP 51820/21820, 80/443 |
| **Traefik** | `traefik:v3.6.4` | Edge TLS terminator; shares Gerbil's network namespace |
| **Newt** | `fosrl/newt:1.13.0` | Docker-to-Pangolin agent; reads Docker socket via socket-proxy |
| **socket-proxy** | `tecnativa/docker-socket-proxy:v0.4.2` | Read-only Docker socket proxy for Newt |

**Deployment flow** (`proxyTasks`, line 152):
1. Validate config (domain regex, email, server secret, setup token format)
2. Auto-generate credentials: Pangolin setup token, admin password (32 chars), Newt ID (15 chars), Newt secret (48 chars)
3. Create directory tree under `/opt/servestead/proxy/config/`
4. Create the external `servestead-public` Docker network (shared with app stacks)
5. Write 4 config files: `config.yml`, `traefik_config.yml`, `dynamic_config.yml`, `docker-compose.yml`
6. Configure UFW: allow 80/443 TCP, 51820/21820 UDP, route/masquerade rules
7. `docker compose pull && down && up -d`
8. **Pangolin bootstrap** (`pangolinBootstrapCommand`, line 236): POSTs to Pangolin API to create admin user, log in, create a `local-vps` Newt site with generated credentials
9. Verify all 5 services running

**Networks**: `pangolin` bridge (`172.30.0.0/24`) for the proxy stack + external `servestead-public` for app stacks.

## 6. Observability

**Source:** `backend/observability.go`, `backend/resources/observability/`

Deploys a 5-container observability stack at `/opt/servestead/stacks/observability/`:

| Container | Image | Role |
|---|---|---|
| **Beszel** | `henrygd/beszel:0.18.7` | System metrics dashboard; trusted auth header, password disabled |
| **beszel-agent** | `henrygd/beszel-agent:0.18.7` | Host metrics collector; mounts `/proc`, `/sys` read-only |
| **Dozzle** | `amir20/dozzle:v10.6.6` | Real-time container log viewer; forward-proxy auth, actions/shell disabled |
| **Dockhand** | `fnsys/dockhand:latest` | Container management UI; also bound to `127.0.0.1:3003` for local API |
| **dockhand-socket-proxy** | tecnativa socket-proxy | Separate socket proxy for Dockhand with POST enabled |

**Deployment flow** (`observability.go` line 76):
1. Create directories for beszel_data, agent_keys, dockhand_data, `/etc/servestead/`
2. Write Beszel `config.yml`, Hub private/public keys
3. **Declarative mode** (if `RepositoryCommit` + `RepositoryCompose` set): use committed `stacks/observability/compose.yaml` with `/etc/servestead/observability.env`. **Generated mode**: render from template.
4. Reconcile Pangolin public resources for `beszel.<domain>`, `dozzle.<domain>`, `dockhand.<domain>` with Traefik labels and health checks
5. `docker compose pull && up -d`, restart Newt for a complete scan
6. Verify all services running + Pangolin resources exist
7. **Dockhand environment reconciliation**: create/update `local-vps` Dockhand environment pointing at `dockhand-socket-proxy:2375`, test connection, verify container visibility

**Stable resource IDs**: `servestead-beszel`, `servestead-dozzle`, `servestead-dockhand`. Newt is paused during container replacement to prevent race conditions.

**Auth model**: Beszel and Dozzle trust the reverse proxy (Pangolin/Traefik) to pass the authenticated user's email via headers — no separate login.

## 7. Application Stacks

**Source:** `backend/stack.go`, `backend/observability.go` (lines 142–226), `backend/resources/stacks/pangolin-override.yml.tmpl`

Stacks are Docker Compose applications managed through the config repository. Each stack lives at `stacks/<name>/` with:
- `compose.yaml` — the consumer-owned Docker Compose file
- `servestead.yaml` — metadata: public resources (routes), secrets metadata
- `servestead.secrets.yaml` — SOPS/age-encrypted runtime secrets (optional)

### Stack add flow
`runStackAdd` (`stack.go` line 465): Parses the compose file, detects services, lets the user configure public routes (service:port:subdomain:protocol:sso), writes files to the repo, commits.

### Override generation
`generateStackPangolinOverride` (`stack.go` line 1099): Generates a Docker Compose override that:
- Resets host ports (`ports: !reset []`) for published services — traffic flows through Pangolin
- Joins the `servestead-public` network
- Adds Pangolin labels: `pangolin.public-resources.servestead-<stack>-<id>.full-domain`, `.protocol`, `.targets[0].hostname`, `.targets[0].port`, `.auth.sso-enabled`, `.auth.sso-users[0]`

Newt reads these labels from Docker and registers them as Pangolin resources.

### Remote deployment
For each configured stack (`observability.go` line 142):
1. Deploy stack files to `/opt/servestead/repository/stacks/<name>/` (snapshot mode) or use Git checkout
2. Create `/data/<stack>` owned by `1000:1000`
3. Write generated override to `/opt/servestead/generated/<name>.pangolin.yaml`
4. `docker compose -p servestead-<name> -f <compose> -f <override> up -d --remove-orphans`
5. Stop/start Newt around compose up so Pangolin picks up label changes
6. Validate compose config + verify Pangolin public resources exist via API
7. Reconcile Dockhand Git stack + push secret env vars (declarative mode)

### Cleanup
`removedStackCleanupTask` (`observability.go` line 300): Detects stacks on the remote that no longer exist in config, removes containers/networks/files, and deletes their Pangolin resources.

## Platform Stage

The TUI offers a **Platform** action that runs network → proxy → observability in sequence from a single command. This is the typical path after bootstrap+harden are complete.

## Full Run with `--ip`

When `setup --ip` is used, Servestead can run all stages as one plan:
1. Create or select a saved profile for the IP
2. Collect missing values (SSH key, domain, email)
3. Generate and store the Pangolin server secret
4. Run preflight checks (`doctor`)
5. Execute bootstrap → harden → network → proxy → observability → stacks sequentially
6. Interactive runs show live terminal output; `--yes` keeps script-friendly stdout/stderr

The `--fresh` flag creates a new profile for a reused IP, preserving old profile data. When a fresh profile is created from a profile that already completed bootstrap, Servestead skips root login and continues with the saved admin user.
