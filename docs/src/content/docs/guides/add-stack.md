---
title: Add an Application Stack
description: Import a Compose file, choose public routes, store secrets outside Git, and deploy.
---

Servestead deploys application stacks from a Git-backed configuration repository. The Compose file stays consumer-owned.

## Add From the TUI

From a saved profile dashboard:

1. Press `s` to open the stack manager.
2. Press `a` to open the Compose file browser.
3. Review detected services.
4. Select services that should receive public routes.
5. Choose no runtime environment, a detected adjacent `.env`, or another environment file.
6. Save the generated stack metadata.
7. Review and commit repository files.
8. Deploy the selected committed stack.

Every Compose service deploys. Only explicitly selected services receive Pangolin routes.

## Add From the CLI

```sh
bin/servestead stack add \
  --profile <profile-id> \
  --compose /path/to/docker-compose.yml \
  --publish web:3000:app \
  --publish api:8080:api \
  --env-file /path/to/.env
```

`--publish` is repeatable and uses:

```text
service:port:subdomain[:id]
```

The optional ID is required when one service has more than one public route. Omitting every `--publish` creates a private stack.

## Files Created

Servestead copies and writes:

| File | Purpose |
| --- | --- |
| `stacks/<name>/compose.yaml` | Your reviewed Compose file. |
| `stacks/<name>/servestead.yaml` | Public-resource contract and route metadata. |

It does not inject labels into the consumer-owned Compose file.

## Runtime Environment

Runtime environment values are stored in the profile's owner-only `secrets.json`, outside the configuration repository.

Commit `.env.example` when required keys need documentation. Do not commit populated `.env` files.

Update or remove stack environment values without editing the stack:

```sh
bin/servestead stack env set --profile <profile-id> --stack <name> --file /path/to/.env
bin/servestead stack env remove --profile <profile-id> --stack <name>
```

## Bind Mounts

Use `/data/<stack>/...` for application bind mounts in standalone Compose files.

Servestead creates `/data` as root-owned and creates `/data/<stack>` with owner `1000:1000` the first time a stack deploys. If an image runs as another UID or GID, create or chown that specific subdirectory before deployment, or use a Docker named volume.

Do not bind-mount writable application data from `/opt/servestead/repository`; that checkout is deployment input, not runtime state.

## Deployment Override

During deployment, Servestead generates an override that:

- Connects published services to the external `servestead-public` network.
- Adds stable Pangolin resource, target, SSO, and health-check labels.
- Removes direct host port publishing from published services.
- Validates the merged Compose model before stopping or replacing containers.
- Restarts Newt and verifies exactly one expected public resource.
