# Servestead

Servestead, the Server Homestead, is a local Go CLI for turning a raw Ubuntu VPS into a hardened, Git-backed place to run private application stacks. It supports DigitalOcean provisioning, administrative-user bootstrapping, operating-system hardening, Pangolin-backed ingress, and observability through native Go orchestration.

## Prerequisites

- Go 1.26.4 or newer to build the CLI
- A DigitalOcean API token when provisioning from Servestead
- A local ED25519 key pair for administrative access

`bootstrap`, `harden`, `network`, and `keygen` do not require local Ansible, OpenSSH, or `ssh-keygen` binaries. Remote bootstrap, hardening, and network configuration still assume a supported Ubuntu target with standard system tools such as `apt`, `sudo`, `systemctl`, `curl`, `gpg`, and `iptables`.

## Build

```sh
go build -o bin/servestead .
```

## Documentation site

The docs site lives in `docs/` and is built with Astro Starlight:

```sh
cd docs
npm install
npm run dev
```

Use `npm run build` from `docs/` to verify the static GitHub Pages output locally.

## Provision a VPS

The recommended path is the setup TUI:

```sh
bin/servestead setup
```

Choose **Provision a new DigitalOcean VPS**. The TUI prompts for a DigitalOcean token, Droplet name, and local Servestead key, then loads regions, sizes, Ubuntu images, and SSH keys from DigitalOcean. Size choices show monthly and hourly prices. Before creating anything billable, Servestead shows a review screen and requires an exact typed confirmation phrase. After the Droplet has a public IPv4 address, Servestead saves it as a profile and returns to the setup dashboard.

DigitalOcean requires an SSH public key before it can create a Droplet. Servestead can generate a provider login keypair:

```sh
bin/servestead keygen
```

The default key path is `$HOME/.ssh/servestead_ed25519`. The setup TUI can upload the matching public key to DigitalOcean when it is not already present.

The TUI can prompt for the DigitalOcean token. To avoid entering it each run, export it first:

```sh
export DIGITALOCEAN_ACCESS_TOKEN='...'
```

For direct CLI provisioning, add the public key to DigitalOcean first, then use the key ID or fingerprint with `--ssh-key`:

```sh
bin/servestead provision \
  --provider digitalocean \
  --name aegis-01 \
  --ssh-key 'provider-key-id-or-fingerprint'
```

Defaults target Ubuntu 24.04 in `nyc3` on `s-1vcpu-1gb` and can be overridden with `--region`, `--size`, and `--image`. Provisioning is billable and is not run by the test suite.

## Guided setup

For guided setup on an existing disposable Ubuntu VPS, lead with the server IP:

```sh
bin/servestead setup --ip 203.0.113.10
```

With `--ip`, Servestead creates or selects a saved profile, collects the missing full-run values up front, generates and stores the Pangolin server secret, checks local prerequisites, then runs bootstrap, hardening, Docker networking, and reverse proxy deployment as one setup plan. Interactive runs show a live terminal run view with task progress, current stage/task, and inline logs; `--yes` keeps script-friendly stdout/stderr output. Saved profiles live under the directory returned by `os.UserConfigDir()` in a `servestead` subdirectory. Each profile keeps metadata, run state, secrets, and JSONL run logs in separate files with owner-only permissions.

Profiles are keyed by generated profile IDs, so starting fresh for a reused IP preserves old profile data instead of overwriting it:

```sh
bin/servestead setup --ip 203.0.113.10 --fresh
```

When a fresh profile is created from an existing saved profile that already completed bootstrap, Servestead treats administrative access as already present and continues with the remaining setup stages using the saved admin user. This avoids trying to log in as `root` on hardened servers where root SSH has already been disabled.

For scripts or repeatable smoke tests, provide all upfront values explicitly:

```sh
bin/servestead setup \
  --ip 203.0.113.10 \
  --private-key "$HOME/.ssh/id_ed25519" \
  --domain example.com \
  --email admin@example.com \
  --yes
```

