---
title: Access and secrets
description: Understand terminal credential controls, local secret storage, and stack encryption recovery.
---

Servestead separates profile credentials, encrypted application values, provider tokens, and remote service secrets.

## Pangolin access in the terminal UI

The profile dashboard reports Pangolin registration without showing saved credentials. Press `p` only when you need the initial setup URL or administrator username and password. The value remains visible in the terminal until you hide it or leave the screen.

If an existing Pangolin installation needs credentials for a Platform retry, press `a` and enter its administrator email and password in the masked advanced fields. On a saved profile, `ctrl+s` saves the update without starting a run.

You can also print saved credentials directly:

```sh
./bin/servestead pangolin-credentials --profile <profile-id>
```

## GitHub token

Press `g` from the profile dashboard. The token screen reports whether a profile token or `SERVESTEAD_GITHUB_TOKEN` is active.

- Paste a token and press `enter` to save it in the profile.
- Press `ctrl+e` to copy the current `SERVESTEAD_GITHUB_TOKEN` value into the profile.
- Press `ctrl+x` to remove the saved profile token.

Prefer a fine-grained token limited to the configuration repository with read-only Contents access. Give it an expiration you can rotate.

The direct alternatives are:

```sh
./bin/servestead github-token set --profile <profile-id> --file ./github-token.txt
./bin/servestead github-token status --profile <profile-id>
./bin/servestead github-token remove --profile <profile-id>
```

## Where secrets live

| Secret | Storage and use |
| --- | --- |
| GitHub PAT | Owner-only local profile secrets. Servestead sends it over SSH stdin for Git checkout when needed. |
| Pangolin administrator password | Owner-only local profile secrets. |
| Pangolin server and Newt credentials | Local profile secrets and remote platform configuration. |
| Application environment values | SOPS-compatible age-encrypted `servestead.secrets.yaml` in the configuration repository. |
| Observability environment values | Remote `/etc/servestead/observability.env`, mode `0600`. |
| DigitalOcean API token | Environment or masked field for the current action. It is not saved in the profile. |

Do not commit populated plaintext `.env` files.

## Import stack secrets directly

The terminal stack flow can import an environment file during add or edit. The direct commands are useful for repeatable updates:

```sh
./bin/servestead stack env set \
  --profile <profile-id> \
  --stack <name> \
  --file /path/to/.env
```

```sh
./bin/servestead stack env remove \
  --profile <profile-id> \
  --stack <name>
```

Servestead decrypts values locally for deployment, transmits them over SSH stdin for the remote task, and does not write a populated stack `.env` file on the server.

## Back up the age identity

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

:::caution[Terminal output can contain a revealed secret]
Servestead masks stored values and redacts known secrets from run history. An explicit credential reveal or direct credential command still prints the value. Clear scrollback or close the terminal when your environment requires it.
:::
