---
title: Common issues
description: Diagnose terminal setup runs, SSH, DNS, Git state, credentials, cloud actions, and stack reconciliation.
---

Start with the exact failed run and stage. Select the profile, press `h`, and open the newest matching run before changing server or repository state.

## The terminal UI does not start

`servestead setup` needs an interactive terminal. In scripts or a non-interactive shell, supply the target and all required values:

```sh
./bin/servestead setup \
  --ip 203.0.113.10 \
  --private-key "$HOME/.ssh/id_ed25519" \
  --domain example.com \
  --email admin@example.com \
  --yes
```

If the layout is too small, enlarge the terminal. Long tables and the run log work best at 80 columns by 24 rows or larger.

## Setup is waiting, cancelling, or failed

Open the latest run and identify whether the stop happened during preflight, repository preparation, SSH, or a named setup stage.

- Fix the reported profile or repository value before retrying.
- Save existing Pangolin credentials in the advanced profile fields when prompted.
- Press `ctrl+c` once to request cancellation, then wait for the cancelling state to finish. Repeatedly terminating the process can hide the final task result. If SSH work had started, assume the active task may be partially applied and inspect both the saved run and remote state before retrying.
- Rerun the selected stage after fixing its condition. Completed stages remain recorded.

## SSH fails on first connect

Check that:

- The address and initial SSH user are correct.
- The private key path exists on the machine running Servestead.
- The matching public key is registered with the provider or installed on the server.
- The server host-key fingerprint matches the provider console.

The first connection uses trust on first use. An unknown key is added to `$HOME/.ssh/known_hosts`, while a changed known key fails. Verify a changed fingerprint out of band before editing `known_hosts`.

## Bootstrap already ran

If root SSH was disabled by a prior run, resume the saved profile so Servestead uses the administrative account. If you intentionally need a new local profile for the same address:

```sh
./bin/servestead setup --ip 203.0.113.10 --fresh
```

When the source profile shows completed Bootstrap, the fresh profile preserves that access assumption and uses the saved administrative user.

## DNS or certificates do not work

Verify:

- Apex and wildcard `A` records point to the VPS IPv4 address.
- TCP port 80 is reachable for HTTP-01 challenges.
- TCP port 443 is reachable for HTTPS traffic.
- DNS propagation has reached the resolver you are testing.

DNS records remain outside Servestead. Use your registrar, DNS provider, and provider firewall tools to confirm the path.

## Platform retry needs Pangolin credentials

Select the profile, press `a`, and save the existing Pangolin administrator email and password. Then retry Platform.

You can also inspect saved credentials from the CLI:

```sh
./bin/servestead pangolin-credentials --ip 203.0.113.10
```

## Git state blocks deployment

Press `s` from the profile dashboard and follow the reported state:

1. Press `v` and review the working-tree changes.
2. Press `g` to stage intentional managed stack changes.
3. Press `c` and commit with a specific message.
4. Press `p` when the repository uses an origin and needs a push.
5. Deploy one stack with `r`, or press `y` to reconcile every committed stack.

An uncommitted observability Compose change blocks deployment. Unrelated working-tree changes do not.

## A stack is not eligible to deploy

Open the stack manager and confirm that:

- The Compose file parses and names the referenced services.
- Public-resource service names and ports are correct.
- Generated metadata is saved.
- Required repository changes are committed and pushed when required.

Use [Add an application stack](../guides/add-stack/) to review the expected sequence.

## A deleted stack still has resources

Local stack deletion changes the configuration repository only. Commit that deletion, then press `y` in the stack manager. Reconciliation removes managed containers, generated overrides, deployment manifests, and Pangolin resources for stacks no longer present in Git.

## Docker commands require `sudo`

Docker group membership applies to new login sessions. Disconnect and reconnect to the VPS before running:

```sh
docker ps
```

## Profile or secret recovery

Do not delete a local profile until you have exported any age identity required to decrypt stack secrets. See [Access and secrets](../guides/access-and-secrets/#back-up-the-age-identity).

Use `SERVESTEAD_CONFIG_DIR` when you need to test with an empty, isolated configuration root. This prevents test runs from loading real saved profiles or writing into their default repositories.
