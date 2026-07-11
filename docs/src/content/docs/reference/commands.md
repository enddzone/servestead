---
title: CLI Commands
description: Launch Servestead Web, run setup, and use direct commands for automation and recovery.
---

Run `./bin/servestead --help` or `./bin/servestead <command> --help` for the complete flag list.

## Build

```sh
mkdir -p bin
go build -o ./bin/servestead ./backend
```

## Servestead Web

```sh
./bin/servestead ui
./bin/servestead ui --no-open
./bin/servestead ui --addr 127.0.0.1:8080 --no-open
```

`ui` accepts only local loopback addresses. The default chooses a random available port and opens the tokenized session URL.

## Local Preflight

```sh
./bin/servestead doctor
```

## Generate an SSH Key

```sh
./bin/servestead keygen
```

## Guided Setup

```sh
./bin/servestead setup
./bin/servestead setup --ip 203.0.113.10
```

The first command opens the Terminal UI. The second begins an interactive profile-aware run for a known server address.

For a fully supplied scripted run:

```sh
./bin/servestead setup \
  --ip 203.0.113.10 \
  --private-key "$HOME/.ssh/id_ed25519" \
  --domain example.com \
  --email admin@example.com \
  --yes
```

## Provision Directly

```sh
./bin/servestead provision \
  --provider digitalocean \
  --name production-vps \
  --ssh-key provider-key-id-or-fingerprint
```

Direct provisioning creates a billable Droplet and stops after reporting its public IPv4 address. It does not create a completed setup plan.

## Direct Setup Stages

```sh
./bin/servestead bootstrap \
  --host 203.0.113.10 \
  --admin-public-key "$HOME/.ssh/id_ed25519.pub" \
  --private-key "$HOME/.ssh/id_ed25519"
```

```sh
./bin/servestead harden \
  --host 203.0.113.10 \
  --private-key "$HOME/.ssh/id_ed25519"
```

```sh
./bin/servestead network \
  --host 203.0.113.10 \
  --private-key "$HOME/.ssh/id_ed25519"
```

```sh
./bin/servestead proxy \
  --host 203.0.113.10 \
  --private-key "$HOME/.ssh/id_ed25519" \
  --domain example.com \
  --email admin@example.com \
  --server-secret 'replace-with-a-long-random-secret'
```

Prefer a reviewed profile-aware setup unless a script intentionally manages each stage and secret.

## Profile Credentials

```sh
./bin/servestead pangolin-credentials --profile <profile-id>
./bin/servestead pangolin-credentials --ip 203.0.113.10
```

```sh
./bin/servestead github-token set --profile <profile-id> --file /path/to/token.txt
./bin/servestead github-token set --profile <profile-id> --from-env
./bin/servestead github-token status --profile <profile-id>
./bin/servestead github-token remove --profile <profile-id>
```

## Stack Management

```sh
./bin/servestead stack add \
  --profile <profile-id> \
  --compose /path/to/docker-compose.yml \
  --publish web:3000:app
```

```sh
./bin/servestead stack env set --profile <profile-id> --stack <name> --file /path/to/.env
./bin/servestead stack env remove --profile <profile-id> --stack <name>
```

## Stack Secret Recovery

```sh
./bin/servestead secrets init --profile <profile-id>
./bin/servestead secrets status --profile <profile-id>
./bin/servestead secrets export-key --profile <profile-id>
./bin/servestead secrets import-key --profile <profile-id> --file /path/to/stack-secret-key.txt
```

```sh
SOPS_AGE_KEY_FILE=/path/to/stack-secret-key.txt \
  sops -d stacks/<name>/servestead.secrets.yaml
```
