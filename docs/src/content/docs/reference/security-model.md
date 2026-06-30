---
title: Security Model
description: Security boundaries, trust assumptions, and secret handling in Servestead.
---

Servestead is designed for fresh VPS setup. It makes strong changes to SSH, package state, firewall policy, Docker networking, and reverse proxy resources.

## SSH Trust

The first SSH connection uses a native trust-on-first-use host key policy similar to OpenSSH `accept-new`:

- Unknown host keys are added to `$HOME/.ssh/known_hosts`.
- Changed known host keys fail.

For high-assurance deployments, verify the server host fingerprint through the provider console before bootstrapping.

## Bootstrap Boundary

`bootstrap` creates the administrative user, grants passwordless sudo, and installs the ED25519 authorized key.

Root SSH access is intentionally left enabled until hardening has installed and verified administrative key access.

## Hardening

The hardening runner:

- Validates Ubuntu 22.04 or newer on Linux 5.15 or newer.
- Applies pending package upgrades.
- Configures persistent swap.
- Disables root SSH login.
- Disables SSH password and keyboard-interactive login.
- Validates every sysctl key before applying the hardening config.
- Enables unattended upgrades.
- Installs CrowdSec and the matching firewall bouncer.

## Network and Firewall

The network runner:

- Installs Docker from Docker's official Ubuntu apt repository.
- Ensures the administrative user has passwordless sudo.
- Adds the administrative user to the `docker` group.
- Writes Docker daemon firewall and NAT configuration.
- Enables IPv4 forwarding.
- Manages the Servestead UFW NAT block.
- Preserves SSH access on the configured SSH port.
- Denies incoming and routed traffic by default.
- Allows HTTP and HTTPS ingress.
- Allows routed traffic from the default Docker bridge networks.

Docker group membership applies to new login sessions. Disconnect and reconnect before running Docker commands without `sudo`.

## Secret Handling

Generated runtime secrets are stored outside Git in owner-only profile files or on the remote server:

| Secret | Storage |
| --- | --- |
| Pangolin server secret | Local profile secrets and remote config. |
| Pangolin administrator password | Local profile secrets. |
| Stack environment values | Local profile secrets and remote `/etc/servestead/stacks/<name>.env`. |
| Observability environment values | Remote `/etc/servestead/observability.env`. |

Configuration repositories should contain reviewed Compose and metadata files, not populated secret values.

## Provisioning Boundary

Direct `servestead provision` creates one billable DigitalOcean Droplet and stops after reporting the public IPv4 address. Guided setup can also create one DigitalOcean Droplet, save it as a local profile, and return to the dashboard. Provisioning does not bootstrap or harden automatically.

DigitalOcean API tokens are used from the environment or a masked TUI prompt for the current run. They are not saved in profile metadata or profile secrets.
