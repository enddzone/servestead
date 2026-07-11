---
title: Observability
description: Verify the built-in Beszel, Dozzle, and Dockhand platform and understand its Git-backed configuration.
---

The observability stage deploys a private operations suite behind Pangolin SSO. None of these services publishes a direct host port.

## Verify the Stage

After a platform run:

1. Open **Profiles** and select the environment.
2. Confirm setup is complete and open the latest run.
3. Verify the observability stage completed without a recovery message.
4. Use the configured Pangolin administrator to open each hostname.

| Service | Default hostname | Purpose |
| --- | --- | --- |
| Beszel | `beszel.example.com` | Host metrics and system overview. |
| Dozzle | `dozzle.example.com` | Container log viewing. |
| Dockhand | `dockhand.example.com` | Git-backed stack visibility and Docker environment integration. |

Replace `example.com` with the profile's base domain.

### Expected Result

Pangolin protects all three services with SSO, each target passes its configured health check, Beszel sees the local system, and Dockhand can list the server's containers.

## Where Files Live

| Path | Purpose |
| --- | --- |
| `/opt/servestead/repository` | Exact committed deployment input. |
| `/opt/servestead/stacks/observability` | Runtime data. |
| `/etc/servestead/observability.env` | Runtime secrets, mode `0600`. |

The consumer-owned configuration is `stacks/observability/compose.yaml` in the profile repository.

## Repository Rules

Servestead deploys the exact committed `HEAD`. An uncommitted observability Compose change blocks deployment, while unrelated working-tree changes do not.

When the repository has a GitHub origin and branch, stack synchronization creates or updates matching Dockhand Git-stack records with automatic updates disabled. Servestead remains the authoritative deployer.

Use [GitOps review and sync](../gitops/) to inspect, commit, and push changes before rerunning the stage.

## GitHub Personal Access Token

Private repositories require a GitHub PAT. Public repositories can also use one to avoid anonymous rate limits.

Prefer a fine-grained token that is limited to the configuration repository, grants read-only `Contents` access, and has an expiration you can rotate. Save or replace it under **Profiles → Access**.

For a terminal-only workflow:

```sh
./bin/servestead github-token set \
  --profile <profile-id> \
  --file ./github-token.txt
rm ./github-token.txt
./bin/servestead github-token status --profile <profile-id>
```

`SERVESTEAD_GITHUB_TOKEN` remains available as a one-run override. When both exist, the environment value wins.

## If Verification Fails

- Check DNS and ports 80/443 for hostname or certificate failures.
- Open the latest run for the exact proxy or observability task error.
- Review GitOps when the committed revision or working tree is rejected.
- Save existing Pangolin credentials under **Access** when a retry reports missing credentials.

Continue with [Common issues](../../troubleshooting/) for focused recovery steps.
