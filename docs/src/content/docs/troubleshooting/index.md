---
title: Common Issues
description: Troubleshoot common Servestead setup, DNS, proxy, Docker, and profile problems.
---

Start with the exact stage that failed. The guided run view and profile JSONL logs preserve the command output needed to diagnose most issues.

## SSH Fails on First Connect

Check:

- The server IP is correct.
- The private key path is correct.
- The matching public key is registered with the provider or installed on the server.
- The server host key fingerprint matches what you expect.

If the known host key changed, Servestead fails instead of silently replacing it. Confirm through the provider console before editing `known_hosts`.

## Bootstrap Already Ran

If root SSH was disabled by a previous run, create a fresh profile from an existing saved profile or use the saved administrative user.

```sh
bin/servestead setup --ip 203.0.113.10 --fresh
```

When a fresh profile is created from an existing bootstrapped profile, Servestead treats administrative access as already present.

## DNS or Certificates Do Not Work

Check:

- Apex and wildcard `A` records point at the VPS public IPv4.
- TCP port 80 is reachable for HTTP-01 challenges.
- TCP port 443 is reachable for HTTPS traffic.
- DNS propagation has reached the resolver you are testing from.

Use your registrar and DNS provider tools first. DNS records remain external to Servestead.

## Docker Commands Require sudo

Docker group membership applies to new login sessions. Disconnect and reconnect before running:

```sh
docker ps
```

## Platform Rerun Needs Pangolin Credentials

Retrying Platform after Pangolin has already been registered opens masked administrator email and password inputs. Supply the existing credentials so Servestead can save them in the owner-only profile secrets file.

You can retrieve saved credentials with:

```sh
bin/servestead pangolin-credentials --ip 203.0.113.10
```

## Stack Deployment Blocks on Git State

Servestead deploys committed configuration. Commit stack changes before deployment:

1. Press `v` in the stack manager to view diffs.
2. Press `g` to stage all changes under `stacks/`.
3. Press `c` to commit with a supplied message.
4. Press `p` to push when an origin remote is configured.
5. Press `y` to synchronize the committed repository with the server.

Uncommitted changes to the observability Compose file block deployment. Unrelated working-tree changes do not.

## A Deleted Stack Still Has Resources

Synchronize the committed repository with the server. Synchronization deploys current stacks and removes containers, generated overrides, deployment manifests, and Pangolin resources for stacks deleted from Git.
