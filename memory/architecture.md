# Phase 1 Architecture

## Commands

`servestead provision` creates one DigitalOcean Droplet and waits until its public IPv4 address is available. It does not bootstrap or harden automatically. This separation ensures that a successfully created, billable instance is clearly reported even if later remote configuration fails.

Supported provider and defaults:

| Provider | Credential environment variable | Region | Size | Image |
| --- | --- | --- | --- | --- |
| DigitalOcean | `DIGITALOCEAN_ACCESS_TOKEN` or `DIGITALOCEAN_TOKEN` | `nyc3` | `s-1vcpu-1gb` | `ubuntu-24-04-x64` |

All defaults can be overridden by CLI flags. Direct CLI provisioning uses an existing DigitalOcean SSH key ID or fingerprint. Guided TUI provisioning can read the local public key, match it to existing DigitalOcean keys, upload it if needed, show catalog pricing, create one Droplet, save cloud metadata on the local profile, and return to the setup dashboard. Tokens are read from the environment or masked TUI input and are not persisted.

`servestead bootstrap` uses the native Go SSH runner to connect to the target and execute the admin setup commands. It creates `servestead` by default, locks password authentication for that account, grants passwordless sudo, and installs an ED25519 authorized key.

## Security boundaries

- Bootstrap does not disable root SSH. That occurs after the tunnel path is available and verified.
- Initial SSH uses a native trust-on-first-use host key policy equivalent to `accept-new`: new host keys are written to `$HOME/.ssh/known_hosts`, and changed known host keys fail. For a high-assurance deployment, compare the server host fingerprint through the provider console before bootstrapping.
- Remote file content is base64-encoded into shell scripts before being decoded on the target.
- `bootstrap` replaces the new admin user's `authorized_keys` content. This is intentional for a newly created account; reconsider before supporting existing admin accounts.
- Provisioning creates billable infrastructure. Automated tests use local HTTP servers and must never call live provider APIs.

## Repository layout

- `main.go`: process entry point.
- `cli.go`: command parsing, validation, provider defaults, and credential lookup.
- `cloud.go`: thin `godo`-backed DigitalOcean provider wrapper for catalog, SSH keys, create, reboot, and destroy.
- `provision_tui.go`: DigitalOcean provisioning TUI screens and profile creation handoff.
- `profile_cloud.go`: saved-profile DigitalOcean restart/destroy actions.
- `bootstrap.go`: admin bootstrap remote command sequence.
- `remote.go`: native SSH client, known-host handling, shell quoting, and remote file writes.
- `resources/`: embedded deployment resources grouped by runtime area (`bootstrap`, `hardening`, `network`, `observability`, `proxy`, and `stacks`).
- `resource_renderer.go`: `go:embed` rendering bridge that applies existing shell, YAML, JSON, and apt command helpers to resource templates.
- `*_test.go`: provider contract, native command, key generation, and CLI tests.
- `README.md`: operator-facing build and usage instructions.

The remote runner intentionally uses `golang.org/x/crypto/ssh` instead of local OpenSSH or Ansible binaries. DigitalOcean provisioning uses the official `github.com/digitalocean/godo` client behind local wrapper interfaces so tests can use mocked HTTP servers or fake providers without live cloud calls.

## Resource embedding practice

Generated deployment files and substantial shell scripts should live under `resources/` and be embedded with Go's `embed.FS`. Keep the tree organized by owning runtime area so resources are easy to find: proxy Compose and Pangolin/Traefik configs under `resources/proxy/`, observability scaffolds under `resources/observability/`, Docker/UFW assets under `resources/network/`, hardening assets under `resources/hardening/`, bootstrap scripts under `resources/bootstrap/`, and generated stack overrides under `resources/stacks/`.

Resource templates should contain file and script structure. Go should keep validation, dynamic payload construction, resource grouping, and quoting-sensitive helpers such as `shellQuote`, `yamlSingleQuote`, `yamlDoubleQuote`, and `jsonString`. Existing functions such as `pangolinComposeFile` and `observabilityComposeFile` should remain thin render wrappers unless a wider package refactor is explicitly planned.

Do not embed user-owned application Compose files, runtime environment files, profile secrets, or configuration repository content. Those remain external operator data. When adding a new generated artifact, add it to `resources/resources.go`, render it through `resource_renderer.go`, and verify it through the existing task or Compose tests.

## External references checked

- DigitalOcean Droplet API: <https://docs.digitalocean.com/reference/api/reference/droplets/>
- DigitalOcean Regions, Sizes, Images, SSH Keys, and Droplet Actions APIs.
