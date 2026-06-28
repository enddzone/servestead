# AegisNode

AegisNode is a local Go CLI for provisioning and hardening Ubuntu VPS instances. It supports Hetzner and DigitalOcean provisioning, administrative-user bootstrapping, and operating-system hardening through a native Go SSH runner.

## Prerequisites

- Go 1.24.2 or newer to build the CLI
- An existing SSH key registered with the selected cloud provider
- A local ED25519 key pair for administrative access

`bootstrap`, `harden`, `network`, and `keygen` do not require local Ansible, OpenSSH, or `ssh-keygen` binaries. Remote bootstrap, hardening, and network configuration still assume a supported Ubuntu target with standard system tools such as `apt`, `sudo`, `systemctl`, `curl`, `gpg`, and `iptables`.

## Build

```sh
go build -o bin/aegisnode .
```

## Provision a VPS

Credentials are read from the environment so they do not appear in shell process listings.

Cloud providers require an SSH public key before they can create a server. AegisNode can generate a provider login keypair and print the public key to copy into the provider UI:

```sh
bin/aegisnode keygen
```

The default key path is `$HOME/.ssh/aegisnode_ed25519`. After adding the printed public key to Hetzner or DigitalOcean, use the provider's key name, ID, or fingerprint with `--ssh-key`, and use the generated private key for setup and manual login.

Hetzner:

```sh
export HETZNER_API_TOKEN='...'
bin/aegisnode provision \
  --provider hetzner \
  --name aegis-01 \
  --ssh-key my-provider-key
```

DigitalOcean:

```sh
export DIGITALOCEAN_ACCESS_TOKEN='...'
bin/aegisnode provision \
  --provider digitalocean \
  --name aegis-01 \
  --ssh-key 'provider-key-id-or-fingerprint'
```

Provider defaults target Ubuntu 24.04 and can be overridden with `--region`, `--size`, and `--image`. Provisioning is billable and is not run by the test suite.

## Guided setup

For guided setup on an existing disposable Ubuntu VPS, lead with the server IP:

```sh
bin/aegisnode setup --ip 203.0.113.10
```

With `--ip`, AegisNode creates or selects a saved profile, collects the missing full-run values up front, generates and stores the Pangolin server secret, checks local prerequisites, then runs bootstrap, hardening, Docker networking, and reverse proxy deployment as one setup plan. Interactive runs show a live terminal run view with task progress, current stage/task, and inline logs; `--yes` keeps script-friendly stdout/stderr output. Saved profiles live under the directory returned by `os.UserConfigDir()` in an `aegisnode` subdirectory. Each profile keeps metadata, run state, secrets, and JSONL run logs in separate files with owner-only permissions.

Profiles are keyed by generated profile IDs, so starting fresh for a reused IP preserves old profile data instead of overwriting it:

```sh
bin/aegisnode setup --ip 203.0.113.10 --fresh
```

When a fresh profile is created from an existing saved profile that already completed bootstrap, AegisNode treats administrative access as already present and continues with the remaining setup stages using the saved admin user. This avoids trying to log in as `root` on hardened servers where root SSH has already been disabled.

For scripts or repeatable smoke tests, provide all upfront values explicitly:

```sh
bin/aegisnode setup \
  --ip 203.0.113.10 \
  --private-key "$HOME/.ssh/id_ed25519" \
  --domain example.com \
  --email admin@example.com \
  --yes
```

Running `setup` without `--ip` opens the full-screen, profile-first terminal UI. It lists saved profiles and presents three setup actions: Bootstrap, Harden, and Platform. Platform runs networking, Pangolin proxy, and observability in order from one command. The TUI collects missing full-run values before any remote command runs and presents explicit choices to create a local configuration repository, use an existing checkout, or clone GitHub. The review screen shows the selected repository action. After confirmation, AegisNode prepares the repository first and starts SSH execution only after that succeeds. From a saved profile dashboard, use `j`/`k` to select an action and press `r` to run it once, even if it is already marked complete. Retrying Platform after Pangolin has already been registered opens masked administrator email/password inputs and saves the supplied credentials in the owner-only profile secrets file. Press `q` to quit from navigation or run screens, `esc` to go back, or `x` to delete only the local saved profile, secrets, state, and run logs; delete does not change the remote server. The older one-off guided paths remain available from the advanced legacy setup entry. Setup does not create billable cloud resources; use `provision` separately when you want the CLI to create a server.

