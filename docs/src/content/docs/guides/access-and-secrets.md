---
title: Access and Secrets
description: Understand which credentials the browser manages, where secrets live, and how to handle stack encryption safely.
---

Servestead separates profile credentials, encrypted application values, and remote service secrets. Use the browser for status and deliberate profile updates; use the CLI for stack `.env` import and age-key recovery.

## Browser Access Workspace

Open **Profiles**, select a profile, and choose **Access**.

The workspace shows whether the GitHub PAT and Pangolin administrator password are configured without displaying either value. It can:

- Reveal the saved GitHub PAT or Pangolin password once in the current session.
- Update or remove the profile's GitHub PAT.
- Update the Pangolin administrator email and password.

Only reveal a value when you need to use it. Clear the browser view or close the local Servestead session when you are finished.

## Where Secrets Live

| Secret | Storage and use |
| --- | --- |
| GitHub PAT | Owner-only local profile secrets; sent over SSH stdin for Git checkout when needed. |
| Pangolin administrator password | Owner-only local profile secrets. |
| Pangolin server and Newt credentials | Local profile secrets and the remote platform configuration. |
| Application environment values | SOPS-compatible age-encrypted `servestead.secrets.yaml` in the configuration repository. |
| Observability environment values | Remote `/etc/servestead/observability.env`, mode `0600`. |

Do not commit populated plaintext `.env` files. Commit an `.env.example` when operators need to know the required variable names.

## Import Stack Secrets

The browser stack editor manages Compose and public-resource metadata, but it does not import a new `.env` file. Use the CLI:

```sh
./bin/servestead stack env set \
  --profile <profile-id> \
  --stack <name> \
  --file /path/to/.env
```

Remove a stack's encrypted environment values with:

```sh
./bin/servestead stack env remove \
  --profile <profile-id> \
  --stack <name>
```

Servestead decrypts values locally for deployment, transmits them over SSH stdin for the remote task, and does not write a populated stack `.env` file on the server.

## Back Up the Age Identity

The profile's age identity is required to recover encrypted stack values. Export it before deleting a profile or moving operation to another machine:

```sh
./bin/servestead secrets export-key --profile <profile-id>
```

Restore it with:

```sh
./bin/servestead secrets import-key \
  --profile <profile-id> \
  --file /path/to/stack-secret-key.txt
```

For recovery without Servestead:

```sh
SOPS_AGE_KEY_FILE=/path/to/stack-secret-key.txt \
  sops -d stacks/<name>/servestead.secrets.yaml
```

:::caution[The browser is local, not secret-free]
The Servestead UI is loopback-only and masks credentials by default, but an explicit reveal renders the value in the page. Treat the machine and active browser session as part of the trust boundary.
:::
