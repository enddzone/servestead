---
title: Common Issues
description: Recover Servestead Web sessions, setup runs, SSH, DNS, GitOps, credentials, and stack state.
---

Start with the exact failed run and stage. Open **Profiles**, select the environment, choose **View history**, and inspect the newest matching run before changing server or repository state.

## Servestead Web Does Not Open

Launch without automatic browser opening:

```sh
./bin/servestead ui --no-open
```

Copy the complete printed URL into a browser on the same machine. If the page says `servestead ui token required`, use the newest tokenized URL from the currently running process. A URL from an earlier launch cannot authenticate a new session.

Make sure the CLI process is still running. Closing only the tab does not stop it, but closing the terminal or selecting **Shutdown** does.

## Setup Is Waiting or Failed

Open the latest run and identify whether the failure happened during preflight, repository preparation, SSH, or a named setup stage.

- Use the inline credential form when the recovery panel asks for Pangolin or GitHub values.
- Resolve repository review errors in GitOps before retrying.
- Select **Retry** only after the reported condition is fixed.
- Use **Cancel safely** instead of terminating the process when a run is active and the interface is responsive.

Completed stages remain recorded and can be skipped by a later full run.

## SSH Fails on First Connect

Check that:

- The address and initial SSH user are correct.
- The private key path exists on the machine running Servestead.
- The matching public key is registered with the provider or installed on the server.
- The server host-key fingerprint matches the provider console.

The first connection uses trust on first use: an unknown key is added to `$HOME/.ssh/known_hosts`, while a changed known key fails. Verify a changed fingerprint out of band before editing `known_hosts`.

## Bootstrap Already Ran

If root SSH was disabled by a prior run, resume the saved profile so Servestead uses the administrative account. If you intentionally need a new local profile for the same address:

```sh
./bin/servestead setup --ip 203.0.113.10 --fresh
```

When the source profile shows completed bootstrap, the fresh profile preserves that access assumption and uses the saved administrative user.

## DNS or Certificates Do Not Work

Verify:

- Apex and wildcard `A` records point to the VPS IPv4 address.
- TCP port 80 is reachable for HTTP-01 challenges.
- TCP port 443 is reachable for HTTPS traffic.
- DNS propagation has reached the resolver you are testing.

DNS records remain outside Servestead. Use your registrar, DNS provider, and provider firewall tools to confirm the path.

## Platform Retry Needs Pangolin Credentials

Open **Profiles → Access** and save the existing Pangolin administrator email and password, or use the inline recovery form on the failed run. Then retry the platform stage.

You can also inspect saved credentials from the CLI:

```sh
./bin/servestead pangolin-credentials --ip 203.0.113.10
```

## GitOps Blocks Deployment

Open **GitOps Review** and follow the displayed next action:

1. Expand **Review working tree changes**.
2. Stage intentional managed stack changes.
3. Commit with a specific message.
4. Push when the repository uses an origin.
5. Run stacks only when the repository state is intentional.

An uncommitted observability Compose change blocks deployment. Unrelated working-tree changes do not.

## A Stack Is Not Eligible to Deploy

Open **Profiles → Stacks** and check its metadata and Git columns. Confirm that:

- The Compose file parses and names the referenced services.
- Public-resource service names and ports are correct.
- The generated metadata is saved.
- Required repository changes are committed.

Use [Add an application stack](../guides/add-stack/) to review the expected sequence.

## A Deleted Stack Still Has Resources

Commit the deletion, then run the committed stack reconciliation from GitOps Review. Reconciliation removes managed containers, generated overrides, deployment manifests, and Pangolin resources for stacks no longer present in Git.

## Docker Commands Require `sudo`

Docker group membership applies to new login sessions. Disconnect and reconnect to the VPS before running:

```sh
docker ps
```

## Profile or Secret Recovery

Do not delete a local profile until you have exported any age identity required to decrypt stack secrets. See [Access and secrets](../guides/access-and-secrets/#back-up-the-age-identity).
