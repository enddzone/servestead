---
title: Security Model
description: Security boundaries, trust assumptions, and secret handling in Servestead.
---

Servestead is designed for fresh VPS setup. It makes strong changes to SSH, package state, firewall policy, Docker networking, and reverse proxy resources.

## Local Web Boundary

`servestead ui` is a local control plane, not a remotely hosted dashboard.

- The listener accepts only `localhost`, `127.0.0.1`, or `::1` addresses.
- Each launch generates a random bootstrap token, session value, and CSRF token.
- The tokenized `/ui` URL establishes an `HttpOnly`, `SameSite=Lax` session cookie and immediately redirects to a URL without the token.
- Every state-changing POST requires the session cookie and matching CSRF value.
- **Shutdown** stops the local HTTP server; closing a browser tab alone does not.

The cookie is not marked `Secure` because the loopback-only server uses local HTTP. Do not expose the listener through a proxy, tunnel, port forward, or non-loopback interface. Treat the printed bootstrap URL as a temporary session credential.

The interface masks saved secrets by default, but an explicit reveal displays the selected value in the page for the current session. The local machine and browser are therefore part of the trust boundary.

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

Generated platform secrets are stored in owner-only profile files or on the remote server. Imported application stack secrets are Git-backed, but only as SOPS-compatible encrypted files:

| Secret | Storage |
| --- | --- |
| Pangolin server secret | Local profile secrets and remote config. |
| Pangolin administrator password | Local profile secrets. |
| GitHub repository token | Local profile secrets; sent over SSH stdin only for Git checkout. |
| Stack environment values | `stacks/<name>/servestead.secrets.yaml` in the config repository; Dockhand secret envs at runtime. |
| Observability environment values | Remote `/etc/servestead/observability.env`. |

Configuration repositories should contain reviewed Compose, metadata, and SOPS-compatible stack secret files, not populated plaintext secret values. The profile stack secret identity is stored in the owner-only local profile secrets file and can be managed with `servestead secrets init`, `servestead secrets export-key`, and `servestead secrets import-key`. The exported identity can also be used with `SOPS_AGE_KEY` or `SOPS_AGE_KEY_FILE` and `sops -d` as a recovery path. Deployment exports decrypted stack secrets only for the remote Compose task, and stack secret sync uses the server-local Dockhand API over SSH stdin; it does not require exposing Dockhand's API publicly.

GitHub personal access tokens are managed with `servestead github-token set`, `servestead github-token status`, and `servestead github-token remove`. The saved token is stored in local profile secrets and is not persisted on the remote server. During Git-backed deployment, Servestead sends the token over SSH stdin to the checkout task, exposes it only through a temporary `GIT_ASKPASS` script environment, and unsets it when the task exits. `SERVESTEAD_GITHUB_TOKEN` overrides the saved token for the current run.

## Provisioning Boundary

Direct `servestead provision` creates one billable DigitalOcean Droplet and stops after reporting the public IPv4 address. The browser provisioning form under **Profiles** also creates one Droplet immediately on valid submission and saves it as a local profile. The Terminal UI adds live catalog prices and an exact typed confirmation before it creates the Droplet. No provisioning path bootstraps or hardens automatically.

DigitalOcean API tokens are used from the environment or a masked browser/Terminal UI field for the current action. They are not saved in profile metadata or profile secrets.
