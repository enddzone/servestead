# Phase 1 Architecture

## Commands

`aegisnode provision` creates one VPS and waits until its public IPv4 address is available. It does not bootstrap or harden automatically. This separation ensures that a successfully created, billable instance is clearly reported even if later remote configuration fails.

Supported providers and defaults:

| Provider | Credential environment variable | Region | Size | Image |
| --- | --- | --- | --- | --- |
| Hetzner | `HETZNER_API_TOKEN` or `HCLOUD_TOKEN` | `fsn1` | `cx23` | `ubuntu-24.04` |
| DigitalOcean | `DIGITALOCEAN_ACCESS_TOKEN` or `DIGITALOCEAN_TOKEN` | `nyc3` | `s-1vcpu-1gb` | `ubuntu-24-04-x64` |

All defaults can be overridden by CLI flags. The cloud SSH key must already exist at the provider and is supplied by ID, name, or fingerprint as supported by that provider. Tokens are environment-only to avoid exposure in command arguments.

`aegisnode bootstrap` uses the native Go SSH runner to connect to the target and execute the admin setup commands. It creates `aegisadmin` by default, locks password authentication for that account, grants passwordless sudo, and installs an ED25519 authorized key.

## Security boundaries

- Bootstrap does not disable root SSH. That occurs after the tunnel path is available and verified.
- Initial SSH uses a native trust-on-first-use host key policy equivalent to `accept-new`: new host keys are written to `$HOME/.ssh/known_hosts`, and changed known host keys fail. For a high-assurance deployment, compare the server host fingerprint through the provider console before bootstrapping.
- Remote file content is base64-encoded into shell scripts before being decoded on the target.
- `bootstrap` replaces the new admin user's `authorized_keys` content. This is intentional for a newly created account; reconsider before supporting existing admin accounts.
- Provisioning creates billable infrastructure. Automated tests use local HTTP servers and must never call live provider APIs.

## Repository layout

- `main.go`: process entry point.
- `cli.go`: command parsing, validation, provider defaults, and credential lookup.
- `cloud.go`: minimal standard-library clients for the two provider APIs.
- `bootstrap.go`: admin bootstrap remote command sequence.
- `remote.go`: native SSH client, known-host handling, shell quoting, and remote file writes.
- `*_test.go`: provider contract, native command, key generation, and CLI tests.
- `README.md`: operator-facing build and usage instructions.

The remote runner intentionally uses `golang.org/x/crypto/ssh` instead of local OpenSSH or Ansible binaries. Provider API clients remain minimal standard-library HTTP clients; adding provider SDKs is not justified by the current command surface.

## External references checked

- Hetzner Cloud API: <https://docs.hetzner.cloud/reference/cloud>
- DigitalOcean Droplet API: <https://docs.digitalocean.com/products/droplets/reference/api/droplets/>
