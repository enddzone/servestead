---
title: Overview
description: Choose the right Servestead setup path before running commands.
---

Servestead, the Server Homestead, is a local Go CLI for turning a raw Ubuntu VPS into a hardened, Git-backed place to run private application stacks.

The safest way to learn it is to start with a fresh, disposable Ubuntu VPS and use the guided setup flow.

## Pick Your Starting Point

<div class="setup-path">
  <div class="path-card">
    <h3>Existing VPS</h3>
    <p>You already have a public IPv4 address and SSH key access. This is the best first path for most beginners.</p>
    <p><a href="/getting-started/existing-vps/">Set up an existing VPS</a></p>
  </div>
  <div class="path-card">
    <h3>New cloud server</h3>
    <p>You want Servestead to create a DigitalOcean VPS before setup. Provisioning is billable.</p>
    <p><a href="/getting-started/provision-vps/">Provision a VPS</a></p>
  </div>
</div>

## What Guided Setup Does

With one profile-aware run, Servestead can:

- Create or reuse a saved local profile.
- Collect the missing host, key, domain, email, and repository values before remote commands run.
- Bootstrap administrative SSH access.
- Harden the operating system and SSH.
- Install Docker and configure UFW networking.
- Deploy Pangolin, Traefik, Newt, and observability services.
- Save local run state and secrets with owner-only file permissions.

## What It Does Not Do

- It does not create DNS records at your registrar.
- It does not make live cloud API calls during tests.
- It does not store plaintext runtime secrets in the Git-backed configuration repository.
- It does not disable external SSH until the requested setup stages do so.
- It does not bootstrap or harden a newly provisioned Droplet until you explicitly run setup actions.

## Recommended First Run

```sh
bin/servestead setup --ip 203.0.113.10
```

Replace `203.0.113.10` with your server IPv4 address.

The interactive flow shows a task progress view and persists profile state so completed stages can be skipped on later full runs.
