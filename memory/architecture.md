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
- `resources/`: embedded deployment resources grouped by runtime area (`bootstrap`, `hardening`, `network`, `observability`, `proxy`, and `stacks`).
- `resource_renderer.go`: `go:embed` rendering bridge that applies existing shell, YAML, JSON, and apt command helpers to resource templates.
- `*_test.go`: provider contract, native command, key generation, and CLI tests.
- `README.md`: operator-facing build and usage instructions.

The remote runner intentionally uses `golang.org/x/crypto/ssh` instead of local OpenSSH or Ansible binaries. Provider API clients remain minimal standard-library HTTP clients; adding provider SDKs is not justified by the current command surface.

## Resource embedding practice

Generated deployment files and substantial shell scripts should live under `resources/` and be embedded with Go's `embed.FS`. Keep the tree organized by owning runtime area so resources are easy to find: proxy Compose and Pangolin/Traefik configs under `resources/proxy/`, observability scaffolds under `resources/observability/`, Docker/UFW assets under `resources/network/`, hardening assets under `resources/hardening/`, bootstrap scripts under `resources/bootstrap/`, and generated stack overrides under `resources/stacks/`.

Resource templates should contain file and script structure. Go should keep validation, dynamic payload construction, resource grouping, and quoting-sensitive helpers such as `shellQuote`, `yamlSingleQuote`, `yamlDoubleQuote`, and `jsonString`. Existing functions such as `pangolinComposeFile` and `observabilityComposeFile` should remain thin render wrappers unless a wider package refactor is explicitly planned.

Do not embed user-owned application Compose files, runtime environment files, profile secrets, or configuration repository content. Those remain external operator data. When adding a new generated artifact, add it to `resources/resources.go`, render it through `resource_renderer.go`, and verify it through the existing task or Compose tests.

## External references checked

- Hetzner Cloud API: <https://docs.hetzner.cloud/reference/cloud>
- DigitalOcean Droplet API: <https://docs.digitalocean.com/products/droplets/reference/api/droplets/>