For a quick preflight check without opening the TUI:

```sh
bin/aegisnode doctor
```

## Bootstrap administrative access

```sh
bin/aegisnode bootstrap \
  --host 203.0.113.10 \
  --admin-public-key "$HOME/.ssh/id_ed25519.pub" \
  --private-key "$HOME/.ssh/id_ed25519"
```

The first SSH connection uses a native trust-on-first-use host key policy similar to OpenSSH's `accept-new`: unknown host keys are added to `$HOME/.ssh/known_hosts`, and changed known host keys fail. Verify the host fingerprint through the provider console before running the command when the threat model requires out-of-band host verification. Root SSH access is intentionally left enabled until hardening has installed and verified administrative key access.

## Harden the server

```sh
bin/aegisnode harden \
  --host 203.0.113.10 \
  --private-key "$HOME/.ssh/id_ed25519"
```

The hardening runner targets the administrative user created by `bootstrap` and defaults to `--ssh-user aegisadmin`. It validates the target is Ubuntu 22.04 or newer on Linux 5.15 or newer, applies pending package upgrades, configures a persistent `/swapfile` sized from detected RAM (2× below 2 GiB, 1× from 2–8 GiB, and 4 GiB above 8 GiB), disables root SSH login, disables SSH password and keyboard-interactive login, checks every sysctl key before applying `/etc/sysctl.d/99-vps-hardening.conf`, enables unattended upgrades, installs CrowdSec from its apt repository, installs the matching CrowdSec firewall bouncer for nftables or iptables, and ensures both services are running.

When logging in manually with the generated key, use the key path explicitly:

```sh
ssh -i "$HOME/.ssh/aegisnode_ed25519" aegisadmin@203.0.113.10
```

## Configure Docker networking and UFW

```sh
bin/aegisnode network \
  --host 203.0.113.10 \
  --private-key "$HOME/.ssh/id_ed25519"
```

The network runner installs Docker from Docker's official Ubuntu apt repository, ensures the administrative SSH user has passwordless sudo, adds that user to the `docker` group for Docker commands without `sudo`, writes `/etc/docker/daemon.json` with Docker bridge firewall/NAT support enabled, enables IPv4 forwarding, injects AegisNode-managed Docker masquerade translations into `/etc/ufw/before.rules`, preserves SSH access on the configured SSH port, sets UFW to deny incoming and routed traffic by default, explicitly allows HTTP/HTTPS ingress, allows routed traffic from the default Docker bridge networks, enables UFW, and restarts Docker. Apt operations wait up to 300 seconds for an existing dpkg frontend lock before failing.

Docker group membership applies to new login sessions. After `network` completes, disconnect and reconnect before running `docker ps` without `sudo`.

## Deploy Pangolin and the reverse proxy stack

After DNS records point the apex domain and wildcard subdomains to the VPS, deploy the Phase 4 Pangolin stack:

```sh
bin/aegisnode proxy \
  --host 203.0.113.10 \
  --private-key "$HOME/.ssh/id_ed25519" \
  --domain example.com \
  --email admin@example.com \
  --server-secret 'replace-with-a-long-random-secret'
```

The direct proxy command keeps `--server-secret` for scripts. Normal profile-aware setup generates and saves the Pangolin server secret, administrator password, Newt credentials, Beszel credentials, and Beszel key material. It creates the Pangolin administrator, `aegisnode` organization, and `local-vps` Newt site through Pangolin's API. The proxy stack uses pinned Pangolin, Gerbil, Traefik, Newt, and Docker socket proxy images. Newt continuously reconciles Pangolin resources from Compose labels through a read-only socket proxy.

The observability stage deploys the committed Compose file under `/opt/aegisnode/repository`, keeps runtime data in `/opt/aegisnode/stacks/observability`, preconfigures a local Beszel system, and deploys Beszel Hub, Beszel Agent, and Dozzle without public host ports. Beszel and Dozzle are exposed as `beszel.<domain>` and `dozzle.<domain>` through Pangolin SSO for the saved administrator, with Pangolin target health checks enabled. The stage pauses Newt while replacing the application containers so Docker events cannot race concurrent blueprint creation, reconciles those exact names and hostnames to the stable resource IDs `aegisnode-beszel` and `aegisnode-dozzle`, removes conflicting duplicates, restarts Newt for one complete scan, and verifies exactly one of each before completing. Dozzle container actions and shell access are disabled.

