# Phase 1 Architecture

## Commands

`aegisnode provision` creates one VPS and waits until its public IPv4 address is available. It does not run Ansible automatically. This separation ensures that a successfully created, billable instance is clearly reported even if the local Ansible environment is unavailable.

Supported providers and defaults:

| Provider | Credential environment variable | Region | Size | Image |
| --- | --- | --- | --- | --- |
| Hetzner | `HETZNER_API_TOKEN` or `HCLOUD_TOKEN` | `fsn1` | `cx23` | `ubuntu-24.04` |
| DigitalOcean | `DIGITALOCEAN_ACCESS_TOKEN` or `DIGITALOCEAN_TOKEN` | `nyc3` | `s-1vcpu-1gb` | `ubuntu-24-04-x64` |

All defaults can be overridden by CLI flags. The cloud SSH key must already exist at the provider and is supplied by ID, name, or fingerprint as supported by that provider. Tokens are environment-only to avoid exposure in command arguments.

`aegisnode bootstrap` extracts the compiled `playbooks/bootstrap.yml` into a mode-`0600` temporary file, invokes the local `ansible-playbook`, and deletes the temporary directory afterward. It creates `aegisadmin` by default, locks password authentication for that account, grants passwordless sudo, and installs an ED25519 authorized key.

## Security boundaries

- Phase 1 does not disable root SSH. That occurs in Phase 5 after the tunnel path is available and verified.
- Initial SSH uses `StrictHostKeyChecking=accept-new`. For a high-assurance deployment, compare the server host fingerprint through the provider console before bootstrapping.
- The admin public key is passed to Ansible as JSON extra variables, not shell-interpolated text.
- The embedded playbook uses Ansible built-in modules only; no Galaxy collection is required.
- `bootstrap.yml` replaces the new admin user's `authorized_keys` content. This is intentional for a newly created account; reconsider before supporting existing admin accounts.
- Provisioning creates billable infrastructure. Automated tests use local HTTP servers and must never call live provider APIs.

## Repository layout

- `main.go`: process entry point.
- `cli.go`: command parsing, validation, provider defaults, and credential lookup.
- `cloud.go`: minimal standard-library clients for the two provider APIs.
- `bootstrap.go`: embedded playbook extraction and Ansible invocation.
- `playbooks/bootstrap.yml`: Phase 1 target configuration.
- `*_test.go`: provider contract, embedding, argument, and CLI tests.
- `README.md`: operator-facing build and usage instructions.

The implementation intentionally uses the Go standard library only. Adding a CLI framework or provider SDK is not justified by the current command surface.

## External references checked

- Hetzner Cloud API: <https://docs.hetzner.cloud/reference/cloud>
- DigitalOcean Droplet API: <https://docs.digitalocean.com/products/droplets/reference/api/droplets/>
