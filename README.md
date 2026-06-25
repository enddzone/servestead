# AegisNode

AegisNode is a local Go CLI for provisioning and hardening Ubuntu VPS instances. It supports Hetzner and DigitalOcean provisioning, administrative-user bootstrapping, and operating-system hardening through a native Go SSH runner.

## Prerequisites

- Go 1.24.2 or newer to build the CLI
- An existing SSH key registered with the selected cloud provider
- A local ED25519 key pair for administrative access

`bootstrap`, `harden`, and `keygen` do not require local Ansible, OpenSSH, or `ssh-keygen` binaries. Remote bootstrap and hardening still assume a supported Ubuntu target with standard system tools such as `apt`, `sudo`, `systemctl`, `curl`, `gpg`, and `iptables`.

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

For guided setup on an existing disposable Ubuntu VPS, use the terminal UI:

```sh
bin/aegisnode setup
```

The guided flow explains each path before it runs anything. It can prepare the AegisNode SSH key, set up an existing VPS and then harden it, harden an already set-up VPS, or run local preflight checks only. It does not create billable cloud resources; use `provision` separately when you want the CLI to create a server.

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