Observability configuration is consumer-owned and Git-backed at `stacks/observability/compose.yaml`. By default, setup creates one repository per profile under the AegisNode configuration directory, initializes `main`, and commits the scaffold as `AegisNode <aegisnode@localhost>`. Use `--config-repo <path>` to select an existing checkout or `--github-repo <https-url>` to clone a GitHub HTTPS repository. Private-repository credentials are read from `AEGISNODE_GITHUB_TOKEN`.

AegisNode deploys the exact committed `HEAD`. Uncommitted changes to the observability Compose file block deployment, while unrelated working-tree changes do not. If an existing checkout has no observability file, AegisNode creates the scaffold and stops so it can be reviewed and committed. Secrets remain outside Git in `/etc/aegisnode/observability.env`, and remote snapshot or checkout drift is rejected rather than overwritten.

## Add an application stack

Saved-profile dashboards show stacks detected in the profile configuration repository. Press `s` to open the standalone stack manager. From there, `a` imports a Docker Compose file, `e` edits its public-resource metadata, `ctrl+s` saves an edit, `d` removes it after confirmation, and `r` deploys only the selected committed stack. Repository actions are also available without leaving the TUI: `v` views staged, unstaged, and untracked diffs; `g` stages all changes under `stacks/`; `c` commits the staged stack changes with a supplied message; and `p` pushes the current branch when an `origin` remote is configured. The manager reports `commit required`, `push required`, `sync required`, or `in sync`. Press `y` to synchronize the committed repository with the server. Synchronization deploys every current stack and removes containers, generated overrides, deployment manifests, and Pangolin resources for stacks deleted from Git. The direct command remains available for scripted imports:

```sh
bin/aegisnode stack add \
  --profile <profile-id> \
  --compose /path/to/docker-compose.yml \
  --publish web:3000:app \
  --publish api:8080:api \
  --env-file /path/to/.env
```

`--publish` is repeatable and uses `service:port:subdomain[:id]`. The optional ID is required when one service has more than one public route. Omitting every `--publish` creates a private stack.

The terminal UI shows detected services and container ports, then opens a resource list. Add, edit, or remove any number of public resources; each resource controls its service, port, public subdomain, stable ID, display name, protocol, health check, and SSO setting. Press `n` to import or replace the stack's runtime `.env` file. AegisNode copies the original Compose file to `stacks/<name>/compose.yaml` and writes the reviewed public-resource contract to `stacks/<name>/aegisnode.yaml`. It does not inject labels into the consumer-owned Compose file.

Runtime environment values are stored in the profile's owner-only `secrets.json`, outside the configuration repository. Commit a `.env.example` when the required keys need documentation, not the populated file. Deployment writes populated values to `/etc/aegisnode/stacks/<name>.env` with mode `0600` and passes that file explicitly to every Compose command. Only variable names are shown in the TUI and CLI. The Compose file must explicitly consume values through `environment`, `env_file`, or `secrets`; AegisNode does not inject every value into every service. Update or remove an environment without editing the stack:

```sh
bin/aegisnode stack env set --profile <profile-id> --stack <name> --file /path/to/.env
bin/aegisnode stack env remove --profile <profile-id> --stack <name>
```

During deployment, AegisNode generates an override that:

- Connects every published service to the external `aegis-public` network.
- Adds stable Pangolin resource, target, SSO, and health-check labels.
- Removes direct host port publishing from published services so Pangolin remains the public entry point.
- Validates the merged Compose model before stopping or replacing containers.
- Restarts Newt and verifies that Pangolin created exactly one expected public resource.

Review and commit the generated stack files, then select that stack in the TUI and press `r`. AegisNode deploys and reports each stack independently, and deploys committed stack configuration only.

DNS registrar changes remain external. Create records for `pangolin.<domain>`, `beszel.<domain>`, and `dozzle.<domain>` pointing to the VPS. Traefik uses HTTP-01 to issue a separate certificate for each hostname, so TCP port 80 must remain reachable.

Retrieve the generated Pangolin administrator credentials:

```sh
bin/aegisnode pangolin-credentials --profile <profile-id>
```

For an existing registered Pangolin profile created before automated bootstrap, set `PANGOLIN_ADMIN_PASSWORD` once when rerunning setup so AegisNode can save the existing password in the owner-only profile secrets file.
