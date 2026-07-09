# Domain Concepts

This page covers the core conceptual domains of Servestead: profiles, the config repository, Pangolin ingress, observability, application stacks, secrets, and cloud integration.

## Profiles

Profiles are the persistence layer for managed servers. Each profile tracks everything Servestead needs to operate on a VPS: connection details, credentials, run state, and Git repository location.

**Data model** (`backend/profile.go`):
- `Profile` (line 37): `ID`, `Name`, `IP`, `InitialSSHUser` (default `root`), `AdminUser` (default `servestead`), `PrivateKeyPath`, `BaseDomain`, `LetsEncryptEmail`, `PangolinAdminEmail`, `ConfigRepositoryPath`, `Cloud *ProfileCloud`, timestamps.
- `ProfileCloud` (line 53): `Provider`, `ResourceID`, `Name`, `Region`, `Size`, `Image`, `PriceMonthly`, `PriceHourly`, `CreatedAt`, `DestroyedAt`.
- `ProfileState` (line 74): `ActiveRunID`, `StackRepositoryCommit`, `Runs` map.
- `ProfileSecrets` (line 95): `ServerSecret`, `PangolinSetupToken`, `PangolinAdminPassword`, `NewtID`, `NewtSecret`, `BeszelAdminPassword`, `BeszelSystemToken`, `BeszelHubPrivateKey`/`PublicKey`, `GitHubToken`, `StackSecretIdentity`/`Recipient`. Each secret has an `Ensure*` method that generates it on first access (e.g., `EnsureServerSecret` generates a 32-byte base64url secret).

**Storage** (`fileProfileStore`, line 270):
- Root: `$XDG_CONFIG_HOME/servestead/profiles/` (or `~/.config/servestead/profiles/`)
- Per-profile directory at `0700` with: `profile.json`, `state.json`, `secrets.json` (`0600`), `logs/<runID>.jsonl` (`0600`)
- IDs are generated from sanitized IP + UTC timestamp, so reusing an IP creates a new profile rather than overwriting
- All writes are atomic (temp file → fsync → rename → `chmod 0600`)
- Path traversal protection: IDs validated against `storePathComponentPattern`

**Cloud actions** (`backend/profile_cloud.go`): From the TUI or web UI, saved DigitalOcean profiles expose restart (`r`) and destroy (`d`) actions. Destroying a Droplet retains the local profile, secrets, state, and logs — only `Cloud.DestroyedAt` is set. Confirmation requires typing `"<action> <resourceID>"`.

## Config Repository & GitOps

Each profile has a Git configuration repository that holds consumer-owned deployment definitions.

**Repository model** (`backend/config_repository.go`):
- `configRepositoryRevision` (line 33): `Path`, `Commit`, `Branch`, `Compose`, `ComposeSHA`, `Origin`, `Stacks []repositoryStack`.
- Default path: `$XDG_CONFIG_HOME/servestead/repositories/<profile-id>/`
- Can be a local init, an existing checkout (`--config-repo`), or a GitHub clone (`--github-repo`).

**Repository structure**:
```
stacks/
  observability/
    compose.yaml          # Observability stack definition
  <name>/
    compose.yaml          # Application Docker Compose file
    servestead.yaml       # Stack metadata: public resources, secrets metadata
    servestead.secrets.yaml  # SOPS/age-encrypted secrets (optional)
```

**Initialization flow** (`prepareConfigRepository`, line 179):
1. Resolve path
2. Clone from GitHub or `git init -b main` if path doesn't exist
3. Write managed observability compose scaffold if missing or outdated
4. If scaffold changed on existing repo, return `errRepositoryReviewRequired` — user must review and commit
5. Auto-commit with author "Servestead \<servestead@localhost\>"
6. Read HEAD commit, branch, origin, and all committed stacks

**Stack loading from Git** (`loadCommittedStacks`, line 483): `git ls-tree -d --name-only HEAD:stacks`, then for each directory loads compose + metadata via `git show HEAD:<path>`, validates, computes SHA-256.

**Remote deployment** (`observabilityRepositoryTask`, `observability.go` line 712):
- **Snapshot mode** (no Git origin): writes files directly + a `.deployment` manifest with SHA-256 for drift detection
- **Git mode**: fetches/clones the repo, checks out the exact commit in detached HEAD, verifies compose SHA matches expected. Refuses to overwrite a dirty checkout or mismatched origin.

**GitHub authentication** (`config_repository.go` line 615):
- Uses `GIT_ASKPASS` with a temp script that outputs `x-access-token` as username and the token as password
- `GIT_TERMINAL_PROMPT=0` to prevent hangs
- Token source: `SERVESTEAD_GITHUB_TOKEN` env var (preferred) or `ProfileSecrets.GitHubToken`
- CLI: `github-token set/status/remove`

