---
title: Install and launch
description: Build Servestead from source, run its preflight, and open the terminal UI.
---

## 1. Build the CLI

From the repository root:

```sh
mkdir -p bin
go build -o ./bin/servestead ./backend
```

Confirm the binary is available:

```sh
./bin/servestead --help
```

## 2. Run the preflight

```sh
./bin/servestead doctor
```

Resolve missing local requirements before connecting to a server.

## 3. Open the terminal UI

```sh
./bin/servestead setup
```

The first screen lists saved profiles followed by actions for DigitalOcean provisioning, a new server profile, and the direct legacy tools. Use `j` and `k` or the arrow keys to move, then press `enter`.

The footer changes with each screen. Follow it for available actions. `esc` returns to the previous screen, `q` exits from navigation screens, and `ctrl+c` requests cancellation while work is running.

### Expected result

An empty configuration root shows no saved profiles. A configured machine lists its profiles with the latest run status. Selecting a profile opens its stage dashboard.

Continue with [Connect an existing VPS](../existing-vps/) or [Provision with DigitalOcean](../provision-vps/).

:::note[Contributor checks]
Development tests and docs-site commands live in the repository README and `docs/README.md`. Operators do not need them for normal setup.
:::
