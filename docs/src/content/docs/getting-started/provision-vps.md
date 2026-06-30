---
title: Provision a VPS
description: Create a Hetzner or DigitalOcean server before running setup.
---

Servestead can create one Ubuntu VPS and wait until its public IPv4 address is available. Provisioning does not bootstrap or harden the server automatically.

:::caution[Provisioning is billable]
These commands call real cloud APIs and create billable infrastructure. Delete unused servers in the provider console when you are done testing.
:::

## 1. Register an SSH Public Key

Generate a key if needed:

```sh
bin/servestead keygen
```

Add the printed public key to Hetzner or DigitalOcean. Keep the provider key name, ID, or fingerprint available for `--ssh-key`.

## 2. Provision on Hetzner

```sh
export HETZNER_API_TOKEN='...'

bin/servestead provision \
  --provider hetzner \
  --name aegis-01 \
  --ssh-key my-provider-key
```

Defaults:

| Setting | Value |
| --- | --- |
| Region | `fsn1` |
| Size | `cx23` |
| Image | `ubuntu-24.04` |

## 3. Provision on DigitalOcean

```sh
export DIGITALOCEAN_ACCESS_TOKEN='...'

bin/servestead provision \
  --provider digitalocean \
  --name aegis-01 \
  --ssh-key 'provider-key-id-or-fingerprint'
```

Defaults:

| Setting | Value |
| --- | --- |
| Region | `nyc3` |
| Size | `s-1vcpu-1gb` |
| Image | `ubuntu-24-04-x64` |

## 4. Continue With Setup

After provisioning reports the IPv4 address, continue with:

```sh
bin/servestead setup --ip <new-server-ip>
```

Provider defaults can be overridden with `--region`, `--size`, and `--image`.
