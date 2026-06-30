---
title: Prerequisites
description: Local, cloud, DNS, and server prerequisites before running Servestead.
---

Use this checklist before running remote setup.

<ul class="checklist">
  <li>A local machine with Go 1.26.4 or newer.</li>
  <li>A fresh Ubuntu 22.04 or newer VPS. Ubuntu 24.04 is the default for provisioning.</li>
  <li>An ED25519 SSH key pair for administrative access.</li>
  <li>If provisioning: an SSH public key already registered with Hetzner or DigitalOcean.</li>
  <li>If deploying the proxy: a domain you control and an email address for Let's Encrypt.</li>
  <li>DNS access for the apex domain and wildcard subdomains.</li>
</ul>

## Local Checks

Run the doctor command when you want a quick preflight without opening the TUI:

```sh
bin/servestead doctor
```

`bootstrap`, `harden`, `network`, and `keygen` do not require local Ansible, OpenSSH, or `ssh-keygen` binaries. Remote setup still assumes standard Ubuntu tools such as `apt`, `sudo`, `systemctl`, `curl`, `gpg`, and `iptables`.

## SSH Key

Servestead can generate a provider login keypair:

```sh
bin/servestead keygen
```

The default private key path is:

```sh
$HOME/.ssh/servestead_ed25519
```

Add the printed public key to your cloud provider before provisioning or use an existing key pair for an existing VPS.

## DNS Records

Before the proxy can issue certificates and expose services, point these records at the VPS:

| Hostname | Type | Value |
| --- | --- | --- |
| `example.com` | `A` | VPS public IPv4 |
| `*.example.com` | `A` | VPS public IPv4 |

Replace `example.com` with your real domain.

:::tip
DNS registrar changes are external to Servestead. Keep your provider console open during setup so you can confirm records and propagation.
:::
