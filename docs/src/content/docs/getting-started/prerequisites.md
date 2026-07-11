---
title: Requirements
description: Prepare the local machine, Ubuntu server, SSH key, domain, and provider access Servestead needs.
---

Complete this checklist before starting a reviewed setup run.

<ul class="checklist">
  <li>A local machine with Go 1.26.5 or newer.</li>
  <li>A fresh Ubuntu 22.04 or newer VPS. Ubuntu 24.04 is the default DigitalOcean image.</li>
  <li>The server's public IPv4 address or hostname.</li>
  <li>An ED25519 SSH key pair with access to the server.</li>
  <li>A domain you control and an email address for Let's Encrypt.</li>
  <li>Access to create apex and wildcard DNS records.</li>
  <li>For provisioning: a DigitalOcean API token and an SSH public key in DigitalOcean.</li>
</ul>

:::caution[Use a fresh server]
Setup hardens SSH, changes firewall policy, installs packages and Docker, and deploys ingress and observability services. Do not use a server with workloads or data you are not prepared to replace.
:::

## Local Preflight

From the repository root, run:

```sh
bin/servestead doctor
```

The direct `bootstrap`, `harden`, `network`, and `keygen` commands do not require local Ansible, OpenSSH, or `ssh-keygen` binaries. The remote server still needs standard Ubuntu tools such as `apt`, `sudo`, `systemctl`, `curl`, `gpg`, and `iptables`.

## Browser Expectations

`servestead ui` starts a local web server on a loopback address and opens a tokenized URL in your browser. Keep the CLI process running while you use Servestead Web. The interface is not intended to be exposed over a LAN or the public internet.

If the browser cannot open automatically, launch with `--no-open` and copy the printed URL into a browser on the same machine.

## SSH Key

Generate the default Servestead key pair if you do not already have an ED25519 key:

```sh
bin/servestead keygen
```

The default private key is `$HOME/.ssh/servestead_ed25519`. Use the matching `.pub` file when registering the key with a cloud provider.

## DNS Records

Before the proxy can issue certificates and expose services, create:

| Hostname | Type | Value |
| --- | --- | --- |
| `example.com` | `A` | VPS public IPv4 |
| `*.example.com` | `A` | VPS public IPv4 |

Replace `example.com` with your domain. DNS changes remain outside Servestead, so keep your registrar or DNS provider available while you verify propagation.

## DigitalOcean Access

The browser form under **Profiles → New DigitalOcean profile** expects a token, Droplet name, region slug, size slug, image slug, and SSH key ID or fingerprint. The [Terminal UI provisioning path](../../reference/terminal-ui/#provision-a-digitalocean-vps) can load live catalog choices and prices when you prefer guided selection.
