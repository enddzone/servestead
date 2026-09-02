---
title: DNS and proxy
description: Point DNS, review profile values, and deploy the Pangolin-backed ingress platform from the terminal UI.
---

Servestead deploys Pangolin, Gerbil, Traefik, and Newt after your domain points to the VPS. Complete DNS first so HTTP-01 certificate issuance can succeed during the Platform run.

## Before you begin

The profile needs a server address, base domain, Let's Encrypt email, and working administrative SSH access.

## 1. Create DNS records

At your DNS provider, create:

| Hostname | Type | Value |
| --- | --- | --- |
| `example.com` | `A` | VPS public IPv4 |
| `*.example.com` | `A` | VPS public IPv4 |

Replace `example.com` with the profile's base domain. Traefik uses HTTP-01, so TCP port 80 must remain reachable. HTTPS traffic requires TCP port 443.

DNS changes remain outside Servestead. Confirm propagation before starting the proxy stage.

## 2. Review profile values

Run `servestead setup`, select the profile, and press `e`. Verify the base domain and Let's Encrypt email. Press `a` only when Pangolin needs a different administrator email or an existing administrator password.

Use `ctrl+s` to save a corrected profile without starting a run.

## 3. Run Platform

Back on the dashboard, select **Platform** and press `r`. Platform runs Network, Proxy, and Observability in order. Review the plan before Servestead prepares the repository or opens an SSH connection.

Follow the live task output. Certificate or resource failures usually point to DNS propagation, blocked ports, or credentials for an already-registered Pangolin instance.

## 4. Verify the result

The proxy stage:

- Writes deployment input below `/opt/servestead`.
- Starts Pangolin, Gerbil, Traefik, Newt, and a read-only Docker socket proxy.
- Registers the Pangolin administrator, `servestead` organization, and `local-vps` Newt site.
- Verifies the expected services and public resources.

Press `h` from the dashboard and inspect the Platform run. Then open `https://pangolin.example.com`, replacing `example.com` with the profile domain. Pangolin is a remote service deployed on the VPS, not a Servestead interface.

Press `p` on the profile dashboard when you need the saved setup URL or administrator credentials.

## Direct command

Use the direct command only for a scripted workflow that already manages the server secret:

```sh
./bin/servestead proxy \
  --host 203.0.113.10 \
  --private-key "$HOME/.ssh/id_ed25519" \
  --domain example.com \
  --email admin@example.com \
  --server-secret 'replace-with-a-long-random-secret'
```

Normal profile-aware setup generates and saves this secret.

:::tip[Existing Pangolin registration]
If an older profile was registered before automated bootstrap, save the existing administrator email and password in the advanced profile fields, then retry Platform.
:::
