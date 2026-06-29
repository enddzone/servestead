package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const usage = `Servestead provisions and hardens Ubuntu VPS instances.

Usage:
  servestead setup

Direct commands:
  servestead keygen
  servestead provision --provider <hetzner|digitalocean> --name <name> --ssh-key <provider-key>
  servestead bootstrap --host <ipv4> --admin-public-key <path> --private-key <path>
  servestead harden --host <ipv4> --private-key <path>
  servestead network --host <ipv4> --private-key <path>
  servestead proxy --host <ipv4> --private-key <path> --domain <domain> --email <email> --server-secret <secret>
  servestead pangolin-token (--profile <id> | --ip <ipv4>)
  servestead pangolin-credentials --profile <id>
  servestead stack add --profile <id> --compose <path> [--publish <service:port:subdomain[:id]> ...] [--env-file <path>]
  servestead stack env set --profile <id> --stack <name> --file <path>
  servestead stack env remove --profile <id> --stack <name>
  servestead doctor

Run "servestead <command> -help" for command-specific options.
`

type getenvFunc func(string) string

func run(ctx context.Context, args []string, stdout, stderr io.Writer, getenv getenvFunc) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return errors.New("a command is required")
	}

	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return nil
	case "provision":
		err := runProvision(ctx, args[1:], stdout, stderr, getenv)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	case "bootstrap":
		err := runBootstrap(ctx, args[1:], stdout, stderr)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	case "harden":
		err := runHarden(ctx, args[1:], stdout, stderr)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	case "network":
		err := runNetwork(ctx, args[1:], stdout, stderr)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	case "proxy":
		err := runProxy(ctx, args[1:], stdout, stderr)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	case "pangolin-token":
		err := runPangolinToken(args[1:], stdout, stderr)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	case "pangolin-credentials":
		err := runPangolinCredentials(args[1:], stdout, stderr)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	case "keygen":
		err := runKeygen(ctx, args[1:], stdout, stderr)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	case "stack":
		err := runStack(ctx, args[1:], stdout, stderr)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	case "setup":
		err := runSetup(ctx, args[1:], stdout, stderr)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	case "doctor":
		err := runDoctor(args[1:], stdout, stderr)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	default:
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runProvision(ctx context.Context, args []string, stdout, stderr io.Writer, getenv getenvFunc) error {
	flags := flag.NewFlagSet("provision", flag.ContinueOnError)
	flags.SetOutput(stderr)
	providerName := flags.String("provider", "", "cloud provider: hetzner or digitalocean")
	name := flags.String("name", "", "server name")
	region := flags.String("region", "", "provider region/location (provider default when omitted)")
	size := flags.String("size", "", "provider server size (provider default when omitted)")
	image := flags.String("image", "", "Ubuntu image slug (provider default when omitted)")
	sshKey := flags.String("ssh-key", "", "existing provider SSH key ID, name, or fingerprint; run keygen first if needed")
	timeout := flags.Duration("timeout", 5*time.Minute, "maximum time to wait for a public IPv4 address")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	if *name == "" || *sshKey == "" {
		return errors.New("--name and --ssh-key are required")
	}
	if *timeout <= 0 {
		return errors.New("--timeout must be greater than zero")
	}

	config := provisionConfig{
		Name:   *name,
		Region: *region,
		Size:   *size,
		Image:  *image,
		SSHKey: *sshKey,
	}

	var provider cloudProvider
	switch *providerName {
	case "hetzner":
		config.withDefaults("fsn1", "cx23", "ubuntu-24.04")
		token := firstNonEmpty(getenv("HETZNER_API_TOKEN"), getenv("HCLOUD_TOKEN"))
		if token == "" {
			return errors.New("HETZNER_API_TOKEN or HCLOUD_TOKEN is required")
		}
		provider = newHetznerProvider(http.DefaultClient, "https://api.hetzner.cloud/v1", token)
	case "digitalocean":
		config.withDefaults("nyc3", "s-1vcpu-1gb", "ubuntu-24-04-x64")
		token := firstNonEmpty(getenv("DIGITALOCEAN_ACCESS_TOKEN"), getenv("DIGITALOCEAN_TOKEN"))
		if token == "" {
			return errors.New("DIGITALOCEAN_ACCESS_TOKEN or DIGITALOCEAN_TOKEN is required")
		}
		provider = newDigitalOceanProvider(http.DefaultClient, "https://api.digitalocean.com/v2", token)
	case "":
		return errors.New("--provider is required")
	default:
		return fmt.Errorf("unsupported provider %q", *providerName)
	}

	waitCtx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	server, err := provider.Create(waitCtx, config)
	if err != nil {
		return fmt.Errorf("provision %s server: %w", *providerName, err)
	}
	fmt.Fprintf(stdout, "server created: provider=%s id=%s ipv4=%s\n", *providerName, server.ID, server.IPv4)
	return nil
}

func (config *provisionConfig) withDefaults(region, size, image string) {
	if config.Region == "" {
		config.Region = region
	}
	if config.Size == "" {
		config.Size = size
	}
	if config.Image == "" {
		config.Image = image
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

var linuxUsername = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
