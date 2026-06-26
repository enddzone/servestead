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

With `--ip`, AegisNode creates or selects a saved profile, collects the missing full-run values up front, generates and stores the Pangolin server secret, checks local prerequisites, then runs bootstrap, hardening, Docker networking, and reverse proxy deployment as one setup plan. Saved profiles live under the directory returned by `os.UserConfigDir()` in an `aegisnode` subdirectory. Each profile keeps metadata, run state, secrets, and JSONL run logs in separate files with owner-only permissions.

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

Running `setup` without `--ip` opens the profile-first terminal UI. It lists saved profiles, shows their stage dashboard, collects missing full-run values before any remote command runs, and lets you review the plan before execution. From a saved profile dashboard, press `x` to delete only the local saved profile, secrets, state, and run logs; this does not change the remote server. The older one-off guided paths remain available from the advanced legacy setup entry. Setup does not create billable cloud resources; use `provision` separately when you want the CLI to create a server.

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

The hardening runner targets the administrative user created by `bootstrap` and defaults to `--ssh-user aegisadmin`. It validates the target is Ubuntu 22.04 or newer on Linux 5.15 or newer, applies pending package upgrades, disables root SSH login, disables SSH password and keyboard-interactive login, checks every sysctl key before applying `/etc/sysctl.d/99-vps-hardening.conf`, enables unattended upgrades, installs CrowdSec from its apt repository, installs the matching CrowdSec firewall bouncer for nftables or iptables, and ensures both services are running.

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

The direct proxy command keeps `--server-secret` for scripts. Normal profile-aware setup generates this value automatically and reuses it from the profile secrets file. The proxy runner writes `/opt/aegisnode/proxy/docker-compose.yml`, Pangolin application config, and Traefik config files, prepares persistent data directories, opens TCP/80, TCP/443, UDP/51820, and UDP/21820 for Traefik and Gerbil/Pangolin ingress, starts Traefik, Pangolin, and Gerbil with Docker Compose, and verifies all three services are running. DNS registrar changes remain external; create `A example.com -> 203.0.113.10` and `A *.example.com -> 203.0.113.10` before expecting Let's Encrypt HTTP-01 issuance to complete. On first boot, open `https://pangolin.example.com/auth/initial-setup` and replace `example.com` with your domain.
