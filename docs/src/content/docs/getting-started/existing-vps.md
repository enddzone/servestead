---
title: Use an Existing VPS
description: Run guided setup against a fresh Ubuntu server you already created.
---

Use this path when you already have a public IPv4 address for a fresh Ubuntu VPS.

:::caution[Use a disposable server first]
The setup path can harden SSH, alter firewall policy, install Docker, deploy reverse proxy services, and persist local profile secrets. Start with a server that does not contain important data.
:::

## 1. Build Servestead

```sh
go build -o bin/servestead .
```

## 2. Start Guided Setup

```sh
bin/servestead setup --ip 203.0.113.10
```

Replace `203.0.113.10` with your server IP.

With `--ip`, Servestead creates or selects a saved profile, collects missing values, generates and stores the Pangolin server secret, checks local prerequisites, then runs bootstrap, hardening, Docker networking, and reverse proxy deployment as one setup plan.

## 3. Provide Values When Prompted

The guided flow may ask for:

- Private key path.
- Domain name.
- Let's Encrypt email.
- Local configuration repository choice.
- Pangolin administrator values if rerunning after registration.

## 4. Review Before Remote Work

The TUI collects missing full-run values before any remote command runs. It prepares the configuration repository before starting SSH execution.

## 5. Rerun Safely

Saved profiles persist stage state. Later full runs skip previously completed stages and print explicit skip messages.

If you reuse an IP and want a new local profile, run:

```sh
bin/servestead setup --ip 203.0.113.10 --fresh
```

When a fresh profile is created from an already bootstrapped profile, Servestead treats administrative access as present and continues with the saved admin user.
