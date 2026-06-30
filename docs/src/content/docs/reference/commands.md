---
title: Command Reference
description: Common Servestead commands and when to use them.
---

This reference summarizes the commands most operators need. Run `bin/servestead --help` and `bin/servestead <command> --help` for the complete flag list.

## Build

```sh
go build -o bin/servestead .
```

## Local Preflight

```sh
bin/servestead doctor
```

## Generate SSH Key

```sh
bin/servestead keygen
```

## Provision

```sh
bin/servestead provision \
  --provider hetzner \
  --name aegis-01 \
  --ssh-key my-provider-key
```

```sh
bin/servestead provision \
  --provider digitalocean \
  --name aegis-01 \
  --ssh-key provider-key-id-or-fingerprint
```

## Guided Setup

```sh
bin/servestead setup --ip 203.0.113.10
```

```sh
bin/servestead setup
```

## Direct Stages

```sh
bin/servestead bootstrap \
  --host 203.0.113.10 \
  --admin-public-key "$HOME/.ssh/id_ed25519.pub" \
  --private-key "$HOME/.ssh/id_ed25519"
```

```sh
bin/servestead harden \
  --host 203.0.113.10 \
  --private-key "$HOME/.ssh/id_ed25519"
```

```sh
bin/servestead network \
  --host 203.0.113.10 \
  --private-key "$HOME/.ssh/id_ed25519"
```

```sh
bin/servestead proxy \
  --host 203.0.113.10 \
  --private-key "$HOME/.ssh/id_ed25519" \
  --domain example.com \
  --email admin@example.com \
  --server-secret 'replace-with-a-long-random-secret'
```

## Credentials

```sh
bin/servestead pangolin-credentials --profile <profile-id>
bin/servestead pangolin-credentials --ip 203.0.113.10
```

## Stack Management

```sh
bin/servestead stack add \
  --profile <profile-id> \
  --compose /path/to/docker-compose.yml \
  --publish web:3000:app
```

```sh
bin/servestead stack env set --profile <profile-id> --stack <name> --file /path/to/.env
bin/servestead stack env remove --profile <profile-id> --stack <name>
```
