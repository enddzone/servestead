---
title: Observability
description: Verify the remote Beszel, Dozzle, and Dockhand services and understand their Git-backed configuration.
---

The Observability stage deploys a private operations suite behind Pangolin SSO. None of these services publishes a direct host port.

## Verify the stage

After a Platform run:

1. Return to the profile dashboard and confirm Platform is complete.
2. Press `h`, open the latest Platform run, and verify the Observability stage completed.
3. Sign in through Pangolin with the saved administrator account.
4. Open each remote service hostname.

| Service | Default hostname | Purpose |
| --- | --- | --- |
| Beszel | `beszel.example.com` | Host metrics and system overview. |
| Dozzle | `dozzle.example.com` | Container log viewing. |
| Dockhand | `dockhand.example.com` | Git-backed stack visibility and Docker environment integration. |

Replace `example.com` with the profile's base domain.

### Expected result

Pangolin protects all three services with SSO, each target passes its health check, Beszel sees the local system, and Dockhand can list the server's containers.

## Where files live

| Path | Purpose |
| --- | --- |
| `/opt/servestead/repository` | Exact committed deployment input. |
| `/opt/servestead/stacks/observability` | Runtime data. |
| `/etc/servestead/observability.env` | Runtime secrets, mode `0600`. |

The consumer-owned configuration is `stacks/observability/compose.yaml` in the profile repository.

## Repository rules

Servestead deploys the exact committed `HEAD`. An uncommitted observability Compose change blocks deployment, while unrelated working-tree changes do not.

When the repository has a GitHub origin and branch, stack synchronization creates or updates matching Dockhand Git-stack records with automatic updates disabled. Servestead remains the authoritative deployer.

Use [GitOps review and sync](../gitops/) to inspect, commit, and push changes before rerunning Platform.

## GitHub personal access token

Private repositories require a GitHub PAT. Public repositories can use one to avoid anonymous rate limits. Press `g` from the profile dashboard to manage the saved token, or use:

```sh
./bin/servestead github-token set \
  --profile <profile-id> \
  --file ./github-token.txt
rm ./github-token.txt
./bin/servestead github-token status --profile <profile-id>
```

`SERVESTEAD_GITHUB_TOKEN` remains available as a one-run override. The environment value wins when both exist.

## If verification fails

- Check DNS and ports 80 and 443 for hostname or certificate failures.
- Press `h` and open the latest run for the exact Proxy or Observability task error.
- Review the repository when the committed revision or working tree is rejected.
- Save the existing Pangolin administrator credentials in the profile's advanced fields when a retry reports missing credentials.

Continue with [Common issues](../../troubleshooting/) for focused recovery steps.