Running `setup` without `--ip` opens the full-screen, profile-first terminal UI. It lists saved profiles and can provision a new DigitalOcean VPS before setup. The provisioning path reads a DigitalOcean token, loads regions, sizes, Ubuntu images, and SSH keys from the API, displays hourly and monthly size prices, requires explicit confirmation, creates one billable Droplet, saves it as a profile, and returns to the dashboard. It does not bootstrap or harden automatically. Saved DigitalOcean profiles expose cloud actions from the dashboard: press `o` to restart or destroy the Droplet after confirmation; destroying a Droplet keeps the local profile, secrets, state, and logs.

Existing profile dashboards present three setup actions: Bootstrap, Harden, and Platform. Platform runs networking, Pangolin proxy, and observability in order from one command. The TUI collects missing full-run values before any remote command runs and presents explicit choices to create a local configuration repository, use an existing checkout, or clone GitHub. The review screen shows the selected repository action. After confirmation, Servestead prepares the repository first and starts SSH execution only after that succeeds. From a saved profile dashboard, use `j`/`k` to select an action and press `r` to run it once, even if it is already marked complete. Press `p` to reveal the saved Pangolin administrator username and password. Retrying Platform after Pangolin has already been registered opens masked administrator email/password inputs and saves the supplied credentials in the owner-only profile secrets file. Press `q` to quit from navigation or run screens, `esc` to go back, or `x` to delete only the local saved profile, secrets, state, and run logs; local profile delete does not change the remote server. The older one-off guided paths remain available from the advanced legacy setup entry.

For a quick preflight check without opening the TUI:

```sh
bin/servestead doctor
```

## Bootstrap administrative access

```sh
bin/servestead bootstrap \
  --host 203.0.113.10 \
  --admin-public-key "$HOME/.ssh/id_ed25519.pub" \
  --private-key "$HOME/.ssh/id_ed25519"
```

The first SSH connection uses a native trust-on-first-use host key policy similar to OpenSSH's `accept-new`: unknown host keys are added to `$HOME/.ssh/known_hosts`, and changed known host keys fail. Verify the host fingerprint through the provider console before running the command when the threat model requires out-of-band host verification. Root SSH access is intentionally left enabled until hardening has installed and verified administrative key access.

## Harden the server

```sh
bin/servestead harden \
  --host 203.0.113.10 \
  --private-key "$HOME/.ssh/id_ed25519"
```

The hardening runner targets the administrative user created by `bootstrap` and defaults to `--ssh-user servestead`. It validates the target is Ubuntu 22.04 or newer on Linux 5.15 or newer, applies pending package upgrades, configures a persistent `/swapfile` sized from detected RAM (2× below 2 GiB, 1× from 2–8 GiB, and 4 GiB above 8 GiB), disables root SSH login, disables SSH password and keyboard-interactive login, checks every sysctl key before applying `/etc/sysctl.d/99-vps-hardening.conf`, enables unattended upgrades, installs CrowdSec from its apt repository, installs the matching CrowdSec firewall bouncer for nftables or iptables, and ensures both services are running.

When logging in manually with the generated key, use the key path explicitly:

```sh
ssh -i "$HOME/.ssh/servestead_ed25519" servestead@203.0.113.10
```

## Configure Docker networking and UFW

```sh
bin/servestead network \
  --host 203.0.113.10 \
  --private-key "$HOME/.ssh/id_ed25519"
```

The network runner installs Docker from Docker's official Ubuntu apt repository, ensures the administrative SSH user has passwordless sudo, adds that user to the `docker` group for Docker commands without `sudo`, writes `/etc/docker/daemon.json` with Docker bridge firewall/NAT support enabled, enables IPv4 forwarding, injects Servestead-managed Docker masquerade translations into `/etc/ufw/before.rules`, preserves SSH access on the configured SSH port, sets UFW to deny incoming and routed traffic by default, explicitly allows HTTP/HTTPS ingress, allows routed traffic from the default Docker bridge networks, enables UFW, and restarts Docker. Apt operations wait up to 300 seconds for an existing dpkg frontend lock before failing.

Docker group membership applies to new login sessions. After `network` completes, disconnect and reconnect before running `docker ps` without `sudo`.

## Deploy Pangolin and the reverse proxy stack

After DNS records point the apex domain and wildcard subdomains to the VPS, deploy the Phase 4 Pangolin stack:

