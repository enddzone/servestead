---
title: DNS and Proxy
description: Point DNS and deploy the Pangolin-backed reverse proxy stack.
---

Servestead deploys a Pangolin-backed ingress stack after DNS points at your VPS.

## Required DNS

Create these records at your DNS provider:

| Hostname | Type | Value |
| --- | --- | --- |
| `example.com` | `A` | VPS public IPv4 |
| `*.example.com` | `A` | VPS public IPv4 |

Replace `example.com` with your real domain.

Traefik uses HTTP-01 to issue a separate certificate for each hostname, so TCP port 80 must remain reachable.

## Direct Proxy Command

Most users should let guided setup generate and save the server secret. Use the direct proxy command for scripts:

```sh
bin/servestead proxy \
  --host 203.0.113.10 \
  --private-key "$HOME/.ssh/id_ed25519" \
  --domain example.com \
  --email admin@example.com \
  --server-secret 'replace-with-a-long-random-secret'
```

## What Gets Deployed

The proxy stage writes deployment files under `/opt/servestead`, starts the Compose stack, and verifies the expected services are running.

The stack includes:

- Pangolin for access and resources.
- Gerbil for tunneling.
- Traefik for HTTP routing and certificates.
- Newt for resource reconciliation through a read-only Docker socket proxy.

## Generated Credentials

Profile-aware setup generates and saves:

- Pangolin administrator password.
- Pangolin server secret.
- Newt credentials.
- Beszel credentials.
- Beszel key material.

Retrieve the generated Pangolin administrator credentials with:

```sh
bin/servestead pangolin-credentials --profile <profile-id>
```

Or by IP:

```sh
bin/servestead pangolin-credentials --ip 203.0.113.10
```

:::tip
If an existing Pangolin profile was registered before automated bootstrap, set `PANGOLIN_ADMIN_PASSWORD` once when rerunning setup so Servestead can save the existing password in the owner-only profile secrets file.
:::