**Security**: Only uses git from trusted paths (`/usr/bin/git`, `/usr/local/bin/git`, `/opt/homebrew/bin/git`). Sanitizes PATH to `/usr/local/bin:/usr/bin:/bin:/opt/homebrew/bin`.

**Servestead deploys the exact committed `HEAD`** — uncommitted changes to the observability Compose file block deployment, while unrelated working-tree changes do not.

## Pangolin Ingress Model

Pangolin is the ingress management layer that sits between Docker containers and the public internet. Servestead deploys it as part of the proxy stack.

### Architecture

```
Internet → Gerbil (WireGuard tunnel, 80/443) → Traefik (TLS termination) → Container
                                                        ↑
                                                    Pangolin (config API)
                                                        ↑
                                                    Newt (Docker agent)
                                                        ↑
                                                    socket-proxy (read-only Docker socket)
```

- **Gerbil** handles WireGuard tunneling and exposes ports 80/443
- **Traefik** shares Gerbil's network namespace and handles TLS termination with HTTP-01 Let's Encrypt certificates
- **Pangolin** provides the management dashboard/API and configures Traefik dynamic config
- **Newt** continuously reconciles Docker Compose labels into Pangolin resources — it reads the Docker socket through a read-only `tecnativa/docker-socket-proxy`

### Public resources (routes)
Newt reads labels like `pangolin.public-resources.servestead-<stack>-<id>.full-domain=<sub>.<domain>` from Docker containers and registers them as Pangolin public resources. Each resource controls:
- **Full domain** (subdomain + base domain)
- **Protocol** (`http`, `tcp`, `udp`, `ssh`, `rdp`, `vpc`)
- **Target** (hostname = container service name, port)
- **SSO** (enabled/disabled, allowed users)
- **Health check** (optional)

### Credentials
Servestead auto-generates and stores:
- Pangolin admin email + password (in `ProfileSecrets`)
- Pangolin setup token (32 lowercase alphanumeric chars)
- Newt ID + secret (for the `local-vps` site)

Retrieve with `servestead pangolin-credentials --profile <id>` or `--ip <ipv4>`.

### DNS requirements
Create A records for `pangolin.<domain>`, `beszel.<domain>`, `dozzle.<domain>`, and any stack subdomains pointing to the VPS. Traefik uses HTTP-01 challenges, so port 80 must remain reachable.

## Observability Stack

Servestead deploys three observability tools behind Pangolin SSO:

| Tool | Purpose | Subdomain | Auth |
|---|---|---|---|
| **Beszel** | System metrics (CPU, RAM, disk, network) | `beszel.<domain>` | Trusted auth header (`Remote-Email`) |
| **Dozzle** | Real-time container log viewer | `dozzle.<domain>` | Forward-proxy auth |
| **Dockhand** | Container management UI + deployment API | `dockhand.<domain>` | Pangolin SSO |

**Key design decisions**:
- Beszel and Dozzle trust the reverse proxy to pass the authenticated user's email — no separate login
- Dozzle container actions and shell access are disabled
- Dockhand has a separate socket proxy with POST enabled (for container management) and is also bound to `127.0.0.1:3003` for local API access over SSH
- The observability Compose file is consumer-owned and Git-backed at `stacks/observability/compose.yaml`
- Newt is paused during container replacement to prevent race conditions with concurrent blueprint creation

**Stable resource IDs**: `servestead-beszel`, `servestead-dozzle`, `servestead-dockhand` — these are reconciled to exact container names and hostnames, with conflicting duplicates removed.

## Application Stacks & Public Routes

Application stacks are Docker Compose applications managed through the config repository. The terminal UI and web UI both support stack CRUD, Git operations, and deployment.

### Stack structure
Each stack at `stacks/<name>/`:
- `compose.yaml` — the original Docker Compose file (consumer-owned, not modified by Servestead)
- `servestead.yaml` — reviewed metadata: `public_resources` list (id, service, name, subdomain, port, protocol, sso, healthcheck) + `secrets` metadata
- `servestead.secrets.yaml` — SOPS/age-encrypted runtime secrets (optional)

### Public vs. private services
Every Compose service deploys, but only explicitly selected services receive Pangolin routes. Each route controls: service, port, public subdomain, stable ID, display name, protocol, health check, and SSO setting. Multiple routes per service are supported (each needs a unique ID).

### Override generation
Servestead generates a Docker Compose override (`pangolin-override.yml.tmpl`) that:
- Resets host port publishing for published services (`ports: !reset []`)
- Joins the `servestead-public` external network
- Adds Pangolin resource labels

The override is deployed to `/opt/servestead/generated/<name>.pangolin.yaml` — it is not committed to the config repo.

