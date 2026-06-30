---
title: Guided Setup
description: Understand the profile-aware setup flow and when to rerun stages.
---

The guided setup flow is the easiest way to run Servestead. It keeps the local profile, run state, secrets, and logs together while still making every remote stage explicit.

## Start From an IP

```sh
bin/servestead setup --ip 203.0.113.10
```

This path creates or selects a saved profile and runs the full plan:

1. Bootstrap administrative access.
2. Harden the server.
3. Configure Docker networking and UFW.
4. Deploy the platform proxy and observability stack.

Interactive runs show task progress, current stage, current task, and inline logs. Scripted runs can use `--yes` when every value is already supplied.

## Start From the Dashboard

Run setup without `--ip` to open the profile-first terminal UI:

```sh
bin/servestead setup
```

The dashboard lists saved profiles and provides three setup actions:

- Bootstrap
- Harden
- Platform

Platform runs networking, Pangolin proxy, and observability in order.

## Profile Files

Saved profiles live below the directory returned by `os.UserConfigDir()` in a `servestead` subdirectory.

Each profile stores:

| File | Purpose |
| --- | --- |
| `profile.json` | Profile metadata. |
| `state.json` | Stage and run state. |
| `secrets.json` | Owner-only secrets such as generated Pangolin credentials. |
| `*.jsonl` logs | Structured run logs. |

Profile directories use owner-only permissions. Secrets are not committed to the configuration repository.

## Useful Dashboard Keys

| Key | Action |
| --- | --- |
| `j` / `k` | Select a stage or action. |
| `r` | Run the selected stage once, even if it is marked complete. |
| `v` | View the full plan review. |
| `p` | Reveal saved Pangolin administrator credentials. |
| `x` | Delete only the local saved profile, secrets, state, and logs. |
| `esc` | Go back. |
| `q` | Quit navigation or run screens. |

Deleting a local profile does not change the remote server.

## Non-Interactive Smoke Test

Provide every required value up front:

```sh
bin/servestead setup \
  --ip 203.0.113.10 \
  --private-key "$HOME/.ssh/id_ed25519" \
  --domain example.com \
  --email admin@example.com \
  --yes
```

Use this mode for repeatable smoke tests and scripts.
