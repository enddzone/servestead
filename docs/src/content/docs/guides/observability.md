---
title: Observability
description: Understand the built-in Beszel, Dozzle, and Dockhand deployment.
---

The observability stage deploys local tools behind Pangolin SSO. It does not expose public host ports for those services.

## Services

| Service | Public hostname | Purpose |
| --- | --- | --- |
| Beszel | `beszel.example.com` | Host metrics and system overview. |
| Dozzle | `dozzle.example.com` | Container log viewing. |
| Dockhand | `dockhand.example.com` | Git-backed stack visibility and Docker environment integration. |

Replace `example.com` with your configured domain.

## Where Files Live

| Path | Purpose |
| --- | --- |
| `/opt/servestead/repository` | Committed deployment input. |
| `/opt/servestead/stacks/observability` | Runtime data. |
| `/etc/servestead/observability.env` | Runtime secrets, mode `0600`. |

The configuration is consumer-owned and Git-backed at `stacks/observability/compose.yaml`.

## Repository Behavior

By default, setup creates one repository per profile under the Servestead configuration directory, initializes `main`, and commits the scaffold as `Servestead <servestead@localhost>`.

You can choose a different repository:

```sh
bin/servestead setup \
  --ip 203.0.113.10 \
  --config-repo /path/to/repository
```

Or clone a GitHub HTTPS repository:

```sh
SERVESTEAD_GITHUB_TOKEN='...' \
bin/servestead setup \
  --ip 203.0.113.10 \
  --github-repo https://github.com/owner/repo.git
```

Private repository credentials are read from `SERVESTEAD_GITHUB_TOKEN`.

## Deployment Rules

Servestead deploys the exact committed `HEAD`. Uncommitted changes to the observability Compose file block deployment. Unrelated working-tree changes do not.

If a GitHub origin and branch are configured, stack synchronization creates or updates matching Dockhand Git-stack records with automatic updates disabled. Servestead still performs the authoritative deployment.