```sh
bin/servestead proxy \
  --host 203.0.113.10 \
  --private-key "$HOME/.ssh/id_ed25519" \
  --domain example.com \
  --email admin@example.com \
  --server-secret 'replace-with-a-long-random-secret'
```

The direct proxy command keeps `--server-secret` for scripts. Normal profile-aware setup generates and saves the Pangolin server secret, administrator password, Newt credentials, Beszel credentials, and Beszel key material. It creates the Pangolin administrator, `servestead` organization, and `local-vps` Newt site through Pangolin's API. The proxy stack uses pinned Pangolin, Gerbil, Traefik, Newt, and Docker socket proxy images. Newt continuously reconciles Pangolin resources from Compose labels through a read-only socket proxy.

The observability stage deploys the committed Compose file under `/opt/servestead/repository`, keeps runtime data in `/opt/servestead/stacks/observability`, preconfigures a local Beszel system, and deploys Beszel Hub, Beszel Agent, Dozzle, and Dockhand without public host ports. Beszel, Dozzle, and Dockhand are exposed as `beszel.<domain>`, `dozzle.<domain>`, and `dockhand.<domain>` through Pangolin SSO for the saved administrator, with Pangolin target health checks enabled. The stage pauses Newt while replacing the application containers so Docker events cannot race concurrent blueprint creation, reconciles those exact names and hostnames to the stable resource IDs `servestead-beszel`, `servestead-dozzle`, and `servestead-dockhand`, removes conflicting duplicates, restarts Newt for one complete scan, and verifies exactly one of each before completing. Servestead also creates or repairs Dockhand's `local-vps` environment through the dedicated Docker socket proxy, tests the connection, and verifies that Dockhand can list visible containers. Dockhand shell execution is enabled through that dedicated proxy; Dozzle container actions and shell access remain disabled.

Observability configuration is consumer-owned and Git-backed at `stacks/observability/compose.yaml`. By default, setup creates one repository per profile under the Servestead configuration directory, initializes `main`, and commits the scaffold as `Servestead <servestead@localhost>`. Use `--config-repo <path>` to select an existing checkout or `--github-repo <https-url>` to clone a GitHub HTTPS repository. Private repositories require a GitHub personal access token, and public repositories can optionally use one to avoid anonymous rate limits. Store a profile token with `bin/servestead github-token set --profile <profile-id> --file /path/to/token.txt`; `SERVESTEAD_GITHUB_TOKEN` remains available as an environment override for one-off runs.

Servestead deploys the exact committed `HEAD`. Uncommitted changes to the observability Compose file block deployment, while unrelated working-tree changes do not. If an existing checkout has no observability file, Servestead creates the scaffold and stops so it can be reviewed and committed. Observability secrets remain on the server in `/etc/servestead/observability.env`, and remote snapshot or checkout drift is rejected rather than overwritten. When the configuration repository has a GitHub origin and branch, stack synchronization also creates or updates matching Dockhand Git-stack records with automatic updates disabled, writes secret-backed stack environment values to Dockhand through `http://127.0.0.1:3003/api` over SSH stdin, checks whether Dockhand already reports the committed revision, and calls Dockhand's Git sync API only when reconciliation is needed. Servestead still performs the authoritative deployment.

## Add an application stack

Saved-profile dashboards show stacks detected in the profile configuration repository. Press `s` to open the standalone stack manager. From there, `a` opens a Compose file browser; press `/` when manual path entry is preferable. If editing the repository directly, place the Compose file at `stacks/<name>/compose.yaml`; setup shows it as a draft until metadata is reviewed and saved. After inspecting the file, the TUI lists every detected service as private by default. Select a service and press `enter` or `space` to configure a public route, use `a` to add another route for the selected service, then press `n` to choose no runtime secrets, use a detected adjacent `.env`, or browse for another file. The final review screen saves local repository files, including any encrypted `servestead.secrets.yaml` file and the base repository scaffold when it is missing, so one config-repository review and commit covers the full import. Existing stacks retain the route editor: `e` edits metadata, `ctrl+s` saves an edit, `d` removes the stack after confirmation, and `r` deploys only the selected committed stack. Repository actions are also available without leaving the TUI: `v` views staged, unstaged, and untracked diffs; `g` stages all changes under `stacks/`; `c` commits the staged stack changes with a supplied message; and `p` pushes the current branch when an `origin` remote is configured. The manager reports `commit required`, `push required`, `sync required`, or `in sync`. Press `y` to synchronize the committed repository with the server. Synchronization deploys every current stack and removes containers, generated overrides, deployment manifests, and Pangolin resources for stacks deleted from Git. The direct command remains available for scripted imports:

