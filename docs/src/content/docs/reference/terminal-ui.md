---
title: Terminal UI
description: Use Servestead's full-screen terminal workflow for guided DigitalOcean selection and keyboard-driven profile operations.
---

The Terminal UI remains useful when you want live DigitalOcean catalog choices, a typed provisioning confirmation, or a keyboard-driven workflow. Servestead Web is the recommended default for everyday setup, diagnostics, GitOps, access, and cloud operations.

## Open the Terminal UI

```sh
./bin/servestead setup
```

The initial screen lists saved profiles and setup paths. Press `q` to leave a navigation screen or `esc` to go back.

## Provision a DigitalOcean VPS

Choose **Provision a new DigitalOcean VPS**. The flow asks for a token, Droplet name, and local Servestead key, then loads:

- Regions.
- Sizes with CPU, memory, disk, hourly price, and monthly price.
- Supported Ubuntu images.
- Existing DigitalOcean SSH keys, with an option to upload the matching local public key.

The review screen requires the exact displayed confirmation phrase before creating the billable Droplet. Provisioning saves a profile and returns to its dashboard; it does not bootstrap or harden automatically.

## Run Profile Stages

Saved-profile dashboards expose:

- **Bootstrap** — create and verify administrative SSH access.
- **Harden** — apply operating-system and SSH hardening.
- **Platform** — run networking, Pangolin proxy, and observability in order.

The Terminal UI collects missing values and reviews the repository action before it starts remote execution.

## Dashboard Keys

| Key | Action |
| --- | --- |
| `j` / `k` | Move through stages or actions. |
| `r` | Run the selected stage once, including a completed stage. |
| `v` | Review the full setup plan. |
| `p` | Reveal saved Pangolin administrator credentials. |
| `o` | Open DigitalOcean cloud actions for a provisioned profile. |
| `s` | Open the application stack manager. |
| `x` | Delete only the local profile, secrets, state, and logs. |
| `esc` | Go back. |
| `q` | Quit the current navigation or run screen. |

Deleting a local profile does not change the server or cloud resource. Destroying a Droplet through cloud actions keeps the local profile and marks its cloud metadata as destroyed.

## Stack Manager Keys

From a saved profile, press `s`.

| Key | Action |
| --- | --- |
| `a` | Browse for a Compose file. |
| `/` | Enter a Compose path manually. |
| `enter` / `space` | Configure a public route for the selected service. |
| `n` | Choose no secrets, a detected `.env`, or another environment file. |
| `e` | Edit existing stack metadata. |
| `ctrl+s` | Save the current edit. |
| `r` | Deploy the selected committed stack. |
| `v` | View staged, unstaged, and untracked changes. |
| `g` | Stage managed changes below `stacks/`. |
| `c` | Commit staged stack changes. |
| `p` | Push the current branch when `origin` is configured. |
| `y` | Synchronize the committed repository with the server. |
| `d` | Remove a stack after confirmation. |

Every Compose service deploys, but only explicitly configured services receive Pangolin public routes.

## Direct Interactive Setup

Skip the profile dashboard and begin from a known server address:

```sh
./bin/servestead setup --ip 203.0.113.10
```

This still collects missing values, shows the plan, and records the profile and run state. For fully supplied automation, see [CLI commands](../commands/).
