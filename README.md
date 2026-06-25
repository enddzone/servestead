# AegisNode

AegisNode is a local Go CLI for provisioning and hardening Ubuntu VPS instances. It supports Hetzner and DigitalOcean provisioning, administrative-user bootstrapping, and Phase 2 operating-system hardening through embedded Ansible playbooks.

## Prerequisites

- Go 1.24.2 or newer to build the CLI
- `ansible-playbook` from ansible-core to run `bootstrap` or `harden`
- An existing SSH key registered with the selected cloud provider
- A local ED25519 key pair for administrative access

## Build

```sh
go build -o bin/aegisnode .
```

## Provision a VPS

Credentials are read from the environment so they do not appear in shell process listings.

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

For live Phase 1 and Phase 2 testing on an existing disposable Ubuntu VPS, use the terminal UI:

```sh
bin/aegisnode setup
```

The guided flow explains each path before it runs anything. It can bootstrap an existing VPS and then harden it, harden an already-bootstrapped VPS, or run local preflight checks only. It does not create billable cloud resources; use `provision` separately when you want the CLI to create a server.

For a quick local dependency check without opening the TUI:

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

The first SSH connection uses OpenSSH's `accept-new` policy. Verify the host fingerprint through the provider console before running the command when the threat model requires out-of-band host verification. Root SSH access is intentionally left enabled until Phase 5.

## Apply Phase 2 hardening

```sh
bin/aegisnode harden \
  --host 203.0.113.10 \
  --private-key "$HOME/.ssh/id_ed25519"
```

The hardening playbook targets the administrative user created by `bootstrap` and defaults to `--ssh-user aegisadmin`. It validates the target is Ubuntu 22.04 or newer on Linux 5.15 or newer, checks every sysctl key before applying `/etc/sysctl.d/99-vps-hardening.conf`, enables unattended upgrades, installs CrowdSec from its apt repository, and ensures the `crowdsec` service is running.