```sh
bin/servestead stack add \
  --profile <profile-id> \
  --compose /path/to/docker-compose.yml \
  --publish web:3000:app \
  --publish api:8080:api \
  --env-file /path/to/.env
```

`--publish` is repeatable and uses `service:port:subdomain[:id]`. The optional ID is required when one service has more than one public route. Omitting every `--publish` creates a private stack.

The terminal UI separates deployed services from public exposure: every Compose service deploys, but only explicitly selected services receive Pangolin routes. Each route controls its service, port, public subdomain, stable ID, display name, protocol, health check, and SSO setting. Servestead copies the original Compose file to `stacks/<name>/compose.yaml` and writes the reviewed public-resource contract to `stacks/<name>/servestead.yaml`. It does not inject labels into the consumer-owned Compose file.

Use `/data/<stack>/...` for application bind mounts in standalone Compose files. Servestead creates `/data` as a root-owned base directory and creates `/data/<stack>` with owner `1000:1000` the first time a stack deploys, matching common non-root images such as Node-based containers. If an image runs as another UID/GID, create or chown the specific `/data/<stack>` subdirectory for that image before deployment, or use a Docker named volume instead. Do not bind-mount writable application data from `/opt/servestead/repository`; that checkout is deployment input, not runtime state.

Runtime secret values imported from `.env` files are stored in `stacks/<name>/servestead.secrets.yaml` as SOPS-compatible age-encrypted Git state, with provider and key metadata in `stacks/<name>/servestead.yaml`. Servestead creates the profile stack secret identity automatically on first import, or explicitly with `servestead secrets init --profile <profile-id>`. Back it up with `servestead secrets export-key --profile <profile-id>` and restore it with `servestead secrets import-key --profile <profile-id> --file <path>`. If Servestead is unavailable, export the key to a file and decrypt with SOPS using `SOPS_AGE_KEY_FILE=/path/to/key.txt sops -d stacks/<name>/servestead.secrets.yaml`. Commit a `.env.example` when required keys need documentation, not the populated file. Deployment decrypts values locally, exports them only for the remote Compose task, sends them to Dockhand's localhost API over SSH stdin as secret env vars, and does not write populated stack `.env` files to the remote host. Only variable names are shown in the TUI and CLI. The Compose file must explicitly consume values through environment-variable references. Update or remove secrets without editing the stack by hand:

```sh
bin/servestead stack env set --profile <profile-id> --stack <name> --file /path/to/.env
bin/servestead stack env remove --profile <profile-id> --stack <name>
```

During deployment, Servestead generates an override that:

- Connects every published service to the external `servestead-public` network.
- Adds stable Pangolin resource, target, SSO, and health-check labels.
- Removes direct host port publishing from published services so Pangolin remains the public entry point.
- Validates the merged Compose model before stopping or replacing containers.
- Restarts Newt and verifies that Pangolin created exactly one expected public resource.

Review and commit the generated stack files, then select that stack in the TUI and press `r`. Servestead deploys and reports each stack independently, and deploys committed stack configuration only.

DNS registrar changes remain external. Create records for `pangolin.<domain>`, `beszel.<domain>`, and `dozzle.<domain>` pointing to the VPS. Traefik uses HTTP-01 to issue a separate certificate for each hostname, so TCP port 80 must remain reachable.

Retrieve the generated Pangolin administrator credentials:

```sh
bin/servestead pangolin-credentials --profile <profile-id>
bin/servestead pangolin-credentials --ip 203.0.113.10
```

For an existing registered Pangolin profile created before automated bootstrap, set `PANGOLIN_ADMIN_PASSWORD` once when rerunning setup so Servestead can save the existing password in the owner-only profile secrets file.
