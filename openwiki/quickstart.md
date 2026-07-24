---
type: Quickstart Guide
title: Servestead OpenWiki Quickstart
description: Build and use the Servestead Go CLI to provision, harden, network, and operate Ubuntu VPS application stacks with Pangolin ingress, observability, Git-backed configuration, and encrypted secrets. Includes commands, prerequisites, key concepts, and repository layout.
tags: [servestead, quickstart, cli, vps, docker, pangolin]
---

# Servestead — OpenWiki Quickstart

Servestead is a local Go CLI and control plane for turning a raw Ubuntu VPS into a hardened, Git-backed platform for running private application stacks. It handles the full server lifecycle — cloud provisioning, admin-user bootstrap, OS hardening, Docker/UFW networking, Pangolin-backed ingress, observability, and application stack management — all orchestrated over SSH from a single binary. The recommended interface is the local web UI (`servestead ui`); a full-screen terminal UI and direct commands remain available for automation and guided provisioning. No local Ansible, OpenSSH, or `ssh-keygen` binaries are required.

## What Servestead Does

| Capability | Summary |
|---|---|
| **Cloud provisioning** | Creates DigitalOcean Droplets (Ubuntu 24.04) via the godo SDK, polls for a public IPv4, saves the server as a profile. |
| **Bootstrap** | Creates a passwordless-sudo admin user with your ED25519 public key over SSH (initial login as `root`). |
| **Hardening** | Applies system upgrades, swap, sysctl hardening, SSH lockdown, unattended-upgrades, and CrowdSec intrusion prevention. |
| **Networking** | Installs Docker, configures UFW firewall with Docker bridge NAT/masquerade, enables IPv4 forwarding. |
| **Ingress proxy** | Deploys Pangolin + Traefik + Gerbil + Newt as a Docker Compose stack for SSO-protected reverse proxy ingress. |
| **Observability** | Deploys Beszel (metrics), Dozzle (logs), and Dockhand (container management) behind Pangolin SSO. |
| **Application stacks** | Imports Docker Compose files, generates Pangolin route labels, manages SOPS/age-encrypted secrets, deploys via Dockhand. |
| **Config repository** | Git-backed configuration per profile — local, existing checkout, or GitHub clone. Observability Compose and stack definitions are consumer-owned and committed. |
| **TUI & Web UI** | Local web UI (templ + htmx) is the recommended interface for setup, profiles, GitOps, access, and cloud operations. A full-screen Bubble Tea terminal UI provides guided DigitalOcean provisioning and keyboard-driven stack management. |

## Build & Install

```sh
go build -o bin/servestead ./backend
```

Requires Go 1.26.5+. The binary entry point is `backend/main.go`; GoReleaser builds for linux/darwin/windows × amd64/arm64.

### From source (development)

```sh
go build -o bin/servestead ./backend
bin/servestead ui                        # local web UI (recommended)
bin/servestead doctor                    # preflight check
bin/servestead setup                     # interactive TUI
bin/servestead setup --ip 203.0.113.10   # guided setup on existing VPS
```

### Frontend templates

The web UI uses [a-h/templ](https://github.com/a-h/templ) components in `frontend/`. Regenerate the Go code before building if templates change:

```sh
go tool templ generate
```

### Docs site

The documentation site in `docs/` is built with Astro Starlight:

```sh
cd docs && npm install && npm run dev    # local dev
cd docs && npm run build                 # static build for GitHub Pages
```

## Command Reference

| Command | Purpose |
|---|---|
| `ui` | Local web UI (loopback HTTP server with browser auto-open) — recommended entry point |
| `setup` | Interactive TUI or `--ip` guided setup |
| `provision` | Create a DigitalOcean Droplet directly (billable) |
| `keygen` | Generate an ED25519 keypair for provider login |
| `bootstrap` | Create the admin user on a raw VPS |
| `harden` | Apply OS hardening (sysctl, CrowdSec, swap, SSH lockdown) |
| `network` | Install Docker and configure UFW firewall |
| `proxy` | Deploy the Pangolin/Traefik/Gerbil/Newt ingress stack |
| `pangolin-credentials` | Retrieve saved Pangolin admin credentials for a profile |
| `stack add` | Import a Docker Compose file as a managed stack |
| `stack env set/remove` | Manage stack runtime secrets |
| `secrets init/status/export-key/import-key` | Manage the profile's age encryption identity |
| `github-token set/status/remove` | Store a GitHub PAT for private config repo cloning |
| `doctor` | Preflight check of local prerequisites |

Run `servestead <command> -help` for command-specific flags.

## Key Concepts

- **Profiles** — Each server is tracked as a profile (ID, IP, SSH keys, domain, secrets, run state) stored under `os.UserConfigDir()/servestead/profiles/`. Profiles are keyed by generated IDs, so reusing an IP creates a new profile rather than overwriting.
- **Config repository** — A Git repository per profile holding `stacks/<name>/compose.yaml`, `stacks/<name>/servestead.yaml` (route metadata), and `stacks/observability/compose.yaml`. Can be local, an existing checkout, or a GitHub clone.
- **Pangolin** — The ingress management layer. Newt reads Docker Compose labels and reconciles public resources (routes) into Pangolin, which configures Traefik for TLS termination and SSO.
- **SOPS + age** — Stack runtime secrets are encrypted at rest in `servestead.secrets.yaml` using the profile's age identity. Deployment decrypts locally and injects via SSH stdin or Dockhand's API — no plaintext `.env` files on the server.

## Documentation Sections

- [Architecture](architecture.md) — CLI dispatch, setup orchestrator, TUI, web UI, SSH execution, embedded resources, profile storage.
- [Server Lifecycle Workflows](workflows.md) — The provision → bootstrap → harden → network → proxy → observability → stacks pipeline, stage by stage.
- [Domain Concepts](domain.md) — Profiles, config repository & GitOps, Pangolin ingress, observability, application stacks, secrets, cloud integration.
- [Operations](operations.md) — CI/CD pipelines, releases, linting, security scanning, docs site.
- [Testing](testing.md) — Test inventory, patterns, how to run tests, coverage configuration.

## Repository Layout

```
backend/           Go source — single-package CLI (package main)
  resources/       Embedded shell scripts, config templates, Docker Compose templates
frontend/          templ components for the web UI (compiled to Go)
docs/              Astro Starlight documentation site
examples/          Example Docker Compose stacks (arr-vpn)
.github/workflows/ CI/CD: ci, codeql, docs, openwiki-update, release, release-please, renovate, security
```

All Go source lives in `backend/` as a single `package main`. The `frontend/` directory contains templ source files (`.templ`) and their generated Go code (`_templ.go`). Deployment scripts and config templates are embedded at compile time via `go:embed` in `backend/resources/resources.go`.

## Prerequisites

- Go 1.26.5+ to build the CLI
- A DigitalOcean API token when provisioning from Servestead
- A local ED25519 key pair for administrative access
- Remote target: Ubuntu 22.04+ on Linux 5.15+ with `apt`, `sudo`, `systemctl`, `curl`, `gpg`, and `iptables`
