---
title: CLI commands
description: Open the terminal UI, run setup, and use direct commands for automation and recovery.
---

Run `./bin/servestead --help` or `./bin/servestead <command> --help` for the complete flag list.

## Build

```sh
mkdir -p bin
go build -o ./bin/servestead ./backend
```

## Terminal UI

```sh
./bin/servestead setup
```

This is the primary interactive interface. It opens the profile picker, DigitalOcean provisioning, reviewed setup stages, stack management, and run history.

## Local preflight

```sh
./bin/servestead doctor
```

## Generate an SSH key

```sh
./bin/servestead keygen
```

## Start setup from an address

```sh
./bin/servestead setup --ip 203.0.113.10
```

This starts an interactive profile-aware run for the known server address. Use `--fresh` to create a separate local profile for an address that already has saved profiles.

For a fully supplied scripted run:

```sh
./bin/servestead setup \
  --ip 203.0.113.10 \
  --private-key "$HOME/.ssh/id_ed25519" \
  --domain example.com \
  --email admin@example.com \
  --yes
```

## Provision directly

```sh
./bin/servestead provision \
  --provider digitalocean \
  --name production-vps \
  --ssh-key provider-key-id-or-fingerprint
```

Direct provisioning creates a billable Droplet and stops after reporting the public IPv4 address. It does not create a profile-aware setup plan.

## Direct setup stages

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

Prefer the reviewed profile workflow unless a script intentionally manages each stage and secret.

## Profile credentials

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

## Stack management

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

## Stack secret recovery

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

## Isolated configuration root

Set `SERVESTEAD_CONFIG_DIR` to keep profiles and default configuration repositories below an explicit directory:

```sh
SERVESTEAD_CONFIG_DIR=/path/to/isolated-servestead ./bin/servestead setup
```

This is useful for disposable tests and separate operator environments. The directory becomes the Servestead root itself.
