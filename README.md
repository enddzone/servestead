# AegisNode

AegisNode is a local Go CLI for provisioning and hardening Ubuntu VPS instances. Phase 1 supports Hetzner and DigitalOcean provisioning plus administrative-user bootstrapping through an embedded Ansible playbook.

## Prerequisites

- Go 1.24 or newer to build the CLI
- `ansible-playbook` from ansible-core to run `bootstrap`
- An existing SSH key registered with the selected cloud provider
- A local ED25519 key pair for administrative access

## Build

```sh
go build -o bin/aegisnode .
```

## Provision a VPS

Credentials are read from the environment so they do not appear in shell process listings.

Hetzner:

```sh
export HETZNER_API_TOKEN='...'
bin/aegisnode provision \
  --provider hetzner \
  --name aegis-01 \
  --ssh-key my-provider-key
```

DigitalOcean:

```sh
export DIGITALOCEAN_ACCESS_TOKEN='...'
bin/aegisnode provision \
  --provider digitalocean \
  --name aegis-01 \
  --ssh-key 'provider-key-id-or-fingerprint'
```

Provider defaults target Ubuntu 24.04 and can be overridden with `--region`, `--size`, and `--image`. Provisioning is billable and is not run by the test suite.

## Bootstrap administrative access

```sh
bin/aegisnode bootstrap \
  --host 203.0.113.10 \
  --admin-public-key "$HOME/.ssh/id_ed25519.pub" \
  --private-key "$HOME/.ssh/id_ed25519"
```

The first SSH connection uses OpenSSH's `accept-new` policy. Verify the host fingerprint through the provider console before running the command when the threat model requires out-of-band host verification. Root SSH access is intentionally left enabled until Phase 5.
