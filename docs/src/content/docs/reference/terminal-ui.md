---
title: Terminal UI
description: Use Servestead's full-screen workflow for profiles, DigitalOcean provisioning, setup stages, stacks, and run history.
---

The terminal UI is Servestead's primary interactive interface.

## Open the UI

```sh
./bin/servestead setup
```

The opening list shows saved profiles and setup paths. Use `j` and `k` or the arrow keys to move and `enter` to open an item. Press `q` to leave a navigation screen or `esc` to go back.

The footer changes by screen and is the authoritative key reference.

## Provision a DigitalOcean VPS

Choose **Provision a new DigitalOcean VPS**. The flow asks for a token, Droplet name, and local key, then loads:

- Regions.
- Sizes with CPU, memory, disk, hourly price, and monthly price.
- Supported Ubuntu images.
- Existing DigitalOcean SSH keys, with an option to upload the matching local public key.

The review requires the exact displayed confirmation phrase before creating a billable Droplet. Provisioning saves a profile and returns to its dashboard. It does not bootstrap or harden automatically.

Press `ctrl+c` while loading or creating to request cancellation. If DigitalOcean creation succeeds but saving the profile fails, the recovery action retries the local save only.

## Run profile stages

Saved-profile dashboards expose:

- **Bootstrap** creates and verifies administrative SSH access.
- **Harden** applies operating-system and SSH hardening.
- **Platform** runs networking, Pangolin proxy, and observability in order.

The TUI collects missing values and reviews the repository action before it starts remote execution.

## Dashboard keys

| Key | Action |
| --- | --- |
| `j` / `k` | Move through setup stages. |
| `r` | Run the selected stage once, including a completed stage. |
| `v` | Review the full setup plan. |
| `h` | Open persisted run history and redacted log detail. |
| `p` | Reveal or hide saved Pangolin access values. |
| `o` | Open DigitalOcean cloud actions. |
| `s` | Open the application stack manager. |
| `g` | Manage the saved GitHub token. |
| `e` | Edit normal profile values. |
| `a` | Edit advanced values. |
| `f` | Create a fresh local profile from the selected profile. |
| `x` | Review deletion of local profile files. |
| `page up` / `page down` | Scroll long dashboard content. |
| `esc` | Go back. |
| `q` | Quit. |

Local profile deletion requires `delete <profile-name>` exactly as displayed. It does not change the server or cloud resource, and active runs block it.

## Stack manager keys

From a saved profile, press `s`.

| Key | Action |
| --- | --- |
| `a` | Pick a Compose file. |
| `/` | Enter a Compose path manually from the file picker. |
| `enter` / `space` | Configure or toggle a public route for the selected service. |
| `n` | Continue to runtime-secret choices. |
| `e` | Edit existing stack metadata. |
| `ctrl+s` | Save the current edit. |
| `r` | Deploy the selected committed stack. |
| `v` | View staged, unstaged, and untracked changes. |
| `g` | Stage managed changes below `stacks/`. |
| `c` | Commit staged stack changes. |
| `p` | Push the current branch when `origin` is configured. |
| `y` | Synchronize the committed repository with the server. |
| `d` | Review local stack removal. |
| `page up` / `page down` | Scroll long stack edit and review screens. |

Every Compose service deploys, but only explicitly configured services receive Pangolin public routes. Stack removal requires `delete <stack-name>` exactly as displayed. Commit that deletion and press `y` only when you intend to remove the managed remote resources.

## Run view and history

The live run view reports the stage, task, progress, and output. Press `ctrl+c` to request cancellation. Cancellation stops local preparation before SSH begins, but an active remote task may already be partially applied. Wait for the cancelling state to finish, then inspect the saved run and remote state before retrying. When you scroll away from the newest log line, Servestead pauses follow-tail. Press `end` to resume following new output.

Press `h` from the profile dashboard to open run history. Choose a run for status, timestamps, stage errors, the log path, and known-secret redacted task output. Servestead loads this view asynchronously and bounds it to the latest 500 saved events, with an on-screen notice when older or oversized data was omitted.

## Direct interactive setup

Start from a known server address without visiting the profile picker:

```sh
./bin/servestead setup --ip 203.0.113.10
```

This still collects missing values, shows the plan, and records the profile and run state. For fully supplied automation, see [CLI commands](../commands/).
