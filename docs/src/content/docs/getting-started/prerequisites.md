---
title: Requirements
description: Prepare the local machine, Ubuntu server, SSH key, domain, and provider access Servestead needs.
---

Complete this checklist before starting a reviewed setup run.

<ul class="checklist">
  <li>A local machine with Go 1.26.8 or newer when building from source.</li>
  <li>A fresh Ubuntu 22.04 or newer VPS. Ubuntu 24.04 is the default DigitalOcean image.</li>
  <li>The server's public IPv4 address or hostname.</li>
  <li>An ED25519 SSH key pair with access to the server.</li>
  <li>A domain you control and an email address for Let's Encrypt.</li>
  <li>Access to create apex and wildcard DNS records.</li>
  <li>For provisioning, a DigitalOcean API token and an SSH public key in DigitalOcean.</li>
</ul>

:::caution[Use a fresh server]
Setup hardens SSH, changes firewall policy, installs packages and Docker, and deploys ingress and observability services. Do not use a server with workloads or data you are not prepared to replace.
:::

## Local preflight

From the repository root, run:

```sh
bin/servestead doctor
```

The direct `bootstrap`, `harden`, `network`, and `keygen` commands do not require local Ansible, OpenSSH, or `ssh-keygen` binaries. The remote server still needs standard Ubuntu tools such as `apt`, `sudo`, `systemctl`, `curl`, `gpg`, and `iptables`.

Run the terminal UI in a real terminal with color and alternate-screen support. A normal terminal size of at least 80 columns by 24 rows gives the setup tables and log view enough room.

## SSH key

Generate the default Servestead key pair if you do not already have an ED25519 key:

```sh
bin/servestead keygen
```

The default private key is `$HOME/.ssh/servestead_ed25519`. Use the matching `.pub` file when registering the key with a cloud provider.

## DNS records

Before the proxy can issue certificates and expose services, create:

| Hostname | Type | Value |
| --- | --- | --- |
| `example.com` | `A` | VPS public IPv4 |
| `*.example.com` | `A` | VPS public IPv4 |

Replace `example.com` with your domain. DNS changes remain outside Servestead, so keep your registrar or DNS provider available while you verify propagation.

## DigitalOcean access

The guided provisioning path loads live regions, sizes, prices, Ubuntu images, and SSH keys. You can enter a token in its masked field or export `DIGITALOCEAN_ACCESS_TOKEN` before launch. Servestead uses the token for the current provider action and does not save it in the profile.