### Data directories
Use `/data/<stack>/...` for application bind mounts. Servestead creates `/data` as root-owned and `/data/<stack>` with owner `1000:1000` on first deploy. For images running as other UIDs, pre-create or chown the directory, or use Docker named volumes. Do not bind-mount writable data from `/opt/servestead/repository` — that checkout is deployment input, not runtime state.

### Stack sync
`y` (TUI) or the sync command deploys all committed stacks and removes containers, generated overrides, deployment manifests, and Pangolin resources for stacks deleted from Git. The manager reports `commit required`, `push required`, `sync required`, or `in sync`.

### Dockhand Git stack integration
When the config repo has a GitHub origin and branch, stack synchronization also creates or updates Dockhand Git-stack records with automatic updates disabled, writes secret-backed stack environment values to Dockhand through `http://127.0.0.1:3003/api` over SSH stdin, and calls Dockhand's Git sync API only when reconciliation is needed. Servestead still performs the authoritative deployment.

## SOPS + age Secrets

Stack runtime secrets are encrypted at rest using [SOPS](https://github.com/getsops/sops) with [age](https://github.com/FiloSottile/age) encryption.

**Identity management** (`backend/secrets.go`):
- `generateStackSecretIdentity` (line 524): Generates an age X25519 identity using `filippo.io/age`
- `ProfileSecrets.EnsureStackSecretIdentity` (line 532): Lazily creates and persists the age identity
- CLI: `secrets init` (create), `secrets export-key` (backup), `secrets import-key` (restore), `secrets status`

**Encryption flow** (`encryptSOPSStackSecrets`, line 329):
1. Converts age recipient strings to SOPS `MasterKey`s
2. Builds a `sops.Tree` with `EncryptedRegex: ".*"`, the key group, SOPS version `3.13.2`
3. Generates a data key (wrapped by age), encrypts with AES cipher, computes MAC
4. Emits as YAML

**Decryption flow** (`decryptSOPSStackSecrets`, line 374):
1. Loads encrypted YAML
2. Sets `SOPS_AGE_KEY` env var (mutex-protected)
3. Retrieves data key, decrypts tree, verifies MAC

**Runtime delivery**:
- At deploy time, decrypted values are injected as environment variables via SSH stdin
- `stackSecretEnvironmentPrelude` generates a Python script that reads JSON from stdin, validates key names match `^[A-Za-z_][A-Za-z0-9_]*$`, and exports them
- If Dockhand Git stacks are enabled, secrets are also pushed to Dockhand's variable store via `http://127.0.0.1:3003/api`
- No plaintext `.env` files are written to the remote host
- The Compose file must explicitly consume values through environment-variable references

**Manual decryption** (if Servestead is unavailable):
```sh
SOPS_AGE_KEY_FILE=/path/to/key.txt sops -d stacks/<name>/servestead.secrets.yaml
```

**CLI commands**:
```sh
servestead secrets init --profile <id>
servestead secrets status --profile <id>
servestead secrets export-key --profile <id>
servestead secrets import-key --profile <id> --file <path>
servestead stack env set --profile <id> --stack <name> --file /path/to/.env
servestead stack env remove --profile <id> --stack <name>
```

## DigitalOcean Cloud Integration

**Source**: `backend/cloud.go`, `backend/profile_cloud.go`

Servestead integrates with DigitalOcean via the `godo` SDK for:
- **Provisioning**: Create Droplets with SSH keys, poll for IPv4
- **Catalog**: Paginate regions, sizes, Ubuntu images, SSH keys
- **SSH key upload**: `CreateSSHKey` uploads the generated public key
- **Reboot**: `DropletActions.Reboot`
- **Destroy**: Delete Droplet by numeric ID (local profile retained)

Token: `DIGITALOCEAN_ACCESS_TOKEN` or `DIGITALOCEAN_TOKEN` env var.

The `cloudProvider` interface (line 86) is designed for potential future provider extensions. Currently only `digitalOceanProvider` is implemented.

## Example: arr-vpn Stack

**Source**: `examples/arr-vpn/`

A reference Docker Compose stack demonstrating a VPN-gated media stack:
- **gluetun** (`qmcgaw/gluetun`) — WireGuard VPN gateway (AirVPN), `NET_ADMIN` cap, `/dev/net/tun`
- **qbittorrent** (`linuxserver/qbittorrent`) — Torrent client routed through gluetun (`network_mode: service:gluetun`)
- **sonarr** (`linuxserver/sonarr`) — TV show manager, port 8989
- **radarr** (`linuxserver/radarr`) — Movie manager, port 7878

The `.env.example` shows the required WireGuard credential variables (private key, preshared key, address, server country, forwarded port).
