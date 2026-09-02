---
title: Security model
description: Local state, SSH trust, secret handling, provisioning, deletion, and remote service boundaries in Servestead.
---

Servestead is designed for fresh VPS setup. It makes strong changes to SSH, package state, firewall policy, Docker networking, reverse proxy resources, and cloud instances.

## Local terminal boundary

Servestead runs as the current local user. The terminal UI does not start an HTTP server or expose a local network listener. It can still display sensitive values when you explicitly reveal Pangolin credentials or run a direct credential command. Treat terminal output, scrollback, shell history, and screen recording as part of the local trust boundary.

Profiles use the operating system's user configuration directory by default. `SERVESTEAD_CONFIG_DIR` can select a different Servestead root. Profile metadata, state, secrets, and run logs use owner-only permissions. Run-history views replace known profile secrets before displaying log text.

Local profile deletion requires `delete <profile-name>` and refuses to run while the profile has an active setup run. It removes local profile files only. It does not change the server, cloud resource, or separate configuration repository.

## SSH trust

The first SSH connection uses a native trust-on-first-use host key policy similar to OpenSSH `accept-new`:

- Unknown host keys are added to `$HOME/.ssh/known_hosts`.
- Changed known host keys fail.

For high-assurance deployments, verify the server host fingerprint through the provider console before bootstrapping.

## Bootstrap boundary

`bootstrap` creates the administrative user, grants passwordless sudo, and installs the ED25519 authorized key.

Root SSH access remains enabled until hardening has installed and verified administrative key access.

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

## Network and firewall

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

## Secret handling

Generated platform secrets are stored in owner-only profile files or on the remote server. Imported application stack secrets are Git-backed only as SOPS-compatible encrypted files.

| Secret | Storage |
| --- | --- |
| Pangolin server secret | Local profile secrets and remote config. |
| Pangolin administrator password | Local profile secrets. |
| GitHub repository token | Local profile secrets. Servestead sends it over SSH stdin for Git checkout. |
| Stack environment values | `stacks/<name>/servestead.secrets.yaml` in the config repository and Dockhand secret environment values at runtime. |
| Observability environment values | Remote `/etc/servestead/observability.env`. |
| DigitalOcean token | Environment or masked prompt for the current action. It is not saved in the profile. |

Configuration repositories should contain reviewed Compose, metadata, and encrypted stack secret files, not populated plaintext values. The profile stack age identity is stored in local profile secrets. Back it up with `servestead secrets export-key` before deleting or moving the profile.

During Git-backed deployment, Servestead sends the GitHub token over SSH stdin to the checkout task, exposes it only through a temporary `GIT_ASKPASS` environment, and unsets it when the task exits. `SERVESTEAD_GITHUB_TOKEN` overrides the saved profile token for the current run.

## Provisioning and provider actions

The terminal provisioning path loads the live DigitalOcean catalog and prices, then requires the exact displayed confirmation phrase before creating one billable Droplet. If the provider creates the Droplet but local profile persistence fails, retrying saves that result and does not call provider creation again.

Direct `servestead provision` creates one billable Droplet and stops after reporting its public address. Its caller owns the review boundary.

Restart and destroy use cancellable provider operations. Destroy requires the exact phrase displayed by the TUI and permanently removes the Droplet. It keeps local profile state and records the resource as destroyed.

## Remote service dashboards

Pangolin, Beszel, Dozzle, and Dockhand are remote services deployed on the VPS. Pangolin and Traefik expose their configured hostnames over HTTPS. Beszel, Dozzle, and Dockhand do not publish direct host ports and sit behind Pangolin SSO. These dashboards remain separate from the local Servestead terminal process.

## Stack deletion

Local stack deletion requires `delete <stack-name>` and removes the stack directory from the configuration repository. The remote deployment remains until you commit the deletion and explicitly synchronize the repository. That later reconciliation removes the managed containers, manifests, overrides, and Pangolin resources.
