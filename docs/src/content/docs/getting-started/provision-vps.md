---
title: Provision a VPS
description: Create a DigitalOcean Droplet before running setup.
---

The recommended way to provision is the guided TUI inside `servestead setup`. It lets you choose a DigitalOcean region, size, Ubuntu image, and SSH key before anything billable is created. Size choices show the monthly and hourly price returned by DigitalOcean.

Provisioning creates one Ubuntu DigitalOcean Droplet, waits for its public IPv4 address, saves it as a Servestead profile, and returns to the profile dashboard. It does not bootstrap or harden the server automatically; those remain explicit setup actions.

:::caution[Provisioning is billable]
Provisioning calls real DigitalOcean APIs and creates billable infrastructure. Review the displayed monthly and hourly price in the TUI before confirming. Delete unused Droplets when you are done testing.
:::

## Default path: use the TUI

Start the profile-first setup UI:

```sh
bin/servestead setup
```

Choose **Provision a new DigitalOcean VPS**.

The TUI will ask for:

- A DigitalOcean API token.
- A Droplet name.
- The local Servestead private key path.

Then it loads live DigitalOcean catalog data and walks through these selections:

- Region.
- Size, including CPU, memory, disk, monthly price, and hourly price.
- Ubuntu image.
- Existing DigitalOcean SSH key, or upload the matching local public key.

Before creation, Servestead shows a review screen and requires the exact confirmation phrase. After DigitalOcean reports the public IPv4 address, Servestead saves the Droplet as a local profile and opens that profile's dashboard.

## DigitalOcean token

Create a DigitalOcean API token with enough access to read catalog data, read or create SSH keys, create Droplets, reboot Droplets, and destroy Droplets when you use those actions.

You can paste the token into the masked TUI prompt. To avoid entering it every run, export it before launching setup:

```sh
export DIGITALOCEAN_ACCESS_TOKEN='...'
```

Servestead uses the token for the current run only. It is not saved in profile metadata or secrets.

## SSH key

Generate the default Servestead keypair if needed:

```sh
bin/servestead keygen
```

The default private key path is:

```sh
$HOME/.ssh/servestead_ed25519
```

The TUI reads the matching `.pub` file, compares it with existing DigitalOcean SSH keys, and offers to upload it when it is not already present.

## Continue setup

Provisioning only creates the Droplet and saves a profile. From the returned dashboard, run the setup stages when you are ready:

- **Bootstrap** creates the administrative SSH user.
- **Harden** applies OS and SSH hardening.
- **Platform** configures Docker networking, Pangolin, and observability.

You can also resume later with:

```sh
bin/servestead setup
```

## Restart or destroy the Droplet

For a profile created through guided DigitalOcean provisioning, open the profile dashboard and press `o` for cloud actions.

- Restart requests a DigitalOcean reboot action.
- Destroy permanently deletes the remote DigitalOcean Droplet after typed confirmation.
- Destroy does not delete local Servestead profile files, secrets, state, or run logs; it marks the profile's cloud metadata as destroyed.

## Advanced: direct CLI provisioning

Use the direct command only when you already know the DigitalOcean IDs or slugs and have already added the SSH public key to DigitalOcean:

```sh
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

Defaults can be overridden with `--region`, `--size`, and `--image`.
