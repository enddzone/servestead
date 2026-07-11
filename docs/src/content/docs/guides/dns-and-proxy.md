---
title: DNS and Proxy
description: Point DNS, review the domain values, and deploy the Pangolin-backed ingress platform.
---

Servestead deploys Pangolin, Gerbil, Traefik, and Newt after your domain points to the VPS. Complete DNS first so HTTP-01 certificate issuance can succeed during the platform run.

## Before You Begin

The profile needs a server address, base domain, Let's Encrypt email, and working administrative SSH access.

## 1. Create DNS Records

At your DNS provider, create:

| Hostname | Type | Value |
| --- | --- | --- |
| `example.com` | `A` | VPS public IPv4 |
| `*.example.com` | `A` | VPS public IPv4 |

Replace `example.com` with the profile's base domain. Traefik uses HTTP-01, so TCP port 80 must remain reachable; HTTPS traffic requires TCP port 443.

DNS changes remain outside Servestead. Confirm propagation from the resolver you will use before starting the proxy stage.

## 2. Review Profile Values

Open **Setup**, resume the profile, and verify **Base domain** and **Let's Encrypt email**. Open **Connection and credential overrides** only when Pangolin should use a different administrator email.

Continue through GitOps and read the review plan before running.

## 3. Run the Platform

For a new environment, the full reviewed setup includes networking, proxy, and observability after bootstrap and hardening. A resumed profile can target the remaining platform work.

Follow the live logs. A certificate or resource failure usually points to DNS propagation, blocked ports, or previously registered Pangolin credentials.

## 4. Verify the Result

The proxy stage:

- Writes deployment input below `/opt/servestead`.
- Starts Pangolin, Gerbil, Traefik, Newt, and a read-only Docker socket proxy.
- Registers the Pangolin administrator, `servestead` organization, and `local-vps` Newt site.
- Verifies the expected services and public resources.

Open **Profiles → Access** to check the Pangolin credential status. Reveal the administrator password only when you need to sign in, then close the local session when finished.

## Generated Credentials

Profile-aware setup generates and stores the Pangolin administrator password, server secret, Newt credentials, Beszel credentials, and Beszel key material. See [Access and secrets](../access-and-secrets/) for storage boundaries.

## Direct CLI Alternative

Use the direct command only for a scripted workflow that already manages the server secret:

```sh
./bin/servestead proxy \
  --host 203.0.113.10 \
  --private-key "$HOME/.ssh/id_ed25519" \
  --domain example.com \
  --email admin@example.com \
  --server-secret 'replace-with-a-long-random-secret'
```

Normal profile-aware setup generates and saves this secret for you.

:::tip[Existing Pangolin registration]
If an older profile was registered before automated bootstrap, save the existing administrator email and password through the recovery form or **Profiles → Access**, then retry the platform run.
:::
